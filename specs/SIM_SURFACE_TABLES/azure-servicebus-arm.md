# Azure Service Bus — ARM control plane

Surface: `simulator-azure/servicebus.go` for Microsoft.ServiceBus ARM management routes.

This is the ARM provider surface used by Terraform `azurerm_servicebus_*`, Azure CLI `az rest`, and the official `armservicebus` SDK. It is separate from the namespace-level ATOM admin protocol in `azure-servicebus-admin.md` and the Service Bus message data plane in `azure-servicebus-data-plane.md`.

## Status legend

- ✓ — implemented + tested
- n/a — not a canonical client surface for this operation in the repo harness

## ARM control plane

| Operation | Verb + path | sim handler | sdk-test | cli-test | tf-test | notes |
|---|---|---|---|---|---|---|
| Namespace create/update | `PUT .../Microsoft.ServiceBus/namespaces/{name}` | ✓ `handleSBCreateNamespace` | ✓ `TestServiceBusARM_SDKNetworkRuleSetsAndAdjunctReads` | ✓ `TestServiceBusCLI_ARMResources` | ✓ `azurerm_servicebus_namespace.az_sb_ns` | Returns Azure-shaped namespace defaults and simulator-derived `serviceBusEndpoint`; creates `RootManageSharedAccessKey`. |
| Namespace get | `GET .../Microsoft.ServiceBus/namespaces/{name}` | ✓ `handleSBGetNamespace` | ✓ `TestServiceBusARM_SDKNetworkRuleSetsAndAdjunctReads` | n/a | ✓ `azurerm_servicebus_namespace.az_sb_ns` | Provider read path gets the same persisted namespace resource. |
| Namespace list by resource group | `GET .../Microsoft.ServiceBus/namespaces` | ✓ `handleSBListNamespacesByRG` | n/a | n/a | n/a | Lists namespaces under the resource group. |
| Namespace delete | `DELETE .../Microsoft.ServiceBus/namespaces/{name}` | ✓ `handleSBDeleteNamespace` | ✓ `TestServiceBusARM_SDKNetworkRuleSetsAndAdjunctReads` | ✓ `TestServiceBusCLI_ARMResources` | ✓ `azurerm_servicebus_namespace.az_sb_ns` | Cascades queues, topics, subscriptions, rules, auth rules, and network rule sets. |
| Namespace network rule set get/list/update | `GET/PUT .../namespaces/{name}/networkRuleSets/default`, `GET .../networkRuleSets` | ✓ `handleSBGetNamespaceNetworkRuleSet` / `handleSBPutNamespaceNetworkRuleSet` / `handleSBListNamespaceNetworkRuleSets` | ✓ `TestServiceBusARM_SDKNetworkRuleSetsAndAdjunctReads` | ✓ `TestServiceBusCLI_ARMResources` | ✓ `azurerm_servicebus_namespace.az_sb_ns` | Returns and persists the default rule-set shape read by azurerm. |
| Disaster recovery config list | `GET .../namespaces/{name}/disasterRecoveryConfigs` | ✓ `handleSBListDisasterRecoveryConfigs` | ✓ `TestServiceBusARM_SDKNetworkRuleSetsAndAdjunctReads` | ✓ `TestServiceBusCLI_ARMResources` | n/a | Returns an empty list for namespaces with no alias configured. |
| Disaster recovery config get | `GET .../namespaces/{name}/disasterRecoveryConfigs/{alias}` | ✓ `handleSBGetDisasterRecoveryConfig` | n/a | n/a | n/a | Missing aliases return Azure-shaped 404. |
| Migration config list | `GET .../namespaces/{name}/migrationConfigurations` | ✓ `handleSBListMigrationConfigurations` | ✓ `TestServiceBusARM_SDKNetworkRuleSetsAndAdjunctReads` | ✓ `TestServiceBusCLI_ARMResources` | n/a | Returns an empty list for namespaces with no migration configured. |
| Migration config get | `GET .../namespaces/{name}/migrationConfigurations/{config}` | ✓ `handleSBGetMigrationConfiguration` | ✓ `TestServiceBusARM_SDKNetworkRuleSetsAndAdjunctReads` | n/a | n/a | Missing `$default` migration configuration returns Azure-shaped 404. |
| Namespace auth rule lifecycle | `PUT/GET/DELETE/LIST .../namespaces/{name}/authorizationRules...` | ✓ `sbAuthRuleCreate` / `sbAuthRuleGet` / `sbAuthRuleList` / `sbAuthRuleDelete` | n/a | n/a | n/a | Covers namespace-level SAS rules, including the auto-created root rule. |
| Namespace auth rule list/regenerate keys | `POST .../authorizationRules/{rule}/listKeys|regenerateKeys` | ✓ `sbAuthRuleListKeys` / `sbAuthRuleRegenerateKeys` | ✓ `TestAzureServiceBus_ARMLifecycle` | n/a | n/a | Returns key name, keys, and Service Bus connection strings. |
| Queue create/get/list/delete | `PUT/GET/DELETE .../namespaces/{name}/queues/{queue}`, `GET .../queues` | ✓ `handleSBCreateQueue` / `handleSBGetQueue` / `handleSBListQueues` / `handleSBDeleteQueue` | ✓ `TestServiceBusARM_SDKNetworkRuleSetsAndAdjunctReads` | ✓ `TestServiceBusCLI_ARMResources` | ✓ `azurerm_servicebus_queue.az_sb_queue` | Persists queue properties and active status. |
| Topic create/get/list/delete | `PUT/GET/DELETE .../namespaces/{name}/topics/{topic}`, `GET .../topics` | ✓ `handleSBCreateTopic` / `handleSBGetTopic` / `handleSBListTopics` / `handleSBDeleteTopic` | ✓ `TestAzureServiceBus_ARMLifecycle` | n/a | n/a | Cascades subscriptions and rules under the topic. |
| Subscription create/get/list/delete | `PUT/GET/DELETE .../topics/{topic}/subscriptions/{sub}`, `GET .../subscriptions` | ✓ `handleSBCreateSubscription` / `handleSBGetSubscription` / `handleSBListSubscriptions` / `handleSBDeleteSubscription` | ✓ `TestAzureServiceBus_ARMLifecycle` | n/a | n/a | Persists subscription properties under the topic. |
| Queue/topic authorization rules | `PUT/GET/DELETE/LIST .../queues|topics/.../authorizationRules...` | ✓ `sbAuthRuleCreate` / `sbAuthRuleGet` / `sbAuthRuleList` / `sbAuthRuleDelete` | n/a | n/a | n/a | Covers queue- and topic-scoped SAS rules. |

## Follow-up audit note

Issue #276 showed that provider reads can include adjunct ARM child resources immediately after core resource creation. Service Bus ARM coverage now includes the namespace network rule set plus empty disaster recovery and migration list resources so azurerm can complete namespace and queue lifecycle reads without simulator-specific paths.
