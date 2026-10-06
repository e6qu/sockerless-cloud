package aws_sdk_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
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
	s3c := s3Client()
	caBucket := uniqueName("my-ca-bucket")
	s3CreateBucket(t, s3c, caBucket)
	firstCA, firstKey := elbv2TestCA(t, "first")
	secondCA, _ := elbv2TestCA(t, "second")
	s3Put(t, s3c, caBucket, "bundle.pem", string(elbv2PEM("CERTIFICATE", firstCA.Raw))+string(elbv2PEM("CERTIFICATE", secondCA.Raw)))
	s3Put(t, s3c, caBucket, "bundle-v2.pem", string(elbv2PEM("CERTIFICATE", secondCA.Raw)))
	s3Put(t, s3c, caBucket, "crl-1.pem", string(elbv2TestCRL(t, firstCA, firstKey, 3)))
	s3Put(t, s3c, caBucket, "crl-2.pem", string(elbv2TestCRL(t, firstCA, firstKey, 2)))

	created, err := c.CreateTrustStore(ctx, &elbv2.CreateTrustStoreInput{
		Name:                         aws.String("mtls-store"),
		CaCertificatesBundleS3Bucket: aws.String(caBucket),
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
	assert.Equal(t, int32(2), aws.ToInt32(ts.NumberOfCaCertificates), "the bundle holds two CA certificates")

	t.Cleanup(func() {
		_, _ = c.DeleteTrustStore(ctx, &elbv2.DeleteTrustStoreInput{TrustStoreArn: aws.String(arn)})
	})

	tags, err := c.DescribeTags(ctx, &elbv2.DescribeTagsInput{ResourceArns: []string{arn}})
	require.NoError(t, err)
	require.Len(t, tags.TagDescriptions, 1)
	assert.Equal(t, []elbtypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}}, tags.TagDescriptions[0].Tags,
		"the trust store keeps the tags CreateTrustStore carried")

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
		CaCertificatesBundleS3Bucket: aws.String(caBucket),
		CaCertificatesBundleS3Key:    aws.String("bundle-v2.pem"),
	})
	require.NoError(t, err)
	require.Len(t, modified.TrustStores, 1)
	assert.Equal(t, int32(1), aws.ToInt32(modified.TrustStores[0].NumberOfCaCertificates))

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
			{S3Bucket: aws.String(caBucket), S3Key: aws.String("crl-1.pem"), RevocationType: elbtypes.RevocationTypeCrl},
			{S3Bucket: aws.String(caBucket), S3Key: aws.String("crl-2.pem"), RevocationType: elbtypes.RevocationTypeCrl},
		},
	})
	require.NoError(t, err)
	require.Len(t, added.TrustStoreRevocations, 2)
	assert.Equal(t, int64(3), aws.ToInt64(added.TrustStoreRevocations[0].NumberOfRevokedEntries))
	assert.Equal(t, int64(2), aws.ToInt64(added.TrustStoreRevocations[1].NumberOfRevokedEntries))
	described, err := c.DescribeTrustStores(ctx, &elbv2.DescribeTrustStoresInput{TrustStoreArns: []string{arn}})
	require.NoError(t, err)
	assert.Equal(t, int64(5), aws.ToInt64(described.TrustStores[0].TotalRevokedEntries))
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

// TestELBv2_TrustStoreReadsItsBundleFromS3 refuses a CA bundle or a revocation
// list Amazon S3 does not hold, and one that is not what it should be, and
// reads the object version a trust store names rather than the current one.
func TestELBv2_TrustStoreReadsItsBundleFromS3(t *testing.T) {
	c := elbv2Client()
	s3c := s3Client()
	bucket := s3VersionedBucket(t, s3c, "ts-bundles", s3types.BucketVersioningStatusEnabled)
	first, firstKey := elbv2TestCA(t, "first")
	second, _ := elbv2TestCA(t, "second")
	oneCert := aws.ToString(s3Put(t, s3c, bucket, "bundle.pem", string(elbv2PEM("CERTIFICATE", first.Raw))).VersionId)
	s3Put(t, s3c, bucket, "bundle.pem", string(elbv2PEM("CERTIFICATE", first.Raw))+string(elbv2PEM("CERTIFICATE", second.Raw)))
	s3Put(t, s3c, bucket, "not-a-bundle.pem", "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n")
	s3Put(t, s3c, bucket, "garbage.pem", "not pem at all")
	s3Put(t, s3c, bucket, "crl.pem", string(elbv2TestCRL(t, first, firstKey, 1)))

	create := func(name, key, version string) (*elbv2.CreateTrustStoreOutput, error) {
		in := &elbv2.CreateTrustStoreInput{Name: aws.String(name),
			CaCertificatesBundleS3Bucket: aws.String(bucket), CaCertificatesBundleS3Key: aws.String(key)}
		if version != "" {
			in.CaCertificatesBundleS3ObjectVersion = aws.String(version)
		}
		return c.CreateTrustStore(ctx, in)
	}
	code := func(err error) string {
		t.Helper()
		var apiErr smithy.APIError
		require.True(t, errors.As(err, &apiErr), "expected an Elastic Load Balancing error, got %v", err)
		return apiErr.ErrorCode()
	}
	_, err := create("ts-missing", "absent.pem", "")
	assert.Equal(t, "CaCertificatesBundleNotFound", code(err))
	_, err = create("ts-missing-version", "bundle.pem", "noSuchVersionId")
	assert.Equal(t, "CaCertificatesBundleNotFound", code(err))
	_, err = create("ts-key", "not-a-bundle.pem", "")
	assert.Equal(t, "InvalidCaCertificatesBundle", code(err))
	_, err = create("ts-garbage", "garbage.pem", "")
	assert.Equal(t, "InvalidCaCertificatesBundle", code(err))

	pinned, err := create("ts-pinned", "bundle.pem", oneCert)
	require.NoError(t, err)
	arn := aws.ToString(pinned.TrustStores[0].TrustStoreArn)
	t.Cleanup(func() { _, _ = c.DeleteTrustStore(ctx, &elbv2.DeleteTrustStoreInput{TrustStoreArn: aws.String(arn)}) })
	assert.Equal(t, int32(1), aws.ToInt32(pinned.TrustStores[0].NumberOfCaCertificates), "the named version holds one certificate")

	_, err = c.ModifyTrustStore(ctx, &elbv2.ModifyTrustStoreInput{TrustStoreArn: aws.String(arn),
		CaCertificatesBundleS3Bucket: aws.String(bucket), CaCertificatesBundleS3Key: aws.String("garbage.pem")})
	assert.Equal(t, "InvalidCaCertificatesBundle", code(err))
	current, err := c.ModifyTrustStore(ctx, &elbv2.ModifyTrustStoreInput{TrustStoreArn: aws.String(arn),
		CaCertificatesBundleS3Bucket: aws.String(bucket), CaCertificatesBundleS3Key: aws.String("bundle.pem")})
	require.NoError(t, err)
	assert.Equal(t, int32(2), aws.ToInt32(current.TrustStores[0].NumberOfCaCertificates), "the current version holds two")

	revoke := func(key string) error {
		_, err := c.AddTrustStoreRevocations(ctx, &elbv2.AddTrustStoreRevocationsInput{TrustStoreArn: aws.String(arn),
			RevocationContents: []elbtypes.RevocationContent{{S3Bucket: aws.String(bucket), S3Key: aws.String(key), RevocationType: elbtypes.RevocationTypeCrl}}})
		return err
	}
	assert.Equal(t, "RevocationContentNotFound", code(revoke("absent-crl.pem")))
	assert.Equal(t, "InvalidRevocationContent", code(revoke("bundle.pem")))
	require.NoError(t, revoke("crl.pem"))
	_, err = c.DescribeTrustStores(ctx, &elbv2.DescribeTrustStoresInput{TrustStoreArns: []string{"arn:aws:elasticloadbalancing:us-east-1:000000000000:truststore/absent/0123456789abcdef"}})
	assert.Equal(t, "TrustStoreNotFound", code(err))
}

// elbv2TestCA makes a self-signed CA certificate.
func elbv2TestCA(t *testing.T, name string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name + " CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert, key
}

// elbv2TestCRL makes a PEM certificate revocation list, issued by ca, that
// revokes entries serial numbers.
func elbv2TestCRL(t *testing.T, ca *x509.Certificate, key *ecdsa.PrivateKey, entries int) []byte {
	t.Helper()
	list := &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: time.Now().Add(-time.Hour), NextUpdate: time.Now().Add(24 * time.Hour)}
	for i := range entries {
		list.RevokedCertificateEntries = append(list.RevokedCertificateEntries,
			x509.RevocationListEntry{SerialNumber: big.NewInt(int64(100 + i)), RevocationTime: time.Now().Add(-time.Minute)})
	}
	der, err := x509.CreateRevocationList(rand.Reader, list, ca, key)
	require.NoError(t, err)
	return elbv2PEM("X509 CRL", der)
}

func elbv2PEM(blockType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
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
