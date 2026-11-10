# Sim surface — aws-pi

Surface registered in `simulator-aws/pi.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action PerformanceInsightsv20180227.CreatePerformanceAnalysisReport` | ✓ `simulator-aws/pi.go:86::handlePICreatePerformanceAnalysisReport` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action PerformanceInsightsv20180227.DeletePerformanceAnalysisReport` | ✓ `simulator-aws/pi.go:87::handlePIDeletePerformanceAnalysisReport` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action PerformanceInsightsv20180227.DescribeDimensionKeys` | ✓ `simulator-aws/pi.go:88::handlePIDescribeDimensionKeys` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action PerformanceInsightsv20180227.GetDimensionKeyDetails` | ✓ `simulator-aws/pi.go:89::handlePIGetDimensionKeyDetails` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action PerformanceInsightsv20180227.GetPerformanceAnalysisReport` | ✓ `simulator-aws/pi.go:90::handlePIGetPerformanceAnalysisReport` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action PerformanceInsightsv20180227.GetResourceMetadata` | ✓ `simulator-aws/pi.go:91::handlePIGetResourceMetadata` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action PerformanceInsightsv20180227.GetResourceMetrics` | ✓ `simulator-aws/pi.go:92::handlePIGetResourceMetrics` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action PerformanceInsightsv20180227.ListAvailableResourceDimensions` | ✓ `simulator-aws/pi.go:93::handlePIListAvailableResourceDimensions` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action PerformanceInsightsv20180227.ListAvailableResourceMetrics` | ✓ `simulator-aws/pi.go:94::handlePIListAvailableResourceMetrics` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action PerformanceInsightsv20180227.ListPerformanceAnalysisReportRecommendations` | ✓ `simulator-aws/pi.go:95::handlePIListPerformanceAnalysisReportRecommendations` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action PerformanceInsightsv20180227.ListPerformanceAnalysisReports` | ✓ `simulator-aws/pi.go:96::handlePIListPerformanceAnalysisReports` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action PerformanceInsightsv20180227.ListTagsForResource` | ✓ `simulator-aws/pi.go:97::handlePIListTagsForResource` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action PerformanceInsightsv20180227.TagResource` | ✓ `simulator-aws/pi.go:98::handlePITagResource` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |
| `Action PerformanceInsightsv20180227.UntagResource` | ✓ `simulator-aws/pi.go:99::handlePIUntagResource` | ✓ (direct; see coverage matrix) | n/a (not exposed by provider; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
