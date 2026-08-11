package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func acmRequestUnknownPrivateCACLI(t *testing.T, domain string) {
	t.Helper()
	out, err := awsCLI("acm", "request-certificate",
		"--domain-name", domain,
		"--certificate-authority-arn", "arn:aws:acm-pca:us-east-1:000000000000:certificate-authority/test-ca",
		"--output", "json").CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "ResourceNotFoundException")
}

func TestACMCLI_RejectsUnknownPrivateCertificateAuthority(t *testing.T) {
	acmRequestUnknownPrivateCACLI(t, "unknown-ca.cli.private.example.com")
}

func TestACMCLI_ExportCertificate(t *testing.T) {
	created := runCLI(t, awsCLI("acm", "request-certificate",
		"--domain-name", "export.cli.public.example.com",
		"--validation-method", "DNS",
		"--output", "json"))
	var certificate struct {
		CertificateArn string `json:"CertificateArn"`
	}
	parseJSON(t, created, &certificate)
	out, err := awsCLI("acm", "export-certificate",
		"--certificate-arn", certificate.CertificateArn,
		"--passphrase", "hunter2",
		"--cli-binary-format", "raw-in-base64-out",
		"--output", "json").CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "RequestInProgressException")
}

func TestACMCLI_RevokeCertificate(t *testing.T) {
	created := runCLI(t, awsCLI("acm", "request-certificate",
		"--domain-name", "revoke.cli.public.example.com",
		"--validation-method", "DNS",
		"--output", "json"))
	var certificate struct {
		CertificateArn string `json:"CertificateArn"`
	}
	parseJSON(t, created, &certificate)
	out, err := awsCLI("acm", "revoke-certificate",
		"--certificate-arn", certificate.CertificateArn,
		"--revocation-reason", "KEY_COMPROMISE",
		"--output", "json").CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "ResourceInUseException")
}

func TestACMCLI_AccountConfiguration(t *testing.T) {
	runCLI(t, awsCLI("acm", "put-account-configuration",
		"--expiry-events", "DaysBeforeExpiry=21",
		"--idempotency-token", "cli-acct-config-tok"))

	out := runCLI(t, awsCLI("acm", "get-account-configuration", "--output", "json"))
	var res struct {
		ExpiryEvents struct {
			DaysBeforeExpiry int `json:"DaysBeforeExpiry"`
		} `json:"ExpiryEvents"`
	}
	parseJSON(t, out, &res)
	assert.Equal(t, 21, res.ExpiryEvents.DaysBeforeExpiry)
}

func TestACMCLI_SearchCertificates(t *testing.T) {
	acmRequestUnknownPrivateCACLI(t, "search.cli.private.example.com")

	out := runCLI(t, awsCLI("acm", "search-certificates",
		"--filter-statement", `{"Filter":{"AcmCertificateMetadataFilter":{"Type":"PRIVATE"}}}`,
		"--output", "json"))
	var res struct {
		Results []struct {
			CertificateArn      string `json:"CertificateArn"`
			CertificateMetadata struct {
				AcmCertificateMetadata struct {
					Type string `json:"Type"`
				} `json:"AcmCertificateMetadata"`
			} `json:"CertificateMetadata"`
		} `json:"Results"`
	}
	parseJSON(t, out, &res)
	var foundSynthetic bool
	for _, r := range res.Results {
		assert.Equal(t, "PRIVATE", r.CertificateMetadata.AcmCertificateMetadata.Type,
			"search filtered by Type=PRIVATE must only return PRIVATE certs")
		if r.CertificateArn != "" {
			foundSynthetic = true
		}
	}
	assert.False(t, foundSynthetic, "a missing AWS Private CA must not produce a synthetic certificate")
}
