package aws_sdk_test

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A running instance outlives the simulator process that launched it: the
// next process of the same persistent simulator reports it running, reaches
// it through Run Command, and finds what the instance wrote before the
// restart on its disk.
func TestEC2InstanceSurvivesSimulatorRestart_SDK(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	tcpPort, udpPort := persistentSimulatorPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", tcpPort)
	cfg := persistentSDKConfig()
	ec2API := ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.BaseEndpoint = aws.String(endpoint) })
	ssmAPI := ssm.NewFromConfig(cfg, func(o *ssm.Options) { o.BaseEndpoint = aws.String(endpoint) })

	cmd := startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	t.Cleanup(func() { shutdownSimulator(cmd) })

	images, err := ec2API.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{"amazon"},
		Filters: []types.Filter{
			{Name: aws.String("name"), Values: []string{"al2023-ami-2023.*-kernel-6.1-x86_64"}},
			{Name: aws.String("architecture"), Values: []string{"x86_64"}},
			{Name: aws.String("state"), Values: []string{"available"}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, images.Images)
	vpc, err := ec2API.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.216.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, ec2API, vpc.Vpc.VpcId, "10.216.1.0/24")
	run, err := ec2API.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: images.Images[0].ImageId, InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
		UserData: aws.String(base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\necho before-restart > /var/tmp/marker\n"))),
	})
	require.NoError(t, err)
	instanceID := aws.ToString(run.Instances[0].InstanceId)
	t.Cleanup(func() {
		_, _ = ec2API.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{instanceID}})
	})
	waitForEC2InstanceRunning(t, ec2API, instanceID)
	runShell := func(command string) string {
		send, err := ssmAPI.SendCommand(ctx, &ssm.SendCommandInput{
			DocumentName: aws.String("AWS-RunShellScript"),
			InstanceIds:  []string{instanceID},
			Parameters:   map[string][]string{"commands": {command}},
		})
		require.NoError(t, err)
		inv := waitForSSMCommandExecuted(t, ssmAPI, aws.ToString(send.Command.CommandId), instanceID)
		require.Equal(t, "Success", string(inv.Status), "%s: %s", command, aws.ToString(inv.StandardErrorContent))
		return aws.ToString(inv.StandardOutputContent)
	}
	assert.Equal(t, "before-restart\n", runShell("cat /var/tmp/marker"))

	require.NoError(t, shutdownSimulator(cmd))
	cmd = startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")

	described, err := ec2API.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	require.NoError(t, err)
	require.Len(t, described.Reservations, 1)
	assert.Equal(t, types.InstanceStateNameRunning, described.Reservations[0].Instances[0].State.Name,
		"the instance kept running across the restart")
	assert.Equal(t, "before-restart\n", runShell("cat /var/tmp/marker"), "the restarted simulator reaches the same machine")
	runShell("echo after-restart > /var/tmp/marker")
	assert.Equal(t, "after-restart\n", runShell("cat /var/tmp/marker"))
}
