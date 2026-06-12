// Copyright Armada Contributors

//go:build integration

// Integration tests run the shared objfstest conformance suite against
// fake-gcs-server in a container. They require Docker and are gated behind the
// "integration" build tag:
//
//	go test -tags=integration ./...
package gcs_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/api/option"

	objfsgcs "github.com/armadakv/objfs/gcs"
	"github.com/armadakv/objfs/objfstest"
)

func TestGCSIntegration(t *testing.T) {
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "fsouza/fake-gcs-server:1.49.2",
		ExposedPorts: []string{"4443/tcp"},
		WaitingFor:   wait.ForListeningPort("4443/tcp"),
		Cmd:          []string{"-scheme", "http", "-backend", "memory", "-port", "4443"},
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start fake-gcs-server: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "4443")
	if err != nil {
		t.Fatal(err)
	}
	hostPort := fmt.Sprintf("%s:%s", host, port.Port())
	base := "http://" + hostPort

	// Point the emulator at its own mapped host:port. externalUrl fixes the
	// links it mints; publicHost must match the dynamic port or the emulator
	// 404s media (object body) downloads even though the JSON API resolves them.
	cfg := fmt.Sprintf(`{"externalUrl": %q, "publicHost": %q}`, base, hostPort)
	upReq, _ := http.NewRequestWithContext(ctx, http.MethodPut, base+"/_internal/config", strings.NewReader(cfg))
	upReq.Header.Set("Content-Type", "application/json")
	if resp, err := http.DefaultClient.Do(upReq); err != nil {
		t.Fatalf("configure emulator: %v", err)
	} else {
		resp.Body.Close()
	}

	// STORAGE_EMULATOR_HOST makes the client route *both* JSON-API and media
	// (object body) requests at the emulator. WithEndpoint alone leaves media
	// reads pointing at production, which 404s.
	t.Setenv("STORAGE_EMULATOR_HOST", hostPort)
	client, err := storage.NewClient(ctx, option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("storage client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	const bucket = "objfs-test"
	if err := client.Bucket(bucket).Create(ctx, "test-project", nil); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	b := objfsgcs.New(client, bucket)
	t.Cleanup(func() { _ = b.Close() })

	// fake-gcs-server has no signing credentials, so skip the presigned-URL HTTP
	// check; presigning is unit-covered against the real GCS signer.
	objfstest.RunBucket(t, b, objfstest.Options{PresignGetHTTP: false})
}
