# Sim surface — gcp-cloudbuild

Surface registered in `simulator-gcp/cloudbuild.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /v1/projects/{project}/builds` | ✓ `simulator-gcp/cloudbuild.go:220::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/triggers` | ✓ `simulator-gcp/cloudbuild.go:261::handleCreateBuildTrigger` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/triggers` | ✓ `simulator-gcp/cloudbuild.go:262::handleListBuildTriggers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/triggers/{trigger}` | ✓ `simulator-gcp/cloudbuild.go:263::handleGetBuildTrigger` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/triggers/{trigger}` | ✓ `simulator-gcp/cloudbuild.go:264::handleUpdateBuildTrigger` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/triggers/{trigger}` | ✓ `simulator-gcp/cloudbuild.go:265::handleDeleteBuildTrigger` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/builds` | ✓ `simulator-gcp/cloudbuild.go:268::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/builds/{id}` | ✓ `simulator-gcp/cloudbuild.go:284::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/builds/{idAction}` | ✓ `simulator-gcp/cloudbuild.go:316::cancelBuild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/builds/{idAction}` | ✓ `simulator-gcp/cloudbuild.go:317::cancelBuild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/operations/build/{project}/{id}` | ✓ `simulator-gcp/cloudbuild.go:321::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/operations/{operation...}` | ✓ `simulator-gcp/cloudbuild.go:349::handleCloudBuildGetOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/builds` | ✓ `simulator-gcp/cloudbuild.go:352::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/builds/{id}` | ✓ `simulator-gcp/cloudbuild.go:366::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/defaultServiceAccount` | ○ `simulator-gcp/cloudbuild.go:377::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/workerPools` | ✓ `simulator-gcp/cloudbuild.go:387::handleCreateWorkerPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/workerPools` | ✓ `simulator-gcp/cloudbuild.go:388::handleListWorkerPools` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/workerPools/{pool}` | ✓ `simulator-gcp/cloudbuild.go:389::handleGetWorkerPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/workerPools/{pool}` | ✓ `simulator-gcp/cloudbuild.go:390::handlePatchWorkerPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/workerPools/{pool}` | ✓ `simulator-gcp/cloudbuild.go:391::handleDeleteWorkerPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/githubEnterpriseConfigs` | ✓ `simulator-gcp/cloudbuild.go:394::handleCreateGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/githubEnterpriseConfigs` | ✓ `simulator-gcp/cloudbuild.go:395::handleListGHEConfigs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:396::handleGetGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:397::handlePatchGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:398::handleDeleteGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs` | ✓ `simulator-gcp/cloudbuild.go:399::handleCreateGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs` | ✓ `simulator-gcp/cloudbuild.go:400::handleListGHEConfigs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:401::handleGetGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:402::handlePatchGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:403::handleDeleteGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/bitbucketServerConfigs` | ✓ `simulator-gcp/cloudbuild.go:406::handleCreateBitbucketConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/bitbucketServerConfigs` | ✓ `simulator-gcp/cloudbuild.go:407::handleListBitbucketConfigs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:408::handleGetBitbucketConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:409::handlePatchBitbucketConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:410::handleDeleteBitbucketConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}/repos` | ✓ `simulator-gcp/cloudbuild.go:411::handleListBitbucketRepos` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
