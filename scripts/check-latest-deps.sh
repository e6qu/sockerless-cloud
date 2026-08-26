#!/usr/bin/env bash
# check-latest-deps.sh — fail loud if any dependency is behind the newest
# published version that has cleared the adoption quarantine. Runs as a
# pre-commit hook + CI job.
#
# Scope:
#   1. Go modules across every go.mod — for each direct require, list the
#      published versions and compare the pin against the newest one that has
#      cleared the quarantine. ANY drift fails (no warn tier — operator runs
#      `make upgrade-deps` to bring everything current).
#   2. Terraform providers across every required_providers block — check the
#      version constraint against the newest registry version that has cleared
#      the quarantine. Any drift fails.
#   3. GitHub Actions in .github/workflows — every owner/repo action must be
#      pinned to the newest published semantic version tag that has cleared the
#      quarantine.
#
# Exit code: 0 only when every direct dependency is on the newest adoptable
# version. 1 on any drift, and on any dependency whose publication time cannot
# be established — an unknown age is never a licence to adopt.

set -euo pipefail

# --- Adoption quarantine ---------------------------------------------------
#
# Dependencies are kept current AND kept at arm's length for their first day in
# the world. A release published minutes ago has had no time to be noticed,
# yanked, or flagged: a compromised, hijacked, or accidentally-broken version is
# at its most dangerous in the hours right after publication, when nobody has
# looked at it yet. This project should not be the one that finds out.
#
# So "latest" here does not mean "the newest version that exists"; it means "the
# newest version that has been public for at least this long". A newer version
# inside the window is reported as HELD — visible, explained, and adopted by a
# later run once it has aged out — never as drift, and never a failure.
#
# The window is a whole day rather than an hour because that is the timescale on
# which the mitigation actually operates: registry yanks, advisory publication,
# and "we just shipped a broken tag" reports land in hours, not minutes. Do not
# shorten this, and do not delete it to turn a red gate green — the delay IS the
# mitigation, and a gate that adopts a release the instant it appears provides
# none of it.
#
# This reasoning is specific to code this repository installs and executes. It
# is deliberately NOT applied to the vendored cloud specifications checked by
# scripts/check-spec-freshness.sh: those documents are inert reference data
# validated by our own suites, never executed, so there is no window in which a
# bad one can hurt us — and delaying their pins would only hide rot from the
# scheduled freshness run whose whole job is to catch it.
readonly ADOPTION_QUARANTINE_FLOOR_SECONDS=86400 # 24 hours

# The effective window may be LENGTHENED (a stricter local policy, or a test
# that needs a known-old release to land inside the window) but never
# shortened: the floor above is clamped in, so no environment can weaken the
# gate by setting this small.
quarantine_seconds=${DEPS_ADOPTION_QUARANTINE_SECONDS:-$ADOPTION_QUARANTINE_FLOOR_SECONDS}
if ! [[ $quarantine_seconds =~ ^[0-9]+$ ]]; then
  echo "ERROR: DEPS_ADOPTION_QUARANTINE_SECONDS must be a whole number of seconds, got '$quarantine_seconds'" >&2
  exit 2
fi
if ((quarantine_seconds < ADOPTION_QUARANTINE_FLOOR_SECONDS)); then
  quarantine_seconds=$ADOPTION_QUARANTINE_FLOOR_SECONDS
fi
readonly quarantine_seconds

# --- Baseline attribution --------------------------------------------------
#
# Usage: scripts/check-latest-deps.sh [--baseline <ref>]
#
# Without --baseline every drift fails. That is the form the scheduled run on
# main uses, and it is what keeps the dependencies from rotting.
#
# --baseline <ref> attributes each drift before failing on it, the same way
# scripts/check-spec-freshness.sh already does for the vendored specifications.
# Upstream publishes continuously and independently of any branch, and the
# quarantine guarantees a steady drip: a version becomes drift exactly 86400s
# after it appears, whether or not anybody pushed. On main over one day that
# turned five of six consecutive runs red on dependencies nothing had touched
# --- docker/setup-buildx-action v4.3.0, google.golang.org/grpc v1.83.1,
# aws-sdk-go-v2/service/organizations v1.54.0 --- and a permanently red main is
# not a gate, it is noise that hides the run which matters.
#
# With a baseline, a pin byte-identical to the baseline's carries drift the
# baseline already carried: upstream moved under the branch rather than the
# branch letting the pin rot. That is reported as INHERITED without failing. A
# pin the branch changed, rolled back, or added is the branch's own and is
# still held to the newest adoptable version, so a branch can never leave
# dependency freshness worse than the base it started from.
#
# Inherited drift is relocated, not forgiven: the scheduled run carries no
# baseline and fails on it there.
BASELINE=""
if [ "${1:-}" = "--baseline" ]; then
  BASELINE="${2:?--baseline needs a git ref}"
  shift 2
fi

if ! REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null); then
  script_path=$0
  case "$script_path" in
    */*) ;;
    *) script_path="./$script_path" ;;
  esac
  REPO_ROOT="$(cd "$(dirname "$script_path")/.." && pwd)"
fi
cd "$REPO_ROOT"

if [ -n "$BASELINE" ] && ! git rev-parse --verify --quiet "$BASELINE^{commit}" >/dev/null; then
  echo "ERROR: baseline ref '$BASELINE' is not available in $REPO_ROOT; fetch it before running this check" >&2
  exit 2
fi

for tool in go curl jq; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "ERROR: $tool not on PATH" >&2
    exit 1
  }
done

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

now_epoch=$(date -u +%s)
readonly now_epoch

fail=0
held=0
inherited=0

# rfc3339_epoch converts an RFC 3339 UTC timestamp (2026-06-10T14:10:43Z, with
# optional fractional seconds) to seconds since the Unix epoch, and fails on
# anything it cannot read exactly. Two portability constraints shape it. date(1)
# parses timestamps with incompatible flags on GNU and BSD, so the conversion is
# arithmetic (Howard Hinnant's days_from_civil) rather than a shell-out; and CI
# runs this script under both bash and zsh, which expose regex captures through
# different variables, so the fields come out of sed rather than a `=~` match.
# Only UTC is accepted: every registry this script talks to stamps Z, and
# silently misreading an offset would move a release in or out of the window.
rfc3339_epoch() {
  local ts=$1
  local fields y m d hh mm ss era yoe doy doe days
  fields=$(printf '%s\n' "$ts" | sed -n -E \
    's/^([0-9]{4})-([0-9]{2})-([0-9]{2})[Tt]([0-9]{2}):([0-9]{2}):([0-9]{2})(\.[0-9]+)?([Zz]|\+00:00)$/\1 \2 \3 \4 \5 \6/p')
  [ -n "$fields" ] || return 1
  read -r y m d hh mm ss <<<"$fields"
  y=$((10#$y))
  m=$((10#$m))
  d=$((10#$d))
  hh=$((10#$hh))
  mm=$((10#$mm))
  ss=$((10#$ss))
  if ((m <= 2)); then y=$((y - 1)); fi
  era=$(((y >= 0 ? y : y - 399) / 400))
  yoe=$((y - era * 400))
  doy=$(((153 * (m + (m > 2 ? -3 : 9)) + 2) / 5 + d - 1))
  doe=$((yoe * 365 + yoe / 4 - yoe / 100 + doy))
  days=$((era * 146097 + doe - 719468))
  printf '%s\n' "$((days * 86400 + hh * 3600 + mm * 60 + ss))"
}

# describe_age renders a publication timestamp as a human-sized age, so a HELD
# line says how much longer the version has to wait.
describe_age() {
  local age=$1
  if ((age < 3600)); then
    printf '%dm\n' "$((age / 60))"
  else
    printf '%dh%dm\n' "$((age / 3600))" "$(((age % 3600) / 60))"
  fi
}

# publish_time prints the RFC 3339 publication time of one version of
# LOOKUP_SUBJECT, and prints nothing when the registry will not say. Each
# dependency class answers the question from its own registry's real
# publication metadata; the three lookups are defined with their sections
# below.
publish_time() {
  case $LOOKUP_CLASS in
    go) go_module_publish_time "$1" ;;
    terraform) tf_provider_publish_time "$1" ;;
    github) gh_tag_publish_time "$1" ;;
    *)
      echo "ERROR: no publication-time source for dependency class '$LOOKUP_CLASS'" >&2
      return 1
      ;;
  esac
}

# adoptable_version walks the published versions of LOOKUP_SUBJECT (a
# LOOKUP_CLASS dependency) from the newest down and reports the newest one that
# has cleared the quarantine.
#
#   $1  the currently pinned version; the walk stops there, so a dependency
#       already on the newest version costs no publication-time lookups at all
#   $2  newline-separated published versions, in any order
#
# Sets ADOPTABLE to the newest adoptable version ("" when every version newer
# than the pin is still inside the window) and HELD_NOTE to a description of
# the versions it skipped for being too young. Returns non-zero, with
# UNREADABLE naming the version, when a publication time cannot be established.
adoptable_version() {
  local pinned=$1 versions=$2
  local v ts age
  ADOPTABLE=""
  HELD_NOTE=""
  UNREADABLE=""
  while IFS= read -r v; do
    [[ -z $v ]] && continue
    if [[ $v == "$pinned" ]]; then
      ADOPTABLE=$pinned
      return 0
    fi
    ts=$(publish_time "$v" || true)
    if [[ -z $ts ]]; then
      UNREADABLE="$v"
      return 1
    fi
    if ! age=$(rfc3339_epoch "$ts"); then
      UNREADABLE="$v (publication time \"$ts\" is not an RFC 3339 UTC timestamp)"
      return 1
    fi
    # A registry that has no publication record for a version can still answer
    # with a well-formed timestamp: the Go toolchain renders a module proxy
    # .info without a Time field as the zero time, 0001-01-01T00:00:00Z. Left
    # alone that reads as "published two millennia ago" and clears the
    # quarantine instantly — the exact silent adoption this gate exists to stop.
    # Nothing these registries serve predates the epoch, so treat it as no
    # answer at all.
    if ((age <= 0)); then
      UNREADABLE="$v (registry reported no publication time, only the placeholder \"$ts\")"
      return 1
    fi
    age=$((now_epoch - age))
    if ((age >= quarantine_seconds)); then
      ADOPTABLE=$v
      return 0
    fi
    HELD_NOTE="${HELD_NOTE:+$HELD_NOTE, }$v published $ts ($(describe_age "$age") ago)"
  done < <(printf '%s\n' "$versions" | sort -rV)
  return 0
}

# baseline_holds_pin answers whether the baseline ref records the same pin for
# this dependency, which is what makes the drift the baseline's rather than the
# branch's.
#
#   $1 file the pin lives in (repo-relative)  $2 dependency token  $3 pin
#
# The dependency token and the pin share a line in both formats that actually
# churn -- `module/path v1.2.3` in a go.mod require, and
# `uses: owner/repo@v1.2.3` in a workflow -- so a line in the baseline's copy
# of the file carrying both is the baseline recording that pin. A Terraform
# constraint spans two lines of a required_providers block, so those pass the
# file itself as the token and fall back to "the branch did not touch this
# file at all", which cannot wrongly forgive a branch that edited a pin.
#
# Absence is not inheritance: a file the baseline does not have is a file the
# branch added, and its pins are the branch's own.
baseline_holds_pin() {
  local file=$1 token=$2 pinned=$3 baseline_copy
  [ -n "$BASELINE" ] || return 1
  baseline_copy=$(git show "$BASELINE:$file" 2>/dev/null) || return 1
  if [ "$token" = "$file" ]; then
    [ "$baseline_copy" = "$(cat "$file" 2>/dev/null)" ]
    return
  fi
  printf '%s\n' "$baseline_copy" |
    grep -F -- "$token" |
    grep -qE "(^|[^0-9A-Za-z._-])${pinned//./\\.}([^0-9A-Za-z._-]|\$)"
}

# report_version_state turns one resolved dependency into a line of output: a
# FAIL for real drift, an INHERITED for drift the baseline already carried, a
# HELD for a newer version still inside the quarantine, and silence when the
# pin is exactly what should be adopted.
#   $1 label for the dependency  $2 pinned version
#   $3 file the pin lives in (optional)  $4 dependency token (optional)
report_version_state() {
  local label=$1 pinned=$2 file=${3:-} token=${4:-}
  if [[ -n $ADOPTABLE && $ADOPTABLE != "$pinned" ]]; then
    if [[ -n $file ]] && baseline_holds_pin "$file" "${token:-$file}" "$pinned"; then
      echo "  INHERITED  $label pinned $pinned (latest adoptable $ADOPTABLE)"
      echo "      pin unchanged from $BASELINE; the scheduled dependency freshness run holds this one"
      # Not failing the branch is not the same as saying nothing. Inherited
      # drift printed only into a passing job's log is drift nobody reads, and
      # the scheduled run fails somewhere that belongs to no branch. A warning
      # annotation puts it in front of whoever holds the open pull request,
      # which is where the bump has to land anyway.
      if [ -n "${GITHUB_ACTIONS:-}" ]; then
        echo "::warning title=Dependency is behind the newest adoptable version::${label} pinned ${pinned}, latest adoptable ${ADOPTABLE} — inherited from ${BASELINE} rather than caused by this branch, so it does not fail here. Bundle the bump into this pull request; the scheduled run has nowhere else to put it."
      fi
      inherited=$((inherited + 1))
      return
    fi
    echo "  FAIL  $label pinned $pinned (latest adoptable $ADOPTABLE)"
    fail=$((fail + 1))
    return
  fi
  if [[ -n $HELD_NOTE ]]; then
    echo "  HELD  $label stays on $pinned: $HELD_NOTE — inside the ${quarantine_seconds}s adoption quarantine, so it is not drift yet"
    held=$((held + 1))
  fi
}

# 1. Go modules -------------------------------------------------------
# The module proxy records when each version was published and the Go toolchain
# surfaces it: `go list -m -json <module>@<version>` prints the `Time` field
# proxy.golang.org serves at /<module>/@v/<version>.info, resolved through
# whatever GOPROXY the build itself uses. That is the real publication
# timestamp, not a heuristic derived from the version string or a VCS date.
# A proxy that has no publication record for a version omits Time and the
# toolchain prints the zero time instead of nothing; adoptable_version rejects
# that rather than reading it as an ancient, safely-adoptable release.
go_module_publish_time() {
  GOFLAGS='' go list -m -json "$LOOKUP_SUBJECT@$1" 2>/dev/null | jq -r '.Time // empty'
}

echo "=== Go module dependency freshness (adoption quarantine ${quarantine_seconds}s) ==="
while IFS= read -r mod_file; do
  [[ -z "$mod_file" ]] && continue
  mod_dir=$(dirname "$mod_file")
  pushd "$mod_dir" >/dev/null

  deps=$(awk '
    /^require \(/ { in_block=1; next }
    /^\)/ && in_block { in_block=0; next }
    in_block && !/\/\/ indirect/ {
      sub(/^[ \t]+/, ""); sub(/[ \t]*\/\/.*$/, "")
      if (NF >= 2) print $1, $2
    }
    /^require [^(]/ && !/\/\/ indirect/ {
      sub(/[ \t]*\/\/.*$/, "")
      if (NF >= 3) print $2, $3
    }
  ' go.mod)

  if [[ -z "$deps" ]]; then
    popd >/dev/null
    continue
  fi

  while IFS=' ' read -r name pinned; do
    [[ -z "$name" ]] && continue
    published=$(GOFLAGS='' go list -m -versions "$name" 2>/dev/null \
      | tr ' ' '\n' | tail -n +2 \
      | grep -vE '\-(beta|alpha|rc|dev|preview)' || true)
    if [[ -z "$published" ]]; then continue; fi
    LOOKUP_CLASS=go
    LOOKUP_SUBJECT=$name
    if ! adoptable_version "$pinned" "$published"; then
      echo "  FAIL  $mod_dir: $name publication time for $UNREADABLE could not be determined from the module proxy (a version of unknown age is never adopted)"
      fail=$((fail + 1))
      continue
    fi
    report_version_state "$mod_dir: $name" "$pinned" "$mod_file" "$name"
  done <<<"$deps"
  popd >/dev/null
done < <(git ls-files 'go.mod' '*/go.mod' | sort)

# 2. Terraform providers ---------------------------------------------
# The public registry publishes per-version metadata at
# /v1/providers/<namespace>/<type>/<version>, whose `published_at` is the
# moment that provider build became downloadable. The index at
# /v1/providers/<namespace>/<type> lists every version to walk.
tf_provider_publish_time() {
  curl -fsSL "https://registry.terraform.io/v1/providers/$LOOKUP_SUBJECT/$1" 2>/dev/null | jq -r '.published_at // empty'
}

echo
echo "=== Terraform provider freshness (adoption quarantine ${quarantine_seconds}s) ==="
while IFS= read -r tf; do
  [[ -z "$tf" ]] && continue
  # Parse required_providers blocks. Output lines: "<name>|<source>|<constraint>".
  # Brace depth is tracked so the block ends at ITS closing brace rather than at
  # the first provider entry's, which would hide every provider but the first.
  parsed=$(awk '
    !in_rp && /required_providers[[:space:]]*\{/ { in_rp=1; depth=1; next }
    in_rp {
      opens = gsub(/\{/, "{"); closes = gsub(/\}/, "}")
      next_depth = depth + opens - closes
    }
    in_rp && /^[[:space:]]*[a-zA-Z_][a-zA-Z0-9_-]*[[:space:]]*=[[:space:]]*\{/ {
      n=$1; gsub("=","",n); gsub("[[:space:]]","",n); name=n; src=""; ver=""
    }
    in_rp && /source[[:space:]]*=/ {
      match($0, /"[^"]+"/); src=substr($0, RSTART+1, RLENGTH-2)
    }
    in_rp && /version[[:space:]]*=/ {
      match($0, /"[^"]+"/); ver=substr($0, RSTART+1, RLENGTH-2)
    }
    in_rp {
      depth = next_depth
      if (depth <= 0) {
        if (name != "" && src != "") { print name "|" src "|" ver }
        in_rp=0; name=""; src=""; ver=""
      }
    }
  ' "$tf")

  [[ -z "$parsed" ]] && continue

  while IFS='|' read -r name source ver_constraint; do
    [[ -z "$source" ]] && continue
    # A provider entry with no version installs whatever is newest at `terraform
    # init`, which walks straight past the adoption quarantine this check
    # exists to enforce: hashicorp/google 8.0.0 was published at 19:15Z on
    # 2026-08-26 and CI installed it 77 minutes later, breaking the Google
    # Cloud Terraform job on main. It was invisible here because the parser
    # only emitted entries that carried a version, so the one provider that
    # could not be held was also the one nobody was told about.
    if [[ -z "$ver_constraint" ]]; then
      echo "  FAIL  $tf: $name ($source) declares no version — an unpinned provider installs the newest release at init, ignoring the ${quarantine_seconds}s adoption quarantine; pin it so a major shows up here as drift instead of as a broken build"
      fail=$((fail + 1))
      continue
    fi
    index="$work/tf-index.json"
    if ! curl -fsSL -o "$index" "https://registry.terraform.io/v1/providers/${source}" 2>/dev/null; then
      echo "  FAIL  $tf: $name ($source) could not be read from the Terraform registry"
      fail=$((fail + 1))
      continue
    fi
    versions=$(jq -r '.versions[]? // empty' "$index" | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' || true)
    if [[ -z "$versions" ]]; then
      echo "  FAIL  $tf: $name ($source) publishes no comparable version in the Terraform registry"
      fail=$((fail + 1))
      continue
    fi
    LOOKUP_CLASS=terraform
    LOOKUP_SUBJECT=$source
    # The constraint is not a bare version ("~> 6.0", ">= 6.1.0"), so there is
    # no pin for the walk to stop at; it runs from the newest version down to
    # the first one that has cleared the quarantine.
    if ! adoptable_version '' "$versions"; then
      echo "  FAIL  $tf: $name ($source) publication time for $UNREADABLE could not be determined from the Terraform registry (a version of unknown age is never adopted)"
      fail=$((fail + 1))
      continue
    fi
    if [[ -z "$ADOPTABLE" ]]; then
      echo "  HELD  $tf: $name ($source) constraint $ver_constraint: $HELD_NOTE — inside the ${quarantine_seconds}s adoption quarantine, so it is not drift yet"
      held=$((held + 1))
      continue
    fi
    # An exact pin names the one version Terraform may install, so it is behind
    # the moment a newer one is adoptable. Comparing its major only would let a
    # pin sit ten minor versions back and still read as current, which is what
    # every AWS provider pin in this repository was doing. A constraint carrying
    # an operator ("~> 6.0", ">= 6.1.0") admits newer versions by itself, so
    # only its major has to keep up.
    exact_pin=$(echo "$ver_constraint" | sed -E 's/^[[:space:]]*=?[[:space:]]*//')
    if [[ "$exact_pin" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
      if [[ "$exact_pin" == "$ADOPTABLE" ]]; then
        if [[ -n "$HELD_NOTE" ]]; then
          echo "  HELD  $tf: $name ($source) pinned at $exact_pin: $HELD_NOTE — inside the ${quarantine_seconds}s adoption quarantine, so it is not drift yet"
          held=$((held + 1))
        fi
        continue
      fi
      # A pin newer than the newest adoptable release is a version still inside
      # the quarantine window, which is the state a freshly bumped pin is in for
      # its first day. That is held, not drift: reporting it would demand a
      # downgrade of the very version the last upgrade adopted.
      newer=$(printf '%s\n%s\n' "$exact_pin" "$ADOPTABLE" | sort -V | tail -1)
      if [[ "$newer" == "$exact_pin" ]]; then
        echo "  HELD  $tf: $name ($source) pinned at $exact_pin, newer than the latest adoptable $ADOPTABLE — inside the ${quarantine_seconds}s adoption quarantine, so it is not drift"
        held=$((held + 1))
        continue
      fi
      if baseline_holds_pin "$tf" "$tf" "$exact_pin"; then
        echo "  INHERITED  $tf: $name ($source) pinned at $exact_pin vs latest adoptable $ADOPTABLE"
        echo "      file unchanged from $BASELINE; the scheduled dependency freshness run holds this one"
        inherited=$((inherited + 1))
        continue
      fi
      echo "  FAIL  $tf: $name ($source) pinned at $exact_pin vs latest adoptable $ADOPTABLE (bump the pin, then \`terraform init -upgrade\`)"
      fail=$((fail + 1))
      continue
    fi
    constraint_major=$(echo "$ver_constraint" | sed -E 's/[^0-9]*([0-9]+).*/\1/')
    latest_major=$(echo "$ADOPTABLE" | sed -E 's/^([0-9]+).*/\1/')
    if [[ "$constraint_major" != "$latest_major" ]]; then
      if baseline_holds_pin "$tf" "$tf" "$ver_constraint"; then
        echo "  INHERITED  $tf: $name ($source) constraint $ver_constraint vs latest adoptable $ADOPTABLE"
        echo "      file unchanged from $BASELINE; the scheduled dependency freshness run holds this one"
        inherited=$((inherited + 1))
      else
        echo "  FAIL  $tf: $name ($source) constraint $ver_constraint vs latest adoptable $ADOPTABLE (run \`terraform init -upgrade\` then bump constraint)"
        fail=$((fail + 1))
      fi
    elif [[ -n "$HELD_NOTE" ]]; then
      echo "  HELD  $tf: $name ($source) constraint $ver_constraint: $HELD_NOTE — inside the ${quarantine_seconds}s adoption quarantine, so it is not drift yet"
      held=$((held + 1))
    fi
  done <<<"$parsed"
  # Every .tf file, because a required_providers block lives wherever the
  # configuration puts it. Scanning only versions.tf matched no file in this
  # repository at all, and a section that reads nothing reports nothing and
  # passes — the one failure mode a freshness gate cannot afford.
done < <(git ls-files '*.tf' | sort)

# 3. GitHub Actions ---------------------------------------------------
echo
echo "=== GitHub Actions freshness (adoption quarantine ${quarantine_seconds}s) ==="
# Unauthenticated requests to the GitHub API are rate limited, and a throttled
# reply looks exactly like "no tags". Use whichever credential is already
# present rather than reporting every action as current, and treat every reply
# that is not HTTP 200 as a failure so a throttled run goes red instead of
# quietly declaring everything up to date.
gh_token=${GITHUB_TOKEN:-${GH_TOKEN:-}}
if [[ -z "$gh_token" ]] && command -v gh >/dev/null 2>&1; then
  gh_token=$(gh auth token 2>/dev/null || true)
fi
github_headers=(-H 'Accept: application/vnd.github+json')
if [[ -n "$gh_token" ]]; then
  github_headers+=(-H "Authorization: Bearer $gh_token")
fi

# The rate-limit protocol lives in its own file so the decision it makes can be
# exercised against crafted headers: a throttle is not something a test can ask
# GitHub for. Sourced through REPO_ROOT rather than BASH_SOURCE, which zsh —
# the other shell this check runs under in CI — does not set.
# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib/github-throttle.sh
source "$REPO_ROOT/scripts/lib/github-throttle.sh"

# gh_api writes the body of <url> to <outfile> and succeeds only on HTTP 200.
# A bad credential (401) and a missing resource (404) fail here, so no caller
# can mistake either for an empty-but-valid answer.
#
# A throttled reply (403 or 429 carrying a rate-limit signal) is different in
# kind: it is transient, and the API says when to come back — `Retry-After`, or
# `X-RateLimit-Reset` once the remaining quota is zero. Failing the run on the
# first one turns a branch red for a reason nobody caused, so the documented
# wait is honoured and the request retried. The wait is never guessed: a 403
# that carries no rate-limit signal at all is a real refusal and fails at once.
gh_api() {
  local url=$1 out=$2 code headers attempt wait_for
  headers="$work/gh-headers.txt"
  for attempt in 1 2 3; do
    code=$(curl -sSL -o "$out" -D "$headers" -w '%{http_code}' "${github_headers[@]}" "$url" 2>/dev/null || echo 000)
    [[ $code == 200 ]] && return 0
    [[ $code == 403 || $code == 429 ]] || return 1
    [[ $attempt == 3 ]] && return 1
    wait_for=$(gh_throttle_wait "$headers" "$(date +%s)") || return 1
    echo "  ..    GitHub API throttled on ${url#https://api.github.com/repos/}; waiting ${wait_for}s as the reply asked" >&2
    sleep "$wait_for"
  done
  return 1
}

# gh_tag_publish_time prints when a tag of LOOKUP_SUBJECT became available.
#
# A GitHub Release is the moment a version becomes something people install, so
# its `published_at` is the timestamp that matters and is used whenever the tag
# has one. The release is fetched for that one tag rather than looked up in a
# page of the repository's releases: the list endpoint has been observed
# answering 200 with an empty array for a repository whose releases exist and
# are individually readable, and an absence read out of that page is
# indistinguishable from a tag that carries no release — which would silently
# age a version by days, adopting it before its quarantine had run. A 404 here
# says the tag genuinely has no release; anything else is a failure.
#
# Tags cut without a release carry only git's own record: an annotated tag
# object's `tagger.date`, or, for a lightweight tag — which stores no metadata
# of its own — the committer date of the commit it names. Anything unresolvable
# prints nothing, and the caller fails the run.
gh_tag_publish_time() {
  local tag=$1
  local release obj ref_type ref_sha ts code
  release="$work/gh-release.json"
  code=$(curl -sSL -o "$release" -w '%{http_code}' "${github_headers[@]}" \
    "https://api.github.com/repos/$LOOKUP_SUBJECT/releases/tags/$tag" 2>/dev/null || echo 000)
  case "$code" in
    200)
      ts=$(jq -r '.published_at // empty' "$release" 2>/dev/null || true)
      [[ -n $ts ]] || return 1
      printf '%s\n' "$ts"
      return 0
      ;;
    404) ;; # the tag carries no release, so git's own record is all there is
    *) return 1 ;;
  esac
  obj="$work/gh-object.json"
  gh_api "https://api.github.com/repos/$LOOKUP_SUBJECT/git/ref/tags/$tag" "$obj" || return 1
  ref_type=$(jq -r '.object.type // empty' "$obj")
  ref_sha=$(jq -r '.object.sha // empty' "$obj")
  case "$ref_type" in
    tag)
      gh_api "https://api.github.com/repos/$LOOKUP_SUBJECT/git/tags/$ref_sha" "$obj" || return 1
      ts=$(jq -r '.tagger.date // empty' "$obj")
      ;;
    commit)
      gh_api "https://api.github.com/repos/$LOOKUP_SUBJECT/commits/$ref_sha" "$obj" || return 1
      ts=$(jq -r '.commit.committer.date // empty' "$obj")
      ;;
    *) return 1 ;;
  esac
  [[ -n $ts ]] || return 1
  printf '%s\n' "$ts"
}

if [[ -d .github/workflows ]]; then
  actions=$(
    while IFS= read -r workflow_file; do
      [[ -z "$workflow_file" ]] && continue
      awk '
        /^[[:space:]-]*uses:[[:space:]]*/ {
          ref=$0
          sub(/^[[:space:]-]*uses:[[:space:]]*/, "", ref)
          gsub(/["'\'']/, "", ref)
          split(ref, fields, /[[:space:]]/)
          ref=fields[1]
          if (ref ~ /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+@/) print FILENAME "|" ref
        }
      ' "$workflow_file"
    done < <(git ls-files '.github/workflows/*.yml' '.github/workflows/*.yaml' | sort) | sort -u
  )

  while IFS='|' read -r file action_ref; do
    [[ -z "$action_ref" ]] && continue
    repo=${action_ref%@*}
    pinned=${action_ref#*@}
    tagfile="$work/gh-tags.json"
    if ! gh_api "https://api.github.com/repos/${repo}/tags?per_page=100" "$tagfile"; then
      echo "  FAIL  $file: $repo tags could not be read (set GITHUB_TOKEN, or authenticate the gh CLI; unauthenticated requests are rate limited)"
      fail=$((fail + 1))
      continue
    fi
    tags=$(jq -r '.[].name' "$tagfile" | grep -E '^v?[0-9]+\.[0-9]+(\.[0-9]+)?$' || true)
    if [[ -z "$tags" ]]; then
      echo "  FAIL  $file: $repo publishes no semantic tag to compare $pinned against"
      fail=$((fail + 1))
      continue
    fi
    LOOKUP_CLASS=github
    LOOKUP_SUBJECT=$repo
    if ! adoptable_version "$pinned" "$tags"; then
      echo "  FAIL  $file: $repo publication time for $UNREADABLE could not be determined from the GitHub API (a tag of unknown age is never adopted)"
      fail=$((fail + 1))
      continue
    fi
    report_version_state "$file: $repo" "$pinned" "$file" "$repo"
  done <<<"$actions"
fi

echo
if [[ $held -gt 0 ]]; then
  echo "$held newer version(s) held: published less than ${quarantine_seconds}s ago and deliberately not adopted yet. They are re-checked on every run and become drift once they age out."
fi
if [[ $inherited -gt 0 ]]; then
  echo "$inherited dependency drift(s) inherited from $BASELINE: upstream moved under this branch rather than this branch letting the pin rot, so they do not fail here. The scheduled dependency freshness run carries no baseline and fails on them there; bundling the bump into the open pull request is what clears it."
fi
if [[ $fail -gt 0 ]]; then
  echo "$fail dependency drift(s) detected. Run \`make upgrade-deps\` from the affected module dirs (Go), update the provider constraint + \`terraform init -upgrade\` (TF), or pin GitHub Actions to the newest adoptable semantic tag, then re-run this check." >&2
  exit 1
fi
echo "OK: every dependency this branch owns is on the newest version that has cleared the ${quarantine_seconds}s adoption quarantine."
exit 0
