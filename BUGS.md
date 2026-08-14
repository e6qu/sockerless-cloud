# BUGS

Open: 10. Resolved: 8.

## Open

Bugs 2909, 2932, 2646, 2712, 2764, and 1345 moved here with
the simulators from the sockerless monorepo, keeping their IDs
(as did 2924, since resolved).

| ID | Sev | Area | Pattern | One-liner |
|----|-----|------|---------|-----------|
| 2909 | P2 | AWS simulator IAM enforcement leaves 190 served operations authorized against `"*"` | the resource-derivation gap BUG-2907 closed for five services is measured across the rest, not closed for them | Thirty services derive their resource from the types AWS declares and the ARN format published beside each — Amazon Data Firehose, AWS Security Token Service and Application Auto Scaling joined the generated table, Amazon EventBridge gained the alias table its Name/Rule abbreviations needed, Amazon DynamoDB reads the export and import family's TableArn, and the state-resolving tail closed — Amazon SQS cancels a message move against the source queue its task record names, AWS Cloud Map resolves GetOperation through the operation record, and AWS CloudTrail reads the ARN-valued ResourceId and ResourceIdList its tagging operations carry — and the per-request cases that predated the table are gone but for AWS Lambda. 1,784 of the 1,974 served operations that authorize against a resource type derive it; the remaining 190 still request a literal `"*"`. `TestIAMResourceDerivationCoverage` ratchets the number and prints the per-service remainder, largest first: Amazon EC2 (55), AWS Glue (35), Amazon RDS (27), Amazon DynamoDB (18), AWS Systems Manager (17). What is left is mostly an operation that creates its resource, so carries no identifier for it yet, names something other than the resource it authorizes against, or names it by an ARN in a shape the coverage probe cannot express — those derive for real requests and are pinned by `TestIAMResourceARNs_*` behavior tests; the comment beside `iamDerivationCoverageFloor` states each service's remaining class. |
| 2932 | P3 | Three AWS Smithy patterns are stricter than the service they describe, so the simulator cannot satisfy both | the vendored model is authoritative for the simulator, but where it contradicts documented service behavior, matching the model would make the simulator less faithful, not more | The runtime pattern check (BUG-2931) reports three responses whose values AWS itself returns. Amazon EventBridge names the managed secret backing a connection `events!connection/<name>/<uuid>`, and `SecretsManagerSecretArn` admits no `!`. AWS Certificate Manager's `DescribeCertificate` reports the issuing authority as an AWS Private Certificate Authority ARN, and the generic `Arn` shape it is typed with requires the service segment to be `acm`. Amazon CloudWatch Logs reports a configuration template's `resourceType` in CloudFormation spelling (`AWS::WAFv2::WebACL`), and `ResourceType` admits no `:`. Each is allowlisted in `simulator-aws/spec-violation-allowlist.txt` against this entry rather than "fixed" by emitting a value the service never emits. The allowlist shrinks if a later model revision widens the patterns, which is the only thing that should close this. |
| 2764 | P2 | Google Compute Engine Terraform validation on macOS | the guest does not finish booting on a macOS host | Two causes previously hidden behind this entry were separate defects and are fixed: the poisoned asset cache (BUG-2911) and the architecture-blind kernel check that rejected every arm64 image (BUG-2912). With both closed, an arm64 container on this host downloads the correct kernel and Firecracker boots it — the console log reaches the ARM64 hardware-breakpoint and ASID-allocator lines — and the apply then fails on the boot not completing within its period, with no `/dev/kvm` in the macOS Podman virtual machine. The full real Compute Engine apply therefore remains a mandatory capable-Linux CI gate rather than a locally executable macOS gate, but the failure it reports is now the real one. The packet mirroring resources are validated by their own Terraform test, which needs no booted guest. |
| 2646 | P3 | GCP simulator Cloud Run worker-pool scaling | upstream publication lag, not a simulator defect | The Cloud Run v2 `WorkerPoolScaling` members `scalingMode`, `minInstanceCount`, and `maxInstanceCount` are now modelled and covered end to end (SDK wire round-trip, CLI, and a real `hashicorp/google` 7.36.0 Terraform apply → `plan -detailed-exitcode` = 0). What remains open is upstream: the newest live Cloud Run Discovery document (revision 20260717, fetched and checked) and the published REST reference still declare only `manualInstanceCount`, even though gcloud's own generated client and the GA provider both send all four members. The runtime spec validator therefore reports six `unknown-field` keys, allowlisted in `simulator-gcp/spec-violation-allowlist.txt` under this ID. Close this and drop those six entries when Google publishes the members in the Discovery document. |
| 1345 | P2 | AzureAD Terraform provider | upstream blocker | The `hashicorp/terraform-provider-azuread` provider still lacks a supported Microsoft Graph API endpoint override, so AzureAD/Entra Terraform resources cannot be tested against the Azure simulator until upstream adds it. |
| 2712 | P2 | AWS simulator outbound delivery protocols | external carrier and mobile-push providers remain unavailable | Amazon SNS email and email-json subscriptions use real SMTP, while Amazon Data Firehose now implements its complete vendored 12-operation API and performs IAM-authorized, optionally KMS-encrypted, buffered Amazon S3 delivery for direct writes, Amazon SNS subscriptions, and Amazon CloudWatch metric streams. SMS still cannot reach a carrier and mobile-push subscriptions cannot reach Apple/Google providers because their provider credentials and delivery endpoints are not represented by an available public AWS contract. SMS sandbox creation fails loudly instead of manufacturing a verification code. Close this only when those external provider primitives can be configured through faithful AWS APIs. |

- **BUG-3 (cross-resource-group move refuses types real ARM moves):** The
  per-provider move-hook dispatch landed: Resources_MoveResources /
  Resources_ValidateMoveResources walk a hook table (`resource_move.go`)
  keyed by resource type, each hook carrying the existence check and the
  re-keying move, and the dispatch re-homes the moved scope's
  Microsoft.Resources/tags/default rows uniformly. Microsoft.Web sites
  (whole child subtree), plans, and certificates became the first hooks,
  behavior unchanged; Microsoft.Storage/storageAccounts moves end to end —
  the account record, the blob-container / file-share / table projections,
  the account-scoped ARM children, and the service-properties documents
  re-key onto the new resource ID, while the access keys are pinned across
  the move (a move never rotates keys) and the Blob/Files/Queue/Table data
  planes, keyed by the globally unique account name, keep serving the same
  bytes. Microsoft.KeyVault/vaults moves the same way: the vault record and
  its privateEndpointConnections children re-key onto the destination group,
  while the whole data plane — secrets, keys and certificates, all addressed
  through the vault's globally unique name — needs no re-keying at all, so
  the vault URI is unchanged and the key created before a move still decrypts
  a ciphertext produced before it. A move naming any other movable type (a
  network resource, an Event Grid topic, a Service Bus namespace) still
  answers ARM's ResourceMoveNotSupported, which stays truthful only for the
  types real ARM refuses (Azure Container Instances container groups).
  Remainder, in the order the store layouts allow: the families whose SAS or
  access keys are derived from the resource ID (Service Bus, Event Grid,
  Event Hubs, Azure Cache for Redis, Azure Container Registry) need their
  material pinned across the move the way the storage keys are; Microsoft
  .Network is last because its resources reference each other by resource ID,
  so moving one without re-pointing every referrer would silently break the
  fabric. No hook re-points an inbound reference held by a resource outside
  the moved set either (a private endpoint's privateLinkServiceId still names
  the pre-move ID) — that lands with the Microsoft.Network work, since it is
  the same re-pointing pass. Fix shape unchanged.

- **BUG-6 (the AIP-151 `operations.cancel` custom method is unserved):**
  Twelve vendored Google Cloud Discovery documents declare a cancel method on
  their long-running-operation collection — API Gateway, Artifact Registry,
  Cloud Build (both the global and the regional spelling), Eventarc,
  Firestore, Cloud Logging (five scope spellings), Memorystore for Redis,
  Service Usage, Cloud Spanner (six spellings), Cloud SQL Admin (v1 and
  v1beta4) and Cloud Storage. `simulator-gcp/operations.go` serves get and
  list only, so a `…/operations/{op}:cancel` POST reaches whichever fan-in
  owns the path and answers an honest unknown-action error rather than
  cancelling. Each service's coverage floor already counts the method as
  unserved. Fix shape: the operation records live in one store per service,
  so cancel is a per-service handler that marks the record cancelled and
  answers the empty body AIP-151 specifies — worth doing as one pass over
  the twelve rather than one service at a time.

- **BUG-7 (Cloud Run v2 reports an etag for jobs only):** The Cloud Run v2
  Job resource mints a fresh `etag` on every write and enforces it on
  `jobs.run`, `jobs.patch` and `jobs.delete`. The other six resources the
  Discovery document declares an `etag` on — Service, Revision, Execution,
  Task, WorkerPool and Instance — never mint one, so the member is always
  absent; the `etag` query parameter their six delete methods accept
  (`services.delete`, `services.revisions.delete`, `jobs.executions.delete`,
  `workerPools.delete`, `workerPools.revisions.delete`, `instances.delete`)
  is unread; and so is the `etag` the CancelExecutionRequest,
  StartInstanceRequest and StopInstanceRequest bodies carry. A client that
  reads one of those resources therefore has nothing to send back, and one
  that sends an etag anyway has its modification conflict ignored. Fix shape
  is the Job's: mint the fingerprint at every store write and refuse a
  mismatch with the 409 ABORTED Cloud Run answers a conflict with.

- **BUG-8 (`Microsoft.Resources/tags/default` writes a plane the resource
  cannot see):** `simulator-azure/tags.go` keeps `tagsStore`, keyed by the
  lowercased scope, entirely separate from each resource's own `tags`
  member. A `PUT`/`PATCH` of `…/providers/Microsoft.Resources/tags/default`
  at a resource scope therefore updates nothing the resource's own `GET`
  reports, and a resource created with tags is invisible to a `GET` of its
  `tags/default`. Real Azure Resource Manager has one set of tags per
  resource reachable through both surfaces. The generic resource lists read
  the resource's own tags, so a list agrees with the resource and disagrees
  with `tags/default` in exactly the same way. Fix shape: make
  `tags/default` at a resource scope read and write the tags the resource
  itself holds — the scope-to-store mapping the resource registry
  (`simulator-azure/resource_registry.go`) already keeps is the lookup that
  makes it possible — and leave `tagsStore` owning only the subscription and
  management-group scopes, which have no resource row of their own.

## Resolved history

- **BUG-4 (the subscription resource list answers only Key Vaults, and
  ignores `$filter`):** `GET /subscriptions/{sub}/resources` and
  `GET /subscriptions/{sub}/resourceGroups/{rg}/resources` are answered from
  a cross-slice registry (`simulator-azure/resource_registry.go`): a
  package-level table keyed by lowercased `provider/type` — the key shape
  `resourceMoveHooks` uses — maps each tracked resource type to the store the
  slice that owns it keeps its rows in, read through a closure at request
  time so a store assigned or reassigned by a register function is always the
  one enumerated. Fifty-six types are registered, spanning Microsoft.Web,
  Storage, KeyVault, Network, Compute, App, ContainerInstance,
  ContainerRegistry, ServiceBus, EventHub, EventGrid, DocumentDB,
  DBforPostgreSQL, Cache, OperationalInsights, Insights, ManagedIdentity,
  ApiManagement and Logic. Only resources ARM tracks are listed: a provider's
  locationless proxy children — a subnet, a Service Bus queue, a DNS record
  set, a role assignment — are reached through their parent's API and are
  absent from the list, as they are from real ARM's. Each row is rendered
  from the stored resource's own wire form into the GenericResourceExpanded
  members (`id`/`name`/`type`/`location`/`kind`/`managedBy`/`sku`/`identity`/
  `plan`/`tags`), so a slice needs no per-type projection and a resource that
  cannot be read back through its own JSON fails loudly instead of vanishing
  from a list that claims to be complete. Real ARM does not return a
  resource's provider-specific `properties` document from a list and neither
  does the simulator; `terraform-provider-azurerm`'s Key Vault cache reads
  only `id` and `name` from it before reading each vault through the Key
  Vault provider.

  Both routes honour the `$filter` grammar the operation documents and real
  clients send: `eq`/`ne` over `name`, `resourceGroup`, `resourceType`,
  `location`, `tagname` and `tagvalue`; `substringof(value, property)` over
  `name` and `resourceGroup`; `startswith(tagname, prefix)`; conjunctions
  and disjunctions with `and`/`or`, `and` binding tighter. A filter naming
  anything else — or carrying grouping parentheses — is refused with the
  400 `InvalidFilterInQueryString` real ARM answers, because a silently
  ignored filter answers with everything and reads as a result. Filtering on
  a tag name or value suppresses the rows' tags, as ARM's documentation
  states. `$expand` accepts the three documented members and reports
  `provisioningState` from the state each resource recorded for itself;
  `createdTime` and `changedTime` are absent because no slice records either,
  which is the same answer ARM gives for a resource it holds no such metadata
  for. `$top`/`$skiptoken` page through the shared `armPage`/`armNextLink`
  helpers. This is what `az resource list -g <rg>` needed: the Azure CLI
  reaches every scoping it offers — `-g`, `--name`, `--location`,
  `--resource-type`, `--tag` — through this one route's `$filter` rather than
  the resource-group-scoped route, so the group-scoped listing was previously
  the whole subscription's vaults.

  The related Managed HSM gap closed with it. `Microsoft.KeyVault/managedHSMs`
  became a real slice (`simulator-azure/keyvault_managedhsm.go`) serving
  ManagedHsms_CreateOrUpdate, _Update, _Get, _Delete, _ListByResourceGroup
  and _ListBySubscription over its own store, so a scope holding no pool
  answers the empty collection real Azure answers rather than the 404 an
  unrouted path returns, and a provisioned pool round-trips through its own
  API and appears in the generic resource list. `managedHsm.json` (2023-07-01)
  is vendored and `keyvault-arm-managedhsm-2023-07-01` entered
  `azureMethodFloor` at 6 — the only coverage floor that moved, and it moved
  because six operations are now genuinely served.

  Covered by `TestResourcesList_ScopesAndFilters`,
  `TestResourcesListByResourceGroup_ScopeExpandAndPaging` and
  `TestManagedHSMs_ListIsEmptyNotMissing` through the canonical armresources
  and armkeyvault clients; by `TestResourceListCLI`, which proves
  `az resource list -g <rg>` reports that group's resources only and reports
  more than one provider's; and by the unit tests
  `TestAzureTrackedResourceKeys`, `TestParseAzureResourceFilter` and
  `TestAzureIDSegmentAfter`, which pin the registry's key shape, every
  accepted and refused filter form, and the scope reading both the list's
  scoping and its `resourceGroup` filter rest on.

- **BUG-5 (the older Knative collections ignore their list parameters):**
  The five Cloud Run Admin v1 collections that predate the jobs family —
  services, revisions, routes, configurations and domainmappings — honour
  `labelSelector`, `limit` and `continue` like the rest. Every list call site
  goes through `knativeCollectionPage` (`simulator-gcp/cloudrun.go`), which
  narrows the stored collection to the request's namespace, applies the
  selector through the shared `knativeLabelSelectorMatches`, orders what is
  left by resource name so a cursor is stable across requests, and pages it
  through the shared `knativeListPage`; a malformed cursor is refused rather
  than silently reset. `CRServiceList`, `CRConfigurationList`, `CRRevisionList`,
  `CRRouteList` and `CRDomainMappingList` carry the `metadata` (a `CRListMeta`
  holding the continue cursor) and `unreachable` members the Discovery
  document declares — `CRServiceList` previously typed `metadata` as a free
  map and the other four omitted both.

  The etag half closed for the resource that named it. The Cloud Run v2 `Job`
  reports the `etag` the Discovery document declares — a fresh fingerprint at
  every write, including the execution-count and completion-time updates a
  run makes — and `jobs.run` refuses a `RunJobRequest.etag` the job has moved
  past with the 409 ABORTED Cloud Run answers a modification conflict with,
  as do `jobs.patch` for a body etag and `jobs.delete` for the query
  parameter. An omitted etag is unconditional, so a client that does not
  track the fingerprint is unaffected. The Knative RunJobRequest declares no
  etag at all — the entry's "unread on both API versions" was wrong about v1,
  which carries only `overrides` — so there was nothing to read there.

  Covered by `TestCloudRunV1_ServicesList_LabelSelectorAndPaging`,
  `TestCloudRunV1_ReconciledChildrenList_LabelSelectorAndPaging`,
  `TestCloudRunV1_DomainMappingsList_LabelSelectorAndPaging` and
  `TestSDK_RunV2REST_Job_RunEtagOptimisticConcurrency`, which page each
  collection into disjoint pages covering it exactly once and select by
  label through the canonical `google.golang.org/api/run/v1` and `/run/v2`
  clients.

- **BUG-2924 (two live VPCs sharing a CIDR conflicted as Docker networks):**
  The AWS simulator stopped making a VPC network's bridge subnet the VPC's own
  CIDR. `EnsureVPCNetwork` allocates each VPC's bridge subnet as a /24 slice of
  the reserved host-side pool `10.213.0.0/16`, scanning from a name-derived
  offset, skipping every subnet a network on the host already holds (which is
  also what makes a simulator restart double-allocation-free — the live
  networks are the allocator's only ledger), and reclaiming a slice held by a
  dead simulator run's leftover under the same four load-bearing conditions as
  before. The workload still genuinely owns its elastic network interface
  address: after the container starts on the pool bridge, an ephemeral busybox
  container joins its network namespace with CAP_NET_ADMIN and runs
  `ip addr add <eni-ip>/<vpc-prefixlen> dev eth0`, plumbing the ENI IP as a
  secondary whose kernel-derived connected route makes same-VPC peers on the
  shared bridge reachable over plain ARP, while the workload itself keeps its
  capability-free cloud-faithful sandbox and same-CIDR VPCs sit on different
  bridges that never see each other. ECS awsvpc tasks and Lambda VPC
  invocations both carry the address through `ContainerConfig.ENIAddress`;
  DescribeTasks and the task metadata kept their reported `privateIPv4Address`
  shape, and the Elastic Load Balancing target lookup became VPC-scoped so two
  same-CIDR VPCs holding identical ENI addresses resolve to the right task.
  `TestECSVPCOverlappingCIDR` runs on both fabrics — two live VPCs with the
  same CIDR, a server in each holding the same ENI IP, each client reaching
  only its own VPC's server — and the dead-run reclaim regressions moved onto
  pool slices.

- **BUG-2887 (Azure Application Gateway managed WAF rule-set catalog):**
  `ApplicationGateways_ListAvailableWafRuleSets` now serves the complete
  managed rule-set catalog — OWASP 3.2/3.1/3.0/2.2.9,
  Microsoft_BotManagerRuleSet 0.1/1.0/1.1, Microsoft_DefaultRuleSet 2.1/2.2;
  95 rule groups, 1,194 rules with wire-faithful descriptions, states,
  actions and tiers — vendored in
  `simulator-azure/network_appgateway_waf_rule_sets_vendored.json` from
  Microsoft's published rule enumeration cross-checked against recorded
  responses of the real service. Per-group counts are locked by
  `TestApplicationGatewayWafRuleSetsVendoredCatalog`; SDK and CLI tests
  exercise the endpoint; the
  `network-arm-applicationgateway-2025-03-01` coverage floor moved 21 → 22
  (the document's full 22 of 22).

- **BUG-2922 (Docker Engine advisories, simulator copy):** The three simulator
  modules moved from `github.com/docker/docker` to `github.com/moby/moby/client`
  v0.5.1 and `github.com/moby/moby/api` v1.55.0 — a wire-identical swap onto the
  new client's Options/Result structs, with 404 classification via
  `containerd/errdefs`, ports as `network.Port`, and addresses parsed to `netip`
  at the boundary. `github.com/docker/docker` left every module graph and
  `govulncheck` no longer reports GO-2026-5668 or GO-2026-4887. The shared
  container-runtime suites passed against the real Podman-backed daemon. The
  sockerless repository's Docker backend still carries its own copy of this bug.

- **BUG-2 (skip-if-absent, Cosmos DB differential):** The differential
  provisions its emulator end to end: the harness pulls the image when the host
  lacks it, hands one OS-selected port to both `docker -p` and the emulator's
  `--port` (the advertised data-plane endpoint follows the configured port, so
  nothing contends for the default 8081), and fails loudly on pull, start, or
  readiness. All four tool-absent skips are gone; both differentials passed
  against the real emulator on a dynamic port.

- **BUG-1 (deadcode coverage gap, shared/):** The genuinely dead helpers were
  deleted from each diverged `shared/` copy per that copy's own Linux findings
  (aws 34, gcp 55, azure 51 — cross-cloud error helpers and routers, unused
  Scanner/FrameReader/process helpers, `StartContainer`/`runContainer` where
  the cloud runs everything through other paths), together with their orphaned
  tests, and `scripts/simulators-deadcode.sh` no longer excludes `shared/`
  findings. `deadcode -tags noui -test .` reports zero findings for all three
  modules on Linux and macOS alike.

- **BUG-2928 (Lambda invocations exceeding their own timeout locally):** The
  class was attributed to a degraded local container runtime, with a restart
  as the recorded remedy and "a restarted local runtime does not reproduce
  it" as a close criterion. After the Podman virtual machine was restarted,
  the full Lambda invocation SDK suite — including the arithmetic invocations
  that had returned `Task timed out after 3.00 seconds` with an empty payload
  — passed locally (11 of 11 in 59.8s), meeting that criterion. Hosted runs
  never reproduced it.
