package gcp_cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `gcloud run jobs create --depends-on --startup-probe` carries the container
// dependencies in the Knative container-dependencies annotation; the main
// container makes one connection attempt to a server that opens its port three
// seconds after it starts, so `execute --wait` succeeds only when the main
// container starts after the server passed its startup probe.
func TestCloudRun_CLI_JobContainerDependsOnStartupProbe(t *testing.T) {
	const job = "cli-job-depends-on"
	runCLI(t, gcloudRegionalRunCLI("run", "jobs", "create", job,
		"--region="+location,
		"--max-retries=0",
		"--task-timeout=60s",
		"--quiet",
		"--container=main",
		"--image=alpine:latest",
		"--command=nc",
		"--args=-z,127.0.0.1,9090",
		"--depends-on=server",
		"--container=server",
		"--image="+commandImageName,
		"--args=http,9090,ready,3",
		"--startup-probe=httpGet.port=9090,httpGet.path=/,periodSeconds=1,timeoutSeconds=1,failureThreshold=30",
	))
	t.Cleanup(func() {
		runCLI(t, gcloudRegionalRunCLI("run", "jobs", "delete", job, "--region="+location, "--quiet"))
	})

	var stored struct {
		Template struct {
			Template struct {
				Containers []struct {
					Name         string   `json:"name"`
					DependsOn    []string `json:"dependsOn"`
					StartupProbe *struct {
						HTTPGet struct {
							Port int `json:"port"`
						} `json:"httpGet"`
					} `json:"startupProbe"`
				} `json:"containers"`
			} `json:"template"`
		} `json:"template"`
	}
	parseJSON(t, httpDoJSON(t, "GET", jobURL(job), ""), &stored)
	containers := stored.Template.Template.Containers
	require.Len(t, containers, 2)
	assert.Equal(t, []string{"server"}, containers[0].DependsOn)
	require.NotNil(t, containers[1].StartupProbe)
	assert.Equal(t, 9090, containers[1].StartupProbe.HTTPGet.Port)

	described := runCLI(t, gcloudRegionalRunCLI("run", "jobs", "describe", job, "--region="+location, "--format=json"))
	assert.Contains(t, described, "run.googleapis.com/container-dependencies")

	runCLI(t, gcloudRegionalRunCLI("run", "jobs", "execute", job, "--region="+location, "--wait", "--quiet"))
}

// `gcloud run jobs executions cancel` on an execution that already succeeded
// leaves it succeeded, and gcloud reports that it completed before it could be
// cancelled.
func TestCloudRun_CLI_CancelCompletedExecution(t *testing.T) {
	const job = "cli-job-cancel-done"
	runCLI(t, gcloudRegionalRunCLI("run", "jobs", "create", job,
		"--region="+location,
		"--image=alpine:latest",
		"--command=true",
		"--max-retries=0",
		"--quiet",
	))
	t.Cleanup(func() {
		runCLI(t, gcloudRegionalRunCLI("run", "jobs", "delete", job, "--region="+location, "--quiet"))
	})
	execution := strings.TrimSpace(runCLI(t, gcloudRegionalRunCLI("run", "jobs", "execute", job,
		"--region="+location, "--wait", "--quiet", "--format=value(metadata.name)")))
	require.NotEmpty(t, execution)

	refused := gcloudCLIFails(t, gcloudRegionalRunCLI("run", "jobs", "executions", "cancel", execution,
		"--region="+location, "--quiet"))
	assert.Contains(t, refused, "has completed successfully before it could be cancelled")

	succeeded := runCLI(t, gcloudRegionalRunCLI("run", "jobs", "executions", "describe", execution,
		"--region="+location, "--format=value(status.succeededCount,status.cancelledCount)"))
	assert.Equal(t, []string{"1"}, strings.Fields(succeeded), "one task succeeded and none was cancelled")
}

// `gcloud run jobs delete` on a job whose execution still runs stops the
// execution's container: the RunJob operation ends once it has stopped.
func TestCloudRun_CLI_DeleteJobStopsItsRunningExecution(t *testing.T) {
	const job = "cli-job-delete-running"
	runCLI(t, gcloudRegionalRunCLI("run", "jobs", "create", job,
		"--region="+location,
		"--image="+commandImageName,
		"--args=log,cli-delete-running-started,600",
		"--max-retries=0",
		"--task-timeout=600s",
		"--quiet",
	))
	run := runJob(t, job)
	require.Eventually(t, func() bool {
		return strings.Contains(runCLI(t, gcloudCLI("logging", "read",
			`resource.type="cloud_run_job" AND resource.labels.job_name="`+job+`"`,
			"--format=value(textPayload)")), "cli-delete-running-started")
	}, 60*time.Second, 250*time.Millisecond, "the execution's workload container never came up")

	runCLI(t, gcloudRegionalRunCLI("run", "jobs", "delete", job, "--region="+location, "--quiet"))
	op := waitRunOperation(t, run.Operation)
	require.NotNil(t, op.Error, "the deleted execution ends its RunJob operation")
	assert.Equal(t, 5, op.Error.Code, "google.rpc.Code.NOT_FOUND")
}
