package main

import (
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

// AWS accepts a SAML response only for itself, and reads the role and session
// from these attributes.
const (
	samlAWSAudience             = "https://signin.aws.amazon.com/saml"
	samlAWSAudienceURN          = "urn:amazon:webservices"
	samlRoleAttribute           = "https://aws.amazon.com/SAML/Attributes/Role"
	samlRoleSessionNameAttr     = "https://aws.amazon.com/SAML/Attributes/RoleSessionName"
	samlPersistentNameIDFormat  = "urn:oasis:names:tc:SAML:2.0:nameid-format:persistent"
	samlTransientNameIDFormat   = "urn:oasis:names:tc:SAML:2.0:nameid-format:transient"
	samlStatusSuccess           = "urn:oasis:names:tc:SAML:2.0:status:Success"
	errSAMLAssertionUnsupported = "the SAML assertion"
)

// samlAttributeKeys maps each attribute-valued saml: condition key to the
// attribute names an identity provider sends it under: the LDAP and eduPerson
// object identifiers, and the attribute's own name.
var samlAttributeKeys = map[string][]string{
	"saml:cn":                           {"urn:oid:2.5.4.3", "cn"},
	"saml:commonName":                   {"urn:oid:2.5.4.3", "commonName"},
	"saml:givenName":                    {"urn:oid:2.5.4.42", "givenName"},
	"saml:surname":                      {"urn:oid:2.5.4.4", "surname", "sn"},
	"saml:name":                         {"urn:oid:2.5.4.41", "name"},
	"saml:mail":                         {"urn:oid:0.9.2342.19200300.100.1.3", "mail"},
	"saml:uid":                          {"urn:oid:0.9.2342.19200300.100.1.1", "uid"},
	"saml:x500UniqueIdentifier":         {"urn:oid:2.5.4.45", "x500UniqueIdentifier"},
	"saml:organizationStatus":           {"urn:oid:0.9.2342.19200300.100.1.45", "organizationStatus"},
	"saml:primaryGroupSID":              {"http://schemas.microsoft.com/ws/2008/06/identity/claims/primarygroupsid", "primaryGroupSID"},
	"saml:edupersonaffiliation":         {"urn:oid:1.3.6.1.4.1.5923.1.1.1.1", "eduPersonAffiliation"},
	"saml:edupersonnickname":            {"urn:oid:1.3.6.1.4.1.5923.1.1.1.2", "eduPersonNickname"},
	"saml:edupersonorgdn":               {"urn:oid:1.3.6.1.4.1.5923.1.1.1.3", "eduPersonOrgDN"},
	"saml:edupersonorgunitdn":           {"urn:oid:1.3.6.1.4.1.5923.1.1.1.4", "eduPersonOrgUnitDN"},
	"saml:edupersonprimaryaffiliation":  {"urn:oid:1.3.6.1.4.1.5923.1.1.1.5", "eduPersonPrimaryAffiliation"},
	"saml:edupersonprincipalname":       {"urn:oid:1.3.6.1.4.1.5923.1.1.1.6", "eduPersonPrincipalName"},
	"saml:edupersonentitlement":         {"urn:oid:1.3.6.1.4.1.5923.1.1.1.7", "eduPersonEntitlement"},
	"saml:edupersonprimaryorgunitdn":    {"urn:oid:1.3.6.1.4.1.5923.1.1.1.8", "eduPersonPrimaryOrgUnitDN"},
	"saml:edupersonscopedaffiliation":   {"urn:oid:1.3.6.1.4.1.5923.1.1.1.9", "eduPersonScopedAffiliation"},
	"saml:edupersontargetedid":          {"urn:oid:1.3.6.1.4.1.5923.1.1.1.10", "eduPersonTargetedID"},
	"saml:edupersonassurance":           {"urn:oid:1.3.6.1.4.1.5923.1.1.1.11", "eduPersonAssurance"},
	"saml:eduorghomepageuri":            {"urn:oid:1.3.6.1.4.1.5923.1.2.1.2", "eduOrgHomePageURI"},
	"saml:eduorgidentityauthnpolicyuri": {"urn:oid:1.3.6.1.4.1.5923.1.2.1.3", "eduOrgIdentityAuthNPolicyURI"},
	"saml:eduorglegalname":              {"urn:oid:1.3.6.1.4.1.5923.1.2.1.4", "eduOrgLegalName"},
	"saml:eduorgsuperioruri":            {"urn:oid:1.3.6.1.4.1.5923.1.2.1.5", "eduOrgSuperiorURI"},
	"saml:eduorgwhitepagesuri":          {"urn:oid:1.3.6.1.4.1.5923.1.2.1.6", "eduOrgWhitePagesURI"},
}

// samlAssertion is what AWS STS reads from a verified SAML response.
type samlAssertion struct {
	Issuer       string
	Subject      string
	SubjectType  string
	Recipient    string
	Audiences    []string
	SessionName  string
	RolePairs    [][2]string
	Attributes   map[string][]string
	ProviderArn  string
	ProviderName string
}

// errSAMLExpired marks an assertion outside its validity window, which AWS STS
// reports as ExpiredTokenException rather than as an invalid token.
var errSAMLExpired = errors.New("the SAML assertion has expired or is not yet valid")

// verifySAMLAssertion verifies a base64 SAML response against the SAML
// provider's metadata, the way AWS STS does: the signature must be one of the
// provider's signing certificates, the issuer the provider's entity, the
// audience AWS, and the assertion within its validity window. Only the element
// the signature covers is read.
func verifySAMLAssertion(encoded string, provider IAMSAMLProvider, now time.Time) (samlAssertion, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return samlAssertion{}, fmt.Errorf("%s is not base64: %w", errSAMLAssertionUnsupported, err)
	}
	entityID, certs, err := samlProviderSigning(provider.SAMLMetadataDocument)
	if err != nil {
		return samlAssertion{}, err
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil {
		return samlAssertion{}, fmt.Errorf("%s is not XML: %w", errSAMLAssertionUnsupported, err)
	}
	response := doc.Root()
	if response == nil || response.Tag != "Response" {
		return samlAssertion{}, fmt.Errorf("%s is not a SAML response", errSAMLAssertionUnsupported)
	}
	validator := dsig.NewDefaultValidationContext(&dsig.MemoryX509CertificateStore{Roots: certs})
	validator.Clock = dsig.NewFakeClockAt(now)

	var assertion *etree.Element
	if response.FindElement("./Signature") != nil {
		verified, err := validator.Validate(response)
		if err != nil {
			return samlAssertion{}, fmt.Errorf("the SAML response signature is not valid: %w", err)
		}
		response = verified
		assertion = response.FindElement("./Assertion")
	} else if unsigned := response.FindElement("./Assertion"); unsigned != nil {
		verified, err := validator.Validate(unsigned)
		if err != nil {
			return samlAssertion{}, fmt.Errorf("the SAML assertion is not signed by the provider: %w", err)
		}
		assertion = verified
	}
	if assertion == nil {
		return samlAssertion{}, fmt.Errorf("the SAML response carries no assertion")
	}
	if status := response.FindElement("./Status/StatusCode"); status != nil && status.SelectAttrValue("Value", "") != samlStatusSuccess {
		return samlAssertion{}, fmt.Errorf("the SAML response reports status %s", status.SelectAttrValue("Value", ""))
	}

	out := samlAssertion{Attributes: map[string][]string{}}
	out.Issuer = strings.TrimSpace(elementText(assertion.FindElement("./Issuer")))
	if out.Issuer != entityID {
		return samlAssertion{}, fmt.Errorf("the SAML assertion issuer %q is not the provider's entity %q", out.Issuer, entityID)
	}
	nameID := assertion.FindElement("./Subject/NameID")
	if nameID == nil || strings.TrimSpace(nameID.Text()) == "" {
		return samlAssertion{}, fmt.Errorf("the SAML assertion names no subject")
	}
	out.Subject = strings.TrimSpace(nameID.Text())
	switch format := nameID.SelectAttrValue("Format", ""); format {
	case samlPersistentNameIDFormat:
		out.SubjectType = "persistent"
	case samlTransientNameIDFormat:
		out.SubjectType = "transient"
	default:
		out.SubjectType = format
	}
	confirmation := assertion.FindElement("./Subject/SubjectConfirmation/SubjectConfirmationData")
	if confirmation != nil {
		out.Recipient = confirmation.SelectAttrValue("Recipient", "")
		if err := samlWithin(now, "", confirmation.SelectAttrValue("NotOnOrAfter", "")); err != nil {
			return samlAssertion{}, err
		}
	}
	conditions := assertion.FindElement("./Conditions")
	if conditions != nil {
		if err := samlWithin(now, conditions.SelectAttrValue("NotBefore", ""), conditions.SelectAttrValue("NotOnOrAfter", "")); err != nil {
			return samlAssertion{}, err
		}
		for _, audience := range conditions.FindElements("./AudienceRestriction/Audience") {
			out.Audiences = append(out.Audiences, strings.TrimSpace(audience.Text()))
		}
	}
	if !slices.Contains(out.Audiences, samlAWSAudience) && !slices.Contains(out.Audiences, samlAWSAudienceURN) {
		return samlAssertion{}, fmt.Errorf("the SAML assertion is not addressed to AWS (audiences %v)", out.Audiences)
	}
	for _, attribute := range assertion.FindElements("./AttributeStatement/Attribute") {
		var values []string
		for _, value := range attribute.FindElements("./AttributeValue") {
			values = append(values, strings.TrimSpace(value.Text()))
		}
		for _, name := range []string{attribute.SelectAttrValue("Name", ""), attribute.SelectAttrValue("FriendlyName", "")} {
			if name != "" {
				out.Attributes[name] = append(out.Attributes[name], values...)
			}
		}
	}
	for _, pair := range out.Attributes[samlRoleAttribute] {
		first, second, ok := strings.Cut(pair, ",")
		if ok {
			out.RolePairs = append(out.RolePairs, [2]string{strings.TrimSpace(first), strings.TrimSpace(second)})
		}
	}
	if names := out.Attributes[samlRoleSessionNameAttr]; len(names) > 0 {
		out.SessionName = names[0]
	}
	return out, nil
}

// namesRole reports whether the assertion's Role attribute pairs roleArn with
// providerArn, in either order.
func (a samlAssertion) namesRole(roleArn, providerArn string) bool {
	for _, pair := range a.RolePairs {
		if (pair[0] == roleArn && pair[1] == providerArn) || (pair[1] == roleArn && pair[0] == providerArn) {
			return true
		}
	}
	return false
}

// nameQualifier is AWS STS's per-provider qualifier of the subject: a hash of
// the issuer, the account and the provider's friendly name joined by '/'. AWS
// documents the inputs but not the function; its published example is 28
// base64 characters, the length of a SHA-1 digest, which is what this uses.
func (a samlAssertion) nameQualifier(account string) string {
	sum := sha1.Sum([]byte(a.Issuer + "/" + account + "/" + a.ProviderName))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// conditionContext is the request context a role's trust policy is evaluated
// against for this assertion.
func (a samlAssertion) conditionContext(account string) map[string][]string {
	ctx := map[string][]string{
		"aws:FederatedProvider": {a.ProviderArn},
		"saml:iss":              {a.Issuer},
		"saml:sub":              {a.Subject},
		"saml:doc":              {account + "/" + a.ProviderName},
		"saml:namequalifier":    {a.nameQualifier(account)},
		"sts:RoleSessionName":   {a.SessionName},
	}
	if a.SubjectType != "" {
		ctx["saml:sub_type"] = []string{a.SubjectType}
	}
	if a.Recipient != "" {
		ctx["saml:aud"] = []string{a.Recipient}
	}
	for key, names := range samlAttributeKeys {
		for _, name := range names {
			if values := a.Attributes[name]; len(values) > 0 {
				ctx[key] = append(ctx[key], values...)
			}
		}
	}
	return ctx
}

func samlWithin(now time.Time, notBefore, notOnOrAfter string) error {
	if notBefore != "" {
		if at, err := time.Parse(time.RFC3339, notBefore); err != nil || now.Before(at) {
			return errSAMLExpired
		}
	}
	if notOnOrAfter != "" {
		if at, err := time.Parse(time.RFC3339, notOnOrAfter); err != nil || !now.Before(at) {
			return errSAMLExpired
		}
	}
	return nil
}

// samlProviderSigning reads a SAML provider's entity ID and signing
// certificates from its metadata document.
func samlProviderSigning(metadata string) (string, []*x509.Certificate, error) {
	doc := etree.NewDocument()
	if err := doc.ReadFromString(metadata); err != nil {
		return "", nil, fmt.Errorf("the SAML provider's metadata is not XML: %w", err)
	}
	root := doc.Root()
	if root == nil {
		return "", nil, fmt.Errorf("the SAML provider's metadata is empty")
	}
	var certs []*x509.Certificate
	for _, descriptor := range root.FindElements(".//IDPSSODescriptor/KeyDescriptor") {
		if use := descriptor.SelectAttrValue("use", "signing"); use != "signing" {
			continue
		}
		for _, element := range descriptor.FindElements(".//X509Certificate") {
			der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(element.Text()), ""))
			if err != nil {
				continue
			}
			if cert, err := x509.ParseCertificate(der); err == nil {
				certs = append(certs, cert)
			}
		}
	}
	if len(certs) == 0 {
		return "", nil, fmt.Errorf("the SAML provider's metadata has no signing certificate")
	}
	return root.SelectAttrValue("entityID", ""), certs, nil
}

func elementText(e *etree.Element) string {
	if e == nil {
		return ""
	}
	return e.Text()
}
