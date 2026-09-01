# DO NEXT

0-monitoring. **Complete release integration for the three application
   observations.** The publisher merged in feature PR #95 and shipped in
   `v0.26.0`. Live acceptance then proved Google Cloud's global cloud-bearer
   verifier treated the monitoring credential as a Google JWT before the
   monitoring handler could authenticate it. The canonical monitoring path is
   now owned by its dedicated bearer boundary, while the same credential stays
   invalid on cloud API routes. After this branch merges and publishes, advance
   the owning infrastructure release pin and verify all three authenticated
   observations through Shauth.

0. **The remaining tails are being served, document by document, on one
   branch.** An earlier revision of this item claimed that what remained
   unserved was "without exception" a family needing invented data or a
   primitive the container engine lacks. That was wrong, and the floor
   comments said so in their own words: measured on 2026-08-27, roughly 79
   Google Cloud method spellings were plain routing misses. A route a handler
   never sees and a fact the simulator cannot know are different problems, and
   filing the first under the second hid ordinary work.

   Done: **Cloud Storage, 89 of 89** (2026-08-27) — soft delete and the
   restore surface it exists for, `objects.move`, and the per-object access
   controls. **Cloud Logging, 508 of 508** and **Artifact Registry, 147 of
   147** (2026-08-28) — the colon-verb split that let locations.get mount
   without inflating Cloud Run, the plain `/v1` spellings of the media publish
   methods, and the prewarmed-artifact family over real state. **Cloud Build,
   114 of 114** (2026-08-28) — the regional surface, the build and trigger
   colon-verbs, the webhook receivers, and the Bitbucket Server
   connected-repository pair. **Firestore, 108 of 120** (2026-08-28) — the
   document-parent custom methods, `documents:write`, and the databases
   clone/restore pair. **BigQuery, 95 of 95**, **Cloud Run v2, 104 of 119** and
   **Memorystore, 90 of 94** (2026-08-28) — the media upload path, the hosted
   build path routed to real Cloud Build, and rescheduleMaintenance.

   Left, largest first, with the per-document floor comment naming each:
   Compute Engine's long tail (1,118 of 2,014 spellings; 559 of 1,007
   methods); then Azure's 31 remaining non-App-Service operations and the implementable part of App Service's 76; then the 230 AWS
   IAM derivations.

   A Discovery document's field descriptions are worth reading before the
   first implementation, not after: the prewarm family's contract was wrong in
   five places on the first pass — resource names versus bare ids, an optional
   member treated as required, the wrong default retention, a resource name
   where a registry URI belongs, and a `gs://` prefix the member does not
   carry.

   Genuinely blocked, and to be left unserved with the reason recorded rather
   than answered with invented data: Cloud Spanner's Key Visualizer scans,
   quorum change and wire-protocol adapter; Cloud KMS' Key Access
   Justifications; App Service's packet capture and process dump/modules/kill;
   the Application Insights query data plane; and the two published catalogs
   declined three times (Microsoft's runtime stacks, Google's SKU list).

0-iam. **The AWS IAM derivation gap was mostly measurement, and what is left
   is not.** 1,967 of 1,994 on 2026-09-01.
   `IAM_DERIVATION_LIST_MISSING=1 go test ./simulator-aws -run
   IAMResourceDerivationCoverage -v` names every missing operation per service.

   Between 1,792 and 1,936 almost every gain came from the coverage probe
   addressing an operation the way no client does, so a derivation that already
   worked measured as absent. Four defects, all closed:

   - Eleven copies of the rule deciding what a probe puts in a request member,
     so a fix in one reached only the services routed through it. There is one
     copy now, service-aware, because a member can only be filled correctly if
     you know whose member it is.
   - Each service's probe filled its ARN members with one ARN chosen for the
     whole service, so an action about something else was addressed with an ARN
     naming a resource it is not about. Every probe now builds that ARN from
     the action's own declared type.
   - That ARN was rendered by filling only the first variable a format
     declares, so a WAFv2 web ACL came out `probe/webacl//`. The whole format
     is rendered now.
   - The probe could express a scalar, a list of scalars and a structure, and
     nothing else — so a list of structures and a map both arrived as bare
     strings. Those are how a service spells a batch, and the identifier sits
     inside the element or in the key. Both render faithfully now, and Amazon
     DynamoDB and Amazon EventBridge went through the shared probe rather than
     their own flat ones.

   The lesson worth keeping: a wrong probe value shows up as a number that does
   not move, and a wrong reader shows up as a grant nobody notices. The
   measurement class was safe to fix in bulk for exactly that reason. What is
   left is reader work and is not.

   The creates are done. Every one of them now names what it mints: by the
   type answering to the operation's name, by saying the same words in another
   order (`RequestSpotFleet`), by being the noun outright where another type is
   only what the noun ends with (`CreateGlobalReplicationGroup`), or by a
   reviewed entry in `iamCreatesItsOnlyDeclaredType` where the name says
   nothing (`CreatePublicIpv4Pool`, `PurchaseCapacityBlock`). Extend that list
   an entry at a time when a new create needs it.

   The 27 that remain are three shapes, and none of them is ordinary work:

   - **A resource named inside a nested query member.** `ec2:ModifyInstanceCreditSpecification`
     names its instances at `InstanceCreditSpecification.1.InstanceId`, and
     AWS Auto Scaling names its group at `Tags.member.1.ResourceId`. The query
     parameter reader drops any key with a dot left in it after the index, so
     neither is visible. Exposing them is a four-line change and it is a trap:
     Amazon EC2's filters are nested the same way, `Filter.1.Name` would then
     read as a member called Name, and a request would derive from whatever it
     was searching on rather than what it was about —
     `TestIAMResourceARNs_EC2IgnoresNestedStructureMembers` exists to stop
     exactly that. Anything here has to tell a filter from an argument first.
   - **A resource the request does not name at all.** `cloudwatch:ListMetrics`
     and `PutMetricData` declare a `dataset` and name none; AWS Glue's
     `ListMLTransforms` and `GetDashboardUrl` are the same. `"*"` is the honest
     answer for these, and the ratchet counts them as misses. Deriving the type
     wildcard instead would turn a derivation bug into a silently broader
     grant — worse than the `"*"` it replaces, because it looks right. So the
     widening has to distinguish "this request names no instance" from "this
     reader did not find the instance".

     Do not try to decide it by inspecting member names. That was built and
     measured on 2026-09-01: read the request members out of the Smithy models
     and call an action "names no instance" when none of them shares a word
     with the type or with an identifier its ARN format declares. Tightened
     three times — a bare `Name` always counts, a word inside a run-together
     type name counts, any member ending in an identifier suffix counts — it
     still produced dangerous false positives of four distinct kinds, each of
     which would have widened a real grant:

     - a resource named indirectly by a value whose member name says nothing
       about it (`ec2:AcceptAddressTransfer` carries an `Address`,
       `iam:ListMFADeviceTags` a `SerialNumber`);
     - the caller as the resource (`iam:ChangePassword` — widening it would
       have authorized changing any user's password);
     - an identifier in a map's keys (`dynamodb:BatchWriteItem` names its
       tables there, which a member-name check cannot see);
     - an identifier that resolves to the resource through the simulator's own
       state (`ec2:DisassociateSubnetCidrBlock`'s `AssociationId`).

     Against two operations it would legitimately have gained, that is a bad
     trade, and the heuristic was discarded rather than shipped. The class
     needs per-action review, not inference — which is what
     `iamCreatesItsOnlyDeclaredType` is: four creates, each read against the
     service's documentation, safe because a create has no instance to
     over-grant against. Extend the same way, an entry at a time, and only
     where the reviewer can say what the operation is about.

   - **A resource found only by looking it up.** This is most of what is left.
     AWS Glue's data-quality family resolves its ruleset through the run
     record; `iam:GetAccessKeyLastUsed` finds the user who owns a key; Amazon
     EC2's Disassociate and Detach family resolves an association to its
     parent. Every one is implemented and held by a direct test, and none can
     move the ratchet as the probe stands, because the probe sends a synthetic
     request against an empty simulator.

     The seam is open and the mechanism is built.
     `iamSeedDerivationFixtures` creates what a family resolves through by
     calling the service's own creation handler, and the probe then names the
     resource by the identifier the service assigned — which measures the
     reader rather than a fixture, the distinction being that nothing writes
     into a store. Amazon EC2's three association derivations went through it
     first: a route table associated to a subnet, an elastic IP associated to
     an interface, an interface attached to a machine.

     AWS Glue's data-quality cluster followed through the awsJson router — a
     recommendation run, an evaluation run and the result row it settles — and
     took five more. Extending it is mechanical: add the creation calls a
     family needs and key the result "<service>:<operation>:<member>", per
     operation because two calls can name different things through the same
     member.

     `iam:GetAccessKeyLastUsed` followed, on a key created for a user the same
     way. That exhausts the readers that were already written: every remaining
     state-resolved operation needs the reader *as well as* the fixture, which
     makes them per-service work rather than more of this. Systems Manager's maintenance-window
     execution went that way first — its handler's own lookup walks every
     window, which a per-request derivation may not do, so the derivation asks
     a generation-keyed index instead. Amazon EC2's remaining associations
     followed — the instance profile, the interface permission and the two CIDR
     blocks, each a record keyed by the id the request carries. Amazon RDS's automated backup followed, resolved
     through the record the simulator keeps under the cluster's own resource
     id. Left: Systems Manager's access request, RDS's proxy target group, AWS CloudTrail's insights and query, and Amazon EC2's
     AcceptAddressTransfer and DeleteVpcEndpointConnectionNotifications.

   Take the first shape one service at a time and hold each to a test that
   names the resource it must derive and one it must not.

   One measurement seam is left open and its cost is known. AWS Systems Manager
   still has its own flat probe, and CreateAssociationBatch names each document
   and machine inside a batch entry — a shape only the shared probe renders.
   The derivation reads it (a batch entry's identifier is read like a top-level
   one, held by `TestIAMResourceARNs_ABatchEntryNamesItsResource`), so the
   reader is not what is missing. Routing Systems Manager through the shared
   probe as it stands *loses* eight operations, measured. Two explanations are
   already ruled out: the target header is right (`AmazonSSM`, as the handler
   table spells it), and the wire names are the model's own — `memberWireName`
   is the identity for this service. What is left is the shapes. The flat probe
   sent every member as a string; the shared one sends a member the model
   declares as a list or a structure as one, and Systems Manager's `Target` is
   a string on `GetConnectionStatus` and a `{Key, Values}` structure elsewhere,
   so rendering it faithfully stops handing the instance-id reader a bare id.
   The eight are derivations that work, so find which members changed shape
   before moving the service.

0-sync. **All three clouds are in sync.** Measured 2026-08-29: zero drift
   across AWS's 41 Smithy models plus its service references, Azure's 120
   Swagger documents, and Google Cloud's 30 Discovery documents.

   Google serves several Discovery revisions concurrently, so the last one or
   two documents can oscillate by edge; the scheduled run vendors from what it
   sampled rather than re-fetching, which is what settles them.

   Three defects had kept the nightly refresh from landing anything, and all
   three are fixed. The one-PR gate made the run give up whenever a pull
   request was open; it now pushes onto that request. A fetcher asked
   www.googleapis.com for `apis/www/v1` when re-vendoring Compute Engine, and
   the 404 aborted the whole Google sweep. Last, the freshness check compared
   every pin against `commits?srcpath=` — a parameter the GitHub API does not
   define, so the query dropped the filter and answered with the repository's
   branch tip. Every AWS and Azure row therefore reported drift against a
   commit that never touched it, and no refresh could ever clear it: of the
   162 reported, 5 were real. `scripts/check-gh-api-params.sh` now refuses a
   query parameter GitHub does not define, in pre-commit and in CI.

0-spec. **A re-vendor moves counts, so read them.** No document is behind
   today, but the next refresh will move both a declared total and a served
   floor, and the floor comment has to say which methods moved and why.

   Cloud Build is the worked example (revision 20260814): Google withdrew the
   whole `gitLabConfigs` collection, and the simulator no longer serves it. Expect
   more withdrawals — a served count that falls after a re-vendor is not
   automatically a regression, but it must be shown to be a withdrawal.

0a. **A served count can hide an unserved method, and a gate now says so.**
   The coverage probe reads any handler answer as served, so a sibling
   collection swallowed by a multi-segment wildcard counted as covered while no
   handler for it existed. Cloud Storage's five per-object ACL reads and writes
   were covered that way for as long as the gate had existed; `/o/{object}/acl`
   reached `objects.get`, which answered `object "doc.txt/acl" not found`.

   `TestServiceConformance_GCPNoPhantomCoverage` closed the class across every
   document: it asks the mux which pattern actually matched and holds each
   served method to a route that names its Discovery path's literal segments.
   The sweep found six more — Compute Engine's `backendServices.listUsable`
   (global and regional) and `backendBuckets.listUsable`, swallowed by the
   `{name}` get, and Cloud Storage's object `getIamPolicy`, `setIamPolicy` and
   `testIamPermissions`, swallowed by the `{object...}` get — and all six are
   served now. The served counts did not move, because all six already counted.

   `gcpFanInPatterns` lists the routes that legitimately dispatch inside the
   handler, each with the reason. Add one only with the evidence that the
   handler reads the tail and rejects what it does not route; an entry that
   merely silences the gate reinstates the blind spot.

0b. **Judge a route on both clients before believing it.** The generated Go
   client sends `softDeleted=true`; gcloud sends `softDeleted=True`. An
   exact-match comparison passes every SDK test and returns an empty list to
   the CLI, silently. Query booleans go through `strconv.ParseBool`.

1. The gRPC surfaces are served (2026-08-26). The ratchet in
   simulator-gcp/grpc_coverage_test.go holds 210 of 213, and the three that
   remain are not work waiting to be picked up:
   `Bigtable.OpenMaterializedView`, `Firestore.ExecutePipeline` and
   `Spanner.FetchCacheUpdate` each need state this simulator does not hold —
   a materialized result set, a pipeline expression evaluator, and a
   split/zone topology — and the floor comment records why for each. Reopen
   one only if the simulator gains the state it would report; serving it
   before then means inventing that state.

   The door-parity gap this exposed is closed for Google Cloud (2026-08-26).
   `simulator-gcp/sdk-tests/cross_door_test.go` writes through one protocol and
   observes through the other for every mounted gRPC service, and
   `simulator-gcp/cross_door_test.go` holds that file to the services the
   server mounts. It found the long-running Operations divergence on its first
   run — two stores and two name shapes for one resource — which is now one
   store and the name the bigtableadmin document declares.

   Neither of the other two clouds has a second protocol door, so there is
   nothing to cross there: the AWS and Azure simulators serve one HTTP surface
   each. The equivalent question for them is a different one — whether the SDK,
   CLI and Terraform clients that reach the same operation agree — and the
   shard-coverage gates already hold each of those surfaces to its own tests.

2. Simulators no longer outlive their tests (2026-08-27). If orphaned
   simulators ever reappear, the watch is the first thing to check: a harness
   that starts one without `SOCKERLESS_PARENT_PID` in its environment gets no
   watch at all, silently, because the variable is read by the simulator and
   nothing asserts the harness set it. `TestSimulatorExitsWithItsParent` covers
   the Google Cloud suite's own path; the equivalent for the other two clouds
   is their `shared` package's unit tests plus the wiring being identical.

3. The full-store-read class is closed: scripts/check-store-scans.sh holds
   the floor at zero, and its comment now records that every exemption the
   file ever carried — including the final seven — turned out to be a keyed
   lookup on a second reading. A new scan on a request path is a regression;
   convert it to a GenerationIndex rather than writing a new exemption
   paragraph.

4. App Service is at 616 of 692 operations, and the 76 that remain were
   enumerated rather than left as "the long tail". They are, by family:

   - **Network trace / packet capture** (18 spellings: `networkTrace`,
     `networkTraces`, `startNetworkTrace`, `stopNetworkTrace`, and their
     operation-result and slot spellings). Capturing a site's packets is real
     work the simulator does not do; serving a trace means fabricating one.
   - **Process control and introspection beyond list and get**: `DELETE
     .../processes/{id}` (kill), `.../processes/{id}/modules`,
     `.../processes/{id}/dump`, across the site, instance and slot spellings.
     Not implementable, for the reason `web_processes.go` already records: the
     container engine's HTTP API exposes exactly one process primitive,
     `GET /containers/{id}/top`, and it reports no loaded modules. A module
     list would have to come from `/proc/<pid>/maps` inside the container,
     which needs a shell in the workload image (a scratch image has none) or
     the engine host's own `/proc` (unreachable when the engine runs in a
     virtual machine, so serving it would work on a Linux engine and not on
     macOS — a host-dependent API surface). The same limit stops the kill: the
     engine can signal a container's main process, not an arbitrary process
     inside it. Reopen only if the engine gains a real primitive for it.

   - **`metricdefinitions`** (4) and `outboundNetworkDependenciesEndpoints`,
     the two rule-detail reads of the recommendations family, and the declined
     `Provider_*Stacks` (6) — each answers with a series or a catalog only the
     real platform holds, in the same class as the declined catalogs below.
     Each already answers a declared 501 naming its reason.

   The blanket claim this item used to make — that *nothing* in the tail was
   implementable — was wrong, and **`recommendations`** disproved it: 13 of its
   15 operations were served on 2026-08-31 from what the simulator does hold.
   No advisory engine runs here, so the lists and histories are honestly empty;
   the filters are the client's own decisions and are recorded per scope. Only
   the two rule-detail reads need Microsoft's published copy.

   `iscloneable` (2) was the second: it is computed from the plan the site is
   placed on and the deployment slots a clone would leave behind, both of
   which this simulator holds. `resourceHealthMetadata` (6) went the other
   way on examination — the operation defines its category as the one the
   resource matches in Microsoft's Resource Health Check policy file — and
   now answers a declared 501 naming it.

   So the honest split for the 31 App Service operations still unserved is:
   **`perfcounters`** and **`phplogging`** (4), the `migrate`/`migratemysql`
   trio and the four process `dump` spellings have not been examined against
   what the site and its workload container already know, and must be before
   any of them is called blocked. The rest need a catalog, a metric series or
   a `/proc` primitive that is not there, and each says so in a declared 501.

5. Cloud Spanner admin is **closed**, not pending. Its measured number counts
   Discovery *method spellings*, not methods — the document declares most
   methods twice, an expanded `flatPath` and a `{+name}` template — so 188 of
   198 reads like ten missing methods and is five: 99 distinct methods, 94
   served, and the five unserved ones account for exactly the ten missing
   spellings. Those five are `databases.getScans`, `databases.addSplitPoints`,
   `databases.changequorum`, `sessions.adapter` and `sessions.adaptMessage`,
   each unserved because the simulator holds nothing to report — a Key
   Visualizer heatmap derived from production traffic, key-range splits on what
   is one SQLite database, a dual-region quorum with one replica, and raw
   PostgreSQL and Cassandra wire protocols it does not speak. Serving any of
   them means inventing the answer, so they belong with the declined catalogs
   below rather than on a work list. Google Cloud Billing is fully served
   (36 of 36): the account collection, sub-accounts, organization-scoped
   spellings, project links, the IAM triple, and the installation's own
   service catalog — whose SKU lists are empty because the deployment
   publishes no price sheet. Read a measured Google number as spellings before treating
   the gap as a method count.

## Consumer follow-ups in the sockerless repository

Both recorded follow-ups shipped there: the Azure Container Registry token
exchange (sockerless #926) and the build-context blob client's shared-key
credential (sockerless #927). Nothing is pending on the consumer side.

## Tooling quirks that are not simulator defects

- `route_coverage_paths_test.go` is a wire-path index whose owning test —
  `TestRouteCoveragePathsAreServed` — rejects a duplicated line and is not in
  pre-commit, which only greps the file. Editing the index by anchored
  insertion duplicates a line whenever a later edit anchors on one an earlier
  edit added. Run the SDK suite of the simulator whose index changed, not only
  the tests named after the change.

- Running two simulator suites at once starves this host's Podman: the SDK
  suite failed with `Get "http://%2Fvar%2Frun%2Fdocker.sock/_ping": context
  deadline exceeded` while the CLI suite held the engine, and the isolated
  simulator it was starting never became healthy. The engine answered normally
  a minute later and the same suite passed alone. Run them in sequence.

- This host's Podman drops `buildx` with `rpc error: ... EOF` at the
  `exporting to docker image format` step, which fails the Terraform harness
  before Terraform runs at all. `podman machine stop && podman machine start`
  clears it. Do not restart the machine while another suite is running: it
  pulls the engine out from under every container-dependent test and produces
  failures that look like the change under test.

- The Google Cloud Terraform package runs under `-timeout 300s` and takes
  163s with a warm provider cache. The first run on a cold one spends the
  difference downloading providers and dies at 307s, and what it prints is a
  goroutine dump from `runTimed`'s watchdog rather than a named failure — so
  it reads like a hang in whichever command was in flight. Measured on
  2026-08-27, both runs on the same tree. Re-run before diagnosing; if the
  warm number ever approaches the budget, raise the budget rather than
  trimming the stack, because the stack is the coverage.

- The two container engines take different blob-upload paths, so a registry
  upload change cannot be judged on a local run. Docker's `docker push` opens
  the session with POST, sends the whole blob in a single `PATCH` and finalizes
  with `PUT`; this host's Podman sends the blob on the `PUT` and never issues
  the `PATCH` at all. A refusal added to the `PATCH` therefore passed every
  local suite and broke `TestArtifactRegistryCLI_DockerLoginPushPull` on CI.
  Judge `/v2/` upload behaviour on the CI engine.

- This host's Podman container store can acquire a dangling entry that makes
  every `ContainerList(All: true)` fail with `container not known`, which is
  the call `sim.FindExistingContainers` uses for workload recovery. It
  presents as unrelated Lambda, Step Functions and container-reaper failures
  that all pass in isolation. Clear it with `docker rm -f <dangling id>`; it
  is not a simulator defect.
- Microsoft's Cosmos DB emulator is started once for the whole Azure SDK suite,
  from `TestMain`, and warms in the background while the rest of the suite runs.
  It used to be started by whichever test asked first, and the two differential
  tests each started one — so the engine ran two emulators at once on a
  two-core runner, which is precisely the contention the reaper comment in
  `cosmos_differential_test.go` describes: the second one's pgcosmos extension
  is starved and answers "still starting" until the readiness budget expires.
  That failed three runs (2026-08-21, 2026-08-23, 2026-08-24) before the shape
  was recognised. Sharing one emulator and warming it early fixed it; the
  readiness budget was deliberately *not* raised, because go test gets 13
  minutes for this suite and the step 14, so buying readiness time would trade
  a named Go failure for an opaque step kill. A run whose `-run` filter cannot
  reach either differential test skips the warm-up and pays nothing; the tests
  still boot the emulator themselves if it was not warmed, so the filter
  decides when the cost is paid and never whether the oracle is available. The
  readiness failure also classifies itself — "still starting" means host
  starvation, anything else means the emulator never answered.

- Azure CLI 2.88's `az keyvault update --set tags.<k>=<v>` issues a vault
  GET followed by a PUT that does not carry the changed tags, and
  `az keyvault show` reported a stale tag set after a server-side change.
  Verified by hand against the simulator that the server is correct in both
  cases, and that the same sequence through `az servicebus` behaves
  correctly — so this is client-side. The Key Vault CLI tests avoid those
  two commands; do not chase it as a simulator bug.

## Declined catalog work

Two surfaces were offered for vendoring across three passes and declined
each time; they are recorded here so they stop being re-proposed. Neither
is a defect — both are surfaces whose only faithful implementation is
somebody else's published data, and a partial catalog would be fabrication:

- **Microsoft.Web `Provider_*Stacks` (6 operations)** answer with
  Microsoft's published runtime-stack catalog. Unserved, with the reason
  recorded beside the `web-arm-openapi-2025-03-01` floor row.
- **Google Cloud Billing (6 of 36)** — `services.list` and
  `services.skus.list` answer with Google's public SKU catalog. The slice
  stays at its current floor.

Revisit either only if a consumer needs it; the Application Gateway WAF
rule-set catalog is the precedent for how the vendoring would be done.

## Next Recommended Slice

BUG-2798 and BUG-2799 closed. ECS services now drive durable AWS Cloud Map
registration from real task transitions and implement persisted launch
throttling, deployment circuit-breaker rollback, and CloudWatch-alarm rollback.
Official AWS SDK and AWS CLI scenarios, hard-restart regressions, and the
production-shaped HashiCorp AWS provider graph exercised the completed data
plane.

BUG-2766 remained the next independent AWS fidelity slice: implement the
published AWS Amplify Hosting `ImageOptimization` fetch, source-policy,
transformation, validation, format-negotiation, and cache contract, then prove
it through hosted requests and external image decoders. BUG-2764 remained a
host boundary: the shared Linux test image contained the real Firecracker and
squashfs tools, while the macOS Podman virtual machine exposed no nested KVM;
the capable-Linux Terraform CI cell remained mandatory.

The completed baseline retained real AWS Private Certificate Authority and
Amazon Data Firehose implementations with official SDK, AWS CLI, Terraform,
and authenticated browser coverage.

The external review's locally actionable gaps and the follow-up implementation
audit were closed. AWS Step Functions ran and cancelled real Amazon ECS and
AWS CodeBuild workloads; CodeBuild used the requested source revision,
credential, build specification, and image; AWS Amplify ran authenticated
multi-language monorepo builds with complete phase, cache, and artifact
lifecycle; Amazon RDS exposed persistent PostgreSQL, MySQL, and MariaDB native
data planes with TLS-only IAM authentication and real password rotation; and
deployed workloads used the standard SDK endpoint environment variables.
Hosted concurrency validation preserved sub-second AWS Amplify release order,
accepted Microsoft Azure's valid subnet-before-public-prefix NAT-gateway
state, and gave the real Step Functions container integrations a
cloud-shaped cold-provisioning window with useful terminal diagnostics. The
AWS SDK shard provisioned the exact configured Alpine and official AWS CLI
images before `m.Run`, so registry transfer no longer consumed that
integration's lifecycle deadline while both real containers still executed.
Explicit Amazon ECR Public coordinates reached the container runtime unchanged,
and cancellation killed the CodeBuild workload whether Docker completed its
wait through the context or error channel, so a stopped build produced no
delayed Amazon SQS side effect. The macOS/Linux Docker validation harness loaded
Buildx output and shared the container host's PID namespace; the full
production-shaped HashiCorp AWS provider graph completed apply, a real
VPC-attached Lambda invocation, refresh, and destroy through HTTPS.
The Amazon ECS integration harness loaded its real arithmetic workload through
the backend's Docker Image Load API instead of building it outside the backend
catalog; live-cloud runs required the corresponding pre-provisioned Amazon ECR
coordinate, and all six simulator-backed real-container cases passed.
The AWS external Terraform harness preserved the original request host through
Caddy for AWS Signature Version 4, serialized heavyweight packages locally,
and assigned the root, Amazon ElastiCache, and three Amazon RDS graphs to
separate hosted runners. All five HTTPS packages completed apply, real
workload or data-plane assertions, and destroy without cross-package resource
contention.
The mandatory publication audit upgraded the AWS simulator to `go-git` 5.19.2
and its current transitive graph. The complete module suite passed, and the
authenticated dependency audit reported no drift.
The shared e2e harness loaded its compiled arithmetic fixture through every
active cloud backend's Docker Image Load API, keeping the backend catalog
authoritative. The exact e2e suite and its optional second Amazon ECS
simulator-backend path passed.
The hosted publication edge then advanced `docker/login-action` to 4.6.0.
Both immutable multi-architecture publication jobs upgraded, and action
syntax, the publication contract, and the authenticated freshness audit
passed.
Native Linux workload coordinates retained Docker's
`host.docker.internal:host-gateway` alias instead of rewriting it to the
virtual machine's default gateway; rewriting remained correct for a simulator
that itself ran in a container. The official AWS SDK Step Functions
integration passed its real Amazon ECS task, AWS CodeBuild container, and
vendor AWS CLI flow.
Publication also upgraded every newly drifted SQLite and Google Cloud client
module, moved Firestore and Spanner protobuf imports to their current
canonical modules, and passed the complete official Google Cloud SDK suite.
The exact hosted Cloud Run v1 and v2 Discovery revision 20260727 documents were
also retained; their public methods, paths, and schema fields were unchanged,
and the Google simulator route, specification, and measured-coverage suite
passed against their newer descriptions.
The three console accessibility checks anchored keyboard traversal at the
loaded document before pressing Tab, so real Chromium consistently proved each
skip link was the first in-document focus target.
Explicit Lambda deployment remained intentional because AWS Lambda itself runs
only functions a caller creates. The repository retained its truthful
unaudited/non-production warning because functional validation did not
constitute an independent security audit.

The next pass should recheck the six external blockers below and resume only
when their missing credentials, upstream API coordinates, published schemas,
provider transports, or external repository become available. Mobile push and
SMS remained under BUG-2712 because no available public AWS configuration
exposed the carrier/provider primitives needed for faithful delivery.

## Externally Blocked Work

- BUG-1075 retained authenticated Google Cloud Run, Azure Container Apps,
  Azure Functions, Lambda service-mesh, and Azure identity-backed live-cloud
  cells that required operator credentials.
- BUG-2646 retained Google's publication of Cloud Run worker-pool scaling
  members in the Discovery document.
- BUG-1345 retained the upstream AzureAD Terraform provider's missing
  Microsoft Graph endpoint override. Checked again on 2026-08-23: the
  provider's latest release is v3.9.0 (2026-06-18) and its changelog records
  no endpoint or base-URI override, so the gate is unchanged.
- BUG-2523 and BUG-2441 remained owned by the external Bleephub repository,
  which was not present in this workspace.

## Durable Validation Contract

- Simulator endpoints were exercised through official SDK, vendor CLI, and
  Terraform surfaces in the same change.
- Tests differed between simulator and cloud only in endpoint and credential
  coordinates.
- Production builds created every frontend before any UI-bearing Go binary.
- Workflow changes kept every ordinary job at or below 15 minutes and
  preserved exact AWS CLI and SDK shard coverage.
- Dependency freshness retained authenticated GitHub API requests in both its
  Bash and Zsh portability passes.
- Every observed failure or warning was fixed or recorded with evidence in
  [BUGS.md](BUGS.md).
