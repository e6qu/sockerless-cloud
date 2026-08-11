---
name: sim-handler-checklist
description: Pre-write checklist for adding a new service handler under simulator-{aws,gcp,azure}/. Distilled from Phase 159 (CloudFront / ACM / Route 53 / WAFv2 / Amplify / IAM SLR/OIDC) — every load-bearing fix came from one of these four checks, and skipping any of them meant a CI-red round-trip. Use before writing the first line of a new service.go file.
---

# Sim-handler checklist

Phase 159 added six AWS service families to `simulator-aws/`. Every CI-red iteration in that phase mapped to skipping one of four pre-write checks. This skill makes those checks explicit so the next sim service ships without the same round-trips.

## When this skill applies

- Adding a new `simulator-<cloud>/<service>.go` file.
- Adding a new verb (e.g., a previously-unimplemented op) to an existing service handler.
- Investigating a Terraform `apply` failure against the sim that says `panic`, "couldn't find resource", or "Provider produced inconsistent result after apply".

Skip for: bug-fixes that don't change the wire shape, doc-only changes, internal refactors that don't touch the handler surface.

## The four checks

### 1. Read the SDK serializer source — don't guess wire shape

`--debug` traces show *some* of the wire shape but not all of it. The SDK enforces client-side encoding choices (timestamp format, request body wrapper, query-param variants) that never appear on the wire if the request never goes out. The serializer source is authoritative.

```bash
# For AWS Go SDK v2
find ~/go/pkg/mod/github.com/aws/aws-sdk-go-v2 -name "serializers.go" \
  | xargs grep -l "<OpName>"

# Inspect: aws-sdk-go-v2/service/<svc>/serializers.go
#   awsRestxml_serializeOp<OpName>      — REST + XML route + body
#   awsRestjson_serializeOp<OpName>     — REST + JSON
#   awsAwsjson11_serializeOp<OpName>    — AWS-JSON 1.1
#   awsAwsquery_serializeOp<OpName>     — AWS Query Protocol

# Inspect: aws-sdk-go-v2/service/<svc>/types/types.go for response shapes.
# Inspect: aws-sdk-go-v2/service/<svc>/deserializers.go for response parsing
#   (especially error-envelope shape — JSON vs XML, field names).
```

Real Phase 159 bugs this would have caught up front:

- **ACM** encodes timestamps as Unix-epoch JSON numbers, not RFC3339. The SDK's deserializer threw "expected TStamp to be a JSON Number, got string instead". Visible in `aws-sdk-go-v2/service/acm/deserializers.go`.
- **CloudFront** dispatches `CreateDistributionWithTags` on `?WithTags` query at the same path as `CreateDistribution`. Visible in `cloudfront/serializers.go` as a separate `awsRestxml_serializeOpCreateDistributionWithTags` function.
- **WAFv2 ARN** has region literally `us-east-1` (not `global`) with `global/` in the path for CLOUDFRONT scope. Visible in the SDK's input/output struct comments.
- **Amplify `CreateDeployment`** routes to `/apps/{appId}/branches/{branchName}/deployments` (branch-level, NOT app-level). Visible at the top of `amplify/serializers.go`.

When in doubt, the serializer source is the spec. The model's prior on "the obvious URL shape" is wrong about 30% of the time.

### 2. Read the Terraform provider's `resourceXxxRead` for nil-deref patterns

Terraform's `aws` / `google` / `azurerm` providers dereference deeply-nested response fields without nil-checks during their Read function. When the sim returns a minimal response (only the required fields), the provider crashes with `panic: invalid memory address`. The fix is **always** to fill empty containers on the response — never to wait for the crash and patch one field at a time.

```bash
# Find the provider's Read function for the resource you're adding.
# (provider is hashicorp/terraform-provider-aws or -google or -azurerm)
git -C ~/code/terraform-provider-aws show v6.32.1:internal/service/<svc>/<resource>.go \
  | grep -A 50 "resource<X>Read"

# Look for chains: d.Set("foo", flatten(out.Config.Foo.Bar.Baz))
# Any chain three deep is a likely nil-deref target.
```

The pattern in `simulator-aws/cloudfront.go` (`cfNormalizeConfig`) shows the response-side fix: when the input has nil for an optional nested struct, replace it with the empty-but-present form (`&CFAliases{Quantity: 0}`, `&CFCacheBehaviors{Quantity: 0}`, etc.) before the response goes out.

If you find yourself thinking "the field is optional, I'll leave it nil" — the TF provider doesn't agree. Fill the container.

### 3. Verify which API the Terraform Read calls

Terraform Read functions sometimes call a different API than the Create. Examples observed in Phase 159:

- **`aws_iam_service_linked_role.Read` calls `GetRole`**, not `GetServiceLinkedRole`. (There is no `GetServiceLinkedRole` API.) Fix: `CreateServiceLinkedRole` writes a shadow `IAMRole` to the `iamRoles` store as well, so `GetRole` finds it.
- **`aws_amplify_app.Read` calls `GetApp`** but its Update path goes through a separate `UpdateApp` with different field semantics — verify both.
- **`aws_route53_record.Read` paginates via `ListResourceRecordSets`** with `StartRecordName`/`StartRecordType` cursor — your `ListXxx` handler must honour the cursor or TF reports "record not found" when seed records (NS/SOA) come back first.

The mechanical check:

```bash
# Read the provider's Read function for your resource.
grep -A 5 "func resource<X>Read" \
  ~/code/terraform-provider-aws/internal/service/<svc>/<resource>.go
# Note every "conn.<Op>(...)" call. Each one is an API your sim must implement.
```

If Read calls `GetRole` but you only implemented `GetServiceLinkedRole`, TF apply will fail with "couldn't find resource" — *after* a successful Create. The sim looks OK from the SDK side and the CLI side; only TF surfaces the gap.

### 4. Register both `/path` and `/path/` for paths the CLI hits with a trailing slash

Go 1.22's `http.ServeMux` treats `POST /rrset` and `POST /rrset/` as distinct patterns. The `aws` CLI uses the trailing-slash form for some endpoints (notably Route 53 `ChangeResourceRecordSets`). The SDK does not. Net effect: SDK tests pass, CLI tests 404.

When registering REST routes, scan the SDK's serializer source for `request.Method = http.MethodPost; request.URL.Path = "..."` lines — the trailing-slash detail is in there. When in doubt:

```go
mux.HandleFunc("POST /api/v1/rrset",  handleX)
mux.HandleFunc("POST /api/v1/rrset/", handleX)
```

…and run the CLI test as well as the SDK test. The pre-commit hook "Simulator testing contract" forces sdk + cli + terraform tests per change for exactly this reason.

## What "done" looks like for a new sim handler

Before committing:

- [ ] Handler file `simulator-<cloud>/<service>.go` with wire shape verified against SDK serializer source.
- [ ] SDK test `simulator-<cloud>/sdk-tests/<service>_test.go` covering each verb's happy path + the error envelope for at least one failure mode.
- [ ] CLI test `simulator-<cloud>/cli-tests/<service>_test.go` covering the same verbs via the real `aws` / `gcloud` / `az` binary.
- [ ] Terraform test entry in `simulator-<cloud>/terraform-tests/main.tf` for each TF resource your new handler unlocks, plus assertions in `apply_test.go` (see the `cross-resource-stack-test` skill).
- [ ] `simulator-<cloud>/API_SPEC.md` updated with the new verbs in the per-service section + REST path appendix.
- [ ] Real-AWS error codes returned on missing resources, never synthesised 200 (per `avoid-vibe-slop` and the BUG-991 / BUG-992 lineage).

## Failure modes this skill catches

- "I'll write the handler from the SDK input/output type names, those describe the shape" — wrong on `?WithTags` dispatch, wrong on epoch timestamps, wrong on WAFv2 ARN region.
- "The TF Read should just work if the SDK Create works" — wrong on `GetRole` for SLRs, wrong on cursor-paginated lists, wrong when the response omits optional fields.
- "The CLI uses the SDK so anything that works in the SDK works in the CLI" — wrong on trailing-slash routes.
- "I'll fix the TF crashes one at a time as they happen" — wrong; that's a 5-iteration loop instead of one `cfNormalizeConfig`-style pass.

## Quick references

- Phase 159 commit history on `phase-159-aws-sim-cloudfront-amplify` — every sub-task is a worked example.
- `simulator-aws/cloudfront.go` — `cfNormalizeConfig` is the canonical fill-empty-containers pattern.
- `simulator-aws/iam_slr_oidc.go` — the canonical shadow-write pattern for asymmetric Create/Read APIs.
- `simulator-aws/route53.go` — the canonical "both `/rrset` and `/rrset/`" registration.
- `simulator-aws/API_SPEC.md` §8–13 — wire shapes documented for the six P159 services as a template for new ones.
- `.claude/skills/adaptor-fidelity-check/SKILL.md` — the broader wire-fidelity discipline this skill specialises.

## Output

When this skill fires, name the service you're about to handle (`cloudfront`, `route53`, etc.), the cloud (`aws`, `gcp`, `azure`), the wire protocol you've identified by reading the SDK serializer (AWS-JSON 1.1 / REST+XML / REST+JSON / AWS Query / gRPC-Web / etc.), and the TF resources that will consume your handler. Then proceed verb by verb; don't batch.
