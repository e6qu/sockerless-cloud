# go-app.mk — standardized recipes for Go-binary apps.
#
# Required variables (set by the leaf Makefile *before* including):
#   APP_NAME     : output binary name (e.g. simulator-aws)
#   GO_PACKAGE   : Go package path to build (e.g. .)
#
# Optional — for apps that embed a UI bundle:
#   UI_PACKAGE   : ui/packages/<name> directory basename (e.g. simulator-aws)
#                  When set, `build` embeds the console built at
#                  ui/packages/<UI_PACKAGE>/dist (build the UI first) with
#                  `-tags console`; `build-api` builds without it.
#
# Optional — for `run`:
#   DEFAULT_PORT : if set, gets exposed in `make run` echo banner
#   RUN_FLAGS    : flags appended to `./<binary> <RUN_FLAGS>` on `make run`
#   RUN_ENV      : env vars exported before `make run` (e.g.
#                  RUN_ENV := SIM_LISTEN_ADDR=:4566)
#
# Optional — for non-workspace builds:
#   GO_BUILD_FLAGS   : extra `go build` flags
#   GO_ENV           : env prepended to every go invocation (e.g. GOWORK=off)
#   FAAS_SMOKE_TESTS : regexp for the sim-backed FaaS smoke tests
#
# REPO_ROOT_REL is the path from the leaf Makefile to the repository root:
# ../.. unless the leaf sets it, and ".." in a simulator-<cloud>/ leaf.

REPO_ROOT_REL ?= ../..
REPO_ROOT     := $(abspath $(CURDIR)/$(REPO_ROOT_REL))
UI_DIST_SRC   := $(REPO_ROOT)/ui/packages/$(UI_PACKAGE)/dist
LOCAL_DIST    := $(CURDIR)/dist

include $(REPO_ROOT)/make/help.mk
include $(REPO_ROOT)/make/colors.mk

.PHONY: install build build-api embed run dev test test-integration test-faas-smoke lint clean

install: ## install Go module deps
	$(GO_ENV) go mod download

# GO_LDFLAGS is set by leaves that want stripped binaries
# (e.g. simulators set GO_LDFLAGS := -s -w). Passed to go build as
# `-ldflags="$(GO_LDFLAGS)"` so the recipe quotes it correctly.
GO_LDFLAGS_ARG := $(if $(GO_LDFLAGS),-ldflags="$(GO_LDFLAGS)",)

build: ## build the binary, embedding the console when the app has one
ifndef UI_PACKAGE
	$(call STEP,$(APP_NAME): building)
	$(GO_ENV) go build $(GO_BUILD_FLAGS) $(GO_LDFLAGS_ARG) -o $(APP_NAME) $(GO_PACKAGE)
else
	@$(MAKE) -s embed
	$(call STEP,$(APP_NAME): building with the console)
	$(GO_ENV) go build -tags console $(GO_BUILD_FLAGS) $(GO_LDFLAGS_ARG) -o $(APP_NAME) $(GO_PACKAGE)
endif

build-api: ## build the binary without an embedded console
	$(call STEP,$(APP_NAME): building without the console)
	$(GO_ENV) go build $(GO_BUILD_FLAGS) $(GO_LDFLAGS_ARG) -o $(APP_NAME) $(GO_PACKAGE)

# `embed` is exposed as a stand-alone target so a top-level orchestrator
# can sequence ui-build → embed → go-build deterministically. It does
# NOT invoke another Makefile — it just copies a known dist/ path.
embed: ## copy UI dist from ui/packages/<UI_PACKAGE>/dist into ./dist
ifndef UI_PACKAGE
	@printf "$(COLOR_DIM)$(APP_NAME): no UI configured, skipping embed.$(COLOR_RESET)\n"
else
	@if [ -d "$(UI_DIST_SRC)" ]; then \
	  rm -rf $(LOCAL_DIST) && cp -r $(UI_DIST_SRC) $(LOCAL_DIST) ; \
	  printf "$(COLOR_GREEN)$(APP_NAME): embedded $(UI_DIST_SRC) → $(LOCAL_DIST)$(COLOR_RESET)\n" ; \
	else \
	  printf "$(COLOR_RED)$(APP_NAME): no dist at $(UI_DIST_SRC) — build the UI first$(COLOR_RESET)\n" ; \
	  exit 1 ; \
	fi
endif

run: build ## run the binary in foreground with default flags
	$(call STEP,$(APP_NAME): running on $(or $(DEFAULT_PORT),default port))
	@$(RUN_ENV) ./$(APP_NAME) $(RUN_FLAGS)

# `dev` runs the Go server (no-UI build) AND the UI dev server in
# parallel. Implemented in the simplest portable way: parallel make
# invocation. Press Ctrl-C to stop both.
dev: ## run Go server (no UI) + UI dev server in parallel
ifndef UI_PACKAGE
	@$(MAKE) run
else
	@printf "$(COLOR_CYAN)▸ %s: dev mode — Go @ :$(or $(DEFAULT_PORT),9999) + Vite @ :5173$(COLOR_RESET)\n" "$(APP_NAME)"
	@printf "$(COLOR_DIM)To start Vite, in another terminal:  cd $(REPO_ROOT)/ui/packages/$(UI_PACKAGE) && make run$(COLOR_RESET)\n"
	@$(MAKE) -s build-api
	@$(RUN_ENV) ./$(APP_NAME) $(RUN_FLAGS)
endif

test: ## run unit tests
	$(GO_ENV) go test ./...

test-integration: ## run integration tests against the local simulator
	$(GO_ENV) SOCKERLESS_TEST_TARGET=sim go test -tags integration -v -timeout 15m ./...

test-faas-smoke: ## run this backend's sim-backed FaaS runner smoke test
ifndef FAAS_SMOKE_TESTS
	@printf "$(COLOR_DIM)$(APP_NAME): no FaaS smoke test configured.$(COLOR_RESET)\n"
else
	$(GO_ENV) SOCKERLESS_TEST_TARGET=sim go test -tags integration -v -count=1 -run '$(FAAS_SMOKE_TESTS)' -timeout 15m ./...
endif

test-integration-cloud: ## run integration tests against the operator-supplied real cloud (requires SOCKERLESS_ENDPOINT_URL + per-backend ARM env vars)
	$(GO_ENV) SOCKERLESS_TEST_TARGET=cloud go test -tags integration -v -timeout 30m ./...

# This repository's own modules are excluded. A release pins them by commit, so
# they are required at a pseudo-version, and `@latest` prefers any semver tag
# over one — including the deleted bootstrap `v0.1.0` tags, which still resolve
# from the module proxy cache. Upgrading them here therefore walks the pin
# backwards to a revision predating the fields the simulators call, and the
# build stops compiling.
upgrade-deps: ## bump every direct require in go.mod to its latest (this repository's own modules excluded — a release pins those by commit)
	@deps=$$(awk '/^require \(/{b=1;next} /^\)/&&b{b=0} b&&!/\/\/ indirect/{sub(/^[ \t]+/,"");sub(/[ \t]*\/\/.*$$/,"");if(NF>=2)print $$1}' go.mod); \
	for d in $$deps; do \
	  case "$$d" in github.com/e6qu/sockerless-cloud/*) \
	    printf "$(COLOR_CYAN)▸ skip %s (published here; a release pins it by commit)$(COLOR_RESET)\n" "$$d"; \
	    continue;; \
	  esac; \
	  printf "$(COLOR_CYAN)▸ go get -u %s@latest$(COLOR_RESET)\n" "$$d"; \
	  $(GO_ENV) go get -u "$$d@latest"; \
	done; \
	$(GO_ENV) go mod tidy

lint: ## go vet + gofmt check (golangci-lint when available)
	$(GO_ENV) go vet ./...
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
	  printf "$(COLOR_RED)gofmt -l .$(COLOR_RESET)\n%s\n" "$$unformatted"; \
	  exit 1; \
	fi
	@if command -v golangci-lint >/dev/null 2>&1; then \
	  printf "$(COLOR_DIM)$(APP_NAME): golangci-lint$(COLOR_RESET)\n"; \
	  $(GO_ENV) golangci-lint run ./...; \
	fi

clean: ## remove built binary + dist
	rm -f $(APP_NAME)
	rm -rf $(LOCAL_DIST)
	$(GO_ENV) go clean -testcache 2>/dev/null || true
