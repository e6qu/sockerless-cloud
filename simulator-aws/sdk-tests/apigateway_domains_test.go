package aws_sdk_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	apigwtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIGateway_DomainNames covers CreateDomainName, GetDomainName,
// GetDomainNames, UpdateDomainName and DeleteDomainName for edge-optimized
// and Regional custom domain names.
func TestAPIGateway_DomainNames(t *testing.T) {
	t.Parallel()
	c := apigwClient()

	_, err := c.UpdateDomainName(ctx, &apigateway.UpdateDomainNameInput{
		DomainName:      aws.String("absent.sdk-domains.example.com"),
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/certificateName", "x")},
	})
	var notFound *apigwtypes.NotFoundException
	require.ErrorAs(t, err, &notFound, "UpdateDomainName of an unknown domain name")

	_, err = c.CreateDomainName(ctx, &apigateway.CreateDomainNameInput{DomainName: aws.String("nocert.sdk-domains.example.com")})
	var badRequest *apigwtypes.BadRequestException
	require.ErrorAs(t, err, &badRequest, "an edge-optimized domain name needs a certificate")
	_, err = c.CreateDomainName(ctx, &apigateway.CreateDomainNameInput{
		DomainName:     aws.String("badcert.sdk-domains.example.com"),
		CertificateArn: aws.String("arn:aws:acm:us-east-1:123456789012:certificate/00000000-0000-0000-0000-000000000000"),
	})
	require.ErrorAs(t, err, &badRequest, "an unknown certificate")

	edgeName := "edge.sdk-domains.example.com"
	edge, err := c.CreateDomainName(ctx, &apigateway.CreateDomainNameInput{
		DomainName:     aws.String(edgeName),
		CertificateArn: aws.String(importELBv2Certificate(t, edgeName)),
		Tags:           map[string]string{"team": "edge"},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDomainName(ctx, &apigateway.DeleteDomainNameInput{DomainName: aws.String(edgeName)})
	})
	assert.Regexp(t, regexp.MustCompile(`^d[a-z0-9]{13}\.cloudfront\.net$`), aws.ToString(edge.DistributionDomainName))
	assert.Equal(t, "Z2FDTNDATAQYW2", aws.ToString(edge.DistributionHostedZoneId))
	assert.Equal(t, []apigwtypes.EndpointType{apigwtypes.EndpointTypeEdge}, edge.EndpointConfiguration.Types)
	assert.Equal(t, apigwtypes.DomainNameStatusAvailable, edge.DomainNameStatus)
	assert.Equal(t, "arn:aws:apigateway:us-east-1::/domainnames/"+edgeName, aws.ToString(edge.DomainNameArn))
	assert.NotNil(t, edge.CertificateUploadDate)

	_, err = c.CreateDomainName(ctx, &apigateway.CreateDomainNameInput{
		DomainName:     aws.String(edgeName),
		CertificateArn: edge.CertificateArn,
	})
	var conflict *apigwtypes.ConflictException
	require.ErrorAs(t, err, &conflict, "a second CreateDomainName of one name")

	regionalName := "regional.sdk-domains.example.com"
	regional, err := c.CreateDomainName(ctx, &apigateway.CreateDomainNameInput{
		DomainName:             aws.String(regionalName),
		RegionalCertificateArn: aws.String(importELBv2Certificate(t, regionalName)),
		EndpointConfiguration:  &apigwtypes.EndpointConfiguration{Types: []apigwtypes.EndpointType{apigwtypes.EndpointTypeRegional}},
		SecurityPolicy:         apigwtypes.SecurityPolicyTls12,
	})
	require.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile(`^d-[a-z0-9]{10}\.execute-api\.us-east-1\.amazonaws\.com$`), aws.ToString(regional.RegionalDomainName))
	assert.Equal(t, "Z1UJRXOUMOOFQ8", aws.ToString(regional.RegionalHostedZoneId))

	got, err := c.GetDomainName(ctx, &apigateway.GetDomainNameInput{DomainName: aws.String(regionalName)})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(regional.RegionalDomainName), aws.ToString(got.RegionalDomainName))

	list, err := c.GetDomainNames(ctx, &apigateway.GetDomainNamesInput{})
	require.NoError(t, err)
	var names []string
	for _, d := range list.Items {
		names = append(names, aws.ToString(d.DomainName))
	}
	assert.Contains(t, names, edgeName)
	assert.Contains(t, names, regionalName)

	updated, err := c.UpdateDomainName(ctx, &apigateway.UpdateDomainNameInput{
		DomainName:      aws.String(regionalName),
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/regionalCertificateName", "renamed")},
	})
	require.NoError(t, err)
	assert.Equal(t, "renamed", aws.ToString(updated.RegionalCertificateName))

	_, err = c.DeleteDomainName(ctx, &apigateway.DeleteDomainNameInput{DomainName: aws.String(regionalName)})
	require.NoError(t, err)
	_, err = c.GetDomainName(ctx, &apigateway.GetDomainNameInput{DomainName: aws.String(regionalName)})
	require.ErrorAs(t, err, &notFound, "GetDomainName after DeleteDomainName")
}

// TestAPIGateway_VpcLinks covers CreateVpcLink, GetVpcLink, GetVpcLinks,
// UpdateVpcLink and DeleteVpcLink: a link to a Network Load Balancer turns
// AVAILABLE, and a link to a target that does not exist turns FAILED.
func TestAPIGateway_VpcLinks(t *testing.T) {
	t.Parallel()
	c := apigwClient()
	elb := elbv2Client()

	_, err := c.UpdateVpcLink(ctx, &apigateway.UpdateVpcLinkInput{
		VpcLinkId:       aws.String("absent"),
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/name", "x")},
	})
	var notFound *apigwtypes.NotFoundException
	require.ErrorAs(t, err, &notFound, "UpdateVpcLink of an unknown link")

	lb, err := elb.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name: aws.String("sdk-vpclink-nlb"), Type: elbtypes.LoadBalancerTypeEnumNetwork, Scheme: elbtypes.LoadBalancerSchemeEnumInternal,
	})
	require.NoError(t, err)
	nlbArn := aws.ToString(lb.LoadBalancers[0].LoadBalancerArn)
	t.Cleanup(func() {
		_, _ = elb.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(nlbArn)})
	})

	settle := func(id string) *apigateway.GetVpcLinkOutput {
		t.Helper()
		var link *apigateway.GetVpcLinkOutput
		require.Eventually(t, func() bool {
			link, err = c.GetVpcLink(ctx, &apigateway.GetVpcLinkInput{VpcLinkId: aws.String(id)})
			require.NoError(t, err)
			return link.Status != apigwtypes.VpcLinkStatusPending
		}, 30*time.Second, 100*time.Millisecond, "VPC link %s stayed PENDING", id)
		return link
	}

	created, err := c.CreateVpcLink(ctx, &apigateway.CreateVpcLinkInput{
		Name:       aws.String("sdk-private-backends"),
		TargetArns: []string{nlbArn},
		Tags:       map[string]string{"team": "network"},
	})
	require.NoError(t, err)
	id := aws.ToString(created.Id)
	assert.Equal(t, apigwtypes.VpcLinkStatusPending, created.Status)
	link := settle(id)
	assert.Equal(t, apigwtypes.VpcLinkStatusAvailable, link.Status)
	assert.Equal(t, []string{nlbArn}, link.TargetArns)
	assert.Equal(t, "network", link.Tags["team"])

	broken, err := c.CreateVpcLink(ctx, &apigateway.CreateVpcLinkInput{
		Name:       aws.String("sdk-broken"),
		TargetArns: []string{"arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/net/absent/0123456789abcdef"},
	})
	require.NoError(t, err)
	failed := settle(aws.ToString(broken.Id))
	assert.Equal(t, apigwtypes.VpcLinkStatusFailed, failed.Status)
	assert.NotEmpty(t, aws.ToString(failed.StatusMessage))

	updated, err := c.UpdateVpcLink(ctx, &apigateway.UpdateVpcLinkInput{
		VpcLinkId:       aws.String(id),
		PatchOperations: []apigwtypes.PatchOperation{apigwPatch("/name", "renamed")},
	})
	require.NoError(t, err)
	assert.Equal(t, "renamed", aws.ToString(updated.Name))

	list, err := c.GetVpcLinks(ctx, &apigateway.GetVpcLinksInput{})
	require.NoError(t, err)
	var ids []string
	for _, l := range list.Items {
		ids = append(ids, aws.ToString(l.Id))
	}
	assert.Contains(t, ids, id)

	for _, del := range []string{id, aws.ToString(broken.Id)} {
		_, err = c.DeleteVpcLink(ctx, &apigateway.DeleteVpcLinkInput{VpcLinkId: aws.String(del)})
		require.NoError(t, err)
	}
	_, err = c.GetVpcLink(ctx, &apigateway.GetVpcLinkInput{VpcLinkId: aws.String(id)})
	require.ErrorAs(t, err, &notFound, "GetVpcLink after DeleteVpcLink")
}
