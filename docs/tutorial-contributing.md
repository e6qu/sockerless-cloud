# Tutorial: adding to a cloud

This tutorial walks one real change from specification to pull request:
Amazon EC2 regional NAT gateways. A NAT gateway created with
`AvailabilityMode` `regional` spans the VPC's Availability Zones instead of
sitting in one subnet. Every step names the file or command the change used,
so you can follow it in the repository's history (`git log --grep 'regional
NAT'`) and repeat the steps for your own change.

[Maintaining](maintaining.md) describes the machinery the steps run through;
[`AGENTS.md`](../AGENTS.md) holds the rules.

## Step 1: Start from the records

```sh
cat STATUS.md DO_NEXT.md
grep -n "regional NAT" BUGS.md
```

The change started from a `BUGS.md` row: CreateNatGateway refused `regional`
with `UnsupportedOperation`. A change either closes a row or adds one, and
updates the continuity files in the same commit.

## Step 2: Read the contract in the vendored specification

The EC2 model is already vendored, so the operation's shape is one `jq` away:

```sh
zcat specs/cloud-api/aws/ec2.smithy.json.gz \
  | jq '.shapes["com.amazonaws.ec2#CreateNatGatewayRequest"].members | keys'
zcat specs/cloud-api/aws/ec2.smithy.json.gz \
  | jq -r '.shapes["com.amazonaws.ec2#CreateNatGatewayRequest"].members.AvailabilityMode.traits["smithy.api#documentation"]'
```

The model gives the members (`AvailabilityMode`, `VpcId`,
`AvailabilityZoneAddresses`), the response shape (`NatGateway` with
`AutoProvisionZones`, `AutoScalingIps` and per-zone `NatGatewayAddress`
fields) and the failure codes. It doesn't give the behaviour: how a regional
gateway follows the VPC's subnets, or what an uncovered zone does. For that:
- read the documentation pages vendored beside the model (`specs/cloud-api/aws/SOURCES.md`), or vendor the page that states it with `scripts/fetch-aws-doc.sh`;
- read the Terraform provider's acceptance tests, which run against the real cloud.

What no source states becomes a `BUGS.md` row naming the inference. For this
change that was BUG-3754.

For a service whose specification isn't vendored yet, vendor it first:
[Specifications → Adding or refreshing a specification](specifications.md#adding-or-refreshing-a-specification).

## Step 3: Write the handler

A service slice is one or more `simulator-<cloud>/<service>*.go` files
registered on the shared mux. EC2 speaks the EC2 Query protocol, so its
operations register on the Query router by action name. The regional change:
- added `simulator-aws/ec2_nat_regional.go`, which holds regional create, the
  per-zone address operations, and the function that keeps a gateway's zones
  in step with the VPC's subnets;
- extended the stored resource in `simulator-aws/ec2.go`, with `Zones`,
  `AutoProvisionZones` and `AutoScalingIps` on `EC2NatGateway`, and the zone
  on each address;
- removed the refusal in `simulator-aws/ec2_gateways_addresses.go`, and gave
  Create and Describe one renderer, so the two answers can't drift;
- branched `AssociateNatGatewayAddress` and `DisassociateNatGatewayAddress` in
  `simulator-aws/ec2_networking_misc.go` on the gateway's mode.

Two rules shape a handler:

- **Do the real work.** A regional gateway carries traffic. Each covered
  zone gets an egress sidecar on the VPC's engine network
  (`simulator-aws/ec2_nat_dataplane.go`), and each subnet routed to the
  gateway leaves through its own zone's sidecar. A gateway that only stored a
  record would count as a bug.
- **Use the cloud's own wire.** Responses use the model's member names and XML
  shape; errors use EC2's codes (`InvalidParameterCombination`,
  `MissingParameter`); states come from the model's enums.

`.claude/skills/sim-handler-checklist` is the checklist to run before writing a
new service.

## Step 4: Test through all three clients

Every operation the change touches is exercised by the AWS SDK for Go, the AWS
CLI and the Terraform provider, against the real simulator binary:

| Suite | File | Tests |
|---|---|---|
| SDK | `simulator-aws/sdk-tests/ec2_nat_regional_test.go` | `TestEC2_RegionalNatGatewayAutomaticZones`, `…ManualZones`, `…Refusals`, `…CarriesEachZonesConnections` |
| CLI | `simulator-aws/cli-tests/ec2_nat_regional_test.go` | `TestEC2CLI_RegionalNatGatewayAutomaticZones`, `…ManualZones` |
| Terraform | `simulator-aws/terraform-tests/nat-gateway-regional/` | `TestRegionalNatGatewayTerraform` |

The Terraform test is the same loop a user runs:

```go
env := tfsim.Start(t, ".")
env.Terraform(t, "init")
env.Terraform(t, "apply", "-auto-approve")
env.Terraform(t, "plan", "-detailed-exitcode") // the read-back matches: no changes
```

What a test may and may not do:
- **Coordinates only.** It configures the client exactly as against AWS, apart
  from the endpoint and the `test` credentials. No `if sim`, no simulator-only
  header.
- **Wait on events.** It waits on the SDK's waiters or the cloud's status
  fields, never on a sleep.
- **Its own address range.** A test that places a workload on a network takes
  a range no other test in its suite uses: the data-plane test took
  `10.244.0.0/16` and the Terraform test `10.87.0.0/16`.
  `go run scripts/check-test-network-ranges.go` checks it.
- **One CI shard.** A new Terraform package joins a `tf (aws …)` shard in
  `.github/workflows/ci.yml`; this one went into the network-interface shard.
  `scripts/check-tf-shard-coverage.sh` checks that every package runs in
  exactly one. Route tables steer instances only on the microVM backend
  (BUG-3755), so the data-plane SDK test runs in the `ec2-microvm` shard and
  the container-backed `compute` shard skips it.

Run them:

```sh
make simulator-aws/sdk-test SDK_TEST_ARGS='-run RegionalNatGateway'
make simulator-aws/cli-test CLI_TEST_ARGS='-run RegionalNatGateway'
make simulator-aws/terraform-test TERRAFORM_TEST_PACKAGES=./nat-gateway-regional
make simulator-aws/unit-test   # the specification gates
```

## Step 5: Regenerate the records

```sh
bash scripts/seed-surface-tables.sh
git diff --stat specs/SIM_SURFACE_TABLES/
```

The surface tables (`specs/SIM_SURFACE_TABLES/aws-ec2.md`) record each
operation's handler and tests. A pre-commit hook fails when they're stale.

## Step 6: Update the continuity files

In the same commit, in the past tense, describing the end state:
- `BUGS.md`: delete the row the change closes, and add a row for every gap it
  leaves (BUG-3754 for the unsourced details, BUG-3755 for container-backed
  instances ignoring route tables).
- `WHAT_WE_DID.md`: the decision, if it sets a rule others will follow.
- `STATUS.md`, `PLAN.md` and `DO_NEXT.md`, where the change moves them.

## Step 7: Commit

```sh
git add -A
git commit -m "feat(aws): serve Amazon EC2 regional NAT gateways"
```

The commit message is a Conventional Commit, because release-please builds the
changelog from it. The pre-commit hooks run:
- formatting, lint and the testing contract;
- the coverage records and the CI-shard checks;
- the code-shape scans.

A failing hook is fixed at its cause, never skipped.

## Step 8: Push and drive the pull request to green

```sh
git fetch origin main && git rebase origin/main
git push -u origin <branch>
```

All work goes on the one open pull request. CI runs about a hundred jobs, the
three client suites sharded per cloud among them. A red job is fixed in the
same pull request, including one the change didn't cause. The owner merges,
and release-please carries the `feat:` into the next release.
