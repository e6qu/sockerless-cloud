package main

import "time"

// mintAzureSimJWT mints a real-shape Azure AD access token for the directory's
// default identity. It is a test-only convenience over mintAzureSimJWTForUser:
// production token issuance always names the concrete signed-in or federated
// identity, so once the implicit-client client_credentials grant was removed
// (unregistered clients now get AADSTS700016) this default-identity variant is
// used only by tests.
func mintAzureSimJWT(tenantId, audience string, issuedAt, expiresAt time.Time) (string, error) {
	return mintAzureSimJWTForUser(entraDefaultUser, tenantId, audience, issuedAt, expiresAt)
}
