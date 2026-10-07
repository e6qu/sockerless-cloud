package azure_sdk_test

import (
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSDK_SiteContainers_EnvironmentVariablesNameAppSettings holds a
// sitecontainer's environmentVariables to Microsoft.Web's contract: "The value
// of this environment variable must be the name of an AppSetting. The actual
// value of the environment variable in container will be retrieved from the
// specified AppSetting at runtime. If the AppSetting is not found, the value
// will be set to an empty string in the container at runtime." An app setting
// that is a Key Vault reference reaches the container as the secret's value.
func TestSDK_SiteContainers_EnvironmentVariablesNameAppSettings(t *testing.T) {
	rg, name, vault := "sdk-sc-env-rg", "sdk-sc-env-app", "sdkscenvvault"
	ensureRG(t, rg)
	vaults, err := armkeyvault.NewVaultsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vaultPoller, err := vaults.BeginCreateOrUpdate(ctx, rg, vault, armkeyvault.VaultCreateOrUpdateParameters{
		Location: to.Ptr("eastus"),
		Properties: &armkeyvault.VaultProperties{
			TenantID: to.Ptr(simTenantID),
			SKU:      &armkeyvault.SKU{Family: to.Ptr(armkeyvault.SKUFamilyA), Name: to.Ptr(armkeyvault.SKUNameStandard)},
			AccessPolicies: []*armkeyvault.AccessPolicyEntry{{
				TenantID:    to.Ptr(simTenantID),
				ObjectID:    to.Ptr(simCallerObjectID),
				Permissions: &armkeyvault.Permissions{Secrets: []*armkeyvault.SecretPermissions{to.Ptr(armkeyvault.SecretPermissionsSet)}},
			}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = vaultPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = vaults.Delete(ctx, rg, vault, nil) })
	secrets, err := azsecrets.NewClient(kvVaultURL(vault), &fakeCredential{}, &azsecrets.ClientOptions{
		ClientOptions:                        kvClientOptions(),
		DisableChallengeResourceVerification: true,
	})
	require.NoError(t, err)
	_, err = secrets.SetSecret(ctx, "db-password", azsecrets.SetSecretParameters{Value: to.Ptr("from-the-vault")}, nil)
	require.NoError(t, err)

	planID := webMoreEnsurePlan(t, rg, name+"-plan")
	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	t.Cleanup(func() { azureDeleteSite(rg, name) })
	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("app,linux,sitecontainers"),
		Identity: &armappservice.ManagedServiceIdentity{Type: to.Ptr(armappservice.ManagedServiceIdentityTypeSystemAssigned)},
		Properties: &armappservice.SiteProperties{
			ServerFarmID: to.Ptr(planID),
			SiteConfig: &armappservice.SiteConfig{
				LinuxFxVersion: to.Ptr("SITECONTAINERS"),
				AppSettings: slotSettings(map[string]string{
					"GREETING_SETTING": "hello from an app setting",
					"DB_SETTING":       "@Microsoft.KeyVault(VaultName=" + vault + ";SecretName=db-password)",
				}),
			},
		},
	}, nil)
	require.NoError(t, err)
	site, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = vaults.UpdateAccessPolicy(ctx, rg, vault, armkeyvault.AccessPolicyUpdateKindAdd, armkeyvault.VaultAccessPolicyParameters{
		Properties: &armkeyvault.VaultAccessPolicyProperties{AccessPolicies: []*armkeyvault.AccessPolicyEntry{{
			TenantID:    to.Ptr(simTenantID),
			ObjectID:    site.Identity.PrincipalID,
			Permissions: &armkeyvault.Permissions{Secrets: []*armkeyvault.SecretPermissions{to.Ptr(armkeyvault.SecretPermissionsGet)}},
		}}},
	}, nil)
	require.NoError(t, err)

	_, err = client.CreateOrUpdateSiteContainer(ctx, rg, name, "main", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:          to.Ptr(commandImageName),
			IsMain:         to.Ptr(true),
			TargetPort:     to.Ptr("80"),
			StartUpCommand: to.Ptr(filesHTTPCommand),
			EnvironmentVariables: []*armappservice.EnvironmentVariable{
				{Name: to.Ptr("GREETING"), Value: to.Ptr("GREETING_SETTING")},
				{Name: to.Ptr("DB"), Value: to.Ptr("DB_SETTING")},
				{Name: to.Ptr("ABSENT"), Value: to.Ptr("NO_SUCH_SETTING")},
			},
		},
	}, nil)
	require.NoError(t, err)
	got, err := client.GetSiteContainer(ctx, rg, name, "main", nil)
	require.NoError(t, err)
	require.Len(t, got.Properties.EnvironmentVariables, 3)
	assert.Equal(t, "GREETING_SETTING", *got.Properties.EnvironmentVariables[0].Value, "the resource keeps the setting's name")
	require.NotNil(t, got.Properties.InheritAppSettingsAndConnectionStrings)
	assert.True(t, *got.Properties.InheritAppSettingsAndConnectionStrings, "inheritAppSettingsAndConnectionStrings defaults to true")

	assert.Equal(t, "hello from an app setting", siteEnv(t, name, "GREETING"))
	assert.Equal(t, "from-the-vault", siteEnv(t, name, "DB"), "a Key Vault reference resolves through the setting")
	assert.Equal(t, "", siteEnv(t, name, "ABSENT"), "a setting that does not exist is an empty string")
	assert.Equal(t, "hello from an app setting", siteEnv(t, name, "GREETING_SETTING"), "a main that inherits by default gets every app setting")
	assert.Equal(t, "from-the-vault", siteEnv(t, name, "DB_SETTING"), "an inherited Key Vault reference resolves")
}

// createInheritanceSite creates a sitecontainers web app with one app setting
// and one connection string, and returns its Web Apps client.
func createInheritanceSite(t *testing.T, rg, name string) *armappservice.WebAppsClient {
	t.Helper()
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, name+"-plan")
	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	t.Cleanup(func() { azureDeleteSite(rg, name) })
	poller, err := client.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
		Location: to.Ptr("eastus"),
		Kind:     to.Ptr("app,linux,sitecontainers"),
		Properties: &armappservice.SiteProperties{
			ServerFarmID: to.Ptr(planID),
			SiteConfig: &armappservice.SiteConfig{
				LinuxFxVersion: to.Ptr("SITECONTAINERS"),
				AppSettings:    slotSettings(map[string]string{"GREETING_SETTING": "hello from an app setting"}),
				ConnectionStrings: []*armappservice.ConnStringInfo{{
					Name:             to.Ptr("Orders"),
					ConnectionString: to.Ptr("Server=orders"),
					Type:             to.Ptr(armappservice.ConnectionStringTypeCustom),
				}},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return client
}

// TestSDK_SiteContainers_MainThatDoesNotInheritGetsNoAppSettings holds a main
// sitecontainer to Microsoft.Web's inheritAppSettingsAndConnectionStrings:
// "true if all AppSettings and ConnectionStrings have to be passed to the
// container as environment variables; false otherwise." A main that sets it
// false gets neither, and still gets the platform's own variables.
func TestSDK_SiteContainers_MainThatDoesNotInheritGetsNoAppSettings(t *testing.T) {
	rg, name := "sdk-sc-noinherit-rg", "sdk-sc-noinherit-app"
	client := createInheritanceSite(t, rg, name)
	_, err := client.CreateOrUpdateSiteContainer(ctx, rg, name, "main", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:                                  to.Ptr(commandImageName),
			IsMain:                                 to.Ptr(true),
			TargetPort:                             to.Ptr("80"),
			StartUpCommand:                         to.Ptr(filesHTTPCommand),
			InheritAppSettingsAndConnectionStrings: to.Ptr(false),
		},
	}, nil)
	require.NoError(t, err)
	got, err := client.GetSiteContainer(ctx, rg, name, "main", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties.InheritAppSettingsAndConnectionStrings)
	assert.False(t, *got.Properties.InheritAppSettingsAndConnectionStrings)

	assert.Equal(t, name, siteEnv(t, name, "WEBSITE_SITE_NAME"), "the platform's variables reach the main")
	status, body := siteGet(t, name, "/env/GREETING_SETTING")
	assert.Equal(t, http.StatusNotFound, status, "a main that does not inherit gets no app setting: %s", body)
	status, body = siteGet(t, name, "/env/CUSTOMCONNSTR_Orders")
	assert.Equal(t, http.StatusNotFound, status, "a main that does not inherit gets no connection string: %s", body)
}

// TestSDK_SiteContainers_SidecarInheritsAppSettingsUnlessFalse gives a sidecar
// the app settings and connection strings unless its
// inheritAppSettingsAndConnectionStrings is false; unset, it defaults to true. The main relays requests to
// each sidecar's files-http over the shared loopback.
func TestSDK_SiteContainers_SidecarInheritsAppSettingsUnlessFalse(t *testing.T) {
	rg, name := "sdk-sc-inherit-rg", "sdk-sc-inherit-app"
	client := createInheritanceSite(t, rg, name)
	for _, sc := range []struct {
		name  string
		props armappservice.SiteContainerProperties
	}{
		{"main", armappservice.SiteContainerProperties{
			Image: to.Ptr(httpProbeImageName), IsMain: to.Ptr(true), TargetPort: to.Ptr("8080"), StartUpCommand: to.Ptr("relay-local"),
		}},
		{"inherits", armappservice.SiteContainerProperties{
			Image: to.Ptr(commandImageName), IsMain: to.Ptr(false), TargetPort: to.Ptr("9090"),
			StartUpCommand: to.Ptr("files-http 9090 /tmp"),
		}},
		{"isolated", armappservice.SiteContainerProperties{
			Image: to.Ptr(commandImageName), IsMain: to.Ptr(false), TargetPort: to.Ptr("9091"),
			StartUpCommand: to.Ptr("files-http 9091 /tmp"), InheritAppSettingsAndConnectionStrings: to.Ptr(false),
		}},
	} {
		_, err := client.CreateOrUpdateSiteContainer(ctx, rg, name, sc.name, armappservice.SiteContainer{Properties: &sc.props}, nil)
		require.NoError(t, err)
	}

	status, body := siteGet(t, name, "/9090/env/GREETING_SETTING")
	assert.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "hello from an app setting", body, "a sidecar that inherits gets every app setting")
	status, body = siteGet(t, name, "/9090/env/CUSTOMCONNSTR_Orders")
	assert.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "Server=orders", body, "a sidecar that inherits gets every connection string")
	status, body = siteGet(t, name, "/9091/env/GREETING_SETTING")
	assert.Equal(t, http.StatusNotFound, status, "a sidecar that does not inherit gets no app setting: %s", body)
	status, body = siteGet(t, name, "/9091/env/CUSTOMCONNSTR_Orders")
	assert.Equal(t, http.StatusNotFound, status, "a sidecar that does not inherit gets no connection string: %s", body)
}
