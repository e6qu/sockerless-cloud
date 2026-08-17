package aws_sdk_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/stretchr/testify/require"
)

// waitForELBv2TargetHealth blocks until Elastic Load Balancing's health
// checker reports the target in the wanted state. A target is not in service
// the instant it is registered — "After your target is registered, it must
// pass one health check to be considered healthy" — and leaving service takes
// UnhealthyThresholdCount consecutive failed checks at the target group's
// configured interval, so a client polls the health before depending on it.
func waitForELBv2TargetHealth(
	t *testing.T,
	targetGroupArn string,
	target elbtypes.TargetDescription,
	want elbtypes.TargetHealthStateEnum,
) {
	t.Helper()
	elb := elbv2Client()
	observed := "none"
	require.Eventuallyf(t, func() bool {
		health, err := elb.DescribeTargetHealth(ctx, &elbv2.DescribeTargetHealthInput{
			TargetGroupArn: aws.String(targetGroupArn),
			Targets:        []elbtypes.TargetDescription{target},
		})
		if err != nil || len(health.TargetHealthDescriptions) != 1 {
			observed = fmt.Sprintf("describe failed: %v", err)
			return false
		}
		observed = string(health.TargetHealthDescriptions[0].TargetHealth.State)
		return observed == string(want)
	}, 30*time.Second, 100*time.Millisecond,
		"target %s:%d in %s never reported %s",
		aws.ToString(target.Id), aws.ToInt32(target.Port), targetGroupArn, want)
	t.Logf("target %s:%d reported %s (last observed %s)",
		aws.ToString(target.Id), aws.ToInt32(target.Port), want, observed)
}

func importELBv2Certificate(t *testing.T, domain string) string {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)
	out, err := acmClient().ImportCertificate(ctx, &acm.ImportCertificateInput{
		Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}),
		PrivateKey:  pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}),
	})
	require.NoError(t, err)
	return aws.ToString(out.CertificateArn)
}

func availableELBv2ListenerPort(t *testing.T) int32 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := int32(listener.Addr().(*net.TCPAddr).Port)
	require.NoError(t, listener.Close())
	return port
}
