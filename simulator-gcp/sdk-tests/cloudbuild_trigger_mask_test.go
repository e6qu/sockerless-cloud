package gcp_sdk_test

import (
	"testing"

	cloudbuild "google.golang.org/api/cloudbuild/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudBuild_TriggerPatchUpdateMask pins triggers.patch field-mask
// semantics: a PATCH with updateMask=description updates only the description
// and preserves every unmasked field (e.g. filename), even when the request
// body carries a different value for it. Before the fix the handler did a
// wholesale replace, dropping unmasked fields → terraform drift.
func TestCloudBuild_TriggerPatchUpdateMask(t *testing.T) {
	svc := cloudbuildService(t)
	project := "cb-mask-project"

	created, err := svc.Projects.Triggers.Create(project, &cloudbuild.BuildTrigger{
		Name:        "mask-trig",
		Description: "d1",
		Filename:    "a.yaml",
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, created.Id)

	// updateMask=description: change description AND send a different filename
	// that must be ignored because filename is not in the mask.
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
