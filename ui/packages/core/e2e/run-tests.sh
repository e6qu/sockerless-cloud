#!/usr/bin/env bash
set -euo pipefail

# Playwright enables FORCE_COLOR for child processes. Carrying NO_COLOR at the
# same time makes Bun and Node report conflicting color configuration instead
# of running the browser suite quietly.
unset NO_COLOR
exec playwright test "$@"
