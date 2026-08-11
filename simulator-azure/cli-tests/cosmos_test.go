package azure_cli_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAzureCosmosDB_ARMAndDataPlaneRESTCLIFlows(t *testing.T) {
	account := "clicosmos"
	armBase := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account, "2024-05-15")
	runCLI(t, azRest("PUT", armBase, `{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard"}}`))
	runCLI(t, azRest("POST", strings.Replace(armBase, "?api-version=", "/listKeys?api-version=", 1), ""))

	dbURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/sqlDatabases/appdb", "2024-05-15")
	runCLI(t, azRest("PUT", dbURL, `{"properties":{"resource":{"id":"appdb"}}}`))

	collURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/sqlDatabases/appdb/containers/users", "2024-05-15")
	runCLI(t, azRest("PUT", collURL, `{"properties":{"resource":{"id":"users","partitionKey":{"paths":["/id"],"kind":"Hash"}}}}`))

	tableURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/tables/clitable", "2024-08-15")
	tableOut := runCLI(t, azRest("PUT", tableURL, `{"properties":{"resource":{"id":"clitable"},"options":{"throughput":400}}}`))
	if !strings.Contains(tableOut, `"name": "clitable"`) {
		t.Fatalf("Cosmos table create response missing table name: %s", tableOut)
	}
	listTablesURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/tables", "2024-08-15")
	listOut := runCLI(t, azRest("GET", listTablesURL, ""))
	if !strings.Contains(listOut, `"name": "clitable"`) {
		t.Fatalf("Cosmos table list response missing table: %s", listOut)
	}
	throughputURL := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account+"/tables/clitable/throughputSettings/default", "2024-08-15")
	throughputOut := runCLI(t, azRest("GET", throughputURL, ""))
	if !strings.Contains(throughputOut, `"throughput": 400`) {
		t.Fatalf("Cosmos table throughput response missing RU value: %s", throughputOut)
	}

	req, err := http.NewRequest("POST", baseURL+"/dbs/appdb/colls/users/docs", strings.NewReader(`{"id":"alice","team":"platform"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-ms-cosmos-account", account)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create document status = %d", resp.StatusCode)
	}

	req, err = http.NewRequest("POST", baseURL+"/dbs/appdb/colls/users/docs", strings.NewReader(`{"query":"SELECT * FROM c WHERE c.team = 'platform'"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-ms-cosmos-account", account)
	req.Header.Set("Content-Type", "application/query+json")
	req.Header.Set("x-ms-documentdb-isquery", "True")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "alice") {
		t.Fatalf("query response did not include alice: %s", string(body))
	}

	runCLI(t, azRest("DELETE", tableURL, ""))
}

func TestAzureStorageTables_ARMRESTCLIFlows(t *testing.T) {
	account := "clitableacct"
	accountURL := armURL("Microsoft.Storage", "storageAccounts/"+account, "2023-05-01")
	runCLI(t, azRest("PUT", accountURL, `{"location":"eastus","kind":"StorageV2","sku":{"name":"Standard_LRS"}}`))

	tableURL := armURL("Microsoft.Storage", "storageAccounts/"+account+"/tableServices/default/tables/clistorage", "2024-01-01")
	out := runCLI(t, azRest("PUT", tableURL, ""))
	if !strings.Contains(out, `"tableName": "clistorage"`) {
		t.Fatalf("storage table create response missing tableName: %s", out)
	}
	out = runCLI(t, azRest("GET", tableURL, ""))
	if !strings.Contains(out, `"tableName": "clistorage"`) {
		t.Fatalf("storage table get response missing tableName: %s", out)
	}
	listURL := armURL("Microsoft.Storage", "storageAccounts/"+account+"/tableServices/default/tables", "2024-01-01")
	out = runCLI(t, azRest("GET", listURL, ""))
	if !strings.Contains(out, `"name": "clistorage"`) {
		t.Fatalf("storage table list response missing table: %s", out)
	}
	runCLI(t, azRest("DELETE", tableURL, ""))
}
