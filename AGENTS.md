# Agent Guidelines — sockerless-cloud

> `CLAUDE.md` is a symlink to this file. Edit `AGENTS.md`.

This repository contains the Sockerless cloud simulators — local, wire-faithful
reimplementations of the AWS, Google Cloud, and Microsoft Azure API slices that
sockerless depends on — together with their SDK, CLI, and Terraform test
suites, their embedded console UIs, and the vendored API specifications they
are validated against. The sockerless backends live in the
[sockerless](https://github.com/e6qu/sockerless) repository and consume the
simulators from here via `go install github.com/e6qu/sockerless-cloud/simulator-<cloud>@version`.

## Continuity files — read before, update after, write timeless and in past tense

The continuity files are **`STATUS.md`, `PLAN.md`, `DO_NEXT.md`, `WHAT_WE_DID.md`, `BUGS.md`** (and only these — never invent new continuity docs). They are the project's memory across sessions and compactions. Treat them as a first-class deliverable of every task.

**Before starting a task:** read `STATUS.md` and `DO_NEXT.md`. If they disagree with `git status`/the actual branch, fix them first — a stale continuity file is a bug.

**After finishing a task (in the same commit as the code):** update the continuity files together with the code and tests. Write them in the past tense, describing the merged end state — never a session diary. Never create a PR that contains only continuity-file edits.

## No stubs. No fakes. No mocks. No synthetic behavior. Ever.

This is the single most important rule. Every piece of code in this repository must do real work or not exist. Stubs and fakes are bugs — not shortcuts, not placeholders. If you are tempted to stub something out, stop and ask the user instead.

- **Simulators**: every API endpoint must behave like the real cloud service. If the real API returns labels, the simulator returns labels. If the real API tracks execution state, the simulator tracks execution state.
- **Tests**: tests run against the real simulator binaries through the real SDK / CLI / Terraform provider. No mock objects, no fake HTTP responses.
- **CI**: suites exercise real API flows end-to-end. If a test can't work without a feature, implement the feature — don't mock around it.

## Simulators are real implementations

The simulators are **local reimplementations** of cloud services, not mocks. They run real logic: jobs run, functions execute, timeouts fire, logs are produced — driven by the same cloud-native config the real services honor. No synthetic timers, hardcoded delays, or fake completion signals; if a cloud service has no native timeout, neither does the simulator. Every field the real API returns, the simulator returns.

Always ask "How does the real cloud service behave?" and implement that — use the cloud's own configuration knobs, never simulator-specific env vars or shortcuts.

### Simulator architecture — cloud-slice principle

1. **The simulator is a cloud slice.** `simulator-aws/` implements the subset of AWS's real public API surface that sockerless depends on, at cloud-API fidelity. It is *not* an emulation of a single product — there is no "Lambda simulator" or "ECS simulator" in isolation.
2. **One simulator binary per cloud.** All AWS service slices live in `simulator-aws/` (single Go module, one `simulator-aws` binary, one shared `sim.Server` mux). Adding a new service slice = a new `registerX(srv)` call + handler file in the existing per-cloud binary. Never a new binary per product.
3. **Cloud-API fidelity.** Match the real cloud's error shapes, response headers, async operation semantics, path templates, and HTTP status codes exactly. When the cloud's contract doesn't cover something, neither does the simulator.

**How to add a new slice:**
1. Read the cloud's public API reference for the service.
2. Create `simulator-<cloud>/<service>.go` with handlers matching the cloud's endpoints, error codes, and response shapes.
3. Call `register<Service>(srv)` so the new slice mounts on the shared mux.
4. Add SDK + CLI + Terraform tests per the testing contract below — the pre-commit hook enforces this.

**What "cloud-API fidelity" rules out:**
- Stdout-as-response shortcuts, in-memory TODO placeholders, embedded third-party local emulators (the simulator IS the cloud from the client's perspective).
- Synthetic disambiguation (custom headers, custom env vars) that real cloud clients wouldn't produce.
- **Any sockerless-aware or runner-aware special-casing.** The sim must be faithful to the real cloud and provide *no* special functionality on top to make a sockerless backend or a CI-runner harness work. If it can't be done through faithful cloud APIs, find the real cloud primitive that does.

**What it does allow:**
- Ephemeral sidecar listeners as long as the container-facing contract matches the cloud.
- Docker user-defined networks as the implementation mechanism behind Cloud Map / Cloud DNS / Private DNS.

### Simulator fidelity — testing contract

Every simulator endpoint must be exercisable via all three real-world client surfaces, in the same commit that registers the endpoint:

1. **SDK** — the official cloud SDK for Go. Tests live in `simulator-<cloud>/sdk-tests/`.
2. **CLI** — the vendor CLI (`aws`, `gcloud`, `az`) shelled out via `runCLI`. Tests in `simulator-<cloud>/cli-tests/`.
3. **Terraform** — the official provider resource that wraps the endpoint. Tests in `simulator-<cloud>/terraform-tests/`.

The pre-commit hook `scripts/check-simulator-tests.sh` blocks any commit that adds a `r.Register("OpName", …)` line without touching at least one file in the three test dirs that references the operation. Endpoints that genuinely aren't exposed via SDK/CLI/terraform go on `simulator-<cloud>/tests-exempt.txt`.

There is no "just land it and add tests later." If you edit a simulator, the tests ship with it.

### A sim test differs from a cloud test ONLY in coordinates

A client talking to a simulator must use the **same code and the same identifiers** it uses against the real cloud, differing **only in coordinates** — the endpoint URL(s) and credentials. **Never** add an `if sim` branch, a sim-only env var, or any sim-aware behaviour to test code. Such a special case is a *fake test*: it proves the sim-special path works, not that the real client path does.

### A simulator console UI differs from a real-cloud console ONLY in coordinates

The console SPAs (`ui/packages/simulator-*`) read **only real cloud APIs** at a configured base URL, and federate operator credentials through the **cloud's own federation primitive** (Google Cloud Workforce Identity Federation, AWS `AssumeRoleWithWebIdentity`, Microsoft Entra). No `if sim` branch, no sim-only data-plane endpoint, no fallback. The console's Shauth SSO layer is the console's own, not the simulator's.

## All synthetic behavior is a bug

Any fake, synthetic, hardcoded, or placeholder behavior is a **bug**, not a feature or acceptable shortcut. If the real implementation is not feasible today, file a bug in `BUGS.md` and track it. When you encounter synthetic behavior in the codebase, treat it as a bug to fix, not as intended behavior to preserve.

## Module layout — installable per-cloud modules

- `simulator-aws/`, `simulator-gcp/`, `simulator-azure/` are separate root Go modules (`github.com/e6qu/sockerless-cloud/simulator-<cloud>`), each containing its own `shared/` framework package. They carry **no `replace` directives** so `go install …@version` always works; the sibling support modules (`realexec/`, `ui-auth/`, `testutil/`) are required at tagged versions (`realexec/vX.Y.Z` subdirectory tags) and resolved locally through the repo-root `go.work` during development.
- The sdk/cli/terraform test modules are never installed and keep relative `replace` directives.
- When a support module changes, tag it and bump the requiring modules **in the same PR**.

### Committed console dist

`simulator-<cloud>/dist/` (the built console SPA that `//go:embed all:dist` bakes into the binary) is **committed**, so `go install` from a version tag ships the console without needing bun. After any `ui/packages/simulator-*` change, rebuild and re-embed (`cd ui && bun install && bunx turbo run build --no-daemon; make simulator-<cloud>/embed`) and commit the regenerated `dist/` in the same PR.

## Always fix CI failures and test failures

If CI fails or tests fail, fix the issue — even if the failure is "pre-existing" and not caused by the current change. We do not tolerate broken CI on any branch.

## Never ignore or work around a pre-commit / pre-push failure

A hook failure — even one that looks incidental — is flagging a real problem. **Fix the underlying problem the hook points at.** Never bypass it: no `--no-verify`, no commenting the hook out, no narrowing its scope. If you believe a hook is genuinely wrong, **stop and ask the user**.

## Always bundle dependency updates into the open PR

When `scripts/check-latest-deps.sh` reports drift, **upgrade the drifted modules and commit them into the pull request already in flight** — without asking. Run `make upgrade-deps` in each drifted module, re-run the check, build and test every upgraded module, and run the SDK test suites when a cloud SDK moved.

## Never create more than one PR — one branch, one PR

All work goes on a single branch and a single PR. Never open a second PR while one is open. Enforced by `scripts/check-single-open-pr.sh`.

## Never dismiss a problem as "unrelated"

Any problem you notice gets one of two outcomes: **fix it on the spot** (strongly preferred), or file it in `BUGS.md` (area, symptom, suspected cause, fix shape). Noticing it and moving on is forbidden. "Pre-existing", "not caused by my change", "not my job" are not exits.

## Never merge PRs

Create PRs with `gh pr create`. Never run `gh pr merge`. The user handles all merges.

## Branch hygiene

Before pushing a PR branch, always rebase it on top of `origin/main`; after rebasing and pushing, sync local `main` with `origin/main`. Remote repository state is authoritative.

## No bug IDs in code comments

Code comments describe intent and behavior — never which bug prompted them. Bug tracking belongs in `BUGS.md`.

## Use proper, fully-qualified service and feature names

Call every cloud service, product, and feature by its **real, fully-qualified name** in prose. Real API operation names, SDK types, wire fields, CLI command names, and package names stay verbatim as the cloud's SDK/CLI/API spells them — these are the contract.

## No skip-if-absent tests

Never write tests that `t.Skip` when an external tool is "not installed". The dependency is either required — install it in `TestMain` so the test runs unconditionally — or it isn't, and the test should not exist. The one narrow exception is a capability the *host kernel* cannot provide (e.g. Linux-only `CAP_NET_ADMIN` network-namespace tests on macOS), gated on `runtime.GOOS` or `realexec.DetectNetworkCapabilities().Require()`.

## No fallbacks. No degraded modes. No silent deferrals.

If a dependency is required, it is required. Fallbacks create two code paths and two sets of bugs. If you think a fallback is needed, **ask the user**. When given a task, implement it fully — do not silently skip, defer, or stub out parts of the work.
