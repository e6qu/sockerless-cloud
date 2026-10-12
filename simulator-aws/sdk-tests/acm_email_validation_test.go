package aws_sdk_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/stretchr/testify/require"
)

func runSMTPReceiver(t *testing.T) <-chan string {
	t.Helper()
	listener, err := net.Listen("tcp", ":25")
	require.NoError(t, err, "Amazon Certificate Manager email validation requires an available local SMTP port")
	t.Cleanup(func() { _ = listener.Close() })
	messages := make(chan string, 4)
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go receiveSMTPMessage(connection, messages)
		}
	}()
	return messages
}

func receiveSMTPMessage(connection net.Conn, messages chan<- string) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	write := func(line string) {
		_, _ = writer.WriteString(line + "\r\n")
		_ = writer.Flush()
	}
	write("220 localhost ESMTP")
	var body strings.Builder
	inData := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				messages <- body.String()
				write("250 2.0.0 queued")
				inData = false
			} else {
				body.WriteString(line)
				body.WriteByte('\n')
			}
			continue
		}
		command := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(command, "EHLO"):
			_, _ = writer.WriteString("250-localhost\r\n250 8BITMIME\r\n")
			_ = writer.Flush()
		case strings.HasPrefix(command, "HELO"), strings.HasPrefix(command, "MAIL FROM"),
			strings.HasPrefix(command, "RCPT TO"):
			write("250 2.1.0 ok")
		case command == "DATA":
			body.Reset()
			inData = true
			write("354 End data with <CR><LF>.<CR><LF>")
		case command == "QUIT":
			write("221 2.0.0 bye")
			return
		default:
			write("502 5.5.2 command not recognized")
		}
	}
}

func awaitValidationEmail(t *testing.T, messages <-chan string) string {
	t.Helper()
	select {
	case message := <-messages:
		return message
	case <-time.After(10 * time.Second):
		t.Fatal("Amazon Certificate Manager did not deliver the validation email over SMTP")
		return ""
	}
}

func TestACMEmailValidationAndResendUseRealSMTP(t *testing.T) {
	messages := runSMTPReceiver(t)
	client := acmClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const domain = "email-validation.example.test"
	created, err := client.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String(domain),
		ValidationMethod: acmtypes.ValidationMethodEmail,
		DomainValidationOptions: []acmtypes.DomainValidationOption{{
			DomainName:       aws.String(domain),
			ValidationDomain: aws.String("localhost"),
		}},
	})
	require.NoError(t, err)
	arn := aws.ToString(created.CertificateArn)
	t.Cleanup(func() {
		_, _ = client.DeleteCertificate(context.Background(), &acm.DeleteCertificateInput{CertificateArn: &arn})
	})

	firstMessage := awaitValidationEmail(t, messages)
	require.Contains(t, firstMessage, domain)
	linkPattern := regexp.MustCompile(`https?://[^[:space:]]+/acm/email-validation/[a-f0-9]+`)
	validationURL := linkPattern.FindString(firstMessage)
	require.NotEmpty(t, validationURL, "validation email body: %s", firstMessage)

	_, err = client.ResendValidationEmail(ctx, &acm.ResendValidationEmailInput{
		CertificateArn:   &arn,
		Domain:           aws.String(domain),
		ValidationDomain: aws.String("localhost"),
	})
	require.NoError(t, err)
	secondMessage := awaitValidationEmail(t, messages)
	require.Contains(t, secondMessage, "/acm/email-validation/")

	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(validationURL)
	require.NoError(t, err)
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)

	described, err := client.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: &arn})
	require.NoError(t, err)
	require.Equal(t, acmtypes.CertificateStatusIssued, described.Certificate.Status)
	require.Equal(t, acmtypes.DomainStatusSuccess, described.Certificate.DomainValidationOptions[0].ValidationStatus)
}
