package azure_sdk_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cosmos/armcosmos"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The account's metrics aggregate the requests its data plane served and the
// request units they charged, and its usages count the documents and bytes
// its containers hold.
func TestCosmos_MetricsCountTheDataPlane(t *testing.T) {
	t.Parallel()
	account := uniquename.Alnum("sdkcosmosmetricdata")
	client := cosmosSimClient(t, account)
	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "metricsdb"}, nil)
	require.NoError(t, err)
	db, err := client.NewDatabase("metricsdb")
	require.NoError(t, err)
	_, err = db.CreateContainer(ctx, azcosmos.ContainerProperties{
		ID:                     "events",
		PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/kind"}},
	}, nil)
	require.NoError(t, err)
	container, err := client.NewContainer("metricsdb", "events")
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		item := []byte(fmt.Sprintf(`{"id":"e%d","kind":"click","n":%d}`, i, i))
		_, err := container.CreateItem(ctx, azcosmos.NewPartitionKeyString("click"), item, nil)
		require.NoError(t, err)
	}

	start := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	end := time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339)
	filter := fmt.Sprintf("(name.value eq 'Total Requests' or name.value eq 'Total Request Units' or name.value eq 'Document Count') and timeGrain eq duration'PT1H' and startTime eq %s and endTime eq %s", start, end)
	accounts, err := armcosmos.NewDatabaseAccountsClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	page, err := accounts.NewListMetricsPager(cosmosDataResourceGroup, account, filter, nil).NextPage(ctx)
	require.NoError(t, err)
	totals := map[string]float64{}
	maxima := map[string]float64{}
	for _, m := range page.Value {
		for _, v := range m.MetricValues {
			totals[*m.Name.Value] += *v.Total
			if *v.Maximum > maxima[*m.Name.Value] {
				maxima[*m.Name.Value] = *v.Maximum
			}
		}
	}
	assert.GreaterOrEqual(t, totals["Total Requests"], float64(5), "the database, the container and three items were requests")
	assert.Positive(t, totals["Total Request Units"], "the writes charged request units")
	assert.Equal(t, float64(3), maxima["Document Count"])

	usages, err := accounts.NewListUsagesPager(cosmosDataResourceGroup, account, nil).NextPage(ctx)
	require.NoError(t, err)
	current := map[string]int64{}
	for _, u := range usages.Value {
		current[*u.Name.Value] = *u.CurrentValue
	}
	assert.Equal(t, int64(3), current["Document Count"])
	assert.Positive(t, current["Data Stored"])
	assert.Positive(t, current["Index Size"])
}
