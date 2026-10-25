package azure_sdk_test

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// acaJobReplicas is a manual job's replica settings.
type acaJobReplicas struct {
	parallelism, completions, retryLimit, timeout int32
}

// createACAReplicaJob creates a manual Container Apps job in the suite's
// logging environment whose single container runs script under sh, and
// returns its name.
func createACAReplicaJob(t *testing.T, client *armappcontainers.JobsClient, rg, envID string, replicas acaJobReplicas, script string) string {
	t.Helper()
	name := uniquename.New("acarun")
	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armappcontainers.Job{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.JobProperties{
			EnvironmentID: to.Ptr(envID),
			Configuration: &armappcontainers.JobConfiguration{
				TriggerType:       to.Ptr(armappcontainers.TriggerTypeManual),
				ReplicaTimeout:    to.Ptr(replicas.timeout),
				ReplicaRetryLimit: to.Ptr(replicas.retryLimit),
				ManualTriggerConfig: &armappcontainers.JobConfigurationManualTriggerConfig{
					Parallelism:            to.Ptr(replicas.parallelism),
					ReplicaCompletionCount: to.Ptr(replicas.completions),
				},
			},
			Template: &armappcontainers.JobTemplate{
				Containers: []*armappcontainers.Container{{
					Name:    to.Ptr("worker"),
					Image:   to.Ptr("mirror.gcr.io/library/alpine:latest"),
					Command: []*string{to.Ptr("sh"), to.Ptr("-c"), to.Ptr(script)},
				}},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := client.BeginDelete(ctx, rg, name, nil); err == nil {
			_, _ = del.PollUntilDone(ctx, nil)
		}
	})
	return name
}

func startACAReplicaJob(t *testing.T, client *armappcontainers.JobsClient, rg, name string) string {
	t.Helper()
	poller, err := client.BeginStart(ctx, rg, name, nil)
	require.NoError(t, err)
	started, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return *started.Name
}

// acaJobConsoleHosts returns the distinct hosts the job's replicas logged a
// line starting with prefix from, once there are want of them.
func acaJobConsoleHosts(t *testing.T, jobName, prefix string, want int) []string {
	t.Helper()
	kql := fmt.Sprintf(`ContainerAppConsoleLogs_CL | where ContainerGroupName_s == %q`, jobName)
	var hosts []string
	require.Eventually(t, func() bool {
		result := queryWorkspace(t, acaLogsCustomerID(t), kql)
		if len(result.Tables) != 1 {
			return false
		}
		logColumn := requireColumnIndex(t, result.Tables[0], "Log_s")
		seen := map[string]bool{}
		hosts = nil
		for _, row := range result.Tables[0].Rows {
			line, _ := row[logColumn].(string)
			if host, ok := strings.CutPrefix(line, prefix); ok && !seen[host] {
				seen[host] = true
				hosts = append(hosts, host)
			}
		}
		return len(hosts) >= want
	}, 60*time.Second, 200*time.Millisecond, "the job's replicas never logged %d hosts", want)
	return hosts
}

// An execution runs the job's parallelism of replicas, each its own
// container, and succeeds once its replica completion count succeeded.
func TestSDK_ContainerAppsJobs_ExecutionRunsItsParallelismOfReplicas(t *testing.T) {
	t.Parallel()
	rg := "sdk-aca-parallel-rg"
	ensureRG(t, rg)
	client, err := armappcontainers.NewJobsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	name := createACAReplicaJob(t, client, rg, acaLogsEnvironmentID(t), acaJobReplicas{parallelism: 3, completions: 3, timeout: 60},
		`echo "replica $HOSTNAME"`)

	execName := startACAReplicaJob(t, client, rg, name)
	exec := acaWaitExecution(t, rg, name, execName)
	assert.Equal(t, "Succeeded", exec["properties"].(map[string]any)["status"])
	assert.Len(t, acaJobConsoleHosts(t, name, "replica ", 3), 3, "three replicas ran, each its own container")
}

// A failed replica runs again under the job's replicaRetryLimit, and the
// execution fails once its retries are spent.
func TestSDK_ContainerAppsJobs_ReplicaRetriesUpToItsLimit(t *testing.T) {
	t.Parallel()
	rg := "sdk-aca-retry-rg"
	ensureRG(t, rg)
	client, err := armappcontainers.NewJobsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	name := createACAReplicaJob(t, client, rg, acaLogsEnvironmentID(t), acaJobReplicas{parallelism: 1, completions: 1, retryLimit: 2, timeout: 60},
		`echo "attempt $HOSTNAME"; exit 1`)

	execName := startACAReplicaJob(t, client, rg, name)
	exec := acaWaitExecution(t, rg, name, execName)
	assert.Equal(t, "Failed", exec["properties"].(map[string]any)["status"])
	assert.Len(t, acaJobConsoleHosts(t, name, "attempt ", 3), 3, "the replica ran once and was retried twice")
}

// An execution fails once too few replicas can still succeed to reach its
// replica completion count.
func TestSDK_ContainerAppsJobs_ExecutionFailsWhenItsCompletionCountIsOutOfReach(t *testing.T) {
	t.Parallel()
	rg := "sdk-aca-completions-rg"
	ensureRG(t, rg)
	client, err := armappcontainers.NewJobsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	name := createACAReplicaJob(t, client, rg, acaLogsEnvironmentID(t), acaJobReplicas{parallelism: 3, completions: 2, timeout: 60},
		`echo "replica $HOSTNAME"; exit 1`)

	execName := startACAReplicaJob(t, client, rg, name)
	exec := acaWaitExecution(t, rg, name, execName)
	assert.Equal(t, "Failed", exec["properties"].(map[string]any)["status"])
	assert.NotEmpty(t, acaJobConsoleHosts(t, name, "replica ", 2), "the replicas ran before the execution failed")
}

// The replica timeout bounds a replica across its retries: a replica that
// outlives it is stopped and not retried.
func TestSDK_ContainerAppsJobs_ReplicaTimeoutStopsTheReplicaForGood(t *testing.T) {
	t.Parallel()
	rg := "sdk-aca-timeout-rg"
	ensureRG(t, rg)
	client, err := armappcontainers.NewJobsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	name := createACAReplicaJob(t, client, rg, acaLogsEnvironmentID(t), acaJobReplicas{parallelism: 1, completions: 1, retryLimit: 3, timeout: 3},
		`echo "attempt $HOSTNAME"; trap 'echo stopped; exit 143' TERM; sleep 600 & wait $!`)

	execName := startACAReplicaJob(t, client, rg, name)
	exec := acaWaitExecution(t, rg, name, execName)
	props := exec["properties"].(map[string]any)
	assert.Equal(t, "Failed", props["status"])
	start, err := time.Parse(time.RFC3339, props["startTime"].(string))
	require.NoError(t, err)
	end, err := time.Parse(time.RFC3339, props["endTime"].(string))
	require.NoError(t, err)
	assert.Less(t, end.Sub(start), 30*time.Second, "the timeout ended the execution, not the replica's sleep")
	assert.Len(t, acaJobConsoleHosts(t, name, "attempt ", 1), 1, "the timed-out replica was not retried")
}

// A persistent simulator that restarts takes over a running execution's
// replica, so a stop after the restart still stops its container; an
// execution whose replica container is gone after the restart fails, as a
// replica lost with no retries left does, instead of staying Running.
func TestSDK_ContainerAppsJobs_ExecutionsResumeAfterASimulatorRestart(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	port := freeAzureSimPort(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	cmd := startPersistentAzureSimulatorOn(t, "docker", stateDir, port)
	t.Cleanup(func() { _ = shutdownAzureSimulator(cmd) })

	rg := uniquename.New("sdk-aca-job-restart-rg")
	bearer, err := fetchAccessTokenFrom(endpoint, "test-client-id", "test-client-secret", "https://management.azure.com/.default")
	require.NoError(t, err)
	resp := restartRawReq(t, "PUT", endpoint, "/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"?api-version=2023-07-01",
		"", bearer, []byte(`{"location":"eastus"}`), map[string]string{"Content-Type": "application/json"})
	resp.Body.Close()
	require.Contains(t, []int{http.StatusOK, http.StatusCreated}, resp.StatusCode)

	opts := restartClientOpts(endpoint)
	client, err := armappcontainers.NewJobsClient(subscriptionID, &restartCredential{endpoint: endpoint}, opts)
	require.NoError(t, err)
	executions, err := armappcontainers.NewContainerAppsAPIClient(subscriptionID, &restartCredential{endpoint: endpoint}, opts)
	require.NoError(t, err)
	create := func() string {
		name := uniquename.New("acaresume")
		poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armappcontainers.Job{
			Location: to.Ptr("eastus"),
			Properties: &armappcontainers.JobProperties{
				Configuration: &armappcontainers.JobConfiguration{
					TriggerType:       to.Ptr(armappcontainers.TriggerTypeManual),
					ReplicaTimeout:    to.Ptr[int32](600),
					ReplicaRetryLimit: to.Ptr[int32](0),
				},
				Template: &armappcontainers.JobTemplate{Containers: []*armappcontainers.Container{{
					Name:    to.Ptr("worker"),
					Image:   to.Ptr("mirror.gcr.io/library/alpine:latest"),
					Command: []*string{to.Ptr("sh"), to.Ptr("-c"), to.Ptr(`trap 'exit 143' TERM; sleep 600 & wait $!`)},
				}}},
			},
		}, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		return name
	}
	start := func(name string) (string, string) {
		poller, err := client.BeginStart(ctx, rg, name, nil)
		require.NoError(t, err)
		started, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		return *started.Name, *started.ID
	}
	replicaContainers := func(execID string) []string {
		out, err := exec.Command("docker", "ps", "-a", "-q", "--filter", "label=sockerless-exec-id="+execID).Output()
		require.NoError(t, err)
		return strings.Fields(string(out))
	}
	status := func(name, execName string) string {
		got, err := executions.JobExecution(ctx, rg, name, execName, nil)
		require.NoError(t, err)
		return string(*got.Properties.Status)
	}

	adoptedJob, lostJob := create(), create()
	adoptedExec, adoptedID := start(adoptedJob)
	lostExec, lostID := start(lostJob)
	for _, id := range []string{adoptedID, lostID} {
		require.Eventually(t, func() bool { return len(replicaContainers(id)) == 1 },
			60*time.Second, 200*time.Millisecond, "execution %s never placed its replica", id)
	}

	require.NoError(t, shutdownAzureSimulator(cmd))
	removed, err := exec.Command("docker", append([]string{"rm", "-f"}, replicaContainers(lostID)...)...).CombinedOutput()
	require.NoError(t, err, "%s", removed)
	cmd = startPersistentAzureSimulatorOn(t, "docker", stateDir, port)

	assert.Equal(t, "Failed", status(lostJob, lostExec), "an execution whose replica was lost with no retries left fails")
	assert.Equal(t, "Running", status(adoptedJob, adoptedExec), "the restarted simulator runs the execution on")
	require.Len(t, replicaContainers(adoptedID), 1, "the restarted simulator kept the replica's container")

	stopPoller, err := client.BeginStopExecution(ctx, rg, adoptedJob, adoptedExec, nil)
	require.NoError(t, err)
	_, err = stopPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "Stopped", status(adoptedJob, adoptedExec))
	require.Eventually(t, func() bool { return len(replicaContainers(adoptedID)) == 0 },
		60*time.Second, 200*time.Millisecond, "only a process that adopted the replica can stop its container")
}
