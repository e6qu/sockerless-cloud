# Sim surface — azure-cosmos

Surface registered in `simulator-azure/cosmos.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `GET /{$}` | ✓ `simulator-azure/cosmos.go:157::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dbs` | ✓ `simulator-azure/cosmos.go:161::handleCosmosDataCreateDB` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dbs` | ✓ `simulator-azure/cosmos.go:162::handleCosmosDataListDBs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dbs/{database}` | ○ `simulator-azure/cosmos.go:163::handleCosmosDataGetDB` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dbs/{database}` | ✓ `simulator-azure/cosmos.go:164::handleCosmosDataDeleteDB` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dbs/{database}/colls` | ✓ `simulator-azure/cosmos.go:165::handleCosmosDataCreateColl` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dbs/{database}/colls` | ✓ `simulator-azure/cosmos.go:166::handleCosmosDataListColls` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dbs/{database}/colls/{container}` | ○ `simulator-azure/cosmos.go:167::handleCosmosDataGetColl` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dbs/{database}/colls/{container}` | ✓ `simulator-azure/cosmos.go:168::handleCosmosDataDeleteColl` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dbs/{database}/colls/{container}/docs` | ✓ `simulator-azure/cosmos.go:169::handleCosmosDataCreateOrQueryDoc` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dbs/{database}/colls/{container}/docs` | ✓ `simulator-azure/cosmos.go:170::handleCosmosDataListDocs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dbs/{database}/colls/{container}/docs/{doc}` | ✓ `simulator-azure/cosmos.go:171::handleCosmosDataGetDoc` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dbs/{database}/colls/{container}/docs/{doc}` | ✓ `simulator-azure/cosmos.go:172::handleCosmosDataReplaceDoc` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /dbs/{database}/colls/{container}/docs/{doc}` | ✓ `simulator-azure/cosmos.go:173::handleCosmosDataPatchDoc` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dbs/{database}/colls/{container}/docs/{doc}` | ✓ `simulator-azure/cosmos.go:174::handleCosmosDataDeleteDoc` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.DocumentDB/databaseAccounts` | ✓ `simulator-azure/cosmos_apis.go:62::handleCosmosListAccountsBySubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `HEAD /providers/Microsoft.DocumentDB/databaseAccountNames/{account}` | ✓ `simulator-azure/cosmos_apis.go:66::handleCosmosCheckNameExists` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers/Microsoft.DocumentDB/operations` | ○ `simulator-azure/cosmos_apis.go:69::handleCosmosOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.DocumentDB/locations` | ○ `simulator-azure/cosmos_apis.go:70::handleCosmosLocations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/Microsoft.DocumentDB/locations/{location}` | ○ `simulator-azure/cosmos_apis.go:71::handleCosmosLocationGet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dbs/{database}/colls/{container}/conflicts` | ○ `simulator-azure/cosmos_changefeed.go:33::handleCosmosListConflicts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dbs/{database}/colls/{container}/sprocs` | ✓ `simulator-azure/cosmos_scripts.go:55::handleCosmosCreateScript` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dbs/{database}/colls/{container}/sprocs` | ✓ `simulator-azure/cosmos_scripts.go:56::handleCosmosListScripts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dbs/{database}/colls/{container}/sprocs/{script}` | ✓ `simulator-azure/cosmos_scripts.go:57::handleCosmosGetScript` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dbs/{database}/colls/{container}/sprocs/{script}` | ✓ `simulator-azure/cosmos_scripts.go:58::handleCosmosReplaceScript` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dbs/{database}/colls/{container}/sprocs/{script}` | ✓ `simulator-azure/cosmos_scripts.go:59::handleCosmosDeleteScript` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dbs/{database}/colls/{container}/sprocs/{script}` | ✓ `simulator-azure/cosmos_scripts.go:61::handleCosmosExecuteSproc` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dbs/{database}/colls/{container}/udfs` | ✓ `simulator-azure/cosmos_scripts.go:63::handleCosmosCreateScript` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dbs/{database}/colls/{container}/udfs` | ✓ `simulator-azure/cosmos_scripts.go:64::handleCosmosListScripts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dbs/{database}/colls/{container}/udfs/{script}` | ✓ `simulator-azure/cosmos_scripts.go:65::handleCosmosGetScript` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dbs/{database}/colls/{container}/udfs/{script}` | ✓ `simulator-azure/cosmos_scripts.go:66::handleCosmosReplaceScript` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dbs/{database}/colls/{container}/udfs/{script}` | ✓ `simulator-azure/cosmos_scripts.go:67::handleCosmosDeleteScript` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /dbs/{database}/colls/{container}/triggers` | ✓ `simulator-azure/cosmos_scripts.go:69::handleCosmosCreateScript` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dbs/{database}/colls/{container}/triggers` | ✓ `simulator-azure/cosmos_scripts.go:70::handleCosmosListScripts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /dbs/{database}/colls/{container}/triggers/{script}` | ✓ `simulator-azure/cosmos_scripts.go:71::handleCosmosGetScript` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /dbs/{database}/colls/{container}/triggers/{script}` | ✓ `simulator-azure/cosmos_scripts.go:72::handleCosmosReplaceScript` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /dbs/{database}/colls/{container}/triggers/{script}` | ✓ `simulator-azure/cosmos_scripts.go:73::handleCosmosDeleteScript` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /offers` | ✓ `simulator-azure/cosmos_throughput.go:67::handleCosmosOffersQuery` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /offers/{offer}` | ✓ `simulator-azure/cosmos_throughput.go:68::handleCosmosGetOffer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /offers/{offer}/` | ✓ `simulator-azure/cosmos_throughput.go:69::handleCosmosGetOffer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /offers/{offer}` | ✓ `simulator-azure/cosmos_throughput.go:70::handleCosmosReplaceOffer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /offers/{offer}/` | ✓ `simulator-azure/cosmos_throughput.go:71::handleCosmosReplaceOffer` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
