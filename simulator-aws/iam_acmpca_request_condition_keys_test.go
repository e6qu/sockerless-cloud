package main

import "testing"

// TestACMPCAConditionKeysReadTheCertificateTemplate proves a policy can allow
// an issuer one certificate template and not another: the key carries the
// template the request names, and nothing when the request names none, so a
// policy that requires a template refuses a request that leaves it to the
// service's default.
func TestACMPCAConditionKeysReadTheCertificateTemplate(t *testing.T) {
	const template = "arn:aws:acm-pca:::template/EndEntityCertificate/V1"
	ctx := jsonConditionContext("acm-pca", "IssueCertificate",
		`{"CertificateAuthorityArn":"arn:aws:acm-pca:us-east-1:123456789012:certificate-authority/ca",`+
			`"TemplateArn":"`+template+`","SigningAlgorithm":"SHA256WITHRSA"}`)
	assertConditionValues(t, ctx, map[string][]string{"acm-pca:TemplateArn": {template}})

	unnamed := jsonConditionContext("acm-pca", "IssueCertificate",
		`{"CertificateAuthorityArn":"arn:aws:acm-pca:us-east-1:123456789012:certificate-authority/ca"}`)
	if got := unnamed["acm-pca:TemplateArn"]; len(got) != 0 {
		t.Errorf("a request naming no template produced %v — there is no template to authorize", got)
	}

	// The key belongs to IssueCertificate alone.
	other := jsonConditionContext("acm-pca", "GetCertificate", `{"TemplateArn":"`+template+`"}`)
	if got := other["acm-pca:TemplateArn"]; len(got) != 0 {
		t.Errorf("GetCertificate produced %v for a key the reference declares only on IssueCertificate", got)
	}
}
