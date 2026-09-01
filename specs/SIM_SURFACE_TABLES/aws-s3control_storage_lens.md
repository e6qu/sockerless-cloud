# Sim surface — aws-s3control_storage_lens

Surface registered in `simulator-aws/s3control_storage_lens.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `PUT /v20180820/storagelens/{configId}` | ✓ `simulator-aws/s3control_storage_lens.go:54::handleS3PutStorageLensConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v20180820/storagelens/{configId}` | ✓ `simulator-aws/s3control_storage_lens.go:55::handleS3GetStorageLensConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v20180820/storagelens/{configId}` | ✓ `simulator-aws/s3control_storage_lens.go:56::handleS3DeleteStorageLensConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v20180820/storagelens` | ✓ `simulator-aws/s3control_storage_lens.go:57::handleS3ListStorageLensConfigurations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v20180820/storagelens/{configId}/tagging` | ✓ `simulator-aws/s3control_storage_lens.go:58::handleS3PutStorageLensConfigurationTagging` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v20180820/storagelens/{configId}/tagging` | ✓ `simulator-aws/s3control_storage_lens.go:59::handleS3GetStorageLensConfigurationTagging` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v20180820/storagelens/{configId}/tagging` | ✓ `simulator-aws/s3control_storage_lens.go:60::handleS3DeleteStorageLensConfigurationTagging` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v20180820/storagelensgroup` | ✓ `simulator-aws/s3control_storage_lens.go:62::handleS3CreateStorageLensGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v20180820/storagelensgroup/{name}` | ✓ `simulator-aws/s3control_storage_lens.go:63::handleS3GetStorageLensGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v20180820/storagelensgroup/{name}` | ✓ `simulator-aws/s3control_storage_lens.go:64::handleS3UpdateStorageLensGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v20180820/storagelensgroup/{name}` | ✓ `simulator-aws/s3control_storage_lens.go:65::handleS3DeleteStorageLensGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v20180820/storagelensgroup` | ✓ `simulator-aws/s3control_storage_lens.go:66::handleS3ListStorageLensGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
