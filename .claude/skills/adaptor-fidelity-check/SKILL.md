---
name: adaptor-fidelity-check
description: Verify a simulator change against its real reference adaptors (the cloud's Go SDK, its CLI — aws / gcloud / az — and its Terraform provider). Use whenever editing files under simulator-<cloud>/ or sim/, or anything that other tools speak to over the wire. Catches drift between what the simulator implements and what real adaptors send.
---

# Adaptor-fidelity check

Every simulator is paired with external **reference adaptors**: the cloud's
official Go SDK, its vendor CLI, and its Terraform provider. The adaptors are
the validation harness and the source of truth for what "correct" means. This
skill enforces that pairing.

## When this skill applies

| Component | Adaptors |
|---|---|
| `simulator-aws` | AWS SDK for Go v2 + `aws` CLI + Terraform `hashicorp/aws` |
| `simulator-gcp` | Google Cloud Go client libraries + `gcloud` (and `cbt`) + Terraform `hashicorp/google` |
| `simulator-azure` | Azure SDK for Go + `az` + Terraform `hashicorp/azurerm` and `hashicorp/azuread` |
| `sim` (the shared framework) | every adaptor of every simulator that reaches the changed code |
| the OCI registry data planes | the Docker CLI and engine (`docker push` / `docker pull`), Podman |

## The check

Before you commit any change to a wire-facing handler or response shape, answer all six:

### 1. Identify the request shape the real adaptor sends

Run the real adaptor against the real cloud with verbose logging:

```bash
aws --debug ec2 describe-instances 2>&1
gcloud --log-http run jobs list
az --debug containerapp env list
TF_LOG=DEBUG terraform plan -refresh=true
docker --debug push ...
```

Copy the exact path, method, headers and body the adaptor emits. **This is the
spec.** Not what the model thinks; what the adaptor actually does.

#### 1a. When `--debug` isn't enough, read the SDK serializer source

`--debug` shows wire bytes, but it cannot surface client-side encoding choices
for paths the request never reaches (because validation rejected it first). For
SDK-driven handlers the **serializer source code is the authoritative spec**:

```bash
# AWS Go SDK v2
find "$(go env GOMODCACHE)/github.com/aws/aws-sdk-go-v2" -name "serializers.go" \
  | xargs grep -l "<OpName>"

#   awsRestxml_serializeOp<OpName>     — REST + XML route + body
#   awsRestjson_serializeOp<OpName>    — REST + JSON
#   awsAwsjson11_serializeOp<OpName>   — AWS-JSON 1.1
#   awsAwsquery_serializeOp<OpName>    — AWS Query Protocol

# Google Cloud / Azure SDKs: the same pattern under their service-client directories.
```

Wire-shape facts only visible in the serializer, found this way:

- **ACM** encodes timestamps as Unix-epoch JSON numbers, not RFC 3339 strings.
- **CloudFront `CreateDistributionWithTags`** is a distinct serializer at the same path, dispatched by the `?WithTags` query.
- **WAFv2 ARNs for CLOUDFRONT scope** have region `us-east-1` (not `global`) with `global/` in the path.
- **Amplify `CreateDeployment`** is branch-level (`/apps/{appId}/branches/{name}/deployments`), not app-level.

If a handler change fails in ways `--debug` doesn't explain, the answer is in
`service/<svc>/serializers.go` or `deserializers.go`.

#### 1b. For Terraform-consumed handlers, also read `resource<X>Read` in the provider source

The failure mode: SDK test green, CLI test green, `terraform apply` panics or
reports "couldn't find resource". Causes:

- **Terraform's Read calls a different API than Create.** `aws_iam_service_linked_role`'s Read calls `GetRole`, not `GetServiceLinkedRole`; the simulator must serve both sides.
- **Terraform's Read dereferences deeply nested optional fields without nil checks.** A minimal response panics the provider. Fill the containers the service always returns before responding (see `cfNormalizeConfig` in `simulator-aws/cloudfront.go`).
- **Terraform's Read paginates with a cursor that must be honoured.** `aws_route53_record` uses `StartRecordName`/`StartRecordType`; without cursor filtering, seeded NS and SOA records come back first and Terraform reports "record not found".

Check what your resource's Read does in the provider version `terraform-tests` pins:

```bash
git -C <terraform-provider-aws checkout> show v<pinned>:internal/service/<svc>/<resource>.go \
  | grep -A 50 "func resource<X>Read"
# Note every conn.<Op>(...) call; each is a handler you need.
# Look for d.Set("foo", flatten(out.Config.A.B.C)) — every deep chain is a nil-deref risk.
```

### 2. Diff against the simulator handler

Compare field by field:

- Path template (case-sensitive, trailing slash included — Route 53's `/rrset/`).
- Query parameters (`softDeleted=true` from the Go client, `softDeleted=True` from gcloud).
- Body shape (a string `"false"` versus a boolean `false`).
- Response headers (`Content-Type: application/json; charset=utf-8` — the charset matters).
- Response shape (self links, optional fields, null versus missing).
- Status codes (400 versus 409 versus 412 for a conflict).

If any of these differs from the adaptor's emission, that's a real bug. File it
in `BUGS.md` or fix it in the same change.

### 3. Round-trip a test through the real adaptor

A test that doesn't drive the real adaptor doesn't count. Tests that count:

- `simulator-<cloud>/sdk-tests/` — the real SDK against the running simulator.
- `simulator-<cloud>/cli-tests/` — the real CLI through `runCLI`.
- `simulator-<cloud>/terraform-tests/` — the real Terraform provider.

Tests that don't count:

- Mocked tests where the adaptor never speaks to the binary.
- "Manual integration" tests that aren't in a Makefile target or CI.
- Tests against fixtures captured once and never re-validated.

### 4. Check the cross-cloud invariant

If you found a bug in one simulator's handler, check the same shape in the
other two simulators and in `sim/`. Fix all of them in the same commit.

### 5. Confirm the change preserves the contract

After your edit, re-run the real adaptor (step 1) against the modified
simulator. Does its behaviour match its behaviour against the real cloud? If
not, the change is wrong. Iterate.

### 6. Document the contract

Update the simulator's README where the change touches it:

- Reference adaptor and the version the tests pin.
- Validation: the test path.
- Sample command with **real captured output** (run it, paste it, don't guess).
- Out of scope: what the adaptor exercises that the simulator declines, and why.

## Failure modes this skill catches

- "I'll mock the cloud API to test this."
- "The AWS SDK probably sends `Action=DescribeTasks` as a query parameter" — verify, don't guess.
- "Looks like the test passes" — against a fixture captured months ago, not a live adaptor.
- "I only changed the response shape for one service" — but the handler is shared across services or clouds.
- "I'll add a `null` check in the parser" — when the real adaptor never sends null in that field.
- **"The SDK and CLI tests pass, so the wire shape is right"** — but the CLI uses a trailing slash the SDK doesn't. Register both forms; run both tests.
- **"Apply works, so the cross-resource references work"** — they only *compile*. Use the `cross-resource-stack-test` skill to assert what references resolve to.
- **"Terraform apply passed once, ship it"** — but the next plan shows drift because the Read response omitted optional fields. Use 1b.

## Compile-time guardrails

When the contract fits in the type system, prefer that to a manual review:

- **Interface satisfaction proofs.** `var _ Interface = (*Impl)(nil)` at package scope turns a dropped method into a build error.
- **Typed identifiers.** When you introduce a new identifier kind (ARN, task ID, function name, hostname), define a named type rather than using `string`, so passing one where another belongs fails to compile.
- **Types that forbid the wrong read.** `sim.PrefixStore` has no `List` or `Filter`, so a whole-store read of object data does not compile.

## Quick references

- `specs/SIM_SURFACE_TABLES/` — per-surface operation tables.
- `specs/SIM_TEST_COVERAGE_MATRIX.md` — which client exercises each surface.
- `specs/cloud-api/` — the vendored specifications the simulators are validated against.
- Each simulator's README — adaptors, validation, wiring and samples.

## Output

When this skill fires, name the reference adaptors in one sentence ("AWS SDK /
aws CLI / Terraform aws"), the validation entry point (test path), and what
you're about to verify. Then verify. Don't write code until step 1 (the real
adaptor's request shape) is captured.
