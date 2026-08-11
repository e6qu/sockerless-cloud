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
| `POST /v1/projects/{project}/locations/{location}/repositories` | ✓ `simulator-gcp/artifactregistry.go:204::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}` | ✓ `simulator-gcp/artifactregistry.go:250::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repoAction}` | ✓ `simulator-gcp/artifactregistry.go:277::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories` | ✓ `simulator-gcp/artifactregistry.go:293::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}` | ✓ `simulator-gcp/artifactregistry.go:318::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/dockerImages` | ✓ `simulator-gcp/artifactregistry.go:344::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages` | ✓ `simulator-gcp/artifactregistry.go:415::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}` | ✓ `simulator-gcp/artifactregistry.go:438::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}` | ✓ `simulator-gcp/artifactregistry.go:452::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}` | ✓ `simulator-gcp/artifactregistry.go:476::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions` | ✓ `simulator-gcp/artifactregistry.go:501::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}` | ✓ `simulator-gcp/artifactregistry.go:524::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}` | ✓ `simulator-gcp/artifactregistry.go:538::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}` | ✓ `simulator-gcp/artifactregistry.go:565::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions:batchDelete` | ✓ `simulator-gcp/artifactregistry.go:581::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags` | ✓ `simulator-gcp/artifactregistry.go:606::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}` | ✓ `simulator-gcp/artifactregistry.go:629::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags` | ✓ `simulator-gcp/artifactregistry.go:643::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}` | ✓ `simulator-gcp/artifactregistry.go:663::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}` | ✓ `simulator-gcp/artifactregistry.go:686::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files` | ✓ `simulator-gcp/artifactregistry.go:702::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}` | ✓ `simulator-gcp/artifactregistry.go:725::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}` | ✓ `simulator-gcp/artifactregistry.go:751::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}` | ✓ `simulator-gcp/artifactregistry.go:775::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /download/v1/projects/{project}/locations/{location}/repositories/{repo}/files/{fileAction}` | ✓ `simulator-gcp/artifactregistry.go:793::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload` | ✓ `simulator-gcp/artifactregistry.go:812::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules` | ✓ `simulator-gcp/artifactregistry.go:837::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}` | ✓ `simulator-gcp/artifactregistry.go:860::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/rules` | ✓ `simulator-gcp/artifactregistry.go:874::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}` | ✓ `simulator-gcp/artifactregistry.go:894::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}` | ✓ `simulator-gcp/artifactregistry.go:926::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments` | ✓ `simulator-gcp/artifactregistry.go:942::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}` | ✓ `simulator-gcp/artifactregistry.go:965::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments` | ✓ `simulator-gcp/artifactregistry.go:979::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}` | ✓ `simulator-gcp/artifactregistry.go:1002::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/dockerImages/{image}` | ✓ `simulator-gcp/artifactregistry.go:1053::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts` | ✓ `simulator-gcp/artifactregistry.go:1067::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts/{artifact}` | ✓ `simulator-gcp/artifactregistry.go:1073::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages` | ✓ `simulator-gcp/artifactregistry.go:1079::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages/{npmPackage}` | ✓ `simulator-gcp/artifactregistry.go:1085::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages` | ✓ `simulator-gcp/artifactregistry.go:1091::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages/{pythonPackage}` | ✓ `simulator-gcp/artifactregistry.go:1097::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/repositories/{repo}/prewarmedArtifacts` | ✓ `simulator-gcp/artifactregistry.go:1103::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}` | ✓ `simulator-gcp/artifactregistry.go:1112::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/projectSettings` | ✓ `simulator-gcp/artifactregistry.go:1146::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/projectSettings` | ✓ `simulator-gcp/artifactregistry.go:1155::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/vpcscConfig` | ✓ `simulator-gcp/artifactregistry.go:1177::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/vpcscConfig` | ✓ `simulator-gcp/artifactregistry.go:1186::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/projectConfig` | ✓ `simulator-gcp/artifactregistry.go:1205::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/projectConfig` | ✓ `simulator-gcp/artifactregistry.go:1214::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
