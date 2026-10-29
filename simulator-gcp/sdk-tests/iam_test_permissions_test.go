package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/option"
)

// TestIAM_TestPermissionsAnswersFromThePolicyNotTheQuestion covers what
// testIamPermissions is for: telling a caller which of a set of permissions it
// actually holds, so it can decide whether to offer an action before trying it.
//
// The operation used to return the set it was handed, unchanged, so a caller
// bound to nothing got the same reply as an owner and the answer carried no
// information. Here a service account is bound to one role, and asks about a
// permission that role includes and one it does not.
func TestIAM_TestPermissionsAnswersFromThePolicyNotTheQuestion(t *testing.T) {
	_, _, keyFile := mintServiceAccountKeyFile(t, "test-permissions-sa")
	credentials := tokenSourceFromKeyFile(t, keyFile)
	const member = "serviceAccount:test-permissions-sa@test-project.iam.gserviceaccount.com"
	const project = "test-permissions-project"
	ensureProject(t, project)

	// roles/storage.objectViewer includes the object reads and nothing else,
	// which is what makes the answer separable from the question.
	owner := crmService(t)
	policy, err := owner.Projects.GetIamPolicy(project, &cloudresourcemanager.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	policy.Bindings = append(policy.Bindings, &cloudresourcemanager.Binding{Role: "roles/storage.objectViewer", Members: []string{member}})
	_, err = owner.Projects.SetIamPolicy(project, &cloudresourcemanager.SetIamPolicyRequest{Policy: policy}).Do()
	require.NoError(t, err)

	asAccount, err := cloudresourcemanager.NewService(ctx,
		option.WithEndpoint(baseURL), option.WithTokenSource(credentials.TokenSource))
	require.NoError(t, err)

	asked := []string{
		"storage.objects.get",             // the bound role includes this
		"storage.objects.list",            // and this
		"storage.buckets.update",          // and not this
		"resourcemanager.projects.delete", // nor this
	}
	held, err := asAccount.Projects.TestIamPermissions(project,
		&cloudresourcemanager.TestIamPermissionsRequest{Permissions: asked}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"storage.objects.get", "storage.objects.list"}, held.Permissions,
		"the answer is the permissions the bound role includes, not the set that was asked about")

	// A principal bound to nothing holds nothing, which is the reply the echo
	// could never give.
	_, _, unboundKey := mintServiceAccountKeyFile(t, "test-permissions-unbound-sa")
	unbound := tokenSourceFromKeyFile(t, unboundKey)
	asUnbound, err := cloudresourcemanager.NewService(ctx,
		option.WithEndpoint(baseURL), option.WithTokenSource(unbound.TokenSource))
	require.NoError(t, err)
	none, err := asUnbound.Projects.TestIamPermissions(project,
		&cloudresourcemanager.TestIamPermissionsRequest{Permissions: asked}).Do()
	require.NoError(t, err)
	assert.Empty(t, none.Permissions, "a principal no binding names holds none of what it asked about")

	// The bootstrap account holds Owner through the organization. Owner holds
	// a Cloud Storage object or bucket permission only through a bucket's
	// convenience members, never on the project itself.
	all, err := owner.Projects.TestIamPermissions(project,
		&cloudresourcemanager.TestIamPermissionsRequest{Permissions: asked}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"resourcemanager.projects.delete"}, all.Permissions,
		"an inherited Owner holds the project's own permissions and none of Cloud Storage's")
}
