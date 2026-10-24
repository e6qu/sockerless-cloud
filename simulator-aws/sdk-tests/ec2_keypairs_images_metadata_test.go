package aws_sdk_test

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// testRSAPublicKey is an OpenSSH RSA public key whose MD5 fingerprint,
// as ssh-keygen -l -E md5 reports it, is testRSAPublicKeyMD5.
const (
	testRSAPublicKey    = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDTZERMdSlqD/Ot+7wjgU7BkrYryTecvQMxKvIVNkdzTuYr6DitxLO7j3mbGZT7ly8aNCcKWKXnRA0C1fp7q6GQ+rhttiBg0i6q5ArpUOIqrKt3BqjZPF+HEyWXb1/ip2flCP19PQHFmIhcceyineRE9pP5aeQ2fCBZOG9/JWFMUeSa269IxCs8XsJR1pQRVuI4wlsot0kuHTOYteMykgBjbwNnO7UhZTlJJVaoX0Prk1dFHjWnckgMYBA+ePIRoFWtHXcfhhM/bfQ5Got0JJET2umvr0Vb8Jh/el0rqNOBthz+uI+fAkLyvWmRwwRL1mkKe9G2dsltyDMYwYPV7WeN sockerless@test"
	testRSAPublicKeyMD5 = "14:41:93:6c:46:cb:ab:57:36:8f:f6:91:57:b4:6a:09"
)

// TestEC2_KeyPairsSDK covers CreateKeyPair, ImportKeyPair, DescribeKeyPairs
// and DeleteKeyPair with the fingerprints EC2 documents: the SHA-1 digest of
// the DER-encoded private key for an RSA pair it creates, the MD5 digest of
// the public key for an imported RSA key, and the base64 SHA-256 digest of
// the public key for ED25519.
func TestEC2_KeyPairsSDK(t *testing.T) {
	t.Parallel()
	c := ec2Client()

	created, err := c.CreateKeyPair(ctx, &ec2.CreateKeyPairInput{KeyName: aws.String("sdk-created-key")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{KeyName: aws.String("sdk-created-key")}) })
	assert.Contains(t, aws.ToString(created.KeyPairId), "key-")
	block, _ := pem.Decode([]byte(aws.ToString(created.KeyMaterial)))
	require.NotNil(t, block, "CreateKeyPair returns the private key as PEM")
	require.Equal(t, "RSA PRIVATE KEY", block.Type)
	rsaKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	require.NoError(t, err)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	require.NoError(t, err)
	sum := sha1.Sum(pkcs8)
	assert.Equal(t, colonHex(sum[:]), aws.ToString(created.KeyFingerprint))

	ed, err := c.CreateKeyPair(ctx, &ec2.CreateKeyPairInput{KeyName: aws.String("sdk-created-ed25519"), KeyType: types.KeyTypeEd25519})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{KeyName: aws.String("sdk-created-ed25519")})
	})
	edKey, err := ssh.ParseRawPrivateKey([]byte(aws.ToString(ed.KeyMaterial)))
	require.NoError(t, err, "an ED25519 pair's private key is in OpenSSH format")
	edSigner, err := ssh.NewSignerFromKey(edKey)
	require.NoError(t, err)
	edSum := sha256.Sum256(edSigner.PublicKey().Marshal())
	assert.Equal(t, base64.StdEncoding.EncodeToString(edSum[:]), aws.ToString(ed.KeyFingerprint))

	imported, err := c.ImportKeyPair(ctx, &ec2.ImportKeyPairInput{
		KeyName: aws.String("sdk-imported-key"), PublicKeyMaterial: []byte(testRSAPublicKey),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{KeyName: aws.String("sdk-imported-key")}) })
	assert.Equal(t, testRSAPublicKeyMD5, aws.ToString(imported.KeyFingerprint))

	_, err = c.ImportKeyPair(ctx, &ec2.ImportKeyPairInput{
		KeyName: aws.String("sdk-bad-key"), PublicKeyMaterial: []byte("ssh-rsa not-a-key"),
	})
	var apiErr smithy.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "InvalidKey.Format", apiErr.ErrorCode())

	// DescribeKeyPairs returns the public key only when asked.
	out, err := c.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{KeyNames: []string{"sdk-imported-key"}})
	require.NoError(t, err)
	require.Len(t, out.KeyPairs, 1)
	assert.Nil(t, out.KeyPairs[0].PublicKey)
	out, err = c.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{KeyNames: []string{"sdk-imported-key", "sdk-created-ed25519"}, IncludePublicKey: aws.Bool(true)})
	require.NoError(t, err)
	require.Len(t, out.KeyPairs, 2)
	for _, kp := range out.KeyPairs {
		switch aws.ToString(kp.KeyName) {
		case "sdk-imported-key":
			assert.Equal(t, testRSAPublicKey, aws.ToString(kp.PublicKey))
			assert.Equal(t, types.KeyTypeRsa, kp.KeyType)
		case "sdk-created-ed25519":
			assert.Equal(t, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(edSigner.PublicKey())))+" sdk-created-ed25519", aws.ToString(kp.PublicKey))
			assert.Equal(t, types.KeyTypeEd25519, kp.KeyType)
		}
	}
	_, err = c.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{KeyNames: []string{"sdk-no-such-key"}})
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "InvalidKeyPair.NotFound", apiErr.ErrorCode())

	// Delete then confirm it's gone.
	_, err = c.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{KeyName: aws.String("sdk-created-key")})
	require.NoError(t, err)
	gone, err := c.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{
		Filters: []types.Filter{{Name: aws.String("key-name"), Values: []string{"sdk-created-key"}}},
	})
	require.NoError(t, err)
	assert.Empty(t, gone.KeyPairs, "deleted key pair must not be returned")
}

func colonHex(b []byte) string {
	parts := make([]string, len(b))
	for i, v := range b {
		parts[i] = fmt.Sprintf("%02x", v)
	}
	return strings.Join(parts, ":")
}

// TestEC2_ModifyInstanceMetadataOptionsSDK covers in-place metadata_options
// updates (the aws_instance.metadata_options update path).
func TestEC2_ModifyInstanceMetadataOptionsSDK(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.251.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.251.1.0/24")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
	})
	require.NoError(t, err)
	terminateEC2InstancesOnCleanup(t, c, run)
	id := aws.ToString(run.Instances[0].InstanceId)
	require.NotNil(t, run.Instances[0].MetadataOptions)
	assert.Equal(t, types.InstanceMetadataProtocolStateDisabled, run.Instances[0].MetadataOptions.HttpProtocolIpv6, "the IPv6 endpoint is disabled unless the launch enables it")

	_, err = c.ModifyInstanceMetadataOptions(ctx, &ec2.ModifyInstanceMetadataOptionsInput{
		InstanceId: aws.String(id),
		HttpTokens: types.HttpTokensStateRequired, HttpPutResponseHopLimit: aws.Int32(3),
		HttpProtocolIpv6: types.InstanceMetadataProtocolStateEnabled,
	})
	require.NoError(t, err)

	got := describeOneInstance(t, c, id)
	require.NotNil(t, got.MetadataOptions)
	assert.Equal(t, types.HttpTokensStateRequired, got.MetadataOptions.HttpTokens, "metadata http_tokens must persist after modify")
	assert.Equal(t, int32(3), aws.ToInt32(got.MetadataOptions.HttpPutResponseHopLimit))
	assert.Equal(t, types.InstanceMetadataProtocolStateEnabled, got.MetadataOptions.HttpProtocolIpv6, "the IPv6 endpoint setting persists after modify")
}

// TestEC2_LaunchTemplateMarketCreditSDK covers the launch-template
// instance_market_options + credit_specification round-trip.
func TestEC2_LaunchTemplateMarketCreditSDK(t *testing.T) {
	t.Parallel()
	ltName := uniqueName("market-credit-lt")
	c := ec2Client()
	lt, err := c.CreateLaunchTemplate(ctx, &ec2.CreateLaunchTemplateInput{
		LaunchTemplateName: aws.String(ltName),
		LaunchTemplateData: &types.RequestLaunchTemplateData{
			ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: types.InstanceTypeT3Micro,
			CreditSpecification: &types.CreditSpecificationRequest{CpuCredits: aws.String("unlimited")},
			InstanceMarketOptions: &types.LaunchTemplateInstanceMarketOptionsRequest{
				MarketType: types.MarketTypeSpot,
				SpotOptions: &types.LaunchTemplateSpotMarketOptionsRequest{
					MaxPrice:                     aws.String("0.05"),
					SpotInstanceType:             types.SpotInstanceTypeOneTime,
					InstanceInterruptionBehavior: types.InstanceInterruptionBehaviorTerminate,
				},
			},
		},
	})
	require.NoError(t, err)

	ver, err := c.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{
		LaunchTemplateId: lt.LaunchTemplate.LaunchTemplateId, Versions: []string{"$Latest"},
	})
	require.NoError(t, err)
	require.Len(t, ver.LaunchTemplateVersions, 1)
	d := ver.LaunchTemplateVersions[0].LaunchTemplateData
	require.NotNil(t, d.CreditSpecification)
	assert.Equal(t, "unlimited", aws.ToString(d.CreditSpecification.CpuCredits), "credit_specification must round-trip")
	require.NotNil(t, d.InstanceMarketOptions)
	assert.Equal(t, types.MarketTypeSpot, d.InstanceMarketOptions.MarketType)
	require.NotNil(t, d.InstanceMarketOptions.SpotOptions)
	assert.Equal(t, "0.05", aws.ToString(d.InstanceMarketOptions.SpotOptions.MaxPrice), "spot max_price must round-trip")
}

// TestEC2_DescribeImagesFiltersSDK covers DescribeImages honouring filters so
// data.aws_ami resolves by name/architecture (was ignored).
func TestEC2_DescribeImagesFiltersSDK(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	out, err := c.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{"amazon"},
		Filters: []types.Filter{
			{Name: aws.String("name"), Values: []string{"al2023-ami-2023*-arm64"}},
			{Name: aws.String("architecture"), Values: []string{"arm64"}},
			{Name: aws.String("root-device-type"), Values: []string{"ebs"}},
		},
	})
	require.NoError(t, err)
	require.Len(t, out.Images, 1, "one Amazon Linux 2023 arm64 AMI is published")
	img := out.Images[0]
	assert.Regexp(t, `^al2023-ami-2023\..*-kernel-6\.1-arm64$`, aws.ToString(img.Name), "the name filter selects the arm64 build")
	assert.Equal(t, types.ArchitectureValuesArm64, img.Architecture, "the architecture filter must be honoured")
	assert.Equal(t, "amazon", aws.ToString(img.ImageOwnerAlias))
	assert.Regexp(t, `^ami-0[0-9a-f]{16}$`, aws.ToString(img.ImageId))
	assert.True(t, aws.ToBool(img.Public))

	// Every public AMI the account can launch lists under its owner.
	canonical, err := c.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners:  []string{"099720109477"},
		Filters: []types.Filter{{Name: aws.String("name"), Values: []string{"ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-*"}}},
	})
	require.NoError(t, err)
	require.Len(t, canonical.Images, 2, "Ubuntu 24.04 for amd64 and arm64")
	_, err = c.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{"ami-0123456789abcdef0"}})
	requireAWSErrorCode(t, err, "InvalidAMIID.NotFound")
}
