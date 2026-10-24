# DO NEXT

## Next

- **M1 of `PLAN.md`: releases flow again.** After this pull request merges,
  the owner removes `autorelease: pending` from the stale 2.4.1 release pull
  request (#284) and confirms the next push to `main` opens a fresh release
  pull request whose release run creates its tag, Release and artifacts; then
  creates the GitHub App release-please runs with, which holds the
  `workflows` permission (BUG-3465).
- **M2: finish the stage 5 extractions** this pull request carries —
  `sim/msgq`'s waiter with Cloud Tasks, the host-routed front end in
  `sim/lbplane`, `sim/eventbus` with Cloud Scheduler, `sim/jobrun` and
  `sim/funcrt` — and close their rows (BUG-3600 to BUG-3605).
- **M3: the new shared libraries in ranked order**, starting with the keyed
  change signal (BUG-3608), the long-running operation tracker and
  asymmetric cryptography in `sim/simkms`.
- **M4: cleanups**, starting with the AWS body decoding and error envelope
  (BUG-3610), the shared test helpers (BUG-3611), the invented timers
  (BUG-3607) and the copy-paste threshold at 150 tokens.
- **CI cost**: fold the simulator shards back from the parallel suites'
  measured durations (BUG-3437) and stop repeating builds and image loads
  per job (BUG-3151, BUG-3575, BUG-3515).

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
