package main

import (
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cacheengine"
)

// Memorystore's in-transit encryption: each instance, cluster or region that
// serves TLS has a certificate authority of its own, which the API reports
// (an instance's serverCaCerts, a cluster's certificateAuthority, a region's
// sharedRegionalCertificateAuthority) and which signs the certificate every
// endpoint presents for the address a client reached it at.

const msRedisCAValidity = 10 * 365 * 24 * time.Hour

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
	msRedisCAs  sim.Store[msRedisCertificateAuthority]
	msRedisCAMu sync.Mutex
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
	authority, err := cacheengine.NewAuthority(pkix.Name{
		Country:      []string{"US"},
		Organization: []string{"Google, Inc"},
		CommonName:   "Google Cloud Memorystore Redis Server CA",
	}, msRedisCAValidity)
	if err != nil {
		return msRedisCertificateAuthority{}, err
	}
	fingerprint := sha1.Sum(authority.DER)
	ca := msRedisCertificateAuthority{
		Name:       owner,
		CertPEM:    authority.CertPEM,
		KeyPEM:     authority.KeyPEM,
		Serial:     authority.Serial.String(),
		CreateTime: authority.NotBefore.Format(time.RFC3339),
		ExpireTime: authority.NotAfter.Format(time.RFC3339),
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
}

// msRedisServerTLSConfig is what an endpoint presents: a certificate the
// owner's CA signs for the address a client reached the resource at, so a
// client verifying the address it dialed against the reported CA accepts it.
func msRedisServerTLSConfig(owner string) (*tls.Config, error) {
	ca, err := msRedisEnsureCA(owner)
	if err != nil {
		return nil, err
	}
	config, err := cacheengine.ServerTLS(ca.CertPEM, ca.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("certificate authority %s: %w", owner, err)
	}
	return config, nil
}
