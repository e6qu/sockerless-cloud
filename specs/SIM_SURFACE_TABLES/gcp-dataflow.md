# Sim surface — gcp-dataflow

Surface registered in `simulator-gcp/dataflow.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /v1b3/projects/{project}/locations/{location}/templates` | ✓ `simulator-gcp/dataflow.go:101::handleDataflowCreateJobFromTemplate` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/locations/{location}/templates:get` | ✓ `simulator-gcp/dataflow.go:102::handleDataflowGetTemplate` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/locations/{location}/templates:launch` | ✓ `simulator-gcp/dataflow.go:103::handleDataflowLaunchTemplate` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/locations/{location}/flexTemplates:launch` | ✓ `simulator-gcp/dataflow.go:104::handleDataflowLaunchFlexTemplate` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/snapshots` | ✓ `simulator-gcp/dataflow.go:106::handleDataflowListSnapshotsGlobal` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `DELETE /v1b3/projects/{project}/snapshots` | ✓ `simulator-gcp/dataflow.go:107::handleDataflowDeleteSnapshotsGlobal` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/snapshots/{snapshot}` | ✓ `simulator-gcp/dataflow.go:108::handleDataflowGetSnapshotGlobal` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/locations/{location}/snapshots` | ✓ `simulator-gcp/dataflow.go:110::handleDataflowListSnapshots` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/locations/{location}/snapshots/{snapshot}` | ✓ `simulator-gcp/dataflow.go:111::handleDataflowGetSnapshot` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `DELETE /v1b3/projects/{project}/locations/{location}/snapshots/{snapshot}` | ✓ `simulator-gcp/dataflow.go:112::handleDataflowDeleteSnapshot` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/WorkerMessages` | ○ `simulator-gcp/dataflow.go:114::handleDataflowWorkerMessages` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/locations/{location}/WorkerMessages` | ○ `simulator-gcp/dataflow.go:115::handleDataflowWorkerMessages` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/jobs` | ✓ `simulator-gcp/dataflow.go:68::handleDataflowCreateJobGlobal` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/jobs` | ✓ `simulator-gcp/dataflow.go:69::handleDataflowListJobsGlobal` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/jobs:aggregated` | ✓ `simulator-gcp/dataflow.go:70::handleDataflowAggregatedJobs` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/jobs/{jobAction}` | ✓ `simulator-gcp/dataflow.go:71::handleDataflowGetJobGlobal` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `PUT /v1b3/projects/{project}/jobs/{job}` | ✓ `simulator-gcp/dataflow.go:72::handleDataflowUpdateJobGlobal` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/jobs/{jobAction}` | ✓ `simulator-gcp/dataflow.go:73::handleDataflowJobPostActionGlobal` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/jobs/{job}/metrics` | ✓ `simulator-gcp/dataflow.go:74::handleDataflowGetMetricsGlobal` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/jobs/{job}/messages` | ✓ `simulator-gcp/dataflow.go:75::handleDataflowListMessagesGlobal` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/jobs/{job}/debug/getConfig` | ○ `simulator-gcp/dataflow.go:76::handleDataflowGetDebugConfig` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/jobs/{job}/debug/sendCapture` | ○ `simulator-gcp/dataflow.go:77::handleDataflowSendDebugCapture` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/jobs/{job}/workItems:lease` | ○ `simulator-gcp/dataflow.go:78::handleDataflowLeaseWorkItems` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/jobs/{job}/workItems:reportStatus` | ○ `simulator-gcp/dataflow.go:79::handleDataflowReportWorkItemStatus` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/locations/{location}/jobs` | ✓ `simulator-gcp/dataflow.go:81::handleDataflowCreateJob` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/locations/{location}/jobs` | ✓ `simulator-gcp/dataflow.go:82::handleDataflowListJobs` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/dataflow.go:83::handleDataflowGetJob` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `PUT /v1b3/projects/{project}/locations/{location}/jobs/{job}` | ✓ `simulator-gcp/dataflow.go:84::handleDataflowUpdateJob` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/locations/{location}/jobs/{jobAction}` | ✓ `simulator-gcp/dataflow.go:85::handleDataflowJobPostAction` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/locations/{location}/jobs/{job}/metrics` | ✓ `simulator-gcp/dataflow.go:86::handleDataflowGetMetrics` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/locations/{location}/jobs/{job}/messages` | ✓ `simulator-gcp/dataflow.go:87::handleDataflowListMessages` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/locations/{location}/jobs/{job}/executionDetails` | ✓ `simulator-gcp/dataflow.go:88::handleDataflowGetExecutionDetails` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/locations/{location}/jobs/{job}/snapshots` | ✓ `simulator-gcp/dataflow.go:89::handleDataflowListJobSnapshots` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/locations/{location}/jobs/{job}/stages/{stage}/executionDetails` | ✓ `simulator-gcp/dataflow.go:90::handleDataflowGetStageExecutionDetails` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/locations/{location}/jobs/{job}/debug/getConfig` | ○ `simulator-gcp/dataflow.go:91::handleDataflowGetDebugConfig` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/locations/{location}/jobs/{job}/debug/sendCapture` | ○ `simulator-gcp/dataflow.go:92::handleDataflowSendDebugCapture` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/locations/{location}/jobs/{job}/debug/getWorkerStacktraces` | ○ `simulator-gcp/dataflow.go:93::handleDataflowGetWorkerStacktraces` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/locations/{location}/jobs/{job}/workItems:lease` | ○ `simulator-gcp/dataflow.go:94::handleDataflowLeaseWorkItems` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/locations/{location}/jobs/{job}/workItems:reportStatus` | ○ `simulator-gcp/dataflow.go:95::handleDataflowReportWorkItemStatus` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/templates` | ✓ `simulator-gcp/dataflow.go:97::handleDataflowCreateJobFromTemplateGlobal` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `GET /v1b3/projects/{project}/templates:get` | ✓ `simulator-gcp/dataflow.go:98::handleDataflowGetTemplate` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `POST /v1b3/projects/{project}/templates:launch` | ✓ `simulator-gcp/dataflow.go:99::handleDataflowLaunchTemplateGlobal` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
