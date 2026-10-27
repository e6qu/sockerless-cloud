package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/applicationinsights/armapplicationinsights"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppInsights_ComponentCRUD(t *testing.T) {
	t.Parallel()
	const (
		rgName        = "insights-sdk-rg"
		componentName = "sdk-test-appinsights"
	)

	ensureRG(t, rgName)

	client, err := armapplicationinsights.NewComponentsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	createResp, err := client.CreateOrUpdate(ctx, rgName, componentName, armapplicationinsights.Component{
		Location: ptrStr("eastus"),
		Kind:     ptrStr("web"),
		Properties: &armapplicationinsights.ComponentProperties{
			ApplicationType: ptrApplicationType(armapplicationinsights.ApplicationTypeWeb),
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, componentName, *createResp.Name)
	require.NotNil(t, createResp.Properties)
	assert.NotEmpty(t, *createResp.Properties.InstrumentationKey)
	assert.NotEmpty(t, *createResp.Properties.ConnectionString)
	assert.Contains(t, *createResp.Properties.ConnectionString, "InstrumentationKey=")

	getResp, err := client.Get(ctx, rgName, componentName, nil)
	require.NoError(t, err)
	assert.Equal(t, componentName, *getResp.Name)
	assert.Equal(t, *createResp.Properties.InstrumentationKey, *getResp.Properties.InstrumentationKey)

	_, err = client.Delete(ctx, rgName, componentName, nil)
	require.NoError(t, err)

	_, err = client.Get(ctx, rgName, componentName, nil)
	assert.Error(t, err, "Get after delete should fail with 404")
}

func TestAppInsights_InstrumentationKeyStableOnUpsert(t *testing.T) {
	t.Parallel()
	const (
		rgName = "insights-key-rg"
		name   = "stable-key-component"
	)
	ensureRG(t, rgName)

	client, err := armapplicationinsights.NewComponentsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	first, err := client.CreateOrUpdate(ctx, rgName, name, armapplicationinsights.Component{
		Location: ptrStr("westus"),
		Kind:     ptrStr("web"),
		Properties: &armapplicationinsights.ComponentProperties{
			ApplicationType: ptrApplicationType(armapplicationinsights.ApplicationTypeWeb),
		},
	}, nil)
	require.NoError(t, err)

	// The instrumentation key stays stable across an upsert.
	second, err := client.CreateOrUpdate(ctx, rgName, name, armapplicationinsights.Component{
		Location: ptrStr("westus"),
		Kind:     ptrStr("web"),
		Properties: &armapplicationinsights.ComponentProperties{
			ApplicationType: ptrApplicationType(armapplicationinsights.ApplicationTypeWeb),
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, *first.Properties.InstrumentationKey, *second.Properties.InstrumentationKey)
}

// TestAppInsights_BillingFeatures covers currentbillingfeatures GET and PUT.
// Azure's wire shape is camelCase; the SDK reads PascalCase keys as nil.
func TestAppInsights_BillingFeatures(t *testing.T) {
	t.Parallel()
	rgName, componentName := "insights-billing-rg", uniquename.New("billing-component")
	ensureRG(t, rgName)

	comps, err := armapplicationinsights.NewComponentsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = comps.CreateOrUpdate(ctx, rgName, componentName, armapplicationinsights.Component{
		Location: ptrStr("eastus"),
		Kind:     ptrStr("web"),
		Properties: &armapplicationinsights.ComponentProperties{
			ApplicationType: ptrApplicationType(armapplicationinsights.ApplicationTypeWeb),
		},
	}, nil)
	require.NoError(t, err)

	billing, err := armapplicationinsights.NewComponentCurrentBillingFeaturesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	getResp, err := billing.Get(ctx, rgName, componentName, nil)
	require.NoError(t, err)
	require.NotEmpty(t, getResp.CurrentBillingFeatures, "currentBillingFeatures must deserialize (camelCase wire shape)")
	assert.Equal(t, "Basic", *getResp.CurrentBillingFeatures[0])
	require.NotNil(t, getResp.DataVolumeCap, "dataVolumeCap must deserialize")
	require.NotNil(t, getResp.DataVolumeCap.Cap)
	assert.Equal(t, float32(100), *getResp.DataVolumeCap.Cap)

	cap200 := float32(200)
	updateResp, err := billing.Update(ctx, rgName, componentName,
		armapplicationinsights.ComponentBillingFeatures{
			CurrentBillingFeatures: []*string{ptrStr("Basic")},
			DataVolumeCap:          &armapplicationinsights.ComponentDataVolumeCap{Cap: &cap200},
		}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, updateResp.CurrentBillingFeatures)
	// PUT echoes the submitted cap.
	require.NotNil(t, updateResp.DataVolumeCap)
	require.NotNil(t, updateResp.DataVolumeCap.Cap)
	assert.Equal(t, float32(200), *updateResp.DataVolumeCap.Cap)

	// A later GET returns the persisted cap.
	afterGet, err := billing.Get(ctx, rgName, componentName, nil)
	require.NoError(t, err)
	require.NotNil(t, afterGet.DataVolumeCap.Cap)
	assert.Equal(t, float32(200), *afterGet.DataVolumeCap.Cap, "GET must return the persisted daily cap")
}

func ptrApplicationType(s armapplicationinsights.ApplicationType) *armapplicationinsights.ApplicationType {
	return &s
}

// Every component with no record of its own shares the default plan, so no
// request may write to it.
func TestAppInsights_OneComponentsPlanIsNotAnothersDefault(t *testing.T) {
	t.Parallel()
	const rgName = "insights-default-rg"
	ensureRG(t, rgName)

	comps, err := armapplicationinsights.NewComponentsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	newComponent := func(name string) {
		t.Helper()
		_, err := comps.CreateOrUpdate(ctx, rgName, name, armapplicationinsights.Component{
			Location: ptrStr("eastus"),
			Kind:     ptrStr("web"),
			Properties: &armapplicationinsights.ComponentProperties{
				ApplicationType: ptrApplicationType(armapplicationinsights.ApplicationTypeWeb),
			},
		}, nil)
		require.NoError(t, err)
	}
	newComponent("default-moved")
	newComponent("default-untouched")

	billing, err := armapplicationinsights.NewComponentCurrentBillingFeaturesClient(
		subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	enterpriseCap := float32(50)
	_, err = billing.Update(ctx, rgName, "default-moved",
		armapplicationinsights.ComponentBillingFeatures{
			CurrentBillingFeatures: []*string{ptrStr("Application Insights Enterprise")},
			DataVolumeCap: &armapplicationinsights.ComponentDataVolumeCap{
				Cap: &enterpriseCap,
			},
		}, nil)
	require.NoError(t, err)

	moved, err := billing.Get(ctx, rgName, "default-moved", nil)
	require.NoError(t, err)
	require.NotEmpty(t, moved.CurrentBillingFeatures)
	assert.Equal(t, "Application Insights Enterprise", *moved.CurrentBillingFeatures[0],
		"the component that was moved is on the plan it was moved to")

	untouched, err := billing.Get(ctx, rgName, "default-untouched", nil)
	require.NoError(t, err)
	require.NotEmpty(t, untouched.CurrentBillingFeatures)
	assert.Equal(t, "Basic", *untouched.CurrentBillingFeatures[0],
		"a component nobody moved is still on the plan every component starts on")
}
