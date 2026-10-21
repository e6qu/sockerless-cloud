package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A serverless cache migration opens with PrepareMigration, syncs from
// StartMigration, stops with StopMigration and restarts only with
// ClearTargetCacheData, and ends with CompleteMigration.
func TestElastiCacheServerlessMigrationLifecycle(t *testing.T) {
	ecServerlessCaches = sim.MakeStore[ECServerlessCache](nil, "elasticache_serverless_caches")
	const name = "migrating-cache"
	ecServerlessCaches.Put(name, ECServerlessCache{
		ServerlessCacheName: name, Status: "available", Engine: "valkey",
		EndpointAddress: name + ".serverless.example", EndpointPort: 6379,
	})
	call := func(handler http.HandlerFunc, form url.Values) (int, string) {
		t.Helper()
		form.Set("ServerlessCacheName", name)
		recorder := rdsFormCall(t, handler, form)
		return recorder.Code, recorder.Body.String()
	}
	wantCode := func(handler http.HandlerFunc, form url.Values, code string) {
		t.Helper()
		status, body := call(handler, form)
		if status == http.StatusOK || !strings.Contains(body, "<Code>"+code+"</Code>") {
			t.Fatalf("answered %d %s, want %s", status, body, code)
		}
	}
	wantOK := func(handler http.HandlerFunc, form url.Values) string {
		t.Helper()
		status, body := call(handler, form)
		if status != http.StatusOK {
			t.Fatalf("answered %d %s", status, body)
		}
		return body
	}

	wantCode(handleECStopMigration, url.Values{}, "ServerlessCacheNotUnderMigrationFault")
	wantCode(handleECStartMigration, url.Values{}, "ServerlessCacheNotUnderMigrationFault")
	wantCode(handleECPrepareMigration, url.Values{
		"CustomerNodeEndpointList.member.1.Address": {"10.0.0.5"}, "CustomerNodeEndpointList.member.2.Address": {"10.0.0.6"},
	}, "InvalidParameterValue")
	source := url.Values{"CustomerNodeEndpointList.member.1.Address": {"10.0.0.5"}, "CustomerNodeEndpointList.member.1.Port": {"6379"}}
	prepared := wantOK(handleECPrepareMigration, source)
	if !strings.Contains(prepared, "<Migration><MigrationEndpoint><Address>"+name+".serverless.example</Address><Port>6379</Port></MigrationEndpoint></Migration>") {
		t.Fatalf("PrepareMigration answered %s, want the cache's endpoint as the migration endpoint", prepared)
	}
	wantCode(handleECPrepareMigration, source, "ServerlessCacheAlreadyUnderMigrationFault")
	wantCode(handleECStopMigration, url.Values{}, "InvalidServerlessCacheStateFault")
	wantCode(handleECStartMigration, url.Values{"ReplicationGroupId": {"rg"}}, "InvalidParameterCombination")

	wantOK(handleECStartMigration, url.Values{})
	wantCode(handleECStartMigration, url.Values{}, "InvalidServerlessCacheStateFault")
	wantOK(handleECStopMigration, url.Values{})
	wantCode(handleECStartMigration, url.Values{}, "InvalidParameterValue")
	wantCode(handleECCompleteMigration, url.Values{}, "MigrationHealthFault")
	wantOK(handleECStartMigration, url.Values{"ClearTargetCacheData": {"true"}})
	completed := wantOK(handleECCompleteMigration, url.Values{})
	if strings.Contains(completed, "<Migration>") {
		t.Fatalf("CompleteMigration left the migration open: %s", completed)
	}
	wantCode(handleECCompleteMigration, url.Values{}, "ServerlessCacheNotUnderMigrationFault")
}
