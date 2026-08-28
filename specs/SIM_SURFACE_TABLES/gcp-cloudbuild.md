# Sim surface — gcp-cloudbuild

Surface registered in `simulator-gcp/cloudbuild.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /v1/projects/{project}/builds` | ✓ `simulator-gcp/cloudbuild.go:200::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/triggers` | ✓ `simulator-gcp/cloudbuild.go:241::handleCreateBuildTrigger` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/triggers` | ✓ `simulator-gcp/cloudbuild.go:242::handleListBuildTriggers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/triggers/{trigger}` | ✓ `simulator-gcp/cloudbuild.go:243::handleGetBuildTrigger` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/triggers/{trigger}` | ✓ `simulator-gcp/cloudbuild.go:244::handleUpdateBuildTrigger` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/triggers/{trigger}` | ✓ `simulator-gcp/cloudbuild.go:245::handleDeleteBuildTrigger` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/builds` | ✓ `simulator-gcp/cloudbuild.go:248::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/builds/{id}` | ✓ `simulator-gcp/cloudbuild.go:264::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/builds/{idAction}` | ✓ `simulator-gcp/cloudbuild.go:296::cancelBuild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/builds/{idAction}` | ✓ `simulator-gcp/cloudbuild.go:297::cancelBuild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/operations/build/{project}/{id}` | ✓ `simulator-gcp/cloudbuild.go:301::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/operations/{operation...}` | ✓ `simulator-gcp/cloudbuild.go:329::handleCloudBuildGetOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/builds` | ✓ `simulator-gcp/cloudbuild.go:332::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/builds/{id}` | ✓ `simulator-gcp/cloudbuild.go:346::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/defaultServiceAccount` | ✓ `simulator-gcp/cloudbuild.go:357::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/workerPools` | ✓ `simulator-gcp/cloudbuild.go:367::handleCreateWorkerPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/workerPools` | ✓ `simulator-gcp/cloudbuild.go:368::handleListWorkerPools` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/workerPools/{pool}` | ✓ `simulator-gcp/cloudbuild.go:369::handleGetWorkerPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/workerPools/{pool}` | ✓ `simulator-gcp/cloudbuild.go:370::handlePatchWorkerPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/workerPools/{pool}` | ✓ `simulator-gcp/cloudbuild.go:371::handleDeleteWorkerPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/githubEnterpriseConfigs` | ✓ `simulator-gcp/cloudbuild.go:374::handleCreateGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/githubEnterpriseConfigs` | ✓ `simulator-gcp/cloudbuild.go:375::handleListGHEConfigs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:376::handleGetGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:377::handlePatchGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:378::handleDeleteGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs` | ✓ `simulator-gcp/cloudbuild.go:379::handleCreateGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs` | ✓ `simulator-gcp/cloudbuild.go:380::handleListGHEConfigs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:381::handleGetGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:382::handlePatchGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:383::handleDeleteGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/bitbucketServerConfigs` | ✓ `simulator-gcp/cloudbuild.go:386::handleCreateBitbucketConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/bitbucketServerConfigs` | ✓ `simulator-gcp/cloudbuild.go:387::handleListBitbucketConfigs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:388::handleGetBitbucketConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:389::handlePatchBitbucketConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:390::handleDeleteBitbucketConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}/repos` | ✓ `simulator-gcp/cloudbuild.go:391::handleListBitbucketRepos` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
