// Copyright Armada Contributors

package objfs

import (
	"context"
	"io/fs"
	"slices"
	"strings"
)

// dirPrefix returns the listing prefix for a directory name. The root (".")
// maps to the empty prefix; "a/b" maps to "a/b/".
func dirPrefix(name string) string {
	if name == "." {
		return ""
	}
	return name + "/"
}

// SortDirEntries sorts entries by name, as [io/fs.ReadDir] requires. Backends
// call it before returning from ReadDir.
func SortDirEntries(entries []fs.DirEntry) {
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
}

// ReadDir lists the immediate children of the directory name within b,
// synthesising subdirectories from the "/" delimiter. It is the backend-
// agnostic implementation of [io/fs.ReadDirFS] built on [Bucket.List]; cloud
// backends override it with native delimiter listing for efficiency, but it is
// exported so it works against any Bucket.
//
// Object stores have no empty directories, so a name with no children yields an
// empty slice rather than an error.
func ReadDir(ctx context.Context, b Bucket, name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	prefix := dirPrefix(name)
	seen := make(map[string]bool)
	var entries []fs.DirEntry
	err := b.List(ctx, prefix, func(a Attributes) error {
		rest := strings.TrimPrefix(a.Name, prefix)
		if rest == "" {
			return nil // directory-marker object for the prefix itself
		}
		if child, _, isDir := strings.Cut(rest, "/"); isDir {
			if !seen[child] {
				seen[child] = true
				entries = append(entries, fs.FileInfoToDirEntry(NewDirInfo(prefix+child)))
			}
			return nil
		}
		entries = append(entries, fs.FileInfoToDirEntry(NewFileInfo(a)))
		return nil
	})
	if err != nil {
		return nil, err
	}
	SortDirEntries(entries)
	return entries, nil
}

// ReadDir implements [io/fs.ReadDirFS] for the prefix-scoped bucket, delegating
// to the parent's native implementation when available.
func (s *subBucket) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	if rd, ok := s.parent.(fs.ReadDirFS); ok {
		return rd.ReadDir(s.full(name))
	}
	return ReadDir(context.Background(), s, name)
}
