package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_LaunchTemplateVersions covers the in-place update path:
// CreateLaunchTemplateVersion appends a version (latest, not
// default) and ModifyLaunchTemplate moves the default version.
func TestEC2_LaunchTemplateVersions(t *testing.T) {
	client := ec2Client()

	created, err := client.CreateLaunchTemplate(ctx, &ec2.CreateLaunchTemplateInput{
		LaunchTemplateName: aws.String("lt-versions"),
		LaunchTemplateData: &types.RequestLaunchTemplateData{
			ImageId:      aws.String("ami-11111111"),
			InstanceType: types.InstanceTypeT3Micro,
		},
	})
	require.NoError(t, err)
	ltID := aws.ToString(created.LaunchTemplate.LaunchTemplateId)

	v2, err := client.CreateLaunchTemplateVersion(ctx, &ec2.CreateLaunchTemplateVersionInput{
		LaunchTemplateId:   aws.String(ltID),
		VersionDescription: aws.String("v2"),
		LaunchTemplateData: &types.RequestLaunchTemplateData{
			ImageId:      aws.String("ami-22222222"),
			InstanceType: types.InstanceTypeT3Small,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, v2.LaunchTemplateVersion)
	assert.Equal(t, int64(2), aws.ToInt64(v2.LaunchTemplateVersion.VersionNumber))
	assert.False(t, aws.ToBool(v2.LaunchTemplateVersion.DefaultVersion), "a new version is not the default")
	assert.Equal(t, "ami-22222222", aws.ToString(v2.LaunchTemplateVersion.LaunchTemplateData.ImageId))

	// Latest advanced to 2; default still 1.
	desc, err := client.DescribeLaunchTemplates(ctx, &ec2.DescribeLaunchTemplatesInput{LaunchTemplateIds: []string{ltID}})
	require.NoError(t, err)
	require.Len(t, desc.LaunchTemplates, 1)
	assert.Equal(t, int64(2), aws.ToInt64(desc.LaunchTemplates[0].LatestVersionNumber))
	assert.Equal(t, int64(1), aws.ToInt64(desc.LaunchTemplates[0].DefaultVersionNumber))

	// Move the default to version 2.
	_, err = client.ModifyLaunchTemplate(ctx, &ec2.ModifyLaunchTemplateInput{
		LaunchTemplateId: aws.String(ltID),
		DefaultVersion:   aws.String("2"),
	})
	require.NoError(t, err)

	desc2, err := client.DescribeLaunchTemplates(ctx, &ec2.DescribeLaunchTemplatesInput{LaunchTemplateIds: []string{ltID}})
	require.NoError(t, err)
	assert.Equal(t, int64(2), aws.ToInt64(desc2.LaunchTemplates[0].DefaultVersionNumber))

	// $Default now resolves to version 2 with the v2 data.
	dv, err := client.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{
		LaunchTemplateId: aws.String(ltID),
		Versions:         []string{"$Default"},
	})
	require.NoError(t, err)
	require.Len(t, dv.LaunchTemplateVersions, 1)
	assert.Equal(t, int64(2), aws.ToInt64(dv.LaunchTemplateVersions[0].VersionNumber))
	assert.True(t, aws.ToBool(dv.LaunchTemplateVersions[0].DefaultVersion))
	assert.Equal(t, "ami-22222222", aws.ToString(dv.LaunchTemplateVersions[0].LaunchTemplateData.ImageId))

	// A bad version selector is an error.
	_, err = client.ModifyLaunchTemplate(ctx, &ec2.ModifyLaunchTemplateInput{
		LaunchTemplateId: aws.String(ltID),
		DefaultVersion:   aws.String("99"),
	})
	require.Error(t, err)
}
