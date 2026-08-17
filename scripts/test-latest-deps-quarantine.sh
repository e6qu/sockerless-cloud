#!/usr/bin/env bash
# Tests for check-latest-deps.sh's adoption quarantine — the rule that a
# dependency must have been public for at least a day before this project
# adopts it — in both directions, plus the negative controls that prove the
# gate still fails on everything it failed on before.
#
# The fixtures are real. A real go.mod pinned to a real superseded release, a
# real workflow pinned to a real superseded action tag, and a real
# required_providers block are resolved against the real Go module proxy, the
# real GitHub API, and the real Terraform registry; every publication timestamp
# the check compares against is the one the registry actually serves. Only the
# repository the check reads is a fixture, and only one thing is synthesised at
# all: a module proxy that answers with no publication time, which is the one
# condition no public registry can be asked to reproduce on demand.
#
# The quarantine window is widened, never narrowed, to put a known-old release
# inside it: check-latest-deps.sh clamps DEPS_ADOPTION_QUARANTINE_SECONDS up to
# its floor, so no test — and no CI environment — can weaken the gate, and one
# of the cases below proves that clamp holds.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
fixture="$(mktemp -d)"
# Go's module cache is written read-only, so the fixture has to be made
# writable again before it can be removed.
trap 'chmod -R u+w "$fixture" 2>/dev/null || true; rm -rf "$fixture"' EXIT

# A window wide enough that testify v1.12.0 (published 2026-06-10) and
# actions/checkout v7.0.1 (published 2026-07-20) both land inside it.
wide_window=315360000 # 10 years

failures=0

# new_repo lays out one fixture git repository holding the check under test.
new_repo() {
	local dir="$fixture/$1"
	mkdir -p "$dir/scripts"
	cp "$root/scripts/check-latest-deps.sh" "$dir/scripts/"
	git -C "$dir" init -q
	git -C "$dir" config user.email latest-deps@example.invalid
	git -C "$dir" config user.name 'latest deps fixture'
	printf '%s\n' "$dir"
}

commit_repo() {
	git -C "$1" add -A
	git -C "$1" commit -qm fixture
}

# run_check_with executes the check inside <dir> under <shell>, with the
# remaining arguments as environment assignments, capturing output and the real
# exit status. CI runs this check under both bash and zsh, so both are real
# invocations and both are exercised below — the two shells disagree about
# enough (regex captures, array expansion) that a bash-only test would let a
# zsh-only break through.
run_check_with() {
	local shell="$1" dir="$2"
	shift 2
	set +e
	CHECK_OUT="$(cd "$dir" && env "$@" "$shell" "$dir/scripts/check-latest-deps.sh" 2>&1)"
	CHECK_STATUS=$?
	set -e
}

run_check() {
	run_check_with bash "$@"
}

expect_status() {
	local want="$1" label="$2"
	if [[ "$CHECK_STATUS" != "$want" ]]; then
		echo "FAIL $label: expected exit $want, got $CHECK_STATUS" >&2
		printf '%s\n' "$CHECK_OUT" >&2
		failures=$((failures + 1))
	fi
}

expect_says() {
	local want="$1" label="$2"
	if [[ "$CHECK_OUT" != *"$want"* ]]; then
		echo "FAIL $label: expected output to mention '$want'" >&2
		printf '%s\n' "$CHECK_OUT" >&2
		failures=$((failures + 1))
	fi
}

expect_silent_about() {
	local unwanted="$1" label="$2"
	if [[ "$CHECK_OUT" == *"$unwanted"* ]]; then
		echo "FAIL $label: expected output NOT to mention '$unwanted'" >&2
		printf '%s\n' "$CHECK_OUT" >&2
		failures=$((failures + 1))
	fi
}

# --- Go modules ------------------------------------------------------------
# testify v1.11.1 is superseded by v1.12.0, which the module proxy records as
# published 2026-06-10 — comfortably older than a day, and the drift the real
# repository is red on today.
go_repo="$(new_repo go)"
cat >"$go_repo/go.mod" <<'GOMOD'
module example.invalid/latest-deps-fixture

go 1.25

require github.com/stretchr/testify v1.11.1
GOMOD
(cd "$go_repo" && GOWORK=off GOFLAGS=-mod=mod go mod download github.com/stretchr/testify >/dev/null)
commit_repo "$go_repo"

# A release older than the window, behind the pin: still drift, still red. This
# is the negative control for the whole idea — the quarantine must not have
# turned a failing gate green.
run_check "$go_repo" GOWORK=off
expect_status 1 'go: aged-out release is still drift'
expect_says 'FAIL' 'go: aged-out release is still drift'
expect_says 'github.com/stretchr/testify pinned v1.11.1 (latest adoptable v1.' \
	'go: aged-out release is still drift'

# The same release, the same pin, inside the window: held, explained, and green.
run_check "$go_repo" GOWORK=off "DEPS_ADOPTION_QUARANTINE_SECONDS=$wide_window"
expect_status 0 'go: release inside the window is held'
expect_says 'HELD' 'go: release inside the window is held'
expect_says 'v1.12.0 published 2026-06-10T14:10:43Z' 'go: release inside the window is held'
expect_says 'adoption quarantine' 'go: release inside the window is held'
expect_silent_about 'FAIL' 'go: release inside the window is held'

# Both directions again under zsh, the other shell CI runs this check with.
run_check_with zsh "$go_repo" GOWORK=off
expect_status 1 'go/zsh: aged-out release is still drift'
expect_says 'pinned v1.11.1 (latest adoptable v1.' 'go/zsh: aged-out release is still drift'

run_check_with zsh "$go_repo" GOWORK=off "DEPS_ADOPTION_QUARANTINE_SECONDS=$wide_window"
expect_status 0 'go/zsh: release inside the window is held'
expect_says 'v1.12.0 published 2026-06-10T14:10:43Z' 'go/zsh: release inside the window is held'
expect_silent_about 'FAIL' 'go/zsh: release inside the window is held'

# Negative control: the window is a floor, not a setting. Asking for a one
# second quarantine must not shorten it, and must not stop the drift above from
# failing.
run_check "$go_repo" GOWORK=off DEPS_ADOPTION_QUARANTINE_SECONDS=1
expect_status 1 'go: the window cannot be shortened'
expect_says 'adoption quarantine 86400s' 'go: the window cannot be shortened'
expect_says 'pinned v1.11.1 (latest adoptable v1.' 'go: the window cannot be shortened'

# Negative control: a window that is not a number is a configuration error, not
# a reason to run with no quarantine at all.
run_check "$go_repo" GOWORK=off DEPS_ADOPTION_QUARANTINE_SECONDS=soon
expect_status 2 'go: a nonsense window is rejected'
expect_says 'must be a whole number of seconds' 'go: a nonsense window is rejected'

# --- Go modules: publication time unavailable ------------------------------
# No public registry can be asked to withhold a publication time on demand, so
# this fixture is a module proxy of its own: real testify bytes fetched from
# proxy.golang.org, served over Go's real file:// proxy protocol, with one
# version's .info rewritten to the protocol-legal form that carries no Time.
# The Go toolchain renders that absence as the zero time rather than dropping
# the field, so a check that trusted it would read "published in year 1" and
# adopt instantly. It must fail loudly and name the version instead.
notime_repo="$(new_repo notime)"
proxy_dir="$fixture/notime-proxy/github.com/stretchr/testify/@v"
mkdir -p "$proxy_dir"
printf 'v1.11.1\nv1.12.0\n' >"$proxy_dir/list"
for artefact in v1.11.1.info v1.11.1.mod v1.12.0.mod; do
	curl -fsSL -o "$proxy_dir/$artefact" \
		"https://proxy.golang.org/github.com/stretchr/testify/@v/$artefact"
done
printf '{"Version":"v1.12.0"}' >"$proxy_dir/v1.12.0.info"
cat >"$notime_repo/go.mod" <<'GOMOD'
module example.invalid/latest-deps-notime-fixture

go 1.25

require github.com/stretchr/testify v1.11.1
GOMOD
cp "$go_repo/go.sum" "$notime_repo/go.sum"
commit_repo "$notime_repo"

# The fixture proxy is consulted first and the public proxy answers everything
# it does not carry, so only the one withheld timestamp differs from a normal
# run. A module cache of its own keeps the warm copy of the real .info on this
# machine from answering the question the fixture is asking.
notime_proxy="file://$fixture/notime-proxy,https://proxy.golang.org,direct"
notime_cache="$fixture/notime-gomodcache"
run_check "$notime_repo" GOWORK=off "GOPROXY=$notime_proxy" "GOMODCACHE=$notime_cache"
expect_status 1 'go: unknown publication time fails loudly'
expect_says 'github.com/stretchr/testify publication time for v1.12.0' \
	'go: unknown publication time fails loudly'
expect_says 'could not be determined' 'go: unknown publication time fails loudly'

# --- GitHub Actions --------------------------------------------------------
# actions/checkout v6.1.0 is superseded by v7.0.1, whose GitHub Release records
# published_at 2026-07-20 — again older than a day.
gha_repo="$(new_repo actions)"
mkdir -p "$gha_repo/.github/workflows"
cat >"$gha_repo/.github/workflows/pins.yml" <<'YAML'
name: pins
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 5
    steps:
      - uses: actions/checkout@v6.1.0
YAML
commit_repo "$gha_repo"

run_check "$gha_repo"
expect_status 1 'actions: aged-out tag is still drift'
expect_says 'actions/checkout pinned v6.1.0 (latest adoptable v' \
	'actions: aged-out tag is still drift'

run_check "$gha_repo" "DEPS_ADOPTION_QUARANTINE_SECONDS=$wide_window"
expect_status 0 'actions: tag inside the window is held'
expect_says 'HELD' 'actions: tag inside the window is held'
expect_says 'v7.0.1 published 2026-07-20T15:10:05Z' 'actions: tag inside the window is held'
expect_silent_about 'FAIL' 'actions: tag inside the window is held'

# Negative control: a credential the API rejects is indistinguishable from a
# throttled reply, and neither may be read as "no newer tag exists".
run_check "$gha_repo" GITHUB_TOKEN=ghp_000000000000000000000000000000000000 \
	GH_TOKEN=ghp_000000000000000000000000000000000000
expect_status 1 'actions: a rejected credential fails loudly'
expect_says 'tags could not be read' 'actions: a rejected credential fails loudly'

# --- Terraform providers ---------------------------------------------------
# hashicorp/null is a real registry provider with a short version history, so
# the wide-window case walks a handful of real published_at timestamps rather
# than the several hundred a provider like hashicorp/aws carries. Which major
# is current comes from the registry, not from this file, so the fixture stays
# true as the provider moves.
tf_provider=hashicorp/null
tf_latest="$(curl -fsSL "https://registry.terraform.io/v1/providers/$tf_provider" | jq -er '.version')"
tf_major="${tf_latest%%.*}"
if ! [[ "$tf_major" =~ ^[0-9]+$ ]]; then
	echo "FAIL terraform fixture: the registry did not report a version for $tf_provider" >&2
	exit 1
fi

tf_repo="$(new_repo terraform)"
write_tf() {
	cat >"$tf_repo/main.tf" <<TF
terraform {
  required_providers {
    null = {
      source  = "$tf_provider"
      version = "$1"
    }
  }
}
TF
}

# Negative control for the section itself: it reads required_providers wherever
# Terraform puts it, and a constraint a major behind the newest adoptable
# release is red. A Terraform section that matched no file at all would pass
# this fixture in silence and prove nothing.
write_tf "$((tf_major - 1)).0.0"
commit_repo "$tf_repo"
run_check "$tf_repo"
expect_status 1 'terraform: a stale major is drift'
expect_says "constraint $((tf_major - 1)).0.0 vs latest adoptable" 'terraform: a stale major is drift'

write_tf "${tf_major}.0.0"
run_check "$tf_repo"
expect_status 0 'terraform: a current major is clean'
expect_silent_about 'FAIL' 'terraform: a current major is clean'

run_check "$tf_repo" "DEPS_ADOPTION_QUARANTINE_SECONDS=$wide_window"
expect_status 0 'terraform: releases inside the window are held'
expect_says 'HELD' 'terraform: releases inside the window are held'
expect_says "$tf_provider" 'terraform: releases inside the window are held'
expect_silent_about 'FAIL' 'terraform: releases inside the window are held'

if ((failures > 0)); then
	echo "$failures adoption quarantine test(s) failed" >&2
	exit 1
fi
echo 'dependency adoption quarantine tests passed'
