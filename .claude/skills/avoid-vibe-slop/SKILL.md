---
name: avoid-vibe-slop
description: Project-local checklist that loads before any non-trivial code change in sockerless. Anchored in the project rules in AGENTS.md; refuses fake/fallback/anemic patterns. Use proactively whenever about to write Go or TypeScript code, modify a handler, add a test, or stage a fix.
---

# Avoid vibe-coding slop

Sockerless is a vibe-coded project with explicit countermeasures. Read the project rules in `AGENTS.md` for the full catalogue. This skill is the **runtime checklist**: a small set of questions to answer *before each substantial edit*.

## When this skill applies

- Before writing or modifying a Go file under `simulator-*/`, `realexec/`, `ui-auth/`, or `testutil/`.
- Before writing or modifying a TypeScript file under `ui/packages/*/src/`.
- Before adding a test.
- Before staging a "fix" for a bug.
- NOT for: trivial typo / comment-only edits, state-save updates, markdown-only docs.

## The checklist

Stop after each "no" and resolve it before writing code.

### Truth and adaptor fidelity

1. **Has someone already implemented this in the repo?** Grep the surface for the function/path/type name. If yes, extend that — never re-implement (pattern 11, 13).
2. **What is the reference adaptor for this code path?** Docker SDK, gh CLI, aws CLI, gcloud, az, Terraform provider. If you can't name it, you don't know if the change is right (pattern 22, 29).
3. **Does the adaptor's real behaviour confirm what I'm about to write?** If your only evidence is "model says so," verify the wire shape with `curl -v` / `--debug` / `Wireshark` / the upstream spec (pattern 6, 30).
4. **If you're adding a "fallback" branch — is it actually a fallback, or is it lying about success?** Patterns 1, 7, 9 are the same shape: silent success when the truth is missing. Default answer: return an error, never fabricate.
5. **Marking a resolver / handler as "unreachable in practice"?** Flag it for re-audit if you ever make the parent collection non-empty — the placeholder will start firing for real. `unreachableFieldErr` is a temporary contract, not a permanent one (pattern 30 lineage).

### Plan and root cause

6. **Is this the right fix, or the quick fix?** If you'd be embarrassed to explain it in a code review, it's the quick fix (pattern 19).
7. **Have you read at least one nearby function for the surrounding pattern?** If no, you'll fight the codebase's conventions and lose (pattern 13).
8. **If the fix involves stacking guards** (`if x != nil && x.Y != "" && len(x.Z) > 0 ...`) — what's the root cause? Five-deep conditionals hide the real bug (pattern 5).
9. **Refactoring to delegate via self-dispatch?** Audit every defensive layer in the original — post-filters, error normalisers, fallback paths often live as silent backstops that the delegate doesn't preserve. The asymmetry is invisible to compilers + linters (pattern 34).
10. **Bulk text rewrite across many files?** Use language-aware tools — `gofmt -r`, `goimports`, `go/ast` visitors, `tree-sitter` queries. Sed / regex on code can join lines, eat required args, leave orphaned tokens. If you must use sed, run `go build ./...` AND `go test ./...` immediately after — don't trust visual inspection of the diff (pattern 35).

### Tests and fidelity

11. **Are you adding a test?** It must drive the real adaptor — not a mock (pattern 2, 29). For sockerless this means: `docker` CLI / `gh` CLI / `aws` CLI / SDK clients, against a running binary, not a struct mocked-out in a unit test.
12. **Is the test derived from spec, or from the implementation?** If you wrote the assertion by reading the code you just wrote, you're testing yourself, not the contract (pattern 3, 28).
13. **Does the test assert on implementation metadata?** Bug IDs in error strings, phase numbers, internal IDs, version strings — these break the moment you clean up the metadata. Re-derive the assertion from the contract: what the error *means*, not the bug-tracking artifact (pattern 28).
14. **Coverage % is not the goal.** Mutation-killed % is. A 95%-covered branch with one assert that everything returns non-nil is a lie (pattern 2).
15. **Did you sweep test fixtures for the same strings you just cleaned from production code?** A grep on the production code's stripped substring will find anchored tests before CI does (pattern 28).

### Comments, abstraction, pruning

16. **Default: write no comments.** Add one only when the *why* would surprise a reader (a hidden constraint, a subtle invariant, a workaround for a specific bug). Restating signatures is forbidden (pattern 8).
17. **Are you about to add a factory / adapter / provider / manager for a single call-site?** Stop. Three similar lines is better than premature abstraction (pattern 14).
18. **Did this change DELETE code you no longer need?** AI is an expansion engine — it adds, it rarely prunes. Look for dead helpers, retired branches, stale shims. A change that only adds lines is feeding pattern 27.
19. **If you changed code-behaviour, did you sync the docs?** READMEs, env-var tables, error-message contracts in `specs/`, comment blocks describing the code you just rewrote. Wrong docs are worse than missing docs because they confidently mislead operators (pattern 33).

### Dependencies

20. **Is this package real?** Before `go get <name>` or `bun add <name>`, confirm upstream existence via the official registry (pkg.go.dev / npmjs.com / pypi.org). Slopsquatted package names are weaponised malware (pattern 4).
21. **Is this package current?** The pre-push `check-latest-deps` hook will flag drift, but proactive upgrades beat reactive flag-fixing (pattern 21).

### Destructive actions

22. **Are you about to run `rm -rf`, `git push --force`, `terraform destroy`, drop a DB, or modify shared infrastructure?** Default: ask first. The agent-deleted-production-DB stories in the destructive-command rule in `AGENTS.md` are why.

### Context, commit, and re-verification discipline

23. **Did the conversation just compact?** Re-read STATUS.md + DO_NEXT.md + the last 2 commits before continuing. Context-amnesia silently rewrites prior decisions (pattern 17).
24. **Did you just claim something works?** Did you actually run it? "Works on my machine" without an `$ ` shell prompt and real output in the message is suspicious.
25. **After `git commit` reports success, did you run `git log --oneline -1` to verify the SHA actually advanced?** Pre-commit hook auto-fixes can roll back a commit silently. "Hook output passed" ≠ "commit landed" (pattern 31).
26. **First-pass review is sycophantic-by-default.** When closing a BUG or finishing a substantial change, plan an explicit re-verification pass with fresh eyes — typically surfaces new bugs the first pass rubber-stamped (patterns 24, 32). Budget the time; don't skip it.
27. **Did a Makefile / script start background services?** A zero exit code or PID file is not evidence. Verify with the repo's status target and at least one real HTTP / CLI request after the parent command has returned. Background-process bugs often hide until the launching shell exits.

## Failure modes to recognise in your own output

Stop and rewrite if you catch yourself producing any of these:

- "This *should* work" — claim without test.
- "I've added comprehensive error handling" — followed by `try { ... } catch (e) { console.log(e) }`.
- "Let me add a fallback for now" — pattern 9, always.
- Adding `// TODO: fix this properly later` — Phase 158 says NO; the right fix goes in this commit or it gets staged as a new phase entry in PLAN.md.
- "Backward-compatibility shim" in code that isn't released or has no users — pattern 8.
- Tests with `assert.NotNil(x)` as the only assertion.
- 47 files for a one-call-site change — pattern 14.
- "Looks good to me" on your own work after a single pass — pattern 24 (sycophancy). Re-read with fresh eyes; first-pass review trusts the wrong things.
- A diff that's only additions, no deletions — pattern 27. AI rarely prunes; force the pruning audit.
- Assertions on bug IDs / phase numbers / internal IDs in error strings — pattern 28. Re-derive from the contract.
- Sed / regex on Go source spanning multiple files — pattern 35. Run `go build && go test` immediately; visual inspection misses joined lines + eaten args.
- Code change merged without docs change — pattern 33. If the behaviour changed, the README, env-var table, and adjacent comment block need to change with it.
- Stack / dev-server target says "up" but no post-start status + request probe was run — Q27. Start scripts lie when child processes die with the parent shell.
- Commit message says "passed" but you didn't run `git log` afterward — pattern 31. The hook may have rolled it back.

## Sockerless-specific invariants (load-bearing; don't violate)

- Components decoupled from admin / UI. No admin-required env vars on components.
- Backend ↔ host primitive must match (ECS in ECS, Lambda in Lambda, etc.).
- Persistence is opt-in + fail-loud (`log.Fatalf` on open failure).
- Test target gating: `SOCKERLESS_TEST_TARGET=sim|cloud` is mandatory.
- specs/CLOUD_RESOURCE_MAPPING.md is authoritative for cloud-mapping decisions.
- `gh` CLI is the reference adaptor for bleephub; HTTPS-only; `--hostname` is the wiring flag.
- Never auto-merge PRs; user merges every one.

## Make the type system catch what discipline misses

When a bug class (like BUG-991/BUG-992 — "handler read Store directly instead of `s.self.X`") could be enforced at compile time, prefer the type-system fix over a comment / lint rule. The doc `docs/GOLANG_STRONG_TYPING.md` catalogues 15 approaches with cost/risk per option. Three patterns specifically protect against vibe-coding regressions:

- **`var _ Interface = (*Impl)(nil)`** — every implementor of an interface in `backends/core/` has this satisfaction proof. If the agent drops a method, build fails. (Approach 8.)
- **Sealed interfaces + `gochecksumtype`** — for sum types like `core.PodSpec` variants; missing a case in a switch is a build failure. (Approach 10.)
- **Typed IDs** — `ContainerARN`, `TaskID`, `LambdaFunctionName` as distinct Go types so the compiler rejects ARN-where-task-name-was-expected at call-sites. (Approach 1.)

When you add a new sum-type-shaped enum or interface to this repo, reach for these first. When you're tempted to use `any` / `interface{}` / `map[string]any` outside `api/types_gen.go`, that's a flag — see `forbidigo` rule candidates in the same doc.

## Output

When this skill fires, restate the 1–2 checklist items most relevant to the current change. Don't dump the whole list — there are 26 items and reciting them is itself a sycophancy trap. Pick the items that match the kind of change you're about to make:

- New handler / refactor of an existing handler → Q1, Q2, Q9 (delegation safety nets).
- Bulk rewrite across many files → Q10 (language-aware tools), Q15 (sweep tests too), Q18 (delete something).
- New test → Q11–14 (real adaptor, spec not impl, no metadata in assertions).
- Behaviour change → Q19 (sync docs).
- Closing a BUG → Q26 (explicit re-verification pass).
- After commit → Q25 (verify SHA advanced).
- Make / process lifecycle fix → Q24 + Q27 (real status and request probe after start).

Then proceed — or stop and ask if a "no" surfaced.
