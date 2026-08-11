---
name: adaptor-fidelity-check
description: Verify a sockerless component change against its real reference adaptor (docker CLI / aws CLI / gcloud / az / Terraform / gh CLI / Docker SDK). Use whenever editing files under backends/, simulator-, or anything that other tools speak to over the wire. Catches drift between what we implement and what real adaptors send.
---

# Adaptor-fidelity check

Every sockerless component is paired with an external **reference adaptor** (per `docs/RUNNERS.md` and the per-backend READMEs). The adaptor is the validation harness, the user-facing utility, and the source of truth for what "correct" means. This skill enforces that pairing.

## When this skill applies

| Component | Adaptor(s) |
|---|---|
| `backends/docker` | docker CLI, podman CLI, Docker Go SDK |
| `backends/ecs` | docker CLI/SDK + aws CLI/SDK + Terraform aws |
| `backends/lambda` | docker CLI/SDK + aws CLI/SDK + Terraform aws |
| `backends/cloudrun` | docker CLI/SDK + gcloud + GCP Go SDK + Terraform google |
| `backends/cloudrun-functions` | docker CLI/SDK + gcloud + Go SDK + Terraform google |
| `backends/aca` | docker CLI/SDK + az + Azure Go SDK + Terraform azurerm |
| `backends/azure-functions` | docker CLI/SDK + az + Azure Go SDK + Terraform azurerm |
| `simulator-aws` | aws CLI + AWS Go SDK + Terraform aws |
| `simulator-gcp` | gcloud + GCP Go SDK + Terraform google |
| `simulator-azure` | az + Azure Go SDK + Terraform azurerm |
| `bleephub` | gh CLI + smart-HTTP git + `actions/runner` |

## The check

Before you commit any change to a wire-facing handler or response shape, answer all six:

### 1. Identify the request shape the real adaptor sends

Run the real adaptor against a known-good upstream (real cloud, real `github.com`, real Docker daemon) with verbose logging:

```bash
docker --debug run ...
aws --debug ec2 describe-instances 2>&1
gcloud --log-http run jobs list
az --debug containerapp env list
gh api --include /repos/admin/demo
terraform plan -refresh=true  # then inspect .terraform/plugin*.log
```

Copy the exact path + method + headers + body the adaptor emits. **This is the spec.** Not what the model thinks; what the adaptor actually does.

#### 1a. When `--debug` isn't enough, read the SDK serializer source

`--debug` shows wire bytes, but it can't surface client-side encoding choices for paths that the request never reaches (because validation rejected it first). For SDK-driven handlers the **serializer source code is the authoritative spec**:

```bash
# AWS Go SDK v2
find ~/go/pkg/mod/github.com/aws/aws-sdk-go-v2 -name "serializers.go" \
  | xargs grep -l "<OpName>"

#   awsRestxml_serializeOp<OpName>     — REST + XML route + body
#   awsRestjson_serializeOp<OpName>    — REST + JSON
#   awsAwsjson11_serializeOp<OpName>   — AWS-JSON 1.1
#   awsAwsquery_serializeOp<OpName>    — AWS Query Protocol

# GCP / Azure SDKs: same pattern under their service-client directories.
```

Phase 159 caught four wire-shape facts only visible in the serializer:

- **ACM** encodes timestamps as Unix-epoch JSON numbers, not RFC3339 strings.
- **CloudFront `CreateDistributionWithTags`** is a distinct serializer at the same path, dispatched by `?WithTags` query.
- **WAFv2 ARNs for CLOUDFRONT scope** have region `us-east-1` (not `global`) with `global/` in the path.
- **Amplify `CreateDeployment`** is branch-level (`/apps/{appId}/branches/{name}/deployments`), not app-level.

If a sim-handler change is failing in ways `--debug` doesn't explain, the answer is in `service/<svc>/serializers.go` or `deserializers.go`.

#### 1b. For Terraform-consumed handlers, also read `resource<X>Read` in the provider source

Real failure mode: SDK test green, CLI test green, `terraform apply` panics or reports "couldn't find resource". Causes:

- **TF Read calls a different API than Create.** `aws_iam_service_linked_role.Read` calls `GetRole` (not `GetServiceLinkedRole`). Your sim must implement *both* sides. Fix pattern: shadow-write to the Read store on Create (see `simulator-aws/iam_slr_oidc.go`).
- **TF Read derefs deeply-nested optional fields without nil-checks.** Returning a minimal response panics the provider. Fix pattern: a `<svc>NormalizeConfig` pass that fills empty containers before responding (see `cfNormalizeConfig` in `simulator-aws/cloudfront.go`).
- **TF Read paginates with a cursor that must be honoured.** `aws_route53_record.Read` uses `StartRecordName`/`StartRecordType`; without cursor filtering, seeded records (NS/SOA) come back first and TF reports "record not found".

Check what your resource's Read actually does:

```bash
git -C ~/code/terraform-provider-aws show v6.32.1:internal/service/<svc>/<resource>.go \
  | grep -A 50 "func resource<X>Read"
# Note every conn.<Op>(...) call; each is a sim handler you need.
# Look for d.Set("foo", flatten(out.Config.A.B.C)) — every deep chain is a nil-deref risk.
```

### 2. Diff against the sockerless handler

Open the corresponding handler in the codebase. Compare field-by-field:
- Path template (`POST /v1.44/containers/{id}/wait` — case-sensitive).
- Query params (`condition=removed` vs `condition=next-exit`).
- Body shape (`{"private": "false"}` (string!) vs `{"private": false}`).
- Response headers (`Content-Type: application/json; charset=utf-8` — yes, charset matters).
- Response shape (HATEOAS URLs, optional fields, null vs missing).
- Status codes (400 vs 422 vs 409 for conflict).

If any of these differs from the adaptor's emission, that's a real bug. File it in BUGS.md before fixing.

### 3. Round-trip a test through the real adaptor

A test that doesn't drive the real adaptor doesn't count. Examples that DO count:

- [Bleephub](https://github.com/e6qu/bleephub)'s harness — real `gh` binary against Bleephub in Docker.
- `tests/` — real Docker Go SDK against running backend.
- `simulator-aws/sdk-tests/` — real AWS Go SDK against running simulator.
- `simulator-<cloud>/terraform-tests/` — real Terraform provider against running simulator.

Tests that DON'T count:

- Mocked-everything tests where the adaptor never speaks to the binary.
- "Manual integration" tests that aren't in a Makefile target or CI.
- Tests against fixtures captured once and never re-validated.

### 4. Check the cross-cloud invariant

If you found a bug in one backend's handler, check the same handler shape in:
- The other backends in the same cloud (Pattern B: shared in `*-common`).
- The other clouds.
- The simulator handler if applicable.

Repeat per `MEMORY.md` § cross-cloud-sweep. Fix all in the same commit.

### 5. Confirm the change preserves the contract

After your edit, re-run the real adaptor (step 1) against your modified sockerless. Does the adaptor's behaviour against sockerless match its behaviour against the real upstream?

If not, the change is wrong. Iterate.

### 6. Document the contract

Update the component's README (per Phase 157 doc shape):
- Reference adaptor + min version.
- Validation: test path + last-green date.
- Sample command + **real captured output** (run it, paste it, don't guess).
- Out-of-scope: what the adaptor exercises that you didn't implement.

## Failure modes this skill catches

- "I'll mock the cloud API to test this" — pattern 2.
- "The aws SDK probably sends `Action=DescribeTasks` as a query param" — pattern 6, verify don't guess.
- "Looks like the test passes" — but the test uses fixtures captured 8 months ago, not a live `gh` call (pattern 3).
- "I only changed the response shape for ECS; should be fine" — but `lambda` and `aca` share the same handler (pattern 12).
- "I'll add a `null` check in the parser" — when the real adaptor never sends null in that field (pattern 22).
- **"The SDK and CLI tests pass, so the wire shape is right"** — but the CLI uses trailing-slash on the route the SDK doesn't (Route 53 `/rrset/`). Register both forms; run both tests.
- **"Apply works, so the cross-resource refs work"** — they only *compile*. Use the `cross-resource-stack-test` skill to assert what references resolve to.
- **"TF apply passed once, ship it"** — but the next plan after restart shows drift because the Read response omitted optional fields. Use `1b` to read the provider's Read function and normalise the response shape.

## Compile-time guardrails for adaptor contracts

When the contract you're enforcing fits in the type system, prefer that to a manual review. Patterns that work in this repo (see `docs/GOLANG_STRONG_TYPING.md` for the full set):

- **Interface satisfaction proofs.** Every backend's `*Server` type declares `var _ api.Backend = (*Server)(nil)` at package scope. Adding a method to `api.Backend` then forces a build error in every backend until they all implement it. New cloud backends: copy this line. (Approach 8 in the typing doc.)
- **Typed IDs for cloud-state layers.** When you introduce a new identifier kind (ARN, task ID, function name, hostname), don't use `string`. Define `type ContainerARN string` (or similar) at the cloud-common layer. The compiler catches "passed the ECS task ARN to a Lambda function-name parameter" mismatches that the agent's "this string is opaque, who cares" instinct would miss. (Approach 1.)
- **Sealed sum types + `gochecksumtype`.** Used in `core.PodSpec`-style variants. When a new variant lands, every switch missing the case is a build error. Match cloud lifecycles, dispatch shapes, etc. (Approach 10.)
- **`forbidigo` against raw `any` outside `api/types_gen.go`.** Keep heterogeneous JSON quarantined to the generated Docker-API types; insist on typed shapes everywhere else. (Approach 12.)

For spec-driven domains (the OpenAPI Docker surface, the bleephub GitHub surface), code-generation from spec (`oapi-codegen`, `gqlgen`) is the long-game upgrade. Adoption decisions stay deferred per the typing doc's status banner.

## Quick references

- `docs/VIBE_CODING.md` — full anti-pattern catalogue.
- `docs/GOLANG_STRONG_TYPING.md` — type-strengthening approaches (research only; adoption deferred).
- `docs/RUNNERS.md` — runner ↔ sockerless wiring guide.
- `specs/BLEEPHUB_GITHUB_API_PARITY.md` — GitHub API contract for bleephub.
- `specs/CLOUD_RESOURCE_MAPPING.md` — Docker→cloud mapping per backend.
- Per-component README — adaptor + validation + wiring + sample.

## Output

When this skill fires, name the reference adaptor in one sentence ("docker SDK / aws CLI / gcloud / gh CLI"), the validation entry point (test path), and what you're about to verify. Then verify. Don't proceed to writing code until step 1 (real adaptor request shape) is captured.
