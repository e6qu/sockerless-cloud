package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CloudFront extra resources — origin access identities (the legacy
// pre-OAC identity), continuous deployment policies, and per-distribution
// monitoring subscriptions. Same REST + XML wire as the Distribution and
// policy endpoints in cloudfront.go / cloudfront_policies.go.

type CFOriginAccessIdentityConfig struct {
	XMLName         xml.Name `xml:"CloudFrontOriginAccessIdentityConfig"`
	Xmlns           string   `xml:"xmlns,attr,omitempty"`
	CallerReference string   `xml:"CallerReference"`
	Comment         string   `xml:"Comment,omitempty"`
}

type CFOriginAccessIdentity struct {
	XMLName                              xml.Name                     `xml:"CloudFrontOriginAccessIdentity"`
	Xmlns                                string                       `xml:"xmlns,attr,omitempty"`
	Id                                   string                       `xml:"Id"`
	S3CanonicalUserId                    string                       `xml:"S3CanonicalUserId"`
	CloudFrontOriginAccessIdentityConfig CFOriginAccessIdentityConfig `xml:"CloudFrontOriginAccessIdentityConfig"`
}

type CFOriginAccessIdentitySummary struct {
	Id                string `xml:"Id"`
	S3CanonicalUserId string `xml:"S3CanonicalUserId"`
	Comment           string `xml:"Comment,omitempty"`
}

type CFOriginAccessIdentityList struct {
	XMLName     xml.Name                        `xml:"CloudFrontOriginAccessIdentityList"`
	Xmlns       string                          `xml:"xmlns,attr,omitempty"`
	Marker      string                          `xml:"Marker"`
	NextMarker  string                          `xml:"NextMarker,omitempty"`
	MaxItems    int                             `xml:"MaxItems"`
	IsTruncated bool                            `xml:"IsTruncated"`
	Quantity    int                             `xml:"Quantity"`
	Items       []CFOriginAccessIdentitySummary `xml:"Items>CloudFrontOriginAccessIdentitySummary,omitempty"`
}

type cfStoredOAI struct {
	Identity CFOriginAccessIdentity
	ETag     string
}

type CFStagingDistributionDnsNames struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>DnsName,omitempty"`
}

type CFContinuousDeploymentSingleHeaderConfig struct {
	Header string `xml:"Header"`
	Value  string `xml:"Value"`
}

type CFSessionStickinessConfig struct {
	IdleTTL    int `xml:"IdleTTL"`
	MaximumTTL int `xml:"MaximumTTL"`
}

type CFContinuousDeploymentSingleWeightConfig struct {
	Weight                  float64                    `xml:"Weight"`
	SessionStickinessConfig *CFSessionStickinessConfig `xml:"SessionStickinessConfig,omitempty"`
}

type CFTrafficConfig struct {
	SingleWeightConfig *CFContinuousDeploymentSingleWeightConfig `xml:"SingleWeightConfig,omitempty"`
	SingleHeaderConfig *CFContinuousDeploymentSingleHeaderConfig `xml:"SingleHeaderConfig,omitempty"`
	Type               string                                    `xml:"Type"`
}

type CFContinuousDeploymentPolicyConfig struct {
	XMLName                     xml.Name                      `xml:"ContinuousDeploymentPolicyConfig"`
	Xmlns                       string                        `xml:"xmlns,attr,omitempty"`
	StagingDistributionDnsNames CFStagingDistributionDnsNames `xml:"StagingDistributionDnsNames"`
	Enabled                     bool                          `xml:"Enabled"`
	TrafficConfig               *CFTrafficConfig              `xml:"TrafficConfig,omitempty"`
}

type CFContinuousDeploymentPolicy struct {
	XMLName                          xml.Name                           `xml:"ContinuousDeploymentPolicy"`
	Xmlns                            string                             `xml:"xmlns,attr,omitempty"`
	Id                               string                             `xml:"Id"`
	LastModifiedTime                 string                             `xml:"LastModifiedTime"`
	ContinuousDeploymentPolicyConfig CFContinuousDeploymentPolicyConfig `xml:"ContinuousDeploymentPolicyConfig"`
}

type CFContinuousDeploymentPolicySummary struct {
	ContinuousDeploymentPolicy CFContinuousDeploymentPolicy `xml:"ContinuousDeploymentPolicy"`
}

type CFContinuousDeploymentPolicyList struct {
	XMLName    xml.Name                              `xml:"ContinuousDeploymentPolicyList"`
	Xmlns      string                                `xml:"xmlns,attr,omitempty"`
	NextMarker string                                `xml:"NextMarker,omitempty"`
	MaxItems   int                                   `xml:"MaxItems"`
	Quantity   int                                   `xml:"Quantity"`
	Items      []CFContinuousDeploymentPolicySummary `xml:"Items>ContinuousDeploymentPolicySummary,omitempty"`
}

type cfStoredCDP struct {
	Policy CFContinuousDeploymentPolicy
	ETag   string
}

type CFRealtimeMetricsSubscriptionConfig struct {
	RealtimeMetricsSubscriptionStatus string `xml:"RealtimeMetricsSubscriptionStatus"`
}

type CFMonitoringSubscription struct {
	XMLName                           xml.Name                             `xml:"MonitoringSubscription"`
	Xmlns                             string                               `xml:"xmlns,attr,omitempty"`
	RealtimeMetricsSubscriptionConfig *CFRealtimeMetricsSubscriptionConfig `xml:"RealtimeMetricsSubscriptionConfig,omitempty"`
}

var (
	cfOAIs                       sim.Store[cfStoredOAI]
	cfContinuousDeploymentPolicy sim.Store[cfStoredCDP]
	cfMonitoringSubscriptions    sim.Store[CFMonitoringSubscription]
)

// cfCanonicalUserID derives a deterministic 64-hex-char S3 canonical user
// id from the OAI id, mirroring the opaque value real CloudFront returns.
func cfCanonicalUserID() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// registerCloudFrontExtras is invoked from registerCloudFront in
// cloudfront.go; it adds the OAI, continuous-deployment-policy, and
// monitoring-subscription CRUD endpoints onto the same mux.
func registerCloudFrontExtras(srv *sim.Server) {
	cfOAIs = sim.MakeStore[cfStoredOAI](srv.DB(), "cloudfront_oais")
	cfContinuousDeploymentPolicy = sim.MakeStore[cfStoredCDP](srv.DB(), "cloudfront_continuous_deployment_policies")
	cfMonitoringSubscriptions = sim.MakeStore[CFMonitoringSubscription](srv.DB(), "cloudfront_monitoring_subscriptions")

	mux := srv

	oaiResource := cloudTrailRESTResource("AWS::CloudFront::CloudFrontOriginAccessIdentity", "Id")
	cdpResource := cloudTrailRESTResource("AWS::CloudFront::ContinuousDeploymentPolicy", "Id")
	distResource := cloudTrailRESTResource("AWS::CloudFront::Distribution", "DistributionId")

	// CloudFront Origin Access Identity
	mux.HandleFunc("POST /"+cfAPIVersion+"/origin-access-identity/cloudfront", cloudTrailRecordedREST("CreateCloudFrontOriginAccessIdentity", "cloudfront.amazonaws.com", nil, handleCFCreateOAI))
	mux.HandleFunc("GET /"+cfAPIVersion+"/origin-access-identity/cloudfront", cloudTrailRecordedREST("ListCloudFrontOriginAccessIdentities", "cloudfront.amazonaws.com", nil, handleCFListOAIs))
	mux.HandleFunc("GET /"+cfAPIVersion+"/origin-access-identity/cloudfront/{Id}", cloudTrailRecordedREST("GetCloudFrontOriginAccessIdentity", "cloudfront.amazonaws.com", oaiResource, handleCFGetOAI))
	mux.HandleFunc("GET /"+cfAPIVersion+"/origin-access-identity/cloudfront/{Id}/config", cloudTrailRecordedREST("GetCloudFrontOriginAccessIdentityConfig", "cloudfront.amazonaws.com", oaiResource, handleCFGetOAIConfig))
	mux.HandleFunc("PUT /"+cfAPIVersion+"/origin-access-identity/cloudfront/{Id}/config", cloudTrailRecordedREST("UpdateCloudFrontOriginAccessIdentity", "cloudfront.amazonaws.com", oaiResource, handleCFUpdateOAI))
	mux.HandleFunc("DELETE /"+cfAPIVersion+"/origin-access-identity/cloudfront/{Id}", cloudTrailRecordedREST("DeleteCloudFrontOriginAccessIdentity", "cloudfront.amazonaws.com", oaiResource, handleCFDeleteOAI))

	// Continuous Deployment Policy
	mux.HandleFunc("POST /"+cfAPIVersion+"/continuous-deployment-policy", cloudTrailRecordedREST("CreateContinuousDeploymentPolicy", "cloudfront.amazonaws.com", nil, handleCFCreateCDP))
	mux.HandleFunc("GET /"+cfAPIVersion+"/continuous-deployment-policy", cloudTrailRecordedREST("ListContinuousDeploymentPolicies", "cloudfront.amazonaws.com", nil, handleCFListCDPs))
	mux.HandleFunc("GET /"+cfAPIVersion+"/continuous-deployment-policy/{Id}", cloudTrailRecordedREST("GetContinuousDeploymentPolicy", "cloudfront.amazonaws.com", cdpResource, handleCFGetCDP))
	mux.HandleFunc("GET /"+cfAPIVersion+"/continuous-deployment-policy/{Id}/config", cloudTrailRecordedREST("GetContinuousDeploymentPolicyConfig", "cloudfront.amazonaws.com", cdpResource, handleCFGetCDPConfig))
	mux.HandleFunc("PUT /"+cfAPIVersion+"/continuous-deployment-policy/{Id}", cloudTrailRecordedREST("UpdateContinuousDeploymentPolicy", "cloudfront.amazonaws.com", cdpResource, handleCFUpdateCDP))
	mux.HandleFunc("DELETE /"+cfAPIVersion+"/continuous-deployment-policy/{Id}", cloudTrailRecordedREST("DeleteContinuousDeploymentPolicy", "cloudfront.amazonaws.com", cdpResource, handleCFDeleteCDP))

	// Monitoring Subscription (per-distribution)
	mux.HandleFunc("POST /"+cfAPIVersion+"/distributions/{DistributionId}/monitoring-subscription", cloudTrailRecordedREST("CreateMonitoringSubscription", "cloudfront.amazonaws.com", distResource, handleCFCreateMonitoringSubscription))
	mux.HandleFunc("GET /"+cfAPIVersion+"/distributions/{DistributionId}/monitoring-subscription", cloudTrailRecordedREST("GetMonitoringSubscription", "cloudfront.amazonaws.com", distResource, handleCFGetMonitoringSubscription))
	mux.HandleFunc("DELETE /"+cfAPIVersion+"/distributions/{DistributionId}/monitoring-subscription", cloudTrailRecordedREST("DeleteMonitoringSubscription", "cloudfront.amazonaws.com", distResource, handleCFDeleteMonitoringSubscription))
}

func handleCFCreateOAI(w http.ResponseWriter, r *http.Request) {
	var cfg CFOriginAccessIdentityConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode CloudFrontOriginAccessIdentityConfig: "+err.Error())
		return
	}
	if cfg.CallerReference == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "CallerReference is required")
		return
	}
	cfg.Xmlns = ""
	id := cfRandomID("E")
	etag := cfETag()
	identity := CFOriginAccessIdentity{
		Xmlns:                                cfNamespace,
		Id:                                   id,
		S3CanonicalUserId:                    cfCanonicalUserID(),
		CloudFrontOriginAccessIdentityConfig: cfg,
	}
	cfOAIs.Put(id, cfStoredOAI{Identity: identity, ETag: etag})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/origin-access-identity/cloudfront/"+id)
	cfWriteXML(w, http.StatusCreated, identity)
}

func handleCFGetOAI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfOAIs.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchCloudFrontOriginAccessIdentity", "The specified origin access identity does not exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.Identity)
}

func handleCFGetOAIConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfOAIs.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchCloudFrontOriginAccessIdentity", "The specified origin access identity does not exist.")
		return
	}
	cfg := stored.Identity.CloudFrontOriginAccessIdentityConfig
	cfg.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, cfg)
}

func handleCFUpdateOAI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfOAIs.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchCloudFrontOriginAccessIdentity", "The specified origin access identity does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	var cfg CFOriginAccessIdentityConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode CloudFrontOriginAccessIdentityConfig: "+err.Error())
		return
	}
	cfg.Xmlns = ""
	newETag := cfETag()
	stored.Identity.CloudFrontOriginAccessIdentityConfig = cfg
	stored.ETag = newETag
	cfOAIs.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.Identity)
}

func handleCFDeleteOAI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfOAIs.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchCloudFrontOriginAccessIdentity", "The specified origin access identity does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	cfOAIs.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListOAIs(w http.ResponseWriter, _ *http.Request) {
	items := []CFOriginAccessIdentitySummary{}
	for _, stored := range cfOAIs.List() {
		items = append(items, CFOriginAccessIdentitySummary{
			Id:                stored.Identity.Id,
			S3CanonicalUserId: stored.Identity.S3CanonicalUserId,
			Comment:           stored.Identity.CloudFrontOriginAccessIdentityConfig.Comment,
		})
	}
	list := CFOriginAccessIdentityList{
		Xmlns:       cfNamespace,
		Marker:      "",
		MaxItems:    100,
		IsTruncated: false,
		Quantity:    len(items),
		Items:       items,
	}
	cfWriteXML(w, http.StatusOK, list)
}

func handleCFCreateCDP(w http.ResponseWriter, r *http.Request) {
	var cfg CFContinuousDeploymentPolicyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode ContinuousDeploymentPolicyConfig: "+err.Error())
		return
	}
	cfg.Xmlns = ""
	id := cfRandomID("CDP")
	etag := cfETag()
	policy := CFContinuousDeploymentPolicy{
		Xmlns:                            cfNamespace,
		Id:                               id,
		LastModifiedTime:                 cfNowISO(),
		ContinuousDeploymentPolicyConfig: cfg,
	}
	cfContinuousDeploymentPolicy.Put(id, cfStoredCDP{Policy: policy, ETag: etag})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/continuous-deployment-policy/"+id)
	cfWriteXML(w, http.StatusCreated, policy)
}

func handleCFGetCDP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfContinuousDeploymentPolicy.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchContinuousDeploymentPolicy", "The specified continuous deployment policy does not exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.Policy)
}

func handleCFGetCDPConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfContinuousDeploymentPolicy.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchContinuousDeploymentPolicy", "The specified continuous deployment policy does not exist.")
		return
	}
	cfg := stored.Policy.ContinuousDeploymentPolicyConfig
	cfg.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, cfg)
}

func handleCFUpdateCDP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfContinuousDeploymentPolicy.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchContinuousDeploymentPolicy", "The specified continuous deployment policy does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	var cfg CFContinuousDeploymentPolicyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode ContinuousDeploymentPolicyConfig: "+err.Error())
		return
	}
	cfg.Xmlns = ""
	newETag := cfETag()
	stored.Policy.ContinuousDeploymentPolicyConfig = cfg
	stored.Policy.LastModifiedTime = cfNowISO()
	stored.ETag = newETag
	cfContinuousDeploymentPolicy.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.Policy)
}

func handleCFDeleteCDP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfContinuousDeploymentPolicy.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchContinuousDeploymentPolicy", "The specified continuous deployment policy does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	cfContinuousDeploymentPolicy.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListCDPs(w http.ResponseWriter, _ *http.Request) {
	items := []CFContinuousDeploymentPolicySummary{}
	for _, stored := range cfContinuousDeploymentPolicy.List() {
		items = append(items, CFContinuousDeploymentPolicySummary{ContinuousDeploymentPolicy: stored.Policy})
	}
	list := CFContinuousDeploymentPolicyList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	}
	cfWriteXML(w, http.StatusOK, list)
}

func handleCFCreateMonitoringSubscription(w http.ResponseWriter, r *http.Request) {
	distID := r.PathValue("DistributionId")
	if _, ok := cfDistributions.Get(distID); !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
		return
	}
	var sub CFMonitoringSubscription
	if err := xml.NewDecoder(r.Body).Decode(&sub); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode MonitoringSubscription: "+err.Error())
		return
	}
	sub.Xmlns = cfNamespace
	cfMonitoringSubscriptions.Put(distID, sub)
	cfWriteXML(w, http.StatusOK, sub)
}

func handleCFGetMonitoringSubscription(w http.ResponseWriter, r *http.Request) {
	distID := r.PathValue("DistributionId")
	if _, ok := cfDistributions.Get(distID); !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
		return
	}
	sub, ok := cfMonitoringSubscriptions.Get(distID)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchMonitoringSubscription", "A monitoring subscription does not exist for the specified distribution.")
		return
	}
	sub.Xmlns = cfNamespace
	cfWriteXML(w, http.StatusOK, sub)
}

func handleCFDeleteMonitoringSubscription(w http.ResponseWriter, r *http.Request) {
	distID := r.PathValue("DistributionId")
	if _, ok := cfDistributions.Get(distID); !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
		return
	}
	if _, ok := cfMonitoringSubscriptions.Get(distID); !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchMonitoringSubscription", "A monitoring subscription does not exist for the specified distribution.")
		return
	}
	cfMonitoringSubscriptions.Delete(distID)
	cfWriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"DeleteMonitoringSubscriptionResult"`
	}{})
}

// cfRequireIfMatch validates the If-Match header against the current ETag.
// Returns "" on success, or an error code on failure.
func cfRequireIfMatch(r *http.Request, etag string) string {
	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	if ifMatch == "" {
		return "InvalidIfMatchVersion"
	}
	if ifMatch != etag {
		return "PreconditionFailed"
	}
	return ""
}

func cfWriteIfMatchError(w http.ResponseWriter, code string) {
	if code == "InvalidIfMatchVersion" {
		cfWriteError(w, http.StatusBadRequest, "InvalidIfMatchVersion", "The If-Match header is required.")
		return
	}
	cfWriteError(w, http.StatusPreconditionFailed, "PreconditionFailed", "The If-Match version is missing or does not match the resource's current ETag.")
}
