package gcp_cli_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bucketObjectsWithPrefix(t *testing.T, bucket, prefix string) []string {
	t.Helper()
	listed := runCLI(t, gcloudCLI("storage", "objects", "list", "gs://"+bucket+"/**", "--format=value(name)"))
	var names []string
	for _, name := range strings.Fields(listed) {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	return names
}

// cloudRunWorkloadScriptJSON is a container command, as a JSON array, that
// records the container's start in the Cloud Storage volume at /mnt/bucket,
// logs line and its hostname, and records its stop on the stop signal. listen
// keeps a TCP listener on $PORT for an instance's default startup probe.
func cloudRunWorkloadScriptJSON(line string, listen bool) string {
	lines := []string{
		"trap 'echo stopped > /mnt/bucket/stopped-$HOSTNAME; exit 0' TERM",
		"echo started > /mnt/bucket/started-$HOSTNAME",
	}
	if listen {
		lines = append(lines, `nc -lk -p "$PORT" -e true &`)
	}
	lines = append(lines, `echo "`+line+` $HOSTNAME"`, "sleep 2147483647 &", "wait $!")
	command, err := json.Marshal([]string{"sh", "-c", strings.Join(lines, "\n")})
	if err != nil {
		panic(err)
	}
	return string(command)
}

// startedHosts returns the hostnames that `logs read` output shows logging
// prefix, each once.
func startedHosts(out, prefix string) []string {
	var hosts []string
	for _, line := range strings.Split(out, "\n") {
		_, host, ok := strings.Cut(line, prefix+" ")
		host = strings.TrimSpace(host)
		if ok && host != "" && !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// A worker pool runs as many instances as its manual instance count, with its
// Cloud Storage volume mounted, and `gcloud run worker-pools logs read` reads
// what they log. Lowering the count stops the surplus with the stop signal
// before the update answers, and deleting the pool stops the rest. gcloud's
// deploy and update commands speak Cloud Run v2 over gRPC, which the
// simulator does not serve, so the pool is deployed over the v2 REST
// collection.
func TestCloudRunWorkerPools_CLI_LogsAndVolumeOfRunningInstances(t *testing.T) {
	const bucket = "cli-run-wp-volume"
	const pool = "cli-wp-run"
	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--location=us", "--format=json"))

	httpDoJSON(t, "POST", workerPoolsBaseURL()+"?workerPoolId="+pool, `{
		"scaling": {"manualInstanceCount": 2},
		"template": {
			"containers": [{
				"image": "public.ecr.aws/docker/library/alpine:latest",
				"command": `+cloudRunWorkloadScriptJSON("worker started", false)+`,
				"volumeMounts": [{"name": "bucket", "mountPath": "/mnt/bucket"}]
			}],
			"volumes": [{"name": "bucket", "gcs": {"bucket": "`+bucket+`"}}]
		}
	}`)
	deleted := false
	t.Cleanup(func() {
		if deleted {
			return
		}
		resp, err := httpDo("DELETE", workerPoolURL(pool), "")
		if err == nil {
			resp.Body.Close()
		}
	})

	// Cloud Logging offers the CLI no wait on an entry, so the log is read
	// until both instances' start lines are in it.
	var out string
	require.Eventually(t, func() bool {
		out = runCLI(t, gcloudCLI("run", "worker-pools", "logs", "read", pool, "--region="+location, "--limit=100"))
		return len(startedHosts(out, "worker started")) == 2
	}, 60*time.Second, 250*time.Millisecond, "the pool's two instances never logged their start: %s", out)
	assert.Len(t, bucketObjectsWithPrefix(t, bucket, "started-"), 2, "each instance wrote through the pool's volume")

	httpDoJSON(t, "PATCH", workerPoolURL(pool)+"?updateMask=scaling", `{"scaling": {"manualInstanceCount": 1}}`)
	assert.Len(t, bucketObjectsWithPrefix(t, bucket, "stopped-"), 1, "lowering the instance count stopped one instance")

	httpDoJSON(t, "DELETE", workerPoolURL(pool), "")
	deleted = true
	assert.Len(t, bucketObjectsWithPrefix(t, bucket, "stopped-"), 2, "deleting the pool stopped its last instance")
}

// A Cloud Run instance runs its containers from creation, and `gcloud alpha run
// instances logs read` reads what they log. Stopping the instance stops them
// with the stop signal before the call answers.
func TestCloudRunInstances_CLI_LogsAndVolumeOfRunningInstance(t *testing.T) {
	const bucket = "cli-run-inst-volume"
	const instance = "cli-inst-run"
	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--location=us", "--format=json"))

	httpDoJSON(t, "POST", instancesBaseURL()+"?instanceId="+instance, `{
		"containers": [{
			"image": "public.ecr.aws/docker/library/alpine:latest",
			"command": `+cloudRunWorkloadScriptJSON("instance started", true)+`,
			"volumeMounts": [{"name": "bucket", "mountPath": "/mnt/bucket"}]
		}],
		"volumes": [{"name": "bucket", "gcs": {"bucket": "`+bucket+`"}}]
	}`)
	t.Cleanup(func() {
		resp, err := httpDo("DELETE", instanceURL(instance), "")
		if err == nil {
			resp.Body.Close()
		}
	})

	var out string
	require.Eventually(t, func() bool {
		out = runCLI(t, gcloudCLI("alpha", "run", "instances", "logs", "read", instance, "--region="+location, "--limit=100"))
		return len(startedHosts(out, "instance started")) == 1
	}, 60*time.Second, 250*time.Millisecond, "the instance never logged its start: %s", out)
	assert.Len(t, bucketObjectsWithPrefix(t, bucket, "started-"), 1, "the instance wrote through its volume")

	httpDoJSON(t, "POST", instanceURL(instance)+":stop", "{}")
	assert.Len(t, bucketObjectsWithPrefix(t, bucket, "stopped-"), 1, "stopping the instance stopped its container")
}

// A worker pool whose instance cannot start does not deploy: the operation
// fails with the start error, and `gcloud run worker-pools describe` reports
// the pool's Ready condition False with that error, where a pool whose
// instances started reports it True.
func TestCloudRunWorkerPools_CLI_DescribeReportsTheDeployOutcome(t *testing.T) {
	const failing = "cli-wp-missing-image"
	failedOp := awaitRunOperation(t, httpDoJSON(t, "POST", workerPoolsBaseURL()+"?workerPoolId="+failing, `{
		"scaling": {"manualInstanceCount": 1},
		"template": {"containers": [{"image": "gcr.io/test-project/`+failing+`"}]}
	}`))
	t.Cleanup(func() {
		if resp, err := httpDo("DELETE", workerPoolURL(failing), ""); err == nil {
			resp.Body.Close()
		}
	})
	var op struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	parseJSON(t, failedOp, &op)
	assert.Equal(t, 13, op.Error.Code)
	assert.Contains(t, op.Error.Message, "gcr.io/test-project/"+failing)

	type described struct {
		Status struct {
			Conditions []struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"conditions"`
		} `json:"status"`
	}
	var pool described
	parseJSON(t, runCLI(t, gcloudRegionalRunCLI("run", "worker-pools", "describe", failing, "--region="+location, "--format=json")), &pool)
	require.NotEmpty(t, pool.Status.Conditions)
	assert.Equal(t, "Ready", pool.Status.Conditions[0].Type)
	assert.Equal(t, "False", pool.Status.Conditions[0].Status)
	assert.Contains(t, pool.Status.Conditions[0].Message, "gcr.io/test-project/"+failing)

	const ready = "cli-wp-started"
	awaitRunOperation(t, httpDoJSON(t, "POST", workerPoolsBaseURL()+"?workerPoolId="+ready, `{
		"scaling": {"manualInstanceCount": 1},
		"template": {"containers": [{"image": "`+commandImageName+`", "args": ["hold"]}]}
	}`))
	t.Cleanup(func() {
		if resp, err := httpDo("DELETE", workerPoolURL(ready), ""); err == nil {
			resp.Body.Close()
		}
	})
	pool = described{}
	parseJSON(t, runCLI(t, gcloudRegionalRunCLI("run", "worker-pools", "describe", ready, "--region="+location, "--format=json")), &pool)
	require.NotEmpty(t, pool.Status.Conditions)
	assert.Equal(t, "True", pool.Status.Conditions[0].Status)
}
