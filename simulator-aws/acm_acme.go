package main

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon Certificate Manager ACME consists of an AWS control plane and an
// RFC 8555 certificate-authority data plane. The stores below are the service
// state shared by both planes: accounts only arise from authenticated ACME
// registration, and issued certificates only arise from finalized CSRs.

type acmAcmeCertificateAuthority struct {
	PublicCertificateAuthority *struct {
		AllowedKeyAlgorithms []string `json:"AllowedKeyAlgorithms"`
	} `json:"PublicCertificateAuthority,omitempty"`
}

type acmAcmeEndpoint struct {
	AcmeEndpointArn       string                      `json:"AcmeEndpointArn"`
	AuthorizationBehavior string                      `json:"AuthorizationBehavior"`
	CertificateAuthority  acmAcmeCertificateAuthority `json:"CertificateAuthority"`
	CertificateTags       []acmTag                    `json:"CertificateTags,omitempty"`
	Contact               string                      `json:"Contact,omitempty"`
	CreatedAt             float64                     `json:"CreatedAt"`
	EndpointURL           string                      `json:"EndpointUrl"`
	Status                string                      `json:"Status"`
	UpdatedAt             float64                     `json:"UpdatedAt"`
	Tags                  []acmTag                    `json:"-"`
}

type acmAcmeDomainScope struct {
	ExactDomain string `json:"ExactDomain,omitempty"`
	Subdomains  string `json:"Subdomains,omitempty"`
	Wildcards   string `json:"Wildcards,omitempty"`
}

type acmAcmeDNSPrevalidationOptions struct {
	DomainScope  acmAcmeDomainScope `json:"DomainScope"`
	HostedZoneID string             `json:"HostedZoneId,omitempty"`
}

type acmAcmePrevalidationOptions struct {
	DNSPrevalidation *acmAcmeDNSPrevalidationOptions `json:"DnsPrevalidation,omitempty"`
}

type acmAcmeDNSPrevalidationDetails struct {
	DomainScope    acmAcmeDomainScope `json:"DomainScope"`
	HostedZoneID   string             `json:"HostedZoneId,omitempty"`
	ResourceRecord *ACMResourceRecord `json:"ResourceRecord"`
}

type acmAcmePrevalidationDetails struct {
	DNSPrevalidation *acmAcmeDNSPrevalidationDetails `json:"DnsPrevalidation,omitempty"`
}

type acmAcmeDomainValidation struct {
	AcmeDomainValidationArn string                      `json:"AcmeDomainValidationArn"`
	AcmeEndpointArn         string                      `json:"AcmeEndpointArn"`
	CreatedAt               float64                     `json:"CreatedAt"`
	DomainName              string                      `json:"DomainName"`
	PrevalidationDetails    acmAcmePrevalidationDetails `json:"PrevalidationDetails"`
	PrevalidationType       string                      `json:"PrevalidationType"`
	Status                  string                      `json:"Status"`
	UpdatedAt               float64                     `json:"UpdatedAt"`
	Tags                    []acmTag                    `json:"-"`
}

type acmAcmeExternalAccountBinding struct {
	AcmeEndpointArn               string   `json:"AcmeEndpointArn"`
	AcmeExternalAccountBindingArn string   `json:"AcmeExternalAccountBindingArn"`
	CreatedAt                     float64  `json:"CreatedAt"`
	ExpiresAt                     *float64 `json:"ExpiresAt,omitempty"`
	LastUsedAt                    *float64 `json:"LastUsedAt,omitempty"`
	RevokedAt                     *float64 `json:"RevokedAt,omitempty"`
	RoleArn                       string   `json:"RoleArn"`
	UpdatedAt                     float64  `json:"UpdatedAt"`
	KeyID                         string   `json:"-"`
	MACKey                        string   `json:"-"`
	AccountURL                    string   `json:"-"`
	Tags                          []acmTag `json:"-"`
}

type acmAcmeAccount struct {
	AccountURL                    string          `json:"AccountUrl"`
	AcmeEndpointArn               string          `json:"-"`
	AcmeExternalAccountBindingArn string          `json:"AcmeExternalAccountBindingArn"`
	Contacts                      []string        `json:"Contacts"`
	CreatedAt                     float64         `json:"CreatedAt"`
	PublicKeyThumbprint           string          `json:"PublicKeyThumbprint"`
	Status                        string          `json:"Status"`
	PublicJWK                     json.RawMessage `json:"-"`
}

type acmAcmeIdentifier struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type acmAcmeAuthorization struct {
	ID            string            `json:"-"`
	EndpointID    string            `json:"-"`
	AccountURL    string            `json:"-"`
	Identifier    acmAcmeIdentifier `json:"identifier"`
	Status        string            `json:"status"`
	Expires       string            `json:"expires,omitempty"`
	Wildcard      bool              `json:"wildcard,omitempty"`
	ValidationArn string            `json:"-"`
}

type acmAcmeOrder struct {
	ID             string              `json:"-"`
	EndpointID     string              `json:"-"`
	AccountURL     string              `json:"-"`
	Status         string              `json:"status"`
	Expires        string              `json:"expires,omitempty"`
	Identifiers    []acmAcmeIdentifier `json:"identifiers"`
	Authorizations []string            `json:"authorizations"`
	Finalize       string              `json:"finalize"`
	Certificate    string              `json:"certificate,omitempty"`
	CertificateArn string              `json:"-"`
	Error          map[string]any      `json:"error,omitempty"`
}

type acmAcmeCA struct {
	CertificateDER []byte `json:"certificateDer"`
	PrivateKeyPEM  []byte `json:"privateKeyPem"`
}

type acmAcmeNonce struct {
	Value string `json:"value"`
}

var (
	acmAcmeEndpoints         sim.Store[acmAcmeEndpoint]
	acmAcmeDomainValidations sim.Store[acmAcmeDomainValidation]
	acmAcmeBindings          sim.Store[acmAcmeExternalAccountBinding]
	acmAcmeAccounts          sim.Store[acmAcmeAccount]
	acmAcmeAuthorizations    sim.Store[acmAcmeAuthorization]
	acmAcmeOrders            sim.Store[acmAcmeOrder]
	acmAcmeCAs               sim.Store[acmAcmeCA]
	acmAcmeNonces            sim.Store[acmAcmeNonce]
)

func registerACMAcme(r *AWSRouter, srv *sim.Server) {
	acmAcmeEndpoints = sim.MakeStore[acmAcmeEndpoint](srv.DB(), "acm_acme_endpoints")
	acmAcmeDomainValidations = sim.MakeStore[acmAcmeDomainValidation](srv.DB(), "acm_acme_domain_validations")
	acmAcmeBindings = sim.MakeStore[acmAcmeExternalAccountBinding](srv.DB(), "acm_acme_bindings")
	acmAcmeAccounts = sim.MakeStore[acmAcmeAccount](srv.DB(), "acm_acme_accounts")
	acmAcmeAuthorizations = sim.MakeStore[acmAcmeAuthorization](srv.DB(), "acm_acme_authorizations")
	acmAcmeOrders = sim.MakeStore[acmAcmeOrder](srv.DB(), "acm_acme_orders")
	acmAcmeCAs = sim.MakeStore[acmAcmeCA](srv.DB(), "acm_acme_cas")
	acmAcmeNonces = sim.MakeStore[acmAcmeNonce](srv.DB(), "acm_acme_nonces")

	r.Register("CertificateManager.CreateAcmeEndpoint", handleACMCreateAcmeEndpoint)
	r.Register("CertificateManager.DeleteAcmeEndpoint", handleACMDeleteAcmeEndpoint)
	r.Register("CertificateManager.DescribeAcmeEndpoint", handleACMDescribeAcmeEndpoint)
	r.Register("CertificateManager.ListAcmeEndpoints", handleACMListAcmeEndpoints)
	r.Register("CertificateManager.UpdateAcmeEndpoint", handleACMUpdateAcmeEndpoint)
	r.Register("CertificateManager.CreateAcmeDomainValidation", handleACMCreateAcmeDomainValidation)
	r.Register("CertificateManager.DeleteAcmeDomainValidation", handleACMDeleteAcmeDomainValidation)
	r.Register("CertificateManager.DescribeAcmeDomainValidation", handleACMDescribeAcmeDomainValidation)
	r.Register("CertificateManager.ListAcmeDomainValidations", handleACMListAcmeDomainValidations)
	r.Register("CertificateManager.UpdateAcmeDomainValidation", handleACMUpdateAcmeDomainValidation)
	r.Register("CertificateManager.CreateAcmeExternalAccountBinding", handleACMCreateAcmeExternalAccountBinding)
	r.Register("CertificateManager.DeleteAcmeExternalAccountBinding", handleACMDeleteAcmeExternalAccountBinding)
	r.Register("CertificateManager.DescribeAcmeExternalAccountBinding", handleACMDescribeAcmeExternalAccountBinding)
	r.Register("CertificateManager.GetAcmeExternalAccountBindingCredentials", handleACMGetAcmeExternalAccountBindingCredentials)
	r.Register("CertificateManager.ListAcmeExternalAccountBindings", handleACMListAcmeExternalAccountBindings)
	r.Register("CertificateManager.RevokeAcmeExternalAccountBinding", handleACMRevokeAcmeExternalAccountBinding)
	r.Register("CertificateManager.DescribeAcmeAccount", handleACMDescribeAcmeAccount)
	r.Register("CertificateManager.ListAcmeAccounts", handleACMListAcmeAccounts)
	r.Register("CertificateManager.RevokeAcmeAccount", handleACMRevokeAcmeAccount)

	srv.HandleFunc("GET /acme/{endpoint}/directory", handleACMEDataPlane)
	srv.HandleFunc("HEAD /acme/{endpoint}/new-nonce", handleACMEDataPlane)
	srv.HandleFunc("GET /acme/{endpoint}/new-nonce", handleACMEDataPlane)
	srv.HandleFunc("POST /acme/{endpoint}/{resource}", handleACMEDataPlane)
	srv.HandleFunc("POST /acme/{endpoint}/{resource}/{id}", handleACMEDataPlane)
}

func acmAcmeARN(kind, endpointID, resourceID string) string {
	base := fmt.Sprintf("arn:aws:acm:%s:%s:acme-endpoint/%s", awsRegion(), awsAccountID(), endpointID)
	if kind == "endpoint" {
		return base
	}
	return base + "/" + kind + "/" + resourceID
}

func acmAcmeEndpointID(arn string) string {
	const marker = ":acme-endpoint/"
	i := strings.Index(arn, marker)
	if i < 0 {
		return ""
	}
	tail := arn[i+len(marker):]
	if j := strings.IndexByte(tail, '/'); j >= 0 {
		tail = tail[:j]
	}
	return tail
}

func acmDecodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		acmWriteError(w, "ValidationException", "could not decode request: "+err.Error())
		return false
	}
	return true
}

func acmResourceNotFound(w http.ResponseWriter, arn string) {
	acmWriteError(w, "ResourceNotFoundException", "Could not find resource "+arn)
}

func handleACMCreateAcmeEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AuthorizationBehavior string                      `json:"AuthorizationBehavior"`
		CertificateAuthority  acmAcmeCertificateAuthority `json:"CertificateAuthority"`
		CertificateTags       []acmTag                    `json:"CertificateTags"`
		Contact               string                      `json:"Contact"`
		Tags                  []acmTag                    `json:"Tags"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	if req.AuthorizationBehavior != "PRE_APPROVED" || req.CertificateAuthority.PublicCertificateAuthority == nil {
		acmWriteError(w, "ValidationException", "AuthorizationBehavior PRE_APPROVED and PublicCertificateAuthority are required")
		return
	}
	if req.Contact == "" {
		req.Contact = "NOT_REQUIRED"
	}
	if req.Contact != "REQUIRED" && req.Contact != "NOT_REQUIRED" {
		acmWriteError(w, "ValidationException", "Contact must be REQUIRED or NOT_REQUIRED")
		return
	}
	if len(req.CertificateAuthority.PublicCertificateAuthority.AllowedKeyAlgorithms) == 0 {
		req.CertificateAuthority.PublicCertificateAuthority.AllowedKeyAlgorithms = []string{"RSA_2048", "EC_prime256v1", "EC_secp384r1"}
	}
	id := acmRandomID()
	arn := acmAcmeARN("endpoint", id, "")
	now := float64(time.Now().Unix())
	endpoint := acmAcmeEndpoint{
		AcmeEndpointArn:       arn,
		AuthorizationBehavior: req.AuthorizationBehavior,
		CertificateAuthority:  req.CertificateAuthority,
		CertificateTags:       req.CertificateTags,
		Contact:               req.Contact,
		CreatedAt:             now,
		EndpointURL:           awsRequestURLBase(r) + "/acme/" + id + "/directory",
		Status:                "ACTIVE",
		UpdatedAt:             now,
		Tags:                  req.Tags,
	}
	ca, err := acmGenerateCertificateAuthority("Sockerless Amazon Certificate Manager ACME CA " + id)
	if err != nil {
		acmWriteError(w, "InternalServerException", "could not initialize ACME certificate authority: "+err.Error())
		return
	}
	acmAcmeCAs.Put(id, ca)
	acmAcmeEndpoints.Put(arn, endpoint)
	acmWriteJSON(w, http.StatusOK, map[string]string{"AcmeEndpointArn": arn})
}

func handleACMDescribeAcmeEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AcmeEndpointArn string `json:"AcmeEndpointArn"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	endpoint, ok := acmAcmeEndpoints.Get(req.AcmeEndpointArn)
	if !ok {
		acmResourceNotFound(w, req.AcmeEndpointArn)
		return
	}
	acmWriteJSON(w, http.StatusOK, map[string]any{"AcmeEndpoint": endpoint})
}

func handleACMListAcmeEndpoints(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	all := acmAcmeEndpoints.List()
	sort.Slice(all, func(i, j int) bool { return all[i].AcmeEndpointArn < all[j].AcmeEndpointArn })
	page, next := awsPageExplicit(all, req.NextToken, req.MaxResults)
	resp := map[string]any{"AcmeEndpoints": page}
	if next != "" {
		resp["NextToken"] = next
	}
	acmWriteJSON(w, http.StatusOK, resp)
}

func handleACMUpdateAcmeEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AcmeEndpointArn       string                       `json:"AcmeEndpointArn"`
		AuthorizationBehavior string                       `json:"AuthorizationBehavior"`
		CertificateAuthority  *acmAcmeCertificateAuthority `json:"CertificateAuthority"`
		Contact               string                       `json:"Contact"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	if !acmAcmeEndpoints.Update(req.AcmeEndpointArn, func(endpoint *acmAcmeEndpoint) {
		if req.AuthorizationBehavior != "" {
			endpoint.AuthorizationBehavior = req.AuthorizationBehavior
		}
		if req.CertificateAuthority != nil {
			endpoint.CertificateAuthority = *req.CertificateAuthority
		}
		if req.Contact != "" {
			endpoint.Contact = req.Contact
		}
		endpoint.UpdatedAt = float64(time.Now().Unix())
	}) {
		acmResourceNotFound(w, req.AcmeEndpointArn)
		return
	}
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

func handleACMDeleteAcmeEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AcmeEndpointArn string `json:"AcmeEndpointArn"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	if _, ok := acmAcmeEndpoints.Get(req.AcmeEndpointArn); !ok {
		acmResourceNotFound(w, req.AcmeEndpointArn)
		return
	}
	for _, validation := range acmAcmeDomainValidations.List() {
		if validation.AcmeEndpointArn == req.AcmeEndpointArn {
			acmWriteError(w, "ResourceInUseException", "The ACME endpoint still has domain validations")
			return
		}
	}
	for _, binding := range acmAcmeBindings.List() {
		if binding.AcmeEndpointArn == req.AcmeEndpointArn && binding.RevokedAt == nil {
			acmWriteError(w, "ResourceInUseException", "The ACME endpoint still has active external account bindings")
			return
		}
	}
	acmAcmeEndpoints.Delete(req.AcmeEndpointArn)
	acmAcmeCAs.Delete(acmAcmeEndpointID(req.AcmeEndpointArn))
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

func handleACMCreateAcmeDomainValidation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AcmeEndpointArn      string                      `json:"AcmeEndpointArn"`
		DomainName           string                      `json:"DomainName"`
		PrevalidationOptions acmAcmePrevalidationOptions `json:"PrevalidationOptions"`
		Tags                 []acmTag                    `json:"Tags"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	if _, ok := acmAcmeEndpoints.Get(req.AcmeEndpointArn); !ok {
		acmResourceNotFound(w, req.AcmeEndpointArn)
		return
	}
	if req.DomainName == "" || req.PrevalidationOptions.DNSPrevalidation == nil {
		acmWriteError(w, "ValidationException", "DomainName and DnsPrevalidation are required")
		return
	}
	for _, existing := range acmAcmeDomainValidations.List() {
		if existing.AcmeEndpointArn == req.AcmeEndpointArn && strings.EqualFold(existing.DomainName, req.DomainName) {
			acmWriteError(w, "ConflictException", "A domain validation already exists for "+req.DomainName)
			return
		}
	}
	id := acmRandomID()
	endpointID := acmAcmeEndpointID(req.AcmeEndpointArn)
	arn := acmAcmeARN("acme-domain-validation", endpointID, id)
	token := strings.ReplaceAll(acmRandomID(), "-", "")
	domain := strings.TrimPrefix(strings.ToLower(req.DomainName), "*.")
	record := &ACMResourceRecord{
		Name:  "_" + token + "." + domain + ".",
		Type:  "CNAME",
		Value: "_" + strings.ReplaceAll(acmRandomID(), "-", "") + ".acm-validations.aws.",
	}
	now := float64(time.Now().Unix())
	validation := acmAcmeDomainValidation{
		AcmeDomainValidationArn: arn,
		AcmeEndpointArn:         req.AcmeEndpointArn,
		CreatedAt:               now,
		DomainName:              req.DomainName,
		PrevalidationDetails: acmAcmePrevalidationDetails{DNSPrevalidation: &acmAcmeDNSPrevalidationDetails{
			DomainScope:    req.PrevalidationOptions.DNSPrevalidation.DomainScope,
			HostedZoneID:   req.PrevalidationOptions.DNSPrevalidation.HostedZoneID,
			ResourceRecord: record,
		}},
		PrevalidationType: "DNS_PREVALIDATION",
		Status:            "VALIDATING",
		UpdatedAt:         now,
		Tags:              req.Tags,
	}
	acmAcmeDomainValidations.Put(arn, validation)
	acmAcmeProvisionRoute53Record(validation)
	acmWriteJSON(w, http.StatusOK, map[string]string{"AcmeDomainValidationArn": arn})
}

func acmAcmeProvisionRoute53Record(validation acmAcmeDomainValidation) {
	dns := validation.PrevalidationDetails.DNSPrevalidation
	if dns == nil || dns.HostedZoneID == "" || dns.ResourceRecord == nil {
		return
	}
	zoneID := strings.TrimPrefix(dns.HostedZoneID, "/hostedzone/")
	r53Zones.Update(zoneID, func(zone *r53StoredZone) {
		ttl := int64(300)
		rr := R53ResourceRecordSet{
			Name:            dns.ResourceRecord.Name,
			Type:            "CNAME",
			TTL:             &ttl,
			ResourceRecords: &R53ResourceRecords{Items: []R53ResourceRecord{{Value: dns.ResourceRecord.Value}}},
		}
		zone.Records = rrsetReplace(zone.Records, rr)
		zone.Zone.ResourceRecordSetCount = len(zone.Records)
	})
}

func acmReconcileAcmeDomainValidation(validation acmAcmeDomainValidation) acmAcmeDomainValidation {
	dns := validation.PrevalidationDetails.DNSPrevalidation
	if validation.Status == "VALIDATING" && dns != nil && dns.ResourceRecord != nil &&
		acmDNSRecordMatches(dns.ResourceRecord.Name, dns.ResourceRecord.Value) {
		validation.Status = "VALID"
		validation.UpdatedAt = float64(time.Now().Unix())
		acmAcmeDomainValidations.Put(validation.AcmeDomainValidationArn, validation)
	}
	return validation
}

func handleACMDescribeAcmeDomainValidation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arn string `json:"AcmeDomainValidationArn"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	validation, ok := acmAcmeDomainValidations.Get(req.Arn)
	if !ok {
		acmResourceNotFound(w, req.Arn)
		return
	}
	validation = acmReconcileAcmeDomainValidation(validation)
	acmWriteJSON(w, http.StatusOK, map[string]any{"AcmeDomainValidation": validation})
}

func handleACMListAcmeDomainValidations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AcmeEndpointArn string `json:"AcmeEndpointArn"`
		MaxResults      int    `json:"MaxResults"`
		NextToken       string `json:"NextToken"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	if _, ok := acmAcmeEndpoints.Get(req.AcmeEndpointArn); !ok {
		acmResourceNotFound(w, req.AcmeEndpointArn)
		return
	}
	all := make([]acmAcmeDomainValidation, 0)
	for _, validation := range acmAcmeDomainValidations.List() {
		if validation.AcmeEndpointArn == req.AcmeEndpointArn {
			all = append(all, acmReconcileAcmeDomainValidation(validation))
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].AcmeDomainValidationArn < all[j].AcmeDomainValidationArn })
	page, next := awsPageExplicit(all, req.NextToken, req.MaxResults)
	resp := map[string]any{"AcmeDomainValidations": page}
	if next != "" {
		resp["NextToken"] = next
	}
	acmWriteJSON(w, http.StatusOK, resp)
}

func handleACMUpdateAcmeDomainValidation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arn                  string                      `json:"AcmeDomainValidationArn"`
		PrevalidationOptions acmAcmePrevalidationOptions `json:"PrevalidationOptions"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	if req.PrevalidationOptions.DNSPrevalidation == nil {
		acmWriteError(w, "ValidationException", "DnsPrevalidation is required")
		return
	}
	if !acmAcmeDomainValidations.Update(req.Arn, func(validation *acmAcmeDomainValidation) {
		details := validation.PrevalidationDetails.DNSPrevalidation
		details.DomainScope = req.PrevalidationOptions.DNSPrevalidation.DomainScope
		details.HostedZoneID = req.PrevalidationOptions.DNSPrevalidation.HostedZoneID
		validation.Status = "VALIDATING"
		validation.UpdatedAt = float64(time.Now().Unix())
	}) {
		acmResourceNotFound(w, req.Arn)
		return
	}
	validation, _ := acmAcmeDomainValidations.Get(req.Arn)
	acmAcmeProvisionRoute53Record(validation)
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

func handleACMDeleteAcmeDomainValidation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arn string `json:"AcmeDomainValidationArn"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	if !acmAcmeDomainValidations.Delete(req.Arn) {
		acmResourceNotFound(w, req.Arn)
		return
	}
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

func handleACMCreateAcmeExternalAccountBinding(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AcmeEndpointArn string `json:"AcmeEndpointArn"`
		RoleArn         string `json:"RoleArn"`
		Expiration      *struct {
			Type  string `json:"Type"`
			Value int64  `json:"Value"`
		} `json:"Expiration"`
		Tags []acmTag `json:"Tags"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	if _, ok := acmAcmeEndpoints.Get(req.AcmeEndpointArn); !ok {
		acmResourceNotFound(w, req.AcmeEndpointArn)
		return
	}
	if req.RoleArn == "" {
		acmWriteError(w, "ValidationException", "RoleArn is required")
		return
	}
	id := acmRandomID()
	arn := acmAcmeARN("acme-external-account-binding", acmAcmeEndpointID(req.AcmeEndpointArn), id)
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		acmWriteError(w, "InternalServerException", "could not generate external account binding key")
		return
	}
	nowTime := time.Now().UTC()
	now := float64(nowTime.Unix())
	var expires *float64
	if req.Expiration != nil {
		duration := time.Duration(req.Expiration.Value)
		switch req.Expiration.Type {
		case "MINUTES":
			duration *= time.Minute
		case "HOURS":
			duration *= time.Hour
		case "DAYS":
			duration *= 24 * time.Hour
		default:
			acmWriteError(w, "ValidationException", "Expiration.Type must be MINUTES, HOURS, or DAYS")
			return
		}
		value := float64(nowTime.Add(duration).Unix())
		expires = &value
	}
	binding := acmAcmeExternalAccountBinding{
		AcmeEndpointArn:               req.AcmeEndpointArn,
		AcmeExternalAccountBindingArn: arn,
		CreatedAt:                     now,
		ExpiresAt:                     expires,
		RoleArn:                       req.RoleArn,
		UpdatedAt:                     now,
		KeyID:                         id,
		MACKey:                        base64.RawURLEncoding.EncodeToString(keyBytes),
		Tags:                          req.Tags,
	}
	acmAcmeBindings.Put(arn, binding)
	acmWriteJSON(w, http.StatusOK, map[string]any{"ExternalAccountBinding": binding})
}

func handleACMDescribeAcmeExternalAccountBinding(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arn string `json:"AcmeExternalAccountBindingArn"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	binding, ok := acmAcmeBindings.Get(req.Arn)
	if !ok {
		acmResourceNotFound(w, req.Arn)
		return
	}
	acmWriteJSON(w, http.StatusOK, map[string]any{"ExternalAccountBinding": binding})
}

func handleACMGetAcmeExternalAccountBindingCredentials(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arn string `json:"AcmeExternalAccountBindingArn"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	binding, ok := acmAcmeBindings.Get(req.Arn)
	if !ok {
		acmResourceNotFound(w, req.Arn)
		return
	}
	if binding.RevokedAt != nil || (binding.ExpiresAt != nil && *binding.ExpiresAt <= float64(time.Now().Unix())) {
		acmWriteError(w, "ConflictException", "The external account binding is not active")
		return
	}
	acmWriteJSON(w, http.StatusOK, map[string]string{"KeyId": binding.KeyID, "MacKey": binding.MACKey})
}

func handleACMListAcmeExternalAccountBindings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AcmeEndpointArn string `json:"AcmeEndpointArn"`
		MaxResults      int    `json:"MaxResults"`
		NextToken       string `json:"NextToken"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	if _, ok := acmAcmeEndpoints.Get(req.AcmeEndpointArn); !ok {
		acmResourceNotFound(w, req.AcmeEndpointArn)
		return
	}
	all := make([]acmAcmeExternalAccountBinding, 0)
	for _, binding := range acmAcmeBindings.List() {
		if binding.AcmeEndpointArn == req.AcmeEndpointArn {
			all = append(all, binding)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].AcmeExternalAccountBindingArn < all[j].AcmeExternalAccountBindingArn
	})
	page, next := awsPageExplicit(all, req.NextToken, req.MaxResults)
	resp := map[string]any{"ExternalAccountBindings": page}
	if next != "" {
		resp["NextToken"] = next
	}
	acmWriteJSON(w, http.StatusOK, resp)
}

func handleACMRevokeAcmeExternalAccountBinding(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arn string `json:"AcmeExternalAccountBindingArn"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	now := float64(time.Now().Unix())
	if !acmAcmeBindings.Update(req.Arn, func(binding *acmAcmeExternalAccountBinding) {
		binding.RevokedAt = &now
		binding.UpdatedAt = now
	}) {
		acmResourceNotFound(w, req.Arn)
		return
	}
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

func handleACMDeleteAcmeExternalAccountBinding(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arn string `json:"AcmeExternalAccountBindingArn"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	binding, ok := acmAcmeBindings.Get(req.Arn)
	if !ok {
		acmResourceNotFound(w, req.Arn)
		return
	}
	if binding.AccountURL != "" && binding.RevokedAt == nil {
		acmWriteError(w, "ResourceInUseException", "The external account binding is associated with an ACME account")
		return
	}
	acmAcmeBindings.Delete(req.Arn)
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

func handleACMDescribeAcmeAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AcmeEndpointArn string `json:"AcmeEndpointArn"`
		AccountURL      string `json:"AccountUrl"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	account, ok := acmAcmeAccounts.Get(req.AccountURL)
	if !ok || account.AcmeEndpointArn != req.AcmeEndpointArn {
		acmResourceNotFound(w, req.AccountURL)
		return
	}
	acmWriteJSON(w, http.StatusOK, map[string]any{"AcmeAccount": account})
}

func handleACMListAcmeAccounts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AcmeEndpointArn string `json:"AcmeEndpointArn"`
		MaxResults      int    `json:"MaxResults"`
		NextToken       string `json:"NextToken"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	if _, ok := acmAcmeEndpoints.Get(req.AcmeEndpointArn); !ok {
		acmResourceNotFound(w, req.AcmeEndpointArn)
		return
	}
	all := make([]acmAcmeAccount, 0)
	for _, account := range acmAcmeAccounts.List() {
		if account.AcmeEndpointArn == req.AcmeEndpointArn {
			all = append(all, account)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].AccountURL < all[j].AccountURL })
	page, next := awsPageExplicit(all, req.NextToken, req.MaxResults)
	resp := map[string]any{"AcmeAccounts": page}
	if next != "" {
		resp["NextToken"] = next
	}
	acmWriteJSON(w, http.StatusOK, resp)
}

func handleACMRevokeAcmeAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AcmeEndpointArn string `json:"AcmeEndpointArn"`
		AccountURL      string `json:"AccountUrl"`
	}
	if !acmDecodeJSON(w, r, &req) {
		return
	}
	account, ok := acmAcmeAccounts.Get(req.AccountURL)
	if !ok || account.AcmeEndpointArn != req.AcmeEndpointArn {
		acmResourceNotFound(w, req.AccountURL)
		return
	}
	account.Status = "REVOKED"
	acmAcmeAccounts.Put(account.AccountURL, account)
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

func acmGenerateCertificateAuthority(commonName string) (acmAcmeCA, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return acmAcmeCA{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return acmAcmeCA{}, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return acmAcmeCA{}, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return acmAcmeCA{}, err
	}
	return acmAcmeCA{
		CertificateDER: der,
		PrivateKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
	}, nil
}

func acmACMEURL(r *http.Request, suffix string) string {
	return awsRequestURLBase(r) + "/acme/" + sim.PathParam(r, "endpoint") + suffix
}

func acmIssueNonce(w http.ResponseWriter) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return
	}
	nonce := base64.RawURLEncoding.EncodeToString(buf)
	acmAcmeNonces.Put(nonce, acmAcmeNonce{Value: nonce})
	w.Header().Set("Replay-Nonce", nonce)
}

func acmProblem(w http.ResponseWriter, status int, problemType, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	acmWriteJSON(w, status, map[string]any{
		"type":   "urn:ietf:params:acme:error:" + problemType,
		"detail": detail,
		"status": status,
	})
}

func handleACMEDataPlane(w http.ResponseWriter, r *http.Request) {
	endpointID := sim.PathParam(r, "endpoint")
	endpointArn := acmAcmeARN("endpoint", endpointID, "")
	endpoint, ok := acmAcmeEndpoints.Get(endpointArn)
	if !ok || endpoint.Status != "ACTIVE" {
		acmProblem(w, http.StatusNotFound, "malformed", "ACME endpoint not found")
		return
	}
	acmIssueNonce(w)
	resource := sim.PathParam(r, "resource")
	id := sim.PathParam(r, "id")
	if r.Method == http.MethodGet && resource == "" {
		acmWriteJSON(w, http.StatusOK, map[string]any{
			"newNonce":   acmACMEURL(r, "/new-nonce"),
			"newAccount": acmACMEURL(r, "/new-account"),
			"newOrder":   acmACMEURL(r, "/new-order"),
			"revokeCert": acmACMEURL(r, "/revoke-cert"),
			"keyChange":  acmACMEURL(r, "/key-change"),
			"meta":       map[string]any{"externalAccountRequired": true},
		})
		return
	}
	if resource == "new-nonce" && (r.Method == http.MethodHead || r.Method == http.MethodGet) {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		acmProblem(w, http.StatusMethodNotAllowed, "malformed", "ACME resources require POST-as-GET")
		return
	}
	jws, payload, account, publicJWK, err := acmVerifyJWS(
		r,
		endpointArn,
		resource == "new-account",
		resource == "revoke-cert",
	)
	if err != nil {
		acmProblem(w, http.StatusBadRequest, "malformed", err.Error())
		return
	}
	_ = jws
	switch resource {
	case "new-account":
		acmACMENewAccount(w, r, endpoint, payload, publicJWK)
	case "new-order":
		acmACMENewOrder(w, r, endpoint, account, payload)
	case "account":
		acmACMEAccount(w, account, payload)
	case "order":
		acmACMEOrderStatus(w, endpointID, id, account)
	case "authz":
		acmACMEAuthorizationStatus(w, endpointID, id, account, payload)
	case "finalize":
		acmACMEFinalize(w, r, endpoint, id, account, payload)
	case "certificate":
		acmACMECertificate(w, id, account)
	case "revoke-cert":
		acmACMERevokeCertificate(w, endpoint, account, publicJWK, payload)
	case "key-change":
		acmACMEKeyChange(w, r, account, payload)
	default:
		acmProblem(w, http.StatusNotFound, "malformed", "ACME resource not found")
	}
}

type acmJWS struct {
	Protected string `json:"protected"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type acmJWSProtected struct {
	Algorithm string          `json:"alg"`
	Nonce     string          `json:"nonce"`
	URL       string          `json:"url"`
	KeyID     string          `json:"kid"`
	JWK       json.RawMessage `json:"jwk"`
}

func acmVerifyJWS(r *http.Request, endpointArn string, requireJWK, allowEither bool) (acmJWS, []byte, acmAcmeAccount, json.RawMessage, error) {
	var envelope acmJWS
	if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
		return envelope, nil, acmAcmeAccount{}, nil, fmt.Errorf("invalid JWS: %w", err)
	}
	protectedBytes, err := base64.RawURLEncoding.DecodeString(envelope.Protected)
	if err != nil {
		return envelope, nil, acmAcmeAccount{}, nil, fmt.Errorf("invalid protected JWS")
	}
	var protected acmJWSProtected
	if err := json.Unmarshal(protectedBytes, &protected); err != nil {
		return envelope, nil, acmAcmeAccount{}, nil, fmt.Errorf("invalid protected JWS")
	}
	if protected.Nonce == "" || !acmAcmeNonces.Delete(protected.Nonce) {
		return envelope, nil, acmAcmeAccount{}, nil, fmt.Errorf("bad or replayed nonce")
	}
	if protected.URL != awsRequestURLBase(r)+r.URL.EscapedPath() {
		return envelope, nil, acmAcmeAccount{}, nil, fmt.Errorf("protected URL does not match request URL")
	}
	var account acmAcmeAccount
	jwk := protected.JWK
	if requireJWK {
		if len(jwk) == 0 || protected.KeyID != "" {
			return envelope, nil, account, nil, fmt.Errorf("JWS must carry jwk and no kid")
		}
	} else if allowEither && len(jwk) != 0 {
		if protected.KeyID != "" {
			return envelope, nil, account, nil, fmt.Errorf("JWS cannot carry both jwk and kid")
		}
	} else {
		if protected.KeyID == "" || len(jwk) != 0 {
			return envelope, nil, account, nil, fmt.Errorf("authenticated JWS must carry kid and no jwk")
		}
		var ok bool
		account, ok = acmAcmeAccounts.Get(protected.KeyID)
		if !ok || account.AcmeEndpointArn != endpointArn || account.Status != "VALID" {
			return envelope, nil, account, nil, fmt.Errorf("ACME account is not valid")
		}
		jwk = account.PublicJWK
	}
	publicKey, err := acmJWKPublicKey(jwk)
	if err != nil {
		return envelope, nil, account, nil, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil {
		return envelope, nil, account, nil, fmt.Errorf("invalid JWS signature encoding")
	}
	if err := acmVerifyJWSSignature(publicKey, protected.Algorithm, []byte(envelope.Protected+"."+envelope.Payload), signature); err != nil {
		return envelope, nil, account, nil, err
	}
	payload, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return envelope, nil, account, nil, fmt.Errorf("invalid JWS payload")
	}
	return envelope, payload, account, jwk, nil
}

func acmJWKPublicKey(raw json.RawMessage) (crypto.PublicKey, error) {
	var jwk struct {
		KeyType string `json:"kty"`
		Curve   string `json:"crv"`
		N       string `json:"n"`
		E       string `json:"e"`
		X       string `json:"x"`
		Y       string `json:"y"`
	}
	if err := json.Unmarshal(raw, &jwk); err != nil {
		return nil, fmt.Errorf("invalid JWK")
	}
	switch jwk.KeyType {
	case "RSA":
		nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
		if err != nil {
			return nil, fmt.Errorf("invalid RSA JWK modulus")
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
		if err != nil || len(eBytes) == 0 || len(eBytes) > 4 {
			return nil, fmt.Errorf("invalid RSA JWK exponent")
		}
		var e uint32
		for _, b := range eBytes {
			e = e<<8 | uint32(b)
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: int(e)}, nil
	case "EC":
		var curve elliptic.Curve
		switch jwk.Curve {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		default:
			return nil, fmt.Errorf("unsupported EC JWK curve")
		}
		x, errX := base64.RawURLEncoding.DecodeString(jwk.X)
		y, errY := base64.RawURLEncoding.DecodeString(jwk.Y)
		if errX != nil || errY != nil {
			return nil, fmt.Errorf("invalid EC JWK coordinates")
		}
		publicKey := &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !curve.IsOnCurve(publicKey.X, publicKey.Y) {
			return nil, fmt.Errorf("EC JWK point is not on curve")
		}
		return publicKey, nil
	default:
		return nil, fmt.Errorf("unsupported JWK key type")
	}
}

func acmVerifyJWSSignature(publicKey crypto.PublicKey, algorithm string, input, signature []byte) error {
	var digest []byte
	var hash crypto.Hash
	switch algorithm {
	case "RS256", "ES256":
		sum := sha256.Sum256(input)
		digest, hash = sum[:], crypto.SHA256
	case "ES384":
		sum := sha512.Sum384(input)
		digest, hash = sum[:], crypto.SHA384
	default:
		return fmt.Errorf("unsupported JWS algorithm %s", algorithm)
	}
	switch key := publicKey.(type) {
	case *rsa.PublicKey:
		if algorithm != "RS256" {
			return fmt.Errorf("JWS algorithm does not match RSA key")
		}
		if err := rsa.VerifyPKCS1v15(key, hash, digest, signature); err != nil {
			return fmt.Errorf("invalid JWS signature")
		}
	case *ecdsa.PublicKey:
		size := (key.Curve.Params().BitSize + 7) / 8
		if len(signature) != 2*size ||
			!ecdsa.Verify(key, digest, new(big.Int).SetBytes(signature[:size]), new(big.Int).SetBytes(signature[size:])) {
			return fmt.Errorf("invalid JWS signature")
		}
	default:
		return fmt.Errorf("unsupported JWS public key")
	}
	return nil
}

func acmJWKThumbprint(raw json.RawMessage) (string, error) {
	var fields map[string]string
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	var canonical []byte
	switch fields["kty"] {
	case "RSA":
		canonical, _ = json.Marshal(struct {
			E   string `json:"e"`
			KTY string `json:"kty"`
			N   string `json:"n"`
		}{fields["e"], "RSA", fields["n"]})
	case "EC":
		canonical, _ = json.Marshal(struct {
			CRV string `json:"crv"`
			KTY string `json:"kty"`
			X   string `json:"x"`
			Y   string `json:"y"`
		}{fields["crv"], "EC", fields["x"], fields["y"]})
	default:
		return "", fmt.Errorf("unsupported JWK")
	}
	sum := sha256.Sum256(canonical)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// The ACME data plane answers per-request lookups from generation-keyed
// indexes rather than decoding whole stores: accounts by their key
// thumbprint, external bindings by their key identifier, prevalidated
// domains by their base name, certificates by their ACME identifier. Each
// key carries the endpoint so one directory cannot answer for another.
var (
	acmAcmeAccountsByThumbprint    sim.GenerationIndex[acmAcmeAccount]
	acmAcmeBindingsByKeyID         sim.GenerationIndex[acmAcmeExternalAccountBinding]
	acmAcmeValidationsByBaseDomain sim.GenerationIndex[acmAcmeDomainValidation]
	acmCertificatesByAcmeID        sim.GenerationIndex[acmStoredCert]
)

func acmAcmeAccountThumbprintKey(endpointArn, thumbprint string) string {
	return endpointArn + "\x00" + thumbprint
}

func acmAcmeAccountKeys(account acmAcmeAccount) []string {
	return []string{acmAcmeAccountThumbprintKey(account.AcmeEndpointArn, account.PublicKeyThumbprint)}
}

func acmAcmeBindingKeyIDKey(endpointArn, keyID string) string {
	return endpointArn + "\x00" + keyID
}

func acmAcmeValidationBaseDomainKey(endpointArn, baseDomain string) string {
	return endpointArn + "\x00" + strings.ToLower(baseDomain)
}

func acmACMENewAccount(w http.ResponseWriter, r *http.Request, endpoint acmAcmeEndpoint, payload []byte, publicJWK json.RawMessage) {
	var req struct {
		Contact                []string `json:"contact"`
		OnlyReturnExisting     bool     `json:"onlyReturnExisting"`
		ExternalAccountBinding acmJWS   `json:"externalAccountBinding"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		acmProblem(w, http.StatusBadRequest, "malformed", "invalid new-account payload")
		return
	}
	thumbprint, err := acmJWKThumbprint(publicJWK)
	if err != nil {
		acmProblem(w, http.StatusBadRequest, "badPublicKey", err.Error())
		return
	}
	if existing, ok := acmAcmeAccountsByThumbprint.Lookup(acmAcmeAccounts,
		acmAcmeAccountThumbprintKey(endpoint.AcmeEndpointArn, thumbprint), acmAcmeAccountKeys); ok {
		w.Header().Set("Location", existing.AccountURL)
		acmWriteJSON(w, http.StatusOK, acmACMEAccountResponse(existing))
		return
	}
	if req.OnlyReturnExisting {
		acmProblem(w, http.StatusBadRequest, "accountDoesNotExist", "no account exists for this key")
		return
	}
	if endpoint.Contact == "REQUIRED" && len(req.Contact) == 0 {
		acmProblem(w, http.StatusBadRequest, "invalidContact", "the endpoint requires account contact information")
		return
	}
	binding, err := acmValidateExternalAccountBinding(req.ExternalAccountBinding, publicJWK, acmACMEURL(r, "/new-account"), endpoint.AcmeEndpointArn)
	if err != nil {
		acmProblem(w, http.StatusBadRequest, "unauthorized", err.Error())
		return
	}
	id := acmRandomID()
	accountURL := acmACMEURL(r, "/account/"+id)
	account := acmAcmeAccount{
		AccountURL:                    accountURL,
		AcmeEndpointArn:               endpoint.AcmeEndpointArn,
		AcmeExternalAccountBindingArn: binding.AcmeExternalAccountBindingArn,
		Contacts:                      req.Contact,
		CreatedAt:                     float64(time.Now().Unix()),
		PublicKeyThumbprint:           thumbprint,
		Status:                        "VALID",
		PublicJWK:                     append(json.RawMessage(nil), publicJWK...),
	}
	acmAcmeAccounts.Put(accountURL, account)
	now := float64(time.Now().Unix())
	binding.AccountURL = accountURL
	binding.LastUsedAt = &now
	binding.UpdatedAt = now
	acmAcmeBindings.Put(binding.AcmeExternalAccountBindingArn, binding)
	w.Header().Set("Location", accountURL)
	acmWriteJSON(w, http.StatusCreated, acmACMEAccountResponse(account))
}

func acmValidateExternalAccountBinding(envelope acmJWS, publicJWK json.RawMessage, expectedURL, endpointArn string) (acmAcmeExternalAccountBinding, error) {
	protectedBytes, err := base64.RawURLEncoding.DecodeString(envelope.Protected)
	if err != nil {
		return acmAcmeExternalAccountBinding{}, fmt.Errorf("invalid external account binding")
	}
	var protected acmJWSProtected
	if err := json.Unmarshal(protectedBytes, &protected); err != nil {
		return acmAcmeExternalAccountBinding{}, fmt.Errorf("invalid external account binding")
	}
	if protected.Algorithm != "HS256" || protected.KeyID == "" || protected.URL != expectedURL {
		return acmAcmeExternalAccountBinding{}, fmt.Errorf("external account binding protected header is invalid")
	}
	binding, found := acmAcmeBindingsByKeyID.Lookup(acmAcmeBindings,
		acmAcmeBindingKeyIDKey(endpointArn, protected.KeyID),
		func(candidate acmAcmeExternalAccountBinding) []string {
			return []string{acmAcmeBindingKeyIDKey(candidate.AcmeEndpointArn, candidate.KeyID)}
		})
	if !found || binding.RevokedAt != nil || binding.AccountURL != "" ||
		(binding.ExpiresAt != nil && *binding.ExpiresAt <= float64(time.Now().Unix())) {
		return acmAcmeExternalAccountBinding{}, fmt.Errorf("external account binding is not active")
	}
	key, err := base64.RawURLEncoding.DecodeString(binding.MACKey)
	if err != nil {
		return acmAcmeExternalAccountBinding{}, fmt.Errorf("external account binding key is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil {
		return acmAcmeExternalAccountBinding{}, fmt.Errorf("external account binding signature is invalid")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(envelope.Protected + "." + envelope.Payload))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return acmAcmeExternalAccountBinding{}, fmt.Errorf("external account binding signature is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil || !acmJSONEqual(payload, publicJWK) {
		return acmAcmeExternalAccountBinding{}, fmt.Errorf("external account binding key does not match account key")
	}
	return binding, nil
}

func acmJSONEqual(a, b []byte) bool {
	var av, bv any
	return json.Unmarshal(a, &av) == nil && json.Unmarshal(b, &bv) == nil &&
		reflect.DeepEqual(av, bv)
}

func acmACMEAccountResponse(account acmAcmeAccount) map[string]any {
	return map[string]any{"status": strings.ToLower(account.Status), "contact": account.Contacts}
}

func acmACMEAccount(w http.ResponseWriter, account acmAcmeAccount, payload []byte) {
	if len(payload) != 0 {
		var req struct {
			Contact []string `json:"contact"`
			Status  string   `json:"status"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			acmProblem(w, http.StatusBadRequest, "malformed", "invalid account update")
			return
		}
		if req.Contact != nil {
			account.Contacts = req.Contact
		}
		if req.Status == "deactivated" {
			account.Status = "DEACTIVATED"
		}
		acmAcmeAccounts.Put(account.AccountURL, account)
	}
	acmWriteJSON(w, http.StatusOK, acmACMEAccountResponse(account))
}

func acmACMENewOrder(w http.ResponseWriter, r *http.Request, endpoint acmAcmeEndpoint, account acmAcmeAccount, payload []byte) {
	var req struct {
		Identifiers []acmAcmeIdentifier `json:"identifiers"`
	}
	if err := json.Unmarshal(payload, &req); err != nil || len(req.Identifiers) == 0 {
		acmProblem(w, http.StatusBadRequest, "malformed", "order must contain identifiers")
		return
	}
	validations := make([]acmAcmeDomainValidation, 0, len(req.Identifiers))
	for _, identifier := range req.Identifiers {
		if identifier.Type != "dns" {
			acmProblem(w, http.StatusBadRequest, "unsupportedIdentifier", "only DNS identifiers are supported")
			return
		}
		validation, ok := acmFindAcmeValidation(endpoint.AcmeEndpointArn, identifier.Value)
		if !ok {
			acmProblem(w, http.StatusForbidden, "rejectedIdentifier", "no valid pre-approved domain validation covers "+identifier.Value)
			return
		}
		validations = append(validations, validation)
	}
	orderID := acmRandomID()
	order := acmAcmeOrder{
		ID:          orderID,
		EndpointID:  acmAcmeEndpointID(endpoint.AcmeEndpointArn),
		AccountURL:  account.AccountURL,
		Status:      "ready",
		Expires:     time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		Identifiers: req.Identifiers,
		Finalize:    acmACMEURL(r, "/finalize/"+orderID),
	}
	for i, identifier := range req.Identifiers {
		authzID := acmRandomID()
		authzURL := acmACMEURL(r, "/authz/"+authzID)
		order.Authorizations = append(order.Authorizations, authzURL)
		acmAcmeAuthorizations.Put(authzID, acmAcmeAuthorization{
			ID:            authzID,
			EndpointID:    order.EndpointID,
			AccountURL:    account.AccountURL,
			Identifier:    identifier,
			Status:        "valid",
			Expires:       order.Expires,
			Wildcard:      strings.HasPrefix(identifier.Value, "*."),
			ValidationArn: validations[i].AcmeDomainValidationArn,
		})
	}
	acmAcmeOrders.Put(orderID, order)
	w.Header().Set("Location", acmACMEURL(r, "/order/"+orderID))
	acmWriteJSON(w, http.StatusCreated, order)
}

func acmFindAcmeValidation(endpointArn, domain string) (acmAcmeDomainValidation, bool) {
	wildcard := strings.HasPrefix(domain, "*.")
	plain := strings.TrimPrefix(strings.ToLower(domain), "*.")
	// A validation answers on its base domain, so the queried name and each
	// of its parent suffixes are the only keys that can hold a match — the
	// same question the scan asked, in as many lookups as the name has
	// labels. Reconciliation still happens on read, on the rows the key
	// narrowed to rather than on every row the store holds.
	keysOf := func(candidate acmAcmeDomainValidation) []string {
		return []string{acmAcmeValidationBaseDomainKey(candidate.AcmeEndpointArn,
			strings.TrimPrefix(strings.ToLower(candidate.DomainName), "*."))}
	}
	for base := plain; base != ""; {
		for _, candidate := range acmAcmeValidationsByBaseDomain.LookupAll(acmAcmeDomainValidations,
			acmAcmeValidationBaseDomainKey(endpointArn, base), keysOf) {
			candidate = acmReconcileAcmeDomainValidation(candidate)
			if candidate.Status != "VALID" || candidate.PrevalidationDetails.DNSPrevalidation == nil {
				continue
			}
			scope := candidate.PrevalidationDetails.DNSPrevalidation.DomainScope
			switch {
			case wildcard && plain == base && scope.Wildcards == "ENABLED":
				return candidate, true
			case !wildcard && plain == base && scope.ExactDomain == "ENABLED":
				return candidate, true
			case !wildcard && plain != base && scope.Subdomains == "ENABLED":
				return candidate, true
			}
		}
		if i := strings.IndexByte(base, '.'); i >= 0 {
			base = base[i+1:]
		} else {
			base = ""
		}
	}
	return acmAcmeDomainValidation{}, false
}

func acmACMEOrderStatus(w http.ResponseWriter, endpointID, id string, account acmAcmeAccount) {
	order, ok := acmAcmeOrders.Get(id)
	if !ok || order.EndpointID != endpointID || order.AccountURL != account.AccountURL {
		acmProblem(w, http.StatusNotFound, "malformed", "order not found")
		return
	}
	acmWriteJSON(w, http.StatusOK, order)
}

func acmACMEAuthorizationStatus(w http.ResponseWriter, endpointID, id string, account acmAcmeAccount, payload []byte) {
	authorization, ok := acmAcmeAuthorizations.Get(id)
	if !ok || authorization.EndpointID != endpointID || authorization.AccountURL != account.AccountURL {
		acmProblem(w, http.StatusNotFound, "malformed", "authorization not found")
		return
	}
	if len(payload) != 0 {
		var req struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(payload, &req) == nil && req.Status == "deactivated" {
			authorization.Status = "deactivated"
			acmAcmeAuthorizations.Put(id, authorization)
		}
	}
	acmWriteJSON(w, http.StatusOK, authorization)
}

func acmACMEFinalize(w http.ResponseWriter, r *http.Request, endpoint acmAcmeEndpoint, orderID string, account acmAcmeAccount, payload []byte) {
	order, ok := acmAcmeOrders.Get(orderID)
	if !ok || order.EndpointID != acmAcmeEndpointID(endpoint.AcmeEndpointArn) || order.AccountURL != account.AccountURL {
		acmProblem(w, http.StatusNotFound, "malformed", "order not found")
		return
	}
	if order.Status != "ready" {
		acmProblem(w, http.StatusForbidden, "orderNotReady", "order is not ready for finalization")
		return
	}
	var req struct {
		CSR string `json:"csr"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		acmProblem(w, http.StatusBadRequest, "badCSR", "invalid CSR request")
		return
	}
	csrDER, err := base64.RawURLEncoding.DecodeString(req.CSR)
	if err != nil {
		acmProblem(w, http.StatusBadRequest, "badCSR", "invalid CSR encoding")
		return
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil || csr.CheckSignature() != nil {
		acmProblem(w, http.StatusBadRequest, "badCSR", "CSR signature is invalid")
		return
	}
	if !acmCSRMatchesOrder(csr, order) {
		acmProblem(w, http.StatusBadRequest, "badCSR", "CSR identifiers do not match the order")
		return
	}
	if !acmACMEPublicKeyAllowed(endpoint, csr.PublicKey) {
		acmProblem(w, http.StatusBadRequest, "badPublicKey", "CSR key algorithm is not allowed by the endpoint")
		return
	}
	leafDER, chainPEM, err := acmIssueAcmeCertificate(endpoint, account, csr)
	if err != nil {
		acmProblem(w, http.StatusInternalServerError, "serverInternal", err.Error())
		return
	}
	certID := acmRandomID()
	certArn := acmCertARN(certID)
	leaf, _ := x509.ParseCertificate(leafDER)
	now := float64(time.Now().Unix())
	notBefore := float64(leaf.NotBefore.Unix())
	notAfter := float64(leaf.NotAfter.Unix())
	keyAlgorithm := "RSA_2048"
	if key, ok := csr.PublicKey.(*ecdsa.PublicKey); ok {
		if key.Curve == elliptic.P384() {
			keyAlgorithm = "EC_secp384r1"
		} else {
			keyAlgorithm = "EC_prime256v1"
		}
	}
	acmCertificates.Put(certID, acmStoredCert{
		Cert: ACMCertificate{
			CertificateArn:           certArn,
			DomainName:               order.Identifiers[0].Value,
			SubjectAlternativeNames:  acmOrderDomains(order),
			Status:                   "ISSUED",
			IssuedAt:                 &now,
			NotBefore:                &notBefore,
			NotAfter:                 &notAfter,
			KeyAlgorithm:             keyAlgorithm,
			SignatureAlgorithm:       "SHA256WITHRSA",
			InUseBy:                  []string{},
			Type:                     "AMAZON_ISSUED",
			RenewalEligibility:       "INELIGIBLE",
			CreatedAt:                &now,
			Serial:                   leaf.SerialNumber.Text(16),
			Subject:                  leaf.Subject.String(),
			Issuer:                   leaf.Issuer.String(),
			AcmeAccountID:            account.PublicKeyThumbprint,
			AcmeEndpointArn:          endpoint.AcmeEndpointArn,
			CertificateKeyPairOrigin: "ACME",
		},
		CertificateBody:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})),
		CertificateChain: chainPEM,
	})
	order.Status = "valid"
	order.Certificate = acmACMEURL(r, "/certificate/"+orderID)
	order.CertificateArn = certArn
	acmAcmeOrders.Put(orderID, order)
	acmWriteJSON(w, http.StatusOK, order)
}

func acmCSRMatchesOrder(csr *x509.CertificateRequest, order acmAcmeOrder) bool {
	got := append([]string(nil), csr.DNSNames...)
	if len(got) == 0 && csr.Subject.CommonName != "" {
		got = append(got, csr.Subject.CommonName)
	}
	want := acmOrderDomains(order)
	sort.Strings(got)
	sort.Strings(want)
	return len(got) == len(want) && strings.EqualFold(strings.Join(got, "\x00"), strings.Join(want, "\x00"))
}

func acmOrderDomains(order acmAcmeOrder) []string {
	result := make([]string, 0, len(order.Identifiers))
	for _, identifier := range order.Identifiers {
		result = append(result, identifier.Value)
	}
	return result
}

func acmACMEPublicKeyAllowed(endpoint acmAcmeEndpoint, publicKey any) bool {
	algorithm := ""
	switch key := publicKey.(type) {
	case *rsa.PublicKey:
		if key.N.BitLen() == 2048 {
			algorithm = "RSA_2048"
		}
	case *ecdsa.PublicKey:
		switch key.Curve {
		case elliptic.P256():
			algorithm = "EC_prime256v1"
		case elliptic.P384():
			algorithm = "EC_secp384r1"
		}
	}
	if endpoint.CertificateAuthority.PublicCertificateAuthority == nil {
		return false
	}
	for _, allowed := range endpoint.CertificateAuthority.PublicCertificateAuthority.AllowedKeyAlgorithms {
		if allowed == algorithm {
			return true
		}
	}
	return false
}

func acmIssueAcmeCertificate(endpoint acmAcmeEndpoint, account acmAcmeAccount, csr *x509.CertificateRequest) ([]byte, string, error) {
	endpointID := acmAcmeEndpointID(endpoint.AcmeEndpointArn)
	caState, ok := acmAcmeCAs.Get(endpointID)
	if !ok {
		return nil, "", fmt.Errorf("ACME certificate authority is unavailable")
	}
	root, err := x509.ParseCertificate(caState.CertificateDER)
	if err != nil {
		return nil, "", err
	}
	block, _ := pem.Decode(caState.PrivateKeyPEM)
	if block == nil {
		return nil, "", fmt.Errorf("ACME certificate authority private key is invalid")
	}
	privateKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, "", err
	}
	signer, ok := privateKey.(crypto.Signer)
	if !ok {
		return nil, "", fmt.Errorf("ACME certificate authority key cannot sign")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:    serial,
		Subject:         csr.Subject,
		DNSNames:        append([]string(nil), csr.DNSNames...),
		NotBefore:       now.Add(-5 * time.Minute),
		NotAfter:        now.AddDate(0, 3, 0),
		KeyUsage:        x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		ExtraExtensions: append([]pkix.Extension(nil), csr.Extensions...),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, root, csr.PublicKey, signer)
	if err != nil {
		return nil, "", err
	}
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caState.CertificateDER})
	_ = account
	return der, string(chain), nil
}

func acmACMECertificate(w http.ResponseWriter, orderID string, account acmAcmeAccount) {
	order, ok := acmAcmeOrders.Get(orderID)
	if !ok || order.AccountURL != account.AccountURL || order.Status != "valid" {
		acmProblem(w, http.StatusNotFound, "malformed", "certificate not found")
		return
	}
	stored, ok := acmCertificates.Get(acmARNToID(order.CertificateArn))
	if !ok {
		acmProblem(w, http.StatusNotFound, "malformed", "certificate not found")
		return
	}
	w.Header().Set("Content-Type", "application/pem-certificate-chain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(stored.CertificateBody))
	_, _ = w.Write([]byte(stored.CertificateChain))
}

func acmACMERevokeCertificate(w http.ResponseWriter, endpoint acmAcmeEndpoint, account acmAcmeAccount, requestJWK json.RawMessage, payload []byte) {
	var req struct {
		Certificate string `json:"certificate"`
		Reason      int    `json:"reason"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		acmProblem(w, http.StatusBadRequest, "malformed", "invalid revocation request")
		return
	}
	der, err := base64.RawURLEncoding.DecodeString(req.Certificate)
	if err != nil {
		acmProblem(w, http.StatusBadRequest, "malformed", "invalid certificate encoding")
		return
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		acmProblem(w, http.StatusBadRequest, "malformed", "invalid certificate")
		return
	}
	for _, stored := range acmCertificatesByAcmeID.LookupAll(acmCertificates,
		acmCertificateDERKey(certificate.Raw), acmCertificateDERKeys) {
		id := acmARNToID(stored.Cert.CertificateArn)
		block, _ := pem.Decode([]byte(stored.CertificateBody))
		if block == nil || !bytes.Equal(block.Bytes, certificate.Raw) ||
			stored.Cert.AcmeEndpointArn != endpoint.AcmeEndpointArn {
			continue
		}
		authorized := account.AccountURL != "" && stored.Cert.AcmeAccountID == account.PublicKeyThumbprint
		if !authorized && len(requestJWK) != 0 {
			requestKey, keyErr := acmJWKPublicKey(requestJWK)
			requestDER, marshalErr := x509.MarshalPKIXPublicKey(requestKey)
			certificateDER, certificateErr := x509.MarshalPKIXPublicKey(certificate.PublicKey)
			authorized = keyErr == nil && marshalErr == nil && certificateErr == nil && bytes.Equal(requestDER, certificateDER)
		}
		if !authorized {
			acmProblem(w, http.StatusForbidden, "unauthorized", "the signing key is not authorized to revoke this certificate")
			return
		}
		now := float64(time.Now().Unix())
		stored.Cert.Status = "REVOKED"
		stored.RevokedAt = &now
		acmCertificates.Put(id, stored)
		w.WriteHeader(http.StatusOK)
		return
	}
	acmProblem(w, http.StatusNotFound, "malformed", "certificate not found")
}

func acmACMEKeyChange(w http.ResponseWriter, r *http.Request, account acmAcmeAccount, payload []byte) {
	var inner acmJWS
	if err := json.Unmarshal(payload, &inner); err != nil {
		acmProblem(w, http.StatusBadRequest, "malformed", "keyChange nested JWS is invalid")
		return
	}
	protectedBytes, err := base64.RawURLEncoding.DecodeString(inner.Protected)
	if err != nil {
		acmProblem(w, http.StatusBadRequest, "malformed", "keyChange protected header is invalid")
		return
	}
	var protected acmJWSProtected
	if err := json.Unmarshal(protectedBytes, &protected); err != nil ||
		len(protected.JWK) == 0 || protected.KeyID != "" || protected.Nonce != "" ||
		protected.URL != awsRequestURLBase(r)+r.URL.EscapedPath() {
		acmProblem(w, http.StatusBadRequest, "malformed", "keyChange protected header is invalid")
		return
	}
	newKey, err := acmJWKPublicKey(protected.JWK)
	if err != nil {
		acmProblem(w, http.StatusBadRequest, "badPublicKey", err.Error())
		return
	}
	signature, err := base64.RawURLEncoding.DecodeString(inner.Signature)
	if err != nil || acmVerifyJWSSignature(newKey, protected.Algorithm,
		[]byte(inner.Protected+"."+inner.Payload), signature) != nil {
		acmProblem(w, http.StatusBadRequest, "malformed", "keyChange nested JWS signature is invalid")
		return
	}
	innerPayload, err := base64.RawURLEncoding.DecodeString(inner.Payload)
	if err != nil {
		acmProblem(w, http.StatusBadRequest, "malformed", "keyChange nested payload is invalid")
		return
	}
	var request struct {
		Account string          `json:"account"`
		OldKey  json.RawMessage `json:"oldKey"`
	}
	if err := json.Unmarshal(innerPayload, &request); err != nil ||
		request.Account != account.AccountURL || !acmJSONEqual(request.OldKey, account.PublicJWK) {
		acmProblem(w, http.StatusBadRequest, "malformed", "keyChange old key or account does not match")
		return
	}
	thumbprint, err := acmJWKThumbprint(protected.JWK)
	if err != nil {
		acmProblem(w, http.StatusBadRequest, "badPublicKey", err.Error())
		return
	}
	for _, candidate := range acmAcmeAccountsByThumbprint.LookupAll(acmAcmeAccounts,
		acmAcmeAccountThumbprintKey(account.AcmeEndpointArn, thumbprint), acmAcmeAccountKeys) {
		if candidate.AccountURL != account.AccountURL {
			acmProblem(w, http.StatusConflict, "malformed", "the new key already belongs to another account")
			return
		}
	}
	account.PublicJWK = append(json.RawMessage(nil), protected.JWK...)
	account.PublicKeyThumbprint = thumbprint
	acmAcmeAccounts.Put(account.AccountURL, account)
	acmWriteJSON(w, http.StatusOK, struct{}{})
}

// acmCertificateDERKey names a certificate by the digest of its leaf DER —
// the identity a revocation request presents.
func acmCertificateDERKey(der []byte) string {
	digest := sha256.Sum256(der)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func acmCertificateDERKeys(stored acmStoredCert) []string {
	block, _ := pem.Decode([]byte(stored.CertificateBody))
	if block == nil {
		return nil
	}
	return []string{acmCertificateDERKey(block.Bytes)}
}
