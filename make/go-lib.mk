# go-lib.mk — standardized recipes for Go libraries (no main binary).
#
# Required: nothing.
# Optional:
#   GO_ENV : env prepended to go invocations (e.g. GOWORK=off)
#   GO_TEST_FLAGS : flags passed to `go test` (e.g. a suite-specific timeout)
#   REPO_ROOT_REL : path from this Makefile's dir to the repo root
#                   (default ../.., same as go-app.mk).

REPO_ROOT_REL ?= ../..
REPO_ROOT     := $(abspath $(CURDIR)/$(REPO_ROOT_REL))

include $(REPO_ROOT)/make/help.mk
include $(REPO_ROOT)/make/colors.mk

.PHONY: install build build-noui test test-integration lint clean

# Build for a library means "compile-check": no binary output.
install: ## install Go module deps
	$(GO_ENV) go mod download

build: ## compile-check (no binary output for libraries)
	$(GO_ENV) go build ./...

build-noui: build ## compile-check alias for top-level no-UI fanout

test: ## run unit tests
	$(GO_ENV) go test $(GO_TEST_FLAGS) ./...

test-integration: ## run integration tests against the local simulator
	$(GO_ENV) SOCKERLESS_TEST_TARGET=sim go test -v -timeout 15m ./...

test-integration-cloud: ## run integration tests against the operator-supplied real cloud (requires SOCKERLESS_ENDPOINT_URL + per-backend env vars)
	$(GO_ENV) SOCKERLESS_TEST_TARGET=cloud go test -v -timeout 30m ./...

upgrade-deps: ## bump every direct require in go.mod to its latest (excluding github.com/sockerless/* in-repo modules)
	@gomod=$$($(GO_ENV) go env GOMOD); \
	if [ "$$gomod" = "/dev/null" ]; then \
	  echo "no go.mod found"; \
	  exit 1; \
	fi; \
	deps=$$(awk '/^require \(/{b=1;next} /^\)/&&b{b=0} b&&!/\/\/ indirect/&&!/github.com\/sockerless\//{sub(/^[ \t]+/,"");sub(/[ \t]*\/\/.*$$/,"");if(NF>=2)print $$1}' "$$gomod"); \
	for d in $$deps; do \
	  printf "▸ go get -u %s@latest\n" "$$d"; \
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
	  $(GO_ENV) golangci-lint run ./...; \
	fi

clean: ## clean go test cache
	$(GO_ENV) go clean -testcache 2>/dev/null || true
