#!/usr/bin/env bash
# check-locked-helpers.sh — forbid store-mutex acquisition inside *Locked helpers.
#
# Functions whose name ends in "Locked" run under a store lock the CALLER
# already holds; acquiring the same sync.RWMutex again deadlocks, because Go's
# RWMutex read locks are not reentrant (a queued writer blocks new readers, so
# a goroutine that re-RLocks while holding the read lock waits forever on the
# writer that is waiting on it).
#
# The scan flags any `<store>.mu.Lock()` / `<store>.mu.RLock()` inside a
# top-level Go function whose declared name ends in "Locked". The receiver is
# not spelled out: this repository's stores are named for what they hold
# (ecsTasks, azureSites), not `st` or `*store`, so a pattern naming those
# receivers would match none of them. Function bodies are delimited by
# column-0 `func` / `}` lines (gofmt guarantees both), so the scan is
# deterministic.
#
# Functions that use "Locked" as domain vocabulary rather than as the
# lock-contract suffix (e.g. IsUserMigrationRepoLocked, "is this repo
# locked?") go in scripts/locked-helpers-allowlist.txt, one name per line.
#
# bash + zsh portable; shellcheck-clean.
set -u

cd "$(git rev-parse --show-toplevel)" || exit 2

allowlist="scripts/locked-helpers-allowlist.txt"

# The Go source this repository owns. The sockerless monorepo's backends/,
# agent/, simulators/, core/ and cmd/ are not among its directories, and a
# find naming those listed no files here.
SCAN_DIRS="sim simulator-aws simulator-azure simulator-gcp realexec testutil ui-auth"

# A gate that scans nothing passes for the wrong reason. Prove the scan set is
# non-empty before trusting a clean result.
# shellcheck disable=SC2086 # SCAN_DIRS is a deliberate word list
scan_count="$(find $SCAN_DIRS -name '*.go' ! -name '*_test.go' 2>/dev/null | grep -c . || true)"
if [ "${scan_count:-0}" -eq 0 ]; then
  echo "check-locked-helpers: no Go files under $SCAN_DIRS — the scan set is empty," >&2
  echo "so a clean result would prove nothing. Fix the scan set." >&2
  exit 2
fi

# shellcheck disable=SC2016,SC2086 # awk $0/$NF are awk fields, not shell
# expansions; SCAN_DIRS is a deliberate word list
hits="$(find $SCAN_DIRS -name '*.go' ! -name '*_test.go' 2>/dev/null \
  | sort \
  | xargs awk -v allowfile="$allowlist" '
      BEGIN {
        while ((getline line < allowfile) > 0) {
          sub(/#.*/, "", line)
          gsub(/[ \t]/, "", line)
          if (line != "") allow[line] = 1
        }
        close(allowfile)
      }
      /^func / {
        infunc = 0
        name = $0
        sub(/^func +/, "", name)
        sub(/^\([^)]*\) */, "", name)
        sub(/[(\[].*$/, "", name)
        if (name ~ /Locked$/ && !(name in allow)) infunc = 1
      }
      /^}/ { infunc = 0 }
      infunc && /(^|[^.A-Za-z0-9_])[A-Za-z0-9_.]+\.mu\.R?Lock\(\)/ {
        printf "%s:%d: %s\n", FILENAME, FNR, $0
      }
    ' || true)"

if [ -n "$hits" ]; then
  echo "Store-mutex acquisition inside a *Locked helper (caller already holds the lock):"
  echo "$hits"
  echo
  echo "*Locked helpers run under the caller's store mutex; re-acquiring it"
  echo "deadlocks (sync.RWMutex is not reentrant once a writer queues). Either"
  echo "drop the acquisition and rely on the caller's lock, or rename the helper"
  echo "to give it a self-locking contract. Domain names that merely end in"
  echo "\"Locked\" go in scripts/locked-helpers-allowlist.txt."
  exit 1
fi
exit 0
