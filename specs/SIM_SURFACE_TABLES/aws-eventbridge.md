# Sim surface — aws-eventbridge

Surface registered in `simulator-aws/eventbridge.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action AWSEvents.CreateEventBus` | ✓ `simulator-aws/eventbridge.go:121::handleEBCreateEventBus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeEventBus` | ✓ `simulator-aws/eventbridge.go:122::handleEBDescribeEventBus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListEventBuses` | ✓ `simulator-aws/eventbridge.go:123::handleEBListEventBuses` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeleteEventBus` | ✓ `simulator-aws/eventbridge.go:124::handleEBDeleteEventBus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.PutPermission` | ✓ `simulator-aws/eventbridge.go:125::handleEBPutPermission` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.RemovePermission` | ✓ `simulator-aws/eventbridge.go:126::handleEBRemovePermission` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.PutRule` | ✓ `simulator-aws/eventbridge.go:127::handleEBPutRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeRule` | ✓ `simulator-aws/eventbridge.go:128::handleEBDescribeRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListRules` | ✓ `simulator-aws/eventbridge.go:129::handleEBListRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListRuleNamesByTarget` | ✓ `simulator-aws/eventbridge.go:130::handleEBListRuleNamesByTarget` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.TestEventPattern` | ○ `simulator-aws/eventbridge.go:131::handleEBTestEventPattern` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.UpdateEventBus` | ✓ `simulator-aws/eventbridge.go:132::handleEBUpdateEventBus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeleteRule` | ✓ `simulator-aws/eventbridge.go:133::handleEBDeleteRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.EnableRule` | ✓ `simulator-aws/eventbridge.go:134::handleEBEnableRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DisableRule` | ✓ `simulator-aws/eventbridge.go:135::handleEBDisableRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.PutTargets` | ✓ `simulator-aws/eventbridge.go:136::handleEBPutTargets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListTargetsByRule` | ✓ `simulator-aws/eventbridge.go:137::handleEBListTargetsByRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.RemoveTargets` | ✓ `simulator-aws/eventbridge.go:138::handleEBRemoveTargets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.PutEvents` | ✓ `simulator-aws/eventbridge.go:139::handleEBPutEvents` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.TagResource` | ✓ `simulator-aws/eventbridge.go:140::handleEBTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.UntagResource` | ✓ `simulator-aws/eventbridge.go:141::handleEBUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListTagsForResource` | ✓ `simulator-aws/eventbridge.go:142::handleEBListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.CreateArchive` | ✓ `simulator-aws/eventbridge.go:143::handleEBCreateArchive` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeArchive` | ✓ `simulator-aws/eventbridge.go:144::handleEBDescribeArchive` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListArchives` | ✓ `simulator-aws/eventbridge.go:145::handleEBListArchives` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeleteArchive` | ✓ `simulator-aws/eventbridge.go:146::handleEBDeleteArchive` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.StartReplay` | ✓ `simulator-aws/eventbridge.go:147::handleEBStartReplay` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeReplay` | ✓ `simulator-aws/eventbridge.go:148::handleEBDescribeReplay` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListReplays` | ✓ `simulator-aws/eventbridge.go:149::handleEBListReplays` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.UpdateArchive` | ✓ `simulator-aws/eventbridge.go:150::handleEBUpdateArchive` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.CancelReplay` | ✓ `simulator-aws/eventbridge.go:151::handleEBCancelReplay` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListConnections` | ✓ `simulator-aws/eventbridge_connectivity.go:100::handleEBListConnections` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.UpdateConnection` | ✓ `simulator-aws/eventbridge_connectivity.go:101::handleEBUpdateConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeauthorizeConnection` | ✓ `simulator-aws/eventbridge_connectivity.go:102::handleEBDeauthorizeConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeleteConnection` | ✓ `simulator-aws/eventbridge_connectivity.go:103::handleEBDeleteConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.CreateEndpoint` | ✓ `simulator-aws/eventbridge_connectivity.go:105::handleEBCreateEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeEndpoint` | ✓ `simulator-aws/eventbridge_connectivity.go:106::handleEBDescribeEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListEndpoints` | ✓ `simulator-aws/eventbridge_connectivity.go:107::handleEBListEndpoints` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.UpdateEndpoint` | ✓ `simulator-aws/eventbridge_connectivity.go:108::handleEBUpdateEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeleteEndpoint` | ✓ `simulator-aws/eventbridge_connectivity.go:109::handleEBDeleteEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.CreatePartnerEventSource` | ✓ `simulator-aws/eventbridge_connectivity.go:111::handleEBCreatePartnerEventSource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribePartnerEventSource` | ✓ `simulator-aws/eventbridge_connectivity.go:112::handleEBDescribePartnerEventSource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListPartnerEventSources` | ✓ `simulator-aws/eventbridge_connectivity.go:113::handleEBListPartnerEventSources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListPartnerEventSourceAccounts` | ✓ `simulator-aws/eventbridge_connectivity.go:114::handleEBListPartnerEventSourceAccounts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeletePartnerEventSource` | ✓ `simulator-aws/eventbridge_connectivity.go:115::handleEBDeletePartnerEventSource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.PutPartnerEvents` | ○ `simulator-aws/eventbridge_connectivity.go:116::handleEBPutPartnerEvents` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ActivateEventSource` | ✓ `simulator-aws/eventbridge_connectivity.go:118::handleEBActivateEventSource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeactivateEventSource` | ✓ `simulator-aws/eventbridge_connectivity.go:119::handleEBDeactivateEventSource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeEventSource` | ✓ `simulator-aws/eventbridge_connectivity.go:120::handleEBDescribeEventSource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListEventSources` | ✓ `simulator-aws/eventbridge_connectivity.go:121::handleEBListEventSources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.CreateApiDestination` | ✓ `simulator-aws/eventbridge_connectivity.go:92::handleEBCreateApiDestination` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeApiDestination` | ✓ `simulator-aws/eventbridge_connectivity.go:93::handleEBDescribeApiDestination` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListApiDestinations` | ✓ `simulator-aws/eventbridge_connectivity.go:94::handleEBListApiDestinations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.UpdateApiDestination` | ✓ `simulator-aws/eventbridge_connectivity.go:95::handleEBUpdateApiDestination` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeleteApiDestination` | ✓ `simulator-aws/eventbridge_connectivity.go:96::handleEBDeleteApiDestination` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.CreateConnection` | ✓ `simulator-aws/eventbridge_connectivity.go:98::handleEBCreateConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeConnection` | ✓ `simulator-aws/eventbridge_connectivity.go:99::handleEBDescribeConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
