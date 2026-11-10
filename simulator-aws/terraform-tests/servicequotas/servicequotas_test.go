package servicequotas_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServiceQuotasTerraform raises a quota through aws_servicequotas_service_quota,
// reads the catalog through the data sources, and keeps a quota request
// template associated with the organization the account runs, whose trusted
// access for Service Quotas the test turns on first.
func TestServiceQuotasTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	org := organizations.New(organizations.Options{Region: "us-east-1", BaseEndpoint: aws.String(env.Endpoint), HTTPClient: env.Client,
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")})
	_, err := org.EnableAWSServiceAccess(context.Background(), &organizations.EnableAWSServiceAccessInput{
		ServicePrincipal: aws.String("servicequotas.amazonaws.com")})
	require.NoError(t, err)

	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")
	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	assert.Equal(t, "ecr", outputs["service_code"].Value)
	assert.Equal(t, "100000", outputs["images_default"].Value)
	assert.Equal(t, "150000", outputs["images_value"].Value)
	assert.Equal(t, "ResourceCount", outputs["images_usage_metric"].Value)
	assert.Equal(t, "Roles per account", outputs["template_quota_name"].Value)
	assert.Equal(t, "ASSOCIATED", outputs["association_status"].Value)
	env.Terraform(t, "plan", "-detailed-exitcode")

	env.Terraform(t, "destroy", "-auto-approve")
}
