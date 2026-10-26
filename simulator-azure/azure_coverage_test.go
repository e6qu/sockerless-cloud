package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Azure operation-coverage gate: for each vendored Azure Resource Manager
// Swagger document, count the operations the simulator serves. The gate boots
// the full server in-process, synthesizes a concrete, authenticated request for
// every documented operation, and counts it only when a mounted handler
// answered. A pattern of the right shape in the route table proves nothing: a
// more specific sibling may shadow it, it may be registered for another method,
// or the mux may never route a real request to it.
//
// Each probe addresses the operation as a real client does: the documented
// method and api-version, the query discriminator an x-ms-paths key carries, a
// simulator-minted Azure Resource Manager bearer, and the per-resource host for
// data planes Azure publishes on one. Path parameters take values the
// specification allows: an enum member where it declares one, a resource path
// where it marks the slot x-ms-skip-url-encoding, a synthesized name otherwise.

// azureDeclaredOperationTotals locks each vendored Swagger document's declared
// operation count. The served floor cannot see a re-vendored document that adds
// operations: every served count stays unchanged while the new operations sit
// unserved. A changed total forces the decision — serve the new operations or
// record why not — and both tables move together.
var azureDeclaredOperationTotals = map[string]int{
	"apimanagement-arm-apimapis-2022-08-01":                           91,
	"apimanagement-arm-apimbackends-2022-08-01":                       7,
	"apimanagement-arm-apimdeletedservices-2022-08-01":                3,
	"apimanagement-arm-apimdeployment-2022-08-01":                     15,
	"apimanagement-arm-apimnamedvalues-2022-08-01":                    8,
	"apimanagement-arm-apimproducts-2022-08-01":                       31,
	"apimanagement-arm-apimsubscriptions-2022-08-01":                  9,
	"app-arm-containerapps-2025-01-01":                                11,
	"app-arm-containerappsrevisions-2025-01-01":                       7,
	"app-arm-jobs-2025-01-01":                                         12,
	"app-arm-managedenvironments-2025-01-01":                          19,
	"app-arm-managedenvironmentsstorages-2025-01-01":                  4,
	"applicationinsights-arm-components_api-2020-02-02":               8,
	"applicationinsights-arm-featuresandpricing-2015-05-01":           5,
	"applicationinsights-dataplane-appinsights-v1-preview":            10,
	"authorization-arm-authorization-roleassignmentscalls-2022-04-01": 10,
	"authorization-arm-authorization-roledefinitionscalls-2022-04-01": 7,
	"compute-arm-computerpcommon-2022-03-01":                          3,
	"compute-arm-skus-2021-07-01":                                     1,
	"compute-arm-virtualmachine-2022-03-01":                           29,
	"containerinstance-arm-containerinstance-2021-10-01":              18,
	"containerregistry-arm-containerregistry-2023-07-01":              52,
	"containerregistry-arm-containerregistry-2025-11-01":              58,
	"containerregistry-arm-registrytasks-2019-06-01-preview":          25,
	"containerregistry-dataplane-containerregistry-2021-07-01":        29,
	"cosmos-db-arm-cosmos-db-2021-10-15":                              121,
	"cosmos-db-arm-cosmos-db-2024-08-15":                              124,
	"cosmos-db-arm-privateendpointconnection-2021-10-15":              4,
	"cosmos-db-arm-privateendpointconnection-2024-08-15":              4,
	"cosmos-db-dataplane-table-2019-02-02":                            14,
	"dns-arm-dns-2018-05-01":                                          14,
	"eventgrid-arm-eventgrid-2021-12-01":                              61,
	"eventgrid-arm-eventgrid-2022-06-15":                              127,
	"eventgrid-dataplane-eventgrid-2018-01-01":                        3,
	"eventhub-arm-authorizationrules-2024-01-01":                      15,
	"eventhub-arm-consumergroups-2024-01-01":                          4,
	"eventhub-arm-eventhubs-2024-01-01":                               4,
	"eventhub-arm-namespaces-2024-01-01":                              14,
	"eventhub-arm-networkrulessets-2024-01-01":                        3,
	"imds-dataplane-imds-2021-02-01":                                  4,
	"keyvault-arm-keyvault-2023-07-01":                                17,
	"keyvault-arm-managedhsm-2023-07-01":                              16,
	"keyvault-dataplane-certificates-2025-07-01":                      27,
	"keyvault-dataplane-keys-2025-07-01":                              25,
	"keyvault-dataplane-secrets-2025-07-01":                           12,
	"logic-arm-logic-2019-05-01":                                      106,
	"monitor-dataplane-datacollectionrules-2023-01-01":                1,
	"monitor-dataplane-operationalinsights-v1":                        7,
	"msi-arm-managedidentity-2023-01-31":                              12,
	"msi-arm-managedidentity-2024-11-30":                              12,
	"network-arm-applicationgateway-2025-03-01":                       22,
	"network-arm-applicationsecuritygroup-2025-03-01":                 6,
	"network-arm-loadbalancer-2025-03-01":                             27,
	"network-arm-natgateway-2025-03-01":                               6,
	"network-arm-networkinterface-2025-03-01":                         15,
	"network-arm-networkmanager-2025-03-01":                           8,
	"network-arm-networkprofile-2025-03-01":                           6,
	"network-arm-networksecuritygroup-2025-03-01":                     12,
	"network-arm-networkwatcher-2025-03-01":                           35,
	"network-arm-privateendpoint-2025-03-01":                          11,
	"network-arm-privatelinkservice-2025-03-01":                       13,
	"network-arm-publicipaddress-2025-03-01":                          9,
	"network-arm-publicipprefix-2025-03-01":                           6,
	"network-arm-routetable-2025-03-01":                               10,
	"network-arm-serviceendpointpolicy-2025-03-01":                    10,
	"network-arm-virtualnetwork-2025-03-01":                           21,
	"network-arm-virtualnetworktap-2025-03-01":                        6,
	"operationalinsights-arm-sharedkeys-2020-08-01":                   2,
	"operationalinsights-arm-workspaces-2020-08-01":                   8,
	"operationalinsights-arm-tables-2022-10-01":                       7,
	"monitor-arm-datacollection-2024-03-11":                           18,
	"monitor-arm-activitylogs-2015-04-01":                             1,
	"monitor-arm-metrics-2024-02-01":                                  6,
	"monitor-arm-diagnosticssettings-2021-05-01-preview":              4,
	"monitor-arm-diagnosticssettingscategories-2017-05-01-preview":    2,
	"postgresql-arm-openapi-2025-08-01":                               66,
	"privatedns-arm-privatedns-2024-06-01":                            17,
	"redis-arm-redis-2024-11-01":                                      41,
	"resources-arm-resources-2021-04-01":                              40,
	"resources-arm-subscriptions-2022-12-01":                          7,
	"servicebus-arm-authorizationrules-2021-11-01":                    21,
	"servicebus-arm-disasterrecoveryconfigs-2021-11-01":               6,
	"servicebus-arm-migrationconfigs-2021-11-01":                      6,
	"servicebus-arm-namespace-preview-2021-11-01":                     11,
	"servicebus-arm-namespaces-2024-01-01":                            11,
	"servicebus-arm-networksets-2021-11-01":                           3,
	"servicebus-arm-queue-2021-11-01":                                 4,
	"servicebus-arm-subscriptions-2021-11-01":                         4,
	"servicebus-arm-topics-2021-11-01":                                4,
	"servicebus-dataplane-servicebus-2021-05":                         13,
	"storage-arm-blob-2024-01-01":                                     17,
	"storage-arm-file-2024-01-01":                                     12,
	"storage-arm-queue-2024-01-01":                                    8,
	"storage-arm-storage-2024-01-01":                                  49,
	"storage-arm-table-2024-01-01":                                    8,
	"storage-dataplane-blob-2026-04-06":                               69,
	"storage-dataplane-file-2026-04-06":                               51,
	"storage-dataplane-queue-2018-03-28":                              16,
	"subscription-arm-subscriptions-2021-10-01":                       15,
	"web-arm-openapi-2025-03-01":                                      692,
}

// azureMethodFloor locks the served-operation count per vendored Swagger
// document (file name without .swagger.json.gz). Every number is a probe result,
// so growing the vendored specs cannot move it on its own.
var azureMethodFloor = map[string]int{
	"apimanagement-arm-apimapis-2022-08-01":             91,
	"apimanagement-arm-apimbackends-2022-08-01":         7,
	"apimanagement-arm-apimdeletedservices-2022-08-01":  3,
	"apimanagement-arm-apimdeployment-2022-08-01":       15,
	"apimanagement-arm-apimnamedvalues-2022-08-01":      8,
	"apimanagement-arm-apimproducts-2022-08-01":         31,
	"apimanagement-arm-apimsubscriptions-2022-08-01":    9,
	"app-arm-containerapps-2025-01-01":                  11,
	"app-arm-containerappsrevisions-2025-01-01":         7,
	"app-arm-jobs-2025-01-01":                           12,
	"app-arm-managedenvironments-2025-01-01":            19,
	"app-arm-managedenvironmentsstorages-2025-01-01":    4,
	"applicationinsights-arm-components_api-2020-02-02": 8,
	// The Enterprise plan carries continuous export and the higher burst; the
	// Basic plan does not.
	"applicationinsights-arm-featuresandpricing-2015-05-01": 5,
	// Application Insights reads the same log store Log Analytics queries,
	// addressed by app id instead of workspace id.
	"applicationinsights-dataplane-appinsights-v1-preview":            10,
	"authorization-arm-authorization-roleassignmentscalls-2022-04-01": 10,
	"authorization-arm-authorization-roledefinitionscalls-2022-04-01": 7,
	// The per-location usage counts the machines and cores the subscription holds
	// there, against its own quotas.
	"compute-arm-computerpcommon-2022-03-01":                 3,
	"compute-arm-skus-2021-07-01":                            1,
	"compute-arm-virtualmachine-2022-03-01":                  29,
	"containerinstance-arm-containerinstance-2021-10-01":     18,
	"containerregistry-arm-containerregistry-2023-07-01":     52,
	"containerregistry-arm-containerregistry-2025-11-01":     58,
	"containerregistry-arm-registrytasks-2019-06-01-preview": 25,
	// The properties APIs report what the registry holds; the only state they add
	// is the four changeable attributes, which the data plane honours.
	"containerregistry-dataplane-containerregistry-2021-07-01": 29,
	"cosmos-db-arm-cosmos-db-2021-10-15":                       121,
	"cosmos-db-arm-cosmos-db-2024-08-15":                       124,
	"cosmos-db-arm-privateendpointconnection-2021-10-15":       4,
	"cosmos-db-arm-privateendpointconnection-2024-08-15":       4,
	// The storage, Service Bus and Key Vault data planes are host-addressed
	// (myaccount.table.<host>, myvault.vault.<host>, …) and served by WrapHandler
	// middlewares, so the probe addresses them at that host.
	"cosmos-db-dataplane-table-2019-02-02": 14,
	"dns-arm-dns-2018-05-01":               14,
	// An extension topic is the name Event Grid gives the events a resource
	// already emits, so the simulator derives it from the scope addressed.
	"eventgrid-arm-eventgrid-2021-12-01":         61,
	"eventgrid-arm-eventgrid-2022-06-15":         127,
	"eventgrid-dataplane-eventgrid-2018-01-01":   3,
	"eventhub-arm-authorizationrules-2024-01-01": 15,
	"eventhub-arm-consumergroups-2024-01-01":     4,
	"eventhub-arm-eventhubs-2024-01-01":          4,
	"eventhub-arm-namespaces-2024-01-01":         14,
	"eventhub-arm-networkrulessets-2024-01-01":   3,
	// The attestation is a real signature over the instance's identity and the
	// caller's nonce, made with the simulator's own signing key.
	"imds-dataplane-imds-2021-02-01":                   4,
	"keyvault-arm-keyvault-2023-07-01":                 17,
	"keyvault-arm-managedhsm-2023-07-01":               16,
	"keyvault-dataplane-certificates-2025-07-01":       27,
	"keyvault-dataplane-keys-2025-07-01":               25,
	"keyvault-dataplane-secrets-2025-07-01":            12,
	"logic-arm-logic-2019-05-01":                       106,
	"monitor-dataplane-datacollectionrules-2023-01-01": 1,
	// The resource-centric query is the workspace query addressed by the Azure
	// resource whose logs are read: "/v1/{resourceId}/query".
	"monitor-dataplane-operationalinsights-v1": 7,
	// The 2023-01-31 document declares the same twelve operations as 2024-11-30;
	// the same routes serve both.
	"msi-arm-managedidentity-2023-01-31":              12,
	"msi-arm-managedidentity-2024-11-30":              12,
	"network-arm-applicationgateway-2025-03-01":       22,
	"network-arm-applicationsecuritygroup-2025-03-01": 6,
	"network-arm-loadbalancer-2025-03-01":             27,
	"network-arm-natgateway-2025-03-01":               6,
	"network-arm-networkinterface-2025-03-01":         15,
	// The configuration resources a commit deploys live in specifications not
	// vendored here, so a commit naming one is refused.
	"network-arm-networkmanager-2025-03-01":         8,
	"network-arm-networkprofile-2025-03-01":         6,
	"network-arm-networksecuritygroup-2025-03-01":   12,
	"network-arm-networkwatcher-2025-03-01":         35,
	"network-arm-privateendpoint-2025-03-01":        11,
	"network-arm-privatelinkservice-2025-03-01":     13,
	"network-arm-publicipaddress-2025-03-01":        9,
	"network-arm-publicipprefix-2025-03-01":         6,
	"network-arm-routetable-2025-03-01":             10,
	"network-arm-serviceendpointpolicy-2025-03-01":  10,
	"network-arm-virtualnetwork-2025-03-01":         21,
	"network-arm-virtualnetworktap-2025-03-01":      6,
	"operationalinsights-arm-sharedkeys-2020-08-01": 2,
	"operationalinsights-arm-workspaces-2020-08-01": 8,
	// A search job's cancellation is a declared 501: the simulator runs no search
	// jobs.
	"operationalinsights-arm-tables-2022-10-01": 6,
	// The six association operations are a declared 501: the simulator runs no
	// Azure Monitor Agent.
	"monitor-arm-datacollection-2024-03-11": 12,
	"monitor-arm-activitylogs-2015-04-01":   1,
	// Platform metrics are served for NAT gateways, whose data plane records them.
	"monitor-arm-metrics-2024-02-01":                               6,
	"monitor-arm-diagnosticssettings-2021-05-01-preview":           4,
	"monitor-arm-diagnosticssettingscategories-2017-05-01-preview": 2,
	"postgresql-arm-openapi-2025-08-01":                            66,
	"privatedns-arm-privatedns-2024-06-01":                         17,
	"redis-arm-redis-2024-11-01":                                   41,
	"resources-arm-resources-2021-04-01":                           40,
	"resources-arm-subscriptions-2022-12-01":                       7,
	"servicebus-arm-authorizationrules-2021-11-01":                 21,
	"servicebus-arm-disasterrecoveryconfigs-2021-11-01":            6,
	"servicebus-arm-migrationconfigs-2021-11-01":                   6,
	"servicebus-arm-namespace-preview-2021-11-01":                  11,
	"servicebus-arm-namespaces-2024-01-01":                         11,
	"servicebus-arm-networksets-2021-11-01":                        3,
	"servicebus-arm-queue-2021-11-01":                              4,
	"servicebus-arm-subscriptions-2021-11-01":                      4,
	"servicebus-arm-topics-2021-11-01":                             4,
	"servicebus-dataplane-servicebus-2021-05":                      13,
	"storage-arm-blob-2024-01-01":                                  17,
	"storage-arm-file-2024-01-01":                                  12,
	"storage-arm-queue-2024-01-01":                                 8,
	"storage-arm-storage-2024-01-01":                               49,
	"storage-arm-table-2024-01-01":                                 8,
	// Copy Blob is reachable both where Azure documents it — the bare PUT carrying
	// x-ms-copy-source — and at the "?comp=copy" key the specification models it
	// under.
	"storage-dataplane-blob-2026-04-06":         69,
	"storage-dataplane-file-2026-04-06":         51,
	"storage-dataplane-queue-2018-03-28":        16,
	"subscription-arm-subscriptions-2021-10-01": 15,
	// Every operation is served. The declared 501s that remain are conditions a
	// particular request meets: a process whose memory this host cannot read, a
	// site whose container runs no PHP, a detector whose input does not exist here.
	"web-arm-openapi-2025-03-01": 692,
}

// azureSimRoute holds the pattern's segments exactly as registered, casing and
// Go 1.22 mux wildcards intact: http.ServeMux compares literal segments
// byte-for-byte, so a probe synthesized from a lowercased template would miss
// "Microsoft.App".
type azureSimRoute struct {
	method string
	raw    []string
}

// rawAzureSegs keeps casing and parameter names; splitAzureSegs is its lossy
// sibling for shape comparison.
func rawAzureSegs(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func azureSimRoutes(srv *sim.Server) []azureSimRoute {
	var routes []azureSimRoute
	for _, p := range srv.RoutePatterns() {
		method, path, ok := strings.Cut(p, " ")
		if !ok {
			continue
		}
		routes = append(routes, azureSimRoute{method: method, raw: rawAzureSegs(path)})
	}
	// Probe the most specific routes first: the mux resolves a concrete path to its
	// most specific pattern.
	sort.SliceStable(routes, func(i, j int) bool {
		return azureLiteralCount(routes[i].raw) > azureLiteralCount(routes[j].raw)
	})
	return routes
}

func azureLiteralCount(segs []string) int {
	n := 0
	for _, s := range segs {
		if !azureIsWildcard(s) {
			n++
		}
	}
	return n
}

func azureIsWildcard(s string) bool {
	return strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")
}

func azureIsGreedyWildcard(s string) bool {
	return azureIsWildcard(s) && strings.HasSuffix(s[:len(s)-1], "...")
}

const (
	azureProbeSubscription  = "00000000-0000-0000-0000-000000000001"
	azureProbeResourceGroup = "sim-coverage-rg"
	azureProbeLocation      = "eastus"
)

// azureProbeResourceID fills a path parameter the specification marks as a
// whole resource path. A single synthetic segment is not an address any client
// sends.
const azureProbeResourceID = "subscriptions/" + azureProbeSubscription +
	"/resourceGroups/" + azureProbeResourceGroup +
	"/providers/Microsoft.Web/sites/simprobedsite"

// azureProbeParamValue gives the identity-bearing parameters values of the
// right shape (a GUID subscription, a resource-group name, a region), because
// handlers parse them; a malformed value would get the request rejected for a
// reason unrelated to whether the handler is mounted.
func azureProbeParamValue(param string) string {
	name := strings.Trim(param, "{}")
	name = strings.TrimSuffix(name, "...")
	lower := strings.ToLower(name)
	switch {
	case lower == "resourceid" || lower == "resourceuri":
		return azureProbeResourceID
	case strings.Contains(lower, "subscriptionid"):
		return azureProbeSubscription
	case strings.Contains(lower, "tenantid"):
		return simTenantID
	case strings.Contains(lower, "resourcegroup"):
		return azureProbeResourceGroup
	case lower == "location" || lower == "locationname" || lower == "region" ||
		strings.HasSuffix(lower, "location") || strings.HasSuffix(lower, "region"):
		return azureProbeLocation
	case strings.Contains(lower, "apiversion"):
		return "2024-01-01"
	}
	// A stable, DNS-label-safe name per parameter, so probes for different
	// parameters never collide in the simulator's stores.
	var b strings.Builder
	b.WriteString("sim")
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// azureUnify builds one concrete request path that is both an instance of the
// swagger template and a path the route pattern matches. Per segment:
//
//   - literal vs literal: they must agree case-insensitively, as Azure Resource
//     Manager matches paths, and the route's spelling wins because
//     http.ServeMux is case-sensitive;
//   - route wildcard vs template literal: use the template's literal;
//   - template parameter: the value azureTemplateVariants chose, never the
//     route's literal. Every host shares one port, so borrowing route literals
//     would let Azure Queue Storage's "PUT /{queueName}" unify with the OCI
//     registry's "PUT /v2/" and count as served;
//   - a trailing route "{name...}" absorbs every remaining template segment.
//
// It returns false when the shapes cannot produce a common path.
func azureUnify(route, spec []string) (string, bool) {
	var out []string
	i, j := 0, 0
	for ; i < len(route) && j < len(spec); i, j = i+1, j+1 {
		rs, ss := route[i], spec[j]
		if rs == "{$}" {
			return "", false // end-of-path anchor cannot consume a segment
		}
		if azureIsGreedyWildcard(rs) {
			for ; j < len(spec); j++ {
				out = append(out, spec[j])
			}
			return "/" + strings.Join(out, "/"), true
		}
		if !azureIsWildcard(rs) {
			if !strings.EqualFold(rs, ss) {
				return "", false
			}
			out = append(out, rs)
			continue
		}
		out = append(out, ss)
	}
	if i < len(route) && route[i] == "{$}" && j == len(spec) {
		i++
	}
	if i == len(route)-1 && j == len(spec) && azureIsGreedyWildcard(route[i]) {
		i++
	}
	if i != len(route) || j != len(spec) {
		return "", false
	}
	if len(out) == 0 {
		return "/", true
	}
	return "/" + strings.Join(out, "/"), true
}

// azureTemplateVariants returns one segment list per enum value of each enum
// path parameter — DNS record sets live at ".../{zoneName}/A/{name}", a storage
// service at ".../blobServices/default" — and a synthesized value for every
// other parameter.
func azureTemplateVariants(sp swaggerPath) [][]string {
	const maxVariants = 8
	variants := [][]string{{}}
	for _, seg := range sp.Raw {
		var values []string
		switch {
		case !azureIsWildcard(seg):
			values = []string{seg}
		default:
			name := strings.TrimSuffix(strings.Trim(seg, "{}"), "...")
			if enum := sp.PathEnums[name]; len(enum) > 0 {
				values = enum
			} else {
				values = []string{azureProbeParamValue(seg)}
			}
		}
		next := make([][]string, 0, len(variants)*len(values))
		for _, v := range variants {
			for _, value := range values {
				if len(next) == maxVariants {
					break
				}
				next = append(next, append(append([]string{}, v...), value))
			}
		}
		variants = next
	}
	return variants
}

// azureHasScopeLead reports an operation whose first path parameter is an Azure
// Resource Manager scope or resource ID and whose remaining template still pins
// the service with a literal. A template that is nothing but a resource ID —
// "GET /{resourceId}" — would otherwise absorb every route and count every
// generic-resource operation as served.
func azureHasScopeLead(sp swaggerPath) bool {
	if len(sp.Raw) < 2 || !azureIsWildcard(sp.Raw[0]) {
		return false
	}
	// Some documents omit x-ms-skip-url-encoding — Event Grid's extensionTopics
	// declares a bare `scope` — but a leading `scope` followed by `providers` is
	// an Azure Resource Manager scope by construction.
	name := strings.Trim(sp.Raw[0], "{}")
	bareARMScope := name == "scope" && len(sp.Raw) > 1 && sp.Raw[1] == "providers"
	if !sp.PathScopes[name] && !bareARMScope {
		return false
	}
	for _, s := range sp.Raw[1:] {
		if !azureIsWildcard(s) {
			return true
		}
	}
	return false
}

// azureUnifyScoped lets the scope slot absorb as many leading route segments as
// the shapes allow. Borrowing route segments is legitimate only here: the
// specification declares the slot to hold a resource path.
func azureUnifyScoped(route, spec []string) []string {
	var out []string
	for k := 1; k <= len(route)-(len(spec)-1); k++ {
		rest, ok := azureUnify(route[k:], spec[1:])
		if !ok {
			continue
		}
		var scope []string
		for _, s := range route[:k] {
			if azureIsWildcard(s) {
				if s == "{$}" || azureIsGreedyWildcard(s) {
					scope = nil
					break
				}
				scope = append(scope, azureProbeParamValue(s))
				continue
			}
			scope = append(scope, s)
		}
		if len(scope) != k {
			continue
		}
		path := "/" + strings.Join(scope, "/")
		if rest != "/" {
			path += rest
		}
		out = append(out, path)
	}
	return out
}

// azureProbeCandidates derives candidates from the registered routes, the only
// paths a handler can be reached at, capped so a pathological template cannot
// dominate the run. A candidate is not evidence of coverage; only the
// simulator's answer to it is.
func azureProbeCandidates(routes []azureSimRoute, sp swaggerPath) []string {
	const maxCandidates = 12
	seen := map[string]bool{}
	var out []string
	add := func(path string) bool {
		if seen[path] {
			return true
		}
		seen[path] = true
		out = append(out, path)
		return len(out) < maxCandidates
	}
	scopeLed := azureHasScopeLead(sp)
	for _, variant := range azureTemplateVariants(sp) {
		matched := false
		for _, r := range routes {
			if r.method != sp.Method {
				continue
			}
			var paths []string
			if scopeLed {
				paths = azureUnifyScoped(r.raw, variant)
			} else if path, ok := azureUnify(r.raw, variant); ok {
				paths = []string{path}
			}
			for _, path := range paths {
				matched = true
				if !add(path) {
					return out
				}
			}
		}
		if !matched {
			// No registered pattern produces a path for this spelling. Probe the plain
			// instantiation anyway, so the simulator's answer, not the route table, shows
			// the operation unserved.
			if !add("/" + strings.Join(variant, "/")) {
				return out
			}
		}
	}
	return out
}

type azureProber struct {
	srv   *sim.Server
	token string
}

func newAzureProber(t *testing.T) *azureProber {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	// Long-running operations complete in a goroutine; one still running when this
	// test ends would race the next test's store rebuild.
	t.Cleanup(bg.Await)
	now := time.Now()
	// AzureBearerVerificationMiddleware rejects every unauthenticated
	// /subscriptions/ and /providers/ request; without a token every probe would
	// count as served by its 401.
	token, err := mintBootstrapSimJWT("https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint Azure Resource Manager token: %v", err)
	}
	p := &azureProber{srv: srv, token: token}
	// Azure Resource Manager refuses a request beneath a missing resource group
	// before any provider sees it.
	for _, group := range []string{
		"/subscriptions/" + azureProbeSubscription + "/resourceGroups/" + azureProbeResourceGroup,
		"/subscriptions/" + azureDefaultSubscriptionID + "/resourceGroups/probe-rg",
	} {
		req := httptest.NewRequest(http.MethodPut, group+"?api-version=2021-04-01",
			strings.NewReader(`{"location":"`+azureProbeLocation+`"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create resource group %s: status %d: %s", group, rec.Code, rec.Body.String())
		}
	}
	return p
}

type azureProbeResult struct {
	path     string
	status   int
	body     string
	pattern  string // the route pattern the mux resolved the request to, "" for a miss
	served   bool
	panicked bool
	reason   string
}

// serve sends one synthesized request through the real handler chain (CORS,
// OAuth interception, path cleanup, bearer verification, api-version, mux).
func (p *azureProber) serve(method, path, query, apiVersion, host string) azureProbeResult {
	url := path + "?api-version=" + apiVersion
	if query != "" {
		url += "&" + query
	}
	var body io.Reader
	hasBody := method == http.MethodPut || method == http.MethodPost || method == http.MethodPatch
	if hasBody {
		// Enough for a handler to decode; whether it answers 400 or 201, the handler
		// is mounted.
		body = strings.NewReader("{}")
	}
	req := httptest.NewRequest(method, url, body)
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "application/json")
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	if host != "" {
		// The Azure Storage SDKs send x-ms-version on every call, and the storage
		// dispatcher reads it to separate storage traffic from the other services on
		// the port.
		req.Host = host
		req.Header.Set("x-ms-version", apiVersion)
	}
	rec := httptest.NewRecorder()

	// http.ServeMux.Handler makes the real routing decision, shadowing and
	// precedence included, and returns an empty pattern when no route matches for
	// this method.
	_, pattern := p.srv.Mux().Handler(req)

	res := azureProbeResult{path: path, pattern: pattern}
	func() {
		defer func() {
			if r := recover(); r != nil {
				// A panic proves a handler ran, so the operation is served — and the panic is
				// a fidelity bug.
				res.panicked = true
				res.served = true
				res.reason = fmt.Sprintf("handler panicked: %v", r)
			}
		}()
		p.srv.ServeHTTP(rec, req)
	}()
	if res.panicked {
		return res
	}

	res.status = rec.Code
	res.body = strings.TrimSpace(rec.Body.String())
	res.served, res.reason = azureClassify(pattern, rec)
	return res
}

// azureClassify separates "nothing is mounted here" from "a handler ran and
// reported the resource absent". Both answer 404; only the first means the
// operation is unimplemented.
func azureClassify(pattern string, rec *httptest.ResponseRecorder) (served bool, reason string) {
	if gap, why := azureDeclaredGap(rec); gap {
		return false, why
	}
	if pattern == "" {
		// No route matched. Either http.ServeMux answered itself (404, or 405 when the
		// path exists for other methods only) and no simulator code ran, or a
		// middleware ahead of the mux answered, which is a served surface.
		body := strings.TrimSpace(rec.Body.String())
		switch {
		case rec.Code == http.StatusNotFound && body == "404 page not found":
			return false, "mux miss: no route matched"
		case rec.Code == http.StatusMethodNotAllowed && body == "Method Not Allowed":
			return false, "mux miss: path registered for other methods only"
		case rec.Code == http.StatusMovedPermanently && rec.Header().Get("Location") != "":
			return false, "mux redirect: no handler ran"
		}
		return true, fmt.Sprintf("middleware answered %d ahead of the mux", rec.Code)
	}
	// The router resolved a registered pattern and its handler answered; any
	// status code here required simulator code to run.
	return true, fmt.Sprintf("%s answered %d", pattern, rec.Code)
}

// azureDeclaredGap reports a response in which the simulator says it has no
// handler for the operation. The host-addressed data planes dispatch inside one
// middleware, so their "nothing is mounted here" is a structured error rather
// than the mux's plain-text 404.
func azureDeclaredGap(rec *httptest.ResponseRecorder) (bool, string) {
	if rec.Code == http.StatusNotImplemented {
		return true, "handler answered 501 NotImplemented"
	}
	code := rec.Header().Get("x-ms-error-code")
	var message string
	if code != "" {
		// Azure Storage puts the machine-readable error code in this header.
		message = rec.Body.String()
	} else {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &envelope) != nil {
			return false, ""
		}
		code, message = envelope.Error.Code, envelope.Error.Message
	}
	switch {
	case code == "MethodNotAllowed" && strings.Contains(message, "Method not supported"):
		// Every host-addressed dispatcher writes this when it recognizes no operation
		// for the method and path.
		return true, "dispatcher does not recognize the operation (MethodNotAllowed)"
	case strings.Contains(message, "not implemented"):
		return true, "dispatcher declared the operation not implemented"
	}
	return false, ""
}

// Azure Resource Manager rejects a request without an api-version
// (AzureARMAPIVersionMiddleware), so take it from the document being measured.
func azureProbeAPIVersion(sp swaggerPath) string {
	if sp.APIVersion != "" {
		return sp.APIVersion
	}
	return "2024-01-01"
}

// azureDataPlaneHosts maps a vendored data-plane document to the host a client
// addresses it at. Azure publishes these planes on per-resource hostnames, and
// the simulator dispatches them on the host from WrapHandler middlewares, so a
// probe without the host reaches the Azure Resource Manager plane instead.
var azureDataPlaneHosts = map[string]string{
	"storage-dataplane-blob-":     "simaccount.blob.localhost",
	"storage-dataplane-file-":     "simaccount.file.localhost",
	"storage-dataplane-queue-":    "simaccount.queue.localhost",
	"cosmos-db-dataplane-table-":  "simaccount.table.localhost",
	"keyvault-dataplane-":         "simvault.vault.localhost",
	"servicebus-dataplane-":       "simnamespace.servicebus.localhost",
	"eventgrid-dataplane-":        "simtopic.eventgrid.localhost",
	"containerregistry-dataplane": "simregistry.azurecr.localhost",
}

func azureProbeHost(file string) string {
	for prefix, host := range azureDataPlaneHosts {
		if strings.HasPrefix(file, prefix) {
			return host
		}
	}
	return ""
}

// azureCoverage counts, per vendored swagger file, the operations the simulator
// serves. findings holds mounted handlers that panic or answer a server error.
type azureCoverage struct {
	impl     map[string]int
	total    map[string]int
	unserved map[string][]string
	findings []string
	// silent holds every unserved operation that did not answer a declared 501: a
	// routing 404 claims the resource is absent, and a client cannot tell that
	// from "not implemented here".
	silent []string
}

func azureProbeCoverage(t *testing.T) *azureCoverage {
	t.Helper()
	_, byFile := loadSwaggerPaths(t)
	p := newAzureProber(t)
	routes := azureSimRoutes(p.srv)

	cov := &azureCoverage{
		impl:     map[string]int{},
		total:    map[string]int{},
		unserved: map[string][]string{},
	}

	type probeOp struct {
		file       string
		method     string
		template   string
		query      string
		apiVersion string
		host       string
		sp         swaggerPath
	}
	var ops []probeOp
	for file, specs := range byFile {
		cov.total[strings.TrimSuffix(file, ".swagger.json.gz")] = len(specs)
		for _, sp := range specs {
			ops = append(ops, probeOp{
				file:       file,
				method:     sp.Method,
				template:   "/" + strings.Join(sp.Raw, "/"),
				query:      sp.Query,
				apiVersion: azureProbeAPIVersion(sp),
				host:       azureProbeHost(file),
				sp:         sp,
			})
		}
	}
	// Probe in a fixed order so the count does not depend on map iteration. Reads
	// run before writes and deletes run last, so the delete probe tears down what
	// the create probe for the same template made.
	methodRank := map[string]int{
		http.MethodGet: 0, http.MethodHead: 0, http.MethodOptions: 0,
		http.MethodPost: 1, http.MethodPut: 1, http.MethodPatch: 1,
		http.MethodDelete: 2,
	}
	sort.Slice(ops, func(i, j int) bool {
		a, b := ops[i], ops[j]
		if ra, rb := methodRank[a.method], methodRank[b.method]; ra != rb {
			return ra < rb
		}
		if a.template != b.template {
			return a.template < b.template
		}
		if a.query != b.query {
			return a.query < b.query
		}
		if a.method != b.method {
			return a.method < b.method
		}
		if a.apiVersion != b.apiVersion {
			return a.apiVersion < b.apiVersion
		}
		return a.file < b.file
	})

	// One operation can appear in several vendored documents (api-version variants
	// of one service); probe each distinct coordinate once.
	type opKey struct{ method, template, query, apiVersion, host string }
	cache := map[opKey]azureProbeResult{}

	for _, op := range ops {
		name := strings.TrimSuffix(op.file, ".swagger.json.gz")
		key := opKey{op.method, op.template, op.query, op.apiVersion, op.host}
		res, done := cache[key]
		if !done {
			res = azureProbeResult{reason: "no candidate path could be synthesized"}
			for _, path := range azureProbeCandidates(routes, op.sp) {
				res = p.serve(op.method, path, op.query, op.apiVersion, op.host)
				if res.served {
					break
				}
			}
			cache[key] = res
		}
		label := op.method + " " + op.template
		if op.query != "" {
			label += "?" + op.query
		}
		switch {
		case !res.served:
			cov.unserved[name] = append(cov.unserved[name],
				fmt.Sprintf("%s → probed %s: %s", label, res.path, res.reason))
			if res.status != http.StatusNotImplemented {
				cov.silent = append(cov.silent,
					fmt.Sprintf("%s: %s → probed %s: answered %d, not a declared 501: %s",
						name, label, res.path, res.status, res.reason))
			}
		case res.panicked:
			cov.impl[name]++
			cov.findings = append(cov.findings,
				fmt.Sprintf("%s: %s → %s (%s)", name, label, res.path, res.reason))
		case res.status >= 500:
			cov.impl[name]++
			cov.findings = append(cov.findings,
				fmt.Sprintf("%s: %s → %s returned %d: %s", name, label, res.path, res.status, azureTruncate(res.body)))
		default:
			cov.impl[name]++
		}
	}
	for name := range cov.unserved {
		sort.Strings(cov.unserved[name])
	}
	sort.Strings(cov.findings)
	sort.Strings(cov.silent)
	return cov
}

func azureTruncate(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// TestServiceConformance_AzureCoverage is informational and never fails.
func TestServiceConformance_AzureCoverage(t *testing.T) {
	cov := azureProbeCoverage(t)
	names := make([]string, 0, len(cov.total))
	for name := range cov.total {
		names = append(names, name)
	}
	sort.Strings(names)
	ti, tt := 0, 0
	for _, name := range names {
		if cov.total[name] == 0 {
			continue // pure-definitions swagger (common-types, etc.)
		}
		ti += cov.impl[name]
		tt += cov.total[name]
		if cov.impl[name] > 0 {
			t.Logf("%-60s %d/%d", name, cov.impl[name], cov.total[name])
		}
		// Name what is missing, so the next operation to serve is readable from an
		// ordinary run.
		for _, op := range cov.unserved[name] {
			t.Logf("%-60s   unserved: %s", name, op)
		}
	}
	t.Logf("TOTAL: %d/%d Azure Swagger operations served by a mounted handler", ti, tt)
}

// TestServiceConformance_AzureCoverageFloor is an exact-equality ratchet: a
// drop is a regression, and an increase must bump the floor.
func TestServiceConformance_AzureCoverageFloor(t *testing.T) {
	cov := azureProbeCoverage(t)
	for name, total := range cov.total {
		if declared, locked := azureDeclaredOperationTotals[name]; !locked {
			t.Errorf("%s: vendored Swagger document has no azureDeclaredOperationTotals entry — add one at its declared count (%d)", name, total)
		} else if total != declared {
			t.Errorf("%s: the vendored document declares %d operations, the lock says %d — a re-vendor changed the surface. Serve the new operations or record why not, then update azureDeclaredOperationTotals (and azureMethodFloor if coverage moved).",
				name, total, declared)
		}
	}
	for name, floor := range azureMethodFloor {
		if _, ok := cov.total[name]; !ok {
			t.Errorf("%s: floor set but no vendored Swagger document found", name)
			continue
		}
		if cov.impl[name] != floor {
			t.Errorf("%s: coverage %d/%d != floor %d — update azureMethodFloor (a drop is a regression; more is a ratchet-up).\n  unserved:\n    %s",
				name, cov.impl[name], cov.total[name], floor,
				strings.Join(cov.unserved[name], "\n    "))
		}
	}
}

// TestServiceConformance_AzureUnservedOperationsDeclareThemselves holds every
// unserved operation to a declared 501. The floor counts unserved operations
// without caring why, so a gap that stopped declaring itself would keep the
// count and lose the declaration.
func TestServiceConformance_AzureUnservedOperationsDeclareThemselves(t *testing.T) {
	cov := azureProbeCoverage(t)
	if len(cov.silent) > 0 {
		t.Errorf("%d unserved operation(s) answered something other than a declared 501:\n  %s",
			len(cov.silent), strings.Join(cov.silent, "\n  "))
	}
}

// TestServiceConformance_AzureProbedHandlersAreHealthy fails on a mounted
// handler that panics or answers 5xx; such a handler still counts toward
// coverage, so the floor alone would hide it.
func TestServiceConformance_AzureProbedHandlersAreHealthy(t *testing.T) {
	cov := azureProbeCoverage(t)
	if len(cov.findings) > 0 {
		t.Errorf("%d mounted handler(s) failed a synthesized request (panic or 5xx) — each is a fidelity bug:\n  %s",
			len(cov.findings), strings.Join(cov.findings, "\n  "))
	}
}
