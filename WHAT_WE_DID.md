# WHAT WE DID

## 2026-08-31, forty-third pass — the declared tails, served

The three slices were carried toward their declared totals one document at a
time, and the numbers are the record: Google Cloud 5,363 → 5,440 of 5,480
Discovery method spellings, with `compute-v1` at 1,976 of 2,016 and
`dataflow-v1b3`, `cloudrun-v2`, `firestore-v1`, `spanner-v1`, `cloudkms-v1` and
`redis-v1` each complete; Azure 2,521 → 2,566 of 2,628 Swagger operations, with
App Service at 661 of 692.

Two of the pass's own instruments were wrong and were corrected before the work
they measured. The Google Cloud coverage probe rendered a greedy
`{+parentResource}` label as a single path segment, reporting whole collections
unserved that were mounted and answering; it now substitutes a sibling method's
shape for a shapeless label, and a unit test pins the predicate so the fix
cannot widen again. The Azure coverage report named only its totals, so the
next operation to serve could be read off nothing but a deliberately broken
floor; it now names every unserved operation and the address it was probed at.

App Service recommendations went in at all three scopes — subscription, App
Service Environment and site — as 13 of their 15 operations. What each answers
follows from the simulator running no advisory engine: the lists and the
histories are empty, because nothing has been observed about any site and an
invented advisory would be worse than none; the filters are the client's own
decisions and are recorded against the scope until they are reset. The two
rule-detail reads answer a declared 501, because a `RecommendationRule` is
Microsoft's published advisory copy — display name, portal message, blade link
— which this project does not vendor.

Whether an app can be cloned went in the same way, computed rather than
declared: App Service clones an app only from a Premium or Isolated plan, so
the plan the site is placed on decides the result, and a deployment slot — which
a clone does not copy — makes the result partial. A slot asked directly is
answered for by its production site's plan, because a slot carries none of its
own. The six ResourceHealthMetadata spellings beside it declare a 501: the
operation defines its category as the one the resource matches in Microsoft's
Resource Health Check policy file, and matching a site against a policy this
project does not vendor would be fabrication.

Four Compute Engine verbs recorded a machine state without moving the machine,
and all four now move it. `instances.startWithEncryptionKey` set `RUNNING` in
the store and booted nothing; `suspend` and `resume` did the same in both
directions; and `instances.bulkInsert` wrote a whole run of instances with no
network interface, no disks and no boot, then called them running. Linux CI
caught the first — it reads an instance's status back from the machine — and
macOS never could, because those tests are capability-skipped there.
`bulkInsert` was invisible even on Linux, because its own test was the one in
the family that was not capability-gated; it now builds every member of the run
through the same function `instances.insert` builds one with, boots each machine
behind the operation the caller polls, and its test is gated like the rest, so
a real host proves the run is three attached, running machines rather than three
records shaped like them.

That last distinction also retired a claim this repository had been making
about itself. `DO_NEXT.md` said nothing in the App Service tail was
implementable from what the simulator can observe; recommendations disproved
it, and the item now records the corrected split, naming the families that have
not yet been examined rather than filing them under a blanket refusal.

## 2026-08-30, forty-second pass — the project describes itself

Sockerless Cloud read as a component of the repository that consumes it: the
README opened by defining the simulators as what "the Sockerless cloud backends
consume", a column of the front table named the backends each simulator
"serves", and the per-cloud READMEs called each simulator the "upstream" for a
named backend. It is the other way round — the simulators are a general-purpose
reimplementation of the clouds, and what is built on them is downstream.

The framing is gone, along with every link into the other repository. What
replaced it is what the project actually is: anything that speaks a cloud's API
can be pointed at a simulator — the cloud's SDKs, its official CLI, its
Terraform provider, and the libraries built on those, boto among them — and each
surface is validated against that cloud's published specification in the format
the cloud publishes it, Smithy for AWS, Discovery for Google Cloud, OpenAPI for
Azure. Which services a slice covers is this project's choice; nothing
downstream defines it.

Three links pointed at another repository's copy of skills this repository owns,
and now point at its own. Historical issue references keep the fact and drop the
dead cross-repository URL.

The README carries a copyright and licence notice: copyright retained by Adrian
Mârza and by each contributor to the extent of their contributions, under
AGPL-3.0-or-later, with the licence text as distributed, the upstream text and
the SPDX identifier all linked. Beside it is the vendored material — the Smithy,
Discovery and OpenAPI snapshots the simulators are validated against — with each
corpus's own licence and its traceability: every `SOURCES.md` row records the
local file, the upstream repository or host, the exact upstream path, the
licence, the pinned revision and the fetch time, so any vendored byte can be
traced to the published document it came from.

Six dangling links left over from the extraction are repaired, five of them
documentation of things this repository does not contain: `make/README.md`
described `components.mk`, `stack.mk` and an `observability-config/` directory
that came from the other repository, and the Caddyfile row named a stack target
that does not exist here rather than the Terraform and CLI harnesses that
actually start the gateway.

## 2026-08-30, forty-first pass — the surface tables show the whole surface

The tables listed only routes whose path was a single string literal. A
registration that composes its path — `"GET "+prefix+"/projects/{project}/…"`,
the shape every surface served under two version prefixes uses — produced no
row at all, so the tables carried 4,044 of 5,041 registered operations and
fifteen surfaces had no table whatsoever: Azure networking, Azure Service Bus,
Azure Event Hubs, Azure DNS, Azure App Service plans, the Key Vault managed-HSM
tail, Amazon CloudFront and its function/key/policy resources among them.

`scripts/classify-sim-handlers.go` resolves the route now, substituting the
literals a caller passes for a prefix parameter and reading package-level and
function-local constants, and the seeder builds its rows from that rather than
from a regular expression over the source. The tables gained 997 rows, a
quarter more surface, and every status the legend declares now actually
appears — including the 501 on Azure Resource Manager's generic provider path,
which had been unreachable because those four registrations name their path
through a local constant.

Each of the fifteen new tables carries a coverage-matrix row written against
test files that were checked, not inferred. Three surfaces looked uncovered
under a filename search and were not: Azure App Service plans are exercised
through `Microsoft.Web/serverfarms`, public DNS through
`Microsoft.Network/dnsZones`. Searching by resource type rather than by file
name found them — the same false-absence the tooling itself was being fixed to
stop reporting.

The sweep's own findings were then acted on rather than filed. Azure's
virtual-machine extension surface reached no Terraform client, so the Azure
stack declares an `azurerm_virtual_machine_extension` and Terraform now reaches
`virtualMachines/{vm}/extensions/{name}` through PUT, GET and DELETE. The patch
surface — `assessPatches`, `installPatches` and `capture` — had no test on any
client, and now has one that asserts on what the guest produced: the package
counts its own package manager reported, an installation that honoured
never-reboot, and a capture that names the disk it copied.

A machine's disk now outlives the guest process that runs it. Firecracker builds
its root filesystem inside a working directory it removes when the guest stops,
so a stopped machine had no disk — and that made `VirtualMachines_Capture`
unreachable by any order of calls, because Azure generalizes only a stopped
machine and captures only a generalized one. Generalizing first destroyed the
disk the capture needed; capturing first was refused for want of
generalization. The disk is copied to a path derived from the resource id
before the guest is stopped, the capture reads it there and quiesces the guest
only when one is running, and a deleted machine discards it while a deallocated
one keeps it — which is what deallocation means in Azure.

Three claims in the first draft of those matrix rows were wrong and are
corrected. `compute_network_request_validation_test.go` was cited for all three
surfaces and exercises none of them; the extension surface is covered by
`compute_vm_guest_operations_test.go` and listing by location by
`compute_vm_operations_test.go`. Reading a guest-gated test's `ok` as a pass is
what produced the wrong citation — every one of these skips on a host without
the kernel capability, and `go test` reports a skipped package as `ok`.

`classify-sim-handlers.go` also resolves a constant defined from another
constant now. `const extPath = armBase + "/virtualMachines/…"` is the shape ARM
paths are written in, and treating only string literals as constants reported
the extension surface at one route of five.

## 2026-08-29, fortieth pass — Google monitoring reaches its own authenticator

Production acceptance reached `GET /monitoring/observation` with the exact
deployment credential configured in both Shauth and the Google Cloud
simulator, but received Google's `UNAUTHENTICATED` envelope saying the token
was not a JWT. The values matched; the authentication boundaries did not.

The Google Cloud simulator wraps its complete published route table with the
same access-token verifier that protects its cloud data plane. Because the
monitoring route is published on that table, the verifier tried to parse the
independent monitoring bearer as a simulator-minted Google JWT and rejected it
before the monitoring handler could compare its own token digest. AWS and
Microsoft Azure do not place that verifier in front of the route, which is why
their observations worked with identical infrastructure wiring.

The Google verifier now exempts the shared canonical monitoring path exactly
as it exempts the console's independent session boundary. The monitoring
handler still owns its constant-time bearer check. The final-handler regression
proves a valid monitoring credential returns `e6qu.monitoring/v2`, a wrong one
returns the monitoring realm's 401 challenge, and the valid monitoring
credential remains rejected by a real Cloud Run API route.

## 2026-08-29, thirty-ninth pass — a deployment cannot complete while it is still failing

An Amazon ECS deployment could report its rollout COMPLETED while none of its
tasks ran, which silently disarmed the deployment circuit breaker.

A task whose essential container has already exited reads RUNNING until the
watcher observes the exit. That window satisfied every completion condition, so
the rollout latched COMPLETED on a task that was about to die. Completion is
sticky by design — a completed deployment that later loses a task does not
restart its rollout — and `ecsRecordServiceTaskFailure` ignores a rollout that
is not IN_PROGRESS. Together those made the latch permanent: the circuit breaker
stopped counting mid-deployment, never reached its threshold, and never rolled
back. A deployment the scheduler still holds launch failures for no longer
completes.

The steady-state window is honoured now whatever the second boundary. It is
judged against `startedAt`, which the Amazon ECS wire format carries in Unix
seconds, so it truncates: a task that really started at 10.999 records 10, and
comparing elapsed time against the window alone cleared a task a millisecond
after it started whenever the start landed late in its second. A
second-resolution stamp only proves the window elapsed once the window plus one
second has, so that is what the scheduler requires, and the wake-up it arms
lands on the same instant.

Only a circuit breaker makes the failure count meaningful, so only a breaker
gates completion on it. The count is what counts to a threshold, and
`ecsResetServiceFailureCountAfterHealthyTask` clears it only when a breaker is
enabled — gating on the count unconditionally would hold a breakerless service
that recovered from one launch failure IN_PROGRESS for good, which
`TestAmazonECSServiceReleasesItsVPCNetworkAcrossSimulatorRestart_SDK` caught.

The same change carries the dependency refresh the freshness gate asked for: 53
Go modules across the AWS, Azure and Google SDK test suites, and the `azurerm`
Terraform provider from 5.2.0 to 5.3.0 in both stacks.

Every client the tests drive is the newest published build. The AWS CLI and
gcloud already fetched theirs unversioned, and `hashicorp/setup-terraform` takes
the newest Terraform, but the step named "Install Azure CLI" installed nothing —
its whole body was `az version`, so the Azure suites ran against whatever build
the runner image baked in, which lags Microsoft's releases. It installs the
newest now, as the other two do.

Google Cloud also gained Cloud SQL blue-green deployments, which the scheduled
specification refresh pulled onto this branch: create, get, list, delete and the
switchover verb, on both the v1 and v1beta4 spellings. All ten are served,
taking Cloud SQL Admin from 150 to 160 method spellings on each. The green
instance is a real instance in the store every other Cloud SQL read serves;
switchover promotes it into the source's name and retires the source under a
name `deleteOldSource` can delete. The generated Go client carries no
`BlueGreenDeployment` type yet, so its test drives the same authenticated
transport directly.

`make upgrade-deps` no longer upgrades this repository's own modules. A release
pins them by commit, so they are required at a pseudo-version, and `@latest`
prefers any semver tag over one — including the deleted bootstrap `v0.1.0` tags,
which the module proxy still serves. The refresh walked `ui-auth` backwards to a
revision predating the fields the simulators call, and all three simulators
stopped compiling. The upgrade skips them now and says so per module.

`TestAmazonECSServiceDeploymentFailureStateSurvivesSimulatorRestart_SDK` failed
about one run in four — on CI and locally — and passes 12 of 12. The truncation
carries its own unit test, checked against a negative control: the first version
of that test passed on the unfixed code, which made it worth nothing.

## 2026-08-29, thirty-seventh pass — the freshness check reads the filter it sends

Every vendored specification is in sync with upstream: AWS 41 Smithy models and
their service references, Azure 120 Swagger documents, Google Cloud 30 Discovery
documents, all measured at zero drift. AWS and Azure had reported 42 and 120
documents behind; 5 of the 162 were real.

`scripts/check-spec-freshness.sh` asked GitHub for
`repos/<repo>/commits?srcpath=<path>`. The REST API names that parameter `path`
and ignores a key it does not know, so every query dropped its filter and
answered with the repository's branch tip. Each file then compared its pin
against a commit that had never touched it — drift for all but the one file the
tip happened to change. Re-vendoring could not clear it either: the fetcher
correctly pins the last commit touching the file, so the next run reported the
same drift against the same tip. Both numbers stood still while the daily run
refetched 162 documents at the revisions they already held.

`scripts/check-gh-api-params.sh` now refuses a `gh api` query parameter GitHub
does not define, in pre-commit and in CI. The mistake arrived as a rename that
reached inside a URL, and its class is worse than a wrong answer: an unknown key
is accepted, so the script exits cleanly on a result that means nothing.

The four AWS models genuinely behind — Amazon EC2, Amazon ECS, Amazon RDS,
Amazon CloudWatch Logs — and the Amazon EC2 service reference are re-vendored.
The Amazon EC2 model brought one operation with it,
`ReplaceImageInstanceTypeSpecification`, which sets or removes the instance type
specification on an AMI. It is served: the specification is stored against the
image, `DescribeImages` reports it, and `RunInstances` enforces it in the order
the API documents — no specification allows everything, an unsupported entry
excludes its matches, a supported list requires one, and a `t3.*` entry matches
the family. Storing it without enforcing it would report a restriction the
simulator does not apply. Amazon EC2 stays fully covered, at 801 of 801.

## 2026-08-29, thirty-eighth pass — a served count now has to name its method

The coverage probe reads any handler answer as served, so a route that owns a
subtree answers for a sibling collection nobody implemented and the sibling
counts as covered. Cloud Storage's per-object ACLs were the known case.
`TestServiceConformance_GCPNoPhantomCoverage` closes the class across every
Google document: it asks `http.ServeMux.Handler` which pattern actually matched
the rendering the probe judged served, and holds that pattern to the literal
segments of the method's Discovery path. Google routes on those segments, so a
pattern missing one did not route the method.

The sweep found six, all now served. Compute Engine's `backendServices`
`listUsable` (global and regional) and `backendBuckets.listUsable` were reaching
the `{name}` get, which answered `backend service "listUsable" not found`; they
answer their own lists under the response kind the document declares —
`compute#usableBackendServiceList`, which Google does not derive from the
resource kind. Cloud Storage's object `getIamPolicy`, `setIamPolicy` and
`testIamPermissions` were reaching the `{object...}` get; they read and write the
same shared policy store the bucket and managed-folder policies use. No served
count moved, because all six already counted — that was the defect.

`gcpFanInPatterns` records the twelve routes that legitimately dispatch inside
the handler, each with its reason. The bar for adding one is evidence that the
handler reads the tail and rejects what it does not route; an entry that merely
silences the gate reinstates the blind spot.

## 2026-08-28, thirty-sixth pass — the specifications sync again

Two defects kept the vendored specifications behind upstream, and both are
fixed. Google Cloud went from eighteen documents behind to one, and the one
oscillates because Google serves several Discovery revisions concurrently.

`scripts/fetch-gcp-discovery.sh` derived a central-index fetch's service from
its host. Compute Engine is the one document the central index alone serves, so
its host *is* `www.googleapis.com` and the fetch asked for `apis/www/v1`. The
404 aborted the whole Google sweep, every night, so no Google document was
refreshed at all. It uses the service name now.

The scheduled run also had nowhere to put a refresh. It opens a bump pull
request only when nothing else is open, this repository allows exactly one, and
development has kept one open continuously — so the run failed every night
since at least 2026-08-24 and landed nothing. It now pushes the refresh onto
the open pull request instead, the way a dependency bump rides it, skipping a
release-please branch because a commit there is overwritten by the next release.

The re-vendor moved three declared totals, all upward and none a regression:
Cloud KMS gained two spellings in the Key Access Justifications family, Compute
Engine two in its unserved long tail, and Dataflow thirty — a surface Google
published that this simulator does not serve. The served counts are unchanged.

## 2026-08-28, thirty-fifth pass — a create authorizes against its type, and the installable build is fixed

AWS resource-derivation coverage reaches 1,792 of 1,994 served operations, up
from 1,764, and Amazon EC2's underived count falls from 56 to 34.

A create names a resource that does not exist yet, so the request carries no
identifier — but AWS still evaluates the call against the type:
`ec2:AllocateHosts` authorizes against
`arn:aws:ec2:<region>:<account>:dedicated-host/*`. Falling back to a literal
`"*"` is not a smaller answer, it is a different one, because `"*"` matches
only a policy whose Resource is itself `"*"`. A policy scoped to
`arn:aws:ec2:*:*:dedicated-host/*` is honoured by AWS and was denied here.

Every segment comes from the ARN format AWS publishes; the only invented part
is the wildcard the service itself evaluates against. Three rules keep it from
widening anything: the action must declare exactly one resource type, the
format must name exactly one identifier variable once the partition, region and
account are filled, and the operation's own noun must match the declared type —
`CreateStateMachineAlias` declares `statemachine`, and a wildcard over every
state machine is not what creating one alias authorizes against. It runs only
where the service's own reader found no identifier, so `CreateTopic`, which
carries the topic's name, still derives that name.

The first draft ran before the service readers instead of after and regressed
four services to wildcards; the second counted `${Partition}`, `${Region}` and
`${Account}` as identifier variables, so no format ever qualified.

**The freshness gate could not have caught it, and now something can.** A
module this repository publishes is not an upstream dependency: the support
modules carry no per-module tag, so a pin is the pseudo-version of a release
commit, which always sorts below the bootstrap `*/v0.1.0` tags that were
deleted from the repo but survive in the proxy cache. The gate read every
correct pin as a downgrade. It now skips self-published modules, and
`scripts/check-installable-build.sh` replaces that check by building each
simulator with `GOWORK=off` — the mode `go install` and every SDK harness use.
Reverting the pin makes it fail, which is how it was proven.

**The installable build was broken and is fixed.** All three simulators
reference `uiauth.Config.ApplicationSlug`, `MonitoringToken` and
`RegisterMonitoring`, and all three pinned `ui-auth v0.1.0`, which predates
them — so every `GOWORK=off` build failed, which is the mode `go install
github.com/e6qu/sockerless-cloud/simulator-<cloud>@<tag>` uses and the mode
every SDK harness builds its simulator in. The workspace hid it locally. The
three modules now pin the pseudo-version of a commit that carries the feature.

## 2026-08-28, thirty-fourth pass — Azure's Managed HSM tail

Managed HSM reaches 16 of 16, up from 6, taking Azure to 2,521 of 2,628.

Deleting a pool that carries soft delete retires it: the record leaves the live
collection, joins the deleted one with a scheduled purge date, and answers
`GetDeleted`. Purging destroys the record, and purge protection refuses the
purge — which is the whole reason the flag exists. `checkMhsmNameAvailability`
reads both the live pools and the retired records, so a name held by either is
unavailable. The private endpoint connections round-trip, and the private-link
resources and regions listings report the group and location the pool actually
holds.

The purge answers 202 with a Location the client polls, mirroring the
deleted-vault purge beside it; that poll URL is a Location target Azure does not
document, so it takes an allowlist entry with the same justification the vault's
does.

Two defects found in my own first draft. The delete handler read the pool after
deleting it, so the retired record was addressed by an empty location and
`GetDeleted` could never find it — the listing still passed, because it filters
on subscription alone. And the purge answered a bare 200, which the SDK's poller
rejects as a response with no terminal state.

## 2026-08-28, thirty-third pass — Cloud Run's build path, BigQuery's upload, Memorystore's maintenance

BigQuery reaches 95 of 95, Cloud Run v2 104 of 119 and Memorystore 90 of 94,
taking Google Cloud to 4,489 of 5,426.

BigQuery's `jobs.insert` declares a JSON path and a `/upload` media path that
carries a load job's bytes; the same handler answers both, because the body is
the same Job either way.

Cloud Run v2's `builds.submit` hands the request to the Cloud Build this
simulator serves, so the operation it returns names a build that really ran —
and a submit naming source that was never uploaded is NOT_FOUND rather than a
build of nothing. Both it and `sourceUploads.upload` ride the `/v2` locations
segment Cloud Functions also claims, so they dispatch from its fan-in; that
service's count is unmoved at 42, which is what proves neither shadows the
other. This is the third such collision after Cloud Logging/Cloud Run and
Cloud Build/Eventarc.

Memorystore serves `rescheduleMaintenance`, which records the moved window on
the instance. Its `export` and `import` stay unserved, and not for want of
routing: both move an RDB snapshot of the instance's keyspace, and the slice
models the control plane only — no Redis runs behind an instance, so there are
no bytes to write out and nothing an import could load. Serving them would
fabricate an RDB, so the floor comment records that instead.

## 2026-08-28, thirty-second pass — Firestore's document verbs, and a picked-up tail

Firestore reaches 108 of 120 method spellings, taking Google Cloud to 4,480 of
5,426. `listCollectionIds`, `runAggregationQuery` and `partitionQuery` are
served on both the document parent and the documents root; `documents:write`
applies its writes through the same path a commit uses; and `databases:clone`
and `:restore` mint a database from an existing source, refusing a destination
id already taken and a source that does not exist.

The aggregations run over the documents `runQuery` selects, so a filter narrows
an aggregate exactly as it narrows the query it wraps. COUNT honours `upTo`,
SUM reports an integral total as an `integerValue` and anything else as a
`doubleValue`, and AVG over nothing reports null rather than zero.

Three shapes came from the generated client rather than from assumption:
`runAggregationQuery` returns a single response where `runQuery` streams an
array, `Value.integerValue` is an `int64` in the client and a string on the
wire, and a null aggregate is the enum spelling `NULL_VALUE` rather than a JSON
null. A fourth came from the mux: the same verbs on the documents root need
their own mounts, because the catch-all that serves the document parent only
matches paths continuing past the collection.

`TestUnservedCustomMethodsAreMethodNotFound` asserted `partitionQuery` answers
"Method not found", which was true until this change served it. The case is
gone and the SDK tests cover the method instead.

Twelve spellings remain unserved, all of them the streaming surface —
`documents.listen`, `documents.executePipeline`, and the `changeStreams`
collection whose deliveries need that same plumbing.

This pass also finished an application-observation change left uncommitted in
the checkout after its feature had merged: the discarded JSON encode error, the
`nil` request bodies, and the undocumented `SIM_MONITORING_TOKEN`. It had filed
itself as BUG-2933, which already belonged to the orphaned-simulator leak, so
the observation is BUG-2934 with a note recording why the number moved.

## 2026-08-28 — authenticated application observations

Added one deployment-neutral observation implementation to `ui-auth` and
wired it into the AWS, Google Cloud, and Microsoft Azure simulator composition
roots. Each binary reads its own `SIM_MONITORING_TOKEN`, validates and digests
it at startup, registers `GET /monitoring/observation` outside browser OpenID
Connect, and publishes fixed-cardinality real session, runtime, memory, and
uptime evidence using `e6qu.monitoring/v2`. Missing credentials leave the route
absent, malformed credentials fail startup, exact bearer checks run in constant
time, plaintext credentials do not survive construction, and the observation
does not fabricate cloud costs. Feature PR #95 merged and release PR #96
published the implementation as immutable `v0.26.0`. One shared response
encoder now reports JSON write failures for both monitoring and the existing UI
authentication responses. Race-enabled tests passed across `ui-auth` and all
three simulator shared packages.

## 2026-08-28, thirty-first pass — Cloud Build is whole

Cloud Build reaches 114 of 114 method spellings, taking Google Cloud to 4,468
of 5,426. The regional build create, `builds.retry` and `.approve`,
`triggers.run` and `.webhook`, the three webhook receivers, and the Bitbucket
Server connected-repository pair are all served.

Retrying starts a new build from the original's specification rather than
re-running the record, which is what the service does. Approving records the
decision on a build that is pending one and refuses a build that is not, so
`Build` grew the `approval` member the schema declares and the simulator did
not model. Running a trigger starts the trigger's inline build and stamps the
started build with the trigger that ran it.

Eventarc owns `projects/{p}/locations/{l}/triggers` under the same `/v1`
prefix, so Cloud Build's trigger verbs are offered Eventarc's fan-in before its
IAM ones and fall through when the verb is not theirs. Eventarc's count is
unmoved at 132 of 132, which is what proves neither shadows the other — the
same shape as the Cloud Logging colon-verb split.

Three shapes were wrong in the first draft and the generated client caught each
one: `Build` carries no `approval` field in this simulator, a Bitbucket config's
`connectedRepositories` holds repository ids while the batchCreate response
holds connected-repository resources, and a remove request's
`connectedRepository` is the id itself rather than a wrapper around one.

## 2026-08-28, thirtieth pass — Cloud Logging and Artifact Registry are whole

Two more documents are served end to end. Cloud Logging reaches 508 of 508 and
Artifact Registry 147 of 147, taking Google Cloud to 4,440 of 5,426 method
spellings.

Cloud Logging's `projects.locations.get` was unmounted to avoid inflating Cloud
Run's coverage: both services live under `/v2`, and a wildcard segment matches
one carrying a colon, so a literal route also answers
`locations/{location}:exportProjectMetadata`. The route is mounted now and the
handler splits on the colon — a location is answered, a custom method is still
reported unknown. Cloud Run's count is unmoved by the mount, which is what
proves the split rather than the absence of a route.

Artifact Registry needed two things. A media method declares two paths, the
`/upload/v1` one that carries the bytes and the plain `/v1` one, and the
service answers both; only the upload spelling was registered, which left seven
publish methods unserved. And the prewarmed-artifact family is now real state:
`prewarmArtifact` records the artifact against a stream location with an
expiry, `checkPrewarmedArtifact` and `removePrewarmedArtifact` read and delete
that record, and the listing reports it — it previously answered a hardcoded
empty array whatever the repository held. `exportArtifact` writes the artifact
into the Cloud Storage bucket the request names, taking the bytes from the OCI
blob the digest addresses; an artifact with no stored blob is NOT_FOUND rather
than an export of invented content.

Reading the Discovery document's field descriptions before trusting the first
implementation was what made it faithful. `version` and `tag` are full resource
names rather than bare ids, `streamLocation` is optional and defaults to the
repository's own location, retention defaults to three days, the reported `uri`
is a registry address rather than a resource name, and `gcsPath` starts with a
bare bucket name rather than a `gs://` URL. The first draft had all five wrong.

The four custom methods share the repository colon-verb fan-in with the IAM
triple, so they dispatch from inside it and fall through when the verb is not
theirs. `TestArtifactRegistry_RepositoryIAMStillWorksBesideTheCustomVerbs`
holds that sharing.

## 2026-08-28, twenty-ninth pass — pin the tools CI installs, and follow the cloud that withdrew a surface

The freshness gate's new Go-tool section then failed in CI while passing every
local run, and the cause was the shell. CI runs `zsh scripts/check-latest-deps.sh`;
zsh binds `path` to `PATH` as an array, so the section's `local path=$1` emptied
the command search path for the whole function and every `go` call inside it
failed to resolve. The symptom was "no module resolves" for all three tools.
The resolver now names its local `candidate`, keeps the `go list` error that
the verdict used to discard, and captures stderr to a file rather than
`2>&1 >/dev/null` — zsh's MULTIOS reads that idiom differently from bash and
swallowed the error it was meant to surface.

Five more scripts carried the same class, and `status` is worse than `path`:
zsh makes it read-only, so an assignment aborts the script outright. Both
parse cleanly under bash and under the `zsh -n` sweep CI already runs, because
the damage is at runtime. `scripts/check-zsh-special-vars.sh` now refuses an
assignment to any name zsh reserves, in pre-commit and in CI, and eighteen
bindings across eight scripts were renamed to satisfy it.

Renaming is not free: rewriting `${path//\\/}` in the Google Discovery reader
was missed by the first sweep, which left the freshness check probing bare
hostnames and reporting every document unqueryable while still exiting 0. All
three clouds report their full row counts again — 30, 74 and 120.

The `dupl` quality gate failed on a download, not on the code: `go install
github.com/mibk/dupl@latest` hit an `INTERNAL_ERROR` from proxy.golang.org and
the gate never ran. `deadcode` and `dupl` were the only two unpinned tool
installs in CI — `golangci-lint` was already pinned — so both now name a
version. `@latest` resolves at job time, which puts a release published minutes
earlier into CI with no quarantine and no commit, and makes the download
uncacheable and the verdict irreproducible.

`check-latest-deps.sh` covered Go modules, Terraform providers and GitHub
Actions but not the tools a workflow installs, so it now reads every `go
install <pkg>@<version>`, fails outright on `@latest`, and holds the pinned
version to the same adoption quarantine as everything else. The section shipped
broken on its first draft: under `set -euo pipefail` a `grep` exiting 1 on the
first workflow without a `go install` abandoned the whole scan, so it printed
its header and found nothing. Both negative controls — an `@latest` install and
a deliberately stale pin — came back silent, which is what exposed it.

Bundling the drift the gate then reported upgraded 46 AWS SDK modules, two
Google client modules, `hashicorp/aws` to 6.62.0, and `hashicorp/google` and
`hashicorp/google-beta` to 8.0.0. The Google provider's major bump is exercised
end to end: all seven Terraform tests apply, plan clean and destroy.

The Google client upgrade broke a compile, and the cause belonged to the cloud
rather than the client. `google.golang.org/api` v0.294.0 dropped the
`GitLabConfig` type because Google withdrew the whole `gitLabConfigs`
collection from Cloud Build v1 between Discovery revision 20260627 and 20260814
— the simulator was serving a surface the cloud no longer publishes. The
document is re-vendored at 20260814 and the collection is gone from the
simulator, its store, its type, its SDK test and the wire-path index. Cloud
Build declares 114 method spellings where it declared 130, and serves 86 where
it served 98; both drops are the withdrawal, and the floor comment says so.

The same sweep found eighteen Google Discovery documents behind upstream,
including Compute Engine, Firestore, Cloud Run v1 and v2, Logging and Storage.
That backlog belongs to the tail-serving work rather than to this pass.

## 2026-08-27, twenty-eighth pass — Cloud Storage is whole, and a phantom class is named

The storage v1 document is served end to end: 89 of 89 method spellings, up
from 85. What the four new ones needed was one missing concept rather than four
routes. `objects.restore` and `objects.bulkRestore` had nothing to restore
because a delete dropped the object outright, so soft delete is now real — a
bucket carries the seven-day retention policy Cloud Storage gives it, a delete
under one retires the object instead of destroying it,
`objects.list?softDeleted=true` reports the retired generations with both
delete times, and restore brings one back with its bytes. `bulkRestore`
selects with the members the service actually declares — `matchGlobs` and the
created/soft-deleted time bounds — over a glob compiled so `**` crosses `/`
and `*` does not. `objects.move` renames within a hierarchical-namespace
bucket and rejects a flat one the way the service does.

Serving soft delete closed a leak that had nothing to do with coverage. The
delete path removed the store row and the bucket index entry but never the
backing file under `GCSBucketHostDir`, so every deleted object left its payload
on the host for the life of the process. Retention is what decides between the
two endings now: an object retired under a policy keeps its bytes because
restore will need them, and an object deleted without one is destroyed, file
included, as are the retired objects whose `hardDeleteTime` has passed.

The fifth method is the one worth remembering. Only
`objectAccessControls.insert` showed up as unserved; its five siblings —
`list`, `get`, `update`, `patch`, `delete` — were counted as **served** and no
handler for them existed. `/o/{object}/acl` matched the `{object...}`
catch-all that serves `objects.get`, which answered `object "doc.txt/acl" not
found`: a JSON 404 the coverage probe reads as a handler answering. Only
`insert` was visible, because POST had no catch-all to swallow it. The whole
per-object ACL surface is now real — entries seeded at object creation from the
bucket's default object ACL, as the service does, and the legacy surface
refused with the documented 400 when the bucket has uniform bucket-level access
enabled. `TestGCS_ObjectACLIsNotTheObjectHandler` holds the listing to naming
the object itself, and unregistering the routes reproduces the old answer
verbatim.

The gcloud leg caught a defect the SDK leg could not. The generated Go client
sends `softDeleted=true`; gcloud, rendering Python's bool, sends
`softDeleted=True`. A handler comparing against the lower-case spelling serves
the SDK perfectly and returns an empty list to the CLI, with no error anywhere
— so the parameter is read with `strconv.ParseBool` and both clients drive the
same routes.

`BUGS.md` was repaired in passing: a table row's text had spilled past its
cell, swallowing BUG-2932's entire row, so the file rendered six open bugs and
held seven. BUG-2909's figures had drifted a second time — the row said 190 in
its title and 1,758 of 1,994 in its body while the ratchet measured 1,764 —
and now says 1,764 of 1,994 with 230 remaining, which is what
`TestIAMResourceDerivationCoverage` reports.

## 2026-08-27, twenty-seventh pass — a simulator does not outlive its test

Every test harness starts a simulator as a child process and stops it from its
own cleanup, and each simulator starts a container reaper that polls the
simulator's PID and removes its containers once it exits. Nothing closed the
outer loop. A `go test` killed outright — a timeout kill, a stopped run, an
editor closing the process — never reaches its cleanup, so the simulator kept
running, the reaper kept seeing it alive, and the pair survived together.
Seventeen were found on one machine, aged two to twelve days across all three
clouds, beside a workload container up two days and twenty-eight stale volumes,
holding ports and memory the whole time. They are the likeliest explanation for
a run of local-only failures: an OOM-killed `mkfs.ext4` in the Azure microVM
boot, an Azure simulator that never became healthy, and a Cloud Run execution
sampled after its container had already settled.

A simulator now watches the pid in `SOCKERLESS_PARENT_PID` and exits once that
process is gone — the relationship the reaper already had with the simulator,
one level up. The variable is explicit rather than inferred from
`os.Getppid()`, because a guessed parent would end a `nohup`ed simulator the
moment its shell closed, and a simulator run by hand, by a service manager or
by a container runtime has no parent it should die with; unset, the watch does
nothing. It polls rather than handling a signal, because the ending that
matters is the one signal a process cannot trap. It is set once per `TestMain`
rather than at each of the seventeen places a simulator is started, because
every one of them passes `os.Environ()`.

The watch lives in each cloud's own `shared` package, beside the reaper it
mirrors, and not in `realexec`. The simulators require the support modules at
tagged versions with no `replace` directives, so that `go install` works
against a tag; a function added to the working tree's `realexec` is invisible
to any build that is not using the workspace, and the harnesses build with
`GOWORK=off`. The first attempt put it in `realexec` and broke every AWS suite
with `undefined: realexec.ExitWithParent` — the tagged version had no such
function. `realexec.ProcessAlive`, which the watch is built on, is in the
tagged version already.

`TestSimulatorExitsWithItsParent` drives the whole chain: a real simulator
watching a stand-in parent, held up for three seconds to prove the watch does
not fire on its own, then required to follow that parent into exit. Without the
watch it fails with "the simulator outlived the parent it was told to watch".
Each cloud's copy carries unit tests covering what the watch must ignore —
unset, unparseable, zero, negative, and its own pid — because a watch that
fired on every input would pass a happy-path test.

## 2026-08-26, twenty-sixth pass — the two doors are crossed

Closing the gRPC gaps left one thing unmeasured, and the previous pass said so:
several Google Cloud services are reached over two protocols and served here
from one set of stores, and nothing checked that claim. Every suite drove one
door and read back through the same door, so a handler that answered plausibly
while doing nothing passed as long as its sibling behaved. That is how the REST
`dropRowRange` came to acknowledge deletes it never performed while the gRPC
spelling deleted for real.

`simulator-gcp/sdk-tests/cross_door_test.go` crosses every mounted gRPC service
against its REST door — writing through one protocol and observing through the
other, in both directions — and `simulator-gcp/cross_door_test.go` holds that
file to the services the server actually mounts, so a two-door service cannot
arrive uncrossed. Both halves of the gate were negative-controlled: removing a
service's entry names it, renaming a test breaks the table, and reverting
`dropRowRange` to its no-op fails the Cloud Bigtable crossing.

**The crossing found a second divergence immediately.** The long-running
Operations service kept its own in-memory store while the REST operations doors
read the shared one, and the two minted different name shapes for the same
resource — so an operation a gRPC call returned was invisible to the REST
operations door, and the reverse. Both doors now mint the name the
bigtableadmin document declares (`operations/projects/{project}/operations/…`)
and read one store, so an operation is one resource whichever protocol returned
it. The REST listing had also ignored its own `{project}` parameter and
reported every project's operations; it is scoped now.

**Every Terraform provider is pinned, and an unpinned one is now a failure.**
`hashicorp/google` 8.0.0 was published at 19:15Z on 2026-08-26 and the Google
Cloud Terraform job installed it 77 minutes later, breaking `main`: the major
removes `custom_audiences` from `google_cloud_run_v2_worker_pool`. The
repository has a 24-hour adoption quarantine for exactly this, and the
provider walked past it by being unpinned — `terraform init` takes the newest
release when nothing says otherwise.

The freshness check could not have caught it. Its parser emitted a provider
entry only when that entry carried a version, so the one provider that could
not be held was also the one nobody was told about — the failure mode the
file's own comment warns of, one level down. It fails on an unpinned provider
now, and that immediately found three more: `azurerm` twice and `azuread`
once, the same break waiting on the next Azure major.

The Google providers are pinned at 7.46.0 rather than 8.0.0, because a
two-hour-old major is precisely what the quarantine exists to refuse; the
check reports the pin as held while it ages. `custom_audiences` is gone from
the worker pool regardless: the provider deprecates it there as "not
applicable to WorkerPool" and the major removes it, so the configuration
validates against both 7.46.0 and 8.0.0 and adopting the major will be a
no-op. The field stays covered where it belongs — the Cloud Run v2 document
declares it, the simulator serves it, and the SDK suite sets it and reads it
back.

A flaky Cloud Run test was fixed with it. `TestCloudRun_ExecutionRunningState`
holds a container for ten seconds and samples the execution once the
container's marker reaches Cloud Logging; under the load of the full suite that
trip outlasted the container, so the execution had already settled and
`runningCount` was zero. The hold is thirty seconds now — long enough to
survive a busy machine, short enough that the container still exits on its own,
which the succeeded-count assertion depends on.

## 2026-08-26, twenty-fifth pass — the gRPC gaps are closed, 210 of 213

The previous pass measured the Google Cloud gRPC surfaces and found 130 of
213 methods served. This pass closed the gap: **210 of 213**. Complete: both
Cloud Bigtable admin services (31/31 and 35/35), Cloud KMS 35/35, Cloud
Logging 6/6, the long-running Operations service 5/5, Pub/Sub's three
services, Secret Manager; the Cloud Bigtable data service is at 14/15 and
Firestore at 16/17.

Most of it was the second door the measurement predicted — app profiles,
logical and materialized views, backups, authorized views, schema bundles,
consistency tokens, IAM triples, import jobs, `DeleteLog`, `ListLogs`,
`ListMonitoredResourceDescriptors`, `CancelOperation`, `ListOperations` —
wired to the stores the REST slices already serve, so a resource written
through one door reads back through the other. The rest was genuinely new
behaviour: Firestore's `Listen` and `Write` bidirectional streams following
the document store's own change counter, Cloud Logging's `TailLogEntries`,
Cloud Bigtable's change stream over a real mutation log, its session
protocol (`OpenTable`, `OpenAuthorizedView`) and its GoogleSQL `SELECT *`,
and Cloud KMS's import-job family performing real RSA-OAEP and AES-KWP
unwraps.

**Three methods remain unserved, each because the state it would report does
not exist here** — recorded in the floor comment rather than left as a task:
`Bigtable.OpenMaterializedView` (a materialized view's rows are the
maintained result of a GoogleSQL query the simulator stores as a string and
never evaluates), `Firestore.ExecutePipeline` (pipeline expression trees are
not expressible as the structured query the evaluator speaks), and
`Spanner.FetchCacheUpdate` (a location cache of splits, groups and zones,
none of which one SQLite database in one process has). Each stays on its
embedded Unimplemented server, where a client gets a clear status instead of
a plausible wrong one.

**Backups and snapshots became real copies.** A Cloud Bigtable backup
recorded only its own metadata, so `RestoreTable` produced an empty table and
reported success — every row lost behind a green result. Backups and
snapshots now capture the source table's schema and rows, keyed by the
backup's or snapshot's own name, and a restore reads that capture; a copied
backup holds its own, so deleting the source leaves the copy restorable. The
snapshot family is served on the gRPC door alone, since the bigtableadmin
Discovery document declares no snapshots collection.

**Five defects the work surfaced, fixed with it:**

- The REST `dropRowRange` acknowledged without deleting anything, behind a
  comment claiming the simulator did not model row data — stale, since it
  does. It now deletes from the same store `ReadRows` serves, as the gRPC
  spelling does.
- Cloud KMS deleted a CryptoKeyVersion in any state, and cascaded a
  CryptoKey delete through its versions. The API permits neither: a version
  is deletable only once it has reached a terminal state and was never
  imported, and a key only once it has no versions left. Enforcing that
  exposed a dead end — nothing ever moved a version out of
  `DESTROY_SCHEDULED` — so `destroyScheduledDuration` is now honoured, at its
  documented 30-day default rather than a hardcoded 24 hours, and the
  transition to `DESTROYED` is derived from the scheduled time.
- `ImportCryptoKeyVersion` dropped `trustedWrappingEnabled` and `ListLogs`
  dropped `resourceNames`. Both are declared fields, and a widened listing
  answered with the parent's logs alone reads as "those are all the logs".
- Firestore stamped commit times at millisecond truncation. A watching
  client treats a document whose `updateTime` equals the one it holds as
  unchanged, so two writes inside one millisecond left the second invisible
  to every watcher. Firestore commit times now carry the microseconds real
  Firestore carries.
- Registering the gRPC services started Pub/Sub's ack-deadline sweeper, so
  anything enumerating the mounted surface without serving it — the coverage
  ratchet, the route conformance tests — started another sweeper racing the
  first. Registration now only mounts handlers, and the sweeper starts when
  the process starts serving, beside the Cloud Spanner schedule loop that
  already worked that way. This was a real data race, caught by the race
  detector on the previous pass's own gate.

## 2026-08-26, twenty-fourth pass — the gRPC surfaces are measured for the first time

The survey named one blindness the drift locks did not cover: the Google
Cloud simulator's gRPC services had no coverage measurement at all. The
Discovery probe speaks HTTP, and a gRPC service that embeds its generated
Unimplemented server answers every method it does not implement with
codes.Unimplemented — a correct status and an invisible gap. Nobody could
say how much of Cloud Bigtable, Cloud Spanner, Firestore, Pub/Sub, Cloud
KMS, Secret Manager or Cloud Logging was actually served.

They serve **130 of 213 methods**. The measurement compares two facts: the
declared methods come from the server itself — the ratchet calls the same
registerAllGRPCServices production calls, and grpc.Server.GetServiceInfo
reports each service's method set from the generated ServiceDesc — and the
served methods come from the implementation's own declarations, read from
the syntax tree. Reflection cannot make that distinction: Go names a
promoted method's wrapper after the outer type, so a first attempt
measured every service as complete before the ground truth (Cloud
Logging's implementation declares exactly two of its six methods)
disproved it.

The shape of the remainder matters more than the number. Most unserved
methods are the gRPC spelling of an operation this simulator already serves
over REST — Cloud Bigtable's admin surface is 164/164 over REST against 13
of 66 here, Cloud Logging 504/508 against 2 of 6 — so closing them is
wiring an existing store to a second door rather than new behaviour. The
exceptions are the streaming methods with no REST analogue to wire to:
Firestore's Listen and Write, Cloud Logging's TailLogEntries, Cloud
Bigtable's ReadChangeStream and ExecuteQuery. Pub/Sub's three services and
Secret Manager are complete.

The gate carries the same two locks the REST gates carry — a served floor
that only rises, and a declared-total lock so a re-vendored proto that adds
methods fails rather than drifting — plus a check that every mounted
service is measured, so a new service cannot arrive unseen. All three were
negative-controlled before being trusted.

## 2026-08-26, twenty-third pass — the implementable tails, served

The survey's remaining gaps split into two kinds: families that cannot be
served without inventing Microsoft's or Google's own data (recommendations,
runtime-stack catalogs, packet captures, Key Access Justifications), and
methods that were simply mux misses. This pass served the second kind.

**Google Cloud (+66 method spellings).** Cloud DNS managed zones carry an
IAM policy like every other AIP-141 resource, through the same per-resource
policy store; the document is complete at 80/80. IAM v1 is complete at
266/266: an uploaded user-managed key stores the X.509-wrapped RSA public
key the caller supplies (and refuses anything else, as the API does),
enable and disable flip the stored key's state, and the three catalog reads
— lint, auditable services, grantable roles — answer from the policies,
services and roles this installation actually holds. Cloud SQL is complete
at 150/150: the connector's resolve answers from the same instance state
connect settings read, and point-in-time restore builds a new instance from
the backup covering the requested moment, on the volume machinery the
snapshot work established. Service Usage is complete at 20/20. Cloud
Storage gained the rapid-cache collection and the managed-folder patch —
whose configuration nests its policies map exactly as the schema declares,
which the runtime validator proved by rejecting the shape a first draft
sent.

**Microsoft Azure (+26 operations).** Key Vault's data planes are complete
for keys (25/25: recover, rotate, and real random bytes) and certificates
(27/27: contacts, issuers, recover). Microsoft.Authorization is complete on
both documents: the by-full-resource-ID spellings for role assignments and
definitions, and the permission listings — which report what the *caller*
may do, read from the role definitions their own assignments name, which is
what Azure Resource Manager's operation means. Microsoft.Resources is
complete at 40/40; the generic by-resource-ID methods needed no parallel
store, because a real by-ID URL is the same bytes as the typed URL and Go's
mux precedence already dispatches it — what was missing was Azure's answer
for the addresses no typed route serves. The container registry data plane
gained four more operations.

**A fidelity bug the tests found, and the carve-out it needed.** Azure's
three existence checks — ResourceGroups_CheckExistence,
Resources_CheckExistence and Resources_CheckExistenceById — are HEAD
requests declaring exactly 204 or 404, but Go routes a HEAD to the GET
handler and the read's 200 went out instead. Management-plane HEAD requests
now map the read's verdict onto the check's vocabulary. The first version of
that mapping was too broad and broke API Management, whose twenty-five
entity-tag reads answer HEAD with 200 and an ETag of their own: the carve-out
is derived from the vendored swaggers — API Management and Azure Cosmos DB
are the two providers that declare HEAD operations — and
TestAzureProviderOwnsHEADMatchesTheVendoredSwaggers re-derives that set on
every run, so a re-vendor that gives another provider a HEAD operation fails
until the list agrees. The storage and registry data planes sit outside the
management-plane prefix entirely and keep HEAD's own meaning.

**Two capability gates that were checking the wrong server.** The Azure
PostgreSQL replica and geo-restore tests each stand up a *second* server,
and each asked the loopback question only of the first. On a host with one
usable loopback address the source takes it, the second server stays
modeled, and the tests sat through a three-minute connect budget before
failing. The gate is now asked of every server that needs a listener: Linux
still never skips, and a host without a second address skips only the half
that needs one.

**A store-scan regression, caught by its floor.** The new certificate-issuer
listing read its whole store on a data-plane path. It answers from a
generation index keyed by vault now, and the floor is back at zero.

**Two long-red tests, gated at last.** The App Service Environment
placement and diagnostics tests place an environment in a virtual network
subnet, which needs the Linux network capabilities the simulator's fabric
is built on — so they had been failing on every macOS run rather than
skipping, and the noise was being read around. They call the repository's
own capability gate now: Linux runs them, a host without the capability
skips, and a local suite that is green means green.

Every newly served operation is driven by the client a real caller uses —
the generated Go clients for Cloud DNS, IAM, Cloud SQL and Service Usage,
raw Azure Resource Manager requests for the Azure surfaces, and the JSON
API directly for Cloud Storage's rapid caches, whose collection the
generated client does not carry yet. Both coverage floors moved with
rewritten comments; the declared-total locks are untouched, which is what
made it safe to raise them.

## 2026-08-25, twenty-second pass — Google Cloud Billing fully served

The survey had flagged Cloud Billing as mislabelled: called "declined" in
the work list while its floor comment recorded plain mux misses, with no
reasoning on file. The decision came back "implement", and the document
probes 36 of 36 now.

Billing accounts are real control-plane state: created (top-level or as a
subaccount of a master), listed with the API's master_billing_account
filter, patched (display name, the one field the API admits), and moved
between organizations — through both the POST spelling and the
organization-scoped GET spelling, exactly as the Discovery document
declares them. The project link is one store with two doors:
projects.updateBillingInfo writes it and projects.getBillingInfo — the
read terraform-provider-google issues on every google_project Read, which
this simulator already served — reads it, so the two halves can never
disagree; linking to a closed account is refused, and unlinking disables
billing. The IAM triple rides the same per-resource policy store as every
other AIP-141 resource, with the GET getIamPolicy spelling now served
alongside the POST verbs the generic dispatcher already answered.

The service catalog is the installation's own: services.list names the
services this simulator hosts under stable identifiers in the API's format,
and skus.list is served and empty — a SKU carries published pricing, this
deployment has no price sheet, and an empty catalog is that truth, pinned
by a test so it never becomes fabricated pricing.

Proven through the real clients: the google.golang.org/api/cloudbilling/v1
SDK end to end (lifecycle, subaccounts, move, links, IAM, 404s) and gcloud
billing (accounts list/describe, projects link/describe/list/unlink), with
the runtime spec validator armed and clean.

## 2026-08-25, twenty-first pass — the gates tell the truth, and the survey's findings closed

A three-cloud gap survey of the chosen slices found the ratchets green and
lying in places, and this pass made them honest.

**Drift locks for all three clouds.** The Google Cloud and Azure coverage
floors locked only the served count, so a re-vendored spec that added
methods tripped nothing — the exact hole through which forty-three AWS
operations drifted unnoticed before that simulator's model-drift gate
existed. Both gates now also lock every vendored document's declared total
(30 Discovery documents, 92 Swagger documents): a re-vendor that changes a
surface fails the gate and forces serve-or-document. Both locks were
negative-controlled before being trusted.

**The AWS gates got their own repairs.** AWS Budgets was complete but
untracked — it joined the conformance catalogue and the coverage floor at
26/26, and the fourteen allowlist entries claiming its model was not
vendored (no longer true) are gone. The model-drift gate matched operation
names anywhere in the source, comments included; it now strips comments by
re-printing each file's syntax tree, which immediately exposed four bucket
subresource operations that had passed on prose alone (all genuinely served
by the subresource table, now exempted with that reason) — and the S3
Express pair gained model-scoped exemptions, since AWS Glue legitimately
serves its own CreateSession and an unscoped exemption would trip the
staleness sweep. Behind the stale floor-comment claim that "AWS Budgets
derives completely" sat a real gap: budgets had no derivation case at all,
so the whole budget-action family authorized against a literal "*". It
derives now — budget plus ActionId fill the published global ARN format —
raising resource derivation from 1,758 to 1,764 of 1,994, with only
CreateBudgetAction remaining (its ActionId is a UUID AWS assigns), pinned
by TestIAMResourceARNs_BudgetsAssemblesTheGlobalActionARN. The floor prose
was refreshed everywhere it had drifted.

**The behavioral-pattern gate was inert and is alive again.** Its detector
only scanned the staged diff — nothing, on CI's clean tree — and predated
the StartBackground refactor, so it matched zero workers. It now scans the
whole tree, recognises both launch shapes, and the two unregistered
persistent loops — the Elastic Load Balancing target health checker and the
Amazon ECS stopped-task sweeper — are registered with their test evidence.
Proven by negative controls in both directions, including a synthetic
legacy-shape file.

**Azure PostgreSQL Flexible Server stopped faking.** Long-term-retention
backups had fabricated Succeeded/100% for any name, with Get and List
contradicting each other and Start a no-op; they are store-backed now, the
capture writing a real volume through the operation's own poll, Get
answering 404 for a name never started, and the server delete cascading the
volumes. createMode fell through silently to a plain create for every value
but PointInTimeRestore; the switch is complete now — Replica clones the
source's live volume and sets the replication properties the swagger
declares (Replicas_ListByServer returns real rows), GeoRestore clones the
newest backup, ReviveDropped is refused naming what the simulator does not
retain, and unknown values are rejected — proven by four raw-ARM tests with
their data-plane halves behind the documented loopback gate.

**The record-keeping now matches the measurements.** Every Google Cloud
floor comment is a usable work list — Cloud Run v2's export family, Cloud
DNS's managed-zone IAM triple, Cloud KMS's Key Access Justifications reads,
Firestore's changeStreams collection, Cloud Storage's actual ten unserved
spellings (rapidCaches and managedFolders.patch alongside the four already
named), BigQuery's media path, Cloud Logging's recorded routing trade, and
an honest Compute Engine paragraph for its deliberate 559-of-1,007. Cloud
Billing is recharacterized in DO_NEXT as needing a decision — its floor
records plain mux misses, not a decline. The stale Discovery revision in
the Cloud Run surface table is fixed.

## 2026-08-25, twentieth pass — the derivation ratchet moved, and the recorded impossibilities fell again

BUG-2909's remainder notes met the same second reading the store-scan
exemptions did, and the same thing happened. 1,741 → 1,758 of 1,994 served
operations derive their resource from the type AWS declares.

The Amazon RDS and Amazon ElastiCache copy operations authorize both of
their ends: the target's ARN is fully determined by the name the request
supplies before the resource exists — the argument the floor comment itself
already made for AWS Step Functions creates — and an ARN-named cross-region
source is authorized as sent. AWS Glue's usage profiles and connection types
are name-addressed and derive; its integrations are named by an ARN-valued
IntegrationIdentifier; its tagging operations authorize the ResourceArn the
caller sends. Amazon EC2's tag operations read each identifier's type from
its prefix, longest match first — a grant scoped to one instance allows
tagging that instance and denies tagging another — and the hottest of its
Disassociate/Detach family resolve their association to its parent through
generation-keyed indexes over the simulator's own state, honouring the
store-scan floor this sits behind. Where the coverage probe cannot express
a shape (prefix-typed ids, store-resolved associations, ARN-valued
members), the real behaviour is pinned by TestIAMResourceARNs_* tests
instead of by teaching the probe.

The coverage report gained an opt-in listing
(IAM_DERIVATION_LIST_MISSING=1) of the exact underived operations per
service, which is what the re-reading worked from. The floor holds 1,758,
and the per-service notes now say which families derive for real callers
without moving the metric, and why.

## 2026-08-25, nineteenth pass — the store-scan floor reached zero

The last seven full store reads on request paths fell to the same
re-examination the floor's own warning demanded, and every one of them
disproved its recorded exemption. The five AWS Certificate Manager ACME
scans, filed as "reconcile each row as they read", were keyed lookups:
accounts and key changes answer by endpoint + JWK thumbprint, external
account bindings by endpoint + key identifier, prevalidated domains by
endpoint + base domain — the queried name and its parent suffixes are the
only keys that can match, and reconciliation now applies to the rows the key
narrows to rather than to every row — and revocation finds the stored
certificate by the digest of the DER the request presents, instead of
decoding and PEM-parsing the whole certificate store.

CloudTrail delivery, filed as "genuinely every row", reads a constant-keyed
index of logging trails. What made that possible was noticing why it could
not work before: delivery wrote each trail's LatestDelivery back into the
trail row, so per-request writes re-keyed the trails store on every event
and an index would have rebuilt as often as it was read. The timestamp lives
in its own store now (GetTrailStatus reads it there), the trails store stays
stable, and the index rebuilds only when a trail is created, started,
stopped or deleted. The Azure role-assignment listing, filed the same way,
reads the whole collection through a constant-keyed index — the unfiltered
answer is still every row, but the JSON decode happens once per mutation
instead of once per request on the middleware path.

scripts/check-store-scans.sh holds the floor at zero, and its comment now
says what the history taught: a new scan on a request path is a regression,
not a candidate for an exemption paragraph, because every exemption the file
ever recorded was a keyed lookup on a second reading.

## 2026-08-25, eighteenth pass — database data planes for Cloud SQL and Azure PostgreSQL, and backups that carry the data

BUG-74's port turned out to have a precondition Amazon RDS never faced:
neither Cloud SQL nor the Azure flexible servers had a data plane at all —
instances answered a fabricated `10.0.0.1` or a nominal FQDN with no
listener, no engine and no volume behind them. Both slices now run the
architecture `rds_dataplane.go` established, each implemented in its own
module against its own cloud's semantics.

**Cloud SQL (simulator-gcp).** An instance's `ipAddresses` PRIMARY address is
a listener the simulator owns at the engine's conventional port — the Cloud
SQL Admin API carries no port field, so the port is part of the contract:
per-instance loopback addresses where the host provides them (Linux does),
127.0.0.1 as the last resort, and a loudly-logged modeled tier where neither
exists (macOS refuses loopback aliases without root). The first client
connection boots a real PostgreSQL or MySQL container on the named volume
sockerless-cloudsql-<project>-<instance>; the front proxy owns TLS and
authentication and relays bytes. Identity is real end to end: `rootPassword`
— which the simulator had silently dropped while gcloud was already sending
it — becomes the built-in admin user's credential (postgres / root, listed by
users.list as on Google Cloud), every credential is sealed under a
Cloud-SQL-owned key in the simulator's own Cloud KMS slice — user passwords
had been stored in cleartext — and the users and databases the Admin API
declares are reconciled into the engine as real roles and databases, so a
session runs as the user the client named. Secret Manager's managed rotation
rotates the real engine password through the same path.

Backups carry the data: backupRuns and the retained projects/backups capture
the instance volume through `sim.SnapshotVolume` (one `cp -a --reflink=auto`
— copy-on-write on btrfs/XFS-reflink/OpenZFS block cloning, a full copy
elsewhere, one code path), `instances.restoreBackup` — previously a no-op
verb that discarded its request body — stops the engine, clones the backup
volume back over the instance's, and the next connection boots on the
restored data; clone copies users, credentials and the data volume; deleting
a backup or the instance removes the volumes. The affected operations are
genuinely asynchronous now — BACKUP_VOLUME / RESTORE_VOLUME / CREATE_BACKUP /
CLONE answer RUNNING and settle to DONE, carrying the sql#operationErrors
envelope on failure — which is the loop gcloud and terraform actually run.
Proven end to end by stock drivers on both engine families
(TestCloudSQL_BackupCapturesDataAndRestoreReturnsToIt via pgx,
TestCloudSQL_MySQLBackupCapturesDataAndRestoreReturnsToIt via
go-sql-driver): rows from before the backup present after restore, rows from
after it absent, and current_user is the declared user. The full GCP SDK
suite ran clean with the spec validator armed, the CLI suite passed, and the
terraform suite passed in the Linux container harness.

**Azure PostgreSQL Flexible Server (simulator-azure).** The same
architecture, in Azure's terms: a per-server loopback listener at 5432 whose
address the server's fullyQualifiedDomainName resolves to through the
simulator's own DNS server (the PG slice registers per-name A records;
serving stays opt-in via SIM_AZURE_DNS_LISTEN_ADDR), a real PostgreSQL engine
on the volume sockerless-azurepg-<rg>-<name>, and
`administratorLoginPassword` stripped from stored properties — it had been
persisted and echoed back on GET, which real ARM never does — and sealed
under a service-managed key, Azure's default data-encryption mode.
`require_secure_transport` is enforced ON by default: a plaintext startup is
refused with SQLSTATE 28000 unless the server's configuration turns it OFF.
On-demand backups capture the server volume through the operation's own LRO
(completedTime lands when the capture settles; a failed capture fails the
LRO and withdraws the backup), and `createMode=PointInTimeRestore` clones the
newest backup at or before pointInTimeUTC — else the source's live volume,
a restore at the latest restorable time — into the new server before its
data plane installs, with the source's sealed credential carried over.
Declared databases are reconciled into the engine; PATCH rotates the admin
password through ALTER ROLE. Proven by
TestAzurePGFlexibleServer_BackupCapturesDataAndRestoreReturnsToIt through
raw ARM requests plus pgx resolving the FQDN through the simulator's DNS;
the full flow runs on Linux, and on macOS the test exercises everything up
to the restored server's second loopback alias before the documented
kernel-capability skip.

Both simulators gained the shared plumbing this needed, each its own copy:
PublishPorts on ContainerConfig, the persistent-container reclaim helpers
(FindExistingContainers/AdoptContainer/StartExistingContainer), the volume
lifecycle (RemoveVolume/VolumeExists), volume_snapshot.go, NoopSink, and
RequireContainerRuntime; simulator-gcp also gained the counted-background-
work tracker (simGo/AwaitSimulatorBackground) the AWS simulator already had,
and simulator-azure's DNS server answers registered per-name records. Both
control planes recover their data planes across a persistent restart by
re-binding recorded addresses and adopting the engine containers an earlier
process left running.

## 2026-08-24, seventeenth pass — RDS snapshots carry the data, copy-on-write where the filesystem allows

Amazon RDS snapshots were metadata: CreateDBSnapshot recorded a row and
settled "available" while the instance's data — a real PostgreSQL, MySQL or
MariaDB engine's volume — was never captured, and RestoreDBInstanceFromDBSnapshot
built an instance with a fabricated `.rds.amazonaws.com` endpoint string and
no data plane at all. Two defects wearing one API.

A snapshot now captures the instance's volume into a snapshot volume, restore
clones that volume into the new instance's before its engine first starts, and
deleting the snapshot deletes the volume. The capture is one command —
`cp -a --reflink=auto`, in a one-shot helper container with the source mounted
read-only — so on a container engine whose volume store sits on btrfs, XFS
with reflinks, or OpenZFS with block cloning, the capture clones blocks
copy-on-write and is effectively instant however large the database, and on
any other filesystem the same command is a real full copy. One code path; the
filesystem decides the speed; the RDS API is byte-identical either way, which
is the no-divergence requirement. A log line tells the operator which they
got. The snapshot's status is a real state machine now — creating until the
capture settles, failed with the capture's own words when it fails — and the
master credential travels with the data, because the restored engine expects
the credentials the data was written under. On the API-only tier (no engine)
snapshots remain exactly as modeled as their instances, which is that tier's
contract for everything.

Proven end to end through a stock PostgreSQL driver:
TestRDS_SnapshotCapturesDataAndRestoreReturnsToIt writes rows, snapshots,
writes more, restores, and asserts the restored engine serves the rows from
before the snapshot and not the ones from after — the property that separates
a snapshot from a metadata row. On this development host the log read
"captured copy-on-write on xfs": the Podman machine's volume store has
reflinks, so the test exercised the instant path for real.

Cloud SQL and the Azure database slices still take metadata-only backups;
BUG-74 records the port, with the three touch points and the test as the
template.

The branch's first CI run failed three shards, each a real defect:

- **The spec gate caught the IPAM routing-policy wire shape.** The EC2 model
  returns an `ipamRoutingPolicyRegistrationDelta` from *every* registration
  mutation — create, modify, batch-modify and delete — not the registration.
  Each mutation now records a delta (the single-registration operations
  author a one-entry document in the same schema the batch form accepts) and
  the listing returns them all. The state vocabularies are the model's, not
  invented: deltas are `published`/`failed` per
  `IpamRoutingPolicyRegistrationDeltaState`, registrations are
  `create-complete`/`update-complete`/`delete-complete` per
  `IpamRoutingPolicyRegistrationState`, and a created-but-never-enabled
  association is `pending-enable` per
  `IpamInternetRegistryAssociationState` — the SDK's own enum constants pin
  all of them in the test.
- **The RDS CLI lifecycle test pinned the pre-data-plane behavior** —
  `available` synchronously from CreateDBSnapshot. It now pins `creating`,
  as RDS answers, and drives the CLI's own `wait db-snapshot-available`
  waiter before restoring; the SDK snapshot tests follow the same async
  machine through the SDK's waiter.
- **The Batch job test pulled `public.ecr.aws/...alpine:3` at job-run time**,
  and one CI run's registry-token fetch timed out inside the test. The CI
  shard pre-pulls the image with the same retry/backoff contract as the
  DynamoDB oracle pull, so image acquisition sits outside the test deadline.
  (The fourth red shard was fail-fast cancellation, no defect of its own.)

Sweeping the rest of the snapshot family to the same standard closed the
divergences the cancelled shard would have hidden. **CopyDBSnapshot** was a
metadata copy — it now refuses a source that is not `available`
(`InvalidDBSnapshotState`, as RDS does), clones the source snapshot's data
volume and master credential, and settles asynchronously, so a restore from
a copy returns to the same data as a restore from the source.
**DeleteDBInstance** ignored the final-snapshot contract — it now enforces
`SkipFinalSnapshot`/`FinalDBSnapshotIdentifier` exactly
(`InvalidParameterCombination` in both directions, `DBSnapshotAlreadyExists`
on a name collision) and captures a real final snapshot before the
instance's volume is removed, ordering the capture ahead of the data-plane
shutdown in the same background task. **DeleteDBCluster** enforces the same
parameter contract, its final snapshot as modeled as every cluster snapshot.
The restored-endpoint test asserts what is true now that restores install a
real data plane: on the engine tier the endpoint accepts connections; on the
modeled tier the nominal port derives from the engine.

## 2026-08-24, sixteenth pass — the model-drift sweep, and 42 operations AWS added since

The 41 vendored AWS models were diffed against the simulator's handwritten
source. 3,607 operations; 86 names appeared nowhere. 43 of those were false
positives — S3 routes its bucket subresources by query parameter, so operation
names never appear as strings — and the other 43 were real drift: operations
AWS added to the models (vendored 2026-08-12 through 2026-08-23) after the
simulator's implementation. 42 are implemented now; the one left is filed.

**AWS Glue (2):** the Data Catalog export configuration round-trips, one per
account, with the status settling to the setting because the change is applied
synchronously and no ENABLING window exists to observe.

**IAM (4):** account properties are a real account-scoped map. Role templates
are AWS's own catalog — every template lives under the literal account `aws`
and carries trust policies AWS authors — so `GetRoleTemplateVersion` and
`AcquireRole` fail naming that catalog, and point at `CreateRole` as the
equivalent without the catalog dependency.

**AWS Budgets (12):** the whole budget-action family. An action's execution is
performed through the simulator's own services — an IAM definition attaches
the policy through the same stores the IAM handlers write, an SCP definition
through the organizations attachments, and an SSM definition stops instances
through the same halt the StopInstances handler uses, extracted into
`ec2HaltInstance` so there is one copy of those semantics. The SDK test reads
the execution's effect back through IAM itself, not through the action's own
status, and reversal detaches what execution attached. `UpdateNotification`
moves a notification's subscribers with it, because the notification's
identity is its field tuple.

**Amazon EC2 (25):** two families. The internet-registry associations and
routing-policy registrations are real control-plane state; enabling an
association begins verification with the Regional Internet Registry — ARIN,
RIPE, APNIC — which is outside AWS, so that path fails naming the registry,
and route origin authorizations fail naming the RPKI repositories the same
way. Discovered routes are derived from the account's own route tables.
Application status checks are measured, not declared: the check is probed over
its own protocol, port and path against the instance's address, in the exact
response shape the SDK deserialises — which is how the SDK caught two wire
defects during the build: the status vocabulary is
passed/failed/impaired/suppressed rather than anything invented, and
NetworkProtocolEnum admits exactly http and https, so the TCP default the
first version had was a protocol the model does not allow.

Coverage ratchets rose and hold the gains: EC2 800/800, IAM 180/180, Glue
299/299 in the service-conformance floor, and IAM resource derivation
1,735 → 1,741 because the new operations derive.

**And the sweep is a gate now.** The drift went unnoticed because the
service-conformance floors hold the count of operations *served*: a model
re-vendored with new operations changes nothing they measure.
`TestVendoredModelOperationsAreImplementedOrExempt` is the sweep made
permanent — every operation in every vendored model must appear in the
handwritten source or carry an exemption naming its reason, and an exemption
whose operation has since been implemented fails as stale. Verified against
both failure modes before trusting it.

**Filed, not hidden:** S3's `WriteGetObjectResponse` is the data plane of
S3 Object Lambda, whose control plane is the `s3control` service — not a
vendored slice. Serving the callback without access points would acknowledge
writes nothing can read back. BUG-73 records the one-slice fix shape.

## 2026-08-24, fifteenth pass — PutEvents authorized against the wrong thing

Amazon EventBridge's `PutEvents` names its event bus per entry rather than once
at the top level, so nothing flat read it and the call authorized against `"*"`
— which denies every policy written for a particular bus. AWS authorizes each
entry against the bus it targets, exactly as it authorizes each item of an
Amazon DynamoDB transaction against its own table, and that precedent was
already in this file.

Each distinct bus a batch writes to is authorized once, an entry naming none
writes to the default bus, and a bus given as an ARN is taken as it stands. The
measured floor does not move, because the coverage probe sends a list member as
a list of strings while `Entries` takes objects — the same gap already recorded
for the Amazon ECS attribute operations and the AWS Systems Manager tagging
family — so the behaviour is pinned by its own test and the note beside the
floor says which three EventBridge operations genuinely cannot derive: they
declare no request members at all.

## 2026-08-24, fourteenth pass — the external destinations say which one is missing

A stubbed implementation is acceptable where the dependency is somebody else's
service, on one condition: it must fail loudly and say precisely which external
thing is missing. Amazon SNS was failing, but not saying.

`Publish` takes one of three targets, and the handler read only `TopicArn`. A
caller publishing an SMS to a `PhoneNumber`, or a notification to a device
`TargetArn`, was told *"TopicArn and Message are required"* — a reader would go
looking for a defect in their own request rather than learning where the
simulator stops. Both destinations now fail with the reason itself: SMS names
the telecommunications carrier no AWS API provisions, mobile push names Apple's
and Google's own hosts, and each says what *is* implemented up to the hand-off
so the boundary is legible. The SMS sandbox, which verifies a number by texting
it a one-time password, gives the same carrier reason instead of a bare "carrier
transport is unavailable".

Nothing was manufactured to make them pass: a derivable or log-delivered
one-time password standing in for a real SMS is exactly what the loud failure
exists to prevent.

The error code stayed `InternalError`, which is what this simulator has always
answered for a delivery it cannot perform. Two alternatives were tried on the
way and both were worse, which CI caught: `EndpointDisabled` claims a device
endpoint was disabled — a different fact, and untrue here — and returning 503
made the AWS SDK retry three times against a condition that is permanent,
turning one clear failure into a slow one. The existing tests pinned the code
and were right to.

## 2026-08-24, thirteenth pass — the open bugs, examined one by one

Five bugs were open. One was mine to fix and is advanced; the other four are
not defects this repository can repair, and each was re-checked rather than
re-labelled.

**BUG-2909** advanced to 1,735 of 1,979. AWS Systems Manager's tagging
operations name their own resource type, and that type selects which ARN format
the `ResourceId` fills — the discriminator matters, because a bare identifier
filling all eleven types those actions declare would authorize against ten
resources the request is not about. All ten declared types derive, an
undeclared one derives nothing, and an identifier already an ARN is taken as it
stands. The measured number barely moves because the probe fills `ResourceType`
with a placeholder, so the behaviour is pinned by its own test and the gap
recorded beside the floor — the same shape as the Amazon ECS attribute
operations.

**BUG-2646** is Google's to publish: the live Cloud Run Discovery document was
re-fetched on 2026-08-23 at revision 20260814 and still declares only
`manualInstanceCount`, while gcloud's own client and the GA provider send all
four members.

**BUG-2712** is not a missing API. All 42 Amazon SNS operations in the vendored
model are served; what is missing is a carrier for SMS and Apple's and Google's
hosts for mobile push, which are not AWS-configurable coordinates. There is
nothing faithful to point at.

**BUG-42** is the macOS host boundary: the Podman virtual machine exposes no
nested virtualisation, so the shared azurerm stack cannot boot its guest
locally. That is the one sanctioned skip shape — a capability the host kernel
cannot provide — and the Linux CI cell executes it.

**BUG-56** records its own condition: act "if the throttling recurs outside an
incident". It has not recurred, and the entry's evidence is that a GitHub
incident caused it. Cutting actions or capping matrix parallelism now would
trade wall clock on every run against a problem that has not reappeared.

## 2026-08-24, twelfth pass — Amazon ECS deployment lifecycle hooks, implemented

BUG-72 was the one ECS input the simulator read and did not act on, filed in
the previous pass rather than faked. It is implemented now.

A deployment walks the stages `ServiceDeploymentLifecycleStage` declares and
stops at the first one a hook guards, recording that hook with an identifier
and a status. A `PAUSE` hook waits for an operator; an `AWS_LAMBDA` hook is
invoked through this simulator's own Lambda implementation with the payload the
service sends, and a hook naming a function that does not exist fails the
deployment rather than passing it — a gate that cannot run is not a gate that
opened. `ContinueServiceDeployment` releases the hook its `hookId` names,
advancing to the next guarded stage or abandoning the deployment, and refuses
an identifier the deployment does not carry or one already resolved.

**The gate holds tasks, not just the record.** While a deployment waits at a
stage before `SCALE_UP`, the service scheduler does not launch the new
revision's tasks — that is what a `PRE_SCALE_UP` hook is for, and a hook that
decorated the deployment while the rollout proceeded anyway would be the
decoration this whole sweep has been removing. The test asserts the service has
no tasks while the hook waits and has them once it is released.

Two defects fell out of building it. The `DescribeServiceDeployments`
projection emitted neither `lifecycleStage` nor `lifecycleHookDetails`, so the
deployment's own state would have been invisible to every client and the
`hookId` unobtainable. And `TestECS_ServiceDeployments` had been continuing a
fabricated `hook-1`, which passed only while the identifier was ignored — the
fourth test in this sweep found asserting behaviour that did not exist.

## 2026-08-24, eleventh pass — the Cosmos suite was starving its own emulator

The Azure SDK suite lost three CI runs to `pgcosmos extension is still
starting`, and the readiness failure — made self-classifying in an earlier pass
— said in its own message that the emulator was alive and initialising and the
host was starved. What it could not say is why the host was starved: the suite
was doing it. Two differential tests each started an emulator of their own, so
a two-core runner ran two at once, which is exactly the contention the reaper
comment in that file already described for a *leaked* emulator.

One emulator serves the suite now, started from `TestMain` in a goroutine so it
initialises alongside the suite's own setup rather than inside the first test
that asks for it. The second differential test went from booting an emulator to
0.08 seconds.

The readiness budget was deliberately left alone: `go test` gets thirteen
minutes for this suite and the step fourteen, so buying readiness time trades a
named Go failure for an opaque step kill. And a run that cannot reach either
differential test skips the warm-up entirely, so a developer running one
unrelated test does not pay for an emulator — the tests still boot it
themselves when it was not warmed, so the filter decides when the cost is paid,
never whether the oracle is available.

## 2026-08-24, tenth pass — the Amazon ECS APIs that acknowledged instead of acting

All 77 operations in the ECS model were registered, which is what made the
surface look finished. Registered is not implemented, and auditing the handlers
by depth rather than by presence found the rot.

**Four agent-facing APIs were theatre.** `SubmitTaskStateChange`,
`SubmitContainerStateChange` and `SubmitAttachmentStateChanges` each parsed
their request, ignored every field, and answered
`{"acknowledgment":"ACK"}` — a function that works by ignoring its inputs and
returning a canned response. An agent reporting that a task had stopped changed
nothing, and DescribeTasks went on reporting whatever the scheduler last
assumed. They apply what they are told now: the task's status with the reason
and timestamps it carries, each container's status, exit code, reason, runtime
id and network bindings, and the elastic network interface attachment states. A
task-level report also carries its containers, and it asks the service
scheduler to reconcile, because a task the agent has just stopped is what the
scheduler most needs to see. A report about a task, container or attachment the
control plane does not hold is refused rather than acknowledged.

`DiscoverPollEndpoint` returned hardcoded Amazon hostnames —
`ecs-a-1.<region>.amazonaws.com` — which point an agent at AWS rather than at
this simulator, while the comment above it claimed the opposite. It returns
this simulator's own address now.

**Three force flags were parsed and ignored.** `DeleteService`,
`DeregisterContainerInstance` and `DeleteTaskSet` all declared `force` and
never read it, so operations real Amazon ECS refuses — deleting a service still
scaled above zero, deregistering an instance still running tasks, deleting a
scaled task set — all succeeded here. A caller relying on the refusal was never
told it had skipped a step.

**Two more dropped inputs.** `RegisterContainerInstance` discarded the EC2
instance identity document and invented an id, leaving `ec2InstanceId` empty —
the join every autoscaling integration makes. `ListDaemonTaskDefinitions`
ignored its `status` filter and handed back INACTIVE definitions to a caller
asking for ACTIVE.

**A state-change report could reach across clusters, and StartTask dropped a
flag the scheduler honoured.** The agent reports name the cluster they are
scoped to and the resolver ignored it, so a report naming one cluster reached a
task in another. And `enableECSManagedTags` was applied to the tasks a service
launches but parsed and dropped by `StartTask`, so the same request produced
tagged tasks through a service and untagged ones directly. Both are fixed, and
the cross-cluster refusal is covered.

**A capacity provider could be deleted out from under its cluster.**
`DeleteCapacityProvider` deleted whatever it was given: the AWS-managed
FARGATE and FARGATE_SPOT providers, which this file's own comment says cannot
be deleted and which `CreateCapacityProvider` already refused by name, and a
provider a cluster still listed — leaving clusters naming a provider that no
longer existed. Both are refused now, the second telling the caller to
disassociate it with `PutClusterCapacityProviders`, which is what AWS says.

**And two tests that asserted the theatre, one per surface.**
`TestECS_ContainerInstanceLifecycle` sent fabricated identifiers to the three
state-change APIs and asserted only that each call returned, which the canned
acknowledgement satisfied while doing nothing; its own comment read "each
acknowledges the change". `TestECSCLI_ContainerInstances` was the same test
through the AWS CLI, reporting against the same made-up task id — and it was
the one that failed on CI once the handlers began applying their reports, which
is the gap it should have been reporting all along.

Both assert the real contract now. The SDK test drives a real task and reads
the applied state back, and was checked against the canned handler to be sure
it fails on it; the CLI test runs a real task, reports a container stop with an
exit code and a task stop with a reason, reads all of it back through
`describe-tasks`, and asserts the poll endpoint does not hand the agent an
amazonaws.com address.

## 2026-08-24, ninth pass — Amazon ECS tagging, for every type AWS declares

**Five of the nine taggable Amazon ECS resource types answered "tag-target type
not implemented in sim".** The service reference lists nine for
`ecs:TagResource` — capacity provider, cluster, container instance, daemon,
daemon task definition, service, task, task definition and task set — and the
three tagging operations covered four. Every one of the five refused is a
resource this simulator already holds, so the refusal was a gap, not a limit.

All nine resolve now, through one function the three operations share, so a
type cannot be taggable through `TagResource` and invisible to
`ListTagsForResource`. The one rule that belongs to the operation rather than
the resource — real Amazon ECS refuses to tag a stopped task — is kept, and
kept out of the read path, because reading a stopped task's tags is allowed.

**Two dropped fields.** `CreateDaemon` and `RegisterDaemonTaskDefinition` both
accept a `tags` member and neither stored shape had a tags field at all, so the
tags a caller supplied were discarded silently. Both keep them now, and the
test asserts a create's own tags survive the create rather than only checking a
later `TagResource`.

**A task set was named by a service-shaped ARN.** `TaskSetArn` was built as the
service's own ARN with the id appended, where the published format is
`task-set/<cluster>/<service>/<id>`. Anything dispatching on the resource type
in an ARN would read it as the service — the tagging path would have tagged the
service instead of the task set. Corrected, and asserted.

**The attribute operations derive their container instance.** `PutAttributes`
and `DeleteAttributes` name no container instance of their own; each attribute
carries the instance it is about as its `targetId`. They still count as
underived because the coverage probe sends a list member as a list of strings
and these take a list of objects — a measurement gap, recorded as such, with
the real behaviour pinned by its own test.

## 2026-08-24, eighth pass — the Amazon ECS daemon and Express Mode families

**Amazon ECS went from 20 underived operations to 8, and the derivation ratchet
from 1,722 to 1,734 of 1,979.** The daemon family and the Amazon ECS Express
Mode operations authorize against resource types the derivation did not build,
and most of them name the resource by its own ARN outright — a daemon, a daemon
deployment or revision, an Express Mode service, a service deployment or
revision. The rest assemble: a daemon's ARN is `daemon/<cluster>/<name>`, which
a create supplies in full before the daemon exists.

**The member is chosen by the type the operation authorizes against, never by
whichever ARN the body happens to carry.** `CreateDaemon` authorizes against
the daemon and also carries the task definition's ARN, so a reader taking the
first ARN it found would let a policy scoped to a task definition permit
creating a daemon. The first version of this change did exactly that; the test
written to catch it did, and the reader is keyed by declared type now.

Two things fell out of writing those tests. The cluster reader did not know
`clusterArn`, which is how the daemon family spells it, so a daemon ARN was
built against the default cluster. And an expectation of mine was simply wrong:
`DescribeTaskDefinition` declares no resource type at all, which is how AWS says
an action takes no resource-level permission, so `"*"` is the right answer and
inventing a task-definition ARN there would make a policy scoped to one revision
appear to restrict a call AWS does not scope. That is pinned now too.

**The last two Docker Hub pulls left the test suite.** A CI run lost
`TestContainerReaperAbnormalExit` and `TestStartupSweepCollectsAKilledRunsWorkloads`
together, both at exactly 15.00s, and the child processes' transcripts named
the cause outright: `Get "https://registry-1.docker.io/v2/": context deadline
exceeded`. The runner could not reach Docker Hub. Every other pull in the
repository already reads from the Amazon ECR Public Gallery — an
unauthenticated Hub pull is rate-limited per source address — and these two
were the only ones left hardcoding `docker.io/library/alpine`. They now read
from the mirror like the rest, so a Hub outage or a rate-limited runner no
longer fails the container reaper.

## 2026-08-24, seventh pass — the CloudWatch Logs families that were never assembled

**Amazon CloudWatch Logs went from 31 underived operations to 3, and the IAM
derivation ratchet from 1,694 to 1,722 of 1,979.** The families beyond log
groups and streams — delivery, delivery source and destination, subscription
destination, anomaly detector, lookup table, scheduled query — each authorize
against a resource type of their own, and the floor comment recorded them as
"a resource type of their own that nothing assembles yet". Every one of their
ARNs turned out to be `<type>:<name>` over a name, an id or an ARN the request
already carries, in the format the service reference publishes beside the type.

The declared type selects which member is read, because several of these spell
their identifier `name` or `id` and would otherwise collide: a request whose
declared type does not match what it carries derives nothing rather than
guessing. A member that already holds an ARN is used as it stands, which is how
an anomaly detector, a lookup table and a delivery's destination resolve.
`CreateDelivery` authorizes against both the destination it names by ARN and
the source it names by name, so it derives both.

**The exact ARNs are pinned, not the fact that one appeared.** A derivation
that builds the wrong ARN authorizes the wrong resource, which is worse than
deriving nothing and falling back to `"*"`, so each family has a case
asserting its exact string, alongside two negatives: an opaque record pointer
still derives nothing, and a log-group request is unchanged by any of it.

The three that remain name nothing that resolves — an opaque record pointer, a
query id, and a detector list filtered by log group rather than naming a
detector.

**Corrected:** the BUG-2909 entry had been left carrying both a stale figure
(1,788 of 1,975) and a corrective sentence added beside it, which is worse than
either alone. It now states the measured figures once.

## 2026-08-23, sixth pass — the IAM ratchet's own probe was lying, and the gates were re-checked

**The resource-derivation probe sent a request no client sends.** It lower-cased
every member name it put in the body, while the production derivation reads the
member's real wire name, so an operation whose resource is named by a
`ResourceARN` derived correctly for every real caller and was measured as
deriving nothing. The measurement is what decides where the remaining work is,
and it had already produced wrong explanations — notes blaming "a placeholder
the probe fills" where the cause was the case of the key. The probe now sends
each member under its own wire name, and the guards that assumed a lower-cased
name (the `action`/`version` skips, the ARN-suffix tests) compare
case-insensitively.

**AWS Budgets joined the derivation table.** Its Smithy model is vendored, the
generated resource-type table covers it, and its three tagging operations
resolve the budget or budget action from the ARN they name — the gap the floor
comment had recorded as needing "a generated table and an extractor", which
turned out to need the table plus a probe that sends a real ARN under a real
member name. Coverage 1,691 → 1,694 of 1,979, and the floor holds it.

**The five externally gated bugs were re-checked against upstream rather than
trusted.** Google's live Cloud Run Discovery is now revision 20260814 and still
declares only `manualInstanceCount`, so BUG-2646 stands. The three Smithy
patterns behind BUG-2932 were re-read from `aws-sdk-go-v2` main and are
unchanged, so its allowlist stands. The AzureAD provider's latest release
(v3.9.0, 2026-06-18) still records no Graph endpoint override, so BUG-1345
stands. Each entry now carries the date and the evidence, so the next pass
re-checks rather than re-assumes.

**Corrected:** BUGS.md had drifted to "1,788 of 1,975 … remaining 187" for
BUG-2909 against a measured 1,691 of 1,979. The figures now come from the
ratchet.

## 2026-08-23, fifth pass — the rest of the deployment lifecycle, and a wait that says what it saw

**BUG-70 was one instance of a class, so the class was swept.** Two more
Amazon ECS deployment transitions published a client-visible state in one store
write and the event recording it in the next: a rollout marked `FAILED` before
the event explaining the failure existed, and a service restored to its
previous task definition before anything said a rollback had begun. Real Amazon
ECS answers the rollout state and the events from one service, so neither
intermediate state is reachable there; here any client polling the API could
observe both. Each event is now appended by the same write that publishes the
state it describes, and the remaining `ecsAddServiceEvent` callers were checked
— they write the scheduler's own state, which no API returns, so their events
are the only client-visible part of those transitions and nothing can be
observed out of order.

**A wait that timed out said only "Condition never satisfied".** The ECS
rollback helper polls five conditions and reported none of them, which is what
a run on a machine whose container engine never started the workload looks
like too — indistinguishable from a logic failure without the service in front
of you. It now reports the task definition it settled on, the running, desired
and pending counts, the rollout state and its reason, and the full event list.
Proved by forcing the wait to fail and reading the message.

## 2026-08-21, fourth pass — the race job was killing its own runner

**A test that proved a two-gibibyte cap by reaching it was the cause of four
"infrastructure" failures.** The `race (simulator-aws shared)` job had lost
four pull requests in a row — three to `The runner has received a shutdown
signal`, the fourth to its own ten-and-a-half-minute deadline — and had been
filed as a hosted-runner problem to be worked around by sharding the job.
Measuring it settled the question: `TestOCIReadBodyRejectsGzipBomb` and
`TestOCIReadBodyRejectsOversizedIdentity` together peaked at **7.7 GiB of
resident memory** under the race detector, on a runner with 7 GiB. The job was
being killed for exhausting its runner, and the successful runs — 10m1s to
10m16s against a 10m30s budget — were the ones that finished while thrashing.

The OCI body cap is a parameter now rather than a constant read at the point of
use. What the two tests assert is a property of the boundary, not of its size,
so they supply a 64 KiB cap and assert *both* of its sides: a body of exactly
the cap is returned whole, one byte more is refused rather than truncated, on
the plain path and after inflation alike. The full-size version could only ever
afford the refusal half. A third test pins the cap the served path applies, so
the parameter cannot drift from the registry's real limit.

The suite went from 229 seconds and 7.7 GiB to 104 seconds and 1.4 GiB. The
sharding the bug entry had proposed is not needed, and no required status
context changes.

**And the barrier that makes those tests safe had a hole of its own.** The
same CI run turned up four data races in an Amazon ECS scheduler test.
`AwaitSimulatorBackground` — what a test calls before replacing the
package-level stores — counted `simGo` goroutines and pending timers, but not
work handed to the server's own lifecycle, which is how an ECS task start runs.
A drain could return while a task was still moving through
PROVISIONING→RUNNING, and the next test replaced the stores underneath it. A
finite unit of work registers with both lifecycles now; the lifetime daemons
deliberately do not, because counting a loop that returns only on cancellation
would make the barrier wait forever. A test holds the guarantee directly rather
than leaving it to a timing window.

**A third CI failure, and a third real defect.** A rolled-back Amazon ECS
deployment reported its rollout `COMPLETED` in one write to the service row and
recorded the `deployment rollback completed` event in the next. Between the two
a `DescribeServices` call saw a finished rollback with no event recording it —
the stable task definition restored, counts settled, rollout `COMPLETED` — which
real Amazon ECS cannot return, because it answers both from one service. The
event is written by the same store write that publishes the state now, and the
scheduler state deciding it is read beforehand so that write never takes a
second store's lock. Both rollback tests require exactly one such event, so the
separate append cannot come back unnoticed.

## 2026-08-21, third pass — the parent-scoped store scans converted

**Thirty-nine full store reads left the request paths, and the floor fell
46 → 7.** The previous pass converted every single-row-by-stable-key lookup and
left the parent-scoped collections behind as "collection-shaped by
inspection". They were not: a resource identifier's every "/"-terminated
prefix is a key, so one generation-keyed index per store answers a direct
child collection and a cascading delete alike, and the whole class converts
without deciding in advance which depth a caller will ask about.

That primitive took the Service Bus admin surface — the queue, topic,
subscription and rule listings, the topic and subscription deletes that
cascade over their children, and the authorization-rule drop each entity
delete performs. Listing a namespace's topics had been decoding every
subscription in the process once per topic, because each topic's description
counted its subscriptions by scanning. Key Vault's four listings became
per-vault lookups; AWS Amplify's hosted-content path stopped reading every job
and artifact in the process to serve one request; and the Route 53 CNAME
search that AWS Certificate Manager's validation poll and Amplify's domain
verification both make is now one index keyed on the record names each zone
carries.

The Shared Access Signature rules the Service Bus and Event Hubs hosts
authenticate against are keyed by every `/namespaces/` suffix of their
identifier — exactly the `HasSuffix` question the scan asked, so a namespace
under a resource group called "namespaces", or a queue called "namespaces",
resolves correctly where resolving the segment by its first or last occurrence
would not.

The Azure Files and Table service families went the same way, on the same
primitive promoted to `sim.PathPrefixes`: every Files row is keyed
`account/share/...` and every table entity `account/table/partition/row`, so a
share delete, a directory rename, a share listing, an entity query, a table
deletion and a batch snapshot or restore each read only their own subtree.
Deleting one share had been decoding every other share's objects,
directories, leases, permissions and snapshots.

The backend-address-pool joins went too, on the observation that a pool
identifier is a stable key: a workload joins a load balancer's backend and an
application gateway's the same way, through its own network interface, so one
index over the interfaces answers both — and the gateway's ran from a handler
wrapper, so every request into the simulator was paying it.

Four more went the same way once the earlier pass's own classification was
checked rather than trusted: the ELBv2 listener a proxied request lands on is
keyed by load balancer and port, the target-group-in-use check by the target
groups a listener's or rule's actions forward to, and Event Grid delivery by
the scopes a subscription belongs to. All four had been recorded as fan-outs
that visit every row by design. They were not, and the floor comment now says
so, so the next pass checks instead of repeating it.

**The conversions are held by equivalence tests, not by expectations.** Each
case computes the answer with the scan it replaced and with the index, over
the same rows, and requires them to agree; the awkward shapes (two vaults
holding a same-named key, two namespaces holding same-named children, a newer
failed job beside an older successful one, two zones carrying the same record
name) are in the seed. Each test was checked against a deliberately broken
index and fails on it, so a green run means the index was exercised.

That check found a gap that predated the work: Event Grid's delivery matches a
subscription either by its resource identifier or by the topic its properties
name, and no test exercised the identifier half — every fixture set both. A
subscription carrying no `topic` property now proves it, through the real
delivery function and a real webhook.

## 2026-08-21, second pass — the azurerm crash run to ground, and the scan floor made honest

**BUG-43 closed with a full capture.** The crash that forced the backup
Terraform leg's revert was reproduced inside the Linux Docker harness — sim,
Caddy HTTPS gateway, a minimal azurerm 5.1.0 function-app-with-backup stack —
after macOS's `*.localhost` resolution had stopped every local attempt. It is
a SIGSEGV in the provider's own FlattenBackupConfig, which dereferences the
backup schedule's start time one line before checking it for nil. The trigger
was ours: the simulator served a backup schedule without `startTime`, a
document real Azure never returns because the service defaults the start time
at configuration save. Two fixes fell out — the simulator defaults it the same
way, and the storage plane verifies the ten-field *account* SAS that
`data.azurerm_storage_account_sas` emits, which is the azurerm provider's own
documented shape for `storage_account_url` and which real Azure accepts
anywhere a service SAS is. azblob's account signer pins the layout. Apply,
`plan -detailed-exitcode` clean, destroy — all proven in the harness — and the
reverted leg is restored to the shared stack on a dedicated S1 plan, because
az_sp is deliberately Y1 and real Azure refuses backups on Consumption.

**The store-scan floor now measures a class that can reach zero.** The
analyzer stopped counting a scan whose function reads the same store's
Generation() — that is the amortized rebuild of a generation-keyed index, the
fix itself, and counting it meant the floor was unreachable by construction.
Twelve single-row lookups converted: the four AWS WAF ARN resolutions (web
ACL, IP set, regex set, rule group — paid per evaluated request when a web ACL
is associated), the four Azure storage-account-by-name scans behind the
authorizing data plane, the resource-group middleware lookup, the Service Bus
namespace resolution, and the managed-identity principal check. Floor 58 → 46,
and the floor comment now names what the remainder is: List responses, bulk
mutations, fan-outs, small-store joins, and the ACME scans that reconcile rows
as they read — none of them a one-row lookup paid per request.

## 2026-08-21 — The storage data plane stops taking everyone's word for it

BUG-44 was filed as "the Blob data plane authorizes no shared access
signature" and understated itself: the whole Azure Storage data plane verified
nothing. `Authorization: SharedKey …` was a routing signal, `sig=` was a
routing signal, a request carrying neither was served anyway, and the storage
CLI tests ran for months on one hardcoded fake key that az faithfully signed
with into a void.

blob_authorization.go now verifies all three credentials on Blob, Files,
Queues and Tables alike, and the verification is pinned by Microsoft's own
signers rather than by the simulator agreeing with itself — azblob's
SharedKeyCredential and GetSASURL through the App Service backup tests,
azqueue's and azfile's SignWithSharedKey through tests written for exactly
that purpose, and the az CLI across the whole CLI suite. Five defects only
those signers could catch, each found as a real failure and fixed:

- A service Shared Access Signature has **sixteen** fields, not the nineteen
  the combined reference reads — `saoid`, `suoid` and `scid` belong to a user
  delegation signature, and signing them produces a string no client signs.
- The string grew with the service version **the signature itself declares**:
  `sr` and the snapshot time in 2018-11-09, `ses` in 2020-12-06. The az CLI's
  bundled SDK signs an older layout than the current azblob module, and each
  verifies only against its own.
- The Queue service signs **eight** fields and the File service **thirteen**,
  read out of azqueue's and azfile's own `SignWithSharedKey`.
- The signed path is the **escaped** path: az percent-encodes the slash in a
  nested directory name (`build%2Fartifacts`), and a verifier reading the
  decoded path agrees with every client until the first path that needs
  escaping.
- `Content-Length` must come from the parsed request — Go's server moves it
  out of the header map, so a handler reading the header signs a different
  string than the client did.

Path-style requests are verified against the path the client signed — the one
still carrying the account segment, before the dispatcher's rewrite — and with
the client's own account spelling, because az's storage SDK derives
`queue/<account>` from a path-style endpoint and signs with that literal
string. Batch sub-requests run under the outer request's authorization. Get
User Delegation Key refuses everything but a storage-audience bearer. And
webParseBackupStorageURL now verifies the signature it used to only count
parameters on, which was the original bug.

The suites had to become honest to prove any of this: 23 client constructions
across six SDK-test files and every raw helper presented no credential, and
the CLI suite's key was fake. The harness now provisions each account through
the real ARM API and signs with the key listKeys serves — its own
canonicalization written out independently, so harness and simulator cannot
agree by construction — and the persistence tests provision on the child
simulator they boot. The store-scan gate caught the new per-request account
lookup scanning the account store and it now goes through a GenerationIndex.

**BUG-27 rode along**: the vnet document's `subnets` member was a reference
shape with no `properties`, so `az network vnet create --subnet-name` had its
subnet silently dropped. The member now embeds full subnet documents and an
inline subnet materializes exactly as its standalone PUT does — on an
incapable host that means the standalone PUT's 503 instead of a 200 that
dropped the subnet, which is the half a macOS run proves while Linux CI
proves the created half.

**Closed as repository state, not code**: BUG-41 (branch protection moved to
the sharded Azure CLI contexts, plus eight Terraform contexts the manifest
required and the live setting never enforced) and BUG-67 (the three publishes
the prune race killed were re-dispatched after the grace window merged; every
commit on `main` carries its image).

**BUG-43 stays open with a ready repro**: a minimal azurerm 5.1.0
function-app-with-backup stack against the sim behind the Caddy gateway,
blocked locally only by macOS's `*.localhost` resolution — the capture has to
run on a Linux host, and BUGS.md records the exact recipe.

The consumer note that matters: `backends/azure-common/build.go` builds its
blob client with `NewClientWithNoCredential` and a comment claiming the
simulator "does not enforce storage bearer auth". It does now. DO_NEXT
carries the fix shape for the next pin bump.

## 2026-08-20 — Registries that answer their own service, and workloads nobody could collect

Nine recorded bugs closed, and one of them was not the bug it was filed as.

**The `/v2/` base endpoint** answered `{}` in all three clouds, which is Docker
Distribution's answer and none of these three registries'. Captured with a
token from each service's own token service: Amazon ECR sends 200 with
`content-length: 0` and no `content-type` at all; Google Artifact Registry
sends an empty body declared `text/html; charset=UTF-8`; Azure Container
Registry sends the two-byte body `{}` as `application/json; charset=utf-8`.
The premise that this was one shared fix was wrong — they disagree — so each
cloud's copy answers its own service and each cloud's SDK test pins what was
captured.

**Amazon ECR hydrates a pull through a cache rule.** The registry accepted
pull-through cache rules and served them back, then refused every pull through
one with `NAME_UNKNOWN`, which is the whole of what the feature does: the
repository a rule covers exists only once something has been pulled through it.
A pull now creates that repository — from a `PULL_THROUGH_CACHE` creation
template when one matches — and fetches the image from the rule's upstream
registry through the container engine the simulator already runs workloads on,
so the bytes served are the upstream image's own and the control plane sees the
cached image exactly as it sees a pushed one.

**Google Artifact Registry refuses what Google says it refuses.** Chunked
uploads: "You must use monolithic uploads when you push container images to
Artifact Registry". The line is between one write into an upload session and
several, not between `PATCH` and `PUT` — an engine's `docker push` sends the
whole blob in a single `PATCH`, and that push succeeds against the live service.
Refusing the first write broke a real `docker push` on CI while passing locally,
because this host's Podman sends the blob on the `PUT` instead; the second write
is the chunking Google names, and that is what is refused. The consumer was checked in the same change and does not chunk, and a
real `docker push` through the CLI harness still succeeds — which is the same
thing that makes it work against the live service. The token service also
refuses at the mint a repository scope an uncredentialled caller cannot reach,
with the `DENIED` the live service sends, naming the IAM permission and the
resource; a scope naming no repository is still minted, which is what lets a
client reach the base endpoint and find the challenge at all.

**An Azure Cosmos DB account name is a hostname.** The data plane read its
account from a sockerless-invented `x-ms-cosmos-account` header, and from the
lexicographically-first account when that was absent. Both are gone. The
control plane advertises `<name>.documents.…` as the account's
documentEndpoint, the data plane reads the account out of the host the client
dialled, and a request naming no account reaches none. Two accounts can no
longer share a name either — the service publishes an operation whose only
purpose is to say a name is taken, and creating a second account under one
contradicted it.

**A run's workloads are collectable from their labels alone.** The reaper is a
detached child that dies with the harness container it lives in, so a run
killed that way left its workloads running: twenty-two on one development host,
five still running up to twenty-five hours later. The next simulator over the
same state directory now sweeps what the last one left, and the state directory
is the identity that cannot be shared, so a concurrent suite's workloads are
never touched. `TestStartupSweepCollectsAKilledRunsWorkloads` SIGKILLs a run
with no reaper behind it and requires the next run to collect its container and
network while a workload under a different state directory survives.

**Amazon DynamoDB's last two per-item reads.** BatchGetItem and
TransactGetItems still took one stripe acquisition per key — the two operations
whose entire purpose is naming a hundred items at once. Both take one
acquisition per table now, and the transactional read is thereby the atomic
instant DynamoDB documents: a writer committing between two of its items used
to be visible in the result, which is the anomaly the operation exists to
exclude, and the test proves it by construction rather than by repetition.

**The scheduled specification-freshness run has somewhere to put a refresh.**
It fails on `main`, which belongs to no branch, and the drifted documents it
captures expire in a week. `scripts/refresh-drifted-specs.sh` turns that report
into vendored files through the fetcher that owns each corpus, and the workflow
opens the bump as its own pull request — but only when no pull request is open,
because this project allows exactly one at a time. Twelve specifications were
refreshed with it in this pass, which raised the IAM resource-derivation floor
to 1,691 of 1,979.

**The Azure CLI suite runs as two shards.** It measured 680 seconds against a
fourteen-minute step allowance with its inner deadline already at thirteen
minutes — under two minutes of headroom for a suite that had roughly doubled in
one pass. Splitting it made two gates learn something: the required-status
renderer now reads an include-only matrix as the jobs it runs rather than as a
cross product it cannot form, and the AWS shard-coverage gates now read only
their own job's regexes, since the Azure shards are written in the same shape.
Moving `main`'s protection onto the two new contexts is a merge-time step, and
BUG-41 stays open until it is done.

**A prune that deletes a publish still in flight.** The six imageless commits
were dispatched, and three of them failed at the manifest step with `not found`
for a per-arch tag their own job had pushed minutes earlier. Registry retention
is triggered by the completion of a publish, and the publish that triggers it is
rarely the only one running; a release is "complete" only once both per-arch
tags exist, so a publish between its two pushes is indistinguishable from an
abandoned remnant and was deleted as one. The workflow header claimed the
separation kept concurrent publishes from pruning against each other, and it
never did. Age is the only thing in the listing that tells the two apart, so the
pruner spares anything younger than a two-hour window. Filed as BUG-67 with the
three commits still to re-publish.

**Two things noticed on the way.** The Cosmos differential test made unbounded
container-engine calls, so an engine that stopped answering took the whole
suite's twenty-minute timeout and the panic named the test rather than the
engine; every engine call it makes is bounded now. And two of the six imageless
commits were recorded in `DO_NEXT.md` under identifiers that do not exist — the
eighth hex digit had been invented when padding a seven-character list.

## 2026-08-19 — A fatal lock mismatch, two request-path scans, and the last races

`main` was red on `aws sdk services-a-m`, and the failure named the wrong
thing: dozens of tests reporting `connection refused` against a port nothing
was listening on. The simulator had died a few tests earlier with `fatal error:
sync: Unlock of unlocked RWMutex`. Converting Amazon Lambda's durable
executions to read locks had left four `Unlock()` calls behind their new
`RLock()`, and `sync` answers that by taking the process down — not a request
that fails, but every request after it.

Neither the compiler nor `go vet` sees a mismatch: both calls are real methods
on a real receiver, and only executing the path proves the pair wrong, so a
handler no test exercises can carry the defect indefinitely. The syntax tree
shows it plainly, and `scripts/check-lock-pairing.{go,sh}` reads it there, at a
floor of zero in pre-commit and CI. It reported exactly those four, stays
silent on the release-to-upgrade pattern that legitimately has all four calls,
and a synthetic control confirms it catches the mirror-image mistake.

**A whole class of request-path scans became indexes.** A CPU profile of the
deployed simulator, taken under twelve concurrent requests, put 84.8% of all
its CPU in `ecsPublishedTargetPort` — 99.7% of that JSON-decoding every stored
Amazon ECS task, stopped ones included, once per proxied request. The guest has
two vCPUs, so the data plane ran at an effective concurrency of two: a static
JSON health endpoint behind the load balancer answered in 1.3s where the same
endpoint directly proxied answered in 0.13s, and it grew linearly from there.
Target resolution is 964 ns/op now, down from 1,437,485 on 201 tasks.

It was reported as one function's problem, and it is a class. The load
balancer's own hostname match had the identical shape on a hotter path still —
a handler wrapper, so every request into the simulator paid it before any
handler ran, an Amazon DynamoDB call as much as a proxied page load — and it
was invisible only because a deployment holds a handful of load balancers
against a few hundred tasks. Nothing but a profile of a live deployment would
have found either.

So the shape is what got fixed. `GenerationIndex`, in `shared/index.go` in the
two clouds that have such a wrapper, answers a lookup from a store and rebuilds only when the
store's generation moves; a resource that is deleted, renamed or replaced
leaves the index on the next lookup with no invalidation call anywhere in its
lifecycle. Every handler wrapper that decides whether to claim a request now
uses it — Elastic Load Balancing, AWS Amplify hosting, Azure Load Balancer,
Azure Container Apps ingress, Azure Application Gateway, Azure Event Grid's
publish scope — and `scripts/check-store-scans.sh` holds the rest, all of it
behind a guard, to a floor that may only fall. Google Cloud has no such wrapper
and so has no copy of the helper: the dead-code gate refused one, correctly. Amplify's was the worst of them:
its custom-domain branch is the fall-through for every hostname the simulator
serves, and it evaluated each stored association's verification against the
Route 53 zone store as it went.

Two defects fell out of building it. The scan let a request with no `Host`
header match a load balancer whose DNS name was still empty. And a generation
was only unique within one store instance, so when a test replaced a
package-level store the replacement started counting from zero too and an index
built from the old store was served for the new one — a load balancer that no
longer existed still answering on its hostname. Generations are drawn from one
process-wide counter now, and a test asserts an index refuses a store it was
not built from.

**The race count reached zero.** It began at 144 in `simulator-aws` and this
finished the last fifteen, which had three causes and none was the one the bug
entry had guessed. Amazon ECS defers three reconciliations with
`time.AfterFunc`, which registers nothing until it fires — so a drain saw
quiescence, the stores were replaced, and the timer woke into the next test's.
A pending timer is background work from the moment it is scheduled now, and a
drain is a barrier: it stops the timers that have not fired and drops the work
requested while it runs. That second half is load-bearing, not tidiness — a
reconciliation requests another whenever it moves a task, so a drain that kept
admitting them waited on a group that refilled itself, and CI killed the job
with a reconciliation still runnable after eight minutes where a developer
machine had converged in microseconds. The other twelve
were in the shared server: `finalHandler` built the outermost handler chain on
first use and cached it in an unguarded field, so two concurrent first requests
both saw nil, both built a chain, and both wrote it. That is a live defect
rather than a test artefact — a real deployment's first two requests race
identically — and all three clouds' copies are fixed.

The last two were `realexec.TCPProxy.Close`, which waited for its accept loop
but not for the handlers that loop had spawned, so it returned while a handler
was still calling the caller's target resolver — reading, for a stream
listener, the load-balancer and target-group stores a test was busy replacing.
Any caller that closes a proxy and then tears down what it proxies to has that
race, not just a test. Close now ends in-flight connections and waits for every
handler.

A `race (simulator-*)` job runs the detector over all three modules on every
pull request, because a race found by running the detector by hand is a race
that comes back — and this branch is its own evidence: the first count of zero
was taken from runs made while the tree was being edited, and a clean run
afterwards found two more.

**Every error-path assertion names its error.** The `any-error` class began at
62 assertions satisfied by any failure at all, and is at zero. Each one now
carries the code read out of its handler — `ObjectNotFoundException` for a
scaling policy with no target, `ActiveInstanceRefreshNotFound` for a cancel
with no refresh, `PopReceiptMismatch` for a superseded Azure queue receipt,
`InvalidAMIID.NotFound` for a watermark on an absent image. Two of the 62 were
the detector's fault rather than the tests': a message read through
`strings.Contains(err.Error(), …)` identifies a refusal as surely as
`ErrorContains` does, and a helper that hands its error back to its caller has
moved the obligation rather than dodged it — so a caller that ignores what such
a helper returns is now reported in the helper's place. The last two were the
container-engine refusal helpers, whose registry-side half was asserted by
convention in the caller; the registry's observed status is a parameter now, so
the engine half cannot be written without it.

**Release plumbing.** The publish workflow takes a `commit` input, so the six
main commits a cancelled publish left with no image can each be built by SHA —
no push event can be replayed for a commit already in history. Inherited
specification drift, which deliberately does not fail a branch, is reported as
a warning annotation on the open pull request rather than only in a passing
job's log: the daily run that does fail on it belongs to no branch, and this
project keeps exactly one pull request open, so that is where a refresh has to
land.

## 2026-08-18 — One lock, and the shape behind three bug reports

Issue #43 is the cause under #37 and #39: the mutex guarding the DynamoDB item
store was exclusive, so reads excluded each other and a workspace create that
fans out into a few dozen single-item calls served them one at a time. It is a
read-write lock now, and the contract lives where it is declared — reads take
RLock, anything that writes or reads-then-writes takes Lock for the whole span,
and neither is reentrant. All thirteen of its sites were classified first: four
pure reads, nine writers, including the three PartiQL paths that need the whole
operation and would become lost updates under a read lock.

The result is measured rather than timed. Each reader records that it is inside
the critical section and the test asserts more than one was there at once: peak
concurrent readers went from 1 of 16 to 16 of 16, and a separate assertion holds
writers to still excluding each other. A duration would only have said "fast on
this machine today".

Three bug reports in two days were the same shape — a lock taken for reading,
so a service's read concurrency is one — and each was found by someone watching
a page time out. That shape is now counted:
`scripts/check-readonly-locks.go` reports critical sections that hold an
exclusive lock while only reading a store, and the gate holds the count to a
floor that may only fall. Thirty-two remain, in AWS Glue, Lambda durable
executions, Amazon ECS revisions and the EC2 real-execution fabric.

Then converted, all of them, once the detector could be trusted — and getting
it there was the work. Its first run reported ninety-nine findings, including
functions whose whole job is removal, because `delete` is a builtin rather than
a method. Teaching it to follow calls transitively cut that to eleven and
silently dropped the largest true cluster, because writing an HTTP response
counts as a write if you let it. Excluding the response writer brought the
Glue handlers back at twenty-three. A mechanical sweep on any of the three
earlier numbers would have converted writers to read locks and traded slow
reads for lost updates.

What the trustworthy number described was converted service by service: the
Glue catalog's twelve read handlers, Lambda's four durable-execution reads, the
ECS revision index, and all three clouds' real-execution fabric maps. Every
declaration carries the contract; every writing site kept its exclusive lock.
The detector is held at zero now rather than at a floor.

Running the suites under the race detector afterwards — CI never has — found
something else entirely: 144 races in the AWS module, none of them from the
lock change. A simulator a test builds but never serves still starts its
background workers, and `StopBackground` exists for exactly that but no test
called it, so a load-balancer health checker kept sweeping stores while the
next test rebuilt them. Azure had the same shape in bare goroutines that
complete long-running operations and provision subscription aliases. The AWS
builders stop their workers now and the Azure completions are counted in a wait
group its builders drain: 144 down to 103, Azure clean across four consecutive
runs. The remaining 103 are pre-existing, measured against the merge base
rather than assumed, and filed rather than left silent.

## 2026-08-18 — What the repaired fuzz targets found on their first real night

The sweep found two fuzz targets spending the nightly budget on routes that do
not exist, and a third whose only assertion was that a recorder's code was not
zero — which it never is. The first nightly run after they reached real code
reported three defects, one per cloud family touched.

Cloud KMS read `/cryptoKeyVersions/0000000000000000001` as version 1, because
`strconv.Atoi` accepts leading zeros and a leading sign. A key version has one
name; this gave it several, and a caller could read a version it had not named.
The segment must be the number now, not merely parse to it.

BigQuery trimmed backticks from the two ends of a whole reference and left any
inside it, so `0.` followed by a quoted `0` parsed to a table literally named
with a backtick — an identifier BigQuery cannot have, addressing a table that
can never exist while looking to the caller like a reference that parsed.

The Service Bus AMQP frame reader returned its partly filled buffer beside the
error when a body arrived short: the size the peer claimed, zero-padded, to any
caller that checked one of the two return values rather than both. The reported
case was 3,157,808 bytes, on a pre-authentication path where the peer chooses
both the size and where to stop sending.

Every failing input is a seed now, so an ordinary test run catches a regression
rather than the next nightly.

## 2026-08-18 — A DynamoDB Query that read the table instead of the partition

Issue #37 reported an authenticated page timing out with forty-four concurrent
queries in flight, each over a minute old, and ninety-one goroutines blocked on
one mutex inside the per-item snapshot. The report named the per-item lock, and
fixing that alone was not enough: measured against the database-backed store
the simulator runs on, holding the lock once and copying only the matches took
forty concurrent queriers from 8.65s to 5.20s. Worth having, and not the
problem.

The problem was that a query examined every item in the table. DynamoDB
requires the key condition to fix the partition key with an equality, so the
items a query can return are one contiguous run of the sorted key space — the
reason a Query is cheap in the first place. The partition is now read out of
the compiled key condition rather than the request text, so an aliased name and
a reversed comparison narrow the same way, and a condition that does not fix a
partition examines the full set exactly as before and answers identically.
Together: 8.02s to 0.43s for the same forty concurrent queriers.

Two things went wrong while proving it, both worth recording. The first
reproduction used a memory-backed store and showed the fix making no difference
at all — the simulator files items in a database, where each per-item read
costs something, and a memory store hid the entire effect. The second measured
one partition, where narrowing cannot help by construction. Only a fixture
shaped like the real thing — database-backed, many partitions — showed either
cost. And the narrowing immediately failed a test whose seeded keys were
invented rather than produced by the simulator's own key function, which is the
kind of fixture that tests a table the simulator could never have written.

The deterministic assertion counts the store reads one query performs, because
whether a query reads one partition or the whole table changes nothing it
answers, only what it touches: 600 of 600 items before, at most 30 after. A
helper asserted on its own would have stayed green with the call removed, which
is what the first version of that test did.

## 2026-08-18 — Two ways a run failed without anything being wrong with it

A store soak ran twenty-four readers spinning without pause against
twenty-four writers. On a machine with fewer cores than the reader pool the
readers held every processor and the writers crawled: twelve seconds at full
parallelism, thirty at four processors, forty-six at two, and on the runner two
minutes thirty-seven without finishing — which surfaced as a package-wide
timeout panic naming a test that was only slow. The reader pool is sized to the
machine now and yields between passes, which keeps the hazard it exists for and
gives the writers somewhere to run: three seconds at four processors, and the
module's whole unit-test step in eighty-four.

Two other jobs died in `apt-get update`, each after three honest retries over
ten minutes, while installing seven packages of which five ship on the runner
image. The index is refreshed only when something is actually missing now, so a
degraded mirror cannot fail a job that had no question to ask of it. A package
that genuinely is unavailable still fails loudly.

## 2026-08-18 — The cache a job emptied and then saved

The AWS SDK job freed disk before its timed run by deleting the Go build and
module caches. `actions/setup-go` saves at post-job, and a branch's first run —
having restored the default branch's entry rather than its own — is not an
exact hit in its own scope, so it saves. What it saved was the emptiness: the
entry under the key every Go job shares went from 224,164,847 bytes to 7,564,
written the minute the previous pull request merged, and every branch cut after
that inherited it and built cold. The pre-build step that takes 3m16s against a
warm cache hit its six-minute ceiling twice before the cause was visible.

The deletion was buying 6 GB on a 145 GB disk that was 43% used. It no longer
touches the Go caches; the container images and the apt lists are the part
worth reclaiming, and the pre-build budget now fits the cold build the step
exists to absorb rather than sitting just under twice the warm cost. The
poisoned entry was deleted so the next run on the default branch writes a real
one.

## 2026-08-18 — A second sweep, and the gate that keeps the class out

The first sweep was a reading of every simulator against a taxonomy. This one
is mechanical: `scripts/check-fake-tests.go` decides seven classes of
can't-fail test from the syntax tree, and `scripts/check-fake-tests.sh` turns
its report into a gate — classes with no instances left held at zero, the two
with a standing population carrying a floor that may only fall.

Building it was itself an exercise in the thing it looks for. Its first run
reported twenty-four collections as permanently empty; every one was a map
filled by index assignment the detector could not see, and two more were
counter maps grown with `++`. Its first `no-assertion` run reported twenty-two
tests, eleven of which asserted through a helper one call away. Its first
`any-error` run reported ninety-one, nineteen of which named the error through
a package helper. A detector whose findings nobody can act on is a fake test
with a different shape, so each was fixed — helpers resolved transitively,
growth recognised in every form the repository uses — before a single finding
was acted on. What the calibrated detectors then reported was small and real.

The classes now at zero are at zero because nothing was there: no self
comparison, no wait that cannot be false, no empty subtest, no table that never
runs, no `t.Fatal` off the test goroutine. Three real findings came out of the
rest. A parser-depth test discarded both return values of all three guards, so
it detected a stack overflow and nothing else — a guard quietly stopped
refusing would have passed; each refusal is now named. And two filter tests —
one for traffic capture, one for mirroring — asserted only that the filtered-out
protocol was absent, which a capture or mirror that recorded nothing at all
also satisfies; both now generate the protocol the filter names and require it
through.

Two of those weak assertions turned out to be testing nothing whatsoever, and
only tightening them revealed it. A Certificate Manager export with no
passphrase, and a configuration write with no idempotency token, never reach
the service at all: the generated client validates its own required members and
refuses to send the request. The bare error assertions were passing on that
client-side failure. Demonstrated rather than argued — with the simulator's
passphrase validation deleted, the original test still passes; the replacement
fails. Both now assert the client's refusal for what it is and drive the
service's own validation over the wire, signed the way the client signs, since
that refusal is unreachable through the typed client.

Fourteen error-path assertions that accepted any error at all were given the
service's own refusal: an export without a passphrase, a configuration write
without an idempotency token, a distribution with no origins, four Logic Apps
reads that must 404, and five Google Cloud identity refusals. A transport
fault, a 500 and a body the client could not decode all satisfied them before.
Sixty-two remain, each needing its service's real code read out of the handler
and the vendored model, and the floor holds them visible while they burn down.

The gate was proved by planting one instance of each held-at-zero class and
watching it fail, then removing them and watching it pass.

## 2026-08-18 — The App Service Environment pagers, which did close here

BUG-45 recorded that three App Service Environment operations hand back a pager
the SDK panics on, and concluded that nothing about it could be fixed in the
simulator. That was wrong, and the specification says so: suspend, resume and
change-virtual-network are all declared long-running with their final state via
Location, with 202 a documented response. Answering the collection
synchronously was the divergence. On a synchronous answer azcore selects its
no-op poller, whose result field is a zero value of the poller's type parameter
— here a pager — so it allocates a fresh pager with a nil handler and assigns
it over the one the client built, leaving every read to dereference nil.

The three now answer 202 and record the collection as the operation's result,
which the Location poll serves; the suite drains the pager the generated client
returns rather than working around it over raw HTTP. Draining it takes the
first page directly rather than through the `More` loop Microsoft's generated
example uses: a long-running operation's pager is created already holding its
first page and `More` consults that page's `nextLink`, so for a single-page
result the example's loop yields nothing at all — the second defect the bug
recorded, and a client-side idiom rather than a simulator one. The same mechanism is
covered directly: a running operation's Location answers 202 with no body, a
succeeded one answers with the result and never the status envelope, and a
failed one carries the error and no payload.

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

## 2026-08-17 — What running the newly-wired suites found

The suites the sweep put back into the pipeline immediately reported, and each
report was a real divergence rather than a test that needed loosening.

Two Amazon EC2 members and one AWS Glue member were reported to clients that no
model declares. An `ExportImageTask` carries fewer members than the
`ExportImage` response that starts it — the disk image format and the role the
export assumes belong to the request and its answer, not to the task — and a
batch-get of iterable forms answers `IterableFormItem` members, where the
description belongs to the list item's different shape.

The virtual network's DDoS protection status is a long-running operation whose
final state comes via Location, and answering the list synchronously is a shape
no client is built for: the official Azure SDK builds its pager out of the
polled result and discards the one it constructed, so a synchronous body is
never read and the pager it hands back panics on first use. The operation now
answers 202 with the Location its result is read from, and an operation can
record the payload its Location poll serves — Azure Resource Manager serves the
operation's result there rather than the status envelope.

The security-group host-firewall test registered a namespace interface with no
task behind it, a state the simulator never produces, so the reapply it exercised
had nothing to find; the fixture now stores the task the interface belongs to,
which is where both the live attach and the reapply read the groups from. An
Azure Container Apps replica's console assertion waited for the first log line
of any kind, which the platform's own lifecycle line always wins, so the
assertion read an incomplete console; both such waits now wait for the
workload's own output. An AWS Glue Python shell job's completion budget still
assumed a host process rather than the container it now runs in.

Writing the exact-pin rule surfaced one more defect in the freshness check
itself. A version's publication time was read out of a page of the repository's
releases, and the GitHub list endpoint has been observed answering 200 with an
empty array for a repository whose releases exist and are individually
readable. An absence read out of that page is indistinguishable from a tag that
carries no release, so the check fell through to the git tag's own date and
aged the version by days — adopting it before its quarantine had run. The
release is now fetched for the one tag, where a 404 says the tag genuinely
carries no release and anything else fails.

Two host dependencies had drifted. The Cloud Firestore emulator's component now
refuses anything below a Java 21 runtime, and the runner's default is older, so
the newest installed runtime that satisfies it goes on PATH ahead of the
default and the harness names that requirement instead of reporting a port that
never opened. Reading what the runner already has, rather than adding an action
that installs one, is deliberate: the runner downloads every action a workflow
references before it evaluates any step's condition, so a step scoped to one
cloud still costs every job in the matrix another tarball fetch from the
service already throttling them. And a Compute Engine
describe renders as a one-element list under the client CI installs while
rendering as an object under an older one, so the described resource is decoded
from either rendering — still exactly one resource, or a failure.

## 2026-08-17 — The second round of what CI reported

The provider bump the freshness rule forced turned up a fidelity gap of its
own. `terraform-provider-aws` 6.60.0 waits for a REST API to report itself
available before it deploys to one, and the simulator reported no status at
all, so the wait ran out against a state it had never seen. API Gateway carries
`apiStatus` on a REST API; the simulator's is usable the moment its create
returns, so that is the status it reports from creation onward.

Two suites were pinning behaviour the branch had corrected. The Amazon ECS
load-balancer test asserted that a replaced task's target vanished, which was
the reconciler's old behaviour and not the service's — it now asserts what
Elastic Load Balancing does, the replacement in service alongside the stopped
task's address draining. And the Organizations command-line suite still asked
for the effective form of a service control policy, which the simulator had
started refusing the way the service does; it asserts the refusal now and reads
a tag policy's effective form instead.

Writing that assertion found one more divergence. Attaching a policy did not
require its type to be enabled in the root, which is what makes a policy govern
a target at all: a tag policy nobody enabled attached cleanly and then resolved
through the effective-policy read as though the organization had chosen it.
Both suites enable the type before attaching now, and assert the refusal before
that.

The dependency check itself failed once for a reason nobody caused, which is
the class of failure it was rebuilt to stop producing: a single throttled reply
from the GitHub API made it report that an action's tags could not be read. A
throttle is transient and the API says when to come back, so the documented
wait is honoured — `Retry-After`, or `X-RateLimit-Reset` once the quota is
spent — and the request retried. The wait is never invented: a refusal carrying
no rate-limit signal, or one with quota still left, fails immediately rather
than turning a permanent error into a slow one, and a reset beyond the cap is
reported rather than sat on. The protocol lives beside the check as its own
file so those decisions can be exercised against crafted headers, which is the
only way to test a throttle nobody can ask for.

Left open and filed: three jobs across two runs died in setup with `429`
fetching
`actions/setup-go` from codeload, after the three attempts the runner makes on
its own, with no repository code executed in any of them. The workflow starts around
forty-six jobs at once and every one downloads the same action tarballs within
seconds. The repair — cutting the simultaneous fan-out, or not fetching the
actions per job — is a continuous-integration-wide wall-clock trade-off rather
than a fix to make silently, so it is recorded rather than applied.

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
