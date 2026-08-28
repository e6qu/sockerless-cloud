# Sim surface — gcp-firestore

Surface registered in `simulator-gcp/firestore.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /v1/projects/{project}/databases/{database}/documents:beginTransaction` | ✓ `simulator-gcp/firestore.go:177::handleFSBeginTransaction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:rollback` | ✓ `simulator-gcp/firestore.go:178::handleFSRollback` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:commit` | ✓ `simulator-gcp/firestore.go:179::handleFSCommit` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:batchGet` | ✓ `simulator-gcp/firestore.go:180::handleFSBatchGet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:batchWrite` | ✓ `simulator-gcp/firestore.go:181::handleFSBatchWrite` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:runQuery` | ✓ `simulator-gcp/firestore.go:182::handleFSRunRootQuery` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents/{postPath...}` | ✓ `simulator-gcp/firestore.go:183::handleFSPostDocuments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/documents/{docPath...}` | ✓ `simulator-gcp/firestore.go:184::handleFSGetOrList` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/databases/{database}/documents/{docPath...}` | ✓ `simulator-gcp/firestore.go:185::handleFSPatchDocument` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}/documents/{docPath...}` | ✓ `simulator-gcp/firestore.go:186::handleFSDeleteDocument` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases` | ✓ `simulator-gcp/firestore.go:1215::handleFSDatabasesCollection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases` | ✓ `simulator-gcp/firestore.go:1216::handleFSListDatabases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}` | ✓ `simulator-gcp/firestore.go:1217::handleFSGetDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/databases/{database}` | ✓ `simulator-gcp/firestore.go:1218::handleFSPatchDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}` | ✓ `simulator-gcp/firestore.go:1219::handleFSDeleteDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{dbAction}` | ✓ `simulator-gcp/firestore.go:1220::handleFSDatabaseVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes` | ✓ `simulator-gcp/firestore.go:1223::handleFSCreateIndex` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes` | ✓ `simulator-gcp/firestore.go:1224::handleFSListIndexes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes/{index}` | ✓ `simulator-gcp/firestore.go:1225::handleFSGetIndex` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes/{index}` | ✓ `simulator-gcp/firestore.go:1226::handleFSDeleteIndex` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields` | ✓ `simulator-gcp/firestore.go:1229::handleFSListFields` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields/{field}` | ✓ `simulator-gcp/firestore.go:1230::handleFSGetField` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields/{field}` | ✓ `simulator-gcp/firestore.go:1231::handleFSPatchField` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/backupSchedules` | ✓ `simulator-gcp/firestore.go:1234::handleFSCreateBackupSchedule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/backupSchedules` | ✓ `simulator-gcp/firestore.go:1235::handleFSListBackupSchedules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/backupSchedules/{bs}` | ✓ `simulator-gcp/firestore.go:1236::handleFSGetBackupSchedule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/databases/{database}/backupSchedules/{bs}` | ✓ `simulator-gcp/firestore.go:1237::handleFSPatchBackupSchedule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}/backupSchedules/{bs}` | ✓ `simulator-gcp/firestore.go:1238::handleFSDeleteBackupSchedule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/backups` | ✓ `simulator-gcp/firestore.go:1241::handleFSListBackups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/backups/{backup}` | ✓ `simulator-gcp/firestore.go:1242::handleFSGetBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/backups/{backup}` | ✓ `simulator-gcp/firestore.go:1243::handleFSDeleteBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/userCreds` | ✓ `simulator-gcp/firestore.go:1246::handleFSUserCredsCollection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/userCreds` | ✓ `simulator-gcp/firestore.go:1247::handleFSListUserCreds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/userCreds/{uc}` | ✓ `simulator-gcp/firestore.go:1248::handleFSGetUserCreds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}/userCreds/{uc}` | ✓ `simulator-gcp/firestore.go:1249::handleFSDeleteUserCreds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/userCreds/{ucAction}` | ✓ `simulator-gcp/firestore.go:1250::handleFSUserCredsVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/operations` | ✓ `simulator-gcp/firestore.go:1253::handleFSListOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/operations/{operation}` | ✓ `simulator-gcp/firestore.go:1254::handleFSGetOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}/operations/{operation}` | ✓ `simulator-gcp/firestore.go:1255::handleFSDeleteOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/operations/{opAction}` | ✓ `simulator-gcp/firestore.go:1256::handleFSCancelOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
