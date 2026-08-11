package gcp_cli_test

// `gcloud resource-manager org-policies`, `gcloud organizations` and
// `gcloud projects get-ancestors` against the simulator's Cloud Resource
// Manager slice. All three speak the v1 API — the org-policy verbs are POST
// colon-methods on the hierarchy node, organizations list/describe are
// SearchOrganizations and organizations.get, and get-ancestors is
// projects.getAncestry — riding the
// CLOUDSDK_API_ENDPOINT_OVERRIDES_CLOUDRESOURCEMANAGER coordinate.
//
// Routes exercised: POST /v1/projects/{projectAction},
// POST /v1/folders/{folderAction}, POST /v1/organizations/{orgAction},
// GET /v1/organizations/{org}, POST /v1/organizations:search,
// POST /v1/liens, GET /v1/liens, DELETE /v1/liens/{lien}.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	cliOrgPolicyBoolean = "constraints/iam.disableServiceAccountKeyCreation"
	cliOrgPolicyList    = "constraints/gcp.resourceLocations"
	cliOrganizationID   = "123456789012"
)

// cliV2Folder is the Cloud Resource Manager v2 Folder shape gcloud prints.
type cliV2Folder struct {
	Name           string `json:"name"`
	Parent         string `json:"parent"`
	DisplayName    string `json:"displayName"`
	LifecycleState string `json:"lifecycleState"`
	CreateTime     string `json:"createTime"`
}

type cliOrgPolicy struct {
	Constraint    string `json:"constraint"`
	Etag          string `json:"etag"`
	BooleanPolicy *struct {
		Enforced bool `json:"enforced"`
	} `json:"booleanPolicy"`
	ListPolicy *struct {
		AllowedValues []string `json:"allowedValues"`
		DeniedValues  []string `json:"deniedValues"`
		AllValues     string   `json:"allValues"`
	} `json:"listPolicy"`
}

func TestOrgPoliciesProjectScopeCLI(t *testing.T) {
	const projectID = "cli-orgpol-project"
	runCLI(t, gcloudCLI("projects", "create", projectID,
		"--organization="+cliOrganizationID, "--format=json"))

	// enable-enforce sets a boolean policy through setOrgPolicy.
	out := runCLI(t, gcloudCLI("resource-manager", "org-policies", "enable-enforce",
		cliOrgPolicyBoolean, "--project="+projectID, "--format=json"))
	var enforced cliOrgPolicy
	parseJSONObject(t, out, &enforced)
	assert.Equal(t, cliOrgPolicyBoolean, enforced.Constraint)
	require.NotNil(t, enforced.BooleanPolicy)
	assert.True(t, enforced.BooleanPolicy.Enforced)
	assert.NotEmpty(t, enforced.Etag)

	// describe reads it back through getOrgPolicy.
	out = runCLI(t, gcloudCLI("resource-manager", "org-policies", "describe",
		cliOrgPolicyBoolean, "--project="+projectID, "--format=json"))
	var described cliOrgPolicy
	parseJSONObject(t, out, &described)
	require.NotNil(t, described.BooleanPolicy)
	assert.True(t, described.BooleanPolicy.Enforced)

	// --effective reads getEffectiveOrgPolicy, which resolves the hierarchy.
	out = runCLI(t, gcloudCLI("resource-manager", "org-policies", "describe",
		cliOrgPolicyBoolean, "--project="+projectID, "--effective", "--format=json"))
	var effective cliOrgPolicy
	parseJSONObject(t, out, &effective)
	require.NotNil(t, effective.BooleanPolicy)
	assert.True(t, effective.BooleanPolicy.Enforced)

	// list shows the policies set on the resource.
	out = runCLI(t, gcloudCLI("resource-manager", "org-policies", "list",
		"--project="+projectID, "--format=json"))
	assert.Contains(t, out, cliOrgPolicyBoolean)

	// --show-unset lists the available constraints through
	// listAvailableOrgPolicyConstraints.
	out = runCLI(t, gcloudCLI("resource-manager", "org-policies", "list",
		"--project="+projectID, "--show-unset", "--format=json"))
	assert.Contains(t, out, cliOrgPolicyList,
		"the constraint catalog includes every constraint the slice recognizes")

	// disable-enforce flips the stored policy.
	out = runCLI(t, gcloudCLI("resource-manager", "org-policies", "disable-enforce",
		cliOrgPolicyBoolean, "--project="+projectID, "--format=json"))
	var disabled cliOrgPolicy
	parseJSONObject(t, out, &disabled)
	require.NotNil(t, disabled.BooleanPolicy)
	assert.False(t, disabled.BooleanPolicy.Enforced)

	// allow / deny build a list policy on a list constraint.
	out = runCLI(t, gcloudCLI("resource-manager", "org-policies", "allow",
		cliOrgPolicyList, "in:us-locations", "--project="+projectID, "--format=json"))
	var allowed cliOrgPolicy
	parseJSONObject(t, out, &allowed)
	require.NotNil(t, allowed.ListPolicy)
	assert.Contains(t, allowed.ListPolicy.AllowedValues, "in:us-locations")

	// delete clears the policy; the constraint then reads back unset.
	runCLI(t, gcloudCLI("resource-manager", "org-policies", "delete",
		cliOrgPolicyBoolean, "--project="+projectID))
	out = runCLI(t, gcloudCLI("resource-manager", "org-policies", "describe",
		cliOrgPolicyBoolean, "--project="+projectID, "--format=json"))
	var cleared cliOrgPolicy
	parseJSONObject(t, out, &cleared)
	assert.Nil(t, cleared.BooleanPolicy, "a cleared policy reads back empty: %s", out)

	// An unrecognized constraint is rejected, so the catalog and the accepted
	// set are one set.
	badOut, badErr := gcloudCLI("resource-manager", "org-policies", "enable-enforce",
		"constraints/not.aRealConstraint", "--project="+projectID).CombinedOutput()
	require.Error(t, badErr, "unknown constraint must fail: %s", badOut)
	assert.Contains(t, string(badOut), "not recognized")
}

func TestOrgPoliciesOrganizationAndFolderScopeCLI(t *testing.T) {
	// Organization scope.
	out := runCLI(t, gcloudCLI("resource-manager", "org-policies", "enable-enforce",
		cliOrgPolicyBoolean, "--organization="+cliOrganizationID, "--format=json"))
	var orgPolicy cliOrgPolicy
	parseJSONObject(t, out, &orgPolicy)
	require.NotNil(t, orgPolicy.BooleanPolicy)
	assert.True(t, orgPolicy.BooleanPolicy.Enforced)
	t.Cleanup(func() {
		_, _ = gcloudCLI("resource-manager", "org-policies", "delete",
			cliOrgPolicyBoolean, "--organization="+cliOrganizationID).CombinedOutput()
	})

	// Folder scope. The folder itself is created over v2, the version
	// gcloud's `resource-manager folders` group speaks, and the org-policy
	// verbs address the same folder over v1.
	out = runCLI(t, gcloudCLI("resource-manager", "folders", "create",
		"--display-name=cli-orgpol-folder", "--organization="+cliOrganizationID, "--format=json"))
	var folder cliV2Folder
	parseJSONObject(t, out, &folder)
	require.NotEmpty(t, folder.Name, "folder create returned no resource: %s", out)
	folderID := strings.TrimPrefix(folder.Name, "folders/")

	out = runCLI(t, gcloudCLI("resource-manager", "org-policies", "enable-enforce",
		cliOrgPolicyBoolean, "--folder="+folderID, "--format=json"))
	var folderPolicy cliOrgPolicy
	parseJSONObject(t, out, &folderPolicy)
	require.NotNil(t, folderPolicy.BooleanPolicy)
	assert.True(t, folderPolicy.BooleanPolicy.Enforced)

	out = runCLI(t, gcloudCLI("resource-manager", "org-policies", "list",
		"--folder="+folderID, "--format=json"))
	assert.Contains(t, out, cliOrgPolicyBoolean)

	runCLI(t, gcloudCLI("resource-manager", "org-policies", "delete",
		cliOrgPolicyBoolean, "--folder="+folderID))
}

// TestFoldersV2CLI drives the Cloud Resource Manager v2 folders collection —
// the wire `gcloud resource-manager folders` speaks. Routes exercised:
// POST /v2/folders, GET /v2/folders, GET /v2/folders/{folder},
// PATCH /v2/folders/{folder}, DELETE /v2/folders/{folder},
// POST /v2/folders:search, POST /v2/folders/{folderAction}.
func TestFoldersV2CLI(t *testing.T) {
	out := runCLI(t, gcloudCLI("resource-manager", "folders", "create",
		"--display-name=cli-folders-v2", "--organization="+cliOrganizationID, "--format=json"))
	// gcloud settles the create operation and prints the folder the response
	// envelope carries.
	var created cliV2Folder
	parseJSONObject(t, out, &created)
	folderName := created.Name
	require.NotEmpty(t, folderName, "output: %s", out)
	assert.Equal(t, "cli-folders-v2", created.DisplayName)
	assert.Equal(t, "organizations/"+cliOrganizationID, created.Parent)
	assert.Equal(t, "ACTIVE", created.LifecycleState)
	folderID := strings.TrimPrefix(folderName, "folders/")

	out = runCLI(t, gcloudCLI("resource-manager", "folders", "describe", folderID, "--format=json"))
	var described cliV2Folder
	parseJSONObject(t, out, &described)
	assert.Equal(t, folderName, described.Name)
	assert.Equal(t, "cli-folders-v2", described.DisplayName)

	out = runCLI(t, gcloudCLI("resource-manager", "folders", "list",
		"--organization="+cliOrganizationID, "--format=json"))
	assert.Contains(t, out, folderName)

	// patch is an immediate write returning the Folder, not a long-running
	// operation.
	runCLI(t, gcloudCLI("resource-manager", "folders", "update", folderID,
		"--display-name=cli-folders-v2-renamed"))
	out = runCLI(t, gcloudCLI("resource-manager", "folders", "describe", folderID, "--format=json"))
	var renamed cliV2Folder
	parseJSONObject(t, out, &renamed)
	assert.Equal(t, folderName, renamed.Name)
	assert.Equal(t, "cli-folders-v2-renamed", renamed.DisplayName)

	// delete moves the folder into DELETE_REQUESTED; undelete brings it back.
	runCLI(t, gcloudCLI("resource-manager", "folders", "delete", folderID, "--format=json"))
	out = runCLI(t, gcloudCLI("resource-manager", "folders", "describe", folderID, "--format=json"))
	assert.Contains(t, out, "DELETE_REQUESTED")
	runCLI(t, gcloudCLI("resource-manager", "folders", "undelete", folderID, "--format=json"))
	out = runCLI(t, gcloudCLI("resource-manager", "folders", "describe", folderID, "--format=json"))
	assert.Contains(t, out, "ACTIVE")

	// The IAM triple is served on the v2 folder as well.
	runCLI(t, gcloudCLI("resource-manager", "folders", "add-iam-policy-binding", folderID,
		"--member=user:folder-admin@example.com", "--role=roles/resourcemanager.folderAdmin", "--format=json"))
	out = runCLI(t, gcloudCLI("resource-manager", "folders", "get-iam-policy", folderID, "--format=json"))
	assert.Contains(t, out, "roles/resourcemanager.folderAdmin")
}

func TestOrganizationsAndAncestryCLI(t *testing.T) {
	out := runCLI(t, gcloudCLI("organizations", "describe", cliOrganizationID, "--format=json"))
	var org struct {
		Name           string `json:"name"`
		DisplayName    string `json:"displayName"`
		LifecycleState string `json:"lifecycleState"`
		Owner          struct {
			DirectoryCustomerId string `json:"directoryCustomerId"`
		} `json:"owner"`
	}
	parseJSONObject(t, out, &org)
	assert.Equal(t, "organizations/"+cliOrganizationID, org.Name)
	assert.Equal(t, "example.com", org.DisplayName)
	assert.Equal(t, "ACTIVE", org.LifecycleState)
	assert.NotEmpty(t, org.Owner.DirectoryCustomerId)

	out = runCLI(t, gcloudCLI("organizations", "list", "--format=json"))
	assert.Contains(t, out, "organizations/"+cliOrganizationID)

	const projectID = "cli-ancestry-proj"
	runCLI(t, gcloudCLI("projects", "create", projectID,
		"--organization="+cliOrganizationID, "--format=json"))

	out = runCLI(t, gcloudCLI("projects", "get-ancestors", projectID, "--format=json"))
	var ancestors []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &ancestors), "output: %s", out)
	require.Len(t, ancestors, 2, "output: %s", out)
	assert.Equal(t, "project", ancestors[0].Type)
	assert.Equal(t, "organization", ancestors[1].Type)
	assert.Equal(t, cliOrganizationID, ancestors[1].ID)
}

func TestResourceManagerLiensCLI(t *testing.T) {
	const projectID = "cli-lien-proj"
	runCLI(t, gcloudCLI("projects", "create", projectID,
		"--organization="+cliOrganizationID, "--format=json"))

	out := runCLI(t, gcloudCLI("alpha", "resource-manager", "liens", "create",
		"--project="+projectID,
		"--restrictions=resourcemanager.projects.delete",
		"--reason=cli lien coverage",
		"--origin=cli-tests",
		"--format=json"))
	var created struct {
		Name         string   `json:"name"`
		Parent       string   `json:"parent"`
		Reason       string   `json:"reason"`
		Restrictions []string `json:"restrictions"`
	}
	parseJSONObject(t, out, &created)
	require.True(t, strings.HasPrefix(created.Name, "liens/"), "output: %s", out)
	assert.Equal(t, "projects/"+projectID, created.Parent)
	assert.Equal(t, "cli lien coverage", created.Reason)

	out = runCLI(t, gcloudCLI("alpha", "resource-manager", "liens", "list",
		"--project="+projectID, "--format=json"))
	assert.Contains(t, out, created.Name)

	// `liens delete` takes the bare lien id, not the resource name.
	runCLI(t, gcloudCLI("alpha", "resource-manager", "liens", "delete",
		strings.TrimPrefix(created.Name, "liens/")))

	out = runCLI(t, gcloudCLI("alpha", "resource-manager", "liens", "list",
		"--project="+projectID, "--format=json"))
	assert.NotContains(t, out, created.Name)
}
