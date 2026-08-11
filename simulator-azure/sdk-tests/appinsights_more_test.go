package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/applicationinsights/armapplicationinsights"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAzureAppInsights_ListUpdateTagsAndPurge(t *testing.T) {
	rg := "appinsights-more-rg"
	name := "appinsights-more-comp"
	createResourceGroup(t, rg)

	client, err := armapplicationinsights.NewComponentsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	_, err = client.CreateOrUpdate(ctx, rg, name, armapplicationinsights.Component{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("web"),
		Properties: &armapplicationinsights.ComponentProperties{
			ApplicationType: to.Ptr(armapplicationinsights.ApplicationTypeWeb),
		},
	}, nil)
	require.NoError(t, err)

	t.Run("ListByResourceGroupAndSubscription", func(t *testing.T) {
		rgPager := client.NewListByResourceGroupPager(rg, nil)
		rgPage, err := rgPager.NextPage(ctx)
		require.NoError(t, err)
		found := false
		for _, c := range rgPage.Value {
			if *c.Name == name {
				found = true
			}
		}
		assert.True(t, found, "component should appear in resource-group list")

		subPager := client.NewListPager(nil)
		subPage, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		assert.NotEmpty(t, subPage.Value)
	})

	t.Run("UpdateTags", func(t *testing.T) {
		resp, err := client.UpdateTags(ctx, rg, name, armapplicationinsights.TagsResource{
			Tags: map[string]*string{"owner": to.Ptr("observability")},
		}, nil)
		require.NoError(t, err)
		assert.Equal(t, "observability", *resp.Tags["owner"])
	})

	t.Run("PurgeAndStatus", func(t *testing.T) {
		purge, err := client.Purge(ctx, rg, name, armapplicationinsights.ComponentPurgeBody{
			Table: to.Ptr("traces"),
			Filters: []*armapplicationinsights.ComponentPurgeBodyFilters{{
				Column:   to.Ptr("timestamp"),
				Operator: to.Ptr("<"),
				Value:    "2030-01-01",
			}},
		}, nil)
		require.NoError(t, err)
		require.NotNil(t, purge.OperationID)

		status, err := client.GetPurgeStatus(ctx, rg, name, *purge.OperationID, nil)
		require.NoError(t, err)
		assert.Equal(t, armapplicationinsights.PurgeState("completed"), *status.Status)
	})
}
