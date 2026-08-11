package azure_cli_test

import (
	"strings"
	"testing"
)

// TestAzureCosmosDB_MultiAPIFamiliesCLIFlows drives the MongoDB / Cassandra /
// Gremlin resource families, throughput autoscale migration, and the provider
// operations/locations surfaces through `az rest` against the simulator.
func TestAzureCosmosDB_MultiAPIFamiliesCLIFlows(t *testing.T) {
	account := "clicosmosapis"
	const ver = "2024-08-15"
	acctURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account, ver)
	runCLI(t, azRest("PUT", acctURL, `{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard","locations":[{"locationName":"East US","failoverPriority":0}]}}`))

	// MongoDB database + collection.
	mongoDBURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/mongodbDatabases/shop", ver)
	out := runCLI(t, azRest("PUT", mongoDBURL, `{"properties":{"resource":{"id":"shop"},"options":{"throughput":400}}}`))
	if !strings.Contains(out, `"name": "shop"`) {
		t.Fatalf("MongoDB database create missing name: %s", out)
	}
	collURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/mongodbDatabases/shop/collections/orders", ver)
	collOut := runCLI(t, azRest("PUT", collURL, `{"properties":{"resource":{"id":"orders","shardKey":{"region":"Hash"}}}}`))
	if !strings.Contains(collOut, `"name": "orders"`) {
		t.Fatalf("MongoDB collection create missing name: %s", collOut)
	}
	mongoTpURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/mongodbDatabases/shop/throughputSettings/default", ver)
	tpOut := runCLI(t, azRest("GET", mongoTpURL, ""))
	if !strings.Contains(tpOut, `"throughput": 400`) {
		t.Fatalf("MongoDB database throughput missing RU value: %s", tpOut)
	}
	migrateURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/mongodbDatabases/shop/throughputSettings/default/migrateToAutoscale", ver)
	migOut := runCLI(t, azRest("POST", migrateURL, ""))
	if !strings.Contains(migOut, "autoscaleSettings") {
		t.Fatalf("migrateToAutoscale missing autoscaleSettings: %s", migOut)
	}

	// Cassandra keyspace.
	ksURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/cassandraKeyspaces/ks", ver)
	ksOut := runCLI(t, azRest("PUT", ksURL, `{"properties":{"resource":{"id":"ks"}}}`))
	if !strings.Contains(ksOut, `"name": "ks"`) {
		t.Fatalf("Cassandra keyspace create missing name: %s", ksOut)
	}
	ksListURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/cassandraKeyspaces", ver)
	ksListOut := runCLI(t, azRest("GET", ksListURL, ""))
	if !strings.Contains(ksListOut, `"name": "ks"`) {
		t.Fatalf("Cassandra keyspace list missing keyspace: %s", ksListOut)
	}

	// Gremlin database + graph.
	gremlinDBURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/gremlinDatabases/social", ver)
	runCLI(t, azRest("PUT", gremlinDBURL, `{"properties":{"resource":{"id":"social"}}}`))
	graphURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/gremlinDatabases/social/graphs/people", ver)
	graphOut := runCLI(t, azRest("PUT", graphURL, `{"properties":{"resource":{"id":"people","partitionKey":{"paths":["/id"],"kind":"Hash"}}}}`))
	if !strings.Contains(graphOut, `"name": "people"`) {
		t.Fatalf("Gremlin graph create missing name: %s", graphOut)
	}

	// Provider operations + locations.
	opsURL := baseURL + "/providers/Microsoft.DocumentDB/operations?api-version=" + ver
	opsOut := runCLI(t, azRest("GET", opsURL, ""))
	if !strings.Contains(opsOut, "databaseAccounts/read") {
		t.Fatalf("operations list missing entries: %s", opsOut)
	}
	locURL := baseURL + "/subscriptions/" + subscriptionID + "/providers/Microsoft.DocumentDB/locations?api-version=" + ver
	locOut := runCLI(t, azRest("GET", locURL, ""))
	if !strings.Contains(locOut, "backupStorageRedundancies") {
		t.Fatalf("locations list missing properties: %s", locOut)
	}

	// Account name existence (HEAD): a follow-on REST HEAD returns 200/empty
	// for the existing account.
	nameURL := baseURL + "/providers/Microsoft.DocumentDB/databaseAccountNames/" + account + "?api-version=" + ver
	runCLI(t, azRest("HEAD", nameURL, ""))
}
