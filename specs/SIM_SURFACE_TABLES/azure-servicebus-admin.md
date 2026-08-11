# Azure Service Bus — namespace admin data plane

Surface: `simulator-azure/servicebus_admin.go` via the `{namespace}.servicebus.<sim-host>` subdomain wrapper in `servicebus_dataplane.go`.

This is the namespace-level ATOM XML admin protocol used by `github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus/admin`. It is separate from the ARM management surface in `simulator-azure/servicebus.go` and from the REST message data plane in `simulator-azure/servicebus_dataplane.go`.

## Status legend

- ✓ — implemented + tested
- ✗ — missing
- n/a — not a canonical client surface for this protocol in the repo harness

## Queue admin

| Operation | Verb + path | sim handler | sdk-test | cli-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|---|---|
| CreateQueue | `PUT /{queue}?api-version=2021-05` | ✓ `handleSBAdminPutEntity` | ✓ `TestServiceBusAdmin_QueueSDKLifecycle` | n/a | n/a | n/a | ATOM entry with `QueueDescription`. |
| GetQueue | `GET /{queue}?api-version=2021-05` | ✓ `handleSBAdminGetEntity` | ✓ `TestServiceBusAdmin_QueueSDKLifecycle` | n/a | n/a | n/a | Missing queue returns empty ATOM feed so the SDK maps it to `nil, nil`. |
| DeleteQueue | `DELETE /{queue}?api-version=2021-05` | ✓ `handleSBAdminDeleteEntity` | ✓ `TestServiceBusAdmin_QueueSDKLifecycle` | n/a | n/a | n/a | |
| ListQueues | `GET /$Resources/Queues?api-version=2021-05` | ✓ `handleSBAdminListQueues` | ✓ `TestServiceBusAdmin_QueueSDKLifecycle` + `TestServiceBusAdmin_QueueListPagingIsNamespaceScoped` | n/a | n/a | ✓ | Filters by namespace before applying `$skip` / `$top`. |

## Topic admin

| Operation | Verb + path | sim handler | sdk-test | cli-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|---|---|
| CreateTopic | `PUT /{topic}?api-version=2021-05` | ✓ `handleSBAdminPutEntity` | ✓ `TestServiceBusAdmin_TopicSubscriptionRuleSDKLifecycle` | n/a | n/a | n/a | ATOM entry with `TopicDescription`. |
| GetTopic | `GET /{topic}?api-version=2021-05` | ✓ `handleSBAdminGetEntity` | ✓ `TestServiceBusAdmin_TopicSubscriptionRuleSDKLifecycle` | n/a | n/a | n/a | Missing topic returns empty ATOM feed. |
| DeleteTopic | `DELETE /{topic}?api-version=2021-05` | ✓ `handleSBAdminDeleteEntity` | ✓ `TestServiceBusAdmin_TopicSubscriptionRuleSDKLifecycle` | n/a | n/a | n/a | Cascades subscriptions and rules under the topic. |
| ListTopics | `GET /$Resources/Topics?api-version=2021-05` | ✓ `handleSBAdminListTopics` | ✓ `TestServiceBusAdmin_TopicSubscriptionRuleSDKLifecycle` | n/a | n/a | ✓ | Filters by namespace before paging. |

## Subscription admin

| Operation | Verb + path | sim handler | sdk-test | cli-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|---|---|
| CreateSubscription | `PUT /{topic}/Subscriptions/{subscription}?api-version=2021-05` | ✓ `handleSBAdminPutSubscription` | ✓ `TestServiceBusAdmin_TopicSubscriptionRuleSDKLifecycle` | n/a | n/a | n/a | Auto-creates the `$Default` rule, matching the Service Bus admin model. |
| GetSubscription | `GET /{topic}/Subscriptions/{subscription}?api-version=2021-05` | ✓ `handleSBAdminGetSubscription` | ✓ `TestServiceBusAdmin_TopicSubscriptionRuleSDKLifecycle` | n/a | n/a | n/a | Missing subscription returns empty ATOM feed. |
| DeleteSubscription | `DELETE /{topic}/Subscriptions/{subscription}?api-version=2021-05` | ✓ `handleSBAdminDeleteSubscription` | ✓ `TestServiceBusAdmin_TopicSubscriptionRuleSDKLifecycle` | n/a | n/a | n/a | Cascades rules under the subscription. |
| ListSubscriptions | `GET /{topic}/Subscriptions?api-version=2021-05` | ✓ `handleSBAdminListSubscriptions` | ✓ `TestServiceBusAdmin_TopicSubscriptionRuleSDKLifecycle` | n/a | n/a | ✓ | Filters by topic before paging. |

## Rule admin

| Operation | Verb + path | sim handler | sdk-test | cli-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|---|---|
| CreateRule | `PUT /{topic}/Subscriptions/{subscription}/Rules/{rule}?api-version=2021-05` | ✓ `handleSBAdminPutRule` | ✓ `TestServiceBusAdmin_TopicSubscriptionRuleSDKLifecycle` | n/a | n/a | n/a | Supports SQL filter payloads used by the official SDK. |
| GetRule | `GET /{topic}/Subscriptions/{subscription}/Rules/{rule}?api-version=2021-05` | ✓ `handleSBAdminGetRule` | ✓ `TestServiceBusAdmin_TopicSubscriptionRuleSDKLifecycle` | n/a | n/a | n/a | Missing rule returns empty ATOM feed. |
| DeleteRule | `DELETE /{topic}/Subscriptions/{subscription}/Rules/{rule}?api-version=2021-05` | ✓ `handleSBAdminDeleteRule` | ✓ `TestServiceBusAdmin_TopicSubscriptionRuleSDKLifecycle` | n/a | n/a | n/a | |
| ListRules | `GET /{topic}/Subscriptions/{subscription}/Rules?api-version=2021-05` | ✓ `handleSBAdminListRules` | ✓ `TestServiceBusAdmin_TopicSubscriptionRuleSDKLifecycle` | n/a | n/a | ✓ | Includes `$Default` plus user-created rules; filters by subscription before paging. |

## Follow-up audit note

Issue #223 exposed a systematic blind spot: service-native SDKs can bypass ARM/control-plane tables and speak a namespace or host-scoped data-plane protocol that the route seeder does not enumerate from plain `HandleFunc` registrations. The next protocol-surface audit should explicitly check host-wrapper dispatchers and service-native SDK admin clients before declaring an ARM-managed service complete.
