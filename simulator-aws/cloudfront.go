package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// CloudFront is REST + XML. All paths are versioned at /2020-05-31/.
// Namespace on every body: http://cloudfront.amazonaws.com/doc/2020-05-31/
// ETag header returns on Get + Create + Update; If-Match required on
// Update + Delete. Mirrors aws-sdk-go-v2/service/cloudfront wire shape.

const (
	cfAPIVersion = "2020-05-31"
	cfNamespace  = "http://cloudfront.amazonaws.com/doc/2020-05-31/"
)

// ---------- DistributionConfig (request payload) ----------

type CFDistributionConfig struct {
	XMLName              xml.Name             `xml:"DistributionConfig"`
	Xmlns                string               `xml:"xmlns,attr,omitempty"`
	CallerReference      string               `xml:"CallerReference"`
	Aliases              *CFAliases           `xml:"Aliases,omitempty"`
	DefaultRootObject    string               `xml:"DefaultRootObject,omitempty"`
	Origins              CFOrigins            `xml:"Origins"`
	OriginGroups         *CFOriginGroups      `xml:"OriginGroups,omitempty"`
	DefaultCacheBehavior CFCacheBehavior      `xml:"DefaultCacheBehavior"`
	CacheBehaviors       *CFCacheBehaviors    `xml:"CacheBehaviors,omitempty"`
	CustomErrorResponses *CFCustomErrors      `xml:"CustomErrorResponses,omitempty"`
	Comment              string               `xml:"Comment"`
	Logging              *CFLogging           `xml:"Logging,omitempty"`
	PriceClass           string               `xml:"PriceClass,omitempty"`
	Enabled              bool                 `xml:"Enabled"`
	ViewerCertificate    *CFViewerCertificate `xml:"ViewerCertificate,omitempty"`
	Restrictions         *CFRestrictions      `xml:"Restrictions,omitempty"`
	WebACLId             string               `xml:"WebACLId,omitempty"`
	HttpVersion          string               `xml:"HttpVersion,omitempty"`
	IsIPV6Enabled        *bool                `xml:"IsIPV6Enabled,omitempty"`
	Staging              *bool                `xml:"Staging,omitempty"`
}

type CFAliases struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>CNAME,omitempty"`
}

type CFOrigins struct {
	Quantity int        `xml:"Quantity"`
	Items    []CFOrigin `xml:"Items>Origin"`
}

type CFOrigin struct {
	Id                    string                `xml:"Id"`
	DomainName            string                `xml:"DomainName"`
	OriginPath            string                `xml:"OriginPath,omitempty"`
	CustomHeaders         *CFCustomHeaders      `xml:"CustomHeaders,omitempty"`
	S3OriginConfig        *CFS3OriginConfig     `xml:"S3OriginConfig,omitempty"`
	CustomOriginConfig    *CFCustomOriginConfig `xml:"CustomOriginConfig,omitempty"`
	ConnectionAttempts    *int                  `xml:"ConnectionAttempts,omitempty"`
	ConnectionTimeout     *int                  `xml:"ConnectionTimeout,omitempty"`
	OriginShield          *CFOriginShield       `xml:"OriginShield,omitempty"`
	OriginAccessControlId string                `xml:"OriginAccessControlId,omitempty"`
}

type CFCustomHeaders struct {
	Quantity int              `xml:"Quantity"`
	Items    []CFCustomHeader `xml:"Items>OriginCustomHeader,omitempty"`
}

type CFCustomHeader struct {
	HeaderName  string `xml:"HeaderName"`
	HeaderValue string `xml:"HeaderValue"`
}

type CFS3OriginConfig struct {
	OriginAccessIdentity string `xml:"OriginAccessIdentity"`
}

type CFCustomOriginConfig struct {
	HTTPPort               int          `xml:"HTTPPort"`
	HTTPSPort              int          `xml:"HTTPSPort"`
	OriginProtocolPolicy   string       `xml:"OriginProtocolPolicy"`
	OriginSslProtocols     *CFSslProtos `xml:"OriginSslProtocols,omitempty"`
	OriginReadTimeout      *int         `xml:"OriginReadTimeout,omitempty"`
	OriginKeepaliveTimeout *int         `xml:"OriginKeepaliveTimeout,omitempty"`
}

type CFSslProtos struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>SslProtocol,omitempty"`
}

type CFOriginShield struct {
	Enabled            bool   `xml:"Enabled"`
	OriginShieldRegion string `xml:"OriginShieldRegion,omitempty"`
}

type CFOriginGroups struct {
	Quantity int             `xml:"Quantity"`
	Items    []CFOriginGroup `xml:"Items>OriginGroup,omitempty"`
}

type CFOriginGroup struct {
	Id               string                `xml:"Id"`
	FailoverCriteria *CFFailoverCriteria   `xml:"FailoverCriteria,omitempty"`
	Members          *CFOriginGroupMembers `xml:"Members,omitempty"`
}

type CFFailoverCriteria struct {
	StatusCodes *CFStatusCodes `xml:"StatusCodes,omitempty"`
}

type CFStatusCodes struct {
	Quantity int   `xml:"Quantity"`
	Items    []int `xml:"Items>StatusCode,omitempty"`
}

type CFOriginGroupMembers struct {
	Quantity int                   `xml:"Quantity"`
	Items    []CFOriginGroupMember `xml:"Items>OriginGroupMember,omitempty"`
}

type CFOriginGroupMember struct {
	OriginId string `xml:"OriginId"`
}

type CFCacheBehavior struct {
	PathPattern                string              `xml:"PathPattern,omitempty"`
	TargetOriginId             string              `xml:"TargetOriginId"`
	TrustedSigners             *CFTrustedSigners   `xml:"TrustedSigners,omitempty"`
	TrustedKeyGroups           *CFTrustedKeyGroups `xml:"TrustedKeyGroups,omitempty"`
	ViewerProtocolPolicy       string              `xml:"ViewerProtocolPolicy"`
	AllowedMethods             *CFAllowedMethods   `xml:"AllowedMethods,omitempty"`
	SmoothStreaming            *bool               `xml:"SmoothStreaming,omitempty"`
	Compress                   *bool               `xml:"Compress,omitempty"`
	LambdaFunctionAssociations *CFLambdaAssocs     `xml:"LambdaFunctionAssociations,omitempty"`
	FunctionAssociations       *CFFunctionAssocs   `xml:"FunctionAssociations,omitempty"`
	FieldLevelEncryptionId     string              `xml:"FieldLevelEncryptionId,omitempty"`
	RealtimeLogConfigArn       string              `xml:"RealtimeLogConfigArn,omitempty"`
	CachePolicyId              string              `xml:"CachePolicyId,omitempty"`
	OriginRequestPolicyId      string              `xml:"OriginRequestPolicyId,omitempty"`
	ResponseHeadersPolicyId    string              `xml:"ResponseHeadersPolicyId,omitempty"`
	ForwardedValues            *CFForwardedValues  `xml:"ForwardedValues,omitempty"`
	MinTTL                     *int64              `xml:"MinTTL,omitempty"`
	DefaultTTL                 *int64              `xml:"DefaultTTL,omitempty"`
	MaxTTL                     *int64              `xml:"MaxTTL,omitempty"`
}

type CFCacheBehaviors struct {
	Quantity int               `xml:"Quantity"`
	Items    []CFCacheBehavior `xml:"Items>CacheBehavior,omitempty"`
}

type CFTrustedSigners struct {
	Enabled  bool     `xml:"Enabled"`
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>AwsAccountNumber,omitempty"`
}

type CFTrustedKeyGroups struct {
	Enabled  bool     `xml:"Enabled"`
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>KeyGroup,omitempty"`
}

type CFAllowedMethods struct {
	Quantity      int              `xml:"Quantity"`
	Items         []string         `xml:"Items>Method"`
	CachedMethods *CFCachedMethods `xml:"CachedMethods,omitempty"`
}

type CFCachedMethods struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>Method"`
}

type CFLambdaAssocs struct {
	Quantity int             `xml:"Quantity"`
	Items    []CFLambdaAssoc `xml:"Items>LambdaFunctionAssociation,omitempty"`
}

type CFLambdaAssoc struct {
	LambdaFunctionARN string `xml:"LambdaFunctionARN"`
	EventType         string `xml:"EventType"`
	IncludeBody       *bool  `xml:"IncludeBody,omitempty"`
}

type CFFunctionAssocs struct {
	Quantity int               `xml:"Quantity"`
	Items    []CFFunctionAssoc `xml:"Items>FunctionAssociation,omitempty"`
}

type CFFunctionAssoc struct {
	FunctionARN string `xml:"FunctionARN"`
	EventType   string `xml:"EventType"`
}

type CFForwardedValues struct {
	QueryString          bool       `xml:"QueryString"`
	Cookies              CFCookies  `xml:"Cookies"`
	Headers              *CFHeaders `xml:"Headers,omitempty"`
	QueryStringCacheKeys *CFQSKeys  `xml:"QueryStringCacheKeys,omitempty"`
}

type CFCookies struct {
	Forward          string         `xml:"Forward"`
	WhitelistedNames *CFCookieNames `xml:"WhitelistedNames,omitempty"`
}

type CFCookieNames struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>Name,omitempty"`
}

type CFHeaders struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>Name,omitempty"`
}

type CFQSKeys struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>Name,omitempty"`
}

type CFCustomErrors struct {
	Quantity int             `xml:"Quantity"`
	Items    []CFCustomError `xml:"Items>CustomErrorResponse,omitempty"`
}

type CFCustomError struct {
	ErrorCode          int    `xml:"ErrorCode"`
	ResponsePagePath   string `xml:"ResponsePagePath,omitempty"`
	ResponseCode       string `xml:"ResponseCode,omitempty"`
	ErrorCachingMinTTL *int64 `xml:"ErrorCachingMinTTL,omitempty"`
}

type CFLogging struct {
	Enabled        bool   `xml:"Enabled"`
	IncludeCookies bool   `xml:"IncludeCookies"`
	Bucket         string `xml:"Bucket"`
	Prefix         string `xml:"Prefix"`
}

type CFViewerCertificate struct {
	CloudFrontDefaultCertificate *bool  `xml:"CloudFrontDefaultCertificate,omitempty"`
	IAMCertificateId             string `xml:"IAMCertificateId,omitempty"`
	ACMCertificateArn            string `xml:"ACMCertificateArn,omitempty"`
	SSLSupportMethod             string `xml:"SSLSupportMethod,omitempty"`
	MinimumProtocolVersion       string `xml:"MinimumProtocolVersion,omitempty"`
	Certificate                  string `xml:"Certificate,omitempty"`
	CertificateSource            string `xml:"CertificateSource,omitempty"`
}

type CFRestrictions struct {
	GeoRestriction CFGeoRestriction `xml:"GeoRestriction"`
}

type CFGeoRestriction struct {
	RestrictionType string   `xml:"RestrictionType"`
	Quantity        int      `xml:"Quantity"`
	Items           []string `xml:"Items>Location,omitempty"`
}

// ---------- Distribution (response payload) ----------

type CFDistribution struct {
	XMLName                       xml.Name                  `xml:"Distribution"`
	Xmlns                         string                    `xml:"xmlns,attr,omitempty"`
	Id                            string                    `xml:"Id"`
	ARN                           string                    `xml:"ARN"`
	Status                        string                    `xml:"Status"`
	LastModifiedTime              string                    `xml:"LastModifiedTime"`
	InProgressInvalidationBatches int                       `xml:"InProgressInvalidationBatches"`
	DomainName                    string                    `xml:"DomainName"`
	ActiveTrustedSigners          CFActiveTrustedSigners    `xml:"ActiveTrustedSigners"`
	ActiveTrustedKeyGroups        *CFActiveTrustedKeyGroups `xml:"ActiveTrustedKeyGroups,omitempty"`
	DistributionConfig            CFDistributionConfig      `xml:"DistributionConfig"`
	AliasICPRecordals             *CFAliasICPRecordals      `xml:"AliasICPRecordals,omitempty"`
}

type CFActiveTrustedSigners struct {
	Enabled  bool                    `xml:"Enabled"`
	Quantity int                     `xml:"Quantity"`
	Items    []CFActiveTrustedSigner `xml:"Items>Signer,omitempty"`
}

type CFActiveTrustedSigner struct {
	AwsAccountNumber string        `xml:"AwsAccountNumber"`
	KeyPairIds       *CFKeyPairIds `xml:"KeyPairIds,omitempty"`
}

type CFKeyPairIds struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>KeyPairId,omitempty"`
}

type CFActiveTrustedKeyGroups struct {
	Enabled  bool                      `xml:"Enabled"`
	Quantity int                       `xml:"Quantity"`
	Items    []CFActiveTrustedKeyGroup `xml:"Items>KeyGroup,omitempty"`
}

type CFActiveTrustedKeyGroup struct {
	KeyGroupId string        `xml:"KeyGroupId"`
	KeyPairIds *CFKeyPairIds `xml:"KeyPairIds,omitempty"`
}

type CFAliasICPRecordals struct {
	Items []CFAliasICPRecordal `xml:"AliasICPRecordal,omitempty"`
}

type CFAliasICPRecordal struct {
	CNAME             string `xml:"CNAME"`
	ICPRecordalStatus string `xml:"ICPRecordalStatus"`
}

// ---------- List response wrappers ----------

type CFDistributionList struct {
	XMLName     xml.Name                `xml:"DistributionList"`
	Xmlns       string                  `xml:"xmlns,attr,omitempty"`
	Marker      string                  `xml:"Marker,omitempty"`
	NextMarker  string                  `xml:"NextMarker,omitempty"`
	MaxItems    int                     `xml:"MaxItems"`
	IsTruncated bool                    `xml:"IsTruncated"`
	Quantity    int                     `xml:"Quantity"`
	Items       []CFDistributionSummary `xml:"Items>DistributionSummary,omitempty"`
}

// CFDistributionSummary mirrors a Distribution but without nested config
// payload (the list shape inlines the same fields flat).
type CFDistributionSummary struct {
	Id                   string               `xml:"Id"`
	ARN                  string               `xml:"ARN"`
	Status               string               `xml:"Status"`
	LastModifiedTime     string               `xml:"LastModifiedTime"`
	DomainName           string               `xml:"DomainName"`
	Aliases              *CFAliases           `xml:"Aliases,omitempty"`
	Origins              CFOrigins            `xml:"Origins"`
	OriginGroups         *CFOriginGroups      `xml:"OriginGroups,omitempty"`
	DefaultCacheBehavior CFCacheBehavior      `xml:"DefaultCacheBehavior"`
	CacheBehaviors       *CFCacheBehaviors    `xml:"CacheBehaviors,omitempty"`
	CustomErrorResponses *CFCustomErrors      `xml:"CustomErrorResponses,omitempty"`
	Comment              string               `xml:"Comment"`
	PriceClass           string               `xml:"PriceClass,omitempty"`
	Enabled              bool                 `xml:"Enabled"`
	ViewerCertificate    CFViewerCertificate  `xml:"ViewerCertificate"`
	Restrictions         CFRestrictions       `xml:"Restrictions"`
	WebACLId             string               `xml:"WebACLId,omitempty"`
	HttpVersion          string               `xml:"HttpVersion,omitempty"`
	IsIPV6Enabled        bool                 `xml:"IsIPV6Enabled"`
	Staging              bool                 `xml:"Staging"`
	AliasICPRecordals    *CFAliasICPRecordals `xml:"AliasICPRecordals,omitempty"`
}

// ---------- Tags (shared across CF resources) ----------

type CFTags struct {
	Items []CFTag `xml:"Items>Tag,omitempty"`
}

type CFTag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value,omitempty"`
}

// DistributionConfigWithTags is the CreateDistributionWithTags body. The
// SDK emits this whenever the Terraform aws_cloudfront_distribution
// resource is applied; both no-tag and with-tag paths flow through the
// ?WithTags query variant. See aws-sdk-go-v2 serializers.go.
type CFDistributionConfigWithTags struct {
	XMLName            xml.Name             `xml:"DistributionConfigWithTags"`
	Xmlns              string               `xml:"xmlns,attr,omitempty"`
	DistributionConfig CFDistributionConfig `xml:"DistributionConfig"`
	Tags               CFTags               `xml:"Tags"`
}

// ---------- OriginAccessControl ----------

type CFOriginAccessControlConfig struct {
	XMLName                       xml.Name `xml:"OriginAccessControlConfig"`
	Xmlns                         string   `xml:"xmlns,attr,omitempty"`
	Name                          string   `xml:"Name"`
	Description                   string   `xml:"Description,omitempty"`
	SigningProtocol               string   `xml:"SigningProtocol"`
	SigningBehavior               string   `xml:"SigningBehavior"`
	OriginAccessControlOriginType string   `xml:"OriginAccessControlOriginType"`
}

type CFOriginAccessControl struct {
	XMLName                   xml.Name                    `xml:"OriginAccessControl"`
	Xmlns                     string                      `xml:"xmlns,attr,omitempty"`
	Id                        string                      `xml:"Id"`
	OriginAccessControlConfig CFOriginAccessControlConfig `xml:"OriginAccessControlConfig"`
}

type CFOriginAccessControlList struct {
	XMLName     xml.Name                       `xml:"OriginAccessControlList"`
	Xmlns       string                         `xml:"xmlns,attr,omitempty"`
	Marker      string                         `xml:"Marker,omitempty"`
	NextMarker  string                         `xml:"NextMarker,omitempty"`
	MaxItems    int                            `xml:"MaxItems"`
	IsTruncated bool                           `xml:"IsTruncated"`
	Quantity    int                            `xml:"Quantity"`
	Items       []CFOriginAccessControlSummary `xml:"Items>OriginAccessControlSummary,omitempty"`
}

type CFOriginAccessControlSummary struct {
	Id                            string `xml:"Id"`
	Description                   string `xml:"Description,omitempty"`
	Name                          string `xml:"Name"`
	SigningProtocol               string `xml:"SigningProtocol"`
	SigningBehavior               string `xml:"SigningBehavior"`
	OriginAccessControlOriginType string `xml:"OriginAccessControlOriginType"`
}

// ---------- Error response ----------

type CFErrorResponse struct {
	XMLName   xml.Name `xml:"ErrorResponse"`
	Xmlns     string   `xml:"xmlns,attr,omitempty"`
	Error     CFError  `xml:"Error"`
	RequestId string   `xml:"RequestId"`
}

type CFError struct {
	Type    string `xml:"Type,omitempty"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

// ---------- Storage envelope ----------

// cfStoredDistribution holds a Distribution with its current ETag. ETag
// changes on every Update; If-Match on Update/Delete must match the
// current value. Tags travel alongside the distribution but live in a
// separate `ListTagsForResource` path (added in ).
type cfStoredDistribution struct {
	Distribution CFDistribution
	ETag         string
	Tags         []CFTag
}

type cfStoredOAC struct {
	OAC  CFOriginAccessControl
	ETag string
}

// ---------- State + helpers ----------

var (
	cfDistributions        sim.Store[cfStoredDistribution]
	cfOriginAccessControls sim.Store[cfStoredOAC]
)

func cfRandomID(prefix string) string {
	buf := make([]byte, 7)
	_, _ = rand.Read(buf)
	return strings.ToUpper(prefix + hex.EncodeToString(buf))
}

func cfETag() string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return strings.ToUpper("E" + hex.EncodeToString(buf))
}

func cfDistributionARN(id string) string {
	return fmt.Sprintf("arn:aws:cloudfront::%s:distribution/%s", awsAccountID(), id)
}

func cfNowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func cfDomainName(id string) string {
	return strings.ToLower(id) + ".cloudfront.net"
}

// cfValidateViewerCertificate enforces real-AWS constraints on the
// distribution's ViewerCertificate block. Specifically: ACM
// certificates referenced via ACMCertificateArn must be issued in
// us-east-1 (real AWS rejects any other region with
// InvalidViewerCertificate). Returns empty string on success, error
// message on failure.
func cfValidateViewerCertificate(vc *CFViewerCertificate) string {
	if vc == nil || vc.ACMCertificateArn == "" {
		return ""
	}
	exists, regionMatch := acmCertExistsInRegion(vc.ACMCertificateArn, "us-east-1")
	if !exists {
		return "The specified ACM certificate does not exist: " + vc.ACMCertificateArn
	}
	if !regionMatch {
		return "The specified ACM certificate must be in the us-east-1 region for use with CloudFront: " + vc.ACMCertificateArn
	}
	return ""
}

// cfNormalizeConfig fills in empty values for every optional nested element
// in DistributionConfig so the GetDistribution response always carries the
// full XML shape the Terraform aws provider expects. The provider reads
// many sub-fields without nil-checks; an omitted XML element deserialises
// to *T == nil and crashes the provider.
func cfNormalizeConfig(c *CFDistributionConfig) {
	if c.Aliases == nil {
		c.Aliases = &CFAliases{Quantity: 0}
	}
	if c.OriginGroups == nil {
		c.OriginGroups = &CFOriginGroups{Quantity: 0}
	}
	if c.CacheBehaviors == nil {
		c.CacheBehaviors = &CFCacheBehaviors{Quantity: 0}
	}
	if c.CustomErrorResponses == nil {
		c.CustomErrorResponses = &CFCustomErrors{Quantity: 0}
	}
	if c.Logging == nil {
		c.Logging = &CFLogging{Enabled: false}
	}
	if c.ViewerCertificate == nil {
		def := true
		c.ViewerCertificate = &CFViewerCertificate{CloudFrontDefaultCertificate: &def}
	}
	if c.Restrictions == nil {
		c.Restrictions = &CFRestrictions{GeoRestriction: CFGeoRestriction{RestrictionType: "none", Quantity: 0}}
	}
	if c.IsIPV6Enabled == nil {
		f := false
		c.IsIPV6Enabled = &f
	}
	if c.Staging == nil {
		f := false
		c.Staging = &f
	}
	if c.PriceClass == "" {
		c.PriceClass = "PriceClass_All"
	}
	if c.HttpVersion == "" {
		c.HttpVersion = "http2"
	}
}

func cfWriteXML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(v)
}

func cfWriteError(w http.ResponseWriter, status int, code, msg string) {
	cfWriteXML(w, status, CFErrorResponse{
		Xmlns:     cfNamespace,
		Error:     CFError{Type: "Sender", Code: code, Message: msg},
		RequestId: cfRandomID("REQ"),
	})
}

// ---------- Registration + handlers ----------

func registerCloudFront(srv *sim.Server) {
	cfDistributions = sim.MakeStore[cfStoredDistribution](srv.DB(), "cloudfront_distributions")
	cfOriginAccessControls = sim.MakeStore[cfStoredOAC](srv.DB(), "cloudfront_oacs")

	mux := srv

	cfDistributionResource := cloudTrailRESTResource("AWS::CloudFront::Distribution", "id", "Resource")
	cfOACResource := cloudTrailRESTResource("AWS::CloudFront::OriginAccessControl", "id")
	// Distributions
	mux.HandleFunc("POST /"+cfAPIVersion+"/distribution", cloudTrailRecordedRESTDynamic(cfCreateDistributionOperationName, "cloudfront.amazonaws.com", nil, handleCFCreateDistribution))
	mux.HandleFunc("GET /"+cfAPIVersion+"/distribution", cloudTrailRecordedREST("ListDistributions", "cloudfront.amazonaws.com", nil, handleCFListDistributions))
	mux.HandleFunc("GET /"+cfAPIVersion+"/distribution/{id}", cloudTrailRecordedREST("GetDistribution", "cloudfront.amazonaws.com", cfDistributionResource, handleCFGetDistribution))
	mux.HandleFunc("GET /"+cfAPIVersion+"/distribution/{id}/config", cloudTrailRecordedREST("GetDistributionConfig", "cloudfront.amazonaws.com", cfDistributionResource, handleCFGetDistributionConfig))
	mux.HandleFunc("PUT /"+cfAPIVersion+"/distribution/{id}/config", cloudTrailRecordedREST("UpdateDistribution", "cloudfront.amazonaws.com", cfDistributionResource, handleCFUpdateDistribution))
	mux.HandleFunc("DELETE /"+cfAPIVersion+"/distribution/{id}", cloudTrailRecordedREST("DeleteDistribution", "cloudfront.amazonaws.com", cfDistributionResource, handleCFDeleteDistribution))

	// Tagging — single endpoint dispatches on ?Operation= param
	mux.HandleFunc("GET /"+cfAPIVersion+"/tagging", cloudTrailRecordedREST("ListTagsForResource", "cloudfront.amazonaws.com", cfDistributionResource, handleCFListTags))
	mux.HandleFunc("POST /"+cfAPIVersion+"/tagging", cloudTrailRecordedRESTDynamic(cfTagOperationName, "cloudfront.amazonaws.com", cfDistributionResource, handleCFTagDispatch))

	// Policies (cache / origin-request / response-headers) — same wire shape
	registerCloudFrontPolicies(srv)

	// Functions + Invalidations
	registerCloudFrontFunctions(srv)

	// KeyGroups + PublicKeys
	registerCloudFrontKeys(srv)

	// OriginAccessIdentities + ContinuousDeploymentPolicies + MonitoringSubscriptions
	registerCloudFrontExtras(srv)

	// Field-level encryption, key value stores, real-time log configs,
	// streaming distributions, VPC origins, and Anycast IP lists.
	registerCloudFrontExtras2(srv)

	// Distribution tenants, connection groups, trust stores, resource policies,
	// WebACL associations, alias/domain conflicts, the ListDistributionsBy*
	// projections, and the copy/promote/anycast-update/managed-cert variants.
	registerCloudFrontExtras3(srv)

	// Connection functions, the CloudFront Functions TestFunction op, tagging
	// op-name registration, and the create-distribution variant op names.
	registerCloudFrontComplete(srv)

	// OriginAccessControl
	mux.HandleFunc("POST /"+cfAPIVersion+"/origin-access-control", cloudTrailRecordedREST("CreateOriginAccessControl", "cloudfront.amazonaws.com", nil, handleCFCreateOAC))
	mux.HandleFunc("GET /"+cfAPIVersion+"/origin-access-control", cloudTrailRecordedREST("ListOriginAccessControls", "cloudfront.amazonaws.com", nil, handleCFListOACs))
	mux.HandleFunc("GET /"+cfAPIVersion+"/origin-access-control/{id}", cloudTrailRecordedREST("GetOriginAccessControl", "cloudfront.amazonaws.com", cfOACResource, handleCFGetOAC))
	mux.HandleFunc("GET /"+cfAPIVersion+"/origin-access-control/{id}/config", cloudTrailRecordedREST("GetOriginAccessControlConfig", "cloudfront.amazonaws.com", cfOACResource, handleCFGetOACConfig))
	mux.HandleFunc("PUT /"+cfAPIVersion+"/origin-access-control/{id}/config", cloudTrailRecordedREST("UpdateOriginAccessControl", "cloudfront.amazonaws.com", cfOACResource, handleCFUpdateOAC))
	mux.HandleFunc("DELETE /"+cfAPIVersion+"/origin-access-control/{id}", cloudTrailRecordedREST("DeleteOriginAccessControl", "cloudfront.amazonaws.com", cfOACResource, handleCFDeleteOAC))
}

func cfCreateDistributionOperationName(r *http.Request, _ []byte) string {
	if r.URL.Query().Has("WithTags") {
		return "CreateDistributionWithTags"
	}
	return "CreateDistribution"
}

func cfTagOperationName(r *http.Request, _ []byte) string {
	switch strings.ToLower(r.URL.Query().Get("Operation")) {
	case "tag":
		return "TagResource"
	case "untag":
		return "UntagResource"
	}
	return ""
}

// ----- Distribution handlers -----

// handleCFCreateDistribution dispatches between CreateDistribution and
// CreateDistributionWithTags based on the `WithTags` query string the
// AWS SDK sets on the same POST path. Real AWS sniffs the query param
// to disambiguate; we mirror that.
func handleCFCreateDistribution(w http.ResponseWriter, r *http.Request) {
	withTags := r.URL.Query().Has("WithTags")
	var cfg CFDistributionConfig
	var tags []CFTag
	if withTags {
		var wrap CFDistributionConfigWithTags
		if err := xml.NewDecoder(r.Body).Decode(&wrap); err != nil {
			cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode DistributionConfigWithTags: "+err.Error())
			return
		}
		cfg = wrap.DistributionConfig
		tags = wrap.Tags.Items
	} else {
		if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
			cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode DistributionConfig: "+err.Error())
			return
		}
	}
	if cfg.CallerReference == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "CallerReference is required")
		return
	}
	if cfg.Origins.Quantity == 0 || len(cfg.Origins.Items) == 0 {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "At least one origin is required")
		return
	}
	if err := cfValidateViewerCertificate(cfg.ViewerCertificate); err != "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidViewerCertificate", err)
		return
	}
	cfg.Xmlns = "" // we set namespace on the outer Distribution element
	cfNormalizeConfig(&cfg)
	id := cfRandomID("E")
	etag := cfETag()
	dist := CFDistribution{
		Xmlns:                         cfNamespace,
		Id:                            id,
		ARN:                           cfDistributionARN(id),
		Status:                        "Deployed",
		LastModifiedTime:              cfNowISO(),
		InProgressInvalidationBatches: 0,
		DomainName:                    cfDomainName(id),
		ActiveTrustedSigners:          CFActiveTrustedSigners{Enabled: false, Quantity: 0},
		ActiveTrustedKeyGroups:        &CFActiveTrustedKeyGroups{Enabled: false, Quantity: 0},
		DistributionConfig:            cfg,
	}
	cfDistributions.Put(id, cfStoredDistribution{Distribution: dist, ETag: etag, Tags: tags})
	w.Header().Set("ETag", etag)
	// external: real-AWS canonical Location header; aws-sdk-go-v2
	// parses the distribution ID from the response body, not from
	// this header, so callers configured with a sim endpoint work
	// fine despite the URL host pointing at the real AWS surface.
	w.Header().Set("Location", fmt.Sprintf("https://cloudfront.amazonaws.com/%s/distribution/%s", cfAPIVersion, id))
	cfWriteXML(w, http.StatusCreated, dist)
}

func handleCFGetDistribution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfDistributions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.Distribution)
}

func handleCFGetDistributionConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfDistributions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
		return
	}
	cfg := stored.Distribution.DistributionConfig
	cfg.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, cfg)
}

func handleCFUpdateDistribution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfDistributions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
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
	var cfg CFDistributionConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode DistributionConfig: "+err.Error())
		return
	}
	if msg := cfValidateViewerCertificate(cfg.ViewerCertificate); msg != "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidViewerCertificate", msg)
		return
	}
	cfg.Xmlns = ""
	cfNormalizeConfig(&cfg)
	newETag := cfETag()
	stored.Distribution.DistributionConfig = cfg
	stored.Distribution.LastModifiedTime = cfNowISO()
	stored.ETag = newETag
	cfDistributions.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.Distribution)
}

func handleCFDeleteDistribution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfDistributions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
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
	if stored.Distribution.DistributionConfig.Enabled {
		cfWriteError(w, http.StatusBadRequest, "DistributionNotDisabled", "The distribution you are trying to delete has not been disabled.")
		return
	}
	cfDistributions.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListDistributions(w http.ResponseWriter, r *http.Request) {
	items := []CFDistributionSummary{}
	for _, stored := range cfDistributions.List() {
		d := stored.Distribution
		c := d.DistributionConfig
		s := CFDistributionSummary{
			Id:                   d.Id,
			ARN:                  d.ARN,
			Status:               d.Status,
			LastModifiedTime:     d.LastModifiedTime,
			DomainName:           d.DomainName,
			Aliases:              c.Aliases,
			Origins:              c.Origins,
			OriginGroups:         c.OriginGroups,
			DefaultCacheBehavior: c.DefaultCacheBehavior,
			CacheBehaviors:       c.CacheBehaviors,
			CustomErrorResponses: c.CustomErrorResponses,
			Comment:              c.Comment,
			PriceClass:           c.PriceClass,
			Enabled:              c.Enabled,
			WebACLId:             c.WebACLId,
			HttpVersion:          c.HttpVersion,
		}
		if c.ViewerCertificate != nil {
			s.ViewerCertificate = *c.ViewerCertificate
		}
		if c.Restrictions != nil {
			s.Restrictions = *c.Restrictions
		}
		if c.IsIPV6Enabled != nil {
			s.IsIPV6Enabled = *c.IsIPV6Enabled
		}
		if c.Staging != nil {
			s.Staging = *c.Staging
		}
		items = append(items, s)
	}
	list := CFDistributionList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	}
	cfWriteXML(w, http.StatusOK, list)
}

// ----- OriginAccessControl handlers -----

func handleCFCreateOAC(w http.ResponseWriter, r *http.Request) {
	var cfg CFOriginAccessControlConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode OriginAccessControlConfig: "+err.Error())
		return
	}
	if cfg.Name == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Name is required")
		return
	}
	if cfg.SigningProtocol == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "SigningProtocol is required")
		return
	}
	if cfg.SigningBehavior == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "SigningBehavior is required")
		return
	}
	if cfg.OriginAccessControlOriginType == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "OriginAccessControlOriginType is required")
		return
	}
	cfg.Xmlns = ""
	id := cfRandomID("E")
	etag := cfETag()
	oac := CFOriginAccessControl{
		Xmlns:                     cfNamespace,
		Id:                        id,
		OriginAccessControlConfig: cfg,
	}
	cfOriginAccessControls.Put(id, cfStoredOAC{OAC: oac, ETag: etag})
	w.Header().Set("ETag", etag)
	// external: same rationale as the distribution Location header
	// above — SDK parses ID from body, not from this header.
	w.Header().Set("Location", fmt.Sprintf("https://cloudfront.amazonaws.com/%s/origin-access-control/%s", cfAPIVersion, id))
	cfWriteXML(w, http.StatusCreated, oac)
}

func handleCFGetOAC(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfOriginAccessControls.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchOriginAccessControl", "The specified origin access control does not exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.OAC)
}

func handleCFGetOACConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfOriginAccessControls.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchOriginAccessControl", "The specified origin access control does not exist.")
		return
	}
	cfg := stored.OAC.OriginAccessControlConfig
	cfg.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, cfg)
}

func handleCFUpdateOAC(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfOriginAccessControls.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchOriginAccessControl", "The specified origin access control does not exist.")
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
	var cfg CFOriginAccessControlConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode OriginAccessControlConfig: "+err.Error())
		return
	}
	cfg.Xmlns = ""
	newETag := cfETag()
	stored.OAC.OriginAccessControlConfig = cfg
	stored.ETag = newETag
	cfOriginAccessControls.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.OAC)
}

func handleCFDeleteOAC(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfOriginAccessControls.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchOriginAccessControl", "The specified origin access control does not exist.")
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
	cfOriginAccessControls.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

// ----- Tagging handlers -----

// cfDistributionIDFromARN extracts the distribution ID from an ARN of the
// form "arn:aws:cloudfront::<acct>:distribution/<id>".
func cfDistributionIDFromARN(arn string) string {
	const prefix = "distribution/"
	i := strings.LastIndex(arn, prefix)
	if i < 0 {
		return ""
	}
	return arn[i+len(prefix):]
}

// cfFunctionNameFromARN extracts the function name from a CloudFront Function
// ARN (arn:aws:cloudfront::<acct>:function/<name>/<stage>), tolerating both the
// stage-qualified (.../LIVE) and unqualified forms. Returns "" for non-function
// ARNs (e.g. distributions), which the tagging handlers route to cfDistributions.
func cfFunctionNameFromARN(arn string) string {
	const prefix = "function/"
	i := strings.LastIndex(arn, prefix)
	if i < 0 {
		return ""
	}
	rest := arn[i+len(prefix):]
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		return rest[:slash]
	}
	return rest
}

// cfMergeTags overlays incoming tags onto existing ones: existing-key values are
// replaced, new keys appended. Insertion order is preserved so the listed tag
// order is stable (real AWS does not guarantee order, but a deterministic shape
// avoids spurious test churn).
func cfMergeTags(existing, incoming []CFTag) []CFTag {
	values := make(map[string]string, len(existing)+len(incoming))
	order := make([]string, 0, len(existing)+len(incoming))
	add := func(t CFTag) {
		if _, seen := values[t.Key]; !seen {
			order = append(order, t.Key)
		}
		values[t.Key] = t.Value
	}
	for _, t := range existing {
		add(t)
	}
	for _, t := range incoming {
		add(t)
	}
	merged := make([]CFTag, 0, len(order))
	for _, k := range order {
		merged = append(merged, CFTag{Key: k, Value: values[k]})
	}
	return merged
}

// cfDropTags returns existing tags minus any whose key is in keys.
func cfDropTags(existing []CFTag, keys []string) []CFTag {
	drop := make(map[string]bool, len(keys))
	for _, k := range keys {
		drop[k] = true
	}
	kept := make([]CFTag, 0, len(existing))
	for _, t := range existing {
		if !drop[t.Key] {
			kept = append(kept, t)
		}
	}
	return kept
}

func handleCFListTags(w http.ResponseWriter, r *http.Request) {
	arn := r.URL.Query().Get("Resource")
	if arn == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Resource query parameter is required")
		return
	}
	var tags []CFTag
	if fname := cfFunctionNameFromARN(arn); fname != "" {
		stored, ok := cfFunctions.Get(fname)
		if !ok {
			cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified resource does not exist.")
			return
		}
		tags = stored.Tags
	} else {
		stored, ok := cfDistributions.Get(cfDistributionIDFromARN(arn))
		if !ok {
			cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified resource does not exist.")
			return
		}
		tags = stored.Tags
	}
	resp := struct {
		XMLName xml.Name `xml:"Tags"`
		Xmlns   string   `xml:"xmlns,attr,omitempty"`
		Items   []CFTag  `xml:"Items>Tag"`
	}{Xmlns: cfNamespace, Items: tags}
	cfWriteXML(w, http.StatusOK, resp)
}

func handleCFTagDispatch(w http.ResponseWriter, r *http.Request) {
	op := r.URL.Query().Get("Operation")
	switch op {
	case "Tag":
		handleCFTagResource(w, r)
	case "Untag":
		handleCFUntagResource(w, r)
	default:
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Operation query parameter must be Tag or Untag")
	}
}

func handleCFTagResource(w http.ResponseWriter, r *http.Request) {
	arn := r.URL.Query().Get("Resource")
	var body struct {
		XMLName xml.Name `xml:"Tags"`
		Items   []CFTag  `xml:"Items>Tag"`
	}
	if err := xml.NewDecoder(r.Body).Decode(&body); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode Tags: "+err.Error())
		return
	}
	if fname := cfFunctionNameFromARN(arn); fname != "" {
		stored, ok := cfFunctions.Get(fname)
		if !ok {
			cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified resource does not exist.")
			return
		}
		stored.Tags = cfMergeTags(stored.Tags, body.Items)
		cfFunctions.Put(fname, stored)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	id := cfDistributionIDFromARN(arn)
	stored, ok := cfDistributions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified resource does not exist.")
		return
	}
	stored.Tags = cfMergeTags(stored.Tags, body.Items)
	cfDistributions.Put(id, stored)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFUntagResource(w http.ResponseWriter, r *http.Request) {
	arn := r.URL.Query().Get("Resource")
	var body struct {
		XMLName xml.Name `xml:"TagKeys"`
		Items   []string `xml:"Items>Key"`
	}
	if err := xml.NewDecoder(r.Body).Decode(&body); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode TagKeys: "+err.Error())
		return
	}
	if fname := cfFunctionNameFromARN(arn); fname != "" {
		stored, ok := cfFunctions.Get(fname)
		if !ok {
			cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified resource does not exist.")
			return
		}
		stored.Tags = cfDropTags(stored.Tags, body.Items)
		cfFunctions.Put(fname, stored)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	id := cfDistributionIDFromARN(arn)
	stored, ok := cfDistributions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchResource", "The specified resource does not exist.")
		return
	}
	stored.Tags = cfDropTags(stored.Tags, body.Items)
	cfDistributions.Put(id, stored)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListOACs(w http.ResponseWriter, r *http.Request) {
	items := []CFOriginAccessControlSummary{}
	for _, stored := range cfOriginAccessControls.List() {
		c := stored.OAC.OriginAccessControlConfig
		items = append(items, CFOriginAccessControlSummary{
			Id:                            stored.OAC.Id,
			Name:                          c.Name,
			Description:                   c.Description,
			SigningProtocol:               c.SigningProtocol,
			SigningBehavior:               c.SigningBehavior,
			OriginAccessControlOriginType: c.OriginAccessControlOriginType,
		})
	}
	list := CFOriginAccessControlList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	}
	cfWriteXML(w, http.StatusOK, list)
}
