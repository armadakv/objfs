// Copyright Armada Contributors

package objfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Local is a [Bucket] backed by a directory on the local filesystem. It is part
// of the core module and depends only on the standard library, which makes it
// the natural choice for tests, local development and single-node deployments.
//
// All access is confined to the root directory via [os.Root]: object names that
// would escape the root (via "..", absolute paths or symlinks) are rejected.
// Local does not implement [Presigner]; presign helpers return [ErrUnsupported].
type Local struct {
	dir  string
	root *os.Root
}

// compile-time checks for the interfaces Local satisfies.
var (
	_ Bucket        = (*Local)(nil)
	_ fs.SubFS      = (*Local)(nil)
	_ fs.ReadFileFS = (*Local)(nil)
	_ fs.ReadDirFS  = (*Local)(nil)
)

// NewLocal opens (creating it if necessary) the directory dir and returns a
// [Bucket] rooted there.
func NewLocal(dir string) (*Local, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("objfs: create local root %q: %w", dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("objfs: open local root %q: %w", dir, err)
	}
	return &Local{dir: dir, root: root}, nil
}

func (l *Local) osPath(name string) (string, error) {
	if !fs.ValidPath(name) {
		return "", &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return filepath.FromSlash(name), nil
}

// Open implements [io/fs.FS]. The returned file is an *os.File, so directories
// support ReadDir and regular files support Seek.
func (l *Local) Open(name string) (fs.File, error) {
	p, err := l.osPath(name)
	if err != nil {
		return nil, err
	}
	return l.root.Open(p)
}

// Get returns a reader over the whole object.
func (l *Local) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := l.osPath(name)
	if err != nil {
		return nil, err
	}
	return l.root.Open(p)
}

// GetRange returns a reader over [off, off+length); a negative length reads to
// the end of the object.
func (l *Local) GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := l.osPath(name)
	if err != nil {
		return nil, err
	}
	f, err := l.root.Open(p)
	if err != nil {
		return nil, err
	}
	if off > 0 {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
	}
	if length < 0 {
		return f, nil
	}
	return sectionReadCloser{Reader: io.LimitReader(f, length), closer: f}, nil
}

// Upload writes r to name, creating parent directories as needed.
func (l *Local) Upload(ctx context.Context, name string, r io.Reader, _ ...UploadOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := l.osPath(name)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(p); dir != "." {
		if err := l.root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("objfs: mkdir for %q: %w", name, err)
		}
	}
	f, err := l.root.Create(p)
	if err != nil {
		return fmt.Errorf("objfs: create %q: %w", name, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return fmt.Errorf("objfs: write %q: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("objfs: close %q: %w", name, err)
	}
	return nil
}

// Delete removes name. Removing a missing object is not an error.
func (l *Local) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := l.osPath(name)
	if err != nil {
		return err
	}
	if err := l.root.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("objfs: delete %q: %w", name, err)
	}
	return nil
}

// Stat returns metadata about name.
func (l *Local) Stat(ctx context.Context, name string) (Attributes, error) {
	if err := ctx.Err(); err != nil {
		return Attributes{}, err
	}
	p, err := l.osPath(name)
	if err != nil {
		return Attributes{}, err
	}
	fi, err := l.root.Stat(p)
	if err != nil {
		return Attributes{}, err
	}
	return Attributes{
		Name:         name,
		Size:         fi.Size(),
		LastModified: fi.ModTime(),
		ContentType:  mime.TypeByExtension(path.Ext(name)),
	}, nil
}

// Exists reports whether name exists.
func (l *Local) Exists(ctx context.Context, name string) (bool, error) {
	_, err := l.Stat(ctx, name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// List walks the directory tree and reports every regular file whose name
// begins with prefix.
func (l *Local) List(ctx context.Context, prefix string, fn func(Attributes) error) error {
	return fs.WalkDir(l.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := filepath.ToSlash(p)
		if !strings.HasPrefix(name, prefix) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return fn(Attributes{
			Name:         name,
			Size:         fi.Size(),
			LastModified: fi.ModTime(),
			ContentType:  mime.TypeByExtension(path.Ext(name)),
		})
	})
}

// ReadFile implements [io/fs.ReadFileFS].
func (l *Local) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return ReadFile(context.Background(), l, name)
}

// Sub implements [io/fs.SubFS], returning a prefix-scoped Bucket.
func (l *Local) Sub(dir string) (fs.FS, error) { return Sub(l, dir) }

// ReadDir implements [io/fs.ReadDirFS], returning the immediate children of the
// directory name (real filesystem directories, so empty dirs are honoured).
func (l *Local) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	return fs.ReadDir(l.root.FS(), name)
}

// Close releases the underlying [os.Root] handle.
func (l *Local) Close() error { return l.root.Close() }

// sectionReadCloser couples a bounded Reader with the underlying file's Closer.
type sectionReadCloser struct {
	io.Reader
	closer io.Closer
}

func (s sectionReadCloser) Close() error { return s.closer.Close() }
