package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// graphTokenAudiences are the audiences Microsoft Graph accepts: its
// resource URI, which a v1.0 token carries, and its application ID, which a
// v2.0 token carries.
var graphTokenAudiences = map[string]bool{
	"https://graph.microsoft.com":          true,
	"https://graph.microsoft.com/":         true,
	"00000003-0000-0000-c000-000000000000": true,
}

// graphBearerVerificationMiddleware authenticates every request a Microsoft
// Graph route serves, as Graph does: the request carries a bearer token the
// simulator's Microsoft Entra issued, unexpired, for Graph's audience. The
// simulator's own origin counts as that audience too, because
// /metadata/endpoints advertises it as microsoftGraphResourceId, and a client
// that reads its endpoints there asks for a token for it. A path no Graph route
// serves passes through to its own answer.
func graphBearerVerificationMiddleware(srv *sim.Server) func(http.Handler) http.Handler {
	mux := srv.Mux()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, pattern := mux.Handler(r); !isGraphRoutePattern(pattern) {
				next.ServeHTTP(w, r)
				return
			}
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")) == "" {
				graphUnauthorized(w, r, "Access token is empty.")
				return
			}
			claims, err := verifyAzureSimJWT(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")))
			if err != nil {
				graphUnauthorized(w, r, "Access token validation failure.")
				return
			}
			if aud := azureTokenAudience(claims); !graphTokenAudiences[aud] && !isSimOriginAudience(aud, r) {
				graphUnauthorized(w, r, "Access token validation failure. Invalid audience.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isGraphRoutePattern reports whether a mux pattern ("POST /v1.0/users") is
// one of the Microsoft Graph surface's: Graph serves its v1.0 and beta
// endpoints under those two path roots.
func isGraphRoutePattern(pattern string) bool {
	path := pattern
	if _, after, ok := strings.Cut(pattern, " "); ok {
		path = after
	}
	return strings.HasPrefix(path, "/v1.0/") || strings.HasPrefix(path, "/beta/")
}

func isSimOriginAudience(aud string, r *http.Request) bool {
	origin := azureAuthBaseURL(r)
	return aud != "" && (aud == origin || aud == origin+"/")
}

// graphUnauthorized writes Graph's 401: the InvalidAuthenticationToken error
// with its innerError, and the Bearer challenge naming Graph's application.
func graphUnauthorized(w http.ResponseWriter, r *http.Request, message string) {
	requestID := newGraphID()
	w.Header().Set("WWW-Authenticate", `Bearer realm="", authorization_uri="`+azureAuthBaseURL(r)+`/common/oauth2/v2.0/authorize", client_id="00000003-0000-0000-c000-000000000000"`)
	w.Header().Set("request-id", requestID)
	w.Header().Set("client-request-id", requestID)
	sim.WriteJSON(w, http.StatusUnauthorized, map[string]any{
		"error": map[string]any{
			"code":    "InvalidAuthenticationToken",
			"message": message,
			"innerError": map[string]string{
				"date":              time.Now().UTC().Format("2006-01-02T15:04:05"),
				"request-id":        requestID,
				"client-request-id": requestID,
			},
		},
	})
}
