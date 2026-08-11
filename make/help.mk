# help.mk — auto-generated `help` target.
#
# Any leaf Makefile that includes this gets a `make help` target that
# prints every target whose recipe carries a `## description` comment.
# The default goal becomes `help` so a bare `make` is informative.

include $(dir $(lastword $(MAKEFILE_LIST)))colors.mk

.DEFAULT_GOAL := help

.PHONY: help
help: ## show this help
	@printf "$(COLOR_BOLD)%s$(COLOR_RESET)\n" "$(notdir $(CURDIR))"
	@printf "$(COLOR_DIM)$(MAKEFILE_LIST)$(COLOR_RESET)\n\n"
	@awk 'BEGIN { FS = ":.*##[ ]?" } \
	      /^[a-zA-Z0-9_.-]+:.*##/ { \
	        printf "  $(COLOR_GREEN)%-18s$(COLOR_RESET) %s\n", $$1, $$2 \
	      }' $(MAKEFILE_LIST)
