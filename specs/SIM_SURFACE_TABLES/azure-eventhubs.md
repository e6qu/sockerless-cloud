# Azure Event Hubs — ARM and AMQP data plane

Surface: `simulator-azure/eventhub.go` for ARM control-plane routes, plus `simulator-azure/servicebus_amqp.go` for Event Hubs AMQP send/receive over the shared raw AMQP/TLS listener.

## Status legend

- ✓ — implemented + tested
- n/a — not a canonical client surface for this operation in the repo harness

## ARM control plane

| Operation | Verb + path | sim handler | sdk-test | cli-test | tf-test | notes |
|---|---|---|---|---|---|---|
| Namespace create/update | `PUT .../Microsoft.EventHub/namespaces/{name}` | ✓ `handleEHCreateNamespace` | ✓ `TestEventHubsSDK_ARMAndAMQPRoundTrip` | ✓ `TestEventHubsCLI_ARMResources` | ✓ `azurerm_eventhub_namespace.az_eh_ns` | Returns Azure-shaped namespace defaults and simulator-derived `serviceBusEndpoint`. |
| Namespace get | `GET .../Microsoft.EventHub/namespaces/{name}` | ✓ `handleEHGetNamespace` | ✓ `TestEventHubsSDK_ARMAndAMQPRoundTrip` | n/a | ✓ `azurerm_eventhub_namespace.az_eh_ns` | Provider read path includes default fields such as throughput units and TLS/public network settings. |
| Namespace list by resource group | `GET .../Microsoft.EventHub/namespaces` | ✓ `handleEHListNamespacesByRG` | n/a | n/a | n/a | Lists namespaces under the resource group. |
| Namespace delete | `DELETE .../Microsoft.EventHub/namespaces/{name}` | ✓ `handleEHDeleteNamespace` | ✓ `TestEventHubsSDK_ARMAndAMQPRoundTrip` | ✓ `TestEventHubsCLI_ARMResources` | ✓ `azurerm_eventhub_namespace.az_eh_ns` | Cascades stored event hubs, consumer groups, auth rules, and partition events. |
| Namespace network rule set get | `GET .../namespaces/{name}/networkRuleSets/default` | ✓ `handleEHGetNamespaceNetworkRuleSet` | n/a | n/a | ✓ `azurerm_eventhub_namespace.az_eh_ns` | Returns the default rule-set shape read by azurerm. |
| Namespace auth rule create/update | `PUT .../namespaces/{name}/authorizationRules/{rule}` | ✓ `ehAuthRuleCreate` | n/a | n/a | n/a | Persists rights and keys. |
| Namespace auth rule get/list/delete | `GET/DELETE .../namespaces/{name}/authorizationRules...` | ✓ `ehAuthRuleGet` / `ehAuthRuleList` / `ehAuthRuleDelete` | n/a | n/a | n/a | Covers namespace-level authorization rules. |
| Namespace auth rule list keys | `POST .../authorizationRules/{rule}/listKeys` | ✓ `ehAuthRuleListKeys` | n/a | ✓ `TestEventHubsCLI_ARMResources` | n/a | Returns key name, keys, and Event Hubs connection strings. |
| Namespace auth rule regenerate keys | `POST .../authorizationRules/{rule}/regenerateKeys` | ✓ `ehAuthRuleRegenerateKeys` | n/a | n/a | n/a | Rotates the selected key while preserving the rule. |
| Event hub create/update | `PUT .../namespaces/{name}/eventhubs/{eventhub}` | ✓ `handleEHCreateEventHub` | ✓ `TestEventHubsSDK_ARMAndAMQPRoundTrip` | ✓ `TestEventHubsCLI_ARMResources` | ✓ `azurerm_eventhub.az_eh` | Creates partition IDs and the default consumer group. |
| Event hub get/list/delete | `GET/DELETE .../eventhubs...` | ✓ `handleEHGetEventHub` / `handleEHListEventHubs` / `handleEHDeleteEventHub` | ✓ `TestEventHubsSDK_ARMAndAMQPRoundTrip` | n/a | ✓ `azurerm_eventhub.az_eh` | Returns partition and retention metadata. |
| Event hub auth rule lifecycle | `PUT/GET/DELETE/LIST .../eventhubs/{eventhub}/authorizationRules...` | ✓ `ehAuthRuleCreate` / `ehAuthRuleGet` / `ehAuthRuleList` / `ehAuthRuleDelete` | n/a | n/a | ✓ `azurerm_eventhub_authorization_rule.az_eh_rule` | Covers event-hub-scoped rules. |
| Event hub auth rule list/regenerate keys | `POST .../eventhubs/{eventhub}/authorizationRules/{rule}/listKeys|regenerateKeys` | ✓ `ehAuthRuleListKeys` / `ehAuthRuleRegenerateKeys` | n/a | n/a | ✓ `azurerm_eventhub_authorization_rule.az_eh_rule` | Returns scoped connection strings. |
| Consumer group lifecycle | `PUT/GET/DELETE/LIST .../eventhubs/{eventhub}/consumerGroups...` | ✓ `handleEHCreateConsumerGroup` / `handleEHGetConsumerGroup` / `handleEHListConsumerGroups` / `handleEHDeleteConsumerGroup` | ✓ `TestEventHubsSDK_ARMAndAMQPRoundTrip` | ✓ `TestEventHubsCLI_ARMResources` | ✓ `azurerm_eventhub_consumer_group.az_eh_cg` | Both `consumerGroups` and SDK-observed `consumergroups` path spellings route to the same public resource. |

## AMQP data plane

| Operation | Protocol path | sim handler | sdk-test | notes |
|---|---|---|---|---|
| Raw AMQP/TLS listener | `SIM_SERVICEBUS_AMQP_LISTEN_ADDR` | ✓ `startSBAMQPTLSListener` | ✓ `TestEventHubsSDK_ARMAndAMQPRoundTrip` | Event Hubs shares the raw AMQP/TLS listener used by Service Bus, with namespace routing from the connection host. |
| Event Hubs management read | AMQP `$management` READ for `com.microsoft:eventhub` and `com.microsoft:partition` | ✓ `ehAMQPHandleRPC` | ✓ `TestEventHubsSDK_ARMAndAMQPRoundTrip` | Returns event hub properties and partition metadata expected by `azeventhubs/v2`. |
| Producer send | AMQP target `{eventhub}` or `{eventhub}/Partitions/{partition}` | ✓ `ehAMQPEnqueue` | ✓ `TestEventHubsSDK_ARMAndAMQPRoundTrip` | Stores sent event payloads in the selected partition stream. |
| Consumer receive | AMQP source `{eventhub}/ConsumerGroups/{group}/Partitions/{partition}` | ✓ `ehAMQPNextEvent` via `handleFlow` | ✓ `TestEventHubsSDK_ARMAndAMQPRoundTrip` | Sends stored events to receivers without removing them from the partition stream. |

## Follow-up audit note

This table records the foundational Event Hubs slice added for stream-ingestion parity. Future Event Hubs expansions should keep the public ARM and AMQP contracts aligned with the official SDK and provider surfaces rather than adding simulator-specific transport knobs.
