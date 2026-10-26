# See docs/MAKEFILE_STANDARD.md. `build` embeds the console built in ui/
# (build the UI first); `build-api`, which the test suites use, builds
# without it.

APP_NAME       := simulator-azure
GO_PACKAGE     := .
UI_PACKAGE     := simulator-azure
DEFAULT_PORT   := 4568
GO_ENV         := CGO_ENABLED=0
GO_LDFLAGS      := -s -w
RUN_ENV        := SIM_LISTEN_ADDR=:4568
RUN_FLAGS      :=

# Each `az` invocation pays ~0.5-2s of Python startup, so the CLI suite's
# runtime grows with its test count. 13m is the ceiling: the CI step allows 14
# minutes, and a larger inner deadline would surface as an opaque step kill
# instead of a named Go stack.
CLI_TEST_TIMEOUT ?= 13m

REPO_ROOT_REL  := ..

include $(REPO_ROOT_REL)/make/go-app.mk

.PHONY: unit-test race-test sdk-test sdk-test-local docker-sdk-test cli-test terraform-test

# CI passes a deadline under its step budget so a hung test fails with a
# named Go stack instead of an opaque step kill; locally the default matches
# go test's own.
UNIT_TEST_TIMEOUT ?= 10m

unit-test: ## go test the simulator module's own package-internal tests (fast; no Docker/real-exec)
	$(GO_ENV) go test -count=1 -timeout $(UNIT_TEST_TIMEOUT) ./...

# Separate from unit-test because the race detector needs cgo, which unit-test
# does not have.
RACE_TEST_TIMEOUT ?= 20m
RACE_TEST_PKGS ?= ./...

race-test: ## go test the simulator module's own tests under the race detector
	CGO_ENABLED=1 go test -race -count=1 -timeout $(RACE_TEST_TIMEOUT) $(RACE_TEST_PKGS)

ifeq ($(shell uname -s),Darwin)
sdk-test: docker-sdk-test ## go test the SDK-driven sim tests in the Linux real-network harness on macOS
else
sdk-test: sdk-test-local ## go test the SDK-driven sim tests
endif

# The Azure Cosmos DB emulator differential tests are slow and variable, so the
# SDK suite needs more than Go's 10-minute default.
SDK_TEST_TIMEOUT ?= 13m

# A sharded CI job narrows its suite with `-run`; the default runs everything.
SDK_TEST_ARGS ?= ./...
CLI_TEST_ARGS ?= ./...

sdk-test-local: build-api ## go test the SDK-driven sim tests on a Linux host with real-network capabilities
	cd sdk-tests && $(GO_ENV) bash ../../scripts/go-test-summary.sh -count=1 -timeout $(SDK_TEST_TIMEOUT) $(SDK_TEST_ARGS)

cli-test: build-api ## go test the CLI-driven sim tests
	cd cli-tests && $(GO_ENV) bash ../../scripts/go-test-summary.sh -count=1 -timeout $(CLI_TEST_TIMEOUT) $(CLI_TEST_ARGS)

# TERRAFORM_TEST_TIMEOUT bounds each apply and destroy; the outer go test
# timeout only has to outlast it.
TERRAFORM_TEST_TIMEOUT ?= 600s

# CI's Terraform shards select one apply/destroy entrypoint per job with `-run`.
TERRAFORM_TEST_ARGS ?=

terraform-test: build-api ## go test the terraform-driven sim tests (apply+destroy deadline = TERRAFORM_TEST_TIMEOUT)
	cd terraform-tests && TERRAFORM_TEST_TIMEOUT=$(TERRAFORM_TEST_TIMEOUT) $(GO_ENV) go test -v -count=1 -timeout 20m $(TERRAFORM_TEST_ARGS) ./...

.PHONY: test-all
test-all: unit-test sdk-test cli-test terraform-test ## run every test category

.PHONY: docker-build docker-run docker-test docker-test-build

IMAGE := sockerless-simulator-azure
DOCKER_TEST_IMAGE := sockerless-test-azure
DOCKER_TEST_USER := $(shell id -u):$(shell id -g)
DOCKER_SOCK_GID := $(shell stat -f '%g' /var/run/docker.sock 2>/dev/null || stat -c '%g' /var/run/docker.sock 2>/dev/null || echo 0)
DOCKER_TEST_GROUPS := $(DOCKER_SOCK_GID),0
DOCKER_TEST_ENV := -e HOME=/tmp/sockerless-home -e USER=sockerless -e SOCKERLESS_DOCKER_TEST_UIDGID=$(DOCKER_TEST_USER) -e SOCKERLESS_DOCKER_TEST_GROUPS=$(DOCKER_TEST_GROUPS) -e CGO_ENABLED=0 -e TF_IN_AUTOMATION=1 -e CLOUDSDK_CORE_DISABLE_PROMPTS=1 -e AZURE_CORE_NO_COLOR=true -e AWS_PAGER=
# The engine resolves a bind-mount source on its own host, not inside this
# container, so the simulator's temporary directory must be the same path on
# both sides; otherwise every workload mounts an empty directory. /var/tmp,
# not /tmp: /tmp on an engine host is often memory-backed, and a build tree or
# a volume seed would fill it.
DOCKER_TEST_SHARED_TMP := /var/tmp/skl
DOCKER_TEST_SHARED_TMP_MOUNT := -v $(DOCKER_TEST_SHARED_TMP):$(DOCKER_TEST_SHARED_TMP)

DOCKER_TEST_RUN_ENV := -e HOME=/tmp/sockerless-home -e USER=sockerless -e CGO_ENABLED=0 -e TF_IN_AUTOMATION=1 -e CLOUDSDK_CORE_DISABLE_PROMPTS=1 -e AZURE_CORE_NO_COLOR=true -e AWS_PAGER= -e NO_PROXY=localhost,127.0.0.1,::1,.localhost,*.localhost -e no_proxy=localhost,127.0.0.1,::1,.localhost,*.localhost -e TMPDIR=$(DOCKER_TEST_SHARED_TMP)

# Carry the spec-validation coordinates into the container; without them a
# macOS run of `SOCKERLESS_SPEC_VALIDATE=... make sdk-test` validates nothing.
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

# Pass the shard filter and the deadline through to the inner make.
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
