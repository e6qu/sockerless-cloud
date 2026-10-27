# Sim surface — aws-ssm_parameters

Surface registered in `simulator-aws/ssm_cloud_connectors.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action AmazonSSM.CreateCloudConnector` | ✓ `simulator-aws/ssm_cloud_connectors.go:55::handleSSMCreateCloudConnector` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.GetCloudConnector` | ✓ `simulator-aws/ssm_cloud_connectors.go:56::handleSSMGetCloudConnector` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.ListCloudConnectors` | ✓ `simulator-aws/ssm_cloud_connectors.go:57::handleSSMListCloudConnectors` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.UpdateCloudConnector` | ✓ `simulator-aws/ssm_cloud_connectors.go:58::handleSSMUpdateCloudConnector` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.DeleteCloudConnector` | ✓ `simulator-aws/ssm_cloud_connectors.go:59::handleSSMDeleteCloudConnector` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.ValidateCloudConnector` | ✓ `simulator-aws/ssm_cloud_connectors.go:60::handleSSMValidateCloudConnector` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.CreateDocument` | ✓ `simulator-aws/ssm_documents.go:47::handleSSMCreateDocument` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.DeleteDocument` | ✓ `simulator-aws/ssm_documents.go:48::handleSSMDeleteDocument` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.DescribeDocument` | ✓ `simulator-aws/ssm_documents.go:49::handleSSMDescribeDocument` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.GetDocument` | ✓ `simulator-aws/ssm_documents.go:50::handleSSMGetDocument` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.ListDocuments` | ✓ `simulator-aws/ssm_documents.go:51::handleSSMListDocuments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.ListDocumentVersions` | ✓ `simulator-aws/ssm_documents.go:52::handleSSMListDocumentVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.UpdateDocument` | ✓ `simulator-aws/ssm_documents.go:53::handleSSMUpdateDocument` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.UpdateDocumentDefaultVersion` | ✓ `simulator-aws/ssm_documents.go:54::handleSSMUpdateDocumentDefaultVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.CreateMaintenanceWindow` | ✓ `simulator-aws/ssm_maintenance.go:76::handleSSMCreateMaintenanceWindow` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.DeleteMaintenanceWindow` | ✓ `simulator-aws/ssm_maintenance.go:77::handleSSMDeleteMaintenanceWindow` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.GetMaintenanceWindow` | ✓ `simulator-aws/ssm_maintenance.go:78::handleSSMGetMaintenanceWindow` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.UpdateMaintenanceWindow` | ✓ `simulator-aws/ssm_maintenance.go:79::handleSSMUpdateMaintenanceWindow` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.DescribeMaintenanceWindows` | ✓ `simulator-aws/ssm_maintenance.go:80::handleSSMDescribeMaintenanceWindows` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.RegisterTargetWithMaintenanceWindow` | ✓ `simulator-aws/ssm_maintenance.go:81::handleSSMRegisterTarget` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.RegisterTaskWithMaintenanceWindow` | ✓ `simulator-aws/ssm_maintenance.go:82::handleSSMRegisterTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.DeregisterTargetFromMaintenanceWindow` | ✓ `simulator-aws/ssm_maintenance.go:83::handleSSMDeregisterTarget` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.DeregisterTaskFromMaintenanceWindow` | ✓ `simulator-aws/ssm_maintenance.go:84::handleSSMDeregisterTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.DescribeMaintenanceWindowTargets` | ✓ `simulator-aws/ssm_maintenance.go:85::handleSSMDescribeTargets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.DescribeMaintenanceWindowTasks` | ✓ `simulator-aws/ssm_maintenance.go:86::handleSSMDescribeTasks` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.PutParameter` | ✓ `simulator-aws/ssm_parameters.go:125::handleSSMPutParameter` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.GetParameter` | ✓ `simulator-aws/ssm_parameters.go:126::handleSSMGetParameter` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.GetParameters` | ✓ `simulator-aws/ssm_parameters.go:127::handleSSMGetParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.GetParametersByPath` | ✓ `simulator-aws/ssm_parameters.go:128::handleSSMGetParametersByPath` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.DescribeParameters` | ✓ `simulator-aws/ssm_parameters.go:129::handleSSMDescribeParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.DeleteParameter` | ✓ `simulator-aws/ssm_parameters.go:130::handleSSMDeleteParameter` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.DeleteParameters` | ✓ `simulator-aws/ssm_parameters.go:131::handleSSMDeleteParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.AddTagsToResource` | ✓ `simulator-aws/ssm_parameters.go:132::handleSSMAddTagsToResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.RemoveTagsFromResource` | ✓ `simulator-aws/ssm_parameters.go:133::handleSSMRemoveTagsFromResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.ListTagsForResource` | ✓ `simulator-aws/ssm_parameters.go:134::handleSSMListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.CreatePatchBaseline` | ✓ `simulator-aws/ssm_patch_baselines.go:49::handleSSMCreatePatchBaseline` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.DeletePatchBaseline` | ✓ `simulator-aws/ssm_patch_baselines.go:50::handleSSMDeletePatchBaseline` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.GetPatchBaseline` | ✓ `simulator-aws/ssm_patch_baselines.go:51::handleSSMGetPatchBaseline` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.UpdatePatchBaseline` | ✓ `simulator-aws/ssm_patch_baselines.go:52::handleSSMUpdatePatchBaseline` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.DescribePatchBaselines` | ✓ `simulator-aws/ssm_patch_baselines.go:53::handleSSMDescribePatchBaselines` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.GetDefaultPatchBaseline` | ✓ `simulator-aws/ssm_patch_baselines.go:54::handleSSMGetDefaultPatchBaseline` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.RegisterDefaultPatchBaseline` | ✓ `simulator-aws/ssm_patch_baselines.go:55::handleSSMRegisterDefaultPatchBaseline` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.CreateResourceDataSync` | ✓ `simulator-aws/ssm_resource_data_sync.go:32::handleSSMCreateResourceDataSync` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.DeleteResourceDataSync` | ✓ `simulator-aws/ssm_resource_data_sync.go:33::handleSSMDeleteResourceDataSync` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.ListResourceDataSync` | ✓ `simulator-aws/ssm_resource_data_sync.go:34::handleSSMListResourceDataSync` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.UpdateResourceDataSync` | ✓ `simulator-aws/ssm_resource_data_sync.go:35::handleSSMUpdateResourceDataSync` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.GetServiceSetting` | ✓ `simulator-aws/ssm_service_settings.go:30::handleSSMGetServiceSetting` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.UpdateServiceSetting` | ✓ `simulator-aws/ssm_service_settings.go:31::handleSSMUpdateServiceSetting` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonSSM.ResetServiceSetting` | ✓ `simulator-aws/ssm_service_settings.go:32::handleSSMResetServiceSetting` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
