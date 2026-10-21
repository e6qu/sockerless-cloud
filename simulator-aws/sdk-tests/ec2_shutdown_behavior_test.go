package aws_sdk_test

import (
	"encoding/base64"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SIGRTMIN+4 asks systemd, the image's init, to power the machine off.
const ec2PowerOffUserData = "#!/bin/sh\n(sleep 5; kill -38 1) &\n"

func TestEC2_InstanceInitiatedShutdownBehavior(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.231.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.231.1.0/24")

	launch := func(behavior types.ShutdownBehavior) string {
		t.Helper()
		run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
			ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: types.InstanceTypeT3Micro,
			MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
			InstanceInitiatedShutdownBehavior: behavior,
			UserData:                          aws.String(base64.StdEncoding.EncodeToString([]byte(ec2PowerOffUserData))),
		})
		require.NoError(t, err)
		terminateEC2InstancesOnCleanup(t, c, run)
		return aws.ToString(run.Instances[0].InstanceId)
	}
	behaviorOf := func(id string) string {
		t.Helper()
		out, err := c.DescribeInstanceAttribute(ctx, &ec2.DescribeInstanceAttributeInput{
			InstanceId: aws.String(id),
			Attribute:  types.InstanceAttributeNameInstanceInitiatedShutdownBehavior,
		})
		require.NoError(t, err)
		require.NotNil(t, out.InstanceInitiatedShutdownBehavior)
		return aws.ToString(out.InstanceInitiatedShutdownBehavior.Value)
	}

	terminating := launch(types.ShutdownBehaviorTerminate)
	assert.Equal(t, "terminate", behaviorOf(terminating))
	waitForEC2InstanceRunning(t, c, terminating)
	terminated := waitForEC2InstanceTerminated(t, c, terminating).Reservations[0].Instances[0]
	require.NotNil(t, terminated.StateReason)
	assert.Equal(t, "Client.InstanceInitiatedShutdown", aws.ToString(terminated.StateReason.Code))
	assert.Empty(t, terminated.NetworkInterfaces, "a terminated instance gives up its network interface")

	stopping := launch("")
	assert.Equal(t, "stop", behaviorOf(stopping), "an instance launched without the attribute stops")
	waitForEC2InstanceRunning(t, c, stopping)
	stopped := waitForEC2InstanceStopped(t, c, stopping).Reservations[0].Instances[0]
	require.NotNil(t, stopped.StateReason)
	assert.Equal(t, "Client.InstanceInitiatedShutdown", aws.ToString(stopped.StateReason.Code))

	_, err = c.ModifyInstanceAttribute(ctx, &ec2.ModifyInstanceAttributeInput{
		InstanceId:                        aws.String(stopping),
		InstanceInitiatedShutdownBehavior: &types.AttributeValue{Value: aws.String("terminate")},
	})
	require.NoError(t, err)
	assert.Equal(t, "terminate", behaviorOf(stopping))

	_, err = c.ModifyInstanceAttribute(ctx, &ec2.ModifyInstanceAttributeInput{
		InstanceId:                        aws.String(stopping),
		InstanceInitiatedShutdownBehavior: &types.AttributeValue{Value: aws.String("hibernate")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InvalidParameterValue")
}

func TestEC2_RunInstancesRefusesAnAMIWithNoRootFilesystem(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.234.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.234.1.0/24")
	ami := registerImageMgmtAMI(t, c, "mapping-only-ami")
	t.Cleanup(func() { _, _ = c.DeregisterImage(ctx, &ec2.DeregisterImageInput{ImageId: aws.String(ami)}) })

	_, err = c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(ami), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InvalidAMIID.Unavailable")
}
