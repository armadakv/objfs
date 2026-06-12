// Copyright Armada Contributors

package objfs_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/armadakv/objfs"
)

func newLocal(t *testing.T) *objfs.Local {
	t.Helper()
	b, err := objfs.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func upload(t *testing.T, b objfs.Bucket, name, body string) {
	t.Helper()
	if err := b.Upload(context.Background(), name, strings.NewReader(body)); err != nil {
		t.Fatalf("Upload(%q): %v", name, err)
	}
}

func TestLocalRoundTrip(t *testing.T) {
	b := newLocal(t)
	ctx := context.Background()

	tests := []struct {
		name string
		body string
	}{
		{"top.txt", "hello"},
		{"nested/deep/file.json", `{"k":"v"}`},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upload(t, b, tt.name, tt.body)

			rc, err := b.Get(ctx, tt.name)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			defer rc.Close()
			got, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if string(got) != tt.body {
				t.Errorf("body = %q, want %q", got, tt.body)
			}

			at, err := b.Stat(ctx, tt.name)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if at.Size != int64(len(tt.body)) {
				t.Errorf("Size = %d, want %d", at.Size, len(tt.body))
			}
		})
	}
}

func TestLocalGetRange(t *testing.T) {
	b := newLocal(t)
	ctx := context.Background()
	upload(t, b, "data", "0123456789")

	tests := []struct {
		off, length int64
		want        string
	}{
		{0, 4, "0123"},
		{3, 4, "3456"},
		{6, -1, "6789"},
		{0, -1, "0123456789"},
	}
	for _, tt := range tests {
		rc, err := b.GetRange(ctx, "data", tt.off, tt.length)
		if err != nil {
			t.Fatalf("GetRange(%d,%d): %v", tt.off, tt.length, err)
		}
		got, _ := io.ReadAll(rc)
		rc.Close()
		if string(got) != tt.want {
			t.Errorf("GetRange(%d,%d) = %q, want %q", tt.off, tt.length, got, tt.want)
		}
	}
}

func TestLocalNotExist(t *testing.T) {
	b := newLocal(t)
	ctx := context.Background()

	if _, err := b.Get(ctx, "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Get missing: error = %v, want fs.ErrNotExist", err)
	}
	ok, err := b.Exists(ctx, "missing")
	if err != nil || ok {
		t.Errorf("Exists missing = (%v, %v), want (false, nil)", ok, err)
	}
	// Deleting a missing object is not an error.
	if err := b.Delete(ctx, "missing"); err != nil {
		t.Errorf("Delete missing: %v", err)
	}
}

func TestLocalDelete(t *testing.T) {
	b := newLocal(t)
	ctx := context.Background()
	upload(t, b, "gone.txt", "bye")

	if err := b.Delete(ctx, "gone.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ok, _ := b.Exists(ctx, "gone.txt"); ok {
		t.Error("object still exists after Delete")
	}
}

func TestLocalList(t *testing.T) {
	b := newLocal(t)
	upload(t, b, "logs/a.txt", "a")
	upload(t, b, "logs/b.txt", "b")
	upload(t, b, "other/c.txt", "c")

	var names []string
	err := b.List(context.Background(), "logs/", func(a objfs.Attributes) error {
		names = append(names, a.Name)
		return nil
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	slices.Sort(names)
	want := []string{"logs/a.txt", "logs/b.txt"}
	if !slices.Equal(names, want) {
		t.Errorf("List(logs/) = %v, want %v", names, want)
	}
}

func TestLocalListSkipAll(t *testing.T) {
	b := newLocal(t)
	upload(t, b, "x/1", "1")
	upload(t, b, "x/2", "2")

	var count int
	err := b.List(context.Background(), "", func(objfs.Attributes) error {
		count++
		return objfs.SkipAll
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if count != 1 {
		t.Errorf("callback ran %d times, want 1 (SkipAll)", count)
	}
}

func TestLocalEscapeRejected(t *testing.T) {
	b := newLocal(t)
	ctx := context.Background()
	for _, name := range []string{"../escape", "/abs", "a/../../b"} {
		if _, err := b.Get(ctx, name); err == nil {
			t.Errorf("Get(%q) succeeded, want rejection", name)
		}
	}
}

// TestLocalSatisfiesFS proves Local is a well-behaved io/fs.FS using the
// standard library's own conformance checker, and that the reusable fs helpers
// work against it.
func TestLocalSatisfiesFS(t *testing.T) {
	b := newLocal(t)
	upload(t, b, "a.txt", "alpha")
	upload(t, b, "sub/b.txt", "bravo")

	if err := fstest.TestFS(b, "a.txt", "sub/b.txt"); err != nil {
		t.Fatalf("fstest.TestFS: %v", err)
	}

	got, err := fs.ReadFile(b, "sub/b.txt")
	if err != nil {
		t.Fatalf("fs.ReadFile: %v", err)
	}
	if !bytes.Equal(got, []byte("bravo")) {
		t.Errorf("fs.ReadFile = %q, want %q", got, "bravo")
	}
}

func TestPresignUnsupported(t *testing.T) {
	b := newLocal(t)
	_, err := objfs.PresignedGet(context.Background(), b, "x", 0)
	if !errors.Is(err, objfs.ErrUnsupported) {
		t.Errorf("PresignedGet error = %v, want ErrUnsupported", err)
	}
}
