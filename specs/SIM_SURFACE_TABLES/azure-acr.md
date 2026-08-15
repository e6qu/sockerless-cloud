# Sim surface — azure-acr

Surface registered in `simulator-azure/acr.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `POST /subscriptions/{subscriptionId}/providers/Microsoft.ContainerRegistry/checknameavailability` | ✓ `simulator-azure/acr.go:100::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.ContainerRegistry/registries` | ✓ `simulator-azure/acr.go:224::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /acr/v1/_catalog` | ✓ `simulator-azure/acr.go:423::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /acr/v1/{path...}` | ✓ `simulator-azure/acr.go:456::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /oauth2/exchange` | ✓ `simulator-azure/acr.go:517::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /oauth2/token` | ✓ `simulator-azure/acr.go:539::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /oauth2/token` | ✓ `simulator-azure/acr.go:582::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers/Microsoft.ContainerRegistry/operations` | ✓ `simulator-azure/acr.go:1203::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
PR #388 (BUG-1320/1321/1322) added manifest delete (`DELETE /v2/{path...}`), catalog listing (`GET /acr/v1/_catalog`), and tag listing (`GET /acr/v1/{name}/_tags`) to support the `azcontainerregistry` data-plane SDK (`UploadManifest` → `GetManifest` → `NewListRepositoriesPager` → `NewListTagsPager` → `DeleteManifest`). SDK coverage: `simulator-azure/sdk-tests/acr_test.go` (`TestACR_ImageManifestPushGetDelete`). CLI coverage: `simulator-azure/cli-tests/acr_test.go` (`TestACR_ImageCatalogAndTags`).
<!-- HAND-WRITTEN END -->
