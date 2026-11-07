package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

// The attested document's signature is a PKCS#7 SignedData laid out as the
// vendored page's example decodes: the document attached, the signing
// certificate inside, and an RSA signature over the document that the
// certificate's key verifies. The certificate names the machine's Region's
// subdomain of metadata.azure.com and metadata.azure.com, the names the page
// tells a verifier to check, and is the same for every document.
func TestIMDSAttestedDocumentIsPKCS7(t *testing.T) {
	srv := vmOpsSimulator(t)
	const address = "192.0.2.43"
	vm := putIMDSMachine(t, "imds-attested-vm", address, func(vm *VirtualMachine) { vm.Location = "westeurope" }, nil)

	read := func(nonce string) (pkcs7SignedData, *x509.Certificate, map[string]any) {
		t.Helper()
		rec := imdsRequest(t, srv, address, http.MethodGet, "/metadata/attested/document?api-version=2025-04-07&nonce="+nonce, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("attested document: %d %s", rec.Code, rec.Body.String())
		}
		body := imdsJSON(t, rec)
		if body["encoding"] != "pkcs7" {
			t.Fatalf("encoding = %v, want pkcs7", body["encoding"])
		}
		der, err := base64.StdEncoding.DecodeString(body["signature"].(string))
		if err != nil {
			t.Fatal(err)
		}
		var info pkcs7ContentInfo
		if _, err := asn1.Unmarshal(der, &info); err != nil || !info.ContentType.Equal(oidPKCS7SignedData) {
			t.Fatalf("content info: %v %v", info.ContentType, err)
		}
		var signed pkcs7SignedData
		if _, err := asn1.Unmarshal(info.Content.Bytes, &signed); err != nil {
			t.Fatalf("signed data: %v", err)
		}
		cert, err := x509.ParseCertificate(signed.Certificates.Bytes)
		if err != nil {
			t.Fatalf("certificate: %v", err)
		}
		var document map[string]any
		if err := json.Unmarshal(signed.Content.Content, &document); err != nil {
			t.Fatalf("document: %v", err)
		}
		return signed, cert, document
	}

	signed, cert, document := read("1234567890")
	if document["nonce"] != "1234567890" || document["vmId"] != vm.Properties.VMID {
		t.Errorf("document = %v, want the nonce and the machine's vmId", document)
	}
	if signed.Version != 1 || len(signed.SignerInfos) != 1 || !signed.DigestAlgorithms[0].Algorithm.Equal(oidSHA256WithRSA) ||
		!signed.SignerInfos[0].SignatureAlgorithm.Algorithm.Equal(oidRSAEncryption) {
		t.Errorf("signed data layout differs from the page's example: %+v", signed)
	}
	if err := cert.VerifyHostname("westeurope.metadata.azure.com"); err != nil {
		t.Error(err)
	}
	if err := cert.VerifyHostname("metadata.azure.com"); err != nil {
		t.Error(err)
	}
	digest := sha256.Sum256(signed.Content.Content)
	if err := rsa.VerifyPKCS1v15(cert.PublicKey.(*rsa.PublicKey), crypto.SHA256, digest[:], signed.SignerInfos[0].Signature); err != nil {
		t.Errorf("the certificate's key does not verify the signature: %v", err)
	}
	if signed.SignerInfos[0].IssuerAndSerial.Serial.Cmp(cert.SerialNumber) != 0 {
		t.Error("the signer is not the certificate the document carries")
	}

	_, again, other := read("9876543210")
	if !again.Equal(cert) {
		t.Error("a second document carries another certificate")
	}
	if other["nonce"] != "9876543210" {
		t.Errorf("nonce = %v", other["nonce"])
	}
}
