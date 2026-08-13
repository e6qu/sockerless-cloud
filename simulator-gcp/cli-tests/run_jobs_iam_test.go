package gcp_cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cloud Run Jobs IAM from the vendor CLI.
//
// What `gcloud run jobs` reaches, and what it does not:
//
//   - The IAM commands (`get-iam-policy`, `set-iam-policy`,
//     `add-iam-policy-binding`, `remove-iam-policy-binding`) are declarative
//     commands bound to the `run.projects.locations.jobs` collection, a Cloud
//     Run Admin **v1** collection served from the *global* endpoint at
//     `/v1/projects/{p}/locations/{l}/jobs/{id}:{verb}`. `CLOUDSDK_API_ENDPOINT_OVERRIDES_RUN`
//     points that at the simulator, so those commands are driven here as real
//     gcloud invocations.
//   - `create`, `describe`, `list`, `update`, `delete`, `execute` and the
//     `executions` / `tasks` groups resolve the *regional* Cloud Run host
//     (`{region}-run.googleapis.com`), which gcloud builds by prefixing the
//     configured endpoint with `{region}-`; no endpoint coordinate can point
//     that at a loopback simulator. Those commands therefore have no CLI
//     coverage — the same limitation `gcloud run worker-pools` hits (see
//     run_workerpools_v2_test.go). The Knative jobs, executions and tasks
//     collections they would reach are driven by the official
//     google.golang.org/api/run/v1 client in sdk-tests.

func runJobsBaseURL() string {
	return fmt.Sprintf("%s/v2/projects/%s/locations/%s/jobs", baseURL, project, location)
}

// createRunJobForCLI creates a Cloud Run job over the v2 collection and
// registers its teardown, so the IAM commands act on a resource that exists.
func createRunJobForCLI(t *testing.T, id string) {
	t.Helper()
	body := `{"template":{"template":{"containers":[{"image":"gcr.io/test-project/` + id + `"}]}}}`
	httpDoJSON(t, "POST", runJobsBaseURL()+"?jobId="+id, body)
	t.Cleanup(func() {
		resp, err := httpDo("DELETE", runJobsBaseURL()+"/"+id, "")
		if err == nil {
			resp.Body.Close()
		}
	})
}

func TestCloudRunJobs_CLI_IAMPolicyLifecycle(t *testing.T) {
	const id = "cli-job-iam"
	createRunJobForCLI(t, id)

	out := runCLI(t, gcloudCLI("run", "jobs", "get-iam-policy", id,
		"--region="+location, "--format=json"))
	var initial struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
		Etag string `json:"etag"`
	}
	parseJSON(t, out, &initial)
	assert.Empty(t, initial.Bindings, "a newly created job has no IAM bindings")
	assert.NotEmpty(t, initial.Etag, "getIamPolicy always returns an etag")

	added := runCLI(t, gcloudCLI("run", "jobs", "add-iam-policy-binding", id,
		"--region="+location,
		"--member=user:cli-job@example.com",
		"--role=roles/run.invoker",
		"--format=json"))
	var afterAdd struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
	}
	parseJSON(t, added, &afterAdd)
	require.Len(t, afterAdd.Bindings, 1)
	assert.Equal(t, "roles/run.invoker", afterAdd.Bindings[0].Role)
	assert.Equal(t, []string{"user:cli-job@example.com"}, afterAdd.Bindings[0].Members)

	// The v1 collection gcloud writes through and the v2 collection the SDK
	// reads are two spellings of one resource — the binding is visible on both.
	v2Policy := httpDoJSON(t, "GET", runJobsBaseURL()+"/"+id+":getIamPolicy", "")
	var fromV2 struct {
		Bindings []struct {
			Role string `json:"role"`
		} `json:"bindings"`
	}
	parseJSON(t, v2Policy, &fromV2)
	require.Len(t, fromV2.Bindings, 1, "the v2 collection must see the policy gcloud wrote through v1")
	assert.Equal(t, "roles/run.invoker", fromV2.Bindings[0].Role)

	policyPath := filepath.Join(tmpDir, "run-job-policy.json")
	data, err := json.Marshal(map[string]any{
		"bindings": []map[string]any{{
			"role":    "roles/run.developer",
			"members": []string{"serviceAccount:deployer@test-project.iam.gserviceaccount.com"},
		}},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(policyPath, data, 0o600))

	set := runCLI(t, gcloudCLI("run", "jobs", "set-iam-policy", id, policyPath,
		"--region="+location, "--format=json"))
	var afterSet struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
	}
	parseJSON(t, set, &afterSet)
	require.Len(t, afterSet.Bindings, 1)
	assert.Equal(t, "roles/run.developer", afterSet.Bindings[0].Role)
}

// TestCloudRunJobs_CLI_IAMPolicyOnMissingJob pins the CLI-visible error for a
// job that was never created: the IAM verb is NOT_FOUND, not an empty policy.
func TestCloudRunJobs_CLI_IAMPolicyOnMissingJob(t *testing.T) {
	cmd := gcloudCLI("run", "jobs", "get-iam-policy", "cli-job-never-created",
		"--region="+location, "--format=json")
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "get-iam-policy on an absent job must fail")
	assert.Contains(t, string(out), "cli-job-never-created")
}
