# Standardized Makefile for simulator-azure — see docs/MAKEFILE_STANDARD.md.
#
# This sim is its own Go module, resolved against the sibling support
# modules (realexec, ui-auth) through the repo-root go.work during
# development and through tagged versions when installed with
# `go install ...@version`. The default `build` produces a stripped,
# no-UI binary; `make embed && make build` bakes the console SPA in.

APP_NAME       := simulator-azure
GO_PACKAGE     := .
UI_PACKAGE     := simulator-azure
DEFAULT_PORT   := 4568
GO_ENV         := CGO_ENABLED=0
GO_LDFLAGS      := -s -w
RUN_ENV        := SIM_LISTEN_ADDR=:4568
RUN_FLAGS      :=

# go-test deadline for the CLI suite. Each `az` invocation carries ~0.5-2s of
# Python interpreter startup, so the wall-clock grows with the test count as the
# sim's coverage ratchets up; keep generous headroom over the current runtime.
CLI_TEST_TIMEOUT ?= 600s

REPO_ROOT_REL  := ..

include $(REPO_ROOT_REL)/make/go-app.mk

# ── Domain-specific test targets ────────────────────────────────────
# Each sub-tree (sdk-tests, cli-tests, terraform-tests, shared) is a
# separate Go module; the CI calls these directly.

.PHONY: unit-test shared-test sdk-test sdk-test-local docker-sdk-test cli-test terraform-test

# CI passes a deadline under its step budget so a hung test fails with a
# named Go stack instead of an opaque step kill; locally the default matches
# go test's own.
UNIT_TEST_TIMEOUT ?= 10m

unit-test: ## go test the simulator module's own package-internal tests (fast; no Docker/real-exec)
	$(GO_ENV) go test -tags noui -count=1 -timeout $(UNIT_TEST_TIMEOUT) ./...

shared-test: ## go test the shared/ helper module
	cd shared && $(GO_ENV) go test -v -count=1 ./...

ifeq ($(shell uname -s),Darwin)
sdk-test: docker-sdk-test ## go test the SDK-driven sim tests in the Linux real-network harness on macOS
else
sdk-test: sdk-test-local ## go test the SDK-driven sim tests
endif

# The Cosmos DB emulator differential tests are slow and variable, so the SDK
# suite carries an explicit deadline above Go's 10-minute default; the CI step
# gives it headroom over this.
SDK_TEST_TIMEOUT ?= 13m
sdk-test-local: build ## go test the SDK-driven sim tests on a Linux host with real-network capabilities
	cd sdk-tests && $(GO_ENV) go test -v -count=1 -timeout $(SDK_TEST_TIMEOUT) ./...

cli-test: build ## go test the CLI-driven sim tests
	cd cli-tests && $(GO_ENV) go test -v -count=1 -timeout $(CLI_TEST_TIMEOUT) ./...

# The azure terraform stack is Docker-only: this outer `go test` wraps a
# `docker run go test` (terraform-tests/helpers_test.go) that does the real
# apply+destroy bounded by TERRAFORM_TEST_TIMEOUT. The outer timeout just has
# to outlast the inner run + docker startup, so it's deliberately generous;
# the apply/destroy deadline the tests honour is TERRAFORM_TEST_TIMEOUT.
TERRAFORM_TEST_TIMEOUT ?= 600s

# TERRAFORM_TEST_ARGS carries extra `go test` flags — CI's tf shards use it to
# select one apply/destroy entrypoint per job (`-run TestTerraformApplyDestroy`
# for the shared stack, `-run TestTerraformSubscriptionApplyDestroy` for the
# standalone Microsoft.Subscription config).
TERRAFORM_TEST_ARGS ?=

terraform-test: build ## go test the terraform-driven sim tests (apply+destroy deadline = TERRAFORM_TEST_TIMEOUT)
	cd terraform-tests && TERRAFORM_TEST_TIMEOUT=$(TERRAFORM_TEST_TIMEOUT) $(GO_ENV) go test -v -count=1 -timeout 20m $(TERRAFORM_TEST_ARGS) ./...

# Aggregate that runs all four legacy test categories.
.PHONY: test-all
test-all: unit-test shared-test sdk-test cli-test terraform-test ## run every test category

# ── Docker targets ──────────────────────────────────────────────────
.PHONY: docker-build docker-run docker-test docker-test-build

IMAGE := sockerless-simulator-azure
DOCKER_TEST_IMAGE := sockerless-test-azure
DOCKER_TEST_USER := $(shell id -u):$(shell id -g)
DOCKER_SOCK_GID := $(shell stat -f '%g' /var/run/docker.sock 2>/dev/null || stat -c '%g' /var/run/docker.sock 2>/dev/null || echo 0)
DOCKER_TEST_GROUPS := $(DOCKER_SOCK_GID),0
DOCKER_TEST_ENV := -e HOME=/tmp/sockerless-home -e USER=sockerless -e SOCKERLESS_DOCKER_TEST_UIDGID=$(DOCKER_TEST_USER) -e SOCKERLESS_DOCKER_TEST_GROUPS=$(DOCKER_TEST_GROUPS) -e -e CGO_ENABLED=0 -e TF_IN_AUTOMATION=1 -e CLOUDSDK_CORE_DISABLE_PROMPTS=1 -e AZURE_CORE_NO_COLOR=true -e AWS_PAGER=
DOCKER_REALNET_TEST_ENV := -e HOME=/tmp/sockerless-home -e USER=sockerless -e -e CGO_ENABLED=0 -e TF_IN_AUTOMATION=1 -e CLOUDSDK_CORE_DISABLE_PROMPTS=1 -e AZURE_CORE_NO_COLOR=true -e AWS_PAGER= -e NO_PROXY=localhost,127.0.0.1,::1,.localhost,*.localhost -e no_proxy=localhost,127.0.0.1,::1,.localhost,*.localhost
DOCKER_REALNET_FLAGS := --privileged
# Spec validation is armed by two coordinates the harness has to carry into the
# container, exactly as the Amazon Web Services harness does. Without them a
# macOS run of `SOCKERLESS_SPEC_VALIDATE=... make sdk-test` validates nothing
# and writes no report — a pass that checked no wire shape at all.
DOCKER_SPEC_VALIDATION_FLAGS := $(if $(strip $(SOCKERLESS_SPEC_VALIDATE)),-v "$(dir $(SOCKERLESS_SPEC_VALIDATE)):/sockerless-spec" -e SOCKERLESS_SPEC_VALIDATE="/sockerless-spec/$(notdir $(SOCKERLESS_SPEC_VALIDATE))" -e SOCKERLESS_SPEC_DIR=/src/specs/cloud-api/azure)

docker-build: ## build the docker image
	docker build --load -t $(IMAGE) -f Dockerfile ..

docker-run: docker-build ## run the docker image
	docker run --rm -p $(DEFAULT_PORT):$(DEFAULT_PORT) \
		-e SIM_LISTEN_ADDR=:$(DEFAULT_PORT) \
		-e SIM_LOG_LEVEL=info \
		$(IMAGE)

docker-test-build: ## build the test docker image
	docker build --load -t $(DOCKER_TEST_IMAGE) -f $(REPO_ROOT)/Dockerfile.test $(REPO_ROOT)

docker-test: docker-test-build ## run all tests inside docker
	docker run --rm --network host \
		$(DOCKER_REALNET_FLAGS) \
		--security-opt label=disable \
		--group-add $(DOCKER_SOCK_GID) \
		--group-add 0 \
		-v $(REPO_ROOT):/src \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-w /src/simulator-azure \
		$(DOCKER_REALNET_TEST_ENV) \
		$(DOCKER_SPEC_VALIDATION_FLAGS) \
		$(DOCKER_TEST_IMAGE) make test-all

docker-sdk-test: docker-test-build ## run SDK tests inside the Linux real-network docker harness
	docker run --rm --network host \
		$(DOCKER_REALNET_FLAGS) \
		--security-opt label=disable \
		--group-add $(DOCKER_SOCK_GID) \
		--group-add 0 \
		-v $(REPO_ROOT):/src \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-w /src/simulator-azure \
		$(DOCKER_REALNET_TEST_ENV) \
		$(DOCKER_SPEC_VALIDATION_FLAGS) \
		$(DOCKER_TEST_IMAGE) make sdk-test-local
