package simulator

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestAWSQueryRouterInvalidActionEscaped verifies the InvalidAction error body
// XML-escapes the reflected Action value, so a value containing XML
// metacharacters can't produce malformed or attacker-shaped XML.
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
	// The escaped form must be present instead.
	if !strings.Contains(body, "&lt;inject&gt;") {
		t.Errorf("expected escaped action in body, got %q", body)
	}
}

// TestAWSQueryRouterDispatchesVersioned verifies the (Version, Action) routing:
// a versioned handler wins over the legacy bucket for the same Action.
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

	// No Version → legacy bucket.
	got = ""
	form2 := url.Values{"Action": {"ListTagsForResource"}}
	req2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form2.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(httptest.NewRecorder(), req2)
	if got != "legacy" {
		t.Errorf("unversioned request should hit legacy bucket, got %q", got)
	}
}

// TestReadJSONCapsBody verifies ReadJSON rejects an oversized body instead of
// reading it unbounded into memory.
func TestReadJSONCapsBody(t *testing.T) {
	// A body just over the cap: a valid-ish JSON whitespace-padded blob.
	big := strings.Repeat(" ", maxQueryJSONBody+10) + "{}"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
	var v map[string]any
	if err := ReadJSON(req, &v); err == nil {
		t.Fatal("expected ReadJSON to reject a body over the cap")
	}

	// A small body still decodes.
	req2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"k":"v"}`))
	var v2 map[string]any
	if err := ReadJSON(req2, &v2); err != nil {
		t.Fatalf("small body should decode: %v", err)
	}
	if v2["k"] != "v" {
		t.Errorf("decode mismatch: %v", v2)
	}
}
