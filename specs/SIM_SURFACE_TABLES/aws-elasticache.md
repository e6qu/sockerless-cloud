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
| `Action CreateCacheCluster` | ✓ `simulator-aws/elasticache.go:206::handleECCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheClusters` | ✓ `simulator-aws/elasticache.go:207::handleECDescribe` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyCacheCluster` | ✓ `simulator-aws/elasticache.go:208::handleECModify` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteCacheCluster` | ✓ `simulator-aws/elasticache.go:209::handleECDelete` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RebootCacheCluster` | ✓ `simulator-aws/elasticache.go:210::handleECReboot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateReplicationGroup` | ✓ `simulator-aws/elasticache.go:211::handleECCreateReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeReplicationGroups` | ✓ `simulator-aws/elasticache.go:212::handleECDescribeReplGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyReplicationGroup` | ✓ `simulator-aws/elasticache.go:213::handleECModifyReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteReplicationGroup` | ✓ `simulator-aws/elasticache.go:214::handleECDeleteReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateCacheSubnetGroup` | ✓ `simulator-aws/elasticache.go:215::handleECCreateSubnetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheSubnetGroups` | ✓ `simulator-aws/elasticache.go:216::handleECDescribeSubnetGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyCacheSubnetGroup` | ✓ `simulator-aws/elasticache.go:217::handleECModifySubnetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteCacheSubnetGroup` | ✓ `simulator-aws/elasticache.go:218::handleECDeleteSubnetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateCacheParameterGroup` | ✓ `simulator-aws/elasticache.go:219::handleECCreateParamGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheParameterGroups` | ✓ `simulator-aws/elasticache.go:220::handleECDescribeParamGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteCacheParameterGroup` | ✓ `simulator-aws/elasticache.go:221::handleECDeleteParamGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AddTagsToResource` | ✓ `simulator-aws/elasticache.go:222::handleECAddTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListTagsForResource` | ✓ `simulator-aws/elasticache.go:223::handleECListTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RemoveTagsFromResource` | ✓ `simulator-aws/elasticache.go:224::handleECRemoveTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateSnapshot` | ✓ `simulator-aws/elasticache.go:229::handleECCreateSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeSnapshots` | ✓ `simulator-aws/elasticache.go:230::handleECDescribeSnapshots` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteSnapshot` | ✓ `simulator-aws/elasticache.go:231::handleECDeleteSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CopySnapshot` | ✓ `simulator-aws/elasticache.go:232::handleECCopySnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateUser` | ✓ `simulator-aws/elasticache.go:233::handleECCreateUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeUsers` | ✓ `simulator-aws/elasticache.go:234::handleECDescribeUsers` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyUser` | ✓ `simulator-aws/elasticache.go:235::handleECModifyUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteUser` | ✓ `simulator-aws/elasticache.go:236::handleECDeleteUser` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateUserGroup` | ✓ `simulator-aws/elasticache.go:237::handleECCreateUserGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeUserGroups` | ✓ `simulator-aws/elasticache.go:238::handleECDescribeUserGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyUserGroup` | ✓ `simulator-aws/elasticache.go:239::handleECModifyUserGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteUserGroup` | ✓ `simulator-aws/elasticache.go:240::handleECDeleteUserGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheParameters` | ✓ `simulator-aws/elasticache.go:241::handleECDescribeParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyCacheParameterGroup` | ✓ `simulator-aws/elasticache.go:242::handleECModifyParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ResetCacheParameterGroup` | ✓ `simulator-aws/elasticache.go:243::handleECResetParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeEngineDefaultParameters` | ○ `simulator-aws/elasticache.go:244::handleECDescribeEngineDefaultParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeEvents` | ✓ `simulator-aws/elasticache.go:245::handleECDescribeEvents` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheEngineVersions` | ○ `simulator-aws/elasticache.go:246::handleECDescribeCacheEngineVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeReservedCacheNodes` | ✓ `simulator-aws/elasticache.go:247::handleECDescribeReservedCacheNodes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeReservedCacheNodesOfferings` | ○ `simulator-aws/elasticache.go:248::handleECDescribeReservedCacheNodesOfferings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeServiceUpdates` | ○ `simulator-aws/elasticache.go:249::handleECDescribeServiceUpdates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCacheSecurityGroups` | ○ `simulator-aws/elasticache.go:250::handleECDescribeCacheSecurityGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateServerlessCache` | ✓ `simulator-aws/elasticache_serverless.go:133::handleECCreateServerlessCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeServerlessCaches` | ✓ `simulator-aws/elasticache_serverless.go:134::handleECDescribeServerlessCaches` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyServerlessCache` | ✓ `simulator-aws/elasticache_serverless.go:135::handleECModifyServerlessCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteServerlessCache` | ✓ `simulator-aws/elasticache_serverless.go:136::handleECDeleteServerlessCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateServerlessCacheSnapshot` | ✓ `simulator-aws/elasticache_serverless.go:139::handleECCreateServerlessSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeServerlessCacheSnapshots` | ✓ `simulator-aws/elasticache_serverless.go:140::handleECDescribeServerlessSnapshots` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteServerlessCacheSnapshot` | ✓ `simulator-aws/elasticache_serverless.go:141::handleECDeleteServerlessSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CopyServerlessCacheSnapshot` | ✓ `simulator-aws/elasticache_serverless.go:142::handleECCopyServerlessSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ExportServerlessCacheSnapshot` | ✓ `simulator-aws/elasticache_serverless.go:143::handleECExportServerlessSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:146::handleECCreateGlobalReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeGlobalReplicationGroups` | ✓ `simulator-aws/elasticache_serverless.go:147::handleECDescribeGlobalReplGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:148::handleECModifyGlobalReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:149::handleECDeleteGlobalReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DisassociateGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:150::handleECDisassociateGlobalReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action FailoverGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:151::handleECFailoverGlobalReplGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action IncreaseNodeGroupsInGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:152::handleECIncreaseNodeGroupsGlobal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DecreaseNodeGroupsInGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:153::handleECDecreaseNodeGroupsGlobal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RebalanceSlotsInGlobalReplicationGroup` | ✓ `simulator-aws/elasticache_serverless.go:154::handleECRebalanceSlotsGlobal` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateCacheSecurityGroup` | ✓ `simulator-aws/elasticache_serverless.go:157::handleECCreateCacheSecGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteCacheSecurityGroup` | ✓ `simulator-aws/elasticache_serverless.go:158::handleECDeleteCacheSecGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AuthorizeCacheSecurityGroupIngress` | ✓ `simulator-aws/elasticache_serverless.go:159::handleECAuthorizeCacheSecGroupIngress` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RevokeCacheSecurityGroupIngress` | ✓ `simulator-aws/elasticache_serverless.go:160::handleECRevokeCacheSecGroupIngress` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeUpdateActions` | ✓ `simulator-aws/elasticache_serverless.go:163::handleECDescribeUpdateActions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action BatchApplyUpdateAction` | ✓ `simulator-aws/elasticache_serverless.go:164::handleECBatchApplyUpdateAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action BatchStopUpdateAction` | ✓ `simulator-aws/elasticache_serverless.go:165::handleECBatchStopUpdateAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action IncreaseReplicaCount` | ✓ `simulator-aws/elasticache_serverless.go:168::handleECIncreaseReplicaCount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DecreaseReplicaCount` | ✓ `simulator-aws/elasticache_serverless.go:169::handleECDecreaseReplicaCount` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyReplicationGroupShardConfiguration` | ✓ `simulator-aws/elasticache_serverless.go:170::handleECModifyReplGroupShardConfig` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TestFailover` | ✓ `simulator-aws/elasticache_serverless.go:171::handleECTestFailover` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListAllowedNodeTypeModifications` | ○ `simulator-aws/elasticache_serverless.go:174::handleECListAllowedNodeTypeModifications` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PurchaseReservedCacheNodesOffering` | ✓ `simulator-aws/elasticache_serverless.go:175::handleECPurchaseReservedCacheNodesOffering` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StartMigration` | ✓ `simulator-aws/elasticache_serverless.go:176::handleECStartMigration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CompleteMigration` | ✓ `simulator-aws/elasticache_serverless.go:177::handleECCompleteMigration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action TestMigration` | ✓ `simulator-aws/elasticache_serverless.go:178::handleECTestMigration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
