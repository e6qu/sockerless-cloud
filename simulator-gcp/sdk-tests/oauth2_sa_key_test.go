package gcp_sdk_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/iam/v1"
	"google.golang.org/api/option"
)

// These tests prove the credential loop the console's service-account pages
// rely on: a key minted through the real IAM API (CreateServiceAccountKey) is
// the one and only signer the OAuth2 token endpoint accepts for that account.
// The verification is real Google's: the assertion's `iss` resolves to a
// registered account and its RS256 signature must verify against the
// account's registered public keys — so a tampered private key, an unknown
// account, or a revoked (deleted) key is rejected with the real endpoint's
// `invalid_grant` error, and the minted file works end-to-end: JWT-bearer
// exchange, then an authenticated data-plane read over the resulting token.

// mintServiceAccountKeyFile creates a service account and mints a key through
// the real IAM API, returning the decoded credential file with its token_uri
// repointed at the simulator's token endpoint — the coordinate swap a real
// client makes, and the only edit.
func mintServiceAccountKeyFile(t *testing.T, accountID string) (saName, keyName string, keyFile map[string]any) {
	t.Helper()
	svc := iamService(t)
	sa, err := svc.Projects.ServiceAccounts.Create("projects/test-project",
		&iam.CreateServiceAccountRequest{
			AccountId:      accountID,
			ServiceAccount: &iam.ServiceAccount{DisplayName: "Minted key SA"},
		}).Do()
	require.NoError(t, err)
	key, err := svc.Projects.ServiceAccounts.Keys.Create(sa.Name,
		&iam.CreateServiceAccountKeyRequest{}).Do()
	require.NoError(t, err)

	raw, err := base64.StdEncoding.DecodeString(key.PrivateKeyData)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &keyFile))
	keyFile["token_uri"] = baseURL + "/token"
	return sa.Name, key.Name, keyFile
}

// tokenSourceFromKeyFile builds the standard service-account JWT-bearer flow
// (golang.org/x/oauth2/google) from a credential-file JSON — the same flow
// GOOGLE_APPLICATION_CREDENTIALS drives in a backend.
func tokenSourceFromKeyFile(t *testing.T, keyFile map[string]any) *google.Credentials {
	t.Helper()
	body, err := json.Marshal(keyFile)
	require.NoError(t, err)
	creds, err := google.CredentialsFromJSON(ctx, body, "https://www.googleapis.com/auth/cloud-platform")
	require.NoError(t, err)
	return creds
}

func TestOAuth2_MintedServiceAccountKey_EndToEnd(t *testing.T) {
	_, _, keyFile := mintServiceAccountKeyFile(t, "minted-key-sa")
	creds := tokenSourceFromKeyFile(t, keyFile)

	// The JWT-bearer exchange succeeds only because the assertion verifies
	// against the key CreateServiceAccountKey registered.
	tok, err := creds.TokenSource.Token()
	require.NoError(t, err, "token endpoint must accept an assertion signed with the minted key")
	require.NotEmpty(t, tok.AccessToken)

	// The minted token authenticates a real data-plane read.
	svc, err := iam.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(creds.TokenSource),
	)
	require.NoError(t, err)
	list, err := svc.Projects.ServiceAccounts.List("projects/test-project").Do()
	require.NoError(t, err, "the minted token must be accepted by the data plane")
	found := false
	for _, sa := range list.Accounts {
		if sa.Email == "minted-key-sa@test-project.iam.gserviceaccount.com" {
			found = true
		}
	}
	assert.True(t, found, "authenticated list must include the created account")
}

func TestOAuth2_TamperedKeyRejected(t *testing.T) {
	_, _, keyFile := mintServiceAccountKeyFile(t, "tampered-key-sa")

	// Swap the private key for a fresh one the IAM API never registered,
	// keeping the account and key ID intact — the signature no longer
	// verifies against the registered public half.
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	keyFile["private_key"] = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	creds := tokenSourceFromKeyFile(t, keyFile)
	_, err = creds.TokenSource.Token()
	require.Error(t, err, "an assertion signed with an unregistered key must be rejected")
	assert.Contains(t, err.Error(), "invalid_grant")
}

func TestOAuth2_UnknownServiceAccountRejected(t *testing.T) {
	_, _, keyFile := mintServiceAccountKeyFile(t, "known-sa-for-unknown-test")

	// The minted key is genuine, but the assertion claims an account that
	// does not exist.
	keyFile["client_email"] = "never-created@test-project.iam.gserviceaccount.com"

	creds := tokenSourceFromKeyFile(t, keyFile)
	_, err := creds.TokenSource.Token()
	require.Error(t, err, "an assertion for an unknown account must be rejected")
	assert.Contains(t, err.Error(), "invalid_grant")
	assert.Contains(t, err.Error(), "account not found")
}

func TestOAuth2_DeletedKeyRejected(t *testing.T) {
	_, keyName, keyFile := mintServiceAccountKeyFile(t, "revoked-key-sa")

	// The key works while it is registered…
	creds := tokenSourceFromKeyFile(t, keyFile)
	_, err := creds.TokenSource.Token()
	require.NoError(t, err)

	// …and stops working the moment it is deleted: revocation is real.
	_, err = iamService(t).Projects.ServiceAccounts.Keys.Delete(keyName).Do()
	require.NoError(t, err)

	fresh := tokenSourceFromKeyFile(t, keyFile)
	_, err = fresh.TokenSource.Token()
	require.Error(t, err, "an assertion signed with a deleted key must be rejected")
	assert.Contains(t, err.Error(), "invalid_grant")
}

func TestOAuth2_ExpiredAssertionRejected(t *testing.T) {
	_, keyName, keyFile := mintServiceAccountKeyFile(t, "expired-assertion-sa")

	// Sign an assertion with the genuine minted key, expired an hour ago. The
	// signature verifies; the time claims must still reject it.
	block, _ := pem.Decode([]byte(keyFile["private_key"].(string)))
	require.NotNil(t, block)
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	require.NoError(t, err)
	keyID := keyName[strings.LastIndex(keyName, "/")+1:]
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: parsed},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", keyID),
	)
	require.NoError(t, err)
	assertion, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer:   keyFile["client_email"].(string),
		Audience: jwt.Audience{baseURL + "/token"},
		IssuedAt: jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		Expiry:   jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
	}).Serialize()
	require.NoError(t, err)

	resp, err := http.PostForm(baseURL+"/token", url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	})
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, string(body), "invalid_grant")
}
