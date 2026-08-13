# DO NEXT

1. Watch the polish-pass-2 pull request's CI and fix failures for real.
2. BUG-2924 still awaits user sign-off on the proposed design (host-subnet
   allocator decoupled from the VPC CIDR, ENI IP as a secondary interface
   address via an ephemeral NET_ADMIN netns-join container).
3. App Service Stages 2-5, one per pass: Static Web Apps completion (+~69),
   function/host keys + webjobs + deployment extras (+~62, and wire the
   x-functions-key contract into POST /api/function so keys are
   load-bearing), certificates/hostnames/provider tail (+~44, with the six
   Provider_*Stacks operations needing a vendored-catalog decision like the
   WAF rule sets), networking across sites and plans (+~47). Deferred with
   reasons: processes/instances read the live container (highest fidelity,
   highest complexity), backup/restore wants a real Blob round-trip,
   App Service Environments + Kube Environments are two new top-level
   resources, detector execution is data the simulator cannot compute.
4. Google Cloud Billing (6/36) still carries the data decision:
   services.list / services.skus.list answer with Google's public SKU
   catalog, which would need vendoring under the no-partial-catalog rule.
5. Other measured ratchets, one service per pass: Cloud Spanner admin
   (82/198), Cloud Run v1 (100/152).
6. IAM derivation remainder: 190 operations, every service classified in the
   floor comment; nothing derivable is left without either a new shape or a
   state design.

## Simulator burn-downs (carried over from the sockerless monorepo)

Resource-scoped IAM authorization covers thirty services, and every
per-request case that predated the generated table is gone but for AWS Lambda.
BUG-2909 records the 195 served operations that still authorize against a
literal `"*"`, largest first — Amazon EC2 (55), AWS Glue (35), Amazon RDS (27),
Amazon DynamoDB (18), AWS Systems Manager (17).

Every service with a straightforward answer has one. What remains is mostly an
operation that creates its resource and so carries no identifier for it yet, or
one that names something other than what it authorizes against — the two shapes
that no lookup can resolve, and the reason the remainder is measured rather than
promised.

Adding a service is small. `iamTableDrivenARNs` fills any published ARN format,
so a service needs its entry in `scripts/gen-aws-iam-resource-types.sh`, a
lookup that reads a field in its protocol, and, where the API renamed an
identifier, an alias table written off the model rather than from the name.

Three shapes now have precedent for the harder cases. Where an ARN carries an
identifier no request supplies, the gate resolves it through the simulator's own
state, as Amazon RDS does for a custom engine version and Amazon EC2 Auto
Scaling does for a group's assigned id. Where the reference publishes one
resource two ways, the request decides which applies before the derivation runs,
as Amazon EventBridge does for a rule on the default bus versus a custom one.
Where the identifier itself says what it identifies, the type is read off it
rather than off the member it arrived under, as AWS Organizations does — the
shape for a service whose members accept several resource types at once.

The services a straightforward pass could finish are finished — the latest
slice added Amazon Data Firehose, AWS Security Token Service, Application
Auto Scaling and the Amazon EventBridge alias table. What remains is
dominated by creates with no identifier yet and operations that name
something other than what they authorize against; AWS Glue's remaining 35
are the data-quality operations, which name a result rather than the ruleset
they authorize against, and creates that have no identifier yet.

The response-pattern burn-down is done on all three simulators. The AWS
validator checks `smithy.api#pattern`, the Google Cloud one Discovery's
`pattern` and the Azure one Swagger's; AWS carries a three-entry allowlist
against BUG-2932, Google Cloud reports none, and each check has its own
regression. What is left of the class is the design question in BUG-2924 and
whatever the Azure suites report on a host that can run them — the Azure
harness needs a Linux real-network host, and this one cannot bind the
resolver it wants.

Two pieces remain staged in [PLAN.md](PLAN.md) § "Staged: the Compute and
Console Tails", and both are breadth work needing no special host: Google
Compute Engine's ~455-method tail, and the Azure console's service surface.
Stage each by resource family or service so a pass stays reviewable.

An operator-side step is outstanding and is not a code change: deployments
pinned to a simulator image built before live workload log streaming landed
serve an Amazon ECS service task's stream with nothing but the synthetic
"container started" event, because that build reached CloudWatch only through
the post-exit drain and a service task never exits. Any deployment showing that
symptom needs its `sockerless-simulator-aws` digest repinned to a current
build; no simulator change makes an already-published older image stream.

## Completed Baseline

Google Cloud Resource Manager reached complete coverage on all three API
versions — v1 76/76, v2 24/24 over a newly vendored Discovery document, v3
126/126 — closing the Organization Policy family on every hierarchy node,
`getAncestry`, the organizations and liens collections, and the v2 folders
collection `gcloud resource-manager folders` speaks. Two IAM constraints are
enforced by the service they govern, so an organization policy changes
behavior rather than only being recorded. Two defects surfaced with it and
were fixed: the simulator answered 401 for every unrouted path instead of 404
(GitHub issue #875), and the Compute Engine load-balancer front end demanded a
Google access token no real client sends to a load balancer. The next measured
ratchet candidates are Azure App Service (161/692), Google Cloud Billing
(6/36), Spanner (82/198) and Cloud Run v1 (100/152).

The Azure simulator's assessed surface gaps closed: the Blob, Files, and Queue
data planes serve every documented operation (69/69, 51/51, 16/16), all nine
previously unserved Microsoft.Network swaggers now serve 116 of 123
operations, and Microsoft.Subscription serves 15/15 — the measured Azure floor
moved from 1786 to 1998. Six defects surfaced by that work were fixed with it,
including host-addressed data planes bypassing the observability middlewares
in all three simulators and an NSG rule-compilation failure that broke every
interface in a governed subnet on a real-network host. The Application Gateway
managed WAF catalog and Network Watcher packet captures remain deliberately
unbuilt and tracked, because both would require presenting data the simulator
cannot compute. The next measured ratchet candidates are Azure App Service
(161/692), Google Cloud Billing (6/36), and Cloud Resource Manager v1 (26/76).

The last three locally actionable bugs closed in one pass: AWS Amplify
Hosting image optimization became the real fetch-validate-transform-cache
primitive with the Next.js-exact error contract; Azure Container Apps
modeled its complete Configuration surface and assembles a real daprd
sidecar for dapr-enabled apps; and key rotation landed across all seven
Azure key-bearing surfaces. The rotation work exposed and closed Event
Hubs' hardcoded constant keys, Service Bus' orphaned-rule deletes, and the
messaging data planes' accept-any-key authentication — real SAS signature
verification now guards the AMQP and HTTP surfaces with negative-control
coverage. Amplify's silent malformed-manifest fallback and missing route
fallback on Compute/ImageOptimization targets were fixed opportunistically,
and the Azure storage data-plane surface table reached full SDK/CLI
client coverage. The remaining open bugs are all externally gated
(upstream providers, live-cloud credentials, carrier primitives, the
Bleephub repository, macOS KVM).

GitHub issue #872 closed: workload container logs stream live to the cloud
log sinks in all three simulators, with the post-exit drain deduplicated by
per-stream line counts. A full cross-sim persistence audit then eliminated
every found case of cloud state dying with the simulator process while its
metadata persisted: bulk-data roots joined `SIM_DATA_DIR`, the in-memory
Entra directory, Service Bus data plane, Spanner and Bigtable data planes
moved to durable storage, the Azure store gained the AWS hidden-sidecar
codec for wire-hidden fields, signing identities and counters persist, and
workloads left RUNNING by a dead process settle truthfully at registration.
Every simulator now carries an end-to-end restart regression suite. The
four stateless key-regeneration surfaces remained tracked as BUG-2872.

Community-filed GitHub issues #870 and #853 closed alongside two staged
contract gaps. The AWS call-time IAM gate derived per-item DynamoDB table
ARNs for transactions and batches, so resource-scoped least-privilege
policies authorized them. Azure Container Apps PATCH applied real RFC 7396
JSON Merge Patch through one shared helper and its DELETEs became true ARM
long-running operations on the shared LRO store, with 409 on writes during
deletion; the unmodeled Configuration members remained tracked as BUG-2842.
Google Cloud Run v2 update masks validated against the complete Discovery
mutable field set and rejected unknown or output-only paths with 400
INVALID_ARGUMENT instead of silently dropping them. The deployment recipe
gated Caddy on simulator `/health` checks and answered residual proxy
failures with 503 + Retry-After, while OpenID Connect discovery fetches in
the federation and console-auth paths ran on a bounded background client and
the console SPAs deduplicated concurrent token exchanges.

A hosted services shard panicked after its real-workload assertions: a task-
container-start goroutine launched by `runECSTasks` read the Elastic Load
Balancing store after orderly shutdown had closed SQLite. Task startup now
runs under the server's background-worker lifecycle, so shutdown drains every
in-flight start before the database closes — the BUG-2827 class extended to
ECS task startup. The complete AWS simulator module and focused official AWS
SDK suites passed.

The following hosted iteration surfaced one more specification edge and three
more budget defects. A hosted edge served Dataflow v1b3 revision 20260729
before operator-visible edges; the exact CI-captured document was vendored
byte-for-byte, identical to the previous pin except its revision marker. The
ARM64 core job's two-minute shared-module deadline could not hold a 73-second
SQLite soak plus the remaining suite, so those packages now share the
five-minute sibling deadline. Ten-second AWS Batch and CodeBuild workload
assertions became sixty-second assertions with last-status diagnostics. The
AWS CLI compute shard cancelled at its fifteen-minute limit on two of five
runs, so its forty Amazon ECS tests moved into a dedicated `sim (aws cli ecs)`
shard while the coverage gate still assigns all 664 CLI tests exactly once.

The widened Express rollout window paid off immediately: its new diagnostic
showed two replacement tasks RUNNING beside one old task for two minutes —
the health gate never passed. The Express-managed security group had no
ingress permissions, so the real-VPC tier's nftables filter dropped every
health check and forwarded packet. The managed group now admits TCP 443 from
the world and the container port from the VPC CIDR, the lifecycle test runs
in its own VPC with an authorized caller group, and the rollout diagnostic
reports per-task and per-target state. The focused official AWS SDK Express
suite passed.

The same pass fixed two neighbours. A filtered unit run panicked when the
delete-time drain's asynchronous reconciliation read scheduler and alarm
stores the test had not initialized; it now initializes them, and the
complete AWS simulator module suite passed. The surface-table seeder's
filename glob order followed `LC_COLLATE`, so macOS and hosted runners
emitted identical rows in different orders; the script pins `LC_ALL=C`.

The replacement hosted run drifted Google Artifact Registry v1 to Discovery
revision 20260727 and timed out the ECS Express rollout assertion at thirty
seconds. The repository vendor script retained the exact multi-probe revision
20260727 document — identical to the CI-captured artifact except its revision
marker — and the complete freshness audit passed. The Express rollout window
now covers the scheduler's full bounded placement-retry chain, matching real
Amazon ECS rollout timing, and its failure output retains the last observed
desired/running/pending counts, both task definitions, deployment state, and
latest service events. The focused official AWS SDK Express suite passed.

Amazon EC2 implemented all 772 operations in the vendored service model.
Regional account VPC encryption controls persisted their mode and exclusions,
reconciled existing and newly created VPCs, and surfaced through the Cloudscape
console. VPC endpoints persisted payer responsibility, and service acceptance,
rejection, and deletion drove the real local PrivateLink provider connection.
Official AWS SDK, AWS CLI, HashiCorp AWS provider, and hard-restart scenarios
passed.

AWS Glue implemented all 297 operations in the vendored service model.
Durable business glossaries, terms, form types, asset types, assets,
attachments, associations, and idempotency tokens survived hard simulator
replacement. Entity metadata and records came from native Data Catalog and
Amazon S3 state or an actual DynamoDB connection, while data-quality batch
reads returned existing evaluation runs. The AWS console exposed business
glossaries and asset types through public Glue APIs. Official AWS SDK, AWS CLI,
simulator-package, hard-restart, console-package, and browser checks passed.
`ListGlossaryTerms` also stopped emitting its undeclared top-level
`GlossaryId`; signed raw SDK and exact CLI requests passed the runtime Smithy
response-shape validator. Entity coverage locates the DynamoDB table it creates
without assuming that earlier tests left the shared simulator account empty.

The AWS SDK restart harness now reserves each Route 53 test coordinate only
after binding the same wildcard port on TCP and UDP. The focused dual-protocol
allocation regression and real-container ECS service-adoption restart passed.

AWS store-backed periodic work now belongs to the server lifecycle. Orderly
shutdown cancels and drains Lambda event-source, CloudWatch alarm, Application
Auto Scaling, and Scheduler workers before SQLite is checkpointed and closed.
A deterministic drain/reopen regression passed, and the persistent-state SDK
scenario now requires its child process to exit cleanly before restart.
The final exact official AWS SDK A-M shard passed in 300.747 seconds.
Server context creation occurs only after fallible persistence initialization;
the exact shared-module lint and complete tests passed.
The badge-only stacked PR 869 was squash-merged through GitHub into PR 868's
branch, retaining its complete change and restoring the one-open-PR invariant.

The hosted Shauth relying-party job reached no browser assertion because its
single Docker Hub request timed out while pulling PostgreSQL. The harness now
retries convergence of the same real Compose stack four times with bounded
backoff and still fails loudly after exhaustion.
The local rerun then found the AWS relying party inheriting Route 53's fixed
port 5353; harness-owned simulators now request an operating-system-selected
DNS coordinate. ShellCheck, bash and zsh parsing, and the complete real
PostgreSQL/Ory Hydra/Shauth/Chromium matrix passed.

Host disk exhaustion damaged the cached Lambda Python 3.12 runtime image's
overlay graph. Only that replaceable image was repulled; the real deployed
Python function again used its bundled SDK to send to Amazon SQS. The scenario
now includes the complete invocation payload when a function error occurs.

Google Cloud Eventarc v1 Discovery revision 20260723 replaced revision
20260717. Only the revision marker changed, and the complete multi-probe
Google Cloud specification freshness audit passed.

Amazon Simple Queue Service visibility-timeout coverage always waited for real
redelivery and no longer disappeared under Go's short mode.

The same-day AWS SDK release wave advanced Lambda to `v1.101.0` in the Lambda
backend and official SDK suite and IAM to `v1.57.0` in the SDK suite. The
Lambda backend built and passed its tests, the complete official AWS SDK suite
passed in 546.212 seconds, and the repository-wide dependency, Terraform
provider, and GitHub Actions audit was current.

The exhaustive local AWS SDK target no longer inherited Go's ten-minute
package timeout. The shared Go library test recipe accepted module-specific
flags, the AWS SDK suite declared a 30-minute budget, and hosted CI retained
its separate four-shard limits.

The wider validation exposed an ECS
test-isolation defect: generated VPC CIDRs overlapped fixed-CIDR cases and
cleanup ignored the transient dependency error while stopped containers still
held a network. ECS helper VPCs now use the reserved 10.225-249 range and retry
deletion until asynchronous task shutdown releases the network.

The exact hosted Amazon ECS compute shard then exposed a real long-lived
lifecycle defect: its default subnet exhausted because allocation advanced a
cursor without reclaiming addresses from stopped tasks. Allocation now derives
occupancy from live elastic network interfaces, NAT gateway addresses, and
non-stopped ECS tasks, searches the usable range circularly, and reuses
released addresses. Task startup and stop transitions are serialized, and
cleanup removes dependent containers before their primary and pause network
holders. Real compiled and BusyBox fixtures replaced shell commands that had
been aimed at scratch images. The complete AWS simulator module, shared
container runtime suite, multi-container localhost scenario, and exact
`TestE` shard passed.

The same hosted rerun completed the repaired compute shard but cancelled the
required combined simulator lint job at its exact five-minute job limit.
Lighter lint shards retained five-minute limits; the simulator matrix entry now
supplies a verifiable ten-minute limit to both its job and lint step without
renaming the required status context. The workflow timeout gate, its fixtures,
and actionlint passed.

Microsoft Azure resource-deletion dialogs retained Azure Resource Manager's
actionable failure after a rejected request even when a concurrent Fluent UI
backdrop event arrived. Backdrop dismissal was suppressed only while the error
was displayed; explicit Cancel and Escape remained functional. The Azure
console typecheck, all 131 package tests, and production build passed.

Secondary process-mode AWS simulators no longer inherited the default Route 53
DNS listener port. The AWS CLI harness assigned each nested process an
operating-system-selected UDP/TCP coordinate, so a simulator already using
port 5353 could not prevent the child from starting. The focused process-mode
case and the complete compute shard passed.

Amazon ECS task definitions now turn `taskRoleArn` into usable workload
credentials. The task-metadata service mints expiring `ASIA` sessions bound to
the configured IAM role, registers them with the simulator's Signature Version
4 verifier, and injects the standard ECS relative credential URI in the
real-VPC network tier. The Docker-network compatibility tier exposes the same
task-scoped endpoint through its reachable host coordinate. An official AWS CLI
container consumed the credentials and returned its exact assumed-role ARN
from `sts:GetCallerIdentity`.

Amazon ECS services now complete the discovery and failed-deployment
contracts left after the core scheduler landed. Running service tasks register
their real network address and port in AWS Cloud Map and reconcile those
instances through replacement, scale-in, deletion, and hard restart. Durable
launch failures apply bounded backoff and drive configured deployment circuit
breakers or CloudWatch alarms to failure and rollback. Restart regressions
retain failure timing and release adopted Fargate network state on deletion.
Load-balanced deployments retain one bounded reconciliation timer while in
progress, so real target health that changes after the first steady-state probe
completes the rollout without another API request or task transition.

Amazon DynamoDB enforces its 400 KiB item limit from stored attribute bytes
rather than JSON encoding size. AWS Secrets Manager owns independent
Region-scoped primary and replica records with synchronization, removal,
promotion, and persistence. AWS Step Functions generic AWS SDK integrations
now dispatch generated Smithy bindings for AWS JSON, AWS Query, REST JSON, and
REST XML protocols. The AWS console exposes ECS service deployment operations
and Secrets Manager replica management through those public service APIs.

The Terraform-in-ECS workload image carries an ahead-of-time filesystem mirror
of the exact AWS provider, so its private-subnet task performs one offline
initialization and one apply without undeclared internet egress. It publishes
exact task output through Amazon CloudWatch Logs, and terminal Step Functions
failures report that output immediately. The focused real-container execution
and exact AWS SDK N-Z shard passed.

Core filesystem-driver staging validation no longer assumed `/usr/local` was
unwritable. Both tests force the direct path to fail portably by creating the
requested destination beneath a regular file, independent of runner privilege.

Google Cloud and Microsoft Azure SDK/CLI jobs pre-fetched their separate
official-client modules through the bounded dependency-download helper before
the suites started.

Simulator SDK, CLI, and Terraform matrix jobs restored the immutable
Firecracker seed cache without trying to save their root-mutated guest
filesystems. The dedicated Firecracker job remained the cache publisher.

The Microsoft Azure workload-dispatch invariant keeps its two justified
`os/exec` exceptions as source comments without logging file-and-line-shaped
messages that GitHub's Go problem matcher turns into failure annotations.

The pre-push dependency audit's coordinated AWS SDK patch wave and Google Cloud
Spanner client release were applied across every affected Go module with the
repository-owned upgrade target. Direct pins and their resolved transitive
graphs were current again. Go 1.26 module reconciliation also advanced the
Microsoft Azure and Google Cloud common backends to the selected
`go-isatty` 0.0.24 transitive release; both focused lint and unit suites passed.

AWS Key Management Service custom key policies persisted in the simulator's
SQLite key record instead of disappearing during JSON serialization. A
store-close-and-reopen regression proved durable read-back, and the
production-shaped HashiCorp AWS provider graph supplied a custom policy so its
post-create policy waiter exercised the same contract.

AWS DynamoDB auxiliary table state no longer depended on fields excluded from
JSON serialization. TTL, point-in-time recovery, and tags lived in one durable
out-of-band settings record, deletion removed that record, IAM resource-tag
conditions read it, and a SQLite close-and-reopen regression plus the
production-shaped provider graph exercised all three convergence paths.

The hosted Google Cloud specification gate's exact Cloud SQL Admin v1 and
v1beta4 revision 20260722 artifacts were retained. Their 75 methods and routes
were unchanged, while authenticated public-route coverage implemented and
round-tripped the three newly published schema members.

The hosted Dataflow v1b3 Discovery edge advanced to revision 20260719 after
local validation. The exact preserved artifact replaced the older pin after a
structural comparison proved all 42 methods and paths and all 1,174 schema
field/type entries unchanged. A subsequent local multi-probe fetch observed
API Gateway v1 revision 20260724; all 30 methods and paths and all 143 schema
field/type entries were likewise unchanged.

AWS simulator state survived hard process replacement as a coherent cloud
slice. The SQLite envelope retained exported runtime configuration hidden from
public JSON, startup rebuilt monotonic counters and derived revisions, real
Network Load Balancing and Amazon RDS listeners rebound, and state-scoped
Amazon ECS, AWS Batch, CodeBuild, Amplify, Lambda, scheduler, and autoscaling
work was adopted or resumed. Asynchronous Lambda work became durable before
acceptance, while Step Functions checkpointed and reattached to the original
Amazon ECS or CodeBuild task. Official AWS SDK and AWS CLI restart suites
passed, and the production-shaped HashiCorp AWS provider completed apply,
hard restart, zero-change refresh, and destroy.

SQLite durability no longer depended on the host preserving the database and
WAL under `synchronous=NORMAL`. The AWS, Google Cloud, and Microsoft Azure
simulators opened every persistent connection with `synchronous=FULL`, and an
orderly server shutdown truncate-checkpointed the WAL, closed the database, and
returned any checkpoint or close failure. Each complete shared simulator suite
proved the connection-level pragma and preserved data after close and reopen.

Legacy Amazon ECS state from releases before state-scoped workload adoption no
longer prevented that durable simulator from starting. A persisted task that
claimed `RUNNING` but had zero matching runtime containers became truthfully
`STOPPED` with the restart cause and unknown exit code; recovery continued
through the remaining tasks, while Docker or Podman discovery failures still
failed startup loudly.

The production Compose recipes enabled the existing durable stores for the
AWS, Google Cloud, and Microsoft Azure simulators on named volumes. The AWS
Batch console listed real jobs and definitions, polled status, surfaced
terminal details, and terminated live work through standard AWS APIs.
Associated AWS WAF web ACLs evaluated the complete supported statement graph,
and Elastic Load Balancing listener creates and modifications failed
transactionally when their real TCP or TLS binding could not be provisioned.
The Elastic Load Balancing official-client fixtures imported issued,
exportable AWS Certificate Manager certificates and selected isolated real
listener ports, while nested simulator processes received their own Route 53
DNS coordinate. The focused listener cases and complete AWS SDK compute shard
passed together.

AWS Glue database tags remained durable internal state and were projected only
through `GetTags`, so `GetDatabase` matched its Smithy model without losing
tags across hard process replacement. An unconfigured AWS Cloud Map HTTP
service no longer gained an invented custom-health configuration. The durable
Lambda callback restart proof read its runtime checkpoint from Amazon
CloudWatch Logs through the official AWS SDK rather than a host callback. The
complete AWS SDK services A–M shard passed with zero Smithy violations, and the
complete AWS CLI edge-delivery shard passed with real imported certificates
and isolated listener ports.

The macOS AWS Terraform container wrapper mounted the runtime Smithy report
back to the host, surfaced Podman attachment failures, and removed the exact
container plus anonymous volumes. A non-destructive Podman virtual-machine
restart cleared the volatile overlay fault, after which the complete local
provider apply, hard restart, refresh, assertions, and destroy passed.

The pre-push freshness gate advanced gRPC to 1.83.0 in both Cloud Run
backends, the shared Google Cloud backend, the simulator, and its official SDK
module. All five affected modules and the complete official Google Cloud SDK
suite passed. The Cloud Build build-and-push scenario separated real registry
container creation from startup and removed its anonymous volume, so Podman
start errors stayed visible and successful runs leaked no fixture storage.

Google Cloud Spanner executed SQL, DML, batch DML, reads, mutations,
transactions, partitions, and batch writes through real SQLite transactions
over official REST and gRPC clients. Strict DDL and composite-key behavior
passed official SDK and gcloud coverage; the HashiCorp Google provider passed
instance/database/DDL apply, a zero-change plan, and destroy.

AWS Step Functions launched the official HashiCorp Terraform image as a
synchronous Amazon ECS task, and Terraform applied Amazon SQS through the
standard global AWS endpoint. AWS Amplify retained build and test artifacts,
retry lifecycles, WAF association, hosted request enforcement, and sampled
requests. An unmodified ecs-dev-desktop graph applied 178 resources, converged
to a zero-change plan, passed external assertions with no Smithy violations,
and destroyed every resource.

AWS Lambda implemented all 85 operations in the vendored Smithy service model.
ZIP and image functions executed through the AWS Lambda Runtime API; layers,
versions, aliases, function URLs, concurrency, capacity providers, response
streaming, code signing, durable executions, callbacks, timeouts, pagination,
and lifecycle validation retained real service state and response shapes.
Deployment-package and layer roots were readable by Lambda's sandbox user and
mounted read-only, so managed runtimes executed the same ZIP on Linux and
Docker Desktop.

AWS Step Functions implemented all 37 operations in its vendored Smithy model.
Standard and Express Workflows executed JSONPath and JSONata definitions with
Pass, Task, Choice, Wait, Succeed, Fail, Parallel, Map, distributed Map,
activities, callbacks, retries, nested workflows, Lambda tasks, redrive,
versions, and aliases. Execution snapshots and histories retained immutable,
service-shaped events and input/output.

Official AWS SDK, AWS CLI, and Terraform suites exercised both services through
their public APIs. Selected control-plane, runtime, history, nested-workflow,
distributed-Map, ZIP/layer, and version/alias flows ran against short-lived
live AWS resources and matched the simulator differential. The live resources
and temporary IAM roles were removed after validation.

The AWS console exposed Lambda overview, code, test, logs, configuration,
layers, environment, concurrency, versions, aliases, URLs, and tags. Its Step
Functions experience exposed the graph, editable definition, execution input,
history, input/output inspection, publishing, aliases, tags, and redrive. AWS
Private Certificate Authority and Amazon Data Firehose added complete
authority lifecycle, encrypted delivery-stream, and Amazon S3 delivery
workflows. The production UI passed 241 Chromium package tests, and the
authenticated Shauth/Ory Hydra/PostgreSQL matrix exercised all four services
through federated AWS credentials.

AWS Step Functions executed optimized and SDK Amazon ECS and AWS CodeBuild
tasks with request/response, synchronous, callback, failure, and cancellation
semantics. CodeBuild cloned authenticated Git revisions and ran checked-in or
explicit build specifications inside each project's exact configured image;
stop and workflow abort cancelled the real container. AWS Amplify encrypted
connected-repository credentials and executed backend, frontend, and test
pre/build/post phases with monorepo roots, environment precedence, declared
caches, and artifacts in a managed Python and Node.js image. Amazon Relational
Database Service ran real PostgreSQL, MySQL, and MariaDB data planes with
native TLS, IAM database authentication, live password rotation, and
stop/start volume persistence. Explicit AWS Lambda deployments and CodeBuild
workloads reached downstream AWS APIs through the standard global or
per-service endpoint coordinates. The production AWS console passed 241
Chromium tests and its authenticated browser matrix operated CodeBuild,
Amplify, and RDS through federated AWS credentials.

The AWS CLI harness provisioned and validated the official Session Manager
plugin when the host lacked it, so Amazon ECS ExecuteCommand coverage no longer
depended on undeclared host tooling. Route-conformance builds registered the
full AWS surface without starting runtime evaluator goroutines, removing the
store-rebinding race while production builds retained their Amazon CloudWatch
and Application Auto Scaling evaluators.

The CI closure kept the external-client contracts real. CloudWatch
metric-stream CLI coverage provisioned Amazon S3, IAM, and Amazon Data Firehose
resources instead of using placeholder ARNs. Azure Container Apps and Azure
Functions Terraform modules and examples used HashiCorp AzureRM 5.0.0, and the
production-shaped Azure simulator stack migrated every resource whose provider
schema became ID-based. The official provider completed a
Microsoft.Subscription apply, zero-drift plan, and destroy. Google Discovery
drift failures retained the exact newest response as a short-lived artifact;
the transient Cloud Resource Manager 20260715 rollout disappeared from every
sampled edge, so the pinned 20260709 documents remained the truthful source.
The Azure Terraform job installed Ubuntu's signed Caddy package through its
retry- and timeout-bounded APT path, so a third-party repository bootstrap
could no longer consume the provider test's execution budget. Microsoft.Network
subnets accepted AzureRM 5's plural `addressPrefixes` request and used it for
the real network fabric. Azure Container Apps environments that linked a Log
Analytics workspace explicitly selected the provider-required `log-analytics`
destination. Failed portal deletions kept their provider error inside the open
Fluent confirmation surface instead of racing with dismissal. The AWS Lambda
module's Step Functions differential role built its policy ARNs from the
module's declared `region` input, and all six production modules validated.
The complete Terraform tree also retained canonical HCL formatting.

Every AWS Terraform graph declared HashiCorp AWS provider 6.50.0, so the root
production graph and its sibling packages executed one reproducible provider
implementation. The root graph passed its complete concurrent apply, workload
assertions, refresh, and destroy through Caddy HTTPS with runtime Smithy
validation armed. The Microsoft Azure console's failed-delete assertion awaited
React Query's settled mutation before checking the retained accessible Fluent
dialog; all 131 package tests and the complete UI fan-out passed.

The final hosted freshness pass advanced the exact Google Discovery documents
for Bigtable Admin v2, Cloud Logging v2, Pub/Sub v1, and Cloud Resource Manager
v1/v3. The two methods newly present in those specifications were real
implementations: Bigtable memory layers retained enable/disable state and
etags and returned durable operations, while Cloud Resource Manager returned
resource-semantics metadata through its published v3 route. Authenticated
official-SDK transports exercised both methods, and generated surface coverage
measured Bigtable at 164 of 164 and Cloud Resource Manager v3 at 126 of 126.

AzureRM 5's complete external stack also converged after refresh.
Microsoft.OperationalInsights workspaces returned Azure's default public
network access values, and Microsoft.Storage File-share policies stayed
consistent between the ARM resource and Azure Files data plane. The official
Azure SDK round-tripped the share policy and Azure CLI round-tripped the
workspace defaults. The external stack's post-plan assertions used AzureRM
5's canonical Microsoft.Storage ARM IDs for Blob containers, Tables, and File
shares instead of the removed legacy data-plane IDs.

The pull-request CI freshness pass supplied the exact newer official Cloud
Logging v2 revision 20260724 and IAM Service Account Credentials v1 revision
20260723 Discovery documents. Their method, route, and schema-field sets were
unchanged; the newer descriptions and provenance pins were retained, and the
Google simulator route, specification, and measured-coverage suite passed.

The publication repair preserved current public contracts across the failing
client surfaces. Amazon SQS redrive used the normal enqueue path and therefore
assigned a new message ID, millisecond enqueue timestamp, FIFO sequence, and
destination delay; its validation audit used the current 1 MiB limit. An
omitted Amazon ECS launch type selected EC2 capacity rather than an AWS Fargate
sandbox. Azure Database for PostgreSQL flexible servers round-tripped their
top-level SKU through create, update, get, list, the official Azure SDK, and
the AzureRM provider. Google Cloud Run v1 collection validation located the
projected resource within the real shared collection. The Azure console's
embedded-root contract ran only in UI-bearing builds, while `noui` retained a
real 404. Google Cloud DNS and Artifact Registry specifications were refreshed
to Discovery revisions 20260723 and 20260724.

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
  Microsoft Graph endpoint override.
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
