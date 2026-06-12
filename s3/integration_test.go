// Copyright Armada Contributors

//go:build integration

// Integration tests run the shared objfstest conformance suite against a real
// S3 API served by MinIO in a container. They require Docker and are gated
// behind the "integration" build tag:
//
//	go test -tags=integration ./...
package s3_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/armadakv/objfs/objfstest"
	objfss3 "github.com/armadakv/objfs/s3"
)

func TestS3Integration(t *testing.T) {
	ctx := context.Background()

	c, err := tcminio.Run(ctx, "minio/minio:RELEASE.2024-12-18T13-15-44Z")
	if err != nil {
		t.Fatalf("start minio: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	endpoint, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(c.Username, c.Password, ""),
		),
	)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String("http://" + endpoint)
		o.UsePathStyle = true // MinIO needs path-style addressing
	})

	const bucket = "objfs-test"
	if _, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	b := objfss3.New(client, bucket)
	t.Cleanup(func() { _ = b.Close() })

	objfstest.RunBucket(t, b, objfstest.Options{PresignGetHTTP: true})
}
