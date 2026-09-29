package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEBListBySourceFiltersByStateAndRefusesABadBody(t *testing.T) {
	archives := []EBArchive{
		{ArchiveName: "b-archive", EventSourceArn: "arn:bus", State: "ENABLED"},
		{ArchiveName: "a-archive", EventSourceArn: "arn:bus", State: "DISABLED"},
		{ArchiveName: "c-archive", EventSourceArn: "arn:other", State: "ENABLED"},
	}
	fields := func(a EBArchive) (string, string, string) { return a.ArchiveName, a.EventSourceArn, a.State }
	list := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		ebListBySource(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), "Archives", archives, fields, ebArchiveSummary)
		return rec
	}

	rec := list(`{"EventSourceArn":"arn:bus","State":"ENABLED"}`)
	var out struct {
		Archives []struct{ ArchiveName string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("ListArchives body: %v: %s", err, rec.Body.String())
	}
	if rec.Code != http.StatusOK || len(out.Archives) != 1 || out.Archives[0].ArchiveName != "b-archive" {
		t.Fatalf("State and source filter: %d %s", rec.Code, rec.Body.String())
	}

	if err := json.Unmarshal(list(`{}`).Body.Bytes(), &out); err != nil || len(out.Archives) != 3 || out.Archives[0].ArchiveName != "a-archive" {
		t.Fatalf("an unfiltered listing is every archive by name: %v %+v", err, out.Archives)
	}

	if rec := list(`{"State":`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ValidationException") {
		t.Fatalf("an unreadable body: %d %s", rec.Code, rec.Body.String())
	}
}
