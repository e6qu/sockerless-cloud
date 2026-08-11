# Sockerless Cloud — top-level Makefile.
#
# Thin orchestrator. Per-app recipes live in each app's own Makefile;
# this file just delegates and aggregates. See docs/MAKEFILE_STANDARD.md
# for the standard target surface every app implements.
#
# Common workflows:
#
#   make help              # list targets
#   make build             # build every app
#   make test              # unit-test every app
#   make lint              # lint every app
#   make clean             # clean every app
#
#   make simulator-aws/build          # build a single app via path
#   make simulator-aws/sdk-test       # run one sim's SDK suite
#   make docker-test                  # SDK+CLI+Terraform suites in Docker
#
# Every simulator is installable straight from this repository:
#
#   go install github.com/e6qu/sockerless-cloud/simulator-aws@latest

include make/help.mk
include make/colors.mk

define docker_build_local
	@if docker buildx version >/dev/null 2>&1; then \
		docker buildx build --load $(1); \
	else \
		docker build $(1); \
	fi
endef

# ── Apps — explicit lists (not glob) ────────────────────────────────
#
# When a new app lands, add it to one of these lists. The fan-out and
# delegation rules below pick it up automatically.

# Go binaries with embedded console UI (3).
GO_UI_APPS := \
  simulator-aws \
  simulator-gcp \
  simulator-azure

# Go libraries without UI.
GO_APPS := \
  realexec \
  testutil \
  ui-auth

# UI packages. Each consumed by the corresponding GO_UI_APPS entry
# (except `core`, which is a shared library).
UI_APPS := \
  ui/packages/simulator-aws \
  ui/packages/simulator-gcp \
  ui/packages/simulator-azure \
  ui/packages/core

# Test-category Makefiles (SDK/CLI/Terraform suites per cloud).
TEST_DIRS := \
  simulator-aws/sdk-tests simulator-aws/cli-tests simulator-aws/terraform-tests \
  simulator-gcp/sdk-tests simulator-gcp/cli-tests simulator-gcp/terraform-tests \
  simulator-azure/sdk-tests simulator-azure/cli-tests simulator-azure/terraform-tests

ALL_APPS := $(GO_UI_APPS) $(GO_APPS) $(UI_APPS)

# ── Standard fan-out targets ────────────────────────────────────────

.PHONY: install build build-noui test test-integration lint lint-ui lint-all clean upgrade-deps check-deps check-workflow-timeouts hooks

install: ## install deps in every app
	@$(MAKE) -s _fanout TARGET=install APPS="$(ALL_APPS)"

hooks: ## install pre-commit / commit-msg / pre-push git hooks (one-time bootstrap on fresh clones)
	@command -v pre-commit >/dev/null 2>&1 || { echo "pre-commit not on PATH — install via 'uv pip install pre-commit' or 'pipx install pre-commit'"; exit 1; }
	@pre-commit install --install-hooks
	@pre-commit install --hook-type commit-msg --hook-type pre-push

build: ## build every app
	@$(MAKE) -s _fanout TARGET=build APPS="$(UI_APPS) $(GO_UI_APPS) $(GO_APPS)"

build-noui: ## build every Go app with -tags noui (skips UI embed)
	@$(MAKE) -s _fanout TARGET=build-noui APPS="$(GO_UI_APPS) $(GO_APPS)"

test: ## unit-test every app
	@$(MAKE) -s _fanout TARGET=test APPS="$(ALL_APPS)"

test-integration: ## run integration tests across every Go app
	@$(MAKE) -s _fanout TARGET=test-integration APPS="$(GO_UI_APPS) $(GO_APPS) $(TEST_DIRS)"

lint: ## lint every Go app (CI lint runner has no bun — use lint-ui separately)
	@$(MAKE) -s _fanout TARGET=lint APPS="$(GO_UI_APPS) $(GO_APPS)"

lint-ui: ## lint every UI package (requires bun)
	@$(MAKE) -s _fanout TARGET=lint APPS="$(UI_APPS)"

lint-all: lint lint-ui ## lint every app (Go + UI)

clean: ## clean every app's artefacts
	@$(MAKE) -s _fanout TARGET=clean APPS="$(ALL_APPS)"

upgrade-deps: ## bump every Go module's direct deps to latest (per-module independence preserved; TEST_DIRS included so scripts/check-latest-deps.sh stays clean)
	@$(MAKE) -s _fanout TARGET=upgrade-deps APPS="$(GO_UI_APPS) $(GO_APPS) $(TEST_DIRS)"

check-deps: ## fail if any Go module / Terraform provider is behind its latest published version
	@bash scripts/check-latest-deps.sh

check-workflow-timeouts: ## verify every GitHub Actions job has a timeout of at most 15 minutes
	@bash scripts/test-workflow-timeouts.sh
	@bash scripts/check-workflow-timeouts.sh

# Internal helper: iterate APPS and run TARGET in each. Stops on
# first failure. Honours --keep-going via $(MAKEFLAGS).
.PHONY: _fanout
_fanout:
	@for app in $(APPS); do \
	  if [ -f "$$app/Makefile" ]; then \
	    printf "$(COLOR_CYAN)▸ %s: %s$(COLOR_RESET)\n" "$$app" "$(TARGET)"; \
	    $(MAKE) -s -C "$$app" $(TARGET) || exit $$?; \
	  else \
	    printf "$(COLOR_DIM)skip %s (no Makefile)$(COLOR_RESET)\n" "$$app"; \
	  fi; \
	done

# ── Per-app delegation via path ─────────────────────────────────────
#
# `make simulator-aws/build` → `$(MAKE) -C simulator-aws build`.
# Works for any standardized target. `$*` is the path; `$@` carries
# the full target with the suffix appended.

# FORCE keeps the recipe from being short-circuited when a directory
# happens to share the target name, so without FORCE a path target
# could silently report "up to date" instead of delegating to its Makefile.
.PHONY: FORCE
FORCE:

%/install %/build %/build-noui %/embed %/run %/dev %/test %/test-integration %/lint %/clean %/preview %/help %/docker-test %/docker-test-build %/unit-test %/shared-test %/sdk-test %/cli-test %/terraform-test %/terraform-https-test: FORCE
	@$(MAKE) -s -C $* $(notdir $@)

# ── Simulator Docker test harness ───────────────────────────────────

SIMULATOR_APPS := simulator-aws simulator-gcp simulator-azure

.PHONY: docker-test docker-test-build firecracker-test realexec-network-test
docker-test-build: ## build Docker test images for all cloud simulators
	@$(MAKE) -s _fanout TARGET=docker-test-build APPS="$(SIMULATOR_APPS)"

docker-test: ## run SDK, CLI, and Terraform tests for all cloud simulators inside Docker
	@$(MAKE) -s _fanout TARGET=docker-test APPS="$(SIMULATOR_APPS)"

firecracker-test: ## boot a real Firecracker microVM and run Go arithmetic build/execution inside it
	@bash tests/firecracker/run-arithmetic.sh

realexec-network-test: ## create a real Linux bridge/netns/veth NIC path and verify cleanup
	@bash tests/realexec/run-network-nic.sh
