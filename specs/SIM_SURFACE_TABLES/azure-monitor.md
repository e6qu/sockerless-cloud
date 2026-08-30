# Sim surface — azure-monitor

Surface registered in `simulator-azure/insights.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

The extractor reads the route out of a single string literal, so a registration that composes its path from a variable (`"GET "+prefix+"/…"`) produces no row here. Absence from this table is therefore not evidence that an op is unserved — check the source before concluding a gap. The status marker comes from `scripts/classify-sim-handlers.go`, which reads what the handler behind each route actually does.

## Status legend

- ✓ — implemented: the handler reads or writes simulator state, so the operation remembers what it did
- ○ — answers without reaching state. Correct for a published catalog or a computed echo, and the shape a stub has too — read the handler before trusting it
- ? — the handler is not declared in this package, so the generator cannot say
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — NotImplemented on the wire (a declared gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.Insights/components` | ✓ `simulator-azure/insights.go:237::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/apps/{appId}/query` | ○ `simulator-azure/insights.go:330::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.OperationalInsights/workspaces` | ✓ `simulator-azure/monitor.go:331::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/workspaces/{workspaceId}/query` | ? `simulator-azure/monitor.go:376::postQueryHandler` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/workspaces/{workspaceId}/query` | ? `simulator-azure/monitor.go:377::getQueryHandler` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/workspaces/{workspaceId}/metadata` | ? `simulator-azure/monitor.go:384::metadataHandler` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/workspaces/{workspaceId}/metadata` | ? `simulator-azure/monitor.go:385::metadataHandler` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/$batch` | ✓ `simulator-azure/monitor.go:389::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dataCollectionRules/{dcrId}/streams/{streamName}` | ✓ `simulator-azure/monitor.go:413::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
PR #388 (BUG-1316/1317) added Application Insights component CRUD (`PUT/GET/DELETE .../Microsoft.Insights/components/{name}`) and billing features. The upsert handler preserves the instrumentation key across updates (real App Insights keeps the same key). SDK coverage: `simulator-azure/sdk-tests/insights_test.go`. CLI coverage: `simulator-azure/cli-tests/monitor_test.go`. Terraform coverage: `simulator-azure/terraform-tests/main.tf` (`azurerm_application_insights`).
<!-- HAND-WRITTEN END -->
