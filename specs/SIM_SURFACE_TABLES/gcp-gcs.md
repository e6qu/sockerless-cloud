# Sim surface — gcp-gcs

Surface registered in `simulator-gcp/gcs.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `PUT /upload/storage/v1/b/{bucket}/o` | ✓ `simulator-gcp/gcs.go:1012::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /upload/storage/v1/b/{bucket}/o` | ✓ `simulator-gcp/gcs.go:1024::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/o/{destObject...}` | ✓ `simulator-gcp/gcs.go:1195::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action /{bucket}/{object...}` | ✓ `simulator-gcp/gcs.go:1288::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /download/storage/v1/b/{bucket}/o/{object...}` | ✓ `simulator-gcp/gcs.go:1325::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/acl` | ✓ `simulator-gcp/gcs.go:1701::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/acl/{entity}` | ✓ `simulator-gcp/gcs.go:1714::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/acl` | ✓ `simulator-gcp/gcs.go:1742::insert` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}/acl/{entity}` | ✓ `simulator-gcp/gcs.go:1762::update` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/acl/{entity}` | ✓ `simulator-gcp/gcs.go:1763::update` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/acl/{entity}` | ✓ `simulator-gcp/gcs.go:1765::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/defaultObjectAcl` | ✓ `simulator-gcp/gcs.go:1792::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/defaultObjectAcl/{entity}` | ✓ `simulator-gcp/gcs.go:1805::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/defaultObjectAcl` | ✓ `simulator-gcp/gcs.go:1815::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}/defaultObjectAcl/{entity}` | ✓ `simulator-gcp/gcs.go:1852::update` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/defaultObjectAcl/{entity}` | ✓ `simulator-gcp/gcs.go:1853::update` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/defaultObjectAcl/{entity}` | ✓ `simulator-gcp/gcs.go:1855::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/folders` | ✓ `simulator-gcp/gcs.go:1881::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/folders/{folder}` | ✓ `simulator-gcp/gcs.go:1906::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/folders` | ✓ `simulator-gcp/gcs.go:1916::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/folders/{folder}` | ✓ `simulator-gcp/gcs.go:1939::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/folders/{folder}/deleteRecursive` | ✓ `simulator-gcp/gcs.go:1951::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/folders/{sourceFolder}/renameTo/folders/{destinationFolder}` | ✓ `simulator-gcp/gcs.go:1966::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/managedFolders` | ✓ `simulator-gcp/gcs.go:2000::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}` | ✓ `simulator-gcp/gcs.go:2025::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/managedFolders` | ✓ `simulator-gcp/gcs.go:2035::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/managedFolders/{managedFolder}` | ✓ `simulator-gcp/gcs.go:2064::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/managedFolders/{managedFolder}` | ✓ `simulator-gcp/gcs.go:2096::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam` | ✓ `simulator-gcp/gcs.go:2110::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam` | ✓ `simulator-gcp/gcs.go:2126::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam/testPermissions` | ○ `simulator-gcp/gcs.go:2159::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/notificationConfigs` | ✓ `simulator-gcp/gcs.go:2167::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/notificationConfigs/{notification}` | ✓ `simulator-gcp/gcs.go:2180::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/notificationConfigs` | ✓ `simulator-gcp/gcs.go:2190::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/notificationConfigs/{notification}` | ✓ `simulator-gcp/gcs.go:2216::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/projects/{projectId}/serviceAccount` | ○ `simulator-gcp/gcs.go:2227::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/projects/{projectId}/hmacKeys` | ✓ `simulator-gcp/gcs.go:2235::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/projects/{projectId}/hmacKeys/{accessId}` | ✓ `simulator-gcp/gcs.go:2267::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/projects/{projectId}/hmacKeys` | ✓ `simulator-gcp/gcs.go:2277::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/projects/{projectId}/hmacKeys/{accessId}` | ✓ `simulator-gcp/gcs.go:2308::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/projects/{projectId}/hmacKeys/{accessId}` | ✓ `simulator-gcp/gcs.go:2328::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/anywhereCaches` | ✓ `simulator-gcp/gcs.go:2348::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}` | ✓ `simulator-gcp/gcs.go:2370::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/anywhereCaches` | ✓ `simulator-gcp/gcs.go:2382::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}` | ✓ `simulator-gcp/gcs.go:2411::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/pause` | ✓ `simulator-gcp/gcs.go:2448::stateVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/resume` | ✓ `simulator-gcp/gcs.go:2449::stateVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/disable` | ✓ `simulator-gcp/gcs.go:2450::stateVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/rapidCaches` | ✓ `simulator-gcp/gcs.go:2481::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}` | ✓ `simulator-gcp/gcs.go:2503::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/rapidCaches` | ✓ `simulator-gcp/gcs.go:2513::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}` | ✓ `simulator-gcp/gcs.go:2555::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}/disable` | ✓ `simulator-gcp/gcs.go:2585::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/iam/testPermissions` | ○ `simulator-gcp/gcs.go:2622::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/lockRetentionPolicy` | ✓ `simulator-gcp/gcs.go:2636::returnBucket` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/restore` | ✓ `simulator-gcp/gcs.go:2637::returnBucket` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/relocate` | ✓ `simulator-gcp/gcs.go:2642::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/operations` | ✓ `simulator-gcp/gcs.go:2685::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/operations/{operationId}` | ✓ `simulator-gcp/gcs.go:2712::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/operations/{operationId}/cancel` | ✓ `simulator-gcp/gcs.go:2725::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/operations/{operationId}/advanceRelocateBucket` | ✓ `simulator-gcp/gcs.go:2734::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/channels/stop` | ○ `simulator-gcp/gcs.go:2747::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/o` | ✓ `simulator-gcp/gcs.go:2754::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b` | ✓ `simulator-gcp/gcs.go:668::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}` | ✓ `simulator-gcp/gcs.go:714::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}` | ✓ `simulator-gcp/gcs.go:756::patchBucket` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}` | ✓ `simulator-gcp/gcs.go:757::patchBucket` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/storageLayout` | ✓ `simulator-gcp/gcs.go:760::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}` | ✓ `simulator-gcp/gcs.go:788::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b` | ✓ `simulator-gcp/gcs.go:807::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/o` | ✓ `simulator-gcp/gcs.go:830::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/o/{object...}` | ✓ `simulator-gcp/gcs.go:923::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/o/{object...}` | ✓ `simulator-gcp/gcs.go:980::patchObject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}/o/{object...}` | ✓ `simulator-gcp/gcs.go:981::patchObject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/o/{object...}` | ✓ `simulator-gcp/gcs.go:984::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
