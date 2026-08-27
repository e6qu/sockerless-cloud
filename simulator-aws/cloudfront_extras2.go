package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// CloudFront extra resources, part 2 — field-level encryption configs and
// profiles, key value stores, real-time log configs, streaming distributions,
// VPC origins, and Anycast IP lists. Same REST + XML wire as the Distribution
// and policy endpoints in cloudfront.go / cloudfront_policies.go: each is a
// named resource carrying an Id + ARN + an ETag returned in the response
// header and required as If-Match on Update/Delete.

type CFContentTypeProfileConfig struct {
	ForwardWhenContentTypeIsUnknown bool `xml:"ForwardWhenContentTypeIsUnknown"`
}

type CFQueryArgProfileConfig struct {
	ForwardWhenQueryArgProfileIsUnknown bool `xml:"ForwardWhenQueryArgProfileIsUnknown"`
}

type CFFieldLevelEncryptionConfig struct {
	XMLName                  xml.Name                    `xml:"FieldLevelEncryptionConfig"`
	Xmlns                    string                      `xml:"xmlns,attr,omitempty"`
	CallerReference          string                      `xml:"CallerReference"`
	Comment                  string                      `xml:"Comment,omitempty"`
	QueryArgProfileConfig    *CFQueryArgProfileConfig    `xml:"QueryArgProfileConfig,omitempty"`
	ContentTypeProfileConfig *CFContentTypeProfileConfig `xml:"ContentTypeProfileConfig,omitempty"`
}

type CFFieldLevelEncryption struct {
	XMLName                    xml.Name                     `xml:"FieldLevelEncryption"`
	Xmlns                      string                       `xml:"xmlns,attr,omitempty"`
	Id                         string                       `xml:"Id"`
	LastModifiedTime           string                       `xml:"LastModifiedTime"`
	FieldLevelEncryptionConfig CFFieldLevelEncryptionConfig `xml:"FieldLevelEncryptionConfig"`
}

type CFFieldLevelEncryptionSummary struct {
	Id                       string                      `xml:"Id"`
	LastModifiedTime         string                      `xml:"LastModifiedTime"`
	Comment                  string                      `xml:"Comment,omitempty"`
	QueryArgProfileConfig    *CFQueryArgProfileConfig    `xml:"QueryArgProfileConfig,omitempty"`
	ContentTypeProfileConfig *CFContentTypeProfileConfig `xml:"ContentTypeProfileConfig,omitempty"`
}

type CFFieldLevelEncryptionList struct {
	XMLName    xml.Name                        `xml:"FieldLevelEncryptionList"`
	Xmlns      string                          `xml:"xmlns,attr,omitempty"`
	NextMarker string                          `xml:"NextMarker,omitempty"`
	MaxItems   int                             `xml:"MaxItems"`
	Quantity   int                             `xml:"Quantity"`
	Items      []CFFieldLevelEncryptionSummary `xml:"Items>FieldLevelEncryptionSummary,omitempty"`
}

type cfStoredFLE struct {
	FLE  CFFieldLevelEncryption
	ETag string
}

type CFFieldPatterns struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>FieldPattern,omitempty"`
}

type CFEncryptionEntity struct {
	PublicKeyId   string          `xml:"PublicKeyId"`
	ProviderId    string          `xml:"ProviderId"`
	FieldPatterns CFFieldPatterns `xml:"FieldPatterns"`
}

type CFEncryptionEntities struct {
	Quantity int                  `xml:"Quantity"`
	Items    []CFEncryptionEntity `xml:"Items>EncryptionEntity,omitempty"`
}

type CFFieldLevelEncryptionProfileConfig struct {
	XMLName            xml.Name             `xml:"FieldLevelEncryptionProfileConfig"`
	Xmlns              string               `xml:"xmlns,attr,omitempty"`
	Name               string               `xml:"Name"`
	CallerReference    string               `xml:"CallerReference"`
	Comment            string               `xml:"Comment,omitempty"`
	EncryptionEntities CFEncryptionEntities `xml:"EncryptionEntities"`
}

type CFFieldLevelEncryptionProfile struct {
	XMLName                           xml.Name                            `xml:"FieldLevelEncryptionProfile"`
	Xmlns                             string                              `xml:"xmlns,attr,omitempty"`
	Id                                string                              `xml:"Id"`
	LastModifiedTime                  string                              `xml:"LastModifiedTime"`
	FieldLevelEncryptionProfileConfig CFFieldLevelEncryptionProfileConfig `xml:"FieldLevelEncryptionProfileConfig"`
}

type CFFieldLevelEncryptionProfileSummary struct {
	Id                 string               `xml:"Id"`
	LastModifiedTime   string               `xml:"LastModifiedTime"`
	Name               string               `xml:"Name"`
	EncryptionEntities CFEncryptionEntities `xml:"EncryptionEntities"`
	Comment            string               `xml:"Comment,omitempty"`
}

type CFFieldLevelEncryptionProfileList struct {
	XMLName    xml.Name                               `xml:"FieldLevelEncryptionProfileList"`
	Xmlns      string                                 `xml:"xmlns,attr,omitempty"`
	NextMarker string                                 `xml:"NextMarker,omitempty"`
	MaxItems   int                                    `xml:"MaxItems"`
	Quantity   int                                    `xml:"Quantity"`
	Items      []CFFieldLevelEncryptionProfileSummary `xml:"Items>FieldLevelEncryptionProfileSummary,omitempty"`
}

type cfStoredFLEProfile struct {
	Profile CFFieldLevelEncryptionProfile
	ETag    string
}

// CFKeyValueStore mirrors the SDK KeyValueStore shape, which CloudFront
// returns as the response payload directly (httpPayload binding).
type CFKeyValueStore struct {
	XMLName          xml.Name `xml:"KeyValueStore"`
	Xmlns            string   `xml:"xmlns,attr,omitempty"`
	Name             string   `xml:"Name"`
	Id               string   `xml:"Id"`
	Comment          string   `xml:"Comment"`
	ARN              string   `xml:"ARN"`
	Status           string   `xml:"Status,omitempty"`
	LastModifiedTime string   `xml:"LastModifiedTime"`
}

// CFKeyValueStoreCreateRequest is the XML body the SDK/CLI send to
// CreateKeyValueStore (the CreateKeyValueStoreRequest input shape).
type CFKeyValueStoreCreateRequest struct {
	XMLName xml.Name `xml:"CreateKeyValueStoreRequest"`
	Name    string   `xml:"Name"`
	Comment string   `xml:"Comment"`
}

// CFKeyValueStoreUpdateRequest is the body of UpdateKeyValueStore.
type CFKeyValueStoreUpdateRequest struct {
	XMLName xml.Name `xml:"UpdateKeyValueStoreRequest"`
	Comment string   `xml:"Comment"`
}

// CFKeyValueStoreSummary is the list item; the list element wraps the same
// KeyValueStore shape under <KeyValueStore>.
type CFKeyValueStoreSummary struct {
	Name             string `xml:"Name"`
	Id               string `xml:"Id"`
	Comment          string `xml:"Comment"`
	ARN              string `xml:"ARN"`
	Status           string `xml:"Status,omitempty"`
	LastModifiedTime string `xml:"LastModifiedTime"`
}

type CFKeyValueStoreList struct {
	XMLName    xml.Name                 `xml:"KeyValueStoreList"`
	Xmlns      string                   `xml:"xmlns,attr,omitempty"`
	NextMarker string                   `xml:"NextMarker,omitempty"`
	MaxItems   int                      `xml:"MaxItems"`
	Quantity   int                      `xml:"Quantity"`
	Items      []CFKeyValueStoreSummary `xml:"Items>KeyValueStore,omitempty"`
}

type cfStoredKVS struct {
	KVS  CFKeyValueStore
	ETag string
}

type CFKinesisStreamConfig struct {
	RoleARN   string `xml:"RoleARN"`
	StreamARN string `xml:"StreamARN"`
}

type CFEndPoint struct {
	StreamType          string                 `xml:"StreamType"`
	KinesisStreamConfig *CFKinesisStreamConfig `xml:"KinesisStreamConfig,omitempty"`
}

type CFRealtimeLogConfig struct {
	XMLName      xml.Name     `xml:"RealtimeLogConfig"`
	Xmlns        string       `xml:"xmlns,attr,omitempty"`
	ARN          string       `xml:"ARN"`
	Name         string       `xml:"Name"`
	SamplingRate int64        `xml:"SamplingRate"`
	EndPoints    []CFEndPoint `xml:"EndPoints>member,omitempty"`
	Fields       []string     `xml:"Fields>Field,omitempty"`
}

// CFRealtimeLogConfigCreateRequest mirrors CreateRealtimeLogConfigRequest.
type CFRealtimeLogConfigCreateRequest struct {
	XMLName      xml.Name     `xml:"CreateRealtimeLogConfigRequest"`
	EndPoints    []CFEndPoint `xml:"EndPoints>member,omitempty"`
	Fields       []string     `xml:"Fields>Field,omitempty"`
	Name         string       `xml:"Name"`
	SamplingRate int64        `xml:"SamplingRate"`
}

// CFRealtimeLogConfigUpdateRequest mirrors UpdateRealtimeLogConfigRequest.
type CFRealtimeLogConfigUpdateRequest struct {
	XMLName      xml.Name     `xml:"UpdateRealtimeLogConfigRequest"`
	EndPoints    []CFEndPoint `xml:"EndPoints>member,omitempty"`
	Fields       []string     `xml:"Fields>Field,omitempty"`
	Name         string       `xml:"Name"`
	ARN          string       `xml:"ARN"`
	SamplingRate int64        `xml:"SamplingRate"`
}

// CFRealtimeLogConfigLookupRequest mirrors Get/DeleteRealtimeLogConfigRequest
// (the resource is identified by Name or ARN carried in the POST body).
type CFRealtimeLogConfigLookupRequest struct {
	Name string `xml:"Name"`
	ARN  string `xml:"ARN"`
}

type CFRealtimeLogConfigCreateResult struct {
	XMLName           xml.Name            `xml:"CreateRealtimeLogConfigResult"`
	Xmlns             string              `xml:"xmlns,attr,omitempty"`
	RealtimeLogConfig CFRealtimeLogConfig `xml:"RealtimeLogConfig"`
}

type CFRealtimeLogConfigGetResult struct {
	XMLName           xml.Name            `xml:"GetRealtimeLogConfigResult"`
	Xmlns             string              `xml:"xmlns,attr,omitempty"`
	RealtimeLogConfig CFRealtimeLogConfig `xml:"RealtimeLogConfig"`
}

type CFRealtimeLogConfigUpdateResult struct {
	XMLName           xml.Name            `xml:"UpdateRealtimeLogConfigResult"`
	Xmlns             string              `xml:"xmlns,attr,omitempty"`
	RealtimeLogConfig CFRealtimeLogConfig `xml:"RealtimeLogConfig"`
}

// CFRealtimeLogConfigMember serializes a list entry as <member> (the
// RealtimeLogConfigList member element name), overriding the standalone
// <RealtimeLogConfig> root the CFRealtimeLogConfig struct carries.
type CFRealtimeLogConfigMember struct {
	ARN          string       `xml:"ARN"`
	Name         string       `xml:"Name"`
	SamplingRate int64        `xml:"SamplingRate"`
	EndPoints    []CFEndPoint `xml:"EndPoints>member,omitempty"`
	Fields       []string     `xml:"Fields>Field,omitempty"`
}

type CFRealtimeLogConfigs struct {
	XMLName     xml.Name                    `xml:"RealtimeLogConfigs"`
	Xmlns       string                      `xml:"xmlns,attr,omitempty"`
	MaxItems    int                         `xml:"MaxItems"`
	IsTruncated bool                        `xml:"IsTruncated"`
	Marker      string                      `xml:"Marker"`
	NextMarker  string                      `xml:"NextMarker,omitempty"`
	Items       []CFRealtimeLogConfigMember `xml:"Items>member,omitempty"`
}

type CFS3Origin struct {
	DomainName           string `xml:"DomainName"`
	OriginAccessIdentity string `xml:"OriginAccessIdentity"`
}

type CFStreamingTrustedSigners struct {
	Enabled  bool     `xml:"Enabled"`
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>AwsAccountNumber,omitempty"`
}

type CFStreamingAliases struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>CNAME,omitempty"`
}

type CFStreamingLoggingConfig struct {
	Enabled bool   `xml:"Enabled"`
	Bucket  string `xml:"Bucket"`
	Prefix  string `xml:"Prefix"`
}

type CFStreamingDistributionConfig struct {
	XMLName         xml.Name                  `xml:"StreamingDistributionConfig"`
	Xmlns           string                    `xml:"xmlns,attr,omitempty"`
	CallerReference string                    `xml:"CallerReference"`
	S3Origin        CFS3Origin                `xml:"S3Origin"`
	Aliases         *CFStreamingAliases       `xml:"Aliases,omitempty"`
	Comment         string                    `xml:"Comment"`
	Logging         *CFStreamingLoggingConfig `xml:"Logging,omitempty"`
	TrustedSigners  CFStreamingTrustedSigners `xml:"TrustedSigners"`
	PriceClass      string                    `xml:"PriceClass,omitempty"`
	Enabled         bool                      `xml:"Enabled"`
}

type CFStreamingDistribution struct {
	XMLName                     xml.Name                      `xml:"StreamingDistribution"`
	Xmlns                       string                        `xml:"xmlns,attr,omitempty"`
	Id                          string                        `xml:"Id"`
	ARN                         string                        `xml:"ARN"`
	Status                      string                        `xml:"Status"`
	LastModifiedTime            string                        `xml:"LastModifiedTime,omitempty"`
	DomainName                  string                        `xml:"DomainName"`
	ActiveTrustedSigners        CFActiveTrustedSigners        `xml:"ActiveTrustedSigners"`
	StreamingDistributionConfig CFStreamingDistributionConfig `xml:"StreamingDistributionConfig"`
}

type CFStreamingDistributionSummary struct {
	Id               string                    `xml:"Id"`
	ARN              string                    `xml:"ARN"`
	Status           string                    `xml:"Status"`
	LastModifiedTime string                    `xml:"LastModifiedTime"`
	DomainName       string                    `xml:"DomainName"`
	S3Origin         CFS3Origin                `xml:"S3Origin"`
	Aliases          CFStreamingAliases        `xml:"Aliases"`
	TrustedSigners   CFStreamingTrustedSigners `xml:"TrustedSigners"`
	Comment          string                    `xml:"Comment"`
	PriceClass       string                    `xml:"PriceClass"`
	Enabled          bool                      `xml:"Enabled"`
}

type CFStreamingDistributionList struct {
	XMLName     xml.Name                         `xml:"StreamingDistributionList"`
	Xmlns       string                           `xml:"xmlns,attr,omitempty"`
	Marker      string                           `xml:"Marker"`
	NextMarker  string                           `xml:"NextMarker,omitempty"`
	MaxItems    int                              `xml:"MaxItems"`
	IsTruncated bool                             `xml:"IsTruncated"`
	Quantity    int                              `xml:"Quantity"`
	Items       []CFStreamingDistributionSummary `xml:"Items>StreamingDistributionSummary,omitempty"`
}

type cfStoredStreamingDist struct {
	Dist CFStreamingDistribution
	ETag string
}

type CFOriginSslProtocols struct {
	Quantity int      `xml:"Quantity"`
	Items    []string `xml:"Items>SslProtocol,omitempty"`
}

type CFVpcOriginEndpointConfig struct {
	XMLName              xml.Name              `xml:"VpcOriginEndpointConfig"`
	Xmlns                string                `xml:"xmlns,attr,omitempty"`
	Name                 string                `xml:"Name"`
	Arn                  string                `xml:"Arn"`
	HTTPPort             int                   `xml:"HTTPPort"`
	HTTPSPort            int                   `xml:"HTTPSPort"`
	OriginProtocolPolicy string                `xml:"OriginProtocolPolicy"`
	OriginSslProtocols   *CFOriginSslProtocols `xml:"OriginSslProtocols,omitempty"`
}

// CFVpcOriginCreateRequest is the CreateVpcOrigin body: the SDK wraps the
// endpoint config in a <CreateVpcOriginRequest> root element (unlike
// UpdateVpcOrigin, which sends the bare <VpcOriginEndpointConfig> payload).
type CFVpcOriginCreateRequest struct {
	XMLName                 xml.Name                  `xml:"CreateVpcOriginRequest"`
	VpcOriginEndpointConfig CFVpcOriginEndpointConfig `xml:"VpcOriginEndpointConfig"`
}

type CFVpcOrigin struct {
	XMLName                 xml.Name                  `xml:"VpcOrigin"`
	Xmlns                   string                    `xml:"xmlns,attr,omitempty"`
	Id                      string                    `xml:"Id"`
	Arn                     string                    `xml:"Arn"`
	AccountId               string                    `xml:"AccountId,omitempty"`
	Status                  string                    `xml:"Status"`
	CreatedTime             string                    `xml:"CreatedTime"`
	LastModifiedTime        string                    `xml:"LastModifiedTime"`
	VpcOriginEndpointConfig CFVpcOriginEndpointConfig `xml:"VpcOriginEndpointConfig"`
}

type CFVpcOriginSummary struct {
	Id                string `xml:"Id"`
	Name              string `xml:"Name"`
	Status            string `xml:"Status"`
	CreatedTime       string `xml:"CreatedTime"`
	LastModifiedTime  string `xml:"LastModifiedTime"`
	Arn               string `xml:"Arn"`
	AccountId         string `xml:"AccountId,omitempty"`
	OriginEndpointArn string `xml:"OriginEndpointArn"`
}

type CFVpcOriginList struct {
	XMLName     xml.Name             `xml:"VpcOriginList"`
	Xmlns       string               `xml:"xmlns,attr,omitempty"`
	Marker      string               `xml:"Marker"`
	NextMarker  string               `xml:"NextMarker,omitempty"`
	MaxItems    int                  `xml:"MaxItems"`
	IsTruncated bool                 `xml:"IsTruncated"`
	Quantity    int                  `xml:"Quantity"`
	Items       []CFVpcOriginSummary `xml:"Items>VpcOriginSummary,omitempty"`
}

type cfStoredVpcOrigin struct {
	Origin CFVpcOrigin
	ETag   string
}

// CFAnycastIpList — AnycastIps is a flat list of <AnycastIp> elements nested
// directly under <AnycastIps> (the AnycastIps shape is a Smithy list, not a
// Quantity/Items wrapper).
type CFAnycastIpList struct {
	XMLName          xml.Name `xml:"AnycastIpList"`
	Xmlns            string   `xml:"xmlns,attr,omitempty"`
	Id               string   `xml:"Id"`
	Name             string   `xml:"Name"`
	Status           string   `xml:"Status"`
	Arn              string   `xml:"Arn"`
	IpAddressType    string   `xml:"IpAddressType,omitempty"`
	AnycastIps       []string `xml:"AnycastIps>AnycastIp,omitempty"`
	IpCount          int      `xml:"IpCount"`
	LastModifiedTime string   `xml:"LastModifiedTime"`
}

// CFAnycastIpListCreateRequest mirrors CreateAnycastIpListRequest.
type CFAnycastIpListCreateRequest struct {
	XMLName       xml.Name `xml:"CreateAnycastIpListRequest"`
	Name          string   `xml:"Name"`
	IpCount       int      `xml:"IpCount"`
	IpAddressType string   `xml:"IpAddressType"`
}

type CFAnycastIpListSummary struct {
	Id               string `xml:"Id"`
	Name             string `xml:"Name"`
	Status           string `xml:"Status"`
	Arn              string `xml:"Arn"`
	IpCount          int    `xml:"IpCount"`
	LastModifiedTime string `xml:"LastModifiedTime"`
	IpAddressType    string `xml:"IpAddressType,omitempty"`
	ETag             string `xml:"ETag,omitempty"`
}

type CFAnycastIpListCollection struct {
	XMLName     xml.Name                 `xml:"AnycastIpListCollection"`
	Xmlns       string                   `xml:"xmlns,attr,omitempty"`
	Items       []CFAnycastIpListSummary `xml:"Items>AnycastIpListSummary,omitempty"`
	Marker      string                   `xml:"Marker"`
	NextMarker  string                   `xml:"NextMarker,omitempty"`
	MaxItems    int                      `xml:"MaxItems"`
	IsTruncated bool                     `xml:"IsTruncated"`
	Quantity    int                      `xml:"Quantity"`
}

type cfStoredAnycastIpList struct {
	List CFAnycastIpList
	ETag string
}

var (
	cfFLEConfigs             sim.Store[cfStoredFLE]
	cfFLEProfiles            sim.Store[cfStoredFLEProfile]
	cfKeyValueStores         sim.Store[cfStoredKVS]
	cfRealtimeLogConfigs     sim.Store[CFRealtimeLogConfig]
	cfStreamingDistributions sim.Store[cfStoredStreamingDist]
	cfVpcOrigins             sim.Store[cfStoredVpcOrigin]
	cfAnycastIpLists         sim.Store[cfStoredAnycastIpList]
)

// cfRealtimeLogARN builds the real-time-log-config ARN for a given name.
func cfRealtimeLogARN(name string) string {
	return fmt.Sprintf("arn:aws:cloudfront::%s:realtime-log-config/%s", awsAccountID(), name)
}

// registerCloudFrontExtras2 mounts the field-level-encryption, key-value-store,
// real-time-log, streaming-distribution, VPC-origin, and Anycast-IP-list CRUD
// endpoints onto the same mux. Invoked from registerCloudFront in cloudfront.go.
func registerCloudFrontExtras2(srv *sim.Server) {
	cfFLEConfigs = sim.MakeStore[cfStoredFLE](srv.DB(), "cloudfront_fle_configs")
	cfFLEProfiles = sim.MakeStore[cfStoredFLEProfile](srv.DB(), "cloudfront_fle_profiles")
	cfKeyValueStores = sim.MakeStore[cfStoredKVS](srv.DB(), "cloudfront_key_value_stores")
	cfRealtimeLogConfigs = sim.MakeStore[CFRealtimeLogConfig](srv.DB(), "cloudfront_realtime_log_configs")
	cfStreamingDistributions = sim.MakeStore[cfStoredStreamingDist](srv.DB(), "cloudfront_streaming_distributions")
	cfVpcOrigins = sim.MakeStore[cfStoredVpcOrigin](srv.DB(), "cloudfront_vpc_origins")
	cfAnycastIpLists = sim.MakeStore[cfStoredAnycastIpList](srv.DB(), "cloudfront_anycast_ip_lists")

	mux := srv
	v := cfAPIVersion

	fleRes := cloudTrailRESTResource("AWS::CloudFront::FieldLevelEncryption", "Id")
	fleProfileRes := cloudTrailRESTResource("AWS::CloudFront::FieldLevelEncryptionProfile", "Id")
	kvsRes := cloudTrailRESTResource("AWS::CloudFront::KeyValueStore", "Name")
	streamingRes := cloudTrailRESTResource("AWS::CloudFront::StreamingDistribution", "Id")
	vpcOriginRes := cloudTrailRESTResource("AWS::CloudFront::VpcOrigin", "Id")
	anycastRes := cloudTrailRESTResource("AWS::CloudFront::AnycastIpList", "Id")

	// Field-Level Encryption Config
	mux.HandleFunc("POST /"+v+"/field-level-encryption", cloudTrailRecordedREST("CreateFieldLevelEncryptionConfig", "cloudfront.amazonaws.com", nil, handleCFCreateFLE))
	mux.HandleFunc("GET /"+v+"/field-level-encryption", cloudTrailRecordedREST("ListFieldLevelEncryptionConfigs", "cloudfront.amazonaws.com", nil, handleCFListFLE))
	mux.HandleFunc("GET /"+v+"/field-level-encryption/{Id}", cloudTrailRecordedREST("GetFieldLevelEncryption", "cloudfront.amazonaws.com", fleRes, handleCFGetFLE))
	mux.HandleFunc("GET /"+v+"/field-level-encryption/{Id}/config", cloudTrailRecordedREST("GetFieldLevelEncryptionConfig", "cloudfront.amazonaws.com", fleRes, handleCFGetFLEConfig))
	mux.HandleFunc("PUT /"+v+"/field-level-encryption/{Id}/config", cloudTrailRecordedREST("UpdateFieldLevelEncryptionConfig", "cloudfront.amazonaws.com", fleRes, handleCFUpdateFLE))
	mux.HandleFunc("DELETE /"+v+"/field-level-encryption/{Id}", cloudTrailRecordedREST("DeleteFieldLevelEncryptionConfig", "cloudfront.amazonaws.com", fleRes, handleCFDeleteFLE))

	// Field-Level Encryption Profile
	mux.HandleFunc("POST /"+v+"/field-level-encryption-profile", cloudTrailRecordedREST("CreateFieldLevelEncryptionProfile", "cloudfront.amazonaws.com", nil, handleCFCreateFLEProfile))
	mux.HandleFunc("GET /"+v+"/field-level-encryption-profile", cloudTrailRecordedREST("ListFieldLevelEncryptionProfiles", "cloudfront.amazonaws.com", nil, handleCFListFLEProfiles))
	mux.HandleFunc("GET /"+v+"/field-level-encryption-profile/{Id}", cloudTrailRecordedREST("GetFieldLevelEncryptionProfile", "cloudfront.amazonaws.com", fleProfileRes, handleCFGetFLEProfile))
	mux.HandleFunc("GET /"+v+"/field-level-encryption-profile/{Id}/config", cloudTrailRecordedREST("GetFieldLevelEncryptionProfileConfig", "cloudfront.amazonaws.com", fleProfileRes, handleCFGetFLEProfileConfig))
	mux.HandleFunc("PUT /"+v+"/field-level-encryption-profile/{Id}/config", cloudTrailRecordedREST("UpdateFieldLevelEncryptionProfile", "cloudfront.amazonaws.com", fleProfileRes, handleCFUpdateFLEProfile))
	mux.HandleFunc("DELETE /"+v+"/field-level-encryption-profile/{Id}", cloudTrailRecordedREST("DeleteFieldLevelEncryptionProfile", "cloudfront.amazonaws.com", fleProfileRes, handleCFDeleteFLEProfile))

	// Key Value Store
	mux.HandleFunc("POST /"+v+"/key-value-store", cloudTrailRecordedREST("CreateKeyValueStore", "cloudfront.amazonaws.com", nil, handleCFCreateKVS))
	mux.HandleFunc("GET /"+v+"/key-value-store", cloudTrailRecordedREST("ListKeyValueStores", "cloudfront.amazonaws.com", nil, handleCFListKVS))
	mux.HandleFunc("GET /"+v+"/key-value-store/{Name}", cloudTrailRecordedREST("DescribeKeyValueStore", "cloudfront.amazonaws.com", kvsRes, handleCFDescribeKVS))
	mux.HandleFunc("PUT /"+v+"/key-value-store/{Name}", cloudTrailRecordedREST("UpdateKeyValueStore", "cloudfront.amazonaws.com", kvsRes, handleCFUpdateKVS))
	mux.HandleFunc("DELETE /"+v+"/key-value-store/{Name}", cloudTrailRecordedREST("DeleteKeyValueStore", "cloudfront.amazonaws.com", kvsRes, handleCFDeleteKVS))

	// Real-time Log Config (POST-based; identified by Name/ARN in body)
	mux.HandleFunc("POST /"+v+"/realtime-log-config", cloudTrailRecordedREST("CreateRealtimeLogConfig", "cloudfront.amazonaws.com", nil, handleCFCreateRealtimeLog))
	mux.HandleFunc("GET /"+v+"/realtime-log-config", cloudTrailRecordedREST("ListRealtimeLogConfigs", "cloudfront.amazonaws.com", nil, handleCFListRealtimeLog))
	mux.HandleFunc("PUT /"+v+"/realtime-log-config", cloudTrailRecordedREST("UpdateRealtimeLogConfig", "cloudfront.amazonaws.com", nil, handleCFUpdateRealtimeLog))
	mux.HandleFunc("POST /"+v+"/get-realtime-log-config", cloudTrailRecordedREST("GetRealtimeLogConfig", "cloudfront.amazonaws.com", nil, handleCFGetRealtimeLog))
	mux.HandleFunc("POST /"+v+"/delete-realtime-log-config", cloudTrailRecordedREST("DeleteRealtimeLogConfig", "cloudfront.amazonaws.com", nil, handleCFDeleteRealtimeLog))

	// Streaming Distribution — the create path serves both
	// CreateStreamingDistribution and CreateStreamingDistributionWithTags, which
	// the SDK both send to POST /streaming-distribution (the WithTags variant
	// adds a ?WithTags query flag and wraps the config in
	// StreamingDistributionConfigWithTags). The dynamic dispatcher records the
	// right op name per request.
	mux.HandleFunc("POST /"+v+"/streaming-distribution", cloudTrailRecordedRESTDynamic(cfCreateStreamingDistributionOperationName, "cloudfront.amazonaws.com", nil, handleCFCreateStreamingDist))
	mux.HandleFunc("GET /"+v+"/streaming-distribution", cloudTrailRecordedREST("ListStreamingDistributions", "cloudfront.amazonaws.com", nil, handleCFListStreamingDist))
	mux.HandleFunc("GET /"+v+"/streaming-distribution/{Id}", cloudTrailRecordedREST("GetStreamingDistribution", "cloudfront.amazonaws.com", streamingRes, handleCFGetStreamingDist))
	mux.HandleFunc("GET /"+v+"/streaming-distribution/{Id}/config", cloudTrailRecordedREST("GetStreamingDistributionConfig", "cloudfront.amazonaws.com", streamingRes, handleCFGetStreamingDistConfig))
	mux.HandleFunc("PUT /"+v+"/streaming-distribution/{Id}/config", cloudTrailRecordedREST("UpdateStreamingDistribution", "cloudfront.amazonaws.com", streamingRes, handleCFUpdateStreamingDist))
	mux.HandleFunc("DELETE /"+v+"/streaming-distribution/{Id}", cloudTrailRecordedREST("DeleteStreamingDistribution", "cloudfront.amazonaws.com", streamingRes, handleCFDeleteStreamingDist))

	// VPC Origin
	mux.HandleFunc("POST /"+v+"/vpc-origin", cloudTrailRecordedREST("CreateVpcOrigin", "cloudfront.amazonaws.com", nil, handleCFCreateVpcOrigin))
	mux.HandleFunc("GET /"+v+"/vpc-origin", cloudTrailRecordedREST("ListVpcOrigins", "cloudfront.amazonaws.com", nil, handleCFListVpcOrigins))
	mux.HandleFunc("GET /"+v+"/vpc-origin/{Id}", cloudTrailRecordedREST("GetVpcOrigin", "cloudfront.amazonaws.com", vpcOriginRes, handleCFGetVpcOrigin))
	mux.HandleFunc("PUT /"+v+"/vpc-origin/{Id}", cloudTrailRecordedREST("UpdateVpcOrigin", "cloudfront.amazonaws.com", vpcOriginRes, handleCFUpdateVpcOrigin))
	mux.HandleFunc("DELETE /"+v+"/vpc-origin/{Id}", cloudTrailRecordedREST("DeleteVpcOrigin", "cloudfront.amazonaws.com", vpcOriginRes, handleCFDeleteVpcOrigin))

	// Anycast IP List
	mux.HandleFunc("POST /"+v+"/anycast-ip-list", cloudTrailRecordedREST("CreateAnycastIpList", "cloudfront.amazonaws.com", nil, handleCFCreateAnycastIpList))
	mux.HandleFunc("GET /"+v+"/anycast-ip-list", cloudTrailRecordedREST("ListAnycastIpLists", "cloudfront.amazonaws.com", nil, handleCFListAnycastIpLists))
	mux.HandleFunc("GET /"+v+"/anycast-ip-list/{Id}", cloudTrailRecordedREST("GetAnycastIpList", "cloudfront.amazonaws.com", anycastRes, handleCFGetAnycastIpList))
	mux.HandleFunc("DELETE /"+v+"/anycast-ip-list/{Id}", cloudTrailRecordedREST("DeleteAnycastIpList", "cloudfront.amazonaws.com", anycastRes, handleCFDeleteAnycastIpList))
}

func handleCFCreateFLE(w http.ResponseWriter, r *http.Request) {
	var cfg CFFieldLevelEncryptionConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode FieldLevelEncryptionConfig: "+err.Error())
		return
	}
	if cfg.CallerReference == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "CallerReference is required")
		return
	}
	cfg.Xmlns = ""
	id := cfRandomID("FLE")
	etag := cfETag()
	fle := CFFieldLevelEncryption{
		Xmlns:                      cfNamespace,
		Id:                         id,
		LastModifiedTime:           cfNowISO(),
		FieldLevelEncryptionConfig: cfg,
	}
	cfFLEConfigs.Put(id, cfStoredFLE{FLE: fle, ETag: etag})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/field-level-encryption/"+id)
	cfWriteXML(w, http.StatusCreated, fle)
}

func handleCFGetFLE(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfFLEConfigs.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchFieldLevelEncryptionConfig", "The specified configuration for field-level encryption doesn't exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.FLE)
}

func handleCFGetFLEConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfFLEConfigs.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchFieldLevelEncryptionConfig", "The specified configuration for field-level encryption doesn't exist.")
		return
	}
	cfg := stored.FLE.FieldLevelEncryptionConfig
	cfg.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, cfg)
}

func handleCFUpdateFLE(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfFLEConfigs.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchFieldLevelEncryptionConfig", "The specified configuration for field-level encryption doesn't exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	var cfg CFFieldLevelEncryptionConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode FieldLevelEncryptionConfig: "+err.Error())
		return
	}
	cfg.Xmlns = ""
	newETag := cfETag()
	stored.FLE.FieldLevelEncryptionConfig = cfg
	stored.FLE.LastModifiedTime = cfNowISO()
	stored.ETag = newETag
	cfFLEConfigs.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.FLE)
}

func handleCFDeleteFLE(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfFLEConfigs.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchFieldLevelEncryptionConfig", "The specified configuration for field-level encryption doesn't exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	cfFLEConfigs.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListFLE(w http.ResponseWriter, _ *http.Request) {
	items := []CFFieldLevelEncryptionSummary{}
	for _, stored := range cfFLEConfigs.List() {
		items = append(items, CFFieldLevelEncryptionSummary{
			Id:                       stored.FLE.Id,
			LastModifiedTime:         stored.FLE.LastModifiedTime,
			Comment:                  stored.FLE.FieldLevelEncryptionConfig.Comment,
			QueryArgProfileConfig:    stored.FLE.FieldLevelEncryptionConfig.QueryArgProfileConfig,
			ContentTypeProfileConfig: stored.FLE.FieldLevelEncryptionConfig.ContentTypeProfileConfig,
		})
	}
	cfWriteXML(w, http.StatusOK, CFFieldLevelEncryptionList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	})
}

func handleCFCreateFLEProfile(w http.ResponseWriter, r *http.Request) {
	var cfg CFFieldLevelEncryptionProfileConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode FieldLevelEncryptionProfileConfig: "+err.Error())
		return
	}
	if cfg.CallerReference == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "CallerReference is required")
		return
	}
	cfg.Xmlns = ""
	id := cfRandomID("FLEP")
	etag := cfETag()
	profile := CFFieldLevelEncryptionProfile{
		Xmlns:                             cfNamespace,
		Id:                                id,
		LastModifiedTime:                  cfNowISO(),
		FieldLevelEncryptionProfileConfig: cfg,
	}
	cfFLEProfiles.Put(id, cfStoredFLEProfile{Profile: profile, ETag: etag})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/field-level-encryption-profile/"+id)
	cfWriteXML(w, http.StatusCreated, profile)
}

func handleCFGetFLEProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfFLEProfiles.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchFieldLevelEncryptionProfile", "The specified profile for field-level encryption doesn't exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.Profile)
}

func handleCFGetFLEProfileConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfFLEProfiles.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchFieldLevelEncryptionProfile", "The specified profile for field-level encryption doesn't exist.")
		return
	}
	cfg := stored.Profile.FieldLevelEncryptionProfileConfig
	cfg.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, cfg)
}

func handleCFUpdateFLEProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfFLEProfiles.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchFieldLevelEncryptionProfile", "The specified profile for field-level encryption doesn't exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	var cfg CFFieldLevelEncryptionProfileConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode FieldLevelEncryptionProfileConfig: "+err.Error())
		return
	}
	cfg.Xmlns = ""
	newETag := cfETag()
	stored.Profile.FieldLevelEncryptionProfileConfig = cfg
	stored.Profile.LastModifiedTime = cfNowISO()
	stored.ETag = newETag
	cfFLEProfiles.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.Profile)
}

func handleCFDeleteFLEProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfFLEProfiles.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchFieldLevelEncryptionProfile", "The specified profile for field-level encryption doesn't exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	cfFLEProfiles.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListFLEProfiles(w http.ResponseWriter, _ *http.Request) {
	items := []CFFieldLevelEncryptionProfileSummary{}
	for _, stored := range cfFLEProfiles.List() {
		items = append(items, CFFieldLevelEncryptionProfileSummary{
			Id:                 stored.Profile.Id,
			LastModifiedTime:   stored.Profile.LastModifiedTime,
			Name:               stored.Profile.FieldLevelEncryptionProfileConfig.Name,
			EncryptionEntities: stored.Profile.FieldLevelEncryptionProfileConfig.EncryptionEntities,
			Comment:            stored.Profile.FieldLevelEncryptionProfileConfig.Comment,
		})
	}
	cfWriteXML(w, http.StatusOK, CFFieldLevelEncryptionProfileList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	})
}

func cfKVSARN(name string) string {
	return fmt.Sprintf("arn:aws:cloudfront::%s:key-value-store/%s", awsAccountID(), name)
}

func handleCFCreateKVS(w http.ResponseWriter, r *http.Request) {
	var req CFKeyValueStoreCreateRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode CreateKeyValueStoreRequest: "+err.Error())
		return
	}
	if req.Name == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Name is required")
		return
	}
	if _, ok := cfKeyValueStores.Get(req.Name); ok {
		cfWriteError(w, http.StatusConflict, "EntityAlreadyExists", "The key value store already exists.")
		return
	}
	etag := cfETag()
	kvs := CFKeyValueStore{
		Xmlns:            cfNamespace,
		Name:             req.Name,
		Id:               cfRandomID("KVS"),
		Comment:          req.Comment,
		ARN:              cfKVSARN(req.Name),
		Status:           "PROVISIONING",
		LastModifiedTime: cfNowISO(),
	}
	cfKeyValueStores.Put(req.Name, cfStoredKVS{KVS: kvs, ETag: etag})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/key-value-store/"+req.Name)
	cfWriteXML(w, http.StatusCreated, kvs)
}

func handleCFDescribeKVS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("Name")
	stored, ok := cfKeyValueStores.Get(name)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The key value store does not exist.")
		return
	}
	// A described key value store has finished provisioning.
	if stored.KVS.Status != "READY" {
		stored.KVS.Status = "READY"
		cfKeyValueStores.Put(name, stored)
	}
	stored.KVS.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.KVS)
}

func handleCFUpdateKVS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("Name")
	stored, ok := cfKeyValueStores.Get(name)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The key value store does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	var req CFKeyValueStoreUpdateRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode UpdateKeyValueStoreRequest: "+err.Error())
		return
	}
	newETag := cfETag()
	stored.KVS.Comment = req.Comment
	stored.KVS.Status = "READY"
	stored.KVS.LastModifiedTime = cfNowISO()
	stored.ETag = newETag
	cfKeyValueStores.Put(name, stored)
	stored.KVS.Xmlns = cfNamespace
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.KVS)
}

func handleCFDeleteKVS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("Name")
	stored, ok := cfKeyValueStores.Get(name)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The key value store does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	cfKeyValueStores.Delete(name)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListKVS(w http.ResponseWriter, _ *http.Request) {
	items := []CFKeyValueStoreSummary{}
	for _, stored := range cfKeyValueStores.List() {
		items = append(items, CFKeyValueStoreSummary{
			Name:             stored.KVS.Name,
			Id:               stored.KVS.Id,
			Comment:          stored.KVS.Comment,
			ARN:              stored.KVS.ARN,
			Status:           stored.KVS.Status,
			LastModifiedTime: stored.KVS.LastModifiedTime,
		})
	}
	cfWriteXML(w, http.StatusOK, CFKeyValueStoreList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	})
}

// cfRealtimeLogKey resolves a real-time log config store key from a request
// that identifies the resource by Name or ARN. Returns the key (Name) or "".
func cfRealtimeLogKey(name, arn string) string {
	if name != "" {
		return name
	}
	if arn != "" {
		// ARN form: arn:aws:cloudfront::<acct>:realtime-log-config/<name>
		if idx := strings.LastIndex(arn, "/"); idx >= 0 {
			return arn[idx+1:]
		}
	}
	return ""
}

func handleCFCreateRealtimeLog(w http.ResponseWriter, r *http.Request) {
	var req CFRealtimeLogConfigCreateRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode CreateRealtimeLogConfigRequest: "+err.Error())
		return
	}
	if req.Name == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Name is required")
		return
	}
	if _, ok := cfRealtimeLogConfigs.Get(req.Name); ok {
		cfWriteError(w, http.StatusConflict, "RealtimeLogConfigAlreadyExists", "A real-time log configuration with this name already exists.")
		return
	}
	rlc := CFRealtimeLogConfig{
		ARN:          cfRealtimeLogARN(req.Name),
		Name:         req.Name,
		SamplingRate: req.SamplingRate,
		EndPoints:    req.EndPoints,
		Fields:       req.Fields,
	}
	cfRealtimeLogConfigs.Put(req.Name, rlc)
	rlc.Xmlns = cfNamespace
	w.Header().Set("Location", rlc.ARN)
	cfWriteXML(w, http.StatusCreated, CFRealtimeLogConfigCreateResult{RealtimeLogConfig: rlc})
}

func handleCFGetRealtimeLog(w http.ResponseWriter, r *http.Request) {
	var req CFRealtimeLogConfigLookupRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode GetRealtimeLogConfigRequest: "+err.Error())
		return
	}
	key := cfRealtimeLogKey(req.Name, req.ARN)
	rlc, ok := cfRealtimeLogConfigs.Get(key)
	if key == "" || !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchRealtimeLogConfig", "The real-time log configuration does not exist.")
		return
	}
	rlc.Xmlns = cfNamespace
	cfWriteXML(w, http.StatusOK, CFRealtimeLogConfigGetResult{RealtimeLogConfig: rlc})
}

func handleCFUpdateRealtimeLog(w http.ResponseWriter, r *http.Request) {
	var req CFRealtimeLogConfigUpdateRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode UpdateRealtimeLogConfigRequest: "+err.Error())
		return
	}
	key := cfRealtimeLogKey(req.Name, req.ARN)
	rlc, ok := cfRealtimeLogConfigs.Get(key)
	if key == "" || !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchRealtimeLogConfig", "The real-time log configuration does not exist.")
		return
	}
	if req.EndPoints != nil {
		rlc.EndPoints = req.EndPoints
	}
	if req.Fields != nil {
		rlc.Fields = req.Fields
	}
	if req.SamplingRate != 0 {
		rlc.SamplingRate = req.SamplingRate
	}
	cfRealtimeLogConfigs.Put(key, rlc)
	rlc.Xmlns = cfNamespace
	cfWriteXML(w, http.StatusOK, CFRealtimeLogConfigUpdateResult{RealtimeLogConfig: rlc})
}

func handleCFDeleteRealtimeLog(w http.ResponseWriter, r *http.Request) {
	var req CFRealtimeLogConfigLookupRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode DeleteRealtimeLogConfigRequest: "+err.Error())
		return
	}
	key := cfRealtimeLogKey(req.Name, req.ARN)
	if _, ok := cfRealtimeLogConfigs.Get(key); key == "" || !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchRealtimeLogConfig", "The real-time log configuration does not exist.")
		return
	}
	cfRealtimeLogConfigs.Delete(key)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListRealtimeLog(w http.ResponseWriter, _ *http.Request) {
	items := []CFRealtimeLogConfigMember{}
	for _, rlc := range cfRealtimeLogConfigs.List() {
		items = append(items, CFRealtimeLogConfigMember{
			ARN:          rlc.ARN,
			Name:         rlc.Name,
			SamplingRate: rlc.SamplingRate,
			EndPoints:    rlc.EndPoints,
			Fields:       rlc.Fields,
		})
	}
	cfWriteXML(w, http.StatusOK, CFRealtimeLogConfigs{
		Xmlns:       cfNamespace,
		MaxItems:    100,
		IsTruncated: false,
		Marker:      "",
		Items:       items,
	})
}

func cfStreamingARN(id string) string {
	return fmt.Sprintf("arn:aws:cloudfront::%s:streaming-distribution/%s", awsAccountID(), id)
}

// cfActiveSignersFrom projects the configured TrustedSigners account numbers
// into the ActiveTrustedSigners shape the StreamingDistribution response
// carries (one Signer entry per AWS account number).
func cfActiveSignersFrom(ts CFStreamingTrustedSigners) CFActiveTrustedSigners {
	signers := make([]CFActiveTrustedSigner, 0, len(ts.Items))
	for _, acct := range ts.Items {
		signers = append(signers, CFActiveTrustedSigner{AwsAccountNumber: acct})
	}
	return CFActiveTrustedSigners{Enabled: ts.Enabled, Quantity: ts.Quantity, Items: signers}
}

// cfCreateStreamingDistributionOperationName disambiguates the create op by the
// ?WithTags query flag the SDK sets on the shared POST /streaming-distribution
// path, mirroring real AWS.
func cfCreateStreamingDistributionOperationName(r *http.Request, _ []byte) string {
	if r.URL.Query().Has("WithTags") {
		return "CreateStreamingDistributionWithTags"
	}
	return "CreateStreamingDistribution"
}

// CFStreamingDistributionConfigWithTags is the CreateStreamingDistributionWithTags
// body wrapper. The SDK emits this whenever the request carries tags.
type CFStreamingDistributionConfigWithTags struct {
	XMLName                     xml.Name                      `xml:"StreamingDistributionConfigWithTags"`
	StreamingDistributionConfig CFStreamingDistributionConfig `xml:"StreamingDistributionConfig"`
	Tags                        CFTags                        `xml:"Tags"`
}

func handleCFCreateStreamingDist(w http.ResponseWriter, r *http.Request) {
	var cfg CFStreamingDistributionConfig
	if r.URL.Query().Has("WithTags") {
		var wrap CFStreamingDistributionConfigWithTags
		if err := xml.NewDecoder(r.Body).Decode(&wrap); err != nil {
			cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode StreamingDistributionConfigWithTags: "+err.Error())
			return
		}
		cfg = wrap.StreamingDistributionConfig
	} else if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode StreamingDistributionConfig: "+err.Error())
		return
	}
	if cfg.CallerReference == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "CallerReference is required")
		return
	}
	cfg.Xmlns = ""
	id := cfRandomID("ES")
	etag := cfETag()
	dist := CFStreamingDistribution{
		Xmlns:                       cfNamespace,
		Id:                          id,
		ARN:                         cfStreamingARN(id),
		Status:                      "Deployed",
		LastModifiedTime:            cfNowISO(),
		DomainName:                  strings.ToLower(id) + ".cloudfront.net",
		ActiveTrustedSigners:        cfActiveSignersFrom(cfg.TrustedSigners),
		StreamingDistributionConfig: cfg,
	}
	cfStreamingDistributions.Put(id, cfStoredStreamingDist{Dist: dist, ETag: etag})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/streaming-distribution/"+id)
	cfWriteXML(w, http.StatusCreated, dist)
}

func handleCFGetStreamingDist(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfStreamingDistributions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchStreamingDistribution", "The specified streaming distribution does not exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.Dist)
}

func handleCFGetStreamingDistConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfStreamingDistributions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchStreamingDistribution", "The specified streaming distribution does not exist.")
		return
	}
	cfg := stored.Dist.StreamingDistributionConfig
	cfg.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, cfg)
}

func handleCFUpdateStreamingDist(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfStreamingDistributions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchStreamingDistribution", "The specified streaming distribution does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	var cfg CFStreamingDistributionConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode StreamingDistributionConfig: "+err.Error())
		return
	}
	cfg.Xmlns = ""
	newETag := cfETag()
	stored.Dist.StreamingDistributionConfig = cfg
	stored.Dist.LastModifiedTime = cfNowISO()
	stored.Dist.ActiveTrustedSigners = cfActiveSignersFrom(cfg.TrustedSigners)
	stored.ETag = newETag
	cfStreamingDistributions.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.Dist)
}

func handleCFDeleteStreamingDist(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfStreamingDistributions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchStreamingDistribution", "The specified streaming distribution does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	cfStreamingDistributions.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListStreamingDist(w http.ResponseWriter, _ *http.Request) {
	items := []CFStreamingDistributionSummary{}
	for _, stored := range cfStreamingDistributions.List() {
		c := stored.Dist.StreamingDistributionConfig
		aliases := CFStreamingAliases{Quantity: 0}
		if c.Aliases != nil {
			aliases = *c.Aliases
		}
		items = append(items, CFStreamingDistributionSummary{
			Id:               stored.Dist.Id,
			ARN:              stored.Dist.ARN,
			Status:           stored.Dist.Status,
			LastModifiedTime: stored.Dist.LastModifiedTime,
			DomainName:       stored.Dist.DomainName,
			S3Origin:         c.S3Origin,
			Aliases:          aliases,
			TrustedSigners:   c.TrustedSigners,
			Comment:          c.Comment,
			PriceClass:       c.PriceClass,
			Enabled:          c.Enabled,
		})
	}
	cfWriteXML(w, http.StatusOK, CFStreamingDistributionList{
		Xmlns:    cfNamespace,
		Marker:   "",
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	})
}

func cfVpcOriginARN(id string) string {
	return fmt.Sprintf("arn:aws:cloudfront:us-east-1:%s:vpcorigin/%s", awsAccountID(), id)
}

func handleCFCreateVpcOrigin(w http.ResponseWriter, r *http.Request) {
	var req CFVpcOriginCreateRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode CreateVpcOriginRequest: "+err.Error())
		return
	}
	cfg := req.VpcOriginEndpointConfig
	if cfg.Name == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Name is required")
		return
	}
	cfg.Xmlns = ""
	id := cfRandomID("VO")
	etag := cfETag()
	now := cfNowISO()
	origin := CFVpcOrigin{
		Xmlns:                   cfNamespace,
		Id:                      id,
		Arn:                     cfVpcOriginARN(id),
		AccountId:               awsAccountID(),
		Status:                  "Deploying",
		CreatedTime:             now,
		LastModifiedTime:        now,
		VpcOriginEndpointConfig: cfg,
	}
	cfVpcOrigins.Put(id, cfStoredVpcOrigin{Origin: origin, ETag: etag})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/vpc-origin/"+id)
	cfWriteXML(w, http.StatusAccepted, origin)
}

func handleCFGetVpcOrigin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfVpcOrigins.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The VPC origin does not exist.")
		return
	}
	// A read VPC origin has finished deploying.
	if stored.Origin.Status != "Deployed" {
		stored.Origin.Status = "Deployed"
		cfVpcOrigins.Put(id, stored)
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.Origin)
}

func handleCFUpdateVpcOrigin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfVpcOrigins.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The VPC origin does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	var cfg CFVpcOriginEndpointConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode VpcOriginEndpointConfig: "+err.Error())
		return
	}
	cfg.Xmlns = ""
	newETag := cfETag()
	stored.Origin.VpcOriginEndpointConfig = cfg
	stored.Origin.Status = "Deploying"
	stored.Origin.LastModifiedTime = cfNowISO()
	stored.ETag = newETag
	cfVpcOrigins.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusAccepted, stored.Origin)
}

func handleCFDeleteVpcOrigin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfVpcOrigins.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The VPC origin does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	stored.Origin.Status = "Deleting"
	cfVpcOrigins.Delete(id)
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusAccepted, stored.Origin)
}

func handleCFListVpcOrigins(w http.ResponseWriter, _ *http.Request) {
	items := []CFVpcOriginSummary{}
	for _, stored := range cfVpcOrigins.List() {
		items = append(items, CFVpcOriginSummary{
			Id:                stored.Origin.Id,
			Name:              stored.Origin.VpcOriginEndpointConfig.Name,
			Status:            stored.Origin.Status,
			CreatedTime:       stored.Origin.CreatedTime,
			LastModifiedTime:  stored.Origin.LastModifiedTime,
			Arn:               stored.Origin.Arn,
			AccountId:         stored.Origin.AccountId,
			OriginEndpointArn: stored.Origin.VpcOriginEndpointConfig.Arn,
		})
	}
	cfWriteXML(w, http.StatusOK, CFVpcOriginList{
		Xmlns:    cfNamespace,
		Marker:   "",
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	})
}

func cfAnycastARN(id string) string {
	return fmt.Sprintf("arn:aws:cloudfront::%s:anycast-ip-list/%s", awsAccountID(), id)
}

func handleCFCreateAnycastIpList(w http.ResponseWriter, r *http.Request) {
	var req CFAnycastIpListCreateRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode CreateAnycastIpListRequest: "+err.Error())
		return
	}
	if req.Name == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Name is required")
		return
	}
	id := cfRandomID("AIL")
	etag := cfETag()
	ips := make([]string, 0, req.IpCount)
	for i := 0; i < req.IpCount; i++ {
		ips = append(ips, fmt.Sprintf("203.0.113.%d", i+1))
	}
	list := CFAnycastIpList{
		Xmlns:            cfNamespace,
		Id:               id,
		Name:             req.Name,
		Status:           "Deployed",
		Arn:              cfAnycastARN(id),
		IpAddressType:    req.IpAddressType,
		AnycastIps:       ips,
		IpCount:          req.IpCount,
		LastModifiedTime: cfNowISO(),
	}
	cfAnycastIpLists.Put(id, cfStoredAnycastIpList{List: list, ETag: etag})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", "https://cloudfront.amazonaws.com/"+cfAPIVersion+"/anycast-ip-list/"+id)
	cfWriteXML(w, http.StatusAccepted, list)
}

func handleCFGetAnycastIpList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfAnycastIpLists.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The Anycast static IP list does not exist.")
		return
	}
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.List)
}

func handleCFDeleteAnycastIpList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfAnycastIpLists.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The Anycast static IP list does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	cfAnycastIpLists.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListAnycastIpLists(w http.ResponseWriter, _ *http.Request) {
	items := []CFAnycastIpListSummary{}
	for _, stored := range cfAnycastIpLists.List() {
		items = append(items, CFAnycastIpListSummary{
			Id:               stored.List.Id,
			Name:             stored.List.Name,
			Status:           stored.List.Status,
			Arn:              stored.List.Arn,
			IpCount:          stored.List.IpCount,
			LastModifiedTime: stored.List.LastModifiedTime,
			IpAddressType:    stored.List.IpAddressType,
			ETag:             stored.ETag,
		})
	}
	cfWriteXML(w, http.StatusOK, CFAnycastIpListCollection{
		Xmlns:    cfNamespace,
		Items:    items,
		Marker:   "",
		MaxItems: 100,
		Quantity: len(items),
	})
}
