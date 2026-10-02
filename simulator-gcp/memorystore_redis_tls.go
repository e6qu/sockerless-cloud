package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Memorystore's in-transit encryption: each instance, cluster or region that
// serves TLS has a certificate authority of its own, which the API reports
// (an instance's serverCaCerts, a cluster's certificateAuthority, a region's
// sharedRegionalCertificateAuthority) and which signs the certificate every
// endpoint presents for the address a client reached it at.

const (
	msRedisCAValidity   = 10 * 365 * 24 * time.Hour
	msRedisLeafValidity = 365 * 24 * time.Hour
)

// msRedisCertificateAuthority is a server CA and its private key.
type msRedisCertificateAuthority struct {
	Name       string `json:"name"`
	CertPEM    string `json:"certPem"`
	KeyPEM     string `json:"keyPem"`
	Serial     string `json:"serial"`
	CreateTime string `json:"createTime"`
	ExpireTime string `json:"expireTime"`
	SHA1       string `json:"sha1Fingerprint"`
}

var (
	msRedisCAs   sim.Store[msRedisCertificateAuthority]
	msRedisCAMu  sync.Mutex
	msRedisLeafs sync.Map // CA name -> *msRedisLeafIssuer
)

// MSRedisTLSCertificate mirrors google.cloud.redis.v1.TlsCertificate.
type MSRedisTLSCertificate struct {
	SerialNumber    string `json:"serialNumber,omitempty"`
	Cert            string `json:"cert,omitempty"`
	CreateTime      string `json:"createTime,omitempty"`
	ExpireTime      string `json:"expireTime,omitempty"`
	SHA1Fingerprint string `json:"sha1Fingerprint,omitempty"`
}

// msRedisEnsureCA returns the certificate authority owner names, creating it
// on first use.
func msRedisEnsureCA(owner string) (msRedisCertificateAuthority, error) {
	msRedisCAMu.Lock()
	defer msRedisCAMu.Unlock()
	if ca, ok := msRedisCAs.Get(owner); ok {
		return ca, nil
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return msRedisCertificateAuthority{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
	if err != nil {
		return msRedisCertificateAuthority{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Country:      []string{"US"},
			Organization: []string{"Google, Inc"},
			CommonName:   "Google Cloud Memorystore Redis Server CA",
		},
		NotBefore:             now,
		NotAfter:              now.Add(msRedisCAValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return msRedisCertificateAuthority{}, err
	}
	fingerprint := sha1.Sum(der)
	ca := msRedisCertificateAuthority{
		Name:       owner,
		CertPEM:    string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:     string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
		Serial:     serial.String(),
		CreateTime: now.Format(time.RFC3339),
		ExpireTime: template.NotAfter.Format(time.RFC3339),
		SHA1:       hex.EncodeToString(fingerprint[:]),
	}
	msRedisCAs.Put(owner, ca)
	return ca, nil
}

func (ca msRedisCertificateAuthority) tlsCertificate() MSRedisTLSCertificate {
	return MSRedisTLSCertificate{
		SerialNumber:    ca.Serial,
		Cert:            ca.CertPEM,
		CreateTime:      ca.CreateTime,
		ExpireTime:      ca.ExpireTime,
		SHA1Fingerprint: ca.SHA1,
	}
}

// msRedisReleaseCA forgets a deleted resource's certificate authority.
func msRedisReleaseCA(owner string) {
	msRedisCAs.Delete(owner)
	msRedisLeafs.Delete(owner)
}

// msRedisLeafIssuer signs the certificate an endpoint presents, one per
// address a client reaches the resource at, so a client verifying the
// address it dialed against the reported CA accepts it.
type msRedisLeafIssuer struct {
	ca     *x509.Certificate
	key    *rsa.PrivateKey
	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

func msRedisServerTLSConfig(owner string) (*tls.Config, error) {
	ca, err := msRedisEnsureCA(owner)
	if err != nil {
		return nil, err
	}
	certBlock, _ := pem.Decode([]byte(ca.CertPEM))
	keyBlock, _ := pem.Decode([]byte(ca.KeyPEM))
	if certBlock == nil || keyBlock == nil {
		return nil, fmt.Errorf("certificate authority %s is not PEM", owner)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	issuer := &msRedisLeafIssuer{ca: cert, key: key, leaves: map[string]*tls.Certificate{}}
	msRedisLeafs.Store(owner, issuer)
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: issuer.certificate}, nil
}

func (i *msRedisLeafIssuer) certificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host, _, err := net.SplitHostPort(hello.Conn.LocalAddr().String())
	if err != nil {
		return nil, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if leaf, ok := i.leaves[host]; ok {
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
		NotAfter:     now.Add(msRedisLeafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, i.ca, &key.PublicKey, i.key)
	if err != nil {
		return nil, err
	}
	leaf := &tls.Certificate{Certificate: [][]byte{der, i.ca.Raw}, PrivateKey: key}
	i.leaves[host] = leaf
	return leaf, nil
}
