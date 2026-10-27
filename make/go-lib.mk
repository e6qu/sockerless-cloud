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

.PHONY: install build build-api test lint clean

# Build for a library means "compile-check": no binary output.
install: ## install Go module deps
	$(GO_ENV) go mod download

build: ## compile-check (no binary output for libraries)
	$(GO_ENV) go build ./...

build-api: build ## compile-check alias for the top-level fan-out

test: ## run unit tests
	$(GO_ENV) go test $(GO_TEST_FLAGS) ./...

# This repository's own modules are excluded, for the reason go-app.mk records:
# a release pins them by commit, and `@latest` walks that pin backwards to a
# deleted bootstrap tag the module proxy still serves.
upgrade-deps: ## bump every direct require in go.mod to its latest (this repository's own modules excluded — a release pins those by commit)
	@gomod=$$($(GO_ENV) go env GOMOD); \
	if [ "$$gomod" = "/dev/null" ]; then \
	  echo "no go.mod found"; \
	  exit 1; \
	fi; \
	deps=$$(awk '/^require \(/{b=1;next} /^\)/&&b{b=0} b&&!/\/\/ indirect/{sub(/^[ \t]+/,"");sub(/[ \t]*\/\/.*$$/,"");if(NF>=2)print $$1}' "$$gomod"); \
	for d in $$deps; do \
	  case "$$d" in github.com/e6qu/sockerless-cloud/*) \
	    printf "▸ skip %s (published here; a release pins it by commit)\n" "$$d"; \
	    continue;; \
	  esac; \
	  printf "▸ go get -u %s@latest\n" "$$d"; \
	  $(GO_ENV) go get -u "$$d@latest"; \
	done; \
	$(GO_ENV) go mod tidy

lint: ## go vet, gofmt and golangci-lint
	$(GO_ENV) go vet ./...
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
	  printf "$(COLOR_RED)gofmt -l .$(COLOR_RESET)\n%s\n" "$$unformatted"; \
	  exit 1; \
	fi
	@command -v golangci-lint >/dev/null 2>&1 || { printf "$(COLOR_RED)golangci-lint is not on PATH: install v2.15.0, the version CI runs$(COLOR_RESET)\n"; exit 1; }
	$(GO_ENV) golangci-lint run ./...

clean: ## clean go test cache
	$(GO_ENV) go clean -testcache 2>/dev/null || true
