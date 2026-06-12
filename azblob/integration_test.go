// Copyright Armada Contributors

//go:build integration

// Integration tests run the shared objfstest conformance suite against Azurite
// (the Azure Storage emulator) in a container. They require Docker and are
// gated behind the "integration" build tag:
//
//	go test -tags=integration ./...
package azblob_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/azure/azurite"

	objfsaz "github.com/armadakv/objfs/azblob"
	"github.com/armadakv/objfs/objfstest"
)

func TestAzblobIntegration(t *testing.T) {
	ctx := context.Background()

	c, err := azurite.Run(ctx, "mcr.microsoft.com/azure-storage/azurite:3.33.0",
		azurite.WithInMemoryPersistence(64),
	)
	if err != nil {
		t.Fatalf("start azurite: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	blobURL, err := c.BlobServiceURL(ctx)
	if err != nil {
		t.Fatalf("blob service URL: %v", err)
	}
	// The SDK only recognises Azurite's path-style URL (first segment = account)
	// when the host is an IP literal; with "localhost" the SAS signer mis-parses
	// account/container/blob and Azurite returns 403. Force 127.0.0.1.
	blobURL = strings.Replace(blobURL, "localhost", "127.0.0.1", 1)
	// The SDK service URL must include the account path.
	serviceURL := blobURL + "/" + azurite.AccountName

	cred, err := azblob.NewSharedKeyCredential(azurite.AccountName, azurite.AccountKey)
	if err != nil {
		t.Fatalf("shared key: %v", err)
	}
	client, err := azblob.NewClientWithSharedKeyCredential(serviceURL, cred, nil)
	if err != nil {
		t.Fatalf("azblob client: %v", err)
	}

	const container = "objfs-test"
	if _, err := client.CreateContainer(ctx, container, nil); err != nil {
		t.Fatalf("create container: %v", err)
	}

	b := objfsaz.New(client, container)
	t.Cleanup(func() { _ = b.Close() })

	// Azurite validates account SAS, so the presigned-GET HTTP check applies.
	objfstest.RunBucket(t, b, objfstest.Options{PresignGetHTTP: true})
}
