package gcp_tf_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTerraformServiceAccountKey provisions the credential an operator hands a
// non-interactive Google client — `gcloud auth activate-service-account`, a
// backend reading GOOGLE_APPLICATION_CREDENTIALS — through the provider's own
// google_service_account_key resource, and then proves the provisioned key is
// real: it signs a JWT-bearer assertion the simulator's token endpoint verifies
// against the public half IAM registered, and the resulting token authenticates
// a data-plane read. A key that merely round-tripped through terraform state
// would pass a shape assertion and fail this one.
func TestTerraformServiceAccountKey(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "service-account-key")

	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)

	out, err = runTimed(t, "terraform apply", terraformCmdInDir(fixtureDir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", terraformCmdInDir(fixtureDir, "destroy", "-auto-approve"))
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	})

	outputs := readOutputsInDir(t, fixtureDir)
	email := outputs.must(t, "service_account_email")
	require.Equal(t, "tf-key-owner-sa@test-project.iam.gserviceaccount.com", email)
	require.Equal(t, "KEY_ALG_RSA_2048", outputs.must(t, "key_algorithm"))

	keyName := outputs.must(t, "key_name")
	require.Contains(t, keyName,
		"projects/test-project/serviceAccounts/"+email+"/keys/",
		"key name must carry the canonical {serviceAccount}/keys/{keyId} path; got %s", keyName)
	keyID := keyName[strings.LastIndex(keyName, "/")+1:]

	// The credential file IAM issues, decoded from the base64 private_key the
	// provider stored.
	raw, err := base64.StdEncoding.DecodeString(outputs.must(t, "key_private_key"))
	require.NoError(t, err, "private_key must be the base64 credential file IAM returns")
	var keyFile map[string]string
	require.NoError(t, json.Unmarshal(raw, &keyFile),
		"the decoded private_key must be the JSON credential file: %s", raw)
	require.Equal(t, "service_account", keyFile["type"])
	require.Equal(t, "test-project", keyFile["project_id"])
	require.Equal(t, email, keyFile["client_email"])
	require.Equal(t, keyID, keyFile["private_key_id"],
		"the credential file's private_key_id must be the key id the resource name carries")
	require.Contains(t, keyFile["private_key"], "BEGIN PRIVATE KEY")
	require.NotEmpty(t, keyFile["client_id"])
	require.NotEmpty(t, keyFile["auth_uri"])
	require.NotEmpty(t, keyFile["token_uri"])
	require.NotEmpty(t, keyFile["auth_provider_x509_cert_url"])
	require.NotEmpty(t, keyFile["client_x509_cert_url"])

	assertKeyFileAuthenticates(t, keyFile)
}

// assertKeyFileAuthenticates drives the credential file through the flow a
// Google client runs with it: sign a JWT-bearer assertion with its private key,
// exchange it at the token endpoint, and read the data plane with the token
// that comes back. The token endpoint verifies the assertion against the public
// half CreateServiceAccountKey registered, so this passes only for a genuinely
// issued key.
func assertKeyFileAuthenticates(t *testing.T, keyFile map[string]string) {
	t.Helper()

	// The token endpoint coordinate: the credential file names real Google's,
	// as an IAM-issued file does, and a client pointed at the simulator swaps
	// that one coordinate.
	tokenURL := baseURL + "/token"
	assertion := signJWTBearerAssertion(t, keyFile, tokenURL)

	resp, err := http.PostForm(tokenURL, url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	})
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"the token endpoint must accept an assertion signed with the provisioned key: %s", body)

	var token struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.Unmarshal(body, &token))
	require.NotEmpty(t, token.AccessToken)

	// Addressed at baseURL, the simulator's own endpoint, exactly as the token
	// exchange above is. tfEndpoint is the TLS gateway terraform is pointed at;
	// these assertions talk to the simulator directly and so do not need the
	// gateway's certificate authority.
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/projects/test-project/serviceAccounts", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	read, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer read.Body.Close()
	readBody, _ := io.ReadAll(read.Body)
	require.Equal(t, http.StatusOK, read.StatusCode,
		"the data plane must accept the token minted from the provisioned key: %s", readBody)
}

// signJWTBearerAssertion builds the RS256 assertion a Google client signs with
// a service-account credential file: issuer and subject are the account's
// email, the audience is the token endpoint, the `kid` header names the key id,
// and the signature is made with the file's own private key.
func signJWTBearerAssertion(t *testing.T, keyFile map[string]string, audience string) string {
	t.Helper()

	block, _ := pem.Decode([]byte(keyFile["private_key"]))
	require.NotNil(t, block, "the credential file's private_key must be PEM")
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	require.NoError(t, err, "the credential file's private_key must be PKCS#8")
	priv, ok := parsed.(*rsa.PrivateKey)
	require.True(t, ok, "the credential file's private_key must be RSA, got %T", parsed)

	encode := func(v any) string {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	now := time.Now()
	signingInput := encode(map[string]string{
		"alg": "RS256", "typ": "JWT", "kid": keyFile["private_key_id"],
	}) + "." + encode(map[string]any{
		"iss":   keyFile["client_email"],
		"sub":   keyFile["client_email"],
		"aud":   audience,
		"scope": "https://www.googleapis.com/auth/cloud-platform",
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	require.NoError(t, err)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}
