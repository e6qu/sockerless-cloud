package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestELBv2_TrustStoreLifecycle exercises the mutual-TLS trust store control
// plane: CreateTrustStore, DescribeTrustStores, ModifyTrustStore,
// GetTrustStoreCaCertificatesBundle, AddTrustStoreRevocations,
// DescribeTrustStoreRevocations, GetTrustStoreRevocationContent,
// RemoveTrustStoreRevocations, DescribeTrustStoreAssociations,
// DeleteSharedTrustStoreAssociation, GetResourcePolicy, DeleteTrustStore.
func TestELBv2_TrustStoreLifecycle(t *testing.T) {
	c := elbv2Client()

	created, err := c.CreateTrustStore(ctx, &elbv2.CreateTrustStoreInput{
		Name:                         aws.String("mtls-store"),
		CaCertificatesBundleS3Bucket: aws.String("my-ca-bucket"),
		CaCertificatesBundleS3Key:    aws.String("bundle.pem"),
		Tags: []elbtypes.Tag{
			{Key: aws.String("env"), Value: aws.String("test")},
		},
	})
	require.NoError(t, err)
	require.Len(t, created.TrustStores, 1)
	ts := created.TrustStores[0]
	arn := aws.ToString(ts.TrustStoreArn)
	assert.Equal(t, "mtls-store", aws.ToString(ts.Name))
	assert.Equal(t, elbtypes.TrustStoreStatusActive, ts.Status)
	assert.NotZero(t, aws.ToInt32(ts.NumberOfCaCertificates))

	t.Cleanup(func() {
		_, _ = c.DeleteTrustStore(ctx, &elbv2.DeleteTrustStoreInput{TrustStoreArn: aws.String(arn)})
	})

	// Describe by ARN and by Name.
	descByArn, err := c.DescribeTrustStores(ctx, &elbv2.DescribeTrustStoresInput{
		TrustStoreArns: []string{arn},
	})
	require.NoError(t, err)
	require.Len(t, descByArn.TrustStores, 1)
	assert.Equal(t, arn, aws.ToString(descByArn.TrustStores[0].TrustStoreArn))

	descByName, err := c.DescribeTrustStores(ctx, &elbv2.DescribeTrustStoresInput{
		Names: []string{"mtls-store"},
	})
	require.NoError(t, err)
	require.Len(t, descByName.TrustStores, 1)

	// Modify the bundle reference.
	modified, err := c.ModifyTrustStore(ctx, &elbv2.ModifyTrustStoreInput{
		TrustStoreArn:                aws.String(arn),
		CaCertificatesBundleS3Bucket: aws.String("my-ca-bucket"),
		CaCertificatesBundleS3Key:    aws.String("bundle-v2.pem"),
	})
	require.NoError(t, err)
	require.Len(t, modified.TrustStores, 1)

	// CA bundle location.
	bundle, err := c.GetTrustStoreCaCertificatesBundle(ctx, &elbv2.GetTrustStoreCaCertificatesBundleInput{
		TrustStoreArn: aws.String(arn),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(bundle.Location))

	// Add two revocation lists.
	added, err := c.AddTrustStoreRevocations(ctx, &elbv2.AddTrustStoreRevocationsInput{
		TrustStoreArn: aws.String(arn),
		RevocationContents: []elbtypes.RevocationContent{
			{S3Bucket: aws.String("my-ca-bucket"), S3Key: aws.String("crl-1.pem"), RevocationType: elbtypes.RevocationTypeCrl},
			{S3Bucket: aws.String("my-ca-bucket"), S3Key: aws.String("crl-2.pem"), RevocationType: elbtypes.RevocationTypeCrl},
		},
	})
	require.NoError(t, err)
	require.Len(t, added.TrustStoreRevocations, 2)
	firstRevID := aws.ToInt64(added.TrustStoreRevocations[0].RevocationId)

	// Describe revocations: both present.
	revs, err := c.DescribeTrustStoreRevocations(ctx, &elbv2.DescribeTrustStoreRevocationsInput{
		TrustStoreArn: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Len(t, revs.TrustStoreRevocations, 2)

	// Revocation content location.
	content, err := c.GetTrustStoreRevocationContent(ctx, &elbv2.GetTrustStoreRevocationContentInput{
		TrustStoreArn: aws.String(arn),
		RevocationId:  aws.Int64(firstRevID),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(content.Location))

	// Remove the first revocation; one remains.
	_, err = c.RemoveTrustStoreRevocations(ctx, &elbv2.RemoveTrustStoreRevocationsInput{
		TrustStoreArn: aws.String(arn),
		RevocationIds: []int64{firstRevID},
	})
	require.NoError(t, err)
	revs2, err := c.DescribeTrustStoreRevocations(ctx, &elbv2.DescribeTrustStoreRevocationsInput{
		TrustStoreArn: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Len(t, revs2.TrustStoreRevocations, 1)

	// No associations yet.
	assoc, err := c.DescribeTrustStoreAssociations(ctx, &elbv2.DescribeTrustStoreAssociationsInput{
		TrustStoreArn: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Empty(t, assoc.TrustStoreAssociations)

	// Resource policy for the (shareable) trust store.
	pol, err := c.GetResourcePolicy(ctx, &elbv2.GetResourcePolicyInput{
		ResourceArn: aws.String(arn),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(pol.Policy))

	// Delete a (nonexistent) shared association fails cleanly.
	_, err = c.DeleteSharedTrustStoreAssociation(ctx, &elbv2.DeleteSharedTrustStoreAssociationInput{
		TrustStoreArn: aws.String(arn),
		ResourceArn:   aws.String("arn:aws:elasticloadbalancing:us-east-1:000000000000:listener/app/x/y/z"),
	})
	assert.Error(t, err)
}

// TestELBv2_DescribeSSLPolicies verifies the predefined SSL security policy
// catalog: the full set, a name-filtered lookup, and a load-balancer-type
// filter — each policy carries real Ciphers and SslProtocols.
func TestELBv2_DescribeSSLPolicies(t *testing.T) {
	c := elbv2Client()

	all, err := c.DescribeSSLPolicies(ctx, &elbv2.DescribeSSLPoliciesInput{})
	require.NoError(t, err)
	require.NotEmpty(t, all.SslPolicies)
	for _, p := range all.SslPolicies {
		assert.NotEmpty(t, aws.ToString(p.Name))
		assert.NotEmpty(t, p.SslProtocols)
		assert.NotEmpty(t, p.Ciphers)
	}

	named, err := c.DescribeSSLPolicies(ctx, &elbv2.DescribeSSLPoliciesInput{
		Names: []string{"ELBSecurityPolicy-TLS13-1-2-2021-06"},
	})
	require.NoError(t, err)
	require.Len(t, named.SslPolicies, 1)
	assert.Equal(t, "ELBSecurityPolicy-TLS13-1-2-2021-06", aws.ToString(named.SslPolicies[0].Name))
	assert.Contains(t, named.SslPolicies[0].SslProtocols, "TLSv1.3")

	byType, err := c.DescribeSSLPolicies(ctx, &elbv2.DescribeSSLPoliciesInput{
		LoadBalancerType: elbtypes.LoadBalancerTypeEnumApplication,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, byType.SslPolicies)
}

// TestELBv2_ModifyCapacityReservationAndIpPools verifies ModifyCapacityReservation
// records a configured minimum and ModifyIpPools assigns/removes an IPAM pool.
func TestELBv2_ModifyCapacityReservationAndIpPools(t *testing.T) {
	ec2c := ec2Client()
	vpc, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.74.0.0/16")})
	require.NoError(t, err)
	sub, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.74.1.0/24"),
	})
	require.NoError(t, err)

	c := elbv2Client()
	lb, err := c.CreateLoadBalancer(ctx, &elbv2.CreateLoadBalancerInput{
		Name:    aws.String("cap-mod-lb"),
		Type:    elbtypes.LoadBalancerTypeEnumApplication,
		Subnets: []string{aws.ToString(sub.Subnet.SubnetId)},
	})
	require.NoError(t, err)
	arn := aws.ToString(lb.LoadBalancers[0].LoadBalancerArn)
	t.Cleanup(func() {
		_, _ = c.DeleteLoadBalancer(ctx, &elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String(arn)})
	})

	mod, err := c.ModifyCapacityReservation(ctx, &elbv2.ModifyCapacityReservationInput{
		LoadBalancerArn: aws.String(arn),
		MinimumLoadBalancerCapacity: &elbtypes.MinimumLoadBalancerCapacity{
			CapacityUnits: aws.Int32(100),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, mod.MinimumLoadBalancerCapacity)
	assert.Equal(t, int32(100), aws.ToInt32(mod.MinimumLoadBalancerCapacity.CapacityUnits))

	// Reset clears the configured minimum.
	reset, err := c.ModifyCapacityReservation(ctx, &elbv2.ModifyCapacityReservationInput{
		LoadBalancerArn:          aws.String(arn),
		ResetCapacityReservation: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Nil(t, reset.MinimumLoadBalancerCapacity)

	pools, err := c.ModifyIpPools(ctx, &elbv2.ModifyIpPoolsInput{
		LoadBalancerArn: aws.String(arn),
		IpamPools:       &elbtypes.IpamPools{Ipv4IpamPoolId: aws.String("ipam-pool-0abc123")},
	})
	require.NoError(t, err)
	require.NotNil(t, pools.IpamPools)
	assert.Equal(t, "ipam-pool-0abc123", aws.ToString(pools.IpamPools.Ipv4IpamPoolId))
}
