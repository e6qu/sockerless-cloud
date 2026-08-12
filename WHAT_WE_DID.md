# WHAT WE DID

## 2026-08-12 — Polish pass: release verified, protection gated, nightly fuzz hardened

Verified the v0.2.0 release end to end: binaries and console bundles on the
GitHub Release, and — after the stale sockerless-owned GHCR packages were
removed — re-shipped the tag so `ghcr.io/e6qu/sockerless-simulator-<cloud>`
exists as `:v0.2.0-amd64`/`-arm64` plus the unsuffixed OCI index, owned by
this repository and public.

Ported sockerless's required-status-check contract:
`.github/required-status-checks.txt` mirrors `main`'s branch protection
(38 contexts, strict, linear history), `scripts/check-required-status-checks.sh`
fails any change that leaves a required context unemittable (pre-commit +
build-gates), and ci.yml gained the `At most one PR open` and
`Branch rebased on origin/main` jobs.

Hardened the nightly fuzz. `run-fuzz.sh` requalifies the one benign failure
shape Go's fuzz coordinator produces when it races its own `-fuzztime`
shutdown — a single FAIL block whose only diagnostic is a bare
`context deadline exceeded` at or past the fuzztime budget, with no failing
input written and no new crasher on disk (golang.org/issue/72104 tracks Go's
own CI flaking on the same signature); every neighboring shape (crasher,
panic, early deadline, second FAIL block) still fails, proven by positive and
negative controls plus an end-to-end crasher run. The aws nightly group went
from two shards (13m48s / 11m41s against a 15-minute limit) to three,
restoring real headroom.

Extracted the cloud simulators out of the sockerless monorepo into this
standalone repository. Flattened `simulators/*` to the repo root, renamed the
per-cloud directories to `simulator-{aws,gcp,azure}` (so `go install` produces
binaries with those names), folded each cloud's `shared/` module into its
cloud module as a package, and rewrote all module paths from
`github.com/sockerless/simulator*` to `github.com/e6qu/sockerless-cloud/*`.
Brought along: the sim console UI packages (+ `ui/packages/core`), the
vendored cloud API specs (`specs/cloud-api`, surface tables, behavioral
registries), the sim-scoped scripts and pre-commit hooks, the
Firecracker/realexec test harness, and the simulator jobs from CI (adapted
paths, workspace-based module resolution instead of `GOWORK=off`).
Fixed two errcheck violations in `testutil/registrytrust` that had never been
lint-gated in the monorepo.
