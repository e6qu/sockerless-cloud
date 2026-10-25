# STATUS

The state of the sockerless-cloud repository. `PLAN.md` holds the roadmap,
`BUGS.md` the open bugs and `WHAT_WE_DID.md` the decisions behind the code.

## Layout

- **Three installable simulators.** `simulator-aws/`, `simulator-gcp/` and
  `simulator-azure/` are root Go modules
  (`github.com/e6qu/sockerless-cloud/simulator-<cloud>`) with no `replace`
  directives, so `go install …@<release-commit>` works. Each is one binary on
  one `sim.Server` mux (default ports 4566, 4567 and 4568; Google Cloud's gRPC
  door on 4569). A `-tags console` build embeds the console built from `ui/`;
  a plain build serves the bundle `SIM_CONSOLE_DIR` names, or none.
- **Support modules.** `sim/` is the cloud-neutral framework; `ui-auth/` is
  the consoles' OpenID Connect layer and the `GET /monitoring/observation`
  endpoint; `testutil/` holds test-only helpers (`simready`, `freeports`,
  `baseimage`, `scratchimage`, `uniquename`, `httpsgateway`, `registrytrust`,
  `samlidp`, `githttp`), which the nine suites share instead of copying. The
  simulators pin `sim` and `ui-auth` by pseudo-version and resolve them
  locally through the root `go.work`.
- **Test modules.** `simulator-<cloud>/{sdk,cli,terraform}-tests` are separate
  Go modules with relative `replace` directives, never installed. Each
  suite's `TestMain` builds its simulator, starts it and waits for its
  `Listening on` banner.
- **Consoles.** `ui/` (Bun and Turborepo) builds `ui/packages/simulator-*`
  and `core`. A console reads only real cloud APIs and federates operator
  credentials through its cloud's own federation primitive.
- **Docs.** `docs/so-you-want-to-build-a-cloud.md` explains what a cloud is
  made of:
  - where the API specifications come from, and the core services in build
    order with the APIs to build first;
  - the `sim` library behind each service, and the control-plane mechanisms
    behind the public API;
  - what is still missing;
  - a comparison with LocalStack and Floci, with per-service feature tables
    and Mermaid diagrams of the three control planes.

  `docs/tutorial.md` builds one stack on all three simulators with Terraform.
  Its configurations live beside the tests that apply them (the `tutorial`
  directories of each simulator's `terraform-tests`), and CI runs them as
  `tf (aws tutorial)`, `tf (gcp tutorial)` and `tf (azure tutorial)`.
- **Specifications.** `specs/cloud-api/{aws,gcp,azure}` vendors the Smithy
  models, Discovery documents, Swagger documents, the AWS Service Reference
  and dated documentation pages, each row of a `SOURCES.md` pinned and
  traceable; `specs/SIM_SURFACE_TABLES/` holds the per-operation tables.
- **No built artifacts in git.** The console `dist/` directories are
  ignored; the network sidecar, the node agent and Amazon RDS's
  `AWSAuthenticationPlugin` are built on the container engine from embedded
  sources by `sim.EnsureEngineBuild`.

## The framework

`sim` owns startup (`sim.Main` with a `sim.Program`), the server and its
middleware chain, configuration from `SIM_*` variables, durable state on
SQLite (`Store`, `PrefixStore`, cached stores, generations and
`GenerationIndex`), the container-engine layer, the OCI Distribution data
plane, diagnostics (`/debug/inflight`, `/debug/stores`) and runtime
wire-shape validation. A cloud's error shapes, protocol routers, sandbox
profiles, console coordinates, request rewrites and registry behaviour live in
that cloud's module and reach `sim` through hooks.

Shared mechanisms, one package each:

| Area | Packages |
|---|---|
| Placement and machines | `placement` (container and microVM backends behind one interface, chosen by `SIM_PLACEMENT`), `nodeagent` (the Firecracker agent and its guest init), `machine` (VM lifecycle), `vminstance` (image catalogs, cloud-init over user data, an empty `/run` per boot), `workload` (container groups and builds), `replicas` (replica sets, the container probe runner and the crash-loop back-off), `revisions`, `fileshare` (NFSv4 shares to guests), `procimage`, `workloadhost` |
| Networks | `sim.EnsureCloudNetwork` with `ipam` and `netrules`, `sidecar` (resolver, metadata, NAT egress, credential forwarder), `dnsserver`, `lbplane` (load-balancer data plane, the host-routed front end `HostRouter` with its forward `Serve`, and HTTP, TCP and gRPC health probes) |
| Messaging and events | `signals` (keyed change signals; `sim.Announcing` stores announce their writes), `msgq` (leased queues and the long-poll `Waiter`), `streamlog` (partitioned logs), `eventbus` (fan-out with a filter hook, batching, input transformation, archive and replay), `delivery` (persisted push delivery with retries and dead letters), `cron` |
| Runs and functions | `jobrun` (executions of N tasks with parallelism, retries, timeouts, cancellation and restart recovery), `funcrt` (execution-environment pools, concurrency limits, provisioned environments and event-source pollers) |
| Storage and data | `blobstore`, `sparse`, `kvstore`, `listq` (offset, key and multi-column pages, filters, ordering), `logstore`, `dbengine` with `pgwire` and `mysqlwire` |
| Identity and keys | `simjwt`, `oidcfed`, `simkms`, registry authentication (`ociauth.go`) |
| Plumbing | `bg` (counted background work), `archive`, `Server.Call` (in-process calls through the whole request chain), `Wildcard` (the one `*` pattern matcher) |

## Services on the shared libraries

- **`msgq`**: Amazon SQS, Pub/Sub subscriptions, Azure Service Bus entities,
  Azure Queue Storage and Cloud Tasks queues. Its `Waiter` serves SQS
  `ReceiveMessage`, Pub/Sub `Pull`, `StreamingPull`, push and the dead-letter
  sweep, the Service Bus REST receive and AMQP session accept, and Cloud Tasks
  dispatch; Service Bus deduplicates through `Dedup.Admit` as SQS does.
  Dead-letter routing, Service Bus sessions and SQS message moves stay with
  each cloud.
- **`signals`**: every wait for a store change. No simulator keeps a
  hand-rolled channel map, and the tickers over the simulators' own stores
  are gone: Step Functions task tokens and `GetActivityTask`, synchronous
  Lambda durable invocations, Firestore `Listen`, the metadata server's
  `wait_for_change`, and operation and ARM asynchronous-operation waits all
  wake on the write. The Kudu log stream keeps the 1.5-second cadence of
  Kudu's own Linux file watcher, because the site's container writes those
  files.
- **`eventbus`**: Amazon EventBridge rules, scheduled rules, archives and
  replays; Amazon SNS, every protocol through one dispatcher with the
  per-protocol retry policy and the subscription's dead-letter queue; Azure
  Event Grid subscriptions; Pub/Sub topic fan-out and Eventarc Cloud Audit
  Logs triggers, both into queues that own their retries.
- **`cron` and `delivery`**: Cloud Scheduler, EventBridge Scheduler and
  Container Apps scheduled jobs; Cloud Tasks HTTP tasks go out through
  `delivery.Post`, keeping their attempts in the queue.
- **`lbplane`**: `HostRouter` fronts Cloud Run services and instances, Cloud
  Run functions, Container Apps ingress, App Service sites and slots, and
  AWS Lambda function URLs. Those and Amplify Hosting compute, Elastic Load
  Balancing, Application Gateway, Azure Load Balancer and the Google Cloud
  load balancers forward through `Serve`. Route 53 health checks probe
  through `ProbeHTTP` and `ProbeTCP`.
- **`replicas`**: Cloud Run startup probes and Container Apps startup,
  readiness and liveness probes run on `Probes`; Container Apps revisions and
  Cloud Run services and worker pools restart under `CrashLoopBackoff`.
- **`jobrun`**: Cloud Run jobs run one container group per task attempt with
  `CLOUD_RUN_TASK_INDEX`, `CLOUD_RUN_TASK_COUNT` and `CLOUD_RUN_TASK_ATTEMPT`,
  honouring parallelism, `maxRetries` and the per-attempt timeout; Container
  Apps jobs run their replicas under parallelism, `replicaCompletionCount`,
  `replicaRetryLimit` and `replicaTimeout`. Both resume after a restart.
  Amazon ECS `RunTask` and AWS Batch keep their own schedulers.
- **`funcrt`**: AWS Lambda reuses execution environments, scales out, is
  throttled by reserved concurrency and the account's unreserved concurrency,
  keeps provisioned environments warm, and polls Amazon SQS and Amazon Kinesis
  Data Streams event sources. Azure Functions keeps its App Service site
  instance and Cloud Run functions stay Cloud Run services on `replicas`.

## What the simulators serve

- **AWS**: the 42 vendored Smithy models are implemented or exempt in full.
  Requests are SigV4-verified and authorized by an IAM gate that derives each
  operation's resources from the Service Reference's ARN formats, resolves
  its declared condition keys or classifies them with a reason
  (`testdata/iam_condition_key_gaps.tsv`), and authorizes tag-on-create as the
  service's tagging action; Query, JSON, REST and Smithy RPC v2 CBOR routes
  all pass it.
- **Google Cloud**: every method spelling across the 33 vendored Discovery
  documents, Cloud Tasks and Cloud Scheduler among them, reaches a route that
  serves it or answers a declared 501; the gRPC door serves all but three
  methods, each named in the simulator's README. Every gRPC service is crossed against its REST door.
- **Azure**: 2,653 of 2,660 Swagger operations across 122 documents are
  served; the seven others answer a declared 501.
- **Coverage ratchets** hold a served floor and every document's declared
  total, refuse phantom coverage (a served method must reach a route naming
  its literal path segments), and require every unserved operation to answer
  its declared 501. The daily specification refresh pushes onto the open pull
  request; a served count that falls must be shown to be a withdrawal.

## Execution

- Every workload runs on a placement backend: a container on the engine by
  default, or a Firecracker guest on a node agent for the services
  `SIM_PLACEMENT` names. `SIM_RUNTIME=process` is API-only; any other mode
  refuses to start when no engine answers. The simulator process holds no
  host privilege, and a workload container holds no capability.
- Amazon EC2, Compute Engine and Azure virtual machine instances boot from
  each cloud's image catalog through `sim/machine`, run the image's cloud-init
  over their user data, stop and start on their own root disk, capture images
  and serve their console. Compute services — Amazon ECS, AWS Lambda, AWS
  Batch, AWS CodeBuild, AWS Glue, Amplify Hosting, Cloud Run services, jobs,
  worker pools and functions, Cloud Build, Container Apps apps and jobs,
  Container Instances, App Service and Azure Functions — run their workloads
  as real containers or guests, stream output to the cloud's log service, and
  honour the cloud's own timeouts and stop grace.
- A VPC, Compute Engine subnetwork or Azure virtual network prefix is an
  engine network over its own range; workloads on no cloud network join the
  platform network. Each network carries a sidecar at the address its cloud
  reserves for the resolver, serving DNS and the metadata endpoints under a
  minted token; workloads find metadata through the variables their SDKs
  honour. Each cloud's metadata service answers its own workloads and nothing
  else. NAT gateways, Cloud NAT and Azure NAT Gateway are egress sidecars.
  Security groups, firewall rules and network security groups apply on
  load-balancer flows and at a guest's tap.
- Managed engines run real servers: Amazon RDS and Aurora, Cloud SQL and Azure
  Database for PostgreSQL on PostgreSQL, MySQL and MariaDB; Memorystore for
  Redis and Redis Cluster on Redis; with snapshots, backups, replicas and
  failover on the engines' own mechanisms.
- With `SIM_PERSIST`, a stopping simulator leaves its containers, node agent
  and guests running and the next process adopts them by label; without it,
  the detached reaper and the startup sweep collect a run's containers,
  networks and volumes. A simulator exits when `SOCKERLESS_PARENT_PID` is
  gone.
- Every credential is verified: SigV4, Google Cloud and Microsoft Entra
  bearers against the simulator's signing keys, Azure Storage Shared Key and
  shared access signatures, Cosmos DB keys, and each registry's tokens.

## Consoles

- **AWS** (Cloudscape) follows the AWS Management Console: Console Home, the
  Services menu, unified search (Alt+S), the Region selector filled from
  `DescribeRegions`, per-service side navigation, PropertyFilter tables with
  remembered preferences, typed delete confirmations, and full-page creates
  for S3 buckets, EC2 key pairs, IAM roles and policies.
- **Google Cloud** follows console.cloud.google.com: the project picker,
  product search ("/"), the navigation menu with pinned products, the real
  console's URLs under `/ui/` with `?project=`, filter-bar tables, full-page
  creates with Equivalent code, and operations waited on through each API's
  own wait.
- **Azure** keeps its earlier portal shell; its rebuild on the Azure portal's
  patterns follows (`DO_NEXT.md`).
- **Browser suites** (Playwright, one per console) sign their operator in the
  way a deployment does: simulator-azure's Microsoft Entra ID is the OpenID
  Connect issuer, federated into AWS through `AssumeRoleWithWebIdentity` and
  into Google Cloud through Workforce Identity Federation, which each suite's
  `e2e/global-setup.ts` provisions through the real APIs. They drive create,
  list, describe and delete flows through the UI and run axe-core on every
  page: 123 tests for AWS, 116 for Google Cloud.
- **Harness requirements.** On Linux Playwright launches
  `ui/packages/core/e2e/isolated-chromium.sh`, which runs Chromium in a
  network namespace of its own under `unshare --user --map-root-user pasta`,
  forwarding the suite's loopback ports. The host needs `pasta` (the `passt`
  package; `scripts/install-passt.sh` installs the pinned Ubuntu build),
  unprivileged user namespaces (on Ubuntu 24.04,
  `kernel.apparmor_restrict_unprivileged_userns=0`) and a `/dev/net/tun` the
  user can open. `PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` names the Chromium to
  run; without it the suite runs the full Chromium build Playwright
  installed. On macOS Chromium launches directly. Playwright stops the
  backend with SIGTERM, so `start-backend.sh` removes its build and the
  simulators' workloads.

## Running the suites in a sandbox

- **gcloud**: unset `CLOUDSDK_PROXY_TYPE`, `CLOUDSDK_PROXY_ADDRESS`,
  `CLOUDSDK_PROXY_PORT` and `CLOUDSDK_AUTH_ACCESS_TOKEN` before the Google
  Cloud CLI and Terraform suites: an agent environment that points gcloud at
  its own proxy sends the suite's local calls to a relay that refuses them
  with 405, and a `no_proxy` naming `127.0.0.1` makes gcloud's regional hosts
  bypass the proxy the harness routes them through.
- **Azure**: `*.localhost` must resolve to loopback. A resolver that sends
  those names upstream breaks the Azure Terraform suites and the registry
  pushes of the Azure Container Registry Tasks tests (BUG-3228, BUG-3333).
- **Firewall and NAT data planes**: on some sandbox hosts (one ran its
  engine with `bridge-nf-call-iptables=1`) the Compute Engine firewall and
  Cloud NAT tests and the Amazon EC2 security-group and NAT gateway tests
  fail on every run (BUG-3652).
- **Disk**: the suites build simulators, images and Terraform provider
  caches, and a full disk fails them with ENOSPC in places that look
  unrelated. A simulator killed without its reaper leaves its networks
  behind; the next run reclaims a dead run's range, removing the dead run's
  own containers on it first.

## Gates

Every gate has been shown to fail on a planted violation of its own shape, and
a gate whose scan set can go empty exits non-zero.

- **Pre-commit** (`.pre-commit-config.yaml`): `check-simulator-tests.sh`
  (a registered operation needs SDK, CLI and Terraform tests or a
  `tests-exempt.txt` entry), `check-test-network-ranges.go`,
  `check-parallel-tests.go` (every top-level suite test is parallel or listed
  in `serial-tests.txt` with its reason), `check-fake-tests.sh`,
  `check-no-tool-absent-skips.sh`, `check-store-scans.sh`,
  `check-readonly-locks.sh`, `check-lock-pairing.sh`,
  `check-locked-helpers.sh`, `check-casefold-slice.sh`,
  `check-repo-config-sane.sh`, `scan-mux-overlap.sh`, the generated surface
  tables, the shard-coverage checks, `simulators-deadcode.sh`,
  `simulators-dupl.sh` (zero clone groups at 150 tokens),
  `simulators-knip.sh`, `gofmt`, `golangci-lint`, `shellcheck`,
  `terraform fmt`, workflow timeouts and concurrency, and the
  required-status-check manifest, which a pre-push hook also compares against
  `main`'s live branch protection.
- **CI build gates**: `check-support-module-pins.sh` (each pin carries the
  working tree's content), `check-installable-build.sh` (each simulator
  builds with `GOWORK=off`), `check-latest-deps.sh` (Go modules, Terraform
  providers, Actions, tools, npm packages and digest-pinned images at their
  newest release past a 24-hour quarantine), `simulators-jscpd.sh`, the
  pre-commit set again, and the spec-violation allowlists, which only shrink.
- **Tests**: spec conformance in every simulator's unit tests; the race
  detector over every module on every pull request; the nightly fuzz
  workflow over the Go modules.
- **Pull requests**: one open pull request (`check-single-open-pr.sh`, which
  ignores release-please's), rebased on `main`, with a Conventional Commit
  title. A support-module change lands in two pushes: push, pin the pushed
  commit in every requiring module, push again (`AGENTS.md` has the rule).

## Continuous integration

`.github/workflows/ci.yml` runs per-cloud lint and unit tests; the SDK, CLI
and Terraform suites in shards sized by measured cost, each test in exactly
one shard; microVM shards that place a service on Firecracker guests on the
runner's KVM; console vitest, typecheck, build and Playwright (each browser
job installs `pasta` and allows unprivileged user namespaces); race jobs per
module; the node-agent KVM proof; and the quality and build gates. Every job
holds a fifteen-minute ceiling. Each lint shard keeps its own Go and
golangci-lint cache and restores the newest older entry when its exact key
misses. A `warm-base-images` job fetches each base image set once per run
into a cache the other jobs restore, and every suite
takes base images through `testutil/baseimage.Ensure`; Docker's official
images come from `mirror.gcr.io`. A push to `main` whose tree already passed
on its pull request skips the suites (`scripts/ci-verified-on-pr.sh`).
Separate workflows refresh the vendored specifications daily, check
dependency freshness, publish per-commit container images and prune them.

## Releases

release-please (`release-please-config.json`) turns Conventional Commits on
`main` into one release pull request; merging it creates one `vX.Y.Z` tag and
the GitHub Release, and the `Release` workflow attaches the simulator binaries
with consoles embedded (linux and darwin, amd64 and arm64), the console
bundles and the per-architecture images composed into an unsuffixed OCI index,
then reconciles them (`scripts/verify-release-complete.sh`). Release images
are immortal; the per-commit image stream is pruned. Go consumers pin the
release commit. The latest release is v3.0.0, which lacks its darwin binaries
and AWS images (`PLAN.md` M1); release-please runs with the Actions token
until the owner installs a GitHub App for it (BUG-3465).
