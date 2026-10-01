package azure_sdk_test

import (
	"encoding/base64"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// acrTasksRunLog reads a finished run's whole log blob.
func acrTasksRunLog(t *testing.T, runs *armcontainerregistry.RunsClient, rg, registry, runID string) string {
	t.Helper()
	sas, err := runs.GetLogSasURL(ctx, rg, registry, runID, nil)
	require.NoError(t, err)
	logBlob, err := blob.NewClientWithNoCredential(*sas.LogLink, nil)
	require.NoError(t, err)
	download, err := logBlob.DownloadStream(ctx, nil)
	require.NoError(t, err)
	content, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	return string(content)
}

// acrTasksSchedule schedules a run and waits for it to end.
func acrTasksSchedule(t *testing.T, rg, registry string, request armcontainerregistry.RunRequestClassification) armcontainerregistry.Run {
	t.Helper()
	registries, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := registries.BeginScheduleRun(ctx, rg, registry, request, nil)
	require.NoError(t, err)
	queued, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, queued.Properties.RunID)
	runs, err := armcontainerregistry.NewRunsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	return awaitACRRun(t, runs, rg, registry, *queued.Properties.RunID)
}

// TestACRTasks_FileTaskRunFromUploadedSource uploads a source directory the
// way `az acr run` does — to the URL Registries_GetBuildSourceUploadUrl hands
// out, with a block blob upload — and schedules a FileTaskRunRequest on the
// relative path. The task file's cmd steps run in the source's /workspace and
// share what they write, its build step builds the source, and its push step
// pushes into the registry as the run, which reports the image it pushed.
func TestACRTasks_FileTaskRunFromUploadedSource(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("platform gate: the container engine pushes to the registry's login server itself, and on a host whose engine runs inside its own virtual machine it has no route to this host's loopback, where the simulator's registry listens.")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for ACR Tasks runs (no fallback): %v", err)
	}
	const rg, regName = "acr-tasks-run-rg", "acrtaskfilerunreg"
	acrEnsureRegistry(t, rg, regName)
	pullImageWithRetry(t, "public.ecr.aws/docker/library/alpine:3.20")

	registries, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	registry, err := registries.Get(ctx, rg, regName, nil)
	require.NoError(t, err)
	loginServer := *registry.Properties.LoginServer

	marker := fmt.Sprintf("uploaded-%d", time.Now().UnixNano())
	upload, err := registries.GetBuildSourceUploadURL(ctx, rg, regName, nil)
	require.NoError(t, err)
	require.NotNil(t, upload.UploadURL)
	require.NotNil(t, upload.RelativePath)
	uploader, err := blockblob.NewClientWithNoCredential(*upload.UploadURL, nil)
	require.NoError(t, err)
	_, err = uploader.UploadBuffer(ctx, makeACRBuildContext(t, map[string]string{
		"marker.txt": marker + "\n",
		"Dockerfile": "FROM public.ecr.aws/docker/library/alpine:3.20\nCOPY marker.txt step.txt /\n",
		"acb.yaml": `version: v1.1.0
steps:
  - id: write
    cmd: public.ecr.aws/docker/library/alpine:3.20 sh -c "cat marker.txt > step.txt && echo step-saw-$(cat marker.txt)"
  - id: image
    build: -t $Registry/task-run:{{.Values.tag}} .
  - push: ["$Registry/task-run:{{.Values.tag}}"]
`,
	}), &blockblob.UploadBufferOptions{BlockSize: 64})
	require.NoError(t, err, "the upload URL takes a staged block upload")

	tag := fmt.Sprintf("t%d", time.Now().UnixNano())
	run := acrTasksSchedule(t, rg, regName, &armcontainerregistry.FileTaskRunRequest{
		Type:           to.Ptr("FileTaskRunRequest"),
		TaskFilePath:   to.Ptr("acb.yaml"),
		SourceLocation: upload.RelativePath,
		Values:         []*armcontainerregistry.SetValue{{Name: to.Ptr("tag"), Value: to.Ptr(tag)}},
		Platform:       &armcontainerregistry.PlatformProperties{OS: to.Ptr(armcontainerregistry.OSLinux)},
	})
	runs, err := armcontainerregistry.NewRunsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	log := acrTasksRunLog(t, runs, rg, regName, *run.Properties.RunID)
	require.Equal(t, armcontainerregistry.RunStatusSucceeded, ptrVal(run.Properties.Status),
		"%s\n%s", ptrVal(run.Properties.RunErrorMessage), log)
	assert.Equal(t, armcontainerregistry.RunTypeQuickRun, ptrVal(run.Properties.RunType))
	assert.Contains(t, log, "step-saw-"+marker, "the cmd step ran in the uploaded source")
	require.Len(t, run.Properties.OutputImages, 1)
	assert.Equal(t, "task-run", ptrVal(run.Properties.OutputImages[0].Repository))
	assert.Equal(t, tag, ptrVal(run.Properties.OutputImages[0].Tag))

	_, err = acrDataPlaneClient(t, loginServer).GetManifest(ctx, "task-run", tag, nil)
	require.NoError(t, err, "the image the push step pushed is in the registry")
	assert.Error(t, exec.Command("docker", "image", "inspect", loginServer+"/task-run:"+tag).Run(),
		"the run leaves its build output in the registry, not on the build host")
}

// TestACRTasks_TaskRunRequestRunsTheTask creates a Task whose step is an
// encoded task file, runs it through a TaskRunRequest with an overriding
// value, and reads the run's task name and output.
func TestACRTasks_TaskRunRequestRunsTheTask(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for ACR Tasks runs (no fallback): %v", err)
	}
	const rg, regName, taskName = "acr-tasks-run-rg", "acrtaskrunreqreg", "greet"
	acrEnsureRegistry(t, rg, regName)
	pullImageWithRetry(t, "public.ecr.aws/docker/library/alpine:3.20")

	tasks, err := armcontainerregistry.NewTasksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	content := "steps:\n  - cmd: public.ecr.aws/docker/library/alpine:3.20 echo greeting={{.Values.who}} task={{.Run.TaskName}}\n"
	poller, err := tasks.BeginCreate(ctx, rg, regName, taskName, armcontainerregistry.Task{
		Location: to.Ptr("eastus"),
		Properties: &armcontainerregistry.TaskProperties{
			Status:   to.Ptr(armcontainerregistry.TaskStatusEnabled),
			Platform: &armcontainerregistry.PlatformProperties{OS: to.Ptr(armcontainerregistry.OSLinux)},
			Step: &armcontainerregistry.EncodedTaskStep{
				Type:               to.Ptr(armcontainerregistry.StepTypeEncodedTask),
				EncodedTaskContent: to.Ptr(base64.StdEncoding.EncodeToString([]byte(content))),
				Values:             []*armcontainerregistry.SetValue{{Name: to.Ptr("who"), Value: to.Ptr("task-default")}},
			},
		},
	}, nil)
	require.NoError(t, err)
	task, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	run := acrTasksSchedule(t, rg, regName, &armcontainerregistry.TaskRunRequest{
		Type:   to.Ptr("TaskRunRequest"),
		TaskID: task.ID,
		OverrideTaskStepProperties: &armcontainerregistry.OverrideTaskStepProperties{
			Values: []*armcontainerregistry.SetValue{{Name: to.Ptr("who"), Value: to.Ptr("override")}},
		},
	})
	runs, err := armcontainerregistry.NewRunsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	log := acrTasksRunLog(t, runs, rg, regName, *run.Properties.RunID)
	require.Equal(t, armcontainerregistry.RunStatusSucceeded, ptrVal(run.Properties.Status),
		"%s\n%s", ptrVal(run.Properties.RunErrorMessage), log)
	assert.Equal(t, taskName, ptrVal(run.Properties.Task))
	assert.Contains(t, log, "greeting=override task="+taskName)
}

// TestACRTasks_AgentPoolQueuesRunsBeyondItsAgents schedules two runs on an
// agent pool of one agent: the second waits Queued while the first runs, and
// AgentPools_GetQueueStatus counts it, until a cancel frees the agent.
func TestACRTasks_AgentPoolQueuesRunsBeyondItsAgents(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for ACR Tasks runs (no fallback): %v", err)
	}
	const rg, regName, pool = "acr-tasks-run-rg", "acrtaskpoolreg", "pool1"
	acrEnsureRegistry(t, rg, regName)
	pullImageWithRetry(t, "public.ecr.aws/docker/library/alpine:3.20")

	pools, err := armcontainerregistry.NewAgentPoolsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	created, err := pools.BeginCreate(ctx, rg, regName, pool, armcontainerregistry.AgentPool{
		Location:   to.Ptr("eastus"),
		Properties: &armcontainerregistry.AgentPoolProperties{Count: to.Ptr[int32](1), Tier: to.Ptr("S1"), OS: to.Ptr(armcontainerregistry.OSLinux)},
	}, nil)
	require.NoError(t, err)
	_, err = created.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	registries, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	runs, err := armcontainerregistry.NewRunsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	content := base64.StdEncoding.EncodeToString([]byte(
		"steps:\n  - cmd: public.ecr.aws/docker/library/alpine:3.20 sh -c \"echo agent-busy && sleep 600\"\n"))
	schedule := func() string {
		poller, err := registries.BeginScheduleRun(ctx, rg, regName, &armcontainerregistry.EncodedTaskRunRequest{
			Type:               to.Ptr("EncodedTaskRunRequest"),
			EncodedTaskContent: to.Ptr(content),
			AgentPoolName:      to.Ptr(pool),
			Platform:           &armcontainerregistry.PlatformProperties{OS: to.Ptr(armcontainerregistry.OSLinux)},
		}, nil)
		require.NoError(t, err)
		queued, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		assert.Equal(t, pool, ptrVal(queued.Properties.AgentPoolName))
		return *queued.Properties.RunID
	}
	cancel := func(runID string) {
		poller, err := runs.BeginCancel(ctx, rg, regName, runID, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	}

	first := schedule()
	t.Cleanup(func() { cancel(first) })
	running := awaitACRRunLeaves(t, runs, rg, regName, first, armcontainerregistry.RunStatusQueued)
	require.Equal(t, armcontainerregistry.RunStatusRunning, ptrVal(running.Properties.Status))

	second := schedule()
	t.Cleanup(func() { cancel(second) })
	status, err := pools.GetQueueStatus(ctx, rg, regName, pool, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(1), ptrVal(status.Count), "the run waiting for the pool's one agent is queued")
	waiting, err := runs.Get(ctx, rg, regName, second, nil)
	require.NoError(t, err)
	assert.Equal(t, armcontainerregistry.RunStatusQueued, ptrVal(waiting.Properties.Status))

	cancel(first)
	next := awaitACRRunLeaves(t, runs, rg, regName, second, armcontainerregistry.RunStatusQueued)
	assert.Equal(t, armcontainerregistry.RunStatusRunning, ptrVal(next.Properties.Status),
		"the queued run takes the agent the canceled run freed")
	status, err = pools.GetQueueStatus(ctx, rg, regName, pool, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(0), ptrVal(status.Count))
	cancel(second)
	got, err := runs.Get(ctx, rg, regName, second, nil)
	require.NoError(t, err)
	assert.True(t, strings.EqualFold(string(ptrVal(got.Properties.Status)), "Canceled"))
}
