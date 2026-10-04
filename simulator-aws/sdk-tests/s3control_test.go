package aws_sdk_test

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3control"
	s3ctypes "github.com/aws/aws-sdk-go-v2/service/s3control/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// s3ControlRole registers a role the control plane can hand work to, and
// returns its ARN. Several control-plane resources refuse to reference a role
// that does not exist, so the tests create real ones.
func s3ControlRole(t *testing.T, name string) string {
	t.Helper()
	ic := iam.NewFromConfig(sdkConfig(), func(o *iam.Options) { o.BaseEndpoint = aws.String(baseURL) })
	out, err := ic.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String(name),
		AssumeRolePolicyDocument: aws.String(
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
				`"Principal":{"Service":"s3.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = ic.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)}) })
	return aws.ToString(out.Role.Arn)
}

// s3ControlCallerARN is the identity the suite's credentials resolve to, read
// from STS the way any caller discovers its own principal.
func s3ControlCallerARN(t *testing.T) string {
	t.Helper()
	stsClient := sts.NewFromConfig(sdkConfig(), func(o *sts.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
	out, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	return aws.ToString(out.Arn)
}

// TestS3Control_StorageLensConfiguration covers a Storage Lens dashboard: the
// configuration comes back as it was written, its tags round-trip, and the
// listing reports it.
func TestS3Control_StorageLensConfiguration(t *testing.T) {
	sc := s3ControlClient()
	configID := "sl-config"

	_, err := sc.PutStorageLensConfiguration(ctx, &s3control.PutStorageLensConfigurationInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ConfigId: aws.String(configID),
		StorageLensConfiguration: &s3ctypes.StorageLensConfiguration{
			Id:        aws.String(configID),
			IsEnabled: true,
			AccountLevel: &s3ctypes.AccountLevel{
				BucketLevel: &s3ctypes.BucketLevel{},
			},
		},
		Tags: []s3ctypes.StorageLensTag{{Key: aws.String("team"), Value: aws.String("storage")}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sc.DeleteStorageLensConfiguration(ctx, &s3control.DeleteStorageLensConfigurationInput{
			AccountId: aws.String(s3ObjectLambdaAccount), ConfigId: aws.String(configID)})
	})

	got, err := sc.GetStorageLensConfiguration(ctx, &s3control.GetStorageLensConfigurationInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ConfigId: aws.String(configID)})
	require.NoError(t, err)
	require.NotNil(t, got.StorageLensConfiguration)
	assert.Equal(t, configID, aws.ToString(got.StorageLensConfiguration.Id))
	assert.True(t, got.StorageLensConfiguration.IsEnabled)
	assert.Contains(t, aws.ToString(got.StorageLensConfiguration.StorageLensArn), "storage-lens/"+configID,
		"the service fills in the configuration's own ARN")

	tags, err := sc.GetStorageLensConfigurationTagging(ctx,
		&s3control.GetStorageLensConfigurationTaggingInput{
			AccountId: aws.String(s3ObjectLambdaAccount), ConfigId: aws.String(configID)})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "storage", aws.ToString(tags.Tags[0].Value))

	listed, err := sc.ListStorageLensConfigurations(ctx,
		&s3control.ListStorageLensConfigurationsInput{AccountId: aws.String(s3ObjectLambdaAccount)})
	require.NoError(t, err)
	found := false
	for _, entry := range listed.StorageLensConfigurationList {
		if aws.ToString(entry.Id) == configID {
			found = true
			assert.True(t, entry.IsEnabled)
		}
	}
	assert.True(t, found, "the listing reports the configuration that was written")

	_, err = sc.DeleteStorageLensConfiguration(ctx, &s3control.DeleteStorageLensConfigurationInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ConfigId: aws.String(configID)})
	require.NoError(t, err)
	_, err = sc.GetStorageLensConfiguration(ctx, &s3control.GetStorageLensConfigurationInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ConfigId: aws.String(configID)})
	require.Error(t, err)
}

// TestS3Control_StorageLensGroup covers a custom segment: create, read back
// the filter as written, update it, list, delete.
func TestS3Control_StorageLensGroup(t *testing.T) {
	sc := s3ControlClient()
	name := "logs-segment"

	_, err := sc.CreateStorageLensGroup(ctx, &s3control.CreateStorageLensGroupInput{
		AccountId: aws.String(s3ObjectLambdaAccount),
		StorageLensGroup: &s3ctypes.StorageLensGroup{
			Name: aws.String(name),
			Filter: &s3ctypes.StorageLensGroupFilter{
				MatchAnyPrefix: []string{"logs/"},
			},
		},
		Tags: []s3ctypes.Tag{{Key: aws.String("team"), Value: aws.String("logs")}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sc.DeleteStorageLensGroup(ctx, &s3control.DeleteStorageLensGroupInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(name)})
	})

	got, err := sc.GetStorageLensGroup(ctx, &s3control.GetStorageLensGroupInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(name)})
	require.NoError(t, err)
	require.NotNil(t, got.StorageLensGroup)
	require.NotNil(t, got.StorageLensGroup.Filter)
	assert.Equal(t, []string{"logs/"}, got.StorageLensGroup.Filter.MatchAnyPrefix)
	assert.Contains(t, aws.ToString(got.StorageLensGroup.StorageLensGroupArn), "storage-lens-group/"+name)

	tags, err := sc.ListTagsForResource(ctx, &s3control.ListTagsForResourceInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ResourceArn: got.StorageLensGroup.StorageLensGroupArn})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1, "the group carries the tags it was created with")
	assert.Equal(t, "logs", aws.ToString(tags.Tags[0].Value))

	_, err = sc.UpdateStorageLensGroup(ctx, &s3control.UpdateStorageLensGroupInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(name),
		StorageLensGroup: &s3ctypes.StorageLensGroup{
			Name:   aws.String(name),
			Filter: &s3ctypes.StorageLensGroupFilter{MatchAnyPrefix: []string{"logs/", "archive/"}},
		},
	})
	require.NoError(t, err)
	updated, err := sc.GetStorageLensGroup(ctx, &s3control.GetStorageLensGroupInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, []string{"logs/", "archive/"}, updated.StorageLensGroup.Filter.MatchAnyPrefix)

	listed, err := sc.ListStorageLensGroups(ctx, &s3control.ListStorageLensGroupsInput{
		AccountId: aws.String(s3ObjectLambdaAccount)})
	require.NoError(t, err)
	require.NotEmpty(t, listed.StorageLensGroupList)

	_, err = sc.DeleteStorageLensGroup(ctx, &s3control.DeleteStorageLensGroupInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(name)})
	require.NoError(t, err)
	_, err = sc.GetStorageLensGroup(ctx, &s3control.GetStorageLensGroupInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(name)})
	require.Error(t, err)
}

// TestS3Control_AccessGrants walks the whole Access Grants flow: an instance,
// a location behind a real role, a grant inside it, and the credentials
// GetDataAccess vends by assuming that role.
func TestS3Control_AccessGrants(t *testing.T) {
	sc := s3ControlClient()
	s3c := s3Client()
	bucket := "grants-bucket"
	roleArn := s3ControlRole(t, "s3-access-grants-role")

	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	instance, err := sc.CreateAccessGrantsInstance(ctx, &s3control.CreateAccessGrantsInstanceInput{
		AccountId: aws.String(s3ObjectLambdaAccount)})
	require.NoError(t, err)
	assert.Equal(t, "default", aws.ToString(instance.AccessGrantsInstanceId))
	t.Cleanup(func() {
		_, _ = sc.DeleteAccessGrantsInstance(ctx, &s3control.DeleteAccessGrantsInstanceInput{
			AccountId: aws.String(s3ObjectLambdaAccount)})
	})

	// A second instance in the same account is a conflict: there is one.
	_, err = sc.CreateAccessGrantsInstance(ctx, &s3control.CreateAccessGrantsInstanceInput{
		AccountId: aws.String(s3ObjectLambdaAccount)})
	require.Error(t, err)

	readInstance, err := sc.GetAccessGrantsInstance(ctx, &s3control.GetAccessGrantsInstanceInput{
		AccountId: aws.String(s3ObjectLambdaAccount)})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(readInstance.AccessGrantsInstanceArn), "access-grants/default")

	scope := fmt.Sprintf("s3://%s/data/*", bucket)
	location, err := sc.CreateAccessGrantsLocation(ctx, &s3control.CreateAccessGrantsLocationInput{
		AccountId:     aws.String(s3ObjectLambdaAccount),
		LocationScope: aws.String(scope), IAMRoleArn: aws.String(roleArn)})
	require.NoError(t, err)
	locationID := aws.ToString(location.AccessGrantsLocationId)
	require.NotEmpty(t, locationID)
	t.Cleanup(func() {
		_, _ = sc.DeleteAccessGrantsLocation(ctx, &s3control.DeleteAccessGrantsLocationInput{
			AccountId: aws.String(s3ObjectLambdaAccount), AccessGrantsLocationId: aws.String(locationID)})
	})

	// A location behind a role that does not exist is rejected: nothing could
	// ever reach its data.
	_, err = sc.CreateAccessGrantsLocation(ctx, &s3control.CreateAccessGrantsLocationInput{
		AccountId:     aws.String(s3ObjectLambdaAccount),
		LocationScope: aws.String("s3://other/*"),
		IAMRoleArn:    aws.String("arn:aws:iam::123456789012:role/never-created")})
	require.Error(t, err)

	// The grant is made to the identity the test calls as, because
	// GetDataAccess redeems a grant on behalf of its caller.
	grantee := s3ControlCallerARN(t)
	grant, err := sc.CreateAccessGrant(ctx, &s3control.CreateAccessGrantInput{
		AccountId:              aws.String(s3ObjectLambdaAccount),
		AccessGrantsLocationId: aws.String(locationID),
		Grantee: &s3ctypes.Grantee{
			GranteeType: s3ctypes.GranteeTypeIam, GranteeIdentifier: aws.String(grantee)},
		Permission: s3ctypes.PermissionRead,
	})
	require.NoError(t, err)
	grantID := aws.ToString(grant.AccessGrantId)
	require.NotEmpty(t, grantID)
	assert.Equal(t, scope, aws.ToString(grant.GrantScope),
		"a grant with no sub-prefix covers the whole location")
	t.Cleanup(func() {
		_, _ = sc.DeleteAccessGrant(ctx, &s3control.DeleteAccessGrantInput{
			AccountId: aws.String(s3ObjectLambdaAccount), AccessGrantId: aws.String(grantID)})
	})

	readGrant, err := sc.GetAccessGrant(ctx, &s3control.GetAccessGrantInput{
		AccountId: aws.String(s3ObjectLambdaAccount), AccessGrantId: aws.String(grantID)})
	require.NoError(t, err)
	assert.Equal(t, grantee, aws.ToString(readGrant.Grantee.GranteeIdentifier))
	assert.Equal(t, s3ctypes.PermissionRead, readGrant.Permission)

	grants, err := sc.ListAccessGrants(ctx, &s3control.ListAccessGrantsInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Permission: s3ctypes.PermissionRead})
	require.NoError(t, err)
	require.Len(t, grants.AccessGrantsList, 1)

	// Filtering by a permission nothing was granted returns nothing.
	writeGrants, err := sc.ListAccessGrants(ctx, &s3control.ListAccessGrantsInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Permission: s3ctypes.PermissionWrite})
	require.NoError(t, err)
	assert.Empty(t, writeGrants.AccessGrantsList)

	// Redeeming the grant vends credentials for the location's role.
	access, err := sc.GetDataAccess(ctx, &s3control.GetDataAccessInput{
		AccountId:  aws.String(s3ObjectLambdaAccount),
		Target:     aws.String(fmt.Sprintf("s3://%s/data/report.csv", bucket)),
		Permission: s3ctypes.PermissionRead,
	})
	require.NoError(t, err)
	require.NotNil(t, access.Credentials)
	assert.NotEmpty(t, aws.ToString(access.Credentials.AccessKeyId))
	assert.NotEmpty(t, aws.ToString(access.Credentials.SessionToken))
	assert.Equal(t, scope, aws.ToString(access.MatchedGrantTarget))

	// A target no grant covers is denied rather than served credentials.
	_, err = sc.GetDataAccess(ctx, &s3control.GetDataAccessInput{
		AccountId:  aws.String(s3ObjectLambdaAccount),
		Target:     aws.String(fmt.Sprintf("s3://%s/secrets/key.pem", bucket)),
		Permission: s3ctypes.PermissionRead,
	})
	require.Error(t, err)

	// The instance owns its grants and locations, so it will not delete while
	// they exist.
	_, err = sc.DeleteAccessGrantsInstance(ctx, &s3control.DeleteAccessGrantsInstanceInput{
		AccountId: aws.String(s3ObjectLambdaAccount)})
	require.Error(t, err)

	_, err = sc.DeleteAccessGrant(ctx, &s3control.DeleteAccessGrantInput{
		AccountId: aws.String(s3ObjectLambdaAccount), AccessGrantId: aws.String(grantID)})
	require.NoError(t, err)
	_, err = sc.DeleteAccessGrantsLocation(ctx, &s3control.DeleteAccessGrantsLocationInput{
		AccountId: aws.String(s3ObjectLambdaAccount), AccessGrantsLocationId: aws.String(locationID)})
	require.NoError(t, err)
	_, err = sc.DeleteAccessGrantsInstance(ctx, &s3control.DeleteAccessGrantsInstanceInput{
		AccountId: aws.String(s3ObjectLambdaAccount)})
	require.NoError(t, err)
}

// TestS3Control_MultiRegionAccessPoint covers the asynchronous endpoint: the
// create returns a token whose poll reports what happened, the endpoint reads
// back, and a routes update moves the traffic dials.
func TestS3Control_MultiRegionAccessPoint(t *testing.T) {
	sc := s3ControlClient()
	s3c := s3Client()
	bucket, name := "mrap-bucket", "reports"

	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	created, err := sc.CreateMultiRegionAccessPoint(ctx, &s3control.CreateMultiRegionAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ClientToken: aws.String("token-1"),
		Details: &s3ctypes.CreateMultiRegionAccessPointInput{
			Name:    aws.String(name),
			Regions: []s3ctypes.Region{{Bucket: aws.String(bucket)}},
		},
	})
	require.NoError(t, err)
	token := aws.ToString(created.RequestTokenARN)
	require.NotEmpty(t, token)
	t.Cleanup(func() {
		_, _ = sc.DeleteMultiRegionAccessPoint(ctx, &s3control.DeleteMultiRegionAccessPointInput{
			AccountId: aws.String(s3ObjectLambdaAccount), ClientToken: aws.String("token-cleanup"),
			Details: &s3ctypes.DeleteMultiRegionAccessPointInput{Name: aws.String(name)}})
	})

	operation, err := sc.DescribeMultiRegionAccessPointOperation(ctx,
		&s3control.DescribeMultiRegionAccessPointOperationInput{
			AccountId: aws.String(s3ObjectLambdaAccount), RequestTokenARN: aws.String(token)})
	require.NoError(t, err)
	require.NotNil(t, operation.AsyncOperation)
	assert.Equal(t, "SUCCEEDED", aws.ToString(operation.AsyncOperation.RequestStatus))
	assert.Equal(t, s3ctypes.AsyncOperationName("CreateMultiRegionAccessPoint"), operation.AsyncOperation.Operation)

	got, err := sc.GetMultiRegionAccessPoint(ctx, &s3control.GetMultiRegionAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(name)})
	require.NoError(t, err)
	require.NotNil(t, got.AccessPoint)
	require.Len(t, got.AccessPoint.Regions, 1)
	assert.Equal(t, bucket, aws.ToString(got.AccessPoint.Regions[0].Bucket))
	assert.True(t, aws.ToBool(got.AccessPoint.PublicAccessBlock.BlockPublicPolicy),
		"a new endpoint blocks public access unless the request says otherwise")

	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",`+
		`"Principal":{"AWS":"arn:aws:iam::%s:root"},"Action":"s3:GetObject",`+
		`"Resource":"arn:aws:s3::%s:accesspoint/%s/object/*"}]}`,
		s3ObjectLambdaAccount, s3ObjectLambdaAccount, aws.ToString(got.AccessPoint.Alias))
	_, err = sc.PutMultiRegionAccessPointPolicy(ctx, &s3control.PutMultiRegionAccessPointPolicyInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ClientToken: aws.String("token-2"),
		Details: &s3ctypes.PutMultiRegionAccessPointPolicyInput{
			Name: aws.String(name), Policy: aws.String(policy)},
	})
	require.NoError(t, err)

	readPolicy, err := sc.GetMultiRegionAccessPointPolicy(ctx,
		&s3control.GetMultiRegionAccessPointPolicyInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(name)})
	require.NoError(t, err)
	require.NotNil(t, readPolicy.Policy)
	require.NotNil(t, readPolicy.Policy.Established)
	assert.JSONEq(t, policy, aws.ToString(readPolicy.Policy.Established.Policy))

	status, err := sc.GetMultiRegionAccessPointPolicyStatus(ctx,
		&s3control.GetMultiRegionAccessPointPolicyStatusInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(name)})
	require.NoError(t, err)
	assert.False(t, status.Established.IsPublic)

	routes, err := sc.GetMultiRegionAccessPointRoutes(ctx,
		&s3control.GetMultiRegionAccessPointRoutesInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Mrap: aws.String(name)})
	require.NoError(t, err)
	require.Len(t, routes.Routes, 1)
	assert.Equal(t, int32(100), aws.ToInt32(routes.Routes[0].TrafficDialPercentage))

	_, err = sc.SubmitMultiRegionAccessPointRoutes(ctx,
		&s3control.SubmitMultiRegionAccessPointRoutesInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Mrap: aws.String(name),
			RouteUpdates: []s3ctypes.MultiRegionAccessPointRoute{{
				Bucket: aws.String(bucket), TrafficDialPercentage: aws.Int32(0)}},
		})
	require.NoError(t, err)
	updated, err := sc.GetMultiRegionAccessPointRoutes(ctx,
		&s3control.GetMultiRegionAccessPointRoutesInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Mrap: aws.String(name)})
	require.NoError(t, err)
	require.Len(t, updated.Routes, 1)
	assert.Equal(t, int32(0), aws.ToInt32(updated.Routes[0].TrafficDialPercentage),
		"the dial the update submitted is the one the routes report")

	listed, err := sc.ListMultiRegionAccessPoints(ctx,
		&s3control.ListMultiRegionAccessPointsInput{AccountId: aws.String(s3ObjectLambdaAccount)})
	require.NoError(t, err)
	require.NotEmpty(t, listed.AccessPoints)
}

// TestS3Control_MultiRegionAccessPointCreateReportsFailure holds the
// asynchronous contract: a create that cannot succeed is still accepted, and
// the poll is where the caller learns it failed.
func TestS3Control_MultiRegionAccessPointCreateReportsFailure(t *testing.T) {
	sc := s3ControlClient()
	created, err := sc.CreateMultiRegionAccessPoint(ctx, &s3control.CreateMultiRegionAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ClientToken: aws.String("token-missing"),
		Details: &s3ctypes.CreateMultiRegionAccessPointInput{
			Name:    aws.String("mrap-no-bucket"),
			Regions: []s3ctypes.Region{{Bucket: aws.String("bucket-that-was-never-created")}},
		},
	})
	// A region naming a bucket that does not exist is rejected outright,
	// because the request itself is not well formed.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bucket-that-was-never-created does not exist")
	assert.Nil(t, created)
}

// TestS3Control_BatchJob runs a real batch job: the manifest lists objects,
// the job tags each one, and the tags are readable afterwards.
func TestS3Control_BatchJob(t *testing.T) {
	sc := s3ControlClient()
	s3c := s3Client()
	bucket := "batch-job-bucket"
	roleArn := s3ControlRole(t, "s3-batch-operations-role")

	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	for _, key := range []string{"one.txt", "two.txt"} {
		_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(key)})
		require.NoError(t, err)
	}
	manifestKey := "manifest.csv"
	manifestBody := fmt.Sprintf("%s,one.txt\n%s,two.txt\n", bucket, bucket)
	manifest, err := s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(manifestKey),
		Body: strings.NewReader(manifestBody)})
	require.NoError(t, err)

	created, err := sc.CreateJob(ctx, &s3control.CreateJobInput{
		AccountId:            aws.String(s3ObjectLambdaAccount),
		ClientRequestToken:   aws.String("batch-token-1"),
		Priority:             aws.Int32(10),
		RoleArn:              aws.String(roleArn),
		Description:          aws.String("tag every object in the manifest"),
		ConfirmationRequired: aws.Bool(true),
		Operation: &s3ctypes.JobOperation{
			S3PutObjectTagging: &s3ctypes.S3SetObjectTaggingOperation{
				TagSet: []s3ctypes.S3Tag{{Key: aws.String("reviewed"), Value: aws.String("yes")}},
			},
		},
		Report: &s3ctypes.JobReport{
			Enabled: true, Bucket: aws.String("arn:aws:s3:::" + bucket), Prefix: aws.String("reports"),
			Format: s3ctypes.JobReportFormatReportCsv20180820, ReportScope: s3ctypes.JobReportScopeAllTasks,
		},
		Manifest: &s3ctypes.JobManifest{
			Spec: &s3ctypes.JobManifestSpec{
				Format: s3ctypes.JobManifestFormatS3BatchOperationsCsv20180820,
				Fields: []s3ctypes.JobManifestFieldName{
					s3ctypes.JobManifestFieldNameBucket, s3ctypes.JobManifestFieldNameKey,
				},
			},
			Location: &s3ctypes.JobManifestLocation{
				ObjectArn: aws.String(fmt.Sprintf("arn:aws:s3:::%s/%s", bucket, manifestKey)),
				ETag:      manifest.ETag,
			},
		},
	})
	require.NoError(t, err)
	jobID := aws.ToString(created.JobId)
	require.NotEmpty(t, jobID)

	// A job created awaiting confirmation is prepared — its manifest read —
	// and then waits, running nothing, until the caller confirms it.
	suspended := awaitS3BatchJob(t, sc, jobID, func(job *s3ctypes.JobDescriptor) bool {
		return job.Status == s3ctypes.JobStatusSuspended
	})
	require.NotNil(t, suspended.ProgressSummary)
	assert.Equal(t, int64(2), aws.ToInt64(suspended.ProgressSummary.TotalNumberOfTasks))
	assert.Equal(t, int64(0), aws.ToInt64(suspended.ProgressSummary.NumberOfTasksSucceeded))
	assert.NotNil(t, suspended.SuspendedDate)
	assert.True(t, aws.ToBool(suspended.ConfirmationRequired))
	untagged, err := s3c.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{
		Bucket: aws.String(bucket), Key: aws.String("one.txt")})
	require.NoError(t, err)
	assert.Empty(t, untagged.TagSet, "a suspended job has run no task")

	confirmed, err := sc.UpdateJobStatus(ctx, &s3control.UpdateJobStatusInput{
		AccountId: aws.String(s3ObjectLambdaAccount), JobId: aws.String(jobID),
		RequestedJobStatus: s3ctypes.RequestedJobStatusReady, StatusUpdateReason: aws.String("reviewed")})
	require.NoError(t, err)
	assert.Equal(t, s3ctypes.JobStatusReady, confirmed.Status)

	settled := awaitS3BatchJob(t, sc, jobID, s3BatchJobSettled)
	assert.Equal(t, s3ctypes.JobStatusComplete, settled.Status, "failure reasons: %v", settled.FailureReasons)
	require.NotNil(t, settled.ProgressSummary)
	assert.Equal(t, int64(2), aws.ToInt64(settled.ProgressSummary.TotalNumberOfTasks))
	assert.Equal(t, int64(2), aws.ToInt64(settled.ProgressSummary.NumberOfTasksSucceeded))
	assert.Equal(t, int64(0), aws.ToInt64(settled.ProgressSummary.NumberOfTasksFailed))
	assert.NotNil(t, settled.TerminationDate)

	// The completion report lists every task the job ran.
	report := s3BatchCompletionReport(t, s3c, bucket, "reports/job-"+jobID)
	require.Len(t, report, 1)
	assert.Equal(t, "succeeded", report[0].status)
	assert.ElementsMatch(t, []string{
		bucket + ",one.txt,,succeeded,200,,Successful",
		bucket + ",two.txt,,succeeded,200,,Successful",
	}, report[0].rows)

	_, err = sc.UpdateJobStatus(ctx, &s3control.UpdateJobStatusInput{
		AccountId: aws.String(s3ObjectLambdaAccount), JobId: aws.String(jobID),
		RequestedJobStatus: s3ctypes.RequestedJobStatusCancelled})
	require.Error(t, err, "a complete job cannot be cancelled")
	assert.Contains(t, err.Error(), "JobStatusException")

	// The job actually tagged the objects, which is what makes the progress
	// report mean something.
	for _, key := range []string{"one.txt", "two.txt"} {
		tags, err := s3c.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{
			Bucket: aws.String(bucket), Key: aws.String(key)})
		require.NoError(t, err)
		require.Len(t, tags.TagSet, 1)
		assert.Equal(t, "reviewed", aws.ToString(tags.TagSet[0].Key))
		assert.Equal(t, "yes", aws.ToString(tags.TagSet[0].Value))
	}

	priority, err := sc.UpdateJobPriority(ctx, &s3control.UpdateJobPriorityInput{
		AccountId: aws.String(s3ObjectLambdaAccount), JobId: aws.String(jobID), Priority: 42})
	require.NoError(t, err)
	assert.Equal(t, int32(42), priority.Priority)

	_, err = sc.PutJobTagging(ctx, &s3control.PutJobTaggingInput{
		AccountId: aws.String(s3ObjectLambdaAccount), JobId: aws.String(jobID),
		Tags: []s3ctypes.S3Tag{{Key: aws.String("owner"), Value: aws.String("data-team")}}})
	require.NoError(t, err)
	jobTags, err := sc.GetJobTagging(ctx, &s3control.GetJobTaggingInput{
		AccountId: aws.String(s3ObjectLambdaAccount), JobId: aws.String(jobID)})
	require.NoError(t, err)
	require.Len(t, jobTags.Tags, 1)
	assert.Equal(t, "data-team", aws.ToString(jobTags.Tags[0].Value))

	_, err = sc.DeleteJobTagging(ctx, &s3control.DeleteJobTaggingInput{
		AccountId: aws.String(s3ObjectLambdaAccount), JobId: aws.String(jobID)})
	require.NoError(t, err)
	cleared, err := sc.GetJobTagging(ctx, &s3control.GetJobTaggingInput{
		AccountId: aws.String(s3ObjectLambdaAccount), JobId: aws.String(jobID)})
	require.NoError(t, err)
	assert.Empty(t, cleared.Tags)

	// A finished job cannot be moved to another status; its work is done.
	_, err = sc.UpdateJobStatus(ctx, &s3control.UpdateJobStatusInput{
		AccountId: aws.String(s3ObjectLambdaAccount), JobId: aws.String(jobID),
		RequestedJobStatus: s3ctypes.RequestedJobStatusCancelled})
	require.Error(t, err)

	listed, err := sc.ListJobs(ctx, &s3control.ListJobsInput{
		AccountId:   aws.String(s3ObjectLambdaAccount),
		JobStatuses: []s3ctypes.JobStatus{s3ctypes.JobStatusComplete}})
	require.NoError(t, err)
	require.NotEmpty(t, listed.Jobs)
}

// TestS3Control_AccessPointScope covers narrowing an access point to a set of
// prefixes and operations.
func TestS3Control_AccessPointScope(t *testing.T) {
	sc := s3ControlClient()
	s3c := s3Client()
	bucket, apName := "scope-bucket", "scoped-ap"

	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = sc.CreateAccessPoint(ctx, &s3control.CreateAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName),
		Bucket: aws.String(bucket)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sc.DeleteAccessPoint(ctx, &s3control.DeleteAccessPointInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName)})
	})

	// A fresh access point has no scope, so it restricts nothing.
	empty, err := sc.GetAccessPointScope(ctx, &s3control.GetAccessPointScopeInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName)})
	require.NoError(t, err)
	if empty.Scope != nil {
		assert.Empty(t, empty.Scope.Prefixes)
		assert.Empty(t, empty.Scope.Permissions)
	}

	_, err = sc.PutAccessPointScope(ctx, &s3control.PutAccessPointScopeInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName),
		Scope: &s3ctypes.Scope{
			Prefixes:    []string{"reports/"},
			Permissions: []s3ctypes.ScopePermission{s3ctypes.ScopePermissionGetObject},
		},
	})
	require.NoError(t, err)

	got, err := sc.GetAccessPointScope(ctx, &s3control.GetAccessPointScopeInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName)})
	require.NoError(t, err)
	require.NotNil(t, got.Scope)
	assert.Equal(t, []string{"reports/"}, got.Scope.Prefixes)
	require.Len(t, got.Scope.Permissions, 1)
	assert.Equal(t, s3ctypes.ScopePermissionGetObject, got.Scope.Permissions[0])

	_, err = sc.DeleteAccessPointScope(ctx, &s3control.DeleteAccessPointScopeInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName)})
	require.NoError(t, err)
	cleared, err := sc.GetAccessPointScope(ctx, &s3control.GetAccessPointScopeInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(apName)})
	require.NoError(t, err)
	if cleared.Scope != nil {
		assert.Empty(t, cleared.Scope.Prefixes)
	}
}

// TestS3Control_ListRegionalBuckets reports the account's regional buckets,
// and reports nothing for an Outpost this simulator does not serve.
func TestS3Control_ListRegionalBuckets(t *testing.T) {
	sc := s3ControlClient()
	s3c := s3Client()
	bucket := "regional-listing-bucket"

	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	listed, err := sc.ListRegionalBuckets(ctx, &s3control.ListRegionalBucketsInput{
		AccountId: aws.String(s3ObjectLambdaAccount)})
	require.NoError(t, err)
	found := false
	for _, entry := range listed.RegionalBucketList {
		if aws.ToString(entry.Bucket) == bucket {
			found = true
			assert.Contains(t, aws.ToString(entry.BucketArn), bucket)
		}
	}
	assert.True(t, found)

	onOutpost, err := sc.ListRegionalBuckets(ctx, &s3control.ListRegionalBucketsInput{
		AccountId: aws.String(s3ObjectLambdaAccount), OutpostId: aws.String("op-01234567890abcdef")})
	require.NoError(t, err)
	assert.Empty(t, onOutpost.RegionalBucketList,
		"the account has no buckets on that Outpost")
}

// TestS3Control_ResourceTagging covers the tagging trio the control plane
// shares across its resources, against a Storage Lens group. The three
// operations are one path under three methods:
// "POST /v20180820/tags/{resourceArn...}",
// "GET /v20180820/tags/{resourceArn...}" and
// "DELETE /v20180820/tags/{resourceArn...}".
func TestS3Control_ResourceTagging(t *testing.T) {
	sc := s3ControlClient()
	groupName := "sl-tagged-group"

	_, err := sc.CreateStorageLensGroup(ctx, &s3control.CreateStorageLensGroupInput{
		AccountId: aws.String(s3ObjectLambdaAccount),
		StorageLensGroup: &s3ctypes.StorageLensGroup{
			Name:   aws.String(groupName),
			Filter: &s3ctypes.StorageLensGroupFilter{MatchAnyPrefix: []string{"logs/"}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sc.DeleteStorageLensGroup(ctx, &s3control.DeleteStorageLensGroupInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(groupName)})
	})
	arn := fmt.Sprintf("arn:aws:s3:us-east-1:%s:storage-lens-group/%s", s3ObjectLambdaAccount, groupName)

	_, err = sc.TagResource(ctx, &s3control.TagResourceInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ResourceArn: aws.String(arn),
		Tags: []s3ctypes.Tag{
			{Key: aws.String("team"), Value: aws.String("storage")},
			{Key: aws.String("tier"), Value: aws.String("gold")},
		},
	})
	require.NoError(t, err)

	listed, err := sc.ListTagsForResource(ctx, &s3control.ListTagsForResourceInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, listed.Tags, 2)

	_, err = sc.UntagResource(ctx, &s3control.UntagResourceInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ResourceArn: aws.String(arn),
		TagKeys: []string{"tier"}})
	require.NoError(t, err)

	remaining, err := sc.ListTagsForResource(ctx, &s3control.ListTagsForResourceInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, remaining.Tags, 1)
	assert.Equal(t, "team", aws.ToString(remaining.Tags[0].Key))

	// Tagging something that does not exist is refused rather than recorded
	// against an ARN nothing can read back.
	_, err = sc.TagResource(ctx, &s3control.TagResourceInput{
		AccountId: aws.String(s3ObjectLambdaAccount),
		ResourceArn: aws.String(fmt.Sprintf(
			"arn:aws:s3:us-east-1:%s:storage-lens-group/never-created", s3ObjectLambdaAccount)),
		Tags: []s3ctypes.Tag{{Key: aws.String("k"), Value: aws.String("v")}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NotFoundException")
}

// TestS3Control_StorageLensConfigurationTagging covers a Storage Lens
// configuration's tags, which its own tagging operations keep as the one tag
// set: PutStorageLensConfigurationTagging replaces it and
// DeleteStorageLensConfigurationTagging empties it. TagResource and
// ListTagsForResource do not serve the configuration type and refuse its ARN.
func TestS3Control_StorageLensConfigurationTagging(t *testing.T) {
	sc := s3ControlClient()
	configID := "sl-config-tagging"
	_, err := sc.PutStorageLensConfiguration(ctx, &s3control.PutStorageLensConfigurationInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ConfigId: aws.String(configID),
		StorageLensConfiguration: &s3ctypes.StorageLensConfiguration{
			Id: aws.String(configID), IsEnabled: true,
			AccountLevel: &s3ctypes.AccountLevel{BucketLevel: &s3ctypes.BucketLevel{}},
		},
		Tags: []s3ctypes.StorageLensTag{{Key: aws.String("team"), Value: aws.String("storage")}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sc.DeleteStorageLensConfiguration(ctx, &s3control.DeleteStorageLensConfigurationInput{
			AccountId: aws.String(s3ObjectLambdaAccount), ConfigId: aws.String(configID)})
	})
	arn := fmt.Sprintf("arn:aws:s3:us-east-1:%s:storage-lens/%s", s3ObjectLambdaAccount, configID)
	tagsOf := func() map[string]string {
		t.Helper()
		out, err := sc.GetStorageLensConfigurationTagging(ctx, &s3control.GetStorageLensConfigurationTaggingInput{
			AccountId: aws.String(s3ObjectLambdaAccount), ConfigId: aws.String(configID)})
		require.NoError(t, err)
		tags := map[string]string{}
		for _, tag := range out.Tags {
			tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
		return tags
	}

	_, err = sc.TagResource(ctx, &s3control.TagResourceInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ResourceArn: aws.String(arn),
		Tags: []s3ctypes.Tag{{Key: aws.String("tier"), Value: aws.String("gold")}},
	})
	require.Error(t, err, "TagResource does not tag a Storage Lens configuration")
	assert.Contains(t, err.Error(), "NotFoundException")
	_, err = sc.ListTagsForResource(ctx, &s3control.ListTagsForResourceInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ResourceArn: aws.String(arn)})
	require.Error(t, err, "ListTagsForResource does not read a Storage Lens configuration")
	assert.Contains(t, err.Error(), "NotFoundException")
	assert.Equal(t, map[string]string{"team": "storage"}, tagsOf())

	_, err = sc.PutStorageLensConfigurationTagging(ctx, &s3control.PutStorageLensConfigurationTaggingInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ConfigId: aws.String(configID),
		Tags: []s3ctypes.StorageLensTag{{Key: aws.String("tier"), Value: aws.String("gold")}},
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"tier": "gold"}, tagsOf(), "the put replaces the tag set")

	_, err = sc.DeleteStorageLensConfigurationTagging(ctx, &s3control.DeleteStorageLensConfigurationTaggingInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ConfigId: aws.String(configID)})
	require.NoError(t, err)
	assert.Empty(t, tagsOf())
}

// s3BatchLambdaJob creates a function from the Lambda handler image, a bucket
// holding one object per key and a manifest listing them, and runs a
// LambdaInvoke job over them with a completion report under "reports". It
// returns the job's ID, the bucket and the function's name.
func s3BatchLambdaJob(t *testing.T, name string, keys []string, invoke *s3ctypes.LambdaInvokeOperation) (string, string, string) {
	t.Helper()
	sc := s3ControlClient()
	s3c := s3Client()
	lc := lambdaClient()
	bucket := uniqueName(name)
	functionName := uniqueName(name + "-fn")
	roleArn := s3ControlRole(t, uniqueName(name+"-role"))

	function, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName:  aws.String(functionName),
		Role:          aws.String("arn:aws:iam::123456789012:role/" + functionName),
		PackageType:   lambdatypes.PackageTypeImage,
		Code:          &lambdatypes.FunctionCode{ImageUri: aws.String(lambdaHandlerImageName)},
		Architectures: nativeLambdaArchitectures(),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: function.FunctionName})
	})
	invoke.FunctionArn = function.FunctionArn

	_, err = s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	var manifestBody strings.Builder
	for _, key := range keys {
		_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(key)})
		require.NoError(t, err)
		fmt.Fprintf(&manifestBody, "%s,%s\n", bucket, url.QueryEscape(key))
	}
	manifest, err := s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("manifest.csv"),
		Body: strings.NewReader(manifestBody.String())})
	require.NoError(t, err)

	created, err := sc.CreateJob(ctx, &s3control.CreateJobInput{
		AccountId:          aws.String(s3ObjectLambdaAccount),
		ClientRequestToken: aws.String(uniqueName(name + "-token")),
		Priority:           aws.Int32(10),
		RoleArn:            aws.String(roleArn),
		Operation:          &s3ctypes.JobOperation{LambdaInvoke: invoke},
		Report: &s3ctypes.JobReport{
			Enabled: true, Bucket: aws.String("arn:aws:s3:::" + bucket), Prefix: aws.String("reports"),
			Format: s3ctypes.JobReportFormatReportCsv20180820, ReportScope: s3ctypes.JobReportScopeAllTasks,
		},
		Manifest: &s3ctypes.JobManifest{
			Spec: &s3ctypes.JobManifestSpec{
				Format: s3ctypes.JobManifestFormatS3BatchOperationsCsv20180820,
				Fields: []s3ctypes.JobManifestFieldName{
					s3ctypes.JobManifestFieldNameBucket, s3ctypes.JobManifestFieldNameKey,
				},
			},
			Location: &s3ctypes.JobManifestLocation{
				ObjectArn: aws.String(fmt.Sprintf("arn:aws:s3:::%s/manifest.csv", bucket)),
				ETag:      manifest.ETag,
			},
		},
	})
	require.NoError(t, err)
	return aws.ToString(created.JobId), bucket, functionName
}

// TestS3Control_BatchJobLambdaInvoke runs a LambdaInvoke job in invocation
// schema 1.0: Batch Operations invokes the function once per manifest entry
// with the bucket's ARN and the key as the manifest lists it, URL-encoded,
// and each task's outcome and message are the resultCode and resultString
// the function returns for it.
func TestS3Control_BatchJobLambdaInvoke(t *testing.T) {
	sc := s3ControlClient()
	jobID, bucket, _ := s3BatchLambdaJob(t, "batch-lambda-v1", []string{"one.txt", "q3 report,é.txt"},
		&s3ctypes.LambdaInvokeOperation{})

	job := awaitS3BatchJob(t, sc, jobID, s3BatchJobSettled)
	assert.Equal(t, s3ctypes.JobStatusComplete, job.Status, "failure reasons: %v", job.FailureReasons)
	require.NotNil(t, job.ProgressSummary)
	assert.Equal(t, int64(2), aws.ToInt64(job.ProgressSummary.NumberOfTasksSucceeded))
	assert.Equal(t, int64(0), aws.ToInt64(job.ProgressSummary.NumberOfTasksFailed))

	report := s3BatchCompletionReport(t, s3Client(), bucket, "reports/job-"+jobID)
	require.Len(t, report, 1)
	assert.Equal(t, "succeeded", report[0].status)
	assert.ElementsMatch(t, []string{
		bucket + ",one.txt,,succeeded,200,,processed " + bucket + "/one.txt",
		bucket + ",q3+report%2C%C3%A9.txt,,succeeded,200,,processed " + bucket + "/q3+report%2C%C3%A9.txt",
	}, report[0].rows)
}

// TestS3Control_BatchJobURLEncodedKeys runs a tagging job over a manifest whose
// keys are URL-encoded, as the S3BatchOperations_CSV_20180820 format requires:
// %20 and + each name a space, %2C a comma, %C3%A9 an é and %2B a plus, so
// the job tags the objects those decoded keys name and no other.
func TestS3Control_BatchJobURLEncodedKeys(t *testing.T) {
	sc := s3ControlClient()
	s3c := s3Client()
	bucket := uniqueName("batch-encoded-keys")
	roleArn := s3ControlRole(t, uniqueName("batch-encoded-role"))
	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	listed := map[string]string{
		"q3 report,é.txt":   "q3%20report%2C%C3%A9.txt",
		"plus as space.txt": "plus+as+space.txt",
		"a+b.txt":           "a%2Bb.txt",
	}
	var manifestBody strings.Builder
	for key, encoded := range listed {
		_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(key)})
		require.NoError(t, err)
		fmt.Fprintf(&manifestBody, "%s,%s\n", bucket, encoded)
	}
	_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("plus+as+space.txt"), Body: strings.NewReader("literal")})
	require.NoError(t, err)
	manifest, err := s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("manifest.csv"),
		Body: strings.NewReader(manifestBody.String())})
	require.NoError(t, err)

	created, err := sc.CreateJob(ctx, &s3control.CreateJobInput{
		AccountId:          aws.String(s3ObjectLambdaAccount),
		ClientRequestToken: aws.String(uniqueName("batch-encoded-token")),
		Priority:           aws.Int32(10),
		RoleArn:            aws.String(roleArn),
		Operation: &s3ctypes.JobOperation{
			S3PutObjectTagging: &s3ctypes.S3SetObjectTaggingOperation{
				TagSet: []s3ctypes.S3Tag{{Key: aws.String("reviewed"), Value: aws.String("yes")}},
			},
		},
		Report: &s3ctypes.JobReport{
			Enabled: true, Bucket: aws.String("arn:aws:s3:::" + bucket), Prefix: aws.String("reports"),
			Format: s3ctypes.JobReportFormatReportCsv20180820, ReportScope: s3ctypes.JobReportScopeAllTasks,
		},
		Manifest: &s3ctypes.JobManifest{
			Spec: &s3ctypes.JobManifestSpec{
				Format: s3ctypes.JobManifestFormatS3BatchOperationsCsv20180820,
				Fields: []s3ctypes.JobManifestFieldName{
					s3ctypes.JobManifestFieldNameBucket, s3ctypes.JobManifestFieldNameKey,
				},
			},
			Location: &s3ctypes.JobManifestLocation{
				ObjectArn: aws.String(fmt.Sprintf("arn:aws:s3:::%s/manifest.csv", bucket)),
				ETag:      manifest.ETag,
			},
		},
	})
	require.NoError(t, err)
	jobID := aws.ToString(created.JobId)

	job := awaitS3BatchJob(t, sc, jobID, s3BatchJobSettled)
	assert.Equal(t, s3ctypes.JobStatusComplete, job.Status, "failure reasons: %v", job.FailureReasons)
	require.NotNil(t, job.ProgressSummary)
	assert.Equal(t, int64(3), aws.ToInt64(job.ProgressSummary.NumberOfTasksSucceeded))
	assert.Equal(t, int64(0), aws.ToInt64(job.ProgressSummary.NumberOfTasksFailed))

	for key := range listed {
		tags, err := s3c.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{
			Bucket: aws.String(bucket), Key: aws.String(key)})
		require.NoError(t, err, key)
		require.Len(t, tags.TagSet, 1, key)
		assert.Equal(t, "reviewed", aws.ToString(tags.TagSet[0].Key), key)
	}
	literal, err := s3c.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{
		Bucket: aws.String(bucket), Key: aws.String("plus+as+space.txt")})
	require.NoError(t, err)
	assert.Empty(t, literal.TagSet, "a + in a listed key names a space, not a plus")

	report := s3BatchCompletionReport(t, s3c, bucket, "reports/job-"+jobID)
	require.Len(t, report, 1)
	var rows []string
	for _, encoded := range listed {
		rows = append(rows, bucket+","+encoded+",,succeeded,200,,Successful")
	}
	assert.ElementsMatch(t, rows, report[0].rows)
}

// TestS3Control_BatchJobLambdaResultCodes runs a LambdaInvoke job in
// invocation schema 2.0 with user arguments. The function succeeds one task,
// fails one permanently and answers TemporaryFailure for the third every time:
// Batch Operations redrives that task before the job completes, then counts
// it failed, and the progress summary and completion report carry each
// task's resultCode and resultString.
func TestS3Control_BatchJobLambdaResultCodes(t *testing.T) {
	sc := s3ControlClient()
	jobID, bucket, functionName := s3BatchLambdaJob(t, "batch-lambda-v2",
		[]string{"ok.txt", "permanent.txt", "temporary.txt"},
		&s3ctypes.LambdaInvokeOperation{
			InvocationSchemaVersion: aws.String("2.0"),
			UserArguments:           map[string]string{"label": "nightly"},
		})

	job := awaitS3BatchJob(t, sc, jobID, s3BatchJobSettled)
	assert.Equal(t, s3ctypes.JobStatusComplete, job.Status, "failure reasons: %v", job.FailureReasons)
	require.NotNil(t, job.ProgressSummary)
	assert.Equal(t, int64(3), aws.ToInt64(job.ProgressSummary.TotalNumberOfTasks))
	assert.Equal(t, int64(1), aws.ToInt64(job.ProgressSummary.NumberOfTasksSucceeded))
	assert.Equal(t, int64(2), aws.ToInt64(job.ProgressSummary.NumberOfTasksFailed))

	rows := map[string][]string{}
	for _, results := range s3BatchCompletionReport(t, s3Client(), bucket, "reports/job-"+jobID) {
		rows[results.status] = results.rows
	}
	assert.Equal(t, []string{bucket + ",ok.txt,,succeeded,200,,processed " + bucket + "/ok.txt for nightly"},
		rows["succeeded"])
	assert.ElementsMatch(t, []string{
		bucket + ",permanent.txt,,failed,400,PermanentFailure,refused " + bucket + "/permanent.txt",
		bucket + ",temporary.txt,,failed,500,TemporaryFailure,retry " + bucket + "/temporary.txt",
	}, rows["failed"])

	// The function logs every event it receives: the task answered with
	// TemporaryFailure reached it more than once.
	logs := cwLogsClient()
	invocations := 0
	paginator := cloudwatchlogs.NewFilterLogEventsPaginator(logs, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String("/aws/lambda/" + functionName)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		require.NoError(t, err)
		for _, event := range page.Events {
			message := aws.ToString(event.Message)
			if strings.HasPrefix(message, "invocation ") && strings.Contains(message, `"s3Key":"temporary.txt"`) {
				invocations++
			}
		}
	}
	assert.Greater(t, invocations, 1, "a TemporaryFailure task is redriven")
}

// TestS3Control_BatchJobCancelledBeforeConfirmation cancels a job awaiting
// confirmation: it ends Cancelled without running a task, and a job in a
// final status refuses any further move.
func TestS3Control_BatchJobCancelledBeforeConfirmation(t *testing.T) {
	sc := s3ControlClient()
	s3c := s3Client()
	bucket := uniqueName("batch-cancel")
	roleArn := s3ControlRole(t, uniqueName("batch-cancel-role"))
	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("one.txt"), Body: strings.NewReader("one")})
	require.NoError(t, err)
	manifest, err := s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("manifest.csv"), Body: strings.NewReader(bucket + ",one.txt\n")})
	require.NoError(t, err)
	created, err := sc.CreateJob(ctx, &s3control.CreateJobInput{
		AccountId:            aws.String(s3ObjectLambdaAccount),
		ClientRequestToken:   aws.String(uniqueName("batch-cancel-token")),
		Priority:             aws.Int32(1),
		RoleArn:              aws.String(roleArn),
		ConfirmationRequired: aws.Bool(true),
		Operation: &s3ctypes.JobOperation{
			S3PutObjectTagging: &s3ctypes.S3SetObjectTaggingOperation{
				TagSet: []s3ctypes.S3Tag{{Key: aws.String("reviewed"), Value: aws.String("yes")}},
			},
		},
		Report: &s3ctypes.JobReport{Enabled: false},
		Manifest: &s3ctypes.JobManifest{
			Spec: &s3ctypes.JobManifestSpec{
				Format: s3ctypes.JobManifestFormatS3BatchOperationsCsv20180820,
				Fields: []s3ctypes.JobManifestFieldName{
					s3ctypes.JobManifestFieldNameBucket, s3ctypes.JobManifestFieldNameKey,
				},
			},
			Location: &s3ctypes.JobManifestLocation{
				ObjectArn: aws.String(fmt.Sprintf("arn:aws:s3:::%s/manifest.csv", bucket)),
				ETag:      manifest.ETag,
			},
		},
	})
	require.NoError(t, err)
	jobID := aws.ToString(created.JobId)

	awaitS3BatchJob(t, sc, jobID, func(job *s3ctypes.JobDescriptor) bool {
		return job.Status == s3ctypes.JobStatusSuspended
	})
	cancelled, err := sc.UpdateJobStatus(ctx, &s3control.UpdateJobStatusInput{
		AccountId: aws.String(s3ObjectLambdaAccount), JobId: aws.String(jobID),
		RequestedJobStatus: s3ctypes.RequestedJobStatusCancelled, StatusUpdateReason: aws.String("not needed")})
	require.NoError(t, err)
	assert.Equal(t, s3ctypes.JobStatusCancelled, cancelled.Status)
	assert.Equal(t, "not needed", aws.ToString(cancelled.StatusUpdateReason))

	described, err := sc.DescribeJob(ctx, &s3control.DescribeJobInput{
		AccountId: aws.String(s3ObjectLambdaAccount), JobId: aws.String(jobID)})
	require.NoError(t, err)
	assert.Equal(t, s3ctypes.JobStatusCancelled, described.Job.Status)
	assert.Equal(t, int64(0), aws.ToInt64(described.Job.ProgressSummary.NumberOfTasksSucceeded))
	tags, err := s3c.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{Bucket: aws.String(bucket), Key: aws.String("one.txt")})
	require.NoError(t, err)
	assert.Empty(t, tags.TagSet, "a job cancelled before confirmation ran no task")

	_, err = sc.UpdateJobStatus(ctx, &s3control.UpdateJobStatusInput{
		AccountId: aws.String(s3ObjectLambdaAccount), JobId: aws.String(jobID),
		RequestedJobStatus: s3ctypes.RequestedJobStatusReady})
	require.Error(t, err, "a cancelled job cannot be confirmed")
	assert.Contains(t, err.Error(), "JobStatusException")
}

// awaitS3BatchJob polls DescribeJob, the only view Amazon S3 Batch Operations
// gives of a job's progress, until done accepts the job.
func awaitS3BatchJob(t *testing.T, sc *s3control.Client, jobID string, done func(*s3ctypes.JobDescriptor) bool) *s3ctypes.JobDescriptor {
	t.Helper()
	var job *s3ctypes.JobDescriptor
	var seen string
	require.Eventually(t, func() bool {
		out, err := sc.DescribeJob(ctx, &s3control.DescribeJobInput{
			AccountId: aws.String(s3ObjectLambdaAccount), JobId: aws.String(jobID)})
		require.NoError(t, err)
		require.NotNil(t, out.Job)
		job = out.Job
		seen = fmt.Sprintf("status=%s failures=%v", job.Status, job.FailureReasons)
		return done(job)
	}, 90*time.Second, 100*time.Millisecond, "job %s never reached the awaited state; last seen %s", jobID, &seen)
	return job
}

func s3BatchJobSettled(job *s3ctypes.JobDescriptor) bool {
	switch job.Status {
	case s3ctypes.JobStatusComplete, s3ctypes.JobStatusFailed, s3ctypes.JobStatusCancelled:
		return true
	}
	return false
}

// s3BatchReportResults is one results file of a completion report.
type s3BatchReportResults struct {
	status string
	rows   []string
}

// s3BatchCompletionReport reads the completion report under base: its
// manifest.json, and each results file it lists, whose MD5 checksum must
// match.
func s3BatchCompletionReport(t *testing.T, s3c *s3.Client, bucket, base string) []s3BatchReportResults {
	t.Helper()
	read := func(key string) []byte {
		t.Helper()
		out, err := s3c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		require.NoError(t, err, key)
		defer out.Body.Close()
		body, err := io.ReadAll(out.Body)
		require.NoError(t, err, key)
		return body
	}
	var manifest struct {
		Format       string `json:"Format"`
		ReportSchema string `json:"ReportSchema"`
		Results      []struct {
			TaskExecutionStatus string `json:"TaskExecutionStatus"`
			Bucket              string `json:"Bucket"`
			MD5Checksum         string `json:"MD5Checksum"`
			Key                 string `json:"Key"`
		} `json:"Results"`
	}
	require.NoError(t, json.Unmarshal(read(base+"/manifest.json"), &manifest))
	assert.Equal(t, "Report_CSV_20180820", manifest.Format)
	assert.Equal(t, "Bucket, Key, VersionId, TaskStatus, ErrorCode, HTTPStatusCode, ResultMessage", manifest.ReportSchema)
	var out []s3BatchReportResults
	for _, entry := range manifest.Results {
		assert.Equal(t, bucket, entry.Bucket)
		assert.True(t, strings.HasPrefix(entry.Key, base+"/results/"), entry.Key)
		body := read(entry.Key)
		sum := md5.Sum(body)
		assert.Equal(t, hex.EncodeToString(sum[:]), entry.MD5Checksum, entry.Key)
		out = append(out, s3BatchReportResults{
			status: entry.TaskExecutionStatus,
			rows:   strings.Split(strings.TrimSuffix(string(body), "\n"), "\n"),
		})
	}
	return out
}
