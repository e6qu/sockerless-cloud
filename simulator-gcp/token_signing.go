package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/simjwt"
	uiauth "github.com/e6qu/sockerless-cloud/ui-auth"
)

// The simulator issues every data-plane access token — the OAuth2
// service-account token endpoint, the Security Token Service federated token
// exchange, the GCE metadata server token, and IAM Credentials
// generateAccessToken — from one signing key, and the data-plane bearer
// middleware verifies against that same key. Real Google access tokens are
// opaque strings Google introspects internally; the simulator plays the same
// role with a JWT it issues and verifies itself, so a token the simulator
// minted is the only kind its resource endpoints accept.
//
// Every token names Google's issuer, the one its identity tokens carry and its
// OpenID Connect discovery document publishes, so a relying party that checks
// an identity token's iss against the discovery document accepts it. The
// audience is the simulator's own: an access token is opaque to clients, so
// the audience is the contract between the simulator's minters and its
// verifier. The subject carries the minted principal (service-account email or
// federated workforce principal).
const (
	googleTokenIssuer      = "https://accounts.google.com"
	simAccessTokenAudience = "https://sockerless-sim.googleapis.com/"
)

// accessSigner is the process-stable RSA key every access-token minter signs
// with and the data-plane middleware verifies against. buildSimulator
// initialises it through initAccessTokenSigner; a nil accessSigner is a
// programming error rather than a runtime condition.
var accessSigner *simjwt.Signer

// accessTokenSigningKeyRecord is the persisted form of the simulator's
// access-token signing key: the RSA private key as PKCS#1 PEM. Reusing the
// key across restarts keeps pre-restart bearer tokens verifiable and the
// published JWKS `kid` stable, the way a real identity provider's signing key
// outlives any single server process.
type accessTokenSigningKeyRecord struct {
	PrivateKeyPEM string `json:"privateKeyPem"`
}

// accessTokenKeyStore keeps the record shape earlier releases persisted, so a
// SIM_PERSIST data directory keeps its key.
type accessTokenKeyStore struct {
	store sim.Store[accessTokenSigningKeyRecord]
}

func (s accessTokenKeyStore) Get(id string) (string, bool) {
	rec, ok := s.store.Get(id)
	return rec.PrivateKeyPEM, ok
}

func (s accessTokenKeyStore) Put(id, pemText string) {
	s.store.Put(id, accessTokenSigningKeyRecord{PrivateKeyPEM: pemText})
}

const accessTokenSigningKeyID = "access-token-signing-key"

// initAccessTokenSigner loads the simulator's access-token signing key from
// the persistence store, generating and persisting it on first boot. Without
// persistence (db nil) the key is process-lifetime, matching the rest of the
// in-memory state. A corrupt persisted key aborts startup rather than
// degrading to an unverifiable token.
func initAccessTokenSigner(db *sql.DB) error {
	if accessSigner != nil {
		return nil
	}
	signer, err := simjwt.LoadOrCreate(
		accessTokenKeyStore{sim.MakeStore[accessTokenSigningKeyRecord](db, "token_signing_keys")},
		accessTokenSigningKeyID, simjwt.RS256)
	if err != nil {
		return fmt.Errorf("access-token signing key: %w", err)
	}
	accessSigner = signer
	return nil
}

// signWithAccessKey signs a JWT with the simulator's access-token key. Every
// bearer the simulator issues — access tokens and the GCE metadata server's
// identity tokens — flows through here so all share one signing key and one
// JWKS.
func signWithAccessKey(claims map[string]any) string {
	if accessSigner == nil {
		panic("simulator access-token signer not initialised")
	}
	token, err := accessSigner.Sign(claims)
	if err != nil {
		panic(fmt.Sprintf("sign simulator token: %v", err))
	}
	return token
}

// signAccessToken issues an RS256 JWT access token for the given principal,
// signed with the simulator's access-token key. Every data-plane access token
// the simulator mints flows through here.
func signAccessToken(subject string, issuedAt, expiresAt time.Time) string {
	return signWithAccessKey(map[string]any{
		"iss":   googleTokenIssuer,
		"aud":   simAccessTokenAudience,
		"sub":   subject,
		"iat":   issuedAt.Unix(),
		"exp":   expiresAt.Unix(),
		"scope": "https://www.googleapis.com/auth/cloud-platform",
	})
}

// signIdentityToken issues the RS256 identity token the GCE metadata server's
// `service-accounts/{sa}/identity` endpoint hands a workload: the bearer it
// presents when invoking a Cloud Run service, whose single `aud` is the
// requested audience — the service URL. The subject and email are the
// workload's service-account email.
func signIdentityToken(subject, audience string, issuedAt, expiresAt time.Time) string {
	return signWithAccessKey(map[string]any{
		"iss":            googleTokenIssuer,
		"aud":            audience,
		"azp":            subject,
		"sub":            subject,
		"email":          subject,
		"email_verified": true,
		"iat":            issuedAt.Unix(),
		"exp":            expiresAt.Unix(),
	})
}

// signInvokeIDToken issues the RS256 ID token the OAuth2 token endpoint returns
// for a service-account JWT-bearer grant that requested one: a workload minting
// the bearer it presents to a Cloud Run service's URL. Its single `aud` is the
// assertion's target_audience, a string as Google spells it — the
// golang.org/x/oauth2 service-account flow decodes the returned id_token with
// a string-typed `aud`.
func signInvokeIDToken(subject, audience string, issuedAt, expiresAt time.Time) string {
	return signWithAccessKey(map[string]any{
		"iss":            googleTokenIssuer,
		"aud":            audience,
		"azp":            subject,
		"sub":            subject,
		"email":          subject,
		"email_verified": true,
		"iat":            issuedAt.Unix(),
		"exp":            expiresAt.Unix(),
	})
}

// signServiceAccountIDToken issues the RS256 ID token the IAM Credentials
// `serviceAccounts:generateIdToken` API returns for a service account. Real
// Google returns an RS256 JWT whose single `aud` claim is the caller-requested
// audience (the receiving service's URL) and which carries `email` /
// `email_verified` only when includeEmail is set; the simulator matches that
// shape and signs with the same key its data-plane middleware and JWKS use.
func signServiceAccountIDToken(subject, audience string, includeEmail bool, issuedAt, expiresAt time.Time) string {
	claims := map[string]any{
		"iss": googleTokenIssuer,
		"aud": audience,
		"azp": subject,
		"sub": subject,
		"iat": issuedAt.Unix(),
		"exp": expiresAt.Unix(),
	}
	if includeEmail {
		claims["email"] = subject
		claims["email_verified"] = true
	}
	return signWithAccessKey(claims)
}

// accessTokenClaims is the claim set of a simulator-minted access token, as
// returned by verifiedAccessTokenClaims for callers — the bearer middleware
// and the Security Token Service introspection endpoint — that need the
// token's contents after verification.
type accessTokenClaims struct {
	Iss   string `json:"iss"`
	Aud   any    `json:"aud"`
	Sub   string `json:"sub"`
	Scope string `json:"scope"`
	Exp   int64  `json:"exp"`
	Iat   int64  `json:"iat"`
}

// verifyAccessToken checks that a bearer token is one the simulator minted:
// a well-formed RS256 JWT, signed by the simulator's key, carrying the
// simulator's issuer and audience, and unexpired. It returns a descriptive
// error the middleware surfaces in the UNAUTHENTICATED response.
func verifyAccessToken(raw string) error {
	_, err := verifiedAccessTokenClaims(raw)
	return err
}

// verifiedAccessTokenClaims verifies a simulator-minted access token and
// returns its claims: a well-formed RS256 JWT, signed by the simulator's key,
// carrying the simulator's issuer and audience, and unexpired.
func verifiedAccessTokenClaims(raw string) (accessTokenClaims, error) {
	var claims accessTokenClaims
	if accessSigner == nil {
		return claims, fmt.Errorf("access-token signer not initialised")
	}
	err := simjwt.Verify(raw, &claims, simjwt.Options{
		Issuer:        googleTokenIssuer,
		Audience:      simAccessTokenAudience,
		RequireExpiry: true,
	}, accessSigner)
	if err == nil && workforceSessionRevoked(claims.Sub, claims.Iat) {
		err = fmt.Errorf("the session of %s was revoked", claims.Sub)
	}
	return claims, err
}

// registerTokenDiscovery publishes the OpenID Connect discovery document and
// JSON Web Key Set for the simulator's access-token signing key, the way a
// cloud identity provider publishes the keys a resource server verifies its
// tokens against. Both endpoints are unauthenticated (they are the material a
// caller needs before it can present a token) and are exempt from the bearer
// middleware.
func registerTokenDiscovery(srv *sim.Server) {
	srv.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		origin := requestOrigin(r)
		doc := simjwt.Discovery(googleTokenIssuer, origin+"/.well-known/jwks.json", []string{"token"}, accessSigner)
		doc["token_endpoint"] = origin + "/token"
		sim.WriteJSON(w, http.StatusOK, doc)
	})
	srv.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		if accessSigner == nil {
			GCPError(w, http.StatusInternalServerError, "access-token signer not initialised", "INTERNAL")
			return
		}
		sim.WriteJSON(w, http.StatusOK, simjwt.JWKS(accessSigner))
	})
}

// requestOrigin reconstructs the scheme://host base URL the request arrived on,
// the coordinate of the JWKS and token endpoints discovery publishes.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		scheme = forwarded
	}
	return scheme + "://" + r.Host
}

// bearerAuthMiddleware verifies the Authorization: Bearer access token on every
// data-plane request. Real Google APIs reject a request without a valid OAuth2
// access token with HTTP 401 UNAUTHENTICATED; the simulator does the same,
// verifying the token against the key it issued it with. Endpoints that a real
// Google client reaches without a bearer — the token minters, OpenID Connect
// discovery and JWKS, the health check, the GCE metadata server (gated by the
// Metadata-Flavor header instead), the console and its own authentication
// layer, and the OCI registry data plane (its own registry-token/Basic scheme)
// — are exempt.
//
// A URI no method publishes is answered before the credential is examined, the
// way Google's own API frontend does: `GET https://run.googleapis.com/nope`
// answers 404 anonymously while `GET .../v2/projects/…/services` answers 401.
// Authenticating first would make every unrouted path answer 401 and turn the
// absence of a route into an authentication failure, hiding both from a
// client — the simulator would report an endpoint it does not serve as one
// that merely rejected the caller.
func bearerAuthMiddleware(srv *sim.Server) func(http.Handler) http.Handler {
	mux := srv.Mux()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isAuthExempt(r) || !publishesAPIMethod(mux, r) {
				next.ServeHTTP(w, r)
				return
			}
			if !verifyRequestBearer(w, r) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// verifyRequestBearer checks the request's OAuth2 access token against the key
// the simulator issued it with, writing Google's UNAUTHENTICATED response and
// reporting false when it is missing or unverifiable. A host-addressed data
// plane that the API-method gate does not cover calls it itself, once it knows
// the request is one it serves.
func verifyRequestBearer(w http.ResponseWriter, r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		GCPError(w, http.StatusUnauthorized,
			"Request is missing required authentication credential. Expected OAuth 2 access token, login cookie or other valid authentication credential.",
			"UNAUTHENTICATED")
		return false
	}
	if err := verifyAccessToken(strings.TrimSpace(auth[len(prefix):])); err != nil {
		GCPError(w, http.StatusUnauthorized,
			"Invalid authentication credentials: "+err.Error(),
			"UNAUTHENTICATED")
		return false
	}
	return true
}

// gcpDataPlaneCatchAlls are the mount patterns of the simulator's
// host-addressed data planes: the Compute Engine load-balancer front end,
// which claims every path on a forwarding rule's address, and the Cloud
// Storage XML data plane, which claims every path whose first segment is a
// bucket. They match any URI, so a match on one is not evidence that a Google
// API method publishes it — the handler behind each decides from the Host or
// the bucket, and does its own authentication once it has.
var gcpDataPlaneCatchAlls = map[string]bool{
	"/{path...}":            true,
	"/{bucket}/{object...}": true,
}

// publishesAPIMethod reports whether a Google API method claims the request.
// Go's ServeMux answers an unmatched request with its own not-found handler and
// an empty pattern; a matched one names the pattern that will serve it.
func publishesAPIMethod(mux *http.ServeMux, r *http.Request) bool {
	_, pattern := mux.Handler(r)
	return pattern != "" && !gcpDataPlaneCatchAlls[pattern]
}

// isAuthExempt reports whether a request reaches a surface a real Google client
// contacts without an OAuth2 access token. Everything else is data plane and
// requires a verified bearer.
func isAuthExempt(r *http.Request) bool {
	p := r.URL.Path
	switch {
	case isCloudRunHost(r.Host):
		// A run.app request is for a Cloud Run workload, whose front end
		// authenticates the invoker itself.
		return true
	case p == "/health":
		return true
	case p == "/":
		return true
	case r.Method == http.MethodPost && (p == "/token" || p == "/oauth2/v4/token" || p == "/o/oauth2/token" || p == "/v1/token"):
		// Token minters: how a client obtains a token in the first place.
		return true
	case r.Method == http.MethodPost && p == "/v1/introspect":
		// Security Token Service token introspection authenticates with
		// OAuth client credentials (RFC 7662 §2.1) inside the handler — a
		// client introspects the very token it would otherwise present.
		return true
	case r.Method == http.MethodGet && (p == "/$discovery/rest" || strings.HasPrefix(p, "/discovery/v1/apis")):
		// Discovery documents and the Discovery service's directory are
		// public: the Discovery service's own document declares no scopes.
		return true
	case strings.HasPrefix(p, "/.well-known/"):
		// OpenID Connect discovery + JWKS.
		return true
	case strings.HasPrefix(p, "/computeMetadata/"):
		// GCE metadata server — gated by the Metadata-Flavor: Google header
		// inside the handlers, not by a bearer (matches real GCE).
		return true
	case strings.HasPrefix(p, "/ui/") || strings.HasPrefix(p, "/auth/"):
		// The console SPA and its own OpenID Connect authentication layer,
		// which use a server-side session, not a cloud access token.
		return true
	case p == uiauth.MonitoringPath:
		// Application monitoring has its own deployment-provided bearer. Its
		// handler validates that credential; treating it as a Google access
		// token prevents the handler from ever seeing a valid request.
		return true
	case isOCIRegistryPath(p):
		// The OCI Distribution data plane authenticates with its own
		// registry-token / Basic scheme, distinct from the cloud bearer.
		return true
	}
	return false
}

// isOCIRegistryPath reports whether a /v2/ path is served by the OCI
// Distribution registry rather than a Google control-plane API. Most of the
// simulator's /v2/ surface is real Google data plane (Cloud Run, Cloud
// Functions v2, Bigtable admin, Cloud Logging) under /v2/projects/... or
// /v2/entries...; only registry blob/manifest/tag traffic, the base endpoint
// and the Docker token service belong to the OCI registry. This mirrors the
// dispatch in the shared OCI registry's serve method, including its skip of
// /v2/projects/.
func isOCIRegistryPath(p string) bool {
	if p == "/v2" || p == "/v2/" || p == arTokenPath {
		return true
	}
	if !strings.HasPrefix(p, "/v2/") || strings.HasPrefix(p, "/v2/projects/") {
		return false
	}
	rest := strings.TrimPrefix(p, "/v2/")
	return strings.Contains(rest, "/blobs/") ||
		strings.Contains(rest, "/manifests/") ||
		strings.HasSuffix(rest, "/tags/list")
}
