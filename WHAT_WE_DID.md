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
  carried, including the fixes one copy had and the others lacked. The MySQL
  relay logs into the engine the way a client does: it answers an auth switch
  to `mysql_native_password`, `caching_sha2_password` or `sha256_password`,
  and, since its link to the engine has no TLS, completes a full
  authentication by requesting the engine's RSA public key and sending the
  password under RSA-OAEP, so a user the engine has not cached signs in and a
  refused login reaches the client as the engine's own 1045.

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
- **A resource the service moves through transitional states moves through
  them.** An Amazon Kinesis Data Streams consumer answers RegisterStreamConsumer
  CREATING and DeregisterStreamConsumer DELETING and settles as tracked
  background work, as a resharding stream passes through UPDATING; the
  Terraform provider's consumer waiters poll those states, and
  SubscribeToShard refuses a consumer that is not ACTIVE. A Client VPN
  endpoint's Cedar authorization policy answers creating, updating and
  deleting the same way. DryRun on GetRecords, GetShardIterator, PutRecord and
  PutRecords validates the request and answers `DryRunOperationException`
  instead of acting.
- **A delete the service refuses is refused.** Amazon ECR DeleteRepository
  answers `RepositoryNotEmptyException` for a repository holding images unless
  the request sets `force`, which the Terraform provider sends from
  `force_delete`; a test that deletes a repository holding images says so.
- **A NAT gateway route translates the subnets its route table governs now.**
  The translation's sources are recomputed on every association change —
  AssociateRouteTable, DisassociateRouteTable, ReplaceRouteTableAssociation,
  CreateSubnet and DeleteSubnet — and include the subnets the main route table
  governs implicitly; a route table that governs no subnet translates nothing.
  The probe that proves it reads the source address a task's request arrives
  from, because egress through an untranslated route still reached the host.
  DeleteSubnet ends the subnet's association, and DeleteRouteTable refuses the
  main route table or one still associated with `DependencyViolation`, as EC2
  does, and withdraws its NAT translations when it succeeds.
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
- **A key is proven per action, by the request a client sends.**
  `TestIAMConditionKeyCoveragePerAction` renders every served operation from
  its vendored Smithy model in the operation's own protocol with every member
  filled, classifies it with the gate's own classifiers, builds the gate's
  context for each action it is authorized as, and checks every key that
  action declares. Resources whose state some keys report — a tagged bucket,
  object and access point, a key behind an alias, a bounded role, a sized task
  definition, one tagged resource of every type Elastic Load Balancing,
  Amazon ElastiCache, AWS Systems Manager and AWS WAF tag, an Amazon ECS
  capacity provider, container instance and task set, an AWS Cloud Map
  namespace and service — are created through each service's own API first. The pairs it
  does not build are listed one by one in `testdata/iam_condition_key_gaps.tsv`
  with a reason from a closed table, so a new gap and a fixed one both fail.
  Measuring this way found keys a name-only check had credited: untagging
  requests carried no `aws:TagKeys`, AWS Lambda and the Amazon S3 control plane
  reached the gate with their tags unread, Amazon ECS resources had no
  `aws:ResourceTag/<k>`, and a role's or user's existing permissions boundary
  never reached `iam:PermissionsBoundary`. It also found PutObject dropping its
  `x-amz-tagging`, CreateUser its `PermissionsBoundary` and CreateAccessPoint
  its `Tags`. Seeding every taggable type found more: Elastic Load Balancing
  and Amazon ElastiCache read a resource's tags from a few members rather than
  from the resource the gate authorizes against, and AWS Systems Manager and
  AWS WAF read none; a task definition named by family alone resolved no
  revision; AWS Cloud Map's DeleteService and UpdateService never carried
  `servicediscovery:ServiceCreatedByAccount`; an ElastiCache global datastore
  create was not authorized against its primary replication group, and the
  simulator minted a global datastore's ARN with a region, which AWS writes
  without one. Resource tags now resolve from the ARNs the gate derives. The creates themselves were
  dropping tags: CreateCacheSecurityGroup, PurchaseReservedCacheNodesOffering
  and CreateGlobalReplicationGroup kept none, ElastiCache's tagging operations
  did not reach serverless caches and their snapshots, and every Systems
  Manager create but CreateCloudConnector discarded its `Tags`, while that one
  kept them where ListTagsForResource never looked.
- **A key about a source is read from the source.** A daemon's size is its
  daemon task definition's; a blue/green deployment's engine, name, encryption,
  Multi-AZ placement and tags are the DB instance's or DB cluster's it clones,
  and its parameter-group tags the groups it names; a DB snapshot's
  `rds:BackupTarget` is its instance's, now stored and rendered; a cluster
  restore's `rds:StorageSize` is its snapshot's or source cluster's. A key that
  differs between the entries of one request — the version each DeleteObjects
  entry names — travels with that entry's authorization target rather than with
  the request. GetAccessPoint authorizes `"*"`, as the reference says, and still
  reports the tags of the access point its path names. A web-identity or SAML
  session keeps the provider claims AWS STS declares on sts:AssumeRole and an
  AssumeRole it signs carries them, so a chained role's trust policy can still
  name the person behind the first session; the measure probes AWS STS as each
  such session as well as an IAM user. A probe whose member takes either of two
  resource kinds is rendered once per kind.
- **A function URL is served at its own host.** `<url-id>.lambda-url.<region>.on.aws`
  reaches the function as a payload format version 2.0 event and its result is
  the HTTP response; an AWS_IAM URL verifies the lambda SigV4 signature and
  authorizes both lambda:InvokeFunctionUrl and lambda:InvokeFunction, the
  second with `lambda:InvokedViaFunctionUrl`; a NONE URL admits whoever the
  function's resource-based policy admits, and AddPermission writes the
  conditions its scoping members name. CORS preflights are answered from the
  URL config without invoking the function.
- **An ARN is minted in the Service Reference's format.** An AWS Glue
  integration's ARN is `…:integration:<id>`, keyed by an id Glue assigns at
  CreateIntegration, not `…:integration/<name>`, so a policy naming the
  integration's ARN matches the ARN the IAM gate derives from the request.
- **A resource that exists implicitly is not minted on reference.** CloudWatch
  supports one metrics dataset, `default`, which every account has; GetDataset
  had created a dataset for any identifier it was given. Any other identifier
  is now ResourceNotFoundException, and the default dataset carries tags.
- **Maintenance may not end the service.** Failing loudly on a persistence
  fault is right in a handler, where net/http turns the panic into a 500. On a
  background goroutine it was a restart loop: the retention sweeper met a busy
  database and took the simulator down thirteen times in thirteen minutes. A
  busy or locked database ends the sweep, which resumes next pass; a corrupt
  row still panics; `StartBackground` contains a panic in any worker.
- **A stalled CLI call fails with the evidence.** One `aws` call in the CLI
  suite hung until the test binary's timeout and left nothing but Go stacks.
  `runCLI` now runs every call with `--debug` in a process group of its own,
  bounded by 60 s or the test's deadline less 30 s; past the bound it reads the
  simulator's `/debug/inflight` and goroutine profile on the suite's own
  diagnostics port, kills the group, and fails with both and the tail of the
  CLI's debug log. It never retries the call.
- **A stopping simulator lets go of work in flight.** A deployed simulator
  waited out systemd's 90-second stop timeout because its Amazon ECS lifecycle
  steps, AWS Lambda event source mapping batches and asynchronous invocation
  attempts ignored the context `StartBackground` hands them. Each now returns
  once that context is done and leaves its work where the next process picks
  it up: an Amazon ECS stop stays stopping and recovery finishes it, an
  interrupted start leaves the task PENDING with its partial containers removed
  so the resumed start can reuse their names, an asynchronous invocation's
  attempt is rolled back and retried, and an event source mapping's messages
  become visible again after their visibility timeout. The task lifecycle lock
  is a channel so a step can stop waiting for it. A persistent simulator stopped
  with a task inside a two-minute `stopTimeout` and a function inside a
  15-minute timeout proves the stop takes under a second.
- **A request ends with the server.** `ListenAndServe` drained the HTTP server
  for up to 10 s because no request's context was cancelled at shutdown, so an
  open CloudWatch Logs Live Tail session or a long poll held the drain to its
  bound. The `http.Server`'s `BaseContext` returns the background context the
  signal handler cancels before `Shutdown`, so every long-poll handler, which
  selects on its request's context, returns at once; a `sim` test holds a poll
  open across SIGTERM and stops in milliseconds. `StartContainerSyncContext`
  bounds the image pull and the create by its caller's context, removes a
  container whose start it abandoned under a context that outlives the
  caller's, and leaves the started container's lifetime to its handle. It
  replaced the context-less `StartContainerSync`, and every simulator call site
  passes its own context; an AWS Lambda execution
  environment starts under its worker's, so a shutdown stops an image download
  instead of waiting it out. `TestSimulatorStopsWithLifecycleWorkInFlight_SDK`
  keeps a Live Tail session open across SIGTERM and still exits within 5 s.
- **Work a request starts can outlive its caller but not the server.**
  `sim.LifetimeContext` returns, from any request's context, the server's
  background context, which only shutdown cancels; the server's outermost
  handler stores it on every request it serves, through `ListenAndServe` and
  `ServeHTTP` alike, and `Server.RequestContext` gives the same to a request a
  service builds to call a handler in-process. It panics for a context no
  server serves rather than answer with a context that either never ends or
  ends with the caller. It
  serves work like a synchronous AWS Lambda invocation, which real Lambda keeps
  running after its caller hangs up.
- **A synchronous call ends with the simulator, not with its caller.** A
  synchronous AWS Lambda Invoke, InvokeWithResponseStream, an Amazon S3 Object
  Lambda transformation, an Amazon S3 Batch Operations LambdaInvoke task, and a
  Step Functions StartSyncExecution or TestState ran their functions under
  `context.Background()`, so one in flight held the HTTP drain to its 10 s
  bound. They run under `sim.LifetimeContext`, and the Step Functions
  interpreter takes that context down to its Lambda tasks and aborts a
  synchronous execution when it ends. Step Functions calls an awsJson or
  awsQuery handler in-process with a request from `Server.RequestContext`, so
  an AWS SDK integration such as `sfn:startSyncExecution` finds the same
  lifetime. Amazon S3 event notifications and AWS Lambda function
  destinations invoke their function asynchronously, through the same
  persisted dispatcher as an `Event` Invoke, as AWS does. A Batch Operations
  LambdaInvoke task had read the invocation's unhandled flag inverted and
  counted every succeeded task as failed, and S3 Object Lambda named a failed
  function's response as a missing WriteGetObjectResponse.
  `TestSimulatorStopsWithLifecycleWorkInFlight_SDK` keeps a synchronous Invoke
  and a StartSyncExecution on a Lambda task in flight across SIGTERM and still
  stops within 5 s, and `TestS3Control_BatchJobLambdaInvoke` runs a
  LambdaInvoke job to `Complete`.
- **Recorded executions run as server background work and stop with it.** A
  Step Functions execution started by StartExecution, RedriveExecution, a
  nested `states:startExecution` task, EventBridge, EventBridge Scheduler or
  recovery, and an AWS Lambda durable execution's coordinator, run under
  `StartBackground` and invoke their functions under its context, so the
  invocation in flight ends with the process and its execution environment
  goes with it. The interpreter treats that context ending like an abort: it
  waits for the task in flight to return, records neither its outcome nor a
  Catch transition, and leaves the execution `RUNNING` at its checkpoint for
  the next process's recovery; a Parallel state waits for its other branches
  first, and a Distributed Map leaves its item executions unrecorded. An
  Amazon ECS task or AWS CodeBuild build a `.sync` task waits on keeps
  running for the next process to resume waiting on, where StopExecution
  stops it. The durable coordinator likewise drops the interrupted
  invocation's response.
  Both had run on goroutines the server did not track, under
  `context.Background()`, so a stopping simulator left their execution
  environments running until the function timeout while the next process
  invoked the same step again.
  `TestLambdaWorkInFlightEndsWithTheSimulatorAndResumes_SDK` stops a
  simulator during both invocations and finds their containers gone and both
  executions `RUNNING`, with no failure in the execution history.
- **An Amazon S3 Batch Operations job runs after CreateJob answers.** CreateJob
  stores the job `New` and hands it to the server's background workers, which
  move it through `Preparing` (reading the manifest into one task per row),
  `Suspended` when the job requires confirmation or `Ready`, `Active`, and
  `Completing`, `Failing` or `Cancelling` into its final status, writing the
  completion report on the way. UpdateJobStatus confirms only a `Suspended`
  job and cancels a running one between tasks. Each task's outcome is
  recorded as it lands, so a stopping simulator leaves the job where it was
  and `s3RecoverBatchJobs` resumes it after the last recorded task. A
  LambdaInvoke task sends the event of the invocation schema the operation
  names, 1.0 or 2.0 with its user arguments, and takes its outcome from the
  `results[]` entry for its task — `Succeeded`, `PermanentFailure`, or
  `TemporaryFailure`, which redrives the task after every other has run — with
  `treatMissingKeysAs` for a missing entry. The completion report is the
  service's: `manifest.json` and one results CSV per task status under
  `<prefix>/job-<id>/`. Tests wait on DescribeJob's status, never on
  CreateJob's answer.
- **A Batch Operations CSV manifest lists keys URL-encoded.** The
  S3BatchOperations_CSV_20180820 format requires it, so preparing a job
  decodes each key as a form value, `+` to a space as AWS Lambda Powertools'
  Batch Operations event (`unquote_plus`, tested against the service) decodes
  it, and fails the job on a malformed escape. The task keeps the listed form
  too: the LambdaInvoke event's `s3Key` carries the key URL-encoded, as the
  user guide's examples decode it, and a LambdaInvoke task invokes the function
  whether or not an object holds the key, since the guide's JSON-key manifests
  name none. `TestS3Control_BatchJobURLEncodedKeys` tags objects whose keys
  hold a space (`%20` and `+`), a comma, an é and a plus.
- **An image pull says why it pulled.** `pullImage` writes a `[sim-pull]` line
  when the held-image check fails, with the inspect error or the held and
  wanted platforms, and one per throttled retry with the attempt, the error
  and the backoff, so a start that took the retry's 30 s says so in the log.
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
- **An audited call writes its Cloud Audit Logs entry.** One middleware around
  the whole route table and one gRPC unary interceptor audit the calls of
  Cloud Storage's JSON API and of the RPC-defined Cloud Run Admin v2, Pub/Sub,
  Secret Manager, Artifact Registry and Cloud Functions v2 APIs. Cloud Storage
  keeps its own method names (`storage.buckets.create`,
  `storage.setIamPermissions`, `storage.objects.create`) in a route table; the
  RPC-defined APIs resolve a REST call to its RPC through the
  `google.api.http` bindings of their registered descriptors, so the entry
  names the RPC's full name and the API's `google.api.default_host` and carries
  the request and response messages in JSON with their `@type`. The caller is
  the principal its access token names. A configuration change lands in the
  project's `cloudaudit.googleapis.com%2Factivity` log; a configuration read or
  a data read or write lands in `%2Fdata_access` only when the project's IAM
  policy `auditConfigs` enable that log type for the service, and
  `setIamPolicy` changes `auditConfigs` only when its `updateMask` names them.
  `LogEntry` carries `protoPayload` and `receiveTimestamp` through REST and
  gRPC. Eventarc `google.cloud.audit.log.v1.written` triggers receive the
  entries of their project and location (a global trigger every location's)
  whose `serviceName`, `methodName` and `resourceName` filters, the last
  optionally a `match-path-pattern`, match, as CloudEvents whose data is the
  `LogEntryData`.
  Compute Engine and Cloud DNS are defined by Discovery documents rather than
  RPCs, so their calls resolve through the embedded Discovery document's
  method paths: Compute Engine records `v1.` plus the method ID
  (`v1.compute.networks.insert`), the resource from the operation's
  `targetLink`, and the `compute#operation` as the response; Cloud DNS records
  the method ID, the resource relative to the project (`managedZones/{zone}`)
  and the request as its `cloud.dns.api` message. Cloud Resource Manager v1
  records its project methods under their own names (`SetIamPolicy`,
  `CreateProject`) and a `SetIamPolicy` entry carries the
  `google.iam.v1.logging.AuditData` policy delta, read from the policy before
  and after the call. An API records the methodName its service writes: the
  RPC's full name by default, the simple name for Cloud KMS (`CreateKeyRing`,
  `Decrypt`), and `google.iam.admin.v1.CreateServiceAccount` for IAM, which
  also names a service account by its unique ID. The permission in
  `authorizationInfo` comes from an explicit table where a method checks one
  its name does not spell (`cloudkms.cryptoKeyVersions.useToEncrypt`), and
  otherwise from IAM's `service.collection.verb` convention: the collection
  the call addresses and the RPC's name without the resource types its proto
  package defines. A call that starts a long-running operation writes an
  entry marked `operation.first` and, when the operation ends, one marked
  `operation.last`; an operation already done when the call answers gets one
  entry marked both, as Cloud Audit Logs documents. When two APIs publish the
  same path (Eventarc's and Cloud Build's regional triggers) the call's host
  decides, and without one the simulator's own routing rule does. A log
  filter that crosses a repeated field compares each element, so
  `protoPayload.serviceData.policyDelta.bindingDeltas.action="ADD"` matches.
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
  `TimeGenerated` before the query runs. The engine also reads `dynamic`
  values — `parse_json`/`todynamic`, property and element access
  (`d.name`, `d["name"]`, `d[i]`), `array_length`, and `tostring` back to
  JSON text — and `bool` and `dynamic` columns.
- **A workspace reads only its own rows, and rows arrive only where something
  names the workspace.** Every Container Apps line, App Service trace and
  Logs Ingestion upload had landed under one `default` key that a query of
  any workspace id read through, and the upload chose its table by which
  fields a row populated. A workspace keeps its rows under its customer id,
  and a query names a workspace that exists (404 `WorkspaceNotFoundError`
  otherwise) and resolves its table against that workspace's tables. Rows
  arrive by three routes. A Container Apps environment whose
  `appLogsConfiguration` names a workspace has its containers' output written
  to that workspace's `ContainerAppConsoleLogs_CL` and the platform's events —
  the app's replica events, a job's execution start, completion, failure and
  stop — to `ContainerAppSystemLogs_CL`, the two classic custom log tables the
  link creates; the console holds nothing the platform said. A site connected
  to an Application Insights component by `APPLICATIONINSIGHTS_CONNECTION_STRING`
  or `APPINSIGHTS_INSTRUMENTATIONKEY` has its container output written to the
  component's `AppTraces`: in the workspace a workspace-based component names,
  or in a classic component's own store; the site's docker log, which
  `containerlogs` serves, keeps every line whatever the site is connected to.
  A Logs Ingestion upload names a data collection rule by its immutable id
  and a stream the rule declares; each data flow carrying the stream runs its
  `transformKql` through the query engine and writes the result to its
  `outputStream` table in each Log Analytics destination. The rules and
  endpoints are served as `Microsoft.Insights/dataCollectionRules` and
  `dataCollectionEndpoints`, and a rule is refused (`InvalidPayload`) when a
  destination workspace, its output table or its endpoint does not exist,
  the output table is still classic, or the transform does not bind or writes
  a column the table lacks. A workspace's tables are served as
  `Microsoft.OperationalInsights/workspaces/tables`: the Azure tables every
  workspace holds, custom log tables a customer creates (`_CL`, with a
  `TimeGenerated` datetime column), retention and plan, and `migrate` of a
  classic table onto data collection rules. A resource-centric query reads a
  workspace, a component's telemetry, or the rows of every workspace whose
  `_ResourceId` lies at or under the queried scope; an Application Insights
  app-id query reads its component's rows. A component's `AppId` is the
  unique id the data plane addresses it by, and `ApplicationId` mirrors its
  name, as the specification says; the simulator had put the id in
  `ApplicationId`.
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
- **Every Elastic Load Balancing resource keeps its tags.** CreateListener
  and CreateRule store the tags they carry, and AddTags, RemoveTags,
  DescribeTags and the IAM gate's `aws:ResourceTag/<k>` read and write the tag
  set of a listener, rule or trust store as they do a load balancer's or a
  target group's; the simulator had dropped listener and rule tags and served
  no trust store's. A tagging request naming a resource that does not exist
  answers that type's not-found error (`ListenerNotFound`, `RuleNotFound`, …)
  and changes nothing. Load balancers, target groups, listeners, rules and
  trust stores are identified by 16 lowercase hexadecimal characters, as
  their ARNs are in AWS.
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
routes to it — one of the host's interfaces is on the container's network, as a
Linux engine's bridge is — rather than through the engine's published loopback port, whose userland proxy
accepts a connection before the workload listens, so a bare TCP accept there
proved nothing and reset the first request; a host that routes no container
address reaches the workload only through that port. The route is found from
the host's interfaces, not by connecting: a connection Cloud Run never makes
had been the first one a workload accepted, so a workload could not act on its
startup probe's. A probe that fails its threshold, or a container that
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

A Linux function app on a built-in stack runs the Azure Functions host.
`linuxFxVersion` `Node|22` on a `functionapp` site selects
`mcr.microsoft.com/azure-functions/node:4.1054.250-4-node22-appservice`, the
build the platform's floating `4-node22-appservice` tag named, with the same
`/home` layout as a web stack: the host loads the function app from
`site/wwwroot` and answers 401 for a function-level function without a key and
404 for a route no function declares. Every site's container gets the
platform's own environment beside its app settings — `WEBSITE_SITE_NAME`,
`WEBSITE_HOSTNAME`, `WEBSITE_RESOURCE_GROUP`, a fresh `WEBSITE_INSTANCE_ID` per
start, and a per-site `WEBSITE_AUTH_ENCRYPTION_KEY` kept with the site's host
keys — and the host needs them: without `WEBSITE_INSTANCE_ID` it does not run
in App Service mode and keeps its keys under its own install directory, and
without an encryption key it cannot write a key at all. With
`AzureWebJobsSecretStorageType=files` the host reads its keys from
`/home/data/Functions/secrets`, and the ARM key operations read that store
before they answer and write it after they change a key, so a key set or
deleted through ARM opens or closes the running function and a key the host
generated lists through ARM. Without that setting the host picks its blob
store, as `DefaultSecretManagerProvider` does when no SAS URI or other store is
named: `azure-webjobs-secrets/<site>/host.json` and one
`<function>.json` per function in the account the `AzureWebJobsStorage`
connection string names, with the same document shape and the same
encryption. The ARM key operations read and write those blobs through the
simulator's Blob service, refuse a connection string whose account does not
exist or whose key or SAS does not open it, and generate and store the keys
when the host has not stored any yet, as the host does the first time it loads
them; a deleted function's blob stays, since the host's blob repository never
deletes one. The store's values are encrypted the way the host
encrypts them, measured against the host itself: an ASP.NET Core Data
Protection payload under the purpose `function-secrets` and the empty key id,
AES-256-CBC with HMAC-SHA256 keyed through SP800-108 over HMAC-SHA512, with the
key the host resolves first — the `AzureWebEncryptionKey` app setting, then the
`MACHINEKEY_DecryptionKey` `az functionapp create` sets, then
`WEBSITE_AUTH_ENCRYPTION_KEY`. Keys are the identifiable secrets the host
generates (33 random bytes, the `AzFu` signature and a Marvin checksum under
the key family's seed) instead of digests of the resource ID, and
`functions/admin/token` is signed with `WEBSITE_AUTH_ENCRYPTION_KEY`, the key
the host validates platform tokens with, rather than the master key, which the
host rejects as a token key. The ARM functions list of such an app includes
each `<function>/function.json` its content holds, as Kudu lists them, with
the HTTP trigger's invoke URL. `functionAppStacks` lists the stack from the
same table, so `az functionapp list-runtimes` and `az functionapp create
--runtime node --runtime-version 22` see what the site path runs, and a site
PUT naming its plan by bare name, as `az functionapp create` does, records the
plan's resource ID, so the site reports its plan's SKU.

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
deployment. The Azure Resource Manager MSDeploy and OneDeploy operations
settle their deploymentStatus the same way: they had reported
`RuntimeSuccessful` the moment the package unpacked, so a function app with no
image reported a runtime that never started; they now hand the restart to the
same tracker (and a package that fails to land reports `BuildFailed`), and the
tests asserting `RuntimeSuccessful` deploy to sites that run — a built-in-stack
web app, or a container site whose command answers HTTP. Once the build phase
ends the status stops naming the operation's Azure-AsyncOperation URL, so a
poller follows the status itself to the runtime outcome rather than the
finished build. `WEBSITE_RUN_FROM_PACKAGE=1` makes each zip deployment the whole of
wwwroot, mounted read-only. The CLI suite reaches the `.localhost` SCM host
through an HTTPS proxy the test runs, the CLI analogue of the SDK suite's
dialer: the commands and their requests are the ones a real deployment sends.
The SCM site also serves Kudu's WebJobs API over the webjob records the
Microsoft.Web webjob resources read, where it had answered the `url` and
`history_url` those resources advertise with 501: `/api/webjobs`,
`/api/triggeredwebjobs` and `/api/continuouswebjobs` list and get jobs in
Kudu's own spelling (`run_command`, `type`, `latest_run`, `settings`); a PUT
places a job — a zip, or one run file named by `Content-Disposition` — under
`App_Data/jobs` and rediscovers the site's jobs, a DELETE removes its files
and with them the job; a triggered run answers 202 with its history entry as
`Location`, passes `arguments` to the run file's command line and as
`WEBJOBS_COMMAND_ARGUMENTS`, records `External - <user agent>` as its trigger,
and answers 409 while the job already runs or `WEBJOBS_STOPPED` is set;
`settings` reads and writes the job's `settings.job`; start and stop drive the
continuous job's container. The advertised URLs carry the scheme the request
came in on, so they resolve. Because the SCM site sits behind a handler wrapper, every store read a Kudu
deployment reaches answers from a generation index — a site's Kudu
deployments, webjobs and host-name bindings, and a webjob's runs — instead of
a full-store scan.

The rest of the SCM site followed. The VFS (`/api/vfs`, and `/vfs`, the
spelling Kudu's job log URLs use) reads, writes and deletes under the site's
`/home` with Kudu's semantics: a trailing slash addresses a directory, which
lists as JSON entries with `href` and `path`; a directory or file addressed in
the other spelling redirects with 307; a file carries an ETag derived from its
write time, and overwriting or deleting one takes `If-Match`; a directory
deletes only empty or with `recursive=true`. A change under `site/wwwroot`
rediscovers the site's webjobs, so a job written through the VFS is
discovered as a deployed one would be. A run-from-package app's wwwroot
refuses writes. `/api/command` runs `sh -c` in
the site's image with `/home` mounted, the app settings in its environment and
the requested directory as its working directory, answers `Output`, `Error`
and `ExitCode`, and kills a command that writes nothing for
`SCM_COMMAND_IDLE_TIMEOUT` seconds. `/api/settings` merges Kudu's defaults, the
settings written through it and the app settings, in that order of
precedence; a slot swap carries the written settings with the content.
`/logstream` writes a welcome line and then each line the site's containers
log, a quiet-minute line while nothing is logged, and ends after
`SCM_LOGSTREAM_TIMEOUT`. Webjob runs write Kudu's log files under
`data/jobs`: a triggered run's `output_log.txt` with the run's status changes
and stdout and stderr lines, and its `error_log.txt` once it writes to stderr;
a continuous job's `job_log.txt`; and a scheduled job's `job_scheduler.log`.
The Kudu and Azure Resource Manager job records name those files by their VFS
URLs (`output_url`, `error_url`, `log_url`, `scheduler_logs_url`) once they
exist. A triggered job whose `settings.job` names a six-field NCRONTAB
`schedule` runs at each occurrence, read in `WEBSITE_TIME_ZONE`, with trigger
`Schedule - <expression>`, skipping an occurrence while the job runs or while
`WEBJOBS_STOPPED` is set; a schedule that does not parse is the job's
`error`. Like Kudu's, the scheduler starts from the next occurrence after the
simulator comes up and runs none it missed. The command API resolves the
site's registry credential, which put the Azure Container Registry
login-server lookup behind the SCM site's handler wrapper, so that lookup
answers from an index rather than a scan of every registry.

Deployment slots had held their own content, settings, publishing
credentials and SCM site while running nothing. Each slot came to run its
own container exactly as its app does, keyed by the slot: the front end routes a
slot's hostname to it, its `/home`, containers and volumes go by
`<app>__<slot>` (a slot's name holds a slash, which neither a directory nor a
container name may), its container sees `WEBSITE_SITE_NAME` as the app's name
and `WEBSITE_SLOT_NAME` as its own (`Production` for the app), a settings,
configuration or storage-mount change restarts it, and a deployment's
deploymentStatus settles on its start. Deleting a slot, or its app, stops its
container and removes its storage. The swap, which had answered 200 and
exchanged nothing, became a long-running operation that exchanges what the two
slots run: their deployed content and everything kept in their file systems
(deployment history, webjobs and their runs, Kudu settings, `/home`), their
general settings and sitecontainers, and their app settings, connection
strings and storage mounts except those the app's `slotconfignames` sticks to
a slot and the `_EXTENSION_VERSION` settings, which stick unless every slot
sets `WEBSITE_OVERRIDE_PRESERVE_DEFAULT_STICKY_SLOT_SETTINGS` to 0 or false.
Each slot keeps its hostnames, publishing endpoints, Always On, scale, IP
restriction, CORS, diagnostic-log, VNet and TLS settings. The destination
starts on its new content before the swap succeeds; one that does not start
swaps back and fails the operation, leaving both slots as they were. Both
slots then report the swap in `slotSwapStatus`, which
terraform-provider-azurerm's `azurerm_web_app_active_slot` waits on. The CLI
and Terraform suites pull an App Service platform image only when the run
selects a test that runs it.

The deployed content had been a store of its own that the simulator projected
onto a site's `/home` at each start and before each Kudu request, which
deleted every file the running app had written to `site/wwwroot`. App Service
keeps `/home` as one persistent share the app, Kudu and every instance mount,
so the share became the content: a deployment writes into it with KuduSync's
manifest semantics and leaves the files no deployment wrote, a restore
replaces it and restarts the app, a swap exchanges the two slots' shares, and
backups, snapshots, webjob discovery and the Functions host's function list
read it. A new app or slot starts on an empty share, and an app's deletion
retains its content for `restoreFromDeletedApp` before the share goes. A
custom container mounts the share when `WEBSITES_ENABLE_APP_SERVICE_STORAGE` is
true, as App Service mounts it.

Connection strings had been stored, listed and swapped without reaching the
workload. Each one now enters the container's environment — and a webjob's and
the Kudu command's — under the prefix App Service gives its type
(`MYSQLCONNSTR_`, `SQLCONNSTR_`, `SQLAZURECONNSTR_`, `CUSTOMCONNSTR_`,
`NOTIFICATIONHUBCONNSTR_`, `SERVICEBUSCONNSTR_`, `EVENTHUBCONNSTR_`,
`APIHUBCONNSTR_`, `DOCDBCONNSTR_`, `REDISCACHECONNSTR_`, `POSTGRESQLCONNSTR_`);
a write names a `ConnectionStringType` or is refused, a site PUT's
`siteConfig.connectionStrings` sets them, and a change restarts the app or
slot.

`WebApps_Stop` had only recorded `state: Stopped`. A stopped app or slot now
runs nothing: the stop tears its container down and stops its webjobs, nothing
starts it again — a request, Always On, a swap's warm-up, a deployment, a
webjob schedule — and its hostname answers App Service's 403 stopped-site page.
`WebApps_Start`, and an App Service Environment's resume, start it again, and
an update leaves a stopped app stopped, since `state` is read-only.

Swap with preview and the slot differences read had answered 200 and an empty
list. `applySlotConfig` now gives the slot it addresses the target slot's
sticky app settings and connection strings, keeps its own swappable ones,
restarts it and waits for it to answer, reverting it when it does not;
`resetSlotConfig` on either slot of the pair restores the source's own
settings; the swap between the same two slots completes the preview from the
source's own settings, and a swap of any other pair is refused while a preview
is pending. `slotsdiffs` lists each app setting, connection string and
general setting the two slots hold differently, with both values: a swappable
one as `SettingsWillBeSwapped` at level `Information`, a slot setting as
`SettingsWillNotBeSwapped` at level `Warning`.

An App Service app's Key Vault references had reached its workload as the
reference text, and the `configreferences` reads had resolved them without an
identity. An app or slot now carries a managed identity: `identity` on a site
PUT or PATCH enables a system-assigned identity, whose principal lasts as long
as the identity and is registered in the directory so role assignments and
Graph reads find it, and attaches user-assigned identities by resource ID,
each reported with its own principal and client IDs; `keyVaultReferenceIdentity`
(SystemAssigned unless set) names the one the app reaches Key Vault as. A
reference — `SecretUri=…` or `VaultName=…;SecretName=…[;SecretVersion=…]`, in
an app setting or a connection string — resolves when that identity is attached
and the vault grants it the secret: through an Azure RBAC role assignment
carrying `Microsoft.KeyVault/vaults/secrets/getSecret/action` at the secret, the
vault or a scope above it when the vault uses Azure RBAC (the Key Vault
Administrator, Reader, Secrets Officer and Secrets User built-in roles carry
their published permissions), and otherwise through an access policy for that
object with the secret `get` permission; the secret, and a pinned version,
must exist and be enabled and current. The container, a webjob, the Kudu
command and Kudu's settings see the secret's value; an unresolved reference
reaches them as written, and the `configreferences` status says why —
`MSINotEnabled`, `VaultNotFound`, `AccessToKeyVaultDenied`, `SecretNotFound`,
`SecretVersionNotFound`, `InvalidSyntax` or `OtherReasons` — with its details
and the identity it tried as `identityType`.

Source control had stored a repository and branch, and `WebApps_SyncRepository`
had answered 200, without anything deployed. Configuring `sourcecontrols/web`
now fetches the branch's head over HTTP or HTTPS and deploys its tree, without
the repository's `.git`, into wwwroot with KuduSync's semantics, as does each
sync; the deployment is recorded under the commit's ID with its author, author
email and message, the previous one stops being active, and a fetch that fails
is recorded as a failed deployment of the fetch while the app keeps what it
ran. The site's `siteConfig.scmType` is `None` until source control sets it
(`ExternalGit` for a manually integrated repository, `GitHub` or `BitbucketGit`
for a continuously integrated one) and returns to `None` when it is removed,
which is what terraform-provider-azurerm's `azurerm_app_service_source_control`
reads; a sync of an app without source control is refused. Azure Resource
Manager paths fold `sourceControls` to the registered spelling, and a
deployment record reports `status` and `active` even when they are zero.

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

A deletion discards the resource's engine rather than stopping it
(`dbengine.Instance.Discard`, called by Amazon RDS, Amazon Aurora and Cloud SQL
deletions). An engine that has not yet accepted clients is killed at once: the
MySQL image's entrypoint runs as PID 1 and ignores SIGTERM while it initialises
the data directory, so a deletion that landed while the first automated backup
was starting the engine had waited out the five-second stop grace, and every
Aurora MySQL cluster test in the SDK suite had grown by about ten seconds. The
directory goes with the resource, so it needs no clean shutdown. A stop keeps
the grace, since its volume stays. Every stop also ends a start that is
waiting on the engine at once, rather than at the next two-second liveness
check. The deletion stops the engine first, settles the automated backups, and
then removes the volume.

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
a real Redis engine on the image the instance's `redisVersion` names (clusters
run 7.2): one container per node, each a redis-server on port 6379, on a Docker
network every Memorystore engine of the simulator shares, with the node
directories on one volume per resource. Volume, network and alias names hash
the resource name with the simulator's workload scope (its state directory, or
its run when it does not persist), so two simulators serving a resource of the
same name never share them. The create starts the engine and settles only once
every node answers and, for a cluster, the slots are assigned and every replica
has synchronised, so a resource reported ready is serving; the delete stops it
and removes its volume. The `host`, `readEndpoint` and discovery endpoint the
API reports are per-resource loopback listeners relaying to the nodes; instance
replicas follow their primary by its network alias, and cluster nodes meet over
the network and announce their own loopback endpoint as their hostname
(`cluster-preferred-endpoint-type hostname`), so a cluster client follows
redirections to addresses it can reach. AUTH is the engine's `requirepass`,
whose value `getAuthString` returns, and an `authEnabled` update changes it
live; a failover promotes a replica and moves the primary endpoint to it, and a
Basic Tier failover is refused with FAILED_PRECONDITION, since there is no
replica to promote. A failover in `LIMITED_DATA_LOSS` mode, the default, first
reads the primary's `INFO replication` and fails the operation with
FAILED_PRECONDITION when the replica's acknowledged offset trails
`master_repl_offset` by 30 MB or more, or the replica is not replicating;
`FORCE_DATA_LOSS` promotes it regardless. The Go REST client sends
`dataProtectionMode` as the enum's number, which the simulator accepts beside
its name. A `replicaCount` update starts or stops replica containers
while the primary serves, and a `readReplicasMode` update binds or closes the
read endpoint. A cluster `shardCount` update meets new primaries and moves
an equal share of the slots onto them, or moves the removed shards' slots onto
the rest and deletes their nodes with `del-node`; a `replicaCount` update adds
replicas with `CLUSTER REPLICATE` or deletes them. The control plane moves slots
with Redis Cluster's own resharding commands — `CLUSTER SETSLOT IMPORTING` and
`MIGRATING`, `MIGRATE` for each slot's keys, and `CLUSTER SETSLOT NODE` on every
primary — pipelined 512 slots at a time. `redis-cli --cluster rebalance` once
moved them, one slot per round trip, and spent 27 seconds moving 8,192 empty
slots onto one new shard. A cluster created from `gcsSource` or `managedBackupSource`
loads each RDB file into a standalone redis-server inside one node's container
and moves the keys onto their shards with `redis-cli --cluster import`.

Settling a cluster waits on the engine, never on a timer of the simulator's.
Before it reads the topology, every node sends `CLUSTER MEET` to every other
one, because a node learns a peer's role and announced hostname only from a
packet that peer sends, and gossip picks whom to ping at random, up to half the
node timeout apart; left to gossip, a replica-count update settled up to ten
seconds late. Engines start with `repl-diskless-sync-delay 0`, so a primary
starts a replica's full synchronisation when the replica asks rather than five
seconds later, and a delete stops a resource's nodes in parallel. What a settle still waits on is Redis's own: a new primary stays
`cluster_state:fail` for the two seconds Redis holds a master before letting it
accept writes.

`transitEncryptionMode` `SERVER_AUTHENTICATION` serves TLS from the relay, on
port 6378 for an instance and 6379 for a cluster, with no plaintext listener.
Each resource has a server CA of its own (a cluster with
`SERVER_CA_MODE_GOOGLE_MANAGED_SHARED_CA` uses the region's, which
`sharedRegionalCertificateAuthority` reports), kept in the simulator's state;
an instance reports it in `serverCaCerts` and a cluster through
`getCertificateAuthority`, and every endpoint presents a certificate that CA
signs for the address the client dialed. A cluster with `AUTH_MODE_IAM_AUTH`
runs its engine behind a credential only the simulator holds; the relay reads
each AUTH, or HELLO with AUTH, and exchanges a password that is an access token
the simulator issued to a principal holding `redis.clusters.connect` (granted by
`roles/redis.dbConnectionUser`) for that credential, and forwards any other
password unchanged, so the engine refuses it and replies keep their order. A
cluster with `AUTH_MODE_TOKEN_AUTH` runs each token-auth user as an engine ACL
user whose passwords are its active auth tokens, rewritten on every node when a
user or token is added or deleted. A cluster's `aclPolicy` runs each of the
policy's rules as `ACL SETUSER <username> reset <rule>` on every node, a
selector in parentheses being one argument, and removes every other engine
user but `default`; it applies at create, when an update attaches, swaps or
detaches the policy, when an `aclPolicies.patch` revises the rules of an
attached policy, and on nodes a reshape adds. The cluster reports the
revision it runs in `aclPolicyInfo`, the policy its clusters in
`clusterAclPolicyAttachments`, and a revision the clusters running it in
`attachedClusters`; a policy refuses deletion while a cluster runs it, and a
rule for the `default` user, a duplicate username, or an unbalanced selector
is refused with INVALID_ARGUMENT. On a cluster that authenticates with IAM, a
principal whose email is a rule's username connects as that policy user: the
relay authenticates it as the user with the engine credential, which the
simulator adds to the user's passwords ahead of the rule, and any other
principal connects as `default`. A cluster with deletion protection refuses
its delete.

`persistenceConfig` is honoured: RDB snapshots are BGSAVEs the control plane
takes on the reported schedule (`rdbSnapshotStartTime` plus whole
`rdbSnapshotPeriod`s, which `rdbNextSnapshotTime` reports), not the engine's
save points. A schedule given no start time starts at the current time, as the
API reference says, and owes its first snapshot then: the control plane takes
it at once, or as soon as the engine is up. A cluster's AOF mode runs `appendonly yes` with the configured
`appendfsync`; an update applies live. A node keeps the files its persistence
writes across a restart and loads its dataset from them, and a cluster node
always keeps its `nodes.conf` identity. An export has the primary write its RDB
with SAVE and stores that file in Cloud Storage; an import and an upgrade stage
the snapshot as the primary's `dump.rdb` and restart the engine on it; a
cluster backup keeps every shard's RDB, which `backups:export` writes to the
bucket. A simulator started API-only (`SIM_RUNTIME=process`) runs no engine
even when another server in the same process holds a container client, and its
instances report no host.

Every named volume a workload container mounts is created labelled with the
simulator's run before the container starts, and the detached reaper and the
shutdown cleanup of a simulator that does not persist remove the run's volumes
with its containers and networks, so an engine volume of a resource that was
never deleted does not outlive the simulator. `EnsureDockerNetwork` returns the
network a concurrent caller created first instead of failing.

Every container `sim` removes goes with `RemoveVolumes`, so the anonymous
volumes its image declares (the MySQL image's `VOLUME /var/lib/mysql`, Redis's
`VOLUME /data`) go with it, while the named volumes it mounted stay: the engine
removes only anonymous volumes on that flag. Without it, each Amazon RDS SDK
run had left about four dangling engine volumes behind, enough to fill a
small disk over repeated runs.

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
A network interface's namespace and veth names derive from its ID, so a
simulator killed before its cleanup ran leaves them for the next process to
collide with: attaching an interface deletes a namespace of its name, and the
veth it held, and retries once, as creating a network's namespace does, and
the fabric serializes attaches of one interface so the reclaim never destroys
a live namespace. The Azure simulator closes its fabric when SIGTERM stops it,
and the Azure suites stop their simulators with SIGTERM.
An Azure network interface realizes every IP configuration: the primary one
(the one marked primary, else the first) attaches the interface, and each
secondary leases its own address of the same subnet onto it — `ip addr add` in
the interface's namespace, removed and released through its cleanup stack, or,
while a virtual machine carries the interface as a tap, a lease the tap holds.
A rewrite keeps the address each kept configuration held and releases the
dropped ones first; configurations in two subnets are refused with
`IpConfigurationsOnSameNicCannotUseDifferentSubnets`, and an address another
interface holds with `PrivateIPAddressInUse`. The simulator does not configure
the secondary addresses inside a guest, because Azure's DHCP hands a guest
only its primary; the Instance Metadata Service lists them all, primary first.
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

An Aurora cluster restores to any time in its restorable window, kept as base
backups and the engine's own log. Each base backup is an automated DB cluster
snapshot named `rds:<cluster>-<yyyy-mm-dd-hh-mm>`: `Ready` takes the first when
the engine first accepts clients, before the endpoint relays any client to it,
and a timer takes another at the start of every `PreferredBackupWindow`
(honoured on CreateDBCluster and ModifyDBCluster, which refuse a window not
spelled hh24:mi-hh24:mi) while the cluster is available, starting its engine
when no client has. Aurora backs a cluster up whether or not a client
connects, so `rdsTakeFirstClusterBackup` starts the engine in the background,
the way `rdsTakeFirstInstanceBackup` does for a DB instance, whenever an
available cluster keeps automated backups and holds none: after
CreateDBCluster, a restore from a snapshot, to a time or from Amazon S3,
StartDBCluster, a ModifyDBCluster that changes the backup settings, and a
simulator restart. The start holds the backups' start lock, so a
DeleteDBCluster closes the engine and waits for the start to give up, and a
manual DB cluster snapshot waits for it rather than capture a data directory
the engine is still initialising; a ModifyDBCluster that rotates the master
password while the engine starts waits for the engine to accept clients. The
SDK suite creates a cluster no client connects to, waits for its automated
snapshot, finds `EarliestRestorableTime`, restores it to the latest restorable
time and finds the restored cluster's own first automated snapshot; the CLI
suite lists a new cluster's automated snapshot without adding a writer; the
Terraform suite waits for the first automated snapshot of the clusters
`aws_rds_cluster` restored before any client connects. The package's own
tests start real engines once another test has given the process a container
runtime, so the ones that create Aurora clusters wait for that first backup and
remove the cluster volumes and snapshot volumes they made.
DescribeDBClusterSnapshots lists them under `SnapshotType` `automated`, and
DeleteDBClusterSnapshot refuses one, as it does for every automated snapshot.
DescribeDBClusters reports `EarliestRestorableTime` as the later of the oldest
base backup and the start of the `BackupRetentionPeriod`, and the present as
`LatestRestorableTime`. Each backup run also expires what the period no longer
covers: automated snapshots older than the period go, a base backup's volume
stays until a newer one was taken by the start of the period (and while a
creating restore seeds from it), and the log before the oldest remaining base
backup goes — `PURGE BINARY LOGS TO` its binary log file for MySQL, whose engine
runs with `binlog_expire_logs_seconds=0` so its own 30-day expiry never purges
a file a restore needs, and for PostgreSQL every archived segment before the
REDO WAL file `pg_controldata` reports for that base backup. A capture freezes
the engine, so it holds what a crash would leave; for MySQL the simulator reads
the capture's newest binary log file and records the offset of the first
transaction it does not hold whole (crash recovery commits exactly the
transactions the binary log holds whole), where the replay onto that base
backup starts. Aurora PostgreSQL's engine runs with `archive_mode=on`
and archives every completed write-ahead log segment into the cluster volume;
Aurora MySQL's binary log is on by default. RestoreDBClusterToPointInTime with
`RestoreToTime` refuses a time outside the window with `InvalidRestoreFault`,
seeds the new cluster volume from the newest base backup taken by then, and
replays the source's log
read from its live cluster volume — every transaction that ended by the restore
time is already in it, and a record still being written ends after it. For
PostgreSQL a helper copies the archive and `pg_wal` into the new volume's
archive, `pg_waldump` lists the commit and abort records, and the engine's
first start runs archive recovery with `recovery_target_time`, or to the end of
the log when no transaction ended after the time (archive recovery refuses a
target the log never reaches), then promotes. It recovers with `hot_standby`
off, so it refuses clients with 57P03 until it has promoted and the restored
cluster turns available only once it accepts writes; with hot standby on, the
readiness probe had admitted clients to the read-only replay. For MySQL, whose image ships no
`mysqlbinlog`, the simulator reads the GTID events' microsecond immediate
commit timestamps itself, cuts the binary log before the first transaction
committed after the time, and a server started on the new volume applies it as
its relay log from the base backup's recorded offset with the replication SQL
thread (`START REPLICA SQL_THREAD UNTIL`),
writing no binary log, driven by a user only its init file creates, which then
sets the cluster's master password and is dropped. RestoreDBClusterFromSnapshot
also takes a DB snapshot ARN: an RDS for PostgreSQL or RDS for MySQL instance's
data directory becomes the cluster volume of an Aurora PostgreSQL or Aurora
MySQL cluster of the same image, under the instance's master credential and
database, which DB snapshots now carry. The SDK, CLI and Terraform suites
restore to a time the engine's clock has passed by a millisecond and prove the
restored cluster holds the row committed before it and not the one after, and
that a migrated cluster serves the instance's rows; the SDK suite also moves
a cluster's backup window to the next minute, keeps committing rows while the
window's automated snapshot is taken, and restores to a time after it with
every committed row present exactly once.

RestoreDBClusterFromS3 creates an Aurora MySQL cluster from a Percona
XtraBackup of a MySQL 8.0 server. The request needs the bucket to exist and
the `S3IngestionRoleArn` role to trust `rds.amazonaws.com` and allow
`s3:ListBucket` on the bucket and `s3:GetObject` on every object under
`S3Prefix`, or it is refused with `InvalidS3BucketFault`; a 5.7 source is
refused with `InvalidParameterCombination`. The cluster is `creating` while a
background import copies the objects to a staging directory as that role, and
a `docker.io/percona/percona-xtrabackup:8.0` helper unpacks them (xbstream,
tar or gzip-compressed tar archives, whole or split into numbered parts, or the
backup directory's own files), runs `xtrabackup --prepare` and copies the
backup back into the cluster volume; a MySQL helper started on the volume with
no network listener then creates or resets the master user with every
privilege under the request's password, gives root the same password, and
creates the cluster's database. The cluster lands `available`, or
`migration-failed` when the import fails. The SDK, CLI and Terraform
(`s3_import`) suites take a real backup of a MySQL 8.0 container and read its
rows through the restored cluster.

An RDS for PostgreSQL or RDS for MySQL DB instance keeps automated backups the
way an Aurora cluster does, through one machinery (`rds_backups.go`) that an
Aurora cluster and a DB instance each drive as the owner of their backups. The
instance takes `BackupRetentionPeriod` (0 to 35, default 1) and
`PreferredBackupWindow` from CreateDBInstance, ModifyDBInstance and the
restores, and reports them with `LatestRestorableTime` once its first base
backup exists. Its engine archives PostgreSQL's write-ahead log or keeps
MySQL's binary log in the instance volume; it takes an automated DB snapshot
named `rds:<instance>-<yyyy-mm-dd-hh-mm>` at the start of each backup window,
and expires the snapshots and log the retention period no longer covers. It
takes its first one as Amazon RDS does, whether or not a client ever connects:
when CreateDBInstance, a restore, StartDBInstance or a simulator restart leaves
an available instance with a backup retention period and no base backup, or
ModifyDBInstance turns its retention on, `rdsTakeFirstInstanceBackup` starts
its engine in the background, and the engine's start takes the backup before
the endpoint relays a client. The window's run starts an engine no client has
started rather than skipping it. A DeleteDBInstance, StopDBInstance or
RebootDBInstance ends such a start by stopping the engine and waits for it to
give up, so no engine outlives the data plane on a volume its instance let go
of; a CreateDBSnapshot waits for the start to finish, so its capture never
copies a data directory the engine is still initialising, and a
ModifyDBInstance password change waits for it too, so it rotates the password
in an engine that accepts clients.
StartDBInstanceAutomatedBackupsReplication on a source still taking its first
backup leaves the copy to the source's recording of it. The SDK suite creates
an instance no client connects to, finds its automated backup `active` and its
automated snapshot available, turns retention off and on again and finds a new
one; the CLI suite lists the backup `active` and the snapshot available; the
Terraform suite reads the instance's first automated snapshot through the
`aws_db_snapshot` data source.

A DB instance reports the statuses Amazon RDS reports while it brings the
instance up. CreateDBInstance and RestoreDBInstanceFromDBSnapshot answer
`creating`, StartDBInstance answers `starting`, and a restore to a time or from
Amazon S3 stays `creating` once its volume is seeded; `rdsFinishInstanceBringUp`
then starts the engine of an instance that keeps automated backups and holds
none, the instance turns `backing-up` when the capture of its first automated
snapshot begins (`recordAutomatedSnapshot`), and lands `available` only once
that snapshot is taken — or `failed` when the engine does not start or the
capture fails. An instance with no backup to take, no data plane or a
retention period of 0 lands `available` at once. A simulator restart resumes
the bring-up of an instance it finds in any of those statuses. Until then
ModifyDBInstance, CreateDBSnapshot, CreateDBInstanceReadReplica (of it as the
source), PromoteReadReplica and SwitchoverReadReplica answer
`InvalidDBInstanceState` ("Instance … is not in available state."), as do
RebootDBInstance and StopDBInstance, which run only from `available`. The SDK
suite waits with `DBInstanceAvailableWaiter`, the CLI suite with `aws rds wait
db-instance-available`, and terraform-provider-aws's create waiter already
treats `creating`, `backing-up` and `starting` as pending. The CLI's waiter
polls every 30 seconds, so CLI tests whose instance takes no automated backup
create it with `--backup-retention-period 0`, which leaves it available by the
waiter's first poll; a read replica's source keeps its backups, as Amazon RDS
requires. DescribeDBSnapshots filters on `SnapshotType` and
`DbiResourceId`, and DeleteDBSnapshot refuses an automated snapshot with
`InvalidDBSnapshotState`. An automated snapshot's volume name carries a dot
where its identifier carries the colon a volume name cannot hold.

RestoreDBInstanceToPointInTime records the new instance `creating` and seeds
its volume in the background: for `UseLatestRestorableTime` from the source
instance's volume, for a `RestoreTime` from the newest base backup taken by
then, replaying the source's log onto it — PostgreSQL's archive recovery when
the engine first starts, MySQL's replication applier before the instance
becomes available. A time outside the window is `InvalidRestoreFault`, a
source without backup retention `PointInTimeRestoreNotEnabled`, and an Aurora
member is sent to RestoreDBClusterToPointInTime. RestoreDBInstanceFromS3
imports a Percona XtraBackup of MySQL 8.0 into an RDS for MySQL instance's
volume through the same import RestoreDBClusterFromS3 runs, refusing a bucket
the ingestion role cannot read with `InvalidS3BucketFault`. A restore that
fails lands the instance `incompatible-restore`, an import `failed`; a seed a
previous process left part-way starts again. DeleteDBInstance of a seeding
instance with `SkipFinalSnapshot` marks it `deleting` and tears it down once
the seed ends, and refuses a final snapshot with `InvalidDBInstanceState`. An
Aurora DB instance reports its cluster's backup settings. The SDK and CLI
suites restore an instance to a millisecond between two commits and to the
latest restorable time, and import a real XtraBackup into an instance; the
Terraform suites restore an `aws_db_instance` with `restore_to_point_in_time`
and import one with `s3_import`.

DescribeDBInstanceAutomatedBackups reads a live DB instance's automated backup
from the instance itself (`rds_automated_backups.go`): `creating` until its
first automated snapshot, then `active` with the restore window the instance's
base backups and retention period give, for an instance outside a cluster with
a backup retention period on an engine that keeps its log. DeleteDBInstance
and DeleteDBCluster honour `DeleteAutomatedBackups=false`: once the engine has
stopped, the deletion copies the volume into a `sockerless-rds-auto-backup_<resource-id>`
volume and keeps a `retained` row with the base backups, the automated
snapshots and the properties a restore reads, its window ending at the
deletion. DescribeDBClusterAutomatedBackups lists the retained rows, the only
status its shape names. RestoreDBInstanceToPointInTime restores a retained
backup through `SourceDbiResourceId` or `SourceDBInstanceAutomatedBackupsArn`
and RestoreDBClusterToPointInTime through `SourceDbClusterResourceId`, seeding
from its base backups and replaying the kept log. DeleteDBInstanceAutomatedBackup
and DeleteDBClusterAutomatedBackup delete only a retained backup, refusing a
live resource's with `InvalidDBInstanceAutomatedBackupState` or
`InvalidDBClusterAutomatedBackupStateFault`. A retained backup expires once its
retention period has passed since the deletion, on a timer the next process
re-arms, and one a creating restore still reads goes when that restore ends.
The listings filter on `status`, the identifier and the resource ID. The SDK
suite restores a deleted RDS for PostgreSQL instance and a deleted Aurora MySQL
cluster from their retained backups to a millisecond between two commits; the
CLI suite lists an instance's backup `creating`, then `active`, then
`retained`, and restores a deleted instance with `--source-dbi-resource-id`;
the Terraform suite destroys an instance with `delete_automated_backups =
false` and finds its retained backup.

A DB instance's automated backups replicate to another Region the way RDS
does it: StartDBInstanceAutomatedBackupsReplication is called in the
destination Region — the Region the request is signed for, refused when that
is the simulator's own — and records a replicated automated backup
(`rds_backup_replication.go`) whose ARN, `auto-backup:ab-<id>`, names the
destination while `Region` keeps the source's. The backup holds its own
copies: every automated snapshot the source takes lands in a
`sockerless-rds-replicated-snapshot_*` volume as the source's base backup is
recorded, and the source's log in a `sockerless-rds-replicated-backup_<id>`
volume, so the copy outlives the source's own automated backups. It is
`pending` until it holds an automated snapshot and `replicating` after, and
its own `BackupRetentionPeriod` expires its copies. Only requests signed for
its Region see it: DescribeDBInstanceAutomatedBackups lists it,
ListTagsForResource and the tag operations reach it, the source's
`DBInstanceAutomatedBackupsReplications` names it, and
RestoreDBInstanceToPointInTime restores it through
`SourceDBInstanceAutomatedBackupsArn`, first bringing its log up to the
source's. StopDBInstanceAutomatedBackupsReplication, or the source's
deletion, takes a last copy and leaves it `retained`; it then expires like a
retained backup, and DeleteDBInstanceAutomatedBackup deletes it, refusing one
still replicating. The simulator holds its DB instances in its own Region, so
an instance restored from a replicated backup is created there (BUG-3358). The
SDK suite replicates an RDS for PostgreSQL instance to us-west-2, restores
every committed row from the replicating backup, stops the replication,
deletes the source and restores from the retained copy; the CLI suite starts,
lists, stops and deletes a replication in us-west-2; the Terraform suite
creates `aws_db_instance_automated_backups_replication` through a provider
configured for us-west-2.

An RDS endpoint, a DB instance's and an Aurora cluster's alike, owns two
logins: the master user's, under the password the control plane records, and
IAM database authentication. Every other login reaches the engine, which
checks it against its own users (`rds_database_users.go`, where one
`rdsEndpointLogins` serves `rdsDataPlane` and `rdsAuroraDataPlane`). A
MySQL-family relay logs in to the engine with the client's own user and
password, so the engine's refusal reaches the client verbatim and a session
holds only its user's privileges. A PostgreSQL engine trusts the relay, so the
endpoint has the engine check the password: a `DO` block run as the master
user reads the role's `pg_authid` verifier and compares the MD5 digest, or
recomputes the SCRAM-SHA-256 StoredKey through PBKDF2-HMAC-SHA-256 with the
builtin `sha256`, the presented user and password reaching it as session
settings rather than spliced into the SQL. A role granted `rds_iam`, directly
or through another role, signs in only with an IAM authentication token whose
`DBUser` names it, and the session runs as that role; the check walks
`pg_auth_members`, since `pg_has_role` counts every role as granted to a
superuser, and a master user not granted `rds_iam` cannot sign in with a
token. On every engine start a PostgreSQL engine gets the `rds_iam` role, and
a MySQL-family master user gets the global privileges RDS grants it,
`CREATE USER` among them, while the image's remote `root` account goes; a
Percona XtraBackup import installs the master user with those privileges too,
not `ALL PRIVILEGES`. The SDK suite signs in a user the master user created on
each engine, on an Aurora cluster and on an RDS for PostgreSQL and RDS for
MySQL instance, and proves its wrong password, an unknown user and `root` are
refused, that its session holds only its own grants, and that a PostgreSQL
role granted `rds_iam` signs in with a token, and not with its password, as
itself; it also signs in a user the restored XtraBackup held. The CLI suite
signs in a role granted `rds_iam` with the token `aws rds
generate-db-auth-token` makes, and a password user on the instance a
point-in-time restore made.

On RDS for MySQL, RDS for MariaDB and Aurora MySQL an IAM authentication
token signs in the user its `DBUser` names, as that user, and only when the
user was created `IDENTIFIED WITH AWSAuthenticationPlugin AS 'RDS'`. The stock
MySQL 8.0 and MariaDB 11.4 images hold no plugin of that name and refuse the
statement with error 1524, and no built-in plugin, proxy account or rewritten
statement gives the same observable behaviour, so the simulator ships a real
server plugin named `AWSAuthenticationPlugin`
(`simulator-aws/rds_auth_plugin/`): a C source with no C library dependency,
built by `build.sh` with clang and ld.lld for each engine's plugin ABI on
amd64 and arm64 and embedded in the binary, since `go install` cannot compile
it. The engine loads plugins from `.sockerless-plugin` in its data volume
(`--plugin-dir`, and `--ignore-db-dirs` on MariaDB, which lists every
directory of its data directory as a database), so snapshots and restores
carry the plugin with the users identified with it; on every engine start the
endpoint writes the build for the container's architecture there, replacing
it by rename so a loaded copy stays mapped, and installs the plugin unless the
engine has it loaded. The plugin trusts the relay as the PostgreSQL engine's
`trust` method does, and the endpoint enforces what RDS's plugin does: it
validates the token and relays it as the user's password, which the engine
admits for a user identified with the plugin and refuses, with its own error
1045, for every other user, the master user included; a password login of a
user identified with the plugin never reaches the engine, since the endpoint
asks the engine for the account's plugin before it relays a non-master login.
The SDK suite signs in such a user with a token on an RDS for MySQL instance
and an Aurora MySQL cluster and proves its password, an empty password, and a
token for the master user or a password user are refused; the CLI suite does
the same on RDS for MySQL and RDS for MariaDB with `aws rds
generate-db-auth-token`, and the Terraform suite on an `aws_db_instance` with
`iam_database_authentication_enabled`. A unit test proves each embedded build
is a shared object for its architecture that imports nothing and exports its
engine's plugin declarations.
The `build-gates` CI job installs clang and lld and runs
`scripts/check-rds-auth-plugin-build.sh`, which rebuilds the shared objects in
a scratch copy with `build.sh` and fails when a committed binary differs from
the rebuild, so a source edit cannot ship without its binaries; the build is
reproducible because it links with no build ID and strips its symbols.

An Amazon RDS blue/green deployment of a DB instance is a real green
environment. CreateBlueGreenDeployment answers PROVISIONING and records a green
instance named `<blue>-green-<six letters>` as a read replica of the blue one,
with the request's target engine version, class, storage and DB parameter group
(DescribeDBInstances now reports a DB parameter group a request named, and
CreateDBInstance and ModifyDBInstance refuse one that does not exist; it also
reports AutoMinorVersionUpgrade, true unless a request turns it off, whose
absence had the provider modify every green instance to set it); the
green instance seeds its volume from a capture of the blue volume the way a
restore to the latest restorable time does, takes its first automated backup,
and its coming up settles the deployment AVAILABLE, or INVALID_CONFIGURATION
when it fails. Its endpoint serves sessions read-only, as RDS's green
environment does. SwitchoverBlueGreenDeployment answers
SWITCHOVER_IN_PROGRESS and, within SwitchoverTimeout, stops both engines,
copies the blue volume to the `-old1` identifier's, and moves the green record
onto the blue identifier, ARN and endpoint over the blue volume, with the blue
master-user credential and a fresh first automated backup; the deployment's
Source then names the `-old1` instance, which is what the Terraform provider
deletes after a `blue_green_update`, and its Target the instance in
production. A switchover that fails or runs out of time rolls back and reports
SWITCHOVER_FAILED, and one a previous process left in progress runs again.
DeleteBlueGreenDeployment refuses DeleteTarget after a switchover, deletes the
green instance with it before one, and otherwise leaves the green instance
standalone and writable. A Multi-AZ or Aurora DB cluster source answers
SourceClusterNotSupportedFault, a source without automated backups, a read
replica or a cluster member SourceDatabaseNotSupportedFault, so the IAM
condition-key tests that name a Multi-AZ DB cluster source expect that refusal
once the grant admits the request. The SDK suite switches an RDS for MySQL and
an RDS for PostgreSQL instance over — the PostgreSQL one from 16.14 to
16.15 — and reads every committed row through the original endpoint, the CLI
suite does the same on RDS for MySQL in its own `rds-blue-green` shard, and
the Terraform suite applies a parameter group change to an `aws_db_instance`
with `blue_green_update` in its own shard. A switchover moves the blue
instance's read replicas onto the `-old1` identifier, whose engine they keep
replicating.

Every RDS engine version runs the engine's own release of that version.
`rdsEngineVersions` lists each version the simulator offers with the image of
that release — PostgreSQL 17.11, 16.15 and 16.14, MySQL 8.0.46 and MariaDB
11.4.13 from the ECR Public Gallery's Docker official images, where each
patch release has a tag of its own — and with its DB parameter group family;
`rdsEngine` resolves an instance's engine and version to that image, so
`SELECT version()` answers the EngineVersion DescribeDBInstances reports. The
set is what the Docker images can run: it carries no MySQL 8.4 or MariaDB 10.11,
and DescribeDBEngineVersions lists exactly these versions, each with the
ValidUpgradeTarget entries the simulator performs. CreateDBInstance,
CreateBlueGreenDeployment's TargetEngineVersion, the snapshot restores and
ModifyDBSnapshot refuse any other version with InvalidParameterCombination
("Cannot find version 15.4 for postgres"), a major version alone resolves to
that major's newest version as Amazon RDS resolves it, and a downgrade is
refused. ModifyDBInstance with a newer version answers `upgrading` with the
version under PendingModifiedValues, stops the engine and starts the new
release on the same volume — PostgreSQL reads a minor release's data as it
is, and MySQL and MariaDB upgrade their data dictionary themselves — and lands
`available` once it accepts clients. A PostgreSQL major version upgrade needs
`pg_upgrade` with both releases' binaries, which no official image carries, so
it is refused and listed in `BUGS.md`; Aurora clusters keep running the one
community release their row names. Records an earlier simulator wrote — which
ran PostgreSQL 16, MySQL 8.0 and MariaDB 11.4 whatever version they recorded —
move to the newest offered version of that major when the simulator starts, so
the engine that starts on a volume reads it. The default PostgreSQL version is
16.15, the major the earlier images held. Every row's image is a literal in
the source, so `base-images-for.sh` warms each in CI.

An instance created, restored or provisioned as a read replica without a DB
parameter group is associated with its family's default group,
`default.<family>`, which Amazon RDS creates in each account the first time a
family is used and which DescribeDBParameterGroups and DescribeDBParameters
read; ModifyDBParameterGroup, ResetDBParameterGroup and DeleteDBParameterGroup
refuse a default group with InvalidParameterValue, and a group of another
family is refused with InvalidParameterCombination. DB cluster parameter
groups take their default name from the family too (`default.aurora-postgresql16`).

An RDS DB instance read replica is a real replica. CreateDBInstanceReadReplica
refuses a source without automated backups (InvalidDBInstanceState) or in a DB
cluster, answers `creating` and in the background readies the source's
running engine: the root of the replication chain creates the
`rdsrepladmin` replication user, whose password it records sealed, a
PostgreSQL source admits that user's replication connections in `pg_hba.conf`
and keeps a physical replication slot for the replica, and the source's
engine joins `sockerless-rds-replication-<source DbiResourceId>`, a container
network on which it answers as `source`. The replica's volume is a frozen
capture of the source's; a MySQL-family replica drops the source's server UUID
and replicates from the end of the last whole transaction in the source's
newest binary log file, read with the parser the restores to a time use, and
runs with a server ID of its own and `read_only` on; a PostgreSQL replica drops
the copied slots and starts as a hot standby with `primary_conninfo` and
`primary_slot_name`. Its engine joins the source's network when it starts,
and a MySQL-family replica runs `CHANGE REPLICATION SOURCE TO` (`CHANGE MASTER
TO` on MariaDB) at the captured position the first time; the engine keeps
that configuration across restarts. The replica lands `available` once its
engine reports its replication running, serves sessions read-only, and
reports the `read replication` status in StatusInfos and ReplicaLag in the
`AWS/RDS` namespace from its engine's own status (`Seconds_Behind_Source`,
`Seconds_Behind_Master`, or the replay delay of `pg_stat_wal_receiver`'s
stream), at once and then each minute, Amazon RDS's metric period;
`rdsEngineOutput` reads that status through the Docker Engine API's exec. The
replica takes the source's master-user password with its data, refuses one of
its own, and follows the source's when it changes. A source with read replicas
and a replica cannot be stopped. PromoteReadReplica answers `modifying`, ends
the replication (`RESET REPLICA ALL` and `read_only` off, or `pg_promote`),
drops the source's slot and reopens the engine writable with the request's
backup settings; deleting a source promotes its replicas and removes its
network, and deleting a replica drops its slot. SwitchoverReadReplica, an RDS
for Oracle Data Guard operation, refuses the engines the simulator runs.
Engines the simulator runs no data plane for keep record-only replicas. The
SDK suite replicates and promotes RDS for MySQL, PostgreSQL and MariaDB
replicas and promotes the replica of a deleted source, the CLI suite does the
same for RDS for MySQL and upgrades an RDS for PostgreSQL instance in its own
`rds-read-replica` shard, and the Terraform suite creates an `aws_db_instance`
with `replicate_source_db` in its own shard.

RDS for MariaDB keeps automated backups and restores to a time as RDS for
MySQL does. Its engine writes a binary log only when told to, so the instance
runs MariaDB with `--log-bin=binlog`, into the same `binlog.NNNNNN` files, and
`rdsKeepsLog` names MariaDB beside PostgreSQL and MySQL. MariaDB's binary log
differs in what the replay reads: its GTID event (type 162) opens a
transaction without a BEGIN, a standalone one wraps a single DDL or
non-transactional statement, a transaction ends at its XID event or a
`COMMIT` query, and the GTID event dates the transaction in whole seconds, so
a restore replays the transactions dated before the restore time's second, as
`mariadb-binlog --stop-datetime` does, and the tests restore to a whole second.
The base backup's replay start and the replay's end both stop before a
transaction the log holds only in part. MariaDB's applier runs
`CHANGE MASTER TO ... MASTER_USE_GTID=no` and `START SLAVE SQL_THREAD UNTIL`
with `mariadbd`, checks its UNTIL position only as it reads a next event the
cut relay log never has, and moves on to the relay log the server opened after
the copied ones, so the replay ends once the applier has executed up to the
cut or moved past the copied logs. The SDK suite restores an RDS for MariaDB
instance to a time beside PostgreSQL and MySQL, the CLI suite with `aws rds
restore-db-instance-to-point-in-time --restore-time`, and the Terraform suite
through `aws_db_instance`'s `restore_to_point_in_time` with `restore_time`.

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

The vendor CLIs drive those paths past 5 MiB, where apitools and
googleapiclient switch to the resumable protocol: `gcloud storage cp`,
`gcloud artifacts generic upload`, `gcloud artifacts files upload` and
`bq load` each have a CLI test with a 9 MiB payload generated in the test's
temporary directory. `bq` builds its client from BigQuery's Discovery document
whenever its API root is not Google's, so the simulator serves that document,
byte-identical to the vendored one, at `/$discovery/rest?version=v2`; and
BigQuery's REST errors carry the `errors[]` entry (`reason`, `domain`,
`message`) whose `notFound` reason is how `bq mk` learns a dataset is absent.

Every API the simulator implements serves its own Discovery document, the way
Google's APIs do: `GET /$discovery/rest?version=…` under the API's host. The
simulator embeds each vendored document (`simulator-gcp/discovery/`, copied by
`scripts/fetch-gcp-discovery.sh` and held byte-identical by a test) and indexes
them by the document's `name` and version. The name is the service label of the
host that serves it, which the `rootUrl` is not for every API — Compute
Engine's and the Discovery service's own name `www.googleapis.com` — so the
regional and mTLS hosts reach the same document through `gcpServiceFromHost`.
A bare address:port names no API, so it serves a version only one implemented
API publishes, and `v2` stays BigQuery's for `bq`; `v1`, which most publish,
answers 404 rather than a guess.

The Discovery service's directory is served from a capture of the real one,
`simulator-gcp/discovery_directory_vendored.json`: the directory entries for
the embedded documents verbatim, which `discoveryRestUrl` and `preferred` they
carry included, the ids each of `www.googleapis.com` and
`discovery.googleapis.com` serves through `apis/{api}/{version}/rest` (the
two differ, and neither serves every listed document — Cloud Run Admin v2 and
Eventarc are per-host only), and each API's default version for a request
without one. A test holds the capture and the embedded documents to the same
set. The Discovery service's own document declares no auth scopes, and
Google answers documents and the directory anonymously, so both are exempt
from the bearer check; the 404 and 400 bodies are the captured ones.
The Discovery service's `RestDescription` schema predates `mtlsRootUrl` and
`serviceVersion`, which the served documents carry, so
`specs/cloud-api/gcp/discovery-v1.supplement.json` declares both and the spec
validator checks them.

Eventarc and Cloud Build both publish `/v1/projects/{p}/locations/{l}/triggers`.
Google tells them apart by host, and so does the simulator; the harnesses
reach each through its own host, `eventarc.googleapis.com` or
`cloudbuild.googleapis.com`, with an HTTP proxy that delivers the request to the
simulator — gcloud through `HTTP_PROXY`, the Go SDK through its HTTP client's
proxy — so the request a client sends is the one it would send to Google.
At a bare address the simulator still resolves a create by Eventarc's required
`triggerId` and a read by which service holds the trigger.

A project has its Compute Engine default service account in IAM from the moment
Cloud Resource Manager creates it, because every API is enabled on a new
project here and Compute Engine creates the account when its API is enabled.
The account is created with the project, not on every start, so one a client
deleted stays deleted. Its email names the project number, so the `-` wildcard
resolves it through Cloud Resource Manager.

The GCE metadata server answers for the workload that asks. A Compute Engine
instance is found by its private address in the real-execution index; a Cloud
Run container by the address of its network namespace, which the simulator
reads from the Docker engine's container list (sidecars share the first
container's namespace) and maps through the container's labels to its service,
instance, job execution or worker pool. The project ID comes from the resource,
the number from Cloud Resource Manager, and the `default` account is the one
the resource runs as, or the project's Compute Engine default service account,
as on Google. A key the workload does not hold — the number of a project Cloud
Resource Manager does not know, an account the workload does not run as —
answers 404. A caller outside every workload is answered for the simulator's
default project, `sockerless`.

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
that settled without them. A Cloud Run job task starts its containers in
`dependsOn` order: the first to start owns the network namespace the others
join, and a container starts once each container it depends on has started and
passed its startup probe; a probe that fails fails the attempt with a status
naming it. gcloud's `--depends-on` reaches the Knative surface as the
`run.googleapis.com/container-dependencies` template annotation, which the
simulator folds onto the containers' `dependsOn`. Deleting a job or an
execution stops what it still runs, and cancelling an execution that has
completed leaves it as it is, which is what gcloud's cancel reads as
"completed successfully before it could be cancelled". A job that names
`startExecutionToken` or `runExecutionToken` (v2, or the Knative `spec` that
`gcloud run jobs replace` sends) starts the execution `<job>-<token>` unless it
exists, and stays reconciling, with its create or update operation running,
until that execution has started or completed; a failed execution fails both
the job's `Ready` condition and the operation, and the Knative condition
carries the message gcloud prints. A job name and token of 63 characters or
more, or both tokens at once, are refused. `operations.wait` on REST and `WaitOperation` on
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

A Cloud Build step whose builder is not `gcr.io/cloud-builders/docker` runs as
Cloud Build runs every step: a container of the builder image with the build's
workspace mounted at `/workspace`, in the step's `dir`, under its
`entrypoint`, `args`, `env` and resolved `secretEnv`, pulled as the build's
service account from Artifact Registry or Container Registry. A step that exits
non-zero fails the build with its exit status and the last twenty lines it
printed. The docker builder still runs on the host's engine over the same
directory, so a file a container step writes is in the next `docker build`'s
context. Refusing every other builder had made the simulator unable to run any
build but a Dockerfile one — the runtime buildpacks a Cloud Run functions
deploy runs among them. A Cloud Storage source may be a zip archive as well as
a gzipped tarball, as the service documents; `gcloud builds submit` of a
`gs://…/source.zip` runs.

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

An Amazon S3 key's current object stays in `s3Objects`, so every listing,
conditional write and other service reading an object reads the latest
version unchanged; every other version of the key, and every delete marker,
lives in `s3ObjectVersions` under `<bucket>/<key>\x00<version id>`, the null
version under `null`. A version records a sequence number from a clock that
never repeats, which orders a key's versions newest first in
ListObjectVersions and picks the version a removal makes current again. The
version and its tags move together: a version that stops being current takes
its tag set out of `s3ObjectTags` with it, and one that becomes current again
brings it back. The single write path (`s3StoreObject`) applies the bucket's
versioning state, so PutObject, CopyObject, CompleteMultipartUpload and the
other services that write objects all version alike, and the payload adoption
at start keeps the contents every noncurrent version references.

An AWS owned KMS key (RDS, Amplify, CodeBuild, Firehose) gets its material on
first use through `kmsEnsureKeyMaterial`, which generates under the store's
lock: a separate check and generate let a Terraform apply's DB cluster and DB
instance both generate, and the second overwrote the material the first had
already sealed its master password under.

The S3 Smithy supplement declares GetObject's 206 Partial Content beside its
200: a ranged read, such as the Terraform `aws_s3_object` data source's, answers
206, and the trait names only the code an unranged read gets.

Amazon S3 Object Lock lives on the object version. Enablement is the bucket's
stored `object-lock` configuration, written at CreateBucket together with an
enabled versioning configuration, so the versioning machinery needs nothing
of its own; PutBucketVersioning refuses to suspend it. The single write path
applies the bucket's default retention to a version whose write asked for
none, so every service writing into a locked bucket honours it. Protection is
checked under the key's write lock in the version delete itself, so
DeleteObject and each DeleteObjects entry are refused alike, and a delete
without a version id only adds a delete marker, which Object Lock never
refuses. A PutObject carrying Object Lock headers needs Content-MD5 or a
checksum, as Amazon S3 requires: the AWS SDK for Go v2 sends one only when its
checksum calculation is `when_supported` or the input names an algorithm.
s3:BypassGovernanceRetention authorizes against the object, as the Service
Reference declares, per DeleteObjects entry.

An Elastic Load Balancing trust store reads its CA bundle and revocation lists
from Amazon S3 at the call, through the same object and version lookup S3's
own GetObject uses, so a bundle written a moment earlier is the one counted
and a version id names exactly that version.
The trust store keeps the bytes it read. Its content locations are presigned
URLs to that copy in a bucket Elastic Load Balancing writes, keyed by the
content's digest, so the URL serves what was ingested even after the
customer's object changes or goes, and the client fetches it from the same
endpoint it called.

Amazon EC2 instance-type facts come from the AWS Price List bulk offer file,
the one machine-readable, unauthenticated publication of them: the EC2 Smithy
model carries only the InstanceType enum. `scripts/fetch-aws-ec2-instance-types.go`
streams the us-east-1 offer, keeps the Compute Instance products, fails when
two products of one type disagree on a fact, and records the pinned offer URL
and its SHA-256. The simulator converts the verbatim attributes: "<n> GiB"
truncates to MiB, which gives DescribeInstanceTypes' figures for the legacy
sizes the Price List rounds; the processor names the instruction set, because
the offer says "64-bit" for both x86 and Arm. A fact the offer does not state
stays absent from the answer rather than being guessed, and a filter over one
answers Unsupported.

Amazon ECR's imageSizeInBytes counts the config blob with the layers and
leaves the manifest document out. The AWS CLI's own examples settle it: its
describe-images example reports cluster-autoscaler v1.13.6 at 48318255 bytes,
and its batch-get-image example prints the same digest's manifest, whose four
layers total 48315478 and whose config is 2777 bytes.

A Cloud Storage bucket belongs to a project that exists. The insert resolves
its `project` through Cloud Resource Manager and stamps that project's number,
and the service agent is named for the same number, because gcloud's
`storage buckets notifications create` reads the bucket's `projectNumber`,
asks for that number's agent and grants it publish on the topic before the
insert that checks it. A fixed number on every bucket and an agent named for
the project ID had each answer look right alone while the flow granted one
identity and checked another. gcloud and Terraform name the topic by its
relative name (`projects/{p}/topics/{t}`), so the insert accepts it and stores
the full `//pubsub.googleapis.com/` name. Tests create their buckets in
projects Cloud Resource Manager holds.

A Cloud Run service instance starts its containers through the job path's
ordering (`cloudRunContainerStartOrder`): the first container in that order
owns the network namespace and publishes the ingress port, whichever container
it is, and the others start in the background, each once its dependencies have
started and passed their startup probes. The ingress container always runs a
startup probe (Cloud Run's default TCP probe when it configures none), and the
request that started the instance waits for every probe. Starting the ingress
first and every sidecar at once let an ingress that needs its sidecar at start
up exit before the sidecar listened. The Knative service surface folds
`dependsOn` into the revision template's container-dependencies annotation and
back, as the job surface does.

A Cloud Run worker pool runs as many instances as its manual instance count
(the minimum in automatic scaling), and each instance is a container group
started by the same code a job task uses (`startCloudRunContainerGroup`):
`dependsOn` order, startup probes, Cloud Storage volume binds and write
ingestion, and the stop signal with Cloud Run's ten-second grace. A changed
template replaces every instance, a changed count starts or stops the
difference, and the update or delete that retires an instance answers only once
its containers have stopped and the writes they made through the volume are
objects, so a client reads the effect of the stop the moment the call returns.
A Cloud Run instance runs through the service-instance path
(`ensureCloudRunServiceInstance`): its ingress container publishes its port
behind Cloud Run's default TCP startup probe. Creation, `instances.start` and an
update run it, and `instances.stop` and deletion stop it, on both API versions.
Worker pools log under the `cloud_run_worker_pool` monitored resource and
instances under `cloud_run_instance`, the resource types `gcloud run
worker-pools logs read` and `gcloud alpha run instances logs read` filter by.
gcloud's worker-pool `deploy`, `update` and `delete` speak Cloud Run v2 over
gRPC, which the simulator does not serve, so the CLI suite deploys over REST
and reads the pool through `logs read` and `gcloud storage`. The Go REST
client sends a worker pool's `instanceSplitStatuses[].type` as the enum's
number, which the simulator maps to its name.

A worker pool's or instance's create, update and `instances.start` answer with
an operation that is not done, and the resource reports `reconciling` and a
`CONDITION_RECONCILING` `Ready` condition until every instance has started and
passed its startup probes. Then the operation completes with the resource, the
pool's created revision becomes its ready revision and its observed generation
catches up; an instance that cannot start — an image that does not pull, a
failed startup probe — fails the operation with INTERNAL and the start error
and the `Ready` condition with the same message, and the pool keeps its last
ready revision. Answering at once had reported a pool whose image did not exist
as ready. A pool whose scaling runs no instance settles before the call
answers. Cancelling the operation stops the instances it was starting and fails
`Ready` with the reason `Cancelled`; deleting the resource aborts it. The
existing tests had deployed images that did not exist, so they deploy runnable
ones, and the round-trip tests whose images do not exist assert the failed
deploy.

A Cloud Run instance whose container exits is restarted as its
`restartPolicy` says: ON_FAILURE (the default) after a non-zero exit, ALWAYS
after any exit except a clean one of an instance of several containers, NEVER
not at all. Cloud Run documents restarting a failing instance "up to 3 times
sequentially" before it is FAILED and publishes no delay between attempts, so
the simulator restarts at once and fails the fourth exit; a clean exit that is
not restarted leaves the instance stopped. The instance's record settles before
its exit is logged, so a reader that waits on the log line finds the outcome
recorded.

A simulator restarted on its state directory keeps the instances of every
stored worker pool and of every stored instance not stopped or failed running,
as Cloud Run's control plane never restarts the instances it runs. A persistent
simulator leaves its workloads running when it stops, and the next one adopts
them (`cloudrun_adopt.go`): every container of an instance carries its
instance's group label and a digest of the template it runs, so the adoption
finds whole instances of the current template whose containers all still run,
attaches to them (`sim.AdoptContainer`, and for an instance's network-namespace
owner its log stream and its ingress route), registers them where
`runCloudRunWorkerPool` and `runCloudRunInstance` look, and removes every
other container the earlier process left for the resource, which includes the
containers of a stopped instance or a failed pool. Those then start only what
is missing, and a reconciliation the restart interrupted settles. An adopted
container's log stream replays its whole output, so the adopted sink drops the
lines written by the resource's newest Cloud Logging entry. Stopping a
service instance or an instance stops its containers before it ends the owner
container's log stream, and waits for that stream to drain, so what a container
writes as it stops reaches Cloud Logging; the stream had ended before the stop
signal was sent. The SDK suite
restarts a simulator under a worker pool and an instance, reads the same
container's hostname through the instance's URL before and after, deletes both
and finds the stop lines of the containers the first process started, with
their start lines logged once.

A service's instance and a job execution's task survive the restart the same
way. The restarted simulator adopts, for every stored service, the instance
running the service's template whole, so the next request reaches it, and
removes every other service container: instances of other revisions and of
services since deleted. Its output reaches Cloud Logging under the service's
`cloud_run_revision` resource through the same deduplicating sink. A job
execution still running keeps its task's containers: the restarted simulator
adopts them, arms what is left of the task's timeout counted from the
execution's start, and settles the execution from the main container's exit,
which a task that exited while no process watched it reports at once. Only an
execution whose containers are gone fails, and the containers of an execution
that no longer runs are removed. The adoption has to precede the recovery that
fails executions without a workload, so both run at start-up with the rest of
the Cloud Run resumption rather than at registration. The SDK
restart suite adds a service, read through its URL before and after the
restart, and a job whose `startExecutionToken` execution runs across it and is
then cancelled, and finds each container's start and stop lines logged once.

A Cloud Run instance's `urls` are served by the Cloud Run front end the way a
service's URL is: a request whose Host is one of them reaches the instance's
running ingress container with the method, path, query, headers and body, and
gets the container's answer back, behind the same invoker check
(`run.routes.invoke` through the instance's own policy or one it inherits, or
`invokerIamDisabled`), whose ID token names one of the instance's URLs. The
instance runs its containers itself, so a request finds no container while it
is stopped or between restarts. The SDK suite invokes an instance with the
invoker's ID token after granting `roles/run.invoker` through the v2 REST
client's setIamPolicy; the CLI suite grants it with `gcloud alpha run instances
add-iam-policy-binding` and invokes with the token `gcloud auth
print-identity-token` mints. The Terraform provider has no Cloud Run instance
resource.

A project has one number, the one Cloud Resource Manager assigned. Cloud DNS,
Cloud Build, Cloud Run's service agent, Compute Engine, BigQuery and Cloud
Logging each resolve the project through `crmProjectNumber` and name their
identities for that number; a number hashed from the project ID gave each
service a plausible answer of its own, so a client that read the number from
Resource Manager and the agent from another service named an identity nobody
recognised. A project that does not exist (or is pending deletion) is refused
with each service's own error: 403 `PERMISSION_DENIED` for Cloud DNS, Cloud
Build and Cloud Logging, 404 for Compute Engine and BigQuery. Compute Engine
addresses the project by ID or number and keys its record by the ID. Tests
create their projects in Cloud Resource Manager before using them. The Cloud
Build push test reads the pushed manifest accepting the OCI image index a
BuildKit build pushes, which the registry otherwise answers 404.

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

An ARN format's identifiers end at the next `/`, except the ones AWS documents
as names that may be paths — `SecretId`, `ParameterNameWithoutLeadingSlash`,
`EntityPath` and every `…WithPath` — which end only at a `:`. Without that
exception the tagging check of a CreateSecret for `edd/workspace/ws-1` fell to
`"*"`, and a grant scoped to the `edd/workspace/*` prefix allowed the untagged
create and refused the tagged one.

A create that carries tags is authorized twice, as AWS does: as itself and as
its service's tagging action, with `<service>:CreateAction` naming the create
on the second check. Which tagging action a create adds is generated, not
listed: `iamTagOnCreateActions` holds, for every operation whose Service
Reference entry authorizes an action annotated `IsTaggingOnly` beside one that
is not, those tagging actions — 297 operations across 31 services, Amazon S3
excepted because it reads its tagging from headers and control-plane
documents. Whether a request carries tags is read in the shape its protocol
sends them, Smithy RPC v2 CBOR included, so the check reaches the awsJson and
awsQuery gate, the AWS Lambda REST gate, the Amazon CloudWatch CBOR routes
and EventBridge Scheduler's universal targets alike. The tagging check
authorizes against the resource being created — for Amazon EC2 the wildcard
of each tag specification's resource type — and reports no `aws:ResourceTag`,
because that resource has none yet. Where the create itself authorizes against
a parent, the check names the type it mints under that parent with the
identifier the service assigns as the wildcard (`iamMintedResourceARNs`):
Elastic Load Balancing's CreateListener a `listener/app/<lb>/<id>/*` under
its load balancer, CreateRule a `listener-rule/…/*` under its listener, and
Amazon ECS's RunTask and StartTask a `task/<cluster>/*`. AWS Lambda's
CreateFunction authorizes, and tags, the `function:<name>` its body names,
and the Amazon CloudWatch RPC v2 CBOR routes derive their resources from the
CBOR body as the JSON routes do from theirs, so PutMetricAlarm authorizes
`alarm:<name>` rather than `"*"`. The AWS CLI's awsJson1_0 CloudWatch
requests carry their tags in the same `Tags` list, which the check reads too. Session tags on an `AssumeRole` need the
role's trust policy to allow `sts:TagSession` too.

An AWS STS session keeps the tags it was given on its temporary credential,
with the keys that are transitive. `aws:PrincipalTag/<key>` reads the session's
tags over its role's, a session tag replacing a role tag of the same key,
compared case-insensitively. An AssumeRole a session signs keeps that
session's transitive tags and refuses a passed tag that would replace one, and
needs sts:TagSession only for the tags the request itself passes. A SAML
assertion's `PrincipalTag:<key>` attributes and a web identity token's
`https://aws.amazon.com/tags` claim are session tags too, authorized as
sts:TagSession against the trust policy with the federation context, so a
statement can condition tagging on `saml:aud` and `aws:RequestTag/<key>`.

A DeleteObjects entry is authorized as its own s3:DeleteObject or
s3:DeleteObjectVersion. An entry the caller may not delete is that entry's
AccessDenied `<Error>` in the DeleteResult, Quiet mode included, and the
entries the caller may delete are deleted; the gate hands the handler the
refused entries on the request context.

An Amazon S3 control-plane resource has one tag set, held in
`s3ControlResourceTags` under the resource's ARN. The creates that take tags —
CreateAccessGrantsInstance, CreateAccessGrantsLocation, CreateAccessGrant,
CreateStorageLensGroup and CreateAccessPoint — write theirs there, the deletes
remove it, and TagResource, ListTagsForResource and the IAM gate's
`aws:ResourceTag/<k>` read nothing else, so a tag given at create time and one
added later are indistinguishable to every reader. TagResource on an Access
Grants location or grant ARN requires that location or grant to exist, not
merely the instance. A Storage Lens configuration and a Batch Operations job
are the exceptions: their own tagging operations
(PutStorageLensConfigurationTagging, PutJobTagging and their Get and Delete
pairs) keep their one tag set, the IAM gate reads `aws:ResourceTag/<k>` from
it, and the trio refuses their ARNs, since the Service Reference lists neither
type for TagResource, UntagResource or ListTagsForResource.

The Azure Key Vault data plane had answered any request that carried a bearer
of any kind. It now authenticates and authorizes each one the way Key Vault
does, in Key Vault's order. The token must be one the simulator signed and
unexpired, issued for Key Vault (`https://vault.azure.net`, its application ID,
or `https://<keyVaultDns>` for the cloud `/metadata/endpoints` describes, the
audience a custom-cloud client such as Terraform's acquires) by the vault's own
tenant (`sts.windows.net/{tenant}/` or `login.microsoftonline.com/{tenant}/v2.0`);
a request without one, or with another, gets 401 `Unauthorized` with the
`Bearer authorization="<authority>/<vault tenant>", resource="https://vault.azure.net"`
challenge clients start their token acquisition from, and the documented
`AKV10000`, `AKV10022` and `AKV10032` messages. The network rules come next:
`publicNetworkAccess: Disabled` refuses with 403 `ForbiddenByConnection`, and a
`Deny` default action admits only a client address an IP rule names or one in
a subnet a virtual network rule names, refusing with `ForbiddenByFirewall` and
the address it saw. Last, `kvDataPlaneOperation` maps each route to the
permission its REST reference documents — an access-policy permission in the
keys, secrets or certificates category and the Azure RBAC data action — and
`keyVaultGrants` decides it under the vault's model: role assignments matched
through the shared `dataActions`/`notDataActions` matcher at the object, the
vault or a scope above when `enableRbacAuthorization` is set, access policies
in the vault's tenant otherwise, a compound identity's only for tokens its
application obtained. Either may name a group the caller belongs to, directly
or through nested groups. A refusal is 403 `Forbidden` with Key Vault's
`AccessDenied` or `ForbiddenByRbac` inner code and a message naming the caller
as `appid=…;oid=…;iss=…`. App Service's Key Vault reference resolution goes
through the same decision. The Key Vault Crypto Officer, Crypto User,
Certificates Officer and Certificate User built-in roles joined the four Key
Vault roles the simulator served.

Every test that wrote to a vault had relied on any token working. Each now
grants itself access the way an operator does: an access policy naming the
object ID its own token carries (`data.azurerm_client_config.current` in
Terraform, `az keyvault create`'s creator policy, `az keyvault set-policy`), or
a role assignment (`az role assignment create`) on a vault that uses Azure
RBAC. A role assignment at a vault's own scope does not move with the vault,
so the move tests grant at the destination group as well. That exposed three
gaps the Azure CLI reaches on the way: Microsoft Graph's `GET /me`, which
answers an app-only token's caller 400 "/me request is only valid with
delegated authentication flow." (so `az keyvault create` falls back to looking
the service principal up); the OData `any`/`all` lambda, which
`servicePrincipalNames/any(c:c eq '<appId>')` needs; and a service principal's
`servicePrincipalNames`, which hold its appId. An AD FS authority
(`<host>/adfs`, the shape the CLI's custom-cloud login uses) issues its tokens
for the simulator's tenant, the tenant its subscriptions report, so a vault
`az keyvault create` makes accepts them. `keyVaultDns` in `/metadata/endpoints`
became `vault.<suffix>`, as `vault.azure.net` carries the label.

An App Service app's code had acquired tokens through `IDENTITY_ENDPOINT` as
one simulator-wide identity, presenting a header every workload shared. An app
or slot with a managed identity now gets its own `IDENTITY_HEADER` secret,
issued on first start and bound to the app's resource ID, and the endpoint
(`GET /msi/token`, api-version 2019-08-01) authenticates the request by it and
mints the app's system-assigned identity's token — `oid` its principal,
`appid` its client ID, `xms_mirid` the app's resource ID — or the token of the
user-assigned identity `client_id`, `principal_id`, `object_id` or `mi_res_id`
selects among the app's own, refusing one the app does not have with App
Service's 400 "Unable to load the proper Managed Identity.". An app without an
identity gets neither variable, as on App Service. The instance metadata
service's token endpoint is a separate handler that keeps its own behaviour.
`container-command files-http` relays `GET /identity-token` to the endpoint, so
the tests ask for a token the way app code does; the Terraform harness's
simulator listens on every interface, as the SDK and CLI suites' do, so an app
reaches it.

Container apps and Container Apps jobs carry a managed identity as sites do,
through the same settling code: `identity` (common types v3, which spells the
combined type `SystemAssigned,UserAssigned` and reports `{"type": "None"}` for
none), a system-assigned principal kept for the resource's life and
registered in the directory under the resource's name, and user-assigned
identities resolved to their principal and client IDs. A workload with an
identity gets `IDENTITY_ENDPOINT` and its own `IDENTITY_HEADER`, and
`/msi/token` resolves the header to the app, slot, container app or job it was
issued to. A container app is stored before its replicas start, so a replica
that asks for a token at once finds the identity it started with. A secret
that names a `keyVaultUrl` is read from Key Vault as the identity its
`identity` names — `system` or an attached user-assigned identity — under the
vault's access policies or Azure RBAC, at the version the URL pins; an
environment variable's `secretRef` carries the secret's value, which no
workload had received before, and a registry password reference reads the same
values. A secret the identity cannot read fails the PUT or PATCH with 400
`Field 'configuration.secrets' is invalid ... Unable to get value using Managed
identity <identity> for secret <name>`, and a `secretRef` that names no secret
fails it too; a job execution that cannot read its secrets fails and logs why.

App Service had imported a Key Vault certificate whenever any access policy of
the vault granted any object the secret `get` permission, and always from an
Azure RBAC vault. It reads the secret as its first-party service principal,
"Microsoft Azure App Service" (application ID
`abfa0a7c-a6b6-4736-8310-5855508787cd`), which the directory holds in every
tenant as Microsoft Entra does, so the import is authorized for that principal
alone through the data plane's own decision; the tests grant it the way an
operator does, with `az keyvault set-policy --spn <appId>`, a role assignment,
or an `azurerm_key_vault_access_policy` whose object ID
`azuread_service_principal` reads from Microsoft Graph.

`Vaults_UpdateAccessPolicy` had replaced a principal's policy on `add` and
removed it whole on `remove`. Each operation addresses the policy of the same
tenant, object and application IDs and works per permission category: `add`
merges the request's permissions in, `replace` sets them, `remove` takes them
away, and a policy an update leaves with no permissions goes; add and replace
append a policy no entry matches, remove ignores it, and every other policy
stays as it was. `azurerm_key_vault_access_policy` relies on all three, sending
`remove` with the permissions it reads back.

A front end — the Container Apps ingress, the App Service front end, Azure Load
Balancer, Application Gateway, the Application Load Balancer, Amplify Hosting's
compute, the Cloud Run front end and the external Application Load Balancer —
had written an error body after a forward failed while it copied the target's
body, so the error landed inside the response the client was already reading.
A failure after the target's status and headers reached the client now relays
what the target sent and closes the client's stream unfinished, as a proxy
that streams its upstream's answer and then loses it does; a failure before
that is still answered with the front end's own error. Each simulator carries
the decision itself (`abortStartedForward`), since `lbplane.Forward` reports
the failure and leaves the answer to the cloud's front end; Cloud Run removes
an instance whose ingress container died before it aborts the response.

A sitecontainer's `environmentVariables` had reached its container as written,
the app setting's name in place of its value. Microsoft.Web defines each
entry's value as the name of an app setting whose value the container gets
under the entry's name, and an empty string when the setting does not exist;
the main container and every sidecar now get that, with a Key Vault reference
in the setting resolved as the app's own environment resolves it. The Azure CLI
refuses a spec file whose entry names an app setting the app lacks, so the CLI
test removes the setting afterwards to reach the empty string.

A sitecontainer's `inheritAppSettingsAndConnectionStrings` had been stored and
ignored: the main container got every app setting and connection string, and
a sidecar none. Microsoft.Web passes them to a sitecontainer as environment
variables unless the flag is false, and fills an unset flag with true; the
simulator now does both, for the main container and each sidecar, and keeps
the platform's own variables and the container's `environmentVariables` either
way. The Azure CLI's `--sitecontainers-spec-file` never sends the flag, so a
sitecontainer it creates inherits; the CLI test sets the flag false through
`az rest`. The `http-localhost-probe` image's `relay-local` mode relays a
request to a sidecar's port over the shared loopback, so a test reads a
sidecar's environment through the main container. The `azurerm` Terraform
provider has no sitecontainer resource.

An app had resolved its Key Vault references only when its container started,
so a rotated secret reached a running app only on a restart. App Service caches
the values for 24 hours and then re-fetches them: each start records the values
its containers got and schedules the re-fetch 24 hours on; a re-fetch that
finds them unchanged keeps the app running and schedules the next, and one that
finds a changed value — a versionless reference to a rotated secret, a disabled
version, a revoked grant — restarts the app on the new values, at once when
Always On and on its next request otherwise. The vendored Microsoft.Web
specification defines no operation that forces the re-fetch, so the simulator
offers none; a configuration change restarts the app and resolves the
references afresh, as it always did.

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

A deleted Google Cloud service account is a resource IAM still holds, not an
absence. The deleted account lives in its own store keyed by unique ID, not as
a flag on the live row, because the live store is keyed by email and a new
account may take that email during the 30-day window; undelete is keyed by
unique ID for the same reason, which is why gcloud refuses anything else. The
window is IAM's documented 30 days measured against the clock at each delete
and undelete, with no timer of the simulator's own, the way Cloud Storage's
soft-deleted objects lapse at their `hardDeleteTime`.

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
prints after binding (`simready`), not on a health loop, and every harness
that fronts one with the Caddy HTTPS gateway starts Caddy through
`testutil/httpsgateway`, which reads Caddy's JSON log: the http app names the
certificates it manages, and certmagic's `tls.cache` logger, which the
gateway's Caddyfile routes to standard error at debug level, reports each one
as it enters the cache handshakes read. The harness returns once every managed
name is cached and then makes one health request, because certmagic logs
`certificate obtained successfully` before it caches the certificate, so that
line leaves a window in which a handshake still finds none. The AWS suites wait
for an alarm state with the AlarmExists waiter filtered by StateValue, for a
target with TargetInService, and watch a queue that must stay empty with one
long poll for the window instead of receives in a loop.

The suites then waited on events where the clouds offered one. The AWS CLI
suite waited with `aws cloudwatch wait alarm-exists --state-value` and, for a
service replacing a stopped task, with `aws ecs wait tasks-stopped` followed
by `services-stable`; the SDK suites did the same with TasksStopped and
ServicesStable, waited for a task's managed Amazon EBS volume with
VolumeDeleted, for an RDS instance with DBInstanceAvailable before connecting
once, and for Cloud Map registrations on GetOperation instead of retrying
RegisterInstance. The rds-restore Terraform fixture seeded its snapshot through
the SDK and waited with DBSnapshotAvailable, which retired the fixture's
hand-signed Query helpers. A restart test opened CloudWatch Logs Live Tail on
the function's log group before the asynchronous invoke and restarted on the
START line. An API destination's endpoint handed each call to the test over a
channel. The Event Hubs Latest consumer sent its event once azeventhubs logged
the completed receiver attach through azcore's log listener. Azure CLI Location
polls answered 202 by waiting their Retry-After (`azLocationResult`).

Container Apps gained its revisions, replicas and log streams. A container app
reports `eventStreamEndpoint`; its revisions list, read and restart through
ContainerAppsRevisions, and their replicas — one per
`minReplicas`, named `{revision}-{hash}-{suffix}` — list and read through
ContainerAppsRevisionReplicas, each container advertising its
`logStreamEndpoint`. The streams sit on the event stream host under
`/subscriptions/…/containerApps/{app}/…`, outside the ARM path, take the token
getAuthToken issued, and serve newline-delimited `{"TimeStamp","Log"}` lines
(or text) with `tailLines` and `follow`: a console stream opens with the
service's "Connecting to the container" lines, carries the container's output
as it is written, and ends when the container exits; the app's `eventstream`
carries its system events (AssigningReplica, ContainerCreated,
ContainerStarted, ContainerTerminated). The simulator keeps the newest 10 MiB of
a container's output, the kubelet's default containerLogMaxSize. The SDK and
CLI suites read an app's console through the stream (`az containerapp logs
show --follow`) instead of polling Log Analytics.

A container app keeps a revision history, as the service does. A change to
`properties.template` creates a revision — `{app}--{random 7}` first,
`{app}--0000001`, `--0000002` after it, or `{app}--{revisionSuffix}` when the
template names one, refused when the app already has it — with the template
it was created from, its own `createdTime` and its own replicas; a change to
`properties.configuration` creates none and applies to the revisions the app
has. A Single-mode app deactivates every revision but its latest, a Multiple
mode app keeps them active, and inactive revisions report `lastActiveTime` and
are removed oldest first beyond `maxInactiveRevisions` (stamped 100 when
unset). `ingress.traffic` (stamped `latestRevision` 100 when unset; `az
containerapp ingress traffic set` sends the weights as strings) must name
revisions the app has and add up to 100, reports as each revision's
`trafficWeight`, and splits the requests the app's ingress FQDN
(`{app}.internal.{env}…`) receives; a revision answers on its own FQDN
(`{revision}.internal.{env}…`, the app's `latestRevisionFqdn` for the latest).
ActivateRevision and DeactivateRevision start and stop a revision's replicas.
Restart, activate and deactivate answer 200 with the JSON string the service
returns and the Azure CLI prints (`"Restart succeeded"`, `"Activate
succeeded"`, `"Deactivate succeeded"`), captured from the Azure CLI's own
recorded test of those commands.

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
`objects.list` filtered on them, and `objects.viewFullContext` read one back;
the Go clients, gcloud (`--custom-contexts`, `--update-custom-contexts`,
`--remove-custom-contexts`, `--metadata-filter`) and the Terraform provider's
`google_storage_bucket_object.contexts` all drove them. gcloud's
`objects update --clear-custom-contexts` sends an empty `custom` map, which a
patch treats as no change (Google's Go client sends `contexts` as null to clear
them), so the CLI test removes contexts by name.
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
The Azure SDK tests name theirs the same way: `uniqueName` for namespaces,
queues, resource groups, sites, zones and log tags, and `uniqueAlnumName` for
the resources whose names admit no hyphen or stop at 24 characters (storage
accounts, key vaults, managed HSM pools, container registries). A Cosmos DB
test provisions an account of its own rather than sharing one, and a test that
reads a subscription-wide listing, such as the deleted managed HSM pools, picks
out its own row instead of counting the listing.

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
same two things instead of re-reading the queue every 50 ms, and checks that
its stream is still open before it leases: a negative acknowledgement and the
client's cancellation that reach a waiting stream together left `select` free
to pick the acknowledgement, and the closed stream then held the redelivered
message for its whole ack deadline. Messages a failed send never delivered
are released uncounted. The dead-letter test nacks on the stream that received
each attempt, as a subscriber client does, because a nack sent beside a stream
the client is closing can reach the service first. The REST pull
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

The quarantine's self-test (`scripts/test-latest-deps-quarantine.sh`) had read
the live registries, and failed once when the Terraform registry listed
`hashicorp/null` 3.3.2 before its version document carried `published_at`. It
serves every registry from fixtures it writes: a Go module proxy over Go's
`file://` protocol holding testify's version list and publication records,
with `proxy.golang.org` answering only the checksum-pinned go.mod files and
archives, and a local HTTP server holding the Terraform registry's provider
index and version documents and the GitHub API's tag, ref, commit and release
documents, which answers an unknown credential with 401 as GitHub does. The
check reads them through `GOPROXY`, `DEPS_TERRAFORM_REGISTRY_URL` and
`DEPS_GITHUB_API_URL`, the last two defaulting to the public services. Every
publication time is fixed, except one Terraform release the test dates an hour
before it runs, so the newest release is always inside the default window;
and the incident itself is a case, a listed version whose document carries no
`published_at` failing the run by name. Reading `GOPROXY` runs with
`GOTOOLCHAIN=local`: under a Go older than a module's `go` line, `go env`
otherwise downloads the newer toolchain first, through the very proxy a stalled
run cannot reach, with no deadline.

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
release missing part of its contents. The simulator images build from the
committed `simulator-<cloud>/dist` console bundle, the one `go install` and the
release binaries embed, with no console build stage, and keep the Go caches in
cache mounts. They export no BuildKit cache: every commit changes the source
layer and the cache mounts never reach an export, so a `mode=max` export only
re-uploaded the base images and the console stage's `node_modules`, and once
spent 569 s of v2.0.7's fifteen-minute AWS image job doing so after the image
was already pushed. Manifest composition retries only a broken connection.

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

The Google Cloud SDK suite once took 14.4 to 14.9 of its fifteen minutes. Its
slow tests waited on holds and schedules they had chosen, not on events. A
Cloud Run job that must be seen running now runs `container-command log-until
MESSAGE PATH`, holding until an object appears in a Cloud Storage bucket the job
mounts read-only as a Cloud Run volume, and the test writes that object once it
has observed the running state; a cancelled execution's hold is never released.
`TestCloudRun_ExecutionRunningState` had held for thirty seconds and then
waited out the RunJob operation's polling backoff, 36.5 s on CI and 74 s
locally; it takes about a second. The RDB snapshot test waited fifteen seconds
for a start time it had set ahead of the create; it now asks for a schedule
with no start time, which snapshots at once. The waits that remain are the
cloud's or the engine's own: Pub/Sub's ten-second minimum ack deadline in
`TestPubSub_GRPC_AckDeadlineRedelivery`, the five-second
`busy-reply-threshold` before a Redis replica running a script answers BUSY in
`TestMemorystoreRedis_LimitedDataLossFailover`, and Redis's two-second hold on
a new cluster primary.

`Dockerfile.test`, the shared harness image, had matched `.gitignore`'s
`*.test` and was never committed, so every `make docker-test` failed. It is
committed, un-ignored by name, with every toolchain pinned to a version and a
digest, because the image decides what a test result means.
