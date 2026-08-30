# Sim surface — gcp-sqladmin

Surface registered in `simulator-gcp/sqladmin.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /sql/v1beta4/projects/{projectAction}` | ✓ `simulator-gcp/sqladmin.go:194::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /sql/v1beta4/projects/{project}/instances` | ✓ `simulator-gcp/sqladmin.go:215::handleSQLInsertInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/instances` | ✓ `simulator-gcp/sqladmin.go:215::handleSQLInsertInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/sqladmin.go:216::handleSQLGetInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/sqladmin.go:216::handleSQLGetInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances` | ✓ `simulator-gcp/sqladmin.go:217::handleSQLListInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances` | ✓ `simulator-gcp/sqladmin.go:217::handleSQLListInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /sql/v1beta4/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/sqladmin.go:218::handleSQLPatchInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/sqladmin.go:218::handleSQLPatchInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /sql/v1beta4/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/sqladmin.go:219::handleSQLUpdateInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/sqladmin.go:219::handleSQLUpdateInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /sql/v1beta4/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/sqladmin.go:220::handleSQLDeleteInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/sqladmin.go:220::handleSQLDeleteInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /sql/v1beta4/projects/{project}/instances/{instance}/databases` | ✓ `simulator-gcp/sqladmin.go:222::handleSQLInsertDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/instances/{instance}/databases` | ✓ `simulator-gcp/sqladmin.go:222::handleSQLInsertDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances/{instance}/databases/{database}` | ✓ `simulator-gcp/sqladmin.go:223::handleSQLGetDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances/{instance}/databases/{database}` | ✓ `simulator-gcp/sqladmin.go:223::handleSQLGetDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances/{instance}/databases` | ✓ `simulator-gcp/sqladmin.go:224::handleSQLListDatabases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances/{instance}/databases` | ✓ `simulator-gcp/sqladmin.go:224::handleSQLListDatabases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /sql/v1beta4/projects/{project}/instances/{instance}/databases/{database}` | ✓ `simulator-gcp/sqladmin.go:225::handleSQLPatchDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/instances/{instance}/databases/{database}` | ✓ `simulator-gcp/sqladmin.go:225::handleSQLPatchDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /sql/v1beta4/projects/{project}/instances/{instance}/databases/{database}` | ✓ `simulator-gcp/sqladmin.go:226::handleSQLUpdateDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/instances/{instance}/databases/{database}` | ✓ `simulator-gcp/sqladmin.go:226::handleSQLUpdateDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /sql/v1beta4/projects/{project}/instances/{instance}/databases/{database}` | ✓ `simulator-gcp/sqladmin.go:227::handleSQLDeleteDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/instances/{instance}/databases/{database}` | ✓ `simulator-gcp/sqladmin.go:227::handleSQLDeleteDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /sql/v1beta4/projects/{project}/instances/{instance}/users` | ✓ `simulator-gcp/sqladmin.go:229::handleSQLInsertUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/instances/{instance}/users` | ✓ `simulator-gcp/sqladmin.go:229::handleSQLInsertUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances/{instance}/users/{name}` | ✓ `simulator-gcp/sqladmin.go:230::handleSQLGetUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances/{instance}/users/{name}` | ✓ `simulator-gcp/sqladmin.go:230::handleSQLGetUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances/{instance}/users` | ✓ `simulator-gcp/sqladmin.go:231::handleSQLListUsers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances/{instance}/users` | ✓ `simulator-gcp/sqladmin.go:231::handleSQLListUsers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /sql/v1beta4/projects/{project}/instances/{instance}/users` | ✓ `simulator-gcp/sqladmin.go:232::handleSQLUpdateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v1/projects/{project}/instances/{instance}/users` | ✓ `simulator-gcp/sqladmin.go:232::handleSQLUpdateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /sql/v1beta4/projects/{project}/instances/{instance}/users` | ✓ `simulator-gcp/sqladmin.go:233::handleSQLDeleteUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/instances/{instance}/users` | ✓ `simulator-gcp/sqladmin.go:233::handleSQLDeleteUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /sql/v1beta4/projects/{project}/instances/{instance}/backupRuns` | ✓ `simulator-gcp/sqladmin.go:235::handleSQLInsertBackupRun` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/instances/{instance}/backupRuns` | ✓ `simulator-gcp/sqladmin.go:235::handleSQLInsertBackupRun` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances/{instance}/backupRuns` | ✓ `simulator-gcp/sqladmin.go:236::handleSQLListBackupRuns` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances/{instance}/backupRuns` | ✓ `simulator-gcp/sqladmin.go:236::handleSQLListBackupRuns` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances/{instance}/backupRuns/{id}` | ✓ `simulator-gcp/sqladmin.go:237::handleSQLGetBackupRun` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances/{instance}/backupRuns/{id}` | ✓ `simulator-gcp/sqladmin.go:237::handleSQLGetBackupRun` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /sql/v1beta4/projects/{project}/instances/{instance}/backupRuns/{id}` | ✓ `simulator-gcp/sqladmin.go:238::handleSQLDeleteBackupRun` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/instances/{instance}/backupRuns/{id}` | ✓ `simulator-gcp/sqladmin.go:238::handleSQLDeleteBackupRun` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /sql/v1beta4/projects/{project}/instances/{instance}/sslCerts` | ✓ `simulator-gcp/sqladmin.go:241::handleSQLInsertSslCert` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/instances/{instance}/sslCerts` | ✓ `simulator-gcp/sqladmin.go:241::handleSQLInsertSslCert` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances/{instance}/sslCerts` | ✓ `simulator-gcp/sqladmin.go:242::handleSQLListSslCerts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances/{instance}/sslCerts` | ✓ `simulator-gcp/sqladmin.go:242::handleSQLListSslCerts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances/{instance}/sslCerts/{sha1Fingerprint}` | ✓ `simulator-gcp/sqladmin.go:243::handleSQLGetSslCert` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances/{instance}/sslCerts/{sha1Fingerprint}` | ✓ `simulator-gcp/sqladmin.go:243::handleSQLGetSslCert` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /sql/v1beta4/projects/{project}/instances/{instance}/sslCerts/{sha1Fingerprint}` | ✓ `simulator-gcp/sqladmin.go:244::handleSQLDeleteSslCert` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/instances/{instance}/sslCerts/{sha1Fingerprint}` | ✓ `simulator-gcp/sqladmin.go:244::handleSQLDeleteSslCert` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /sql/v1beta4/projects/{project}/instances/{instance}/createEphemeral` | ✓ `simulator-gcp/sqladmin.go:245::handleSQLCreateEphemeralCert` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/instances/{instance}/createEphemeral` | ✓ `simulator-gcp/sqladmin.go:245::handleSQLCreateEphemeralCert` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances/{instance}/connectSettings` | ✓ `simulator-gcp/sqladmin.go:249::handleSQLConnectGet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances/{instance}/connectSettings` | ✓ `simulator-gcp/sqladmin.go:249::handleSQLConnectGet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/locations/{location}/dns/{dnsNameAction}` | ✓ `simulator-gcp/sqladmin.go:250::handleSQLConnectResolve` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/locations/{location}/dns/{dnsNameAction}` | ✓ `simulator-gcp/sqladmin.go:250::handleSQLConnectResolve` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /sql/v1beta4/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/sqladmin.go:251::handleSQLInstanceColonVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/sqladmin.go:251::handleSQLInstanceColonVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances/{instance}/getDiskShrinkConfig` | ✓ `simulator-gcp/sqladmin.go:254::handleSQLGetDiskShrinkConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances/{instance}/getDiskShrinkConfig` | ✓ `simulator-gcp/sqladmin.go:254::handleSQLGetDiskShrinkConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances/{instance}/getLatestRecoveryTime` | ✓ `simulator-gcp/sqladmin.go:255::handleSQLGetLatestRecoveryTime` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances/{instance}/getLatestRecoveryTime` | ✓ `simulator-gcp/sqladmin.go:255::handleSQLGetLatestRecoveryTime` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances/{instance}/listServerCas` | ✓ `simulator-gcp/sqladmin.go:256::handleSQLListServerCas` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances/{instance}/listServerCas` | ✓ `simulator-gcp/sqladmin.go:256::handleSQLListServerCas` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances/{instance}/listServerCertificates` | ✓ `simulator-gcp/sqladmin.go:257::handleSQLListServerCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances/{instance}/listServerCertificates` | ✓ `simulator-gcp/sqladmin.go:257::handleSQLListServerCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/instances/{instance}/listEntraIdCertificates` | ✓ `simulator-gcp/sqladmin.go:258::handleSQLListEntraIdCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/instances/{instance}/listEntraIdCertificates` | ✓ `simulator-gcp/sqladmin.go:258::handleSQLListEntraIdCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /sql/v1beta4/projects/{project}/instances/{instance}/clone` | ✓ `simulator-gcp/sqladmin.go:264::handleSQLCloneInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/instances/{instance}/clone` | ✓ `simulator-gcp/sqladmin.go:264::handleSQLCloneInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /sql/v1beta4/projects/{project}/instances/{instance}/restoreBackup` | ✓ `simulator-gcp/sqladmin.go:265::handleSQLRestoreBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/instances/{instance}/restoreBackup` | ✓ `simulator-gcp/sqladmin.go:265::handleSQLRestoreBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /sql/v1beta4/projects/{project}/instances/{instance}/executeSql` | ✓ `simulator-gcp/sqladmin.go:268::handleSQLExecuteSql` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/instances/{instance}/executeSql` | ✓ `simulator-gcp/sqladmin.go:268::handleSQLExecuteSql` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /sql/v1beta4/projects/{project}/instances/{instance}/acquireSsrsLease` | ✓ `simulator-gcp/sqladmin.go:269::handleSQLAcquireSsrsLease` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/instances/{instance}/acquireSsrsLease` | ✓ `simulator-gcp/sqladmin.go:269::handleSQLAcquireSsrsLease` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /sql/v1beta4/projects/{project}/instances/{instance}/releaseSsrsLease` | ✓ `simulator-gcp/sqladmin.go:270::handleSQLReleaseSsrsLease` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/instances/{instance}/releaseSsrsLease` | ✓ `simulator-gcp/sqladmin.go:270::handleSQLReleaseSsrsLease` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /sql/v1beta4/projects/{project}/instances/{instance}/verifyExternalSyncSettings` | ✓ `simulator-gcp/sqladmin.go:271::handleSQLVerifyExternalSyncSettings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/instances/{instance}/verifyExternalSyncSettings` | ✓ `simulator-gcp/sqladmin.go:271::handleSQLVerifyExternalSyncSettings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /sql/v1beta4/projects/{project}/backups` | ✓ `simulator-gcp/sqladmin.go:279::handleSQLCreateBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/backups` | ✓ `simulator-gcp/sqladmin.go:279::handleSQLCreateBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/backups` | ✓ `simulator-gcp/sqladmin.go:280::handleSQLListBackups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/backups` | ✓ `simulator-gcp/sqladmin.go:280::handleSQLListBackups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/backups/{backup}` | ✓ `simulator-gcp/sqladmin.go:281::handleSQLGetBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/backups/{backup}` | ✓ `simulator-gcp/sqladmin.go:281::handleSQLGetBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /sql/v1beta4/projects/{project}/backups/{backup}` | ✓ `simulator-gcp/sqladmin.go:282::handleSQLUpdateBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/backups/{backup}` | ✓ `simulator-gcp/sqladmin.go:282::handleSQLUpdateBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /sql/v1beta4/projects/{project}/backups/{backup}` | ✓ `simulator-gcp/sqladmin.go:283::handleSQLDeleteBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/backups/{backup}` | ✓ `simulator-gcp/sqladmin.go:283::handleSQLDeleteBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/operations/{operation}` | ✓ `simulator-gcp/sqladmin.go:285::handleSQLGetOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/operations/{operation}` | ✓ `simulator-gcp/sqladmin.go:285::handleSQLGetOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/operations` | ✓ `simulator-gcp/sqladmin.go:286::handleSQLListOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/operations` | ✓ `simulator-gcp/sqladmin.go:286::handleSQLListOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /sql/v1beta4/projects/{project}/operations/{operation}/cancel` | ✓ `simulator-gcp/sqladmin.go:287::handleSQLCancelOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/operations/{operation}/cancel` | ✓ `simulator-gcp/sqladmin.go:287::handleSQLCancelOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/projects/{project}/tiers` | ○ `simulator-gcp/sqladmin.go:289::handleSQLListTiers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/tiers` | ○ `simulator-gcp/sqladmin.go:289::handleSQLListTiers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /sql/v1beta4/flags` | ○ `simulator-gcp/sqladmin.go:290::handleSQLListFlags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/flags` | ○ `simulator-gcp/sqladmin.go:290::handleSQLListFlags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
