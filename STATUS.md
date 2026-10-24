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
  `baseimage`, `httpsgateway`, `registrytrust`, `samlidp`, `githttp`). The
  simulators pin `sim` and `ui-auth` by pseudo-version and resolve them
  locally through the root `go.work`.
- **Test modules.** `simulator-<cloud>/{sdk,cli,terraform}-tests` are separate
  Go modules with relative `replace` directives, never installed. Each
  suite's `TestMain` builds its simulator, starts it and waits for its
  `Listening on` banner.
- **Consoles.** `ui/` (Bun and Turborepo) builds `ui/packages/simulator-*`
  and `core`. A console reads only real cloud APIs and federates operator
  credentials through its cloud's own federation primitive.
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
| Placement and machines | `placement` (container and microVM backends behind one interface, chosen by `SIM_PLACEMENT`), `nodeagent` (the Firecracker agent and its guest init), `machine` (VM lifecycle), `vminstance` (image catalogs, cloud-init over user data), `workload` (container groups and builds), `replicas`, `revisions`, `fileshare` (NFSv4 shares to guests), `procimage`, `workloadhost` |
| Networks | `sim.EnsureCloudNetwork` with `ipam` and `netrules`, `sidecar` (resolver, metadata, NAT egress, credential forwarder), `dnsserver`, `lbplane` (load-balancer and front-end data plane) |
| Messaging and events | `msgq` (leased queues), `streamlog` (partitioned logs), `delivery` (persisted push delivery with retries and dead letters), `cron` |
| Storage and data | `blobstore`, `sparse`, `kvstore`, `listq`, `logstore`, `dbengine` with `pgwire` and `mysqlwire` |
| Identity and keys | `simjwt`, `oidcfed`, `simkms`, registry authentication (`ociauth.go`) |
| Plumbing | `bg` (counted background work), `archive`, `Server.Call` (in-process calls through the whole request chain) |

## What the simulators serve

- **AWS**: the 42 vendored Smithy models are implemented or exempt in full.
  Requests are SigV4-verified and authorized by an IAM gate that derives each
  operation's resources from the Service Reference's ARN formats, resolves
  its declared condition keys or classifies them with a reason
  (`testdata/iam_condition_key_gaps.tsv`), and authorizes tag-on-create as the
  service's tagging action; Query, JSON, REST and Smithy RPC v2 CBOR routes
  all pass it.
- **Google Cloud**: every method spelling across the 31 vendored Discovery
  documents reaches a route that serves it or answers a declared 501; the
  gRPC door serves all but three methods, each named in the simulator's
  README. Every gRPC service is crossed against its REST door.
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
  `simulators-dupl.sh` (zero clone groups at 200 tokens),
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
runner's KVM; console vitest, typecheck, build and Playwright; race jobs per
module; the node-agent KVM proof; and the quality and build gates. Every job
holds a fifteen-minute ceiling. A `warm-base-images` job fetches each base
image set once per run into a cache the other jobs restore, and every suite
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
release commit. The latest release is v2.4.0; releases are blocked by
BUG-3465 until its owner actions are done (`PLAN.md` M1).
