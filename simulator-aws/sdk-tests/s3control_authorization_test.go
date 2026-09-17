package aws_sdk_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3control"
	s3ctypes "github.com/aws/aws-sdk-go-v2/service/s3control/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The S3 control plane is authorized like every other surface: the operation's
// IAM action, against the resource the request names. These tests prove both
// halves per route family — a credential scoped to the resource the request is
// about gets through, and the same grant pointed at another resource of the
// same type is refused, which only holds if the gate derives the ARN rather
// than just the action.

// s3ControlClientWithCreds talks to the control plane as one IAM principal.
func s3ControlClientWithCreds(akid, secret string) *s3control.Client {
	return s3control.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, ""),
		HTTPClient:  simHTTPClient,
	}, func(o *s3control.Options) { o.BaseEndpoint = aws.String(simEndpoint("s3-control")) })
}

// s3ControlPolicy is an identity policy allowing one action on one resource.
func s3ControlPolicy(action, resource string) string {
	return fmt.Sprintf(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":%q,"Resource":%q}]}`,
		action, resource)
}

func s3ControlARN(resource string) string {
	return "arn:aws:s3:us-east-1:" + s3ObjectLambdaAccount + ":" + resource
}

// TestS3Control_AccessGrantsInstanceIsAuthorizedAgainstTheInstance covers the
// Access Grants surface, whose reads and writes AWS authorizes against the
// account's instance ARN.
func TestS3Control_AccessGrantsInstanceIsAuthorizedAgainstTheInstance(t *testing.T) {
	admin := s3ControlClient()
	_, err := admin.CreateAccessGrantsInstance(ctx, &s3control.CreateAccessGrantsInstanceInput{
		AccountId: aws.String(s3ObjectLambdaAccount)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteAccessGrantsInstance(ctx, &s3control.DeleteAccessGrantsInstanceInput{
			AccountId: aws.String(s3ObjectLambdaAccount)})
	})

	allowed := s3ControlClientWithCreds(restrictedCredential(t, "s3control-instance-reader",
		s3ControlPolicy("s3:GetAccessGrantsInstance", s3ControlARN("access-grants/default"))))
	read, err := allowed.GetAccessGrantsInstance(ctx, &s3control.GetAccessGrantsInstanceInput{
		AccountId: aws.String(s3ObjectLambdaAccount)})
	require.NoError(t, err, "the grant names this account's instance")
	assert.Contains(t, aws.ToString(read.AccessGrantsInstanceArn), "access-grants/default")

	elsewhere := s3ControlClientWithCreds(restrictedCredential(t, "s3control-instance-elsewhere",
		s3ControlPolicy("s3:GetAccessGrantsInstance",
			"arn:aws:s3:us-east-1:999988887777:access-grants/default")))
	_, err = elsewhere.GetAccessGrantsInstance(ctx, &s3control.GetAccessGrantsInstanceInput{
		AccountId: aws.String(s3ObjectLambdaAccount)})
	require.Error(t, err, "a grant on another account's instance does not reach this one")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
	assert.Contains(t, err.Error(), "s3:GetAccessGrantsInstance")

	noGrant := s3ControlClientWithCreds(restrictedCredential(t, "s3control-instance-lister",
		s3ControlPolicy("s3:ListAccessGrants", s3ControlARN("access-grants/default"))))
	_, err = noGrant.GetAccessGrantsInstance(ctx, &s3control.GetAccessGrantsInstanceInput{
		AccountId: aws.String(s3ObjectLambdaAccount)})
	require.Error(t, err, "a grant for another action on the same instance is not this action")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
}

// TestS3Control_AccessGrantsLocationAlsoAuthorizesPassingItsRole covers the
// second authorization AWS performs on a registration: the location reaches its
// data through an IAM role, and handing S3 that role is iam:PassRole on the
// role itself. The role arrives in an XML document, which is the only place the
// request names it.
func TestS3Control_AccessGrantsLocationAlsoAuthorizesPassingItsRole(t *testing.T) {
	admin := s3ControlClient()
	s3c := s3Client()
	bucket := "grants-passrole-bucket"
	roleArn := s3ControlRole(t, "s3-grants-passrole-role")

	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = admin.CreateAccessGrantsInstance(ctx, &s3control.CreateAccessGrantsInstanceInput{
		AccountId: aws.String(s3ObjectLambdaAccount)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteAccessGrantsInstance(ctx, &s3control.DeleteAccessGrantsInstanceInput{
			AccountId: aws.String(s3ObjectLambdaAccount)})
	})

	register := func(client *s3control.Client, scope string) error {
		out, err := client.CreateAccessGrantsLocation(ctx, &s3control.CreateAccessGrantsLocationInput{
			AccountId:     aws.String(s3ObjectLambdaAccount),
			LocationScope: aws.String(scope),
			IAMRoleArn:    aws.String(roleArn),
		})
		if err == nil {
			id := aws.ToString(out.AccessGrantsLocationId)
			t.Cleanup(func() {
				_, _ = admin.DeleteAccessGrantsLocation(ctx, &s3control.DeleteAccessGrantsLocationInput{
					AccountId: aws.String(s3ObjectLambdaAccount), AccessGrantsLocationId: aws.String(id)})
			})
		}
		return err
	}

	noPassRole := s3ControlClientWithCreds(restrictedCredential(t, "s3control-location-no-passrole",
		s3ControlPolicy("s3:CreateAccessGrantsLocation",
			s3ControlARN("access-grants/default/location/*"))))
	err = register(noPassRole, fmt.Sprintf("s3://%s/one/*", bucket))
	require.Error(t, err, "creating the location is allowed, passing its role is not")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
	assert.Contains(t, err.Error(), "iam:PassRole")

	both := s3ControlClientWithCreds(restrictedCredential(t, "s3control-location-passrole",
		`{"Version":"2012-10-17","Statement":[
		  {"Effect":"Allow","Action":"s3:CreateAccessGrantsLocation","Resource":"`+
			s3ControlARN("access-grants/default/location/*")+`"},
		  {"Effect":"Allow","Action":"iam:PassRole","Resource":"`+roleArn+`"}]}`))
	require.NoError(t, register(both, fmt.Sprintf("s3://%s/two/*", bucket)),
		"a caller allowed both registers the location")
}

// TestS3Control_BatchJobIsAuthorizedAgainstTheJob covers Batch Operations,
// where everything addressed to one job is authorized against that job's ARN.
func TestS3Control_BatchJobIsAuthorizedAgainstTheJob(t *testing.T) {
	admin := s3ControlClient()
	s3c := s3Client()
	bucket := "batch-auth-bucket"
	roleArn := s3ControlRole(t, "s3-batch-auth-role")

	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("one.txt"), Body: strings.NewReader("one")})
	require.NoError(t, err)
	manifest, err := s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("manifest.csv"),
		Body: strings.NewReader(bucket + ",one.txt\n")})
	require.NoError(t, err)

	created, err := admin.CreateJob(ctx, &s3control.CreateJobInput{
		AccountId:          aws.String(s3ObjectLambdaAccount),
		ClientRequestToken: aws.String("batch-auth-token"),
		Priority:           aws.Int32(5),
		RoleArn:            aws.String(roleArn),
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
	require.NotEmpty(t, jobID)

	allowed := s3ControlClientWithCreds(restrictedCredential(t, "s3control-job-reader",
		s3ControlPolicy("s3:DescribeJob", s3ControlARN("job/"+jobID))))
	described, err := allowed.DescribeJob(ctx, &s3control.DescribeJobInput{
		AccountId: aws.String(s3ObjectLambdaAccount), JobId: aws.String(jobID)})
	require.NoError(t, err, "the grant names this job")
	require.NotNil(t, described.Job)
	assert.Equal(t, jobID, aws.ToString(described.Job.JobId))

	otherJob := s3ControlClientWithCreds(restrictedCredential(t, "s3control-job-other",
		s3ControlPolicy("s3:DescribeJob", s3ControlARN("job/00000000-0000-4000-8000-000000000000"))))
	_, err = otherJob.DescribeJob(ctx, &s3control.DescribeJobInput{
		AccountId: aws.String(s3ObjectLambdaAccount), JobId: aws.String(jobID)})
	require.Error(t, err, "a grant on another job does not describe this one")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
	assert.Contains(t, err.Error(), "s3:DescribeJob")
}

// TestS3Control_AccessPointIsAuthorizedAgainstTheAccessPoint covers the access
// point routes, authorized against the access point's own ARN.
func TestS3Control_AccessPointIsAuthorizedAgainstTheAccessPoint(t *testing.T) {
	admin := s3ControlClient()
	s3c := s3Client()
	bucket, point := "ap-auth-bucket", "ap-auth-point"

	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = admin.CreateAccessPoint(ctx, &s3control.CreateAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(point),
		Bucket: aws.String(bucket)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteAccessPoint(ctx, &s3control.DeleteAccessPointInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(point)})
	})

	allowed := s3ControlClientWithCreds(restrictedCredential(t, "s3control-point-reader",
		s3ControlPolicy("s3:GetAccessPointPolicyStatus", s3ControlARN("accesspoint/"+point))))
	status, err := allowed.GetAccessPointPolicyStatus(ctx, &s3control.GetAccessPointPolicyStatusInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(point)})
	require.NoError(t, err, "the grant names this access point")
	require.NotNil(t, status.PolicyStatus)

	otherPoint := s3ControlClientWithCreds(restrictedCredential(t, "s3control-point-other",
		s3ControlPolicy("s3:GetAccessPointPolicyStatus", s3ControlARN("accesspoint/somewhere-else"))))
	_, err = otherPoint.GetAccessPointPolicyStatus(ctx, &s3control.GetAccessPointPolicyStatusInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(point)})
	require.Error(t, err, "a grant on another access point does not read this one")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
}

// TestS3Control_StorageLensIsAuthorizedAgainstTheConfiguration covers Storage
// Lens, authorized against the dashboard configuration's own ARN.
func TestS3Control_StorageLensIsAuthorizedAgainstTheConfiguration(t *testing.T) {
	admin := s3ControlClient()
	configID := "sl-auth-config"

	_, err := admin.PutStorageLensConfiguration(ctx, &s3control.PutStorageLensConfigurationInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ConfigId: aws.String(configID),
		StorageLensConfiguration: &s3ctypes.StorageLensConfiguration{
			Id: aws.String(configID), IsEnabled: true,
			AccountLevel: &s3ctypes.AccountLevel{BucketLevel: &s3ctypes.BucketLevel{}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteStorageLensConfiguration(ctx, &s3control.DeleteStorageLensConfigurationInput{
			AccountId: aws.String(s3ObjectLambdaAccount), ConfigId: aws.String(configID)})
	})

	allowed := s3ControlClientWithCreds(restrictedCredential(t, "s3control-lens-reader",
		s3ControlPolicy("s3:GetStorageLensConfiguration", s3ControlARN("storage-lens/"+configID))))
	read, err := allowed.GetStorageLensConfiguration(ctx, &s3control.GetStorageLensConfigurationInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ConfigId: aws.String(configID)})
	require.NoError(t, err, "the grant names this configuration")
	require.NotNil(t, read.StorageLensConfiguration)

	otherConfig := s3ControlClientWithCreds(restrictedCredential(t, "s3control-lens-other",
		s3ControlPolicy("s3:GetStorageLensConfiguration", s3ControlARN("storage-lens/another"))))
	_, err = otherConfig.GetStorageLensConfiguration(ctx, &s3control.GetStorageLensConfigurationInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ConfigId: aws.String(configID)})
	require.Error(t, err, "a grant on another configuration does not read this one")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
}

// TestS3Control_MultiRegionAccessPointIsAuthorizedAgainstItsAlias covers the
// Multi-Region Access Point ARN, which names the endpoint by the alias S3
// assigned it and carries no region at all.
func TestS3Control_MultiRegionAccessPointIsAuthorizedAgainstItsAlias(t *testing.T) {
	admin := s3ControlClient()
	s3c := s3Client()
	bucket, name := "mrap-auth-bucket", "authpoint"

	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = admin.CreateMultiRegionAccessPoint(ctx, &s3control.CreateMultiRegionAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount), ClientToken: aws.String("mrap-auth-token"),
		Details: &s3ctypes.CreateMultiRegionAccessPointInput{
			Name:    aws.String(name),
			Regions: []s3ctypes.Region{{Bucket: aws.String(bucket)}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteMultiRegionAccessPoint(ctx, &s3control.DeleteMultiRegionAccessPointInput{
			AccountId: aws.String(s3ObjectLambdaAccount), ClientToken: aws.String("mrap-auth-delete"),
			Details: &s3ctypes.DeleteMultiRegionAccessPointInput{Name: aws.String(name)}})
	})

	endpoint, err := admin.GetMultiRegionAccessPoint(ctx, &s3control.GetMultiRegionAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(name)})
	require.NoError(t, err)
	require.NotNil(t, endpoint.AccessPoint)
	alias := aws.ToString(endpoint.AccessPoint.Alias)
	require.NotEmpty(t, alias)

	allowed := s3ControlClientWithCreds(restrictedCredential(t, "s3control-mrap-reader",
		s3ControlPolicy("s3:GetMultiRegionAccessPoint",
			"arn:aws:s3::"+s3ObjectLambdaAccount+":accesspoint/"+alias)))
	read, err := allowed.GetMultiRegionAccessPoint(ctx, &s3control.GetMultiRegionAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(name)})
	require.NoError(t, err, "the grant names the endpoint by the alias S3 gave it")
	require.NotNil(t, read.AccessPoint)

	otherEndpoint := s3ControlClientWithCreds(restrictedCredential(t, "s3control-mrap-other",
		s3ControlPolicy("s3:GetMultiRegionAccessPoint",
			"arn:aws:s3::"+s3ObjectLambdaAccount+":accesspoint/elsewhere.9012.mrap")))
	_, err = otherEndpoint.GetMultiRegionAccessPoint(ctx, &s3control.GetMultiRegionAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(name)})
	require.Error(t, err, "a grant on another endpoint does not read this one")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
}
