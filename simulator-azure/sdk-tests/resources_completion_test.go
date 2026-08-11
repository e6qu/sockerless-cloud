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
}
