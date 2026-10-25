# Sim surface — gcp-cloudtasks

Surface registered in `simulator-gcp/cloudtasks.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /v2/projects/{project}/locations/{location}/queues` | ✓ `simulator-gcp/cloudtasks.go:206::handleCTCreateQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/queues` | ✓ `simulator-gcp/cloudtasks.go:207::handleCTListQueues` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/queues/{queue}` | ✓ `simulator-gcp/cloudtasks.go:208::handleCTGetQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/queues/{queue}` | ✓ `simulator-gcp/cloudtasks.go:209::handleCTPatchQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/queues/{queue}` | ✓ `simulator-gcp/cloudtasks.go:210::handleCTDeleteQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/queues/{queueAction}` | ✓ `simulator-gcp/cloudtasks.go:211::handleCTQueueAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks` | ✓ `simulator-gcp/cloudtasks.go:213::handleCTCreateTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/queues/{queue}/tasks` | ✓ `simulator-gcp/cloudtasks.go:214::handleCTListTasks` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks:batchCreate` | ✓ `simulator-gcp/cloudtasks.go:215::handleCTBatchCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks:batchDelete` | ✓ `simulator-gcp/cloudtasks.go:216::handleCTBatchDelete` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks:buffer` | 501 `simulator-gcp/cloudtasks.go:217::handleCTBuffer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/queues/{queue}/tasks/{task}` | ✓ `simulator-gcp/cloudtasks.go:218::handleCTGetTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v2/projects/{project}/locations/{location}/queues/{queue}/tasks/{task}` | ✓ `simulator-gcp/cloudtasks.go:219::handleCTDeleteTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks/{taskAction}` | ✓ `simulator-gcp/cloudtasks.go:220::handleCTTaskAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v2/projects/{project}/locations/{location}/cmekConfig` | 501 `simulator-gcp/cloudtasks.go:225::cmek` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /v2/projects/{project}/locations/{location}/cmekConfig` | 501 `simulator-gcp/cloudtasks.go:226::cmek` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
