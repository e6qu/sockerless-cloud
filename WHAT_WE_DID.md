# WHAT WE DID

The decisions behind the shape of this repository, kept so they need not be
rediscovered. `CHANGELOG.md` dates each release, `BUGS.md` holds the open bugs
(resolved ones live in git history), and `STATUS.md` is the snapshot.

## Origin

The simulators came out of the sockerless monorepo as a fresh snapshot without
history. The per-cloud directories became `simulator-{aws,gcp,azure}` so
`go install` produces binaries with those names, and every module path moved
to `github.com/e6qu/sockerless-cloud/*`, bringing the console packages, the
vendored specifications with their surface tables and behavioural registries,
the simulator scripts and hooks, the Firecracker and realexec harness, and the
simulator CI jobs.

The simulators are a general-purpose reimplementation of slices of the clouds,
not a component of their first consumer. A simulator reads nothing from a
consumer's conventions: no image path, app setting or header named after a
consumer changes what it does (see Execution).

## The framework is one module

Each simulator once carried its own `shared/` copy of the framework, and the
copies drifted: fixes landed in one and never the others. The framework is the
`sim` module, pinned by pseudo-version like `realexec` and `ui-auth`. What is a
cloud's own stays in that cloud's module and reaches the framework through
hooks: the error writers (`AWSError`, `S3ErrorXML`, `GCPError`, `AzureError`),
the AWS JSON and Query routers, the sandbox profiles, the console coordinates
(`ConsoleOptions`, through which Azure registers its Microsoft Entra
federation broker), the request rewrites (Amazon S3's zonal virtual-hosted
addressing and Azure Resource Manager's case folding, both
`Config.RewriteRequest`), and the registry behaviours the three container
registries disagree on (`BaseResponse`, `RefuseChunkedUpload`,
`AdmitRepository`, `Scope`, `Authorize`). A Google Cloud method served over
both REST and gRPC shares one implementation returning a gRPC status, which
the REST handler writes through `GCPStatusError`.

A pin must carry the working tree's content, and
`check-support-module-pins.sh` fails when it does not: `go.work` resolves the
tree, so a stale pin passes every workspace build while the installed binary
lacks the fix. A support-module change lands in two pushes — push, pin the
pushed commit, push again — and the pin moves onto the merge commit
afterwards, because a pin on a branch head stops resolving once the branch is
deleted.

## Mechanisms each simulator carried moved into sim

Mechanisms the three simulators each built alone live in cloud-neutral
packages, with each cloud's wire shapes, error envelopes and names left in its
own module:

- `workloadhost` (the coordinates a workload dials back on, one policy for
  moby, Podman and Docker Desktop), `bg` (counted background work, including
  every Google Cloud and Azure goroutine), and `workload` (container groups
  with sidecars in the main container's network, bootstrap POSTs, image
  platforms, docker builds that interrupt on cancel).
- `archive`: every zip and tar extraction goes through an `os.Root` with a
  size limit; a string-prefix containment check let a sibling directory
  through.
- `blobstore` and `sparse`: payload files written in one pass with their
  digests, HTTP conditions, byte ranges and listing pages. Cloud Storage keeps
  each generation's bytes in its own payload and mirrors only the live one into
  the bucket directory a Cloud Run mount reads.
- `simjwt` and `oidcfed`: one signing-key store and one cached, bounded
  external-issuer verifier. AWS `GetWebIdentityToken` issues tokens signed
  under a per-account issuer whose discovery document and JWKS the simulator
  serves.
- `listq`: list paging and filters. A page token the simulator never issued is
  refused with the error each service declares. AWS's event-pattern grammar is
  one matcher with the documented differences between Amazon SNS filter
  policies and Amazon EventBridge patterns, and Lambda event-source mappings
  apply their `FilterCriteria`.
- `kvstore`: throughput buckets, ordered read-write locks, time-to-live
  sweeping and change logs. Cosmos DB answers 429 with a retry-after on
  provisioned throughput, expires items by time to live, serves both
  change-feed modes and reads one key range per query; Azure Table storage
  enforces If-Match and runs a batch under its partition's lock.
- `msgq`, `streamlog`, `delivery` and `cron`: Amazon SQS, Pub/Sub, Azure
  Service Bus and Queue Storage lease messages from one engine, so Pub/Sub
  dead-letter and retry policies, Service Bus lock durations, delivery counts,
  duplicate detection and AMQP peek-lock are enforced; Amazon Kinesis and Azure
  Event Hubs append to one log with retention enforced. Push delivery for
  Amazon SNS HTTP, EventBridge targets, Event Grid and Pub/Sub push retries and
  dead-letters by each service's policy, and one cron evaluator fires
  EventBridge scheduled rules and Scheduler schedules in their time zones,
  Application Auto Scaling scheduled actions, Container Apps scheduled jobs and
  Logic Apps recurrences.
- `realexec/fabric` and `realexec/lbplane`: each cloud's realized networks,
  subnets, interfaces, microVMs and NAT addresses live in one fabric, and the
  load-balancer forwarders, health probes, health tracking and TLS listeners
  are one data plane that never follows a target's redirect and grades a probe
  by the cloud's own matcher.
- `dbengine` with `pgwire` and `mysqlwire`: the managed relational database
  data plane of Amazon RDS, Cloud SQL and Azure Database for PostgreSQL. The
  MySQL relay logs into the engine as a client does: it answers an auth switch
  to `mysql_native_password`, `caching_sha2_password` or `sha256_password`,
  and, its link to the engine having no TLS, completes a full authentication by
  requesting the engine's RSA public key and sending the password under
  RSA-OAEP, so a refused login reaches the client as the engine's own 1045.

## Fidelity rules that came from bugs

- **A served count is not proof a handler exists.** Google Cloud and Azure
  hold every served operation to a route naming its literal path segments; the
  routes that legitimately dispatch inside a handler are listed with the
  reason. A multi-segment wildcard had counted Cloud Storage's per-object ACLs
  as served while `objects.get` answered them.
- **An unserved operation declares itself** with a 501 naming what is missing;
  the mux's 404 is indistinguishable from a missing resource.
- **A registered operation is not an implemented one.**
  `scripts/classify-sim-handlers.go` marks handlers that answer without
  reaching state, and the surface tables carry the marker. A ✓ means only that
  a handler reaches state.
- **An answer never invents the record it describes.** Every answer comes from
  what the simulator holds, or is the service's refusal. A member the simulator
  has no basis for is omitted rather than filled: an AWS Lambda REPORT line
  carries the measured memory peak or no figure, and an error reply carries no
  tracking identifier the simulator would have to mint.
- **Read the schema, not the floor comment.** Only a response whose *required*
  content the simulator would have to invent is declined, and it declines by
  naming what is missing. Published catalogues that can be read
  deterministically are vendored — the Azure managed WAF rule sets, Google's
  interconnect locations, the Cloud Armor expression sets — with source,
  retrieval and counts locked by a test.
- **A catalogue and a finding are different things.** A published set exists
  whether or not anyone asks, so an empty answer is false; a risk or a
  recommendation is something an analysis detected, so an empty collection is
  true of a simulator that runs no analysis.
- **Judge a route on every client.** The generated Go client sends
  `softDeleted=true` and gcloud sends `softDeleted=True`, so query booleans go
  through `strconv.ParseBool`. `docker push` sends a blob in one `PATCH` and
  Podman sends it on the `PUT`, so Artifact Registry refuses the *second* write
  into an upload session, which is the chunking Google names.
- **Real clients decide what a document does not list.** A Discovery enum is
  the best evidence until a real client contradicts it: Cloud Run's condition
  `reason` is enum-typed but incomplete, and `gcloud run jobs executions
  cancel` compares against the literal `Cancelled`. The validator leaves such
  fields unjudged with the evidence beside it.
- **The simulator is never stricter than the service.** Amazon S3 answers 204
  to `PutBucketPolicy` where the model says 200, and three Smithy patterns
  reject values AWS returns. Corrections live in
  `specs/cloud-api/<cloud>/<document>.supplement.json`, each pinning the value
  it replaces and its evidence. An Amazon ECR token works against any registry
  the principal can reach and an Artifact Registry token's scope is not the
  gate, so neither refusal exists here.
- **The validators judge three dimensions**: unknown fields, a required member
  a response omits, and, for AWS and Google Cloud, a value outside a declared
  enum (Azure's `modelAsString` enums permit other values). Discovery documents
  express required-ness only for requests, so
  `TestRequestsMissingARequiredPropertyAreRefused` drives each insert with the
  property removed and requires a refusal.
- **An int64 field answers as a string.** Proto3 JSON reads an int64 from a
  number or a string and writes a decimal string; a Cloud Build worker pool
  stores `privatePoolV1Config.workerConfig.diskSizeGb` as the string and
  refuses a non-integer.
- **A workload's answer is not the cloud's.** The Azure validator skips a
  response served on an App Service site's host, its SCM host, or a container
  app's or revision's ingress FQDN: the body is what the workload wrote.
- **An error shape is attested, never invented.** Where the service documents
  a refusal but not its code — a resource move onto an occupied name — the
  simulator carries the attested message with no leaf code. Each registry's
  `/v2/` answer and challenge was captured from the service: ECR an empty body
  with no content type and a Basic challenge, Artifact Registry `text/html`,
  ACR `{}`.
- **Two APIs onto one setting are one store.** Blob soft delete is one store
  behind the ARM `deleteRetentionPolicy` and the data plane's service
  properties. Cloud Run v1 is a projection over the v2 stores; every Google
  Cloud service's gRPC and REST doors read one store, and a cross-door test
  writes through one and observes through the other. Azure tags have one
  holder per scope.
- **A state and the event that explains it are one write.** An Amazon ECS
  rollout's `COMPLETED` or `FAILED` and the event recording why go in one
  write, and the scheduler state that decides it is read first so no second
  store's lock is taken under the first.
- **A filter the simulator cannot evaluate is refused**, since an ignored
  filter answers everything and reads as a result: the Azure resource list
  refuses an unsupported `$filter` with `InvalidFilterInQueryString`, and a
  malformed page token or cursor is refused rather than read as none.
- **Optimistic concurrency is enforced where the document declares it.** An
  omitted etag or `resourceVersion` is unconditional and a stale one answers
  409 ABORTED; a supplied etag is checked before an update mask merges.
- **An asynchronous operation answers as its specification says.** An Azure
  operation declared long-running with a 202 answers 202 and serves its result
  through the `Location` poll (azcore's no-op poller for a synchronous answer
  overwrites the client's pager with nil). A Compute Engine insert returns a
  running operation and boots behind it on a context detached from the
  request. Operations minted complete answer cancel as each service documents
  for a finished operation.
- **A stubbed external dependency fails loudly and names itself.** Amazon SNS
  SMS and mobile push say they need a carrier or Apple's and Google's hosts.
- **A retention the service reports also bounds what the simulator holds.**
  Sweepers delete stopped Amazon ECS tasks, temporary credentials and AWS WAF
  sampled requests at their real lifetimes through `Store.Prune`, 500 rows at a
  time; filtering them out of answers alone let a deployed simulator reach
  21,409 task rows and 1.2 million credentials. Where deleting a record would
  change an answer, the answer moves into what the client presents: a session
  token carries its expiration under the simulator's key, so a pruned
  credential is still refused as expired. CloudTrail event history keeps 90
  days apart from each CloudTrail Lake event data store's copy, CloudWatch Logs
  applies `retentionInDays`, and DynamoDB's TTL sweep consumes no write
  capacity.
- **A figure the service refreshes lazily comes from a cache.** DynamoDB's
  DescribeTable counts and sizes come from a cache a background refresh updates
  at most once a minute (DynamoDB refreshes them about every six hours). Hot
  DynamoDB stores are `sim.MakeCachedStore`s, in memory with SQLite as the
  durable copy.
- **A request answers what the service answers at once.** Amazon ECS StopTask
  returns the task with desired status STOPPED and stops the containers as
  tracked background work.
- **A resource the service moves through transitional states moves through
  them.** An Amazon Kinesis Data Streams consumer answers
  RegisterStreamConsumer CREATING and DeregisterStreamConsumer DELETING and
  settles as tracked background work, as a resharding stream passes through
  UPDATING; SubscribeToShard refuses a consumer that is not ACTIVE. A Client
  VPN endpoint's Cedar authorization policy passes through creating, updating
  and deleting. DryRun on GetRecords, GetShardIterator, PutRecord and PutRecords
  validates and answers `DryRunOperationException`.
- **A delete the service refuses is refused.** Amazon ECR DeleteRepository
  answers `RepositoryNotEmptyException` for a repository holding images unless
  the request sets `force` (Terraform's `force_delete`).
- **A NAT gateway route translates the subnets its route table governs now.**
  The sources are recomputed on every association change — AssociateRouteTable,
  DisassociateRouteTable, ReplaceRouteTableAssociation, CreateSubnet,
  DeleteSubnet — and include the subnets the main route table governs
  implicitly. The probe reads the source address a task's request arrives from,
  since untranslated egress still reaches the host. DeleteRouteTable refuses
  the main route table or one still associated with `DependencyViolation` and
  withdraws its NAT translations when it succeeds.
- **A managed EBS volume is a block device, and the engine is asked for nothing
  it has to interpret.** The volume is an image file of the requested size and
  filesystem; a privileged helper attaches it to a loop device and the engine
  mounts `/dev/loopN`. An `o=loop` option passed on Podman, which implements it
  in `mount(8)`, and failed on moby, whose `mount(2)` flag table lacks it.
- **Engine work never runs under a store's write lock.** An `Update` callback
  returns what to remove and the caller removes it afterwards.
- **A trust policy is evaluated, and a federated identity verified, as AWS
  does.** Assuming a role evaluates its trust policy with the condition keys
  AWS documents for the caller — OpenID Connect claims under the provider's
  name, the `saml:` attributes of a signed assertion — and a SAML response is
  verified against the provider's metadata. Tests sign real tokens and
  assertions (`testutil/samlidp`).
- **A request is authorized as the action AWS names, not as its operation.**
  The two differ for 94 operations: a multipart upload is `s3:PutObject`, a
  batch send `sqs:SendMessage`, a re-encrypt `kms:ReEncryptFrom` on one key and
  `kms:ReEncryptTo` on the other. The unambiguous ones are generated from the
  Service Reference; the rest resolve from the request, down to each key of a
  batch delete and each statement of a PartiQL call.
- **Condition keys are read as each service defines them.** AWS CodeBuild's
  keys name the request member they read, so one resolver serves them; other
  services register request-settled keys per service. A key is populated only
  where AWS's definition fixes its value, so AWS Glue's Lake Formation keys and
  `s3:JobSuspendedCause` stay absent. A key counts as built only for a service
  whose prefix the source composes or whose populator is registered.
- **Each service's keys and tags are spelled as that service spells them.**
  Amazon RDS declares `rds:db-tag/`, `rds:cluster-tag/` and nine more per
  resource type and has no `rds:ResourceTag/`; IAM users and roles carry
  `iam:ResourceTag/<k>` and its policies only `aws:ResourceTag/<k>`. A
  request's tags are read from the member path each service's model serializes
  — `Tags.Tag.N`, `Tags.member.N`, `TagSpecification.N.Tag.M`, a lower-case
  `tags` list, `TagKey`/`TagValue`, or a map.
- **A key that says who called is set only when someone else called.**
  `kms:ViaService` and `kms:GrantIsForAWSResource` come from the
  service-initiation the request carries, and a check a service runs on a
  principal's behalf evaluates each action against that service's context.
- **An S3 operation is authorized in the namespace AWS publishes it under** —
  s3, s3express, s3-outposts or s3-object-lambda, each against its own
  reference and ARN format.
- **What the gate cannot resolve is named, with the reason.**
  `TestIAM_DeclaredConditionKeysAreResolvedOrClassified` fails unless every
  declared key is resolved or classified with what the simulator would have to
  model first, and a classified key that becomes resolvable fails its own row.
- **A key is proven per action, by the request a client sends.**
  `TestIAMConditionKeyCoveragePerAction` renders every served operation from
  its vendored Smithy model in its own protocol with every member filled,
  classifies it with the gate's classifiers, builds the gate's context for each
  action it is authorized as, and checks every key that action declares.
  Resources whose state some keys report — tagged buckets, objects and access
  points, a key behind an alias, a bounded role, a sized task definition, one
  tagged resource of every type Elastic Load Balancing, Amazon ElastiCache, AWS
  Systems Manager and AWS WAF tag, Amazon ECS capacity providers, container
  instances and task sets, AWS Cloud Map namespaces and services — are created
  through each service's API first. The pairs it does not build are listed in
  `testdata/iam_condition_key_gaps.tsv` with a reason from a closed table, so a
  new gap and a fixed one both fail. Resource tags resolve from the ARNs the
  gate derives, and every create stores the tags it carries where
  ListTagsForResource reads them.
- **A key about a source is read from the source.** A daemon's size is its
  daemon task definition's; a blue/green deployment's engine, name,
  encryption, Multi-AZ placement and tags are those of the DB instance or
  cluster it clones; a DB snapshot's `rds:BackupTarget` is its instance's; a
  cluster restore's `rds:StorageSize` is its snapshot's or source cluster's. A
  key that differs between entries of one request — the version each
  DeleteObjects entry names — travels with that entry's authorization target.
  GetAccessPoint authorizes `"*"`, as the reference says, and reports the tags
  of the access point its path names. A web-identity or SAML session keeps the
  provider claims AWS STS declares on sts:AssumeRole, and an AssumeRole it
  signs carries them, so a chained role's trust policy can name the person
  behind the first session. A probe whose member takes either of two resource
  kinds is rendered once per kind.
- **A function URL is served at its own host.**
  `<url-id>.lambda-url.<region>.on.aws` reaches the function as a payload
  format version 2.0 event whose result is the HTTP response. An AWS_IAM URL
  verifies the lambda SigV4 signature and authorizes lambda:InvokeFunctionUrl
  and lambda:InvokeFunction, the second with `lambda:InvokedViaFunctionUrl`; a
  NONE URL admits whoever the resource-based policy admits, and AddPermission
  writes the conditions its scoping members name. CORS preflights are answered
  from the URL config without invoking the function.
- **An ARN is minted in the Service Reference's format.** An AWS Glue
  integration's ARN is `…:integration:<id>`, keyed by the id Glue assigns at
  CreateIntegration, so a policy naming it matches the ARN the IAM gate
  derives.
- **A resource that exists implicitly is not minted on reference.** CloudWatch
  supports one metrics dataset, `default`, which carries tags; any other
  identifier is ResourceNotFoundException.
- **Maintenance may not end the service.** A persistence fault panics in a
  handler, where net/http turns it into a 500. On a background goroutine a busy
  or locked database ends the sweep, which resumes next pass; a corrupt row
  still panics; `StartBackground` contains a panic in any worker.
- **A stalled CLI call fails with the evidence.** `runCLI` runs every call
  with `--debug` in a process group of its own, bounded by 60 s or the test's
  deadline less 30 s; past the bound it reads the simulator's `/debug/inflight`
  and goroutine profile on the suite's diagnostics port, kills the group, and
  fails with both and the tail of the CLI's debug log. It never retries.
- **A stopping simulator lets go of work in flight.** Amazon ECS lifecycle
  steps, AWS Lambda event source mapping batches and asynchronous invocation
  attempts return once the context `StartBackground` hands them is done, and
  leave their work for the next process: an ECS stop stays stopping and
  recovery finishes it, an interrupted start leaves the task PENDING with its
  partial containers removed, an asynchronous attempt is rolled back and
  retried, and an event source mapping's messages become visible again after
  their visibility timeout. The task lifecycle lock is a channel so a step can
  stop waiting for it.
- **A request ends with the server.** The `http.Server`'s `BaseContext` is the
  background context the signal handler cancels before `Shutdown`, so every
  long-poll handler, which selects on its request's context, returns at once.
  `StartContainerSyncContext` bounds the image pull and the create by its
  caller's context, removes a container whose start it abandoned, and leaves
  the started container's lifetime to its handle; an AWS Lambda execution
  environment starts under its worker's context, so a shutdown stops an image
  download. `TestSimulatorStopsWithLifecycleWorkInFlight_SDK` keeps a Live Tail
  session, a synchronous Invoke and a StartSyncExecution on a Lambda task open
  across SIGTERM and exits within 5 s.
- **Work a request starts can outlive its caller but not the server.**
  `sim.LifetimeContext` returns, from any request's context, the server's
  background context; the outermost handler stores it on every request, and
  `Server.RequestContext` gives the same to a request a service builds to call
  a handler in-process. It panics for a context no server serves. Synchronous
  AWS Lambda Invoke and InvokeWithResponseStream, Amazon S3 Object Lambda
  transformations, S3 Batch Operations LambdaInvoke tasks, and Step Functions
  StartSyncExecution and TestState run under it, and the Step Functions
  interpreter aborts a synchronous execution when it ends. Amazon S3 event
  notifications and Lambda function destinations invoke asynchronously,
  through the same persisted dispatcher as an `Event` Invoke.
- **Recorded executions run as server background work and stop with it.** A
  Step Functions execution started by StartExecution, RedriveExecution, a
  nested `states:startExecution` task, EventBridge, EventBridge Scheduler or
  recovery, and an AWS Lambda durable execution's coordinator run under
  `StartBackground` and invoke their functions under its context. When it
  ends, the interpreter waits for the task in flight, records neither its
  outcome nor a Catch transition, and leaves the execution `RUNNING` at its
  checkpoint for the next process; a Parallel state waits for its other
  branches first, and a Distributed Map leaves its item executions unrecorded.
  An Amazon ECS task or AWS CodeBuild build a `.sync` task waits on keeps
  running for the next process to resume waiting on.
- **An Amazon S3 Batch Operations job runs after CreateJob answers.** CreateJob
  stores the job `New`, and background workers move it through `Preparing`
  (one task per manifest row), `Suspended` when it requires confirmation or
  `Ready`, `Active`, and `Completing`, `Failing` or `Cancelling` into its final
  status, writing the completion report. UpdateJobStatus confirms only a
  `Suspended` job and cancels a running one between tasks. Each task's outcome
  is recorded as it lands, and `s3RecoverBatchJobs` resumes after the last
  one. A LambdaInvoke task sends the invocation schema's 1.0 or 2.0 event with
  its user arguments and takes its outcome from the `results[]` entry for its
  task — `Succeeded`, `PermanentFailure`, or `TemporaryFailure`, which redrives
  the task after every other has run — with `treatMissingKeysAs` for a missing
  entry. The completion report is `manifest.json` and one results CSV per task
  status under `<prefix>/job-<id>/`. Tests wait on DescribeJob's status.
- **A Batch Operations CSV manifest lists keys URL-encoded**, as the
  S3BatchOperations_CSV_20180820 format requires: preparing a job decodes each
  key as a form value (`+` to a space, as AWS Lambda Powertools'
  `unquote_plus` does) and fails the job on a malformed escape. The
  LambdaInvoke event's `s3Key` carries the key URL-encoded, and the task
  invokes the function whether or not an object holds the key.
- **An image pull says why it pulled.** `pullImage` writes a `[sim-pull]` line
  when the held-image check fails, with the inspect error or the held and
  wanted platforms, and one per throttled retry with the attempt, error and
  backoff.
- **A test drives the production write path.** Retention tests store through
  the key RunTask uses, and a listing takes each object's key from the id it is
  stored under rather than from a field a writer fills separately.
- **A DynamoDB key orders as the service orders it.** Key values are encoded so
  numbers sort by value and binary by bytes, joined by a byte no encoded
  component contains (`dynamodb_keys.go`); a restore computes each item's key
  from the target table's schema. Every secondary index keeps ordered entries
  of its own (`dynamodb_index.go`), written only by the two functions that
  write items, so an index query reads in index order, returns what the index
  projects and resumes by the index's key. A page resumes after its start key's
  position whether or not the item survives and carries a LastEvaluatedKey
  whenever Limit is reached; a parallel scan's segment is a hash of the
  partition key. A transaction prepares every write before applying any. A key
  or item that does not fit the schema is refused in DynamoDB Local's words,
  and the differential scenarios compare message and code.
- **An archived S3 object stays archived.** A write's storage class is stored
  and reported, an unknown one is `InvalidStorageClass`, and reading or copying
  an unrestored GLACIER or DEEP_ARCHIVE object answers `InvalidObjectState` or
  `ObjectNotInActiveTierError` until a restore, whose `Days` sets the expiry,
  makes a temporary copy.
- **An operation answers on every protocol its model declares.** The Go SDK,
  and so Terraform, speaks only rpc-v2-cbor to Amazon CloudWatch; an operation
  without a CBOR route is served from its JSON handler, converted both ways by
  shapes generated from the vendored model, and a test fails on any model
  operation no protocol serves.
- **An operation names a type that exists.** Every Google Cloud long-running
  operation carries the metadata message its Discovery document or proto
  `operation_info` declares; the operation constructor requires that builder,
  so no service falls back to a type a gRPC client cannot decode. Each gets a
  fresh name in its service's operations collection and a row in the
  operations store, with `verb` and `target` filled, readable over REST and
  gRPC.
- **Each service spells its errors its own way.** An AWS error carries the
  message member (`message` or `Message`) and, for Amazon EFS, the `ErrorCode`
  member the serving service's Smithy model declares; a middleware resolves the
  model from the request's target or signing name.
- **A state change follows the work it names.** Amazon RDS instances and
  clusters report `stopping` and `starting` until their engines have stopped or
  started, and Application Gateway, target-pool and backend-service health
  comes from probes that have run, reporting `Unknown` until a verdict.
- **A restore writes a new object.** A restored Cloud Storage object gets a new
  generation and metageneration 1, and its preconditions are judged against the
  live object it replaces.
- **A managed cluster runs what it says it runs.** An Aurora cluster runs one
  PostgreSQL- or MySQL-compatible engine on its own volume behind writer,
  reader and member endpoints that follow its members' states; Kinesis
  reshards by real splits and merges; Application Auto Scaling acts on the
  CloudWatch alarms it creates within the policy's cooldowns; EC2 application
  status checks run on their own interval and thresholds.
- **An event reaches its subscribers because something emitted it.** Cloud
  Storage publishes JSON_API_V1 notifications for object writes, deletes and
  metadata changes; Eventarc delivers Cloud Storage triggers from them as
  binary-mode CloudEvents; Cloud Logging `entries:copy` copies the entries its
  sinks routed to the bucket.
- **An audited call writes its Cloud Audit Logs entry.** One middleware around
  the route table and one gRPC unary interceptor audit the calls. Cloud
  Storage keeps its own method names (`storage.buckets.create`,
  `storage.setIamPermissions`) in a route table; RPC-defined APIs resolve a
  REST call to its RPC through the `google.api.http` bindings of their
  registered descriptors, so the entry names the RPC's full name and the API's
  `google.api.default_host` and carries request and response as JSON with
  `@type`. Compute Engine and Cloud DNS resolve through their embedded
  Discovery documents: Compute Engine records `v1.` plus the method ID, the
  resource from the operation's `targetLink` and the `compute#operation`; Cloud
  DNS the method ID, the resource relative to the project and the request as
  its `cloud.dns.api` message. Cloud Resource Manager v1 records `SetIamPolicy`
  and `CreateProject`, and a `SetIamPolicy` entry carries the
  `google.iam.v1.logging.AuditData` policy delta. Cloud KMS records simple
  method names and IAM `google.iam.admin.v1.CreateServiceAccount`, naming a
  service account by unique ID. The caller is the principal its access token
  names. The permission in `authorizationInfo` comes from an explicit table
  where a method checks one its name does not spell
  (`cloudkms.cryptoKeyVersions.useToEncrypt`), otherwise from IAM's
  `service.collection.verb` convention. A configuration change lands in
  `cloudaudit.googleapis.com%2Factivity`; a read or data write lands in
  `%2Fdata_access` only when the project's `auditConfigs` enable it, and
  `setIamPolicy` changes `auditConfigs` only when its `updateMask` names them.
  A long-running call writes entries marked `operation.first` and
  `operation.last`, or one marked both when already done. When two APIs publish
  the same path (Eventarc's and Cloud Build's regional triggers) the host
  decides. A log filter crossing a repeated field compares each element. Eventarc
  `google.cloud.audit.log.v1.written` triggers receive the matching entries of
  their project and location (a global trigger every location's), filtered by
  `serviceName`, `methodName` and `resourceName` (optionally a
  `match-path-pattern`), as CloudEvents whose data is the `LogEntryData`.
- **A subscription stays open as long as the cloud holds it.** Amazon Kinesis
  Data Streams `SubscribeToShard` holds its event stream for the documented
  five minutes (`sim.DeclareWait`), sends the backlog from the
  `StartingPosition` and then one `SubscribeToShardEvent` each time a put,
  reshard or deletion signals the shard, each with `ContinuationSequenceNumber`
  and `MillisBehindLatest`. The event that drains a closed shard names its
  `ChildShards` and ends the stream. A second call for the same consumer and
  shard within five seconds fails with `ResourceInUseException`, and a later
  one takes the subscription over; deregistering the consumer ends the stream
  with `ResourceNotFoundException`. `DeleteStream` refuses a stream with
  registered consumers unless `EnforceConsumerDeletion` is set, and consumers
  carry their own tags.
- **A revoked session stays revoked.** `workforcePools.subjects.revokeSessions`
  records when a subject's sessions end, and every check of a simulator-minted
  access token refuses one issued to that subject at or before that second.
- **AMQP is a byte stream.** A WebSocket message or TCP read can end inside a
  frame, so the Service Bus and Event Hubs connection carries unconsumed bytes
  to the next read.
- **A link receives only what its address names.** A management reply link
  (`<entity>/$management`) never receives the entity's messages, and AMQP
  credit is the receiver's delivery-count plus link-credit minus the sender's,
  as AMQP 1.0 defines it.
- **A REST receive waits for its `timeout`.** Service Bus Receive and Delete
  and Peek-Lock wait up to the `timeout` query parameter (60 seconds when
  absent) and answer 204 only when no message became receivable. The wait
  wakes on the entity's arrival signal — a send, a scheduled message reaching
  its time, an abandon, a lock running out — ends with the request, and
  declares itself with `sim.DeclareWait`. A renewed lock re-arms the
  lock-expiry wake-up, and a restarted simulator re-arms the wake-ups of the
  scheduled and locked messages it loads.
- **A Log Analytics query runs or is refused, never half-read.** A tokenizer
  honours single, double, verbatim and obfuscated string literals; a typed
  expression parser handles `and`, `or`, `not()`, parentheses, the comparison,
  string, `in`/`in~`, `has_any`, `between` and `matches regex` operators, and
  arithmetic over numbers, datetimes and timespans; the tabular operators are
  `where`, `take`/`limit`, `project`, `project-away`, `project-rename`,
  `extend` (each item sees the columns before it), `order by`/`sort by`, `top`,
  `count`, `summarize` and `distinct`. Every expression is typed against the
  schema before a row is read, so an unknown table, column or function, a type
  mismatch, or an operator the engine does not run answers 400
  `BadArgumentError` with a nested `SyntaxError` (`SYN0002`, with line,
  position and token) or `SemanticError` — alone and per member of a `$batch`.
  Application Insights tables have their own schemas. The request's `timespan`
  bounds `TimeGenerated`. `dynamic` values work through `parse_json`/
  `todynamic`, property and element access, `array_length` and `tostring`.
- **A workspace reads only its own rows, and rows arrive only where something
  names the workspace.** A workspace keeps its rows under its customer id; a
  query names a workspace that exists (404 `WorkspaceNotFoundError` otherwise)
  and resolves tables against it. Rows arrive by three routes: a Container Apps
  environment whose `appLogsConfiguration` names the workspace writes container
  output to `ContainerAppConsoleLogs_CL` and the platform's events (replica
  events, a job execution's start, completion, failure and stop) to
  `ContainerAppSystemLogs_CL`; a site connected to an Application Insights
  component by `APPLICATIONINSIGHTS_CONNECTION_STRING` or
  `APPINSIGHTS_INSTRUMENTATIONKEY` writes its container output to the
  component's `AppTraces` (in the workspace a workspace-based component names,
  or a classic component's own store), while `containerlogs` keeps every line;
  a Logs Ingestion upload names a data collection rule by immutable id and a
  stream it declares, and each data flow carrying the stream runs its
  `transformKql` and writes to its `outputStream` table in each Log Analytics
  destination. Data collection rules and endpoints are served, and a rule is
  refused (`InvalidPayload`) when a destination workspace, output table or
  endpoint is missing, the table is still classic, or the transform does not
  bind or writes a column the table lacks. Workspace tables are served: the
  Azure tables every workspace holds, custom `_CL` tables with a
  `TimeGenerated` datetime column, retention and plan, and `migrate` of a
  classic table. A resource-centric query reads a workspace, a component's
  telemetry, or every workspace whose `_ResourceId` lies under the scope; an
  Application Insights app-id query reads its component's rows. A component's
  `AppId` is the id the data plane addresses it by and `ApplicationId` mirrors
  its name.
- **A proxy's bound comes from the resource, and idle is not a deadline.**
  `lbplane` keeps `Upstream.Timeout` (a fixed end) apart from
  `Upstream.IdleTimeout` (no byte moved either way for that long, upgraded
  connections included), and `Upstream.Activity` lets the caller move its
  `sim.DeclareWait` as bytes flow. The Application Load Balancer reads
  `idle_timeout.timeout_seconds` (1 to 4000, default 60) per request and
  answers its `504 Gateway Time-out` page past it; Container Apps ingress bounds
  a request at the documented 240 seconds and answers Envoy's `504 upstream
  request timeout`.
- **A load balancer adds its own headers after the hop-by-hop ones are gone.**
  Each data plane passes `lbplane.Upstream.RequestHeader`, which `Forward` runs
  after removing the client's hop-by-hop headers, so a client naming
  `X-Forwarded-For` in `Connection` strips only its own copy. Application
  Gateway builds its header set itself because its rewrite rule set must see
  the headers it inserts: it removes hop-by-hop headers with
  `lbplane.RemoveHopByHopHeaders`, inserts its six, then rewrites. The
  Application Load Balancer selects a listener by the port the client reached,
  never by a client-sent `X-Forwarded-Proto`, and forwards that listener's own
  protocol and port.
- **Platform front ends add only the headers their documentation states.** The
  Cloud Run, App Service and Container Apps front ends each pass a
  `RequestHeader` builder holding exactly the headers its platform documents:
  the Cloud Functions request-headers reference for Cloud Run, the App Service
  language and TLS-binding guides (`X-Forwarded-Proto`, `X-AppService-Proto`,
  `X-Forwarded-For` as `IP:port`), and the Container Apps ingress table.
  Headers seen on the real platforms but undocumented — Cloud Run's
  `traceparent` and `Forwarded`, the App Service server variables in a
  commented-out table, anything from Amplify Hosting compute — stay out until a
  source states them.
- **Load balancer attributes belong to a type.** The defaults are built per
  type from the LoadBalancerAttribute reference in the vendored Smithy model,
  the keys a type reports are the keys it accepts, and an Application Load
  Balancer's cross-zone load balancing, which the reference says "can't be
  changed", refuses modification with `InvalidConfigurationRequest`.
- **A Dedicated Host is its family's host in the Price List.**
  `scripts/fetch-aws-ec2-instance-types.go` keeps the offer file's Dedicated
  Host products beside its instance types: each family's vCPUs, physical cores
  and `instanceCapacity-<size>` counts, verbatim. AllocateHosts reports those
  cores and vCPUs and no socket count; a family the offer does not list gets
  neither. DescribeHosts counts what is left after the instances placed on the
  host. An instance carries its own Placement: RunInstances takes it from the
  request or the launch template, ModifyInstancePlacement changes it,
  DescribeInstances reports it in the subnet's Availability Zone, and a
  placement on a host without room for the type is refused with
  `InsufficientHostCapacity`.
- **A scaling metric is one the simulator publishes.** Target tracking sets
  the smallest whole capacity at which a metric moving in inverse proportion to
  capacity meets the target. ALBRequestCountPerTarget alarms on the
  `RequestCountPerTarget` the Application Load Balancer data plane publishes;
  the proxy path only adds to a counter, since writing each request to
  CloudWatch would read every metric stream per request. A predefined type
  whose metric nothing in the simulator emits gets no alarm.
- **A download URL addresses logs the workload wrote.** An AWS Glue Python
  shell run writes to the CloudWatch Logs groups the AWS Glue model names, and
  GetSystemLogsForJobRun and GetSystemLogsForSession store what those streams
  hold as an object and answer a presigned URL to it.
- **Every Elastic Load Balancing resource keeps its tags.** CreateListener and
  CreateRule store their tags, and AddTags, RemoveTags, DescribeTags and the
  IAM gate's `aws:ResourceTag/<k>` serve listeners, rules and trust stores as
  they do load balancers and target groups. Tagging a missing resource answers
  that type's not-found error and changes nothing. Elastic Load Balancing
  resources are identified by 16 lowercase hexadecimal characters, as their
  ARNs are in AWS.
- **A page token proves where it came from.** Every listing tags the tokens it
  issues and refuses one it never issued with the service's invalid-argument
  error.
- **A create is decided when it is accepted.** Every Amazon RDS create and
  restore handler claims its identifier (`rdsIdentifierClaims`) before it
  checks for the resource and holds the claim until the resource is stored, so
  a repeat answers DBInstanceAlreadyExists or DBClusterAlreadyExistsFault. The
  AWS SDK retries a request its HTTP client stopped waiting for, and two
  provisionings of one identifier ran initdb on one volume. The Terraform
  harness's SDK client has no overall timeout, as the SDKs' default client has
  none.

## Execution

Every workload runs as a real container on the engine the simulator started
against; there is no host-process path, and `SIM_RUNTIME=process` is API-only.
Startup refuses to serve in any mode that executes workloads when no engine
answers: a process that passes its health check and fails every workload in
the background is worse than one that does not start. Each cloud product's
sandbox profile applies to its containers, and every profile refuses the host
network and the engine socket.

A workload's stop grace comes from the cloud's own setting — an Amazon ECS
container definition's `stopTimeout` (30 seconds unset), Cloud Run's ten
seconds, a Container App template's `terminationGracePeriodSeconds`, App
Service's `WEBSITES_CONTAINER_STOP_TIME_LIMIT`. Azure Container Instances and
the managed-database engines keep five seconds because their clouds publish
none. A stopping Amazon ECS task signals all its containers together, so it
takes as long as its slowest container, and the awsvpc pause container stops
only after the containers in its namespace have exited; a task start reports
`pause-image` resolution apart from `pause-start`. An ECS container
definition's `workingDirectory` reaches the engine, which creates it when the
image lacks it, and an ExecuteCommand session inherits it. An AWS Lambda
invocation's timeout starts when the runtime first asks for work, with Init
bounded at ten seconds, as AWS documents.

The engine allocates every loopback port a workload publishes, and the
simulator reads the bound port from the container's inspection after each
start (`ContainerHandle.PublishedPort`, `sim.PublishedHostPort`); picking a
free port first raced anything that bound it in between. A stopped container
holds no published port and a resumed one gets a new one, so adopting an
engine left by an earlier process reads its ports after resuming it.

### App Service and Azure Functions

A site that names a container image runs it as App Service runs a Linux custom
container. The image's ENTRYPOINT runs with `siteConfig.appCommandLine`
(`az functionapp config set --startup-file`, Terraform's `app_command_line`)
or a sitecontainer's `startUpCommand` as its command; the app settings and
`PORT` are its environment; the front end forwards every request on one of the
site's hostnames to the container's port — `WEBSITES_PORT`, else 80, or the
main sitecontainer's `targetPort` — and passes the answer back untouched. A
request starts the container when none runs, and Always On starts it without
one; the start waits until the port accepts, the container exits, or
`WEBSITES_CONTAINER_START_TIME_LIMIT` (230 seconds unset) passes, and fails
with 503. A restart, a configuration write and an app-settings write restart
the container and keep its VNet integration; `PATCH config/web` merges onto
the stored configuration. The simulator reads no setting named after a
consumer.

A Linux web app on a built-in stack runs the platform's image for it:
`linuxFxVersion` `NODE|20-lts`, `NODE|22-lts` or `PYTHON|3.12` selects
`mcr.microsoft.com/appsvc/node` or `appsvc/python`, pinned to one build, with
`/home` mounted and `site/wwwroot` holding what MSDeploy and OneDeploy
deployed, or the package a `WEBSITE_RUN_FROM_PACKAGE` URL names, read-only. The
image's entrypoint runs Oryx: the startup command, else the detected app
(`server.js`, a `start` script, `app.py` under gunicorn), else the platform's
default page. The front end forwards to the port the image declares (8080 for
Node, 8000 for Python) unless `WEBSITES_PORT` names one, and a deployment
restarts the site. `webAppStacks` and `availableStacks` list exactly these
stacks from the same table. A site created without a kind reports `app`, or
`app,linux` on a reserved plan; every site reports `hostNameSslStates` with its
Standard and Repository (SCM) hostnames. A site the simulator has nothing to
run for answers 503 naming what it lacks.

A Linux function app on `Node|22` runs the Azure Functions host
(`mcr.microsoft.com/azure-functions/node:4.1054.250-4-node22-appservice`, the
build the floating `4-node22-appservice` tag named) with the same `/home`
layout: it loads the app from `site/wwwroot`, answers 401 for a function-level
function without a key and 404 for a route no function declares. Every site's
container gets the platform environment beside its app settings —
`WEBSITE_SITE_NAME`, `WEBSITE_HOSTNAME`, `WEBSITE_RESOURCE_GROUP`, a fresh
`WEBSITE_INSTANCE_ID` per start, and a per-site `WEBSITE_AUTH_ENCRYPTION_KEY` —
without which the host neither runs in App Service mode nor writes keys. With
`AzureWebJobsSecretStorageType=files` the host reads its keys from
`/home/data/Functions/secrets`, and the ARM key operations read that store
before answering and write it after changing a key. Otherwise the host uses its
blob store, as `DefaultSecretManagerProvider` does: `azure-webjobs-secrets/
<site>/host.json` and one `<function>.json` per function in the account
`AzureWebJobsStorage` names. The ARM key operations read and write those blobs
through the Blob service, refuse a connection string whose account is missing
or whose key or SAS does not open it, and generate keys the host has not
stored yet; a deleted function's blob stays. Values are encrypted as the host
encrypts them: an ASP.NET Core Data Protection payload under purpose
`function-secrets` and the empty key id, AES-256-CBC with HMAC-SHA256 keyed
through SP800-108 over HMAC-SHA512, with the first of `AzureWebEncryptionKey`,
`MACHINEKEY_DecryptionKey` or `WEBSITE_AUTH_ENCRYPTION_KEY`. Keys are the
identifiable secrets the host generates (33 random bytes, the `AzFu` signature
and a Marvin checksum), and `functions/admin/token` is signed with
`WEBSITE_AUTH_ENCRYPTION_KEY`. The ARM functions list includes each
`<function>/function.json` the content holds, with the HTTP trigger's invoke
URL; `functionAppStacks` lists the stack, and a site PUT naming its plan by bare
name records the plan's resource ID.

A web app's SCM site serves Kudu. Its host — the Repository entry of
`hostNameSslStates` that `az webapp deploy` and Terraform's `zip_deploy_file`
send to — is the app's subdomain of the ARM request host (`<app>.scm.<host>`)
or the `appServiceScm` template of `SIM_AZURE_ARM_EXTERNAL_DATA_PLANE_URLS_JSON`;
a handler wrapper routes a request whose Host is an app's or slot's SCM
hostname (or `<app>.scm.azurewebsites.net`) to that site's Kudu through a
generation index, so every store read Kudu reaches answers from an index. Kudu
admits the site's publishing credentials (`$<app>`, `$<app>__<slot>`) or the
subscription's deployment user as basic auth while the stored scm basic
publishing credentials policy allows it, and a Microsoft Entra token for Azure
Resource Manager or App Service. `/api/zipdeploy` (KuduSync: files the last zip
deployment wrote that the new package lacks are deleted) and
`/api/publish?type=zip|war|jar|ear|lib|static|startup` (OneDeploy's targets,
`path`, `clean`, `restart`) land the artifact through the placement the ARM
OneDeploy and MSDeploy operations share, synchronously (200) or async (202 and
a `Location` on `/api/deployments/latest`), behind a per-site lock that answers
a concurrent deployment 409. Each deployment is a Kudu record (`/api/deployments`,
`/latest`, `/{id}`, `/{id}/log`, legacy `/deployments`) that ARM's
`/deployments` proxies, with a deploymentStatus going `BuildInProgress`,
`RuntimeStarting`, then `RuntimeSuccessful` once the restarted site answers the
warmup request, or `RuntimeFailed` naming why; the ARM MSDeploy and OneDeploy
operations settle the same way (`BuildFailed` for a package that fails to
land), and once the build ends the status stops naming the operation's
Azure-AsyncOperation URL. `WEBSITE_RUN_FROM_PACKAGE=1` makes each zip
deployment the whole of wwwroot, read-only. The CLI suite reaches the
`.localhost` SCM host through an HTTPS proxy the test runs.

Kudu's WebJobs API serves the webjob records the Microsoft.Web webjob resources
read: `/api/webjobs`, `/api/triggeredwebjobs` and `/api/continuouswebjobs` in
Kudu's spelling; a PUT places a job (a zip, or one run file named by
`Content-Disposition`) under `App_Data/jobs` and rediscovers the site's jobs,
and a DELETE removes it. A triggered run answers 202 with its history entry as
`Location`, passes `arguments` on the command line and as
`WEBJOBS_COMMAND_ARGUMENTS`, records `External - <user agent>` as its trigger,
and answers 409 while the job runs or `WEBJOBS_STOPPED` is set; `settings`
reads and writes `settings.job`; start and stop drive a continuous job's
container. Advertised URLs carry the request's scheme. A triggered job whose
`settings.job` names a six-field NCRONTAB `schedule` runs at each occurrence in
`WEBSITE_TIME_ZONE`, with trigger `Schedule - <expression>`, skipping an
occurrence while the job runs or `WEBJOBS_STOPPED` is set; a schedule that does
not parse is the job's `error`, and like Kudu's the scheduler runs none it
missed while down. Runs write Kudu's log files under `data/jobs`
(`output_log.txt`, `error_log.txt`, `job_log.txt`, `job_scheduler.log`), which
the job records name by VFS URL once they exist.

The VFS (`/api/vfs`, and `/vfs` as Kudu's job log URLs spell it) reads, writes
and deletes under `/home` with Kudu's semantics: a trailing slash addresses a
directory, listed as JSON entries with `href` and `path`; the other spelling
redirects with 307; a file's ETag derives from its write time and an overwrite
or delete takes `If-Match`; a directory deletes only empty or with
`recursive=true`. A change under `site/wwwroot` rediscovers webjobs, and a
run-from-package app's wwwroot refuses writes. `/api/command` runs `sh -c` in
the site's image with `/home` mounted, the app settings in its environment and
the requested working directory, answers `Output`, `Error` and `ExitCode`, and
kills a command silent for `SCM_COMMAND_IDLE_TIMEOUT` seconds. `/api/settings`
merges Kudu's defaults, the settings written through it and the app settings,
in that order of precedence. `/logstream` writes a welcome line, then each line
the site's containers log, a quiet-minute line while nothing is logged, and
ends after `SCM_LOGSTREAM_TIMEOUT`.

Each deployment slot runs its own container as its app does, keyed by the
slot: the front end routes the slot's hostname to it, its `/home`, containers
and volumes go by `<app>__<slot>`, its container sees `WEBSITE_SITE_NAME` as the
app's name and `WEBSITE_SLOT_NAME` as its own (`Production` for the app), a
settings, configuration or storage-mount change restarts it, and deleting a
slot or its app stops its container and removes its storage. A swap is a
long-running operation exchanging what the two slots run: deployed content and
everything in their file systems (deployment history, webjobs and runs, Kudu
settings, `/home`), general settings and sitecontainers, and app settings,
connection strings and storage mounts except those `slotconfignames` sticks to
a slot and the `_EXTENSION_VERSION` settings, which stick unless every slot sets
`WEBSITE_OVERRIDE_PRESERVE_DEFAULT_STICKY_SLOT_SETTINGS` to 0 or false. Each
slot keeps its hostnames, publishing endpoints, Always On, scale, IP
restriction, CORS, diagnostic-log, VNet and TLS settings. The destination
starts on its new content before the swap succeeds; one that does not start
swaps back and fails the operation. Both slots then report the swap in
`slotSwapStatus`, which `azurerm_web_app_active_slot` waits on. Swap with
preview: `applySlotConfig` gives the slot the target's sticky app settings and
connection strings, keeps its own swappable ones, restarts it and reverts when
it does not answer; `resetSlotConfig` on either slot restores the source's
settings; the swap between the same two slots completes the preview, and a
swap of any other pair is refused while one is pending. `slotsdiffs` lists each
app setting, connection string and general setting the slots hold differently,
a swappable one as `SettingsWillBeSwapped` at level `Information` and a slot
setting as `SettingsWillNotBeSwapped` at level `Warning`.

`/home` is one persistent share the app, Kudu and every instance mount: a
deployment writes into it with KuduSync's manifest semantics and leaves files
no deployment wrote, a restore replaces it and restarts the app, a swap
exchanges the two slots' shares, and backups, snapshots, webjob discovery and
the Functions host's function list read it. A new app or slot starts empty, and
a deleted app's content stays for `restoreFromDeletedApp`. A custom container
mounts the share when `WEBSITES_ENABLE_APP_SERVICE_STORAGE` is true.

Each connection string reaches the container's environment — and a webjob's
and the Kudu command's — under its type's prefix (`MYSQLCONNSTR_`,
`SQLCONNSTR_`, `SQLAZURECONNSTR_`, `CUSTOMCONNSTR_`, `NOTIFICATIONHUBCONNSTR_`,
`SERVICEBUSCONNSTR_`, `EVENTHUBCONNSTR_`, `APIHUBCONNSTR_`, `DOCDBCONNSTR_`,
`REDISCACHECONNSTR_`, `POSTGRESQLCONNSTR_`); a write names a
`ConnectionStringType` or is refused, a site PUT's `siteConfig.connectionStrings`
sets them, and a change restarts the app or slot.

A stopped app or slot runs nothing: `WebApps_Stop` tears down its container and
stops its webjobs, nothing starts it again — a request, Always On, a swap's
warm-up, a deployment, a webjob schedule — and its hostname answers App
Service's 403 stopped-site page until `WebApps_Start` or an App Service
Environment's resume. An update leaves a stopped app stopped, since `state` is
read-only.

An app or slot carries managed identities: `identity` on a site PUT or PATCH
enables a system-assigned identity, registered in the directory for its
lifetime, and attaches user-assigned identities by resource ID;
`keyVaultReferenceIdentity` (SystemAssigned unless set) names the one the app
reaches Key Vault as. A Key Vault reference — `SecretUri=…` or
`VaultName=…;SecretName=…[;SecretVersion=…]`, in an app setting or connection
string — resolves when that identity is attached and the vault grants it the
secret: an Azure RBAC role assignment carrying
`Microsoft.KeyVault/vaults/secrets/getSecret/action` at the secret, the vault
or above (the Key Vault Administrator, Reader, Secrets Officer and Secrets User
built-in roles carry their published permissions), or else an access policy
with secret `get`; the secret, and a pinned version, must exist and be enabled
and current. The container, webjobs and Kudu see the value; an unresolved
reference reaches them as written, and `configreferences` reports why —
`MSINotEnabled`, `VaultNotFound`, `AccessToKeyVaultDenied`, `SecretNotFound`,
`SecretVersionNotFound`, `InvalidSyntax` or `OtherReasons` — with the
`identityType` it tried.

Configuring `sourcecontrols/web`, and each `WebApps_SyncRepository`, fetches the
branch's head over HTTP or HTTPS and deploys its tree, without `.git`, into
wwwroot with KuduSync's semantics, recorded under the commit's ID, author,
author email and message; a failed fetch is recorded as a failed deployment
while the app keeps what it ran. `siteConfig.scmType` is `None` until source
control sets it (`ExternalGit`, or `GitHub`/`BitbucketGit` for continuous
integration) and returns to `None` when it is removed, as
`azurerm_app_service_source_control` reads; a sync without source control is
refused.

### Cloud Run and Cloud Run functions

A Cloud Run service is served at its own URL,
`https://<service>-<hash>-<region>.a.run.app`, which `uri` and the Knative
projection's `status.url` report. The front end dispatches on the Host header,
so a client connects to the simulator's endpoint and names the service host.
Every method and path goes to the ingress container on `ports[0].containerPort`
(8080 unset, passed as `PORT`), bounded by the revision template's `timeout`
(300 seconds unset), and the answer comes back untouched; the caller's bearer
reaches the container with its JWT signature replaced by
`SIGNATURE_REMOVED_BY_GOOGLE`. A service with `invokerIamDisabled`, or whose
policy grants `run.routes.invoke` to `allUsers`, is public. Any other request
carries a Google-signed ID token — in `X-Serverless-Authorization`, which
leaves `Authorization` to the application, or in `Authorization` — whose
audience is the service URL or a custom audience, and whose principal holds
`run.routes.invoke` through the service's policy or one inherited from its
project, folders and organization, conditions included. An access token, or an
ID token for another audience, answers 401; a principal without the permission
answers 403 with Google's front-end error page. The token endpoint's
`id_token` for a JWT-bearer grant names the grant's `target_audience`, and the
metadata server's identity token names only the requested audience, so a
Google API answers either 401. Pub/Sub push and Eventarc deliver to a run.app
endpoint through this front end, under the same check. `PostBootstrap` reads an
error status as a failed invocation.

An instance takes traffic once its startup probes succeed: the configured
`startupProbe` (`tcpSocket`, `httpGet` with 2xx–3xx success, or `grpc` health)
with the API's defaults of a 1-second timeout, 10-second period and threshold
3, or Cloud Run's default TCP probe on the container port (timeout and period
240 seconds, threshold 1); sidecars that configure one are probed after it.
Probes and traffic go to the container's own address wherever one of the host's
interfaces is on the container's network, found from the host's interfaces
rather than by connecting, because the engine's published loopback port
accepts before the workload listens; a host that routes no container address
uses that port. A probe that fails its threshold, or a container that exits
first, fails the instance with 503.

A Cloud Run function is served by the Cloud Run service CreateFunction creates
for it. `serviceConfig.uri` reports the service's run.app URL and `url` the
function's `https://<region>-<project>.cloudfunctions.net/<function>` URL; the
cloudfunctions.net front end dispatches on the Host header, takes the function
name off the path and hands the request to the same service, admitting an ID
token for either URL. The service's IAM policy governs invocation on both, so
`gcloud functions add-invoker-policy-binding` and `--allow-unauthenticated`
write roles/run.invoker into it, and deleting the service deletes its policy.
`timeoutSeconds`, environment variables and CPU become the service template's
request timeout (60 seconds unset), environment and CPU limit, and an
UpdateFunction changing `serviceConfig` rolls a new revision while `uri` and
`service` survive. DeleteFunction deletes the service, its revisions and its
instance.

Cloud Run's v2 `RunJob` answers with an operation that stays running while the
execution runs, carrying the Execution as metadata as its `operation_info`
declares; the execution's settle completes it once tasks, the job's reference
and log lines are written — the Execution as response when every task
succeeded, the failed `Completed` condition's message under
FAILED_PRECONDITION, CANCELLED, or NOT_FOUND when the execution was deleted
mid-run. Cancelling the operation cancels the execution, and a restart
completes the operations of executions that settled without them. A job task
starts its containers in `dependsOn` order: the first owns the network
namespace, and each other starts once those it depends on have started and
passed their startup probes; a failed probe fails the attempt naming it.
gcloud's `--depends-on` arrives as the `run.googleapis.com/container-dependencies`
template annotation, folded onto `dependsOn`. Deleting a job or execution stops
what it runs, and cancelling a completed execution leaves it as it is. A job
naming `startExecutionToken` or `runExecutionToken` (v2, or the Knative `spec`
`gcloud run jobs replace` sends) starts `<job>-<token>` unless it exists and
holds its create or update operation and `Ready` condition until that
execution has started or completed; a failed execution fails both. A job name
and token of 63 characters or more, or both tokens at once, are refused.
`operations.wait` on REST and `WaitOperation` on gRPC block on the signal
`gcpFinishOperation` raises, bounded by the request's `timeout` or the caller's
connection.

Cloud Build's `CreateBuild`, its regional twin, `RetryBuild`, `ApproveBuild`,
`RunBuildTrigger`, the trigger webhooks and Cloud Run's `builds:submit` record
the build QUEUED and return its operation at once with a
`BuildOperationMetadata`; the build runs in the background under its `timeout`
(sixty minutes unset, ending TIMEOUT with DEADLINE_EXCEEDED), and the operation
turns done once the build is terminal and its steps have stopped. Tests wait on
the operation (`op.Wait`, `operations.wait`, `CreateBuildOperation(name).Wait`).

A Cloud Build step whose builder is not `gcr.io/cloud-builders/docker` runs as
a container of the builder image with the workspace at `/workspace`, in its
`dir`, under its `entrypoint`, `args`, `env` and resolved `secretEnv`, pulled as
the build's service account; a step that exits non-zero fails the build with
its exit status and last twenty lines. The docker builder runs on the host's
engine over the same directory, so a container step's files are in the next
`docker build`'s context. A Cloud Storage source may be a zip archive or a
gzipped tarball.

### Image pulls and build services

A workload host pulls its image as the cloud pulls it. The Cloud Run and Cloud
Functions hosts present the project's Cloud Run service agent's token to
Artifact Registry and Container Registry and nothing elsewhere. The Azure hosts
pull with what the workload declared — a managed identity's identity token,
exchanged through the registry's refresh-token grant, or a username and
password — and nothing for an undeclared registry. The Lambda and Amazon ECS
hosts resolve a pull-through-cache reference through its rule to the upstream
and pull a reference to this simulator's own port from its ECR with an
authorization token. An image held for another architecture is pulled for the
one asked for rather than run under emulation. A pull asks the host first, as
`docker run`'s "missing" policy does, and a registry's data-cap refusal is not
retried.

Cloud Build, AWS CodeBuild and Azure Container Registry Tasks give their docker
steps a Docker configuration built on the host's own (`sim.WriteDockerConfig`)
whose credential helper answers the build's registries with the build
identity's token and hands other registries to the host's helper; it names
those registries outright, because the legacy `docker build` sends the daemon
only the credentials of registries the configuration names. Every docker
invocation a step makes interrupts on cancel and bounds the unwind with
`WaitDelay`, since a killed CLI never tells buildkit to stop. A privileged
CodeBuild environment gets the simulator's own engine, and its output streams
to CloudWatch Logs.

ACR Tasks' `scheduleRun` records the Run Queued and answers 200 with it at once
— the Azure CLI's client accepts nothing else, and the Go SDK's poller completes
on a 200 naming no operation — and the build runs in the background, Queued →
Running → Succeeded, Failed, Canceled, Timeout (DockerBuildRequest `timeout`,
3600 seconds unset, 300 to 28800 accepted) or Error when the simulator stops
under it; a restart ends the runs a previous process left. `Runs_Cancel`
interrupts the docker steps and answers once the run reads Canceled. The run's
log is a blob that grows while the build writes, serves ranged reads, and gains
`Complete` metadata when the run ends, which `az acr build` streams until.
All four run request types share that lifecycle. A FileTaskRunRequest, an
EncodedTaskRunRequest and a TaskRunRequest of a task file run the ACR Tasks
YAML as Azure/acr-builder reads it: Go templates over `{{.Run.*}}` (date as
`20060102-150405z`) and `{{.Values.*}}` under `--set` and `--set-secret`, then
from v1.1.0 the `$` aliases, custom ones and the documented image aliases, then
`build`, `push` and `cmd` steps ordered by `when`, each with timeout, retries,
repeat, start delay, environment and ignore-errors. Steps share the source in a
run volume at `/workspace` on a run network where each step answers to its ID.
A TaskRunRequest runs the Task's step with the request's file, context,
arguments and values merged in. `listBuildSourceUploadUrl` hands out a SAS URL
into storage the registry service owns (an account name no customer can hold),
served by the Blob service's handlers; a relative `sourceLocation` resolves to
that upload. A DockerBuildRequest places an image name without a registry in
the run's registry. An agent pool runs as many runs as its `count`, the rest
wait Queued, and `listQueueStatus` counts them. The run engine drives cmd and
push steps through the Docker Engine API — network, volume, step containers
under step-ID aliases, archive-endpoint seeding, cleanup at the run's end —
presenting the run's identity token as registry credential; only `docker
build` stays a CLI invocation. A step's output reaches the run log through a
followed log stream opened at the container's start, and its exit code through
a next-exit wait registered before the start.

### Amazon ECS, DynamoDB and VPC placement

RunTask places a task only where it fits: it commits each placed task's
declared memory and CPU against what the simulator's own cgroup, or the
machine, can hold, and refuses with the service's `failures[]` shape before any
ENI, volume or record exists. The ledger is the task store plus in-flight
reservations, so two concurrent RunTasks cannot both take the last slot.

A PROVISIONED DynamoDB table runs token buckets per table and per global
secondary index, refilled at the provisioned rate with 300 seconds of burst,
spending the units `ConsumedCapacity` computes; batches return throttled
entries as unprocessed, and on-demand tables are never throttled. A
requests-per-second limiter was rejected as a fake.

VPC networks take their bridge subnet from a host-side pool rather than the
VPC's CIDR, so two live VPCs sharing a CIDR coexist; the workload's elastic
network interface address is a real secondary address, plumbed by an ephemeral
`CAP_NET_ADMIN` container so the workload keeps its capability-free sandbox.
The live networks are the allocator's only ledger, which makes a restart safe.
A simulator resource records its owning process — hostname and pid — and a
slice is reclaimed only from an owner on this host whose pid no process holds.

A run's containers are collectable from their labels alone: a detached reaper
collects its own run, the next simulator over the same state directory collects
what a killed one left, and a concurrent suite's workloads are never touched.
Every harness sets `SOCKERLESS_PARENT_PID`, and a simulator exits when that
process is gone; the variable is explicit because a guessed `os.Getppid()`
would end a `nohup`ed run when its shell closed. The parent-process watch and
the container reaper wait on the parent's exit event — a pidfd on Linux, a
kqueue `NOTE_EXIT` filter on macOS. A simulator binds its port before it prints
its banner, so `Listening on` means the port answers, and the harnesses start
it through `testutil/simready`, which returns on that line or fails when the
process exits first.

Every named volume a workload container mounts is created labelled with the
simulator's run, and the reaper and the shutdown cleanup of a non-persistent
simulator remove the run's volumes with its containers and networks.
`EnsureDockerNetwork` returns the network a concurrent caller created first.
Every container `sim` removes goes with `RemoveVolumes`, so the anonymous
volumes its image declares (MySQL's `VOLUME /var/lib/mysql`, Redis's
`VOLUME /data`) go with it while named volumes stay.

### Managed databases

Amazon RDS, Cloud SQL and Azure Database for PostgreSQL run real PostgreSQL,
MySQL and MariaDB engines on named volumes, with readiness classified by
SQLSTATE (57P03 is not ready, whatever the socket answers), credentials sealed
under the simulator's own key service, and snapshots that capture the data.

Every volume capture holds one crash-consistent point in time.
`sim.SnapshotVolume` pauses each running container that mounts the source
volume writable through the Docker Engine API's cgroup freezer for the length
of a `cp -a --reflink=auto` (copy-on-write where the volume store allows it),
then thaws it; a per-container hold count lets concurrent captures share the
freeze, and `sim.AdoptContainer` thaws a container a dead process left frozen.
The frozen engine keeps its connections, whose I/O waits out the copy. Amazon
RDS instance and cluster snapshots, final snapshots, point-in-time clones and
Azure Database for PostgreSQL flexible server backups all use it. The Docker
Engine refuses `exec` into a paused container, so `dbengine.Instance.Exec` runs
engine commands (password and user changes) under `sim.HoldThawed`, which
waits for the last capture to thaw and keeps new captures off until the command
ends.

Each Amazon RDS volume is `sockerless-rds-<kind>_<identifier>` for an instance,
cluster, snapshot or cluster snapshot; no identifier contains an underscore, so
no two resources share a volume. An automated snapshot's volume name carries a
dot where its identifier carries a colon. DeleteDBInstance and DeleteDBCluster
with a final snapshot keep the resource `deleting` until capture and teardown
finish; meanwhile its identifier answers `DBInstanceAlreadyExists` or
`DBClusterAlreadyExistsFault` to a create and `InvalidDBInstanceState` or
`InvalidDBClusterStateFault` to other actions. The teardown acts only while the
record carries the resource ID it started for, and a restart resumes an
unfinished capture, copy or deletion.

A deletion discards the engine rather than stopping it
(`dbengine.Instance.Discard`): an engine that has not yet accepted clients is
killed at once, since the MySQL entrypoint ignores SIGTERM while it initialises
and its directory goes with the resource. A stop keeps the grace. Every stop
ends a start waiting on the engine at once. An engine that stops before it
accepts clients is reported with its exit status and last 40 output lines, read
from the container handle, whose Wait returns the exit to every caller.

An Amazon RDS instance's log files are its engine's own output.
`dbengine.Instance` hands the container's dated lines to a sink that stores them
by hour under the instance's resource ID, so they outlive a stop, a reboot and a
restart; an adopted container replays its output and the sink drops lines no
later than its last. PostgreSQL gets `error/postgresql.log.YYYY-MM-DD-HH` per
hour, kept 4320 minutes; MySQL and MariaDB get `error/mysql-error.log`, moved
into `error/mysql-error-running.log` every five minutes, and hourly
`error/mysql-error-running.log.N` kept 24 hours. DownloadDBLogFilePortion
returns the most recent lines without a Marker and the lines after a byte-offset
marker with one, at most 10,000 lines by default and 1 MB in any case.

#### Aurora

An Aurora cluster's reader endpoint (once a replica exists) and a reader
instance's endpoint open read-only sessions in the engine itself: PostgreSQL's
startup packet carries `default_transaction_read_only=on`, and MySQL sessions
run `SET SESSION TRANSACTION READ ONLY` before the client sees its login.

An Aurora DB cluster snapshot carries the cluster volume's data.
CreateDBClusterSnapshot and the final snapshot answer `creating` and settle
`available` once `sim.CaptureVolume` has copied the volume (`failed` with the
copy's error otherwise); a final snapshot is captured before the engine stops.
CopyDBClusterSnapshot answers `copying` and clones the volume;
DeleteDBClusterSnapshot refuses one still being taken. The snapshot holds the
cluster's sealed master credential and the engine's. RestoreDBClusterFromSnapshot
and RestoreDBClusterToPointInTime record the new cluster `creating`, seed its
volume in the background and land it `available` (`incompatible-restore` when
the seed fails); every endpoint refuses clients until then. A restart resumes a
capture, copy or seed.

An Aurora cluster restores to any time in its window, kept as base backups and
the engine's own log. Each base backup is an automated DB cluster snapshot
`rds:<cluster>-<yyyy-mm-dd-hh-mm>`: the first when the engine first accepts
clients, before the endpoint relays any client, and another at the start of
every `PreferredBackupWindow` (validated as hh24:mi-hh24:mi). Aurora backs up
whether or not a client connects, so `rdsTakeFirstClusterBackup` starts the
engine in the background whenever an available cluster keeps automated backups
and holds none — after a create, any restore, StartDBCluster, a backup-settings
change and a restart. That start holds the backups' start lock, so a delete
closes the engine and waits, a manual snapshot waits rather than capture an
initialising data directory, and a master-password rotation waits for the
engine to accept clients. Automated snapshots list as `SnapshotType`
`automated` and refuse deletion. `EarliestRestorableTime` is the later of the
oldest base backup and the start of `BackupRetentionPeriod`;
`LatestRestorableTime` is now. Each backup run expires what the period no longer
covers: older automated snapshots, base backup volumes superseded by the start
of the period (unless a restore seeds from one), and the log before the oldest
remaining base backup — `PURGE BINARY LOGS TO` for MySQL, whose engine runs with
`binlog_expire_logs_seconds=0`, and for PostgreSQL every archived segment before
the REDO WAL file `pg_controldata` reports.

A capture freezes the engine, holding what a crash would leave. For MySQL the
simulator reads the capture's newest binary log file and records the offset of
the first transaction it does not hold whole, where the replay starts — true
only of crash recovery run with the binary log, which commits prepared
transactions whose XID the log holds. A server started without the binary log
rolls those back, losing a row committed between its binary log write and its
InnoDB commit. So the capture runs the engine on the snapshot volume with its
own arguments, binary log included, until it accepts connections, and shuts it
down cleanly before the snapshot turns available: a base backup is stored
recovered.

Aurora PostgreSQL runs with `archive_mode=on`, archiving every completed WAL
segment into the cluster volume; Aurora MySQL's binary log is on by default.
RestoreDBClusterToPointInTime with `RestoreToTime` refuses a time outside the
window with `InvalidRestoreFault`, seeds from the newest base backup taken by
then, and replays the source's log from its live volume. For PostgreSQL a helper
copies the archive and `pg_wal` into the new volume's archive, `pg_waldump`
lists commit and abort records, and the first start runs archive recovery with
`recovery_target_time` (or to the end of the log when no transaction ended after
the time), then promotes, with `hot_standby` off so it refuses clients with 57P03
until it accepts writes. For MySQL, whose image ships no `mysqlbinlog`, the
simulator reads the GTID events' immediate commit timestamps, cuts the binary
log before the first transaction committed after the time, and a server on the
new volume applies it as its relay log from the recorded offset with `START
REPLICA SQL_THREAD UNTIL`, writing no binary log, driven by a user only its init
file creates, which then sets the master password and is dropped.
RestoreDBClusterFromSnapshot also takes a DB snapshot ARN: an RDS for PostgreSQL
or RDS for MySQL instance's data directory becomes the cluster volume of an
Aurora cluster of the same image, under the instance's master credential and
database, which DB snapshots carry.

RestoreDBClusterFromS3 creates an Aurora MySQL cluster from a Percona
XtraBackup of MySQL 8.0. The bucket must exist and `S3IngestionRoleArn` must
trust `rds.amazonaws.com` and allow `s3:ListBucket` on the bucket and
`s3:GetObject` under `S3Prefix`, or the request is `InvalidS3BucketFault`; a 5.7
source is `InvalidParameterCombination`. The cluster is `creating` while a
background import copies the objects as that role and a
`docker.io/percona/percona-xtrabackup:8.0` helper unpacks them (xbstream, tar or
gzipped tar, whole or split into numbered parts, or plain files), runs
`xtrabackup --prepare` and copies the backup into the volume; a MySQL helper
with no network listener then creates or resets the master user with the
request's password, gives root the same password and creates the database. The
cluster lands `available`, or `migration-failed`.

#### RDS DB instances

An RDS for PostgreSQL, MySQL or MariaDB DB instance keeps automated backups
through the machinery an Aurora cluster uses (`rds_backups.go`). It takes
`BackupRetentionPeriod` (0 to 35, default 1) and `PreferredBackupWindow` from
CreateDBInstance, ModifyDBInstance and the restores, keeps PostgreSQL's WAL
archive or MySQL's binary log in its volume, takes an automated DB snapshot
`rds:<instance>-<yyyy-mm-dd-hh-mm>` at each window's start, and expires what
the period no longer covers. `rdsTakeFirstInstanceBackup` takes the first
whether or not a client connects, whenever an available instance with backup
retention holds no base backup. DeleteDBInstance, StopDBInstance and
RebootDBInstance end such a start and wait; CreateDBSnapshot and a password
change wait for it to finish.

A DB instance reports the statuses RDS reports while coming up:
CreateDBInstance and RestoreDBInstanceFromDBSnapshot answer `creating`,
StartDBInstance `starting`, and `rdsFinishInstanceBringUp` starts the engine,
turns the instance `backing-up` when the first automated snapshot's capture
begins (`recordAutomatedSnapshot`) and lands it `available` once it is taken, or
`failed`. An instance with no backup to take lands `available` at once. A
restart resumes the bring-up. Until then ModifyDBInstance, CreateDBSnapshot,
CreateDBInstanceReadReplica, PromoteReadReplica, SwitchoverReadReplica,
RebootDBInstance and StopDBInstance answer `InvalidDBInstanceState`. The AWS
CLI's waiter polls every 30 seconds, so CLI tests whose instance needs no
backups create it with `--backup-retention-period 0`. DescribeDBSnapshots
filters on `SnapshotType` and `DbiResourceId`, and DeleteDBSnapshot refuses an
automated snapshot with `InvalidDBSnapshotState`.

RestoreDBInstanceToPointInTime records the new instance `creating` and seeds it
in the background — from the source's volume for `UseLatestRestorableTime`, or
from the newest base backup with the source's log replayed for a `RestoreTime`.
A time outside the window is `InvalidRestoreFault`, a source without retention
`PointInTimeRestoreNotEnabled`, and an Aurora member is sent to the cluster
restore. RestoreDBInstanceFromS3 imports a Percona XtraBackup into an RDS for
MySQL instance through the cluster import. A failed restore lands
`incompatible-restore`, an import `failed`; a seed a previous process left
part-way starts again. DeleteDBInstance of a seeding instance with
`SkipFinalSnapshot` tears it down once the seed ends and refuses a final
snapshot. An Aurora DB instance reports its cluster's backup settings.

DescribeDBInstanceAutomatedBackups reads a live instance's automated backup
from the instance (`rds_automated_backups.go`): `creating` until its first
automated snapshot, then `active` with its restore window. With
`DeleteAutomatedBackups=false`, DeleteDBInstance and DeleteDBCluster copy the
stopped volume into `sockerless-rds-auto-backup_<resource-id>` and keep a
`retained` row with the base backups, automated snapshots and restore
properties, its window ending at the deletion. RestoreDBInstanceToPointInTime
restores one through `SourceDbiResourceId` or
`SourceDBInstanceAutomatedBackupsArn`, RestoreDBClusterToPointInTime through
`SourceDbClusterResourceId`. DeleteDBInstanceAutomatedBackup and
DeleteDBClusterAutomatedBackup delete only a retained backup. A retained backup
expires once its retention period has passed since the deletion, on a timer the
next process re-arms. The listings filter on `status`, identifier and resource
ID.

Automated backups replicate to another Region as RDS does it:
StartDBInstanceAutomatedBackupsReplication is called in the destination Region
— the Region the request is signed for, refused when it is the simulator's own
— and records a replicated automated backup (`rds_backup_replication.go`) whose
ARN `auto-backup:ab-<id>` names the destination while `Region` keeps the
source's. It copies every automated snapshot into
`sockerless-rds-replicated-snapshot_*` and the log into
`sockerless-rds-replicated-backup_<id>`, is `pending` until it holds a snapshot
and `replicating` after, and expires copies by its own retention period. Only
requests signed for its Region see it. StopDBInstanceAutomatedBackupsReplication,
or the source's deletion, takes a last copy and leaves it `retained`. An
instance restored from it is created in the simulator's own Region (BUG-3358).

An RDS endpoint owns two logins: the master user's, under the password the
control plane records, and IAM database authentication. Every other login
reaches the engine (`rds_database_users.go`, `rdsEndpointLogins`). A
MySQL-family relay logs in with the client's own user and password, so the
engine's refusal reaches the client verbatim. A PostgreSQL engine trusts the
relay, so a `DO` block run as the master user checks the password against the
role's `pg_authid` verifier — MD5, or SCRAM-SHA-256 recomputed through
PBKDF2-HMAC-SHA-256 — with user and password passed as session settings, never
spliced into SQL. A role granted `rds_iam`, directly or through another role
(the check walks `pg_auth_members`, since `pg_has_role` counts every role as
granted to a superuser), signs in only with an IAM token whose `DBUser` names
it. Every engine start gives PostgreSQL the `rds_iam` role and a MySQL-family
master user the global privileges RDS grants, `CREATE USER` among them, and
removes the image's remote `root`.

On RDS for MySQL, RDS for MariaDB and Aurora MySQL an IAM token signs in the
user its `DBUser` names only when that user was created `IDENTIFIED WITH
AWSAuthenticationPlugin AS 'RDS'`. The stock images lack that plugin, so the
simulator ships a real server plugin (`simulator-aws/rds_auth_plugin/`): C with
no C library dependency, built by `build.sh` with clang and ld.lld per engine
ABI on amd64 and arm64 and embedded in the binary, since `go install` cannot
compile it. The engine loads plugins from `.sockerless-plugin` in its data
volume (`--plugin-dir`, plus `--ignore-db-dirs` on MariaDB), so snapshots carry
it; every start writes the build for the container's architecture there by
rename and installs the plugin unless loaded. The endpoint validates the token
and relays it as the password, which the engine admits only for a user
identified with the plugin; a password login of such a user never reaches the
engine. `scripts/check-rds-auth-plugin-build.sh` in `build-gates` rebuilds the
shared objects and fails when a committed binary differs; the build links with
no build ID and strips symbols, so it is reproducible.

A blue/green deployment of a DB instance is a real green environment.
CreateBlueGreenDeployment answers PROVISIONING and records a green instance
`<blue>-green-<six letters>` as a read replica of the blue one with the target
engine version, class, storage and DB parameter group, seeded from a capture of
the blue volume; its coming up settles the deployment AVAILABLE, or
INVALID_CONFIGURATION, and it serves sessions read-only. DescribeDBInstances
reports a named DB parameter group and AutoMinorVersionUpgrade (true unless
turned off), and a missing group is refused. SwitchoverBlueGreenDeployment
answers SWITCHOVER_IN_PROGRESS and, within SwitchoverTimeout, stops both
engines, copies the blue volume to the `-old1` identifier's, and moves the green
record onto the blue identifier, ARN and endpoint with the blue master
credential and a fresh first automated backup; Source then names the `-old1`
instance, which Terraform's `blue_green_update` deletes. The blue instance's
read replicas move with it to `-old1`. A failed or timed-out switchover rolls
back as SWITCHOVER_FAILED, and one a previous process left runs again.
DeleteBlueGreenDeployment refuses DeleteTarget after a switchover, deletes the
green instance with it before, and otherwise leaves it standalone and writable.
A Multi-AZ or Aurora DB cluster source answers SourceClusterNotSupportedFault; a
source without automated backups, a read replica or a cluster member
SourceDatabaseNotSupportedFault.

Every engine version runs that release's image. `rdsEngineVersions` lists
PostgreSQL 17.11, 16.15 and 16.14, MySQL 8.0.46 and MariaDB 11.4.13 from the ECR
Public Gallery's Docker official images, each with its DB parameter group
family, so `SELECT version()` answers the EngineVersion DescribeDBInstances
reports. DescribeDBEngineVersions lists exactly these with the upgrades the
simulator performs. Any other version is InvalidParameterCombination ("Cannot
find version 15.4 for postgres"), a major alone resolves to its newest, and a
downgrade is refused. ModifyDBInstance with a newer version answers `upgrading`
and starts the new release on the same volume; a PostgreSQL major upgrade needs
`pg_upgrade` with both releases' binaries, which no official image carries, so it
is refused (BUG-3398). Aurora clusters run the one release their row names. The
default PostgreSQL version is 16.15. Every image is a literal in the source, so
`base-images-for.sh` warms each in CI.

An instance created without a DB parameter group is associated with its
family's `default.<family>`, which ModifyDBParameterGroup, ResetDBParameterGroup
and DeleteDBParameterGroup refuse with InvalidParameterValue; a group of another
family is InvalidParameterCombination. DB cluster parameter groups default the
same way (`default.aurora-postgresql16`). Each family's parameters are its
engine's own: `scripts/capture-rds-parameter-catalogs.go` starts each family's
newest image with the simulator's arguments and reads `pg_settings`,
`information_schema.SYSTEM_VARIABLES` on MariaDB, or on MySQL 8.0 the
Performance Schema variable tables, `mysqld --verbose --help` and a
`SET GLOBAL v = @@GLOBAL.v` probe, into `rds_parameter_catalogs_vendored.json`;
`TestRDSParameterCatalogImages` fails when a catalog's image drifts from
`rdsEngineVersions`. A restart-applied setting is `static`, any other
`dynamic`; a parameter is modifiable when the server takes it as an option,
except the settings the simulator runs every engine with. ModifyDBParameterGroup
refuses an unknown or unmodifiable parameter or a value outside AllowedValues
(InvalidParameterValue), and `immediate` for a static parameter
(InvalidParameterCombination). A MySQL-family engine takes its group's
parameters as command-line options; PostgreSQL takes static ones as `-c` and
dynamic ones from `rds_parameters.conf`, because a reload never overrides a
command-line setting. A dynamic change applied immediately reaches running
engines (`SET GLOBAL`, or a rewritten file and `pg_reload_conf()` waited on).
While the parameters an engine started with differ from its group's,
ParameterApplyStatus is `pending-reboot`, and any engine restart applies the
group as it is then.

ModifyDBInstance follows ApplyImmediately: without it, class, storage, engine
version, backups on or off, MultiAZ, StorageType, Iops, StorageThroughput,
AllocatedStorage, DBSubnetGroupName, OptionGroupName, CACertificateIdentifier and
NewDBInstanceIdentifier wait in PendingModifiedValues until a `sim/bg` timer for
the next PreferredMaintenanceWindow (or at once when the window is open with 30
minutes left); a later ApplyImmediately request applies them with its own. A
pending option group shows `pending-maintenance-apply` beside the current
group's `pending-maintenance-removal`. Every instance has a maintenance window:
the request's, checked for format, a 30-minute minimum and overlap with the
backup window, or a random 30-minute window in the Region's 8-hour block. The
storage members follow the reference's rules over the storage pending changes
leave: io1 and io2 need Iops, gp2 and standard refuse it, StorageThroughput is
gp3's alone, IOPS stay within 0.5 to 50 times the storage (1 to 50 on SQL
Server), an IOPS increase needs AllocatedStorage, and an increase under a tenth
rounds up to a tenth on the engines the reference names. An instance gets gp3
(io1 with Iops), its version's `default:<engine>-<major>` option group, and the
account's CA for new launches unless named. DBPortNumber applies at once,
restarting the engine on the new port (`rdsRestartModifiedInstance`). A rename
moves the record as it applies, then the volume (`sim.SnapshotVolume`), and
read replicas, blue/green deployments and custom cluster endpoints follow the
name while DbiResourceId stays. A CA change restarts the engine unless
CertificateRotationRestart is false, and the endpoint presents a server
certificate the instance's CA signs; each CA DescribeCertificates lists is a
real CA with a KMS-sealed key whose thumbprint is the SHA-1 of its certificate.

A read replica is a real replica. CreateDBInstanceReadReplica refuses a source
without automated backups or in a DB cluster, answers `creating` and readies the
source's engine: the root of the chain creates the `rdsrepladmin` user (password
sealed), a PostgreSQL source admits its replication connections and keeps a
physical replication slot per replica, and the source joins
`sockerless-rds-replication-<source DbiResourceId>`, answering as `source`. The
replica's volume is a frozen capture of the source's; a MySQL-family replica
drops the server UUID, replicates from the end of the last whole transaction in
the newest binary log, and runs with its own server ID and `read_only`; a
PostgreSQL replica drops the copied slots and starts as a hot standby with
`primary_conninfo` and `primary_slot_name`. It lands `available` once its engine
reports replication running, serves sessions read-only, and reports the `read
replication` status and the `AWS/RDS` ReplicaLag metric each minute from the
engine's own status (read through the Docker Engine API's exec by
`rdsEngineOutput`). It takes the source's master password and follows changes
to it. A source with read replicas, and a replica, cannot be stopped.
PromoteReadReplica answers `modifying`, ends replication (`RESET REPLICA ALL`
and `read_only` off, or `pg_promote`), drops the slot and reopens writable;
deleting a source promotes its replicas. SwitchoverReadReplica, an RDS for
Oracle operation, refuses the engines the simulator runs; engines without a
data plane keep record-only replicas.

A Multi-AZ DB instance runs a real standby on
`sockerless-rds-standby_<DbiResourceId>`, seeded through `rdsSeedReplicaVolume`
(slot `<resource-id>-standby`), labelled `sockerless-rds-standby=<DbiResourceId>`
so a rename keeps it. Replication is synchronous: the PostgreSQL primary lists
`rds_multi_az_standby` in `synchronous_standby_names` once the standby streams,
and MySQL and MariaDB run semisynchronous replication. The PostgreSQL standby
runs `archive_mode=always` and the MariaDB standby `log_slave_updates`, so a
promoted standby's log serves restores. Every PostgreSQL primary start resets
`synchronous_standby_names`, since the data directory carries it into copies.
CreateDBInstance with MultiAZ lands `available` once the standby replicates;
ModifyDBInstance conversions hold the instance `modifying`; stop, start,
upgrade and delete carry the standby along, and a standby that stops
replicating is rebuilt. SecondaryAvailabilityZone is another of the `a`/`b`/`c`
zones. RebootDBInstance with ForceFailover refuses a Single-AZ instance with
InvalidDBInstanceState, answers `rebooting`, promotes the standby, moves its
volume onto the instance's, swaps the zones and starts the endpoint's engine on
it; a new standby is built in the old primary's zone, and an interrupted
failover resumes. DescribeEvents reads recorded events (`rds_events`, kept 14
days) over Duration, StartTime, EndTime and EventCategories: conversions record
RDS-EVENT-0024/0025 and 0030/0029, failovers RDS-EVENT-0013/0015.

RDS for MariaDB keeps automated backups and restores to a time as RDS for MySQL
does, running with `--log-bin=binlog`. Its binary log differs: a GTID event
(type 162) opens a transaction without a BEGIN, a standalone one wraps a single
DDL or non-transactional statement, a transaction ends at its XID event or a
`COMMIT` query, and the GTID event dates it in whole seconds, so a restore
replays transactions dated before the restore time's second, as
`mariadb-binlog --stop-datetime` does. The replay start and end stop before a
transaction the log holds only in part. MariaDB's applier runs `CHANGE MASTER TO
... MASTER_USE_GTID=no` and `START SLAVE SQL_THREAD UNTIL`, and the replay ends
once the applier has executed up to the cut or moved past the copied logs.

### Memorystore for Redis

Memorystore for Redis instances and Memorystore for Redis Cluster clusters run a
real Redis engine on the image `redisVersion` names (clusters run 7.2): one
container per node on port 6379, on a Docker network every Memorystore engine
shares, with node directories on one volume per resource. Volume, network and
alias names hash the resource name with the simulator's workload scope, so two
simulators never share them. A create settles only once every node answers and,
for a cluster, slots are assigned and every replica has synchronised; a delete
stops the nodes in parallel and removes the volume. The reported `host`,
`readEndpoint` and discovery endpoint are per-resource loopback relays; cluster
nodes announce their loopback endpoint as hostname
(`cluster-preferred-endpoint-type hostname`), so a client follows redirections
it can reach. AUTH is the engine's `requirepass`, whose value `getAuthString`
returns, and an `authEnabled` update changes it live. A failover promotes a
replica and moves the primary endpoint; a Basic Tier failover is
FAILED_PRECONDITION. `LIMITED_DATA_LOSS` (the default) first reads the primary's
`INFO replication` and fails with FAILED_PRECONDITION when the replica trails
`master_repl_offset` by 30 MB or more or is not replicating; `FORCE_DATA_LOSS`
promotes regardless. The Go REST client sends `dataProtectionMode` as the enum's
number, which the simulator accepts beside its name. `replicaCount` and
`readReplicasMode` updates reshape the running engine; a cluster `shardCount`
update moves slots with Redis Cluster's own commands — `CLUSTER SETSLOT
IMPORTING`/`MIGRATING`, `MIGRATE`, `CLUSTER SETSLOT NODE` — pipelined 512 at a
time, or `del-node`s removed shards. A cluster from `gcsSource` or
`managedBackupSource` loads each RDB into a standalone server in one node's
container and moves keys with `redis-cli --cluster import`.

Settling waits on the engine, never on a simulator timer: every node sends
`CLUSTER MEET` to every other before the topology is read, since a node learns
a peer's role only from a packet the peer sends; engines start with
`repl-diskless-sync-delay 0`; what remains is Redis's own two-second hold of a
new master in `cluster_state:fail`.

`SERVER_AUTHENTICATION` transit encryption serves TLS from the relay (port 6378
for an instance, 6379 for a cluster) with no plaintext listener. Each resource
has its own server CA (a `SERVER_CA_MODE_GOOGLE_MANAGED_SHARED_CA` cluster uses
the region's), reported in `serverCaCerts` or `getCertificateAuthority`, and
every endpoint presents a certificate it signs for the dialed address. An
`AUTH_MODE_IAM_AUTH` cluster's engine runs behind a credential only the
simulator holds; the relay exchanges an AUTH (or HELLO with AUTH) password that
is an access token for a principal holding `redis.clusters.connect` for that
credential, and forwards any other unchanged. `AUTH_MODE_TOKEN_AUTH` runs each
token-auth user as an engine ACL user whose passwords are its active tokens. A
cluster's `aclPolicy` runs each rule as `ACL SETUSER <username> reset <rule>` on
every node and removes other users but `default`, at create, attach, swap,
detach, a rule revision and on nodes a reshape adds; `aclPolicyInfo`,
`clusterAclPolicyAttachments` and `attachedClusters` report the running
revision. A policy refuses deletion while a cluster runs it, and a `default`
rule, duplicate username or unbalanced selector is INVALID_ARGUMENT. On an IAM
cluster, a principal whose email is a rule's username connects as that policy
user. A cluster with deletion protection refuses its delete.

`persistenceConfig` is honoured: RDB snapshots are BGSAVEs on the reported
schedule (`rdbSnapshotStartTime` plus whole `rdbSnapshotPeriod`s, reported as
`rdbNextSnapshotTime`), the first taken at once when no start time is given. A
cluster's AOF mode runs `appendonly yes` with the configured `appendfsync`. A
node keeps its persistence files and `nodes.conf` across restarts. Export, import,
upgrade and cluster backups move the engine's own RDB files through Cloud
Storage. An API-only simulator runs no engine and its instances report no host.

### Virtual machines and the network fabric

Firecracker boots Compute Engine, Amazon EC2 and Azure virtual machines where
the host kernel allows it, over the default virtio-MMIO transport (the PCI
transport never delivers the first virtio-blk completion on aarch64). A
machine's disk outlives the guest process, so a stopped machine can be
generalized and captured, a deallocated one keeps its disk and a deleted one
discards it. Stopping or terminating a pending Amazon EC2 instance cancels its
boot. Every SDK test that launches instances terminates them when it ends.

Compute Engine `instances.start`, `stop`, `suspend`, `resume`, `reset`,
`startWithEncryptionKey` and `delete` answer at once with a RUNNING zone
operation, move through STAGING, STOPPING or SUSPENDING, and do the work in the
background. `reset` halts and reboots the guest while the instance reads
RUNNING; a failed reboot leaves it TERMINATED. `delete` refuses an instance with
`deletionProtection`. The operations' `wait` methods block on the signal
`computeOpFinish` raises, bounded by the documented two minutes, and
`zoneOperations.delete` and its siblings wake waiters. An Azure VM DELETE
answers 202 with long-running-operation headers and `SimulateEviction` 204,
both stopping the guest in the background; the machine leaving clears
`properties.virtualMachine` on every interface it had attached.

The fabric keeps a network's or machine's lock only while someone holds or
waits for it. Interface namespace and veth names derive from the interface ID,
so attaching deletes a leftover namespace of its name, and its veth, and retries
once; attaches of one interface are serialized so the reclaim never destroys a
live namespace. The Azure simulator closes its fabric on SIGTERM. An Azure
network interface realizes every IP configuration: the primary attaches the
interface and each secondary leases its own address of the same subnet onto it
(`ip addr add` in the namespace, or a lease the tap holds while a VM carries the
interface). A rewrite keeps kept configurations' addresses and releases dropped
ones first; configurations in two subnets are
`IpConfigurationsOnSameNicCannotUseDifferentSubnets`, and an address another
interface holds `PrivateIPAddressInUse`. Secondary addresses are not configured
inside a guest, since Azure's DHCP hands a guest only its primary; the Instance
Metadata Service lists them all.

Creating an Azure file share or a Cloud Storage bucket makes its empty host
directory or fails the request, and a bucket reuses no files a deleted bucket of
the same name left. Removing a Docker network the engine refuses because a
container is attached waits for the disconnect event and retries. Deleting a
Cloud DNS managed zone succeeds whatever still resolves through it; its Docker
network goes in the background once the last container disconnects.

### Scheduler, Auto Scaling and STS

Every Amazon EventBridge Scheduler target call runs as the schedule's execution
role, which must trust `scheduler.amazonaws.com` and allow the call; a denied
call fails with `AccessDeniedException` and reaches the dead-letter queue. An
EventBridge target goes through the bus's own PutEvents path. A universal target
calls an awsJson or awsQuery action through the Step Functions AWS SDK
dispatcher, checking every action and resource against the role. A call is
authorized against the request it builds, so an Amazon ECS RunTask target
reports its `ecs:cluster`, for Scheduler and EventBridge rules alike. Create and
update reject values outside the model's ranges, and ListSchedules and
ListScheduleGroups page. The IAM gate reads an Amazon SQS request's queue from a
JSON body as well as from query parameters.

An EC2 Auto Scaling instance refresh runs in the background, replacing instances
in batches that keep MinHealthyPercentage in service (or launching ahead under
MaxHealthyPercentage), waiting out each batch's instance warmup (the preference,
else DefaultInstanceWarmup, else the health-check grace period), honouring
checkpoints, bake time, SkipMatching and the Standby and scale-in-protected
preferences, and moving the group onto its desired launch template on success.
CancelInstanceRefresh stops one; RollbackInstanceRefresh reverses only one still
under way. Groups take a launch template, and members report the version they
launched from.

GetCallerIdentity reports the signing identity's unique ID: a user's ID,
`<role ID>:<session name>` for an assumed role, `<account>:<name>` for a
federated user. A credential resolving to no identity fails with
`InvalidClientTokenId` in GetCallerIdentity, GetWebIdentityToken and
GetDelegatedAccessToken, and `InvalidAccessKeyId` in an Amazon S3 Express One
Zone CreateSession. GetDelegatedAccessToken's credential acts as its caller.

### Google Cloud media, Discovery and identity

Artifact Registry stores uploaded files' bytes and serves them from
`files.download`. Generic, Go module, KFP, Apt, Yum and GooGet uploads create the
package, version and file from each format's metadata (a .deb control file, an
RPM header, a `.pkgspec`, a module zip); `:import` publishes Cloud Storage
objects the same way, reporting bad objects in `errors`. `exportArtifact`
resolves a version through recorded package versions, and deleting a version,
package or repository deletes its files. A Docker push records a File per
manifest and referenced blob, named by digest; a shared layer belongs to the
first image to push it and passes on deletion. `files.delete` refuses non-generic
repositories, and `registryUri` follows gcloud's
`LOCATION-FORMAT.pkg.dev/PROJECT/REPOSITORY`.

Every resumable media path Discovery declares is served, and the conformance
loader indexes `mediaUpload.protocols.resumable.path` beside the simple path. A
session begins with `uploadType=resumable` on `/upload` (the Go client,
googleapiclient) or `/resumable/upload` (apitools), answers 200 with the session
URI in `Location`, and takes chunks under `Content-Range`, answering 308 with a
`Range` — 200 with `X-Http-Status-Code-Override: 308` for a client sending
`X-GUploader-No-308` — and the method's response on the last byte; `bytes */*`
reports progress and DELETE cancels with 499. Sessions live in the store.
`media_upload.go` serves Artifact Registry and BigQuery; Cloud Storage keeps its
own sessions, staging bytes in the payload store, through the same helpers:
`objects.insert` sessions on `/resumable/upload/storage/v1/b/{bucket}/o` echo
the path and query they began on, refuse a chunk that would leave a gap, and
answer later requests with the object's metadata. The vendor CLIs switch to the
resumable protocol past 5 MiB, so `gcloud storage cp`, `gcloud artifacts generic
upload`, `gcloud artifacts files upload` and `bq load` each have a test with a
9 MiB payload.

BigQuery load, copy and extract jobs run as real jobs: `jobs.insert` records
PENDING and answers at once, and a goroutine moves the job to RUNNING, does the
work and settles it DONE with statistics or an `errorResult`. A job writes its
destination under the table's lock as one update, `jobs.cancel` and
`jobTimeoutMs` stop it before it writes, a job ID cannot be reused, and a
restart settles unfinished jobs. A load reads `sourceUris` from the Cloud
Storage store (one `*` per URI), inflates gzip, and parses CSV with its own
splitter (`quote`, `allowQuotedNewlines`, `encoding`, `nullMarkers`,
`skipLeadingRows`, `fieldDelimiter`, `allowJaggedRows`) or newline-delimited JSON
(`ignoreUnknownValues`), honouring `createDisposition`, `writeDisposition` and
`maxBadRecords`; every cell converts to its column type, and a value the type
cannot hold is a bad record. `autodetect` detects CSV and JSON schemas; AVRO
loads use `hamba/avro` and PARQUET `parquet-go`. A copy writes one or more tables
sharing a schema; an extract writes CSV, JSON, Avro or Parquet, compressed and
sharded through a wildcard URI. `numBytes` uses BigQuery's logical size per type.
Tests wait through `Job.Wait`. `bq` builds its client from BigQuery's Discovery
document whenever its API root is not Google's, and BigQuery's REST errors carry
the `errors[]` entry whose `notFound` reason `bq mk` reads.

Every implemented API serves its own Discovery document at
`GET /$discovery/rest?version=…` under its host, byte-identical to the vendored
one (`simulator-gcp/discovery/`, copied by `scripts/fetch-gcp-discovery.sh`),
indexed by `name` and version; regional and mTLS hosts reach the same document
through `gcpServiceFromHost`. A bare address:port serves a version only one
implemented API publishes, `v2` stays BigQuery's for `bq`, and `v1` answers 404.
The Discovery service's directory is a capture of the real one
(`simulator-gcp/discovery_directory_vendored.json`): the entries for the embedded
documents verbatim, the ids each of `www.googleapis.com` and
`discovery.googleapis.com` serves through `apis/{api}/{version}/rest` (neither
serves every listed document), and each API's default version. Documents and the
directory answer anonymously, as Google's do.
`specs/cloud-api/gcp/discovery-v1.supplement.json` declares the `mtlsRootUrl` and
`serviceVersion` the `RestDescription` schema predates.

Eventarc and Cloud Build both publish `/v1/projects/{p}/locations/{l}/triggers`,
and the host tells them apart; the harnesses reach each through its own host
with an HTTP proxy that delivers to the simulator — gcloud through `HTTP_PROXY`,
the Go SDK through its client's proxy. At a bare address a create resolves by
Eventarc's required `triggerId` and a read by which service holds the trigger.

A project has its Compute Engine default service account in IAM from the moment
Cloud Resource Manager creates it (every API is enabled on a new project here),
created once, so a deleted one stays deleted; its email names the project number,
so the `-` wildcard resolves it.

The GCE metadata server answers for the workload that asks: a Compute Engine
instance by its private address in the real-execution index, a Cloud Run
container by its network namespace's address, read from the engine's container
list and mapped through labels to its service, instance, job execution or worker
pool. The project ID comes from the resource, the number from Cloud Resource
Manager, and `default` is the account the resource runs as or the project's
Compute Engine default service account. A key the workload does not hold answers
404. A caller outside every workload is answered for the default project,
`sockerless`.

## Storage

An object store's conditional write is one step. A client that keeps its
consistency in an object store — a git server arbitrating a branch update, a
lock file — relies on exactly one of two conditional writers winning. The
evaluation and the store sit under one per-object lock (`sim.KeyedLocks`) at
each service's single write path, so a handler added later cannot skip it,
and unrelated writes stay concurrent. Cloud Storage generations are timestamps
that never repeat.

An object store is read by key or key prefix. The three object stores are
`sim.PrefixStore`s, which have no `List` or `Filter`: a listing is a range on
the primary key (`bucket/key`, `account/container/name`) that decodes only the
rows under the prefix. The store-scan gate counts scans per request, not bytes
per scan, so the type is what keeps a whole-store read off object stores. The
in-memory and cached stores keep ids sorted on every write, so `ListPrefix` is
a binary search and the run after it. DynamoDB reads a query's partition, or a
scan's table, as such a range; Amazon ECR's DescribeImages reads the
`<repository>:` range and Amazon ECS's task-definition listings the family's
range from a cached store. Deleting an Amazon ECR repository deletes its images
and manifests under `<repository>:`, its blobs and layers under `<repository>@`,
and its policies, so a repository created again starts empty.

An object's contents are not in its row, since a row is decoded whole on every
read: `Server.Payloads` keeps contents in files the row references. A write is
always a new file, written before the row that names it, and the replaced file
is released after, so a crash leaves at worst an unreferenced file the startup
sweep removes. A file belongs to exactly one row; a copy or snapshot writes its
own.

An Amazon S3 key's current object stays in `s3Objects`, so every listing,
conditional write and service reading an object sees the latest version; every
other version and delete marker lives in `s3ObjectVersions` under
`<bucket>/<key>\x00<version id>`, the null version under `null`. A version
records a sequence number from a clock that never repeats, ordering
ListObjectVersions newest first and choosing the version a removal makes
current. A version's tag set in `s3ObjectTags` moves with it. The single write
path (`s3StoreObject`) applies the bucket's versioning state, so every writer
versions alike, and the payload adoption at start keeps the contents every
noncurrent version references.

Amazon S3 Object Lock lives on the object version. Enablement is the bucket's
stored `object-lock` configuration, written at CreateBucket with an enabled
versioning configuration; PutBucketVersioning refuses to suspend it. The single
write path applies the bucket's default retention to a version whose write
asked for none. Protection is checked under the key's write lock in the version
delete, so DeleteObject and each DeleteObjects entry are refused alike, and a
delete without a version id only adds a delete marker, which Object Lock never
refuses. A PutObject carrying Object Lock headers needs Content-MD5 or a
checksum (the AWS SDK for Go v2 sends one only when its checksum calculation is
`when_supported` or the input names an algorithm). s3:BypassGovernanceRetention
authorizes against the object, per DeleteObjects entry.

The S3 Smithy supplement declares GetObject's 206 Partial Content beside its
200, since a ranged read — the Terraform `aws_s3_object` data source's —
answers 206.

An AWS owned KMS key (RDS, Amplify, CodeBuild, Firehose) gets its material on
first use through `kmsEnsureKeyMaterial`, which checks and generates under the
store's lock, so two concurrent first uses cannot seal under different
material.

An Elastic Load Balancing trust store reads its CA bundle and revocation lists
from Amazon S3 at the call, through S3's own object and version lookup, and
keeps the bytes it read. Its content locations are presigned URLs to that copy
in a bucket Elastic Load Balancing writes, keyed by the content's digest, served
on the endpoint the client called.

Amazon EC2 instance-type facts come from the AWS Price List bulk offer file, the
one machine-readable, unauthenticated publication of them; the EC2 Smithy model
carries only the InstanceType enum. `scripts/fetch-aws-ec2-instance-types.go`
streams the us-east-1 offer, keeps the Compute Instance products, fails when two
products of one type disagree, and records the pinned offer URL and SHA-256.
"<n> GiB" truncates to MiB, matching DescribeInstanceTypes for the legacy sizes
the Price List rounds; the processor names the instruction set, since the offer
says "64-bit" for both x86 and Arm. A fact the offer does not state stays absent,
and a filter over one answers Unsupported.

Amazon ECR's imageSizeInBytes counts the config blob with the layers and leaves
the manifest document out, as the AWS CLI's own describe-images and
batch-get-image examples settle (48318255 = four layers of 48315478 + a
2777-byte config).

A Cloud Storage bucket belongs to a project that exists. The insert resolves
`project` through Cloud Resource Manager and stamps its number, and the service
agent is named for the same number, because gcloud's `storage buckets
notifications create` reads the bucket's `projectNumber`, grants that number's
agent publish on the topic, and the insert checks it. gcloud and Terraform name
the topic by its relative name, which the insert stores as the full
`//pubsub.googleapis.com/` name.

A project has one number, the one Cloud Resource Manager assigned. Cloud DNS,
Cloud Build, Cloud Run's service agent, Compute Engine, BigQuery and Cloud
Logging resolve the project through `crmProjectNumber` and name their identities
for that number. A project that does not exist or is pending deletion is refused
with each service's own error: 403 `PERMISSION_DENIED` for Cloud DNS, Cloud
Build and Cloud Logging, 404 for Compute Engine and BigQuery. Compute Engine
addresses a project by ID or number and keys it by ID. Tests create their
projects in Cloud Resource Manager first.

### Cloud Run instances, worker pools and restarts

A Cloud Run service instance starts its containers in
`cloudRunContainerStartOrder`: the first owns the network namespace and
publishes the ingress port, and the others start in the background once their
dependencies have started and passed their startup probes. The ingress
container always runs a startup probe (Cloud Run's default TCP probe when none
is configured), and the request that started the instance waits for every
probe. The Knative service surface folds `dependsOn` into the revision
template's container-dependencies annotation and back.

A worker pool runs its manual instance count (the minimum under automatic
scaling), each instance a container group started by the job task's code
(`startCloudRunContainerGroup`): `dependsOn` order, startup probes, Cloud Storage
volume binds and write ingestion, and Cloud Run's ten-second stop grace. A
changed template replaces every instance, a changed count starts or stops the
difference, and an update or delete answers only once retired instances have
stopped and their volume writes are objects. A Cloud Run instance runs through
the service-instance path (`ensureCloudRunServiceInstance`) from creation,
`instances.start` or an update until `instances.stop` or deletion. Worker pools
log under `cloud_run_worker_pool` and instances under `cloud_run_instance`.
gcloud's worker-pool `deploy`, `update` and `delete` speak Cloud Run v2 over
gRPC, which the simulator does not serve (BUG-3341), so the CLI suite deploys
over REST. The Go REST client sends `instanceSplitStatuses[].type` as the enum's
number, which the simulator maps to its name.

A worker pool's or instance's create, update and `instances.start` answer with
an operation that is not done, and the resource reports `reconciling` and a
`CONDITION_RECONCILING` `Ready` condition until every instance has passed its
startup probes. The operation then completes with the resource and the pool's
created revision becomes its ready revision; an instance that cannot start fails
the operation with INTERNAL and the `Ready` condition with the same message, and
the pool keeps its last ready revision. A pool whose scaling runs no instance
settles before the call answers. Cancelling the operation stops the instances it
was starting and fails `Ready` with `Cancelled`; deleting the resource aborts it.

A Cloud Run instance whose container exits restarts as `restartPolicy` says:
ON_FAILURE (default) after a non-zero exit, ALWAYS after any exit except a clean
one of a multi-container instance, NEVER not at all. Cloud Run restarts a failing
instance "up to 3 times sequentially" with no published delay, so the simulator
restarts at once and fails the fourth exit. The record settles before the exit
is logged, so a reader waiting on the log line finds the outcome recorded.

A simulator restarted on its state directory keeps every stored worker pool's
and every non-stopped, non-failed instance's workloads running, as Cloud Run's
control plane never restarts what it runs. A persistent simulator leaves them
running when it stops, and the next one adopts them (`cloudrun_adopt.go`):
every container carries its instance's group label and a digest of its
template, so adoption finds whole instances of the current template whose
containers all run, attaches to them (`sim.AdoptContainer`, plus the
namespace owner's log stream and ingress route), registers them, and removes
every other container left for the resource; then only what is missing starts.
For every stored service it adopts the instance running the current template
and removes other revisions' and deleted services' containers. A job execution
still running keeps its task's containers: the restarted simulator adopts them,
arms what is left of the task's timeout from the execution's start, and settles
from the main container's exit; only an execution whose containers are gone
fails. Adoption precedes the recovery that fails executions without a workload,
so both run at start-up. An adopted container's log stream replays its whole
output, and the sink drops the lines up to the resource's newest Cloud Logging
entry. Stopping an instance stops its containers before ending the owner's log
stream and waits for it to drain, so stop-time output reaches Cloud Logging.

A Cloud Run instance's `urls` are served by the Cloud Run front end as a
service's URL is, behind the same invoker check (`run.routes.invoke` through
the instance's own or an inherited policy, or `invokerIamDisabled`), with an ID
token naming one of the instance's URLs. The Terraform provider has no Cloud
Run instance resource.

### Cloud Storage volume mounts

A Cloud Run workload's Cloud Storage volume writes back as Cloud Storage FUSE
does, without a FUSE file system. While a workload mounts a bucket writable, one
inotify instance watches every directory of it, and events map onto Cloud
Storage FUSE's object operations: a close after writing (`IN_CLOSE_WRITE` after
`IN_MODIFY` or `IN_CREATE`) is a new generation, never each write; a `mkdir` is
a placeholder object; an unlink, an `rmdir` or a rename out of the mount is a
delete; a rename within it is a copy and a delete. Each write is conditioned on
the generation the file held, keeps the object's metadata, takes a new file's
content type from its extension and records `gcsfuse_mtime`. The simulator tells
the workload's changes from its own by state, not by event: it keeps a view of
what each path stands for (generation and inode), every change it makes and its
record happen under one mutex, and its mirror of an API write is staged outside
the bucket directories and renamed in, so a watch event only prompts a
comparison with the view. `IN_EXCL_UNLINK` drops the close of a file an API
write already replaced. Ingestion is asynchronous to the workload's close, so a
barrier — closing a file of the simulator's own under the data root and waiting
for that close to arrive through the same queue — makes every Cloud Storage
JSON API request, and the end of a job task, wait for the events queued before
it: the read-your-writes Cloud Storage FUSE gives by writing before `close`
returns. Files open with `RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS`, so a link a
workload makes never reads a host file into a bucket. `only-dir` binds that
directory of the bucket; the Knative v1 surface carries the options in the CSI
`mountOptions` attribute; a volume of a missing bucket fails the workload's
start. Linux only (BUG-3084).

### Derived state

A future-dated Capacity Reservation becomes active on its start date with nobody
calling anything. Storing that transition would need a timer or a sweep that
could be late, so the record holds what was asked for and the one accessor every
read goes through derives the state as of now.

An Azure cross-resource-group move repoints inbound references by walking every
store a build creates (`sim.TrackedStores`), rewriting keys beneath the moved
identifier and any string naming it at a resource-identifier boundary; a
hand-maintained list would rot silently. Each moved family's credentials are
pinned across the move, and Azure Container Registry content, scoped by the
registry's resource identifier, re-keys in the same pass.

## Authorization and authentication

Every credential is verified: AWS requests by SigV4 against the principal's
stored secret, from the header and from a presigned URL alike; Google Cloud and
Microsoft Entra bearers against the simulator's own signing keys; the Azure
Storage data plane's Shared Key and shared access signatures over the layouts
Microsoft's own signers produce, each version's layout pinned by those signers;
Cosmos DB's shared key through a middleware on every data-plane path, so a new
route cannot skip it; Event Grid's key and SAS against its published format; and
each container registry against the credentials its control plane mints, with
its own challenge shape. Authorization reads at most 16 MiB of a request body
and streams the rest to the handler.

### AWS IAM

IAM enforcement derives the resource an action authorizes against from the
types AWS declares and the ARN format published beside each; operations that
name no resource authorize against `"*"`, as AWS does, and a create authorizes
against its type's wildcard. A resource-policy statement naming the caller
grants; one matching only by account delegates to that account's IAM, which is
what the default AWS KMS key policy means. The Amazon S3 control plane runs the
same gate route by route, and a test crosses every route against the reference.

An ARN format's identifiers end at the next `/`, except those AWS documents as
names that may be paths — `SecretId`, `ParameterNameWithoutLeadingSlash`,
`EntityPath` and every `…WithPath` — which end only at a `:`, so a grant scoped
to a secret path prefix matches a create under it.

A create that carries tags is authorized twice, as AWS does: as itself and as
its service's tagging action, with `<service>:CreateAction` naming the create on
the second check. `iamTagOnCreateActions` is generated: for every operation whose
Service Reference entry authorizes an action annotated `IsTaggingOnly` beside one
that is not, those tagging actions — 297 operations across 31 services, Amazon
S3 excepted because it reads tagging from headers and control-plane documents.
Tags are read in the shape each protocol sends them, Smithy RPC v2 CBOR
included. The tagging check authorizes against the resource being created — for
Amazon EC2 the wildcard of each tag specification's resource type — and reports
no `aws:ResourceTag`. Where the create authorizes against a parent, the check
names the type it mints under that parent with a wildcard identifier
(`iamMintedResourceARNs`): CreateListener's `listener/app/<lb>/<id>/*`,
CreateRule's `listener-rule/…/*`, RunTask and StartTask's `task/<cluster>/*`. AWS
Lambda's CreateFunction authorizes and tags the `function:<name>` its body names,
and the CloudWatch CBOR routes derive resources from the CBOR body as the JSON
routes do, so PutMetricAlarm authorizes `alarm:<name>`. Session tags on an
`AssumeRole` also need the trust policy to allow `sts:TagSession`.

An AWS STS session keeps its tags on its temporary credential, with the
transitive keys. `aws:PrincipalTag/<key>` reads the session's tags over its
role's, a session tag replacing a role tag of the same key case-insensitively.
An AssumeRole a session signs keeps that session's transitive tags, refuses a
passed tag that would replace one, and needs sts:TagSession only for the tags
the request passes. A SAML assertion's `PrincipalTag:<key>` attributes and a web
identity token's `https://aws.amazon.com/tags` claim are session tags too,
authorized as sts:TagSession with the federation context, so a statement can
condition tagging on `saml:aud` and `aws:RequestTag/<key>`.

A DeleteObjects entry is authorized as its own s3:DeleteObject or
s3:DeleteObjectVersion. A refused entry is that entry's AccessDenied `<Error>` in
the DeleteResult, Quiet mode included, and the rest are deleted; the gate hands
the handler the refused entries on the request context.

An Amazon S3 control-plane resource has one tag set, in `s3ControlResourceTags`
under its ARN. The creates that take tags — CreateAccessGrantsInstance,
CreateAccessGrantsLocation, CreateAccessGrant, CreateStorageLensGroup,
CreateAccessPoint — write there, deletes remove it, and TagResource,
ListTagsForResource and `aws:ResourceTag/<k>` read nothing else. TagResource on
an Access Grants location or grant ARN requires that location or grant to exist.
A Storage Lens configuration and a Batch Operations job keep their one tag set
through their own tagging operations (PutStorageLensConfigurationTagging,
PutJobTagging and their Get and Delete pairs), and the TagResource trio refuses
their ARNs, since the Service Reference lists neither type for it.

### Azure Key Vault and managed identities

The Key Vault data plane authenticates and authorizes every request in Key
Vault's order. The token must be simulator-signed, unexpired, issued for Key
Vault (`https://vault.azure.net`, its application ID, or `https://<keyVaultDns>`
for the cloud `/metadata/endpoints` describes, the audience a custom-cloud client
such as Terraform's acquires) by the vault's tenant (`sts.windows.net/{tenant}/`
or `login.microsoftonline.com/{tenant}/v2.0`); otherwise 401 `Unauthorized` with
the `Bearer authorization="<authority>/<vault tenant>",
resource="https://vault.azure.net"` challenge and the documented `AKV10000`,
`AKV10022` and `AKV10032` messages. Network rules come next:
`publicNetworkAccess: Disabled` refuses with 403 `ForbiddenByConnection`, and a
`Deny` default action admits only an address an IP rule or a virtual network
rule's subnet names, refusing with `ForbiddenByFirewall` and the address.
Last, `kvDataPlaneOperation` maps each route to its documented access-policy
permission and Azure RBAC data action, and `keyVaultGrants` decides under the
vault's model: role assignments through the shared
`dataActions`/`notDataActions` matcher at the object, the vault or above when
`enableRbacAuthorization` is set, otherwise access policies in the vault's
tenant, a compound identity's only for tokens its application obtained. Either
may name a group the caller belongs to, directly or nested. A refusal is 403
`Forbidden` with `AccessDenied` or `ForbiddenByRbac` and a message naming the
caller as `appid=…;oid=…;iss=…`. App Service's Key Vault reference resolution
uses the same decision. The Key Vault Administrator, Reader, Secrets Officer,
Secrets User, Crypto Officer, Crypto User, Certificates Officer and Certificate
User built-in roles carry their published permissions.

Tests grant themselves vault access as an operator does: an access policy naming
the object ID their token carries (`data.azurerm_client_config.current`, `az
keyvault create`'s creator policy, `az keyvault set-policy`), or a role
assignment on an Azure RBAC vault. A role assignment at a vault's scope does not
move with the vault, so move tests grant at the destination group too. Microsoft
Graph's `GET /me` answers an app-only token 400 "/me request is only valid with
delegated authentication flow.", the OData `any`/`all` lambda serves
`servicePrincipalNames/any(c:c eq '<appId>')`, and a service principal's
`servicePrincipalNames` hold its appId. An AD FS authority (`<host>/adfs`, the
CLI's custom-cloud login) issues tokens for the simulator's tenant.
`keyVaultDns` in `/metadata/endpoints` is `vault.<suffix>`.

An App Service app or slot with a managed identity gets its own
`IDENTITY_HEADER` secret, issued on first start and bound to its resource ID,
and the endpoint (`GET /msi/token`, api-version 2019-08-01) authenticates by it
and mints the system-assigned identity's token — `oid` its principal, `appid`
its client ID, `xms_mirid` the app's resource ID — or the user-assigned identity
`client_id`, `principal_id`, `object_id` or `mi_res_id` selects, refusing one the
app does not have with App Service's 400 "Unable to load the proper Managed
Identity.". An app without an identity gets neither variable. The instance
metadata service's token endpoint is a separate handler. The test workload's
`container-command files-http` relays `GET /identity-token` to the endpoint, and
every harness's simulator listens on every interface so an app reaches it.

Container apps and Container Apps jobs carry managed identities through the
same settling code: `identity` (common types v3, which spells
`SystemAssigned,UserAssigned` and reports `{"type": "None"}`), a system-assigned
principal registered under the resource's name, and user-assigned identities.
A workload with an identity gets `IDENTITY_ENDPOINT` and its own
`IDENTITY_HEADER`, which `/msi/token` resolves to the app, slot, container app or
job it was issued to. A container app is stored before its replicas start, so a
replica asking at once finds its identity. A secret naming a `keyVaultUrl` is
read as the identity its `identity` names (`system` or an attached user-assigned
identity), at the version the URL pins; an environment variable's `secretRef`
carries the value, and a registry password reference reads the same values. A
secret the identity cannot read fails the PUT or PATCH with 400 `Field
'configuration.secrets' is invalid ... Unable to get value using Managed
identity <identity> for secret <name>`, as does a `secretRef` naming no secret;
a job execution that cannot read its secrets fails and logs why.

App Service imports a Key Vault certificate as its first-party service
principal, "Microsoft Azure App Service" (application ID
`abfa0a7c-a6b6-4736-8310-5855508787cd`), which the directory holds in every
tenant, authorized through the data plane's own decision; tests grant it with
`az keyvault set-policy --spn <appId>`, a role assignment, or an
`azurerm_key_vault_access_policy` whose object ID `azuread_service_principal`
reads.

`Vaults_UpdateAccessPolicy` addresses the policy of the same tenant, object and
application IDs per permission category: `add` merges, `replace` sets, `remove`
subtracts, and a policy left with no permissions goes; add and replace append a
policy no entry matches, remove ignores it, and every other policy stays.
`azurerm_key_vault_access_policy` relies on all three.

A running app re-fetches its Key Vault references every 24 hours, as App Service
caches them: each start records the values its containers got and schedules the
re-fetch; unchanged values keep the app running, and a changed one — a rotated
versionless secret, a disabled version, a revoked grant — restarts it on the new
values, at once with Always On and on the next request otherwise. The vendored
Microsoft.Web specification defines no operation that forces the re-fetch, so
the simulator offers none.

A sitecontainer's `environmentVariables` entry names an app setting whose value
the container gets under the entry's name (empty when the setting does not
exist), with a Key Vault reference resolved; the Azure CLI refuses a spec naming
a missing setting, so the CLI test removes the setting afterwards.
`inheritAppSettingsAndConnectionStrings`, true when unset, passes the app
settings and connection strings to each sitecontainer as environment variables;
the platform variables and `environmentVariables` apply either way. The Azure
CLI's `--sitecontainers-spec-file` never sends the flag, so the CLI test sets it
false through `az rest`; the test workload's `relay-local` mode relays to a
sidecar's port over the shared loopback. The `azurerm` provider has no
sitecontainer resource.

### Front ends that lose their target mid-response

Every front end — Container Apps ingress, App Service, Azure Load Balancer,
Application Gateway, the Application Load Balancer, Amplify Hosting compute,
the Cloud Run front end and the external Application Load Balancer — relays
what the target sent and closes the client's stream unfinished when the forward
fails after the target's status and headers reached the client, as a streaming
proxy does; a failure before that gets the front end's own error. Each
simulator decides this itself (`abortStartedForward`), since `lbplane.Forward`
reports the failure and leaves the answer to the cloud's front end; Cloud Run
removes an instance whose ingress container died before aborting the response.

### Google Cloud IAM

`testIamPermissions` answers from the stored policy resolved through the
vendored curated roles and the held custom roles. A conditional binding grants
only while its condition holds: `cel-go` compiles the Common Expression Language
expression and evaluates it against `request.time` and the resource's `name`,
`type` and `service`, and an expression that fails to compile or evaluate grants
nothing. The Cloud Run front end's invoker check and `testIamPermissions` are one
evaluation. A caller presenting no simulator-issued token is the account's
operator, as a credential no IAM user registered is on AWS.
`generateAccessToken` honours the requested lifetime up to one hour, or twelve
where the Organization Policy constraint allows it, defaulting to deny.

A deleted service account lives in its own store keyed by unique ID, because the
live store is keyed by email and a new account may take that email during the
30-day window; undelete is keyed by unique ID for the same reason, which is why
gcloud sends nothing else. The window is IAM's 30 days measured against the
clock at each delete and undelete, with no timer of the simulator's own.

## Work that settles behind the request

### Azure asynchronous work and Container Apps revisions

Azure's asynchronous work answers the request and settles behind it. An Event
Grid webhook subscription stays Creating until its endpoint echoes the
validation code or someone opens the validation URL, and receives events only
once it has Succeeded. A Logic Apps trigger answers 202 with the run id and runs
the workflow in the background. Compute polls its operations at
`locations/{location}/operations/{id}`, the shape armcompute's recordings show;
every other provider uses `operationStatuses`. Cosmos DB mints hierarchical
resource ids and accepts them in paths.

A container app reports `eventStreamEndpoint`; its revisions list, read and
restart through ContainerAppsRevisions, and their replicas — one per
`minReplicas`, named `{revision}-{hash}-{suffix}` — through
ContainerAppsRevisionReplicas, each container advertising its
`logStreamEndpoint`. The streams sit on the event stream host outside the ARM
path, take the token getAuthToken issued, and serve newline-delimited
`{"TimeStamp","Log"}` lines (or text) with `tailLines` and `follow`: a console
stream opens with the service's "Connecting to the container" lines, carries
the container's output and ends when it exits; `eventstream` carries the app's
system events (AssigningReplica, ContainerCreated, ContainerStarted,
ContainerTerminated). The simulator keeps the newest 10 MiB of a container's
output, the kubelet's default containerLogMaxSize.

A container app keeps a revision history. A change to `properties.template`
creates a revision — `{app}--{random 7}` first, then `{app}--0000001`,
`--0000002`, or `{app}--{revisionSuffix}`, refused when the app already has it —
with its own template, `createdTime` and replicas; a change to
`properties.configuration` creates none. A Single-mode app deactivates every
revision but its latest, a Multiple-mode app keeps them active, and inactive
revisions report `lastActiveTime` and are removed oldest first beyond
`maxInactiveRevisions` (100 unset). `ingress.traffic` (`latestRevision` 100
unset; `az containerapp ingress traffic set` sends weights as strings) must name
revisions the app has and add up to 100, reports as each revision's
`trafficWeight`, and splits the requests the app's ingress FQDN receives; a
revision answers on its own FQDN. ActivateRevision and DeactivateRevision start
and stop a revision's replicas. Restart, activate and deactivate answer 200 with
the JSON strings the service returns (`"Restart succeeded"`, `"Activate
succeeded"`, `"Deactivate succeeded"`), captured from the Azure CLI's recorded
tests.

### Cloud Storage, Artifact Registry and Pub/Sub

Cloud Storage answers a permission question from the caller's own grants: the
project, bucket and object or managed-folder policies, and the bucket and object
ACLs while uniform bucket-level access is off. It refuses to delete a bucket
holding live objects, and a notification whose Pub/Sub topic is malformed,
missing or closed to its service agent. Objects keep their custom contexts
through every write, copy, rewrite and compose; `objects.list` filters on them
and `objects.viewFullContext` reads one back. gcloud's `objects update
--clear-custom-contexts` sends an empty `custom` map, which a patch treats as no
change (Google's Go client sends `contexts` as null to clear), so the CLI test
removes contexts by name. Artifact Registry deletes cascade across both planes:
a manifest DELETE over OCI removes its version, tags and image row, and a
package or repository takes everything beneath it. The simulator's tokens carry
Google's issuer, and its discovery document names that issuer with its own key
and token URLs, so a relying party verifies them as it verifies Google's.

A Pub/Sub `Pull`, over gRPC and REST, holds an empty subscription open unless
the caller sets the deprecated but honoured `returnImmediately`, and answers
with at most `maxMessages` once a message becomes deliverable, the server's
bound passes, or the caller goes away. The wait wakes on the subscription's own
signal — a publish, an ack, a nack or ack deadline change, a seek, the
subscription's update, detachment or deletion — and on a timer set to the next
moment the queue changes by itself (a lapsed ack deadline, the end of a retry
backoff). `StreamingPull` waits on the same two things and checks its stream is
still open before it leases, so a nack racing the client's cancellation cannot
strand a redelivered message on a closed stream. Messages a failed send never
delivered are released uncounted. The REST pull declares its bound with
`sim.DeclareWait`; the gRPC listener sits outside `sim.InFlightMiddleware`.
Suites that check a subscription is drained pull with `returnImmediately`.

### AWS work that finishes later

AWS work the services finish later finishes later in the simulator, gated on
the real inputs. An EC2 instance stays pending until its VM boots, whether
`RunInstances` or EC2 Auto Scaling launched it, and a launch lifecycle hook
holds it in `Pending:Wait` until the hook's action completes or times out. EC2
Auto Scaling answers a capacity change at once: members join Pending, each
launch has an InProgress activity, and a background boot moves both on. An
awsvpc ECS task creates its network interface at `RunTask`, so `DeleteSubnet`
refuses while the task holds it, and DeleteSubnet ends the subnet's route table
association. Amazon S3 checks each notification destination's resource policy
at put time and sends the test event. `ImportSnapshot` and `ImportImage` read
the disk image from S3 and convert RAW, VHD and VMDK into real snapshot data. A
waiting SQS receive wakes on the send, delay or visibility change it waits for.
ECS creates an awslogs log group only when the task definition asks for it.

AWS Batch answers `SubmitJob` at once and schedules the job behind it: RUNNABLE
once evaluated, STARTING when an ENABLED compute environment of its queue with
`maxvCpus` to spare placed it, RUNNING when its container started, and a
terminal state when it exited; a job no environment can place waits in
RUNNABLE. Each attempt runs a new container with the `AWS_BATCH_*` variables,
logs to its own `/aws/batch/job` stream, and is recorded in `attempts`. The
request's `retryStrategy` overrides the job definition's, `evaluateOnExit`
conditions decide in order, and a terminated or timed-out attempt is never
retried. An array job spawns `<parent>:<index>` children and settles once every
child has. `CancelJob` stops jobs that have not started, `TerminateJob` started
ones, both reaching an array's children. A job with `dependsOn` waits in PENDING
until each dependency finished, becoming RUNNABLE when all succeeded and failing
with `Dependent Job failed` otherwise, cascading down the chain; `N_TO_N` pairs
array children by index and `SEQUENTIAL` runs child *i* after *i − 1*. A
fair-share queue takes only jobs with a `shareIdentifier`, and a FIFO queue
none; its scheduler places next the job of the share with the least weighted
usage — the vCPUs the share holds plus their average over `shareDecaySeconds`
under exponential decay, times `weightFactor`, matched by name or `*` prefix —
then by `schedulingPriority` and arrival. `computeReservation` holds back
(`computeReservation`/100)^active shares of `maxvCpus` for shares that hold none
yet. Queues sharing a compute environment are served highest `priority` first.
A queue's scheduling policy can be replaced but not added to a FIFO queue, and a
policy in use cannot be deleted.

## Measurement and gates

Every quality gate has been shown to fail on a planted violation of its own
shape, and a gate whose scan set can go empty exits non-zero rather than green:
two gates once scanned the monorepo's directories, found nothing, and hid two
live slice-bounds panics.

The coverage ratchets hold a served floor that may only rise and the declared
total of every vendored document, so a re-vendor that adds a surface fails until
it is served or declared, and a withdrawal is named in the floor comment. What a
re-vendor adds is served wherever the document describes something the
simulator already holds. A floor counts only what it measures.

The store-scan gate holds request-path full-store reads at zero; a row indexed
under every `/`-terminated prefix of its identifier answers a child listing and
a cascading delete from one `GenerationIndex`. The lock gates hold read-only
critical sections under exclusive locks, and `RLock`/`Unlock` mismatches —
which neither the compiler nor `go vet` sees, and which kill the process — at
zero. A section that only reads takes `RLock`; one that writes, or reads and
then writes on what it read, takes `Lock` for the whole span. The fake-test
gate decides nine shapes of can't-fail test from the syntax tree, and the
dead-code gate judges the framework from the three programs that link it.

Attribute a slow phase by measuring it where the simulator runs, never by
reading the code or the database header. Amazon ECS task-start phase marks
travel on the context (`realexec.WithMark`), and `/debug/stores` reports each
table's size. A duration says only "fast today", so concurrency fixes are proven
by counting — peak readers inside a lock, items a query read.

The slow-request diagnostic (`sim.InFlightMiddleware`) reports a request only
once it outlives the wait its handler declared by `SlowRequestThreshold`. A
method that blocks by design names its bound on the request context:
`sim.DeclareWait` for a long poll, an operation wait or a synchronous run under
its configured timeout, `sim.DeclareOpenEndedWait` for a stream or a wait the
caller left unbounded. An upgraded connection counts as open-ended by itself.
The framework holds no list of such methods; each cloud's handler says what the
cloud documents. The watcher reads an injected clock and the middleware waits
for it before finishing a request, so its tests step time and a report cannot
land after the request ended.

The race detector runs on every pull request over every module. `bg.Go` and
`bg.AfterFunc` count goroutines and pending timers; a drain (`bg.Await`) is a
barrier that stops unfired timers and drops work requested while it runs,
because a reconciliation requests another whenever it moves a task.
`bg.JoinedGo` counts work a caller waits on and never drops it. Finite work
handed to `Server.StartBackground` registers with the drain (`bg.Handoff`);
lifetime daemons do not, or the barrier would wait forever. Arming a timer or
watch holds a read lock across its admission and its entry in the map a drain
stops, and a drain takes the write lock to begin, so a timer armed as a drain
starts is either stopped by it or refused.

A test waits on the event it asserts. The AWS suites use the SDK's own waiters
with 250 ms to 2 s delay bounds instead of the published 5 to 60 s —
`InstanceRunning`, `SnapshotCompleted`, `TasksStopped`, `ServicesStable` with an
acceptor that needs the rollout COMPLETED, `VolumeDeleted`,
`DBInstanceAvailable`, `DBSnapshotAvailable`, AlarmExists filtered by
StateValue, TargetInService — and the CLI suite the matching `aws … wait`
commands. Where no waiter exists they use a long poll, Cloud Map's GetOperation,
or Live Tail opened before stored history is read; a queue that must stay empty
is watched with one long poll for the window. An `aws <service> wait` call is
bounded only by the test's deadline, since the waiter polls on its own delay (30
seconds for Amazon RDS) and fails by itself. The SQS retention test sets a
message's visibility timeout to end just after the retention deadline and then
long-polls. Waiting for full steady state is preferred over checking a partial
condition.

An Azure test that starts a long-running operation by hand waits as a client
does: the SDK suites hand the raw response to azcore's poller
(`awaitARMOperation`), and the CLI suites drive `az rest --debug` through
`azLongRunning`, which answers a Location poll's 202 by waiting its Retry-After
(`azLocationResult`). An effect is read once after its operation ends, never
polled for. The Event Hubs Latest consumer sends its event once azeventhubs logs
the completed receiver attach through azcore's log listener.

Every simulator a test starts waits on the banner it prints after binding
(`simready`). Every harness that fronts one with the Caddy HTTPS gateway starts
Caddy through `testutil/httpsgateway`, which reads Caddy's JSON log — the http
app names the certificates it manages, and certmagic's `tls.cache` logger, which
the gateway's Caddyfile routes to standard error at debug level, reports each as
it enters the cache handshakes read — and returns once every managed name is
cached, then makes one health request; certmagic's `certificate obtained
successfully` line precedes caching. The Caddyfile turns off Caddy's storage
check and clean-up, since each gateway starts on an empty data directory and the
clean-up raced the first issuance. Those options need Caddy 2.7, newer than
Ubuntu 24.04's 2.6.2, and Caddy's apt repository answers CI runners 402, so CI
and `Dockerfile.test` install a pinned release from GitHub against its published
checksums (`scripts/install-caddy.sh`), which ends by adapting the Caddyfile so
an incompatible Caddy fails the install step.

A Cloud Run job that must be seen running runs `container-command log-until
MESSAGE PATH`, holding until an object appears in a Cloud Storage bucket it
mounts read-only, and the test writes that object once it has observed the
running state. The waits that remain are the cloud's or the engine's own:
Pub/Sub's ten-second minimum ack deadline, the five-second
`busy-reply-threshold` before a Redis replica running a script answers BUSY, and
Redis's two-second hold on a new cluster primary.

Tests name their resources per run — `uniqueName`, and `uniqueAlnumName` for
names that admit no hyphen or stop at 24 characters — so a repeated run against
one simulator passes; a test that changes an account-wide setting (the Amazon
EC2 default credit specification, the account's Amazon VPC encryption control)
restores it at cleanup. A Cosmos DB test provisions its own account, and a test
reading a subscription-wide listing picks out its own row.

The SDK and CLI suites run their tests in parallel. Every top-level test in the
six suites starts with `t.Parallel()`, or names itself in its suite's
`serial-tests.txt` with the reason it cannot share the simulator: it changes an
account-, project- or subscription-wide setting other tests read, owns the
account's one AWS Organizations organization, sets an organization policy every
project inherits, sets an environment variable of the test process, or restarts
the simulator. `scripts/check-parallel-tests.go`
holds the suites to that in pre-commit and CI's build gates, and fails a listed
test that no longer exists or already runs in parallel, and a parallel test that
reaches `t.Setenv` or `t.Chdir` through any function of its suite. A parallel
test names its resources apart from every other test's and never asserts the
length of a whole account-wide list. The Google Cloud SDK suite sets
`STORAGE_EMULATOR_HOST`, `BIGTABLE_EMULATOR_HOST` and `SPANNER_EMULATOR_HOST`
once in `TestMain`; its Firestore and Pub/Sub gRPC clients take the simulator's
address as their endpoint, because the Firestore REST client refuses to start
while `FIRESTORE_EMULATOR_HOST` is set.

Every SDK and CLI suite target runs `go test -v` through
`scripts/go-test-summary.sh`, which ends a failed run with each failing test's
own lines grouped under its `--- FAIL`. A suite's output runs to megabytes and
its parallel tests interleave, so the tail a CI log reader sees otherwise held a
bare FAIL.

The gcloud suite installs the `alpha` and `beta` release tracks in `TestMain`
when the CLI lacks them, and CI and `Dockerfile.test` install them with the CLI:
gcloud installs a missing track on the first command that names it, inside that
test's per-command budget and alongside every other parallel test doing the
same. A test that cancels a Cloud Run execution to exercise its workload's
SIGTERM handler waits for the workload's own log line, matched whole, because
the job's log also holds lines the service writes before any container runs. A
cancel that lands while an execution's containers are still starting stops the
start and settles the execution cancelled, with no attempt failure recorded or
logged.

Cloud Storage's lists resume strictly after the last name (or access ID, or
cache ID) a page returned, so a bucket created or deleted between two page reads
moves no other bucket onto a second page or off every page. `buckets.list`
requires `project`, lists only that project's buckets and honours `prefix`; a
test walking a shared project's buckets lists its own under a prefix of its own.

An Amazon ECS test that deletes its subnet first stops its own cluster's tasks
in that subnet and waits with `aws ecs wait tasks-stopped`, scanning only its
own cluster. A task that holds the subnet traps SIGTERM, so `StopTask` ends it at
once.

The AWS CLI tests create AWS Lambda functions with the architecture of the
images `TestMain` builds, which match the host's.

A test asserts a boundary at a small parameterised limit rather than by reaching
the real one: the OCI body cap is a parameter tested at 64 KiB. A soak test's
reader pool is sized to the machine and yields. A nightly fuzz failure becomes a
seed, so ordinary `go test` catches the regression.

`sim.SyncContainerHostEntries` rewrites a container's whole hosts file from what
it read, so it holds a mutex across concurrent Cloud Map namespace syncs.

Dependencies of every class — Go modules, Terraform providers, GitHub Actions,
installed tools, the consoles' npm packages and digest-pinned container images —
are held to their newest release past a 24-hour adoption quarantine.
`ui/bunfig.toml` sets `install.minimumReleaseAge` to the same day, because caret
ranges let the resolver pick versions the quarantine refuses. An exact provider
pin is compared exactly, an unpinned provider is a failure, and every entry of a
`required_providers` block is read. On a pull request, a drift byte-identical to
`main`'s is reported rather than failed, since upstream moved under the branch.
Every network lookup carries a deadline and fails naming what never answered. A
deliberate hold names its cause and goes when the cause does. Resolving the
module behind a workflow's `go install` path asks the module proxies alone,
since `direct` would clone each non-module prefix.

The quarantine's self-test (`scripts/test-latest-deps-quarantine.sh`) serves
every registry from fixtures it writes: a Go module proxy over Go's `file://`
protocol, with `proxy.golang.org` answering only checksum-pinned files, and a
local HTTP server holding the Terraform registry's and the GitHub API's
documents, answering an unknown credential with 401 as GitHub does. The check
reads them through `GOPROXY`, `DEPS_TERRAFORM_REGISTRY_URL` and
`DEPS_GITHUB_API_URL`. Every publication time is fixed except one Terraform
release dated an hour before the run, and a listed version whose document
carries no `published_at` fails the run by name. Reading `GOPROXY` runs with
`GOTOOLCHAIN=local`, since `go env` would otherwise download a newer toolchain
through the proxy a stalled run cannot reach.

Digest-pinned images follow the same quarantine. `scripts/check-latest-deps.sh`
finds every `public.ecr.aws/docker/library/...` or `mirror.gcr.io/...`
reference spelling a tag and a digest, outside Markdown, and reads Docker Hub's
tag API and the copy's manifest. A pin fails when Docker Hub has served another
digest for its tag for a day, when a newer release with the same variant and
major version has been on Docker Hub for a day, or when the copy serves another
digest than the pin; anything younger is held. A newer number Docker Hub stopped
rebuilding before it last rebuilt the pinned tag, such as Ubuntu 24.10 beside
24.04, is an abandoned line. Requests to ECR Public are spaced a second apart,
its unauthenticated limit.

## Continuous integration

Jobs are held to fifteen minutes; a timeout kill reports as "cancelled", which
reads as infrastructure noise. The suites are sharded on measured time, with
gates holding every test to exactly one shard. Each suite builds its simulator
into `.build/<suite>/` and namespaces its images per suite.

Base images are read out of the source by `scripts/base-images-for.sh` — which
scans `sim/` for every simulator and reads digest references — and warmed from
one cache entry per cloud keyed on the image set, because the ECR Public Gallery
caps anonymous pulls by data volume, which no retry recovers from. Every suite
takes a base image through `testutil/baseimage.Ensure`, which asks the host
first; a pull that tests a simulator's own registry is the named exception. A
reference spelling a tag and a digest is saved and found under the digest's
local tag (`repo:sha256-<hex>`) by the warm script and
`testutil/baseimage.LocalRef`. Warming is `continue-on-error`, because the point
of use fails naming the image. Jobs never delete the shared Go caches and
refresh the apt index only when a package is missing. In the AWS SDK shards one
shard saves the Go cache after its pre-build and the rest only restore it; the
build gates keep their own Go cache. Workflows reference as few external actions
as possible, since the runner downloads every named action per job. A tool a
suite needs — gcloud, `cbt`, the AWS CLI — is installed in `TestMain` with a few
retries, never skipped. A Terraform harness stops when its simulator does not
build.

Nothing CI builds or runs pulls from Docker Hub, which limits anonymous pulls
per address. The simulator images and `Dockerfile.test` build from the ECR
Public Gallery's copy of Docker's official images and BuildKit comes from
`mirror.gcr.io`, each pinned to the index digest Docker Hub serves for the tag;
a test's workload image names its ECR Public tag, and another public image comes
from `mirror.gcr.io`. A digest-pinned workload image is left out of warming
because `docker load` restores none by digest. The images the simulators pull
themselves follow the rule: the Container Apps Dapr sidecar, the Percona
XtraBackup helper and the Memorystore for Redis 3.2 and 4.0 engines come from
`mirror.gcr.io`, and the Amazon EBS volume-copy helper runs the ECR Public
alpine.

A differential oracle is pinned by its multi-platform index digest, and the
harness refuses an image built for another architecture than the engine's. Its
readiness wait is a wall-clock deadline with a single-attempt probe, and the
container is kept until the test removes it, so a failure reports the last probe
error, the container's state and its log. Microsoft's Cosmos DB emulator starts
once for the Azure SDK suite, from `TestMain`, because two emulators on a
two-core runner starve each other. Every Terraform harness runs `init -upgrade`
against exactly pinned providers, a pre-commit hook holds `.tf` files to
`terraform fmt`, and a suite's Lambda image declares the machine's architecture.
The Google Cloud gRPC door listens on the HTTP port plus two (`:4569`), so the
three simulators' defaults coexist.

A push to `main` runs CI only when its tree has not already passed on its pull
request. Branch protection makes a pull request current with `main` before it
merges, so a squash merge commits the tree its head carried, and re-running
every job per merge starved the account's runners. The first job
(`scripts/ci-verified-on-pr.sh`) finds the merged pull request, compares trees
and looks for a successful CI run on its head; when all hold, every other job
skips and the run's verdict is that pull request's. A merge made before its CI
finished, a push without a pull request, a tag push and any failed lookup run
the full suite, and `!cancelled()` on every job keeps a failure of the check
itself from skipping anything.

Release pull requests run no CI: they change only `CHANGELOG.md` and
`.release-please-manifest.json`, the Actions approval policy holds runs of pull
requests opened by `github-actions[bot]`, and the owner keeps that policy strict
so outside contributors cannot run CI; the owner merges them by bypass. The
Release workflow ends in a reconciliation job
(`scripts/verify-release-complete.sh`) that fails unless every expected asset
and image index exists. The simulator images build from the committed
`simulator-<cloud>/dist` console bundle, with no console build stage, keep the
Go caches in cache mounts and export no BuildKit cache, which only re-uploaded
base layers. Manifest composition retries only a broken connection.

Publishes are keyed per commit and never cancelled by a later merge; retention
runs in its own workflow and spares anything younger than two hours, because a
publish between its two per-architecture pushes looks like an abandoned remnant.
The required-status-check manifest is compared against `main`'s live branch
protection by a pre-push hook, since protection drifts without a commit. The
daily specification refresh checks out the pull request it will land on before
re-vendoring, so it measures drift against that branch's pins and lands as one
commit.

The real network host tests run in the Firecracker job, which has `/dev/kvm`,
root through `sudo`, iproute2 and nftables. `tests/realexec/run-network-nic.sh`
finds every package holding a `realexec_host` test file and runs the tests they
declare, compiling each binary as the runner user on the warm Go cache and
running only the binary as root. A suite's host gate skips only for what a run
cannot be given — a kernel other than Linux, `/dev/kvm`, `CAP_NET_ADMIN` — and
fails on a Linux host that lacks a command, naming what to install. The Google
Cloud and Azure suites share `requireNetworkHost` and `requireMicroVMHost`, the
latter adding the Firecracker detection the simulator boots with.

A Firecracker guest's boot is the kernel's `IP-Config: Complete` line on the
serial console, after which one ping confirms the address.

A workload container publishes every port the host reaches it on:
`sim.HTTPContainerConfig.PublishPorts` is required, and `StartHTTPContainer`
publishes each on loopback — an App Service site its `WEBSITES_PORT`, and a
Cloud Run network-namespace owner the ingress port and every startup probe's
port, for services and jobs alike.

A Google provider release that adds a resource over an API the simulator already
serves gets a fixture under `simulator-gcp/terraform-tests/fixtures/`, applied,
read back and re-planned for drift by `applyFixture` and `requireNoDrift`, with
only the endpoints that resource reaches.

`scripts/check-repo-config-sane.sh` fails a commit whose checkout has acquired
`core.bare = true` or a test fixture's identity — the corruption a `git config`
run without `-C` in a linked worktree writes into the shared `.git/config`.
Every `git config` this repository runs names its target with `-C`.

`Dockerfile.test`, the shared harness image, is committed and un-ignored by name
(`.gitignore` ignores `*.test`), with every toolchain pinned to a version and a
digest, because the image decides what a test result means.

The console browser suites run fully parallel, with Playwright's default of one
worker per two cores.
