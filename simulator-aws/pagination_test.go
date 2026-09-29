package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Each AWS service answers a page token it never issued with the error its
// model declares, instead of silently restarting at the first page.
func TestAWSPageRejectsForeignToken(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
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
	} {
		rec := httptest.NewRecorder()
		if _, _, ok := awsPage(rec, c.bad, []int{1, 2, 3}, "not-a-token", 1, 0); ok {
			t.Fatalf("%s: a foreign token was accepted", c.code)
		}
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.code) {
			t.Errorf("got %d %s, want 400 %s", rec.Code, rec.Body.String(), c.code)
		}
	}
}

func TestAWSPageSizes(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	for _, c := range []struct {
		token     string
		size, def int
		want      []int
		next      string
	}{
		{"", 0, 0, []int{1, 2, 3, 4, 5}, ""},
		{"", 2, 0, []int{1, 2}, "2"},
		{"", 0, 3, []int{1, 2, 3}, "3"},
		{"", 9, 3, []int{1, 2, 3}, "3"},
		{"3", 0, 3, []int{4, 5}, ""},
	} {
		rec := httptest.NewRecorder()
		page, next, ok := awsPage(rec, ec2BadToken, items, c.token, c.size, c.def)
		if !ok || !reflect.DeepEqual(page, c.want) || next != c.next {
			t.Errorf("%+v: got %v %q %v", c, page, next, ok)
		}
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
