// Package loopbacktls issues the certificate a test serves a TLS listener on
// the loopback interface with: a certificate authority of its own, and a leaf
// it signs for 127.0.0.1, ::1 and localhost. A client trusts the authority the
// way it trusts any private one, through its root pool or SSL_CERT_FILE.
package loopbacktls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Files names the PEM files Write produces.
type Files struct {
	// CA is the authority's certificate, the root a client trusts.
	CA string
	// Cert and Key are the leaf the listener serves.
	Cert string
	Key  string
}

// Pool returns a root pool holding the authority.
func (f Files) Pool() (*x509.CertPool, error) {
	raw, err := os.ReadFile(f.CA)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("%s holds no PEM certificate", f.CA)
	}
	return pool, nil
}

// Write issues a fresh authority and leaf into dir as ca.pem, cert.pem and
// key.pem.
func Write(dir string) (Files, error) {
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Files{}, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "loopbacktls test authority"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(7 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return Files{}, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return Files{}, err
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Files{}, err
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(7 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		return Files{}, err
	}
	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return Files{}, err
	}

	files := Files{CA: filepath.Join(dir, "ca.pem"), Cert: filepath.Join(dir, "cert.pem"), Key: filepath.Join(dir, "key.pem")}
	for path, block := range map[string]*pem.Block{
		files.CA:   {Type: "CERTIFICATE", Bytes: caDER},
		files.Cert: {Type: "CERTIFICATE", Bytes: leafDER},
		files.Key:  {Type: "PRIVATE KEY", Bytes: leafKeyDER},
	} {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
			return Files{}, err
		}
	}
	return files, nil
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(fmt.Sprintf("draw a certificate serial number: %v", err))
	}
	return n
}
