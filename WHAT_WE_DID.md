# WHAT WE DID

## 2026-08-14 — Ninth polish pass: the locally actionable bug sweep

Closed the four locally actionable bugs and recorded what closing them
found. The operations cancel method is served across every Google document
that publishes it: nine answer a completed operation with success and an
untouched record, because their own descriptions call completion a
documented outcome of cancelling, while Cloud SQL answers the failed
precondition its documentation shows. Every long-running operation in this
simulator is minted complete, so for eleven services that answer is the
only honest one — an invariant test now fails if that changes — and Cloud
Build, which runs real processes, really terminates a running build, proven
by removing the termination and watching the test hang. The Cloud Run v2
family mints and enforces etags everywhere the document declares them, with
the cancel-execution, start-instance and stop-instance requests decoded for
the first time.

Tags became one set per scope: a resource scope reads and writes the
resource's own tags through the pass-eight registry, a resource group
writes its own record, and the subscription and management-group scopes
keep the tags store as their only home. A scope holding no resource answers
404 as Azure Resource Manager does, the registry refuses at startup to
track a type whose stored form has no settable tags member, and the move
dispatch's separate tag re-homing became dead code and went.

Microsoft.ServiceBus became the fifth family that moves between resource
groups, chosen over Event Grid because it has a real SAS-authenticated data
plane to prove the claim: the namespace record and nine child stores
re-key, both shared-access slots of every rule are pinned across the move,
and a connection string captured before the move still receives the message
enqueued before it. The Event Grid half could not be proved because its
data plane authenticates nothing at all, which is now BUG-9.

## 2026-08-14 — Seventh polish pass: Cloud Run v1 complete, Key Vault moves

Completed the Cloud Run v1 surface, 100 to 152 of 152 served spellings, as
projections over the state the v2 surface already owns rather than a second
bookkeeping layer: Knative jobs, executions, tasks, instances and worker
pools read and write the same records the v2 API serves, the operations wait
alias and the jobs IAM read fill the last strays, and the new collections
honour labelSelector, limit and continue. The job execution engine was
lifted to package scope in its own step so both API versions run and cancel
through one implementation — a v1 run really starts containers and a v1
cancel really stops them, pinned by a test that watches the container's own
output stop rather than the record change. Instance start and stop flip
conditions exactly as the v2 surface does, because no execution exists there
on either version and inventing one would be a fidelity regression. Two real
defects surfaced with the work: the v2 colon-verb fan-in ran the job on
setIamPolicy, and RunJobRequest's overrides and validateOnly were silently
ignored; both are fixed with regressions.

Cross-resource-group moves gained Microsoft.KeyVault, chosen by surveying
five candidate families' store layouts: vaults keep two resource-id-keyed
stores while the whole data plane keys on the vault name, so the move
re-keys the record and its private endpoint connections and touches nothing
else. An RSA key created before a move still decrypts pre-move ciphertext
after it — an implementation that re-derived material from the resource id
could not pass that. The survey also recorded why Microsoft.Network is the
wrong family to move next: its resources reference each other by resource
id, so moving one without re-pointing every referrer would silently break
the fabric.

## 2026-08-13 — Sixth polish pass: App Service Stage 5 and the move-hook table

Raised the web-arm surface 426 to 503 of 692 by completing the networking
families. The classic virtualNetworkConnections spelling extends the same
real fabric the swift path established: a connection resolves its subnet
against the Microsoft.Network stores and genuinely attaches the site's
containers to the network — proven by a test that reaches a VNet-only
resource from the site — and deleting it really disconnects; the two
spellings describe one integration and agree. Private access, network
features assembled from real connection state, site private endpoint
connections and private link resources, and the App Service Plan tail
(vnets and routes, gateways, hybrid connections and their keys, capabilities,
SKUs, instance details) all answer from real stores, empty only where the
simulator genuinely hosts none of the thing.

Cross-resource-group moves gained the dispatch BUG-3 asked for: a
per-provider hook table that Microsoft.Resources walks, with the existing
Microsoft.Web logic as its first entries and Microsoft.Storage as the first
new family. A moved storage account keeps its access keys (the derived
material is pinned across the move rather than silently rotating), every
resource-ID-keyed ARM projection re-keys, the data planes stay readable
because they key on the account name a move never changes, and tags now
follow every hooked move as real ARM does. BUG-3 stays open for the
remaining providers.

## 2026-08-13 — Fifth polish pass: App Service Stage 4

Raised the web-arm surface 384 to 426 of 692. Microsoft.Web certificates
parse the real PFX, PEM or DER payload — thumbprint, subject, issuer, SANs
and validity all derived from the certificate itself, wrong passwords
refused — and Key Vault-sourced certificates resolve against the sim's own
vaults with the real keyVaultSecretStatus values; the secret blobs are
write-only through the persistence sidecar. Site and slot certificates share
the parsing. Custom-hostname analysis answers from real CNAME/TXT/A lookups
against the sim's DNS record sets, and the global hostname lists assemble
from the real binding stores. Every site-config write records a snapshot
that recover restores exactly. Container logs serve the site container's
real retained output, zip spelling included. Resource moves are real:
sites relocate with their entire child subtree, plans re-point every
referencing site, and the previously fake move test (it "moved" a
nonexistent resource against a no-op handler) now proves relocation.
Provider and global singletons answer truthfully — operations catalog from
what the sim serves, validation against the real stores, empty collections
only where the sim genuinely hosts none of the thing. Provider_*Stacks
stays unserved with the vendored-catalog reason recorded beside the floor;
BUG-3 records the cross-provider move gap. Official SDK, native az CLI and
Terraform (real PFX certificate + DNS-backed hostname + SNI binding)
exercise the surface with the wire-shape validator at zero violations.

## 2026-08-13 — Fourth polish pass: App Service Stage 3

Raised the web-arm surface 307 to 384 of 692. The Functions key surface is
load-bearing: durable host/master/function key stores mint at site, slot and
function creation, the admin token is a real HS256 JWT signed with the
site's master key, and POST /api/function enforces the real authLevel
contract — anonymous keyless, function accepting function/host/master keys
via x-functions-key or ?code=, admin master-only, bare 401 otherwise — while
container sites without function configs stay keyless as the real platform
proxies them. WebJobs materialize from deployed packages exactly as the real
platform's Kudu channel does and run as real containers with the site's own
image and settings; histories record actual exit-derived status, continuous
jobs honor WEBJOBS_STOPPED, and a simulator restart settles persisted
Running jobs at PendingRestart. MSDeploy and OneDeploy fetch the package
over HTTP, unzip and persist the content durably, discover webjobs, and
report real provisioning transitions through Azure-AsyncOperation LROs with
409 on concurrent deployments; publishing-password regeneration rotates what
the credentials list actually returns. Official SDK and native az CLI flows
cover every cluster with the wire-shape validator at zero violations.

## 2026-08-13 — Third polish pass: BUG-2924 implemented, Static Web Apps complete

Implemented BUG-2924's approved design: every VPC network takes its Docker
bridge subnet from a host-side allocator over 10.213.0.0/16 (per-VPC /24
slices, live networks as the only ledger so restarts cannot double-allocate,
dead-run reclaim unchanged), and the ENI address is genuinely on the
workload's interface — an ephemeral NET_ADMIN netns-join container adds it
as a secondary address, while the workload keeps its capability-free
sandbox, matching real Amazon ECS. Two live VPCs sharing a CIDR now coexist,
holding even identical ENI addresses, with intra-VPC reachability on the ENI
address and cross-VPC isolation pinned end to end. The audit surfaced and
fixed a real cross-VPC defect: the Elastic Load Balancing target lookup
keyed on the bare ENI address and now scopes by VPC.

Completed the Static Web Apps family — all 75 StaticSites operations —
raising the web-arm floor 238 → 307 of 692: builds, both app-settings bags
at site and build scope, secrets and API-key rotation, users and roles,
custom domains validated truthfully against the sim's Azure DNS records,
basic auth, database connections, linked backends resolved against the real
site/app stores, user-provided function apps linking existing Microsoft.Web
sites, private endpoint connections, zip-deployment LROs, detach and
workflow preview. Official SDK, az CLI (native az staticwebapp flow through
az cloud update against the TLS sim), and Terraform in the same change; the
wire-shape validator reported zero violations.

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
