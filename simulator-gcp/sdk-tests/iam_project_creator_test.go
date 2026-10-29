package gcp_sdk_test

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/require"
	crm "google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/option"
)

// A project's creator becomes its Owner: a service account that holds only
// Project Creator on a folder creates a project there and then deletes it,
// which only an Owner may.
func TestIAMEnforcement_ProjectCreatorOwnsTheProject(t *testing.T) {
	t.Parallel()
	folder := createTestFolder(t, "creator-owns")
	email, ts := enforcementPrincipal(t, "enf-project-creator")
	member := "serviceAccount:" + email

	admin, err := crm.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	policy, err := admin.Folders.GetIamPolicy(folder, &crm.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	policy.Bindings = append(policy.Bindings, &crm.Binding{Role: "roles/resourcemanager.projectCreator", Members: []string{member}})
	_, err = admin.Folders.SetIamPolicy(folder, &crm.SetIamPolicyRequest{Policy: policy}).Do()
	require.NoError(t, err)

	creator, err := crm.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(ts))
	require.NoError(t, err)
	id := uniquename.New("creator-owns")
	_, err = creator.Projects.Create(&crm.Project{ProjectId: id, Parent: folder}).Do()
	require.NoError(t, err)

	got, err := admin.Projects.GetIamPolicy("projects/"+id, &crm.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	var owners []string
	for _, b := range got.Bindings {
		if b.Role == "roles/owner" {
			owners = append(owners, b.Members...)
		}
	}
	require.Contains(t, owners, member)

	_, err = creator.Projects.Delete("projects/" + id).Do()
	require.NoError(t, err, "the creator, as the project's Owner, deletes it")
}
