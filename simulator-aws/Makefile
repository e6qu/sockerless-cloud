# Standardized Makefile for simulator-aws — see docs/MAKEFILE_STANDARD.md.
#
# This sim is its own Go module, resolved against the sibling support
# modules (sim, realexec, ui-auth) through the repo-root go.work during
# development and through tagged versions when installed with
# `go install ...@version`. The default `build` produces a stripped,
# no-UI binary; `make embed && make build` bakes the console SPA in.

APP_NAME       := simulator-aws
GO_PACKAGE     := .
UI_PACKAGE     := simulator-aws
DEFAULT_PORT   := 4566
GO_ENV         := CGO_ENABLED=0
GO_LDFLAGS      := -s -w
RUN_ENV        := SIM_LISTEN_ADDR=:4566
RUN_FLAGS      :=
SDK_TEST_TIMEOUT ?= 600s
SDK_TEST_ARGS    ?= ./...
CLI_TEST_TIMEOUT ?= 600s
CLI_TEST_ARGS    ?= ./...
TERRAFORM_TEST_TIMEOUT ?= 300s
TERRAFORM_TEST_ARGS    ?=
# Every stack under terraform-tests/, so a package added there runs without
# being listed anywhere. CI shards the same set one package per job; the shard
# list is gated against this directory by scripts/check-tf-shard-coverage.sh.
TERRAFORM_TEST_PACKAGES ?= ./...
# Each package launches its own simulator, Caddy gateway, Terraform provider,
# and real database/container workloads. Keep packages isolated while Terraform
# retains its own resource-level parallelism inside each production-shaped graph.
TERRAFORM_TEST_PACKAGE_PARALLELISM ?= 1
TERRAFORM_TEST_JSON ?= 1
TERRAFORM_TEST_JSON_FLAG := $(if $(filter 1 true yes,$(TERRAFORM_TEST_JSON)),-json,)

REPO_ROOT_REL  := ..

include $(REPO_ROOT_REL)/make/go-app.mk

# ── Domain-specific test targets ────────────────────────────────────
# Each sub-tree (sdk-tests, cli-tests, terraform-tests) is a
# separate Go module; the CI calls these directly.

.PHONY: unit-test race-test sdk-test cli-test terraform-test terraform-https-test terraform-https-test-docker

# CI passes a deadline under its step budget so a hung test fails with a
# named Go stack instead of an opaque step kill; locally the default matches
# go test's own.
UNIT_TEST_TIMEOUT ?= 10m

unit-test: ## go test the simulator module's own package-internal tests (fast; AST/unit guards, no Docker/real-exec)
	$(GO_ENV) go test -tags noui -count=1 -timeout $(UNIT_TEST_TIMEOUT) ./...

# The same tests under the race detector. Separate from unit-test because the
# detector needs cgo, which unit-test deliberately does not have.
#
# The first race-detector run of this package reported 144 races. Every one was
# asynchronous simulator work — reconciliations, builds, deployments,
# certificate validation — running in goroutines and timers nothing tracked, so
# work still in flight when a test ended read package-level stores the next test
# was replacing. background_work.go counts all of it now, and this target is
# what keeps the count at zero: a suite whose races are only measured by hand is
# a suite whose races come back.
RACE_TEST_TIMEOUT ?= 20m
RACE_TEST_PKGS ?= ./...

race-test: ## go test the simulator module's own tests under the race detector
	CGO_ENABLED=1 go test -tags noui -race -count=1 -timeout $(RACE_TEST_TIMEOUT) $(RACE_TEST_PKGS)

sdk-test: build ## go test the SDK-driven sim tests
	cd sdk-tests && $(GO_ENV) go test -v -count=1 -timeout $(SDK_TEST_TIMEOUT) $(SDK_TEST_ARGS)

cli-test: build ## go test the CLI-driven sim tests
	cd cli-tests && $(GO_ENV) go test -v -count=1 -timeout $(CLI_TEST_TIMEOUT) $(CLI_TEST_ARGS)

terraform-test: build ## go test the terraform-driven sim tests
	cd terraform-tests && SOCKERLESS_AWS_SIMULATOR_BINARY=$(CURDIR)/$(APP_NAME) $(GO_ENV) go test -p $(TERRAFORM_TEST_PACKAGE_PARALLELISM) $(TERRAFORM_TEST_JSON_FLAG) -v -count=1 -timeout $(TERRAFORM_TEST_TIMEOUT) $(TERRAFORM_TEST_ARGS) $(TERRAFORM_TEST_PACKAGES)

terraform-https-test: ## go test terraform via the optional Caddy HTTPS gateway
	@if [ "$$(uname -s)" = "Darwin" ]; then \
	  $(MAKE) terraform-https-test-docker; \
	else \
	  $(MAKE) build; \
	  cd terraform-tests && SOCKERLESS_AWS_SIMULATOR_BINARY=$(CURDIR)/$(APP_NAME) SOCKERLESS_TF_HTTPS_GATEWAY=1 $(GO_ENV) go test -p $(TERRAFORM_TEST_PACKAGE_PARALLELISM) $(TERRAFORM_TEST_JSON_FLAG) -v -count=1 -timeout $(TERRAFORM_TEST_TIMEOUT) $(TERRAFORM_TEST_ARGS) $(TERRAFORM_TEST_PACKAGES); \
	fi

# Aggregate that runs every test category.
.PHONY: test-all
test-all: unit-test sdk-test cli-test terraform-test ## run every test category

# ── Docker targets ──────────────────────────────────────────────────
.PHONY: docker-build docker-run docker-test docker-test-build

IMAGE := sockerless-simulator-aws
DOCKER_TEST_IMAGE := sockerless-test-aws
DOCKER_TEST_USER := $(shell id -u):$(shell id -g)
DOCKER_SOCK_GID := $(shell stat -f '%g' /var/run/docker.sock 2>/dev/null || stat -c '%g' /var/run/docker.sock 2>/dev/null || echo 0)
DOCKER_TEST_GROUPS := $(DOCKER_SOCK_GID),0
DOCKER_TEST_ENV := -e HOME=/tmp/sockerless-home -e USER=sockerless -e SOCKERLESS_DOCKER_TEST_UIDGID=$(DOCKER_TEST_USER) -e SOCKERLESS_DOCKER_TEST_GROUPS=$(DOCKER_TEST_GROUPS) -e CGO_ENABLED=0 -e TF_IN_AUTOMATION=1 -e CLOUDSDK_CORE_DISABLE_PROMPTS=1 -e AZURE_CORE_NO_COLOR=true -e AWS_PAGER=
# One directory that is the same path on both sides of the harness.
#
# The simulator hands the container engine host paths — an Amazon EFS access
# point a task mounts, an AWS CodeBuild source tree, a Firecracker root image —
# and the engine resolves a mount source on the host it runs on, not inside the
# client container. Without a directory mounted into the container at its own
# host path and made the simulator's temporary directory, every workload
# bind-mounts an empty directory the engine created for itself and reads none of
# the files it was given. Two Azure tests failed exactly that way until that
# harness gained one.
#
# The name is terse and it sits under /var/tmp rather than /tmp on purpose: a
# Firecracker API socket lands at
# <tmp>/sockerless-firecracker/<=40 chars>/firecracker.socket and a UNIX socket
# path cannot exceed 108 bytes, which leaves the directory 22; and /tmp on a
# container-engine host is commonly memory-backed, which a booting machine's
# root image would fill.
DOCKER_TEST_SHARED_TMP := /var/tmp/skl
DOCKER_TEST_SHARED_TMP_MOUNT := -v $(DOCKER_TEST_SHARED_TMP):$(DOCKER_TEST_SHARED_TMP)

DOCKER_REALNET_TEST_ENV := -e HOME=/tmp/sockerless-home -e USER=sockerless -e CGO_ENABLED=0 -e TF_IN_AUTOMATION=1 -e CLOUDSDK_CORE_DISABLE_PROMPTS=1 -e AZURE_CORE_NO_COLOR=true -e AWS_PAGER= -e TMPDIR=$(DOCKER_TEST_SHARED_TMP)
DOCKER_REALNET_FLAGS := --privileged --pid host
DOCKER_SPEC_VALIDATION_FLAGS := $(if $(strip $(SOCKERLESS_SPEC_VALIDATE)),-v "$(dir $(SOCKERLESS_SPEC_VALIDATE)):/sockerless-spec" -e SOCKERLESS_SPEC_VALIDATE="/sockerless-spec/$(notdir $(SOCKERLESS_SPEC_VALIDATE))" -e SOCKERLESS_SPEC_DIR=/src/specs/cloud-api/aws)

docker-build: ## build the docker image
	@if docker buildx version >/dev/null 2>&1; then \
		docker buildx build --load -t $(IMAGE) -f Dockerfile ..; \
	else \
		docker build -t $(IMAGE) -f Dockerfile ..; \
	fi

docker-run: docker-build ## run the docker image
	docker run --rm -p $(DEFAULT_PORT):$(DEFAULT_PORT) \
		-e SIM_LISTEN_ADDR=:$(DEFAULT_PORT) \
		-e SIM_LOG_LEVEL=info \
		$(IMAGE)

docker-test-build: ## build the test docker image
	@if docker buildx version >/dev/null 2>&1; then \
		docker buildx build --load -t $(DOCKER_TEST_IMAGE) -f $(REPO_ROOT)/Dockerfile.test $(REPO_ROOT); \
	else \
		docker build -t $(DOCKER_TEST_IMAGE) -f $(REPO_ROOT)/Dockerfile.test $(REPO_ROOT); \
	fi

docker-test: docker-test-build ## run all tests inside docker
	docker run --rm --network host \
		$(DOCKER_REALNET_FLAGS) \
		--security-opt label=disable \
		--group-add $(DOCKER_SOCK_GID) \
		--group-add 0 \
		-v $(REPO_ROOT):/src \
		-v /var/run/docker.sock:/var/run/docker.sock \
		$(DOCKER_TEST_SHARED_TMP_MOUNT) \
		-w /src/simulator-aws \
		$(DOCKER_REALNET_TEST_ENV) \
		$(DOCKER_TEST_IMAGE) make test-all

terraform-https-test-docker: docker-test-build ## run terraform HTTPS tests inside docker
	@container_id="$$(docker create --network host \
		$(DOCKER_REALNET_FLAGS) \
		--security-opt label=disable \
		--group-add $(DOCKER_SOCK_GID) \
		--group-add 0 \
		-v $(REPO_ROOT):/src \
		-v /var/run/docker.sock:/var/run/docker.sock \
		$(DOCKER_TEST_SHARED_TMP_MOUNT) \
		-w /src/simulator-aws \
		$(DOCKER_REALNET_TEST_ENV) \
		$(DOCKER_SPEC_VALIDATION_FLAGS) \
		-e SOCKERLESS_TF_HTTPS_GATEWAY=1 \
		$(DOCKER_TEST_IMAGE) make terraform-https-test TERRAFORM_TEST_TIMEOUT=$(TERRAFORM_TEST_TIMEOUT) TERRAFORM_TEST_PACKAGES="$(TERRAFORM_TEST_PACKAGES)" TERRAFORM_TEST_PACKAGE_PARALLELISM=$(TERRAFORM_TEST_PACKAGE_PARALLELISM) TERRAFORM_TEST_ARGS="$(TERRAFORM_TEST_ARGS)" TERRAFORM_TEST_JSON=$(TERRAFORM_TEST_JSON))" || exit $$?; \
	status=0; \
	docker start --attach "$$container_id" || status=$$?; \
	cleanup_status=0; \
	docker rm -f -v "$$container_id" >/dev/null || cleanup_status=$$?; \
	if [ "$$status" -ne 0 ]; then exit "$$status"; fi; \
	exit "$$cleanup_status"
