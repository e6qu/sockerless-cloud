package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAM_CallTimeEnforcement proves call-time IAM enforcement (#657): a
// credential bound to a least-privilege policy is denied the actions its policy
// doesn't grant, with the correct per-service error shape — while the allowed
// action succeeds. Unregistered (test) credentials remain permissive, which is
// why the setup calls (run with the default creds) work.
func TestIAM_CallTimeEnforcement(t *testing.T) {
	admin := iamClient()
	user := "restricted-user"

	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})

	// Inline policy: allow only ec2:DescribeVolumes — nothing else.
	_, err = admin.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:   aws.String(user),
		PolicyName: aws.String("least-priv"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[` +
			`{"Effect":"Allow","Action":"ec2:DescribeVolumes","Resource":"*"}]}`),
	})
	require.NoError(t, err)

	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	akid := aws.ToString(key.AccessKey.AccessKeyId)
	secret := aws.ToString(key.AccessKey.SecretAccessKey)
	require.NotEmpty(t, akid)
	require.NotEmpty(t, secret)

	restricted := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, ""),
	}
	ec2r := ec2.NewFromConfig(restricted, func(o *ec2.Options) { o.BaseEndpoint = aws.String(baseURL) })
	ecsr := ecs.NewFromConfig(restricted, func(o *ecs.Options) { o.BaseEndpoint = aws.String(baseURL) })

	errCode := func(err error) string {
		var ae smithy.APIError
		if errors.As(err, &ae) {
			return ae.ErrorCode()
		}
		return ""
	}

	// Allowed action → succeeds.
	_, err = ec2r.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{})
	assert.NoError(t, err, "ec2:DescribeVolumes is granted and must succeed")

	// Denied EC2 action → UnauthorizedOperation (the EC2 query-protocol shape).
	_, err = ec2r.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(1),
	})
	require.Error(t, err, "ec2:CreateVolume is not granted and must be denied")
	assert.Equal(t, "UnauthorizedOperation", errCode(err))

	// Denied awsJson action → AccessDeniedException (the ECS/JSON shape).
	_, err = ecsr.ListClusters(ctx, &ecs.ListClustersInput{})
	require.Error(t, err, "ecs:ListClusters is not granted and must be denied")
	assert.Equal(t, "AccessDeniedException", errCode(err))

	// A sanity check that the default (unregistered) credentials are still
	// permissive: the admin client created above can CreateVolume.
	adminEC2 := ec2.NewFromConfig(sdkConfig(), func(o *ec2.Options) { o.BaseEndpoint = aws.String(baseURL) })
	_, err = adminEC2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{})
	assert.NoError(t, err, "unregistered test credentials remain permissive")
}

// TestIAM_CloudWatchNamespaceEnforcement proves Amazon CloudWatch's
// monitoring.amazonaws.com CloudTrail event source is authorized through the
// service's real cloudwatch:* IAM namespace.
func TestIAM_CloudWatchNamespaceEnforcement(t *testing.T) {
	cfg := restrictedConfig(t, iamClient(), "cloudwatch-restricted-user", "cloudwatch-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"cloudwatch:ListDashboards","Resource":"*"}]}`)
	cw := cloudwatch.NewFromConfig(cfg, func(o *cloudwatch.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})

	_, err := cw.ListDashboards(ctx, &cloudwatch.ListDashboardsInput{})
	assert.NoError(t, err, "cloudwatch:ListDashboards must authorize the query-protocol request")

	_, err = cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{})
	require.Error(t, err, "cloudwatch:DescribeAlarms was not granted and must be denied")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
}
