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
  `ui-auth/` and `testutil/` sit beside it.
- **Shared mechanisms live in `sim`.** Workload-host coordinates, background
  work, workload launching, archive extraction, payload storage and sparse
  files, JWT signing and OpenID Connect federation, registry authentication and
  the managed relational database data plane are one package each under
  `sim/`; the load-balancer data plane is `sim/lbplane`, and queues,
  stream logs, push delivery and cron schedules are `sim/msgq`, `sim/streamlog`, `sim/delivery`
  and `sim/cron`. All three simulators use them. Key material is `sim/simkms`,
  and log entries are `sim/logstore`, which Amazon CloudWatch Logs, Cloud
  Logging and Azure Monitor Logs use; Cloud Logging sinks deliver to log
  buckets, Pub/Sub topics, Cloud Storage buckets and BigQuery datasets. Route 53, Azure DNS and Cloud DNS
  answer their zones over UDP and TCP through `sim/dnsserver`, each cloud
  deciding through its catalog which zones a query sees: Route 53 private
  zones answer the VPCs they are associated with, Azure Private DNS zones the
  virtual networks they are linked to, Cloud DNS private, forwarding and
  peering zones the networks they are visible to, and public zones anyone.
  Azure Private DNS registers the virtual machines of a registration-enabled
  linked network, and Cloud DNS applies the response policies and DNS
  policies bound to a network.
  Route 53 health checks probe their endpoints, and the resolver chooses a
  name's record sets by routing policy and health.
  An AWS Cloud Map DNS namespace owns the Route 53 hosted zone it reports,
  and its instances' records live there, so tasks resolve service names
  through the VPC resolver. An unhealthy instance drops out of DNS answers,
  through a Route 53 health check for a `HealthCheckConfig` service or 30
  seconds after a custom status for a `HealthCheckCustomConfig` one.
  Every workload on an engine network names its network's sidecar
  (`sim/sidecar`) as its DNS server, at the address the cloud reserves for
  its resolver inside the range, and the sidecar forwards to the cloud's
  resolver with the network attached and metadata requests to the simulator
  under a minted token.
- **Execution modes.** `PLAN.md` names three — embedded, embedded with node
  agents, distributed — under one rule: the simulator process holds no host
  privilege, and a workload container holds no capability. The
  host-namespace tier and the two helper containers that held privilege are
  gone with the `realexec` module; a VPC, a Compute Engine subnetwork and an
  Azure virtual network are engine networks over their own CIDR
  (`sim.EnsureCloudNetwork`), and a workload's address is the one the cloud
  API reports for its interface. Where a service's workloads run is the
  placement table `SIM_PLACEMENT` reads (`sim/placement`): the container
  backend by default, and the microVM backend, served by a node agent
  (`sim/nodeagent`) the binary starts as a container with `/dev/kvm`, for the
  services configured onto it. Amazon EC2, Compute Engine and Azure virtual
  machine instances are containers from each cloud's image catalog
  (`sim/vminstance`): the init runs the instance's user data, the instance
  stops and starts on its own root disk, and an image captured from one is
  the committed container. Per cloud: Amazon EC2 instances boot from the
  published Amazon Linux 2023, Ubuntu 24.04 and Debian 12 AMIs or from
  OCI-referenced customer AMIs (`RegisterImage` `ImageLocation`,
  `ImportImage` `Url`), with IMDS through the sidecar, `GetConsoleOutput`,
  `CreateImage` capture and Systems Manager Run Command through the machine;
  Compute Engine instances boot from the `debian-cloud` and `ubuntu-os-cloud`
  families with their startup script on the serial console, machine types
  from a catalog, and images from a stopped instance's disk; Azure virtual
  machines boot from the Canonical and Debian marketplace images or
  `Microsoft.Compute/images`, with `customData`, IMDS through the sidecar,
  deallocate/start on the kept OS disk and capture. Security groups, firewall
  rules and network security groups apply on load balancer flows through
  `lbplane`'s admission hooks.
- **One startup sequence.** Every simulator binary's `main` is a `sim.Main`
  call with a `sim.Program`: the cloud supplies its default port, its build and
  the listeners it serves beside the HTTP port, and `sim` owns the reaper
  invocation, the parent-process watch, configuration, observability, the
  container-engine runtime mode and shutdown.
- **Pins carry the working tree's content.** Each simulator pins `sim` and
  `ui-auth` by pseudo-version;
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
  2,009 of 2,017 served operations; the eight others name no resource and
  authorize against `"*"`. Every condition key the vendored Service References
  declare (653 over 1,917 actions) is resolved by the gate or classified as
  unmodelled with the reason, and per action the gate builds 3,695 of 4,310
  (action, key) pairs on served operations, the other 615 listed with reasons
  in `testdata/iam_condition_key_gaps.tsv` (BUG-2965 holds the 16 that need a
  running workload to seed). A create carrying tags is also authorized as
  its service's tagging action, with `<service>:CreateAction` naming it, for
  the 297 operations the references list one for. The Amazon S3 control plane
  is authorized route by route in the namespace AWS publishes each action under
  (s3, s3express, s3-outposts, s3-object-lambda); DeleteBucketLifecycleConfiguration
  is tested as ungated because no vendored document declares its action. Each
  control-plane resource has one tag set.
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

`WHAT_WE_DID.md` records the rules behind each line below.

### Every cloud

- **Every workload is a real container** on the engine the simulator started
  against, under the cloud product's sandbox profile. Startup refuses to serve
  in any mode that executes workloads when no engine answers, after retrying
  the readiness ping for a budget. `SIM_RUNTIME=process` is API-only and says
  so.
- **A stopped workload gets its cloud's grace** between SIGTERM and SIGKILL:
  an Amazon ECS container definition's `stopTimeout`, Cloud Run's ten seconds,
  a Container App template's `terminationGracePeriodSeconds`, App Service's
  `WEBSITES_CONTAINER_STOP_TIME_LIMIT`, each with the platform's default.
- **Persistent workloads survive a restart.** With `SIM_PERSIST`, shutdown
  leaves the containers running and the next process adopts them by label;
  without it, the detached reaper and the startup sweep collect a run's
  containers, scoped to the state directory so a concurrent suite is never
  touched; a removed container takes its anonymous volumes and leaves its named
  ones. A simulator exits when the process in `SOCKERLESS_PARENT_PID` is gone.
  A stopping simulator waits out no container stop timeout or function
  timeout: interrupted Amazon ECS and AWS Lambda work resumes in the next
  process, and an open long poll, a Live Tail session or a synchronous AWS
  Lambda or Step Functions call ends with the server.
- **Every credential is verified**: SigV4 against the principal's stored
  secret, from the header and from a presigned URL alike; Google Cloud and
  Microsoft Entra bearers against the simulator's signing keys; the Azure
  Storage data plane's Shared Key and shared access signatures against the
  layouts Microsoft's own signers produce; Cosmos DB's shared key on every
  path; each container registry against the credentials its control plane
  mints, with its own challenge shape.
- **Registries answer as their service does**: Amazon ECR's empty ping with no
  content type, Artifact Registry's `text/html`, Azure Container Registry's
  `{}`. ECR hydrates a pull-through cache from the rule's upstream, and the
  Lambda and ECS hosts run a `<prefix>/<path>` reference from it; Artifact
  Registry refuses the second write into an upload session; ACR keys its
  content per registry and serves its catalog on `/v2/_catalog` and
  `/acr/v1/_catalog`.
- **Workload hosts pull as the cloud pulls**: the Cloud Run hosts (which serve
  Cloud Run functions too) present the project's Cloud Run service agent to
  Artifact Registry and Container Registry and nothing elsewhere; Azure hosts
  pull with what the workload declared — a Container App's or job's
  `registries` entry, a site's ACR managed identity or
  `DOCKER_REGISTRY_SERVER_*` settings — and nothing for an undeclared registry.
- **Build services' docker steps run as the build** through a Docker
  configuration built on the host's own (`sim.WriteDockerConfig`) whose
  credential helper answers the build's registries with the build identity's
  token. A Cloud Build step of any builder image runs as a container over the
  build's `/workspace`, and a Cloud Storage source may be a zip archive or a
  gzipped tarball.
- **Object stores arbitrate conditional writes.** Cloud Storage's
  preconditions, Azure Blob Storage's conditional headers and Amazon S3
  conditional writes are evaluated and stored as one step under a per-object
  lock (`sim.KeyedLocks`); a Cloud Storage generation is never reused. All
  three keep each object's contents in a file of its own that the row
  references.
- **Load balancers and platform front ends add the forwarded-request headers
  their documentation states**, after removing the client's hop-by-hop
  headers: the Application Load Balancer (`X-Forwarded-For` per
  `routing.http.xff_header_processing.mode` and `xff_client_port`,
  `X-Forwarded-Proto` and `-Port` from the listener reached, `X-Amzn-Trace-Id`),
  Google Cloud's external Application Load Balancer (`<client-ip>,<lb-ip>`,
  `Via: 1.1 google`, `X-Cloud-Trace-Context`), Application Gateway (its six
  headers before its rewrite rule set, and its server variables), the Cloud Run
  front end (drops client `X-Google-*`), App Service (`IP:port` and
  `X-AppService-Proto`) and Container Apps ingress. Network Load Balancer and
  Azure Load Balancer add nothing. A front end whose target fails after the
  status and headers reached the client closes the stream rather than
  appending an error.
- **VPC networks** allocate bridge subnets from a host-side pool, so same-CIDR
  VPCs coexist; a slice is reclaimed only from an owner this host can see to be
  gone. Azure network interfaces lease an address per IP configuration.
- **Declined surfaces are the ones whose required content is somebody else's
  data**: Cloud Spanner's Key Visualizer scans and wire-protocol adapter, Cloud
  KMS Key Access Justifications, Firestore's streaming REST spellings, and
  Amazon SNS SMS and mobile push. Each answers by naming what is missing.

### AWS

- **Amazon ECS** passes a container definition's `workingDirectory` to the
  engine (and ExecuteCommand inherits it), and RunTask commits each task's
  declared memory and CPU against the simulator's own cgroup or machine,
  refusing one that would not fit with Fargate's capacity message or
  `RESOURCE:MEMORY`/`RESOURCE:CPU` in `failures[]` before any ENI, volume or
  record exists.
- **Amazon DynamoDB PROVISIONED tables spend their throughput** through token
  buckets per table and per global secondary index, refilled at the provisioned
  rate with 300 seconds of burst; an uncovered request gets
  `ProvisionedThroughputExceededException` or comes back unprocessed from a
  batch. On-demand tables are never throttled, and `CreateTable` requires
  capacity units exactly when the billing mode does.
- **Managed databases run real engines** — PostgreSQL 17.11, 16.15 and 16.14,
  MySQL 8.0.46 and MariaDB 11.4.13, each version its release's own image, which
  DescribeDBEngineVersions lists with the upgrades the simulator runs — with
  volumes, credentials sealed under the simulator's key service, readiness
  classified by SQLSTATE, and crash-consistent snapshots. Amazon RDS instances
  and Aurora clusters:
  - record the events the Amazon RDS event table lists as their state
    changes — creation, backups, reboots, stops and starts, snapshots,
    parameter changes, Multi-AZ conversions and failovers, deletion — and
    send each DB instance event to the default Amazon EventBridge bus;
  - take automated backups whether or not a client connects, reporting
    `creating`, `starting` and `backing-up` until `available`, and restore to
    any time in the retention period by replaying PostgreSQL's archived WAL or
    the MySQL or MariaDB binary log (MariaDB to the whole second) onto the daily
    automated snapshot; `DeleteAutomatedBackups=false` retains the backups, and
    an instance's backups replicate to another Region;
  - restore an Aurora cluster from an RDS DB snapshot ARN, and Aurora MySQL or
    RDS for MySQL from a Percona XtraBackup in Amazon S3;
  - refuse a create or restore repeated under an identifier still provisioning,
    as one under a stored identifier;
  - sign in the master user and IAM-authenticated users at the endpoint
    (PostgreSQL roles granted `rds_iam`; MySQL and MariaDB users identified with
    the `AWSAuthenticationPlugin` server plugin the simulator ships) and every
    other user through the engine's own checks;
  - associate `default.<family>` parameter groups whose parameters are the
    catalog each engine reports (`rds_parameter_catalogs_vendored.json`); a
    dynamic change reaches running engines at once and a static one waits for a
    reboot with the group `pending-reboot`;
  - hold ModifyDBInstance changes without ApplyImmediately in
    PendingModifiedValues until a timer for PreferredMaintenanceWindow applies
    them; apply DBPortNumber at once; move record, ARN, endpoint and volume on a
    rename; present a server certificate from the instance's CA, a real CA;
  - run blue/green deployments of a DB instance with a real green instance and
    a switchover bounded by SwitchoverTimeout that keeps the blue as `-old1`;
  - run read replicas on the engine's own replication (MySQL and MariaDB binary
    log replication, PostgreSQL streaming through a physical slot), read-only,
    reporting ReplicaLag each minute, reopened writable by PromoteReadReplica or
    the source's deletion;
  - run a Multi-AZ instance's standby engine in another Availability Zone with
    synchronous replication, converting either way through ModifyDBInstance and
    failing over on RebootDBInstance with ForceFailover, with DescribeEvents
    listing the events;
  - kill an engine still initialising its volume on deletion rather than wait
    out the stop grace.
- **Amazon S3 versions objects and enforces Object Lock.** A versioned bucket
  keeps superseded versions and delete markers, a suspended one writes the
  `null` version, and every reader and writer — GetObject, HeadObject, the
  object subresources, CopyObject's and UploadPartCopy's source, DeleteObjects,
  ListObjectVersions, AWS Lambda's `S3ObjectVersion` — addresses a version by
  id. Legal holds and COMPLIANCE or unbypassed GOVERNANCE retention refuse a
  version delete with AccessDenied; a bucket without Object Lock refuses its
  operations with InvalidRequest. DeleteObjects refuses an entry the caller may
  not delete as that entry's AccessDenied and deletes the rest.
- **Service-owned Amazon S3 buckets belong to AWS.** The buckets AWS Lambda,
  AWS Amplify, AWS Glue and Elastic Load Balancing hand out presigned URLs
  into live in an AWS-owned account: ListBuckets and ListRegionalBuckets omit
  them, every request the caller signs against one answers AccessDenied (and
  CreateBucket BucketAlreadyExists), and the URLs, signed with a credential
  only the simulator process holds, download the objects.
- **Amazon S3 Batch Operations jobs run** as server background work from `New`
  to `Complete`, `Failed` or `Cancelled`, taking LambdaInvoke outcomes from the
  function's `results[]`, writing the completion report, reading CSV manifest
  keys URL-decoded, and resuming after a restart.
- **Elastic Load Balancing**: a trust store reads its CA bundle and revocation
  lists from the Amazon S3 object version named and keeps its own copy, served
  by presigned URL; DescribeLoadBalancerAttributes reports exactly the keys the
  reference lists for the load balancer's type at their defaults, and
  ModifyLoadBalancerAttributes refuses another type's key and checks each
  value; a Gateway Load Balancer's ARN names `loadbalancer/gwy/`. An
  Application Load Balancer publishes `AWS/ApplicationELB` `RequestCount` and
  `RequestCountPerTarget` once a minute, and Application Auto Scaling target
  tracking sets ceil(current × metric ÷ target), so an Amazon ECS service
  scales on the requests its load balancer routes.
- **AWS Glue** Python shell runs log to CloudWatch Logs `/aws-glue/jobs/output`
  and `/aws-glue/jobs/error` under the run ID; GetSystemLogsForJobRun and
  GetSystemLogsForSession answer presigned Amazon S3 URLs.
- **Amazon EC2 instance types and Dedicated Hosts carry the AWS Price List's
  facts** (`ec2_instance_types_vendored.json`, written by
  `scripts/fetch-aws-ec2-instance-types.go` with the offer version and
  SHA-256), which DescribeInstanceTypes, the offerings, Capacity Manager, the
  machine shape a launch reads and future-dated Capacity Reservations read. A
  future-dated Capacity Reservation is `scheduled` until its start date, derived
  on every read, and moves only through an accepted date-change quote within 30
  days of the original.
- **Amazon ECR** sizes an image by the compressed layers plus the config (a
  manifest list by its largest manifest) and refuses to delete a repository
  holding images without `force`.
- **AWS STS sessions carry session tags** from AssumeRole's `Tags`, SAML
  `PrincipalTag:<key>` attributes and the web identity
  `https://aws.amazon.com/tags` claim, each authorized as sts:TagSession;
  transitive tags pass to chained sessions.
- **Amazon SNS logs delivery status** to `sns/<region>/<account>/<topic>`
  and its `/Failure` group through the topic's `<Protocol>…FeedbackRoleArn`
  for Lambda, SQS, Firehose and HTTP/S, sampling successes at
  `…SuccessFeedbackSampleRate`, and publishes `NumberOfMessagesPublished`,
  `PublishSize`, `NumberOfNotificationsDelivered` and
  `NumberOfNotificationsFailed` per topic.
- **Transitional states are served**: Kinesis Data Streams consumers pass
  through CREATING and DELETING, Client VPN endpoint authorization policies
  through creating, updating and deleting.
- **A NAT gateway route translates the subnets its route table governs**,
  explicitly or through the main route table, recomputed on every association
  change.

### Google Cloud

- **Every Discovery document is served** at `GET /$discovery/rest?version=…`
  under its own host (regional and mTLS hosts too), byte-identical to the
  vendored one, and the Discovery directory serves the real directory's entries
  (`simulator-gcp/discovery_directory_vendored.json`), both without a
  credential. A bare address:port answers `v2` with BigQuery's, which `bq`
  builds its client from.
- **Projects and identities agree.** A bucket's project must exist in Cloud
  Resource Manager (`400 Unknown project id` otherwise) and carries its number;
  the Cloud Storage service agent is
  `service-{projectNumber}@gs-project-accounts.iam.gserviceaccount.com`. Cloud
  DNS, Cloud Build's default service account, the Cloud Run service agent,
  Compute Engine, BigQuery and Cloud Logging name identities by the number
  Cloud Resource Manager holds and refuse unknown projects with their own
  error. Every new project has the Compute Engine default service account in
  IAM. A deleted service account is held for IAM's 30 days and undeleted by
  unique ID. Cloud Resource Manager starts holding only the deployment's own
  project, `sockerless`; every test harness creates the project it addresses
  through projects.create.
- **Service Usage reports a never-enabled service DISABLED** through
  services.get and services.batchGet, as the Service state defines it; the
  gcloud suite's harness enables the APIs it drives on the project it creates,
  as a user does. A new project enables no services by default (BUG-3451).
- **List pages resume after the last item returned.** A page token carries the
  last item's sort values under the client's `orderBy` and the list's own
  keys, so creates and deletes between page reads neither repeat nor skip an
  item, over REST, gRPC and the Knative `continue` cursor alike; BigQuery
  `tabledata.list` alone pages by row index. Service Usage's `operations.list`
  answers only the operations Service Usage started, and each
  `projects.locations.operations` collection only its own service's, by host.
- **A bucket carries Cloud Storage's default policy** from creation — the four
  legacy bindings for the project's owners, editors and viewers — so revoking
  what a client granted restores the defaults.
- **Uploads past 5 MiB take the resumable path** from `gcloud storage cp`,
  `gcloud artifacts generic upload`, `gcloud artifacts files upload` and
  `bq load`. BigQuery answers REST errors with its `errors[]` reasons.
- **BigQuery queries run their SQL** in an in-memory SQLite copy of the tables
  they name, and a query job writes its result to its `destinationTable` under
  its create and write dispositions.
- **The GCE metadata server answers for the workload that asks**, placing a
  Compute Engine instance by its private address and a Cloud Run container by
  its namespace's address; a caller outside every workload is answered for the
  default project, `sockerless`.
- **Eventarc and Cloud Build are addressed by host**, since both publish
  `/v1/projects/{p}/locations/{l}/triggers`.
- **Cloud Run** serves a service at its run.app URL once startup probes pass,
  behind a Google-signed ID token whose principal holds `run.routes.invoke`
  (conditions evaluated with Common Expression Language) unless the service is
  public; starts service, job and worker pool containers in `dependsOn` order;
  holds a job's operation and `Ready` condition for its
  `startExecutionToken`/`runExecutionToken` execution; runs worker pools and
  instances (logging under `cloud_run_worker_pool` and `cloud_run_instance`,
  restarting per `restartPolicy` up to three times, serving instance `urls`);
  serves a Cloud Run function through its service at both its run.app and
  cloudfunctions.net URLs; and adopts running workloads across a simulator
  restart.
- **A Cloud Storage volume mount writes back as Cloud Storage FUSE does**:
  inotify turns a close after writing into a conditioned new generation, and
  every JSON API request first waits for the events queued before it. Linux
  only; the rest is BUG-3084.
- **Audited calls write Cloud Audit Logs entries** for Cloud Storage's JSON API,
  Compute Engine, Cloud DNS, Cloud Resource Manager v1 projects and the Cloud
  Run Admin v2, Pub/Sub, Secret Manager, Artifact Registry, Cloud Functions v2,
  Cloud KMS, Cloud Build, Eventarc, Memorystore for Redis, Spanner admin,
  Bigtable admin, Firestore admin and IAM admin APIs — Admin Activity always,
  Data Access where `auditConfigs` enable it — and Eventarc audit-log triggers
  deliver them as CloudEvents.
- **Memorystore for Redis** instances and Redis Cluster clusters run a real
  Redis engine, one container per node, at the endpoints the API reports;
  updates reshape the running engine, TLS comes from the reported server CA,
  IAM and token auth and a cluster's ACL policy are enforced by the engine, a
  `LIMITED_DATA_LOSS` failover refuses a replica more than 30 MB behind,
  persistence runs as configured, and exports, imports and backups move the
  engine's own RDB files through Cloud Storage.

### Azure

- **Azure Resource Manager answers for the resource group first**: a request
  beneath a missing group gets `404 ResourceGroupNotFound`, and a write or
  action beneath a group being deleted gets `409 ResourceGroupBeingDeleted`,
  before any resource provider runs.
- **App Service and Azure Functions sites run their containers as App Service
  does**: a custom image's entrypoint with `siteConfig.appCommandLine` on
  `WEBSITES_PORT` (80 unset), started by a request or Always On; built-in
  `NODE`/`PYTHON` stacks on the platform's images; a Linux `Node|22` function
  app on the Azure Functions host with its secret store (the file store under
  `AzureWebJobsSecretStorageType=files`, otherwise the `azure-webjobs-secrets`
  blobs). Other function stacks, the host's own route to the Blob service and
  code-defined functions stay open as BUG-3237.
- **An app runs as the platform runs it**: one persistent `/home` share,
  connection strings under their type prefixes, a stopped app answering the
  403 stopped-site page, deployment slots running their own containers with
  swap, swap with preview and `slotsdiffs`, and source control deploying the
  repository's head.
- **The SCM site serves Kudu** — zip deploy and OneDeploy with deploymentStatus,
  WebJobs (scheduled by `settings.job`), the VFS over `/home`, the command API
  bounded by `SCM_COMMAND_IDLE_TIMEOUT`, settings and the log stream — at the
  Repository hostname the site reports.
- **Managed identities and Key Vault**: an App Service app, slot, container app
  or Container Apps job gets its own `IDENTITY_HEADER` for `IDENTITY_ENDPOINT`;
  Key Vault references resolve as `keyVaultReferenceIdentity` under access
  policies or Azure RBAC and are re-fetched every 24 hours; Container Apps
  secrets read Key Vault as the named identity; sitecontainer
  `environmentVariables` name app settings; App Service imports certificates as
  its own service principal. The Key Vault data plane verifies the bearer's
  signature, expiry, audience and tenant, applies `publicNetworkAccess` and
  `networkAcls`, and authorizes each operation against access policies
  (compound identities and nested groups included) or Azure RBAC data actions;
  access-policy updates `add`, `replace` and `remove` per permission.
- **Azure Monitor logs land where something names the workspace** — a
  Container Apps environment's `appLogsConfiguration`, a site's Application
  Insights connection, or a data collection rule's Log Analytics destination
  through the Logs Ingestion API and `transformKql` — and a workspace's queries
  read only its own rows.

## Gates

Every quality gate has been shown to fail on a planted violation of its own
shape, and one whose scan set can go empty exits non-zero.

- Coverage ratchets with served floors and declared-total locks, per cloud,
  plus the phantom-coverage and unserved-declares-itself tests.
- `check-store-scans.sh` at zero request-path full-store reads, and the three
  object stores typed `sim.PrefixStore`, so no path can read one whole: a
  listing costs its bucket and prefix, not every stored object;
  `check-readonly-locks.sh` and `check-lock-pairing.sh` at zero;
  `check-fake-tests.sh` holding six can't-fail shapes at zero and three at
  floors that may only fall; `check-casefold-slice.sh` and
  `check-locked-helpers.sh` scanning every module including `sim`.
- `simulators-deadcode.sh` per simulator, judging the framework from the three
  programs that link it; `simulators-dupl.sh` and `simulators-jscpd.sh` for
  copy-paste.
- `check-latest-deps.sh` holding Go modules, Terraform providers, GitHub
  Actions, installed tools, the consoles' npm packages and the digest-pinned
  container images to the newest release past a 24-hour adoption quarantine,
  with the repository's own modules excluded and covered by the pin gate
  instead. Its self-test serves the module proxy, the Terraform registry, the
  GitHub API, Docker Hub and `mirror.gcr.io` from fixtures with fixed
  publication times.
- The race detector over every module on every pull request, with background
  work counted by `bg.Go`, `bg.AfterFunc` and `bg.JoinedGo`.
- Spec conformance in every simulator's unit tests, and the runtime
  wire-shape validator over the SDK and CLI suites with allowlists that only
  shrink.
- The required-status-check manifest compared against the workflows in
  pre-commit and against `main`'s live protection at push time.
- `check-rds-auth-plugin-build.sh` in `build-gates` rebuilds the committed
  Amazon RDS `AWSAuthenticationPlugin` shared objects from their C source with
  their `build.sh` and fails when any committed binary differs.

## Continuous integration

Per-cloud lint and unit tests; the AWS SDK suite in eight shards and its CLI
suite in twenty-three; the Google Cloud SDK suite in two shards and its CLI
suite in one; the Azure SDK and CLI suites in two shards each; Terraform in
twenty-nine shards; console vitest, typecheck, build and Playwright; the race
jobs per simulator and for `sim`; the quality gates; the one-open-pull-request,
rebased-on-main and Conventional Commit title checks; the nightly fuzz workflow
across the four Go modules. A push to `main` whose tree already passed CI on
its pull request skips the suites (`scripts/ci-verified-on-pr.sh`). Jobs
install Go and Bun through the local actions in `.github/actions`, which
download with retries. Every job
holds a fifteen-minute ceiling, and an AWS CLI call that stalls fails at its
own bound with the simulator's in-flight requests, its goroutine profile and
the CLI's `--debug` log. Base images are warmed from one cache entry per
module, read out of the source by `scripts/base-images-for.sh`, and every
suite takes a base image through `testutil/baseimage.Ensure`, which asks the
host before a registry; `build-gates` runs the `testutil` tests, whose guard
fails on a suite that pulls a base image itself. A harness that fronts a
simulator with the Caddy HTTPS gateway starts it through
`testutil/httpsgateway`, which returns once Caddy's log reports a cached
certificate for every name the gateway manages. Tests wait on events they can
observe — a Cloud Run job's hold ends when the test writes a release object
into a mounted Cloud Storage bucket — and the waits left are the cloud's or the
engine's own, such as Pub/Sub's ten-second minimum ack deadline (BUG-3154
lists the rest).

## Releases

One `vX.Y.Z` tag per release via release-please. The `Release` workflow ships
binaries with consoles embedded for linux/darwin × amd64/arm64, the console
bundles, and per-architecture container images composed into the unsuffixed
OCI index. Release images are immortal; the short-SHA stream is pruned to
twenty. Go consumers pin the release commit, since subdirectory modules cannot
carry a plain tag under this policy.
