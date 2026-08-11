package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_LaunchTemplateLifecycle covers the EC2 Launch Template
// create → describe → describe-versions → delete lifecycle, the fck-nat
// NAT-instance shape (aws_launch_template as the ASG launch config). The
// launch-template data submitted on create must round-trip verbatim through
// DescribeLaunchTemplateVersions, so the Terraform provider sees no drift.
func TestEC2_LaunchTemplateLifecycle(t *testing.T) {
	client := ec2Client()

	data := &types.RequestLaunchTemplateData{
		ImageId:      aws.String("ami-12345678"),
		InstanceType: types.InstanceTypeT4gNano,
		KeyName:      aws.String("nat-key"),
		EbsOptimized: aws.Bool(true),
		IamInstanceProfile: &types.LaunchTemplateIamInstanceProfileSpecificationRequest{
			Name: aws.String("fck-nat-profile"),
		},
		Monitoring: &types.LaunchTemplatesMonitoringRequest{Enabled: aws.Bool(true)},
		NetworkInterfaces: []types.LaunchTemplateInstanceNetworkInterfaceSpecificationRequest{{
			DeviceIndex:              aws.Int32(0),
			AssociatePublicIpAddress: aws.Bool(true),
			DeleteOnTermination:      aws.Bool(true),
			Description:              aws.String("primary"),
			Groups:                   []string{"sg-0abc1234", "sg-0def5678"},
		}},
		BlockDeviceMappings: []types.LaunchTemplateBlockDeviceMappingRequest{{
			DeviceName: aws.String("/dev/xvda"),
			Ebs: &types.LaunchTemplateEbsBlockDeviceRequest{
				VolumeSize:          aws.Int32(8),
				VolumeType:          types.VolumeTypeGp3,
				DeleteOnTermination: aws.Bool(true),
				Encrypted:           aws.Bool(true),
			},
		}},
		MetadataOptions: &types.LaunchTemplateInstanceMetadataOptionsRequest{
			HttpTokens:              types.LaunchTemplateHttpTokensStateRequired,
			HttpPutResponseHopLimit: aws.Int32(2),
			HttpEndpoint:            types.LaunchTemplateInstanceMetadataEndpointStateEnabled,
		},
		TagSpecifications: []types.LaunchTemplateTagSpecificationRequest{{
			ResourceType: types.ResourceTypeInstance,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("fck-nat")}},
		}},
	}

	created, err := client.CreateLaunchTemplate(ctx, &ec2.CreateLaunchTemplateInput{
		LaunchTemplateName: aws.String("fck-nat-lt"),
		VersionDescription: aws.String("v1"),
		LaunchTemplateData: data,
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeLaunchTemplate,
			Tags:         []types.Tag{{Key: aws.String("module"), Value: aws.String("fck-nat")}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, created.LaunchTemplate)
	ltID := aws.ToString(created.LaunchTemplate.LaunchTemplateId)
	assert.Contains(t, ltID, "lt-")
	assert.Equal(t, "fck-nat-lt", aws.ToString(created.LaunchTemplate.LaunchTemplateName))
	assert.Equal(t, int64(1), aws.ToInt64(created.LaunchTemplate.DefaultVersionNumber))
	assert.Equal(t, int64(1), aws.ToInt64(created.LaunchTemplate.LatestVersionNumber))
	require.Len(t, created.LaunchTemplate.Tags, 1)
	assert.Equal(t, "module", aws.ToString(created.LaunchTemplate.Tags[0].Key))

	// Re-creating the same name conflicts (real AWS AlreadyExistsException).
	_, err = client.CreateLaunchTemplate(ctx, &ec2.CreateLaunchTemplateInput{
		LaunchTemplateName: aws.String("fck-nat-lt"),
		LaunchTemplateData: &types.RequestLaunchTemplateData{ImageId: aws.String("ami-99999999")},
	})
	require.Error(t, err)

	// DescribeLaunchTemplates by name filters to the one template.
	listed, err := client.DescribeLaunchTemplates(ctx, &ec2.DescribeLaunchTemplatesInput{
		LaunchTemplateNames: []string{"fck-nat-lt"},
	})
	require.NoError(t, err)
	require.Len(t, listed.LaunchTemplates, 1)
	assert.Equal(t, ltID, aws.ToString(listed.LaunchTemplates[0].LaunchTemplateId))

	// DescribeLaunchTemplateVersions with $Latest must echo the data back.
	versions, err := client.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{
		LaunchTemplateId: aws.String(ltID),
		Versions:         []string{"$Latest"},
	})
	require.NoError(t, err)
	require.Len(t, versions.LaunchTemplateVersions, 1)
	v := versions.LaunchTemplateVersions[0]
	assert.Equal(t, int64(1), aws.ToInt64(v.VersionNumber))
	assert.True(t, aws.ToBool(v.DefaultVersion))
	assert.Equal(t, "v1", aws.ToString(v.VersionDescription))

	rd := v.LaunchTemplateData
	require.NotNil(t, rd)
	assert.Equal(t, "ami-12345678", aws.ToString(rd.ImageId))
	assert.Equal(t, types.InstanceTypeT4gNano, rd.InstanceType)
	assert.Equal(t, "nat-key", aws.ToString(rd.KeyName))
	assert.True(t, aws.ToBool(rd.EbsOptimized))
	require.NotNil(t, rd.IamInstanceProfile)
	assert.Equal(t, "fck-nat-profile", aws.ToString(rd.IamInstanceProfile.Name))
	require.NotNil(t, rd.Monitoring)
	assert.True(t, aws.ToBool(rd.Monitoring.Enabled))

	require.Len(t, rd.NetworkInterfaces, 1)
	ni := rd.NetworkInterfaces[0]
	assert.Equal(t, int32(0), aws.ToInt32(ni.DeviceIndex))
	assert.True(t, aws.ToBool(ni.AssociatePublicIpAddress))
	assert.True(t, aws.ToBool(ni.DeleteOnTermination))
	assert.ElementsMatch(t, []string{"sg-0abc1234", "sg-0def5678"}, ni.Groups)

	require.Len(t, rd.BlockDeviceMappings, 1)
	bdm := rd.BlockDeviceMappings[0]
	assert.Equal(t, "/dev/xvda", aws.ToString(bdm.DeviceName))
	require.NotNil(t, bdm.Ebs)
	assert.Equal(t, int32(8), aws.ToInt32(bdm.Ebs.VolumeSize))
	assert.Equal(t, types.VolumeTypeGp3, bdm.Ebs.VolumeType)
	assert.True(t, aws.ToBool(bdm.Ebs.Encrypted))

	require.NotNil(t, rd.MetadataOptions)
	assert.Equal(t, types.LaunchTemplateHttpTokensStateRequired, rd.MetadataOptions.HttpTokens)
	assert.Equal(t, int32(2), aws.ToInt32(rd.MetadataOptions.HttpPutResponseHopLimit))

	require.Len(t, rd.TagSpecifications, 1)
	assert.Equal(t, types.ResourceTypeInstance, rd.TagSpecifications[0].ResourceType)
	require.Len(t, rd.TagSpecifications[0].Tags, 1)
	assert.Equal(t, "Name", aws.ToString(rd.TagSpecifications[0].Tags[0].Key))
	assert.Equal(t, "fck-nat", aws.ToString(rd.TagSpecifications[0].Tags[0].Value))

	// $Default resolves to the same version too.
	def, err := client.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{
		LaunchTemplateId: aws.String(ltID),
		Versions:         []string{"$Default"},
	})
	require.NoError(t, err)
	require.Len(t, def.LaunchTemplateVersions, 1)

	// Unknown template ID is a NotFound, not an empty set.
	_, err = client.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{
		LaunchTemplateId: aws.String("lt-doesnotexist"),
	})
	require.Error(t, err)

	// Delete echoes the deleted template, then it's gone.
	deleted, err := client.DeleteLaunchTemplate(ctx, &ec2.DeleteLaunchTemplateInput{
		LaunchTemplateId: aws.String(ltID),
	})
	require.NoError(t, err)
	assert.Equal(t, ltID, aws.ToString(deleted.LaunchTemplate.LaunchTemplateId))

	_, err = client.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{
		LaunchTemplateId: aws.String(ltID),
	})
	require.Error(t, err, "describe-versions after delete must fail")
}
