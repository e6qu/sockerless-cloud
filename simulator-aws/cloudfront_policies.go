package main

import (
	"encoding/xml"
	"net/http"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// CloudFront policy resources — cache, origin-request, response-headers.
// Same REST + XML wire as the Distribution + OAC endpoints in cloudfront.go.

// ---------- CachePolicy ----------

type CFCachePolicyConfig struct {
	XMLName                                  xml.Name                                         `xml:"CachePolicyConfig"`
	Xmlns                                    string                                           `xml:"xmlns,attr,omitempty"`
	Name                                     string                                           `xml:"Name"`
	Comment                                  string                                           `xml:"Comment,omitempty"`
	DefaultTTL                               *int64                                           `xml:"DefaultTTL,omitempty"`
	MaxTTL                                   *int64                                           `xml:"MaxTTL,omitempty"`
	MinTTL                                   int64                                            `xml:"MinTTL"`
	ParametersInCacheKeyAndForwardedToOrigin *CFCacheParametersInCacheKeyAndForwardedToOrigin `xml:"ParametersInCacheKeyAndForwardedToOrigin,omitempty"`
}

type CFCacheParametersInCacheKeyAndForwardedToOrigin struct {
	EnableAcceptEncodingGzip   *bool                           `xml:"EnableAcceptEncodingGzip,omitempty"`
	EnableAcceptEncodingBrotli *bool                           `xml:"EnableAcceptEncodingBrotli,omitempty"`
	HeadersConfig              CFCachePolicyHeadersConfig      `xml:"HeadersConfig"`
	CookiesConfig              CFCachePolicyCookiesConfig      `xml:"CookiesConfig"`
	QueryStringsConfig         CFCachePolicyQueryStringsConfig `xml:"QueryStringsConfig"`
}

type CFCachePolicyHeadersConfig struct {
	HeaderBehavior string     `xml:"HeaderBehavior"`
	Headers        *CFHeaders `xml:"Headers,omitempty"`
}

type CFCachePolicyCookiesConfig struct {
	CookieBehavior string         `xml:"CookieBehavior"`
	Cookies        *CFCookieNames `xml:"Cookies,omitempty"`
}

type CFCachePolicyQueryStringsConfig struct {
	QueryStringBehavior string    `xml:"QueryStringBehavior"`
	QueryStrings        *CFQSKeys `xml:"QueryStrings,omitempty"`
}

type CFCachePolicy struct {
	XMLName           xml.Name            `xml:"CachePolicy"`
	Xmlns             string              `xml:"xmlns,attr,omitempty"`
	Id                string              `xml:"Id"`
	LastModifiedTime  string              `xml:"LastModifiedTime"`
	CachePolicyConfig CFCachePolicyConfig `xml:"CachePolicyConfig"`
}

// CachePolicySummary is the list-shape wrapper around a CachePolicy with
// a Type discriminator (managed | custom). Terraform only creates
// "custom"; "managed" types come pre-baked on real AWS.
type CFCachePolicySummary struct {
	Type        string        `xml:"Type"`
	CachePolicy CFCachePolicy `xml:"CachePolicy"`
}

type CFCachePolicyList struct {
	XMLName    xml.Name               `xml:"CachePolicyList"`
	Xmlns      string                 `xml:"xmlns,attr,omitempty"`
	MaxItems   int                    `xml:"MaxItems"`
	Quantity   int                    `xml:"Quantity"`
	NextMarker string                 `xml:"NextMarker,omitempty"`
	Items      []CFCachePolicySummary `xml:"Items>CachePolicySummary,omitempty"`
}

// ---------- OriginRequestPolicy ----------

type CFOriginRequestPolicyConfig struct {
	XMLName            xml.Name                                `xml:"OriginRequestPolicyConfig"`
	Xmlns              string                                  `xml:"xmlns,attr,omitempty"`
	Name               string                                  `xml:"Name"`
	Comment            string                                  `xml:"Comment,omitempty"`
	HeadersConfig      CFOriginRequestPolicyHeadersConfig      `xml:"HeadersConfig"`
	CookiesConfig      CFOriginRequestPolicyCookiesConfig      `xml:"CookiesConfig"`
	QueryStringsConfig CFOriginRequestPolicyQueryStringsConfig `xml:"QueryStringsConfig"`
}

type CFOriginRequestPolicyHeadersConfig struct {
	HeaderBehavior string     `xml:"HeaderBehavior"`
	Headers        *CFHeaders `xml:"Headers,omitempty"`
}

type CFOriginRequestPolicyCookiesConfig struct {
	CookieBehavior string         `xml:"CookieBehavior"`
	Cookies        *CFCookieNames `xml:"Cookies,omitempty"`
}

type CFOriginRequestPolicyQueryStringsConfig struct {
	QueryStringBehavior string    `xml:"QueryStringBehavior"`
	QueryStrings        *CFQSKeys `xml:"QueryStrings,omitempty"`
}

type CFOriginRequestPolicy struct {
	XMLName                   xml.Name                    `xml:"OriginRequestPolicy"`
	Xmlns                     string                      `xml:"xmlns,attr,omitempty"`
	Id                        string                      `xml:"Id"`
	LastModifiedTime          string                      `xml:"LastModifiedTime"`
	OriginRequestPolicyConfig CFOriginRequestPolicyConfig `xml:"OriginRequestPolicyConfig"`
}

type CFOriginRequestPolicySummary struct {
	Type                string                `xml:"Type"`
	OriginRequestPolicy CFOriginRequestPolicy `xml:"OriginRequestPolicy"`
}

type CFOriginRequestPolicyList struct {
	XMLName    xml.Name                       `xml:"OriginRequestPolicyList"`
	Xmlns      string                         `xml:"xmlns,attr,omitempty"`
	MaxItems   int                            `xml:"MaxItems"`
	Quantity   int                            `xml:"Quantity"`
	NextMarker string                         `xml:"NextMarker,omitempty"`
	Items      []CFOriginRequestPolicySummary `xml:"Items>OriginRequestPolicySummary,omitempty"`
}

// ---------- ResponseHeadersPolicy ----------

type CFResponseHeadersPolicyConfig struct {
	XMLName                   xml.Name                                `xml:"ResponseHeadersPolicyConfig"`
	Xmlns                     string                                  `xml:"xmlns,attr,omitempty"`
	Name                      string                                  `xml:"Name"`
	Comment                   string                                  `xml:"Comment,omitempty"`
	CorsConfig                *CFResponseHeadersPolicyCorsConfig      `xml:"CorsConfig,omitempty"`
	SecurityHeadersConfig     *CFResponseHeadersPolicySecurityHeaders `xml:"SecurityHeadersConfig,omitempty"`
	ServerTimingHeadersConfig *CFResponseHeadersPolicyServerTiming    `xml:"ServerTimingHeadersConfig,omitempty"`
	CustomHeadersConfig       *CFResponseHeadersPolicyCustomHeaders   `xml:"CustomHeadersConfig,omitempty"`
	RemoveHeadersConfig       *CFResponseHeadersPolicyRemoveHeaders   `xml:"RemoveHeadersConfig,omitempty"`
}

type CFResponseHeadersPolicyCorsConfig struct {
	AccessControlAllowCredentials bool                            `xml:"AccessControlAllowCredentials"`
	AccessControlAllowHeaders     CFResponseHeadersAllowHeaders   `xml:"AccessControlAllowHeaders"`
	AccessControlAllowMethods     CFResponseHeadersAllowMethods   `xml:"AccessControlAllowMethods"`
	AccessControlAllowOrigins     CFResponseHeadersAllowOrigins   `xml:"AccessControlAllowOrigins"`
	AccessControlExposeHeaders    *CFResponseHeadersExposeHeaders `xml:"AccessControlExposeHeaders,omitempty"`
	AccessControlMaxAgeSec        *int                            `xml:"AccessControlMaxAgeSec,omitempty"`
	OriginOverride                bool                            `xml:"OriginOverride"`
}

type CFResponseHeadersAllowHeaders struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>Header,omitempty"`
}

type CFResponseHeadersAllowMethods struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>Method,omitempty"`
}

type CFResponseHeadersAllowOrigins struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>Origin,omitempty"`
}

type CFResponseHeadersExposeHeaders struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>Header,omitempty"`
}

type CFResponseHeadersPolicySecurityHeaders struct {
	XSSProtection           *CFXSSProtectionConfig           `xml:"XSSProtection,omitempty"`
	FrameOptions            *CFFrameOptionsConfig            `xml:"FrameOptions,omitempty"`
	ReferrerPolicy          *CFReferrerPolicyConfig          `xml:"ReferrerPolicy,omitempty"`
	ContentSecurityPolicy   *CFContentSecurityPolicyConfig   `xml:"ContentSecurityPolicy,omitempty"`
	ContentTypeOptions      *CFContentTypeOptionsConfig      `xml:"ContentTypeOptions,omitempty"`
	StrictTransportSecurity *CFStrictTransportSecurityConfig `xml:"StrictTransportSecurity,omitempty"`
}

type CFXSSProtectionConfig struct {
	Override   bool   `xml:"Override"`
	Protection bool   `xml:"Protection"`
	ModeBlock  *bool  `xml:"ModeBlock,omitempty"`
	ReportUri  string `xml:"ReportUri,omitempty"`
}

type CFFrameOptionsConfig struct {
	Override    bool   `xml:"Override"`
	FrameOption string `xml:"FrameOption"`
}

type CFReferrerPolicyConfig struct {
	Override       bool   `xml:"Override"`
	ReferrerPolicy string `xml:"ReferrerPolicy"`
}

type CFContentSecurityPolicyConfig struct {
	Override              bool   `xml:"Override"`
	ContentSecurityPolicy string `xml:"ContentSecurityPolicy"`
}

type CFContentTypeOptionsConfig struct {
	Override bool `xml:"Override"`
}

type CFStrictTransportSecurityConfig struct {
	Override               bool  `xml:"Override"`
	AccessControlMaxAgeSec int64 `xml:"AccessControlMaxAgeSec"`
	IncludeSubdomains      *bool `xml:"IncludeSubdomains,omitempty"`
	Preload                *bool `xml:"Preload,omitempty"`
}

type CFResponseHeadersPolicyServerTiming struct {
	Enabled      bool     `xml:"Enabled"`
	SamplingRate *float64 `xml:"SamplingRate,omitempty"`
}

type CFResponseHeadersPolicyCustomHeaders struct {
	Quantity int                      `xml:"Quantity"`
	Items    []CFResponseCustomHeader `xml:"Items>ResponseHeadersPolicyCustomHeader,omitempty"`
}

type CFResponseCustomHeader struct {
	Header   string `xml:"Header"`
	Value    string `xml:"Value"`
	Override bool   `xml:"Override"`
}

type CFResponseHeadersPolicyRemoveHeaders struct {
	Quantity int                      `xml:"Quantity"`
	Items    []CFResponseRemoveHeader `xml:"Items>ResponseHeadersPolicyRemoveHeader,omitempty"`
}

type CFResponseRemoveHeader struct {
	Header string `xml:"Header"`
}

type CFResponseHeadersPolicy struct {
	XMLName                     xml.Name                      `xml:"ResponseHeadersPolicy"`
	Xmlns                       string                        `xml:"xmlns,attr,omitempty"`
	Id                          string                        `xml:"Id"`
	LastModifiedTime            string                        `xml:"LastModifiedTime"`
	ResponseHeadersPolicyConfig CFResponseHeadersPolicyConfig `xml:"ResponseHeadersPolicyConfig"`
}

type CFResponseHeadersPolicySummary struct {
	Type                  string                  `xml:"Type"`
	ResponseHeadersPolicy CFResponseHeadersPolicy `xml:"ResponseHeadersPolicy"`
}

type CFResponseHeadersPolicyList struct {
	XMLName    xml.Name                         `xml:"ResponseHeadersPolicyList"`
	Xmlns      string                           `xml:"xmlns,attr,omitempty"`
	MaxItems   int                              `xml:"MaxItems"`
	Quantity   int                              `xml:"Quantity"`
	NextMarker string                           `xml:"NextMarker,omitempty"`
	Items      []CFResponseHeadersPolicySummary `xml:"Items>ResponseHeadersPolicySummary,omitempty"`
}

// ---------- Storage ----------

type cfStoredCachePolicy struct {
	Policy CFCachePolicy
	ETag   string
}

type cfStoredOriginRequestPolicy struct {
	Policy CFOriginRequestPolicy
	ETag   string
}

type cfStoredResponseHeadersPolicy struct {
	Policy CFResponseHeadersPolicy
	ETag   string
}

var (
	cfCachePolicies           sim.Store[cfStoredCachePolicy]
	cfOriginRequestPolicies   sim.Store[cfStoredOriginRequestPolicy]
	cfResponseHeadersPolicies sim.Store[cfStoredResponseHeadersPolicy]
)

// registerCloudFrontPolicies is invoked from registerCloudFront in
// cloudfront.go; it adds the policy CRUD endpoints onto the same mux.
func registerCloudFrontPolicies(srv *sim.Server) {
	cfCachePolicies = sim.MakeStore[cfStoredCachePolicy](srv.DB(), "cloudfront_cache_policies")
	cfOriginRequestPolicies = sim.MakeStore[cfStoredOriginRequestPolicy](srv.DB(), "cloudfront_origin_request_policies")
	cfResponseHeadersPolicies = sim.MakeStore[cfStoredResponseHeadersPolicy](srv.DB(), "cloudfront_response_headers_policies")

	mux := srv

	cachePolicyResource := cloudTrailRESTResource("AWS::CloudFront::CachePolicy", "id")
	originRequestPolicyResource := cloudTrailRESTResource("AWS::CloudFront::OriginRequestPolicy", "id")
	responseHeadersPolicyResource := cloudTrailRESTResource("AWS::CloudFront::ResponseHeadersPolicy", "id")
	// CachePolicy
	mux.HandleFunc("POST /"+cfAPIVersion+"/cache-policy", cloudTrailRecordedREST("CreateCachePolicy", "cloudfront.amazonaws.com", nil, handleCFCreateCachePolicy))
	mux.HandleFunc("GET /"+cfAPIVersion+"/cache-policy", cloudTrailRecordedREST("ListCachePolicies", "cloudfront.amazonaws.com", nil, handleCFListCachePolicies))
	mux.HandleFunc("GET /"+cfAPIVersion+"/cache-policy/{id}", cloudTrailRecordedREST("GetCachePolicy", "cloudfront.amazonaws.com", cachePolicyResource, handleCFGetCachePolicy))
	mux.HandleFunc("GET /"+cfAPIVersion+"/cache-policy/{id}/config", cloudTrailRecordedREST("GetCachePolicyConfig", "cloudfront.amazonaws.com", cachePolicyResource, handleCFGetCachePolicyConfig))
	mux.HandleFunc("PUT /"+cfAPIVersion+"/cache-policy/{id}", cloudTrailRecordedREST("UpdateCachePolicy", "cloudfront.amazonaws.com", cachePolicyResource, handleCFUpdateCachePolicy))
	mux.HandleFunc("DELETE /"+cfAPIVersion+"/cache-policy/{id}", cloudTrailRecordedREST("DeleteCachePolicy", "cloudfront.amazonaws.com", cachePolicyResource, handleCFDeleteCachePolicy))

	// OriginRequestPolicy
	mux.HandleFunc("POST /"+cfAPIVersion+"/origin-request-policy", cloudTrailRecordedREST("CreateOriginRequestPolicy", "cloudfront.amazonaws.com", nil, handleCFCreateORP))
	mux.HandleFunc("GET /"+cfAPIVersion+"/origin-request-policy", cloudTrailRecordedREST("ListOriginRequestPolicies", "cloudfront.amazonaws.com", nil, handleCFListORPs))
	mux.HandleFunc("GET /"+cfAPIVersion+"/origin-request-policy/{id}", cloudTrailRecordedREST("GetOriginRequestPolicy", "cloudfront.amazonaws.com", originRequestPolicyResource, handleCFGetORP))
	mux.HandleFunc("GET /"+cfAPIVersion+"/origin-request-policy/{id}/config", cloudTrailRecordedREST("GetOriginRequestPolicyConfig", "cloudfront.amazonaws.com", originRequestPolicyResource, handleCFGetORPConfig))
	mux.HandleFunc("PUT /"+cfAPIVersion+"/origin-request-policy/{id}", cloudTrailRecordedREST("UpdateOriginRequestPolicy", "cloudfront.amazonaws.com", originRequestPolicyResource, handleCFUpdateORP))
	mux.HandleFunc("DELETE /"+cfAPIVersion+"/origin-request-policy/{id}", cloudTrailRecordedREST("DeleteOriginRequestPolicy", "cloudfront.amazonaws.com", originRequestPolicyResource, handleCFDeleteORP))

	// ResponseHeadersPolicy
	mux.HandleFunc("POST /"+cfAPIVersion+"/response-headers-policy", cloudTrailRecordedREST("CreateResponseHeadersPolicy", "cloudfront.amazonaws.com", nil, handleCFCreateRHP))
	mux.HandleFunc("GET /"+cfAPIVersion+"/response-headers-policy", cloudTrailRecordedREST("ListResponseHeadersPolicies", "cloudfront.amazonaws.com", nil, handleCFListRHPs))
	mux.HandleFunc("GET /"+cfAPIVersion+"/response-headers-policy/{id}", cloudTrailRecordedREST("GetResponseHeadersPolicy", "cloudfront.amazonaws.com", responseHeadersPolicyResource, handleCFGetRHP))
	mux.HandleFunc("GET /"+cfAPIVersion+"/response-headers-policy/{id}/config", cloudTrailRecordedREST("GetResponseHeadersPolicyConfig", "cloudfront.amazonaws.com", responseHeadersPolicyResource, handleCFGetRHPConfig))
	mux.HandleFunc("PUT /"+cfAPIVersion+"/response-headers-policy/{id}", cloudTrailRecordedREST("UpdateResponseHeadersPolicy", "cloudfront.amazonaws.com", responseHeadersPolicyResource, handleCFUpdateRHP))
	mux.HandleFunc("DELETE /"+cfAPIVersion+"/response-headers-policy/{id}", cloudTrailRecordedREST("DeleteResponseHeadersPolicy", "cloudfront.amazonaws.com", responseHeadersPolicyResource, handleCFDeleteRHP))
}

// ----- CachePolicy handlers -----

func handleCFCreateCachePolicy(w http.ResponseWriter, r *http.Request) {
	var cfg CFCachePolicyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode CachePolicyConfig: "+err.Error())
		return
	}
	if cfg.Name == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Name is required")
		return
	}
	cfg.Xmlns = ""
	id := cfRandomID("CP")
	etag := cfETag()
	policy := CFCachePolicy{
		Xmlns:             cfNamespace,
		Id:                id,
		LastModifiedTime:  cfNowISO(),
		CachePolicyConfig: cfg,
	}
	cfCachePolicies.Put(id, cfStoredCachePolicy{Policy: policy, ETag: etag})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/cache-policy/"+id)
	cfWriteXML(w, http.StatusCreated, policy)
}

func handleCFGetCachePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfCachePolicies.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchCachePolicy", "The specified cache policy does not exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.Policy)
}

func handleCFGetCachePolicyConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfCachePolicies.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchCachePolicy", "The specified cache policy does not exist.")
		return
	}
	cfg := stored.Policy.CachePolicyConfig
	cfg.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, cfg)
}

func handleCFUpdateCachePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfCachePolicies.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchCachePolicy", "The specified cache policy does not exist.")
		return
	}
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidIfMatchVersion", "The If-Match header is required.")
		return
	}
	if ifMatch != stored.ETag {
		cfWriteError(w, http.StatusPreconditionFailed, "PreconditionFailed", "The If-Match version is missing or does not match the resource's current ETag.")
		return
	}
	var cfg CFCachePolicyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode CachePolicyConfig: "+err.Error())
		return
	}
	cfg.Xmlns = ""
	newETag := cfETag()
	stored.Policy.CachePolicyConfig = cfg
	stored.Policy.LastModifiedTime = cfNowISO()
	stored.ETag = newETag
	cfCachePolicies.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.Policy)
}

func handleCFDeleteCachePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfCachePolicies.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchCachePolicy", "The specified cache policy does not exist.")
		return
	}
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidIfMatchVersion", "The If-Match header is required.")
		return
	}
	if ifMatch != stored.ETag {
		cfWriteError(w, http.StatusPreconditionFailed, "PreconditionFailed", "The If-Match version is missing or does not match the resource's current ETag.")
		return
	}
	cfCachePolicies.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListCachePolicies(w http.ResponseWriter, r *http.Request) {
	items := []CFCachePolicySummary{}
	for _, stored := range cfCachePolicies.List() {
		items = append(items, CFCachePolicySummary{Type: "custom", CachePolicy: stored.Policy})
	}
	list := CFCachePolicyList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	}
	cfWriteXML(w, http.StatusOK, list)
}

// ----- OriginRequestPolicy handlers -----

func handleCFCreateORP(w http.ResponseWriter, r *http.Request) {
	var cfg CFOriginRequestPolicyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode OriginRequestPolicyConfig: "+err.Error())
		return
	}
	if cfg.Name == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Name is required")
		return
	}
	cfg.Xmlns = ""
	id := cfRandomID("ORP")
	etag := cfETag()
	policy := CFOriginRequestPolicy{
		Xmlns:                     cfNamespace,
		Id:                        id,
		LastModifiedTime:          cfNowISO(),
		OriginRequestPolicyConfig: cfg,
	}
	cfOriginRequestPolicies.Put(id, cfStoredOriginRequestPolicy{Policy: policy, ETag: etag})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/origin-request-policy/"+id)
	cfWriteXML(w, http.StatusCreated, policy)
}

func handleCFGetORP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfOriginRequestPolicies.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchOriginRequestPolicy", "The specified origin request policy does not exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.Policy)
}

func handleCFGetORPConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfOriginRequestPolicies.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchOriginRequestPolicy", "The specified origin request policy does not exist.")
		return
	}
	cfg := stored.Policy.OriginRequestPolicyConfig
	cfg.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, cfg)
}

func handleCFUpdateORP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfOriginRequestPolicies.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchOriginRequestPolicy", "The specified origin request policy does not exist.")
		return
	}
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidIfMatchVersion", "The If-Match header is required.")
		return
	}
	if ifMatch != stored.ETag {
		cfWriteError(w, http.StatusPreconditionFailed, "PreconditionFailed", "The If-Match version is missing or does not match the resource's current ETag.")
		return
	}
	var cfg CFOriginRequestPolicyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode OriginRequestPolicyConfig: "+err.Error())
		return
	}
	cfg.Xmlns = ""
	newETag := cfETag()
	stored.Policy.OriginRequestPolicyConfig = cfg
	stored.Policy.LastModifiedTime = cfNowISO()
	stored.ETag = newETag
	cfOriginRequestPolicies.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.Policy)
}

func handleCFDeleteORP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfOriginRequestPolicies.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchOriginRequestPolicy", "The specified origin request policy does not exist.")
		return
	}
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidIfMatchVersion", "The If-Match header is required.")
		return
	}
	if ifMatch != stored.ETag {
		cfWriteError(w, http.StatusPreconditionFailed, "PreconditionFailed", "The If-Match version is missing or does not match the resource's current ETag.")
		return
	}
	cfOriginRequestPolicies.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListORPs(w http.ResponseWriter, r *http.Request) {
	items := []CFOriginRequestPolicySummary{}
	for _, stored := range cfOriginRequestPolicies.List() {
		items = append(items, CFOriginRequestPolicySummary{Type: "custom", OriginRequestPolicy: stored.Policy})
	}
	list := CFOriginRequestPolicyList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	}
	cfWriteXML(w, http.StatusOK, list)
}

// ----- ResponseHeadersPolicy handlers -----

func handleCFCreateRHP(w http.ResponseWriter, r *http.Request) {
	var cfg CFResponseHeadersPolicyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode ResponseHeadersPolicyConfig: "+err.Error())
		return
	}
	if cfg.Name == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Name is required")
		return
	}
	cfg.Xmlns = ""
	id := cfRandomID("RHP")
	etag := cfETag()
	policy := CFResponseHeadersPolicy{
		Xmlns:                       cfNamespace,
		Id:                          id,
		LastModifiedTime:            cfNowISO(),
		ResponseHeadersPolicyConfig: cfg,
	}
	cfResponseHeadersPolicies.Put(id, cfStoredResponseHeadersPolicy{Policy: policy, ETag: etag})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/response-headers-policy/"+id)
	cfWriteXML(w, http.StatusCreated, policy)
}

func handleCFGetRHP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfResponseHeadersPolicies.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResponseHeadersPolicy", "The specified response headers policy does not exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.Policy)
}

func handleCFGetRHPConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfResponseHeadersPolicies.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResponseHeadersPolicy", "The specified response headers policy does not exist.")
		return
	}
	cfg := stored.Policy.ResponseHeadersPolicyConfig
	cfg.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, cfg)
}

func handleCFUpdateRHP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfResponseHeadersPolicies.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResponseHeadersPolicy", "The specified response headers policy does not exist.")
		return
	}
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidIfMatchVersion", "The If-Match header is required.")
		return
	}
	if ifMatch != stored.ETag {
		cfWriteError(w, http.StatusPreconditionFailed, "PreconditionFailed", "The If-Match version is missing or does not match the resource's current ETag.")
		return
	}
	var cfg CFResponseHeadersPolicyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode ResponseHeadersPolicyConfig: "+err.Error())
		return
	}
	cfg.Xmlns = ""
	newETag := cfETag()
	stored.Policy.ResponseHeadersPolicyConfig = cfg
	stored.Policy.LastModifiedTime = cfNowISO()
	stored.ETag = newETag
	cfResponseHeadersPolicies.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.Policy)
}

func handleCFDeleteRHP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfResponseHeadersPolicies.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResponseHeadersPolicy", "The specified response headers policy does not exist.")
		return
	}
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidIfMatchVersion", "The If-Match header is required.")
		return
	}
	if ifMatch != stored.ETag {
		cfWriteError(w, http.StatusPreconditionFailed, "PreconditionFailed", "The If-Match version is missing or does not match the resource's current ETag.")
		return
	}
	cfResponseHeadersPolicies.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListRHPs(w http.ResponseWriter, r *http.Request) {
	items := []CFResponseHeadersPolicySummary{}
	for _, stored := range cfResponseHeadersPolicies.List() {
		items = append(items, CFResponseHeadersPolicySummary{Type: "custom", ResponseHeadersPolicy: stored.Policy})
	}
	list := CFResponseHeadersPolicyList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	}
	cfWriteXML(w, http.StatusOK, list)
}
