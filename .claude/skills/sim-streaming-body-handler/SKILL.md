---
name: sim-streaming-body-handler
description: Verify that a sim handler reading a request body inspects and handles streaming-envelope sentinel headers (`Content-Encoding: aws-chunked`, `x-amz-content-sha256: STREAMING-*`, `x-amz-decoded-content-length`, Azure SSE-C, GCS SSE-C, multipart boundaries). Catches the BUG-1099 shape — `handleS3PutObject` did `io.ReadAll(r.Body)` straight to the object store, storing the chunk-encoding envelope verbatim. Use when writing or editing any sim handler that reads a request body.
---

# Sim streaming-body sentinel-header invariant

The aws-chunked / streaming-signed family is the most surprising shape AWS SDKs put on the wire. When the SDK uses a non-seekable body (`io.Pipe`, `http.Request.Body`, an `io.LimitReader`, a streaming compressor), it switches to `Transfer-Encoding: chunked` over HTTP and wraps the payload in AWS's own chunked-encoding scheme on top: each chunk gets a size header, a `\r\n`, the payload, another `\r\n`, and a final `0\r\n` followed by trailer headers.

Real S3 unwraps this server-side. The sim before BUG-1099 didn't — `io.ReadAll(r.Body)` stored the chunk-encoded bytes verbatim. The bug is structurally undetectable when:

1. The sim listens on plain HTTP — the SDK refuses streaming-signed payloads over HTTP.
2. The test uses a seekable body (`bytes.NewReader`) — the SDK opts out of streaming.
3. The handler doesn't log `Content-Encoding` or `x-amz-content-sha256` — there's no operator-visible footprint.

This skill is the pre-write check that any new body-reading sim handler considers streaming envelopes.

## When this skill applies

- Adding or editing a sim handler that calls `io.ReadAll(r.Body)`, `io.Copy(w, r.Body)`, or `json.NewDecoder(r.Body).Decode(...)`.
- Adding a sim service that handles uploads / streaming inserts: S3, Glacier, Kinesis, Azure Blob, Azure Files (data plane), GCS, BigQuery streaming inserts, Pub/Sub publish.
- Auditing an existing handler that ingests a body and the sim feels lossy ("uploaded X, got back Y with prefix bytes").

Skip for: handlers that only read query parameters or fixed-shape JSON request bodies that the SDK serializes with `Content-Length` set (control-plane CRUD typically falls here).

## The sentinel headers to inspect

### AWS

| Header | Meaning | Action |
|---|---|---|
| `Content-Encoding: aws-chunked` | Body is AWS chunk-encoded. | Decode before reading payload. |
| `x-amz-content-sha256: STREAMING-AWS4-HMAC-SHA256-PAYLOAD` | Signed streaming (no trailer). | Decode chunks; verify per-chunk signature if strict. |
| `x-amz-content-sha256: STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER` | Signed streaming with trailing checksum header. | Decode chunks; parse trailer block. |
| `x-amz-content-sha256: STREAMING-UNSIGNED-PAYLOAD-TRAILER` | Unsigned streaming with trailer. | Decode chunks; parse trailer block. |
| `x-amz-decoded-content-length: <int>` | Decoded payload size. | Use as size sentinel; if present, body is chunked. |
| `Content-MD5: <base64>` | Integrity check for the *decoded* body. | Verify after decode. |
| `x-amz-server-side-encryption-customer-*` | SSE-C: client-provided encryption key. | Either honour or refuse explicitly. |

### Azure

| Header | Meaning | Action |
|---|---|---|
| `Transfer-Encoding: chunked` | HTTP/1.1 chunked transfer. | Stdlib handles; no special decode. |
| `x-ms-blob-type` | `BlockBlob` / `AppendBlob` / `PageBlob` | Branch on type — append + page have different semantics. |
| `x-ms-encryption-key` / `x-ms-encryption-key-sha256` | SSE-C with client-provided key. | Honour or refuse explicitly. |
| `x-ms-blob-condition-*` | Conditional write (etag match, leased blob). | Honour or refuse explicitly. |
| `Content-MD5` | Integrity check. | Verify after read. |

### GCS

| Header | Meaning | Action |
|---|---|---|
| `Transfer-Encoding: chunked` | HTTP chunked. | Stdlib handles. |
| `Content-Type: multipart/related; boundary=...` | Multipart upload combining metadata + content. | Parse multipart; metadata is the first part, content the second. |
| `X-Upload-Content-Length` | Resumable upload's eventual size. | Use to pre-size the destination. |
| `Content-Range: bytes <start>-<end>/<total>` | Resumable upload chunk. | Honour range; accumulate until total reached. |
| `x-goog-encryption-key` / `x-goog-encryption-key-sha256` | SSE-C. | Honour or refuse explicitly. |

## How to apply

### Step 1 — inspect the SDK's serializer

Before the sim handler reads `r.Body`, identify which streaming modes the SDK can put on the wire. The serializer source is authoritative:

```bash
# AWS Go SDK v2 — find streaming-mode branches
rg -n 'STREAMING-|aws-chunked' ~/go/pkg/mod/github.com/aws/aws-sdk-go-v2/

# Azure Go SDK — find chunked / SSE-C branches
rg -n 'aws-chunked|x-ms-encryption' ~/go/pkg/mod/github.com/!azure/

# GCS Go client — find multipart/resumable branches
rg -n 'multipart/related|X-Upload-Content' ~/go/pkg/mod/cloud.google.com/go/storage/
```

### Step 2 — branch on the sentinel before reading

The pattern that catches BUG-1099-shape bugs:

```go
func handleUpload(w http.ResponseWriter, r *http.Request) {
    body := r.Body
    defer body.Close()

    // Sentinel: aws-chunked envelope?
    if r.Header.Get("Content-Encoding") == "aws-chunked" ||
        strings.HasPrefix(r.Header.Get("x-amz-content-sha256"), "STREAMING-") {
        body = sim.NewAWSChunkedReader(body)
    }

    // Sentinel: GCS multipart upload?
    if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "multipart/related") {
        body = sim.NewGCSMultipartReader(body, ct)
    }

    // Now safe to read the payload.
    payload, err := io.ReadAll(body)
    if err != nil { /* ... */ }
    // ...
}
```

The decoder lives in the shared `sim` package so every service that ingests bodies uses the same implementation.

### Step 3 — log the sentinel when present

So future operators can grep:

```go
if ce := r.Header.Get("Content-Encoding"); ce != "" {
    sim.Log(r).Debug().Str("content_encoding", ce).Msg("body uses content-encoding")
}
if sha := r.Header.Get("x-amz-content-sha256"); strings.HasPrefix(sha, "STREAMING-") {
    sim.Log(r).Debug().Str("streaming_variant", sha).Msg("aws streaming-signed payload")
}
```

Cheap, makes the bug category greppable, and lives at Debug level so production logs aren't noisy.

### Step 4 — verify with a non-seekable body

Per `sim-canonical-config-test`, write the test using the same SDK config a stock consumer would. Then make the body non-seekable so the SDK is forced into the streaming path:

```go
type nonSeekable struct{ r io.Reader }
func (n *nonSeekable) Read(p []byte) (int, error) { return n.r.Read(p) }

// Force streaming path:
_, err := s3Client.PutObject(ctx, &s3.PutObjectInput{
    Bucket: aws.String("b"),
    Key:    aws.String("k"),
    Body:   &nonSeekable{r: bytes.NewReader(payload)},
})
// Assert: GetObject returns the original payload, not the chunked envelope.
```

For AWS, this test must run against the sim with **TLS enabled** (set `SIM_TLS_CERT` / `SIM_TLS_KEY`); the SDK refuses streaming-signed payloads over HTTP. The test harness should generate a short-lived self-signed cert in `TestMain`.

## Verification commands

Find body-reading sim handlers that don't inspect any sentinel header:

```bash
# Handlers that ReadAll(r.Body) — check each for the branching pattern
rg -nB2 -A6 'io\.ReadAll\(r\.Body\)' simulator-

# Handlers that don't mention Content-Encoding or x-amz-content-sha256
# within 30 lines of the body read — suspicious
for f in $(rg -l 'io\.ReadAll\(r\.Body\)' simulator-); do
  if ! rg -q 'Content-Encoding|x-amz-content-sha256|x-amz-decoded-content-length|multipart/related' "$f"; then
    echo "$f"
  fi
done
```

### Positive-confirmation check

A helper added in a PR is not automatically called by other handlers in the same PR — the existence of `openStreamingBody` is not a sufficient signal that every upload handler uses it. Every upload-handler PR must include a positive enumeration of every site that should call the helper, plus a per-site verification that it actually does.

**Rule**: every handler in a file named `*_dataplane.go`, `*blob*.go`, `*storage*.go`, `*files*.go`, `*queue*.go`, `*bucket*.go`, or that handles a `PUT` / `POST` / `PATCH` with a binary body, MUST do one of:

1. Pass `r.Body` through `openStreamingBody(r)` (Azure / GCP) or `awsChunkedReader` / `isAWSChunkedRequest` (AWS S3), then read.
2. Document via a 1-line comment why the wrapped form is genuinely safe (e.g., "SDK serializes this control-plane CRUD with `Content-Length`; no streaming envelope possible").
3. Be on the documented "fixed-shape JSON request" allowlist (e.g., `Tables InsertEntity` with OData JSON, `Queue PutMessage` with XML — these have SDK-fixed serialization and very small bodies).

**Verification grep — to be run as part of every PR audit**:

```bash
# Every upload-shaped handler file
for f in $(rg -l 'PUT\s|POST\s|PATCH\s' --type go simulator- | rg '(dataplane|blob|storage|files|bucket|queue|registry)'); do
  # Find io.ReadAll/io.Copy without an openStreamingBody/awsChunkedReader pair within 5 lines
  awk '
    /io\.ReadAll\(r\.Body\)|io\.Copy\([^,]+,\s*r\.Body\)/ {
      # Check 5 lines back for openStreamingBody / awsChunkedReader
      ok=0
      for (i=NR-5; i<NR; i++) {
        if (lines[i] ~ /openStreamingBody|awsChunkedReader|isAWSChunkedRequest/) { ok=1; break }
      }
      if (!ok) print FILENAME ":" NR ": " $0
    }
    { lines[NR] = $0 }
  ' "$f"
done
```

A clean run produces zero output. Any output is a candidate for one of the three rules above.

## Known prior occurrences

- **BUG-1099** (fixed in 173.2) — `handleS3PutObject` stored aws-chunked envelope verbatim. Catchable by this skill plus a non-seekable-body, TLS-enabled test.
- **BUG-1110** (fixed in 174 round 2) — 9 upload-handler sites across GCS / Cloud Run invoke / AR blob × 2 / ACR blob × 4 / Azure Blob PutBlob bypassed streaming-envelope decoding. `openStreamingBody(r)` helper added per cloud; wired into all 9 sites.
- **(Phase 175 finding)** — 3 sites that bypassed the **helper introduced in the same PR**: Azure Files PutFile (`storage_dataplane.go:286`), Azure Files PutRange (`files.go:774`), AWS Lambda Invoke (`lambda.go:365`). This is the new meta-shape: a helper added in a PR is not automatically called by other handlers in the same PR — the positive-confirmation check above is the answer.

## Related skills

- `sim-handler-checklist` — broader sim-handler checklist; this is one of its sub-checks.
- `sim-canonical-config-test` — sister skill for test-side fidelity.
- `adaptor-fidelity-check` — the canonical end-to-end fidelity rule.
