package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	dataflow "google.golang.org/api/dataflow/v1b3"
	"google.golang.org/api/option"
)

func dataflowService(t *testing.T) *dataflow.Service {
	t.Helper()
	svc, err := dataflow.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	return svc
}

// A config store setting is a named value set at a project, folder or
// organization. They inherit down the hierarchy, and resolve is where a caller
// asks which one actually applies.
func TestDataflow_ConfigStoreSettingsAreResolvedWhereTheyAreSet(t *testing.T) {
	svc := dataflowService(t)
	const location, id = "us-central1", "max-workers"
	const project, folder, org = "settings-project", "1234", "5678"

	projectParent := "projects/" + project + "/locations/" + location
	folderParent := "folders/" + folder + "/locations/" + location
	orgParent := "organizations/" + org + "/locations/" + location

	// The organization sets it first, then the folder, then the project.
	for _, set := range []struct{ parent, value string }{
		{orgParent, "10"}, {folderParent, "20"}, {projectParent, "30"},
	} {
		created, err := svc.Projects.Locations.ConfigStoreSettings.Create(set.parent,
			&dataflow.ConfigStoreSetting{
				Name:  set.parent + "/configStoreSettings/" + id,
				Value: &dataflow.ConfigStoreSettingValue{StringValue: set.value},
			}).Do()
		require.NoError(t, err, "creating the setting under %s", set.parent)
		require.NotNil(t, created.Value)
		assert.Equal(t, set.value, created.Value.StringValue)
	}

	got, err := svc.Projects.Locations.ConfigStoreSettings.Get(
		projectParent + "/configStoreSettings/" + id).Do()
	require.NoError(t, err)
	require.NotNil(t, got.Value)
	assert.Equal(t, "30", got.Value.StringValue)

	listed, err := svc.Projects.Locations.ConfigStoreSettings.List(projectParent).Do()
	require.NoError(t, err)
	require.Len(t, listed.ConfigStoreSettings, 1, "a parent lists only its own settings")

	// resolve answers for the resource it is asked about. Each level has its
	// own setting, and each resolves to its own — the request names one
	// resource, and what is above it is not something this API carries.
	for _, want := range []struct{ parent, value string }{
		{projectParent, "30"}, {folderParent, "20"}, {orgParent, "10"},
	} {
		resolved, err := svc.Projects.Locations.ConfigStoreSettings.Resolve(
			want.parent+"/configStoreSettings/"+id,
			&dataflow.ResolveConfigStoreSettingRequest{}).Do()
		require.NoError(t, err, "resolving under %s", want.parent)
		require.NotNil(t, resolved.Setting)
		assert.Equal(t, want.value, resolved.Setting.Value.StringValue)
		assert.Len(t, resolved.Choices, 1)
	}

	// Deleting one leaves it resolving nowhere, rather than falling back to a
	// parent the request never named.
	_, err = svc.Projects.Locations.ConfigStoreSettings.Delete(
		projectParent + "/configStoreSettings/" + id).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Locations.ConfigStoreSettings.Resolve(
		projectParent+"/configStoreSettings/"+id,
		&dataflow.ResolveConfigStoreSettingRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "applies here")

	// A setting nobody set applies nowhere either.
	_, err = svc.Projects.Locations.ConfigStoreSettings.Resolve(
		projectParent+"/configStoreSettings/never-set",
		&dataflow.ResolveConfigStoreSettingRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "applies here")

	// The same setting cannot be created twice under one parent.
	_, err = svc.Projects.Locations.ConfigStoreSettings.Create(folderParent,
		&dataflow.ConfigStoreSetting{
			Name:  folderParent + "/configStoreSettings/" + id,
			Value: &dataflow.ConfigStoreSettingValue{StringValue: "99"},
		}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}
