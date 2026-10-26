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
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAM_CallTimeEnforcement expects a credential bound to a least-privilege
// policy to be denied what the policy does not grant, in each service's error
// shape, while the allowed action succeeds. The setup calls use the suite's
// seed credential, the account administrator.
func TestIAM_CallTimeEnforcement(t *testing.T) {
	t.Parallel()
	admin := iamClient()
	user := uniquename.New("restricted-user")

	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})

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

	_, err = ec2r.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{})
	assert.NoError(t, err, "ec2:DescribeVolumes is granted and must succeed")

	// A denied EC2 action answers UnauthorizedOperation, the query-protocol shape.
	_, err = ec2r.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(1),
	})
	require.Error(t, err, "ec2:CreateVolume is not granted and must be denied")
	assert.Equal(t, "UnauthorizedOperation", errCode(err))

	// A denied awsJson action answers AccessDeniedException.
	_, err = ecsr.ListClusters(ctx, &ecs.ListClustersInput{})
	require.Error(t, err, "ecs:ListClusters is not granted and must be denied")
	assert.Equal(t, "AccessDeniedException", errCode(err))

	// The seed credential can still CreateVolume.
	adminEC2 := ec2.NewFromConfig(sdkConfig(), func(o *ec2.Options) { o.BaseEndpoint = aws.String(baseURL) })
	_, err = adminEC2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{})
	assert.NoError(t, err, "unregistered test credentials remain permissive")
}

// TestIAM_CloudWatchNamespaceEnforcement expects CloudWatch's
// monitoring.amazonaws.com event source to authorize under the cloudwatch:* IAM
// namespace.
func TestIAM_CloudWatchNamespaceEnforcement(t *testing.T) {
	t.Parallel()
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
