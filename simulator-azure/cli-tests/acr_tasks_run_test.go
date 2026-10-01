package azure_cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestACRTasksCLI_BuildRunAndTaskRun drives the native az commands that run
// Azure Container Registry Tasks from a local directory: `az acr build`
// uploads the directory to the URL listBuildSourceUploadUrl hands out and
// schedules a DockerBuildRequest on the relative path; `az acr run -f` does the
// same with a FileTaskRunRequest; `az acr run --cmd` schedules an
// EncodedTaskRunRequest without source; and `az acr task run` schedules a
// TaskRunRequest of a task `az acr task create` made. Each streams the run's
// log blob until it completes and fails unless the run succeeded.
//
// The runs build without pushing: this suite's simulator serves its
// registries over TLS with a certificate the container engine's token client
// does not consult per-registry trust for. The SDK suite covers the push.
func TestACRTasksCLI_BuildRunAndTaskRun(t *testing.T) {
	env := startAzLoginSimulator(t)
	runCLI(t, env.command("cloud", "register", "-n", "sockerless-acr-tasks",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-acr-tasks"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	defer runCLI(t, env.command("logout"))

	const rg, registry = "cli-acr-tasks-run-rg", "cliacrtasksrun"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	runCLI(t, env.command("acr", "create", "-n", registry, "-g", rg, "--sku", "Standard", "-o", "json"))
	pullWorkloadImage("public.ecr.aws/docker/library/alpine:3.20")

	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "Dockerfile"),
		[]byte("FROM public.ecr.aws/docker/library/alpine:3.20\nCOPY greeting.txt /greeting.txt\nRUN cat /greeting.txt\n"), 0o644))
	// A greeting of this run's own keeps the build host's layer cache from
	// answering for the build step that prints it.
	greeting := fmt.Sprintf("hello from the uploaded source %d", time.Now().UnixNano())
	require.NoError(t, os.WriteFile(filepath.Join(source, "greeting.txt"), []byte(greeting+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "acb.yaml"), []byte(`version: v1.1.0
steps:
  - cmd: public.ecr.aws/docker/library/alpine:3.20 sh -c "cat greeting.txt && echo written-by-step-one > artifact.txt"
  - cmd: public.ecr.aws/docker/library/alpine:3.20 cat artifact.txt
  - build: -t $Registry/cli-run:$ID .
  - cmd: public.ecr.aws/docker/library/alpine:3.20 echo value={{.Values.greeting}} registry=$RegistryName
`), 0o644))

	build := runCLI(t, env.command("acr", "build", "-r", registry, "-g", rg,
		"-t", "cli-build:{{.Run.ID}}", "--no-push", source))
	assert.Contains(t, build, greeting, "the build's context is the uploaded directory")
	assert.Contains(t, build, "was successful", "az acr build streams the run log to its end")

	run := runCLI(t, env.command("acr", "run", "-r", registry, "-g", rg, "-f", "acb.yaml",
		"--set", "greeting=from-set", source))
	assert.Contains(t, run, greeting, "a cmd step reads the uploaded source in /workspace")
	assert.Contains(t, run, "written-by-step-one", "a later step reads what an earlier step wrote to /workspace")
	assert.Contains(t, run, "value=from-set registry="+registry, "--set values and aliases render into the task file")

	cmd := runCLI(t, env.command("acr", "run", "-r", registry, "-g", rg,
		"--cmd", "public.ecr.aws/docker/library/alpine:3.20 echo quick-run-ok", "/dev/null"))
	assert.Contains(t, cmd, "quick-run-ok")

	runCLI(t, env.command("acr", "task", "create", "-n", "clitask", "-r", registry, "-g", rg,
		"--cmd", "public.ecr.aws/docker/library/alpine:3.20 echo task-run-ok", "-c", "/dev/null", "-o", "json"))
	task := runCLI(t, env.command("acr", "task", "run", "-n", "clitask", "-r", registry, "-g", rg))
	assert.Contains(t, task, "task-run-ok")

	var runs []struct {
		RunType string `json:"runType"`
		Status  string `json:"status"`
		Task    string `json:"task"`
	}
	parseJSON(t, runCLI(t, env.command("acr", "task", "list-runs", "-r", registry, "-g", rg, "-o", "json")), &runs)
	got := []string{}
	for _, r := range runs {
		assert.Equal(t, "Succeeded", r.Status)
		got = append(got, r.RunType+":"+r.Task)
	}
	sort.Strings(got)
	assert.Equal(t, []string{"QuickBuild:", "QuickRun:", "QuickRun:", "QuickRun:clitask"}, got)

	failing := runStorageCLIExpectFailure(t, env.command("acr", "run", "-r", registry, "-g", rg,
		"--cmd", "public.ecr.aws/docker/library/alpine:3.20 false", "/dev/null"))
	assert.Contains(t, failing, "Run failed", "a failing step fails the run")
}
