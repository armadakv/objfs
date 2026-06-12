// Copyright Armada Contributors

package objfs

import (
	"io"
	"io/fs"
	"path"
	"time"
)

// FileInfo adapts [Attributes] to [io/fs.FileInfo].
type FileInfo struct {
	attrs Attributes
	dir   bool
}

// NewFileInfo returns an [io/fs.FileInfo] describing a regular object.
func NewFileInfo(a Attributes) FileInfo { return FileInfo{attrs: a} }

// NewDirInfo returns an [io/fs.FileInfo] describing a synthetic directory at
// name. Object stores have no real directories; backends synthesise them from
// key prefixes so that [io/fs.WalkDir] works.
func NewDirInfo(name string) FileInfo {
	return FileInfo{attrs: Attributes{Name: name}, dir: true}
}

func (fi FileInfo) Name() string { return path.Base(fi.attrs.Name) }
func (fi FileInfo) Size() int64  { return fi.attrs.Size }

func (fi FileInfo) Mode() fs.FileMode {
	if fi.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}

func (fi FileInfo) ModTime() time.Time { return fi.attrs.LastModified }
func (fi FileInfo) IsDir() bool        { return fi.dir }

// Sys returns the underlying [Attributes].
func (fi FileInfo) Sys() any { return fi.attrs }

// Attributes returns the object metadata backing this FileInfo.
func (fi FileInfo) Attributes() Attributes { return fi.attrs }

// streamFile is an [io/fs.File] backed by an object's byte stream plus its
// known [Attributes]. Backends return it from Open. Its Stat never performs
// additional I/O — the attributes are captured at open time.
type streamFile struct {
	r     io.ReadCloser
	attrs Attributes
}

// NewFile returns an [io/fs.File] that reads from r and reports info from
// attrs. The returned file takes ownership of r and closes it on Close.
func NewFile(r io.ReadCloser, attrs Attributes) fs.File {
	return &streamFile{r: r, attrs: attrs}
}

func (f *streamFile) Stat() (fs.FileInfo, error) { return NewFileInfo(f.attrs), nil }
func (f *streamFile) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *streamFile) Close() error               { return f.r.Close() }
