package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSDK_Web_RuntimeStackCatalogsListTheStacksSitesRun covers all six
// spellings of App Service's runtime-stack catalogs.
//
// The catalogs report the built-in runtime stacks the App Service runs: the
// Linux Node and Python stacks whose platform images a site on them starts.
// A Windows selection lists none, and the function app catalog lists none,
// because this App Service does not run the Azure Functions host.
//
// Each subtest drives one spelling:
//
//	GetAvailableStacks               GET /providers/Microsoft.Web/availableStacks
//	GetAvailableStacksOnPrem         GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/availableStacks
//	GetWebAppStacks                  GET /providers/Microsoft.Web/webAppStacks
//	GetFunctionAppStacks             GET /providers/Microsoft.Web/functionAppStacks
//	GetWebAppStacksForLocation       GET /providers/Microsoft.Web/locations/{location}/webAppStacks
//	GetFunctionAppStacksForLocation  GET /providers/Microsoft.Web/locations/{location}/functionAppStacks
func TestSDK_Web_RuntimeStackCatalogsListTheStacksSitesRun(t *testing.T) {
	provider, err := armappservice.NewProviderClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	want := []string{"NODE|22-lts", "NODE|20-lts", "PYTHON|3.12"}

	availableRuntimes := func(stacks []*armappservice.ApplicationStackResource) []string {
		var out []string
		for _, s := range stacks {
			for _, mv := range s.Properties.MajorVersions {
				out = append(out, *mv.RuntimeVersion)
			}
		}
		return out
	}
	webAppRuntimes := func(stacks []*armappservice.WebAppStack) []string {
		var out []string
		for _, s := range stacks {
			require.NotNil(t, s.Properties.PreferredOs)
			assert.Equal(t, armappservice.StackPreferredOsLinux, *s.Properties.PreferredOs)
			for _, mv := range s.Properties.MajorVersions {
				for _, minor := range mv.MinorVersions {
					out = append(out, *minor.StackSettings.LinuxRuntimeSettings.RuntimeVersion)
				}
			}
		}
		return out
	}

	t.Run("GetAvailableStacks", func(t *testing.T) {
		var got []*armappservice.ApplicationStackResource
		pager := provider.NewGetAvailableStacksPager(&armappservice.ProviderClientGetAvailableStacksOptions{
			OSTypeSelected: to.Ptr(armappservice.ProviderOsTypeSelectedLinux),
		})
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			got = append(got, page.Value...)
		}
		assert.Equal(t, want, availableRuntimes(got))

		windows := provider.NewGetAvailableStacksPager(&armappservice.ProviderClientGetAvailableStacksOptions{
			OSTypeSelected: to.Ptr(armappservice.ProviderOsTypeSelectedWindows),
		})
		for windows.More() {
			page, err := windows.NextPage(ctx)
			require.NoError(t, err)
			assert.Empty(t, page.Value, "every stack here is a Linux stack")
		}
	})

	t.Run("GetAvailableStacksOnPrem", func(t *testing.T) {
		var got []*armappservice.ApplicationStackResource
		pager := provider.NewGetAvailableStacksOnPremPager(nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			got = append(got, page.Value...)
		}
		assert.Equal(t, want, availableRuntimes(got))
	})

	t.Run("GetWebAppStacks", func(t *testing.T) {
		var got []*armappservice.WebAppStack
		pager := provider.NewGetWebAppStacksPager(&armappservice.ProviderClientGetWebAppStacksOptions{
			StackOsType: to.Ptr(armappservice.ProviderStackOsTypeLinux),
		})
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			got = append(got, page.Value...)
		}
		assert.Equal(t, want, webAppRuntimes(got))
	})

	t.Run("GetFunctionAppStacks", func(t *testing.T) {
		pager := provider.NewGetFunctionAppStacksPager(nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			assert.Empty(t, page.Value, "this App Service runs no Functions host")
		}
	})

	t.Run("GetWebAppStacksForLocation", func(t *testing.T) {
		var got []*armappservice.WebAppStack
		pager := provider.NewGetWebAppStacksForLocationPager("eastus", nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			got = append(got, page.Value...)
		}
		assert.Equal(t, want, webAppRuntimes(got))
		for _, s := range got {
			require.NotNil(t, s.Location)
			assert.Equal(t, "eastus", *s.Location)
		}
	})

	t.Run("GetFunctionAppStacksForLocation", func(t *testing.T) {
		pager := provider.NewGetFunctionAppStacksForLocationPager("eastus", nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			assert.Empty(t, page.Value, "this App Service runs no Functions host")
		}
	})
}
