package azure_tf_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTerraformWebAppKeyVaultCertificateAndContainerAppSecrets provisions one
// vault whose access policies grant App Service's first-party service
// principal, a user-assigned identity and the caller, each through its own
// azurerm_key_vault_access_policy, and the resources that read the vault: an
// azurerm_app_service_certificate imported from it, and an
// azurerm_container_app and azurerm_container_app_job whose secrets reference
// it through the user-assigned identity. The certificate carries the
// thumbprint of the certificate the vault holds, and the app answers with the
// secret's value and the token its identity endpoint issues the identity.
func TestTerraformWebAppKeyVaultCertificateAndContainerAppSecrets(t *testing.T) {
	t.Parallel()
	bundle, thumbprint := kvConsumersCertificate(t)
	dir := tfWorkspaceFrom(t, mustAbs("kvconsumers"))
	run := func(name string, args ...string) {
		t.Helper()
		cmd := terraformCmd(dir, args...)
		cmd.Env = append(cmd.Env, "TF_VAR_certificate_pem="+bundle)
		out, err := runTimed(t, name, cmd)
		require.NoError(t, err, "%s failed:\n%s", name, out)
	}
	run("terraform init", "init")
	run("terraform apply", "apply", "-auto-approve")
	run("terraform plan", "plan", "-detailed-exitcode")

	outputs := readOutputs(t, dir)
	require.Equal(t, thumbprint, outputs.must(t, "certificate_thumbprint"),
		"App Service reads the certificate as the service principal the vault grants")

	want := []string{
		outputs.must(t, "app_service_principal_id"),
		outputs.must(t, "reader_principal_id"),
		"00000000-0000-0000-0000-0000000000b2",
	}
	sort.Strings(want)
	require.Equal(t, want, outputs.mustList(t, "access_policy_object_ids"),
		"each access policy resource adds its own policy and keeps the others")
	require.Len(t, outputs.mustList(t, "job_identity_ids"), 1)
	require.NotEmpty(t, outputs.must(t, "app_principal_id"), "the app has a system-assigned identity")

	served := strings.SplitN(slotSiteServes(t, outputs.must(t, "app_fqdn")), "\n", 2)
	require.Len(t, served, 2, "the app answers its secret and its identity's token")
	require.Equal(t, "hunter2", served[0], "the app sees the value of the Key Vault secret its secret references")
	var token struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.Unmarshal([]byte(served[1]), &token), served[1])
	parts := strings.Split(token.AccessToken, ".")
	require.Len(t, parts, 3, served[1])
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	require.Equal(t, outputs.must(t, "reader_principal_id"), claims["oid"],
		"the app's identity endpoint issues the token of the user-assigned identity it names")

	run("terraform destroy", "destroy", "-auto-approve")
}

// kvConsumersCertificate issues a self-signed certificate and returns the PEM
// bundle of it and its key, as the secret of a Key Vault certificate with the
// application/x-pem-file content type holds it, and its SHA-1 thumbprint.
func kvConsumersCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "kvconsumers.example.com"},
		DNSNames:     []string{"kvconsumers.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	bundle := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) +
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	sum := sha1.Sum(der)
	return bundle, strings.ToUpper(hex.EncodeToString(sum[:]))
}
