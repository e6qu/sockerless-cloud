# Sim surface — gcp-bigquery

Surface registered in `simulator-gcp/bigquery.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `GET /bigquery/v2/projects` | ✓ `simulator-gcp/bigquery.go:223::handleBQListProjects` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets` | ✓ `simulator-gcp/bigquery.go:225::handleBQInsertDataset` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets` | ✓ `simulator-gcp/bigquery.go:226::handleBQListDatasets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}` | ✓ `simulator-gcp/bigquery.go:227::handleBQGetDataset` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /bigquery/v2/projects/{project}/datasets/{dataset}` | ✓ `simulator-gcp/bigquery.go:228::handleBQPatchDataset` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /bigquery/v2/projects/{project}/datasets/{dataset}` | ✓ `simulator-gcp/bigquery.go:229::handleBQPatchDataset` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /bigquery/v2/projects/{project}/datasets/{dataset}` | ✓ `simulator-gcp/bigquery.go:230::handleBQDeleteDataset` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{datasetVerb}` | ✓ `simulator-gcp/bigquery.go:233::handleBQDatasetVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/serviceAccount` | ○ `simulator-gcp/bigquery.go:235::handleBQGetServiceAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables` | ✓ `simulator-gcp/bigquery.go:237::handleBQInsertTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables` | ✓ `simulator-gcp/bigquery.go:238::handleBQListTables` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}` | ✓ `simulator-gcp/bigquery.go:239::handleBQGetTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}` | ✓ `simulator-gcp/bigquery.go:240::handleBQPatchTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}` | ✓ `simulator-gcp/bigquery.go:241::handleBQPatchTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}` | ✓ `simulator-gcp/bigquery.go:242::handleBQDeleteTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{tableVerb}` | ✓ `simulator-gcp/bigquery.go:245::handleBQTableVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/insertAll` | ✓ `simulator-gcp/bigquery.go:247::handleBQInsertAll` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/data` | ✓ `simulator-gcp/bigquery.go:248::handleBQTableDataList` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/models` | ✓ `simulator-gcp/bigquery.go:252::handleBQListModels` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/models/{model}` | ✓ `simulator-gcp/bigquery.go:253::handleBQGetModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /bigquery/v2/projects/{project}/datasets/{dataset}/models/{model}` | ✓ `simulator-gcp/bigquery.go:254::handleBQPatchModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/models/{model}` | ✓ `simulator-gcp/bigquery.go:255::handleBQDeleteModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/routines` | ✓ `simulator-gcp/bigquery.go:258::handleBQListRoutines` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/routines` | ✓ `simulator-gcp/bigquery.go:259::handleBQInsertRoutine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routine}` | ✓ `simulator-gcp/bigquery.go:260::handleBQGetRoutine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routine}` | ✓ `simulator-gcp/bigquery.go:261::handleBQUpdateRoutine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routine}` | ✓ `simulator-gcp/bigquery.go:262::handleBQDeleteRoutine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routineVerb}` | ✓ `simulator-gcp/bigquery.go:264::handleBQRoutineVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies` | ✓ `simulator-gcp/bigquery.go:267::handleBQListRAPs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies` | ✓ `simulator-gcp/bigquery.go:268::handleBQInsertRAP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies:batchDelete` | ✓ `simulator-gcp/bigquery.go:269::handleBQBatchDeleteRAP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policy}` | ✓ `simulator-gcp/bigquery.go:270::handleBQGetRAP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policy}` | ✓ `simulator-gcp/bigquery.go:271::handleBQUpdateRAP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policy}` | ✓ `simulator-gcp/bigquery.go:272::handleBQDeleteRAP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policyVerb}` | ✓ `simulator-gcp/bigquery.go:274::handleBQRAPVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/queries` | ✓ `simulator-gcp/bigquery.go:276::handleBQQuery` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/jobs` | ✓ `simulator-gcp/bigquery.go:277::handleBQInsertJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /upload/bigquery/v2/projects/{project}/jobs` | ✓ `simulator-gcp/bigquery.go:280::handleBQInsertJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/jobs` | ✓ `simulator-gcp/bigquery.go:281::handleBQListJobs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/jobs/{job}` | ✓ `simulator-gcp/bigquery.go:282::handleBQGetJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/jobs/{job}/cancel` | ✓ `simulator-gcp/bigquery.go:283::handleBQCancelJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /bigquery/v2/projects/{project}/jobs/{job}/delete` | ✓ `simulator-gcp/bigquery.go:284::handleBQDeleteJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/queries/{job}` | ✓ `simulator-gcp/bigquery.go:285::handleBQGetQueryResults` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
