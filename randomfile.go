package objfs

import (
	"context"
	"errors"
	"io"
	"io/fs"
)

// randomFile is an [io/fs.File] that additionally implements [io.ReaderAt] and
// [io.Seeker] by issuing range reads against a [Bucket]. Remote backends return
// it from Open so that consumers such as archive/zip — which require an
// io.ReaderAt — can operate directly over a remote object without buffering it
// whole.
//
// ReadAt is stateless and therefore safe for concurrent use (as archive/zip
// requires). The sequential Read/Seek pair shares mutable state and, like
// *os.File, must not be used concurrently.
type randomFile struct {
	b     Bucket
	attrs Attributes

	off    int64         // logical offset for sequential Read/Seek
	body   io.ReadCloser // lazily opened stream for sequential Read
	bodyAt int64         // offset at which body currently sits
}

// NewRandomAccessFile returns an [io/fs.File] for the object described by attrs
// whose bytes are fetched on demand from b via [Bucket.GetRange]. The returned
// file also satisfies [io.ReaderAt] and [io.Seeker].
//
// attrs.Name and attrs.Size must be populated; Size bounds Seek and ReadAt.
func NewRandomAccessFile(b Bucket, attrs Attributes) fs.File {
	return &randomFile{b: b, attrs: attrs}
}

func (f *randomFile) Stat() (fs.FileInfo, error) { return NewFileInfo(f.attrs), nil }

// ReadAt implements [io.ReaderAt]. It is safe for concurrent use.
func (f *randomFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, &fs.PathError{Op: "readat", Path: f.attrs.Name, Err: fs.ErrInvalid}
	}
	if off >= f.attrs.Size {
		return 0, io.EOF
	}
	rc, err := f.b.GetRange(context.Background(), f.attrs.Name, off, int64(len(p)))
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	n, err := io.ReadFull(rc, p)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		// Fewer than len(p) bytes because we hit the end of the object.
		err = io.EOF
	}
	return n, err
}

// Read implements [io.Reader] over a lazily (re)opened sequential stream.
func (f *randomFile) Read(p []byte) (int, error) {
	if f.off >= f.attrs.Size {
		return 0, io.EOF
	}
	if f.body == nil || f.bodyAt != f.off {
		if f.body != nil {
			f.body.Close()
		}
		rc, err := f.b.GetRange(context.Background(), f.attrs.Name, f.off, -1)
		if err != nil {
			return 0, err
		}
		f.body, f.bodyAt = rc, f.off
	}
	n, err := f.body.Read(p)
	f.off += int64(n)
	f.bodyAt += int64(n)
	return n, err
}

// Seek implements [io.Seeker]. Seeking does not perform I/O; the next Read
// reopens the stream at the new offset.
func (f *randomFile) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = f.off + offset
	case io.SeekEnd:
		abs = f.attrs.Size + offset
	default:
		return 0, &fs.PathError{Op: "seek", Path: f.attrs.Name, Err: fs.ErrInvalid}
	}
	if abs < 0 {
		return 0, &fs.PathError{Op: "seek", Path: f.attrs.Name, Err: fs.ErrInvalid}
	}
	f.off = abs
	return abs, nil
}

func (f *randomFile) Close() error {
	if f.body != nil {
		return f.body.Close()
	}
	return nil
}
