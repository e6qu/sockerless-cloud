# Sim surface — aws-scheduler

Surface registered in `simulator-aws/scheduler.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /schedules/{Name}` | ✓ `simulator-aws/scheduler.go:89::schedulerRecorded` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /schedules/{Name}` | ✓ `simulator-aws/scheduler.go:90::schedulerRecorded` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /schedules/{Name}` | ✓ `simulator-aws/scheduler.go:91::schedulerRecorded` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /schedules/{Name}` | ✓ `simulator-aws/scheduler.go:92::schedulerRecorded` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /schedules` | ✓ `simulator-aws/scheduler.go:93::schedulerRecorded` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /schedule-groups/{Name}` | ✓ `simulator-aws/scheduler.go:95::schedulerRecorded` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /schedule-groups/{Name}` | ✓ `simulator-aws/scheduler.go:96::schedulerRecorded` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /schedule-groups/{Name}` | ✓ `simulator-aws/scheduler.go:97::schedulerRecorded` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /schedule-groups` | ✓ `simulator-aws/scheduler.go:98::schedulerRecorded` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
