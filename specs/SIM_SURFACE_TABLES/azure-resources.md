# Sim surface — azure-resources

Surface registered in `simulator-azure/resourcesarm.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `GET /{resourceId}` | ? `simulator-azure/resourcesarm.go:120::byID` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /{resourceId}` | ? `simulator-azure/resourcesarm.go:121::byID` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /{resourceId}` | ? `simulator-azure/resourcesarm.go:122::byID` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /{resourceId}` | ? `simulator-azure/resourcesarm.go:123::byID` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{resourceProviderNamespace}/{parentResourcePath}/{resourceType}/{resourceName}` | 501 `simulator-azure/resourcesarm.go:126::handleGenericProviderResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{resourceProviderNamespace}/{parentResourcePath}/{resourceType}/{resourceName}` | 501 `simulator-azure/resourcesarm.go:127::handleGenericProviderResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{resourceProviderNamespace}/{parentResourcePath}/{resourceType}/{resourceName}` | 501 `simulator-azure/resourcesarm.go:128::handleGenericProviderResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{resourceProviderNamespace}/{parentResourcePath}/{resourceType}/{resourceName}` | 501 `simulator-azure/resourcesarm.go:129::handleGenericProviderResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/tagNames` | ✓ `simulator-azure/resourcesarm.go:132::handleTagNamesList` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /subscriptions/{subscriptionId}/tagNames/{tagName}` | ✓ `simulator-azure/resourcesarm.go:133::handleTagNameCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /subscriptions/{subscriptionId}/tagNames/{tagName}` | ✓ `simulator-azure/resourcesarm.go:134::handleTagNameDelete` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /subscriptions/{subscriptionId}/tagNames/{tagName}/tagValues/{tagValue}` | ✓ `simulator-azure/resourcesarm.go:135::handleTagValueCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /subscriptions/{subscriptionId}/tagNames/{tagName}/tagValues/{tagValue}` | ✓ `simulator-azure/resourcesarm.go:136::handleTagValueDelete` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers/Microsoft.Resources/operations` | ○ `simulator-azure/resourcesarm.go:36::handleResourcesOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers` | ○ `simulator-azure/resourcesarm.go:39::handleProvidersListTenant` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers/{resourceProviderNamespace}` | ○ `simulator-azure/resourcesarm.go:40::handleProviderGetTenant` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}` | ○ `simulator-azure/resourcesarm.go:41::handleProviderGet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/resourceTypes` | ○ `simulator-azure/resourcesarm.go:42::handleProviderResourceTypes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/providerPermissions` | ○ `simulator-azure/resourcesarm.go:43::handleProviderPermissions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/register` | ○ `simulator-azure/resourcesarm.go:44::handleProviderRegister` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/unregister` | ○ `simulator-azure/resourcesarm.go:45::handleProviderUnregister` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}` | ✓ `simulator-azure/resourcesarm.go:48::handleResourceGroupUpdate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/exportTemplate` | ✓ `simulator-azure/resourcesarm.go:49::handleResourceGroupExportTemplate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/moveResources` | ✓ `simulator-azure/resourcesarm.go:52::handleMoveResources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/validateMoveResources` | ✓ `simulator-azure/resourcesarm.go:53::handleMoveResources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /providers/Microsoft.Management/managementGroups/{groupId}/providers/{resourceProviderNamespace}/register` | ○ `simulator-azure/resourcesarm.go:56::handleProviderRegisterAtMG` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
