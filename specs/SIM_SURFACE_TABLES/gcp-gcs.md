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
| `PUT /upload/storage/v1/b/{bucket}/o` | ✓ `simulator-gcp/gcs.go:1034::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /upload/storage/v1/b/{bucket}/o` | ✓ `simulator-gcp/gcs.go:1044::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /upload/storage/v1/b/{bucket}/o` | ✓ `simulator-gcp/gcs.go:1056::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/o/{destObject...}` | ✓ `simulator-gcp/gcs.go:1235::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action /{bucket}/{object...}` | ✓ `simulator-gcp/gcs.go:1347::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /download/storage/v1/b/{bucket}/o/{object...}` | ✓ `simulator-gcp/gcs.go:1384::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/acl` | ✓ `simulator-gcp/gcs.go:1742::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/acl/{entity}` | ✓ `simulator-gcp/gcs.go:1755::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/acl` | ✓ `simulator-gcp/gcs.go:1783::insert` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}/acl/{entity}` | ✓ `simulator-gcp/gcs.go:1803::update` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/acl/{entity}` | ✓ `simulator-gcp/gcs.go:1804::update` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/acl/{entity}` | ✓ `simulator-gcp/gcs.go:1806::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/defaultObjectAcl` | ✓ `simulator-gcp/gcs.go:1833::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/defaultObjectAcl/{entity}` | ✓ `simulator-gcp/gcs.go:1846::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/defaultObjectAcl` | ✓ `simulator-gcp/gcs.go:1856::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}/defaultObjectAcl/{entity}` | ✓ `simulator-gcp/gcs.go:1893::update` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/defaultObjectAcl/{entity}` | ✓ `simulator-gcp/gcs.go:1894::update` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/defaultObjectAcl/{entity}` | ✓ `simulator-gcp/gcs.go:1896::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/folders` | ✓ `simulator-gcp/gcs.go:1922::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/folders/{folder}` | ✓ `simulator-gcp/gcs.go:1947::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/folders` | ✓ `simulator-gcp/gcs.go:1957::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/folders/{folder}` | ✓ `simulator-gcp/gcs.go:1980::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/folders/{folder}/deleteRecursive` | ✓ `simulator-gcp/gcs.go:1992::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/folders/{sourceFolder}/renameTo/folders/{destinationFolder}` | ✓ `simulator-gcp/gcs.go:2007::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/managedFolders` | ✓ `simulator-gcp/gcs.go:2041::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}` | ✓ `simulator-gcp/gcs.go:2066::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/managedFolders` | ✓ `simulator-gcp/gcs.go:2076::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/managedFolders/{managedFolder}` | ✓ `simulator-gcp/gcs.go:2105::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/managedFolders/{managedFolder}` | ✓ `simulator-gcp/gcs.go:2137::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam` | ✓ `simulator-gcp/gcs.go:2151::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam` | ✓ `simulator-gcp/gcs.go:2167::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/managedFolders/{managedFolder}/iam/testPermissions` | ✓ `simulator-gcp/gcs.go:2200::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/notificationConfigs` | ○ `simulator-gcp/gcs.go:2216::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/notificationConfigs/{notification}` | ✓ `simulator-gcp/gcs.go:2224::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/notificationConfigs` | ✓ `simulator-gcp/gcs.go:2234::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/notificationConfigs/{notification}` | ✓ `simulator-gcp/gcs.go:2272::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/projects/{projectId}/serviceAccount` | ○ `simulator-gcp/gcs.go:2283::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/projects/{projectId}/hmacKeys` | ✓ `simulator-gcp/gcs.go:2291::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/projects/{projectId}/hmacKeys/{accessId}` | ✓ `simulator-gcp/gcs.go:2323::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/projects/{projectId}/hmacKeys` | ✓ `simulator-gcp/gcs.go:2333::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/projects/{projectId}/hmacKeys/{accessId}` | ✓ `simulator-gcp/gcs.go:2364::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/projects/{projectId}/hmacKeys/{accessId}` | ✓ `simulator-gcp/gcs.go:2384::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/anywhereCaches` | ✓ `simulator-gcp/gcs.go:2404::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}` | ✓ `simulator-gcp/gcs.go:2426::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/anywhereCaches` | ✓ `simulator-gcp/gcs.go:2438::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}` | ✓ `simulator-gcp/gcs.go:2467::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/pause` | ✓ `simulator-gcp/gcs.go:2504::stateVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/resume` | ✓ `simulator-gcp/gcs.go:2505::stateVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/anywhereCaches/{anywhereCacheId}/disable` | ✓ `simulator-gcp/gcs.go:2506::stateVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/rapidCaches` | ✓ `simulator-gcp/gcs.go:2537::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}` | ✓ `simulator-gcp/gcs.go:2559::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/rapidCaches` | ✓ `simulator-gcp/gcs.go:2569::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}` | ✓ `simulator-gcp/gcs.go:2611::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}/disable` | ✓ `simulator-gcp/gcs.go:2641::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/iam/testPermissions` | ✓ `simulator-gcp/gcs.go:2678::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/lockRetentionPolicy` | ✓ `simulator-gcp/gcs.go:2696::returnBucket` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/restore` | ✓ `simulator-gcp/gcs.go:2697::returnBucket` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/relocate` | ✓ `simulator-gcp/gcs.go:2702::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/operations` | ✓ `simulator-gcp/gcs.go:2745::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/operations/{operationId}` | ✓ `simulator-gcp/gcs.go:2772::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/operations/{operationId}/cancel` | ✓ `simulator-gcp/gcs.go:2785::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/operations/{operationId}/advanceRelocateBucket` | ✓ `simulator-gcp/gcs.go:2794::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/channels/stop` | ○ `simulator-gcp/gcs.go:2807::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b/{bucket}/o` | ✓ `simulator-gcp/gcs.go:2814::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /storage/v1/b` | ✓ `simulator-gcp/gcs.go:653::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}` | ✓ `simulator-gcp/gcs.go:711::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}` | ✓ `simulator-gcp/gcs.go:753::patchBucket` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}` | ✓ `simulator-gcp/gcs.go:754::patchBucket` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/storageLayout` | ✓ `simulator-gcp/gcs.go:757::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}` | ✓ `simulator-gcp/gcs.go:785::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b` | ✓ `simulator-gcp/gcs.go:806::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/o` | ✓ `simulator-gcp/gcs.go:829::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /storage/v1/b/{bucket}/o/{object...}` | ✓ `simulator-gcp/gcs.go:913::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /storage/v1/b/{bucket}/o/{object...}` | ✓ `simulator-gcp/gcs.go:991::patchObject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /storage/v1/b/{bucket}/o/{object...}` | ✓ `simulator-gcp/gcs.go:992::patchObject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /storage/v1/b/{bucket}/o/{object...}` | ✓ `simulator-gcp/gcs.go:995::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
