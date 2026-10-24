package main

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A directory bucket's operations are authorized in the s3express namespace,
// against the bucket's s3express ARN, as the Amazon S3 User Guide's
// "Authenticating and authorizing requests" table for directory buckets lists
// them: a Regional endpoint operation as the action named for it, and a Zonal
// endpoint operation as s3express:CreateSession. A Zonal request signed with
// a session's credentials is authorized by the session itself -- "after the
// session is created, you don't need to use other policies to grant
// permissions to each Zonal endpoint API individually" (CreateSession, in the
// vendored Amazon S3 model) -- which s3EnforceExpressSession enforces.

// s3ExpressRegionalActions maps the table's Regional endpoint operations to
// their actions.
var s3ExpressRegionalActions = map[string]string{
	"CreateBucket":                       "CreateBucket",
	"DeleteBucket":                       "DeleteBucket",
	"DeleteBucketInventoryConfiguration": "PutInventoryConfiguration",
	"DeleteBucketPolicy":                 "DeleteBucketPolicy",
	"GetBucketInventoryConfiguration":    "GetInventoryConfiguration",
	"GetBucketPolicy":                    "GetBucketPolicy",
	"ListBucketInventoryConfigurations":  "GetInventoryConfiguration",
	"PutBucketInventoryConfiguration":    "PutInventoryConfiguration",
	"PutBucketPolicy":                    "PutBucketPolicy",
}

// s3ExpressZonalOperations are the table's Zonal endpoint operations, each
// authorized as s3express:CreateSession, with GetObject, which CreateSession's
// own documentation names among the operations a session serves.
var s3ExpressZonalOperations = map[string]bool{
	"CreateSession": true, "CopyObject": true, "DeleteObject": true, "DeleteObjects": true,
	"GetObject": true, "HeadObject": true, "PutObject": true, "RenameObject": true,
	"GetObjectAttributes": true, "ListObjectsV2": true, "HeadBucket": true,
	"CreateMultipartUpload": true, "UploadPart": true, "UploadPartCopy": true,
	"CompleteMultipartUpload": true, "AbortMultipartUpload": true, "ListParts": true,
	"ListMultipartUploads": true,
}

// s3ExpressBucketARN is a directory bucket's ARN, the s3express bucket format
// the Service Reference publishes.
func s3ExpressBucketARN(r *http.Request, bucket string) string {
	return "arn:aws:s3express:" + iamRESTRegion(r) + ":" + awsAccountID() + ":bucket/" + bucket
}

// s3BucketPolicyARN is the ARN a bucket's policy is attached at: the
// s3express ARN for a directory bucket, whose actions are authorized against
// it, and the s3 ARN for every other bucket.
func s3BucketPolicyARN(r *http.Request, bucket string) string {
	if s3DirectoryBucketZone(bucket) != "" {
		return s3ExpressBucketARN(r, bucket)
	}
	return s3BucketARN(bucket)
}

// s3ExpressAuthorizationTargets is what a directory-bucket operation is
// authorized as, and reports whether the operation is one the table lists.
func s3ExpressAuthorizationTargets(r *http.Request, operation string) ([]iamAuthorizationTarget, bool) {
	if operation == "ListBuckets" && s3IsListDirectoryBuckets(r) {
		return []iamAuthorizationTarget{{action: "s3express:ListAllMyDirectoryBuckets", resource: "*"}}, true
	}
	bucket := sim.PathParam(r, "bucket")
	if s3DirectoryBucketZone(bucket) == "" {
		return nil, false
	}
	if action, regional := s3ExpressRegionalActions[operation]; regional {
		return []iamAuthorizationTarget{{action: "s3express:" + action, resource: s3ExpressBucketARN(r, bucket)}}, true
	}
	if !s3ExpressZonalOperations[operation] {
		return nil, false
	}
	if operation != "CreateSession" && r.Header.Get(s3ExpressSessionTokenHeader) != "" {
		return nil, true
	}
	return []iamAuthorizationTarget{{action: "s3express:CreateSession", resource: s3ExpressBucketARN(r, bucket)}}, true
}
