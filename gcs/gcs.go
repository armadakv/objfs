// Package gcs provides a Google Cloud Storage implementation of
// [objfs.Bucket].
//
// It lives in its own module so that the Google Cloud SDK is only pulled into
// builds that actually use GCS:
//
//	import objfsgcs "github.com/armadakv/objfs/gcs"
package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/armadakv/objfs"
)

// Bucket is an [objfs.Bucket] backed by a Google Cloud Storage bucket.
type Bucket struct {
	client *storage.Client
	bucket *storage.BucketHandle
	name   string
	owns   bool // whether Close should close client (true when created via Open)
}

var (
	_ objfs.Bucket    = (*Bucket)(nil)
	_ objfs.Presigner = (*Bucket)(nil)
	_ fs.SubFS        = (*Bucket)(nil)
	_ fs.ReadFileFS   = (*Bucket)(nil)
	_ fs.ReadDirFS    = (*Bucket)(nil)
)

// New wraps an already-configured *storage.Client. The Bucket does not take
// ownership of the client, so Close is a no-op; close the client yourself.
func New(client *storage.Client, bucket string) *Bucket {
	return &Bucket{client: client, bucket: client.Bucket(bucket), name: bucket}
}

// Open creates a *storage.Client from Application Default Credentials (or the
// given options) and returns a Bucket that owns it; Close shuts the client down.
func Open(ctx context.Context, bucket string, opts ...option.ClientOption) (*Bucket, error) {
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("objfs/gcs: new client: %w", err)
	}
	b := New(client, bucket)
	b.owns = true
	return b, nil
}

// Open implements [io/fs.FS].
func (b *Bucket) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	at, err := b.Stat(context.Background(), name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	// Random-access file: body is fetched lazily via GetRange, so the returned
	// file supports io.ReaderAt/io.Seeker (e.g. for archive/zip).
	return objfs.NewRandomAccessFile(b, at), nil
}

// ReadFile implements [io/fs.ReadFileFS] with a single read.
func (b *Bucket) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return objfs.ReadFile(context.Background(), b, name)
}

// Sub implements [io/fs.SubFS], returning a prefix-scoped Bucket.
func (b *Bucket) Sub(dir string) (fs.FS, error) { return objfs.Sub(b, dir) }

// ReadDir implements [io/fs.ReadDirFS] using a delimited query so only the
// immediate children of name are fetched (collapsed prefixes become
// directories).
func (b *Bucket) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	var prefix string
	if name != "." {
		prefix = name + "/"
	}
	it := b.bucket.Objects(context.Background(), &storage.Query{Prefix: prefix, Delimiter: "/"})
	var entries []fs.DirEntry
	for {
		a, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("objfs/gcs: readdir %q: %w", name, err)
		}
		if a.Prefix != "" {
			entries = append(entries, fs.FileInfoToDirEntry(objfs.NewDirInfo(strings.TrimSuffix(a.Prefix, "/"))))
			continue
		}
		if a.Name == prefix {
			continue // directory-marker object
		}
		entries = append(entries, fs.FileInfoToDirEntry(objfs.NewFileInfo(objfs.Attributes{
			Name:         a.Name,
			Size:         a.Size,
			LastModified: a.Updated,
			ContentType:  a.ContentType,
			ETag:         a.Etag,
		})))
	}
	objfs.SortDirEntries(entries)
	return entries, nil
}

// Get returns a reader over the whole object.
func (b *Bucket) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	r, err := b.bucket.Object(name).NewReader(ctx)
	if err != nil {
		return nil, mapErr(name, err)
	}
	return r, nil
}

// GetRange returns a reader over [off, off+length); a negative length reads to
// the end of the object.
func (b *Bucket) GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error) {
	r, err := b.bucket.Object(name).NewRangeReader(ctx, off, length)
	if err != nil {
		return nil, mapErr(name, err)
	}
	return r, nil
}

// Upload stores r under name.
func (b *Bucket) Upload(ctx context.Context, name string, r io.Reader, opts ...objfs.UploadOption) error {
	o := objfs.ApplyUploadOptions(opts)
	w := b.bucket.Object(name).NewWriter(ctx)
	w.ContentType = o.ContentType
	w.CacheControl = o.CacheControl
	if len(o.Metadata) > 0 {
		w.Metadata = o.Metadata
	}
	if _, err := io.Copy(w, r); err != nil {
		_ = w.Close()
		return fmt.Errorf("objfs/gcs: write %q: %w", name, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("objfs/gcs: upload %q: %w", name, err)
	}
	return nil
}

// Delete removes name. Deleting a missing object is not an error.
func (b *Bucket) Delete(ctx context.Context, name string) error {
	err := b.bucket.Object(name).Delete(ctx)
	if err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("objfs/gcs: delete %q: %w", name, err)
	}
	return nil
}

// Stat returns metadata about name.
func (b *Bucket) Stat(ctx context.Context, name string) (objfs.Attributes, error) {
	a, err := b.bucket.Object(name).Attrs(ctx)
	if err != nil {
		return objfs.Attributes{}, mapErr(name, err)
	}
	return objfs.Attributes{
		Name:         a.Name,
		Size:         a.Size,
		LastModified: a.Updated,
		ContentType:  a.ContentType,
		ETag:         a.Etag,
		Metadata:     a.Metadata,
	}, nil
}

// Exists reports whether name exists.
func (b *Bucket) Exists(ctx context.Context, name string) (bool, error) {
	_, err := b.Stat(ctx, name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// List reports every object whose name begins with prefix.
func (b *Bucket) List(ctx context.Context, prefix string, fn func(objfs.Attributes) error) error {
	it := b.bucket.Objects(ctx, &storage.Query{Prefix: prefix})
	for {
		a, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("objfs/gcs: list %q: %w", prefix, err)
		}
		cbErr := fn(objfs.Attributes{
			Name:         a.Name,
			Size:         a.Size,
			LastModified: a.Updated,
			ContentType:  a.ContentType,
			ETag:         a.Etag,
			Metadata:     a.Metadata,
		})
		if errors.Is(cbErr, objfs.SkipAll) {
			return nil
		}
		if cbErr != nil {
			return cbErr
		}
	}
}

// PresignedURL returns a V4-signed, time-limited URL for op on name. The
// underlying client must have been created with credentials capable of signing
// (a service-account key, or one resolved via the IAM SignBlob API).
func (b *Bucket) PresignedURL(_ context.Context, name string, op objfs.Operation, expiry time.Duration) (string, error) {
	var method string
	switch op {
	case objfs.OpGet:
		method = "GET"
	case objfs.OpPut:
		method = "PUT"
	default:
		return "", fmt.Errorf("objfs/gcs: presign: %w", objfs.ErrUnsupported)
	}
	url, err := b.bucket.SignedURL(name, &storage.SignedURLOptions{
		Method:  method,
		Expires: time.Now().Add(expiry),
		Scheme:  storage.SigningSchemeV4,
	})
	if err != nil {
		return "", fmt.Errorf("objfs/gcs: presign %s %q: %w", op, name, err)
	}
	return url, nil
}

// Close closes the underlying client if this Bucket created it.
func (b *Bucket) Close() error {
	if b.owns {
		return b.client.Close()
	}
	return nil
}

func mapErr(name string, err error) error {
	if errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("objfs/gcs: %q: %w", name, fs.ErrNotExist)
	}
	return fmt.Errorf("objfs/gcs: %q: %w", name, err)
}
