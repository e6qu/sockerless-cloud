package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// azureSimSigningKey is the RS256 key used to sign Azure AD tokens the sim
// mints. Real Azure AD publishes public RSA signing keys via JWKS and keeps
// them stable across service restarts; the simulator does the same — the key
// PEM is persisted in azureAuthSigningKeys on first use, so bearers minted
// before a restart still verify against the JWKS afterwards.
//
// Authorization codes stay in a plain map: they live for 60 seconds and are
// consumed exactly once, so they are genuinely transient process state.
var (
	azureSimSigningKeyOnce sync.Once
	azureSimSigningKeyVal  *rsa.PrivateKey
	azureSimSigningKeyErr  error

	azureAuthCodeMu    sync.Mutex
	azureAuthCodeStore = map[string]azureAuthCode{}

	// azureAuthSigningKeys and azureRefreshTokens default to in-memory
	// stores (the same behavior MakeStore has without a database) so
	// key minting and grant flows work in isolation; registerAzureAuthState
	// rebinds them to the server's database before any request is served.
	azureAuthSigningKeys sim.Store[string]            = sim.NewStateStore[string]()
	azureRefreshTokens   sim.Store[azureRefreshToken] = sim.NewStateStore[azureRefreshToken]()
)

// azureSigningKeyStoreID is the row the durable RS256 signing-key PEM lives
// under in azureAuthSigningKeys.
const azureSigningKeyStoreID = "signing-key"

// registerAzureAuthState binds the token-issuance state — the RS256 signing
// key and issued refresh tokens — to the server's database so both survive a
// SIM_PERSIST restart. Called from registerEntra, before the server accepts
// requests.
func registerAzureAuthState(srv *sim.Server) {
	azureAuthSigningKeys = sim.MakeStore[string](srv.DB(), "azure_auth_signing_keys")
	azureRefreshTokens = sim.MakeStore[azureRefreshToken](srv.DB(), "azure_auth_refresh_tokens")
}

const defaultAzureTokenAudience = "https://management.azure.com/"
const azureAuthCodeTTL = time.Minute

type azureAuthCode struct {
	TenantID            string
	ClientID            string
	RedirectURI         string
	Scope               string
	Nonce               string
	CodeChallenge       string
	CodeChallengeMethod string
	// UserOID binds the code to a directory user when the authorize request
	// carried a login_hint; empty means the grant mints tokens for the
	// directory's built-in default identity.
	UserOID   string
	ExpiresAt time.Time
}

type azureRefreshToken struct {
	TenantID string
	ClientID string
	Scope    string
	Nonce    string
	// UserOID carries the user binding of the originating grant so refreshed
	// tokens keep minting for the same user.
	UserOID string
}

var azureScopeAudienceOverrides = map[string]string{
	"https://management.azure.com": defaultAzureTokenAudience,
	"https://storage.azure.com":    "https://storage.azure.com/",
}

func azureSimSigningKey() (*rsa.PrivateKey, error) {
	azureSimSigningKeyOnce.Do(func() {
		azureSimSigningKeyVal, azureSimSigningKeyErr = loadOrCreateAzureSimSigningKey()
	})
	return azureSimSigningKeyVal, azureSimSigningKeyErr
}

// loadOrCreateAzureSimSigningKey returns the durable RS256 signing key: the
// PEM persisted in azureAuthSigningKeys when one exists, otherwise a freshly
// generated key that is persisted for subsequent boots. A stored PEM that no
// longer parses is corrupt simulator state and fails loudly — regenerating
// would silently invalidate every outstanding bearer.
func loadOrCreateAzureSimSigningKey() (*rsa.PrivateKey, error) {
	if pemText, ok := azureAuthSigningKeys.Get(azureSigningKeyStoreID); ok {
		block, _ := pem.Decode([]byte(pemText))
		if block == nil || block.Type != "RSA PRIVATE KEY" {
			return nil, fmt.Errorf("persisted Azure simulator signing key is not an RSA PRIVATE KEY PEM block")
		}
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse persisted Azure simulator signing key: %w", err)
		}
		return key, nil
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	azureAuthSigningKeys.Put(azureSigningKeyStoreID, string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})))
	return key, nil
}

// CleanPathMiddleware removes double slashes from request paths.
// The azurerm v3 provider (go-azure-sdk) constructs URLs by joining
// the resourceManager endpoint (with trailing slash) and the resource path
// (with leading slash), producing "//subscriptions/..." paths. Go's default
// mux 301-redirects these, which changes PUT→GET and breaks creates.
func CleanPathMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for strings.Contains(r.URL.Path, "//") {
			r.URL.Path = strings.ReplaceAll(r.URL.Path, "//", "/")
		}
		if r.URL.RawPath != "" {
			for strings.Contains(r.URL.RawPath, "//") {
				r.URL.RawPath = strings.ReplaceAll(r.URL.RawPath, "//", "/")
			}
		}
		next.ServeHTTP(w, r)
	})
}

// AzureARMAPIVersionMiddleware enforces the ARM control-plane api-version
// contract. Azure data planes and metadata endpoints have their own versioning
// rules, so only ARM resource paths are checked here.
func AzureARMAPIVersionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAzureARMPath(r.URL.Path) && r.URL.Query().Get("api-version") == "" {
			sim.AzureError(w, "InvalidApiVersionParameter",
				"The api-version query parameter (?api-version=) is required for all requests.",
				http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isAzureARMPath(path string) bool {
	return strings.HasPrefix(path, "/subscriptions/") || strings.HasPrefix(path, "/providers/")
}

// AzureAuthMiddleware intercepts OAuth2 and OpenID discovery requests needed
// by the Azure SDK for authentication. This is implemented as middleware
// rather than registered routes to avoid conflicts with ACR's /v2/{path...}.
func AzureAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Instance discovery: GET /common/discovery/instance. Entra serves this
		// only under /common — a tenant-scoped spelling is a 404 there — so the
		// match is exact rather than a suffix.
		if r.Method == http.MethodGet && path == "/common/discovery/instance" {
			handleAzureInstanceDiscovery(w, r)
			return
		}

		// Authorization endpoint: GET /{tenantId}/oauth2/v2.0/authorize
		if r.Method == http.MethodGet && strings.Contains(path, "/oauth2/v2.0/authorize") {
			handleAzureAuthorize(w, r, path)
			return
		}

		// Token endpoint: POST /{tenantId}/oauth2/v2.0/token
		if r.Method == http.MethodPost && strings.Contains(path, "/oauth2/v2.0/token") {
			handleAzureToken(w, r, path)
			return
		}
		// Token endpoint v1: POST /{tenantId}/oauth2/token. The Azure AD v1 endpoint
		// always carries a tenant prefix; the bare /oauth2/token (and /oauth2/exchange)
		// are ACR's registry-token endpoints, which must fall through to the ACR mux
		// routes rather than be handled as an AAD token request.
		if r.Method == http.MethodPost && strings.Contains(path, "/oauth2/token") &&
			path != "/oauth2/token" && path != "/oauth2/exchange" {
			handleAzureToken(w, r, path)
			return
		}

		// OpenID discovery endpoints
		if r.Method == http.MethodGet && strings.HasSuffix(path, "/.well-known/openid-configuration") {
			tenantId := extractTenantFromPath(path)
			baseURL := azureAuthBaseURL(r)
			isV2 := strings.Contains(path, "/v2.0/.well-known/")
			// The issuer is version-aware. For the v2.0 discovery path
			// (`/{tenant}/v2.0/.well-known/...`) it MUST equal the URL the
			// document was fetched from (RFC 8414 §3) so strict OIDC clients
			// (coreos/go-oidc, Pomerium) validate it. For the v1 path the real
			// AAD issuer is `sts.windows.net/{tenant}/`, which azidentity / the
			// Azure SDK compare against (they don't dereference it as a URL).
			// userinfo_endpoint is sim-hosted (real AAD points at Graph, which the
			// sim doesn't proxy) so coreos/go-oidc's provider.UserInfo() — called
			// by Pomerium after the token exchange — can actually resolve it.
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"issuer":                                azureIssuer(baseURL, tenantId, isV2),
				"authorization_endpoint":                fmt.Sprintf("%s/%s/oauth2/v2.0/authorize", baseURL, tenantId),
				"token_endpoint":                        fmt.Sprintf("%s/%s/oauth2/v2.0/token", baseURL, tenantId),
				"userinfo_endpoint":                     azureUserInfoEndpoint(baseURL, tenantId, isV2),
				"jwks_uri":                              fmt.Sprintf("%s/%s/discovery/v2.0/keys", baseURL, tenantId),
				"response_types_supported":              []string{"code"},
				"response_modes_supported":              []string{"query", "fragment", "form_post"},
				"grant_types_supported":                 []string{"authorization_code", "client_credentials", "refresh_token", "password"},
				"code_challenge_methods_supported":      []string{"plain", "S256"},
				"id_token_signing_alg_values_supported": []string{"RS256"},
				"token_endpoint_auth_methods_supported": []string{"client_secret_post", "client_secret_basic"},
				"subject_types_supported":               []string{"pairwise"},
				"scopes_supported":                      []string{"openid", "profile", "email", "offline_access"},
				"claims_supported":                      []string{"aud", "exp", "groups", "iat", "iss", "name", "nonce", "oid", "preferred_username", "sub", "tid", "ver"},
				"request_uri_parameter_supported":       false,
			})
			return
		}

		// JWKS endpoint — publish the public key that verifies freshly
		// minted RS256 tokens, matching Azure AD's verifier contract.
		if r.Method == http.MethodGet && (strings.HasSuffix(path, "/discovery/v2.0/keys") || strings.HasSuffix(path, "/discovery/keys")) {
			jwk, err := azureSimJWK()
			if err != nil {
				sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"keys": []map[string]any{jwk}})
			return
		}

		// UserInfo endpoint (OIDC). Pomerium / coreos/go-oidc call this after the
		// token exchange. Returns the standard UserInfo claims for the bearer
		// token's user.
		if r.Method == http.MethodGet && strings.HasSuffix(path, "/userinfo") {
			handleAzureUserInfo(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func azureAuthBaseURL(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

func handleAzureAuthorize(w http.ResponseWriter, r *http.Request, path string) {
	tenantID := extractTenantFromPath(path)
	q := r.URL.Query()
	clientID := strings.TrimSpace(q.Get("client_id"))
	redirectURI := strings.TrimSpace(q.Get("redirect_uri"))
	responseType := strings.TrimSpace(q.Get("response_type"))
	scope := strings.TrimSpace(q.Get("scope"))
	state := q.Get("state")

	if clientID == "" {
		azureOAuthError(w, "invalid_request", "client_id is required", http.StatusBadRequest)
		return
	}
	if redirectURI == "" {
		azureOAuthError(w, "invalid_request", "redirect_uri is required", http.StatusBadRequest)
		return
	}
	if responseType != "code" {
		redirectAzureAuthError(w, redirectURI, q.Get("response_mode"), state, "unsupported_response_type", "response_type must be code")
		return
	}
	if scope == "" {
		redirectAzureAuthError(w, redirectURI, q.Get("response_mode"), state, "invalid_request", "scope is required")
		return
	}

	codeChallenge := strings.TrimSpace(q.Get("code_challenge"))
	codeChallengeMethod := strings.TrimSpace(q.Get("code_challenge_method"))
	if codeChallengeMethod != "" && codeChallenge == "" {
		redirectAzureAuthError(w, redirectURI, q.Get("response_mode"), state, "invalid_request", "code_challenge is required when code_challenge_method is set")
		return
	}
	if codeChallengeMethod == "" && codeChallenge != "" {
		codeChallengeMethod = "plain"
	}
	if codeChallengeMethod != "" && codeChallengeMethod != "plain" && codeChallengeMethod != "S256" {
		redirectAzureAuthError(w, redirectURI, q.Get("response_mode"), state, "invalid_request", "code_challenge_method must be plain or S256")
		return
	}

	// login_hint binds the authorization code to a directory user, resolved
	// by userPrincipalName exactly like the ROPC (password) grant. The sim
	// has no interactive login page, so an unresolvable hint cannot show the
	// account picker real AAD would; it answers with AAD's
	// interaction-required semantics (error=login_required) instead of
	// silently minting tokens for a different user.
	userOID := ""
	if loginHint := strings.TrimSpace(q.Get("login_hint")); loginHint != "" {
		u, ok := findEntraUserByUPN(loginHint)
		if !ok {
			redirectAzureAuthError(w, redirectURI, q.Get("response_mode"), state, "login_required",
				fmt.Sprintf("AADSTS50058: A silent sign-in request was sent but no user is signed in. The login_hint '%s' does not match any user in the directory; provision the user first (POST /v1.0/users).", loginHint))
			return
		}
		userOID = u.OID
	}

	code, err := newAzureAuthorizationCode()
	if err != nil {
		sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	azureAuthCodeMu.Lock()
	azureAuthCodeStore[code] = azureAuthCode{
		TenantID:            tenantID,
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		Scope:               scope,
		Nonce:               q.Get("nonce"),
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		UserOID:             userOID,
		ExpiresAt:           time.Now().Add(azureAuthCodeTTL),
	}
	azureAuthCodeMu.Unlock()

	values := url.Values{"code": {code}}
	if state != "" {
		values.Set("state", state)
	}
	redirectAzureAuthorizeResponse(w, redirectURI, q.Get("response_mode"), values)
}

func newAzureAuthorizationCode() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate authorization code: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// azureIssuer returns the OIDC issuer for a tenant. The v2.0 issuer is the
// sim's own discovery base (`<baseURL>/{tenant}/v2.0`) — it must match the URL
// strict OIDC clients fetch the discovery document from (RFC 8414 §3, issue
// #504). The v1 issuer is the real AAD `sts.windows.net/{tenant}/` value the
// Azure SDK expects.
func azureIssuer(baseURL, tenant string, v2 bool) string {
	if v2 {
		return fmt.Sprintf("%s/%s/v2.0", baseURL, tenant)
	}
	return fmt.Sprintf("https://sts.windows.net/%s/", tenant)
}

// azureUserInfoEndpoint is sim-hosted so OIDC clients can resolve it (real AAD
// points at graph.microsoft.com/oidc/userinfo, which the sim doesn't proxy).
func azureUserInfoEndpoint(baseURL, tenant string, v2 bool) string {
	if v2 {
		return fmt.Sprintf("%s/%s/v2.0/userinfo", baseURL, tenant)
	}
	return fmt.Sprintf("%s/%s/userinfo", baseURL, tenant)
}

// verifyAzureSimJWT verifies a sim-minted RS256 JWT against the sim's signing
// key (the same key published at the JWKS endpoint) and returns its claims. It
// rejects malformed tokens, bad signatures, and expired tokens — there is no
// fallback to an unauthenticated identity.
func verifyAzureSimJWT(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed JWT")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}
	key, err := azureSimSigningKey()
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		return nil, fmt.Errorf("signature verification failed")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}
	if exp, ok := claims["exp"].(float64); ok && time.Now().Unix() > int64(exp) {
		return nil, fmt.Errorf("token expired")
	}
	return claims, nil
}

// handleAzureUserInfo serves the OIDC UserInfo endpoint. Per OpenID Connect Core
// §5.3 it REQUIRES a valid bearer access token and returns 401 with a
// WWW-Authenticate header otherwise — no fallback to a default identity. The
// returned `sub`/`oid` come from the verified token (authoritative); profile
// claims (name/email) come from the token's user.
func handleAzureUserInfo(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", error_description="missing bearer token"`)
		sim.AzureError(w, "invalid_token", "The UserInfo endpoint requires a bearer access token", http.StatusUnauthorized)
		return
	}
	claims, err := verifyAzureSimJWT(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")))
	if err != nil {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer error="invalid_token", error_description=%q`, err.Error()))
		sim.AzureError(w, "invalid_token", fmt.Sprintf("invalid access token: %v", err), http.StatusUnauthorized)
		return
	}
	sub, _ := claims["sub"].(string)
	oid, _ := claims["oid"].(string)
	if sub == "" {
		sim.AzureError(w, "invalid_token", "access token has no sub claim", http.StatusUnauthorized)
		return
	}
	u := getEntraSimUser(oid)
	out := map[string]any{"sub": sub, "oid": oid}
	if u.Name != "" {
		out["name"] = u.Name
	}
	if u.PreferredUsername != "" {
		out["preferred_username"] = u.PreferredUsername
	}
	if u.Email != "" {
		out["email"] = u.Email
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func extractTenantFromPath(path string) string {
	// Path is like /{tenantId}/v2.0/.well-known/openid-configuration
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return "unknown"
}

func handleAzureToken(w http.ResponseWriter, r *http.Request, path string) {
	tenantId := extractTenantFromPath(path)
	if err := r.ParseForm(); err != nil {
		azureOAuthError(w, "invalid_request", fmt.Sprintf("parse Azure token request form: %v", err), http.StatusBadRequest)
		return
	}
	clientID, err := azureTokenClientID(r)
	if err != nil {
		azureOAuthError(w, "invalid_request", err.Error(), http.StatusBadRequest)
		return
	}
	if r.Form.Get("grant_type") == "authorization_code" {
		handleAzureAuthorizationCodeToken(w, r, tenantId, clientID)
		return
	}
	if r.Form.Get("grant_type") == "refresh_token" {
		handleAzureRefreshToken(w, r, tenantId, clientID)
		return
	}
	if r.Form.Get("grant_type") == "password" {
		handleAzureROPC(w, r, tenantId, clientID)
		return
	}
	if grantType := r.Form.Get("grant_type"); grantType != "" && grantType != "client_credentials" {
		azureOAuthError(w, "unsupported_grant_type", "grant_type is unsupported", http.StatusBadRequest)
		return
	}

	// Workload Identity Federation: a client_credentials request carrying a
	// JWT-bearer client_assertion is exchanged against the client's federated
	// identity credentials rather than authenticated with a secret.
	if handleAzureFederatedClientCredentials(w, r, tenantId, clientID) {
		return
	}

	// A client_id registered as an application in the directory is a real
	// confidential client: its client_secret must validate against the app
	// registration's password credentials, exactly as Microsoft Entra
	// validates the client_credentials grant.
	if app, ok := entraFindApplicationByAppID(clientID); ok {
		handleAzureRegisteredAppClientCredentials(w, r, tenantId, app)
		return
	}

	// A client_id the directory holds no application registration for is not
	// a confidential client Microsoft Entra recognizes for this tenant — real
	// Microsoft Entra rejects it before ever inspecting a client_secret.
	azureOAuthError(w, "unauthorized_client",
		fmt.Sprintf("AADSTS700016: Application with identifier '%s' was not found in the directory '%s'. This can happen if the application has not been installed by the administrator of the tenant or consented to by any user in the tenant. You may have sent your authentication request to the wrong tenant.", clientID, tenantId),
		http.StatusBadRequest)
}

// azureTokenClientID resolves the OAuth2 client identity for a token request.
// Real AAD v2.0 accepts client_secret_post (client_id/client_secret form
// fields) and client_secret_basic (RFC 6749 §2.3.1: Authorization: Basic
// base64(urlencoded(client_id) + ":" + urlencoded(client_secret))). A request
// carrying both mechanisms with different client_ids is rejected — RFC 6749
// §2.3 forbids using more than one client authentication method per request.
func azureTokenClientID(r *http.Request) (string, error) {
	formClientID := strings.TrimSpace(r.Form.Get("client_id"))
	basicClientID, _, hasBasic, err := azureBasicAuthClient(r)
	if err != nil {
		return "", err
	}
	if !hasBasic {
		return formClientID, nil
	}
	if formClientID != "" && formClientID != basicClientID {
		return "", fmt.Errorf("request must not use more than one client authentication mechanism: form client_id %q does not match Basic authorization client_id %q", formClientID, basicClientID)
	}
	return basicClientID, nil
}

// azureTokenClientSecret resolves the client_secret the request presents,
// from whichever of the two client-authentication mechanisms carries it.
func azureTokenClientSecret(r *http.Request) (string, error) {
	_, basicSecret, hasBasic, err := azureBasicAuthClient(r)
	if err != nil {
		return "", err
	}
	if hasBasic {
		return basicSecret, nil
	}
	return strings.TrimSpace(r.Form.Get("client_secret")), nil
}

// azureBasicAuthClient extracts the client_id and client_secret from an HTTP
// Basic Authorization header. The components are form-urlencoded before base64
// per RFC 6749 §2.3.1, so they are unescaped here; the raw value is kept when
// unescaping fails because common clients (MSAL, Auth.js) send unreserved
// characters without encoding them.
func azureBasicAuthClient(r *http.Request) (clientID, clientSecret string, has bool, err error) {
	const prefix = "Basic "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		return "", "", false, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(auth[len(prefix):]))
	if err != nil {
		return "", "", false, fmt.Errorf("decode Basic authorization header: %v", err)
	}
	clientID, clientSecret, found := strings.Cut(string(raw), ":")
	if !found {
		return "", "", false, fmt.Errorf("basic authorization header must decode to client_id:client_secret")
	}
	if unescaped, err := url.QueryUnescape(clientID); err == nil {
		clientID = unescaped
	}
	if unescaped, err := url.QueryUnescape(clientSecret); err == nil {
		clientSecret = unescaped
	}
	if clientID == "" {
		return "", "", false, fmt.Errorf("basic authorization header carries an empty client_id")
	}
	return clientID, clientSecret, true, nil
}

// handleAzureRegisteredAppClientCredentials serves the client_credentials
// grant for a client_id the directory holds an application registration for.
// Real Microsoft Entra authenticates the client against the app registration's
// password credentials, requires a service principal materializing the
// application in the tenant, and issues an app-only token whose oid and sub
// are the service principal's object ID.
func handleAzureRegisteredAppClientCredentials(w http.ResponseWriter, r *http.Request, tenantID string, app EntraApplication) {
	secret, err := azureTokenClientSecret(r)
	if err != nil {
		azureOAuthError(w, "invalid_request", err.Error(), http.StatusBadRequest)
		return
	}
	if secret == "" {
		azureOAuthError(w, "invalid_client",
			fmt.Sprintf("AADSTS7000218: The request body must contain the following parameter: 'client_secret' or 'client_assertion'. Application: %s.", app.AppID),
			http.StatusUnauthorized)
		return
	}
	if !entraClientSecretMatches(app, secret) {
		azureOAuthError(w, "invalid_client",
			fmt.Sprintf("AADSTS7000215: Invalid client secret provided. Ensure the secret being sent in the request is the client secret value, not the client secret ID, for a secret added to app %q.", app.AppID),
			http.StatusUnauthorized)
		return
	}
	sp, ok := entraFindServicePrincipalByAppID(app.AppID)
	if !ok {
		azureOAuthError(w, "unauthorized_client",
			fmt.Sprintf("AADSTS700016: Application with identifier '%s' was not found in the directory. A service principal must exist for the application in this tenant (POST /v1.0/servicePrincipals).", app.AppID),
			http.StatusBadRequest)
		return
	}
	audience, err := azureTokenAudienceFromRequest(r)
	if err != nil {
		sim.AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return
	}
	now := time.Now()
	token, err := mintAzureSimSignedJWT(map[string]any{
		"tid":   tenantID,
		"oid":   sp.ID,
		"sub":   sp.ID,
		"aud":   audience,
		"iss":   fmt.Sprintf("https://sts.windows.net/%s/", tenantID),
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
		"nbf":   now.Unix(),
		"ver":   "1.0",
		"appid": app.AppID,
		"idtyp": "app",
	})
	if err != nil {
		sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token":   token,
		"token_type":     "Bearer",
		"expires_in":     3600,
		"ext_expires_in": 3600,
	})
}

// azureGrantUser resolves the directory user a grant mints tokens for. An
// empty oid means the grant is not bound to a user (no login_hint on the
// authorize request) and the directory's built-in default identity applies.
func azureGrantUser(oid string) (EntraUser, error) {
	if oid == "" {
		return entraDefaultUser, nil
	}
	if u, ok := entraUsersStore.Get(oid); ok {
		return u, nil
	}
	if oid == entraDefaultUser.OID {
		return entraDefaultUser, nil
	}
	return EntraUser{}, fmt.Errorf("the user this grant was issued for no longer exists")
}

func handleAzureAuthorizationCodeToken(w http.ResponseWriter, r *http.Request, tenantID, clientID string) {
	code := strings.TrimSpace(r.Form.Get("code"))
	redirectURI := strings.TrimSpace(r.Form.Get("redirect_uri"))
	if code == "" {
		azureOAuthError(w, "invalid_request", "code is required", http.StatusBadRequest)
		return
	}
	if clientID == "" {
		azureOAuthError(w, "invalid_request", "client_id is required", http.StatusBadRequest)
		return
	}
	if redirectURI == "" {
		azureOAuthError(w, "invalid_request", "redirect_uri is required", http.StatusBadRequest)
		return
	}

	authCode, ok := consumeAzureAuthorizationCode(code)
	if !ok {
		azureOAuthError(w, "invalid_grant", "authorization code is invalid or expired", http.StatusBadRequest)
		return
	}
	if authCode.TenantID != tenantID || authCode.ClientID != clientID || authCode.RedirectURI != redirectURI {
		azureOAuthError(w, "invalid_grant", "authorization code was issued for a different client or redirect URI", http.StatusBadRequest)
		return
	}
	if err := validateAzurePKCE(authCode, r.Form.Get("code_verifier")); err != nil {
		azureOAuthError(w, "invalid_grant", err.Error(), http.StatusBadRequest)
		return
	}
	user, err := azureGrantUser(authCode.UserOID)
	if err != nil {
		azureOAuthError(w, "invalid_grant", err.Error(), http.StatusBadRequest)
		return
	}

	scope := strings.TrimSpace(r.Form.Get("scope"))
	if scope == "" {
		scope = authCode.Scope
	}
	now := time.Now()
	accessToken, err := mintAzureSimJWTForUser(user, tenantID, azureAudienceFromScope(scope), now, now.Add(time.Hour))
	if err != nil {
		sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	body := map[string]any{
		"access_token":   accessToken,
		"token_type":     "Bearer",
		"expires_in":     3600,
		"ext_expires_in": 3600,
		"scope":          scope,
	}
	if azureScopeIncludes(scope, "openid") {
		issuer := azureIssuer(azureAuthBaseURL(r), tenantID, true)
		idToken, err := mintAzureSimIDTokenForUser(user, tenantID, clientID, authCode.Nonce, scope, issuer, now, now.Add(time.Hour))
		if err != nil {
			sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
			return
		}
		body["id_token"] = idToken
	}
	if azureScopeIncludes(scope, "offline_access") {
		refreshToken, err := newAzureAuthorizationCode()
		if err != nil {
			sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
			return
		}
		azureRefreshTokens.Put(refreshToken, azureRefreshToken{
			TenantID: tenantID,
			ClientID: clientID,
			Scope:    scope,
			Nonce:    authCode.Nonce,
			UserOID:  authCode.UserOID,
		})
		body["refresh_token"] = refreshToken
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleAzureRefreshToken(w http.ResponseWriter, r *http.Request, tenantID, clientID string) {
	refreshToken := strings.TrimSpace(r.Form.Get("refresh_token"))
	if refreshToken == "" {
		azureOAuthError(w, "invalid_request", "refresh_token is required", http.StatusBadRequest)
		return
	}
	if clientID == "" {
		azureOAuthError(w, "invalid_request", "client_id is required", http.StatusBadRequest)
		return
	}

	stored, ok := lookupAzureRefreshToken(refreshToken)
	if !ok || stored.TenantID != tenantID || stored.ClientID != clientID {
		azureOAuthError(w, "invalid_grant", "refresh token is invalid", http.StatusBadRequest)
		return
	}
	user, err := azureGrantUser(stored.UserOID)
	if err != nil {
		azureOAuthError(w, "invalid_grant", err.Error(), http.StatusBadRequest)
		return
	}
	scope := strings.TrimSpace(r.Form.Get("scope"))
	if scope == "" {
		scope = stored.Scope
	}
	now := time.Now()
	accessToken, err := mintAzureSimJWTForUser(user, tenantID, azureAudienceFromScope(scope), now, now.Add(time.Hour))
	if err != nil {
		sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	body := map[string]any{
		"access_token":   accessToken,
		"token_type":     "Bearer",
		"expires_in":     3600,
		"ext_expires_in": 3600,
		"scope":          scope,
		"refresh_token":  refreshToken,
	}
	if azureScopeIncludes(scope, "openid") {
		issuer := azureIssuer(azureAuthBaseURL(r), tenantID, true)
		idToken, err := mintAzureSimIDTokenForUser(user, tenantID, clientID, stored.Nonce, scope, issuer, now, now.Add(time.Hour))
		if err != nil {
			sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
			return
		}
		body["id_token"] = idToken
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

func lookupAzureRefreshToken(refreshToken string) (azureRefreshToken, bool) {
	return azureRefreshTokens.Get(refreshToken)
}

func consumeAzureAuthorizationCode(code string) (azureAuthCode, bool) {
	azureAuthCodeMu.Lock()
	defer azureAuthCodeMu.Unlock()
	authCode, ok := azureAuthCodeStore[code]
	if !ok {
		return azureAuthCode{}, false
	}
	delete(azureAuthCodeStore, code)
	if time.Now().After(authCode.ExpiresAt) {
		return azureAuthCode{}, false
	}
	return authCode, true
}

func validateAzurePKCE(authCode azureAuthCode, verifier string) error {
	if authCode.CodeChallenge == "" {
		return nil
	}
	if verifier == "" {
		return fmt.Errorf("code_verifier is required")
	}
	switch authCode.CodeChallengeMethod {
	case "", "plain":
		if verifier != authCode.CodeChallenge {
			return fmt.Errorf("code_verifier does not match code_challenge")
		}
	case "S256":
		digest := sha256.Sum256([]byte(verifier))
		if base64.RawURLEncoding.EncodeToString(digest[:]) != authCode.CodeChallenge {
			return fmt.Errorf("code_verifier does not match code_challenge")
		}
	default:
		return fmt.Errorf("code_challenge_method is unsupported")
	}
	return nil
}

func azureTokenAudienceFromRequest(r *http.Request) (string, error) {
	if r.Form == nil {
		if err := r.ParseForm(); err != nil {
			return "", fmt.Errorf("parse Azure token request form: %w", err)
		}
	}
	return azureTokenAudienceFromForm(r.Form), nil
}

func azureTokenAudienceFromForm(form url.Values) string {
	if scope := strings.TrimSpace(form.Get("scope")); scope != "" {
		return azureAudienceFromScope(scope)
	}
	if resource := strings.TrimSpace(form.Get("resource")); resource != "" {
		return resource
	}
	return defaultAzureTokenAudience
}

func azureAudienceFromScope(scope string) string {
	fields := strings.Fields(scope)
	for _, field := range fields {
		if azureScopeIsOIDC(field) {
			continue
		}
		audience := strings.TrimSuffix(field, "/.default")
		if idx := strings.LastIndex(audience, "/"); idx > len("https://") {
			audience = audience[:idx]
		}
		if override, ok := azureScopeAudienceOverrides[audience]; ok {
			return override
		}
		return audience
	}
	return defaultAzureTokenAudience
}

func azureScopeIncludes(scope, want string) bool {
	for _, field := range strings.Fields(scope) {
		if field == want {
			return true
		}
	}
	return false
}

func azureScopeIsOIDC(scope string) bool {
	switch scope {
	case "openid", "profile", "email", "offline_access":
		return true
	default:
		return false
	}
}

// mintAzureSimJWTForUser produces a real-shape Azure AD access token JWT for
// a specific user.
func mintAzureSimJWTForUser(u EntraUser, tenantId, audience string, issuedAt, expiresAt time.Time) (string, error) {
	return mintAzureSimSignedJWT(map[string]any{
		"tid":   tenantId,
		"oid":   u.OID,
		"sub":   u.Sub,
		"aud":   audience,
		"iss":   fmt.Sprintf("https://sts.windows.net/%s/", tenantId),
		"iat":   issuedAt.Unix(),
		"exp":   expiresAt.Unix(),
		"nbf":   issuedAt.Unix(),
		"ver":   "1.0",
		"appid": "sockerless-sim",
	})
}

// mintAzureSimIDTokenForUser produces a real-shape Azure AD id_token for a
// specific user. Groups are resolved from the directory's membership store.
func mintAzureSimIDTokenForUser(u EntraUser, tenantID, clientID, nonce, scope, issuer string, issuedAt, expiresAt time.Time) (string, error) {
	email := u.Email
	if email == "" {
		email = u.PreferredUsername
	}
	claims := map[string]any{
		"tid": tenantID,
		"oid": u.OID,
		"sub": u.Sub,
		"aud": clientID,
		// v2.0 id_token: iss is the v2.0 issuer and must equal the discovery
		// document's `issuer` so OIDC clients (coreos/go-oidc) validate it.
		"iss":                issuer,
		"iat":                issuedAt.Unix(),
		"exp":                expiresAt.Unix(),
		"nbf":                issuedAt.Unix(),
		"ver":                "2.0",
		"name":               u.Name,
		"preferred_username": u.PreferredUsername,
	}

	memberships := entraGroupMembershipStore.Filter(func(m entraGroupMembership) bool {
		return m.MemberID == u.OID
	})
	if len(memberships) > 0 {
		groupIDSet := map[string]bool{}
		for _, m := range memberships {
			groupIDSet[m.GroupID] = true
		}
		groupIDs := make([]string, 0, len(groupIDSet))
		for id := range groupIDSet {
			groupIDs = append(groupIDs, id)
		}
		claims["groups"] = groupIDs
	}

	if nonce != "" {
		claims["nonce"] = nonce
	}
	if azureScopeIncludes(scope, "email") {
		claims["email"] = email
	}
	return mintAzureSimSignedJWT(claims)
}

// handleAzureROPC implements the Resource Owner Password Credentials grant
// (grant_type=password). Real Entra supports this for non-interactive test
// flows where a specific user's id_token is needed without a browser.
// The sim looks up the user by userPrincipalName (the username field) and
// mints tokens carrying that user's identity and group memberships.
func handleAzureROPC(w http.ResponseWriter, r *http.Request, tenantID, clientID string) {
	username := strings.TrimSpace(r.Form.Get("username"))
	scope := strings.TrimSpace(r.Form.Get("scope"))
	if username == "" {
		azureOAuthError(w, "invalid_request", "username is required for grant_type=password", http.StatusBadRequest)
		return
	}

	u, ok := findEntraUserByUPN(username)
	if !ok {
		azureOAuthError(w, "invalid_grant", "user not found: "+username, http.StatusBadRequest)
		return
	}

	if scope == "" {
		scope = "openid profile"
	}
	now := time.Now()
	audience := azureAudienceFromScope(scope)
	accessToken, err := mintAzureSimJWTForUser(u, tenantID, audience, now, now.Add(time.Hour))
	if err != nil {
		sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	body := map[string]any{
		"access_token":   accessToken,
		"token_type":     "Bearer",
		"expires_in":     3600,
		"ext_expires_in": 3600,
		"scope":          scope,
	}
	if azureScopeIncludes(scope, "openid") {
		issuer := azureIssuer(azureAuthBaseURL(r), tenantID, true)
		idToken, err := mintAzureSimIDTokenForUser(u, tenantID, clientID, "", scope, issuer, now, now.Add(time.Hour))
		if err != nil {
			sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
			return
		}
		body["id_token"] = idToken
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

func mintAzureSimSignedJWT(claims map[string]any) (string, error) {
	headerJSON, _ := json.Marshal(map[string]string{
		"alg": "RS256",
		"typ": "JWT",
		"kid": "sockerless-sim-key-1",
	})
	payloadJSON, _ := json.Marshal(claims)
	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signingInput := headerB64 + "." + payloadB64
	digest := sha256.Sum256([]byte(signingInput))
	key, err := azureSimSigningKey()
	if err != nil {
		return "", fmt.Errorf("generate Azure simulator signing key: %w", err)
	}
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign Azure simulator JWT: %w", err)
	}
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)
	return signingInput + "." + sigB64, nil
}

func redirectAzureAuthError(w http.ResponseWriter, redirectURI, responseMode, state, code, description string) {
	values := url.Values{
		"error":             {code},
		"error_description": {description},
	}
	if state != "" {
		values.Set("state", state)
	}
	redirectAzureAuthorizeResponse(w, redirectURI, responseMode, values)
}

func redirectAzureAuthorizeResponse(w http.ResponseWriter, redirectURI, responseMode string, values url.Values) {
	switch responseMode {
	case "", "query":
		u, err := url.Parse(redirectURI)
		if err != nil {
			azureOAuthError(w, "invalid_request", "redirect_uri is invalid", http.StatusBadRequest)
			return
		}
		q := u.Query()
		for k, vals := range values {
			for _, v := range vals {
				q.Add(k, v)
			}
		}
		u.RawQuery = q.Encode()
		w.Header().Set("Location", u.String())
		w.WriteHeader(http.StatusFound)
	case "fragment":
		u, err := url.Parse(redirectURI)
		if err != nil {
			azureOAuthError(w, "invalid_request", "redirect_uri is invalid", http.StatusBadRequest)
			return
		}
		fragment := url.Values{}
		if u.Fragment != "" {
			parsed, err := url.ParseQuery(u.Fragment)
			if err == nil {
				fragment = parsed
			}
		}
		for k, vals := range values {
			for _, v := range vals {
				fragment.Add(k, v)
			}
		}
		u.Fragment = fragment.Encode()
		w.Header().Set("Location", u.String())
		w.WriteHeader(http.StatusFound)
	case "form_post":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `<!doctype html><html><body><form method="post" action="%s">`, html.EscapeString(redirectURI))
		for k, vals := range values {
			for _, v := range vals {
				fmt.Fprintf(w, `<input type="hidden" name="%s" value="%s">`, html.EscapeString(k), html.EscapeString(v))
			}
		}
		fmt.Fprint(w, `<noscript><button type="submit">Continue</button></noscript><script>document.forms[0].submit()</script></form></body></html>`)
	default:
		azureOAuthError(w, "invalid_request", "response_mode is unsupported", http.StatusBadRequest)
	}
}

func azureOAuthError(w http.ResponseWriter, code, description string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":             code,
		"error_description": description,
	})
}

func azureSimJWK() (map[string]any, error) {
	key, err := azureSimSigningKey()
	if err != nil {
		return nil, fmt.Errorf("generate Azure simulator signing key: %w", err)
	}
	pub := key.PublicKey
	return map[string]any{
		"kid": "sockerless-sim-key-1",
		"kty": "RSA",
		"alg": "RS256",
		"use": "sig",
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}, nil
}
