package main

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Microsoft Entra ID instance discovery.
//
// `GET /common/discovery/instance` is the endpoint an OpenID Connect client
// calls to learn (a) whether an authority host belongs to this identity
// service and (b) where that authority's tenant metadata document lives. Entra
// serves it unauthenticated at the authority root; the simulator serves the
// same contract for its own authority host.
//
// The contract below mirrors the live endpoint at
// login.microsoftonline.com/common/discovery/instance response for response:
// the query takes an `api-version` of 1.0 or 1.1 and exactly one of `issuer`
// or `authorization_endpoint`; api-version 1.0 answers with the bare
// `tenant_discovery_endpoint`, api-version 1.1 adds `api-version` and the
// `metadata` alias table; and each rejection carries its AADSTS code in the
// standard OAuth error envelope.
//
// Only the first path segment of the supplied endpoint is read as the tenant —
// Entra does not check that the tenant exists here, it checks only the host.
// A `/oauth2/v2.0/authorize` endpoint resolves to the tenant's v2.0 metadata
// document, anything else to the v1 document.

// azureInstanceDiscoveryAPIVersions are the api-version values the endpoint
// accepts. MSAL Python requests 1.0 and MSAL Go requests 1.1.
var azureInstanceDiscoveryAPIVersions = map[string]bool{"1.0": true, "1.1": true}

// handleAzureInstanceDiscovery serves GET /common/discovery/instance.
func handleAzureInstanceDiscovery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	if !azureInstanceDiscoveryAPIVersions[q.Get("api-version")] {
		azureInstanceDiscoveryError(w, "invalid_request", 500501,
			"Invalid value for 'api-version'.")
		return
	}

	issuer := q.Get("issuer")
	authorizationEndpoint := q.Get("authorization_endpoint")
	// Entra requires exactly one of the two; supplying both, or neither, is
	// AADSTS500502.
	if (issuer == "") == (authorizationEndpoint == "") {
		azureInstanceDiscoveryError(w, "invalid_request", 500502,
			"Expected exactly one of 'issuer' and 'authorization_endpoint'.")
		return
	}

	param, raw := "authorization_endpoint", authorizationEndpoint
	if authorizationEndpoint == "" {
		param, raw = "issuer", issuer
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		azureInstanceDiscoveryError(w, "invalid_request", 50050,
			fmt.Sprintf("The request is malformed: invalid format for '%s' value.", param))
		return
	}

	// The simulator is the authority reachable at the host this request
	// arrived on, and vouches for that host alone — the same way the live
	// endpoint vouches for the hosts Microsoft operates and answers
	// AADSTS50049 for every other authority.
	authorityHost := azureAuthorityHost(r)
	if !strings.EqualFold(parsed.Host, authorityHost) {
		azureInstanceDiscoveryError(w, "invalid_instance", 50049,
			"Unknown or invalid instance.")
		return
	}

	body := map[string]any{
		"tenant_discovery_endpoint": azureTenantDiscoveryEndpoint(r, parsed),
	}
	// api-version 1.0 answers with the tenant discovery endpoint alone;
	// the alias table is a 1.1 addition.
	if q.Get("api-version") == "1.1" {
		body["api-version"] = "1.1"
		body["metadata"] = []map[string]any{{
			// A simulator authority is a single instance: it is its own
			// preferred network and cache host, and has no aliases beyond
			// itself. Entra returns one entry per national cloud because it
			// operates several.
			"preferred_network": authorityHost,
			"preferred_cache":   authorityHost,
			"aliases":           []string{authorityHost},
		}}
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

// azureTenantDiscoveryEndpoint maps the supplied authorization endpoint or
// issuer to the OpenID metadata document that describes the same tenant. The
// tenant is the first path segment; an authorization endpoint spelled
// `/oauth2/v2.0/authorize` resolves to the v2.0 document and everything else
// to the v1 document — an issuer (always a v1 identifier) therefore always
// resolves to the v1 document.
func azureTenantDiscoveryEndpoint(r *http.Request, endpoint *url.URL) string {
	tenant := extractTenantFromPath(endpoint.Path)
	version := ""
	if strings.HasSuffix(strings.TrimSuffix(endpoint.Path, "/"), "/oauth2/v2.0/authorize") {
		version = "/v2.0"
	}
	return fmt.Sprintf("%s/%s%s/.well-known/openid-configuration",
		azureAuthBaseURL(r), tenant, version)
}

// azureAuthorityHost returns the host — including a non-default port — that
// this request addressed the simulator's authority by.
func azureAuthorityHost(r *http.Request) string {
	if host := r.Header.Get("X-Forwarded-Host"); host != "" {
		return host
	}
	return r.Host
}

// azureInstanceDiscoveryError writes the OAuth error envelope Entra returns
// from the instance discovery endpoint. The AADSTS code is repeated in
// error_codes and interpolated into error_description together with the trace
// and correlation identifiers, exactly as the live endpoint formats it.
func azureInstanceDiscoveryError(w http.ResponseWriter, code string, aadsts int, message string) {
	now := time.Now().UTC()
	timestamp := now.Format("2006-01-02 15:04:05Z")
	traceID := azureDiagnosticID()
	correlationID := azureDiagnosticID()
	body := map[string]any{
		"error": code,
		"error_description": fmt.Sprintf("AADSTS%d: %s Trace ID: %s Correlation ID: %s Timestamp: %s",
			aadsts, message, traceID, correlationID, timestamp),
		"error_codes":    []int{aadsts},
		"timestamp":      timestamp,
		"trace_id":       traceID,
		"correlation_id": correlationID,
	}
	// Entra carries the lookup link on instance errors but not on the
	// request-shape ones.
	if code == "invalid_instance" {
		body["error_uri"] = fmt.Sprintf("https://login.microsoftonline.com/error?code=%d", aadsts)
	}
	sim.WriteJSON(w, http.StatusBadRequest, body)
}

// azureDiagnosticID returns a random RFC 4122 version 4 UUID, the form Entra
// uses for the trace and correlation identifiers on every error response.
func azureDiagnosticID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("read random bytes for diagnostic identifier: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
