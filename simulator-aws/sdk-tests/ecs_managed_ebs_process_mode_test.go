package aws_sdk_test

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startProcessModeSim starts a second simulator-aws instance with
// SIM_RUNTIME=process (the API-only mode that never initialises a Docker
// client) on a fresh port and returns its base URL. Reuses the binary
// TestMain already built. Killed on test cleanup.
func startProcessModeSim(t *testing.T) string {
	t.Helper()
	// The coordinates come from freeSimulatorPortPair, which probes TCP and UDP
	// together — the Route 53 listener the simulator binds needs both — and
	// cannot hand back the port the HTTP listener is about to take.
	//
	// A probe only holds a port until it returns it, so another process can
	// still take it in the window before the simulator binds. The simulator
	// then exits on the bind, and waiting on its health endpoint would spend
	// its whole budget reporting a refused connection instead of the bind that
	// actually failed. Watch the process instead: if it exits before it is
	// healthy, start again on fresh coordinates.
	const attempts = 5
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		port, dnsPort, err := freeSimulatorPortPair()
		require.NoError(t, err)

		cmd := exec.Command(binaryPath)
		cmd.Env = append(
			os.Environ(),
			fmt.Sprintf("SIM_LISTEN_ADDR=:%d", port),
			fmt.Sprintf("SIM_DNS_PORT=%d", dnsPort),
			"SIM_RUNTIME=process",
			"SIM_LOG_LEVEL=warn",
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		require.NoError(t, cmd.Start())

		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()

		url := fmt.Sprintf("http://127.0.0.1:%d", port)
		healthy := make(chan error, 1)
		go func() { healthy <- waitForHealth(url + "/health") }()

		select {
		case waitErr := <-exited:
			lastErr = fmt.Errorf("process-mode simulator exited before serving on :%d (Route 53 on :%d): %v", port, dnsPort, waitErr)
			continue
		case healthErr := <-healthy:
			if healthErr != nil {
				_ = cmd.Process.Kill()
				<-exited
				lastErr = healthErr
				continue
			}
		}

		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			<-exited
		})
		return url
	}
	t.Fatalf("process-mode simulator did not start after %d attempts: %v", attempts, lastErr)
	return ""
}

// TestECS_ManagedEBSRunTaskProcessMode covers issue #569: a managed-EBS
// RunTask under SIM_RUNTIME=process must not panic the simulator on the nil
// Docker client. The deleteOnTermination cleanup previously routed through
// ebsRemoveDockerVolume → sim.DockerClient().VolumeRemove, dereferencing the
// nil client in the async task-transition goroutine and crashing the process.
// With the fix the managed-EBS volume is host-path-backed in process mode (the
// same in-memory model ec2:CreateVolume uses), so the transition completes and
// the simulator keeps serving.
func TestECS_ManagedEBSRunTaskProcessMode(t *testing.T) {
	url := startProcessModeSim(t)
	ecsc := ecs.NewFromConfig(sdkConfig(), func(o *ecs.Options) { o.BaseEndpoint = aws.String(url) })
	ec2c := ec2.NewFromConfig(sdkConfig(), func(o *ec2.Options) { o.BaseEndpoint = aws.String(url) })

	vpcOut, err := ec2c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.190.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpcOut.Vpc.VpcId)
	subnetOut, err := ec2c.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:     aws.String(vpcID),
		CidrBlock: aws.String("10.190.1.0/24"),
	})
	require.NoError(t, err)
	subnetID := aws.ToString(subnetOut.Subnet.SubnetId)

	clusterName := "managed-ebs-process"
	_, err = ecsc.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)

	tdOut, err := ecsc.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("managed-ebs-process"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		Volumes: []ecstypes.Volume{{
			Name:               aws.String("workspace"),
			ConfiguredAtLaunch: aws.Bool(true),
		}},
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:  aws.String("app"),
			Image: aws.String("public.ecr.aws/docker/library/alpine:3.20"),
			MountPoints: []ecstypes.MountPoint{{
				SourceVolume:  aws.String("workspace"),
				ContainerPath: aws.String("/workspace"),
			}},
		}},
	})
	require.NoError(t, err)

	runOut, err := ecsc.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(clusterName),
		TaskDefinition: tdOut.TaskDefinition.TaskDefinitionArn,
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
		VolumeConfigurations: []ecstypes.TaskVolumeConfiguration{{
			Name: aws.String("workspace"),
			ManagedEBSVolume: &ecstypes.TaskManagedEBSVolumeConfiguration{
				RoleArn:    aws.String("arn:aws:iam::123456789012:role/ecsInfrastructureRole"),
				SizeInGiB:  aws.Int32(1),
				VolumeType: aws.String("gp3"),
				// deleteOnTermination=true exercises the cleanup path that
				// dereferenced the nil Docker client and crashed the sim.
				TerminationPolicy: &ecstypes.TaskManagedEBSVolumeTerminationPolicy{
					DeleteOnTermination: aws.Bool(true),
				},
			},
		}},
	})
	require.NoError(t, err)
	require.Len(t, runOut.Tasks, 1)
	taskArn := aws.ToString(runOut.Tasks[0].TaskArn)
	require.NotEmpty(t, taskArn)

	// The async transition runs the deleteOnTermination volume cleanup (the
	// crash site). Poll the simulator for a few seconds: if it had panicked
	// the connection would be refused. Surviving requests prove the fix.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		_, derr := ecsc.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(clusterName),
			Tasks:   []string{taskArn},
		})
		require.NoError(t, derr, "simulator must keep serving through the managed-EBS task transition (issue #569)")
		time.Sleep(500 * time.Millisecond)
	}

	// Final liveness proof on an unrelated control-plane call.
	_, err = ecsc.ListClusters(ctx, &ecs.ListClustersInput{})
	require.NoError(t, err, "simulator must still be alive after a process-mode managed-EBS RunTask")
	assert.Contains(t, taskArn, "task/")
}
