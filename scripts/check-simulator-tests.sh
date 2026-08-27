#!/usr/bin/env bash
# check-simulator-tests.sh — enforces the testing contract:
# every simulator code change must ship with matching SDK + CLI +
# terraform-test coverage. Runs as a pre-commit hook.
#
# Contract:
#   - Any new `r.Register("<operation>", ...)` or
#     `r.RegisterVersioned("<api-version>", "<operation>", ...)` line added under
#     simulator-<cloud>/ (outside *_test.go / docs / README) must be
#     referenced in a *test file* within the same commit under
#     simulator-<cloud>/{sdk-tests,cli-tests,terraform-tests}.
#   - The same applies to newly-added `(srv|mux).HandleFunc("<METHOD> <path>", ...)`
#     route mounts: an SDK/CLI/terraform-exercisable REST endpoint mounted that
#     way must ship with a test that references its route path — or be listed on
#     tests-exempt.txt (for internal runtime/metadata/dashboard routes no
#     SDK/CLI/terraform surface exposes). The reference token is the route's
#     literal path (e.g. `/apps/{appId}/domains`).
#   - An operation, or a HandleFunc route's literal "<METHOD> <path>" token, can
#     be placed on simulator-<cloud>/tests-exempt.txt to opt out (e.g. Lambda
#     Runtime API routes internal to the container lifecycle, IMDS metadata
#     routes, or sim dashboard routes).
#
# Diff scoping: only NEWLY-ADDED ('+') Register/RegisterVersioned/HandleFunc
# lines in the diff are enforced. The hundreds of pre-existing HandleFunc routes
# are grandfathered — they are never retroactively required to grow tests.
#
# Usage:
#   scripts/check-simulator-tests.sh           # check staged changes
#   scripts/check-simulator-tests.sh --ref HEAD^  # check between ref and HEAD
set -euo pipefail

ref="${1:-}"
if [[ "$ref" == "--ref" && -n "${2:-}" ]]; then
    staged_range="$2..HEAD"
else
    staged_range="--cached"
fi

# The contract is incremental: an operation registered IN THIS CHANGE must
# bring its SDK/CLI/terraform tests IN THIS CHANGE. A root commit has no
# parent, so "the change" is the whole already-gated tree imported from the
# sockerless monorepo — every historical registration would be judged as new,
# including routes that pre-date the hook. There is no incremental change to
# gate, so pass explicitly rather than mis-applying the contract.
if ! git rev-parse --verify HEAD >/dev/null 2>&1; then
    echo "[simulator-tests] root commit (no parent): nothing incremental to gate"
    exit 0
fi

# Collect changed simulator .go files that aren't test files
changed_go=$(git diff --name-only "$staged_range" 2>/dev/null \
    | grep -E '^simulator-(aws|gcp|azure)/[^/]*\.go$' \
    | grep -vE '(_test\.go|/docs/|/README\.md|/go\.mod|/go\.sum)$' \
    || true)

if [[ -z "$changed_go" ]]; then
    exit 0
fi

regex_escape() { printf '%s' "$1" | sed 's|[][\\.*^$/]|\\&|g'; }

# Added ('+') lines across the changed PRODUCTION simulator .go files only
# (`changed_go` already excludes *_test.go). A route mounted in a _test.go
# fixture is not a real endpoint and must not require SDK/CLI/terraform coverage.
# shellcheck disable=SC2086
added_lines=$(git diff "$staged_range" -- $changed_go 2>/dev/null \
    | grep -E '^\+[^+]' || true)

# Newly-registered operations (Register / RegisterVersioned).
newly_registered=$(printf '%s\n' "$added_lines" \
    | grep -E 'r\.Register(Versioned)?\s*\(' \
    | sed -nE \
        -e 's/.*r\.Register\s*\(\s*"([^"]+)".*/\1/p' \
        -e 's/.*r\.RegisterVersioned\s*\(\s*[^,]+\s*,\s*"([^"]+)".*/\1/p' \
    | sort -u || true)

# Newly-added HandleFunc route mounts whose first argument is a literal
# "METHOD /path" string. The route token is "METHOD /literal-path-prefix":
# the literal portion of the path up to the first concatenation (closing quote),
# which is robust to forms like  "POST /"+cfAPIVersion+"/distribution".
# Routes whose first argument is not a "METHOD /…" literal (rare) are ignored —
# they cannot be reliably keyed, and the Register/op path already covers SDK ops.
handlefunc_routes=$(printf '%s\n' "$added_lines" \
    | grep -E '(srv|mux)\.HandleFunc\s*\(\s*"[A-Z]+ /' \
    | sed -nE 's/.*HandleFunc\s*\(\s*"([A-Z]+ \/[^"]*)".*/\1/p' \
    | sed -E 's/[[:space:]]+$//' \
    | sort -u || true)

if [[ -z "$newly_registered" && -z "$handlefunc_routes" ]]; then
    exit 0
fi

# Collect test-file changes by cloud.
get_tests_for_cloud() {
    local cloud="$1"
    git diff --name-only "$staged_range" 2>/dev/null \
        | grep -E "^simulator-${cloud}/(sdk-tests|cli-tests|terraform-tests)/" \
        || true
}

changed_sim_files=$(git diff --name-only "$staged_range" 2>/dev/null | grep -E '^simulator-' || true)

# Materialize the added ('+') diff lines ONCE per cloud, for both the
# production sim files and the test files. Every lookup below greps these
# capture files. Diffing per (op, file) pair instead was quadratic: an import
# commit that registers thousands of operations across hundreds of files spent
# tens of minutes re-running `git diff` for every operation.
added_dir=$(mktemp -d)
trap 'rm -rf "$added_dir"' EXIT
for cloud in aws gcp azure; do
    prod_files=$(printf '%s\n' "$changed_sim_files" \
        | grep -E "^simulator-${cloud}/[^/]*\.go$" | grep -vE '_test\.go$' || true)
    if [[ -n "$prod_files" ]]; then
        # shellcheck disable=SC2086
        git diff "$staged_range" -- $prod_files 2>/dev/null \
            | grep -E '^\+[^+]' > "$added_dir/prod-$cloud" || true
    else
        : > "$added_dir/prod-$cloud"
    fi
    test_files=$(get_tests_for_cloud "$cloud")
    if [[ -n "$test_files" ]]; then
        # shellcheck disable=SC2086
        git diff "$staged_range" -- $test_files 2>/dev/null \
            | grep -E '^\+' > "$added_dir/tests-$cloud" || true
    else
        : > "$added_dir/tests-$cloud"
    fi
done

# Cloud of a registered op: the cloud whose added production diff defines its
# Register line.
op_to_cloud() {
    local op escaped_op cloud
    op="$1"; escaped_op="$(regex_escape "$op")"
    for cloud in aws gcp azure; do
        if grep -qE "(r\.Register\s*\(\s*\"${escaped_op}\")|(r\.RegisterVersioned\s*\(\s*[^,]+\s*,\s*\"${escaped_op}\")" "$added_dir/prod-$cloud"; then
            echo "$cloud"; return
        fi
    done
}

# Cloud of a HandleFunc route: the cloud whose added production diff mounts it.
route_to_cloud() {
    local route escaped cloud
    route="$1"; escaped="$(regex_escape "$route")"
    for cloud in aws gcp azure; do
        if grep -qE "(srv|mux)\.HandleFunc\s*\(\s*\"${escaped}" "$added_dir/prod-$cloud"; then
            echo "$cloud"; return
        fi
    done
}

# An op is "referenced" only in a real call/assertion context on an added test
# line — not a bare comment/string mention:
#   SDK Go:  .<Op>(            CLI: "<kebab-op>"            literal: "<Op>"
op_referenced_in_tests() {
    local short cloud kebab
    short="$1"; cloud="$2"
    kebab=$(printf '%s' "$short" \
        | sed -E 's/([a-z0-9])([A-Z])/\1-\2/g; s/([A-Z]+)([A-Z][a-z])/\1-\2/g' \
        | tr '[:upper:]' '[:lower:]')
    local esc_short esc_kebab
    esc_short="$(regex_escape "$short")"; esc_kebab="$(regex_escape "$kebab")"
    grep -qE "\.${esc_short}\(|\"${esc_kebab}\"|\"${esc_short}\"" "$added_dir/tests-$cloud"
}

# A route is "referenced" when its literal path appears on an added test line,
# OR — for routes whose final path segment is a CamelCase operation name (the
# SDK-op-in-path form, e.g. /operation/PutDashboard, where the SDK/CLI client
# invokes the op by name rather than POSTing the literal wire path) — when that
# op is referenced in a call/assertion (.PutDashboard( / "put-dashboard"),
# OR — for a REST route mounted via cloudTrailRecordedREST("Op", …) (where the
# wire path is a lowercase subresource like /policy but the SDK/CLI invokes the
# named operation) — when that named operation is referenced.
route_referenced_in_tests() {
    # route_path, not `path`: zsh ties `path` to `PATH`, so a local named
    # `path` here would blank the command search path for this function and
    # every grep below would stop resolving.
    local route cloud route_path last_seg
    route="$1"; cloud="$2"
    route_path="${route#* }"
    if grep -qF "$route_path" "$added_dir/tests-$cloud"; then
        return 0
    fi
    # Trailing CamelCase segment ⇒ SDK op name; accept a call/assertion reference.
    last_seg="${route_path##*/}"
    if printf '%s' "$last_seg" | grep -qE '^[A-Z][a-zA-Z0-9]+$'; then
        op_referenced_in_tests "$last_seg" "$cloud" && return 0
    fi
    # cloudTrailRecordedREST("Op", …) names the operation at the route mount;
    # accept a call/assertion reference to that named op.
    local esc_route ctop
    esc_route="$(regex_escape "$route")"
    ctop=$(grep -E "HandleFunc\s*\(\s*\"${esc_route}\"" "$added_dir/prod-$cloud" \
            | sed -nE 's/.*cloudTrailRecordedREST(Dynamic)?\s*\(\s*"([A-Za-z0-9]+)".*/\2/p' \
            | head -1)
    if [[ -n "$ctop" ]]; then
        op_referenced_in_tests "$ctop" "$cloud" && return 0
    fi
    return 1
}

fail=0

for op in $newly_registered; do
    cloud=$(op_to_cloud "$op")
    if [[ -z "$cloud" ]]; then
        echo "[simulator-tests] FAIL: registered op \"$op\" — could not determine its cloud (no matching added Register line found in simulator-<cloud>/). Ops must not slip silently; define it in a simulator-<cloud>/*.go file in this commit." >&2
        fail=1; continue
    fi

    exempt_file="simulator-${cloud}/tests-exempt.txt"
    if [[ -f "$exempt_file" ]] && grep -qxF "$op" "$exempt_file"; then continue; fi

    short=$(printf '%s' "$op" | sed 's|.*\.||')
    tests_changed=$(get_tests_for_cloud "$cloud")
    if [[ -z "$tests_changed" ]]; then
        echo "[simulator-tests] FAIL: op \"$op\" ($cloud) — no test file changes under simulator-$cloud/{sdk-tests,cli-tests,terraform-tests}/ in this commit." >&2
        fail=1; continue
    fi

    if ! op_referenced_in_tests "$short" "$cloud"; then
        echo "[simulator-tests] FAIL: op \"$op\" ($cloud) — changed test files don't reference \"$short\" in a call/assertion (.$short( for SDK, \"${short}\"/kebab for CLI). Add a real SDK/CLI/terraform test, or add it to simulator-$cloud/tests-exempt.txt." >&2
        fail=1
    fi
done

while IFS= read -r route; do
    [[ -z "$route" ]] && continue
    cloud=$(route_to_cloud "$route")
    if [[ -z "$cloud" ]]; then
        echo "[simulator-tests] FAIL: HandleFunc route \"$route\" — could not determine its cloud. Routes must not slip silently." >&2
        fail=1; continue
    fi

    exempt_file="simulator-${cloud}/tests-exempt.txt"
    if [[ -f "$exempt_file" ]] && grep -qxF "$route" "$exempt_file"; then continue; fi

    tests_changed=$(get_tests_for_cloud "$cloud")
    if [[ -z "$tests_changed" ]]; then
        echo "[simulator-tests] FAIL: HandleFunc route \"$route\" ($cloud) — no test file changes under simulator-$cloud/{sdk-tests,cli-tests,terraform-tests}/ in this commit. If the route is internal (runtime/metadata/dashboard) and not SDK/CLI/terraform-exercisable, add the exact \"$route\" token to simulator-$cloud/tests-exempt.txt." >&2
        fail=1; continue
    fi

    if ! route_referenced_in_tests "$route" "$cloud"; then
        echo "[simulator-tests] FAIL: HandleFunc route \"$route\" ($cloud) — changed test files don't reference its path. Add an SDK/CLI/terraform test exercising the route, or add the exact \"$route\" token to simulator-$cloud/tests-exempt.txt." >&2
        fail=1
    fi
done <<< "$handlefunc_routes"

[[ $fail -ne 0 ]] && exit 1
exit 0
