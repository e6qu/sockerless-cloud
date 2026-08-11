package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestELBv2_CapacityReservationOmitsUnsetMinimum verifies an ALB created
// without a minimum-capacity configuration reports no MinimumLoadBalancerCapacity
// from DescribeCapacityReservation. Returning CapacityUnits=0 makes the provider
// read a configured 0 and plan "capacity_units = 0 -> null" every idempotency plan.
func TestELBv2_CapacityReservationOmitsUnsetMinimum(t *testing.T) {
	ec2c := ec2Client()
	vpc, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.73.0.0/16")})
	require.NoError(t, err)
	sub, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.73.1.0/24"),
	})
	require.NoError(t, err)

	c := elbv2Client()
	lb, err := c.CreateLoadBalancer(ctx, &elasticloadbalancingv2.CreateLoadBalancerInput{
		Name:    aws.String("cap-res-lb"),
		Type:    elbv2types.LoadBalancerTypeEnumApplication,
		Subnets: []string{aws.ToString(sub.Subnet.SubnetId)},
	})
	require.NoError(t, err)
	arn := aws.ToString(lb.LoadBalancers[0].LoadBalancerArn)

	out, err := c.DescribeCapacityReservation(ctx, &elasticloadbalancingv2.DescribeCapacityReservationInput{
		LoadBalancerArn: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Nil(t, out.MinimumLoadBalancerCapacity, "no minimum configured => attribute absent")
}
