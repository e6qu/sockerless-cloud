# make/

Shared make infrastructure included by the repo-root Makefile and every leaf (per-app) Makefile. The authoritative specification of the standard target surface (`install` / `build` / `test` / `lint` / `clean` / `run` / `upgrade-deps`, path-delegation, conventions) is [`docs/MAKEFILE_STANDARD.md`](../docs/MAKEFILE_STANDARD.md) — this file is just the directory map.

## Include files

| File | Included by | Provides |
|---|---|---|
| `colors.mk` | everything else | TTY-detected ANSI colour variables (empty in CI logs) + the `STEP` banner macro. |
| `help.mk` | root + leaf Makefiles | Auto-generated `make help` listing every target with a `## description` comment; sets `help` as the default goal. |
| `go-app.mk` | Go-binary apps (backends, simulators, admin, …) | Standardized recipes for Go binaries. Leaf sets `APP_NAME` + `GO_PACKAGE` (optionally `UI_PACKAGE` for embedded UI bundles, `RUN_FLAGS` / `RUN_ENV` / `DEFAULT_PORT`, `GO_ENV`, `FAAS_SMOKE_TESTS`) before including. |
| `go-lib.mk` | Go library modules (e.g. [`realexec`](../realexec/README.md), [`ui-auth`](../ui-auth), [`testutil`](../testutil)) | Library recipes: `build` is a compile-check, `test` / `test-integration` (sim target) / `test-integration-cloud`, `lint` (vet + gofmt + golangci-lint when available), `upgrade-deps`. |
| `ui-app.mk` | `ui/packages/<x>/` Vite/Bun packages | UI recipes driven by `bun --filter` against the workspace root; leaf sets `UI_PACKAGE` to the full `@sockerless/...` package name. |

## Subdirectories

| Dir | Contents |
|---|---|
| `https-gateway/` | The `Caddyfile` the Terraform and CLI harnesses start a gateway from: per-cloud `https://{aws,gcp,azure}.sockerless.localhost:8443` virtual hosts reverse-proxying to the local simulators (ports overridable via `SOCKERLESS_*` env vars). CA trust setup and details in each simulator's README. |
