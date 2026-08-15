# Sim surface — gcp-artifactregistry

Surface registered in `simulator-gcp/artifactregistry.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /v1/projects/{project}/locations/{location}/repositories` | ✓ `simulator-gcp/artifactregistry.go:205::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}` | ✓ `simulator-gcp/artifactregistry.go:251::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repoAction}` | ✓ `simulator-gcp/artifactregistry.go:278::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories` | ✓ `simulator-gcp/artifactregistry.go:294::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}` | ✓ `simulator-gcp/artifactregistry.go:319::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/dockerImages` | ✓ `simulator-gcp/artifactregistry.go:345::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/token` | ✓ `simulator-gcp/artifactregistry.go:381::arTokenServiceHandler` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/token` | ✓ `simulator-gcp/artifactregistry.go:382::arTokenServiceMethodNotAllowed` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v2/token` | ✓ `simulator-gcp/artifactregistry.go:383::arTokenServiceMethodNotAllowed` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/token` | ✓ `simulator-gcp/artifactregistry.go:384::arTokenServiceMethodNotAllowed` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/token` | ✓ `simulator-gcp/artifactregistry.go:385::arTokenServiceMethodNotAllowed` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages` | ✓ `simulator-gcp/artifactregistry.go:426::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}` | ✓ `simulator-gcp/artifactregistry.go:449::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}` | ✓ `simulator-gcp/artifactregistry.go:463::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}` | ✓ `simulator-gcp/artifactregistry.go:487::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions` | ✓ `simulator-gcp/artifactregistry.go:512::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}` | ✓ `simulator-gcp/artifactregistry.go:535::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}` | ✓ `simulator-gcp/artifactregistry.go:549::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}` | ✓ `simulator-gcp/artifactregistry.go:576::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions:batchDelete` | ✓ `simulator-gcp/artifactregistry.go:592::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags` | ✓ `simulator-gcp/artifactregistry.go:617::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}` | ✓ `simulator-gcp/artifactregistry.go:640::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags` | ✓ `simulator-gcp/artifactregistry.go:654::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}` | ✓ `simulator-gcp/artifactregistry.go:674::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}` | ✓ `simulator-gcp/artifactregistry.go:697::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files` | ✓ `simulator-gcp/artifactregistry.go:713::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}` | ✓ `simulator-gcp/artifactregistry.go:736::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}` | ✓ `simulator-gcp/artifactregistry.go:762::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}` | ✓ `simulator-gcp/artifactregistry.go:786::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /download/v1/projects/{project}/locations/{location}/repositories/{repo}/files/{fileAction}` | ✓ `simulator-gcp/artifactregistry.go:804::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:823::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules` | ✓ `simulator-gcp/artifactregistry.go:848::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}` | ✓ `simulator-gcp/artifactregistry.go:871::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/rules` | ✓ `simulator-gcp/artifactregistry.go:885::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}` | ✓ `simulator-gcp/artifactregistry.go:905::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}` | ✓ `simulator-gcp/artifactregistry.go:937::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments` | ✓ `simulator-gcp/artifactregistry.go:953::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}` | ✓ `simulator-gcp/artifactregistry.go:976::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments` | ✓ `simulator-gcp/artifactregistry.go:990::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}` | ✓ `simulator-gcp/artifactregistry.go:1013::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/dockerImages/{image}` | ✓ `simulator-gcp/artifactregistry.go:1064::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts` | ✓ `simulator-gcp/artifactregistry.go:1078::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts/{artifact}` | ✓ `simulator-gcp/artifactregistry.go:1084::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages` | ✓ `simulator-gcp/artifactregistry.go:1090::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages/{npmPackage}` | ✓ `simulator-gcp/artifactregistry.go:1096::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages` | ✓ `simulator-gcp/artifactregistry.go:1102::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages/{pythonPackage}` | ✓ `simulator-gcp/artifactregistry.go:1108::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/prewarmedArtifacts` | ✓ `simulator-gcp/artifactregistry.go:1114::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}` | ✓ `simulator-gcp/artifactregistry.go:1123::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/projectSettings` | ✓ `simulator-gcp/artifactregistry.go:1157::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/projectSettings` | ✓ `simulator-gcp/artifactregistry.go:1166::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/vpcscConfig` | ✓ `simulator-gcp/artifactregistry.go:1188::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/vpcscConfig` | ✓ `simulator-gcp/artifactregistry.go:1197::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/projectConfig` | ✓ `simulator-gcp/artifactregistry.go:1216::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/projectConfig` | ✓ `simulator-gcp/artifactregistry.go:1225::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
