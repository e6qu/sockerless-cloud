# WHAT WE DID

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
