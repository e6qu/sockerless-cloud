# DO NEXT

1. Seven full store reads remain on request paths, held by the floor in
   `scripts/check-store-scans.sh`. Every keyed lookup is converted: single rows
   by stable key, parent-scoped child collections (indexed under each
   "/"-terminated prefix of a row's identifier, `sim.PathPrefixes`), the
   backend-address-pool joins, the ELBv2 listener a proxied request lands on,
   the target-group-in-use check, and Event Grid delivery. The seven that
   remain are two shapes: two whose operation genuinely is "every row"
   (CloudTrail delivering to every logging trail, and the role-assignment
   listing, whose unfiltered response is the whole collection), and the five
   AWS Certificate Manager ACME scans, which reconcile each row as they read
   it. Before writing any of them off again, check the claim — four of this
   pass's conversions had been recorded as "not that class" by the pass before
   it, and all four were keyed lookups.

2. App Service is at 616 of 692 operations, and the 76 that remain were
   enumerated rather than left as "the long tail". They are, by family:

   - **Network trace / packet capture** (~20 spellings: `networkTrace`,
     `networkTraces`, `startNetworkTrace`, `stopNetworkTrace`, and their
     operation-result and slot spellings). Capturing a site's packets is real
     work the simulator does not do; serving a trace means fabricating one.
   - **Process control and introspection beyond list and get**: `DELETE
     .../processes/{id}` (kill), `.../processes/{id}/modules`,
     `.../processes/{id}/dump`, across the site, instance and slot spellings.
     Not implementable, for the reason `web_processes.go` already records: the
     container engine's HTTP API exposes exactly one process primitive,
     `GET /containers/{id}/top`, and it reports no loaded modules. A module
     list would have to come from `/proc/<pid>/maps` inside the container,
     which needs a shell in the workload image (a scratch image has none) or
     the engine host's own `/proc` (unreachable when the engine runs in a
     virtual machine, so serving it would work on a Linux engine and not on
     macOS — a host-dependent API surface). The same limit stops the kill: the
     engine can signal a container's main process, not an arbitrary process
     inside it. Reopen only if the engine gains a real primitive for it.

   - **`resourceHealthMetadata`** (4), **`metricdefinitions`** (4),
     **`perfcounters`**, **`phplogging`**, **`recommendations`**, `iscloneable`,
     `migratemysql/status`, and the declined `Provider_*Stacks` — each answers
     with a series, catalog or telemetry the simulator has no input for, in the
     same class as the declined catalogs below.

   So the honest split is: nothing in the 76 is implementable from what the
   simulator can observe today. Each family needs either a primitive the
   container engine does not expose or data only the real platform holds.

3. Cloud Spanner admin is **closed**, not pending. Its measured number counts
   Discovery *method spellings*, not methods — the document declares most
   methods twice, an expanded `flatPath` and a `{+name}` template — so 188 of
   198 reads like ten missing methods and is five: 99 distinct methods, 94
   served, and the five unserved ones account for exactly the ten missing
   spellings. Those five are `databases.getScans`, `databases.addSplitPoints`,
   `databases.changequorum`, `sessions.adapter` and `sessions.adaptMessage`,
   each unserved because the simulator holds nothing to report — a Key
   Visualizer heatmap derived from production traffic, key-range splits on what
   is one SQLite database, a dual-region quorum with one replica, and raw
   PostgreSQL and Cassandra wire protocols it does not speak. Serving any of
   them means inventing the answer, so they belong with the declined catalogs
   below rather than on a work list. Google Cloud Billing (6 of 36) is likewise
   already declined. Read a measured Google number as spellings before treating
   the gap as a method count.

## Consumer follow-ups in the sockerless repository

- `backends/azure-common/build.go` builds its blob data-plane client with
  `azblob.NewClientWithNoCredential`, with a comment saying the simulator
  endpoint "does not enforce storage bearer auth". The simulator now enforces
  storage authorization on every data-plane request, so that client is refused
  at the next pin bump — and it was never right against a sovereign cloud
  either. Fix shape: read the account key the backend can already reach
  (`armstorage` `ListKeys`, it holds the accounts client) and build with
  `azblob.NewSharedKeyCredential`, which signs over plain HTTP; same code
  against simulator and cloud, differing only in coordinates.

- `backends/azure-common/acr_auth.go` asks Microsoft Entra for the
  `https://<registry>.azurecr.io/.default` scope and puts that raw token on
  `/v2/` as a Bearer. Real Azure Container Registry refuses it: the Entra
  token must be exchanged at `/oauth2/exchange` for a refresh token and then
  at `/oauth2/token` for an access token, and the audience is
  `https://containerregistry.azure.net`. The simulator now enforces this, so
  the two agree about what is wrong. Nothing fails in CI today because no
  harness sets `SOCKERLESS_AZURE_ACR_ENDPOINT`, but Azure Container Apps and
  Azure Functions image operations would fail against a real registry. Fix it
  in the sockerless repository alongside the next pin bump, not here.

## Tooling quirks that are not simulator defects

- The two container engines take different blob-upload paths, so a registry
  upload change cannot be judged on a local run. Docker's `docker push` opens
  the session with POST, sends the whole blob in a single `PATCH` and finalizes
  with `PUT`; this host's Podman sends the blob on the `PUT` and never issues
  the `PATCH` at all. A refusal added to the `PATCH` therefore passed every
  local suite and broke `TestArtifactRegistryCLI_DockerLoginPushPull` on CI.
  Judge `/v2/` upload behaviour on the CI engine.

- This host's Podman container store can acquire a dangling entry that makes
  every `ContainerList(All: true)` fail with `container not known`, which is
  the call `sim.FindExistingContainers` uses for workload recovery. It
  presents as unrelated Lambda, Step Functions and container-reaper failures
  that all pass in isolation. Clear it with `docker rm -f <dangling id>`; it
  is not a simulator defect.
- Microsoft's Cosmos DB emulator is started once for the whole Azure SDK suite,
  from `TestMain`, and warms in the background while the rest of the suite runs.
  It used to be started by whichever test asked first, and the two differential
  tests each started one — so the engine ran two emulators at once on a
  two-core runner, which is precisely the contention the reaper comment in
  `cosmos_differential_test.go` describes: the second one's pgcosmos extension
  is starved and answers "still starting" until the readiness budget expires.
  That failed three runs (2026-08-21, 2026-08-23, 2026-08-24) before the shape
  was recognised. Sharing one emulator and warming it early fixed it; the
  readiness budget was deliberately *not* raised, because go test gets 13
  minutes for this suite and the step 14, so buying readiness time would trade
  a named Go failure for an opaque step kill. A run whose `-run` filter cannot
  reach either differential test skips the warm-up and pays nothing; the tests
  still boot the emulator themselves if it was not warmed, so the filter
  decides when the cost is paid and never whether the oracle is available. The
  readiness failure also classifies itself — "still starting" means host
  starvation, anything else means the emulator never answered.

- Azure CLI 2.88's `az keyvault update --set tags.<k>=<v>` issues a vault
  GET followed by a PUT that does not carry the changed tags, and
  `az keyvault show` reported a stale tag set after a server-side change.
  Verified by hand against the simulator that the server is correct in both
  cases, and that the same sequence through `az servicebus` behaves
  correctly — so this is client-side. The Key Vault CLI tests avoid those
  two commands; do not chase it as a simulator bug.

## Declined catalog work

Two surfaces were offered for vendoring across three passes and declined
each time; they are recorded here so they stop being re-proposed. Neither
is a defect — both are surfaces whose only faithful implementation is
somebody else's published data, and a partial catalog would be fabrication:

- **Microsoft.Web `Provider_*Stacks` (6 operations)** answer with
  Microsoft's published runtime-stack catalog. Unserved, with the reason
  recorded beside the `web-arm-openapi-2025-03-01` floor row.
- **Google Cloud Billing (6 of 36)** — `services.list` and
  `services.skus.list` answer with Google's public SKU catalog. The slice
  stays at its current floor.

Revisit either only if a consumer needs it; the Application Gateway WAF
rule-set catalog is the precedent for how the vendoring would be done.

## Next Recommended Slice

BUG-2798 and BUG-2799 closed. ECS services now drive durable AWS Cloud Map
registration from real task transitions and implement persisted launch
throttling, deployment circuit-breaker rollback, and CloudWatch-alarm rollback.
Official AWS SDK and AWS CLI scenarios, hard-restart regressions, and the
production-shaped HashiCorp AWS provider graph exercised the completed data
plane.

BUG-2766 remained the next independent AWS fidelity slice: implement the
published AWS Amplify Hosting `ImageOptimization` fetch, source-policy,
transformation, validation, format-negotiation, and cache contract, then prove
it through hosted requests and external image decoders. BUG-2764 remained a
host boundary: the shared Linux test image contained the real Firecracker and
squashfs tools, while the macOS Podman virtual machine exposed no nested KVM;
the capable-Linux Terraform CI cell remained mandatory.

The completed baseline retained real AWS Private Certificate Authority and
Amazon Data Firehose implementations with official SDK, AWS CLI, Terraform,
and authenticated browser coverage.

The external review's locally actionable gaps and the follow-up implementation
audit were closed. AWS Step Functions ran and cancelled real Amazon ECS and
AWS CodeBuild workloads; CodeBuild used the requested source revision,
credential, build specification, and image; AWS Amplify ran authenticated
multi-language monorepo builds with complete phase, cache, and artifact
lifecycle; Amazon RDS exposed persistent PostgreSQL, MySQL, and MariaDB native
data planes with TLS-only IAM authentication and real password rotation; and
deployed workloads used the standard SDK endpoint environment variables.
Hosted concurrency validation preserved sub-second AWS Amplify release order,
accepted Microsoft Azure's valid subnet-before-public-prefix NAT-gateway
state, and gave the real Step Functions container integrations a
cloud-shaped cold-provisioning window with useful terminal diagnostics. The
AWS SDK shard provisioned the exact configured Alpine and official AWS CLI
images before `m.Run`, so registry transfer no longer consumed that
integration's lifecycle deadline while both real containers still executed.
Explicit Amazon ECR Public coordinates reached the container runtime unchanged,
and cancellation killed the CodeBuild workload whether Docker completed its
wait through the context or error channel, so a stopped build produced no
delayed Amazon SQS side effect. The macOS/Linux Docker validation harness loaded
Buildx output and shared the container host's PID namespace; the full
production-shaped HashiCorp AWS provider graph completed apply, a real
VPC-attached Lambda invocation, refresh, and destroy through HTTPS.
The Amazon ECS integration harness loaded its real arithmetic workload through
the backend's Docker Image Load API instead of building it outside the backend
catalog; live-cloud runs required the corresponding pre-provisioned Amazon ECR
coordinate, and all six simulator-backed real-container cases passed.
The AWS external Terraform harness preserved the original request host through
Caddy for AWS Signature Version 4, serialized heavyweight packages locally,
and assigned the root, Amazon ElastiCache, and three Amazon RDS graphs to
separate hosted runners. All five HTTPS packages completed apply, real
workload or data-plane assertions, and destroy without cross-package resource
contention.
The mandatory publication audit upgraded the AWS simulator to `go-git` 5.19.2
and its current transitive graph. The complete module suite passed, and the
authenticated dependency audit reported no drift.
The shared e2e harness loaded its compiled arithmetic fixture through every
active cloud backend's Docker Image Load API, keeping the backend catalog
authoritative. The exact e2e suite and its optional second Amazon ECS
simulator-backend path passed.
The hosted publication edge then advanced `docker/login-action` to 4.6.0.
Both immutable multi-architecture publication jobs upgraded, and action
syntax, the publication contract, and the authenticated freshness audit
passed.
Native Linux workload coordinates retained Docker's
`host.docker.internal:host-gateway` alias instead of rewriting it to the
virtual machine's default gateway; rewriting remained correct for a simulator
that itself ran in a container. The official AWS SDK Step Functions
integration passed its real Amazon ECS task, AWS CodeBuild container, and
vendor AWS CLI flow.
Publication also upgraded every newly drifted SQLite and Google Cloud client
module, moved Firestore and Spanner protobuf imports to their current
canonical modules, and passed the complete official Google Cloud SDK suite.
The exact hosted Cloud Run v1 and v2 Discovery revision 20260727 documents were
also retained; their public methods, paths, and schema fields were unchanged,
and the Google simulator route, specification, and measured-coverage suite
passed against their newer descriptions.
The three console accessibility checks anchored keyboard traversal at the
loaded document before pressing Tab, so real Chromium consistently proved each
skip link was the first in-document focus target.
Explicit Lambda deployment remained intentional because AWS Lambda itself runs
only functions a caller creates. The repository retained its truthful
unaudited/non-production warning because functional validation did not
constitute an independent security audit.

The next pass should recheck the six external blockers below and resume only
when their missing credentials, upstream API coordinates, published schemas,
provider transports, or external repository become available. Mobile push and
SMS remained under BUG-2712 because no available public AWS configuration
exposed the carrier/provider primitives needed for faithful delivery.

## Externally Blocked Work

- BUG-1075 retained authenticated Google Cloud Run, Azure Container Apps,
  Azure Functions, Lambda service-mesh, and Azure identity-backed live-cloud
  cells that required operator credentials.
- BUG-2646 retained Google's publication of Cloud Run worker-pool scaling
  members in the Discovery document.
- BUG-1345 retained the upstream AzureAD Terraform provider's missing
  Microsoft Graph endpoint override. Checked again on 2026-08-23: the
  provider's latest release is v3.9.0 (2026-06-18) and its changelog records
  no endpoint or base-URI override, so the gate is unchanged.
- BUG-2523 and BUG-2441 remained owned by the external Bleephub repository,
  which was not present in this workspace.

## Durable Validation Contract

- Simulator endpoints were exercised through official SDK, vendor CLI, and
  Terraform surfaces in the same change.
- Tests differed between simulator and cloud only in endpoint and credential
  coordinates.
- Production builds created every frontend before any UI-bearing Go binary.
- Workflow changes kept every ordinary job at or below 15 minutes and
  preserved exact AWS CLI and SDK shard coverage.
- Dependency freshness retained authenticated GitHub API requests in both its
  Bash and Zsh portability passes.
- Every observed failure or warning was fixed or recorded with evidence in
  [BUGS.md](BUGS.md).
