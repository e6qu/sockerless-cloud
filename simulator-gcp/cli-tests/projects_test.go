package gcp_cli_test

// `gcloud projects` lifecycle against the simulator's Cloud Resource Manager
// slice. gcloud's projects surface speaks the v1 API — create POSTs the v1
// Project and waits on the returned operation, describe/list/delete/undelete
// use the v1 reads and colon-verbs, and list always sends the
// lifecycleState:ACTIVE filter — riding the
// CLOUDSDK_API_ENDPOINT_OVERRIDES_CLOUDRESOURCEMANAGER coordinate.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectsLifecycleCLI(t *testing.T) {
	const projectID = "cli-proj-lifecycle"

	out := runCLI(t, gcloudCLI("projects", "create", projectID,
		"--name=CLI Lifecycle",
		"--labels=env=cli-test",
		"--organization=123456789012",
		"--format=json",
	))
	require.Contains(t, out, projectID)

	// describe returns the v1 shape: projectNumber, lifecycleState, the
	// display name under `name`, and the organization parent.
	out = runCLI(t, gcloudCLI("projects", "describe", projectID, "--format=json"))
	var described struct {
		ProjectNumber  string            `json:"projectNumber"`
		ProjectID      string            `json:"projectId"`
		LifecycleState string            `json:"lifecycleState"`
		Name           string            `json:"name"`
		Labels         map[string]string `json:"labels"`
		Parent         struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"parent"`
	}
	parseJSONObject(t, out, &described)
	require.Equal(t, projectID, described.ProjectID)
	require.Equal(t, "ACTIVE", described.LifecycleState)
	require.Equal(t, "CLI Lifecycle", described.Name)
	require.NotEmpty(t, described.ProjectNumber)
	require.Equal(t, "cli-test", described.Labels["env"])
	require.Equal(t, "organization", described.Parent.Type)
	require.Equal(t, "123456789012", described.Parent.ID)

	// Duplicate project ID: gcloud surfaces the real 409 as its
	// "already in use" message.
	dupOut, dupErr := gcloudCLI("projects", "create", projectID).CombinedOutput()
	require.Error(t, dupErr, "duplicate create must fail, got: %s", dupOut)
	assert.Contains(t, string(dupOut), "already in use")

	// list (gcloud sends filter=lifecycleState:ACTIVE) includes the project.
	require.Contains(t, cliListProjectIDs(t), projectID)

	// update renames via the v1 read-modify-write PUT.
	out = runCLI(t, gcloudCLI("projects", "update", projectID, "--name=CLI Renamed", "--format=json"))
	require.Contains(t, out, "CLI Renamed")

	// get-iam-policy speaks the v1 colon-verb and returns a real policy etag.
	out = runCLI(t, gcloudCLI("projects", "get-iam-policy", projectID, "--format=json"))
	var policy struct {
		Etag string `json:"etag"`
	}
	parseJSONObject(t, out, &policy)
	require.NotEmpty(t, policy.Etag)

	// delete soft-deletes: DELETE_REQUESTED, still describable, excluded
	// from the ACTIVE list, then undeletable.
	runCLI(t, gcloudCLI("projects", "delete", projectID, "--quiet"))
	out = runCLI(t, gcloudCLI("projects", "describe", projectID, "--format=json"))
	parseJSONObject(t, out, &described)
	require.Equal(t, "DELETE_REQUESTED", described.LifecycleState)
	assert.NotContains(t, cliListProjectIDs(t), projectID, "pending-deletion project must not list as ACTIVE")

	runCLI(t, gcloudCLI("projects", "undelete", projectID, "--quiet"))
	out = runCLI(t, gcloudCLI("projects", "describe", projectID, "--format=json"))
	parseJSONObject(t, out, &described)
	require.Equal(t, "ACTIVE", described.LifecycleState)
}

func TestProjectsDescribeUnknownCLI(t *testing.T) {
	out, err := gcloudCLI("projects", "describe", "cli-never-created-proj").CombinedOutput()
	require.Error(t, err, "describing a project that does not exist must fail, got: %s", out)
	assert.Contains(t, string(out), "does not have permission", "the real API answers 403 PERMISSION_DENIED: %s", out)
}

// cliListProjectIDs runs `gcloud projects list` (which filters to ACTIVE)
// and returns the listed project IDs.
func cliListProjectIDs(t *testing.T) []string {
	t.Helper()
	out := runCLI(t, gcloudCLI("projects", "list", "--format=json"))
	jsonStart := strings.Index(out, "[")
	require.NotEqual(t, -1, jsonStart, "list output not a JSON array: %s", out)
	var listed []struct {
		ProjectID string `json:"projectId"`
	}
	require.NoError(t, json.Unmarshal([]byte(out[jsonStart:]), &listed), "output: %s", out)
	ids := make([]string, 0, len(listed))
	for _, p := range listed {
		ids = append(ids, p.ProjectID)
	}
	return ids
}
