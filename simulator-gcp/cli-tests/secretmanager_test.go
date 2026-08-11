package gcp_cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecretManagerSecretLifecycleCLI(t *testing.T) {
	secretID := "cli-lifecycle-secret"

	out := runCLI(t, gcloudCLI("secrets", "create", secretID,
		"--replication-policy=automatic",
		"--labels=env=dev",
		"--format=json",
	))
	var created struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	}
	parseJSONObject(t, out, &created)
	require.Equal(t, "projects/"+project+"/secrets/"+secretID, created.Name)
	require.Equal(t, map[string]string{"env": "dev"}, created.Labels)

	payload1 := filepath.Join(tmpDir, "secret-v1.txt")
	require.NoError(t, os.WriteFile(payload1, []byte("first"), 0o600))
	out = runCLI(t, gcloudCLI("secrets", "versions", "add", secretID,
		"--data-file="+payload1,
		"--format=json",
	))
	var v1 struct {
		Name string `json:"name"`
	}
	parseJSONObject(t, out, &v1)
	require.Equal(t, created.Name+"/versions/1", v1.Name)

	payload2 := filepath.Join(tmpDir, "secret-v2.txt")
	require.NoError(t, os.WriteFile(payload2, []byte("second"), 0o600))
	out = runCLI(t, gcloudCLI("secrets", "versions", "add", secretID,
		"--data-file="+payload2,
		"--format=json",
	))
	var v2 struct {
		Name string `json:"name"`
	}
	parseJSONObject(t, out, &v2)
	require.Equal(t, created.Name+"/versions/2", v2.Name)

	out = runCLI(t, gcloudCLI("secrets", "versions", "list", secretID, "--format=json"))
	jsonStart := strings.Index(out, "[")
	require.NotEqual(t, -1, jsonStart, "gcloud output did not contain JSON array: %s", out)
	var versions []struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal([]byte(out[jsonStart:]), &versions), "gcloud output: %s", out)
	require.Len(t, versions, 2)
	require.Equal(t, created.Name+"/versions/2", versions[0].Name)
	require.Equal(t, created.Name+"/versions/1", versions[1].Name)

	out = runCLI(t, gcloudCLI("secrets", "update", secretID,
		"--update-labels=env=prod,owner=cli",
		"--format=json",
	))
	var updated struct {
		Labels map[string]string `json:"labels"`
	}
	parseJSONObject(t, out, &updated)
	require.Equal(t, map[string]string{"env": "prod", "owner": "cli"}, updated.Labels)

	runCLI(t, gcloudCLI("secrets", "delete", secretID, "--quiet"))
}

// TestSecretManagerIamPolicyCLI exercises the secret IAM policy surface via
// gcloud: add-iam-policy-binding (setIamPolicy after a getIamPolicy read)
// then get-iam-policy reads the binding back.
func TestSecretManagerIamPolicyCLI(t *testing.T) {
	secretID := "cli-iam-secret"

	runCLI(t, gcloudCLI("secrets", "create", secretID,
		"--replication-policy=automatic",
		"--format=json",
	))
	t.Cleanup(func() {
		runCLI(t, gcloudCLI("secrets", "delete", secretID, "--quiet"))
	})

	// add-iam-policy-binding does a get→modify→set round-trip under the hood.
	out := runCLI(t, gcloudCLI("secrets", "add-iam-policy-binding", secretID,
		"--member=user:alice@example.com",
		"--role=roles/secretmanager.secretAccessor",
		"--format=json",
	))
	var policy struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
		Etag string `json:"etag"`
	}
	parseJSONObject(t, out, &policy)
	require.Len(t, policy.Bindings, 1)
	require.Equal(t, "roles/secretmanager.secretAccessor", policy.Bindings[0].Role)
	require.Contains(t, policy.Bindings[0].Members, "user:alice@example.com")

	// get-iam-policy reads the persisted binding back.
	out = runCLI(t, gcloudCLI("secrets", "get-iam-policy", secretID, "--format=json"))
	var got struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
	}
	parseJSONObject(t, out, &got)
	require.Len(t, got.Bindings, 1)
	require.Contains(t, got.Bindings[0].Members, "user:alice@example.com")
}

func parseJSONObject(t *testing.T, out string, target any) {
	t.Helper()
	jsonStart := strings.Index(out, "{")
	require.NotEqual(t, -1, jsonStart, "gcloud output did not contain JSON object: %s", out)
	require.NoError(t, json.Unmarshal([]byte(out[jsonStart:]), target), "gcloud output: %s", out)
}
