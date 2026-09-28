package aws_sdk_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acmpca"
	pcatypes "github.com/aws/aws-sdk-go-v2/service/acmpca/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A revocation publishes the CA's CRL to its bucket and counts CRLGenerated;
// a bucket the CRL cannot be written to counts MisconfiguredCRLBucket while
// the revocation itself stands, as AWS Private CA reports it.
func TestPrivateCA_CRLPublicationIsCountedInCloudWatch(t *testing.T) {
	for _, tc := range []struct {
		name, bucket, metric string
		createBucket         bool
	}{
		{"published", "sdk-private-ca-crl-published", "CRLGenerated", true},
		{"misconfigured", "sdk-private-ca-crl-missing-bucket", "MisconfiguredCRLBucket", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := acmpcaClient()
			if tc.createBucket {
				_, err := s3Client().CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(tc.bucket)})
				require.NoError(t, err)
			}
			arn := activateRootPrivateCA(t, "sdk-crl-"+tc.name)
			_, err := client.UpdateCertificateAuthority(ctx, &acmpca.UpdateCertificateAuthorityInput{
				CertificateAuthorityArn: aws.String(arn),
				RevocationConfiguration: &pcatypes.RevocationConfiguration{CrlConfiguration: &pcatypes.CrlConfiguration{
					Enabled: aws.Bool(true), S3BucketName: aws.String(tc.bucket), ExpirationInDays: aws.Int32(7),
				}},
			})
			require.NoError(t, err)

			key, err := rsa.GenerateKey(rand.Reader, 2048)
			require.NoError(t, err)
			csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
				Subject: pkix.Name{CommonName: "crl-" + tc.name + ".private.example.com"},
			}, key)
			require.NoError(t, err)
			issued, err := client.IssueCertificate(ctx, &acmpca.IssueCertificateInput{
				CertificateAuthorityArn: aws.String(arn),
				Csr:                     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}),
				SigningAlgorithm:        pcatypes.SigningAlgorithmSha256withrsa,
				Validity:                &pcatypes.Validity{Type: pcatypes.ValidityPeriodTypeDays, Value: aws.Int64(30)},
			})
			require.NoError(t, err)
			material, err := client.GetCertificate(ctx, &acmpca.GetCertificateInput{
				CertificateAuthorityArn: aws.String(arn), CertificateArn: issued.CertificateArn,
			})
			require.NoError(t, err)
			leaf := parseCertificatePEM(t, aws.ToString(material.Certificate))

			start := time.Now().UTC().Add(-time.Minute)
			_, err = client.RevokeCertificate(ctx, &acmpca.RevokeCertificateInput{
				CertificateAuthorityArn: aws.String(arn),
				CertificateSerial:       aws.String(leaf.SerialNumber.String()),
				RevocationReason:        pcatypes.RevocationReasonKeyCompromise,
			})
			require.NoError(t, err, "a CRL the bucket cannot take does not fail the revocation")

			if tc.createBucket {
				object, err := s3Client().GetObject(ctx, &s3.GetObjectInput{
					Bucket: aws.String(tc.bucket), Key: aws.String(arn[strings.LastIndex(arn, "/")+1:] + ".crl"),
				})
				require.NoError(t, err)
				der, err := io.ReadAll(object.Body)
				require.NoError(t, object.Body.Close())
				require.NoError(t, err)
				crl, err := x509.ParseRevocationList(der)
				require.NoError(t, err)
				require.Len(t, crl.RevokedCertificateEntries, 1)
				assert.Equal(t, 0, crl.RevokedCertificateEntries[0].SerialNumber.Cmp(leaf.SerialNumber))
			}

			stats, err := cloudwatchClient().GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
				Namespace:  aws.String("AWS/ACMPrivateCA"),
				MetricName: aws.String(tc.metric),
				Dimensions: []cwtypes.Dimension{{Name: aws.String("PrivateCAArn"), Value: aws.String(arn)}},
				StartTime:  aws.Time(start),
				EndTime:    aws.Time(time.Now().UTC().Add(time.Minute)),
				Period:     aws.Int32(300),
				Statistics: []cwtypes.Statistic{cwtypes.StatisticSum},
			})
			require.NoError(t, err)
			var sum float64
			for _, point := range stats.Datapoints {
				sum += aws.ToFloat64(point.Sum)
			}
			assert.Equal(t, float64(1), sum, "one %s datapoint for the revocation", tc.metric)
		})
	}
}
