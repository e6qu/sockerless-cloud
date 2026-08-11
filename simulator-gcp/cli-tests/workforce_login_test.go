package gcp_cli_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// workforceIssuer is a minimal but real OpenID Connect issuer: it serves the
// discovery document and JSON Web Key Set the Security Token Service fetches
// to verify a subject token, and signs assertions with the matching RSA key.
// It stands in for the external identity provider a workforce pool federates.
type workforceIssuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
}

func newWorkforceIssuer(t *testing.T) *workforceIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate issuer key: %v", err)
	}
	issuer := &workforceIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer.server.URL,
			"jwks_uri":                              issuer.server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := &key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "workforce-login-key",
				"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})
	issuer.server = httptest.NewServer(mux)
	t.Cleanup(issuer.server.Close)
	return issuer
}

// mintAssertion signs the OpenID Connect assertion the external identity
// provider would issue for a signed-in user.
func (i *workforceIssuer) mintAssertion(t *testing.T, subject, audience string) string {
	t.Helper()
	encode := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal claim set: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	now := time.Now()
	signingInput := encode(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "workforce-login-key"}) + "." +
		encode(map[string]any{
			"iss": i.server.URL, "sub": subject, "aud": audience,
			"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
		})
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign assertion: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// TestWorkforceExternalAccountGcloudLogin drives the full Workforce Identity
// Federation login the `gcloud` CLI runs from an external_account credential
// file: `gcloud auth login --cred-file` exchanges the subject token at the
// Security Token Service token endpoint (`/v1/token`), resolves the federated
// principal through STS token introspection (`/v1/introspect`, the
// `token_info_url` coordinate), stores the workforce principal as the account
// name, and then authenticates a data-plane command with the federated
// credential — all against the simulator, differing from real Google only in
// the endpoint coordinates inside the credential file.
func TestWorkforceExternalAccountGcloudLogin(t *testing.T) {
	issuer := newWorkforceIssuer(t)
	const subject = "operator@example.test"
	const audience = "sockerless-cli"

	poolID := fmt.Sprintf("login-pool-%d", time.Now().UnixNano())
	poolName := "locations/global/workforcePools/" + poolID
	httpDoJSON(t, http.MethodPost,
		baseURL+"/v1/locations/global/workforcePools?workforcePoolId="+poolID,
		`{"displayName":"Workforce Login Pool","parent":"organizations/sockerless"}`)
	httpDoJSON(t, http.MethodPost,
		baseURL+"/v1/"+poolName+"/providers?workforcePoolProviderId=idp",
		fmt.Sprintf(`{"displayName":"Login IdP","oidc":{"issuerUri":%q,"clientId":%q},"attributeMapping":{"google.subject":"assertion.sub"}}`,
			issuer.server.URL, audience))

	workDir := t.TempDir()
	tokenPath := filepath.Join(workDir, "subject-token")
	if err := os.WriteFile(tokenPath, []byte(issuer.mintAssertion(t, subject, audience)), 0o600); err != nil {
		t.Fatal(err)
	}
	credentials := map[string]any{
		"type":               "external_account",
		"audience":           "//iam.googleapis.com/" + poolName + "/providers/idp",
		"subject_token_type": "urn:ietf:params:oauth:token-type:id_token",
		"token_url":          baseURL + "/v1/token",
		"token_info_url":     baseURL + "/v1/introspect",
		"credential_source":  map[string]string{"file": tokenPath},
	}
	credJSON, err := json.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	credPath := filepath.Join(workDir, "external-account.json")
	if err := os.WriteFile(credPath, credJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	// An isolated gcloud config dir carrying no pre-minted access token: the
	// stored external-account credential is what must authenticate.
	configDir := filepath.Join(workDir, "gcloud-config")
	gcloudEnv := []string{
		"CLOUDSDK_CONFIG=" + configDir,
		"CLOUDSDK_CORE_DISABLE_PROMPTS=1",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_CLOUDRESOURCEMANAGER=" + baseURL + "/",
	}
	gcloudWith := func(args ...string) *exec.Cmd {
		cmd := exec.Command("gcloud", args...)
		cmd.Env = append(os.Environ(), gcloudEnv...)
		return cmd
	}

	runCLI(t, gcloudWith("auth", "login", "--cred-file="+credPath, "--brief", "--quiet"))

	wantAccount := "principal://iam.googleapis.com/" + poolName + "/subject/" + subject
	accounts := runCLI(t, gcloudWith("auth", "list", "--format=value(account)"))
	if !strings.Contains(accounts, wantAccount) {
		t.Fatalf("gcloud stored account %q, want %q", strings.TrimSpace(accounts), wantAccount)
	}

	// The federated credential authenticates the data plane: gcloud refreshes
	// it itself through the token-exchange + introspection coordinates.
	projects := runCLI(t, gcloudWith("projects", "list", "--format=value(projectId)"))
	if !strings.Contains(projects, "test-project") {
		t.Fatalf("projects list with the federated credential returned %q", projects)
	}
}
