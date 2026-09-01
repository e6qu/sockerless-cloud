# BUGS

Open: 4. Resolved: 89.

## Open

- **BUG-2963 (a Cloud Build webhook receiver accepts a delivery and starts no
  build):** `webhook`, `regionalWebhook` and the repository webhook receiver
  answer `Empty` and do nothing. `Empty` is what the API returns on success, so
  no response could tell a caller otherwise — the observable gap is that a
  webhook-triggered build never appears. This is not an answer that misreports;
  it is a feature not yet assembled, and assembling it means matching the
  delivery to a trigger, verifying that trigger's secret against the delivery's
  signature, and starting the build the trigger names with the delivery's own
  head commit as its source. Came out of the BUG-2960 handler-state sweep,
  which named it as the one candidate of a different kind and left it.

- **BUG-1702 (CI pulls its base images from registries that rate-limit it):**
  Three jobs failed on 2026-08-31 for the same reason, across two registries:
  `tf (aws)` could not pull `public.ecr.aws/docker/library/busybox` for the
  Amazon ECS pause image, `sim (aws cli glue-iam)` timed out downloading
  `public.ecr.aws/docker/library/python:3.9` inside a job-run budget, and
  `tf (azure subscription)` exhausted four retries on `alpine` with
  "toomanyrequests: Data limit exceeded". Retry-with-backoff is already in
  place in the Azure terraform suite and did not help — the limit is a data
  cap on the runner's anonymous pulls, not a transient blip, so waiting inside
  one job does not recover it and neither does moving between Docker Hub and
  the ECR Public Gallery, which are both throttling the same runners.

  The affected images are few and stable: `alpine`, `busybox`,
  `python:3.9`, and the Lambda runtime images. Fix shape: stop fetching them
  per job. Either cache a `docker save` tarball of the set with `actions/cache`
  keyed on the image list, restoring it before any suite runs, or mirror the
  set into this repository's own GHCR namespace on a schedule and pull from
  there. The second is more work and more robust — a GHCR pull from the
  repository's own package registry is authenticated by the job's token and
  not subject to an anonymous cap.

  Every job that runs a simulator's containers now warms through
  `scripts/warm-base-images.sh`, which loads the set from an `actions/cache`
  tarball and fetches only what the tarball does not hold. The two AWS jobs had
  been the gap — `sim-aws-sdk` and `sim-aws-cli` are separate jobs from the
  `sim` matrix, which holds no AWS entry at all, so a warm added to that matrix
  never reached them. The SDK job's two hand-written pull-with-backoff steps,
  for the DynamoDB oracle and the Batch workload, are the same fetch the script
  performs and were replaced by it; its warm sits after the disk prune, which
  deletes every image on the host.

  `race (simulator-aws shared)` was the next to fail, on `alpine:3.22` for the
  memory tests and `busybox` for the reaper and sweep, and it now warms through
  the same script. Warming alone did not fix it: the reaper and sweep asked
  `ImagePull` for an image the host already held, and a capped host refuses the
  manifest check as readily as the layers, so both now ask `ImageInspect` first
  — the same correction the Azure terraform suite's puller needed. Its image list is read out of the package by
  `scripts/base-images-for.sh` rather than restated in the workflow, and the
  cache key follows that list, so a test that starts pulling something new is
  warmed on its first run instead of missing a stale cache. The `module` shard
  of the same matrix keeps the exposure: its package names the whole Lambda
  runtime table, some thirty images that only an invocation fetches, so the
  same package scan would cache far more than the shard pulls. Distinguishing
  the two needs the image list to come from what a test fetches rather than
  from what its package mentions.

  `tf (aws Amazon RDS snapshot)` failed next, and it named the root of the
  whole class: the simulator itself pulled unconditionally. Capturing an RDS
  snapshot starts a helper container from `alpine:3.22` to copy the volume, and
  `pullImage` asked the registry for that image whether or not the host held
  it, then spent its five backoff attempts on a cap that no amount of waiting
  clears. The snapshot settled `failed` and Terraform reported "unexpected
  state 'failed'". `pullImage` now asks `ImageInspect` first in all three
  simulators, which is what `docker run` itself does — its default pull policy
  is "missing" — and it checks a pinned platform against what the host holds
  rather than assuming it, because the simulator's own architecture is
  routinely not the workload's.

  That failure was also invisible: the simulator's stderr is not in the
  Terraform job's log, so the reason the snapshot failed had to be read out of
  the source rather than the run. Surfacing a simulator-side failure in the job
  that provoked it is worth doing before the next one.

  The scan those jobs share reads the whole simulator tree, and reads more than
  Go. A Terraform suite keeps one Go file per stack in a subdirectory and names
  the workload image in the stack's HCL, so the original flat Go-only scan saw
  neither — it missed the Amazon ECS pause image and the alpine workloads,
  which are the pulls that failed those jobs in the first place.

  The Lambda runtime table was the one source that could not be read literally:
  it maps some thirty runtime identifiers to images, and one arrives only when
  a suite invokes a function on that runtime. `scripts/lambda-runtime-images-for.sh`
  resolves it the other way round, from the identifiers the suites name against
  the table itself, which is four images rather than thirty and stays right
  when a suite starts exercising a fifth. AWS Amplify composed its image tag
  out of the runtime name instead of mapping it, which no scan can read and
  which answered for versions the service does not offer; it now maps the two
  it serves.

  One key per cloud, keyed on the image set itself, so every job that runs that
  cloud's containers shares a single cache entry: the set is fetched once for
  the whole workflow rather than once per job, which is what a cap counted in
  bytes responds to. The AWS set is nineteen images and about 2 GB.

  The simulators also stopped mistaking the cap for the rate limit it is spelled
  like. `toomanyrequests: Data limit exceeded` was classified transient, so a
  capped pull spent five widening backoffs — about two minutes — arriving at the
  same answer, with the reason buried five identical lines above the failure.
  It is classified permanent now and fails at once.

  Warming is a step that primes a cache, not one that checks a dependency, so
  all three are `continue-on-error`. Without that the first run of the sim
  suites failed outright when the cap refused one image while warming, denying
  the run everything else those jobs would have reported; now the job carries
  on and whatever actually needs the image fails at the point of use, naming
  it.

  Until those go through the cache, a job that pulls one of them for the first
  time can still fail for reasons unrelated to the change under test, and a red
  run has to be read before it is believed.


| ID | Sev | Area | Pattern | One-liner |
|----|-----|------|---------|-----------|
| 2909 | P2 | AWS simulator IAM enforcement leaves 230 served operations authorized against `"*"` | the resource-derivation gap BUG-2907 closed for five services is measured across the rest, not closed for them | Thirty services derive their resource from the types AWS declares and the ARN format published beside each — Amazon Data Firehose, AWS Security Token Service and Application Auto Scaling joined the generated table, Amazon EventBridge gained the alias table its Name/Rule abbreviations needed, Amazon DynamoDB reads the export and import family's TableArn, and the state-resolving tail closed — Amazon SQS cancels a message move against the source queue its task record names, AWS Cloud Map resolves GetOperation through the operation record, and AWS CloudTrail reads the ARN-valued ResourceId and ResourceIdList its tagging operations carry — and the per-request cases that predated the table are gone but for AWS Lambda. 1,764 of the 1,994 served operations that authorize against a resource type derive it; the remaining 230 still request a literal `"*"`. The Amazon RDS and Amazon ElastiCache copies authorize both of their ends — the target ARN is name-determined before the resource exists, the AWS Step Functions argument — AWS Glue's usage profiles, connection types, integrations and tagging derive, Amazon EC2's tag operations read each id's type from its prefix, and its route-table, address and network-interface associations resolve to their parents through generation-keyed indexes over the simulator's own state. AWS Budgets joined the table, its Smithy model vendored for the probe, and its three tagging operations derive from the ARN they name. The coverage probe was also sending every member under a lower-cased name — a body no client sends, while the derivation reads the real member name — so it now sends the wire name in its own case, which is what let those three register. AWS Step Functions state-machine and activity creation joined the table — their ARNs are name-determined, so the create request already carries everything the ARN needs, and the older comment calling every create underivable was wrong for them. `TestIAMResourceDerivationCoverage` ratchets the number and prints the per-service remainder, largest first: Amazon EC2 (56), AWS Glue (25), AWS CodeBuild (23), Amazon RDS (22), AWS Identity and Access Management (21), Amazon DynamoDB (18), AWS Systems Manager (16). Amazon ECS fell from 20 to 8 when its daemon and Express Mode families were read from the ARNs they name, type by type. Amazon CloudWatch Logs fell from 31 to 3 when its named families — delivery, delivery destination and source, subscription destination, anomaly detector, lookup table, scheduled query — were assembled from the identifiers their requests carry. What is left is mostly an operation that creates its resource, so carries no identifier for it yet, names something other than the resource it authorizes against, or names it by an ARN in a shape the coverage probe cannot express — those derive for real requests and are pinned by `TestIAMResourceARNs_*` behavior tests; the comment beside `iamDerivationCoverageFloor` states each service's remaining class. | The figures come from `TestIAMResourceDerivationCoverage`, which is the only place they should come from — they had drifted twice — to 1,788 of 1,975, and again to 1,758 of 1,994 while `iamDerivationCoverageFloor` read 1,764 — so read the ratchet, never this row.
| 2932 | P3 | Three AWS Smithy patterns are stricter than the service they describe, so the simulator cannot satisfy both | the vendored model is authoritative for the simulator, but where it contradicts documented service behavior, matching the model would make the simulator less faithful, not more | The runtime pattern check (BUG-2931) reports three responses whose values AWS itself returns. Amazon EventBridge names the managed secret backing a connection `events!connection/<name>/<uuid>`, and `SecretsManagerSecretArn` admits no `!`. AWS Certificate Manager's `DescribeCertificate` reports the issuing authority as an AWS Private Certificate Authority ARN, and the generic `Arn` shape it is typed with requires the service segment to be `acm`. Amazon CloudWatch Logs reports a configuration template's `resourceType` in CloudFormation spelling (`AWS::WAFv2::WebACL`), and `ResourceType` admits no `:`. Each is allowlisted in `simulator-aws/spec-violation-allowlist.txt` against this entry rather than "fixed" by emitting a value the service never emits. The allowlist shrinks if a later model revision widens the patterns, which is the only thing that should close this. Re-read from `aws/aws-sdk-go-v2` main on 2026-08-23 and all three are unchanged: `SecretsManagerSecretArn` is `^arn:aws([a-z]|\-)*:secretsmanager:([a-z]|\d|\-)*:([0-9]{12})?:secret:[\/_+=\.@\-A-Za-z0-9]+$` (no `!`), Amazon CloudWatch Logs' `ResourceType` is `^[\w-_]*$` (no `:`), and AWS Certificate Manager's generic `Arn` still requires the service segment to be literally `acm`. |
| 2646 | P3 | GCP simulator Cloud Run worker-pool scaling | upstream publication lag, not a simulator defect | The Cloud Run v2 `WorkerPoolScaling` members `scalingMode`, `minInstanceCount`, and `maxInstanceCount` are now modelled and covered end to end (SDK wire round-trip, CLI, and a real `hashicorp/google` 7.36.0 Terraform apply → `plan -detailed-exitcode` = 0). What remains open is upstream: the newest live Cloud Run Discovery document (revision 20260814, fetched and checked again on 2026-08-23) and the published REST reference still declare only `manualInstanceCount`, even though gcloud's own generated client and the GA provider both send all four members. The runtime spec validator therefore reports six `unknown-field` keys, allowlisted in `simulator-gcp/spec-violation-allowlist.txt` under this ID. Close this and drop those six entries when Google publishes the members in the Discovery document. |
| 2712 | P2 | AWS simulator outbound delivery protocols | the external carrier and mobile-push providers are unreachable, and every path that would reach one says so | All 42 Amazon SNS operations in the vendored model are served, and everything up to the hand-off is real: subscriptions, attributes, opt-outs, origination numbers, platform applications and device endpoints all behave as the API defines them, and email and email-json subscriptions deliver over real SMTP. Two destinations are not AWS coordinates and cannot be reached from here — SMS needs a telecommunications carrier, and mobile push needs Apple's and Google's own hosts; no AWS API provisions either, so there is nothing faithful to point at. Every path that would reach one now fails with that reason in the message rather than a substitute: publishing to a PhoneNumber had been rejected as a missing TopicArn, which sent a reader hunting a defect in their own request instead of telling them where the simulator stops, and publishing to a device endpoint was rejected the same way. `TestSNS_ExternalDeliveryFailsWithItsOwnReason` holds each failure to naming its own dependency, and holds that a topic publish is unaffected. This stays open as the record of a boundary, not of a defect: close it only if those provider primitives ever become configurable through a faithful AWS API.

- **BUG-56 (action downloads failed during a GitHub incident, and the fan-out
  is the standing risk):** Filed as "the job fan-out throttles GitHub's own
  action download", which the evidence does not support. Every `429` fetching
  an action tarball from codeload landed after 2026-08-17T13:40Z, the minute
  GitHub opened a critical incident (Actions degraded, API degraded, Issues in
  major outage); the run immediately before it had ten failures and not one was
  a throttle, and the run at `94a8e3c` — still forty-six jobs, still fetching
  the same tarballs — went 46 of 46 green. The incident was the cause. What
  remains true, and is why this stays open rather than being struck: the runner
  downloads every action a workflow references before evaluating any step's
  condition, so each of the forty-six jobs fetches every action the workflow
  names, and a step scoped to one cloud costs all of them. That was measured —
  a gcp-only `actions/setup-java` step was fetched by the azure job — and the
  step was replaced by one reading the runtime the runner already ships. Fix
  shape, if the throttling recurs outside an incident: cut the number of
  actions the workflow references, and only then consider a matrix
  `max-parallel`, which trades wall clock on every run against a burst that has
  not been shown to be the problem.

- **BUG-42 (the macOS Terraform harness skips the whole shared azurerm stack):**
  The harness drops to the host user through `setpriv`, stripping
  `CAP_NET_ADMIN` and `CAP_SYS_ADMIN`, so `TestTerraformApplyDestroy` skips on
  every macOS run; adding `--privileged` does not restore them. Running as root
  with `--privileged` gets past the capability gate and then fails booting the
  guest, because the Podman virtual machine exposes no nested virtualisation for
  that path. CI's Linux runner does execute it, so the coverage exists — but no
  local run of that stack means anything, and a green local suite must not be
  read as covering it.



## Resolved history

- ~~**BUG-2960 (a query-protocol answer can invent the row it hands back, and
  the surface tables can already point at every candidate):**
  `PurchaseReservedCacheNodesOffering` accepted any offering id, ignored it,
  and answered with terms of its own — `cache.t3.micro`, `redis`, `0.018` an
  hour — whatever was asked for, and stored nothing, so the reservation it
  reported could never be read back through `DescribeReservedCacheNodes`. It
  was a receipt for something nobody sold. That one is fixed: the offerings a
  read answers with and the offerings a purchase can be made against are the
  same table, a purchase carries that offering's terms, the reservation is
  stored, and an id no offering answers to is refused with
  `ReservedCacheNodesOfferingNotFound`.

  The class is not swept. `scripts/classify-sim-handlers.go` marks the handlers
  it can see answering without reaching state, and the surface tables carry the
  marker: 81 such rows in AWS, 140 in Google Cloud, 93 in Azure. Read it as a
  reading list, not a defect count. The marker understates reaching state by
  design, and its blind spot is a real shape here: a handler that mutates
  through a closure held in a struct field — the Compute Engine disk verbs call
  `disks.setField(key, …)`, which does write — cannot be followed to the store
  and reads as static. Following methods by name was tried and reclassified one
  registration of 341, so it was not kept; resolving the closures needs
  dataflow the marker deliberately does not do. Most are
  honest — a transcription of a published fact (the AWS Lambda runtime images,
  the ElastiCache engine versions, the ELB security policies), a computed echo
  (`TestEventPattern`), or a collection that is genuinely empty because the
  simulator runs no discovery. The ones to find are the other kind: an answer
  that mints an identifier nothing records, or that states a fact — a price, a
  size, a status — the simulator has no basis for. Read each one and decide;
  the marker narrows 4,500 operations to 314 candidates but does not judge
  them. Fix shape per instance: answer from state the simulator holds, or
  refuse the way the service refuses.

  Reading AWS's 81 found one more of the first kind and no others.
  `GetDataQualityModel` answered SUCCEEDED for any profile — a model trained
  and ready to read, which `GetDataQualityModelResult` then contradicted with
  an empty model. Both answer `EntityNotFoundException` now, the error the
  model declares, because a model is trained from statistics this simulator
  does not collect. The rest of AWS's are transcriptions of published facts,
  computed echoes, or collections that are empty because nothing was observed.
  Azure's 93 found three more. Azure Container Registry's
  `checknameavailability` answered `nameAvailable: true` for every name,
  including one this simulator already holds — the operation exists so a client
  can avoid a conflict, and answering it that way makes the create the thing
  that tells the caller the name was taken. It reads the registries now,
  through a name index because a registry is stored under its resource id, and
  refuses a name the document's own pattern rejects.
  `connectedRegistries/{name}/deactivate` answered 200 without looking the
  connected registry up or changing anything, so the read straight after
  contradicted it; it sets the activation status and connection state, and a
  registry that does not exist is not found rather than reported done. And the
  Logic Apps run details — repetitions, scope repetitions, request histories,
  expression traces — answered an empty collection without looking the run up,
  so a caller that mistyped a run name was told the run had no repetitions
  rather than that it named no run. A fourth: unregistering a resource provider
  answered `Unregistered` and stored nothing, so the next read said Registered
  again — an unregister that reverts on the read a client polls. Registration
  is recorded per subscription now, and register clears it. The
  subscription-scoped provider listing built its own answer with the state
  hardcoded, so it disagreed with the single read the moment that read told the
  truth; both go through one function.

  Azure's Cosmos DB data plane was two more of the first kind, with one of the
  second behind them. Reading a database or a container answered 200 for any
  name, so a client was told every name it asked about was already there — and
  the listing beside it enumerates only what exists, so the two contradicted
  each other. Behind that, creating a database recorded nothing: existence was
  inferred from the containers and documents under it, which cannot see a
  database created and not yet filled. The create records it, the reads answer
  for what exists (created on the data plane or through the management plane),
  and the delete takes the record with it.

  Azure Container Registry's `listLogSasUrl` handed out a link to the logs of
  any run id, including one nobody scheduled, and the endpoint that link points
  at answers 404 — so the action reported a log that is not there. It checks
  the run. It does not check the registry resource, because scheduling a run
  does not require one either: the build runs against the registry's login
  server, and `TestACRTasks_ScheduleRunDockerBuild` never creates the ARM
  resource. Whether the tasks family should require it is a separate question
  and is not answered here.

  Cloud Dataflow's `templates:get` answered a fixed "Word Count" template for
  whatever `gcsPath` it was asked about, describing a template nobody staged. A
  template is a file in Cloud Storage and its metadata is the sibling
  `<template>_metadata` Dataflow's own tooling writes beside it — both the
  caller's, both in a bucket this simulator serves. It reads them; a path
  nothing was staged at is not found, and a template staged without metadata
  answers without any rather than with a name invented for it.

  One candidate is a different kind and was left: Cloud Build's three webhook
  receivers answer Empty and start no build. That is what the API returns on
  success, so no response could tell a caller otherwise — the observable gap is
  that a webhook-triggered build never appears. It is a feature not yet
  assembled rather than an answer that misreports, and it needs the delivery to
  be matched to a trigger and its secret verified, which is more than this
  sweep.

  A second sweep ran the other way round — not "what answers without reaching
  state" but "what the model requires and the response omits", which the field
  walk cannot see because it can only look at keys that are there. Both spec
  validators gained a `missing-required` kind. It found four across the whole
  AWS SDK suite and seven across Azure's, all fixed and both suites clean:
  Step Functions' empty diagnostics list, an Amazon ECS capacity provider whose
  auto scaling group ARN an update threw away, an Application Auto Scaling
  target with no role where the service creates a service-linked one, AWS
  Glue's session endpoint auth token, the Azure Container Registry tag listing's
  registry and push times, a Logic Apps workflow version's region, the resource
  id and region of the workspace and component the two metadata documents
  describe, and two API Management contracts that could be stored without what
  they require — a schema with no document and a subscription with no scope.

  A third dimension followed: a value outside the set an enum declares. AWS's
  validator gained an `enum-mismatch` kind and it found seven — a Systems
  Manager inventory type spelled `STRING` for `string`, a CodeBuild batch
  ending on a `COMPLETED` phase a batch does not have, a webhook status of
  `NORMAL`, three optional enum members sent as `""` where the absence is the
  answer, and AWS Glue listing caller-registered connectors in the catalogue of
  the types Glue supports. Azure's enums are mostly `x-ms-enum` with
  `modelAsString: true`, which explicitly permits values outside the list, so
  the check was not added there — 27 closed against 95 open in a 40-document
  sample.

  A fourth dimension — a success status the model does not declare — found one
  defect and confirmed the rest: PUT Object tagging answered 204 where both the
  trait and Amazon S3 say 200. Azure's shards were already clean. Four AWS
  responses disagree with their models because the models are wrong about S3
  (204 for PutBucketPolicy and PutBucketTagging, 202 for a restore it starts);
  those are corrections in `specs/cloud-api/aws/s3.supplement.json` rather than
  allowlist entries, so the code stays checked against what S3 sends. Google
  Cloud has no analogue: a Discovery method declares a response schema and no
  status at all.

  Google Cloud's validator took the enum check, where the surface is largest —
  1,248 Discovery properties declare one — and it found eleven, all fixed: an
  interconnect group status, a delegated prefix's state, three Cloud SQL
  operation types, two Cloud Run export states, an empty build-template status,
  and `ingress` answered as the digits of the proto numeric form rather than
  the name. One family was not a defect: Cloud Run's condition `reason` is
  enum-typed but the enum is incomplete, which gcloud proves — its cancellation
  poller reads `condition["reason"]` and compares it to the literal
  "Cancelled". Those three fields are left unjudged rather than judged wrongly.
  The lesson generalises: a Discovery enum is the best evidence available until
  a real client contradicts it, and then the client wins.

  Google Cloud has no equivalent of the required-member check and should not grow one: a
  Discovery document expresses required-ness only for requests. Its
  `annotations.required` is a list of the method ids a property is required
  *for* — `Route.destRange` carries `["compute.routes.insert"]` — and says
  nothing about what a response guarantees, so a response-side check built on
  it would be measuring the wrong thing. The available analogue is the request
  side, and it is a real one: 63 properties across the corpus are marked
  required for some insert, and nothing checks that the simulator refuses an
  insert that omits one. That is the same defect the two API Management
  contracts had — a resource stored that the service would have refused —
  and it wants a conformance test driving each insert with one property
  removed, not a runtime validator.

  Reading Google Cloud's 140 found four defects and one reason. The four are
  all in Resource Manager v3's tag surfaces and all the same shape:
  `effectiveTags` answered an empty list for a resource whose tag binding the
  simulator holds; a tag-binding collection's PATCH returned the tags it was
  handed and stored nothing, so the GET stayed empty; the effective collection
  reported nothing for either; and a folder capability's read always said
  `false` whatever a PATCH had set. Each answers from what the simulator
  already had — a collection is addressed by its resource's percent-encoded
  name, so the read never has to guess what it is about.

  The reason the rest read as they did: almost all of them
  were the marker's own blind spot. A registrar binds sibling closures —
  `patchAutokey := func(…)`, `load := func(…)` — and the handler reaches state
  only through one of them, which the walker did not follow. It follows them
  now, scoped to the enclosing function so two files' `load` cannot be resolved
  against each other, and the marker fell from 341 registrations to 286: AWS 89,
  Google Cloud 109, Azure 88. What is left of the shape is a closure held in a
  struct field (`disks.setField(key, …)`), which needs dataflow the marker
  deliberately does not do.

  A ✓ still means only that the handler reaches state, never that its answer
  was built from what it read — a handler that looks its parent up and then
  answers a fixed body is marked ✓. The legend says so.

  The sweep is closed. All three clouds' candidate lists were read; the request
  side got the check it was missing, and it found two more defects. A Discovery
  document's `annotations.required` is per method — it lists the method ids a
  property is required *for* — and nothing verified that the simulator refuses
  a request omitting one, which the response validators cannot see because they
  can only judge fields that are there.
  `TestRequestsMissingARequiredPropertyAreRefused` drives all 73 of them with
  the property left out and requires a refusal. Cloud Storage's bucket and
  managed-folder `setIamPolicy` both stored a policy with no bindings, and
  neither they nor their `getIamPolicy` looked the bucket or folder up first —
  a read of a bucket nobody created minted and persisted a default policy for
  it. Both check, and both refuse a policy that grants nobody.

  The probe carries a floor for how much of the corpus it reaches, because 18
  of the 73 answer 404 to a parent the probe does not create: those are
  unjudged, not passing, and counting them as passes is how the gate would go
  quiet. The one candidate that was not part of this class is now BUG-2963.

- ~~**BUG-2955 (a distributed map run does not finish on CI, and the test waits
  sixty seconds for it):**~~ `TestSFNCLI_DistributedMapRun` waited out its whole
  budget with the run still RUNNING, on work that should settle in two seconds.
  The cause was `simGo`. It drops what it is handed once a drain has begun —
  right for work that outlives its request, and fatal for a fan-out the caller
  joins. A Map state ran its workers and its feed through `simGo`, so a drain
  overlapping the start of a map left the feed blocked on a channel nobody read,
  or the collector blocked on a channel nobody closed. Neither is a slow finish:
  the run never completes, which is exactly the shape the earlier investigation
  described and could not place.

  Goroutines the caller joins now go through `simJoinedGo`, which is counted
  like any other work and never dropped. Five more call sites had the same
  shape and are converted: a Task state waiting on its own result, the AWS
  Lambda Runtime API sidecar's `Serve`, the container watch a Lambda invoke
  waits on, and both halves of the Elastic Load Balancing TLS proxy's stream
  copy. `TestSFN_MapCompletesWhileABackgroundDrainIsInProgress` pins it; put
  back on `simGo` it fails in exactly the reported way, a map that never
  returns.

- ~~**BUG-2961 (a Cloud Build cancel test fails only inside the full suite, and
  only sometimes):**~~ `TestSDK_CloudBuild_CancelStopsARunningBuild` failed
  after exactly 30 seconds — its own deadline for the create call to come back
  — with the build already recorded CANCELLED. Reproducing it with the whole
  output kept named the defect: the cancel stopped the client, not the work.
  A build step ran under `exec.CommandContext`, which kills the docker CLI on
  cancel, and `docker buildx` hands the build to buildkit and only tells
  buildkit to stop when the CLI unwinds — which it does on an interrupt, never
  on a kill. Worse, a killed CLI can leave a child holding the write end of the
  output pipe, so `Wait` blocks after the process is gone and the call that
  started the build hangs for the length of the build. Every docker invocation
  a Cloud Build step makes now goes through one helper that interrupts on
  cancel and bounds the unwind with `WaitDelay`, so the engine is told to stop
  and the blocked call returns. Two full verbose suite runs after the fix, plus
  three isolated runs, all passed it.

  The same investigation separated a second cause that had been mistaken for
  the same flake: three unrelated tests failed in one of those runs with
  `Stopping 'podman.service', but its triggering units are still active`. That
  is the local Podman machine going down mid-run, not the simulator — it takes
  out whatever is running at the time, so it looks like a different flake each
  time. A failure that names podman.service is a host condition; read the whole
  output before treating one as a defect.

- ~~**BUG-73 (S3 `WriteGetObjectResponse` is the data plane of a slice that was
  not chosen):**~~ The callback an AWS Lambda transformation function makes to
  return an object through Amazon S3 Object Lambda was the one operation in the
  vendored S3 model without a handler, because the access points it answers
  behind are managed by `s3control`, which was not a vendored slice. The
  simulator now serves the whole loop: `s3control` is vendored, S3 access
  points and Object Lambda access points are real resources, and a GetObject
  addressed to an Object Lambda access point invokes the transformation
  function with a route token and returns what the function posts back through
  `WriteGetObjectResponse` — the stored object is never served directly. The
  s3-control model's other 57 operations came with it: S3 Batch Operations jobs
  that read their manifest out of S3 and apply their operation to every object
  in it, S3 Access Grants (instance, locations, grants, and the credentials
  `GetDataAccess` vends by assuming the location's role), Storage Lens
  configurations and groups, Multi-Region Access Points with their traffic
  dials and asynchronous request tokens, access point scopes, and the regional
  and directory-bucket listings.

- ~~**BUG-1700 (blob soft delete was two settings in the simulator and one in
  Azure):**~~ `Microsoft.Storage/storageAccounts/{a}/blobServices/default`
  `properties.deleteRetentionPolicy` and the data-plane Set Blob Service
  Properties `DeleteRetentionPolicy` are the same setting in Azure — two APIs
  onto one configuration — and here they were independent stores. A client that
  enabled blob soft delete through ARM, which is what
  `azurerm_storage_account`'s `blob_properties.delete_retention_policy` and
  `armstorage.BlobServicesClient.SetServiceProperties` do, got container soft
  delete and no blob soft delete: its deletions were permanent and a
  point-in-time restore had nothing to bring back.

  The policy now lives in one place, the data-plane service-properties document
  that a blob delete consults. The ARM write puts it there and keeps no copy of
  its own; the ARM read renders it from there. That is one configuration with
  two views rather than two stores kept in step, which would have been the same
  divergence with more code. `TestStorageSDK_BlobSoftDeleteThroughARM` drives
  the operator's path end to end — set through ARM, read back on both APIs,
  delete a blob, find it retained and undelete it — and fails without the fix.

- ~~**BUG-2954 (one Application Insights component's billing plan became every
  later component's default):**~~ The billing PUT started from the shared
  default value and decoded the request body into it. Copying the struct gives
  away the backing array of its `CurrentBillingFeatures` slice, and
  `encoding/json` decodes an array into an existing slice in place — so a PUT
  naming the Enterprise plan overwrote `"Basic"` inside the default itself, and
  every component created afterwards started on Enterprise without anyone
  asking. The default is now copied slice and all before any request can reach
  it, on the read path as well as the write. It surfaced as
  `TestAppInsights_BillingFeatures` failing only when the whole suite ran and
  passing alone, which is what cross-request corruption through a shared value
  looks like from the outside.

- ~~**BUG-2953 (capturing an Azure virtual machine was unreachable, because the
  machine's disk did not outlive its guest process):**~~ `VirtualMachines_Capture`
  requires a generalized machine and `VirtualMachines_Generalize` refuses a
  running one — both faithful to Azure — while the capture read `rootfs.ext4`
  out of the live guest's working directory, which stopping the machine
  removed. Generalizing first destroyed the disk the capture needed and
  capturing first was refused for want of generalization, so no order of calls
  reached the operation. The machine's disk now has a lifetime of its own: it is
  copied to a path derived from the resource id before the guest is stopped, the
  capture reads it there and quiesces the guest only when one is running, and a
  deleted machine discards it while a deallocated one keeps it — which is what
  deallocation means in Azure. `TestCompute_VirtualMachinePatchOperations`
  performs the whole deallocate, generalize, capture sequence.

- ~~**BUG-2952 (Azure virtual-machine extension, operation and patch surfaces
  had no Terraform coverage):**~~ One of the three was a real gap and is closed:
  `azurerm_virtual_machine_extension` is in the Azure stack now, so Terraform
  reaches `virtualMachines/{vm}/extensions/{name}` through PUT, GET and DELETE
  alongside the SDK and CLI. The other two were misfiled — the entry claimed the
  provider exposed them without checking which routes they serve. Listing
  machines by location is a read no configuration performs, and `assessPatches`,
  `installPatches` and `capture` are imperative verbs the provider (v5.2.0)
  wraps no resource for; both are recorded as not applicable with that evidence.
  The surfaces were invisible until the tables began resolving routes composed
  from a constant, and the extension surface was itself under-reported at one
  route of five because a constant defined from another constant did not
  resolve.

- ~~**BUG-2956 (Google Cloud rejected its application-monitoring bearer as an
  invalid cloud JWT):**~~ The simulator's global cloud access-token verifier
  wraps its complete published route table. That table also contains
  `GET /monitoring/observation`, whose deployment bearer is deliberately not a
  Google access token, so the verifier rejected the exact configured credential
  before the monitoring handler could authenticate it. The verifier now
  delegates the shared canonical monitoring path to its dedicated bearer
  handler. A final-handler integration test proves the valid monitoring token
  returns `e6qu.monitoring/v2`, an invalid one receives the monitoring realm's
  challenge, and the valid monitoring token still cannot authenticate a Cloud
  Run data-plane request.

- ~~**BUG-2951 (an Amazon ECS deployment reported a rollout COMPLETED while no
  task of it ran):**~~ A deployment reached steady state on a task the watcher
  had not yet reconciled: a task whose essential container has already exited
  reads RUNNING until the exit is observed, and that window satisfied every
  completion condition. Completion is sticky and `ecsRecordServiceTaskFailure`
  ignores a rollout that is not IN_PROGRESS, so the latch was permanent and
  stopped the deployment circuit breaker mid-count — the threshold was never
  reached and the rollback never ran. A deployment the scheduler still holds
  launch failures for no longer completes. The steady-state window is also
  honoured now whatever the second boundary: it is judged against a Unix-second
  `startedAt`, which truncates, so comparing elapsed time against the window
  alone cleared a task a millisecond after it started whenever the start landed
  late in its second. `TestAmazonECSServiceDeploymentFailureStateSurvivesSimulatorRestart_SDK`
  failed about one run in four and now passes 15 of 15.

- ~~**BUG-2934 (Shauth could not observe any simulator application):**~~ All
  three simulator registrations advertised no monitoring coordinate, because
  their binaries served no authenticated application observation. The shared
  `ui-auth` boundary owns a fixed-cardinality `e6qu.monitoring/v2` publisher and
  each simulator supplies its own name, slug, token, session store and process
  evidence; the observation reports real session, runtime, memory and uptime
  figures and fabricates no cloud resource or cost. `SIM_MONITORING_TOKEN` is
  documented beside the other coordinates in each `shared/README.md`. The
  implementation merged in PR #95 and shipped in `v0.26.0`. Filed as BUG-2933
  by the change that fixed it, which was already the number of the orphaned-
  simulator leak below; renumbered here.

- ~~**BUG-2933 (a test binary that died without cleanup leaked its
  simulator):**~~ Every harness starts a simulator as a child and stops it from
  its own cleanup, and each simulator starts a container reaper that polls the
  simulator's PID and removes its containers once it exits. Nothing closed the
  outer loop: a `go test` killed outright never reaches cleanup, so the
  simulator kept running, the reaper kept seeing it alive, and the pair
  survived — seventeen of them, aged two to twelve days across all three
  clouds, were found holding ports and memory on 2026-08-27. The simulator now
  watches the pid in `SOCKERLESS_PARENT_PID` and exits when it goes, which is
  the relationship the reaper already had with the simulator, one level up. The
  variable is explicit rather than inferred from `os.Getppid()`, because a
  guessed parent would end a `nohup`ed run when its shell closed, and the watch
  polls rather than handling a signal, because the ending that matters is the
  one signal a process cannot trap. `TestSimulatorExitsWithItsParent` drives
  the whole chain against a stand-in parent and fails without the watch.

- ~~**BUG-74 (Cloud SQL and the Azure database slices still took metadata-only
  backups):**~~ Both slices now run real database data planes and their
  backups carry the data, the port Amazon RDS's snapshots established. Cloud
  SQL instances serve a real PostgreSQL or MySQL engine at a loopback address
  the simulator owns at the engine's conventional port (the Admin API carries
  no port field), with rootPassword honoured as the built-in admin user's
  KMS-sealed credential (it had been silently dropped, and user passwords had
  been stored in cleartext), API-declared users and databases reconciled into
  the engine as real roles and databases, backupRuns and projects/backups
  capturing the instance volume through `sim.SnapshotVolume` (copy-on-write
  where the volume store allows), restoreBackup restoring in place, clone
  carrying users and data, and the affected operations genuinely
  RUNNING → DONE. Azure PostgreSQL Flexible Servers serve the same
  architecture with administratorLoginPassword stripped from stored
  properties (it had been echoed back on GET, which real ARM never does) and
  sealed under a service-managed key, require_secure_transport enforced ON by
  default, on-demand backups capturing through the LRO, and
  createMode=PointInTimeRestore cloning the newest backup at or before
  pointInTimeUTC — else the live volume — into the new server. Proven end to
  end by stock drivers on both clouds: rows from before the backup present
  after restore, rows from after it absent.

- ~~**BUG-72 (Amazon ECS deployment lifecycle hooks were stored but never
  invoked):**~~ A service's `deploymentConfiguration.lifecycleHooks`
  round-tripped and did nothing, so `ContinueServiceDeployment`'s `hookId` had
  nothing to continue and was the one declared ECS field read and not acted on.
  Implemented rather than approximated: a deployment now walks the lifecycle
  stages `ServiceDeploymentLifecycleStage` declares and stops at the first one a
  hook guards, recording the hook with an identifier and a status the SDK
  deserialises. An `AWS_LAMBDA` hook is invoked through this simulator's own
  Lambda implementation, and a hook whose target does not exist fails the
  deployment rather than passing it. `ContinueServiceDeployment` releases the
  hook its `hookId` names — CONTINUE advances to the next guarded stage,
  ROLLBACK abandons it — and refuses an identifier the deployment does not
  carry or one already resolved.
  The gate is real, which is the point: while a deployment waits at a stage
  before SCALE_UP the scheduler does not launch the new revision's tasks, and
  the test asserts the task list is empty while the hook waits and non-empty
  once it is released. Two things fell out of building it — the
  DescribeServiceDeployments projection emitted neither `lifecycleStage` nor
  `lifecycleHookDetails`, so the state would have been invisible to every
  client, and `TestECS_ServiceDeployments` had been continuing a fabricated
  "hook-1" that only succeeded while the identifier was ignored.

- ~~**BUG-71 (two more Amazon ECS deployment transitions published their state
  before the event recording it):**~~ The same defect as BUG-70, found by
  sweeping the rest of the deployment lifecycle for it rather than waiting for
  another test to lose the race. `ecsFailServiceDeployment` published a
  rollout as `FAILED` in one write to the service row and appended the event
  explaining the failure in the next, and `ecsStartServiceDeploymentRollback`
  restored the previous task definition in one write and recorded "began
  rolling back" in the next. Between either pair a client polling
  DescribeServices sees a failed rollout with nothing saying why, or a service
  pointing back at its last good revision with nothing saying a rollback began
  — neither of which real Amazon ECS can return, because it answers the
  rollout state and the events from one service. Both events are now appended
  by the write that publishes the state they describe.

- ~~**BUG-70 (a rolled-back Amazon ECS deployment reported COMPLETED before the
  event recording the rollback existed):**~~ The scheduler published the
  rollout as `COMPLETED` in one write to the service row and appended the
  `deployment rollback completed` event in another, immediately after. Between
  them a `DescribeServices` call satisfied every condition of a finished
  rollback — the stable task definition restored, running count equal to
  desired, no pending tasks, rollout state `COMPLETED` — while the event that
  records the rollback did not yet exist. Real Amazon ECS answers both from one
  service, so no client can observe that state, and any client polling the API
  could observe it here, not only a test. It surfaced as
  `TestAmazonECSServiceDeploymentFailureStateSurvivesSimulatorRestart_SDK`
  failing on a loaded CI runner while passing everywhere else; the two
  non-restart rollback tests assert the same invariant and had simply been
  winning the race. The event is now appended by the same store write that
  publishes the rollout as `COMPLETED`, so the two cannot be observed apart,
  and the scheduler state that decides it is read before that write rather than
  inside it, so no second store's lock is taken while holding one. Both
  rollback tests now require exactly one such event, which fails if the
  completion path starts appending it separately again.

- ~~**BUG-69 (the drain barrier counted only one of the simulator's two
  background lifecycles):**~~ `AwaitSimulatorBackground` is what lets a test
  replace the package-level stores safely, and every caller is a test doing
  exactly that. It counted work started through `simGo` and through
  `simAfterFunc`, but not work handed to the server's own lifecycle
  (`Server.StartBackground`), which exists so orderly shutdown drains it before
  SQLite closes. Amazon ECS task starts go that way, so a drain could return
  while a task was still moving through its PROVISIONING→RUNNING lifecycle and
  the next test then replaced the control-plane stores it was reading. It
  surfaced as four data races in
  `TestSchedulerStopsTheUnhealthyTaskOnceItsReplacementIsInService` on a CI
  runner and never on a developer machine, because the window is scheduling
  latency. The two lifecycles are both wanted and are not alternatives — one
  drains before the database closes, the other before a test swaps the stores —
  so a finite unit of work now registers with both, through `simTracked`. The
  seven other `StartBackground` callers are lifetime daemons and are
  deliberately not wrapped: counting a loop that only returns on context
  cancellation would make the barrier wait forever.
  `TestAwaitSimulatorBackgroundDrainsServerLifecycleWork` holds the guarantee
  directly, so it no longer depends on a timing window reopening to be noticed.

- ~~**BUG-68 (the `race (simulator-aws shared)` job kept losing its
  runner):**~~ Filed as an infrastructure problem — four pull requests in a row
  lost this one job, three to `The runner has received a shutdown signal` and
  the fourth to its own `-timeout`, always green on re-run — with a fix shape
  of sharding the job to halve the window. That diagnosis was wrong, and that
  fix would have hidden the cause instead of removing it. The job was killing
  its runner: `TestOCIReadBodyRejectsGzipBomb` and
  `TestOCIReadBodyRejectsOversizedIdentity` proved the OCI request-body cap by
  reaching it, and the cap is two gibibytes, so the pair peaked at **7.7 GiB of
  resident memory** under the race detector on a hosted runner that has 7 GiB
  in total. Successful runs sat at 10m1s–10m16s against a 10m30s budget because
  the job was thrashing; the failures were the runs that lost the race with the
  out-of-memory killer. The cap is a parameter now, the two tests assert both
  sides of the boundary at 64 KiB — which the full-size version could never
  afford, so the coverage is better as well as cheaper — and a third pins the
  cap the served path actually applies. The shared suite went from 229s and
  7.7 GiB to 104s and 1.4 GiB. No sharding, and no branch-protection change.

- **BUG-43 (the azurerm provider crashed on the App Service backup path):**
  Captured in full inside the Linux Docker harness — sim, Caddy HTTPS gateway
  and a minimal azurerm 5.1.0 function-app-with-backup stack — after macOS's
  `*.localhost` resolution had blocked every local attempt. The stack is a
  SIGSEGV in the provider's own `FlattenBackupConfig`
  (common_web_app_schema.go:1158), which dereferences the backup schedule's
  start time one line before its nil check. The trigger was the simulator's:
  it served a backup schedule without `startTime`, a document real Azure never
  returns because the service defaults the start time at configuration save.
  The simulator now defaults it the same way, and the same capture found the
  second half: `data.azurerm_storage_account_sas` — the provider's own
  documented shape for `storage_account_url` — emits an *account* SAS, which
  real Azure accepts for backups and the storage plane now verifies over
  azblob's ten-field account layout. The full lifecycle was proven in the
  harness — apply, `plan -detailed-exitcode` clean, destroy — and the reverted
  Terraform leg is restored to the shared stack on a dedicated S1 plan, since
  az_sp is deliberately Y1 and real Azure refuses backups on Consumption.

- **BUG-67 (a prune deleted a publish still in flight):** Fixed by the
  two-hour grace window in `scripts/select-obsolete-container-versions.jq`, and
  proven live: the three publishes the race had killed — `b9d651fb5a1c`,
  `b01a8e29385e`, `418e0c8482f2` — were re-dispatched after the window landed
  on `main` and all three completed, so every commit on `main` now carries its
  immutable image.

- **BUG-41 (the Azure CLI suite's required status contexts had not moved):**
  Done as a repository setting after the sharding merged: `main`'s branch
  protection now requires `sim (azure cli A-M)` and `sim (azure cli N-Z)`, and
  syncing it to the manifest also surfaced and fixed eight Terraform contexts
  `.github/required-status-checks.txt` required that the live setting had
  never enforced. `scripts/check-required-status-checks.sh
  --verify-branch-protection` reports a match.

- **BUG-44 (the storage data plane authorized nothing):** Filed as "the Blob
  data plane authorizes no shared access signature", and it was wider: the
  whole plane verified nothing — `Authorization: SharedKey …` and `sig=` were
  routing signals, a request carrying neither was served anyway, every
  credential the simulator issues was decorative, and the storage CLI tests ran
  on a hardcoded fake key that az signed with faithfully into a void.
  blob_authorization.go now verifies all three credentials on Blob, Files,
  Queues and Tables alike: Shared Key over the documented canonicalization
  against both live key slots (the Table service signs its own shorter string),
  a service Shared Access Signature over the layout its own `sv` defines, and
  anonymous access only where the container's public access level allows it. A
  Microsoft Entra bearer authorizes only with the storage audience, and Get
  User Delegation Key is OAuth-only, as on Azure. Every layout is pinned by
  Microsoft's own signers rather than by this simulator agreeing with itself:
  azblob's SharedKeyCredential and GetSASURL through the App Service backup
  tests, azqueue's and azfile's SignWithSharedKey through their own, and the az
  CLI across the CLI suite. Five defects only those signers could find: a
  service signature has sixteen fields, not the nineteen the combined reference
  reads (`saoid`/`suoid`/`scid` belong to a user delegation signature); the
  string grew with `sv` and a client signs its own version's layout; the Queue
  and File services sign eight and thirteen fields respectively; the signed
  path is the escaped path, which the decoded form corrupts at the first
  `%2F`; and Content-Length must be read from the parsed request because Go's
  server moves it out of the header map. webParseBackupStorageURL now verifies
  the signature it used to only count parameters on.

- **BUG-27 (a virtual network ignored the subnets declared inline on it):**
  The vnet document's `subnets` member was a reference shape with no
  `properties`, so `az network vnet create --subnet-name` had its subnet
  silently dropped and a later read 404ed while the create answered 200. The
  member now embeds full subnet documents, request and response alike, and an
  inline subnet is materialized exactly as its standalone PUT materializes it —
  the same fabric, the same App Service delegation path, the same refusal on a
  host without the netns capabilities. The refusal is the other half of the
  fix: an incapable host now answers the standalone PUT's 503 instead of a 200
  that dropped the subnet, which is what
  TestVirtualNetworkCreatesItsInlineSubnets proves on macOS while the Linux CI
  runner proves the created half.

- **BUG-20 (a killed run's workload containers were collectable by nobody):**
  The reaper is a detached child that waits for the simulator and then collects
  that run, and it dies with the harness container it lives in — so a run killed
  that way left its workloads running. Twenty-two were found on one development
  host, five still running between two and twenty-five hours after the runs that
  made them had ended, which consumes the host continuously and accumulates into
  the engine state that makes `ContainerList(All: true)` fail outright. The next
  simulator over the same state directory now sweeps what the last one left. The
  state directory is the identity that cannot be shared, so the sweep never
  touches a concurrent suite's workloads — CI runs three suites of one cloud at
  once — and Google Cloud and Microsoft Azure gained the state identity Amazon
  Web Services already had. `TestStartupSweepCollectsAKilledRunsWorkloads`
  SIGKILLs a run that has no reaper behind it and requires the next run to
  collect its container and network while a workload under a different state
  directory survives.

- **BUG-22 (the Artifact Registry data plane accepted chunked uploads):**
  Google is explicit — "Artifact Registry doesn't support Docker chunked
  uploads. … You must use monolithic uploads when you push container images to
  Artifact Registry" — and the registry accepted a chunked `PATCH` anyway, with
  a test pinning that acceptance. The chunk is refused with the Docker Registry
  HTTP API v2 code for an operation a registry does not implement; Google
  documents the limitation but not the response, so the code and status are the
  specification's rather than a capture. The consumer was checked in the same
  change and does not rely on the laxity: `backends/core/oci_push.go` uploads a
  blob as POST followed by a PUT carrying the whole body. A real `docker push`
  through the CLI harness still succeeds, which is the same thing that makes it
  work against the live service.

- **BUG-24 (the `/v2/` base endpoint answered a body the registries do not
  send):** All three copies answered `{}`, which is Docker Distribution's
  answer and none of these three registries'. Captured with tokens from each
  service's own token service: Amazon ECR answers 200 with `content-length: 0`
  and no `content-type` at all; Google Artifact Registry answers an empty body
  declared `text/html; charset=UTF-8`; Azure Container Registry answers the
  two-byte body `{}` as `application/json; charset=utf-8`. They disagree, so the
  premise that this was one shared fix was wrong — each cloud's copy answers its
  own service, and each cloud's SDK test pins the captured shape.

- **BUG-35 (an Amazon ECR pull through a cache rule was not hydrated):** The
  registry accepted pull-through cache rules and served them back, then refused
  every pull through one with `NAME_UNKNOWN` — the repository a rule covers does
  not exist until something has been pulled through it, which is the whole of
  what the feature does. A pull for a repository a rule covers now creates that
  repository, from a `PULL_THROUGH_CACHE` creation template when one matches,
  and fetches the image from the rule's upstream registry through the container
  engine the simulator already runs its workloads on. The bytes served are the
  upstream image's own, so a cached image is in the repository exactly as a
  pushed one is and the control plane sees it.

- **BUG-36 (each cloud's test suites built the simulator to one shared path):**
  The AWS and Azure sdk, cli and terraform suites all built to
  `../simulator-<cloud>`, so one suite's `go build -o` could overwrite a binary
  another was executing. Each suite now builds into `.build/<suite>/` as the
  Google suites already did.

- **BUG-37 (the Artifact Registry token service refused no scope):** The live
  service refuses an uncredentialled mint for a repository scope the caller
  cannot reach, with `DENIED` naming the IAM permission and the resource —
  captured for `:pull`, `:push` and `:pull,push`, the last two naming
  `uploadArtifacts`. The mint refuses it too. A scope naming no repository is
  still minted without a credential, which is what lets a client reach the base
  endpoint and find the challenge, and the token it yields still carries an
  identity rather than a permission, so the data plane keeps evaluating access
  per request against the repository addressed.

- **BUG-38 (the shared registry-trust helper left other harnesses
  unconfigured):** The AWS and Google harness makefiles gained the shared
  engine-host temporary directory the Azure one has; without it the engine
  resolves a workload's bind mount on its own host and the workload reads an
  empty directory instead of the files the simulator wrote. The other half of
  this entry did not reproduce: `TestCloudBuild_FaithfulBuildPush` passes, and
  the helper configures Podman's trust for the loopback coordinate while Docker
  trusts loopback natively, so neither engine takes a path that fails.

- **BUG-39 (`x-ms-cosmos-account` was a header the cloud does not have):** The
  Cosmos data plane read its account from a sockerless-invented routing header,
  and from the lexicographically-first account when that was absent. Both are
  gone: an account name is a hostname, the control plane advertises
  `<name>.documents.…` as the account's documentEndpoint, and the data plane
  reads the account out of the host the client dialled and nothing else. A
  request naming no account reaches none, and the data plane's own authorization
  refuses it, because no account's keys could have signed it.

- **BUG-40 (two Cosmos DB accounts could share a name):** The name is a
  hostname, so it is global, and the service publishes an operation whose only
  purpose is to say so — `DatabaseAccounts_CheckNameExists`, which this
  simulator serves. Creating a second account under a name that operation
  reports as taken contradicted it, and is now refused with a 409 naming what is
  unavailable. A PUT to the same resource identifier is still the update it is
  defined to be.

- **BUG-66 (a read lock released with `Unlock` killed the simulator
  mid-suite):** Converting Amazon Lambda's durable executions to read locks
  left four `lambdaDurableMu.Unlock()` calls behind their new `RLock()`.
  `sync.RWMutex` answers that with `fatal error: sync: Unlock of unlocked
  RWMutex`, which no handler can recover: the first `GetDurableExecution` to
  arrive took the whole process down, and the `aws sdk services-a-m` job
  reported not one failure but every test after it, each unable to connect to a
  port nothing was listening on any more. Neither the compiler nor `go vet`
  sees a mismatch — both calls are real methods on a real receiver — so
  `scripts/check-lock-pairing.{go,sh}` reads it out of the syntax tree instead,
  at a floor of zero in pre-commit and CI. It reported exactly those four and
  nothing else, and stays silent on the release-to-upgrade pattern that has all
  four calls.

- **BUG-65 (asynchronous work outran the tests that started it, and the shared
  server built its handler chain twice):** The first race-detector run of these
  suites reported 144 races in `simulator-aws`; the count is zero now, and the
  `race (simulator-*)` CI job holds all three modules there.

  Most of the 144 were one cause: asynchronous simulator work —
  reconciliations, builds, deployments, certificate validation — ran in
  goroutines nothing tracked, so work still in flight when its test ended read
  package-level stores the next test was replacing. `simGo` counts those, and
  `AwaitSimulatorBackground` drains to quiescence rather than waiting once,
  because the work chains.

  The residue had three causes the earlier count hid, and none was the
  long-lived worker this entry previously guessed at. Amazon ECS defers three
  reconciliations with `time.AfterFunc`, which registers nothing until it
  fires: between the schedule and the fire a drain sees quiescence, the stores
  are replaced, and the timer then wakes into the next test's. `simAfterFunc`
  counts a pending timer from the moment it is scheduled, and a drain is a
  barrier: it stops the timers that have not fired and drops the work requested
  while it runs, rather than waiting either out. The barrier is load-bearing
  rather than an optimisation — a reconciliation requests another whenever it
  moves a task, so a drain that kept admitting them waited on a group that
  refilled itself. On a developer machine that chain converged in microseconds;
  under the race detector on a CI runner it did not, and the job was killed with
  a reconciliation still runnable after eight minutes. The other twelve were in
  `shared.(*Server).finalHandler`, which built the outermost handler chain on
  first use and cached it in a plain field: two concurrent first requests both
  saw nil, both built a chain, and both wrote it. That one is a live defect
  rather than a test artefact — a real deployment's first two requests race
  identically — and it was fixed in all three clouds' copies.

  The last two were `realexec.TCPProxy.Close`, which waited for its accept loop
  and not for the handlers that loop had already spawned. It therefore returned
  while a handler was still calling the caller's target resolver — which for an
  Elastic Load Balancing stream listener reads the load-balancer and
  target-group stores — so a test that closed its proxy and moved on raced its
  own teardown. Also a live defect rather than a test artefact: any caller that
  closes a proxy and then tears down what it proxies to has the same race.
  Close now closes in-flight connections and waits for every handler, and two
  tests pin both halves — that it blocks while a handler is mid-resolve, and
  that it does not wait out an idle stream, since a proxied stream is meant to
  last for hours.


- **BUG-64 (background goroutines outlived their tests and raced the next
  one):** Running the suites under the race detector for the first time — CI
  never has — reported 144 races in the AWS module and intermittent ones in
  Azure. One cause, in two shapes. A simulator built by a test and never served
  still starts its background workers, and `StopBackground` exists precisely
  for that but no test called it, so an Elastic Load Balancing health checker
  kept sweeping package-level stores while the next test rebuilt them. In
  Azure, long-running operations and subscription-alias provisioning complete
  in bare goroutines that nothing waited for, with the same result. The AWS
  test builders stop their workers now, the Azure completions are counted in a
  wait group the test builders drain, and the AWS count fell from 144 to 103
  with Azure clean across four consecutive runs. The remaining 103 are
  pre-existing and untouched by this branch — measured against the merge base,
  not assumed — and none is in the paths converted here.

- **BUG-63 (one lock serialised every DynamoDB operation):** Reported as issue
  #43, the shared cause under #37 and #39: `ddbItemsMu` was a plain mutex, so
  reads excluded each other and a workspace create fanning out into a few dozen
  GetItem and UpdateItem calls served them one at a time — single-item reads
  that are O(1) by any measure taking thirteen seconds. It is a read-write lock
  now, with the contract written where it is declared: a section that only
  reads takes RLock; a section that writes, or reads and then writes based on
  what it read, takes Lock for the whole span, which is what keeps a
  read-modify-write atomic; neither is reentrant. Every one of its thirteen
  sites was classified before the change — four pure reads, nine writers
  including the three PartiQL paths that explicitly need the whole operation.
  Measured rather than timed, because a duration only says "fast today": peak
  concurrent readers inside the lock went from 1 of 16 to 16 of 16, and a
  writer still excludes everyone.

  The shape is now counted rather than waited for, and the count is zero.
  `scripts/check-readonly-locks.sh` reports critical sections that hold an
  exclusive lock while only reading a store, and every one in the tree was
  converted: the Glue catalog's twelve read handlers, Lambda's four
  durable-execution reads, the ECS revision index, and all three clouds'
  real-execution fabric maps. Each declaration carries the contract, each
  conversion left every writing site on Lock, and the detector is held at zero
  rather than at a floor.

- **BUG-62 (three parsers accepted input the services cannot produce, found by
  the nightly fuzz run):** The first nightly run after the repaired fuzz targets
  reached real code reported three, each a parser that answered rather than
  refused. Cloud KMS read `/cryptoKeyVersions/0000000000000000001` as version 1
  because `strconv.Atoi` accepts leading zeros and a leading sign, so a resource
  with exactly one name had several and a caller could read a version it never
  named; the segment must now be the number, not merely parse to it. BigQuery
  trimmed backticks from the ends of a whole reference and left any inside it,
  so `0.` + "`0" parsed to a table literally named "`0" — an identifier BigQuery
  cannot have, addressing a table that can never exist while looking like a
  reference that parsed; a backtick inside a component is now refused. And the
  Service Bus AMQP frame reader returned its partly filled buffer beside the
  error when a body arrived short, handing a caller that checked only one of the
  two the size the peer claimed, zero-padded — 3,157,808 bytes in the reported
  case, on a pre-authentication path where the peer chooses both the size and
  where to stop sending; a frame that did not arrive whole now comes back nil.
  Each failing input is a seed now, so ordinary `go test` catches a regression
  without waiting for a nightly.

- **BUG-61 (a Query read the whole table, item by item, under one lock):**
  Reported as issue #37: an authenticated page timed out with forty-four
  concurrent `Query` requests in flight, each over a minute old and the count
  climbing rather than draining, ninety-one goroutines blocked on the same
  mutex inside `ddbItemSnapshot`. Two costs, both now gone. The scan took the
  process-wide item lock once per candidate and deep copied each candidate
  through JSON before deciding it did not match, so concurrent queries
  interleaved item by item; the scan now holds the lock once and copies only
  what it returns. And a query examined every item in the table, when DynamoDB
  requires the key condition to fix the partition key with an equality — the
  items a query can return are one contiguous run of the sorted key space. The
  partition is read out of the compiled key condition, so an aliased name and a
  reversed comparison narrow the same way, and a condition that does not fix a
  partition still examines the full set and answers identically. Measured on
  one machine against the database-backed store the simulator runs on, forty
  concurrent queriers over a two-thousand-item table in forty partitions: 8.02s
  before, 0.43s after. A counting store proves the narrowing is wired into the
  query rather than merely available to it — 600 of 600 items read before, at
  most 30 after.

- **BUG-59 (a soak test starved the writers it was racing):** The store soak
  ran twenty-four readers spinning without pause against twenty-four writers,
  so on a machine with fewer cores than the reader pool the readers held every
  processor and the writers crawled. Measured: twelve seconds at full
  parallelism, thirty at four processors, forty-six at two — and on a
  four-processor continuous-integration runner it ran two minutes thirty-seven
  without finishing and took the whole module's five-and-a-half-minute budget
  down with it, as a package-wide timeout panic naming a test that was merely
  slow. The reader pool is now sized to the machine and yields between passes,
  which keeps the hazard it exists for — a Filter result racing a concurrent
  Delete — while leaving the writers somewhere to run: three seconds at four
  processors, two at two, and the module's whole unit-test step in eighty-four.

- **BUG-60 (a degraded package mirror failed jobs that needed nothing from
  it):** Two jobs of one run died in `apt-get update`, each after three honest
  retries over ten minutes, while installing seven packages of which five ship
  on the runner image. The index is now refreshed only when something is
  actually missing, so a degraded mirror cannot fail a job with no question to
  ask of it. A genuinely unavailable package still fails loudly; the retries
  and the loud failure are unchanged.

- **BUG-58 (a job deleted the shared Go caches and saved the emptiness):** The
  AWS SDK job freed disk before its timed run by deleting the Go build and
  module caches, and `actions/setup-go` saves at post-job, so the run stored
  what was left — nothing — under the key every Go job in the workflow shares.
  A branch's first run is not an exact hit in its own scope, which is what
  makes it save. Measured: the entry under
  `setup-go-…-go-1.25.12-69c66a05…` was 224,164,847 bytes on the reference
  branch and 7,564 bytes on `refs/heads/main`, written the minute the previous
  pull request merged; every branch cut afterwards inherited the empty one and
  built cold, and the pre-build step that takes 3m16s warm hit its six-minute
  ceiling twice. The deletion was buying 6 GB on a 145 GB disk that was 43%
  used, so it no longer touches the Go caches — the container images and apt
  lists are the part worth reclaiming — and the pre-build budget now fits the
  cold build it exists to absorb. The poisoned entry was deleted so the next
  run on the default branch writes a real one.

- **BUG-45 (the Azure SDK's environment lifecycle pagers panicked):** Closed by
  changing the simulator, which this entry had concluded was impossible. Suspend,
  resume and change-virtual-network are declared long-running with their final
  state via Location and with 202 a documented response, so answering the
  collection synchronously was the divergence, not the client's problem: on a
  synchronous answer azcore selects its no-op poller, whose result field is a
  zero value of the poller's type parameter — here a pager — so it allocates a
  fresh pager with a nil handler and assigns it over the one the client built,
  and every read panics. The three now answer 202 and record the collection as
  the operation's result, which the Location poll serves. The generated client
  reads its own pager, which the suite now drains rather than discarding —
  taking the first page directly, since a long-running operation's pager is
  created already holding it and `More` consults that page's `nextLink`, so the
  `More` loop Microsoft's own example uses yields nothing for a single-page
  result. That was the second defect this entry recorded, and it is a client
  idiom rather than a simulator divergence. The
  earlier reasoning — that manufacturing an accepted response for finished work
  would be a fake completion signal — had it backwards: the service answers 202
  here, the specification documents it, and the operation completes for real
  behind it.

- **BUG-57 (AttachPolicy accepted a policy type the root had not enabled):**
  Attaching a policy now requires its type to be enabled in the root, which is
  what makes a policy govern a target at all — service control policies are
  enabled in every root, and every other type has to be enabled first. Stored
  without that check, a tag policy nobody enabled resolved through
  DescribeEffectivePolicy as though it governed the target, a policy decision
  the organization never made. Both suites enable the type before attaching and
  assert the refusal before that.

- **BUG-54 (the ECS reconciler bypassed the deregistration delay):** The Amazon
  ECS service reconciler now deregisters a target-group target the way the API
  does — marking it draining and letting the target group's deregistration
  delay run — rather than removing it the moment its task stops, so a scaled-in
  task leaves the target group the way Elastic Load Balancing makes one leave
  it. A task running again at a draining address cancels the drain, as
  registering that target would, and a zero delay still completes at once. Both
  paths were observed failing against the unfixed reconciler.

- **BUG-55 (AWS provider pins were checked by major version only):** An exact
  Terraform provider pin is now compared exactly: it names the one version
  Terraform may install, so it is behind the moment a newer one clears the
  adoption quarantine. A constraint carrying an operator admits newer versions
  by itself and keeps the major-only comparison. Ten `hashicorp/aws` pins were
  ten minor versions behind and silent; they moved to 6.60.0 in the same pass.

- **BUG-48 (three Elastic Load Balancing target-health gaps):** A target group
  no listener rule forwards to now reports its targets unused rather than being
  health-checked, as the service requires before it checks anything; the
  configured matcher grades the response code and a mismatch names it; and a
  deregistering target drains for the configured delay instead of vanishing.
  Found beside them: an HTTPS health check was only a connection attempt, so a
  target answering an error over HTTPS reported healthy.

- **BUG-49 (the simulator could start without a container client):** The filed
  mechanism was wrong, and proving it wrong found the real one. Container mode
  already refused to start against a missing, hanging or unhealthy engine — all
  three verified against the unfixed binary. The reachable path was the
  process runtime, which the engine-down message itself recommends: following
  that advice produced a simulator reporting itself healthy, accepting work, and
  failing it later in the background. Startup now refuses for any mode that
  executes workloads, health no longer claims an execution capability the
  process lacks, and the engine constructor returns an error instead of exiting,
  which is what made any of this testable.

- **BUG-50 (CodeBuild invented test and coverage data):** Reports are read from
  the files the buildspec declares, out of the build container. Four formats are
  ingested and the seven other documented ones are refused by name, so partial
  support is loud rather than a silent fabrication. A build declaring no reports
  produces none, and a pattern matching nothing produces the incomplete report
  the service documents.

- **BUG-51 (two workloads ran as host processes):** The CodeBuild command and
  the Glue Python job run in containers, with the platform derived from the
  image manifest like every other launcher here. With the last callers gone the
  process substrate was unreachable and has been removed, and the dispatch gate
  that failed to notice either of them now walks the whole module.

- **BUG-52 (the derivation floor counted what it never measured):** 1,788 became
  **1,687**. One hundred and one operations across five services were credited by
  table membership while absent from the probe's own switch. The floor going
  down is the honest outcome and the comment says so: no derivation was lost,
  the number stopped crediting derivation nobody measured. The condition-key
  ratchet was the same shape — twenty-four hand-written booleans no code had to
  agree with — and probing them properly showed three keys never reach the
  request path at all.

- **BUG-53 (a surface with no reachable success path):** The vendored model has
  no API that creates an iterable form, because it is the catalog object's own
  repeating structure: a table asset's columns. It is derived from the table the
  asset names, with item attachments and glossary terms merged on, so the
  operations can succeed.

- **BUG-47 (DescribeTargetHealth probed every target on the request path):**
  The read paid a full health-check timeout for each unresponsive target —
  measured at 5.001 seconds against one, now 114 microseconds. No checker
  existed at all: the describe, the data plane's listener lookup and the Amazon
  ECS scheduler each probed inline. A real continuous checker now runs under
  the server lifecycle, checking each target on its own group's configured
  interval with the documented threshold state machine, and all three consumers
  read what it recorded. The read reports the states and reason codes the
  service documents, including the initial state before a first check completes,
  and the health-check port is honoured rather than ignored while being echoed
  back.

- **BUG-46 (a failed health check reverted a completed deployment):** A
  completed deployment is terminal, as the service documents — there is no
  documented edge back to in-progress — and the omission behind it is closed
  too: the scheduler now replaces an unhealthy task rather than merely
  reopening the rollout, starting the replacement first and stopping the
  unhealthy one once it is in service, or one at a time when the maximum
  percentage leaves no room. The initial state is not treated as unhealthy. One
  dependency surfaced on the way: a new deployment had been marked completed
  from the previous revision's counts, so with a terminal completed state the
  circuit-breaker and alarm rollbacks would never have fired; deployments now
  start in progress as documented.

- **BUG-26 (the Azure Cosmos DB data plane authenticated nothing):** A
  middleware verifies the shared-key token on every data-plane path, so a new
  route cannot skip it. The canonicalisation is Microsoft's published one —
  verb, resource type, resource link and date newline-joined with the trailing
  blank line the documentation calls out, verb and type and date lowercased,
  resource names case-sensitive — pinned by a unit test against Microsoft's own
  published encoding vector. Offers are the documented exception, signing the
  lowercased resource identifier only. All four keys authorize reads and only
  the read-write pair authorizes writes, a query POST counting as a read.
  Resource tokens and Entra tokens are refused rather than accepted unchecked,
  because Microsoft publishes the resource token's shape but not its
  construction. Every Cosmos test now provisions through ARM and signs with
  real key material, so the resource-move proof that previously could not
  demonstrate a working credential now does.

- **BUG-28 (a resource move onto an occupied identifier overwrote it):** The
  move refuses the collision. The shape is as attested as it can be and no
  further: nothing published states the constraint, but the reference does
  state that validation answers 409 with an error message, and one real failed
  move in Microsoft's own support corpus supplies both the code
  `ResourceMoveProviderValidationFailed` and the sentence naming resources
  "which have the same name as a resource in the target resource group". None
  of the plausible-sounding codes searched for exists anywhere. The nested
  detail therefore carries the attested sentence with no leaf code, because
  inventing one would put a code on the wire no client could ever have seen.

- **BUG-30 (a Logic Apps callback URL embedded the resource group):** A
  workflow is issued a 32-hex identifier at creation, preserved across updates
  and carried through a move, and its access endpoint is built from that rather
  than from the resource identifier — matching the real shape, whose published
  example response is generated for a named resource group and contains neither
  the group, the subscription nor the workflow name. A callback URL issued
  before a move is byte-identical after it. Stated as inference rather than
  fact: whether the real identifier itself survives a move is not published.

- **BUG-34 (three Microsoft Azure SDK tests failed on this host):** Two root
  causes, both measured rather than reasoned about. The registry test failed
  because the shared trust helper was a **no-op** inside the Linux harness, on
  the false premise that the engine already treats loopback registries as
  insecure; a bare push reproduced the exact reported error, and writing the
  configuration made it succeed. Two further measurements shaped the repair:
  the engine parses its registry configuration once per API-service lifetime,
  and the harness pins that service for its whole run, so the configuration can
  never be reloaded mid-run — while the per-registry certificate directory is
  read per operation. Trust is therefore installed as a certificate authority
  through real channels, the insecure-HTTP path no longer silently no-ops
  anywhere, and where it cannot take effect it fails loudly instead. The other
  two tests were one cause: the simulator handed the engine bind-mount sources
  that the engine resolves on its own host rather than inside the harness
  container, so every workload mounted an empty directory — the harness now
  shares one engine-host directory as the simulator's temporary root. Its path
  is deliberately short, because a longer one overflowed the Firecracker API
  socket's `SUN_LEN` limit and broke all nine Compute tests.

- **BUG-21 (`generateAccessToken` ignored the lifetime it was asked for):** The
  method honours the requested lifetime, and the rule turned out to be
  narrower than "up to twelve hours": the discovery document says the maximum
  is one hour by default, and twelve only for a service account allowed by an
  Organization Policy enforcing
  `constraints/iam.allowServiceAccountCredentialLifetimeExtension`, with
  43200 seconds the absolute ceiling. The simulator already modelled
  Organization Policy, so the constraint joined the catalog with Google's own
  description and a list-constraint evaluator beside the existing boolean one,
  defaulting to deny — an allow default would silently grant every account
  twelve hours and contradict the documented behaviour. Doing so exposed an
  over-broad existing assertion that every catalog entry defaults to allow.
  The Artifact Registry expired-credential case moved onto the real API path
  as a result: a token is minted with a one-second lifetime through the real
  method and presented after it expires, instead of being forged in-package.
  Unattested: Google publishes no verbatim message for an over-long lifetime
  and none could be captured, since authentication precedes argument
  validation; the status and canonical code are grounded, the wording is not.

- **BUG-23 (Artifact Registry invented a location):** The location is no longer
  manufactured. Checked against the live service rather than reasoned about:
  real Artifact Registry takes the location from the registry endpoint host, so
  the same repository path reports `locations/us-central1`, `locations/us`,
  `locations/europe-west1` or `locations/asia` depending only on which host was
  addressed, and a host that is not a regional registry is not a registry at
  all. The data plane derives the location the same way and reproduces the live
  denial exactly; at the simulator's own coordinate, which names no location and
  has no real-service equivalent, the repository the control plane created
  supplies it. When neither can, it answers `NAME_UNKNOWN` — the code OCI
  Distribution defines and Artifact Registry implements — rather than inventing
  a region.

- **BUG-29 (test suites shared global container image tags):** Every cloud's
  harness images are namespaced per suite, so concurrent suites can no longer
  clobber each other's tag mid-run. The latent defect this was expected to
  unmask was real and live: the Google Cloud CLI harness used plain
  `docker build`, which on this host's default `docker-container` builder exits
  zero while leaving the image only in the build cache — proven by a control
  showing the image absent from the store after a successful build and present
  after `buildx build --load`. Two more defects surfaced with it: two tests
  pulled a hardcoded tag and so depended on another suite having populated the
  store, and all three Google suites built the simulator binary to one shared
  path where a build could overwrite a binary another suite was executing.
  Proven by running two suites concurrently against one daemon.

- **BUG-31 (Compute Engine booted the instance inside the insert request):**
  Insert records the instance provisioning, returns a running zone operation
  immediately, and boots behind it on a context detached from the request with
  its own budget deliberately longer than a client's wait, so a client that
  gives up no longer destroys the machine it asked for. `zoneOperations.wait`
  implements the documented two-minute contract instead of answering a
  fabricated completion. Adjacent synthetic behaviour went with it: operation
  reads on all three scopes rendered an invented `DONE`, the operation lists
  returned a hardcoded empty set, and the aggregated list an empty map — all
  now read the record. Restart recovery settles a provisioning instance and any
  operation left running, so a client cannot poll forever. Exercised on Linux
  rather than accepting the macOS kernel skip.

- **BUG-32 (the Amazon ECR data plane created repositories implicitly):** Every
  repository-scoped route passes through one admission chokepoint after
  authentication and before any store access, and a repository the registry has
  no record of answers 404 with the Docker Registry v2 envelope and
  `NAME_UNKNOWN`, the code and message confirmed from real client captures
  rather than assumed. AWS is explicit that this is the difference from Docker
  Hub: "With Amazon ECR, new repositories must be explicitly created before
  they can be used." The one documented exception is implemented with it,
  because the refusal is over-broad without it: a push whose repository matches
  a repository creation template applied for `CREATE_ON_PUSH` creates the
  repository from that template — most-specific prefix first, `ROOT` last —
  carrying its tag mutability, encryption, tags and policies. Reads never
  create. Three existing tests were pushing to repositories they had never
  created and passed only because nothing checked; they create them through the
  real API now.

- **BUG-33 (an AWS Lambda REPORT line invented its memory figure):** The
  reported maximum memory is measured from the execution environment's own
  container instead of being half the configured size. What the engine can
  actually provide was measured rather than assumed: this host's engine reports
  only `usage` and `limit` under cgroup v2 — no `max_usage`, no `peak` — and
  its streaming stats endpoint samples about every five seconds, which for a
  sub-second invocation is one reading taken before the handler allocates
  anything. The container observer therefore polls every 50 milliseconds for
  the container's life and keeps the highest figure the engine reports,
  including `max_usage` where an engine keeps that counter. When the engine
  reports nothing the member is omitted entirely rather than substituted — an
  absent field is honest, an invented one is not. An idle function reports
  78–82 MB and one holding a 384 MB buffer reports 466–469 MB.

- **BUG-1345 (AzureAD Terraform resources could not be tested):** Never an
  upstream blocker. The `hashicorp/azuread` provider has supported a Microsoft
  Graph endpoint override since v2.35.0 through `metadata_host`, and v3.9.0 now
  drives a real Entra stack against the simulator with that as the only
  coordinate — application, service principal, application password, users with
  a manager, group and group member — through apply, an idempotent
  `plan -detailed-exitcode` of 0, and destroy, on its own CI shard. The gap was
  measured off the wire rather than guessed, and it was not the advanced-query
  behaviour suspected when this was reopened: the provider sends no
  `ConsistencyLevel` header for these resources at all. What was missing was
  the whole Microsoft Graph `beta` endpoint, which the provider uses
  deliberately to work around documented v1.0 omissions — `oauth2RequirePostResponse`
  on applications, `showInAddressList` on users, `samlMetadataUrl` on service
  principals, and the entire group family — together with owner and member
  reference collections, the `manager` navigation property with a real 404 when
  unset, polymorphic `directoryObjects` carrying a concrete `@odata.type` so the
  provider can sort by type, `$select`, and round-tripping every property the
  client writes rather than the handful previously stored, which would
  otherwise have shown as plan drift. Entra is modelled as directory objects
  and mounted under both `/v1.0` and `/beta`. `$count` is implemented as
  documented and gated on `ConsistencyLevel: eventual`, with both states
  asserted.

- **BUG-19 (the AWS Lambda invocation timeout included the runtime INIT
  phase):** The suspicion was right and the divergence was real. AWS documents
  that the Init phase ends when the runtime signals readiness by requesting its
  first invocation, that Init is separately limited to ten seconds, and that
  the function's timeout bounds the Invoke phase — its own worked example shows
  a three-second function reporting `Duration: 3004.92 ms` beside
  `Init Duration: 111.23 ms`, the initialisation sitting outside the duration
  rather than inside it. The invocation timer now starts when the runtime first
  requests work, not when the container starts, and the runtime deadline header
  is computed at delivery. A ten-second Init limit is enforced, with the
  documented `INIT_REPORT … Phase: init Status: timeout` and a re-created
  execution environment whose retried init is bounded by the configured timeout,
  as the real service does. Container create and start remain outside both
  budgets, being the sandbox provisioning that precedes Init. The REPORT line
  now carries a real `Init Duration` and a `Billed Duration` derived as the
  ceiling of duration plus the ceiling of init, a formula that reproduces all
  three of AWS's published examples exactly. Proven by a function whose
  module-level initialisation blocks five seconds under a three-second timeout:
  it succeeds, and the negative control restoring the old timer reproduces the
  original `Task timed out after 3.00 seconds`.

- **BUG-18 (the Amazon ECR and Google Artifact Registry data planes
  authenticated nothing):** Both authenticate now, each against its own
  published contract rather than a copy of Azure's, and the differences are the
  point. Amazon ECR answers **Basic**, not a Bearer challenge — captured from a
  real registry as `Www-Authenticate: Basic realm="…",service="ecr.amazonaws.com"`
  with a 15-byte `Not Authorized` body — and the whole `authorizationToken` is
  the Basic parameter, as AWS's own `curl` example shows.
  `GetAuthorizationToken` was itself decorative, returning a constant; it now
  mints random material recorded under its password with the documented
  twelve-hour expiry, and an expired token is refused with the `DENIED`
  envelope a Docker client renders as "Your authorization token has expired".
  Deliberately NOT implemented: refusing a token used against another registry.
  AWS states a token "can be used to access any Amazon ECR registry that your
  IAM principal has access to", so that refusal would reject requests the real
  service accepts; the older per-registry wording belongs to the deprecated
  `registryIds` era. Google Artifact Registry differs again, verified by
  probing the live service: an absent credential gets a challenge, a *rejected*
  one gets 401 with no challenge, and an authenticated caller who cannot reach
  a repository gets `403 DENIED` naming the IAM permission — not Azure's
  `insufficient_scope`. Its token scope was proved by experiment NOT to be the
  gate (a token minted for one repository serves another, because the service
  re-evaluates per request), so scope enforcement was deliberately not added:
  it would have made the simulator stricter than the real registry. Both clouds
  use the same nil-able per-registry `Authorize` hook, so no cloud-aware branch
  exists in the shared registry. Proven with real clients — go-containerregistry
  pushing and pulling a three-layer image against ECR, podman login, push and
  pull against Artifact Registry — each with the full refusal set.

- **BUG-2764 (a Google Compute Engine guest never finished booting):** The
  cause was neither the one recorded here — no nested KVM on the macOS host —
  nor the boot deadline it was later suspected to be. `realexec` launched every
  microVM with `--enable-pci`, opting into Firecracker's PCI transport instead
  of its default virtio-MMIO. On aarch64 the guest never receives the
  completion interrupt for its first virtio-blk request, so the boot stops at
  the hand-off to the root filesystem, with the console frozen after `Key type
  encrypted registered` and zero bytes ever read from the root filesystem image
  while the vcpu thread spins. Raising the budget to fifteen minutes produced no
  further console output at all, which settled by measurement that the guest
  hangs rather than boots slowly. Removing the flag — one transport for every
  host, and Firecracker's own default — takes the same kernel and root
  filesystem to `Run /sbin/init` and a reachable guest in 31 seconds, and the
  whole Google Cloud Terraform suite passes. The same flag was removed from the
  Firecracker CI boot harness. It survived because CI runs on x86_64, where the
  PCI transport works, and no CI leg boots a guest on aarch64; hosted arm64
  runners expose no `/dev/kvm`, so that coverage is the local Firecracker and
  Terraform gates until a self-hosted arm64 runner exists. Two causes
  previously hidden behind this entry were separate defects and were fixed
  earlier: the poisoned asset cache and the architecture-blind kernel check.


- **BUG-3 (cross-resource-group move refused types real ARM moves):**
  Twenty-nine Azure type keys move, up from five when this was filed — API
  Management, standalone Logic Apps workflows, Cosmos DB accounts, Event Grid
  system, partner and partner-namespace topics, and thirteen
  Microsoft.Network types joined the earlier families. What made the network
  family possible is a general inbound-reference repointing pass rather than a
  hand-listed set: every store a build creates is now recorded, and after a
  hook runs the mover walks all of them, rewriting both keys beneath the moved
  identifier and any string naming it at a resource-identifier boundary, so an
  identifier embedded in a URL is caught too. Scanning every store is
  deliberate — a hand-maintained list rots silently. Confirmed rewrites include
  an Azure Cache for Redis linked server, an Event Grid system topic's source,
  a private DNS zone's virtual-network links, a Logic Apps access endpoint and
  the container registry's content. Each family's credential is pinned across
  the move, and where a data plane exists the proof is a real call rather than
  a key comparison; where one does not, the entry says so instead of
  downgrading quietly. The types Azure itself refuses stay refused and are
  pinned by tests at unit, SDK and CLI level — partner registrations, private
  link services, application gateways, NAT gateways, network profiles and
  virtual network taps are all published as unmovable, and private endpoints
  are conditional on the linked resource's type, implemented against the
  published allow-list. Verified against Azure's move-support tables as
  published on 2026-05-26. Fixed beside it: a Logic Apps callback signature
  covered the workflow's full resource identifier, so a move invalidated every
  outstanding callback URL; it signs the relative path now, as the real service
  does.

- **BUG-17 (the Azure Container Registry content stores were global):** The
  manifest, blob and upload stores carry a scope, and the Azure registry
  supplies its ARM resource identifier as that scope, so two registries can no
  longer resolve the same repository name to the same content. The catalog and
  tag listings filter by scope too. Because the scope is the resource
  identifier, a moved registry's content is re-keyed by the same repointing
  pass that closed BUG-3, and a test proves login server, admin credential,
  manifest, blob, tag list and catalog all survive a cross-group move.

- **BUG-25 (the specification validator judged registry responses against
  unrelated schemas, and a push test proved nothing):** Two defects found while
  arming the validator for the Artifact Registry work. The validator carried
  its own copy of the "is this an OCI data-plane path" predicate, which had
  drifted from the one `token_signing.go` uses, so `GET /v2/token` matched
  Cloud Logging v2's `GET v2/{+name}` template and its perfectly valid `token`
  and `expires_in` members were reported as fields Cloud Logging's
  `LogExclusion` does not define. The duplicate is gone and both callers share
  one predicate, so they cannot disagree again. Beside it, the existing OCI
  push test pushed to a repository that was never created and passed only
  because nothing checked; it now creates the repository through the real SDK
  first.

- **BUG-16 (a release tag existed before the artifacts it named):** The Release
  workflow ends in a reconciliation job that asserts the finished release
  matches what its tag promises: all thirty assets the build matrix produces —
  three simulators across linux and darwin on amd64 and arm64, plus the three
  console bundles, each with its checksum — and all three multi-architecture
  image indexes resolving to an OCI index carrying linux/amd64 and linux/arm64.
  An asset the matrix no longer produces fails it too, so the workflow and the
  release cannot drift apart silently. A failing build was always caught by the
  job that failed; what this closes is the hanging build, which left a tagged,
  published release that looked entirely ordinary while carrying part of its
  contents. `scripts/verify-release-complete.sh` runs standalone against any
  tag, so a release can be checked before a consumer pins it. Proven in both
  directions: it passes v0.9.1 and v0.9.2, and it fails for a tag with no
  release, for an expected asset that is not published, and for an image index
  that does not exist.

- **BUG-15 (an Amazon RDS instance served connections before its engine was
  ready):** The lazy data-plane start always did wait for the engine — the
  defect was that its probe read any PostgreSQL `ErrorResponse` as proof of
  readiness, and `FATAL: the database system is starting up` (SQLSTATE 57P03)
  is an `ErrorResponse`, so the gate opened the moment the postmaster bound its
  port and the proxy forwarded clients into a server refusing all of them. The
  probe now parses the error's SQLSTATE and treats only 57P03 as not-ready, the
  classification `pg_isready` makes; MySQL and MariaDB were already correct,
  reporting ready only on a real protocol handshake, and the reason is
  documented beside them. Two defects in the same path went with it: the adopt
  path taken after a restart or `StartDBInstance` marked the lazy start
  complete without probing at all, so the first client met an engine still
  replaying its write-ahead log — which is what the 57P03 wait in
  `sdk-tests/persistence_dataplane_restart_test.go` had been papering over, and
  that wait is now removed so the test guards the bug instead of hiding it —
  and the 90-second engine budget both under-provisioned a real first boot
  (a `mysql:8.0` cold start measured 253 seconds under load) and destroyed the
  instance when it expired, with the error cached permanently, so a slow host
  bricked a database. The budget is ten minutes and re-reads the container's
  real state every two seconds, so a genuinely dead engine still fails fast.
  Reproduced deliberately under contention before the fix (three of three runs
  failed on 57P03), verified under the same contention after (three of three
  passed), and guarded by a wire-level unit test with the negative control
  confirming the old classification fails it.

- **BUG-13 (the Azure Container Registry data plane authenticated nothing):**
  Every registry request is authenticated. An unauthenticated call answers the
  Docker Bearer challenge ACR publishes — `Www-Authenticate: Bearer
  realm="…/oauth2/token",service="…",scope="…"`, the form the official
  `azcontainerregistry` policy requires both `service` and `scope` from — and
  the token service behind it is real: `GET /oauth2/token` verifies the admin
  Basic credential and only while `adminUserEnabled` is set, `POST
  /oauth2/exchange` verifies a Microsoft Entra token for the
  `https://containerregistry.azure.net` audience, and `POST /oauth2/token`
  verifies the refresh token or password grant. Tokens are real JWTs because
  the Azure SDK decodes `exp` out of them, they are issued for one registry,
  and their `access` claims are checked against the access record the request
  implies, following distribution's own method mapping. A credential-less
  caller reaches only a `pull` on a registry with `anonymousPullEnabled`, and
  the granted scope is filtered by what the credential authorizes. Regenerating
  an admin credential invalidates both the password and the tokens derived from
  it, through a fingerprint recomputed at verification time. Proven with a real
  client end to end — `podman login` refuses the wrong password and accepts the
  right one, push and pull succeed, and both stop working after logout and
  after rotation — and with the official SDK performing the documented
  401 → exchange → token → retry flow. The shared `/v2/` subtree gained a
  nil-able per-registry `Authorize` hook rather than any cloud-aware branch, so
  Amazon ECR and Google Artifact Registry are byte-identical to before and
  unaffected (their own gap is BUG-18). The registry's method floor ratcheted
  from 19 to 20 for the `GET /oauth2/token` the specification declares. The
  Terraform assertion added beside this runs only on a capable Linux host, so
  it is exercised by CI rather than locally.

- **BUG-9 (the Event Grid data plane authenticated nothing):** The publish
  data plane authenticates every caller. A custom topic or domain accepts an
  `aeg-sas-key` matching either current slot as a header or a query
  parameter, an `aeg-sas-token` or `Authorization: SharedAccessSignature`
  verified as base64 HMAC-SHA256 of the token's own `r=…&e=…` prefix under
  the base64-decoded key — the format Event Grid publishes, which differs
  from the Service Bus signature beside it in both the signed string and the
  key encoding, so it was implemented from Event Grid's own generators rather
  than copied — with the expiry and the signed resource prefix honoured, or a
  Microsoft Entra bearer for the `https://eventgrid.azure.net` audience.
  `properties.disableLocalAuth`, declared in both vendored swaggers and
  previously inert, leaves only the last. Anything else answers Event Grid's
  401 `Unauthorized` envelope with its mirrored `details` array; the real
  service also appends a support-report identifier, which the simulator omits
  rather than mint a tracking ID referencing an organisation it has no
  relationship with. Event Grid's keys moved onto the shared rotation store,
  so `regenerateKey` invalidates the key and every signature derived from it.
  The domain publish path, which previously 404'd against its own advertised
  endpoint because host resolution searched only topics, routes each event to
  the domain topic its `topic` member names.

- **BUG-14 (a Microsoft.Web/sites move silently rotated the site's
  credentials):** The shipped move hook pinned nothing, so every move
  rotated the site's `publishingPassword` and the
  `logic-access-primary`/`secondary` keys of any workflow hosted under it,
  invalidating publish profiles and already-issued Logic Apps callback URL
  signatures. The hook now pins that material the way the storage and
  Service Bus hooks do, and `TestWebSiteMovePinsSiteDerivedCredentials`
  asserts a callback signature is identical across the move. The three
  hand-rolled pin loops were folded into one shared helper so a new family
  cannot forget the step, and `redisFirewallRules` — the only Azure Cache for
  Redis store keyed by name segments rather than by resource ID, which made
  the cache unmovable — is keyed like its siblings.

- **BUG-10 (Knative v1 set a resourceVersion nobody enforced):** All five
  Cloud Run v1 replace methods the document publishes now enforce
  `metadata.resourceVersion` — omitted is unconditional, as the document's
  own wording says, matching proceeds, and stale answers 409 ABORTED in the
  Google error envelope the service uses. The Knative `Status` object the
  document declares is the delete methods' response shape, not an error
  shape, which is why the conflict is not spelled that way. resourceVersion
  is the resource's generation and every v2 write bumps it while every v1
  write mints a fresh v2 etag, so neither spelling can land a write the
  other would have refused.

- **BUG-11 (Cloud Storage operations were fabricated):** The slice records
  its long-running operations into the shared operation store, parented by
  the bucket: bucket relocation, recursive folder deletion, folder rename
  and both Anywhere Cache writes all record, and get, list, cancel and the
  relocation advance answer about records that exist and 404 about ones
  that do not. The list pages and honours the documented `done` filter,
  refusing any other term loudly rather than ignoring it. Found with it:
  `buckets.relocate` drained its request body and reported a relocation it
  never performed — it now applies the destination location, placement and
  key, honours validateOnly, and defaults an absent location the way the
  service documents.

- **BUG-12 (the Cloud Run executions verb fan-in accepted any verb):** The
  fan-in switches on the verb and answers an unpublished one with the
  service's method-not-found, matching the spelling the v1 fan-ins already
  used. The cloudrun-v2 floor did not move and stays 102: cancel is the only
  POST custom method the document publishes on that collection, so no
  documented spelling changed verdict. Measured with the probe and a
  negative control rather than assumed — the expected decrease recorded in
  this entry never materialised, and no comment claiming one was added.
  Fixed beside it: a cancelled execution reported its terminal condition as
  succeeded, so the real `gcloud run jobs executions cancel` failed with
  "has completed successfully before it could be cancelled"; the cancel
  path now writes the failed condition with reason Cancelled.

- **BUG-8 (`Microsoft.Resources/tags/default` wrote a plane the resource
  could not see):** Every scope now resolves to one holder of its tags. A
  resource scope reads and writes the resource's own `tags` member through
  the resource registry, which gained a tags reader and writer beside its
  enumerator so no second lookup table exists; a resource-group scope
  writes the group's own record, which had the same divergence; and the
  subscription and management-group scopes keep `tagsStore` as their only
  home, because the simulator holds no record for either. PATCH honours
  Merge, Replace and Delete against the holder, the generic resource lists
  report the same set either way, and a scope holding no resource answers
  404 as Azure Resource Manager does. The registry's initialisation now
  refuses a tracked type whose stored form has no settable tags member,
  which caught the nine Microsoft.Network types that carry theirs in an
  embedded envelope. The move dispatch's separate tag re-homing became dead
  code and was deleted: a resource's tags travel with the record its hook
  re-keys.
- **BUG-6 (the operations cancel method was unserved):** The entry named
  twelve documents; the coverage worklist showed five of them
  (Cloud Spanner, Firestore, Cloud SQL Admin v1 and v1beta4, Cloud Storage)
  already served cancel, so the real remainder was seven documents and
  twenty method spellings, all now served with each service's own answer.
  The nine services whose vendored description says a caller checks
  GetOperation for "whether the cancellation succeeded or whether the
  operation completed despite cancellation" answer 200 with the record
  untouched, because a completed operation is a documented outcome there
  rather than an error; Cloud SQL Admin answers the 400 FAILED_PRECONDITION
  its own documentation shows, with the distinct message it gives for an
  operation type it cannot cancel. An unknown operation name answers 404
  everywhere, which Cloud Logging previously did not check at all. Every
  long-running operation in this simulator is minted complete, so for
  eleven services the already-done answer is the only honest one and no
  unreachable cancellation branch was written; a new invariant test fails
  if that ever stops being true. AWS Cloud Build was the exception: it runs
  real processes, so its steps now execute under a cancellable context and
  cancel really terminates the running build — proven by a control that
  removes the termination and watches the test hang until its deadline.
  Three defects surfaced with it: Service Usage named its operations under
  a path its own get, delete and cancel could never resolve, Cloud Build
  recorded no operation for its non-build long-running work and returned
  the resource name where the operation name belonged, and Cloud Storage
  minted two different identifiers for one operation's name and selfLink.

- **BUG-7 (Cloud Run v2 minted an etag for Job alone):** Service, Revision,
  Execution, Task, WorkerPool and Instance now mint an etag at every store
  write, including the Knative v1 write paths and the Cloud Functions
  backing service, and a supplied etag is enforced on all six deletes, on
  the Service, WorkerPool and Instance patches — read before the update
  mask merges, so a mask cannot smuggle the condition away — and on the
  cancel-execution, start-instance and stop-instance requests, which were
  not decoded at all before and now honour validateOnly too. An omitted
  etag stays unconditional and a stale one answers 409 ABORTED, matching
  the service. Knative v1 deliberately keeps no etag: its document declares
  one only on the IAM policy, and its optimistic concurrency is
  resourceVersion, tracked as BUG-10.


- **BUG-4 (the subscription resource list answers only Key Vaults, and
  ignores `$filter`):** `GET /subscriptions/{sub}/resources` and
  `GET /subscriptions/{sub}/resourceGroups/{rg}/resources` are answered from
  a cross-slice registry (`simulator-azure/resource_registry.go`): a
  package-level table keyed by lowercased `provider/type` — the key shape
  `resourceMoveHooks` uses — maps each tracked resource type to the store the
  slice that owns it keeps its rows in, read through a closure at request
  time so a store assigned or reassigned by a register function is always the
  one enumerated. Fifty-six types are registered, spanning Microsoft.Web,
  Storage, KeyVault, Network, Compute, App, ContainerInstance,
  ContainerRegistry, ServiceBus, EventHub, EventGrid, DocumentDB,
  DBforPostgreSQL, Cache, OperationalInsights, Insights, ManagedIdentity,
  ApiManagement and Logic. Only resources ARM tracks are listed: a provider's
  locationless proxy children — a subnet, a Service Bus queue, a DNS record
  set, a role assignment — are reached through their parent's API and are
  absent from the list, as they are from real ARM's. Each row is rendered
  from the stored resource's own wire form into the GenericResourceExpanded
  members (`id`/`name`/`type`/`location`/`kind`/`managedBy`/`sku`/`identity`/
  `plan`/`tags`), so a slice needs no per-type projection and a resource that
  cannot be read back through its own JSON fails loudly instead of vanishing
  from a list that claims to be complete. Real ARM does not return a
  resource's provider-specific `properties` document from a list and neither
  does the simulator; `terraform-provider-azurerm`'s Key Vault cache reads
  only `id` and `name` from it before reading each vault through the Key
  Vault provider.

  Both routes honour the `$filter` grammar the operation documents and real
  clients send: `eq`/`ne` over `name`, `resourceGroup`, `resourceType`,
  `location`, `tagname` and `tagvalue`; `substringof(value, property)` over
  `name` and `resourceGroup`; `startswith(tagname, prefix)`; conjunctions
  and disjunctions with `and`/`or`, `and` binding tighter. A filter naming
  anything else — or carrying grouping parentheses — is refused with the
  400 `InvalidFilterInQueryString` real ARM answers, because a silently
  ignored filter answers with everything and reads as a result. Filtering on
  a tag name or value suppresses the rows' tags, as ARM's documentation
  states. `$expand` accepts the three documented members and reports
  `provisioningState` from the state each resource recorded for itself;
  `createdTime` and `changedTime` are absent because no slice records either,
  which is the same answer ARM gives for a resource it holds no such metadata
  for. `$top`/`$skiptoken` page through the shared `armPage`/`armNextLink`
  helpers. This is what `az resource list -g <rg>` needed: the Azure CLI
  reaches every scoping it offers — `-g`, `--name`, `--location`,
  `--resource-type`, `--tag` — through this one route's `$filter` rather than
  the resource-group-scoped route, so the group-scoped listing was previously
  the whole subscription's vaults.

  The related Managed HSM gap closed with it. `Microsoft.KeyVault/managedHSMs`
  became a real slice (`simulator-azure/keyvault_managedhsm.go`) serving
  ManagedHsms_CreateOrUpdate, _Update, _Get, _Delete, _ListByResourceGroup
  and _ListBySubscription over its own store, so a scope holding no pool
  answers the empty collection real Azure answers rather than the 404 an
  unrouted path returns, and a provisioned pool round-trips through its own
  API and appears in the generic resource list. `managedHsm.json` (2023-07-01)
  is vendored and `keyvault-arm-managedhsm-2023-07-01` entered
  `azureMethodFloor` at 6 — the only coverage floor that moved, and it moved
  because six operations are now genuinely served.

  Covered by `TestResourcesList_ScopesAndFilters`,
  `TestResourcesListByResourceGroup_ScopeExpandAndPaging` and
  `TestManagedHSMs_ListIsEmptyNotMissing` through the canonical armresources
  and armkeyvault clients; by `TestResourceListCLI`, which proves
  `az resource list -g <rg>` reports that group's resources only and reports
  more than one provider's; and by the unit tests
  `TestAzureTrackedResourceKeys`, `TestParseAzureResourceFilter` and
  `TestAzureIDSegmentAfter`, which pin the registry's key shape, every
  accepted and refused filter form, and the scope reading both the list's
  scoping and its `resourceGroup` filter rest on.

- **BUG-5 (the older Knative collections ignore their list parameters):**
  The five Cloud Run Admin v1 collections that predate the jobs family —
  services, revisions, routes, configurations and domainmappings — honour
  `labelSelector`, `limit` and `continue` like the rest. Every list call site
  goes through `knativeCollectionPage` (`simulator-gcp/cloudrun.go`), which
  narrows the stored collection to the request's namespace, applies the
  selector through the shared `knativeLabelSelectorMatches`, orders what is
  left by resource name so a cursor is stable across requests, and pages it
  through the shared `knativeListPage`; a malformed cursor is refused rather
  than silently reset. `CRServiceList`, `CRConfigurationList`, `CRRevisionList`,
  `CRRouteList` and `CRDomainMappingList` carry the `metadata` (a `CRListMeta`
  holding the continue cursor) and `unreachable` members the Discovery
  document declares — `CRServiceList` previously typed `metadata` as a free
  map and the other four omitted both.

  The etag half closed for the resource that named it. The Cloud Run v2 `Job`
  reports the `etag` the Discovery document declares — a fresh fingerprint at
  every write, including the execution-count and completion-time updates a
  run makes — and `jobs.run` refuses a `RunJobRequest.etag` the job has moved
  past with the 409 ABORTED Cloud Run answers a modification conflict with,
  as do `jobs.patch` for a body etag and `jobs.delete` for the query
  parameter. An omitted etag is unconditional, so a client that does not
  track the fingerprint is unaffected. The Knative RunJobRequest declares no
  etag at all — the entry's "unread on both API versions" was wrong about v1,
  which carries only `overrides` — so there was nothing to read there.

  Covered by `TestCloudRunV1_ServicesList_LabelSelectorAndPaging`,
  `TestCloudRunV1_ReconciledChildrenList_LabelSelectorAndPaging`,
  `TestCloudRunV1_DomainMappingsList_LabelSelectorAndPaging` and
  `TestSDK_RunV2REST_Job_RunEtagOptimisticConcurrency`, which page each
  collection into disjoint pages covering it exactly once and select by
  label through the canonical `google.golang.org/api/run/v1` and `/run/v2`
  clients.

- **BUG-2924 (two live VPCs sharing a CIDR conflicted as Docker networks):**
  The AWS simulator stopped making a VPC network's bridge subnet the VPC's own
  CIDR. `EnsureVPCNetwork` allocates each VPC's bridge subnet as a /24 slice of
  the reserved host-side pool `10.213.0.0/16`, scanning from a name-derived
  offset, skipping every subnet a network on the host already holds (which is
  also what makes a simulator restart double-allocation-free — the live
  networks are the allocator's only ledger), and reclaiming a slice held by a
  dead simulator run's leftover under the same four load-bearing conditions as
  before. The workload still genuinely owns its elastic network interface
  address: after the container starts on the pool bridge, an ephemeral busybox
  container joins its network namespace with CAP_NET_ADMIN and runs
  `ip addr add <eni-ip>/<vpc-prefixlen> dev eth0`, plumbing the ENI IP as a
  secondary whose kernel-derived connected route makes same-VPC peers on the
  shared bridge reachable over plain ARP, while the workload itself keeps its
  capability-free cloud-faithful sandbox and same-CIDR VPCs sit on different
  bridges that never see each other. ECS awsvpc tasks and Lambda VPC
  invocations both carry the address through `ContainerConfig.ENIAddress`;
  DescribeTasks and the task metadata kept their reported `privateIPv4Address`
  shape, and the Elastic Load Balancing target lookup became VPC-scoped so two
  same-CIDR VPCs holding identical ENI addresses resolve to the right task.
  `TestECSVPCOverlappingCIDR` runs on both fabrics — two live VPCs with the
  same CIDR, a server in each holding the same ENI IP, each client reaching
  only its own VPC's server — and the dead-run reclaim regressions moved onto
  pool slices.

- **BUG-2887 (Azure Application Gateway managed WAF rule-set catalog):**
  `ApplicationGateways_ListAvailableWafRuleSets` now serves the complete
  managed rule-set catalog — OWASP 3.2/3.1/3.0/2.2.9,
  Microsoft_BotManagerRuleSet 0.1/1.0/1.1, Microsoft_DefaultRuleSet 2.1/2.2;
  95 rule groups, 1,194 rules with wire-faithful descriptions, states,
  actions and tiers — vendored in
  `simulator-azure/network_appgateway_waf_rule_sets_vendored.json` from
  Microsoft's published rule enumeration cross-checked against recorded
  responses of the real service. Per-group counts are locked by
  `TestApplicationGatewayWafRuleSetsVendoredCatalog`; SDK and CLI tests
  exercise the endpoint; the
  `network-arm-applicationgateway-2025-03-01` coverage floor moved 21 → 22
  (the document's full 22 of 22).

- **BUG-2922 (Docker Engine advisories, simulator copy):** The three simulator
  modules moved from `github.com/docker/docker` to `github.com/moby/moby/client`
  v0.5.1 and `github.com/moby/moby/api` v1.55.0 — a wire-identical swap onto the
  new client's Options/Result structs, with 404 classification via
  `containerd/errdefs`, ports as `network.Port`, and addresses parsed to `netip`
  at the boundary. `github.com/docker/docker` left every module graph and
  `govulncheck` no longer reports GO-2026-5668 or GO-2026-4887. The shared
  container-runtime suites passed against the real Podman-backed daemon. The
  sockerless repository's Docker backend still carries its own copy of this bug.

- **BUG-2 (skip-if-absent, Cosmos DB differential):** The differential
  provisions its emulator end to end: the harness pulls the image when the host
  lacks it, hands one OS-selected port to both `docker -p` and the emulator's
  `--port` (the advertised data-plane endpoint follows the configured port, so
  nothing contends for the default 8081), and fails loudly on pull, start, or
  readiness. All four tool-absent skips are gone; both differentials passed
  against the real emulator on a dynamic port.

- **BUG-1 (deadcode coverage gap, shared/):** The genuinely dead helpers were
  deleted from each diverged `shared/` copy per that copy's own Linux findings
  (aws 34, gcp 55, azure 51 — cross-cloud error helpers and routers, unused
  Scanner/FrameReader/process helpers, `StartContainer`/`runContainer` where
  the cloud runs everything through other paths), together with their orphaned
  tests, and `scripts/simulators-deadcode.sh` no longer excludes `shared/`
  findings. `deadcode -tags noui -test .` reports zero findings for all three
  modules on Linux and macOS alike.

- **BUG-2928 (Lambda invocations exceeding their own timeout locally):** The
  class was attributed to a degraded local container runtime, with a restart
  as the recorded remedy and "a restarted local runtime does not reproduce
  it" as a close criterion. After the Podman virtual machine was restarted,
  the full Lambda invocation SDK suite — including the arithmetic invocations
  that had returned `Task timed out after 3.00 seconds` with an empty payload
  — passed locally (11 of 11 in 59.8s), meeting that criterion. Hosted runs
  never reproduced it.
