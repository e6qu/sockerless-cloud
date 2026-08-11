package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The expectations below were captured from the live Microsoft Entra ID
// endpoint, https://login.microsoftonline.com/common/discovery/instance, by
// requesting each branch against it: every status code, error code, AADSTS
// number and JSON field name asserted here is the shape that endpoint actually
// returns, not a shape inferred from documentation.

const instanceDiscoveryHost = "login.simulator.test"

func instanceDiscoveryGET(t *testing.T, query url.Values) *httptest.ResponseRecorder {
	t.Helper()
	srv := buildAuthTestSim(t)
	req := httptest.NewRequest(http.MethodGet, "/common/discovery/instance?"+query.Encode(), nil)
	req.Host = instanceDiscoveryHost
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func decodeInstanceDiscovery(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode instance discovery response: %v\nbody: %s", err, rec.Body.String())
	}
	return body
}

func authorizeEndpoint(host, tenant, version string) string {
	return fmt.Sprintf("https://%s/%s/oauth2%s/authorize", host, tenant, version)
}

// TestInstanceDiscovery_V11ReturnsTenantEndpointAndAliasMetadata locks the
// api-version=1.1 success body. MSAL Go reads exactly these field names off
// the wire (tenant_discovery_endpoint, and metadata entries carrying
// preferred_network / preferred_cache / aliases), so a rename here silently
// breaks every client that validates an authority.
func TestInstanceDiscovery_V11ReturnsTenantEndpointAndAliasMetadata(t *testing.T) {
	const tenant = "11111111-1111-1111-1111-111111111111"
	rec := instanceDiscoveryGET(t, url.Values{
		"api-version":            {"1.1"},
		"authorization_endpoint": {authorizeEndpoint(instanceDiscoveryHost, tenant, "/v2.0")},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeInstanceDiscovery(t, rec)

	// A v2.0 authorize endpoint resolves to the tenant's v2.0 metadata
	// document, matching the live endpoint's mapping.
	wantDoc := fmt.Sprintf("http://%s/%s/v2.0/.well-known/openid-configuration", instanceDiscoveryHost, tenant)
	if got := body["tenant_discovery_endpoint"]; got != wantDoc {
		t.Errorf("tenant_discovery_endpoint = %v, want %q", got, wantDoc)
	}
	if got := body["api-version"]; got != "1.1" {
		t.Errorf("api-version = %v, want \"1.1\"", got)
	}

	metadata, ok := body["metadata"].([]any)
	if !ok || len(metadata) != 1 {
		t.Fatalf("metadata = %v, want exactly one instance entry", body["metadata"])
	}
	entry, ok := metadata[0].(map[string]any)
	if !ok {
		t.Fatalf("metadata[0] = %v, want an object", metadata[0])
	}
	// The simulator is a single authority instance: it is its own preferred
	// network and cache host and aliases only itself.
	if got := entry["preferred_network"]; got != instanceDiscoveryHost {
		t.Errorf("preferred_network = %v, want %q", got, instanceDiscoveryHost)
	}
	if got := entry["preferred_cache"]; got != instanceDiscoveryHost {
		t.Errorf("preferred_cache = %v, want %q", got, instanceDiscoveryHost)
	}
	aliases, ok := entry["aliases"].([]any)
	if !ok || len(aliases) != 1 || aliases[0] != instanceDiscoveryHost {
		t.Errorf("aliases = %v, want [%q]", entry["aliases"], instanceDiscoveryHost)
	}
}

// TestInstanceDiscovery_V10OmitsAliasMetadata locks the api-version=1.0 body.
// The live endpoint answers 1.0 with the tenant discovery endpoint alone — the
// api-version echo and the alias table are 1.1 additions — and MSAL Python
// requests 1.0.
func TestInstanceDiscovery_V10OmitsAliasMetadata(t *testing.T) {
	const tenant = "common"
	rec := instanceDiscoveryGET(t, url.Values{
		"api-version":            {"1.0"},
		"authorization_endpoint": {authorizeEndpoint(instanceDiscoveryHost, tenant, "/v2.0")},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeInstanceDiscovery(t, rec)
	if body["tenant_discovery_endpoint"] == nil {
		t.Errorf("tenant_discovery_endpoint missing: %s", rec.Body.String())
	}
	if _, present := body["metadata"]; present {
		t.Errorf("api-version 1.0 must not carry metadata: %s", rec.Body.String())
	}
	if _, present := body["api-version"]; present {
		t.Errorf("api-version 1.0 must not echo api-version: %s", rec.Body.String())
	}
}

// TestInstanceDiscovery_NonV2EndpointsResolveToV1Document covers the other half
// of the live endpoint's mapping: only a `/oauth2/v2.0/authorize` endpoint
// resolves to the v2.0 metadata document. A v1 authorize endpoint, an issuer
// (always a v1 identifier), and a path that is not an authorize endpoint at all
// each resolve to the tenant's v1 document.
func TestInstanceDiscovery_NonV2EndpointsResolveToV1Document(t *testing.T) {
	const tenant = "11111111-1111-1111-1111-111111111111"
	wantDoc := fmt.Sprintf("http://%s/%s/.well-known/openid-configuration", instanceDiscoveryHost, tenant)

	for _, tc := range []struct {
		name  string
		query url.Values
	}{
		{"v1 authorize endpoint", url.Values{
			"api-version":            {"1.1"},
			"authorization_endpoint": {authorizeEndpoint(instanceDiscoveryHost, tenant, "")},
		}},
		{"issuer", url.Values{
			"api-version": {"1.1"},
			"issuer":      {fmt.Sprintf("https://%s/%s/", instanceDiscoveryHost, tenant)},
		}},
		{"bare tenant path", url.Values{
			"api-version":            {"1.1"},
			"authorization_endpoint": {fmt.Sprintf("https://%s/%s", instanceDiscoveryHost, tenant)},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := instanceDiscoveryGET(t, tc.query)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if got := decodeInstanceDiscovery(t, rec)["tenant_discovery_endpoint"]; got != wantDoc {
				t.Errorf("tenant_discovery_endpoint = %v, want %q", got, wantDoc)
			}
		})
	}
}

// TestInstanceDiscovery_TenantDocumentIsServed proves the endpoint does not
// advertise a URL the simulator cannot answer: the tenant_discovery_endpoint it
// returns resolves to a real OpenID metadata document naming the token endpoint
// a client goes on to call.
func TestInstanceDiscovery_TenantDocumentIsServed(t *testing.T) {
	const tenant = "11111111-1111-1111-1111-111111111111"
	srv := buildAuthTestSim(t)

	req := httptest.NewRequest(http.MethodGet, "/common/discovery/instance?"+url.Values{
		"api-version":            {"1.1"},
		"authorization_endpoint": {authorizeEndpoint(instanceDiscoveryHost, tenant, "/v2.0")},
	}.Encode(), nil)
	req.Host = instanceDiscoveryHost
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("instance discovery status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	advertised, _ := decodeInstanceDiscovery(t, rec)["tenant_discovery_endpoint"].(string)
	parsed, err := url.Parse(advertised)
	if err != nil {
		t.Fatalf("parse advertised tenant_discovery_endpoint %q: %v", advertised, err)
	}

	docReq := httptest.NewRequest(http.MethodGet, parsed.Path, nil)
	docReq.Host = parsed.Host
	docRec := httptest.NewRecorder()
	srv.ServeHTTP(docRec, docReq)
	if docRec.Code != http.StatusOK {
		t.Fatalf("advertised tenant document %s returned %d, want 200: %s", advertised, docRec.Code, docRec.Body.String())
	}
	var doc struct {
		Issuer        string `json:"issuer"`
		TokenEndpoint string `json:"token_endpoint"`
		JWKSURI       string `json:"jwks_uri"`
	}
	if err := json.Unmarshal(docRec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode tenant metadata: %v", err)
	}
	if doc.Issuer == "" || doc.TokenEndpoint == "" || doc.JWKSURI == "" {
		t.Fatalf("tenant metadata missing issuer/token_endpoint/jwks_uri: %s", docRec.Body.String())
	}
	if !strings.Contains(doc.TokenEndpoint, tenant) {
		t.Errorf("token_endpoint = %q, want it to address tenant %s", doc.TokenEndpoint, tenant)
	}
}

// TestInstanceDiscovery_RejectsUnknownAuthority locks the rejection MSAL keys
// on. MSAL Python raises its "invalid_instance: The authority you provided …
// is not known" error when the body's `error` reads invalid_instance, and MSAL
// Go matches the same string against a 400 — so both the status and the code
// are load-bearing.
func TestInstanceDiscovery_RejectsUnknownAuthority(t *testing.T) {
	rec := instanceDiscoveryGET(t, url.Values{
		"api-version":            {"1.1"},
		"authorization_endpoint": {authorizeEndpoint("someone-elses-authority.test", "tenant", "/v2.0")},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	body := decodeInstanceDiscovery(t, rec)
	if got := body["error"]; got != "invalid_instance" {
		t.Errorf("error = %v, want \"invalid_instance\"", got)
	}
	desc, _ := body["error_description"].(string)
	if !strings.HasPrefix(desc, "AADSTS50049: Unknown or invalid instance.") {
		t.Errorf("error_description = %q, want the AADSTS50049 text", desc)
	}
	codes, ok := body["error_codes"].([]any)
	if !ok || len(codes) != 1 || codes[0] != float64(50049) {
		t.Errorf("error_codes = %v, want [50049]", body["error_codes"])
	}
	for _, field := range []string{"timestamp", "trace_id", "correlation_id", "error_uri"} {
		if body[field] == nil || body[field] == "" {
			t.Errorf("%s missing from the error body: %s", field, rec.Body.String())
		}
	}
}

// TestInstanceDiscovery_RejectsMalformedRequests locks the request-shape
// rejections, each with the AADSTS code the live endpoint answers with.
func TestInstanceDiscovery_RejectsMalformedRequests(t *testing.T) {
	const tenant = "11111111-1111-1111-1111-111111111111"
	authorize := authorizeEndpoint(instanceDiscoveryHost, tenant, "/v2.0")

	for _, tc := range []struct {
		name        string
		query       url.Values
		wantAADSTS  string
		wantMessage string
	}{
		{
			name:        "missing api-version",
			query:       url.Values{"authorization_endpoint": {authorize}},
			wantAADSTS:  "AADSTS500501",
			wantMessage: "Invalid value for 'api-version'.",
		},
		{
			name:        "unsupported api-version",
			query:       url.Values{"api-version": {"9.9"}, "authorization_endpoint": {authorize}},
			wantAADSTS:  "AADSTS500501",
			wantMessage: "Invalid value for 'api-version'.",
		},
		{
			name:        "neither issuer nor authorization_endpoint",
			query:       url.Values{"api-version": {"1.1"}},
			wantAADSTS:  "AADSTS500502",
			wantMessage: "Expected exactly one of 'issuer' and 'authorization_endpoint'.",
		},
		{
			name: "both issuer and authorization_endpoint",
			query: url.Values{
				"api-version":            {"1.1"},
				"issuer":                 {fmt.Sprintf("https://%s/%s/", instanceDiscoveryHost, tenant)},
				"authorization_endpoint": {authorize},
			},
			wantAADSTS:  "AADSTS500502",
			wantMessage: "Expected exactly one of 'issuer' and 'authorization_endpoint'.",
		},
		{
			name: "non-https authorization_endpoint",
			query: url.Values{
				"api-version":            {"1.1"},
				"authorization_endpoint": {strings.Replace(authorize, "https://", "http://", 1)},
			},
			wantAADSTS:  "AADSTS50050",
			wantMessage: "The request is malformed: invalid format for 'authorization_endpoint' value.",
		},
		{
			name:        "non-https issuer",
			query:       url.Values{"api-version": {"1.1"}, "issuer": {"http://" + instanceDiscoveryHost + "/" + tenant + "/"}},
			wantAADSTS:  "AADSTS50050",
			wantMessage: "The request is malformed: invalid format for 'issuer' value.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := instanceDiscoveryGET(t, tc.query)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			body := decodeInstanceDiscovery(t, rec)
			if got := body["error"]; got != "invalid_request" {
				t.Errorf("error = %v, want \"invalid_request\"", got)
			}
			desc, _ := body["error_description"].(string)
			if !strings.HasPrefix(desc, tc.wantAADSTS+": "+tc.wantMessage) {
				t.Errorf("error_description = %q, want it to start with %q", desc, tc.wantAADSTS+": "+tc.wantMessage)
			}
		})
	}
}

// TestInstanceDiscovery_OnlyServedUnderCommon matches the live endpoint, which
// serves instance discovery under /common alone and 404s a tenant-scoped
// spelling.
func TestInstanceDiscovery_OnlyServedUnderCommon(t *testing.T) {
	srv := buildAuthTestSim(t)
	req := httptest.NewRequest(http.MethodGet,
		"/11111111-1111-1111-1111-111111111111/discovery/instance?api-version=1.1&authorization_endpoint=https%3A%2F%2Fhost%2Ft%2Foauth2%2Fv2.0%2Fauthorize", nil)
	req.Host = instanceDiscoveryHost
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("tenant-scoped instance discovery status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
