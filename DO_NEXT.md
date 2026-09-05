# DO NEXT

## After this branch merges

1. **Re-pin `sim` to the merged commit.** The simulators pin
   `github.com/e6qu/sockerless-cloud/sim` at the pseudo-version of the branch
   commit that introduced it, which the module proxy caches forever but which
   no branch will reference once the pull request is squash-merged. Pin the
   `main` commit in all three simulators
   (`go get github.com/e6qu/sockerless-cloud/sim@<sha>`), run
   `scripts/check-support-module-pins.sh` and `scripts/check-installable-build.sh`,
   and ship it with the next change. The same applies to `realexec` and
   `ui-auth`, whose pins were moved to the pre-merge `main` commit in the same
   change.

2. **State each cloud's stop grace from its own configuration.** The grace a
   workload gets between SIGTERM and SIGKILL is a caller-stated value now
   (`sim.StopContainer`'s `grace`, `ContainerConfig.CancelGracePeriod`), and
   the values passed are the ones each cloud's framework copy used to hardcode:
   one second for an Amazon ECS task stop, ten seconds for Cloud Run and Azure
   Container Apps stops, five seconds on cancellation everywhere but AWS. The
   real services define them — an Amazon ECS container definition's
   `stopTimeout` (default 30s), Cloud Run's documented ten seconds, an Azure
   Container Apps revision's `terminationGracePeriodSeconds` (default 30s) —
   and BUG-2970 records the gap.

## Standing work

- **Serve what a re-vendor adds.** The daily specification refresh pushes onto
  the open pull request; a moved declared total has to be served or declared,
  and a served count that falls has to be shown to be a withdrawal, with the
  floor comment naming which methods moved and why. Read a measured Google
  number as method spellings — most methods are declared twice, an expanded
  `flatPath` and a `{+name}` template — before treating a gap as a method
  count.
- **Keep the negative control when a gate is added or moved.** Every gate has
  been shown to fail on a planted violation of its own shape, and a gate whose
  scan set can go empty exits non-zero rather than green. A new gate earns its
  place by being watched to fail once.
- **The AWS SDK shards' next lever is the fixed per-job cost, not the split.**
  The four shards spend about 815s running tests and 2,706s of wall time; each
  pays its own base-image load, test-binary pre-build and cache restore. If the
  fifteen-minute cap gets tight again, the duplicated setup is what to attack.
- **Add a `iamCreatesItsOnlyDeclaredType` entry when a new create needs
  one**, and creation calls to `iamSeedDerivationFixtures` when a new
  derivation family resolves through state, keyed
  `<service>:<operation>:<member>`. Do not try to decide the "names no
  resource" class by inspecting member names; that was built, measured, and
  discarded for widening real grants.

## Tooling quirks that are not simulator defects

- `route_coverage_paths_test.go` is a wire-path index whose owning test
  rejects a duplicated line and is not in pre-commit. Editing the index by
  anchored insertion duplicates a line whenever a later edit anchors on one an
  earlier edit added; run the SDK suite of the simulator whose index changed.
- Running two simulator suites at once starves this host's Podman: the SDK
  suite fails with `Get "http://%2Fvar%2Frun%2Fdocker.sock/_ping": context
  deadline exceeded` while the CLI suite holds the engine. Run them in
  sequence.
- This host's Podman drops `buildx` with `rpc error: ... EOF` at the
  `exporting to docker image format` step, which fails the Terraform harness
  before Terraform runs. `podman machine stop && podman machine start` clears
  it; never restart the machine while another suite is running.
- The Google Cloud Terraform package runs under `-timeout 300s` and takes
  about 163s with a warm provider cache; a cold cache spends the difference
  downloading providers and dies at the deadline with a goroutine dump from
  `runTimed`'s watchdog. Re-run before diagnosing.
- Docker's `docker push` opens the upload with POST, sends the whole blob in a
  single `PATCH` and finalizes with `PUT`; this host's Podman sends the blob on
  the `PUT` and never issues the `PATCH`. Judge `/v2/` upload behaviour on the
  CI engine.
- This host's Podman container store can acquire a dangling entry that makes
  every `ContainerList(All: true)` fail with `container not known`, which is
  the call `sim.FindExistingContainers` makes. It presents as unrelated Lambda,
  Step Functions and container-reaper failures that pass in isolation; clear it
  with `docker rm -f <dangling id>`.
- Microsoft's Cosmos DB emulator is started once for the whole Azure SDK suite
  from `TestMain` and warms in the background. Its readiness failure classifies
  itself: "still starting" means host starvation, anything else means the
  emulator never answered. The readiness budget stays where it is, because
  `go test` gets thirteen minutes for the suite and the step fourteen.
- Azure CLI 2.88's `az keyvault update --set tags.<k>=<v>` issues a vault GET
  followed by a PUT that does not carry the changed tags, and `az keyvault
  show` reports a stale tag set after a server-side change. Verified against
  the simulator that the server is correct; the Key Vault CLI tests avoid
  those two commands.

## Declined catalog work

- **Google Cloud Billing SKUs** — `services.skus.list` answers with Google's
  public SKU catalog. This installation publishes no price sheet, so the
  listing is served and empty, pinned by a test so it never becomes fabricated
  pricing. Revisit only if a consumer needs the catalog; the Application
  Gateway WAF rule-set catalog is the precedent for how vendoring is done.

## Externally blocked

- **BUG-1075** — authenticated live-cloud cells for Cloud Run, Azure Container
  Apps, Azure Functions, the Lambda service mesh and Azure identity need
  operator credentials.
- **BUG-2646** — Google has not published the Cloud Run worker-pool scaling
  members in the Discovery document.
- **BUG-1345** — the upstream AzureAD Terraform provider carries no Microsoft
  Graph endpoint override (latest release v3.9.0 checked; changelog records
  none).
- **BUG-2712** — Amazon SNS SMS and mobile push need a carrier and Apple's and
  Google's hosts, which no AWS API provisions.
- **BUG-42** — the shared azurerm Terraform stack's Firecracker guest never
  reaches userspace on this arm64 host; CI's amd64 Linux cell runs it.
