package aws_sdk_test

import (
	"sort"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_RunInstancesClientTokenIdempotency pins that a RunInstances retried
// with the same ClientToken replays the original reservation instead of
// launching a duplicate batch. The aws-sdk-go-v2 auto-fills ClientToken (Smithy
// idempotencyToken trait) and re-sends it on every retry, so without server-side
// dedup a transient retry doubles the instance count.
func TestEC2_RunInstancesClientTokenIdempotency(t *testing.T) {
	c := ec2Client()

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.43.0.0/16")})
	require.NoError(t, err)
	subnet, err := c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.43.1.0/24"),
	})
	require.NoError(t, err)

	in := &ec2.RunInstancesInput{
		ImageId:      aws.String("ami-idem1234"),
		InstanceType: ec2types.InstanceTypeT3Micro,
		MinCount:     aws.Int32(2),
		MaxCount:     aws.Int32(2),
		SubnetId:     subnet.Subnet.SubnetId,
		ClientToken:  aws.String("fixed-idempotency-token-abc"),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeInstance,
			Tags:         []ec2types.Tag{{Key: aws.String("idem-test"), Value: aws.String("yes")}},
		}},
	}

	ids := func(o *ec2.RunInstancesOutput) []string {
		out := make([]string, 0, len(o.Instances))
		for _, inst := range o.Instances {
			out = append(out, aws.ToString(inst.InstanceId))
		}
		sort.Strings(out)
		return out
	}

	first, err := c.RunInstances(ctx, in)
	require.NoError(t, err)
	firstIDs := ids(first)
	require.Len(t, firstIDs, 2)

	// Re-issue with the same ClientToken — the retry path a loaded client takes.
	second, err := c.RunInstances(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, firstIDs, ids(second), "same ClientToken replays the original reservation")
	assert.Equal(t, aws.ToString(first.ReservationId), aws.ToString(second.ReservationId), "same reservation id")

	// And no duplicate instances exist under the tag.
	desc, err := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{{Name: aws.String("tag:idem-test"), Values: []string{"yes"}}},
	})
	require.NoError(t, err)
	n := 0
	for _, res := range desc.Reservations {
		n += len(res.Instances)
	}
	assert.Equal(t, 2, n, "retried RunInstances created no extra instances")
}
