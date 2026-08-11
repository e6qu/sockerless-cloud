package aws_cli_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The X509 certificate blob is padded so the document exceeds the 1000-char
// minimum the IAM CLI client-side-validates for --saml-metadata-document.
var samlMetadataDocCLI = `<?xml version="1.0"?><md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example.com/cli"><md:IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol"><md:KeyDescriptor use="signing"><ds:KeyInfo xmlns:ds="http://www.w3.org/2000/09/xmldsig#"><ds:X509Data><ds:X509Certificate>` +
	strings.Repeat("MIIC", 300) +
	`</ds:X509Certificate></ds:X509Data></ds:KeyInfo></md:KeyDescriptor></md:IDPSSODescriptor></md:EntityDescriptor>`

const (
	cliCertBody = `-----BEGIN CERTIFICATE-----
MIIBkTCB+wIJANaELjMYBOEOMA0GCSqGSIb3DQEBCwUAMA0xCzAJBgNVBAYTAlVT
-----END CERTIFICATE-----`
	cliPrivKey = `-----BEGIN PRIVATE KEY-----
MIIBVgIBADANBgkqhkiG9w0BAQEFAASCAUAwggE8AgEAAkEA0fakekeyfakekeyfk
-----END PRIVATE KEY-----`
)

func TestIAM_SAMLProvider_CLI(t *testing.T) {
	name := "cli-saml-" + time.Now().Format("150405")
	createOut := runCLI(t, awsCLI("iam", "create-saml-provider",
		"--name", name,
		"--saml-metadata-document", samlMetadataDocCLI,
		"--tags", "Key=env,Value=test",
		"--output", "json",
	))
	var created struct {
		SAMLProviderArn string `json:"SAMLProviderArn"`
	}
	require.NoError(t, json.Unmarshal([]byte(createOut), &created))
	require.Contains(t, created.SAMLProviderArn, ":saml-provider/"+name)
	arn := created.SAMLProviderArn
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-saml-provider", "--saml-provider-arn", arn))
	})

	getOut := runCLI(t, awsCLI("iam", "get-saml-provider", "--saml-provider-arn", arn, "--output", "json"))
	var got struct {
		SAMLMetadataDocument string `json:"SAMLMetadataDocument"`
		SAMLProviderUUID     string `json:"SAMLProviderUUID"`
	}
	require.NoError(t, json.Unmarshal([]byte(getOut), &got))
	require.Equal(t, samlMetadataDocCLI, got.SAMLMetadataDocument)
	require.NotEmpty(t, got.SAMLProviderUUID)

	runCLI(t, awsCLI("iam", "update-saml-provider",
		"--saml-provider-arn", arn,
		"--saml-metadata-document", samlMetadataDocCLI, "--output", "json"))

	runCLI(t, awsCLI("iam", "tag-saml-provider",
		"--saml-provider-arn", arn,
		"--tags", "Key=team,Value=platform"))

	tagsOut := runCLI(t, awsCLI("iam", "list-saml-provider-tags", "--saml-provider-arn", arn, "--output", "json"))
	require.Contains(t, tagsOut, "platform")

	runCLI(t, awsCLI("iam", "untag-saml-provider", "--saml-provider-arn", arn, "--tag-keys", "team"))

	listOut := runCLI(t, awsCLI("iam", "list-saml-providers", "--output", "json"))
	require.Contains(t, listOut, name)

	runCLI(t, awsCLI("iam", "delete-saml-provider", "--saml-provider-arn", arn))
}

func TestIAM_ServerCertificate_CLI(t *testing.T) {
	name := "cli-cert-" + time.Now().Format("150405")
	upOut := runCLI(t, awsCLI("iam", "upload-server-certificate",
		"--server-certificate-name", name,
		"--certificate-body", cliCertBody,
		"--private-key", cliPrivKey,
		"--tags", "Key=env,Value=test",
		"--output", "json",
	))
	var up struct {
		ServerCertificateMetadata struct {
			Arn string `json:"Arn"`
		} `json:"ServerCertificateMetadata"`
	}
	require.NoError(t, json.Unmarshal([]byte(upOut), &up))
	require.Contains(t, up.ServerCertificateMetadata.Arn, ":server-certificate/"+name)
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-server-certificate", "--server-certificate-name", name))
		runCLIIgnore(awsCLI("iam", "delete-server-certificate", "--server-certificate-name", name+"-2"))
	})

	getOut := runCLI(t, awsCLI("iam", "get-server-certificate", "--server-certificate-name", name, "--output", "json"))
	var got struct {
		ServerCertificate struct {
			CertificateBody string `json:"CertificateBody"`
		} `json:"ServerCertificate"`
	}
	require.NoError(t, json.Unmarshal([]byte(getOut), &got))
	require.Equal(t, cliCertBody, got.ServerCertificate.CertificateBody)

	newName := name + "-2"
	runCLI(t, awsCLI("iam", "update-server-certificate",
		"--server-certificate-name", name,
		"--new-server-certificate-name", newName,
		"--new-path", "/cloudfront/test/"))

	getOut2 := runCLI(t, awsCLI("iam", "get-server-certificate", "--server-certificate-name", newName, "--output", "json"))
	require.Contains(t, getOut2, "/cloudfront/test/")

	runCLI(t, awsCLI("iam", "tag-server-certificate",
		"--server-certificate-name", newName,
		"--tags", "Key=team,Value=platform"))
	tagsOut := runCLI(t, awsCLI("iam", "list-server-certificate-tags", "--server-certificate-name", newName, "--output", "json"))
	require.Contains(t, tagsOut, "platform")
	runCLI(t, awsCLI("iam", "untag-server-certificate", "--server-certificate-name", newName, "--tag-keys", "env"))

	listOut := runCLI(t, awsCLI("iam", "list-server-certificates", "--output", "json"))
	require.Contains(t, listOut, newName)

	runCLI(t, awsCLI("iam", "delete-server-certificate", "--server-certificate-name", newName))
}

func TestIAM_AccountAlias_CLI(t *testing.T) {
	alias := "clialias" + time.Now().Format("150405")
	runCLI(t, awsCLI("iam", "create-account-alias", "--account-alias", alias))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-account-alias", "--account-alias", alias))
	})

	listOut := runCLI(t, awsCLI("iam", "list-account-aliases", "--output", "json"))
	require.Contains(t, listOut, alias)

	runCLI(t, awsCLI("iam", "delete-account-alias", "--account-alias", alias))

	listOut2 := runCLI(t, awsCLI("iam", "list-account-aliases", "--output", "json"))
	require.False(t, strings.Contains(listOut2, alias), "alias must be gone after delete")
}

func TestIAM_OIDCProviderTags_CLI(t *testing.T) {
	url := "https://oidc.eks.us-east-1.amazonaws.com/id/clitags" + time.Now().Format("150405")
	createOut := runCLI(t, awsCLI("iam", "create-open-id-connect-provider",
		"--url", url,
		"--client-id-list", "sts.amazonaws.com",
		"--thumbprint-list", "9e99a48a9960b14926bb7f3b02e22da2b0ab7280",
		"--tags", "Key=env,Value=test",
		"--output", "json",
	))
	var created struct {
		OpenIDConnectProviderArn string `json:"OpenIDConnectProviderArn"`
	}
	require.NoError(t, json.Unmarshal([]byte(createOut), &created))
	arn := created.OpenIDConnectProviderArn
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-open-id-connect-provider", "--open-id-connect-provider-arn", arn))
	})

	runCLI(t, awsCLI("iam", "tag-open-id-connect-provider",
		"--open-id-connect-provider-arn", arn,
		"--tags", "Key=team,Value=platform"))

	tagsOut := runCLI(t, awsCLI("iam", "list-open-id-connect-provider-tags",
		"--open-id-connect-provider-arn", arn, "--output", "json"))
	require.Contains(t, tagsOut, "platform")
	require.Contains(t, tagsOut, "test")

	runCLI(t, awsCLI("iam", "delete-open-id-connect-provider", "--open-id-connect-provider-arn", arn))
}
