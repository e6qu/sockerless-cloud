package main

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
)

// Application gateways are Azure's layer-7 load balancer. A frontend IP
// configuration and port define where clients arrive, an HTTP listener binds
// the two (optionally narrowed to host names), a request routing rule sends
// the listener's traffic to a backend pool with backend HTTP settings or
// through a URL path map, and a probe decides which pool members are healthy.
// network_appgateway_dataplane.go executes all of it, and BackendHealth reports
// what those same probes found.

type ApplicationGateway struct {
	azureNetworkResourceHeader
	Zones      []string                     `json:"zones,omitempty"`
	Identity   map[string]any               `json:"identity,omitempty"`
	Properties ApplicationGatewayProperties `json:"properties"`
}

type ApplicationGatewayProperties struct {
	Sku                                 *ApplicationGatewaySku                                 `json:"sku,omitempty"`
	SslPolicy                           *ApplicationGatewaySslPolicy                           `json:"sslPolicy,omitempty"`
	OperationalState                    string                                                 `json:"operationalState,omitempty"`
	GatewayIPConfigurations             []ApplicationGatewayIPConfiguration                    `json:"gatewayIPConfigurations,omitempty"`
	AuthenticationCertificates          []ApplicationGatewayCertificate                        `json:"authenticationCertificates,omitempty"`
	TrustedRootCertificates             []ApplicationGatewayCertificate                        `json:"trustedRootCertificates,omitempty"`
	TrustedClientCertificates           []ApplicationGatewayCertificate                        `json:"trustedClientCertificates,omitempty"`
	SslCertificates                     []ApplicationGatewayCertificate                        `json:"sslCertificates,omitempty"`
	FrontendIPConfigurations            []ApplicationGatewayFrontendIPConfiguration            `json:"frontendIPConfigurations,omitempty"`
	FrontendPorts                       []ApplicationGatewayFrontendPort                       `json:"frontendPorts,omitempty"`
	Probes                              []ApplicationGatewayProbe                              `json:"probes,omitempty"`
	BackendAddressPools                 []ApplicationGatewayBackendAddressPool                 `json:"backendAddressPools,omitempty"`
	BackendHTTPSettingsCollection       []ApplicationGatewayBackendHTTPSettings                `json:"backendHttpSettingsCollection,omitempty"`
	BackendSettingsCollection           []ApplicationGatewayBackendSettings                    `json:"backendSettingsCollection,omitempty"`
	HTTPListeners                       []ApplicationGatewayHTTPListener                       `json:"httpListeners,omitempty"`
	Listeners                           []ApplicationGatewayListener                           `json:"listeners,omitempty"`
	SslProfiles                         []ApplicationGatewaySslProfile                         `json:"sslProfiles,omitempty"`
	URLPathMaps                         []ApplicationGatewayURLPathMap                         `json:"urlPathMaps,omitempty"`
	RequestRoutingRules                 []ApplicationGatewayRequestRoutingRule                 `json:"requestRoutingRules,omitempty"`
	RoutingRules                        []ApplicationGatewayRoutingRule                        `json:"routingRules,omitempty"`
	RewriteRuleSets                     []ApplicationGatewayRewriteRuleSet                     `json:"rewriteRuleSets,omitempty"`
	RedirectConfigurations              []ApplicationGatewayRedirectConfiguration              `json:"redirectConfigurations,omitempty"`
	WebApplicationFirewallConfiguration *ApplicationGatewayWebApplicationFirewallConfiguration `json:"webApplicationFirewallConfiguration,omitempty"`
	FirewallPolicy                      *SubResource                                           `json:"firewallPolicy,omitempty"`
	EnableHTTP2                         bool                                                   `json:"enableHttp2,omitempty"`
	EnableFips                          bool                                                   `json:"enableFips,omitempty"`
	AutoscaleConfiguration              *ApplicationGatewayAutoscaleConfiguration              `json:"autoscaleConfiguration,omitempty"`
	PrivateLinkConfigurations           []ApplicationGatewayPrivateLinkConfiguration           `json:"privateLinkConfigurations,omitempty"`
	PrivateEndpointConnections          []ApplicationGatewayPrivateEndpointConnection          `json:"privateEndpointConnections,omitempty"`
	ResourceGUID                        string                                                 `json:"resourceGuid,omitempty"`
	ProvisioningState                   string                                                 `json:"provisioningState,omitempty"`
	CustomErrorConfigurations           []ApplicationGatewayCustomError                        `json:"customErrorConfigurations,omitempty"`
	ForceFirewallPolicyAssociation      bool                                                   `json:"forceFirewallPolicyAssociation,omitempty"`
	LoadDistributionPolicies            []ApplicationGatewayLoadDistributionPolicy             `json:"loadDistributionPolicies,omitempty"`
	EntraJWTValidationConfigs           []ApplicationGatewayEntraJWTValidationConfig           `json:"entraJWTValidationConfigs,omitempty"`
	GlobalConfiguration                 *ApplicationGatewayGlobalConfiguration                 `json:"globalConfiguration,omitempty"`
	DefaultPredefinedSslPolicy          string                                                 `json:"defaultPredefinedSslPolicy,omitempty"`
}

type ApplicationGatewaySku struct {
	Name     string `json:"name,omitempty"`
	Tier     string `json:"tier,omitempty"`
	Capacity int32  `json:"capacity,omitempty"`
	Family   string `json:"family,omitempty"`
}

type ApplicationGatewaySslPolicy struct {
	DisabledSslProtocols []string `json:"disabledSslProtocols,omitempty"`
	PolicyType           string   `json:"policyType,omitempty"`
	PolicyName           string   `json:"policyName,omitempty"`
	CipherSuites         []string `json:"cipherSuites,omitempty"`
	MinProtocolVersion   string   `json:"minProtocolVersion,omitempty"`
}

type applicationGatewayChild struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
	Etag string `json:"etag,omitempty"`
}

// Every collection member embeds applicationGatewayChild, so each satisfies
// applicationGatewayChildRef through method promotion.
func (c *applicationGatewayChild) child() *applicationGatewayChild { return c }

type applicationGatewayChildRef interface {
	child() *applicationGatewayChild
}

// The subnet the gateway's instances are deployed into.
type ApplicationGatewayIPConfiguration struct {
	applicationGatewayChild
	Properties ApplicationGatewayIPConfigurationProperties `json:"properties"`
}

type ApplicationGatewayIPConfigurationProperties struct {
	Subnet            *SubResource `json:"subnet,omitempty"`
	ProvisioningState string       `json:"provisioningState,omitempty"`
}

type ApplicationGatewayFrontendIPConfiguration struct {
	applicationGatewayChild
	Properties ApplicationGatewayFrontendIPConfigurationProperties `json:"properties"`
}

// A public IP reference, or a private address taken from a subnet.
type ApplicationGatewayFrontendIPConfigurationProperties struct {
	PrivateIPAddress          string       `json:"privateIPAddress,omitempty"`
	PrivateIPAllocationMethod string       `json:"privateIPAllocationMethod,omitempty"`
	Subnet                    *SubResource `json:"subnet,omitempty"`
	PublicIPAddress           *SubResource `json:"publicIPAddress,omitempty"`
	PrivateLinkConfiguration  *SubResource `json:"privateLinkConfiguration,omitempty"`
	ProvisioningState         string       `json:"provisioningState,omitempty"`
}

type ApplicationGatewayFrontendPort struct {
	applicationGatewayChild
	Properties ApplicationGatewayFrontendPortProperties `json:"properties"`
}

type ApplicationGatewayFrontendPortProperties struct {
	Port              int32  `json:"port,omitempty"`
	ProvisioningState string `json:"provisioningState,omitempty"`
}

type ApplicationGatewayBackendAddressPool struct {
	applicationGatewayChild
	Properties ApplicationGatewayBackendAddressPoolProperties `json:"properties"`
}

// The pool's declared addresses and the interface IP configurations that
// joined it.
type ApplicationGatewayBackendAddressPoolProperties struct {
	BackendIPConfigurations []NetworkInterfaceIPConfiguration  `json:"backendIPConfigurations,omitempty"`
	BackendAddresses        []ApplicationGatewayBackendAddress `json:"backendAddresses,omitempty"`
	ProvisioningState       string                             `json:"provisioningState,omitempty"`
}

type ApplicationGatewayBackendAddress struct {
	Fqdn      string `json:"fqdn,omitempty"`
	IPAddress string `json:"ipAddress,omitempty"`
}

type ApplicationGatewayBackendHTTPSettings struct {
	applicationGatewayChild
	Properties ApplicationGatewayBackendHTTPSettingsProperties `json:"properties"`
}

type ApplicationGatewayBackendHTTPSettingsProperties struct {
	Port                           int32                                 `json:"port,omitempty"`
	Protocol                       string                                `json:"protocol,omitempty"`
	CookieBasedAffinity            string                                `json:"cookieBasedAffinity,omitempty"`
	RequestTimeout                 int32                                 `json:"requestTimeout,omitempty"`
	Probe                          *SubResource                          `json:"probe,omitempty"`
	AuthenticationCertificates     []SubResource                         `json:"authenticationCertificates,omitempty"`
	TrustedRootCertificates        []SubResource                         `json:"trustedRootCertificates,omitempty"`
	ConnectionDraining             *ApplicationGatewayConnectionDraining `json:"connectionDraining,omitempty"`
	HostName                       string                                `json:"hostName,omitempty"`
	PickHostNameFromBackendAddress bool                                  `json:"pickHostNameFromBackendAddress,omitempty"`
	AffinityCookieName             string                                `json:"affinityCookieName,omitempty"`
	ProbeEnabled                   bool                                  `json:"probeEnabled,omitempty"`
	Path                           string                                `json:"path,omitempty"`
	DedicatedBackendConnection     bool                                  `json:"dedicatedBackendConnection,omitempty"`
	ValidateCertChainAndExpiry     bool                                  `json:"validateCertChainAndExpiry,omitempty"`
	ValidateSNI                    bool                                  `json:"validateSNI,omitempty"`
	SniName                        string                                `json:"sniName,omitempty"`
	ProvisioningState              string                                `json:"provisioningState,omitempty"`
}

type ApplicationGatewayConnectionDraining struct {
	Enabled           bool  `json:"enabled"`
	DrainTimeoutInSec int32 `json:"drainTimeoutInSec"`
}

// The layer-4 counterpart of backend HTTP settings.
type ApplicationGatewayBackendSettings struct {
	applicationGatewayChild
	Properties ApplicationGatewayBackendSettingsProperties `json:"properties"`
}

type ApplicationGatewayBackendSettingsProperties struct {
	Port                           int32         `json:"port,omitempty"`
	Protocol                       string        `json:"protocol,omitempty"`
	Timeout                        int32         `json:"timeout,omitempty"`
	Probe                          *SubResource  `json:"probe,omitempty"`
	TrustedRootCertificates        []SubResource `json:"trustedRootCertificates,omitempty"`
	HostName                       string        `json:"hostName,omitempty"`
	PickHostNameFromBackendAddress bool          `json:"pickHostNameFromBackendAddress,omitempty"`
	EnableL4ClientIPPreservation   bool          `json:"enableL4ClientIpPreservation,omitempty"`
	ProvisioningState              string        `json:"provisioningState,omitempty"`
}

type ApplicationGatewayHTTPListener struct {
	applicationGatewayChild
	Properties ApplicationGatewayHTTPListenerProperties `json:"properties"`
}

type ApplicationGatewayHTTPListenerProperties struct {
	FrontendIPConfiguration     *SubResource                    `json:"frontendIPConfiguration,omitempty"`
	FrontendPort                *SubResource                    `json:"frontendPort,omitempty"`
	Protocol                    string                          `json:"protocol,omitempty"`
	HostName                    string                          `json:"hostName,omitempty"`
	SslCertificate              *SubResource                    `json:"sslCertificate,omitempty"`
	SslProfile                  *SubResource                    `json:"sslProfile,omitempty"`
	RequireServerNameIndication bool                            `json:"requireServerNameIndication,omitempty"`
	ProvisioningState           string                          `json:"provisioningState,omitempty"`
	CustomErrorConfigurations   []ApplicationGatewayCustomError `json:"customErrorConfigurations,omitempty"`
	FirewallPolicy              *SubResource                    `json:"firewallPolicy,omitempty"`
	HostNames                   []string                        `json:"hostNames,omitempty"`
}

// The layer-4 counterpart of an HTTP listener.
type ApplicationGatewayListener struct {
	applicationGatewayChild
	Properties ApplicationGatewayListenerProperties `json:"properties"`
}

type ApplicationGatewayListenerProperties struct {
	FrontendIPConfiguration *SubResource `json:"frontendIPConfiguration,omitempty"`
	FrontendPort            *SubResource `json:"frontendPort,omitempty"`
	Protocol                string       `json:"protocol,omitempty"`
	SslCertificate          *SubResource `json:"sslCertificate,omitempty"`
	SslProfile              *SubResource `json:"sslProfile,omitempty"`
	ProvisioningState       string       `json:"provisioningState,omitempty"`
	HostNames               []string     `json:"hostNames,omitempty"`
}

type ApplicationGatewayRequestRoutingRule struct {
	applicationGatewayChild
	Properties ApplicationGatewayRequestRoutingRuleProperties `json:"properties"`
}

// Routes a listener's traffic to a pool, a URL path map or a redirect
// configuration.
type ApplicationGatewayRequestRoutingRuleProperties struct {
	RuleType                 string       `json:"ruleType,omitempty"`
	Priority                 int32        `json:"priority,omitempty"`
	BackendAddressPool       *SubResource `json:"backendAddressPool,omitempty"`
	BackendHTTPSettings      *SubResource `json:"backendHttpSettings,omitempty"`
	HTTPListener             *SubResource `json:"httpListener,omitempty"`
	URLPathMap               *SubResource `json:"urlPathMap,omitempty"`
	RewriteRuleSet           *SubResource `json:"rewriteRuleSet,omitempty"`
	RedirectConfiguration    *SubResource `json:"redirectConfiguration,omitempty"`
	LoadDistributionPolicy   *SubResource `json:"loadDistributionPolicy,omitempty"`
	EntraJWTValidationConfig *SubResource `json:"entraJWTValidationConfig,omitempty"`
	ProvisioningState        string       `json:"provisioningState,omitempty"`
}

// The layer-4 counterpart of a request routing rule.
type ApplicationGatewayRoutingRule struct {
	applicationGatewayChild
	Properties ApplicationGatewayRoutingRuleProperties `json:"properties"`
}

type ApplicationGatewayRoutingRuleProperties struct {
	RuleType           string       `json:"ruleType,omitempty"`
	Priority           int32        `json:"priority,omitempty"`
	BackendAddressPool *SubResource `json:"backendAddressPool,omitempty"`
	BackendSettings    *SubResource `json:"backendSettings,omitempty"`
	Listener           *SubResource `json:"listener,omitempty"`
	ProvisioningState  string       `json:"provisioningState,omitempty"`
}

type ApplicationGatewayURLPathMap struct {
	applicationGatewayChild
	Properties ApplicationGatewayURLPathMapProperties `json:"properties"`
}

type ApplicationGatewayURLPathMapProperties struct {
	DefaultBackendAddressPool     *SubResource                 `json:"defaultBackendAddressPool,omitempty"`
	DefaultBackendHTTPSettings    *SubResource                 `json:"defaultBackendHttpSettings,omitempty"`
	DefaultRewriteRuleSet         *SubResource                 `json:"defaultRewriteRuleSet,omitempty"`
	DefaultRedirectConfiguration  *SubResource                 `json:"defaultRedirectConfiguration,omitempty"`
	DefaultLoadDistributionPolicy *SubResource                 `json:"defaultLoadDistributionPolicy,omitempty"`
	PathRules                     []ApplicationGatewayPathRule `json:"pathRules,omitempty"`
	ProvisioningState             string                       `json:"provisioningState,omitempty"`
}

type ApplicationGatewayPathRule struct {
	applicationGatewayChild
	Properties ApplicationGatewayPathRuleProperties `json:"properties"`
}

type ApplicationGatewayPathRuleProperties struct {
	Paths                  []string     `json:"paths,omitempty"`
	BackendAddressPool     *SubResource `json:"backendAddressPool,omitempty"`
	BackendHTTPSettings    *SubResource `json:"backendHttpSettings,omitempty"`
	RedirectConfiguration  *SubResource `json:"redirectConfiguration,omitempty"`
	RewriteRuleSet         *SubResource `json:"rewriteRuleSet,omitempty"`
	LoadDistributionPolicy *SubResource `json:"loadDistributionPolicy,omitempty"`
	ProvisioningState      string       `json:"provisioningState,omitempty"`
	FirewallPolicy         *SubResource `json:"firewallPolicy,omitempty"`
}

type ApplicationGatewayProbe struct {
	applicationGatewayChild
	Properties ApplicationGatewayProbeProperties `json:"properties"`
}

type ApplicationGatewayProbeProperties struct {
	Protocol                            string                                      `json:"protocol,omitempty"`
	Host                                string                                      `json:"host,omitempty"`
	Path                                string                                      `json:"path,omitempty"`
	Interval                            int32                                       `json:"interval,omitempty"`
	Timeout                             int32                                       `json:"timeout,omitempty"`
	UnhealthyThreshold                  int32                                       `json:"unhealthyThreshold,omitempty"`
	PickHostNameFromBackendHTTPSettings bool                                        `json:"pickHostNameFromBackendHttpSettings,omitempty"`
	PickHostNameFromBackendSettings     bool                                        `json:"pickHostNameFromBackendSettings,omitempty"`
	MinServers                          int32                                       `json:"minServers,omitempty"`
	Match                               *ApplicationGatewayProbeHealthResponseMatch `json:"match,omitempty"`
	EnableProbeProxyProtocolHeader      bool                                        `json:"enableProbeProxyProtocolHeader,omitempty"`
	ProvisioningState                   string                                      `json:"provisioningState,omitempty"`
	Port                                int32                                       `json:"port,omitempty"`
}

type ApplicationGatewayProbeHealthResponseMatch struct {
	Body        string   `json:"body,omitempty"`
	StatusCodes []string `json:"statusCodes,omitempty"`
}

type ApplicationGatewayRedirectConfiguration struct {
	applicationGatewayChild
	Properties ApplicationGatewayRedirectConfigurationProperties `json:"properties"`
}

type ApplicationGatewayRedirectConfigurationProperties struct {
	RedirectType        string        `json:"redirectType,omitempty"`
	TargetListener      *SubResource  `json:"targetListener,omitempty"`
	TargetURL           string        `json:"targetUrl,omitempty"`
	IncludePath         *bool         `json:"includePath,omitempty"`
	IncludeQueryString  *bool         `json:"includeQueryString,omitempty"`
	RequestRoutingRules []SubResource `json:"requestRoutingRules,omitempty"`
	URLPathMaps         []SubResource `json:"urlPathMaps,omitempty"`
	PathRules           []SubResource `json:"pathRules,omitempty"`
}

type ApplicationGatewayRewriteRuleSet struct {
	applicationGatewayChild
	Properties ApplicationGatewayRewriteRuleSetProperties `json:"properties"`
}

type ApplicationGatewayRewriteRuleSetProperties struct {
	RewriteRules      []ApplicationGatewayRewriteRule `json:"rewriteRules,omitempty"`
	ProvisioningState string                          `json:"provisioningState,omitempty"`
}

// Conditions, and the header and URL changes applied when they all hold.
type ApplicationGatewayRewriteRule struct {
	Name         string                                   `json:"name,omitempty"`
	RuleSequence int32                                    `json:"ruleSequence,omitempty"`
	Conditions   []ApplicationGatewayRewriteRuleCondition `json:"conditions,omitempty"`
	ActionSet    *ApplicationGatewayRewriteRuleActionSet  `json:"actionSet,omitempty"`
}

type ApplicationGatewayRewriteRuleCondition struct {
	Variable   string `json:"variable,omitempty"`
	Pattern    string `json:"pattern,omitempty"`
	IgnoreCase bool   `json:"ignoreCase,omitempty"`
	Negate     bool   `json:"negate,omitempty"`
}

type ApplicationGatewayRewriteRuleActionSet struct {
	RequestHeaderConfigurations  []ApplicationGatewayHeaderConfiguration `json:"requestHeaderConfigurations,omitempty"`
	ResponseHeaderConfigurations []ApplicationGatewayHeaderConfiguration `json:"responseHeaderConfigurations,omitempty"`
	URLConfiguration             *ApplicationGatewayURLConfiguration     `json:"urlConfiguration,omitempty"`
}

// An empty header value deletes the header, as the rewrite engine does.
type ApplicationGatewayHeaderConfiguration struct {
	HeaderName         string                                `json:"headerName,omitempty"`
	HeaderValue        string                                `json:"headerValue,omitempty"`
	HeaderValueMatcher *ApplicationGatewayHeaderValueMatcher `json:"headerValueMatcher,omitempty"`
}

type ApplicationGatewayHeaderValueMatcher struct {
	Pattern    string `json:"pattern,omitempty"`
	IgnoreCase bool   `json:"ignoreCase,omitempty"`
	Negate     bool   `json:"negate,omitempty"`
}

type ApplicationGatewayURLConfiguration struct {
	ModifiedPath        string `json:"modifiedPath,omitempty"`
	ModifiedQueryString string `json:"modifiedQueryString,omitempty"`
	Reroute             bool   `json:"reroute,omitempty"`
}

// The shared shape of the authentication, trusted-root, trusted-client and SSL
// certificate collections; each keeps only the members the specification
// declares for it.
type ApplicationGatewayCertificate struct {
	applicationGatewayChild
	Properties ApplicationGatewayCertificateProperties `json:"properties"`
}

// Inline material, or a Key Vault secret reference.
type ApplicationGatewayCertificateProperties struct {
	Data               string `json:"data,omitempty"`
	Password           string `json:"password,omitempty"`
	PublicCertData     string `json:"publicCertData,omitempty"`
	KeyVaultSecretID   string `json:"keyVaultSecretId,omitempty"`
	ValidatedCertData  string `json:"validatedCertData,omitempty"`
	ClientCertIssuerDN string `json:"clientCertIssuerDN,omitempty"`
	ProvisioningState  string `json:"provisioningState,omitempty"`
}

type ApplicationGatewaySslProfile struct {
	applicationGatewayChild
	Properties ApplicationGatewaySslProfileProperties `json:"properties"`
}

type ApplicationGatewaySslProfileProperties struct {
	TrustedClientCertificates []SubResource                              `json:"trustedClientCertificates,omitempty"`
	SslPolicy                 *ApplicationGatewaySslPolicy               `json:"sslPolicy,omitempty"`
	ClientAuthConfiguration   *ApplicationGatewayClientAuthConfiguration `json:"clientAuthConfiguration,omitempty"`
	ProvisioningState         string                                     `json:"provisioningState,omitempty"`
}

type ApplicationGatewayClientAuthConfiguration struct {
	VerifyClientCertIssuerDN bool   `json:"verifyClientCertIssuerDN,omitempty"`
	VerifyClientRevocation   string `json:"verifyClientRevocation,omitempty"`
	VerifyClientAuthMode     string `json:"verifyClientAuthMode,omitempty"`
}

// The private-link projection of a frontend IP configuration.
type ApplicationGatewayPrivateLinkConfiguration struct {
	applicationGatewayChild
	Properties ApplicationGatewayPrivateLinkConfigurationProperties `json:"properties"`
}

type ApplicationGatewayPrivateLinkConfigurationProperties struct {
	IPConfigurations  []ApplicationGatewayPrivateLinkIPConfiguration `json:"ipConfigurations,omitempty"`
	ProvisioningState string                                         `json:"provisioningState,omitempty"`
}

type ApplicationGatewayPrivateLinkIPConfiguration struct {
	applicationGatewayChild
	Properties ApplicationGatewayPrivateLinkIPConfigurationProperties `json:"properties"`
}

type ApplicationGatewayPrivateLinkIPConfigurationProperties struct {
	PrivateIPAddress          string       `json:"privateIPAddress,omitempty"`
	PrivateIPAllocationMethod string       `json:"privateIPAllocationMethod,omitempty"`
	Subnet                    *SubResource `json:"subnet,omitempty"`
	Primary                   bool         `json:"primary,omitempty"`
	ProvisioningState         string       `json:"provisioningState,omitempty"`
}

type ApplicationGatewayLoadDistributionPolicy struct {
	applicationGatewayChild
	Properties ApplicationGatewayLoadDistributionPolicyProperties `json:"properties"`
}

type ApplicationGatewayLoadDistributionPolicyProperties struct {
	LoadDistributionTargets   []ApplicationGatewayLoadDistributionTarget `json:"loadDistributionTargets,omitempty"`
	LoadDistributionAlgorithm string                                     `json:"loadDistributionAlgorithm,omitempty"`
	ProvisioningState         string                                     `json:"provisioningState,omitempty"`
}

type ApplicationGatewayLoadDistributionTarget struct {
	applicationGatewayChild
	Properties ApplicationGatewayLoadDistributionTargetProperties `json:"properties"`
}

type ApplicationGatewayLoadDistributionTargetProperties struct {
	WeightPerServer    int32        `json:"weightPerServer,omitempty"`
	BackendAddressPool *SubResource `json:"backendAddressPool,omitempty"`
}

// Microsoft Entra JSON Web Token validation for a routing rule.
type ApplicationGatewayEntraJWTValidationConfig struct {
	applicationGatewayChild
	Properties ApplicationGatewayEntraJWTValidationConfigProperties `json:"properties"`
}

type ApplicationGatewayEntraJWTValidationConfigProperties struct {
	UnAuthorizedRequestAction string   `json:"unAuthorizedRequestAction,omitempty"`
	TenantID                  string   `json:"tenantId,omitempty"`
	ClientID                  string   `json:"clientId,omitempty"`
	Audiences                 []string `json:"audiences,omitempty"`
	ProvisioningState         string   `json:"provisioningState,omitempty"`
}

type ApplicationGatewayCustomError struct {
	StatusCode         string `json:"statusCode,omitempty"`
	CustomErrorPageURL string `json:"customErrorPageUrl,omitempty"`
}

type ApplicationGatewayAutoscaleConfiguration struct {
	MinCapacity int32 `json:"minCapacity"`
	MaxCapacity int32 `json:"maxCapacity,omitempty"`
}

type ApplicationGatewayGlobalConfiguration struct {
	EnableRequestBuffering  bool `json:"enableRequestBuffering,omitempty"`
	EnableResponseBuffering bool `json:"enableResponseBuffering,omitempty"`
}

type ApplicationGatewayWebApplicationFirewallConfiguration struct {
	Enabled                bool             `json:"enabled"`
	FirewallMode           string           `json:"firewallMode,omitempty"`
	RuleSetType            string           `json:"ruleSetType,omitempty"`
	RuleSetVersion         string           `json:"ruleSetVersion,omitempty"`
	DisabledRuleGroups     []map[string]any `json:"disabledRuleGroups,omitempty"`
	RequestBodyCheck       bool             `json:"requestBodyCheck,omitempty"`
	MaxRequestBodySize     int32            `json:"maxRequestBodySize,omitempty"`
	MaxRequestBodySizeInKb int32            `json:"maxRequestBodySizeInKb,omitempty"`
	FileUploadLimitInMb    int32            `json:"fileUploadLimitInMb,omitempty"`
	Exclusions             []map[string]any `json:"exclusions,omitempty"`
}

type ApplicationGatewayPrivateEndpointConnection struct {
	applicationGatewayChild
	Properties ApplicationGatewayPrivateEndpointConnectionProperties `json:"properties"`
}

// The private endpoint is stored as a bare reference and rendered from the live
// endpoint record on read.
type ApplicationGatewayPrivateEndpointConnectionProperties struct {
	PrivateEndpoint                   *PrivateEndpoint                   `json:"privateEndpoint,omitempty"`
	PrivateLinkServiceConnectionState *PrivateLinkServiceConnectionState `json:"privateLinkServiceConnectionState,omitempty"`
	ProvisioningState                 string                             `json:"provisioningState,omitempty"`
	LinkIdentifier                    string                             `json:"linkIdentifier,omitempty"`
}

// The group a consumer's private endpoint asks for.
type ApplicationGatewayPrivateLinkResource struct {
	applicationGatewayChild
	Properties ApplicationGatewayPrivateLinkResourceProperties `json:"properties"`
}

type ApplicationGatewayPrivateLinkResourceProperties struct {
	GroupID           string   `json:"groupId,omitempty"`
	RequiredMembers   []string `json:"requiredMembers,omitempty"`
	RequiredZoneNames []string `json:"requiredZoneNames,omitempty"`
}

var (
	azureApplicationGateways sim.Store[ApplicationGateway]
	// Keyed by connection ID, so the gateway's collection and the connection
	// surface read one object.
	azureAppGatewayPEConnections sim.Store[ApplicationGatewayPrivateEndpointConnection]
)

const azureAppGatewayType = "Microsoft.Network/applicationGateways"

func registerNetworkApplicationGateways(srv *sim.Server) {
	azureApplicationGateways = sim.MakeStore[ApplicationGateway](srv.DB(), "network_application_gateways")
	azureAppGatewayPEConnections = sim.MakeStore[ApplicationGatewayPrivateEndpointConnection](srv.DB(), "network_application_gateway_pe_connections")

	registerAzureNetworkResource(srv, azureNetworkResourceSpec[ApplicationGateway]{
		collection:   "applicationGateways",
		nameParam:    "applicationGatewayName",
		resourceType: azureAppGatewayType,
		store:        azureApplicationGateways,
		header: func(gw *ApplicationGateway) *azureNetworkResourceHeader {
			return &gw.azureNetworkResourceHeader
		},
		validate:    validateApplicationGateway,
		provision:   provisionApplicationGateway,
		project:     projectApplicationGateway,
		afterDelete: deleteApplicationGatewayResources,
	})

	// The consumer's endpoint opens the connection in the gateway's own
	// collection, so both sides see one object. A gateway's groups are its private
	// link configurations' names, not a fixed set, so the entry declares none.
	azurePrivateLinkTargets = append(azurePrivateLinkTargets, azurePrivateLinkTarget{
		armType:   azureAppGatewayType,
		childType: azureAppGatewayType + "/privateEndpointConnections",
		put: func(id, name string, props map[string]any) {
			conn := ApplicationGatewayPrivateEndpointConnection{
				applicationGatewayChild: applicationGatewayChild{
					ID: id, Name: name,
					Type: azureAppGatewayType + "/privateEndpointConnections",
					Etag: azureNetworkEtag(),
				},
			}
			conn.Properties.ProvisioningState = azureMapString(props, "provisioningState")
			conn.Properties.LinkIdentifier = azureMapString(props, "linkIdentifier")
			if pe, ok := props["privateEndpoint"].(map[string]any); ok {
				conn.Properties.PrivateEndpoint = &PrivateEndpoint{
					azureNetworkResourceHeader: azureNetworkResourceHeader{ID: azureMapString(pe, "id")},
				}
			}
			if state, ok := props["privateLinkServiceConnectionState"].(map[string]any); ok {
				conn.Properties.PrivateLinkServiceConnectionState = &PrivateLinkServiceConnectionState{
					Status:          azureMapString(state, "status"),
					Description:     azureMapString(state, "description"),
					ActionsRequired: azureMapString(state, "actionsRequired"),
				}
			}
			azureAppGatewayPEConnections.Put(id, conn)
		},
		get: func(id string) (map[string]any, bool) {
			conn, ok := azureAppGatewayPEConnections.Get(id)
			if !ok {
				return nil, false
			}
			props := map[string]any{
				"provisioningState": conn.Properties.ProvisioningState,
				"linkIdentifier":    conn.Properties.LinkIdentifier,
			}
			if conn.Properties.PrivateEndpoint != nil {
				props["privateEndpoint"] = map[string]any{"id": conn.Properties.PrivateEndpoint.ID}
			}
			if state := conn.Properties.PrivateLinkServiceConnectionState; state != nil {
				props["privateLinkServiceConnectionState"] = map[string]any{
					"status":          state.Status,
					"description":     state.Description,
					"actionsRequired": state.ActionsRequired,
				}
			}
			return props, true
		},
		del: func(id string) { azureAppGatewayPEConnections.Delete(id) },
	})

	registerApplicationGatewayOperations(srv)
	registerApplicationGatewayPrivateLink(srv)
	registerApplicationGatewayCatalogs(srv)
	registerApplicationGatewayWafRuleSets(srv)
	registerApplicationGatewayDataPlane(srv)
	srv.StartBackground("Azure Application Gateway health prober", func(ctx context.Context) {
		lbplane.SweepEvery(ctx, applicationGatewayProbeSweep, applicationGatewaySweepProbes)
	})
}

// Every child collection member must be named, and every outside reference —
// the deployment subnet, a frontend's subnet or public IP — must resolve.
func validateApplicationGateway(w http.ResponseWriter, _ *http.Request, gw *ApplicationGateway) bool {
	named := func(name, collection string) bool {
		if name != "" {
			return true
		}
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"The request format was unexpected: every %s member of an application gateway requires a name.", collection)
		return false
	}
	for _, ipcfg := range gw.Properties.GatewayIPConfigurations {
		if !named(ipcfg.Name, "gatewayIPConfigurations") {
			return false
		}
		subnetID := ""
		if ipcfg.Properties.Subnet != nil {
			subnetID = ipcfg.Properties.Subnet.ID
		}
		if _, ok := azureRequireSubnet(w, subnetID); !ok {
			return false
		}
	}
	for _, fe := range gw.Properties.FrontendIPConfigurations {
		if !named(fe.Name, "frontendIPConfigurations") {
			return false
		}
		if fe.Properties.PublicIPAddress != nil && fe.Properties.PublicIPAddress.ID != "" {
			if _, ok := azurePublicIPs.Get(fe.Properties.PublicIPAddress.ID); !ok {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"The Resource %q was not found.", fe.Properties.PublicIPAddress.ID)
				return false
			}
			continue
		}
		if fe.Properties.Subnet != nil {
			if _, ok := azureRequireSubnet(w, fe.Properties.Subnet.ID); !ok {
				return false
			}
		}
	}
	for _, plc := range gw.Properties.PrivateLinkConfigurations {
		if !named(plc.Name, "privateLinkConfigurations") {
			return false
		}
		for _, ipcfg := range plc.Properties.IPConfigurations {
			subnetID := ""
			if ipcfg.Properties.Subnet != nil {
				subnetID = ipcfg.Properties.Subnet.ID
			}
			if _, ok := azureRequireSubnet(w, subnetID); !ok {
				return false
			}
		}
	}
	for _, collection := range []struct {
		names []string
		label string
	}{
		{applicationGatewayNames(gw.Properties.FrontendPorts), "frontendPorts"},
		{applicationGatewayNames(gw.Properties.BackendAddressPools), "backendAddressPools"},
		{applicationGatewayNames(gw.Properties.BackendHTTPSettingsCollection), "backendHttpSettingsCollection"},
		{applicationGatewayNames(gw.Properties.HTTPListeners), "httpListeners"},
		{applicationGatewayNames(gw.Properties.RequestRoutingRules), "requestRoutingRules"},
		{applicationGatewayNames(gw.Properties.URLPathMaps), "urlPathMaps"},
		{applicationGatewayNames(gw.Properties.Probes), "probes"},
		{applicationGatewayNames(gw.Properties.RedirectConfigurations), "redirectConfigurations"},
		{applicationGatewayNames(gw.Properties.RewriteRuleSets), "rewriteRuleSets"},
	} {
		for _, name := range collection.names {
			if !named(name, collection.label) {
				return false
			}
		}
	}
	return true
}

func applicationGatewayNames[T any, PT interface {
	*T
	applicationGatewayChildRef
}](items []T) []string {
	names := make([]string, 0, len(items))
	for i := range items {
		names = append(names, PT(&items[i]).child().Name)
	}
	return names
}

// applicationGatewayStamp assigns each member the ID composed from the gateway
// and collection, the child type and a fresh etag, then applies the
// collection's defaults and provisioning state.
func applicationGatewayStamp[T any, PT interface {
	*T
	applicationGatewayChildRef
}](gatewayID, collection string, items []T, ready func(PT)) {
	for i := range items {
		item := PT(&items[i])
		head := item.child()
		head.ID = armChildID(gatewayID, collection, head.Name)
		head.Type = azureAppGatewayType + "/" + collection
		head.Etag = azureNetworkEtag()
		if ready != nil {
			ready(item)
		}
	}
}

func stampApplicationGatewayChildren(gw *ApplicationGateway) {
	p := &gw.Properties
	applicationGatewayStamp(gw.ID, "gatewayIPConfigurations", p.GatewayIPConfigurations, func(c *ApplicationGatewayIPConfiguration) {
		c.Properties.ProvisioningState = "Succeeded"
	})
	for _, certs := range []struct {
		collection string
		items      []ApplicationGatewayCertificate
	}{
		{"authenticationCertificates", p.AuthenticationCertificates},
		{"trustedRootCertificates", p.TrustedRootCertificates},
		{"trustedClientCertificates", p.TrustedClientCertificates},
		{"sslCertificates", p.SslCertificates},
	} {
		applicationGatewayStamp(gw.ID, certs.collection, certs.items, func(c *ApplicationGatewayCertificate) {
			c.Properties.ProvisioningState = "Succeeded"
		})
	}
	applicationGatewayStamp(gw.ID, "frontendIPConfigurations", p.FrontendIPConfigurations, func(c *ApplicationGatewayFrontendIPConfiguration) {
		// The service reports an allocation method on every frontend; one naming no
		// static address is dynamic.
		if c.Properties.PrivateIPAllocationMethod == "" {
			if c.Properties.PrivateIPAddress != "" {
				c.Properties.PrivateIPAllocationMethod = "Static"
			} else {
				c.Properties.PrivateIPAllocationMethod = "Dynamic"
			}
		}
		c.Properties.ProvisioningState = "Succeeded"
	})
	applicationGatewayStamp(gw.ID, "frontendPorts", p.FrontendPorts, func(c *ApplicationGatewayFrontendPort) {
		c.Properties.ProvisioningState = "Succeeded"
	})
	applicationGatewayStamp(gw.ID, "probes", p.Probes, func(c *ApplicationGatewayProbe) {
		if c.Properties.Protocol == "" {
			c.Properties.Protocol = "Http"
		}
		if c.Properties.Path == "" {
			c.Properties.Path = "/"
		}
		c.Properties.ProvisioningState = "Succeeded"
	})
	applicationGatewayStamp(gw.ID, "backendAddressPools", p.BackendAddressPools, func(c *ApplicationGatewayBackendAddressPool) {
		c.Properties.ProvisioningState = "Succeeded"
	})
	applicationGatewayStamp(gw.ID, "backendHttpSettingsCollection", p.BackendHTTPSettingsCollection, func(c *ApplicationGatewayBackendHTTPSettings) {
		if c.Properties.Protocol == "" {
			c.Properties.Protocol = "Http"
		}
		if c.Properties.CookieBasedAffinity == "" {
			c.Properties.CookieBasedAffinity = "Disabled"
		}
		if c.Properties.RequestTimeout == 0 {
			c.Properties.RequestTimeout = 30
		}
		c.Properties.ProvisioningState = "Succeeded"
	})
	applicationGatewayStamp(gw.ID, "backendSettingsCollection", p.BackendSettingsCollection, func(c *ApplicationGatewayBackendSettings) {
		if c.Properties.Protocol == "" {
			c.Properties.Protocol = "Tcp"
		}
		c.Properties.ProvisioningState = "Succeeded"
	})
	applicationGatewayStamp(gw.ID, "httpListeners", p.HTTPListeners, func(c *ApplicationGatewayHTTPListener) {
		if c.Properties.Protocol == "" {
			c.Properties.Protocol = "Http"
		}
		c.Properties.ProvisioningState = "Succeeded"
	})
	applicationGatewayStamp(gw.ID, "listeners", p.Listeners, func(c *ApplicationGatewayListener) {
		c.Properties.ProvisioningState = "Succeeded"
	})
	applicationGatewayStamp(gw.ID, "sslProfiles", p.SslProfiles, func(c *ApplicationGatewaySslProfile) {
		c.Properties.ProvisioningState = "Succeeded"
	})
	applicationGatewayStamp(gw.ID, "urlPathMaps", p.URLPathMaps, func(c *ApplicationGatewayURLPathMap) {
		// A path rule's ID is composed from its map's ID.
		applicationGatewayStamp(c.ID, "pathRules", c.Properties.PathRules, func(rule *ApplicationGatewayPathRule) {
			rule.Type = azureAppGatewayType + "/urlPathMaps/pathRules"
			rule.Properties.ProvisioningState = "Succeeded"
		})
		c.Properties.ProvisioningState = "Succeeded"
	})
	applicationGatewayStamp(gw.ID, "requestRoutingRules", p.RequestRoutingRules, func(c *ApplicationGatewayRequestRoutingRule) {
		if c.Properties.RuleType == "" {
			c.Properties.RuleType = "Basic"
		}
		c.Properties.ProvisioningState = "Succeeded"
	})
	applicationGatewayStamp(gw.ID, "routingRules", p.RoutingRules, func(c *ApplicationGatewayRoutingRule) {
		if c.Properties.RuleType == "" {
			c.Properties.RuleType = "Basic"
		}
		c.Properties.ProvisioningState = "Succeeded"
	})
	applicationGatewayStamp(gw.ID, "rewriteRuleSets", p.RewriteRuleSets, func(c *ApplicationGatewayRewriteRuleSet) {
		// ApplicationGatewayRewriteRuleSet declares only id, name, etag and
		// properties; unlike its siblings it carries no `type`.
		c.Type = ""
		c.Properties.ProvisioningState = "Succeeded"
	})
	applicationGatewayStamp(gw.ID, "redirectConfigurations", p.RedirectConfigurations, nil)
	applicationGatewayStamp(gw.ID, "privateLinkConfigurations", p.PrivateLinkConfigurations, func(c *ApplicationGatewayPrivateLinkConfiguration) {
		applicationGatewayStamp(c.ID, "ipConfigurations", c.Properties.IPConfigurations, func(ipcfg *ApplicationGatewayPrivateLinkIPConfiguration) {
			ipcfg.Type = azureAppGatewayType + "/privateLinkConfigurations/ipConfigurations"
			ipcfg.Properties.ProvisioningState = "Succeeded"
		})
		c.Properties.ProvisioningState = "Succeeded"
	})
	applicationGatewayStamp(gw.ID, "loadDistributionPolicies", p.LoadDistributionPolicies, func(c *ApplicationGatewayLoadDistributionPolicy) {
		applicationGatewayStamp(c.ID, "loadDistributionTargets", c.Properties.LoadDistributionTargets, func(t *ApplicationGatewayLoadDistributionTarget) {
			t.Type = azureAppGatewayType + "/loadDistributionPolicies/loadDistributionTargets"
		})
		c.Properties.ProvisioningState = "Succeeded"
	})
	applicationGatewayStamp(gw.ID, "entraJWTValidationConfigs", p.EntraJWTValidationConfigs, func(c *ApplicationGatewayEntraJWTValidationConfig) {
		c.Properties.ProvisioningState = "Succeeded"
	})
}

// provisionApplicationGateway leases a real subnet address for each
// dynamically addressed private frontend.
func provisionApplicationGateway(ctx context.Context, gw *ApplicationGateway, previous *ApplicationGateway) error {
	// Assigned once, at creation: an update of a stopped gateway must not restart
	// it.
	gw.Properties.ResourceGUID = sim.NewUUID()
	gw.Properties.OperationalState = "Running"
	if previous != nil {
		if previous.Properties.ResourceGUID != "" {
			gw.Properties.ResourceGUID = previous.Properties.ResourceGUID
		}
		if previous.Properties.OperationalState != "" {
			gw.Properties.OperationalState = previous.Properties.OperationalState
		}
	}
	gw.Properties.ProvisioningState = "Succeeded"
	gw.Properties.DefaultPredefinedSslPolicy = applicationGatewayDefaultSslPolicy(gw.Properties.Sku)
	if gw.Properties.Sku != nil && gw.Properties.Sku.Tier == "" {
		gw.Properties.Sku.Tier = applicationGatewaySkuTier(gw.Properties.Sku.Name)
	}
	stampApplicationGatewayChildren(gw)

	// Private frontends lease from the subnet's ledger, the one network interfaces
	// use, so a frontend and an interface can never share an address. A static
	// frontend leases its own address; a public frontend takes its public IP's.
	live := map[string]string{}
	for i := range gw.Properties.FrontendIPConfigurations {
		fe := &gw.Properties.FrontendIPConfigurations[i]
		if fe.Properties.PublicIPAddress != nil && fe.Properties.PublicIPAddress.ID != "" {
			// A public frontend still reports an allocation method: the azurerm provider
			// re-plans the gateway when it is absent.
			fe.Properties.PrivateIPAddress = ""
			continue
		}
		if fe.Properties.Subnet == nil || fe.Properties.Subnet.ID == "" {
			continue
		}
		if fe.Properties.PrivateIPAllocationMethod == "" {
			fe.Properties.PrivateIPAllocationMethod = "Dynamic"
		}
		nicID := applicationGatewayFrontendNICID(gw.ID, fe.Name)
		var requested net.IP
		if strings.EqualFold(fe.Properties.PrivateIPAllocationMethod, "Static") {
			requested = net.ParseIP(fe.Properties.PrivateIPAddress)
		} else if previous != nil {
			requested = applicationGatewayStoredFrontendAddress(*previous, fe.Name, fe.Properties.Subnet.ID)
		}
		address, err := azureLeaseSubnetAddress(fe.Properties.Subnet.ID, nicID, requested)
		if err != nil {
			return err
		}
		fe.Properties.PrivateIPAddress = address.String()
		live[nicID] = address.String()
	}
	if previous == nil {
		return nil
	}
	for _, old := range previous.Properties.FrontendIPConfigurations {
		if old.Properties.Subnet == nil || old.Properties.PrivateIPAddress == "" {
			continue
		}
		nicID := applicationGatewayFrontendNICID(previous.ID, old.Name)
		if live[nicID] != old.Properties.PrivateIPAddress {
			azureReleaseSubnetAddress(old.Properties.Subnet.ID, nicID, net.ParseIP(old.Properties.PrivateIPAddress))
		}
	}
	return nil
}

// The address a frontend held in the same subnet, so an update keeps it.
func applicationGatewayStoredFrontendAddress(gw ApplicationGateway, frontendName, subnetID string) net.IP {
	for _, fe := range gw.Properties.FrontendIPConfigurations {
		if fe.Name == frontendName && fe.Properties.Subnet != nil && strings.EqualFold(fe.Properties.Subnet.ID, subnetID) {
			return net.ParseIP(fe.Properties.PrivateIPAddress)
		}
	}
	return nil
}

// Azure does not surface the gateway's own interfaces, only the address they
// hold, so the platform interface is scoped to the frontend configuration.
func applicationGatewayFrontendNICID(gatewayID, frontendName string) string {
	return armChildID(armChildID(gatewayID, "frontendIPConfigurations", frontendName), "networkInterface")
}

func applicationGatewaySkuTier(name string) string {
	switch {
	case name == "":
		return ""
	case strings.HasPrefix(name, "WAF_v2"):
		return "WAF_v2"
	case strings.HasPrefix(name, "WAF"):
		return "WAF"
	case strings.HasSuffix(name, "_v2"):
		return "Standard_v2"
	case name == "Basic":
		return "Basic"
	default:
		return "Standard"
	}
}

// The v2 generation defaults to the 2022 policy, the original one to 2015.
func applicationGatewayDefaultSslPolicy(sku *ApplicationGatewaySku) string {
	tier := ""
	if sku != nil {
		tier = sku.Tier
		if tier == "" {
			tier = applicationGatewaySkuTier(sku.Name)
		}
	}
	switch tier {
	case "Standard_v2", "WAF_v2", "Basic":
		return "AppGwSslPolicy20220101"
	default:
		return "AppGwSslPolicy20150501"
	}
}

// Refresh what the resource provider recomputes on every read: each pool's
// joined interface IP configurations and the live private endpoint
// connections.
func projectApplicationGateway(gw *ApplicationGateway) {
	for i := range gw.Properties.BackendAddressPools {
		pool := &gw.Properties.BackendAddressPools[i]
		pool.Properties.BackendIPConfigurations = applicationGatewayPoolMemberIPConfigurations(pool.ID)
	}
	gw.Properties.PrivateEndpointConnections = applicationGatewayConnections(gw.ID)
}

// A virtual machine joins the backend through its interface, as with a load
// balancer, not through the gateway's configuration.
func applicationGatewayPoolMemberIPConfigurations(poolID string) []NetworkInterfaceIPConfiguration {
	if azureNICs == nil || poolID == "" {
		return nil
	}
	var members []NetworkInterfaceIPConfiguration
	for _, nic := range azureNICsInBackendPool(poolID) {
		for _, ipcfg := range nic.Properties.IPConfigurations {
			for _, ref := range ipcfg.Properties.ApplicationGatewayBackendAddressPools {
				if strings.EqualFold(ref.ID, poolID) {
					members = append(members, ipcfg)
				}
			}
		}
	}
	return members
}

func deleteApplicationGatewayResources(_ context.Context, id string, deleted ApplicationGateway) {
	for _, fe := range deleted.Properties.FrontendIPConfigurations {
		if fe.Properties.Subnet == nil {
			continue
		}
		azureReleaseSubnetAddress(fe.Properties.Subnet.ID, applicationGatewayFrontendNICID(id, fe.Name), net.ParseIP(fe.Properties.PrivateIPAddress))
	}
	for _, conn := range applicationGatewayConnections(id) {
		azureAppGatewayPEConnections.Delete(conn.ID)
	}
}

var azureAppGatewayConnectionsByPrefix sim.GenerationIndex[ApplicationGatewayPrivateEndpointConnection]

func applicationGatewayConnections(gatewayID string) []ApplicationGatewayPrivateEndpointConnection {
	// Copy out of the index: each connection's endpoint reference is rendered
	// below, and the index's rows must not be written through.
	conns := append([]ApplicationGatewayPrivateEndpointConnection(nil),
		azureAppGatewayConnectionsByPrefix.LookupAll(azureAppGatewayPEConnections,
			gatewayID+"/privateEndpointConnections/",
			func(c ApplicationGatewayPrivateEndpointConnection) []string {
				return sim.PathPrefixes(c.ID)
			})...)
	for i := range conns {
		projectApplicationGatewayConnection(&conns[i])
	}
	return conns
}

// Render from the live endpoint record, so a connection never reports an
// endpoint that has changed or been deleted.
func projectApplicationGatewayConnection(conn *ApplicationGatewayPrivateEndpointConnection) {
	if conn.Properties.PrivateEndpoint == nil || conn.Properties.PrivateEndpoint.ID == "" || azurePrivateEndpoints == nil {
		return
	}
	pe, ok := azurePrivateEndpoints.Get(conn.Properties.PrivateEndpoint.ID)
	if !ok {
		return
	}
	projectPrivateEndpoint(&pe)
	conn.Properties.PrivateEndpoint = &pe
}

func registerApplicationGatewayOperations(srv *sim.Server) {
	armBase := azureNetworkArmBase()
	base := armBase + "/applicationGateways/{applicationGatewayName}"

	setState := func(state string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id := applicationGatewayID(r)
			if !azureApplicationGateways.Update(id, func(gw *ApplicationGateway) {
				gw.Properties.OperationalState = state
				gw.Etag = azureNetworkEtag()
			}) {
				azureNetworkResourceNotFound(w, azureAppGatewayType,
					sim.PathParam(r, "applicationGatewayName"), sim.PathParam(r, "resourceGroupName"))
				return
			}
			w.WriteHeader(http.StatusOK)
		}
	}
	// A stopped gateway answers nothing on its frontend; the data plane reads
	// operationalState.
	srv.HandleFunc("POST "+base+"/start", setState("Running"))
	srv.HandleFunc("POST "+base+"/stop", setState("Stopped"))

	srv.HandleFunc("POST "+base+"/backendhealth", func(w http.ResponseWriter, r *http.Request) {
		gw, ok := azureApplicationGateways.Get(applicationGatewayID(r))
		if !ok {
			azureNetworkResourceNotFound(w, azureAppGatewayType,
				sim.PathParam(r, "applicationGatewayName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		projectApplicationGateway(&gw)
		sim.WriteJSON(w, http.StatusOK, applicationGatewayBackendHealth(r.Context(), gw))
	})

	// BackendHealthOnDemand runs the caller's probe instead of the configured
	// ones.
	srv.HandleFunc("POST "+base+"/getBackendHealthOnDemand", func(w http.ResponseWriter, r *http.Request) {
		gw, ok := azureApplicationGateways.Get(applicationGatewayID(r))
		if !ok {
			azureNetworkResourceNotFound(w, azureAppGatewayType,
				sim.PathParam(r, "applicationGatewayName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		var probe ApplicationGatewayOnDemandProbe
		if err := sim.ReadJSON(r, &probe); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		projectApplicationGateway(&gw)
		result, err := applicationGatewayOnDemandHealth(r.Context(), gw, probe)
		if err != nil {
			AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest, "%v", err)
			return
		}
		sim.WriteJSON(w, http.StatusOK, result)
	})
}

type ApplicationGatewayOnDemandProbe struct {
	Protocol                            string                                      `json:"protocol,omitempty"`
	Host                                string                                      `json:"host,omitempty"`
	Path                                string                                      `json:"path,omitempty"`
	Timeout                             int32                                       `json:"timeout,omitempty"`
	PickHostNameFromBackendHTTPSettings bool                                        `json:"pickHostNameFromBackendHttpSettings,omitempty"`
	EnableProbeProxyProtocolHeader      bool                                        `json:"enableProbeProxyProtocolHeader,omitempty"`
	Match                               *ApplicationGatewayProbeHealthResponseMatch `json:"match,omitempty"`
	BackendAddressPool                  *SubResource                                `json:"backendAddressPool,omitempty"`
	BackendHTTPSettings                 *SubResource                                `json:"backendHttpSettings,omitempty"`
}

func registerApplicationGatewayPrivateLink(srv *sim.Server) {
	armBase := azureNetworkArmBase()
	base := armBase + "/applicationGateways/{applicationGatewayName}"

	// One private link resource per private link configuration; its name is the
	// group a consumer asks for.
	srv.HandleFunc("GET "+base+"/privateLinkResources", func(w http.ResponseWriter, r *http.Request) {
		gw, ok := azureApplicationGateways.Get(applicationGatewayID(r))
		if !ok {
			azureNetworkResourceNotFound(w, azureAppGatewayType,
				sim.PathParam(r, "applicationGatewayName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		resources := make([]ApplicationGatewayPrivateLinkResource, 0, len(gw.Properties.PrivateLinkConfigurations))
		for _, plc := range gw.Properties.PrivateLinkConfigurations {
			resources = append(resources, ApplicationGatewayPrivateLinkResource{
				applicationGatewayChild: applicationGatewayChild{
					ID:   armChildID(gw.ID, "privateLinkResources", plc.Name),
					Name: plc.Name,
					Type: azureAppGatewayType + "/privateLinkResources",
					Etag: azureNetworkEtag(),
				},
				Properties: ApplicationGatewayPrivateLinkResourceProperties{
					GroupID:         plc.Name,
					RequiredMembers: applicationGatewayPrivateLinkMembers(gw, plc),
					// The endpoint resolves the gateway's name inside the consumer's virtual
					// network, so it needs the application gateway private zone.
					RequiredZoneNames: []string{"privatelink.azure.com"},
				},
			})
		}
		azureWriteList(w, resources)
	})

	connBase := base + "/privateEndpointConnections"
	connID := func(r *http.Request) string {
		return armChildID(applicationGatewayID(r), "privateEndpointConnections", sim.PathParam(r, "connectionName"))
	}

	srv.HandleFunc("GET "+connBase, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := azureApplicationGateways.Get(applicationGatewayID(r)); !ok {
			azureNetworkResourceNotFound(w, azureAppGatewayType,
				sim.PathParam(r, "applicationGatewayName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		azureWriteList(w, applicationGatewayConnections(applicationGatewayID(r)))
	})

	srv.HandleFunc("GET "+connBase+"/{connectionName}", func(w http.ResponseWriter, r *http.Request) {
		conn, ok := azureAppGatewayPEConnections.Get(connID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Private endpoint connection %q was not found.", sim.PathParam(r, "connectionName"))
			return
		}
		projectApplicationGatewayConnection(&conn)
		sim.WriteJSON(w, http.StatusOK, conn)
	})

	// Only the connection state is writable; the endpoint reference belongs to the
	// consumer side.
	srv.HandleFunc("PUT "+connBase+"/{connectionName}", func(w http.ResponseWriter, r *http.Request) {
		var req ApplicationGatewayPrivateEndpointConnection
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.PrivateLinkServiceConnectionState == nil {
			AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
				"The request format was unexpected: privateLinkServiceConnectionState is required.")
			return
		}
		id := connID(r)
		if !azureAppGatewayPEConnections.Update(id, func(conn *ApplicationGatewayPrivateEndpointConnection) {
			conn.Properties.PrivateLinkServiceConnectionState = req.Properties.PrivateLinkServiceConnectionState
			conn.Etag = azureNetworkEtag()
		}) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Private endpoint connection %q was not found.", sim.PathParam(r, "connectionName"))
			return
		}
		conn, _ := azureAppGatewayPEConnections.Get(id)
		projectApplicationGatewayConnection(&conn)
		sim.WriteJSON(w, http.StatusOK, conn)
	})

	srv.HandleFunc("DELETE "+connBase+"/{connectionName}", func(w http.ResponseWriter, r *http.Request) {
		if !azureAppGatewayPEConnections.Delete(connID(r)) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func applicationGatewayPrivateLinkMembers(gw ApplicationGateway, plc ApplicationGatewayPrivateLinkConfiguration) []string {
	var members []string
	for _, fe := range gw.Properties.FrontendIPConfigurations {
		if fe.Properties.PrivateLinkConfiguration != nil && strings.EqualFold(fe.Properties.PrivateLinkConfiguration.ID, plc.ID) {
			members = append(members, fe.Name)
		}
	}
	return members
}

func applicationGatewayID(r *http.Request) string {
	return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "applicationGateways", sim.PathParam(r, "applicationGatewayName"))
}
