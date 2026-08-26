# Sim surface — gcp-logging

Surface registered in `simulator-gcp/logging.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /v2/entries:list` | ✓ `simulator-gcp/logging.go:265::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/entries:write` | ✓ `simulator-gcp/logging.go:279::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/sinks` | ✓ `simulator-gcp/logging.go:289::handleCreateLoggingSink` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/sinks` | ✓ `simulator-gcp/logging.go:290::handleListLoggingSinks` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/sinks/{sink}` | ✓ `simulator-gcp/logging.go:291::handleGetLoggingSink` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v2/projects/{project}/sinks/{sink}` | ✓ `simulator-gcp/logging.go:292::handleUpdateLoggingSink` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/sinks/{sink}` | ✓ `simulator-gcp/logging.go:293::handleUpdateLoggingSink` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/sinks/{sink}` | ✓ `simulator-gcp/logging.go:294::handleDeleteLoggingSink` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/metrics` | ✓ `simulator-gcp/logging.go:296::handleCreateLoggingMetric` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/metrics` | ✓ `simulator-gcp/logging.go:297::handleListLoggingMetrics` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/metrics/{metric}` | ✓ `simulator-gcp/logging.go:298::handleGetLoggingMetric` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /v2/projects/{project}/metrics/{metric}` | ✓ `simulator-gcp/logging.go:299::handleUpdateLoggingMetric` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/metrics/{metric}` | ✓ `simulator-gcp/logging.go:300::handleUpdateLoggingMetric` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/metrics/{metric}` | ✓ `simulator-gcp/logging.go:301::handleDeleteLoggingMetric` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/entries:copy` | ✓ `simulator-gcp/logging_admin.go:142::handleLoggingEntriesCopy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/entries:tail` | ✓ `simulator-gcp/logging_admin.go:143::handleLoggingEntriesTail` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/monitoredResourceDescriptors` | ✓ `simulator-gcp/logging_admin.go:144::handleLoggingListMRD` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
