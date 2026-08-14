# Sim surface — azure-resourcegroups

Surface registered in `simulator-azure/resourcegroups.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}` | ✓ `simulator-azure/resourcegroups.go:32::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}` | ✓ `simulator-azure/resourcegroups.go:67::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/resourceGroups` | ✓ `simulator-azure/resourcegroups.go:82::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}` | ✓ `simulator-azure/resourcegroups.go:96::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/resources` | ✓ `simulator-azure/resourcegroups.go:108::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/resources` | ✓ `simulator-azure/resourcegroups.go:120::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `HEAD /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}` | ✓ `simulator-azure/resourcegroups.go:125::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
