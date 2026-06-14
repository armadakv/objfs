// Copyright Armada Contributors

// Package azblob provides an Azure Blob Storage implementation of
// [objfs.Bucket], scoped to a single container.
//
// It lives in its own module so that the Azure SDK is only pulled into builds
// that actually use Azure Blob Storage:
//
//	import objfsaz "github.com/armadakv/objfs/azblob"
package azblob
