package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func iamClient() *iam.Client {
	return iam.NewFromConfig(sdkConfig(), func(o *iam.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestIAM_CreateRole(t *testing.T) {
	roleName := uniqueName("test-role")
	client := iamClient()
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

	out, err := client.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(policy),
	})
	require.NoError(t, err)
	assert.Equal(t, roleName, *out.Role.RoleName)
	assert.Contains(t, *out.Role.Arn, roleName)
}

func TestIAM_GetRole(t *testing.T) {
	roleName := uniqueName("get-role")
	client := iamClient()
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

	_, err := client.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(policy),
	})
	require.NoError(t, err)

	out, err := client.GetRole(ctx, &iam.GetRoleInput{
		RoleName: aws.String(roleName),
	})
	require.NoError(t, err)
	assert.Equal(t, roleName, *out.Role.RoleName)
}

func TestIAM_PutRolePolicy(t *testing.T) {
	roleName := uniqueName("policy-role")
	client := iamClient()
	assumePolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

	_, err := client.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(assumePolicy),
	})
	require.NoError(t, err)

	inlinePolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`
	_, err = client.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String("s3-read"),
		PolicyDocument: aws.String(inlinePolicy),
	})
	require.NoError(t, err)

	getOut, err := client.GetRolePolicy(ctx, &iam.GetRolePolicyInput{
		RoleName:   aws.String(roleName),
		PolicyName: aws.String("s3-read"),
	})
	require.NoError(t, err)
	assert.Equal(t, "s3-read", *getOut.PolicyName)
}
