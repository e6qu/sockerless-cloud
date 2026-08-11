#!/usr/bin/env bash
# check-locked-helpers.sh — forbid store-mutex acquisition inside *Locked helpers.
#
# Functions whose name ends in "Locked" run under a store lock the CALLER
# already holds; acquiring the same sync.RWMutex again deadlocks, because Go's
# RWMutex read locks are not reentrant (a queued writer blocks new readers, so
# a goroutine that re-RLocks while holding the read lock waits forever on the
# writer that is waiting on it).
#
# The scan flags `st.mu.Lock()` / `st.mu.RLock()` / `s.store.mu.*` inside a
# top-level Go function whose declared name ends in "Locked". Sub-store
# mutexes (st.Misc.mu, st.Reactions.mu, …) are NOT flagged: the documented
# lock order (Store.mu before sub-store mutexes) makes acquiring them under
# the store lock legal. Function bodies are delimited by column-0 `func` /
# `}` lines (gofmt guarantees both), so the scan is deterministic.
#
# Functions that use "Locked" as domain vocabulary rather than as the
# lock-contract suffix (e.g. IsUserMigrationRepoLocked, "is this repo
# locked?") go in scripts/locked-helpers-allowlist.txt, one name per line.
#
# bash + zsh portable; shellcheck-clean.
set -u

cd "$(git rev-parse --show-toplevel)" || exit 2

allowlist="scripts/locked-helpers-allowlist.txt"

# shellcheck disable=SC2016 # awk $0/$NF are awk fields, not shell expansions
hits="$(find backends agent simulators core cmd -name '*.go' ! -name '*_test.go' 2>/dev/null \
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
      infunc && /(^|[^.A-Za-z0-9_])(st|[A-Za-z0-9_.]*store)\.mu\.R?Lock\(\)/ {
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
