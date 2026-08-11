---
name: sim-emitted-url-roundtrip
description: Verify that every URL a sim handler emits in a response body (advertised endpoints, presigned URLs, LRO operation links, callback URLs, selfLink fields) actually resolves to a non-404 handler when followed. Catches the BUG-1103 shape — the Azure sim advertised `https://{account}.blob.localhost:4568/` in its storage-account ARM response but had no Blob data-plane handler to service it. Use when adding or editing a sim handler that returns any URL field, and when reviewing PRs that touch sim response shapes.
---

# Sim emitted-URL round-trip invariant

When a simulator emits a URL in a response body — an advertised endpoint URL, a presigned URL, an LRO operation `selfLink`, a `callbackUrl`, a paginated `nextPageToken` URL — it's making a promise: "follow this URL and you'll get a sensible response." If the URL 404s, the sim is lying about its API surface and any SDK following the URL will fail in a way that's hard to map back to the missing handler.

BUG-1103 is the load-bearing case: `simulator-azure/files.go:192` returns `Blob: fmt.Sprintf("%s://%s.blob.%s%s/", scheme, name, hostname, portSuffix)` on every storage-account `PUT`, but no handler matches the `{account}.blob.<host>` subdomain. The SDK happily takes the URL from the storage-account response and dispatches its first blob request to a non-existent endpoint.

This skill exists so every URL the sim hands out is verified to round-trip.

## When this skill applies

- Adding a new sim handler that emits any URL field in its response.
- Editing an existing handler's response shape in a way that changes a URL field.
- Reviewing a PR that touches a `<sim>/<file>.go` response builder.
- Auditing an existing sim service for fidelity gaps.

Skip for: handlers that emit URLs pointing at external systems by design (e.g., webhook delivery URLs the operator configured; OAuth redirect URIs supplied by the client).

## The rule

Every URL emitted by a sim handler must be one of:

1. **Routable by the sim** — there's an `srv.HandleFunc("METHOD <path>", ...)` registration that matches when the URL is followed, and the handler returns a sensible (non-404, non-default-fallthrough) response.
2. **Documented as external** — the URL points at a system outside the sim's control (webhook target, OAuth redirect), and the response shape's field documentation says so.

A URL that's neither is a wire-protocol bug.

## How to apply

### Step 1 — find URL emissions in the handler

Grep for response-shape fields that carry URLs. AWS / GCP / Azure all have predictable field names:

```bash
# Look for URL-bearing struct field tags in the sim under review
rg -n '`json:"[^"]*(?:[uU]ri|[uU]rl|[lL]ink|[eE]ndpoint|[hH]ref)' simulator-<cloud>/

# Look for inline URL composition that lands in a response
rg -n 'fmt\.Sprintf\(.*://' simulator-<cloud>/<file>.go
```

For each hit, locate the response builder that emits it.

### Step 2 — confirm a handler matches the URL

For every emitted URL, mentally (or actually) issue a follow-up request:

- **Same-host URLs** (`http://localhost:<port>/...`, `http://{name}.<service>.localhost:<port>/...`) — grep for a `HandleFunc` whose method+path matches. Subdomain-routed URLs need both subdomain dispatch (look for a `Host:` header check in `main.go` or a host-stripping helper) **and** a path handler.
- **Cloud-public URLs** (`https://*.amazonaws.com`, `https://*.googleapis.com`, `https://*.azure.com`) emitted by the sim — these are usually wrong; the sim should not be handing out real-cloud URLs in test mode.
- **LRO operation URLs** (`/v1/projects/.../operations/<id>`, `/v1.0/operations/<id>`) — check the matching `Operations.Get` handler returns a `done: true` terminal state, not a perpetual pending.
- **Presigned URLs** (`...?X-Amz-Signature=...`, `...?X-Goog-Signature=...`) — confirm the sim accepts the signature (or has a documented "accept any signature" stance for test mode).

### Step 3 — write a follow-up test

For every URL field the handler emits, add a test that follows the URL and asserts a non-404 response with a sensible body shape:

```go
// Example: GCS object selfLink round-trip
func TestGCSObject_SelfLinkRoundTrips(t *testing.T) {
    // ... upload an object via the sim ...
    selfLink := obj.SelfLink

    resp, err := http.Get(selfLink)
    if err != nil {
        t.Fatalf("emitted selfLink does not resolve: %v", err)
    }
    if resp.StatusCode == http.StatusNotFound {
        t.Fatalf("sim emitted selfLink %q that 404s — fix the sim's routing", selfLink)
    }
    // Optionally also assert the response body's `kind` / `name` matches.
}
```

For LRO links specifically, the round-trip test should poll the operation and assert `done: true` within a bounded time (typically 1-2 polls because sim LROs are synthesized synchronously).

### Step 4 — document handlers that emit external URLs

If a URL field intentionally points outside the sim (webhook delivery, OAuth redirect, GitHub Apps redirect_uri), add a comment on the response struct field that says so:

```go
type AppHookEvent struct {
    // DeliveryURL points at the operator-configured webhook target.
    // The sim does not service this URL; deliveries are fire-and-
    // forget HTTP POSTs to whatever the operator supplied.
    DeliveryURL string `json:"delivery_url"`
}
```

## Verification commands

Quick scan for URL-bearing fields without round-trip coverage:

```bash
# Find every URL-bearing field tag in the AWS sim
rg -n '`json:"[^"]*(?:[uU]ri|[uU]rl|[lL]ink|[eE]ndpoint|[hH]ref)' simulator-aws/

# Find every fmt.Sprintf composing a URL inside a sim
rg -n 'fmt\.Sprintf\([^)]*://' simulator-

# Cross-reference: every URL-emitting struct, then grep its field name
# in the sim's tests directory. Fields with zero hits in tests are
# the prime suspects for emitted-URL-without-round-trip-coverage.
```

## Known prior occurrences (catchable by this skill)

- **BUG-1103** (open in current branch) — Azure storage account ARM response advertises `blob` / `queue` / `table` endpoint URLs; only `file` is serviced. Catchable: follow any of the four URLs after `PUT Microsoft.Storage/storageAccounts/{name}` and observe the 404.
- **BUG-1044** (fixed) — GCS object `selfLink` / `mediaLink` interpolated the object name without `url.PathEscape`; object names containing `/` or space produced malformed or wrong-resource URLs. Catchable: round-trip an object whose name contains `/`.
- **BUG-1038 sub-fix** (fixed) — GCS object `selfLink` field was omitted from the upload + read responses. Catchable: assert response body contains the expected URL fields.

## Related skills

- `sim-handler-checklist` — broader pre-write checklist; this skill is one of its sub-checks.
- `sim-canonical-config-test` — sister skill for test-side fidelity (this skill is sim-side fidelity).
- `adaptor-fidelity-check` — the canonical "do real adaptors actually work" rule.
