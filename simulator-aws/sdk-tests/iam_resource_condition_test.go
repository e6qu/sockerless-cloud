package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAM_ResourceTagCondition reproduces issue #661: a grant conditioned on
// aws:ResourceTag/<k> is denied until the target resource actually carries the
// tag, then allowed — proving the gate resolves the resource's tags into the
// condition context.
func TestIAM_ResourceTagCondition(t *testing.T) {
	admin := iamClient()
	user := "rt-cond-user"
	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})

	_, err = admin.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:   aws.String(user),
		PolicyName: aws.String("tag-scoped"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[` +
			`{"Effect":"Allow","Action":["ec2:CreateVolume","ec2:CreateTags","ec2:DescribeVolumes"],"Resource":"*"},` +
			`{"Effect":"Allow","Action":"ec2:DeleteVolume","Resource":"*","Condition":{"StringEquals":{"aws:ResourceTag/edd:managed":"true"}}}]}`),
	})
	require.NoError(t, err)
	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)

	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")}
	c := ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.BaseEndpoint = aws.String(baseURL) })

	vol, err := c.CreateVolume(ctx, &ec2.CreateVolumeInput{AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(1)})
	require.NoError(t, err)
	volID := aws.ToString(vol.VolumeId)

	// Untagged: the tag-scoped DeleteVolume grant doesn't match → denied.
	_, err = c.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(volID)})
	require.Error(t, err, "DeleteVolume on an untagged volume must be denied")
	assert.Equal(t, "UnauthorizedOperation", errCodeOf(err))

	// Tag it edd:managed=true.
	_, err = c.CreateTags(ctx, &ec2.CreateTagsInput{
		Resources: []string{volID},
		Tags:      []ec2types.Tag{{Key: aws.String("edd:managed"), Value: aws.String("true")}},
	})
	require.NoError(t, err)

	// Now the condition matches → allowed.
	_, err = c.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(volID)})
	assert.NoError(t, err, "DeleteVolume must be allowed once the volume carries edd:managed=true")
}

// TestIAM_ECSClusterCondition proves the ecs:cluster condition key: an
// ecs:StopTask grant scoped to one cluster denies a call targeting another.
func TestIAM_ECSClusterCondition(t *testing.T) {
	admin := iamClient()
	user := "ecs-cond-user"
	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})

	_, err = admin.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:   aws.String(user),
		PolicyName: aws.String("cluster-scoped"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ecs:StopTask","Resource":"*",` +
			`"Condition":{"StringEquals":{"ecs:cluster":"arn:aws:ecs:us-east-1:123456789012:cluster/allowed"}}}]}`),
	})
	require.NoError(t, err)
	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)

	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")}
	c := ecs.NewFromConfig(cfg, func(o *ecs.Options) { o.BaseEndpoint = aws.String(baseURL) })

	// Wrong cluster → denied by IAM (AccessDeniedException).
	_, err = c.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String("other"), Task: aws.String("task-1")})
	require.Error(t, err)
	assert.Equal(t, "AccessDeniedException", errCodeOf(err), "StopTask on a non-allowed cluster must be IAM-denied")

	// Allowed cluster → enforcement passes (any later error is not an IAM denial).
	_, err = c.StopTask(ctx, &ecs.StopTaskInput{Cluster: aws.String("allowed"), Task: aws.String("task-1")})
	if err != nil {
		assert.NotEqual(t, "AccessDeniedException", errCodeOf(err), "StopTask on the allowed cluster must pass IAM enforcement")
	}
}
