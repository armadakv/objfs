// Package azblob provides an Azure Blob Storage implementation of
// [objfs.Bucket], scoped to a single container.
//
// It lives in its own module so that the Azure SDK is only pulled into builds
// that actually use Azure Blob Storage:
//
//	import objfsaz "github.com/armadakv/objfs/azblob"
package azblob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"

	"github.com/armadakv/objfs"
)

// Bucket is an [objfs.Bucket] backed by a single Azure Blob Storage container.
type Bucket struct {
	client    *azblob.Client
	container string
}

var (
	_ objfs.Bucket    = (*Bucket)(nil)
	_ objfs.Presigner = (*Bucket)(nil)
	_ fs.SubFS        = (*Bucket)(nil)
	_ fs.ReadFileFS   = (*Bucket)(nil)
	_ fs.ReadDirFS    = (*Bucket)(nil)
)

// New wraps an already-configured *azblob.Client and scopes it to container.
// Presigned URLs require the client to have been created with a shared-key
// credential (see [OpenWithSharedKey]).
func New(client *azblob.Client, container string) *Bucket {
	return &Bucket{client: client, container: container}
}

// OpenWithSharedKey builds a shared-key authenticated client for accountName at
// the default blob endpoint and scopes it to container. This is the credential
// type required for [Bucket.PresignedURL].
func OpenWithSharedKey(accountName, accountKey, container string) (*Bucket, error) {
	cred, err := azblob.NewSharedKeyCredential(accountName, accountKey)
	if err != nil {
		return nil, fmt.Errorf("objfs/azblob: shared key: %w", err)
	}
	url := fmt.Sprintf("https://%s.blob.core.windows.net/", accountName)
	client, err := azblob.NewClientWithSharedKeyCredential(url, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("objfs/azblob: new client: %w", err)
	}
	return New(client, container), nil
}

func (b *Bucket) blob(name string) *blob.Client {
	return b.client.ServiceClient().NewContainerClient(b.container).NewBlobClient(name)
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

// ReadFile implements [io/fs.ReadFileFS] with a single download.
func (b *Bucket) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return objfs.ReadFile(context.Background(), b, name)
}

// Sub implements [io/fs.SubFS], returning a prefix-scoped Bucket.
func (b *Bucket) Sub(dir string) (fs.FS, error) { return objfs.Sub(b, dir) }

// ReadDir implements [io/fs.ReadDirFS] using a hierarchical (delimited) listing
// so only the immediate children of name are fetched (blob prefixes become
// directories).
func (b *Bucket) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	var prefix string
	if name != "." {
		prefix = name + "/"
	}
	opts := &container.ListBlobsHierarchyOptions{}
	if prefix != "" {
		opts.Prefix = &prefix
	}
	cc := b.client.ServiceClient().NewContainerClient(b.container)
	pager := cc.NewListBlobsHierarchyPager("/", opts)
	var entries []fs.DirEntry
	for pager.More() {
		page, err := pager.NextPage(context.Background())
		if err != nil {
			return nil, fmt.Errorf("objfs/azblob: readdir %q: %w", name, err)
		}
		for _, bp := range page.Segment.BlobPrefixes {
			entries = append(entries, fs.FileInfoToDirEntry(objfs.NewDirInfo(strings.TrimSuffix(deref(bp.Name), "/"))))
		}
		for _, item := range page.Segment.BlobItems {
			key := deref(item.Name)
			if key == prefix {
				continue // directory-marker blob
			}
			at := objfs.Attributes{Name: key}
			if p := item.Properties; p != nil {
				at.Size = deref(p.ContentLength)
				at.LastModified = deref(p.LastModified)
				at.ContentType = deref(p.ContentType)
				if p.ETag != nil {
					at.ETag = string(*p.ETag)
				}
			}
			entries = append(entries, fs.FileInfoToDirEntry(objfs.NewFileInfo(at)))
		}
	}
	objfs.SortDirEntries(entries)
	return entries, nil
}

// Get returns a reader over the whole object.
func (b *Bucket) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	resp, err := b.client.DownloadStream(ctx, b.container, name, nil)
	if err != nil {
		return nil, mapErr(name, err)
	}
	return resp.Body, nil
}

// GetRange returns a reader over [off, off+length); a negative length reads to
// the end of the object.
func (b *Bucket) GetRange(ctx context.Context, name string, off, length int64) (io.ReadCloser, error) {
	count := length
	if length < 0 {
		count = blob.CountToEnd
	}
	resp, err := b.client.DownloadStream(ctx, b.container, name, &azblob.DownloadStreamOptions{
		Range: blob.HTTPRange{Offset: off, Count: count},
	})
	if err != nil {
		return nil, mapErr(name, err)
	}
	return resp.Body, nil
}

// Upload stores r under name.
func (b *Bucket) Upload(ctx context.Context, name string, r io.Reader, opts ...objfs.UploadOption) error {
	o := objfs.ApplyUploadOptions(opts)
	up := &azblob.UploadStreamOptions{}
	if o.ContentType != "" || o.CacheControl != "" {
		up.HTTPHeaders = &blob.HTTPHeaders{}
		if o.ContentType != "" {
			up.HTTPHeaders.BlobContentType = &o.ContentType
		}
		if o.CacheControl != "" {
			up.HTTPHeaders.BlobCacheControl = &o.CacheControl
		}
	}
	if len(o.Metadata) > 0 {
		up.Metadata = toPtrMap(o.Metadata)
	}
	if _, err := b.client.UploadStream(ctx, b.container, name, r, up); err != nil {
		return fmt.Errorf("objfs/azblob: upload %q: %w", name, err)
	}
	return nil
}

// Delete removes name. Deleting a missing object is not an error.
func (b *Bucket) Delete(ctx context.Context, name string) error {
	_, err := b.client.DeleteBlob(ctx, b.container, name, nil)
	if err != nil && !bloberror.HasCode(err, bloberror.BlobNotFound) {
		return fmt.Errorf("objfs/azblob: delete %q: %w", name, err)
	}
	return nil
}

// Stat returns metadata about name.
func (b *Bucket) Stat(ctx context.Context, name string) (objfs.Attributes, error) {
	resp, err := b.blob(name).GetProperties(ctx, nil)
	if err != nil {
		return objfs.Attributes{}, mapErr(name, err)
	}
	var etag string
	if resp.ETag != nil {
		etag = string(*resp.ETag)
	}
	return objfs.Attributes{
		Name:         name,
		Size:         deref(resp.ContentLength),
		LastModified: deref(resp.LastModified),
		ContentType:  deref(resp.ContentType),
		ETag:         etag,
		Metadata:     fromPtrMap(resp.Metadata),
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

// List reports every blob whose name begins with prefix.
func (b *Bucket) List(ctx context.Context, prefix string, fn func(objfs.Attributes) error) error {
	opts := &azblob.ListBlobsFlatOptions{}
	if prefix != "" {
		opts.Prefix = &prefix
	}
	pager := b.client.NewListBlobsFlatPager(b.container, opts)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("objfs/azblob: list %q: %w", prefix, err)
		}
		for _, item := range page.Segment.BlobItems {
			at := objfs.Attributes{Name: deref(item.Name)}
			if p := item.Properties; p != nil {
				at.Size = deref(p.ContentLength)
				at.LastModified = deref(p.LastModified)
				at.ContentType = deref(p.ContentType)
				if p.ETag != nil {
					at.ETag = string(*p.ETag)
				}
			}
			cbErr := fn(at)
			if errors.Is(cbErr, objfs.SkipAll) {
				return nil
			}
			if cbErr != nil {
				return cbErr
			}
		}
	}
	return nil
}

// PresignedURL returns a time-limited SAS URL for op on name. The client must
// have been created with a shared-key credential (see [OpenWithSharedKey]).
func (b *Bucket) PresignedURL(_ context.Context, name string, op objfs.Operation, expiry time.Duration) (string, error) {
	var perms sas.BlobPermissions
	switch op {
	case objfs.OpGet:
		perms = sas.BlobPermissions{Read: true}
	case objfs.OpPut:
		perms = sas.BlobPermissions{Create: true, Write: true}
	default:
		return "", fmt.Errorf("objfs/azblob: presign: %w", objfs.ErrUnsupported)
	}
	url, err := b.blob(name).GetSASURL(perms, time.Now().Add(expiry), nil)
	if err != nil {
		return "", fmt.Errorf("objfs/azblob: presign %s %q: %w", op, name, err)
	}
	return url, nil
}

// Close is a no-op; the *azblob.Client manages its own transport.
func (b *Bucket) Close() error { return nil }

func mapErr(name string, err error) error {
	if bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound) {
		return fmt.Errorf("objfs/azblob: %q: %w", name, fs.ErrNotExist)
	}
	return fmt.Errorf("objfs/azblob: %q: %w", name, err)
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func toPtrMap(m map[string]string) map[string]*string {
	out := make(map[string]*string, len(m))
	for k, v := range m {
		out[k] = &v
	}
	return out
}

func fromPtrMap(m map[string]*string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = deref(v)
	}
	return out
}
