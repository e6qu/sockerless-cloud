# colors.mk — pretty output helpers for sockerless app Makefiles.
# Detects whether we're in a TTY (so CI logs stay clean) and either
# emits ANSI colour codes or empty strings.

ifeq ($(NO_COLOR),)
ifneq ($(shell tput colors 2>/dev/null),)
  COLOR_RESET := \033[0m
  COLOR_DIM   := \033[2m
  COLOR_BOLD  := \033[1m
  COLOR_RED   := \033[31m
  COLOR_GREEN := \033[32m
  COLOR_YEL   := \033[33m
  COLOR_BLUE  := \033[34m
  COLOR_MAG   := \033[35m
  COLOR_CYAN  := \033[36m
endif
endif

# Convenience macro: STEP "message" prints a banner line.
STEP = @printf "\n$(COLOR_CYAN)▸ %s$(COLOR_RESET)\n" "$(1)"
