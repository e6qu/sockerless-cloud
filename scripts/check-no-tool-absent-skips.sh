#!/usr/bin/env bash
# Reject newly-added test skips that hide missing tools/dependencies. Required
# tools must be installed by the test harness or fail loud; only platform/kernel
# capability gates should skip.
#
# Three shapes are rejected on added (+) lines of *_test.go:
#   1. t.Skip / t.Skipf carrying a tool-absent phrase.
#   2. fmt.Print*/log.Print* carrying a tool-absent phrase — a TestMain-level
#      "print then return/os.Exit(0)" skip that never calls t.Skip.
#   3. A bare os.Exit(0) within a few added lines of a LookPath(...) in the same
#      hunk — the "LookPath fails -> exit success" silent skip.
#
# Kernel/platform capability gates (runtime.GOOS, CAP_NET_ADMIN, /dev/kvm) are
# not matched: they do not use LookPath, so the LookPath-anchored shape 3 can
# never hit them, and the phrase-based shapes 1 and 2 skip any line that
# self-identifies as a platform/capability gate (see $platform_gate) — even one
# whose message happens to contain a tool-absent phrase, e.g. the GOOS-gated
# `t.Skip("platform gate: sleep binary not available ... Unix-like host required")`.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

# The check is incremental — it gates skip code ADDED by this change. A root
# commit has no parent, so the whole tree imported from the sockerless
# monorepo would count as added, including grandfathered skips that predate
# the hook (tracked in BUGS.md). With no parent there is no incremental
# change to gate.
if ! git rev-parse --verify HEAD >/dev/null 2>&1; then
  echo "[no-tool-absent-skips] root commit (no parent): nothing incremental to gate"
  exit 0
fi

range="${1:---cached}"
diff=$(git diff "$range" -- '*_test.go' 2>/dev/null || true)
if [[ -z "$diff" ]]; then
  exit 0
fi

# Phrases that mark a skip as tool/dependency-absent (as opposed to a
# capability/platform gate). Shared by the t.Skip and print-then-skip checks.
phrases='not available|not found|not installed|missing|required.*(binary|tool|cli)|could not launch.*skipping|skipping.*(emulator|differential|oracle)'

# Markers that a skip is a sanctioned platform/kernel-capability gate — allowed
# even when its message also carries a tool-absent phrase. AGENTS.md requires
# these gates to be phrased as such ("requires a Linux host", "platform gate"),
# so keying the exemption on that phrasing is the intended contract; a
# LookPath-guarded tool-absent skip does not use this language, and mislabelling
# one to dodge the gate is visible in review.
platform_gate='platform gate|capability gate|requires (a |the )?(linux|unix|posix|darwin|macos|windows|x86|arm|amd64).*host|host required|unix-like|GOOS|CAP_NET_ADMIN|/dev/kvm|netns fabric|kernel'

# 1. t.Skip / t.Skipf with a tool-absent phrase (unless a platform-gate marker).
skip_matches=$(printf '%s\n' "$diff" \
  | grep -E '^\+[^+].*t\.Skipf?\(' \
  | grep -Ei "$phrases" \
  | grep -viE "$platform_gate" || true)

# 2. fmt.Print*/log.Print* with a tool-absent phrase (print-then-skip; no
#    t.Skip call). Matches Print/Println/Printf only — not Fatal/Fatalf (loud
#    failure, allowed) and not Fprintf (used by the sanctioned fail-loud helper
#    that prints to stderr then os.Exit(1)). Platform-gate markers exempt.
print_matches=$(printf '%s\n' "$diff" \
  | grep -E '^\+[^+].*(fmt\.Print(ln|f)?|log\.Print(ln|f)?)\(' \
  | grep -Ei "$phrases" \
  | grep -viE "$platform_gate" || true)

# 3. os.Exit(0) within a few added lines of a LookPath(...) in the same hunk.
#    os.Exit(0) is a success exit; guarded on a LookPath failure it is a silent
#    skip. os.Exit(1)/log.Fatal (the fail-loud shape) are deliberately not
#    matched. Hunk-scoped so the window never spans unrelated code.
exit_matches=$(printf '%s\n' "$diff" | awk '
  /^@@/     { win = 0; next }
  /^\+\+\+/ { next }
  /^\+/ {
    if (win > 0 && $0 ~ /os\.Exit\(0\)/) { print }
    if ($0 ~ /LookPath\(/) { win = 6 }
    else if (win > 0)      { win-- }
    next
  }
')

matches=$(printf '%s\n%s\n%s\n' "$skip_matches" "$print_matches" "$exit_matches" | grep -v '^$' || true)

if [[ -n "$matches" ]]; then
  cat >&2 <<'MSG'
New skip-if-tool/dependency-absent test code is not allowed.

Required tools must be installed by TestMain/harness setup or fail loud with
t.Fatal/log.Fatal. Kernel/platform capability gates are allowed, but phrase them
as capability/platform gates rather than missing-tool skips. Do not skip via
t.Skip, a fmt/log print + return/os.Exit(0), or a LookPath-guarded os.Exit(0).
MSG
  printf '%s\n' "$matches" >&2
  exit 1
fi

exit 0
