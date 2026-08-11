package main

import "testing"

// FuzzAzureResourceIDParsers fuzzes the ARM resource-id path extractors. These
// run over an untrusted resource ID drawn from request bodies (@odata.id,
// cross-resource references) and path params, and must never panic on a
// malformed `/subscriptions/X/resourceGroups/Y/providers/...` string.
func FuzzAzureResourceIDParsers(f *testing.F) {
	seeds := []string{
		"",
		"/",
		"//",
		"/subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.DocumentDB/databaseAccounts/acc1",
		"/subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.DocumentDB/databaseAccounts/acc1/sqlDatabases/db1/containers/c1",
		"subscriptions",
		"/subscriptions/",
		"resourceGroups/",
		"/subscriptions/sub1/resourceGroups",
		"databaseAccounts/sqlDatabases/containers",
		"/////////databaseAccounts",
		"\x00\xff/subscriptions/\x00",
		"databaseAccounts/",
		"sqlDatabases/",
		"containers/",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, id string) {
		// None of these may panic on any input.
		_ = azureSubscriptionFromID(id, "default-sub")
		_ = azureResourceGroupFromID(id, "default-rg")
		_, _, _, _ = cosmosARMIDNames(id)
	})
}

// FuzzAzureStorageAddressParsers fuzzes the storage/messaging address parsers
// that decompose an untrusted Host header or AMQP address into account /
// container / blob / hub / partition components. A malformed host or address
// must produce ok=false, never a panic.
func FuzzAzureStorageAddressParsers(f *testing.F) {
	seeds := []string{
		"",
		"/",
		"acct.blob.localhost/container/blob",
		"https://acct.blob.localhost:4568/c/b",
		"acct.blob.",
		".blob.",
		"acct.blob.localhost/",
		"http://[::1]/x",
		":",
		"hub/Partitions/0",
		"cg/ConsumerGroups/x/Partitions/3",
		"a/b/c/d/e/f",
		"\x00.blob.\x00",
		"acct.blob.localhost/%ZZ/blob",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _, _, _ = parseBlobCopySource(s)
		_, _, _ = splitContainerBlobPath(s)
		_, _, _, _ = parseACRBlobURL(s)
		_, _, _ = ehAMQPParseEventHubAddress(s)
		_, _, _ = ehAMQPParseConsumerAddress(s)
	})
}

// FuzzCosmosSQLQuery fuzzes the Cosmos SQL-subset parser + evaluator, which run
// over an untrusted SQL query string from the Cosmos data plane. Neither parsing
// nor evaluation may panic regardless of how malformed the query is.
func FuzzCosmosSQLQuery(f *testing.F) {
	seeds := []string{
		"",
		"SELECT * FROM c",
		"SELECT c.a, c.b FROM c",
		"SELECT VALUE COUNT(1) FROM c",
		"SELECT * FROM c WHERE c.id = 'x'",
		"SELECT * FROM c WHERE c.id = @id",
		"SELECT * FROM c WHERE c.a > 1 AND c.b <= 2 OR NOT c.c = 'z'",
		"SELECT * FROM c WHERE c.tags IN ('a','b') AND CONTAINS(c.name, 'foo')",
		"SELECT * FROM c WHERE STARTSWITH(c.x,'a') ORDER BY c.n DESC OFFSET 1 LIMIT 2",
		"SELECT TOP 3 * FROM c",
		"where",
		"where =",
		"where ===",
		"WHERE c.a = b = c",
		"where c.id=",
		"\x00where\x00=\x00",
		"WhErE   c.[`x`]   =   'v'",
		"SELECT ( ( ( ( ( ( c.a",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	params := map[string]any{"@id": "x", "@n": float64(1)}
	docs := []CosmosDocument{
		{ID: "1", Body: map[string]any{"id": "1", "a": float64(2), "name": "foobar", "tags": "a"}},
		{ID: "2", Body: map[string]any{"id": "2", "a": float64(0), "n": float64(5)}},
	}
	f.Fuzz(func(t *testing.T, query string) {
		plan, err := cosmosParseQuery(query)
		if err != nil {
			return
		}
		_ = cosmosRunQuery(plan, docs, params)
	})
}
