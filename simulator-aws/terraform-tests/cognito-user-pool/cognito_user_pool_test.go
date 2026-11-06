package cognito_user_pool_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCognitoUserPoolTerraform creates an Amazon Cognito user pool with a
// password policy and a custom attribute, an app client for an Application
// Load Balancer's authenticate action, a prefix domain and a user with a
// permanent password through terraform-provider-aws; a second plan finds
// nothing to change. An HTTPS listener of an Application Load Balancer signs
// users in through the pool with an authenticate_cognito default action.
func TestCognitoUserPoolTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	certificate, key := selfSignedCertificate(t)
	vars := []string{"-var", "certificate_pem=" + certificate, "-var", "private_key_pem=" + key}
	env.Terraform(t, append([]string{"apply", "-auto-approve"}, vars...)...)

	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	poolID := outputs["pool_id"].Value
	assert.True(t, strings.HasPrefix(poolID, "us-east-1_"), poolID)
	assert.Equal(t, "arn:aws:cognito-idp:us-east-1:123456789012:userpool/"+poolID, outputs["pool_arn"].Value)
	assert.Equal(t, "tf-login", outputs["pool_domain"].Value)
	assert.NotEmpty(t, outputs["client_id"].Value)
	assert.NotEmpty(t, outputs["client_secret"].Value, "the client generated a secret")
	assert.Equal(t, "CONFIRMED", outputs["user_status"].Value, "a user created with a password has it permanently")
	assert.NotEmpty(t, outputs["user_sub"].Value)

	assert.Equal(t, "TfSession", outputs["listener_cookie"].Value)
	assert.Equal(t, "tf-login", outputs["listener_domain"].Value)

	env.Terraform(t, append([]string{"plan", "-detailed-exitcode"}, vars...)...)
	env.Terraform(t, append([]string{"destroy", "-auto-approve"}, vars...)...)
}

func selfSignedCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "tf-cognito-alb.example.com"},
		DNSNames: []string{"tf-cognito-alb.example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}
