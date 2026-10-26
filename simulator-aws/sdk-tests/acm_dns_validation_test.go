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
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/require"
)

// TestACMDNSCertWildcardAndIssuance expects a DNS-validated certificate to stay
// PENDING until its _acm-challenge records exist in Route 53 and then reach
// ISSUED, with the wildcard SAN's record name stripped of "*.".
func TestACMDNSCertWildcardAndIssuance(t *testing.T) {
	t.Parallel()
	acmC := acmClient()
	r53C := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	appDomain := uniquename.New("app") + ".example.test"
	devbox := uniquename.New("devbox") + ".example.test"

	reqOut, err := acmC.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:              aws.String(appDomain),
		ValidationMethod:        acmtypes.ValidationMethodDns,
		SubjectAlternativeNames: []string{"*." + devbox},
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
	// DomainName keeps the wildcard; the record name does not.
	require.Contains(t, records, "*."+devbox)
	wildName := aws.ToString(records["*."+devbox].Name)
	require.Equal(t, "_acm-challenge."+devbox+".", wildName)
	require.NotContains(t, wildName, "*")

	// A second certificate for the same domain name gets the same validation
	// CNAME value, so a regional certificate and a us-east-1 CloudFront one share
	// one long-lived record.
	secondOut, err := acmC.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String(appDomain),
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
		aws.ToString(records[appDomain].Value),
		aws.ToString(secondDesc.Certificate.DomainValidationOptions[0].ResourceRecord.Value),
	)

	// With no validation records yet, the certificate stays PENDING.
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
