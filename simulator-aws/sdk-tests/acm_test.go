package aws_sdk_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func acmClient() *acm.Client {
	cfg := sdkConfig()
	return acm.NewFromConfig(cfg, func(o *acm.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestACMRequestDescribeDelete(t *testing.T) {
	c := acmClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := c.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String("example.com"),
		ValidationMethod: acmtypes.ValidationMethodDns,
		SubjectAlternativeNames: []string{
			"www.example.com",
			"api.example.com",
		},
	})
	require.NoError(t, err)
	arn := aws.ToString(out.CertificateArn)
	require.NotEmpty(t, arn)

	descOut, err := c.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: aws.String(arn),
	})
	require.NoError(t, err)
	require.NotNil(t, descOut.Certificate)
	assert.Equal(t, "example.com", aws.ToString(descOut.Certificate.DomainName))
	assert.Equal(t, acmtypes.CertificateStatusPendingValidation, descOut.Certificate.Status)
	// DNS validation method must include ResourceRecord for each domain.
	require.Len(t, descOut.Certificate.DomainValidationOptions, 3)
	for _, dvo := range descOut.Certificate.DomainValidationOptions {
		assert.NotNil(t, dvo.ResourceRecord, "DNS validation must include ResourceRecord")
		if dvo.ResourceRecord != nil {
			assert.Equal(t, acmtypes.RecordTypeCname, dvo.ResourceRecord.Type)
		}
	}

	listOut, err := c.ListCertificates(ctx, &acm.ListCertificatesInput{})
	require.NoError(t, err)
	found := false
	for _, s := range listOut.CertificateSummaryList {
		if aws.ToString(s.CertificateArn) == arn {
			found = true
		}
	}
	assert.True(t, found, "expected new certificate in list")

	_, err = c.DeleteCertificate(ctx, &acm.DeleteCertificateInput{
		CertificateArn: aws.String(arn),
	})
	require.NoError(t, err)

	_, err = c.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: aws.String(arn),
	})
	require.Error(t, err, "describe after delete must fail")
}

func TestACMUpdateOptionsRenewResendValidation(t *testing.T) {
	c := acmClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := c.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String("opts.example.com"),
		ValidationMethod: acmtypes.ValidationMethodDns,
	})
	require.NoError(t, err)
	arn := aws.ToString(req.CertificateArn)
	defer func() {
		_, _ = c.DeleteCertificate(ctx, &acm.DeleteCertificateInput{CertificateArn: aws.String(arn)})
	}()

	// UpdateCertificateOptions — set transparency logging
	_, err = c.UpdateCertificateOptions(ctx, &acm.UpdateCertificateOptionsInput{
		CertificateArn: aws.String(arn),
		Options: &acmtypes.CertificateOptions{
			CertificateTransparencyLoggingPreference: acmtypes.CertificateTransparencyLoggingPreferenceDisabled,
		},
	})
	require.NoError(t, err)

	// A pending certificate is not eligible for renewal.
	_, err = c.RenewCertificate(ctx, &acm.RenewCertificateInput{CertificateArn: aws.String(arn)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InvalidStateException")

	// ResendValidationEmail rejects a DNS-validated certificate rather than
	// claiming that an email was sent.
	_, err = c.ResendValidationEmail(ctx, &acm.ResendValidationEmailInput{
		CertificateArn:   aws.String(arn),
		Domain:           aws.String("opts.example.com"),
		ValidationDomain: aws.String("opts.example.com"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InvalidDomainValidationOptionsException")
}

func TestACMImportCertificate(t *testing.T) {
	c := acmClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "imported.example.com"},
		DNSNames:     []string{"imported.example.com", "www.imported.example.com"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})

	out, err := c.ImportCertificate(ctx, &acm.ImportCertificateInput{
		Certificate: certificatePEM,
		PrivateKey:  privateKeyPEM,
	})
	require.NoError(t, err)
	arn := aws.ToString(out.CertificateArn)
	require.NotEmpty(t, arn)

	descOut, err := c.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Equal(t, acmtypes.CertificateStatusIssued, descOut.Certificate.Status)
	assert.Equal(t, acmtypes.CertificateTypeImported, descOut.Certificate.Type)
	assert.Equal(t, "imported.example.com", aws.ToString(descOut.Certificate.DomainName))
	assert.ElementsMatch(t, template.DNSNames, descOut.Certificate.SubjectAlternativeNames)
	assert.Equal(t, acmtypes.KeyAlgorithmRsa2048, descOut.Certificate.KeyAlgorithm)
}

func TestACMTagsLifecycle(t *testing.T) {
	c := acmClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := c.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String("tagtest.example.com"),
		ValidationMethod: acmtypes.ValidationMethodDns,
		Tags: []acmtypes.Tag{
			{Key: aws.String("env"), Value: aws.String("test")},
		},
	})
	require.NoError(t, err)
	arn := aws.ToString(req.CertificateArn)
	defer func() {
		_, _ = c.DeleteCertificate(ctx, &acm.DeleteCertificateInput{CertificateArn: aws.String(arn)})
	}()

	listOut, err := c.ListTagsForCertificate(ctx, &acm.ListTagsForCertificateInput{
		CertificateArn: aws.String(arn),
	})
	require.NoError(t, err)
	require.Len(t, listOut.Tags, 1)
	assert.Equal(t, "env", aws.ToString(listOut.Tags[0].Key))

	_, err = c.AddTagsToCertificate(ctx, &acm.AddTagsToCertificateInput{
		CertificateArn: aws.String(arn),
		Tags: []acmtypes.Tag{
			{Key: aws.String("team"), Value: aws.String("infra")},
		},
	})
	require.NoError(t, err)

	listOut, err = c.ListTagsForCertificate(ctx, &acm.ListTagsForCertificateInput{
		CertificateArn: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Len(t, listOut.Tags, 2)

	_, err = c.RemoveTagsFromCertificate(ctx, &acm.RemoveTagsFromCertificateInput{
		CertificateArn: aws.String(arn),
		Tags: []acmtypes.Tag{
			{Key: aws.String("env")},
		},
	})
	require.NoError(t, err)

	listOut, err = c.ListTagsForCertificate(ctx, &acm.ListTagsForCertificateInput{
		CertificateArn: aws.String(arn),
	})
	require.NoError(t, err)
	require.Len(t, listOut.Tags, 1)
	assert.Equal(t, "team", aws.ToString(listOut.Tags[0].Key))
}

// TestCloudFrontRejectsNonUSEast1ACMCert verifies the us-east-1 pin
// enforcement. Sim simulates against region "us-east-1" by default;
// we synthesise a bogus non-us-east-1 ARN that points at a real
// cert ID in the sim's store, and confirm CloudFront rejects it.
func TestCloudFrontRejectsNonUSEast1ACMCert(t *testing.T) {
	a := acmClient()
	cf := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create a cert. Sim's region is us-east-1 by default; we munge the
	// ARN region to "us-west-2" to test cross-resource rejection.
	out, err := a.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String("pin.example.com"),
		ValidationMethod: acmtypes.ValidationMethodDns,
	})
	require.NoError(t, err)
	defer func() {
		_, _ = a.DeleteCertificate(ctx, &acm.DeleteCertificateInput{CertificateArn: out.CertificateArn})
	}()

	usEastArn := aws.ToString(out.CertificateArn)
	// Build a non-us-east-1 variant of the same ARN
	wrongArn := ""
	{
		// arn:aws:acm:us-east-1:<acct>:certificate/<id>
		parts := splitN(usEastArn, ":", 6)
		if len(parts) == 6 {
			parts[3] = "us-west-2"
			wrongArn = parts[0] + ":" + parts[1] + ":" + parts[2] + ":" + parts[3] + ":" + parts[4] + ":" + parts[5]
		}
	}
	require.NotEmpty(t, wrongArn)

	_, err = cf.CreateDistribution(ctx, &cloudfront.CreateDistributionInput{
		DistributionConfig: &cftypes.DistributionConfig{
			CallerReference: aws.String("acm-pin-" + time.Now().Format("150405.000000")),
			Comment:         aws.String("acm pin test"),
			Enabled:         aws.Bool(false),
			Origins: &cftypes.Origins{
				Quantity: aws.Int32(1),
				Items: []cftypes.Origin{{
					Id: aws.String("o1"), DomainName: aws.String("example.com"),
					CustomOriginConfig: &cftypes.CustomOriginConfig{
						HTTPPort: aws.Int32(80), HTTPSPort: aws.Int32(443),
						OriginProtocolPolicy: cftypes.OriginProtocolPolicyHttpOnly,
						OriginSslProtocols: &cftypes.OriginSslProtocols{
							Quantity: aws.Int32(1),
							Items:    []cftypes.SslProtocol{cftypes.SslProtocolTLSv12},
						},
					},
				}},
			},
			DefaultCacheBehavior: &cftypes.DefaultCacheBehavior{
				TargetOriginId:       aws.String("o1"),
				ViewerProtocolPolicy: cftypes.ViewerProtocolPolicyAllowAll,
				ForwardedValues: &cftypes.ForwardedValues{
					QueryString: aws.Bool(false),
					Cookies:     &cftypes.CookiePreference{Forward: cftypes.ItemSelectionNone},
				},
				MinTTL: aws.Int64(0),
			},
			ViewerCertificate: &cftypes.ViewerCertificate{
				ACMCertificateArn: aws.String(wrongArn),
				SSLSupportMethod:  cftypes.SSLSupportMethodSniOnly,
			},
			Restrictions: &cftypes.Restrictions{
				GeoRestriction: &cftypes.GeoRestriction{
					RestrictionType: cftypes.GeoRestrictionTypeNone,
					Quantity:        aws.Int32(0),
				},
			},
		},
	})
	require.Error(t, err, "us-west-2 ACM cert must be rejected by CloudFront")
	var ae smithy.APIError
	require.True(t, errors.As(err, &ae), "expected smithy.APIError")
	assert.Equal(t, "InvalidViewerCertificate", ae.ErrorCode())
}

// splitN is a tiny strings.SplitN substitute to avoid importing strings here.
func splitN(s, sep string, n int) []string {
	out := []string{}
	for i := 0; i < n-1; i++ {
		idx := indexOf(s, sep)
		if idx < 0 {
			break
		}
		out = append(out, s[:idx])
		s = s[idx+len(sep):]
	}
	out = append(out, s)
	return out
}

func indexOf(s, sep string) int {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return i
		}
	}
	return -1
}
