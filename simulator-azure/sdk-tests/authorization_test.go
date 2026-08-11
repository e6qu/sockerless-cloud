package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRoleDefinitionCustomCRUD exercises RoleDefinitions_CreateOrUpdate,
// RoleDefinitions_Get and RoleDefinitions_Delete for a custom role definition.
func TestRoleDefinitionCustomCRUD(t *testing.T) {
	scope := "/subscriptions/" + subscriptionID
	roleDefID := "11111111-2222-3333-4444-555555555555"

	client, err := armauthorization.NewRoleDefinitionsClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)

	created, err := client.CreateOrUpdate(ctx, scope, roleDefID, armauthorization.RoleDefinition{
		Properties: &armauthorization.RoleDefinitionProperties{
			RoleName:    to.Ptr("Sockerless Custom Reader"),
			Description: to.Ptr("Custom read-only role."),
			RoleType:    to.Ptr("CustomRole"),
			Permissions: []*armauthorization.Permission{{
				Actions:    []*string{to.Ptr("Microsoft.Resources/subscriptions/resourceGroups/read")},
				NotActions: []*string{},
			}},
			AssignableScopes: []*string{to.Ptr(scope)},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	assert.Equal(t, "Sockerless Custom Reader", ptrVal(created.Properties.RoleName))
	assert.Equal(t, "CustomRole", ptrVal(created.Properties.RoleType))

	got, err := client.Get(ctx, scope, roleDefID, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	assert.Equal(t, "Sockerless Custom Reader", ptrVal(got.Properties.RoleName))
	require.NotEmpty(t, got.Properties.Permissions)
	assert.Equal(t, []string{"Microsoft.Resources/subscriptions/resourceGroups/read"}, derefSlice(got.Properties.Permissions[0].Actions))

	_, err = client.Delete(ctx, scope, roleDefID, nil)
	require.NoError(t, err)

	_, err = client.Get(ctx, scope, roleDefID, nil)
	require.Error(t, err, "custom role definition must be gone after delete")
}

// TestRoleAssignmentSubscriptionScope exercises RoleAssignments_Create,
// RoleAssignments_Get, RoleAssignments_ListForSubscription and
// RoleAssignments_Delete at subscription scope.
func TestRoleAssignmentSubscriptionScope(t *testing.T) {
	scope := "/subscriptions/" + subscriptionID
	raName := "22222222-3333-4444-5555-666666666666"
	principalID := "33333333-4444-5555-6666-777777777777"
	// Reader built-in role.
	roleDefID := scope + "/providers/Microsoft.Authorization/roleDefinitions/acdd72a7-3385-48ef-bd42-f606fba81ae7"

	client, err := armauthorization.NewRoleAssignmentsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	created, err := client.Create(ctx, scope, raName, armauthorization.RoleAssignmentCreateParameters{
		Properties: &armauthorization.RoleAssignmentProperties{
			RoleDefinitionID: to.Ptr(roleDefID),
			PrincipalID:      to.Ptr(principalID),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	assert.Equal(t, principalID, ptrVal(created.Properties.PrincipalID))

	got, err := client.Get(ctx, scope, raName, nil)
	require.NoError(t, err)
	assert.Equal(t, raName, ptrVal(got.Name))

	pager := client.NewListForSubscriptionPager(nil)
	found := false
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, ra := range page.Value {
			if ptrVal(ra.Name) == raName {
				found = true
			}
		}
	}
	assert.True(t, found, "created assignment must appear in the subscription list")

	_, err = client.Delete(ctx, scope, raName, nil)
	require.NoError(t, err)

	_, err = client.Get(ctx, scope, raName, nil)
	require.Error(t, err, "assignment must be gone after delete")
}

// TestRoleAssignmentResourceGroupScope exercises RoleAssignments_Create and
// RoleAssignments_ListForResourceGroup at resource-group scope, assigning a
// custom role definition to verify custom roles are accepted as assignment
// targets.
func TestRoleAssignmentResourceGroupScope(t *testing.T) {
	rg := "rbac-rg"
	ensureRG(t, rg)
	scope := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg
	roleDefID := "44444444-5555-6666-7777-888888888888"
	raName := "55555555-6666-7777-8888-999999999999"
	principalID := "66666666-7777-8888-9999-aaaaaaaaaaaa"

	defs, err := armauthorization.NewRoleDefinitionsClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = defs.CreateOrUpdate(ctx, "/subscriptions/"+subscriptionID, roleDefID, armauthorization.RoleDefinition{
		Properties: &armauthorization.RoleDefinitionProperties{
			RoleName:         to.Ptr("Sockerless RG Role"),
			RoleType:         to.Ptr("CustomRole"),
			Permissions:      []*armauthorization.Permission{{Actions: []*string{to.Ptr("*/read")}}},
			AssignableScopes: []*string{to.Ptr(scope)},
		},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = defs.Delete(ctx, "/subscriptions/"+subscriptionID, roleDefID, nil) })

	client, err := armauthorization.NewRoleAssignmentsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = client.Create(ctx, scope, raName, armauthorization.RoleAssignmentCreateParameters{
		Properties: &armauthorization.RoleAssignmentProperties{
			RoleDefinitionID: to.Ptr(scope + "/providers/Microsoft.Authorization/roleDefinitions/" + roleDefID),
			PrincipalID:      to.Ptr(principalID),
		},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.Delete(ctx, scope, raName, nil) })

	pager := client.NewListForResourceGroupPager(rg, nil)
	found := false
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, ra := range page.Value {
			if ptrVal(ra.Name) == raName {
				found = true
			}
		}
	}
	assert.True(t, found, "created assignment must appear in the resource-group list")
}

func derefSlice(in []*string) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		out = append(out, ptrVal(p))
	}
	return out
}
