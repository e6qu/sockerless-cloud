package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_DescribeInstanceTypeOfferings covers the fck-nat
// pre-flight that validates the chosen instance type is offered in the target
// availability zones.
func TestEC2_DescribeInstanceTypeOfferings(t *testing.T) {
	client := ec2Client()

	out, err := client.DescribeInstanceTypeOfferings(ctx, &ec2.DescribeInstanceTypeOfferingsInput{
		LocationType: ec2types.LocationTypeAvailabilityZone,
		Filters: []ec2types.Filter{
			{Name: aws.String("instance-type"), Values: []string{"t4g.nano"}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.InstanceTypeOfferings)
	off := out.InstanceTypeOfferings[0]
	assert.Equal(t, ec2types.InstanceTypeT4gNano, off.InstanceType)
	assert.Equal(t, ec2types.LocationTypeAvailabilityZone, off.LocationType)
	assert.NotEmpty(t, aws.ToString(off.Location))

	// A specific location filter echoes that location.
	loc, err := client.DescribeInstanceTypeOfferings(ctx, &ec2.DescribeInstanceTypeOfferingsInput{
		LocationType: ec2types.LocationTypeAvailabilityZone,
		Filters: []ec2types.Filter{
			{Name: aws.String("instance-type"), Values: []string{"t4g.nano"}},
			{Name: aws.String("location"), Values: []string{"us-east-1a"}},
		},
	})
	require.NoError(t, err)
	require.Len(t, loc.InstanceTypeOfferings, 1)
	assert.Equal(t, "us-east-1a", aws.ToString(loc.InstanceTypeOfferings[0].Location))
}
