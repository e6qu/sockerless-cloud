package gcp_tf_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTerraformCloudRunV2JobCloudStorageVolume applies a
// google_cloud_run_v2_job that mounts a google_storage_bucket through two
// gcs volumes, one of them narrowed to a directory by the only-dir mount
// option, runs the job, and reads the bucket: what the job wrote, overwrote
// and renamed through the mounts is there as object changes, as Cloud Storage
// FUSE makes them.
func TestTerraformCloudRunV2JobCloudStorageVolume(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "cloudrun-gcs-volume")
	cleanTerraformFixture(t, fixtureDir)
	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", terraformCmdInDir(fixtureDir, "destroy", "-auto-approve"))
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	})
	out, err = runTimed(t, "terraform apply", terraformCmdInDir(fixtureDir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	outputs := readOutputsInDir(t, fixtureDir)
	assert.Equal(t, []any{"only-dir=results"}, outputs.mustValue(t, "mount_options"),
		"the provider reads the volume's mount options back")
	bucket := outputs.must(t, "bucket")

	var run struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal(simCall(t, http.MethodPost, "/v2/"+outputs.must(t, "job_name")+":run", "{}"), &run))
	require.NotEmpty(t, run.Name)
	var op struct {
		Done  bool            `json:"done"`
		Error json.RawMessage `json:"error"`
	}
	for !op.Done {
		require.NoError(t, json.Unmarshal(simCall(t, http.MethodPost, "/v2/"+run.Name+":wait", `{"timeout":"120s"}`), &op))
	}
	require.Empty(t, op.Error, "the job's execution failed")

	var listing struct {
		Items []struct {
			Name     string            `json:"name"`
			Metadata map[string]string `json:"metadata"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(simCall(t, http.MethodGet, "/storage/v1/b/"+bucket+"/o", ""), &listing))
	var names []string
	for _, item := range listing.Items {
		names = append(names, item.Name)
		if item.Name == "seed.txt" {
			assert.NotEmpty(t, item.Metadata["gcsfuse_mtime"], "the generation records the file's modification time")
		}
	}
	sort.Strings(names)
	assert.Equal(t, []string{"made/", "made/inside.txt", "renamed.txt", "results/copy.txt", "seed.txt"}, names)

	media := func(name string) string {
		return string(simCall(t, http.MethodGet, "/storage/v1/b/"+bucket+"/o/"+url.PathEscape(name)+"?alt=media", ""))
	}
	assert.Equal(t, "seed\nappended\n", media("seed.txt"))
	assert.Equal(t, "seed\n", media("results/copy.txt"))
	assert.Equal(t, "inside\n", media("made/inside.txt"))
	assert.Equal(t, "renamed\n", media("renamed.txt"))
}

// simCall sends a request to the simulator with the access token the
// provider presents, and returns the body of a successful answer.
func simCall(t *testing.T, method, path, body string) []byte {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, baseURL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Less(t, resp.StatusCode, 300, "%s %s: %s", method, path, data)
	return data
}
