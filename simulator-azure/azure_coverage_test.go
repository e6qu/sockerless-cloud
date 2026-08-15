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

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Azure operation-coverage gate — the Swagger-spec analogue of the AWS
// service-conformance ratchet and the GCP Discovery-doc ratchet. For each
// vendored Azure ARM Swagger document it counts how many of the service's
// operations the simulator implements. "Implements" is decided by asking the
// running simulator: the gate boots the full server in-process, synthesizes a
// concrete, authenticated request for every documented operation, and counts
// the operation only when a mounted handler actually answered it. A route
// table that merely contains a pattern of the right shape proves nothing —
// the pattern may be shadowed by a more specific sibling, may be registered
// for a different method, or may spell a template the mux never routes a real
// request to. The count is locked by azureMethodFloor: a drop is a
// regression, an increase is a ratchet-up that must bump the floor.
//
// Each probe is addressed the way a real client addresses the operation: the
// documented method, the documented api-version, the query discriminator an
// x-ms-paths key carries, an Azure Resource Manager bearer the simulator
// minted, and — for the data planes Azure publishes on per-resource hostnames —
// that host. Path parameters take the values the specification allows: an enum
// member where it declares one, an ARM resource path where it marks the slot
// x-ms-skip-url-encoding, a synthesized name otherwise. Two things then decide
// the verdict: http.ServeMux's own routing of that concrete request, and the
// answer the simulator gives it.

// azureMethodFloor locks the implemented-operation COUNT per vendored Azure
// Swagger document (keyed by file name without the .swagger.json.gz suffix).
// Implement an operation (or grow the vendored spec) and the matching floor
// must move with it. The bulk of the large surfaces (web-arm ~692,
// cosmos-db ~124, logic ~106) is intentionally far from 100% — the floor
// records the honest implemented count, not an aspiration.
//
// Every number here is a probe result, not a pattern count: the simulator was
// asked to serve each documented operation and answered. Growing the vendored
// specs therefore cannot move these numbers on its own.
var azureMethodFloor = map[string]int{
	"apimanagement-arm-apimapis-2022-08-01":                 91,
	"apimanagement-arm-apimbackends-2022-08-01":             7,
	"apimanagement-arm-apimdeletedservices-2022-08-01":      3,
	"apimanagement-arm-apimdeployment-2022-08-01":           15,
	"apimanagement-arm-apimnamedvalues-2022-08-01":          8,
	"apimanagement-arm-apimproducts-2022-08-01":             31,
	"apimanagement-arm-apimsubscriptions-2022-08-01":        9,
	"app-arm-containerapps-2025-01-01":                      11,
	"app-arm-jobs-2025-01-01":                               12,
	"app-arm-managedenvironments-2025-01-01":                19,
	"app-arm-managedenvironmentsstorages-2025-01-01":        4,
	"applicationinsights-arm-components_api-2020-02-02":     8,
	"applicationinsights-arm-featuresandpricing-2015-05-01": 2,
	"applicationinsights-dataplane-appinsights-v1-preview":  1,
	// Lowered from 9. GET/PUT/DELETE "/{roleAssignmentId}" — the full-resource-ID
	// spelling — probe to a plain mux miss: no registered pattern serves a bare
	// resource ID. The old count matched them by shape against the scoped
	// "/{scope}/.../roleAssignments/{name}" routes.
	"authorization-arm-authorization-roleassignmentscalls-2022-04-01": 7,
	// Lowered from 5. GET "/{roleId}" and the two ".../Microsoft.Authorization/
	// permissions" list operations mux-miss; nothing is registered for either.
	"authorization-arm-authorization-roledefinitionscalls-2022-04-01": 4,
	"compute-arm-computerpcommon-2022-03-01":                          1,
	"compute-arm-skus-2021-07-01":                                     1,
	"compute-arm-virtualmachine-2022-03-01":                           29,
	"containerinstance-arm-containerinstance-2021-10-01":              18,
	"containerregistry-arm-containerregistry-2023-07-01":              52,
	"containerregistry-arm-containerregistry-2025-11-01":              58,
	"containerregistry-arm-registrytasks-2019-06-01-preview":          25,
	// Raised from 13: the "/acr/v1/{path...}" registry routes do serve these,
	// which shape matching missed where a route parameter sat under a spec
	// literal. Raised again from 19 by the registry's token service growing
	// the spec's "GET /oauth2/token"
	// (Authentication_GetAcrAccessTokenFromLogin) — the Docker Registry v2
	// token endpoint that trades a Basic admin credential for the scoped
	// access token the data plane now requires.
	"containerregistry-dataplane-containerregistry-2021-07-01": 20,
	"cosmos-db-arm-cosmos-db-2021-10-15":                       121,
	"cosmos-db-arm-cosmos-db-2024-08-15":                       124,
	"cosmos-db-arm-privateendpointconnection-2021-10-15":       4,
	"cosmos-db-arm-privateendpointconnection-2024-08-15":       4,
	// Raised from 3, and "servicebus-dataplane" / the three "keyvault-dataplane"
	// / the three "storage-dataplane" entries likewise: these planes are
	// host-addressed (myaccount.table.<host>, myvault.vault.<host>, …) and are
	// served from WrapHandler middlewares rather than mux patterns, so the probe
	// addresses them at that coordinate. The old numbers here were phantom —
	// they came from shape matches against Cosmos DB's "/offers/{offer}" and
	// "/dbs/{database}" routes and the OCI registry's "/v2/", not from the
	// storage handlers at all.
	"cosmos-db-dataplane-table-2019-02-02": 14,
	"dns-arm-dns-2018-05-01":               14,
	// Lowered from 61. GET "/{scope}/providers/Microsoft.EventGrid/
	// extensionTopics/default" mux-misses — no extensionTopics route exists.
	"eventgrid-arm-eventgrid-2021-12-01": 60,
	// Lowered from 127, for the same unserved extensionTopics operation.
	"eventgrid-arm-eventgrid-2022-06-15":               126,
	"eventgrid-dataplane-eventgrid-2018-01-01":         3,
	"eventhub-arm-authorizationrules-2024-01-01":       15,
	"eventhub-arm-consumergroups-2024-01-01":           4,
	"eventhub-arm-eventhubs-2024-01-01":                4,
	"eventhub-arm-namespaces-2024-01-01":               14,
	"eventhub-arm-networkrulessets-2024-01-01":         3,
	"imds-dataplane-imds-2021-02-01":                   2,
	"keyvault-arm-keyvault-2023-07-01":                 17,
	"keyvault-arm-managedhsm-2023-07-01":               6,
	"keyvault-dataplane-certificates-2025-07-01":       24,
	"keyvault-dataplane-keys-2025-07-01":               22,
	"keyvault-dataplane-secrets-2025-07-01":            12,
	"logic-arm-logic-2019-05-01":                       106,
	"monitor-dataplane-datacollectionrules-2023-01-01": 1,
	"monitor-dataplane-operationalinsights-v1":         5,
	"msi-arm-managedidentity-2024-11-30":               12,
	"network-arm-applicationgateway-2025-03-01":        22,
	"network-arm-applicationsecuritygroup-2025-03-01":  6,
	"network-arm-loadbalancer-2025-03-01":              27,
	"network-arm-natgateway-2025-03-01":                6,
	"network-arm-networkinterface-2025-03-01":          15,
	// Azure Virtual Network Manager's own resource, its commit and its
	// deployment status. The configuration resources a commit deploys
	// (network groups, connectivity and security-admin configurations) are
	// separate specifications that are not vendored here, so a commit that
	// names one is refused for the configuration that does not exist.
	"network-arm-networkmanager-2025-03-01":       8,
	"network-arm-networkprofile-2025-03-01":       6,
	"network-arm-networksecuritygroup-2025-03-01": 12,
	// Complete, including the six PacketCaptures operations: a capture opens a
	// packet socket on the target machine's interface and writes the frames it
	// records into the storage account it names, through the same Blob data
	// plane a client reads them back from.
	"network-arm-networkwatcher-2025-03-01":         35,
	"network-arm-privateendpoint-2025-03-01":        11,
	"network-arm-privatelinkservice-2025-03-01":     13,
	"network-arm-publicipaddress-2025-03-01":        9,
	"network-arm-publicipprefix-2025-03-01":         6,
	"network-arm-routetable-2025-03-01":             10,
	"network-arm-serviceendpointpolicy-2025-03-01":  10,
	"network-arm-virtualnetwork-2025-03-01":         21,
	"network-arm-virtualnetworktap-2025-03-01":      6,
	"operationalinsights-arm-sharedkeys-2020-08-01": 1,
	"operationalinsights-arm-workspaces-2020-08-01": 8,
	"postgresql-arm-openapi-2025-08-01":             66,
	"privatedns-arm-privatedns-2024-06-01":          17,
	"redis-arm-redis-2024-11-01":                    41,
	// Lowered from 36. The generic-resource operations — the five methods on
	// "/{resourceId}" and the five on ".../providers/{ns}/{parentResourcePath}/
	// {type}/{name}" — all mux-miss. Their templates are almost pure parameters,
	// so under shape matching they unified with unrelated typed routes.
	"resources-arm-resources-2021-04-01":                30,
	"resources-arm-subscriptions-2022-12-01":            7,
	"servicebus-arm-authorizationrules-2021-11-01":      21,
	"servicebus-arm-disasterrecoveryconfigs-2021-11-01": 6,
	"servicebus-arm-migrationconfigs-2021-11-01":        6,
	"servicebus-arm-namespace-preview-2021-11-01":       11,
	"servicebus-arm-namespaces-2024-01-01":              11,
	"servicebus-arm-networksets-2021-11-01":             3,
	"servicebus-arm-queue-2021-11-01":                   4,
	"servicebus-arm-subscriptions-2021-11-01":           4,
	"servicebus-arm-topics-2021-11-01":                  4,
	"servicebus-dataplane-servicebus-2021-05":           13,
	"storage-arm-blob-2024-01-01":                       17,
	"storage-arm-file-2024-01-01":                       12,
	"storage-arm-queue-2024-01-01":                      8,
	"storage-arm-storage-2024-01-01":                    44,
	"storage-arm-table-2024-01-01":                      8,
	// The three storage data-plane counts are measurements now that each
	// dispatcher answers an unrecognized "comp"/"restype" with a declared gap
	// instead of falling through to whichever sibling handler sits under the
	// same method. What the numbers cover:
	//
	//   blob 69/69 — the whole vendored Blob data-plane surface: containers
	//     (create/get/delete/list flat + hierarchical, metadata, ACL, rename,
	//     restore, batch, filter, lease), blobs (put/get/head/delete, the copy
	//     family, block staging and commit, page and append ranges, snapshots,
	//     undelete, tags, tier, expiry, HTTP headers, immutability and legal
	//     hold, lease, query), and the account-wide service operations
	//     (properties get+set, statistics, user delegation key, account info,
	//     filter, batch). Copy Blob is reachable both where Azure documents it —
	//     the bare PUT carrying x-ms-copy-source — and at the "?comp=copy" key
	//     the specification models it under.
	//   file 51/51 — the whole documented Files data plane: the service
	//     (list shares, service properties get+set, user delegation key), the
	//     share (create/get/delete, lease, snapshot, stored permissions, ACL,
	//     statistics, metadata, properties, restore), every directory operation
	//     at any depth, and every file operation (create, download, properties,
	//     metadata, leases, ranges and range lists, handles, rename, copy, hard
	//     and symbolic links).
	//   queue 16/16 — the whole documented Queues data plane: the service
	//     (list queues, service properties get+set, statistics), the queue
	//     (create/delete, metadata get+set, access policy get+set) and messages
	//     (enqueue/dequeue/peek/clear/update/delete).
	"storage-dataplane-blob-2026-04-06":  69,
	"storage-dataplane-file-2026-04-06":  51,
	"storage-dataplane-queue-2018-03-28": 16,
	// The whole document: aliases, the subscription actions, the ownership
	// acceptance long-running operation and its status, the tenant and
	// billing-account policies, and the provider operation catalog.
	"subscription-arm-subscriptions-2021-10-01": 15,
	// Raised from 503 by the App Service instance and process family: the
	// site's running workload container is the instance, and the container
	// engine's process table is the site's process list, so
	// ListInstanceIdentifiers, GetInstanceInfo, ListProcesses, GetProcess and
	// ListProcessThreads are served at both the site and the per-instance
	// scope, in both the production and the slot spelling (16 operations).
	//
	// Three families of that swagger stay unserved for a demonstrated reason
	// rather than a deferral, and are named here so the gap is visible beside
	// the number: GetProcessDump, ListProcessModules and GetProcessModule (12
	// operations across both scopes and both spellings) plus DeleteProcess (4).
	// The container engine exposes exactly one process-inspection primitive
	// over its HTTP API — `GET /containers/{id}/top`, the `ps` output for the
	// container's processes — which reports no loaded modules and no core
	// dumps, and it can terminate only the container's main process, not an
	// arbitrary one inside it. Reading modules or writing a dump needs
	// `/proc/<pid>` from inside the container, which requires either a shell
	// in the workload image (a scratch image has none) or the engine host's
	// own `/proc` (unreachable when the engine runs in a virtual machine).
	//
	// The Provider_*Stacks operations (availableStacks, webAppStacks,
	// functionAppStacks and their per-location spellings — 6 in all) stay
	// unserved deliberately: the runtime-stack catalog is Microsoft-published
	// data that would need a vendored catalog, like the WAF rule sets; the
	// decision is recorded in DO_NEXT.md.
	"web-arm-openapi-2025-03-01": 519,
}

// ---------------------------------------------------------------------------
// Route table
// ---------------------------------------------------------------------------

// azureSimRoute is one registered simulator route: the method plus the pattern
// split into segments exactly as registered (original casing, Go 1.22 mux
// wildcards such as "{name}", "{path...}" and "{$}" intact). The casing
// matters — http.ServeMux compares literal segments byte-for-byte, so a probe
// synthesized from a lowercased template would miss "Microsoft.App".
type azureSimRoute struct {
	method string
	raw    []string
}

// rawAzureSegs splits a path into segments without normalizing anything:
// casing and parameter names survive. splitAzureSegs is its lossy sibling,
// used for shape comparison in the route-validity gate.
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
	// Probe the most specific routes first: the mux resolves a concrete path
	// to its most specific pattern, so a candidate built from a route with
	// many literal segments is the one most likely to reach a handler.
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

// azureIsGreedyWildcard reports a Go 1.22 mux "{name...}" tail wildcard, which
// absorbs every remaining segment.
func azureIsGreedyWildcard(s string) bool {
	return azureIsWildcard(s) && strings.HasSuffix(s[:len(s)-1], "...")
}

// ---------------------------------------------------------------------------
// Request synthesis
// ---------------------------------------------------------------------------

const (
	azureProbeSubscription  = "00000000-0000-0000-0000-000000000000"
	azureProbeResourceGroup = "sim-coverage-rg"
	azureProbeLocation      = "eastus"
)

// azureProbeParamValue picks a plausible concrete value for one swagger path
// parameter. The identity-bearing ARM parameters get values of the right shape
// (a GUID subscription, a resource-group name, a region) because handlers
// routinely parse them; everything else gets a stable simulator-scoped name.
// The value never decides routing — http.ServeMux wildcards accept any
// non-empty segment — but a well-shaped value keeps a handler from rejecting
// the request for a reason unrelated to whether it is mounted.
func azureProbeParamValue(param string) string {
	name := strings.Trim(param, "{}")
	name = strings.TrimSuffix(name, "...")
	lower := strings.ToLower(name)
	switch {
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
	// A stable, DNS-label-safe name derived from the parameter, so probes for
	// different parameters never collide in the simulator's stores.
	var b strings.Builder
	b.WriteString("sim")
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// azureUnify builds one concrete request path that is simultaneously a valid
// instance of the swagger template and a path the given simulator route
// pattern can match. Per segment:
//
//   - literal vs literal: they must agree (ARM path matching is
//     case-insensitive in the real service, so the comparison is too), and the
//     ROUTE's spelling wins because http.ServeMux is case-sensitive;
//   - route wildcard vs template literal: the wildcard accepts it, so use the
//     template's literal;
//   - anywhere the TEMPLATE has a parameter: the value azureTemplateVariants
//     already chose for that slot, never the route's literal. Borrowing a
//     route literal for a documented parameter is how phantom coverage gets
//     manufactured on this simulator: every cloud host is collapsed onto one
//     port, so the Azure Queue Storage template "PUT /{queueName}" would
//     "unify" with the OCI registry route "PUT /v2/", and the Blob Storage
//     template "PUT /{containerName}/{blob}" with the Cosmos DB route
//     "PUT /offers/{offer}". A documented operation is served only if the
//     simulator answers it for a value the SPECIFICATION allows.
//   - a trailing route "{name...}" absorbs every remaining template segment.
//
// It returns false when the shapes cannot produce a common path at all.
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
			// http.ServeMux compares literals byte-for-byte while ARM path
			// matching is case-insensitive, so the route's spelling wins.
			out = append(out, rs)
			continue
		}
		out = append(out, ss)
	}
	// A trailing "{$}" is satisfied only when the template ended too.
	if i < len(route) && route[i] == "{$}" && j == len(spec) {
		i++
	}
	// A trailing "{name...}" also matches the empty remainder.
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

// azureTemplateVariants turns one swagger template into the concrete segment
// lists worth probing. A path parameter the specification declares as an enum
// has a closed set of legal values — DNS record sets are addressed as
// ".../{zoneName}/A/{relativeRecordSetName}", a storage service as
// ".../blobServices/default" — so the probe uses those values rather than an
// invented name, and produces one variant per value. Every other parameter
// gets its synthesized value, because the operation is documented for an
// arbitrary one.
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

// azureHasScopeLead reports an operation whose FIRST path parameter is an ARM
// scope or resource ID (x-ms-skip-url-encoding) and whose remaining template
// still pins the service — "/{scope}/providers/Microsoft.Authorization/
// roleAssignments/{name}". Those scopes are several segments long, so the slot
// cannot be filled with a single synthesized name.
//
// The literal-remainder requirement is what keeps this from re-opening the
// fan-in door. A template that is nothing BUT a resource ID — Azure Resource
// Manager's generic "GET /{resourceId}" — would otherwise absorb any route at
// all and report every generic-resource operation as served; with no literal
// left to pin the service, the probe leaves it to the ordinary path, where the
// simulator's answer decides.
func azureHasScopeLead(sp swaggerPath) bool {
	if len(sp.Raw) < 2 || !azureIsWildcard(sp.Raw[0]) {
		return false
	}
	if !sp.PathScopes[strings.Trim(sp.Raw[0], "{}")] {
		return false
	}
	for _, s := range sp.Raw[1:] {
		if !azureIsWildcard(s) {
			return true
		}
	}
	return false
}

// azureUnifyScoped unifies a scope-led template against one route by letting
// the scope slot absorb as many leading route segments as the shapes allow. The
// absorbed segments come from the route itself — "subscriptions/{id}",
// "subscriptions/{id}/resourceGroups/{rg}" — which is legitimate here and only
// here: the specification has declared this slot to hold a resource path, so a
// resource path is exactly what belongs in it.
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

// azureProbeCandidates enumerates the concrete request paths worth trying for
// one documented operation, most specific first and capped so a pathological
// template cannot dominate the run. Candidates are derived from the registered
// routes because those are the only paths a handler can possibly be reached
// at — but deriving a candidate is not evidence of coverage; only the
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
			// No registered pattern can produce a path for this spelling. Probe
			// the plain instantiation anyway: the simulator's answer is the
			// evidence that the operation is unserved, rather than an inference
			// drawn from the route table.
			if !add("/" + strings.Join(variant, "/")) {
				return out
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Probing
// ---------------------------------------------------------------------------

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
	now := time.Now()
	// AzureBearerVerificationMiddleware rejects every /subscriptions/ and
	// /providers/ request that does not carry a simulator-minted Azure
	// Resource Manager token. Probing without one would report the entire ARM
	// surface as "served" by the middleware's 401 and measure nothing.
	token, err := mintAzureSimJWT(simTenantID, "https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint Azure Resource Manager token: %v", err)
	}
	return &azureProber{srv: srv, token: token}
}

// azureProbeResult records one exchange with the running simulator.
type azureProbeResult struct {
	path     string
	status   int
	body     string
	pattern  string // the route pattern the mux resolved the request to, "" for a miss
	served   bool
	panicked bool
	reason   string
}

// serve issues one synthesized request through the simulator's real handler
// chain (CORS → OAuth interception → path cleanup → bearer verification →
// api-version → mux) and classifies the answer.
func (p *azureProber) serve(method, path, query, apiVersion, host string) azureProbeResult {
	url := path + "?api-version=" + apiVersion
	if query != "" {
		url += "&" + query
	}
	var body io.Reader
	hasBody := method == http.MethodPut || method == http.MethodPost || method == http.MethodPatch
	if hasBody {
		// An empty JSON object: enough for a handler to decode, not enough to
		// satisfy a required-property check. Whether it 400s or 201s is
		// immaterial — either answer proves the handler is mounted.
		body = strings.NewReader("{}")
	}
	req := httptest.NewRequest(method, url, body)
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "application/json")
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	if host != "" {
		// The data-plane coordinate. x-ms-version accompanies it because every
		// Azure Storage SDK call carries the REST version it speaks, and the
		// simulator's storage dispatcher reads it as the protocol marker that
		// separates storage traffic from the other services sharing the port.
		req.Host = host
		req.Header.Set("x-ms-version", apiVersion)
	}
	rec := httptest.NewRecorder()

	// Ask the router itself, before anything runs, which registered pattern
	// (if any) owns this exact method + path. http.ServeMux.Handler performs
	// the real routing decision and returns an empty pattern when no route
	// matches — including when the path is registered only for other methods.
	// That is the ground truth for "is a handler mounted here", and unlike
	// re-deriving it from the pattern list it accounts for shadowing between
	// overlapping patterns and for Go's own precedence rules.
	_, pattern := p.srv.Mux().Handler(req)

	res := azureProbeResult{path: path, pattern: pattern}
	func() {
		defer func() {
			if r := recover(); r != nil {
				// A panic proves a handler ran, so the operation IS served —
				// and the panic is a fidelity bug the probe just uncovered.
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

// azureClassify decides whether the exchange proves a handler served the
// operation.
//
// The distinction the whole gate rests on is between "nothing is mounted here"
// and "a handler ran and told me the resource is absent". Both answer 404. Only
// the first means the operation is unimplemented — the second is the operation
// working correctly against a resource the probe deliberately never created.
func azureClassify(pattern string, rec *httptest.ResponseRecorder) (served bool, reason string) {
	if gap, why := azureDeclaredGap(rec); gap {
		return false, why
	}
	if pattern == "" {
		// The router matched nothing. Either Go's ServeMux answered on its own
		// (404 "page not found", or 405 when the path exists for other methods
		// only) — NOT covered, no simulator code ran at all — or a middleware
		// ahead of the mux answered first, which is a served surface.
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
	// Covered: the router resolved the request to a registered pattern and that
	// handler answered. A 404 here is a handler reporting an absent resource; a
	// 400 is a handler rejecting a malformed or incomplete body; 401/403 is a
	// data-plane authorization check; 409 a conflicting resource state; 2xx a
	// success. Every one of them required simulator code to run.
	return true, fmt.Sprintf("%s answered %d", pattern, rec.Code)
}

// azureDeclaredGap reports the responses in which the simulator says, in its
// own words, that it has no handler for this operation. The host-addressed data
// planes (Blob/File/Queue/Table on *.<service>.<host>, Key Vault on
// *.vault.<host>, Service Bus on *.servicebus.<host>) dispatch inside one
// middleware instead of through the mux, so their "nothing is mounted here" is
// a structured error rather than the mux's plain-text 404 — the same fact,
// spoken differently. Counting a self-declared gap as coverage is exactly the
// overstatement this gate exists to prevent.
func azureDeclaredGap(rec *httptest.ResponseRecorder) (bool, string) {
	if rec.Code == http.StatusNotImplemented {
		return true, "handler answered 501 NotImplemented"
	}
	code := rec.Header().Get("x-ms-error-code")
	var message string
	if code != "" {
		// Azure Storage errors are XML bodies with the machine-readable code in
		// this header, exactly as the real service emits them.
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
		// The uniform sentinel every host-addressed dispatcher writes when the
		// method+path combination is not one of the operations it recognizes.
		return true, "dispatcher does not recognize the operation (MethodNotAllowed)"
	case strings.Contains(message, "not implemented"):
		// A dispatcher's default arm naming the surface it does not implement.
		return true, "dispatcher declared the operation not implemented"
	}
	return false, ""
}

// azureProbeAPIVersion is the api-version a probe sends. ARM requests are
// rejected outright without one (AzureARMAPIVersionMiddleware), so it comes
// from the document being measured.
func azureProbeAPIVersion(sp swaggerPath) string {
	if sp.APIVersion != "" {
		return sp.APIVersion
	}
	return "2024-01-01"
}

// azureDataPlaneHosts maps a vendored data-plane document to the Host a real
// client addresses that service at. Azure publishes these planes on
// per-resource hostnames — myaccount.blob.core.windows.net,
// myvault.vault.azure.net, myns.servicebus.windows.net — and the simulator
// serves them the same way, from WrapHandler middlewares that dispatch on the
// host rather than from mux patterns. The host is a coordinate, exactly like
// the endpoint URL an SDK is pointed at; a probe that omits it is asking the
// Azure Resource Manager plane about a blob operation and will be told, quite
// correctly, that nothing is mounted there.
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

// azureCoverage probes every documented operation once and returns, per
// vendored swagger file, how many the simulator actually serves. Findings
// collects the fidelity problems the probing exposed — handlers that are
// mounted but panic or fail with a server error.
type azureCoverage struct {
	impl     map[string]int
	total    map[string]int
	unserved map[string][]string
	findings []string
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
	// Probe in a fixed order — the count this gate locks must not depend on Go's
	// map iteration. Reads run before writes and deletes run last, so a resource
	// a create probe brought into being is torn down by the delete probe for the
	// same template rather than left behind for the rest of the run.
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

	// One operation can appear in several vendored documents (api-version
	// variants of the same service). Probe each distinct coordinate once and
	// share the verdict.
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
		case res.panicked:
			cov.impl[name]++
			cov.findings = append(cov.findings,
				fmt.Sprintf("%s: %s → %s (%s)", name, label, res.path, res.reason))
		case res.status >= 500 && !azureHostCapabilityRefusal(res.body):
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
	return cov
}

// azureHostCapabilityRefusal reports the simulator refusing to realize real
// network fabric because the HOST cannot provide the Linux namespace, bridge,
// veth, route and nftables capabilities it needs (azureRequireNetworkHost).
// That is a property of the machine the test runs on — macOS can never grant
// them — not a defect in the handler, which is mounted and did exactly the
// right thing. On a host that does have the capabilities the response cannot
// occur, so this carve-out never hides a real 5xx.
func azureHostCapabilityRefusal(body string) bool {
	return strings.Contains(body, "missing real-execution host capabilities")
}

func azureTruncate(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// Gates
// ---------------------------------------------------------------------------

// TestServiceConformance_AzureCoverage reports per-Swagger-file coverage
// (informational — never fails) so the implemented fraction is visible.
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
	}
	t.Logf("TOTAL: %d/%d Azure Swagger operations served by a mounted handler", ti, tt)
}

// TestServiceConformance_AzureCoverageFloor locks each Swagger document's
// implemented-operation count: an exact-equality ratchet (a drop is a
// regression; more requires bumping the floor).
func TestServiceConformance_AzureCoverageFloor(t *testing.T) {
	cov := azureProbeCoverage(t)
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

// TestServiceConformance_AzureProbedHandlersAreHealthy fails on the fidelity
// problems probing exposes in handlers that ARE mounted: a panic, or a 5xx.
// A mounted-but-broken handler still counts toward coverage — it is reached —
// so without this gate the coverage number would hide it.
func TestServiceConformance_AzureProbedHandlersAreHealthy(t *testing.T) {
	cov := azureProbeCoverage(t)
	if len(cov.findings) > 0 {
		t.Errorf("%d mounted handler(s) failed a synthesized request (panic or 5xx) — each is a fidelity bug:\n  %s",
			len(cov.findings), strings.Join(cov.findings, "\n  "))
	}
}
