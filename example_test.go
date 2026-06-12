package objfs_test

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	"github.com/armadakv/objfs"
)

// Example shows the core workflow: upload, read back through the io/fs.FS
// surface, and check for the optional presign capability — all backend-agnostic.
func Example() {
	ctx := context.Background()

	// Any backend works here; the local one needs only the standard library.
	var bucket objfs.Bucket
	bucket, _ = objfs.NewLocal("/tmp/objfs-example")
	defer bucket.Close()

	_ = bucket.Upload(ctx, "greetings/hello.txt", strings.NewReader("hi"))

	// Read it back via the standard library — bucket is an io/fs.FS.
	data, _ := fs.ReadFile(bucket, "greetings/hello.txt")
	fmt.Printf("contents: %s\n", data)

	// Presigning is an optional capability. Local does not support it.
	if _, err := objfs.PresignedGet(ctx, bucket, "greetings/hello.txt", 0); err != nil {
		fmt.Println("presign:", err)
	}

	// Output:
	// contents: hi
	// presign: objfs: presign GET: objfs: unsupported operation
}
