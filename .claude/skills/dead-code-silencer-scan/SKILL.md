---
name: dead-code-silencer-scan
description: Specialist scan for the dead-code-silencer pattern — `var _ = pkg.Fn` import silencers, `//nolint:unused` directives without consumer references, and `_ = someVar` "keep for later" pins. Distilled from BUGS 1020 / 1021 / 1022 / 1024 / 1034 — five bug IDs across the same shape, where code was held for hypothetical future use and the silencer became the load-bearing artifact. Use before merging any Go change and periodically across the codebase.
---

# Dead-code-silencer scan

Five bugs across one phase (BUGS 1020, 1021, 1022, 1024, 1034) all collapsed to the same shape: a function / variable / import was retained for hypothetical future use with a silencer (`var _ = pkg.Fn // keep for ad-hoc tweaks`, `//nolint:unused // consumers ship in subsequent commits`, `_ = someVar // suppress unused`), then the future use never arrived. The silencer becomes the only thing keeping the dead code alive.

The `docs/VIBE_CODING.md` catalogue calls this out as patterns 14 (dead code held for hypothetical future use) and 27 (AI-as-expansion-engine, no pruning). This skill is the automated scan that surfaces it before more `//nolint` directives accumulate.

## When this skill applies

- Before merging any Go change.
- Periodically across the codebase (every 10 PRs).
- When auditing a service that has accumulated `//nolint:unused` directives.
- When the "test still references X" comment shows up in a PR review.

Skip for: generated code (`*_gen.go`, `goverter` output, `cd-protocol` types), genuinely-unused exported APIs (these are not "dead code" — they're public surface area), vendor.

## The patterns to scan

### Pattern A — import silencers

```bash
# `var _ = pkg.Fn` at package scope (the "keep import" pattern)
rg -n '^var\s+_\s*=\s*[a-z]\w*\.[A-Z]\w*' --type go \
  -g '!*_test.go' -g '!*_gen.go' -g '!vendor/'

# `_ = pkg.Fn` at function scope claiming to silence imports
rg -nC1 '_\s*=\s*[a-z]\w*\.[A-Z]\w*\s*//.*(silence|suppress|keep|future|reserved)' \
  --type go -g '!*_test.go' -g '!vendor/'

# Imports that look like silencer victims: imported package whose only
# in-file reference is the silencer line
rg -l '^var\s+_\s*=' --type go simulator-aws/ simulator-gcp/ simulator-azure/ realexec/ ui-auth/ testutil/
```

### Pattern B — `//nolint:unused` directives

The directive itself is a signal — without the directive, `staticcheck` would surface the dead code.

```bash
# All nolint:unused directives, with the line being silenced
rg -nC2 '//nolint:unused' --type go -g '!*_gen.go' -g '!vendor/'

# Specifically targeting the "consumers ship in a later commit" excuse
rg -nB1 -A2 'nolint:unused.*(consumer|subsequent|later commit|future)' --type go
```

### Pattern C — "kept for diagnostics" stale helpers

The BUG-1023 / 1034 shape — a function exists but every caller has been removed; comment says "kept for diagnostics."

```bash
# `func stringifyFoo` or similar diagnostic helpers — verify each has callers
rg -n '^func\s+\w+\s*\([^)]*\)\s*\w*\s*\{?\s*$' --type go -g '!*_test.go' -g '!*_gen.go' \
  | while read line; do
      file=$(echo "$line" | cut -d: -f1)
      fnName=$(echo "$line" | rg -o '^[^:]+:\d+:func\s+(\w+)' -r '$1')
      # Skip helpers with multiple callers
      callers=$(rg -c "$fnName\(" "$file" --type go)
      if [ "$callers" -le "1" ]; then
        echo "$line — only self-reference"
      fi
    done
```

### Pattern D — "reserved for" variables

The BUG-1021 `flexInt64` shape — a type defined to mirror another type "in case a future endpoint needs it."

```bash
rg -nC3 '//.*[Rr]eserved for' --type go -g '!*_test.go' -g '!vendor/'
rg -nC3 '//.*[Nn]ot consumed by any' --type go -g '!*_test.go' -g '!vendor/'
rg -nC3 '//.*[Cc]urrently unused' --type go -g '!*_test.go' -g '!vendor/'
rg -nC3 '//.*[Ii]n case' --type go -g '!*_test.go' -g '!vendor/'
```

### Pattern E — "consumers land in" stale forward-references

The BUG-1021 + BUG-1020 shape — the nolint directive claims the consumer ships in a later commit, but the commit either landed (consumer exists now → drop the directive) or never came (dead code).

```bash
rg -nC1 '//.*(consumers? (ship|land|come|will))' --type go -g '!vendor/'
```

## How to classify each finding

For every hit:

1. **The consumer landed → drop the silencer.** The most common outcome. `//nolint:unused // consumers ship in subsequent commits on this branch` paired with a function that now has 3 callers. The directive is stale; remove it.
2. **The consumer never landed → drop both.** Function with no callers, no `_test.go` reference, no caller in any cmd / wire / handler. Delete the function and the silencer.
3. **Genuine future API → either export it for real or remove it.** If a function is "for SDK consumers", make it a public exported function in a public package and document it. Internal "future use" is not a valid retention reason.
4. **Generated code → annotate the source generator.** If a silencer exists in code that's regenerated, the generator emits the silencer; either teach the generator to skip or split the file so hand-edits don't accumulate.

## How to fix

```go
// Before: stale forward-reference
//nolint:unused // consumer ships in the workflow-trigger commit
func buildPullRequestPayloadWithInstallation(...) {...}

// After (consumer landed): drop the directive
func buildPullRequestPayloadWithInstallation(...) {...}

// After (consumer never landed): delete the function entirely
```

```go
// Before: import silencer holding a "maybe useful" helper alive
var _ = httputil.DumpRequest // keep import for ad-hoc tweaks
import "net/http/httputil"

// After (the helper was never used in the file)
// (delete both lines; the import goes too)
```

If a future PR genuinely needs the helper back, it's a one-line `import` + a one-line consumer. The retention cost is zero. The "keep just in case" tax compounds.

## Known prior occurrences this skill replays

- **BUG-1020** — `buildPullRequestPayloadWithInstallation` + `buildIssuesPayloadWithInstallation` carried `//nolint:unused // callers land in the workflow-trigger commit` for 100+ commits; that commit never came.
- **BUG-1021** — Stale `//nolint:unused` pragmas on `gh_middleware.go` context helpers; consumers had landed but the directives stayed. Plus `flexInt64` reserved-for-future-use type with zero consumers.
- **BUG-1022** — Six unused-import silencers across bleephub + AWS sim.
- **BUG-1023** — `stringifyJobState` in `github-runner-dispatcher-gcp` was unused with `//nolint:unused // kept for diagnostics`.
- **BUG-1024** — `tools/http-trace/main.go::var _ = httputil.DumpRequest // keep import for ad-hoc tweaks`.
- **BUG-1034** — `backends/lambda/agent_e2e_integration_test.go::var _ = fmt.Sprintf` with the lying comment "fmt is used by the framing demuxer when debugging" — the claimed consumer didn't reference `fmt` at all.

Six bugs in two phases. The pattern is recurrent enough to deserve its own scan.

## Related skills

- `avoid-vibe-slop` — broader project-local checklist; this skill is its specialist for the dead-code dimension.
- `silent-error-swallow-scan` — sister skill for the silent-error pattern.
- `backpedal-pattern-audit` — meta-skill that surfaces patterns like this from BUGS.md.
