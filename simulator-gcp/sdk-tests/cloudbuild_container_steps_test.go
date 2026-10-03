package gcp_sdk_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"testing"
	"time"

	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// A Cloud Build step whose builder is any image runs as a container of that
// image with the build's workspace at /workspace, in the step's dir, under its
// entrypoint, args and environment. A file one step writes is there for the
// next step, a docker step included, and the source may be a zip archive.
func TestCloudBuild_ContainerStepsShareTheWorkspace(t *testing.T) {
	project := "cb-container-steps-project"
	bucket := "cb-container-steps-bucket"
	createBucket(t, project, bucket)
	object := fmt.Sprintf("container-steps-%d.zip", time.Now().UnixNano())
	uploadGCSObject(t, bucket, object, makeZip(t, map[string]string{
		"app/input.txt": "from the source archive\n",
		"Dockerfile":    "FROM " + cbBaseImage + "\nCOPY app/generated.txt /generated.txt\nRUN grep -q 'written by a step' /generated.txt\n",
	}))
	image := fmt.Sprintf("sim-cb-container-steps:%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerCLIWithTimeout(60*time.Second, "image", "rm", "-f", image) })

	body := cbBuildBody(t, bucket, object, []any{
		map[string]any{
			"name":       cbBaseImage,
			"entrypoint": "sh",
			"dir":        "app",
			"env":        []string{"GREETING=written by a step"},
			"args":       []string{"-c", `grep -q 'from the source archive' input.txt && echo "$GREETING" > generated.txt`},
		},
		cbDockerStep([]string{"build", "-t", image, "."}),
		map[string]any{
			"name":       cbBaseImage,
			"entrypoint": "sh",
			"args":       []string{"-c", "test -s /workspace/app/generated.txt"},
		},
	}, nil)
	built, err := waitBuild(t, readStartedBuild(t, httpPOST(t, fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, project), body)))
	require.NoError(t, err)
	assert.Equal(t, cloudbuildpb.Build_SUCCESS, built.GetStatus())
	for i, step := range built.GetSteps() {
		assert.Equal(t, cloudbuildpb.Build_SUCCESS, step.GetStatus(), "step %d", i)
	}
}

// A container step that exits non-zero fails its build with the exit status
// and what the step printed last, and the steps after it never run.
func TestCloudBuild_FailingContainerStepFailsTheBuild(t *testing.T) {
	project := "cb-container-steps-project"
	bucket := "cb-container-steps-bucket"
	createBucket(t, project, bucket)
	object := fmt.Sprintf("failing-step-%d.tar.gz", time.Now().UnixNano())
	uploadGCSObject(t, bucket, object, makeTarGz(t, map[string]string{"README": "source\n"}))

	body := cbBuildBody(t, bucket, object, []any{
		map[string]any{
			"name":       cbBaseImage,
			"entrypoint": "sh",
			"args":       []string{"-c", "echo the step gives up; exit 7"},
		},
		map[string]any{"name": cbBaseImage, "args": []string{"true"}},
	}, nil)
	started := readStartedBuild(t, httpPOST(t, fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, project), body))
	_, err := waitBuild(t, started)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 7")
	assert.Contains(t, err.Error(), "the step gives up")

	build, err := newCloudBuildClient(t).GetBuild(ctx, &cloudbuildpb.GetBuildRequest{ProjectId: project, Id: started.BuildID})
	require.NoError(t, err)
	assert.Equal(t, cloudbuildpb.Build_FAILURE, build.GetStatus())
	require.Len(t, build.GetSteps(), 2)
	assert.Equal(t, cloudbuildpb.Build_FAILURE, build.GetSteps()[0].GetStatus())
	assert.Empty(t, build.GetSteps()[1].GetTiming().GetStartTime(), "the step after the failed one never started")
}
