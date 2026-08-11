package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_DescribeInstancesPagination pins MaxResults/NextToken pagination on
// the list form of DescribeInstances (it previously returned every instance,
// ignoring MaxResults). A unique tag isolates this test's instances from any
// others in the shared sim state.
func TestEC2_DescribeInstancesPagination(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.42.0.0/16")})
	require.NoError(t, err)
	subnet, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.42.1.0/24"),
	})
	require.NoError(t, err)

	_, err = c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:      aws.String("ami-page1234"),
		InstanceType: ec2types.InstanceTypeT3Micro,
		MinCount:     aws.Int32(3),
		MaxCount:     aws.Int32(3),
		SubnetId:     subnet.Subnet.SubnetId,
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeInstance,
			Tags:         []ec2types.Tag{{Key: aws.String("page-test"), Value: aws.String("yes")}},
		}},
	})
	require.NoError(t, err)

	filt := []ec2types.Filter{{Name: aws.String("tag:page-test"), Values: []string{"yes"}}}
	count := func(o *ec2.DescribeInstancesOutput) int {
		n := 0
		for _, r := range o.Reservations {
			n += len(r.Instances)
		}
		return n
	}

	// Page 1: MaxResults=2 → 2 instances + a NextToken.
	p1, err := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: filt, MaxResults: aws.Int32(2)})
	require.NoError(t, err)
	assert.Equal(t, 2, count(p1), "MaxResults=2 caps the first page")
	require.NotEmpty(t, aws.ToString(p1.NextToken), "more instances remain → NextToken")

	// Page 2: resume → the remaining 1, no further token.
	p2, err := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: filt, MaxResults: aws.Int32(2), NextToken: p1.NextToken})
	require.NoError(t, err)
	assert.Equal(t, 1, count(p2), "remaining instance on page 2")
	assert.Empty(t, aws.ToString(p2.NextToken), "last page → no token")
}
