# WHAT WE DID

## 2026-08-11 — Repository extracted from sockerless

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
