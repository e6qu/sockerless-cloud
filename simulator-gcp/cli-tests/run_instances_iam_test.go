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

// Cloud Run instances IAM from the vendor CLI.
//
// `gcloud alpha run instances {get,set}-iam-policy` and
// `{add,remove}-iam-policy-binding` are declarative commands bound to the
// `run.projects.locations.instances` collection, which is a Cloud Run Admin
// **v1** collection: they call
// `/v1/projects/{p}/locations/{l}/instances/{id}:{verb}` on the global
// endpoint. Real Google Cloud serves that path from run.googleapis.com while
// Memorystore for Redis serves the identically-shaped instance lifecycle from
// redis.googleapis.com; the single-origin simulator resolves the owner from the
// request `Host` when a real Google host was resolved, and otherwise from the
// AIP-136 custom method (the two services' custom methods are disjoint). Both
// CLIs are driven here against the same prefix.

func runInstancesBaseURL() string {
	return fmt.Sprintf("%s/v2/projects/%s/locations/%s/instances", baseURL, project, location)
}

// createRunInstanceForCLI creates a Cloud Run instance over the v2 collection
// and registers its teardown. `gcloud run instances create` resolves the
// regional Cloud Run host (`{region}-run.googleapis.com`), which no endpoint
// coordinate points at a loopback simulator, so the resource the IAM commands
// act on is created through the same v2 API the Cloud Run backend uses.
func createRunInstanceForCLI(t *testing.T, id string) {
	t.Helper()
	body := `{"containers": [{"image": "gcr.io/test-project/` + id + `"}]}`
	httpDoJSON(t, "POST", runInstancesBaseURL()+"?instanceId="+id, body)
	t.Cleanup(func() {
		resp, err := httpDo("DELETE", runInstancesBaseURL()+"/"+id, "")
		if err == nil {
			resp.Body.Close()
		}
	})
}

func TestCloudRunInstances_CLI_IAMPolicyLifecycle(t *testing.T) {
	const id = "cli-inst-iam"
	createRunInstanceForCLI(t, id)

	out := runCLI(t, gcloudCLI("alpha", "run", "instances", "get-iam-policy", id,
		"--region="+location, "--format=json"))
	var initial struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
		Etag string `json:"etag"`
	}
	parseJSON(t, out, &initial)
	assert.Empty(t, initial.Bindings, "a newly created instance has no IAM bindings")
	assert.NotEmpty(t, initial.Etag, "getIamPolicy always returns an etag")

	added := runCLI(t, gcloudCLI("alpha", "run", "instances", "add-iam-policy-binding", id,
		"--region="+location,
		"--member=user:cli-inst@example.com",
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
	assert.Equal(t, []string{"user:cli-inst@example.com"}, afterAdd.Bindings[0].Members)

	// The v1 collection gcloud writes through and the v2 collection the SDK
	// reads are two spellings of one resource — the binding is visible on both.
	v2Policy := httpDoJSON(t, "GET", runInstancesBaseURL()+"/"+id+":getIamPolicy", "")
	var fromV2 struct {
		Bindings []struct {
			Role string `json:"role"`
		} `json:"bindings"`
	}
	parseJSON(t, v2Policy, &fromV2)
	require.Len(t, fromV2.Bindings, 1, "the v2 collection must see the policy gcloud wrote through v1")
	assert.Equal(t, "roles/run.invoker", fromV2.Bindings[0].Role)

	removed := runCLI(t, gcloudCLI("alpha", "run", "instances", "remove-iam-policy-binding", id,
		"--region="+location,
		"--member=user:cli-inst@example.com",
		"--role=roles/run.invoker",
		"--format=json"))
	var afterRemove struct {
		Bindings []struct {
			Role string `json:"role"`
		} `json:"bindings"`
	}
	parseJSON(t, removed, &afterRemove)
	assert.Empty(t, afterRemove.Bindings, "the only binding is gone after remove")

	policyPath := filepath.Join(tmpDir, "run-instance-policy.json")
	data, err := json.Marshal(map[string]any{
		"bindings": []map[string]any{{
			"role":    "roles/run.developer",
			"members": []string{"serviceAccount:deployer@test-project.iam.gserviceaccount.com"},
		}},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(policyPath, data, 0o600))

	set := runCLI(t, gcloudCLI("alpha", "run", "instances", "set-iam-policy", id, policyPath,
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
	assert.Equal(t,
		[]string{"serviceAccount:deployer@test-project.iam.gserviceaccount.com"},
		afterSet.Bindings[0].Members)
}

// TestCloudRunInstances_CLI_IAMPolicyOnMissingInstance pins the CLI-visible
// error for an instance that was never created: the IAM verb is NOT_FOUND, not
// an empty policy.
func TestCloudRunInstances_CLI_IAMPolicyOnMissingInstance(t *testing.T) {
	cmd := gcloudCLI("alpha", "run", "instances", "get-iam-policy", "cli-inst-never-created",
		"--region="+location, "--format=json")
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "get-iam-policy on an absent instance must fail")
	assert.Contains(t, string(out), "NOT_FOUND")
	assert.Contains(t, string(out), "cli-inst-never-created")
}

// TestCloudRunInstances_CLI_CoexistWithMemorystore drives both vendor CLIs that
// address `/v1/projects/{p}/locations/{l}/instances/…` — `gcloud alpha run
// instances get-iam-policy` and `gcloud redis instances describe` — against one
// simulator origin, with the same instance id on both services.
func TestCloudRunInstances_CLI_CoexistWithMemorystore(t *testing.T) {
	const id = "cli-shared-instances"
	createRunInstanceForCLI(t, id)

	runCLI(t, gcloudCLI("redis", "instances", "create", id,
		"--region", location,
		"--size", "1",
		"--tier", "basic",
		"--redis-version", "redis_7_0",
		"--format=json",
	))
	t.Cleanup(func() {
		_ = gcloudCLI("redis", "instances", "delete", id,
			"--region", location, "--quiet", "--format=json").Run()
	})

	policy := runCLI(t, gcloudCLI("alpha", "run", "instances", "get-iam-policy", id,
		"--region="+location, "--format=json"))
	var runPolicy struct {
		Etag string `json:"etag"`
	}
	parseJSON(t, policy, &runPolicy)
	assert.NotEmpty(t, runPolicy.Etag, "Cloud Run's IAM alias answers on the shared prefix")

	described := runCLI(t, gcloudCLI("redis", "instances", "describe", id,
		"--region", location, "--format=json"))
	var redisInstance struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	parseJSON(t, described, &redisInstance)
	assert.Equal(t,
		fmt.Sprintf("projects/%s/locations/%s/instances/%s", project, location, id),
		redisInstance.Name, "Memorystore's instance lifecycle answers on the same prefix")
	assert.Equal(t, "READY", redisInstance.State)
}
