---
name: backpedal-pattern-audit
description: Meta-skill that surfaces patterns from BUGS.md "Resolved history" where the same shape was filed and fixed N times across M phases. Each repeat group is a candidate for a new specialist skill or a class-of-bug rule. Run periodically (every ~10 PRs or before starting a major phase) or when the user asks "what blind spots do we have." Distilled from the observation that BUGS 1016/1017/1018/1019/1025/1033 (silent-error swallow) and BUGS 1020/1021/1022/1024/1034 (dead-code silencer) recurred so often they justified dedicated `silent-error-swallow-scan` and `dead-code-silencer-scan` skills.
---

# Backpedal-pattern audit

This is the meta-skill. Specialist scan skills (`silent-error-swallow-scan`, `dead-code-silencer-scan`, `sim-canonical-config-test`, `sim-emitted-url-roundtrip`, `sim-streaming-body-handler`) catch *known* recurring patterns. This skill catches the recurring patterns we haven't yet codified — the ones BUGS.md is in the middle of filing for the 3rd or 4th time.

The signal: when the same kind of bug shape has been filed and fixed 3+ times across 2+ phases, the *category* itself has become a known weakness. That's the inflection point where it deserves a dedicated scan rather than catching each instance one-at-a-time.

## When this skill applies

- Every ~10 merged PRs (rough cadence — when WHAT_WE_DID.md grows by 5+ phase entries).
- Before starting a major phase (so the phase plan can include preemptive scans for known recurrences).
- When the user asks "what blind spots do we have" or "where do we backpedal a lot."
- When BUGS.md "Resolved history" grows past 100 entries since the last audit.

Skip for: per-bug fix work (use the per-pattern specialist skill instead), routine commits, doc-only changes.

## The process

### Step 1 — load BUGS.md resolved history

```bash
# Read the resolved-history section of BUGS.md
sed -n '/## Resolved history/,/^## /p' BUGS.md \
  | rg -n '^- \*\*\d+\*\*' \
  | head -200
```

Each entry begins with `- **BUG-NNNN** (Phase NN, ...) — <one-liner>`.

### Step 2 — group by bug-fix shape

For each entry, extract the shape signature. Heuristics for shape extraction (no exact algorithm — pattern-match on the one-liner):

| Shape signature | Cue keywords |
|---|---|
| Silent error swallow | "silently swallowed", "ignored error", "_ = json.Unmarshal", "dropped err", "fell through to fallback" |
| Dead-code silencer | "//nolint:unused", "var _ =", "kept for diagnostics", "reserved for future", "consumers ship in subsequent commits" |
| Phase metadata in comments | "Phase N", "BUG-NNNN" in code, "lineage header", "implementation-coupled metadata" |
| Test asserted on phase / metadata | "asserts on Phase metadata", "test docstring", "implementation-metadata in test" |
| Sim-quirk-in-test (this audit's catalyst) | "BaseEndpoint = baseURL", "UsePathStyle = true to match sim", "test rewritten to match" |
| Emitted-URL not serviced | "advertised endpoint", "selfLink without round-trip", "URL emitted but no handler" |
| Streaming-envelope not decoded | "aws-chunked", "STREAMING-", "x-amz-content-sha256", "multipart/related not parsed" |
| Wrong route mount (path / verb / host) | "mounted under prefix", "missing path-style", "host-based dispatch missing" |
| Real-cloud field omission | "field omitted on read", "deserializer rejected", "Status not persisted across Read" |
| Cross-cloud cross-pollution | "ECS in Lambda", "Path B fallback", "host primitive mismatch" |

For each shape, count entries and list their bug IDs + phases. A 3+ count across 2+ phases is a candidate for codification.

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

Reviewed BUGS.md entries from BUG-NNNN through BUG-MMMM (X total).

### Patterns above the codification threshold (3+ entries / 2+ phases)

1. **<Shape name>** — N entries across M phases
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
- Mention the new skill in the next phase's continuity docs (`PLAN.md`, `STATUS.md`, `DO_NEXT.md`).

## What "above the threshold" means concretely

A pattern is **above the codification threshold** when:

- **3 or more entries** with the same shape, **AND**
- **Spanning 2 or more phases** (so it's not just a one-time refactor cleanup), **AND**
- The shape has a **mechanical signature** (a grep pattern, a `git diff` shape, a SKILL.md check) that an automated scan can detect.

If the recurrence is real but the signature is fuzzy ("error messages are inconsistent across services"), the right output is a class-of-bug rule in `BUGS.md § Class-of-bug rules` rather than a scan skill.

## Known patterns this audit found (as of 2026-05-23)

### Above threshold — already codified

1. **Silent error swallow** — 6 entries (BUGS 1016, 1017, 1018, 1019, 1025, 1033) across Phases 164.
   Codified as `silent-error-swallow-scan`.

2. **Dead-code silencer** — 6 entries (BUGS 1020, 1021, 1022, 1023, 1024, 1034) across Phases 164–165.
   Codified as `dead-code-silencer-scan`.

3. **Phase metadata in code / test comments** — 5+ entries (BUGS 994, 1014, 1015, 1026, 1036) across Phases 161–165.
   Already documented in BUGS.md § Class-of-bug rules; absorbed into `avoid-vibe-slop`. No standalone skill needed.

### Above threshold — codified by Phase 173.0

4. **Sim-quirk in tests** — 1+ explicit entry (BUG-1098 / 1104) but high blast radius (every sim's `sdk-tests/` is a candidate).
   Codified as `sim-canonical-config-test`.

5. **Emitted-URL not serviced** — 3 entries (BUG-1029 missing handler routes, BUG-1044 url.PathEscape, BUG-1103 advertised-but-404).
   Codified as `sim-emitted-url-roundtrip`.

6. **Streaming-envelope handling** — 1 explicit entry (BUG-1099) but predicted recurrence on every future upload-handler add (Blob, GCS, Service Bus messaging).
   Codified as `sim-streaming-body-handler` (preemptive).

### Approaching threshold (2 entries) — watch list

- **Cross-cloud cross-pollution / Path B fallback** — BUG-1046, BUG-1054. Covered by the existing class-of-bug rule "Backend ↔ host primitive must match"; no skill yet because the rule + Phase 168 cleanup is recent and the fix shape is well-understood.

### One-offs to keep in mind

- BUG-1075 — live-cloud validation deferred. Not a recurring pattern; a long-term track.
- BUG-944 / 987 — doc-only fix where the cloud rejected the documented config. Covered by class-of-bug rule "Doc-only fixes are unsafe when the cloud rejects the config."

## Related skills

- `silent-error-swallow-scan`, `dead-code-silencer-scan`, `sim-canonical-config-test`, `sim-emitted-url-roundtrip`, `sim-streaming-body-handler` — the codified outputs of prior audits.
- `avoid-vibe-slop` — the broader catalogue this skill feeds into.
- `sim-handler-checklist` — the pre-write checklist that pulls in several of the above as sub-checks.
