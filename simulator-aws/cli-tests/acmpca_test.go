package aws_cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrivateCACLI_RootAuthorityLifecycle(t *testing.T) {
	config := `{"KeyAlgorithm":"RSA_2048","SigningAlgorithm":"SHA256WITHRSA","Subject":{"CommonName":"CLI Private Root","Organization":"Sockerless"}}`
	created := runCLI(t, awsCLI("acm-pca", "create-certificate-authority",
		"--certificate-authority-configuration", config,
		"--certificate-authority-type", "ROOT",
		"--idempotency-token", "cli-private-root",
		"--tags", "Key=environment,Value=cli",
		"--output", "json"))
	var authority struct {
		CertificateAuthorityARN string `json:"CertificateAuthorityArn"`
	}
	parseJSON(t, created, &authority)
	require.NotEmpty(t, authority.CertificateAuthorityARN)
	t.Cleanup(func() {
		_, _ = awsCLI("acm-pca", "update-certificate-authority",
			"--certificate-authority-arn", authority.CertificateAuthorityARN, "--status", "DISABLED").CombinedOutput()
		_, _ = awsCLI("acm-pca", "delete-certificate-authority",
			"--certificate-authority-arn", authority.CertificateAuthorityARN,
			"--permanent-deletion-time-in-days", "7").CombinedOutput()
	})

	csrOutput := runCLI(t, awsCLI("acm-pca", "get-certificate-authority-csr",
		"--certificate-authority-arn", authority.CertificateAuthorityARN, "--output", "json"))
	var csr struct {
		CSR string `json:"Csr"`
	}
	parseJSON(t, csrOutput, &csr)
	csrPath := filepath.Join(t.TempDir(), "root.csr")
	require.NoError(t, os.WriteFile(csrPath, []byte(csr.CSR), 0o600))

	issued := runCLI(t, awsCLI("acm-pca", "issue-certificate",
		"--certificate-authority-arn", authority.CertificateAuthorityARN,
		"--csr", "fileb://"+csrPath,
		"--signing-algorithm", "SHA256WITHRSA",
		"--template-arn", "arn:aws:acm-pca:::template/RootCACertificate/V1",
		"--validity", "Value=10,Type=YEARS",
		"--idempotency-token", "cli-private-root-cert",
		"--output", "json"))
	var certificate struct {
		CertificateARN string `json:"CertificateArn"`
	}
	parseJSON(t, issued, &certificate)
	require.NotEmpty(t, certificate.CertificateARN)

	material := runCLI(t, awsCLI("acm-pca", "get-certificate",
		"--certificate-authority-arn", authority.CertificateAuthorityARN,
		"--certificate-arn", certificate.CertificateARN, "--output", "json"))
	var root struct {
		Certificate string `json:"Certificate"`
	}
	parseJSON(t, material, &root)
	certificatePath := filepath.Join(t.TempDir(), "root.pem")
	require.NoError(t, os.WriteFile(certificatePath, []byte(root.Certificate), 0o600))
	runCLI(t, awsCLI("acm-pca", "import-certificate-authority-certificate",
		"--certificate-authority-arn", authority.CertificateAuthorityARN,
		"--certificate", "fileb://"+certificatePath))

	described := runCLI(t, awsCLI("acm-pca", "describe-certificate-authority",
		"--certificate-authority-arn", authority.CertificateAuthorityARN, "--output", "json"))
	assert.Contains(t, described, `"Status": "ACTIVE"`)
	assert.Contains(t, runCLI(t, awsCLI("acm-pca", "list-certificate-authorities", "--output", "json")),
		authority.CertificateAuthorityARN)
	assert.Contains(t, runCLI(t, awsCLI("acm-pca", "get-certificate-authority-certificate",
		"--certificate-authority-arn", authority.CertificateAuthorityARN, "--output", "json")), "BEGIN CERTIFICATE")
	assert.Contains(t, runCLI(t, awsCLI("acm-pca", "list-tags",
		"--certificate-authority-arn", authority.CertificateAuthorityARN, "--output", "json")), "environment")

	runCLI(t, awsCLI("acm-pca", "create-permission",
		"--certificate-authority-arn", authority.CertificateAuthorityARN,
		"--principal", "acm.amazonaws.com", "--source-account", "000000000000",
		"--actions", "IssueCertificate", "GetCertificate", "ListPermissions"))
	assert.Contains(t, runCLI(t, awsCLI("acm-pca", "list-permissions",
		"--certificate-authority-arn", authority.CertificateAuthorityARN, "--output", "json")), "acm.amazonaws.com")
	runCLI(t, awsCLI("acm-pca", "delete-permission",
		"--certificate-authority-arn", authority.CertificateAuthorityARN,
		"--principal", "acm.amazonaws.com", "--source-account", "000000000000"))
}
