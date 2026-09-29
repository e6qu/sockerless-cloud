# DO NEXT

## Standing work

- **An engine-touching change is not verified by a local run.** The developer
  machine runs Podman behind the docker socket and CI runs moby; Podman
  implements volume options in `mount(8)` that moby passes to `mount(2)`, which
  is how `o=loop` passed locally and failed on CI. Prefer a mechanism neither
  engine has to interpret, and when only CI can confirm a claim, say so
  instead of reporting the local run as the proof.
- **Serve what a re-vendor adds.** The daily specification refresh pushes onto
  the open pull request; a moved declared total has to be served or declared,
  and a served count that falls has to be shown to be a withdrawal, with the
  floor comment naming which methods moved and why. Read a measured Google
  number as method spellings — most methods are declared twice, an expanded
  `flatPath` and a `{+name}` template — before treating a gap as a method
  count.
- **Keep the negative control when a gate is added or moved.** A gate earns its
  place by being watched to fail once on a planted violation of its own shape,
  and a gate whose scan set can go empty must exit non-zero rather than green.
- **The AWS SDK shards' next lever is the fixed per-job cost, not the split.**
  The four shards spend about 815s running tests and 2,706s of wall time; each
  pays its own base-image load, test-binary pre-build and cache restore. If the
  fifteen-minute cap gets tight, attack the duplicated setup.
- **Add an `iamCreatesItsOnlyDeclaredType` entry when a new create needs
  one**, and creation calls to `iamSeedDerivationFixtures` when a new
  derivation family resolves through state, keyed
  `<service>:<operation>:<member>`. Do not decide the "names no resource" class
  by inspecting member names; that was built, measured, and discarded for
  widening real grants.
- **Watch the AWS WAF sample sweep on a busy deployment.** Request sampling
  writes `wafv2_sampled_requests` on the request path while the sweep deletes
  from it in 500-row transactions, so the two contend under load; a busy
  database ends the sweep early. If the table stops shrinking, use a smaller
  batch with a pause between batches for the high-write stores, not a bigger
  one.

## Tooling quirks that are not simulator defects

- `route_coverage_paths_test.go` is a wire-path index whose owning test
  rejects a duplicated line and is not in pre-commit. Editing it by anchored
  insertion duplicates a line whenever a later edit anchors on one an earlier
  edit added; run the SDK suite of the simulator whose index changed.
- Running two simulator suites at once starves a developer host's Podman: the
  SDK suite fails with `Get "http://%2Fvar%2Frun%2Fdocker.sock/_ping": context
  deadline exceeded` while the CLI suite holds the engine. Run them in
  sequence.
- A failure naming `podman.service` ("Stopping 'podman.service', but its
  triggering units are still active") is the local Podman machine going down
  mid-run; it takes out whatever test is running, so it looks like a different
  flake each time. Read the whole output before treating one as a defect.
- Podman can drop `buildx` with `rpc error: ... EOF` at the `exporting to
  docker image format` step, which fails the Terraform harness before
  Terraform runs. `podman machine stop && podman machine start` clears it;
  never restart the machine while another suite is running.
- The Google Cloud Terraform package runs under `-timeout 300s` and takes
  about 163s with a warm provider cache; a cold cache spends the difference
  downloading providers and dies at the deadline with a goroutine dump from
  `runTimed`'s watchdog. Re-run before diagnosing.
- Docker's `docker push` sends the whole blob in a single `PATCH` and
  finalizes with `PUT`; Podman sends the blob on the `PUT` and never issues the
  `PATCH`. Judge `/v2/` upload behaviour on the CI engine.
- A Podman container store can acquire a dangling entry that makes every
  `ContainerList(All: true)` fail with `container not known`, which is the
  call `sim.FindExistingContainers` makes. It presents as unrelated Lambda,
  Step Functions and container-reaper failures that pass in isolation; clear
  it with `docker rm -f <dangling id>`.
- Microsoft's Cosmos DB emulator starts once for the whole Azure SDK suite
  from `TestMain` and warms in the background. Its readiness failure classifies
  itself: "still starting" means host starvation, anything else means the
  emulator never answered. The readiness budget stays where it is, because
  `go test` gets thirteen minutes for the suite and the step fourteen.
- Azure CLI 2.88's `az keyvault update --set tags.<k>=<v>` issues a vault GET
  followed by a PUT that does not carry the changed tags, and `az keyvault
  show` reports a stale tag set after a server-side change. The simulator
  answers correctly; the Key Vault CLI tests avoid those two commands.

## Declined catalog work

- **Google Cloud Billing SKUs** — `services.skus.list` answers with Google's
  public SKU catalog. This installation publishes no price sheet, so the
  listing is served and empty, pinned by a test so it never becomes fabricated
  pricing. Revisit only if a consumer needs the catalog; the Application
  Gateway WAF rule-set catalog is the precedent for how to vendor one.

## Externally blocked

- **BUG-2646** — Google has not published the Cloud Run worker-pool scaling
  members in the Discovery document.
- **BUG-2712** — Amazon SNS SMS and mobile push need a carrier and Apple's and
  Google's hosts, which no AWS API provisions.
- **BUG-2977** and **BUG-3046** — each needs a capture from a real cloud
  account: an unmigrated Azure Storage account's `default` migration body, and
  a Cloud Bigtable memory layer's reported size.
