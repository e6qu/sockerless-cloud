package iam_organizations_features_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/aws/smithy-go"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAMOrganizationsFeaturesTerraform adopts the account's organization with
// trusted access for IAM, turns on centralized root access through
// aws_iam_organizations_features, and creates a member account and an IAM user
// allowed sts:AssumeRoot. The user opens a root session on the member account
// while the organization enables root sessions, and none once Terraform turns
// them off.
func TestIAMOrganizationsFeaturesTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	features := func(list string) []string {
		return []string{"-auto-approve", "-var", "enabled_features=" + list}
	}
	env.Terraform(t, append([]string{"apply"}, features(`["RootCredentialsManagement","RootSessions"]`)...)...)
	var outputs map[string]struct {
		Value json.RawMessage `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	value := func(name string) string {
		var s string
		require.NoError(t, json.Unmarshal(outputs[name].Value, &s), name)
		return s
	}
	assert.Regexp(t, `^o-[a-z0-9]{10,32}$`, value("organization_id"))
	assert.JSONEq(t, `["RootCredentialsManagement","RootSessions"]`, string(outputs["enabled_features"].Value))
	member := value("member_account_id")

	ctx := context.Background()
	caller := sts.New(sts.Options{Region: "us-east-1", BaseEndpoint: aws.String(env.Endpoint), HTTPClient: env.Client,
		Credentials: credentials.NewStaticCredentialsProvider(value("access_key_id"), value("secret_access_key"), "")})
	assumeRoot := func() error {
		_, err := caller.AssumeRoot(ctx, &sts.AssumeRootInput{TargetPrincipal: aws.String(member),
			TaskPolicyArn: &ststypes.PolicyDescriptorType{Arn: aws.String("arn:aws:iam::aws:policy/root-task/IAMAuditRootUserCredentials")}})
		return err
	}
	require.NoError(t, assumeRoot(), "the organization enables root sessions")

	env.Terraform(t, append([]string{"apply"}, features(`["RootCredentialsManagement"]`)...)...)
	err := assumeRoot()
	var apiErr smithy.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "AccessDenied", apiErr.ErrorCode(), "an organization without root sessions opens none")

	env.Terraform(t, append([]string{"destroy"}, features(`["RootCredentialsManagement"]`)...)...)
}
