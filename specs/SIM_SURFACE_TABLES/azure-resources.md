# Sim surface — azure-resources

Surface registered in `simulator-azure/resourcesarm.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `GET /providers/Microsoft.Resources/operations` | ✓ `simulator-azure/resourcesarm.go:33::handleResourcesOperations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers` | ✓ `simulator-azure/resourcesarm.go:36::handleProvidersListTenant` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /providers/{resourceProviderNamespace}` | ✓ `simulator-azure/resourcesarm.go:37::handleProviderGetTenant` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}` | ✓ `simulator-azure/resourcesarm.go:38::handleProviderGet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/resourceTypes` | ✓ `simulator-azure/resourcesarm.go:39::handleProviderResourceTypes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/providerPermissions` | ✓ `simulator-azure/resourcesarm.go:40::handleProviderPermissions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/register` | ✓ `simulator-azure/resourcesarm.go:41::handleProviderRegister` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/unregister` | ✓ `simulator-azure/resourcesarm.go:42::handleProviderUnregister` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}` | ✓ `simulator-azure/resourcesarm.go:45::handleResourceGroupUpdate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/exportTemplate` | ✓ `simulator-azure/resourcesarm.go:46::handleResourceGroupExportTemplate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/moveResources` | ✓ `simulator-azure/resourcesarm.go:49::handleMoveResources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/validateMoveResources` | ✓ `simulator-azure/resourcesarm.go:50::handleMoveResources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /providers/Microsoft.Management/managementGroups/{groupId}/providers/{resourceProviderNamespace}/register` | ✓ `simulator-azure/resourcesarm.go:53::handleProviderRegisterAtMG` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /subscriptions/{subscriptionId}/tagNames` | ✓ `simulator-azure/resourcesarm.go:56::handleTagNamesList` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /subscriptions/{subscriptionId}/tagNames/{tagName}` | ✓ `simulator-azure/resourcesarm.go:57::handleTagNameCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /subscriptions/{subscriptionId}/tagNames/{tagName}` | ✓ `simulator-azure/resourcesarm.go:58::handleTagNameDelete` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /subscriptions/{subscriptionId}/tagNames/{tagName}/tagValues/{tagValue}` | ✓ `simulator-azure/resourcesarm.go:59::handleTagValueCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /subscriptions/{subscriptionId}/tagNames/{tagName}/tagValues/{tagValue}` | ✓ `simulator-azure/resourcesarm.go:60::handleTagValueDelete` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
