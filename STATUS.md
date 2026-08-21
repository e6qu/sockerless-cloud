# STATUS

Current state of the sockerless-cloud repository.

## Snapshot

- **Repository born 2026-08-11** by extraction of `simulators/` (plus the sim
  console UI packages, vendored cloud API specs, sim scripts/hooks, and the
  Firecracker/realexec harness) from the
  [sockerless](https://github.com/e6qu/sockerless) repository, as a fresh
  snapshot without history.
- **Module layout**: `simulator-aws/`, `simulator-gcp/`, `simulator-azure/`
  are separate installable root modules
  (`github.com/e6qu/sockerless-cloud/simulator-<cloud>`); each folds its
  former `shared/` module in as a package. Support modules `realexec/`,
  `ui-auth/`, `testutil/` are required at tagged subdirectory versions —
  no `replace` directives in installable modules, so
  `go install github.com/e6qu/sockerless-cloud/simulator-<cloud>@<tag>` works.
  A repo-root `go.work` wires everything for local development; the
  sdk/cli/terraform test modules keep relative `replace` directives.
- **Console SPAs** build from `ui/` (Bun + Turborepo) and the built `dist/`
  is committed under each `simulator-<cloud>/dist/` so installed binaries
  ship the console.
- **CI** ports the simulator jobs from the sockerless repo: per-cloud lint +
  unit tests, gcp/azure SDK+CLI suites, AWS SDK (4 shards) + CLI (10 shards),
  Terraform (8 shards), UI vitest/typecheck/build, Playwright browser suites,
  dead-code/copy-paste quality gates (the deadcode gate covers `shared/`),
  spec-freshness and shard-coverage gates, the `At most one PR open` and
  `Branch rebased on origin/main` jobs, and the nightly fuzz workflow (aws in
  three shards; `run-fuzz.sh` requalifies Go's fuzztime-boundary shutdown
  race and nothing else).
- **Branch protection**: `main` requires the contexts mirrored in
  `.github/required-status-checks.txt` (strict, linear history), and the live
  setting matches the manifest — synced after the Azure CLI split merged, which
  also enforced eight Terraform contexts the manifest required and the setting
  had never carried. `--verify-branch-protection` reports a match.
  `scripts/check-required-status-checks.sh` gates the manifest against the
  workflows in pre-commit and build-gates, and
  `--verify-branch-protection` matches the live settings.
- **The App Service backup Terraform leg is back**: the azurerm 5.1.0 crash
  was run down to the provider dereferencing a backup schedule's start time
  before its nil check, triggered by the simulator serving a schedule without
  one — real Azure defaults it at save, and now so does the simulator. The
  storage plane also verifies the ten-field *account* SAS
  `data.azurerm_storage_account_sas` emits, which is the provider's own
  documented backup shape. Apply, idempotent plan and destroy proven in the
  Linux Docker harness.
- **The Azure Storage data plane authorizes every request**: Shared Key over
  the documented canonicalization (the Table service's own shorter string
  included), a service Shared Access Signature over the layout its `sv`
  defines, anonymous access only where the container's public access level
  allows it, and a Microsoft Entra bearer only with the storage audience — Get
  User Delegation Key is OAuth-only, as on Azure. Every layout is pinned by
  Microsoft's own signers: azblob, azqueue and azfile `SignWithSharedKey` /
  `GetSASURL`, and the az CLI across the CLI suite. The suites' clients hold
  the account's real `listKeys` key; the hardcoded CLI key constant is gone.
- **A virtual network creates its inline subnets**: the vnet document embeds
  full subnet objects, request and response alike, and an inline subnet
  materializes exactly as its standalone PUT does — including the 503 on a
  host without netns capabilities, where it used to answer 200 and drop the
  subnet.
- **Registries answer their own service**: the `/v2/` base endpoint sends what
  each registry sends — nothing at all from Amazon ECR, an empty `text/html`
  body from Google Artifact Registry, the two bytes `{}` from Azure Container
  Registry — captured from each with a token from its own token service. Amazon
  ECR hydrates a pull through a cache rule from the rule's upstream registry,
  creating the repository as the service does. Artifact Registry refuses the
  chunked upload Google documents it does not support, and refuses at the mint a
  repository scope an uncredentialled caller cannot reach.
- **Workload collection**: a run's containers are collectable from their labels
  alone. The detached reaper collects its own run, and the next simulator over
  the same state directory collects what a killed one left — the state directory
  is the identity that cannot be shared, so a concurrent suite's workloads are
  never touched.
- **Azure Cosmos DB coordinates**: an account name is a hostname, so it is
  global — a second account under a name another holds is refused — and the data
  plane reads the account out of the host the client dialled and nothing else.
  The sockerless-invented `x-ms-cosmos-account` header and the
  lexicographically-first-account fallback behind it are both gone.
- **Container client**: the simulators use `github.com/moby/moby/client` +
  `github.com/moby/moby/api` (no `github.com/docker/docker` anywhere in the
  module graphs; govulncheck clean).
- **Measured floors**: IAM resource derivation 1,691 of 1,979 served
  operations — the floor fell from 1,784 when the ratchet stopped crediting
  101 operations that belonged to the coverage table while being absent from
  the probe's own switch, and the condition-key ratchet likewise stopped
  asserting hand-written booleans no code had to agree with;
  `network-arm-applicationgateway-2025-03-01` 22 of 22 (managed
  WAF rule-set catalog vendored); `web-arm-openapi-2025-03-01` 616 of 692; `cloudrun-v1` 152 of 152; `spanner-v1` 188 of 198;
  `containerregistry-dataplane-containerregistry-2021-07-01` 20 of 29
  (App Service Stages 1-5: child resources, site-scoped workflows, Key Vault
  configuration references, the complete Static Web Apps family);
  `keyvault-arm-managedhsm-2023-07-01` 6 of 16 (the Managed HSM pool's own
  lifecycle and both list scopes). VPC networks allocate bridge subnets from
  a host-side pool with ENI addresses as real secondary interface addresses,
  so same-CIDR VPCs coexist.
- **Data races**: zero across all three simulator modules, held by the
  `race (simulator-*)` CI job rather than by memory. The first detector run of
  `simulator-aws` reported 144: asynchronous simulator work in untracked
  goroutines and timers, still reading package-level stores after the test that
  started it had ended, plus two shutdown paths that reported completion while
  work was still running — a shared-server handler chain that two concurrent
  first requests each built and wrote, and a TCP proxy whose Close waited for
  its accept loop but not for the handlers it had spawned. `background_work.go` counts goroutines
  (`simGo`) and pending timers (`simAfterFunc`) alike, and
  `AwaitSimulatorBackground` drains to quiescence, stopping timers that have
  not fired.
- **Request-path indexes**: every handler wrapper that decides whether to claim
  a request answers from an index keyed by the store's `Generation()` rather
  than by decoding every row — Elastic Load Balancing, AWS Amplify hosting,
  Azure Load Balancer, Azure Container Apps ingress, Azure Application Gateway
  and Azure Event Grid's publish scope, plus the Amazon ECS target resolution a
  profile of the deployed simulator found at 84.8% of all its CPU. Those
  wrappers run ahead of every service's handler, so the scan was paid by an
  Amazon DynamoDB call as much as by a proxied page load. `GenerationIndex`
  lives in `shared/index.go` in the two clouds that have such a wrapper — Google
  Cloud has none, and an abstraction with no caller is what the dead-code gate
  exists to refuse. Generations are unique across every store in the process, so
  a replaced store cannot be served a stale index, and
  `scripts/check-store-scans.sh` holds the remaining count — all of it behind a
  guard — to a floor that may only fall, now 14. Parent-scoped child
  collections are converted along with the single-row lookups: a row indexed
  under every "/"-terminated prefix of its resource identifier answers a
  listing and a cascading delete at any depth from one index, which is what
  took the Service Bus admin surface, the per-vault Key Vault listings, the
  Azure Files share families and the Table service's entity query, deletion
  and batch snapshot, the AWS Amplify hosted-content path, and the Route 53
  CNAME search that both certificate validation and domain verification make. Each conversion is held
  by a test that computes the same answer with the scan it replaced and
  requires the two to agree.
- **Error-path assertions**: every one of them names the code its service
  returns. The class began at 62 assertions that accepted any error at all —
  a transport fault, a 500 and a deserialisation failure all satisfied them —
  and `scripts/check-fake-tests.sh` now holds `any-error` at zero alongside its
  five other zero classes.
- **Lock discipline**: two gates at a floor of zero.
  `scripts/check-readonly-locks.sh` fails a read-only critical section that
  takes an exclusive lock, and `scripts/check-lock-pairing.sh` fails an `RLock`
  released with `Unlock` or a `Lock` released with `RUnlock` — a mismatch
  `go vet` cannot see and `sync` answers with a process-wide fatal error.
- **Data-plane authentication**: Azure Event Grid publish and all three
  container registries authenticate every caller against the credentials their
  control planes mint and rotate — Amazon ECR with Basic and a real twelve-hour
  authorization token, Google Artifact Registry and Azure Container Registry
  with each service's own Bearer challenge and refusal shapes. Each cloud
  injects its own authenticator into the shared registry through a nil-able
  hook, so no cloud-aware branch exists in the shared code.
- **Cross-resource-group moves**: twenty-nine Azure type keys move, including
  Microsoft.Network, each pinning the credential material its resource ID
  derives so a move never rotates a key. A general repointing pass rewrites
  inbound references held from outside the moved set, and the types Azure
  publishes as unmovable stay refused.
- **Data-plane authorization**: the Azure Cosmos DB data plane verifies the
  shared-key signature on every path through a middleware, and Amazon ECR
  refuses a repository it has no record of rather than creating it implicitly.
- **Asynchronous cloud operations**: Google Compute Engine's instance insert
  returns a running operation and boots behind it, and its operation reads and
  lists answer from the record rather than rendering invented completions.
- **Real engine readiness**: an Amazon RDS data plane serves a connection only
  once the engine accepts one, classified by SQLSTATE rather than by any byte
  arriving on the socket, on both the fresh-start and post-restart paths.
- **Azure Resource Manager resource lists**: `Resources_List` and
  `Resources_ListByResourceGroup` enumerate 56 tracked resource types from
  the cross-slice registry in `simulator-azure/resource_registry.go` — a
  table keyed the way `resourceMoveHooks` is keyed, reading each owning
  slice's store at request time — and honour the `$filter`, `$expand` and
  `$top` forms the Azure CLI and terraform-provider-azurerm send, refusing a
  filter naming anything ARM does not filter on.

- **Tests that cannot pass without proving something**: every simulator has
  been audited against a taxonomy of fake tests, each candidate judged by
  breaking the behaviour it names and watching whether it noticed, and the
  class is now held by a gate rather than by a reading.
  `scripts/check-fake-tests.go` decides seven of its shapes from the syntax
  tree and `scripts/check-fake-tests.sh` holds five of them at zero — no self
  comparison, no wait that cannot be false, no empty subtest, no table that
  never runs, no `t.Fatal` off the test goroutine — while error assertions that
  accept any error, and the other two populated classes, carry floors that may
  only fall. The suites
  that had never run — five Terraform packages absent from the makefile and the
  workflow, a security-group firewall test excluded by a shell filter, two fuzz
  targets aimed at routes that do not exist — run now, and gates make each
  class of drift impossible to repeat: a Terraform shard-coverage gate, a
  concurrency gate, and an adoption-quarantine fixture.
- **Dependency adoption quarantine**: `scripts/check-latest-deps.sh` requires a
  Go module, Terraform provider or GitHub Action to be at least 24 hours
  published before it is adopted, reporting a younger newest version as held
  rather than as drift. The window can only be lengthened, an unknown
  publication time fails loudly, and Go's zero-valued proxy timestamp is
  refused rather than treated as ancient. Vendored specifications are outside
  the quarantine by design: it mitigates executing code we install, and a
  discovery document is inert data our own suites validate.
- **Publication and retention**: container publishes are keyed per commit and
  never cancelled by a later merge, retention runs in its own workflow so two
  prunes cannot race, and it holds only prunable releases to the limit —
  versions coalesced per architecture onto immutable release tags cannot be
  deleted, and counting them made the limit unsatisfiable. Specification
  freshness holds a branch to what it changed, with an unbaselined daily run.

## Releases

- One `vX.Y.Z` tag per release via release-please (Conventional Commits →
  release PR → tag + GitHub Release); no per-module tags, no `latest`. The
  `Release` workflow ships binaries (consoles embedded, linux/darwin ×
  amd64/arm64), the console bundles, and `-amd64`/`-arm64` suffixed container
  images composed into the unsuffixed `vX.Y.Z` manifest list. Release images
  are exempt from GHCR retention; the short-SHA stream stays bounded at 20.

## Published

- **v0.2.0** is the first release-please release: one repository tag, with
  the Release workflow attaching binaries, console bundles, and the
  version-tagged multi-architecture images. The bootstrap `v0.1.0` tags
  (repository + per-module) were deleted; the v0.1.0 module versions
  survive only in the Go module proxy cache, which keeps the simulator
  modules' `require` graph resolvable.
  `go install github.com/e6qu/sockerless-cloud/simulator-<cloud>@<release-commit>`
  was verified from a clean module cache after the tag deletion, consoles
  embedded.

## Verified locally at extraction time

- `go build` + `go vet` green for all 6 root modules and 9 test modules.
- Unit tests green: simulator-aws (+shared), simulator-gcp, simulator-azure,
  realexec, testutil, ui-auth.
- UI: 12/12 turbo tasks green (build/typecheck/test × 4 packages);
  binaries with embedded consoles serve `/health` and `/ui/`.
- golangci-lint green for all root modules.
