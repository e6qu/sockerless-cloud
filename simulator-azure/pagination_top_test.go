package main

import (
	"net/http/httptest"
	"testing"
)

// TestArmPage_TopAndSkipToken pins the $top / $skiptoken pagination contract the
// Cosmos / APIM / Key Vault list handlers now honor (previously they returned
// the full set regardless of $top).
func TestArmPage_TopAndSkipToken(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}

	page, next := armPage(httptest.NewRequest("GET", "/x?$top=2", nil), items)
	if len(page) != 2 || page[0] != 1 || next != "2" {
		t.Fatalf("$top=2 → page=%v next=%q", page, next)
	}

	page, next = armPage(httptest.NewRequest("GET", "/x?$top=2&$skiptoken=2", nil), items)
	if len(page) != 2 || page[0] != 3 || next != "4" {
		t.Fatalf("$skiptoken=2 → page=%v next=%q", page, next)
	}

	page, next = armPage(httptest.NewRequest("GET", "/x?$top=2&$skiptoken=4", nil), items)
	if len(page) != 1 || page[0] != 5 || next != "" {
		t.Fatalf("$skiptoken=4 (last page) → page=%v next=%q", page, next)
	}

	page, next = armPage(httptest.NewRequest("GET", "/x", nil), items)
	if len(page) != 5 || next != "" {
		t.Fatalf("no $top → all items, no next; got page=%v next=%q", page, next)
	}
}
