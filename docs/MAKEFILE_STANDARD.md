# Makefile standardization

Every independently-buildable app in this repo has its own Makefile with a consistent target surface. The top-level Makefile delegates to those leaf Makefiles and owns only fan-out targets, cross-cutting test suites, and stack orchestration.

## Why

Per-app Makefiles keep build and run details beside the app they belong to. The top-level Makefile stays as a thin orchestrator:

- Anyone hacking on `backends/ecs` runs `make build` from inside that dir, no `cd ../..; make build-ecs-with-ui` ceremony.
- `simulator-azure/Makefile` documents how that one sim builds + runs, in the place a developer would look first.
- The top-level Makefile is an explicit app list, fan-out helper, path delegation rule, and stack orchestration include.
- New backends, simulators, UI packages, and test harnesses add a leaf Makefile plus one list entry in the top-level Makefile.

## Inventory of independently-buildable apps

The top-level app lists currently cover three kinds of independently buildable packages:

### Go binaries with optional embedded UI (13)

| App | Binary | UI package consumed | Default port |
|---|---|---|---|
| `cmd/sockerless-admin` | `sockerless-admin` | `ui/packages/admin` | `:9090` |
| `backends/docker` | `sockerless-backend-docker` | `ui/packages/backend-docker` | `:3375` |
| `backends/ecs` | `sockerless-backend-ecs` | `ui/packages/backend-ecs` | `:3375` |
| `backends/lambda` | `sockerless-backend-lambda` | `ui/packages/backend-lambda` | `:3375` |
| `backends/cloudrun` | `sockerless-backend-cloudrun` | `ui/packages/backend-cloudrun` | `:3375` |
| `backends/cloudrun-functions` | `sockerless-backend-gcf` | `ui/packages/backend-gcf` | `:3375` |
| `backends/aca` | `sockerless-backend-aca` | `ui/packages/backend-aca` | `:3375` |
| `backends/azure-functions` | `sockerless-backend-azf` | `ui/packages/backend-azf` | `:3375` |
| `simulator-aws` | `simulator-aws` | `ui/packages/simulator-aws` | `:4566` |
| `simulator-gcp` | `simulator-gcp` | `ui/packages/simulator-gcp` | `:4567` |
| `simulator-azure` | `simulator-azure` | `ui/packages/simulator-azure` | `:4568` |

### Go binaries / libraries (no UI) (7)

| App | Binary |
|---|---|
| `cmd/sockerless` | `sockerless` (CLI) |
| `agent` | `sockerless-agent`, `sockerless-lambda-bootstrap`, `sockerless-cloudrun-bootstrap`, `sockerless-gcf-bootstrap`, `sockerless-azf-bootstrap` |
| `backends/aws-common` | Go library shared by the ECS + Lambda backends |
| `realexec` | Go library for the shared real-execution substrate |
| `github-runner-dispatcher-aws` | dispatcher binary |
| `github-runner-dispatcher-gcp` | dispatcher binary |
| `github-runner-dispatcher-azure` | dispatcher binary |

### UI packages (14)

| Package | Embeds into |
|---|---|
| `ui/packages/admin` | `cmd/sockerless-admin` |
| `ui/packages/backend-{docker,ecs,lambda,cloudrun,gcf,aca,azf}` | corresponding backend |
| `ui/packages/simulator-{aws,gcp,azure}` | corresponding simulator |
| `ui/packages/core` | (shared lib — no embed) |

## Standard target surface

Every leaf Makefile MUST implement these 7 targets. Targets that don't apply to a given kind (e.g. `run` on a UI package) call `@echo "n/a for this app type"` and exit 0, or alias the nearest compile-check target for libraries. The contract is "the target name always exists."

| Target | What it does |
|---|---|
| `help` | Print one-line description of every target in this Makefile. Default goal. |
| `install` | Fetch deps (`go mod download` / `bun install`). Idempotent. |
| `build` | Produce the artefact. For Go-with-UI apps, `build` embeds the UI; use `build-noui` to skip. |
| `test` | Run unit tests. Fast — must complete in under a minute on a clean cache. |
| `lint` | Static checks (`go vet` + `gofmt -l` + UI: `tsc --noEmit`). Non-zero exit on findings. |
| `run` | Run the binary in the foreground with sensible default flags. UI packages run `vite dev`. |
| `clean` | Delete build artefacts owned by this app (binary, `dist/`, `.test` caches). |

Optional targets, when meaningful:

| Target | Applies to | What it does |
|---|---|---|
| `build-noui` | Go-with-UI apps and Go libraries in the top-level no-UI fanout | Build the binary with `-tags noui`, or compile-check libraries. |
| `embed` | Go-with-UI apps | Build the UI + copy `ui/packages/<x>/dist` → local `dist/`. Implicit dep of `build`. |
| `test-integration` | apps with `_integration_test.go` | Run the build-tag-gated integration tests. |
| `test-faas-smoke` | FaaS backends | Run the backend's runner-shaped simulator smoke (`create -> start -> exec×N -> wait -> remove`). No-op with a clear message when `FAAS_SMOKE_TESTS` is unset. |
| `dev` | Go-with-UI apps | Run Go server (`-tags noui`) + Vite dev server in parallel. |
| `preview` | UI packages | `vite preview` — serve the built bundle locally. |
| `start` / `stop` | Go binaries | Background daemonization with PID file. (Optional — see "stack" below.) |

Simulator Makefiles additionally expose the per-suite test categories CI's sim jobs call directly: `unit-test` (the sim module's own package tests, including the spec-conformance gates against [`specs/cloud-api/`](../specs/cloud-api/README.md)), `shared-test`, `sdk-test`, `cli-test`, `terraform-test`, and `test-all` (all five), plus `docker-build` / `docker-run` / `docker-test`.

## Per-app Makefile shape

Each leaf Makefile is small — 5–10 lines of variables + one `include`. Example:

```make
# backends/ecs/Makefile

APP_NAME      := sockerless-backend-ecs
GO_PACKAGE    := ./cmd/sockerless-backend-ecs
UI_PACKAGE    := backend-ecs
DEFAULT_PORT  := 3375
RUN_FLAGS     := --addr :$(DEFAULT_PORT)

include ../../make/go-app.mk
```

```make
# ui/packages/admin/Makefile

UI_PACKAGE := admin
DEV_PORT   := 5173

include ../../../make/ui-app.mk
```

```make
# simulator-aws/Makefile

APP_NAME      := simulator-aws
GO_PACKAGE    := .
UI_PACKAGE    := simulator-aws
DEFAULT_PORT  := 4566
GO_FLAGS      := GOWORK=off       # this module is outside the workspace
RUN_FLAGS     := -addr :$(DEFAULT_PORT)

include ../../make/go-app.mk
```

Convention: leaf Makefiles only carry **data** (the table above). All recipe code lives in `make/*.mk`.

## Shared `make/` includes

```
make/
├── colors.mk             # Pretty output: $(CYAN), $(GREEN), $(RESET) helpers
├── components.mk         # Per-component start/stop/rebuild/log/status targets
├── go-app.mk             # Recipes for Go-binary-with-optional-UI apps
├── go-lib.mk             # Recipes for Go libraries (test/lint/clean only)
├── help.mk               # Auto-generated `make help`; sets `help` as default goal
├── ui-app.mk             # Recipes for UI packages
├── stack.mk              # Pre-canned dev-stack recipes used by top-level
├── https-gateway/        # Caddyfile for the optional local HTTPS gateway
└── observability-config/ # Default configs for the stack-observability-* targets
```

Per-file detail in [`make/README.md`](../make/README.md).

`go-app.mk` outline:

```make
$(APP_NAME): build  ## build the binary

build: embed
	go build $(GO_BUILD_FLAGS) -o $(APP_NAME) $(GO_PACKAGE)

build-noui:
	go build -tags noui $(GO_BUILD_FLAGS) -o $(APP_NAME) $(GO_PACKAGE)

embed:
	$(MAKE) -C $(UI_PKG_DIR) build
	rm -rf dist && cp -r $(UI_PKG_DIR)/dist dist

run: build
	./$(APP_NAME) $(RUN_FLAGS)

dev:
	$(MAKE) -j2 dev-server dev-ui

dev-server: build-noui
	./$(APP_NAME) $(RUN_FLAGS)

dev-ui:
	$(MAKE) -C $(UI_PKG_DIR) run

test:
ifdef UI_PACKAGE
	go test -tags noui ./...
else
	go test ./...
endif

test-integration:
	SOCKERLESS_TEST_TARGET=sim go test -tags 'noui integration' ./...

lint:
	go vet ./...
	gofmt -l . | tee /dev/stderr | (! read)

install:
	go mod download

clean:
	rm -f $(APP_NAME) ; rm -rf dist
	go clean -testcache

help:
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make <target>\n\nTargets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  %-18s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
```

`ui-app.mk` outline:

```make
build:
	bun run build

run dev:
	bun run dev

preview: build
	bun run preview

test:
	if package.json defines a test script, run it; otherwise report no test script configured

lint:
	bunx tsc --noEmit

install:
	bun install --cwd $(REPO_ROOT)/ui

clean:
	rm -rf dist node_modules .turbo

help:
	@awk … (same)
```

## Top-level Makefile

The top-level Makefile has four jobs:

- Maintain explicit lists of Go-with-UI apps, Go apps, UI packages, and test harness directories.
- Fan out standard targets such as `build`, `test`, `test-integration`, `lint`, `lint-ui`, `clean`, `install`, `upgrade-deps`, and `check-deps`.
- Delegate path targets: `make backends/ecs/build` runs `make -C backends/ecs build`; `make ui/packages/admin/test` runs that package's tests.
- Include `make/components.mk` and `make/stack.mk` for local stack lifecycle.

Per-app aliases were intentionally removed. Use the path-delegation form:

```sh
make cmd/sockerless-admin/build
make backends/ecs/test-integration
make simulator-aws/sdk-tests/test
make ui/packages/admin/test
```

Cross-cutting Docker-driven suites remain as top-level targets because they span multiple apps or external tools: `e2e-*`, `tf-int-test-*`, `smoke-test-*`, `faas-smoke-test-*`, and `upstream-test-*`.

## Stack orchestration

`make/stack.mk` provides six pre-canned single-cell stacks:

| Target | Starts |
|---|---|
| `make stack-aws-ecs` | AWS simulator + ECS backend + admin |
| `make stack-aws-lambda` | AWS simulator + Lambda backend + admin |
| `make stack-gcp-cloudrun` | GCP simulator + Cloud Run backend + admin |
| `make stack-gcp-gcf` | GCP simulator + Cloud Run Functions backend + admin |
| `make stack-azure-aca` | Azure simulator + ACA backend + admin |
| `make stack-azure-azf` | Azure simulator + Azure Functions backend + admin |

Each `stack-X-Y` target composes the real per-component targets from `make/components.mk`:

```sh
make rebuild-component KIND=sim CLOUD=aws
make rebuild-component KIND=backend CLOUD=aws BACKEND=ecs
make start-component KIND=sim CLOUD=aws NAME=sim PORT=4566
make start-component KIND=backend CLOUD=aws BACKEND=ecs NAME=backend PORT=3375 SIM_PORT=4566
```

The pre-canned stack names are operator shortcuts for the common one-simulator, one-backend, one-admin workflow. Arbitrary topologies use `start-component` directly or the admin topology API documented in `docs/ADMIN_ORCHESTRATION.md`.

Each runtime file is keyed by component name:

| File | Purpose |
|---|---|
| `.stack-pids/<NAME>.pid` | Supervisor process PID. |
| `.stack-pids/<NAME>.log` | Component stdout/stderr. |
| `.stack-pids/<NAME>.exit` | Exit code + UTC timestamp from the supervised child process. |
| `.stack-pids/<NAME>.env` | Optional per-instance env file, written by admin topology lifecycle. |
| `.stack-pids/backend.env` | Pre-canned stack backend env file for simulator-safe defaults. |

`start-component` starts a supervisor that ignores HUP, forwards TERM/INT to the child, and records the child exit code. `stop-component` sends SIGTERM to the supervisor and removes the pidfile. `status-components` and `stop-components` sweep every `.stack-pids/*.pid`.

The pre-canned stack targets write `.stack-pids/backend.env` only when the selected backend needs local simulator defaults:

| Backend family | Env written |
|---|---|
| ACA | `SOCKERLESS_ACA_SUBSCRIPTION_ID`, `SOCKERLESS_ACA_RESOURCE_GROUP`, `SOCKERLESS_ACA_LOG_ANALYTICS_WORKSPACE`, `SOCKERLESS_CALLBACK_URL` |
| Azure Functions | `SOCKERLESS_AZF_SUBSCRIPTION_ID`, `SOCKERLESS_AZF_RESOURCE_GROUP`, `SOCKERLESS_AZF_STORAGE_ACCOUNT`, `SOCKERLESS_CALLBACK_URL` |
| Cloud Run | `SOCKERLESS_GCR_PROJECT`, `SOCKERLESS_GCP_LOGADMIN_ENDPOINT` |
| Cloud Run Functions | `SOCKERLESS_GCF_PROJECT` |
| Lambda | `SOCKERLESS_LAMBDA_ROLE_ARN`, `SOCKERLESS_CALLBACK_URL` |

These are the same backend env vars an operator would pass by hand. The admin surface introduced.

## Operational checks

After changing Makefile plumbing, run the smallest target that proves the changed path:

```sh
make help
make stack-status
make stack-azure-aca
curl -fsS http://localhost:9090/api/v1/overview
make stack-down
```

For leaf Makefile changes, also run the relevant path target, for example `make backends/ecs/build` or `make ui/packages/admin/test`.
