# Standardized Makefile for simulator-azure — see docs/MAKEFILE_STANDARD.md.
#
# This sim is its own Go module, resolved against the sibling support
# modules (sim, ui-auth) through the repo-root go.work during
# development and through tagged versions when installed with
# `go install ...@version`. `build` embeds the console built in ui/ (build the UI first);
# `build-api`, which the test suites use, builds without it.

APP_NAME       := simulator-azure
GO_PACKAGE     := .
UI_PACKAGE     := simulator-azure
DEFAULT_PORT   := 4568
GO_ENV         := CGO_ENABLED=0
GO_LDFLAGS      := -s -w
RUN_ENV        := SIM_LISTEN_ADDR=:4568
RUN_FLAGS      :=

# go-test deadline for the CLI suite. Each `az` invocation carries ~0.5-2s of
# Python interpreter startup, so the wall-clock grows with the test count.
# 13m matches the SDK suite and is the most this can be: the CI step allows
# 14 minutes and the job 15, so a larger inner deadline would surface as an
# opaque step kill instead of a named Go stack; CI shards the suite to stay
# under it.
CLI_TEST_TIMEOUT ?= 13m

REPO_ROOT_REL  := ..

include $(REPO_ROOT_REL)/make/go-app.mk

# Each sub-tree (sdk-tests, cli-tests, terraform-tests) is a
# separate Go module; the CI calls these directly.

.PHONY: unit-test race-test sdk-test sdk-test-local docker-sdk-test cli-test terraform-test

# CI passes a deadline under its step budget so a hung test fails with a
# named Go stack instead of an opaque step kill; locally the default matches
# go test's own.
UNIT_TEST_TIMEOUT ?= 10m

unit-test: ## go test the simulator module's own package-internal tests (fast; no Docker/real-exec)
	$(GO_ENV) go test -count=1 -timeout $(UNIT_TEST_TIMEOUT) ./...

# The same tests under the race detector. Separate from unit-test because the
# detector needs cgo, which unit-test deliberately does not have; the AWS
# module's Makefile records why this target exists.
RACE_TEST_TIMEOUT ?= 20m
RACE_TEST_PKGS ?= ./...

race-test: ## go test the simulator module's own tests under the race detector
	CGO_ENABLED=1 go test -race -count=1 -timeout $(RACE_TEST_TIMEOUT) $(RACE_TEST_PKGS)

ifeq ($(shell uname -s),Darwin)
sdk-test: docker-sdk-test ## go test the SDK-driven sim tests in the Linux real-network harness on macOS
else
sdk-test: sdk-test-local ## go test the SDK-driven sim tests
endif

# The Cosmos DB emulator differential tests are slow and variable, so the SDK
# suite carries an explicit deadline above Go's 10-minute default; the CI step
# gives it headroom over this.
SDK_TEST_TIMEOUT ?= 13m

# What each suite runs. A sharded CI job narrows its shard with
# `-run`; the default runs everything. Without these the arguments a
# shard passes are accepted by make and dropped on the floor, so every
# shard runs the whole suite — twice the work the split was meant to
# avoid, and no warning that the partition is doing nothing.
SDK_TEST_ARGS ?= ./...
CLI_TEST_ARGS ?= ./...

sdk-test-local: build-api ## go test the SDK-driven sim tests on a Linux host with real-network capabilities
	cd sdk-tests && $(GO_ENV) bash ../../scripts/go-test-summary.sh -count=1 -timeout $(SDK_TEST_TIMEOUT) $(SDK_TEST_ARGS)

cli-test: build-api ## go test the CLI-driven sim tests
	cd cli-tests && $(GO_ENV) bash ../../scripts/go-test-summary.sh -count=1 -timeout $(CLI_TEST_TIMEOUT) $(CLI_TEST_ARGS)

# TERRAFORM_TEST_TIMEOUT bounds each apply and destroy; the outer go test
# timeout only has to outlast it.
TERRAFORM_TEST_TIMEOUT ?= 600s

# TERRAFORM_TEST_ARGS carries extra `go test` flags — CI's tf shards use it to
# select one apply/destroy entrypoint per job (`-run TestTerraformApplyDestroy`
# for the shared stack, `-run TestTerraformSubscriptionApplyDestroy` for the
# standalone Microsoft.Subscription config).
TERRAFORM_TEST_ARGS ?=

terraform-test: build-api ## go test the terraform-driven sim tests (apply+destroy deadline = TERRAFORM_TEST_TIMEOUT)
	cd terraform-tests && TERRAFORM_TEST_TIMEOUT=$(TERRAFORM_TEST_TIMEOUT) $(GO_ENV) go test -v -count=1 -timeout 20m $(TERRAFORM_TEST_ARGS) ./...

# Aggregate that runs every test category.
.PHONY: test-all
test-all: unit-test sdk-test cli-test terraform-test ## run every test category

.PHONY: docker-build docker-run docker-test docker-test-build

IMAGE := sockerless-simulator-azure
DOCKER_TEST_IMAGE := sockerless-test-azure
DOCKER_TEST_USER := $(shell id -u):$(shell id -g)
DOCKER_SOCK_GID := $(shell stat -f '%g' /var/run/docker.sock 2>/dev/null || stat -c '%g' /var/run/docker.sock 2>/dev/null || echo 0)
DOCKER_TEST_GROUPS := $(DOCKER_SOCK_GID),0
DOCKER_TEST_ENV := -e HOME=/tmp/sockerless-home -e USER=sockerless -e SOCKERLESS_DOCKER_TEST_UIDGID=$(DOCKER_TEST_USER) -e SOCKERLESS_DOCKER_TEST_GROUPS=$(DOCKER_TEST_GROUPS) -e CGO_ENABLED=0 -e TF_IN_AUTOMATION=1 -e CLOUDSDK_CORE_DISABLE_PROMPTS=1 -e AZURE_CORE_NO_COLOR=true -e AWS_PAGER=
# The simulator hands the container engine host paths — a webjob's materialized
# directory, an Azure Files share a workload mounts — and the engine resolves a
# mount source on the host it runs on, not inside the client container. Running
# the simulator in this harness therefore needs one directory that is the same
# path on both sides: the engine's own host directory, mounted into the
# container at its own path and made the simulator's temporary directory.
# Without it every workload bind-mounts an empty directory the engine created,
# and a job reads none of the files it was given.
#
# It sits under /var/tmp rather than /tmp on purpose: /tmp on a container-engine
# host is commonly a memory-backed filesystem, which a build tree or a volume
# seed would fill.
DOCKER_TEST_SHARED_TMP := /var/tmp/skl
DOCKER_TEST_SHARED_TMP_MOUNT := -v $(DOCKER_TEST_SHARED_TMP):$(DOCKER_TEST_SHARED_TMP)

DOCKER_TEST_RUN_ENV := -e HOME=/tmp/sockerless-home -e USER=sockerless -e CGO_ENABLED=0 -e TF_IN_AUTOMATION=1 -e CLOUDSDK_CORE_DISABLE_PROMPTS=1 -e AZURE_CORE_NO_COLOR=true -e AWS_PAGER= -e NO_PROXY=localhost,127.0.0.1,::1,.localhost,*.localhost -e no_proxy=localhost,127.0.0.1,::1,.localhost,*.localhost -e TMPDIR=$(DOCKER_TEST_SHARED_TMP)

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
		--security-opt label=disable \
		--group-add $(DOCKER_SOCK_GID) \
		--group-add 0 \
		-v $(REPO_ROOT):/src \
		-v /var/run/docker.sock:/var/run/docker.sock \
		$(DOCKER_TEST_SHARED_TMP_MOUNT) \
		-w /src/simulator-azure \
		$(DOCKER_TEST_RUN_ENV) \
		$(DOCKER_SPEC_VALIDATION_FLAGS) \
		$(DOCKER_TEST_IMAGE) make test-all

# The shard filter and the deadline travel into the container, so a sharded
# or long-budget local run is what the caller asked for.
docker-sdk-test: docker-test-build ## run SDK tests inside the Linux real-network docker harness
	docker run --rm --network host \
		--security-opt label=disable \
		--group-add $(DOCKER_SOCK_GID) \
		--group-add 0 \
		-v $(REPO_ROOT):/src \
		-v /var/run/docker.sock:/var/run/docker.sock \
		$(DOCKER_TEST_SHARED_TMP_MOUNT) \
		-w /src/simulator-azure \
		$(DOCKER_TEST_RUN_ENV) \
		$(DOCKER_SPEC_VALIDATION_FLAGS) \
		$(DOCKER_TEST_IMAGE) make sdk-test-local SDK_TEST_TIMEOUT="$(SDK_TEST_TIMEOUT)" SDK_TEST_ARGS="$(SDK_TEST_ARGS)"
