package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/e6qu/sockerless-cloud/sim"
)

// registerSTS mounts the Security Token Service token-exchange endpoint that
// backs Google Cloud Workforce Identity Federation.
//
// Real flow: a client holding an external OpenID Connect assertion POSTs it to
// `https://sts.googleapis.com/v1/token` with
// `grant_type=urn:ietf:params:oauth:grant-type:token-exchange`, naming a
// workforce pool provider as the audience. The Security Token Service verifies
// the assertion against the provider's configured issuer and returns a
// short-lived federated access token the caller then presents as
// `Authorization: Bearer` to Google Cloud APIs.
//
// The simulator plays that role faithfully: it resolves the workforce pool
// provider the audience names, verifies the subject token against the
// provider's OpenID Connect issuer — real discovery, real JSON Web Key Set,
// real signature, issuer, audience and expiry checks — and issues a federated
// access token its own resource endpoints validate.
func registerSTS(srv *sim.Server) {
	srv.HandleFunc("POST /v1/token", handleSTSTokenExchange)
	srv.HandleFunc("POST /v1/introspect", handleSTSIntrospect)
}

const (
	grantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenTypeAccessToken   = "urn:ietf:params:oauth:token-type:access_token"
	subjectTokenTypeJWT    = "urn:ietf:params:oauth:token-type:jwt"
	subjectTokenTypeIDTok  = "urn:ietf:params:oauth:token-type:id_token"

	workforceAudiencePrefix = "//iam.googleapis.com/"
)

// stsError writes the OAuth 2.0 token-exchange error shape the Security Token
// Service returns (RFC 6749 §5.2, which token exchange inherits): a JSON body
// with `error` and `error_description`, at HTTP 400.
func stsError(w http.ResponseWriter, code, description string) {
	sim.WriteJSON(w, http.StatusBadRequest, map[string]string{
		"error":             code,
		"error_description": description,
	})
}

func handleSTSTokenExchange(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		stsError(w, "invalid_request", "request body could not be parsed")
		return
	}
	if got := r.Form.Get("grant_type"); got != grantTypeTokenExchange {
		stsError(w, "invalid_request", fmt.Sprintf("unsupported grant_type %q", got))
		return
	}
	subjectToken := r.Form.Get("subject_token")
	if subjectToken == "" {
		stsError(w, "invalid_request", "subject_token is required")
		return
	}
	switch r.Form.Get("subject_token_type") {
	case subjectTokenTypeJWT, subjectTokenTypeIDTok:
	default:
		stsError(w, "invalid_request", "subject_token_type must name a JWT or ID token")
		return
	}
	if requested := r.Form.Get("requested_token_type"); requested != "" && requested != tokenTypeAccessToken {
		stsError(w, "invalid_request", "requested_token_type must be an access token")
		return
	}

	providerName, err := workforceProviderFromAudience(r.Form.Get("audience"))
	if err != nil {
		stsError(w, "invalid_request", err.Error())
		return
	}

	token, expiresIn, code, err := federateWorkforceSubject(r.Context(), providerName, subjectToken)
	if err != nil {
		stsError(w, code, err.Error())
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token":      token,
		"issued_token_type": tokenTypeAccessToken,
		"token_type":        "Bearer",
		"expires_in":        expiresIn,
	})
}

// handleSTSIntrospect implements the Security Token Service token
// introspection endpoint (`https://sts.googleapis.com/v1/introspect`,
// RFC 7662). gcloud calls it after `gcloud auth login --cred-file` refreshes
// a workforce external_account credential, to resolve the federated
// principal it stores as the account name: a form-encoded POST of
// `token` (+ `token_type_hint`), authenticated with the caller's OAuth
// client credentials over HTTP Basic — the gcloud CLI presents Google's own
// published gcloud client id and secret.
//
// A token this simulator minted introspects active, with `username` naming
// the workforce principal
// (`principal://iam.googleapis.com/locations/.../subject/...`). A token the
// simulator did not mint — malformed, unsigned by its key, or expired —
// introspects `{"active": false}` at HTTP 200, which is both RFC 7662 §2.2
// and real Google's observed response for a token it does not recognise.
func handleSTSIntrospect(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		stsError(w, "invalid_request", "request body could not be parsed")
		return
	}
	// RFC 7662 §2.1: the endpoint must authenticate the caller so token
	// contents cannot be scanned anonymously. Clients authenticate with
	// their OAuth client credentials — HTTP Basic (what gcloud sends) or the
	// equivalent client_id/client_secret body parameters.
	if _, _, hasBasic := r.BasicAuth(); !hasBasic && r.Form.Get("client_id") == "" {
		w.Header().Set("WWW-Authenticate", "Basic")
		sim.WriteJSON(w, http.StatusUnauthorized, map[string]string{
			"error":             "invalid_client",
			"error_description": "the introspection request carried no client authentication",
		})
		return
	}
	token := r.Form.Get("token")
	if token == "" {
		stsError(w, "invalid_request", "token is required")
		return
	}

	claims, err := verifiedAccessTokenClaims(token)
	if err != nil {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	response := map[string]any{
		"active": true,
		"sub":    claims.Sub,
		"scope":  claims.Scope,
		"iss":    claims.Iss,
		"exp":    claims.Exp,
		"iat":    claims.Iat,
	}
	if strings.Contains(claims.Sub, "/workforcePools/") && strings.Contains(claims.Sub, "/subject/") {
		response["username"] = "principal:" + workforceAudiencePrefix + claims.Sub
	}
	sim.WriteJSON(w, http.StatusOK, response)
}

// federateWorkforceSubject performs the Workforce Identity Federation token
// exchange: it resolves the workforce pool provider, verifies the subject token
// against that provider's OpenID Connect issuer, and mints a short-lived
// federated access token naming the workforce principal. The Security Token
// Service endpoint and the console's session broker share it so both take the
// exact same verification path. The returned code is the OAuth error code to
// report when err is non-nil.
func federateWorkforceSubject(ctx context.Context, providerName, subjectToken string) (token string, expiresIn int, code string, err error) {
	provider, ok := iamResources.Get(providerName)
	if !ok {
		return "", 0, "invalid_target", fmt.Errorf("workforce pool provider %q does not exist", providerName)
	}
	oidcConfig, issuerURI, allowedAudiences, err := workforceProviderOIDC(provider)
	if err != nil {
		return "", 0, "invalid_target", err
	}
	subject, err := verifyWorkforceSubjectToken(ctx, subjectToken, issuerURI, allowedAudiences, oidcConfig)
	if err != nil {
		return "", 0, "invalid_grant", err
	}

	now := time.Now()
	expires := now.Add(time.Hour)
	principal := fmt.Sprintf("%s/subject/%s", workforcePoolFromProvider(providerName), subject)
	return signAccessToken(principal, now, expires), int(time.Until(expires).Seconds()), "", nil
}

// workforceProviderFromAudience turns the STS audience — the workforce pool
// provider resource written `//iam.googleapis.com/locations/.../providers/...`
// — into the resource name the provider is stored under.
func workforceProviderFromAudience(audience string) (string, error) {
	if audience == "" {
		return "", fmt.Errorf("audience is required")
	}
	name := strings.TrimPrefix(audience, workforceAudiencePrefix)
	if name == audience {
		return "", fmt.Errorf("audience must name a workforce pool provider")
	}
	if !strings.Contains(name, "/workforcePools/") || !strings.Contains(name, "/providers/") {
		return "", fmt.Errorf("audience %q is not a workforce pool provider", audience)
	}
	return name, nil
}

// workforcePoolFromProvider drops the `/providers/{provider}` suffix, leaving
// the pool the federated principal belongs to.
func workforcePoolFromProvider(providerName string) string {
	if index := strings.Index(providerName, "/providers/"); index >= 0 {
		return providerName[:index]
	}
	return providerName
}

// workforceProviderOIDC reads the OpenID Connect configuration a workforce pool
// provider was created with. The provider resource is stored verbatim, so this
// reads the same `oidc.issuerUri` and `oidc.clientId` a real provider carries,
// plus the attribute mapping used to derive the federated subject. Google
// requires a workforce provider's `clientId` to match the audience claim of the
// assertion, so the client ID is the allowed audience.
func workforceProviderOIDC(provider map[string]any) (config map[string]any, issuerURI string, allowedAudiences []string, err error) {
	oidcRaw, ok := provider["oidc"].(map[string]any)
	if !ok {
		return nil, "", nil, fmt.Errorf("workforce pool provider has no OpenID Connect configuration")
	}
	issuerURI, _ = oidcRaw["issuerUri"].(string)
	if issuerURI == "" {
		return nil, "", nil, fmt.Errorf("workforce pool provider has no issuerUri")
	}
	if clientID, _ := oidcRaw["clientId"].(string); clientID != "" {
		allowedAudiences = append(allowedAudiences, clientID)
	}
	return oidcRaw, issuerURI, allowedAudiences, nil
}

// verifyWorkforceSubjectToken verifies the external assertion the way the
// Security Token Service does and returns the federated subject derived from
// it. Verification is real: OpenID Connect discovery against the issuer, its
// JSON Web Key Set, the token signature, the issuer, the audience against the
// provider's allowed set, and expiry. The subject comes from the provider's
// attribute mapping — `google.subject`, defaulting to the assertion subject
// exactly as Google does when no mapping is configured.
func verifyWorkforceSubjectToken(ctx context.Context, rawToken, issuerURI string, allowedAudiences []string, oidcConfig map[string]any) (string, error) {
	provider, err := oidc.NewProvider(ctx, issuerURI)
	if err != nil {
		return "", fmt.Errorf("issuer %q could not be discovered: %w", issuerURI, err)
	}
	// The audience is checked against the provider's allowed set below rather
	// than against a single client ID, since a workforce provider may permit
	// several audiences.
	verified, err := provider.Verifier(&oidc.Config{SkipClientIDCheck: true}).Verify(ctx, rawToken)
	if err != nil {
		return "", fmt.Errorf("subject token failed verification: %w", err)
	}
	if len(allowedAudiences) > 0 && !audienceAllowed(verified.Audience, allowedAudiences) {
		return "", fmt.Errorf("subject token audience is not permitted by the provider")
	}

	var claims map[string]any
	if err := verified.Claims(&claims); err != nil {
		return "", fmt.Errorf("subject token claims could not be read: %w", err)
	}
	subject := mapWorkforceSubject(oidcConfig, claims, verified.Subject)
	if subject == "" {
		return "", fmt.Errorf("subject token did not yield a federated subject")
	}
	return subject, nil
}

func audienceAllowed(tokenAudiences, allowed []string) bool {
	for _, aud := range tokenAudiences {
		for _, candidate := range allowed {
			if aud == candidate {
				return true
			}
		}
	}
	return false
}

// mapWorkforceSubject applies the provider's `google.subject` attribute
// mapping. The simulator supports the assertion-field form Google documents
// first — `assertion.<claim>` — and falls back to the assertion subject, which
// is Google's default when a provider configures no mapping for
// `google.subject`.
func mapWorkforceSubject(oidcConfig map[string]any, claims map[string]any, defaultSubject string) string {
	mapping, _ := oidcConfig["attributeMapping"].(map[string]any)
	expression, _ := mapping["google.subject"].(string)
	if strings.HasPrefix(expression, "assertion.") {
		claim := strings.TrimPrefix(expression, "assertion.")
		if value, ok := claims[claim].(string); ok && value != "" {
			return value
		}
	}
	return defaultSubject
}

// Federated access tokens the Security Token Service issues are signed with the
// simulator's shared access-token key (see signAccessToken), so a token this
// endpoint mints is verified by the data-plane bearer middleware exactly like a
// service-account or metadata token. Real Google signs opaque federated tokens
// its resource services validate internally; the simulator plays the same role
// with a token it both issues and verifies.
