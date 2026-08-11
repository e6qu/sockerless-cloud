package main

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

type odItem struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Tier  int    `json:"tier"`
}

func TestAzureODataFilter(t *testing.T) {
	items := []odItem{
		{Name: "alpha", State: "ACTIVE", Tier: 1},
		{Name: "beta", State: "STOPPED", Tier: 3},
		{Name: "alefel", State: "ACTIVE", Tier: 2},
	}
	apply := func(filter string) []odItem {
		q := url.Values{"$filter": {filter}}
		r := httptest.NewRequest("GET", "/x?"+q.Encode(), nil)
		got, err := azureApplyListQuery(items, r)
		if err != nil {
			t.Fatalf("$filter=%q returned unexpected error: %v", filter, err)
		}
		return got
	}

	cases := []struct {
		filter string
		want   int
	}{
		{"name eq 'alpha'", 1},
		{"name ne 'alpha'", 2},
		{"state eq 'ACTIVE'", 2},
		{"tier ge 2", 2},
		{"tier gt 1 and state eq 'ACTIVE'", 1},
		{"name eq 'alpha' or name eq 'beta'", 2},
		{"startswith(name, 'al')", 2},
		{"endswith(name, 'a')", 2}, // alpha, beta
		{"contains(name, 'lef')", 1},
		{"not (state eq 'ACTIVE')", 1},
		{"substringof('eta', name)", 1},
	}
	for _, tc := range cases {
		if got := apply(tc.filter); len(got) != tc.want {
			t.Errorf("$filter=%q → %d items, want %d", tc.filter, len(got), tc.want)
		}
	}

	// A valid filter that simply matches nothing returns zero items, not an error.
	if got := apply("name eq 'nonexistent'"); len(got) != 0 {
		t.Errorf("$filter matching nothing → %d items, want 0", len(got))
	}

	// $orderby
	q := url.Values{"$orderby": {"tier desc"}}
	r := httptest.NewRequest("GET", "/x?"+q.Encode(), nil)
	got, err := azureApplyListQuery(items, r)
	if err != nil {
		t.Fatalf("$orderby returned unexpected error: %v", err)
	}
	if got[0].Tier != 3 || got[2].Tier != 1 {
		t.Errorf("$orderby=tier desc → %v", got)
	}
}

// TestAzureODataFilterMalformed: a malformed $filter is a client error — real
// Azure rejects it (HTTP 400) rather than matching every item. azureApplyListQuery
// must return an error so the list handlers surface BadRequest.
func TestAzureODataFilterMalformed(t *testing.T) {
	items := []odItem{
		{Name: "alpha", State: "ACTIVE", Tier: 1},
		{Name: "beta", State: "STOPPED", Tier: 3},
	}
	bad := []string{
		"name eq",             // missing value
		"name",                // missing operator + value
		"name foo 'x'",        // unknown operator
		"startswith(name",     // truncated function
		"((name eq 'alpha'",   // unbalanced parens
		"contains('x')",       // missing function field arg
		"and name eq 'alpha'", // leading binary operator
	}
	for _, f := range bad {
		q := url.Values{"$filter": {f}}
		r := httptest.NewRequest("GET", "/x?"+q.Encode(), nil)
		got, err := azureApplyListQuery(items, r)
		if err == nil {
			t.Errorf("$filter=%q: want parse error, got %d items (matched all without failing)", f, len(got))
		}
	}
}
