package main

import (
	"encoding/json"
	"net/http"
)

// AWS Certificate Manager's request condition keys: the domains, issuer, key
// algorithm, validation and export options a certificate request asks for,
// which a policy holds to an approved set.

func init() {
	registerIAMRequestConditionPopulator("acm", iamPopulateACMRequestConditionKeys)
}

type acmConditionRequest struct {
	CertificateArn          string
	DomainName              string
	SubjectAlternativeNames []string
	ValidationMethod        string
	CertificateAuthorityArn string
	KeyAlgorithm            string
	Options                 struct {
		CertificateTransparencyLoggingPreference string
		Export                                   string
		ValidationMethod                         string
	}
}

func iamPopulateACMRequestConditionKeys(_ *http.Request, operation string, body []byte, ctx map[string][]string) {
	var request acmConditionRequest
	if json.Unmarshal(body, &request) != nil {
		return
	}
	switch operation {
	case "RequestCertificate":
		ecSetString(ctx, "acm:CertificateAuthority", request.CertificateAuthorityArn)
		ecSetString(ctx, "acm:KeyAlgorithm", request.KeyAlgorithm)
		ecSetString(ctx, "acm:Export", request.Options.Export)
		asSetList(ctx, "acm:DomainNames", acmConditionDomainNames(request.DomainName, request.SubjectAlternativeNames))
		ctx["acm:CertificateTransparencyLogging"] = []string{acmConditionDefault(request.Options.CertificateTransparencyLoggingPreference, "ENABLED")}
		ctx["acm:ValidationMethod"] = []string{acmConditionDefault(request.ValidationMethod, "EMAIL")}
	case "UpdateCertificateOptions":
		ctx["acm:ValidationMethod"] = []string{acmConditionDefault(request.Options.ValidationMethod, "EMAIL")}
	case "ExportCertificate", "RevokeCertificate":
		if stored, ok := acmCertificates.Get(acmARNToID(request.CertificateArn)); ok {
			asSetList(ctx, "acm:DomainNames", acmConditionDomainNames(stored.Cert.DomainName, stored.Cert.SubjectAlternativeNames))
		}
	}
}

func acmConditionDefault(value, absent string) string {
	if value == "" {
		return absent
	}
	return value
}

// acmConditionDomainNames lists every name a certificate covers once: its
// subject name and its alternative names, which ACM repeats the subject in.
func acmConditionDomainNames(domain string, alternatives []string) []string {
	seen := map[string]bool{}
	var names []string
	for _, name := range append([]string{domain}, alternatives...) {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}
