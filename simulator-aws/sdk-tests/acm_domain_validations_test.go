package aws_sdk_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/stretchr/testify/require"
)

// ListCertificateDomainValidations is the paginated read AWS added beside
// DescribeCertificate, which carries the same validations inline. The two
// views answer from one record, so this asserts they agree rather than
// asserting a shape of their own.
func TestACMListCertificateDomainValidationsMatchesDescribe(t *testing.T) {
	acmC := acmClient()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	reqOut, err := acmC.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:              aws.String("listdv.example.test"),
		ValidationMethod:        acmtypes.ValidationMethodDns,
		SubjectAlternativeNames: []string{"alt.listdv.example.test"},
	})
	require.NoError(t, err)
	arn := aws.ToString(reqOut.CertificateArn)

	desc, err := acmC.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(arn)})
	require.NoError(t, err)
	described := map[string]acmtypes.DomainValidation{}
	for _, dvo := range desc.Certificate.DomainValidationOptions {
		described[aws.ToString(dvo.DomainName)] = dvo
	}
	require.Len(t, described, 2)

	listed, err := acmC.ListCertificateDomainValidations(ctx, &acm.ListCertificateDomainValidationsInput{
		CertificateArn: aws.String(arn),
	})
	require.NoError(t, err)
	require.Len(t, listed.DomainValidationSummaryList, len(described))

	for _, summary := range listed.DomainValidationSummaryList {
		name := aws.ToString(summary.DomainName)
		dvo, ok := described[name]
		require.True(t, ok, "listed a domain DescribeCertificate does not report: %s", name)
		require.NotNil(t, summary.RequestedValidationConfiguration, "%s carries no requested configuration", name)
		require.Equal(t, dvo.ValidationMethod, summary.RequestedValidationConfiguration.ValidationMethod)
		require.Equal(t, dvo.ValidationStatus, summary.RequestedValidationConfiguration.ValidationStatus)
		// The configuration in force is the one the request asked for until a
		// caller changes the method, which nothing here does.
		require.Equal(t, summary.RequestedValidationConfiguration.ValidationMethod,
			summary.ActiveValidationConfiguration.ValidationMethod)
		// The challenge is a tagged union; a DNS validation carries the
		// record the caller must publish.
		dnsChallenge, ok := summary.RequestedValidationConfiguration.ValidationChallenge.(*acmtypes.ValidationChallengeMemberDnsValidationChallenge)
		require.True(t, ok, "%s did not carry a DNS validation challenge", name)
		require.Equal(t, aws.ToString(dvo.ResourceRecord.Name),
			aws.ToString(dnsChallenge.Value.ResourceRecord.Name))
	}

	// A page smaller than the validation set carries a continuation token, and
	// the pages together cover the whole set exactly once.
	first, err := acmC.ListCertificateDomainValidations(ctx, &acm.ListCertificateDomainValidationsInput{
		CertificateArn: aws.String(arn), MaxItems: aws.Int32(1),
	})
	require.NoError(t, err)
	require.Len(t, first.DomainValidationSummaryList, 1)
	require.NotEmpty(t, aws.ToString(first.NextToken))
	second, err := acmC.ListCertificateDomainValidations(ctx, &acm.ListCertificateDomainValidationsInput{
		CertificateArn: aws.String(arn), NextToken: first.NextToken,
	})
	require.NoError(t, err)
	require.Len(t, second.DomainValidationSummaryList, 1)
	require.NotEqual(t, aws.ToString(first.DomainValidationSummaryList[0].DomainName),
		aws.ToString(second.DomainValidationSummaryList[0].DomainName))

	// An unknown certificate is refused, not answered with an empty list.
	_, err = acmC.ListCertificateDomainValidations(ctx, &acm.ListCertificateDomainValidationsInput{
		CertificateArn: aws.String("arn:aws:acm:us-east-1:123456789012:certificate/00000000-0000-0000-0000-000000000000"),
	})
	require.Error(t, err)
}
