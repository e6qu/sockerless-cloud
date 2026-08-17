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
- **Branch protection**: `main` requires the 40 contexts mirrored in
  `.github/required-status-checks.txt` (strict, linear history);
  `scripts/check-required-status-checks.sh` gates the manifest against the
  workflows in pre-commit and build-gates, and
  `--verify-branch-protection` matches the live settings.
- **Container client**: the simulators use `github.com/moby/moby/client` +
  `github.com/moby/moby/api` (no `github.com/docker/docker` anywhere in the
  module graphs; govulncheck clean).
- **Measured floors**: IAM resource derivation 1,784 of 1,974 served
  operations; `network-arm-applicationgateway-2025-03-01` 22 of 22 (managed
  WAF rule-set catalog vendored); `web-arm-openapi-2025-03-01` 616 of 692; `cloudrun-v1` 152 of 152; `spanner-v1` 188 of 198;
  `containerregistry-dataplane-containerregistry-2021-07-01` 20 of 29
  (App Service Stages 1-5: child resources, site-scoped workflows, Key Vault
  configuration references, the complete Static Web Apps family);
  `keyvault-arm-managedhsm-2023-07-01` 6 of 16 (the Managed HSM pool's own
  lifecycle and both list scopes). VPC networks allocate bridge subnets from
  a host-side pool with ENI addresses as real secondary interface addresses,
  so same-CIDR VPCs coexist.
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
