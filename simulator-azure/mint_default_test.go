package main

import "time"

// mintAzureSimJWT mints an access token for the directory's default identity.
// Production issuance always names a concrete identity, so only tests use it.
func mintAzureSimJWT(tenantId, audience string, issuedAt, expiresAt time.Time) (string, error) {
	return mintAzureSimJWTForUser(entraDefaultUser, tenantId, audience, issuedAt, expiresAt)
}

// mintBootstrapSimJWT mints the app-only token the client_credentials grant
// issues the bootstrap service principal, which holds Owner on the seeded
// subscription.
func mintBootstrapSimJWT(audience string, issuedAt, expiresAt time.Time) (string, error) {
	return mintAzureSimSignedJWT(map[string]any{
		"tid":   simTenantID,
		"oid":   entraBootstrapSPObjectID,
		"sub":   entraBootstrapSPObjectID,
		"aud":   audience,
		"iss":   "https://sts.windows.net/" + simTenantID + "/",
		"iat":   issuedAt.Unix(),
		"nbf":   issuedAt.Unix(),
		"exp":   expiresAt.Unix(),
		"ver":   "1.0",
		"appid": entraBootstrapClientID,
		"idtyp": "app",
	})
}
