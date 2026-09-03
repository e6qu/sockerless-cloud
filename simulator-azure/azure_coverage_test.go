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
// azureDeclaredOperationTotals locks each vendored Swagger document's
// declared operation count. The served floor below cannot see the failure
// mode this closes: a re-vendored document that ADDS operations leaves every
// served count unchanged, so the floors stay green while the new operations
// sit silently unserved — exactly how forty-three AWS operations drifted
// unnoticed between 2026-08-12 and 2026-08-23 before that simulator's model
// drift gate existed. A changed total fails here and forces the decision:
// serve the new operations, or record why not — then update both tables
// together.
var azureDeclaredOperationTotals = map[string]int{
	"apimanagement-arm-apimapis-2022-08-01":                           91,
	"apimanagement-arm-apimbackends-2022-08-01":                       7,
	"apimanagement-arm-apimdeletedservices-2022-08-01":                3,
	"apimanagement-arm-apimdeployment-2022-08-01":                     15,
	"apimanagement-arm-apimnamedvalues-2022-08-01":                    8,
	"apimanagement-arm-apimproducts-2022-08-01":                       31,
	"apimanagement-arm-apimsubscriptions-2022-08-01":                  9,
	"app-arm-containerapps-2025-01-01":                                11,
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

var azureMethodFloor = map[string]int{
	"apimanagement-arm-apimapis-2022-08-01":             91,
	"apimanagement-arm-apimbackends-2022-08-01":         7,
	"apimanagement-arm-apimdeletedservices-2022-08-01":  3,
	"apimanagement-arm-apimdeployment-2022-08-01":       15,
	"apimanagement-arm-apimnamedvalues-2022-08-01":      8,
	"apimanagement-arm-apimproducts-2022-08-01":         31,
	"apimanagement-arm-apimsubscriptions-2022-08-01":    9,
	"app-arm-containerapps-2025-01-01":                  11,
	"app-arm-jobs-2025-01-01":                           12,
	"app-arm-managedenvironments-2025-01-01":            19,
	"app-arm-managedenvironmentsstorages-2025-01-01":    4,
	"applicationinsights-arm-components_api-2020-02-02": 8,
	// A component's billing plan decides what it is entitled to: the Enterprise
	// plan carries continuous export and the higher burst, the Basic one does
	// not, so the capabilities are a read of the plan rather than a fixed
	// answer. The available features are the plans the component could be on
	// with the one it is on marked — a choice, not a published price list. The
	// quota status compares the telemetry the application actually wrote
	// against the cap the component set.
	//
	// Raised from 2 by those three, completing the document.
	"applicationinsights-arm-featuresandpricing-2015-05-01": 5,
	// An application's telemetry is the log store its workload writes into, and
	// Application Insights is a view onto the same store Log Analytics queries,
	// addressed by app id instead of workspace id. So the query, the events and
	// the metrics all read through the one engine and all move when the
	// application writes: an event is a row of the table its type names, and a
	// metric is that table counted.
	//
	// Raised from 1 by nine operations and a correction. The one that counted
	// as served answered a fixed empty result set and ignored the query it was
	// given — served, and fake.
	"applicationinsights-dataplane-appinsights-v1-preview": 10,
	// Lowered from 9. GET/PUT/DELETE "/{roleAssignmentId}" — the full-resource-ID
	// spelling — probe to a plain mux miss: no registered pattern serves a bare
	// resource ID. The old count matched them by shape against the scoped
	// "/{scope}/.../roleAssignments/{name}" routes.
	"authorization-arm-authorization-roleassignmentscalls-2022-04-01": 10,
	// Lowered from 5. GET "/{roleId}" and the two ".../Microsoft.Authorization/
	// permissions" list operations mux-miss; nothing is registered for either.
	"authorization-arm-authorization-roledefinitionscalls-2022-04-01": 7,
	// Microsoft.Compute's operation catalog is the provider's own surface
	// expressed as role-assignable actions, derived from the vendored documents
	// and held to that derivation by TestComputeOperationCatalogCoversSpec — a
	// re-vendor that adds an operation fails until the catalog names the action
	// it needs. The per-location usage is counted rather than declared: the
	// figures are the machines and cores the subscription is actually holding
	// there, against the subscription's own quotas.
	//
	// Raised from 1 by those two, completing the document.
	"compute-arm-computerpcommon-2022-03-01":                 3,
	"compute-arm-skus-2021-07-01":                            1,
	"compute-arm-virtualmachine-2022-03-01":                  29,
	"containerinstance-arm-containerinstance-2021-10-01":     18,
	"containerregistry-arm-containerregistry-2023-07-01":     52,
	"containerregistry-arm-containerregistry-2025-11-01":     58,
	"containerregistry-arm-registrytasks-2019-06-01-preview": 25,
	// Raised from 13: the "/acr/v1/{path...}" registry routes do serve these,
	// which shape matching missed where a route parameter sat under a spec
	// literal. Raised again from 19 by the registry's token service growing
	// the spec's "GET /oauth2/token"
	// (Authentication_GetAcrAccessTokenFromLogin) — the Docker Registry v2
	// token endpoint that trades a Basic admin credential for the scoped
	// access token the data plane now requires.
	// The registry's properties APIs describe what the registry holds — the
	// manifests it stores, the tags pointing at them, the size of each manifest
	// document, the platform its image config declares, and when it was pushed
	// (which the shared OCI store now stamps, because a registry knows when it
	// received a manifest). The only state they add is the four changeable
	// attributes a client sets, which the data plane then honours: a tag or
	// repository with deletion disabled is refused.
	//
	// Raised from 24 by all nine of them. Five were unserved outright; the other
	// four counted as served while answering a bare 404 — the GET handler on the
	// shared /acr/v1 path served only the tag list and fell through for
	// everything else, which the probe reads as an answer. That is the phantom
	// coverage the Google Cloud gate exists for, found here by reading the
	// handler rather than the number.
	"containerregistry-dataplane-containerregistry-2021-07-01": 29,
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
	// An extension topic is not a resource a client creates: it is the name
	// Event Grid gives the events a resource already emits, so it is derived
	// from the scope addressed and names the system topic whose source is that
	// resource, when the subscription holds one.
	//
	// Raised from 60 by ExtensionTopics_Get, completing the document.
	"eventgrid-arm-eventgrid-2021-12-01": 61,
	// Lowered from 127, for the same unserved extensionTopics operation.
	// The same extension topic, in this document's api-version.
	//
	// Raised from 126 by ExtensionTopics_Get, completing the document.
	"eventgrid-arm-eventgrid-2022-06-15":         127,
	"eventgrid-dataplane-eventgrid-2018-01-01":   3,
	"eventhub-arm-authorizationrules-2024-01-01": 15,
	"eventhub-arm-consumergroups-2024-01-01":     4,
	"eventhub-arm-eventhubs-2024-01-01":          4,
	"eventhub-arm-namespaces-2024-01-01":         14,
	"eventhub-arm-networkrulessets-2024-01-01":   3,
	// The instance metadata service attests the instance it is asked on and
	// names the tenant its managed identity belongs to. The attestation is a
	// real signature over the instance's identity and the caller's nonce, made
	// with the simulator's own signing key — the coordinate difference from
	// Azure, whose key chains to a Microsoft root, is which key a verifier
	// trusts, not whether the document is signed.
	//
	// Raised from 2 by those two, completing the document.
	"imds-dataplane-imds-2021-02-01":   4,
	"keyvault-arm-keyvault-2023-07-01": 17,
	// Raised from 6: the deleted-pool collection and purge, name availability,
	// the private endpoint connections and private-link resources, and the
	// regions listing. A delete retires a pool that carries soft delete, and
	// purge protection refuses the purge.
	"keyvault-arm-managedhsm-2023-07-01":               16,
	"keyvault-dataplane-certificates-2025-07-01":       27,
	"keyvault-dataplane-keys-2025-07-01":               25,
	"keyvault-dataplane-secrets-2025-07-01":            12,
	"logic-arm-logic-2019-05-01":                       106,
	"monitor-dataplane-datacollectionrules-2023-01-01": 1,
	// The resource-centric query is the workspace query addressed by the Azure
	// resource whose logs are read. Serving it needed the coverage probe
	// generalised: its scope unifier only handled a scope in the first segment,
	// and a data plane puts its api-version in front — "/v1/{resourceId}/query"
	// — so the probe collapsed a whole resource ID into one synthetic segment.
	//
	// Raised from 5 by those two, completing the document.
	"monitor-dataplane-operationalinsights-v1":        7,
	"msi-arm-managedidentity-2024-11-30":              12,
	"network-arm-applicationgateway-2025-03-01":       22,
	"network-arm-applicationsecuritygroup-2025-03-01": 6,
	"network-arm-loadbalancer-2025-03-01":             27,
	"network-arm-natgateway-2025-03-01":               6,
	"network-arm-networkinterface-2025-03-01":         15,
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
	"network-arm-networkwatcher-2025-03-01":        35,
	"network-arm-privateendpoint-2025-03-01":       11,
	"network-arm-privatelinkservice-2025-03-01":    13,
	"network-arm-publicipaddress-2025-03-01":       9,
	"network-arm-publicipprefix-2025-03-01":        6,
	"network-arm-routetable-2025-03-01":            10,
	"network-arm-serviceendpointpolicy-2025-03-01": 10,
	"network-arm-virtualnetwork-2025-03-01":        21,
	"network-arm-virtualnetworktap-2025-03-01":     6,
	// A workspace's shared keys are its own pair, minted on first use and
	// replaced by a regeneration, so the keys read back after one are not the
	// keys read back before. They used to be a constant, which made a
	// regeneration unobservable.
	//
	// Raised from 1 by SharedKeys_Regenerate, completing the document.
	"operationalinsights-arm-sharedkeys-2020-08-01": 2,
	"operationalinsights-arm-workspaces-2020-08-01": 8,
	"postgresql-arm-openapi-2025-08-01":             66,
	"privatedns-arm-privatedns-2024-06-01":          17,
	"redis-arm-redis-2024-11-01":                    41,
	// Lowered from 36. The generic-resource operations — the five methods on
	// "/{resourceId}" and the five on ".../providers/{ns}/{parentResourcePath}/
	// {type}/{name}" — all mux-miss. Their templates are almost pure parameters,
	// so under shape matching they unified with unrelated typed routes.
	"resources-arm-resources-2021-04-01":                40,
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
	// A storage account's migrations and its point-in-time blob restore each
	// change the account or its blobs rather than reporting that they would: a
	// customer-initiated migration moves the account to the SKU it names, the
	// hierarchical-namespace migration turns the namespace on (and its
	// validation request deliberately does not), and a blob-range restore takes
	// the blobs in the ranges it covers back to the instant it names — one
	// deleted after that instant comes back, one written after it goes away.
	// The deletion and modification times a blob records carry seconds, because
	// that is the precision of the header they ride in, so the restore point is
	// compared at the same precision.
	//
	// Raised from 44 by those five, completing the document.
	"storage-arm-storage-2024-01-01": 49,
	"storage-arm-table-2024-01-01":   8,
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
	// The process-inspection family is served from the process itself. The
	// engine's HTTP API exposes one primitive — `GET /containers/{id}/top`, the
	// `ps` output for the container's processes — and it reports them in the
	// engine host's PID namespace, so where the simulator shares that kernel
	// `/proc/<pid>` is the process's own. ListProcessModules and
	// GetProcessModule read `/proc/<pid>/maps`, folding a file's mappings into
	// one module at the address its lowest mapping begins; GetProcessDump
	// writes an ELF core from those mappings and `/proc/<pid>/mem`, without
	// stopping the process, because reading a process's memory needs
	// permission to trace and not an attach. Both verify the PID against
	// `/proc/<pid>/cmdline` before reading, so a reused PID cannot be reported
	// as the site's. Where the simulator does not share the engine's kernel —
	// the engine in a virtual machine, which is every macOS host — the site's
	// processes are not in this host's /proc and both declare that.
	//
	// The modules read used to answer a fabricated module instead: one entry
	// per process whose base_address was the PID formatted as hex, which is not
	// an address of anything. It was counted as served, so the number said
	// covered while the answer was invented — the shape NoPhantomCoverage
	// exists to catch, arriving through a handler the probe reaches only far
	// enough to see a 404.
	//
	// DeleteProcess (4) stays unserved: the engine can terminate the
	// container's main process, not an arbitrary one inside it.
	//
	// The Provider_*Stacks operations (availableStacks, webAppStacks,
	// functionAppStacks and their per-location spellings — 6 in all) stay
	// unserved deliberately: the runtime-stack catalog is Microsoft-published
	// data that would need a vendored catalog, like the WAF rule sets; the
	// decision is recorded in DO_NEXT.md.
	//
	// Raised from 545 by the two families that were the last recorded App
	// Service deferrals: App Service Environments with Kubernetes Environments
	// (47 operations) and the diagnostics family (24).
	//
	// An App Service Environment is a real placement scope: it occupies a
	// Microsoft.Network subnet that has to exist, leases its outbound address
	// from the same public-IPv4 pool Microsoft.Network/publicIPAddresses
	// reserves from, derives the address it answers on inside its subnet from
	// that subnet's prefix, and reports its front-end count, its Linux-worker
	// flag, its stamp capacity and its app and plan inventory from the pools
	// and the resources placed in it.
	//
	// Five of that family's 48 operations stay unserved, and each answers a
	// declared 501 naming its reason rather than a silent routing 404:
	// ListMultiRoleMetricDefinitions, ListMultiRolePoolInstanceMetricDefinitions,
	// ListWebWorkerMetricDefinitions and ListWorkerPoolInstanceMetricDefinitions
	// would declare Microsoft.Insights metric series for pools the simulator
	// emits no metrics for, and GetOutboundNetworkDependenciesEndpoints answers
	// with Microsoft's published catalog of platform endpoints and address
	// ranges — the same class as the declined Provider_*Stacks. The inbound
	// half IS served: it is computed from the environment's own addresses,
	// subnet and feature switches.
	//
	// The diagnostics family is served for the measurements the simulator can
	// actually make about a site: its workload container's terminal state, the
	// engine's CPU and memory samples with the kernel's own throttling and
	// OOM-kill counters, the container's thread count, the site's restart
	// journal and its deployment records. The detectors of Microsoft's catalog
	// whose inputs do not exist here — service health, slot swaps, worker
	// availability, per-request telemetry, Windows-only counters, auto-heal
	// history — are not listed and answer a declared 501 naming the missing
	// input per detector (web_detectors.go).
	//
	// Raised from 519 by the backup, restore and snapshot family (26
	// operations across the production-site and deployment-slot spellings):
	// the backup configuration, the backup itself, the backup list, status,
	// secrets and delete reads, backup discovery, and the four restore
	// spellings (from a backup item, from a backup blob, from a snapshot, from
	// a deleted app) plus the primary and geo-redundant-secondary snapshot
	// lists. A backup builds a real ZIP of the site's deployed content and
	// writes it, with the XML manifest Microsoft documents beside it, into the
	// Blob data plane of the storage account its SAS URL names; a restore
	// reads that blob back and replaces the site's file system with it, so
	// deleting the archive through the Blob API makes the restore fail.
	//
	// Raised from 646 by the recommendations family (13 of its 15 operations,
	// across the subscription, App Service Environment and site scopes). The
	// simulator runs no advisory engine, which decides what each answers: the
	// lists and the histories are empty because nothing has been observed about
	// anything, and the filters — disable one rule, disable them all, reset —
	// are the client's own decisions and are recorded against the scope. The two
	// remaining operations, GetRuleDetailsByWebApp and
	// GetRuleDetailsByHostingEnvironment, answer a declared 501: a
	// RecommendationRule is Microsoft's published advisory copy (its display
	// name, portal message and blade link), the same class as the declined
	// Provider_*Stacks catalog.
	//
	// Raised from 659 by WebApps_IsCloneable and its slot spelling, which are
	// computed from the site rather than declared: App Service clones an app
	// only from a Premium or Isolated plan, so the plan the site is placed on
	// (read at the time of the question, and inherited from the production site
	// when a slot is asked) decides the result, and the deployment slots a clone
	// would leave behind make it partial.
	//
	// Raised by the four App Service Environment pool metric-definition reads.
	// A pool's definitions are the metric series Microsoft.Insights publishes
	// about it, and this simulator publishes none — which is an answer, not a
	// refusal, and the document spells it as an empty collection. The reason
	// the declared 501 gave was already "it has no metric definitions to
	// declare", which is what the empty collection says; withholding it was
	// keeping back something the simulator knows. The outbound dependency
	// catalog beside them stays declared: Microsoft's own list of platform
	// endpoints and address ranges is not something to invent.
	//
	// Raised by the content migration too, on the same reading. Moving a site's
	// content into an Azure Files share was declined because these sites are
	// served out of a container image rather than out of a share — a primitive
	// the simulator lacks, which is not the same as data it would have to
	// invent. What a caller can observe is the operation the platform starts,
	// and that is state like any other.
	//
	// Raised by the MySQL migration and the two status reads. Moving a site's
	// in-app database out is a request the platform records and then reports
	// on, and both halves are the simulator's: whether the site has in-app
	// MySQL at all is the app setting it already stores, and the migration the
	// caller started is state like any other. No bytes move — there is no MySQL
	// process here whose tables could be copied — but a request against a site
	// with no in-app database is refused exactly as App Service refuses it,
	// before any copying would begin, which is the answer a caller gets.
	//
	// Raised by the six ResourceHealthMetadata spellings, which are read from
	// the sites the scope holds. Only one of the resource's two properties
	// belongs to Microsoft: the category is the one the site matches in the
	// Resource Health Check policy file, which this project does not vendor, so
	// it is left absent — the document does not require it. signalAvailability
	// is a fact about the site, and a site with a workload running is producing
	// the signal Resource Health reads while a site with nothing running is
	// producing none. Declining the whole read over the one field that is
	// Microsoft's would have withheld an answer the simulator has.
	//
	// Raised from 661 by WebApps_ListPerfMonCounters and its slot spelling. A
	// site's counters are what the site is using, and the site's workload
	// container is what is using it, so each counter carries the container
	// engine's own reading — the same source the instance statistics and the
	// diagnostics samples come from. A site with nothing running is measuring
	// nothing and reports no counters, rather than a set of zeroes that would
	// claim a measurement was taken.
	//
	// No App Service operation is a silent gap: every one that remains answers a
	// declared 501 naming what is missing. Beside the catalogs and metric series
	// already listed, those are phplogging (the effective php.ini of a PHP
	// worker that does not run here, whose master values are the platform
	// image's own defaults) and migrate and migratemysql (there is no in-app
	// MySQL database and no content share to move). The six Provider_*Stacks
	// spellings used to miss the router outright and answer a bare 404, which
	// reads as "no such API" rather than "this API exists and its data is not
	// vendored"; they declare it now.
	//
	// Raised from 677 by the four process-dump spellings, which are written
	// from the process's own memory rather than declared.
	"web-arm-openapi-2025-03-01": 681,
}

// Route table

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

// Request synthesis

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
// azureProbeResourceID is the address the probe uses for a path parameter the
// specification marks as a whole resource path. A single synthetic segment is
// not an address any client sends, so an operation addressed that way would be
// probed at a path nothing serves and reported unserved while answering.
const azureProbeResourceID = "subscriptions/" + azureProbeSubscription +
	"/resourceGroups/" + azureProbeResourceGroup +
	"/providers/Microsoft.Web/sites/simprobedsite"

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
	// The marker Azure Resource Manager scopes usually carry is
	// x-ms-skip-url-encoding, because a scope is a resource ID whose slashes
	// must survive. Some documents forget it — Event Grid's extensionTopics
	// declares a bare `scope` — but a leading parameter named `scope` followed
	// by the literal `providers` is an Azure Resource Manager scope by
	// construction, and probing it as one synthetic segment would address the
	// operation at a path no client uses.
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

// Probing

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
	// Long-running operations complete in a goroutine. One still running
	// when this test ends would read and write the stores while the next
	// test rebuilds them.
	t.Cleanup(AwaitAzureAsyncOperations)
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
	// silent holds every unserved operation that did not answer a declared
	// 501. An operation this simulator does not serve has to say so: a routing
	// 404 claims the resource is absent, and a path the probe could not
	// synthesize claims nothing at all. Both read to a client as an answer,
	// and both keep the count at its floor while the reason for the gap has
	// changed underneath it.
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
			if res.status != http.StatusNotImplemented {
				cov.silent = append(cov.silent,
					fmt.Sprintf("%s: %s → probed %s: answered %d, not a declared 501: %s",
						name, label, res.path, res.status, res.reason))
			}
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
	sort.Strings(cov.silent)
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

// Gates

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
		// Name what is missing, so the next operation to serve is readable from
		// an ordinary run rather than only from a floor that has been broken.
		for _, op := range cov.unserved[name] {
			t.Logf("%-60s   unserved: %s", name, op)
		}
	}
	t.Logf("TOTAL: %d/%d Azure Swagger operations served by a mounted handler", ti, tt)
}

// TestServiceConformance_AzureCoverageFloor locks each Swagger document's
// implemented-operation count: an exact-equality ratchet (a drop is a
// regression; more requires bumping the floor).
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

// TestServiceConformance_AzureProbedHandlersAreHealthy fails on the fidelity
// problems probing exposes in handlers that ARE mounted: a panic, or a 5xx.
// A mounted-but-broken handler still counts toward coverage — it is reached —
// so without this gate the coverage number would hide it.
// Every operation this simulator does not serve says so with a declared 501
// naming what is missing. The floor counts unserved operations without caring
// why, so a gap that stopped declaring itself — a route that went away and now
// answers the mux's 404, or one the probe can no longer address — would hold
// the count and lose the declaration. A client cannot tell a 404 that means
// "not implemented here" from one that means "this resource does not exist".
func TestServiceConformance_AzureUnservedOperationsDeclareThemselves(t *testing.T) {
	cov := azureProbeCoverage(t)
	if len(cov.silent) > 0 {
		t.Errorf("%d unserved operation(s) answered something other than a declared 501:\n  %s",
			len(cov.silent), strings.Join(cov.silent, "\n  "))
	}
}

func TestServiceConformance_AzureProbedHandlersAreHealthy(t *testing.T) {
	cov := azureProbeCoverage(t)
	if len(cov.findings) > 0 {
		t.Errorf("%d mounted handler(s) failed a synthesized request (panic or 5xx) — each is a fidelity bug:\n  %s",
			len(cov.findings), strings.Join(cov.findings, "\n  "))
	}
}
