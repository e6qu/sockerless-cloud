package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// AWS Amplify domain associations + backend environments. Same wire
// pattern as amplify.go (REST + JSON, versionless paths under /apps/).

// AmplifyDomainStatus / AmplifyDomainUpdateStatus are the DomainStatus and
// UpdateStatus enum subsets the sim assigns. Domain verification is REAL
// against the sim's own Route 53: an AMPLIFY_MANAGED association starts
// PENDING_VERIFICATION and becomes AVAILABLE only once the
// certificate-verification CNAME exists in a hosted zone covering the
// domain. Verification is evaluated at READ time (Get/List/Update and the
// hosting data plane) — terraform's wait_for_verification polls
// GetDomainAssociation, so read-triggered evaluation converges, and a
// domain with no hosted zone honestly stays PENDING_VERIFICATION forever.
type AmplifyDomainStatus string

const (
	AmplifyDomainStatusAvailable           AmplifyDomainStatus = "AVAILABLE"
	AmplifyDomainStatusPendingVerification AmplifyDomainStatus = "PENDING_VERIFICATION"
)

type AmplifyDomainUpdateStatus string

const (
	AmplifyDomainUpdateComplete            AmplifyDomainUpdateStatus = "UPDATE_COMPLETE"
	AmplifyDomainUpdatePendingVerification AmplifyDomainUpdateStatus = "PENDING_VERIFICATION"
)

type AmplifyDomainAssociation struct {
	DomainAssociationArn             string                    `json:"domainAssociationArn"`
	DomainName                       string                    `json:"domainName"`
	EnableAutoSubDomain              bool                      `json:"enableAutoSubDomain"`
	AutoSubDomainCreationPatterns    []string                  `json:"autoSubDomainCreationPatterns,omitempty"`
	AutoSubDomainIamRole             string                    `json:"autoSubDomainIAMRole,omitempty"`
	DomainStatus                     AmplifyDomainStatus       `json:"domainStatus"`
	UpdateStatus                     AmplifyDomainUpdateStatus `json:"updateStatus"`
	StatusReason                     string                    `json:"statusReason,omitempty"`
	Certificate                      *AmplifyCertificate       `json:"certificate,omitempty"`
	CertificateVerificationDNSRecord string                    `json:"certificateVerificationDNSRecord,omitempty"`
	SubDomains                       []AmplifySubDomain        `json:"subDomains"`
}

type AmplifyCertificate struct {
	Type                             string `json:"type"`
	CustomCertificateArn             string `json:"customCertificateArn,omitempty"`
	CertificateVerificationDNSRecord string `json:"certificateVerificationDNSRecord,omitempty"`
}

// AmplifyCertificateSettings is the CertificateSettings request shape
// (create/update input for the read-only Certificate member).
type AmplifyCertificateSettings struct {
	Type                 string `json:"type"`
	CustomCertificateArn string `json:"customCertificateArn,omitempty"`
}

type AmplifySubDomain struct {
	SubDomainSetting AmplifySubDomainSetting `json:"subDomainSetting"`
	Verified         bool                    `json:"verified"`
	DnsRecord        string                  `json:"dnsRecord"`
}

type AmplifySubDomainSetting struct {
	Prefix     string `json:"prefix"`
	BranchName string `json:"branchName"`
}

type amplifyStoredDomain struct {
	Domain AmplifyDomainAssociation
	AppId  string
}

type AmplifyBackendEnvironment struct {
	BackendEnvironmentArn string  `json:"backendEnvironmentArn"`
	EnvironmentName       string  `json:"environmentName"`
	StackName             string  `json:"stackName,omitempty"`
	DeploymentArtifacts   string  `json:"deploymentArtifacts,omitempty"`
	CreateTime            float64 `json:"createTime"`
	UpdateTime            float64 `json:"updateTime"`
}

type amplifyStoredBackend struct {
	Env   AmplifyBackendEnvironment
	AppId string
}

var (
	amplifyDomains  sim.Store[amplifyStoredDomain]
	amplifyBackends sim.Store[amplifyStoredBackend]
)

func amplifyDomainARN(appID, domain string) string {
	return fmt.Sprintf("arn:aws:amplify:%s:%s:apps/%s/domains/%s", awsRegion(), awsAccountID(), appID, domain)
}
func amplifyBackendARN(appID, name string) string {
	return fmt.Sprintf("arn:aws:amplify:%s:%s:apps/%s/backendenvironments/%s", awsRegion(), awsAccountID(), appID, name)
}
func amplifyDomainKey(appID, name string) string { return appID + "/" + name }

// registerAmplifyDomains is invoked from registerAmplify in amplify.go.
func registerAmplifyDomains(srv *sim.Server) {
	amplifyDomains = sim.MakeStore[amplifyStoredDomain](srv.DB(), "amplify_domains")
	amplifyBackends = sim.MakeStore[amplifyStoredBackend](srv.DB(), "amplify_backends")

	mux := srv
	domainResource := cloudTrailRESTResource("AWS::Amplify::Domain", "domainName")
	backendResource := cloudTrailRESTResource("AWS::Amplify::BackendEnvironment", "environmentName")
	// Domains
	mux.HandleFunc("POST /apps/{appId}/domains", cloudTrailRecordedREST("CreateDomainAssociation", "amplify.amazonaws.com", cloudTrailRESTResource("AWS::Amplify::App", "appId"), handleAmplifyCreateDomain))
	mux.HandleFunc("GET /apps/{appId}/domains", cloudTrailRecordedREST("ListDomainAssociations", "amplify.amazonaws.com", cloudTrailRESTResource("AWS::Amplify::App", "appId"), handleAmplifyListDomains))
	mux.HandleFunc("GET /apps/{appId}/domains/{domainName}", cloudTrailRecordedREST("GetDomainAssociation", "amplify.amazonaws.com", domainResource, handleAmplifyGetDomain))
	mux.HandleFunc("POST /apps/{appId}/domains/{domainName}", cloudTrailRecordedREST("UpdateDomainAssociation", "amplify.amazonaws.com", domainResource, handleAmplifyUpdateDomain))
	mux.HandleFunc("DELETE /apps/{appId}/domains/{domainName}", cloudTrailRecordedREST("DeleteDomainAssociation", "amplify.amazonaws.com", domainResource, handleAmplifyDeleteDomain))
	// BackendEnvironments
	mux.HandleFunc("POST /apps/{appId}/backendenvironments", cloudTrailRecordedREST("CreateBackendEnvironment", "amplify.amazonaws.com", cloudTrailRESTResource("AWS::Amplify::App", "appId"), handleAmplifyCreateBackend))
	mux.HandleFunc("GET /apps/{appId}/backendenvironments", cloudTrailRecordedREST("ListBackendEnvironments", "amplify.amazonaws.com", cloudTrailRESTResource("AWS::Amplify::App", "appId"), handleAmplifyListBackends))
	mux.HandleFunc("GET /apps/{appId}/backendenvironments/{environmentName}", cloudTrailRecordedREST("GetBackendEnvironment", "amplify.amazonaws.com", backendResource, handleAmplifyGetBackend))
	mux.HandleFunc("DELETE /apps/{appId}/backendenvironments/{environmentName}", cloudTrailRecordedREST("DeleteBackendEnvironment", "amplify.amazonaws.com", backendResource, handleAmplifyDeleteBackend))
}

type amplifyCreateDomainReq struct {
	DomainName                    string                      `json:"domainName"`
	EnableAutoSubDomain           *bool                       `json:"enableAutoSubDomain,omitempty"`
	SubDomainSettings             []AmplifySubDomainSetting   `json:"subDomainSettings,omitempty"`
	AutoSubDomainCreationPatterns []string                    `json:"autoSubDomainCreationPatterns,omitempty"`
	AutoSubDomainIamRole          string                      `json:"autoSubDomainIAMRole,omitempty"`
	CertificateSettings           *AmplifyCertificateSettings `json:"certificateSettings,omitempty"`
}

// amplifyCertificateFromSettings materializes the read-only Certificate
// member from the client's CertificateSettings: nil settings mean an
// Amplify-managed certificate, and managed certificates carry the
// verification DNS record that custom certificates don't need.
func amplifyCertificateFromSettings(settings *AmplifyCertificateSettings, appID, domainName string) *AmplifyCertificate {
	cert := &AmplifyCertificate{Type: "AMPLIFY_MANAGED"}
	if settings != nil && settings.Type != "" {
		cert.Type = settings.Type
		cert.CustomCertificateArn = settings.CustomCertificateArn
	}
	if cert.Type == "AMPLIFY_MANAGED" {
		cert.CertificateVerificationDNSRecord = amplifyCertVerificationRecord(appID, domainName)
	}
	return cert
}

// amplifyCertVerificationRecord is the ACM-style verification CNAME the
// association advertises: "_<hash>.<domain>. CNAME _<hash>.acm-validations.aws."
// The hash is deterministic per app + domain so read-time verification can
// recompute exactly what was advertised.
func amplifyCertVerificationRecord(appID, domainName string) string {
	name, value := amplifyCertVerificationParts(appID, domainName)
	return name + " CNAME " + value
}

func amplifyCertVerificationParts(appID, domainName string) (name, value string) {
	hash := md5.Sum([]byte("amplify-cert-verification/" + appID + "/" + strings.ToLower(domainName)))
	token := hex.EncodeToString(hash[:])
	return "_" + token + "." + domainName + ".", "_" + token + ".acm-validations.aws."
}

func amplifySubDomainsFromSettings(appID, domainName string, settings []AmplifySubDomainSetting, verified bool) []AmplifySubDomain {
	subs := make([]AmplifySubDomain, 0, len(settings))
	for _, s := range settings {
		subs = append(subs, AmplifySubDomain{
			SubDomainSetting: s,
			Verified:         verified,
			DnsRecord:        s.Prefix + " CNAME " + amplifyCloudFrontDomain(appID) + ".",
		})
	}
	return subs
}

// amplifyEvaluateDomainVerification settles a pending association against
// the sim's own Route 53: AMPLIFY_MANAGED certificates verify when the
// advertised certificate-verification CNAME exists in a hosted zone for the
// domain; CUSTOM certificates have no DNS challenge to wait on and settle
// immediately. The settled state is persisted so the flip is observed once.
func amplifyEvaluateDomainVerification(stored amplifyStoredDomain) amplifyStoredDomain {
	if stored.Domain.DomainStatus == AmplifyDomainStatusAvailable {
		return stored
	}
	var verified bool
	if stored.Domain.Certificate != nil && stored.Domain.Certificate.Type == "CUSTOM" {
		verified = true
	} else {
		name, value := amplifyCertVerificationParts(stored.AppId, stored.Domain.DomainName)
		verified = amplifyRoute53HasCNAME(stored.Domain.DomainName, name, value)
	}
	if !verified {
		return stored
	}
	stored.Domain.DomainStatus = AmplifyDomainStatusAvailable
	stored.Domain.UpdateStatus = AmplifyDomainUpdateComplete
	for i := range stored.Domain.SubDomains {
		stored.Domain.SubDomains[i].Verified = true
	}
	amplifyDomains.Put(amplifyDomainKey(stored.AppId, stored.Domain.DomainName), stored)
	return stored
}

// amplifyRoute53HasCNAME reports whether any sim hosted zone covering
// domainName carries the CNAME recordName → recordValue.
func amplifyRoute53HasCNAME(domainName, recordName, recordValue string) bool {
	normalize := r53DNSName
	wantName, wantValue := normalize(recordName), normalize(recordValue)
	domain := normalize(domainName)
	// Only a zone carrying the record can answer, and the zone must also cover
	// the domain, which the loop still decides.
	for _, zone := range r53ZonesWithCNAME(wantName) {
		zoneName := normalize(zone.Zone.Name)
		if zoneName == "" || (domain != zoneName && !strings.HasSuffix(domain, "."+zoneName)) {
			continue
		}
		for _, rr := range zone.Records {
			if !strings.EqualFold(rr.Type, "CNAME") || normalize(rr.Name) != wantName || rr.ResourceRecords == nil {
				continue
			}
			for _, value := range rr.ResourceRecords.Items {
				if normalize(value.Value) == wantValue {
					return true
				}
			}
		}
	}
	return false
}

func handleAmplifyCreateDomain(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("appId")
	if _, ok := amplifyApps.Get(appID); !ok {
		amplifyWriteError(w, http.StatusNotFound, "NotFoundException", "app not found")
		return
	}
	var req amplifyCreateDomainReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		amplifyWriteError(w, http.StatusBadRequest, "BadRequestException", "could not decode: "+err.Error())
		return
	}
	if req.DomainName == "" {
		amplifyWriteError(w, http.StatusBadRequest, "BadRequestException", "domainName is required")
		return
	}
	domain := AmplifyDomainAssociation{
		DomainAssociationArn:             amplifyDomainARN(appID, req.DomainName),
		DomainName:                       req.DomainName,
		EnableAutoSubDomain:              boolOr(req.EnableAutoSubDomain, false),
		AutoSubDomainCreationPatterns:    req.AutoSubDomainCreationPatterns,
		AutoSubDomainIamRole:             req.AutoSubDomainIamRole,
		DomainStatus:                     AmplifyDomainStatusPendingVerification,
		UpdateStatus:                     AmplifyDomainUpdatePendingVerification,
		Certificate:                      amplifyCertificateFromSettings(req.CertificateSettings, appID, req.DomainName),
		CertificateVerificationDNSRecord: amplifyCertVerificationRecord(appID, req.DomainName),
		SubDomains:                       amplifySubDomainsFromSettings(appID, req.DomainName, req.SubDomainSettings, false),
	}
	stored := amplifyStoredDomain{Domain: domain, AppId: appID}
	amplifyDomains.Put(amplifyDomainKey(appID, req.DomainName), stored)
	// The verification CNAME may already exist (re-association of a domain
	// whose records were kept); settle immediately in that case.
	stored = amplifyEvaluateDomainVerification(stored)
	amplifyWriteJSON(w, http.StatusOK, map[string]AmplifyDomainAssociation{"domainAssociation": stored.Domain})
}

func handleAmplifyGetDomain(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("appId")
	name := r.PathValue("domainName")
	stored, ok := amplifyDomains.Get(amplifyDomainKey(appID, name))
	if !ok {
		amplifyWriteError(w, http.StatusNotFound, "NotFoundException", "domain not found")
		return
	}
	stored = amplifyEvaluateDomainVerification(stored)
	amplifyWriteJSON(w, http.StatusOK, map[string]AmplifyDomainAssociation{"domainAssociation": stored.Domain})
}

func handleAmplifyUpdateDomain(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("appId")
	name := r.PathValue("domainName")
	key := amplifyDomainKey(appID, name)
	stored, ok := amplifyDomains.Get(key)
	if !ok {
		amplifyWriteError(w, http.StatusNotFound, "NotFoundException", "domain not found")
		return
	}
	var req amplifyCreateDomainReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		amplifyWriteError(w, http.StatusBadRequest, "BadRequestException", "could not decode: "+err.Error())
		return
	}
	if req.EnableAutoSubDomain != nil {
		stored.Domain.EnableAutoSubDomain = *req.EnableAutoSubDomain
	}
	if req.AutoSubDomainCreationPatterns != nil {
		stored.Domain.AutoSubDomainCreationPatterns = req.AutoSubDomainCreationPatterns
	}
	if req.AutoSubDomainIamRole != "" {
		stored.Domain.AutoSubDomainIamRole = req.AutoSubDomainIamRole
	}
	if req.SubDomainSettings != nil {
		stored.Domain.SubDomains = amplifySubDomainsFromSettings(appID, stored.Domain.DomainName, req.SubDomainSettings,
			stored.Domain.DomainStatus == AmplifyDomainStatusAvailable)
	}
	if req.CertificateSettings != nil {
		stored.Domain.Certificate = amplifyCertificateFromSettings(req.CertificateSettings, appID, stored.Domain.DomainName)
	}
	if stored.Domain.DomainStatus == AmplifyDomainStatusAvailable {
		stored.Domain.UpdateStatus = AmplifyDomainUpdateComplete
	}
	amplifyDomains.Put(key, stored)
	stored = amplifyEvaluateDomainVerification(stored)
	amplifyWriteJSON(w, http.StatusOK, map[string]AmplifyDomainAssociation{"domainAssociation": stored.Domain})
}

func handleAmplifyDeleteDomain(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("appId")
	name := r.PathValue("domainName")
	key := amplifyDomainKey(appID, name)
	stored, ok := amplifyDomains.Get(key)
	if !ok {
		amplifyWriteError(w, http.StatusNotFound, "NotFoundException", "domain not found")
		return
	}
	amplifyDomains.Delete(key)
	amplifyWriteJSON(w, http.StatusOK, map[string]AmplifyDomainAssociation{"domainAssociation": stored.Domain})
}

func handleAmplifyListDomains(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("appId")
	if _, ok := amplifyApps.Get(appID); !ok {
		amplifyWriteError(w, http.StatusNotFound, "NotFoundException", "app not found")
		return
	}
	token, maxResults, ok := amplifyPageQuery(w, r)
	if !ok {
		return
	}
	items := []AmplifyDomainAssociation{}
	for _, s := range amplifyDomains.List() {
		if s.AppId == appID {
			items = append(items, amplifyEvaluateDomainVerification(s).Domain)
		}
	}
	sortBy(items, func(d AmplifyDomainAssociation) string { return d.DomainName })
	amplifyWriteListPage(w, "domainAssociations", items, token, maxResults)
}

type amplifyCreateBackendReq struct {
	EnvironmentName     string `json:"environmentName"`
	StackName           string `json:"stackName,omitempty"`
	DeploymentArtifacts string `json:"deploymentArtifacts,omitempty"`
}

func handleAmplifyCreateBackend(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("appId")
	if _, ok := amplifyApps.Get(appID); !ok {
		amplifyWriteError(w, http.StatusNotFound, "NotFoundException", "app not found")
		return
	}
	var req amplifyCreateBackendReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		amplifyWriteError(w, http.StatusBadRequest, "BadRequestException", "could not decode: "+err.Error())
		return
	}
	if req.EnvironmentName == "" {
		amplifyWriteError(w, http.StatusBadRequest, "BadRequestException", "environmentName is required")
		return
	}
	now := amplifyEpoch()
	env := AmplifyBackendEnvironment{
		BackendEnvironmentArn: amplifyBackendARN(appID, req.EnvironmentName),
		EnvironmentName:       req.EnvironmentName,
		StackName:             req.StackName,
		DeploymentArtifacts:   req.DeploymentArtifacts,
		CreateTime:            now,
		UpdateTime:            now,
	}
	amplifyBackends.Put(amplifyDomainKey(appID, req.EnvironmentName), amplifyStoredBackend{Env: env, AppId: appID})
	amplifyWriteJSON(w, http.StatusOK, map[string]AmplifyBackendEnvironment{"backendEnvironment": env})
}

func handleAmplifyGetBackend(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("appId")
	name := r.PathValue("environmentName")
	stored, ok := amplifyBackends.Get(amplifyDomainKey(appID, name))
	if !ok {
		amplifyWriteError(w, http.StatusNotFound, "NotFoundException", "backend environment not found")
		return
	}
	amplifyWriteJSON(w, http.StatusOK, map[string]AmplifyBackendEnvironment{"backendEnvironment": stored.Env})
}

func handleAmplifyDeleteBackend(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("appId")
	name := r.PathValue("environmentName")
	key := amplifyDomainKey(appID, name)
	stored, ok := amplifyBackends.Get(key)
	if !ok {
		amplifyWriteError(w, http.StatusNotFound, "NotFoundException", "backend environment not found")
		return
	}
	amplifyBackends.Delete(key)
	amplifyWriteJSON(w, http.StatusOK, map[string]AmplifyBackendEnvironment{"backendEnvironment": stored.Env})
}

func handleAmplifyListBackends(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("appId")
	if _, ok := amplifyApps.Get(appID); !ok {
		amplifyWriteError(w, http.StatusNotFound, "NotFoundException", "app not found")
		return
	}
	token, maxResults, ok := amplifyPageQuery(w, r)
	if !ok {
		return
	}
	// ListBackendEnvironments additionally filters by ?environmentName=.
	nameFilter := r.URL.Query().Get("environmentName")
	items := []AmplifyBackendEnvironment{}
	for _, s := range amplifyBackends.List() {
		if s.AppId == appID && (nameFilter == "" || s.Env.EnvironmentName == nameFilter) {
			items = append(items, s.Env)
		}
	}
	sortBy(items, func(e AmplifyBackendEnvironment) string { return e.EnvironmentName })
	amplifyWriteListPage(w, "backendEnvironments", items, token, maxResults)
}
