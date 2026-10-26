# Sim surface — aws-ecs

Surface registered in `simulator-aws/ecs.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action AmazonEC2ContainerServiceV20141113.CreateCluster` | ✓ `simulator-aws/ecs.go:551::handleECSCreateCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeClusters` | ✓ `simulator-aws/ecs.go:552::handleECSDescribeClusters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateCluster` | ✓ `simulator-aws/ecs.go:553::handleECSUpdateCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateClusterSettings` | ✓ `simulator-aws/ecs.go:554::handleECSUpdateClusterSettings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.RegisterTaskDefinition` | ✓ `simulator-aws/ecs.go:555::handleECSRegisterTaskDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeregisterTaskDefinition` | ✓ `simulator-aws/ecs.go:556::handleECSDeregisterTaskDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeTaskDefinition` | ✓ `simulator-aws/ecs.go:557::handleECSDescribeTaskDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.RunTask` | ✓ `simulator-aws/ecs.go:558::handleECSRunTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeTasks` | ✓ `simulator-aws/ecs.go:559::handleECSDescribeTasks` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.StopTask` | ✓ `simulator-aws/ecs.go:560::handleECSStopTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListTasks` | ✓ `simulator-aws/ecs.go:561::handleECSListTasks` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteCluster` | ✓ `simulator-aws/ecs.go:562::handleECSDeleteCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListTagsForResource` | ✓ `simulator-aws/ecs.go:563::handleECSListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.TagResource` | ✓ `simulator-aws/ecs.go:564::handleECSTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UntagResource` | ✓ `simulator-aws/ecs.go:565::handleECSUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ExecuteCommand` | ✓ `simulator-aws/ecs.go:566::srv` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteTaskDefinitions` | ✓ `simulator-aws/ecs.go:579::handleECSDeleteTaskDefinitions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /ecs-exec/{sessionId}` | ○ `simulator-aws/ecs.go:581::func` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.PutAccountSetting` | ✓ `simulator-aws/ecs_account.go:24::handleECSPutAccountSetting` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.PutAccountSettingDefault` | ✓ `simulator-aws/ecs_account.go:25::handleECSPutAccountSettingDefault` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteAccountSetting` | ✓ `simulator-aws/ecs_account.go:26::handleECSDeleteAccountSetting` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListAccountSettings` | ✓ `simulator-aws/ecs_account.go:27::handleECSListAccountSettings` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.PutAttributes` | ✓ `simulator-aws/ecs_attributes.go:16::handleECSPutAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteAttributes` | ✓ `simulator-aws/ecs_attributes.go:17::handleECSDeleteAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListAttributes` | ✓ `simulator-aws/ecs_attributes.go:18::handleECSListAttributes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.CreateCapacityProvider` | ✓ `simulator-aws/ecs_capacity.go:31::handleECSCreateCapacityProvider` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteCapacityProvider` | ✓ `simulator-aws/ecs_capacity.go:32::handleECSDeleteCapacityProvider` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateCapacityProvider` | ✓ `simulator-aws/ecs_capacity.go:33::handleECSUpdateCapacityProvider` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.RegisterContainerInstance` | ✓ `simulator-aws/ecs_container_instances.go:51::handleECSRegisterContainerInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeregisterContainerInstance` | ✓ `simulator-aws/ecs_container_instances.go:52::handleECSDeregisterContainerInstance` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeContainerInstances` | ✓ `simulator-aws/ecs_container_instances.go:53::handleECSDescribeContainerInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListContainerInstances` | ✓ `simulator-aws/ecs_container_instances.go:54::handleECSListContainerInstances` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateContainerInstancesState` | ✓ `simulator-aws/ecs_container_instances.go:55::handleECSUpdateContainerInstancesState` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateContainerAgent` | ✓ `simulator-aws/ecs_container_instances.go:56::handleECSUpdateContainerAgent` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.SubmitContainerStateChange` | ✓ `simulator-aws/ecs_container_instances.go:57::handleECSSubmitContainerStateChange` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.SubmitTaskStateChange` | ✓ `simulator-aws/ecs_container_instances.go:58::handleECSSubmitTaskStateChange` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.SubmitAttachmentStateChanges` | ✓ `simulator-aws/ecs_container_instances.go:59::handleECSSubmitAttachmentStateChanges` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DiscoverPollEndpoint` | ○ `simulator-aws/ecs_container_instances.go:60::handleECSDiscoverPollEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.CreateDaemon` | ✓ `simulator-aws/ecs_daemons.go:82::handleECSCreateDaemon` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteDaemon` | ✓ `simulator-aws/ecs_daemons.go:83::handleECSDeleteDaemon` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeDaemon` | ✓ `simulator-aws/ecs_daemons.go:84::handleECSDescribeDaemon` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateDaemon` | ✓ `simulator-aws/ecs_daemons.go:85::handleECSUpdateDaemon` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListDaemons` | ✓ `simulator-aws/ecs_daemons.go:86::handleECSListDaemons` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeDaemonDeployments` | ✓ `simulator-aws/ecs_daemons.go:87::handleECSDescribeDaemonDeployments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListDaemonDeployments` | ✓ `simulator-aws/ecs_daemons.go:88::handleECSListDaemonDeployments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeDaemonRevisions` | ✓ `simulator-aws/ecs_daemons.go:89::handleECSDescribeDaemonRevisions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.RegisterDaemonTaskDefinition` | ✓ `simulator-aws/ecs_daemons.go:90::handleECSRegisterDaemonTaskDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteDaemonTaskDefinition` | ✓ `simulator-aws/ecs_daemons.go:91::handleECSDeleteDaemonTaskDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeDaemonTaskDefinition` | ✓ `simulator-aws/ecs_daemons.go:92::handleECSDescribeDaemonTaskDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListDaemonTaskDefinitions` | ✓ `simulator-aws/ecs_daemons.go:93::handleECSListDaemonTaskDefinitions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.CreateExpressGatewayService` | ✓ `simulator-aws/ecs_express.go:176::handleECSCreateExpressGatewayService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeExpressGatewayService` | ✓ `simulator-aws/ecs_express.go:177::handleECSDescribeExpressGatewayService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateExpressGatewayService` | ✓ `simulator-aws/ecs_express.go:178::handleECSUpdateExpressGatewayService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteExpressGatewayService` | ✓ `simulator-aws/ecs_express.go:179::handleECSDeleteExpressGatewayService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.CreateGateway` | ✓ `simulator-aws/ecs_gateways.go:146::handleECSCreateGateway` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeGateway` | ✓ `simulator-aws/ecs_gateways.go:147::handleECSDescribeGateway` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListGateways` | ✓ `simulator-aws/ecs_gateways.go:148::handleECSListGateways` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateGateway` | ✓ `simulator-aws/ecs_gateways.go:149::handleECSUpdateGateway` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteGateway` | ✓ `simulator-aws/ecs_gateways.go:150::handleECSDeleteGateway` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.AssociateGatewayWithCluster` | ✓ `simulator-aws/ecs_gateways.go:151::handleECSAssociateGatewayWithCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DisassociateGatewayFromCluster` | ✓ `simulator-aws/ecs_gateways.go:152::handleECSDisassociateGatewayFromCluster` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListGatewayClusterAssociations` | ✓ `simulator-aws/ecs_gateways.go:153::handleECSListGatewayClusterAssociations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeGatewayResources` | ✓ `simulator-aws/ecs_gateways.go:154::handleECSDescribeGatewayResources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeGatewayRoute` | ✓ `simulator-aws/ecs_gateways.go:155::handleECSDescribeGatewayRoute` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListGatewayRoutes` | ✓ `simulator-aws/ecs_gateways.go:156::handleECSListGatewayRoutes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.CreateService` | ✓ `simulator-aws/ecs_service.go:86::handleECSCreateService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeServices` | ✓ `simulator-aws/ecs_service.go:87::handleECSDescribeServices` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListServices` | ✓ `simulator-aws/ecs_service.go:88::handleECSListServices` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateService` | ✓ `simulator-aws/ecs_service.go:89::handleECSUpdateService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteService` | ✓ `simulator-aws/ecs_service.go:90::handleECSDeleteService` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.PutClusterCapacityProviders` | ✓ `simulator-aws/ecs_service.go:91::handleECSPutClusterCapacityProviders` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListClusters` | ✓ `simulator-aws/ecs_service.go:92::handleECSListClusters` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListTaskDefinitions` | ○ `simulator-aws/ecs_service.go:93::handleECSListTaskDefinitions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListTaskDefinitionFamilies` | ○ `simulator-aws/ecs_service.go:94::handleECSListTaskDefinitionFamilies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeCapacityProviders` | ✓ `simulator-aws/ecs_service.go:95::handleECSDescribeCapacityProviders` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeServiceDeployments` | ✓ `simulator-aws/ecs_service_deployments.go:53::handleECSDescribeServiceDeployments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListServiceDeployments` | ✓ `simulator-aws/ecs_service_deployments.go:54::handleECSListServiceDeployments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeServiceRevisions` | ✓ `simulator-aws/ecs_service_deployments.go:55::handleECSDescribeServiceRevisions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.StopServiceDeployment` | ✓ `simulator-aws/ecs_service_deployments.go:56::handleECSStopServiceDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ContinueServiceDeployment` | ✓ `simulator-aws/ecs_service_deployments.go:57::handleECSContinueServiceDeployment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.ListServicesByNamespace` | ✓ `simulator-aws/ecs_service_deployments.go:58::handleECSListServicesByNamespace` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.StartTask` | ✓ `simulator-aws/ecs_start_task.go:13::handleECSStartTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.GetTaskProtection` | ✓ `simulator-aws/ecs_task_protection.go:25::handleECSGetTaskProtection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateTaskProtection` | ✓ `simulator-aws/ecs_task_protection.go:26::handleECSUpdateTaskProtection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.CreateTaskSet` | ✓ `simulator-aws/ecs_tasksets.go:47::handleECSCreateTaskSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DeleteTaskSet` | ✓ `simulator-aws/ecs_tasksets.go:48::handleECSDeleteTaskSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.DescribeTaskSets` | ✓ `simulator-aws/ecs_tasksets.go:49::handleECSDescribeTaskSets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateTaskSet` | ✓ `simulator-aws/ecs_tasksets.go:50::handleECSUpdateTaskSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AmazonEC2ContainerServiceV20141113.UpdateServicePrimaryTaskSet` | ✓ `simulator-aws/ecs_tasksets.go:51::handleECSUpdateServicePrimaryTaskSet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

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
their own service slices. See `docs/ECS_EXPRESS_MODE.md`
for the full API, the Express-vs-vanilla-ECS comparison, and the assembly details.

<!-- HAND-WRITTEN END -->
