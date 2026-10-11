package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // Amazon RDS reports a CA certificate's thumbprint as its SHA-1 digest.
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// RDSCertificate is an Amazon RDS certificate authority: a CA certificate and
// its private key, which signs the server certificates of the DB instances
// that name it.
type RDSCertificate struct {
	CertificateIdentifier string
	CertificateType       string
	Thumbprint            string
	ValidFrom             string
	ValidTill             string
	CustomerOverride      bool
	ARN                   string
	CertificateDER        []byte `json:",omitempty"`
	// PrivateKeySecret is the CA's PKCS #8 private key, encrypted under the
	// AWS owned RDS key.
	PrivateKeySecret []byte `json:",omitempty"`
}

// rdsCertificateAuthorities are the CAs DescribeCertificates lists, with the
// key each signs with.
var rdsCertificateAuthorities = []string{"rds-ca-rsa2048-g1", "rds-ca-rsa4096-g1", "rds-ca-ecc384-g1"}

// rdsSystemDefaultCertificate is the CA a new DB instance's server
// certificate is issued by when the account sets no override.
const rdsSystemDefaultCertificate = "rds-ca-rsa2048-g1"

var rdsCertificateSeed sync.Mutex

func rdsGenerateKey(identifier string) (crypto.Signer, error) {
	switch {
	case strings.Contains(identifier, "ecc384"):
		return ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case strings.Contains(identifier, "rsa4096"):
		return rsa.GenerateKey(rand.Reader, 4096)
	default:
		return rsa.GenerateKey(rand.Reader, 2048)
	}
}

func rdsSerialNumber() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

// rdsSeedCertificates creates each CA with its key the first time the account
// needs one, and gives a CA an earlier simulator recorded without a key a key
// and a certificate of its own.
func rdsSeedCertificates() error {
	rdsCertificateSeed.Lock()
	defer rdsCertificateSeed.Unlock()
	for _, id := range rdsCertificateAuthorities {
		existing, ok := rdsCertificates.Get(id)
		if ok && len(existing.CertificateDER) > 0 && len(existing.PrivateKeySecret) > 0 {
			continue
		}
		c, err := rdsNewCertificateAuthority(id)
		if err != nil {
			return fmt.Errorf("create CA certificate %s: %w", id, err)
		}
		c.CustomerOverride = existing.CustomerOverride
		rdsCertificates.Put(id, c)
	}
	return nil
}

func rdsNewCertificateAuthority(id string) (RDSCertificate, error) {
	key, err := rdsGenerateKey(id)
	if err != nil {
		return RDSCertificate{}, err
	}
	serial, err := rdsSerialNumber()
	if err != nil {
		return RDSCertificate{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: id},
		NotBefore:             now.AddDate(-1, 0, 0),
		NotAfter:              now.AddDate(40, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return RDSCertificate{}, err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return RDSCertificate{}, err
	}
	sealed, err := rdsSealMasterPassword(string(pkcs8))
	if err != nil {
		return RDSCertificate{}, err
	}
	thumbprint := sha1.Sum(der) //nolint:gosec // The thumbprint is the certificate's SHA-1 digest.
	return RDSCertificate{
		CertificateIdentifier: id,
		CertificateType:       "CA",
		Thumbprint:            hex.EncodeToString(thumbprint[:]),
		ValidFrom:             template.NotBefore.Format(time.RFC3339),
		ValidTill:             template.NotAfter.Format(time.RFC3339),
		ARN:                   rdsCertificateARN(id),
		CertificateDER:        der,
		PrivateKeySecret:      sealed,
	}, nil
}

// rdsDefaultCertificateForNewLaunches is the CA a new DB instance's server
// certificate is issued by: the account's override, or the system default.
func rdsDefaultCertificateForNewLaunches() string {
	for _, c := range rdsCertificates.List() {
		if c.CustomerOverride {
			return c.CertificateIdentifier
		}
	}
	return rdsSystemDefaultCertificate
}

// rdsCheckCertificate answers whether id names a CA DescribeCertificates
// lists.
func rdsCheckCertificate(id string) bool {
	for _, known := range rdsCertificateAuthorities {
		if known == id {
			return true
		}
	}
	return false
}

// rdsIssueServerCertificate issues the certificate an instance's endpoint
// presents, signed by the CA the instance names, for the endpoint's address.
func rdsIssueServerCertificate(caID, endpoint string) (tls.Certificate, error) {
	if err := rdsSeedCertificates(); err != nil {
		return tls.Certificate{}, err
	}
	ca, ok := rdsCertificates.Get(caID)
	if !ok {
		return tls.Certificate{}, fmt.Errorf("CA certificate %s not found", caID)
	}
	caCert, err := x509.ParseCertificate(ca.CertificateDER)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse CA certificate %s: %w", caID, err)
	}
	_, pkcs8, decrypted := kmsDecryptBytes(ca.PrivateKeySecret)
	if !decrypted {
		return tls.Certificate{}, fmt.Errorf("decrypt the private key of CA certificate %s", caID)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(pkcs8)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse the private key of CA certificate %s: %w", caID, err)
	}
	caKey, ok := parsed.(crypto.Signer)
	if !ok {
		return tls.Certificate{}, fmt.Errorf("CA certificate %s holds a %T key", caID, parsed)
	}
	var key crypto.Signer
	if _, ecc := caKey.(*ecdsa.PrivateKey); ecc {
		key, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	} else {
		key, err = rsa.GenerateKey(rand.Reader, 2048)
	}
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rdsSerialNumber()
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	notAfter := now.AddDate(1, 0, 0)
	if notAfter.After(caCert.NotAfter) {
		notAfter = caCert.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: endpoint},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(endpoint); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{endpoint}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, key.Public(), caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

func renderRDSCertificate(c RDSCertificate) string {
	var b strings.Builder
	b.WriteString("<Certificate>")
	fmt.Fprintf(&b, "<CertificateIdentifier>%s</CertificateIdentifier>", xmlEscape(c.CertificateIdentifier))
	fmt.Fprintf(&b, "<CertificateType>%s</CertificateType>", xmlEscape(c.CertificateType))
	fmt.Fprintf(&b, "<Thumbprint>%s</Thumbprint>", xmlEscape(c.Thumbprint))
	fmt.Fprintf(&b, "<ValidFrom>%s</ValidFrom>", xmlEscape(c.ValidFrom))
	fmt.Fprintf(&b, "<ValidTill>%s</ValidTill>", xmlEscape(c.ValidTill))
	fmt.Fprintf(&b, "<CertificateArn>%s</CertificateArn>", xmlEscape(c.ARN))
	fmt.Fprintf(&b, "<CustomerOverride>%t</CustomerOverride>", c.CustomerOverride)
	b.WriteString("</Certificate>")
	return b.String()
}

func handleRDSDescribeCertificates(w http.ResponseWriter, r *http.Request) {
	if err := rdsSeedCertificates(); err != nil {
		rdsErrorXML(w, "InternalFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
		return
	}
	wanted := r.FormValue("CertificateIdentifier")
	matched := false
	var b strings.Builder
	fmt.Fprintf(&b, "<DefaultCertificateForNewLaunches>%s</DefaultCertificateForNewLaunches>", rdsDefaultCertificateForNewLaunches())
	b.WriteString("<Certificates>")
	for _, c := range rdsCertificates.List() {
		if wanted != "" && c.CertificateIdentifier != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSCertificate(c))
	}
	b.WriteString("</Certificates>")
	if wanted != "" && !matched {
		rdsErrorXML(w, "CertificateNotFound", fmt.Sprintf("Certificate %q not found", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsXMLResponse(w, "DescribeCertificates", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyCertificates(w http.ResponseWriter, r *http.Request) {
	if err := rdsSeedCertificates(); err != nil {
		rdsErrorXML(w, "InternalFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
		return
	}
	id := r.FormValue("CertificateIdentifier")
	if r.FormValue("RemoveCustomerOverride") == "true" {
		for _, c := range rdsCertificates.List() {
			rdsCertificates.Update(c.CertificateIdentifier, func(cert *RDSCertificate) {
				cert.CustomerOverride = false
			})
		}
		if id != "" {
			if c, ok := rdsCertificates.Get(id); ok {
				rdsXMLResponse(w, "ModifyCertificates", renderRDSCertificate(c), sim.RequestID(r.Context()))
				return
			}
		}
		rdsXMLResponse(w, "ModifyCertificates", "", sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsCertificates.Get(id); !ok {
		rdsErrorXML(w, "CertificateNotFound", fmt.Sprintf("Certificate %q not found", id), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	for _, other := range rdsCertificates.List() {
		rdsCertificates.Update(other.CertificateIdentifier, func(cert *RDSCertificate) {
			cert.CustomerOverride = cert.CertificateIdentifier == id
		})
	}
	c, _ := rdsCertificates.Get(id)
	rdsXMLResponse(w, "ModifyCertificates", renderRDSCertificate(c), sim.RequestID(r.Context()))
}
