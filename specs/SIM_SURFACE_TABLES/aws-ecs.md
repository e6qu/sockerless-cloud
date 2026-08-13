# Sim surface — aws-ecs

Surface registered in `simulator-aws/ecs.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `GET /ecs-exec/{sessionId}` | ✓ `simulator-aws/ecs.go:508::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `PUT /sockerless/tasks/{taskId}/archive` | ✓ `simulator-aws/ecs.go:514::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.CreateCluster` | ✓ `simulator-aws/ecs.go:472::handleECSCreateCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeClusters` | ✓ `simulator-aws/ecs.go:473::handleECSDescribeClusters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateCluster` | ✓ `simulator-aws/ecs.go:474::handleECSUpdateCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateClusterSettings` | ✓ `simulator-aws/ecs.go:475::handleECSUpdateClusterSettings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.RegisterTaskDefinition` | ✓ `simulator-aws/ecs.go:476::handleECSRegisterTaskDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeregisterTaskDefinition` | ✓ `simulator-aws/ecs.go:477::handleECSDeregisterTaskDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeTaskDefinition` | ✓ `simulator-aws/ecs.go:478::handleECSDescribeTaskDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.RunTask` | ✓ `simulator-aws/ecs.go:479::handleECSRunTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeTasks` | ✓ `simulator-aws/ecs.go:480::handleECSDescribeTasks` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.StopTask` | ✓ `simulator-aws/ecs.go:481::handleECSStopTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListTasks` | ✓ `simulator-aws/ecs.go:482::handleECSListTasks` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteCluster` | ✓ `simulator-aws/ecs.go:483::handleECSDeleteCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListTagsForResource` | ✓ `simulator-aws/ecs.go:484::handleECSListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.TagResource` | ✓ `simulator-aws/ecs.go:485::handleECSTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UntagResource` | ✓ `simulator-aws/ecs.go:486::handleECSUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ExecuteCommand` | ✓ `simulator-aws/ecs.go:487::handleECSExecuteCommand` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteTaskDefinitions` | ✓ `simulator-aws/ecs.go:505::handleECSDeleteTaskDefinitions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.PutAccountSetting` | ✓ `simulator-aws/ecs_account.go:30::handleECSPutAccountSetting` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.PutAccountSettingDefault` | ✓ `simulator-aws/ecs_account.go:31::handleECSPutAccountSettingDefault` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteAccountSetting` | ✓ `simulator-aws/ecs_account.go:32::handleECSDeleteAccountSetting` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListAccountSettings` | ✓ `simulator-aws/ecs_account.go:33::handleECSListAccountSettings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.PutAttributes` | ✓ `simulator-aws/ecs_attributes.go:21::handleECSPutAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteAttributes` | ✓ `simulator-aws/ecs_attributes.go:22::handleECSDeleteAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListAttributes` | ✓ `simulator-aws/ecs_attributes.go:23::handleECSListAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.CreateCapacityProvider` | ✓ `simulator-aws/ecs_capacity.go:34::handleECSCreateCapacityProvider` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteCapacityProvider` | ✓ `simulator-aws/ecs_capacity.go:35::handleECSDeleteCapacityProvider` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateCapacityProvider` | ✓ `simulator-aws/ecs_capacity.go:36::handleECSUpdateCapacityProvider` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.RegisterContainerInstance` | ✓ `simulator-aws/ecs_container_instances.go:61::handleECSRegisterContainerInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeregisterContainerInstance` | ✓ `simulator-aws/ecs_container_instances.go:62::handleECSDeregisterContainerInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeContainerInstances` | ✓ `simulator-aws/ecs_container_instances.go:63::handleECSDescribeContainerInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListContainerInstances` | ✓ `simulator-aws/ecs_container_instances.go:64::handleECSListContainerInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateContainerInstancesState` | ✓ `simulator-aws/ecs_container_instances.go:65::handleECSUpdateContainerInstancesState` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateContainerAgent` | ✓ `simulator-aws/ecs_container_instances.go:66::handleECSUpdateContainerAgent` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.SubmitContainerStateChange` | ✓ `simulator-aws/ecs_container_instances.go:67::handleECSSubmitContainerStateChange` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.SubmitTaskStateChange` | ✓ `simulator-aws/ecs_container_instances.go:68::handleECSSubmitTaskStateChange` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.SubmitAttachmentStateChanges` | ✓ `simulator-aws/ecs_container_instances.go:69::handleECSSubmitAttachmentStateChanges` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DiscoverPollEndpoint` | ✓ `simulator-aws/ecs_container_instances.go:70::handleECSDiscoverPollEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.CreateDaemon` | ✓ `simulator-aws/ecs_daemons.go:89::handleECSCreateDaemon` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteDaemon` | ✓ `simulator-aws/ecs_daemons.go:90::handleECSDeleteDaemon` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeDaemon` | ✓ `simulator-aws/ecs_daemons.go:91::handleECSDescribeDaemon` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateDaemon` | ✓ `simulator-aws/ecs_daemons.go:92::handleECSUpdateDaemon` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListDaemons` | ✓ `simulator-aws/ecs_daemons.go:93::handleECSListDaemons` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeDaemonDeployments` | ✓ `simulator-aws/ecs_daemons.go:94::handleECSDescribeDaemonDeployments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListDaemonDeployments` | ✓ `simulator-aws/ecs_daemons.go:95::handleECSListDaemonDeployments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeDaemonRevisions` | ✓ `simulator-aws/ecs_daemons.go:96::handleECSDescribeDaemonRevisions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.RegisterDaemonTaskDefinition` | ✓ `simulator-aws/ecs_daemons.go:97::handleECSRegisterDaemonTaskDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteDaemonTaskDefinition` | ✓ `simulator-aws/ecs_daemons.go:98::handleECSDeleteDaemonTaskDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeDaemonTaskDefinition` | ✓ `simulator-aws/ecs_daemons.go:99::handleECSDescribeDaemonTaskDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListDaemonTaskDefinitions` | ✓ `simulator-aws/ecs_daemons.go:100::handleECSListDaemonTaskDefinitions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.CreateExpressGatewayService` | ✓ `simulator-aws/ecs_express.go:128::handleECSCreateExpressGatewayService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeExpressGatewayService` | ✓ `simulator-aws/ecs_express.go:129::handleECSDescribeExpressGatewayService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateExpressGatewayService` | ✓ `simulator-aws/ecs_express.go:130::handleECSUpdateExpressGatewayService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteExpressGatewayService` | ✓ `simulator-aws/ecs_express.go:131::handleECSDeleteExpressGatewayService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.CreateService` | ✓ `simulator-aws/ecs_service.go:96::handleECSCreateService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeServices` | ✓ `simulator-aws/ecs_service.go:97::handleECSDescribeServices` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListServices` | ✓ `simulator-aws/ecs_service.go:98::handleECSListServices` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateService` | ✓ `simulator-aws/ecs_service.go:99::handleECSUpdateService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteService` | ✓ `simulator-aws/ecs_service.go:100::handleECSDeleteService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.PutClusterCapacityProviders` | ✓ `simulator-aws/ecs_service.go:101::handleECSPutClusterCapacityProviders` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListClusters` | ✓ `simulator-aws/ecs_service.go:102::handleECSListClusters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListTaskDefinitions` | ✓ `simulator-aws/ecs_service.go:103::handleECSListTaskDefinitions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListTaskDefinitionFamilies` | ✓ `simulator-aws/ecs_service.go:104::handleECSListTaskDefinitionFamilies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeCapacityProviders` | ✓ `simulator-aws/ecs_service.go:105::handleECSDescribeCapacityProviders` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeServiceDeployments` | ✓ `simulator-aws/ecs_service_deployments.go:57::handleECSDescribeServiceDeployments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListServiceDeployments` | ✓ `simulator-aws/ecs_service_deployments.go:58::handleECSListServiceDeployments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeServiceRevisions` | ✓ `simulator-aws/ecs_service_deployments.go:59::handleECSDescribeServiceRevisions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.StopServiceDeployment` | ✓ `simulator-aws/ecs_service_deployments.go:60::handleECSStopServiceDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ContinueServiceDeployment` | ✓ `simulator-aws/ecs_service_deployments.go:61::handleECSContinueServiceDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListServicesByNamespace` | ✓ `simulator-aws/ecs_service_deployments.go:62::handleECSListServicesByNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.StartTask` | ✓ `simulator-aws/ecs_start_task.go:19::handleECSStartTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.GetTaskProtection` | ✓ `simulator-aws/ecs_task_protection.go:29::handleECSGetTaskProtection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateTaskProtection` | ✓ `simulator-aws/ecs_task_protection.go:30::handleECSUpdateTaskProtection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.CreateTaskSet` | ✓ `simulator-aws/ecs_tasksets.go:52::handleECSCreateTaskSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteTaskSet` | ✓ `simulator-aws/ecs_tasksets.go:53::handleECSDeleteTaskSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeTaskSets` | ✓ `simulator-aws/ecs_tasksets.go:54::handleECSDescribeTaskSets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateTaskSet` | ✓ `simulator-aws/ecs_tasksets.go:55::handleECSUpdateTaskSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateServicePrimaryTaskSet` | ✓ `simulator-aws/ecs_tasksets.go:56::handleECSUpdateServicePrimaryTaskSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->

## ECS Express Mode (Express Gateway services)

The four Express Gateway operations are registered in `simulator-aws/ecs_express.go`
(`CreateExpressGatewayService`, `DescribeExpressGatewayService`,
`UpdateExpressGatewayService`, `DeleteExpressGatewayService`). Each composes the real
backing resources (ECS Fargate service, ELBv2 ALB/target-group/listener, ACM cert, EC2
security group, Application Auto Scaling target + policy) so they are describable through
their own service slices. See [`docs/ECS_EXPRESS_MODE.md`](https://github.com/e6qu/sockerless/blob/main/docs/ECS_EXPRESS_MODE.md)
for the full API, the Express-vs-vanilla-ECS comparison, and the assembly details.

<!-- HAND-WRITTEN END -->
