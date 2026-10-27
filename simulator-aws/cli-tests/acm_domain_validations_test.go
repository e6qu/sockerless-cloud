package aws_cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// ListCertificateDomainValidations and describe-certificate answer from one
// record, so the two views agree.
func TestACMCLI_ListCertificateDomainValidations(t *testing.T) {
	t.Parallel()
	const domain = "list-dv.cli.example.test"
	createdJSON := runCLI(t, awsCLI("acm", "request-certificate",
		"--domain-name", domain,
		"--validation-method", "DNS",
		"--subject-alternative-names", "alt."+domain,
		"--output", "json"))
	var created struct {
		CertificateArn string `json:"CertificateArn"`
	}
	require.NoError(t, json.Unmarshal([]byte(createdJSON), &created))
	require.NotEmpty(t, created.CertificateArn)

	describedJSON := runCLI(t, awsCLI("acm", "describe-certificate",
		"--certificate-arn", created.CertificateArn, "--output", "json"))
	var described struct {
		Certificate struct {
			DomainValidationOptions []struct {
				DomainName     string `json:"DomainName"`
				ResourceRecord struct {
					Name string `json:"Name"`
				} `json:"ResourceRecord"`
			} `json:"DomainValidationOptions"`
		} `json:"Certificate"`
	}
	require.NoError(t, json.Unmarshal([]byte(describedJSON), &described))
	require.Len(t, described.Certificate.DomainValidationOptions, 2)

	listedJSON := runCLI(t, awsCLI("acm", "list-certificate-domain-validations",
		"--certificate-arn", created.CertificateArn, "--output", "json"))
	var listed struct {
		DomainValidationSummaryList []struct {
			DomainName                       string `json:"DomainName"`
			RequestedValidationConfiguration struct {
				ValidationMethod    string `json:"ValidationMethod"`
				ValidationChallenge struct {
					DnsValidationChallenge struct {
						ResourceRecord struct {
							Name string `json:"Name"`
						} `json:"ResourceRecord"`
					} `json:"DnsValidationChallenge"`
				} `json:"ValidationChallenge"`
			} `json:"RequestedValidationConfiguration"`
		} `json:"DomainValidationSummaryList"`
	}
	require.NoError(t, json.Unmarshal([]byte(listedJSON), &listed))
	require.Len(t, listed.DomainValidationSummaryList, len(described.Certificate.DomainValidationOptions))

	records := map[string]string{}
	for _, option := range described.Certificate.DomainValidationOptions {
		records[option.DomainName] = option.ResourceRecord.Name
	}
	for _, summary := range listed.DomainValidationSummaryList {
		want, ok := records[summary.DomainName]
		require.True(t, ok, "listed a domain describe-certificate does not report: %s", summary.DomainName)
		require.Equal(t, "DNS", summary.RequestedValidationConfiguration.ValidationMethod)
		require.Equal(t, want,
			summary.RequestedValidationConfiguration.ValidationChallenge.DnsValidationChallenge.ResourceRecord.Name)
	}
}
