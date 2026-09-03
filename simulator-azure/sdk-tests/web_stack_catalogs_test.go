package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSDK_Web_RuntimeStackCatalogsAreEmptyBecauseThisPlatformSuppliesNoStacks
// covers all six spellings of App Service's runtime-stack catalogs.
//
// The catalogs report which built-in runtime stacks the App Service offers.
// This one offers none: a site here runs the container image its
// linuxFxVersion names, and a site configured with a stack instead ("PHP|8.2")
// cannot start, because the platform image that stack names is Microsoft's.
// An empty collection states that, and it is the same fact the site path
// refuses on, so the two cannot come to disagree.
//
// The pagers below drive, in order:
//
//	GET /providers/Microsoft.Web/availableStacks
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/availableStacks
//	GET /providers/Microsoft.Web/webAppStacks
//	GET /providers/Microsoft.Web/functionAppStacks
//	GET /providers/Microsoft.Web/locations/{location}/webAppStacks
//	GET /providers/Microsoft.Web/locations/{location}/functionAppStacks
func TestSDK_Web_RuntimeStackCatalogsAreEmptyBecauseThisPlatformSuppliesNoStacks(t *testing.T) {
	provider, err := armappservice.NewProviderClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	t.Run("GetAvailableStacks", func(t *testing.T) {
		pager := provider.NewGetAvailableStacksPager(nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			assert.Empty(t, page.Value, "this App Service supplies no platform images")
		}
	})

	t.Run("GetAvailableStacksOnPrem", func(t *testing.T) {
		pager := provider.NewGetAvailableStacksOnPremPager(nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			assert.Empty(t, page.Value, "this App Service supplies no platform images")
		}
	})

	t.Run("GetWebAppStacks", func(t *testing.T) {
		pager := provider.NewGetWebAppStacksPager(nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			assert.Empty(t, page.Value, "no built-in web app stack runs here")
		}
	})

	t.Run("GetFunctionAppStacks", func(t *testing.T) {
		pager := provider.NewGetFunctionAppStacksPager(nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			assert.Empty(t, page.Value, "no built-in function app stack runs here")
		}
	})

	t.Run("GetWebAppStacksForLocation", func(t *testing.T) {
		pager := provider.NewGetWebAppStacksForLocationPager("eastus", nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			assert.Empty(t, page.Value, "a location offers no stack this platform does not supply")
		}
	})

	t.Run("GetFunctionAppStacksForLocation", func(t *testing.T) {
		pager := provider.NewGetFunctionAppStacksForLocationPager("eastus", nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			assert.Empty(t, page.Value, "a location offers no stack this platform does not supply")
		}
	})
}
