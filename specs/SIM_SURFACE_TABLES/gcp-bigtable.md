# Sim surface — gcp-bigtable

Surface registered in `simulator-gcp/bigtable.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /v2/projects/{project}/instances/{instance}/clusters` | ✓ `simulator-gcp/bigtable.go:100::handleBigtableCreateCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/clusters` | ✓ `simulator-gcp/bigtable.go:101::handleBigtableListClusters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}` | ✓ `simulator-gcp/bigtable.go:102::handleBigtableGetCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v2/projects/{project}/instances/{instance}/clusters/{cluster}` | ✓ `simulator-gcp/bigtable.go:103::handleBigtableUpdateCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/instances/{instance}/clusters/{cluster}` | ✓ `simulator-gcp/bigtable.go:104::handleBigtablePartialUpdateCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/instances/{instance}/clusters/{cluster}` | ✓ `simulator-gcp/bigtable.go:105::handleBigtableDeleteCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}/hotTablets` | ✓ `simulator-gcp/bigtable.go:108::handleBigtableListHotTablets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}/memoryLayer` | ✓ `simulator-gcp/bigtable.go:109::handleBigtableGetMemoryLayer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/instances/{instance}/clusters/{cluster}/memoryLayer` | ✓ `simulator-gcp/bigtable.go:110::handleBigtableUpdateMemoryLayer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}/memoryLayers` | ✓ `simulator-gcp/bigtable.go:111::handleBigtableListMemoryLayers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups` | ✓ `simulator-gcp/bigtable.go:116::handleBigtableCreateBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups` | ✓ `simulator-gcp/bigtable.go:117::handleBigtableListBackups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instance}/clusters/{cluster}/{backupsColl}` | ✓ `simulator-gcp/bigtable.go:118::handleBigtableBackupCollectionAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups/{backupAction}` | ✓ `simulator-gcp/bigtable.go:120::handleBigtableBackupItemAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups/{backup}` | ✓ `simulator-gcp/bigtable.go:121::handleBigtableGetBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups/{backup}` | ✓ `simulator-gcp/bigtable.go:122::handleBigtablePatchBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups/{backup}` | ✓ `simulator-gcp/bigtable.go:123::handleBigtableDeleteBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instance}/appProfiles` | ✓ `simulator-gcp/bigtable.go:126::handleBigtableCreateAppProfile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/appProfiles` | ✓ `simulator-gcp/bigtable.go:127::handleBigtableListAppProfiles` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/appProfiles/{appProfile}` | ✓ `simulator-gcp/bigtable.go:128::handleBigtableGetAppProfile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/instances/{instance}/appProfiles/{appProfile}` | ✓ `simulator-gcp/bigtable.go:129::handleBigtablePatchAppProfile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/instances/{instance}/appProfiles/{appProfile}` | ✓ `simulator-gcp/bigtable.go:130::handleBigtableDeleteAppProfile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instance}/tables` | ✓ `simulator-gcp/bigtable.go:135::handleBigtableCreateTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/tables` | ✓ `simulator-gcp/bigtable.go:136::handleBigtableListTables` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instance}/{tablesColl}` | ✓ `simulator-gcp/bigtable.go:137::handleBigtableTableCollectionAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instance}/tables/{tableAction}` | ✓ `simulator-gcp/bigtable.go:140::handleBigtableTableAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/tables/{table}` | ✓ `simulator-gcp/bigtable.go:141::handleBigtableGetTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/instances/{instance}/tables/{table}` | ✓ `simulator-gcp/bigtable.go:142::handleBigtablePatchTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/instances/{instance}/tables/{table}` | ✓ `simulator-gcp/bigtable.go:143::handleBigtableDeleteTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews` | ✓ `simulator-gcp/bigtable.go:146::handleBigtableCreateAuthView` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews` | ✓ `simulator-gcp/bigtable.go:147::handleBigtableListAuthViews` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews/{authViewAction}` | ✓ `simulator-gcp/bigtable.go:148::handleBigtableAuthViewItemAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews/{authView}` | ✓ `simulator-gcp/bigtable.go:149::handleBigtableGetAuthView` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews/{authView}` | ✓ `simulator-gcp/bigtable.go:150::handleBigtablePatchAuthView` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews/{authView}` | ✓ `simulator-gcp/bigtable.go:151::handleBigtableDeleteAuthView` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles` | ✓ `simulator-gcp/bigtable.go:154::handleBigtableCreateSchemaBundle` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles` | ✓ `simulator-gcp/bigtable.go:155::handleBigtableListSchemaBundles` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles/{schemaBundleAction}` | ✓ `simulator-gcp/bigtable.go:156::handleBigtableSchemaBundleItemAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles/{schemaBundle}` | ✓ `simulator-gcp/bigtable.go:157::handleBigtableGetSchemaBundle` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles/{schemaBundle}` | ✓ `simulator-gcp/bigtable.go:158::handleBigtablePatchSchemaBundle` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles/{schemaBundle}` | ✓ `simulator-gcp/bigtable.go:159::handleBigtableDeleteSchemaBundle` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instance}/logicalViews` | ✓ `simulator-gcp/bigtable.go:162::handleBigtableCreateLogicalView` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/logicalViews` | ✓ `simulator-gcp/bigtable.go:163::handleBigtableListLogicalViews` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instance}/logicalViews/{logicalViewAction}` | ✓ `simulator-gcp/bigtable.go:164::handleBigtableLogicalViewItemAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/logicalViews/{logicalView}` | ✓ `simulator-gcp/bigtable.go:165::handleBigtableGetLogicalView` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/instances/{instance}/logicalViews/{logicalView}` | ✓ `simulator-gcp/bigtable.go:166::handleBigtablePatchLogicalView` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/instances/{instance}/logicalViews/{logicalView}` | ✓ `simulator-gcp/bigtable.go:167::handleBigtableDeleteLogicalView` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instance}/materializedViews` | ✓ `simulator-gcp/bigtable.go:170::handleBigtableCreateMatView` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/materializedViews` | ✓ `simulator-gcp/bigtable.go:171::handleBigtableListMatViews` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instance}/materializedViews/{matViewAction}` | ✓ `simulator-gcp/bigtable.go:172::handleBigtableMatViewItemAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}/materializedViews/{matView}` | ✓ `simulator-gcp/bigtable.go:173::handleBigtableGetMatView` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/instances/{instance}/materializedViews/{matView}` | ✓ `simulator-gcp/bigtable.go:174::handleBigtablePatchMatView` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/instances/{instance}/materializedViews/{matView}` | ✓ `simulator-gcp/bigtable.go:175::handleBigtableDeleteMatView` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/operations/projects/{project}/operations` | ✓ `simulator-gcp/bigtable.go:86::handleBigtableListOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances` | ✓ `simulator-gcp/bigtable.go:89::handleBigtableCreateInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances` | ✓ `simulator-gcp/bigtable.go:90::handleBigtableListInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/instances/{instanceAction}` | ✓ `simulator-gcp/bigtable.go:93::handleBigtableInstanceAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/bigtable.go:94::handleBigtableGetInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/bigtable.go:95::handleBigtablePartialUpdateInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v2/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/bigtable.go:96::handleBigtableReplaceInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/instances/{instance}` | ✓ `simulator-gcp/bigtable.go:97::handleBigtableDeleteInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
