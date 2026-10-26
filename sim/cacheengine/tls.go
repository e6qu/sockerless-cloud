package cacheengine

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"
)

const leafValidity = 365 * 24 * time.Hour

// Authority is a server certificate authority, PEM-encoded.
type Authority struct {
	CertPEM string
	KeyPEM  string
	// DER is the certificate, and Serial, NotBefore and NotAfter its fields.
	DER       []byte
	Serial    *big.Int
	NotBefore time.Time
	NotAfter  time.Time
}

// NewAuthority creates a certificate authority for subject, valid from now
// for validity.
func NewAuthority(subject pkix.Name, validity time.Duration) (Authority, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return Authority{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
	if err != nil {
		return Authority{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               subject,
		NotBefore:             now,
		NotAfter:              now.Add(validity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return Authority{}, err
	}
	return Authority{
		CertPEM:   string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:    string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
		DER:       der,
		Serial:    serial,
		NotBefore: template.NotBefore,
		NotAfter:  template.NotAfter,
	}, nil
}

// ServerTLS is the TLS configuration an endpoint serves with: a certificate
// the authority signs for the address a client reached the endpoint at, and
// for the name it asked for, so a client verifying what it dialed against the
// authority accepts it.
func ServerTLS(caCertPEM, caKeyPEM string) (*tls.Config, error) {
	certBlock, _ := pem.Decode([]byte(caCertPEM))
	keyBlock, _ := pem.Decode([]byte(caKeyPEM))
	if certBlock == nil || keyBlock == nil {
		return nil, fmt.Errorf("the certificate authority is not PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	issuer := &leafIssuer{ca: cert, key: key, leaves: map[[2]string]*tls.Certificate{}}
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: issuer.certificate}, nil
}

type leafIssuer struct {
	ca     *x509.Certificate
	key    *rsa.PrivateKey
	mu     sync.Mutex
	leaves map[[2]string]*tls.Certificate
}

func (i *leafIssuer) certificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host, _, err := net.SplitHostPort(hello.Conn.LocalAddr().String())
	if err != nil {
		return nil, err
	}
	name := [2]string{host, hello.ServerName}
	i.mu.Lock()
	defer i.mu.Unlock()
	if leaf, ok := i.leaves[name]; ok {
		return leaf, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	if hello.ServerName != "" && hello.ServerName != host {
		template.Subject.CommonName = hello.ServerName
		template.DNSNames = append(template.DNSNames, hello.ServerName)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, i.ca, &key.PublicKey, i.key)
	if err != nil {
		return nil, err
	}
	leaf := &tls.Certificate{Certificate: [][]byte{der, i.ca.Raw}, PrivateKey: key}
	i.leaves[name] = leaf
	return leaf, nil
}
