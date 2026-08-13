package main

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// acmIssueManagedLeaf creates a new key pair and issues a CA-signed X.509 leaf
// from the persistent Amazon Certificate Manager service CA. The private key
// remains service-side, as it does for an Amazon-issued certificate, while the
// certificate and chain are returned by GetCertificate and loaded internally
// by services such as Elastic Load Balancing.
func acmIssueManagedLeaf(commonName string, sans []string, keyAlgorithm string) (
	certPEM, chainPEM, keyPEM string,
	leaf *x509.Certificate,
	err error,
) {
	caState, err := acmManagedCertificateAuthority()
	if err != nil {
		return "", "", "", nil, err
	}
	root, err := x509.ParseCertificate(caState.CertificateDER)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("parse managed certificate authority: %w", err)
	}
	block, _ := pem.Decode(caState.PrivateKeyPEM)
	if block == nil {
		return "", "", "", nil, fmt.Errorf("managed certificate authority private key is invalid")
	}
	caPrivateKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("parse managed certificate authority private key: %w", err)
	}
	caSigner, ok := caPrivateKey.(crypto.Signer)
	if !ok {
		return "", "", "", nil, fmt.Errorf("managed certificate authority private key cannot sign")
	}
	var leafSigner crypto.Signer
	switch keyAlgorithm {
	case "", "RSA_2048":
		leafSigner, err = rsa.GenerateKey(rand.Reader, 2048)
	case "RSA_3072":
		leafSigner, err = rsa.GenerateKey(rand.Reader, 3072)
	case "RSA_4096":
		leafSigner, err = rsa.GenerateKey(rand.Reader, 4096)
	case "EC_prime256v1":
		leafSigner, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "EC_secp384r1":
		leafSigner, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	default:
		return "", "", "", nil, fmt.Errorf("unsupported managed-certificate key algorithm %q", keyAlgorithm)
	}
	if err != nil {
		return "", "", "", nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", "", nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		DNSNames:     sans,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, root, leafSigner.Public(), caSigner)
	if err != nil {
		return "", "", "", nil, err
	}
	leaf, err = x509.ParseCertificate(der)
	if err != nil {
		return "", "", "", nil, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(leafSigner)
	if err != nil {
		return "", "", "", nil, err
	}
	certBlock := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	chainBlock := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caState.CertificateDER})
	keyBlock := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	return string(certBlock), string(chainBlock), string(keyBlock), leaf, nil
}

// AWS Certificate Manager. Wire: AWS-JSON 1.1 (POST /, X-Amz-Target =
// "CertificateManager.<Op>"). ImportCertificate is ISSUED immediately.
// A DNS-validated RequestCertificate (AMAZON_ISSUED) starts
// PENDING_VALIDATION and transitions to ISSUED on DescribeCertificate
// once its _acm-challenge records exist in the Route53 sim store — the
// sim can't perform real public DNS validation, so record presence is
// the validation signal (a cert with no record stays PENDING).

// ---------- Types ----------

// AWS-JSON 1.1 encodes timestamps as Unix-epoch JSON numbers (seconds
// with optional fractional part), not RFC3339 strings. The SDK
// deserialiser fails with "expected TStamp to be a JSON Number, got
// string instead" if we send a string. Using float64 keeps it lossless.
type ACMCertificate struct {
	CertificateArn           string                      `json:"CertificateArn"`
	DomainName               string                      `json:"DomainName"`
	SubjectAlternativeNames  []string                    `json:"SubjectAlternativeNames,omitempty"`
	DomainValidationOptions  []ACMDomainValidationOption `json:"DomainValidationOptions,omitempty"`
	Status                   string                      `json:"Status"`
	IssuedAt                 *float64                    `json:"IssuedAt,omitempty"`
	ImportedAt               *float64                    `json:"ImportedAt,omitempty"`
	NotBefore                *float64                    `json:"NotBefore,omitempty"`
	NotAfter                 *float64                    `json:"NotAfter,omitempty"`
	KeyAlgorithm             string                      `json:"KeyAlgorithm,omitempty"`
	SignatureAlgorithm       string                      `json:"SignatureAlgorithm,omitempty"`
	InUseBy                  []string                    `json:"InUseBy"`
	Type                     string                      `json:"Type"`
	RenewalEligibility       string                      `json:"RenewalEligibility,omitempty"`
	Options                  *ACMCertificateOptions      `json:"Options,omitempty"`
	CreatedAt                *float64                    `json:"CreatedAt,omitempty"`
	CertificateAuthorityArn  string                      `json:"CertificateAuthorityArn,omitempty"`
	Serial                   string                      `json:"Serial,omitempty"`
	Subject                  string                      `json:"Subject,omitempty"`
	Issuer                   string                      `json:"Issuer,omitempty"`
	AcmeAccountID            string                      `json:"AcmeAccountId,omitempty"`
	AcmeEndpointArn          string                      `json:"AcmeEndpointArn,omitempty"`
	CertificateKeyPairOrigin string                      `json:"CertificateKeyPairOrigin,omitempty"`
}

func acmEpochNow() *float64 {
	f := float64(time.Now().UTC().Unix())
	return &f
}

type ACMDomainValidationOption struct {
	DomainName       string             `json:"DomainName"`
	ValidationDomain string             `json:"ValidationDomain,omitempty"`
	ValidationMethod string             `json:"ValidationMethod,omitempty"`
	ValidationStatus string             `json:"ValidationStatus,omitempty"`
	ResourceRecord   *ACMResourceRecord `json:"ResourceRecord,omitempty"`
}

type ACMResourceRecord struct {
	Name  string `json:"Name"`
	Type  string `json:"Type"`
	Value string `json:"Value"`
}

type ACMCertificateOptions struct {
	CertificateTransparencyLoggingPreference string `json:"CertificateTransparencyLoggingPreference,omitempty"`
}

type acmTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value,omitempty"`
}

type acmStoredCert struct {
	Cert ACMCertificate
	Tags []acmTag
	// Material holds the PEM bytes — for an IMPORTED cert, the bytes the
	// caller supplied; for a PRIVATE cert, the leaf and chain issued by the
	// selected AWS Private Certificate Authority; for an AMAZON_ISSUED
	// DNS-validated cert, the managed service CA-signed certificate minted at issuance
	// time (PENDING_VALIDATION → ISSUED). PrivateKey is only ever returned by
	// ExportCertificate for a PRIVATE cert, matching real ACM.
	CertificateBody       string
	CertificateChain      string
	PrivateKey            string
	PrivateCertificateArn string
	// RevokedAt records the revocation time once RevokeCertificate runs.
	RevokedAt *float64
}

type acmEmailValidation struct {
	Token            string  `json:"token"`
	CertificateID    string  `json:"certificateId"`
	Domain           string  `json:"domain"`
	ValidationDomain string  `json:"validationDomain"`
	ExpiresAt        float64 `json:"expiresAt"`
}

var (
	acmCertificates     sim.Store[acmStoredCert]
	acmManagedCAs       sim.Store[acmAcmeCA]
	acmEmailValidations sim.Store[acmEmailValidation]
)

// acmCertMaterial returns the PEM-encoded certificate body and private key for
// an ISSUED certificate ARN, the material a TLS terminator (ELBv2 HTTPS/TLS
// listener) loads into a tls.Certificate. Returns ok=false if the cert is
// absent or has no key material (PENDING_VALIDATION, or a non-exportable type).
func acmCertMaterial(arn string) (certPEM, keyPEM string, ok bool) {
	id := acmARNToID(arn)
	if id == "" {
		return "", "", false
	}
	stored, found := acmCertificates.Get(id)
	if !found {
		return "", "", false
	}
	if stored.CertificateBody == "" || stored.PrivateKey == "" {
		return "", "", false
	}
	return stored.CertificateBody, stored.PrivateKey, true
}

// acmCertARN constructs an ARN for the simulator's region. Real ACM
// pins us-east-1 only for CloudFront associations — that constraint
// is enforced on the CloudFront side (cloudfront.go) against the
// region embedded in the ARN, not here at certificate creation time.
func acmCertARN(id string) string {
	return fmt.Sprintf("arn:aws:acm:%s:%s:certificate/%s", awsRegion(), awsAccountID(), id)
}

func acmRandomID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	hex := hex.EncodeToString(buf)
	// AWS uses a UUID-like format with dashes
	return hex[0:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:32]
}

func acmARNToID(arn string) string {
	const prefix = "certificate/"
	i := strings.LastIndex(arn, prefix)
	if i < 0 {
		return ""
	}
	return arn[i+len(prefix):]
}

// ---------- Registration ----------

func registerACM(r *sim.AWSRouter, srv *sim.Server) {
	acmCertificates = sim.MakeStore[acmStoredCert](srv.DB(), "acm_certificates")
	acmAccountConfiguration = sim.MakeStore[acmAccountConfig](srv.DB(), "acm_account_config")
	acmManagedCAs = sim.MakeStore[acmAcmeCA](srv.DB(), "acm_managed_certificate_authorities")
	acmEmailValidations = sim.MakeStore[acmEmailValidation](srv.DB(), "acm_email_validations")
	registerACMAcme(r, srv)
	srv.HandleFunc("GET /acm/email-validation/{token}", handleACMEmailValidation)

	r.Register("CertificateManager.RequestCertificate", handleACMRequestCertificate)
	r.Register("CertificateManager.DescribeCertificate", handleACMDescribeCertificate)
	r.Register("CertificateManager.DeleteCertificate", handleACMDeleteCertificate)
	r.Register("CertificateManager.ListCertificates", handleACMListCertificates)
	r.Register("CertificateManager.ListCertificateDomainValidations", handleACMListCertificateDomainValidations)
	r.Register("CertificateManager.AddTagsToCertificate", handleACMAddTags)
	r.Register("CertificateManager.RemoveTagsFromCertificate", handleACMRemoveTags)
	r.Register("CertificateManager.ListTagsForCertificate", handleACMListTags)
	r.Register("CertificateManager.ImportCertificate", handleACMImportCertificate)
	r.Register("CertificateManager.UpdateCertificateOptions", handleACMUpdateOptions)
	r.Register("CertificateManager.ResendValidationEmail", handleACMResendValidationEmail)
	r.Register("CertificateManager.RenewCertificate", handleACMRenewCertificate)
	r.Register("CertificateManager.GetCertificate", handleACMGetCertificate)
	r.Register("CertificateManager.ExportCertificate", handleACMExportCertificate)
	r.Register("CertificateManager.RevokeCertificate", handleACMRevokeCertificate)
	r.Register("CertificateManager.GetAccountConfiguration", handleACMGetAccountConfiguration)
	r.Register("CertificateManager.PutAccountConfiguration", handleACMPutAccountConfiguration)
	r.Register("CertificateManager.SearchCertificates", handleACMSearchCertificates)
	r.Register("CertificateManager.TagResource", handleACMTagResource)
	r.Register("CertificateManager.UntagResource", handleACMUntagResource)
	r.Register("CertificateManager.ListTagsForResource", handleACMListTagsForResource)
}

func acmManagedCertificateAuthority() (acmAcmeCA, error) {
	const key = "amazon-managed"
	if ca, ok := acmManagedCAs.Get(key); ok {
		return ca, nil
	}
	ca, err := acmGenerateCertificateAuthority("Sockerless Amazon Certificate Manager CA")
	if err != nil {
		return acmAcmeCA{}, err
	}
	acmManagedCAs.Put(key, ca)
	return ca, nil
}

// ---------- Cross-resource tagging API ----------
//
// TagResource / UntagResource / ListTagsForResource address certificates and
// every Amazon Certificate Manager ACME control-plane resource by ARN.

type acmResourceTagReq struct {
	ResourceArn string   `json:"ResourceArn"`
	Tags        []acmTag `json:"Tags"`
	TagKeys     []string `json:"TagKeys"`
}

func acmResourceTags(arn string) ([]acmTag, bool) {
	if id := acmARNToID(arn); id != "" {
		if stored, ok := acmCertificates.Get(id); ok {
			return stored.Tags, true
		}
	}
	if endpoint, ok := acmAcmeEndpoints.Get(arn); ok {
		return endpoint.Tags, true
	}
	if validation, ok := acmAcmeDomainValidations.Get(arn); ok {
		return validation.Tags, true
	}
	if binding, ok := acmAcmeBindings.Get(arn); ok {
		return binding.Tags, true
	}
	return nil, false
}

func acmSetResourceTags(arn string, tags []acmTag) bool {
	if id := acmARNToID(arn); id != "" {
		if acmCertificates.Update(id, func(stored *acmStoredCert) { stored.Tags = tags }) {
			return true
		}
	}
	if acmAcmeEndpoints.Update(arn, func(endpoint *acmAcmeEndpoint) { endpoint.Tags = tags }) {
		return true
	}
	if acmAcmeDomainValidations.Update(arn, func(validation *acmAcmeDomainValidation) { validation.Tags = tags }) {
		return true
	}
	return acmAcmeBindings.Update(arn, func(binding *acmAcmeExternalAccountBinding) { binding.Tags = tags })
}

func handleACMTagResource(w http.ResponseWriter, r *http.Request) {
	var req acmResourceTagReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "ValidationException", "could not decode request: "+err.Error())
		return
	}
	if req.ResourceArn == "" {
		acmWriteError(w, "ValidationException", "ResourceArn is required")
		return
	}
	current, ok := acmResourceTags(req.ResourceArn)
	if !ok {
		acmWriteError(w, "ResourceNotFoundException", "Could not find resource "+req.ResourceArn)
		return
	}
	tagMap := map[string]string{}
	for _, t := range current {
		tagMap[t.Key] = t.Value
	}
	for _, t := range req.Tags {
		tagMap[t.Key] = t.Value
	}
	keys := make([]string, 0, len(tagMap))
	for k := range tagMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	merged := make([]acmTag, 0, len(tagMap))
	for _, k := range keys {
		merged = append(merged, acmTag{Key: k, Value: tagMap[k]})
	}
	acmSetResourceTags(req.ResourceArn, merged)
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

func handleACMUntagResource(w http.ResponseWriter, r *http.Request) {
	var req acmResourceTagReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "ValidationException", "could not decode request: "+err.Error())
		return
	}
	current, ok := acmResourceTags(req.ResourceArn)
	if !ok {
		acmWriteError(w, "ResourceNotFoundException", "Could not find resource "+req.ResourceArn)
		return
	}
	drop := map[string]bool{}
	for _, k := range req.TagKeys {
		drop[k] = true
	}
	kept := make([]acmTag, 0, len(current))
	for _, t := range current {
		if !drop[t.Key] {
			kept = append(kept, t)
		}
	}
	acmSetResourceTags(req.ResourceArn, kept)
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

func handleACMListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req acmResourceTagReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "ValidationException", "could not decode request: "+err.Error())
		return
	}
	tags, ok := acmResourceTags(req.ResourceArn)
	if !ok {
		acmWriteError(w, "ResourceNotFoundException", "Could not find resource "+req.ResourceArn)
		return
	}
	if tags == nil {
		tags = []acmTag{}
	}
	acmWriteJSON(w, http.StatusOK, map[string][]acmTag{"Tags": tags})
}

// acmAccountConfig holds the account-level certificate configuration set by
// PutAccountConfiguration and read by GetAccountConfiguration. Real ACM keys
// this per-account; the sim is single-account, so one stored value suffices.
type acmAccountConfig struct {
	DaysBeforeExpiry *int32 `json:"DaysBeforeExpiry,omitempty"`
}

var acmAccountConfiguration sim.Store[acmAccountConfig]

// handleACMGetCertificate returns the certificate body and chain for an
// ISSUED certificate. Real ACM serves the PEM the operator imported (or that
// ACM minted); the simulator returns the stored PEM for imported/private
// certificates and the CA-signed PEM minted at issuance for an Amazon-issued
// certificate. It does not return the private key.
func handleACMGetCertificate(w http.ResponseWriter, r *http.Request) {
	var req acmCertARNReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	id := acmARNToID(req.CertificateArn)
	stored, ok := acmCertificates.Get(id)
	if !ok {
		acmWriteError(w, "ResourceNotFoundException", "Could not find certificate "+req.CertificateArn)
		return
	}
	if stored.Cert.Status != "ISSUED" {
		acmWriteError(w, "RequestInProgressException",
			"The certificate body is not yet available for "+req.CertificateArn)
		return
	}
	if stored.CertificateBody == "" {
		acmWriteError(w, "RequestInProgressException",
			"The certificate body is not yet available for "+req.CertificateArn)
		return
	}
	resp := map[string]string{"Certificate": stored.CertificateBody}
	if stored.CertificateChain != "" {
		resp["CertificateChain"] = stored.CertificateChain
	}
	acmWriteJSON(w, http.StatusOK, resp)
}

// handleACMExportCertificate returns the certificate, chain, and the
// passphrase-protected private key for a PRIVATE certificate. Real ACM only
// permits export of PRIVATE certs (those issued by an ACM Private CA or
// imported with EXPORT enabled); the simulator enforces that and encrypts the
// stored private key with the caller's passphrase before returning it.
func handleACMExportCertificate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateArn string `json:"CertificateArn"`
		Passphrase     []byte `json:"Passphrase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	if len(req.Passphrase) == 0 {
		acmWriteError(w, "InvalidParameterValueException", "Passphrase is required")
		return
	}
	id := acmARNToID(req.CertificateArn)
	stored, ok := acmCertificates.Get(id)
	if !ok {
		acmWriteError(w, "ResourceNotFoundException", "Could not find certificate "+req.CertificateArn)
		return
	}
	if stored.Cert.Type != "PRIVATE" {
		acmWriteError(w, "RequestInProgressException",
			"Certificate "+req.CertificateArn+" is not a private certificate and cannot be exported")
		return
	}
	if stored.CertificateBody == "" || stored.PrivateKey == "" {
		acmWriteError(w, "RequestInProgressException",
			"The certificate material is not yet available for "+req.CertificateArn)
		return
	}
	keyBlock, _ := pem.Decode([]byte(stored.PrivateKey))
	if keyBlock == nil {
		acmWriteError(w, "InvalidStateException", "The certificate private key is invalid")
		return
	}
	encryptedKey, err := x509.EncryptPEMBlock(rand.Reader, "ENCRYPTED PRIVATE KEY", keyBlock.Bytes,
		req.Passphrase, x509.PEMCipherAES256)
	if err != nil {
		acmWriteError(w, "InvalidStateException", "The certificate private key could not be encrypted: "+err.Error())
		return
	}
	resp := map[string]string{
		"Certificate": stored.CertificateBody,
		"PrivateKey":  string(pem.EncodeToMemory(encryptedKey)),
	}
	if stored.CertificateChain != "" {
		resp["CertificateChain"] = stored.CertificateChain
	}
	acmWriteJSON(w, http.StatusOK, resp)
}

// handleACMRevokeCertificate moves a PRIVATE certificate to REVOKED. Real ACM
// only allows revoking PRIVATE certs (issued by an ACM Private CA); the sim
// enforces that and records the revocation time. Returns the CertificateArn.
func handleACMRevokeCertificate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateArn   string `json:"CertificateArn"`
		RevocationReason string `json:"RevocationReason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	if req.RevocationReason == "" {
		acmWriteError(w, "InvalidParameterValueException", "RevocationReason is required")
		return
	}
	id := acmARNToID(req.CertificateArn)
	stored, ok := acmCertificates.Get(id)
	if !ok {
		acmWriteError(w, "ResourceNotFoundException", "Could not find certificate "+req.CertificateArn)
		return
	}
	if stored.Cert.Type != "PRIVATE" {
		acmWriteError(w, "ResourceInUseException",
			"Only private certificates can be revoked")
		return
	}
	if err := privateCARevokeIssuedCertificate(stored.PrivateCertificateArn, req.RevocationReason); err != nil {
		acmWriteError(w, "InvalidStateException", err.Error())
		return
	}
	now := acmEpochNow()
	stored.Cert.Status = "REVOKED"
	stored.RevokedAt = now
	acmCertificates.Put(id, stored)
	acmWriteJSON(w, http.StatusOK, map[string]string{"CertificateArn": stored.Cert.CertificateArn})
}

// handleACMGetAccountConfiguration returns the account expiry-events config.
// Real ACM defaults DaysBeforeExpiry to 45 when unset.
func handleACMGetAccountConfiguration(w http.ResponseWriter, r *http.Request) {
	cfg, ok := acmAccountConfiguration.Get("default")
	days := int32(45)
	if ok && cfg.DaysBeforeExpiry != nil {
		days = *cfg.DaysBeforeExpiry
	}
	acmWriteJSON(w, http.StatusOK, map[string]any{
		"ExpiryEvents": map[string]any{"DaysBeforeExpiry": days},
	})
}

// handleACMPutAccountConfiguration stores the account expiry-events config.
// Real ACM requires an IdempotencyToken; the sim validates its presence.
func handleACMPutAccountConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpiryEvents *struct {
			DaysBeforeExpiry *int32 `json:"DaysBeforeExpiry"`
		} `json:"ExpiryEvents"`
		IdempotencyToken string `json:"IdempotencyToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	if req.IdempotencyToken == "" {
		acmWriteError(w, "InvalidParameterValueException", "IdempotencyToken is required")
		return
	}
	cfg := acmAccountConfig{}
	if req.ExpiryEvents != nil {
		cfg.DaysBeforeExpiry = req.ExpiryEvents.DaysBeforeExpiry
	}
	acmAccountConfiguration.Put("default", cfg)
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

// handleACMSearchCertificates filters certificates by the AcmCertificateMetadata
// criteria (Status / Type / InUse / ValidationMethod) supplied in the
// FilterStatement and returns CertificateSearchResult entries carrying the
// per-cert metadata. The sim honors a single top-level Filter / a flat And of
// metadata filters — the criteria real callers (and terraform's data source)
// use; nested And/Or/Not trees beyond that are flattened to their metadata
// predicates.
func handleACMSearchCertificates(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FilterStatement *acmFilterStatement `json:"FilterStatement"`
		MaxResults      int                 `json:"MaxResults"`
		NextToken       string              `json:"NextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	filters := collectACMMetadataFilters(req.FilterStatement)

	results := []map[string]any{}
	for _, stored := range acmCertificates.List() {
		c := stored.Cert
		if !acmMetadataFiltersMatch(filters, c) {
			continue
		}
		meta := map[string]any{
			"Status":             c.Status,
			"Type":               c.Type,
			"RenewalEligibility": c.RenewalEligibility,
			"InUse":              len(c.InUseBy) > 0,
		}
		if c.CreatedAt != nil {
			meta["CreatedAt"] = *c.CreatedAt
		}
		if c.IssuedAt != nil {
			meta["IssuedAt"] = *c.IssuedAt
		}
		if c.ImportedAt != nil {
			meta["ImportedAt"] = *c.ImportedAt
		}
		if stored.RevokedAt != nil {
			meta["RevokedAt"] = *stored.RevokedAt
		}
		results = append(results, map[string]any{
			"CertificateArn":      c.CertificateArn,
			"CertificateMetadata": map[string]any{"AcmCertificateMetadata": meta},
		})
	}
	sort.Slice(results, func(i, j int) bool {
		a, _ := results[i]["CertificateArn"].(string)
		b, _ := results[j]["CertificateArn"].(string)
		return a < b
	})
	page, next := awsPageExplicit(results, req.NextToken, req.MaxResults)
	resp := map[string]any{"Results": page}
	if next != "" {
		resp["NextToken"] = next
	}
	acmWriteJSON(w, http.StatusOK, resp)
}

// acmFilterStatement mirrors the recursive CertificateFilterStatement union.
type acmFilterStatement struct {
	And    []acmFilterStatement  `json:"And,omitempty"`
	Or     []acmFilterStatement  `json:"Or,omitempty"`
	Not    *acmFilterStatement   `json:"Not,omitempty"`
	Filter *acmCertificateFilter `json:"Filter,omitempty"`
}

type acmCertificateFilter struct {
	CertificateArn               string                 `json:"CertificateArn,omitempty"`
	AcmCertificateMetadataFilter *acmCertMetadataFilter `json:"AcmCertificateMetadataFilter,omitempty"`
}

// acmCertMetadataFilter mirrors the AcmCertificateMetadataFilter union: each
// search request supplies exactly one of these scalar members (the SDK
// serializes e.g. {"Type":"PRIVATE"} or {"InUse":true}).
type acmCertMetadataFilter struct {
	Status           string `json:"Status,omitempty"`
	Type             string `json:"Type,omitempty"`
	InUse            *bool  `json:"InUse,omitempty"`
	ValidationMethod string `json:"ValidationMethod,omitempty"`
}

// collectACMMetadataFilters flattens a filter statement tree into the metadata
// predicates the sim evaluates. And/Or/Filter all contribute their metadata
// predicates (treated conjunctively — the common single-criterion search), Not
// is skipped (the sim doesn't model negation). This covers the searches real
// callers issue without fabricating behaviour.
func collectACMMetadataFilters(fs *acmFilterStatement) []acmCertMetadataFilter {
	if fs == nil {
		return nil
	}
	var out []acmCertMetadataFilter
	if fs.Filter != nil && fs.Filter.AcmCertificateMetadataFilter != nil {
		out = append(out, *fs.Filter.AcmCertificateMetadataFilter)
	}
	for i := range fs.And {
		out = append(out, collectACMMetadataFilters(&fs.And[i])...)
	}
	for i := range fs.Or {
		out = append(out, collectACMMetadataFilters(&fs.Or[i])...)
	}
	return out
}

func acmMetadataFiltersMatch(filters []acmCertMetadataFilter, c ACMCertificate) bool {
	for _, f := range filters {
		if f.Status != "" && f.Status != c.Status {
			return false
		}
		if f.Type != "" && f.Type != c.Type {
			return false
		}
		if f.InUse != nil && (*f.InUse) != (len(c.InUseBy) > 0) {
			return false
		}
	}
	return true
}

// acmWriteJSON / acmWriteError — JSON-1.1 protocol wraps errors in
// {"__type": "Code", "message": "..."}; status is 400 for invalid /
// 200 + body for normal success. ACM only returns 400 on errors —
// real ACM does not use 404 for missing resources.
func acmWriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func acmWriteError(w http.ResponseWriter, code, msg string) {
	acmWriteJSON(w, http.StatusBadRequest, map[string]string{
		"__type":  code,
		"message": msg,
	})
}

// ---------- Handlers ----------

type acmRequestCertificateReq struct {
	DomainName              string                      `json:"DomainName"`
	ValidationMethod        string                      `json:"ValidationMethod"`
	SubjectAlternativeNames []string                    `json:"SubjectAlternativeNames,omitempty"`
	IdempotencyToken        string                      `json:"IdempotencyToken,omitempty"`
	DomainValidationOptions []ACMDomainValidationOption `json:"DomainValidationOptions,omitempty"`
	Options                 *ACMCertificateOptions      `json:"Options,omitempty"`
	CertificateAuthorityArn string                      `json:"CertificateAuthorityArn,omitempty"`
	Tags                    []acmTag                    `json:"Tags,omitempty"`
	KeyAlgorithm            string                      `json:"KeyAlgorithm,omitempty"`
}

func handleACMRequestCertificate(w http.ResponseWriter, r *http.Request) {
	var req acmRequestCertificateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	if req.DomainName == "" {
		acmWriteError(w, "InvalidParameterValueException", "DomainName is required")
		return
	}
	method := req.ValidationMethod
	if method == "" {
		method = "EMAIL" // real ACM default
	}
	id := acmRandomID()
	now := acmEpochNow()
	domains := append([]string{req.DomainName}, req.SubjectAlternativeNames...)
	dvOpts := make([]ACMDomainValidationOption, 0, len(domains))
	for _, d := range domains {
		validationDomain := d
		for _, supplied := range req.DomainValidationOptions {
			if strings.EqualFold(supplied.DomainName, d) && supplied.ValidationDomain != "" {
				validationDomain = supplied.ValidationDomain
				break
			}
		}
		opt := ACMDomainValidationOption{
			DomainName:       d,
			ValidationDomain: validationDomain,
			ValidationMethod: method,
			ValidationStatus: "PENDING_VALIDATION",
		}
		if method == "DNS" {
			// Real ACM strips a leading "*." and validates the base domain,
			// so a wildcard SAN yields `_acm-challenge.devbox.example.com.`,
			// not a star-bearing `_acm-challenge.*.devbox.example.com.` that
			// aws_acm_certificate_validation rejects. DomainName still echoes
			// the original wildcard.
			base := strings.TrimPrefix(d, "*.")
			opt.ResourceRecord = &ACMResourceRecord{
				Name:  "_acm-challenge." + base + ".",
				Type:  "CNAME",
				Value: acmDNSValidationValue(base),
			}
		}
		dvOpts = append(dvOpts, opt)
	}
	options := req.Options
	if options == nil {
		// Real ACM defaults certificate transparency logging to ENABLED
		// and returns it on DescribeCertificate.
		options = &ACMCertificateOptions{CertificateTransparencyLoggingPreference: "ENABLED"}
	}
	cert := ACMCertificate{
		CertificateArn:          acmCertARN(id),
		DomainName:              req.DomainName,
		SubjectAlternativeNames: req.SubjectAlternativeNames,
		DomainValidationOptions: dvOpts,
		Status:                  "PENDING_VALIDATION",
		Type:                    "AMAZON_ISSUED",
		RenewalEligibility:      "INELIGIBLE",
		KeyAlgorithm:            firstNonEmpty(req.KeyAlgorithm, "RSA_2048"),
		SignatureAlgorithm:      "SHA256WITHRSA",
		Options:                 options,
		CreatedAt:               now,
		InUseBy:                 []string{},
	}
	stored := acmStoredCert{Tags: req.Tags}
	if req.CertificateAuthorityArn != "" {
		ca, exists := privateCAs.Get(privateCAID(req.CertificateAuthorityArn))
		if !exists {
			acmWriteError(w, "ResourceNotFoundException",
				"Could not find AWS Private Certificate Authority "+req.CertificateAuthorityArn)
			return
		}
		if ca.Status != "ACTIVE" {
			acmWriteError(w, "InvalidParameterException",
				"The AWS Private Certificate Authority must be ACTIVE to issue a certificate")
			return
		}
		dnsNames := append([]string{req.DomainName}, req.SubjectAlternativeNames...)
		issued, keyPEM, leaf, err := privateCAIssueManagedCertificate(
			req.CertificateAuthorityArn, req.DomainName, dnsNames, cert.KeyAlgorithm)
		if err != nil {
			acmWriteError(w, "InvalidParameterException", err.Error())
			return
		}
		notBefore := float64(leaf.NotBefore.Unix())
		notAfter := float64(leaf.NotAfter.Unix())
		cert.Status = "ISSUED"
		cert.Type = "PRIVATE"
		cert.CertificateAuthorityArn = req.CertificateAuthorityArn
		cert.DomainValidationOptions = nil
		cert.IssuedAt = now
		cert.NotBefore = &notBefore
		cert.NotAfter = &notAfter
		cert.RenewalEligibility = "ELIGIBLE"
		cert.Serial = leaf.SerialNumber.Text(16)
		cert.Subject = leaf.Subject.String()
		cert.Issuer = leaf.Issuer.String()
		cert.SignatureAlgorithm = acmSignatureAlgorithm(leaf.SignatureAlgorithm)
		stored.Cert = cert
		stored.CertificateBody = issued.Certificate
		stored.CertificateChain = issued.Chain
		stored.PrivateKey = keyPEM
		stored.PrivateCertificateArn = issued.ARN
		acmCertificates.Put(id, stored)
		acmWriteJSON(w, http.StatusOK, map[string]string{"CertificateArn": cert.CertificateArn})
		return
	}
	stored.Cert = cert
	acmCertificates.Put(id, stored)
	if method == "EMAIL" {
		acmCreateAndSendValidationEmails(r, id, stored)
	}
	acmWriteJSON(w, http.StatusOK, map[string]string{"CertificateArn": cert.CertificateArn})
}

// ACM reuses the DNS validation CNAME value for repeated certificate requests
// for the same fully qualified domain name in one account. That lets one
// long-lived Route 53 record validate replacement certificates and concurrent
// regional/CloudFront certificates without record churn.
func acmDNSValidationValue(domain string) string {
	normalized := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(domain, "*."), "."))
	sum := sha256.Sum256([]byte(awsAccountID() + ":" + normalized))
	return "_" + hex.EncodeToString(sum[:16]) + ".acm-validations.aws."
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type acmCertARNReq struct {
	CertificateArn string `json:"CertificateArn"`
}

func handleACMDescribeCertificate(w http.ResponseWriter, r *http.Request) {
	var req acmCertARNReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	id := acmARNToID(req.CertificateArn)
	stored, ok := acmCertificates.Get(id)
	if !ok {
		acmWriteError(w, "ResourceNotFoundException", "Could not find certificate "+req.CertificateArn)
		return
	}
	stored, err := acmReconcileIssuance(id, stored)
	if err != nil {
		acmWriteError(w, "InvalidParameterValueException", err.Error())
		return
	}
	acmWriteJSON(w, http.StatusOK, map[string]ACMCertificate{"Certificate": stored.Cert})
}

// acmReconcileIssuance transitions an Amazon-issued certificate from
// PENDING_VALIDATION to ISSUED once every DNS record or email validation has
// succeeded. It creates fresh service-held key material and a leaf signed by
// the persistent Amazon Certificate Manager service CA.
func acmReconcileIssuance(id string, stored acmStoredCert) (acmStoredCert, error) {
	cert := stored.Cert
	if cert.Type != "AMAZON_ISSUED" || cert.Status != "PENDING_VALIDATION" {
		return stored, nil
	}
	for i := range cert.DomainValidationOptions {
		dvo := &cert.DomainValidationOptions[i]
		if dvo.ValidationStatus == "SUCCESS" {
			continue
		}
		if dvo.ValidationMethod != "DNS" || dvo.ResourceRecord == nil ||
			!acmDNSRecordMatches(dvo.ResourceRecord.Name, dvo.ResourceRecord.Value) {
			return stored, nil
		}
		dvo.ValidationStatus = "SUCCESS"
	}
	domains := append([]string{cert.DomainName}, cert.SubjectAlternativeNames...)
	certPEM, chainPEM, keyPEM, leaf, err := acmIssueManagedLeaf(cert.DomainName, domains, cert.KeyAlgorithm)
	if err != nil {
		return stored, fmt.Errorf("mint AMAZON_ISSUED certificate material: %w", err)
	}
	now := acmEpochNow()
	notBefore := float64(leaf.NotBefore.Unix())
	notAfter := float64(leaf.NotAfter.Unix())
	cert.Status = "ISSUED"
	cert.IssuedAt = now
	cert.NotBefore = &notBefore
	cert.NotAfter = &notAfter
	cert.RenewalEligibility = "ELIGIBLE"
	cert.Serial = leaf.SerialNumber.Text(16)
	cert.Subject = leaf.Subject.String()
	cert.Issuer = leaf.Issuer.String()
	stored.Cert = cert
	stored.CertificateBody = certPEM
	stored.CertificateChain = chainPEM
	stored.PrivateKey = keyPEM
	acmCertificates.Put(id, stored)
	return stored, nil
}

func acmCreateAndSendValidationEmails(r *http.Request, certificateID string, stored acmStoredCert) {
	baseURL := awsRequestURLBase(r)
	for _, option := range stored.Cert.DomainValidationOptions {
		if option.ValidationMethod != "EMAIL" {
			continue
		}
		token := strings.ReplaceAll(acmRandomID(), "-", "")
		validation := acmEmailValidation{
			Token:            token,
			CertificateID:    certificateID,
			Domain:           option.DomainName,
			ValidationDomain: option.ValidationDomain,
			ExpiresAt:        float64(time.Now().Add(72 * time.Hour).Unix()),
		}
		acmEmailValidations.Put(token, validation)
		go acmDeliverValidationEmail(validation, baseURL+"/acm/email-validation/"+token)
	}
}

func acmDeliverValidationEmail(validation acmEmailValidation, validationURL string) {
	domain := strings.TrimSuffix(validation.ValidationDomain, ".")
	recipients := []string{
		"administrator@" + domain,
		"hostmaster@" + domain,
		"postmaster@" + domain,
		"webmaster@" + domain,
		"admin@" + domain,
	}
	message := []byte("From: Amazon Web Services <no-reply-aws@amazon.com>\r\n" +
		"To: " + strings.Join(recipients, ", ") + "\r\n" +
		"Subject: Amazon Certificate Manager certificate validation\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" +
		"Approve the Amazon Certificate Manager certificate request for " + validation.Domain + ":\r\n" +
		validationURL + "\r\n")
	if err := awsDeliverSMTP(domain, "no-reply-aws@amazon.com", recipients, message); err != nil {
		cwEvalLogger.Error().Err(err).Str("domain", domain).Str("certificateID", validation.CertificateID).
			Msg("Amazon Certificate Manager email validation delivery failed")
	}
}

func handleACMEmailValidation(w http.ResponseWriter, r *http.Request) {
	token := sim.PathParam(r, "token")
	validation, ok := acmEmailValidations.Get(token)
	if !ok || validation.ExpiresAt < float64(time.Now().Unix()) {
		http.Error(w, "The Amazon Certificate Manager validation link is invalid or expired.", http.StatusBadRequest)
		return
	}
	stored, ok := acmCertificates.Get(validation.CertificateID)
	if !ok {
		http.Error(w, "The Amazon Certificate Manager certificate request no longer exists.", http.StatusNotFound)
		return
	}
	found := false
	for i := range stored.Cert.DomainValidationOptions {
		option := &stored.Cert.DomainValidationOptions[i]
		if strings.EqualFold(option.DomainName, validation.Domain) && option.ValidationMethod == "EMAIL" {
			option.ValidationStatus = "SUCCESS"
			found = true
		}
	}
	if !found {
		http.Error(w, "The Amazon Certificate Manager validation request no longer exists.", http.StatusNotFound)
		return
	}
	acmCertificates.Put(validation.CertificateID, stored)
	acmEmailValidations.Delete(token)
	if _, err := acmReconcileIssuance(validation.CertificateID, stored); err != nil {
		http.Error(w, "Amazon Certificate Manager could not issue the certificate.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "<!doctype html><title>Certificate validated</title><p>Amazon Certificate Manager validated "+validation.Domain+".</p>")
}

// acmDNSRecordPresent reports whether a CNAME ResourceRecordSet with the given
// name exists in any Route53 hosted zone — the signal that the operator
// created the _acm-challenge validation record. DNS names are matched
// case-insensitively and trailing-dot-insensitively.
func acmDNSRecordMatches(name, value string) bool {
	want := strings.TrimSuffix(name, ".")
	wantValue := strings.TrimSuffix(value, ".")
	for _, z := range r53Zones.List() {
		for _, rec := range z.Records {
			if strings.EqualFold(rec.Type, "CNAME") &&
				strings.EqualFold(strings.TrimSuffix(rec.Name, "."), want) {
				if rec.ResourceRecords == nil {
					continue
				}
				for _, candidate := range rec.ResourceRecords.Items {
					if strings.EqualFold(strings.TrimSuffix(candidate.Value, "."), wantValue) {
						return true
					}
				}
			}
		}
	}
	return false
}

func handleACMDeleteCertificate(w http.ResponseWriter, r *http.Request) {
	var req acmCertARNReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	id := acmARNToID(req.CertificateArn)
	stored, ok := acmCertificates.Get(id)
	if !ok {
		acmWriteError(w, "ResourceNotFoundException", "Could not find certificate "+req.CertificateArn)
		return
	}
	if len(stored.Cert.InUseBy) > 0 {
		acmWriteError(w, "ResourceInUseException", "Certificate is in use and cannot be deleted")
		return
	}
	acmCertificates.Delete(id)
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

type acmCertSummary struct {
	CertificateArn                  string   `json:"CertificateArn"`
	DomainName                      string   `json:"DomainName"`
	SubjectAlternativeNameSummaries []string `json:"SubjectAlternativeNameSummaries,omitempty"`
	Status                          string   `json:"Status"`
	Type                            string   `json:"Type"`
	KeyAlgorithm                    string   `json:"KeyAlgorithm,omitempty"`
	CreatedAt                       *float64 `json:"CreatedAt,omitempty"`
}

func handleACMListCertificates(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateStatuses []string `json:"CertificateStatuses"`
		MaxItems            int      `json:"MaxItems"`
		NextToken           string   `json:"NextToken"`
	}
	// Body is optional for ListCertificates; an empty body is tolerated, but a
	// malformed body is rejected rather than silently treated as no filter.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}

	statusFilter := map[string]bool{}
	for _, s := range req.CertificateStatuses {
		statusFilter[s] = true
	}

	items := []acmCertSummary{}
	for _, stored := range acmCertificates.List() {
		c := stored.Cert
		if len(statusFilter) > 0 && !statusFilter[c.Status] {
			continue
		}
		items = append(items, acmCertSummary{
			CertificateArn:                  c.CertificateArn,
			DomainName:                      c.DomainName,
			SubjectAlternativeNameSummaries: c.SubjectAlternativeNames,
			Status:                          c.Status,
			Type:                            c.Type,
			KeyAlgorithm:                    c.KeyAlgorithm,
			CreatedAt:                       c.CreatedAt,
		})
	}
	sortBy(items, func(s acmCertSummary) string { return s.CertificateArn })
	page, next := awsPageExplicit(items, req.NextToken, req.MaxItems)
	resp := map[string]any{"CertificateSummaryList": page}
	if next != "" {
		resp["NextToken"] = next
	}
	acmWriteJSON(w, http.StatusOK, resp)
}

type acmTagReq struct {
	CertificateArn string   `json:"CertificateArn"`
	Tags           []acmTag `json:"Tags"`
}

func handleACMAddTags(w http.ResponseWriter, r *http.Request) {
	var req acmTagReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	id := acmARNToID(req.CertificateArn)
	stored, ok := acmCertificates.Get(id)
	if !ok {
		acmWriteError(w, "ResourceNotFoundException", "Could not find certificate "+req.CertificateArn)
		return
	}
	tagMap := map[string]string{}
	for _, t := range stored.Tags {
		tagMap[t.Key] = t.Value
	}
	for _, t := range req.Tags {
		tagMap[t.Key] = t.Value
	}
	merged := make([]acmTag, 0, len(tagMap))
	for k, v := range tagMap {
		merged = append(merged, acmTag{Key: k, Value: v})
	}
	stored.Tags = merged
	acmCertificates.Put(id, stored)
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

type acmRemoveTagsReq struct {
	CertificateArn string   `json:"CertificateArn"`
	Tags           []acmTag `json:"Tags"`
}

func handleACMRemoveTags(w http.ResponseWriter, r *http.Request) {
	var req acmRemoveTagsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	id := acmARNToID(req.CertificateArn)
	stored, ok := acmCertificates.Get(id)
	if !ok {
		acmWriteError(w, "ResourceNotFoundException", "Could not find certificate "+req.CertificateArn)
		return
	}
	drop := map[string]bool{}
	for _, t := range req.Tags {
		drop[t.Key] = true
	}
	kept := stored.Tags[:0]
	for _, t := range stored.Tags {
		if !drop[t.Key] {
			kept = append(kept, t)
		}
	}
	stored.Tags = kept
	acmCertificates.Put(id, stored)
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

func handleACMListTags(w http.ResponseWriter, r *http.Request) {
	var req acmCertARNReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	id := acmARNToID(req.CertificateArn)
	stored, ok := acmCertificates.Get(id)
	if !ok {
		acmWriteError(w, "ResourceNotFoundException", "Could not find certificate "+req.CertificateArn)
		return
	}
	acmWriteJSON(w, http.StatusOK, map[string][]acmTag{"Tags": stored.Tags})
}

type acmImportCertificateReq struct {
	CertificateArn string `json:"CertificateArn,omitempty"`
	// The SDK encodes these blob members as base64 on the wire and
	// json.Unmarshal decodes them back into raw []byte — storing the PEM
	// bytes verbatim so GetCertificate / ExportCertificate round-trip.
	Certificate      []byte   `json:"Certificate"`
	PrivateKey       []byte   `json:"PrivateKey"`
	CertificateChain []byte   `json:"CertificateChain,omitempty"`
	Tags             []acmTag `json:"Tags,omitempty"`
}

func handleACMImportCertificate(w http.ResponseWriter, r *http.Request) {
	var req acmImportCertificateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	if len(req.Certificate) == 0 || len(req.PrivateKey) == 0 {
		acmWriteError(w, "InvalidParameterValueException", "Certificate and PrivateKey are required")
		return
	}
	certificate, privateKey, err := acmParseImportedCertificate(req.Certificate, req.PrivateKey)
	if err != nil {
		acmWriteError(w, "InvalidParameterValueException", err.Error())
		return
	}
	if _, err := acmParseCertificateChain(req.CertificateChain); err != nil {
		acmWriteError(w, "InvalidParameterValueException", err.Error())
		return
	}
	certificatePublic, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil {
		acmWriteError(w, "InvalidParameterValueException", "Certificate public key is invalid")
		return
	}
	privatePublic, err := x509.MarshalPKIXPublicKey(privateKey.Public())
	if err != nil || !bytes.Equal(certificatePublic, privatePublic) {
		acmWriteError(w, "InvalidParameterValueException", "The private key does not match the certificate public key")
		return
	}
	now := acmEpochNow()
	// If CertificateArn is provided, this is an update — replace in place.
	id := acmARNToID(req.CertificateArn)
	if id == "" {
		id = acmRandomID()
	} else if prior, ok := acmCertificates.Get(id); !ok || prior.Cert.Type != "IMPORTED" {
		acmWriteError(w, "ResourceNotFoundException", "Could not find imported certificate "+req.CertificateArn)
		return
	}
	domainName := certificate.Subject.CommonName
	if domainName == "" && len(certificate.DNSNames) > 0 {
		domainName = certificate.DNSNames[0]
	}
	notBefore := float64(certificate.NotBefore.Unix())
	notAfter := float64(certificate.NotAfter.Unix())
	cert := ACMCertificate{
		CertificateArn:           acmCertARN(id),
		DomainName:               domainName,
		SubjectAlternativeNames:  append([]string(nil), certificate.DNSNames...),
		Status:                   "ISSUED",
		Type:                     "IMPORTED",
		ImportedAt:               now,
		CreatedAt:                now,
		IssuedAt:                 now,
		NotBefore:                &notBefore,
		NotAfter:                 &notAfter,
		KeyAlgorithm:             acmPublicKeyAlgorithm(certificate.PublicKey),
		SignatureAlgorithm:       acmSignatureAlgorithm(certificate.SignatureAlgorithm),
		InUseBy:                  []string{},
		RenewalEligibility:       "INELIGIBLE",
		Serial:                   certificate.SerialNumber.Text(16),
		Subject:                  certificate.Subject.String(),
		Issuer:                   certificate.Issuer.String(),
		CertificateKeyPairOrigin: "CUSTOMER_PROVIDED",
	}
	// Preserve any tags from a prior import when this is a re-import
	// (CertificateArn supplied) and the caller omits tags.
	tags := req.Tags
	if prior, ok := acmCertificates.Get(id); ok && len(tags) == 0 {
		tags = prior.Tags
	}
	acmCertificates.Put(id, acmStoredCert{
		Cert:             cert,
		Tags:             tags,
		CertificateBody:  string(req.Certificate),
		CertificateChain: string(req.CertificateChain),
		PrivateKey:       string(req.PrivateKey),
	})
	acmWriteJSON(w, http.StatusOK, map[string]string{"CertificateArn": cert.CertificateArn})
}

func acmParseImportedCertificate(certificatePEM, privateKeyPEM []byte) (*x509.Certificate, crypto.Signer, error) {
	certificateBlock, rest := pem.Decode(certificatePEM)
	if certificateBlock == nil || certificateBlock.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, nil, fmt.Errorf("certificate must contain exactly one PEM-encoded X.509 certificate")
	}
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("certificate is not a valid X.509 certificate: %w", err)
	}
	keyBlock, rest := pem.Decode(privateKeyPEM)
	if keyBlock == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, nil, fmt.Errorf("PrivateKey must contain exactly one PEM-encoded private key")
	}
	var parsed any
	switch keyBlock.Type {
	case "PRIVATE KEY":
		parsed, err = x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	case "RSA PRIVATE KEY":
		parsed, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	case "EC PRIVATE KEY":
		parsed, err = x509.ParseECPrivateKey(keyBlock.Bytes)
	default:
		return nil, nil, fmt.Errorf("PrivateKey PEM type %q is not supported", keyBlock.Type)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("PrivateKey is invalid: %w", err)
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, nil, fmt.Errorf("PrivateKey does not contain an RSA or EC signing key")
	}
	return certificate, signer, nil
}

func acmParseCertificateChain(chainPEM []byte) ([]*x509.Certificate, error) {
	var chain []*x509.Certificate
	rest := chainPEM
	for len(bytes.TrimSpace(rest)) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("CertificateChain must contain only PEM-encoded X.509 certificates")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("CertificateChain contains an invalid X.509 certificate: %w", err)
		}
		chain = append(chain, certificate)
	}
	return chain, nil
}

func acmPublicKeyAlgorithm(publicKey any) string {
	switch key := publicKey.(type) {
	case *rsa.PublicKey:
		return fmt.Sprintf("RSA_%d", key.N.BitLen())
	case *ecdsa.PublicKey:
		switch key.Curve {
		case elliptic.P256():
			return "EC_prime256v1"
		case elliptic.P384():
			return "EC_secp384r1"
		case elliptic.P521():
			return "EC_secp521r1"
		}
	}
	return ""
}

func acmSignatureAlgorithm(algorithm x509.SignatureAlgorithm) string {
	switch algorithm {
	case x509.SHA256WithRSA:
		return "SHA256WITHRSA"
	case x509.SHA384WithRSA:
		return "SHA384WITHRSA"
	case x509.SHA512WithRSA:
		return "SHA512WITHRSA"
	case x509.ECDSAWithSHA256:
		return "SHA256WITHECDSA"
	case x509.ECDSAWithSHA384:
		return "SHA384WITHECDSA"
	case x509.ECDSAWithSHA512:
		return "SHA512WITHECDSA"
	default:
		return strings.ToUpper(strings.ReplaceAll(algorithm.String(), "-", "WITH"))
	}
}

type acmUpdateOptionsReq struct {
	CertificateArn string                 `json:"CertificateArn"`
	Options        *ACMCertificateOptions `json:"Options"`
}

func handleACMUpdateOptions(w http.ResponseWriter, r *http.Request) {
	var req acmUpdateOptionsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	id := acmARNToID(req.CertificateArn)
	stored, ok := acmCertificates.Get(id)
	if !ok {
		acmWriteError(w, "ResourceNotFoundException", "Could not find certificate "+req.CertificateArn)
		return
	}
	stored.Cert.Options = req.Options
	acmCertificates.Put(id, stored)
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

func handleACMResendValidationEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateArn   string `json:"CertificateArn"`
		Domain           string `json:"Domain"`
		ValidationDomain string `json:"ValidationDomain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	id := acmARNToID(req.CertificateArn)
	stored, ok := acmCertificates.Get(id)
	if !ok {
		acmWriteError(w, "ResourceNotFoundException", "Could not find certificate "+req.CertificateArn)
		return
	}
	if stored.Cert.Status != "PENDING_VALIDATION" {
		acmWriteError(w, "InvalidStateException", "The certificate is not pending email validation")
		return
	}
	found := false
	for _, option := range stored.Cert.DomainValidationOptions {
		if strings.EqualFold(option.DomainName, req.Domain) &&
			option.ValidationMethod == "EMAIL" &&
			strings.EqualFold(option.ValidationDomain, req.ValidationDomain) {
			found = true
			break
		}
	}
	if !found {
		acmWriteError(w, "InvalidDomainValidationOptionsException", "The domain validation options do not match the certificate request")
		return
	}
	acmCreateAndSendValidationEmails(r, id, stored)
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

func handleACMRenewCertificate(w http.ResponseWriter, r *http.Request) {
	var req acmCertARNReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	id := acmARNToID(req.CertificateArn)
	stored, ok := acmCertificates.Get(id)
	if !ok {
		acmWriteError(w, "ResourceNotFoundException", "Could not find certificate "+req.CertificateArn)
		return
	}
	if stored.Cert.Type != "AMAZON_ISSUED" || stored.Cert.Status != "ISSUED" ||
		stored.Cert.RenewalEligibility != "ELIGIBLE" || stored.Cert.CertificateKeyPairOrigin == "ACME" {
		acmWriteError(w, "InvalidStateException", "The certificate is not eligible for managed renewal")
		return
	}
	stored.Cert.Status = "PENDING_VALIDATION"
	rotated, err := acmReconcileIssuance(id, stored)
	if err != nil {
		acmWriteError(w, "InvalidStateException", "The certificate could not be renewed: "+err.Error())
		return
	}
	if rotated.Cert.Status != "ISSUED" {
		acmWriteError(w, "InvalidStateException", "The certificate validation is no longer complete")
		return
	}
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

// ---------- CloudFront cross-resource enforcement helper ----------

// acmCertExistsInRegion checks whether the given certificate ARN exists
// AND was issued in the named region. Returns (true, true) only if both
// hold. (false, false) for missing; (true, false) for region-mismatch.
// Used by cloudfront.go to enforce the us-east-1 pin on
// ViewerCertificate.ACMCertificateArn references.
func acmCertExistsInRegion(arn, requireRegion string) (exists bool, regionMatch bool) {
	id := acmARNToID(arn)
	if id == "" {
		return false, false
	}
	if _, ok := acmCertificates.Get(id); !ok {
		return false, false
	}
	// ARN form: arn:aws:acm:<region>:<account>:certificate/<id>
	parts := strings.Split(arn, ":")
	if len(parts) < 4 {
		return true, false
	}
	return true, parts[3] == requireRegion
}

// handleACMListCertificateDomainValidations lists a certificate's domain
// validations. AWS added the operation as a paginated read beside
// DescribeCertificate, which carries the same validations inline: this answers
// from the certificate's own DomainValidationOptions, so the two views can
// never disagree. The summary splits each validation into the configuration
// the request asked for and the one currently in force, which are the same
// configuration for a certificate whose validation method has not been
// changed.
func handleACMListCertificateDomainValidations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertificateArn string `json:"CertificateArn"`
		MaxItems       int    `json:"MaxItems"`
		NextToken      string `json:"NextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		acmWriteError(w, "InvalidParameterValueException", "could not decode request: "+err.Error())
		return
	}
	if req.CertificateArn == "" {
		acmWriteError(w, "InvalidParameterValueException", "CertificateArn is required")
		return
	}
	id := acmARNToID(req.CertificateArn)
	stored, ok := acmCertificates.Get(id)
	if !ok {
		acmWriteError(w, "ResourceNotFoundException", "Could not find certificate "+req.CertificateArn)
		return
	}
	stored, err := acmReconcileIssuance(id, stored)
	if err != nil {
		acmWriteError(w, "InvalidParameterValueException", err.Error())
		return
	}

	summaries := make([]map[string]any, 0, len(stored.Cert.DomainValidationOptions))
	for _, option := range stored.Cert.DomainValidationOptions {
		configuration := map[string]any{}
		if option.ValidationMethod != "" {
			configuration["ValidationMethod"] = option.ValidationMethod
		}
		if option.ValidationStatus != "" {
			configuration["ValidationStatus"] = option.ValidationStatus
		}
		// The challenge is the method's own shape: a DNS validation carries
		// the resource record the caller must publish, an email validation
		// the domain the approval mail goes to.
		switch {
		case option.ResourceRecord != nil:
			configuration["ValidationChallenge"] = map[string]any{
				"DnsValidationChallenge": map[string]any{"ResourceRecord": option.ResourceRecord},
			}
		case strings.EqualFold(option.ValidationMethod, "EMAIL") && option.ValidationDomain != "":
			configuration["ValidationChallenge"] = map[string]any{
				"EmailValidationChallenge": map[string]any{"ValidationDomain": option.ValidationDomain},
			}
		}
		summary := map[string]any{"DomainName": option.DomainName}
		if len(configuration) > 0 {
			summary["RequestedValidationConfiguration"] = configuration
			summary["ActiveValidationConfiguration"] = configuration
		}
		summaries = append(summaries, summary)
	}

	page, next := awsPageExplicit(summaries, req.NextToken, req.MaxItems)
	out := map[string]any{"DomainValidationSummaryList": page}
	if next != "" {
		out["NextToken"] = next
	}
	acmWriteJSON(w, http.StatusOK, out)
}
