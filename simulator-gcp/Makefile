# Standardized Makefile for simulator-gcp — see docs/MAKEFILE_STANDARD.md.
#
# The support modules (sim, ui-auth) resolve through the repo-root go.work
# during development and through their pseudo-version pins under
# `go install ...@version`. `build` embeds the console built in ui/ (build the
# UI first); `build-api`, which the test suites use, builds without it.

APP_NAME       := simulator-gcp
GO_PACKAGE     := .
UI_PACKAGE     := simulator-gcp
DEFAULT_PORT   := 4567
GO_ENV         := CGO_ENABLED=0
GO_LDFLAGS      := -s -w
RUN_ENV        := SIM_LISTEN_ADDR=:4567
RUN_FLAGS      :=

REPO_ROOT_REL  := ..

include $(REPO_ROOT_REL)/make/go-app.mk

# sdk-tests, cli-tests and terraform-tests are separate Go modules; CI calls
# these targets directly.

.PHONY: unit-test race-test sdk-test cli-test terraform-test terraform-test-native terraform-test-docker terraform-https-test terraform-https-test-docker

# CI passes a deadline under its step budget so a hung test fails with a
# named Go stack instead of an opaque step kill; locally the default matches
# go test's own.
UNIT_TEST_TIMEOUT ?= 10m

unit-test: ## go test the simulator module's own package-internal tests (fast; no Docker/real-exec)
	$(GO_ENV) go test -count=1 -timeout $(UNIT_TEST_TIMEOUT) ./...

# The race detector needs cgo, which unit-test builds without.
RACE_TEST_TIMEOUT ?= 20m
RACE_TEST_PKGS ?= ./...

race-test: ## go test the simulator module's own tests under the race detector
	CGO_ENABLED=1 go test -race -count=1 -timeout $(RACE_TEST_TIMEOUT) $(RACE_TEST_PKGS)

# A sharded CI job narrows its shard with `-run` here; the default runs
# everything.
SDK_TEST_ARGS ?= ./...
CLI_TEST_ARGS ?= ./...

sdk-test: build-api ## go test the SDK-driven sim tests
	cd sdk-tests && $(GO_ENV) bash ../../scripts/go-test-summary.sh -count=1 $(SDK_TEST_ARGS)

cli-test: build-api ## go test the CLI-driven sim tests
	cd cli-tests && $(GO_ENV) bash ../../scripts/go-test-summary.sh -count=1 -timeout 900s $(CLI_TEST_ARGS)

TERRAFORM_TEST_TIMEOUT ?= 300s
TERRAFORM_TEST_ARGS ?=

terraform-test: ## go test the terraform-driven sim tests
	@if [ "$$(uname -s)" = "Darwin" ]; then \
	  $(MAKE) terraform-test-docker; \
	else \
	  $(MAKE) terraform-test-native; \
	fi

terraform-test-native: build-api
	cd terraform-tests && $(GO_ENV) go test -v -count=1 -timeout $(TERRAFORM_TEST_TIMEOUT) $(TERRAFORM_TEST_ARGS) ./...

terraform-https-test: ## go test terraform via the optional Caddy HTTPS gateway
	@if [ "$$(uname -s)" = "Darwin" ]; then \
	  $(MAKE) terraform-https-test-docker; \
	else \
	  $(MAKE) build-api && \
	  cd terraform-tests && SOCKERLESS_TF_HTTPS_GATEWAY=1 $(GO_ENV) go test -v -count=1 -timeout $(TERRAFORM_TEST_TIMEOUT) $(TERRAFORM_TEST_ARGS) ./...; \
	fi

.PHONY: test-all
test-all: unit-test sdk-test cli-test terraform-test ## run every test category

.PHONY: docker-build docker-run docker-test docker-test-build

IMAGE := sockerless-simulator-gcp
DOCKER_TEST_IMAGE := sockerless-test-gcp
DOCKER_TEST_USER := $(shell id -u):$(shell id -g)
DOCKER_SOCK_GID := $(shell stat -f '%g' /var/run/docker.sock 2>/dev/null || stat -c '%g' /var/run/docker.sock 2>/dev/null || echo 0)
DOCKER_TEST_GROUPS := $(DOCKER_SOCK_GID),0
DOCKER_TEST_ENV := -e HOME=/tmp/sockerless-home -e USER=sockerless -e SOCKERLESS_DOCKER_TEST_UIDGID=$(DOCKER_TEST_USER) -e SOCKERLESS_DOCKER_TEST_GROUPS=$(DOCKER_TEST_GROUPS) -e CGO_ENABLED=0 -e TF_IN_AUTOMATION=1 -e CLOUDSDK_CORE_DISABLE_PROMPTS=1 -e AZURE_CORE_NO_COLOR=true -e AWS_PAGER=
# The engine resolves bind-mount sources (a Cloud Build workspace, a Cloud Run
# volume) on its own host, so the simulator's temporary directory must be the
# same path inside the test container and on the host. It sits under /var/tmp
# because /tmp on an engine host is commonly memory-backed.
DOCKER_TEST_SHARED_TMP := /var/tmp/skl
DOCKER_TEST_SHARED_TMP_MOUNT := -v $(DOCKER_TEST_SHARED_TMP):$(DOCKER_TEST_SHARED_TMP)

DOCKER_TEST_RUN_ENV := -e HOME=/tmp/sockerless-home -e USER=sockerless -e CGO_ENABLED=0 -e TF_IN_AUTOMATION=1 -e CLOUDSDK_CORE_DISABLE_PROMPTS=1 -e AZURE_CORE_NO_COLOR=true -e AWS_PAGER= -e TMPDIR=$(DOCKER_TEST_SHARED_TMP)

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
		-w /src/simulator-gcp \
		$(DOCKER_TEST_RUN_ENV) \
		$(DOCKER_TEST_IMAGE) make test-all

terraform-test-docker: docker-test-build ## run terraform tests on a capable Linux real-network host
	docker run --rm --network host \
		--security-opt label=disable \
		--group-add $(DOCKER_SOCK_GID) \
		--group-add 0 \
		-v $(REPO_ROOT):/src \
		-v /var/run/docker.sock:/var/run/docker.sock \
		$(DOCKER_TEST_SHARED_TMP_MOUNT) \
		-w /src/simulator-gcp \
		$(DOCKER_TEST_RUN_ENV) \
		$(DOCKER_TEST_IMAGE) make terraform-test-native

terraform-https-test-docker: docker-test-build ## run terraform HTTPS tests inside docker
	docker run --rm --network host \
		--security-opt label=disable \
		--group-add $(DOCKER_SOCK_GID) \
		--group-add 0 \
		-v $(REPO_ROOT):/src \
		-v /var/run/docker.sock:/var/run/docker.sock \
		$(DOCKER_TEST_SHARED_TMP_MOUNT) \
		-w /src/simulator-gcp/terraform-tests \
		$(DOCKER_TEST_RUN_ENV) \
		-e SOCKERLESS_TF_HTTPS_GATEWAY=1 \
		$(DOCKER_TEST_IMAGE) go test -v -count=1 -timeout $(TERRAFORM_TEST_TIMEOUT) ./...
