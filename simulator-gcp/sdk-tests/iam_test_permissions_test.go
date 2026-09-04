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

	// roles/storage.objectViewer includes the object reads and nothing else,
	// which is what makes the answer separable from the question.
	owner := crmService(t)
	_, err := owner.Projects.SetIamPolicy("test-project", &cloudresourcemanager.SetIamPolicyRequest{
		Policy: &cloudresourcemanager.Policy{
			Bindings: []*cloudresourcemanager.Binding{
				{Role: "roles/storage.objectViewer", Members: []string{member}},
			},
		},
	}).Do()
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
	held, err := asAccount.Projects.TestIamPermissions("test-project",
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
	none, err := asUnbound.Projects.TestIamPermissions("test-project",
		&cloudresourcemanager.TestIamPermissionsRequest{Permissions: asked}).Do()
	require.NoError(t, err)
	assert.Empty(t, none.Permissions, "a principal no binding names holds none of what it asked about")

	// The operator of the account the simulator serves is its owner, and an
	// owner holds what it asks about — which is what real Google answers.
	all, err := owner.Projects.TestIamPermissions("test-project",
		&cloudresourcemanager.TestIamPermissionsRequest{Permissions: asked}).Do()
	require.NoError(t, err)
	assert.Equal(t, asked, all.Permissions, "the account's operator holds what it asks about")
}
