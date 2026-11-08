# Sim surface — gcp-firestore

Surface registered in `simulator-gcp/firestore.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /v1/projects/{project}/databases` | ✓ `simulator-gcp/firestore.go:1264::handleFSDatabasesCollection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/{databasesVerb}` | ✓ `simulator-gcp/firestore.go:1268::handleFSDatabasesVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases` | ✓ `simulator-gcp/firestore.go:1270::handleFSListDatabases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}` | ✓ `simulator-gcp/firestore.go:1271::handleFSGetDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/databases/{database}` | ✓ `simulator-gcp/firestore.go:1272::handleFSPatchDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}` | ✓ `simulator-gcp/firestore.go:1273::handleFSDeleteDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{dbAction}` | ✓ `simulator-gcp/firestore.go:1274::handleFSDatabaseVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes` | ✓ `simulator-gcp/firestore.go:1276::handleFSCreateIndex` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes` | ✓ `simulator-gcp/firestore.go:1277::handleFSListIndexes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes/{index}` | ✓ `simulator-gcp/firestore.go:1278::handleFSGetIndex` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes/{index}` | ✓ `simulator-gcp/firestore.go:1279::handleFSDeleteIndex` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields` | ✓ `simulator-gcp/firestore.go:1281::handleFSListFields` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields/{field}` | ✓ `simulator-gcp/firestore.go:1282::handleFSGetField` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields/{field}` | ✓ `simulator-gcp/firestore.go:1283::handleFSPatchField` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/backupSchedules` | ✓ `simulator-gcp/firestore.go:1285::handleFSCreateBackupSchedule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/backupSchedules` | ✓ `simulator-gcp/firestore.go:1286::handleFSListBackupSchedules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/backupSchedules/{bs}` | ✓ `simulator-gcp/firestore.go:1287::handleFSGetBackupSchedule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/databases/{database}/backupSchedules/{bs}` | ✓ `simulator-gcp/firestore.go:1288::handleFSPatchBackupSchedule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}/backupSchedules/{bs}` | ✓ `simulator-gcp/firestore.go:1289::handleFSDeleteBackupSchedule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/backups` | ✓ `simulator-gcp/firestore.go:1291::handleFSListBackups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/backups/{backup}` | ✓ `simulator-gcp/firestore.go:1292::handleFSGetBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/backups/{backup}` | ✓ `simulator-gcp/firestore.go:1293::handleFSDeleteBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/userCreds` | ✓ `simulator-gcp/firestore.go:1295::handleFSUserCredsCollection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/userCreds` | ✓ `simulator-gcp/firestore.go:1296::handleFSListUserCreds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/userCreds/{uc}` | ✓ `simulator-gcp/firestore.go:1297::handleFSGetUserCreds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}/userCreds/{uc}` | ✓ `simulator-gcp/firestore.go:1298::handleFSDeleteUserCreds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/userCreds/{ucAction}` | ✓ `simulator-gcp/firestore.go:1299::handleFSUserCredsVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/operations` | ✓ `simulator-gcp/firestore.go:1301::handleFSListOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/operations/{operation}` | ✓ `simulator-gcp/firestore.go:1302::handleFSGetOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}/operations/{operation}` | ✓ `simulator-gcp/firestore.go:1303::handleFSDeleteOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/operations/{opAction}` | ✓ `simulator-gcp/firestore.go:1304::handleFSCancelOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:beginTransaction` | ✓ `simulator-gcp/firestore.go:187::handleFSBeginTransaction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:rollback` | ✓ `simulator-gcp/firestore.go:188::handleFSRollback` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:commit` | ✓ `simulator-gcp/firestore.go:189::handleFSCommit` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:batchGet` | ✓ `simulator-gcp/firestore.go:190::handleFSBatchGet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:batchWrite` | ✓ `simulator-gcp/firestore.go:191::handleFSBatchWrite` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:runQuery` | ✓ `simulator-gcp/firestore.go:192::handleFSRunRootQuery` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:listCollectionIds` | ○ `simulator-gcp/firestore.go:195::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:runAggregationQuery` | ✓ `simulator-gcp/firestore.go:197::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:partitionQuery` | ○ `simulator-gcp/firestore.go:199::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents/{postPath...}` | ✓ `simulator-gcp/firestore.go:201::handleFSPostDocuments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/documents/{docPath...}` | ✓ `simulator-gcp/firestore.go:202::handleFSGetOrList` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/databases/{database}/documents/{docPath...}` | ✓ `simulator-gcp/firestore.go:203::handleFSPatchDocument` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}/documents/{docPath...}` | ✓ `simulator-gcp/firestore.go:204::handleFSDeleteDocument` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
