package apigatewaydomainname_test

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/require"
)

// TestAPIGatewayDomainNameTerraform applies a Regional custom domain name on a
// DNS-validated certificate, a Route 53 alias to it, a base path mapping onto
// a REST API stage, and a VPC link to a Network Load Balancer, then destroys
// them.
func TestAPIGatewayDomainNameTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	require.Regexp(t, regexp.MustCompile(`^d-[a-z0-9]{10}\.execute-api\.us-east-1\.amazonaws\.com$`), outputs["regional_domain_name"].Value)
	require.Equal(t, "Z1UJRXOUMOOFQ8", outputs["regional_zone_id"].Value)
	require.Equal(t, "arn:aws:apigateway:us-east-1::/domainnames/api.example.test", outputs["domain_arn"].Value)
	require.Equal(t, "api.example.test", outputs["alias_fqdn"].Value)
	require.Equal(t, "orders", outputs["base_path"].Value)
	require.NotEmpty(t, outputs["vpc_link_id"].Value)

	env.Terraform(t, "plan", "-detailed-exitcode")
	env.Terraform(t, "destroy", "-auto-approve")
}
