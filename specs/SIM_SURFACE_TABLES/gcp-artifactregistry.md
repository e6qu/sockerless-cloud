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
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}` | ✓ `simulator-gcp/artifactregistry.go:1024::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/rules` | ✓ `simulator-gcp/artifactregistry.go:1038::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}` | ✓ `simulator-gcp/artifactregistry.go:1058::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}` | ✓ `simulator-gcp/artifactregistry.go:1090::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments` | ✓ `simulator-gcp/artifactregistry.go:1104::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}` | ✓ `simulator-gcp/artifactregistry.go:1131::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments` | ✓ `simulator-gcp/artifactregistry.go:1145::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}` | ✓ `simulator-gcp/artifactregistry.go:1168::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create` | ? `simulator-gcp/artifactregistry.go:1214::genericCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create` | ? `simulator-gcp/artifactregistry.go:1215::genericCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create` | ? `simulator-gcp/artifactregistry.go:1216::genericCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create` | ? `simulator-gcp/artifactregistry.go:1217::genericCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create` | ? `simulator-gcp/artifactregistry.go:1218::genericCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/dockerImages/{image}` | ✓ `simulator-gcp/artifactregistry.go:1234::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts` | ✓ `simulator-gcp/artifactregistry.go:1248::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts/{artifact}` | ✓ `simulator-gcp/artifactregistry.go:1254::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages` | ✓ `simulator-gcp/artifactregistry.go:1260::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages/{npmPackage}` | ✓ `simulator-gcp/artifactregistry.go:1266::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages` | ✓ `simulator-gcp/artifactregistry.go:1272::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages/{pythonPackage}` | ✓ `simulator-gcp/artifactregistry.go:1278::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/prewarmedArtifacts` | ✓ `simulator-gcp/artifactregistry.go:1284::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}` | ✓ `simulator-gcp/artifactregistry.go:1296::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/projectSettings` | ✓ `simulator-gcp/artifactregistry.go:1330::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/projectSettings` | ✓ `simulator-gcp/artifactregistry.go:1339::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/vpcscConfig` | ✓ `simulator-gcp/artifactregistry.go:1361::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/vpcscConfig` | ✓ `simulator-gcp/artifactregistry.go:1370::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/projectConfig` | ✓ `simulator-gcp/artifactregistry.go:1389::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/projectConfig` | ✓ `simulator-gcp/artifactregistry.go:1398::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
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
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages` | ✓ `simulator-gcp/artifactregistry.go:513::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}` | ✓ `simulator-gcp/artifactregistry.go:540::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}` | ✓ `simulator-gcp/artifactregistry.go:554::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}` | ✓ `simulator-gcp/artifactregistry.go:578::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions` | ✓ `simulator-gcp/artifactregistry.go:608::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}` | ✓ `simulator-gcp/artifactregistry.go:635::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}` | ✓ `simulator-gcp/artifactregistry.go:649::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}` | ✓ `simulator-gcp/artifactregistry.go:676::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions:batchDelete` | ✓ `simulator-gcp/artifactregistry.go:701::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags` | ✓ `simulator-gcp/artifactregistry.go:750::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}` | ✓ `simulator-gcp/artifactregistry.go:777::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags` | ✓ `simulator-gcp/artifactregistry.go:791::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}` | ✓ `simulator-gcp/artifactregistry.go:811::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}` | ✓ `simulator-gcp/artifactregistry.go:834::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files` | ✓ `simulator-gcp/artifactregistry.go:848::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}` | ✓ `simulator-gcp/artifactregistry.go:875::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}` | ✓ `simulator-gcp/artifactregistry.go:898::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}` | ✓ `simulator-gcp/artifactregistry.go:922::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /download/v1/projects/{project}/locations/{location}/repositories/{repo}/files/{fileAction}` | ✓ `simulator-gcp/artifactregistry.go:945::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:989::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:990::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:991::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:992::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:993::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:994::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:995::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules` | ✓ `simulator-gcp/artifactregistry.go:997::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
