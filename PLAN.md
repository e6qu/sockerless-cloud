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

The same pull request landed M3 items 1, 4 and 13 and M4's `sim` duplicates
(marked below), and rebuilt the AWS and Google Cloud consoles on their real
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
- **Scope.** In this pull request: drop the unreleased 2.4.1 entry from
  `CHANGELOG.md` and set `.release-please-manifest.json` back to 2.4.0, so the
  next release is computed from main's own history. Owner actions: remove
  `autorelease: pending` from #284 so release-please stops retrying it; create
  a GitHub App with contents, pull-requests and workflows permissions, install
  it on the repository, and run release-please with its installation token,
  which removes the recurrence.
- **Exit.** After this pull request merges and the label is gone, the next
  push to `main` opens a fresh release pull request, and its release run
  creates the tag, the GitHub Release, the binaries, console bundles and image
  index, with `scripts/verify-release-complete.sh` passing; release-please
  runs with the App's token.
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

### M4 — Cleanups

Intra-module work that makes the code smaller and the gates tighter; no wire
behaviour changes. The Google Cloud cleanups are in progress — the split
registration closures, Memorystore for Redis and Cloud Build on the placement
backend, and waits on the engine instead of a clock; the AWS and Azure
cleanups come next.

- **AWS request decoding and errors.** Every JSON body through
  `sim.ReadJSON`; one error envelope writer parameterised by protocol in
  place of the twenty per-service helpers. *Closes:* BUG-3610.
- **ARN builder and parser** for AWS (about 170 hand-built ARNs and 24
  `*FromARN` parsers) and **tag-set helpers** (the eight tag-merge copies).
- **Azure resource IDs and ARM CRUD.** An ARM resource ID builder in place of
  244 hand-formatted IDs and 158 `*ID()` builders; one generic ARM CRUD shape
  in place of the network and Event Grid variants.
- **Split giant registration closures and files.** `registerIAM`,
  `registerCRMv3`, `registerGCS`, `registerCompute`, `registerARSubresources`
  in Google Cloud; `keyvault.go`, `registerAzureFunctions`,
  `registerAzureFiles`, `registerNetwork` in Azure; `ec2.go`, `glue.go`,
  `ecs.go`, `rds.go` and the functions over 300 lines in AWS.
- **Shared test helpers into `testutil`** and `testutil/githttp` with push
  mode. *Closes:* BUG-3611.
- **Placement everywhere.** Route the workloads that reach the engine
  directly through `placement.Backend`. *Closes:* BUG-3609.
- **Waits on events, not clocks.** Remove the fixed sleeps and retry loops in
  simulator code; Amazon ECS's handle-lookup and archive retry loops already
  went with `sim/signals`. *Closes:* BUG-3607; narrows BUG-3213 and
  BUG-3154.
- **`sim` duplicates in the simulators** — *landed* with M2's front end.
  The AWS store sweeper copy and the hand-rolled Azure Monitor and Cloud
  Logging sweepers run on `kvstore.StartSweeper`; `listq.IntKey` replaced
  `awsIntKey` and `azureIntKey`, and `listq.ColumnPage` replaced
  `gcpColumnPageOf`. `listq.OffsetPage` and `TokenPage` stayed, now used by
  AWS Glue, AWS Budgets and DynamoDB `ExecuteStatement`.
- **Tighten the copy-paste gate stepwise.** `scripts/simulators-dupl.sh`
  holds zero groups at 200 tokens; lower the threshold to 150, then 120,
  clearing the groups each step finds.

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
