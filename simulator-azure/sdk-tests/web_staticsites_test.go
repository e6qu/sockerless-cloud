package azure_sdk_test

import (
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// web_staticsites_test.go exercises the full StaticSites_* family in
// simulator-azure/web_staticsites.go through armappservice's
// StaticSitesClient: resource CRUD, builds + zip-deployment LROs,
// application settings in both spellings, custom domains with DNS-backed
// validation, users/invitations/roles, secrets + API-key reset, basic auth,
// database connections, linked backends, user-provided function apps,
// private endpoint connections + private link resources, workflow preview
// and detach. Single resources and collections are both read back so the
// spec-shape validator sees each response shape.

func staticSitesClient(t *testing.T) *armappservice.StaticSitesClient {
	t.Helper()
	client, err := armappservice.NewStaticSitesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	return client
}

func staticSitesCreate(t *testing.T, client *armappservice.StaticSitesClient, rg, name string) armappservice.StaticSiteARMResource {
	t.Helper()
	poller, err := client.BeginCreateOrUpdateStaticSite(ctx, rg, name, armappservice.StaticSiteARMResource{
		Location: to.Ptr("eastus"),
		SKU:      &armappservice.SKUDescription{Name: to.Ptr("Standard"), Tier: to.Ptr("Standard")},
		Properties: &armappservice.StaticSite{
			RepositoryURL: to.Ptr("https://github.com/example/swa"),
			Branch:        to.Ptr("main"),
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return created.StaticSiteARMResource
}

func TestSDK_StaticSites_CRUDSettingsSecrets(t *testing.T) {
	rg := "swa-sdk-rg"
	ensureRG(t, rg)
	client := staticSitesClient(t)
	name := "sdk-swa-crud"

	created := staticSitesCreate(t, client, rg, name)
	require.NotNil(t, created.Properties)
	assert.Equal(t, name+".azurestaticapps.net", *created.Properties.DefaultHostname)

	// A PUT without a location is rejected the way ARM rejects it.
	_, err := client.BeginCreateOrUpdateStaticSite(ctx, rg, "sdk-swa-nolocation", armappservice.StaticSiteARMResource{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LocationRequired")

	// Read back: defaults real Azure applies on create.
	got, err := client.GetStaticSite(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, "None", *got.Properties.Provider)
	assert.Equal(t, armappservice.StagingEnvironmentPolicyEnabled, *got.Properties.StagingEnvironmentPolicy)
	assert.True(t, *got.Properties.AllowConfigFileUpdates)

	// Collection shapes: by resource group and by subscription.
	foundRG := false
	rgPager := client.NewGetStaticSitesByResourceGroupPager(rg, nil)
	for rgPager.More() {
		page, err := rgPager.NextPage(ctx)
		require.NoError(t, err)
		for _, s := range page.Value {
			if *s.Name == name {
				foundRG = true
			}
		}
	}
	assert.True(t, foundRG)
	foundSub := false
	subPager := client.NewListPager(nil)
	for subPager.More() {
		page, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		for _, s := range page.Value {
			if *s.Name == name {
				foundSub = true
			}
		}
	}
	assert.True(t, foundSub)

	// PATCH.
	upd, err := client.UpdateStaticSite(ctx, rg, name, armappservice.StaticSitePatchResource{
		Properties: &armappservice.StaticSite{Branch: to.Ptr("release")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "release", *upd.Properties.Branch)

	// Application settings: the appsettings and functionappsettings routes
	// address the same bag (functionappsettings is the pre-rename spelling),
	// exactly as their swagger descriptions state.
	_, err = client.CreateOrUpdateStaticSiteAppSettings(ctx, rg, name, armappservice.StringDictionary{
		Properties: map[string]*string{"WORKER_COUNT": to.Ptr("2")},
	}, nil)
	require.NoError(t, err)
	appSettings, err := client.ListStaticSiteAppSettings(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, appSettings.Properties["WORKER_COUNT"])
	assert.Equal(t, "2", *appSettings.Properties["WORKER_COUNT"])

	_, err = client.CreateOrUpdateStaticSiteFunctionAppSettings(ctx, rg, name, armappservice.StringDictionary{
		Properties: map[string]*string{"FUNC_SETTING": to.Ptr("on")},
	}, nil)
	require.NoError(t, err)
	fnSettings, err := client.ListStaticSiteFunctionAppSettings(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, fnSettings.Properties["FUNC_SETTING"])
	sharedBag, err := client.ListStaticSiteAppSettings(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, sharedBag.Properties["FUNC_SETTING"])
	assert.Nil(t, sharedBag.Properties["WORKER_COUNT"])

	// Secrets: the deployment API key exists from creation and rotates on
	// reset.
	secrets, err := client.ListStaticSiteSecrets(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, secrets.Properties["apiKey"])
	firstKey := *secrets.Properties["apiKey"]
	assert.NotEmpty(t, firstKey)
	_, err = client.ResetStaticSiteAPIKey(ctx, rg, name, armappservice.StaticSiteResetPropertiesARMResource{
		Properties: &armappservice.StaticSiteResetPropertiesARMResourceProperties{
			ShouldUpdateRepository: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	secrets, err = client.ListStaticSiteSecrets(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.NotEqual(t, firstKey, *secrets.Properties["apiKey"])

	// Detach disconnects the repository.
	detach, err := client.BeginDetachStaticSite(ctx, rg, name, nil)
	require.NoError(t, err)
	_, err = detach.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	got, err = client.GetStaticSite(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Nil(t, got.Properties.RepositoryURL)
	assert.Equal(t, "None", *got.Properties.Provider)

	// Delete; a later GET reports the canonical ARM 404.
	del, err := client.BeginDeleteStaticSite(ctx, rg, name, nil)
	require.NoError(t, err)
	_, err = del.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.GetStaticSite(ctx, rg, name, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}

func TestSDK_StaticSites_BuildsAndZipDeploy(t *testing.T) {
	rg := "swa-sdk-rg"
	ensureRG(t, rg)
	client := staticSitesClient(t)
	name := "sdk-swa-builds"
	staticSitesCreate(t, client, rg, name)

	// The production build environment exists from creation.
	builds := []*armappservice.StaticSiteBuildARMResource{}
	pager := client.NewGetStaticSiteBuildsPager(rg, name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		builds = append(builds, page.Value...)
	}
	require.Len(t, builds, 1)
	assert.Equal(t, "default", *builds[0].Name)
	assert.Equal(t, armappservice.BuildStatusReady, *builds[0].Properties.Status)

	// Site-scoped zip deployment: an Azure-AsyncOperation LRO that leaves the
	// default build Ready.
	zip, err := client.BeginCreateZipDeploymentForStaticSite(ctx, rg, name, armappservice.StaticSiteZipDeploymentARMResource{
		Properties: &armappservice.StaticSiteZipDeployment{
			AppZipURL:       to.Ptr("https://example.com/artifacts/app.zip"),
			DeploymentTitle: to.Ptr("sdk zip deploy"),
			Provider:        to.Ptr("Custom"),
		},
	}, nil)
	require.NoError(t, err)
	_, err = zip.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	defaultBuild, err := client.GetStaticSiteBuild(ctx, rg, name, "default", nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.BuildStatusReady, *defaultBuild.Properties.Status)

	// Build-scoped zip deployment creates the staging environment.
	buildZip, err := client.BeginCreateZipDeploymentForStaticSiteBuild(ctx, rg, name, "preview", armappservice.StaticSiteZipDeploymentARMResource{
		Properties: &armappservice.StaticSiteZipDeployment{
			AppZipURL: to.Ptr("https://example.com/artifacts/preview.zip"),
		},
	}, nil)
	require.NoError(t, err)
	_, err = buildZip.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	preview, err := client.GetStaticSiteBuild(ctx, rg, name, "preview", nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.BuildStatusReady, *preview.Properties.Status)
	assert.Contains(t, *preview.Properties.Hostname, "-preview")

	// Build-scoped settings, both spellings of the same bag.
	_, err = client.CreateOrUpdateStaticSiteBuildAppSettings(ctx, rg, name, "preview", armappservice.StringDictionary{
		Properties: map[string]*string{"PREVIEW_FLAG": to.Ptr("yes")},
	}, nil)
	require.NoError(t, err)
	buildSettings, err := client.ListStaticSiteBuildAppSettings(ctx, rg, name, "preview", nil)
	require.NoError(t, err)
	require.NotNil(t, buildSettings.Properties["PREVIEW_FLAG"])
	_, err = client.CreateOrUpdateStaticSiteBuildFunctionAppSettings(ctx, rg, name, "preview", armappservice.StringDictionary{
		Properties: map[string]*string{"PREVIEW_FUNC": to.Ptr("on")},
	}, nil)
	require.NoError(t, err)
	buildFnSettings, err := client.ListStaticSiteBuildFunctionAppSettings(ctx, rg, name, "preview", nil)
	require.NoError(t, err)
	require.NotNil(t, buildFnSettings.Properties["PREVIEW_FUNC"])

	// Function lists: no pipeline-built functions exist, so both lists are
	// empty collections.
	fnPager := client.NewListStaticSiteFunctionsPager(rg, name, nil)
	for fnPager.More() {
		page, err := fnPager.NextPage(ctx)
		require.NoError(t, err)
		assert.Empty(t, page.Value)
	}
	buildFnPager := client.NewListStaticSiteBuildFunctionsPager(rg, name, "preview", nil)
	for buildFnPager.More() {
		page, err := buildFnPager.NextPage(ctx)
		require.NoError(t, err)
		assert.Empty(t, page.Value)
	}

	// The staging environment deletes; the default build never does.
	delBuild, err := client.BeginDeleteStaticSiteBuild(ctx, rg, name, "preview", nil)
	require.NoError(t, err)
	_, err = delBuild.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.GetStaticSiteBuild(ctx, rg, name, "preview", nil)
	require.Error(t, err)
	_, err = client.BeginDeleteStaticSiteBuild(ctx, rg, name, "default", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "default build")
}

func TestSDK_StaticSites_CustomDomains(t *testing.T) {
	rg := "swa-sdk-rg"
	ensureRG(t, rg)
	client := staticSitesClient(t)
	name := "sdk-swa-domains"
	created := staticSitesCreate(t, client, rg, name)
	defaultHostname := *created.Properties.DefaultHostname

	zonesClient, err := armdns.NewZonesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	recordsClient, err := armdns.NewRecordSetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	zone := "sdk-swa-example.com"
	_, err = zonesClient.CreateOrUpdate(ctx, rg, zone, armdns.Zone{Location: to.Ptr("global")}, nil)
	require.NoError(t, err)

	// dns-txt-token: the create mints a validation token and the domain stays
	// Validating until the TXT record exists in the simulator's Azure DNS.
	txtDomain := "www." + zone
	domPoller, err := client.BeginCreateOrUpdateStaticSiteCustomDomain(ctx, rg, name, txtDomain,
		armappservice.StaticSiteCustomDomainRequestPropertiesARMResource{
			Properties: &armappservice.StaticSiteCustomDomainRequestPropertiesARMResourceProperties{
				ValidationMethod: to.Ptr("dns-txt-token"),
			},
		}, nil)
	require.NoError(t, err)
	domain, err := domPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, domain.Properties.ValidationToken)
	token := *domain.Properties.ValidationToken
	assert.Equal(t, armappservice.CustomDomainStatusValidating, *domain.Properties.Status)

	_, err = recordsClient.CreateOrUpdate(ctx, rg, zone, "www", armdns.RecordTypeTXT, armdns.RecordSet{
		Properties: &armdns.RecordSetProperties{
			TTL:        to.Ptr[int64](300),
			TxtRecords: []*armdns.TxtRecord{{Value: []*string{to.Ptr(token)}}},
		},
	}, nil)
	require.NoError(t, err)
	validated, err := client.GetStaticSiteCustomDomain(ctx, rg, name, txtDomain, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.CustomDomainStatusReady, *validated.Properties.Status)

	// cname-delegation: validation answers off the CNAME's presence.
	cnameDomain := "app." + zone
	_, err = client.BeginValidateCustomDomainCanBeAddedToStaticSite(ctx, rg, name, cnameDomain,
		armappservice.StaticSiteCustomDomainRequestPropertiesARMResource{
			Properties: &armappservice.StaticSiteCustomDomainRequestPropertiesARMResourceProperties{
				ValidationMethod: to.Ptr("cname-delegation"),
			},
		}, nil)
	require.Error(t, err, "validation must fail while the CNAME record is absent")

	_, err = recordsClient.CreateOrUpdate(ctx, rg, zone, "app", armdns.RecordTypeCNAME, armdns.RecordSet{
		Properties: &armdns.RecordSetProperties{
			TTL:         to.Ptr[int64](300),
			CnameRecord: &armdns.CnameRecord{Cname: to.Ptr(defaultHostname)},
		},
	}, nil)
	require.NoError(t, err)
	valPoller, err := client.BeginValidateCustomDomainCanBeAddedToStaticSite(ctx, rg, name, cnameDomain,
		armappservice.StaticSiteCustomDomainRequestPropertiesARMResource{
			Properties: &armappservice.StaticSiteCustomDomainRequestPropertiesARMResourceProperties{
				ValidationMethod: to.Ptr("cname-delegation"),
			},
		}, nil)
	require.NoError(t, err)
	_, err = valPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	cnamePoller, err := client.BeginCreateOrUpdateStaticSiteCustomDomain(ctx, rg, name, cnameDomain,
		armappservice.StaticSiteCustomDomainRequestPropertiesARMResource{
			Properties: &armappservice.StaticSiteCustomDomainRequestPropertiesARMResourceProperties{
				ValidationMethod: to.Ptr("cname-delegation"),
			},
		}, nil)
	require.NoError(t, err)
	cnameDomainRes, err := cnamePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.CustomDomainStatusReady, *cnameDomainRes.Properties.Status)

	// Collection shape + the site view's aggregated customDomains member.
	domains := []*armappservice.StaticSiteCustomDomainOverviewARMResource{}
	domPager := client.NewListStaticSiteCustomDomainsPager(rg, name, nil)
	for domPager.More() {
		page, err := domPager.NextPage(ctx)
		require.NoError(t, err)
		domains = append(domains, page.Value...)
	}
	require.Len(t, domains, 2)
	site, err := client.GetStaticSite(ctx, rg, name, nil)
	require.NoError(t, err)
	require.Len(t, site.Properties.CustomDomains, 2)

	delPoller, err := client.BeginDeleteStaticSiteCustomDomain(ctx, rg, name, cnameDomain, nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.GetStaticSiteCustomDomain(ctx, rg, name, cnameDomain, nil)
	require.Error(t, err)
}

func TestSDK_StaticSites_UsersRolesBasicAuth(t *testing.T) {
	rg := "swa-sdk-rg"
	ensureRG(t, rg)
	client := staticSitesClient(t)
	name := "sdk-swa-users"
	created := staticSitesCreate(t, client, rg, name)

	// An invitation for a domain the site does not serve is rejected.
	_, err := client.CreateUserRolesInvitationLink(ctx, rg, name, armappservice.StaticSiteUserInvitationRequestResource{
		Properties: &armappservice.StaticSiteUserInvitationRequestResourceProperties{
			Domain:      to.Ptr("wrong.example.com"),
			Provider:    to.Ptr("github"),
			UserDetails: to.Ptr("octocat"),
			Roles:       to.Ptr("admin"),
		},
	}, nil)
	require.Error(t, err)

	invitation, err := client.CreateUserRolesInvitationLink(ctx, rg, name, armappservice.StaticSiteUserInvitationRequestResource{
		Properties: &armappservice.StaticSiteUserInvitationRequestResourceProperties{
			Domain:               created.Properties.DefaultHostname,
			Provider:             to.Ptr("github"),
			UserDetails:          to.Ptr("octocat"),
			Roles:                to.Ptr("admin,reader"),
			NumHoursToExpiration: to.Ptr[int32](12),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, invitation.Properties.InvitationURL)
	assert.Contains(t, *invitation.Properties.InvitationURL, *created.Properties.DefaultHostname)
	require.NotNil(t, invitation.Properties.ExpiresOn)

	listUsers := func(provider string) []*armappservice.StaticSiteUserARMResource {
		users := []*armappservice.StaticSiteUserARMResource{}
		pager := client.NewListStaticSiteUsersPager(rg, name, provider, nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			users = append(users, page.Value...)
		}
		return users
	}
	all := listUsers("all")
	require.Len(t, all, 1)
	assert.Equal(t, "github", *all[0].Properties.Provider)
	assert.Equal(t, "octocat", *all[0].Properties.DisplayName)
	assert.Empty(t, listUsers("aad"))

	userID := *all[0].Properties.UserID
	updated, err := client.UpdateStaticSiteUser(ctx, rg, name, "github", userID, armappservice.StaticSiteUserARMResource{
		Properties: &armappservice.StaticSiteUserARMResourceProperties{Roles: to.Ptr("contributor")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "contributor", *updated.Properties.Roles)

	// Configured roles = the built-ins plus every user-assigned role.
	roles, err := client.ListStaticSiteConfiguredRoles(ctx, rg, name, nil)
	require.NoError(t, err)
	roleValues := make([]string, 0, len(roles.Properties))
	for _, r := range roles.Properties {
		roleValues = append(roleValues, *r)
	}
	assert.Contains(t, roleValues, "anonymous")
	assert.Contains(t, roleValues, "authenticated")
	assert.Contains(t, roleValues, "contributor")

	_, err = client.DeleteStaticSiteUser(ctx, rg, name, "github", userID, nil)
	require.NoError(t, err)
	assert.Empty(t, listUsers("all"))

	// Basic auth: unconfigured default, then a password write that never
	// echoes the password.
	ba, err := client.GetBasicAuth(ctx, rg, name, armappservice.BasicAuthNameDefault, nil)
	require.NoError(t, err)
	assert.Equal(t, "None", *ba.Properties.SecretState)
	setResp, err := client.CreateOrUpdateBasicAuth(ctx, rg, name, armappservice.BasicAuthNameDefault,
		armappservice.StaticSiteBasicAuthPropertiesARMResource{
			Properties: &armappservice.StaticSiteBasicAuthPropertiesARMResourceProperties{
				ApplicableEnvironmentsMode: to.Ptr("AllEnvironments"),
				Password:                   to.Ptr("hunter2hunter2"),
			},
		}, nil)
	require.NoError(t, err)
	assert.Equal(t, "Password", *setResp.Properties.SecretState)
	assert.Nil(t, setResp.Properties.Password)
	baPager := client.NewListBasicAuthPager(rg, name, nil)
	total := 0
	for baPager.More() {
		page, err := baPager.NextPage(ctx)
		require.NoError(t, err)
		for _, item := range page.Value {
			total++
			assert.Nil(t, item.Properties.Password)
			assert.Equal(t, "AllEnvironments", *item.Properties.ApplicableEnvironmentsMode)
		}
	}
	assert.Equal(t, 1, total)
}

func TestSDK_StaticSites_DatabaseConnections(t *testing.T) {
	rg := "swa-sdk-rg"
	ensureRG(t, rg)
	client := staticSitesClient(t)
	name := "sdk-swa-dbconn"
	staticSitesCreate(t, client, rg, name)

	dbResourceID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.DocumentDB/databaseAccounts/sdk-swa-cosmos"

	// resourceId and region are required.
	_, err := client.CreateOrUpdateDatabaseConnection(ctx, rg, name, "default", armappservice.DatabaseConnection{
		Properties: &armappservice.DatabaseConnectionProperties{ResourceID: to.Ptr(dbResourceID)},
	}, nil)
	require.Error(t, err)

	createdConn, err := client.CreateOrUpdateDatabaseConnection(ctx, rg, name, "default", armappservice.DatabaseConnection{
		Properties: &armappservice.DatabaseConnectionProperties{
			ResourceID:         to.Ptr(dbResourceID),
			Region:             to.Ptr("eastus"),
			ConnectionIdentity: to.Ptr("SystemAssigned"),
			ConnectionString:   to.Ptr("AccountEndpoint=https://sdk-swa-cosmos.documents.azure.com;"),
		},
	}, nil)
	require.NoError(t, err)
	assert.Nil(t, createdConn.Properties.ConnectionString, "plain responses never carry the connection string")

	plain, err := client.GetDatabaseConnection(ctx, rg, name, "default", nil)
	require.NoError(t, err)
	assert.Nil(t, plain.Properties.ConnectionString)
	assert.Equal(t, dbResourceID, *plain.Properties.ResourceID)

	details, err := client.GetDatabaseConnectionWithDetails(ctx, rg, name, "default", nil)
	require.NoError(t, err)
	require.NotNil(t, details.Properties.ConnectionString)
	assert.Contains(t, *details.Properties.ConnectionString, "AccountEndpoint")

	patched, err := client.UpdateDatabaseConnection(ctx, rg, name, "default", armappservice.DatabaseConnectionPatchRequest{
		Properties: &armappservice.DatabaseConnectionPatchRequestProperties{Region: to.Ptr("westus2")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "westus2", *patched.Properties.Region)

	listed := 0
	pager := client.NewGetDatabaseConnectionsPager(rg, name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, c := range page.Value {
			listed++
			assert.Nil(t, c.Properties.ConnectionString)
		}
	}
	assert.Equal(t, 1, listed)
	withDetails := 0
	detailsPager := client.NewGetDatabaseConnectionsWithDetailsPager(rg, name, nil)
	for detailsPager.More() {
		page, err := detailsPager.NextPage(ctx)
		require.NoError(t, err)
		for _, c := range page.Value {
			withDetails++
			require.NotNil(t, c.Properties.ConnectionString)
		}
	}
	assert.Equal(t, 1, withDetails)

	// Build-scoped connections live under the environment.
	buildConn, err := client.CreateOrUpdateBuildDatabaseConnection(ctx, rg, name, "default", "default", armappservice.DatabaseConnection{
		Properties: &armappservice.DatabaseConnectionProperties{
			ResourceID: to.Ptr(dbResourceID),
			Region:     to.Ptr("eastus"),
		},
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, *buildConn.ID, "/builds/default/databaseConnections/")
	buildGet, err := client.GetBuildDatabaseConnection(ctx, rg, name, "default", "default", nil)
	require.NoError(t, err)
	assert.Nil(t, buildGet.Properties.ConnectionString)
	_, err = client.GetBuildDatabaseConnectionWithDetails(ctx, rg, name, "default", "default", nil)
	require.NoError(t, err)
	buildPatched, err := client.UpdateBuildDatabaseConnection(ctx, rg, name, "default", "default", armappservice.DatabaseConnectionPatchRequest{
		Properties: &armappservice.DatabaseConnectionPatchRequestProperties{ConnectionIdentity: to.Ptr("SystemAssigned")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "SystemAssigned", *buildPatched.Properties.ConnectionIdentity)
	buildListed := 0
	buildPager := client.NewGetBuildDatabaseConnectionsPager(rg, name, "default", nil)
	for buildPager.More() {
		page, err := buildPager.NextPage(ctx)
		require.NoError(t, err)
		buildListed += len(page.Value)
	}
	assert.Equal(t, 1, buildListed)
	buildDetails := 0
	buildDetailsPager := client.NewGetBuildDatabaseConnectionsWithDetailsPager(rg, name, "default", nil)
	for buildDetailsPager.More() {
		page, err := buildDetailsPager.NextPage(ctx)
		require.NoError(t, err)
		buildDetails += len(page.Value)
	}
	assert.Equal(t, 1, buildDetails)
	_, err = client.DeleteBuildDatabaseConnection(ctx, rg, name, "default", "default", nil)
	require.NoError(t, err)

	_, err = client.DeleteDatabaseConnection(ctx, rg, name, "default", nil)
	require.NoError(t, err)
	_, err = client.GetDatabaseConnection(ctx, rg, name, "default", nil)
	require.Error(t, err)
}

func TestSDK_StaticSites_BackendsAndFunctionApps(t *testing.T) {
	rg := "swa-sdk-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "swa-backend-plan")
	webClient, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	webMoreCreateSite(t, webClient, rg, "swa-backend-fn", planID)
	fnAppID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Web/sites/swa-backend-fn"

	client := staticSitesClient(t)
	name := "sdk-swa-backends"
	staticSitesCreate(t, client, rg, name)

	// Linked backend: the backend resource must exist in the simulator.
	_, err = client.BeginLinkBackend(ctx, rg, name, "ghost", armappservice.StaticSiteLinkedBackendARMResource{
		Properties: &armappservice.StaticSiteLinkedBackendARMResourceProperties{
			BackendResourceID: to.Ptr(fnAppID + "-missing"),
			Region:            to.Ptr("eastus"),
		},
	}, nil)
	require.Error(t, err)

	linkPoller, err := client.BeginLinkBackend(ctx, rg, name, "backend1", armappservice.StaticSiteLinkedBackendARMResource{
		Properties: &armappservice.StaticSiteLinkedBackendARMResourceProperties{
			BackendResourceID: to.Ptr(fnAppID),
			Region:            to.Ptr("eastus"),
		},
	}, nil)
	require.NoError(t, err)
	linked, err := linkPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "Succeeded", *linked.Properties.ProvisioningState)
	require.NotNil(t, linked.Properties.CreatedOn)

	got, err := client.GetLinkedBackend(ctx, rg, name, "backend1", nil)
	require.NoError(t, err)
	assert.Equal(t, fnAppID, *got.Properties.BackendResourceID)
	backends := 0
	lbPager := client.NewGetLinkedBackendsPager(rg, name, nil)
	for lbPager.More() {
		page, err := lbPager.NextPage(ctx)
		require.NoError(t, err)
		backends += len(page.Value)
	}
	assert.Equal(t, 1, backends)

	// The site view aggregates the link.
	site, err := client.GetStaticSite(ctx, rg, name, nil)
	require.NoError(t, err)
	require.Len(t, site.Properties.LinkedBackends, 1)
	assert.Equal(t, fnAppID, *site.Properties.LinkedBackends[0].BackendResourceID)

	valPoller, err := client.BeginValidateBackend(ctx, rg, name, "backend1", armappservice.StaticSiteLinkedBackendARMResource{
		Properties: &armappservice.StaticSiteLinkedBackendARMResourceProperties{
			BackendResourceID: to.Ptr(fnAppID),
			Region:            to.Ptr("eastus"),
		},
	}, nil)
	require.NoError(t, err)
	_, err = valPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// Build-scoped linked backend.
	buildLinkPoller, err := client.BeginLinkBackendToBuild(ctx, rg, name, "default", "backend1", armappservice.StaticSiteLinkedBackendARMResource{
		Properties: &armappservice.StaticSiteLinkedBackendARMResourceProperties{
			BackendResourceID: to.Ptr(fnAppID),
			Region:            to.Ptr("eastus"),
		},
	}, nil)
	require.NoError(t, err)
	_, err = buildLinkPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	buildBackend, err := client.GetLinkedBackendForBuild(ctx, rg, name, "default", "backend1", nil)
	require.NoError(t, err)
	assert.Contains(t, *buildBackend.ID, "/builds/default/linkedBackends/")
	buildBackends := 0
	buildLbPager := client.NewGetLinkedBackendsForBuildPager(rg, name, "default", nil)
	for buildLbPager.More() {
		page, err := buildLbPager.NextPage(ctx)
		require.NoError(t, err)
		buildBackends += len(page.Value)
	}
	assert.Equal(t, 1, buildBackends)
	buildValPoller, err := client.BeginValidateBackendForBuild(ctx, rg, name, "default", "backend1", armappservice.StaticSiteLinkedBackendARMResource{
		Properties: &armappservice.StaticSiteLinkedBackendARMResourceProperties{
			BackendResourceID: to.Ptr(fnAppID),
			Region:            to.Ptr("eastus"),
		},
	}, nil)
	require.NoError(t, err)
	_, err = buildValPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.UnlinkBackendFromBuild(ctx, rg, name, "default", "backend1", nil)
	require.NoError(t, err)
	_, err = client.UnlinkBackend(ctx, rg, name, "backend1", nil)
	require.NoError(t, err)
	_, err = client.GetLinkedBackend(ctx, rg, name, "backend1", nil)
	require.Error(t, err)

	// User-provided function apps LINK an existing Microsoft.Web site;
	// registering one that does not exist is rejected.
	_, err = client.BeginRegisterUserProvidedFunctionAppWithStaticSite(ctx, rg, name, "ghostfn",
		armappservice.StaticSiteUserProvidedFunctionAppARMResource{
			Properties: &armappservice.StaticSiteUserProvidedFunctionAppARMResourceProperties{
				FunctionAppResourceID: to.Ptr(fnAppID + "-missing"),
			},
		}, nil)
	require.Error(t, err)

	regPoller, err := client.BeginRegisterUserProvidedFunctionAppWithStaticSite(ctx, rg, name, "fnapp1",
		armappservice.StaticSiteUserProvidedFunctionAppARMResource{
			Properties: &armappservice.StaticSiteUserProvidedFunctionAppARMResourceProperties{
				FunctionAppResourceID: to.Ptr(fnAppID),
			},
		}, nil)
	require.NoError(t, err)
	registered, err := regPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "eastus", *registered.Properties.FunctionAppRegion, "region resolves from the linked site")
	require.NotNil(t, registered.Properties.CreatedOn)

	fnApp, err := client.GetUserProvidedFunctionAppForStaticSite(ctx, rg, name, "fnapp1", nil)
	require.NoError(t, err)
	assert.Equal(t, fnAppID, *fnApp.Properties.FunctionAppResourceID)
	fnApps := 0
	fnPager := client.NewGetUserProvidedFunctionAppsForStaticSitePager(rg, name, nil)
	for fnPager.More() {
		page, err := fnPager.NextPage(ctx)
		require.NoError(t, err)
		fnApps += len(page.Value)
	}
	assert.Equal(t, 1, fnApps)

	// Build-scoped registration.
	buildRegPoller, err := client.BeginRegisterUserProvidedFunctionAppWithStaticSiteBuild(ctx, rg, name, "default", "fnapp1",
		armappservice.StaticSiteUserProvidedFunctionAppARMResource{
			Properties: &armappservice.StaticSiteUserProvidedFunctionAppARMResourceProperties{
				FunctionAppResourceID: to.Ptr(fnAppID),
				FunctionAppRegion:     to.Ptr("eastus"),
			},
		}, nil)
	require.NoError(t, err)
	_, err = buildRegPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	buildFnApp, err := client.GetUserProvidedFunctionAppForStaticSiteBuild(ctx, rg, name, "default", "fnapp1", nil)
	require.NoError(t, err)
	assert.Contains(t, *buildFnApp.ID, "/builds/default/userProvidedFunctionApps/")
	buildFnApps := 0
	buildFnAppsPager := client.NewGetUserProvidedFunctionAppsForStaticSiteBuildPager(rg, name, "default", nil)
	for buildFnAppsPager.More() {
		page, err := buildFnAppsPager.NextPage(ctx)
		require.NoError(t, err)
		buildFnApps += len(page.Value)
	}
	assert.Equal(t, 1, buildFnApps)
	_, err = client.DetachUserProvidedFunctionAppFromStaticSiteBuild(ctx, rg, name, "default", "fnapp1", nil)
	require.NoError(t, err)
	_, err = client.DetachUserProvidedFunctionAppFromStaticSite(ctx, rg, name, "fnapp1", nil)
	require.NoError(t, err)
	_, err = client.GetUserProvidedFunctionAppForStaticSite(ctx, rg, name, "fnapp1", nil)
	require.Error(t, err)
}

func TestSDK_StaticSites_PrivateEndpointsAndWorkflow(t *testing.T) {
	rg := "swa-sdk-rg"
	ensureRG(t, rg)
	client := staticSitesClient(t)
	name := "sdk-swa-pec"
	staticSitesCreate(t, client, rg, name)

	approvePoller, err := client.BeginApproveOrRejectPrivateEndpointConnection(ctx, rg, name, "pec1",
		armappservice.RemotePrivateEndpointConnectionARMResource{
			Properties: &armappservice.RemotePrivateEndpointConnectionARMResourceProperties{
				PrivateEndpoint: &armappservice.ArmIDWrapper{},
				PrivateLinkServiceConnectionState: &armappservice.PrivateLinkConnectionState{
					Status:      to.Ptr("Approved"),
					Description: to.Ptr("approved by sdk test"),
				},
			},
		}, nil)
	require.NoError(t, err)
	approved, err := approvePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "Succeeded", *approved.Properties.ProvisioningState)
	assert.Equal(t, "Approved", *approved.Properties.PrivateLinkServiceConnectionState.Status)

	pec, err := client.GetPrivateEndpointConnection(ctx, rg, name, "pec1", nil)
	require.NoError(t, err)
	assert.Equal(t, "pec1", *pec.Name)
	pecs := 0
	pecPager := client.NewGetPrivateEndpointConnectionListPager(rg, name, nil)
	for pecPager.More() {
		page, err := pecPager.NextPage(ctx)
		require.NoError(t, err)
		pecs += len(page.Value)
	}
	assert.Equal(t, 1, pecs)

	plr, err := client.GetPrivateLinkResources(ctx, rg, name, nil)
	require.NoError(t, err)
	require.Len(t, plr.Value, 1)
	assert.Equal(t, "staticSites", *plr.Value[0].Properties.GroupID)

	delPoller, err := client.BeginDeletePrivateEndpointConnection(ctx, rg, name, "pec1", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.GetPrivateEndpointConnection(ctx, rg, name, "pec1", nil)
	require.Error(t, err)

	// Workflow preview generates the GitHub Actions workflow file.
	preview, err := client.PreviewWorkflow(ctx, "eastus", armappservice.StaticSitesWorkflowPreviewRequest{
		Properties: &armappservice.StaticSitesWorkflowPreviewRequestProperties{
			RepositoryURL: to.Ptr("https://github.com/example/swa"),
			Branch:        to.Ptr("main"),
			BuildProperties: &armappservice.StaticSiteBuildProperties{
				AppLocation:    to.Ptr("app"),
				APILocation:    to.Ptr("api"),
				OutputLocation: to.Ptr("dist"),
			},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, preview.Properties.Contents)
	assert.True(t, strings.Contains(*preview.Properties.Contents, "Azure/static-web-apps-deploy@v1"))
	assert.True(t, strings.Contains(*preview.Properties.Contents, "main"))
	assert.Contains(t, *preview.Properties.Path, ".github/workflows/")
}
