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

sorted=$(printf '%s\n' "${modules[@]}" | sort -u)

failed=0
for mod in $sorted; do
  # A module may exist only to anchor shared metadata or generated assets.
  # golangci-lint reports a context-loading failure when that module has no
  # Go package at all, so only invoke it for modules containing Go source.
  if ! find "$mod" -type f -name '*.go' -print -quit | grep -q .; then
    echo "lint: $mod (no Go source; skipped)"
    continue
  fi
  echo "lint: $mod"
  # The timeout is .golangci.yml's alone: a shorter one here fails the largest
  # module's package load on a busy machine.
  # The module's Makefile sets LINT_ENV where its lint needs one, as
  # simulator-aws's one compile at a time does.
  lint_env=$(sed -n 's/^LINT_ENV *:= *//p' "$mod/Makefile" 2>/dev/null)
  # shellcheck disable=SC2086
  if ! (cd "$mod" && env $lint_env golangci-lint run ./...); then
    failed=1
  fi
done

exit $failed
