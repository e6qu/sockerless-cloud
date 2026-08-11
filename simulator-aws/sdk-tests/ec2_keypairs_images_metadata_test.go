package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_KeyPairsSDK covers CreateKeyPair/ImportKeyPair/DeleteKeyPair and a
// real DescribeKeyPairs (previously always empty) — backs aws_key_pair.
func TestEC2_KeyPairsSDK(t *testing.T) {
	c := ec2Client()

	created, err := c.CreateKeyPair(ctx, &ec2.CreateKeyPairInput{KeyName: aws.String("sdk-created-key")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{KeyName: aws.String("sdk-created-key")}) })
	assert.Contains(t, aws.ToString(created.KeyMaterial), "PRIVATE KEY", "CreateKeyPair returns the private material")
	assert.NotEmpty(t, aws.ToString(created.KeyFingerprint))
	assert.Contains(t, aws.ToString(created.KeyPairId), "key-")

	pub := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQexample sockerless@test"
	imported, err := c.ImportKeyPair(ctx, &ec2.ImportKeyPairInput{
		KeyName: aws.String("sdk-imported-key"), PublicKeyMaterial: []byte(pub),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{KeyName: aws.String("sdk-imported-key")}) })
	require.NotEmpty(t, aws.ToString(imported.KeyFingerprint))

	// DescribeKeyPairs returns the stored pairs (filtered by name).
	out, err := c.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{KeyNames: []string{"sdk-created-key"}})
	require.NoError(t, err)
	require.Len(t, out.KeyPairs, 1)
	assert.Equal(t, "sdk-created-key", aws.ToString(out.KeyPairs[0].KeyName))
	assert.Equal(t, aws.ToString(created.KeyFingerprint), aws.ToString(out.KeyPairs[0].KeyFingerprint))

	// Delete then confirm it's gone.
	_, err = c.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{KeyName: aws.String("sdk-created-key")})
	require.NoError(t, err)
	gone, err := c.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{
		Filters: []types.Filter{{Name: aws.String("key-name"), Values: []string{"sdk-created-key"}}},
	})
	require.NoError(t, err)
	assert.Empty(t, gone.KeyPairs, "deleted key pair must not be returned")
}

// TestEC2_ModifyInstanceMetadataOptionsSDK covers in-place metadata_options
// updates (the aws_instance.metadata_options update path).
func TestEC2_ModifyInstanceMetadataOptionsSDK(t *testing.T) {
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.140.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.140.1.0/24")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String("ami-12345678"), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
	})
	require.NoError(t, err)
	id := aws.ToString(run.Instances[0].InstanceId)

	_, err = c.ModifyInstanceMetadataOptions(ctx, &ec2.ModifyInstanceMetadataOptionsInput{
		InstanceId: aws.String(id),
		HttpTokens: types.HttpTokensStateRequired, HttpPutResponseHopLimit: aws.Int32(3),
	})
	require.NoError(t, err)

	got := describeOneInstance(t, c, id)
	require.NotNil(t, got.MetadataOptions)
	assert.Equal(t, types.HttpTokensStateRequired, got.MetadataOptions.HttpTokens, "metadata http_tokens must persist after modify")
	assert.Equal(t, int32(3), aws.ToInt32(got.MetadataOptions.HttpPutResponseHopLimit))
}

// TestEC2_LaunchTemplateMarketCreditSDK covers the launch-template
// instance_market_options + credit_specification round-trip.
func TestEC2_LaunchTemplateMarketCreditSDK(t *testing.T) {
	c := ec2Client()
	lt, err := c.CreateLaunchTemplate(ctx, &ec2.CreateLaunchTemplateInput{
		LaunchTemplateName: aws.String("market-credit-lt"),
		LaunchTemplateData: &types.RequestLaunchTemplateData{
			ImageId: aws.String("ami-12345678"), InstanceType: types.InstanceTypeT3Micro,
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
	c := ec2Client()
	out, err := c.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{"amazon"},
		Filters: []types.Filter{
			{Name: aws.String("name"), Values: []string{"al2023-ami-minimal"}},
			{Name: aws.String("architecture"), Values: []string{"arm64"}},
			{Name: aws.String("root-device-type"), Values: []string{"ebs"}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.Images, "filtered DescribeImages must resolve to an image")
	img := out.Images[0]
	assert.Equal(t, "al2023-ami-minimal", aws.ToString(img.Name), "the name filter must flow into the resolved image")
	assert.Equal(t, types.ArchitectureValuesArm64, img.Architecture, "the architecture filter must be honoured")
	assert.True(t, strings.HasPrefix(aws.ToString(img.ImageId), "ami-"))
}
