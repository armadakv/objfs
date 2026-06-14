// Copyright Armada Contributors

// Package gcs provides a Google Cloud Storage implementation of
// [objfs.Bucket].
//
// It lives in its own module so that the Google Cloud SDK is only pulled into
// builds that actually use GCS:
//
//	import objfsgcs "github.com/armadakv/objfs/gcs"
package gcs
