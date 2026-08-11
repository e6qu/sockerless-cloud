---
name: mux-overlap-scan
description: Enumerate every `mux.HandleFunc(...)` + `srv.HandleFunc(...)` route registered across `simulator-<cloud>/*.go` and flag patterns where one wildcard registration shadows another service's literal path. Distilled from BUG-1158 — the collapsed-port sim model means every service's routes share a single `http.ServeMux`, and the sim doesn't have DNS-level service isolation real clouds have. When a wildcard pattern like `POST /{bucket}/{key...}` claims a path another service's handler should own, the wrong handler responds with a plausible-looking error and SDK-level wire-shape probes don't catch it. Use whenever adding a new sim handler that registers a route, and run periodically via `scripts/scan-mux-overlap.sh`.
---

# Mux-overlap invariant

## The class of bug

Real cloud has DNS-level service isolation — `s3.<region>.amazonaws.com`, `apigateway.<region>.amazonaws.com`, `lambda.<region>.amazonaws.com` are separate origins. The sim collapses every service onto one port and dispatches by path. When a new service's request path matches an older service's wildcard registration, Go's ServeMux picks the older registration (because the newer service has no handler at all, or because Go's specificity rules tie-break in favor of the older one).

The wrong handler responds with a plausible error envelope. The SDK retries thinking it's a transient cloud blip. Wire-shape tests don't catch it because the response *shape* (XML error envelope) is well-formed.

Concrete instances:

- **BUG-1150 / issue #204** — `POST /v2/apis/{id}/deployments` (API Gateway v2 CreateDeployment) routes to S3's `POST /{bucket}/{key...}` multipart-upload dispatcher. Sim returns S3-style `InvalidRequest "POST on an object requires ?uploads"`. The SDK gives up.
- **BUG-1154 / issue #208** — `ListTagsForResource` is a canonical awsQuery `Action` across RDS, SNS, ElastiCache, CloudWatchLogs. The awsQuery router dispatches by `Action` alone (ignoring `Version`), so first-registered wins and the wrong service handles every cross-service tag call.

## When this skill applies

- Adding a new `mux.HandleFunc(...)` or `srv.HandleFunc(...)` registration in `simulator-<cloud>/*.go`.
- Modifying an existing pattern (especially adding wildcards or making one more general).
- Auditing a PR that touches any sim service file.
- Periodically — `scripts/scan-mux-overlap.sh` runs in pre-commit (warn mode initially; gating once the baseline overlap count reaches 0).

## The rule

**No pattern may shadow another registered pattern unless the shadowing is documented + intentional.** Two patterns shadow each other when they're for the same HTTP method AND one would match every request the other would match.

Wildcard patterns (`{x}`, `{x...}`) carry the highest shadow risk. The seeder script flags every wildcard registration; the scanner pairs them up with literal patterns to surface conflicts.

## How to apply

### When adding a new route

1. Pick the most specific pattern that captures the surface. Prefer `POST /v1/{service}/{resource}/...` over `POST /v1/{rest...}`.
2. Run `bash scripts/scan-mux-overlap.sh` before pushing. Any overlap involving your new pattern is a finding.
3. If your route conflicts with an existing wildcard from another service: either tighten the existing wildcard (path-prefix guard like Phase 176's GCS `{bucket}` known-bucket check; Phase 177's S3 `bucketSubresourceHandlers` query-key gate) or convert the wildcard into multiple literal patterns.

### Scanner output format

```
simulator-aws/s3.go:121     POST /{bucket}/{key...}      handleS3PostObjectDispatch
simulator-aws/apigatewayv2.go:42  POST /v2/apis/{id}/deployments    handleAPIGwV2CreateDeployment
SHADOW: aws/s3.go's `POST /{bucket}/{key...}` would match `POST /v2/apis/abc/deployments`
   → unless apigatewayv2.go's pattern is more specific (Go 1.22 spec)
   → or s3.go gates by first-segment-must-be-known-bucket
```

The scanner is intentionally noisy — better to surface false positives than miss real shadows. Each finding gets one of three resolutions: (a) tighten the wildcard, (b) document the intentional precedence inline, (c) confirm specificity rules disambiguate correctly.

### Gating mode

Initially (Phase 178 Stage A) the scanner runs as a pre-commit hook in **warn mode** — overlaps print as warnings; commit proceeds. After Phase 178 Stage B fixes BUG-1150 + BUG-1154, the scanner graduates to **gating** — any overlap not in the explicit allowlist (`scripts/mux-overlap-allowlist.txt`) blocks the commit.

The allowlist records intentional precedence pairs. Each entry pairs two patterns + a one-line justification:

```
simulator-aws/s3.go::POST /{bucket}/{key...}  simulator-aws/apigatewayv2.go::POST /v2/apis/{id}/deployments  S3 gates by known-bucket; apigatewayv2 wins for /v2/* paths
```

## Companion skills

- `surface-table-completeness` — every row in a surface table should have a unique route; mux-overlap-scan catches when two rows accidentally point at overlapping patterns.
- `sim-canonical-config-test` — SDK-driven tests will catch the user-visible symptom (wrong response shape); this skill catches the cause at registration time.
- `sim-handler-checklist` — the pre-write checklist for new handlers; this skill is the runtime-of-registration check.

## Worked example

For PR #202 + the in-flight Phase 178 work:

- S3's `POST /{bucket}/{key...}` shadows ANY `POST /<2-or-more-segments>` from any other service that doesn't register its own pattern at that path. Mitigation: Phase 178 Stage B commit 7 adds a `s3Buckets_.Has(firstSegment)` gate to `handleS3PostObjectDispatch` so paths whose first segment isn't a registered bucket fall through to the next mux pattern.
- AWS's `r.Register("ListTagsForResource", ...)` shadows every other service's `ListTagsForResource` action. Mitigation: Phase 178 Stage B commit 6 changes the registration model to `r.Register(version, action, handler)` so dispatch is `Version → Action → handler`.

After both fixes land, the baseline allowlist is empty and gating turns on.
