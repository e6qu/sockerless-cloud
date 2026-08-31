# Sim surface — gcp-spanner

Surface registered in `simulator-gcp/spanner.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /spanner/v1/projects/{project}/instances` | ✓ `simulator-gcp/spanner.go:115::handleSpannerCreateInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /spanner/v1/projects/{project}/instances` | ✓ `simulator-gcp/spanner.go:116::handleSpannerListInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /spanner/v1/projects/{project}/instances/{rest...}` | ✓ `simulator-gcp/spanner.go:117::handleSpannerInstanceChild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /spanner/v1/projects/{project}/instances/{rest...}` | ✓ `simulator-gcp/spanner.go:118::handleSpannerInstanceChild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /spanner/v1/projects/{project}/instances/{rest...}` | ✓ `simulator-gcp/spanner.go:119::handleSpannerInstanceChild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /spanner/v1/projects/{project}/instances/{rest...}` | ✓ `simulator-gcp/spanner.go:120::handleSpannerInstanceChild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /spanner/v1/projects/{project}/instanceConfigs` | ✓ `simulator-gcp/spanner.go:126::handleSpannerCreateInstanceConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /spanner/v1/projects/{project}/instanceConfigs` | ✓ `simulator-gcp/spanner.go:127::handleSpannerListInstanceConfigs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /spanner/v1/projects/{project}/instanceConfigs/{config}` | ✓ `simulator-gcp/spanner.go:128::handleSpannerGetInstanceConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /spanner/v1/projects/{project}/instanceConfigs/{config}` | ✓ `simulator-gcp/spanner.go:129::handleSpannerUpdateInstanceConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /spanner/v1/projects/{project}/instanceConfigs/{config}` | ✓ `simulator-gcp/spanner.go:130::handleSpannerDeleteInstanceConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /spanner/v1/projects/{project}/instanceConfigs/{config}/operations` | ✓ `simulator-gcp/spanner.go:131::handleSpannerListInstanceConfigOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /spanner/v1/projects/{project}/instanceConfigs/{config}/operations/{operation}` | ✓ `simulator-gcp/spanner.go:132::handleSpannerGetInstanceConfigOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /spanner/v1/projects/{project}/instanceConfigs/{config}/operations/{operation}` | ✓ `simulator-gcp/spanner.go:133::handleSpannerDeleteInstanceConfigOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /spanner/v1/projects/{project}/instanceConfigs/{config}/operations/{operation}` | ✓ `simulator-gcp/spanner.go:134::handleSpannerInstanceConfigOperationAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /spanner/v1/projects/{project}/instanceConfigs/{config}/ssdCaches/{ssdCache}/operations` | ✓ `simulator-gcp/spanner.go:135::handleSpannerListInstanceConfigOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /spanner/v1/projects/{project}/instanceConfigs/{config}/ssdCaches/{ssdCache}/operations/{operation}` | ✓ `simulator-gcp/spanner.go:136::handleSpannerGetInstanceConfigOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /spanner/v1/projects/{project}/instanceConfigs/{config}/ssdCaches/{ssdCache}/operations/{operation}` | ✓ `simulator-gcp/spanner.go:137::handleSpannerDeleteInstanceConfigOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /spanner/v1/projects/{project}/instanceConfigs/{config}/ssdCaches/{ssdCache}/operations/{operation}` | ✓ `simulator-gcp/spanner.go:138::handleSpannerInstanceConfigOperationAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /spanner/v1/projects/{project}/instanceConfigOperations` | ✓ `simulator-gcp/spanner.go:140::handleSpannerListInstanceConfigOperationsCollection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /spanner/v1/scans` | ○ `simulator-gcp/spanner.go:141::handleSpannerListScans` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
The session data plane and the whole instance-scoped administrative surface are
mounted by the dynamic Cloud Spanner child router (`instances/{rest...}`), so
the extractor cannot recover these paths from individual `HandleFunc` calls.
The official Google Cloud SDK exercised both REST and gRPC;
`gcloud spanner databases execute-sql` exercised SQL and partitioned DML over
REST. Terraform covered the administrative instance/database/DDL surface plus
backup schedules, instance partitions and the IAM bindings; the official
provider exposed no resource for session-scoped data-plane calls.

Backups hold the database's real bytes: `CreateBackup` serializes the live
SQLite engine the data plane executes against (`spannerCaptureBackupImage`,
SQLite `VACUUM INTO`) and `RestoreDatabase` rebuilds a new engine from that
image (`spannerMaterializeFromImage`), so a create → write → back up → drop →
restore → read round trip returns the same rows. Backup schedules are executed:
`spannerRunDueBackupSchedules` takes a real backup on every crontab occurrence.

Five documented methods are deliberately unserved because the simulator holds
nothing for them to report or act on — `databases.getScans` (no Key Visualizer
traffic heatmap), `databases.addSplitPoints` (no key-space splits),
`databases.changequorum` (one replica, no quorum), and `sessions.adapter` /
`sessions.adaptMessage` (raw PostgreSQL and Cassandra wire protocols). The
floor comment in `simulator-gcp/gcp_coverage_test.go` carries the same
reasoning.

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `GET .../databases/{database}/ddl`, `PATCH .../databases/{database}` | ✓ `simulator-gcp/spanner_admin.go::handleSpannerGetDatabaseDdl`, `handleSpannerUpdateDatabase` | ✓ `spanner_backup_admin_test.go` | ✓ `spanner_admin_test.go` | n/a | Drop protection enforced by DropDatabase and DeleteInstance |
| `POST/GET/PATCH/DELETE .../backups[/{backup}]` | ✓ `simulator-gcp/spanner_backups.go::handleSpannerCreateBackup` + siblings | ✓ `spanner_backup_admin_test.go` | n/a | ✓ | Captures the database's real SQLite image |
| `POST .../backups:copy` | ✓ `simulator-gcp/spanner_backups.go::handleSpannerCopyBackup` | ✓ `spanner_backup_admin_test.go` | n/a | n/a | The copy carries the source's captured bytes |
| `POST .../databases:restore` | ✓ `simulator-gcp/spanner_backups.go::handleSpannerRestoreDatabase` | ✓ `spanner_backup_admin_test.go` | n/a | n/a | Rebuilds the engine from the backup image |
| `POST/GET/PATCH/DELETE .../databases/{database}/backupSchedules[/{schedule}]` | ✓ `simulator-gcp/spanner_backups.go::handleSpannerCreateBackupSchedule` + siblings | ✓ `spanner_backup_admin_test.go` | ✓ `spanner_admin_test.go` | ✓ | Crontab occurrences take real backups |
| `GET .../databases/{database}/databaseRoles` | ✓ `simulator-gcp/spanner_admin.go::handleSpannerListDatabaseRoles` | ✓ `spanner_backup_admin_test.go` | n/a | ✓ | Folded out of the database's CREATE ROLE / DROP ROLE DDL |
| `POST/GET/PATCH/DELETE .../instancePartitions[/{partition}]` | ✓ `simulator-gcp/spanner_admin.go::handleSpannerCreateInstancePartition` + siblings | ✓ `spanner_backup_admin_test.go` | ✓ `spanner_admin_test.go` | ✓ | |
| `POST .../instances/{instance}:move` | ✓ `simulator-gcp/spanner_admin.go::handleSpannerMoveInstance` | ✓ `spanner_backup_admin_test.go` | n/a | n/a | Relocates the instance to the target configuration |
| `POST .../{resource}:getIamPolicy` / `:setIamPolicy` / `:testIamPermissions` | ✓ `simulator-gcp/spanner_admin.go` + `iam.go::handleResourceIAM` | ✓ `spanner_backup_admin_test.go` | ✓ `spanner_admin_test.go` | n/a | Instances, databases, backups, backup schedules, database roles |
| `GET`/`DELETE`/`:cancel` on all five operation collections | ✓ `simulator-gcp/spanner_admin.go::spannerRouteOperation`, `handleSpannerListInstanceScopedOperations` | ✓ `spanner_backup_admin_test.go` | n/a | ✓ | instances, databases, backups, instancePartitions, instanceConfigs |
| `POST .../sessions:batchCreate` | ✓ `simulator-gcp/spanner_rest.go::handleSpannerBatchCreateSessionsREST` | ✓ `spanner_rest_data_test.go` | n/a | n/a | Official REST SDK |
| `POST .../sessions/{session}:executeSql` | ✓ `simulator-gcp/spanner_rest.go::handleSpannerSessionActionREST` | ✓ REST + gRPC | n/a | n/a | Real SQL query and DML execution |
| `POST .../sessions/{session}:executeStreamingSql` | ✓ `simulator-gcp/spanner_rest.go::handleSpannerSessionActionREST` | ✓ REST + gRPC | n/a | n/a | Complete `PartialResultSet` stream |
| `POST .../sessions/{session}:executeBatchDml` | ✓ `simulator-gcp/spanner_rest.go::handleSpannerSessionActionREST` | ✓ REST + gRPC | n/a | n/a | Ordered execution, replay, and partial status |
| `POST .../sessions/{session}:read` | ✓ `simulator-gcp/spanner_rest.go::handleSpannerSessionActionREST` | ✓ REST + gRPC | n/a | n/a | Transactional key-set read |
| `POST .../sessions/{session}:streamingRead` | ✓ `simulator-gcp/spanner_rest.go::handleSpannerSessionActionREST` | ✓ REST + gRPC | n/a | n/a | Complete `PartialResultSet` stream |
| `POST .../sessions/{session}:beginTransaction` | ✓ `simulator-gcp/spanner_rest.go::handleSpannerSessionActionREST` | ✓ REST + gRPC | n/a | n/a | Real SQLite transaction lifecycle |
| `POST .../sessions/{session}:commit` | ✓ `simulator-gcp/spanner_rest.go::handleSpannerSessionActionREST` | ✓ REST + gRPC | n/a | n/a | Commits DML and mutations atomically |
| `POST .../sessions/{session}:rollback` | ✓ `simulator-gcp/spanner_rest.go::handleSpannerSessionActionREST` | ✓ REST + gRPC | n/a | n/a | Discards uncommitted changes |
| `POST .../sessions/{session}:partitionQuery` | ✓ `simulator-gcp/spanner_rest.go::handleSpannerSessionActionREST` | ✓ `spanner_rest_data_test.go` | n/a | n/a | Opaque token bound to request and read-only transaction |
| `POST .../sessions/{session}:partitionRead` | ✓ `simulator-gcp/spanner_rest.go::handleSpannerSessionActionREST` | ✓ `spanner_rest_data_test.go` | n/a | n/a | Opaque token bound to request and read-only transaction |
| `POST .../sessions/{session}:batchWrite` | ✓ `simulator-gcp/spanner_rest.go::handleSpannerSessionActionREST` | ✓ REST + gRPC | n/a | n/a | Independent atomic mutation groups |
<!-- HAND-WRITTEN END -->
