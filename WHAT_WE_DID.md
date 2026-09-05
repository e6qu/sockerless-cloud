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
`AdmitRepository`, `Scope`). The stop and cancellation grace a workload gets is
a value the caller states rather than a constant one copy hardcoded.

A pin must carry the working tree's content, and `check-support-module-pins.sh`
fails when it does not: `ui-auth` had changed twice after its last pin, so the
installed binaries lacked the callback timeout fix while every workspace build
passed. A support-module change therefore lands in two pushes — push, pin the
pushed commit, push again — and a squash merge is content-identical to the
branch head it squashed, so the pin stays current on `main`.

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

The store-scan gate holds request-path full-store reads at zero: every
exemption the file ever carried was a keyed lookup on a second reading, and a
row indexed under every `/`-terminated prefix of its identifier answers a child
listing and a cascading delete from one `GenerationIndex`. The lock gates hold
read-only critical sections under exclusive locks, and `RLock`/`Unlock`
mismatches, at zero. The fake-test gate decides seven shapes of can't-fail test
from the syntax tree. The dead-code gate judges the framework from the three
programs that link it, so a framework function no simulator reaches is dead.

The race detector runs on every pull request over every module. The first run
reported 144 races in the AWS module, every one asynchronous simulator work
that nothing tracked; `simGo` and `simAfterFunc` count goroutines and pending
timers alike, `simJoinedGo` counts work a caller waits on and never drops it,
and `AwaitSimulatorBackground` drains to quiescence.

Dependencies of every class — Go modules, Terraform providers, GitHub Actions,
the tools a workflow installs, the consoles' npm packages — are held to their
newest release that has cleared a 24-hour adoption quarantine, and an unpinned
provider is a failure: `hashicorp/google` 8.0.0 reached CI 77 minutes after
publication and broke `main` by being unpinned.

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
