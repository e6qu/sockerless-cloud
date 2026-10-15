package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type odItem struct {
	Name  string   `json:"name"`
	State string   `json:"state"`
	Tier  int      `json:"tier"`
	Tags  []string `json:"tags,omitempty"`
	Ports []odPort `json:"ports,omitempty"`
}

type odPort struct {
	Number int `json:"number"`
}

func TestAzureODataFilter(t *testing.T) {
	items := []odItem{
		{Name: "alpha", State: "ACTIVE", Tier: 1, Tags: []string{"web", "prod"}, Ports: []odPort{{80}, {443}}},
		{Name: "beta", State: "STOPPED", Tier: 3, Tags: []string{"batch"}, Ports: []odPort{{443}}},
		{Name: "alefel", State: "ACTIVE", Tier: 2},
	}
	apply := func(filter string) []odItem {
		q := url.Values{"$filter": {filter}}
		r := httptest.NewRequest("GET", "/x?"+q.Encode(), nil)
		got, err := odApply(items, r)
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
		{"tags/any(t:t eq 'prod')", 1},
		{"tags/any(t:t ne 'batch')", 1},
		{"ports/any(p:p/number eq 443)", 2},
		{"ports/all(p:p/number eq 443)", 2}, // beta, and alefel with no ports
		{"state eq 'ACTIVE' and tags/any(t:t eq 'web')", 1},
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
	got, err := odApply(items, r)
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
		"name eq",              // missing value
		"name",                 // missing operator + value
		"name foo 'x'",         // unknown operator
		"startswith(name",      // truncated function
		"((name eq 'alpha'",    // unbalanced parens
		"contains('x')",        // missing function field arg
		"and name eq 'alpha'",  // leading binary operator
		"tags/any(t eq 'x')",   // lambda without a variable
		"tags/any(t:u eq 'x')", // lambda expression not using its variable
	}
	for _, f := range bad {
		q := url.Values{"$filter": {f}}
		r := httptest.NewRequest("GET", "/x?"+q.Encode(), nil)
		got, err := odApply(items, r)
		if err == nil {
			t.Errorf("$filter=%q: want parse error, got %d items (matched all without failing)", f, len(got))
		}
	}
}

func odApply(items []odItem, r *http.Request) ([]odItem, error) {
	keyed, err := azureApplyListQuery(items, r, func(it odItem) string { return it.Name })
	if err != nil {
		return nil, err
	}
	out := make([]odItem, len(keyed))
	for i, k := range keyed {
		out[i] = k.item
	}
	return out, nil
}

// The paging key keeps the order $orderby asks for: strings by byte order
// with a prefix ahead of the longer name it starts, numbers numerically, a
// descending term reversed, and ties broken by id.
func TestAzureListQueryKeyKeepsOrderBy(t *testing.T) {
	items := []odItem{
		{Name: "alpha", State: "ACTIVE", Tier: 10},
		{Name: "al", State: "ACTIVE", Tier: 9},
		{Name: "beta", State: "STOPPED", Tier: 10},
		{Name: "gamma", State: "ACTIVE", Tier: 10},
	}
	for orderby, want := range map[string][]string{
		"name":                  {"al", "alpha", "beta", "gamma"},
		"name desc":             {"gamma", "beta", "alpha", "al"},
		"tier":                  {"al", "alpha", "beta", "gamma"},
		"tier desc":             {"alpha", "beta", "gamma", "al"},
		"state desc, tier desc": {"beta", "alpha", "gamma", "al"},
	} {
		q := url.Values{"$orderby": {orderby}}
		got, err := odApply(items, httptest.NewRequest("GET", "/x?"+q.Encode(), nil))
		if err != nil {
			t.Fatalf("$orderby=%s: %v", orderby, err)
		}
		names := make([]string, len(got))
		for i, it := range got {
			names[i] = it.Name
		}
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Errorf("$orderby=%s → %v, want %v", orderby, names, want)
		}
	}
}
