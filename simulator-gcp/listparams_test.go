package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

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

	got := gcpApplyListParams(items, lpReq(map[string]string{"filter": `state="ACTIVE"`}))
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("state=ACTIVE → %v", got)
	}

	got = gcpApplyListParams(items, lpReq(map[string]string{"filter": "labels.env:dev"}))
	if len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("labels.env:dev → %v", got)
	}

	got = gcpApplyListParams(items, lpReq(map[string]string{"filter": `state="ACTIVE" AND name!="a"`}))
	if len(got) != 1 || got[0].Name != "c" {
		t.Fatalf("ACTIVE AND name!=a → %v", got)
	}

	// Full grammar: OR.
	got = gcpApplyListParams(items, lpReq(map[string]string{"filter": `name="a" OR name="b"`}))
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("name=a OR name=b → %v", got)
	}

	// NOT.
	got = gcpApplyListParams(items, lpReq(map[string]string{"filter": `NOT state="ACTIVE"`}))
	if len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("NOT state=ACTIVE → %v", got)
	}

	// Parentheses + precedence: (a OR b) AND prod-env.
	got = gcpApplyListParams(items, lpReq(map[string]string{"filter": `(name="a" OR name="b") AND labels.env="prod"`}))
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("(a OR b) AND env=prod → %v", got)
	}

	// Implicit AND (adjacency).
	got = gcpApplyListParams(items, lpReq(map[string]string{"filter": `state="ACTIVE" labels.env="prod"`}))
	if len(got) != 2 {
		t.Fatalf("implicit-AND → %v", got)
	}

	// Has-operator wildcard (labels.env present).
	got = gcpApplyListParams(items, lpReq(map[string]string{"filter": `labels.env:*`}))
	if len(got) != 3 {
		t.Fatalf("labels.env:* → %v", got)
	}
}

func TestGCPApplyListParams_OrderBy(t *testing.T) {
	items := []lpItem{{Name: "a"}, {Name: "c"}, {Name: "b"}}

	got := gcpApplyListParams(items, lpReq(map[string]string{"orderBy": "name desc"}))
	if got[0].Name != "c" || got[1].Name != "b" || got[2].Name != "a" {
		t.Fatalf("orderBy name desc → %v", got)
	}

	got = gcpApplyOrderBy(items, lpReq(map[string]string{"orderBy": "name"}))
	if got[0].Name != "a" || got[2].Name != "c" {
		t.Fatalf("orderBy name asc → %v", got)
	}

	got = gcpApplyListParams(items, httptest.NewRequest("GET", "/x", nil))
	if len(got) != 3 || got[0].Name != "a" {
		t.Fatalf("no params must be a no-op → %v", got)
	}
}
