# Azure Service Bus — message data plane

Surface: `simulator-azure/servicebus_dataplane.go` and `simulator-azure/servicebus_amqp.go` via the `{namespace}.servicebus.<sim-host>` subdomain wrapper and the raw Service Bus AMQP/TLS listener.

This is the Service Bus message data plane. The simulator supports the REST message protocol, raw AMQP 1.0 over TLS used by the official `github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus` client's default transport, and AMQP 1.0 over WebSocket for clients that explicitly select the WebSocket transport.

## Status legend

- ✓ — implemented + tested
- ✗ — missing
- n/a — not a canonical client surface for this protocol in the repo harness

## REST message protocol

| Operation | Verb + path | sim handler | sdk-test | raw-wire test | notes |
|---|---|---|---|---|---|
| Send queue message | `POST /{queue}/messages` | ✓ `handleSBSendMessage` | n/a | ✓ `TestServiceBus_QueueRESTRoundTrip` | Stores raw body and BrokerProperties metadata. |
| ReceiveAndDelete queue head | `DELETE /{queue}/messages/head` | ✓ `handleSBReceiveAndDelete` | n/a | ✓ `TestServiceBus_QueueRESTRoundTrip` | Returns 200 + body or 204 when empty. |
| PeekLock queue head | `POST /{queue}/messages/head` | ✓ `handleSBPeekLock` | n/a | ✓ `TestServiceBus_PeekLockComplete` | Emits lock token and Location. |
| Complete queue lock | `DELETE /{queue}/messages/{messageID}/{lockToken}` | ✓ `handleSBCompleteLock` | n/a | ✓ `TestServiceBus_PeekLockComplete` | Removes the locked message. |
| Send topic/subscription message | `POST /{topic}/subscriptions/{subscription}/messages` | ✓ `handleSBSendMessage` | n/a | ✓ `TestServiceBus_TopicSubscriptionRoundTrip` | Stores under the subscription path. |
| ReceiveAndDelete subscription head | `DELETE /{topic}/subscriptions/{subscription}/messages/head` | ✓ `handleSBReceiveAndDelete` | n/a | ✓ `TestServiceBus_TopicSubscriptionRoundTrip` | Returns 200 + body or 204 when empty. |

## Raw AMQP-over-TCP/TLS protocol

| Operation | Protocol path | sim handler | sdk-test | notes |
|---|---|---|---|---|
| TLS listener | `SIM_SERVICEBUS_AMQP_LISTEN_ADDR` | ✓ `startSBAMQPTLSListener` | ✓ `TestServiceBus_RawAMQPSDKQueueSendReceive`, `TestServiceBus_RawAMQPSDKTopicSubscriptionSendReceive` | Requires a TLS cert/key and accepts raw AMQP frames on the configured address. |
| Namespace resolution | TLS SNI or AMQP Open hostname | ✓ `sbAMQPNamespaceFromHost` / `sbAMQPConn.handleFrame` | ✓ `TestServiceBus_RawAMQPSDKQueueSendReceive`, `TestServiceBus_RawAMQPSDKTopicSubscriptionSendReceive` | Uses protocol-visible host data instead of an HTTP Host header. |
| SASL anonymous negotiation | AMQP SASL frames | ✓ `sbAMQPConn.handleProto` / `handleFrame` | ✓ `TestServiceBus_RawAMQPSDKQueueSendReceive`, `TestServiceBus_RawAMQPSDKTopicSubscriptionSendReceive` | Matches the SDK connection-string path. |
| CBS claim RPC | `$cbs` sender/receiver links | ✓ `sbAMQPConn.respondRPC` | ✓ `TestServiceBus_RawAMQPSDKQueueSendReceive`, `TestServiceBus_RawAMQPSDKTopicSubscriptionSendReceive` | Accepts token claims and returns correlated status responses. |
| Queue sender link | AMQP target `{queue}` | ✓ `sbAMQPConn.handleTransfer` | ✓ `TestServiceBus_RawAMQPSDKQueueSendReceive` | Grants link credit and accepts sent messages. |
| Queue receive-and-delete link | AMQP source `{queue}` | ✓ `sbAMQPConn.handleFlow` | ✓ `TestServiceBus_RawAMQPSDKQueueSendReceive` | Sends settled message transfers from simulator queue state. |
| Topic sender link | AMQP target `{topic}` | ✓ `sbAMQPConn.handleTransfer` | ✓ `TestServiceBus_RawAMQPSDKTopicSubscriptionSendReceive` | Fans sent messages out to existing simulator subscriptions for the topic. |
| Subscription receive-and-delete link | AMQP source `{topic}/Subscriptions/{subscription}` | ✓ `sbAMQPConn.handleFlow` | ✓ `TestServiceBus_RawAMQPSDKTopicSubscriptionSendReceive` | Normalizes the SDK subscription path to simulator subscription queue state. |
| Management link open | AMQP target/source `{entity}/$management` | ✓ attach + CBS negotiation | ✓ `TestServiceBus_RawAMQPSDKQueueSendReceive`, `TestServiceBus_RawAMQPSDKTopicSubscriptionSendReceive` | Opened by the SDK during sender/receiver initialization; operation RPCs are not needed for the covered Send/Receive flow. |

## AMQP-over-WebSocket protocol

| Operation | Protocol path | sim handler | sdk-test | notes |
|---|---|---|---|---|
| WebSocket upgrade | `/$servicebus/websocket` | ✓ `handleSBAMQPWebSocket` | ✓ `TestServiceBus_AMQPSDKQueueSendReceive`, `TestServiceBus_AMQPSDKTopicSubscriptionSendReceive` | Uses subprotocol `amqp`. |
| SASL anonymous negotiation | AMQP SASL frames | ✓ `sbAMQPConn.handleProto` / `handleFrame` | ✓ `TestServiceBus_AMQPSDKQueueSendReceive`, `TestServiceBus_AMQPSDKTopicSubscriptionSendReceive` | Matches the SDK connection-string path. |
| CBS claim RPC | `$cbs` sender/receiver links | ✓ `sbAMQPConn.respondRPC` | ✓ `TestServiceBus_AMQPSDKQueueSendReceive`, `TestServiceBus_AMQPSDKTopicSubscriptionSendReceive` | Accepts token claims and returns correlated status responses. |
| Queue sender link | AMQP target `{queue}` | ✓ `sbAMQPConn.handleTransfer` | ✓ `TestServiceBus_AMQPSDKQueueSendReceive` | Grants link credit and accepts sent messages. |
| Queue receive-and-delete link | AMQP source `{queue}` | ✓ `sbAMQPConn.handleFlow` | ✓ `TestServiceBus_AMQPSDKQueueSendReceive` | Sends settled message transfers from simulator queue state. |
| Topic sender link | AMQP target `{topic}` | ✓ `sbAMQPConn.handleTransfer` | ✓ `TestServiceBus_AMQPSDKTopicSubscriptionSendReceive` | Fans sent messages out to existing simulator subscriptions for the topic. |
| Subscription receive-and-delete link | AMQP source `{topic}/Subscriptions/{subscription}` | ✓ `sbAMQPConn.handleFlow` | ✓ `TestServiceBus_AMQPSDKTopicSubscriptionSendReceive` | Normalizes the SDK subscription path to simulator subscription queue state. |
| Management link open | AMQP target/source `{entity}/$management` | ✓ attach + CBS negotiation | ✓ `TestServiceBus_AMQPSDKQueueSendReceive`, `TestServiceBus_AMQPSDKTopicSubscriptionSendReceive` | Opened by the SDK during sender/receiver initialization; operation RPCs are not needed for the covered Send/Receive flow. |

## Follow-up audit note

Issues #228 and #230 confirmed that REST coverage alone is not enough for Service Bus message flows because the official Go SDK uses AMQP 1.0 by default, and WebSocket-only AMQP leaks simulator transport plumbing into callers. Future Service Bus data-plane extensions should add AMQP rows here first, then cover the canonical SDK call path that exercises them.
