package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"testing"
)

// The rule set can read and remove the inserted headers, and a client naming
// X-Forwarded-For in Connection cannot strip the gateway's own entry.
func TestApplicationGatewayForwardInsertsItsHeadersBeforeRewrites(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(r.Header)
	}))
	t.Cleanup(backend.Close)
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	backendIP, backendPort, err := net.SplitHostPort(backendURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(backendPort)
	if err != nil {
		t.Fatal(err)
	}
	var settings ApplicationGatewayBackendHTTPSettings
	settings.Properties.Port = int32(port)
	settings.Properties.Protocol = "Http"
	rewrite := &ApplicationGatewayRewriteRuleSet{Properties: ApplicationGatewayRewriteRuleSetProperties{
		RewriteRules: []ApplicationGatewayRewriteRule{{
			Name:         "proxy-chain",
			RuleSequence: 100,
			Conditions: []ApplicationGatewayRewriteRuleCondition{{
				Variable: "add_x_forwarded_for_proxy",
				Pattern:  `^203\.0\.113\.9, 198\.51\.100\.23$`,
			}},
			ActionSet: &ApplicationGatewayRewriteRuleActionSet{
				RequestHeaderConfigurations: []ApplicationGatewayHeaderConfiguration{
					{HeaderName: "X-Proxy-Chain", HeaderValue: "matched"},
					{HeaderName: "X-Original-URL"},
				},
			},
		}},
	}}

	received := func(rewrite *ApplicationGatewayRewriteRuleSet, header http.Header) http.Header {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://20.42.0.10:8080/orders?page=2", nil)
		req.RemoteAddr = "198.51.100.23:41000"
		for name, values := range header {
			req.Header[name] = values
		}
		rr := httptest.NewRecorder()
		if err := applicationGatewayForward(rr, req, settings, rewrite, applicationGatewayServer{address: backendIP}, netip.Addr{}); err != nil {
			t.Fatal(err)
		}
		var seen http.Header
		if err := json.Unmarshal(rr.Body.Bytes(), &seen); err != nil {
			t.Fatalf("backend answer %q: %v", rr.Body.String(), err)
		}
		return seen
	}

	seen := received(nil, http.Header{"X-Forwarded-For": {"203.0.113.9"}, "X-Forwarded-Proto": {"https"}})
	for name, want := range map[string]string{
		"X-Forwarded-For":   "203.0.113.9, 198.51.100.23:41000",
		"X-Forwarded-Proto": "http",
		"X-Forwarded-Port":  "8080",
		"X-Original-Host":   "20.42.0.10:8080",
		"X-Original-Url":    "/orders?page=2",
	} {
		if got := seen.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if got := seen.Get("X-Appgw-Trace-Id"); !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(got) {
		t.Errorf("X-AppGW-Trace-Id = %q", got)
	}

	seen = received(nil, http.Header{"Connection": {"X-Forwarded-For"}, "X-Forwarded-For": {"203.0.113.9"}})
	if got := seen.Get("X-Forwarded-For"); got != "198.51.100.23:41000" {
		t.Errorf("X-Forwarded-For with the client's copy nominated hop-by-hop = %q", got)
	}

	seen = received(rewrite, http.Header{"X-Forwarded-For": {"203.0.113.9"}})
	if got := seen.Get("X-Proxy-Chain"); got != "matched" {
		t.Errorf("rewrite on add_x_forwarded_for_proxy set X-Proxy-Chain = %q", got)
	}
	if got := seen.Values("X-Original-Url"); len(got) != 0 {
		t.Errorf("rewrite removing X-Original-URL left %q", got)
	}
}
