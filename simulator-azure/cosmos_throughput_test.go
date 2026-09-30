package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
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

	// Updating the throughput of a resource that shares its database's is
	// refused as the service refuses it: there is no offer to update.
	status, out = moveARM(t, srv, http.MethodPut, base+"/sqlDatabases/shared/containers/c/throughputSettings/default"+ver,
		`{"properties":{"resource":{"throughput":800}}}`)
	if status != http.StatusNotFound {
		t.Fatalf("PUT throughput of a shared container: %d %s, want 404", status, out)
	}
	notFound(http.MethodGet, "/sqlDatabases/shared/containers/c/throughputSettings/default")
}

// One record holds a NoSQL resource's dedicated throughput: Azure Resource
// Manager's throughputSettings and the data plane's offer read and write the
// same one, whichever surface created it.
func TestCosmosThroughputOneRecordServesBothSurfaces(t *testing.T) {
	c := newCosmosItemsClient(t, "onethroughput")
	base := cosmosTestAccountPath(c.account)
	const ver = "?api-version=2024-08-15"

	armResource := func(path string) map[string]any {
		t.Helper()
		status, out := moveARM(t, c.srv, http.MethodGet, base+path+"/throughputSettings/default"+ver, "")
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
	offerFor := func(rid string) map[string]any {
		t.Helper()
		_, out := c.must(http.StatusOK, http.MethodPost, "/offers",
			`{"query":"SELECT * FROM c WHERE c.offerResourceId = '`+rid+`'"}`,
			map[string]string{"x-ms-documentdb-isquery": "true", "Content-Type": "application/query+json"})
		var feed struct {
			Offers []map[string]any `json:"Offers"`
		}
		if err := json.Unmarshal(out, &feed); err != nil {
			t.Fatalf("offers query: %v: %s", err, out)
		}
		if len(feed.Offers) != 1 {
			t.Fatalf("offers for %s: %s, want one", rid, out)
		}
		return feed.Offers[0]
	}

	if status, out := moveARM(t, c.srv, http.MethodPut, base+"/sqlDatabases/armdb"+ver,
		`{"properties":{"resource":{"id":"armdb"},"options":{"throughput":400}}}`); status != http.StatusOK {
		t.Fatalf("PUT armdb: %d %s", status, out)
	}
	offer := offerFor(c.account + "-armdb")
	content, _ := offer["content"].(map[string]any)
	if content["offerThroughput"] != float64(400) {
		t.Fatalf("offer of a database Azure Resource Manager provisioned: %v", offer)
	}
	armETag := armResource("/sqlDatabases/armdb")["_etag"]
	if armETag == nil || armETag != offer["_etag"] {
		t.Fatalf("ARM _etag %v and offer _etag %v must be the record's one ETag", armETag, offer["_etag"])
	}

	id, _ := offer["id"].(string)
	// azcosmos addresses an offer by its rid, and signs the lower-cased rid.
	replaceOffer := func(body, ifMatch string) int {
		t.Helper()
		request := cosmosDataPlaneHTTPRequest(t, http.MethodPut, "/offers/"+id+"/", c.account, body)
		date := time.Now().UTC().Format(http.TimeFormat)
		request.Header.Set("x-ms-date", date)
		request.Header.Set("If-Match", ifMatch)
		request.Header.Set("Authorization", cosmosAuthorizationHeader(t, c.key,
			cosmosStringToSign(http.MethodPut, "offers", strings.ToLower(id), date)))
		status, out := cosmosServe(c.srv, request)
		if status != http.StatusOK && status != http.StatusPreconditionFailed {
			t.Fatalf("PUT offer %s: %d %s", id, status, out)
		}
		return status
	}
	if status := replaceOffer(`{"content":{"offerThroughput":800},"offerType":"Invalid","offerVersion":"V2"}`, armETag.(string)); status != http.StatusOK {
		t.Fatalf("replacing the offer under its current ETag: %d", status)
	}
	if got := armResource("/sqlDatabases/armdb"); got["throughput"] != float64(800) || got["_etag"] == armETag {
		t.Fatalf("ARM throughput after the data plane replaced the offer: %v", got)
	}
	if status := replaceOffer(`{"content":{"offerThroughput":900}}`, armETag.(string)); status != http.StatusPreconditionFailed {
		t.Fatalf("replacing the offer under a stale ETag: %d, want 412", status)
	}

	c.must(http.StatusCreated, http.MethodPost, "/dbs/armdb/colls",
		`{"id":"dpcoll","partitionKey":{"paths":["/pk"],"kind":"Hash"}}`,
		map[string]string{"x-ms-cosmos-offer-autopilot-settings": `{"maxThroughput":5000}`})
	autoscale, _ := armResource("/sqlDatabases/armdb/containers/dpcoll")["autoscaleSettings"].(map[string]any)
	if autoscale["maxThroughput"] != float64(5000) {
		t.Fatalf("ARM throughput of a container the data plane provisioned: %v", autoscale)
	}
	if status, out := moveARM(t, c.srv, http.MethodPut, base+"/sqlDatabases/armdb/containers/dpcoll/throughputSettings/default"+ver,
		`{"properties":{"resource":{"throughput":1200}}}`); status != http.StatusOK {
		t.Fatalf("ARM PUT of a provisioned container's throughput: %d %s", status, out)
	}
	content, _ = offerFor(c.account + "-armdb-dpcoll")["content"].(map[string]any)
	if content["offerThroughput"] != float64(1200) || content["offerAutopilotSettings"] != nil {
		t.Fatalf("offer after Azure Resource Manager moved the container to manual throughput: %v", content)
	}

	c.must(http.StatusNoContent, http.MethodDelete, "/dbs/armdb/colls/dpcoll", "", nil)
	status, out := moveARM(t, c.srv, http.MethodGet, base+"/sqlDatabases/armdb/containers/dpcoll/throughputSettings/default"+ver, "")
	if status != http.StatusNotFound {
		t.Fatalf("throughput of a deleted container: %d %s", status, out)
	}
}
