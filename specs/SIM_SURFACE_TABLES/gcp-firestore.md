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
| `POST /v1/projects/{project}/databases` | ✓ `simulator-gcp/firestore.go:1225::handleFSDatabasesCollection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/{databasesVerb}` | ✓ `simulator-gcp/firestore.go:1229::handleFSDatabasesVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases` | ✓ `simulator-gcp/firestore.go:1231::handleFSListDatabases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}` | ✓ `simulator-gcp/firestore.go:1232::handleFSGetDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/databases/{database}` | ✓ `simulator-gcp/firestore.go:1233::handleFSPatchDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}` | ✓ `simulator-gcp/firestore.go:1234::handleFSDeleteDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{dbAction}` | ✓ `simulator-gcp/firestore.go:1235::handleFSDatabaseVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes` | ✓ `simulator-gcp/firestore.go:1238::handleFSCreateIndex` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes` | ✓ `simulator-gcp/firestore.go:1239::handleFSListIndexes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes/{index}` | ✓ `simulator-gcp/firestore.go:1240::handleFSGetIndex` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes/{index}` | ✓ `simulator-gcp/firestore.go:1241::handleFSDeleteIndex` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields` | ✓ `simulator-gcp/firestore.go:1244::handleFSListFields` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields/{field}` | ✓ `simulator-gcp/firestore.go:1245::handleFSGetField` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields/{field}` | ✓ `simulator-gcp/firestore.go:1246::handleFSPatchField` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/backupSchedules` | ✓ `simulator-gcp/firestore.go:1249::handleFSCreateBackupSchedule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/backupSchedules` | ✓ `simulator-gcp/firestore.go:1250::handleFSListBackupSchedules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/backupSchedules/{bs}` | ✓ `simulator-gcp/firestore.go:1251::handleFSGetBackupSchedule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/databases/{database}/backupSchedules/{bs}` | ✓ `simulator-gcp/firestore.go:1252::handleFSPatchBackupSchedule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}/backupSchedules/{bs}` | ✓ `simulator-gcp/firestore.go:1253::handleFSDeleteBackupSchedule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/backups` | ✓ `simulator-gcp/firestore.go:1256::handleFSListBackups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/backups/{backup}` | ✓ `simulator-gcp/firestore.go:1257::handleFSGetBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/backups/{backup}` | ✓ `simulator-gcp/firestore.go:1258::handleFSDeleteBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/userCreds` | ✓ `simulator-gcp/firestore.go:1261::handleFSUserCredsCollection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/userCreds` | ✓ `simulator-gcp/firestore.go:1262::handleFSListUserCreds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/userCreds/{uc}` | ✓ `simulator-gcp/firestore.go:1263::handleFSGetUserCreds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}/userCreds/{uc}` | ✓ `simulator-gcp/firestore.go:1264::handleFSDeleteUserCreds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/userCreds/{ucAction}` | ✓ `simulator-gcp/firestore.go:1265::handleFSUserCredsVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/operations` | ✓ `simulator-gcp/firestore.go:1268::handleFSListOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/operations/{operation}` | ✓ `simulator-gcp/firestore.go:1269::handleFSGetOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}/operations/{operation}` | ✓ `simulator-gcp/firestore.go:1270::handleFSDeleteOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/operations/{opAction}` | ✓ `simulator-gcp/firestore.go:1271::handleFSCancelOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:beginTransaction` | ✓ `simulator-gcp/firestore.go:177::handleFSBeginTransaction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:rollback` | ✓ `simulator-gcp/firestore.go:178::handleFSRollback` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:commit` | ✓ `simulator-gcp/firestore.go:179::handleFSCommit` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:batchGet` | ✓ `simulator-gcp/firestore.go:180::handleFSBatchGet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:batchWrite` | ✓ `simulator-gcp/firestore.go:181::handleFSBatchWrite` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:runQuery` | ✓ `simulator-gcp/firestore.go:182::handleFSRunRootQuery` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:listCollectionIds` | ✓ `simulator-gcp/firestore.go:185::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:runAggregationQuery` | ✓ `simulator-gcp/firestore.go:187::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents:partitionQuery` | ✓ `simulator-gcp/firestore.go:189::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/databases/{database}/documents/{postPath...}` | ✓ `simulator-gcp/firestore.go:191::handleFSPostDocuments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/databases/{database}/documents/{docPath...}` | ✓ `simulator-gcp/firestore.go:192::handleFSGetOrList` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/databases/{database}/documents/{docPath...}` | ✓ `simulator-gcp/firestore.go:193::handleFSPatchDocument` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/databases/{database}/documents/{docPath...}` | ✓ `simulator-gcp/firestore.go:194::handleFSDeleteDocument` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
