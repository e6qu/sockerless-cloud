# Standard Go library Makefile for the real-execution substrate.

GO_ENV := GOCACHE=$(CURDIR)/../.gocache/realexec

.PHONY: install build build-noui test test-integration lint run clean upgrade-deps help

install:
	@$(GO_ENV) go mod download

build:
	@$(GO_ENV) go build ./...

build-noui: build

test:
	@$(GO_ENV) go test -v ./...

test-integration:
	@$(GO_ENV) go test -v -timeout 5m ./...

lint:
	@$(GO_ENV) go vet ./...
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
	  printf 'gofmt -l .\n%s\n' "$$unformatted"; \
	  exit 1; \
	fi

run:
	@echo "n/a for Go library"

clean:
	@$(GO_ENV) go clean -testcache

upgrade-deps:
	@$(GO_ENV) go get -u ./...
	@$(GO_ENV) go mod tidy

help:
	@printf '%s\n' 'install build build-noui test test-integration lint run clean upgrade-deps'
