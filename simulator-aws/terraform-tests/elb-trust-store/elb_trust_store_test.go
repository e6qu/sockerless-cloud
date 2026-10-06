package elb_trust_store_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestELBTrustStoreTerraform creates an Elastic Load Balancing trust store
// through terraform-provider-aws from a CA certificates bundle it uploads to
// Amazon S3, and adds a certificate revocation list to it. Elastic Load
// Balancing reads both objects: the trust store counts the bundle's three
// certificates and the revocation its two entries.
func TestELBTrustStoreTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")

	var bundle strings.Builder
	var issuer *x509.Certificate
	var issuerKey *ecdsa.PrivateKey
	for _, name := range []string{"root", "partner", "legacy"} {
		cert, key := testCA(t, name)
		bundle.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
		issuer, issuerKey = cert, key
	}
	vars := []string{"-var", "ca_bundle=" + bundle.String(), "-var", "crl=" + testCRL(t, issuer, issuerKey, 2)}

	env.Terraform(t, append([]string{"apply", "-auto-approve"}, vars...)...)
	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	arn := outputs["trust_store_arn"].Value
	require.Contains(t, arn, ":truststore/tf-mtls/")
	assert.NotEmpty(t, outputs["revocation_id"].Value)

	// The provider does not expose the counts, so read them back through the
	// Elastic Load Balancing API.
	client := elbv2.New(elbv2.Options{Region: "us-east-1", BaseEndpoint: aws.String(env.Endpoint), HTTPClient: env.Client,
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")})
	described, err := client.DescribeTrustStores(context.Background(), &elbv2.DescribeTrustStoresInput{TrustStoreArns: []string{arn}})
	require.NoError(t, err)
	require.Len(t, described.TrustStores, 1)
	assert.Equal(t, int32(3), aws.ToInt32(described.TrustStores[0].NumberOfCaCertificates))
	assert.Equal(t, int64(2), aws.ToInt64(described.TrustStores[0].TotalRevokedEntries))

	env.Terraform(t, append([]string{"destroy", "-auto-approve"}, vars...)...)
}

func testCA(t *testing.T, name string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name + " CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert, key
}

func testCRL(t *testing.T, ca *x509.Certificate, key *ecdsa.PrivateKey, entries int) string {
	t.Helper()
	list := &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: time.Now().Add(-time.Hour), NextUpdate: time.Now().Add(24 * time.Hour)}
	for i := range entries {
		list.RevokedCertificateEntries = append(list.RevokedCertificateEntries,
			x509.RevocationListEntry{SerialNumber: big.NewInt(int64(100 + i)), RevocationTime: time.Now().Add(-time.Minute)})
	}
	der, err := x509.CreateRevocationList(rand.Reader, list, ca, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der}))
}
