# make/

The include files the repository-root Makefile and every leaf Makefile share.
[`docs/MAKEFILE_STANDARD.md`](../docs/MAKEFILE_STANDARD.md) defines the target
surface they implement; this page maps the directory.

## Include files

| File | Included by | Provides |
|---|---|---|
| `colors.mk` | every other file | TTY-detected ANSI colour variables (empty in CI logs) and the `STEP` banner macro |
| `help.mk` | root and leaf Makefiles | `make help`, listing every target that carries a `## description` comment; `help` as the default goal |
| `go-app.mk` | the three simulators | Recipes for a Go binary. The leaf sets `APP_NAME` and `GO_PACKAGE`, and optionally `UI_PACKAGE` (the console to embed), `RUN_FLAGS`, `RUN_ENV`, `DEFAULT_PORT` and `GO_ENV`, before the include. |
| `go-lib.mk` | [`sim`](../sim/README.md), [`ui-auth`](../ui-auth), [`testutil`](../testutil) | Recipes for a Go library: `build` is a compile check; `test`, `lint` (`go vet`, `gofmt`, and golangci-lint when installed) and `upgrade-deps` |
| `ui-app.mk` | `ui/packages/<x>/` | Recipes driven by `bun --filter` against the workspace root. The leaf sets `UI_PACKAGE` to the full `@sockerless/...` package name. |

## Subdirectories

| Directory | Contents |
|---|---|
| `https-gateway/` | The `Caddyfile` of the local HTTPS gateway: `https://{aws,gcp,azure}.sockerless.localhost:8443` and the host-addressed data-plane wildcards, reverse-proxying to the simulators. `SOCKERLESS_*` variables move its ports. [`docs/configuration.md`](../docs/configuration.md#the-local-https-gateway) covers trusting its root. |
