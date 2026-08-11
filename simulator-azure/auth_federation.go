package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
	"golang.org/x/sync/singleflight"
)

// federatedClientAssertionType is the client_assertion_type Microsoft Entra
// requires for a JWT-bearer client assertion (RFC 7523).
const federatedClientAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

var (
	azureOIDCVerifiers sync.Map
	azureOIDCDiscovery singleflight.Group
)

// azureOIDCHTTPClient bounds every OpenID Connect discovery and JSON Web Key
// Set fetch the federation path performs. Without it go-oidc falls back to
// http.DefaultClient, whose zero timeout would let a slow issuer (an
// unresolvable host, a proxy still starting) stall a token exchange for as
// long as it keeps the connection open.
var azureOIDCHTTPClient = &http.Client{Timeout: 10 * time.Second}

// handleAzureFederatedClientCredentials implements Microsoft Entra Workload
// Identity Federation on the client_credentials grant: a confidential client
// authenticates with a client_assertion that is an *external* OIDC token, and
// Entra issues an access token when that assertion matches a federated identity
// credential registered on the client's user-assigned identity — real issuer
// discovery, real JSON Web Key Set, real signature, subject, and audience.
//
// This is the console's federation path: an operator signed in through the
// deployment's identity provider exchanges that assertion for an Azure Resource
// Manager token, exactly as a workload federating into Azure does. It returns
// true when it has taken the federated path (and written a response); a request
// without a JWT-bearer client_assertion returns false so the caller keeps the
// existing client_credentials behavior.
func handleAzureFederatedClientCredentials(w http.ResponseWriter, r *http.Request, tenantID, clientID string) bool {
	assertion := strings.TrimSpace(r.Form.Get("client_assertion"))
	if assertion == "" || strings.TrimSpace(r.Form.Get("client_assertion_type")) != federatedClientAssertionType {
		return false
	}

	issuer, err := azureUnverifiedIssuer(assertion)
	if err != nil {
		azureOAuthError(w, "invalid_request", err.Error(), http.StatusBadRequest)
		return true
	}
	identity, ok := azureIdentityForClientID(clientID)
	if !ok {
		azureOAuthError(w, "invalid_client",
			fmt.Sprintf("no user-assigned identity is registered for client_id %q", clientID), http.StatusUnauthorized)
		return true
	}
	subject, err := azureVerifyFederatedAssertion(r.Context(), identity, issuer, assertion)
	if err != nil {
		azureOAuthError(w, "invalid_client", err.Error(), http.StatusUnauthorized)
		return true
	}

	audience, err := azureTokenAudienceFromRequest(r)
	if err != nil {
		sim.AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
		return true
	}
	now := time.Now()
	// The issued token speaks for the managed identity — Entra federation
	// exchanges the external assertion for the identity's own credential — while
	// the federated subject is recorded so the operator stays traceable.
	token, err := mintAzureSimSignedJWT(map[string]any{
		"tid":         tenantID,
		"oid":         identity.Properties.PrincipalId,
		"sub":         identity.Properties.PrincipalId,
		"aud":         audience,
		"iss":         fmt.Sprintf("https://sts.windows.net/%s/", tenantID),
		"iat":         now.Unix(),
		"exp":         now.Add(time.Hour).Unix(),
		"nbf":         now.Unix(),
		"ver":         "1.0",
		"appid":       clientID,
		"idtyp":       "app",
		"xms_fed_sub": subject,
	})
	if err != nil {
		sim.AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token":   token,
		"token_type":     "Bearer",
		"expires_in":     3600,
		"ext_expires_in": 3600,
	})
	return true
}

// azureVerifyFederatedAssertion verifies an external assertion against the
// identity's federated identity credentials: it discovers and verifies the
// token against its issuer once, then requires a credential whose issuer,
// subject, and audience all match the verified token. It returns the token's
// subject.
func azureVerifyFederatedAssertion(ctx context.Context, identity UserAssignedIdentity, issuer, assertion string) (string, error) {
	creds := azureFederatedCredentialsForIdentity(identity.ID)
	if len(creds) == 0 {
		return "", fmt.Errorf("identity %q has no federated identity credentials", identity.Name)
	}
	verifier, err := azureOIDCVerifier(ctx, issuer)
	if err != nil {
		return "", fmt.Errorf("issuer %q could not be discovered: %w", issuer, err)
	}
	verified, err := verifier.Verify(ctx, assertion)
	if err != nil {
		return "", fmt.Errorf("client assertion failed verification: %w", err)
	}
	for _, fic := range creds {
		if azureNormalizeIssuer(fic.Properties.Issuer) != azureNormalizeIssuer(issuer) {
			continue
		}
		if fic.Properties.Subject != verified.Subject {
			continue
		}
		if !azureAudienceMatches(fic.Properties.Audiences, verified.Audience) {
			continue
		}
		return verified.Subject, nil
	}
	return "", fmt.Errorf("no federated identity credential matches the assertion's issuer, subject, and audience")
}

// azureOIDCVerifier returns the verifier for one exact external issuer. OpenID
// Connect discovery metadata and its remote JSON Web Key Set are issuer
// configuration, not request state, so Microsoft Entra reuses them across
// workload-identity exchanges. The verifier still checks every assertion's
// signature, issuer, expiry, and claims; only the network-backed discovery
// object is retained. singleflight prevents a burst of first exchanges from
// repeating the same discovery request.
func azureOIDCVerifier(ctx context.Context, issuer string) (*oidc.IDTokenVerifier, error) {
	cacheKey := strings.TrimSuffix(strings.TrimSpace(issuer), "/")
	if cached, ok := azureOIDCVerifiers.Load(cacheKey); ok {
		return azureCachedOIDCVerifier(cached)
	}
	value, err, _ := azureOIDCDiscovery.Do(cacheKey, func() (any, error) {
		if cached, ok := azureOIDCVerifiers.Load(cacheKey); ok {
			return azureCachedOIDCVerifier(cached)
		}
		// The provider (and the remote key set it constructs) outlives this
		// request: it is cached process-wide and refetches the JWKS through
		// the context captured here. A background context with the bounded
		// client keeps later refreshes working after the first caller's
		// request context is canceled, while the client timeout bounds every
		// individual discovery and key-set fetch.
		discoveryCtx := oidc.ClientContext(context.Background(), azureOIDCHTTPClient)
		provider, err := oidc.NewProvider(discoveryCtx, issuer)
		if err != nil {
			return nil, err
		}
		verifier := provider.Verifier(&oidc.Config{SkipClientIDCheck: true})
		azureOIDCVerifiers.Store(cacheKey, verifier)
		return verifier, nil
	})
	if err != nil {
		return nil, err
	}
	return azureCachedOIDCVerifier(value)
}

func azureCachedOIDCVerifier(value any) (*oidc.IDTokenVerifier, error) {
	verifier, ok := value.(*oidc.IDTokenVerifier)
	if !ok {
		return nil, fmt.Errorf("cached OpenID Connect verifier has unexpected type %T", value)
	}
	return verifier, nil
}

// azureIdentityForClientID finds the user-assigned identity whose client ID the
// token request authenticates as.
func azureIdentityForClientID(clientID string) (UserAssignedIdentity, bool) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return UserAssignedIdentity{}, false
	}
	for _, identity := range azureManagedIdentities.List() {
		if identity.Properties.ClientId == clientID {
			return identity, true
		}
	}
	return UserAssignedIdentity{}, false
}

// azureFederatedCredentialsForIdentity returns the federated identity
// credentials scoped to one user-assigned identity.
func azureFederatedCredentialsForIdentity(identityID string) []FederatedIdentityCredential {
	prefix := identityID + "/federatedIdentityCredentials/"
	var out []FederatedIdentityCredential
	for _, fic := range azureFederatedCredentials.List() {
		if strings.HasPrefix(fic.ID, prefix) {
			out = append(out, fic)
		}
	}
	return out
}

// azureUnverifiedIssuer reads the `iss` claim without verifying the signature,
// so the right issuer can be discovered before the token is verified against it.
func azureUnverifiedIssuer(rawToken string) (string, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("client assertion is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("client assertion payload could not be decoded: %w", err)
	}
	var claims struct {
		Issuer string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("client assertion claims could not be read: %w", err)
	}
	if claims.Issuer == "" {
		return "", fmt.Errorf("client assertion has no issuer")
	}
	return claims.Issuer, nil
}

func azureNormalizeIssuer(value string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(value, "https://"), "http://"), "/")
}

func azureAudienceMatches(allowed, got []string) bool {
	for _, a := range got {
		for _, candidate := range allowed {
			if a == candidate {
				return true
			}
		}
	}
	return false
}
