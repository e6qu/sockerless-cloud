#!/usr/bin/env bash
# check-zsh-special-vars.sh — refuse assignments to names zsh reserves.
#
# CI runs scripts/check-latest-deps.sh under zsh and syntax-checks every script
# with `zsh -n`. Neither catches this class: `zsh -n` parses without executing,
# and the damage is at runtime.
#
# zsh ties `path` to `PATH` as an array, so `local path=$1` — or a bare `local
# a path b` with no assignment at all — empties the command search path for the
# rest of the scope and every external command silently fails to resolve. `status` is read-only (an alias for `?`), so assigning to it
# aborts the script. Both parse cleanly under bash and under `zsh -n`.
#
# The freshness gate's Go-tool section shipped with `local path=$1`, passed
# every bash run, and reported "no module resolves" for all three tools the
# first time CI ran it under zsh.

set -euo pipefail

if ! REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null); then
	REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
fi
cd "$REPO_ROOT"

# Assignment to any of these breaks or aborts a zsh run.
readonly RESERVED='path|status|fpath|cdpath|manpath|mailpath|module_path|psvar|argv|options|watch'

findings=$(
	git ls-files '*.sh' 'scripts/*' | sort -u | while IFS= read -r f; do
		[[ -f $f ]] || continue
		head -c 2 "$f" | grep -q '#!' || continue
		# Comment lines are prose, not bindings: "a read path" is not an
		# assignment, and flagging it trains readers to ignore this gate.
		grep -vE "^[[:space:]]*#" "$f" | grep -nE \
			"^[[:space:]]*(local[[:space:]]+|declare[[:space:]]+|typeset[[:space:]]+|export[[:space:]]+)?($RESERVED)=" \
			| sed "s|^|$f:|" || true
		# A bare declaration shadows the name just as an assignment does:
		# `local a path b` empties PATH for the whole function.
		grep -vE "^[[:space:]]*#" "$f" | grep -nE \
			"^[[:space:]]*(local|declare|typeset)[[:space:]]+([A-Za-z_][A-Za-z0-9_]*(=[^[:space:]]*)?[[:space:]]+)*($RESERVED)([[:space:]]|=|$)" \
			| sed "s|^|$f:|" || true
		# `read -r ... <name> ...` binds the name the same way.
		# Only `read` in command position binds a name. The word inside
		# `echo "a read path ..."` does not.
		grep -vE "^[[:space:]]*#" "$f" | grep -nE \
			"(^|[;|&]|\bdo\b|\bwhile\b|\buntil\b)[[:space:]]*([A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]+)*read[[:space:]]+(-[A-Za-z]+[[:space:]]+)*[A-Za-z0-9_ ]*\b($RESERVED)\b" \
			| sed "s|^|$f:|" || true
	done
)

if [[ -n $findings ]]; then
	echo "check-zsh-special-vars: assignment to a name zsh reserves." >&2
	echo "  zsh binds 'path' to PATH and makes 'status' read-only; both break at" >&2
	echo "  runtime only, so bash runs and 'zsh -n' both pass. Rename the variable." >&2
	echo "$findings" >&2
	exit 1
fi

echo "check-zsh-special-vars: no script assigns a zsh-reserved name"
