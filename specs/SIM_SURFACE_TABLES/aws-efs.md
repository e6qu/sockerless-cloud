# Sim surface — aws-efs

Surface registered in `simulator-aws/efs.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

The extractor reads the route out of a single string literal, so a registration that composes its path from a variable (`"GET "+prefix+"/…"`) produces no row here. Absence from this table is therefore not evidence that an op is unserved — check the source before concluding a gap. The status marker comes from `scripts/classify-sim-handlers.go`, which reads what the handler behind each route actually does.

## Status legend

- ✓ — implemented: the handler reads or writes simulator state, so the operation remembers what it did
- ○ — answers without reaching state. Correct for a published catalog or a computed echo, and the shape a stub has too — read the handler before trusting it
- ? — the handler is not declared in this package, so the generator cannot say
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — NotImplemented on the wire (a declared gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /2015-02-01/file-systems` | ✓ `simulator-aws/efs.go:238::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-02-01/file-systems` | ✓ `simulator-aws/efs.go:239::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2015-02-01/file-systems/{id}` | ✓ `simulator-aws/efs.go:240::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2015-02-01/file-systems/{id}/protection` | ✓ `simulator-aws/efs.go:241::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2015-02-01/file-systems/{id}` | ✓ `simulator-aws/efs.go:242::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2015-02-01/file-systems/{id}/lifecycle-configuration` | ✓ `simulator-aws/efs.go:243::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-02-01/file-systems/{id}/lifecycle-configuration` | ✓ `simulator-aws/efs.go:244::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-02-01/mount-targets` | ✓ `simulator-aws/efs.go:246::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-02-01/mount-targets` | ✓ `simulator-aws/efs.go:247::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-02-01/mount-targets/{id}/security-groups` | ✓ `simulator-aws/efs.go:248::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2015-02-01/mount-targets/{id}/security-groups` | ✓ `simulator-aws/efs.go:249::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2015-02-01/mount-targets/{id}` | ✓ `simulator-aws/efs.go:250::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-02-01/access-points` | ✓ `simulator-aws/efs.go:252::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-02-01/access-points` | ✓ `simulator-aws/efs.go:253::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2015-02-01/access-points/{id}` | ✓ `simulator-aws/efs.go:254::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2015-02-01/file-systems/{id}/policy` | ✓ `simulator-aws/efs.go:257::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-02-01/file-systems/{id}/policy` | ✓ `simulator-aws/efs.go:258::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2015-02-01/file-systems/{id}/policy` | ✓ `simulator-aws/efs.go:259::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2015-02-01/file-systems/{id}/backup-policy` | ✓ `simulator-aws/efs.go:262::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-02-01/file-systems/{id}/backup-policy` | ✓ `simulator-aws/efs.go:263::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-02-01/file-systems/replication-configurations` | ✓ `simulator-aws/efs.go:267::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-02-01/file-systems/{id}/replication-configuration` | ✓ `simulator-aws/efs.go:268::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2015-02-01/file-systems/{id}/replication-configuration` | ✓ `simulator-aws/efs.go:269::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /2015-02-01/account-preferences` | ✓ `simulator-aws/efs.go:272::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-02-01/account-preferences` | ✓ `simulator-aws/efs.go:273::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-02-01/resource-tags/{id}` | ✓ `simulator-aws/efs.go:277::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-02-01/resource-tags/{id}` | ✓ `simulator-aws/efs.go:278::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /2015-02-01/resource-tags/{id}` | ✓ `simulator-aws/efs.go:279::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-02-01/create-tags/{id}` | ✓ `simulator-aws/efs.go:282::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-02-01/tags/{id}` | ✓ `simulator-aws/efs.go:283::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /2015-02-01/tags/{id}/` | ✓ `simulator-aws/efs.go:284::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /2015-02-01/delete-tags/{id}` | ✓ `simulator-aws/efs.go:285::cloudTrailRecordedREST` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
