package organizations_all_features_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOrganizationsAllFeaturesTerraform creates an organization with
// consolidated billing alone, and changing feature_set to ALL starts the
// migration to all features through EnableAllFeatures. The organization keeps
// consolidated billing, so a refreshed plan still differs, until the
// management account accepts the ENABLE_ALL_FEATURES handshake.
func TestOrganizationsAllFeaturesTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	ctx := context.Background()
	org := organizations.New(organizations.Options{Region: "us-east-1", BaseEndpoint: aws.String(env.Endpoint), HTTPClient: env.Client,
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")})
	// The account starts out running an organization; Terraform creates its own.
	_, err := org.DeleteOrganization(ctx, &organizations.DeleteOrganizationInput{})
	require.NoError(t, err)

	featureSet := func(set string) []string { return []string{"-var", "feature_set=" + set} }
	env.Terraform(t, "init")
	env.Terraform(t, append([]string{"apply", "-auto-approve"}, featureSet("CONSOLIDATED_BILLING")...)...)
	env.Terraform(t, append([]string{"apply", "-auto-approve"}, featureSet("ALL")...)...)

	described, err := org.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	require.NoError(t, err)
	assert.Equal(t, orgtypes.OrganizationFeatureSetConsolidatedBilling, described.Organization.FeatureSet,
		"the migration waits for the management account to finalize it")
	env.TerraformFails(t, append([]string{"plan", "-detailed-exitcode"}, featureSet("ALL")...)...)

	requests, err := org.ListHandshakesForOrganization(ctx, &organizations.ListHandshakesForOrganizationInput{
		Filter: &orgtypes.HandshakeFilter{ActionType: orgtypes.ActionTypeEnableAllFeatures}})
	require.NoError(t, err)
	require.Len(t, requests.Handshakes, 1)
	request := requests.Handshakes[0]
	assert.Equal(t, orgtypes.HandshakeStateOpen, request.State, "an organization without invited accounts finalizes at once")
	_, err = org.AcceptHandshake(ctx, &organizations.AcceptHandshakeInput{HandshakeId: request.Id})
	require.NoError(t, err)

	env.Terraform(t, append([]string{"plan", "-detailed-exitcode"}, featureSet("ALL")...)...)
	env.Terraform(t, append([]string{"destroy", "-auto-approve"}, featureSet("ALL")...)...)
}
