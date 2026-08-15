# DO NEXT

1. App Service: instances and processes are read from the live workload
   container (519 of 692). Three recorded deferrals remain, and the fourth was
   analysed rather than accepted: backup and restore want a real Blob round
   trip; App Service Environments and Kube Environments are two new top-level
   resources; and the detector family is 24 operations, of which the
   "simulator cannot compute this" claim is demonstrably wrong for at least the
   container-health analyses, because the simulator holds real restart and
   failure state in the workload container and real configuration state in the
   site record. Sixteen process operations are deliberately unserved with a
   demonstrated reason recorded beside the floor — the container engine exposes
   one process-inspection primitive, which reports no modules and no dumps.
2. BUG-38: the shared registry-trust helper no longer silently no-ops, which
   exposes two latent defects elsewhere — the Google Cloud build push test
   still takes the insecure path and now fails loudly, and the AWS and Google
   harness makefiles lack the shared engine-host temporary directory whose
   absence made Azure workloads mount empty directories.
3. BUG-20 and BUG-36: the container reaper leaves workload containers running
   for hours after a run ends, and each cloud's suites still build their
   simulator to one shared path where a build can overwrite a binary another
   suite is executing. Both are the same family as the harness collisions
   already fixed.
4. BUG-39 and BUG-40: retire the sockerless-invented Cosmos routing header now
   that the account's advertised endpoint works, and refuse two accounts
   sharing a name the way the service does.
5. BUG-22, BUG-27, BUG-35 and BUG-37: Artifact Registry accepts chunked
   uploads the real service refuses and issues a token for any scope it is
   asked for, a virtual network drops subnets declared inline on it, and an
   Amazon ECR pull through a cache rule is never hydrated.
6. BUG-32's consumer note: the sockerless AWS backend warns and continues when
   repository creation fails, which now surfaces as a loud push failure rather
   than a silent success — correct, and matching the real service.
7. The next measured Google ratchets are Cloud Spanner admin (188 of 198) and
   Google Cloud Billing (6 of 36), the latter still carrying the declined
   SKU-catalog decision below.

## Consumer follow-ups in the sockerless repository

- `backends/azure-common/acr_auth.go` asks Microsoft Entra for the
  `https://<registry>.azurecr.io/.default` scope and puts that raw token on
  `/v2/` as a Bearer. Real Azure Container Registry refuses it: the Entra
  token must be exchanged at `/oauth2/exchange` for a refresh token and then
  at `/oauth2/token` for an access token, and the audience is
  `https://containerregistry.azure.net`. The simulator now enforces this, so
  the two agree about what is wrong. Nothing fails in CI today because no
  harness sets `SOCKERLESS_AZURE_ACR_ENDPOINT`, but Azure Container Apps and
  Azure Functions image operations would fail against a real registry. Fix it
  in the sockerless repository alongside the next pin bump, not here.

## Tooling quirks that are not simulator defects

- This host's Podman container store can acquire a dangling entry that makes
  every `ContainerList(All: true)` fail with `container not known`, which is
  the call `sim.FindExistingContainers` uses for workload recovery. It
  presents as unrelated Lambda, Step Functions and container-reaper failures
  that all pass in isolation. Clear it with `docker rm -f <dangling id>`; it
  is not a simulator defect.
- Microsoft's Cosmos DB emulator container can fail to become ready inside the
  differential tests' 280-second budget when several test suites run at once
  (`pgcosmos extension is still starting`). The budget and its probe are
  sound; the machine was starved. Re-run on a quiet host before suspecting the
  simulator.

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
  Microsoft Graph endpoint override.
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
