# WHAT WE DID

What this repository built, and why it is shaped the way it is. The dated
record of each release is `CHANGELOG.md`; the bugs and their post-mortems are
in `BUGS.md`; the current snapshot is `STATUS.md`. This file keeps the decisions
that would otherwise have to be rediscovered.

## Origin

The cloud simulators were extracted from the sockerless monorepo into this
standalone repository as a fresh snapshot without history. The per-cloud
directories became `simulator-{aws,gcp,azure}` so `go install` produces
binaries with those names, and every module path moved to
`github.com/e6qu/sockerless-cloud/*`. The simulator console packages, the
vendored cloud API specifications with their surface tables and behavioural
registries, the simulator-scoped scripts and hooks, the Firecracker and
realexec harness, and the simulator jobs of the monorepo's CI came with it.

The project described itself as a component of the repository that consumed
it. That framing was removed: the simulators are a general-purpose
reimplementation of slices of the clouds, anything that speaks a cloud's API can
be pointed at one, and what is built on them is downstream.

## The framework is one module

Each simulator carried its own copy of the framework as a `shared/` package,
folded in so the installable modules needed no `replace` directive. The three
copies drifted: only the AWS copy resolved the runtime mode once and refused to
serve without a container engine, only the Google Cloud copy passed `Flush`
through the logging middleware so streaming handlers reached the client, only
Azure's retried the engine's readiness ping instead of reading one slow answer
as absence, the Google Cloud copy had lost the persistence envelope that keeps
`json:"-"` fields across a restart, and Azure's `WrapHandler` had lost the lock
that guards the handler chain. Fixes landed in one copy and never in the others.

The framework is the `sim` module now, pinned by pseudo-version like `realexec`
and `ui-auth`, and it holds the union of what the copies had. What is a cloud's
own stayed in that cloud's module and reaches the framework through hooks: the
error writers (`AWSError`, `S3ErrorXML`, `GCPError`, `AzureError`), the AWS
JSON and Query routers, the sandbox profiles, the console coordinates
(`ConsoleOptions`, with Azure's server-side Microsoft Entra federation broker
registered through it), the request rewrites (Amazon S3's zonal virtual-hosted
addressing and Azure Resource Manager's case folding, both
`Config.RewriteRequest`), and the registry behaviours the three container
registries disagree on (`BaseResponse`, `RefuseChunkedUpload`,
`AdmitRepository`, `Scope`). The stop and cancellation grace a workload gets
comes from the cloud's own setting — an Amazon ECS container definition's
`stopTimeout`, Cloud Run's ten seconds, a Container App template's
`terminationGracePeriodSeconds`, App Service's
`WEBSITES_CONTAINER_STOP_TIME_LIMIT` — rather than a constant one copy
hardcoded.

A pin must carry the working tree's content, and `check-support-module-pins.sh`
fails when it does not: `ui-auth` had changed twice after its last pin, so the
installed binaries lacked the callback timeout fix while every workspace build
passed. A support-module change therefore lands in two pushes — push, pin the
pushed commit, push again — and a squash merge is content-identical to the
branch head it squashed, so the pin stays current on `main`. The pin is then
moved onto the merge commit itself, because a pin on a branch head stops
resolving once that branch is deleted.

## Fidelity rules that came from bugs

- **A served count is not proof a handler exists.** A collection swallowed by a
  multi-segment wildcard counted as covered while no handler for it existed —
  Cloud Storage's per-object ACLs reached `objects.get`, which answered
  `object "doc.txt/acl" not found`. Both Google Cloud and Azure hold every
  served operation to a route that names its literal path segments; the routes
  that legitimately dispatch inside a handler are listed with the mechanism that
  makes each one legitimate.
- **An unserved operation declares itself.** A gap that stops declaring itself
  — a route that went away and answers the mux's 404 — holds the count and loses
  the declaration, and a client cannot tell a routing 404 from a resource that
  does not exist. Gates fail any unserved operation answering anything but a
  501 naming what is missing.
- **A registered operation is not an implemented one.** Amazon ECS had all 77
  operations registered while four agent-facing ones ignored every field and
  answered a canned acknowledgement, three force flags were parsed and dropped,
  and `DiscoverPollEndpoint` pointed agents at `amazonaws.com`. Handlers are
  audited by depth, and `scripts/classify-sim-handlers.go` marks the ones that
  answer without reaching state so the surface tables carry the marker.
- **Read the schema, not the floor comment.** Whole families were declined as
  "Microsoft's published catalogue" or "hardware telemetry" on a reading of the
  document the document did not support. App Service's outbound network
  dependencies are measurements an environment can make; a Cloud Interconnect's
  MACsec configuration is the caller's own keychain; a licence code is the
  project's own once Compute Engine issued it. Only a response whose *required*
  content the simulator would have to invent is declined, and it declines by
  naming what is missing. Published catalogues are vendored when they can be
  read deterministically — the Azure managed WAF rule sets, Google's
  interconnect locations, the Cloud Armor expression sets — with the source,
  the retrieval and the counts locked by a test.
- **A catalogue and a finding are different things.** A published set exists
  whether or not anyone asks, so answering one emptily is a false statement; a
  risk or a recommendation is something an analysis detected, so an empty
  collection says none was detected, which is true of a simulator that runs no
  analysis.
- **Judge a route on every client before believing it.** The generated Go
  client sends `softDeleted=true` and gcloud sends `softDeleted=True`; an exact
  comparison passed every SDK test and returned an empty list to the CLI. Query
  booleans go through `strconv.ParseBool`. Docker's `docker push` sends a blob
  in one `PATCH` and Podman sends it on the `PUT`, so a refusal added to the
  `PATCH` passed every local suite and broke CI: Artifact Registry refuses the
  *second* write into an upload session, which is the chunking Google names.
- **Real clients decide what a document does not list.** Cloud Run's condition
  `reason` is enum-typed and the simulator answered values the document omits;
  changing them broke `gcloud run jobs executions cancel`, whose poller compares
  against the literal `Cancelled`. The values stayed and the validator leaves
  those fields unjudged, with the evidence beside it.
- **A model can be stricter than the service.** Amazon S3 answers 204 to
  `PutBucketPolicy` where the model says 200; three Smithy patterns reject values
  AWS itself returns. Corrections live in `specs/cloud-api/aws/s3.supplement.json`
  and the spec-violation allowlists, each pinning the value it replaces and its
  evidence, so the check keeps running against what the service really sends.
- **Two APIs onto one setting are one store.** Blob soft delete was two stores
  — the ARM `deleteRetentionPolicy` and the data plane's service properties — so
  enabling it through Terraform gave permanent deletes. Cloud Run v1 is a
  projection over the v2 stores; the gRPC and REST doors of every Google Cloud
  service read one store, and a cross-door test writes through one protocol and
  observes through the other in both directions.
- **A stubbed external dependency fails loudly and names itself.** Amazon SNS
  SMS and mobile push need a carrier or Apple's and Google's hosts; each
  failure says so rather than reporting a missing `TopicArn`.
- **A retention the service reports is also a bound on what the simulator
  holds.** Stopped Amazon ECS tasks, temporary credentials and AWS WAF sampled
  requests were only filtered out of answers, so the Scaleway simulator reached
  21,409 task rows, 1.2 million credentials and 196,000 samples. Sweepers now
  delete each at its real lifetime through `Store.Prune`, which reads 500 rows
  at a time. Where deleting a record would change an answer, the answer moved
  into what the client presents: a session token carries its expiration under
  the simulator's key, so a pruned credential is still refused as expired.
  CloudTrail event history keeps 90 days apart from the copies each CloudTrail
  Lake event data store ingests and keeps for its own period, and CloudWatch
  Logs applies a group's `retentionInDays` to its events, not its streams.
- **A managed EBS volume is a block device, mounted by the engine.** A plain
  engine volume showed the workload the host's disk. The volume is an image
  file of the requested size and filesystem, and the container engine mounts it
  through its own `local` driver: the mount then happens on the engine's host,
  where the bind looks, and the simulator needs no privilege to make it.
  Snapshots stay file-level, which is what let volumes made before the change
  restore into it.

- **A trust policy is evaluated, and a federated identity is verified, as AWS
  does.** STS minted credentials for any role once a token parsed, and SAML
  took any base64. Assuming a role now evaluates its trust policy with the
  condition keys AWS documents for the caller — OpenID Connect claims under
  the provider's name, the `saml:` attributes of a signed assertion — and a
  SAML response is verified against the provider's own metadata. Tests sign
  real tokens and assertions rather than sending placeholders.
- **A request is authorized as the action AWS names, not as its operation.**
  The two differ for 94 operations: a multipart upload is `s3:PutObject`, a
  batch send is `sqs:SendMessage`, a re-encrypt is `kms:ReEncryptFrom` on one
  key and `kms:ReEncryptTo` on the other. The unambiguous ones are generated
  from the Service Reference; the rest are resolved from the request, down to
  each key of a batch delete and each statement of a PartiQL call.
- **Condition keys are read from the request the way the service defines
  them.** AWS CodeBuild's keys name the request member they read, so one
  resolver serves all of them from the declared list; other services register
  their request-settled keys per service. A key is populated only where AWS's
  definition fixes its value; where it does not — AWS Glue's Lake Formation
  keys — it stays absent rather than guessed.
- **Each service's keys are spelled the way that service spells them.** The
  gate used to write `<service>:ResourceTag/<k>` for every service, which is a
  spelling Amazon RDS does not have: RDS declares `rds:db-tag/`,
  `rds:cluster-tag/`, `rds:snapshot-tag/`, `rds:pg-tag/` and seven more, one
  per resource type, and the tags of a DB instance must not appear under the
  cluster's key. IAM's own users and roles carry `iam:ResourceTag/<k>`; its
  policies and instance profiles carry only `aws:ResourceTag/<k>`, because that
  is what the reference declares for them.
- **A request's tags are read in the shape that service sends.** The member
  path comes from each service's own vendored model — `Tags.Tag.N` for Amazon
  RDS, `Tags.member.N` for Elastic Load Balancing and IAM,
  `TagSpecification.N.Tag.M` for an Amazon EC2 tag-on-create, a lower-case
  `tags` list for Amazon ECS, `TagKey`/`TagValue` for AWS KMS, and a map for
  Amazon SQS and CloudWatch Logs. Before this, one awsQuery spelling was read
  and every other service settled no `aws:RequestTag/<k>` at all.
- **A key that says who called is only set when someone else called.**
  `kms:ViaService` and `kms:GrantIsForAWSResource` are built from the
  service-initiation the request carries, so a direct client call has neither
  and a policy that grants a key's use only through a service refuses the
  client. Wiring them exposed a dead end: the role check a service ran on the
  principal's behalf evaluated every action against a nil condition context, so
  a role policy scoped to one service could never match.
- **The engine is asked for nothing it has to interpret.** A managed EBS volume
  is a real filesystem on a real loop device: the simulator attaches the device
  and hands the daemon `/dev/loopN`, rather than passing an `o=loop` option that
  Podman implements in `mount(8)` and moby has never had in its flag table. The
  divergence cost a day of green local runs against red CI, and the rule it
  leaves is that a mechanism must not depend on which engine parses it.
- **A policy can bound a batch job and a grant.** The Amazon S3 batch-job and
  access-grant keys are read from the request and from the stored job, grant or
  location, so a statement that allows one job operation at a bounded priority
  allows exactly that. `s3:JobSuspendedCause` is the exception and says why: the
  service writes it, the model enumerates nothing, and no vendored document
  names a value.
- **An S3 operation is authorized in the namespace AWS publishes it under.**
  A directory bucket's access-point scope is an s3express action against an
  s3express ARN, the Outposts bucket listing an s3-outposts one; a route says
  which namespace authorizes it, and each is crossed against that namespace's
  own reference. The request-shape keys follow the same rule — a request
  settles `s3express:TlsVersion`, not `s3:TlsVersion`, when that is what it is
  authorized as.
- **What the gate cannot resolve is named, with the reason.**
  `TestIAM_DeclaredConditionKeysAreResolvedOrClassified` reads every condition
  key the vendored Service References declare and fails unless the gate names
  it or a row classifies it. A row states what the simulator would have to
  model first — a Nitro enclave's attestation document, an FIS experiment, a
  registered managed node, AWS Lake Formation — and a key that becomes
  resolvable makes its own row fail, so the list cannot rot into an excuse. The
  count that lived in BUGS.md was written by hand and had been read as
  authoritative; this one is measured on every run.

- **Maintenance may not end the service.** Failing loudly on a persistence
  fault is right in a handler, where net/http turns the panic into a 500. On a
  background goroutine it is a restart loop: the retention sweeper met a busy
  database, panicked, and took the simulator down thirteen times in thirteen
  minutes, each restart destroying every running task's network. Contention is
  now distinguished from corruption — the sweep yields and returns — and a
  panic in any background worker is contained where it happens.

## Execution

Every workload runs as a real container on the engine the simulator was
started against; there is no host-process path, and `SIM_RUNTIME=process` is
API-only. Startup refuses to serve in any mode that executes workloads when no
engine answers, because a process that passes its health check and fails every
workload in the background is worse than one that does not start. Each cloud
product's sandbox profile is applied to its containers — Lambda's read-only
rootfs and sandbox user, Fargate's capability set, Cloud Run's and Container
Apps' non-root defaults — and every profile refuses the host network and the
engine socket.

VPC networks take their bridge subnet from a host-side pool rather than the
VPC's own CIDR, so two live VPCs sharing a CIDR coexist; the workload's elastic
network interface address is a real secondary address on its interface, plumbed
by an ephemeral `CAP_NET_ADMIN` container so the workload keeps its
capability-free sandbox, as on Amazon ECS. The live networks are the allocator's
only ledger, which is what makes a restart safe.

A run's containers are collectable from their labels alone: a detached reaper
collects its own run, the next simulator over the same state directory collects
what a killed one left, and a concurrent suite's workloads are never touched.
Every test harness sets `SOCKERLESS_PARENT_PID`, and a simulator exits when that
process is gone, which closed the loop that had stranded simulators for days
after a killed `go test`.

The managed-database services run real engines. Amazon RDS, Cloud SQL and Azure
Database for PostgreSQL serve PostgreSQL, MySQL and MariaDB on named volumes,
with readiness classified by SQLSTATE rather than by any byte on the socket,
credentials sealed under the simulator's own key service, and snapshots and
backups that capture the volume with `cp -a --reflink=auto` — copy-on-write
where the volume store allows it, a full copy elsewhere, one code path either
way. A restore returns to the data as it was, which is the property that
separates a snapshot from a metadata row.

Firecracker boots Compute Engine and Azure virtual machines where the host
kernel allows it; a machine's disk outlives the guest process so a stopped
machine can be generalized and captured, and a deallocated machine keeps its
disk while a deleted one discards it.

A workload host pulls its image the way the cloud pulls it. Cloud Run pulls as
the project's Cloud Run service agent, so the simulator's Cloud Run and Cloud
Functions hosts mint that identity's access token from the simulator's own
signer and present it, as the `oauth2accesstoken` password the engine's
`RegistryAuth` carries, to Artifact Registry and Container Registry — named by
their hosts or by this simulator's own port — and present nothing anywhere
else. Before that, an authenticating Artifact Registry accepted a build's push
and refused the host's pull of the same image; the pull the registry's push
test made went through the Docker CLI and its login, which is not the host's
pull.

Cloud Build's docker steps had run with the simulator host's own docker
configuration, so a Dockerfile whose base image lives in Artifact Registry
was pulled anonymously and refused once the registry enforced its credential.
The steps now run with a configuration built on the host's — CLI plugins,
contexts and settings kept — whose credential helper answers Google's
registries with the build service account's token and hands every other
registry to the helper the host configured, as Cloud Build's builder does
through the gcloud helper. An Azure Container Registry Tasks run had the
same gap: its push into the registry it runs in was refused once the
registry enforced its credential, so its steps run with the same kind of
configuration — the framework's `sim.WriteDockerConfig` — whose helper
answers the registry's login server with an identity token of the run, the
form `az acr login` stores, and a run scheduled on a registry that does not
exist is refused as the service refuses it. And a bucket now carries the four legacy bindings
Cloud Storage grants at creation, so Terraform's removal of the one member it
added sets the defaults back instead of an empty policy the service refuses.

The Lambda and Amazon ECS hosts had run a pull-through-cache reference as a
Docker Hub name spelt from its path, which only the `docker-hub` rule's
spelling survived; they now resolve the reference through the registered
rule to its upstream, as ECR hydrates the cache. And the Functions host had
told an HTTP-bootstrap site from a one-shot one by the image path containing
`sockerless-overlay`, a consumer's convention; it now reads what the site
declares in its app settings. Cloud KMS refuses a key ring in a location
the service does not have — `US`, Cloud Storage's spelling of its
multi-region, where Cloud KMS names it `us` — as the real service does.

A privileged CodeBuild build environment runs docker steps against the
engine the mode grants it, curated images resolve to their ECR Public
distribution, a build's environment reaches this simulator's services with
instance-metadata credentials, and its output streams to CloudWatch Logs
as the service does by default; a reference that names the simulator's own
port is pulled by the Lambda and ECS hosts from the simulator's ECR with an
authorization token. Together these are the path a backend's bootstrap
overlay takes — CodeBuild builds and pushes, Lambda pulls — with a
relocated registry coordinate, the way Cloud Build and Cloud Run already
did (BUG-2991, 2993). The framework pulls an image the host holds for
another architecture rather than running it (BUG-2992).

The Azure workload hosts had pulled every image anonymously, whatever the
workload declared — a Container App's or Job's `registries` entry, a site's
Container Registry managed identity or `DOCKER_REGISTRY_SERVER_*` settings —
so a consumer's overlay image in a registry that enforces its credential
could not start. They now pull with what was declared: an identity token of
the named managed identity, which the engine exchanges through the
registry's refresh-token grant, or the named username and password. And the
build services' docker configuration names its registries outright, because
the legacy `docker build` a host without buildx runs sends the daemon only
the credentials of registries the configuration names, and asks for nothing.

An Amazon ECS container definition's `workingDirectory` had been kept only in
the bytes the simulator echoes back; the task's process started in the
image's directory and so did an ExecuteCommand session in it. The runtime
now hands the directory to the engine, which creates it when the image does
not hold it, the way Amazon ECS does — a consumer's `docker run -w /src`
found the gap when act started a job container in a checkout directory.

A simulator resource records the process that owns it — hostname and pid —
because a run id alone cannot say whether a run is over. The VPC subnet
reclaim had read "a different run id and no attached container" as "a dead
run's leftover" and taken a live neighbour's idle network between two of its
workloads; it now reclaims only from an owner on this host whose pid no process
holds, and leaves what it cannot check to the allocator, which takes the next
slice.

## Authorization and authentication

Every credential is verified. AWS requests are SigV4-verified against the
principal's stored secret before any identity is trusted, from the
`Authorization` header and from a presigned URL's `X-Amz-Credential` alike;
Google Cloud and Microsoft Entra bearers are verified against the simulator's
own signing keys; the Azure Storage data plane verifies Shared Key over the
documented canonicalization and service shared access signatures over the layout
the signature's own version defines, pinned by Microsoft's own signers; Cosmos
DB verifies its shared-key signature on every path through a middleware; all
three container registries authenticate against the credentials their control
planes mint, each with its own challenge shape.

AWS IAM enforcement derives the resource an action authorizes against from the
types AWS declares and the ARN format published beside each — 2,000 of 2,008
served operations, the eight that remain naming no resource at all — and
populates the condition keys the request itself settles. A create authorizes
against its type's wildcard rather than `"*"`, which matches only a policy whose
own `Resource` is `"*"`. A resource-policy statement naming the caller grants;
one matching only by account delegates to that account's IAM, which is what the
default AWS KMS key policy means and what reading it as a grant had silently
defeated.

The Amazon S3 control plane is gated like the data plane. Each `/v20180820`
route declares the operation it serves and the resource AWS authorizes that
operation against — an access point, an Access Grants instance, location or
grant, a batch job, a Multi-Region Access Point by its alias, a Storage Lens
configuration or group, or the ARN the shared tagging trio names outright —
and the gate runs the same `iamAuthorize` the rest of the surface does. A
route whose action declares no resource type authorizes `"*"` because that is
what the reference says about the action, and a test crosses every route
against it so a re-vendor moves both together. Registering an Access Grants
location also authorizes `iam:PassRole` on the role it hands S3, which meant
teaching the passed-role scan to read an XML document. The seven routes whose
actions live in the `s3express`, `s3-outposts` and `s3-object-lambda`
namespaces stay ungated and listed as such: no reference for those is
vendored, and inventing an action or a resource denies grants real AWS
honours (BUG-3014).

Google Cloud's `testIamPermissions` answers from the stored policy resolved
through the vendored curated roles and the held custom roles, and a caller
presenting no simulator-issued token is the account's operator.

## Measurement and gates

Every quality gate has been shown to fail on a planted violation of its own
declared shape, and a gate whose scan set can go empty exits non-zero rather
than green: two gates had named the monorepo's directories since the extraction
and had scanned nothing, one of them hiding two live slice-bounds panics from
Unicode-aware lowercasing of client input.

The coverage ratchets hold both a served floor that may only rise and the
declared total of every vendored document, so a re-vendor that adds a surface
fails until it is served or declared. A re-vendor can also *withdraw* a
surface, as Cloud Build's `gitLabConfigs` collection was withdrawn, and the
floor comment has to say which methods moved and why.

What a re-vendor adds is served wherever the document describes something the
simulator already holds. Cloud Resource Manager's `capabilityConfigs` — which
capabilities are enabled over an organization, a folder or a project and its
sub-tree, over which boundaries, in which management project — arrived that
way and is served from one store under all three parents, because it is the
same kind of record as the folder capability toggle beside it and because the
management project the API creates when a caller supplies none is a project,
which this simulator models: the name the config reports resolves through the
same `projects.get` every other client uses. Cloud SQL's `workloadCaptures`
arrived at the `v1` spelling a re-vendor after the `v1beta4` one and needed
nothing, because the module mounts itself on both prefixes and the
declared 501 was already answering there — the captures stay declared, since
the data plane relays the engine's wire protocol without reading a query out
of it and a replay reported as RUNNING would be invented.

The store-scan gate holds request-path full-store reads at zero: every
exemption the file ever carried was a keyed lookup on a second reading, and a
row indexed under every `/`-terminated prefix of its identifier answers a child
listing and a cascading delete from one `GenerationIndex`. The lock gates hold
read-only critical sections under exclusive locks, and `RLock`/`Unlock`
mismatches, at zero. The fake-test gate decides seven shapes of can't-fail test
from the syntax tree. The dead-code gate judges the framework from the three
programs that link it, so a framework function no simulator reaches is dead.

A slow phase of an Amazon ECS task start is attributed by measuring it where
the simulator runs, never by reading the code. The `vpc:egress` and
`vpc:security-groups` phases cost 3-6 s and 1.6-3 s on the Scaleway stack,
where the simulator runs inside a Firecracker microVM, and fixes aimed at the
`nft` commits and timings taken on the host changed nothing. Sub-phase marks
reported from the guest, and goroutine samples taken through its diagnostics
listener during a start, put the time in decoding the whole `ecs_tasks` store:
the stopped-task sweep had never deleted a row (BUG-3006). The marks travel on
the context (`realexec.WithMark`), because every start in a VPC shares one
`realexec.Network`.

The race detector runs on every pull request over every module. The first run
reported 144 races in the AWS module, every one asynchronous simulator work
that nothing tracked; `simGo` and `simAfterFunc` count goroutines and pending
timers alike, `simJoinedGo` counts work a caller waits on and never drops it,
and `AwaitSimulatorBackground` drains to quiescence.

Dependencies of every class — Go modules, Terraform providers, GitHub Actions,
the tools a workflow installs, the consoles' npm packages — are held to their
newest release that has cleared a 24-hour adoption quarantine, and an unpinned
provider is a failure: `hashicorp/google` 8.0.0 reached CI 77 minutes after
publication and broke `main` by being unpinned. A deliberate hold names its
cause and goes when the cause does: Fluent UI 9.74.6 failed every Azure console
test because tabster 8.8.0 shipped no `exports` map, so Vitest resolved its
CommonJS entry and found no named `createTabster`; tabster 8.8.1 added the map
and the Fluent pin went with it. TypeScript 7 rejected the consoles' side-effect
`./index.css` imports (TS2882) until each console declared the `*.css` module;
Vite bundles them regardless, so the built consoles did not change.

## Continuous integration

Jobs are held to fifteen minutes; a timeout kill is reported as "cancelled",
which is what let two of them read as infrastructure noise. The AWS SDK and CLI
suites are sharded on measured time, with gates holding every test to exactly
one shard. Base images are read out of the source by
`scripts/base-images-for.sh` and warmed from one cache entry per cloud, because
the ECR Public Gallery caps anonymous pulls by data volume, which no retry
recovers from. The Azure SDK suite starts Microsoft's Cosmos DB emulator once,
from `TestMain`, because two emulators on a two-core runner starve each other.

Publishes are keyed per commit and never cancelled by a later merge; retention
runs in its own workflow and spares anything younger than two hours, because a
publish between its two per-architecture pushes is indistinguishable from an
abandoned remnant. Releases are one `vX.Y.Z` tag through release-please, and the
required-status-check manifest is compared against `main`'s live branch
protection at push time, since protection drifts with nobody's commit.

## ECS placement is grounded in the host

RunTask on Amazon ECS can refuse a task at placement time — HTTP 200, empty
`tasks[]`, an entry in `failures[]` — and a consumer sized for concurrency
meets that refusal as the normal case, not the exception. The simulator runs
real containers on one finite host, so rather than inventing a capacity
number it commits each placed task's declared memory and CPU (the same figures
its containers' cgroups are bounded to) against what the simulator's own
cgroup, or the machine, can hold, and refuses with the real shape when the
next task would not fit (`simulator-aws/ecs_placement.go`, BUG-2994). The
ledger is the task store plus in-flight reservations, decided before any
allocation happens. The consequence for operators: the simulator container's
memory and CPU limits are its Fargate capacity.

## DynamoDB's provisioned throughput is enforced, at the table's grain

A PROVISIONED table's read and write units were stored and never spent, so a
consumer's retry-with-backoff — the path every SDK ships for
`ProvisionedThroughputExceededException` — never ran here (BUG-2995). The
choice was between a crude "N requests per second" limiter, which would have
been a new fake behaviour, and the service's own model. The service's model
is a token bucket per table and per global secondary index, refilled at the
provisioned rate and holding 300 seconds of unused capacity as burst; the
simulator now runs exactly that, spending the units its `ConsumedCapacity`
accounting already computed, at the granularity a single-partition table has
(`simulator-aws/dynamodb_throughput.go`). Batches return throttled entries as
unprocessed rather than failing, on-demand tables are never throttled, and
`CreateTable` applies the rule that made the numbers real in the first place:
a provisioned table states its units, an on-demand one does not.

## The shared test harness image is in the repository

README, the Makefile standard, four Makefiles and the Azure and Google Cloud
Terraform harnesses all named `Dockerfile.test`, and none of them had it — the
file matched `.gitignore`'s `*.test`, the rule for compiled Go test binaries,
so it was never committed — every `make docker-test` and every macOS run of
those suites failed at "open Dockerfile.test". It is committed now, un-ignored
by name, with every toolchain pinned to a version and a digest (Go, Terraform,
the three cloud CLIs, Firecracker, Caddy, the Docker CLI), because the image
decides what a test result means. With it, the shared azurerm stack's
Firecracker guest was verified to boot on an arm64 host (BUG-42).
