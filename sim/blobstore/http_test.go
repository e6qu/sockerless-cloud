package blobstore

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEvaluateHTTPFollowsRFC9110Order(t *testing.T) {
	modified := time.Date(2026, 9, 1, 12, 0, 0, 500_000_000, time.UTC)
	before := modified.Add(-time.Hour).Format(http.TimeFormat)
	at := modified.Format(http.TimeFormat)
	existing := Validators{ETag: `"abc"`, Modified: modified, Exists: true}
	cases := []struct {
		name    string
		headers map[string]string
		v       Validators
		access  Access
		want    Outcome
	}{
		{"no conditions", nil, existing, Read, Proceed},
		{"If-Match names it", map[string]string{"If-Match": `"x", abc`}, existing, Read, Proceed},
		{"If-Match names another", map[string]string{"If-Match": `"x"`}, existing, Read, PreconditionFailed},
		{"If-Match * on nothing", map[string]string{"If-Match": "*"}, Validators{}, Create, PreconditionFailed},
		{"If-Match wins over If-Unmodified-Since", map[string]string{"If-Match": `"abc"`, "If-Unmodified-Since": before}, existing, Read, Proceed},
		{"If-Unmodified-Since before the change", map[string]string{"If-Unmodified-Since": before}, existing, Modify, PreconditionFailed},
		// The half second the resource carries is below an HTTP date's precision.
		{"If-Unmodified-Since at the change", map[string]string{"If-Unmodified-Since": at}, existing, Modify, Proceed},
		{"If-None-Match names it on a read", map[string]string{"If-None-Match": `"abc"`}, existing, Read, NotModified},
		{"If-None-Match names it on a write", map[string]string{"If-None-Match": `"abc"`}, existing, Modify, PreconditionFailed},
		{"If-None-Match * on a create", map[string]string{"If-None-Match": "*"}, existing, Create, AlreadyExists},
		{"If-None-Match * on nothing", map[string]string{"If-None-Match": "*"}, Validators{}, Create, Proceed},
		{"If-None-Match wins over If-Modified-Since", map[string]string{"If-None-Match": `"x"`, "If-Modified-Since": at}, existing, Read, Proceed},
		{"If-Modified-Since at the change", map[string]string{"If-Modified-Since": at}, existing, Read, NotModified},
		{"If-Modified-Since before the change", map[string]string{"If-Modified-Since": before}, existing, Read, Proceed},
		{"If-Modified-Since on nothing", map[string]string{"If-Modified-Since": at}, Validators{}, Create, Proceed},
	}
	for _, tc := range cases {
		h := http.Header{}
		for k, v := range tc.headers {
			h.Set(k, v)
		}
		if got := EvaluateHTTP(h, tc.v, tc.access); got != tc.want {
			t.Errorf("%s: EvaluateHTTP = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestParseRangeHonoursEachGrammar(t *testing.T) {
	gcs := RangeOpts{AllowSuffix: true, AllowOpenEnd: true, ClampEnd: true}
	azure := RangeOpts{AllowOpenEnd: true, ClampEnd: true}
	exact := RangeOpts{}
	type want struct {
		start, end  int64
		malformed   bool
		unsatisfied bool
	}
	cases := []struct {
		header string
		opts   RangeOpts
		size   int64
		want   want
	}{
		{"bytes=2-5", exact, 10, want{start: 2, end: 5}},
		{"bytes= 2 - 5 ", exact, 10, want{start: 2, end: 5}},
		{"bytes=2-12", exact, 10, want{unsatisfied: true}},
		{"bytes=2-12", azure, 10, want{start: 2, end: 9}},
		{"bytes=2-", exact, 10, want{malformed: true}},
		{"bytes=2-", azure, 10, want{start: 2, end: 9}},
		{"bytes=-3", azure, 10, want{malformed: true}},
		{"bytes=-3", gcs, 10, want{start: 7, end: 9}},
		{"bytes=-30", gcs, 10, want{start: 0, end: 9}},
		{"bytes=-0", gcs, 10, want{unsatisfied: true}},
		{"bytes=-3", gcs, 0, want{unsatisfied: true}},
		{"bytes=10-", gcs, 10, want{unsatisfied: true}},
		{"bytes=5-2", gcs, 10, want{malformed: true}},
		{"bytes=0-1,4-5", gcs, 10, want{malformed: true}},
		{"items=0-1", gcs, 10, want{malformed: true}},
		{"bytes=+1-2", gcs, 10, want{malformed: true}},
	}
	for _, tc := range cases {
		r, err := ParseRange(tc.header, tc.opts)
		if tc.want.malformed {
			if !errors.Is(err, ErrMalformedRange) {
				t.Errorf("%q %+v: err = %v, want malformed", tc.header, tc.opts, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q %+v: %v", tc.header, tc.opts, err)
			continue
		}
		start, end, ok := r.Resolve(tc.size)
		if tc.want.unsatisfied {
			if ok {
				t.Errorf("%q against %d resolved to %d-%d, want unsatisfiable", tc.header, tc.size, start, end)
			}
			continue
		}
		if !ok || start != tc.want.start || end != tc.want.end {
			t.Errorf("%q against %d = %d-%d %v, want %d-%d", tc.header, tc.size, start, end, ok, tc.want.start, tc.want.end)
		}
	}
}

func TestServeRangeStreamsTheRangeAndSkipsHEADBodies(t *testing.T) {
	body := strings.NewReader("0123456789")
	rec := httptest.NewRecorder()
	if err := ServeRange(rec, httptest.NewRequest(http.MethodGet, "/", nil), body, 3, 5, 10); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "345" ||
		rec.Header().Get("Content-Range") != "bytes 3-5/10" || rec.Header().Get("Content-Length") != "3" {
		t.Fatalf("ranged GET = %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	rec = httptest.NewRecorder()
	ServeWhole(rec, httptest.NewRequest(http.MethodHead, "/", nil), strings.NewReader("0123456789"), 10)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "10" {
		t.Fatalf("HEAD = %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
}

func TestRollUpAndPageAfterPageItemsAndPrefixesTogether(t *testing.T) {
	names := []string{"a/1", "a/2", "ab", "abc", "b", "c/x/1", "c/y"}
	id := func(s string) string { return s }
	entries := RollUp(names, id, nil, "", "/")
	keys := func(page []Entry[string]) []string {
		var out []string
		for _, e := range page {
			out = append(out, e.Key)
		}
		return out
	}
	if got := keys(entries); !reflect.DeepEqual(got, []string{"a/", "ab", "abc", "b", "c/"}) {
		t.Fatalf("RollUp = %v", got)
	}
	var seen []string
	marker := ""
	for pages := 0; ; pages++ {
		page, truncated, next := PageAfter(entries, marker, 2)
		seen = append(seen, keys(page)...)
		if !truncated {
			break
		}
		if pages > 5 {
			t.Fatal("paging did not end")
		}
		marker = next
	}
	if !reflect.DeepEqual(seen, keys(entries)) {
		t.Fatalf("paged = %v", seen)
	}
	// A marker that is an item name does not skip a longer name it prefixes.
	if page, _, _ := PageAfter(entries, "ab", -1); !reflect.DeepEqual(keys(page), []string{"abc", "b", "c/"}) {
		t.Fatalf("after ab = %v", keys(page))
	}
	// A marker inside a common prefix still lists the prefix while an item
	// under it follows the marker.
	if page, _, _ := PageAfter(entries, "a/1", -1); !reflect.DeepEqual(keys(page), []string{"a/", "ab", "abc", "b", "c/"}) {
		t.Fatalf("after a/1 = %v", keys(page))
	}
	if page, _, _ := PageAfter(entries, "a/2", -1); keys(page)[0] != "ab" {
		t.Fatalf("after a/2 = %v", keys(page))
	}
	if page, truncated, next := PageAfter(entries, "", 0); len(page) != 0 || !truncated || next != "" {
		t.Fatalf("a zero limit = %v %v %q", keys(page), truncated, next)
	}
	nested := RollUp([]string{"c/x/1", "c/y"}, id, nil, "c/", "/")
	if got := keys(nested); !reflect.DeepEqual(got, []string{"c/x/", "c/y"}) {
		t.Fatalf("RollUp under c/ = %v", got)
	}
}

func TestPageAfterUsesCursorsWhenNamesRepeat(t *testing.T) {
	type version struct{ name, stamp string }
	items := []version{{"a", ""}, {"a", "1"}, {"a", "2"}, {"b", ""}}
	entries := RollUp(items, func(v version) string { return v.name },
		func(v version) string { return v.name + "\x00" + v.stamp }, "", "/")
	page, truncated, next := PageAfter(entries, "", 2)
	if len(page) != 2 || !truncated {
		t.Fatalf("first page = %d %v", len(page), truncated)
	}
	rest, truncated, _ := PageAfter(entries, next, 2)
	if truncated || len(rest) != 2 || rest[0].Item != items[2] || rest[1].Item != items[3] {
		t.Fatalf("second page = %+v", rest)
	}
}
