---
name: sim-canonical-config-test
description: Verify a simulator test (sdk-tests/, cli-tests/, terraform-tests/) uses the same SDK/CLI/provider config a real-cloud consumer would. Refuses sim-quirk options like `BaseEndpoint = baseURL + "/<service>"` or `UsePathStyle = true` that mask wire-protocol bugs by reconfiguring the client around them. Distilled from BUG-1098 / 1099 / 1104 — the meta blind-spot where the project's own SDK tests were rewritten to make the sim look correct. Use before writing or editing any file under `simulator-<cloud>/{sdk,cli,terraform}-tests/`.
---

# Sim canonical-config invariant for sim tests

The meta-bug behind GitHub issues #173 and #174 (BUG-1098, BUG-1099) was not that S3 routes were under `/s3/` — it was that the project's *own* `simulator-aws/sdk-tests/s3_test.go` line 16 said:

```go
o.BaseEndpoint = aws.String(baseURL + "/s3")
o.UsePathStyle = true
```

so SDK tests passed against a sim that no stock client could reach. Once that single line shipped, every subsequent layer of test infrastructure (test-pyramid expansions, vibe-slop sweeps, codex reviews, adaptor-fidelity sweeps) verified the sim against itself rather than against AWS. The bug had nowhere to surface.

This skill exists so the same pattern can't repeat for any new sim test.

## When this skill applies

- Writing a new test under `simulator-<cloud>/sdk-tests/`, `simulator-<cloud>/cli-tests/`, or `simulator-<cloud>/terraform-tests/`.
- Editing the SDK / CLI / Terraform-provider client setup in an existing test.
- Reviewing a PR that touches any of those test directories.

Skip for: test data fixtures (input bodies, expected response bodies), helpers that don't construct an SDK client, harness-only files (test-main lifecycle, port allocator).

## The rule

A simulator-test client config must be **the same config a real-cloud user would write**, with only these allowed diffs:

1. **Endpoint URL** points at the sim instead of the cloud's public endpoint.
2. **TLS verify-skip** when the sim uses a self-signed cert (must carry a comment naming the sim's self-signed cert as the reason; never `InsecureSkipVerify` for any other reason).
3. **Static credentials** in place of cloud-resolved credentials (sims don't verify bearer tokens; document the placeholder values).

That's it. Everything else (signers, retry policy, HTTP transport, base-endpoint suffixes, path-style addressing, etc.) must match a stock consumer's config.

## The "use the SDK, not raw HTTP" rule

Every test under `simulator-<cloud>/sdk-tests/` MUST construct the cloud provider's official SDK client for the service under test. Raw `net/http` is a finding by default — it bypasses the SDK's challenge-then-retry handshake, request signing, response parser, retry policy, and pagination, all of which are part of the contract real consumers depend on. The Phase 176 KV tests that pre-set `Authorization: Bearer fake-token` and posted raw HTTP requests passed against PR #200 but skipped the SDK's `parseTenant` parser entirely — issue #193 reopened the moment a real consumer ran `azsecrets.NewClient` against the merged build.

Permitted raw-HTTP scenarios (narrow, must be commented):

- **The SDK has no method for this surface** (e.g., IMDS `/metadata/instance`, MSI token endpoint — these aren't surfaced by `azidentity` or other public SDKs; a raw HTTP test is the canonical client).
- **The test exercises a 404 / NotImplemented gap intentionally**, where the SDK would refuse to construct a request for an op the sim doesn't support yet.
- **The test is a wire-shape probe** that verifies headers / status codes the SDK abstracts away (e.g., asserting the exact `WWW-Authenticate:` header value bytes). Pair the wire-shape test with an SDK-driven round-trip test that exercises the same surface end-to-end.

If none of those apply, refactor the test to use the SDK. The refactor lands in the same PR as any fix that touches the surface — *partial* SDK coverage is the bug shape this section refuses.

The companion `surface-table-completeness` skill's `sdk-test` column shows whether each row has SDK-driven coverage; rows without an SDK test even though one is possible are a finding for this skill.

### Permitted SDK-config diffs beyond endpoint URL + TLS-skip + static creds

Real-cloud SDK clients sometimes refuse to talk to non-canonical endpoints unless small per-client knobs are flipped. These are allowed *only when documented* (a comment naming the sim-listener constraint):

- `InsecureAllowCredentialWithHTTP: true` — sim listens on plain HTTP; real cloud is HTTPS.
- `DisableChallengeResourceVerification: true` (Azure KV SDKs) — sim's `<vault>.vault.localhost` host doesn't suffix-match the challenge's `resource="https://vault.azure.net"`.
- Custom `Transport` that rewrites HTTPS → HTTP at request time — when the SDK's BearerTokenPolicy refuses non-HTTPS regardless of `InsecureAllowCredentialWithHTTP` (Azure KV).
- DNS-rewriting transport — sim listens on loopback; real-cloud DNS names don't resolve on the test runner. Keep the Host header, dial the loopback address.

Each diff carries a one-line comment naming the constraint and pointing at the canonical-cloud equivalent. Without the comment, the diff is indistinguishable from a sim-quirk masking a real wire bug.

## Terraform-provider tests

The rule extends to `simulator-<cloud>/terraform-tests/` with the same shape:

- Every `provider "<cloud>" {}` block in `main.tf` must use the canonical configuration documented by the upstream `terraform-provider-<cloud>` — endpoint overrides (`endpoints { s3 = "<sim>" }`, etc.) are the only allowed diff.
- `skip_credentials_validation = true` / `skip_metadata_api_check = true` are allowed *with a comment* naming the sim-listener as the reason; never as the path of least resistance.
- Terraform-provider resources must match real-cloud resource names — `aws_s3_bucket_versioning`, not a sim-specific variant.

The `tf-test` column in `specs/SIM_SURFACE_TABLES/<surface>.md` tracks coverage; rows with a real terraform-provider resource but no tf-test entry are findings.

## Paged-iterator rule (List* ops)

Every `List*` operation in a surface table must have an sdk-test that exercises the SDK's **paged iterator**, not just `.Value[0]`-style auto-unwrap.

The class of bug this catches: when the sim returns a single-record envelope (`{value, id, attributes}`) from an endpoint that should return a paged list (`{value: [{id, attributes}, ...], nextLink}`), an SDK call like `client.ListSecretVersions(ctx, name)` auto-unwraps the wrong shape and `out.Value[0]` returns the single record as if it were the first page's only entry. Test passes. Real consumers that drive `client.NewListSecretVersionsPager(...).NextPage(ctx)` hit a deserialise error or get zero items per page.

This is BUG-1149 (issue #203) in canonical form.

**The rule**:

- Every row in a surface table with verb `GET` and path matching `*/list` / `*?list-type=*` / `/{collection}` (no specific resource ID) has the `paged-shape verified` column.
- The cell is ✓ only when a test drives the SDK's canonical pagination idiom (`Pager.NextPage`, `Paginator.HasMorePages`, `iterator.Next`) and asserts the response can be unmarshaled into the paged response type — not just that `.Value[0]` is non-nil.
- ✗ rows in this column are paired with a deferral pointer to BUG-1159 (paged-iterator sweep).

**Canonical patterns per cloud**:

| Cloud | Paged iterator API |
|---|---|
| AWS (`aws-sdk-go-v2`) | `client.NewListXxxPaginator(input).HasMorePages()` + `paginator.NextPage(ctx)` |
| Azure (`azure-sdk-for-go`) | `client.NewListXxxPager(...).NextPage(ctx)` |
| GCP (`google.golang.org/api`) | `client.List(...).Pages(ctx, func(resp *T) error { ... })` |

A test that calls `client.ListXxx(ctx, ...)` directly and reads `.Value[0]` does NOT satisfy this rule — it's the exact shape that masks the bug.

## Refused patterns

The following options are **anti-patterns** in `simulator-<cloud>/{sdk,cli,terraform}-tests/`. If you find them, the sim has a wire-protocol bug — fix the sim, not the test.

### AWS SDK (Go v2)

| Pattern | Why it's wrong | Real fix |
|---|---|---|
| `o.BaseEndpoint = aws.String(baseURL + "/<service>")` | The sim is mounting routes under a service-name prefix; real AWS doesn't. | Re-mount routes at the canonical path; drop the suffix. |
| `o.UsePathStyle = true` for non-S3 services | Path-style addressing is an S3-specific opt-in; meaningless elsewhere. | Drop the option. |
| `o.UsePathStyle = true` for S3 **without** a comment explaining a real consumer would set it | The test is locking in a particular addressing style; real users default to virtual-hosted-style. | Either remove the option or test both addressing styles. |
| Custom `o.HTTPSignerV4` | Replaces the canonical signer with a sim-friendly one. | Sim must accept the canonical SigV4 signer. |
| Custom retry policy that disables retries | Hides flakiness in the sim. | Fix the sim's flakiness; keep stock retries. |

### AWS CLI

| Pattern | Why it's wrong |
|---|---|
| `--endpoint-url=$BASE_URL/<service>` | Same as the SDK `BaseEndpoint` workaround. |
| `--no-sign-request` | Real consumers sign. The sim must accept signed requests. |
| `--no-verify-ssl` without a comment | Only acceptable when the sim's listener uses a self-signed cert; comment the reason. |

### gcloud / Google SDK

| Pattern | Why it's wrong |
|---|---|
| `option.WithEndpoint(endpoint + "/<service>")` | Service-name suffix on the base endpoint. |
| `CLOUDSDK_API_ENDPOINT_OVERRIDES_<SERVICE>` pointing at a path-prefixed URL | gcloud accepts it, but real users don't override these. |
| `option.WithoutAuthentication()` paired with a sim that *does* verify tokens | Either the sim should not verify tokens or the test should authenticate. |

### Azure SDK / azurerm / az CLI

| Pattern | Why it's wrong |
|---|---|
| Custom `cloud.Configuration` that bypasses the sim's `/metadata/endpoints` | The sim already serves a real-cloud-shape metadata endpoint per BUG-1040. Use it. |
| `--query` to extract a specific field that's only present in the sim | Test should assert real-cloud response shape; if it isn't present, the sim is missing a field. |

## How to apply

When writing a new test:

1. **Start from the cloud-provider's quick-start documentation.** Open the SDK / CLI / TF-provider README; copy the canonical "first 5 lines" client setup.
2. **Substitute only the allowed diffs** (endpoint URL, TLS-skip with a comment, static creds).
3. **Run the test.** If it fails because the sim returns 404 / 405 / wrong shape, that's the bug. File it; fix the sim.
4. **Resist the urge to "fix" the test.** Adding `BaseEndpoint = baseURL + "/<service>"` or `UsePathStyle = true` is the bug shape this skill exists to prevent.

When editing an existing test:

1. **Grep the file for the refused patterns above.** If any are present, file a BUG documenting the wire-protocol mismatch.
2. **Do not delete the refused option in the same commit as a new assertion.** Land the sim fix first; the test option falls out naturally.

## Verification command

Quick scan for the most common refused patterns:

```bash
# AWS SDK v2 path-prefix workaround
rg -n 'BaseEndpoint\s*=\s*aws\.String\([^)]*\+\s*"/' simulator-

# gcloud path-prefix override
rg -n 'WithEndpoint\([^)]*\+\s*"/' simulator-

# AWS CLI path-prefixed endpoint
rg -n '--endpoint-url[= ]\$[A-Z_]+/[a-z]' simulator-

# Unconditional InsecureSkipVerify without a sim-cert comment in the same line
rg -B1 -A1 'InsecureSkipVerify\s*:\s*true' simulator- \
  | rg -v 'self-signed|sim cert|simulator cert'
```

Each hit is a candidate for the refused-pattern review above.

## Related skills

- `adaptor-fidelity-check` — the broader "sim ↔ real adaptor" parity rule this skill drills into for one specific dimension (client config).
- `sim-handler-checklist` — the pre-write checklist for new sim handlers; this skill is the pre-write checklist for new sim *tests*.
- `sim-emitted-url-roundtrip` — sister skill for the "sim emits URLs it can't service" blind-spot.
- `backpedal-pattern-audit` — meta-skill that surfaces patterns like this from BUGS.md.
