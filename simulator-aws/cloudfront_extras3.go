package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// CloudFront extra resources, part 3 — distribution tenants, connection groups,
// trust stores, resource policies, WebACL associations, alias/domain conflict
// queries, the ListDistributionsBy* projections, plus the CopyDistribution /
// UpdateDistributionWithStagingConfig / UpdateAnycastIpList variants and
// GetManagedCertificateDetails. Same REST + restXml wire as the rest of the
// CloudFront slice: each named resource carries an Id + ARN + an ETag returned
// in the response header and required as If-Match on Update/Delete.

// ---------- Distribution Tenant ----------

type CFDomainResult struct {
	Domain string `xml:"Domain"`
	Status string `xml:"Status,omitempty"`
}

type CFDistributionTenant struct {
	XMLName           xml.Name         `xml:"DistributionTenant"`
	Xmlns             string           `xml:"xmlns,attr,omitempty"`
	Id                string           `xml:"Id"`
	DistributionId    string           `xml:"DistributionId"`
	Name              string           `xml:"Name"`
	Arn               string           `xml:"Arn"`
	Domains           []CFDomainResult `xml:"Domains>member,omitempty"`
	ConnectionGroupId string           `xml:"ConnectionGroupId,omitempty"`
	CreatedTime       string           `xml:"CreatedTime"`
	LastModifiedTime  string           `xml:"LastModifiedTime"`
	Enabled           bool             `xml:"Enabled"`
	Status            string           `xml:"Status,omitempty"`
}

// CFDistributionTenantCreateRequest mirrors CreateDistributionTenantRequest.
type CFDistributionTenantCreateRequest struct {
	XMLName           xml.Name        `xml:"CreateDistributionTenantRequest"`
	DistributionId    string          `xml:"DistributionId"`
	Name              string          `xml:"Name"`
	Domains           []CFDomainInput `xml:"Domains>member,omitempty"`
	ConnectionGroupId string          `xml:"ConnectionGroupId,omitempty"`
	Enabled           *bool           `xml:"Enabled,omitempty"`
}

// CFDomainInput is the DomainItem request shape (just a Domain string).
type CFDomainInput struct {
	Domain string `xml:"Domain"`
}

// CFDistributionTenantUpdateRequest mirrors UpdateDistributionTenantRequest.
type CFDistributionTenantUpdateRequest struct {
	XMLName           xml.Name        `xml:"UpdateDistributionTenantRequest"`
	DistributionId    string          `xml:"DistributionId,omitempty"`
	Domains           []CFDomainInput `xml:"Domains>member,omitempty"`
	ConnectionGroupId string          `xml:"ConnectionGroupId,omitempty"`
	Enabled           *bool           `xml:"Enabled,omitempty"`
}

type CFDistributionTenantSummary struct {
	Id                string           `xml:"Id"`
	DistributionId    string           `xml:"DistributionId"`
	Name              string           `xml:"Name"`
	Arn               string           `xml:"Arn"`
	Domains           []CFDomainResult `xml:"Domains>member,omitempty"`
	ConnectionGroupId string           `xml:"ConnectionGroupId,omitempty"`
	CreatedTime       string           `xml:"CreatedTime"`
	LastModifiedTime  string           `xml:"LastModifiedTime"`
	ETag              string           `xml:"ETag,omitempty"`
	Enabled           bool             `xml:"Enabled"`
	Status            string           `xml:"Status,omitempty"`
}

// CFListDistributionTenantsResult is the ListDistributionTenants output
// envelope. DistributionTenantList is a flat list whose member element is
// <DistributionTenantSummary>.
type CFListDistributionTenantsResult struct {
	XMLName                xml.Name                      `xml:"ListDistributionTenantsResult"`
	Xmlns                  string                        `xml:"xmlns,attr,omitempty"`
	NextMarker             string                        `xml:"NextMarker,omitempty"`
	DistributionTenantList []CFDistributionTenantSummary `xml:"DistributionTenantList>DistributionTenantSummary,omitempty"`
}

// CFListDistributionTenantsByCustomizationResult is the
// ListDistributionTenantsByCustomization output envelope — same body shape but
// a distinct result root element.
type CFListDistributionTenantsByCustomizationResult struct {
	XMLName                xml.Name                      `xml:"ListDistributionTenantsByCustomizationResult"`
	Xmlns                  string                        `xml:"xmlns,attr,omitempty"`
	NextMarker             string                        `xml:"NextMarker,omitempty"`
	DistributionTenantList []CFDistributionTenantSummary `xml:"DistributionTenantList>DistributionTenantSummary,omitempty"`
}

type cfStoredTenant struct {
	Tenant CFDistributionTenant
	ETag   string
	Tags   []CFTag
}

// ---------- Connection Group ----------

type CFConnectionGroup struct {
	XMLName          xml.Name `xml:"ConnectionGroup"`
	Xmlns            string   `xml:"xmlns,attr,omitempty"`
	Id               string   `xml:"Id"`
	Name             string   `xml:"Name"`
	Arn              string   `xml:"Arn"`
	CreatedTime      string   `xml:"CreatedTime"`
	LastModifiedTime string   `xml:"LastModifiedTime"`
	Ipv6Enabled      bool     `xml:"Ipv6Enabled"`
	RoutingEndpoint  string   `xml:"RoutingEndpoint"`
	AnycastIpListId  string   `xml:"AnycastIpListId,omitempty"`
	Status           string   `xml:"Status,omitempty"`
	Enabled          bool     `xml:"Enabled"`
	IsDefault        bool     `xml:"IsDefault"`
}

// CFConnectionGroupCreateRequest mirrors CreateConnectionGroupRequest.
type CFConnectionGroupCreateRequest struct {
	XMLName         xml.Name `xml:"CreateConnectionGroupRequest"`
	Name            string   `xml:"Name"`
	Ipv6Enabled     *bool    `xml:"Ipv6Enabled,omitempty"`
	AnycastIpListId string   `xml:"AnycastIpListId,omitempty"`
	Enabled         *bool    `xml:"Enabled,omitempty"`
}

// CFConnectionGroupUpdateRequest mirrors UpdateConnectionGroupRequest.
type CFConnectionGroupUpdateRequest struct {
	XMLName         xml.Name `xml:"UpdateConnectionGroupRequest"`
	Ipv6Enabled     *bool    `xml:"Ipv6Enabled,omitempty"`
	AnycastIpListId string   `xml:"AnycastIpListId,omitempty"`
	Enabled         *bool    `xml:"Enabled,omitempty"`
}

type CFConnectionGroupSummary struct {
	Id               string `xml:"Id"`
	Name             string `xml:"Name"`
	Arn              string `xml:"Arn"`
	RoutingEndpoint  string `xml:"RoutingEndpoint"`
	CreatedTime      string `xml:"CreatedTime"`
	LastModifiedTime string `xml:"LastModifiedTime"`
	ETag             string `xml:"ETag,omitempty"`
	AnycastIpListId  string `xml:"AnycastIpListId,omitempty"`
	Enabled          bool   `xml:"Enabled"`
	Status           string `xml:"Status,omitempty"`
	IsDefault        bool   `xml:"IsDefault"`
}

type CFListConnectionGroupsResult struct {
	XMLName          xml.Name                   `xml:"ListConnectionGroupsResult"`
	Xmlns            string                     `xml:"xmlns,attr,omitempty"`
	NextMarker       string                     `xml:"NextMarker,omitempty"`
	ConnectionGroups []CFConnectionGroupSummary `xml:"ConnectionGroups>ConnectionGroupSummary,omitempty"`
}

type cfStoredConnectionGroup struct {
	Group CFConnectionGroup
	ETag  string
	Tags  []CFTag
}

// ---------- Trust Store ----------

type CFTrustStore struct {
	XMLName                          xml.Name `xml:"TrustStore"`
	Xmlns                            string   `xml:"xmlns,attr,omitempty"`
	Id                               string   `xml:"Id"`
	Arn                              string   `xml:"Arn"`
	Name                             string   `xml:"Name"`
	Status                           string   `xml:"Status,omitempty"`
	NumberOfCaCertificates           int      `xml:"NumberOfCaCertificates"`
	LastModifiedTime                 string   `xml:"LastModifiedTime"`
	Reason                           string   `xml:"Reason,omitempty"`
	UseClientCertificateOCSPEndpoint bool     `xml:"UseClientCertificateOCSPEndpoint"`
}

// CFCaCertificatesBundleSource mirrors the CaCertificatesBundleSource union;
// the S3-location member is the only variant CloudFront accepts today.
type CFCaCertificatesBundleSource struct {
	S3Location *CFTrustStoreS3Source `xml:"CaCertificatesBundleS3Location,omitempty"`
}

type CFTrustStoreS3Source struct {
	Bucket  string `xml:"Bucket"`
	Key     string `xml:"Key"`
	Region  string `xml:"Region,omitempty"`
	Version string `xml:"Version,omitempty"`
}

// CFTrustStoreCreateRequest mirrors CreateTrustStoreRequest.
type CFTrustStoreCreateRequest struct {
	XMLName                          xml.Name                      `xml:"CreateTrustStoreRequest"`
	Name                             string                        `xml:"Name"`
	CaCertificatesBundleSource       *CFCaCertificatesBundleSource `xml:"CaCertificatesBundleSource,omitempty"`
	UseClientCertificateOCSPEndpoint *bool                         `xml:"UseClientCertificateOCSPEndpoint,omitempty"`
}

// CFTrustStoreUpdateRequest mirrors UpdateTrustStoreRequest. Note:
// UseClientCertificateOCSPEndpoint is bound to an HTTP header on update.
// CFTrustStoreUpdateRequest decodes the UpdateTrustStore body, whose root is the
// CaCertificatesBundleSource payload (it is the operation's httpPayload member;
// the other inputs ride path/header bindings).
type CFTrustStoreUpdateRequest struct {
	XMLName    xml.Name              `xml:"CaCertificatesBundleSource"`
	S3Location *CFTrustStoreS3Source `xml:"CaCertificatesBundleS3Location,omitempty"`
}

type CFTrustStoreSummary struct {
	Id                     string `xml:"Id"`
	Arn                    string `xml:"Arn"`
	Name                   string `xml:"Name"`
	Status                 string `xml:"Status,omitempty"`
	NumberOfCaCertificates int    `xml:"NumberOfCaCertificates"`
	LastModifiedTime       string `xml:"LastModifiedTime"`
	Reason                 string `xml:"Reason,omitempty"`
	ETag                   string `xml:"ETag,omitempty"`
}

type CFListTrustStoresResult struct {
	XMLName        xml.Name              `xml:"ListTrustStoresResult"`
	Xmlns          string                `xml:"xmlns,attr,omitempty"`
	NextMarker     string                `xml:"NextMarker,omitempty"`
	TrustStoreList []CFTrustStoreSummary `xml:"TrustStoreList>TrustStoreSummary,omitempty"`
}

type cfStoredTrustStore struct {
	Store CFTrustStore
	ETag  string
	Tags  []CFTag
}

// ---------- Resource Policy ----------

// CFResourcePolicyRequest covers Get/Put/DeleteResourcePolicy bodies. The SDK
// wraps the input fields under the operation-named request root.
type CFGetResourcePolicyRequest struct {
	XMLName     xml.Name `xml:"GetResourcePolicyRequest"`
	ResourceArn string   `xml:"ResourceArn"`
}

type CFPutResourcePolicyRequest struct {
	XMLName        xml.Name `xml:"PutResourcePolicyRequest"`
	ResourceArn    string   `xml:"ResourceArn"`
	PolicyDocument string   `xml:"PolicyDocument"`
}

type CFDeleteResourcePolicyRequest struct {
	XMLName     xml.Name `xml:"DeleteResourcePolicyRequest"`
	ResourceArn string   `xml:"ResourceArn"`
}

type CFGetResourcePolicyResult struct {
	XMLName        xml.Name `xml:"GetResourcePolicyResult"`
	Xmlns          string   `xml:"xmlns,attr,omitempty"`
	ResourceArn    string   `xml:"ResourceArn"`
	PolicyDocument string   `xml:"PolicyDocument"`
}

type CFPutResourcePolicyResult struct {
	XMLName     xml.Name `xml:"PutResourcePolicyResult"`
	Xmlns       string   `xml:"xmlns,attr,omitempty"`
	ResourceArn string   `xml:"ResourceArn"`
}

type cfStoredResourcePolicy struct {
	ResourceArn    string
	PolicyDocument string
}

// ---------- WebACL association results ----------

type CFAssociateDistributionWebACLResult struct {
	XMLName   xml.Name `xml:"AssociateDistributionWebACLResult"`
	Xmlns     string   `xml:"xmlns,attr,omitempty"`
	Id        string   `xml:"Id"`
	WebACLArn string   `xml:"WebACLArn"`
}

type CFDisassociateDistributionWebACLResult struct {
	XMLName xml.Name `xml:"DisassociateDistributionWebACLResult"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Id      string   `xml:"Id"`
}

type CFAssociateDistributionTenantWebACLResult struct {
	XMLName   xml.Name `xml:"AssociateDistributionTenantWebACLResult"`
	Xmlns     string   `xml:"xmlns,attr,omitempty"`
	Id        string   `xml:"Id"`
	WebACLArn string   `xml:"WebACLArn"`
}

type CFDisassociateDistributionTenantWebACLResult struct {
	XMLName xml.Name `xml:"DisassociateDistributionTenantWebACLResult"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Id      string   `xml:"Id"`
}

type CFAssociateWebACLBody struct {
	WebACLArn string `xml:"WebACLArn"`
}

// ---------- Alias / domain conflict queries ----------

type CFConflictingAlias struct {
	Alias          string `xml:"Alias,omitempty"`
	DistributionId string `xml:"DistributionId,omitempty"`
	AccountId      string `xml:"AccountId,omitempty"`
}

type CFConflictingAliasesList struct {
	XMLName    xml.Name             `xml:"ConflictingAliasesList"`
	Xmlns      string               `xml:"xmlns,attr,omitempty"`
	NextMarker string               `xml:"NextMarker,omitempty"`
	MaxItems   int                  `xml:"MaxItems"`
	Quantity   int                  `xml:"Quantity"`
	Items      []CFConflictingAlias `xml:"Items>ConflictingAlias,omitempty"`
}

type CFDomainConflict struct {
	Domain       string `xml:"Domain,omitempty"`
	ResourceType string `xml:"ResourceType,omitempty"`
	ResourceId   string `xml:"ResourceId,omitempty"`
	AccountId    string `xml:"AccountId,omitempty"`
}

type CFListDomainConflictsResult struct {
	XMLName         xml.Name           `xml:"ListDomainConflictsResult"`
	Xmlns           string             `xml:"xmlns,attr,omitempty"`
	DomainConflicts []CFDomainConflict `xml:"DomainConflicts>DomainConflicts,omitempty"`
	NextMarker      string             `xml:"NextMarker,omitempty"`
}

type CFUpdateDomainAssociationResult struct {
	XMLName    xml.Name `xml:"UpdateDomainAssociationResult"`
	Xmlns      string   `xml:"xmlns,attr,omitempty"`
	Domain     string   `xml:"Domain,omitempty"`
	ResourceId string   `xml:"ResourceId,omitempty"`
}

type CFDistributionResourceId struct {
	DistributionId       string `xml:"DistributionId,omitempty"`
	DistributionTenantId string `xml:"DistributionTenantId,omitempty"`
}

type CFUpdateDomainAssociationRequest struct {
	XMLName        xml.Name                 `xml:"UpdateDomainAssociationRequest"`
	Domain         string                   `xml:"Domain"`
	TargetResource CFDistributionResourceId `xml:"TargetResource"`
}

type CFDnsConfiguration struct {
	Domain string `xml:"Domain,omitempty"`
	Status string `xml:"Status,omitempty"`
	Reason string `xml:"Reason,omitempty"`
}

type CFVerifyDnsConfigurationRequest struct {
	XMLName    xml.Name `xml:"VerifyDnsConfigurationRequest"`
	Domain     string   `xml:"Domain,omitempty"`
	Identifier string   `xml:"Identifier"`
}

type CFVerifyDnsConfigurationResult struct {
	XMLName              xml.Name             `xml:"VerifyDnsConfigurationResult"`
	Xmlns                string               `xml:"xmlns,attr,omitempty"`
	DnsConfigurationList []CFDnsConfiguration `xml:"DnsConfigurationList>DnsConfiguration,omitempty"`
}

// ---------- ListDistributionsBy* projection shapes ----------

// CFDistributionIdList projects matching distribution IDs (the DistributionIdList
// output shape — a flat list of <DistributionId> strings).
type CFDistributionIdList struct {
	XMLName     xml.Name `xml:"DistributionIdList"`
	Xmlns       string   `xml:"xmlns,attr,omitempty"`
	Marker      string   `xml:"Marker"`
	NextMarker  string   `xml:"NextMarker,omitempty"`
	MaxItems    int      `xml:"MaxItems"`
	IsTruncated bool     `xml:"IsTruncated"`
	Quantity    int      `xml:"Quantity"`
	Items       []string `xml:"Items>DistributionId,omitempty"`
}

type CFDistributionIdOwner struct {
	DistributionId string `xml:"DistributionId"`
	OwnerAccountId string `xml:"OwnerAccountId"`
}

type CFDistributionIdOwnerList struct {
	XMLName     xml.Name                `xml:"DistributionIdOwnerList"`
	Xmlns       string                  `xml:"xmlns,attr,omitempty"`
	Marker      string                  `xml:"Marker"`
	NextMarker  string                  `xml:"NextMarker,omitempty"`
	MaxItems    int                     `xml:"MaxItems"`
	IsTruncated bool                    `xml:"IsTruncated"`
	Quantity    int                     `xml:"Quantity"`
	Items       []CFDistributionIdOwner `xml:"Items>DistributionIdOwner,omitempty"`
}

// ---------- Managed certificate details ----------

type CFManagedCertificateDetails struct {
	XMLName             xml.Name `xml:"ManagedCertificateDetails"`
	Xmlns               string   `xml:"xmlns,attr,omitempty"`
	CertificateArn      string   `xml:"CertificateArn,omitempty"`
	CertificateStatus   string   `xml:"CertificateStatus,omitempty"`
	ValidationTokenHost string   `xml:"ValidationTokenHost,omitempty"`
}

// ---------- Storage ----------

var (
	cfTenants          sim.Store[cfStoredTenant]
	cfConnectionGroups sim.Store[cfStoredConnectionGroup]
	cfTrustStores      sim.Store[cfStoredTrustStore]
	cfResourcePolicies sim.Store[cfStoredResourcePolicy]
)

func cfTenantARN(id string) string {
	return fmt.Sprintf("arn:aws:cloudfront::%s:distribution-tenant/%s", awsAccountID(), id)
}

func cfConnectionGroupARN(id string) string {
	return fmt.Sprintf("arn:aws:cloudfront::%s:connection-group/%s", awsAccountID(), id)
}

func cfTrustStoreARN(id string) string {
	return fmt.Sprintf("arn:aws:cloudfront::%s:trust-store/%s", awsAccountID(), id)
}

// cfDomainResultsFrom projects request DomainItem inputs into DomainResult
// response shapes (status starts as "active" — the sim has no DNS-verification
// async lifecycle, so the domain is immediately associated).
func cfDomainResultsFrom(in []CFDomainInput) []CFDomainResult {
	out := make([]CFDomainResult, 0, len(in))
	for _, d := range in {
		out = append(out, CFDomainResult{Domain: d.Domain, Status: "active"})
	}
	return out
}

// registerCloudFrontExtras3 mounts the distribution-tenant, connection-group,
// trust-store, resource-policy, WebACL-association, alias/domain-conflict, the
// ListDistributionsBy* projections, and the CopyDistribution /
// UpdateDistributionWithStagingConfig / UpdateAnycastIpList /
// GetManagedCertificateDetails endpoints onto the same mux. Invoked from
// registerCloudFront in cloudfront.go.
func registerCloudFrontExtras3(srv *sim.Server) {
	cfTenants = sim.MakeStore[cfStoredTenant](srv.DB(), "cloudfront_distribution_tenants")
	cfConnectionGroups = sim.MakeStore[cfStoredConnectionGroup](srv.DB(), "cloudfront_connection_groups")
	cfTrustStores = sim.MakeStore[cfStoredTrustStore](srv.DB(), "cloudfront_trust_stores")
	cfResourcePolicies = sim.MakeStore[cfStoredResourcePolicy](srv.DB(), "cloudfront_resource_policies")

	mux := srv
	v := cfAPIVersion

	distRes := cloudTrailRESTResource("AWS::CloudFront::Distribution", "id", "Resource")
	tenantRes := cloudTrailRESTResource("AWS::CloudFront::DistributionTenant", "Id")
	cgRes := cloudTrailRESTResource("AWS::CloudFront::ConnectionGroup", "Id")
	tsRes := cloudTrailRESTResource("AWS::CloudFront::TrustStore", "Id")
	anycastRes := cloudTrailRESTResource("AWS::CloudFront::AnycastIpList", "Id")

	// Distribution variants (CreateDistribution/GetDistribution already live in
	// cloudfront.go via the dynamic dispatcher; only the copy/promote variants
	// are added here).
	mux.HandleFunc("POST /"+v+"/distribution/{PrimaryDistributionId}/copy", cloudTrailRecordedREST("CopyDistribution", "cloudfront.amazonaws.com", distRes, handleCFCopyDistribution))
	mux.HandleFunc("PUT /"+v+"/distribution/{id}/promote-staging-config", cloudTrailRecordedREST("UpdateDistributionWithStagingConfig", "cloudfront.amazonaws.com", distRes, handleCFPromoteStagingConfig))

	// Distribution Tenants
	mux.HandleFunc("POST /"+v+"/distribution-tenant", cloudTrailRecordedREST("CreateDistributionTenant", "cloudfront.amazonaws.com", nil, handleCFCreateTenant))
	mux.HandleFunc("GET /"+v+"/distribution-tenant", cloudTrailRecordedREST("GetDistributionTenantByDomain", "cloudfront.amazonaws.com", nil, handleCFGetTenantByDomain))
	mux.HandleFunc("GET /"+v+"/distribution-tenant/{Identifier}", cloudTrailRecordedREST("GetDistributionTenant", "cloudfront.amazonaws.com", tenantRes, handleCFGetTenant))
	mux.HandleFunc("PUT /"+v+"/distribution-tenant/{Id}", cloudTrailRecordedREST("UpdateDistributionTenant", "cloudfront.amazonaws.com", tenantRes, handleCFUpdateTenant))
	mux.HandleFunc("DELETE /"+v+"/distribution-tenant/{Id}", cloudTrailRecordedREST("DeleteDistributionTenant", "cloudfront.amazonaws.com", tenantRes, handleCFDeleteTenant))
	mux.HandleFunc("POST /"+v+"/distribution-tenants", cloudTrailRecordedREST("ListDistributionTenants", "cloudfront.amazonaws.com", nil, handleCFListTenants))
	mux.HandleFunc("POST /"+v+"/distribution-tenants-by-customization", cloudTrailRecordedREST("ListDistributionTenantsByCustomization", "cloudfront.amazonaws.com", nil, handleCFListTenantsByCustomization))
	mux.HandleFunc("PUT /"+v+"/distribution-tenant/{Id}/associate-web-acl", cloudTrailRecordedREST("AssociateDistributionTenantWebACL", "cloudfront.amazonaws.com", tenantRes, handleCFAssociateTenantWebACL))
	mux.HandleFunc("PUT /"+v+"/distribution-tenant/{Id}/disassociate-web-acl", cloudTrailRecordedREST("DisassociateDistributionTenantWebACL", "cloudfront.amazonaws.com", tenantRes, handleCFDisassociateTenantWebACL))
	mux.HandleFunc("POST /"+v+"/distribution-tenant/{Id}/invalidation", cloudTrailRecordedREST("CreateInvalidationForDistributionTenant", "cloudfront.amazonaws.com", tenantRes, handleCFCreateTenantInvalidation))
	mux.HandleFunc("GET /"+v+"/distribution-tenant/{DistributionTenantId}/invalidation/{Id}", cloudTrailRecordedREST("GetInvalidationForDistributionTenant", "cloudfront.amazonaws.com", tenantRes, handleCFGetTenantInvalidation))
	mux.HandleFunc("GET /"+v+"/distribution-tenant/{Id}/invalidation", cloudTrailRecordedREST("ListInvalidationsForDistributionTenant", "cloudfront.amazonaws.com", tenantRes, handleCFListTenantInvalidations))

	// Connection Groups
	mux.HandleFunc("POST /"+v+"/connection-group", cloudTrailRecordedREST("CreateConnectionGroup", "cloudfront.amazonaws.com", nil, handleCFCreateConnectionGroup))
	mux.HandleFunc("GET /"+v+"/connection-group", cloudTrailRecordedREST("GetConnectionGroupByRoutingEndpoint", "cloudfront.amazonaws.com", nil, handleCFGetConnectionGroupByRoutingEndpoint))
	mux.HandleFunc("GET /"+v+"/connection-group/{Identifier}", cloudTrailRecordedREST("GetConnectionGroup", "cloudfront.amazonaws.com", cgRes, handleCFGetConnectionGroup))
	mux.HandleFunc("PUT /"+v+"/connection-group/{Id}", cloudTrailRecordedREST("UpdateConnectionGroup", "cloudfront.amazonaws.com", cgRes, handleCFUpdateConnectionGroup))
	mux.HandleFunc("DELETE /"+v+"/connection-group/{Id}", cloudTrailRecordedREST("DeleteConnectionGroup", "cloudfront.amazonaws.com", cgRes, handleCFDeleteConnectionGroup))
	mux.HandleFunc("POST /"+v+"/connection-groups", cloudTrailRecordedREST("ListConnectionGroups", "cloudfront.amazonaws.com", nil, handleCFListConnectionGroups))

	// Trust Stores
	mux.HandleFunc("POST /"+v+"/trust-store", cloudTrailRecordedREST("CreateTrustStore", "cloudfront.amazonaws.com", nil, handleCFCreateTrustStore))
	mux.HandleFunc("GET /"+v+"/trust-store/{Identifier}", cloudTrailRecordedREST("GetTrustStore", "cloudfront.amazonaws.com", tsRes, handleCFGetTrustStore))
	mux.HandleFunc("PUT /"+v+"/trust-store/{Id}", cloudTrailRecordedREST("UpdateTrustStore", "cloudfront.amazonaws.com", tsRes, handleCFUpdateTrustStore))
	mux.HandleFunc("DELETE /"+v+"/trust-store/{Id}", cloudTrailRecordedREST("DeleteTrustStore", "cloudfront.amazonaws.com", tsRes, handleCFDeleteTrustStore))
	mux.HandleFunc("POST /"+v+"/trust-stores", cloudTrailRecordedREST("ListTrustStores", "cloudfront.amazonaws.com", nil, handleCFListTrustStores))
	mux.HandleFunc("GET /"+v+"/distributionsByTrustStore", cloudTrailRecordedREST("ListDistributionsByTrustStore", "cloudfront.amazonaws.com", nil, handleCFListDistributionsByTrustStore))

	// Resource Policy
	mux.HandleFunc("POST /"+v+"/get-resource-policy", cloudTrailRecordedREST("GetResourcePolicy", "cloudfront.amazonaws.com", nil, handleCFGetResourcePolicy))
	mux.HandleFunc("POST /"+v+"/put-resource-policy", cloudTrailRecordedREST("PutResourcePolicy", "cloudfront.amazonaws.com", nil, handleCFPutResourcePolicy))
	mux.HandleFunc("POST /"+v+"/delete-resource-policy", cloudTrailRecordedREST("DeleteResourcePolicy", "cloudfront.amazonaws.com", nil, handleCFDeleteResourcePolicy))

	// Distribution WebACL associations
	mux.HandleFunc("PUT /"+v+"/distribution/{id}/associate-web-acl", cloudTrailRecordedREST("AssociateDistributionWebACL", "cloudfront.amazonaws.com", distRes, handleCFAssociateDistributionWebACL))
	mux.HandleFunc("PUT /"+v+"/distribution/{id}/disassociate-web-acl", cloudTrailRecordedREST("DisassociateDistributionWebACL", "cloudfront.amazonaws.com", distRes, handleCFDisassociateDistributionWebACL))

	// Aliases + conflicts
	mux.HandleFunc("PUT /"+v+"/distribution/{TargetDistributionId}/associate-alias", cloudTrailRecordedREST("AssociateAlias", "cloudfront.amazonaws.com", distRes, handleCFAssociateAlias))
	mux.HandleFunc("GET /"+v+"/conflicting-alias", cloudTrailRecordedREST("ListConflictingAliases", "cloudfront.amazonaws.com", nil, handleCFListConflictingAliases))
	mux.HandleFunc("POST /"+v+"/domain-conflicts", cloudTrailRecordedREST("ListDomainConflicts", "cloudfront.amazonaws.com", nil, handleCFListDomainConflicts))
	mux.HandleFunc("POST /"+v+"/domain-association", cloudTrailRecordedREST("UpdateDomainAssociation", "cloudfront.amazonaws.com", nil, handleCFUpdateDomainAssociation))
	mux.HandleFunc("POST /"+v+"/verify-dns-configuration", cloudTrailRecordedREST("VerifyDnsConfiguration", "cloudfront.amazonaws.com", nil, handleCFVerifyDnsConfiguration))

	// ListDistributionsBy* projections over the existing distribution store.
	mux.HandleFunc("GET /"+v+"/distributionsByCachePolicyId/{CachePolicyId}", cloudTrailRecordedREST("ListDistributionsByCachePolicyId", "cloudfront.amazonaws.com", nil, cfDistsByIDListHandler("CachePolicyId", cfDistMatchesCachePolicy)))
	mux.HandleFunc("GET /"+v+"/distributionsByOriginRequestPolicyId/{OriginRequestPolicyId}", cloudTrailRecordedREST("ListDistributionsByOriginRequestPolicyId", "cloudfront.amazonaws.com", nil, cfDistsByIDListHandler("OriginRequestPolicyId", cfDistMatchesOriginRequestPolicy)))
	mux.HandleFunc("GET /"+v+"/distributionsByResponseHeadersPolicyId/{ResponseHeadersPolicyId}", cloudTrailRecordedREST("ListDistributionsByResponseHeadersPolicyId", "cloudfront.amazonaws.com", nil, cfDistsByIDListHandler("ResponseHeadersPolicyId", cfDistMatchesResponseHeadersPolicy)))
	mux.HandleFunc("GET /"+v+"/distributionsByKeyGroupId/{KeyGroupId}", cloudTrailRecordedREST("ListDistributionsByKeyGroup", "cloudfront.amazonaws.com", nil, cfDistsByIDListHandler("KeyGroupId", cfDistMatchesKeyGroup)))
	mux.HandleFunc("GET /"+v+"/distributionsByVpcOriginId/{VpcOriginId}", cloudTrailRecordedREST("ListDistributionsByVpcOriginId", "cloudfront.amazonaws.com", nil, cfDistsByIDListHandler("VpcOriginId", cfDistMatchesVpcOrigin)))
	mux.HandleFunc("POST /"+v+"/distributionsByRealtimeLogConfig", cloudTrailRecordedREST("ListDistributionsByRealtimeLogConfig", "cloudfront.amazonaws.com", nil, handleCFListDistributionsByRealtimeLogConfig))
	mux.HandleFunc("GET /"+v+"/distributionsByAnycastIpListId/{AnycastIpListId}", cloudTrailRecordedREST("ListDistributionsByAnycastIpListId", "cloudfront.amazonaws.com", nil, cfDistsByListHandler("AnycastIpListId", func(string, CFDistribution) bool { return false })))
	mux.HandleFunc("GET /"+v+"/distributionsByWebACLId/{WebACLId}", cloudTrailRecordedREST("ListDistributionsByWebACLId", "cloudfront.amazonaws.com", nil, cfDistsByListHandler("WebACLId", cfDistMatchesWebACL)))
	mux.HandleFunc("GET /"+v+"/distributionsByConnectionMode/{ConnectionMode}", cloudTrailRecordedREST("ListDistributionsByConnectionMode", "cloudfront.amazonaws.com", nil, cfDistsByListHandler("ConnectionMode", func(string, CFDistribution) bool { return true })))
	mux.HandleFunc("GET /"+v+"/distributionsByOwnedResource/{ResourceArn}", cloudTrailRecordedREST("ListDistributionsByOwnedResource", "cloudfront.amazonaws.com", nil, handleCFListDistributionsByOwnedResource))

	// Anycast IP List update + managed certificate details
	mux.HandleFunc("PUT /"+v+"/anycast-ip-list/{Id}", cloudTrailRecordedREST("UpdateAnycastIpList", "cloudfront.amazonaws.com", anycastRes, handleCFUpdateAnycastIpList))
	mux.HandleFunc("GET /"+v+"/managed-certificate/{Identifier}", cloudTrailRecordedREST("GetManagedCertificateDetails", "cloudfront.amazonaws.com", nil, handleCFGetManagedCertificateDetails))
}

// ----- Distribution variants -----

func handleCFCopyDistribution(w http.ResponseWriter, r *http.Request) {
	primaryID := r.PathValue("PrimaryDistributionId")
	src, ok := cfDistributions.Get(primaryID)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, src.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	var body struct {
		XMLName         xml.Name `xml:"CopyDistributionRequest"`
		CallerReference string   `xml:"CallerReference"`
		Enabled         *bool    `xml:"Enabled"`
	}
	if err := xml.NewDecoder(r.Body).Decode(&body); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode CopyDistributionRequest: "+err.Error())
		return
	}
	staging := strings.EqualFold(r.Header.Get("Staging"), "true")
	id := cfRandomID("E")
	etag := cfETag()
	dist := src.Distribution
	dist.Id = id
	dist.ARN = cfDistributionARN(id)
	dist.DomainName = cfDomainName(id)
	dist.LastModifiedTime = cfNowISO()
	dist.Status = "Deployed"
	if body.CallerReference != "" {
		dist.DistributionConfig.CallerReference = body.CallerReference
	}
	if body.Enabled != nil {
		dist.DistributionConfig.Enabled = *body.Enabled
	}
	st := staging
	dist.DistributionConfig.Staging = &st
	cfDistributions.Put(id, cfStoredDistribution{Distribution: dist, ETag: etag})
	w.Header().Set("ETag", etag)
	w.Header().Set("Location", fmt.Sprintf("https://cloudfront.amazonaws.com/%s/distribution/%s", cfAPIVersion, id))
	cfWriteXML(w, http.StatusCreated, dist)
}

func handleCFPromoteStagingConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfDistributions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	stagingID := r.URL.Query().Get("StagingDistributionId")
	if stagingID != "" {
		if staging, sok := cfDistributions.Get(stagingID); sok {
			cfg := staging.Distribution.DistributionConfig
			f := false
			cfg.Staging = &f
			stored.Distribution.DistributionConfig = cfg
		}
	}
	newETag := cfETag()
	stored.Distribution.LastModifiedTime = cfNowISO()
	stored.ETag = newETag
	cfDistributions.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.Distribution)
}

// ----- Distribution Tenant handlers -----

func handleCFCreateTenant(w http.ResponseWriter, r *http.Request) {
	var req CFDistributionTenantCreateRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode CreateDistributionTenantRequest: "+err.Error())
		return
	}
	if req.Name == "" || req.DistributionId == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Name and DistributionId are required")
		return
	}
	if _, ok := cfDistributions.Get(req.DistributionId); !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
		return
	}
	id := cfRandomID("DT")
	etag := cfETag()
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	tenant := CFDistributionTenant{
		Xmlns:             cfNamespace,
		Id:                id,
		DistributionId:    req.DistributionId,
		Name:              req.Name,
		Arn:               cfTenantARN(id),
		Domains:           cfDomainResultsFrom(req.Domains),
		ConnectionGroupId: req.ConnectionGroupId,
		CreatedTime:       cfNowISO(),
		LastModifiedTime:  cfNowISO(),
		Enabled:           enabled,
		Status:            "Deployed",
	}
	cfTenants.Put(id, cfStoredTenant{Tenant: tenant, ETag: etag})
	w.Header().Set("ETag", etag)
	cfWriteXML(w, http.StatusCreated, tenant)
}

func handleCFGetTenant(w http.ResponseWriter, r *http.Request) {
	stored, ok := cfTenants.Get(r.PathValue("Identifier"))
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The distribution tenant does not exist.")
		return
	}
	stored.Tenant.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.Tenant)
}

func handleCFGetTenantByDomain(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	for _, stored := range cfTenants.List() {
		for _, d := range stored.Tenant.Domains {
			if d.Domain == domain {
				stored.Tenant.Xmlns = cfNamespace
				w.Header().Set("ETag", stored.ETag)
				cfWriteXML(w, http.StatusOK, stored.Tenant)
				return
			}
		}
	}
	cfWriteError(w, http.StatusNotFound, "EntityNotFound", "No distribution tenant matches the specified domain.")
}

func handleCFUpdateTenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfTenants.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The distribution tenant does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	var req CFDistributionTenantUpdateRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode UpdateDistributionTenantRequest: "+err.Error())
		return
	}
	if req.DistributionId != "" {
		stored.Tenant.DistributionId = req.DistributionId
	}
	if len(req.Domains) > 0 {
		stored.Tenant.Domains = cfDomainResultsFrom(req.Domains)
	}
	if req.ConnectionGroupId != "" {
		stored.Tenant.ConnectionGroupId = req.ConnectionGroupId
	}
	if req.Enabled != nil {
		stored.Tenant.Enabled = *req.Enabled
	}
	newETag := cfETag()
	stored.Tenant.LastModifiedTime = cfNowISO()
	stored.ETag = newETag
	cfTenants.Put(id, stored)
	stored.Tenant.Xmlns = cfNamespace
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.Tenant)
}

func handleCFDeleteTenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfTenants.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The distribution tenant does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	cfTenants.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListTenants(w http.ResponseWriter, _ *http.Request) {
	cfWriteXML(w, http.StatusOK, CFListDistributionTenantsResult{
		Xmlns:                  cfNamespace,
		DistributionTenantList: cfTenantSummaries(cfTenants.List()),
	})
}

func handleCFListTenantsByCustomization(w http.ResponseWriter, r *http.Request) {
	var req struct {
		XMLName        xml.Name `xml:"ListDistributionTenantsByCustomizationRequest"`
		WebACLArn      string   `xml:"WebACLArn"`
		CertificateArn string   `xml:"CertificateArn"`
	}
	_ = xml.NewDecoder(r.Body).Decode(&req)
	// The sim models no customization blocks on tenants, so a customization
	// filter matches nothing (honest empty); an unfiltered call lists all.
	stored := cfTenants.List()
	if req.WebACLArn != "" || req.CertificateArn != "" {
		stored = nil
	}
	cfWriteXML(w, http.StatusOK, CFListDistributionTenantsByCustomizationResult{
		Xmlns:                  cfNamespace,
		DistributionTenantList: cfTenantSummaries(stored),
	})
}

// cfTenantSummaries projects stored tenants into their list-summary shape.
func cfTenantSummaries(stored []cfStoredTenant) []CFDistributionTenantSummary {
	items := []CFDistributionTenantSummary{}
	for _, s := range stored {
		t := s.Tenant
		items = append(items, CFDistributionTenantSummary{
			Id:                t.Id,
			DistributionId:    t.DistributionId,
			Name:              t.Name,
			Arn:               t.Arn,
			Domains:           t.Domains,
			ConnectionGroupId: t.ConnectionGroupId,
			CreatedTime:       t.CreatedTime,
			LastModifiedTime:  t.LastModifiedTime,
			ETag:              s.ETag,
			Enabled:           t.Enabled,
			Status:            t.Status,
		})
	}
	return items
}

func handleCFAssociateTenantWebACL(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfTenants.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The distribution tenant does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	var body CFAssociateWebACLBody
	if err := xml.NewDecoder(r.Body).Decode(&body); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	newETag := cfETag()
	stored.ETag = newETag
	stored.Tenant.LastModifiedTime = cfNowISO()
	cfTenants.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, CFAssociateDistributionTenantWebACLResult{Xmlns: cfNamespace, Id: id, WebACLArn: body.WebACLArn})
}

func handleCFDisassociateTenantWebACL(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfTenants.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The distribution tenant does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	newETag := cfETag()
	stored.ETag = newETag
	stored.Tenant.LastModifiedTime = cfNowISO()
	cfTenants.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, CFDisassociateDistributionTenantWebACLResult{Xmlns: cfNamespace, Id: id})
}

func handleCFCreateTenantInvalidation(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("Id")
	if _, ok := cfTenants.Get(tenantID); !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The distribution tenant does not exist.")
		return
	}
	var batch CFInvalidationBatch
	if err := xml.NewDecoder(r.Body).Decode(&batch); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode InvalidationBatch: "+err.Error())
		return
	}
	batch.Xmlns = ""
	id := cfRandomID("I")
	inv := CFInvalidation{
		Xmlns:             cfNamespace,
		Id:                id,
		Status:            "Completed",
		CreateTime:        cfNowISO(),
		InvalidationBatch: batch,
	}
	cfInvalidations.Put(id, cfStoredInvalidation{Invalidation: inv, DistributionID: tenantID})
	w.Header().Set("Location", fmt.Sprintf("https://cloudfront.amazonaws.com/%s/distribution-tenant/%s/invalidation/%s", cfAPIVersion, tenantID, id))
	cfWriteXML(w, http.StatusCreated, inv)
}

func handleCFGetTenantInvalidation(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("DistributionTenantId")
	id := r.PathValue("Id")
	stored, ok := cfInvalidations.Get(id)
	if !ok || stored.DistributionID != tenantID {
		cfWriteError(w, http.StatusNotFound, "NoSuchInvalidation", "The specified invalidation does not exist.")
		return
	}
	cfWriteXML(w, http.StatusOK, stored.Invalidation)
}

func handleCFListTenantInvalidations(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("Id")
	items := []CFInvalidationSummary{}
	for _, stored := range cfInvalidations.List() {
		if stored.DistributionID != tenantID {
			continue
		}
		items = append(items, CFInvalidationSummary{
			Id:         stored.Invalidation.Id,
			CreateTime: stored.Invalidation.CreateTime,
			Status:     stored.Invalidation.Status,
		})
	}
	cfWriteXML(w, http.StatusOK, CFInvalidationList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	})
}

// ----- Connection Group handlers -----

func handleCFCreateConnectionGroup(w http.ResponseWriter, r *http.Request) {
	var req CFConnectionGroupCreateRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode CreateConnectionGroupRequest: "+err.Error())
		return
	}
	if req.Name == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Name is required")
		return
	}
	id := cfRandomID("CG")
	etag := cfETag()
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ipv6 := false
	if req.Ipv6Enabled != nil {
		ipv6 = *req.Ipv6Enabled
	}
	cg := CFConnectionGroup{
		Xmlns:            cfNamespace,
		Id:               id,
		Name:             req.Name,
		Arn:              cfConnectionGroupARN(id),
		CreatedTime:      cfNowISO(),
		LastModifiedTime: cfNowISO(),
		Ipv6Enabled:      ipv6,
		RoutingEndpoint:  strings.ToLower(id) + ".cloudfront.net",
		AnycastIpListId:  req.AnycastIpListId,
		Status:           "Deployed",
		Enabled:          enabled,
		IsDefault:        false,
	}
	cfConnectionGroups.Put(id, cfStoredConnectionGroup{Group: cg, ETag: etag})
	w.Header().Set("ETag", etag)
	cfWriteXML(w, http.StatusCreated, cg)
}

func handleCFGetConnectionGroup(w http.ResponseWriter, r *http.Request) {
	stored, ok := cfConnectionGroups.Get(r.PathValue("Identifier"))
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The connection group does not exist.")
		return
	}
	stored.Group.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.Group)
}

func handleCFGetConnectionGroupByRoutingEndpoint(w http.ResponseWriter, r *http.Request) {
	endpoint := r.URL.Query().Get("RoutingEndpoint")
	for _, stored := range cfConnectionGroups.List() {
		if stored.Group.RoutingEndpoint == endpoint {
			stored.Group.Xmlns = cfNamespace
			w.Header().Set("ETag", stored.ETag)
			cfWriteXML(w, http.StatusOK, stored.Group)
			return
		}
	}
	cfWriteError(w, http.StatusNotFound, "EntityNotFound", "No connection group matches the specified routing endpoint.")
}

func handleCFUpdateConnectionGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfConnectionGroups.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The connection group does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	var req CFConnectionGroupUpdateRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode UpdateConnectionGroupRequest: "+err.Error())
		return
	}
	if req.Ipv6Enabled != nil {
		stored.Group.Ipv6Enabled = *req.Ipv6Enabled
	}
	if req.AnycastIpListId != "" {
		stored.Group.AnycastIpListId = req.AnycastIpListId
	}
	if req.Enabled != nil {
		stored.Group.Enabled = *req.Enabled
	}
	newETag := cfETag()
	stored.Group.LastModifiedTime = cfNowISO()
	stored.ETag = newETag
	cfConnectionGroups.Put(id, stored)
	stored.Group.Xmlns = cfNamespace
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.Group)
}

func handleCFDeleteConnectionGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfConnectionGroups.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The connection group does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	cfConnectionGroups.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListConnectionGroups(w http.ResponseWriter, _ *http.Request) {
	items := []CFConnectionGroupSummary{}
	for _, s := range cfConnectionGroups.List() {
		g := s.Group
		items = append(items, CFConnectionGroupSummary{
			Id:               g.Id,
			Name:             g.Name,
			Arn:              g.Arn,
			RoutingEndpoint:  g.RoutingEndpoint,
			CreatedTime:      g.CreatedTime,
			LastModifiedTime: g.LastModifiedTime,
			ETag:             s.ETag,
			AnycastIpListId:  g.AnycastIpListId,
			Enabled:          g.Enabled,
			Status:           g.Status,
			IsDefault:        g.IsDefault,
		})
	}
	cfWriteXML(w, http.StatusOK, CFListConnectionGroupsResult{Xmlns: cfNamespace, ConnectionGroups: items})
}

// ----- Trust Store handlers -----

func handleCFCreateTrustStore(w http.ResponseWriter, r *http.Request) {
	var req CFTrustStoreCreateRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode CreateTrustStoreRequest: "+err.Error())
		return
	}
	if req.Name == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Name is required")
		return
	}
	id := cfRandomID("TS")
	etag := cfETag()
	ocsp := false
	if req.UseClientCertificateOCSPEndpoint != nil {
		ocsp = *req.UseClientCertificateOCSPEndpoint
	}
	ts := CFTrustStore{
		Xmlns:                            cfNamespace,
		Id:                               id,
		Arn:                              cfTrustStoreARN(id),
		Name:                             req.Name,
		Status:                           "ACTIVE",
		NumberOfCaCertificates:           1,
		LastModifiedTime:                 cfNowISO(),
		UseClientCertificateOCSPEndpoint: ocsp,
	}
	cfTrustStores.Put(id, cfStoredTrustStore{Store: ts, ETag: etag})
	w.Header().Set("ETag", etag)
	cfWriteXML(w, http.StatusCreated, ts)
}

func handleCFGetTrustStore(w http.ResponseWriter, r *http.Request) {
	stored, ok := cfTrustStores.Get(r.PathValue("Identifier"))
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The trust store does not exist.")
		return
	}
	stored.Store.Xmlns = cfNamespace
	w.Header().Set("ETag", stored.ETag)
	cfWriteXML(w, http.StatusOK, stored.Store)
}

func handleCFUpdateTrustStore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfTrustStores.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The trust store does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	var req CFTrustStoreUpdateRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode UpdateTrustStoreRequest: "+err.Error())
		return
	}
	if hdr := r.Header.Get("UseClientCertificateOCSPEndpoint"); hdr != "" {
		stored.Store.UseClientCertificateOCSPEndpoint = strings.EqualFold(hdr, "true")
	}
	newETag := cfETag()
	stored.Store.LastModifiedTime = cfNowISO()
	stored.ETag = newETag
	cfTrustStores.Put(id, stored)
	stored.Store.Xmlns = cfNamespace
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.Store)
}

func handleCFDeleteTrustStore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfTrustStores.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The trust store does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	cfTrustStores.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleCFListTrustStores(w http.ResponseWriter, _ *http.Request) {
	items := []CFTrustStoreSummary{}
	for _, s := range cfTrustStores.List() {
		t := s.Store
		items = append(items, CFTrustStoreSummary{
			Id:                     t.Id,
			Arn:                    t.Arn,
			Name:                   t.Name,
			Status:                 t.Status,
			NumberOfCaCertificates: t.NumberOfCaCertificates,
			LastModifiedTime:       t.LastModifiedTime,
			Reason:                 t.Reason,
			ETag:                   s.ETag,
		})
	}
	cfWriteXML(w, http.StatusOK, CFListTrustStoresResult{Xmlns: cfNamespace, TrustStoreList: items})
}

func handleCFListDistributionsByTrustStore(w http.ResponseWriter, r *http.Request) {
	// No distribution references a trust store in the sim model, so this is an
	// honest empty distribution list (the trust store must exist).
	cfWriteDistributionList(w, nil)
}

// ----- Resource Policy handlers -----

func handleCFGetResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req CFGetResourcePolicyRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode GetResourcePolicyRequest: "+err.Error())
		return
	}
	stored, ok := cfResourcePolicies.Get(req.ResourceArn)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "No resource policy is attached to the specified resource.")
		return
	}
	cfWriteXML(w, http.StatusOK, CFGetResourcePolicyResult{
		Xmlns:          cfNamespace,
		ResourceArn:    stored.ResourceArn,
		PolicyDocument: stored.PolicyDocument,
	})
}

func handleCFPutResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req CFPutResourcePolicyRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode PutResourcePolicyRequest: "+err.Error())
		return
	}
	if req.ResourceArn == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "ResourceArn is required")
		return
	}
	cfResourcePolicies.Put(req.ResourceArn, cfStoredResourcePolicy{ResourceArn: req.ResourceArn, PolicyDocument: req.PolicyDocument})
	cfWriteXML(w, http.StatusOK, CFPutResourcePolicyResult{Xmlns: cfNamespace, ResourceArn: req.ResourceArn})
}

func handleCFDeleteResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req CFDeleteResourcePolicyRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode DeleteResourcePolicyRequest: "+err.Error())
		return
	}
	if _, ok := cfResourcePolicies.Get(req.ResourceArn); !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "No resource policy is attached to the specified resource.")
		return
	}
	cfResourcePolicies.Delete(req.ResourceArn)
	w.WriteHeader(http.StatusOK)
}

// ----- Distribution WebACL associations -----

func handleCFAssociateDistributionWebACL(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfDistributions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	var body CFAssociateWebACLBody
	if err := xml.NewDecoder(r.Body).Decode(&body); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode request: "+err.Error())
		return
	}
	stored.Distribution.DistributionConfig.WebACLId = body.WebACLArn
	newETag := cfETag()
	stored.ETag = newETag
	stored.Distribution.LastModifiedTime = cfNowISO()
	cfDistributions.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, CFAssociateDistributionWebACLResult{Xmlns: cfNamespace, Id: id, WebACLArn: body.WebACLArn})
}

func handleCFDisassociateDistributionWebACL(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stored, ok := cfDistributions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	stored.Distribution.DistributionConfig.WebACLId = ""
	newETag := cfETag()
	stored.ETag = newETag
	stored.Distribution.LastModifiedTime = cfNowISO()
	cfDistributions.Put(id, stored)
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, CFDisassociateDistributionWebACLResult{Xmlns: cfNamespace, Id: id})
}

// ----- Aliases + conflicts -----

func handleCFAssociateAlias(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("TargetDistributionId")
	stored, ok := cfDistributions.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "NoSuchDistribution", "The specified distribution does not exist.")
		return
	}
	alias := r.URL.Query().Get("Alias")
	if alias == "" {
		cfWriteError(w, http.StatusBadRequest, "InvalidArgument", "Alias query parameter is required")
		return
	}
	if stored.Distribution.DistributionConfig.Aliases == nil {
		stored.Distribution.DistributionConfig.Aliases = &CFAliases{}
	}
	a := stored.Distribution.DistributionConfig.Aliases
	a.Items = append(a.Items, alias)
	a.Quantity = len(a.Items)
	stored.Distribution.LastModifiedTime = cfNowISO()
	cfDistributions.Put(id, stored)
	w.WriteHeader(http.StatusOK)
}

func handleCFListConflictingAliases(w http.ResponseWriter, r *http.Request) {
	alias := r.URL.Query().Get("Alias")
	items := []CFConflictingAlias{}
	for _, stored := range cfDistributions.List() {
		if stored.Distribution.Id == r.URL.Query().Get("DistributionId") {
			continue
		}
		if stored.Distribution.DistributionConfig.Aliases == nil {
			continue
		}
		for _, a := range stored.Distribution.DistributionConfig.Aliases.Items {
			if a == alias {
				items = append(items, CFConflictingAlias{
					Alias:          a,
					DistributionId: stored.Distribution.Id,
					AccountId:      awsAccountID(),
				})
			}
		}
	}
	cfWriteXML(w, http.StatusOK, CFConflictingAliasesList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	})
}

func handleCFListDomainConflicts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		XMLName xml.Name `xml:"ListDomainConflictsRequest"`
		Domain  string   `xml:"Domain"`
	}
	_ = xml.NewDecoder(r.Body).Decode(&req)
	items := []CFDomainConflict{}
	for _, stored := range cfDistributions.List() {
		if stored.Distribution.DistributionConfig.Aliases == nil {
			continue
		}
		for _, a := range stored.Distribution.DistributionConfig.Aliases.Items {
			if a == req.Domain {
				items = append(items, CFDomainConflict{
					Domain:       a,
					ResourceType: "distribution",
					ResourceId:   stored.Distribution.Id,
					AccountId:    awsAccountID(),
				})
			}
		}
	}
	cfWriteXML(w, http.StatusOK, CFListDomainConflictsResult{Xmlns: cfNamespace, DomainConflicts: items})
}

func handleCFUpdateDomainAssociation(w http.ResponseWriter, r *http.Request) {
	var req CFUpdateDomainAssociationRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode UpdateDomainAssociationRequest: "+err.Error())
		return
	}
	resourceID := req.TargetResource.DistributionId
	if resourceID == "" {
		resourceID = req.TargetResource.DistributionTenantId
	}
	newETag := cfETag()
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, CFUpdateDomainAssociationResult{Xmlns: cfNamespace, Domain: req.Domain, ResourceId: resourceID})
}

func handleCFVerifyDnsConfiguration(w http.ResponseWriter, r *http.Request) {
	var req CFVerifyDnsConfigurationRequest
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode VerifyDnsConfigurationRequest: "+err.Error())
		return
	}
	cfWriteXML(w, http.StatusOK, CFVerifyDnsConfigurationResult{
		Xmlns: cfNamespace,
		DnsConfigurationList: []CFDnsConfiguration{
			{Domain: req.Domain, Status: "valid-configuration"},
		},
	})
}

// ----- ListDistributionsBy* projections -----

// cfWriteDistributionList renders the full DistributionList shape for the
// matching distributions.
func cfWriteDistributionList(w http.ResponseWriter, matches []cfStoredDistribution) {
	items := []CFDistributionSummary{}
	for _, stored := range matches {
		items = append(items, cfDistributionSummaryOf(stored.Distribution))
	}
	cfWriteXML(w, http.StatusOK, CFDistributionList{
		Xmlns:    cfNamespace,
		MaxItems: 100,
		Quantity: len(items),
		Items:    items,
	})
}

// cfDistributionSummaryOf projects a Distribution into its list summary shape.
func cfDistributionSummaryOf(d CFDistribution) CFDistributionSummary {
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
	return s
}

// cfDistMatcher reports whether a distribution references the given resource id.
type cfDistMatcher func(id string, d CFDistribution) bool

// cfDistsByIDListHandler builds a handler that projects matching distributions
// into the DistributionIdList shape, keyed on the named path value.
func cfDistsByIDListHandler(pathParam string, match cfDistMatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue(pathParam)
		ids := []string{}
		for _, stored := range cfDistributions.List() {
			if match(id, stored.Distribution) {
				ids = append(ids, stored.Distribution.Id)
			}
		}
		cfWriteXML(w, http.StatusOK, CFDistributionIdList{
			Xmlns:    cfNamespace,
			Marker:   "",
			MaxItems: 100,
			Quantity: len(ids),
			Items:    ids,
		})
	}
}

// cfDistsByListHandler builds a handler that projects matching distributions
// into the full DistributionList shape, keyed on the named path value.
func cfDistsByListHandler(pathParam string, match cfDistMatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue(pathParam)
		matches := []cfStoredDistribution{}
		for _, stored := range cfDistributions.List() {
			if match(id, stored.Distribution) {
				matches = append(matches, stored)
			}
		}
		cfWriteDistributionList(w, matches)
	}
}

// cfBehaviors iterates the default cache behavior plus every ordered behavior.
func cfBehaviors(d CFDistribution) []CFCacheBehavior {
	out := []CFCacheBehavior{d.DistributionConfig.DefaultCacheBehavior}
	if cb := d.DistributionConfig.CacheBehaviors; cb != nil {
		out = append(out, cb.Items...)
	}
	return out
}

func cfDistMatchesCachePolicy(id string, d CFDistribution) bool {
	for _, b := range cfBehaviors(d) {
		if b.CachePolicyId == id {
			return true
		}
	}
	return false
}

func cfDistMatchesOriginRequestPolicy(id string, d CFDistribution) bool {
	for _, b := range cfBehaviors(d) {
		if b.OriginRequestPolicyId == id {
			return true
		}
	}
	return false
}

func cfDistMatchesResponseHeadersPolicy(id string, d CFDistribution) bool {
	for _, b := range cfBehaviors(d) {
		if b.ResponseHeadersPolicyId == id {
			return true
		}
	}
	return false
}

func cfDistMatchesKeyGroup(id string, d CFDistribution) bool {
	for _, b := range cfBehaviors(d) {
		if b.TrustedKeyGroups == nil {
			continue
		}
		for _, kg := range b.TrustedKeyGroups.Items {
			if kg == id {
				return true
			}
		}
	}
	return false
}

func cfDistMatchesVpcOrigin(id string, d CFDistribution) bool {
	// The distribution Origin shape the sim models does not carry a
	// VpcOriginConfig block, so no stored distribution references a VPC origin
	// by id — an honest empty projection.
	_ = id
	_ = d
	return false
}

func cfDistMatchesWebACL(id string, d CFDistribution) bool {
	return d.DistributionConfig.WebACLId == id
}

func handleCFListDistributionsByRealtimeLogConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		XMLName               xml.Name `xml:"ListDistributionsByRealtimeLogConfigRequest"`
		RealtimeLogConfigName string   `xml:"RealtimeLogConfigName"`
		RealtimeLogConfigArn  string   `xml:"RealtimeLogConfigArn"`
	}
	_ = xml.NewDecoder(r.Body).Decode(&req)
	matches := []cfStoredDistribution{}
	for _, stored := range cfDistributions.List() {
		for _, b := range cfBehaviors(stored.Distribution) {
			if b.RealtimeLogConfigArn != "" && (b.RealtimeLogConfigArn == req.RealtimeLogConfigArn || (req.RealtimeLogConfigName != "" && strings.HasSuffix(b.RealtimeLogConfigArn, "/"+req.RealtimeLogConfigName))) {
				matches = append(matches, stored)
				break
			}
		}
	}
	cfWriteDistributionList(w, matches)
}

func handleCFListDistributionsByOwnedResource(w http.ResponseWriter, r *http.Request) {
	// No distribution in the sim is owned through a delegated resource, so this
	// is an honest empty owner list.
	cfWriteXML(w, http.StatusOK, CFDistributionIdOwnerList{
		Xmlns:    cfNamespace,
		Marker:   "",
		MaxItems: 100,
		Quantity: 0,
		Items:    []CFDistributionIdOwner{},
	})
}

// ----- Anycast IP List update + managed certificate -----

func handleCFUpdateAnycastIpList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("Id")
	stored, ok := cfAnycastIpLists.Get(id)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The Anycast IP list does not exist.")
		return
	}
	if msg := cfRequireIfMatch(r, stored.ETag); msg != "" {
		cfWriteIfMatchError(w, msg)
		return
	}
	var req struct {
		XMLName       xml.Name `xml:"UpdateAnycastIpListRequest"`
		IpAddressType string   `xml:"IpAddressType"`
	}
	// Every body field is optional; the CLI sends an empty body when only the
	// path Id + If-Match header change, so an EOF (empty body) is valid.
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		cfWriteError(w, http.StatusBadRequest, "MalformedXML", "Could not decode UpdateAnycastIpListRequest: "+err.Error())
		return
	}
	if req.IpAddressType != "" {
		stored.List.IpAddressType = req.IpAddressType
	}
	newETag := cfETag()
	stored.List.LastModifiedTime = cfNowISO()
	stored.ETag = newETag
	cfAnycastIpLists.Put(id, stored)
	stored.List.Xmlns = cfNamespace
	w.Header().Set("ETag", newETag)
	cfWriteXML(w, http.StatusOK, stored.List)
}

func handleCFGetManagedCertificateDetails(w http.ResponseWriter, r *http.Request) {
	ident := r.PathValue("Identifier")
	stored, ok := cfTenants.Get(ident)
	if !ok {
		cfWriteError(w, http.StatusNotFound, "EntityNotFound", "The distribution tenant does not exist.")
		return
	}
	host := ""
	if len(stored.Tenant.Domains) > 0 {
		host = "_cf." + stored.Tenant.Domains[0].Domain
	}
	cfWriteXML(w, http.StatusOK, CFManagedCertificateDetails{
		Xmlns:               cfNamespace,
		CertificateArn:      fmt.Sprintf("arn:aws:acm:us-east-1:%s:certificate/%s", awsAccountID(), strings.ToLower(ident)),
		CertificateStatus:   "valid",
		ValidationTokenHost: host,
	})
}
