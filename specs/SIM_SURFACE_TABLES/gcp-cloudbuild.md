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
| `POST /v1/projects/{project}/builds` | ✓ `simulator-gcp/cloudbuild.go:227::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/triggers` | ✓ `simulator-gcp/cloudbuild.go:248::handleCreateBuildTrigger` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/triggers` | ✓ `simulator-gcp/cloudbuild.go:249::handleListBuildTriggers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/triggers/{trigger}` | ✓ `simulator-gcp/cloudbuild.go:250::handleGetBuildTrigger` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/triggers/{trigger}` | ✓ `simulator-gcp/cloudbuild.go:251::handleUpdateBuildTrigger` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/triggers/{trigger}` | ✓ `simulator-gcp/cloudbuild.go:252::handleDeleteBuildTrigger` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/builds` | ✓ `simulator-gcp/cloudbuild.go:255::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/builds/{id}` | ✓ `simulator-gcp/cloudbuild.go:271::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/builds/{idAction}` | ✓ `simulator-gcp/cloudbuild.go:303::cancelBuild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/builds/{idAction}` | ✓ `simulator-gcp/cloudbuild.go:304::cancelBuild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/operations/build/{project}/{id}` | ✓ `simulator-gcp/cloudbuild.go:308::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/operations/{operation...}` | ✓ `simulator-gcp/cloudbuild.go:322::handleCloudBuildGetOperation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/builds` | ✓ `simulator-gcp/cloudbuild.go:325::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/builds/{id}` | ✓ `simulator-gcp/cloudbuild.go:339::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/defaultServiceAccount` | ✓ `simulator-gcp/cloudbuild.go:350::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/workerPools` | ✓ `simulator-gcp/cloudbuild.go:365::handleCreateWorkerPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/workerPools` | ✓ `simulator-gcp/cloudbuild.go:366::handleListWorkerPools` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/workerPools/{pool}` | ✓ `simulator-gcp/cloudbuild.go:367::handleGetWorkerPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/workerPools/{pool}` | ✓ `simulator-gcp/cloudbuild.go:368::handlePatchWorkerPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/workerPools/{pool}` | ✓ `simulator-gcp/cloudbuild.go:369::handleDeleteWorkerPool` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/githubEnterpriseConfigs` | ✓ `simulator-gcp/cloudbuild.go:372::handleCreateGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/githubEnterpriseConfigs` | ✓ `simulator-gcp/cloudbuild.go:373::handleListGHEConfigs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:374::handleGetGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:375::handlePatchGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:376::handleDeleteGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs` | ✓ `simulator-gcp/cloudbuild.go:377::handleCreateGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs` | ✓ `simulator-gcp/cloudbuild.go:378::handleListGHEConfigs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:379::handleGetGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:380::handlePatchGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:381::handleDeleteGHEConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/projects/{project}/locations/{location}/bitbucketServerConfigs` | ✓ `simulator-gcp/cloudbuild.go:384::handleCreateBitbucketConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/bitbucketServerConfigs` | ✓ `simulator-gcp/cloudbuild.go:385::handleListBitbucketConfigs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:386::handleGetBitbucketConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:387::handlePatchBitbucketConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}` | ✓ `simulator-gcp/cloudbuild.go:388::handleDeleteBitbucketConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}/repos` | ✓ `simulator-gcp/cloudbuild.go:389::handleListBitbucketRepos` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
