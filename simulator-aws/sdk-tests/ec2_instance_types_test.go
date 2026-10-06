package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_DescribeInstanceTypesReportsPublishedFacts reads instance types'
// vCPUs, memory, architecture and network performance as AWS publishes them,
// refuses an instance type that does not exist, filters over the facts, and
// pages through the whole catalog.
func TestEC2_DescribeInstanceTypesReportsPublishedFacts(t *testing.T) {
	c := ec2Client()
	out, err := c.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{
		InstanceTypes: []types.InstanceType{types.InstanceTypeM5Xlarge, types.InstanceTypeT4gNano, types.InstanceTypeT1Micro},
	})
	require.NoError(t, err)
	byName := map[types.InstanceType]types.InstanceTypeInfo{}
	for _, info := range out.InstanceTypes {
		byName[info.InstanceType] = info
	}
	require.Len(t, byName, 3)
	m5 := byName[types.InstanceTypeM5Xlarge]
	assert.Equal(t, int32(4), aws.ToInt32(m5.VCpuInfo.DefaultVCpus))
	assert.Equal(t, int64(16384), aws.ToInt64(m5.MemoryInfo.SizeInMiB))
	assert.Equal(t, []types.ArchitectureType{types.ArchitectureTypeX8664}, m5.ProcessorInfo.SupportedArchitectures)
	assert.Equal(t, "Up to 10 Gigabit", aws.ToString(m5.NetworkInfo.NetworkPerformance))
	assert.True(t, aws.ToBool(m5.CurrentGeneration))
	assert.False(t, aws.ToBool(m5.InstanceStorageSupported))
	graviton := byName[types.InstanceTypeT4gNano]
	assert.Equal(t, int32(2), aws.ToInt32(graviton.VCpuInfo.DefaultVCpus))
	assert.Equal(t, int64(512), aws.ToInt64(graviton.MemoryInfo.SizeInMiB))
	assert.Equal(t, []types.ArchitectureType{types.ArchitectureTypeArm64}, graviton.ProcessorInfo.SupportedArchitectures)
	legacy := byName[types.InstanceTypeT1Micro]
	assert.Equal(t, int64(627), aws.ToInt64(legacy.MemoryInfo.SizeInMiB), "0.613 GiB")
	assert.Equal(t, []types.ArchitectureType{types.ArchitectureTypeI386, types.ArchitectureTypeX8664}, legacy.ProcessorInfo.SupportedArchitectures)
	assert.False(t, aws.ToBool(legacy.CurrentGeneration))

	_, err = c.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{InstanceTypes: []types.InstanceType{"t9.huge"}})
	assert.Equal(t, "InvalidInstanceType", errCode(t, err))

	filtered, err := c.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{Filters: []types.Filter{
		{Name: aws.String("instance-type"), Values: []string{"m5.*"}},
		{Name: aws.String("vcpu-info.default-vcpus"), Values: []string{"96"}},
	}})
	require.NoError(t, err)
	var names []string
	for _, info := range filtered.InstanceTypes {
		names = append(names, string(info.InstanceType))
	}
	assert.ElementsMatch(t, []string{"m5.24xlarge", "m5.metal"}, names)

	pages, total := 0, 0
	seen := map[types.InstanceType]bool{}
	paginator := ec2.NewDescribeInstanceTypesPaginator(c, &ec2.DescribeInstanceTypesInput{MaxResults: aws.Int32(100)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.InstanceTypes), 100)
		pages++
		for _, info := range page.InstanceTypes {
			require.False(t, seen[info.InstanceType], "instance type %s on two pages", info.InstanceType)
			seen[info.InstanceType] = true
			total++
		}
	}
	assert.Greater(t, pages, 1, "the catalog spans several pages")
	assert.True(t, seen[types.InstanceTypeM5Xlarge] && seen[types.InstanceTypeT4gNano])
	assert.Greater(t, total, 500)
}
