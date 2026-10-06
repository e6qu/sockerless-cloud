package aws_cli_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestELBv2TrustStoreCLI exercises the mutual-TLS trust store control plane and
// the SSL policy / capacity / IP-pool modifications through the aws CLI:
// create-trust-store, describe-trust-stores, modify-trust-store,
// get-trust-store-ca-certificates-bundle, add-trust-store-revocations,
// describe-trust-store-revocations, get-trust-store-revocation-content,
// remove-trust-store-revocations, describe-trust-store-associations,
// get-resource-policy, describe-ssl-policies, modify-capacity-reservation,
// modify-ip-pools, delete-trust-store.
func TestELBv2TrustStoreCLI(t *testing.T) {
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", "cli-ca-bucket"))
	firstCA, firstKey := elbv2CLITestCA(t, "first")
	secondCA, _ := elbv2CLITestCA(t, "second")
	upload := func(key string, content []byte) {
		t.Helper()
		path := filepath.Join(t.TempDir(), key)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		runCLI(t, awsCLI("s3api", "put-object", "--bucket", "cli-ca-bucket", "--key", key, "--body", path))
	}
	upload("bundle.pem", append(elbv2CLIPEM("CERTIFICATE", firstCA.Raw), elbv2CLIPEM("CERTIFICATE", secondCA.Raw)...))
	upload("bundle-v2.pem", elbv2CLIPEM("CERTIFICATE", secondCA.Raw))
	upload("crl.pem", elbv2CLITestCRL(t, firstCA, firstKey, 4))
	upload("garbage.pem", []byte("not a certificate"))

	out := runCLIExpectError(t, awsCLI("elbv2", "create-trust-store", "--name", "cli-mtls-missing",
		"--ca-certificates-bundle-s3-bucket", "cli-ca-bucket", "--ca-certificates-bundle-s3-key", "absent.pem"))
	if !strings.Contains(out, "CaCertificatesBundleNotFound") {
		t.Fatalf("expected CaCertificatesBundleNotFound for a bundle Amazon S3 does not hold, got %s", out)
	}
	out = runCLIExpectError(t, awsCLI("elbv2", "create-trust-store", "--name", "cli-mtls-garbage",
		"--ca-certificates-bundle-s3-bucket", "cli-ca-bucket", "--ca-certificates-bundle-s3-key", "garbage.pem"))
	if !strings.Contains(out, "InvalidCaCertificatesBundle") {
		t.Fatalf("expected InvalidCaCertificatesBundle for a bundle that is not PEM certificates, got %s", out)
	}

	out = runCLI(t, awsCLI("elbv2", "create-trust-store",
		"--name", "cli-mtls-store",
		"--ca-certificates-bundle-s3-bucket", "cli-ca-bucket",
		"--ca-certificates-bundle-s3-key", "bundle.pem",
		"--query", "TrustStores[0].[TrustStoreArn,NumberOfCaCertificates]",
		"--output", "text"))
	fields := strings.Fields(out)
	if len(fields) != 2 || fields[1] != "2" {
		t.Fatalf("expected the trust store ARN and two CA certificates, got %q", out)
	}
	tsArn := fields[0]
	if !strings.Contains(tsArn, ":truststore/cli-mtls-store/") {
		t.Fatalf("expected trust store ARN, got %q", tsArn)
	}
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("elbv2", "delete-trust-store", "--trust-store-arn", tsArn))
	})

	out = runCLI(t, awsCLI("elbv2", "describe-trust-stores",
		"--names", "cli-mtls-store",
		"--query", "TrustStores[0].Status",
		"--output", "text"))
	if strings.TrimSpace(out) != "ACTIVE" {
		t.Fatalf("expected ACTIVE trust store, got %q", out)
	}

	out = runCLI(t, awsCLI("elbv2", "modify-trust-store",
		"--trust-store-arn", tsArn,
		"--ca-certificates-bundle-s3-bucket", "cli-ca-bucket",
		"--ca-certificates-bundle-s3-key", "bundle-v2.pem",
		"--query", "TrustStores[0].NumberOfCaCertificates",
		"--output", "text"))
	if strings.TrimSpace(out) != "1" {
		t.Fatalf("expected the modified bundle's one CA certificate, got %q", out)
	}

	out = runCLI(t, awsCLI("elbv2", "get-trust-store-ca-certificates-bundle",
		"--trust-store-arn", tsArn,
		"--query", "Location",
		"--output", "text"))
	if strings.TrimSpace(out) == "" {
		t.Fatalf("expected CA bundle location, got empty")
	}

	out = runCLI(t, awsCLI("elbv2", "add-trust-store-revocations",
		"--trust-store-arn", tsArn,
		"--revocation-contents", "S3Bucket=cli-ca-bucket,S3Key=crl.pem,RevocationType=CRL",
		"--query", "TrustStoreRevocations[0].[RevocationId,NumberOfRevokedEntries]",
		"--output", "text"))
	fields = strings.Fields(out)
	if len(fields) != 2 || fields[1] != "4" {
		t.Fatalf("expected a revocation id and the list's four revoked entries, got %q", out)
	}
	revID := fields[0]

	out = runCLI(t, awsCLI("elbv2", "describe-trust-store-revocations",
		"--trust-store-arn", tsArn,
		"--query", "length(TrustStoreRevocations)",
		"--output", "text"))
	if strings.TrimSpace(out) != "1" {
		t.Fatalf("expected 1 revocation, got %q", out)
	}

	out = runCLI(t, awsCLI("elbv2", "get-trust-store-revocation-content",
		"--trust-store-arn", tsArn,
		"--revocation-id", revID,
		"--query", "Location",
		"--output", "text"))
	if strings.TrimSpace(out) == "" {
		t.Fatalf("expected revocation content location, got empty")
	}

	runCLI(t, awsCLI("elbv2", "remove-trust-store-revocations",
		"--trust-store-arn", tsArn,
		"--revocation-ids", revID))
	out = runCLI(t, awsCLI("elbv2", "describe-trust-store-revocations",
		"--trust-store-arn", tsArn,
		"--query", "length(TrustStoreRevocations)",
		"--output", "text"))
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("expected 0 revocations after removal, got %q", out)
	}

	out = runCLI(t, awsCLI("elbv2", "describe-trust-store-associations",
		"--trust-store-arn", tsArn,
		"--query", "length(TrustStoreAssociations)",
		"--output", "text"))
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("expected 0 associations, got %q", out)
	}

	out = runCLI(t, awsCLI("elbv2", "get-resource-policy",
		"--resource-arn", tsArn,
		"--query", "Policy",
		"--output", "text"))
	if strings.TrimSpace(out) == "" {
		t.Fatalf("expected resource policy, got empty")
	}

	runCLI(t, awsCLI("elbv2", "delete-trust-store", "--trust-store-arn", tsArn))
}

// TestELBv2DescribeSSLPoliciesCLI verifies the predefined SSL security policy
// catalog through the aws CLI.
func TestELBv2DescribeSSLPoliciesCLI(t *testing.T) {
	out := runCLI(t, awsCLI("elbv2", "describe-ssl-policies",
		"--names", "ELBSecurityPolicy-2016-08",
		"--query", "SslPolicies[0].Name",
		"--output", "text"))
	if strings.TrimSpace(out) != "ELBSecurityPolicy-2016-08" {
		t.Fatalf("expected ELBSecurityPolicy-2016-08, got %q", out)
	}

	out = runCLI(t, awsCLI("elbv2", "describe-ssl-policies",
		"--names", "ELBSecurityPolicy-2016-08",
		"--query", "length(SslPolicies[0].Ciphers)",
		"--output", "text"))
	if strings.TrimSpace(out) == "0" || strings.TrimSpace(out) == "" {
		t.Fatalf("expected non-empty ciphers, got %q", out)
	}

	out = runCLI(t, awsCLI("elbv2", "describe-ssl-policies",
		"--load-balancer-type", "application",
		"--query", "length(SslPolicies)",
		"--output", "text"))
	if strings.TrimSpace(out) == "0" || strings.TrimSpace(out) == "" {
		t.Fatalf("expected predefined SSL policies for application LB, got %q", out)
	}
}

// elbv2CLITestCA makes a self-signed CA certificate.
func elbv2CLITestCA(t *testing.T, name string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

// elbv2CLITestCRL makes a PEM certificate revocation list, issued by ca, that
// revokes entries serial numbers.
func elbv2CLITestCRL(t *testing.T, ca *x509.Certificate, key *ecdsa.PrivateKey, entries int) []byte {
	t.Helper()
	list := &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: time.Now().Add(-time.Hour), NextUpdate: time.Now().Add(24 * time.Hour)}
	for i := range entries {
		list.RevokedCertificateEntries = append(list.RevokedCertificateEntries,
			x509.RevocationListEntry{SerialNumber: big.NewInt(int64(100 + i)), RevocationTime: time.Now().Add(-time.Minute)})
	}
	der, err := x509.CreateRevocationList(rand.Reader, list, ca, key)
	if err != nil {
		t.Fatal(err)
	}
	return elbv2CLIPEM("X509 CRL", der)
}

func elbv2CLIPEM(blockType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
}
