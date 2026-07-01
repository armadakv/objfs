# objfs

[![CI](https://github.com/armadakv/objfs/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/armadakv/objfs/actions/workflows/test.yml)
[![Lint](https://github.com/armadakv/objfs/actions/workflows/lint.yml/badge.svg)](https://github.com/armadakv/objfs/actions/workflows/lint.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/armadakv/objfs.svg)](https://pkg.go.dev/github.com/armadakv/objfs)
[![Go Reference](https://pkg.go.dev/badge/github.com/armadakv/objfs/s3.svg)](https://pkg.go.dev/github.com/armadakv/objfs/s3)
[![Go Reference](https://pkg.go.dev/badge/github.com/armadakv/objfs/gcs.svg)](https://pkg.go.dev/github.com/armadakv/objfs/gcs)
[![Go Reference](https://pkg.go.dev/badge/github.com/armadakv/objfs/azblob.svg)](https://pkg.go.dev/github.com/armadakv/objfs/azblob)

A lightweight object-storage abstraction for Go that is also an [`io/fs.FS`].

Inspired by [Thanos' `objstore`](https://github.com/thanos-io/objstore), `objfs`
exposes one small `Bucket` interface that every backend implements. Because
`Bucket` embeds `io/fs.FS`, a bucket drops straight into the standard library —
`fs.WalkDir`, `fs.ReadFile`, `http.FileServerFS`, `template.ParseFS` — while
still offering context-aware upload, delete, listing, and **presigned URLs**.

```go
type Bucket interface {
    fs.FS // Open(name) — stdlib compatibility (uses context.Background)

    Get(ctx, name) (io.ReadCloser, error)
    GetRange(ctx, name, off, length) (io.ReadCloser, error)
    Upload(ctx, name, r, ...UploadOption) error
    Delete(ctx, name) error
    Stat(ctx, name) (Attributes, error)
    Exists(ctx, name) (bool, error)
    List(ctx, prefix, func(Attributes) error) error
    io.Closer
}
```

Presigned URLs are an *optional capability* — backends that support them
implement `Presigner`; detect it with a type assertion or the `PresignedGet` /
`PresignedPut` helpers.

### `io/fs` extension interfaces

Every backend goes beyond the bare `fs.FS`:

- **`fs.ReadFileFS`** — `bucket.ReadFile(name)` reads an object in one shot (a
  single GET, skipping the Stat that `Open` performs); `fs.ReadFile` routes
  through it automatically.
- **`fs.SubFS`** — `objfs.Sub(bucket, "tenants/acme")` returns a *full*
  prefix-scoped `Bucket` (Upload/Delete/List/presign all scoped, List names
  stripped), richer than the Open-only view `fs.Sub` would give.
- **`fs.ReadDirFS`** — `bucket.ReadDir(dir)` lists the *immediate* children of a
  directory, synthesising subdirectories from the `/` delimiter. Cloud backends
  use native delimited listing (S3 `CommonPrefixes`, GCS `Delimiter`, Azure
  hierarchical listing) so `fs.WalkDir` descends a tree level-by-level instead of
  scanning every key. `objfs.ReadDir(ctx, b, dir)` provides the same over any
  `Bucket`.
- **`io.ReaderAt` + `io.Seeker`** — the file returned by `Open` supports random
  access, backed by range reads. This makes a remote object usable directly with
  `archive/zip`, range-serving, and other random-access readers without
  buffering it whole. `ReadAt` is safe for concurrent use.

```go
// Read a zip stored in any bucket without downloading it whole:
f, _ := bucket.Open("archive.zip")
at, _ := bucket.Stat(ctx, "archive.zip")
zr, _ := zip.NewReader(f.(io.ReaderAt), at.Size)
```

## Lightweight by design

The **core module** (`github.com/armadakv/objfs`) depends only on the
standard library and ships the local-filesystem backend. Each cloud backend is
a **separate module** under its own directory, so its heavy SDK is only pulled
into your build when you import it:

| Module | Import | Docs | Backend | Presign |
|---|---|---|---|---|
| `github.com/armadakv/objfs` | core + `NewLocal` | [pkg.go.dev](https://pkg.go.dev/github.com/armadakv/objfs) | local disk | — |
| `github.com/armadakv/objfs/s3` | `objfs/s3` | [pkg.go.dev](https://pkg.go.dev/github.com/armadakv/objfs/s3) | Amazon S3 / S3-compatible | ✅ |
| `github.com/armadakv/objfs/gcs` | `objfs/gcs` | [pkg.go.dev](https://pkg.go.dev/github.com/armadakv/objfs/gcs) | Google Cloud Storage | ✅ |
| `github.com/armadakv/objfs/azblob` | `objfs/azblob` | [pkg.go.dev](https://pkg.go.dev/github.com/armadakv/objfs/azblob) | Azure Blob Storage | ✅ |

```bash
go get github.com/armadakv/objfs          # core, stdlib only
go get github.com/armadakv/objfs/s3       # adds the AWS SDK
```

## Usage

```go
ctx := context.Background()

bucket, err := objfs.NewLocal("/var/data") // or s3.Open(...), gcs.Open(...), azblob.OpenWithSharedKey(...)
if err != nil {
    return err
}
defer bucket.Close()

// Upload.
if err := bucket.Upload(ctx, "reports/q3.pdf", r, objfs.WithContentType("application/pdf")); err != nil {
    return err
}

// Read back through the standard library — Bucket is an io/fs.FS.
data, err := fs.ReadFile(bucket, "reports/q3.pdf")
if err != nil {
    return err
}
fmt.Printf("read %d bytes\n", len(data))

// Stream a byte range ([off, off+length)).
rc, err := bucket.GetRange(ctx, "reports/q3.pdf", 0, 1024)
if err != nil {
    return err
}
defer rc.Close()

// List by prefix.
if err := bucket.List(ctx, "reports/", func(a objfs.Attributes) error {
    fmt.Println(a.Name, a.Size)
    return nil // return objfs.SkipAll to stop early
}); err != nil {
    return err
}

// Presigned URL (cloud backends only).
url, err := objfs.PresignedGet(ctx, bucket, "reports/q3.pdf", 15*time.Minute)
if errors.Is(err, objfs.ErrUnsupported) {
    // local backend and any backend without presigning support
} else if err != nil {
    return err
} else {
    fmt.Println(url)
}
```

### Backend constructors

```go
// S3 (and S3-compatible: MinIO, Cloudflare R2, ...)
import objfss3 "github.com/armadakv/objfs/s3"
s3Bucket, _ := objfss3.Open(ctx, "my-bucket")              // default AWS config chain
s3Bucket = objfss3.New(existingS3Client, "my-bucket")      // bring your own *s3.Client

// Google Cloud Storage
import objfsgcs "github.com/armadakv/objfs/gcs"
gcsBucket, _ := objfsgcs.Open(ctx, "my-bucket")            // Application Default Credentials

// Azure Blob Storage (shared key enables SAS presigning)
import objfsaz "github.com/armadakv/objfs/azblob"
azBucket, _ := objfsaz.OpenWithSharedKey(account, key, "my-container")
azBucket = objfsaz.New(existingAzureClient, "my-container") // BYO *azblob.Client
```

## Semantics

- **Names** are slash-separated keys satisfying `fs.ValidPath` (no leading
  slash, no `.`/`..`). This keeps every backend interchangeable with the
  `io/fs` helpers.
- **Not-found** errors wrap `fs.ErrNotExist`, so `errors.Is(err, fs.ErrNotExist)`
  works uniformly across backends.
- **Delete** of a missing object is a no-op (not an error), matching cloud
  object-store semantics.
- The **local** backend confines all access to its root via `os.Root` —
  traversal via `..`, absolute paths, or symlinks is rejected.

## Development

Each module is independent. The cloud modules use a `replace` directive
pointing at `../` so they build against your local core checkout:

```bash
go test ./...            # core + local backend
(cd s3 && go build ./...)
(cd gcs && go build ./...)
(cd azblob && go build ./...)
```

The local backend is verified against the standard library's own
`testing/fstest.TestFS` conformance checker.

### Testing

A single conformance suite (`objfstest.RunBucket`) exercises every backend
identically — round-trip, ranges, `ReadFile`, `List`, `ReadDir`, `Sub`,
`io.ReaderAt` (via a real `archive/zip` read), not-found semantics, delete, and
(where supported) a presigned GET fetched over HTTP.

- **Unit tests** (no Docker) run the suite against the local backend:
  ```bash
  go test ./...
  ```
- **Integration tests** run the same suite against real service APIs in
  containers — MinIO (S3), fake-gcs-server (GCS), and Azurite (Azure Blob) — via
  [testcontainers-go](https://golang.testcontainers.org/). They require a
  running Docker daemon and are gated behind the `integration` build tag:
  ```bash
  (cd s3 && go test -tags=integration ./...)
  (cd gcs && go test -tags=integration ./...)
  (cd azblob && go test -tags=integration ./...)
  ```
  testcontainers is a **test-only** dependency of each provider module, so it is
  never pulled into your application build.

[`io/fs.FS`]: https://pkg.go.dev/io/fs#FS
