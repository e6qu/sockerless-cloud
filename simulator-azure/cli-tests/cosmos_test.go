package azure_cli_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// cosmosSignDataPlane applies the shared-key authorization a Cosmos DB client
// builds: `type=master&ver=1.0&sig=<base64 HMAC-SHA256>` over
// "{verb}\n{resourceType}\n{resourceLink}\n{date}\n\n", URL-encoded whole,
// with the verb, the resource type and the date lowercased. An operation over a
// set of resources signs the parent's link; one on a single resource signs its
// own.
func cosmosSignDataPlane(t *testing.T, req *http.Request, key string) {
	t.Helper()
	segments := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
	resourceType, resourceLink := segments[len(segments)-1], strings.Join(segments[:len(segments)-1], "/")
	if len(segments)%2 == 0 {
		resourceType, resourceLink = segments[len(segments)-2], strings.Join(segments, "/")
	}
	date := time.Now().UTC().Format(http.TimeFormat)
	payload := strings.ToLower(req.Method) + "\n" + strings.ToLower(resourceType) + "\n" +
		resourceLink + "\n" + strings.ToLower(date) + "\n\n"
	material, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		t.Fatalf("decode account key: %v", err)
	}
	mac := hmac.New(sha256.New, material)
	if _, err := mac.Write([]byte(payload)); err != nil {
		t.Fatalf("sign payload: %v", err)
	}
	req.Header.Set("x-ms-date", date)
	req.Header.Set("Authorization",
		url.QueryEscape("type=master&ver=1.0&sig="+base64.StdEncoding.EncodeToString(mac.Sum(nil))))
}

func TestAzureCosmosDB_ARMAndDataPlaneRESTCLIFlows(t *testing.T) {
	account := "clicosmos"
	armBase := armURL("Microsoft.DocumentDB", "databaseAccounts/"+account, "2024-05-15")
	runCLI(t, azRest("PUT", armBase, `{"location":"eastus","kind":"GlobalDocumentDB","properties":{"databaseAccountOfferType":"Standard"}}`))
	keysOut := runCLI(t, azRest("POST", strings.Replace(armBase, "?api-version=", "/listKeys?api-version=", 1), ""))
	var keys struct {
		PrimaryMasterKey string `json:"primaryMasterKey"`
	}
	if err := json.Unmarshal([]byte(keysOut), &keys); err != nil {
		t.Fatalf("decode listKeys output: %v: %s", err, keysOut)
	}
	if keys.PrimaryMasterKey == "" {
		t.Fatalf("listKeys served no primary key: %s", keysOut)
	}

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
	cosmosSignDataPlane(t, req, keys.PrimaryMasterKey)
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
	cosmosSignDataPlane(t, req, keys.PrimaryMasterKey)
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
