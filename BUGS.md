# BUGS

Open: 11. Resolved: 18.

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

- **BUG-3 (cross-resource-group move refuses types real ARM moves):** Five
  families move today — Microsoft.Web (sites with their whole child
  subtree, plans, certificates), Microsoft.Storage, Microsoft.KeyVault and
  Microsoft.ServiceBus. The
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
  Eleven type keys move: Microsoft.Web (sites with their whole child
  subtree, plans, certificates), Microsoft.Storage, Microsoft.KeyVault,
  Microsoft.ServiceBus, Microsoft.EventHub, Microsoft.Cache/redis,
  Microsoft.ContainerRegistry, and Microsoft.EventGrid topics and domains.
  Each pins the credential material its resource ID derives, so a move never
  rotates a key: an Event Hubs connection string captured before a move still
  sends and receives over AMQP after it, and an Event Grid key captured
  before a move still publishes after it. Two of those proofs are bounded by
  the data plane rather than by the move — Azure Cache for Redis has no data
  plane in this simulator at all, so `listKeys` parity is as far as its proof
  reaches, and the Azure Container Registry data plane authenticated nothing
  until BUG-13. Still unhooked, all with resource-ID-derived credentials:
  Microsoft.ApiManagement/service (subscription keys),
  Microsoft.Logic/workflows standalone (the site-hosted ones move with their
  site), Microsoft.DocumentDB/databaseAccounts, and the remaining Event Grid
  types — partnerNamespaces, which is key-bearing, plus systemTopics,
  partnerTopics, partnerRegistrations and partnerConfigurations.
  Microsoft.Network is last because its resources reference each other by
  resource ID, so moving one without re-pointing every referrer would
  silently break the fabric. Rewriting an inbound reference held from outside
  the moved set lands with that same pass; the named instances are a private
  endpoint's privateLinkServiceId, an Azure Cache for Redis linked server's
  linkedRedisCacheId, an Event Grid system topic's source and
  metricResourceId, and an event subscription's destination and
  deadLetterDestination resourceId. Event Grid's own properties.topic is the
  first inbound reference any hook rewrites. Fix shape unchanged.

- **BUG-16 (a release tag exists before the artifacts it names):**
  release-please creates and pushes the `vX.Y.Z` tag when the release pull
  request merges, and `release.yml` then builds the binaries, console bundles
  and per-architecture images against that tag. Nothing reconciles the two, so
  a stalled or failed artifact build leaves a tagged, published release whose
  assets are missing or partial, and a consumer that pins the tag resolves it
  to something incomplete. Observed concretely at v0.9.1, which
  eventually published in full: the artifact run wedged on the GHCR login and
  buildx push steps — jobs that declare `timeout-minutes: 15` but sat
  `in_progress` for more than two hours, so the enforcement gap is GitHub's,
  not the workflow's — while the remaining nine jobs waited for a runner. For
  roughly three hours the tag and the GitHub release existed and looked
  ordinary while carrying 8 of their 30 assets and none of the three
  multi-architecture indexes; anyone pinning the tag in that window would have
  resolved it to an incomplete release. The queue drained on its own and the
  run finished green, all 30 assets and all three indexes live. The `manifest`
  job verifies index shape per image, so a *failing* build is caught; a
  *hanging* one is not, and neither is the window between tag and artifacts.
  Owner boundary: the stall itself is GitHub infrastructure and outside this
  repository, but the tag-before-artifacts ordering is ours, and it is what
  turns an upstream stall into a published release that lies about itself. Fix
  shape: either publish the tag only after the artifact run succeeds, or add a
  post-release reconciliation that asserts the expected asset set and all three
  indexes exist for the tag and fails loudly when they do not — the check
  performed by hand after every release this far.

- **BUG-17 (the Azure Container Registry manifest and blob stores are global,
  not per-registry):** Two registries in the same simulator share content, so
  `regA.azurecr.io/foo` and `regB.azurecr.io/foo` resolve to the same manifests
  and blobs. Authentication is now scoped per registry (BUG-13), which makes the
  storage conflation the remaining half: a caller authorized for one registry
  reaches another registry's content by name. Real Azure Container Registry
  isolates content per registry. Fix shape: key the manifest, tag and blob
  stores by the registry's resource ID the way the other Azure stores are, and
  cover it with a two-registry test asserting a repository pushed to one is
  absent from the other.

- **BUG-18 (the Amazon ECR and Google Artifact Registry data planes
  authenticate nothing):** Both real services authenticate every registry
  request — Amazon ECR with the Basic credential `GetAuthorizationToken`
  issues, Google Artifact Registry with the same Docker Bearer challenge flow
  at `<region>-docker.pkg.dev`. Neither simulator checks anything, so the
  credentials their control planes mint are decorative, exactly as Azure's were
  before BUG-13. The shared registry now carries a per-registry `Authorize`
  hook injected by whichever cloud mounts the subtree, so the mechanism exists
  and each cloud needs its own verification pass against its own published
  contract rather than a copy of Azure's. Note the AWS and GCP copies of
  `shared/oci.go` are byte-identical to their pre-BUG-13 state; the hook lives
  only in the Azure copy today.

- **BUG-19 (the AWS Lambda invocation timeout may include the runtime INIT
  phase):** `lambda_runtime.go` starts the function's `Timeout` timer once
  `StartContainerSync` returns, so container create and start are correctly
  outside the window — but the runtime bootstrap that follows, a Python
  interpreter starting and importing `boto3`, runs inside it. Real AWS Lambda
  bills and bounds the INIT phase separately from the invocation timeout, so a
  cold start that spends seconds in initialisation should not consume the
  function's three-second budget. Observed as `Task timed out after 3.00
  seconds` on a host whose container engine was degraded, and not reproducible
  on a healthy one, so the divergence is suspected rather than demonstrated.
  Fix shape: start the invocation timer when the runtime reports itself
  initialised rather than when the container starts, and cover it with a
  function whose initialisation is deliberately slower than its timeout.

## Resolved history

- **BUG-15 (an Amazon RDS instance served connections before its engine was
  ready):** The lazy data-plane start always did wait for the engine — the
  defect was that its probe read any PostgreSQL `ErrorResponse` as proof of
  readiness, and `FATAL: the database system is starting up` (SQLSTATE 57P03)
  is an `ErrorResponse`, so the gate opened the moment the postmaster bound its
  port and the proxy forwarded clients into a server refusing all of them. The
  probe now parses the error's SQLSTATE and treats only 57P03 as not-ready, the
  classification `pg_isready` makes; MySQL and MariaDB were already correct,
  reporting ready only on a real protocol handshake, and the reason is
  documented beside them. Two defects in the same path went with it: the adopt
  path taken after a restart or `StartDBInstance` marked the lazy start
  complete without probing at all, so the first client met an engine still
  replaying its write-ahead log — which is what the 57P03 wait in
  `sdk-tests/persistence_dataplane_restart_test.go` had been papering over, and
  that wait is now removed so the test guards the bug instead of hiding it —
  and the 90-second engine budget both under-provisioned a real first boot
  (a `mysql:8.0` cold start measured 253 seconds under load) and destroyed the
  instance when it expired, with the error cached permanently, so a slow host
  bricked a database. The budget is ten minutes and re-reads the container's
  real state every two seconds, so a genuinely dead engine still fails fast.
  Reproduced deliberately under contention before the fix (three of three runs
  failed on 57P03), verified under the same contention after (three of three
  passed), and guarded by a wire-level unit test with the negative control
  confirming the old classification fails it.

- **BUG-13 (the Azure Container Registry data plane authenticated nothing):**
  Every registry request is authenticated. An unauthenticated call answers the
  Docker Bearer challenge ACR publishes — `Www-Authenticate: Bearer
  realm="…/oauth2/token",service="…",scope="…"`, the form the official
  `azcontainerregistry` policy requires both `service` and `scope` from — and
  the token service behind it is real: `GET /oauth2/token` verifies the admin
  Basic credential and only while `adminUserEnabled` is set, `POST
  /oauth2/exchange` verifies a Microsoft Entra token for the
  `https://containerregistry.azure.net` audience, and `POST /oauth2/token`
  verifies the refresh token or password grant. Tokens are real JWTs because
  the Azure SDK decodes `exp` out of them, they are issued for one registry,
  and their `access` claims are checked against the access record the request
  implies, following distribution's own method mapping. A credential-less
  caller reaches only a `pull` on a registry with `anonymousPullEnabled`, and
  the granted scope is filtered by what the credential authorizes. Regenerating
  an admin credential invalidates both the password and the tokens derived from
  it, through a fingerprint recomputed at verification time. Proven with a real
  client end to end — `podman login` refuses the wrong password and accepts the
  right one, push and pull succeed, and both stop working after logout and
  after rotation — and with the official SDK performing the documented
  401 → exchange → token → retry flow. The shared `/v2/` subtree gained a
  nil-able per-registry `Authorize` hook rather than any cloud-aware branch, so
  Amazon ECR and Google Artifact Registry are byte-identical to before and
  unaffected (their own gap is BUG-18). The registry's method floor ratcheted
  from 19 to 20 for the `GET /oauth2/token` the specification declares. The
  Terraform assertion added beside this runs only on a capable Linux host, so
  it is exercised by CI rather than locally.

- **BUG-9 (the Event Grid data plane authenticated nothing):** The publish
  data plane authenticates every caller. A custom topic or domain accepts an
  `aeg-sas-key` matching either current slot as a header or a query
  parameter, an `aeg-sas-token` or `Authorization: SharedAccessSignature`
  verified as base64 HMAC-SHA256 of the token's own `r=…&e=…` prefix under
  the base64-decoded key — the format Event Grid publishes, which differs
  from the Service Bus signature beside it in both the signed string and the
  key encoding, so it was implemented from Event Grid's own generators rather
  than copied — with the expiry and the signed resource prefix honoured, or a
  Microsoft Entra bearer for the `https://eventgrid.azure.net` audience.
  `properties.disableLocalAuth`, declared in both vendored swaggers and
  previously inert, leaves only the last. Anything else answers Event Grid's
  401 `Unauthorized` envelope with its mirrored `details` array; the real
  service also appends a support-report identifier, which the simulator omits
  rather than mint a tracking ID referencing an organisation it has no
  relationship with. Event Grid's keys moved onto the shared rotation store,
  so `regenerateKey` invalidates the key and every signature derived from it.
  The domain publish path, which previously 404'd against its own advertised
  endpoint because host resolution searched only topics, routes each event to
  the domain topic its `topic` member names.

- **BUG-14 (a Microsoft.Web/sites move silently rotated the site's
  credentials):** The shipped move hook pinned nothing, so every move
  rotated the site's `publishingPassword` and the
  `logic-access-primary`/`secondary` keys of any workflow hosted under it,
  invalidating publish profiles and already-issued Logic Apps callback URL
  signatures. The hook now pins that material the way the storage and
  Service Bus hooks do, and `TestWebSiteMovePinsSiteDerivedCredentials`
  asserts a callback signature is identical across the move. The three
  hand-rolled pin loops were folded into one shared helper so a new family
  cannot forget the step, and `redisFirewallRules` — the only Azure Cache for
  Redis store keyed by name segments rather than by resource ID, which made
  the cache unmovable — is keyed like its siblings.

- **BUG-10 (Knative v1 set a resourceVersion nobody enforced):** All five
  Cloud Run v1 replace methods the document publishes now enforce
  `metadata.resourceVersion` — omitted is unconditional, as the document's
  own wording says, matching proceeds, and stale answers 409 ABORTED in the
  Google error envelope the service uses. The Knative `Status` object the
  document declares is the delete methods' response shape, not an error
  shape, which is why the conflict is not spelled that way. resourceVersion
  is the resource's generation and every v2 write bumps it while every v1
  write mints a fresh v2 etag, so neither spelling can land a write the
  other would have refused.

- **BUG-11 (Cloud Storage operations were fabricated):** The slice records
  its long-running operations into the shared operation store, parented by
  the bucket: bucket relocation, recursive folder deletion, folder rename
  and both Anywhere Cache writes all record, and get, list, cancel and the
  relocation advance answer about records that exist and 404 about ones
  that do not. The list pages and honours the documented `done` filter,
  refusing any other term loudly rather than ignoring it. Found with it:
  `buckets.relocate` drained its request body and reported a relocation it
  never performed — it now applies the destination location, placement and
  key, honours validateOnly, and defaults an absent location the way the
  service documents.

- **BUG-12 (the Cloud Run executions verb fan-in accepted any verb):** The
  fan-in switches on the verb and answers an unpublished one with the
  service's method-not-found, matching the spelling the v1 fan-ins already
  used. The cloudrun-v2 floor did not move and stays 102: cancel is the only
  POST custom method the document publishes on that collection, so no
  documented spelling changed verdict. Measured with the probe and a
  negative control rather than assumed — the expected decrease recorded in
  this entry never materialised, and no comment claiming one was added.
  Fixed beside it: a cancelled execution reported its terminal condition as
  succeeded, so the real `gcloud run jobs executions cancel` failed with
  "has completed successfully before it could be cancelled"; the cancel
  path now writes the failed condition with reason Cancelled.

- **BUG-8 (`Microsoft.Resources/tags/default` wrote a plane the resource
  could not see):** Every scope now resolves to one holder of its tags. A
  resource scope reads and writes the resource's own `tags` member through
  the resource registry, which gained a tags reader and writer beside its
  enumerator so no second lookup table exists; a resource-group scope
  writes the group's own record, which had the same divergence; and the
  subscription and management-group scopes keep `tagsStore` as their only
  home, because the simulator holds no record for either. PATCH honours
  Merge, Replace and Delete against the holder, the generic resource lists
  report the same set either way, and a scope holding no resource answers
  404 as Azure Resource Manager does. The registry's initialisation now
  refuses a tracked type whose stored form has no settable tags member,
  which caught the nine Microsoft.Network types that carry theirs in an
  embedded envelope. The move dispatch's separate tag re-homing became dead
  code and was deleted: a resource's tags travel with the record its hook
  re-keys.
- **BUG-6 (the operations cancel method was unserved):** The entry named
  twelve documents; the coverage worklist showed five of them
  (Cloud Spanner, Firestore, Cloud SQL Admin v1 and v1beta4, Cloud Storage)
  already served cancel, so the real remainder was seven documents and
  twenty method spellings, all now served with each service's own answer.
  The nine services whose vendored description says a caller checks
  GetOperation for "whether the cancellation succeeded or whether the
  operation completed despite cancellation" answer 200 with the record
  untouched, because a completed operation is a documented outcome there
  rather than an error; Cloud SQL Admin answers the 400 FAILED_PRECONDITION
  its own documentation shows, with the distinct message it gives for an
  operation type it cannot cancel. An unknown operation name answers 404
  everywhere, which Cloud Logging previously did not check at all. Every
  long-running operation in this simulator is minted complete, so for
  eleven services the already-done answer is the only honest one and no
  unreachable cancellation branch was written; a new invariant test fails
  if that ever stops being true. AWS Cloud Build was the exception: it runs
  real processes, so its steps now execute under a cancellable context and
  cancel really terminates the running build — proven by a control that
  removes the termination and watches the test hang until its deadline.
  Three defects surfaced with it: Service Usage named its operations under
  a path its own get, delete and cancel could never resolve, Cloud Build
  recorded no operation for its non-build long-running work and returned
  the resource name where the operation name belonged, and Cloud Storage
  minted two different identifiers for one operation's name and selfLink.

- **BUG-7 (Cloud Run v2 minted an etag for Job alone):** Service, Revision,
  Execution, Task, WorkerPool and Instance now mint an etag at every store
  write, including the Knative v1 write paths and the Cloud Functions
  backing service, and a supplied etag is enforced on all six deletes, on
  the Service, WorkerPool and Instance patches — read before the update
  mask merges, so a mask cannot smuggle the condition away — and on the
  cancel-execution, start-instance and stop-instance requests, which were
  not decoded at all before and now honour validateOnly too. An omitted
  etag stays unconditional and a stale one answers 409 ABORTED, matching
  the service. Knative v1 deliberately keeps no etag: its document declares
  one only on the IAM policy, and its optimistic concurrency is
  resourceVersion, tracked as BUG-10.


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
