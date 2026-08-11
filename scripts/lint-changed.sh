#!/usr/bin/env bash
# Lint Go modules that contain changed files.

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

# Collect unique module directories
modules=()
for f in "$@"; do
  dir=$(dirname "$f")
  while [ "$dir" != "." ] && [ ! -f "$dir/go.mod" ]; do
    dir=$(dirname "$dir")
  done
  if [ -f "$dir/go.mod" ]; then
    modules+=("$dir")
  fi
done

if [ "${#modules[@]}" -eq 0 ]; then
  exit 0
fi

# Deduplicate
sorted=$(printf '%s\n' "${modules[@]}" | sort -u)

failed=0
# Track placeholder dist/ directories so we tear them down on exit
# regardless of whether linting succeeds or fails.
placeholders=()
# shellcheck disable=SC2317,SC2329  # invoked via `trap`, not directly
cleanup() {
  if [ "${#placeholders[@]}" -eq 0 ]; then
    return
  fi
  for p in "${placeholders[@]}"; do
    rm -rf "$p"
  done
}
trap cleanup EXIT

for mod in $sorted; do
  # A module may exist only to anchor shared metadata or generated assets.
  # golangci-lint reports a context-loading failure when that module has no
  # Go package at all, so only invoke it for modules containing Go source.
  if ! find "$mod" -type f -name '*.go' -print -quit | grep -q .; then
    echo "lint: $mod (no Go source; skipped)"
    continue
  fi
  # Modules with a `//go:embed all:dist` directive fail to compile when
  # dist/ doesn't exist, which masks every other lint finding. Stub a
  # placeholder so the lint covers the rest of the package. Real
  # builds rebuild dist/ in their own flow.
  if grep -qr 'all:dist' "$mod"/*.go 2>/dev/null && [ ! -d "$mod/dist" ]; then
    mkdir -p "$mod/dist"
    touch "$mod/dist/.lint-placeholder"
    placeholders+=("$mod/dist")
  fi
  echo "lint: $mod"
  if ! (cd "$mod" && golangci-lint run --timeout 2m ./...); then
    failed=1
  fi
done

exit $failed
