# Sim surface — gcp-gcs_object_acls

Surface registered in `simulator-gcp/gcs_object_acls.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `GET /storage/v1/b/{bucket}/o/{object}/acl` | ✓ `simulator-gcp/gcs_object_acls.go:168::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/o/{object}/acl/{entity}` | ✓ `simulator-gcp/gcs_object_acls.go:186::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/o/{object}/acl` | ✓ `simulator-gcp/gcs_object_acls.go:202::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}/o/{object}/acl/{entity}` | ✓ `simulator-gcp/gcs_object_acls.go:245::update` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/o/{object}/acl/{entity}` | ✓ `simulator-gcp/gcs_object_acls.go:246::update` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/o/{object}/acl/{entity}` | ✓ `simulator-gcp/gcs_object_acls.go:248::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
