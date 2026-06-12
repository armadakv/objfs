// Copyright Armada Contributors

// Package s3 provides an Amazon S3 (and S3-compatible, e.g. MinIO, R2)
// implementation of [objfs.Bucket].
//
// It lives in its own module so that the AWS SDK is only pulled into builds
// that actually use S3:
//
//	import objfss3 "github.com/armadakv/objfs/s3"
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/armadakv/objfs"
)

// Bucket is an [objfs.Bucket] backed by an Amazon S3 bucket.
type Bucket struct {
	client   *s3.Client
	presign  *s3.PresignClient
	uploader *manager.Uploader
	bucket   string
}

var (
	_ objfs.Bucket    = (*Bucket)(nil)
	_ objfs.Presigner = (*Bucket)(nil)
	_ fs.SubFS        = (*Bucket)(nil)
	_ fs.ReadFileFS   = (*Bucket)(nil)
	_ fs.ReadDirFS    = (*Bucket)(nil)
)

// New wraps an already-configured *s3.Client. Use this when you need control
// over region, credentials or a custom endpoint (MinIO, Cloudflare R2, ...).
func New(client *s3.Client, bucket string) *Bucket {
	return &Bucket{
		client:   client,
		presign:  s3.NewPresignClient(client),
		uploader: manager.NewUploader(client),
		bucket:   bucket,
	}
}

// Open loads the default AWS config (env, shared files, IMDS) and returns a
// Bucket. optFns are forwarded to config.LoadDefaultConfig.
func Open(ctx context.Context, bucket string, optFns ...func(*config.LoadOptions) error) (*Bucket, error) {
	cfg, err := config.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, fmt.Errorf("objfs/s3: load config: %w", err)
	}
	return New(s3.NewFromConfig(cfg), bucket), nil
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

// ReadFile implements [io/fs.ReadFileFS] with a single GetObject.
func (b *Bucket) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return objfs.ReadFile(context.Background(), b, name)
}

// Sub implements [io/fs.SubFS], returning a prefix-scoped Bucket.
func (b *Bucket) Sub(dir string) (fs.FS, error) { return objfs.Sub(b, dir) }

// ReadDir implements [io/fs.ReadDirFS] using a delimited ListObjectsV2 so only
// the immediate children of name are fetched (CommonPrefixes become
// directories).
func (b *Bucket) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	var prefix string
	if name != "." {
		prefix = name + "/"
	}
	p := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(b.bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})
	var entries []fs.DirEntry
	for p.HasMorePages() {
		page, err := p.NextPage(context.Background())
		if err != nil {
			return nil, fmt.Errorf("objfs/s3: readdir %q: %w", name, err)
		}
		for _, cp := range page.CommonPrefixes {
			dir := strings.TrimSuffix(aws.ToString(cp.Prefix), "/")
			entries = append(entries, fs.FileInfoToDirEntry(objfs.NewDirInfo(dir)))
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			if key == prefix {
				continue // directory-marker object
			}
			entries = append(entries, fs.FileInfoToDirEntry(objfs.NewFileInfo(objfs.Attributes{
				Name:         key,
				Size:         aws.ToInt64(obj.Size),
				LastModified: aws.ToTime(obj.LastModified),
				ETag:         aws.ToString(obj.ETag),
			})))
		}
	}
	objfs.SortDirEntries(entries)
	return entries, nil
}

// Get returns a reader over the whole object.
func (b *Bucket) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(name),
	})
	if err != nil {
		return nil, mapErr(name, err)
	}
	return out.Body, nil
}

// GetRange returns a reader over [off, off+length); a negative length reads to
// the end of the object.
func (b *Bucket) GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error) {
	var rng string
	if length < 0 {
		rng = fmt.Sprintf("bytes=%d-", off)
	} else {
		rng = fmt.Sprintf("bytes=%d-%d", off, off+length-1)
	}
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(name),
		Range:  aws.String(rng),
	})
	if err != nil {
		return nil, mapErr(name, err)
	}
	return out.Body, nil
}

// Upload stores r under name using a multipart-capable uploader.
func (b *Bucket) Upload(ctx context.Context, name string, r io.Reader, opts ...objfs.UploadOption) error {
	o := objfs.ApplyUploadOptions(opts)
	in := &s3.PutObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(name),
		Body:   r,
	}
	if o.ContentType != "" {
		in.ContentType = aws.String(o.ContentType)
	}
	if o.CacheControl != "" {
		in.CacheControl = aws.String(o.CacheControl)
	}
	if len(o.Metadata) > 0 {
		in.Metadata = o.Metadata
	}
	if _, err := b.uploader.Upload(ctx, in); err != nil {
		return fmt.Errorf("objfs/s3: upload %q: %w", name, err)
	}
	return nil
}

// Delete removes name. Deleting a missing object is not an error (S3 semantics).
func (b *Bucket) Delete(ctx context.Context, name string) error {
	_, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(name),
	})
	if err != nil {
		return fmt.Errorf("objfs/s3: delete %q: %w", name, err)
	}
	return nil
}

// Stat returns metadata about name via HeadObject.
func (b *Bucket) Stat(ctx context.Context, name string) (objfs.Attributes, error) {
	out, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(name),
	})
	if err != nil {
		return objfs.Attributes{}, mapErr(name, err)
	}
	return objfs.Attributes{
		Name:         name,
		Size:         aws.ToInt64(out.ContentLength),
		LastModified: aws.ToTime(out.LastModified),
		ContentType:  aws.ToString(out.ContentType),
		ETag:         aws.ToString(out.ETag),
		Metadata:     out.Metadata,
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

// List reports every object whose key begins with prefix.
func (b *Bucket) List(ctx context.Context, prefix string, fn func(objfs.Attributes) error) error {
	p := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(b.bucket),
		Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("objfs/s3: list %q: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			err := fn(objfs.Attributes{
				Name:         aws.ToString(obj.Key),
				Size:         aws.ToInt64(obj.Size),
				LastModified: aws.ToTime(obj.LastModified),
				ETag:         aws.ToString(obj.ETag),
			})
			if errors.Is(err, objfs.SkipAll) {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// PresignedURL returns a time-limited URL for op on name.
func (b *Bucket) PresignedURL(ctx context.Context, name string, op objfs.Operation, expiry time.Duration) (string, error) {
	withExpiry := func(o *s3.PresignOptions) { o.Expires = expiry }
	switch op {
	case objfs.OpGet:
		req, err := b.presign.PresignGetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(b.bucket),
			Key:    aws.String(name),
		}, withExpiry)
		if err != nil {
			return "", fmt.Errorf("objfs/s3: presign get %q: %w", name, err)
		}
		return req.URL, nil
	case objfs.OpPut:
		req, err := b.presign.PresignPutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(b.bucket),
			Key:    aws.String(name),
		}, withExpiry)
		if err != nil {
			return "", fmt.Errorf("objfs/s3: presign put %q: %w", name, err)
		}
		return req.URL, nil
	default:
		return "", fmt.Errorf("objfs/s3: presign: %w", objfs.ErrUnsupported)
	}
}

// Close is a no-op; the *s3.Client manages its own transport.
func (b *Bucket) Close() error { return nil }

// mapErr translates S3 "not found" errors into objfs.ErrNotExist so that
// errors.Is(err, fs.ErrNotExist) holds, and wraps everything else with context.
func mapErr(name string, err error) error {
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return fmt.Errorf("objfs/s3: %q: %w", name, fs.ErrNotExist)
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return fmt.Errorf("objfs/s3: %q: %w", name, fs.ErrNotExist)
		}
	}
	return fmt.Errorf("objfs/s3: %q: %w", name, err)
}
