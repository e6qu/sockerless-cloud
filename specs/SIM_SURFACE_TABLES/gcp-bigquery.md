# Sim surface — gcp-bigquery

Surface registered in `simulator-gcp/bigquery.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `GET /bigquery/v2/projects` | ✓ `simulator-gcp/bigquery.go:222::handleBQListProjects` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets` | ✓ `simulator-gcp/bigquery.go:224::handleBQInsertDataset` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets` | ✓ `simulator-gcp/bigquery.go:225::handleBQListDatasets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}` | ✓ `simulator-gcp/bigquery.go:226::handleBQGetDataset` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /bigquery/v2/projects/{project}/datasets/{dataset}` | ✓ `simulator-gcp/bigquery.go:227::handleBQPatchDataset` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /bigquery/v2/projects/{project}/datasets/{dataset}` | ✓ `simulator-gcp/bigquery.go:228::handleBQPatchDataset` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /bigquery/v2/projects/{project}/datasets/{dataset}` | ✓ `simulator-gcp/bigquery.go:229::handleBQDeleteDataset` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{datasetVerb}` | ✓ `simulator-gcp/bigquery.go:232::handleBQDatasetVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/serviceAccount` | ✓ `simulator-gcp/bigquery.go:234::handleBQGetServiceAccount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables` | ✓ `simulator-gcp/bigquery.go:236::handleBQInsertTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables` | ✓ `simulator-gcp/bigquery.go:237::handleBQListTables` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}` | ✓ `simulator-gcp/bigquery.go:238::handleBQGetTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}` | ✓ `simulator-gcp/bigquery.go:239::handleBQPatchTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}` | ✓ `simulator-gcp/bigquery.go:240::handleBQPatchTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}` | ✓ `simulator-gcp/bigquery.go:241::handleBQDeleteTable` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{tableVerb}` | ✓ `simulator-gcp/bigquery.go:244::handleBQTableVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/insertAll` | ✓ `simulator-gcp/bigquery.go:246::handleBQInsertAll` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/data` | ✓ `simulator-gcp/bigquery.go:247::handleBQTableDataList` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/models` | ✓ `simulator-gcp/bigquery.go:251::handleBQListModels` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/models/{model}` | ✓ `simulator-gcp/bigquery.go:252::handleBQGetModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /bigquery/v2/projects/{project}/datasets/{dataset}/models/{model}` | ✓ `simulator-gcp/bigquery.go:253::handleBQPatchModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/models/{model}` | ✓ `simulator-gcp/bigquery.go:254::handleBQDeleteModel` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/routines` | ✓ `simulator-gcp/bigquery.go:257::handleBQListRoutines` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/routines` | ✓ `simulator-gcp/bigquery.go:258::handleBQInsertRoutine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routine}` | ✓ `simulator-gcp/bigquery.go:259::handleBQGetRoutine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routine}` | ✓ `simulator-gcp/bigquery.go:260::handleBQUpdateRoutine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routine}` | ✓ `simulator-gcp/bigquery.go:261::handleBQDeleteRoutine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routineVerb}` | ✓ `simulator-gcp/bigquery.go:263::handleBQRoutineVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies` | ✓ `simulator-gcp/bigquery.go:266::handleBQListRAPs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies` | ✓ `simulator-gcp/bigquery.go:267::handleBQInsertRAP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies:batchDelete` | ✓ `simulator-gcp/bigquery.go:268::handleBQBatchDeleteRAP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policy}` | ✓ `simulator-gcp/bigquery.go:269::handleBQGetRAP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policy}` | ✓ `simulator-gcp/bigquery.go:270::handleBQUpdateRAP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policy}` | ✓ `simulator-gcp/bigquery.go:271::handleBQDeleteRAP` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policyVerb}` | ✓ `simulator-gcp/bigquery.go:273::handleBQRAPVerb` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/queries` | ✓ `simulator-gcp/bigquery.go:275::handleBQQuery` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/jobs` | ✓ `simulator-gcp/bigquery.go:276::handleBQInsertJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/jobs` | ✓ `simulator-gcp/bigquery.go:277::handleBQListJobs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/jobs/{job}` | ✓ `simulator-gcp/bigquery.go:278::handleBQGetJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /bigquery/v2/projects/{project}/jobs/{job}/cancel` | ✓ `simulator-gcp/bigquery.go:279::handleBQCancelJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /bigquery/v2/projects/{project}/jobs/{job}/delete` | ✓ `simulator-gcp/bigquery.go:280::handleBQDeleteJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /bigquery/v2/projects/{project}/queries/{job}` | ✓ `simulator-gcp/bigquery.go:281::handleBQGetQueryResults` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
