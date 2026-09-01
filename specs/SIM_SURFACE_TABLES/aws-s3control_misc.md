# Sim surface — aws-s3control_misc

Surface registered in `simulator-aws/s3control_misc.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `PUT /v20180820/accesspoint/{name}/scope` | ✓ `simulator-aws/s3control_misc.go:24::handleS3PutAccessPointScope` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accesspoint/{name}/scope` | ✓ `simulator-aws/s3control_misc.go:25::handleS3GetAccessPointScope` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `DELETE /v20180820/accesspoint/{name}/scope` | ✓ `simulator-aws/s3control_misc.go:26::handleS3DeleteAccessPointScope` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/bucket` | ✓ `simulator-aws/s3control_misc.go:28::handleS3ListRegionalBuckets` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `DELETE /v20180820/bucket/{bucket}/lifecycleconfiguration` | ✓ `simulator-aws/s3control_misc.go:29::handleS3DeleteBucketLifecycleConfiguration` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accesspointfordirectory` | ✓ `simulator-aws/s3control_misc.go:30::handleS3ListAccessPointsForDirectoryBuckets` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
