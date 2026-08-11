package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// patch_partial_semantics_test.go covers the ARM PATCH (update) handlers the
// Azure console's edit flows drive: the container-registry registry PATCH, the
// Microsoft.App job PATCH, and the Microsoft.Web site PATCH.
//
// ARM PATCH is a merge: a property the client omits keeps its stored value.
// Each test therefore does a *partial* update — typically tags only — and then
// asserts that the properties it did not send survived. A handler that decodes
// the body into a plain struct and assigns every field back cannot pass these,
// because a decoded zero value is indistinguishable from an absent key.

// TestSDK_ACR_RegistryUpdatePartial drives RegistriesClient.BeginUpdate, the
// PATCH the console's registry-settings flow uses, and checks both that a sent
// property is applied and that unsent properties are preserved.
func TestSDK_ACR_RegistryUpdatePartial(t *testing.T) {
	const (
		rg           = "acr-patch-rg"
		registryName = "acrpatchregistry"
	)
	ensureRG(t, rg)

	client, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	createPoller, err := client.BeginCreate(ctx, rg, registryName, armcontainerregistry.Registry{
		Location: to.Ptr("eastus"),
		Tags:     map[string]*string{"env": to.Ptr("dev")},
		SKU:      &armcontainerregistry.SKU{Name: to.Ptr(armcontainerregistry.SKUNameBasic)},
		Properties: &armcontainerregistry.RegistryProperties{
			AdminUserEnabled: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	created, err := createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.AdminUserEnabled)
	assert.True(t, *created.Properties.AdminUserEnabled)

	defer func() {
		if dp, derr := client.BeginDelete(ctx, rg, registryName, nil); derr == nil {
			_, _ = dp.PollUntilDone(ctx, nil)
		}
	}()

	// PATCH tags only. adminUserEnabled and the SKU must survive untouched.
	tagsPoller, err := client.BeginUpdate(ctx, rg, registryName, armcontainerregistry.RegistryUpdateParameters{
		Tags: map[string]*string{"env": to.Ptr("prod"), "owner": to.Ptr("sockerless")},
	}, nil)
	require.NoError(t, err)
	patched, err := tagsPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	require.NotNil(t, patched.Tags["env"])
	assert.Equal(t, "prod", *patched.Tags["env"])
	require.NotNil(t, patched.Tags["owner"])
	assert.Equal(t, "sockerless", *patched.Tags["owner"])
	require.NotNil(t, patched.Properties, "PATCH response must carry properties")
	require.NotNil(t, patched.Properties.AdminUserEnabled,
		"adminUserEnabled must survive a tags-only PATCH")
	assert.True(t, *patched.Properties.AdminUserEnabled,
		"tags-only PATCH must not clear adminUserEnabled")
	require.NotNil(t, patched.SKU)
	require.NotNil(t, patched.SKU.Name)
	assert.Equal(t, armcontainerregistry.SKUNameBasic, *patched.SKU.Name,
		"tags-only PATCH must not clear the SKU")

	// PATCH a property only. The tags set above must survive.
	propPoller, err := client.BeginUpdate(ctx, rg, registryName, armcontainerregistry.RegistryUpdateParameters{
		Properties: &armcontainerregistry.RegistryPropertiesUpdateParameters{
			AdminUserEnabled: to.Ptr(false),
		},
	}, nil)
	require.NoError(t, err)
	propPatched, err := propPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, propPatched.Properties.AdminUserEnabled)
	assert.False(t, *propPatched.Properties.AdminUserEnabled,
		"adminUserEnabled=false must be applied, not treated as absent")
	require.NotNil(t, propPatched.Tags["owner"],
		"tags must survive a properties-only PATCH")
	assert.Equal(t, "sockerless", *propPatched.Tags["owner"])

	// Read back through GET so the stored row — not just the PATCH response —
	// is verified.
	got, err := client.Get(ctx, rg, registryName, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties.AdminUserEnabled)
	assert.False(t, *got.Properties.AdminUserEnabled)
	require.NotNil(t, got.Tags["owner"])
	assert.Equal(t, "sockerless", *got.Tags["owner"])
}

// TestSDK_ACAJob_UpdatePartial drives JobsClient.BeginUpdate — the PATCH the
// console's job-edit flow uses — with a tags-only body, and asserts the job's
// configuration and template survive.
func TestSDK_ACAJob_UpdatePartial(t *testing.T) {
	rg := "aca-jobpatch-rg"
	ensureRG(t, rg)
	ensureManagedEnv(t, rg, "jobpatch-env")

	client, err := armappcontainers.NewJobsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	const jobName = "sdk-jobpatch-job"
	cp, err := client.BeginCreateOrUpdate(ctx, rg, jobName, armappcontainers.Job{
		Location: to.Ptr("eastus"),
		Properties: &armappcontainers.JobProperties{
			EnvironmentID: to.Ptr(acaEnvID(rg, "jobpatch-env")),
			Configuration: &armappcontainers.JobConfiguration{
				TriggerType:       to.Ptr(armappcontainers.TriggerTypeManual),
				ReplicaTimeout:    to.Ptr[int32](1800),
				ReplicaRetryLimit: to.Ptr[int32](2),
				ManualTriggerConfig: &armappcontainers.JobConfigurationManualTriggerConfig{
					Parallelism:            to.Ptr[int32](1),
					ReplicaCompletionCount: to.Ptr[int32](1),
				},
			},
			Template: &armappcontainers.JobTemplate{
				Containers: []*armappcontainers.Container{
					{Name: to.Ptr("main"), Image: to.Ptr("alpine:3.20")},
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = cp.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	defer func() {
		if dp, derr := client.BeginDelete(ctx, rg, jobName, nil); derr == nil {
			_, _ = dp.PollUntilDone(ctx, nil)
		}
	}()

	// Tags-only PATCH.
	up, err := client.BeginUpdate(ctx, rg, jobName, armappcontainers.JobPatchProperties{
		Tags: map[string]*string{"tier": to.Ptr("batch")},
	}, nil)
	require.NoError(t, err)
	_, err = up.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	got, err := client.Get(ctx, rg, jobName, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Tags["tier"])
	assert.Equal(t, "batch", *got.Tags["tier"])

	require.NotNil(t, got.Properties, "job properties must survive a tags-only PATCH")
	require.NotNil(t, got.Properties.Configuration,
		"job configuration must survive a tags-only PATCH")
	require.NotNil(t, got.Properties.Configuration.ReplicaTimeout)
	assert.Equal(t, int32(1800), *got.Properties.Configuration.ReplicaTimeout,
		"replicaTimeout must not be reset by a tags-only PATCH")
	require.NotNil(t, got.Properties.Template,
		"job template must survive a tags-only PATCH")
	require.Len(t, got.Properties.Template.Containers, 1)
	require.NotNil(t, got.Properties.Template.Containers[0].Image)
	assert.Equal(t, "alpine:3.20", *got.Properties.Template.Containers[0].Image)

	// A configuration-only PATCH must apply the new value and keep the tags.
	cfgUp, err := client.BeginUpdate(ctx, rg, jobName, armappcontainers.JobPatchProperties{
		Properties: &armappcontainers.JobPatchPropertiesProperties{
			Configuration: &armappcontainers.JobConfiguration{
				TriggerType:       to.Ptr(armappcontainers.TriggerTypeManual),
				ReplicaTimeout:    to.Ptr[int32](600),
				ReplicaRetryLimit: to.Ptr[int32](0),
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = cfgUp.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	got2, err := client.Get(ctx, rg, jobName, nil)
	require.NoError(t, err)
	require.NotNil(t, got2.Properties.Configuration.ReplicaTimeout)
	assert.Equal(t, int32(600), *got2.Properties.Configuration.ReplicaTimeout)
	require.NotNil(t, got2.Tags["tier"], "tags must survive a properties-only PATCH")
	assert.Equal(t, "batch", *got2.Tags["tier"])
}

// TestSDK_WebApp_UpdatePartialPreservesHTTPSOnly drives WebAppsClient.Update —
// the Microsoft.Web site PATCH — with bodies that omit httpsOnly, and asserts
// it survives. Real Azure treats an absent property as unchanged; a handler
// that assigns req.Properties.HTTPSOnly unconditionally silently turns the
// site's HTTPS-only enforcement off on any unrelated edit.
//
// SitePatchResource is a ProxyOnlyResource — it carries kind + properties and
// has no tags field — so the partial bodies here are a kind-only PATCH (no
// "properties" key at all) and a properties-subset PATCH, which are exactly the
// shapes the SDK and the azurerm provider emit.
func TestSDK_WebApp_UpdatePartialPreservesHTTPSOnly(t *testing.T) {
	rg := "web-patchpartial-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "wm-patchpartial-plan")

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	const name = "wm-patchpartial-site"
	webMoreCreateSite(t, client, rg, name, planID)

	// Turn httpsOnly on through a real PATCH.
	on, err := client.Update(ctx, rg, name, armappservice.SitePatchResource{
		Properties: &armappservice.SitePatchResourceProperties{
			HTTPSOnly: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, on.Properties)
	require.NotNil(t, on.Properties.HTTPSOnly)
	assert.True(t, *on.Properties.HTTPSOnly, "httpsOnly=true must be applied")

	// Kind-only PATCH — the body carries no "properties" key at all.
	kindOnly, err := client.Update(ctx, rg, name, armappservice.SitePatchResource{
		Kind: to.Ptr("app,linux"),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, kindOnly.Kind)
	assert.Equal(t, "app,linux", *kindOnly.Kind)
	require.NotNil(t, kindOnly.Properties)
	require.NotNil(t, kindOnly.Properties.HTTPSOnly,
		"httpsOnly must still be reported after a kind-only PATCH")
	assert.True(t, *kindOnly.Properties.HTTPSOnly,
		"a PATCH with no properties key must not clear httpsOnly")

	// Properties-subset PATCH — "properties" is present but has no httpsOnly.
	subset, err := client.Update(ctx, rg, name, armappservice.SitePatchResource{
		Properties: &armappservice.SitePatchResourceProperties{
			ClientCertMode: to.Ptr(armappservice.ClientCertModeOptional),
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, subset.Properties)
	require.NotNil(t, subset.Properties.HTTPSOnly,
		"httpsOnly must still be reported after a properties-subset PATCH")
	assert.True(t, *subset.Properties.HTTPSOnly,
		"a properties body without httpsOnly must not clear it")

	// The stored row — not just the PATCH response — must agree.
	got, err := client.Get(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties.HTTPSOnly)
	assert.True(t, *got.Properties.HTTPSOnly,
		"GET after partial PATCHes must still report httpsOnly=true")
	require.NotNil(t, got.Properties.ServerFarmID,
		"serverFarmId must survive a partial PATCH")
	assert.Equal(t, planID, *got.Properties.ServerFarmID)

	// httpsOnly=false must still be applied when the client actually sends it —
	// presence-awareness must not degrade into ignoring false.
	off, err := client.Update(ctx, rg, name, armappservice.SitePatchResource{
		Properties: &armappservice.SitePatchResourceProperties{
			HTTPSOnly: to.Ptr(false),
		},
	}, nil)
	require.NoError(t, err)
	if off.Properties != nil && off.Properties.HTTPSOnly != nil {
		assert.False(t, *off.Properties.HTTPSOnly,
			"an explicit httpsOnly=false must be applied")
	}
	final, err := client.Get(ctx, rg, name, nil)
	require.NoError(t, err)
	if final.Properties != nil && final.Properties.HTTPSOnly != nil {
		assert.False(t, *final.Properties.HTTPSOnly,
			"GET must reflect an explicit httpsOnly=false")
	}
	require.NotNil(t, final.Properties.ServerFarmID,
		"serverFarmId must survive a properties-only PATCH")
}
