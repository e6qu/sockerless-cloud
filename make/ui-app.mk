# ui-app.mk — standardized recipes for Vite/Bun UI packages.
#
# Required:
#   UI_PACKAGE   : workspace package basename, used in `bun --filter`
#                  e.g. @sockerless/ui-admin; use the full scoped package
#                  name because the unscoped basename is ambiguous.
#
# Optional:
#   DEV_PORT     : informational; printed in the run banner
#   REPO_ROOT_REL: path from this Makefile's dir to the repo root.
#                  Default ../../.. (correct for ui/packages/<x>/).

REPO_ROOT_REL ?= ../../..
REPO_ROOT     := $(abspath $(CURDIR)/$(REPO_ROOT_REL))

include $(REPO_ROOT)/make/help.mk
include $(REPO_ROOT)/make/colors.mk

.PHONY: install build run dev preview test lint clean

install: ## install workspace deps (runs at the workspace root)
	cd $(REPO_ROOT)/ui && bun install

# `bun run` from inside the package picks up the package's own scripts.
build: install ## build the production UI bundle into ./dist
	bun run build

run: ## start the Vite dev server (default :5173)
	$(call STEP,$(notdir $(CURDIR)): vite dev$(if $(DEV_PORT), @ :$(DEV_PORT),))
	bun run dev

dev: run ## alias for `run`

preview: build ## serve the built bundle locally
	bun run preview

test: install ## run package tests when this UI package defines a test script
	@if bun -e "const p=require('./package.json'); process.exit(p.scripts && p.scripts.test ? 0 : 1)" >/dev/null 2>&1; then \
	  bun run test; \
	else \
	  printf "$(COLOR_DIM)%s: no test script configured.$(COLOR_RESET)\n" "$(notdir $(CURDIR))"; \
	fi

lint: install ## type-check via tsc --noEmit
	bunx tsc --noEmit

clean: ## remove dist + node_modules + turbo cache
	rm -rf dist node_modules .turbo
