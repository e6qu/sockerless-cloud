package aws_sdk_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/aws-sdk-go-v2/service/acmpca"
	pcatypes "github.com/aws/aws-sdk-go-v2/service/acmpca/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func acmpcaClient() *acmpca.Client {
	return acmpca.NewFromConfig(sdkConfig(), func(o *acmpca.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func activateRootPrivateCA(t *testing.T, name string) string {
	t.Helper()
	client := acmpcaClient()
	created, err := client.CreateCertificateAuthority(ctx, &acmpca.CreateCertificateAuthorityInput{
		CertificateAuthorityType: pcatypes.CertificateAuthorityTypeRoot,
		CertificateAuthorityConfiguration: &pcatypes.CertificateAuthorityConfiguration{
			KeyAlgorithm:     pcatypes.KeyAlgorithmRsa2048,
			SigningAlgorithm: pcatypes.SigningAlgorithmSha256withrsa,
			Subject:          &pcatypes.ASN1Subject{CommonName: aws.String(name), Organization: aws.String("Sockerless")},
		},
		IdempotencyToken: aws.String(name),
		Tags:             []pcatypes.Tag{{Key: aws.String("environment"), Value: aws.String("test")}},
	})
	require.NoError(t, err)
	arn := aws.ToString(created.CertificateAuthorityArn)
	require.NotEmpty(t, arn)

	csr, err := client.GetCertificateAuthorityCsr(ctx, &acmpca.GetCertificateAuthorityCsrInput{
		CertificateAuthorityArn: aws.String(arn),
	})
	require.NoError(t, err)
	issued, err := client.IssueCertificate(ctx, &acmpca.IssueCertificateInput{
		CertificateAuthorityArn: aws.String(arn),
		Csr:                     []byte(aws.ToString(csr.Csr)),
		SigningAlgorithm:        pcatypes.SigningAlgorithmSha256withrsa,
		TemplateArn:             aws.String("arn:aws:acm-pca:::template/RootCACertificate/V1"),
		Validity:                &pcatypes.Validity{Type: pcatypes.ValidityPeriodTypeYears, Value: aws.Int64(10)},
		IdempotencyToken:        aws.String(name + "-root"),
	})
	require.NoError(t, err)
	root, err := client.GetCertificate(ctx, &acmpca.GetCertificateInput{
		CertificateAuthorityArn: aws.String(arn), CertificateArn: issued.CertificateArn,
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(root.Certificate))
	_, err = client.ImportCertificateAuthorityCertificate(ctx, &acmpca.ImportCertificateAuthorityCertificateInput{
		CertificateAuthorityArn: aws.String(arn), Certificate: []byte(aws.ToString(root.Certificate)),
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = client.UpdateCertificateAuthority(ctx, &acmpca.UpdateCertificateAuthorityInput{
			CertificateAuthorityArn: aws.String(arn), Status: pcatypes.CertificateAuthorityStatusDisabled,
		})
		_, _ = client.DeleteCertificateAuthority(ctx, &acmpca.DeleteCertificateAuthorityInput{
			CertificateAuthorityArn: aws.String(arn), PermanentDeletionTimeInDays: aws.Int32(7),
		})
	})
	return arn
}

func TestPrivateCA_CompleteLifecycleAndACMIntegration(t *testing.T) {
	client := acmpcaClient()
	arn := activateRootPrivateCA(t, "sdk-private-root")

	described, err := client.DescribeCertificateAuthority(ctx, &acmpca.DescribeCertificateAuthorityInput{
		CertificateAuthorityArn: aws.String(arn),
	})
	require.NoError(t, err)
	require.NotNil(t, described.CertificateAuthority)
	assert.Equal(t, pcatypes.CertificateAuthorityStatusActive, described.CertificateAuthority.Status)

	listed, err := client.ListCertificateAuthorities(ctx, &acmpca.ListCertificateAuthoritiesInput{})
	require.NoError(t, err)
	require.NotEmpty(t, listed.CertificateAuthorities)

	authorityCertificate, err := client.GetCertificateAuthorityCertificate(ctx, &acmpca.GetCertificateAuthorityCertificateInput{
		CertificateAuthorityArn: aws.String(arn),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(authorityCertificate.Certificate))

	tags, err := client.ListTags(ctx, &acmpca.ListTagsInput{CertificateAuthorityArn: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)
	_, err = client.TagCertificateAuthority(ctx, &acmpca.TagCertificateAuthorityInput{
		CertificateAuthorityArn: aws.String(arn),
		Tags:                    []pcatypes.Tag{{Key: aws.String("owner"), Value: aws.String("platform")}},
	})
	require.NoError(t, err)
	_, err = client.UntagCertificateAuthority(ctx, &acmpca.UntagCertificateAuthorityInput{
		CertificateAuthorityArn: aws.String(arn),
		Tags:                    []pcatypes.Tag{{Key: aws.String("environment"), Value: aws.String("test")}},
	})
	require.NoError(t, err)

	_, err = client.CreatePermission(ctx, &acmpca.CreatePermissionInput{
		CertificateAuthorityArn: aws.String(arn),
		Principal:               aws.String("acm.amazonaws.com"),
		SourceAccount:           aws.String("000000000000"),
		Actions: []pcatypes.ActionType{
			pcatypes.ActionTypeIssueCertificate,
			pcatypes.ActionTypeGetCertificate,
			pcatypes.ActionTypeListPermissions,
		},
	})
	require.NoError(t, err)
	permissions, err := client.ListPermissions(ctx, &acmpca.ListPermissionsInput{
		CertificateAuthorityArn: aws.String(arn),
	})
	require.NoError(t, err)
	require.Len(t, permissions.Permissions, 1)
	_, err = client.DeletePermission(ctx, &acmpca.DeletePermissionInput{
		CertificateAuthorityArn: aws.String(arn),
		Principal:               aws.String("acm.amazonaws.com"),
		SourceAccount:           aws.String("000000000000"),
	})
	require.NoError(t, err)

	const policy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"acm-pca:DescribeCertificateAuthority","Resource":"*"}]}`
	_, err = client.PutPolicy(ctx, &acmpca.PutPolicyInput{ResourceArn: aws.String(arn), Policy: aws.String(policy)})
	require.NoError(t, err)
	gotPolicy, err := client.GetPolicy(ctx, &acmpca.GetPolicyInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	assert.JSONEq(t, policy, aws.ToString(gotPolicy.Policy))
	_, err = client.DeletePolicy(ctx, &acmpca.DeletePolicyInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "service.private.example.com"},
		DNSNames: []string{"service.private.example.com"},
	}, key)
	require.NoError(t, err)
	leaf, err := client.IssueCertificate(ctx, &acmpca.IssueCertificateInput{
		CertificateAuthorityArn: aws.String(arn),
		Csr:                     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}),
		SigningAlgorithm:        pcatypes.SigningAlgorithmSha256withrsa,
		Validity:                &pcatypes.Validity{Type: pcatypes.ValidityPeriodTypeDays, Value: aws.Int64(30)},
	})
	require.NoError(t, err)
	leafMaterial, err := client.GetCertificate(ctx, &acmpca.GetCertificateInput{
		CertificateAuthorityArn: aws.String(arn), CertificateArn: leaf.CertificateArn,
	})
	require.NoError(t, err)
	block, _ := pem.Decode([]byte(aws.ToString(leafMaterial.Certificate)))
	require.NotNil(t, block)
	parsedLeaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "service.private.example.com", parsedLeaf.Subject.CommonName)
	require.NoError(t, parsedLeaf.CheckSignatureFrom(parseCertificatePEM(t, aws.ToString(authorityCertificate.Certificate))))

	_, err = client.RevokeCertificate(ctx, &acmpca.RevokeCertificateInput{
		CertificateAuthorityArn: aws.String(arn),
		CertificateSerial:       aws.String(parsedLeaf.SerialNumber.String()),
		RevocationReason:        pcatypes.RevocationReasonKeyCompromise,
	})
	require.NoError(t, err)

	bucket := "sdk-private-ca-audit"
	s3c := s3Client()
	_, err = s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	t.Cleanup(func() {
		objects, _ := s3c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
		for _, object := range objects.Contents {
			_, _ = s3c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: object.Key})
		}
		_, _ = s3c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	})
	report, err := client.CreateCertificateAuthorityAuditReport(ctx, &acmpca.CreateCertificateAuthorityAuditReportInput{
		CertificateAuthorityArn:   aws.String(arn),
		S3BucketName:              aws.String(bucket),
		AuditReportResponseFormat: pcatypes.AuditReportResponseFormatJson,
	})
	require.NoError(t, err)
	reportStatus, err := client.DescribeCertificateAuthorityAuditReport(ctx, &acmpca.DescribeCertificateAuthorityAuditReportInput{
		CertificateAuthorityArn: aws.String(arn), AuditReportId: report.AuditReportId,
	})
	require.NoError(t, err)
	assert.Equal(t, pcatypes.AuditReportStatusSuccess, reportStatus.AuditReportStatus)
	object, err := s3c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: report.S3Key})
	require.NoError(t, err)
	defer object.Body.Close()
	reportBody, err := io.ReadAll(object.Body)
	require.NoError(t, err)
	assert.Contains(t, string(reportBody), aws.ToString(leaf.CertificateArn))

	acmc := acmClient()
	privateCertificate, err := acmc.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:              aws.String("managed.private.example.com"),
		SubjectAlternativeNames: []string{"api.private.example.com"},
		CertificateAuthorityArn: aws.String(arn),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = acmc.DeleteCertificate(ctx, &acm.DeleteCertificateInput{CertificateArn: privateCertificate.CertificateArn})
	})
	details, err := acmc.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: privateCertificate.CertificateArn})
	require.NoError(t, err)
	assert.Equal(t, acmtypes.CertificateTypePrivate, details.Certificate.Type)
	assert.Equal(t, acmtypes.CertificateStatusIssued, details.Certificate.Status)
	exported, err := acmc.ExportCertificate(ctx, &acm.ExportCertificateInput{
		CertificateArn: privateCertificate.CertificateArn, Passphrase: []byte("real-passphrase"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(exported.Certificate))
	encryptedKey, _ := pem.Decode([]byte(aws.ToString(exported.PrivateKey)))
	require.NotNil(t, encryptedKey)
	_, err = x509.DecryptPEMBlock(encryptedKey, []byte("real-passphrase"))
	require.NoError(t, err)
	_, err = acmc.RevokeCertificate(ctx, &acm.RevokeCertificateInput{
		CertificateArn: privateCertificate.CertificateArn, RevocationReason: acmtypes.RevocationReasonKeyCompromise,
	})
	require.NoError(t, err)

	_, err = client.UpdateCertificateAuthority(ctx, &acmpca.UpdateCertificateAuthorityInput{
		CertificateAuthorityArn: aws.String(arn), Status: pcatypes.CertificateAuthorityStatusDisabled,
	})
	require.NoError(t, err)
	_, err = client.DeleteCertificateAuthority(ctx, &acmpca.DeleteCertificateAuthorityInput{
		CertificateAuthorityArn: aws.String(arn), PermanentDeletionTimeInDays: aws.Int32(7),
	})
	require.NoError(t, err)
	_, err = client.RestoreCertificateAuthority(ctx, &acmpca.RestoreCertificateAuthorityInput{
		CertificateAuthorityArn: aws.String(arn),
	})
	require.NoError(t, err)
	_, err = client.UpdateCertificateAuthority(ctx, &acmpca.UpdateCertificateAuthorityInput{
		CertificateAuthorityArn: aws.String(arn), Status: pcatypes.CertificateAuthorityStatusActive,
	})
	require.NoError(t, err)
}

func parseCertificatePEM(t *testing.T, value string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(value))
	require.NotNil(t, block)
	certificate, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return certificate
}
