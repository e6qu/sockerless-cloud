package gcp_cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An image's IAM policy belongs to an image the project holds; a policy verb
// on an image never created answers the 404 images.get answers.
func TestComputeImageIAMPolicyCLI(t *testing.T) {
	t.Parallel()
	const name = "cli-image-with-policy"
	runCLI(t, gcloudCLI("compute", "images", "create", name,
		"--source-image-family="+debianFamily(), "--source-image-project=debian-cloud"))
	defer runCLI(t, gcloudCLI("compute", "images", "delete", name, "--quiet"))

	runCLI(t, gcloudCLI("compute", "images", "add-iam-policy-binding", name,
		"--member=user:reader@example.com", "--role=roles/compute.imageUser"))
	var policy struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t,
		gcloudCLI("compute", "images", "get-iam-policy", name, "--format=json"))), &policy))
	require.Len(t, policy.Bindings, 1)
	assert.Equal(t, "roles/compute.imageUser", policy.Bindings[0].Role)
	assert.Equal(t, []string{"user:reader@example.com"}, policy.Bindings[0].Members)

	missingGet := gcloudCLIFails(t, gcloudCLI("compute", "images", "describe", "cli-image-never-created"))
	assert.Contains(t, missingGet, "projects/test-project/global/images/cli-image-never-created' was not found")
	missingPolicy := gcloudCLIFails(t, gcloudCLI("compute", "images", "get-iam-policy", "cli-image-never-created"))
	assert.Contains(t, missingPolicy, "projects/test-project/global/images/cli-image-never-created' was not found")
	missingBinding := gcloudCLIFails(t, gcloudCLI("compute", "images", "add-iam-policy-binding", "cli-image-never-created",
		"--member=user:reader@example.com", "--role=roles/compute.imageUser"))
	assert.Contains(t, missingBinding, "was not found")
}
