package main

import (
	"net/http"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// The subscription-scoped application gateway catalogs. These operations
// describe the gateway product rather than any one deployment: the server
// variables and headers a rewrite rule set may name, and the TLS options a
// gateway's SSL policy may be built from. The values are Microsoft.Network's
// own published catalog for the api-version this simulator serves — the same
// answer every subscription gets — and the two TLS lists are exactly the closed
// enumerations the specification declares, so a client that validates a policy
// against them validates it against the specification.

// applicationGatewayServerVariableNames are the server variables an application
// gateway exposes to a rewrite rule's conditions and actions.
var applicationGatewayServerVariableNames = []string{
	"add_x_forwarded_for_proxy",
	"ciphers_supported",
	"ciphers_used",
	"client_ip",
	"client_port",
	"client_tcp_rtt",
	"client_user",
	"host",
	"http_method",
	"http_status",
	"http_version",
	"query_string",
	"received_bytes",
	"request_query",
	"request_scheme",
	"request_uri",
	"sent_bytes",
	"server_port",
	"ssl_cipher",
	"ssl_client_certificate",
	"ssl_client_escaped_cert",
	"ssl_client_verify",
	"ssl_connection_protocol",
	"ssl_enabled",
	"ssl_protocol",
	"ssl_server_name",
	"uri_path",
}

// applicationGatewayRequestHeaderNames are the request headers a rewrite rule
// set may act on.
var applicationGatewayRequestHeaderNames = []string{
	"Accept", "Accept-Charset", "Accept-Encoding", "Accept-Language", "Accept-Datetime",
	"Access-Control-Request-Method", "Access-Control-Request-Headers", "Age", "Authorization",
	"Cache-Control", "Connection", "Content-Length", "Content-MD5", "Content-Type", "Cookie",
	"Date", "Expect", "Forwarded", "From", "Host", "HTTP2-Settings", "If-Match",
	"If-Modified-Since", "If-None-Match", "If-Range", "If-Unmodified-Since", "Max-Forwards",
	"Origin", "Pragma", "Proxy-Authorization", "Range", "Referer", "Server", "TE",
	"Transfer-Encoding", "Upgrade", "User-Agent", "Via", "Warning", "X-ARR-ClientCert",
	"X-ARR-LOG-ID", "X-Client-Ip", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto",
	"X-Original-Host", "X-Original-URL", "X-Original-User-Agent", "X-Requested-With",
	"X-WAWS-Unencoded-URL",
}

// applicationGatewayResponseHeaderNames are the response headers a rewrite rule
// set may act on.
var applicationGatewayResponseHeaderNames = []string{
	"Access-Control-Allow-Origin", "Accept-Ranges", "Age", "Allow", "Cache-Control",
	"Connection", "Content-Disposition", "Content-Encoding", "Content-Language",
	"Content-Length", "Content-Location", "Content-MD5", "Content-Range", "Content-Type",
	"Date", "ETag", "Expires", "Last-Modified", "Link", "Location", "P3P", "Pragma",
	"Proxy-Authenticate", "Refresh", "Retry-After", "Server", "Set-Cookie", "Status",
	"Strict-Transport-Security", "Trailer", "Transfer-Encoding", "Upgrade", "Vary", "Via",
	"Warning", "WWW-Authenticate", "X-AspNet-Version", "X-Powered-By",
}

// applicationGatewaySslProtocols is the ProtocolsEnum of the specification, in
// the order the specification declares it.
var applicationGatewaySslProtocols = []string{"TLSv1_0", "TLSv1_1", "TLSv1_2", "TLSv1_3"}

// applicationGatewaySslCipherSuites is the CipherSuitesEnum of the
// specification, in the order the specification declares it.
var applicationGatewaySslCipherSuites = []string{
	"TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA384", "TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256",
	"TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA", "TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA",
	"TLS_DHE_RSA_WITH_AES_256_GCM_SHA384", "TLS_DHE_RSA_WITH_AES_128_GCM_SHA256",
	"TLS_DHE_RSA_WITH_AES_256_CBC_SHA", "TLS_DHE_RSA_WITH_AES_128_CBC_SHA",
	"TLS_RSA_WITH_AES_256_GCM_SHA384", "TLS_RSA_WITH_AES_128_GCM_SHA256",
	"TLS_RSA_WITH_AES_256_CBC_SHA256", "TLS_RSA_WITH_AES_128_CBC_SHA256",
	"TLS_RSA_WITH_AES_256_CBC_SHA", "TLS_RSA_WITH_AES_128_CBC_SHA",
	"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384", "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
	"TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA384", "TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256",
	"TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA", "TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA",
	"TLS_DHE_DSS_WITH_AES_256_CBC_SHA256", "TLS_DHE_DSS_WITH_AES_128_CBC_SHA256",
	"TLS_DHE_DSS_WITH_AES_256_CBC_SHA", "TLS_DHE_DSS_WITH_AES_128_CBC_SHA",
	"TLS_RSA_WITH_3DES_EDE_CBC_SHA", "TLS_DHE_DSS_WITH_3DES_EDE_CBC_SHA",
	"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
}

// applicationGatewaySslPredefinedPolicy is one predefined TLS policy of the
// catalog.
type applicationGatewaySslPredefinedPolicy struct {
	name               string
	minProtocolVersion string
	cipherSuites       []string
}

// applicationGatewaySslPredefinedPolicies is Microsoft.Network's predefined TLS
// policy catalog, in the order the specification's PolicyNameEnum declares it.
var applicationGatewaySslPredefinedPolicies = []applicationGatewaySslPredefinedPolicy{
	{
		name:               "AppGwSslPolicy20150501",
		minProtocolVersion: "TLSv1_0",
		cipherSuites: []string{
			"TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA384", "TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256",
			"TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA", "TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA",
			"TLS_DHE_RSA_WITH_AES_256_GCM_SHA384", "TLS_DHE_RSA_WITH_AES_128_GCM_SHA256",
			"TLS_DHE_RSA_WITH_AES_256_CBC_SHA", "TLS_DHE_RSA_WITH_AES_128_CBC_SHA",
			"TLS_RSA_WITH_AES_256_GCM_SHA384", "TLS_RSA_WITH_AES_128_GCM_SHA256",
			"TLS_RSA_WITH_AES_256_CBC_SHA256", "TLS_RSA_WITH_AES_128_CBC_SHA256",
			"TLS_RSA_WITH_AES_256_CBC_SHA", "TLS_RSA_WITH_AES_128_CBC_SHA",
			"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384", "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
			"TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA384", "TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256",
			"TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA", "TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA",
			"TLS_DHE_DSS_WITH_AES_256_CBC_SHA256", "TLS_DHE_DSS_WITH_AES_128_CBC_SHA256",
			"TLS_DHE_DSS_WITH_AES_256_CBC_SHA", "TLS_DHE_DSS_WITH_AES_128_CBC_SHA",
			"TLS_RSA_WITH_3DES_EDE_CBC_SHA", "TLS_DHE_DSS_WITH_3DES_EDE_CBC_SHA",
		},
	},
	{
		name:               "AppGwSslPolicy20170401",
		minProtocolVersion: "TLSv1_1",
		cipherSuites:       applicationGatewaySslPolicy2017Suites,
	},
	{
		name:               "AppGwSslPolicy20170401S",
		minProtocolVersion: "TLSv1_2",
		cipherSuites:       applicationGatewaySslPolicy2017Suites,
	},
	{
		name:               "AppGwSslPolicy20220101",
		minProtocolVersion: "TLSv1_2",
		cipherSuites: []string{
			"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256", "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384",
			"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
			"TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256", "TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA384",
			"TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256", "TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA384",
		},
	},
	{
		name:               "AppGwSslPolicy20220101S",
		minProtocolVersion: "TLSv1_2",
		cipherSuites: []string{
			"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256", "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384",
			"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
		},
	},
}

// applicationGatewaySslPolicy2017Suites is the cipher suite list the two 2017
// predefined policies share; they differ only in the minimum protocol version.
var applicationGatewaySslPolicy2017Suites = []string{
	"TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA384", "TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256",
	"TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA", "TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA",
	"TLS_DHE_RSA_WITH_AES_256_GCM_SHA384", "TLS_DHE_RSA_WITH_AES_128_GCM_SHA256",
	"TLS_RSA_WITH_AES_256_GCM_SHA384", "TLS_RSA_WITH_AES_128_GCM_SHA256",
	"TLS_RSA_WITH_AES_256_CBC_SHA256", "TLS_RSA_WITH_AES_128_CBC_SHA256",
	"TLS_RSA_WITH_AES_256_CBC_SHA", "TLS_RSA_WITH_AES_128_CBC_SHA",
	"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384", "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
	"TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA384", "TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256",
	"TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA", "TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA",
}

// applicationGatewaySslOptionsDefaultPolicy is the predefined policy an
// application gateway applies when its SSL policy names none.
const applicationGatewaySslOptionsDefaultPolicy = "AppGwSslPolicy20150501"

func registerApplicationGatewayCatalogs(srv *sim.Server) {
	subBase := azureNetworkSubBase()

	srv.HandleFunc("GET "+subBase+"/applicationGatewayAvailableServerVariables", func(w http.ResponseWriter, _ *http.Request) {
		sim.WriteJSON(w, http.StatusOK, applicationGatewayServerVariableNames)
	})
	srv.HandleFunc("GET "+subBase+"/applicationGatewayAvailableRequestHeaders", func(w http.ResponseWriter, _ *http.Request) {
		sim.WriteJSON(w, http.StatusOK, applicationGatewayRequestHeaderNames)
	})
	srv.HandleFunc("GET "+subBase+"/applicationGatewayAvailableResponseHeaders", func(w http.ResponseWriter, _ *http.Request) {
		sim.WriteJSON(w, http.StatusOK, applicationGatewayResponseHeaderNames)
	})

	sslOptionsBase := subBase + "/applicationGatewayAvailableSslOptions/default"

	srv.HandleFunc("GET "+sslOptionsBase, func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		policies := make([]SubResource, 0, len(applicationGatewaySslPredefinedPolicies))
		for _, policy := range applicationGatewaySslPredefinedPolicies {
			policies = append(policies, SubResource{ID: applicationGatewayPredefinedPolicyID(sub, policy.name)})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":   applicationGatewaySslOptionsID(sub),
			"name": "default",
			"type": "Microsoft.Network/applicationGatewayAvailableSslOptions",
			"properties": map[string]any{
				"predefinedPolicies":    policies,
				"defaultPolicy":         applicationGatewaySslOptionsDefaultPolicy,
				"availableCipherSuites": applicationGatewaySslCipherSuites,
				"availableProtocols":    applicationGatewaySslProtocols,
			},
		})
	})

	srv.HandleFunc("GET "+sslOptionsBase+"/predefinedPolicies", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		policies := make([]map[string]any, 0, len(applicationGatewaySslPredefinedPolicies))
		for _, policy := range applicationGatewaySslPredefinedPolicies {
			policies = append(policies, applicationGatewayPredefinedPolicyBody(sub, policy))
		}
		azureWriteList(w, policies)
	})

	srv.HandleFunc("GET "+sslOptionsBase+"/predefinedPolicies/{predefinedPolicyName}", func(w http.ResponseWriter, r *http.Request) {
		name := sim.PathParam(r, "predefinedPolicyName")
		for _, policy := range applicationGatewaySslPredefinedPolicies {
			if policy.name == name {
				sim.WriteJSON(w, http.StatusOK, applicationGatewayPredefinedPolicyBody(sim.PathParam(r, "subscriptionId"), policy))
				return
			}
		}
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.Network/applicationGatewayAvailableSslOptions/default/predefinedPolicies/%s' was not found.", name)
	})
}

func applicationGatewaySslOptionsID(subscription string) string {
	return "/subscriptions/" + subscription + "/providers/Microsoft.Network/applicationGatewayAvailableSslOptions/default"
}

func applicationGatewayPredefinedPolicyID(subscription, name string) string {
	return applicationGatewaySslOptionsID(subscription) + "/predefinedPolicies/" + name
}

func applicationGatewayPredefinedPolicyBody(subscription string, policy applicationGatewaySslPredefinedPolicy) map[string]any {
	return map[string]any{
		"id":   applicationGatewayPredefinedPolicyID(subscription, policy.name),
		"name": policy.name,
		"properties": map[string]any{
			"cipherSuites":       policy.cipherSuites,
			"minProtocolVersion": policy.minProtocolVersion,
		},
	}
}
