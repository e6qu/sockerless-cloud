---
name: cross-resource-stack-test
description: Codify the production-shape end-to-end test pattern from Phase 159's TestStackProductionShape. Use when adding cross-resource sim features (e.g., new resource A references resource B's ARN/domain/ID) or when authoring a new sim's apply_test.go from scratch. Asserts what references RESOLVE TO, not just that apply doesn't crash.
---

# Cross-resource stack test

`terraform apply -auto-approve && terraform destroy -auto-approve` proves the happy path doesn't crash. That bar is too low for sim correctness: it leaves the question "did resource A's reference to resource B's `arn` actually resolve to B's real ARN?" unanswered. A sim that returns `arn:aws:cloudfront::000000000000:distribution/X` from CreateDistribution but echoes back `arn:aws:cloudfront::000000000000:distribution/Y` from GetWebACLForResource will pass `apply` + `destroy` cleanly — but production stacks will be broken.

The fix is to read `terraform output -json` after apply and assert specific cross-resource invariants in Go.

## When this skill applies

- Adding the first cross-resource Terraform test for a new sim service.
- Adding a new sim feature whose value is *only* observable through how another resource references it (WAF → CloudFront, Route 53 ALIAS → CloudFront, IAM SLR → service-principal mapping, Cloud Map service → DNS record).
- Extending `simulator-<cloud>/terraform-tests/apply_test.go` with new assertions after adding resources to `main.tf`.

## The pattern

The canonical implementation lives at `simulator-aws/terraform-tests/apply_test.go::TestStackProductionShape`. Structure:

### 1. Provision the full production-shape stack in one apply

`main.tf` declares every resource that a real-world production stack of this kind would combine. For an AWS CDN stack that's CloudFront + ACM + WAFv2 + Route 53 ALIAS + Amplify + IAM SLR/OIDC + (existing) ECS + ECR + Cloud Map — ~30 resources in one apply.

Do NOT split into per-resource `apply_test.go` files. The point is the cross-resource graph; isolated tests miss the graph.

### 2. Declare `output` blocks for every value another resource references

```hcl
output "cloudfront_arn"           { value = aws_cloudfront_distribution.tf_dist.arn }
output "cloudfront_domain_name"   { value = aws_cloudfront_distribution.tf_dist.domain_name }
output "wafv2_assoc_resource_arn" { value = aws_wafv2_web_acl_association.tf_assoc.resource_arn }
output "route53_alias_target_name"{ value = aws_route53_record.tf_alias.alias[0].name }
output "acm_certificate_arn"      { value = aws_acm_certificate.tf_cert.arn }
```

Every output you write becomes an assertion target. Be specific — don't output an entire object, output the exact attribute another resource uses.

### 3. Read `terraform output -json` from Go and assert invariants

```go
func TestStackProductionShape(t *testing.T) {
    require.NoError(t, terraform("init"))
    require.NoError(t, terraform("apply", "-auto-approve"))

    out := readOutputs(t)
    cfARN     := out.must(t, "cloudfront_arn")
    cfDomain  := out.must(t, "cloudfront_domain_name")
    wafTarget := out.must(t, "wafv2_assoc_resource_arn")
    alias     := out.must(t, "route53_alias_target_name")
    acmARN    := out.must(t, "acm_certificate_arn")

    // The three load-bearing invariants of a CloudFront-fronted prod stack:
    require.Equal(t, cfARN, wafTarget,
        "WAFv2 association resource_arn must equal CloudFront ARN")
    require.Equal(t, cfDomain, strings.TrimSuffix(alias, "."),
        "Route 53 ALIAS target must equal CloudFront domain_name")
    require.True(t, strings.HasPrefix(acmARN, "arn:aws:acm:us-east-1:"),
        "ACM cert must be in us-east-1 for CloudFront use; got %s", acmARN)

    require.NoError(t, terraform("destroy", "-auto-approve"))
}
```

The `readOutputs` helper + `out.must(t, "...")` pattern is in the same file; reuse it.

### 4. Pick the right invariants

For each cross-resource edge in the stack, ask "what would silently break in production if this reference was wrong?" Then assert *that*. Examples that came out of Phase 159 design:

| Invariant | What it would catch if violated |
|---|---|
| `WAF.resource_arn == CloudFront.arn` | Sim returns a random ARN from `CreateDistribution` but a fixed ARN from `GetWebACLForResource` — the association points at nothing. |
| `Route 53 ALIAS target.name == CloudFront.domain_name` | Sim's CF distribution domain doesn't actually match what GetDistribution echoes back. DNS would resolve to garbage. |
| `ACM ARN region == "us-east-1"` | Sim accepts ACM cert in any region; CloudFront would reject the cert with `InvalidViewerCertificate` against real AWS but the sim let it through silently. |
| `SLR ARN contains service-principal path segment` | Sim's principal-name mapping table missed an entry; SLR ARN looks valid but the path is wrong. |

Each invariant has the form **"computed value from one resource matches a referenced value in another"** — and ideally also **"with a format/region/prefix constraint that the cloud enforces."** Both shapes catch real bugs.

### 5. Keep apply + destroy in the same test, not separate

Splitting into `TestApply` and `TestDestroy` introduces flakiness (one passes, one fails, state is dirty). Single test, sequenced: `init → apply → readOutputs → assert → destroy`. The Phase 159 implementation runs in 86s end-to-end on a developer machine.

## What this skill is NOT

- It's not a replacement for per-verb SDK + CLI tests. Those still ship per sub-task (the `sim-handler-checklist` skill covers it).
- It's not a real-cloud test. It runs against the sim. The point is to assert correctness of the sim's cross-resource bookkeeping, not the cloud's actual production wiring.
- It's not a load test. One apply, one destroy, focused assertions.

## Failure modes this skill catches

- "Apply passes, so the cross-resource graph works" — only proves graphs *compile*, not that they *resolve* correctly.
- "I'll write a separate test per resource" — misses the graph entirely.
- "I'll assert that the output is non-empty" — catches nothing; sim could return `"arn:aws:placeholder:::"` and pass.
- "I'll add a fixture and diff against it" — fixtures rot; assert invariants instead.
- "I'll skip this; the SDK test already verifies the response" — SDK tests verify single-op correctness. Cross-resource bugs are *between* ops.

## Quick references

- `simulator-aws/terraform-tests/apply_test.go::TestStackProductionShape` — the canonical implementation.
- `simulator-aws/terraform-tests/main.tf` — the production-shape stack + output blocks.
- `.claude/skills/sim-handler-checklist/SKILL.md` — the pre-write checklist for the handlers this test exercises.
- `.claude/skills/adaptor-fidelity-check/SKILL.md` — broader wire-fidelity skill.

## Output

When this skill fires, list the cross-resource edges in the stack you're testing (e.g., "WAF → CloudFront", "Route 53 ALIAS → CloudFront", "ACM ← CloudFront region pin"), pick the invariant for each, and write the assertion. Then run the test locally before pushing — the pattern targets ~90s end-to-end so dev-loop feedback is fast.
