# Sim surface — aws-s3control_mrap

Surface registered in `simulator-aws/s3control_mrap.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /v20180820/async-requests/mrap/create` | ✓ `simulator-aws/s3control_mrap.go:73::handleS3CreateMultiRegionAccessPoint` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `POST /v20180820/async-requests/mrap/delete` | ✓ `simulator-aws/s3control_mrap.go:74::handleS3DeleteMultiRegionAccessPoint` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `POST /v20180820/async-requests/mrap/put-policy` | ✓ `simulator-aws/s3control_mrap.go:75::handleS3PutMultiRegionAccessPointPolicy` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/async-requests/mrap/{token...}` | ✓ `simulator-aws/s3control_mrap.go:76::handleS3DescribeMultiRegionAccessPointOperation` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/mrap/instances` | ✓ `simulator-aws/s3control_mrap.go:78::handleS3ListMultiRegionAccessPoints` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/mrap/instances/{name}` | ✓ `simulator-aws/s3control_mrap.go:79::handleS3GetMultiRegionAccessPoint` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/mrap/instances/{name}/policy` | ✓ `simulator-aws/s3control_mrap.go:80::handleS3GetMultiRegionAccessPointPolicy` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/mrap/instances/{name}/policystatus` | ✓ `simulator-aws/s3control_mrap.go:81::handleS3GetMultiRegionAccessPointPolicyStatus` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/mrap/instances/{name}/routes` | ✓ `simulator-aws/s3control_mrap.go:82::handleS3GetMultiRegionAccessPointRoutes` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `PATCH /v20180820/mrap/instances/{name}/routes` | ✓ `simulator-aws/s3control_mrap.go:83::handleS3SubmitMultiRegionAccessPointRoutes` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
