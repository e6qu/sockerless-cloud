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

### M2 — Stage 5 library extractions

This pull request carries the five remaining planned extractions. Each moves a
mechanism two or three clouds implement on their own into one `sim` package,
drives every consumer through it, and closes the consumers' rows that the
mechanism caused.

1. **`sim/msgq` completed — queues.**
   - *Scope:* one long-poll waiter (a per-queue change signal plus a timer to
     the next moment the queue changes by itself) replacing `sqsLongPoll`,
     `psPull` and `sbAwaitReceive`; dead-letter routing on exhaustion and
     deduplication through one path (`EnqueueOpts`) for Amazon SQS, Pub/Sub
     and Azure Service Bus; Cloud Tasks as Google Cloud's task queue on
     `msgq`, dispatching HTTP tasks through `sim/delivery`.
   - *Exit:* the three receive paths and Cloud Tasks run on the package; the
     SQS, Pub/Sub, Service Bus and Queue Storage suites pass unchanged;
     Cloud Tasks has SDK, gcloud and Terraform tests.
   - *Closes:* BUG-3605 (Cloud Tasks half).
2. **One host-routed front end in `sim/lbplane`.**
   - *Scope:* the host lookup, forward, client-gone handling, forwarded
     headers and request timeout that the Cloud Run, Cloud Run functions,
     Container Apps ingress, App Service and AWS Lambda function URL front
     ends each implement (`frontend_forward.go` exists three times); Lambda
     function URLs move onto it; each cloud keeps its header builder.
   - *Exit:* every host-routed front end registers through the package and
     the copies are gone.
   - *Closes:* the front-end part of BUG-3127 (API Gateway's execute-api then
     has a front end to serve on); BUG-3242 where the probe moves with it.
3. **`sim/eventbus` — event routing.**
   - *Scope:* fan-out from an event to its matching subscriptions with a
     filter-dialect hook, an input transform, batching, delivery through
     `sim/delivery` with the subscription's retry policy, a dead-letter
     target, and archive and replay. Consumers: Amazon EventBridge rules,
     Amazon SNS (every protocol), Azure Event Grid subscriptions, Eventarc
     triggers and Pub/Sub push. The dialects (AWS event patterns, Event Grid
     advanced filters, Eventarc attribute and path patterns, the Pub/Sub
     filter) stay per cloud behind the hook. Cloud Scheduler joins
     EventBridge Scheduler and Container Apps scheduled jobs on `sim/cron`.
   - *Exit:* the five fan-outs run on the package; Cloud Scheduler has SDK,
     gcloud and Terraform tests.
   - *Closes:* BUG-3604, BUG-3605 (Cloud Scheduler half); narrows BUG-3534
     and BUG-3535 to the target types whose services do not exist.
4. **`sim/jobrun` — run to completion.**
   - *Scope:* an execution of N tasks with parallelism, per-task attempts and
     retries, a timeout, cancellation before or while running, and adoption
     or failure after a restart, reporting counts and per-task outcomes
     through hooks the clouds map onto their conditions, states and
     long-running operations. AWS Batch (attempts, `retryStrategy`, array
     jobs, timeouts) seeds it; Cloud Run jobs, Container Apps jobs and Amazon
     ECS `RunTask` move onto it.
   - *Exit:* Cloud Run jobs run one container group per task with
     `CLOUD_RUN_TASK_INDEX`, `CLOUD_RUN_TASK_COUNT` and
     `CLOUD_RUN_TASK_ATTEMPT`; Container Apps jobs honour parallelism,
     completion count and retry limit and recover after a restart.
   - *Closes:* BUG-3600, BUG-3601; narrows BUG-3173.
5. **`sim/funcrt` — functions.**
   - *Scope:* an execution-environment pool — reuse of an idle environment,
     scale-out to the function's concurrency, reserved concurrency
     (throttling) and provisioned concurrency (warm environments), idle
     retirement — and the event-source poller, with the invoke protocol as
     the hook: the Lambda Runtime API, the Azure Functions host, and the
     Functions Framework contract Cloud Run functions serve through Cloud
     Run.
   - *Exit:* AWS Lambda invokes through the pool; an idle environment is
     reused; `PutFunctionConcurrency` throttles; the Azure Functions and
     Cloud Run functions paths reach the package through their hooks.
   - *Closes:* BUG-3602; the polling half of BUG-3603.

### M3 — New shared libraries

Mechanisms the survey of the three simulators found implemented two or more
times outside `sim`, in order of value over effort. Each item names what it
unifies; this pull request carries those it lands, and the rest stay planned.

1. **Keyed change signal and announcing store** (high value, low effort).
   The `map[key]chan struct{}` wake-up written about nine times —
   `simulator-aws/store_changes.go` and `sqs_wait.go`, `kinesis_streaming.go`,
   EC2 volume hydrations; `simulator-gcp/operations_wait.go`,
   `pubsub_pull.go`, `metadata_wait.go`; `simulator-azure/servicebus_sessions.go`,
   `arm_lro.go` — as one `sim` primitive beside `Store.Generation`. The
   polling tickers move onto it. *Closes:* BUG-3608; the polling half of
   BUG-3603.
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
4. **Probe runner and crash-loop back-off in `sim/replicas`** (medium–high,
   low). Cloud Run's TCP, HTTP and gRPC probes and startup loop
   (`cloudrun_probes.go`) and Container Apps' await and watch loops
   (`containerapps_probes.go`) over `lbplane.ProbeHTTP`/`ProbeTCP`; the
   byte-identical restart back-offs of Container Apps replicas and Cloud Run
   worker pools. Narrows BUG-3311 and BUG-3539.
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
13. **Glob matcher** (low–medium, trivial). The wildcard matchers of Elastic
    Load Balancing rules, the IAM policy simulator, Azure RBAC, Eventarc path
    patterns and AWS event patterns, and their variants.
14. **Idempotency tokens** (low–medium). Client-token handling with request
    digest and window, written ad hoc across about forty AWS files and
    Google Cloud's `requestId`; `msgq.Dedup` is close.

### M4 — Cleanups

Intra-module work that makes the code smaller and the gates tighter; no wire
behaviour changes.

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
  simulator code. *Closes:* BUG-3607; narrows BUG-3213 and BUG-3154.
- **`sim` duplicates in the simulators.** `simulator-aws/store_sweep.go` and
  the hand-rolled sweepers onto `kvstore.StartSweeper`; `awsIntKey` and
  `azureIntKey` and `gcpColumnPageOf` into `listq`; the unused
  `listq.OffsetPage` and `TokenPage` removed.
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
