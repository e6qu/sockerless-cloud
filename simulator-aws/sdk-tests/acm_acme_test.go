package aws_sdk_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/acme"
)

// acmHostedZoneID strips the "/hostedzone/" Amazon Route 53 prefixes onto the
// id it returns. AWS Certificate Manager's HostedZoneId admits the bare id
// alone, so a caller carrying one API's answer into the other's request has to
// strip it — exactly as here.
func acmHostedZoneID(route53ID *string) string {
	return strings.TrimPrefix(aws.ToString(route53ID), "/hostedzone/")
}

func TestACMAcmeControlAndRFC8555DataPlane(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	acmClient := acmClient()
	route53Client := route53Client()
	zone, err := route53Client.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name:            aws.String("acme-sdk.example."),
		CallerReference: aws.String("acme-sdk-" + time.Now().Format("150405.000000000")),
	})
	require.NoError(t, err)

	createdEndpoint, err := acmClient.CreateAcmeEndpoint(ctx, &acm.CreateAcmeEndpointInput{
		AuthorizationBehavior: acmtypes.AcmeAuthorizationBehaviorPreApproved,
		CertificateAuthority: &acmtypes.CertificateAuthorityMemberPublicCertificateAuthority{
			Value: acmtypes.PublicCertificateAuthority{
				AllowedKeyAlgorithms: []acmtypes.PublicKeyAlgorithm{acmtypes.PublicKeyAlgorithmRsa2048},
			},
		},
		Contact: acmtypes.AcmeContactRequired,
		Tags:    []acmtypes.Tag{{Key: aws.String("suite"), Value: aws.String("sdk-acme")}},
	})
	require.NoError(t, err)
	endpointArn := aws.ToString(createdEndpoint.AcmeEndpointArn)
	require.NotEmpty(t, endpointArn)

	describedEndpoint, err := acmClient.DescribeAcmeEndpoint(ctx, &acm.DescribeAcmeEndpointInput{
		AcmeEndpointArn: aws.String(endpointArn),
	})
	require.NoError(t, err)
	require.Equal(t, acmtypes.AcmeEndpointStatusActive, describedEndpoint.AcmeEndpoint.Status)
	require.NotEmpty(t, aws.ToString(describedEndpoint.AcmeEndpoint.EndpointUrl))
	directoryURL := aws.ToString(describedEndpoint.AcmeEndpoint.EndpointUrl)

	endpoints, err := acmClient.ListAcmeEndpoints(ctx, &acm.ListAcmeEndpointsInput{MaxResults: aws.Int32(1)})
	require.NoError(t, err)
	require.NotEmpty(t, endpoints.AcmeEndpoints)
	_, err = acmClient.UpdateAcmeEndpoint(ctx, &acm.UpdateAcmeEndpointInput{
		AcmeEndpointArn:       aws.String(endpointArn),
		AuthorizationBehavior: acmtypes.AcmeAuthorizationBehaviorPreApproved,
		Contact:               acmtypes.AcmeContactRequired,
		CertificateAuthority: &acmtypes.CertificateAuthorityMemberPublicCertificateAuthority{
			Value: acmtypes.PublicCertificateAuthority{
				AllowedKeyAlgorithms: []acmtypes.PublicKeyAlgorithm{acmtypes.PublicKeyAlgorithmRsa2048},
			},
		},
	})
	require.NoError(t, err)

	createdValidation, err := acmClient.CreateAcmeDomainValidation(ctx, &acm.CreateAcmeDomainValidationInput{
		AcmeEndpointArn: aws.String(endpointArn),
		DomainName:      aws.String("service.acme-sdk.example"),
		PrevalidationOptions: &acmtypes.PrevalidationOptionsMemberDnsPrevalidation{
			Value: acmtypes.DnsPrevalidationOptions{
				HostedZoneId: aws.String(acmHostedZoneID(zone.HostedZone.Id)),
				DomainScope: &acmtypes.DomainScope{
					ExactDomain: acmtypes.DomainScopeOptionEnabled,
					Subdomains:  acmtypes.DomainScopeOptionDisabled,
					Wildcards:   acmtypes.DomainScopeOptionDisabled,
				},
			},
		},
		Tags: []acmtypes.Tag{{Key: aws.String("domain"), Value: aws.String("service")}},
	})
	require.NoError(t, err)
	validationArn := aws.ToString(createdValidation.AcmeDomainValidationArn)

	describedValidation, err := acmClient.DescribeAcmeDomainValidation(ctx, &acm.DescribeAcmeDomainValidationInput{
		AcmeDomainValidationArn: aws.String(validationArn),
	})
	require.NoError(t, err)
	require.Equal(t, acmtypes.AcmeDomainValidationStatusValid, describedValidation.AcmeDomainValidation.Status)
	_, err = acmClient.UpdateAcmeDomainValidation(ctx, &acm.UpdateAcmeDomainValidationInput{
		AcmeDomainValidationArn: aws.String(validationArn),
		PrevalidationOptions: &acmtypes.PrevalidationOptionsMemberDnsPrevalidation{
			Value: acmtypes.DnsPrevalidationOptions{
				HostedZoneId: aws.String(acmHostedZoneID(zone.HostedZone.Id)),
				DomainScope: &acmtypes.DomainScope{
					ExactDomain: acmtypes.DomainScopeOptionEnabled,
					Subdomains:  acmtypes.DomainScopeOptionDisabled,
					Wildcards:   acmtypes.DomainScopeOptionDisabled,
				},
			},
		},
	})
	require.NoError(t, err)
	validations, err := acmClient.ListAcmeDomainValidations(ctx, &acm.ListAcmeDomainValidationsInput{
		AcmeEndpointArn: aws.String(endpointArn),
	})
	require.NoError(t, err)
	require.Len(t, validations.AcmeDomainValidations, 1)

	expirationValue := int64(1)
	createdBinding, err := acmClient.CreateAcmeExternalAccountBinding(ctx, &acm.CreateAcmeExternalAccountBindingInput{
		AcmeEndpointArn: aws.String(endpointArn),
		RoleArn:         aws.String("arn:aws:iam::123456789012:role/AcmeClient"),
		Expiration: &acmtypes.Expiration{
			Type:  acmtypes.TimeTypeDays,
			Value: &expirationValue,
		},
		Tags: []acmtypes.Tag{{Key: aws.String("client"), Value: aws.String("official-go-acme")}},
	})
	require.NoError(t, err)
	bindingArn := aws.ToString(createdBinding.ExternalAccountBinding.AcmeExternalAccountBindingArn)
	require.NotEmpty(t, bindingArn)
	_, err = acmClient.DescribeAcmeExternalAccountBinding(ctx, &acm.DescribeAcmeExternalAccountBindingInput{
		AcmeExternalAccountBindingArn: aws.String(bindingArn),
	})
	require.NoError(t, err)
	bindings, err := acmClient.ListAcmeExternalAccountBindings(ctx, &acm.ListAcmeExternalAccountBindingsInput{
		AcmeEndpointArn: aws.String(endpointArn),
	})
	require.NoError(t, err)
	require.Len(t, bindings.ExternalAccountBindings, 1)
	credentials, err := acmClient.GetAcmeExternalAccountBindingCredentials(ctx, &acm.GetAcmeExternalAccountBindingCredentialsInput{
		AcmeExternalAccountBindingArn: aws.String(bindingArn),
	})
	require.NoError(t, err)
	macKey, err := base64.RawURLEncoding.DecodeString(aws.ToString(credentials.MacKey))
	require.NoError(t, err)

	accountKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	acmeClient := &acme.Client{Key: accountKey, DirectoryURL: directoryURL}
	account, err := acmeClient.Register(ctx, &acme.Account{
		Contact: []string{"mailto:operator@acme-sdk.example"},
		ExternalAccountBinding: &acme.ExternalAccountBinding{
			KID: aws.ToString(credentials.KeyId),
			Key: macKey,
		},
	}, acme.AcceptTOS)
	require.NoError(t, err)
	require.NotEmpty(t, account.URI)

	describedAccount, err := acmClient.DescribeAcmeAccount(ctx, &acm.DescribeAcmeAccountInput{
		AcmeEndpointArn: aws.String(endpointArn),
		AccountUrl:      aws.String(account.URI),
	})
	require.NoError(t, err)
	require.Equal(t, acmtypes.AcmeAccountStatusValid, describedAccount.AcmeAccount.Status)
	accounts, err := acmClient.ListAcmeAccounts(ctx, &acm.ListAcmeAccountsInput{AcmeEndpointArn: aws.String(endpointArn)})
	require.NoError(t, err)
	require.Len(t, accounts.AcmeAccounts, 1)
	originalThumbprint := aws.ToString(describedAccount.AcmeAccount.PublicKeyThumbprint)
	rotatedAccountKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	require.NoError(t, acmeClient.AccountKeyRollover(ctx, rotatedAccountKey))
	describedAccount, err = acmClient.DescribeAcmeAccount(ctx, &acm.DescribeAcmeAccountInput{
		AcmeEndpointArn: aws.String(endpointArn),
		AccountUrl:      aws.String(account.URI),
	})
	require.NoError(t, err)
	require.NotEqual(t, originalThumbprint, aws.ToString(describedAccount.AcmeAccount.PublicKeyThumbprint))

	order, err := acmeClient.AuthorizeOrder(ctx, []acme.AuthzID{{Type: "dns", Value: "service.acme-sdk.example"}})
	require.NoError(t, err)
	require.Equal(t, acme.StatusReady, order.Status)

	certificateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "service.acme-sdk.example"},
		DNSNames: []string{"service.acme-sdk.example"},
	}, certificateKey)
	require.NoError(t, err)
	chain, _, err := acmeClient.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	require.NoError(t, err)
	require.Len(t, chain, 2)
	leaf, err := x509.ParseCertificate(chain[0])
	require.NoError(t, err)
	roots := x509.NewCertPool()
	root, err := x509.ParseCertificate(chain[1])
	require.NoError(t, err)
	roots.AddCert(root)
	_, err = leaf.Verify(x509.VerifyOptions{DNSName: "service.acme-sdk.example", Roots: roots})
	require.NoError(t, err, "the ACME leaf must verify against the returned CA chain")

	certificates, err := acmClient.ListCertificates(ctx, &acm.ListCertificatesInput{})
	require.NoError(t, err)
	var certificateArn string
	for _, summary := range certificates.CertificateSummaryList {
		if aws.ToString(summary.DomainName) == "service.acme-sdk.example" {
			certificateArn = aws.ToString(summary.CertificateArn)
		}
	}
	require.NotEmpty(t, certificateArn)
	certificate, err := acmClient.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(certificateArn)})
	require.NoError(t, err)
	require.Equal(t, acmtypes.CertificateKeyPairOriginAcme, certificate.Certificate.CertificateKeyPairOrigin)
	require.Equal(t, endpointArn, aws.ToString(certificate.Certificate.AcmeEndpointArn))

	require.NoError(t, acmeClient.RevokeCert(ctx, nil, chain[0], acme.CRLReasonKeyCompromise))
	certificate, err = acmClient.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: aws.String(certificateArn)})
	require.NoError(t, err)
	require.Equal(t, acmtypes.CertificateStatusRevoked, certificate.Certificate.Status)

	_, err = acmClient.RevokeAcmeAccount(ctx, &acm.RevokeAcmeAccountInput{
		AcmeEndpointArn: aws.String(endpointArn),
		AccountUrl:      aws.String(account.URI),
	})
	require.NoError(t, err)
	_, err = acmClient.RevokeAcmeExternalAccountBinding(ctx, &acm.RevokeAcmeExternalAccountBindingInput{
		AcmeExternalAccountBindingArn: aws.String(bindingArn),
	})
	require.NoError(t, err)
	_, err = acmClient.DeleteAcmeExternalAccountBinding(ctx, &acm.DeleteAcmeExternalAccountBindingInput{
		AcmeExternalAccountBindingArn: aws.String(bindingArn),
	})
	require.NoError(t, err)
	_, err = acmClient.DeleteAcmeDomainValidation(ctx, &acm.DeleteAcmeDomainValidationInput{
		AcmeDomainValidationArn: aws.String(validationArn),
	})
	require.NoError(t, err)
	_, err = acmClient.DeleteCertificate(ctx, &acm.DeleteCertificateInput{CertificateArn: aws.String(certificateArn)})
	require.NoError(t, err)
	_, err = acmClient.DeleteAcmeEndpoint(ctx, &acm.DeleteAcmeEndpointInput{AcmeEndpointArn: aws.String(endpointArn)})
	require.NoError(t, err)
}
