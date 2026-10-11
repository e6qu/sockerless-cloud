package gcp_sdk_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	crm "google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/googleapi"
)

// settledCapabilityConfig reads the CapabilityConfig a settled long-running
// Operation carries in its response envelope, taking a call's two results.
func settledCapabilityConfig(t *testing.T) func(*crm.Operation, error) *crm.CapabilityConfig {
	return func(op *crm.Operation, err error) *crm.CapabilityConfig {
		t.Helper()
		require.NoError(t, err)
		require.True(t, op.Done, "the operation must settle")
		require.NotEmpty(t, op.Response)
		var cfg crm.CapabilityConfig
		require.NoError(t, json.Unmarshal(op.Response, &cfg))
		return &cfg
	}
}

// requireCapabilityConfigRefusal asserts a call failed with the HTTP status
// and, when contains is not empty, a message naming it.
func requireCapabilityConfigRefusal(t *testing.T, err error, status int, contains string) {
	t.Helper()
	var gerr *googleapi.Error
	require.True(t, errors.As(err, &gerr), "expected a googleapi.Error, got %T: %v", err, err)
	assert.Equal(t, status, gerr.Code, gerr.Error())
	if contains != "" {
		assert.Contains(t, gerr.Body, contains)
	}
}

// TestResourceManagerV3_CapabilityConfigsOnAFolder drives the whole collection
// under a folder: create, get, list, patch and delete, plus the refusals that
// make the stored record one the service would have accepted.
func TestResourceManagerV3_CapabilityConfigsOnAFolder(t *testing.T) {
	t.Parallel()
	svc := crmV3Service(t)
	folderOp, err := svc.Folders.Create(&crm.Folder{
		DisplayName: "capability-config-folder",
		Parent:      "organizations/123456789012",
	}).Do()
	require.NoError(t, err)
	folder := crmOpResourceName(t, folderOp)
	require.NotEmpty(t, folder)

	configs := svc.Folders.CapabilityConfigs
	name := folder + "/capabilityConfigs/capcfg-folder"

	// An id that breaks the documented shape is refused before anything is
	// stored: a config the service would never have named must not exist here.
	for _, id := range []string{"", "short", "Capability", "capability-"} {
		_, err := configs.Create(folder, &crm.CapabilityConfig{Types: []string{"AGENT_MANAGEMENT"}}).CapabilityConfigId(id).Do()
		requireCapabilityConfigRefusal(t, err, 400, "INVALID_ARGUMENT")
	}

	// types is required, and only the capabilities the document declares can
	// be enabled.
	_, err = configs.Create(folder, &crm.CapabilityConfig{DisplayName: "Folder capabilities"}).CapabilityConfigId("capcfg-folder").Do()
	requireCapabilityConfigRefusal(t, err, 400, "types")
	_, err = configs.Create(folder, &crm.CapabilityConfig{Types: []string{"TYPE_UNSPECIFIED"}}).CapabilityConfigId("capcfg-folder").Do()
	requireCapabilityConfigRefusal(t, err, 400, "INVALID_ARGUMENT")

	created := settledCapabilityConfig(t)(configs.Create(folder, &crm.CapabilityConfig{
		DisplayName: "Folder capabilities",
		Types:       []string{"AGENT_MANAGEMENT"},
		Boundaries:  []string{folder + "/boundaries/eu"},
	}).CapabilityConfigId("capcfg-folder").Do())
	assert.Equal(t, folder+"/capabilityConfigs/capcfg-folder", created.Name)
	assert.Equal(t, "ACTIVE", created.State)
	assert.Equal(t, []string{"AGENT_MANAGEMENT"}, created.Types)
	assert.NotEmpty(t, created.Etag)

	// The management project the create was not given is one Cloud Resource
	// Manager made, and it is a project this deployment holds — not a name
	// pointing at nothing.
	require.NotEmpty(t, created.ManagementProject)
	management, err := svc.Projects.Get(created.ManagementProject).Do()
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", management.State)
	assert.Equal(t, folder, management.Parent)

	// Creating it twice is a conflict, not a second config.
	_, err = configs.Create(folder, &crm.CapabilityConfig{Types: []string{"AGENT_MANAGEMENT"}}).CapabilityConfigId("capcfg-folder").Do()
	requireCapabilityConfigRefusal(t, err, 409, "")

	got, err := configs.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, created.Name, got.Name)
	assert.Equal(t, "Folder capabilities", got.DisplayName)
	assert.Equal(t, []string{folder + "/boundaries/eu"}, got.Boundaries)
	assert.Equal(t, created.ManagementProject, got.ManagementProject)

	listed, err := configs.List(folder).Do()
	require.NoError(t, err)
	require.Len(t, listed.CapabilityConfigs, 1)
	assert.Equal(t, created.Name, listed.CapabilityConfigs[0].Name)

	// A stale etag is refused so the caller re-reads rather than overwriting a
	// change it never saw.
	_, err = configs.Patch(name, &crm.CapabilityConfig{DisplayName: "Stale writer", Etag: created.Etag + "stale"}).UpdateMask("displayName").Do()
	requireCapabilityConfigRefusal(t, err, 409, "")

	// Only the three members the method documents can be updated.
	_, err = configs.Patch(name, &crm.CapabilityConfig{ManagementProject: "projects/1"}).UpdateMask("managementProject").Do()
	requireCapabilityConfigRefusal(t, err, 400, "update_mask")

	patched := settledCapabilityConfig(t)(configs.Patch(name, &crm.CapabilityConfig{
		DisplayName: "Renamed capabilities",
		Types:       []string{"AGENT_MANAGEMENT", "APP_MANAGEMENT"},
		Etag:        got.Etag,
	}).UpdateMask("display_name,types").Do())
	assert.Equal(t, "Renamed capabilities", patched.DisplayName)
	assert.Equal(t, []string{"AGENT_MANAGEMENT", "APP_MANAGEMENT"}, patched.Types)
	assert.NotEqual(t, got.Etag, patched.Etag, "an update issues a new etag")
	// The masked update left the members it did not name alone.
	assert.Equal(t, got.Boundaries, patched.Boundaries)
	assert.Equal(t, got.ManagementProject, patched.ManagementProject)

	// A patch that names no mask writes the whole mutable record, so one that
	// leaves out the required types is refused rather than emptying them.
	_, err = configs.Patch(name, &crm.CapabilityConfig{DisplayName: "No mask at all"}).Do()
	requireCapabilityConfigRefusal(t, err, 400, "types")
	unchanged, err := configs.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "Renamed capabilities", unchanged.DisplayName,
		"a refused patch must not have written anything")

	whole := settledCapabilityConfig(t)(configs.Patch(name, &crm.CapabilityConfig{
		DisplayName: "Whole record",
		Types:       []string{"APP_MANAGEMENT"},
	}).Do())
	assert.Equal(t, "Whole record", whole.DisplayName)
	assert.Equal(t, []string{"APP_MANAGEMENT"}, whole.Types)
	assert.Empty(t, whole.Boundaries, "a maskless patch writes every mutable member")

	deleted := settledCapabilityConfig(t)(configs.Delete(name).Do())
	assert.Equal(t, created.Name, deleted.Name)

	_, err = configs.Get(name).Do()
	requireCapabilityConfigRefusal(t, err, 404, "")
	_, err = configs.Delete(name).Do()
	requireCapabilityConfigRefusal(t, err, 404, "")
}

// TestResourceManagerV3_CapabilityConfigsOnAnOrganizationAndProject covers the
// other two parents: the organization spelling, and the project one, which the
// API requires to name its own management project.
func TestResourceManagerV3_CapabilityConfigsOnAnOrganizationAndProject(t *testing.T) {
	t.Parallel()
	svc := crmV3Service(t)
	const organization = "organizations/123456789012"

	org := settledCapabilityConfig(t)(svc.Organizations.CapabilityConfigs.Create(organization, &crm.CapabilityConfig{
		DisplayName: "Org capabilities",
		Types:       []string{"APP_MANAGEMENT"},
	}).CapabilityConfigId("capcfg-org").Do())
	assert.Equal(t, organization+"/capabilityConfigs/capcfg-org", org.Name)

	// A config under a folder of this organization is that folder's, so the
	// organization's own listing does not report it.
	listed, err := svc.Organizations.CapabilityConfigs.List(organization).Do()
	require.NoError(t, err)
	for _, cfg := range listed.CapabilityConfigs {
		assert.True(t, strings.HasPrefix(cfg.Name, organization+"/capabilityConfigs/"),
			"the organization listed %s, which is not its direct child", cfg.Name)
	}

	projectOp, err := svc.Projects.Create(&crm.Project{
		ProjectId:   "capcfg-scoped-proj",
		DisplayName: "Capability config project",
		Parent:      organization,
	}).Do()
	require.NoError(t, err)
	project := crmOpResourceName(t, projectOp)
	require.NotEmpty(t, project)

	projectConfigs := svc.Projects.CapabilityConfigs

	// A project-scoped config must name its management project.
	_, err = projectConfigs.Create(project, &crm.CapabilityConfig{Types: []string{"APP_MANAGEMENT"}}).CapabilityConfigId("capcfg-proj").Do()
	requireCapabilityConfigRefusal(t, err, 400, "management_project")

	// And it must name one this deployment holds.
	_, err = projectConfigs.Create(project, &crm.CapabilityConfig{
		Types:             []string{"APP_MANAGEMENT"},
		ManagementProject: "projects/404404404404",
	}).CapabilityConfigId("capcfg-proj").Do()
	requireCapabilityConfigRefusal(t, err, 400, "")

	scoped := settledCapabilityConfig(t)(projectConfigs.Create(project, &crm.CapabilityConfig{
		Types:             []string{"APP_MANAGEMENT"},
		ManagementProject: project,
	}).CapabilityConfigId("capcfg-proj").Do())
	assert.Equal(t, project+"/capabilityConfigs/capcfg-proj", scoped.Name)
	assert.Equal(t, project, scoped.ManagementProject)

	// The project is reachable by its id as well as its number, and both
	// spellings address the one config rather than one config each.
	byID, err := projectConfigs.Get("projects/capcfg-scoped-proj/capabilityConfigs/capcfg-proj").Do()
	require.NoError(t, err)
	assert.Equal(t, scoped.Name, byID.Name)

	// A parent this deployment does not have is the parent's own refusal, not
	// an empty listing.
	_, err = svc.Folders.CapabilityConfigs.List("folders/404404404404").Do()
	requireCapabilityConfigRefusal(t, err, 404, "")
	_, err = projectConfigs.List("projects/no-such-project-here").Do()
	requireCapabilityConfigRefusal(t, err, 403, "")
}
