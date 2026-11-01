# DO NEXT

## Next

- **Google Cloud methods without a sourced permission** (BUG-3769): the 391
  Owner-only methods whose reference pages state no usable permission take
  theirs from the other tables those services publish.
- **The fidelity gaps the comment pass surfaced** (BUG-3778 to 3784, 3786 to 3789 and 3791), service
  by service, each with its SDK, CLI and Terraform tests.

- **M1 of `PLAN.md`: a complete 3.x release, then the App token**
  (BUG-3465). Merge the release pull request release-please opens after this
  pull request and confirm its run attaches darwin binaries and the AWS images,
  with `scripts/verify-release-complete.sh` passing; then the owner creates a
  GitHub App with contents, pull-requests and workflows permissions, stores
  its ID and private key as Actions secrets, and release-please runs with its
  installation token.
- **The "build your own cloud" write-up.** Turn
  `docs/so-you-want-to-build-a-cloud.md` into a presentation. Keep its gap list
  in step with `PLAN.md` M3 and its LocalStack and Floci tables dated and
  sourced. Change a tutorial step only together with the test that runs it.
- **Core-service gaps.** A managed cache engine for Amazon ElastiCache and
  Azure Cache for Redis (BUG-3732, PLAN M3 item 15), Azure Managed Disks,
  Cloud Monitoring (BUG-3522) and Filestore.
- **M4: the remaining cleanups.** The AWS ARN builder and parser and the
  tag-set helpers; placement for the sites BUG-3609 names; clearing the 39
  clone groups `dupl` finds at 120 tokens, then lowering
  `scripts/simulators-dupl.sh` to 120.
- **M3: the next shared libraries in ranked order** — the long-running
  operation tracker, asymmetric cryptography in `sim/simkms`, time-bounded
  soft delete (BUG-3606), then the build runner.
- **Azure.** Fix the Azure console's header disclosure flake (BUG-3690).
  Close the Azure service gaps the M2 work left in view: Event
  Grid's Azure Functions, Event Hubs and Hybrid Connections destinations
  (BUG-3535), the Functions host's other stacks (BUG-3237), App Service site
  homes shared across runs (BUG-3631), a job replica's own environment
  (BUG-3650) and Azure Container Registry cache rules (BUG-3612).
- **Follow-ups of the M2 libraries.** Reset an AWS Lambda
  environment in place (BUG-3700), keep an event source mapping's qualifier
  (BUG-3701), and serve DynamoDB Streams as a `funcrt` source (BUG-3603);
  deliver Amazon SNS to SQS and Lambda through `sim.Server.Call` (BUG-3641);
  run Cloud Run liveness probes (BUG-3630); serve Cloud Tasks over gRPC
  (BUG-3710).
- **CI cost**: fold the simulator shards back from the parallel suites'
  measured durations (BUG-3437) and stop repeating builds and image loads
  per job (BUG-3151, BUG-3575, BUG-3515); watch the browser jobs, which
  build a second simulator as the identity provider (BUG-3669); stop the
  dependency-quarantine hook downloading a Go toolchain per run (BUG-3730).

## Standing work

- **Serve what a re-vendor adds.** The daily specification refresh pushes onto
  the open pull request; serve or declare every moved total, and show a
  falling served count to be a withdrawal, naming in the floor comment which
  methods moved and why. A measured Google number counts method spellings —
  most methods appear twice, as an expanded `flatPath` and a `{+name}`
  template.
- **Keep the negative control.** Watch a new or moved gate fail once on a
  planted violation of its own shape; a gate whose scan set can go empty exits
  non-zero.
- **Verify engine-touching changes on the CI engine.** CI runs moby; a
  developer machine may run Podman behind the docker socket, and the two
  interpret volume options, uploads and image stores differently. Prefer a
  mechanism neither engine interprets, and when only CI can confirm a claim,
  say so.
- **IAM derivation fixtures.** Add an `iamCreatesItsOnlyDeclaredType` entry
  when a new create needs one, and creation calls to
  `iamSeedDerivationFixtures`, keyed `<service>:<operation>:<member>`, when a
  new derivation family resolves through state. Never decide the "names no
  resource" class from member names: that widened real grants.
- **Watch the AWS WAF sample sweep on a busy deployment.** Request sampling
  writes `wafv2_sampled_requests` while the sweep deletes from it in 500-row
  transactions, and a busy database ends the sweep early. If the table stops
  shrinking, use smaller batches with a pause between them, not bigger ones.

## Tooling quirks that are not simulator defects

- In an agent sandbox, unset gcloud's proxy variables and make `*.localhost`
  resolve to loopback before running the suites (`STATUS.md`, Running the
  suites in a sandbox). A console browser suite there needs `pasta` and
  unprivileged user namespaces like CI's runner.
- gcloud retries a declared 501 with backoff, so a CLI test that asserts one
  takes about 30 seconds.
- `simulator-{gcp,azure}/sdk-tests/route_coverage_paths_test.go` is a
  wire-path index whose owning test rejects a duplicated line, outside
  pre-commit. Anchored-insertion edits duplicate a line when a later edit
  anchors on one an earlier edit added; run that simulator's SDK suite.
- The Google Cloud Terraform package runs under `-timeout 300s` and takes
  about 163 s with a warm provider cache; a cold cache dies at the deadline
  with a goroutine dump from `runTimed`'s watchdog. Re-run before diagnosing.
- The Azure SDK suite starts Microsoft's Cosmos DB emulator once from
  `TestMain`. Its readiness failure classifies itself: "still starting" means
  host starvation, anything else means the emulator never answered.
- Azure CLI 2.88's `az keyvault update --set tags.<k>=<v>` sends a PUT without
  the changed tags, and `az keyvault show` reports a stale tag set after a
  server-side change. The simulator answers correctly; the Key Vault CLI tests
  avoid both commands.
- **On a Podman host:**
  - Two simulator suites at once starve the engine: the SDK suite fails with
    `Get "http://%2Fvar%2Frun%2Fdocker.sock/_ping": context deadline
    exceeded` while the CLI suite holds it. Run them in sequence.
  - A failure naming `podman.service` ("Stopping 'podman.service', but its
    triggering units are still active") is the Podman machine going down
    mid-run; it looks like a different flake each time.
  - `buildx` can drop with `rpc error: ... EOF` at `exporting to docker image
    format`, failing the Terraform harness before Terraform runs; `podman
    machine stop && podman machine start` clears it, never while another
    suite runs.
  - `docker push` sends a blob in one `PATCH` and finalizes with `PUT`;
    Podman sends it on the `PUT`. Judge `/v2/` upload behaviour on moby.
  - A dangling entry in the container store fails every
    `ContainerList(All: true)` with `container not known`, which presents as
    unrelated AWS Lambda, Step Functions and reaper failures; clear it with
    `docker rm -f <dangling id>`.

## Declined catalog work

- **Google Cloud Billing SKUs** — `services.skus.list` serves an empty listing,
  pinned by a test, because this installation publishes no price sheet.
  Revisit only if a consumer needs the catalog; the Application Gateway WAF
  rule-set catalog shows how to vendor one.

## Externally blocked

- **BUG-2646** — Google has not published the Cloud Run worker-pool scaling
  members in the Discovery document.
- **BUG-2712** — Amazon SNS SMS and mobile push need a carrier and Apple's and
  Google's hosts, which no AWS API provisions.
- **BUG-2977** and **BUG-3046** — each needs a capture from a real cloud
  account: an unmigrated Azure Storage account's `default` migration body, and
  a Cloud Bigtable memory layer's reported size.
