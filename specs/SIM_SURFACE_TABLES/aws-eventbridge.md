# Sim surface — aws-eventbridge

Surface registered in `simulator-aws/eventbridge.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `Action AWSEvents.CreateEventBus` | ✓ `simulator-aws/eventbridge.go:124::handleEBCreateEventBus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeEventBus` | ✓ `simulator-aws/eventbridge.go:125::handleEBDescribeEventBus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListEventBuses` | ✓ `simulator-aws/eventbridge.go:126::handleEBListEventBuses` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeleteEventBus` | ✓ `simulator-aws/eventbridge.go:127::handleEBDeleteEventBus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.PutPermission` | ✓ `simulator-aws/eventbridge.go:128::handleEBPutPermission` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.RemovePermission` | ✓ `simulator-aws/eventbridge.go:129::handleEBRemovePermission` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.PutRule` | ✓ `simulator-aws/eventbridge.go:130::handleEBPutRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeRule` | ✓ `simulator-aws/eventbridge.go:131::handleEBDescribeRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListRules` | ✓ `simulator-aws/eventbridge.go:132::handleEBListRules` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListRuleNamesByTarget` | ✓ `simulator-aws/eventbridge.go:133::handleEBListRuleNamesByTarget` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.TestEventPattern` | ✓ `simulator-aws/eventbridge.go:134::handleEBTestEventPattern` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.UpdateEventBus` | ✓ `simulator-aws/eventbridge.go:135::handleEBUpdateEventBus` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeleteRule` | ✓ `simulator-aws/eventbridge.go:136::handleEBDeleteRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.EnableRule` | ✓ `simulator-aws/eventbridge.go:137::handleEBEnableRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DisableRule` | ✓ `simulator-aws/eventbridge.go:138::handleEBDisableRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.PutTargets` | ✓ `simulator-aws/eventbridge.go:139::handleEBPutTargets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListTargetsByRule` | ✓ `simulator-aws/eventbridge.go:140::handleEBListTargetsByRule` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.RemoveTargets` | ✓ `simulator-aws/eventbridge.go:141::handleEBRemoveTargets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.PutEvents` | ✓ `simulator-aws/eventbridge.go:142::handleEBPutEvents` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.TagResource` | ✓ `simulator-aws/eventbridge.go:143::handleEBTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.UntagResource` | ✓ `simulator-aws/eventbridge.go:144::handleEBUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListTagsForResource` | ✓ `simulator-aws/eventbridge.go:145::handleEBListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.CreateArchive` | ✓ `simulator-aws/eventbridge.go:146::handleEBCreateArchive` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeArchive` | ✓ `simulator-aws/eventbridge.go:147::handleEBDescribeArchive` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListArchives` | ✓ `simulator-aws/eventbridge.go:148::handleEBListArchives` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeleteArchive` | ✓ `simulator-aws/eventbridge.go:149::handleEBDeleteArchive` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.StartReplay` | ✓ `simulator-aws/eventbridge.go:150::handleEBStartReplay` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeReplay` | ✓ `simulator-aws/eventbridge.go:151::handleEBDescribeReplay` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListReplays` | ✓ `simulator-aws/eventbridge.go:152::handleEBListReplays` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.UpdateArchive` | ✓ `simulator-aws/eventbridge.go:153::handleEBUpdateArchive` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.CancelReplay` | ✓ `simulator-aws/eventbridge.go:154::handleEBCancelReplay` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.CreateApiDestination` | ✓ `simulator-aws/eventbridge_connectivity.go:102::handleEBCreateApiDestination` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeApiDestination` | ✓ `simulator-aws/eventbridge_connectivity.go:103::handleEBDescribeApiDestination` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListApiDestinations` | ✓ `simulator-aws/eventbridge_connectivity.go:104::handleEBListApiDestinations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.UpdateApiDestination` | ✓ `simulator-aws/eventbridge_connectivity.go:105::handleEBUpdateApiDestination` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeleteApiDestination` | ✓ `simulator-aws/eventbridge_connectivity.go:106::handleEBDeleteApiDestination` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.CreateConnection` | ✓ `simulator-aws/eventbridge_connectivity.go:108::handleEBCreateConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeConnection` | ✓ `simulator-aws/eventbridge_connectivity.go:109::handleEBDescribeConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListConnections` | ✓ `simulator-aws/eventbridge_connectivity.go:110::handleEBListConnections` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.UpdateConnection` | ✓ `simulator-aws/eventbridge_connectivity.go:111::handleEBUpdateConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeauthorizeConnection` | ✓ `simulator-aws/eventbridge_connectivity.go:112::handleEBDeauthorizeConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeleteConnection` | ✓ `simulator-aws/eventbridge_connectivity.go:113::handleEBDeleteConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.CreateEndpoint` | ✓ `simulator-aws/eventbridge_connectivity.go:115::handleEBCreateEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeEndpoint` | ✓ `simulator-aws/eventbridge_connectivity.go:116::handleEBDescribeEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListEndpoints` | ✓ `simulator-aws/eventbridge_connectivity.go:117::handleEBListEndpoints` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.UpdateEndpoint` | ✓ `simulator-aws/eventbridge_connectivity.go:118::handleEBUpdateEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeleteEndpoint` | ✓ `simulator-aws/eventbridge_connectivity.go:119::handleEBDeleteEndpoint` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.CreatePartnerEventSource` | ✓ `simulator-aws/eventbridge_connectivity.go:121::handleEBCreatePartnerEventSource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribePartnerEventSource` | ✓ `simulator-aws/eventbridge_connectivity.go:122::handleEBDescribePartnerEventSource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListPartnerEventSources` | ✓ `simulator-aws/eventbridge_connectivity.go:123::handleEBListPartnerEventSources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListPartnerEventSourceAccounts` | ✓ `simulator-aws/eventbridge_connectivity.go:124::handleEBListPartnerEventSourceAccounts` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeletePartnerEventSource` | ✓ `simulator-aws/eventbridge_connectivity.go:125::handleEBDeletePartnerEventSource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.PutPartnerEvents` | ✓ `simulator-aws/eventbridge_connectivity.go:126::handleEBPutPartnerEvents` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ActivateEventSource` | ✓ `simulator-aws/eventbridge_connectivity.go:128::handleEBActivateEventSource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DeactivateEventSource` | ✓ `simulator-aws/eventbridge_connectivity.go:129::handleEBDeactivateEventSource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.DescribeEventSource` | ✓ `simulator-aws/eventbridge_connectivity.go:130::handleEBDescribeEventSource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSEvents.ListEventSources` | ✓ `simulator-aws/eventbridge_connectivity.go:131::handleEBListEventSources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
