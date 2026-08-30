# Sim surface — aws-rds

Surface registered in `simulator-aws/rds.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action CreateDBInstance` | ✓ `simulator-aws/rds.go:284::handleRDSCreate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBInstances` | ✓ `simulator-aws/rds.go:285::handleRDSDescribe` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyDBInstance` | ✓ `simulator-aws/rds.go:286::handleRDSModify` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteDBInstance` | ✓ `simulator-aws/rds.go:287::handleRDSDelete` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AddTagsToResource` | ✓ `simulator-aws/rds.go:288::handleRDSAddTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ListTagsForResource` | ✓ `simulator-aws/rds.go:289::handleRDSListTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RemoveTagsFromResource` | ✓ `simulator-aws/rds.go:290::handleRDSRemoveTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateDBSnapshot` | ✓ `simulator-aws/rds.go:291::handleRDSCreateSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBSnapshots` | ✓ `simulator-aws/rds.go:292::handleRDSDescribeSnapshots` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBSnapshotAttributes` | ✓ `simulator-aws/rds.go:293::handleRDSDescribeSnapshotAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteDBSnapshot` | ✓ `simulator-aws/rds.go:294::handleRDSDeleteSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RestoreDBInstanceFromDBSnapshot` | ✓ `simulator-aws/rds.go:295::handleRDSRestoreFromSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CopyDBSnapshot` | ✓ `simulator-aws/rds.go:296::handleRDSCopySnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RebootDBInstance` | ✓ `simulator-aws/rds.go:297::handleRDSReboot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateDBInstanceReadReplica` | ✓ `simulator-aws/rds.go:298::handleRDSCreateReadReplica` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StartDBInstance` | ✓ `simulator-aws/rds.go:299::handleRDSStartInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StopDBInstance` | ✓ `simulator-aws/rds.go:300::handleRDSStopInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PromoteReadReplica` | ✓ `simulator-aws/rds.go:301::handleRDSPromoteReadReplica` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyDBSnapshotAttribute` | ✓ `simulator-aws/rds.go:302::handleRDSModifySnapshotAttribute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBParameters` | ✓ `simulator-aws/rds.go:303::handleRDSDescribeParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyDBParameterGroup` | ✓ `simulator-aws/rds.go:304::handleRDSModifyParameterGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ResetDBParameterGroup` | ✓ `simulator-aws/rds.go:305::handleRDSResetParameterGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateDBCluster` | ✓ `simulator-aws/rds.go:308::handleRDSCreateCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBClusters` | ✓ `simulator-aws/rds.go:309::handleRDSDescribeClusters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyDBCluster` | ✓ `simulator-aws/rds.go:310::handleRDSModifyCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteDBCluster` | ✓ `simulator-aws/rds.go:311::handleRDSDeleteCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StartDBCluster` | ✓ `simulator-aws/rds.go:312::handleRDSStartCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StopDBCluster` | ✓ `simulator-aws/rds.go:313::handleRDSStopCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action FailoverDBCluster` | ✓ `simulator-aws/rds.go:314::handleRDSFailoverCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBClusterParameters` | ✓ `simulator-aws/rds.go:315::handleRDSDescribeClusterParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyDBClusterParameterGroup` | ✓ `simulator-aws/rds.go:316::handleRDSModifyClusterParameterGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateGlobalCluster` | ✓ `simulator-aws/rds.go:319::handleRDSCreateGlobalCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeGlobalClusters` | ✓ `simulator-aws/rds.go:320::handleRDSDescribeGlobalClusters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyGlobalCluster` | ✓ `simulator-aws/rds.go:321::handleRDSModifyGlobalCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteGlobalCluster` | ✓ `simulator-aws/rds.go:322::handleRDSDeleteGlobalCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateEventSubscription` | ✓ `simulator-aws/rds.go:325::handleRDSCreateEventSubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeEventSubscriptions` | ✓ `simulator-aws/rds.go:326::handleRDSDescribeEventSubscriptions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyEventSubscription` | ✓ `simulator-aws/rds.go:327::handleRDSModifyEventSubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteEventSubscription` | ✓ `simulator-aws/rds.go:328::handleRDSDeleteEventSubscription` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateDBClusterEndpoint` | ✓ `simulator-aws/rds.go:331::handleRDSCreateClusterEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBClusterEndpoints` | ✓ `simulator-aws/rds.go:332::handleRDSDescribeClusterEndpoints` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteDBClusterEndpoint` | ✓ `simulator-aws/rds.go:333::handleRDSDeleteClusterEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateDBClusterSnapshot` | ✓ `simulator-aws/rds.go:336::handleRDSCreateClusterSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBClusterSnapshots` | ✓ `simulator-aws/rds.go:337::handleRDSDescribeClusterSnapshots` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteDBClusterSnapshot` | ✓ `simulator-aws/rds.go:338::handleRDSDeleteClusterSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CopyDBClusterSnapshot` | ✓ `simulator-aws/rds.go:339::handleRDSCopyClusterSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateDBClusterParameterGroup` | ✓ `simulator-aws/rds.go:342::handleRDSCreateClusterParamGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBClusterParameterGroups` | ✓ `simulator-aws/rds.go:343::handleRDSDescribeClusterParamGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteDBClusterParameterGroup` | ✓ `simulator-aws/rds.go:344::handleRDSDeleteClusterParamGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateOptionGroup` | ✓ `simulator-aws/rds.go:347::handleRDSCreateOptionGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeOptionGroups` | ✓ `simulator-aws/rds.go:348::handleRDSDescribeOptionGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteOptionGroup` | ✓ `simulator-aws/rds.go:349::handleRDSDeleteOptionGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeEvents` | ✓ `simulator-aws/rds.go:351::handleRDSDescribeEvents` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeEventCategories` | ○ `simulator-aws/rds.go:352::handleRDSDescribeEventCategories` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBEngineVersions` | ○ `simulator-aws/rds.go:353::handleRDSDescribeEngineVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeOrderableDBInstanceOptions` | ○ `simulator-aws/rds.go:354::handleRDSDescribeOrderableOptions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateDBSubnetGroup` | ✓ `simulator-aws/rds.go:357::handleRDSCreateSubnetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBSubnetGroups` | ✓ `simulator-aws/rds.go:358::handleRDSDescribeSubnetGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyDBSubnetGroup` | ✓ `simulator-aws/rds.go:359::handleRDSModifySubnetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteDBSubnetGroup` | ✓ `simulator-aws/rds.go:360::handleRDSDeleteSubnetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateDBParameterGroup` | ✓ `simulator-aws/rds.go:363::handleRDSCreateParamGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBParameterGroups` | ✓ `simulator-aws/rds.go:364::handleRDSDescribeParamGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteDBParameterGroup` | ✓ `simulator-aws/rds.go:365::handleRDSDeleteParamGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateCustomDBEngineVersion` | ✓ `simulator-aws/rds_complete.go:105::handleRDSCreateCustomEngineVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyCustomDBEngineVersion` | ✓ `simulator-aws/rds_complete.go:106::handleRDSModifyCustomEngineVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteCustomDBEngineVersion` | ✓ `simulator-aws/rds_complete.go:107::handleRDSDeleteCustomEngineVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBRecommendations` | ✓ `simulator-aws/rds_complete.go:109::handleRDSDescribeRecommendations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyDBRecommendation` | ✓ `simulator-aws/rds_complete.go:110::handleRDSModifyRecommendation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBSnapshotTenantDatabases` | ✓ `simulator-aws/rds_complete.go:112::handleRDSDescribeSnapshotTenantDatabases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeServerlessV2PlatformVersions` | ○ `simulator-aws/rds_complete.go:113::handleRDSDescribeServerlessV2PlatformVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeValidDBInstanceModifications` | ✓ `simulator-aws/rds_complete.go:114::handleRDSDescribeValidDBInstanceModifications` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyCurrentDBClusterCapacity` | ✓ `simulator-aws/rds_complete.go:116::handleRDSModifyCurrentDBClusterCapacity` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyOptionGroup` | ✓ `simulator-aws/rds_complete.go:117::handleRDSModifyOptionGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StartDBInstanceAutomatedBackupsReplication` | ✓ `simulator-aws/rds_complete.go:119::handleRDSStartAutomatedBackupsReplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StopDBInstanceAutomatedBackupsReplication` | ✓ `simulator-aws/rds_complete.go:120::handleRDSStopAutomatedBackupsReplication` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SwitchoverGlobalCluster` | ✓ `simulator-aws/rds_complete.go:122::handleRDSSwitchoverGlobalCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SwitchoverReadReplica` | ✓ `simulator-aws/rds_complete.go:123::handleRDSSwitchoverReadReplica` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeAccountAttributes` | ✓ `simulator-aws/rds_complete.go:125::handleRDSDescribeAccountAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateDBProxy` | ✓ `simulator-aws/rds_proxies_roles.go:217::handleRDSCreateProxy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBProxies` | ✓ `simulator-aws/rds_proxies_roles.go:218::handleRDSDescribeProxies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyDBProxy` | ✓ `simulator-aws/rds_proxies_roles.go:219::handleRDSModifyProxy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteDBProxy` | ✓ `simulator-aws/rds_proxies_roles.go:220::handleRDSDeleteProxy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateDBProxyEndpoint` | ✓ `simulator-aws/rds_proxies_roles.go:221::handleRDSCreateProxyEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBProxyEndpoints` | ✓ `simulator-aws/rds_proxies_roles.go:222::handleRDSDescribeProxyEndpoints` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyDBProxyEndpoint` | ✓ `simulator-aws/rds_proxies_roles.go:223::handleRDSModifyProxyEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteDBProxyEndpoint` | ✓ `simulator-aws/rds_proxies_roles.go:224::handleRDSDeleteProxyEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBProxyTargets` | ✓ `simulator-aws/rds_proxies_roles.go:225::handleRDSDescribeProxyTargets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBProxyTargetGroups` | ✓ `simulator-aws/rds_proxies_roles.go:226::handleRDSDescribeProxyTargetGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyDBProxyTargetGroup` | ✓ `simulator-aws/rds_proxies_roles.go:227::handleRDSModifyProxyTargetGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RegisterDBProxyTargets` | ✓ `simulator-aws/rds_proxies_roles.go:228::handleRDSRegisterProxyTargets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeregisterDBProxyTargets` | ✓ `simulator-aws/rds_proxies_roles.go:229::handleRDSDeregisterProxyTargets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AddRoleToDBCluster` | ✓ `simulator-aws/rds_proxies_roles.go:232::handleRDSAddRoleToCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RemoveRoleFromDBCluster` | ✓ `simulator-aws/rds_proxies_roles.go:233::handleRDSRemoveRoleFromCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AddRoleToDBInstance` | ✓ `simulator-aws/rds_proxies_roles.go:234::handleRDSAddRoleToInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RemoveRoleFromDBInstance` | ✓ `simulator-aws/rds_proxies_roles.go:235::handleRDSRemoveRoleFromInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateDBSecurityGroup` | ✓ `simulator-aws/rds_proxies_roles.go:238::handleRDSCreateSecurityGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBSecurityGroups` | ✓ `simulator-aws/rds_proxies_roles.go:239::handleRDSDescribeSecurityGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteDBSecurityGroup` | ✓ `simulator-aws/rds_proxies_roles.go:240::handleRDSDeleteSecurityGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AuthorizeDBSecurityGroupIngress` | ✓ `simulator-aws/rds_proxies_roles.go:241::handleRDSAuthorizeSecurityGroupIngress` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RevokeDBSecurityGroupIngress` | ✓ `simulator-aws/rds_proxies_roles.go:242::handleRDSRevokeSecurityGroupIngress` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeCertificates` | ✓ `simulator-aws/rds_proxies_roles.go:245::handleRDSDescribeCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyCertificates` | ✓ `simulator-aws/rds_proxies_roles.go:246::handleRDSModifyCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBInstanceAutomatedBackups` | ✓ `simulator-aws/rds_proxies_roles.go:249::handleRDSDescribeInstanceAutomatedBackups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteDBInstanceAutomatedBackup` | ✓ `simulator-aws/rds_proxies_roles.go:250::handleRDSDeleteInstanceAutomatedBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBClusterAutomatedBackups` | ✓ `simulator-aws/rds_proxies_roles.go:251::handleRDSDescribeClusterAutomatedBackups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteDBClusterAutomatedBackup` | ✓ `simulator-aws/rds_proxies_roles.go:252::handleRDSDeleteClusterAutomatedBackup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBLogFiles` | ✓ `simulator-aws/rds_proxies_roles.go:255::handleRDSDescribeLogFiles` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DownloadDBLogFilePortion` | ✓ `simulator-aws/rds_proxies_roles.go:256::handleRDSDownloadLogFilePortion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CopyDBClusterParameterGroup` | ✓ `simulator-aws/rds_proxies_roles.go:259::handleRDSCopyClusterParameterGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CopyDBParameterGroup` | ✓ `simulator-aws/rds_proxies_roles.go:260::handleRDSCopyParameterGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CopyOptionGroup` | ✓ `simulator-aws/rds_proxies_roles.go:261::handleRDSCopyOptionGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AddSourceIdentifierToSubscription` | ✓ `simulator-aws/rds_proxies_roles.go:264::handleRDSAddSourceIdentifier` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RemoveSourceIdentifierFromSubscription` | ✓ `simulator-aws/rds_proxies_roles.go:265::handleRDSRemoveSourceIdentifier` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ApplyPendingMaintenanceAction` | ✓ `simulator-aws/rds_proxies_roles.go:268::handleRDSApplyPendingMaintenanceAction` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribePendingMaintenanceActions` | ✓ `simulator-aws/rds_proxies_roles.go:269::handleRDSDescribePendingMaintenanceActions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RestoreDBClusterFromSnapshot` | ✓ `simulator-aws/rds_restore_extras.go:38::handleRDSRestoreClusterFromSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RestoreDBClusterToPointInTime` | ✓ `simulator-aws/rds_restore_extras.go:39::handleRDSRestoreClusterToPointInTime` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RestoreDBInstanceToPointInTime` | ✓ `simulator-aws/rds_restore_extras.go:40::handleRDSRestoreInstanceToPointInTime` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RestoreDBClusterFromS3` | ✓ `simulator-aws/rds_restore_extras.go:41::handleRDSRestoreClusterFromS3` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RestoreDBInstanceFromS3` | ✓ `simulator-aws/rds_restore_extras.go:42::handleRDSRestoreInstanceFromS3` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeReservedDBInstances` | ✓ `simulator-aws/rds_restore_extras.go:45::handleRDSDescribeReservedInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeReservedDBInstancesOfferings` | ○ `simulator-aws/rds_restore_extras.go:46::handleRDSDescribeReservedInstancesOfferings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PurchaseReservedDBInstancesOffering` | ✓ `simulator-aws/rds_restore_extras.go:47::handleRDSPurchaseReservedInstancesOffering` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateBlueGreenDeployment` | ✓ `simulator-aws/rds_restore_extras.go:50::handleRDSCreateBlueGreenDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeBlueGreenDeployments` | ✓ `simulator-aws/rds_restore_extras.go:51::handleRDSDescribeBlueGreenDeployments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteBlueGreenDeployment` | ✓ `simulator-aws/rds_restore_extras.go:52::handleRDSDeleteBlueGreenDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action SwitchoverBlueGreenDeployment` | ✓ `simulator-aws/rds_restore_extras.go:53::handleRDSSwitchoverBlueGreenDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateIntegration` | ✓ `simulator-aws/rds_restore_extras.go:56::handleRDSCreateIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeIntegrations` | ✓ `simulator-aws/rds_restore_extras.go:57::handleRDSDescribeIntegrations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyIntegration` | ✓ `simulator-aws/rds_restore_extras.go:58::handleRDSModifyIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteIntegration` | ✓ `simulator-aws/rds_restore_extras.go:59::handleRDSDeleteIntegration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateTenantDatabase` | ✓ `simulator-aws/rds_restore_extras.go:62::handleRDSCreateTenantDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeTenantDatabases` | ✓ `simulator-aws/rds_restore_extras.go:63::handleRDSDescribeTenantDatabases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyTenantDatabase` | ✓ `simulator-aws/rds_restore_extras.go:64::handleRDSModifyTenantDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteTenantDatabase` | ✓ `simulator-aws/rds_restore_extras.go:65::handleRDSDeleteTenantDatabase` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CreateDBShardGroup` | ✓ `simulator-aws/rds_restore_extras.go:68::handleRDSCreateShardGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBShardGroups` | ✓ `simulator-aws/rds_restore_extras.go:69::handleRDSDescribeShardGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyDBShardGroup` | ✓ `simulator-aws/rds_restore_extras.go:70::handleRDSModifyShardGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DeleteDBShardGroup` | ✓ `simulator-aws/rds_restore_extras.go:71::handleRDSDeleteShardGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RebootDBShardGroup` | ✓ `simulator-aws/rds_restore_extras.go:72::handleRDSRebootShardGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StartActivityStream` | ✓ `simulator-aws/rds_restore_extras.go:75::handleRDSStartActivityStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StopActivityStream` | ✓ `simulator-aws/rds_restore_extras.go:76::handleRDSStopActivityStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyActivityStream` | ✓ `simulator-aws/rds_restore_extras.go:77::handleRDSModifyActivityStream` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action BacktrackDBCluster` | ✓ `simulator-aws/rds_restore_extras.go:80::handleRDSBacktrackCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBClusterBacktracks` | ✓ `simulator-aws/rds_restore_extras.go:81::handleRDSDescribeClusterBacktracks` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action StartExportTask` | ✓ `simulator-aws/rds_restore_extras.go:84::handleRDSStartExportTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeExportTasks` | ✓ `simulator-aws/rds_restore_extras.go:85::handleRDSDescribeExportTasks` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CancelExportTask` | ✓ `simulator-aws/rds_restore_extras.go:86::handleRDSCancelExportTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RebootDBCluster` | ✓ `simulator-aws/rds_restore_extras.go:89::handleRDSRebootCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ResetDBClusterParameterGroup` | ✓ `simulator-aws/rds_restore_extras.go:90::handleRDSResetClusterParameterGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyDBClusterEndpoint` | ✓ `simulator-aws/rds_restore_extras.go:91::handleRDSModifyClusterEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action FailoverGlobalCluster` | ✓ `simulator-aws/rds_restore_extras.go:92::handleRDSFailoverGlobalCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action RemoveFromGlobalCluster` | ✓ `simulator-aws/rds_restore_extras.go:93::handleRDSRemoveFromGlobalCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action PromoteReadReplicaDBCluster` | ✓ `simulator-aws/rds_restore_extras.go:94::handleRDSPromoteReadReplicaCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action EnableHttpEndpoint` | ✓ `simulator-aws/rds_restore_extras.go:95::handleRDSEnableHTTPEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DisableHttpEndpoint` | ✓ `simulator-aws/rds_restore_extras.go:96::handleRDSDisableHTTPEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyDBClusterSnapshotAttribute` | ✓ `simulator-aws/rds_restore_extras.go:99::handleRDSModifyClusterSnapshotAttribute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBClusterSnapshotAttributes` | ✓ `simulator-aws/rds_restore_extras.go:100::handleRDSDescribeClusterSnapshotAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action ModifyDBSnapshot` | ✓ `simulator-aws/rds_restore_extras.go:101::handleRDSModifySnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeOptionGroupOptions` | ○ `simulator-aws/rds_restore_extras.go:104::handleRDSDescribeOptionGroupOptions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeEngineDefaultParameters` | ○ `simulator-aws/rds_restore_extras.go:105::handleRDSDescribeEngineDefaultParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeEngineDefaultClusterParameters` | ○ `simulator-aws/rds_restore_extras.go:106::handleRDSDescribeEngineDefaultClusterParameters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeSourceRegions` | ○ `simulator-aws/rds_restore_extras.go:107::handleRDSDescribeSourceRegions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action DescribeDBMajorEngineVersions` | ○ `simulator-aws/rds_restore_extras.go:108::handleRDSDescribeDBMajorEngineVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
Amazon Relational Database Service DB instances using PostgreSQL, MySQL, or
MariaDB expose the native database protocol at the returned `Endpoint`. The
engine starts lazily in its real vendor container, retains data in an
instance-owned volume, terminates TLS at the service endpoint, and accepts
either the encrypted master credential or a TLS-protected, 15-minute SigV4 IAM
database authentication token authorized through `rds-db:connect`.
`ModifyDBInstance` changes IAM authentication and rotates the actual database
account both while running and across a stopped/start lifecycle without
replacing the volume. Stock pgx and MySQL drivers prove authentication denial,
TLS enforcement, password rotation, stop/start persistence, and SQL reads and
writes against all three engines.
<!-- HAND-WRITTEN END -->
