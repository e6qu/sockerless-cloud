# PLAN

The simulators serve their declared surfaces: every vendored AWS model is
implemented or exempt, and every Google Cloud Discovery method and Azure
Swagger operation reaches a route that serves it or answers a declared 501
naming what is missing. The roadmap below deepens what those routes do —
compute, eventing and queueing services that behave alike across the three
clouds because they share one mechanism — and keeps the releases flowing.
`STATUS.md` is the snapshot, `DO_NEXT.md` the next steps, `WHAT_WE_DID.md` the
decisions behind the code and `BUGS.md` the open bugs.

## Principles that guide the work

- **No host privilege.** The simulator process holds no capability, device,
  privileged port or `nftables` rule, and starts no helper that holds one on
  its behalf; a workload container holds no capability. The container engine
  is the only privileged thing it talks to, as an ordinary client. What needs
  more runs in a node agent the operator chose to deploy.
- **Execution modes.** *Embedded*: one binary per cloud against a Docker or
  Podman engine; every workload, managed engine and network sidecar is a
  container. *Embedded with node agents*: the same, plus node agents —
  containers granted `/dev/kvm` — that boot Firecracker guests for the
  services configured onto them. *Distributed*: control plane and agents on
  separate hosts (milestone M5). `SIM_RUNTIME=process` places no workload.
- **Placement is the simulator's configuration, never the cloud's API.** One
  backend interface in `sim/placement` with a container and a microVM
  implementation; `SIM_PLACEMENT` picks per service, refined by a cloud's own
  attribute (an Amazon ECS launch type). Every workload path goes through it.
- **Networks without privilege.** A VPC, Compute Engine subnetwork or Azure
  virtual network prefix is an engine network over its own range; the cloud's
  resolver and metadata addresses are a sidecar at the address the cloud
  reserves inside the range; NAT is an egress sidecar. Security groups and
  firewalls are enforced on mediated flows and at a guest's tap; the
  container-to-container gap stays recorded.
- **A library holds mechanism, not wire shapes.** A shared package under
  `sim/` states its contract in its package documentation, proves it in unit
  tests without a simulator, and reaches each cloud through hooks; each cloud
  keeps its wire shapes, error envelopes, names and state vocabulary. Each
  consumer keeps its SDK, CLI and Terraform tests, which prove an extraction
  changed no wire behaviour.
- **Services reach each other through their APIs.** `sim.Server.Call` serves
  an in-process request through the whole chain a client's request takes, as
  the service principal `sim.Caller` names or a session of the role the
  service acts as. Every library that delivers to a target takes a hook that
  makes such a call.

## Landed

Stages 1–4 of the execution model (privileged tier removed, unprivileged
networks and sidecars, the placement interface with the container backend and
VM instances as containers, node agents with the microVM backend) and the
first libraries of the service work: the in-process service seams,
`sim/machine`, `sim/workload` groups, `sim/replicas` and `sim/revisions`.

**M2 — stage 5 library extractions.** Five mechanisms that two or three
clouds had each implemented moved into `sim`, with every consumer driven
through them and the consumers' SDK, CLI and Terraform suites showing no wire
change:

1. **`sim/msgq` completed.** One long-poll `Waiter` and one deduplication
   path (`Dedup.Admit`) under Amazon SQS, Pub/Sub and Azure Service Bus;
   Cloud Tasks v2 served on `msgq`, dispatching HTTP tasks through
   `sim/delivery`, with SDK, gcloud and Terraform tests. Dead-letter routing,
   Service Bus sessions and SQS message moves stayed with each cloud. Closed
   BUG-3605.
2. **One host-routed front end.** `lbplane.HostRouter` and `lbplane.Serve`
   under Cloud Run, Cloud Run functions, Container Apps ingress, App Service
   and AWS Lambda function URLs; the three `frontend_forward.go` copies went.
   The container probe runner and the crash-loop back-off moved into
   `sim/replicas` beside it (M3 item 4). BUG-3127 narrowed to API Gateway's
   execute-api, which now has a front end to register on. BUG-3242 stayed
   open: the probe still runs from the simulator, not inside the instance's
   network namespace.
3. **`sim/eventbus`.** Fan-out under Amazon EventBridge, Amazon SNS (every
   protocol), Azure Event Grid, Pub/Sub topics and Eventarc audit-log
   triggers, each cloud's filter dialect behind the hook; Cloud Scheduler v1
   on `sim/cron` and `sim/delivery`. EventBridge Scheduler stayed on `cron`
   and `delivery` directly, because a schedule addresses one target. Closed
   BUG-3604 and BUG-3605; BUG-3534 and BUG-3535 already named only the target
   types whose services do not exist.
4. **`sim/jobrun`.** Cloud Run jobs and Container Apps jobs run task by task
   on it. Amazon ECS `RunTask` and AWS Batch stayed off it. `RunTask(count)`
   starts independent tasks with no retries, no execution parent and no
   completion policy, so the library adds nothing. AWS Batch returns each
   attempt to RUNNABLE to wait for compute-environment vCPUs and fair share,
   and its array children are independent jobs with their own dependencies,
   so the library's start-the-next-attempt-now loop would change Batch's wire
   states; Batch's `evaluateOnExit` has the shape of the library's `Retry`
   hook. Closed BUG-3600 and BUG-3601; narrowed BUG-3173.
5. **`sim/funcrt`.** AWS Lambda runs on it: execution-environment reuse,
   scale-out, reserved and account concurrency, provisioned concurrency, and
   the event-source pollers for Amazon SQS and Amazon Kinesis. Azure Functions
   and Cloud Run functions stayed off it. The Functions host runs as an App
   Service site instance shared by every request, which dispatches and limits
   invocations inside its container and polls its own triggers, and which
   about fifteen App Service features read; there is no per-invocation
   environment, throttle or reset for a pool to own. Cloud Run functions are
   Cloud Run services, whose instances `sim/replicas` already scales by
   request concurrency, minimum and maximum instances and idle scale-in, and
   whose event triggers are Eventarc on `sim/eventbus`. Closed BUG-3602;
   narrowed BUG-3603 to the sources whose services do not exist.

The same pull request landed M3 items 1, 4 and 13 and most of M4 (marked
below), and rebuilt the AWS, Google Cloud and Azure consoles on their real
consoles' patterns.

## Milestones

### M1 — Releases flow again

- **Goal.** A merge to `main` that release-please turns into a release produces
  the `vX.Y.Z` tag, the GitHub Release and every artifact the `Release`
  workflow attaches.
- **Cause.** release-please tags the release pull request's merge commit.
  When a merge that changes `.github/workflows` lands between that merge and
  the release run, the commit to tag carries other workflow files than
  main's head, and GitHub refuses the Actions `GITHUB_TOKEN` — a GitHub App
  token without the `workflows` permission, which it cannot be granted — to
  create a ref there. That stranded the 2.4.1 release pull request (#284) on
  `68af5b1`, which main's replaced history no longer contains.
- **Done.** The unreleased 2.4.1 entry left `CHANGELOG.md`, the manifest went
  back to 2.4.0, and `autorelease: pending` left #284. release-please then
  opened #353 and released v3.0.0 from main's own history. v3.0.0 shipped
  its Linux binaries, console bundles and the Google Cloud and Azure images,
  but no darwin binaries (`sim/fileshare` read Linux-only `Stat_t` fields)
  and no AWS images (the AWS Dockerfile's workspace named the removed
  `realexec` module); its tag holds that code, so the release that follows
  these fixes is the first complete 3.x release.
  CI now builds every simulator for the four release platforms —
  `build-gates` the runner's, the `release platform build` jobs the other
  three — and checks each Dockerfile's workspace.
- **Remaining.** Create a GitHub App with contents, pull-requests and
  workflows permissions, install it on the repository, store its ID and
  private key as Actions secrets, and run release-please with its
  installation token, which removes the workflow-file race.
- **Exit.** A release run creates the tag, the GitHub Release, the binaries,
  console bundles and image index, with `scripts/verify-release-complete.sh`
  passing, and release-please runs with the App's token.
- **Closes.** BUG-3465.

### M3 — New shared libraries

Mechanisms the survey of the three simulators found implemented two or more
times outside `sim`, in order of value over effort. Each item names what it
unifies; this pull request carries those it lands, and the rest stay planned.

1. **Keyed change signal and announcing store** — *landed* as `sim/signals`
   and `sim.Announcing`, closing BUG-3608 (high value, low effort).
   The `map[key]chan struct{}` wake-up written about nine times —
   `simulator-aws/store_changes.go` and `sqs_wait.go`, `kinesis_streaming.go`,
   EC2 volume hydrations; `simulator-gcp/operations_wait.go`,
   `pubsub_pull.go`, `metadata_wait.go`; `simulator-azure/servicebus_sessions.go`,
   `arm_lro.go` — as one `sim` primitive beside `Store.Generation`. Every
   one of them and the polling tickers over the simulators' own stores moved
   onto it; the Kudu log stream keeps Kudu's own watcher cadence.
2. **Long-running operation tracker** (high, medium). Running → done with a
   result or error, work on `sim/bg`, a done signal, `Await` bounded by the
   caller, and restart recovery as a hook (Azure fails, Google Cloud
   re-adopts). Unifies Google Cloud's `operations*.go` and Compute Engine's
   zone operations with Azure's `arm_lro.go` and `compute_vm_lro.go`.
3. **Asymmetric cryptography in `sim/simkms`** (high, low–medium). RSA and EC
   key generation, PSS, PKCS #1 v1.5 and ECDSA signing, OAEP and HMAC, written
   separately in `simulator-aws/kms_crypto.go`, `simulator-gcp/cloudkms.go`
   and `cloudkms_grpc.go`, and `simulator-azure/keyvault.go`, which does not
   use `simkms` at all.
4. **Probe runner and crash-loop back-off in `sim/replicas`** — *landed*
   with M2's front end (medium–high, low). Cloud Run's TCP, HTTP and gRPC
   probes and startup loop (`cloudrun_probes.go`) and Container Apps' await
   and watch loops (`containerapps_probes.go`) over `lbplane.ProbeHTTP`,
   `ProbeTCP` and `ProbeGRPC`; the byte-identical restart back-offs of
   Container Apps replicas and Cloud Run worker pools. BUG-3311 and BUG-3539
   stay open on their own causes: no source confirms the back-off for Cloud
   Run, and a replica's containers share one network-namespace owner.
5. **Time-bounded soft delete** (medium–high, medium). One settle-at-deadline
   mechanism for Azure Key Vault, AWS KMS pending deletion, AWS Secrets
   Manager recovery windows, Cloud Storage soft delete, Cloud KMS version
   destruction, Blob and Files soft delete and the EC2 Recycle Bin.
   *Closes:* BUG-3606.
6. **Build runner** (medium–high, high). The step pipeline, workspace,
   registry credentials, push and cleanup shared by AWS CodeBuild, Cloud
   Build and Azure Container Registry Tasks (`codebuild.go`, `cloudbuild.go`,
   `acr_tasks*.go`). Narrows BUG-3225, BUG-3343, BUG-3570.
7. **Simulated certificate authority `sim/simca`** (medium, low). The
   `x509.CreateCertificate` sites in AWS Certificate Manager, AWS Private CA,
   Amazon RDS, Amazon SNS, Memorystore for Redis, IAM, Key Vault and
   `sim/dbengine`.
8. **Registry ingest and token service** (medium). Image-save ingestion and
   manifest hydration shared by Amazon ECR pull-through cache and Artifact
   Registry remote repositories, extended to Azure Container Registry cache
   rules; one Docker token flow for Artifact Registry and Azure Container
   Registry over `sim/ociauth.go`. *Closes:* BUG-3612.
9. **Field masks and merge patch** (medium, low). The dozen Google Cloud
   `updateMask` implementations and Azure's RFC 7396 merge patch.
10. **Metric time series, aggregation and alarms** (medium). Amazon
    CloudWatch's aggregation and percentiles beside Azure Monitor metrics; a
    base for Cloud Monitoring (BUG-3522) and Azure metric alerts, and the
    autoscalers that read metrics (BUG-3434).
11. **API audit capture middleware** (medium). AWS CloudTrail and Cloud Audit
    Logs capture; a base for the Azure Activity Log. Narrows BUG-3119.
12. **Versioned secrets** (medium). Versions, stages and the random password
    generator shared by AWS Secrets Manager, Systems Manager parameters,
    Secret Manager and Key Vault secrets.
13. **Glob matcher** — *landed* as `sim.Wildcard` (low–medium, trivial).
    The wildcard matchers of Elastic Load Balancing rules, the IAM policy
    simulator, Azure RBAC, Eventarc path patterns, AWS event patterns and
    Amazon EC2 filters run on it. AWS Batch `evaluateOnExit`, Amplify remote
    patterns, CodeBuild report globs and Cloud Storage `matchGlob` keep their
    own, because their syntaxes are segment-aware or richer than `*` and `?`.
14. **Idempotency tokens** (low–medium). Client-token handling with request
    digest and window, written ad hoc across about forty AWS files and
    Google Cloud's `requestId`; `msgq.Dedup` is close.
15. **Managed cache engine `sim/cacheengine`** (high, medium). Memorystore
    for Redis runs a real Redis topology on the placement backend; Amazon
    ElastiCache and Azure Cache for Redis run no engine. Lift Memorystore's
    engine into `sim` beside `sim/dbengine` and put all three caches on it.
    *Closes:* BUG-3732; narrows BUG-3553.

### M4 — Cleanups

Intra-module work that makes the code smaller and the gates tighter; no wire
behaviour changes. This pull request landed every item marked *landed*; the
ARN and tag-set helpers, the rest of placement everywhere and the 120-token
step stay planned.

- **AWS request decoding and errors** — *landed*. Every JSON body goes
  through `sim.ReadJSON`, and `awsWriteError` writes each protocol's envelope
  in place of the twenty-five per-service writers. Closed BUG-3610.
- **ARN builder and parser** for AWS (about 200 hand-built ARNs and 24
  `*FromARN` parsers) and **tag-set helpers** (the eight tag-merge copies).
- **Azure resource IDs and ARM CRUD** — *landed*. One ARM resource ID builder
  and one generic ARM CRUD shape for the network and Event Grid resources.
- **Split giant registration closures and files** — *landed*. `registerIAM`,
  `registerCRMv3`, `registerGCS`, `registerCompute`, `registerARSubresources`
  in Google Cloud; `keyvault.go`, `registerAzureFunctions`,
  `registerAzureFiles`, `registerNetwork` in Azure; `ec2.go`, `glue.go`,
  `ecs.go` and `rds.go` in AWS, where no function now runs past 300 lines.
- **Shared test helpers into `testutil`** — *landed*. `testutil/scratchimage`
  builds the suites' workload images, `testutil/uniquename` names their
  resources, `freeports.TCPUDP` picks a DNS port, and `githttp.StartRepo`
  serves a repository the Azure suites push to; the per-suite copies went.
  `runCLI` and `terraformCmd` stay per suite, because each sets its own
  cloud's CLI environment. Closed BUG-3611.
- **Placement everywhere.** Route the workloads that reach the engine
  directly through `placement.Backend`. Amplify Hosting builds and compute,
  AWS Batch and AWS CodeBuild recovery, Elastic Load Balancing's task
  addresses, Memorystore for Redis, Cloud Build and App Service's process
  kills and configuration reads moved; the sites BUG-3609 names remain.
- **Waits on events, not clocks** — *landed*. Amazon ECS, Amplify Hosting,
  Memorystore for Redis and the Container Apps group wait on workload events;
  `sim.WaitContainerRemoved` and `sim.RemoveVolumeSettled` wait on the
  engine's removal of the container; the node agent's guest init drains its
  console instead of sleeping before the reset. Amazon RDS replication start
  and an Amplify Hosting or App Service port opening raise no event, so those
  waits poll the engine's own status. Closed BUG-3607; AWS Batch service jobs
  stay BUG-3213.
- **`sim` duplicates in the simulators** — *landed* with M2's front end.
  The AWS store sweeper copy and the hand-rolled Azure Monitor and Cloud
  Logging sweepers run on `kvstore.StartSweeper`; `listq.IntKey` replaced
  `awsIntKey` and `azureIntKey`, and `listq.ColumnPage` replaced
  `gcpColumnPageOf`. `listq.OffsetPage` and `TokenPage` stayed, now used by
  AWS Glue, AWS Budgets and DynamoDB `ExecuteStatement`.
- **Tighten the copy-paste gate stepwise.** `scripts/simulators-dupl.sh`
  holds zero groups at 150 tokens (*landed*); at 120 tokens it finds 32
  groups in AWS, 2 in Google Cloud and 5 in Azure, which the next step
  clears before lowering the threshold again.

### M5 — Distributed

Node agents on other hosts, reached over their API with the token minted for
them, and engine networks that span hosts, so a cloud network's range is
placed on an agent that holds no live network over it. *Closes:* BUG-3482;
lifts the container-backend gaps (BUG-3480, BUG-3481, BUG-3483, BUG-3484,
BUG-3486) for services placed on agents.

## Maintenance

Keep the vendored specifications in sync with upstream and serve what a
re-vendor adds, hold the measured floors, keep dependencies current, and close
the open bugs in `BUGS.md` as their blockers lift.
