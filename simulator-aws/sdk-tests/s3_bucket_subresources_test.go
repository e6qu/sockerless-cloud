package aws_sdk_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3_Bucket_Versioning_RoundTrip locks the invariant: PUT
// `/{bucket}?versioning` must route to the bucket-subresource
// dispatcher and persist the configuration, GET must return the
// same configuration back. A regression that re-routes the PUT
// to CreateBucket would surface as 409 BucketAlreadyOwnedByYou
// and fail this test.
func TestS3_Bucket_Versioning_RoundTrip(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "versioning-bucket"
	s3CreateBucket(t, c, bucket)

	_, err := c.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(bucket),
		VersioningConfiguration: &types.VersioningConfiguration{
			Status: types.BucketVersioningStatusEnabled,
		},
	})
	require.NoError(t, err, "PutBucketVersioning must succeed (subresource dispatcher routes the PUT, not CreateBucket)")

	get, err := c.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	assert.Equal(t, types.BucketVersioningStatusEnabled, get.Status,
		"GetBucketVersioning must return the previously-PUT Enabled status")
}

// TestS3_Bucket_Lifecycle_RoundTrip exercises Put → Get → Delete on
// the bucket lifecycle subresource — the lifecycle config has both
// PUT and DELETE shapes; both must be wired (not just PUT).
func TestS3_Bucket_Lifecycle_RoundTrip(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "lifecycle-bucket"
	s3CreateBucket(t, c, bucket)

	// Initial Get → canonical empty-config 404.
	_, err := c.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(bucket)})
	require.Error(t, err, "initial GetBucketLifecycleConfiguration must 404")
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, "NoSuchLifecycleConfiguration", apiErr.ErrorCode())

	// PUT a rule.
	put, err := c.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket:                             aws.String(bucket),
		TransitionDefaultMinimumObjectSize: types.TransitionDefaultMinimumObjectSizeVariesByStorageClass,
		LifecycleConfiguration: &types.BucketLifecycleConfiguration{
			Rules: []types.LifecycleRule{
				{
					ID:     aws.String("expire-30d"),
					Status: types.ExpirationStatusEnabled,
					Filter: &types.LifecycleRuleFilter{Prefix: aws.String("")},
					Expiration: &types.LifecycleExpiration{
						Days: aws.Int32(30),
					},
				},
			},
		},
	})
	require.NoError(t, err, "PutBucketLifecycleConfiguration must succeed")
	assert.Equal(t, types.TransitionDefaultMinimumObjectSizeVariesByStorageClass, put.TransitionDefaultMinimumObjectSize,
		"PutBucketLifecycleConfiguration must echo the transition default minimum object size header")

	// Get round-trips.
	get, err := c.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(bucket)})
	require.NoError(t, err, "GetBucketLifecycleConfiguration must return the PUT config")
	require.Len(t, get.Rules, 1)
	assert.Equal(t, "expire-30d", aws.ToString(get.Rules[0].ID))
	assert.Equal(t, types.TransitionDefaultMinimumObjectSizeVariesByStorageClass, get.TransitionDefaultMinimumObjectSize,
		"GetBucketLifecycleConfiguration must return the stored transition default minimum object size header")

	// Delete clears the config.
	_, err = c.DeleteBucketLifecycle(ctx, &s3.DeleteBucketLifecycleInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	// Subsequent Get is 404 again.
	_, err = c.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(bucket)})
	require.Error(t, err, "post-delete GetBucketLifecycleConfiguration must 404")
}

// TestS3_Bucket_Cors_RoundTrip covers the bucket CORS subresource.
func TestS3_Bucket_Cors_RoundTrip(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "cors-bucket"
	s3CreateBucket(t, c, bucket)

	_, err := c.PutBucketCors(ctx, &s3.PutBucketCorsInput{
		Bucket: aws.String(bucket),
		CORSConfiguration: &types.CORSConfiguration{
			CORSRules: []types.CORSRule{
				{
					AllowedOrigins: []string{"https://app.example.com"},
					AllowedMethods: []string{"GET", "PUT"},
					AllowedHeaders: []string{"*"},
				},
			},
		},
	})
	require.NoError(t, err, "PutBucketCors must succeed")

	get, err := c.GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.Len(t, get.CORSRules, 1)
	assert.Contains(t, get.CORSRules[0].AllowedOrigins, "https://app.example.com")

	_, err = c.DeleteBucketCors(ctx, &s3.DeleteBucketCorsInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
}

// TestS3_Bucket_Policy_RoundTrip covers the bucket policy
// subresource — PUT body is a JSON IAM policy document (not XML);
// the dispatcher must accept either content type.
func TestS3_Bucket_Policy_RoundTrip(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "policy-bucket"
	s3CreateBucket(t, c, bucket)

	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::policy-bucket/*"}]}`
	_, err := c.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{
		Bucket: aws.String(bucket),
		Policy: aws.String(policy),
	})
	require.NoError(t, err, "PutBucketPolicy must succeed")

	get, err := c.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	assert.Equal(t, policy, aws.ToString(get.Policy))

	status, err := c.GetBucketPolicyStatus(ctx, &s3.GetBucketPolicyStatusInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.NotNil(t, status.PolicyStatus)
	assert.True(t, aws.ToBool(status.PolicyStatus.IsPublic))

	_, err = c.DeleteBucketPolicy(ctx, &s3.DeleteBucketPolicyInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
}

// TestS3_Bucket_Encryption_RoundTrip covers
// PutBucketEncryption / GetBucketEncryption / DeleteBucketEncryption.
func TestS3_Bucket_Encryption_RoundTrip(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "encryption-bucket"
	s3CreateBucket(t, c, bucket)

	_, err := c.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: aws.String(bucket),
		ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
			Rules: []types.ServerSideEncryptionRule{
				{
					ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{
						SSEAlgorithm: types.ServerSideEncryptionAes256,
					},
				},
			},
		},
	})
	require.NoError(t, err, "PutBucketEncryption must succeed")

	get, err := c.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.Len(t, get.ServerSideEncryptionConfiguration.Rules, 1)
	assert.Equal(t, types.ServerSideEncryptionAes256,
		get.ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault.SSEAlgorithm)

	_, err = c.DeleteBucketEncryption(ctx, &s3.DeleteBucketEncryptionInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
}

// TestS3_Bucket_Tagging_RoundTrip covers the bucket-level tagging
// subresource. Distinct from object tagging (which already worked).
func TestS3_Bucket_Tagging_RoundTrip(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "btagging-bucket"
	s3CreateBucket(t, c, bucket)

	_, err := c.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
		Bucket: aws.String(bucket),
		Tagging: &types.Tagging{
			TagSet: []types.Tag{
				{Key: aws.String("env"), Value: aws.String("prod")},
				{Key: aws.String("team"), Value: aws.String("platform")},
			},
		},
	})
	require.NoError(t, err, "PutBucketTagging must succeed")

	get, err := c.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.Len(t, get.TagSet, 2)

	_, err = c.DeleteBucketTagging(ctx, &s3.DeleteBucketTaggingInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
}

// TestS3_Bucket_Website_RoundTrip covers the bucket-website
// subresource — locks in the per-subresource success-status fidelity
// (Website is 200 OK, distinct from Policy's 204 No Content).
func TestS3_Bucket_Website_RoundTrip(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "website-bucket"
	s3CreateBucket(t, c, bucket)

	_, err := c.PutBucketWebsite(ctx, &s3.PutBucketWebsiteInput{
		Bucket: aws.String(bucket),
		WebsiteConfiguration: &types.WebsiteConfiguration{
			IndexDocument: &types.IndexDocument{Suffix: aws.String("index.html")},
		},
	})
	require.NoError(t, err, "PutBucketWebsite must succeed")

	get, err := c.GetBucketWebsite(ctx, &s3.GetBucketWebsiteInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.NotNil(t, get.IndexDocument)
	assert.Equal(t, "index.html", aws.ToString(get.IndexDocument.Suffix))

	_, err = c.DeleteBucketWebsite(ctx, &s3.DeleteBucketWebsiteInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
}

func TestS3_Bucket_Replication_RoundTrip(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "replication-bucket"
	destBucket := "replication-dest-bucket"
	s3CreateBucket(t, c, bucket)
	s3CreateBucket(t, c, destBucket)

	_, err := c.PutBucketReplication(ctx, &s3.PutBucketReplicationInput{
		Bucket: aws.String(bucket),
		ReplicationConfiguration: &types.ReplicationConfiguration{
			Role: aws.String("arn:aws:iam::000000000001:role/s3-replication"),
			Rules: []types.ReplicationRule{
				{
					ID:     aws.String("replicate-logs"),
					Status: types.ReplicationRuleStatusEnabled,
					Filter: &types.ReplicationRuleFilter{
						Prefix: aws.String("logs/"),
					},
					Destination: &types.Destination{
						Bucket: aws.String("arn:aws:s3:::" + destBucket),
					},
				},
			},
		},
	})
	require.NoError(t, err)

	get, err := c.GetBucketReplication(ctx, &s3.GetBucketReplicationInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.Len(t, get.ReplicationConfiguration.Rules, 1)
	assert.Equal(t, "replicate-logs", aws.ToString(get.ReplicationConfiguration.Rules[0].ID))

	_, err = c.DeleteBucketReplication(ctx, &s3.DeleteBucketReplicationInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
}

func TestS3_Bucket_LoggingAclRequestPaymentAccelerate_RoundTrip(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "misc-config-bucket"
	s3CreateBucket(t, c, bucket)

	_, err := c.PutBucketLogging(ctx, &s3.PutBucketLoggingInput{
		Bucket: aws.String(bucket),
		BucketLoggingStatus: &types.BucketLoggingStatus{
			LoggingEnabled: &types.LoggingEnabled{
				TargetBucket: aws.String(bucket),
				TargetPrefix: aws.String("logs/"),
			},
		},
	})
	require.NoError(t, err)
	logging, err := c.GetBucketLogging(ctx, &s3.GetBucketLoggingInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.NotNil(t, logging.LoggingEnabled)
	assert.Equal(t, "logs/", aws.ToString(logging.LoggingEnabled.TargetPrefix))

	_, err = c.PutBucketAcl(ctx, &s3.PutBucketAclInput{
		Bucket: aws.String(bucket),
		AccessControlPolicy: &types.AccessControlPolicy{
			Owner: &types.Owner{ID: aws.String("000000000001"), DisplayName: aws.String("simulator")},
			Grants: []types.Grant{
				{
					Grantee: &types.Grantee{
						Type:        types.TypeCanonicalUser,
						ID:          aws.String("000000000001"),
						DisplayName: aws.String("simulator"),
					},
					Permission: types.PermissionFullControl,
				},
			},
		},
	})
	require.NoError(t, err)
	acl, err := c.GetBucketAcl(ctx, &s3.GetBucketAclInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.Len(t, acl.Grants, 1)
	assert.Equal(t, types.PermissionFullControl, acl.Grants[0].Permission)

	_, err = c.PutBucketRequestPayment(ctx, &s3.PutBucketRequestPaymentInput{
		Bucket: aws.String(bucket),
		RequestPaymentConfiguration: &types.RequestPaymentConfiguration{
			Payer: types.PayerRequester,
		},
	})
	require.NoError(t, err)
	payment, err := c.GetBucketRequestPayment(ctx, &s3.GetBucketRequestPaymentInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	assert.Equal(t, types.PayerRequester, payment.Payer)

	_, err = c.PutBucketAccelerateConfiguration(ctx, &s3.PutBucketAccelerateConfigurationInput{
		Bucket: aws.String(bucket),
		AccelerateConfiguration: &types.AccelerateConfiguration{
			Status: types.BucketAccelerateStatusEnabled,
		},
	})
	require.NoError(t, err)
	accel, err := c.GetBucketAccelerateConfiguration(ctx, &s3.GetBucketAccelerateConfigurationInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	assert.Equal(t, types.BucketAccelerateStatusEnabled, accel.Status)
}

func TestS3_Bucket_OwnershipNotificationPublicAccessObjectLock_RoundTrip(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "controls-bucket"
	s3CreateBucket(t, c, bucket)

	_, err := c.PutBucketOwnershipControls(ctx, &s3.PutBucketOwnershipControlsInput{
		Bucket: aws.String(bucket),
		OwnershipControls: &types.OwnershipControls{
			Rules: []types.OwnershipControlsRule{
				{ObjectOwnership: types.ObjectOwnershipBucketOwnerPreferred},
			},
		},
	})
	require.NoError(t, err)
	ownership, err := c.GetBucketOwnershipControls(ctx, &s3.GetBucketOwnershipControlsInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.Len(t, ownership.OwnershipControls.Rules, 1)
	assert.Equal(t, types.ObjectOwnershipBucketOwnerPreferred, ownership.OwnershipControls.Rules[0].ObjectOwnership)
	_, err = c.DeleteBucketOwnershipControls(ctx, &s3.DeleteBucketOwnershipControlsInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	_, err = c.PutBucketNotificationConfiguration(ctx, &s3.PutBucketNotificationConfigurationInput{
		Bucket: aws.String(bucket),
		NotificationConfiguration: &types.NotificationConfiguration{
			QueueConfigurations: []types.QueueConfiguration{
				{
					Id:       aws.String("queue-created"),
					QueueArn: aws.String("arn:aws:sqs:us-east-1:000000000001:queue"),
					Events:   []types.Event{types.EventS3ObjectCreatedPut},
				},
			},
		},
	})
	require.NoError(t, err)
	notification, err := c.GetBucketNotificationConfiguration(ctx, &s3.GetBucketNotificationConfigurationInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.Len(t, notification.QueueConfigurations, 1)
	assert.Equal(t, "queue-created", aws.ToString(notification.QueueConfigurations[0].Id))

	_, err = c.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(bucket),
		PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	})
	require.NoError(t, err)
	pab, err := c.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(pab.PublicAccessBlockConfiguration.BlockPublicAcls))
	_, err = c.DeletePublicAccessBlock(ctx, &s3.DeletePublicAccessBlockInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	_, err = c.PutObjectLockConfiguration(ctx, &s3.PutObjectLockConfigurationInput{
		Bucket: aws.String(bucket),
		ObjectLockConfiguration: &types.ObjectLockConfiguration{
			ObjectLockEnabled: types.ObjectLockEnabledEnabled,
			Rule: &types.ObjectLockRule{
				DefaultRetention: &types.DefaultRetention{
					Mode: types.ObjectLockRetentionModeGovernance,
					Days: aws.Int32(1),
				},
			},
		},
	})
	require.NoError(t, err)
	objectLock, err := c.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	assert.Equal(t, types.ObjectLockEnabledEnabled, objectLock.ObjectLockConfiguration.ObjectLockEnabled)
}

func TestS3_Bucket_NamedConfiguration_RoundTrips(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "named-config-bucket"
	s3CreateBucket(t, c, bucket)

	_, err := c.PutBucketIntelligentTieringConfiguration(ctx, &s3.PutBucketIntelligentTieringConfigurationInput{
		Bucket: aws.String(bucket),
		Id:     aws.String("archive-tier"),
		IntelligentTieringConfiguration: &types.IntelligentTieringConfiguration{
			Id:     aws.String("archive-tier"),
			Status: types.IntelligentTieringStatusEnabled,
			Tierings: []types.Tiering{
				{AccessTier: types.IntelligentTieringAccessTierArchiveAccess, Days: aws.Int32(90)},
			},
		},
	})
	require.NoError(t, err)
	tiering, err := c.GetBucketIntelligentTieringConfiguration(ctx, &s3.GetBucketIntelligentTieringConfigurationInput{
		Bucket: aws.String(bucket),
		Id:     aws.String("archive-tier"),
	})
	require.NoError(t, err)
	assert.Equal(t, "archive-tier", aws.ToString(tiering.IntelligentTieringConfiguration.Id))
	tieringList, err := c.ListBucketIntelligentTieringConfigurations(ctx, &s3.ListBucketIntelligentTieringConfigurationsInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.Len(t, tieringList.IntelligentTieringConfigurationList, 1)
	_, err = c.DeleteBucketIntelligentTieringConfiguration(ctx, &s3.DeleteBucketIntelligentTieringConfigurationInput{
		Bucket: aws.String(bucket),
		Id:     aws.String("archive-tier"),
	})
	require.NoError(t, err)

	_, err = c.PutBucketInventoryConfiguration(ctx, &s3.PutBucketInventoryConfigurationInput{
		Bucket: aws.String(bucket),
		Id:     aws.String("inventory-current"),
		InventoryConfiguration: &types.InventoryConfiguration{
			Id:                     aws.String("inventory-current"),
			IsEnabled:              aws.Bool(true),
			IncludedObjectVersions: types.InventoryIncludedObjectVersionsCurrent,
			Destination: &types.InventoryDestination{
				S3BucketDestination: &types.InventoryS3BucketDestination{
					Bucket: aws.String("arn:aws:s3:::" + bucket),
					Format: types.InventoryFormatCsv,
				},
			},
			Schedule: &types.InventorySchedule{Frequency: types.InventoryFrequencyDaily},
		},
	})
	require.NoError(t, err)
	inventory, err := c.GetBucketInventoryConfiguration(ctx, &s3.GetBucketInventoryConfigurationInput{
		Bucket: aws.String(bucket),
		Id:     aws.String("inventory-current"),
	})
	require.NoError(t, err)
	assert.Equal(t, "inventory-current", aws.ToString(inventory.InventoryConfiguration.Id))
	inventoryList, err := c.ListBucketInventoryConfigurations(ctx, &s3.ListBucketInventoryConfigurationsInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.Len(t, inventoryList.InventoryConfigurationList, 1)
	_, err = c.DeleteBucketInventoryConfiguration(ctx, &s3.DeleteBucketInventoryConfigurationInput{
		Bucket: aws.String(bucket),
		Id:     aws.String("inventory-current"),
	})
	require.NoError(t, err)

	_, err = c.PutBucketAnalyticsConfiguration(ctx, &s3.PutBucketAnalyticsConfigurationInput{
		Bucket: aws.String(bucket),
		Id:     aws.String("analytics-all"),
		AnalyticsConfiguration: &types.AnalyticsConfiguration{
			Id: aws.String("analytics-all"),
			StorageClassAnalysis: &types.StorageClassAnalysis{
				DataExport: &types.StorageClassAnalysisDataExport{
					OutputSchemaVersion: types.StorageClassAnalysisSchemaVersionV1,
					Destination: &types.AnalyticsExportDestination{
						S3BucketDestination: &types.AnalyticsS3BucketDestination{
							Bucket: aws.String("arn:aws:s3:::" + bucket),
							Format: types.AnalyticsS3ExportFileFormatCsv,
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	analytics, err := c.GetBucketAnalyticsConfiguration(ctx, &s3.GetBucketAnalyticsConfigurationInput{
		Bucket: aws.String(bucket),
		Id:     aws.String("analytics-all"),
	})
	require.NoError(t, err)
	assert.Equal(t, "analytics-all", aws.ToString(analytics.AnalyticsConfiguration.Id))
	analyticsList, err := c.ListBucketAnalyticsConfigurations(ctx, &s3.ListBucketAnalyticsConfigurationsInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.Len(t, analyticsList.AnalyticsConfigurationList, 1)
	_, err = c.DeleteBucketAnalyticsConfiguration(ctx, &s3.DeleteBucketAnalyticsConfigurationInput{
		Bucket: aws.String(bucket),
		Id:     aws.String("analytics-all"),
	})
	require.NoError(t, err)

	_, err = c.PutBucketMetricsConfiguration(ctx, &s3.PutBucketMetricsConfigurationInput{
		Bucket: aws.String(bucket),
		Id:     aws.String("metrics-prefix"),
		MetricsConfiguration: &types.MetricsConfiguration{
			Id:     aws.String("metrics-prefix"),
			Filter: &types.MetricsFilterMemberPrefix{Value: "logs/"},
		},
	})
	require.NoError(t, err)
	metrics, err := c.GetBucketMetricsConfiguration(ctx, &s3.GetBucketMetricsConfigurationInput{
		Bucket: aws.String(bucket),
		Id:     aws.String("metrics-prefix"),
	})
	require.NoError(t, err)
	assert.Equal(t, "metrics-prefix", aws.ToString(metrics.MetricsConfiguration.Id))
	metricsList, err := c.ListBucketMetricsConfigurations(ctx, &s3.ListBucketMetricsConfigurationsInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	require.Len(t, metricsList.MetricsConfigurationList, 1)
	_, err = c.DeleteBucketMetricsConfiguration(ctx, &s3.DeleteBucketMetricsConfigurationInput{
		Bucket: aws.String(bucket),
		Id:     aws.String("metrics-prefix"),
	})
	require.NoError(t, err)
}

func TestS3_Bucket_LifecycleHeadDeleteListAndLocation(t *testing.T) {
	c := s3Client()
	ctx := context.Background()
	bucket := "bucket-lifecycle-ops"
	s3CreateBucket(t, c, bucket)

	_, err := c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	location, err := c.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	assert.Equal(t, types.BucketLocationConstraint("us-east-1"), location.LocationConstraint)

	list, err := c.ListBuckets(ctx, &s3.ListBucketsInput{})
	require.NoError(t, err)
	assert.Contains(t, bucketNames(list.Buckets), bucket)

	_, err = c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	_, err = c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	require.Error(t, err)
}

func bucketNames(buckets []types.Bucket) []string {
	names := make([]string, 0, len(buckets))
	for _, bucket := range buckets {
		names = append(names, aws.ToString(bucket.Name))
	}
	return names
}
