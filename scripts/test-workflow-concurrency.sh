#!/usr/bin/env bash
# Fixture tests for check-workflow-concurrency.sh, including the negative
# controls that prove the gate still fails on the shapes it exists to catch.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT

expect_pass() {
	if ! "$root/scripts/check-workflow-concurrency.sh" "$1" >/dev/null; then
		echo "expected workflow concurrency fixture to pass: $1" >&2
		exit 1
	fi
}

expect_fail() {
	if "$root/scripts/check-workflow-concurrency.sh" "$1" >/dev/null 2>&1; then
		echo "expected workflow concurrency fixture to fail: $1" >&2
		exit 1
	fi
}

mkdir -p "$fixture"/{per-commit,pull-request,no-concurrency,not-push,branch-keyed,ref-keyed,flow-push,constant-group,groupless,empty}

# A push workflow keyed by commit: one member per group, nothing superseded.
cat >"$fixture/per-commit/publish.yml" <<'YAML'
name: publish
on:
  push:
    branches: [main]
concurrency:
  group: ${{ github.workflow }}-${{ github.repository }}-${{ github.sha }}
  cancel-in-progress: false
jobs:
  build:
    runs-on: ubuntu-latest
    steps: []
YAML

# The mixed shape CI uses: pull-request runs cancel by head ref, pushes fall
# back to the commit.
cat >"$fixture/pull-request/ci.yml" <<'YAML'
name: ci
on:
  pull_request:
    branches: [main]
  push:
    branches: [main]
concurrency:
  group: ${{ github.workflow }}-${{ github.event.pull_request.head.ref || github.sha }}
  cancel-in-progress: true
jobs:
  test:
    runs-on: ubuntu-latest
    steps: []
YAML

cat >"$fixture/no-concurrency/release-please.yml" <<'YAML'
name: release-please
on:
  push:
    branches: [main]
jobs:
  release:
    runs-on: ubuntu-latest
    steps: []
YAML

# Not push-triggered: superseding is safe, the newer run subsumes the older.
cat >"$fixture/not-push/prune.yml" <<'YAML'
name: prune
on:
  workflow_run:
    workflows: [publish]
    types: [completed]
  schedule:
    - cron: '0 4 * * *'
concurrency:
  group: prune
  cancel-in-progress: true
jobs:
  prune:
    runs-on: ubuntu-latest
    steps: []
YAML

# Negative control: the exact shape that cancelled five publishes.
cat >"$fixture/branch-keyed/publish.yml" <<'YAML'
name: publish
on:
  push:
    branches: [main]
concurrency:
  group: ${{ github.workflow }}-${{ github.event.pull_request.head.ref || github.ref_name }}
  cancel-in-progress: true
jobs:
  build:
    runs-on: ubuntu-latest
    steps: []
YAML

cat >"$fixture/ref-keyed/publish.yml" <<'YAML'
name: publish
on:
  push:
    branches: [main]
concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: false
jobs:
  build:
    runs-on: ubuntu-latest
    steps: []
YAML

# The same defect reached through the flow-sequence trigger form.
cat >"$fixture/flow-push/publish.yml" <<'YAML'
name: publish
on: [push, pull_request]
concurrency:
  group: ${{ github.workflow }}-${{ github.ref_name }}
  cancel-in-progress: true
jobs:
  build:
    runs-on: ubuntu-latest
    steps: []
YAML

cat >"$fixture/constant-group/publish.yml" <<'YAML'
name: publish
on:
  push:
    branches: [main]
concurrency:
  group: publish
  cancel-in-progress: true
jobs:
  build:
    runs-on: ubuntu-latest
    steps: []
YAML

cat >"$fixture/groupless/publish.yml" <<'YAML'
name: publish
on:
  push:
    branches: [main]
concurrency:
  cancel-in-progress: true
jobs:
  build:
    runs-on: ubuntu-latest
    steps: []
YAML

expect_pass "$fixture/per-commit"
expect_pass "$fixture/pull-request"
expect_pass "$fixture/no-concurrency"
expect_pass "$fixture/not-push"
expect_fail "$fixture/branch-keyed"
expect_fail "$fixture/ref-keyed"
expect_fail "$fixture/flow-push"
expect_fail "$fixture/constant-group"
expect_fail "$fixture/groupless"
# A directory with no workflows means the gate read nothing; it must say so
# rather than report a vacuous pass.
expect_fail "$fixture/empty"
echo "workflow concurrency fixture tests passed"
