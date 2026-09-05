# STATUS

Current state of the sockerless-cloud repository.

## Layout

- **Three installable simulators.** `simulator-aws/`, `simulator-gcp/`,
  `simulator-azure/` are root Go modules
  (`github.com/e6qu/sockerless-cloud/simulator-<cloud>`) with no `replace`
  directives, so `go install …@<release-commit>` works. Each embeds its console
  from a committed `dist/`.
- **One framework module.** `sim/` (`github.com/e6qu/sockerless-cloud/sim`)
  is the server, container-engine layer, durable state, OCI data plane,
  middleware and parsing primitives every simulator is built on. It is
  cloud-neutral: a cloud's error shape, protocol router, sandbox profile,
  console coordinates, request rewrite and registry behaviour live in that
  cloud's module and reach the framework through hooks. The support modules
  `realexec/`, `ui-auth/` and `testutil/` sit beside it.
- **Pins carry the working tree's content.** Each simulator pins `sim`,
  `realexec` and `ui-auth` by pseudo-version;
  `scripts/check-support-module-pins.sh` downloads every pinned version and
  fails when its content differs from the tree, and
  `scripts/check-installable-build.sh` builds each simulator with `GOWORK=off`
  the way `go install` and every SDK harness do. A support-module change lands
  in two pushes: push, pin the pushed commit, push again.
- **Console SPAs** build from `ui/` (Bun + Turborepo); they read only real
  cloud APIs and federate operator credentials through each cloud's own
  federation primitive. The consoles' OpenID Connect layer and the
  `GET /monitoring/observation` endpoint are one `ui-auth` implementation, and
  the Google Cloud access-token verifier exempts the monitoring path exactly as
  it exempts the console's session routes.

## Declared surface

- **AWS**: the 41 vendored Smithy models are implemented or exempt in full, the
  exemptions being the Amazon S3 bucket subresources the query-parameter table
  routes, each verified against that table. IAM resource derivation covers
  2,000 of 2,008 served operations; the eight that remain are requests naming
  no resource, and `"*"` is the honest answer. 1,406 of the 1,739 actions
  declaring an action condition key carry every one of theirs (BUG-2965
  measures the rest).
- **Google Cloud**: 5,486 of 5,486 Discovery method spellings across 30
  documents reach a route that names them; the gRPC surfaces serve 210 of 213
  methods, the three unserved each needing state the simulator does not hold.
  Every gRPC service is crossed against its REST door.
- **Azure**: 2,628 of 2,628 Swagger operations across 120 documents, App
  Service's 692 included.
- **Both ratchets refuse phantom coverage**: a served method must be answered
  by a route naming its literal path segments, and the routes that legitimately
  dispatch inside a handler are listed with the reason each one does. Every
  unserved operation answers a declared 501 naming what is missing, held by a
  gate rather than observed.
- **Vendored specifications track upstream** through the daily freshness
  workflow, which pushes a refresh onto the open pull request. A re-vendor
  moves declared totals; a served count that falls must be shown to be a
  withdrawal.

## Fidelity

- **Every workload is a real container** on the engine the simulator started
  against, under the cloud product's sandbox profile. Startup refuses to serve
  in any mode that executes workloads when no engine answers, after retrying
  the readiness ping for a budget. `SIM_RUNTIME=process` is API-only and says
  so.
- **Persistent workloads survive a restart.** With `SIM_PERSIST`, shutdown
  leaves the containers running and the next process adopts them by label;
  without it, the detached reaper and the startup sweep collect a run's
  containers, scoped to the state directory so a concurrent suite is never
  touched. A simulator exits when the process in `SOCKERLESS_PARENT_PID` is
  gone.
- **Every credential is verified**: SigV4 against the principal's stored
  secret, from the header and from a presigned URL alike; Google Cloud and
  Microsoft Entra bearers against the simulator's signing keys; the Azure
  Storage data plane's Shared Key and shared access signatures against the
  layouts Microsoft's own signers produce; Cosmos DB's shared key on every
  path; each container registry against the credentials its control plane
  mints, with its own challenge shape.
- **Managed databases run real engines** with volumes, credentials sealed
  under the simulator's own key service, readiness classified by SQLSTATE, and
  snapshots that capture the data copy-on-write where the volume store allows
  it.
- **The registries answer their own service**: Amazon ECR's empty ping with
  no content type, Artifact Registry's `text/html`, Azure Container Registry's
  `{}`; ECR hydrates a pull through a cache rule from the rule's upstream;
  Artifact Registry refuses the second write into an upload session; ACR keys
  its content per registry.
- **VPC networks** allocate bridge subnets from a host-side pool with the
  workload's elastic network interface address as a real secondary address,
  so same-CIDR VPCs coexist.
- **Declined surfaces are the ones whose required content is somebody else's
  data**: Cloud Spanner's Key Visualizer scans and wire-protocol adapter,
  Cloud KMS Key Access Justifications, Firestore's streaming REST spellings,
  Memorystore's RDB export and import, and Amazon SNS SMS and mobile push. Each
  answers by naming what is missing.

## Gates

Every quality gate has been shown to fail on a planted violation of its own
shape, and one whose scan set can go empty exits non-zero.

- Coverage ratchets with served floors and declared-total locks, per cloud,
  plus the phantom-coverage and unserved-declares-itself tests.
- `check-store-scans.sh` at zero request-path full-store reads;
  `check-readonly-locks.sh` and `check-lock-pairing.sh` at zero;
  `check-fake-tests.sh` holding five can't-fail shapes at zero and two at
  floors that may only fall; `check-casefold-slice.sh` and
  `check-locked-helpers.sh` scanning every module including `sim`.
- `simulators-deadcode.sh` per simulator, judging the framework from the three
  programs that link it; `simulators-dupl.sh` and `simulators-jscpd.sh` for
  copy-paste.
- `check-latest-deps.sh` holding Go modules, Terraform providers, GitHub
  Actions, installed tools and the consoles' npm packages to the newest
  release past a 24-hour adoption quarantine, with the repository's own modules
  excluded and covered by the pin gate instead.
- The race detector over every module on every pull request, with zero
  races held by `simGo`/`simAfterFunc`/`simJoinedGo` accounting.
- Spec conformance in every simulator's unit tests, and the runtime
  wire-shape validator over the SDK and CLI suites with allowlists that only
  shrink.
- The required-status-check manifest compared against the workflows in
  pre-commit and against `main`'s live protection at push time.

## Continuous integration

Per-cloud lint and unit tests; the Google Cloud and Azure SDK and CLI suites;
the AWS SDK suite in four shards and CLI suite in ten; Terraform in fifteen
shards; console vitest, typecheck, build and Playwright; the race jobs per
simulator and for `sim`; the quality gates; the one-open-pull-request and
rebased-on-main checks; the nightly fuzz workflow across the four Go modules.
Every job holds a fifteen-minute ceiling. Base images are warmed from one
cache entry per module, read out of the source by `scripts/base-images-for.sh`.

## Releases

One `vX.Y.Z` tag per release via release-please. The `Release` workflow ships
binaries with consoles embedded for linux/darwin × amd64/arm64, the console
bundles, and per-architecture container images composed into the unsuffixed
manifest list. Release images are immortal; the short-SHA stream is pruned to
twenty. Go consumers pin the release commit, since subdirectory modules cannot
carry a plain tag under this policy.
