package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decisionFor(t *testing.T, results []iamtypes.EvaluationResult, action string) iamtypes.PolicyEvaluationDecisionType {
	t.Helper()
	for _, r := range results {
		if aws.ToString(r.EvalActionName) == action {
			return r.EvalDecision
		}
	}
	t.Fatalf("no evaluation result for action %q", action)
	return ""
}

// TestIAM_SimulateCustomPolicy covers explicit-deny-wins, resource scoping,
// action wildcards and conditions.
func TestIAM_SimulateCustomPolicy(t *testing.T) {
	t.Parallel()
	client := iamClient()

	out, err := client.SimulateCustomPolicy(ctx, &iam.SimulateCustomPolicyInput{
		PolicyInputList: []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"ec2:DeleteVolume","Resource":"*"}]}`},
		ActionNames:     []string{"ec2:DeleteVolume"},
	})
	require.NoError(t, err)
	assert.Equal(t, iamtypes.PolicyEvaluationDecisionTypeExplicitDeny, decisionFor(t, out.EvaluationResults, "ec2:DeleteVolume"))

	allowPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:GetItem","Resource":"arn:aws:dynamodb:us-east-1:123456789012:table/platform"}]}`
	out, err = client.SimulateCustomPolicy(ctx, &iam.SimulateCustomPolicyInput{
		PolicyInputList: []string{allowPolicy},
		ActionNames:     []string{"dynamodb:GetItem"},
		ResourceArns:    []string{"arn:aws:dynamodb:us-east-1:123456789012:table/platform"},
	})
	require.NoError(t, err)
	require.Len(t, out.EvaluationResults, 1)
	assert.Equal(t, iamtypes.PolicyEvaluationDecisionTypeAllowed, out.EvaluationResults[0].EvalDecision)

	out, err = client.SimulateCustomPolicy(ctx, &iam.SimulateCustomPolicyInput{
		PolicyInputList: []string{allowPolicy},
		ActionNames:     []string{"dynamodb:GetItem"},
		ResourceArns:    []string{"arn:aws:dynamodb:us-east-1:123456789012:table/other"},
	})
	require.NoError(t, err)
	assert.Equal(t, iamtypes.PolicyEvaluationDecisionTypeImplicitDeny, out.EvaluationResults[0].EvalDecision)

	out, err = client.SimulateCustomPolicy(ctx, &iam.SimulateCustomPolicyInput{
		PolicyInputList: []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:Get*","Resource":"*"}]}`},
		ActionNames:     []string{"s3:GetObject"},
	})
	require.NoError(t, err)
	assert.Equal(t, iamtypes.PolicyEvaluationDecisionTypeAllowed, out.EvaluationResults[0].EvalDecision)

	// Allowed only while the aws:ResourceTag condition matches.
	tagPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:DeleteVolume","Resource":"*","Condition":{"StringEquals":{"aws:ResourceTag/edd:managed":"true"}}}]}`
	tagged, err := client.SimulateCustomPolicy(ctx, &iam.SimulateCustomPolicyInput{
		PolicyInputList: []string{tagPolicy},
		ActionNames:     []string{"ec2:DeleteVolume"},
		ContextEntries: []iamtypes.ContextEntry{{
			ContextKeyName:   aws.String("aws:ResourceTag/edd:managed"),
			ContextKeyValues: []string{"true"},
			ContextKeyType:   iamtypes.ContextKeyTypeEnumString,
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, iamtypes.PolicyEvaluationDecisionTypeAllowed, tagged.EvaluationResults[0].EvalDecision)

	// Without a matching context entry the condition fails: implicit deny.
	untagged, err := client.SimulateCustomPolicy(ctx, &iam.SimulateCustomPolicyInput{
		PolicyInputList: []string{tagPolicy},
		ActionNames:     []string{"ec2:DeleteVolume"},
	})
	require.NoError(t, err)
	assert.Equal(t, iamtypes.PolicyEvaluationDecisionTypeImplicitDeny, untagged.EvaluationResults[0].EvalDecision)
}

// TestIAM_SimulatePrincipalPolicy evaluates a role's stored inline policy.
func TestIAM_SimulatePrincipalPolicy(t *testing.T) {
	t.Parallel()
	client := iamClient()
	roleName := uniquename.New("sim-princ-role")
	_, err := client.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[]}`),
	})
	require.NoError(t, err)
	roleArn := "arn:aws:iam::123456789012:role/" + roleName

	_, err = client.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String("inline"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"dynamodb:GetItem","Resource":"arn:aws:dynamodb:us-east-1:123456789012:table/platform"}]}`),
	})
	require.NoError(t, err)

	out, err := client.SimulatePrincipalPolicy(ctx, &iam.SimulatePrincipalPolicyInput{
		PolicySourceArn: aws.String(roleArn),
		ActionNames:     []string{"dynamodb:GetItem"},
		ResourceArns:    []string{"arn:aws:dynamodb:us-east-1:123456789012:table/platform"},
	})
	require.NoError(t, err)
	assert.Equal(t, iamtypes.PolicyEvaluationDecisionTypeAllowed, out.EvaluationResults[0].EvalDecision)

	// Another table and an unrelated action are both implicit denies.
	out, err = client.SimulatePrincipalPolicy(ctx, &iam.SimulatePrincipalPolicyInput{
		PolicySourceArn: aws.String(roleArn),
		ActionNames:     []string{"dynamodb:GetItem", "dynamodb:DeleteItem"},
		ResourceArns:    []string{"arn:aws:dynamodb:us-east-1:123456789012:table/other"},
	})
	require.NoError(t, err)
	assert.Equal(t, iamtypes.PolicyEvaluationDecisionTypeImplicitDeny, decisionFor(t, out.EvaluationResults, "dynamodb:GetItem"))
	assert.Equal(t, iamtypes.PolicyEvaluationDecisionTypeImplicitDeny, decisionFor(t, out.EvaluationResults, "dynamodb:DeleteItem"))
}
