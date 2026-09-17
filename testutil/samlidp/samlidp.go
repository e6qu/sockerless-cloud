// Package samlidp is a SAML 2.0 identity provider for tests. It holds a
// signing key and its certificate, publishes the metadata an AWS account
// registers with CreateSAMLProvider, and issues signed responses.
package samlidp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

// AWS reads these attributes and addresses.
const (
	RoleAttribute            = "https://aws.amazon.com/SAML/Attributes/Role"
	RoleSessionNameAttribute = "https://aws.amazon.com/SAML/Attributes/RoleSessionName"
	AWSAudience              = "https://signin.aws.amazon.com/saml"
	PersistentFormat         = "urn:oasis:names:tc:SAML:2.0:nameid-format:persistent"
)

const (
	protocolNS  = "urn:oasis:names:tc:SAML:2.0:protocol"
	assertionNS = "urn:oasis:names:tc:SAML:2.0:assertion"
	metadataNS  = "urn:oasis:names:tc:SAML:2.0:metadata"
	signatureNS = "http://www.w3.org/2000/09/xmldsig#"
)

// IdP is an identity provider with its own signing key.
type IdP struct {
	EntityID string
	key      *rsa.PrivateKey
	certDER  []byte
}

// New creates an identity provider named entityID with a fresh key.
func New(entityID string) (*IdP, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: entityID},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return &IdP{EntityID: entityID, key: key, certDER: der}, nil
}

// Metadata is the provider's SAML metadata document.
func (i *IdP) Metadata() string {
	return fmt.Sprintf(`<md:EntityDescriptor xmlns:md=%q entityID=%q>`+
		`<md:IDPSSODescriptor protocolSupportEnumeration=%q>`+
		`<md:KeyDescriptor use="signing"><ds:KeyInfo xmlns:ds=%q><ds:X509Data><ds:X509Certificate>%s</ds:X509Certificate></ds:X509Data></ds:KeyInfo></md:KeyDescriptor>`+
		`<md:SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="%s/sso"/>`+
		`</md:IDPSSODescriptor></md:EntityDescriptor>`,
		metadataNS, i.EntityID, protocolNS, signatureNS, base64.StdEncoding.EncodeToString(i.certDER), i.EntityID)
}

// Assertion describes a response to issue.
type Assertion struct {
	Subject      string
	Format       string
	Issuer       string // the provider's entity ID when empty
	Recipient    string // AWSAudience when empty
	Audience     string // AWSAudience when empty
	NotBefore    time.Time
	NotOnOrAfter time.Time
	Attributes   map[string][]string
}

// Response issues a base64 SAML response carrying the assertion, signed with
// the provider's key.
func (i *IdP) Response(a Assertion) (string, error) {
	now := time.Now().UTC()
	if a.Issuer == "" {
		a.Issuer = i.EntityID
	}
	if a.Recipient == "" {
		a.Recipient = AWSAudience
	}
	if a.Audience == "" {
		a.Audience = AWSAudience
	}
	if a.Format == "" {
		a.Format = PersistentFormat
	}
	if a.NotBefore.IsZero() {
		a.NotBefore = now.Add(-time.Minute)
	}
	if a.NotOnOrAfter.IsZero() {
		a.NotOnOrAfter = now.Add(5 * time.Minute)
	}
	assertion := etree.NewElement("saml:Assertion")
	assertion.CreateAttr("xmlns:saml", assertionNS)
	assertion.CreateAttr("ID", fmt.Sprintf("_a%d", now.UnixNano()))
	assertion.CreateAttr("Version", "2.0")
	assertion.CreateAttr("IssueInstant", now.Format(time.RFC3339))
	assertion.CreateElement("saml:Issuer").SetText(a.Issuer)
	subject := assertion.CreateElement("saml:Subject")
	nameID := subject.CreateElement("saml:NameID")
	nameID.CreateAttr("Format", a.Format)
	nameID.SetText(a.Subject)
	confirmation := subject.CreateElement("saml:SubjectConfirmation")
	confirmation.CreateAttr("Method", "urn:oasis:names:tc:SAML:2.0:cm:bearer")
	data := confirmation.CreateElement("saml:SubjectConfirmationData")
	data.CreateAttr("Recipient", a.Recipient)
	data.CreateAttr("NotOnOrAfter", a.NotOnOrAfter.Format(time.RFC3339))
	conditions := assertion.CreateElement("saml:Conditions")
	conditions.CreateAttr("NotBefore", a.NotBefore.Format(time.RFC3339))
	conditions.CreateAttr("NotOnOrAfter", a.NotOnOrAfter.Format(time.RFC3339))
	conditions.CreateElement("saml:AudienceRestriction").CreateElement("saml:Audience").SetText(a.Audience)
	statement := assertion.CreateElement("saml:AttributeStatement")
	for name, values := range a.Attributes {
		attribute := statement.CreateElement("saml:Attribute")
		attribute.CreateAttr("Name", name)
		for _, value := range values {
			attribute.CreateElement("saml:AttributeValue").SetText(value)
		}
	}

	signer := dsig.NewDefaultSigningContext(dsig.TLSCertKeyStore(tls.Certificate{
		Certificate: [][]byte{i.certDER},
		PrivateKey:  i.key,
	}))
	signed, err := signer.SignEnveloped(assertion)
	if err != nil {
		return "", err
	}

	response := etree.NewElement("samlp:Response")
	response.CreateAttr("xmlns:samlp", protocolNS)
	response.CreateAttr("ID", fmt.Sprintf("_r%d", now.UnixNano()))
	response.CreateAttr("Version", "2.0")
	response.CreateAttr("IssueInstant", now.Format(time.RFC3339))
	response.CreateAttr("Destination", a.Recipient)
	status := response.CreateElement("samlp:Status").CreateElement("samlp:StatusCode")
	status.CreateAttr("Value", "urn:oasis:names:tc:SAML:2.0:status:Success")
	response.AddChild(signed)

	doc := etree.NewDocument()
	doc.SetRoot(response)
	raw, err := doc.WriteToBytes()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}
