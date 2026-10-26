package main

import (
	"fmt"
	"net/http"
	"strings"
)

// armTokenAudiences are the canonical Azure Resource Manager token audiences.
// Real ARM authorizes a request only when the bearer token's `aud` claim
// identifies ARM itself; a token minted for a different resource (for example
// https://storage.azure.com/ or a Key Vault audience) is rejected even though
// it is signed by the same tenant. The simulator mints ARM tokens with aud
// https://management.azure.com/ (see defaultAzureTokenAudience and
// azureScopeAudienceOverrides) and advertises both management audiences in
// /metadata/endpoints, so both spellings — with and without a trailing slash —
// are accepted here.
var armTokenAudiences = map[string]bool{
	"https://management.azure.com/":        true,
	"https://management.azure.com":         true,
	"https://management.core.windows.net/": true,
	"https://management.core.windows.net":  true,
}

// AzureBearerVerificationMiddleware authenticates and authorizes every Azure
// Resource Manager request as ARM does: the bearer must be a token this
// simulator signed, unexpired, for the management audience and from the
// subscription's tenant, and Azure RBAC must grant the caller the request's
// action at its scope (armAuthorizeAndServe). Requests on a data plane's host
// or path authenticate with that plane's own scheme and pass through.
func AzureBearerVerificationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !azureARMRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			azureARMBearerUnauthorized(w, r,
				"Authentication failed. The request did not carry an 'Authorization: Bearer <token>' header.")
			return
		}
		claims, err := verifyAzureSimJWT(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")))
		if err != nil {
			azureARMBearerUnauthorized(w, r,
				fmt.Sprintf("The access token is not valid: %v.", err))
			return
		}
		aud := azureTokenAudience(claims)
		if !azureARMAudienceValid(aud, r) {
			azureARMBearerUnauthorized(w, r, fmt.Sprintf(
				"The access token was issued for audience %q, which is not the Azure Resource Manager audience (https://management.azure.com/). Acquire the token with scope https://management.azure.com/.default.",
				aud))
			return
		}
		armAuthorizeAndServe(w, r, next, rbacCallerFromClaims(claims))
	})
}

// azureARMRequest reports whether a request addresses Azure Resource Manager:
// an ARM path on a host that is not a data plane's.
func azureARMRequest(r *http.Request) bool {
	path := "/" + strings.Join(armPathSegments(r.URL.Path), "/")
	lower := strings.ToLower(path)
	if lower != "/subscriptions" && lower != "/tenants" && !strings.HasPrefix(lower, "/tenants/") &&
		!isAzureARMPath(lower) {
		return false
	}
	if azureARMPathUsesSessionAuth(path) || isContainerAppsStreamPath(path) {
		return false
	}
	hostname := strings.ToLower(r.Host)
	if i := strings.LastIndex(hostname, ":"); i >= 0 {
		hostname = hostname[:i]
	}
	for _, marker := range []string{".blob.", ".file.", ".queue.", ".table.", ".dfs.", ".web.", ".eventgrid.", ".documents."} {
		if strings.Contains(hostname, marker) {
			return false
		}
	}
	return !hasNonStorageAzureSubdomain(hostname)
}

// azureARMPathUsesSessionAuth reports whether an ARM-shaped path is actually a
// container exec/attach streaming channel that authenticates with its own
// per-session password rather than an ARM bearer. Real Azure serves these on a
// separate data-plane host (the ARM ExecuteCommand/Attach call — which does
// carry the ARM bearer — returns a webSocketUri + password for the channel);
// the collapsed-host simulator places the websocket under the ARM path, and its
// handler enforces the session password itself (see handleACIContainerExecSession
// / handleACIContainerAttachSession). Gating it on an ARM bearer would break the
// faithful two-step exec flow, so it is exempt here. The initiating POST
// .../exec and .../attach calls are ordinary ARM control-plane requests and stay
// gated.
func azureARMPathUsesSessionAuth(path string) bool {
	return strings.Contains(path, "/execSessions/") || strings.Contains(path, "/attachSessions/")
}

// azureTokenAudience returns the `aud` claim as a string. Per RFC 7519 `aud`
// may be a single string or an array of strings; the sim mints a single-string
// audience, so the string form is the common case and the first element is
// returned for the array form.
func azureTokenAudience(claims map[string]any) string {
	switch v := claims["aud"].(type) {
	case string:
		return v
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return s
			}
		}
	}
	return ""
}

// azureARMAudienceValid reports whether an `aud` claim authorizes an ARM
// request. It accepts the canonical management audiences and the simulator's
// own origin, which /metadata/endpoints advertises as an ARM audience
// (baseURL + "/").
func azureARMAudienceValid(aud string, r *http.Request) bool {
	if aud == "" {
		return false
	}
	if armTokenAudiences[aud] {
		return true
	}
	origin := azureAuthBaseURL(r)
	return aud == origin || aud == origin+"/"
}

// azureARMBearerUnauthorized writes real-ARM's 401 shape: a WWW-Authenticate
// Bearer challenge naming the failure plus an Azure error envelope. The error
// code invalid_token mirrors the sim's UserInfo endpoint (auth.go), and the
// description distinguishes missing token, bad signature, expiry, and wrong
// audience for the operator.
func azureARMBearerUnauthorized(w http.ResponseWriter, r *http.Request, description string) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(
		`Bearer authorization_uri=%q, error="invalid_token", error_description=%q`,
		azureAuthBaseURL(r)+"/common/oauth2/v2.0/authorize", description))
	AzureError(w, "invalid_token", description, http.StatusUnauthorized)
}
