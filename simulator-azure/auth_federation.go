package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/oidcfed"
)

// federatedClientAssertionType is the client_assertion_type Microsoft Entra
// requires for a JWT-bearer client assertion (RFC 7523).
const federatedClientAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

// azureOIDCVerifiers caches the external issuers Microsoft Entra federates.
var azureOIDCVerifiers = oidcfed.New()

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

	issuer, err := oidcfed.UnverifiedIssuer(assertion)
	if err != nil {
		azureOAuthError(w, "invalid_request", "client assertion "+err.Error(), http.StatusBadRequest)
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
		AzureError(w, "InvalidRequest", err.Error(), http.StatusBadRequest)
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
		AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
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
	verifier, err := azureOIDCVerifiers.Verifier(ctx, issuer)
	if err != nil {
		return "", fmt.Errorf("issuer %q could not be discovered: %w", issuer, err)
	}
	verified, err := verifier.Verify(ctx, assertion)
	if err != nil {
		return "", fmt.Errorf("client assertion failed verification: %w", err)
	}
	for _, fic := range creds {
		if oidcfed.NormalizeIssuer(fic.Properties.Issuer) != oidcfed.NormalizeIssuer(issuer) {
			continue
		}
		if fic.Properties.Subject != verified.Subject {
			continue
		}
		if !oidcfed.AudienceIntersects(fic.Properties.Audiences, verified.Audience) {
			continue
		}
		return verified.Subject, nil
	}
	return "", fmt.Errorf("no federated identity credential matches the assertion's issuer, subject, and audience")
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
