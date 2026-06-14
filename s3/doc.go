// Copyright Armada Contributors

// Package s3 provides an Amazon S3 (and S3-compatible, e.g. MinIO, R2)
// implementation of [objfs.Bucket].
//
// It lives in its own module so that the AWS SDK is only pulled into builds
// that actually use S3:
//
//	import objfss3 "github.com/armadakv/objfs/s3"
package s3
