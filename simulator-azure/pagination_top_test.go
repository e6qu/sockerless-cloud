package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim/listq"
)

func intKey(n int) string { return listq.IntKey(int64(n)) }

func strKey(s string) string { return s }

func armPageAt(t *testing.T, query string, items []int) ([]int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	page, next, ok := armPage(rec, httptest.NewRequest("GET", "/x"+query, nil), items, intKey)
	if !ok {
		t.Fatalf("%s: answered %d %s", query, rec.Code, rec.Body.String())
	}
	return page, next
}

func TestArmPage_TopAndSkipToken(t *testing.T) {
	items := []int{5, 3, 1, 4, 2}

	var got [][]int
	query := "?$top=2"
	for {
		page, next := armPageAt(t, query, items)
		got = append(got, page)
		if next == "" {
			break
		}
		query = "?$top=2&$skiptoken=" + url.QueryEscape(next)
	}
	if want := [][]int{{1, 2}, {3, 4}, {5}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("$top=2 pages = %v, want %v", got, want)
	}
	page, next := armPageAt(t, "", items)
	if !slices.Equal(page, []int{1, 2, 3, 4, 5}) || next != "" {
		t.Fatalf("no $top → all items in key order, no next; got page=%v next=%q", page, next)
	}
}

func TestArmSeqPageKeepsBuildOrder(t *testing.T) {
	rec := httptest.NewRecorder()
	page, next, ok := armSeqPage(rec, httptest.NewRequest("GET", "/x?$top=2", nil), []string{"z", "a", "m"})
	if !ok || !slices.Equal(page, []string{"z", "a"}) || next == "" {
		t.Fatalf("first page = %v next %q ok %v", page, next, ok)
	}
	page, next, ok = armSeqPage(rec, httptest.NewRequest("GET", "/x?$top=2&$skiptoken="+url.QueryEscape(next), nil), []string{"z", "a", "m"})
	if !ok || !slices.Equal(page, []string{"m"}) || next != "" {
		t.Fatalf("second page = %v next %q ok %v", page, next, ok)
	}
}

// An offset, which an older token spelling carried, is refused too.
func TestAzurePageRejectsForeignSkipToken(t *testing.T) {
	for _, c := range []struct {
		page func(http.ResponseWriter, *http.Request, []int, func(int) string) ([]int, string, bool)
		code string
	}{
		{armPage[int], "BadRequest"},
		{kvPage[int], "BadParameter"},
	} {
		for _, token := range []string{"bogus", "2"} {
			rec := httptest.NewRecorder()
			if _, _, ok := c.page(rec, httptest.NewRequest("GET", "/x?$skiptoken="+token, nil), []int{1, 2, 3}, intKey); ok {
				t.Fatalf("%s: the foreign $skiptoken %q was accepted", c.code, token)
			}
			var body struct {
				Error struct{ Code string } `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("$skiptoken=%s: error body %q: %v", token, rec.Body.String(), err)
			}
			if rec.Code != http.StatusBadRequest || body.Error.Code != c.code {
				t.Errorf("$skiptoken=%s: got %d %s, want 400 %s", token, rec.Code, rec.Body.String(), c.code)
			}
		}
	}
}

func TestAzurePageCursorSurvivesChangesBeforeIt(t *testing.T) {
	for _, c := range []struct {
		name string
		page func(http.ResponseWriter, *http.Request, []string, func(string) string) ([]string, string, bool)
		size string
	}{
		{"armPage", armPage[string], "$top"},
		{"kvPage", kvPage[string], "maxresults"},
	} {
		at := func(items []string, token string) ([]string, string) {
			t.Helper()
			q := url.Values{c.size: {"2"}}
			if token != "" {
				q.Set("$skiptoken", token)
			}
			rec := httptest.NewRecorder()
			page, next, ok := c.page(rec, httptest.NewRequest("GET", "/x?"+q.Encode(), nil), items, strKey)
			if !ok {
				t.Fatalf("%s: answered %d %s", c.name, rec.Code, rec.Body.String())
			}
			return page, next
		}
		initial := []string{"j", "b", "h", "d", "f"}
		for _, change := range []struct {
			name  string
			after []string
		}{
			{"create", []string{"j", "b", "a", "h", "d", "c", "f"}},
			{"delete", []string{"j", "h", "d", "f"}},
			{"create and delete", []string{"j", "a", "h", "d", "c", "f"}},
		} {
			first, next := at(initial, "")
			if !slices.Equal(first, []string{"b", "d"}) || next == "" {
				t.Fatalf("%s: first page = %v next %q", c.name, first, next)
			}
			var rest []string
			for next != "" {
				var page []string
				page, next = at(change.after, next)
				rest = append(rest, page...)
			}
			if !slices.Equal(rest, []string{"f", "h", "j"}) {
				t.Errorf("%s, %s before the cursor: later pages = %v, want [f h j]", c.name, change.name, rest)
			}
		}
	}
}

func TestKvPageRefusesMaxResultsOutsideOneToTwentyFive(t *testing.T) {
	items := make([]int, 40)
	for i := range items {
		items[i] = i
	}
	for _, raw := range []string{"26", "100", "0", "-1", "ten"} {
		rec := httptest.NewRecorder()
		if page, _, ok := kvPage(rec, httptest.NewRequest("GET", "/secrets?maxresults="+raw, nil), items, intKey); ok {
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
		page, next, ok := kvPage(rec, httptest.NewRequest("GET", target, nil), items, intKey)
		if !ok {
			t.Fatalf("maxresults=%q refused: %d %s", raw, rec.Code, rec.Body.String())
		}
		if len(page) != want || page[0] != 0 || next == "" {
			t.Errorf("maxresults=%q: page of %d next %q, want %d and a next token", raw, len(page), next, want)
		}
	}
}
