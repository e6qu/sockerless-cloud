package aws_cli_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIGatewayCLIDomainNames covers create-domain-name, get-domain-name,
// get-domain-names, update-domain-name and delete-domain-name.
func TestAPIGatewayCLIDomainNames(t *testing.T) {
	t.Parallel()
	assert.Contains(t, runCLIExpectError(t, awsCLI("apigateway", "update-domain-name",
		"--domain-name", "absent.cli-domains.example.com",
		"--patch-operations", "op=replace,path=/certificateName,value=x")), "NotFoundException")
	assert.Contains(t, runCLIExpectError(t, awsCLI("apigateway", "create-domain-name",
		"--domain-name", "nocert.cli-domains.example.com")), "BadRequestException")

	type domainName struct {
		DistributionDomainName   string `json:"distributionDomainName"`
		DistributionHostedZoneID string `json:"distributionHostedZoneId"`
		RegionalDomainName       string `json:"regionalDomainName"`
		RegionalHostedZoneID     string `json:"regionalHostedZoneId"`
		RegionalCertificateName  string `json:"regionalCertificateName"`
		DomainNameStatus         string `json:"domainNameStatus"`
	}
	edgeName := "edge.cli-domains.example.com"
	edgeCert := importELBv2CertificateCLI(t, edgeName)
	var edge domainName
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "create-domain-name",
		"--domain-name", edgeName, "--certificate-arn", edgeCert))), &edge))
	t.Cleanup(func() { runCLIIgnore(awsCLI("apigateway", "delete-domain-name", "--domain-name", edgeName)) })
	assert.Regexp(t, regexp.MustCompile(`^d[a-z0-9]{13}\.cloudfront\.net$`), edge.DistributionDomainName)
	assert.Equal(t, "Z2FDTNDATAQYW2", edge.DistributionHostedZoneID)
	assert.Equal(t, "AVAILABLE", edge.DomainNameStatus)
	assert.Contains(t, runCLIExpectError(t, awsCLI("apigateway", "create-domain-name",
		"--domain-name", edgeName, "--certificate-arn", edgeCert)), "ConflictException")

	regionalName := "regional.cli-domains.example.com"
	var regional domainName
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "create-domain-name",
		"--domain-name", regionalName, "--regional-certificate-arn", importELBv2CertificateCLI(t, regionalName),
		"--endpoint-configuration", "types=REGIONAL"))), &regional))
	assert.Regexp(t, regexp.MustCompile(`^d-[a-z0-9]{10}\.execute-api\.us-east-1\.amazonaws\.com$`), regional.RegionalDomainName)
	assert.Equal(t, "Z1UJRXOUMOOFQ8", regional.RegionalHostedZoneID)

	var got domainName
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "get-domain-name",
		"--domain-name", regionalName))), &got))
	assert.Equal(t, regional.RegionalDomainName, got.RegionalDomainName)
	names := runCLI(t, awsCLI("apigateway", "get-domain-names", "--query", "items[].domainName", "--output", "text"))
	assert.Contains(t, names, edgeName)
	assert.Contains(t, names, regionalName)

	var updated domainName
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "update-domain-name",
		"--domain-name", regionalName,
		"--patch-operations", "op=replace,path=/regionalCertificateName,value=renamed"))), &updated))
	assert.Equal(t, "renamed", updated.RegionalCertificateName)

	runCLI(t, awsCLI("apigateway", "delete-domain-name", "--domain-name", regionalName))
	assert.Contains(t, runCLIExpectError(t, awsCLI("apigateway", "get-domain-name",
		"--domain-name", regionalName)), "NotFoundException")
}

// TestAPIGatewayCLIVpcLinks covers create-vpc-link, get-vpc-link,
// get-vpc-links, update-vpc-link and delete-vpc-link.
func TestAPIGatewayCLIVpcLinks(t *testing.T) {
	t.Parallel()
	assert.Contains(t, runCLIExpectError(t, awsCLI("apigateway", "update-vpc-link",
		"--vpc-link-id", "absent", "--patch-operations", "op=replace,path=/name,value=x")), "NotFoundException")

	nlbArn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-load-balancer", "--name", "cli-vpclink-nlb",
		"--type", "network", "--scheme", "internal",
		"--query", "LoadBalancers[0].LoadBalancerArn", "--output", "text")))
	t.Cleanup(func() { runCLIIgnore(awsCLI("elbv2", "delete-load-balancer", "--load-balancer-arn", nlbArn)) })

	type vpcLink struct {
		ID            string            `json:"id"`
		Name          string            `json:"name"`
		Status        string            `json:"status"`
		StatusMessage string            `json:"statusMessage"`
		TargetArns    []string          `json:"targetArns"`
		Tags          map[string]string `json:"tags"`
	}
	settle := func(id string) vpcLink {
		t.Helper()
		var link vpcLink
		require.Eventually(t, func() bool {
			require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "get-vpc-link", "--vpc-link-id", id))), &link))
			return link.Status != "PENDING"
		}, 30*time.Second, 200*time.Millisecond, "VPC link %s stayed PENDING", id)
		return link
	}

	var created vpcLink
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "create-vpc-link",
		"--name", "cli-private-backends", "--target-arns", nlbArn, "--tags", "team=network"))), &created))
	assert.Equal(t, "PENDING", created.Status)
	link := settle(created.ID)
	assert.Equal(t, "AVAILABLE", link.Status)
	assert.Equal(t, []string{nlbArn}, link.TargetArns)
	assert.Equal(t, "network", link.Tags["team"])

	var broken vpcLink
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "create-vpc-link",
		"--name", "cli-broken",
		"--target-arns", "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/net/absent/0123456789abcdef"))), &broken))
	failed := settle(broken.ID)
	assert.Equal(t, "FAILED", failed.Status)
	assert.NotEmpty(t, failed.StatusMessage)

	var updated vpcLink
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("apigateway", "update-vpc-link",
		"--vpc-link-id", created.ID, "--patch-operations", "op=replace,path=/name,value=renamed"))), &updated))
	assert.Equal(t, "renamed", updated.Name)
	assert.Contains(t, runCLI(t, awsCLI("apigateway", "get-vpc-links", "--query", "items[].id", "--output", "text")), created.ID)

	runCLI(t, awsCLI("apigateway", "delete-vpc-link", "--vpc-link-id", created.ID))
	runCLI(t, awsCLI("apigateway", "delete-vpc-link", "--vpc-link-id", broken.ID))
	assert.Contains(t, runCLIExpectError(t, awsCLI("apigateway", "get-vpc-link",
		"--vpc-link-id", created.ID)), "NotFoundException")
}
