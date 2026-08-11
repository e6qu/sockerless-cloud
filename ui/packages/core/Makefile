# Standardized Makefile for ui-core — shared TS/React library, no
# Vite bundle. Build is a typecheck; there's no `run` for a library.

UI_PACKAGE := core
REPO_ROOT  := $(abspath $(CURDIR)/../../..)

include $(REPO_ROOT)/make/help.mk
include $(REPO_ROOT)/make/colors.mk

.PHONY: install build test lint clean run dev

install: ## install workspace deps
	cd $(REPO_ROOT)/ui && bun install

build: install ## type-check the library (tsc --noEmit)
	bun run build

test: install ## run vitest
	bun run test

lint: install ## type-check
	bunx tsc --noEmit

run: ## n/a for a library
	@printf "$(COLOR_DIM)ui-core is a library, nothing to run.$(COLOR_RESET)\n"

dev: run ## alias for `run`

clean: ## remove node_modules + turbo cache
	rm -rf node_modules .turbo
