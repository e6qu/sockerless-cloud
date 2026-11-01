package gcp_tf_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A google_sql_ssl_cert holds a client certificate and key the instance's
// server CA signed, google_sql_ca_certs reads the active server CA, and the
// instance's data plane presents a server certificate that CA issued.
func TestTerraformCloudSQLCertificates(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "cloudsql-certificates")
	cleanTerraformFixture(t, fixtureDir)

	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	out, err = runTimed(t, "terraform apply", terraformCmdInDir(fixtureDir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", terraformCmdInDir(fixtureDir, "destroy", "-auto-approve"))
		assert.NoError(t, err, "terraform destroy failed:\n%s", out)
	})

	outputs := readOutputsInDir(t, fixtureDir)
	serverCA := outputs.must(t, "instance_server_ca")
	assert.Equal(t, outputs.must(t, "instance_server_ca_fingerprint"), outputs.must(t, "active_ca_version"))
	assert.Equal(t, serverCA, outputs.must(t, "client_server_ca"))

	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM([]byte(serverCA)))
	block, _ := pem.Decode([]byte(outputs.must(t, "client_cert")))
	require.NotNil(t, block)
	client, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "tf-client", client.Subject.CommonName)
	_, err = client.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	require.NoError(t, err, "the server CA signs the client certificate")
	_, err = tls.X509KeyPair([]byte(outputs.must(t, "client_cert")), []byte(outputs.must(t, "client_private_key")))
	require.NoError(t, err, "the private key belongs to the client certificate")

	// PostgreSQL's SSLRequest, then a handshake that trusts only the server CA.
	raw, err := net.DialTimeout("tcp", net.JoinHostPort(outputs.must(t, "address"), "5432"), 30*time.Second)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Write([]byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f})
	require.NoError(t, err)
	answer := make([]byte, 1)
	_, err = raw.Read(answer)
	require.NoError(t, err)
	require.Equal(t, byte('S'), answer[0])
	session := tls.Client(raw, &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // VerifyPeerCertificate checks the chain against the server CA, as sslmode=verify-ca does.
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return err
			}
			_, err = leaf.Verify(x509.VerifyOptions{Roots: roots})
			return err
		},
	})
	require.NoError(t, session.Handshake(), "the data plane presents a certificate the server CA issued")
}
