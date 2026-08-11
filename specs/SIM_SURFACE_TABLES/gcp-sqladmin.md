# Sim surface — gcp-sqladmin

Surface registered in `simulator-gcp/sqladmin.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /v1/projects/{project}/instances` | ✓ `simulator-gcp/sqladmin.go:111::handleSQLInsertInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `GET /v1/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/sqladmin.go:112::handleSQLGetInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `GET /v1/projects/{project}/instances` | ✓ `simulator-gcp/sqladmin.go:113::handleSQLListInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `PATCH /v1/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/sqladmin.go:114::handleSQLPatchInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `DELETE /v1/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/sqladmin.go:115::handleSQLDeleteInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `POST /v1/projects/{project}/instances/{instance}/databases` | ✓ `simulator-gcp/sqladmin.go:117::handleSQLInsertDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `GET /v1/projects/{project}/instances/{instance}/databases/{database}` | ✓ `simulator-gcp/sqladmin.go:118::handleSQLGetDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `GET /v1/projects/{project}/instances/{instance}/databases` | ✓ `simulator-gcp/sqladmin.go:119::handleSQLListDatabases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `DELETE /v1/projects/{project}/instances/{instance}/databases/{database}` | ✓ `simulator-gcp/sqladmin.go:120::handleSQLDeleteDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `POST /v1/projects/{project}/instances/{instance}/users` | ✓ `simulator-gcp/sqladmin.go:122::handleSQLInsertUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `GET /v1/projects/{project}/instances/{instance}/users/{name}` | ✓ `simulator-gcp/sqladmin.go:123::handleSQLGetUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `GET /v1/projects/{project}/instances/{instance}/users` | ✓ `simulator-gcp/sqladmin.go:124::handleSQLListUsers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `PUT /v1/projects/{project}/instances/{instance}/users` | ✓ `simulator-gcp/sqladmin.go:125::handleSQLUpdateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `DELETE /v1/projects/{project}/instances/{instance}/users` | ✓ `simulator-gcp/sqladmin.go:126::handleSQLDeleteUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `POST /v1/projects/{project}/instances/{instance}/backupRuns` | ✓ `simulator-gcp/sqladmin.go:128::handleSQLInsertBackupRun` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `GET /v1/projects/{project}/instances/{instance}/backupRuns` | ✓ `simulator-gcp/sqladmin.go:129::handleSQLListBackupRuns` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `GET /v1/projects/{project}/instances/{instance}/backupRuns/{id}` | ✓ `simulator-gcp/sqladmin.go:130::handleSQLGetBackupRun` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `DELETE /v1/projects/{project}/instances/{instance}/backupRuns/{id}` | ✓ `simulator-gcp/sqladmin.go:131::handleSQLDeleteBackupRun` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `POST /v1/projects/{project}/instances/{instance}/clone` | ✓ `simulator-gcp/sqladmin.go:132::handleSQLCloneInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `GET /v1/projects/{project}/operations/{operation}` | ✓ `simulator-gcp/sqladmin.go:134::handleSQLGetOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |
| `GET /v1/projects/{project}/operations` | ✓ `simulator-gcp/sqladmin.go:135::handleSQLListOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | also mounted at `/sql/v1beta4` |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
