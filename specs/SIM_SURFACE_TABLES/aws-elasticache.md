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
| `Action CreateCacheCluster` | ✓ `simulator-aws/elasticache.go:157::handleECCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheClusters` | ✓ `simulator-aws/elasticache.go:158::handleECDescribe` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyCacheCluster` | ✓ `simulator-aws/elasticache.go:159::handleECModify` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteCacheCluster` | ✓ `simulator-aws/elasticache.go:160::handleECDelete` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RebootCacheCluster` | ✓ `simulator-aws/elasticache.go:161::handleECReboot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateReplicationGroup` | ✓ `simulator-aws/elasticache.go:162::handleECCreateReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeReplicationGroups` | ✓ `simulator-aws/elasticache.go:163::handleECDescribeReplGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyReplicationGroup` | ✓ `simulator-aws/elasticache.go:164::handleECModifyReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteReplicationGroup` | ✓ `simulator-aws/elasticache.go:165::handleECDeleteReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateCacheSubnetGroup` | ✓ `simulator-aws/elasticache.go:166::handleECCreateSubnetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheSubnetGroups` | ✓ `simulator-aws/elasticache.go:167::handleECDescribeSubnetGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyCacheSubnetGroup` | ✓ `simulator-aws/elasticache.go:168::handleECModifySubnetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteCacheSubnetGroup` | ✓ `simulator-aws/elasticache.go:169::handleECDeleteSubnetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateCacheParameterGroup` | ✓ `simulator-aws/elasticache.go:170::handleECCreateParamGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheParameterGroups` | ✓ `simulator-aws/elasticache.go:171::handleECDescribeParamGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteCacheParameterGroup` | ✓ `simulator-aws/elasticache.go:172::handleECDeleteParamGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AddTagsToResource` | ✓ `simulator-aws/elasticache.go:173::handleECAddTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListTagsForResource` | ✓ `simulator-aws/elasticache.go:174::handleECListTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RemoveTagsFromResource` | ✓ `simulator-aws/elasticache.go:175::handleECRemoveTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateSnapshot` | ✓ `simulator-aws/elasticache.go:183::handleECCreateSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeSnapshots` | ✓ `simulator-aws/elasticache.go:184::handleECDescribeSnapshots` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteSnapshot` | ✓ `simulator-aws/elasticache.go:185::handleECDeleteSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CopySnapshot` | ✓ `simulator-aws/elasticache.go:186::handleECCopySnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateUser` | ✓ `simulator-aws/elasticache.go:187::handleECCreateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeUsers` | ✓ `simulator-aws/elasticache.go:188::handleECDescribeUsers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyUser` | ✓ `simulator-aws/elasticache.go:189::handleECModifyUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteUser` | ✓ `simulator-aws/elasticache.go:190::handleECDeleteUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateUserGroup` | ✓ `simulator-aws/elasticache.go:191::handleECCreateUserGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeUserGroups` | ✓ `simulator-aws/elasticache.go:192::handleECDescribeUserGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyUserGroup` | ✓ `simulator-aws/elasticache.go:193::handleECModifyUserGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteUserGroup` | ✓ `simulator-aws/elasticache.go:194::handleECDeleteUserGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheParameters` | ✓ `simulator-aws/elasticache.go:195::handleECDescribeParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyCacheParameterGroup` | ✓ `simulator-aws/elasticache.go:196::handleECModifyParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ResetCacheParameterGroup` | ✓ `simulator-aws/elasticache.go:197::handleECResetParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeEngineDefaultParameters` | ○ `simulator-aws/elasticache.go:198::handleECDescribeEngineDefaultParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeEvents` | ✓ `simulator-aws/elasticache.go:199::handleECDescribeEvents` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheEngineVersions` | ○ `simulator-aws/elasticache.go:200::handleECDescribeCacheEngineVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeReservedCacheNodes` | ✓ `simulator-aws/elasticache.go:201::handleECDescribeReservedCacheNodes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeReservedCacheNodesOfferings` | ○ `simulator-aws/elasticache.go:202::handleECDescribeReservedCacheNodesOfferings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeServiceUpdates` | ○ `simulator-aws/elasticache.go:203::handleECDescribeServiceUpdates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheSecurityGroups` | ○ `simulator-aws/elasticache.go:204::handleECDescribeCacheSecurityGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateServerlessCache` | ✓ `simulator-aws/elasticache_serverless.go:127::handleECCreateServerlessCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeServerlessCaches` | ✓ `simulator-aws/elasticache_serverless.go:128::handleECDescribeServerlessCaches` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyServerlessCache` | ✓ `simulator-aws/elasticache_serverless.go:129::handleECModifyServerlessCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteServerlessCache` | ✓ `simulator-aws/elasticache_serverless.go:130::handleECDeleteServerlessCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateServerlessCacheSnapshot` | ✓ `simulator-aws/elasticache_serverless.go:132::handleECCreateServerlessSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeServerlessCacheSnapshots` | ✓ `simulator-aws/elasticache_serverless.go:133::handleECDescribeServerlessSnapshots` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteServerlessCacheSnapshot` | ✓ `simulator-aws/elasticache_serverless.go:134::handleECDeleteServerlessSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CopyServerlessCacheSnapshot` | ✓ `simulator-aws/elasticache_serverless.go:135::handleECCopyServerlessSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ExportServerlessCacheSnapshot` | ✓ `simulator-aws/elasticache_serverless.go:136::handleECExportServerlessSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:138::handleECCreateGlobalReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeGlobalReplicationGroups` | ✓ `simulator-aws/elasticache_serverless.go:139::handleECDescribeGlobalReplGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:140::handleECModifyGlobalReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:141::handleECDeleteGlobalReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DisassociateGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:142::handleECDisassociateGlobalReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action FailoverGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:143::handleECFailoverGlobalReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action IncreaseNodeGroupsInGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:144::handleECIncreaseNodeGroupsGlobal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DecreaseNodeGroupsInGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:145::handleECDecreaseNodeGroupsGlobal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RebalanceSlotsInGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:146::handleECRebalanceSlotsGlobal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateCacheSecurityGroup` | ✓ `simulator-aws/elasticache_serverless.go:148::handleECCreateCacheSecGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteCacheSecurityGroup` | ✓ `simulator-aws/elasticache_serverless.go:149::handleECDeleteCacheSecGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AuthorizeCacheSecurityGroupIngress` | ✓ `simulator-aws/elasticache_serverless.go:150::handleECAuthorizeCacheSecGroupIngress` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RevokeCacheSecurityGroupIngress` | ✓ `simulator-aws/elasticache_serverless.go:151::handleECRevokeCacheSecGroupIngress` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeUpdateActions` | ✓ `simulator-aws/elasticache_serverless.go:153::handleECDescribeUpdateActions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action BatchApplyUpdateAction` | ✓ `simulator-aws/elasticache_serverless.go:154::handleECBatchApplyUpdateAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action BatchStopUpdateAction` | ✓ `simulator-aws/elasticache_serverless.go:155::handleECBatchStopUpdateAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action IncreaseReplicaCount` | ✓ `simulator-aws/elasticache_serverless.go:157::handleECIncreaseReplicaCount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DecreaseReplicaCount` | ✓ `simulator-aws/elasticache_serverless.go:158::handleECDecreaseReplicaCount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyReplicationGroupShardConfiguration` | ✓ `simulator-aws/elasticache_serverless.go:159::handleECModifyReplGroupShardConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TestFailover` | ✓ `simulator-aws/elasticache_serverless.go:160::handleECTestFailover` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListAllowedNodeTypeModifications` | ○ `simulator-aws/elasticache_serverless.go:162::handleECListAllowedNodeTypeModifications` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PurchaseReservedCacheNodesOffering` | ✓ `simulator-aws/elasticache_serverless.go:163::handleECPurchaseReservedCacheNodesOffering` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StartMigration` | ✓ `simulator-aws/elasticache_serverless.go:164::handleECStartMigration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CompleteMigration` | ✓ `simulator-aws/elasticache_serverless.go:165::handleECCompleteMigration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TestMigration` | ✓ `simulator-aws/elasticache_serverless.go:166::handleECTestMigration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PrepareMigration` | ✓ `simulator-aws/elasticache_serverless.go:167::handleECPrepareMigration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StopMigration` | ✓ `simulator-aws/elasticache_serverless.go:168::handleECStopMigration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
