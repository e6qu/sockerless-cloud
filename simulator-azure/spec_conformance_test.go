package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Every route the simulator registers must be a real Azure API path, checked
// against the Swagger 2.0 specs vendored under specs/cloud-api/azure/ (refresh
// with scripts/fetch-azure-spec.sh). The host-addressed data planes attach
// through WrapHandler middlewares, not mux patterns, so the SDK suites cover
// them instead.

type swaggerPath struct {
	Method string
	Segs   []string
	// Raw keeps casing and parameter names ("{resourceGroupName}"), which Segs
	// normalizes away; the coverage probe needs them to synthesize a routable
	// request.
	Raw []string
	// The api-version every Azure Resource Manager request must carry.
	APIVersion string
	// The closed value set a path parameter declares ("recordType" → A, AAAA,
	// CNAME, …), the only legal spellings of the operation.
	PathEnums map[string][]string
	// Parameters marked x-ms-skip-url-encoding: scopes and full resource IDs
	// spanning several segments.
	PathScopes map[string]bool
	// The x-ms-paths query discriminator ("restype=container&comp=list"). On the
	// storage and Cosmos DB data planes the query selects the operation, so the
	// probe keeps it; routing ignores it, so the route gates use the path alone.
	Query string
}

// Where the value goes, its name, its enum, and the local "#/parameters/..."
// reference form.
type swaggerParameter struct {
	Name string   `json:"name"`
	In   string   `json:"in"`
	Enum []string `json:"enum"`
	Ref  string   `json:"$ref"`
	// Azure sets x-ms-skip-url-encoding precisely on parameters whose value is a
	// resource path spanning several segments.
	SkipURLEncoding bool `json:"x-ms-skip-url-encoding"`
}

// A "$ref" points at the document's own parameters; cross-document references
// are the shared ARM parameters, none enumerated.
func swaggerPathEnums(defs map[string]swaggerParameter, raw json.RawMessage) (enums map[string][]string, scopes map[string]bool) {
	if len(raw) == 0 {
		return nil, nil
	}
	var list []swaggerParameter
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, nil
	}
	enums, scopes = map[string][]string{}, map[string]bool{}
	for _, p := range list {
		if p.Ref != "" {
			name, ok := strings.CutPrefix(p.Ref, "#/parameters/")
			if !ok {
				continue
			}
			p, ok = defs[name]
			if !ok {
				continue
			}
		}
		if p.In != "path" || p.Name == "" {
			continue
		}
		if len(p.Enum) > 0 {
			enums[p.Name] = p.Enum
		}
		if p.SkipURLEncoding {
			scopes[p.Name] = true
		}
	}
	return enums, scopes
}

func loadSwaggerPaths(t *testing.T) (all []swaggerPath, byFile map[string][]swaggerPath) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "specs", "cloud-api", "azure", "*.swagger.json.gz"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no vendored Swagger specs found (glob err: %v) — run scripts/fetch-azure-spec.sh", err)
	}

	byFile = map[string][]swaggerPath{}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			t.Fatalf("open %s: %v", p, err)
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatalf("gunzip %s: %v", p, err)
		}
		var doc struct {
			BasePath string `json:"basePath"`
			Info     struct {
				Version string `json:"version"`
			} `json:"info"`
			Parameters map[string]swaggerParameter           `json:"parameters"`
			Paths      map[string]map[string]json.RawMessage `json:"paths"`
			XMSPaths   map[string]map[string]json.RawMessage `json:"x-ms-paths"`
		}
		err = json.NewDecoder(gz).Decode(&doc)
		_ = gz.Close()
		_ = f.Close()
		if err != nil {
			t.Fatalf("decode %s: %v", p, err)
		}

		file := filepath.Base(p)
		// The Log Analytics query plane lives under /v1
		// (https://api.loganalytics.io/v1/...), which its swagger expresses through
		// the host version rather than basePath.
		if doc.BasePath == "" && strings.HasPrefix(file, "monitor-dataplane-operationalinsights-v1") {
			doc.BasePath = "/v1"
		}
		// The Event Grid publish swagger folds the URL into
		// x-ms-parameterized-host and keys x-ms-paths by query overloads
		// ("?overload=cloudEvent"); the publish URL is
		// https://<topic>.<region>.eventgrid.azure.net/api/events.
		if strings.HasPrefix(file, "eventgrid-dataplane-") {
			doc.BasePath = "/api/events"
		}
		add := func(rawPath string, methods map[string]json.RawMessage) {
			// Routing matches the path part of a "/path?comp=list" key; the probe replays
			// the query.
			query := ""
			if i := strings.Index(rawPath, "?"); i >= 0 {
				query = rawPath[i+1:]
				rawPath = rawPath[:i]
			}
			full := rawPath
			if doc.BasePath != "" && doc.BasePath != "/" {
				full = strings.TrimSuffix(doc.BasePath, "/") + "/" + strings.TrimPrefix(rawPath, "/")
			}
			segs := splitAzureSegs(full)
			raw := rawAzureSegs(full)
			// Path-item parameters apply to every operation under the key.
			sharedEnums, sharedScopes := swaggerPathEnums(doc.Parameters, methods["parameters"])
			for method, rawOp := range methods {
				switch method {
				case "get", "put", "post", "patch", "delete", "head", "options":
					enums := map[string][]string{}
					scopes := map[string]bool{}
					for k, v := range sharedEnums {
						enums[k] = v
					}
					for k := range sharedScopes {
						scopes[k] = true
					}
					var op struct {
						Parameters json.RawMessage `json:"parameters"`
					}
					if json.Unmarshal(rawOp, &op) == nil {
						opEnums, opScopes := swaggerPathEnums(doc.Parameters, op.Parameters)
						for k, v := range opEnums {
							enums[k] = v
						}
						for k := range opScopes {
							scopes[k] = true
						}
					}
					sp := swaggerPath{
						Method:     strings.ToUpper(method),
						Segs:       segs,
						Raw:        raw,
						APIVersion: doc.Info.Version,
						PathEnums:  enums,
						PathScopes: scopes,
						Query:      query,
					}
					all = append(all, sp)
					byFile[file] = append(byFile[file], sp)
				}
			}
		}
		for rawPath, methods := range doc.Paths {
			add(rawPath, methods)
		}
		for rawPath, methods := range doc.XMSPaths {
			add(rawPath, methods)
		}
	}
	return all, byFile
}

// spec_validator.go holds the shared matcher.

func matchesAnySwagger(specs []swaggerPath, method, path string) bool {
	simSegs := splitAzureSegs(path)
	for _, s := range specs {
		if s.Method == method && matchAzureSegs(simSegs, s.Segs, true) {
			return true
		}
	}
	return false
}

// Real, documented Azure surfaces with no swagger in azure-rest-api-specs.
// Each entry must be justified, never a place to hide an invented path.
var allowedNonSpecAzurePrefixes = map[string]string{
	// Cosmos DB SQL data plane: documented REST API, no swagger.
	"/dbs": "Cosmos DB SQL data plane (documented REST API, no upstream swagger)",

	// Microsoft Graph v1.0 and beta, which clients mix
	// (terraform-provider-azuread reads beta-only members). Graph's OpenAPI lives
	// in microsoftgraph/msgraph-metadata, so neither is checked here.
	"/v1.0/": "Microsoft Graph v1.0 surface (spec lives in msgraph-metadata)",
	"/beta/": "Microsoft Graph beta surface (spec lives in msgraph-metadata)",

	// The managed-identity token via IMDS, App Service MSI (/msi/token), and the
	// /metadata/endpoints bootstrap terraform's metadata_host reads; none appear
	// in azure-rest-api-specs.
	"/metadata/identity/": "IMDS managed-identity token endpoint",
	"/msi/token":          "App Service MSI token endpoint",
	"/metadata/endpoints": "ARM cloud-environment metadata bootstrap (terraform metadata_host)",

	// The vendored 2021-05 Service Bus data-plane swagger covers only ATOM entity
	// management; the send/receive/lock REST operations are documented but
	// unswaggered.
	"/{entity}/messages": "ServiceBus HTTP message send/receive (documented, no upstream swagger)",
}

// Exact patterns. Microsoft Entra token endpoints are intercepted ahead of the
// mux; Azure Container Registry's /oauth2/* routes match its data-plane
// swagger.
var allowedNonSpecAzureRoutes = map[string]string{
	// The NAT gateway egress sidecar's channel to the simulator, under a token the
	// simulator minted; no cloud client sees it.
	"POST /sockerless/sidecar/egress": "egress sidecar reports (simulator-internal, token-bound)",
	// Clients poll whatever URL Azure-AsyncOperation or Location carries; the URL
	// shape is service-internal and in no swagger.
	"GET /subscriptions/{subscriptionId}/providers/Microsoft.App/locations/{location}/operationStatuses/{opId}":                       "sim-emitted LRO polling URL (Azure-AsyncOperation)",
	"GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/locations/{location}/deletedVaults/{name}/purge/operation":      "sim-emitted Azure Key Vault purge LRO polling URL (documented Location header)",
	"GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/locations/{location}/deletedManagedHSMs/{name}/purge/operation": "sim-emitted Azure Managed HSM purge LRO polling URL (documented Location header)",
	"GET /subscriptions/{subscriptionId}/providers/{provider}/locations/{location}/operationResults/{opId}":                           "sim-emitted LRO polling URL (Location header)",
	"GET /subscriptions/{subscriptionId}/providers/{provider}/locations/{location}/operationStatuses/{opId}":                          "sim-emitted LRO polling URL (Azure-AsyncOperation)",
	"GET /subscriptions/{subscriptionId}/operationresults/{operationId}":                                                              "sim-emitted Azure Resource Manager LRO polling URL (resource group delete's Location header)",
	"GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/operations/{opId}":                          "sim-emitted Compute LRO polling URL (Azure-AsyncOperation, and Location with monitor=true, as armcompute's recordings carry)",
	"GET /eventsubscriptions/{eventSubscriptionName}/validate":                                                                        "sim-emitted Event Grid validationUrl a SubscriptionValidationEvent carries for manual validation",

	// The Compute VirtualMachineImages_* and Images_* documents
	// (virtualMachineImage.json, image.json) are not vendored; the routes follow
	// their templates verbatim.
	"GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/publishers":                                                                                     "VirtualMachineImages_ListPublishers (ComputeRP virtualMachineImage.json, not yet vendored)",
	"GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/publishers/{publisherName}/artifacttypes/vmimage/offers":                                        "VirtualMachineImages_ListOffers (ComputeRP virtualMachineImage.json, not yet vendored)",
	"GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/publishers/{publisherName}/artifacttypes/vmimage/offers/{offer}/skus":                           "VirtualMachineImages_ListSkus (ComputeRP virtualMachineImage.json, not yet vendored)",
	"GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/publishers/{publisherName}/artifacttypes/vmimage/offers/{offer}/skus/{skus}/versions":           "VirtualMachineImages_List (ComputeRP virtualMachineImage.json, not yet vendored)",
	"GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/publishers/{publisherName}/artifacttypes/vmimage/offers/{offer}/skus/{skus}/versions/{version}": "VirtualMachineImages_Get (ComputeRP virtualMachineImage.json, not yet vendored)",
	"PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/images/{imageName}":                                                               "Images_CreateOrUpdate (ComputeRP image.json, not yet vendored)",
	"PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/images/{imageName}":                                                             "Images_Update (ComputeRP image.json, not yet vendored)",
	"GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/images/{imageName}":                                                               "Images_Get (ComputeRP image.json, not yet vendored)",
	"DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/images/{imageName}":                                                            "Images_Delete (ComputeRP image.json, not yet vendored)",
	"GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/images":                                                                           "Images_ListByResourceGroup (ComputeRP image.json, not yet vendored)",
	"GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/images":                                                                                                              "Images_List (ComputeRP image.json, not yet vendored)",

	// listLogSasUrl returns an opaque log URL (Azure Container Registry hands out
	// a storage SAS URL), which clients fetch verbatim.
	"GET /acr/v1/logs/{runId}": "sim-emitted ACR Tasks run-log link (real API returns an opaque log SAS URL)",

	// The APIs return an opaque WebSocket URI (webSocketUri, execEndpoint) clients
	// connect to verbatim; the simulator shapes its own.
	"GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ContainerInstance/containerGroups/{containerGroupName}/containers/{containerName}/execSessions/{sessionID}":   "sim-emitted exec WebSocket bridge (real API returns an opaque webSocketUri)",
	"GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ContainerInstance/containerGroups/{containerGroupName}/containers/{containerName}/attachSessions/{sessionID}": "sim-emitted attach WebSocket bridge (real API returns an opaque webSocketUri)",

	// An app's eventStreamEndpoint names a data-plane host serving these with a
	// getAuthToken bearer; no swagger describes them.
	"GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/containerApps/{appName}/eventstream":                                                                          "Container Apps system event stream under the app's eventStreamEndpoint (data plane, unswaggered)",
	"GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/containerApps/{appName}/revisions/{revisionName}/replicas/{replicaName}/containers/{containerName}/logstream": "Container Apps console log stream under the app's eventStreamEndpoint (data plane, unswaggered)",
	// az containerapp replica show/list sends the replica GET with a trailing
	// slash.
	"GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/containerApps/{appName}/revisions/{revisionName}/replicas/{replicaName}/{$}": "the Azure CLI's trailing-slash spelling of the documented Replicas_GetReplica path",
}

// Vendored only as $ref targets of implemented documents; their own paths are
// services the simulator does not implement.
var vendoredForRefClosure = map[string]bool{
	"cosmos-db-arm-privateendpointconnection-2021-10-15.swagger.json.gz": true,
	"cosmos-db-arm-privateendpointconnection-2024-08-15.swagger.json.gz": true,
}

// Served through host-addressed WrapHandler middlewares, which the static
// route gate cannot see; the SDK suites exercise them.
var vendoredHostRoutedDataPlane = map[string]bool{
	"keyvault-dataplane-certificates-2025-07-01.swagger.json.gz": true,
	"keyvault-dataplane-keys-2025-07-01.swagger.json.gz":         true,
	"keyvault-dataplane-secrets-2025-07-01.swagger.json.gz":      true,
}

func TestRoutesExistInSwaggerSpecs(t *testing.T) {
	all, byFile := loadSwaggerPaths(t)
	_ = byFile
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	// Long-running operations complete in a goroutine; one still running when
	// this test ends would race the next test's store rebuild.
	t.Cleanup(bg.Await)

	var offenders []string
	for _, pattern := range srv.RoutePatterns() {
		if pattern == "GET /health" {
			continue // shared simulator health endpoint
		}
		if _, ok := allowedNonSpecAzureRoutes[pattern]; ok {
			continue
		}
		method, path, found := strings.Cut(pattern, " ")
		if !found {
			offenders = append(offenders, pattern+"  (method-less pattern)")
			continue
		}
		prefixAllowed := false
		for prefix := range allowedNonSpecAzurePrefixes {
			if strings.HasPrefix(path, prefix) {
				prefixAllowed = true
				break
			}
		}
		if prefixAllowed {
			continue
		}
		if !matchesAnySwagger(all, method, path) {
			offenders = append(offenders, pattern)
		}
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("%d registered route(s) do not exist in the vendored Azure Swagger specs (invented path, wrong path shape, or a real-but-unswaggered surface that needs a justified allowlist entry):\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// Pure-definitions files (common-types, CommonDefinitions) carry no paths and
// are exempt.
func TestVendoredSwaggerSpecsAreConsumed(t *testing.T) {
	_, byFile := loadSwaggerPaths(t)
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	// Long-running operations complete in a goroutine; one still running when
	// this test ends would race the next test's store rebuild.
	t.Cleanup(bg.Await)

	used := map[string]bool{}
	for _, pattern := range srv.RoutePatterns() {
		method, path, found := strings.Cut(pattern, " ")
		if !found {
			continue
		}
		for file, specs := range byFile {
			if !used[file] && matchesAnySwagger(specs, method, path) {
				used[file] = true
			}
		}
	}

	var stale []string
	for file, specs := range byFile {
		if len(specs) == 0 {
			continue // pure-definitions file ($ref target)
		}
		if vendoredForRefClosure[file] || vendoredHostRoutedDataPlane[file] {
			continue
		}
		if !used[file] {
			stale = append(stale, file)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("%d vendored swagger file(s) with paths are not referenced by any registered route (stale vendor, wrong api-version, or a data plane outside the mux — move those to the documented exemptions):\n  %s",
			len(stale), strings.Join(stale, "\n  "))
	}
}
