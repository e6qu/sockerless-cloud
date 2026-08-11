package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/applicationinsights/armapplicationinsights"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppInsights_ComponentCRUD(t *testing.T) {
	const (
		rgName        = "insights-sdk-rg"
		componentName = "sdk-test-appinsights"
	)

	ensureRG(t, rgName)

	client, err := armapplicationinsights.NewComponentsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// Create
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

	// Get
	getResp, err := client.Get(ctx, rgName, componentName, nil)
	require.NoError(t, err)
	assert.Equal(t, componentName, *getResp.Name)
	assert.Equal(t, *createResp.Properties.InstrumentationKey, *getResp.Properties.InstrumentationKey)

	// Delete
	_, err = client.Delete(ctx, rgName, componentName, nil)
	require.NoError(t, err)

	// Get after delete — must 404
	_, err = client.Get(ctx, rgName, componentName, nil)
	assert.Error(t, err, "Get after delete should fail with 404")
}

func TestAppInsights_InstrumentationKeyStableOnUpsert(t *testing.T) {
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

	// Re-create (upsert) — instrumentation key must be stable across updates
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

// TestAppInsights_BillingFeatures covers the currentbillingfeatures GET/PUT
// surface via armapplicationinsights.ComponentCurrentBillingFeaturesClient.
// The response must use Azure's camelCase wire shape — with PascalCase keys
// the SDK silently deserialized CurrentBillingFeatures/DataVolumeCap to nil
// , which this test would have caught.
func TestAppInsights_BillingFeatures(t *testing.T) {
	const (
		rgName        = "insights-billing-rg"
		componentName = "billing-component"
	)
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
	// PUT must echo the submitted cap, not a static value.
	require.NotNil(t, updateResp.DataVolumeCap)
	require.NotNil(t, updateResp.DataVolumeCap.Cap)
	assert.Equal(t, float32(200), *updateResp.DataVolumeCap.Cap)

	// And a subsequent GET must return the persisted cap.
	afterGet, err := billing.Get(ctx, rgName, componentName, nil)
	require.NoError(t, err)
	require.NotNil(t, afterGet.DataVolumeCap.Cap)
	assert.Equal(t, float32(200), *afterGet.DataVolumeCap.Cap, "GET must return the persisted daily cap")
}

func ptrApplicationType(s armapplicationinsights.ApplicationType) *armapplicationinsights.ApplicationType {
	return &s
}
