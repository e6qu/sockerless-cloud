# BUGS

Open: 8. Resolved: 4.

## Open

Bugs 2909, 2932, 2646, 2712, 2764, 2928, 2924, and 1345 moved here with
the simulators from the sockerless monorepo, keeping their IDs.

| ID | Sev | Area | Pattern | One-liner |
|----|-----|------|---------|-----------|
| 2909 | P2 | AWS simulator IAM enforcement leaves 195 served operations authorized against `"*"` | the resource-derivation gap BUG-2907 closed for five services is measured across the rest, not closed for them | Thirty services derive their resource from the types AWS declares and the ARN format published beside each — Amazon Data Firehose, AWS Security Token Service and Application Auto Scaling joined the generated table, Amazon EventBridge gained the alias table its Name/Rule abbreviations needed, and Amazon DynamoDB reads the export and import family's TableArn — and the per-request cases that predated the table are gone but for AWS Lambda. 1,779 of the 1,974 served operations that authorize against a resource type derive it; the remaining 195 still request a literal `"*"`. `TestIAMResourceDerivationCoverage` ratchets the number and prints the per-service remainder, largest first: Amazon EC2 (55), AWS Glue (35), Amazon RDS (27), Amazon DynamoDB (18), AWS Systems Manager (17). What is left is mostly an operation that creates its resource, so carries no identifier for it yet, names something other than the resource it authorizes against, or names it by an ARN in a shape the coverage probe cannot express — those derive for real requests and are pinned by `TestIAMResourceARNs_*` behavior tests; the comment beside `iamDerivationCoverageFloor` states each service's remaining class. |
| 2932 | P3 | Three AWS Smithy patterns are stricter than the service they describe, so the simulator cannot satisfy both | the vendored model is authoritative for the simulator, but where it contradicts documented service behavior, matching the model would make the simulator less faithful, not more | The runtime pattern check (BUG-2931) reports three responses whose values AWS itself returns. Amazon EventBridge names the managed secret backing a connection `events!connection/<name>/<uuid>`, and `SecretsManagerSecretArn` admits no `!`. AWS Certificate Manager's `DescribeCertificate` reports the issuing authority as an AWS Private Certificate Authority ARN, and the generic `Arn` shape it is typed with requires the service segment to be `acm`. Amazon CloudWatch Logs reports a configuration template's `resourceType` in CloudFormation spelling (`AWS::WAFv2::WebACL`), and `ResourceType` admits no `:`. Each is allowlisted in `simulator-aws/spec-violation-allowlist.txt` against this entry rather than "fixed" by emitting a value the service never emits. The allowlist shrinks if a later model revision widens the patterns, which is the only thing that should close this. |
| 2928 | P3 | AWS Lambda invocations exceed their own timeout on this development host, and the tests that depend on them fail | the error text is retained now and it is specific: `{"errorMessage":"Task timed out after 3.00 seconds","errorType":"Runtime.ExitError"}`. The function hits the three-second timeout it was configured with, and the invocation returns an empty payload where the test expects the handler's output. The earlier instances — three Lambda tests in one SDK shard, an Amazon RDS data-plane test, an Amazon ECS command-line test — were all read as container-runtime contention, and that reading is now wrong: this reproduces with the test run alone, so nothing else was competing with it | Two things are ruled out. The simulator does not charge container startup against the function budget: `StartContainerSync` returns before the timer starts, which matches real AWS Lambda, where a cold start is not billed against the handler timeout. And the `aws-sdk-go-v2` upgrade did not cause it — the same test fails identically on the previous SDK, checked by stashing the upgrade and re-running. What remains is the host: every hosted CI run in this session passed this shard, and these same Lambda tests passed on this machine earlier today, so the local container runtime has degraded since. `podman machine restart` is the recorded remedy for that class and is the next thing to try. Close this when a hosted run reproduces it, or when a restarted local runtime does not. |
| 2924 | P3 | Two live VPCs sharing a CIDR cannot both exist as Docker networks, though AWS allows it | the simulator makes a VPC network's subnet the VPC's own CIDR, and a host subnet is exclusive where an AWS CIDR is not | The half this was mostly observed as is fixed: a network holding the wanted subnet, carrying the simulator label, with no containers attached and a run id other than this process's, belongs to a simulator that exited — `EnsureVPCNetwork` reclaims it and retries the create once, which is safe because all four conditions must hold together. A regression reproduces the original failure exactly (`netavark (exit code 1): subnet 10.201.0.0/16 is already used on the host or by another config`) and covers the networks the reclaim must not touch: one belonging to the running simulator, and one the project did not create. What remains is the design question — two *live* VPCs sharing a CIDR still conflict, and resolving it means allocating the Docker subnet independently of the AWS CIDR, which would make the container's real address differ from the ENI address reported unless the two are mapped. Deleting a live peer's network instead would trade a clear failure for a confusing one. |
| 2764 | P2 | Google Compute Engine Terraform validation on macOS | the guest does not finish booting on a macOS host | Two causes previously hidden behind this entry were separate defects and are fixed: the poisoned asset cache (BUG-2911) and the architecture-blind kernel check that rejected every arm64 image (BUG-2912). With both closed, an arm64 container on this host downloads the correct kernel and Firecracker boots it — the console log reaches the ARM64 hardware-breakpoint and ASID-allocator lines — and the apply then fails on the boot not completing within its period, with no `/dev/kvm` in the macOS Podman virtual machine. The full real Compute Engine apply therefore remains a mandatory capable-Linux CI gate rather than a locally executable macOS gate, but the failure it reports is now the real one. The packet mirroring resources are validated by their own Terraform test, which needs no booted guest. |
| 2646 | P3 | GCP simulator Cloud Run worker-pool scaling | upstream publication lag, not a simulator defect | The Cloud Run v2 `WorkerPoolScaling` members `scalingMode`, `minInstanceCount`, and `maxInstanceCount` are now modelled and covered end to end (SDK wire round-trip, CLI, and a real `hashicorp/google` 7.36.0 Terraform apply → `plan -detailed-exitcode` = 0). What remains open is upstream: the newest live Cloud Run Discovery document (revision 20260717, fetched and checked) and the published REST reference still declare only `manualInstanceCount`, even though gcloud's own generated client and the GA provider both send all four members. The runtime spec validator therefore reports six `unknown-field` keys, allowlisted in `simulator-gcp/spec-violation-allowlist.txt` under this ID. Close this and drop those six entries when Google publishes the members in the Discovery document. |
| 1345 | P2 | AzureAD Terraform provider | upstream blocker | The `hashicorp/terraform-provider-azuread` provider still lacks a supported Microsoft Graph API endpoint override, so AzureAD/Entra Terraform resources cannot be tested against the Azure simulator until upstream adds it. |
| 2712 | P2 | AWS simulator outbound delivery protocols | external carrier and mobile-push providers remain unavailable | Amazon SNS email and email-json subscriptions use real SMTP, while Amazon Data Firehose now implements its complete vendored 12-operation API and performs IAM-authorized, optionally KMS-encrypted, buffered Amazon S3 delivery for direct writes, Amazon SNS subscriptions, and Amazon CloudWatch metric streams. SMS still cannot reach a carrier and mobile-push subscriptions cannot reach Apple/Google providers because their provider credentials and delivery endpoints are not represented by an available public AWS contract. SMS sandbox creation fails loudly instead of manufacturing a verification code. Close this only when those external provider primitives can be configured through faithful AWS APIs. |

## Resolved history

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
