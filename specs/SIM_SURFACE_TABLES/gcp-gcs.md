# Sim surface — gcp-gcs

Surface registered in `simulator-gcp/gcs.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /storage/v1/b` | ✓ `simulator-gcp/gcs.go:668::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}` | ✓ `simulator-gcp/gcs.go:714::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}` | ? `simulator-gcp/gcs.go:756::patchBucket` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}` | ? `simulator-gcp/gcs.go:757::patchBucket` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/storageLayout` | ✓ `simulator-gcp/gcs.go:760::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}` | ✓ `simulator-gcp/gcs.go:788::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b` | ✓ `simulator-gcp/gcs.go:807::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/o` | ✓ `simulator-gcp/gcs.go:830::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/o/{object...}` | ✓ `simulator-gcp/gcs.go:923::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/o/{object...}` | ? `simulator-gcp/gcs.go:980::patchObject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}/o/{object...}` | ? `simulator-gcp/gcs.go:981::patchObject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/o/{object...}` | ✓ `simulator-gcp/gcs.go:984::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /upload/storage/v1/b/{bucket}/o` | ✓ `simulator-gcp/gcs.go:1012::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /upload/storage/v1/b/{bucket}/o` | ✓ `simulator-gcp/gcs.go:1024::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/o/{destObject...}` | ✓ `simulator-gcp/gcs.go:1195::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /download/storage/v1/b/{bucket}/o/{object...}` | ✓ `simulator-gcp/gcs.go:1325::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/acl` | ✓ `simulator-gcp/gcs.go:1701::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/acl/{entity}` | ✓ `simulator-gcp/gcs.go:1714::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/acl` | ? `simulator-gcp/gcs.go:1742::insert` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}/acl/{entity}` | ? `simulator-gcp/gcs.go:1762::update` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/acl/{entity}` | ? `simulator-gcp/gcs.go:1763::update` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/acl/{entity}` | ✓ `simulator-gcp/gcs.go:1765::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/defaultObjectAcl` | ✓ `simulator-gcp/gcs.go:1792::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/defaultObjectAcl/{entity}` | ✓ `simulator-gcp/gcs.go:1805::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/defaultObjectAcl` | ✓ `simulator-gcp/gcs.go:1815::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}/defaultObjectAcl/{entity}` | ? `simulator-gcp/gcs.go:1852::update` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/defaultObjectAcl/{entity}` | ? `simulator-gcp/gcs.go:1853::update` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
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
| `PUT /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam` | ✓ `simulator-gcp/gcs.go:2121::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam/testPermissions` | ○ `simulator-gcp/gcs.go:2137::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/notificationConfigs` | ✓ `simulator-gcp/gcs.go:2145::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/notificationConfigs/{notification}` | ✓ `simulator-gcp/gcs.go:2158::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/notificationConfigs` | ✓ `simulator-gcp/gcs.go:2168::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/notificationConfigs/{notification}` | ✓ `simulator-gcp/gcs.go:2194::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/projects/{projectId}/serviceAccount` | ○ `simulator-gcp/gcs.go:2205::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/projects/{projectId}/hmacKeys` | ✓ `simulator-gcp/gcs.go:2213::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/projects/{projectId}/hmacKeys/{accessId}` | ✓ `simulator-gcp/gcs.go:2245::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/projects/{projectId}/hmacKeys` | ✓ `simulator-gcp/gcs.go:2255::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/projects/{projectId}/hmacKeys/{accessId}` | ✓ `simulator-gcp/gcs.go:2286::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/projects/{projectId}/hmacKeys/{accessId}` | ✓ `simulator-gcp/gcs.go:2306::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/anywhereCaches` | ✓ `simulator-gcp/gcs.go:2326::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}` | ✓ `simulator-gcp/gcs.go:2348::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/anywhereCaches` | ✓ `simulator-gcp/gcs.go:2360::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}` | ✓ `simulator-gcp/gcs.go:2389::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/pause` | ? `simulator-gcp/gcs.go:2426::stateVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/resume` | ? `simulator-gcp/gcs.go:2427::stateVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/disable` | ? `simulator-gcp/gcs.go:2428::stateVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/rapidCaches` | ✓ `simulator-gcp/gcs.go:2459::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}` | ✓ `simulator-gcp/gcs.go:2481::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/rapidCaches` | ✓ `simulator-gcp/gcs.go:2491::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}` | ✓ `simulator-gcp/gcs.go:2533::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}/disable` | ✓ `simulator-gcp/gcs.go:2563::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/iam/testPermissions` | ○ `simulator-gcp/gcs.go:2600::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/lockRetentionPolicy` | ? `simulator-gcp/gcs.go:2614::returnBucket` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/restore` | ? `simulator-gcp/gcs.go:2615::returnBucket` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/relocate` | ✓ `simulator-gcp/gcs.go:2620::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/operations` | ✓ `simulator-gcp/gcs.go:2663::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/operations/{operationId}` | ✓ `simulator-gcp/gcs.go:2690::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/operations/{operationId}/cancel` | ✓ `simulator-gcp/gcs.go:2703::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/operations/{operationId}/advanceRelocateBucket` | ✓ `simulator-gcp/gcs.go:2712::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/channels/stop` | ○ `simulator-gcp/gcs.go:2725::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/o` | ✓ `simulator-gcp/gcs.go:2732::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
