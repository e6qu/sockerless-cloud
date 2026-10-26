package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// App Service recommendations at subscription, environment and site scope.
//
//	GET  /subscriptions/{subscriptionId}/providers/Microsoft.Web/recommendations
//	POST .../providers/Microsoft.Web/recommendations/reset
//	POST .../providers/Microsoft.Web/recommendations/{name}/disable
//	GET  .../hostingEnvironments/{hostingEnvironmentName}/recommendations
//	GET  .../hostingEnvironments/{hostingEnvironmentName}/recommendationHistory
//	POST .../hostingEnvironments/{hostingEnvironmentName}/recommendations/{disable,reset}
//	POST .../hostingEnvironments/{hostingEnvironmentName}/recommendations/{name}/disable
//	GET  .../hostingEnvironments/{hostingEnvironmentName}/recommendations/{name}
//	GET  .../sites/{siteName}/recommendations
//	GET  .../sites/{siteName}/recommendationHistory
//	POST .../sites/{siteName}/recommendations/{disable,reset}
//	POST .../sites/{siteName}/recommendations/{name}/disable
//	GET  .../sites/{siteName}/recommendations/{name}

// The simulator runs no advisory engine, so a scope has no recommendations or
// history. The client's filters are recorded.
func TestSDK_Recommendations_SubscriptionAndSiteScopes(t *testing.T) {
	t.Parallel()
	rg, name := "sdk-recommendation-rg", "sdk-recommendation-app"
	ensureRG(t, rg)

	sites, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = sites.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)

	client, err := armappservice.NewRecommendationsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// Nothing was observed in the subscription.
	page, err := client.NewListPager(nil).NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, page.Value, "a subscription the simulator observed nothing about has no recommendations")

	// A subscription-wide filter is recorded, and reset clears it.
	_, err = client.DisableRecommendationForSubscription(ctx, "AppDensity", nil)
	require.NoError(t, err)
	_, err = client.ResetAllFilters(ctx, nil)
	require.NoError(t, err)

	// The site's recommendations and history are empty.
	sitePage, err := client.NewListRecommendedRulesForWebAppPager(rg, name, nil).NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, sitePage.Value, "a site the simulator observed nothing about has no recommended rules")

	historyPage, err := client.NewListHistoryForWebAppPager(rg, name, nil).NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, historyPage.Value, "a site with no recommendation has no history of one either")

	// The site's filters: one rule, then every rule, then cleared.
	_, err = client.DisableRecommendationForSite(ctx, rg, name, "AppDensity", nil)
	require.NoError(t, err)
	_, err = client.DisableAllForWebApp(ctx, rg, name, nil)
	require.NoError(t, err)
	_, err = client.ResetAllFiltersForWebApp(ctx, rg, name, nil)
	require.NoError(t, err)

	// The listing says this site has no recommendations, so there is no rule to
	// read, and the read says so.
	_, err = client.GetRuleDetailsByWebApp(ctx, rg, name, "AppDensity", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound",
		"a rule the listing does not carry is not found, not unimplemented")
	assert.NotContains(t, err.Error(), "NotImplemented")

	// Each site-scoped operation addresses a site, so a missing one is refused,
	// not answered with an empty list.
	_, err = client.NewListRecommendedRulesForWebAppPager(rg, "sdk-recommendation-absent", nil).NextPage(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")

	_, err = client.DisableAllForWebApp(ctx, rg, "sdk-recommendation-absent", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}

// The App Service Environment scope answers the same way.
func TestSDK_Recommendations_HostingEnvironmentScope(t *testing.T) {
	t.Parallel()
	rg, name := "sdk-recommendation-ase-rg", "sdk-recommendation-ase"
	ensureRG(t, rg)
	subnetID := requireSubnet(t, rg, "sdk-recommendation-vnet", "10.61.0.0/16", "ase-subnet", "10.61.1.0/24")

	envs := environmentsClient(t)
	poller, err := envs.BeginCreateOrUpdate(ctx, rg, name, armappservice.EnvironmentResource{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("ASEV3"),
		Properties: &armappservice.Environment{
			VirtualNetwork:            &armappservice.VirtualNetworkProfile{ID: to.Ptr(subnetID)},
			InternalLoadBalancingMode: to.Ptr(armappservice.LoadBalancingModeWebPublishing),
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	client, err := armappservice.NewRecommendationsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	page, err := client.NewListRecommendedRulesForHostingEnvironmentPager(rg, name, nil).NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, page.Value, "an environment the simulator observed nothing about has no recommended rules")

	history, err := client.NewListHistoryForHostingEnvironmentPager(rg, name, nil).NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, history.Value, "an environment with no recommendation has no history of one either")

	// The SDK sends the environment's name twice, in the path and as the
	// environmentName query parameter, as the operation declares.
	_, err = client.DisableRecommendationForHostingEnvironment(ctx, rg, name, "AppDensity", name, nil)
	require.NoError(t, err)
	_, err = client.DisableAllForHostingEnvironment(ctx, rg, name, name, nil)
	require.NoError(t, err)
	_, err = client.ResetAllFiltersForHostingEnvironment(ctx, rg, name, name, nil)
	require.NoError(t, err)

	// The rule read comes from the listing's collection, which is empty.
	_, err = client.GetRuleDetailsByHostingEnvironment(ctx, rg, name, "AppDensity", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
	assert.NotContains(t, err.Error(), "NotImplemented")

	// An environment that was never created is refused.
	_, err = client.NewListHistoryForHostingEnvironmentPager(rg, "sdk-recommendation-ase-absent", nil).NextPage(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}
