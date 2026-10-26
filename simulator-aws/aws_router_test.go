package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A reflected Action with XML metacharacters must not shape the error XML.
func TestAWSQueryRouterInvalidActionEscaped(t *testing.T) {
	r := NewAWSQueryRouter()

	form := url.Values{"Action": {"<inject>&\"]]>"}}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<inject>") {
		t.Errorf("raw '<inject>' leaked unescaped into the XML body: %q", body)
	}
	if !strings.Contains(body, "&lt;inject&gt;") {
		t.Errorf("expected escaped action in body, got %q", body)
	}
}

func TestAWSQueryRouterDispatchesVersioned(t *testing.T) {
	r := NewAWSQueryRouter()
	got := ""
	r.Register("ListTagsForResource", func(w http.ResponseWriter, _ *http.Request) { got = "legacy" })
	r.RegisterVersioned("2014-10-31", "ListTagsForResource", func(w http.ResponseWriter, _ *http.Request) { got = "rds" })

	form := url.Values{"Action": {"ListTagsForResource"}, "Version": {"2014-10-31"}}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(httptest.NewRecorder(), req)
	if got != "rds" {
		t.Errorf("versioned handler should win, got %q", got)
	}

	// No Version uses the unversioned bucket.
	got = ""
	form2 := url.Values{"Action": {"ListTagsForResource"}}
	req2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form2.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(httptest.NewRecorder(), req2)
	if got != "legacy" {
		t.Errorf("unversioned request should hit legacy bucket, got %q", got)
	}
}
