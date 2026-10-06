package aws_sdk_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func s3ErrorCode(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "expected an Amazon S3 error, got %v", err)
	return apiErr.ErrorCode()
}

func s3LockedBucket(t *testing.T, c *s3.Client, prefix string) string {
	t.Helper()
	bucket := uniqueName(prefix)
	_, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket), ObjectLockEnabledForBucket: aws.Bool(true)})
	require.NoError(t, err)
	return bucket
}

func s3DeleteVersion(c *s3.Client, bucket, key, version string, bypass bool) error {
	in := &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), VersionId: aws.String(version)}
	if bypass {
		in.BypassGovernanceRetention = aws.Bool(true)
	}
	_, err := c.DeleteObject(ctx, in)
	return err
}

// TestS3_ObjectLockEnablement enables Object Lock at CreateBucket, which turns
// versioning on for good, and on an existing bucket, which needs versioning
// enabled first; a bucket without it refuses every Object Lock operation.
func TestS3_ObjectLockEnablement(t *testing.T) {
	c := s3Client()
	locked := s3LockedBucket(t, c, "lock-enabled")
	versioning, err := c.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(locked)})
	require.NoError(t, err)
	assert.Equal(t, types.BucketVersioningStatusEnabled, versioning.Status, "Object Lock enables versioning with it")
	config, err := c.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{Bucket: aws.String(locked)})
	require.NoError(t, err)
	assert.Equal(t, types.ObjectLockEnabledEnabled, config.ObjectLockConfiguration.ObjectLockEnabled)
	assert.Nil(t, config.ObjectLockConfiguration.Rule)
	_, err = c.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{Bucket: aws.String(locked),
		VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusSuspended}})
	assert.Equal(t, "InvalidBucketState", s3ErrorCode(t, err), "versioning stays enabled on an Object Lock bucket")

	plain := uniqueName("lock-plain")
	s3CreateBucket(t, c, plain)
	_, err = c.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{Bucket: aws.String(plain)})
	assert.Equal(t, "ObjectLockConfigurationNotFoundError", s3ErrorCode(t, err))
	s3Put(t, c, plain, "doc", "plain")
	_, err = c.PutObjectRetention(ctx, &s3.PutObjectRetentionInput{Bucket: aws.String(plain), Key: aws.String("doc"),
		Retention: &types.ObjectLockRetention{Mode: types.ObjectLockRetentionModeGovernance, RetainUntilDate: aws.Time(time.Now().Add(time.Hour))}})
	assert.Equal(t, "InvalidRequest", s3ErrorCode(t, err))
	_, err = c.PutObjectLegalHold(ctx, &s3.PutObjectLegalHoldInput{Bucket: aws.String(plain), Key: aws.String("doc"),
		LegalHold: &types.ObjectLockLegalHold{Status: types.ObjectLockLegalHoldStatusOn}})
	assert.Equal(t, "InvalidRequest", s3ErrorCode(t, err))
	_, err = c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(plain), Key: aws.String("held"), Body: strings.NewReader("x"),
		ObjectLockLegalHoldStatus: types.ObjectLockLegalHoldStatusOn})
	assert.Equal(t, "InvalidRequest", s3ErrorCode(t, err))

	enable := &s3.PutObjectLockConfigurationInput{Bucket: aws.String(plain),
		ObjectLockConfiguration: &types.ObjectLockConfiguration{ObjectLockEnabled: types.ObjectLockEnabledEnabled}}
	_, err = c.PutObjectLockConfiguration(ctx, enable)
	assert.Equal(t, "InvalidBucketState", s3ErrorCode(t, err), "Object Lock needs versioning enabled first")
	s3SetVersioning(t, c, plain, types.BucketVersioningStatusEnabled)
	_, err = c.PutObjectLockConfiguration(ctx, enable)
	require.NoError(t, err)
	config, err = c.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{Bucket: aws.String(plain)})
	require.NoError(t, err)
	assert.Equal(t, types.ObjectLockEnabledEnabled, config.ObjectLockConfiguration.ObjectLockEnabled)

	_, err = c.PutObjectLockConfiguration(ctx, &s3.PutObjectLockConfigurationInput{Bucket: aws.String(plain),
		ObjectLockConfiguration: &types.ObjectLockConfiguration{ObjectLockEnabled: types.ObjectLockEnabledEnabled,
			Rule: &types.ObjectLockRule{DefaultRetention: &types.DefaultRetention{Mode: types.ObjectLockRetentionModeGovernance,
				Days: aws.Int32(1), Years: aws.Int32(1)}}}})
	assert.Equal(t, "MalformedXML", s3ErrorCode(t, err), "a default retention names days or years, not both")
	_, err = c.PutObjectLockConfiguration(ctx, &s3.PutObjectLockConfigurationInput{Bucket: aws.String(plain),
		ObjectLockConfiguration: &types.ObjectLockConfiguration{ObjectLockEnabled: types.ObjectLockEnabledEnabled,
			Rule: &types.ObjectLockRule{DefaultRetention: &types.DefaultRetention{Mode: types.ObjectLockRetentionModeGovernance,
				Days: aws.Int32(0)}}}})
	assert.Equal(t, "InvalidArgument", s3ErrorCode(t, err))
}

// TestS3_ObjectLockDefaultGovernanceRetention gives a bucket a default
// GOVERNANCE retention: a new version takes it, a version delete is refused
// until the request bypasses governance retention, and a delete without a
// version only adds a delete marker.
func TestS3_ObjectLockDefaultGovernanceRetention(t *testing.T) {
	c := s3Client()
	bucket := s3LockedBucket(t, c, "lock-default")
	_, err := c.PutObjectLockConfiguration(ctx, &s3.PutObjectLockConfigurationInput{Bucket: aws.String(bucket),
		ObjectLockConfiguration: &types.ObjectLockConfiguration{ObjectLockEnabled: types.ObjectLockEnabledEnabled,
			Rule: &types.ObjectLockRule{DefaultRetention: &types.DefaultRetention{Mode: types.ObjectLockRetentionModeGovernance, Days: aws.Int32(1)}}}})
	require.NoError(t, err)

	before := time.Now().UTC()
	version := aws.ToString(s3Put(t, c, bucket, "report", "v1").VersionId)
	head, err := c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("report")})
	require.NoError(t, err)
	assert.Equal(t, types.ObjectLockModeGovernance, head.ObjectLockMode)
	require.NotNil(t, head.ObjectLockRetainUntilDate)
	assert.WithinDuration(t, before.AddDate(0, 0, 1), *head.ObjectLockRetainUntilDate, time.Minute)
	retention, err := c.GetObjectRetention(ctx, &s3.GetObjectRetentionInput{Bucket: aws.String(bucket), Key: aws.String("report")})
	require.NoError(t, err)
	assert.Equal(t, types.ObjectLockRetentionModeGovernance, retention.Retention.Mode)

	assert.Equal(t, "AccessDenied", s3ErrorCode(t, s3DeleteVersion(c, bucket, "report", version, false)))
	marker, err := c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("report")})
	require.NoError(t, err, "a delete marker removes no protected version")
	assert.True(t, aws.ToBool(marker.DeleteMarker))
	_, err = s3Read(t, c, bucket, "report", version)
	require.NoError(t, err, "the protected version is still there")
	require.NoError(t, s3DeleteVersion(c, bucket, "report", version, true))
	_, err = s3Read(t, c, bucket, "report", version)
	assert.Equal(t, "NoSuchVersion", s3ErrorCode(t, err))
}

// TestS3_ObjectLockComplianceRetention puts a version under COMPLIANCE
// retention, with the checksum such a write needs: nothing deletes it or
// shortens its retention, while extending it succeeds.
func TestS3_ObjectLockComplianceRetention(t *testing.T) {
	c := s3Client()
	bucket := s3LockedBucket(t, c, "lock-compliance")
	until := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	_, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("ledger"),
		Body: strings.NewReader("ledger"), ObjectLockMode: types.ObjectLockModeCompliance, ObjectLockRetainUntilDate: aws.Time(until)})
	assert.Equal(t, "InvalidRequest", s3ErrorCode(t, err), "a write with Object Lock parameters needs Content-MD5 or a checksum")
	_, err = c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("ledger"), ChecksumAlgorithm: types.ChecksumAlgorithmCrc32,
		Body: strings.NewReader("ledger"), ObjectLockMode: types.ObjectLockModeCompliance, ObjectLockRetainUntilDate: aws.Time(until)})
	require.NoError(t, err)
	head, err := c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("ledger")})
	require.NoError(t, err)
	assert.Equal(t, types.ObjectLockModeCompliance, head.ObjectLockMode)
	assert.True(t, until.Equal(aws.ToTime(head.ObjectLockRetainUntilDate)))
	version := aws.ToString(head.VersionId)

	assert.Equal(t, "AccessDenied", s3ErrorCode(t, s3DeleteVersion(c, bucket, "ledger", version, true)),
		"no bypass lifts COMPLIANCE retention")
	retain := func(mode types.ObjectLockRetentionMode, until time.Time) error {
		_, err := c.PutObjectRetention(ctx, &s3.PutObjectRetentionInput{Bucket: aws.String(bucket), Key: aws.String("ledger"),
			VersionId: aws.String(version), BypassGovernanceRetention: aws.Bool(true),
			Retention: &types.ObjectLockRetention{Mode: mode, RetainUntilDate: aws.Time(until)}})
		return err
	}
	assert.Equal(t, "AccessDenied", s3ErrorCode(t, retain(types.ObjectLockRetentionModeCompliance, until.Add(-time.Minute))))
	assert.Equal(t, "AccessDenied", s3ErrorCode(t, retain(types.ObjectLockRetentionModeGovernance, until.Add(time.Hour))))
	assert.Equal(t, "InvalidArgument", s3ErrorCode(t, retain(types.ObjectLockRetentionModeCompliance, time.Now().Add(-time.Hour))))
	require.NoError(t, retain(types.ObjectLockRetentionModeCompliance, until.Add(time.Hour)))
	retention, err := c.GetObjectRetention(ctx, &s3.GetObjectRetentionInput{Bucket: aws.String(bucket), Key: aws.String("ledger")})
	require.NoError(t, err)
	assert.True(t, until.Add(time.Hour).Equal(aws.ToTime(retention.Retention.RetainUntilDate)))

	_, err = c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("late"), Body: strings.NewReader("x"),
		ChecksumAlgorithm: types.ChecksumAlgorithmCrc32,
		ObjectLockMode:    types.ObjectLockModeCompliance, ObjectLockRetainUntilDate: aws.Time(time.Now().Add(-time.Hour))})
	assert.Equal(t, "InvalidArgument", s3ErrorCode(t, err))
	_, err = c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("late"), Body: strings.NewReader("x"),
		ChecksumAlgorithm: types.ChecksumAlgorithmCrc32, ObjectLockMode: types.ObjectLockModeCompliance})
	assert.Equal(t, "InvalidArgument", s3ErrorCode(t, err), "a mode needs a retain-until date")
}

// TestS3_ObjectLockGovernanceRetentionChanges shortens and removes a GOVERNANCE
// retention, which only a request bypassing governance retention may do.
func TestS3_ObjectLockGovernanceRetentionChanges(t *testing.T) {
	c := s3Client()
	bucket := s3LockedBucket(t, c, "lock-governance")
	until := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	_, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("draft"),
		ChecksumAlgorithm: types.ChecksumAlgorithmCrc32,
		Body:              strings.NewReader("draft"), ObjectLockMode: types.ObjectLockModeGovernance, ObjectLockRetainUntilDate: aws.Time(until)})
	require.NoError(t, err)
	retain := func(retention *types.ObjectLockRetention, bypass bool) error {
		in := &s3.PutObjectRetentionInput{Bucket: aws.String(bucket), Key: aws.String("draft"), Retention: retention}
		if bypass {
			in.BypassGovernanceRetention = aws.Bool(true)
		}
		_, err := c.PutObjectRetention(ctx, in)
		return err
	}
	shorter := &types.ObjectLockRetention{Mode: types.ObjectLockRetentionModeGovernance, RetainUntilDate: aws.Time(until.Add(-time.Hour))}
	assert.Equal(t, "AccessDenied", s3ErrorCode(t, retain(shorter, false)))
	assert.Equal(t, "AccessDenied", s3ErrorCode(t, retain(&types.ObjectLockRetention{}, false)))
	require.NoError(t, retain(shorter, true))
	require.NoError(t, retain(&types.ObjectLockRetention{}, true))
	_, err = c.GetObjectRetention(ctx, &s3.GetObjectRetentionInput{Bucket: aws.String(bucket), Key: aws.String("draft")})
	assert.Equal(t, "NoSuchObjectLockConfiguration", s3ErrorCode(t, err), "the retention is removed")
}

// TestS3_ObjectLockLegalHold puts a legal hold on a version: DeleteObjects
// refuses that entry, whatever the retention, until the hold is lifted.
func TestS3_ObjectLockLegalHold(t *testing.T) {
	c := s3Client()
	bucket := s3LockedBucket(t, c, "lock-hold")
	version := aws.ToString(s3Put(t, c, bucket, "evidence", "evidence").VersionId)
	_, err := c.GetObjectLegalHold(ctx, &s3.GetObjectLegalHoldInput{Bucket: aws.String(bucket), Key: aws.String("evidence")})
	assert.Equal(t, "NoSuchObjectLockConfiguration", s3ErrorCode(t, err), "no legal hold was ever set")

	hold := func(status types.ObjectLockLegalHoldStatus) {
		_, err := c.PutObjectLegalHold(ctx, &s3.PutObjectLegalHoldInput{Bucket: aws.String(bucket), Key: aws.String("evidence"),
			VersionId: aws.String(version), LegalHold: &types.ObjectLockLegalHold{Status: status}})
		require.NoError(t, err)
	}
	hold(types.ObjectLockLegalHoldStatusOn)
	head, err := c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("evidence")})
	require.NoError(t, err)
	assert.Equal(t, types.ObjectLockLegalHoldStatusOn, head.ObjectLockLegalHoldStatus)

	deleteVersion := func() *s3.DeleteObjectsOutput {
		out, err := c.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(bucket), BypassGovernanceRetention: aws.Bool(true),
			Delete: &types.Delete{Objects: []types.ObjectIdentifier{{Key: aws.String("evidence"), VersionId: aws.String(version)}}}})
		require.NoError(t, err)
		return out
	}
	refused := deleteVersion()
	require.Len(t, refused.Errors, 1, "a legal hold refuses the delete even with the bypass")
	assert.Equal(t, "AccessDenied", aws.ToString(refused.Errors[0].Code))
	assert.Equal(t, version, aws.ToString(refused.Errors[0].VersionId))
	assert.Empty(t, refused.Deleted)

	hold(types.ObjectLockLegalHoldStatusOff)
	deleted := deleteVersion()
	assert.Empty(t, deleted.Errors)
	require.Len(t, deleted.Deleted, 1)
	_, err = s3Read(t, c, bucket, "evidence", version)
	assert.Equal(t, "NoSuchVersion", s3ErrorCode(t, err))
}
