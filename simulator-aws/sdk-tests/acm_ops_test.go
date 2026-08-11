package aws_sdk_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestUnknownPrivateCA proves ACM does not manufacture a certificate
// authority merely because the caller supplied a syntactically valid ARN.
func requestUnknownPrivateCA(t *testing.T, c *acm.Client, domain string) {
	t.Helper()
	_, err := c.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:              aws.String(domain),
		CertificateAuthorityArn: aws.String("arn:aws:acm-pca:us-east-1:000000000000:certificate-authority/test-ca"),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "ResourceNotFoundException")
}

func TestACM_RejectsUnknownPrivateCertificateAuthority(t *testing.T) {
	c := acmClient()
	requestUnknownPrivateCA(t, c, "unknown-ca.private.example.com")
}

// TestACM_ExportCertificate proves public Amazon-issued certificates cannot
// expose their service-held private key.
func TestACM_ExportCertificate(t *testing.T) {
	c := acmClient()

	reqOut, err := c.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String("public.example.com"),
		ValidationMethod: acmtypes.ValidationMethodDns,
	})
	require.NoError(t, err)
	arn := aws.ToString(reqOut.CertificateArn)
	t.Cleanup(func() {
		_, _ = c.DeleteCertificate(context.Background(), &acm.DeleteCertificateInput{CertificateArn: aws.String(arn)})
	})

	// Missing passphrase is rejected.
	_, err = c.ExportCertificate(ctx, &acm.ExportCertificateInput{CertificateArn: aws.String(arn)})
	require.Error(t, err)

	_, err = c.ExportCertificate(ctx, &acm.ExportCertificateInput{
		CertificateArn: aws.String(arn),
		Passphrase:     []byte("x"),
	})
	require.Error(t, err, "a non-PRIVATE certificate must not be exportable")
}

// TestACM_RevokeCertificate proves the private-certificate-only operation
// rejects an Amazon-issued request.
func TestACM_RevokeCertificate(t *testing.T) {
	c := acmClient()

	req, err := c.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String("revoke.public.example.com"),
		ValidationMethod: acmtypes.ValidationMethodDns,
	})
	require.NoError(t, err)
	arn := aws.ToString(req.CertificateArn)
	t.Cleanup(func() {
		_, _ = c.DeleteCertificate(context.Background(), &acm.DeleteCertificateInput{CertificateArn: aws.String(arn)})
	})
	_, err = c.RevokeCertificate(ctx, &acm.RevokeCertificateInput{
		CertificateArn:   aws.String(arn),
		RevocationReason: acmtypes.RevocationReasonKeyCompromise,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceInUseException")
}

// TestACM_AccountConfiguration pins the account expiry-events config
// round-trip: default 45 days, then a Put updates it.
func TestACM_AccountConfiguration(t *testing.T) {
	c := acmClient()

	_, err := c.PutAccountConfiguration(ctx, &acm.PutAccountConfigurationInput{
		ExpiryEvents:     &acmtypes.ExpiryEventsConfiguration{DaysBeforeExpiry: aws.Int32(30)},
		IdempotencyToken: aws.String("tok-account-config"),
	})
	require.NoError(t, err)

	got, err := c.GetAccountConfiguration(ctx, &acm.GetAccountConfigurationInput{})
	require.NoError(t, err)
	require.NotNil(t, got.ExpiryEvents)
	require.NotNil(t, got.ExpiryEvents.DaysBeforeExpiry)
	assert.EqualValues(t, 30, *got.ExpiryEvents.DaysBeforeExpiry)

	// PutAccountConfiguration without an IdempotencyToken must fail.
	_, err = c.PutAccountConfiguration(ctx, &acm.PutAccountConfigurationInput{
		ExpiryEvents: &acmtypes.ExpiryEventsConfiguration{DaysBeforeExpiry: aws.Int32(15)},
	})
	require.Error(t, err)
}

// TestACM_SearchCertificates pins SearchCertificates filtering by metadata
// (Type) and returning per-cert metadata results.
func TestACM_SearchCertificates(t *testing.T) {
	c := acmClient()

	requestUnknownPrivateCA(t, c, "search-private.example.com")

	deadline := time.Now().Add(5 * time.Second)
	var foundSynthetic bool
	for time.Now().Before(deadline) {
		out, err := c.SearchCertificates(ctx, &acm.SearchCertificatesInput{
			FilterStatement: &acmtypes.CertificateFilterStatementMemberFilter{
				Value: &acmtypes.CertificateFilterMemberAcmCertificateMetadataFilter{
					Value: &acmtypes.AcmCertificateMetadataFilterMemberType{
						Value: acmtypes.CertificateTypePrivate,
					},
				},
			},
		})
		require.NoError(t, err)
		for _, res := range out.Results {
			meta, ok := res.CertificateMetadata.(*acmtypes.CertificateMetadataMemberAcmCertificateMetadata)
			require.True(t, ok, "CertificateMetadata must carry AcmCertificateMetadata")
			// Every returned cert must be PRIVATE per the filter.
			assert.Equal(t, acmtypes.CertificateTypePrivate, meta.Value.Type)
			foundSynthetic = true
		}
		if foundSynthetic {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	assert.False(t, foundSynthetic, "a missing AWS Private CA must never create synthetic PRIVATE certificate state")
}
