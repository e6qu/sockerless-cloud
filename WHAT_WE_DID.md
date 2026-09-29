# WHAT WE DID

What this repository built, and why it is shaped the way it is. The dated
record of each release is `CHANGELOG.md`; open bugs are in `BUGS.md` and
resolved ones in git history; the current snapshot is `STATUS.md`. This file
keeps the decisions that would otherwise have to be rediscovered.

## Origin

The cloud simulators were extracted from the sockerless monorepo into this
standalone repository as a fresh snapshot without history. The per-cloud
directories became `simulator-{aws,gcp,azure}` so `go install` produces
binaries with those names, and every module path moved to
`github.com/e6qu/sockerless-cloud/*`. The console packages, the vendored API
specifications with their surface tables and behavioural registries, the
simulator-scoped scripts and hooks, the Firecracker and realexec harness, and
the simulator jobs of the monorepo's CI came with them.

The simulators stopped describing themselves as a component of the repository
that consumed them: they are a general-purpose reimplementation of slices of
the clouds, anything that speaks a cloud's API can be pointed at one, and what
is built on them is downstream. The simulator reads nothing from a consumer's
conventions — the Azure Functions host once told an HTTP-bootstrap site from a
one-shot one by the image path containing a consumer's overlay name, and now
reads what the site declares in its app settings.

## The framework is one module

Each simulator had carried its own copy of the framework as a `shared/`
package, and the copies drifted: only one resolved the runtime mode once and
refused to serve without a container engine, only one passed `Flush` through
the logging middleware, only one retried the engine's readiness ping, one had
lost the persistence envelope that keeps `json:"-"` fields across a restart,
and one had lost the lock around its handler chain. Fixes landed in one copy
and never in the others.

The framework became the `sim` module, pinned by pseudo-version like
`realexec` and `ui-auth`, holding the union of what the copies had. What is a
cloud's own stayed in that cloud's module and reaches the framework through
hooks: the error writers (`AWSError`, `S3ErrorXML`, `GCPError`, `AzureError`),
the AWS JSON and Query routers, the sandbox profiles, the console coordinates
(`ConsoleOptions`, with Azure's server-side Microsoft Entra federation broker
registered through it), the request rewrites (Amazon S3's zonal
virtual-hosted addressing and Azure Resource Manager's case folding, both
`Config.RewriteRequest`), and the registry behaviours the three container
registries disagree on (`BaseResponse`, `RefuseChunkedUpload`,
`AdmitRepository`, `Scope`, `Authorize`). A method served over both REST and
gRPC shares one implementation that returns a gRPC status, which the REST
handler writes through `GCPStatusError`; the two surfaces differ only in how
they carry a request and an error.

A pin must carry the working tree's content, and `check-support-module-pins.sh`
fails when it does not: `ui-auth` had changed twice after its last pin, so the
installed binaries lacked a fix while every workspace build passed, because
`go.work` resolves the tree and `GOWORK=off` builds do not. A support-module
change therefore lands in two pushes — push, pin the pushed commit, push
again — and the pin moves onto the merge commit afterwards, because a pin on a
branch head stops resolving once the branch is deleted.

## Mechanisms each simulator carried moved into sim

The three simulators had each grown their own copy of the same mechanisms, and
the copies had drifted: one carried a fix the others lacked. They moved into
cloud-neutral packages under `sim/`, with each cloud's wire shapes, error
envelopes and names left in its own module and reaching the shared code
through hooks:

- `workloadhost` (the coordinates a workload dials back on, one policy for
  moby, Podman and Docker Desktop), `bg` (counted background work, now also
  covering the Google Cloud and Azure goroutines nothing had tracked), and
  `workload` (container groups with sidecars in the main container's network,
  bootstrap POSTs, image platforms, docker builds that interrupt on cancel).
- `archive`: every zip and tar extraction goes through an `os.Root` with a
  size limit, after a string-prefix containment check let a sibling directory
  through.
- `blobstore` and `sparse`: payload files written in one pass with their
  digests, HTTP conditions, byte ranges and listing pages; Cloud Storage keeps
  each generation's bytes in its own payload and mirrors only the live one into
  the bucket directory a Cloud Run mount reads.
- `simjwt` and `oidcfed`: one signing-key store and one cached, bounded
  external-issuer verifier; AWS `GetWebIdentityToken` issues tokens signed under
  a per-account issuer whose discovery document and JWKS the simulator serves,
  where it had returned an unsigned placeholder.
- `listq`: list paging and filters. A page token the simulator never issued is
  refused with the error each service declares, where the copies had reset to
  the first page, ignored it, or matched everything on a malformed Google Cloud
  filter. AWS's event-pattern grammar is one matcher with the documented
  differences between Amazon SNS filter policies and Amazon EventBridge
  patterns, and Lambda event-source mappings apply their `FilterCriteria`.
- `kvstore`: throughput buckets, ordered read-write locks, time-to-live
  sweeping and change logs. Cosmos DB answers 429 with a retry-after on
  provisioned throughput, expires items by time to live, serves both change-feed
  modes, and reads one key range instead of every tenant's documents; Azure Table
  storage enforces If-Match and runs a batch under its partition's lock.
- `msgq`, `streamlog`, `delivery` and `cron`: Amazon SQS, Cloud Pub/Sub,
  Azure Service Bus and Queue Storage lease messages from one engine, so
  Pub/Sub dead-letter and retry policies, Service Bus lock durations, delivery
  counts and duplicate detection, and AMQP peek-lock are enforced where they had
  been stored and ignored; Amazon Kinesis and Azure Event Hubs append to one log
  with their retention enforced. Push delivery for Amazon SNS HTTP, EventBridge
  targets, Event Grid and Pub/Sub push retries and dead-letters by each service's
  policy, and one cron evaluator fires EventBridge scheduled rules and
  Scheduler schedules in their time zones, Application Auto Scaling scheduled
  actions, Container Apps scheduled jobs and Logic Apps recurrences. Rows an
  earlier simulator persisted in the old shapes are converted once at startup.
- `realexec/fabric` and `realexec/lbplane`: each cloud's realized networks,
  subnets, interfaces, microVMs and NAT addresses live in one fabric with the
  locking only AWS had, and the load-balancer forwarders, health probes,
  background health tracking and TLS listeners are one data plane that never
  follows a target's redirect and grades a probe by the cloud's own matcher.
- `dbengine` with `pgwire` and `mysqlwire`: the managed relational database
  data plane Amazon RDS, Cloud SQL and Azure Database for PostgreSQL each
  carried, including the fixes one copy had and the others lacked.

## Fidelity rules that came from bugs

- **A served count is not proof a handler exists.** A collection swallowed by a
  multi-segment wildcard counted as covered while Cloud Storage's per-object
  ACLs reached `objects.get`. Google Cloud and Azure hold every served
  operation to a route that names its literal path segments; the routes that
  legitimately dispatch inside a handler are listed with the reason.
- **An unserved operation declares itself.** A route that goes away answers
  the mux's 404, which a client cannot tell from a missing resource. Gates fail
  any unserved operation answering anything but a 501 naming what is missing.
- **A registered operation is not an implemented one.** Amazon ECS once had
  all its operations registered while four ignored every field and answered a
  canned acknowledgement. `scripts/classify-sim-handlers.go` marks handlers
  that answer without reaching state, and the surface tables carry the marker.
  A ✓ means only that a handler reaches state, never that its answer was built
  from what it read.
- **An answer never invents the record it describes.** A reserved-node
  purchase that answered fixed terms and stored nothing, a
  `checknameavailability` that called every name free, a deactivate that
  changed nothing, a Dataflow template read that described a template nobody
  staged — each now answers from what the simulator holds or refuses the way
  the service does. A member the simulator has no basis for is omitted rather
  than filled: an AWS Lambda REPORT line carries the measured memory peak or no
  figure at all, and an error reply carries no tracking identifier the
  simulator would have to mint.
- **Read the schema, not the floor comment.** Whole families were once
  declined on a reading the document did not support. Only a response whose
  *required* content the simulator would have to invent is declined, and it
  declines by naming what is missing. Published catalogues are vendored when
  they can be read deterministically — the Azure managed WAF rule sets,
  Google's interconnect locations, the Cloud Armor expression sets — with the
  source, retrieval and counts locked by a test.
- **A catalogue and a finding are different things.** A published set exists
  whether or not anyone asks, so answering one emptily is false; a risk or a
  recommendation is something an analysis detected, so an empty collection is
  true of a simulator that runs no analysis.
- **Judge a route on every client before believing it.** The generated Go
  client sends `softDeleted=true` and gcloud sends `softDeleted=True`, so query
  booleans go through `strconv.ParseBool`. Docker's `docker push` sends a blob
  in one `PATCH` and Podman sends it on the `PUT`, so Artifact Registry refuses
  the *second* write into an upload session, which is the chunking Google
  names.
- **Real clients decide what a document does not list.** Cloud Run's condition
  `reason` is enum-typed but incomplete: `gcloud run jobs executions cancel`
  compares against the literal `Cancelled`. A Discovery enum is the best
  evidence until a real client contradicts it; then the client wins, and the
  validator leaves those fields unjudged with the evidence beside it.
- **A model can be stricter than the service, and the simulator is never
  stricter than the service.** Amazon S3 answers 204 to `PutBucketPolicy` where
  the model says 200; three Smithy patterns reject values AWS returns.
  Corrections live in `specs/cloud-api/<cloud>/<document>.supplement.json`,
  each pinning the value it replaces and its evidence. An Amazon ECR token
  works against any registry the principal can reach and an Artifact Registry
  token's scope is not the gate, so neither refusal exists here.
- **The validators judge three dimensions.** Beyond unknown fields, the
  runtime spec validators report a required member a response omits and, for
  AWS and Google Cloud, a value outside a declared enum (Azure's
  `modelAsString` enums permit other values). Discovery documents express
  required-ness only for requests, so `TestRequestsMissingARequiredPropertyAreRefused`
  drives each insert with the property removed and requires a refusal.
- **An error shape is attested, never invented.** Where the service documents a
  refusal but not its code — a resource move onto an occupied name — the
  simulator carries the attested message with no leaf code rather than a code
  no client has seen. Each container registry's `/v2/` answer and challenge was
  captured from the service with its own token: ECR an empty body with no
  content type and a Basic challenge, Artifact Registry `text/html`, ACR `{}`.
- **Two APIs onto one setting are one store.** Blob soft delete had been two
  stores — the ARM `deleteRetentionPolicy` and the data plane's service
  properties — so enabling it through Terraform gave permanent deletes. Cloud
  Run v1 is a projection over the v2 stores; every Google Cloud service's gRPC
  and REST doors read one store, and a cross-door test writes through one and
  observes through the other. Azure tags have one holder per scope.
- **A state and the event that explains it are one write.** An Amazon ECS
  rollout published `COMPLETED` or `FAILED` in one store write and the event
  recording why in the next, so a poller could see a state real ECS never
  returns. Both go in one write, and the scheduler state that decides it is
  read before that write so no second store's lock is taken under the first.
- **A filter the simulator cannot evaluate is refused.** A silently ignored
  filter answers with everything and reads as a result, so the Azure resource
  list refuses an unsupported `$filter` with `InvalidFilterInQueryString`, and
  a malformed page token or list cursor is refused rather than read as none.
- **Optimistic concurrency is enforced where the document declares it.** An
  omitted etag or `resourceVersion` is unconditional and a stale one answers
  409 ABORTED; a supplied etag is read before an update mask merges, so a mask
  cannot smuggle the condition away.
- **An asynchronous operation answers the way its specification says.** An
  Azure operation declared long-running with a 202 answers 202 and serves its
  result through the `Location` poll, because azcore's no-op poller for a
  synchronous answer overwrote the client's pager with a nil one. A Compute
  Engine insert returns a running operation and boots behind it on a context
  detached from the request, so a client that gives up does not destroy the
  machine. Operations minted complete answer cancel the way each service
  documents for a finished operation.
- **A stubbed external dependency fails loudly and names itself.** Amazon SNS
  SMS and mobile push say they need a carrier or Apple's and Google's hosts
  rather than reporting a missing `TopicArn`.
- **A retention the service reports is also a bound on what the simulator
  holds.** Stopped Amazon ECS tasks, temporary credentials and AWS WAF sampled
  requests were only filtered out of answers, so a deployed simulator reached
  21,409 task rows, 1.2 million credentials and 196,000 samples. Sweepers
  delete each at its real lifetime through `Store.Prune`, 500 rows at a time.
  Where deleting a record would change an answer, the answer moved into what
  the client presents: a session token carries its expiration under the
  simulator's key, so a pruned credential is still refused as expired.
  CloudTrail event history keeps 90 days apart from each CloudTrail Lake event
  data store's own copy, and CloudWatch Logs applies `retentionInDays` to
  events. DynamoDB's TTL sweep deletes expired items as the service documents,
  consuming no write capacity.
- **A figure the service refreshes lazily is served from a cache.**
  DynamoDB's DescribeTable once read every item to count them, which cost
  seconds on the deployed simulator; its counts and sizes come from a cache a
  background refresh updates at most once a minute, where DynamoDB itself
  refreshes them about every six hours. Hot DynamoDB stores are
  `sim.MakeCachedStore`s, in memory with SQLite as the durable copy.
- **A request answers what the service answers at once.** Amazon ECS StopTask
  records the request and returns the task with its desired status STOPPED,
  then stops the containers as tracked background work, as the service does;
  answering after the container's stop timeout held the task's lifecycle lock
  for thirty seconds.
- **A managed EBS volume is a block device, and the engine is asked for
  nothing it has to interpret.** A plain engine volume showed the workload the
  host's disk. The volume is an image file of the requested size and
  filesystem; a privileged helper attaches it to a loop device and the engine
  mounts `/dev/loopN`. An `o=loop` option had passed locally, where Podman
  implements it in `mount(8)`, and failed on moby, whose `mount(2)` flag table
  has never had it. A mechanism must not depend on which engine parses it.
- **Engine work never runs under a store's write lock.** Removing a volume
  inside an `Update` callback blocked every other task transition; the
  callback returns what to remove and the caller removes it afterwards.
- **A trust policy is evaluated, and a federated identity verified, as AWS
  does.** Assuming a role evaluates its trust policy with the condition keys
  AWS documents for the caller — OpenID Connect claims under the provider's
  name, the `saml:` attributes of a signed assertion — and a SAML response is
  verified against the provider's own metadata. Tests sign real tokens and
  assertions (`testutil/samlidp`) rather than sending placeholders.
- **A request is authorized as the action AWS names, not as its operation.**
  The two differ for 94 operations: a multipart upload is `s3:PutObject`, a
  batch send is `sqs:SendMessage`, a re-encrypt is `kms:ReEncryptFrom` on one
  key and `kms:ReEncryptTo` on the other. The unambiguous ones are generated
  from the Service Reference; the rest are resolved from the request, down to
  each key of a batch delete and each statement of a PartiQL call.
- **Condition keys are read the way each service defines them.** AWS
  CodeBuild's keys name the request member they read, so one resolver serves
  all of them; other services register their request-settled keys per
  service. A key is populated only where AWS's definition fixes its value, so
  AWS Glue's Lake Formation keys and `s3:JobSuspendedCause` stay absent. A key
  counts as built only for a service whose prefix the source composes or whose
  populator is registered.
- **Each service's keys and tags are spelled as that service spells them.**
  Amazon RDS declares `rds:db-tag/`, `rds:cluster-tag/` and nine more per
  resource type and has no `rds:ResourceTag/`; IAM users and roles carry
  `iam:ResourceTag/<k>` and its policies only `aws:ResourceTag/<k>`. A
  request's tags are read from the member path each service's own model
  serializes — `Tags.Tag.N`, `Tags.member.N`, `TagSpecification.N.Tag.M`, a
  lower-case `tags` list, `TagKey`/`TagValue`, or a map.
- **A key that says who called is only set when someone else called.**
  `kms:ViaService` and `kms:GrantIsForAWSResource` come from the
  service-initiation the request carries, and the check a service runs on a
  principal's behalf evaluates each action against that service's own context.
- **An S3 operation is authorized in the namespace AWS publishes it under** —
  s3, s3express, s3-outposts or s3-object-lambda, each against its own
  reference and ARN format, request-shape keys included.
- **What the gate cannot resolve is named, with the reason.**
  `TestIAM_DeclaredConditionKeysAreResolvedOrClassified` fails unless every
  declared key is named by the gate or classified with what the simulator
  would have to model first, and a classified key that becomes resolvable fails
  its own row. Hand-written counts had been read as authoritative; this one is
  measured on every run.
- **Maintenance may not end the service.** Failing loudly on a persistence
  fault is right in a handler, where net/http turns the panic into a 500. On a
  background goroutine it was a restart loop: the retention sweeper met a busy
  database and took the simulator down thirteen times in thirteen minutes. A
  busy or locked database ends the sweep, which resumes next pass; a corrupt
  row still panics; `StartBackground` contains a panic in any worker.
- **A test drives the production write path.** The stopped-task sweep deleted
  by ARN while RunTask stores tasks by ID, and its test stored tasks by ARN and
  passed; retention tests now store through the key RunTask uses. A listing
  takes each object's key from the id it is stored under rather than from a
  field a writer fills separately.

- **A DynamoDB key orders as the service orders it.** Key values are encoded
  so numbers sort by value and binary by bytes, and components are joined by a
  byte no encoded component contains (`dynamodb_keys.go`); a store written
  under an older encoding is re-keyed once at startup, and a restore computes
  each item's key from the target table's schema rather than reusing the
  stored one. Every secondary index keeps ordered entries of its own
  (`dynamodb_index.go`), written only by the two functions that write items,
  so an index query reads its partition in index order, returns what the
  index projects and resumes by the index's key. A page resumes after its
  start key's position whether or not the item survives, carries a
  LastEvaluatedKey whenever Limit is reached, and a parallel scan's segment is
  a hash of the partition key, so segments stay disjoint across pages. A
  transaction prepares every write before applying any. A key or item that
  does not fit the schema is refused with DynamoDB Local's own words, and the
  differential scenarios compare the message as well as the code.
- **An archived S3 object stays archived.** The storage class a write names is
  stored and reported, an unknown one is `InvalidStorageClass`, and reading or
  copying an unrestored GLACIER or DEEP_ARCHIVE object answers the model's
  `InvalidObjectState` or `ObjectNotInActiveTierError` until a restore, whose
  `Days` sets the expiry, makes a temporary copy.
- **An operation answers on every protocol its model declares.** The Go SDK,
  and so Terraform, speaks only rpc-v2-cbor to Amazon CloudWatch; an operation
  without a hand-written CBOR route is served from its JSON handler, converted
  both ways by shapes generated from the vendored model, and a test fails on
  any model operation no protocol serves.

## Execution

Every workload runs as a real container on the engine the simulator was
started against; there is no host-process path, and `SIM_RUNTIME=process` is
API-only. Startup refuses to serve in any mode that executes workloads when no
engine answers, because a process that passes its health check and fails every
workload in the background is worse than one that does not start. Each cloud
product's sandbox profile is applied to its containers, and every profile
refuses the host network and the engine socket.

The stop and cancellation grace a workload gets comes from the cloud's own
setting — an Amazon ECS container definition's `stopTimeout` (30 seconds
unset), Cloud Run's ten seconds, a Container App template's
`terminationGracePeriodSeconds`, App Service's
`WEBSITES_CONTAINER_STOP_TIME_LIMIT`. Azure Container Instances and the
managed-database engines keep five seconds because their clouds publish none.
An Amazon ECS container definition's `workingDirectory` reaches the engine,
which creates the directory when the image lacks it, and an ExecuteCommand
session inherits it. An AWS Lambda invocation's timeout starts when the
runtime first asks for work, with Init separately bounded at ten seconds, as
AWS documents.

RunTask places a task only where it fits. The simulator runs real containers on
one finite host, so rather than invent a capacity it commits each placed task's
declared memory and CPU against what the simulator's own cgroup, or the
machine, can hold, and refuses with the service's `failures[]` shape before any
ENI, volume or record exists. The ledger is the task store plus in-flight
reservations, so two concurrent RunTasks cannot both take the last slot. The
simulator container's memory and CPU limits are its Fargate capacity.

A PROVISIONED DynamoDB table runs the service's own model: a token bucket per
table and per global secondary index, refilled at the provisioned rate and
holding 300 seconds of burst, spending the units `ConsumedCapacity` already
computes. A crude requests-per-second limiter was rejected as a new fake.
Batches return throttled entries as unprocessed, and on-demand tables are
never throttled.

VPC networks take their bridge subnet from a host-side pool rather than the
VPC's own CIDR, so two live VPCs sharing a CIDR coexist; the workload's
elastic network interface address is a real secondary address, plumbed by an
ephemeral `CAP_NET_ADMIN` container so the workload keeps its capability-free
sandbox. The live networks are the allocator's only ledger, which makes a
restart safe. A simulator resource records its owning process — hostname and
pid — because a run id cannot say whether a run is over, and a slice is
reclaimed only from an owner on this host whose pid no process holds.

A run's containers are collectable from their labels alone: a detached reaper
collects its own run, the next simulator over the same state directory
collects what a killed one left, and a concurrent suite's workloads are never
touched. Every harness sets `SOCKERLESS_PARENT_PID`, and a simulator exits when
that process is gone. The variable is explicit rather than `os.Getppid()`,
because a guessed parent would end a `nohup`ed run when its shell closed, and
the watch polls because the ending that matters is the signal a process cannot
trap.

The managed-database services run real engines: Amazon RDS, Cloud SQL and
Azure Database for PostgreSQL serve PostgreSQL, MySQL and MariaDB on named
volumes, with readiness classified by SQLSTATE (57P03 is not ready, whatever
bytes the socket answers), credentials sealed under the simulator's own key
service, and snapshots captured with `cp -a --reflink=auto` — copy-on-write
where the volume store allows it, one code path either way. A restore returns
to the data as it was, which separates a snapshot from a metadata row.

Firecracker boots Compute Engine, Amazon EC2 and Azure virtual machines where
the host kernel allows it, over its default virtio-MMIO transport: the opt-in
PCI transport never delivers the first virtio-blk completion on aarch64, and
CI's x86_64 runners could not see it. A machine's disk outlives the guest
process, so a stopped machine can be generalized and captured, and a
deallocated machine keeps its disk while a deleted one discards it.

A workload host pulls its image the way the cloud pulls it. The Cloud Run and
Cloud Functions hosts present the project's Cloud Run service agent's access
token to Artifact Registry and Container Registry and nothing elsewhere. The
Azure hosts pull with what the workload declared — a managed identity's
identity token, which the engine exchanges through the registry's
refresh-token grant, or a username and password — and nothing for an
undeclared registry. The Lambda and Amazon ECS hosts resolve a
pull-through-cache reference through its registered rule to the upstream, and
pull a reference naming this simulator's own port from its ECR with an
authorization token. An image the host holds for another architecture is
pulled for the one asked for rather than run under emulation. A pull asks the
host first, as `docker run`'s default "missing" policy does, and a registry's
data-cap refusal is permanent rather than retried.

The build services' docker steps run as the build. Cloud Build, AWS CodeBuild
and Azure Container Registry Tasks give their steps a Docker configuration
built on the host's own (`sim.WriteDockerConfig`) whose credential helper
answers the build's registries with the build identity's token and hands every
other registry to the host's helper; it names those registries outright,
because the legacy `docker build` sends the daemon only the credentials of
registries the configuration names. Every docker invocation a build step makes
interrupts on cancel and bounds the unwind with `WaitDelay`, because a killed
CLI never tells buildkit to stop and can leave a child holding the output pipe.
A privileged CodeBuild environment gets the simulator's own engine, and its
output streams to CloudWatch Logs as the service does by default.

## Storage

An object store's conditional write is one step. A client that keeps its
consistency in an object store — a git server arbitrating a branch update, a
lock file — builds on one guarantee: of two writers that each require the
version they read, exactly one wins. The evaluation and the store sit under
one per-object lock (`sim.KeyedLocks`, shared by all three slices) at each
service's single write path rather than at each handler, so a handler added
later cannot skip it; a lock per object keeps unrelated writes concurrent.
Cloud Storage generations are timestamps that never repeat.

An object store is read by key or key prefix. Reading one whole costs every
byte every bucket holds, and the store-scan gate could not see it because it
counts scans per request, not bytes per scan. So the three object stores are
`sim.PrefixStore`s, which have no `List` or `Filter`: a listing is a range on
the primary key (`bucket/key`, `account/container/name`) that decodes only the
rows under the prefix.

An object's contents are not in its row. A row is decoded whole on every read,
so `sim.Payloads` keeps contents in files and the row holds a reference. A
write is always a new file, written before the row that names it and the
replaced file released after, so a crash leaves an unreferenced file the
startup sweep removes and never a row naming a missing file. A file belongs to
exactly one row: a copy or snapshot writes its own.

A future-dated Capacity Reservation becomes active on its start date with
nobody calling anything. Storing that transition would need a timer or a sweep
that could be late, so the record holds what was asked for and the one
accessor every read goes through derives the state as of now.

An Azure cross-resource-group move repoints inbound references by walking
every store a build creates (`sim.TrackedStores`), rewriting keys beneath the
moved identifier and any string naming it at a resource-identifier boundary;
a hand-maintained list would rot silently. Each moved family's credentials are
pinned across the move, and Azure Container Registry content is scoped by the
registry's resource identifier, so it re-keys with the same pass.

A listing reads a key range, never the whole store. The in-memory and cached
stores keep their ids sorted on every write, so `ListPrefix` is a binary
search and the run after it. DynamoDB reads a query's partition, or a scan's
table, as such a range, with no derived key index to invalidate at every write
site. Amazon ECR's DescribeImages reads the `<repository>:` range, and Amazon
ECS's task-definition listings read the family's range from a cached store,
because a listing polled every few seconds must not decode the database.

## Authorization and authentication

Every credential is verified. AWS requests are SigV4-verified against the
principal's stored secret from the header and from a presigned URL alike;
Google Cloud and Microsoft Entra bearers against the simulator's own signing
keys; the Azure Storage data plane's Shared Key and shared access signatures
over the layouts Microsoft's own signers produce, each version's layout pinned
by those signers rather than by the simulator agreeing with itself; Cosmos DB's
shared key through a middleware on every data-plane path, so a new route
cannot skip it; Event Grid's key and SAS against its own published format; and
each container registry against the credentials its control plane mints, with
its own challenge shape. Authorization reads at most 16 MiB of a request body
and streams the rest to the handler.

AWS IAM enforcement derives the resource an action authorizes against from the
types AWS declares and the ARN format published beside each; the operations
that remain name no resource and are authorized against `"*"`, as AWS does. A
create authorizes against its type's wildcard. A resource-policy statement
naming the caller grants; one matching only by account delegates to that
account's IAM, which is what the default AWS KMS key policy means. The Amazon
S3 control plane runs the same gate route by route; a route whose action
declares no resource type authorizes `"*"` because that is what the reference
says, and a test crosses every route against the reference.

Google Cloud's `testIamPermissions` answers from the stored policy resolved
through the vendored curated roles and the held custom roles. A caller
presenting no simulator-issued token is the account's operator, as a
credential no IAM user registered is on AWS. `generateAccessToken` honours the
requested lifetime up to one hour, or twelve where the Organization Policy
constraint allows it, with the constraint defaulting to deny.

## Measurement and gates

Every quality gate has been shown to fail on a planted violation of its own
shape, and a gate whose scan set can go empty exits non-zero rather than green:
two gates had named the monorepo's directories since the extraction and
scanned nothing, one hiding two live slice-bounds panics.

The coverage ratchets hold a served floor that may only rise and the declared
total of every vendored document, so a re-vendor that adds a surface fails
until it is served or declared. A re-vendor can also withdraw a surface, and
the floor comment says which methods moved and why. What a re-vendor adds is
served wherever the document describes something the simulator already holds.
A floor that credited what it never measured was lowered to what it measures.

The store-scan gate holds request-path full-store reads at zero; a row indexed
under every `/`-terminated prefix of its identifier answers a child listing and
a cascading delete from one `GenerationIndex`. The lock gates hold read-only
critical sections under exclusive locks, and `RLock`/`Unlock` mismatches —
which neither the compiler nor `go vet` sees, and which kill the process — at
zero. A lock's contract is written where it is declared: a section that only
reads takes `RLock`; one that writes, or reads and then writes on what it read,
takes `Lock` for the whole span. The fake-test gate decides seven shapes of
can't-fail test from the syntax tree, and the dead-code gate judges the
framework from the three programs that link it.

A slow phase is attributed by measuring it where the simulator runs, never by
reading the code or the database header. Amazon ECS task-start phase marks
travel on the context (`realexec.WithMark`), and `/debug/stores` reports each
table's size; the deployed database's size was once blamed on object bodies
that held 4 KiB. A duration says only "fast today", so concurrency fixes are
proven by counting — peak readers inside a lock, items a query read.

The race detector runs on every pull request over every module. `bg.Go` and
`bg.AfterFunc` count goroutines and pending timers; a drain (`bg.Await`) is a
barrier that stops unfired timers and drops work requested while it runs,
because a reconciliation requests another whenever it moves a task.
`bg.JoinedGo` counts work a caller waits on and never drops it, since dropping
a fan-out the caller joins leaves it waiting forever. Finite work handed to
`Server.StartBackground` registers with the drain too (`bg.Handoff`); lifetime
daemons do not, or the barrier would wait forever.

A test asserts a boundary at a small parameterised limit rather than by
reaching the real one: the OCI body-cap tests peaked at 7.7 GiB under the race
detector on a 7 GiB runner until the cap became a parameter tested at 64 KiB.
A soak test's reader pool is sized to the machine and yields. A nightly fuzz
failure becomes a seed, so ordinary `go test` catches the regression.

Dependencies of every class — Go modules, Terraform providers, GitHub Actions,
installed tools, the consoles' npm packages — are held to their newest release
past a 24-hour adoption quarantine. `ui/bunfig.toml` sets
`install.minimumReleaseAge` to the same day, because caret ranges let the
resolver pick versions the quarantine refuses. An exact provider pin is
compared exactly and an unpinned provider is a failure. On a pull request, a
drift byte-identical to `main`'s is reported rather than failed, since upstream
moved under the branch. Every network lookup the check makes carries a
deadline and fails naming what never answered. A deliberate hold names its
cause and goes when the cause does.

## Continuous integration

Jobs are held to fifteen minutes; a timeout kill is reported as "cancelled",
which is what let two read as infrastructure noise. The AWS SDK and CLI suites
are sharded on measured time, with gates holding every test to exactly one
shard. Each suite builds its simulator into `.build/<suite>/` and namespaces
its images per suite, so concurrent suites never overwrite each other.

Base images are read out of the source by `scripts/base-images-for.sh` and
warmed from one cache entry per cloud keyed on the image set, because the ECR
Public Gallery caps anonymous pulls by data volume, which no retry recovers
from. Every suite takes a base image through `testutil/baseimage.Ensure`, which
asks the host first; a pull that tests a simulator's own registry is the named
exception. Warming is `continue-on-error`, because it primes a cache and the
point of use fails naming the image. Jobs never delete the shared Go caches
(`actions/setup-go` saves what is left at post-job), and refresh the apt index
only when a package is missing. The workflows reference as few external
actions as possible, because the runner downloads every action a workflow
names for every job. A tool a suite needs — gcloud, `cbt`, the AWS CLI — is
installed in `TestMain` with a few retries, never skipped.

A differential oracle is pinned by its multi-platform index digest, never a
tag or one platform's manifest, and the harness refuses an image built for
another architecture than the engine's, naming the pin. Its readiness wait is
a wall-clock deadline with a single-attempt probe, and the container is kept
until the test removes it, so a failure reports the last probe error, the
container's state and its log. `scripts/base-images-for.sh` reads digest
references and scans `sim/` for every simulator, so framework-run images are
warmed too. Every Terraform harness runs `init -upgrade` against exactly
pinned providers, a pre-commit hook holds `.tf` files to `terraform fmt`, and
a suite's Lambda image declares the machine's architecture.

Microsoft's Cosmos DB emulator starts once for the Azure SDK suite, from
`TestMain`, because two emulators on a two-core runner starve each other. The
Google Cloud gRPC door listens on the HTTP port plus two (`:4569`), so the
three simulators' defaults coexist on one host.

Release pull requests run no CI: they change only `CHANGELOG.md` and
`.release-please-manifest.json`, the Actions approval policy holds runs of
pull requests opened by `github-actions[bot]`, and the owner keeps that policy
strict so outside contributors cannot run CI. The owner merges them by bypass.
The Release workflow ends in a reconciliation job
(`scripts/verify-release-complete.sh`) that fails unless every expected asset
and image index exists, because a hanging build once left an ordinary-looking
release missing part of its contents. The simulator Dockerfiles keep the Go
caches in cache mounts so the build-cache export carries only the source and
binary, and manifest composition retries only a broken connection.

Publishes are keyed per commit and never cancelled by a later merge; retention
runs in its own workflow and spares anything younger than two hours, because a
publish between its two per-architecture pushes looks like an abandoned
remnant. The required-status-check manifest is compared against `main`'s live
branch protection by a pre-push hook, since protection drifts with nobody's
commit and the push is when it starts to matter. The daily specification refresh checks
out the pull request it will land on before re-vendoring, so it measures drift
against that branch's pins and lands as one commit.

`scripts/check-repo-config-sane.sh` fails a commit whose checkout has acquired
`core.bare = true` or a test fixture's identity — the corruption a `git config`
run without `-C` in a linked worktree writes into the shared `.git/config`.
Every `git config` this repository runs names its target with `-C`.

`Dockerfile.test`, the shared harness image, had matched `.gitignore`'s
`*.test` and was never committed, so every `make docker-test` failed. It is
committed, un-ignored by name, with every toolchain pinned to a version and a
digest, because the image decides what a test result means.
