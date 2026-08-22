package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Application gateways (Microsoft.Network/applicationGateways) are Azure's
// layer-7 load balancer. The resource is a routing program: a frontend IP
// configuration and a frontend port define where clients arrive, an HTTP
// listener binds the two (optionally narrowed to a set of host names), a
// request routing rule sends the listener's traffic either straight at a
// backend address pool with a set of backend HTTP settings or through a URL
// path map that picks a different pool per path, and a probe decides which
// members of a pool are healthy enough to receive it.
//
// Everything in that paragraph is executed, not merely stored:
// network_appgateway_dataplane.go binds the gateway's frontend address as an
// HTTP data plane, resolves the listener, evaluates the routing rules and
// redirect configurations, applies the rewrite rule sets, and forwards the
// request to a pool member it has just probed. BackendHealth reports what those
// same probes found, so a gateway that says a server is Up is a gateway that
// just reached it.

// ApplicationGateway mirrors Microsoft.Network/applicationGateways.
type ApplicationGateway struct {
	azureNetworkResourceHeader
	Zones      []string                     `json:"zones,omitempty"`
	Identity   map[string]any               `json:"identity,omitempty"`
	Properties ApplicationGatewayProperties `json:"properties"`
}

// ApplicationGatewayProperties is ApplicationGatewayPropertiesFormat.
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

// ApplicationGatewaySku is the gateway's SKU envelope.
type ApplicationGatewaySku struct {
	Name     string `json:"name,omitempty"`
	Tier     string `json:"tier,omitempty"`
	Capacity int32  `json:"capacity,omitempty"`
	Family   string `json:"family,omitempty"`
}

// ApplicationGatewaySslPolicy is the TLS policy of a gateway or SSL profile.
type ApplicationGatewaySslPolicy struct {
	DisabledSslProtocols []string `json:"disabledSslProtocols,omitempty"`
	PolicyType           string   `json:"policyType,omitempty"`
	PolicyName           string   `json:"policyName,omitempty"`
	CipherSuites         []string `json:"cipherSuites,omitempty"`
	MinProtocolVersion   string   `json:"minProtocolVersion,omitempty"`
}

// applicationGatewayChild is the identity envelope every child collection
// member of an application gateway carries.
type applicationGatewayChild struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
	Etag string `json:"etag,omitempty"`
}

// child returns the envelope itself. Every collection member embeds
// applicationGatewayChild, so each of them satisfies
// applicationGatewayChildRef through method promotion and the shared stamping
// below reaches its identity without a per-type accessor.
func (c *applicationGatewayChild) child() *applicationGatewayChild { return c }

// applicationGatewayChildRef is anything carrying an application gateway child
// identity envelope.
type applicationGatewayChildRef interface {
	child() *applicationGatewayChild
}

// ApplicationGatewayIPConfiguration is one gatewayIPConfigurations member — the
// subnet the gateway's instances are deployed into.
type ApplicationGatewayIPConfiguration struct {
	applicationGatewayChild
	Properties ApplicationGatewayIPConfigurationProperties `json:"properties"`
}

// ApplicationGatewayIPConfigurationProperties holds the deployment subnet.
type ApplicationGatewayIPConfigurationProperties struct {
	Subnet            *SubResource `json:"subnet,omitempty"`
	ProvisioningState string       `json:"provisioningState,omitempty"`
}

// ApplicationGatewayFrontendIPConfiguration is one frontend address.
type ApplicationGatewayFrontendIPConfiguration struct {
	applicationGatewayChild
	Properties ApplicationGatewayFrontendIPConfigurationProperties `json:"properties"`
}

// ApplicationGatewayFrontendIPConfigurationProperties holds either a public IP
// reference or a private address taken from a subnet.
type ApplicationGatewayFrontendIPConfigurationProperties struct {
	PrivateIPAddress          string       `json:"privateIPAddress,omitempty"`
	PrivateIPAllocationMethod string       `json:"privateIPAllocationMethod,omitempty"`
	Subnet                    *SubResource `json:"subnet,omitempty"`
	PublicIPAddress           *SubResource `json:"publicIPAddress,omitempty"`
	PrivateLinkConfiguration  *SubResource `json:"privateLinkConfiguration,omitempty"`
	ProvisioningState         string       `json:"provisioningState,omitempty"`
}

// ApplicationGatewayFrontendPort is one frontend port.
type ApplicationGatewayFrontendPort struct {
	applicationGatewayChild
	Properties ApplicationGatewayFrontendPortProperties `json:"properties"`
}

// ApplicationGatewayFrontendPortProperties holds the listening port.
type ApplicationGatewayFrontendPortProperties struct {
	Port              int32  `json:"port,omitempty"`
	ProvisioningState string `json:"provisioningState,omitempty"`
}

// ApplicationGatewayBackendAddressPool is one pool of backend servers.
type ApplicationGatewayBackendAddressPool struct {
	applicationGatewayChild
	Properties ApplicationGatewayBackendAddressPoolProperties `json:"properties"`
}

// ApplicationGatewayBackendAddressPoolProperties holds the pool's declared
// addresses and the interface IP configurations that joined it.
type ApplicationGatewayBackendAddressPoolProperties struct {
	BackendIPConfigurations []NetworkInterfaceIPConfiguration  `json:"backendIPConfigurations,omitempty"`
	BackendAddresses        []ApplicationGatewayBackendAddress `json:"backendAddresses,omitempty"`
	ProvisioningState       string                             `json:"provisioningState,omitempty"`
}

// ApplicationGatewayBackendAddress is one backend server address.
type ApplicationGatewayBackendAddress struct {
	Fqdn      string `json:"fqdn,omitempty"`
	IPAddress string `json:"ipAddress,omitempty"`
}

// ApplicationGatewayBackendHTTPSettings is one backendHttpSettingsCollection
// member — how the gateway talks to a pool.
type ApplicationGatewayBackendHTTPSettings struct {
	applicationGatewayChild
	Properties ApplicationGatewayBackendHTTPSettingsProperties `json:"properties"`
}

// ApplicationGatewayBackendHTTPSettingsProperties holds the backend protocol,
// port, host-header handling, path override and probe reference.
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

// ApplicationGatewayConnectionDraining is the connection-draining policy of a
// backend settings member.
type ApplicationGatewayConnectionDraining struct {
	Enabled           bool  `json:"enabled"`
	DrainTimeoutInSec int32 `json:"drainTimeoutInSec"`
}

// ApplicationGatewayBackendSettings is one backendSettingsCollection member —
// the layer-4 counterpart of backend HTTP settings.
type ApplicationGatewayBackendSettings struct {
	applicationGatewayChild
	Properties ApplicationGatewayBackendSettingsProperties `json:"properties"`
}

// ApplicationGatewayBackendSettingsProperties holds the layer-4 backend
// protocol, port and probe reference.
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

// ApplicationGatewayHTTPListener is one httpListeners member.
type ApplicationGatewayHTTPListener struct {
	applicationGatewayChild
	Properties ApplicationGatewayHTTPListenerProperties `json:"properties"`
}

// ApplicationGatewayHTTPListenerProperties binds a frontend address and port,
// optionally narrowed to a set of host names.
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

// ApplicationGatewayListener is one listeners member — the layer-4 counterpart
// of an HTTP listener.
type ApplicationGatewayListener struct {
	applicationGatewayChild
	Properties ApplicationGatewayListenerProperties `json:"properties"`
}

// ApplicationGatewayListenerProperties binds a frontend address and port for a
// layer-4 routing rule.
type ApplicationGatewayListenerProperties struct {
	FrontendIPConfiguration *SubResource `json:"frontendIPConfiguration,omitempty"`
	FrontendPort            *SubResource `json:"frontendPort,omitempty"`
	Protocol                string       `json:"protocol,omitempty"`
	SslCertificate          *SubResource `json:"sslCertificate,omitempty"`
	SslProfile              *SubResource `json:"sslProfile,omitempty"`
	ProvisioningState       string       `json:"provisioningState,omitempty"`
	HostNames               []string     `json:"hostNames,omitempty"`
}

// ApplicationGatewayRequestRoutingRule is one requestRoutingRules member.
type ApplicationGatewayRequestRoutingRule struct {
	applicationGatewayChild
	Properties ApplicationGatewayRequestRoutingRuleProperties `json:"properties"`
}

// ApplicationGatewayRequestRoutingRuleProperties routes a listener's traffic to
// a pool, a URL path map or a redirect configuration.
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

// ApplicationGatewayRoutingRule is one routingRules member — the layer-4
// counterpart of a request routing rule.
type ApplicationGatewayRoutingRule struct {
	applicationGatewayChild
	Properties ApplicationGatewayRoutingRuleProperties `json:"properties"`
}

// ApplicationGatewayRoutingRuleProperties routes a layer-4 listener's traffic.
type ApplicationGatewayRoutingRuleProperties struct {
	RuleType           string       `json:"ruleType,omitempty"`
	Priority           int32        `json:"priority,omitempty"`
	BackendAddressPool *SubResource `json:"backendAddressPool,omitempty"`
	BackendSettings    *SubResource `json:"backendSettings,omitempty"`
	Listener           *SubResource `json:"listener,omitempty"`
	ProvisioningState  string       `json:"provisioningState,omitempty"`
}

// ApplicationGatewayURLPathMap is one urlPathMaps member — path-based routing.
type ApplicationGatewayURLPathMap struct {
	applicationGatewayChild
	Properties ApplicationGatewayURLPathMapProperties `json:"properties"`
}

// ApplicationGatewayURLPathMapProperties holds the path rules and the defaults
// used when no path rule matches.
type ApplicationGatewayURLPathMapProperties struct {
	DefaultBackendAddressPool     *SubResource                 `json:"defaultBackendAddressPool,omitempty"`
	DefaultBackendHTTPSettings    *SubResource                 `json:"defaultBackendHttpSettings,omitempty"`
	DefaultRewriteRuleSet         *SubResource                 `json:"defaultRewriteRuleSet,omitempty"`
	DefaultRedirectConfiguration  *SubResource                 `json:"defaultRedirectConfiguration,omitempty"`
	DefaultLoadDistributionPolicy *SubResource                 `json:"defaultLoadDistributionPolicy,omitempty"`
	PathRules                     []ApplicationGatewayPathRule `json:"pathRules,omitempty"`
	ProvisioningState             string                       `json:"provisioningState,omitempty"`
}

// ApplicationGatewayPathRule is one member of a URL path map.
type ApplicationGatewayPathRule struct {
	applicationGatewayChild
	Properties ApplicationGatewayPathRuleProperties `json:"properties"`
}

// ApplicationGatewayPathRuleProperties holds the path patterns the rule matches
// and where matching requests go.
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

// ApplicationGatewayProbe is one probes member — the health check a backend
// settings member applies to its pool.
type ApplicationGatewayProbe struct {
	applicationGatewayChild
	Properties ApplicationGatewayProbeProperties `json:"properties"`
}

// ApplicationGatewayProbeProperties holds the probe request and the response
// criterion that classifies a server as healthy.
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

// ApplicationGatewayProbeHealthResponseMatch is the criterion that classifies a
// probe response as healthy.
type ApplicationGatewayProbeHealthResponseMatch struct {
	Body        string   `json:"body,omitempty"`
	StatusCodes []string `json:"statusCodes,omitempty"`
}

// ApplicationGatewayRedirectConfiguration is one redirectConfigurations member.
type ApplicationGatewayRedirectConfiguration struct {
	applicationGatewayChild
	Properties ApplicationGatewayRedirectConfigurationProperties `json:"properties"`
}

// ApplicationGatewayRedirectConfigurationProperties holds the redirect target
// and how the incoming path and query are carried over.
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

// ApplicationGatewayRewriteRuleSet is one rewriteRuleSets member.
type ApplicationGatewayRewriteRuleSet struct {
	applicationGatewayChild
	Properties ApplicationGatewayRewriteRuleSetProperties `json:"properties"`
}

// ApplicationGatewayRewriteRuleSetProperties holds the ordered rewrite rules.
type ApplicationGatewayRewriteRuleSetProperties struct {
	RewriteRules      []ApplicationGatewayRewriteRule `json:"rewriteRules,omitempty"`
	ProvisioningState string                          `json:"provisioningState,omitempty"`
}

// ApplicationGatewayRewriteRule is one rewrite rule: a set of conditions and the
// header and URL changes applied when they all hold.
type ApplicationGatewayRewriteRule struct {
	Name         string                                   `json:"name,omitempty"`
	RuleSequence int32                                    `json:"ruleSequence,omitempty"`
	Conditions   []ApplicationGatewayRewriteRuleCondition `json:"conditions,omitempty"`
	ActionSet    *ApplicationGatewayRewriteRuleActionSet  `json:"actionSet,omitempty"`
}

// ApplicationGatewayRewriteRuleCondition is one condition of a rewrite rule.
type ApplicationGatewayRewriteRuleCondition struct {
	Variable   string `json:"variable,omitempty"`
	Pattern    string `json:"pattern,omitempty"`
	IgnoreCase bool   `json:"ignoreCase,omitempty"`
	Negate     bool   `json:"negate,omitempty"`
}

// ApplicationGatewayRewriteRuleActionSet holds the header and URL rewrites a
// rule applies.
type ApplicationGatewayRewriteRuleActionSet struct {
	RequestHeaderConfigurations  []ApplicationGatewayHeaderConfiguration `json:"requestHeaderConfigurations,omitempty"`
	ResponseHeaderConfigurations []ApplicationGatewayHeaderConfiguration `json:"responseHeaderConfigurations,omitempty"`
	URLConfiguration             *ApplicationGatewayURLConfiguration     `json:"urlConfiguration,omitempty"`
}

// ApplicationGatewayHeaderConfiguration sets or deletes one header. An empty
// header value deletes the header, exactly as the real rewrite engine does.
type ApplicationGatewayHeaderConfiguration struct {
	HeaderName         string                                `json:"headerName,omitempty"`
	HeaderValue        string                                `json:"headerValue,omitempty"`
	HeaderValueMatcher *ApplicationGatewayHeaderValueMatcher `json:"headerValueMatcher,omitempty"`
}

// ApplicationGatewayHeaderValueMatcher narrows a header rewrite to values
// matching a pattern.
type ApplicationGatewayHeaderValueMatcher struct {
	Pattern    string `json:"pattern,omitempty"`
	IgnoreCase bool   `json:"ignoreCase,omitempty"`
	Negate     bool   `json:"negate,omitempty"`
}

// ApplicationGatewayURLConfiguration rewrites the request path and query.
type ApplicationGatewayURLConfiguration struct {
	ModifiedPath        string `json:"modifiedPath,omitempty"`
	ModifiedQueryString string `json:"modifiedQueryString,omitempty"`
	Reroute             bool   `json:"reroute,omitempty"`
}

// ApplicationGatewayCertificate is the shared shape of the four certificate
// collections: authentication, trusted-root, trusted-client and SSL. Each
// collection keeps only the members the specification declares for it, so the
// read-only fields stay empty on the collections that have none.
type ApplicationGatewayCertificate struct {
	applicationGatewayChild
	Properties ApplicationGatewayCertificateProperties `json:"properties"`
}

// ApplicationGatewayCertificateProperties holds certificate material, either
// inline or as a Key Vault secret reference.
type ApplicationGatewayCertificateProperties struct {
	Data               string `json:"data,omitempty"`
	Password           string `json:"password,omitempty"`
	PublicCertData     string `json:"publicCertData,omitempty"`
	KeyVaultSecretID   string `json:"keyVaultSecretId,omitempty"`
	ValidatedCertData  string `json:"validatedCertData,omitempty"`
	ClientCertIssuerDN string `json:"clientCertIssuerDN,omitempty"`
	ProvisioningState  string `json:"provisioningState,omitempty"`
}

// ApplicationGatewaySslProfile is one sslProfiles member.
type ApplicationGatewaySslProfile struct {
	applicationGatewayChild
	Properties ApplicationGatewaySslProfileProperties `json:"properties"`
}

// ApplicationGatewaySslProfileProperties holds a per-listener TLS policy and
// client-authentication configuration.
type ApplicationGatewaySslProfileProperties struct {
	TrustedClientCertificates []SubResource                              `json:"trustedClientCertificates,omitempty"`
	SslPolicy                 *ApplicationGatewaySslPolicy               `json:"sslPolicy,omitempty"`
	ClientAuthConfiguration   *ApplicationGatewayClientAuthConfiguration `json:"clientAuthConfiguration,omitempty"`
	ProvisioningState         string                                     `json:"provisioningState,omitempty"`
}

// ApplicationGatewayClientAuthConfiguration is the mutual-TLS configuration of
// an SSL profile.
type ApplicationGatewayClientAuthConfiguration struct {
	VerifyClientCertIssuerDN bool   `json:"verifyClientCertIssuerDN,omitempty"`
	VerifyClientRevocation   string `json:"verifyClientRevocation,omitempty"`
	VerifyClientAuthMode     string `json:"verifyClientAuthMode,omitempty"`
}

// ApplicationGatewayPrivateLinkConfiguration is one privateLinkConfigurations
// member — the private-link projection of a frontend IP configuration.
type ApplicationGatewayPrivateLinkConfiguration struct {
	applicationGatewayChild
	Properties ApplicationGatewayPrivateLinkConfigurationProperties `json:"properties"`
}

// ApplicationGatewayPrivateLinkConfigurationProperties holds the private-link
// IP configurations.
type ApplicationGatewayPrivateLinkConfigurationProperties struct {
	IPConfigurations  []ApplicationGatewayPrivateLinkIPConfiguration `json:"ipConfigurations,omitempty"`
	ProvisioningState string                                         `json:"provisioningState,omitempty"`
}

// ApplicationGatewayPrivateLinkIPConfiguration is one private-link address.
type ApplicationGatewayPrivateLinkIPConfiguration struct {
	applicationGatewayChild
	Properties ApplicationGatewayPrivateLinkIPConfigurationProperties `json:"properties"`
}

// ApplicationGatewayPrivateLinkIPConfigurationProperties holds one private-link
// address taken from a subnet.
type ApplicationGatewayPrivateLinkIPConfigurationProperties struct {
	PrivateIPAddress          string       `json:"privateIPAddress,omitempty"`
	PrivateIPAllocationMethod string       `json:"privateIPAllocationMethod,omitempty"`
	Subnet                    *SubResource `json:"subnet,omitempty"`
	Primary                   bool         `json:"primary,omitempty"`
	ProvisioningState         string       `json:"provisioningState,omitempty"`
}

// ApplicationGatewayLoadDistributionPolicy is one loadDistributionPolicies
// member — how traffic is spread across several pools.
type ApplicationGatewayLoadDistributionPolicy struct {
	applicationGatewayChild
	Properties ApplicationGatewayLoadDistributionPolicyProperties `json:"properties"`
}

// ApplicationGatewayLoadDistributionPolicyProperties holds the weighted pools
// and the distribution algorithm.
type ApplicationGatewayLoadDistributionPolicyProperties struct {
	LoadDistributionTargets   []ApplicationGatewayLoadDistributionTarget `json:"loadDistributionTargets,omitempty"`
	LoadDistributionAlgorithm string                                     `json:"loadDistributionAlgorithm,omitempty"`
	ProvisioningState         string                                     `json:"provisioningState,omitempty"`
}

// ApplicationGatewayLoadDistributionTarget is one weighted pool of a load
// distribution policy.
type ApplicationGatewayLoadDistributionTarget struct {
	applicationGatewayChild
	Properties ApplicationGatewayLoadDistributionTargetProperties `json:"properties"`
}

// ApplicationGatewayLoadDistributionTargetProperties holds one pool weight.
type ApplicationGatewayLoadDistributionTargetProperties struct {
	WeightPerServer    int32        `json:"weightPerServer,omitempty"`
	BackendAddressPool *SubResource `json:"backendAddressPool,omitempty"`
}

// ApplicationGatewayEntraJWTValidationConfig is one entraJWTValidationConfigs
// member — Microsoft Entra JSON Web Token validation for a routing rule.
type ApplicationGatewayEntraJWTValidationConfig struct {
	applicationGatewayChild
	Properties ApplicationGatewayEntraJWTValidationConfigProperties `json:"properties"`
}

// ApplicationGatewayEntraJWTValidationConfigProperties holds the tenant, client
// and audiences a token must carry.
type ApplicationGatewayEntraJWTValidationConfigProperties struct {
	UnAuthorizedRequestAction string   `json:"unAuthorizedRequestAction,omitempty"`
	TenantID                  string   `json:"tenantId,omitempty"`
	ClientID                  string   `json:"clientId,omitempty"`
	Audiences                 []string `json:"audiences,omitempty"`
	ProvisioningState         string   `json:"provisioningState,omitempty"`
}

// ApplicationGatewayCustomError maps a status code to a custom error page.
type ApplicationGatewayCustomError struct {
	StatusCode         string `json:"statusCode,omitempty"`
	CustomErrorPageURL string `json:"customErrorPageUrl,omitempty"`
}

// ApplicationGatewayAutoscaleConfiguration is the gateway's instance-count
// range.
type ApplicationGatewayAutoscaleConfiguration struct {
	MinCapacity int32 `json:"minCapacity"`
	MaxCapacity int32 `json:"maxCapacity,omitempty"`
}

// ApplicationGatewayGlobalConfiguration holds the gateway-wide buffering
// switches.
type ApplicationGatewayGlobalConfiguration struct {
	EnableRequestBuffering  bool `json:"enableRequestBuffering,omitempty"`
	EnableResponseBuffering bool `json:"enableResponseBuffering,omitempty"`
}

// ApplicationGatewayWebApplicationFirewallConfiguration is the gateway's
// built-in web application firewall configuration.
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

// ApplicationGatewayPrivateEndpointConnection is one private endpoint
// connection on an application gateway.
type ApplicationGatewayPrivateEndpointConnection struct {
	applicationGatewayChild
	Properties ApplicationGatewayPrivateEndpointConnectionProperties `json:"properties"`
}

// ApplicationGatewayPrivateEndpointConnectionProperties holds the connection's
// approval state. The private endpoint itself is stored as a bare reference and
// rendered from the live endpoint record on read.
type ApplicationGatewayPrivateEndpointConnectionProperties struct {
	PrivateEndpoint                   *PrivateEndpoint                   `json:"privateEndpoint,omitempty"`
	PrivateLinkServiceConnectionState *PrivateLinkServiceConnectionState `json:"privateLinkServiceConnectionState,omitempty"`
	ProvisioningState                 string                             `json:"provisioningState,omitempty"`
	LinkIdentifier                    string                             `json:"linkIdentifier,omitempty"`
}

// ApplicationGatewayPrivateLinkResource is one privateLinkResources member —
// the group a consumer's private endpoint asks for when it connects.
type ApplicationGatewayPrivateLinkResource struct {
	applicationGatewayChild
	Properties ApplicationGatewayPrivateLinkResourceProperties `json:"properties"`
}

// ApplicationGatewayPrivateLinkResourceProperties holds the group identifier
// and its required members.
type ApplicationGatewayPrivateLinkResourceProperties struct {
	GroupID           string   `json:"groupId,omitempty"`
	RequiredMembers   []string `json:"requiredMembers,omitempty"`
	RequiredZoneNames []string `json:"requiredZoneNames,omitempty"`
}

var (
	azureApplicationGateways sim.Store[ApplicationGateway]
	// azureAppGatewayPEConnections holds every private endpoint connection on an
	// application gateway, keyed by the connection's ARM id, so the gateway's own
	// collection and the connection surface read one object.
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

	// An application gateway terminates private endpoints like every other
	// private-link target: the consumer's endpoint opens the connection in the
	// gateway's own collection, so the endpoint's view and the gateway owner's
	// view are one object. The groups a gateway publishes are the names of its
	// own private link configurations rather than a fixed set, which is why the
	// entry declares none.
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
}

// validateApplicationGateway applies the request validation the resource
// provider applies before it provisions anything: every child collection member
// must be named, and every reference to a resource outside the gateway — the
// deployment subnet, a frontend's subnet or public IP address — must resolve.
func validateApplicationGateway(w http.ResponseWriter, _ *http.Request, gw *ApplicationGateway) bool {
	named := func(name, collection string) bool {
		if name != "" {
			return true
		}
		sim.AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
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
				sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

// applicationGatewayNames collects the names of a child collection through the
// identity envelope every member embeds.
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

// applicationGatewayStamp gives every member of one child collection the
// identity the resource provider assigns it — the ARM id composed from the
// gateway and the collection, the child resource type, a fresh etag — and then
// lets the collection apply its own defaults and provisioning state.
func applicationGatewayStamp[T any, PT interface {
	*T
	applicationGatewayChildRef
}](gatewayID, collection string, items []T, ready func(PT)) {
	for i := range items {
		item := PT(&items[i])
		head := item.child()
		head.ID = azureNetworkChildID(gatewayID, collection, head.Name)
		head.Type = azureAppGatewayType + "/" + collection
		head.Etag = azureNetworkEtag()
		if ready != nil {
			ready(item)
		}
	}
}

// stampApplicationGatewayChildren assigns identity, defaults and provisioning
// state across every child collection of the gateway.
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
		// The service reports an allocation method on every frontend, public
		// or private; a frontend that named no static address is dynamic.
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
		// A path rule is addressed under the map that owns it, so its identity
		// is composed from the map's id rather than the gateway's.
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
		// properties — unlike its sibling collections it carries no `type`.
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

// provisionApplicationGateway stamps every child collection member's identity,
// assigns the read-only state the resource provider computes, and takes a real
// address out of the subnet for each dynamically-addressed private frontend.
func provisionApplicationGateway(ctx context.Context, gw *ApplicationGateway, previous *ApplicationGateway) error {
	// The resource GUID and the operational state are assigned once, at
	// creation: an update of a stopped gateway must not silently restart it.
	gw.Properties.ResourceGUID = generateUUID()
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

	// Every private frontend whose address is allocated by the platform takes
	// one out of its subnet, through the same fabric a network interface uses,
	// so a gateway frontend and a workload interface can never be handed the
	// same address. A frontend that names its own static address keeps it, and a
	// public frontend takes the address of the public IP it references.
	live := map[string]bool{}
	for i := range gw.Properties.FrontendIPConfigurations {
		fe := &gw.Properties.FrontendIPConfigurations[i]
		if fe.Properties.PublicIPAddress != nil && fe.Properties.PublicIPAddress.ID != "" {
			// A public frontend holds no private address, but it still reports
			// the allocation method every frontend carries — the HashiCorp
			// provider reads it back on refresh and re-plans the gateway when
			// it is absent.
			fe.Properties.PrivateIPAddress = ""
			continue
		}
		if fe.Properties.Subnet == nil || fe.Properties.Subnet.ID == "" {
			continue
		}
		if fe.Properties.PrivateIPAllocationMethod == "" {
			fe.Properties.PrivateIPAllocationMethod = "Dynamic"
		}
		if strings.EqualFold(fe.Properties.PrivateIPAllocationMethod, "Static") && fe.Properties.PrivateIPAddress != "" {
			continue
		}
		nicID := applicationGatewayFrontendNICID(gw.ID, fe.Name)
		address, _, err := azureCreateRealNIC(ctx, nicID, fe.Properties.Subnet.ID, "", azureNICMAC(nicID))
		if err != nil {
			return err
		}
		fe.Properties.PrivateIPAddress = address
		live[nicID] = true
	}
	if previous == nil {
		return nil
	}
	for _, old := range previous.Properties.FrontendIPConfigurations {
		nicID := applicationGatewayFrontendNICID(previous.ID, old.Name)
		if !live[nicID] {
			if err := azureDeleteRealNIC(ctx, nicID); err != nil {
				return err
			}
		}
	}
	return nil
}

// applicationGatewayFrontendNICID names the platform-owned interface that holds
// a private frontend's address. It is scoped to the frontend configuration
// rather than exposed as a Microsoft.Network/networkInterfaces resource,
// because Azure does not surface the gateway's own interfaces — only the
// address they hold, on the frontend configuration itself.
func applicationGatewayFrontendNICID(gatewayID, frontendName string) string {
	return azureNetworkChildID(gatewayID, "frontendIPConfigurations", frontendName) + "/networkInterface"
}

// applicationGatewaySkuTier is the tier a SKU name implies when the request
// leaves it out.
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

// applicationGatewayDefaultSslPolicy reports the predefined TLS policy the
// gateway applies when its SSL policy leaves one unset. The v2 generation
// defaults to the 2022 policy; the original generation to the 2015 one.
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

// projectApplicationGateway refreshes the read-only properties the resource
// provider recomputes on every read: the interface IP configurations that
// joined each backend pool, and the live private endpoint connections.
func projectApplicationGateway(gw *ApplicationGateway) {
	for i := range gw.Properties.BackendAddressPools {
		pool := &gw.Properties.BackendAddressPools[i]
		pool.Properties.BackendIPConfigurations = applicationGatewayPoolMemberIPConfigurations(pool.ID)
	}
	gw.Properties.PrivateEndpointConnections = applicationGatewayConnections(gw.ID)
}

// applicationGatewayPoolMemberIPConfigurations lists the network interface IP
// configurations that declared membership in the pool. A virtual machine joins
// an application gateway's backend the same way it joins a load balancer's:
// through its interface, not through the gateway's own configuration.
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

// deleteApplicationGatewayResources releases the addresses the gateway's
// private frontends held and drops the private endpoint connections it owned.
func deleteApplicationGatewayResources(ctx context.Context, id string, deleted ApplicationGateway) {
	for _, fe := range deleted.Properties.FrontendIPConfigurations {
		_ = azureDeleteRealNIC(ctx, applicationGatewayFrontendNICID(id, fe.Name))
	}
	for _, conn := range applicationGatewayConnections(id) {
		azureAppGatewayPEConnections.Delete(conn.ID)
	}
}

var azureAppGatewayConnectionsByPrefix sim.GenerationIndex[ApplicationGatewayPrivateEndpointConnection]

func applicationGatewayConnections(gatewayID string) []ApplicationGatewayPrivateEndpointConnection {
	// Copied out of the index: each connection's private-endpoint reference is
	// rendered from the live endpoint record below, and the index's rows must
	// not be written through.
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

// projectApplicationGatewayConnection renders the stored connection's private
// endpoint reference from the live endpoint record, so a connection can never
// report an endpoint that has since changed or been deleted.
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

// registerApplicationGatewayOperations mounts the lifecycle and diagnostic
// operations: starting and stopping the gateway's data plane, and the two
// backend-health reads.
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
	// A stopped gateway answers nothing on its frontend; a started one resumes
	// serving, which is what the data plane reads back from operationalState.
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

	// BackendHealthOnDemand runs one probe the caller describes, against the
	// pool and backend settings it names, instead of the probes the gateway is
	// configured with.
	srv.HandleFunc("POST "+base+"/getBackendHealthOnDemand", func(w http.ResponseWriter, r *http.Request) {
		gw, ok := azureApplicationGateways.Get(applicationGatewayID(r))
		if !ok {
			azureNetworkResourceNotFound(w, azureAppGatewayType,
				sim.PathParam(r, "applicationGatewayName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		var probe ApplicationGatewayOnDemandProbe
		if err := sim.ReadJSON(r, &probe); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		projectApplicationGateway(&gw)
		result, err := applicationGatewayOnDemandHealth(r.Context(), gw, probe)
		if err != nil {
			sim.AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest, "%v", err)
			return
		}
		sim.WriteJSON(w, http.StatusOK, result)
	})
}

// ApplicationGatewayOnDemandProbe is the body of a backend-health-on-demand
// request: a probe description plus the pool and backend settings to run it
// against.
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

// registerApplicationGatewayPrivateLink mounts the gateway's private-link
// surface: the group a consumer connects to, and the connections consumers
// opened.
func registerApplicationGatewayPrivateLink(srv *sim.Server) {
	armBase := azureNetworkArmBase()
	base := armBase + "/applicationGateways/{applicationGatewayName}"

	// A gateway offers one private link resource per private link configuration,
	// and the group a consumer's private endpoint asks for is that
	// configuration's name.
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
					ID:   azureNetworkChildID(gw.ID, "privateLinkResources", plc.Name),
					Name: plc.Name,
					Type: azureAppGatewayType + "/privateLinkResources",
					Etag: azureNetworkEtag(),
				},
				Properties: ApplicationGatewayPrivateLinkResourceProperties{
					GroupID:         plc.Name,
					RequiredMembers: applicationGatewayPrivateLinkMembers(gw, plc),
					// A private endpoint onto an application gateway resolves the
					// gateway's own name inside the consumer's virtual network, so
					// the zone it needs is the application gateway private zone.
					RequiredZoneNames: []string{"privatelink.azure.com"},
				},
			})
		}
		azureWriteList(w, resources)
	})

	connBase := base + "/privateEndpointConnections"
	connID := func(r *http.Request) string {
		return azureNetworkChildID(applicationGatewayID(r), "privateEndpointConnections", sim.PathParam(r, "connectionName"))
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Private endpoint connection %q was not found.", sim.PathParam(r, "connectionName"))
			return
		}
		projectApplicationGatewayConnection(&conn)
		sim.WriteJSON(w, http.StatusOK, conn)
	})

	// The gateway's owner approves or rejects a pending connection here; only
	// the connection state is writable, the endpoint reference belongs to the
	// consumer side.
	srv.HandleFunc("PUT "+connBase+"/{connectionName}", func(w http.ResponseWriter, r *http.Request) {
		var req ApplicationGatewayPrivateEndpointConnection
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.PrivateLinkServiceConnectionState == nil {
			sim.AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
				"The request format was unexpected: privateLinkServiceConnectionState is required.")
			return
		}
		id := connID(r)
		if !azureAppGatewayPEConnections.Update(id, func(conn *ApplicationGatewayPrivateEndpointConnection) {
			conn.Properties.PrivateLinkServiceConnectionState = req.Properties.PrivateLinkServiceConnectionState
			conn.Etag = azureNetworkEtag()
		}) {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

// applicationGatewayPrivateLinkMembers lists the frontend IP configurations a
// private link configuration projects — the members a consumer's private
// endpoint connects to.
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
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/applicationGateways/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "applicationGatewayName"))
}
