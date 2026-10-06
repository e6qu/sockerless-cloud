# STATUS

Current state of the sockerless-cloud repository.

## Layout

- **Three installable simulators.** `simulator-aws/`, `simulator-gcp/`,
  `simulator-azure/` are root Go modules
  (`github.com/e6qu/sockerless-cloud/simulator-<cloud>`) with no `replace`
  directives, so `go install …@<release-commit>` works. Each embeds its console
  from a committed `dist/`.
- **One framework module.** `sim/` (`github.com/e6qu/sockerless-cloud/sim`)
  is the server, container-engine layer, durable state, OCI data plane,
  middleware and parsing primitives every simulator is built on. It is
  cloud-neutral: a cloud's error shape, protocol router, sandbox profile,
  console coordinates, request rewrite and registry behaviour live in that
  cloud's module and reach the framework through hooks. The support modules
  `realexec/`, `ui-auth/` and `testutil/` sit beside it.
- **Shared mechanisms live in `sim`.** Workload-host coordinates, background
  work, workload launching, archive extraction, payload storage and sparse
  files, JWT signing and OpenID Connect federation, registry authentication and
  the managed relational database data plane are one package each under
  `sim/`; the realized network fabric and the load-balancer data plane are
  `realexec/fabric` and `realexec/lbplane`, and queues, stream logs, push
  delivery and cron schedules are `sim/msgq`, `sim/streamlog`, `sim/delivery`
  and `sim/cron`. All three simulators use them.
- **Pins carry the working tree's content.** Each simulator pins `sim`,
  `realexec` and `ui-auth` by pseudo-version;
  `scripts/check-support-module-pins.sh` downloads every pinned version and
  fails when its content differs from the tree, and
  `scripts/check-installable-build.sh` builds each simulator with `GOWORK=off`
  the way `go install` and every SDK harness do. A support-module change lands
  in two pushes: push, pin the pushed commit, push again; after the merge the
  pin moves onto the merge commit so it never names a deletable branch head.
- **Console SPAs** build from `ui/` (Bun + Turborepo); they read only real
  cloud APIs and federate operator credentials through each cloud's own
  federation primitive. The consoles' OpenID Connect layer and the
  `GET /monitoring/observation` endpoint are one `ui-auth` implementation, and
  the Google Cloud access-token verifier hands the monitoring path to its own
  bearer check, as it does the console's session routes.

## Declared surface

- **AWS**: the 42 vendored Smithy models are implemented or exempt in full, the
  exemptions being the Amazon S3 bucket subresources the query-parameter table
  routes, each verified against that table. IAM resource derivation covers
  2,007 of 2,015 served operations; the eight that remain are requests naming
  no resource, and `"*"` is the honest answer. Condition keys are ratcheted too:
  every key the vendored Service References declare -- 653 over 1,917 actions --
  is either named by the gate or classified as unmodelled with the reason, and a
  classified key the gate later resolves fails its own row, and every key is
  also measured per action: of 4,310 (action, key) pairs on served operations
  the gate builds 3,695, and each of the other 615 is listed with its reason in
  `testdata/iam_condition_key_gaps.tsv` (BUG-2965 holds the 16 that need a
  container or microVM to seed). A create carrying tags is also authorized as its service's
  tagging action with `<service>:CreateAction` naming it, for the 297
  operations the references list one for, against the resource the create
  mints. The Amazon S3 control plane is
  authorized route by route, each in the namespace AWS publishes its action under — s3, s3express,
  s3-outposts or s3-object-lambda. One route is tested as ungated and says why:
  no vendored document declares an action for the control plane's
  DeleteBucketLifecycleConfiguration. Each control-plane resource has one tag
  set: a Storage Lens configuration's and a Batch Operations job's are kept by
  their own tagging operations, every other type's by TagResource and the
  creates that take tags.
- **Google Cloud**: 5,585 of 5,585 Discovery method spellings across 31
  documents reach a route that names them — 5,531 served and 54 answering a
  declared 501; the gRPC surfaces serve 213 of 216 methods, the three unserved
  each needing state the simulator does not hold.
  Every gRPC service is crossed against its REST door.
- **Azure**: 2,653 of 2,660 Swagger operations across 122 documents, App
  Service's 692 included. The seven others answer a declared 501: the six
  data collection rule association operations, as the simulator runs no
  Azure Monitor Agent, and a table's search-job cancellation, as it runs no
  search jobs.
- **Both ratchets refuse phantom coverage**: a served method must be answered
  by a route naming its literal path segments, and the routes that legitimately
  dispatch inside a handler are listed with the reason each one does. Every
  unserved operation answers a declared 501 naming what is missing, held by a
  gate rather than observed.
- **Vendored specifications track upstream** through the daily freshness
  workflow, which pushes a refresh onto the open pull request. A re-vendor
  moves declared totals; a served count that falls must be shown to be a
  withdrawal.

## Fidelity

- **Every workload is a real container** on the engine the simulator started
  against, under the cloud product's sandbox profile. Startup refuses to serve
  in any mode that executes workloads when no engine answers, after retrying
  the readiness ping for a budget. `SIM_RUNTIME=process` is API-only and says
  so.
- **A workload starts where its definition says**: an Amazon ECS container
  definition's `workingDirectory` reaches the engine, which creates the
  directory when the image lacks it, and an ExecuteCommand session inherits it.
- **A task is placed only where it fits.** Amazon ECS RunTask commits each
  placed task's declared memory and CPU against what the simulator's own
  cgroup (or the machine) can hold, and refuses a task that would not fit the
  way the service does — HTTP 200, no task, a `failures[]` entry with
  Fargate's capacity message or `RESOURCE:MEMORY`/`RESOURCE:CPU` — before any
  ENI, volume or record exists. The simulator container's memory and CPU
  limits are its capacity.
- **Provisioned throughput is spent.** An Amazon DynamoDB PROVISIONED table
  runs the service's token buckets — per table and per global secondary
  index, refilled at the provisioned rate, holding 300 seconds of burst — and
  a request the bucket cannot cover gets
  `ProvisionedThroughputExceededException` (or comes back unprocessed from a
  batch). On-demand tables are never throttled; `CreateTable` requires
  capacity units exactly when the billing mode does.
- **A stopped workload gets its cloud's grace** between SIGTERM and SIGKILL:
  an Amazon ECS container definition's `stopTimeout`, Cloud Run's ten seconds,
  a Container App template's `terminationGracePeriodSeconds`, App Service's
  `WEBSITES_CONTAINER_STOP_TIME_LIMIT`, each with the platform's own default.
- **Persistent workloads survive a restart.** With `SIM_PERSIST`, shutdown
  leaves the containers running and the next process adopts them by label;
  without it, the detached reaper and the startup sweep collect a run's
  containers, scoped to the state directory so a concurrent suite is never
  touched; a removed container takes its anonymous volumes with it and
  leaves its named ones. A simulator exits when the process in
  `SOCKERLESS_PARENT_PID` is gone. A stopping simulator does not wait out a
  container's stop timeout or a function's timeout: interrupted Amazon ECS
  and AWS Lambda work resumes in the next process, and an open long poll, a
  Live Tail session or a synchronous AWS Lambda or Step Functions call ends
  with the server.
- **Every credential is verified**: SigV4 against the principal's stored
  secret, from the header and from a presigned URL alike; Google Cloud and
  Microsoft Entra bearers against the simulator's signing keys; the Azure
  Storage data plane's Shared Key and shared access signatures against the
  layouts Microsoft's own signers produce; Cosmos DB's shared key on every
  path; each container registry against the credentials its control plane
  mints, with its own challenge shape.
- **Managed databases run real engines** with volumes, credentials sealed
  under the simulator's own key service, readiness classified by SQLSTATE, and
  snapshots that capture the data copy-on-write where the volume store allows
  it. An Aurora cluster takes its first automated backup when it is created
  or restored, whether or not a client connects, and restores to any time in
  its backup retention period since then, replaying PostgreSQL's archived write-ahead
  log or MySQL's binary log onto the daily automated DB cluster snapshot taken
  in its backup window, and expires the snapshots and log the period no longer
  covers; an Aurora cluster restores from an RDS DB snapshot ARN, and an
  Aurora MySQL cluster from a Percona XtraBackup in Amazon S3. An RDS for
  PostgreSQL or RDS for MySQL instance keeps the same automated backups,
  taking the first when it is created whether or not a client connects and
  reporting `creating`, `starting` and `backing-up` until it is `available`:
  RestoreDBInstanceToPointInTime seeds the new instance from them, and
  RestoreDBInstanceFromS3 imports a Percona XtraBackup into RDS for MySQL. A
  deletion with `DeleteAutomatedBackups=false` retains the automated backups,
  which restore the deleted instance or cluster to a time, and an instance's
  automated backups replicate to another Region, where they restore after
  the replication stops or the source is gone. A DB instance's or an Aurora
  cluster's endpoint signs in the master user and IAM-authenticated users
  itself and every other database user through the engine's own checks; on
  PostgreSQL an IAM token signs in the role granted `rds_iam` that it names. Deleting a
  database kills an engine still initialising its volume rather than waiting
  out the stop grace.
- **The registries answer their own service**: Amazon ECR's empty ping with
  no content type, Artifact Registry's `text/html`, Azure Container Registry's
  `{}`; ECR hydrates a pull through a cache rule from the rule's upstream;
  Artifact Registry refuses the second write into an upload session; ACR keys
  its content per registry and serves its catalog on `/v2/_catalog` as on
  `/acr/v1/_catalog`.
- **A build service's docker steps run as the build**: a Docker
  configuration built on the host's own (the framework's
  `sim.WriteDockerConfig`), whose credential helper answers the build's
  registries — Artifact Registry and Container Registry with the Cloud Build
  service account's token, an Azure Container Registry with an identity
  token of the ACR Tasks run — names them outright for the legacy builder,
  and hands other registries to the host's helper.
- **A Cloud Build step of any builder image runs as a container** over the
  build's `/workspace` (its `dir`, `entrypoint`, `args`, `env`, `secretEnv`),
  sharing the workspace with the docker builder's steps; a source on Cloud
  Storage may be a zip archive or a gzipped tarball.
- **Future-dated Capacity Reservations are scheduled, committed and
  postponed by quote.** A reservation requested for a future start is
  `scheduled` with no instances and a commitment until its start date, derived
  from the clock on every read; its start date moves only through an accepted
  date-change quote, within 30 days of the original.
- **Object stores arbitrate conditional writes.** Cloud Storage's generation
  and metageneration preconditions and Azure Blob Storage's conditional
  headers are evaluated on every read and write, a write's check and store are
  one step under a per-object lock, and a Cloud Storage generation is never
  reused; the XML download, V4 signed URLs and the JSON API batch endpoint
  answer as the service does. Amazon S3 conditional writes take the same
  per-object lock (`sim.KeyedLocks`), and all three object stores keep each
  object's contents in a file of its own (`sim.Payloads`) that the row
  references.
- **Amazon S3 versions objects.** A versioning-enabled bucket gives every
  write a version id and keeps the version it supersedes; a delete without a
  version id leaves a delete marker as the key's latest version, and one with a
  version id removes that version and makes the next newest current. A
  suspended bucket writes and deletes the `null` version. GetObject,
  HeadObject, the object subresources, CopyObject's and UploadPartCopy's
  source, DeleteObject, DeleteObjects and AWS Lambda's `S3ObjectVersion` address
  a version by id, ListObjectVersions pages versions and delete markers with
  key and version-id markers, and noncurrent versions keep their own tags and
  keep a bucket from being deleted. DeleteObjects refuses an entry the caller
  may not delete as that entry's AccessDenied in the DeleteResult and deletes
  the rest.
- **Amazon S3 Object Lock protects object versions.** CreateBucket with
  `x-amz-bucket-object-lock-enabled` enables Object Lock and versioning with
  it, and PutObjectLockConfiguration enables it on a versioning-enabled bucket;
  versioning then stays enabled. A new version takes the retention and legal
  hold its write asks for, or the bucket's default retention. A legal hold, an
  unexpired COMPLIANCE retention, and an unexpired GOVERNANCE retention without
  `x-amz-bypass-governance-retention` (authorized as
  s3:BypassGovernanceRetention) refuse a version delete with AccessDenied, and
  an active retention only grows stricter. A bucket without Object Lock refuses
  the Object Lock operations and headers with InvalidRequest.
- **An Elastic Load Balancing trust store reads its contents from Amazon S3.**
  CreateTrustStore and ModifyTrustStore read the CA certificates bundle from
  the object version named (the current one when none is) and count its PEM
  certificates; AddTrustStoreRevocations reads each certificate revocation list
  and counts its entries. A missing object answers CaCertificatesBundleNotFound
  or RevocationContentNotFound, and content that does not parse answers
  InvalidCaCertificatesBundle or InvalidRevocationContent. The trust store
  keeps its own copy of what it read, and GetTrustStoreCaCertificatesBundle
  and GetTrustStoreRevocationContent answer a presigned Amazon S3 URL on the
  simulator's endpoint that serves exactly that copy, whatever has since
  happened to the customer's object.
- **Amazon EC2 instance types carry AWS's published facts.** The simulator
  embeds the AWS Price List offer file's Compute Instance products for
  us-east-1 (`simulator-aws/ec2_instance_types_vendored.json`, written by
  `scripts/fetch-aws-ec2-instance-types.go` with the offer version and
  SHA-256 it read). DescribeInstanceTypes answers every listed type's vCPUs,
  memory, architectures, network performance, generation and instance-storage
  support, filters and pages over them, and refuses an unknown type with
  InvalidInstanceType; DescribeInstanceTypeOfferings,
  GetInstanceTypesFromInstanceRequirements, the Capacity Manager vCPU metrics,
  the real-execution machine shape and the 32-vCPU minimum of a future-dated
  Capacity Reservation read the same catalog.
- **Amazon ECR sizes an image by the blobs its manifest references.**
  DescribeImages' imageSizeInBytes is the compressed layers plus the config,
  not the manifest document, and a manifest list's largest listed manifest.
- **AWS STS sessions carry session tags.** AssumeRole's `Tags`, a SAML
  assertion's `PrincipalTag:<key>` attributes and a web identity token's
  `https://aws.amazon.com/tags` claim tag the session, each authorized as
  sts:TagSession against the role's trust policy; the session reports them as
  `aws:PrincipalTag/<key>` over its role's tags, and its transitive tags pass
  to every session chained from it.
- **A bucket carries Cloud Storage's default policy** from creation — the
  four legacy bindings for the project's owners, editors and viewers — so a
  client revoking what it granted sets the defaults back, never nothing.
- **Uploads past 5 MiB take the resumable path from the vendor CLIs too**:
  `gcloud storage cp`, `gcloud artifacts generic upload`,
  `gcloud artifacts files upload` and `bq load` are tested over it. BigQuery
  answers REST errors with BigQuery's `errors[]` reasons.
- **Every implemented API serves its Discovery document** at
  `GET /$discovery/rest?version=…` under its own host (regional and mTLS hosts
  too), byte-identical to the vendored one, and without a version the API's
  default version. A bare address:port answers a version one implemented API
  publishes and `v2` with BigQuery's, which `bq` builds its client from. The
  Discovery service's directory, `GET /discovery/v1/apis` and
  `GET /discovery/v1/apis/{api}/{version}/rest`, serves the real directory's
  entries for the embedded documents and each document where that host's
  central path serves it (`simulator-gcp/discovery_directory_vendored.json`).
  Discovery documents and the directory answer without a credential.
- **A bucket belongs to a project Cloud Resource Manager holds.**
  `buckets.insert` resolves its project by ID or number, refuses an unknown one
  with `400 Unknown project id`, and stamps the project's own number;
  `projects.serviceAccount.get` names the agent
  `service-{projectNumber}@gs-project-accounts.iam.gserviceaccount.com`, the
  identity the notification check evaluates, so gcloud's and Terraform's
  notification flows grant publish to the agent Cloud Storage checks.
- **Every identity a service names for a project carries the number Cloud
  Resource Manager holds.** Cloud DNS `projects.get`, Cloud Build's default
  service account (`{number}@cloudbuild.gserviceaccount.com`, also the
  identity a build's docker steps pull and push as), the Cloud Run service
  agent (`service-{number}@serverless-robot-prod…`), Compute Engine's
  `projects.get` (`id` and `{number}-compute@developer.gserviceaccount.com`),
  BigQuery's `bq-{number}@bigquery-encryption…` and Cloud Logging's
  `service-{number}@gcp-sa-logging…` (settings, CMEK settings and unique writer
  identities) resolve the project by ID or number and refuse one that does not
  exist with the service's own error; build creation refuses it too.
- **Compute Engine's default service account is an IAM service account** of
  every project Cloud Resource Manager creates, so Terraform's
  `google_compute_default_service_account` reads it back, and IAM resolves it
  through the `-` wildcard by the number its email carries.
- **The GCE metadata server answers for the workload that asks.** A Compute
  Engine instance is placed by its private address and a Cloud Run container
  by its network namespace's address; the server answers the resource's
  project ID, the number Cloud Resource Manager holds, and the account the
  resource runs as — the one it names, or the project's Compute Engine
  default service account. A read from outside every workload is answered for
  the default project, `sockerless`.
- **Eventarc and Cloud Build are addressed by host.** Both publish
  `/v1/projects/{p}/locations/{l}/triggers`; the CLI and SDK harnesses point
  their endpoint overrides at `eventarc.googleapis.com` and
  `cloudbuild.googleapis.com` and deliver them to the simulator through an HTTP
  proxy, so the `Host` decides every request, a list at `locations/global`
  included.
- **A Cloud Run service instance starts its containers in `dependsOn`
  order**, as a job task does: the first to start owns the network namespace
  and publishes the ingress port, and each other container starts once those
  it depends on have started and passed their startup probes. The Knative
  surface carries the dependencies in the revision template's
  `run.googleapis.com/container-dependencies` annotation (`gcloud run deploy
  --depends-on`), and a create or update whose `dependsOn` names no container
  or forms a cycle answers INVALID_ARGUMENT.
- **A Cloud Storage volume mount writes back as Cloud Storage FUSE does.**
  A Cloud Run job task, service instance, worker pool instance or instance
  binds the bucket's host directory
  (or its `only-dir` directory) into the container, read-only when the volume
  is. While a workload mounts it writable, an inotify watch turns a close
  after writing into a new generation conditioned on the generation the file
  held, a `mkdir` into a placeholder object, an unlink or `rmdir` into a
  delete and a rename into a copy and delete; the simulator's own mirror of
  API writes is staged outside the bucket directories and renamed in, so the
  watch never ingests it. Every Cloud Storage JSON API request first waits
  for the events queued before it, and a task's execution completes only
  after its writes are objects. Linux only; the rest is BUGS.md 3084.
- **A deleted service account is held for IAM's 30 days.** Delete moves the
  account, its keys, its system-managed key and its own IAM policy out of the
  live stores into one held under the account's unique ID with its deletion
  time; get and list no longer see it, and a create under the same email makes
  a new account with a new unique ID. `projects.serviceAccounts.undelete`,
  which gcloud sends to `projects/-/serviceAccounts/{uniqueId}:undelete`,
  restores it with its unique ID while the window lasts and the email is free;
  each delete and undelete purges the accounts whose window has lapsed against
  the clock. Every service-account method accepts the unique ID in the name.
- **Transitional states are served.** Amazon Kinesis Data Streams consumers
  pass through CREATING and DELETING, Client VPN endpoint authorization
  policies through creating, updating and deleting, and Amazon ECR refuses to
  delete a repository holding images without `force`. An Amazon S3 Batch
  Operations job runs as server background work from `New` through
  `Preparing`, `Suspended` or `Ready`, and `Active` to `Complete`, `Failed` or
  `Cancelled`, takes each LambdaInvoke task's outcome from the function's
  `results[]`, writes its completion report, and resumes after a restart; it
  reads its CSV manifest's keys URL-decoded.
- **A NAT gateway route translates the subnets its route table governs**,
  explicitly associated or implicitly through the main route table, recomputed
  on every association change.
- **An Amazon ECR pull-through-cache reference runs its rule's upstream
  image**: the Lambda and ECS hosts resolve `<prefix>/<path>` through the
  registered rule, as ECR hydrates the cache from that upstream.
- **An App Service or Functions site runs its container as App Service
  does**: the image's entrypoint with `siteConfig.appCommandLine` as its
  command, on `WEBSITES_PORT` (80 unset), started by a request or by Always
  On, with every request on the site's hostname forwarded to it; the host
  reads no consumer-named setting and nothing from an image reference's
  spelling.
- **A Linux function app on `Node|22` runs the Azure Functions host** image
  on its deployed content, with the App Service platform environment every
  site container gets; the ARM key operations read and write the host's own
  encrypted secret store — the file store under
  `AzureWebJobsSecretStorageType=files`, and otherwise the
  `azure-webjobs-secrets` blobs of the account `AzureWebJobsStorage` names —
  so with the file store the keys they list are the keys the host accepts,
  and `functionAppStacks` lists the stack. Other function stacks, the host's
  own route to the Blob service, and code-defined functions stay open as
  BUG-3237.
- **Azure Monitor logs land where something names the workspace.** A
  Container Apps environment's `appLogsConfiguration`, a site's Application
  Insights connection, and a data collection rule's Log Analytics destination
  (through the Logs Ingestion API and the rule's `transformKql`) route rows to
  one workspace, whose queries read only its own rows; workspace tables, data
  collection rules and endpoints are served.
- **A web app's SCM site serves Kudu's deployment and WebJobs APIs** at the Repository
  hostname it reports: zip deploy and OneDeploy, authenticated with the
  publishing credentials or a Microsoft Entra token, land the artifact
  through the placement the Azure Resource Manager deployments use, restart
  the site and track its start in deploymentStatus, as the Azure Resource
  Manager MSDeploy and OneDeploy operations do; its WebJobs API lists,
  places, runs, starts, stops and deletes the jobs the Microsoft.Web webjob
  resources read.
- **A Cloud Run service is served at its run.app URL**: a request whose Host
  is the service's `uri` host reaches the ingress container once its startup
  probes (the configured `startupProbe`, or Cloud Run's default TCP probe)
  pass against the container's own address, and gets the container's answer
  untouched. Unless the service is public, the request carries a
  Google-signed ID token for the service whose principal holds
  `run.routes.invoke` through the service's policy or one it inherits,
  conditions evaluated with Common Expression Language.
- **A Cloud Run job task starts its containers in `dependsOn` order**, each
  after its dependencies passed their startup probes, and fails when a
  configured startup probe fails; deleting a job or execution stops its
  running containers, and cancelling a completed execution leaves it as it is.
  A job's `startExecutionToken` or `runExecutionToken` starts the execution
  `<job>-<token>` and holds the job's create or update operation and its
  `Ready` condition until that execution has started or completed.
- **Cloud Run worker pools and instances run their containers.** A worker
  pool runs its manual instance count of container groups through the job
  task's start path (`dependsOn` order, startup probes, Cloud Storage volumes
  with write ingestion); scaling changes start or stop the difference, and an
  update or delete answers once the retired instances have stopped. A Cloud Run
  instance runs through the service-instance path from creation or
  `instances.start` until `instances.stop` or deletion. Their output reaches
  Cloud Logging under `cloud_run_worker_pool` and `cloud_run_instance`. A
  create, update or start holds its operation and the `Ready` condition until
  the instances have passed their startup probes, and fails both with the
  start error; an instance's exits restart it per its `restartPolicy`, up to
  three times in a row; a simulator restart adopts the stored pools' and
  instances' running containers and starts only what is missing, adopts the
  instance serving each service, and lets each running job execution's task
  run on to its outcome.
  An instance's `urls` reach its ingress container through the Cloud Run front
  end, behind the same invoker check a service's URL has.
- **A Cloud Run function is served by its Cloud Run service**:
  `serviceConfig.uri` is the service's run.app URL and `url` the function's
  cloudfunctions.net URL, both served through the Cloud Run front end with
  the container's answer passed through and invocation on both governed by
  the service's IAM policy; DeleteFunction deletes the service and its
  policy.
- **Audited calls write Cloud Audit Logs entries**: Cloud Storage's JSON
  API, Compute Engine, Cloud DNS, Cloud Resource Manager v1 projects and the
  Cloud Run Admin v2, Pub/Sub, Secret Manager, Artifact Registry, Cloud
  Functions v2, Cloud KMS, Cloud Build, Eventarc, Memorystore for Redis,
  Spanner admin, Bigtable admin, Firestore admin and IAM admin APIs write
  Admin Activity entries, and Data Access entries where the project's
  `auditConfigs` enable them, naming the caller its token resolves to, the
  permission the call checked and, for a long-running call, the operation's
  first and last entries; Eventarc `google.cloud.audit.log.v1.written`
  triggers deliver the matching entries as CloudEvents.
- **Azure workload hosts pull with what the workload declared**: a
  Container App's or Job's `registries` entry — a managed identity, as an
  identity token the registry exchanges, or a username and password secret —
  and a site's Azure Container Registry managed identity or
  `DOCKER_REGISTRY_SERVER_*` settings; nothing for an undeclared registry.
- **Workload hosts pull as the cloud pulls**: the Cloud Run job and service
  hosts (which serve Cloud Functions too) present the project's Cloud Run service
  agent to Artifact Registry and Container Registry, and nothing to any other
  registry; the framework carries the credential as the engine's
  `RegistryAuth`.
- **VPC networks** allocate bridge subnets from a host-side pool with the
  workload's elastic network interface address as a real secondary address,
  so same-CIDR VPCs coexist; every simulator resource records its owning
  process, and a slice is reclaimed only from an owner this host can see to
  be gone.
- **Azure network interfaces realize every IP configuration**: each secondary
  holds its own address of the interface's subnet on the realized interface,
  and the simulator tears its realized fabric down on SIGTERM; an interface
  namespace a killed process left behind is reclaimed on the next attach.
- **Declined surfaces are the ones whose required content is somebody else's
  data**: Cloud Spanner's Key Visualizer scans and wire-protocol adapter,
  Cloud KMS Key Access Justifications, Firestore's streaming REST spellings,
  and Amazon SNS SMS and mobile push. Each answers by naming what is missing.
- **Memorystore for Redis** instances and Memorystore for Redis Cluster
  clusters run a real Redis engine, one container per node, at the endpoints
  the API reports; replica-count and shard-count updates reshape the running
  engine, TLS comes from the server CA the API reports, IAM and token auth and
  a cluster's ACL policy are enforced by the engine, a `LIMITED_DATA_LOSS`
  failover refuses a replica more than 30 MB behind, persistence runs as
  configured, and exports, imports, backups and
  cluster import sources move the engine's own RDB snapshots through Cloud
  Storage.

## Gates

Every quality gate has been shown to fail on a planted violation of its own
shape, and one whose scan set can go empty exits non-zero.

- Coverage ratchets with served floors and declared-total locks, per cloud,
  plus the phantom-coverage and unserved-declares-itself tests.
- `check-store-scans.sh` at zero request-path full-store reads, and the three
  object stores typed `sim.PrefixStore`, so no path can read one whole: a
  listing costs its bucket and prefix, not every stored object;
  `check-readonly-locks.sh` and `check-lock-pairing.sh` at zero;
  `check-fake-tests.sh` holding five can't-fail shapes at zero and two at
  floors that may only fall; `check-casefold-slice.sh` and
  `check-locked-helpers.sh` scanning every module including `sim`.
- `simulators-deadcode.sh` per simulator, judging the framework from the three
  programs that link it; `simulators-dupl.sh` and `simulators-jscpd.sh` for
  copy-paste.
- `check-latest-deps.sh` holding Go modules, Terraform providers, GitHub
  Actions, installed tools and the consoles' npm packages to the newest
  release past a 24-hour adoption quarantine, with the repository's own modules
  excluded and covered by the pin gate instead.
- The race detector over every module on every pull request, with zero
  races held by `simGo`/`simAfterFunc`/`simJoinedGo` accounting.
- Spec conformance in every simulator's unit tests, and the runtime
  wire-shape validator over the SDK and CLI suites with allowlists that only
  shrink.
- The required-status-check manifest compared against the workflows in
  pre-commit and against `main`'s live protection at push time.

## Continuous integration

Per-cloud lint and unit tests; the Google Cloud and Azure SDK and CLI suites;
the AWS SDK suite in four shards and CLI suite in sixteen; Terraform in fifteen
shards; console vitest, typecheck, build and Playwright; the race jobs per
simulator and for `sim`; the quality gates; the one-open-pull-request and
rebased-on-main checks; the nightly fuzz workflow across the four Go modules.
Every job holds a fifteen-minute ceiling, and an AWS CLI call that stalls
fails at its own bound with the simulator's in-flight requests, its goroutine
profile and the CLI's `--debug` log. Base images are warmed from one
cache entry per module, read out of the source by `scripts/base-images-for.sh`,
and every suite takes a base image through `testutil/baseimage.Ensure`, which
asks the host before a registry; `build-gates` runs the `testutil` tests, whose
guard fails on a suite that pulls a base image itself. A harness that fronts a
simulator with the Caddy HTTPS gateway starts it through
`testutil/httpsgateway`, which returns once Caddy's log reports a cached
certificate for every name the gateway manages. The Google Cloud SDK suite runs
unsharded; its tests wait on events they can observe — a Cloud Run job's hold
ends when the test writes a release object into a mounted Cloud Storage
bucket — and the waits left are the cloud's or the engine's own, such as
Pub/Sub's ten-second minimum ack deadline.

## Releases

One `vX.Y.Z` tag per release via release-please. The `Release` workflow ships
binaries with consoles embedded for linux/darwin × amd64/arm64, the console
bundles, and per-architecture container images composed into the unsuffixed
manifest list. Release images are immortal; the short-SHA stream is pruned to
twenty. Go consumers pin the release commit, since subdirectory modules cannot
carry a plain tag under this policy.
