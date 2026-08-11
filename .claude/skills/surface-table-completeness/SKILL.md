---
name: surface-table-completeness
description: Enforce the full-enumeration rule before claiming any sim surface "fixed" — community-filed issues against closed operation tables (S3 subresources, KV data plane, Pub/Sub verbs, Lambda subresources, Azure storage data planes) get half-fixed when only the user-named ops land; the rest reopen as follow-up issues with the same shape. This skill keeps every PR honest by requiring a corresponding row-by-row check against `specs/SIM_SURFACE_TABLES/<surface>.md`. Distilled from BUG-1145 — the meta-shape behind issue #196 → #201, #190 → #134 → #1142, and #193 → #1135 → #1143.
---

# Surface-table completeness invariant

## The class of bug

Community-filed issues name a subset of a finite, well-documented operation table:

- *"S3 subresources are missing"* → user names `?uploads`, `?tagging`, `x-amz-copy-source`. The fix lands those. Two weeks later: *"`?versioning`, `?lifecycle`, `?cors`, `?policy` all 409 too."* Same surface, different rows.
- *"Pub/Sub `:patch` missing"* → user names subscription patch. Fix lands. Later: *"topic patch missing too."*
- *"KV WWW-Authenticate missing"* → user names the challenge response. Fix lands the response. Later: *"the URL inside the response panics the SDK parser."* Same surface, different row (the URL format vs. the response presence).

Each "later" is a reopen, a re-file, or an adjacent issue. The fix-and-reopen loop costs real time and shipping latency. Worse: every reopen erodes the *"every gap is a real bug with a real fix"* invariant because *the previous fix wasn't actually for the whole gap.*

## When this skill applies

Apply it when ANY of these are true in the current change:

1. The work touches a file under `simulator-<cloud>/<service>.go` AND adds, removes, or modifies a handler.
2. The work claims to "close" a community-filed issue against a service surface.
3. The work is part of a phase whose PLAN.md / BUGS.md entry names a service surface.
4. A reopen of a previously "fixed" issue lands in the user's queue.

Do **not** apply this skill to bugfixes that don't touch a service surface (test refactors, lint fixes, docs, CI wiring). The full-enumeration rule has a cost; spending it on changes that don't touch a closed operation table is overhead.

## The rule

**Before declaring a surface fixed, the corresponding table in `specs/SIM_SURFACE_TABLES/<surface>.md` must be up-to-date and have no silent `✗` rows.**

Silent ✗ means: the row exists in the table, the op is not implemented, AND there is no corresponding deferred sub-task in PLAN.md or BUG entry in BUGS.md. A row that's ✗ with an explicit deferral pointer is fine — that's *visible* incomplete coverage, not silent.

## How to apply

### When fixing a community-filed issue against a service surface

1. **Identify the surface.** Is there a table in `specs/SIM_SURFACE_TABLES/<surface>.md` already? If yes, read it. If no, this is the moment to create one: enumerate every canonical op from the cloud provider's REST documentation as the first commit of the fix branch.
2. **Mark the user-named rows.** Verify they're ✗ in the table; if they're ✓, the issue is a different shape (regression, wire-quirk, etc.) — apply a different skill.
3. **Look at the user-named rows' siblings.** For each row the user named, ask: *what's the symmetric DELETE? what's the GET? what's the LIST? what's the variant under a different query param?* The reopen risk is in those siblings.
4. **Fix the user-named rows AND every reasonable sibling in the same PR.** "Reasonable" = same handler shape, same dispatcher, same store; landing them together is cheaper than two PRs.
5. **For siblings that ARE bigger** (need new infrastructure, different protocol shape, real-cloud-only quirks), stage them forward: a deferred sub-task in PLAN.md with a BUG number, or a 501 NotImplemented stub that surfaces the gap on the wire.
6. **Update the table** with the new statuses *in the same PR*. Don't claim "fixed" until the table reflects the fix.

### When the table doesn't exist yet

The first instance of a reopen / scope-miss against a surface is the moment to add it. Don't pre-populate every surface in the codebase — tables earn their keep by being the answer to a specific class of miss; pre-emptive population becomes stale documentation the moment a cloud provider adds a new op.

When you add a new table, follow the schema in `specs/SIM_SURFACE_TABLES/README.md`:

```
| Operation | Verb + path | sim handler | sdk-test | tf-test | notes |
|---|---|---|---|---|---|
```

Each row must point at a real file/line/test function — generic ✓ marks without a pointer are vibe-slop.

### When auditing a PR

Run this check per surface touched:

```bash
# Surface tables in scope:
git diff --name-only origin/main...HEAD | grep -E "simulator-.+/[^/]+\.go$" | xargs -I{} basename {} .go | sort -u

# For each surface, find its table and read it:
ls specs/SIM_SURFACE_TABLES/ | grep -E "<cloud>-<service>"
```

For each row in each table:

- **✓ rows:** verify the pointer file/line still exists and the linked test still runs.
- **✗ rows:** check PLAN.md / BUGS.md mentions them by section name or BUG number.
- **501 rows:** check the sim returns the canonical NotImplemented shape.

A silent ✗ row (no PLAN.md / BUG reference) is a finding — file it as a BUG before merging.

## Refused shortcuts

- *"The user only asked for X, so we only need to ship X."* — Read the table; the rest of the row was always part of the contract, the user just hadn't run into it yet.
- *"That row is rare in practice, skip the entry."* — Then mark it ✗ with a "low-priority, deferred until <X>" note. **Don't omit the row.** The table is the canonical enumeration; missing rows defeat the point.
- *"The table is just docs; the code already enumerates the ops."* — The code enumerates *implemented* ops. The table enumerates *every* op, implemented or not. They're different sets.

## Companion skills

- `sim-canonical-config-test` — every row's `sdk-test` column should reference a canonical-SDK-client test, not a raw `net/http` workaround.
- `reopen-postmortem` — every reopen surfaces here too: the postmortem asks *which row of which table was missing*, and the answer goes into both the BUG entry and the table itself.
- `sim-handler-checklist` — the per-handler pre-write checklist; this skill is the per-surface pre-PR checklist.

## Example

When PR #200 closed BUG-1138 (AWS S3 multipart + object-tagging + CopyObject), the table `specs/SIM_SURFACE_TABLES/aws-s3-bucket-subresources.md` did not exist yet. Phase 177 adds it, with every bucket-level row populated. The user-named ops are ✓; siblings the user didn't name but the table now flags as ✗ (replication round-trip test, logging tf-test, etc.) get explicit "deferred under <BUG-N>" notes. Issue #201's class of miss — *partial coverage shipped as fixed* — cannot recur because the table is now load-bearing.
