#!/usr/bin/env bash
# test-check-simulator-tests.sh: proves check-simulator-tests.sh tells a
# moved registration from a new one. Each case builds a throwaway repository,
# commits a starting tree, stages a change, and runs the check against it.
set -euo pipefail

check="$(cd "$(dirname "$0")" && pwd)/check-simulator-tests.sh"
failures=0

# A pre-commit hook runs with GIT_DIR, GIT_INDEX_FILE and their kin pointing at
# the repository being committed to; a throwaway repository built under them
# would write that repository's config and index instead of its own.
# shellcheck disable=SC2046
unset $(git rev-parse --local-env-vars)
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com

new_repo() {
    local dir
    dir="$(mktemp -d)"
    git -C "$dir" init -q
    mkdir -p "$dir/simulator-gcp" "$dir/simulator-aws"
    echo "$dir"
}

commit_all() {
    git -C "$1" add -A
    git -C "$1" commit -q -m "$2"
}

expect() {
    local want="$1" name="$2" dir="$3" got=0
    (cd "$dir" && bash "$check" >"$dir/out" 2>&1) || got=$?
    if [[ "$want" == pass && "$got" -ne 0 ]] || [[ "$want" == fail && "$got" -eq 0 ]]; then
        echo "FAIL $name: expected the check to $want, exit $got" >&2
        sed 's/^/    /' "$dir/out" >&2
        failures=$((failures + 1))
    else
        echo "ok   $name"
    fi
    rm -rf "$dir"
}

# A route moved verbatim from one file to another is an existing route.
dir="$(new_repo)"
printf 'package main\nfunc a(srv S) {\n\tsrv.HandleFunc("GET /v1/things", list)\n\tsrv.HandleFunc("POST /v1/things", create)\n}\n' >"$dir/simulator-gcp/big.go"
commit_all "$dir" start
printf 'package main\nfunc a(srv S) {\n\tsrv.HandleFunc("POST /v1/things", create)\n}\n' >"$dir/simulator-gcp/big.go"
printf 'package main\nfunc b(srv S) {\n\tsrv.HandleFunc("GET /v1/things", list)\n}\n' >"$dir/simulator-gcp/things.go"
git -C "$dir" add -A
expect pass "a route moved to another file needs no test" "$dir"

# A route whose handler changes keeps its token and needs no test.
dir="$(new_repo)"
printf 'package main\nfunc a(srv S) {\n\tsrv.HandleFunc("GET /v1/things", func(w W, r R) {})\n}\n' >"$dir/simulator-gcp/big.go"
commit_all "$dir" start
printf 'package main\nfunc a(srv S) {\n\tsrv.HandleFunc("GET /v1/things", handleListThings)\n}\n' >"$dir/simulator-gcp/big.go"
git -C "$dir" add -A
expect pass "a route whose handler changes needs no test" "$dir"

# A new route beside a moved one still needs its test.
dir="$(new_repo)"
printf 'package main\nfunc a(srv S) {\n\tsrv.HandleFunc("GET /v1/things", list)\n}\n' >"$dir/simulator-gcp/big.go"
commit_all "$dir" start
: >"$dir/simulator-gcp/big.go"
printf 'package main\nfunc b(srv S) {\n\tsrv.HandleFunc("GET /v1/things", list)\n\tsrv.HandleFunc("DELETE /v1/things/{id}", remove)\n}\n' >"$dir/simulator-gcp/things.go"
git -C "$dir" add -A
expect fail "a new route beside a moved one still needs its test" "$dir"

# A route removed from one cloud and added to another is new to that cloud.
dir="$(new_repo)"
printf 'package main\nfunc a(srv S) {\n\tsrv.HandleFunc("GET /v1/things", list)\n}\n' >"$dir/simulator-aws/big.go"
commit_all "$dir" start
: >"$dir/simulator-aws/big.go"
printf 'package main\nfunc b(srv S) {\n\tsrv.HandleFunc("GET /v1/things", list)\n}\n' >"$dir/simulator-gcp/things.go"
git -C "$dir" add -A
expect fail "a route moved to another cloud needs its test" "$dir"

# An operation moved between files is an existing operation.
dir="$(new_repo)"
printf 'package main\nfunc a(r R) {\n\tr.Register("ListThings", list)\n}\n' >"$dir/simulator-aws/big.go"
commit_all "$dir" start
: >"$dir/simulator-aws/big.go"
printf 'package main\nfunc b(r R) {\n\tr.Register("ListThings", list)\n}\n' >"$dir/simulator-aws/things.go"
git -C "$dir" add -A
expect pass "an operation moved to another file needs no test" "$dir"

if [[ "$failures" -ne 0 ]]; then
    echo "$failures check-simulator-tests case(s) failed" >&2
    exit 1
fi
