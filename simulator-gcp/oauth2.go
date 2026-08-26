package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// registerOAuth2 wires the OAuth2 token endpoint that service-account
// JWT-bearer flows hit when minting access and ID tokens.
//
// Real flow: the SDK constructs a JWT signed with the SA's private key and
// POSTs it to `https://oauth2.googleapis.com/token` with
// `grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer`. Google resolves
// the assertion's `iss` (the service-account email) to a registered account,
// verifies the assertion's RS256 signature against that account's registered
// public keys, and only then mints tokens. For an access token it returns
// `{"access_token":"...","token_type":"Bearer",...}`; when the assertion
// carries a `target_audience` claim (the workload requesting an ID token for a
// downstream Cloud Run / Cloud Functions invoke) Google also returns an RS256
// `id_token` whose `aud` is that target audience.
//
// The sim's role is the same: an assertion is verified against the public
// halves the IAM CreateServiceAccountKey endpoint registered — a key minted
// through the real IAM API is the only thing that can sign an acceptable
// assertion, and a tampered, revoked, or unknown key is rejected with the real
// endpoint's `invalid_grant` error shape. Both the access token and the ID
// token are RS256 JWTs the simulator signs with its own access-token key (see
// signAccessToken / signInvokeIDToken); the data-plane bearer middleware
// verifies against that same key, so a token minted here is accepted on
// subsequent requests and an unverifiable one is rejected.
//
// Test surfaces that only need an access token POST a bare grant_type with no
// assertion, in which case the sim mints an access token for the default
// principal and returns no ID token — matching real Google in that an
// id_token is returned only when target_audience is requested.
func registerOAuth2(srv *sim.Server) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		now := time.Now()
		expires := now.Add(1 * time.Hour)

		principal := "sockerless-sim"
		targetAudience := ""
		if assertion := r.Form.Get("assertion"); assertion != "" {
			claims, grantErr := verifyServiceAccountAssertion(assertion, now)
			if grantErr != nil {
				// The real token endpoint answers a failed grant with the
				// RFC 6749 error body, not the Google API error envelope.
				sim.WriteJSON(w, http.StatusBadRequest, map[string]string{
					"error":             grantErr.code,
					"error_description": grantErr.description,
				})
				return
			}
			principal = claims.issuer
			targetAudience = claims.targetAudience
		}

		resp := map[string]any{
			"access_token": signAccessToken(principal, now, expires),
			"expires_in":   int(time.Until(expires).Seconds()),
			"token_type":   "Bearer",
		}
		if targetAudience != "" {
			resp["id_token"] = signInvokeIDToken(principal, now, expires)
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	}
	srv.HandleFunc("POST /token", handler)
	srv.HandleFunc("POST /oauth2/v4/token", handler)
	// Google publishes the same grant on three token endpoints — the current
	// `oauth2.googleapis.com/token`, the versioned
	// `www.googleapis.com/oauth2/v4/token`, and the original
	// `accounts.google.com/o/oauth2/token` that older clients and
	// `auth/token_host` overrides still name. All three take the same form
	// body and return the same token, so all three are the same handler.
	srv.HandleFunc("POST /o/oauth2/token", handler)
}

// oauth2GrantError is an RFC 6749 token-endpoint failure: the `error` code and
// `error_description` the real endpoint returns with HTTP 400.
type oauth2GrantError struct {
	code        string
	description string
}

func invalidGrant(description string) *oauth2GrantError {
	return &oauth2GrantError{code: "invalid_grant", description: description}
}

// saAssertionClaims is what a verified assertion establishes: the
// service-account principal the tokens are minted for and, when the assertion
// requested an ID token, the target audience.
type saAssertionClaims struct {
	issuer         string
	targetAudience string
}

// verifyServiceAccountAssertion validates a service-account JWT-bearer
// assertion the way real Google does: the `iss` claim names a registered
// service account, the RS256 signature verifies against one of that account's
// registered public keys (the `kid` header, which SDKs and gcloud fill with
// the credential file's private_key_id, selects the key when present), and the
// assertion is unexpired. The assertion's `aud` is not pinned: a client
// pointed at the simulator carries the simulator's token-endpoint coordinate
// there, and pinning Google's would reject every coordinate-swapped client.
func verifyServiceAccountAssertion(assertion string, now time.Time) (saAssertionClaims, *oauth2GrantError) {
	var none saAssertionClaims

	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return none, invalidGrant("Invalid JWT: Token must consist of header, claims, and signature")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return none, invalidGrant("Invalid JWT: Failed to parse token header")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return none, invalidGrant("Invalid JWT: Failed to parse token header")
	}
	if header.Alg != "RS256" {
		return none, invalidGrant("Invalid JWT Signature.")
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return none, invalidGrant("Invalid JWT: Failed to parse token payload")
	}
	var claims struct {
		Iss            string `json:"iss"`
		TargetAudience string `json:"target_audience"`
		Exp            int64  `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return none, invalidGrant("Invalid JWT: Failed to parse token payload")
	}
	if claims.Iss == "" {
		return none, invalidGrant("Invalid JWT: iss claim is required")
	}

	saName := fmt.Sprintf("projects/%s/serviceAccounts/%s", gcpProjectFromEmail(claims.Iss), claims.Iss)
	sa, ok := iamServiceAccounts.Get(saName)
	if !ok {
		return none, invalidGrant("Invalid grant: account not found")
	}
	if sa.Disabled {
		return none, invalidGrant("Invalid grant: account disabled")
	}

	// The candidate public keys: the exact key the `kid` header names, or —
	// when the client sent no kid — every key registered for the account.
	var candidates []GCPServiceAccountKeyMaterial
	if header.Kid != "" {
		material, ok := iamSAKeyPublics.Get(saName + "/keys/" + header.Kid)
		if !ok {
			return none, invalidGrant("Invalid JWT Signature.")
		}
		candidates = []GCPServiceAccountKeyMaterial{material}
	} else {
		keyPrefix := saName + "/keys/"
		candidates = iamSAKeyPublics.Filter(func(m GCPServiceAccountKeyMaterial) bool {
			return strings.HasPrefix(m.Name, keyPrefix)
		})
	}

	signingInput := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return none, invalidGrant("Invalid JWT Signature.")
	}
	digest := sha256.Sum256([]byte(signingInput))
	verified := false
	for _, candidate := range candidates {
		// A disabled key does not authenticate: real Google refuses an
		// assertion signed with a key keys.disable turned off, until
		// keys.enable turns it back on.
		if key, ok := iamSAKeys.Get(candidate.Name); ok && key.Disabled {
			continue
		}
		block, _ := pem.Decode([]byte(candidate.PublicKeyPEM))
		if block == nil {
			continue
		}
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			continue
		}
		rsaPub, ok := pub.(*rsa.PublicKey)
		if !ok {
			continue
		}
		if rsa.VerifyPKCS1v15(rsaPub, crypto.SHA256, digest[:], signature) == nil {
			verified = true
			break
		}
	}
	if !verified {
		return none, invalidGrant("Invalid JWT Signature.")
	}

	if claims.Exp == 0 || now.After(time.Unix(claims.Exp, 0)) {
		return none, invalidGrant("Invalid JWT: Token must be a short-lived token (60 minutes) and in a reasonable timeframe. Check your iat and exp values in the JWT claim.")
	}

	return saAssertionClaims{issuer: claims.Iss, targetAudience: claims.TargetAudience}, nil
}
