---
name: timeless-comments
description: Specialist scan for comments that should not exist or should not read the way they do — restatements of the code beneath them, section banners, narration, verbose justification, passive voice, and time-anchored history ("previously", "now", phase numbers, BUG IDs). A comment must earn its place by explaining WHY, in active voice, as briefly as the fact allows. Use before merging any Go / TypeScript / Markdown change that adds or edits comments or docs.
---

# Comment scan — earn the line, then cut it

Default to no comment. Write one only when the code cannot say the thing itself, then cut it to the shortest form that still carries the fact. One line beats five; a clause beats a paragraph. Verbose commentary goes stale faster than code and buries the few comments worth reading.

Useful comments explain a load-bearing invariant — a hidden constraint, a subtle property of the surrounding code, a workaround whose root cause is non-obvious, or a behavioural choice that would surprise the reader without an explanation. The reader is a future engineer (you, six months from now, or a teammate who never saw the diff) looking at the code in isolation. They don't have the PR description, the BUGS.md entry, the conversation history, or the prior versions of the file.

Time-anchored comments break this contract. They orient the reader to the diff, not to the code's standing invariants. The PR description, the commit message, BUGS.md, and `git log` already capture the history. The comment in the file should not.

## When this skill applies

- Before merging any change that adds or edits a comment, in any language.
- Periodically across the codebase to catch drift — comments age faster than code, and a comment that was timely at write-time becomes stale or misleading as the surrounding code evolves.
- Whenever you find yourself writing "now", "previously", "earlier", "before this commit", "after the fix", "used to", "in Phase X" or similar — pause and rewrite.

Skip for: commit messages, PR descriptions, BUGS.md, STATUS.md, WHAT_WE_DID.md, CHANGELOG entries — these are explicitly historical artifacts.

## The patterns to scan

### Pattern F — comments that say what the code already says

Delete outright, no rewrite:

- **Restatement.** `// RequestID returns the request ID from the context` above `func RequestID(ctx) string`. If every content word already appears in the identifier or the signature, the comment is noise.
- **Section banners.** `// ---- Types ----`, `// ===== Amazon Glue =====`, rules of dashes, headings repeating the file name. Structure belongs to the file, not to a comment.
- **Narration.** `// loop over the items`, `// return the result`, `// Create bucket` above `handleCreateBucket`.
- **Justification and apology.** Why a choice was reasonable belongs in the commit message.

```bash
# Banner lines and rules
rg -n '^\s*//\s*[-=~*_+#]{4,}\s*$' --type-add 'src:*.{go,ts,tsx}' --type src
rg -n '^\s*//\s*[-=~*_]{2,}\s*\w' --type src

# Single-line doc comments directly above the symbol they name
rg -n -B0 -A1 '^// [A-Za-z_][A-Za-z0-9_]* (is|are|returns|holds|reports|represents|defines|wraps|stores) ' --type src
```

Nothing requires a Go doc comment here: `staticcheck` runs with ST1020/ST1021/ST1022 off, so an exported symbol whose name says everything gets none.

### Pattern G — passive voice

Write comments, documentation, tutorials and READMEs in **active voice, imperative mood**. Name the actor; the actor is usually the fact worth recording.

| Passive | Active |
|---|---|
| the payload is read from the host file | read the payload from the host file |
| `softDeleted=True` is sent by gcloud | gcloud sends `softDeleted=True` |
| this is called when the bucket is deleted | the delete path calls this |
| the entries are seeded at creation | seed the entries at creation |

```bash
rg -n '^\s*//.*\b(is|are|was|were|be|been|being)\s+[a-z]+(ed|en)\b' --type src
rg -n '\b(is|are|was|were)\s+[a-z]+ed\s+by\b' -g '*.md'
```

Some hits are legitimate — "the field is deprecated", "the value is encoded as base64" describe a state, not a hidden actor. Rewrite the ones that hide who acts.

### Pattern A — history words

Words that anchor a comment to a point in time:

```bash
# Direct evolution language
rg -nC1 -i '// .*\b(previously|earlier|now |before this|after this|formerly|historically|used to|legacy of|prior to|in (phase|bug|PR) [0-9])\b' \
  --type-add 'src:*.{go,ts,tsx,js,jsx,py,rs}' \
  --type src \
  -g '!*_test.go' -g '!vendor/' -g '!node_modules/'

# Multi-line docblocks with evolution language
rg -nC2 -i '\b(was|were)\s+(broken|missing|incorrect|wrong|deprecated|replaced|removed)\b' \
  --type src
```

### Pattern B — phase / bug / PR references in code

The project's `feedback_no_phase_mentions` memory codifies this — keep phase/bug/PR metadata in commits/PRs/BUGS.md only. Inline references rot when phases close.

```bash
# Phase / BUG / PR / issue references in code comments
rg -nC1 '// .*\b(Phase\s+\d+|BUG-\d+|PR\s*#\d+|issue\s*#\d+|#\d{2,})\b' \
  --type src \
  -g '!*_test.go'

# Test files: allowed for regression-check naming, but only if the
# test's NAME or a single-line REGRESSION FOR comment cites it.
# Long narrative comments in tests citing bugs are still violations.
```

### Pattern C — "the fix" / "the patch" framing

The fix-narration shape — comments that orient around the diff that introduced them rather than the current behaviour:

```bash
rg -nC1 -i '// .*\b(the fix|the patch|this fix|this patch|fixed by|hotfix|workaround for)\b' \
  --type src
```

### Pattern D — comparative framing

Comments that explain the code by contrast with a prior shape:

```bash
rg -nC2 -i '// .*\b(instead of|as opposed to|rather than|whereas|unlike (the )?(previous|earlier|old))\b' \
  --type src
```

`instead of` and `rather than` are sometimes legitimate when comparing two CURRENT options ("use openStreamingBody rather than io.ReadAll(r.Body)") — keep those. Flag the variants that compare against a removed/superseded shape.

### Pattern E — date / version anchors

```bash
rg -nC1 -i '// .*\b(202[0-9]-[01][0-9]|q[1-4] 202[0-9]|since version|as of [a-z]+ 202)\b' --type src
```

External vendor dates ("Cloud Run SDK 1.x", "go-azure-sdk v0.20240515") are fine — those describe an external dependency's pinned version. Internal dates ("added 2026-05-24") are not.

## How to classify each finding

For each hit:

1. **Bug (rewrite it).** The comment narrates the change instead of the standing invariant. Rewrite to describe what the code does TODAY and why a reader needs to know.
2. **Legitimate regression-check pointer (optional but allowed).** A one-line comment of the form `// Regression check for X.` where X is a specific behavioural property (NOT a BUG number). Example: `// Regression check: empty body must 400, not silently produce empty struct.` Keep these — they document the test's intent.
3. **External dependency version anchor.** Comments referencing a third-party SDK / API version are fine. Example: `// aws-sdk-go-v2 ≥ 1.30 sets Content-Encoding: gzip on Invoke.`
4. **History doc / CHANGELOG / memory file.** Skip — those are explicit history artefacts.

## How to fix

### Before (time-anchored)

```go
// Earlier, the `Vault` + `Name` fields carried `json:"-"`, so every
// store round-trip dropped them. Restart with SIM_PERSIST=true
// silently emptied every List response. This commit splits storage
// into wrapper types kvSecretStored / kvKeyStored / kvCertStored
// that embed the wire shape and carry Vault+Name as exported fields.
type kvSecretStored struct { ... }
```

### After (timeless, and cut to the fact)

```go
// Export Vault and Name so sim.Store's JSON round-trip keeps them; they stay
// off the wire because the embedded KeyVaultSecret does not carry them.
type kvSecretStored struct { ... }
```

Two lines, active voice, and every word carries something the code cannot. The role of the type is already in its name.

### Before (phase / bug anchor)

```go
// BUG-1115 caught this: r.URL.Scheme is empty on the sim's mux,
// so the previous inline path produced `://host/...`. Use the
// shared helper.
id := buildKVURL(r, vault, "secrets", name, version)
```

### After (timeless)

```go
// r.URL.Scheme is empty behind the mux, so buildKVURL hard-codes https://.
id := buildKVURL(r, vault, "secrets", name, version)
```

### Before (comparative against removed shape)

```go
// Previously this used `touch /tmp/sockerless-done` which raced
// the polling loop. Now we use SIGTERM via ContainerStop instead.
runSmokeExec(t, ctx, resp.ID, ..., "step-2")
```

### After (timeless)

```go
// Signal from outside the container: an exec torn down from within cannot
// report its exit status. ContainerStop → SIGTERM → the container's trap.
runSmokeExec(t, ctx, resp.ID, ..., "step-2")
```

## Verification commands

Run a clean-pass sweep before commit:

```bash
# Should produce zero output on a clean tree.
rg -n '// .*\b(previously|earlier|now |before this|after this|formerly|historically|used to|in (phase|bug|PR) [0-9])\b' \
  --type-add 'src:*.{go,ts,tsx}' --type src \
  -g '!*_test.go' -g '!vendor/' -g '!node_modules/'

# Phase / BUG references in code (excluding *.md docs):
rg -n '\b(Phase\s+\d+|BUG-\d+)\b' --type-add 'src:*.{go,ts,tsx}' --type src \
  -g '!vendor/'
```

Any output is a candidate for the rewrite shapes above.

## What stays — the rubric

A comment earns its line if:

0. **The code cannot say it.** Ask what a reader still could not work out from the identifier, the signature and the body. If the answer is "nothing", delete the comment rather than rewriting it.

1. **It explains a CURRENT invariant** — a property that holds in the code right now and would surprise a reader without explanation.
2. **It doesn't depend on the diff that introduced it** — removable from version control history without losing meaning.
3. **It survives all reasonable future refactors of the surrounding code** — describes WHY the code is shaped this way, in terms that don't reference the specific code that was here before.
4. **It cites only external, persistent facts** — SDK behaviour, protocol specs, cloud-provider docs, physical constraints — not internal evolution.

5. **It is as short as the fact allows, in active voice.** Cut every clause that restates the code, justifies the choice, or hides the actor.

If a comment fails rule 0, delete it. If it fails any other, rewrite it.

## Related skills

- `silent-error-swallow-scan` — sister skill for a different code-shape pattern.
- `dead-code-silencer-scan` — sister skill for unused-symbol silencers.
- `avoid-vibe-slop` — broader code-quality checklist; this skill specialises one of its sub-rules.
