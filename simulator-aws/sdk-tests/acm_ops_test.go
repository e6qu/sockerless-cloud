package aws_sdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestUnknownPrivateCA proves ACM does not manufacture a certificate
// authority merely because the caller supplied a syntactically valid ARN.
func requestUnknownPrivateCA(t *testing.T, c *acm.Client, domain string) {
	t.Helper()
	_, err := c.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:              aws.String(domain),
		CertificateAuthorityArn: aws.String("arn:aws:acm-pca:us-east-1:000000000000:certificate-authority/test-ca"),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "ResourceNotFoundException")
}

func TestACM_RejectsUnknownPrivateCertificateAuthority(t *testing.T) {
	c := acmClient()
	requestUnknownPrivateCA(t, c, "unknown-ca.private.example.com")
}

// acmWireErrorCode posts one AWS Certificate Manager request straight to the
// simulator and returns the error code it answered with. Some required members
// are validated by the generated client before a request is sent, so the
// service's own validation of a missing one is unreachable through the typed
// client and has to be driven over the wire.
func acmWireErrorCode(t *testing.T, target string, body map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/", bytes.NewReader(payload))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/x-amz-json-1.1")
	request.Header.Set("X-Amz-Target", target)
	// Signed the way the generated client signs: the control plane refuses an
	// unsigned request before it validates anything, so an unsigned probe
	// would report the authentication refusal and never reach the check under
	// test.
	signRawSigV4JSON(t, request, "acm", payload)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, response.StatusCode,
		"the service accepted a request it must refuse: %s", raw)
	var refusal struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(raw, &refusal), string(raw))
	// __type may be namespaced (com.amazonaws...#Code); the code is the tail.
	code := refusal.Type
	if index := strings.LastIndexAny(code, "#."); index >= 0 {
		code = code[index+1:]
	}
	return code
}

// TestACM_ExportCertificate proves public Amazon-issued certificates cannot
// expose their service-held private key.
func TestACM_ExportCertificate(t *testing.T) {
	c := acmClient()

	reqOut, err := c.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String("public.example.com"),
		ValidationMethod: acmtypes.ValidationMethodDns,
	})
	require.NoError(t, err)
	arn := aws.ToString(reqOut.CertificateArn)
	t.Cleanup(func() {
		_, _ = c.DeleteCertificate(context.Background(), &acm.DeleteCertificateInput{CertificateArn: aws.String(arn)})
	})

	// A passphrase-less export never reaches the service: the client validates
	// its own required members and refuses to send the request. Asserting a
	// service refusal here is asserting something that cannot happen, and a
	// bare "an error happened" passes on this client-side failure while
	// proving nothing about the simulator at all — which is what it was doing.
	_, err = c.ExportCertificate(ctx, &acm.ExportCertificateInput{CertificateArn: aws.String(arn)})
	require.ErrorContains(t, err, "missing required field, ExportCertificateInput.Passphrase",
		"the client refuses to send an export with no passphrase")
	var unsent smithy.APIError
	require.False(t, errors.As(err, &unsent),
		"a request the client never sent carries no service error: %v", err)

	// The service's own refusal is reachable only over the wire, so that is
	// where it is exercised.
	assert.Equal(t, "InvalidParameterValueException",
		acmWireErrorCode(t, "CertificateManager.ExportCertificate",
			map[string]any{"CertificateArn": arn}),
		"the service refuses an export with no passphrase as an invalid parameter")

	_, err = c.ExportCertificate(ctx, &acm.ExportCertificateInput{
		CertificateArn: aws.String(arn),
		Passphrase:     []byte("x"),
	})
	assert.Equal(t, "RequestInProgressException", errCode(t, err),
		"a non-PRIVATE certificate must not be exportable")
}

// TestACM_RevokeCertificate proves the private-certificate-only operation
// rejects an Amazon-issued request.
func TestACM_RevokeCertificate(t *testing.T) {
	c := acmClient()

	req, err := c.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String("revoke.public.example.com"),
		ValidationMethod: acmtypes.ValidationMethodDns,
	})
	require.NoError(t, err)
	arn := aws.ToString(req.CertificateArn)
	t.Cleanup(func() {
		_, _ = c.DeleteCertificate(context.Background(), &acm.DeleteCertificateInput{CertificateArn: aws.String(arn)})
	})
	_, err = c.RevokeCertificate(ctx, &acm.RevokeCertificateInput{
		CertificateArn:   aws.String(arn),
		RevocationReason: acmtypes.RevocationReasonKeyCompromise,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceInUseException")
}

// TestACM_AccountConfiguration pins the account expiry-events config
// round-trip: default 45 days, then a Put updates it.
func TestACM_AccountConfiguration(t *testing.T) {
	c := acmClient()

	_, err := c.PutAccountConfiguration(ctx, &acm.PutAccountConfigurationInput{
		ExpiryEvents:     &acmtypes.ExpiryEventsConfiguration{DaysBeforeExpiry: aws.Int32(30)},
		IdempotencyToken: aws.String("tok-account-config"),
	})
	require.NoError(t, err)

	got, err := c.GetAccountConfiguration(ctx, &acm.GetAccountConfigurationInput{})
	require.NoError(t, err)
	require.NotNil(t, got.ExpiryEvents)
	require.NotNil(t, got.ExpiryEvents.DaysBeforeExpiry)
	assert.EqualValues(t, 30, *got.ExpiryEvents.DaysBeforeExpiry)

	// As with a passphrase-less export, the client refuses to send a
	// configuration write with no idempotency token, so the service never sees
	// it and the refusal to assert here is the client's.
	_, err = c.PutAccountConfiguration(ctx, &acm.PutAccountConfigurationInput{
		ExpiryEvents: &acmtypes.ExpiryEventsConfiguration{DaysBeforeExpiry: aws.Int32(15)},
	})
	require.ErrorContains(t, err, "missing required field, PutAccountConfigurationInput.IdempotencyToken",
		"the client refuses to send a configuration write with no idempotency token")

	// The service's own refusal, over the wire.
	assert.Equal(t, "InvalidParameterValueException",
		acmWireErrorCode(t, "CertificateManager.PutAccountConfiguration",
			map[string]any{"ExpiryEvents": map[string]any{"DaysBeforeExpiry": 15}}),
		"the service refuses a configuration write with no idempotency token")
}

// TestACM_SearchCertificates pins SearchCertificates filtering by metadata
// (Type) and returning per-cert metadata results.
func TestACM_SearchCertificates(t *testing.T) {
	c := acmClient()

	requestUnknownPrivateCA(t, c, "search-private.example.com")

	deadline := time.Now().Add(5 * time.Second)
	var foundSynthetic bool
	for time.Now().Before(deadline) {
		out, err := c.SearchCertificates(ctx, &acm.SearchCertificatesInput{
			FilterStatement: &acmtypes.CertificateFilterStatementMemberFilter{
				Value: &acmtypes.CertificateFilterMemberAcmCertificateMetadataFilter{
					Value: &acmtypes.AcmCertificateMetadataFilterMemberType{
						Value: acmtypes.CertificateTypePrivate,
					},
				},
			},
		})
		require.NoError(t, err)
		for _, res := range out.Results {
			meta, ok := res.CertificateMetadata.(*acmtypes.CertificateMetadataMemberAcmCertificateMetadata)
			require.True(t, ok, "CertificateMetadata must carry AcmCertificateMetadata")
			// Every returned cert must be PRIVATE per the filter.
			assert.Equal(t, acmtypes.CertificateTypePrivate, meta.Value.Type)
			foundSynthetic = true
		}
		if foundSynthetic {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	assert.False(t, foundSynthetic, "a missing AWS Private CA must never create synthetic PRIVATE certificate state")
}
