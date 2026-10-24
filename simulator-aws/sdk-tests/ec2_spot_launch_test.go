package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunInstances with a spot market launches a Spot Instance: DescribeInstances
// marks it spot and names the one-time request it fulfils, which
// DescribeSpotInstanceRequests reports active and fulfilled, and the
// instance reads its life cycle from the instance metadata service.
func TestEC2_RunInstancesLaunchesASpotInstance(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.226.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.226.1.0/24")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
		IamInstanceProfile: ssmManagedProfileSpec(t),
		InstanceMarketOptions: &types.InstanceMarketOptionsRequest{
			MarketType:  types.MarketTypeSpot,
			SpotOptions: &types.SpotMarketOptions{MaxPrice: aws.String("0.0104")},
		},
	})
	require.NoError(t, err)
	terminateEC2InstancesOnCleanup(t, c, run)
	instanceID := aws.ToString(run.Instances[0].InstanceId)
	assert.Equal(t, types.InstanceLifecycleTypeSpot, run.Instances[0].InstanceLifecycle)

	described, err := c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	inst := described.Reservations[0].Instances[0]
	assert.Equal(t, types.InstanceLifecycleTypeSpot, inst.InstanceLifecycle)
	requestID := aws.ToString(inst.SpotInstanceRequestId)
	require.NotEmpty(t, requestID)

	requests, err := c.DescribeSpotInstanceRequests(ctx, &ec2.DescribeSpotInstanceRequestsInput{SpotInstanceRequestIds: []string{requestID}})
	require.NoError(t, err)
	require.Len(t, requests.SpotInstanceRequests, 1)
	request := requests.SpotInstanceRequests[0]
	assert.Equal(t, types.SpotInstanceStateActive, request.State)
	assert.Equal(t, "fulfilled", aws.ToString(request.Status.Code))
	assert.Equal(t, instanceID, aws.ToString(request.InstanceId))
	assert.Equal(t, types.SpotInstanceTypeOneTime, request.Type)
	assert.Equal(t, "0.0104", aws.ToString(request.SpotPrice))

	waitForEC2InstanceRunning(t, c, instanceID)
	lifecycle := ssmShell(t, ssmClient(), instanceID, `token=$(curl -sS -X PUT "${AWS_EC2_METADATA_SERVICE_ENDPOINT}latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 60")
curl -sS -H "X-aws-ec2-metadata-token: $token" "${AWS_EC2_METADATA_SERVICE_ENDPOINT%/}/latest/meta-data/instance-life-cycle"`)
	assert.Equal(t, "spot", lifecycle)
}
