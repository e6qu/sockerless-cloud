package azure_sdk_test

import (
	"strconv"
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

// A revision scales on its Azure Queue rule: the rule reads the queue as the
// connection string a secret holds, every polling interval, and the revision
// runs one replica per queueLength messages, within its maximum, and none once
// the queue is empty and the cooldown has passed.
func TestSDK_ContainerAppsScale_QueueRuleFollowsTheQueueLength(t *testing.T) {
	t.Parallel()
	rg, name := uniqueName("sdk-aca-qscale-rg"), uniqueName("acaqscale")
	ensureRG(t, rg)
	account := uniqueAlnumName("acaqscale")
	key := storageAccountPrimaryKey(account)
	require.NotEmpty(t, key)
	queues := queueSDKService(t, account)
	_, err := queues.CreateQueue(ctx, "work", nil)
	require.NoError(t, err)
	queue := queues.NewQueueClient("work")

	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	app := servingContainerApp(rg, "worker", &armappcontainers.Configuration{
		Secrets: []*armappcontainers.Secret{{
			Name:  to.Ptr("queue-connection"),
			Value: to.Ptr("DefaultEndpointsProtocol=https;AccountName=" + account + ";AccountKey=" + key + ";EndpointSuffix=core.windows.net"),
		}},
	})
	app.Properties.Template.Scale = &armappcontainers.Scale{
		MinReplicas:     to.Ptr[int32](0),
		MaxReplicas:     to.Ptr[int32](3),
		CooldownPeriod:  to.Ptr[int32](2),
		PollingInterval: to.Ptr[int32](1),
		Rules: []*armappcontainers.ScaleRule{{
			Name: to.Ptr("queue-rule"),
			AzureQueue: &armappcontainers.QueueScaleRule{
				AccountName: to.Ptr(account),
				QueueName:   to.Ptr("work"),
				QueueLength: to.Ptr[int32](2),
				Auth:        []*armappcontainers.ScaleRuleAuth{{SecretRef: to.Ptr("queue-connection"), TriggerParameter: to.Ptr("connection")}},
			},
		}},
	}
	created := createContainerApp(t, client, rg, name, app)
	revision := ptrVal(created.Properties.LatestRevisionName)
	assert.Empty(t, listContainerAppReplicas(t, rg, name, revision), "an empty queue asks for no replica")

	for i := range 3 {
		_, err := queue.EnqueueMessage(ctx, "job "+strconv.Itoa(i), nil)
		require.NoError(t, err)
	}
	// The replica list is the only place Container Apps reports a scale, and
	// the scaler changes it on its polling interval.
	require.Eventually(t, func() bool {
		return len(listContainerAppReplicas(t, rg, name, revision)) == 2
	}, 2*time.Minute, time.Second, "three messages at two per replica ask for two replicas")
	for i := range 5 {
		_, err := queue.EnqueueMessage(ctx, "more "+strconv.Itoa(i), nil)
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool {
		return len(listContainerAppReplicas(t, rg, name, revision)) == 3
	}, 2*time.Minute, time.Second, "eight messages ask for four, held to the maximum of three")

	_, err = queue.ClearMessages(ctx, nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return len(listContainerAppReplicas(t, rg, name, revision)) == 0
	}, 2*time.Minute, time.Second, "the emptied queue scales the revision to zero once the cooldown passes")
}
