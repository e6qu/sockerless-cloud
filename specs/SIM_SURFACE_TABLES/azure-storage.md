# Sim surface — azure-storage

Surface registered in `simulator-azure/files.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.Storage/storageAccounts` | ✓ `simulator-azure/files.go:760::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers/Microsoft.Storage/operations` | ✓ `simulator-azure/storagearm.go:117::handleStorageOperationsList` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.Storage/skus` | ✓ `simulator-azure/storagearm.go:118::handleStorageSkusList` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /subscriptions/{subscriptionId}/providers/Microsoft.Storage/checknameavailability` | ✓ `simulator-azure/storagearm.go:121::handleStorageCheckNameAvailability` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.Storage/locations/{location}/usages` | ✓ `simulator-azure/storagearm.go:122::handleStorageUsagesByLocation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.Storage/deletedAccounts` | ✓ `simulator-azure/storagearm.go:123::handleStorageDeletedAccountsList` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.Storage/locations/{location}/deletedAccounts/{deletedAccountName}` | ✓ `simulator-azure/storagearm.go:124::handleStorageDeletedAccountGet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts` | ✓ `simulator-azure/storagearm.go:125::handleStorageAccountsListByRG` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
