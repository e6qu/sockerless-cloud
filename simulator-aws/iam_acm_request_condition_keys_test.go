package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// jsonServiceConditionContext runs a JSON-protocol service's registered
// populators over a request body, as the gate does.
func jsonServiceConditionContext(t *testing.T, service, operation, body string) map[string][]string {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	ctx := map[string][]string{}
	iamRunRequestConditionPopulators(r, service, operation, []byte(body), ctx)
	return ctx
}

func TestACMConditionKeysReadTheCertificateRequest(t *testing.T) {
	ctx := jsonServiceConditionContext(t, "acm", "RequestCertificate", `{
		"DomainName": "example.com",
		"SubjectAlternativeNames": ["example.com", "www.example.com"],
		"ValidationMethod": "DNS",
		"CertificateAuthorityArn": "arn:aws:acm-pca:us-east-1:123456789012:certificate-authority/ca",
		"KeyAlgorithm": "EC_prime256v1",
		"Options": {"Export": "ENABLED", "CertificateTransparencyLoggingPreference": "DISABLED"}
	}`)
	assertServiceConditionContext(t, ctx, map[string][]string{
		"acm:DomainNames":                    {"example.com", "www.example.com"},
		"acm:ValidationMethod":               {"DNS"},
		"acm:CertificateAuthority":           {"arn:aws:acm-pca:us-east-1:123456789012:certificate-authority/ca"},
		"acm:KeyAlgorithm":                   {"EC_prime256v1"},
		"acm:Export":                         {"ENABLED"},
		"acm:CertificateTransparencyLogging": {"DISABLED"},
	})
}

// AWS defines what an absent validation method and transparency preference
// mean; every other key is absent with its member.
func TestACMConditionKeysAreAbsentForAbsentMembers(t *testing.T) {
	ctx := jsonServiceConditionContext(t, "acm", "RequestCertificate", `{"DomainName": "example.com"}`)
	assertServiceConditionContext(t, ctx, map[string][]string{
		"acm:DomainNames":                    {"example.com"},
		"acm:ValidationMethod":               {"EMAIL"},
		"acm:CertificateTransparencyLogging": {"ENABLED"},
		"acm:CertificateAuthority":           nil,
		"acm:KeyAlgorithm":                   nil,
		"acm:Export":                         nil,
	})
	if len(ctx) != 3 {
		t.Errorf("context = %v, want only the three keys above", ctx)
	}

	ctx = jsonServiceConditionContext(t, "acm", "UpdateCertificateOptions", `{"CertificateArn": "arn", "Options": {"ValidationMethod": "DNS"}}`)
	assertServiceConditionContext(t, ctx, map[string][]string{"acm:ValidationMethod": {"DNS"}})

	ctx = jsonServiceConditionContext(t, "acm", "DescribeCertificate", `{"CertificateArn": "arn"}`)
	if len(ctx) != 0 {
		t.Errorf("DescribeCertificate settled %v", ctx)
	}
}

// ExportCertificate and RevokeCertificate name a certificate, and its domains
// are the ones it was issued for.
func TestACMConditionKeysReadTheNamedCertificatesDomains(t *testing.T) {
	AwaitSimulatorBackground()
	acmCertificates = sim.MakeStore[acmStoredCert](nil, "acm_certificates")
	acmCertificates.Put("c-1", acmStoredCert{Cert: ACMCertificate{
		CertificateArn:          acmCertARN("c-1"),
		DomainName:              "internal.example.com",
		SubjectAlternativeNames: []string{"internal.example.com", "api.internal.example.com"},
	}})
	for _, operation := range []string{"ExportCertificate", "RevokeCertificate"} {
		ctx := jsonServiceConditionContext(t, "acm", operation, `{"CertificateArn": "`+acmCertARN("c-1")+`"}`)
		assertServiceConditionContext(t, ctx, map[string][]string{
			"acm:DomainNames": {"internal.example.com", "api.internal.example.com"},
		})
	}
	ctx := jsonServiceConditionContext(t, "acm", "ExportCertificate", `{"CertificateArn": "`+acmCertARN("c-missing")+`"}`)
	if len(ctx) != 0 {
		t.Errorf("a certificate that does not exist settled %v", ctx)
	}
}
