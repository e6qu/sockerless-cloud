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
| `POST /v1/projects/{project}/locations/{location}/repositories` | ✓ `simulator-gcp/artifactregistry.go:251::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}` | ✓ `simulator-gcp/artifactregistry.go:296::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repoAction}` | ✓ `simulator-gcp/artifactregistry.go:321::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories` | ✓ `simulator-gcp/artifactregistry.go:339::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}` | ✓ `simulator-gcp/artifactregistry.go:367::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/dockerImages` | ✓ `simulator-gcp/artifactregistry.go:392::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/token` | ✓ `simulator-gcp/artifactregistry.go:423::arTokenServiceHandler` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/token` | ○ `simulator-gcp/artifactregistry.go:424::arTokenServiceMethodNotAllowed` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v2/token` | ○ `simulator-gcp/artifactregistry.go:425::arTokenServiceMethodNotAllowed` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/token` | ○ `simulator-gcp/artifactregistry.go:426::arTokenServiceMethodNotAllowed` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/token` | ○ `simulator-gcp/artifactregistry.go:427::arTokenServiceMethodNotAllowed` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/dockerImages/{image}` | ✓ `simulator-gcp/artifactregistry.go:472::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts` | ✓ `simulator-gcp/artifactregistry.go:486::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts/{artifact}` | ✓ `simulator-gcp/artifactregistry.go:492::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages` | ✓ `simulator-gcp/artifactregistry.go:498::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages/{npmPackage}` | ✓ `simulator-gcp/artifactregistry.go:504::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages` | ✓ `simulator-gcp/artifactregistry.go:510::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages/{pythonPackage}` | ✓ `simulator-gcp/artifactregistry.go:516::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/prewarmedArtifacts` | ✓ `simulator-gcp/artifactregistry.go:522::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}` | ✓ `simulator-gcp/artifactregistry.go:534::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/projectSettings` | ✓ `simulator-gcp/artifactregistry.go:566::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/projectSettings` | ✓ `simulator-gcp/artifactregistry.go:575::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/vpcscConfig` | ✓ `simulator-gcp/artifactregistry.go:597::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/vpcscConfig` | ✓ `simulator-gcp/artifactregistry.go:606::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/projectConfig` | ✓ `simulator-gcp/artifactregistry.go:625::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/projectConfig` | ✓ `simulator-gcp/artifactregistry.go:634::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files` | ✓ `simulator-gcp/artifactregistry_files.go:11::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry_files.go:135::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry_files.go:137::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry_files.go:139::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry_files.go:141::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry_files.go:143::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry_files.go:145::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry_files.go:147::uploadFile` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}` | ✓ `simulator-gcp/artifactregistry_files.go:20::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}` | ✓ `simulator-gcp/artifactregistry_files.go:43::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}` | ✓ `simulator-gcp/artifactregistry_files.go:67::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /download/v1/projects/{project}/locations/{location}/repositories/{repo}/files/{fileAction}` | ✓ `simulator-gcp/artifactregistry_files.go:90::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create` | ? `simulator-gcp/artifactregistry_media.go:43::genericCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create` | ? `simulator-gcp/artifactregistry_media.go:45::genericCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create` | ? `simulator-gcp/artifactregistry_media.go:47::genericCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create` | ? `simulator-gcp/artifactregistry_media.go:49::genericCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create` | ? `simulator-gcp/artifactregistry_media.go:51::genericCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}` | ✓ `simulator-gcp/artifactregistry_packages.go:112::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages` | ✓ `simulator-gcp/artifactregistry_packages.go:12::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}` | ✓ `simulator-gcp/artifactregistry_packages.go:139::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions:batchDelete` | ✓ `simulator-gcp/artifactregistry_packages.go:164::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}` | ✓ `simulator-gcp/artifactregistry_packages.go:21::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags` | ✓ `simulator-gcp/artifactregistry_packages.go:213::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}` | ✓ `simulator-gcp/artifactregistry_packages.go:222::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags` | ✓ `simulator-gcp/artifactregistry_packages.go:236::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}` | ✓ `simulator-gcp/artifactregistry_packages.go:256::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}` | ✓ `simulator-gcp/artifactregistry_packages.go:279::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}` | ✓ `simulator-gcp/artifactregistry_packages.go:35::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}` | ✓ `simulator-gcp/artifactregistry_packages.go:59::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions` | ✓ `simulator-gcp/artifactregistry_packages.go:89::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}` | ✓ `simulator-gcp/artifactregistry_packages.go:98::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules` | ✓ `simulator-gcp/artifactregistry_rules.go:10::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}` | ✓ `simulator-gcp/artifactregistry_rules.go:108::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments` | ✓ `simulator-gcp/artifactregistry_rules.go:122::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}` | ✓ `simulator-gcp/artifactregistry_rules.go:145::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}` | ✓ `simulator-gcp/artifactregistry_rules.go:19::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/rules` | ✓ `simulator-gcp/artifactregistry_rules.go:33::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}` | ✓ `simulator-gcp/artifactregistry_rules.go:53::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}` | ✓ `simulator-gcp/artifactregistry_rules.go:85::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments` | ✓ `simulator-gcp/artifactregistry_rules.go:99::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
