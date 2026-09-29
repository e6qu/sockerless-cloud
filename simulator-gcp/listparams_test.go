package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func lpApply(t *testing.T, apply func(http.ResponseWriter, *http.Request, []lpItem) ([]lpItem, bool), items []lpItem, r *http.Request) []lpItem {
	t.Helper()
	rec := httptest.NewRecorder()
	got, ok := apply(rec, r, items)
	if !ok {
		t.Fatalf("%s: answered %d %s", r.URL.RawQuery, rec.Code, rec.Body.String())
	}
	return got
}

// Google Cloud answers a filter or orderBy its grammar does not admit with
// 400 INVALID_ARGUMENT rather than listing everything.
func TestGCPApplyListParams_MalformedIsInvalidArgument(t *testing.T) {
	items := []lpItem{{Name: "a"}, {Name: "b"}}
	for _, params := range []map[string]string{
		{"filter": `name="a`},
		{"filter": `(name="a"`},
		{"filter": `name="a")`},
		{"filter": `name =`},
		{"filter": `name ! "a"`},
		{"filter": `AND`},
		{"filter": `NOT`},
		{"filter": strings.Repeat("(", maxFilterParseDepth+1) + `name="a"` + strings.Repeat(")", maxFilterParseDepth+1)},
		{"filter": strings.Repeat("-", 200_000) + `name="a"`},
		{"orderBy": "name sideways"},
		{"orderBy": "name desc extra"},
	} {
		rec := httptest.NewRecorder()
		if _, ok := gcpApplyListParams(rec, lpReq(params), items); ok {
			t.Errorf("%.60v: accepted", params)
			continue
		}
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "INVALID_ARGUMENT") {
			t.Errorf("%.60v: got %d %.200s, want 400 INVALID_ARGUMENT", params, rec.Code, rec.Body.String())
		}
	}
}

func TestGCPApplyListParams_OrderByTermsAndNumbers(t *testing.T) {
	type sized struct {
		Name string `json:"name"`
		Size int    `json:"size"`
	}
	items := []sized{{"a", 9}, {"b", 10}, {"c", 9}}
	rec := httptest.NewRecorder()
	got, ok := gcpApplyListParams(rec, lpReq(map[string]string{"orderBy": "size desc, name desc"}), items)
	if !ok {
		t.Fatalf("answered %d %s", rec.Code, rec.Body.String())
	}
	if got[0].Name != "b" || got[1].Name != "c" || got[2].Name != "a" {
		t.Fatalf("size numerically descending, then name descending → %v", got)
	}
}

type lpItem struct {
	Name   string            `json:"name"`
	State  string            `json:"state"`
	Labels map[string]string `json:"labels"`
}

func lpReq(params map[string]string) *http.Request {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	return httptest.NewRequest("GET", "/x?"+q.Encode(), nil)
}

func TestGCPApplyListParams_Filter(t *testing.T) {
	items := []lpItem{
		{Name: "a", State: "ACTIVE", Labels: map[string]string{"env": "prod"}},
		{Name: "b", State: "STOPPED", Labels: map[string]string{"env": "dev"}},
		{Name: "c", State: "ACTIVE", Labels: map[string]string{"env": "prod"}},
	}

	got := lpApply(t, gcpApplyListParams[lpItem], items, lpReq(map[string]string{"filter": `state="ACTIVE"`}))
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("state=ACTIVE → %v", got)
	}

	got = lpApply(t, gcpApplyListParams[lpItem], items, lpReq(map[string]string{"filter": "labels.env:dev"}))
	if len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("labels.env:dev → %v", got)
	}

	got = lpApply(t, gcpApplyListParams[lpItem], items, lpReq(map[string]string{"filter": `state="ACTIVE" AND name!="a"`}))
	if len(got) != 1 || got[0].Name != "c" {
		t.Fatalf("ACTIVE AND name!=a → %v", got)
	}

	// Full grammar: OR.
	got = lpApply(t, gcpApplyListParams[lpItem], items, lpReq(map[string]string{"filter": `name="a" OR name="b"`}))
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("name=a OR name=b → %v", got)
	}

	// NOT.
	got = lpApply(t, gcpApplyListParams[lpItem], items, lpReq(map[string]string{"filter": `NOT state="ACTIVE"`}))
	if len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("NOT state=ACTIVE → %v", got)
	}

	// Parentheses + precedence: (a OR b) AND prod-env.
	got = lpApply(t, gcpApplyListParams[lpItem], items, lpReq(map[string]string{"filter": `(name="a" OR name="b") AND labels.env="prod"`}))
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("(a OR b) AND env=prod → %v", got)
	}

	// Implicit AND (adjacency).
	got = lpApply(t, gcpApplyListParams[lpItem], items, lpReq(map[string]string{"filter": `state="ACTIVE" labels.env="prod"`}))
	if len(got) != 2 {
		t.Fatalf("implicit-AND → %v", got)
	}

	// Has-operator wildcard (labels.env present).
	got = lpApply(t, gcpApplyListParams[lpItem], items, lpReq(map[string]string{"filter": `labels.env:*`}))
	if len(got) != 3 {
		t.Fatalf("labels.env:* → %v", got)
	}
}

func TestGCPApplyListParams_OrderBy(t *testing.T) {
	items := []lpItem{{Name: "a"}, {Name: "c"}, {Name: "b"}}

	got := lpApply(t, gcpApplyListParams[lpItem], items, lpReq(map[string]string{"orderBy": "name desc"}))
	if got[0].Name != "c" || got[1].Name != "b" || got[2].Name != "a" {
		t.Fatalf("orderBy name desc → %v", got)
	}

	got = lpApply(t, gcpApplyListParams[lpItem], items, lpReq(map[string]string{"orderBy": "name asc"}))
	if got[0].Name != "a" || got[2].Name != "c" {
		t.Fatalf("orderBy name asc → %v", got)
	}

	got = lpApply(t, gcpApplyListParams[lpItem], items, httptest.NewRequest("GET", "/x", nil))
	if len(got) != 3 || got[0].Name != "a" {
		t.Fatalf("no params must be a no-op → %v", got)
	}
}
