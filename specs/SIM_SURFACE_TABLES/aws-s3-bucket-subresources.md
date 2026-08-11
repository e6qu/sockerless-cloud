# AWS S3 — bucket-level subresources

Surface: `simulator-aws/s3.go` + `simulator-aws/s3_bucket_subresources.go`. Every operation listed is `<verb> /{bucket}?<subresource>` against the canonical S3 endpoint.

Canonical reference: <https://docs.aws.amazon.com/AmazonS3/latest/API/API_Operations_Amazon_Simple_Storage_Service.html>

## Status legend

- ✓ — implemented + tested
- n/a — no current terraform-provider resource wraps this exact operation

## Versioning, lifecycle, configuration

| Operation | Verb + path | sim handler | sdk-test | cli-test | tf-test | notes |
|---|---|---|---|---|---|---|
| PutBucketVersioning | `PUT /{bucket}?versioning` | ✓ `s3_bucket_subresources.go::handleS3PutBucketSubresource` | ✓ `TestS3_Bucket_Versioning_RoundTrip` | ✓ `TestS3API_BucketSubresourceCoverage` | ✓ `aws_s3_bucket_versioning` | |
| GetBucketVersioning | `GET /{bucket}?versioning` | ✓ `s3.go::handleS3GetBucket` | ✓ same | ✓ same | ✓ same | |
| PutBucketLifecycleConfiguration | `PUT /{bucket}?lifecycle` | ✓ same | ✓ `TestS3_Bucket_Lifecycle_RoundTrip` | ✓ same | ✓ `aws_s3_bucket_lifecycle_configuration` | Includes `x-amz-transition-default-minimum-object-size` response header. |
| GetBucketLifecycleConfiguration | `GET /{bucket}?lifecycle` | ✓ same | ✓ same | ✓ same | ✓ same | Includes `x-amz-transition-default-minimum-object-size` response header. |
| DeleteBucketLifecycle | `DELETE /{bucket}?lifecycle` | ✓ `handleS3DeleteBucketSubresource` | ✓ same | ✓ same | ✓ destroy of `aws_s3_bucket_lifecycle_configuration` | |
| PutBucketCors | `PUT /{bucket}?cors` | ✓ same | ✓ `TestS3_Bucket_Cors_RoundTrip` | ✓ same | ✓ `aws_s3_bucket_cors_configuration` | |
| GetBucketCors | `GET /{bucket}?cors` | ✓ same | ✓ same | ✓ same | ✓ same | |
| DeleteBucketCors | `DELETE /{bucket}?cors` | ✓ same | ✓ same | ✓ same | ✓ destroy of `aws_s3_bucket_cors_configuration` | |
| PutBucketPolicy | `PUT /{bucket}?policy` | ✓ same | ✓ `TestS3_Bucket_Policy_RoundTrip` | ✓ same | ✓ `aws_s3_bucket_policy` | |
| GetBucketPolicy | `GET /{bucket}?policy` | ✓ same | ✓ same | ✓ same | ✓ same | |
| DeleteBucketPolicy | `DELETE /{bucket}?policy` | ✓ same | ✓ same | ✓ same | ✓ destroy of `aws_s3_bucket_policy` | |
| GetBucketPolicyStatus | `GET /{bucket}?policyStatus` | ✓ same | ✓ same | ✓ same | n/a | |
| PutBucketEncryption | `PUT /{bucket}?encryption` | ✓ same | ✓ `TestS3_Bucket_Encryption_RoundTrip` | ✓ same | ✓ `aws_s3_bucket_server_side_encryption_configuration` | |
| GetBucketEncryption | `GET /{bucket}?encryption` | ✓ same | ✓ same | ✓ same | ✓ same | |
| DeleteBucketEncryption | `DELETE /{bucket}?encryption` | ✓ same | ✓ same | ✓ same | ✓ destroy of `aws_s3_bucket_server_side_encryption_configuration` | |
| PutBucketReplication | `PUT /{bucket}?replication` | ✓ same | ✓ `TestS3_Bucket_Replication_RoundTrip` | ✓ same | ✓ `aws_s3_bucket_replication_configuration` | |
| GetBucketReplication | `GET /{bucket}?replication` | ✓ same | ✓ same | ✓ same | ✓ same | |
| DeleteBucketReplication | `DELETE /{bucket}?replication` | ✓ same | ✓ same | ✓ same | ✓ destroy of `aws_s3_bucket_replication_configuration` | |
| PutBucketTagging | `PUT /{bucket}?tagging` | ✓ same | ✓ `TestS3_Bucket_Tagging_RoundTrip` | ✓ same | ✓ `aws_s3_bucket.tags` | |
| GetBucketTagging | `GET /{bucket}?tagging` | ✓ same | ✓ same | ✓ same | ✓ same | |
| DeleteBucketTagging | `DELETE /{bucket}?tagging` | ✓ same | ✓ same | ✓ same | n/a | Provider manages bucket tags through bucket create/update. |
| PutBucketWebsite | `PUT /{bucket}?website` | ✓ same | ✓ `TestS3_Bucket_Website_RoundTrip` | ✓ same | ✓ `aws_s3_bucket_website_configuration` | |
| GetBucketWebsite | `GET /{bucket}?website` | ✓ same | ✓ same | ✓ same | ✓ same | |
| DeleteBucketWebsite | `DELETE /{bucket}?website` | ✓ same | ✓ same | ✓ same | ✓ destroy of `aws_s3_bucket_website_configuration` | |
| PutBucketLogging | `PUT /{bucket}?logging` | ✓ same | ✓ `TestS3_Bucket_LoggingAclRequestPaymentAccelerate_RoundTrip` | ✓ same | ✓ `aws_s3_bucket_logging` | |
| GetBucketLogging | `GET /{bucket}?logging` | ✓ same | ✓ same | ✓ same | ✓ same | |
| PutBucketAcl | `PUT /{bucket}?acl` | ✓ same | ✓ same | ✓ same | ✓ `aws_s3_bucket_acl` | |
| GetBucketAcl | `GET /{bucket}?acl` | ✓ same | ✓ same | ✓ same | ✓ same | |
| PutBucketRequestPayment | `PUT /{bucket}?requestPayment` | ✓ same | ✓ same | ✓ same | ✓ `aws_s3_bucket_request_payment_configuration` | |
| GetBucketRequestPayment | `GET /{bucket}?requestPayment` | ✓ same | ✓ same | ✓ same | ✓ same | |
| PutBucketAccelerateConfiguration | `PUT /{bucket}?accelerate` | ✓ same | ✓ same | ✓ same | ✓ `aws_s3_bucket_accelerate_configuration` | |
| GetBucketAccelerateConfiguration | `GET /{bucket}?accelerate` | ✓ same | ✓ same | ✓ same | ✓ same | |
| PutBucketOwnershipControls | `PUT /{bucket}?ownershipControls` | ✓ same | ✓ `TestS3_Bucket_OwnershipNotificationPublicAccessObjectLock_RoundTrip` | ✓ same | ✓ `aws_s3_bucket_ownership_controls` | |
| GetBucketOwnershipControls | `GET /{bucket}?ownershipControls` | ✓ same | ✓ same | ✓ same | ✓ same | |
| DeleteBucketOwnershipControls | `DELETE /{bucket}?ownershipControls` | ✓ same | ✓ same | ✓ same | ✓ destroy of `aws_s3_bucket_ownership_controls` | |
| PutBucketNotificationConfiguration | `PUT /{bucket}?notification` | ✓ same | ✓ same | ✓ same | ✓ `aws_s3_bucket_notification` | |
| GetBucketNotificationConfiguration | `GET /{bucket}?notification` | ✓ same | ✓ same | ✓ same | ✓ same | |
| PutPublicAccessBlock | `PUT /{bucket}?publicAccessBlock` | ✓ same | ✓ same | ✓ same | ✓ `aws_s3_bucket_public_access_block` | |
| GetPublicAccessBlock | `GET /{bucket}?publicAccessBlock` | ✓ same | ✓ same | ✓ same | ✓ same | |
| DeletePublicAccessBlock | `DELETE /{bucket}?publicAccessBlock` | ✓ same | ✓ same | ✓ same | ✓ destroy of `aws_s3_bucket_public_access_block` | |
| PutObjectLockConfiguration | `PUT /{bucket}?object-lock` | ✓ same | ✓ same | ✓ same | ✓ `aws_s3_bucket_object_lock_configuration` | |
| GetObjectLockConfiguration | `GET /{bucket}?object-lock` | ✓ same | ✓ same | ✓ same | ✓ same | |
| PutBucketIntelligentTieringConfiguration | `PUT /{bucket}?intelligent-tiering&id={id}` | ✓ same | ✓ `TestS3_Bucket_NamedConfiguration_RoundTrips` | ✓ same | ✓ `aws_s3_bucket_intelligent_tiering_configuration` | |
| GetBucketIntelligentTieringConfiguration | `GET /{bucket}?intelligent-tiering&id={id}` | ✓ same | ✓ same | ✓ same | ✓ same | |
| ListBucketIntelligentTieringConfigurations | `GET /{bucket}?intelligent-tiering` | ✓ same | ✓ same | ✓ same | n/a | Terraform resource reads by name. |
| DeleteBucketIntelligentTieringConfiguration | `DELETE /{bucket}?intelligent-tiering&id={id}` | ✓ same | ✓ same | ✓ same | ✓ destroy of `aws_s3_bucket_intelligent_tiering_configuration` | |
| PutBucketInventoryConfiguration | `PUT /{bucket}?inventory&id={id}` | ✓ same | ✓ same | ✓ same | ✓ `aws_s3_bucket_inventory` | |
| GetBucketInventoryConfiguration | `GET /{bucket}?inventory&id={id}` | ✓ same | ✓ same | ✓ same | ✓ same | |
| ListBucketInventoryConfigurations | `GET /{bucket}?inventory` | ✓ same | ✓ same | ✓ same | n/a | Terraform resource reads by name. |
| DeleteBucketInventoryConfiguration | `DELETE /{bucket}?inventory&id={id}` | ✓ same | ✓ same | ✓ same | ✓ destroy of `aws_s3_bucket_inventory` | |
| PutBucketAnalyticsConfiguration | `PUT /{bucket}?analytics&id={id}` | ✓ same | ✓ same | ✓ same | ✓ `aws_s3_bucket_analytics_configuration` | |
| GetBucketAnalyticsConfiguration | `GET /{bucket}?analytics&id={id}` | ✓ same | ✓ same | ✓ same | ✓ same | |
| ListBucketAnalyticsConfigurations | `GET /{bucket}?analytics` | ✓ same | ✓ same | ✓ same | n/a | Terraform resource reads by name. |
| DeleteBucketAnalyticsConfiguration | `DELETE /{bucket}?analytics&id={id}` | ✓ same | ✓ same | ✓ same | ✓ destroy of `aws_s3_bucket_analytics_configuration` | |
| PutBucketMetricsConfiguration | `PUT /{bucket}?metrics&id={id}` | ✓ same | ✓ same | ✓ same | ✓ `aws_s3_bucket_metric` | |
| GetBucketMetricsConfiguration | `GET /{bucket}?metrics&id={id}` | ✓ same | ✓ same | ✓ same | ✓ same | |
| ListBucketMetricsConfigurations | `GET /{bucket}?metrics` | ✓ same | ✓ same | ✓ same | n/a | Terraform resource reads by name. |
| DeleteBucketMetricsConfiguration | `DELETE /{bucket}?metrics&id={id}` | ✓ same | ✓ same | ✓ same | ✓ destroy of `aws_s3_bucket_metric` | |
| GetBucketLocation | `GET /{bucket}?location` | ✓ same | ✓ `TestS3_Bucket_LifecycleHeadDeleteListAndLocation` | ✓ same | ✓ `aws_s3_bucket` read | |

## Bucket lifecycle (Create / Delete / List)

| Operation | Verb + path | sim handler | sdk-test | cli-test | tf-test | notes |
|---|---|---|---|---|---|---|
| CreateBucket | `PUT /{bucket}` (no subresource) | ✓ `s3.go::handleS3CreateBucket` | ✓ existing + `TestS3_Bucket_LifecycleHeadDeleteListAndLocation` | ✓ `TestS3API_BucketSubresourceCoverage` | ✓ `aws_s3_bucket` | |
| HeadBucket | `HEAD /{bucket}` | ✓ `s3.go::handleS3HeadBucket` | ✓ `TestS3_Bucket_LifecycleHeadDeleteListAndLocation` | ✓ same | ✓ `aws_s3_bucket` read | |
| DeleteBucket | `DELETE /{bucket}` (no subresource) | ✓ `s3.go::handleS3DeleteBucket` | ✓ same | ✓ same | ✓ destroy of `aws_s3_bucket` | |
| ListBuckets | `GET /` | ✓ `s3.go::handleS3ListBuckets` | ✓ same | ✓ same | n/a | Terraform bucket resources read by bucket name. |

## Coverage status

- SDK coverage for the table lives in `simulator-aws/sdk-tests/s3_bucket_subresources_test.go`.
- CLI coverage for the table lives in `simulator-aws/cli-tests/s3_test.go::TestS3API_BucketSubresourceCoverage`.
- Terraform coverage lives in `simulator-aws/terraform-tests/main.tf` and is asserted by `simulator-aws/terraform-tests/apply_test.go`.
- No silent ✗ rows remained after issue #285 / BUG-1226 closed. Rows marked `n/a` are operations that the current Terraform AWS provider does not expose as an independent resource operation; the SDK and CLI still exercise the public S3 API where available.

## Reopens that produced this table

- Issue [#201](https://github.com/e6qu/sockerless/issues/201) — bucket-level PUT subresources routed to CreateBucket → 409 BucketAlreadyOwnedByYou. PR #200's `s3_subresources.go` only covered the object-level PUT/POST surface the user named. This table exists so the next reopen of this shape never repeats.
- Issue [#285](https://github.com/e6qu/sockerless/issues/285) — remaining bucket-subresource row-level client coverage was completed through official SDK, AWS CLI, and Terraform-provider paths where those surfaces exist.
