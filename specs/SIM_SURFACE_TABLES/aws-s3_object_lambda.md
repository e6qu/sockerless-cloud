# Sim surface — aws-s3_object_lambda

Surface registered in `simulator-aws/s3_object_lambda.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `PUT /v20180820/accesspoint/{name}` | ✓ `simulator-aws/s3_object_lambda.go:138::handleS3CreateAccessPoint` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accesspoint/{name}` | ✓ `simulator-aws/s3_object_lambda.go:139::handleS3GetAccessPoint` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `DELETE /v20180820/accesspoint/{name}` | ✓ `simulator-aws/s3_object_lambda.go:140::handleS3DeleteAccessPoint` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accesspoint` | ✓ `simulator-aws/s3_object_lambda.go:141::handleS3ListAccessPoints` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `PUT /v20180820/accesspoint/{name}/policy` | ✓ `simulator-aws/s3_object_lambda.go:142::handleS3PutAccessPointPolicy` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accesspoint/{name}/policy` | ✓ `simulator-aws/s3_object_lambda.go:143::handleS3GetAccessPointPolicy` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `DELETE /v20180820/accesspoint/{name}/policy` | ✓ `simulator-aws/s3_object_lambda.go:144::handleS3DeleteAccessPointPolicy` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accesspoint/{name}/policyStatus` | ✓ `simulator-aws/s3_object_lambda.go:145::handleS3GetAccessPointPolicyStatus` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `PUT /v20180820/accesspointforobjectlambda/{name}` | ✓ `simulator-aws/s3_object_lambda.go:148::handleS3CreateAccessPointForObjectLambda` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accesspointforobjectlambda/{name}` | ✓ `simulator-aws/s3_object_lambda.go:149::handleS3GetAccessPointForObjectLambda` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `DELETE /v20180820/accesspointforobjectlambda/{name}` | ✓ `simulator-aws/s3_object_lambda.go:150::handleS3DeleteAccessPointForObjectLambda` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accesspointforobjectlambda` | ✓ `simulator-aws/s3_object_lambda.go:151::handleS3ListAccessPointsForObjectLambda` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accesspointforobjectlambda/{name}/configuration` | ✓ `simulator-aws/s3_object_lambda.go:152::handleS3GetAccessPointConfigurationForObjectLambda` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `PUT /v20180820/accesspointforobjectlambda/{name}/configuration` | ✓ `simulator-aws/s3_object_lambda.go:153::handleS3PutAccessPointConfigurationForObjectLambda` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `PUT /v20180820/accesspointforobjectlambda/{name}/policy` | ✓ `simulator-aws/s3_object_lambda.go:154::handleS3PutAccessPointPolicyForObjectLambda` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accesspointforobjectlambda/{name}/policy` | ✓ `simulator-aws/s3_object_lambda.go:155::handleS3GetAccessPointPolicyForObjectLambda` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `DELETE /v20180820/accesspointforobjectlambda/{name}/policy` | ✓ `simulator-aws/s3_object_lambda.go:156::handleS3DeleteAccessPointPolicyForObjectLambda` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `GET /v20180820/accesspointforobjectlambda/{name}/policyStatus` | ✓ `simulator-aws/s3_object_lambda.go:157::handleS3GetAccessPointPolicyStatusForObjectLambda` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |
| `POST /WriteGetObjectResponse` | ✓ `simulator-aws/s3_object_lambda.go:160::handleS3WriteGetObjectResponse` | ✗ (coverage matrix row missing) | ✗ (coverage matrix row missing) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
