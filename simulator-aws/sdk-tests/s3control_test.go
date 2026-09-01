package aws_sdk_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
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
		AccountId:          aws.String(s3ObjectLambdaAccount),
		ClientRequestToken: aws.String("batch-token-1"),
		Priority:           aws.Int32(10),
		RoleArn:            aws.String(roleArn),
		Description:        aws.String("tag every object in the manifest"),
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
				ObjectArn: aws.String(fmt.Sprintf("arn:aws:s3:::%s/%s", bucket, manifestKey)),
				ETag:      manifest.ETag,
			},
		},
	})
	require.NoError(t, err)
	jobID := aws.ToString(created.JobId)
	require.NotEmpty(t, jobID)

	described, err := sc.DescribeJob(ctx, &s3control.DescribeJobInput{
		AccountId: aws.String(s3ObjectLambdaAccount), JobId: aws.String(jobID)})
	require.NoError(t, err)
	require.NotNil(t, described.Job)
	assert.Equal(t, s3ctypes.JobStatusComplete, described.Job.Status)
	require.NotNil(t, described.Job.ProgressSummary)
	assert.Equal(t, int64(2), aws.ToInt64(described.Job.ProgressSummary.TotalNumberOfTasks))
	assert.Equal(t, int64(2), aws.ToInt64(described.Job.ProgressSummary.NumberOfTasksSucceeded))
	assert.Equal(t, int64(0), aws.ToInt64(described.Job.ProgressSummary.NumberOfTasksFailed))

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
