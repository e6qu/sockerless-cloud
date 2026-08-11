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
  dead-code/copy-paste quality gates, spec-freshness and shard-coverage gates,
  and the nightly fuzz workflow.

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
