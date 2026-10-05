package gcp_sdk_test

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	runv2 "google.golang.org/api/run/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// countLogMessages reports how many of messages are exactly line.
func countLogMessages(messages []string, line string) int {
	n := 0
	for _, m := range messages {
		if strings.TrimSpace(m) == line {
			n++
		}
	}
	return n
}

// A worker pool's create operation completes only once its instances have
// started and passed their startup probes. An instance that cannot start fails
// the operation with the start error, and the pool reports the failure on its
// Ready condition and keeps no ready revision.
func TestSDK_CloudRun_WorkerPoolDeployWaitsForItsInstances(t *testing.T) {
	pools := newWorkerPoolsClient(t)
	parent := "projects/test-project/locations/us-central1"
	count := int32(1)

	missingID := uniqueName("sdk-wp-missing-image")
	op, err := pools.CreateWorkerPool(ctx, &runpb.CreateWorkerPoolRequest{
		Parent:       parent,
		WorkerPoolId: missingID,
		WorkerPool: &runpb.WorkerPool{
			Scaling: &runpb.WorkerPoolScaling{ManualInstanceCount: &count},
			Template: &runpb.WorkerPoolRevisionTemplate{
				Containers: []*runpb.Container{{Image: "gcr.io/test-project/" + missingID}},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := pools.DeleteWorkerPool(ctx, &runpb.DeleteWorkerPoolRequest{Name: parent + "/workerPools/" + missingID}); err == nil {
			_, _ = del.Wait(ctx)
		}
	})
	_, err = op.Wait(ctx)
	require.Error(t, err, "a pool whose instance cannot pull its image does not deploy")
	assert.Contains(t, err.Error(), "gcr.io/test-project/"+missingID)

	failed, err := pools.GetWorkerPool(ctx, &runpb.GetWorkerPoolRequest{Name: parent + "/workerPools/" + missingID})
	require.NoError(t, err)
	assert.False(t, failed.Reconciling)
	require.NotNil(t, failed.TerminalCondition)
	assert.Equal(t, runpb.Condition_CONDITION_FAILED, failed.TerminalCondition.State)
	assert.Contains(t, failed.TerminalCondition.Message, "is not ready and cannot serve traffic")
	assert.Empty(t, failed.LatestReadyRevision, "a pool that never became ready has no ready revision")

	probedID := uniqueName("sdk-wp-probe-fails")
	op, err = pools.CreateWorkerPool(ctx, &runpb.CreateWorkerPoolRequest{
		Parent:       parent,
		WorkerPoolId: probedID,
		WorkerPool: &runpb.WorkerPool{
			Scaling: &runpb.WorkerPoolScaling{ManualInstanceCount: &count},
			Template: &runpb.WorkerPoolRevisionTemplate{
				Containers: []*runpb.Container{{
					Image: commandImageName,
					Args:  []string{"hold"},
					StartupProbe: &runpb.Probe{
						PeriodSeconds:    1,
						TimeoutSeconds:   1,
						FailureThreshold: 1,
						ProbeType:        &runpb.Probe_TcpSocket{TcpSocket: &runpb.TCPSocketAction{Port: 8080}},
					},
				}},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := pools.DeleteWorkerPool(ctx, &runpb.DeleteWorkerPoolRequest{Name: parent + "/workerPools/" + probedID}); err == nil {
			_, _ = del.Wait(ctx)
		}
	})
	_, err = op.Wait(ctx)
	require.Error(t, err, "a pool whose instance fails its startup probe does not deploy")
	assert.Contains(t, err.Error(), "startup probe")

	readyID := uniqueName("sdk-wp-ready")
	op, err = pools.CreateWorkerPool(ctx, &runpb.CreateWorkerPoolRequest{
		Parent:       parent,
		WorkerPoolId: readyID,
		WorkerPool: &runpb.WorkerPool{
			Scaling: &runpb.WorkerPoolScaling{ManualInstanceCount: &count},
			Template: &runpb.WorkerPoolRevisionTemplate{
				Containers: []*runpb.Container{{
					Image: commandImageName,
					Args:  []string{"http", "8080", "ready", "2"},
					StartupProbe: &runpb.Probe{
						PeriodSeconds:    1,
						TimeoutSeconds:   1,
						FailureThreshold: 10,
						ProbeType:        &runpb.Probe_TcpSocket{TcpSocket: &runpb.TCPSocketAction{Port: 8080}},
					},
				}},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := pools.DeleteWorkerPool(ctx, &runpb.DeleteWorkerPoolRequest{Name: parent + "/workerPools/" + readyID}); err == nil {
			_, _ = del.Wait(ctx)
		}
	})
	creating, err := pools.GetWorkerPool(ctx, &runpb.GetWorkerPoolRequest{Name: parent + "/workerPools/" + readyID})
	require.NoError(t, err)
	assert.True(t, creating.Reconciling, "the pool reconciles while its instance waits out the listener's delay")
	assert.Equal(t, runpb.Condition_CONDITION_RECONCILING, creating.TerminalCondition.GetState())

	ready, err := op.Wait(ctx)
	require.NoError(t, err)
	assert.False(t, ready.Reconciling)
	assert.Equal(t, runpb.Condition_CONDITION_SUCCEEDED, ready.TerminalCondition.GetState())
	assert.Equal(t, parent+"/workerPools/"+readyID+"/revisions/"+readyID+"-00001-abc", ready.LatestReadyRevision)
	assert.Equal(t, ready.Generation, ready.ObservedGeneration)
}

// Cancelling a worker pool's unfinished deploy stops the instances it was
// starting: the operation ends CANCELLED and the pool's Ready condition fails
// with the reason gcloud's cancellation poller reads.
func TestSDK_CloudRun_WorkerPoolDeployCancel(t *testing.T) {
	pools := newWorkerPoolsClient(t)
	parent := "projects/test-project/locations/us-central1"
	count := int32(1)
	id := uniqueName("sdk-wp-cancel")
	op, err := pools.CreateWorkerPool(ctx, &runpb.CreateWorkerPoolRequest{
		Parent:       parent,
		WorkerPoolId: id,
		WorkerPool: &runpb.WorkerPool{
			Scaling: &runpb.WorkerPoolScaling{ManualInstanceCount: &count},
			Template: &runpb.WorkerPoolRevisionTemplate{
				Containers: []*runpb.Container{{
					Image: commandImageName,
					Args:  []string{"http", "8080", "late", "3600"},
					StartupProbe: &runpb.Probe{
						PeriodSeconds:    240,
						TimeoutSeconds:   1,
						FailureThreshold: 15,
						ProbeType:        &runpb.Probe_TcpSocket{TcpSocket: &runpb.TCPSocketAction{Port: 8080}},
					},
				}},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := pools.DeleteWorkerPool(ctx, &runpb.DeleteWorkerPoolRequest{Name: parent + "/workerPools/" + id}); err == nil {
			_, _ = del.Wait(ctx)
		}
	})

	code, body := gcpDataPlaneRequest(t, http.MethodPost, "", "/v2/"+op.Name()+":cancel", "{}")
	require.Equal(t, http.StatusOK, code, body)
	_, err = op.Wait(ctx)
	require.Error(t, err)
	assert.Equal(t, codes.Canceled, status.Code(err))

	// The Go client's Condition reason enum carries no "Cancelled"; the
	// Discovery client reads the string gcloud compares.
	pool, err := newRunV2RESTService(t).Projects.Locations.WorkerPools.Get(parent + "/workerPools/" + id).Do()
	require.NoError(t, err)
	assert.False(t, pool.Reconciling)
	require.NotNil(t, pool.TerminalCondition)
	assert.Equal(t, "CONDITION_FAILED", pool.TerminalCondition.State)
	assert.Equal(t, "Cancelled", pool.TerminalCondition.Reason)
}

// instanceExitScript serves the default startup probe on $PORT, logs line,
// and exits with code once the probe has had a second to pass.
func instanceExitScript(line string, code int) []string {
	return []string{"sh", "-c", fmt.Sprintf(`nc -lk -p "$PORT" -e true &
echo %q
sleep 2
exit %d`, line, code)}
}

// An instance whose container exits is restarted as its restart policy says.
// ON_FAILURE, the default, restarts it after a non-zero exit, up to three
// times in a row; the fourth exit fails it. NEVER leaves it stopped after a
// clean exit.
func TestSDK_CloudRun_InstanceRestartPolicy(t *testing.T) {
	instances := newInstancesClient(t)
	parent := "projects/test-project/locations/us-central1"

	failingID := uniqueName("sdk-inst-on-failure")
	op, err := instances.CreateInstance(ctx, &runpb.CreateInstanceRequest{
		Parent:     parent,
		InstanceId: failingID,
		Instance: &runpb.Instance{
			Containers: []*runpb.Container{{Image: simWorkloadImage, Command: instanceExitScript("crashing instance started", 3)}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := instances.DeleteInstance(ctx, &runpb.DeleteInstanceRequest{Name: parent + "/instances/" + failingID}); err == nil {
			_, _ = del.Wait(ctx)
		}
	})
	_, err = op.Wait(ctx)
	require.NoError(t, err, "the instance started before its container exited")

	filter := `resource.type="cloud_run_instance" AND resource.labels.instance_name="` + failingID + `"`
	messages := followLogMessages(t, filter, func(messages []string) bool {
		return countLogMessages(messages, "Container called exit(3).") == 4
	})
	assert.Equal(t, 4, countLogMessages(messages, "crashing instance started"), "the instance ran once and was restarted three times")

	failed, err := instances.GetInstance(ctx, &runpb.GetInstanceRequest{Name: parent + "/instances/" + failingID})
	require.NoError(t, err)
	require.NotNil(t, failed.TerminalCondition)
	assert.Equal(t, runpb.Condition_CONDITION_FAILED, failed.TerminalCondition.State)
	assert.Contains(t, failed.TerminalCondition.Message, "restarted it 3 times")

	// The Go client's Instance message carries no restartPolicy; the
	// Discovery client sends it.
	rest := newRunV2RESTService(t)
	neverID := uniqueName("sdk-inst-never")
	restOp, err := rest.Projects.Locations.Instances.Create(parent, &runv2.GoogleCloudRunV2Instance{
		RestartPolicy: "NEVER",
		Containers: []*runv2.GoogleCloudRunV2Container{{
			Image: simWorkloadImage, Command: instanceExitScript("one-shot instance started", 0),
		}},
	}).InstanceId(neverID).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		if del, err := rest.Projects.Locations.Instances.Delete(parent + "/instances/" + neverID).Do(); err == nil {
			waitRunV2Operation(t, rest, del)
		}
	})
	awaitRunV2Operation(t, rest, restOp)
	followLogMessages(t, `resource.type="cloud_run_instance" AND resource.labels.instance_name="`+neverID+`"`,
		func(messages []string) bool { return countLogMessages(messages, "Container called exit(0).") == 1 })

	stopped, err := rest.Projects.Locations.Instances.Get(parent + "/instances/" + neverID).Do()
	require.NoError(t, err)
	require.NotNil(t, stopped.TerminalCondition)
	assert.Equal(t, "CONDITION_PENDING", stopped.TerminalCondition.State)
	assert.Equal(t, "Stopped", stopped.TerminalCondition.Reason)
}

// A simulator restarted on the same state directory keeps running the
// instances of the worker pools and Cloud Run instances it stored, as Cloud
// Run's control plane never restarts the instances it runs: the containers the
// earlier process started go on serving, under the new process's management,
// and none of their output reaches Cloud Logging twice.
func TestSDK_CloudRun_WorkloadsResumeAfterSimulatorRestart(t *testing.T) {
	stateDir := t.TempDir()
	httpPort, grpcPort := freePersistentSimPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	logsAt := fmt.Sprintf("127.0.0.1:%d", grpcPort)

	cmd := startPersistentSimulatorOn(t, "docker", stateDir, httpPort, grpcPort)
	t.Cleanup(func() { _ = shutdownPersistentSimulator(cmd) })

	clientOptions := []option.ClientOption{option.WithEndpoint(endpoint), option.WithTokenSource(simTokenSourceFor(endpoint))}
	pools, err := run.NewWorkerPoolsRESTClient(ctx, clientOptions...)
	require.NoError(t, err)
	defer pools.Close()
	instances, err := run.NewInstancesRESTClient(ctx, clientOptions...)
	require.NoError(t, err)
	defer instances.Close()

	parent := "projects/test-project/locations/us-central1"
	count := int32(1)
	poolOp, err := pools.CreateWorkerPool(ctx, &runpb.CreateWorkerPoolRequest{
		Parent:       parent,
		WorkerPoolId: "resumed-pool",
		WorkerPool: &runpb.WorkerPool{
			Scaling: &runpb.WorkerPoolScaling{ManualInstanceCount: &count},
			Template: &runpb.WorkerPoolRevisionTemplate{
				Containers: []*runpb.Container{{Image: simWorkloadImage, Command: []string{"sh", "-c",
					`trap 'echo "stopping pool worker $HOSTNAME"; exit 0' TERM; echo "pool worker $HOSTNAME"; sleep 2147483647 & wait $!`}}},
			},
		},
	})
	require.NoError(t, err)
	_, err = poolOp.Wait(ctx)
	require.NoError(t, err)

	instOp, err := instances.CreateInstance(ctx, &runpb.CreateInstanceRequest{
		Parent:     parent,
		InstanceId: "resumed-instance",
		Instance: &runpb.Instance{
			InvokerIamDisabled: true,
			Containers: []*runpb.Container{{Image: simWorkloadImage, Command: []string{"sh", "-c",
				`trap 'echo "stopping instance $HOSTNAME"; exit 0' TERM; ` +
					`nc -lk -p "$PORT" -e sh -c 'printf "HTTP/1.0 200 OK\r\nContent-Length: ${#HOSTNAME}\r\n\r\n%s" "$HOSTNAME"' & ` +
					`echo "instance $HOSTNAME"; sleep 2147483647 & wait $!`}}},
		},
	})
	require.NoError(t, err)
	inst, err := instOp.Wait(ctx)
	require.NoError(t, err)
	require.Len(t, inst.Urls, 1)

	poolLogs := `resource.type="cloud_run_worker_pool" AND resource.labels.worker_pool_name="resumed-pool"`
	instanceLogs := `resource.type="cloud_run_instance" AND resource.labels.instance_name="resumed-instance"`
	host := func(filter, prefix string) string {
		var hosts []string
		followLogMessagesAt(t, logsAt, filter, func(messages []string) bool {
			hosts = distinctLogHosts(messages, prefix)
			return len(hosts) >= 1
		})
		require.Len(t, hosts, 1)
		return hosts[0]
	}
	poolHost := host(poolLogs, "pool worker")
	instanceHost := host(instanceLogs, "instance")
	invoke := func() string {
		u, err := url.Parse(inst.Urls[0])
		require.NoError(t, err)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/", nil)
		require.NoError(t, err)
		req.Host = u.Host
		resp, err := rawClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "body=%q", body)
		return string(body)
	}
	assert.Equal(t, instanceHost, invoke(), "the instance's URL reaches its container")

	require.NoError(t, shutdownPersistentSimulator(cmd))
	cmd = startPersistentSimulatorOn(t, "docker", stateDir, httpPort, grpcPort)

	assert.Equal(t, instanceHost, invoke(), "the restarted simulator serves the instance on the container it adopted")
	pool, err := pools.GetWorkerPool(ctx, &runpb.GetWorkerPoolRequest{Name: parent + "/workerPools/resumed-pool"})
	require.NoError(t, err)
	assert.Equal(t, runpb.Condition_CONDITION_SUCCEEDED, pool.TerminalCondition.GetState())

	poolDeleted, err := pools.DeleteWorkerPool(ctx, &runpb.DeleteWorkerPoolRequest{Name: parent + "/workerPools/resumed-pool"})
	require.NoError(t, err)
	_, err = poolDeleted.Wait(ctx)
	require.NoError(t, err)
	instanceDeleted, err := instances.DeleteInstance(ctx, &runpb.DeleteInstanceRequest{Name: parent + "/instances/resumed-instance"})
	require.NoError(t, err)
	_, err = instanceDeleted.Wait(ctx)
	require.NoError(t, err)

	// The deletions stop the containers the earlier process started, which only
	// a process that adopted them can do; their start lines stay logged once.
	poolMessages := followLogMessagesAt(t, logsAt, poolLogs, func(messages []string) bool {
		return countLogMessages(messages, "stopping pool worker "+poolHost) == 1
	})
	assert.Equal(t, []string{poolHost}, distinctLogHosts(poolMessages, "pool worker"), "the pool started no other instance")
	assert.Equal(t, 1, countLogMessages(poolMessages, "pool worker "+poolHost))
	instanceMessages := followLogMessagesAt(t, logsAt, instanceLogs, func(messages []string) bool {
		return countLogMessages(messages, "stopping instance "+instanceHost) == 1
	})
	assert.Equal(t, []string{instanceHost}, distinctLogHosts(instanceMessages, "instance"), "the instance started no other container")
	assert.Equal(t, 1, countLogMessages(instanceMessages, "instance "+instanceHost))
}
