package main

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/csv"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

type privateCASubject struct {
	Country            string `json:"Country,omitempty"`
	Organization       string `json:"Organization,omitempty"`
	OrganizationalUnit string `json:"OrganizationalUnit,omitempty"`
	State              string `json:"State,omitempty"`
	CommonName         string `json:"CommonName,omitempty"`
	SerialNumber       string `json:"SerialNumber,omitempty"`
	Locality           string `json:"Locality,omitempty"`
}

type privateCAConfiguration struct {
	KeyAlgorithm     string           `json:"KeyAlgorithm"`
	SigningAlgorithm string           `json:"SigningAlgorithm"`
	Subject          privateCASubject `json:"Subject"`
}

type privateCA struct {
	ARN                               string                 `json:"Arn"`
	OwnerAccount                      string                 `json:"OwnerAccount"`
	CreatedAt                         float64                `json:"CreatedAt"`
	LastStateChangeAt                 float64                `json:"LastStateChangeAt"`
	Type                              string                 `json:"Type"`
	Serial                            string                 `json:"Serial,omitempty"`
	Status                            string                 `json:"Status"`
	NotBefore                         float64                `json:"NotBefore,omitempty"`
	NotAfter                          float64                `json:"NotAfter,omitempty"`
	CertificateAuthorityConfiguration privateCAConfiguration `json:"CertificateAuthorityConfiguration"`
	RevocationConfiguration           json.RawMessage        `json:"RevocationConfiguration,omitempty"`
	RestorableUntil                   float64                `json:"RestorableUntil,omitempty"`
	KeyStorageSecurityStandard        string                 `json:"KeyStorageSecurityStandard"`
	UsageMode                         string                 `json:"UsageMode"`
	PrivateKeyPEM                     string                 `json:"-"`
	CSRPEM                            string                 `json:"-"`
	CertificatePEM                    string                 `json:"-"`
	CertificateChainPEM               string                 `json:"-"`
	PreDeletionStatus                 string                 `json:"-"`
	Tags                              map[string]string      `json:"-"`
}

type privateCAIssuedCertificate struct {
	ARN         string  `json:"certificateArn"`
	CAARN       string  `json:"certificateAuthorityArn"`
	Certificate string  `json:"certificate"`
	Chain       string  `json:"certificateChain"`
	Serial      string  `json:"serial"`
	Subject     string  `json:"subject"`
	IssuedAt    float64 `json:"issuedAt"`
	NotBefore   float64 `json:"notBefore"`
	NotAfter    float64 `json:"notAfter"`
	Status      string  `json:"status"`
	RevokedAt   float64 `json:"revokedAt,omitempty"`
	Reason      string  `json:"revocationReason,omitempty"`
}

type privateCAPermission struct {
	CertificateAuthorityArn string   `json:"CertificateAuthorityArn"`
	CreatedAt               float64  `json:"CreatedAt"`
	Principal               string   `json:"Principal"`
	SourceAccount           string   `json:"SourceAccount,omitempty"`
	Actions                 []string `json:"Actions"`
	Policy                  string   `json:"Policy,omitempty"`
}

type privateCAAuditReport struct {
	ID        string  `json:"-"`
	CAARN     string  `json:"-"`
	Status    string  `json:"AuditReportStatus"`
	Bucket    string  `json:"S3BucketName"`
	Key       string  `json:"S3Key"`
	CreatedAt float64 `json:"CreatedAt"`
}

type privateCAValidity struct {
	Value int64  `json:"Value"`
	Type  string `json:"Type"`
}

var (
	privateCAs            sim.Store[privateCA]
	privateCACertificates sim.Store[privateCAIssuedCertificate]
	privateCAPermissions  sim.Store[privateCAPermission]
	privateCAPolicies     sim.Store[string]
	privateCAAudits       sim.Store[privateCAAuditReport]
	privateCAIdempotency  sim.Store[string]
)

func registerACMPrivateCA(r *AWSRouter, srv *sim.Server) {
	privateCAs = sim.MakeStore[privateCA](srv.DB(), "acmpca_certificate_authorities")
	privateCACertificates = sim.MakeStore[privateCAIssuedCertificate](srv.DB(), "acmpca_certificates")
	privateCAPermissions = sim.MakeStore[privateCAPermission](srv.DB(), "acmpca_permissions")
	privateCAPolicies = sim.MakeStore[string](srv.DB(), "acmpca_policies")
	privateCAAudits = sim.MakeStore[privateCAAuditReport](srv.DB(), "acmpca_audit_reports")
	privateCAIdempotency = sim.MakeStore[string](srv.DB(), "acmpca_idempotency")

	r.Register("ACMPrivateCA.CreateCertificateAuthority", handlePrivateCACreate)
	r.Register("ACMPrivateCA.CreateCertificateAuthorityAuditReport", handlePrivateCACreateAudit)
	r.Register("ACMPrivateCA.CreatePermission", handlePrivateCACreatePermission)
	r.Register("ACMPrivateCA.DeleteCertificateAuthority", handlePrivateCADelete)
	r.Register("ACMPrivateCA.DeletePermission", handlePrivateCADeletePermission)
	r.Register("ACMPrivateCA.DeletePolicy", handlePrivateCADeletePolicy)
	r.Register("ACMPrivateCA.DescribeCertificateAuthority", handlePrivateCADescribe)
	r.Register("ACMPrivateCA.DescribeCertificateAuthorityAuditReport", handlePrivateCADescribeAudit)
	r.Register("ACMPrivateCA.GetCertificate", handlePrivateCAGetCertificate)
	r.Register("ACMPrivateCA.GetCertificateAuthorityCertificate", handlePrivateCAGetAuthorityCertificate)
	r.Register("ACMPrivateCA.GetCertificateAuthorityCsr", handlePrivateCAGetCSR)
	r.Register("ACMPrivateCA.GetPolicy", handlePrivateCAGetPolicy)
	r.Register("ACMPrivateCA.ImportCertificateAuthorityCertificate", handlePrivateCAImport)
	r.Register("ACMPrivateCA.IssueCertificate", handlePrivateCAIssue)
	r.Register("ACMPrivateCA.ListCertificateAuthorities", handlePrivateCAList)
	r.Register("ACMPrivateCA.ListPermissions", handlePrivateCAListPermissions)
	r.Register("ACMPrivateCA.ListTags", handlePrivateCAListTags)
	r.Register("ACMPrivateCA.PutPolicy", handlePrivateCAPutPolicy)
	r.Register("ACMPrivateCA.RestoreCertificateAuthority", handlePrivateCARestore)
	r.Register("ACMPrivateCA.RevokeCertificate", handlePrivateCARevoke)
	r.Register("ACMPrivateCA.TagCertificateAuthority", handlePrivateCATag)
	r.Register("ACMPrivateCA.UntagCertificateAuthority", handlePrivateCAUntag)
	r.Register("ACMPrivateCA.UpdateCertificateAuthority", handlePrivateCAUpdate)
}

func privateCAError(w http.ResponseWriter, code, message string) {
	AWSError(w, code, message, http.StatusBadRequest)
}

func privateCAARN(id string) string {
	return "arn:aws:acm-pca:" + awsRegion() + ":" + awsAccountID() + ":certificate-authority/" + id
}

func privateCAID(arn string) string {
	const marker = ":certificate-authority/"
	index := strings.Index(arn, marker)
	if index < 0 {
		return ""
	}
	return arn[index+len(marker):]
}

func privateCAGet(w http.ResponseWriter, arn string) (privateCA, bool) {
	ca, ok := privateCAs.Get(privateCAID(arn))
	if !ok {
		privateCAError(w, "ResourceNotFoundException", "The certificate authority "+arn+" was not found.")
	}
	return ca, ok
}

func privateCANewSigner(algorithm string) (crypto.Signer, error) {
	switch algorithm {
	case "RSA_2048":
		return rsa.GenerateKey(rand.Reader, 2048)
	case "RSA_3072":
		return rsa.GenerateKey(rand.Reader, 3072)
	case "RSA_4096":
		return rsa.GenerateKey(rand.Reader, 4096)
	case "EC_prime256v1":
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "EC_secp384r1":
		return ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case "EC_secp521r1":
		return ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	default:
		return nil, fmt.Errorf("unsupported key algorithm %q", algorithm)
	}
}

func privateCASignatureAlgorithm(value string) (x509.SignatureAlgorithm, error) {
	switch value {
	case "SHA256WITHRSA":
		return x509.SHA256WithRSA, nil
	case "SHA384WITHRSA":
		return x509.SHA384WithRSA, nil
	case "SHA512WITHRSA":
		return x509.SHA512WithRSA, nil
	case "SHA256WITHECDSA":
		return x509.ECDSAWithSHA256, nil
	case "SHA384WITHECDSA":
		return x509.ECDSAWithSHA384, nil
	case "SHA512WITHECDSA":
		return x509.ECDSAWithSHA512, nil
	default:
		return x509.UnknownSignatureAlgorithm, fmt.Errorf("unsupported signing algorithm %q", value)
	}
}

func privateCAPKIXName(subject privateCASubject) pkix.Name {
	name := pkix.Name{
		CommonName: subject.CommonName, SerialNumber: subject.SerialNumber,
		Locality: []string{subject.Locality}, Province: []string{subject.State},
		Country: []string{subject.Country}, Organization: []string{subject.Organization},
		OrganizationalUnit: []string{subject.OrganizationalUnit},
	}
	name.Country = compactStrings(name.Country)
	name.Locality = compactStrings(name.Locality)
	name.Province = compactStrings(name.Province)
	name.Organization = compactStrings(name.Organization)
	name.OrganizationalUnit = compactStrings(name.OrganizationalUnit)
	return name
}

func compactStrings(values []string) []string {
	var out []string
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func privateCASigner(ca privateCA) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(ca.PrivateKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("the certificate authority private key is unavailable")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("the certificate authority key is not a signer")
	}
	return signer, nil
}

func privateCAParseCertificate(pemValue string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(pemValue))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("certificate must be PEM encoded")
	}
	return x509.ParseCertificate(block.Bytes)
}

func handlePrivateCACreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityConfiguration privateCAConfiguration `json:"CertificateAuthorityConfiguration"`
		RevocationConfiguration           json.RawMessage        `json:"RevocationConfiguration"`
		CertificateAuthorityType          string                 `json:"CertificateAuthorityType"`
		IdempotencyToken                  string                 `json:"IdempotencyToken"`
		KeyStorageSecurityStandard        string                 `json:"KeyStorageSecurityStandard"`
		UsageMode                         string                 `json:"UsageMode"`
		Tags                              []firehoseTag          `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	if req.IdempotencyToken != "" {
		if arn, ok := privateCAIdempotency.Get(req.IdempotencyToken); ok {
			sim.WriteJSON(w, http.StatusOK, map[string]any{"CertificateAuthorityArn": arn})
			return
		}
	}
	if req.CertificateAuthorityType != "ROOT" && req.CertificateAuthorityType != "SUBORDINATE" {
		privateCAError(w, "InvalidArgsException", "CertificateAuthorityType must be ROOT or SUBORDINATE.")
		return
	}
	signer, err := privateCANewSigner(req.CertificateAuthorityConfiguration.KeyAlgorithm)
	if err != nil {
		privateCAError(w, "InvalidArgsException", err.Error())
		return
	}
	signatureAlgorithm, err := privateCASignatureAlgorithm(req.CertificateAuthorityConfiguration.SigningAlgorithm)
	if err != nil {
		privateCAError(w, "InvalidArgsException", err.Error())
		return
	}
	if _, rsaKey := signer.(*rsa.PrivateKey); rsaKey != strings.Contains(req.CertificateAuthorityConfiguration.SigningAlgorithm, "RSA") {
		privateCAError(w, "InvalidArgsException", "The signing algorithm must match the certificate authority key family.")
		return
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:            privateCAPKIXName(req.CertificateAuthorityConfiguration.Subject),
		SignatureAlgorithm: signatureAlgorithm,
	}, signer)
	if err != nil {
		privateCAError(w, "RequestFailedException", err.Error())
		return
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		privateCAError(w, "RequestFailedException", err.Error())
		return
	}
	id := generateUUID()
	now := float64(time.Now().UTC().UnixMilli()) / 1000
	standard := req.KeyStorageSecurityStandard
	if standard == "" {
		standard = "FIPS_140_2_LEVEL_3_OR_HIGHER"
	}
	usage := req.UsageMode
	if usage == "" {
		usage = "GENERAL_PURPOSE"
	}
	ca := privateCA{
		ARN: privateCAARN(id), OwnerAccount: awsAccountID(), CreatedAt: now, LastStateChangeAt: now,
		Type: req.CertificateAuthorityType, Status: "PENDING_CERTIFICATE",
		CertificateAuthorityConfiguration: req.CertificateAuthorityConfiguration,
		RevocationConfiguration:           req.RevocationConfiguration,
		KeyStorageSecurityStandard:        standard, UsageMode: usage,
		PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
		CSRPEM:        string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})),
		Tags:          map[string]string{},
	}
	for _, tag := range req.Tags {
		ca.Tags[tag.Key] = tag.Value
	}
	privateCAs.Put(id, ca)
	if req.IdempotencyToken != "" {
		privateCAIdempotency.Put(req.IdempotencyToken, ca.ARN)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"CertificateAuthorityArn": ca.ARN})
}

func handlePrivateCAGetCSR(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn string `json:"CertificateAuthorityArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	ca, ok := privateCAGet(w, req.CertificateAuthorityArn)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Csr": ca.CSRPEM})
}

func privateCAValidityTime(validity privateCAValidity, now time.Time) (time.Time, error) {
	switch validity.Type {
	case "ABSOLUTE":
		return time.Unix(validity.Value, 0).UTC(), nil
	case "DAYS":
		return now.AddDate(0, 0, int(validity.Value)), nil
	case "MONTHS":
		return now.AddDate(0, int(validity.Value), 0), nil
	case "YEARS":
		return now.AddDate(int(validity.Value), 0, 0), nil
	case "END_DATE":
		value := strconv.FormatInt(validity.Value, 10)
		layout := "060102150405"
		if len(value) == 14 {
			layout = "20060102150405"
		}
		return time.ParseInLocation(layout, value, time.UTC)
	default:
		return time.Time{}, fmt.Errorf("unsupported validity type %q", validity.Type)
	}
}

func privateCAIssueCertificate(ca privateCA, csrPEM []byte, signingAlgorithm, templateARN string, validity, notBefore *privateCAValidity) (privateCAIssuedCertificate, error) {
	if ca.Status != "ACTIVE" && (ca.Status != "PENDING_CERTIFICATE" || !strings.Contains(templateARN, "RootCACertificate")) {
		return privateCAIssuedCertificate{}, fmt.Errorf("certificate authority is not in a state that can issue this certificate")
	}
	block, rest := pem.Decode(csrPEM)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return privateCAIssuedCertificate{}, fmt.Errorf("CSR must contain exactly one PEM-encoded certificate request")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return privateCAIssuedCertificate{}, fmt.Errorf("CSR signature is invalid")
	}
	signer, err := privateCASigner(ca)
	if err != nil {
		return privateCAIssuedCertificate{}, err
	}
	signature, err := privateCASignatureAlgorithm(signingAlgorithm)
	if err != nil {
		return privateCAIssuedCertificate{}, err
	}
	now := time.Now().UTC()
	start := now.Add(-time.Hour)
	if notBefore != nil {
		start, err = privateCAValidityTime(*notBefore, now)
		if err != nil {
			return privateCAIssuedCertificate{}, err
		}
	}
	if validity == nil {
		return privateCAIssuedCertificate{}, fmt.Errorf("validity is required")
	}
	end, err := privateCAValidityTime(*validity, now)
	if err != nil {
		return privateCAIssuedCertificate{}, err
	}
	if ca.UsageMode == "SHORT_LIVED_CERTIFICATE" && end.After(now.Add(7*24*time.Hour)) {
		return privateCAIssuedCertificate{}, fmt.Errorf("a short-lived certificate cannot be valid for more than seven days")
	}
	if end.Before(start) {
		return privateCAIssuedCertificate{}, fmt.Errorf("certificate validity ends before it starts")
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return privateCAIssuedCertificate{}, err
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	isCA := strings.Contains(templateARN, "CACertificate")
	template := &x509.Certificate{
		SerialNumber: serial, Subject: csr.Subject, NotBefore: start, NotAfter: end,
		SignatureAlgorithm: signature, DNSNames: csr.DNSNames, IPAddresses: csr.IPAddresses,
		EmailAddresses: csr.EmailAddresses, URIs: csr.URIs, IsCA: isCA, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	if isCA {
		template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature
		template.ExtKeyUsage = nil
	}
	parent := template
	chain := ""
	if ca.Status == "ACTIVE" {
		parent, err = privateCAParseCertificate(ca.CertificatePEM)
		if err != nil {
			return privateCAIssuedCertificate{}, err
		}
		chain = ca.CertificatePEM + ca.CertificateChainPEM
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, csr.PublicKey, signer)
	if err != nil {
		return privateCAIssuedCertificate{}, err
	}
	certificate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	certificateARN := ca.ARN + "/certificate/" + serial.String()
	return privateCAIssuedCertificate{
		ARN: certificateARN, CAARN: ca.ARN, Certificate: certificate, Chain: chain,
		Serial: serial.String(), Subject: csr.Subject.String(),
		IssuedAt: float64(now.UnixMilli()) / 1000, NotBefore: float64(start.UnixMilli()) / 1000,
		NotAfter: float64(end.UnixMilli()) / 1000, Status: "ISSUED",
	}, nil
}

func privateCAIssueManagedCertificate(caARN, commonName string, dnsNames []string, keyAlgorithm string) (privateCAIssuedCertificate, string, *x509.Certificate, error) {
	ca, ok := privateCAs.Get(privateCAID(caARN))
	if !ok {
		return privateCAIssuedCertificate{}, "", nil, fmt.Errorf("the certificate authority %s does not exist", caARN)
	}
	if ca.Status != "ACTIVE" {
		return privateCAIssuedCertificate{}, "", nil, fmt.Errorf("the certificate authority %s is not ACTIVE", caARN)
	}
	if keyAlgorithm == "" {
		keyAlgorithm = "RSA_2048"
	}
	signer, err := privateCANewSigner(keyAlgorithm)
	if err != nil {
		return privateCAIssuedCertificate{}, "", nil, err
	}
	signature := x509.SHA256WithRSA
	if _, ok := signer.(*ecdsa.PrivateKey); ok {
		signature = x509.ECDSAWithSHA256
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: commonName}, DNSNames: dnsNames, SignatureAlgorithm: signature,
	}, signer)
	if err != nil {
		return privateCAIssuedCertificate{}, "", nil, err
	}
	issued, err := privateCAIssueCertificate(ca,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}),
		ca.CertificateAuthorityConfiguration.SigningAlgorithm,
		"arn:aws:acm-pca:::template/EndEntityCertificate/V1",
		&privateCAValidity{Value: 395, Type: "DAYS"}, nil)
	if err != nil {
		return privateCAIssuedCertificate{}, "", nil, err
	}
	privateCACertificates.Put(issued.ARN, issued)
	keyDER, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		return privateCAIssuedCertificate{}, "", nil, err
	}
	leaf, err := privateCAParseCertificate(issued.Certificate)
	if err != nil {
		return privateCAIssuedCertificate{}, "", nil, err
	}
	return issued, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), leaf, nil
}

func privateCARevokeIssuedCertificate(certificateARN, reason string) error {
	certificate, ok := privateCACertificates.Get(certificateARN)
	if !ok {
		return fmt.Errorf("the AWS Private CA certificate %s was not found", certificateARN)
	}
	if certificate.Status == "REVOKED" {
		return fmt.Errorf("the AWS Private CA certificate %s is already revoked", certificateARN)
	}
	certificate.Status = "REVOKED"
	certificate.Reason = reason
	certificate.RevokedAt = float64(time.Now().UTC().UnixMilli()) / 1000
	privateCACertificates.Put(certificate.ARN, certificate)
	if ca, ok := privateCAs.Get(privateCAID(certificate.CAARN)); ok {
		privateCAWriteCRL(ca)
	}
	return nil
}

func handlePrivateCAIssue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn string             `json:"CertificateAuthorityArn"`
		Csr                     []byte             `json:"Csr"`
		SigningAlgorithm        string             `json:"SigningAlgorithm"`
		TemplateArn             string             `json:"TemplateArn"`
		Validity                *privateCAValidity `json:"Validity"`
		ValidityNotBefore       *privateCAValidity `json:"ValidityNotBefore"`
		IdempotencyToken        string             `json:"IdempotencyToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	ca, ok := privateCAGet(w, req.CertificateAuthorityArn)
	if !ok {
		return
	}
	if req.TemplateArn == "" {
		req.TemplateArn = "arn:aws:acm-pca:::template/EndEntityCertificate/V1"
	}
	idempotencyKey := ca.ARN + "/" + req.IdempotencyToken
	if req.IdempotencyToken != "" {
		if arn, ok := privateCAIdempotency.Get(idempotencyKey); ok {
			sim.WriteJSON(w, http.StatusOK, map[string]any{"CertificateArn": arn})
			return
		}
	}
	issued, err := privateCAIssueCertificate(ca, req.Csr, req.SigningAlgorithm, req.TemplateArn, req.Validity, req.ValidityNotBefore)
	if err != nil {
		privateCAError(w, "InvalidArgsException", err.Error())
		return
	}
	privateCACertificates.Put(issued.ARN, issued)
	if req.IdempotencyToken != "" {
		privateCAIdempotency.Put(idempotencyKey, issued.ARN)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"CertificateArn": issued.ARN})
}

func handlePrivateCAGetCertificate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn string `json:"CertificateAuthorityArn"`
		CertificateArn          string `json:"CertificateArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	certificate, ok := privateCACertificates.Get(req.CertificateArn)
	if !ok || certificate.CAARN != req.CertificateAuthorityArn {
		privateCAError(w, "ResourceNotFoundException", "The certificate was not found.")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Certificate": certificate.Certificate, "CertificateChain": certificate.Chain,
	})
}

func handlePrivateCAImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn string `json:"CertificateAuthorityArn"`
		Certificate             []byte `json:"Certificate"`
		CertificateChain        []byte `json:"CertificateChain"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	ca, ok := privateCAGet(w, req.CertificateAuthorityArn)
	if !ok {
		return
	}
	if ca.Status != "PENDING_CERTIFICATE" {
		privateCAError(w, "InvalidStateException", "The certificate authority is not waiting for a certificate.")
		return
	}
	certificate, err := privateCAParseCertificate(string(req.Certificate))
	if err != nil || !certificate.IsCA {
		privateCAError(w, "MalformedCertificateException", "The imported certificate is not a valid CA certificate.")
		return
	}
	signer, err := privateCASigner(ca)
	if err != nil || !publicKeysEqual(certificate.PublicKey, signer.Public()) {
		privateCAError(w, "CertificateMismatchException", "The imported certificate does not match the certificate authority key.")
		return
	}
	ca.CertificatePEM = string(req.Certificate)
	ca.CertificateChainPEM = string(req.CertificateChain)
	ca.Status = "ACTIVE"
	ca.Serial = certificate.SerialNumber.String()
	ca.NotBefore = float64(certificate.NotBefore.UnixMilli()) / 1000
	ca.NotAfter = float64(certificate.NotAfter.UnixMilli()) / 1000
	ca.LastStateChangeAt = float64(time.Now().UTC().UnixMilli()) / 1000
	privateCAs.Put(privateCAID(ca.ARN), ca)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func publicKeysEqual(left, right any) bool {
	leftDER, err := x509.MarshalPKIXPublicKey(left)
	if err != nil {
		return false
	}
	rightDER, err := x509.MarshalPKIXPublicKey(right)
	return err == nil && bytes.Equal(leftDER, rightDER)
}

func handlePrivateCAGetAuthorityCertificate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn string `json:"CertificateAuthorityArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	ca, ok := privateCAGet(w, req.CertificateAuthorityArn)
	if !ok {
		return
	}
	if ca.CertificatePEM == "" {
		privateCAError(w, "InvalidStateException", "The certificate authority has no imported certificate.")
		return
	}
	out := map[string]any{"Certificate": ca.CertificatePEM}
	if ca.CertificateChainPEM != "" {
		out["CertificateChain"] = ca.CertificateChainPEM
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func privateCAResponse(ca privateCA) map[string]any {
	out := map[string]any{
		"Arn": ca.ARN, "OwnerAccount": ca.OwnerAccount, "CreatedAt": ca.CreatedAt,
		"LastStateChangeAt": ca.LastStateChangeAt, "Type": ca.Type, "Status": ca.Status,
		"CertificateAuthorityConfiguration": ca.CertificateAuthorityConfiguration,
		"KeyStorageSecurityStandard":        ca.KeyStorageSecurityStandard, "UsageMode": ca.UsageMode,
	}
	if ca.Serial != "" {
		out["Serial"] = ca.Serial
		out["NotBefore"] = ca.NotBefore
		out["NotAfter"] = ca.NotAfter
	}
	if len(ca.RevocationConfiguration) > 0 {
		var value any
		if json.Unmarshal(ca.RevocationConfiguration, &value) == nil {
			out["RevocationConfiguration"] = value
		}
	}
	if ca.RestorableUntil > 0 {
		out["RestorableUntil"] = ca.RestorableUntil
	}
	return out
}

func handlePrivateCADescribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn string `json:"CertificateAuthorityArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	ca, ok := privateCAGet(w, req.CertificateAuthorityArn)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"CertificateAuthority": privateCAResponse(ca)})
}

func handlePrivateCAList(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	cas := privateCAs.List()
	sort.Slice(cas, func(i, j int) bool { return cas[i].ARN < cas[j].ARN })
	start, _ := strconv.Atoi(req.NextToken)
	if start < 0 || start > len(cas) {
		start = 0
	}
	limit := req.MaxResults
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	end := min(start+limit, len(cas))
	items := make([]map[string]any, 0, end-start)
	for _, ca := range cas[start:end] {
		items = append(items, privateCAResponse(ca))
	}
	out := map[string]any{"CertificateAuthorities": items}
	if end < len(cas) {
		out["NextToken"] = strconv.Itoa(end)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handlePrivateCAUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn string          `json:"CertificateAuthorityArn"`
		RevocationConfiguration json.RawMessage `json:"RevocationConfiguration"`
		Status                  string          `json:"Status"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	ca, ok := privateCAGet(w, req.CertificateAuthorityArn)
	if !ok {
		return
	}
	if ca.Status != "ACTIVE" && ca.Status != "DISABLED" {
		privateCAError(w, "InvalidStateException", "Only an ACTIVE or DISABLED certificate authority can be updated.")
		return
	}
	if req.Status != "" && req.Status != "ACTIVE" && req.Status != "DISABLED" {
		privateCAError(w, "InvalidArgsException", "Status must be ACTIVE or DISABLED.")
		return
	}
	if req.Status != "" {
		ca.Status = req.Status
	}
	if len(req.RevocationConfiguration) > 0 {
		ca.RevocationConfiguration = req.RevocationConfiguration
	}
	ca.LastStateChangeAt = float64(time.Now().UTC().UnixMilli()) / 1000
	privateCAs.Put(privateCAID(ca.ARN), ca)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePrivateCADelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn     string `json:"CertificateAuthorityArn"`
		PermanentDeletionTimeInDays int    `json:"PermanentDeletionTimeInDays"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	ca, ok := privateCAGet(w, req.CertificateAuthorityArn)
	if !ok {
		return
	}
	if ca.Status == "ACTIVE" {
		privateCAError(w, "InvalidStateException", "An ACTIVE certificate authority must be disabled before deletion.")
		return
	}
	days := req.PermanentDeletionTimeInDays
	if days == 0 {
		days = 30
	}
	if days < 7 || days > 30 {
		privateCAError(w, "InvalidArgsException", "PermanentDeletionTimeInDays must be between 7 and 30.")
		return
	}
	ca.PreDeletionStatus = ca.Status
	ca.Status = "DELETED"
	ca.RestorableUntil = float64(time.Now().AddDate(0, 0, days).Unix())
	ca.LastStateChangeAt = float64(time.Now().UTC().UnixMilli()) / 1000
	privateCAs.Put(privateCAID(ca.ARN), ca)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePrivateCARestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn string `json:"CertificateAuthorityArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	ca, ok := privateCAGet(w, req.CertificateAuthorityArn)
	if !ok {
		return
	}
	if ca.Status != "DELETED" || ca.RestorableUntil < float64(time.Now().Unix()) {
		privateCAError(w, "InvalidStateException", "The certificate authority is not restorable.")
		return
	}
	ca.Status = ca.PreDeletionStatus
	ca.RestorableUntil = 0
	ca.LastStateChangeAt = float64(time.Now().UTC().UnixMilli()) / 1000
	privateCAs.Put(privateCAID(ca.ARN), ca)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func privateCAPermissionKey(caARN, principal, sourceAccount string) string {
	return caARN + "|" + principal + "|" + sourceAccount
}

func handlePrivateCACreatePermission(w http.ResponseWriter, r *http.Request) {
	var req privateCAPermission
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	if _, ok := privateCAGet(w, req.CertificateAuthorityArn); !ok {
		return
	}
	if req.Principal != "acm.amazonaws.com" {
		privateCAError(w, "InvalidArgsException", "Principal must be acm.amazonaws.com.")
		return
	}
	key := privateCAPermissionKey(req.CertificateAuthorityArn, req.Principal, req.SourceAccount)
	if _, exists := privateCAPermissions.Get(key); exists {
		privateCAError(w, "PermissionAlreadyExistsException", "The permission already exists.")
		return
	}
	req.CreatedAt = float64(time.Now().UTC().UnixMilli()) / 1000
	privateCAPermissions.Put(key, req)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePrivateCADeletePermission(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn string `json:"CertificateAuthorityArn"`
		Principal               string `json:"Principal"`
		SourceAccount           string `json:"SourceAccount"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	if _, ok := privateCAGet(w, req.CertificateAuthorityArn); !ok {
		return
	}
	privateCAPermissions.Delete(privateCAPermissionKey(req.CertificateAuthorityArn, req.Principal, req.SourceAccount))
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePrivateCAListPermissions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn string `json:"CertificateAuthorityArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	if _, ok := privateCAGet(w, req.CertificateAuthorityArn); !ok {
		return
	}
	var permissions []privateCAPermission
	for _, permission := range privateCAPermissions.List() {
		if permission.CertificateAuthorityArn == req.CertificateAuthorityArn {
			permissions = append(permissions, permission)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Permissions": permissions})
}

func handlePrivateCAPutPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
		Policy      string `json:"Policy"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidPolicyException", "The request body could not be parsed.")
		return
	}
	if _, ok := privateCAGet(w, req.ResourceArn); !ok {
		return
	}
	var policy any
	if json.Unmarshal([]byte(req.Policy), &policy) != nil {
		privateCAError(w, "InvalidPolicyException", "Policy must be a JSON document.")
		return
	}
	privateCAPolicies.Put(req.ResourceArn, req.Policy)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePrivateCAGetPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	if _, ok := privateCAGet(w, req.ResourceArn); !ok {
		return
	}
	policy, ok := privateCAPolicies.Get(req.ResourceArn)
	if !ok {
		privateCAError(w, "ResourceNotFoundException", "No resource policy is attached.")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Policy": policy})
}

func handlePrivateCADeletePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	if _, ok := privateCAGet(w, req.ResourceArn); !ok {
		return
	}
	privateCAPolicies.Delete(req.ResourceArn)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePrivateCATag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn string        `json:"CertificateAuthorityArn"`
		Tags                    []firehoseTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidTagException", "The request body could not be parsed.")
		return
	}
	ca, ok := privateCAGet(w, req.CertificateAuthorityArn)
	if !ok {
		return
	}
	for _, tag := range req.Tags {
		ca.Tags[tag.Key] = tag.Value
	}
	privateCAs.Put(privateCAID(ca.ARN), ca)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePrivateCAUntag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn string        `json:"CertificateAuthorityArn"`
		Tags                    []firehoseTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidTagException", "The request body could not be parsed.")
		return
	}
	ca, ok := privateCAGet(w, req.CertificateAuthorityArn)
	if !ok {
		return
	}
	for _, tag := range req.Tags {
		if value, exists := ca.Tags[tag.Key]; exists && (tag.Value == "" || value == tag.Value) {
			delete(ca.Tags, tag.Key)
		}
	}
	privateCAs.Put(privateCAID(ca.ARN), ca)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handlePrivateCAListTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn string `json:"CertificateAuthorityArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	ca, ok := privateCAGet(w, req.CertificateAuthorityArn)
	if !ok {
		return
	}
	keys := make([]string, 0, len(ca.Tags))
	for key := range ca.Tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	tags := make([]firehoseTag, 0, len(keys))
	for _, key := range keys {
		tags = append(tags, firehoseTag{Key: key, Value: ca.Tags[key]})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Tags": tags})
}

func handlePrivateCARevoke(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn string `json:"CertificateAuthorityArn"`
		CertificateSerial       string `json:"CertificateSerial"`
		RevocationReason        string `json:"RevocationReason"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	ca, ok := privateCAGet(w, req.CertificateAuthorityArn)
	if !ok {
		return
	}
	found := false
	for _, certificate := range privateCACertificates.List() {
		if certificate.CAARN == ca.ARN && certificate.Serial == req.CertificateSerial {
			if certificate.Status == "REVOKED" {
				privateCAError(w, "CertificateAlreadyRevokedException", "The certificate is already revoked.")
				return
			}
			certificate.Status = "REVOKED"
			certificate.Reason = req.RevocationReason
			certificate.RevokedAt = float64(time.Now().UTC().UnixMilli()) / 1000
			privateCACertificates.Put(certificate.ARN, certificate)
			found = true
			break
		}
	}
	if !found {
		privateCAError(w, "ResourceNotFoundException", "The certificate serial was not issued by this certificate authority.")
		return
	}
	privateCAWriteCRL(ca)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func privateCAWriteCRL(ca privateCA) {
	var revocation struct {
		CrlConfiguration struct {
			Enabled          bool   `json:"Enabled"`
			S3BucketName     string `json:"S3BucketName"`
			ExpirationInDays int    `json:"ExpirationInDays"`
		} `json:"CrlConfiguration"`
	}
	if json.Unmarshal(ca.RevocationConfiguration, &revocation) != nil ||
		!revocation.CrlConfiguration.Enabled || revocation.CrlConfiguration.S3BucketName == "" {
		return
	}
	certificate, err := privateCAParseCertificate(ca.CertificatePEM)
	if err != nil {
		return
	}
	signer, err := privateCASigner(ca)
	if err != nil {
		return
	}
	var entries []x509.RevocationListEntry
	for _, issued := range privateCACertificates.List() {
		if issued.CAARN != ca.ARN || issued.Status != "REVOKED" {
			continue
		}
		serial := new(big.Int)
		if _, ok := serial.SetString(issued.Serial, 10); !ok {
			continue
		}
		entries = append(entries, x509.RevocationListEntry{
			SerialNumber: serial, RevocationTime: time.UnixMilli(int64(issued.RevokedAt * 1000)).UTC(),
		})
	}
	expiration := revocation.CrlConfiguration.ExpirationInDays
	if expiration <= 0 {
		expiration = 7
	}
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
		SignatureAlgorithm:        certificate.SignatureAlgorithm,
		RevokedCertificateEntries: entries,
		Number:                    big.NewInt(time.Now().Unix()),
		ThisUpdate:                time.Now().UTC(), NextUpdate: time.Now().UTC().AddDate(0, 0, expiration),
	}, certificate, signer)
	if err != nil {
		return
	}
	_, _ = s3PutServiceObject(revocation.CrlConfiguration.S3BucketName,
		privateCAID(ca.ARN)+".crl", der, "application/pkix-crl", nil)
}

func handlePrivateCACreateAudit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn   string `json:"CertificateAuthorityArn"`
		S3BucketName              string `json:"S3BucketName"`
		AuditReportResponseFormat string `json:"AuditReportResponseFormat"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	if _, ok := privateCAGet(w, req.CertificateAuthorityArn); !ok {
		return
	}
	id := generateUUID()
	extension := ".json"
	contentType := "application/json"
	var data []byte
	certificates := make([]privateCAIssuedCertificate, 0)
	for _, certificate := range privateCACertificates.List() {
		if certificate.CAARN == req.CertificateAuthorityArn {
			certificates = append(certificates, certificate)
		}
	}
	sort.Slice(certificates, func(i, j int) bool { return certificates[i].ARN < certificates[j].ARN })
	if req.AuditReportResponseFormat == "CSV" {
		extension = ".csv"
		contentType = "text/csv"
		var output bytes.Buffer
		writer := csv.NewWriter(&output)
		_ = writer.Write([]string{"certificateArn", "serial", "subject", "status", "issuedAt", "notBefore", "notAfter", "revokedAt", "revocationReason"})
		for _, certificate := range certificates {
			_ = writer.Write([]string{
				certificate.ARN, certificate.Serial, certificate.Subject, certificate.Status,
				strconv.FormatFloat(certificate.IssuedAt, 'f', -1, 64),
				strconv.FormatFloat(certificate.NotBefore, 'f', -1, 64),
				strconv.FormatFloat(certificate.NotAfter, 'f', -1, 64),
				strconv.FormatFloat(certificate.RevokedAt, 'f', -1, 64), certificate.Reason,
			})
		}
		writer.Flush()
		data = output.Bytes()
	} else if req.AuditReportResponseFormat == "JSON" {
		data, _ = json.Marshal(certificates)
	} else {
		privateCAError(w, "InvalidArgsException", "AuditReportResponseFormat must be JSON or CSV.")
		return
	}
	key := "audit-report/" + privateCAID(req.CertificateAuthorityArn) + "/" + id + extension
	if _, err := s3PutServiceObject(req.S3BucketName, key, data, contentType, nil); err != nil {
		privateCAError(w, "RequestFailedException", err.Error())
		return
	}
	report := privateCAAuditReport{
		ID: id, CAARN: req.CertificateAuthorityArn, Status: "SUCCESS",
		Bucket: req.S3BucketName, Key: key, CreatedAt: float64(time.Now().UTC().UnixMilli()) / 1000,
	}
	privateCAAudits.Put(req.CertificateAuthorityArn+"/"+id, report)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"AuditReportId": id, "S3Key": key})
}

func handlePrivateCADescribeAudit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateAuthorityArn string `json:"CertificateAuthorityArn"`
		AuditReportId           string `json:"AuditReportId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		privateCAError(w, "InvalidArgsException", "The request body could not be parsed.")
		return
	}
	report, ok := privateCAAudits.Get(req.CertificateAuthorityArn + "/" + req.AuditReportId)
	if !ok {
		privateCAError(w, "ResourceNotFoundException", "The audit report was not found.")
		return
	}
	sim.WriteJSON(w, http.StatusOK, report)
}
