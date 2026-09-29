---
name: backpedal-pattern-audit
description: Meta-skill that surfaces bug shapes that were filed and fixed repeatedly — read from the resolved bugs in git history — so each repeat group becomes a candidate for a new specialist skill or a rule in WHAT_WE_DID.md. Run periodically (every ~10 PRs) or when the user asks "what blind spots do we have." Distilled from the silent-error-swallow and dead-code-silencer shapes, which recurred often enough to justify the dedicated `silent-error-swallow-scan` and `dead-code-silencer-scan` skills.
---

# Backpedal-pattern audit

This is the meta-skill. Specialist scan skills (`silent-error-swallow-scan`, `dead-code-silencer-scan`, `sim-canonical-config-test`, `sim-emitted-url-roundtrip`, `sim-streaming-body-handler`) catch *known* recurring patterns. This skill catches the recurring patterns we haven't yet codified — the ones being filed for the 3rd or 4th time.

The signal: when the same kind of bug shape has been filed and fixed 3+ times across 2+ pull requests, the *category* itself has become a known weakness. That's the inflection point where it deserves a dedicated scan rather than catching each instance one-at-a-time.

## When this skill applies

- Every ~10 merged PRs.
- Before starting a major piece of work, so the plan can include preemptive scans for known recurrences.
- When the user asks "what blind spots do we have" or "where do we backpedal a lot."

Skip for: per-bug fix work (use the per-pattern specialist skill instead), routine commits, doc-only changes.

## The process

### Step 1 — load the resolved bugs from git history

`BUGS.md` holds only open bugs; a bug leaves it when it is fixed. Read the
resolved ones back out of git:

```bash
# Every BUGS.md row or entry a commit removed, newest first
git log -p --format='commit %h %ad %s' --date=short -- BUGS.md \
  | rg '^commit |^-(\| [0-9]+ \||- (~~)?\*\*BUG-)'

# The fix commits themselves
git log --format='%h %s' -E --grep='^fix'
```

`WHAT_WE_DID.md` carries the rule each class of fix left behind; read it
alongside, so a shape that already has a rule is not proposed twice.

### Step 2 — group by bug-fix shape

For each entry, extract the shape signature. Heuristics for shape extraction (no exact algorithm — pattern-match on the one-liner):

| Shape signature | Cue keywords |
|---|---|
| Silent error swallow | "silently swallowed", "ignored error", "_ = json.Unmarshal", "dropped err", "fell through to fallback" |
| Dead-code silencer | "//nolint:unused", "var _ =", "kept for diagnostics", "reserved for future", "consumers ship in subsequent commits" |
| Time-anchored metadata in comments | "Phase N", "BUG-NNNN" in code, "lineage header", "implementation-coupled metadata" |
| Test asserted on metadata | "asserts on metadata", "test docstring", "implementation-metadata in test" |
| Sim-quirk-in-test (this audit's catalyst) | "BaseEndpoint = baseURL", "UsePathStyle = true to match sim", "test rewritten to match" |
| Emitted-URL not serviced | "advertised endpoint", "selfLink without round-trip", "URL emitted but no handler" |
| Streaming-envelope not decoded | "aws-chunked", "STREAMING-", "x-amz-content-sha256", "multipart/related not parsed" |
| Wrong route mount (path / verb / host) | "mounted under prefix", "missing path-style", "host-based dispatch missing" |
| Real-cloud field omission | "field omitted on read", "deserializer rejected", "Status not persisted across Read" |
| Consumer-aware special-casing | "a consumer's naming convention", "image path contains", "downstream-specific branch" |

For each shape, count entries and list their bug IDs and fixing commits. A 3+ count across 2+ pull requests is a candidate for codification.

### Step 3 — cross-reference with existing skills

For each candidate shape, check whether a specialist skill already exists under `.claude/skills/`:

```bash
ls .claude/skills/
```

If a skill already exists → propose a refinement (new pattern to add to its scan) or a sibling skill.
If no skill exists → propose a new skill name and a one-line scope.

### Step 4 — write the audit report

Output a structured summary:

```
## Audit YYYY-MM-DD

Reviewed the resolved bugs BUG-NNNN through BUG-MMMM (X total), read from git history.

### Patterns above the codification threshold (3+ entries / 2+ pull requests)

1. **<Shape name>** — N entries across M pull requests
   Recurrence rate: <high / medium / low>
   Existing skill: <none / refine `<name>` / replaced by `<name>`>
   Recommendation: <propose new skill `<name>` / extend `<name>` to also scan for <new sub-pattern>>

2. ...

### Patterns approaching the threshold (2 entries)

- **<Shape name>** — 2 entries (BUG-X, BUG-Y); watch list.

### One-off shapes (1 entry, kept for reference)

- BUG-Z: <one-line>

### Recommended skill changes

1. Create `.claude/skills/<new-skill-name>/SKILL.md` with scope: <one-line>.
2. Extend `.claude/skills/<existing-skill>/SKILL.md` § Patterns to scan with: `<new grep / check>`.
```

### Step 5 — actually create or refine the skills

The audit isn't done until the codification step lands. For each "Recommended skill change," either:

- Spin up a `Skill` edit (extend an existing SKILL.md), or
- Write a new `.claude/skills/<name>/SKILL.md` (use existing skills like `silent-error-swallow-scan` as templates), and
- Record the new skill in the continuity files (`STATUS.md`, `DO_NEXT.md`) in the same commit.

## What "above the threshold" means concretely

A pattern is **above the codification threshold** when:

- **3 or more entries** with the same shape, **AND**
- **Spanning 2 or more pull requests** (so it's not just a one-time refactor cleanup), **AND**
- The shape has a **mechanical signature** (a grep pattern, a `git diff` shape, a SKILL.md check) that an automated scan can detect.

If the recurrence is real but the signature is fuzzy ("error messages are inconsistent across services"), the right output is a rule in `WHAT_WE_DID.md` (§ Fidelity rules that came from bugs) rather than a scan skill.

## Patterns already codified

- **Silent error swallow** — `silent-error-swallow-scan`.
- **Dead-code silencer** — `dead-code-silencer-scan`.
- **Time-anchored metadata in comments** — `timeless-comments` and the comment rules in `AGENTS.md`.
- **Sim-quirk in tests** — `sim-canonical-config-test`.
- **Emitted URL not serviced** — `sim-emitted-url-roundtrip`.
- **Streaming envelope not decoded** — `sim-streaming-body-handler`.
- **Wildcard route shadowing another service** — `mux-overlap-scan`.
- **Half-enumerated surface** — `surface-table-completeness`.
- **Flat row where the cloud has a lifecycle** — `sim-state-machine-completeness`.

## Related skills

- `silent-error-swallow-scan`, `dead-code-silencer-scan`, `sim-canonical-config-test`, `sim-emitted-url-roundtrip`, `sim-streaming-body-handler` — the codified outputs of prior audits.
- `avoid-vibe-slop` — the broader catalogue this skill feeds into.
- `sim-handler-checklist` — the pre-write checklist that pulls in several of the above as sub-checks.
