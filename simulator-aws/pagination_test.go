package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// Each AWS service answers a page token it never issued with the error its
// model declares, instead of silently restarting at the first page.
func TestAWSPageRejectsForeignToken(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	name := func(s string) string { return s }
	for _, c := range []struct {
		bad  awsBadToken
		code string
	}{
		{ec2BadToken, "InvalidPaginationToken"},
		{asBadToken, "InvalidNextToken"},
		{iamBadToken, "InvalidInput"},
		{snsBadToken(r), "InvalidParameter"},
		{kmsBadToken, "InvalidMarkerException"},
		{glueBadToken, "InvalidInputException"},
		{ssmBadToken, "InvalidNextToken"},
		{smBadToken, "InvalidNextTokenException"},
		{sfnBadToken, "InvalidToken"},
		{r53BadToken, "InvalidInput"},
		{batchBadToken, "ClientException"},
		{wafBadToken, "WAFInvalidParameterException"},
		{lambdaBadToken, "InvalidParameterValueException"},
		{acmBadToken, "ValidationException"},
		{amplifyBadToken, "BadRequestException"},
		{appASBadToken, "InvalidNextTokenException"},
		{cloudMapBadToken, "InvalidInput"},
		{cbBadToken, "InvalidInputException"},
		{ddbBadToken, "ValidationException"},
		{ecrBadToken, "InvalidParameterException"},
		{ecsBadToken, "InvalidParameterException"},
		{efsBadToken, "BadRequest"},
		{ebBadToken, "ValidationException"},
		{logsBadToken, "InvalidParameterException"},
		{sqsBadToken, "InvalidParameterValue"},
		{schedulerBadToken, "ValidationException"},
	} {
		rec := httptest.NewRecorder()
		if _, _, ok := awsKeyPage(rec, c.bad, []string{"a", "b", "c"}, name, "not a token!", 1, 0); ok {
			t.Fatalf("%s: a foreign token was accepted", c.code)
		}
		answered := rec.Body.String() + " " + rec.Header().Get("X-Amzn-Errortype")
		if rec.Code != http.StatusBadRequest || !strings.Contains(answered, c.code) {
			t.Errorf("got %d %s, want 400 %s", rec.Code, answered, c.code)
		}
	}
}

func TestAWSPageSizes(t *testing.T) {
	name := func(s string) string { return s }
	items := []string{"e", "c", "a", "d", "b"}
	for _, c := range []struct {
		size, def int
		pages     [][]string
	}{
		{0, 0, [][]string{{"a", "b", "c", "d", "e"}}},
		{2, 0, [][]string{{"a", "b"}, {"c", "d"}, {"e"}}},
		{0, 3, [][]string{{"a", "b", "c"}, {"d", "e"}}},
		{9, 3, [][]string{{"a", "b", "c"}, {"d", "e"}}},
	} {
		var got [][]string
		token := ""
		for {
			page, next, ok := awsKeyPage(httptest.NewRecorder(), ec2BadToken, slices.Clone(items), name, token, c.size, c.def)
			if !ok {
				t.Fatalf("%+v: token %q refused", c, token)
			}
			got = append(got, page)
			if next == "" {
				break
			}
			token = next
		}
		if !reflect.DeepEqual(got, c.pages) {
			t.Errorf("size %d def %d: pages %v, want %v", c.size, c.def, got, c.pages)
		}
	}
}

// The order keys sort as the values they spell: numbers numerically, and a
// descending key in reverse, prefixes included.
func TestAWSOrderKeys(t *testing.T) {
	ints := []int64{-5, -1, 0, 1, 9, 10, 1 << 40}
	for i := 1; i < len(ints); i++ {
		if listq.IntKey(ints[i-1]) >= listq.IntKey(ints[i]) {
			t.Errorf("listq.IntKey(%d) does not sort before listq.IntKey(%d)", ints[i-1], ints[i])
		}
	}
	floats := []float64{-2.5, -0.5, 0, 0.25, 1, 1.5, 1e12}
	for i := 1; i < len(floats); i++ {
		if awsFloatKey(floats[i-1]) >= awsFloatKey(floats[i]) {
			t.Errorf("awsFloatKey(%v) does not sort before awsFloatKey(%v)", floats[i-1], floats[i])
		}
	}
	strs := []string{"", "a", "ab", "abc", "b", "ba"}
	for i := 1; i < len(strs); i++ {
		if awsDescKey(strs[i-1]) <= awsDescKey(strs[i]) {
			t.Errorf("awsDescKey(%q) does not sort after awsDescKey(%q)", strs[i-1], strs[i])
		}
	}
}

// A request that names the same item twice gets it twice, in request order,
// even when a page boundary falls between the two mentions.
func TestAWSNamedPageKeepsDuplicatesAcrossPages(t *testing.T) {
	name := func(s string) string { return s }
	named := []string{"b", "a", "a", "c"}
	var got []string
	token := ""
	for {
		page, next, ok := awsNamedOrKeyPage(httptest.NewRecorder(), ecrBadToken, named, true, name, token, 2, 100)
		if !ok {
			t.Fatalf("token %q refused", token)
		}
		got = append(got, page...)
		if next == "" {
			break
		}
		token = next
	}
	if !reflect.DeepEqual(got, named) {
		t.Errorf("named pages = %v, want %v", got, named)
	}
	page, _, _ := awsNamedOrKeyPage(httptest.NewRecorder(), ecrBadToken, slices.Clone(named), false, name, "", 0, 100)
	if !reflect.DeepEqual(page, []string{"a", "a", "b", "c"}) {
		t.Errorf("an unnamed list pages by key: %v", page)
	}
}

// A positional page resumes after the last position returned, so an item
// appended between two calls lands on a later page in either direction.
func TestAWSSeqPageResumesAcrossAppends(t *testing.T) {
	events := []string{"e0", "e1", "e2"}
	page, next, ok := awsSeqPage(httptest.NewRecorder(), sfnBadToken, events, false, "", 2, 100)
	if !ok || !reflect.DeepEqual(page, []string{"e0", "e1"}) || next == "" {
		t.Fatalf("first page = %v %q %v", page, next, ok)
	}
	page, _, _ = awsSeqPage(httptest.NewRecorder(), sfnBadToken, append(events, "e3"), false, next, 2, 100)
	if !reflect.DeepEqual(page, []string{"e2", "e3"}) {
		t.Errorf("after an append the second page = %v", page)
	}
	page, next, _ = awsSeqPage(httptest.NewRecorder(), sfnBadToken, events, true, "", 2, 100)
	if !reflect.DeepEqual(page, []string{"e2", "e1"}) || next == "" {
		t.Fatalf("newest-first page = %v %q", page, next)
	}
	page, rest, _ := awsSeqPage(httptest.NewRecorder(), sfnBadToken, append(events, "e3"), true, next, 2, 100)
	if !reflect.DeepEqual(page, []string{"e0"}) || rest != "" {
		t.Errorf("newest first, after an append the second page = %v %q", page, rest)
	}
}

// DynamoDB's list cursors are keys: a listing resumes after the named key
// even when that item is gone.
func TestDDBKeyPageResumesAfterDeletedKey(t *testing.T) {
	names := []string{"a", "b", "d", "e"}
	id := func(s string) string { return s }
	page, last := ddbKeyPage(names, id, "", 2, 100)
	if !reflect.DeepEqual(page, []string{"a", "b"}) || last != "b" {
		t.Fatalf("first page %v last %q", page, last)
	}
	page, last = ddbKeyPage(names, id, "c", 2, 100)
	if !reflect.DeepEqual(page, []string{"d", "e"}) || last != "" {
		t.Fatalf("after a deleted key: %v last %q", page, last)
	}
	page, _ = ddbKeyPage(names, id, "", 0, 3)
	if len(page) != 3 {
		t.Fatalf("no limit takes the service maximum: %v", page)
	}
}

// A key-cursor page resumes after the last name returned, whatever was
// created or deleted before it in between, and still refuses a foreign token.
func TestAWSKeyPageResumesAfterTheLastKey(t *testing.T) {
	name := func(s string) string { return s }
	rec := httptest.NewRecorder()
	page, next, ok := awsKeyPage(rec, wafBadToken, []string{"d", "b", "a", "c"}, name, "", 2, 100)
	if !ok || !reflect.DeepEqual(page, []string{"a", "b"}) || next == "" {
		t.Fatalf("first page = %v %q %v", page, next, ok)
	}
	for _, between := range [][]string{{"b", "c", "d"}, {"0", "a", "b", "c", "d"}} {
		page, rest, ok := awsKeyPage(rec, wafBadToken, slices.Clone(between), name, next, 2, 100)
		if !ok || !reflect.DeepEqual(page, []string{"c", "d"}) || rest != "" {
			t.Errorf("after %v the second page = %v %q %v", between, page, rest, ok)
		}
	}
	if _, _, ok := awsKeyPage(httptest.NewRecorder(), wafBadToken, []string{"a"}, name, "not a token!", 1, 100); ok {
		t.Error("a foreign token was accepted")
	}
}
