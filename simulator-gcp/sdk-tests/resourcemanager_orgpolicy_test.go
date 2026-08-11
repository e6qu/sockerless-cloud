package gcp_sdk_test

// Cloud Resource Manager v1 Organization Policy, organizations, liens and
// hierarchy surface, driven by google.golang.org/api/cloudresourcemanager/v1 —
// the wire gcloud's `resource-manager org-policies` group and
// terraform-provider-google's google_project_organization_policy /
// google_folder_organization_policy / google_organization_policy /
// google_resource_manager_lien resources speak.
//
// Routes exercised: POST /v1/projects/{projectAction}, POST
// /v1/folders/{folderAction}, POST /v1/organizations/{orgAction}, GET
// /v1/organizations/{org}, POST /v1/organizations:search, POST /v1/liens,
// GET /v1/liens, GET /v1/liens/{lien}, DELETE /v1/liens/{lien}, and the v3
// spellings POST /v3/liens, GET /v3/liens, GET /v3/liens/{lien},
// DELETE /v3/liens/{lien}.

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	crmv1 "google.golang.org/api/cloudresourcemanager/v1"
	crmv2 "google.golang.org/api/cloudresourcemanager/v2"
	crm "google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iam/v1"
	"google.golang.org/api/option"
)

const (
	orgPolicyBooleanConstraint = "constraints/iam.disableServiceAccountKeyCreation"
	orgPolicyListConstraint    = "constraints/gcp.resourceLocations"
)

// orgPolicyProject creates a project parented at the simulator's organization
// so hierarchy resolution has a full chain to walk, and clears the policies the
// test sets so a later test reads an unpolicied project.
func orgPolicyProject(t *testing.T, projectID string) string {
	t.Helper()
	svc := crmV1Service(t)
	_, err := svc.Projects.Create(&crmv1.Project{
		ProjectId: projectID,
		Name:      "Org Policy " + projectID,
		Parent:    &crmv1.ResourceId{Type: "organization", Id: "123456789012"},
	}).Do()
	require.NoError(t, err)
	return "projects/" + projectID
}

func TestOrgPolicy_ProjectBooleanLifecycle(t *testing.T) {
	svc := crmV1Service(t)
	resource := orgPolicyProject(t, "sdk-orgpol-boolean")

	// An unset constraint reads back as the empty policy for that constraint,
	// not a 404.
	got, err := svc.Projects.GetOrgPolicy(resource, &crmv1.GetOrgPolicyRequest{
		Constraint: orgPolicyBooleanConstraint,
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, orgPolicyBooleanConstraint, got.Constraint)
	assert.Nil(t, got.BooleanPolicy)
	assert.Empty(t, got.Etag)

	set, err := svc.Projects.SetOrgPolicy(resource, &crmv1.SetOrgPolicyRequest{
		Policy: &crmv1.OrgPolicy{
			Constraint:    orgPolicyBooleanConstraint,
			BooleanPolicy: &crmv1.BooleanPolicy{Enforced: true},
		},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, set.BooleanPolicy)
	assert.True(t, set.BooleanPolicy.Enforced)
	assert.NotEmpty(t, set.Etag, "a stored policy carries an etag")
	assert.NotEmpty(t, set.UpdateTime)

	got, err = svc.Projects.GetOrgPolicy(resource, &crmv1.GetOrgPolicyRequest{
		Constraint: orgPolicyBooleanConstraint,
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, got.BooleanPolicy)
	assert.True(t, got.BooleanPolicy.Enforced)
	assert.Equal(t, set.Etag, got.Etag)

	listed, err := svc.Projects.ListOrgPolicies(resource, &crmv1.ListOrgPoliciesRequest{}).Do()
	require.NoError(t, err)
	var constraints []string
	for _, p := range listed.Policies {
		constraints = append(constraints, p.Constraint)
	}
	assert.Contains(t, constraints, orgPolicyBooleanConstraint)

	// A stale etag loses to the stored one.
	_, err = svc.Projects.SetOrgPolicy(resource, &crmv1.SetOrgPolicyRequest{
		Policy: &crmv1.OrgPolicy{
			Constraint:    orgPolicyBooleanConstraint,
			Etag:          "c3RhbGU=",
			BooleanPolicy: &crmv1.BooleanPolicy{Enforced: false},
		},
	}).Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 409, apiErr.Code)

	_, err = svc.Projects.ClearOrgPolicy(resource, &crmv1.ClearOrgPolicyRequest{
		Constraint: orgPolicyBooleanConstraint,
		Etag:       set.Etag,
	}).Do()
	require.NoError(t, err)

	got, err = svc.Projects.GetOrgPolicy(resource, &crmv1.GetOrgPolicyRequest{
		Constraint: orgPolicyBooleanConstraint,
	}).Do()
	require.NoError(t, err)
	assert.Nil(t, got.BooleanPolicy, "cleared policy reads back empty")
}

func TestOrgPolicy_UnknownConstraintRejected(t *testing.T) {
	svc := crmV1Service(t)
	resource := orgPolicyProject(t, "sdk-orgpol-unknown")

	_, err := svc.Projects.SetOrgPolicy(resource, &crmv1.SetOrgPolicyRequest{
		Policy: &crmv1.OrgPolicy{
			Constraint:    "constraints/not.aRealConstraint",
			BooleanPolicy: &crmv1.BooleanPolicy{Enforced: true},
		},
	}).Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.Code)
	assert.Contains(t, apiErr.Message, "not recognized")

	// A boolean constraint rejects a list policy and the other way round.
	_, err = svc.Projects.SetOrgPolicy(resource, &crmv1.SetOrgPolicyRequest{
		Policy: &crmv1.OrgPolicy{
			Constraint: orgPolicyBooleanConstraint,
			ListPolicy: &crmv1.ListPolicy{AllValues: "DENY"},
		},
	}).Do()
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.Code)
	assert.Contains(t, apiErr.Message, "boolean constraint")
}

func TestOrgPolicy_ConstraintCatalogIsTheAcceptedSet(t *testing.T) {
	svc := crmV1Service(t)

	listed, err := svc.Projects.ListAvailableOrgPolicyConstraints("projects/test-project",
		&crmv1.ListAvailableOrgPolicyConstraintsRequest{}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, listed.Constraints)

	byName := map[string]*crmv1.Constraint{}
	for _, c := range listed.Constraints {
		byName[c.Name] = c
		assert.NotEmpty(t, c.DisplayName, "%s: constraint carries its display name", c.Name)
		assert.NotEmpty(t, c.Description, "%s: constraint carries its description", c.Name)
		assert.Equal(t, "ALLOW", c.ConstraintDefault, "%s", c.Name)
		assert.True(t, c.BooleanConstraint != nil || c.ListConstraint != nil,
			"%s: a constraint is boolean-valued or list-valued", c.Name)
	}
	require.Contains(t, byName, orgPolicyBooleanConstraint)
	require.NotNil(t, byName[orgPolicyBooleanConstraint].BooleanConstraint)
	require.Contains(t, byName, orgPolicyListConstraint)
	require.NotNil(t, byName[orgPolicyListConstraint].ListConstraint)
	// Subtree values ("under:folders/123") belong to the constraints that
	// declare supportsUnder, and gcp.resourceLocations is not one of them.
	assert.False(t, byName[orgPolicyListConstraint].ListConstraint.SupportsUnder)
	require.Contains(t, byName, "constraints/compute.vmExternalIpAccess")
	assert.True(t, byName["constraints/compute.vmExternalIpAccess"].ListConstraint.SupportsUnder)

	// The catalog and the accepted set are the same set: every advertised
	// constraint takes a policy of the value type it declares.
	resource := orgPolicyProject(t, "sdk-orgpol-catalog")
	for name, c := range byName {
		policy := &crmv1.OrgPolicy{Constraint: name}
		if c.BooleanConstraint != nil {
			policy.BooleanPolicy = &crmv1.BooleanPolicy{Enforced: true}
		} else {
			policy.ListPolicy = &crmv1.ListPolicy{AllValues: "DENY"}
		}
		_, err := svc.Projects.SetOrgPolicy(resource, &crmv1.SetOrgPolicyRequest{Policy: policy}).Do()
		require.NoError(t, err, "advertised constraint %s must accept a policy", name)
	}

	// Paging is honoured on the request body, where the method carries it.
	first, err := svc.Projects.ListAvailableOrgPolicyConstraints("projects/test-project",
		&crmv1.ListAvailableOrgPolicyConstraintsRequest{PageSize: 2}).Do()
	require.NoError(t, err)
	assert.Len(t, first.Constraints, 2)
	require.NotEmpty(t, first.NextPageToken)
	second, err := svc.Projects.ListAvailableOrgPolicyConstraints("projects/test-project",
		&crmv1.ListAvailableOrgPolicyConstraintsRequest{PageSize: 2, PageToken: first.NextPageToken}).Do()
	require.NoError(t, err)
	assert.Len(t, second.Constraints, 2)
	assert.NotEqual(t, first.Constraints[0].Name, second.Constraints[0].Name)
}

func TestOrgPolicy_EffectivePolicyResolvesThroughTheHierarchy(t *testing.T) {
	svc := crmV1Service(t)
	v3 := crmV3Service(t)

	// organization → folder → project.
	folderOp, err := v3.Folders.Create(&crm.Folder{
		Parent:      "organizations/123456789012",
		DisplayName: "sdk-orgpol-folder",
	}).Do()
	require.NoError(t, err)
	folder := crmOpResourceName(t, folderOp)
	require.NotEmpty(t, folder)

	const projectID = "sdk-orgpol-inherit"
	_, err = svc.Projects.Create(&crmv1.Project{
		ProjectId: projectID,
		Parent:    &crmv1.ResourceId{Type: "folder", Id: strings.TrimPrefix(folder, "folders/")},
	}).Do()
	require.NoError(t, err)
	project := "projects/" + projectID

	// Nothing set anywhere: the effective policy is the constraint's own
	// default, and an ALLOW default on a boolean constraint is "not enforced".
	eff, err := svc.Projects.GetEffectiveOrgPolicy(project, &crmv1.GetEffectiveOrgPolicyRequest{
		Constraint: orgPolicyBooleanConstraint,
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, eff.BooleanPolicy)
	assert.False(t, eff.BooleanPolicy.Enforced)

	// Enforced at the organization: the project inherits it.
	_, err = svc.Organizations.SetOrgPolicy("organizations/123456789012", &crmv1.SetOrgPolicyRequest{
		Policy: &crmv1.OrgPolicy{
			Constraint:    orgPolicyBooleanConstraint,
			BooleanPolicy: &crmv1.BooleanPolicy{Enforced: true},
		},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = svc.Organizations.ClearOrgPolicy("organizations/123456789012",
			&crmv1.ClearOrgPolicyRequest{Constraint: orgPolicyBooleanConstraint}).Do()
	})

	eff, err = svc.Projects.GetEffectiveOrgPolicy(project, &crmv1.GetEffectiveOrgPolicyRequest{
		Constraint: orgPolicyBooleanConstraint,
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, eff.BooleanPolicy)
	assert.True(t, eff.BooleanPolicy.Enforced, "organization policy reaches the project")

	// The intervening folder turns it back off for everything beneath it.
	_, err = svc.Folders.SetOrgPolicy(folder, &crmv1.SetOrgPolicyRequest{
		Policy: &crmv1.OrgPolicy{
			Constraint:    orgPolicyBooleanConstraint,
			BooleanPolicy: &crmv1.BooleanPolicy{Enforced: false},
		},
	}).Do()
	require.NoError(t, err)

	eff, err = svc.Projects.GetEffectiveOrgPolicy(project, &crmv1.GetEffectiveOrgPolicyRequest{
		Constraint: orgPolicyBooleanConstraint,
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, eff.BooleanPolicy)
	assert.False(t, eff.BooleanPolicy.Enforced, "the nearer folder policy wins")

	// The project's own restoreDefault discards the whole ancestry.
	_, err = svc.Organizations.SetOrgPolicy("organizations/123456789012", &crmv1.SetOrgPolicyRequest{
		Policy: &crmv1.OrgPolicy{
			Constraint: orgPolicyListConstraint,
			ListPolicy: &crmv1.ListPolicy{AllowedValues: []string{"in:us-locations"}},
		},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = svc.Organizations.ClearOrgPolicy("organizations/123456789012",
			&crmv1.ClearOrgPolicyRequest{Constraint: orgPolicyListConstraint}).Do()
	})
	eff, err = svc.Projects.GetEffectiveOrgPolicy(project, &crmv1.GetEffectiveOrgPolicyRequest{
		Constraint: orgPolicyListConstraint,
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, eff.ListPolicy)
	assert.Equal(t, []string{"in:us-locations"}, eff.ListPolicy.AllowedValues)

	_, err = svc.Projects.SetOrgPolicy(project, &crmv1.SetOrgPolicyRequest{
		Policy: &crmv1.OrgPolicy{
			Constraint:     orgPolicyListConstraint,
			RestoreDefault: &crmv1.RestoreDefault{},
		},
	}).Do()
	require.NoError(t, err)
	eff, err = svc.Projects.GetEffectiveOrgPolicy(project, &crmv1.GetEffectiveOrgPolicyRequest{
		Constraint: orgPolicyListConstraint,
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, eff.ListPolicy)
	assert.Empty(t, eff.ListPolicy.AllowedValues)
	assert.Equal(t, "ALLOW", eff.ListPolicy.AllValues, "restoreDefault falls back to the constraint default")

	// A list policy that inherits from its parent extends the inherited set
	// rather than replacing it.
	_, err = svc.Folders.SetOrgPolicy(folder, &crmv1.SetOrgPolicyRequest{
		Policy: &crmv1.OrgPolicy{
			Constraint: orgPolicyListConstraint,
			ListPolicy: &crmv1.ListPolicy{
				AllowedValues:     []string{"in:eu-locations"},
				InheritFromParent: true,
			},
		},
	}).Do()
	require.NoError(t, err)
	eff, err = svc.Folders.GetEffectiveOrgPolicy(folder, &crmv1.GetEffectiveOrgPolicyRequest{
		Constraint: orgPolicyListConstraint,
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, eff.ListPolicy)
	assert.ElementsMatch(t, []string{"in:us-locations", "in:eu-locations"}, eff.ListPolicy.AllowedValues)
}

// TestOrgPolicy_BooleanConstraintIsEnforcedByTheServiceItGoverns is the half
// that makes the catalog more than a list: a policy set through Cloud Resource
// Manager changes what Identity and Access Management does.
func TestOrgPolicy_BooleanConstraintIsEnforcedByTheServiceItGoverns(t *testing.T) {
	svc := crmV1Service(t)
	const projectID = "sdk-orgpol-enforced"
	resource := orgPolicyProject(t, projectID)

	iamSvc, err := iam.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	sa, err := iamSvc.Projects.ServiceAccounts.Create("projects/"+projectID, &iam.CreateServiceAccountRequest{
		AccountId:      "orgpol-sa",
		ServiceAccount: &iam.ServiceAccount{DisplayName: "Org policy subject"},
	}).Do()
	require.NoError(t, err)

	// Unenforced: key creation succeeds.
	_, err = iamSvc.Projects.ServiceAccounts.Keys.Create(sa.Name, &iam.CreateServiceAccountKeyRequest{}).Do()
	require.NoError(t, err)

	_, err = svc.Projects.SetOrgPolicy(resource, &crmv1.SetOrgPolicyRequest{
		Policy: &crmv1.OrgPolicy{
			Constraint:    orgPolicyBooleanConstraint,
			BooleanPolicy: &crmv1.BooleanPolicy{Enforced: true},
		},
	}).Do()
	require.NoError(t, err)

	_, err = iamSvc.Projects.ServiceAccounts.Keys.Create(sa.Name, &iam.CreateServiceAccountKeyRequest{}).Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr, "enforced constraint must block key creation")
	assert.Equal(t, 400, apiErr.Code)
	assert.Equal(t, "Key creation is not allowed on this service account.", apiErr.Message)

	// Service account creation is governed by its own constraint, so it is
	// still allowed.
	_, err = iamSvc.Projects.ServiceAccounts.Create("projects/"+projectID, &iam.CreateServiceAccountRequest{
		AccountId: "orgpol-sa-two",
	}).Do()
	require.NoError(t, err)

	_, err = svc.Projects.SetOrgPolicy(resource, &crmv1.SetOrgPolicyRequest{
		Policy: &crmv1.OrgPolicy{
			Constraint:    "constraints/iam.disableServiceAccountCreation",
			BooleanPolicy: &crmv1.BooleanPolicy{Enforced: true},
		},
	}).Do()
	require.NoError(t, err)
	_, err = iamSvc.Projects.ServiceAccounts.Create("projects/"+projectID, &iam.CreateServiceAccountRequest{
		AccountId: "orgpol-sa-three",
	}).Do()
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.Code)
	assert.Equal(t, "Service account creation is not allowed on this project.", apiErr.Message)

	// Every Cloud Resource Manager method accepts a project by id or by
	// number, so a policy set through one spelling must be found through the
	// other — otherwise a caller addressing the project by number escapes a
	// policy that is in force.
	described, err := svc.Projects.Get(projectID).Do()
	require.NoError(t, err)
	require.NotZero(t, described.ProjectNumber)
	byNumber := "projects/" + strconv.FormatInt(described.ProjectNumber, 10)

	eff, err := svc.Projects.GetEffectiveOrgPolicy(byNumber, &crmv1.GetEffectiveOrgPolicyRequest{
		Constraint: "constraints/iam.disableServiceAccountCreation",
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, eff.BooleanPolicy)
	assert.True(t, eff.BooleanPolicy.Enforced,
		"the policy set through the project id must be in force when the project is addressed by number")

	stored, err := svc.Projects.GetOrgPolicy(byNumber, &crmv1.GetOrgPolicyRequest{
		Constraint: "constraints/iam.disableServiceAccountCreation",
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, stored.BooleanPolicy, "the stored policy reads back through either spelling")
	assert.True(t, stored.BooleanPolicy.Enforced)

	listed, err := svc.Projects.ListOrgPolicies(byNumber, &crmv1.ListOrgPoliciesRequest{}).Do()
	require.NoError(t, err)
	var listedConstraints []string
	for _, p := range listed.Policies {
		listedConstraints = append(listedConstraints, p.Constraint)
	}
	assert.Contains(t, listedConstraints, "constraints/iam.disableServiceAccountCreation")

	// The service enforcing the constraint resolves the hierarchy from
	// whatever spelling its own caller used — Identity and Access Management
	// accepts a project by number too, and a policy must not be escapable by
	// addressing the project differently.
	_, err = iamSvc.Projects.ServiceAccounts.Create(byNumber, &iam.CreateServiceAccountRequest{
		AccountId: "orgpol-sa-bynumber",
	}).Do()
	require.ErrorAs(t, err, &apiErr,
		"the policy must be in force when the enforcing service addresses the project by number")
	assert.Equal(t, 400, apiErr.Code)
	assert.Equal(t, "Service account creation is not allowed on this project.", apiErr.Message)

	// Clearing the policies restores the service's default behaviour, and
	// clearing through the number spelling clears what the id spelling set.
	_, err = svc.Projects.ClearOrgPolicy(byNumber, &crmv1.ClearOrgPolicyRequest{
		Constraint: "constraints/iam.disableServiceAccountCreation",
	}).Do()
	require.NoError(t, err)
	_, err = iamSvc.Projects.ServiceAccounts.Create("projects/"+projectID, &iam.CreateServiceAccountRequest{
		AccountId: "orgpol-sa-four",
	}).Do()
	require.NoError(t, err)

	_, err = svc.Projects.ClearOrgPolicy(resource, &crmv1.ClearOrgPolicyRequest{
		Constraint: orgPolicyBooleanConstraint,
	}).Do()
	require.NoError(t, err)
	_, err = iamSvc.Projects.ServiceAccounts.Keys.Create(sa.Name, &iam.CreateServiceAccountKeyRequest{}).Do()
	require.NoError(t, err)
}

// TestResourceManagerV2_Folders drives the Cloud Resource Manager v2 folders
// collection — the version gcloud's `resource-manager folders` group speaks —
// over google.golang.org/api/cloudresourcemanager/v2. Routes exercised:
// POST /v2/folders, GET /v2/folders, GET /v2/folders/{folder},
// PATCH /v2/folders/{folder}, DELETE /v2/folders/{folder},
// POST /v2/folders:search, POST /v2/folders/{folderAction}.
func TestResourceManagerV2_Folders(t *testing.T) {
	svc, err := crmv2.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)

	op, err := svc.Folders.Create(&crmv2.Folder{DisplayName: "sdk-v2-folder"}).
		Parent("organizations/123456789012").Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	var created crmv2.Folder
	require.NoError(t, json.Unmarshal(op.Response, &created))
	require.True(t, strings.HasPrefix(created.Name, "folders/"), "%s", created.Name)
	assert.Equal(t, "sdk-v2-folder", created.DisplayName)
	assert.Equal(t, "organizations/123456789012", created.Parent)
	assert.Equal(t, "ACTIVE", created.LifecycleState)

	got, err := svc.Folders.Get(created.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, created.Name, got.Name)

	listed, err := svc.Folders.List().Parent("organizations/123456789012").Do()
	require.NoError(t, err)
	var names []string
	for _, f := range listed.Folders {
		names = append(names, f.Name)
	}
	assert.Contains(t, names, created.Name)

	// search has no gcloud command; the SDK and the console reach it directly.
	found, err := svc.Folders.Search(&crmv2.SearchFoldersRequest{
		Query: "displayName:sdk-v2-folder",
	}).Do()
	require.NoError(t, err)
	var searched []string
	for _, f := range found.Folders {
		searched = append(searched, f.Name)
	}
	assert.Contains(t, searched, created.Name)

	// patch returns the Folder itself — only create and move are long-running
	// in v2 — and only display_name is updatable.
	patched, err := svc.Folders.Patch(created.Name, &crmv2.Folder{DisplayName: "sdk-v2-folder-renamed"}).
		UpdateMask("display_name").Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-v2-folder-renamed", patched.DisplayName)

	_, err = svc.Folders.Patch(created.Name, &crmv2.Folder{Parent: "organizations/1"}).
		UpdateMask("parent").Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.Code)

	// The v3 collection reads the same folder.
	v3 := crmV3Service(t)
	v3Got, err := v3.Folders.Get(created.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-v2-folder-renamed", v3Got.DisplayName)
	assert.Equal(t, "ACTIVE", v3Got.State, "v3 spells the state `state`, v2 spells it `lifecycleState`")

	// move is long-running; delete and undelete return the Folder.
	nested, err := svc.Folders.Create(&crmv2.Folder{DisplayName: "sdk-v2-folder-child"}).
		Parent("organizations/123456789012").Do()
	require.NoError(t, err)
	var child crmv2.Folder
	require.NoError(t, json.Unmarshal(nested.Response, &child))

	moveOp, err := svc.Folders.Move(child.Name, &crmv2.MoveFolderRequest{
		DestinationParent: created.Name,
	}).Do()
	require.NoError(t, err)
	require.True(t, moveOp.Done)
	moved, err := svc.Folders.Get(child.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, created.Name, moved.Parent)

	deleted, err := svc.Folders.Delete(child.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "DELETE_REQUESTED", deleted.LifecycleState)

	// A deleted folder is out of the default listing and back in with
	// showDeleted.
	active, err := svc.Folders.List().Parent(created.Name).Do()
	require.NoError(t, err)
	assert.Empty(t, active.Folders)
	withDeleted, err := svc.Folders.List().Parent(created.Name).ShowDeleted(true).Do()
	require.NoError(t, err)
	require.Len(t, withDeleted.Folders, 1)
	assert.Equal(t, child.Name, withDeleted.Folders[0].Name)

	undeleted, err := svc.Folders.Undelete(child.Name, &crmv2.UndeleteFolderRequest{}).Do()
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", undeleted.LifecycleState)

	// The IAM triple shares the policy store with the v3 folder verbs.
	_, err = svc.Folders.SetIamPolicy(created.Name, &crmv2.SetIamPolicyRequest{
		Policy: &crmv2.Policy{Bindings: []*crmv2.Binding{{
			Role:    "roles/resourcemanager.folderAdmin",
			Members: []string{"user:folder-admin@example.com"},
		}}},
	}).Do()
	require.NoError(t, err)
	policy, err := svc.Folders.GetIamPolicy(created.Name, &crmv2.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, policy.Bindings, 1)
	assert.Equal(t, "roles/resourcemanager.folderAdmin", policy.Bindings[0].Role)

	perms, err := svc.Folders.TestIamPermissions(created.Name, &crmv2.TestIamPermissionsRequest{
		Permissions: []string{"resourcemanager.folders.get"},
	}).Do()
	require.NoError(t, err)
	assert.NotNil(t, perms)

	_, err = svc.Folders.Get("folders/999999999999").Do()
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.Code)
}

func TestResourceManagerV1_OrganizationsAndAncestry(t *testing.T) {
	svc := crmV1Service(t)

	org, err := svc.Organizations.Get("organizations/123456789012").Do()
	require.NoError(t, err)
	assert.Equal(t, "organizations/123456789012", org.Name)
	assert.Equal(t, "example.com", org.DisplayName)
	assert.Equal(t, "ACTIVE", org.LifecycleState)
	require.NotNil(t, org.Owner)
	assert.NotEmpty(t, org.Owner.DirectoryCustomerId)

	// An organization the caller cannot see is a 403, never a 404 — the API
	// does not disclose existence.
	_, err = svc.Organizations.Get("organizations/999999999999").Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 403, apiErr.Code)

	found, err := svc.Organizations.Search(&crmv1.SearchOrganizationsRequest{
		Filter: "domain:example.com",
	}).Do()
	require.NoError(t, err)
	require.Len(t, found.Organizations, 1)
	assert.Equal(t, "organizations/123456789012", found.Organizations[0].Name)

	none, err := svc.Organizations.Search(&crmv1.SearchOrganizationsRequest{
		Filter: "domain:no-such-domain.example",
	}).Do()
	require.NoError(t, err)
	assert.Empty(t, none.Organizations)

	// The v3 spellings — GET /v3/organizations/{org}, GET
	// /v3/organizations:search and the IAM triple on POST
	// /v3/organizations/{orgAction} — read the same organization store, in
	// v3's wire shape.
	v3 := crmV3Service(t)
	v3Org, err := v3.Organizations.Get("organizations/123456789012").Do()
	require.NoError(t, err)
	assert.Equal(t, "example.com", v3Org.DisplayName)
	assert.Equal(t, "ACTIVE", v3Org.State, "v3 spells the state `state`, v1 spells it `lifecycleState`")
	assert.NotEmpty(t, v3Org.DirectoryCustomerId)

	_, err = v3.Organizations.Get("organizations/999999999999").Do()
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 403, apiErr.Code)

	v3Found, err := v3.Organizations.Search().Query("domain:example.com").Do()
	require.NoError(t, err)
	require.Len(t, v3Found.Organizations, 1)
	assert.Equal(t, "organizations/123456789012", v3Found.Organizations[0].Name)

	_, err = v3.Organizations.SetIamPolicy("organizations/123456789012", &crm.SetIamPolicyRequest{
		Policy: &crm.Policy{Bindings: []*crm.Binding{{
			Role:    "roles/resourcemanager.organizationAdmin",
			Members: []string{"user:org-admin@example.com"},
		}}},
	}).Do()
	require.NoError(t, err)
	orgPolicy, err := v3.Organizations.GetIamPolicy("organizations/123456789012", &crm.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, orgPolicy.Bindings, 1)
	assert.Equal(t, "roles/resourcemanager.organizationAdmin", orgPolicy.Bindings[0].Role)

	// An unrouted organization custom method is a method miss, not a story
	// about the organization.
	_, err = v3.Organizations.TestIamPermissions("organizations/123456789012", &crm.TestIamPermissionsRequest{
		Permissions: []string{"resourcemanager.organizations.get"},
	}).Do()
	require.NoError(t, err)

	// getAncestry walks from the project up to the organization.
	const projectID = "sdk-ancestry-proj"
	created, err := svc.Projects.Create(&crmv1.Project{
		ProjectId: projectID,
		Parent:    &crmv1.ResourceId{Type: "organization", Id: "123456789012"},
	}).Do()
	require.NoError(t, err)
	require.True(t, created.Done)

	ancestry, err := svc.Projects.GetAncestry(projectID, &crmv1.GetAncestryRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, ancestry.Ancestor, 2)
	assert.Equal(t, "project", ancestry.Ancestor[0].ResourceId.Type)
	assert.NotEmpty(t, ancestry.Ancestor[0].ResourceId.Id)
	assert.Equal(t, "organization", ancestry.Ancestor[1].ResourceId.Type)
	assert.Equal(t, "123456789012", ancestry.Ancestor[1].ResourceId.Id)
}

func TestResourceManagerLiens_BothAPIVersionsShareOneCollection(t *testing.T) {
	v1 := crmV1Service(t)
	v3 := crmV3Service(t)

	created, err := v1.Liens.Create(&crmv1.Lien{
		Parent:       "projects/test-project",
		Restrictions: []string{"resourcemanager.projects.delete"},
		Reason:       "sdk lien coverage",
		Origin:       "sdk-tests",
	}).Do()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(created.Name, "liens/"), "%s", created.Name)

	got, err := v1.Liens.Get(created.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk lien coverage", got.Reason)
	assert.Equal(t, []string{"resourcemanager.projects.delete"}, got.Restrictions)

	listed, err := v1.Liens.List().Parent("projects/test-project").Do()
	require.NoError(t, err)
	var names []string
	for _, l := range listed.Liens {
		names = append(names, l.Name)
	}
	assert.Contains(t, names, created.Name)

	// The v3 spelling addresses the same record.
	v3Got, err := v3.Liens.Get(created.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, created.Name, v3Got.Name)
	v3Listed, err := v3.Liens.List().Parent("projects/test-project").Do()
	require.NoError(t, err)
	var v3Names []string
	for _, l := range v3Listed.Liens {
		v3Names = append(v3Names, l.Name)
	}
	assert.Contains(t, v3Names, created.Name)

	_, err = v1.Liens.Delete(created.Name).Do()
	require.NoError(t, err)
	_, err = v1.Liens.Get(created.Name).Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.Code)

	// A lien restricting resourcemanager.projects.delete is what the
	// collection is for: DeleteProject refuses while it is in place, over
	// either API version, and succeeds once it is removed.
	const lienedProject = "sdk-liened-project"
	createOp, err := v1.Projects.Create(&crmv1.Project{ProjectId: lienedProject}).Do()
	require.NoError(t, err)
	require.True(t, createOp.Done)

	hold, err := v1.Liens.Create(&crmv1.Lien{
		Parent:       "projects/" + lienedProject,
		Restrictions: []string{"resourcemanager.projects.delete"},
		Reason:       "delete protection",
	}).Do()
	require.NoError(t, err)

	_, err = v1.Projects.Delete(lienedProject).Do()
	require.ErrorAs(t, err, &apiErr, "a lien must block DeleteProject")
	assert.Equal(t, 400, apiErr.Code)
	assert.Contains(t, apiErr.Message, "Precondition check failed")

	_, err = v3.Projects.Delete("projects/" + lienedProject).Do()
	require.ErrorAs(t, err, &apiErr, "the v3 delete honours the same lien")
	assert.Equal(t, 400, apiErr.Code)

	_, err = v1.Liens.Delete(hold.Name).Do()
	require.NoError(t, err)
	_, err = v1.Projects.Delete(lienedProject).Do()
	require.NoError(t, err, "deletion proceeds once the lien is removed")

	// A lien created over v3 deletes over v3 through the same store.
	v3Created, err := v3.Liens.Create(&crm.Lien{
		Parent:       "projects/test-project",
		Restrictions: []string{"resourcemanager.projects.delete"},
		Reason:       "v3 lien coverage",
	}).Do()
	require.NoError(t, err)
	_, err = v1.Liens.Get(v3Created.Name).Do()
	require.NoError(t, err)
	_, err = v3.Liens.Delete(v3Created.Name).Do()
	require.NoError(t, err)
	_, err = v3.Liens.Delete(v3Created.Name).Do()
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.Code)
}
