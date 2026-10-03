package gcp_cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Object contexts through gcloud: `storage cp --custom-contexts` attaches them
// on upload, `storage objects update` replaces, updates, removes and clears
// them, `storage objects list --metadata-filter` selects objects by them, and
// a copy keeps them. `--clear-custom-contexts` on `objects update` sends an
// empty custom map, which Cloud Storage's patch treats as no change, so the
// test removes the last context by name.
func TestGCSCLI_ObjectContexts(t *testing.T) {
	const bucket = "cli-contexts-bucket"
	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--location=us"))
	t.Cleanup(func() { runCLI(t, gcloudCLI("storage", "rm", "--recursive", "gs://"+bucket)) })
	source := filepath.Join(t.TempDir(), "report.txt")
	require.NoError(t, os.WriteFile(source, []byte("quarterly"), 0o644))
	object := "gs://" + bucket + "/report.txt"

	contexts := func() map[string]string {
		var described struct {
			Contexts map[string]struct {
				Value string `json:"value"`
				Type  string `json:"type"`
			} `json:"contexts"`
		}
		require.NoError(t, json.Unmarshal([]byte(runCLI(t, gcloudCLI("storage", "objects", "describe", object, "--format=json"))), &described))
		values := map[string]string{}
		for key, payload := range described.Contexts {
			assert.Equal(t, "CUSTOM", payload.Type, "context %s", key)
			values[key] = payload.Value
		}
		return values
	}

	runCLI(t, gcloudCLI("storage", "cp", source, object, "--custom-contexts=team=data,tier=gold"))
	assert.Equal(t, map[string]string{"team": "data", "tier": "gold"}, contexts())

	runCLI(t, gcloudCLI("storage", "cp", source, "gs://"+bucket+"/draft.txt", "--custom-contexts=tier=bronze"))
	listed := runCLI(t, gcloudCLI("storage", "objects", "list", "gs://"+bucket,
		`--metadata-filter=contexts."tier"="gold"`, "--format=value(name)"))
	assert.Equal(t, []string{"report.txt"}, strings.Fields(listed), "the filter selects the object whose context matches")

	runCLI(t, gcloudCLI("storage", "objects", "update", object,
		"--update-custom-contexts=tier=silver,owner=ops", "--remove-custom-contexts=team"))
	assert.Equal(t, map[string]string{"tier": "silver", "owner": "ops"}, contexts())

	runCLI(t, gcloudCLI("storage", "objects", "update", object, "--custom-contexts=stage=final"))
	assert.Equal(t, map[string]string{"stage": "final"}, contexts(), "--custom-contexts replaces every context")

	runCLI(t, gcloudCLI("storage", "cp", object, "gs://"+bucket+"/copy.txt"))
	copied := runCLI(t, gcloudCLI("storage", "objects", "describe", "gs://"+bucket+"/copy.txt", "--format=value(contexts.stage.value)"))
	assert.Equal(t, "final", strings.TrimSpace(copied), "a copy keeps the source's contexts")

	runCLI(t, gcloudCLI("storage", "objects", "update", object, "--remove-custom-contexts=stage"))
	assert.Empty(t, contexts())
}
