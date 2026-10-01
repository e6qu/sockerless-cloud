# Sim surface — gcp-artifactregistry

Surface registered in `simulator-gcp/artifactregistry.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}` | ✓ `simulator-gcp/artifactregistry.go:1025::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/rules` | ✓ `simulator-gcp/artifactregistry.go:1039::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}` | ✓ `simulator-gcp/artifactregistry.go:1059::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}` | ✓ `simulator-gcp/artifactregistry.go:1091::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments` | ✓ `simulator-gcp/artifactregistry.go:1105::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}` | ✓ `simulator-gcp/artifactregistry.go:1132::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments` | ✓ `simulator-gcp/artifactregistry.go:1146::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}` | ✓ `simulator-gcp/artifactregistry.go:1169::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create` | ? `simulator-gcp/artifactregistry.go:1215::genericCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create` | ? `simulator-gcp/artifactregistry.go:1216::genericCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create` | ? `simulator-gcp/artifactregistry.go:1217::genericCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create` | ? `simulator-gcp/artifactregistry.go:1218::genericCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create` | ? `simulator-gcp/artifactregistry.go:1219::genericCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/dockerImages/{image}` | ✓ `simulator-gcp/artifactregistry.go:1235::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts` | ✓ `simulator-gcp/artifactregistry.go:1249::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts/{artifact}` | ✓ `simulator-gcp/artifactregistry.go:1255::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages` | ✓ `simulator-gcp/artifactregistry.go:1261::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages/{npmPackage}` | ✓ `simulator-gcp/artifactregistry.go:1267::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages` | ✓ `simulator-gcp/artifactregistry.go:1273::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages/{pythonPackage}` | ✓ `simulator-gcp/artifactregistry.go:1279::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/prewarmedArtifacts` | ✓ `simulator-gcp/artifactregistry.go:1285::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}` | ✓ `simulator-gcp/artifactregistry.go:1297::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/projectSettings` | ✓ `simulator-gcp/artifactregistry.go:1331::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/projectSettings` | ✓ `simulator-gcp/artifactregistry.go:1340::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/vpcscConfig` | ✓ `simulator-gcp/artifactregistry.go:1362::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/vpcscConfig` | ✓ `simulator-gcp/artifactregistry.go:1371::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/projectConfig` | ✓ `simulator-gcp/artifactregistry.go:1390::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/projectConfig` | ✓ `simulator-gcp/artifactregistry.go:1399::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories` | ✓ `simulator-gcp/artifactregistry.go:263::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}` | ✓ `simulator-gcp/artifactregistry.go:309::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repoAction}` | ✓ `simulator-gcp/artifactregistry.go:336::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories` | ✓ `simulator-gcp/artifactregistry.go:355::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}` | ✓ `simulator-gcp/artifactregistry.go:384::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/dockerImages` | ✓ `simulator-gcp/artifactregistry.go:411::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/token` | ✓ `simulator-gcp/artifactregistry.go:447::arTokenServiceHandler` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/token` | ○ `simulator-gcp/artifactregistry.go:448::arTokenServiceMethodNotAllowed` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v2/token` | ○ `simulator-gcp/artifactregistry.go:449::arTokenServiceMethodNotAllowed` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/token` | ○ `simulator-gcp/artifactregistry.go:450::arTokenServiceMethodNotAllowed` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/token` | ○ `simulator-gcp/artifactregistry.go:451::arTokenServiceMethodNotAllowed` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages` | ✓ `simulator-gcp/artifactregistry.go:514::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}` | ✓ `simulator-gcp/artifactregistry.go:541::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}` | ✓ `simulator-gcp/artifactregistry.go:555::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}` | ✓ `simulator-gcp/artifactregistry.go:579::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions` | ✓ `simulator-gcp/artifactregistry.go:609::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}` | ✓ `simulator-gcp/artifactregistry.go:636::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}` | ✓ `simulator-gcp/artifactregistry.go:650::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}` | ✓ `simulator-gcp/artifactregistry.go:677::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions:batchDelete` | ✓ `simulator-gcp/artifactregistry.go:702::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags` | ✓ `simulator-gcp/artifactregistry.go:751::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}` | ✓ `simulator-gcp/artifactregistry.go:778::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags` | ✓ `simulator-gcp/artifactregistry.go:792::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}` | ✓ `simulator-gcp/artifactregistry.go:812::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}` | ✓ `simulator-gcp/artifactregistry.go:835::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files` | ✓ `simulator-gcp/artifactregistry.go:849::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}` | ✓ `simulator-gcp/artifactregistry.go:876::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}` | ✓ `simulator-gcp/artifactregistry.go:899::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}` | ✓ `simulator-gcp/artifactregistry.go:923::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /download/v1/projects/{project}/locations/{location}/repositories/{repo}/files/{fileAction}` | ✓ `simulator-gcp/artifactregistry.go:946::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:990::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:991::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:992::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:993::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:994::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:995::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:996::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules` | ✓ `simulator-gcp/artifactregistry.go:998::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
