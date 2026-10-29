package gcp_sdk_test

import (
	"fmt"
	"os/exec"
	"testing"
	"time"

	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/types/known/durationpb"
)

// A task with direct VPC egress that a restarted simulator adopted keeps its
// subnetwork address, and the next workload on the subnetwork takes another.
// On a /29, Google's reserved addresses leave the two tasks little room to miss
// each other.
func TestSDK_CloudRun_AdoptedVPCEgressKeepsItsAddressAcrossARestart(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	httpPort, grpcPort := freePersistentSimPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	cmd := startPersistentSimulatorOn(t, "docker", stateDir, httpPort, grpcPort)
	t.Cleanup(func() { _ = shutdownPersistentSimulator(cmd) })

	const project, region = "vpc-lease-restart", "us-central1"
	ensureProjectAt(t, endpoint, project)
	tokens := simTokenSourceFor(endpoint)
	computeSvc, err := compute.NewService(ctx, option.WithEndpoint(endpoint+"/compute/v1/"), option.WithTokenSource(tokens))
	require.NoError(t, err)
	createInstanceSubnetwork(t, computeSvc, project, region, "lease-net", "10.239.0.0/29")

	clientOptions := []option.ClientOption{option.WithEndpoint(endpoint), option.WithTokenSource(tokens)}
	jobs, err := run.NewJobsRESTClient(ctx, clientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { jobs.Close() })
	executions, err := run.NewExecutionsRESTClient(ctx, clientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { executions.Close() })

	parent := "projects/" + project + "/locations/" + region
	start := func(name string) string {
		t.Helper()
		created, err := jobs.CreateJob(ctx, &runpb.CreateJobRequest{
			Parent: parent,
			JobId:  name,
			Job: &runpb.Job{Template: &runpb.ExecutionTemplate{Template: &runpb.TaskTemplate{
				Containers: []*runpb.Container{{Image: commandImageName, Args: []string{"hold"}}},
				Timeout:    durationpb.New(10 * time.Minute),
				VpcAccess: &runpb.VpcAccess{
					NetworkInterfaces: []*runpb.VpcAccess_NetworkInterface{{Network: "lease-net", Subnetwork: "lease-net"}},
					Egress:            runpb.VpcAccess_PRIVATE_RANGES_ONLY,
				},
			}}},
		})
		require.NoError(t, err)
		_, err = created.Wait(ctx)
		require.NoError(t, err)
		ran, err := jobs.RunJob(ctx, &runpb.RunJobRequest{Name: parent + "/jobs/" + name})
		require.NoError(t, err)
		execution, err := ran.Metadata()
		require.NoError(t, err)
		t.Cleanup(func() {
			if cancelled, err := executions.CancelExecution(ctx, &runpb.CancelExecutionRequest{Name: execution.Name}); err == nil {
				_, _ = cancelled.Wait(ctx)
			}
		})
		return execution.Name
	}
	// Cloud Run offers nothing to wait on for a running task, so poll the
	// execution's own counts.
	awaitRunning := func(executionName string) {
		t.Helper()
		var last *runpb.Execution
		require.Eventually(t, func() bool {
			got, err := executions.GetExecution(ctx, &runpb.GetExecutionRequest{Name: executionName})
			if err != nil {
				return false
			}
			last = got
			return got.RunningCount == 1 || got.FailedCount > 0
		}, 2*time.Minute, 250*time.Millisecond)
		require.Equal(t, int32(1), last.RunningCount, "the task runs: %+v", last.Conditions)
	}

	first := start("before-restart")
	awaitRunning(first)
	// The execution's counts do not show the adopted container started, so read
	// it off the engine.
	require.Eventually(t, func() bool {
		return exec.Command("docker", "inspect", "--format", "{{.State.Running}}", jobContainerName(first)).Run() == nil
	}, 2*time.Minute, 250*time.Millisecond, "the task's container starts")

	require.NoError(t, shutdownPersistentSimulator(cmd))
	cmd = startPersistentSimulatorOn(t, "docker", stateDir, httpPort, grpcPort)

	awaitRunning(first)
	awaitRunning(start("after-restart"))
}
