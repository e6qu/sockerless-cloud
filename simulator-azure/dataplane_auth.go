package main

import (
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
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

// AzureBearerVerificationMiddleware enforces bearer-token authentication on the
// Azure Resource Manager plane exactly as real ARM does. Every request under
// /subscriptions/ or /providers/ (see isAzureARMPath) must carry an
// `Authorization: Bearer <jwt>` whose token this simulator minted (valid RS256
// signature against the sim's key, unexpired) and whose `aud` claim identifies
// the Azure Resource Manager audience. Requests to any other surface pass
// through untouched, because on real Azure they authenticate with their own
// scheme rather than an ARM bearer:
//
//   - OAuth2/OpenID endpoints (authorize, token, discovery, JWKS, userinfo) —
//     handled and returned by AzureAuthMiddleware, which wraps this middleware,
//     so they never reach it; they are also non-ARM paths regardless.
//   - The ARM cloud-metadata bootstrap /metadata/endpoints and IMDS
//     (/metadata/instance, /metadata/identity/oauth2/token, /msi/token) — the
//     provider/SDK fetches these before it holds any token; gated by
//     Metadata:true / IDENTITY_HEADER, never an ARM bearer.
//   - The ACR /v2/ registry data plane — its own registry-token scheme.
//   - Microsoft Graph (/v1.0, /v2.0) and the storage / Key Vault / Service Bus /
//     Event Grid data planes — reached on their own hostnames/paths with SAS,
//     shared-key, or a resource-scoped challenge bearer, none of which is the
//     ARM audience this middleware requires.
//   - /health and /ui/ — unauthenticated operator surfaces.
//
// None of those are ARM paths, so scoping to isAzureARMPath yields exactly the
// exemption set without any per-endpoint allowlist.
func AzureBearerVerificationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAzureARMPath(r.URL.Path) || azureARMPathUsesSessionAuth(r.URL.Path) {
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
			// verifyAzureSimJWT distinguishes the failure (malformed JWT, bad
			// signature, expired) so the description is faithful to the cause.
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
		next.ServeHTTP(w, r)
	})
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
	sim.AzureError(w, "invalid_token", description, http.StatusUnauthorized)
}
