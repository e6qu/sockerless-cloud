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

// ensureCosmosAccount provisions a Cosmos DB account via the ARM REST surface
// the existing handlers serve, returning the account name.
func ensureCosmosAccount(t *testing.T, rg, account string) {
	t.Helper()
	ensureRG(t, rg)
	armBase := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.DocumentDB/databaseAccounts/%s", subscriptionID, rg, account)
	resp := armReq(t, "PUT", armBase, `{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard","locations":[{"locationName":"East US","failoverPriority":0}]}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

func TestAzureCosmosDB_MongoDBResourcesSDKLifecycle(t *testing.T) {
	rg, account, db, coll := "sdk-cosmos-mongo-rg", "sdkcosmosmongo", "appdb", "users"
	ensureCosmosAccount(t, rg, account)

	client, err := armcosmos.NewMongoDBResourcesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	dbPoller, err := client.BeginCreateUpdateMongoDBDatabase(ctx, rg, account, db, armcosmos.MongoDBDatabaseCreateUpdateParameters{
		Properties: &armcosmos.MongoDBDatabaseCreateUpdateProperties{
			Resource: &armcosmos.MongoDBDatabaseResource{ID: to.Ptr(db)},
			Options:  &armcosmos.CreateUpdateOptions{Throughput: to.Ptr[int32](400)},
		},
	}, nil)
	require.NoError(t, err)
	createdDB, err := dbPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, createdDB.Properties.Resource)
	assert.Equal(t, db, *createdDB.Properties.Resource.ID)

	gotDB, err := client.GetMongoDBDatabase(ctx, rg, account, db, nil)
	require.NoError(t, err)
	assert.Equal(t, db, *gotDB.Properties.Resource.ID)

	dbThroughput, err := client.GetMongoDBDatabaseThroughput(ctx, rg, account, db, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(400), *dbThroughput.Properties.Resource.Throughput)

	// Manual -> autoscale migration sets autoscaleSettings.maxThroughput.
	migPoller, err := client.BeginMigrateMongoDBDatabaseToAutoscale(ctx, rg, account, db, nil)
	require.NoError(t, err)
	migrated, err := migPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, migrated.Properties.Resource.AutoscaleSettings)
	assert.Equal(t, int32(4000), *migrated.Properties.Resource.AutoscaleSettings.MaxThroughput)

	collPoller, err := client.BeginCreateUpdateMongoDBCollection(ctx, rg, account, db, coll, armcosmos.MongoDBCollectionCreateUpdateParameters{
		Properties: &armcosmos.MongoDBCollectionCreateUpdateProperties{
			Resource: &armcosmos.MongoDBCollectionResource{
				ID:       to.Ptr(coll),
				ShardKey: map[string]*string{"region": to.Ptr("Hash")},
			},
		},
	}, nil)
	require.NoError(t, err)
	createdColl, err := collPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, coll, *createdColl.Properties.Resource.ID)

	pager := client.NewListMongoDBCollectionsPager(rg, account, db, nil)
	require.True(t, pager.More())
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, page.Value)
	assert.Equal(t, coll, *page.Value[0].Name)

	delPoller, err := client.BeginDeleteMongoDBCollection(ctx, rg, account, db, coll, nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

func TestAzureCosmosDB_CassandraResourcesSDKLifecycle(t *testing.T) {
	rg, account, keyspace, table := "sdk-cosmos-cassandra-rg", "sdkcosmoscassandra", "ks1", "tbl1"
	ensureCosmosAccount(t, rg, account)

	client, err := armcosmos.NewCassandraResourcesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	ksPoller, err := client.BeginCreateUpdateCassandraKeyspace(ctx, rg, account, keyspace, armcosmos.CassandraKeyspaceCreateUpdateParameters{
		Properties: &armcosmos.CassandraKeyspaceCreateUpdateProperties{
			Resource: &armcosmos.CassandraKeyspaceResource{ID: to.Ptr(keyspace)},
		},
	}, nil)
	require.NoError(t, err)
	createdKS, err := ksPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, keyspace, *createdKS.Properties.Resource.ID)

	tblPoller, err := client.BeginCreateUpdateCassandraTable(ctx, rg, account, keyspace, table, armcosmos.CassandraTableCreateUpdateParameters{
		Properties: &armcosmos.CassandraTableCreateUpdateProperties{
			Resource: &armcosmos.CassandraTableResource{
				ID: to.Ptr(table),
				Schema: &armcosmos.CassandraSchema{
					Columns: []*armcosmos.Column{{Name: to.Ptr("id"), Type: to.Ptr("text")}},
					PartitionKeys: []*armcosmos.CassandraPartitionKey{
						{Name: to.Ptr("id")},
					},
				},
			},
			Options: &armcosmos.CreateUpdateOptions{Throughput: to.Ptr[int32](500)},
		},
	}, nil)
	require.NoError(t, err)
	createdTbl, err := tblPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, table, *createdTbl.Properties.Resource.ID)

	tblThroughput, err := client.GetCassandraTableThroughput(ctx, rg, account, keyspace, table, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(500), *tblThroughput.Properties.Resource.Throughput)

	ksList := client.NewListCassandraKeyspacesPager(rg, account, nil)
	page, err := ksList.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, page.Value)
	assert.Equal(t, keyspace, *page.Value[0].Name)

	delPoller, err := client.BeginDeleteCassandraKeyspace(ctx, rg, account, keyspace, nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// Cascading delete removed the keyspace's tables.
	_, err = client.GetCassandraTable(ctx, rg, account, keyspace, table, nil)
	require.Error(t, err)
}

func TestAzureCosmosDB_GremlinResourcesSDKLifecycle(t *testing.T) {
	rg, account, db, graph := "sdk-cosmos-gremlin-rg", "sdkcosmosgremlin", "graphdb", "g1"
	ensureCosmosAccount(t, rg, account)

	client, err := armcosmos.NewGremlinResourcesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	dbPoller, err := client.BeginCreateUpdateGremlinDatabase(ctx, rg, account, db, armcosmos.GremlinDatabaseCreateUpdateParameters{
		Properties: &armcosmos.GremlinDatabaseCreateUpdateProperties{
			Resource: &armcosmos.GremlinDatabaseResource{ID: to.Ptr(db)},
		},
	}, nil)
	require.NoError(t, err)
	_, err = dbPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	graphPoller, err := client.BeginCreateUpdateGremlinGraph(ctx, rg, account, db, graph, armcosmos.GremlinGraphCreateUpdateParameters{
		Properties: &armcosmos.GremlinGraphCreateUpdateProperties{
			Resource: &armcosmos.GremlinGraphResource{
				ID:           to.Ptr(graph),
				PartitionKey: &armcosmos.ContainerPartitionKey{Paths: []*string{to.Ptr("/id")}, Kind: to.Ptr(armcosmos.PartitionKindHash)},
			},
			Options: &armcosmos.CreateUpdateOptions{
				AutoscaleSettings: &armcosmos.AutoscaleSettings{MaxThroughput: to.Ptr[int32](4000)},
			},
		},
	}, nil)
	require.NoError(t, err)
	createdGraph, err := graphPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, graph, *createdGraph.Properties.Resource.ID)

	graphThroughput, err := client.GetGremlinGraphThroughput(ctx, rg, account, db, graph, nil)
	require.NoError(t, err)
	require.NotNil(t, graphThroughput.Properties.Resource.AutoscaleSettings)
	assert.Equal(t, int32(4000), *graphThroughput.Properties.Resource.AutoscaleSettings.MaxThroughput)

	// Autoscale -> manual migration.
	migPoller, err := client.BeginMigrateGremlinGraphToManualThroughput(ctx, rg, account, db, graph, nil)
	require.NoError(t, err)
	migrated, err := migPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, migrated.Properties.Resource.Throughput)
	assert.Equal(t, int32(4000), *migrated.Properties.Resource.Throughput)

	listGraphs := client.NewListGremlinGraphsPager(rg, account, db, nil)
	page, err := listGraphs.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, page.Value)
}

func TestAzureCosmosDB_SQLScriptsSDKLifecycle(t *testing.T) {
	rg, account, db, container := "sdk-cosmos-sqlscripts-rg", "sdkcosmossqlscripts", "appdb", "users"
	ensureCosmosAccount(t, rg, account)
	armBase := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.DocumentDB/databaseAccounts/%s", subscriptionID, rg, account)
	resp := armReq(t, "PUT", armBase+"/sqlDatabases/"+db, `{"properties":{"resource":{"id":"appdb"}}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	resp = armReq(t, "PUT", armBase+"/sqlDatabases/"+db+"/containers/"+container, `{"properties":{"resource":{"id":"users","partitionKey":{"paths":["/id"],"kind":"Hash"}}}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	client, err := armcosmos.NewSQLResourcesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	spPoller, err := client.BeginCreateUpdateSQLStoredProcedure(ctx, rg, account, db, container, "sp1", armcosmos.SQLStoredProcedureCreateUpdateParameters{
		Properties: &armcosmos.SQLStoredProcedureCreateUpdateProperties{
			Resource: &armcosmos.SQLStoredProcedureResource{
				ID:   to.Ptr("sp1"),
				Body: to.Ptr("function f(){ var x = 1; }"),
			},
		},
	}, nil)
	require.NoError(t, err)
	createdSP, err := spPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "sp1", *createdSP.Properties.Resource.ID)

	gotSP, err := client.GetSQLStoredProcedure(ctx, rg, account, db, container, "sp1", nil)
	require.NoError(t, err)
	assert.Equal(t, "sp1", *gotSP.Properties.Resource.ID)

	udfPoller, err := client.BeginCreateUpdateSQLUserDefinedFunction(ctx, rg, account, db, container, "udf1", armcosmos.SQLUserDefinedFunctionCreateUpdateParameters{
		Properties: &armcosmos.SQLUserDefinedFunctionCreateUpdateProperties{
			Resource: &armcosmos.SQLUserDefinedFunctionResource{ID: to.Ptr("udf1"), Body: to.Ptr("function u(){}")},
		},
	}, nil)
	require.NoError(t, err)
	_, err = udfPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	trgPoller, err := client.BeginCreateUpdateSQLTrigger(ctx, rg, account, db, container, "trg1", armcosmos.SQLTriggerCreateUpdateParameters{
		Properties: &armcosmos.SQLTriggerCreateUpdateProperties{
			Resource: &armcosmos.SQLTriggerResource{
				ID:               to.Ptr("trg1"),
				Body:             to.Ptr("function t(){}"),
				TriggerType:      to.Ptr(armcosmos.TriggerTypePre),
				TriggerOperation: to.Ptr(armcosmos.TriggerOperationCreate),
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = trgPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	spList := client.NewListSQLStoredProceduresPager(rg, account, db, container, nil)
	page, err := spList.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, page.Value)

	udfList := client.NewListSQLUserDefinedFunctionsPager(rg, account, db, container, nil)
	udfPage, err := udfList.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, udfPage.Value)

	trgList := client.NewListSQLTriggersPager(rg, account, db, container, nil)
	trgPage, err := trgList.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, trgPage.Value)

	delPoller, err := client.BeginDeleteSQLStoredProcedure(ctx, rg, account, db, container, "sp1", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

func TestAzureCosmosDB_SQLThroughputMigrateSDK(t *testing.T) {
	rg, account, db := "sdk-cosmos-sqlmigrate-rg", "sdkcosmossqlmigrate", "appdb"
	ensureCosmosAccount(t, rg, account)

	client, err := armcosmos.NewSQLResourcesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	dbPoller, err := client.BeginCreateUpdateSQLDatabase(ctx, rg, account, db, armcosmos.SQLDatabaseCreateUpdateParameters{
		Properties: &armcosmos.SQLDatabaseCreateUpdateProperties{
			Resource: &armcosmos.SQLDatabaseResource{ID: to.Ptr(db)},
			Options:  &armcosmos.CreateUpdateOptions{Throughput: to.Ptr[int32](600)},
		},
	}, nil)
	require.NoError(t, err)
	_, err = dbPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	migPoller, err := client.BeginMigrateSQLDatabaseToAutoscale(ctx, rg, account, db, nil)
	require.NoError(t, err)
	migrated, err := migPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, migrated.Properties.Resource.AutoscaleSettings)
	assert.Equal(t, int32(4000), *migrated.Properties.Resource.AutoscaleSettings.MaxThroughput)

	backPoller, err := client.BeginMigrateSQLDatabaseToManualThroughput(ctx, rg, account, db, nil)
	require.NoError(t, err)
	back, err := backPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, back.Properties.Resource.Throughput)
	assert.Equal(t, int32(4000), *back.Properties.Resource.Throughput)
}

func TestAzureCosmosDB_AccountOperationsSDK(t *testing.T) {
	rg, account := "sdk-cosmos-acct-rg", "sdkcosmosacct"
	ensureCosmosAccount(t, rg, account)

	client, err := armcosmos.NewDatabaseAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// Update tags.
	updPoller, err := client.BeginUpdate(ctx, rg, account, armcosmos.DatabaseAccountUpdateParameters{
		Tags: map[string]*string{"env": to.Ptr("test")},
	}, nil)
	require.NoError(t, err)
	updated, err := updPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "test", *updated.Tags["env"])

	// Read-only keys (GET).
	roKeys, err := client.GetReadOnlyKeys(ctx, rg, account, nil)
	require.NoError(t, err)
	require.NotNil(t, roKeys.PrimaryReadonlyMasterKey)
	assert.NotEmpty(t, *roKeys.PrimaryReadonlyMasterKey)

	// List accounts in the subscription.
	listPager := client.NewListPager(nil)
	listPage, err := listPager.NextPage(ctx)
	require.NoError(t, err)
	found := false
	for _, a := range listPage.Value {
		if a.Name != nil && *a.Name == account {
			found = true
		}
	}
	assert.True(t, found, "ListBySubscription should include the created account")

	// Name existence: created account name is taken; a random name is not.
	exists, err := client.CheckNameExists(ctx, account, nil)
	require.NoError(t, err)
	assert.True(t, exists.Success)
	missing, err := client.CheckNameExists(ctx, "definitely-not-a-real-cosmos-account-xyz", nil)
	require.NoError(t, err)
	assert.False(t, missing.Success)

	// Regenerate the primary key, then confirm listKeys reports new material.
	keysBefore, err := client.ListKeys(ctx, rg, account, nil)
	require.NoError(t, err)
	regenPoller, err := client.BeginRegenerateKey(ctx, rg, account, armcosmos.DatabaseAccountRegenerateKeyParameters{
		KeyKind: to.Ptr(armcosmos.KeyKindPrimary),
	}, nil)
	require.NoError(t, err)
	_, err = regenPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	keysAfter, err := client.ListKeys(ctx, rg, account, nil)
	require.NoError(t, err)
	assert.NotEqual(t, *keysBefore.PrimaryMasterKey, *keysAfter.PrimaryMasterKey, "regenerateKey must rotate the primary key")
}

func TestAzureCosmosDB_OperationsAndLocationsSDK(t *testing.T) {
	opsClient, err := armcosmos.NewOperationsClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	opsPager := opsClient.NewListPager(nil)
	opsPage, err := opsPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, opsPage.Value)

	locClient, err := armcosmos.NewLocationsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	locPager := locClient.NewListPager(nil)
	locPage, err := locPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, locPage.Value)

	got, err := locClient.Get(ctx, "eastus", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	assert.Equal(t, "eastus", *got.Name)
}

// TestAzureCosmosDB_ClientEncryptionKeysREST exercises the client-encryption-key
// control plane (the armcosmos v1.0.0 release predates a typed client for it, so
// the REST surface the SDK would call is driven directly).
func TestAzureCosmosDB_ClientEncryptionKeysREST(t *testing.T) {
	rg, account, db, cek := "sdk-cosmos-cek-rg", "sdkcosmoscek", "appdb", "cek1"
	ensureCosmosAccount(t, rg, account)
	armBase := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.DocumentDB/databaseAccounts/%s", subscriptionID, rg, account)
	resp := armReq(t, "PUT", armBase+"/sqlDatabases/"+db, `{"properties":{"resource":{"id":"appdb"}}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	cekPath := armBase + "/sqlDatabases/" + db + "/clientEncryptionKeys/" + cek
	resp = armReq(t, "PUT", cekPath, `{"properties":{"resource":{"id":"cek1","encryptionAlgorithm":"AEAD_AES_256_CBC_HMAC_SHA256","wrappedDataEncryptionKey":"AAAA","keyWrapMetadata":{"name":"akvKey","type":"AzureKeyVault","value":"https://kv.vault.azure.net/keys/k/1"}}}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	resp = armReq(t, "GET", cekPath, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	resp = armReq(t, "GET", armBase+"/sqlDatabases/"+db+"/clientEncryptionKeys", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}
