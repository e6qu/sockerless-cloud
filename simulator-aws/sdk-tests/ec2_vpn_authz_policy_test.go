package aws_sdk_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const clientVpnCedarPolicy = `permit (principal, action, resource) when { context.posture.compliant == true };`

// TestEC2_ClientVpnEndpointAuthorizationPolicy creates a Client VPN endpoint's
// Cedar authorization policy, merges a change into it, and deletes it: each
// change answers with the policy's transitional status and settles behind the
// request.
func TestEC2_ClientVpnEndpointAuthorizationPolicy(t *testing.T) {
	c := ec2Client()
	ep, err := c.CreateClientVpnEndpoint(ctx, &ec2.CreateClientVpnEndpointInput{
		ClientCidrBlock:      aws.String("10.230.0.0/22"),
		ServerCertificateArn: aws.String("arn:aws:acm:us-east-1:123456789012:certificate/srv"),
		AuthenticationOptions: []types.ClientVpnAuthenticationRequest{{
			Type: types.ClientVpnAuthenticationTypeCertificateAuthentication,
			MutualAuthentication: &types.CertificateAuthenticationRequest{
				ClientRootCertificateChainArn: aws.String("arn:aws:acm:us-east-1:123456789012:certificate/root"),
			},
		}},
		ConnectionLogOptions: &types.ConnectionLogOptions{Enabled: aws.Bool(false)},
	})
	require.NoError(t, err)
	epID := ep.ClientVpnEndpointId
	t.Cleanup(func() {
		_, _ = c.DeleteClientVpnEndpoint(ctx, &ec2.DeleteClientVpnEndpointInput{ClientVpnEndpointId: epID})
	})

	empty, err := c.GetClientVpnEndpointAuthorizationPolicy(ctx, &ec2.GetClientVpnEndpointAuthorizationPolicyInput{ClientVpnEndpointId: epID})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(epID), aws.ToString(empty.ClientVpnEndpointId))
	assert.Nil(t, empty.PolicyDocument, "an endpoint without a policy reports none")

	var apiErr smithy.APIError
	_, err = c.ModifyClientVpnEndpointAuthorizationPolicy(ctx, &ec2.ModifyClientVpnEndpointAuthorizationPolicyInput{
		ClientVpnEndpointId: epID, Description: aws.String("no document"),
	})
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, "MissingParameter", apiErr.ErrorCode(), "creating a policy requires its document")

	_, err = c.ModifyClientVpnEndpointAuthorizationPolicy(ctx, &ec2.ModifyClientVpnEndpointAuthorizationPolicyInput{
		ClientVpnEndpointId: epID, PolicyDocument: aws.String(clientVpnCedarPolicy), DryRun: aws.Bool(true),
	})
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, "DryRunOperation", apiErr.ErrorCode())

	created, err := c.ModifyClientVpnEndpointAuthorizationPolicy(ctx, &ec2.ModifyClientVpnEndpointAuthorizationPolicyInput{
		ClientVpnEndpointId: epID,
		PolicyDocument:      aws.String(clientVpnCedarPolicy),
		Description:         aws.String("posture"),
	})
	require.NoError(t, err)
	assert.Equal(t, types.ClientVpnAuthorizationPolicyStatusCreating, created.Status)
	policy := waitForClientVpnAuthorizationPolicy(t, c, epID, types.ClientVpnAuthorizationPolicyStatusActive)
	assert.Equal(t, clientVpnCedarPolicy, aws.ToString(policy.PolicyDocument))
	assert.Equal(t, "posture", aws.ToString(policy.Description))
	assert.Equal(t, types.ClientVpnAuthorizationPolicyShadowModeDisabled, policy.ShadowMode, "shadow mode defaults to disabled")

	updated, err := c.ModifyClientVpnEndpointAuthorizationPolicy(ctx, &ec2.ModifyClientVpnEndpointAuthorizationPolicyInput{
		ClientVpnEndpointId: epID, ShadowMode: types.ClientVpnAuthorizationPolicyShadowModeEnabled,
	})
	require.NoError(t, err)
	assert.Equal(t, types.ClientVpnAuthorizationPolicyStatusUpdating, updated.Status)
	policy = waitForClientVpnAuthorizationPolicy(t, c, epID, types.ClientVpnAuthorizationPolicyStatusActive)
	assert.Equal(t, types.ClientVpnAuthorizationPolicyShadowModeEnabled, policy.ShadowMode)
	assert.Equal(t, clientVpnCedarPolicy, aws.ToString(policy.PolicyDocument), "values a modify leaves out remain unchanged")
	assert.Equal(t, "posture", aws.ToString(policy.Description))

	deleted, err := c.DeleteClientVpnEndpointAuthorizationPolicy(ctx, &ec2.DeleteClientVpnEndpointAuthorizationPolicyInput{ClientVpnEndpointId: epID})
	require.NoError(t, err)
	assert.Equal(t, types.ClientVpnAuthorizationPolicyStatusDeleting, deleted.Status)
	waitForClientVpnAuthorizationPolicy(t, c, epID, "")

	_, err = c.GetClientVpnEndpointAuthorizationPolicy(ctx, &ec2.GetClientVpnEndpointAuthorizationPolicyInput{ClientVpnEndpointId: aws.String("cvpn-endpoint-0000000000000000a")})
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, "InvalidClientVpnEndpointId.NotFound", apiErr.ErrorCode())
}

// waitForClientVpnAuthorizationPolicy polls GetClientVpnEndpointAuthorizationPolicy
// until the policy reaches want, or with want empty until it is gone: the SDK
// has no waiter for the policy.
func waitForClientVpnAuthorizationPolicy(t *testing.T, c *ec2.Client, epID *string, want types.ClientVpnAuthorizationPolicyStatus) *ec2.GetClientVpnEndpointAuthorizationPolicyOutput {
	t.Helper()
	delay := waiterMinDelay
	deadline := time.Now().Add(time.Minute)
	for {
		out, err := c.GetClientVpnEndpointAuthorizationPolicy(ctx, &ec2.GetClientVpnEndpointAuthorizationPolicyInput{ClientVpnEndpointId: epID})
		require.NoError(t, err)
		if out.Status == want {
			return out
		}
		require.True(t, time.Now().Before(deadline), "policy of %s is %q, want %q", aws.ToString(epID), out.Status, want)
		time.Sleep(delay)
		delay = min(delay*2, waiterMaxDelay)
	}
}
