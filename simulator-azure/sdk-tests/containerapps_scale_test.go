package azure_sdk_test

import (
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A revision whose HTTP scale rule allows zero replicas runs none until a
// request arrives: the request starts a replica and is answered by it, and
// once no request has come for the cooldown period the revision scales back
// to zero. The rule reads back as written.
func TestSDK_ContainerAppsScale_HTTPRuleScalesFromAndToZero(t *testing.T) {
	t.Parallel()
	rg, name := uniqueName("sdk-aca-scale-rg"), uniqueName("acascale")
	ensureRG(t, rg)
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	app := servingContainerApp(rg, "scaled", nil)
	app.Properties.Template.Scale = &armappcontainers.Scale{
		MinReplicas:    to.Ptr[int32](0),
		MaxReplicas:    to.Ptr[int32](2),
		CooldownPeriod: to.Ptr[int32](1),
		Rules: []*armappcontainers.ScaleRule{{
			Name: to.Ptr("http-rule"),
			HTTP: &armappcontainers.HTTPScaleRule{Metadata: map[string]*string{"concurrentRequests": to.Ptr("5")}},
		}},
	}
	created := createContainerApp(t, client, rg, name, app)
	revision := ptrVal(created.Properties.LatestRevisionName)

	got, err := client.Get(ctx, rg, name, nil)
	require.NoError(t, err)
	require.Len(t, got.Properties.Template.Scale.Rules, 1)
	rule := got.Properties.Template.Scale.Rules[0]
	assert.Equal(t, "http-rule", ptrVal(rule.Name))
	require.NotNil(t, rule.HTTP)
	assert.Equal(t, "5", ptrVal(rule.HTTP.Metadata["concurrentRequests"]))

	assert.Empty(t, listContainerAppReplicas(t, rg, name, revision), "no request has come, so the revision runs no replica")
	assert.Equal(t, "scaled", containerAppIngressGet(t, ptrVal(created.Properties.Configuration.Ingress.Fqdn)),
		"a request starts a replica and is answered by it")
	assert.Len(t, listContainerAppReplicas(t, rg, name, revision), 1)

	// Container Apps reports the scale-in only through the replica list,
	// which the scaler changes on its 15-second evaluation once the cooldown
	// has passed.
	require.Eventually(t, func() bool {
		return len(listContainerAppReplicas(t, rg, name, revision)) == 0
	}, 2*time.Minute, time.Second, "the revision scales back to zero")
}
