// Copyright Armada Contributors

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
// their SDK dependencies are only pulled in when imported:
//
//	github.com/armadakv/objfs/s3      // Amazon S3 (and S3-compatible)
//	github.com/armadakv/objfs/gcs     // Google Cloud Storage
//	github.com/armadakv/objfs/azblob  // Azure Blob Storage
package objfs
