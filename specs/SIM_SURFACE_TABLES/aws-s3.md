# Sim surface — aws-s3

Surface registered in `simulator-aws/s3.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

The extractor reads the route out of a single string literal, so a registration that composes its path from a variable (`"GET "+prefix+"/…"`) produces no row here. Absence from this table is therefore not evidence that an op is unserved — check the source before concluding a gap. The status marker comes from `scripts/classify-sim-handlers.go`, which reads what the handler behind each route actually does.

## Status legend

- ✓ — implemented: the handler reads or writes simulator state, so the operation remembers what it did. It does not follow that the answer is built from what it read: a handler that looks its parent up and then answers a fixed body reaches state and is marked ✓
- ○ — answers without reaching state. Correct for a published catalog or a computed echo, and the shape a stub has too — read the handler before trusting it
- ? — the handler is not declared in this package, so the generator cannot say
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — NotImplemented on the wire (a declared gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `GET /{$}` | ✓ `simulator-aws/s3.go:209::nil` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /{bucket}` | ✓ `simulator-aws/s3.go:210::s3BucketResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /{bucket}` | ✓ `simulator-aws/s3.go:211::s3BucketResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /{bucket}` | ✓ `simulator-aws/s3.go:212::s3BucketResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /{bucket}/{key...}` | ✓ `simulator-aws/s3.go:213::s3ObjectResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /{bucket}/{key...}` | ✓ `simulator-aws/s3.go:214::s3ObjectResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /{bucket}/{key...}` | ✓ `simulator-aws/s3.go:215::s3ObjectResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /{bucket}/{key...}` | ✓ `simulator-aws/s3.go:220::s3ObjectResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /{bucket}` | ✓ `simulator-aws/s3.go:221::s3BucketResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->

## The three unserved operations are scoped out, not outstanding

Amazon S3 serves every operation in the vendored Smithy model except three, and
those three are a **decision**, not a backlog. Do not open them as a coverage
gap; the reasoning, in full, lives next to the ratchet list in
`simulator-aws/service_conformance_test.go` (`s3ConformanceMissing`).

| Operation | Endpoint family it actually belongs to | Why a handler alone would be a fake |
|---|---|---|
| `WriteGetObjectResponse` | `{RequestRoute}.s3-object-lambda.<region>` (per request) | Only an S3 Object Lambda *function* calls it. Without Object Lambda access points routing `GetObject` through a Lambda, the operation has no caller. |
| `CreateSession` | `s3express-control.<region>` / zonal bucket endpoints | Mints session credentials that later requests authenticate with. With no directory-bucket type and no session auth to verify them against, the credentials would be checked by nothing. |
| `ListDirectoryBuckets` | `s3express-control.<region>` | Lists a bucket type the simulator does not model. |

None of the three is addressed to the regional `s3.<region>.amazonaws.com`
surface this simulator hosts, so implementing one means hosting another
endpoint family and building the feature behind it — S3 Express One Zone as its
own bucket type, or S3 Object Lambda access points. No backend, agent, runner,
console, or test path in this repository uses either feature.

**Revisit when a real consumer appears** — a backend storing workload state in a
directory bucket, or a runner reading through an Object Lambda access point —
and then implement the feature, not a bare handler on an endpoint no real
client would reach.

<!-- HAND-WRITTEN END -->
