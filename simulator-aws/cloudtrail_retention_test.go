package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func useCloudTrailMemoryStores(t *testing.T) {
	t.Helper()
	AwaitSimulatorBackground()
	events, lake, stores, trails, queries := cloudTrailEvents, cloudTrailLakeEvents, cloudTrailEventDataStores, cloudTrailTrails, cloudTrailQueries
	cloudTrailEvents = sim.MakeStore[CloudTrailEvent](nil, "cloudtrail_events")
	cloudTrailLakeEvents = sim.MakeStore[CloudTrailLakeEvent](nil, "cloudtrail_lake_events")
	cloudTrailEventDataStores = sim.MakeStore[CloudTrailEventDataStore](nil, "cloudtrail_event_data_stores")
	cloudTrailTrails = sim.MakeStore[CloudTrailTrail](nil, "cloudtrail_trails")
	cloudTrailQueries = sim.MakeStore[CloudTrailQuery](nil, "cloudtrail_queries")
	t.Cleanup(func() {
		cloudTrailEvents, cloudTrailLakeEvents, cloudTrailEventDataStores, cloudTrailTrails, cloudTrailQueries = events, lake, stores, trails, queries
	})
}

func ageEvent(t *testing.T, eventID string, by time.Duration) {
	t.Helper()
	if !cloudTrailEvents.Update(eventID, func(ev *CloudTrailEvent) {
		ev.EventTime = time.Now().UTC().Add(-by).Format(time.RFC3339)
	}) {
		t.Fatalf("event %s is not in history", eventID)
	}
}

func historyIDs() map[string]bool {
	ids := map[string]bool{}
	for _, ev := range cloudTrailEvents.List() {
		ids[ev.EventName] = true
	}
	return ids
}

func TestEventHistoryKeepsNinetyDays(t *testing.T) {
	useCloudTrailMemoryStores(t)
	cloudTrailRecord(CloudTrailEvent{EventName: "Recent"})
	cloudTrailRecord(CloudTrailEvent{EventName: "Old"})
	for _, ev := range cloudTrailEvents.List() {
		if ev.EventName == "Old" {
			ageEvent(t, ev.EventId, cloudTrailHistoryRetention+time.Hour)
		}
	}

	now := time.Now()
	var old CloudTrailEvent
	for _, ev := range cloudTrailEvents.List() {
		if ev.EventName == "Old" {
			old = ev
		}
	}
	if cloudTrailInHistory(old, now) {
		t.Fatal("an event older than 90 days is still in event history")
	}
	if swept := cloudTrailSweepHistory(now); swept != 1 {
		t.Fatalf("history sweep deleted %d events, want 1", swept)
	}
	if ids := historyIDs(); !ids["Recent"] || ids["Old"] {
		t.Fatalf("history holds %v, want only Recent", ids)
	}
}

// An event data store holds what it ingested while enabled, from its creation
// on, and only what its selectors take.
func TestEventDataStoresIngestTheirOwnCopies(t *testing.T) {
	useCloudTrailMemoryStores(t)
	cloudTrailRecord(CloudTrailEvent{EventName: "BeforeAnyStore"})

	put := func(name, status string, selectors []map[string]any) string {
		eds := CloudTrailEventDataStore{ARN: cloudTrailEDSARN(name), Name: name, Status: status, RetentionPeriod: 7, AdvancedEventSelectors: selectors}
		cloudTrailEventDataStores.Put(eds.ARN, eds)
		return eds.ARN
	}
	all := put("all", "ENABLED", nil)
	stopped := put("stopped", "STOPPED_INGESTION", nil)
	writes := put("writes", "ENABLED", []map[string]any{{
		"FieldSelectors": []any{
			map[string]any{"Field": "eventCategory", "Equals": []any{"Management"}},
			map[string]any{"Field": "readOnly", "Equals": []any{"false"}},
		},
	}})
	dataOnly := put("data", "ENABLED", []map[string]any{{
		"FieldSelectors": []any{map[string]any{"Field": "eventCategory", "Equals": []any{"Data"}}},
	}})

	cloudTrailRecord(CloudTrailEvent{EventName: "CreateCluster", ReadOnly: false})
	cloudTrailRecord(CloudTrailEvent{EventName: "DescribeClusters", ReadOnly: true})

	names := func(store string) []string {
		rows, _, _ := cloudTrailRunQuery(store, "SELECT eventName FROM "+store)
		var out []string
		for _, row := range rows {
			out = append(out, row["eventName"])
		}
		return out
	}
	for store, want := range map[string]string{
		all:      "CreateCluster,DescribeClusters",
		stopped:  "",
		writes:   "CreateCluster",
		dataOnly: "",
	} {
		if got := strings.Join(names(store), ","); got != want {
			t.Errorf("%s ingested %q, want %q", store, got, want)
		}
	}
}

func TestLakeSweepKeepsEachStoresRetention(t *testing.T) {
	useCloudTrailMemoryStores(t)
	short := CloudTrailEventDataStore{ARN: cloudTrailEDSARN("short"), Status: "ENABLED", RetentionPeriod: 7}
	long := CloudTrailEventDataStore{ARN: cloudTrailEDSARN("long"), Status: "ENABLED", RetentionPeriod: 3653}
	cloudTrailEventDataStores.Put(short.ARN, short)
	cloudTrailEventDataStores.Put(long.ARN, long)
	tenDaysAgo := CloudTrailEvent{EventId: "e1", EventTime: time.Now().UTC().Add(-10 * 24 * time.Hour).Format(time.RFC3339)}
	for _, store := range []string{short.ARN, long.ARN, cloudTrailEDSARN("gone")} {
		cloudTrailLakeEvents.Put(store+"|e1", CloudTrailLakeEvent{EventDataStore: store, Event: tenDaysAgo})
	}

	if swept := cloudTrailSweepLake(time.Now()); swept != 2 {
		t.Fatalf("lake sweep deleted %d events, want 2 (the short store's and the missing store's)", swept)
	}
	if _, ok := cloudTrailLakeEvents.Get(long.ARN + "|e1"); !ok {
		t.Error("an event inside the long store's retention was deleted")
	}
}

func TestStartQueryNamesItsEventDataStore(t *testing.T) {
	useCloudTrailMemoryStores(t)
	eds := CloudTrailEventDataStore{ARN: cloudTrailEDSARN("named"), Status: "ENABLED", RetentionPeriod: 7}
	cloudTrailEventDataStores.Put(eds.ARN, eds)

	for statement, want := range map[string]string{
		"SELECT eventName FROM " + cloudTrailEDSARN("missing"): "EventDataStoreNotFoundException",
		"SELECT eventName":                    "InvalidQueryStatementException",
		"SELECT eventName FROM named LIMIT 5": "",
	} {
		rec := httptest.NewRecorder()
		body := `{"QueryStatement":` + strconv.Quote(statement) + `}`
		handleCloudTrailStartQuery(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		if want == "" {
			if rec.Code != http.StatusOK {
				t.Errorf("%q: status %d, body %s", statement, rec.Code, rec.Body.String())
			}
			continue
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%q: body %s, want %s", statement, rec.Body.String(), want)
		}
	}
}

func TestCloudTrailSequenceOutrunsAnEarlierProcess(t *testing.T) {
	first := cloudTrailNextSeq()
	if second := cloudTrailNextSeq(); second <= first {
		t.Fatalf("sequence went from %d to %d", first, second)
	}
	if first < time.Now().Add(-time.Minute).UnixNano() {
		t.Fatalf("sequence %d sits below the clock, so a restart could reuse it", first)
	}
}
