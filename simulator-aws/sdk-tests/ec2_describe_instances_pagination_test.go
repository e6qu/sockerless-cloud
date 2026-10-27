package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_DescribeInstancesPagination covers MaxResults and NextToken on
// DescribeInstances; a unique tag isolates the test's instances from the
// shared simulator.
func TestEC2_DescribeInstancesPagination(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	tagValue := uniquename.New("page-test")

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.42.0.0/16")})
	require.NoError(t, err)
	subnet, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.42.1.0/24"),
	})
	require.NoError(t, err)

	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:      aws.String(ec2AmazonLinuxAMI(t)),
		InstanceType: ec2types.InstanceTypeT3Micro,
		MinCount:     aws.Int32(3),
		MaxCount:     aws.Int32(3),
		SubnetId:     subnet.Subnet.SubnetId,
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeInstance,
			Tags:         []ec2types.Tag{{Key: aws.String("page-test"), Value: aws.String(tagValue)}},
		}},
	})
	require.NoError(t, err)
	terminateEC2InstancesOnCleanup(t, c, run)
	// Each instance the request launched carries its position among them.
	indexes := map[int32]bool{}
	for _, inst := range run.Instances {
		indexes[aws.ToInt32(inst.AmiLaunchIndex)] = true
	}
	assert.Equal(t, map[int32]bool{0: true, 1: true, 2: true}, indexes)

	filt := []ec2types.Filter{{Name: aws.String("tag:page-test"), Values: []string{tagValue}}}
	count := func(o *ec2.DescribeInstancesOutput) int {
		n := 0
		for _, r := range o.Reservations {
			n += len(r.Instances)
		}
		return n
	}

	p1, err := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: filt, MaxResults: aws.Int32(2)})
	require.NoError(t, err)
	assert.Equal(t, 2, count(p1), "MaxResults=2 caps the first page")
	require.NotEmpty(t, aws.ToString(p1.NextToken), "more instances remain → NextToken")

	p2, err := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: filt, MaxResults: aws.Int32(2), NextToken: p1.NextToken})
	require.NoError(t, err)
	assert.Equal(t, 1, count(p2), "remaining instance on page 2")
	assert.Empty(t, aws.ToString(p2.NextToken), "last page → no token")
}
