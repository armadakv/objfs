// Package objfstest provides a reusable conformance suite for [objfs.Bucket]
// implementations. The local unit tests and each cloud backend's integration
// tests all run the same suite, so every backend is exercised identically.
package objfstest

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/armadakv/objfs"
)

// Options tunes which optional behaviours the suite exercises for a backend.
type Options struct {
	// PresignGetHTTP, when true, asserts that a presigned GET URL can actually
	// be fetched over HTTP (true for MinIO/S3 and Azurite; emulators without
	// signing credentials, such as fake-gcs-server, should leave it false).
	PresignGetHTTP bool
}

// RunBucket runs the full conformance suite against b. The bucket must be empty
// at the start; the suite isolates its cases under distinct prefixes.
func RunBucket(t *testing.T, b objfs.Bucket, opts Options) {
	t.Helper()
	t.Run("FS", func(t *testing.T) { fstest.TestFS(b) })
	t.Run("RoundTrip", func(t *testing.T) { testRoundTrip(t, b) })
	t.Run("Range", func(t *testing.T) { testRange(t, b) })
	t.Run("ReadFile", func(t *testing.T) { testReadFile(t, b) })
	t.Run("List", func(t *testing.T) { testList(t, b) })
	t.Run("ReadDir", func(t *testing.T) { testReadDir(t, b) })
	t.Run("Sub", func(t *testing.T) { testSub(t, b) })
	t.Run("ReaderAtZip", func(t *testing.T) { testReaderAtZip(t, b) })
	t.Run("NotExist", func(t *testing.T) { testNotExist(t, b) })
	t.Run("Delete", func(t *testing.T) { testDelete(t, b) })
	if opts.PresignGetHTTP {
		t.Run("PresignGetHTTP", func(t *testing.T) { testPresignGetHTTP(t, b) })
	}
}

func put(t *testing.T, b objfs.Bucket, name, body string, opts ...objfs.UploadOption) {
	t.Helper()
	if err := b.Upload(context.Background(), name, strings.NewReader(body), opts...); err != nil {
		t.Fatalf("Upload(%q): %v", name, err)
	}
}

func getString(t *testing.T, b objfs.Bucket, name string) string {
	t.Helper()
	rc, err := b.Get(context.Background(), name)
	if err != nil {
		t.Fatalf("Get(%q): %v", name, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll(%q): %v", name, err)
	}
	return string(data)
}

func testRoundTrip(t *testing.T, b objfs.Bucket) {
	put(t, b, "rt/hello.txt", "hello world", objfs.WithContentType("text/plain"))
	if got := getString(t, b, "rt/hello.txt"); got != "hello world" {
		t.Errorf("Get = %q, want %q", got, "hello world")
	}
	at, err := b.Stat(context.Background(), "rt/hello.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if at.Size != 11 {
		t.Errorf("Stat.Size = %d, want 11", at.Size)
	}
	if ok, err := b.Exists(context.Background(), "rt/hello.txt"); err != nil || !ok {
		t.Errorf("Exists = (%v, %v), want (true, nil)", ok, err)
	}
}

func testRange(t *testing.T, b objfs.Bucket) {
	put(t, b, "range/data", "0123456789")
	cases := []struct {
		off, length int64
		want        string
	}{
		{0, 4, "0123"},
		{3, 4, "3456"},
		{6, -1, "6789"},
	}
	for _, c := range cases {
		rc, err := b.GetRange(context.Background(), "range/data", c.off, c.length)
		if err != nil {
			t.Fatalf("GetRange(%d,%d): %v", c.off, c.length, err)
		}
		got, _ := io.ReadAll(rc)
		rc.Close()
		if string(got) != c.want {
			t.Errorf("GetRange(%d,%d) = %q, want %q", c.off, c.length, got, c.want)
		}
	}
}

func testReadFile(t *testing.T, b objfs.Bucket) {
	put(t, b, "rf/doc.txt", "read me")
	got, err := fs.ReadFile(b, "rf/doc.txt")
	if err != nil {
		t.Fatalf("fs.ReadFile: %v", err)
	}
	if string(got) != "read me" {
		t.Errorf("fs.ReadFile = %q, want %q", got, "read me")
	}
}

func testList(t *testing.T, b objfs.Bucket) {
	put(t, b, "list/a.txt", "a")
	put(t, b, "list/b.txt", "b")
	put(t, b, "list/sub/c.txt", "c")

	var names []string
	err := b.List(context.Background(), "list/", func(a objfs.Attributes) error {
		names = append(names, a.Name)
		return nil
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	slices.Sort(names)
	want := []string{"list/a.txt", "list/b.txt", "list/sub/c.txt"}
	if !slices.Equal(names, want) {
		t.Errorf("List = %v, want %v", names, want)
	}
}

func testReadDir(t *testing.T, b objfs.Bucket) {
	put(t, b, "rd/top.txt", "t")
	put(t, b, "rd/dir/x.txt", "x")
	put(t, b, "rd/dir/y.txt", "y")

	entries, err := fs.ReadDir(b, "rd")
	if err != nil {
		t.Fatalf("ReadDir(rd): %v", err)
	}
	var got []string
	for _, e := range entries {
		if e.IsDir() {
			got = append(got, e.Name()+"/")
		} else {
			got = append(got, e.Name())
		}
	}
	slices.Sort(got)
	if want := []string{"dir/", "top.txt"}; !slices.Equal(got, want) {
		t.Errorf("ReadDir(rd) = %v, want %v", got, want)
	}
}

func testSub(t *testing.T, b objfs.Bucket) {
	put(t, b, "sub/team/a.txt", "A")
	sub, err := objfs.Sub(b, "sub/team")
	if err != nil {
		t.Fatalf("Sub: %v", err)
	}
	if got := getString(t, sub, "a.txt"); got != "A" {
		t.Errorf("sub Get a.txt = %q, want %q", got, "A")
	}
	put(t, sub, "b.txt", "B")
	if got := getString(t, b, "sub/team/b.txt"); got != "B" {
		t.Errorf("sub Upload landed at wrong path; parent reads %q", got)
	}
}

// testReaderAtZip proves the file returned by Open is a usable io.ReaderAt by
// reading a zip archive's central directory (which lives at the end of the file)
// straight from the object.
func testReaderAtZip(t *testing.T, b objfs.Bucket) {
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
	put(t, b, "zip/archive.zip", buf.String())

	f, err := b.Open("zip/archive.zip")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	ra, ok := f.(io.ReaderAt)
	if !ok {
		t.Fatalf("Open returned %T, which is not an io.ReaderAt", f)
	}
	zr, err := zip.NewReader(ra, int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
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

func testNotExist(t *testing.T, b objfs.Bucket) {
	_, err := b.Get(context.Background(), "nope/missing.txt")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Get missing: error = %v, want fs.ErrNotExist", err)
	}
	if ok, err := b.Exists(context.Background(), "nope/missing.txt"); err != nil || ok {
		t.Errorf("Exists missing = (%v, %v), want (false, nil)", ok, err)
	}
}

func testDelete(t *testing.T, b objfs.Bucket) {
	put(t, b, "del/gone.txt", "bye")
	if err := b.Delete(context.Background(), "del/gone.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ok, _ := b.Exists(context.Background(), "del/gone.txt"); ok {
		t.Error("object still exists after Delete")
	}
	// Deleting a missing object must not error.
	if err := b.Delete(context.Background(), "del/gone.txt"); err != nil {
		t.Errorf("Delete missing: %v", err)
	}
}

func testPresignGetHTTP(t *testing.T, b objfs.Bucket) {
	put(t, b, "presign/file.txt", "downloaded via presigned url")
	url, err := objfs.PresignedGet(context.Background(), b, "presign/file.txt", 5*time.Minute)
	if err != nil {
		t.Fatalf("PresignedGet: %v", err)
	}
	resp, err := http.Get(url) //nolint:gosec // URL is generated by the backend under test
	if err != nil {
		t.Fatalf("http.Get(presigned): %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presigned GET status = %d, want 200; body: %s", resp.StatusCode, got)
	}
	if string(got) != "downloaded via presigned url" {
		t.Errorf("presigned GET body = %q", got)
	}
}
