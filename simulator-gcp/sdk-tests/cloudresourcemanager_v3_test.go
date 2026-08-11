package gcp_sdk_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	crm "google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/iam/v1"
	iamcredentials "google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/option"
)

// crmOpResourceName extracts the embedded resource's name field from a settled
// long-running Operation's response envelope (a json.RawMessage in the SDK).
func crmOpResourceName(t *testing.T, op *crm.Operation) string {
	t.Helper()
	require.NotEmpty(t, op.Response, "operation must carry a response")
	var m map[string]any
	require.NoError(t, json.Unmarshal(op.Response, &m))
	name, _ := m["name"].(string)
	return name
}

func crmV3Service(t *testing.T) *crm.Service {
	t.Helper()
	svc, err := crm.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	return svc
}

func TestResourceManagerV3_FetchResourceSemantics(t *testing.T) {
	const fullName = "//cloudresourcemanager.googleapis.com/projects/735298346210"
	resp, err := simAuthHTTPClient().Get(baseURL + "/v3:fetchResourceSemantics?fullResourceName=" + url.QueryEscape(fullName))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got struct {
		FullResourceName string            `json:"fullResourceName"`
		Semantics        map[string]string `json:"semantics"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, fullName, got.FullResourceName)
	assert.Empty(t, got.Semantics)

	invalid, err := simAuthHTTPClient().Get(baseURL + "/v3:fetchResourceSemantics")
	require.NoError(t, err)
	defer invalid.Body.Close()
	invalidBody, err := io.ReadAll(invalid.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, invalid.StatusCode, string(invalidBody))
	assert.Contains(t, string(invalidBody), "INVALID_ARGUMENT")
}

func TestResourceManagerV3_ProjectLifecycle(t *testing.T) {
	svc := crmV3Service(t)

	// Create returns a settled long-running Operation embedding the project.
	op, err := svc.Projects.Create(&crm.Project{
		ProjectId:   "crm-proj-lifecycle",
		DisplayName: "CRM Lifecycle",
		Parent:      "organizations/123456789012",
	}).Do()
	require.NoError(t, err)
	assert.True(t, op.Done)

	got, err := svc.Projects.Get("projects/crm-proj-lifecycle").Do()
	require.NoError(t, err)
	assert.Equal(t, "crm-proj-lifecycle", got.ProjectId)
	assert.Equal(t, "ACTIVE", got.State)

	// Patch displayName via updateMask.
	pop, err := svc.Projects.Patch(got.Name, &crm.Project{DisplayName: "Renamed"}).
		UpdateMask("displayName").Do()
	require.NoError(t, err)
	assert.True(t, pop.Done)
	got, err = svc.Projects.Get(got.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "Renamed", got.DisplayName)

	// Move to a new parent.
	mop, err := svc.Projects.Move(got.Name, &crm.MoveProjectRequest{
		DestinationParent: "folders/000000000001",
	}).Do()
	require.NoError(t, err)
	assert.True(t, mop.Done)

	// List under the new parent surfaces the project.
	list, err := svc.Projects.List().Parent("folders/000000000001").Do()
	require.NoError(t, err)
	var found bool
	for _, p := range list.Projects {
		if p.ProjectId == "crm-proj-lifecycle" {
			found = true
		}
	}
	assert.True(t, found, "moved project should appear under new parent")

	// Search returns all projects.
	sr, err := svc.Projects.Search().Do()
	require.NoError(t, err)
	assert.NotEmpty(t, sr.Projects)

	// Delete then undelete round-trips state.
	dop, err := svc.Projects.Delete(got.Name).Do()
	require.NoError(t, err)
	assert.True(t, dop.Done)
	got, err = svc.Projects.Get(got.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "DELETE_REQUESTED", got.State)
	uop, err := svc.Projects.Undelete(got.Name, &crm.UndeleteProjectRequest{}).Do()
	require.NoError(t, err)
	assert.True(t, uop.Done)
	got, err = svc.Projects.Get(got.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", got.State)
}

func TestResourceManagerV3_ProjectIAM(t *testing.T) {
	svc := crmV3Service(t)
	const name = "projects/crm-iam-proj"

	// IAM verbs address an existing project; a missing one is a real 403.
	cop, err := svc.Projects.Create(&crm.Project{ProjectId: "crm-iam-proj"}).Do()
	require.NoError(t, err)
	require.True(t, cop.Done)

	set, err := svc.Projects.SetIamPolicy(name, &crm.SetIamPolicyRequest{
		Policy: &crm.Policy{
			Bindings: []*crm.Binding{{
				Role:    "roles/viewer",
				Members: []string{"user:alice@example.com"},
			}},
		},
	}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)

	got, err := svc.Projects.GetIamPolicy(name, &crm.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, got.Bindings, 1)
	assert.Equal(t, "roles/viewer", got.Bindings[0].Role)

	tip, err := svc.Projects.TestIamPermissions(name, &crm.TestIamPermissionsRequest{
		Permissions: []string{"resourcemanager.projects.get"},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"resourcemanager.projects.get"}, tip.Permissions)
}

func TestResourceManagerV3_FolderLifecycle(t *testing.T) {
	svc := crmV3Service(t)

	op, err := svc.Folders.Create(&crm.Folder{
		DisplayName: "crm-folder",
		Parent:      "organizations/123456789012",
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	// The folder name lives in the operation response.
	name := crmOpResourceName(t, op)
	require.NotEmpty(t, name)

	got, err := svc.Folders.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "crm-folder", got.DisplayName)
	assert.Equal(t, "ACTIVE", got.State)

	_, err = svc.Folders.Patch(name, &crm.Folder{DisplayName: "crm-folder-2"}).
		UpdateMask("displayName").Do()
	require.NoError(t, err)
	got, err = svc.Folders.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "crm-folder-2", got.DisplayName)

	_, err = svc.Folders.Move(name, &crm.MoveFolderRequest{
		DestinationParent: "folders/000000000099",
	}).Do()
	require.NoError(t, err)

	list, err := svc.Folders.List().Parent("folders/000000000099").Do()
	require.NoError(t, err)
	assert.NotEmpty(t, list.Folders)

	sr, err := svc.Folders.Search().Do()
	require.NoError(t, err)
	assert.NotEmpty(t, sr.Folders)

	_, err = svc.Folders.Delete(name).Do()
	require.NoError(t, err)
	got, err = svc.Folders.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "DELETE_REQUESTED", got.State)
	_, err = svc.Folders.Undelete(name, &crm.UndeleteFolderRequest{}).Do()
	require.NoError(t, err)

	// Folder IAM.
	_, err = svc.Folders.SetIamPolicy(name, &crm.SetIamPolicyRequest{
		Policy: &crm.Policy{Bindings: []*crm.Binding{{
			Role: "roles/resourcemanager.folderViewer", Members: []string{"user:bob@example.com"},
		}}},
	}).Do()
	require.NoError(t, err)
	pol, err := svc.Folders.GetIamPolicy(name, &crm.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, pol.Bindings, 1)
}

func TestResourceManagerV3_Liens(t *testing.T) {
	svc := crmV3Service(t)

	lien, err := svc.Liens.Create(&crm.Lien{
		Parent:       "projects/crm-lien-proj",
		Restrictions: []string{"resourcemanager.projects.delete"},
		Reason:       "protect",
		Origin:       "test",
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, lien.Name)
	assert.Equal(t, "protect", lien.Reason)

	got, err := svc.Liens.Get(lien.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, lien.Name, got.Name)

	list, err := svc.Liens.List().Parent("projects/crm-lien-proj").Do()
	require.NoError(t, err)
	require.NotEmpty(t, list.Liens)

	_, err = svc.Liens.Delete(lien.Name).Do()
	require.NoError(t, err)
	_, err = svc.Liens.Get(lien.Name).Do()
	assert.Error(t, err, "deleted lien should be gone")
}

func TestResourceManagerV3_TagKeys(t *testing.T) {
	svc := crmV3Service(t)

	op, err := svc.TagKeys.Create(&crm.TagKey{
		Parent:      "organizations/123456789012",
		ShortName:   "environment",
		Description: "deploy env",
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	name := crmOpResourceName(t, op)
	require.NotEmpty(t, name)

	got, err := svc.TagKeys.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "environment", got.ShortName)
	assert.Equal(t, "123456789012/environment", got.NamespacedName)

	ns, err := svc.TagKeys.GetNamespaced().Name("123456789012/environment").Do()
	require.NoError(t, err)
	assert.Equal(t, got.Name, ns.Name)

	list, err := svc.TagKeys.List().Parent("organizations/123456789012").Do()
	require.NoError(t, err)
	require.NotEmpty(t, list.TagKeys)

	_, err = svc.TagKeys.Patch(name, &crm.TagKey{Description: "updated"}).
		UpdateMask("description").Do()
	require.NoError(t, err)
	got, err = svc.TagKeys.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "updated", got.Description)

	// TagKey IAM.
	_, err = svc.TagKeys.SetIamPolicy(name, &crm.SetIamPolicyRequest{
		Policy: &crm.Policy{Bindings: []*crm.Binding{{
			Role: "roles/resourcemanager.tagAdmin", Members: []string{"user:tag@example.com"},
		}}},
	}).Do()
	require.NoError(t, err)
	pol, err := svc.TagKeys.GetIamPolicy(name, &crm.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, pol.Bindings, 1)

	_, err = svc.TagKeys.Delete(name).Do()
	require.NoError(t, err)
	_, err = svc.TagKeys.Get(name).Do()
	assert.Error(t, err)
}

func TestResourceManagerV3_TagValues(t *testing.T) {
	svc := crmV3Service(t)

	kop, err := svc.TagKeys.Create(&crm.TagKey{
		Parent:    "organizations/123456789012",
		ShortName: "tier",
	}).Do()
	require.NoError(t, err)
	keyName := crmOpResourceName(t, kop)

	op, err := svc.TagValues.Create(&crm.TagValue{
		Parent:    keyName,
		ShortName: "prod",
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	name := crmOpResourceName(t, op)
	require.NotEmpty(t, name)

	got, err := svc.TagValues.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "prod", got.ShortName)

	list, err := svc.TagValues.List().Parent(keyName).Do()
	require.NoError(t, err)
	require.NotEmpty(t, list.TagValues)

	// TagValue IAM.
	_, err = svc.TagValues.SetIamPolicy(name, &crm.SetIamPolicyRequest{
		Policy: &crm.Policy{Bindings: []*crm.Binding{{
			Role: "roles/resourcemanager.tagUser", Members: []string{"user:tv@example.com"},
		}}},
	}).Do()
	require.NoError(t, err)
	pol, err := svc.TagValues.GetIamPolicy(name, &crm.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, pol.Bindings, 1)
}

func TestIAMCredentials_SignBlobAndJwt(t *testing.T) {
	// The credential ops require the service account to exist; create it via
	// the IAM SDK first (same flow a real caller's IaC would have run).
	_, err := iamService(t).Projects.ServiceAccounts.Create("projects/test-project",
		&iam.CreateServiceAccountRequest{
			AccountId:      "signer",
			ServiceAccount: &iam.ServiceAccount{DisplayName: "Signer SA"},
		}).Do()
	require.NoError(t, err)

	svc := iamCredentialsService(t)
	const name = "projects/-/serviceAccounts/signer@test-project.iam.gserviceaccount.com"

	blob, err := svc.Projects.ServiceAccounts.SignBlob(name, &iamcredentials.SignBlobRequest{
		Payload: base64.StdEncoding.EncodeToString([]byte("hello-sign")),
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, blob.KeyId)
	require.NotEmpty(t, blob.SignedBlob)
	_, derr := base64.StdEncoding.DecodeString(blob.SignedBlob)
	assert.NoError(t, derr, "signedBlob must be valid base64")

	jwt, err := svc.Projects.ServiceAccounts.SignJwt(name, &iamcredentials.SignJwtRequest{
		Payload: `{"sub":"signer@test-project.iam.gserviceaccount.com","aud":"x"}`,
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, jwt.KeyId)
	require.NotEmpty(t, jwt.SignedJwt)
	assert.Equal(t, 2, countDots(jwt.SignedJwt), "signedJwt must be a 3-part JWS")
}

func countDots(s string) int {
	n := 0
	for _, c := range s {
		if c == '.' {
			n++
		}
	}
	return n
}
