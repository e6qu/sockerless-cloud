# DO NEXT

## Next

- **Stage 5 of `PLAN.md`: distributed.** The agent on another host, reached
  over its API with its minted token, and networks that span hosts; the
  same-range gap (two live VPCs over one CIDR on one engine) lifts there.
- **Close the no-privilege gaps `BUGS.md` holds**: NAT gateway, Cloud NAT and
  Azure NAT Gateway data planes as a sidecar proxy per network; security
  groups between two containers on one network (microVM backend only);
  cloud-init for Compute Engine `user-data` and Azure `customData`; the
  fixed link-local metadata address; packet capture and packet mirroring
  data paths; managed block volume size and filesystem on the container
  backend.
- **Fold the simulator CI shards back** from the first parallel run's per-test
  durations (BUG-3437), and stop repeating base-image loading, pre-builds and
  the Caddy install per job (BUG-3151).

## Standing work

- **Verify engine-touching changes on the CI engine.** Developer machines run
  Podman behind the docker socket and CI runs moby; Podman implements volume
  options in `mount(8)` that moby passes to `mount(2)`, which is how `o=loop`
  passed locally and failed on CI. Prefer a mechanism neither engine
  interprets, and when only CI can confirm a claim, say so.
- **Serve what a re-vendor adds.** The daily specification refresh pushes onto
  the open pull request; serve or declare every moved total, and show a falling
  served count to be a withdrawal, naming in the floor comment which methods
  moved and why. A measured Google number counts method spellings — most
  methods appear twice, as an expanded `flatPath` and a `{+name}` template.
- **Keep the negative control.** Watch a new or moved gate fail once on a
  planted violation of its own shape; a gate whose scan set can go empty exits
  non-zero.
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
- Two simulator suites at once starve a developer host's Podman: the SDK suite
  fails with `Get "http://%2Fvar%2Frun%2Fdocker.sock/_ping": context deadline
  exceeded` while the CLI suite holds the engine. Run them in sequence.
- A failure naming `podman.service` ("Stopping 'podman.service', but its
  triggering units are still active") is the local Podman machine going down
  mid-run; it looks like a different flake each time. Read the whole output
  first.
- Podman can drop `buildx` with `rpc error: ... EOF` at `exporting to docker
  image format`, failing the Terraform harness before Terraform runs.
  `podman machine stop && podman machine start` clears it; never restart the
  machine while another suite runs.
- The Google Cloud Terraform package runs under `-timeout 300s` and takes
  about 163 s with a warm provider cache; a cold cache dies at the deadline
  with a goroutine dump from `runTimed`'s watchdog. Re-run before diagnosing.
- `docker push` sends a blob in one `PATCH` and finalizes with `PUT`; Podman
  sends it on the `PUT` with no `PATCH`. Judge `/v2/` upload behaviour on the
  CI engine.
- A dangling entry in a Podman container store fails every
  `ContainerList(All: true)` — the call `sim.FindExistingContainers` makes —
  with `container not known`, which presents as unrelated Lambda, Step
  Functions and reaper failures that pass in isolation. Clear it with
  `docker rm -f <dangling id>`.
- The Azure SDK suite starts Microsoft's Cosmos DB emulator once from
  `TestMain`. Its readiness failure classifies itself: "still starting" means
  host starvation, anything else means the emulator never answered. The
  readiness budget stays put: `go test` gets thirteen minutes, the step
  fourteen.
- Azure CLI 2.88's `az keyvault update --set tags.<k>=<v>` sends a PUT without
  the changed tags, and `az keyvault show` reports a stale tag set after a
  server-side change. The simulator answers correctly; the Key Vault CLI tests
  avoid both commands.

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
