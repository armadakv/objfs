# Contributing to objfs

Thanks for contributing.

## Repository structure

This repository is multi-module:

- `.` (core + local backend)
- `s3/`
- `gcs/`
- `azblob/`

Each cloud module depends on the local root module via `replace ../`.

## Build and test

From the repository root:

```bash
go test ./...
(cd s3 && go test ./...)
(cd gcs && go test ./...)
(cd azblob && go test ./...)
```

Integration tests require Docker:

```bash
(cd s3 && go test -tags=integration ./...)
(cd gcs && go test -tags=integration ./...)
(cd azblob && go test -tags=integration ./...)
```

## Code quality

Use the existing Make targets when available:

```bash
make check
make test
make test-integration
make fmt
```

## Pull requests

- Keep changes focused and minimal.
- Update docs when behavior or APIs change.
- Include tests for behavioral changes.
