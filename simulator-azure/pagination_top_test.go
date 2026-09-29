package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func armPageAt(t *testing.T, query string, items []int) ([]int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	page, next, ok := armPage(rec, httptest.NewRequest("GET", "/x"+query, nil), items)
	if !ok {
		t.Fatalf("%s: answered %d %s", query, rec.Code, rec.Body.String())
	}
	return page, next
}

// TestArmPage_TopAndSkipToken pins the $top / $skiptoken pagination contract.
func TestArmPage_TopAndSkipToken(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}

	page, next := armPageAt(t, "?$top=2", items)
	if len(page) != 2 || page[0] != 1 || next != "2" {
		t.Fatalf("$top=2 → page=%v next=%q", page, next)
	}
	page, next = armPageAt(t, "?$top=2&$skiptoken=2", items)
	if len(page) != 2 || page[0] != 3 || next != "4" {
		t.Fatalf("$skiptoken=2 → page=%v next=%q", page, next)
	}
	page, next = armPageAt(t, "?$top=2&$skiptoken=4", items)
	if len(page) != 1 || page[0] != 5 || next != "" {
		t.Fatalf("$skiptoken=4 (last page) → page=%v next=%q", page, next)
	}
	page, next = armPageAt(t, "", items)
	if len(page) != 5 || next != "" {
		t.Fatalf("no $top → all items, no next; got page=%v next=%q", page, next)
	}
}

// A $skiptoken the list never issued is a client error, not the first page.
func TestAzurePageRejectsForeignSkipToken(t *testing.T) {
	for _, c := range []struct {
		page func(http.ResponseWriter, *http.Request, []int) ([]int, string, bool)
		code string
	}{
		{armPage[int], "BadRequest"},
		{kvPage[int], "BadParameter"},
	} {
		rec := httptest.NewRecorder()
		if _, _, ok := c.page(rec, httptest.NewRequest("GET", "/x?$skiptoken=bogus", nil), []int{1, 2}); ok {
			t.Fatalf("%s: a foreign $skiptoken was accepted", c.code)
		}
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.code) {
			t.Errorf("got %d %s, want 400 %s", rec.Code, rec.Body.String(), c.code)
		}
	}
}

// Key Vault declares maxresults from 1 to 25 on every data-plane list, and
// refuses a page size outside that range instead of serving it.
func TestKvPageRefusesMaxResultsOutsideOneToTwentyFive(t *testing.T) {
	items := make([]int, 40)
	for i := range items {
		items[i] = i
	}
	for _, raw := range []string{"26", "100", "0", "-1", "ten"} {
		rec := httptest.NewRecorder()
		if page, _, ok := kvPage(rec, httptest.NewRequest("GET", "/secrets?maxresults="+raw, nil), items); ok {
			t.Fatalf("maxresults=%s was served as a page of %d", raw, len(page))
		}
		var body struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("maxresults=%s: error body %q: %v", raw, rec.Body.String(), err)
		}
		if rec.Code != http.StatusBadRequest || body.Error.Code != "BadParameter" || !strings.Contains(body.Error.Message, "maxresults") {
			t.Errorf("maxresults=%s: got %d %+v, want 400 BadParameter naming maxresults", raw, rec.Code, body.Error)
		}
	}
	for raw, want := range map[string]int{"1": 1, "25": 25, "": 25} {
		target := "/secrets"
		if raw != "" {
			target += "?maxresults=" + raw
		}
		rec := httptest.NewRecorder()
		page, next, ok := kvPage(rec, httptest.NewRequest("GET", target, nil), items)
		if !ok {
			t.Fatalf("maxresults=%q refused: %d %s", raw, rec.Code, rec.Body.String())
		}
		if len(page) != want || page[0] != 0 || next != strconv.Itoa(want) {
			t.Errorf("maxresults=%q: page of %d next %q, want %d next %q", raw, len(page), next, want, strconv.Itoa(want))
		}
	}
}
