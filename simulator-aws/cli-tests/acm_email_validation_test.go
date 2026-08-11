package aws_cli_test

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func acmCLIStartSMTPReceiver(t *testing.T) <-chan string {
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
			go acmCLIReceiveSMTP(connection, messages)
		}
	}()
	return messages
}

func acmCLIReceiveSMTP(connection net.Conn, messages chan<- string) {
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
		switch command := strings.ToUpper(line); {
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

func acmCLIAwaitEmail(t *testing.T, messages <-chan string) string {
	t.Helper()
	select {
	case message := <-messages:
		return message
	case <-time.After(10 * time.Second):
		t.Fatal("Amazon Certificate Manager did not deliver the validation email over SMTP")
		return ""
	}
}

func TestACMCLI_EmailValidationAndResendUseRealSMTP(t *testing.T) {
	messages := acmCLIStartSMTPReceiver(t)
	const domain = "email-validation.cli.example.test"
	createdJSON := runCLI(t, awsCLI("acm", "request-certificate",
		"--domain-name", domain,
		"--validation-method", "EMAIL",
		"--domain-validation-options", "DomainName="+domain+",ValidationDomain=localhost",
		"--output", "json"))
	var created struct {
		CertificateArn string `json:"CertificateArn"`
	}
	require.NoError(t, json.Unmarshal([]byte(createdJSON), &created))
	require.NotEmpty(t, created.CertificateArn)

	firstMessage := acmCLIAwaitEmail(t, messages)
	pattern := regexp.MustCompile(`https?://[^[:space:]]+/acm/email-validation/[a-f0-9]+`)
	validationURL := pattern.FindString(firstMessage)
	require.NotEmpty(t, validationURL, "validation email body: %s", firstMessage)

	runCLI(t, awsCLI("acm", "resend-validation-email",
		"--certificate-arn", created.CertificateArn,
		"--domain", domain,
		"--validation-domain", "localhost"))
	require.Contains(t, acmCLIAwaitEmail(t, messages), "/acm/email-validation/")

	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(validationURL)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)

	describedJSON := runCLI(t, awsCLI("acm", "describe-certificate",
		"--certificate-arn", created.CertificateArn, "--output", "json"))
	var described struct {
		Certificate struct {
			Status string `json:"Status"`
		} `json:"Certificate"`
	}
	require.NoError(t, json.Unmarshal([]byte(describedJSON), &described))
	require.Equal(t, "ISSUED", described.Certificate.Status)
}
