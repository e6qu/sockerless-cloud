#!/usr/bin/env bash
# check-latest-deps.sh — fail loud if any dependency is behind its
# latest published version. Runs as a pre-commit hook + CI job.
#
# Scope:
#   1. Go modules across every go.mod — for each direct require, query
#      `go list -m -versions <module>` and compare against the latest
#      published version. ANY drift fails (no warn tier — operator runs
#      `make upgrade-deps` to bring everything current).
#   2. Terraform providers across every required_providers block —
#      check the version constraint against the latest registry version.
#      Any drift fails.
#   3. GitHub Actions in .github/workflows — every owner/repo action
#      must be pinned to the latest published semantic version tag.
#
# Exit code: 0 only when every direct dependency matches the latest
# published version. 1 on any drift.

set -euo pipefail

if ! REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null); then
  script_path=$0
  case "$script_path" in
    */*) ;;
    *) script_path="./$script_path" ;;
  esac
  REPO_ROOT="$(cd "$(dirname "$script_path")/.." && pwd)"
fi
cd "$REPO_ROOT"

for tool in go curl jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "ERROR: $tool not on PATH" >&2; exit 1; }
done

fail=0

# 1. Go modules -------------------------------------------------------
echo "=== Go module dependency freshness ==="
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
    latest=$(GOFLAGS='' go list -m -versions "$name" 2>/dev/null \
      | tr ' ' '\n' | tail -n +2 \
      | grep -vE '\-(beta|alpha|rc|dev|preview)' | tail -1 || true)
    if [[ -z "$latest" ]]; then continue; fi
    if [[ "$pinned" != "$latest" ]]; then
      echo "  FAIL  $mod_dir: $name pinned $pinned (latest $latest)"
      fail=$((fail + 1))
    fi
  done <<<"$deps"
  popd >/dev/null
done < <(git ls-files 'go.mod' '*/go.mod' | sort)

# 2. Terraform providers ---------------------------------------------
echo
echo "=== Terraform provider freshness ==="
while IFS= read -r tf; do
  [[ -z "$tf" ]] && continue
  # Parse required_providers block. Output lines: "<name>|<source>|<constraint>"
  parsed=$(awk '
    /required_providers/ { in_rp=1; next }
    in_rp && /^[[:space:]]*}[[:space:]]*$/ { in_rp=0 }
    in_rp && /[a-zA-Z_][a-zA-Z0-9_-]*[[:space:]]*=[[:space:]]*\{/ {
      n=$1; gsub("=","",n); gsub("[[:space:]]","",n); name=n; src=""; ver=""; next
    }
    in_rp && /source[[:space:]]*=/ {
      match($0, /"[^"]+"/); src=substr($0, RSTART+1, RLENGTH-2)
    }
    in_rp && /version[[:space:]]*=/ {
      match($0, /"[^"]+"/); ver=substr($0, RSTART+1, RLENGTH-2)
      if (name != "" && src != "" && ver != "") {
        print name "|" src "|" ver
        name=""; src=""; ver=""
      }
    }
  ' "$tf")

  while IFS='|' read -r name source ver_constraint; do
    [[ -z "$source" ]] && continue
    latest=$(curl -fsSL "https://registry.terraform.io/v1/providers/${source}" 2>/dev/null | jq -r '.version' || echo "")
    if [[ -z "$latest" || "$latest" == "null" ]]; then
      echo "  FAIL  $tf: $name ($source) latest version could not be determined from the Terraform registry"
      fail=$((fail + 1))
      continue
    fi
    constraint_major=$(echo "$ver_constraint" | sed -E 's/[^0-9]*([0-9]+).*/\1/')
    latest_major=$(echo "$latest" | sed -E 's/^([0-9]+).*/\1/')
    if [[ "$constraint_major" != "$latest_major" ]]; then
      echo "  FAIL  $tf: $name ($source) constraint $ver_constraint vs latest $latest (run \`terraform init -upgrade\` then bump constraint)"
      fail=$((fail + 1))
    fi
  done <<<"$parsed"
done < <(git ls-files 'versions.tf' '*/versions.tf' | sort)

# 3. GitHub Actions ---------------------------------------------------
echo
echo "=== GitHub Actions freshness ==="
# Unauthenticated requests to the GitHub API are rate limited, and a throttled
# reply looks exactly like "no tags". Use whichever credential is already
# present rather than reporting every action as current.
gh_token=${GITHUB_TOKEN:-${GH_TOKEN:-}}
if [[ -z "$gh_token" ]] && command -v gh >/dev/null 2>&1; then
  gh_token=$(gh auth token 2>/dev/null || true)
fi
github_headers=()
if [[ -n "$gh_token" ]]; then
  github_headers=(-H "Authorization: Bearer $gh_token")
fi
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
    tags=$(curl -fsSL "${github_headers[@]}" \
      "https://api.github.com/repos/${repo}/tags?per_page=100" 2>/dev/null | jq -r '.[].name' || true)
    if [[ -z "$tags" ]]; then
      echo "  FAIL  $file: $repo tags could not be read (set GITHUB_TOKEN, or authenticate the gh CLI; unauthenticated requests are rate limited)"
      fail=$((fail + 1))
      continue
    fi
    latest=$(printf '%s\n' "$tags" | grep -E '^v?[0-9]+\.[0-9]+(\.[0-9]+)?$' | sort -V | tail -1 || true)
    if [[ -z "$latest" ]]; then
      echo "  FAIL  $file: $repo publishes no semantic tag to compare $pinned against"
      fail=$((fail + 1))
      continue
    fi
    if [[ "$pinned" != "$latest" ]]; then
      echo "  FAIL  $file: $repo pinned $pinned (latest $latest)"
      fail=$((fail + 1))
    fi
  done <<<"$actions"
fi

echo
if [[ $fail -gt 0 ]]; then
  echo "$fail dependency drift(s) detected. Run \`make upgrade-deps\` from the affected module dirs (Go), update versions.tf + \`terraform init -upgrade\` (TF), or pin GitHub Actions to the latest published semantic tag, then re-run this check." >&2
  exit 1
fi
echo "OK: every dependency is on its latest version."
exit 0
