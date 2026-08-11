package azure_sdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cosmos/armcosmos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAzureCosmosDB_ARMAndDataPlaneLifecycle(t *testing.T) {
	sub := "00000000-0000-0000-0000-000000000000"
	rg := "test-rg"
	account := "sdkcosmos"
	armBase := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.DocumentDB/databaseAccounts/%s", sub, rg, account)

	resp := armReq(t, "PUT", armBase, `{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard"}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), `"provisioningState":"Succeeded"`)
	assert.Contains(t, string(body), `"documentEndpoint"`)

	resp = armReq(t, "POST", armBase+"/listKeys", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), "primaryMasterKey")

	dbPath := armBase + "/sqlDatabases/appdb"
	resp = armReq(t, "PUT", dbPath, `{"properties":{"resource":{"id":"appdb"}}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	collPath := dbPath + "/containers/users"
	resp = armReq(t, "PUT", collPath, `{"properties":{"resource":{"id":"users","partitionKey":{"paths":["/id"],"kind":"Hash"}}}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	collBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var coll struct {
		Properties struct {
			PartitionKey map[string]any `json:"partitionKey"`
			Resource     struct {
				PartitionKey map[string]any `json:"partitionKey"`
			} `json:"resource"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(collBody, &coll))
	assert.Nil(t, coll.Properties.PartitionKey, "partitionKey must nest under properties.resource, not properties")
	assert.Equal(t, "Hash", coll.Properties.Resource.PartitionKey["kind"])

	req, err := http.NewRequest("POST", baseURL+"/dbs/appdb/colls/users/docs", strings.NewReader(`{"id":"alice","team":"platform"}`))
	require.NoError(t, err)
	req.Header.Set("x-ms-cosmos-account", account)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	req, err = http.NewRequest("POST", baseURL+"/dbs/appdb/colls/users/docs", strings.NewReader(`{"query":"SELECT * FROM c WHERE c.team = 'platform'"}`))
	require.NoError(t, err)
	req.Header.Set("x-ms-cosmos-account", account)
	req.Header.Set("Content-Type", "application/query+json")
	req.Header.Set("x-ms-documentdb-isquery", "True")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Contains(t, string(body), `"alice"`)
}

func TestAzureCosmosDB_TableARMOfficialSDKLifecycle(t *testing.T) {
	rg := "sdk-cosmos-table-rg"
	account := "sdkcosmostable"
	table := "sdkUsers"
	armBase := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.DocumentDB/databaseAccounts/%s", subscriptionID, rg, account)

	ensureRG(t, rg)
	resp := armReq(t, "PUT", armBase, `{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard"}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	client, err := armcosmos.NewTableResourcesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	poller, err := client.BeginCreateUpdateTable(ctx, rg, account, table, armcosmos.TableCreateUpdateParameters{
		Location: to.Ptr("eastus"),
		Properties: &armcosmos.TableCreateUpdateProperties{
			Resource: &armcosmos.TableResource{ID: to.Ptr(table)},
			Options:  &armcosmos.CreateUpdateOptions{Throughput: to.Ptr[int32](400)},
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.Resource)
	assert.Equal(t, table, *created.Properties.Resource.ID)

	got, err := client.GetTable(ctx, rg, account, table, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	require.NotNil(t, got.Properties.Resource)
	assert.Equal(t, table, *got.Properties.Resource.ID)

	pager := client.NewListTablesPager(rg, account, nil)
	require.True(t, pager.More())
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, page.Value)
	assert.Equal(t, table, *page.Value[0].Name)

	throughput, err := client.GetTableThroughput(ctx, rg, account, table, nil)
	require.NoError(t, err)
	require.NotNil(t, throughput.Properties)
	require.NotNil(t, throughput.Properties.Resource)
	assert.Equal(t, int32(400), *throughput.Properties.Resource.Throughput)

	updatePoller, err := client.BeginUpdateTableThroughput(ctx, rg, account, table, armcosmos.ThroughputSettingsUpdateParameters{
		Properties: &armcosmos.ThroughputSettingsUpdateProperties{
			Resource: &armcosmos.ThroughputSettingsResource{Throughput: to.Ptr[int32](500)},
		},
	}, nil)
	require.NoError(t, err)
	updated, err := updatePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.Properties)
	require.NotNil(t, updated.Properties.Resource)
	assert.Equal(t, int32(500), *updated.Properties.Resource.Throughput)

	deletePoller, err := client.BeginDeleteTable(ctx, rg, account, table, nil)
	require.NoError(t, err)
	_, err = deletePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}
