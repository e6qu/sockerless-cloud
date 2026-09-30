package gcp_sdk_test

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/api/iam/v1"
	iamcredentials "google.golang.org/api/iamcredentials/v1"
)

func getJSON(t *testing.T, url string, into any) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, url)
	require.NoError(t, json.NewDecoder(resp.Body).Decode(into))
}

// A relying party verifies an identity token the way OpenID Connect prescribes:
// the token's iss is the issuer the discovery document publishes, and its
// signature verifies against a key of the JWKS that document names. Google
// issues identity tokens as https://accounts.google.com.
func TestOpenIDDiscoveryVerifiesIdentityTokens(t *testing.T) {
	created, err := iamService(t).Projects.ServiceAccounts.Create("projects/test-project",
		&iam.CreateServiceAccountRequest{AccountId: uniqueName("oidc-rp")}).Do()
	require.NoError(t, err)
	minted, err := iamCredentialsService(t).Projects.ServiceAccounts.GenerateIdToken(created.Name,
		&iamcredentials.GenerateIdTokenRequest{Audience: "https://relying-party.example.com"}).Do()
	require.NoError(t, err)

	var discovery struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	getJSON(t, baseURL+"/.well-known/openid-configuration", &discovery)
	require.Equal(t, "https://accounts.google.com", discovery.Issuer)

	parts := strings.Split(minted.Token, ".")
	require.Len(t, parts, 3)
	var header struct {
		Kid string `json:"kid"`
		Alg string `json:"alg"`
	}
	var claims struct {
		Iss string `json:"iss"`
		Aud string `json:"aud"`
	}
	for i, into := range []any{&header, &claims} {
		raw, err := base64.RawURLEncoding.DecodeString(parts[i])
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, into))
	}
	require.Equal(t, discovery.Issuer, claims.Iss, "the token names the issuer discovery publishes")
	require.Equal(t, "https://relying-party.example.com", claims.Aud)
	require.Equal(t, "RS256", header.Alg)

	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	getJSON(t, discovery.JWKSURI, &jwks)
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	for _, key := range jwks.Keys {
		if key.Kid != header.Kid {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(key.N)
		require.NoError(t, err)
		e, err := base64.RawURLEncoding.DecodeString(key.E)
		require.NoError(t, err)
		public := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		require.NoError(t, rsa.VerifyPKCS1v15(public, crypto.SHA256, digest[:], signature))
		return
	}
	t.Fatalf("the JWKS at %s holds no key %q", discovery.JWKSURI, header.Kid)
}
