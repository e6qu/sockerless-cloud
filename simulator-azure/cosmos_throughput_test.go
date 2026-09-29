package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

// A Cosmos DB resource created without dedicated throughput has no offer: the
// resource provider answers its throughputSettings/default read, and each
// migration of it, with 404 NotFound, and never invents one.
func TestCosmosThroughputOfAResourceWithoutAnOfferIsNotFound(t *testing.T) {
	srv := newMoveTestServer(t)
	account := "nooffercosmos"
	cosmosProvisionAccount(t, srv, account)
	base := cosmosTestAccountPath(account)
	const ver = "?api-version=2024-08-15"

	for _, c := range []struct{ path, body string }{
		{"/sqlDatabases/shared", `{"properties":{"resource":{"id":"shared"}}}`},
		{"/sqlDatabases/shared/containers/c", `{"properties":{"resource":{"id":"c","partitionKey":{"paths":["/pk"],"kind":"Hash"}}}}`},
		{"/sqlDatabases/shared/containers/dedicated", `{"properties":{"resource":{"id":"dedicated","partitionKey":{"paths":["/pk"],"kind":"Hash"}},"options":{"throughput":600}}}`},
		{"/tables/plain", `{"properties":{"resource":{"id":"plain"}}}`},
		{"/tables/autoscaled", `{"properties":{"resource":{"id":"autoscaled"},"options":{"autoscaleSettings":{"maxThroughput":4000}}}}`},
		{"/mongodbDatabases/mongo", `{"properties":{"resource":{"id":"mongo"}}}`},
	} {
		if status, out := moveARM(t, srv, http.MethodPut, base+c.path+ver, c.body); status != http.StatusOK {
			t.Fatalf("PUT %s: %d %s", c.path, status, out)
		}
	}

	notFound := func(method, path string) {
		t.Helper()
		status, out := moveARM(t, srv, method, base+path+ver, "")
		var body struct {
			Error struct{ Code string } `json:"error"`
		}
		if err := json.Unmarshal(out, &body); err != nil {
			t.Fatalf("%s %s: %d %s: %v", method, path, status, out, err)
		}
		if status != http.StatusNotFound || body.Error.Code != "NotFound" {
			t.Fatalf("%s %s: got %d %s, want 404 NotFound", method, path, status, out)
		}
	}
	for _, owner := range []string{"/sqlDatabases/shared", "/sqlDatabases/shared/containers/c", "/tables/plain", "/mongodbDatabases/mongo"} {
		notFound(http.MethodGet, owner+"/throughputSettings/default")
		notFound(http.MethodPost, owner+"/throughputSettings/default/migrateToAutoscale")
		notFound(http.MethodPost, owner+"/throughputSettings/default/migrateToManualThroughput")
		notFound(http.MethodGet, owner+"/throughputSettings/default")
	}

	read := func(path string) map[string]any {
		t.Helper()
		status, out := moveARM(t, srv, http.MethodGet, base+path+"/throughputSettings/default"+ver, "")
		if status != http.StatusOK {
			t.Fatalf("GET %s throughput: %d %s", path, status, out)
		}
		var got struct {
			Properties struct {
				Resource map[string]any `json:"resource"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("GET %s throughput: %v: %s", path, err, out)
		}
		return got.Properties.Resource
	}
	if got := read("/sqlDatabases/shared/containers/dedicated"); got["throughput"] != float64(600) {
		t.Fatalf("dedicated container throughput: %v", got)
	}
	autoscale, _ := read("/tables/autoscaled")["autoscaleSettings"].(map[string]any)
	if autoscale["maxThroughput"] != float64(4000) {
		t.Fatalf("autoscaled table throughput: %v", autoscale)
	}

	status, out := moveARM(t, srv, http.MethodPut, base+"/tables/plain/throughputSettings/default"+ver, `{"properties":{}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("PUT throughput without properties.resource: %d %s", status, out)
	}
	notFound(http.MethodGet, "/tables/plain/throughputSettings/default")
}
