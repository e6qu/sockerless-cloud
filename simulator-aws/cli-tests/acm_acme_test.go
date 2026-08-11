package aws_cli_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/acme"
)

func TestACM_ACMEControlPlaneAndAccountRegistration(t *testing.T) {
	zoneOutput := runCLI(t, awsCLI("route53", "create-hosted-zone",
		"--name", "acme-cli.example.",
		"--caller-reference", fmt.Sprintf("acme-cli-%d", time.Now().UnixNano()),
		"--output", "json",
	))
	var zone struct {
		HostedZone struct {
			ID string `json:"Id"`
		} `json:"HostedZone"`
	}
	require.NoError(t, json.Unmarshal([]byte(zoneOutput), &zone))
	require.NotEmpty(t, zone.HostedZone.ID)
	// Amazon Route 53 prefixes the id it returns with "/hostedzone/", and AWS
	// Certificate Manager's HostedZoneId admits the bare id alone — so a caller
	// carrying one API's answer into the other's request strips it, as here.
	zoneID := strings.TrimPrefix(zone.HostedZone.ID, "/hostedzone/")

	endpointOutput := runCLI(t, awsCLI("acm", "create-acme-endpoint",
		"--authorization-behavior", "PRE_APPROVED",
		"--certificate-authority", `{"PublicCertificateAuthority":{"AllowedKeyAlgorithms":["RSA_2048"]}}`,
		"--contact", "REQUIRED",
		"--tags", "Key=suite,Value=cli-acme",
		"--output", "json",
	))
	var endpointCreated struct {
		Arn string `json:"AcmeEndpointArn"`
	}
	require.NoError(t, json.Unmarshal([]byte(endpointOutput), &endpointCreated))
	require.NotEmpty(t, endpointCreated.Arn)

	endpointDescription := runCLI(t, awsCLI("acm", "describe-acme-endpoint",
		"--acme-endpoint-arn", endpointCreated.Arn,
		"--output", "json",
	))
	var endpointDescribed struct {
		Endpoint struct {
			URL    string `json:"EndpointUrl"`
			Status string `json:"Status"`
		} `json:"AcmeEndpoint"`
	}
	require.NoError(t, json.Unmarshal([]byte(endpointDescription), &endpointDescribed))
	require.Equal(t, "ACTIVE", endpointDescribed.Endpoint.Status)
	require.NotEmpty(t, endpointDescribed.Endpoint.URL)
	runCLI(t, awsCLI("acm", "list-acme-endpoints", "--max-results", "1", "--output", "json"))
	runCLI(t, awsCLI("acm", "update-acme-endpoint",
		"--acme-endpoint-arn", endpointCreated.Arn,
		"--authorization-behavior", "PRE_APPROVED",
		"--certificate-authority", `{"PublicCertificateAuthority":{"AllowedKeyAlgorithms":["RSA_2048"]}}`,
		"--contact", "REQUIRED",
	))

	validationOutput := runCLI(t, awsCLI("acm", "create-acme-domain-validation",
		"--acme-endpoint-arn", endpointCreated.Arn,
		"--domain-name", "service.acme-cli.example",
		"--prevalidation-options", fmt.Sprintf(
			`{"DnsPrevalidation":{"DomainScope":{"ExactDomain":"ENABLED","Subdomains":"DISABLED","Wildcards":"DISABLED"},"HostedZoneId":%q}}`,
			zoneID,
		),
		"--tags", "Key=domain,Value=service",
		"--output", "json",
	))
	var validationCreated struct {
		Arn string `json:"AcmeDomainValidationArn"`
	}
	require.NoError(t, json.Unmarshal([]byte(validationOutput), &validationCreated))
	require.NotEmpty(t, validationCreated.Arn)
	validationDescription := runCLI(t, awsCLI("acm", "describe-acme-domain-validation",
		"--acme-domain-validation-arn", validationCreated.Arn,
		"--output", "json",
	))
	require.Contains(t, validationDescription, `"Status": "VALID"`)
	runCLI(t, awsCLI("acm", "list-acme-domain-validations",
		"--acme-endpoint-arn", endpointCreated.Arn,
		"--output", "json",
	))
	runCLI(t, awsCLI("acm", "update-acme-domain-validation",
		"--acme-domain-validation-arn", validationCreated.Arn,
		"--prevalidation-options", fmt.Sprintf(
			`{"DnsPrevalidation":{"DomainScope":{"ExactDomain":"ENABLED","Subdomains":"DISABLED","Wildcards":"DISABLED"},"HostedZoneId":%q}}`,
			zoneID,
		),
	))

	bindingOutput := runCLI(t, awsCLI("acm", "create-acme-external-account-binding",
		"--acme-endpoint-arn", endpointCreated.Arn,
		"--role-arn", "arn:aws:iam::123456789012:role/AcmeCLIClient",
		"--expiration", "Value=1,Type=DAYS",
		"--tags", "Key=client,Value=aws-cli",
		"--output", "json",
	))
	var bindingCreated struct {
		Binding struct {
			Arn string `json:"AcmeExternalAccountBindingArn"`
		} `json:"ExternalAccountBinding"`
	}
	require.NoError(t, json.Unmarshal([]byte(bindingOutput), &bindingCreated))
	require.NotEmpty(t, bindingCreated.Binding.Arn)
	runCLI(t, awsCLI("acm", "describe-acme-external-account-binding",
		"--acme-external-account-binding-arn", bindingCreated.Binding.Arn,
		"--output", "json",
	))
	runCLI(t, awsCLI("acm", "list-acme-external-account-bindings",
		"--acme-endpoint-arn", endpointCreated.Arn,
		"--output", "json",
	))
	credentialsOutput := runCLI(t, awsCLI("acm", "get-acme-external-account-binding-credentials",
		"--acme-external-account-binding-arn", bindingCreated.Binding.Arn,
		"--output", "json",
	))
	var credentials struct {
		KeyID  string `json:"KeyId"`
		MACKey string `json:"MacKey"`
	}
	require.NoError(t, json.Unmarshal([]byte(credentialsOutput), &credentials))
	macKey, err := base64.RawURLEncoding.DecodeString(credentials.MACKey)
	require.NoError(t, err)

	accountKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	client := &acme.Client{Key: accountKey, DirectoryURL: endpointDescribed.Endpoint.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	account, err := client.Register(ctx, &acme.Account{
		Contact: []string{"mailto:operator@acme-cli.example"},
		ExternalAccountBinding: &acme.ExternalAccountBinding{
			KID: credentials.KeyID,
			Key: macKey,
		},
	}, acme.AcceptTOS)
	require.NoError(t, err)
	require.NotEmpty(t, account.URI)

	runCLI(t, awsCLI("acm", "describe-acme-account",
		"--acme-endpoint-arn", endpointCreated.Arn,
		"--account-url", account.URI,
		"--output", "json",
	))
	accountsOutput := runCLI(t, awsCLI("acm", "list-acme-accounts",
		"--acme-endpoint-arn", endpointCreated.Arn,
		"--output", "json",
	))
	require.Contains(t, accountsOutput, account.URI)
	runCLI(t, awsCLI("acm", "revoke-acme-account",
		"--acme-endpoint-arn", endpointCreated.Arn,
		"--account-url", account.URI,
	))
	runCLI(t, awsCLI("acm", "revoke-acme-external-account-binding",
		"--acme-external-account-binding-arn", bindingCreated.Binding.Arn,
	))
	runCLI(t, awsCLI("acm", "delete-acme-external-account-binding",
		"--acme-external-account-binding-arn", bindingCreated.Binding.Arn,
	))
	runCLI(t, awsCLI("acm", "delete-acme-domain-validation",
		"--acme-domain-validation-arn", validationCreated.Arn,
	))
	runCLI(t, awsCLI("acm", "delete-acme-endpoint",
		"--acme-endpoint-arn", endpointCreated.Arn,
	))
}
