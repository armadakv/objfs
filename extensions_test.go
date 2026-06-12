package objfs_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"slices"
	"sync"
	"testing"

	"github.com/armadakv/objfs"
)

func TestReadFile(t *testing.T) {
	b := newLocal(t)
	upload(t, b, "doc.txt", "lorem ipsum")

	// Direct method (fs.ReadFileFS).
	got, err := b.ReadFile("doc.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "lorem ipsum" {
		t.Errorf("ReadFile = %q", got)
	}

	// Via the stdlib, which routes through fs.ReadFileFS.
	got, err = fs.ReadFile(b, "doc.txt")
	if err != nil {
		t.Fatalf("fs.ReadFile: %v", err)
	}
	if string(got) != "lorem ipsum" {
		t.Errorf("fs.ReadFile = %q", got)
	}

	if _, err := b.ReadFile("../escape"); err == nil {
		t.Error("ReadFile(../escape) succeeded, want error")
	}
}

func TestSub(t *testing.T) {
	root := newLocal(t)
	ctx := context.Background()
	upload(t, root, "tenants/acme/a.txt", "A")
	upload(t, root, "tenants/acme/b.txt", "B")
	upload(t, root, "tenants/other/c.txt", "C")

	sub, err := objfs.Sub(root, "tenants/acme")
	if err != nil {
		t.Fatalf("Sub: %v", err)
	}

	// Names inside the sub-tree are prefix-relative.
	data, err := fs.ReadFile(sub, "a.txt")
	if err != nil || string(data) != "A" {
		t.Fatalf("sub ReadFile a.txt = (%q, %v)", data, err)
	}

	// List is scoped and stripped.
	var names []string
	if err := sub.List(ctx, "", func(a objfs.Attributes) error {
		names = append(names, a.Name)
		return nil
	}); err != nil {
		t.Fatalf("sub.List: %v", err)
	}
	slices.Sort(names)
	if want := []string{"a.txt", "b.txt"}; !slices.Equal(names, want) {
		t.Errorf("sub.List = %v, want %v", names, want)
	}

	// Upload through the sub lands at the prefixed location in the parent.
	if err := sub.Upload(ctx, "d.txt", bytes.NewReader([]byte("D"))); err != nil {
		t.Fatalf("sub.Upload: %v", err)
	}
	if ok, _ := root.Exists(ctx, "tenants/acme/d.txt"); !ok {
		t.Error("sub.Upload did not write to the prefixed parent path")
	}

	// Nested Sub flattens.
	nested, err := objfs.Sub(sub, "deep")
	if err != nil {
		t.Fatalf("nested Sub: %v", err)
	}
	if err := nested.Upload(ctx, "x", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatalf("nested.Upload: %v", err)
	}
	if ok, _ := root.Exists(ctx, "tenants/acme/deep/x"); !ok {
		t.Error("nested sub path not flattened correctly")
	}
}

// dirNames renders entries as "name" for files and "name/" for directories.
func dirNames(entries []fs.DirEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		if e.IsDir() {
			out[i] = e.Name() + "/"
		} else {
			out[i] = e.Name()
		}
	}
	return out
}

func TestReadDir(t *testing.T) {
	b := newLocal(t)
	upload(t, b, "top.txt", "t")
	upload(t, b, "a/x.txt", "x")
	upload(t, b, "a/y.txt", "y")
	upload(t, b, "a/sub/z.txt", "z")

	tests := []struct {
		dir  string
		want []string
	}{
		{".", []string{"a/", "top.txt"}},
		{"a", []string{"sub/", "x.txt", "y.txt"}},
		{"a/sub", []string{"z.txt"}},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			// Native (os-backed) ReadDir.
			got, err := b.ReadDir(tt.dir)
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}
			if names := dirNames(got); !slices.Equal(names, tt.want) {
				t.Errorf("ReadDir(%q) = %v, want %v", tt.dir, names, tt.want)
			}
			// Generic prefix-synthesis path (the implementation cloud backends
			// share via objfs.ReadDir) must agree.
			gen, err := objfs.ReadDir(context.Background(), b, tt.dir)
			if err != nil {
				t.Fatalf("objfs.ReadDir: %v", err)
			}
			if names := dirNames(gen); !slices.Equal(names, tt.want) {
				t.Errorf("objfs.ReadDir(%q) = %v, want %v", tt.dir, names, tt.want)
			}
		})
	}
}

func TestWalkDir(t *testing.T) {
	b := newLocal(t)
	upload(t, b, "top.txt", "t")
	upload(t, b, "a/x.txt", "x")
	upload(t, b, "a/sub/z.txt", "z")

	var files []string
	err := fs.WalkDir(b, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
	slices.Sort(files)
	want := []string{"a/sub/z.txt", "a/x.txt", "top.txt"}
	if !slices.Equal(files, want) {
		t.Errorf("WalkDir files = %v, want %v", files, want)
	}
}

func TestSubReadDir(t *testing.T) {
	root := newLocal(t)
	upload(t, root, "tenants/acme/a.txt", "A")
	upload(t, root, "tenants/acme/logs/1.txt", "1")

	sub, err := objfs.Sub(root, "tenants/acme")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := sub.(fs.ReadDirFS).ReadDir(".")
	if err != nil {
		t.Fatalf("sub.ReadDir: %v", err)
	}
	if names := dirNames(entries); !slices.Equal(names, []string{"a.txt", "logs/"}) {
		t.Errorf("sub.ReadDir(.) = %v, want [a.txt logs/]", names)
	}
}

func TestRandomAccessFileSeek(t *testing.T) {
	b := newLocal(t)
	ctx := context.Background()
	upload(t, b, "data", "0123456789")
	at, err := b.Stat(ctx, "data")
	if err != nil {
		t.Fatal(err)
	}

	f := objfs.NewRandomAccessFile(b, at)
	defer f.Close()
	seeker := f.(io.Seeker)

	if _, err := seeker.Seek(3, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	got, _ := io.ReadAll(f)
	if string(got) != "3456789" {
		t.Errorf("after Seek(3): read %q, want %q", got, "3456789")
	}

	// SeekEnd.
	if _, err := seeker.Seek(-2, io.SeekEnd); err != nil {
		t.Fatalf("Seek end: %v", err)
	}
	got, _ = io.ReadAll(f)
	if string(got) != "89" {
		t.Errorf("after Seek(-2, end): read %q, want %q", got, "89")
	}
}

func TestRandomAccessFileReadAt(t *testing.T) {
	b := newLocal(t)
	ctx := context.Background()
	upload(t, b, "data", "0123456789")
	at, _ := b.Stat(ctx, "data")

	ra := objfs.NewRandomAccessFile(b, at).(io.ReaderAt)

	p := make([]byte, 4)
	n, err := ra.ReadAt(p, 2)
	if err != nil || n != 4 || string(p) != "2345" {
		t.Errorf("ReadAt(4,2) = (%d, %q, %v), want (4, \"2345\", nil)", n, p, err)
	}

	// Reading past the end returns the tail bytes and io.EOF.
	p = make([]byte, 5)
	n, err = ra.ReadAt(p, 8)
	if !errors.Is(err, io.EOF) || n != 2 || string(p[:n]) != "89" {
		t.Errorf("ReadAt at tail = (%d, %q, %v), want (2, \"89\", EOF)", n, p[:n], err)
	}
}

// TestRandomAccessFileConcurrentReadAt exercises the documented guarantee that
// ReadAt is safe for concurrent use (run under -race).
func TestRandomAccessFileConcurrentReadAt(t *testing.T) {
	b := newLocal(t)
	upload(t, b, "data", "0123456789")
	at, _ := b.Stat(context.Background(), "data")
	ra := objfs.NewRandomAccessFile(b, at).(io.ReaderAt)

	var wg sync.WaitGroup
	for off := range int64(8) {
		wg.Go(func() {
			p := make([]byte, 2)
			if _, err := ra.ReadAt(p, off); err != nil {
				t.Errorf("ReadAt(%d): %v", off, err)
			}
		})
	}
	wg.Wait()
}

// TestRandomAccessFileZip proves the io.ReaderAt surface is real: archive/zip
// requires a ReaderAt and reads the central directory from the end of the file.
func TestRandomAccessFileZip(t *testing.T) {
	b := newLocal(t)
	ctx := context.Background()

	// Build a zip in memory and store it as an object.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("inside.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("zipped payload")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	upload(t, b, "archive.zip", buf.String())

	at, err := b.Stat(ctx, "archive.zip")
	if err != nil {
		t.Fatal(err)
	}
	ra := objfs.NewRandomAccessFile(b, at).(io.ReaderAt)

	zr, err := zip.NewReader(ra, at.Size)
	if err != nil {
		t.Fatalf("zip.NewReader over bucket object: %v", err)
	}
	rc, err := zr.Open("inside.txt")
	if err != nil {
		t.Fatalf("open zip entry: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "zipped payload" {
		t.Errorf("zip entry = %q, want %q", got, "zipped payload")
	}
}
