package aws_sdk_test

import (
	"context"
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

// TestACMDNSCertWildcardAndIssuance verifies a DNS-validated AMAZON_ISSUED
// cert reaches ISSUED, and that a wildcard SAN's validation record name strips
// the "*.". The cert stays PENDING until its _acm-challenge records exist in
// Route53, then issues — mirroring real ACM.
func TestACMDNSCertWildcardAndIssuance(t *testing.T) {
	acmC := acmClient()
	r53C := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	reqOut, err := acmC.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:              aws.String("app.example.test"),
		ValidationMethod:        acmtypes.ValidationMethodDns,
		SubjectAlternativeNames: []string{"*.devbox.example.test"},
	})
	require.NoError(t, err)
	arn := aws.ToString(reqOut.CertificateArn)

	desc, err := acmC.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(arn)})
	require.NoError(t, err)
	require.Equal(t, acmtypes.CertificateStatusPendingValidation, desc.Certificate.Status)

	records := map[string]acmtypes.ResourceRecord{}
	for _, dvo := range desc.Certificate.DomainValidationOptions {
		require.NotNil(t, dvo.ResourceRecord, "DNS DVO must carry a ResourceRecord")
		records[aws.ToString(dvo.DomainName)] = *dvo.ResourceRecord
	}
	// DomainName echoes the wildcard, but the record name is de-wildcarded.
	require.Contains(t, records, "*.devbox.example.test")
	wildName := aws.ToString(records["*.devbox.example.test"].Name)
	require.Equal(t, "_acm-challenge.devbox.example.test.", wildName)
	require.NotContains(t, wildName, "*")

	// A second certificate for the same fully qualified domain name receives
	// the same validation CNAME value. Real deployments commonly request one
	// regional certificate and one us-east-1 CloudFront certificate and publish
	// only one long-lived validation record.
	secondOut, err := acmC.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String("app.example.test"),
		ValidationMethod: acmtypes.ValidationMethodDns,
	})
	require.NoError(t, err)
	secondARN := aws.ToString(secondOut.CertificateArn)
	secondDesc, err := acmC.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: secondOut.CertificateArn,
	})
	require.NoError(t, err)
	require.Len(t, secondDesc.Certificate.DomainValidationOptions, 1)
	require.Equal(t,
		aws.ToString(records["app.example.test"].Value),
		aws.ToString(secondDesc.Certificate.DomainValidationOptions[0].ResourceRecord.Value),
	)

	// Faithful: with no validation records yet, the cert stays PENDING —
	// no synthetic issuance.
	desc, err = acmC.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(arn)})
	require.NoError(t, err)
	require.Equal(t, acmtypes.CertificateStatusPendingValidation, desc.Certificate.Status)

	zoneOut, err := r53C.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name:            aws.String("example.test."),
		CallerReference: aws.String("acm-issue-" + arn[len(arn)-8:]),
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

	// With both _acm-challenge records present, the cert reaches ISSUED.
	desc, err = acmC.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(arn)})
	require.NoError(t, err)
	require.Equal(t, acmtypes.CertificateStatusIssued, desc.Certificate.Status)
	require.NotNil(t, desc.Certificate.IssuedAt)
	for _, dvo := range desc.Certificate.DomainValidationOptions {
		require.Equal(t, acmtypes.DomainStatusSuccess, dvo.ValidationStatus)
	}
	secondDesc, err = acmC.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: aws.String(secondARN),
	})
	require.NoError(t, err)
	require.Equal(t, acmtypes.CertificateStatusIssued, secondDesc.Certificate.Status)
}
