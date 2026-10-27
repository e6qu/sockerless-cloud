package main

import (
	"net/http"
	"strings"
)

// azureCORSMaxAge is the preflight cache lifetime in seconds, for Azure
// Resource Manager and Microsoft Graph alike.
const azureCORSMaxAge = "3600"

// azureCORSSurface carries one plane's CORS headers. Azure Resource Manager
// and Microsoft Graph are distinct hosts with their own method and header
// contracts; the simulator keeps them distinct on one origin.
type azureCORSSurface struct {
	allowMethods  string
	allowHeaders  string
	exposeHeaders string
}

// Azure Resource Manager: the full CRUD method set, its correlation and
// client-request headers, and the long-running-operation and rate-limit
// headers it exposes.
var azureARMCORSSurface = azureCORSSurface{
	allowMethods: "GET, HEAD, PUT, POST, PATCH, DELETE, OPTIONS",
	allowHeaders: "authorization, content-type, accept, if-match, if-none-match, " +
		"x-ms-client-request-id, x-ms-return-client-request-id",
	exposeHeaders: "x-ms-request-id, x-ms-correlation-request-id, x-ms-client-request-id, " +
		"x-ms-ratelimit-remaining-subscription-reads, x-ms-ratelimit-remaining-subscription-writes, " +
		"retry-after, location, azure-asyncoperation, content-type",
}

// Microsoft Graph serves CORS on /v1.0 and /beta, with its own
// client-request-id and Prefer headers and request-id response header.
var azureGraphCORSSurface = azureCORSSurface{
	allowMethods:  "GET, POST, PUT, PATCH, DELETE, OPTIONS",
	allowHeaders:  "authorization, content-type, accept, prefer, if-match, if-none-match, client-request-id",
	exposeHeaders: "request-id, client-request-id, content-type",
}

// The Log Analytics and Application Insights query APIs (api.loganalytics.io,
// api.applicationinsights.io) serve CORS so a browser can run a Kusto query
// with its own token.
var azureLogsCORSSurface = azureCORSSurface{
	allowMethods:  "GET, POST, OPTIONS",
	allowHeaders:  "authorization, content-type, accept, prefer, x-ms-app, x-ms-client-request-id",
	exposeHeaders: "x-ms-request-id, content-type",
}

// azureCORSSurfaceFor: Azure Resource Manager paths begin with /subscriptions,
// /providers or /tenants; Microsoft Graph is /v1.0; the query APIs are
// /v1/workspaces and /v1/apps. Everything else is excluded: Azure serves no
// CORS for the Microsoft Entra token endpoint (hence federation_broker.go),
// and the other planes authenticate with their own scheme or are not called
// cross-origin.
func azureCORSSurfaceFor(path string) (azureCORSSurface, bool) {
	switch {
	case strings.HasPrefix(path, "/subscriptions"), strings.HasPrefix(path, "/providers"), strings.HasPrefix(path, "/tenants"):
		return azureARMCORSSurface, true
	case strings.HasPrefix(path, "/v1.0"):
		return azureGraphCORSSurface, true
	case strings.HasPrefix(path, "/v1/workspaces/"), strings.HasPrefix(path, "/v1/apps/"):
		return azureLogsCORSSurface, true
	default:
		return azureCORSSurface{}, false
	}
}

// AzureCORSMiddleware answers a browser's preflight without the bearer or
// api-version the operation needs, as Azure's edge does, and stamps
// Access-Control-Allow-Origin on the real response. It wraps the bearer and
// api-version middlewares (main.go), as a preflight reaches Azure's edge before
// the resource provider's auth.
func AzureCORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Without an Origin header the request is not cross-origin: nothing to
			// negotiate.
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
