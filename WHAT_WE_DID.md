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
one-shot one by the image path containing a consumer's overlay name, and later
by app settings named after that consumer; it now runs every container site
the way App Service does (see Execution).

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
- **An operation names a type that exists.** Every long-running operation
  carries the metadata message its service's Discovery document or proto
  `operation_info` declares; the Google Cloud operation constructor takes that
  builder as a required argument, so no service falls back to a default type
  that a gRPC client cannot decode.
- **Each service spells its errors its own way.** An AWS error is written with
  the message member (`message` or `Message`) and, for Amazon EFS, the
  `ErrorCode` member that the serving service's Smithy model declares; a
  middleware resolves the model from the request's target or signing name.
- **A state change follows the work it names.** Amazon RDS instances and
  clusters report `stopping` and `starting` until the database engines behind
  them have actually stopped or started, and Application Gateway, target-pool
  and backend-service health comes from probes that have run, reporting
  `Unknown` until they reach a verdict.
- **A restore writes a new object.** A restored Cloud Storage object gets a new
  generation and metageneration 1, and its preconditions are judged against the
  live object it would replace.
- **An operation is recorded where its name says it lives.** Every Google Cloud
  long-running operation gets a fresh name in its service's operations
  collection and a row in the operations store, carries the metadata message
  and response type its service declares (with `verb` and `target` filled), and
  can be read back over REST and gRPC.
- **A managed cluster runs what it says it runs.** An Aurora cluster runs one
  PostgreSQL- or MySQL-compatible engine on its own volume behind writer,
  reader and member endpoints that follow its members' states; Kinesis
  reshards by real splits and merges; Application Auto Scaling acts on the
  CloudWatch alarms it creates, within the policy's own cooldowns; EC2
  application status checks run on their own interval and thresholds.
- **An event reaches its subscribers because something emitted it.** Cloud
  Storage publishes JSON_API_V1 notifications for object writes, deletes and
  metadata changes; Eventarc delivers Cloud Storage triggers from those
  notifications as binary-mode CloudEvents; Cloud Logging `entries:copy` copies
  the entries its sinks routed to the bucket.
- **A subscription stays open for as long as the cloud holds it.** Amazon Kinesis
  Data Streams `SubscribeToShard` holds its event stream for the documented
  five minutes, declared with `sim.DeclareWait`. It sends the backlog from the
  `StartingPosition` and then one `SubscribeToShardEvent` each time a put, a
  reshard or a deletion signals the shard. Every event carries a
  `ContinuationSequenceNumber` and `MillisBehindLatest`. The event that drains
  a closed shard names its `ChildShards` and ends the stream. A second call for
  the same consumer and shard within five seconds fails with
  `ResourceInUseException`, and a later one takes the subscription over.
  Deregistering the consumer ends the stream with `ResourceNotFoundException`.
  The handler had sent the stored records in one event and closed, so a
  consumer never saw a record put after it subscribed. `DeleteStream` refuses a
  stream with registered consumers unless `EnforceConsumerDeletion` is set, and
  consumers carry their own tags, as `aws_kinesis_stream_consumer` reads them.
- **A revoked session stays revoked.** `workforcePools.subjects.revokeSessions`
  records when a subject's sessions end, and every check of a simulator-minted
  access token refuses one issued to that subject at or before that second.
- **AMQP is a byte stream.** A WebSocket message or a TCP read can end inside
  a frame, so the Service Bus and Event Hubs connection carries unconsumed bytes
  over to the next read; dropping the connection on a split frame lost
  receive-and-delete messages it had already handed out.
- **A link receives only what its address names.** A management reply link
  (`<entity>/$management`) is never a receiver of the entity, so a message
  goes only to a link that attached to the entity itself; AMQP credit is the
  receiver's delivery-count plus link-credit minus the sender's, as AMQP 1.0
  defines it. Handing a receive-and-delete message to the SDK's reply link
  lost it whenever that link's credit arrived first, which on CI was often.
- **A REST receive waits for its `timeout`.** Service Bus Receive and Delete
  and Peek-Lock on a queue or subscription waited up to the `timeout` query
  parameter (60 seconds when absent) for a message and answered 204 only when
  none became receivable; the simulator had answered 204 at once. The wait
  wakes on the entity's arrival signal — a send, a scheduled message reaching
  its time, an abandon, or a lock running out — ends with the caller's
  request, and declares itself with `sim.DeclareWait`. A renewed lock re-arms
  the lock-expiry wake-up, and a restarted simulator re-arms the wake-ups of
  the scheduled and locked messages it loads, so neither a REST waiter nor an
  AMQP receiver holding credit misses a message whose timer belonged to the
  old process.
- **A Log Analytics query runs or is refused, never half-read.** The query
  engine had split a query on every `|` and read each stage as
  `where <field> <op> <value>`, `take`, `limit` or `project`, dropping anything
  else, so `and`/`or` folded into a comparison's value and a pipe inside a
  string literal split the query. It became a tokenizer that honours single,
  double, verbatim and obfuscated string literals with their escapes, a typed
  expression parser (`and`, `or`, `not()`, parentheses, the comparison, string,
  `in`/`in~`, `has_any`, `between` and `matches regex` operators, arithmetic
  over numbers, datetimes and timespans), and the tabular operators `where`,
  `take`/`limit`, `project`, `project-away`, `project-rename`, `extend`,
  `order by`/`sort by`, `top`, `count`, `summarize` and `distinct`, where each
  `extend` item sees the columns the items before it define. Every
  expression is typed against the schema before a row is read, so an unknown
  table, column or function, a type mismatch, or an operator the engine does
  not run answers HTTP 400 `BadArgumentError` with a nested `SyntaxError`
  (`SYN0002`, with line, position and token) or `SemanticError`, as the
  service does — alone and per member of a `$batch`. An unknown table had been
  read with the Container Apps console schema; the Application Insights tables
  got their own schemas instead. The request's `timespan` bounds
  `TimeGenerated` before the query runs.
- **A proxy's bound comes from the resource, and idle is not a deadline.**
  The Application Load Balancer data plane had bounded every request at a
  fixed 30 seconds and Container Apps ingress at ten minutes. `lbplane` keeps
  two bounds apart: `Upstream.Timeout` ends an exchange at a fixed time, and
  `Upstream.IdleTimeout` ends it only once no byte has moved either way for
  that long, covering an upgraded connection too, with `Upstream.Activity`
  letting the caller move its `sim.DeclareWait` out as bytes flow. The
  Application Load Balancer reads `idle_timeout.timeout_seconds` (1 to 4000,
  default 60, refused outside that range as `InvalidConfigurationRequest`)
  for each request, so `ModifyLoadBalancerAttributes` governs the next one,
  and answers its `504 Gateway Time-out` page when a target stays silent past
  it. Container Apps ingress bounds a request at the service's documented 240
  seconds and answers Envoy's `504 upstream request timeout`.
- **A page token proves where it came from.** Every listing tags the tokens it
  issues and refuses one it never issued with the service's invalid-argument
  error, instead of listing an empty page.

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
A stopping Amazon ECS task signals all its containers together, so it takes
as long as its slowest container rather than the sum of their timeouts, and
the awsvpc pause container that holds the task's network namespace stops only
after the containers that run in it have exited; a task start reports the
pause image's resolution (`pause-image`) apart from the pause container's
start (`pause-start`).
An Amazon ECS container definition's `workingDirectory` reaches the engine,
which creates the directory when the image lacks it, and an ExecuteCommand
session inherits it. An AWS Lambda invocation's timeout starts when the
runtime first asks for work, with Init separately bounded at ten seconds, as
AWS documents.

The engine allocates every loopback port a workload publishes, and the
simulator reads the bound port back from the container's inspection after each
start (`ContainerHandle.PublishedPort`, `sim.PublishedHostPort`). The
simulators had picked a free port by listening on `127.0.0.1:0` and closing the
listener before asking the engine to publish it, so anything that bound the
port in between failed the start with "port is already allocated"; the
database engines, the Memorystore for Redis engine, Cloud Run services, Cloud
Functions, Azure Functions HTTP sites and AWS Amplify Hosting compute stopped
choosing ports. A stopped container holds no published port and a resumed one
holds a new one, so adopting an engine left by an earlier process reads its
ports after resuming it.

An App Service or Azure Functions site that names a container image runs it
the way App Service runs a Linux custom container, and nothing else. The image's
own ENTRYPOINT runs with `siteConfig.appCommandLine` (`az functionapp config set
--startup-file`, terraform's `app_command_line`), or a sitecontainer's
`startUpCommand`, as its command in place of the image's CMD; the app settings
and `PORT` are its environment; and the front end forwards every request on
one of the site's hostnames, any method and path, to the container's port —
`WEBSITES_PORT`, else 80, or the main sitecontainer's `targetPort` — passing
the container's status, headers and body back untouched. A request starts the
container when none runs, and Always On (`siteConfig.alwaysOn`) starts it
without one; the start waits until the port accepts a connection, the
container exits, or `WEBSITES_CONTAINER_START_TIME_LIMIT` (230 seconds unset)
passes, and fails the start with 503. A restart, a configuration write and an
app-settings write restart the container and keep its VNet integration;
`PATCH config/web` merges onto the stored configuration. The host had run a
site's command from base64 JSON in two app settings named after a downstream
consumer (`SOCKERLESS_CMD`, `SOCKERLESS_ENTRYPOINT`), in a container per
invocation whose stdout became the response, and told a long-lived HTTP site
from a raw service by two more such settings; none of those is an Azure
setting, and the simulator reads none of them.

A Cloud Run service is served at its own URL,
`https://<service>-<hash>-<region>.a.run.app`, which `uri` reports and the
Knative projection reports as `status.url`. The front end dispatches on the
Host header, so a client connects to the simulator's endpoint (the address a
resolver would hand it) and names the service host, as with App Service. Every
method and path goes to the ingress container on `ports[0].containerPort`
(8080 unset, passed as `PORT`), bounded by the revision template's `timeout`
(300 seconds unset), and the container's status, headers and body come back
untouched; the caller's bearer reaches the container with its JWT signature
replaced by `SIGNATURE_REMOVED_BY_GOOGLE`. A service with `invokerIamDisabled`,
or one whose policy grants `run.routes.invoke` to `allUsers`, is public. Any
other request carries a Google-signed ID token — in `X-Serverless-Authorization`
when the caller sends it, which leaves `Authorization` to the application, or in
`Authorization` — whose audience is the service URL or a custom audience, and
the principal the token names must hold `run.routes.invoke` on the service
through the service's policy or one it inherits from its project, folders and
organization, conditions included. An OAuth access token is not an ID token: it
answers 401, as does an ID token for another audience, and a principal without
the permission answers 403 with Google's front-end error page. The front end
had admitted any simulator-signed access token, or any ID token for the
service, whatever principal it named. The token endpoint's `id_token` for a
JWT-bearer grant names the grant's `target_audience`, as Google's does, so the
`google.golang.org/api/idtoken` service-account flow reaches the service it
names; it had carried the simulator's API audience instead, and the Compute
Engine metadata server's identity token had carried that audience beside the
requested one, so a Google API accepted either as an access token. Both now
name only the requested audience, and a Google API answers them 401. Pub/Sub push and
Eventarc deliver to a run.app endpoint the simulator serves through this front
end, under the same check on the identity their OIDC token names.
The simulator had served services at `POST /v2-services-invoke/{project}/{location}/{service}`,
answered 200 whatever the container said, and lifted a consumer's
`X-Sockerless-Exit-Code` header into its own; the route and the header went,
and with them `workload.ExitCodeHeader`, so `PostBootstrap` reads an error
status as a failed invocation. An instance takes traffic once its startup
probes succeed: the container's configured `startupProbe` (`tcpSocket`,
`httpGet` with 2xx–3xx success, or `grpc` health), with the API's defaults of a
1-second timeout, 10-second period and threshold 3, or the TCP probe on the
container port Cloud Run applies when none is configured (timeout and period
240 seconds, threshold 1); sidecars that configure one are probed after it.
The probes and the traffic go to the container's own address wherever the host
routes to it, which a connection the container accepts or refuses proves,
rather than through the engine's published loopback port, whose userland proxy
accepts a connection before the workload listens, so a bare TCP accept there
proved nothing and reset the first request; a host that routes no container
address reaches the workload only through that port. A probe that fails its threshold, or a container that
exits first, fails the instance and answers 503, and `workload.FirstReachable`
was removed.

A Cloud Run function is served by the Cloud Run service CreateFunction creates
for it, as on Cloud Run functions. `serviceConfig.uri` reports that service's
run.app URL and the function's `url` its
`https://<region>-<project>.cloudfunctions.net/<function>` URL; the run.app
front end serves the first and a cloudfunctions.net front end, dispatching on
the Host header the same way, takes the function's name off the front of the
path and hands the request to the same service, admitting an ID token for
either URL. The service's IAM policy governs invocation on both URLs, as it
does on Cloud Run functions: `gcloud functions add-invoker-policy-binding`
and `--allow-unauthenticated` write roles/run.invoker into it through the
Cloud Run Admin API, and deleting a service, directly or with its function,
deletes its policy, so a service created again under the same name starts
private. The container's answer passes through on both. The function's
`timeoutSeconds`, environment variables and CPU become the service template's
request timeout (60 seconds unset), container environment and CPU limit at
CreateFunction, and an UpdateFunction that changes `serviceConfig` rolls the
service to a new revision while the output-only `uri` and `service` survive it.
DeleteFunction deletes the service, its revisions and its instance, so both URLs
then answer 404. The simulator had served functions at
`POST /v2-functions-invoke/{functionID}`, reported that URL as
`serviceConfig.uri`, started a container per invocation, answered 500 for any
error status the container returned, and logged a "Function invoked" line for a
function with no image; the route, the per-invocation container path and the
synthetic log line went.

A Linux web app on a built-in runtime stack runs the platform's own image for
that stack. `siteConfig.linuxFxVersion` `NODE|20-lts`, `NODE|22-lts` or
`PYTHON|3.12` selects `mcr.microsoft.com/appsvc/node` or `appsvc/python`,
pinned to one build, with the site's `/home` mounted from the simulator's data
directory and `site/wwwroot` filled from what the site's MSDeploy and OneDeploy
operations deployed, or from the package a `WEBSITE_RUN_FROM_PACKAGE` URL
names, mounted read-only. The image's own entrypoint runs Oryx, which starts
the startup command, else the app it detects (`server.js`, a `start` script,
`app.py` under gunicorn), else the platform's "waiting for your content"
default page; the front end forwards to the port the image declares (8080 for
Node, 8000 for Python) unless `WEBSITES_PORT` names one. A deployment restarts
such a site. The runtime-stack catalogs (`webAppStacks`, `availableStacks`)
list exactly these stacks from the same table, so `az webapp list-runtimes` and
`az webapp create --runtime` see what the site path runs. A site created
without a kind reports `app`, or `app,linux` on a reserved plan, rather than
`functionapp`, and every site reports `hostNameSslStates` with its Standard
and Repository (SCM) hostnames, which `az webapp deploy` and the azurerm
provider read the SCM host from. Before this, a site with no container image answered every
request on its hostname with 200 `{}` and an AppTraces row reading "Function
invoked" without running anything; a site the simulator has nothing to run for
now answers 503 naming what it lacks — a function app without an image (the
simulator runs no Azure Functions host), a stack it does not run, or no runtime
at all — and the authLevel and invoke tests run against container sites.

A web app's SCM site serves Kudu's deployment API. The Repository entry of
`hostNameSslStates` — the host `az webapp deploy`, `az webapp deployment source
config-zip` and terraform-provider-azurerm's `zip_deploy_file` read and send the
artifact to — had named `<app>.scm.azurewebsites.net`, which resolves to no
simulator, and nothing answered there. It became a coordinate like the other
data planes: the app's subdomain of the ARM request host (`<app>.scm.<host>`),
or the `appServiceScm` template of `SIM_AZURE_ARM_EXTERNAL_DATA_PLANE_URLS_JSON`,
and a handler wrapper routes a request whose Host is an app's or slot's SCM
hostname (or the platform's own `<app>.scm.azurewebsites.net`) to that site's
Kudu through a generation index. Kudu admits the site's publishing credentials
(`$<app>`, a slot's `$<app>__<slot>`) or the subscription's deployment user as
basic auth while the site's scm basic publishing credentials policy allows it —
the policy became stored state, where its PUT had been ignored and every read
answered `allow: true` — and a Microsoft Entra token for Azure Resource
Manager or App Service. `/api/zipdeploy` (KuduSync semantics: files the last
zip deployment wrote that the new package lacks are deleted) and
`/api/publish?type=zip|war|jar|ear|lib|static|startup` (OneDeploy's per-type
targets, `path`, `clean` and `restart`) land the artifact through the one
placement the Azure Resource Manager OneDeploy and MSDeploy operations share,
synchronously (200) or with `isAsync`/`async` (202 and a `Location` on
`/api/deployments/latest`), behind a per-site lock that answers a concurrent
deployment 409. Each deployment is a Kudu record — `/api/deployments`, `/latest`,
`/{id}`, `/{id}/log`, and the legacy `/deployments` the provider's warmup reads
— that Azure Resource Manager's `/deployments` proxies, plus a deploymentStatus
that goes `BuildInProgress`, `RuntimeStarting`, then `RuntimeSuccessful` once
the restarted site answers the platform's warmup request, or `RuntimeFailed`
naming why it did not start, which is what the Azure CLI polls after a Linux
deployment. `WEBSITE_RUN_FROM_PACKAGE=1` makes each zip deployment the whole of
wwwroot, mounted read-only. The CLI suite reaches the `.localhost` SCM host
through an HTTPS proxy the test runs, the CLI analogue of the SDK suite's
dialer: the commands and their requests are the ones a real deployment sends.
Because the SCM site sits behind a handler wrapper, every store read a Kudu
deployment reaches answers from a generation index — a site's Kudu
deployments, webjobs and host-name bindings, and a webjob's runs — instead of
a full-store scan.

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

Each Amazon RDS volume is named `sockerless-rds-<kind>_<identifier>` for an
instance, cluster, snapshot or cluster snapshot. No RDS identifier contains an
underscore, so no two resources share a volume, as an instance `cluster-x` and
a cluster `x` once did. At startup the simulator copies a volume from its
earlier name into the new one, first removing any engine container still on
it. When two resources claimed the same earlier name, it leaves that volume
for the operator. DeleteDBInstance and DeleteDBCluster with a final snapshot
keep the resource `deleting` until the capture and teardown finish, as Amazon
RDS does. While it is deleting, its identifier answers `DBInstanceAlreadyExists`
or `DBClusterAlreadyExistsFault` to a create and `InvalidDBInstanceState` or
`InvalidDBClusterStateFault` to further actions. The teardown acts only while
the record still carries the resource ID it was started for. A restart resumes
an unfinished capture, copy or deletion. A final snapshot whose capture an
earlier simulator left unfinished, after it had already dropped the resource,
takes that resource's volume as its own.

An Amazon RDS instance's log files are its engine's own output.
`dbengine.Instance` hands the engine container's lines, each dated by the
container runtime, to a sink. The Amazon RDS sink stores them by hour under the
instance's resource ID, so they outlive a stop, a reboot and a simulator
restart. An adopted container replays its output from the start, and the sink
drops every line no later than the last one its stream recorded.
DescribeDBLogFiles and DownloadDBLogFilePortion lay the lines out as Amazon RDS
names the files. PostgreSQL gets one `error/postgresql.log.YYYY-MM-DD-HH` file
per hour, kept for 4320 minutes. MySQL and MariaDB get `error/mysql-error.log`,
which RDS moves into `error/mysql-error-running.log` every five minutes, and
the hourly `error/mysql-error-running.log.N`, numbered by the hour it rotated
and kept for 24 hours; the AWS CLI's describe-db-log-files example fixes those
names and times. Sizes and LastWritten come from the lines themselves. An
instance whose engine has not started has no log files, and a deleted
instance's output goes with it. DownloadDBLogFilePortion follows the model: the
most recent lines without a Marker, the lines after the marker (a byte offset,
`0` being the start) with one, at most 10,000 lines by default and 1 MB in any
case.

Memorystore for Redis instances and Memorystore for Redis Cluster clusters run
a real Redis engine, one container per resource whose redis-server processes
are its nodes, on the image the instance's `redisVersion` names (clusters run
7.2). The create starts the engine and settles only once every node answers
and, for a cluster, the slots are assigned and every replica has synchronised,
so a resource reported ready is serving; the delete stops it and removes its
volume. The `host`, `readEndpoint` and discovery endpoint the API reports are
per-resource loopback listeners at port 6379 relaying to the engine, and each
cluster node announces a loopback address at its own container port, because a
replica replicates from the address its primary announces and that address
has to reach the primary inside the container as well as from the host. AUTH
is the engine's `requirepass`, whose value `getAuthString` returns; a failover
promotes a replica and moves the primary endpoint to it. An export has the
primary write its RDB with SAVE and stores that file in Cloud Storage; an
import and an upgrade stage the snapshot as the primary's `dump.rdb` and
restart the engine on it, removing the file once loaded so persistence stays
off; a cluster backup keeps every shard's RDB, which `backups:export` writes to
the bucket. A simulator started API-only (`SIM_RUNTIME=process`) runs no
engine even when another server in the same process holds a container client,
and its instances report no host.

Firecracker boots Compute Engine, Amazon EC2 and Azure virtual machines where
the host kernel allows it, over its default virtio-MMIO transport: the opt-in
PCI transport never delivers the first virtio-blk completion on aarch64, and
CI's x86_64 runners could not see it. A machine's disk outlives the guest
process, so a stopped machine can be generalized and captured, and a
deallocated machine keeps its disk while a deleted one discards it.
Stopping or terminating a pending Amazon EC2 instance cancels its boot instead
of waiting for the guest to answer. Every SDK test that launches instances
terminates them when it ends, since each one left running kept a machine
booting or running beside every later test's boot on the same runner.

The fabric keeps a network's or machine's lock only while a caller holds or
waits for it, so its lock maps shrink as networks and machines go away.
Creating an Azure file share or inserting a Cloud Storage bucket makes its
empty host directory and fails the request when it cannot. The mount helpers
only name the directory, and a bucket reuses no files that a deleted bucket
of the same name left behind.
Removing a Docker network the engine refuses because a container is still
attached waits for that container's disconnect event and tries again, instead
of retrying on a timer. Deleting a Cloud DNS managed zone succeeds whatever
still resolves through it, as the service does; the Docker network behind the
zone goes in the background once its last container disconnects, and a failure
is logged rather than dropped.

Every Amazon EventBridge Scheduler target call runs as the schedule's
execution role, which must trust `scheduler.amazonaws.com` and allow the
call; a denied call fails with `AccessDeniedException` and reaches the
dead-letter queue. An EventBridge target puts its event through the bus's own
PutEvents path, so the bus's rules deliver it. A universal target calls an
awsJson or awsQuery API action through the Step Functions AWS SDK dispatcher,
checking every action and resource the request names against the role.
Create and update reject values outside the ranges the model declares, and
ListSchedules and ListScheduleGroups page by MaxResults and NextToken. The
IAM gate reads an Amazon SQS request's queue from a JSON body as well as from
query parameters, so a queue-scoped grant admits the awsJson protocol the SDKs
send.

An EC2 Auto Scaling instance refresh runs in the background. It replaces
instances in batches that keep MinHealthyPercentage in service, or launches
ahead of terminating under MaxHealthyPercentage, and waits out each batch's
instance warmup (the preference, else the group's DefaultInstanceWarmup, else
its health-check grace period). It honours checkpoints, bake time,
SkipMatching and the Standby and scale-in-protected preferences, and moves the
group onto its desired launch template when it succeeds. CancelInstanceRefresh
stops a refresh, and RollbackInstanceRefresh reverses only one still under
way. CreateAutoScalingGroup and UpdateAutoScalingGroup take a launch template,
and members report the template version they launched from.

GetCallerIdentity reports the signing identity's own unique ID: a user's ID,
`<role ID>:<session name>` for an assumed role, `<account>:<name>` for a
federated user. A credential that resolves to no identity fails with
`InvalidClientTokenId`, in GetCallerIdentity, GetWebIdentityToken and
GetDelegatedAccessToken, and with `InvalidAccessKeyId` in an Amazon S3
Express One Zone CreateSession. GetDelegatedAccessToken's credential acts as
its caller.

A simulator binds its port before it prints its banner, so the banner's
`Listening on` line means the port answers, and the SDK, CLI and Terraform
harnesses start a simulator through `testutil/simready`, which returns on that
line or fails when the process exits first, instead of polling `/health`. The
parent-process watch and the container reaper wait on the parent's exit event,
a pidfd on Linux and a kqueue `NOTE_EXIT` filter on macOS, rather than probing
it on a timer.

An Aurora cluster's engine serves a second, read-only address. Sessions on the
reader endpoint (once a replica exists) and on a reader instance's own endpoint
open read-only in the engine itself: PostgreSQL's startup packet carries
`default_transaction_read_only=on`, and MySQL sessions run
`SET SESSION TRANSACTION READ ONLY` before the client sees its login succeed.
The writer's endpoints stay read-write.

An Aurora DB cluster snapshot carries the cluster volume's data.
CreateDBClusterSnapshot and the final snapshot DeleteDBCluster takes answer
`creating` and settle `available` once `sim.CaptureVolume` has copied the
cluster volume into the snapshot's own volume (`failed` with the copy's error
otherwise); the final snapshot's capture runs before the engine stops and its
volume goes. CopyDBClusterSnapshot answers `copying` and clones the source's
volume, and DeleteDBClusterSnapshot refuses a snapshot still being taken and
removes its volume. The snapshot holds the cluster's sealed master credential
and the one its engine holds. RestoreDBClusterFromSnapshot and
RestoreDBClusterToPointInTime with `UseLatestRestorableTime` record the new
cluster `creating` with that credential, bind its endpoints, seed its cluster
volume from the snapshot's or the source cluster's in the background, and land
it `available` (`incompatible-restore` when the seed fails); every endpoint,
instance endpoints included, refuses clients while the cluster is not
available, so the engine first starts on the seeded volume. A process restart
resumes a capture, copy or seed in flight. The SDK suite proves it with pgx
and the MySQL driver: a restore from a copied snapshot holds the rows written
before the snapshot and none after, a point-in-time restore holds every
committed row, and a restore from the final snapshot holds the cluster as it
was deleted.

Every volume capture holds one crash-consistent point in time, the property a
block-level storage snapshot gives. `sim.SnapshotVolume` lists the running
containers that mount the source volume writable, pauses each through the
Docker Engine API's cgroup freezer for the length of the `cp -a`, and thaws it
after; a per-container hold count lets concurrent captures of one volume share
the freeze, and `sim.AdoptContainer` thaws a container a dead process left
frozen. A copy that walked the files while the engine wrote had held each file
as of a different moment, which crash recovery could not always open. The
frozen engine keeps its client connections, whose I/O waits out the copy, as
a Single-AZ RDS instance's I/O suspends briefly during its snapshot. Amazon RDS
instance and cluster snapshots, final snapshots, point-in-time clones of a
running cluster and the Azure Database for PostgreSQL flexible server backups
all go through it. A `sim` test captures a volume twice at once while a
writer renames 64 files to each round number in turn and proves both captures
show a single instant of the writer; the SDK suite snapshots a PostgreSQL
instance under a transaction stream and proves the restore holds a gapless
sequence prefix whose length both account balances agree with. The Docker
Engine refuses `exec` into a paused container, so `dbengine.Instance.Exec` ran
each engine command — a ModifyDBInstance master-password change, a Cloud SQL
or Azure Database for PostgreSQL user change — under `sim.HoldThawed`, which
waited on a channel for the last capture to thaw the container and kept new
captures from freezing it until the command ended; the change applied after
the brief I/O suspension, as the real service applies it.

Artifact Registry stores the bytes of uploaded files and serves them back from
`files.download`. The generic, Go module, KFP, Apt, Yum and GooGet uploads create
the package, version and file their methods describe, reading each format's
own metadata (a .deb control file in any of its compressions, an RPM header, a
`.pkgspec`, a module zip), and `:import` publishes Cloud Storage objects the same
way, reporting a bad object in the response's `errors`. `exportArtifact`
resolves a version through the recorded package versions, so a pushed Docker
image exports its manifest, config and layers, and deleting a version, package
or repository deletes its files.

`files.upload` and `genericArtifacts.upload` speak Google's resumable media
protocol as well as the simple one, the two methods whose Discovery
`mediaUpload` declares it. A session begins with `uploadType=resumable` on the
`/upload` path (the Go client, googleapiclient) or the `/resumable/upload` path
(apitools), answers 200 with the session URI in `Location`, and takes chunks
POSTed or PUT there under `Content-Range`, answering 308 with a `Range` of the
bytes received — 200 with `X-Http-Status-Code-Override: 308` for a client that
sends `X-GUploader-No-308` — and the method's own response on the last byte; a
`bytes */*` query reports progress, and a DELETE cancels with 499. The session
lives in the store, so a restart does not lose it. A Docker push records a
File per manifest and per referenced blob, named by digest and served from the
registry, so an exported blob's `files/<digest>` resolves; a layer two images
share is owned by the first to push it and passes to another on deletion.
`files.delete` refuses repositories that are not generic, and `registryUri`
follows the repository format as gcloud's `AddRegistryBaseToRepositoryInfo`
spells it: `LOCATION-FORMAT.pkg.dev/PROJECT/REPOSITORY`.

Every resumable path Discovery declares is served. The conformance loader and
the response validator index `mediaUpload.protocols.resumable.path` beside the
simple path, so the `/resumable/upload/...` routes are checked as Discovery
methods rather than allowlisted, and a PUT or DELETE is accepted on any media
path of a method that declares the resumable protocol, the session URI the
protocol addresses. The session protocol is one module, `media_upload.go`,
shared by Artifact Registry and BigQuery: a session keyed by its `upload_id`,
owned by the resource and method it began on, finished by the method's own
callback. Cloud Storage keeps its own sessions, because they stage bytes in the
object payload store, but places chunks, answers 308 and builds the session URI
through the same helpers: `objects.insert` takes sessions on
`/resumable/upload/storage/v1/b/{bucket}/o`, the session URI echoes the path and
query the session began on, a `bytes */*` query reports progress, a chunk that
would leave a gap is refused, and a finished session answers later requests
with the object's metadata. BigQuery `jobs.insert` runs a load job on its media
paths, whether the source data arrives in one multipart request or a resumable
session: it parses CSV (`skipLeadingRows`, `fieldDelimiter`, `nullMarker`,
`allowJaggedRows`) or newline-delimited JSON (`ignoreUnknownValues`) against the
table's schema or the job's, honours `createDisposition`, `writeDisposition`
and `maxBadRecords`, and reports a failed load as a DONE job with an
`errorResult`. Before, the media path parsed the request body as a JSON Job and
dropped the bytes.

BigQuery load, copy and extract jobs ran as real jobs. `jobs.insert` recorded
the job PENDING and answered at once; a goroutine moved it to RUNNING, did the
work, and settled it DONE with its statistics and, on failure, an
`errorResult`. A job checked its destination and wrote it under the table's
lock as one atomic update, `jobs.cancel` and `configuration.jobTimeoutMs`
stopped a running job before it wrote anything, a job ID could not be reused,
and a restart settled the jobs it caught unfinished. A load read its
`sourceUris` from the simulator's Cloud Storage store, one `*` per URI matching
any run of an object name, inflated gzip sources, and parsed CSV through its
own splitter (`quote`, `allowQuotedNewlines`, `encoding`, `nullMarkers`) so
quoting followed BigQuery rather than `encoding/csv`. Every cell was converted
to its column's type and stored in the string form `tabledata.list` carries, so
a value the type could not hold became a bad record. `autodetect` detected CSV
and newline-delimited JSON schemas, including CSV headers and nested and
repeated JSON fields; AVRO loads went through `hamba/avro` and PARQUET through
`parquet-go`, taking the table's schema from the file. A copy wrote the rows of
one or more tables sharing a schema under the create and write dispositions,
and an extract wrote CSV, newline-delimited JSON, Avro or Parquet objects,
compressed and sharded through a wildcard URI. `numBytes` was computed from the
stored rows by BigQuery's logical size of each type, and `tabledata.list`
nested RECORD and REPEATED cells in their wire form. The media-load tests waited
on the job through the client's `Job.Wait` instead of reading a DONE state off
the insert response.

Compute Engine `instances.start`, `stop`, `suspend`, `resume`, `reset`,
`startWithEncryptionKey` and `delete` answer at once with a RUNNING zone
operation, move the instance through STAGING, STOPPING or SUSPENDING, and do
the machine's work in the background, settling the operation when it ends.
`reset` is a hard reset: it halts the guest and boots it again while the
instance reads RUNNING, and a reboot that fails leaves the instance
TERMINATED. `delete` refuses an instance with `deletionProtection` before
anything moves. The operations' `wait` methods block on a signal
`computeOpFinish` raises when the operation reaches DONE, bounded by the
documented two minutes, instead of re-reading the record on a timer, and
`zoneOperations.delete` and its regional and global siblings delete the record
and wake its waiters. An Azure VM DELETE answers 202 with the
long-running-operation headers and `SimulateEviction` answers 204, and both
stop the guest in the background; the machine leaving takes its name off the
`properties.virtualMachine` of every network interface it had attached.

Cloud Run's v2 `RunJob` answers with an operation that stays running while the
execution runs, carrying the Execution as its metadata, as the method's
`operation_info` declares; the execution's settle completes it once the tasks,
the job's reference and the log lines are written — the Execution as the
response when every task succeeded, the failed `Completed` condition's message
under FAILED_PRECONDITION when one failed, CANCELLED when it was cancelled, and
NOT_FOUND when the execution was deleted while it ran. Cancelling the operation
cancels the execution, and a restart completes the operations of executions
that settled without them. `operations.wait` on REST and `WaitOperation` on
gRPC block on a signal `gcpFinishOperation` raises, bounded by the request's
`timeout` or else by the caller's connection. Cloud Build's `CreateBuild`, its
regional twin, `RetryBuild`, `ApproveBuild`, `RunBuildTrigger`, the trigger
webhooks and Cloud Run's `builds:submit` record the build QUEUED and return its
operation at once, with a `BuildOperationMetadata` carrying the build; the
build runs in the background under its own `timeout` (sixty minutes unset,
ending TIMEOUT with DEADLINE_EXCEEDED), and the operation is a view of the
record that turns done once the build is terminal and its steps have stopped,
with the full Build as the response. Before, `RunJob` returned its operation
done the moment the execution started and `CreateBuild` held the request until
the build ended, so the SDK, CLI and bash suites polled execution and build
status; they now wait on the operation — `op.Wait`, `operations.wait`, or the
Cloud Build SDK's `CreateBuildOperation(name).Wait` — and read the execution's
name from the operation's metadata where the workload must still be running.

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

Azure Container Registry Tasks' `scheduleRun` records the Run Queued and
answers 200 with it at once — the Azure CLI's own registry-tasks client accepts
nothing else, and the Go SDK's poller completes on a 200 that names no
operation — and the build runs in the background, Queued → Running →
Succeeded, Failed, Canceled, Timeout (the DockerBuildRequest `timeout`, 3600
seconds unset, 300 to 28800 accepted) or Error when the simulator stops under
it; a restart ends the runs a previous process left unfinished. `Runs_Cancel`
interrupts the run's docker steps and answers once the run has stopped and
reads Canceled. The run's log is a blob at its log link that grows while the
build writes it, serves the Blob service's ranged reads, and gains `Complete`
metadata when the run ends, which is what `az acr build` streams until.
Before, `scheduleRun` held the request until the build ended and answered with
the finished Run, so no client could see a running build or cancel one; the
SDK and CLI suites now take the Queued Run from the poller and follow the Run's
own status, and cancel a build mid-run.

`scheduleRun` ran only a DockerBuildRequest whose source was a full blob URL,
so `az acr build .`, `az acr run`, `az acr task run` and
`azurerm_container_registry_task_schedule_run_now` could not run. All four run
requests now go through the one background run lifecycle. A
FileTaskRunRequest, an EncodedTaskRunRequest and a TaskRunRequest of a task
file run the ACR Tasks YAML as the service's open-source run engine
(Azure/acr-builder) reads it: Go templates over `{{.Run.*}}` (the date as
`20060102-150405z`) and `{{.Values.*}}` from the values file under `--set` and
`--set-secret`, then from v1.1.0 the `$` aliases, custom ones and the documented
image aliases, then `build`, `push` and `cmd` steps ordered by `when`, each with
its timeout, retries, repeat, start delay, environment and ignore-errors.
Steps share the source in a run volume mounted at `/workspace`, on a run
network where each step answers to its ID; a cmd step's writes are read back
for the build steps after it. A TaskRunRequest runs the Task's Docker, FileTask
or EncodedTask step with the request's file, context, arguments and values
merged in, and reports the task's name. `listBuildSourceUploadUrl` hands out a
Shared Access Signature URL into storage the registry service owns (an account
name no customer can hold), served by the Blob service's own handlers, Put
Block List included; a run request's relative `sourceLocation` resolves to that
upload. A DockerBuildRequest renders its image names and places a name without
a registry in the run's registry. An agent pool runs as many runs as its
`count` of agents, the rest wait Queued, and `listQueueStatus` counts them.
The run engine drives its cmd and push steps through the Docker Engine API,
not the docker CLI: it creates the run's network and volume, creates each step
container on that network under its step-ID alias, seeds the volume through
the archive endpoint and reads it back the same way, and removes the
containers, images, network and volume at the run's end. Pulls and pushes to
the run's registry present the run's identity token as the engine's registry
credential; only `docker build` stays a CLI invocation. A step's output
reaches the run log through a followed log stream opened at the container's
start, which carries all of it even when the container exits before a reader
attaches, and its exit code through a next-exit wait registered before the
start.

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
Deleting an Amazon ECR repository deletes the same ranges: its images and
registry manifests under `<repository>:`, its blobs and layers under
`<repository>@`, and its lifecycle and repository policies, so a repository
created again under the same name starts empty.

A Cloud Run workload's Cloud Storage volume was a one-way mirror: the bucket's
host directory showed every live generation, and nothing the workload wrote
there ever became an object. The mount now writes back the way Cloud Storage
FUSE does, without a FUSE file system: while a workload mounts a bucket
writable, one inotify instance watches every directory of it, and the events
map onto Cloud Storage FUSE's object operations — a close after writing
(`IN_CLOSE_WRITE` after `IN_MODIFY` or `IN_CREATE`) is a new generation, never
each write; a `mkdir` is a placeholder object; an unlink, an `rmdir` or a
rename out of the mount is a delete; a rename within it is a copy and a delete.
Each write is conditioned on the generation the file held, as Cloud Storage
FUSE conditions its flush, keeps the object's metadata, names a new file's
content type from its extension and records `gcsfuse_mtime`. Telling the
workload's changes from the simulator's own is done by state, not by event:
the simulator keeps a view of what each path stands for (generation and inode),
every change it makes to the directory and the record of it happen under one
mutex, and its mirror of an API write is staged outside the bucket directories
and renamed in, so a watch event only prompts a comparison of the path with the
view. `IN_EXCL_UNLINK` drops the close of a file an API write has already
replaced. Ingestion is asynchronous to the workload's close, so a barrier —
closing a file of the simulator's own under the data root and waiting for that
close to come through the same queue — makes every Cloud Storage JSON API
request, and the end of a job task, wait for the events queued before it,
which is the read-your-writes Cloud Storage FUSE gives by writing before
`close` returns. A file is opened with `RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS`,
so a link a workload makes never reads a host file into a bucket. The `only-dir`
mount option binds that directory of the bucket; the Knative v1 surface
carries the options in the CSI `mountOptions` attribute; a volume of a bucket
that does not exist fails the workload's start.

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
through the vendored curated roles and the held custom roles. A conditional
binding grants its role only while its condition holds: the simulator compiles
the Common Expression Language expression with `cel-go` and evaluates it
against `request.time` and the resource's `name`, `type` and `service`, and an
expression that fails to compile or evaluate — an attribute the resource does
not supply, a function IAM offers that the simulator does not — grants
nothing. The Cloud Run front end's invoker check and `testIamPermissions` are
one evaluation, so a binding that a test reads back as granting is the binding
that admits a request. A caller
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

The slow-request diagnostic (`sim.InFlightMiddleware`) reports a request only
once it outlives the wait its handler declared by `SlowRequestThreshold`. A
method that blocks by design names its bound on the request context:
`sim.DeclareWait` for a long poll, an operation wait or a synchronous run of
the caller's workload under its configured timeout, `sim.DeclareOpenEndedWait`
for a stream or a wait the caller left without a timeout. The middleware
treats an upgraded connection as open-ended by itself, so every WebSocket
session is covered without a declaration. The framework holds no list of such
methods; each cloud's handler says what the cloud documents. Reporting every
`operations.wait` that blocked past ten seconds had pointed the goroutine-dump
hint at healthy requests. The watcher reads an injected clock, and the
middleware waits for it before finishing a request, so its tests step time
instead of waiting it out and a report cannot land after the request ended.

The race detector runs on every pull request over every module. `bg.Go` and
`bg.AfterFunc` count goroutines and pending timers; a drain (`bg.Await`) is a
barrier that stops unfired timers and drops work requested while it runs,
because a reconciliation requests another whenever it moves a task.
`bg.JoinedGo` counts work a caller waits on and never drops it, since dropping
a fan-out the caller joins leaves it waiting forever. Finite work handed to
`Server.StartBackground` registers with the drain too (`bg.Handoff`); lifetime
daemons do not, or the barrier would wait forever.

A test waits on the event it asserts. The AWS suites use the SDK's own
waiters, with 250 ms to 2 s delay bounds instead of the published 5 to 60 s:
`InstanceRunning`, `SnapshotCompleted`, `TasksStopped`, and `ServicesStable`
with a further acceptor that needs the rollout COMPLETED. Where no waiter
exists, they use a long poll, or Live Tail opened before stored history is
read. The SQS retention test sets a message's visibility timeout to end just
after the retention deadline, then long-polls, so a message SQS kept would
come back at that moment. Waiting for full steady state slowed a few ECS tests
by a few seconds; that cost is accepted over checking a partial condition. EC2
Auto Scaling answers a capacity change at once: members join Pending, each
launch has an InProgress activity, and a background boot moves both on.

An Azure test that starts a long-running operation by hand waits for it the
way a client does. The SDK suites hand the raw response to azcore's own poller
(`awaitARMOperation`), which follows Azure-AsyncOperation or Location on the
Retry-After cadence; the CLI suites drive `az rest --debug` through
`azLongRunning`, which reads the same headers and answers a Location poll's 202
by waiting its Retry-After. A delete, restore, MSDeploy publish, Redis or Event
Hubs create, or ownership acceptance is read once after its operation ends,
never polled for its effect. Every simulator a test starts in-process waits on the banner it
prints after binding (`simready`), not on a health loop. The AWS suites wait
for an alarm state with the AlarmExists waiter filtered by StateValue, for a
target with TargetInService, and watch a queue that must stay empty with one
long poll for the window instead of receives in a loop.

Azure's asynchronous work answers the request and settles behind it, as the
service does. An Event Grid webhook subscription stays Creating until its
endpoint echoes the validation code or someone opens the validation URL, and
receives events only once it has Succeeded. A Logic Apps trigger answers 202
with the run id and runs the workflow in the background. Compute polls its
operations at `locations/{location}/operations/{id}`, the shape armcompute's
recordings show; every other provider uses `operationStatuses`, and a
provider-specific path waits for a source that shows it. Cosmos DB mints
hierarchical resource ids and accepts them in paths.

Cloud Storage answers a permission question from the caller's own grants: the
project, bucket and object or managed-folder policies, and the bucket and
object ACLs while uniform bucket-level access is off. It refuses to delete a
bucket that still holds live objects, and refuses a notification whose Pub/Sub
topic is malformed, missing or closed to its service agent. Objects kept
their custom contexts through every write, copy, rewrite and compose,
`objects.list` filtered on them, and `objects.viewFullContext` read one back.
Artifact Registry
deletes cascade across both planes: a manifest DELETE over OCI removes its
version, tags and image row, and a package or repository takes everything
beneath it. The simulator's tokens carry Google's issuer, and its discovery
document names that issuer with its own key and token URLs, so a relying party
verifies them as it verifies Google's. The Google Cloud SDK tests, Cloud
Pub/Sub's included, and the AWS SDK tests name their resources per run with
`uniqueName`, so a repeated run against one simulator passes: a test that
creates a resource under a fixed name, filters a listing by a fixed tag,
aggregates a metric in a fixed namespace or reuses an idempotency token or
client token collides with its own previous run. A test that changes an
account-wide setting, such as the Amazon EC2 default credit specification or
the account's Amazon VPC encryption control, restores it at cleanup.

AWS work that the services finish later now finishes later in the simulator
too, and is gated on the real inputs. An EC2 instance stays pending until its
VM boots, whether `RunInstances` or EC2 Auto Scaling launched it, and a launch
lifecycle hook holds it in `Pending:Wait` until the hook's action completes or
times out. An awsvpc ECS task creates its network interface at `RunTask`, so
`DeleteSubnet` refuses while the task holds it. Amazon S3 checks each
notification destination's resource policy at put time and sends the test
event. `ImportSnapshot` and `ImportImage` read the disk image from S3 and
convert RAW, VHD and VMDK formats into real snapshot data. A waiting SQS
receive wakes on the send, delay or visibility change it waits for. ECS
creates an awslogs log group only when the task definition asks for it.

A Cloud Pub/Sub `Pull`, over gRPC and REST alike, holds an empty subscription
open unless the caller sets the deprecated but honoured `returnImmediately`,
and answers with at most `maxMessages` once a message becomes deliverable, the
server's bound passes, or the caller goes away; it had answered an empty
subscription at once whatever the flag said, so a client that pulled before
publishing read nothing. The wait wakes on the subscription's own signal,
which a publish, an acknowledgement, a negative acknowledgement or ack
deadline change, a seek, and the subscription's update, detachment or deletion
fire, and on a timer set to the moment the queue next changes by itself, a
lapsed ack deadline or the end of a retry backoff. `StreamingPull` waits on the
same two things instead of re-reading the queue every 50 ms. The REST pull
declares its bound with `sim.DeclareWait`; the gRPC listener sits outside
`sim.InFlightMiddleware`, so the declaration there is a no-op. Suites that
check a subscription is drained pull with `returnImmediately`, as a client
does that must not wait.

AWS Batch answers `SubmitJob` at once and schedules the job behind it. The
job moves on real events: RUNNABLE once the scheduler evaluated it, STARTING
when a compute environment of its queue that is ENABLED and has the vCPUs to
spare (`maxvCpus`) placed it, RUNNING when its container started, and a
terminal state when the container exited; a job no environment can place waits
in RUNNABLE until one can. Each attempt runs a new container with the
`AWS_BATCH_*` variables the user guide lists, logs to its own
`/aws/batch/job` stream, and is recorded in `attempts`. The request's
`retryStrategy` overrides the job definition's, and `evaluateOnExit`
conditions decide in order; a terminated or timed-out attempt is never
retried. An array job spawns `<parent>:<index>` children, lists them under
`arrayJobId`, and settles once every child has. `CancelJob` stops only jobs
that have not started, `TerminateJob` stops the containers of started ones,
and both reach an array parent's children.

A job with `dependsOn` waits in PENDING until each dependency has finished:
it becomes RUNNABLE when all succeeded and fails with `Dependent Job failed`
when one failed, and the failure cascades down the chain. An array job's
`N_TO_N` dependency pairs each child with the same index of the other array,
and `SEQUENTIAL` runs child *i* after child *i − 1*. A PENDING job that
`CancelJob` or `TerminateJob` reaches fails once its dependencies have
finished, as the `CancelJob` reference describes. A queue with a fair-share
scheduling policy takes only jobs with a `shareIdentifier`, and a FIFO queue
none. Its scheduler places next the job of the share with the least weighted
usage: the vCPUs the share's jobs hold, plus their average over
`shareDecaySeconds` under exponential decay, times the share's
`weightFactor`, matched by name or `*` prefix. Within a share, jobs go by
`schedulingPriority`, then by arrival. `computeReservation` holds back
(`computeReservation`/100)^active shares of `maxvCpus` for shares that hold
none yet. Queues sharing a compute environment are served highest `priority`
first. A queue's scheduling policy can be replaced but not added to a FIFO
queue, and a policy that a queue uses cannot be deleted.

A test asserts a boundary at a small parameterised limit rather than by
reaching the real one: the OCI body-cap tests peaked at 7.7 GiB under the race
detector on a 7 GiB runner until the cap became a parameter tested at 64 KiB.
A soak test's reader pool is sized to the machine and yields. A nightly fuzz
failure becomes a seed, so ordinary `go test` catches the regression.

An Amazon ECS test that deletes its subnet first stops the tasks of its own
cluster whose interface is in that subnet and waits for them with `aws ecs
wait tasks-stopped`, since EC2 refuses `DeleteSubnet` while an interface
remains. The test scans only its own cluster, because scanning every cluster
grew with each test and pushed the ECS CLI job past its time limit. A task
that holds the subnet traps SIGTERM, so `StopTask` ends it at once instead of
after the container's stop timeout.

Dependencies of every class — Go modules, Terraform providers, GitHub Actions,
installed tools, the consoles' npm packages — are held to their newest release
past a 24-hour adoption quarantine. `ui/bunfig.toml` sets
`install.minimumReleaseAge` to the same day, because caret ranges let the
resolver pick versions the quarantine refuses. An exact provider pin is
compared exactly and an unpinned provider is a failure. On a pull request, a
drift byte-identical to `main`'s is reported rather than failed, since upstream
moved under the branch. Every network lookup the check makes carries a
deadline and fails naming what never answered. A deliberate hold names its
cause and goes when the cause does. Resolving the module behind a workflow's
`go install` package path asks the module proxies alone: `direct` would answer
each non-module prefix by cloning the whole repository.

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
only when a package is missing. In the AWS SDK shards one shard saves the
Go cache after its pre-build and the rest only restore it, so a dependency
change no longer has every shard compress the same cache inside its time
limit. The build gates keep their own Go cache with the same fallback to an
older entry, so a pin or dependency move no longer compiles the three
simulators cold inside the job's five minutes. The workflows reference as few external
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
