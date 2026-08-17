# WHAT WE DID

## 2026-08-17 — A sweep for tests that proved nothing

Every simulator was audited against a taxonomy of fake tests drawn from real
examples found here in the preceding days: assertions on wording that varies by
engine or platform, tests racing their own preconditions, calls whose responses
nothing checked, tests depending on state another suite created, skips that read
as passes, negative controls that could not fail, coverage inflated without
behaviour behind it, and tolerances too wide to fail. Each candidate was judged
by breaking the behaviour it names and watching whether it noticed, usually
through a build overlay so the tree was never modified.

The sweep found defects, not just weak tests. A Google Cloud service account's
sign-blob and sign-JSON-web-token operations returned a keyed hash labelled as
an RSA signature, under a per-process key that rotated on restart, so nothing
could verify it; the test checked that the result was base64 with two dots.
Those operations now sign with a persisted per-account RSA key whose public half
is published through the real key surface. Azure Container Instances ran
workloads on the host's architecture rather than the image's, hidden because the
test that forbade a hardcoded platform grepped two files by name and the
offending expression was in a third. Two Azure subnet child collections were
constants, hidden because both tests addressed a subnet that no test created.
Amazon CloudFront filtered distributions by neither connection mode nor anycast
list because it modelled neither field. An image export or import returned a
task identifier and stored nothing. A deleted web-application-firewall key was
still decodable from its own token, so deletion was unobservable.

Whole suites turned out never to have run. Five Terraform packages were absent
from both the makefile and the workflow, so they had never compiled anywhere; a
shell filter meant a security-group firewall test had never been built; two fuzz
targets had been spending the nightly budget on routes that do not exist; and a
576-line file containing no statements existed only to satisfy a gate that greps
added test lines, where a comment suffices. Wiring the Terraform packages up
immediately exposed two real defects in one of them. A new gate makes that class
of drift impossible to repeat, and the Azure Terraform harness — which had been
skipping its entire stack behind capabilities it was itself dropping — now runs.

The six bugs the sweep filed were closed in the same pass. Elastic Load
Balancing target health gained the three behaviours its checker still lacked: a
target group no listener rule forwards to reports its targets unused rather than
being checked at all, the configured matcher grades the response code and a
mismatch names it, and a deregistering target drains for the configured delay
instead of vanishing. Beside them an HTTPS health check was only a connection
attempt, so a target answering an error over HTTPS reported healthy.

The report that a simulator could start without a container client was wrong
about the mechanism, and disproving it found the real one: container mode
already refused a missing, hanging or unhealthy engine, all three verified
against the unfixed binary, while the reachable path was the process runtime the
engine-down message itself recommends. Taking that advice produced a simulator
that called itself healthy, accepted work and failed it later in the background.
Startup now refuses for any mode that executes workloads, and health no longer
claims a capability the process lacks.

AWS CodeBuild reads test results and coverage from the files the buildspec
declares, out of the build container; four formats are ingested and the seven
other documented ones are refused by name, so partial support is loud rather
than a fabrication. The CodeBuild command and the Glue Python job moved into
containers, which left the process substrate unreachable, so it is gone. An
asset's iterable forms are derived from the catalog table it names, so a surface
that could only ever answer an error can now succeed.

The identity-derivation floor fell from 1,788 to 1,687 because 101 operations
across five services were credited by table membership while absent from the
probe's own switch. The drop is the honest outcome, recorded in the floor's own
comment: no derivation was lost, the count stopped crediting derivation nobody
measured. The condition-key ratchet was the same shape — hand-written booleans
no code had to agree with — and probing them showed three keys never reach the
request path.

## 2026-08-17 — Continuous integration that fails only for reasons this branch caused

A publish is no longer cancelled by the next merge. The concurrency group was
the branch name, so every merge killed its predecessor; nine publishes were
cancelled and six commits sit on the default branch with no image in any
package. Publishes are keyed per commit, and retention moved to its own
workflow, because per-commit publishes overlap and two prunes racing each other
corrupt the count. Retention holds only prunable releases to the limit, since
versions coalesced onto immutable release tags cannot be deleted and counting
them made the limit unsatisfiable and monotonic; coalescing is per architecture,
which the rule now handles and a fixture proves.

Specification freshness holds a branch to what it changed rather than to
upstream's tip, with an unbaselined daily run so nothing rots unnoticed. Before
this a branch could fail three times in forty-two minutes for drift nobody
caused, one of them unsatisfiable locally because two edges served different
revisions of the same document.

Dependencies must be at least a day old before adoption: a release published
minutes ago has had no time to be yanked or flagged, and that delay is the
mitigation. A newer version inside the window is reported held rather than
drift, one past it still fails, and an unknown publication time fails loudly
rather than passing. Writing it surfaced two defects — an absent proxy timestamp
renders as the year one and would have cleared the window instantly, and the
Terraform section had never run at all, having globbed a filename this
repository does not use. The quarantine is deliberately not applied to vendored
specifications: it mitigates executing code we install, whereas a discovery
document is inert data our own suites validate.

## 2026-08-16 — App Service Environments, Kube Environments and detectors

An App Service Environment is a real placement scope rather than a stored
document. Its virtual-network reference must resolve to a subnet the simulator's
own network store holds, and a missing one is refused; its outbound address is
leased from the same public-address pool the network resources reserve from and
released on delete, while its inbound address is derived from the subnet's own
prefix, since Azure reserves a subnet's first four addresses. Its counts are
derived rather than stored — the multi-role count is the front-end pool's worker
count, Linux support appears only once a Linux plan is placed, and available
capacity is each pool's workers minus what the placed plans took. Suspending or
resuming an environment stops and starts the apps inside it, rebooting tears
down their workload containers, and a delete is refused while plans remain
unless it is forced. The environment is a private-link target too, so its
private-endpoint operations act on connections a real endpoint opened. Kube
Environments are served in full.

Five operations are deliberately unserved and say so on the wire rather than
answering a silent 404 inside a working resource: four metric-definition
operations, because a metric definition promises a series the simulator does not
emit, and the outbound network-dependency catalog, which is Microsoft-published
platform data of the same class as the runtime-stack catalogs this project has
declined to invent. The inbound half of that pair is served, computed from the
environment's own addresses, subnet and protocol switches.

Detectors compute from state the simulator actually holds. Site crashes read the
workload container's exit code, whether the kernel killed it for memory, and
whether it is dead; the memory and processor analyses read engine samples and
report a problem only on a real kernel kill or a non-zero throttling counter,
never against an invented threshold; the thread count reads the container's own
process table; and restart history comes from a new site event journal. Every
detector left unimplemented names the input it would need — service-health
incidents, swap history, a worker fleet, request or platform logs, Windows
counters — rather than being dismissed as a family. Fixed beside them: restarting
a web app was a no-op that reported success.

The surface moved from 545 to 616 of 692.

## 2026-08-16 — Twelfth polish pass: App Service backups that really round-trip, and a registry a real engine can log in to

An App Service backup writes a real archive. It builds a ZIP of the site's
deployed content beside the XML manifest Microsoft documents, writes both into
the Blob data plane of the account the request's storage URL names, and a
restore reads them back and replaces the file system — which is the documented
behaviour, since without a filter a restore deletes what is there and replaces
it with the backup's contents. That is also what makes the round trip provable:
the earlier attempt at this work failed because it tried to empty the site with
a deployment, and Web Deploy does not delete files a package omits. The merge
semantics are now asserted as a control of their own, and the restore is the
deleter. A second control deletes the archive through the Blob API and requires
the identical restore to fail, so a decorative round trip could not pass. The
surface moved from 519 to 545 of 692.

Three defects surfaced under it, each further from App Service than the last.
Web jobs were never removed when the script that defined them left the file
system. Every App Service plan reported the Dynamic tier because it was
hardcoded, so a plan created by the Terraform provider — which sends only the
SKU name — looked like Consumption and refused every backup configuration. And
a blob container was two separate objects: one created through Azure Resource
Manager, which is what the provider always does, was invisible at the account's
blob endpoint. Those are one object now.

The Amazon ECR registry accepts a real `docker login`. The engine's own login
endpoint negotiates TLS, so the registry is served over TLS by the HTTPS gateway
this repository already runs for its Terraform stacks, with the gateway's own
authority installed where the engine reads it per operation rather than once per
service lifetime. The login server keeps the real
`<account>.dkr.ecr.<region>.amazonaws.com` shape and differs only in the
coordinate it is reached at. A real push and pull round-trip through it, a wrong
password and a logged-out push are refused, and the whole exchange was exercised
on both container engines — including the one CI uses, through the code path CI
takes.

## 2026-08-15 — Eleventh polish pass: every registry authenticates, moves reach twenty-nine families, and the "flaky" tests were real bugs

Every container registry in the project now authenticates, each against its own
published contract rather than a copy of another cloud's. Amazon ECR answers
Basic — captured from a real registry as `Www-Authenticate: Basic
realm="…",service="ecr.amazonaws.com"` — with the whole authorization token as
the Basic parameter, and `GetAuthorizationToken`, which had returned a
constant, mints real material with the documented twelve-hour expiry. Google
Artifact Registry answers a Bearer challenge for an absent credential but 401
with no challenge for a rejected one, and `403 DENIED` naming the IAM
permission when an authenticated caller cannot reach a repository. Two things
were deliberately not built: ECR does not refuse a token used against another
registry, because AWS documents one token for every registry the principal can
reach; and Artifact Registry does not enforce token scope, because a token
minted for one repository was proved by experiment to serve another. Either
would have made a simulator stricter than the service it imitates. The Azure
registry's content stores became per-registry, so two registries no longer
share a repository name.

Cross-resource-group moves went from eleven type keys to twenty-nine, reaching
Microsoft.Network. What made that family possible is a general
inbound-reference repointing pass: every store a build creates is recorded, and
after a hook runs the mover rewrites both keys beneath the moved identifier and
any string naming it at an identifier boundary, so identifiers embedded in URLs
are caught too. Scanning every store beats a hand-listed set, which rots
silently. The types Azure itself refuses stay refused, pinned by tests at three
levels — partner registrations, private link services, application gateways,
NAT gateways, network profiles and virtual network taps are documented
unmovable, and private endpoints are conditional on the linked resource's type.

An AWS Lambda function's initialisation no longer eats its timeout. AWS
documents that Init ends when the runtime requests its first invocation and
that the timeout bounds Invoke; its own example shows a three-second function
reporting a duration of 3004.92 ms beside an init duration of 111.23 ms. The
invocation timer starts when the runtime asks for work, a ten-second Init limit
is enforced with the documented timeout report and a re-created execution
environment, and billed duration is derived by a formula that reproduces all
three published examples exactly.

A Google Compute Engine guest that never finished booting turned out to be
neither the recorded missing nested virtualisation nor a deadline too short.
Firecracker was launched with `--enable-pci`, and on aarch64 the guest never
receives the completion interrupt for its first block request — zero bytes were
ever read from the root filesystem while the vcpu spun. Raising the budget to
fifteen minutes produced no further console output, which settled by
measurement that it hangs rather than boots slowly. Removing the flag reaches
userspace in 31 seconds.

The Microsoft Entra Terraform surface works against the real `azuread`
provider through one coordinate, closing a bug whose premise had been false
since 2023: the provider has supported a Graph endpoint override all along. The
gap was read off the wire rather than guessed — the provider sends no
consistency header at all for these resources; what was missing was the whole
Graph `beta` endpoint it deliberately uses to work around documented v1.0
omissions, owner and member reference collections, the manager navigation
property, polymorphic directory objects, and round-tripping every property the
client writes.

App Service instances and processes are read from the live workload container —
the container is the instance and the engine's process table is the process
list — taking that surface from 503 to 519 of 692. Sixteen operations are
deliberately unserved with a demonstrated reason rather than zero-filled: the
engine exposes exactly one process-inspection primitive, which reports no
loaded modules and no dumps and can signal only the main process.

Three registry and data-plane authorities went in beside those. Amazon ECR
stopped creating repositories implicitly, refusing a repository it has no
record of the way the real service does while honouring the one documented
exception that makes the refusal correct rather than over-broad. Azure Cosmos
DB verifies the shared-key signature on every data-plane path through a
middleware a new route cannot skip, pinned against Microsoft's own published
encoding vector, with resource tokens refused rather than accepted unchecked
because their construction is not published. An AWS Lambda invocation reports
the memory it actually used, measured by polling the container engine, and
omits the figure entirely when the engine cannot supply one.

Google Compute Engine's insert became asynchronous, returning a running
operation and booting behind a context detached from the request, so a client
that gives up no longer destroys the machine it asked for; the operation reads
and lists that had been rendering invented completions and hardcoded empty sets
now read the record. A Logic Apps callback URL survives a resource-group move
byte-identical, and a move onto an occupied identifier is refused with the only
error shape any real failed move attests.

Two harness defects that had been quietly disabling coverage were also found by
measurement. The shared registry-trust helper was a no-op inside the Linux
harness, on the false premise that the engine already treated loopback
registries as insecure — it does not, and the engine reads its registry
configuration once per service lifetime while the harness pins that service for
its whole run, so trust is installed as a certificate authority through a path
the engine reads per operation, and the insecure path now fails loudly rather
than silently doing nothing. Separately, the simulator was handing the engine
bind-mount sources that the engine resolves on its own host rather than inside
the harness container, so workloads mounted empty directories; the harness now
shares one engine-host directory as the simulator's temporary root, at a
deliberately short path because a longer one overflows the Firecracker API
socket's address limit.

Finally, the test failures repeatedly dismissed as load-sensitive flakiness
were three real defects. All six suites built harness images under global tags
in one daemon, so concurrent suites clobbered each other mid-run and a test
failed on an image its own setup had built. A harness used plain `docker build`
on a driver that leaves the image in the build cache only, and appeared to work
solely because another suite populated the store. And `docker run --rm` leaks
its container when the test binary is killed, so a stale Cosmos emulator
starved every later run — removing one three-hour-old leak turned a repeated
280-second failure into a 13-second pass.

## 2026-08-15 — Tenth polish pass: registry and publish authentication, engine readiness, six more move families

Both simulator data planes that authenticated nobody now authenticate
everybody. Azure Event Grid's publish endpoint accepts an `aeg-sas-key` in a
header or query parameter, an `aeg-sas-token` or
`Authorization: SharedAccessSignature` verified as base64 HMAC-SHA256 over the
token's own `r=…&e=…` prefix under the base64-decoded key, or a Microsoft Entra
bearer for the `eventgrid.azure.net` audience, with `disableLocalAuth` — until
now a declared but inert property — leaving only the last. The signature is
Event Grid's own, not the Service Bus one beside it, which signs a different
string with a differently encoded key. The domain publish path, which used to
answer 404 against its own advertised endpoint because host resolution searched
only topics, routes each event to the domain topic its `topic` member names.

The Azure Container Registry data plane answers the Docker Bearer challenge and
verifies what comes back: `GET /oauth2/token` checks the admin Basic credential
and only while the admin user is enabled, `POST /oauth2/exchange` checks a
Microsoft Entra token for the `containerregistry.azure.net` audience, and
`POST /oauth2/token` checks the refresh token or password grant. Tokens are real
JWTs issued for one registry, and their `access` claims are checked against the
access record each request implies. Rotating an admin credential invalidates the
tokens derived from it. `podman login`, push and pull prove it end to end, and
the official SDK drives the documented challenge → exchange → token → retry
flow. The shared `/v2/` subtree gained a nil-able per-registry `Authorize` hook
rather than any cloud-aware branch, so the Amazon ECR and Google Artifact
Registry copies are byte-identical and unaffected.

Cross-resource-group moves went from five families to eleven — Event Hubs,
Azure Cache for Redis, Container Registry and Event Grid topics and domains
joined Web, Storage, Key Vault and Service Bus — each pinning the credential
material its resource ID derives, so a move never rotates a key. An Event Hubs
connection string captured before a move still sends and receives over AMQP
after it. The shipped Microsoft.Web hook had pinned nothing, silently rotating
every moved site's publishing password and its hosted workflows' access keys;
it pins them now, and the three hand-rolled pin loops became one shared helper
so a new family cannot forget the step.

Cloud Run v1's five replace methods enforce `metadata.resourceVersion` —
omitted is unconditional, stale is 409 ABORTED — coherently with the v2 etags,
since every v2 write bumps the generation the v1 spelling reports. Cloud
Storage records its long-running operations in the shared operation store
instead of inventing them, and `buckets.relocate`, which drained its request
body and reported a relocation it never performed, actually moves the bucket.
The Cloud Run executions fan-in switches on the verb and refuses one the
service does not publish.

An Amazon RDS instance no longer hands out connections its engine cannot serve.
The readiness probe had accepted any PostgreSQL `ErrorResponse` as proof of
life, and `FATAL: the database system is starting up` is an `ErrorResponse`, so
the gate opened as soon as the postmaster bound its port; it now classifies by
SQLSTATE the way `pg_isready` does. The adopt path taken after a restart had no
gate at all, and the 90-second engine budget destroyed the instance when it
expired — a real MySQL cold start measured 253 seconds under load, so a slow
host bricked a database. Both paths share one gate with a ten-minute budget
that fails fast on a dead container.

The AWS CodeBuild completion waits were a container-engine latency budget with
10 seconds of headroom against a measured p50 of 2.1 seconds; four concurrent
test processes failed 21 of 32 runs at exactly that ceiling. They share one
documented four-minute budget now, matching sibling waits in the same files.
The simulator itself adds a few hundred milliseconds and was not at fault.

The `simulator-aws/sdk-tests` module moved to the current aws-sdk-go-v2 service
modules (46 of them), verified by running the suite rather than by compiling.

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
