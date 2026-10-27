package gcp_sdk_test

import (
	"testing"

	cloudbuild "google.golang.org/api/cloudbuild/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudBuild_TriggerPatchUpdateMask: a PATCH with updateMask=description
// changes only the description and keeps every unmasked field, even one the
// body sends a different value for; Terraform drifts otherwise.
func TestCloudBuild_TriggerPatchUpdateMask(t *testing.T) {
	t.Parallel()
	svc := cloudbuildService(t)
	project := "cb-mask-project"

	created, err := svc.Projects.Triggers.Create(project, &cloudbuild.BuildTrigger{
		Name:        "mask-trig",
		Description: "d1",
		Filename:    "a.yaml",
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, created.Id)

	_, err = svc.Projects.Triggers.Patch(project, created.Id, &cloudbuild.BuildTrigger{
		Description: "d2",
		Filename:    "b.yaml",
	}).UpdateMask("description").Do()
	require.NoError(t, err)

	got, err := svc.Projects.Triggers.Get(project, created.Id).Do()
	require.NoError(t, err)
	assert.Equal(t, "d2", got.Description, "masked field (description) updated")
	assert.Equal(t, "a.yaml", got.Filename, "unmasked field (filename) preserved despite b.yaml in the body")
}
