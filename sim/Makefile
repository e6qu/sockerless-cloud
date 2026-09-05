# Standard Go library Makefile — recipes come from make/go-lib.mk.

REPO_ROOT_REL := ..
GO_ENV        := CGO_ENABLED=0

include $(REPO_ROOT_REL)/make/go-lib.mk

# The same tests under the race detector, which needs cgo.
RACE_TEST_TIMEOUT ?= 20m

.PHONY: race-test
race-test: ## run the unit tests under the race detector
	CGO_ENABLED=1 go test -race -count=1 -timeout $(RACE_TEST_TIMEOUT) ./...
