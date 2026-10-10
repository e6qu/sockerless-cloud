#!/usr/bin/env bash
# Tests for check-latest-deps.sh's adoption quarantine — the rule that a
# dependency must have been public for at least a day before this project
# adopts it — in both directions, plus the negative controls that prove the
# gate still fails on everything it failed on before.
#
# The registries the check reads are fixtures this test serves: a Go module
# proxy over Go's file:// proxy protocol, and the Terraform registry's provider
# documents and the GitHub API's tag, ref, commit and release documents over a
# local HTTP server. Every publication time the check compares is one written
# here, so a public registry gaining a release, or listing a version before its
# publication time is readable, cannot move a verdict. The check reaches the
# fixtures through the same coordinates it reaches the public registries
# through — GOPROXY, DEPS_TERRAFORM_REGISTRY_URL and DEPS_GITHUB_API_URL — and
# the module files the fixture proxy does not carry (go.mod files and source
# archives, which the checksum database pins) still come from proxy.golang.org.
#
# The quarantine window is widened, never narrowed, to put a known-old release
# inside it: check-latest-deps.sh clamps DEPS_ADOPTION_QUARANTINE_SECONDS up to
# its floor, so no test — and no CI environment — can weaken the gate, and one
# of the cases below proves that clamp holds.
set -euo pipefail

# Running as a pre-commit hook, this script inherits GIT_DIR and
# GIT_INDEX_FILE pointing at the real repository's git-dir and index.
# new_repo() below does its own `git init` in a throwaway fixture directory,
# and with those variables still set, every one of its nested git commands
# operates on the real repository instead of the fresh fixture one --
# corrupting the real index with the fixture's tiny, unrelated tree instead
# of ever touching the fixture's own .git. Only a real `git commit` sets
# these, so `pre-commit run` and running this script directly both hide the
# bug; unset them before any fixture repo exists.
unset -v GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_COMMON_DIR GIT_PREFIX

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
fixture="$(mktemp -d)"
background_pids=()
cleanup() {
	if ((${#background_pids[@]} > 0)); then
		kill "${background_pids[@]}" 2>/dev/null || true
	fi
	# Go's module cache is written read-only, so the fixture has to be made
	# writable again before it can be removed.
	chmod -R u+w "$fixture" 2>/dev/null || true
	rm -rf "$fixture"
}
trap cleanup EXIT

# A window wide enough that every fixed publication time below lands inside it.
wide_window=315360000 # 10 years

failures=0

# rfc3339_ago prints the UTC time <seconds> before now, for a release the test
# needs inside the default one-day window whenever it runs.
rfc3339_ago() {
	python3 -c 'import datetime, sys
t = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(seconds=int(sys.argv[1]))
print(t.strftime("%Y-%m-%dT%H:%M:%SZ"))' "$1"
}

# --- Fixture registries ----------------------------------------------------
# One HTTP server answers for the Terraform registry under /terraform... and
# for the GitHub API under /github. It serves the file at the request path,
# or the directory's .index file when the path names a directory (a provider's
# version index sits at the same path its version documents sit under), and
# 404s anything absent, as the registries do. Like the GitHub API, it answers
# an unauthenticated request and refuses a credential it does not know with
# 401 Bad credentials.
registry_root="$fixture/registry"
fixture_token=latest-deps-fixture-token
registry_port_file="$fixture/registry-port"
mkdir -p "$registry_root"
python3 - "$registry_root" "$fixture_token" "$registry_port_file" <<'PY' &
import http.server, os, sys

root, token, port_file = sys.argv[1:4]


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        auth = self.headers.get("Authorization")
        if auth is not None and auth != "Bearer " + token:
            return self.reply(401, b'{"message":"Bad credentials"}')
        path = os.path.normpath(os.path.join(root, self.path.split("?", 1)[0].lstrip("/")))
        if not path.startswith(root + os.sep):
            return self.reply(404, b'{"message":"Not Found"}')
        if os.path.isdir(path):
            path = os.path.join(path, ".index")
        try:
            with open(path, "rb") as f:
                body = f.read()
        except (FileNotFoundError, NotADirectoryError):
            return self.reply(404, b'{"message":"Not Found"}')
        self.reply(200, body)

    def reply(self, code, body):
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
with open(port_file + ".tmp", "w") as f:
    f.write(str(server.server_address[1]))
os.rename(port_file + ".tmp", port_file)
server.serve_forever()
PY
background_pids+=($!)
until [[ -s "$registry_port_file" ]]; do sleep 0.1; done
registry_url="http://127.0.0.1:$(cat "$registry_port_file")"

# fixture_doc writes one registry document: <path under the registry root>
# <body>.
fixture_doc() {
	mkdir -p "$(dirname "$registry_root/$1")"
	printf '%s\n' "$2" >"$registry_root/$1"
}

# The fixture module proxy carries testify's version list and publication
# records, at the times proxy.golang.org records for them.
goproxy_dir="$fixture/goproxy/github.com/stretchr/testify/@v"
mkdir -p "$goproxy_dir"
printf 'v1.11.0\nv1.11.1\nv1.12.0\n' >"$goproxy_dir/list"
printf '{"Version":"v1.11.0","Time":"2025-08-17T15:57:51Z"}' >"$goproxy_dir/v1.11.0.info"
printf '{"Version":"v1.11.1","Time":"2025-08-27T10:46:31Z"}' >"$goproxy_dir/v1.11.1.info"
printf '{"Version":"v1.12.0","Time":"2026-06-10T14:10:43Z"}' >"$goproxy_dir/v1.12.0.info"
goproxy="file://$fixture/goproxy,https://proxy.golang.org"

# Every run reads the fixture registries. A case that needs another registry
# names it in its own arguments, which env applies after these.
registry_env=(
	"GOPROXY=$goproxy"
	"DEPS_TERRAFORM_REGISTRY_URL=$registry_url/terraform"
	"DEPS_GITHUB_API_URL=$registry_url/github"
	"GITHUB_TOKEN=$fixture_token"
	"GH_TOKEN=$fixture_token"
)

# new_repo lays out one fixture git repository holding the check under test.
new_repo() {
	local dir="$fixture/$1"
	mkdir -p "$dir/scripts/lib"
	cp "$root/scripts/check-latest-deps.sh" "$dir/scripts/"
	# The check sources the rate-limit protocol, so the fixture copy needs it
	# too — a fixture missing it would fail every case for the wrong reason.
	cp "$root/scripts/lib/github-throttle.sh" "$dir/scripts/lib/"
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
	CHECK_OUT="$(cd "$dir" && env "${registry_env[@]}" "$@" "$shell" "$dir/scripts/check-latest-deps.sh" 2>&1)"
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
# testify v1.11.1 is superseded by v1.12.0, published 2026-06-10 — comfortably
# older than a day.
go_repo="$(new_repo go)"
cat >"$go_repo/go.mod" <<'GOMOD'
module example.invalid/latest-deps-fixture

go 1.25

require github.com/stretchr/testify v1.11.1
GOMOD
(cd "$go_repo" && GOWORK=off GOFLAGS=-mod=mod GOPROXY="$goproxy" go mod download github.com/stretchr/testify >/dev/null)
commit_repo "$go_repo"

# A release older than the window, behind the pin: still drift, still red. This
# is the negative control for the whole idea — the quarantine must not have
# turned a failing gate green.
run_check "$go_repo" GOWORK=off
expect_status 1 'go: aged-out release is still drift'
expect_says 'FAIL' 'go: aged-out release is still drift'
expect_says 'github.com/stretchr/testify pinned v1.11.1 (latest adoptable v1.12.0)' \
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
expect_says 'pinned v1.11.1 (latest adoptable v1.12.0)' 'go/zsh: aged-out release is still drift'

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
expect_says 'pinned v1.11.1 (latest adoptable v1.12.0)' 'go: the window cannot be shortened'

# Negative control: a window that is not a number is a configuration error, not
# a reason to run with no quarantine at all.
run_check "$go_repo" GOWORK=off DEPS_ADOPTION_QUARANTINE_SECONDS=soon
expect_status 2 'go: a nonsense window is rejected'
expect_says 'must be a whole number of seconds' 'go: a nonsense window is rejected'

# --- Go modules: publication time unavailable ------------------------------
# A second fixture proxy carries v1.12.0's .info in the protocol-legal form
# that has no Time. The Go toolchain renders that absence as the zero time
# rather than dropping the field, so a check that trusted it would read
# "published in year 1" and adopt instantly. It must fail loudly and name the
# version instead.
notime_repo="$(new_repo notime)"
proxy_dir="$fixture/notime-proxy/github.com/stretchr/testify/@v"
mkdir -p "$proxy_dir"
printf 'v1.11.1\nv1.12.0\n' >"$proxy_dir/list"
cp "$goproxy_dir/v1.11.1.info" "$proxy_dir/"
printf '{"Version":"v1.12.0"}' >"$proxy_dir/v1.12.0.info"
cat >"$notime_repo/go.mod" <<'GOMOD'
module example.invalid/latest-deps-notime-fixture

go 1.25

require github.com/stretchr/testify v1.11.1
GOMOD
cp "$go_repo/go.sum" "$notime_repo/go.sum"
commit_repo "$notime_repo"

# A module cache of its own keeps the copy of the fixture's real .info the
# cases above cached from answering the question this fixture is asking.
notime_proxy="file://$fixture/notime-proxy,https://proxy.golang.org"
notime_cache="$fixture/notime-gomodcache"
run_check "$notime_repo" GOWORK=off "GOPROXY=$notime_proxy" "GOMODCACHE=$notime_cache"
expect_status 1 'go: unknown publication time fails loudly'
expect_says 'github.com/stretchr/testify publication time for v1.12.0' \
	'go: unknown publication time fails loudly'
expect_says 'could not be determined' 'go: unknown publication time fails loudly'

# --- Go modules: a proxy that never answers ---------------------------------
# A module proxy that accepts a connection and then says nothing is what held a
# CI run until its job was cancelled: the Go toolchain gives the request no
# deadline. The check must give up on it, and fail naming what it could not
# learn, rather than wait forever or read the silence as "no newer versions".
# The listener is a real TCP socket that accepts and never replies, and the
# deadline is shortened so three stalled attempts take seconds; both shells CI
# runs the check under are held to it.
stall_port_file="$fixture/stall-port"
python3 - "$stall_port_file" <<'PY' &
import os, socket, sys
listener = socket.socket()
listener.bind(("127.0.0.1", 0))
listener.listen()
with open(sys.argv[1] + ".tmp", "w") as f:
    f.write(str(listener.getsockname()[1]))
os.rename(sys.argv[1] + ".tmp", sys.argv[1])
held = []
while True:
    connection, _ = listener.accept()
    held.append(connection)
PY
background_pids+=($!)
until [[ -s "$stall_port_file" ]]; do sleep 0.1; done
stall_proxy="http://127.0.0.1:$(cat "$stall_port_file")"
for shell in bash zsh; do
	run_check_with "$shell" "$go_repo" GOWORK=off "GOPROXY=$stall_proxy" \
		"GOMODCACHE=$fixture/stall-gomodcache-$shell" DEPS_GO_PROXY_DEADLINE_SECONDS=2
	expect_status 1 "go ($shell): a proxy that never answers fails the run"
	expect_says 'stalled for 2s (attempt 3 of 3)' "go ($shell): a proxy that never answers fails the run"
	expect_says 'The module proxy never answered' "go ($shell): a proxy that never answers fails the run"
done

run_check "$go_repo" GOWORK=off DEPS_GO_PROXY_DEADLINE_SECONDS=0
expect_status 2 'go: a zero proxy deadline is rejected'

# --- GitHub Actions --------------------------------------------------------
# example/action is pinned at v6.1.0, and each newer tag reaches its
# publication time by a different road the check walks: v7.0.1 through its
# GitHub Release, v7.0.0 (no release) through its annotated tag's tagger date,
# and v6.2.0 (no release, a lightweight tag) through its commit's committer
# date.
gh_repo_api=github/repos/example/action
fixture_doc "$gh_repo_api/tags" '[
  {"name": "v7.0.1", "commit": {"sha": "7010000000000000000000000000000000000000"}},
  {"name": "v7.0.0", "commit": {"sha": "7000000000000000000000000000000000000000"}},
  {"name": "v6.2.0", "commit": {"sha": "6200000000000000000000000000000000000000"}},
  {"name": "v6.1.0", "commit": {"sha": "6100000000000000000000000000000000000000"}}
]'
fixture_doc "$gh_repo_api/releases/tags/v7.0.1" \
	'{"tag_name": "v7.0.1", "draft": false, "published_at": "2026-07-20T15:10:05Z"}'
fixture_doc "$gh_repo_api/git/ref/tags/v7.0.0" \
	'{"ref": "refs/tags/v7.0.0", "object": {"type": "tag", "sha": "a700000000000000000000000000000000000000"}}'
fixture_doc "$gh_repo_api/git/tags/a700000000000000000000000000000000000000" \
	'{"tag": "v7.0.0", "tagger": {"name": "fixture", "date": "2026-07-14T09:30:00Z"}}'
fixture_doc "$gh_repo_api/git/ref/tags/v6.2.0" \
	'{"ref": "refs/tags/v6.2.0", "object": {"type": "commit", "sha": "6200000000000000000000000000000000000000"}}'
fixture_doc "$gh_repo_api/commits/6200000000000000000000000000000000000000" \
	'{"sha": "6200000000000000000000000000000000000000", "commit": {"committer": {"name": "fixture", "date": "2026-05-02T11:00:00Z"}}}'

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
      - uses: example/action@v6.1.0
YAML
commit_repo "$gha_repo"

run_check "$gha_repo"
expect_status 1 'actions: aged-out tag is still drift'
expect_says 'example/action pinned v6.1.0 (latest adoptable v7.0.1)' \
	'actions: aged-out tag is still drift'

run_check "$gha_repo" "DEPS_ADOPTION_QUARANTINE_SECONDS=$wide_window"
expect_status 0 'actions: tags inside the window are held'
expect_says 'HELD' 'actions: tags inside the window are held'
expect_says 'v7.0.1 published 2026-07-20T15:10:05Z' 'actions: a release dates its tag'
expect_says 'v7.0.0 published 2026-07-14T09:30:00Z' 'actions: an annotated tag dates itself'
expect_says 'v6.2.0 published 2026-05-02T11:00:00Z' 'actions: a lightweight tag is dated by its commit'
expect_silent_about 'FAIL' 'actions: tags inside the window are held'

# Negative control: a credential the API rejects is indistinguishable from a
# throttled reply, and neither may be read as "no newer tag exists".
run_check "$gha_repo" GITHUB_TOKEN=ghp_000000000000000000000000000000000000 \
	GH_TOKEN=ghp_000000000000000000000000000000000000
expect_status 1 'actions: a rejected credential fails loudly'
expect_says 'tags could not be read' 'actions: a rejected credential fails loudly'

# --- Terraform providers ---------------------------------------------------
# example/null has one release published an hour before the test runs, so the
# newest release is inside the default window and the newest adoptable one is
# the release below it — the state the registry is in for a day after every
# upstream release.
tf_provider=example/null
tf_fresh="$(rfc3339_ago 3600)"
tf_index="terraform/v1/providers/$tf_provider"
fixture_doc "$tf_index/.index" \
	'{"namespace": "example", "name": "null", "version": "3.3.3", "versions": ["2.1.2", "3.2.3", "3.2.4", "3.3.2", "3.3.3"]}'
fixture_doc "$tf_index/2.1.2" '{"version": "2.1.2", "published_at": "2020-11-30T16:23:46Z"}'
fixture_doc "$tf_index/3.2.3" '{"version": "3.2.3", "published_at": "2024-09-11T19:39:45Z"}'
fixture_doc "$tf_index/3.2.4" '{"version": "3.2.4", "published_at": "2025-04-22T13:25:13Z"}'
fixture_doc "$tf_index/3.3.2" '{"version": "3.3.2", "published_at": "2026-09-10T16:04:21Z"}'
fixture_doc "$tf_index/3.3.3" "{\"version\": \"3.3.3\", \"published_at\": \"$tf_fresh\"}"

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
# Terraform puts it, and a range constraint a major behind the newest adoptable
# release is red. A Terraform section that matched no file at all would pass
# this fixture in silence and prove nothing.
write_tf '~> 2.0'
commit_repo "$tf_repo"
run_check "$tf_repo"
expect_status 1 'terraform: a stale major is drift'
expect_says 'constraint ~> 2.0 vs latest adoptable 3.3.2' 'terraform: a stale major is drift'

# A range constraint admits newer releases by itself, so only its major has to
# keep up; the release inside the window is reported as held.
write_tf '~> 3.0'
run_check "$tf_repo"
expect_status 0 'terraform: a current major range is clean'
expect_says "3.3.3 published $tf_fresh" 'terraform: a current major range is clean'
expect_silent_about 'FAIL' 'terraform: a current major range is clean'

# An exact pin is held to the exact newest adoptable release: one release back
# is drift even inside the current major.
write_tf 3.2.4
run_check "$tf_repo"
expect_status 1 'terraform: an exact pin one release behind is drift'
expect_says 'pinned at 3.2.4 vs latest adoptable 3.3.2' 'terraform: an exact pin one release behind is drift'

# Every entry of a required_providers block is checked, not only its last: a
# stale provider listed before a current one is still drift.
cat >"$tf_repo/main.tf" <<TF
terraform {
  required_providers {
    stale = {
      source  = "$tf_provider"
      version = "3.2.4"
    }
    current = {
      source  = "$tf_provider"
      version = "3.3.2"
    }
  }
}
TF
run_check "$tf_repo"
expect_status 1 'terraform: a stale provider before a current one is drift'
expect_says 'stale (example/null) pinned at 3.2.4 vs latest adoptable 3.3.2' \
	'terraform: a stale provider before a current one is drift'

# The newest adoptable release is not the newest published one: pinning the
# release below a quarantined one is current, and the newer one is held.
write_tf 3.3.2
run_check "$tf_repo"
expect_status 0 'terraform: an exact pin at the newest adoptable release is clean'
expect_says 'pinned at 3.3.2: 3.3.3 published' 'terraform: an exact pin at the newest adoptable release is clean'
expect_silent_about 'FAIL' 'terraform: an exact pin at the newest adoptable release is clean'

# A pin newer than anything adoptable is the state a freshly bumped pin is in
# for its first day. That is held, not a demand to downgrade.
write_tf 3.3.3
run_check "$tf_repo"
expect_status 0 'terraform: a pin inside the window is held'
expect_says 'pinned at 3.3.3, newer than the latest adoptable 3.3.2' 'terraform: a pin inside the window is held'
expect_silent_about 'FAIL' 'terraform: a pin inside the window is held'

run_check "$tf_repo" "DEPS_ADOPTION_QUARANTINE_SECONDS=$wide_window"
expect_status 0 'terraform: releases inside the window are held'
expect_says 'HELD' 'terraform: releases inside the window are held'
expect_says "$tf_provider" 'terraform: releases inside the window are held'
expect_silent_about 'FAIL' 'terraform: releases inside the window are held'

write_tf '~> 3.0'
run_check "$tf_repo" "DEPS_ADOPTION_QUARANTINE_SECONDS=$wide_window"
expect_status 0 'terraform: a range constraint inside the window is held'
expect_says 'HELD' 'terraform: a range constraint inside the window is held'
expect_silent_about 'FAIL' 'terraform: a range constraint inside the window is held'

# The registry can list a version in the provider index before that version's
# document carries published_at. A version of unknown age is never adopted, so
# that is a failure naming the version, not a release to skip or to take.
cp -R "$registry_root/terraform" "$registry_root/terraform-unpublished"
tf_unpublished="terraform-unpublished/v1/providers/$tf_provider"
fixture_doc "$tf_unpublished/.index" \
	'{"namespace": "example", "name": "null", "version": "3.3.4", "versions": ["2.1.2", "3.2.3", "3.2.4", "3.3.2", "3.3.3", "3.3.4"]}'
fixture_doc "$tf_unpublished/3.3.4" '{"version": "3.3.4"}'
run_check "$tf_repo" "DEPS_TERRAFORM_REGISTRY_URL=$registry_url/terraform-unpublished"
expect_status 1 'terraform: a version without a publication time fails loudly'
expect_says "$tf_provider) publication time for 3.3.4 could not be determined from the Terraform registry" \
	'terraform: a version without a publication time fails loudly'

# --- The GitHub rate-limit protocol ----------------------------------------
# A throttle is not something a test can ask GitHub for, so the decision the
# protocol makes is exercised against the headers a throttled reply carries.
# What matters is that it waits only when told to and never guesses: the
# alternative to this discipline is either a branch turning red because someone
# else exhausted a quota, or a sleep invented out of a refusal that will never
# clear.
# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/github-throttle.sh
source "$root/scripts/lib/github-throttle.sh"

throttle_headers="$fixture/throttle-headers.txt"
throttle_now=1786000000

expect_wait() {
	local want=$1 label=$2 got
	if ! got=$(gh_throttle_wait "$throttle_headers" "$throttle_now"); then
		echo "FAIL $label: expected a wait of ${want}s, got a refusal to wait" >&2
		failures=$((failures + 1))
		return
	fi
	if [[ "$got" != "$want" ]]; then
		echo "FAIL $label: expected a wait of ${want}s, got ${got}s" >&2
		failures=$((failures + 1))
	fi
}

expect_no_wait() {
	local label=$1 got
	if got=$(gh_throttle_wait "$throttle_headers" "$throttle_now"); then
		echo "FAIL $label: expected no wait, got ${got}s" >&2
		failures=$((failures + 1))
	fi
}

# Retry-After is the reply saying exactly when to come back, and it wins over
# everything else. Ten per cent and a second are added because the two clocks
# are not the same one.
printf 'HTTP/2 429\r\nretry-after: 30\r\n\r\n' >"$throttle_headers"
expect_wait 34 'throttle: Retry-After is honoured'

# A spent quota is the other documented form: the reset is an absolute time, so
# the wait is what remains of it.
printf 'HTTP/2 403\r\nx-ratelimit-remaining: 0\r\nx-ratelimit-reset: %s\r\n\r\n' \
	"$((throttle_now + 20))" >"$throttle_headers"
expect_wait 23 'throttle: a spent quota waits out its reset'

# Negative control, and the one that matters most: a refusal with quota left is
# not a throttle. Waiting on it would turn a permanent error — a credential
# without the scope, a repository that cannot be read — into a slow one.
printf 'HTTP/2 403\r\nx-ratelimit-remaining: 4993\r\nx-ratelimit-reset: %s\r\n\r\n' \
	"$((throttle_now + 20))" >"$throttle_headers"
expect_no_wait 'throttle: a refusal with quota left is not waited on'

# A reply carrying no rate-limit signal at all says nothing about when to
# return, so there is nothing to honour.
printf 'HTTP/2 403\r\n\r\n' >"$throttle_headers"
expect_no_wait 'throttle: a refusal with no rate-limit signal is not waited on'

# A quota that resets beyond the cap is a quota problem to report, not to sit
# on.
printf 'HTTP/2 403\r\nx-ratelimit-remaining: 0\r\nx-ratelimit-reset: %s\r\n\r\n' \
	"$((throttle_now + 3600))" >"$throttle_headers"
expect_no_wait 'throttle: a reset beyond the cap is reported rather than waited out'

# A reset already in the past still yields once rather than returning a
# negative sleep.
printf 'HTTP/2 403\r\nx-ratelimit-remaining: 0\r\nx-ratelimit-reset: %s\r\n\r\n' \
	"$((throttle_now - 5))" >"$throttle_headers"
expect_wait 1 'throttle: a reset already past yields once'

if ((failures > 0)); then
	echo "$failures adoption quarantine test(s) failed" >&2
	exit 1
fi
echo 'dependency adoption quarantine tests passed'
