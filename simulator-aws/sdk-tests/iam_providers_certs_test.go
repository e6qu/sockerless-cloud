package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A valid SAML 2.0 IdP metadata document — stored and echoed verbatim. The
// X509 certificate blob is padded so the document exceeds the 1000-character
// minimum the SAMLMetadataDocumentType length constraint imposes.
var samlMetadataDoc = `<?xml version="1.0"?><md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example.com/saml"><md:IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol"><md:KeyDescriptor use="signing"><ds:KeyInfo xmlns:ds="http://www.w3.org/2000/09/xmldsig#"><ds:X509Data><ds:X509Certificate>` +
	strings.Repeat("MIIC", 300) +
	`</ds:X509Certificate></ds:X509Data></ds:KeyInfo></md:KeyDescriptor></md:IDPSSODescriptor></md:EntityDescriptor>`

// A self-signed PEM cert + key pair. The body/key contents are opaque to the
// sim; only that they round-trip verbatim matters.
const (
	testCertBody = `-----BEGIN CERTIFICATE-----
MIIBkTCB+wIJANaELjMYBOEOMA0GCSqGSIb3DQEBCwUAMA0xCzAJBgNVBAYTAlVT
-----END CERTIFICATE-----`
	testCertChain = `-----BEGIN CERTIFICATE-----
MIIBkTCB+wIJChainAAAAAAAAAAA0GCSqGSIb3DQEBCwUAMA0xCzAJBgNVBAYTAlVT
-----END CERTIFICATE-----`
	testPrivKey = `-----BEGIN PRIVATE KEY-----
MIIBVgIBADANBgkqhkiG9w0BAQEFAASCAUAwggE8AgEAAkEA0fakekeyfakekeyfk
-----END PRIVATE KEY-----`
)

func TestIAM_SAMLProvider(t *testing.T) {
	c := iamClient()
	name := "sdk-saml-" + time.Now().Format("150405")

	createOut, err := c.CreateSAMLProvider(ctx, &iam.CreateSAMLProviderInput{
		Name:                 aws.String(name),
		SAMLMetadataDocument: aws.String(samlMetadataDoc),
		Tags:                 []iamtypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
	})
	require.NoError(t, err)
	arn := aws.ToString(createOut.SAMLProviderArn)
	require.Contains(t, arn, ":saml-provider/"+name)
	t.Cleanup(func() {
		_, _ = c.DeleteSAMLProvider(ctx, &iam.DeleteSAMLProviderInput{SAMLProviderArn: aws.String(arn)})
	})

	getOut, err := c.GetSAMLProvider(ctx, &iam.GetSAMLProviderInput{SAMLProviderArn: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, samlMetadataDoc, aws.ToString(getOut.SAMLMetadataDocument))
	require.NotEmpty(t, aws.ToString(getOut.SAMLProviderUUID))

	updateOut, err := c.UpdateSAMLProvider(ctx, &iam.UpdateSAMLProviderInput{
		SAMLProviderArn:      aws.String(arn),
		SAMLMetadataDocument: aws.String(samlMetadataDoc),
	})
	require.NoError(t, err)
	assert.Equal(t, arn, aws.ToString(updateOut.SAMLProviderArn))

	_, err = c.TagSAMLProvider(ctx, &iam.TagSAMLProviderInput{
		SAMLProviderArn: aws.String(arn),
		Tags:            []iamtypes.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
	})
	require.NoError(t, err)

	tagsOut, err := c.ListSAMLProviderTags(ctx, &iam.ListSAMLProviderTagsInput{SAMLProviderArn: aws.String(arn)})
	require.NoError(t, err)
	assert.True(t, iamHasTag(tagsOut.Tags, "team", "platform"))
	assert.True(t, iamHasTag(tagsOut.Tags, "env", "test"))

	_, err = c.UntagSAMLProvider(ctx, &iam.UntagSAMLProviderInput{
		SAMLProviderArn: aws.String(arn),
		TagKeys:         []string{"team"},
	})
	require.NoError(t, err)
	tagsOut2, err := c.ListSAMLProviderTags(ctx, &iam.ListSAMLProviderTagsInput{SAMLProviderArn: aws.String(arn)})
	require.NoError(t, err)
	assert.False(t, iamHasTag(tagsOut2.Tags, "team", "platform"))

	listOut, err := c.ListSAMLProviders(ctx, &iam.ListSAMLProvidersInput{})
	require.NoError(t, err)
	found := false
	for _, p := range listOut.SAMLProviderList {
		if aws.ToString(p.Arn) == arn {
			found = true
		}
	}
	assert.True(t, found, "created SAML provider must appear in ListSAMLProviders")

	_, err = c.DeleteSAMLProvider(ctx, &iam.DeleteSAMLProviderInput{SAMLProviderArn: aws.String(arn)})
	require.NoError(t, err)
}

func TestIAM_ServerCertificate(t *testing.T) {
	c := iamClient()
	name := "sdk-cert-" + time.Now().Format("150405")

	upOut, err := c.UploadServerCertificate(ctx, &iam.UploadServerCertificateInput{
		ServerCertificateName: aws.String(name),
		CertificateBody:       aws.String(testCertBody),
		CertificateChain:      aws.String(testCertChain),
		PrivateKey:            aws.String(testPrivKey),
		Tags:                  []iamtypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
	})
	require.NoError(t, err)
	require.NotNil(t, upOut.ServerCertificateMetadata)
	arn := aws.ToString(upOut.ServerCertificateMetadata.Arn)
	require.Contains(t, arn, ":server-certificate/"+name)
	t.Cleanup(func() {
		_, _ = c.DeleteServerCertificate(ctx, &iam.DeleteServerCertificateInput{ServerCertificateName: aws.String(name)})
	})

	getOut, err := c.GetServerCertificate(ctx, &iam.GetServerCertificateInput{ServerCertificateName: aws.String(name)})
	require.NoError(t, err)
	require.NotNil(t, getOut.ServerCertificate)
	assert.Equal(t, testCertBody, aws.ToString(getOut.ServerCertificate.CertificateBody))
	assert.Equal(t, testCertChain, aws.ToString(getOut.ServerCertificate.CertificateChain))

	newName := name + "-renamed"
	_, err = c.UpdateServerCertificate(ctx, &iam.UpdateServerCertificateInput{
		ServerCertificateName:    aws.String(name),
		NewServerCertificateName: aws.String(newName),
		NewPath:                  aws.String("/cloudfront/test/"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteServerCertificate(ctx, &iam.DeleteServerCertificateInput{ServerCertificateName: aws.String(newName)})
	})
	getOut2, err := c.GetServerCertificate(ctx, &iam.GetServerCertificateInput{ServerCertificateName: aws.String(newName)})
	require.NoError(t, err)
	assert.Equal(t, "/cloudfront/test/", aws.ToString(getOut2.ServerCertificate.ServerCertificateMetadata.Path))

	_, err = c.TagServerCertificate(ctx, &iam.TagServerCertificateInput{
		ServerCertificateName: aws.String(newName),
		Tags:                  []iamtypes.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
	})
	require.NoError(t, err)
	tagsOut, err := c.ListServerCertificateTags(ctx, &iam.ListServerCertificateTagsInput{ServerCertificateName: aws.String(newName)})
	require.NoError(t, err)
	assert.True(t, iamHasTag(tagsOut.Tags, "team", "platform"))
	assert.True(t, iamHasTag(tagsOut.Tags, "env", "test"))

	_, err = c.UntagServerCertificate(ctx, &iam.UntagServerCertificateInput{
		ServerCertificateName: aws.String(newName),
		TagKeys:               []string{"env"},
	})
	require.NoError(t, err)

	listOut, err := c.ListServerCertificates(ctx, &iam.ListServerCertificatesInput{})
	require.NoError(t, err)
	found := false
	for _, m := range listOut.ServerCertificateMetadataList {
		if aws.ToString(m.ServerCertificateName) == newName {
			found = true
		}
	}
	assert.True(t, found, "renamed server certificate must appear in ListServerCertificates")

	_, err = c.DeleteServerCertificate(ctx, &iam.DeleteServerCertificateInput{ServerCertificateName: aws.String(newName)})
	require.NoError(t, err)
}

func TestIAM_AccountAlias(t *testing.T) {
	c := iamClient()
	alias := "sdkalias" + time.Now().Format("150405")

	_, err := c.CreateAccountAlias(ctx, &iam.CreateAccountAliasInput{AccountAlias: aws.String(alias)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteAccountAlias(ctx, &iam.DeleteAccountAliasInput{AccountAlias: aws.String(alias)})
	})

	listOut, err := c.ListAccountAliases(ctx, &iam.ListAccountAliasesInput{})
	require.NoError(t, err)
	assert.Contains(t, listOut.AccountAliases, alias)

	_, err = c.DeleteAccountAlias(ctx, &iam.DeleteAccountAliasInput{AccountAlias: aws.String(alias)})
	require.NoError(t, err)

	listOut2, err := c.ListAccountAliases(ctx, &iam.ListAccountAliasesInput{})
	require.NoError(t, err)
	assert.NotContains(t, listOut2.AccountAliases, alias)
}

func TestIAM_OIDCProviderTags(t *testing.T) {
	c := iamClient()
	url := "https://oidc.eks.us-east-1.amazonaws.com/id/sdktags" + time.Now().Format("150405")

	createOut, err := c.CreateOpenIDConnectProvider(ctx, &iam.CreateOpenIDConnectProviderInput{
		Url:            aws.String(url),
		ClientIDList:   []string{"sts.amazonaws.com"},
		ThumbprintList: []string{"9e99a48a9960b14926bb7f3b02e22da2b0ab7280"},
		Tags:           []iamtypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
	})
	require.NoError(t, err)
	arn := aws.ToString(createOut.OpenIDConnectProviderArn)
	t.Cleanup(func() {
		_, _ = c.DeleteOpenIDConnectProvider(ctx, &iam.DeleteOpenIDConnectProviderInput{OpenIDConnectProviderArn: aws.String(arn)})
	})

	_, err = c.TagOpenIDConnectProvider(ctx, &iam.TagOpenIDConnectProviderInput{
		OpenIDConnectProviderArn: aws.String(arn),
		Tags:                     []iamtypes.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
	})
	require.NoError(t, err)

	tagsOut, err := c.ListOpenIDConnectProviderTags(ctx, &iam.ListOpenIDConnectProviderTagsInput{OpenIDConnectProviderArn: aws.String(arn)})
	require.NoError(t, err)
	assert.True(t, iamHasTag(tagsOut.Tags, "team", "platform"))
	assert.True(t, iamHasTag(tagsOut.Tags, "env", "test"))

	_, err = c.DeleteOpenIDConnectProvider(ctx, &iam.DeleteOpenIDConnectProviderInput{OpenIDConnectProviderArn: aws.String(arn)})
	require.NoError(t, err)
}

func iamHasTag(tags []iamtypes.Tag, key, value string) bool {
	for _, t := range tags {
		if aws.ToString(t.Key) == key && aws.ToString(t.Value) == value {
			return true
		}
	}
	return false
}
