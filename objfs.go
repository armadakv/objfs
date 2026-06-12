// Package objfs is a lightweight object-storage abstraction that is also an
// [io/fs.FS].
//
// Inspired by Thanos' objstore, objfs exposes a small [Bucket] interface that
// every backend implements. Because [Bucket] embeds [io/fs.FS], any backend can
// be handed to the standard library — [io/fs.WalkDir], [io/fs.ReadFile],
// [net/http.FileServerFS], templates, and so on — while still offering
// context-aware upload, delete, listing and (optionally) presigned URLs.
//
// The core module depends only on the standard library and ships the local
// filesystem backend ([NewLocal]). Cloud backends live in opt-in submodules so
// their heavy SDK dependencies are only pulled in when used:
//
//	github.com/armadakv/objfs/s3      // Amazon S3 (and S3-compatible)
//	github.com/armadakv/objfs/gcs     // Google Cloud Storage
//	github.com/armadakv/objfs/azblob  // Azure Blob Storage
package objfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"time"
)

// ErrNotExist is returned when an object does not exist. It aliases
// [io/fs.ErrNotExist] so that errors.Is(err, fs.ErrNotExist) holds for every
// backend, keeping objfs interchangeable with the standard library.
var ErrNotExist = fs.ErrNotExist

// ErrUnsupported is returned by operations a backend does not implement, most
// commonly [Presigner.PresignedURL] on backends without signed-URL support.
var ErrUnsupported = errors.New("objfs: unsupported operation")

// Bucket is a read-write object store that doubles as an [io/fs.FS].
//
// Object names are always slash-separated paths satisfying [io/fs.ValidPath]
// (no leading slash, no "." or ".." elements, no empty segments). The reusable
// [io/fs] helpers — ReadFile, WalkDir, Glob, Sub — therefore work against any
// Bucket via the embedded FS.
//
// The context-free [io/fs.FS] Open exists for stdlib compatibility; prefer the
// context-aware methods (Get, Upload, ...) in application code.
type Bucket interface {
	// FS provides Open(name) and, where the backend supports it, the richer
	// fs.ReadDirFS / fs.StatFS / fs.SubFS behaviours. Open uses
	// context.Background internally.
	fs.FS

	// Get returns a reader for the whole object. The caller must Close it.
	// It returns an error wrapping ErrNotExist if name does not exist.
	Get(ctx context.Context, name string) (io.ReadCloser, error)

	// GetRange returns a reader for the half-open byte range [off, off+length).
	// A negative length reads to the end of the object.
	GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error)

	// Upload stores the contents of r under name, creating intermediate
	// "directories" as needed. Existing objects are overwritten.
	Upload(ctx context.Context, name string, r io.Reader, opts ...UploadOption) error

	// Delete removes name. Deleting a missing object is not an error.
	Delete(ctx context.Context, name string) error

	// Stat returns metadata about name without reading its body.
	Stat(ctx context.Context, name string) (Attributes, error)

	// Exists reports whether name exists.
	Exists(ctx context.Context, name string) (bool, error)

	// List calls fn once per object whose name begins with prefix. Iteration
	// stops, and the error is returned, if fn returns a non-nil error;
	// returning [SkipAll] stops iteration without error.
	List(ctx context.Context, prefix string, fn func(Attributes) error) error

	// Close releases resources held by the backend (connection pools, clients).
	io.Closer
}

// SkipAll is returned by a [Bucket.List] callback to stop iteration early
// without reporting an error. It mirrors [io/fs.SkipAll].
var SkipAll = fs.SkipAll

// Operation identifies the HTTP method a presigned URL authorises.
type Operation int

const (
	// OpGet authorises downloading (HTTP GET) an object.
	OpGet Operation = iota
	// OpPut authorises uploading (HTTP PUT) an object.
	OpPut
)

func (o Operation) String() string {
	switch o {
	case OpGet:
		return "GET"
	case OpPut:
		return "PUT"
	default:
		return "UNKNOWN"
	}
}

// Presigner is an optional capability for backends that can mint time-limited
// URLs granting direct access to an object without further authentication.
//
// Detect it with a type assertion, or use the package helpers [PresignedGet]
// and [PresignedPut]:
//
//	if p, ok := bucket.(objfs.Presigner); ok {
//		url, err := p.PresignedURL(ctx, "report.pdf", objfs.OpGet, time.Hour)
//	}
type Presigner interface {
	// PresignedURL returns a URL authorising op on name, valid for expiry.
	PresignedURL(ctx context.Context, name string, op Operation, expiry time.Duration) (string, error)
}

// Attributes describes a stored object.
type Attributes struct {
	// Name is the object's slash-separated key.
	Name string
	// Size is the object size in bytes.
	Size int64
	// LastModified is the last-modified time, if known.
	LastModified time.Time
	// ContentType is the stored MIME type, if any.
	ContentType string
	// ETag is the backend's entity tag, if any.
	ETag string
	// Metadata holds backend user metadata, if any.
	Metadata map[string]string
}

// UploadOptions configures a single [Bucket.Upload] call.
type UploadOptions struct {
	// ContentType sets the object's MIME type. When empty, backends may sniff
	// it from the name or content.
	ContentType string
	// CacheControl sets the Cache-Control header on backends that support it.
	CacheControl string
	// Metadata sets backend user metadata.
	Metadata map[string]string
}

// An UploadOption customises [UploadOptions].
type UploadOption func(*UploadOptions)

// WithContentType sets the object's MIME type.
func WithContentType(ct string) UploadOption {
	return func(o *UploadOptions) { o.ContentType = ct }
}

// WithCacheControl sets the Cache-Control header.
func WithCacheControl(cc string) UploadOption {
	return func(o *UploadOptions) { o.CacheControl = cc }
}

// WithMetadata sets backend user metadata. Repeated calls merge.
func WithMetadata(m map[string]string) UploadOption {
	return func(o *UploadOptions) {
		if o.Metadata == nil {
			o.Metadata = make(map[string]string, len(m))
		}
		maps.Copy(o.Metadata, m)
	}
}

// ApplyUploadOptions builds an [UploadOptions] from opts. Backends call this at
// the top of Upload.
func ApplyUploadOptions(opts []UploadOption) UploadOptions {
	var o UploadOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// PresignedGet is a convenience wrapper that returns a download URL for name,
// or an error wrapping [ErrUnsupported] if b is not a [Presigner].
func PresignedGet(ctx context.Context, b Bucket, name string, expiry time.Duration) (string, error) {
	return presign(ctx, b, name, OpGet, expiry)
}

// PresignedPut is a convenience wrapper that returns an upload URL for name,
// or an error wrapping [ErrUnsupported] if b is not a [Presigner].
func PresignedPut(ctx context.Context, b Bucket, name string, expiry time.Duration) (string, error) {
	return presign(ctx, b, name, OpPut, expiry)
}

func presign(ctx context.Context, b Bucket, name string, op Operation, expiry time.Duration) (string, error) {
	p, ok := b.(Presigner)
	if !ok {
		return "", fmt.Errorf("objfs: presign %s: %w", op, ErrUnsupported)
	}
	return p.PresignedURL(ctx, name, op, expiry)
}
