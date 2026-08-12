# WHAT WE DID

## 2026-08-12 — Second polish pass: App Service Stage 1, derivation tail, release-pipeline gate

Widened the Azure App Service surface from 161 to 238 of 692 served
operations: the sitecontainers slot twins resolve the slot's own records, the
site-scoped Logic Apps workflow surface (hostruntime bridge and the
WebApps workflow operations) mounts on the standalone Logic stores with
site-signed callback keys and a real resubmit LRO, the Key Vault
configuration references derive from the stored appsettings/connection
strings against the sim's own Key Vault, and four child-resource CRUD
families landed (publicCertificates with real DER parsing and SHA-1
thumbprints, domainOwnershipIdentifiers, premieraddons, pushsettings). Site
and slot deletion now purges the whole child subtree, which previously
survived deletion and leaked into a recreated site. SDK, CLI and Terraform
coverage in the same commit; the wire-shape validator reported zero
violations.

Closed the remaining locally actionable bug work. BUG-2928 closed by its own
criterion: the restarted local runtime ran the full Lambda invocation SDK
suite green. The BUG-2909 state-resolving tail closed — Amazon SQS
CancelMessageMoveTask resolves its task handle to the source queue, AWS
Cloud Map GetOperation resolves through the operation record, AWS CloudTrail
reads the ARN-valued ResourceId/ResourceIdList — raising derivation coverage
1,779 → 1,784 of 1,974 with each resolution pinned.

Gated the release pipeline against unparseable squash titles: PR #2's
non-conventional title made release-please report "no user facing commits"
and ship that pass in no release. scripts/check-pr-title.sh rejects any pull
request whose title release-please cannot parse, as the required
`PR title is a Conventional Commit` context (40 contexts, protection
verified matching).

## 2026-08-12 — Polish pass: release verified, protection gated, nightly fuzz hardened

Verified the v0.2.0 release end to end: binaries and console bundles on the
GitHub Release, and — after the stale sockerless-owned GHCR packages were
removed — re-shipped the tag so `ghcr.io/e6qu/sockerless-simulator-<cloud>`
exists as `:v0.2.0-amd64`/`-arm64` plus the unsuffixed OCI index, owned by
this repository and public.

Ported sockerless's required-status-check contract:
`.github/required-status-checks.txt` mirrors `main`'s branch protection
(38 contexts, strict, linear history), `scripts/check-required-status-checks.sh`
fails any change that leaves a required context unemittable (pre-commit +
build-gates), and ci.yml gained the `At most one PR open` and
`Branch rebased on origin/main` jobs.

Hardened the nightly fuzz. `run-fuzz.sh` requalifies the one benign failure
shape Go's fuzz coordinator produces when it races its own `-fuzztime`
shutdown — a single FAIL block whose only diagnostic is a bare
`context deadline exceeded` at or past the fuzztime budget, with no failing
input written and no new crasher on disk (golang.org/issue/72104 tracks Go's
own CI flaking on the same signature); every neighboring shape (crasher,
panic, early deadline, second FAIL block) still fails, proven by positive and
negative controls plus an end-to-end crasher run. The aws nightly group went
from two shards (13m48s / 11m41s against a 15-minute limit) to three,
restoring real headroom.

Closed the four locally actionable bugs in the same pass. The Cosmos DB
differential provisions its emulator end to end — image pulled when absent,
one OS-selected port handed to both `docker -p` and the emulator's `--port`,
every provisioning failure loud — removing all four tool-absent skips (BUG-2).
`ApplicationGateways_ListAvailableWafRuleSets` serves the complete managed
rule-set catalog (nine rule sets, 95 groups, 1,194 rules, vendored from
Microsoft's published enumeration cross-checked id-by-id against recorded
real-service responses; per-group counts locked by a unit test; SDK and CLI
coverage; the appgateway coverage floor moved 21 → 22) (BUG-2887). The three
simulator modules migrated from `github.com/docker/docker` to
`github.com/moby/moby/client` + `github.com/moby/moby/api`, clearing
GO-2026-5668 and GO-2026-4887 from every module graph with the shared
container-runtime suites green against the real daemon (BUG-2922, simulator
copy). The dead cross-cloud helpers left in each diverged `shared/` copy were
deleted per that copy's Linux deadcode findings and the deadcode gate now
covers `shared/` (BUG-1).

Continued the BUG-2909 IAM resource-derivation burn-down: Amazon Data
Firehose, AWS Security Token Service and Application Auto Scaling joined the
generated table, Amazon EventBridge gained the declared alias table its
Name/Rule abbreviations needed (also stopping the one-word prefix drop from
misdirecting Create/DescribeApiDestination at a connection resource), and
Amazon DynamoDB reads the export family's TableArn. Coverage rose 1,740 →
1,779 of 1,974 served operations; the ratchet floor holds the gain and the
remainder prose classifies all 195 still-underived operations.

Extracted the cloud simulators out of the sockerless monorepo into this
standalone repository. Flattened `simulators/*` to the repo root, renamed the
per-cloud directories to `simulator-{aws,gcp,azure}` (so `go install` produces
binaries with those names), folded each cloud's `shared/` module into its
cloud module as a package, and rewrote all module paths from
`github.com/sockerless/simulator*` to `github.com/e6qu/sockerless-cloud/*`.
Brought along: the sim console UI packages (+ `ui/packages/core`), the
vendored cloud API specs (`specs/cloud-api`, surface tables, behavioral
registries), the sim-scoped scripts and pre-commit hooks, the
Firecracker/realexec test harness, and the simulator jobs from CI (adapted
paths, workspace-based module resolution instead of `GOWORK=off`).
Fixed two errcheck violations in `testutil/registrytrust` that had never been
lint-gated in the monorepo.
