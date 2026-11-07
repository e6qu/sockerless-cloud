package main

import (
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// The attested document is a PKCS#7 SignedData (imds-instance-metadata-service,
// "Attested data"), laid out as the page's example decodes: version 1,
// sha256WithRSAEncryption as the digest algorithm, the JSON document attached
// as pkcs7-data, the signing certificate, and one SignerInfo without signed
// attributes whose RSA signature covers the document.
var (
	oidPKCS7Data            = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidPKCS7SignedData      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidRSAEncryption        = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidSHA256WithRSA        = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 11}
	asn1Null                = asn1.RawValue{Tag: asn1.TagNull}
	azureAttestationCertsMu sync.Mutex
	azureAttestationCerts   = map[string][]byte{}
)

type pkcs7ContentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"explicit,tag:0"`
}

type pkcs7EncapsulatedContent struct {
	ContentType asn1.ObjectIdentifier
	Content     []byte `asn1:"explicit,tag:0"`
}

type pkcs7IssuerAndSerial struct {
	Issuer asn1.RawValue
	Serial *big.Int
}

type pkcs7SignerInfo struct {
	Version            int
	IssuerAndSerial    pkcs7IssuerAndSerial
	DigestAlgorithm    pkix.AlgorithmIdentifier
	SignatureAlgorithm pkix.AlgorithmIdentifier
	Signature          []byte
}

type pkcs7SignedData struct {
	Version          int
	DigestAlgorithms []pkix.AlgorithmIdentifier `asn1:"set"`
	Content          pkcs7EncapsulatedContent
	Certificates     asn1.RawValue
	SignerInfos      []pkcs7SignerInfo `asn1:"set"`
}

// azureAttestationCertificate is the certificate a machine's attested
// document carries: one for metadata.azure.com and the machine's Region's
// subdomain, the names the page tells a verifier to check, held by the
// simulator's persisted signing key and issued from fixed fields so a Region
// gets the same certificate every time the simulator starts.
func azureAttestationCertificate(signer crypto.Signer, location string) ([]byte, error) {
	azureAttestationCertsMu.Lock()
	defer azureAttestationCertsMu.Unlock()
	if der, ok := azureAttestationCerts[location]; ok {
		return der, nil
	}
	pub, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append(pub, location...))
	host := location + ".metadata.azure.com"
	template := &x509.Certificate{
		SerialNumber:          new(big.Int).SetBytes(sum[:16]),
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host, "metadata.azure.com"},
		NotBefore:             time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2054, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, signer.Public(), signer)
	if err != nil {
		return nil, err
	}
	azureAttestationCerts[location] = der
	return der, nil
}

// azurePKCS7Sign wraps document in the SignedData the attested document's
// signature field carries.
func azurePKCS7Sign(signer crypto.Signer, location string, document []byte) ([]byte, error) {
	certDER, err := azureAttestationCertificate(signer, location)
	if err != nil {
		return nil, fmt.Errorf("issue the attestation certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(document)
	signature, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return nil, err
	}
	sha256RSA := pkix.AlgorithmIdentifier{Algorithm: oidSHA256WithRSA, Parameters: asn1Null}
	signed, err := asn1.Marshal(pkcs7SignedData{
		Version:          1,
		DigestAlgorithms: []pkix.AlgorithmIdentifier{sha256RSA},
		Content:          pkcs7EncapsulatedContent{ContentType: oidPKCS7Data, Content: document},
		Certificates:     asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: certDER},
		SignerInfos: []pkcs7SignerInfo{{
			Version:            1,
			IssuerAndSerial:    pkcs7IssuerAndSerial{Issuer: asn1.RawValue{FullBytes: cert.RawIssuer}, Serial: cert.SerialNumber},
			DigestAlgorithm:    sha256RSA,
			SignatureAlgorithm: pkix.AlgorithmIdentifier{Algorithm: oidRSAEncryption, Parameters: asn1Null},
			Signature:          signature,
		}},
	})
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(pkcs7ContentInfo{
		ContentType: oidPKCS7SignedData,
		Content:     asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: signed},
	})
}
