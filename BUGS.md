# BUGS

Open: 21. Resolved: 38.

## Open

Bugs 2909, 2932, 2646, 2712, 2764, and 1345 moved here with
the simulators from the sockerless monorepo, keeping their IDs
(as did 2924, since resolved).

| ID | Sev | Area | Pattern | One-liner |
|----|-----|------|---------|-----------|
| 2909 | P2 | AWS simulator IAM enforcement leaves 190 served operations authorized against `"*"` | the resource-derivation gap BUG-2907 closed for five services is measured across the rest, not closed for them | Thirty services derive their resource from the types AWS declares and the ARN format published beside each — Amazon Data Firehose, AWS Security Token Service and Application Auto Scaling joined the generated table, Amazon EventBridge gained the alias table its Name/Rule abbreviations needed, Amazon DynamoDB reads the export and import family's TableArn, and the state-resolving tail closed — Amazon SQS cancels a message move against the source queue its task record names, AWS Cloud Map resolves GetOperation through the operation record, and AWS CloudTrail reads the ARN-valued ResourceId and ResourceIdList its tagging operations carry — and the per-request cases that predated the table are gone but for AWS Lambda. 1,788 of the 1,975 served operations that authorize against a resource type derive it; the remaining 187 still request a literal `"*"`. AWS Step Functions state-machine and activity creation joined the table — their ARNs are name-determined, so the create request already carries everything the ARN needs, and the older comment calling every create underivable was wrong for them. `TestIAMResourceDerivationCoverage` ratchets the number and prints the per-service remainder, largest first: Amazon EC2 (55), AWS Glue (35), Amazon RDS (27), Amazon DynamoDB (18), AWS Systems Manager (17). What is left is mostly an operation that creates its resource, so carries no identifier for it yet, names something other than the resource it authorizes against, or names it by an ARN in a shape the coverage probe cannot express — those derive for real requests and are pinned by `TestIAMResourceARNs_*` behavior tests; the comment beside `iamDerivationCoverageFloor` states each service's remaining class. |
| 2932 | P3 | Three AWS Smithy patterns are stricter than the service they describe, so the simulator cannot satisfy both | the vendored model is authoritative for the simulator, but where it contradicts documented service behavior, matching the model would make the simulator less faithful, not more | The runtime pattern check (BUG-2931) reports three responses whose values AWS itself returns. Amazon EventBridge names the managed secret backing a connection `events!connection/<name>/<uuid>`, and `SecretsManagerSecretArn` admits no `!`. AWS Certificate Manager's `DescribeCertificate` reports the issuing authority as an AWS Private Certificate Authority ARN, and the generic `Arn` shape it is typed with requires the service segment to be `acm`. Amazon CloudWatch Logs reports a configuration template's `resourceType` in CloudFormation spelling (`AWS::WAFv2::WebACL`), and `ResourceType` admits no `:`. Each is allowlisted in `simulator-aws/spec-violation-allowlist.txt` against this entry rather than "fixed" by emitting a value the service never emits. The allowlist shrinks if a later model revision widens the patterns, which is the only thing that should close this. |
| 2646 | P3 | GCP simulator Cloud Run worker-pool scaling | upstream publication lag, not a simulator defect | The Cloud Run v2 `WorkerPoolScaling` members `scalingMode`, `minInstanceCount`, and `maxInstanceCount` are now modelled and covered end to end (SDK wire round-trip, CLI, and a real `hashicorp/google` 7.36.0 Terraform apply → `plan -detailed-exitcode` = 0). What remains open is upstream: the newest live Cloud Run Discovery document (revision 20260807, fetched and checked) and the published REST reference still declare only `manualInstanceCount`, even though gcloud's own generated client and the GA provider both send all four members. The runtime spec validator therefore reports six `unknown-field` keys, allowlisted in `simulator-gcp/spec-violation-allowlist.txt` under this ID. Close this and drop those six entries when Google publishes the members in the Discovery document. |
| 2712 | P2 | AWS simulator outbound delivery protocols | external carrier and mobile-push providers remain unavailable | Amazon SNS email and email-json subscriptions use real SMTP, while Amazon Data Firehose now implements its complete vendored 12-operation API and performs IAM-authorized, optionally KMS-encrypted, buffered Amazon S3 delivery for direct writes, Amazon SNS subscriptions, and Amazon CloudWatch metric streams. SMS still cannot reach a carrier and mobile-push subscriptions cannot reach Apple/Google providers. For mobile push the blocker is only the delivery endpoint: `CreatePlatformApplication` with `PlatformCredential`/`PlatformPrincipal` is a real public contract for the credential half, but the delivery target is Apple's and Google's own hosts rather than an AWS-configurable coordinate, so there is nothing faithful to point at. SMS has neither half. SMS sandbox creation fails loudly instead of manufacturing a verification code. Close this only when those external provider primitives can be configured through faithful AWS APIs. |

- **BUG-20 (the container reaper leaves workload containers running for
  days):** Simulator workload containers outlive the simulator that created
  them. Observed on the development host as 22 `sockerless-sim-azure-func`
  containers, five of them still *running* between 2 and 25 hours after the
  runs that created them ended, alongside exited workloads up to 47 hours old
  — 41 containers in total. The containers are not mislabelled: a leaked
  sidecar carries `sockerless-sim-provider=azure` and
  `sockerless-sim-run=<id>`, which is exactly the filter pair
  `shared/container_reaper.go` lists on, so the reaper would match them if it
  ran. The reaper is a detached child that watches its parent's process
  identifier, so the suspected cause is that a harness killing the simulator's
  process group takes the reaper with it, leaving nothing to collect the run —
  that needs confirming rather than assuming, and the same reaper ships in all
  three simulators, so a leak here is a leak everywhere. This is not cosmetic:
  long-lived leaked containers consume the host continuously and are a
  plausible contributor to the load-sensitive test failures chased repeatedly
  across recent passes, and they accumulate into the container-store state that
  makes `ContainerList(All: true)` fail with `container not known`. Fix shape:
  make reaping survive the simulator's death by any signal — a run's
  containers must be collectable from their labels alone by a subsequent run
  or a startup sweep — and prove it by killing a simulator with SIGKILL and
  asserting the run's containers are gone.

- **BUG-22 (the Artifact Registry data plane accepts chunked uploads the real
  service refuses):** Google documents that monolithic uploads are required
  when pushing container images to Artifact Registry, and the real service
  rejects a chunked `PATCH` sequence. The simulator accepts chunking, and
  `TestArtifactRegistry_OCIChunkedPush` asserts that acceptance, so the test
  pins behaviour the service does not have. Fixing it needs coordination
  rather than a straight refusal: the sockerless push path may rely on the
  laxity, so the consumer has to be checked in the same change.

- **BUG-24 (the `/v2/` base endpoint returns a body the registries do not
  send):** The shared OCI registry answers `GET /v2/` with `{}`, while the real
  registries answer with an empty body and `content-length: 0`. Cosmetic, but
  it is shared by all three clouds' copies of `oci.go`, so it should be fixed
  once across them rather than in one cloud.

- **BUG-27 (a virtual network ignores the subnets declared inline on it):**
  Real Azure creates subnets supplied in the `subnets` member of a virtual
  network PUT — it is what `az network vnet create --subnet-name` sends — while
  the simulator only re-collects rows that already exist, so an inline subnet
  is silently dropped and a later read 404s. Persisting it must also realise
  the network-namespace fabric, which cannot run on a non-Linux host, so this
  lands with a Linux CI test pass rather than as a store-only change.

- **BUG-35 (an Amazon ECR pull through a cache rule is not hydrated):** The ECR
  registry has never implemented pull-through cache hydration, and closing
  BUG-32 made that visible: a pull for a repository covered by a pull-through
  cache rule now answers `NAME_UNKNOWN` where it previously answered
  `MANIFEST_UNKNOWN`. Real Amazon ECR creates the repository from the
  pull-through-cache template and fetches the image from the upstream registry.
  No behaviour was lost — one 404 replaced another — but the gap is real. Fix
  shape: hydrate on a miss for a repository matching a pull-through cache rule,
  fetching from the rule's upstream registry, which is the same hydration hook
  the registry already exposes and leaves unset for this cloud.

- **BUG-36 (each cloud's test suites build the simulator to one shared path):**
  The AWS and Azure sdk, cli and terraform suites all build their simulator
  binary to `../simulator-<cloud>`, so one suite's `go build -o` can overwrite a
  binary another suite is currently executing. The Google suites had the same
  collision and now build to per-suite paths. It is the same class as BUG-29's
  shared image tags and produces the same kind of failure — an `exec format
  error` or a vanished binary in a suite that built its own.

- **BUG-37 (the Artifact Registry token service never refuses a scope):** The
  simulator's token service issues a token for any scope requested, and says so
  in a comment. The live service does not: an unauthenticated request for a
  scope naming a repository it cannot reach is refused with `DENIED` and a
  message naming the IAM permission and resource. Captured from the real
  service while fixing BUG-23. Fix shape: evaluate the requested scope when
  minting, refusing what the service refuses, without making the simulator
  stricter than it — note the separate finding that Artifact Registry
  re-evaluates access per request rather than trusting the token's scope.

- **BUG-38 (the shared registry-trust helper leaves other clouds' harnesses
  unconfigured):** `testutil/registrytrust` no longer silently no-ops, which
  makes two latent defects visible elsewhere. The Google Cloud build push test
  still uses the insecure-HTTP path and will now fail loudly rather than
  quietly doing nothing — it was already failing at the push. The AWS and
  Google harness makefiles also lack the shared engine-host temporary
  directory, so any bind-mounted workload there mounts a directory the engine
  created on its own host rather than the one the simulator wrote, which is the
  cause BUG-34 found for two Azure tests.

- **BUG-39 (`x-ms-cosmos-account` is a header the cloud does not have):** The
  Cosmos data plane honours a sockerless-invented routing header to
  disambiguate accounts. That is the synthetic disambiguation the project
  forbids, and it is now removable: the faithful coordinate — the account's
  advertised `documentEndpoint` host — works, so the header can be retired
  along with whatever still sends it.

- **BUG-40 (two Cosmos DB accounts may share a name):** The create path allows
  the same account name in two resource groups, which real Azure cannot have,
  the name being a hostname. Data-plane resolution was made deterministic
  rather than order-dependent as a stopgap, but the duplicate should be refused
  at creation the way the service refuses it.

- **BUG-41 (the Azure CLI suite is one job approaching the fifteen-minute
  ceiling):** The suite runs unsharded and measured 680 seconds across 171
  tests on a CI runner, against a job ceiling of fifteen minutes and a step
  allowance of fourteen. Its inner deadline was raised to thirteen minutes,
  which is the most it can be before a breach stops producing a named Go stack
  and becomes an opaque step kill — so the remaining headroom is under two
  minutes for a suite that roughly doubled in a single pass. The AWS CLI suite
  solved the same problem by sharding into eleven jobs, and the Azure suite's
  names partition cleanly (all 171 match `Test[A-Z]`, 99 in A-M and 72 in N-Z),
  so the split itself is straightforward. What makes it more than a workflow
  edit is that sharding renames the `sim (azure cli)` status context, and that
  name is in the repository's required-status list — the rename has to land
  together with a branch-protection change, which is a repository-settings
  action rather than a code one.

- **BUG-42 (the macOS Terraform harness skips the whole shared azurerm stack):**
  The harness drops to the host user through `setpriv`, stripping
  `CAP_NET_ADMIN` and `CAP_SYS_ADMIN`, so `TestTerraformApplyDestroy` skips on
  every macOS run; adding `--privileged` does not restore them. Running as root
  with `--privileged` gets past the capability gate and then fails booting the
  guest, because the Podman virtual machine exposes no nested virtualisation for
  that path. CI's Linux runner does execute it, so the coverage exists — but no
  local run of that stack means anything, and a green local suite must not be
  read as covering it.

- **BUG-43 (the azurerm provider crashes on the App Service backup path):**
  `terraform-provider-azurerm v5.1.0` crashed with no captured stack while
  applying an `azurerm_linux_function_app` carrying a `backup` block against the
  simulator, after three earlier failures on that stack were fixed (a Dynamic
  tier reported for every plan, a container visible only on the control plane,
  and a plan tier that refused backups). The Terraform leg was reverted rather
  than left failing. Fix shape: capture the provider's crash log, establish
  whether the simulator returns something the provider cannot parse, and add the
  stack back once it applies cleanly.

- **BUG-44 (the Blob data plane authorizes no shared access signature):**
  `webParseBackupStorageURL` checks that a backup's storage URL carries the
  mandatory signature parameters but not that the signature is valid, because
  the Blob data plane implements no shared-access-signature authorization at
  all. Verifying it in the backup path alone would refuse URLs the data plane
  itself accepts. This is the same shape as the registry and Cosmos gaps closed
  earlier: the credential exists and nothing consumes it. Fix shape: implement
  the documented signature verification in the Blob plane, then let the backup
  path rely on it.

- **BUG-45 (the Azure SDK's environment lifecycle pagers panic):** Three App
  Service Environment operations — suspend, resume and change-virtual-network —
  are declared both long-running and pageable, and the generated client hands
  back a poller whose result type is a pager. On the synchronous branch the
  SDK's no-op poller unmarshals the terminal body into a nil pager, allocating a
  fresh one with a zero handler and assigning it over the client's pre-built
  pager, so every read calls through a nil handler and panics. Only that branch
  is affected; the accepted-plus-location branch unmarshals into the client's
  own pager and reads correctly. A second, separate defect applies to both
  branches: iterating with the `More` loop Microsoft's own generated example
  uses yields zero pages. The simulator is faithful — it emits the collection
  the specification and Microsoft's examples document, verified by the runtime
  validator, and answers synchronously because the work genuinely completes in
  the handler; manufacturing an accepted response and a location for finished
  work would be a fake completion signal. Nothing here closes by changing the
  simulator. It closes when the SDK stops generating these three as pageable,
  or when its no-op poller stops overwriting a caller-supplied response. The
  bodies are now asserted on the wire in the SDK suite and through the Azure
  command-line client, which is as far as a real client can consume them.

- **BUG-48 (three Elastic Load Balancing target-health fidelity gaps):** A
  target group no listener rule references should report itself unused and not
  be health-checked at all; the health-check matcher is ignored, so a response
  code mismatch can never be reported; and deregistration delay with its
  draining state is not modelled. Found while giving target health a real
  checker. The first changes tests that register targets before creating a
  listener, so it is not a drive-by edit.

- **BUG-49 (the simulator can start without a container client):** Under
  container-engine pressure a run logged `container start failed: docker client
  not initialized` while otherwise appearing healthy, so workloads failed
  silently rather than the simulator refusing to start. Seen twice under load
  and not reproducible in isolation. Fix shape: fail loudly at startup when the
  engine client cannot be built, rather than deferring the discovery to the
  first workload.

## Resolved history

- **BUG-47 (DescribeTargetHealth probed every target on the request path):**
  The read paid a full health-check timeout for each unresponsive target —
  measured at 5.001 seconds against one, now 114 microseconds. No checker
  existed at all: the describe, the data plane's listener lookup and the Amazon
  ECS scheduler each probed inline. A real continuous checker now runs under
  the server lifecycle, checking each target on its own group's configured
  interval with the documented threshold state machine, and all three consumers
  read what it recorded. The read reports the states and reason codes the
  service documents, including the initial state before a first check completes,
  and the health-check port is honoured rather than ignored while being echoed
  back.

- **BUG-46 (a failed health check reverted a completed deployment):** A
  completed deployment is terminal, as the service documents — there is no
  documented edge back to in-progress — and the omission behind it is closed
  too: the scheduler now replaces an unhealthy task rather than merely
  reopening the rollout, starting the replacement first and stopping the
  unhealthy one once it is in service, or one at a time when the maximum
  percentage leaves no room. The initial state is not treated as unhealthy. One
  dependency surfaced on the way: a new deployment had been marked completed
  from the previous revision's counts, so with a terminal completed state the
  circuit-breaker and alarm rollbacks would never have fired; deployments now
  start in progress as documented.

- **BUG-26 (the Azure Cosmos DB data plane authenticated nothing):** A
  middleware verifies the shared-key token on every data-plane path, so a new
  route cannot skip it. The canonicalisation is Microsoft's published one —
  verb, resource type, resource link and date newline-joined with the trailing
  blank line the documentation calls out, verb and type and date lowercased,
  resource names case-sensitive — pinned by a unit test against Microsoft's own
  published encoding vector. Offers are the documented exception, signing the
  lowercased resource identifier only. All four keys authorize reads and only
  the read-write pair authorizes writes, a query POST counting as a read.
  Resource tokens and Entra tokens are refused rather than accepted unchecked,
  because Microsoft publishes the resource token's shape but not its
  construction. Every Cosmos test now provisions through ARM and signs with
  real key material, so the resource-move proof that previously could not
  demonstrate a working credential now does.

- **BUG-28 (a resource move onto an occupied identifier overwrote it):** The
  move refuses the collision. The shape is as attested as it can be and no
  further: nothing published states the constraint, but the reference does
  state that validation answers 409 with an error message, and one real failed
  move in Microsoft's own support corpus supplies both the code
  `ResourceMoveProviderValidationFailed` and the sentence naming resources
  "which have the same name as a resource in the target resource group". None
  of the plausible-sounding codes searched for exists anywhere. The nested
  detail therefore carries the attested sentence with no leaf code, because
  inventing one would put a code on the wire no client could ever have seen.

- **BUG-30 (a Logic Apps callback URL embedded the resource group):** A
  workflow is issued a 32-hex identifier at creation, preserved across updates
  and carried through a move, and its access endpoint is built from that rather
  than from the resource identifier — matching the real shape, whose published
  example response is generated for a named resource group and contains neither
  the group, the subscription nor the workflow name. A callback URL issued
  before a move is byte-identical after it. Stated as inference rather than
  fact: whether the real identifier itself survives a move is not published.

- **BUG-34 (three Microsoft Azure SDK tests failed on this host):** Two root
  causes, both measured rather than reasoned about. The registry test failed
  because the shared trust helper was a **no-op** inside the Linux harness, on
  the false premise that the engine already treats loopback registries as
  insecure; a bare push reproduced the exact reported error, and writing the
  configuration made it succeed. Two further measurements shaped the repair:
  the engine parses its registry configuration once per API-service lifetime,
  and the harness pins that service for its whole run, so the configuration can
  never be reloaded mid-run — while the per-registry certificate directory is
  read per operation. Trust is therefore installed as a certificate authority
  through real channels, the insecure-HTTP path no longer silently no-ops
  anywhere, and where it cannot take effect it fails loudly instead. The other
  two tests were one cause: the simulator handed the engine bind-mount sources
  that the engine resolves on its own host rather than inside the harness
  container, so every workload mounted an empty directory — the harness now
  shares one engine-host directory as the simulator's temporary root. Its path
  is deliberately short, because a longer one overflowed the Firecracker API
  socket's `SUN_LEN` limit and broke all nine Compute tests.

- **BUG-21 (`generateAccessToken` ignored the lifetime it was asked for):** The
  method honours the requested lifetime, and the rule turned out to be
  narrower than "up to twelve hours": the discovery document says the maximum
  is one hour by default, and twelve only for a service account allowed by an
  Organization Policy enforcing
  `constraints/iam.allowServiceAccountCredentialLifetimeExtension`, with
  43200 seconds the absolute ceiling. The simulator already modelled
  Organization Policy, so the constraint joined the catalog with Google's own
  description and a list-constraint evaluator beside the existing boolean one,
  defaulting to deny — an allow default would silently grant every account
  twelve hours and contradict the documented behaviour. Doing so exposed an
  over-broad existing assertion that every catalog entry defaults to allow.
  The Artifact Registry expired-credential case moved onto the real API path
  as a result: a token is minted with a one-second lifetime through the real
  method and presented after it expires, instead of being forged in-package.
  Unattested: Google publishes no verbatim message for an over-long lifetime
  and none could be captured, since authentication precedes argument
  validation; the status and canonical code are grounded, the wording is not.

- **BUG-23 (Artifact Registry invented a location):** The location is no longer
  manufactured. Checked against the live service rather than reasoned about:
  real Artifact Registry takes the location from the registry endpoint host, so
  the same repository path reports `locations/us-central1`, `locations/us`,
  `locations/europe-west1` or `locations/asia` depending only on which host was
  addressed, and a host that is not a regional registry is not a registry at
  all. The data plane derives the location the same way and reproduces the live
  denial exactly; at the simulator's own coordinate, which names no location and
  has no real-service equivalent, the repository the control plane created
  supplies it. When neither can, it answers `NAME_UNKNOWN` — the code OCI
  Distribution defines and Artifact Registry implements — rather than inventing
  a region.

- **BUG-29 (test suites shared global container image tags):** Every cloud's
  harness images are namespaced per suite, so concurrent suites can no longer
  clobber each other's tag mid-run. The latent defect this was expected to
  unmask was real and live: the Google Cloud CLI harness used plain
  `docker build`, which on this host's default `docker-container` builder exits
  zero while leaving the image only in the build cache — proven by a control
  showing the image absent from the store after a successful build and present
  after `buildx build --load`. Two more defects surfaced with it: two tests
  pulled a hardcoded tag and so depended on another suite having populated the
  store, and all three Google suites built the simulator binary to one shared
  path where a build could overwrite a binary another suite was executing.
  Proven by running two suites concurrently against one daemon.

- **BUG-31 (Compute Engine booted the instance inside the insert request):**
  Insert records the instance provisioning, returns a running zone operation
  immediately, and boots behind it on a context detached from the request with
  its own budget deliberately longer than a client's wait, so a client that
  gives up no longer destroys the machine it asked for. `zoneOperations.wait`
  implements the documented two-minute contract instead of answering a
  fabricated completion. Adjacent synthetic behaviour went with it: operation
  reads on all three scopes rendered an invented `DONE`, the operation lists
  returned a hardcoded empty set, and the aggregated list an empty map — all
  now read the record. Restart recovery settles a provisioning instance and any
  operation left running, so a client cannot poll forever. Exercised on Linux
  rather than accepting the macOS kernel skip.

- **BUG-32 (the Amazon ECR data plane created repositories implicitly):** Every
  repository-scoped route passes through one admission chokepoint after
  authentication and before any store access, and a repository the registry has
  no record of answers 404 with the Docker Registry v2 envelope and
  `NAME_UNKNOWN`, the code and message confirmed from real client captures
  rather than assumed. AWS is explicit that this is the difference from Docker
  Hub: "With Amazon ECR, new repositories must be explicitly created before
  they can be used." The one documented exception is implemented with it,
  because the refusal is over-broad without it: a push whose repository matches
  a repository creation template applied for `CREATE_ON_PUSH` creates the
  repository from that template — most-specific prefix first, `ROOT` last —
  carrying its tag mutability, encryption, tags and policies. Reads never
  create. Three existing tests were pushing to repositories they had never
  created and passed only because nothing checked; they create them through the
  real API now.

- **BUG-33 (an AWS Lambda REPORT line invented its memory figure):** The
  reported maximum memory is measured from the execution environment's own
  container instead of being half the configured size. What the engine can
  actually provide was measured rather than assumed: this host's engine reports
  only `usage` and `limit` under cgroup v2 — no `max_usage`, no `peak` — and
  its streaming stats endpoint samples about every five seconds, which for a
  sub-second invocation is one reading taken before the handler allocates
  anything. The container observer therefore polls every 50 milliseconds for
  the container's life and keeps the highest figure the engine reports,
  including `max_usage` where an engine keeps that counter. When the engine
  reports nothing the member is omitted entirely rather than substituted — an
  absent field is honest, an invented one is not. An idle function reports
  78–82 MB and one holding a 384 MB buffer reports 466–469 MB.

- **BUG-1345 (AzureAD Terraform resources could not be tested):** Never an
  upstream blocker. The `hashicorp/azuread` provider has supported a Microsoft
  Graph endpoint override since v2.35.0 through `metadata_host`, and v3.9.0 now
  drives a real Entra stack against the simulator with that as the only
  coordinate — application, service principal, application password, users with
  a manager, group and group member — through apply, an idempotent
  `plan -detailed-exitcode` of 0, and destroy, on its own CI shard. The gap was
  measured off the wire rather than guessed, and it was not the advanced-query
  behaviour suspected when this was reopened: the provider sends no
  `ConsistencyLevel` header for these resources at all. What was missing was
  the whole Microsoft Graph `beta` endpoint, which the provider uses
  deliberately to work around documented v1.0 omissions — `oauth2RequirePostResponse`
  on applications, `showInAddressList` on users, `samlMetadataUrl` on service
  principals, and the entire group family — together with owner and member
  reference collections, the `manager` navigation property with a real 404 when
  unset, polymorphic `directoryObjects` carrying a concrete `@odata.type` so the
  provider can sort by type, `$select`, and round-tripping every property the
  client writes rather than the handful previously stored, which would
  otherwise have shown as plan drift. Entra is modelled as directory objects
  and mounted under both `/v1.0` and `/beta`. `$count` is implemented as
  documented and gated on `ConsistencyLevel: eventual`, with both states
  asserted.

- **BUG-19 (the AWS Lambda invocation timeout included the runtime INIT
  phase):** The suspicion was right and the divergence was real. AWS documents
  that the Init phase ends when the runtime signals readiness by requesting its
  first invocation, that Init is separately limited to ten seconds, and that
  the function's timeout bounds the Invoke phase — its own worked example shows
  a three-second function reporting `Duration: 3004.92 ms` beside
  `Init Duration: 111.23 ms`, the initialisation sitting outside the duration
  rather than inside it. The invocation timer now starts when the runtime first
  requests work, not when the container starts, and the runtime deadline header
  is computed at delivery. A ten-second Init limit is enforced, with the
  documented `INIT_REPORT … Phase: init Status: timeout` and a re-created
  execution environment whose retried init is bounded by the configured timeout,
  as the real service does. Container create and start remain outside both
  budgets, being the sandbox provisioning that precedes Init. The REPORT line
  now carries a real `Init Duration` and a `Billed Duration` derived as the
  ceiling of duration plus the ceiling of init, a formula that reproduces all
  three of AWS's published examples exactly. Proven by a function whose
  module-level initialisation blocks five seconds under a three-second timeout:
  it succeeds, and the negative control restoring the old timer reproduces the
  original `Task timed out after 3.00 seconds`.

- **BUG-18 (the Amazon ECR and Google Artifact Registry data planes
  authenticated nothing):** Both authenticate now, each against its own
  published contract rather than a copy of Azure's, and the differences are the
  point. Amazon ECR answers **Basic**, not a Bearer challenge — captured from a
  real registry as `Www-Authenticate: Basic realm="…",service="ecr.amazonaws.com"`
  with a 15-byte `Not Authorized` body — and the whole `authorizationToken` is
  the Basic parameter, as AWS's own `curl` example shows.
  `GetAuthorizationToken` was itself decorative, returning a constant; it now
  mints random material recorded under its password with the documented
  twelve-hour expiry, and an expired token is refused with the `DENIED`
  envelope a Docker client renders as "Your authorization token has expired".
  Deliberately NOT implemented: refusing a token used against another registry.
  AWS states a token "can be used to access any Amazon ECR registry that your
  IAM principal has access to", so that refusal would reject requests the real
  service accepts; the older per-registry wording belongs to the deprecated
  `registryIds` era. Google Artifact Registry differs again, verified by
  probing the live service: an absent credential gets a challenge, a *rejected*
  one gets 401 with no challenge, and an authenticated caller who cannot reach
  a repository gets `403 DENIED` naming the IAM permission — not Azure's
  `insufficient_scope`. Its token scope was proved by experiment NOT to be the
  gate (a token minted for one repository serves another, because the service
  re-evaluates per request), so scope enforcement was deliberately not added:
  it would have made the simulator stricter than the real registry. Both clouds
  use the same nil-able per-registry `Authorize` hook, so no cloud-aware branch
  exists in the shared registry. Proven with real clients — go-containerregistry
  pushing and pulling a three-layer image against ECR, podman login, push and
  pull against Artifact Registry — each with the full refusal set.

- **BUG-2764 (a Google Compute Engine guest never finished booting):** The
  cause was neither the one recorded here — no nested KVM on the macOS host —
  nor the boot deadline it was later suspected to be. `realexec` launched every
  microVM with `--enable-pci`, opting into Firecracker's PCI transport instead
  of its default virtio-MMIO. On aarch64 the guest never receives the
  completion interrupt for its first virtio-blk request, so the boot stops at
  the hand-off to the root filesystem, with the console frozen after `Key type
  encrypted registered` and zero bytes ever read from the root filesystem image
  while the vcpu thread spins. Raising the budget to fifteen minutes produced no
  further console output at all, which settled by measurement that the guest
  hangs rather than boots slowly. Removing the flag — one transport for every
  host, and Firecracker's own default — takes the same kernel and root
  filesystem to `Run /sbin/init` and a reachable guest in 31 seconds, and the
  whole Google Cloud Terraform suite passes. The same flag was removed from the
  Firecracker CI boot harness. It survived because CI runs on x86_64, where the
  PCI transport works, and no CI leg boots a guest on aarch64; hosted arm64
  runners expose no `/dev/kvm`, so that coverage is the local Firecracker and
  Terraform gates until a self-hosted arm64 runner exists. Two causes
  previously hidden behind this entry were separate defects and were fixed
  earlier: the poisoned asset cache and the architecture-blind kernel check.


- **BUG-3 (cross-resource-group move refused types real ARM moves):**
  Twenty-nine Azure type keys move, up from five when this was filed — API
  Management, standalone Logic Apps workflows, Cosmos DB accounts, Event Grid
  system, partner and partner-namespace topics, and thirteen
  Microsoft.Network types joined the earlier families. What made the network
  family possible is a general inbound-reference repointing pass rather than a
  hand-listed set: every store a build creates is now recorded, and after a
  hook runs the mover walks all of them, rewriting both keys beneath the moved
  identifier and any string naming it at a resource-identifier boundary, so an
  identifier embedded in a URL is caught too. Scanning every store is
  deliberate — a hand-maintained list rots silently. Confirmed rewrites include
  an Azure Cache for Redis linked server, an Event Grid system topic's source,
  a private DNS zone's virtual-network links, a Logic Apps access endpoint and
  the container registry's content. Each family's credential is pinned across
  the move, and where a data plane exists the proof is a real call rather than
  a key comparison; where one does not, the entry says so instead of
  downgrading quietly. The types Azure itself refuses stay refused and are
  pinned by tests at unit, SDK and CLI level — partner registrations, private
  link services, application gateways, NAT gateways, network profiles and
  virtual network taps are all published as unmovable, and private endpoints
  are conditional on the linked resource's type, implemented against the
  published allow-list. Verified against Azure's move-support tables as
  published on 2026-05-26. Fixed beside it: a Logic Apps callback signature
  covered the workflow's full resource identifier, so a move invalidated every
  outstanding callback URL; it signs the relative path now, as the real service
  does.

- **BUG-17 (the Azure Container Registry content stores were global):** The
  manifest, blob and upload stores carry a scope, and the Azure registry
  supplies its ARM resource identifier as that scope, so two registries can no
  longer resolve the same repository name to the same content. The catalog and
  tag listings filter by scope too. Because the scope is the resource
  identifier, a moved registry's content is re-keyed by the same repointing
  pass that closed BUG-3, and a test proves login server, admin credential,
  manifest, blob, tag list and catalog all survive a cross-group move.

- **BUG-25 (the specification validator judged registry responses against
  unrelated schemas, and a push test proved nothing):** Two defects found while
  arming the validator for the Artifact Registry work. The validator carried
  its own copy of the "is this an OCI data-plane path" predicate, which had
  drifted from the one `token_signing.go` uses, so `GET /v2/token` matched
  Cloud Logging v2's `GET v2/{+name}` template and its perfectly valid `token`
  and `expires_in` members were reported as fields Cloud Logging's
  `LogExclusion` does not define. The duplicate is gone and both callers share
  one predicate, so they cannot disagree again. Beside it, the existing OCI
  push test pushed to a repository that was never created and passed only
  because nothing checked; it now creates the repository through the real SDK
  first.

- **BUG-16 (a release tag existed before the artifacts it named):** The Release
  workflow ends in a reconciliation job that asserts the finished release
  matches what its tag promises: all thirty assets the build matrix produces —
  three simulators across linux and darwin on amd64 and arm64, plus the three
  console bundles, each with its checksum — and all three multi-architecture
  image indexes resolving to an OCI index carrying linux/amd64 and linux/arm64.
  An asset the matrix no longer produces fails it too, so the workflow and the
  release cannot drift apart silently. A failing build was always caught by the
  job that failed; what this closes is the hanging build, which left a tagged,
  published release that looked entirely ordinary while carrying part of its
  contents. `scripts/verify-release-complete.sh` runs standalone against any
  tag, so a release can be checked before a consumer pins it. Proven in both
  directions: it passes v0.9.1 and v0.9.2, and it fails for a tag with no
  release, for an expected asset that is not published, and for an image index
  that does not exist.

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
