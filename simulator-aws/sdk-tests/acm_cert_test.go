package aws_sdk_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/stretchr/testify/require"
)

// TestACMIssuedCertHasPEMMaterial verifies that a DNS-validated AMAZON_ISSUED
// cert that reaches ISSUED exposes real, parseable X.509 PEM via
// GetCertificate, with the domain name (and SAN) present in the leaf's
// DNSNames. Mirrors what a real ACM caller sees post-issuance.
func TestACMIssuedCertHasPEMMaterial(t *testing.T) {
	acmC := acmClient()
	r53C := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const (
		domain = "api.example.test"
		san    = "www.example.test"
	)
	reqOut, err := acmC.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:              aws.String(domain),
		ValidationMethod:        acmtypes.ValidationMethodDns,
		SubjectAlternativeNames: []string{san},
	})
	require.NoError(t, err)
	arn := aws.ToString(reqOut.CertificateArn)

	desc, err := acmC.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(arn)})
	require.NoError(t, err)
	require.Equal(t, acmtypes.CertificateStatusPendingValidation, desc.Certificate.Status)

	// Pre-issuance: GetCertificate returns RequestInProgressException.
	_, err = acmC.GetCertificate(ctx, &acm.GetCertificateInput{CertificateArn: aws.String(arn)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "RequestInProgressException")

	var records []acmtypes.ResourceRecord
	for _, dvo := range desc.Certificate.DomainValidationOptions {
		require.NotNil(t, dvo.ResourceRecord)
		records = append(records, *dvo.ResourceRecord)
	}
	require.Len(t, records, 2)

	zoneOut, err := r53C.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name:            aws.String("example.test."),
		CallerReference: aws.String("acm-pem-" + arn[len(arn)-8:]),
	})
	require.NoError(t, err)
	zoneID := strings.TrimPrefix(aws.ToString(zoneOut.HostedZone.Id), "/hostedzone/")

	var changes []r53types.Change
	for _, rr := range records {
		changes = append(changes, r53types.Change{
			Action: r53types.ChangeActionUpsert,
			ResourceRecordSet: &r53types.ResourceRecordSet{
				Name:            rr.Name,
				Type:            r53types.RRTypeCname,
				TTL:             aws.Int64(60),
				ResourceRecords: []r53types.ResourceRecord{{Value: rr.Value}},
			},
		})
	}
	_, err = r53C.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch:  &r53types.ChangeBatch{Changes: changes},
	})
	require.NoError(t, err)

	// Poll for issuance (reconcile fires on DescribeCertificate).
	var status acmtypes.CertificateStatus
	for i := 0; i < 30; i++ {
		desc, err = acmC.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(arn)})
		require.NoError(t, err)
		status = desc.Certificate.Status
		if status == acmtypes.CertificateStatusIssued {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.Equal(t, acmtypes.CertificateStatusIssued, status)

	getOut, err := acmC.GetCertificate(ctx, &acm.GetCertificateInput{CertificateArn: aws.String(arn)})
	require.NoError(t, err)
	certPEM := aws.ToString(getOut.Certificate)
	require.NotEmpty(t, certPEM)
	require.Contains(t, certPEM, "BEGIN CERTIFICATE")

	block, _ := pem.Decode([]byte(certPEM))
	require.NotNil(t, block, "certificate PEM must decode")
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.Contains(t, leaf.DNSNames, domain)
	require.Contains(t, leaf.DNSNames, san)
	require.False(t, leaf.IsCA)

	// Chain is served alongside the leaf.
	if chain := aws.ToString(getOut.CertificateChain); chain != "" {
		chainBlock, _ := pem.Decode([]byte(chain))
		require.NotNil(t, chainBlock, "certificate chain PEM must decode")
		root, err := x509.ParseCertificate(chainBlock.Bytes)
		require.NoError(t, err)
		roots := x509.NewCertPool()
		roots.AddCert(root)
		_, err = leaf.Verify(x509.VerifyOptions{DNSName: domain, Roots: roots})
		require.NoError(t, err, "Amazon-issued leaf must verify against its returned CA chain")
	}

	serialBefore := leaf.SerialNumber.String()
	_, err = acmC.RenewCertificate(ctx, &acm.RenewCertificateInput{CertificateArn: aws.String(arn)})
	require.NoError(t, err)
	renewedOut, err := acmC.GetCertificate(ctx, &acm.GetCertificateInput{CertificateArn: aws.String(arn)})
	require.NoError(t, err)
	renewedBlock, _ := pem.Decode([]byte(aws.ToString(renewedOut.Certificate)))
	require.NotNil(t, renewedBlock)
	renewedLeaf, err := x509.ParseCertificate(renewedBlock.Bytes)
	require.NoError(t, err)
	require.NotEqual(t, serialBefore, renewedLeaf.SerialNumber.String(),
		"managed renewal must rotate real certificate material")
}
