package azure_cli_test

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kvDataRest dispatches az rest to the KV data-plane on the sim.
// Real az keyvault commands use HTTPS from the ARM-returned vault URI;
// the sim advertises https:// but runs plain HTTP.  We bypass DNS and
// TLS by targeting baseURL (127.0.0.1:port) directly and routing to
// the right vault via the Host header — the sim's WrapHandler checks
// for ".vault." in Host and dispatches accordingly.
// Requests pre-carry "Authorization: Bearer …" to skip the 401
// challenge (the sim trusts any Bearer token for data-plane ops).
func kvDataRest(method, vaultName, path, body string) *exec.Cmd {
	port := strings.TrimPrefix(baseURL, "http://127.0.0.1:")
	host := fmt.Sprintf("%s.vault.cli-shim.localhost:%s", vaultName, port)
	args := []string{
		"rest", "--method", method,
		"--url", baseURL + path + "?api-version=7.4",
		"--headers", "Host=" + host, "Authorization=Bearer cli-test-token",
		"--output", "json",
	}
	if body != "" {
		args = append(args, "--body", body)
	}
	cmd := exec.Command("az", args...)
	cmd.Env = append(os.Environ(),
		"AZURE_CONFIG_DIR="+filepath.Join(tmpDir, "azure-config"),
		"AZURE_CORE_NO_COLOR=1",
	)
	return cmd
}

// createKVVaultCLI provisions a KV vault via ARM and registers a t.Cleanup
// to delete it.
func createKVVaultCLI(t *testing.T, vaultName string) {
	t.Helper()
	url := armURL("Microsoft.KeyVault", "vaults/"+vaultName, "2024-11-01")
	runCLI(t, azRest("PUT", url, `{"location":"eastus","properties":{"tenantId":"00000000-0000-0000-0000-000000000000","sku":{"family":"A","name":"standard"}}}`))
	t.Cleanup(func() {
		_ = azRest("DELETE", url, "").Run()
	})
}

// TestKVDataPlane_Secrets_CLI exercises the KV secrets data-plane via az rest:
// SetSecret → GetSecret → ListSecrets → DeleteSecret.
func TestKVDataPlane_Secrets_CLI(t *testing.T) {
	vault := "cli-dp-secrets"
	createKVVaultCLI(t, vault)

	// SetSecret
	out := runCLI(t, kvDataRest("PUT", vault, "/secrets/cli-secret", `{"value":"hello-from-cli"}`))
	var setResp struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	parseJSON(t, out, &setResp)
	assert.NotEmpty(t, setResp.ID, "SetSecret must return a secret id")
	assert.Equal(t, "hello-from-cli", setResp.Value)

	// GetSecret
	out = runCLI(t, kvDataRest("GET", vault, "/secrets/cli-secret", ""))
	var getResp struct {
		Value string `json:"value"`
	}
	parseJSON(t, out, &getResp)
	assert.Equal(t, "hello-from-cli", getResp.Value)

	// ListSecrets — created secret must appear
	out = runCLI(t, kvDataRest("GET", vault, "/secrets", ""))
	var listResp struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	parseJSON(t, out, &listResp)
	found := false
	for _, s := range listResp.Value {
		if strings.Contains(s.ID, "cli-secret") {
			found = true
		}
	}
	assert.True(t, found, "ListSecrets must include the created secret")

	// DeleteSecret
	runCLI(t, kvDataRest("DELETE", vault, "/secrets/cli-secret", ""))
}

// TestKVDataPlane_Keys_CLI exercises the KV keys data-plane via az rest:
// CreateKey → GetKey → ListKeys → DeleteKey.
func TestKVDataPlane_Keys_CLI(t *testing.T) {
	vault := "cli-dp-keys"
	createKVVaultCLI(t, vault)

	// CreateKey
	out := runCLI(t, kvDataRest("POST", vault, "/keys/cli-key/create", `{"kty":"RSA","key_size":2048}`))
	var createResp struct {
		Key struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
		} `json:"key"`
	}
	parseJSON(t, out, &createResp)
	assert.NotEmpty(t, createResp.Key.Kid, "CreateKey must return a key ID")
	assert.Equal(t, "RSA", createResp.Key.Kty)

	// GetKey
	out = runCLI(t, kvDataRest("GET", vault, "/keys/cli-key", ""))
	var getResp struct {
		Key struct {
			Kid string `json:"kid"`
		} `json:"key"`
	}
	parseJSON(t, out, &getResp)
	assert.NotEmpty(t, getResp.Key.Kid)

	// Version-less crypto (POST /keys/{name}/encrypt|decrypt) must target the
	// current version, not 405.
	pt := base64.RawURLEncoding.EncodeToString([]byte("cli-secret"))
	out = runCLI(t, kvDataRest("POST", vault, "/keys/cli-key/encrypt", `{"alg":"RSA-OAEP","value":"`+pt+`"}`))
	var encResp struct {
		Value string `json:"value"`
	}
	parseJSON(t, out, &encResp)
	require.NotEmpty(t, encResp.Value, "version-less encrypt must succeed, not 405")

	out = runCLI(t, kvDataRest("POST", vault, "/keys/cli-key/decrypt", `{"alg":"RSA-OAEP","value":"`+encResp.Value+`"}`))
	var decResp struct {
		Value string `json:"value"`
	}
	parseJSON(t, out, &decResp)
	dec, err := base64.RawURLEncoding.DecodeString(decResp.Value)
	require.NoError(t, err)
	require.Equal(t, "cli-secret", string(dec))

	// ListKeys — created key must appear
	out = runCLI(t, kvDataRest("GET", vault, "/keys", ""))
	var listResp struct {
		Value []struct {
			Kid string `json:"kid"`
		} `json:"value"`
	}
	parseJSON(t, out, &listResp)
	found := false
	for _, k := range listResp.Value {
		if strings.Contains(k.Kid, "cli-key") {
			found = true
		}
	}
	assert.True(t, found, "ListKeys must include the created key")

	// DeleteKey
	runCLI(t, kvDataRest("DELETE", vault, "/keys/cli-key", ""))
}

// TestKVDataPlane_Certificates_CLI exercises the KV certificates data-plane
// via az rest: CreateCertificate → GetCertificate → ListCertificates →
// DeleteCertificate.
func TestKVDataPlane_Certificates_CLI(t *testing.T) {
	vault := "cli-dp-certs"
	createKVVaultCLI(t, vault)

	policy := `{"issuer":{"name":"Self"},"x509_props":{"subject":"CN=cli-test","validity_months":12}}`

	// CreateCertificate — sim returns 202 + CertificateOperation (status: completed)
	out := runCLI(t, kvDataRest("POST", vault, "/certificates/cli-cert/create", `{"policy":`+policy+`}`))
	var pending struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	parseJSON(t, out, &pending)
	assert.NotEmpty(t, pending.ID, "CreateCertificate must return an operation id")

	// GetCertificate — sim completes the operation synchronously
	out = runCLI(t, kvDataRest("GET", vault, "/certificates/cli-cert", ""))
	var getResp struct {
		ID  string `json:"id"`
		Cer string `json:"cer"`
	}
	parseJSON(t, out, &getResp)
	assert.NotEmpty(t, getResp.ID, "GetCertificate must return a certificate id")
	// CertificatePolicy uses the snake_case wire member names, not the
	// SDK client names.
	assert.Contains(t, out, `"x509_props"`)
	assert.Contains(t, out, `"issuer"`)
	assert.NotContains(t, out, `"issuerParameters"`)
	assert.NotContains(t, out, `"x509CertificateProperties"`)

	// ListCertificates — created cert must appear
	out = runCLI(t, kvDataRest("GET", vault, "/certificates", ""))
	var listResp struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	parseJSON(t, out, &listResp)
	found := false
	for _, c := range listResp.Value {
		if strings.Contains(c.ID, "cli-cert") {
			found = true
		}
	}
	assert.True(t, found, "ListCertificates must include the created certificate")

	// DeleteCertificate
	runCLI(t, kvDataRest("DELETE", vault, "/certificates/cli-cert", ""))
}
