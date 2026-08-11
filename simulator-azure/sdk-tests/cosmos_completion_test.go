package azure_sdk_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cosmos/armcosmos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cosmosMetricFilter is a realistic Azure Monitor legacy metric $filter naming a
// metric, time grain and a one-hour window.
const cosmosMetricFilter = "(name.value eq 'Total Requests') and timeGrain eq duration'PT5M' and startTime eq 2024-01-01T00:00:00Z and endTime eq 2024-01-01T01:00:00Z"

// ensureCosmosMultiRegionAccount provisions a two-region Cosmos DB account so
// the online/offline-region operations have a non-write region to act on.
func ensureCosmosMultiRegionAccount(t *testing.T, rg, account string) {
	t.Helper()
	ensureRG(t, rg)
	armBase := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.DocumentDB/databaseAccounts/%s", subscriptionID, rg, account)
	body := `{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard","locations":[{"locationName":"East US","failoverPriority":0},{"locationName":"West US","failoverPriority":1}]}}`
	resp := armReq(t, "PUT", armBase, body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

// TestAzureCosmosDB_MetricsAndUsagesSDK drives the account/database/collection
// metric, usage and metric-definition list surfaces through the armcosmos
// clients that call them.
func TestAzureCosmosDB_MetricsAndUsagesSDK(t *testing.T) {
	rg, account := "sdk-cosmos-metrics-rg", "sdkcosmosmetrics"
	ensureCosmosAccount(t, rg, account)
	db, coll := "appdb", "users"

	acctClient, err := armcosmos.NewDatabaseAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	metricsPager := acctClient.NewListMetricsPager(rg, account, cosmosMetricFilter, nil)
	metricsPage, err := metricsPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, metricsPage.Value)
	require.NotNil(t, metricsPage.Value[0].Name)
	assert.Equal(t, "Total Requests", *metricsPage.Value[0].Name.Value)
	require.NotEmpty(t, metricsPage.Value[0].MetricValues)
	require.NotNil(t, metricsPage.Value[0].MetricValues[0].Timestamp)

	usagesPager := acctClient.NewListUsagesPager(rg, account, nil)
	usagesPage, err := usagesPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, usagesPage.Value)
	require.NotNil(t, usagesPage.Value[0].Name)

	defsPager := acctClient.NewListMetricDefinitionsPager(rg, account, nil)
	defsPage, err := defsPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, defsPage.Value)
	require.NotNil(t, defsPage.Value[0].PrimaryAggregationType)

	dbClient, err := armcosmos.NewDatabaseClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	dbMetrics, err := dbClient.NewListMetricsPager(rg, account, db, cosmosMetricFilter, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, dbMetrics.Value)
	dbUsages, err := dbClient.NewListUsagesPager(rg, account, db, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, dbUsages.Value)
	dbDefs, err := dbClient.NewListMetricDefinitionsPager(rg, account, db, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, dbDefs.Value)

	collClient, err := armcosmos.NewCollectionClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	collMetrics, err := collClient.NewListMetricsPager(rg, account, db, coll, cosmosMetricFilter, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, collMetrics.Value)
	collUsages, err := collClient.NewListUsagesPager(rg, account, db, coll, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, collUsages.Value)
	collDefs, err := collClient.NewListMetricDefinitionsPager(rg, account, db, coll, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, collDefs.Value)
}

// TestAzureCosmosDB_PartitionAndPercentileMetricsSDK drives the partition-,
// partition-key-range-, region- and percentile-scoped metric list surfaces.
func TestAzureCosmosDB_PartitionAndPercentileMetricsSDK(t *testing.T) {
	rg, account := "sdk-cosmos-partmetrics-rg", "sdkcosmospartmetrics"
	ensureCosmosAccount(t, rg, account)
	db, coll, region := "appdb", "users", "East US"

	cpClient, err := armcosmos.NewCollectionPartitionClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	cpMetrics, err := cpClient.NewListMetricsPager(rg, account, db, coll, cosmosMetricFilter, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, cpMetrics.Value)
	require.NotNil(t, cpMetrics.Value[0].PartitionKeyRangeID)
	cpUsages, err := cpClient.NewListUsagesPager(rg, account, db, coll, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, cpUsages.Value)
	require.NotNil(t, cpUsages.Value[0].PartitionKeyRangeID)

	cprClient, err := armcosmos.NewCollectionPartitionRegionClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	cprMetrics, err := cprClient.NewListMetricsPager(rg, account, region, db, coll, cosmosMetricFilter, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, cprMetrics.Value)

	pkrClient, err := armcosmos.NewPartitionKeyRangeIDClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pkrMetrics, err := pkrClient.NewListMetricsPager(rg, account, db, coll, "0", cosmosMetricFilter, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, pkrMetrics.Value)
	assert.Equal(t, "0", *pkrMetrics.Value[0].PartitionKeyRangeID)

	pkrrClient, err := armcosmos.NewPartitionKeyRangeIDRegionClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pkrrMetrics, err := pkrrClient.NewListMetricsPager(rg, account, region, db, coll, "0", cosmosMetricFilter, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, pkrrMetrics.Value)

	crClient, err := armcosmos.NewCollectionRegionClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	crMetrics, err := crClient.NewListMetricsPager(rg, account, region, db, coll, cosmosMetricFilter, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, crMetrics.Value)

	darClient, err := armcosmos.NewDatabaseAccountRegionClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	darMetrics, err := darClient.NewListMetricsPager(rg, account, region, cosmosMetricFilter, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, darMetrics.Value)

	percFilter := "(name.value eq 'Probabilistic Bounded Staleness') and timeGrain eq duration'PT5M' and startTime eq 2024-01-01T00:00:00Z and endTime eq 2024-01-01T01:00:00Z"
	pClient, err := armcosmos.NewPercentileClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pMetrics, err := pClient.NewListMetricsPager(rg, account, percFilter, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, pMetrics.Value)
	require.NotEmpty(t, pMetrics.Value[0].MetricValues)
	require.NotNil(t, pMetrics.Value[0].MetricValues[0].P99)

	ptClient, err := armcosmos.NewPercentileTargetClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	ptMetrics, err := ptClient.NewListMetricsPager(rg, account, "West US", percFilter, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, ptMetrics.Value)

	pstClient, err := armcosmos.NewPercentileSourceTargetClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pstMetrics, err := pstClient.NewListMetricsPager(rg, account, region, "West US", percFilter, nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, pstMetrics.Value)
}

// TestAzureCosmosDB_OnlineOfflineRegionSDK exercises the online-/offline-region
// long-running operations and the write-region offline rejection.
func TestAzureCosmosDB_OnlineOfflineRegionSDK(t *testing.T) {
	rg, account := "sdk-cosmos-region-rg", "sdkcosmosregion"
	ensureCosmosMultiRegionAccount(t, rg, account)

	client, err := armcosmos.NewDatabaseAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	offPoller, err := client.BeginOfflineRegion(ctx, rg, account, armcosmos.RegionForOnlineOffline{Region: to.Ptr("West US")}, nil)
	require.NoError(t, err)
	_, err = offPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	onPoller, err := client.BeginOnlineRegion(ctx, rg, account, armcosmos.RegionForOnlineOffline{Region: to.Ptr("West US")}, nil)
	require.NoError(t, err)
	_, err = onPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// The write region (failover priority 0) cannot be taken offline.
	_, err = client.BeginOfflineRegion(ctx, rg, account, armcosmos.RegionForOnlineOffline{Region: to.Ptr("East US")}, nil)
	require.Error(t, err)
}

// TestAzureCosmosDB_PrivateEndpointConnectionsSDK exercises the private
// endpoint connection list/get/create-or-update/delete surface.
func TestAzureCosmosDB_PrivateEndpointConnectionsSDK(t *testing.T) {
	rg, account, pecName := "sdk-cosmos-pec-rg", "sdkcosmospec", "pe-conn-1"
	ensureCosmosAccount(t, rg, account)

	client, err := armcosmos.NewPrivateEndpointConnectionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	peID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/privateEndpoints/pe1", subscriptionID, rg)
	putPoller, err := client.BeginCreateOrUpdate(ctx, rg, account, pecName, armcosmos.PrivateEndpointConnection{
		Properties: &armcosmos.PrivateEndpointConnectionProperties{
			PrivateEndpoint: &armcosmos.PrivateEndpointProperty{ID: to.Ptr(peID)},
			PrivateLinkServiceConnectionState: &armcosmos.PrivateLinkServiceConnectionStateProperty{
				Status:      to.Ptr("Approved"),
				Description: to.Ptr("Approved by the account owner"),
			},
			GroupID: to.Ptr("Sql"),
		},
	}, nil)
	require.NoError(t, err)
	created, err := putPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	assert.Equal(t, "Succeeded", *created.Properties.ProvisioningState)
	assert.Equal(t, pecName, *created.Name)

	got, err := client.Get(ctx, rg, account, pecName, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties.PrivateLinkServiceConnectionState)
	assert.Equal(t, "Approved", *got.Properties.PrivateLinkServiceConnectionState.Status)

	listPager := client.NewListByDatabaseAccountPager(rg, account, nil)
	listPage, err := listPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, listPage.Value)
	assert.Equal(t, pecName, *listPage.Value[0].Name)

	delPoller, err := client.BeginDelete(ctx, rg, account, pecName, nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	_, err = client.Get(ctx, rg, account, pecName, nil)
	require.Error(t, err)
}
