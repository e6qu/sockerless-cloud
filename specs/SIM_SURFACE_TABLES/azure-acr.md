# Sim surface — azure-acr

Surface registered in `simulator-azure/acr.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /subscriptions/{subscriptionId}/providers/Microsoft.ContainerRegistry/checknameavailability` | ○ `simulator-azure/acr.go:98::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.ContainerRegistry/registries` | ✓ `simulator-azure/acr.go:222::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /acr/v1/_catalog` | ✓ `simulator-azure/acr.go:419::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /acr/v1/{path...}` | ✓ `simulator-azure/acr.go:452::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /oauth2/exchange` | ✓ `simulator-azure/acr.go:513::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /oauth2/token` | ✓ `simulator-azure/acr.go:535::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /oauth2/token` | ✓ `simulator-azure/acr.go:578::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers/Microsoft.ContainerRegistry/operations` | ○ `simulator-azure/acr.go:1197::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
PR #388 (BUG-1320/1321/1322) added manifest delete (`DELETE /v2/{path...}`), catalog listing (`GET /acr/v1/_catalog`), and tag listing (`GET /acr/v1/{name}/_tags`) to support the `azcontainerregistry` data-plane SDK (`UploadManifest` → `GetManifest` → `NewListRepositoriesPager` → `NewListTagsPager` → `DeleteManifest`). SDK coverage: `simulator-azure/sdk-tests/acr_test.go` (`TestACR_ImageManifestPushGetDelete`). CLI coverage: `simulator-azure/cli-tests/acr_test.go` (`TestACR_ImageCatalogAndTags`).
<!-- HAND-WRITTEN END -->
