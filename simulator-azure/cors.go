package main

import (
	"net/http"
	"strings"
)

// azureCORSMaxAge is the preflight cache lifetime the sim advertises for both
// Azure Resource Manager and Microsoft Graph, in seconds.
const azureCORSMaxAge = "3600"

// azureCORSSurface carries the CORS response headers one Azure data plane
// advertises to a browser. Azure Resource Manager and Microsoft Graph are
// distinct hosts on real Azure (management.azure.com vs graph.microsoft.com)
// with their own method and header contracts; the collapsed-host simulator
// keeps that distinction in the headers it answers with, even though both are
// served from the same origin here.
type azureCORSSurface struct {
	allowMethods  string
	allowHeaders  string
	exposeHeaders string
}

// azureARMCORSSurface mirrors the Azure Resource Manager control-plane
// contract: the full CRUD method set, the ARM correlation/client-request
// headers on top of the standard bearer + content headers, and the
// long-running-operation headers (Location, Azure-AsyncOperation,
// Retry-After) and rate-limit headers ARM exposes to callers.
var azureARMCORSSurface = azureCORSSurface{
	allowMethods: "GET, HEAD, PUT, POST, PATCH, DELETE, OPTIONS",
	allowHeaders: "authorization, content-type, accept, if-match, if-none-match, " +
		"x-ms-client-request-id, x-ms-return-client-request-id",
	exposeHeaders: "x-ms-request-id, x-ms-correlation-request-id, x-ms-client-request-id, " +
		"x-ms-ratelimit-remaining-subscription-reads, x-ms-ratelimit-remaining-subscription-writes, " +
		"retry-after, location, azure-asyncoperation, content-type",
}

// azureGraphCORSSurface mirrors Microsoft Graph's browser contract: Graph
// supports CORS for the /v1.0 (and /beta) endpoints so single-page apps can
// call it directly, with its own client-request-id / Prefer headers and
// request-id response headers.
var azureGraphCORSSurface = azureCORSSurface{
	allowMethods:  "GET, POST, PUT, PATCH, DELETE, OPTIONS",
	allowHeaders:  "authorization, content-type, accept, prefer, if-match, if-none-match, client-request-id",
	exposeHeaders: "request-id, client-request-id, content-type",
}

// azureCORSSurfaceFor reports the CORS surface a request path belongs to.
// Azure Resource Manager control-plane paths always begin with
// /subscriptions, /providers, or /tenants (the only tenant-scoped, unscoped,
// and directory-list ARM roots); Microsoft Graph is served at /v1.0. Every
// other path — the Microsoft Entra OAuth2/OpenID surface
// (/{tenant}/oauth2/*, /{tenant}/v2.0/.well-known/*), the console's own
// /auth/* auth layer, the OCI/ACR/storage/Key Vault/Service Bus/Event Grid
// data planes, and /health and /ui/ — is deliberately excluded: real Azure
// serves no CORS for the Entra token endpoint (the entire reason the
// federation broker in federation_broker.go exists), and the rest either
// authenticate with their own scheme or are not browser-called cross-origin.
func azureCORSSurfaceFor(path string) (azureCORSSurface, bool) {
	switch {
	case strings.HasPrefix(path, "/subscriptions"), strings.HasPrefix(path, "/providers"), strings.HasPrefix(path, "/tenants"):
		return azureARMCORSSurface, true
	case strings.HasPrefix(path, "/v1.0"):
		return azureGraphCORSSurface, true
	default:
		return azureCORSSurface{}, false
	}
}

// AzureCORSMiddleware serves faithful Azure Resource Manager and Microsoft
// Graph CORS: real Azure answers a browser's preflight OPTIONS request for
// these two data planes without requiring the bearer token or api-version the
// actual operation needs (a preflight carries neither — the browser sends it
// unauthenticated by design), and stamps Access-Control-Allow-Origin on the
// real response so the SPA's fetch() can read it. This must wrap the ARM
// bearer and api-version middlewares (see main.go) rather
// than sit inside them, exactly as a real preflight reaches Azure's edge
// before the resource provider's own auth check.
//
// The Microsoft Entra token endpoint (/{tenant}/oauth2/*) is a distinct host
// on real Azure (login.microsoftonline.com) that serves no CORS at all — the
// path match here never reaches it, so the browser-side client_credentials
// exchange stays impossible and the server-side broker in
// federation_broker.go remains the only path a browser has to a token.
func AzureCORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// No Origin header means this is not a browser cross-origin
			// request (a same-origin fetch, the Azure SDK, the az CLI, or
			// Terraform) — nothing to negotiate, and no header to add that a
			// real non-browser Azure client would not also see absent.
			next.ServeHTTP(w, r)
			return
		}
		surface, ok := azureCORSSurfaceFor(r.URL.Path)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		header := w.Header()
		header.Set("Access-Control-Allow-Origin", origin)
		header.Add("Vary", "Origin")
		header.Set("Access-Control-Expose-Headers", surface.exposeHeaders)

		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			header.Set("Access-Control-Allow-Methods", surface.allowMethods)
			header.Set("Access-Control-Allow-Headers", surface.allowHeaders)
			header.Set("Access-Control-Max-Age", azureCORSMaxAge)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
