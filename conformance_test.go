package objfs_test

import (
	"testing"

	"github.com/armadakv/objfs"
	"github.com/armadakv/objfs/objfstest"
)

// TestLocalConformance runs the shared backend conformance suite against the
// local backend. The cloud backends run the identical suite against emulators
// in their integration tests (build tag "integration").
func TestLocalConformance(t *testing.T) {
	b, err := objfs.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	objfstest.RunBucket(t, b, objfstest.Options{PresignGetHTTP: false})
}
