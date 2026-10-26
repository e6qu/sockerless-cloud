# Sim surface — aws-elasticache

Surface registered in `simulator-aws/elasticache.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action CreateCacheCluster` | ✓ `simulator-aws/elasticache.go:169::handleECCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheClusters` | ✓ `simulator-aws/elasticache.go:170::handleECDescribe` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyCacheCluster` | ✓ `simulator-aws/elasticache.go:171::handleECModify` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteCacheCluster` | ✓ `simulator-aws/elasticache.go:172::handleECDelete` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RebootCacheCluster` | ✓ `simulator-aws/elasticache.go:173::handleECReboot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateReplicationGroup` | ✓ `simulator-aws/elasticache.go:174::handleECCreateReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeReplicationGroups` | ✓ `simulator-aws/elasticache.go:175::handleECDescribeReplGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyReplicationGroup` | ✓ `simulator-aws/elasticache.go:176::handleECModifyReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteReplicationGroup` | ✓ `simulator-aws/elasticache.go:177::handleECDeleteReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateCacheSubnetGroup` | ✓ `simulator-aws/elasticache.go:178::handleECCreateSubnetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheSubnetGroups` | ✓ `simulator-aws/elasticache.go:179::handleECDescribeSubnetGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyCacheSubnetGroup` | ✓ `simulator-aws/elasticache.go:180::handleECModifySubnetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteCacheSubnetGroup` | ✓ `simulator-aws/elasticache.go:181::handleECDeleteSubnetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateCacheParameterGroup` | ✓ `simulator-aws/elasticache.go:182::handleECCreateParamGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheParameterGroups` | ✓ `simulator-aws/elasticache.go:183::handleECDescribeParamGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteCacheParameterGroup` | ✓ `simulator-aws/elasticache.go:184::handleECDeleteParamGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AddTagsToResource` | ✓ `simulator-aws/elasticache.go:185::handleECAddTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListTagsForResource` | ✓ `simulator-aws/elasticache.go:186::handleECListTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RemoveTagsFromResource` | ✓ `simulator-aws/elasticache.go:187::handleECRemoveTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateSnapshot` | ✓ `simulator-aws/elasticache.go:192::handleECCreateSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeSnapshots` | ✓ `simulator-aws/elasticache.go:193::handleECDescribeSnapshots` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteSnapshot` | ✓ `simulator-aws/elasticache.go:194::handleECDeleteSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CopySnapshot` | ✓ `simulator-aws/elasticache.go:195::handleECCopySnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateUser` | ✓ `simulator-aws/elasticache.go:196::handleECCreateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeUsers` | ✓ `simulator-aws/elasticache.go:197::handleECDescribeUsers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyUser` | ✓ `simulator-aws/elasticache.go:198::handleECModifyUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteUser` | ✓ `simulator-aws/elasticache.go:199::handleECDeleteUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateUserGroup` | ✓ `simulator-aws/elasticache.go:200::handleECCreateUserGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeUserGroups` | ✓ `simulator-aws/elasticache.go:201::handleECDescribeUserGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyUserGroup` | ✓ `simulator-aws/elasticache.go:202::handleECModifyUserGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteUserGroup` | ✓ `simulator-aws/elasticache.go:203::handleECDeleteUserGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheParameters` | ✓ `simulator-aws/elasticache.go:204::handleECDescribeParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyCacheParameterGroup` | ✓ `simulator-aws/elasticache.go:205::handleECModifyParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ResetCacheParameterGroup` | ✓ `simulator-aws/elasticache.go:206::handleECResetParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeEngineDefaultParameters` | ○ `simulator-aws/elasticache.go:207::handleECDescribeEngineDefaultParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeEvents` | ✓ `simulator-aws/elasticache.go:208::handleECDescribeEvents` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheEngineVersions` | ○ `simulator-aws/elasticache.go:209::handleECDescribeCacheEngineVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeReservedCacheNodes` | ✓ `simulator-aws/elasticache.go:210::handleECDescribeReservedCacheNodes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeReservedCacheNodesOfferings` | ○ `simulator-aws/elasticache.go:211::handleECDescribeReservedCacheNodesOfferings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeServiceUpdates` | ○ `simulator-aws/elasticache.go:212::handleECDescribeServiceUpdates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheSecurityGroups` | ○ `simulator-aws/elasticache.go:213::handleECDescribeCacheSecurityGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateServerlessCache` | ✓ `simulator-aws/elasticache_serverless.go:140::handleECCreateServerlessCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeServerlessCaches` | ✓ `simulator-aws/elasticache_serverless.go:141::handleECDescribeServerlessCaches` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyServerlessCache` | ✓ `simulator-aws/elasticache_serverless.go:142::handleECModifyServerlessCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteServerlessCache` | ✓ `simulator-aws/elasticache_serverless.go:143::handleECDeleteServerlessCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateServerlessCacheSnapshot` | ✓ `simulator-aws/elasticache_serverless.go:146::handleECCreateServerlessSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeServerlessCacheSnapshots` | ✓ `simulator-aws/elasticache_serverless.go:147::handleECDescribeServerlessSnapshots` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteServerlessCacheSnapshot` | ✓ `simulator-aws/elasticache_serverless.go:148::handleECDeleteServerlessSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CopyServerlessCacheSnapshot` | ✓ `simulator-aws/elasticache_serverless.go:149::handleECCopyServerlessSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ExportServerlessCacheSnapshot` | ✓ `simulator-aws/elasticache_serverless.go:150::handleECExportServerlessSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:153::handleECCreateGlobalReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeGlobalReplicationGroups` | ✓ `simulator-aws/elasticache_serverless.go:154::handleECDescribeGlobalReplGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:155::handleECModifyGlobalReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:156::handleECDeleteGlobalReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DisassociateGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:157::handleECDisassociateGlobalReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action FailoverGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:158::handleECFailoverGlobalReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action IncreaseNodeGroupsInGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:159::handleECIncreaseNodeGroupsGlobal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DecreaseNodeGroupsInGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:160::handleECDecreaseNodeGroupsGlobal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RebalanceSlotsInGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:161::handleECRebalanceSlotsGlobal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateCacheSecurityGroup` | ✓ `simulator-aws/elasticache_serverless.go:164::handleECCreateCacheSecGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteCacheSecurityGroup` | ✓ `simulator-aws/elasticache_serverless.go:165::handleECDeleteCacheSecGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AuthorizeCacheSecurityGroupIngress` | ✓ `simulator-aws/elasticache_serverless.go:166::handleECAuthorizeCacheSecGroupIngress` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RevokeCacheSecurityGroupIngress` | ✓ `simulator-aws/elasticache_serverless.go:167::handleECRevokeCacheSecGroupIngress` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeUpdateActions` | ✓ `simulator-aws/elasticache_serverless.go:170::handleECDescribeUpdateActions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action BatchApplyUpdateAction` | ✓ `simulator-aws/elasticache_serverless.go:171::handleECBatchApplyUpdateAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action BatchStopUpdateAction` | ✓ `simulator-aws/elasticache_serverless.go:172::handleECBatchStopUpdateAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action IncreaseReplicaCount` | ✓ `simulator-aws/elasticache_serverless.go:175::handleECIncreaseReplicaCount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DecreaseReplicaCount` | ✓ `simulator-aws/elasticache_serverless.go:176::handleECDecreaseReplicaCount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyReplicationGroupShardConfiguration` | ✓ `simulator-aws/elasticache_serverless.go:177::handleECModifyReplGroupShardConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TestFailover` | ✓ `simulator-aws/elasticache_serverless.go:178::handleECTestFailover` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListAllowedNodeTypeModifications` | ○ `simulator-aws/elasticache_serverless.go:181::handleECListAllowedNodeTypeModifications` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PurchaseReservedCacheNodesOffering` | ✓ `simulator-aws/elasticache_serverless.go:182::handleECPurchaseReservedCacheNodesOffering` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StartMigration` | ✓ `simulator-aws/elasticache_serverless.go:183::handleECStartMigration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CompleteMigration` | ✓ `simulator-aws/elasticache_serverless.go:184::handleECCompleteMigration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TestMigration` | ✓ `simulator-aws/elasticache_serverless.go:185::handleECTestMigration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PrepareMigration` | ✓ `simulator-aws/elasticache_serverless.go:186::handleECPrepareMigration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StopMigration` | ✓ `simulator-aws/elasticache_serverless.go:187::handleECStopMigration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
