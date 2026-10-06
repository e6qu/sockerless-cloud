package sts_session_tags_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSTSSessionTagsTerraform configures terraform-provider-aws to work as a
// role in a session tagged through its assume_role block. The role may use
// Amazon S3 only as the blue team, by aws:PrincipalTag, so the session's tag
// decides whether the provider can create the bucket.
func TestSTSSessionTagsTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	ctx := context.Background()
	admin := iam.New(iam.Options{Region: "us-east-1", BaseEndpoint: aws.String(env.Endpoint), HTTPClient: env.Client,
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")})
	role, err := admin.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String("tf-session-tags"),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
			"Principal":{"AWS":"*"},"Action":["sts:AssumeRole","sts:TagSession"]}]}`)})
	require.NoError(t, err)
	_, err = admin.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String("tf-session-tags"),
		PolicyName: aws.String("blue-team-s3"), PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{"StringEquals":{"aws:PrincipalTag/team":"blue"}}}]}`)})
	require.NoError(t, err)
	roleArn := aws.ToString(role.Role.Arn)

	env.Terraform(t, "init")
	refused := string(env.TerraformFails(t, "apply", "-auto-approve", "-var", "role_arn="+roleArn, "-var", "team=red"))
	assert.Contains(t, refused, "CreateBucket")
	assert.Contains(t, refused, "AccessDenied", "a session tagged team=red may not use Amazon S3")

	env.Terraform(t, "apply", "-auto-approve", "-var", "role_arn="+roleArn, "-var", "team=blue")
	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	assert.True(t, strings.HasSuffix(outputs["bucket_arn"].Value, ":::tf-session-tags-blue"),
		"a session tagged team=blue creates the bucket; got %q", outputs["bucket_arn"].Value)

	env.Terraform(t, "destroy", "-auto-approve", "-var", "role_arn="+roleArn, "-var", "team=blue")
}
