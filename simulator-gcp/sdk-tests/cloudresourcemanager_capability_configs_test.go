package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	crm "google.golang.org/api/cloudresourcemanager/v3"
)

// Cloud Resource Manager v3 capabilityConfigs.
//
// Google published the collection on all three hierarchy nodes
// (organizations, folders, projects) before the generated Go client acquired a
// call type for it — v0.298.0 carries the operation metadata messages and no
// service — so the methods are driven over the same authenticated OAuth2
// transport the SDK uses, at the wire paths the Discovery document declares.

// capabilityConfig is the cloudresourcemanager#CapabilityConfig wire shape.
type capabilityConfig struct {
	Name              string   `json:"name,omitempty"`
	DisplayName       string   `json:"displayName,omitempty"`
	Types             []string `json:"types,omitempty"`
	Boundaries        []string `json:"boundaries,omitempty"`
	ManagementProject string   `json:"managementProject,omitempty"`
	State             string   `json:"state,omitempty"`
	CreateTime        string   `json:"createTime,omitempty"`
	UpdateTime        string   `json:"updateTime,omitempty"`
	Etag              string   `json:"etag,omitempty"`
}

// capabilityConfigRequest sends one request to the simulator and returns the
// status and body, so a test can assert on a refusal as readily as on a
// success.
func capabilityConfigRequest(t *testing.T, method, uri, body string) (int, string) {
	t.Helper()
	var payload io.Reader
	if body != "" {
		payload = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+uri, payload)
	require.NoError(t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := simAuthHTTPClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(raw)
}

// capabilityConfigFromOperation reads the CapabilityConfig a settled
// long-running Operation carries in its response envelope.
func capabilityConfigFromOperation(t *testing.T, payload string) capabilityConfig {
	t.Helper()
	var op crm.Operation
	require.NoError(t, json.Unmarshal([]byte(payload), &op))
	require.True(t, op.Done, "the operation must settle: %s", payload)
	require.NotEmpty(t, op.Response)
	var cfg capabilityConfig
	require.NoError(t, json.Unmarshal(op.Response, &cfg))
	return cfg
}

func capabilityConfigOf(t *testing.T, payload string) capabilityConfig {
	t.Helper()
	var cfg capabilityConfig
	require.NoError(t, json.Unmarshal([]byte(payload), &cfg))
	return cfg
}

// TestResourceManagerV3_CapabilityConfigsOnAFolder drives the whole collection
// under a folder: create, get, list, patch and delete, plus the refusals that
// make the stored record one the service would have accepted.
func TestResourceManagerV3_CapabilityConfigsOnAFolder(t *testing.T) {
	svc := crmV3Service(t)
	folderOp, err := svc.Folders.Create(&crm.Folder{
		DisplayName: "capability-config-folder",
		Parent:      "organizations/123456789012",
	}).Do()
	require.NoError(t, err)
	folder := crmOpResourceName(t, folderOp)
	require.NotEmpty(t, folder)

	collection := "/v3/" + folder + "/capabilityConfigs"

	// An id that breaks the documented shape is refused before anything is
	// stored: a config the service would never have named must not exist here.
	for _, id := range []string{"", "short", "Capability", "capability-"} {
		status, body := capabilityConfigRequest(t, http.MethodPost,
			collection+"?capabilityConfigId="+id, `{"types":["AGENT_MANAGEMENT"]}`)
		assert.Equal(t, http.StatusBadRequest, status, body)
		assert.Contains(t, body, "INVALID_ARGUMENT")
	}

	// types is required, and only the capabilities the document declares can
	// be enabled.
	status, body := capabilityConfigRequest(t, http.MethodPost,
		collection+"?capabilityConfigId=capcfg-folder", `{"displayName":"Folder capabilities"}`)
	require.Equal(t, http.StatusBadRequest, status, body)
	assert.Contains(t, body, "types")
	status, body = capabilityConfigRequest(t, http.MethodPost,
		collection+"?capabilityConfigId=capcfg-folder", `{"types":["TYPE_UNSPECIFIED"]}`)
	require.Equal(t, http.StatusBadRequest, status, body)
	assert.Contains(t, body, "INVALID_ARGUMENT")

	status, body = capabilityConfigRequest(t, http.MethodPost,
		collection+"?capabilityConfigId=capcfg-folder",
		`{"displayName":"Folder capabilities","types":["AGENT_MANAGEMENT"],"boundaries":["`+folder+`/boundaries/eu"]}`)
	require.Equal(t, http.StatusOK, status, body)
	created := capabilityConfigFromOperation(t, body)
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
	status, body = capabilityConfigRequest(t, http.MethodPost,
		collection+"?capabilityConfigId=capcfg-folder", `{"types":["AGENT_MANAGEMENT"]}`)
	assert.Equal(t, http.StatusConflict, status, body)

	status, body = capabilityConfigRequest(t, http.MethodGet, collection+"/capcfg-folder", "")
	require.Equal(t, http.StatusOK, status, body)
	got := capabilityConfigOf(t, body)
	assert.Equal(t, created.Name, got.Name)
	assert.Equal(t, "Folder capabilities", got.DisplayName)
	assert.Equal(t, []string{folder + "/boundaries/eu"}, got.Boundaries)
	assert.Equal(t, created.ManagementProject, got.ManagementProject)

	status, body = capabilityConfigRequest(t, http.MethodGet, collection, "")
	require.Equal(t, http.StatusOK, status, body)
	var listed struct {
		CapabilityConfigs []capabilityConfig `json:"capabilityConfigs"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &listed))
	require.Len(t, listed.CapabilityConfigs, 1)
	assert.Equal(t, created.Name, listed.CapabilityConfigs[0].Name)

	// A stale etag is refused so the caller re-reads rather than overwriting a
	// change it never saw.
	status, body = capabilityConfigRequest(t, http.MethodPatch,
		collection+"/capcfg-folder?updateMask=displayName",
		`{"displayName":"Stale writer","etag":"`+created.Etag+`stale"}`)
	assert.Equal(t, http.StatusConflict, status, body)

	// Only the three members the method documents can be updated.
	status, body = capabilityConfigRequest(t, http.MethodPatch,
		collection+"/capcfg-folder?updateMask=managementProject",
		`{"managementProject":"projects/1"}`)
	assert.Equal(t, http.StatusBadRequest, status, body)
	assert.Contains(t, body, "update_mask")

	status, body = capabilityConfigRequest(t, http.MethodPatch,
		collection+"/capcfg-folder?updateMask=display_name,types",
		`{"displayName":"Renamed capabilities","types":["AGENT_MANAGEMENT","APP_MANAGEMENT"],"etag":"`+got.Etag+`"}`)
	require.Equal(t, http.StatusOK, status, body)
	patched := capabilityConfigFromOperation(t, body)
	assert.Equal(t, "Renamed capabilities", patched.DisplayName)
	assert.Equal(t, []string{"AGENT_MANAGEMENT", "APP_MANAGEMENT"}, patched.Types)
	assert.NotEqual(t, got.Etag, patched.Etag, "an update issues a new etag")
	// The masked update left the members it did not name alone.
	assert.Equal(t, got.Boundaries, patched.Boundaries)
	assert.Equal(t, got.ManagementProject, patched.ManagementProject)

	// A patch that names no mask writes the whole mutable record, so one that
	// leaves out the required types is refused rather than emptying them.
	status, body = capabilityConfigRequest(t, http.MethodPatch,
		collection+"/capcfg-folder", `{"displayName":"No mask at all"}`)
	require.Equal(t, http.StatusBadRequest, status, body)
	assert.Contains(t, body, "types")
	status, body = capabilityConfigRequest(t, http.MethodGet, collection+"/capcfg-folder", "")
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "Renamed capabilities", capabilityConfigOf(t, body).DisplayName,
		"a refused patch must not have written anything")

	status, body = capabilityConfigRequest(t, http.MethodPatch,
		collection+"/capcfg-folder", `{"displayName":"Whole record","types":["APP_MANAGEMENT"]}`)
	require.Equal(t, http.StatusOK, status, body)
	whole := capabilityConfigFromOperation(t, body)
	assert.Equal(t, "Whole record", whole.DisplayName)
	assert.Equal(t, []string{"APP_MANAGEMENT"}, whole.Types)
	assert.Empty(t, whole.Boundaries, "a maskless patch writes every mutable member")

	status, body = capabilityConfigRequest(t, http.MethodDelete, collection+"/capcfg-folder", "")
	require.Equal(t, http.StatusOK, status, body)
	deleted := capabilityConfigFromOperation(t, body)
	assert.Equal(t, created.Name, deleted.Name)

	status, body = capabilityConfigRequest(t, http.MethodGet, collection+"/capcfg-folder", "")
	assert.Equal(t, http.StatusNotFound, status, body)
	status, body = capabilityConfigRequest(t, http.MethodDelete, collection+"/capcfg-folder", "")
	assert.Equal(t, http.StatusNotFound, status, body)
}

// TestResourceManagerV3_CapabilityConfigsOnAnOrganizationAndProject covers the
// other two parents: the organization spelling, and the project one, which the
// API requires to name its own management project.
func TestResourceManagerV3_CapabilityConfigsOnAnOrganizationAndProject(t *testing.T) {
	svc := crmV3Service(t)
	const organization = "organizations/123456789012"

	status, body := capabilityConfigRequest(t, http.MethodPost,
		"/v3/"+organization+"/capabilityConfigs?capabilityConfigId=capcfg-org",
		`{"displayName":"Org capabilities","types":["APP_MANAGEMENT"]}`)
	require.Equal(t, http.StatusOK, status, body)
	org := capabilityConfigFromOperation(t, body)
	assert.Equal(t, organization+"/capabilityConfigs/capcfg-org", org.Name)

	// A config under a folder of this organization is that folder's, so the
	// organization's own listing does not report it.
	status, body = capabilityConfigRequest(t, http.MethodGet, "/v3/"+organization+"/capabilityConfigs", "")
	require.Equal(t, http.StatusOK, status, body)
	var listed struct {
		CapabilityConfigs []capabilityConfig `json:"capabilityConfigs"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &listed))
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

	// A project-scoped config must name its management project.
	status, body = capabilityConfigRequest(t, http.MethodPost,
		"/v3/"+project+"/capabilityConfigs?capabilityConfigId=capcfg-proj",
		`{"types":["APP_MANAGEMENT"]}`)
	require.Equal(t, http.StatusBadRequest, status, body)
	assert.Contains(t, body, "management_project")

	// And it must name one this deployment holds.
	status, body = capabilityConfigRequest(t, http.MethodPost,
		"/v3/"+project+"/capabilityConfigs?capabilityConfigId=capcfg-proj",
		`{"types":["APP_MANAGEMENT"],"managementProject":"projects/404404404404"}`)
	require.Equal(t, http.StatusBadRequest, status, body)

	status, body = capabilityConfigRequest(t, http.MethodPost,
		"/v3/"+project+"/capabilityConfigs?capabilityConfigId=capcfg-proj",
		`{"types":["APP_MANAGEMENT"],"managementProject":"`+project+`"}`)
	require.Equal(t, http.StatusOK, status, body)
	scoped := capabilityConfigFromOperation(t, body)
	assert.Equal(t, project+"/capabilityConfigs/capcfg-proj", scoped.Name)
	assert.Equal(t, project, scoped.ManagementProject)

	// The project is reachable by its id as well as its number, and both
	// spellings address the one config rather than one config each.
	status, body = capabilityConfigRequest(t, http.MethodGet,
		"/v3/projects/capcfg-scoped-proj/capabilityConfigs/capcfg-proj", "")
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, scoped.Name, capabilityConfigOf(t, body).Name)

	// A parent this deployment does not have is the parent's own refusal, not
	// an empty listing.
	status, body = capabilityConfigRequest(t, http.MethodGet,
		"/v3/folders/404404404404/capabilityConfigs", "")
	assert.Equal(t, http.StatusNotFound, status, body)
	status, body = capabilityConfigRequest(t, http.MethodGet,
		"/v3/projects/no-such-project-here/capabilityConfigs", "")
	assert.Equal(t, http.StatusForbidden, status, body)
}
