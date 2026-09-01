package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAzureResources_CompletionSurfaces exercises the completion of the
// Microsoft.Resources control plane: tag management at an arbitrary ARM scope
// (CreateOrUpdateAtScope / GetAtScope / UpdateAtScope / DeleteAtScope) and
// resource-provider registration at management-group scope.
func TestAzureResources_CompletionSurfaces(t *testing.T) {
	rg := "resources-completion-rg"
	createResourceGroup(t, rg)
	cred := &fakeCredential{}

	t.Run("TagsAtScope", func(t *testing.T) {
		client, err := armresources.NewTagsClient(subscriptionID, cred, clientOpts())
		require.NoError(t, err)
		scope := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg

		put, err := client.CreateOrUpdateAtScope(ctx, scope, armresources.TagsResource{
			Properties: &armresources.Tags{Tags: map[string]*string{"team": to.Ptr("data")}},
		}, nil)
		require.NoError(t, err)
		assert.Equal(t, "data", *put.Properties.Tags["team"])

		got, err := client.GetAtScope(ctx, scope, nil)
		require.NoError(t, err)
		assert.Equal(t, "data", *got.Properties.Tags["team"])

		patched, err := client.UpdateAtScope(ctx, scope, armresources.TagsPatchResource{
			Operation:  to.Ptr(armresources.TagsPatchOperationMerge),
			Properties: &armresources.Tags{Tags: map[string]*string{"costcenter": to.Ptr("42")}},
		}, nil)
		require.NoError(t, err)
		assert.Equal(t, "42", *patched.Properties.Tags["costcenter"])

		_, err = client.DeleteAtScope(ctx, scope, nil)
		require.NoError(t, err)
	})

	t.Run("RegisterAtManagementGroupScope", func(t *testing.T) {
		client, err := armresources.NewProvidersClient(subscriptionID, cred, clientOpts())
		require.NoError(t, err)
		_, err = client.RegisterAtManagementGroupScope(ctx, "Microsoft.Storage", "mygroup", nil)
		require.NoError(t, err)
	})

	// Registering and unregistering a provider have to survive the call. A
	// client registers precisely so the read that follows says Registered, and
	// Terraform polls that read — an unregister whose state reverts on the next
	// GET reports work that did not happen.
	t.Run("RegistrationStateSurvivesTheCall", func(t *testing.T) {
		client, err := armresources.NewProvidersClient(subscriptionID, cred, clientOpts())
		require.NoError(t, err)
		const ns = "Microsoft.EventGrid"

		unregistered, err := client.Unregister(ctx, ns, nil)
		require.NoError(t, err)
		require.NotNil(t, unregistered.RegistrationState)
		assert.Equal(t, "Unregistered", *unregistered.RegistrationState)

		after, err := client.Get(ctx, ns, nil)
		require.NoError(t, err)
		require.NotNil(t, after.RegistrationState)
		assert.Equal(t, "Unregistered", *after.RegistrationState,
			"the state a read reports must be the one the unregister left")

		registered, err := client.Register(ctx, ns, nil)
		require.NoError(t, err)
		require.NotNil(t, registered.RegistrationState)
		assert.Equal(t, "Registered", *registered.RegistrationState)

		back, err := client.Get(ctx, ns, nil)
		require.NoError(t, err)
		require.NotNil(t, back.RegistrationState)
		assert.Equal(t, "Registered", *back.RegistrationState)

		// A namespace this subscription never unregistered is registered, and
		// one subscription's choice is not another's.
		other, err := client.Get(ctx, "Microsoft.Storage", nil)
		require.NoError(t, err)
		require.NotNil(t, other.RegistrationState)
		assert.Equal(t, "Registered", *other.RegistrationState)
	})
}
