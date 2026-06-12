package objfs

import (
	"context"
	"io"
	"io/fs"
	"strings"
	"time"
)

// ReadFile reads the named object from b in full. Backends expose it as their
// [io/fs.ReadFileFS] implementation; it issues a single Get (no preliminary
// Stat), which is cheaper than the Open+ReadAll fallback on stores where Open
// stats first.
func ReadFile(ctx context.Context, b Bucket, name string) ([]byte, error) {
	rc, err := b.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// Sub returns a [Bucket] rooted at dir within b: every name passed to the
// returned bucket is prefixed with dir before reaching b, and names reported by
// List have the prefix stripped. dir must satisfy [io/fs.ValidPath].
//
// The result is a full Bucket — Upload, Delete and (when b supports it)
// presigning all operate within the sub-tree — which is richer than the
// Open-only view that [io/fs.Sub] would produce. Nested Subs are flattened.
func Sub(b Bucket, dir string) (Bucket, error) {
	if !fs.ValidPath(dir) {
		return nil, &fs.PathError{Op: "sub", Path: dir, Err: fs.ErrInvalid}
	}
	if dir == "." {
		return b, nil
	}
	if sb, ok := b.(*subBucket); ok {
		return &subBucket{parent: sb.parent, prefix: sb.prefix + dir + "/"}, nil
	}
	return &subBucket{parent: b, prefix: dir + "/"}, nil
}

// subBucket is a prefix-scoped view of another Bucket.
type subBucket struct {
	parent Bucket
	prefix string // always ends in "/"
}

var (
	_ Bucket        = (*subBucket)(nil)
	_ Presigner     = (*subBucket)(nil)
	_ fs.SubFS      = (*subBucket)(nil)
	_ fs.ReadFileFS = (*subBucket)(nil)
	_ fs.ReadDirFS  = (*subBucket)(nil)
)

// full maps a name in the sub-tree to its full name in the parent.
func (s *subBucket) full(name string) string {
	if name == "." {
		return strings.TrimSuffix(s.prefix, "/")
	}
	return s.prefix + name
}

func (s *subBucket) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return s.parent.Open(s.full(name))
}

func (s *subBucket) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	return s.parent.Get(ctx, s.full(name))
}

func (s *subBucket) GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error) {
	return s.parent.GetRange(ctx, s.full(name), off, length)
}

func (s *subBucket) Upload(ctx context.Context, name string, r io.Reader, opts ...UploadOption) error {
	return s.parent.Upload(ctx, s.full(name), r, opts...)
}

func (s *subBucket) Delete(ctx context.Context, name string) error {
	return s.parent.Delete(ctx, s.full(name))
}

func (s *subBucket) Stat(ctx context.Context, name string) (Attributes, error) {
	at, err := s.parent.Stat(ctx, s.full(name))
	if err != nil {
		return Attributes{}, err
	}
	at.Name = name
	return at, nil
}

func (s *subBucket) Exists(ctx context.Context, name string) (bool, error) {
	return s.parent.Exists(ctx, s.full(name))
}

func (s *subBucket) List(ctx context.Context, prefix string, fn func(Attributes) error) error {
	return s.parent.List(ctx, s.prefix+prefix, func(a Attributes) error {
		a.Name = strings.TrimPrefix(a.Name, s.prefix)
		return fn(a)
	})
}

func (s *subBucket) PresignedURL(ctx context.Context, name string, op Operation, expiry time.Duration) (string, error) {
	return presign(ctx, s.parent, s.full(name), op, expiry)
}

func (s *subBucket) Close() error { return nil }

// Sub implements [io/fs.SubFS], nesting another prefix.
func (s *subBucket) Sub(dir string) (fs.FS, error) { return Sub(s, dir) }

// ReadFile implements [io/fs.ReadFileFS].
func (s *subBucket) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return ReadFile(context.Background(), s, name)
}
