# Makefile standard

Every independently buildable module in this repository has its own Makefile
with one target surface. The top-level Makefile delegates to those leaf
Makefiles and owns only fan-out targets, path delegation and the cross-cutting
Docker harnesses.

## Why

Per-module Makefiles keep build and run details beside the code they belong
to:

- Anyone working in `simulator-azure/` runs `make build` from inside that
  directory, and the Makefile there documents how that one simulator builds,
  embeds its console and runs.
- The top-level Makefile is an explicit module list, a fan-out helper and a
  path-delegation rule, and nothing else.
- A new module adds a leaf Makefile plus one list entry in the top-level
  Makefile.

## Inventory

### Go binaries with an embedded console (3)

| Module | Binary | Console package | Default port |
|---|---|---|---|
| `simulator-aws` | `simulator-aws` | `ui/packages/simulator-aws` | `:4566` |
| `simulator-gcp` | `simulator-gcp` | `ui/packages/simulator-gcp` | `:4567` |
| `simulator-azure` | `simulator-azure` | `ui/packages/simulator-azure` | `:4568` |

### Go libraries (4)

| Module | Provides |
|---|---|
| `sim` | the framework the three simulators are built on |
| `realexec` | the real-execution substrate: network namespaces, Firecracker microVMs, packet capture |
| `ui-auth` | the consoles' OpenID Connect session layer and the application-monitoring endpoint |
| `testutil` | test-only helpers: a git HTTP server and registry trust material |

### UI packages (4)

| Package | Embeds into |
|---|---|
| `ui/packages/simulator-{aws,gcp,azure}` | the corresponding simulator |
| `ui/packages/core` | shared library, no embed |

### Test modules (9)

`simulator-<cloud>/{sdk,cli,terraform}-tests` are Go modules of their own.
They are never installed, keep relative `replace` directives, and are driven
through the simulator's `sdk-test`, `cli-test` and `terraform-test` targets.

## Standard target surface

Every leaf Makefile implements these targets. A target that does not apply to
a module's kind prints why and exits 0, or aliases the nearest compile-check
target, so the target name always exists.

| Target | What it does |
|---|---|
| `help` | Print a one-line description of every target. Default goal. |
| `install` | Fetch dependencies (`go mod download` / `bun install`). Idempotent. |
| `build` | Produce the artefact. A simulator's `build` embeds the console when `ui/packages/<name>/dist` exists and falls back to `build-noui` otherwise; a library's is a compile check. |
| `build-noui` | Build with `-tags noui`; a compile check for libraries. |
| `test` | Run unit tests. |
| `lint` | `go vet` + `gofmt -l` + `golangci-lint` where installed; UI packages type-check with `tsc --noEmit`. Non-zero exit on findings. |
| `clean` | Remove the artefacts the module owns. |
| `upgrade-deps` | Bump every direct dependency to its newest release. This repository's own modules are skipped: a release pins them by commit, and `@latest` would walk that pin back to a deleted bootstrap tag the module proxy still serves. |

Optional targets, where they mean something:

| Target | Applies to | What it does |
|---|---|---|
| `embed` | simulators | Copy `ui/packages/<name>/dist` into the module's committed `dist/`. |
| `run` | simulators | Run the binary in the foreground on its default port. |
| `dev` | simulators | Run the Go server without the console beside the Vite dev server. |
| `race-test` | simulators, `sim` | The unit tests under the race detector (needs cgo). |
| `unit-test`, `sdk-test`, `cli-test`, `terraform-test`, `test-all` | simulators | The per-suite categories CI's jobs call directly; `test-all` runs all four. |
| `docker-build`, `docker-run`, `docker-test` | simulators | The container image, and the SDK/CLI/Terraform suites inside the shared `Dockerfile.test` image with the host engine socket mounted. |
| `preview` | UI packages | `vite preview` of the built bundle. |

## Leaf Makefile shape

A leaf Makefile carries data and one `include`; the recipes live in
`make/*.mk`.

```make
# simulator-gcp/Makefile

APP_NAME       := simulator-gcp
GO_PACKAGE     := .
UI_PACKAGE     := simulator-gcp
DEFAULT_PORT   := 4567
GO_ENV         := CGO_ENABLED=0
GO_LDFLAGS     := -s -w
RUN_ENV        := SIM_LISTEN_ADDR=:4567
REPO_ROOT_REL  := ..

include $(REPO_ROOT_REL)/make/go-app.mk
```

```make
# ui-auth/Makefile

REPO_ROOT_REL := ..

include $(REPO_ROOT_REL)/make/go-lib.mk
```

The simulator Makefiles add the per-suite test targets below the include, as
their own recipes, because those suites are separate Go modules with their own
deadlines and sharding.

## Shared `make/` includes

| File | Included by | Provides |
|---|---|---|
| `colors.mk` | everything | TTY-detected ANSI colour variables and the `STEP` banner macro. |
| `help.mk` | root and leaf Makefiles | The generated `help` target, and `help` as the default goal. |
| `go-app.mk` | the simulators | Recipes for a Go binary with an optional embedded console. |
| `go-lib.mk` | `sim`, `realexec`, `ui-auth`, `testutil` | Recipes for a Go library. |
| `ui-app.mk` | `ui/packages/*` | Recipes driven by `bun --filter` against the workspace root. |
| `https-gateway/` | the Terraform and CLI harnesses | The Caddyfile of the optional local HTTPS gateway. |

Per-file detail is in [`make/README.md`](../make/README.md).

## Top-level Makefile

The top-level Makefile has three jobs:

- Maintain the explicit lists: `GO_UI_APPS`, `GO_APPS`, `UI_APPS`, `TEST_DIRS`.
- Fan out the standard targets — `build`, `build-noui`, `test`,
  `test-integration`, `lint`, `lint-ui`, `clean`, `install`, `upgrade-deps` —
  plus `check-deps`, `check-workflow-timeouts` and `check-workflow-concurrency`.
- Delegate path targets: `make simulator-aws/build` runs
  `make -C simulator-aws build`, and the same form reaches every standard target
  in every leaf.

Cross-cutting harnesses stay at the top level because they span modules or
external tools: `docker-test`, `docker-test-build`, `firecracker-test` and
`realexec-network-test`.

## Operational checks

After changing Makefile plumbing, run the smallest target that proves the
changed path — `make help`, then the leaf's own target, for example
`make simulator-aws/build` or `make sim/test`.
