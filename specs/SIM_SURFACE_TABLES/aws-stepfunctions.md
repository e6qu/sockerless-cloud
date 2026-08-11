# Sim surface — aws-stepfunctions

Surface registered in `simulator-aws/stepfunctions.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `Action AWSStepFunctions.CreateStateMachine` | ✓ `simulator-aws/stepfunctions.go:98::handleSFNCreateStateMachine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DescribeStateMachine` | ✓ `simulator-aws/stepfunctions.go:99::handleSFNDescribeStateMachine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ListStateMachines` | ✓ `simulator-aws/stepfunctions.go:100::handleSFNListStateMachines` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DeleteStateMachine` | ✓ `simulator-aws/stepfunctions.go:101::handleSFNDeleteStateMachine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.UpdateStateMachine` | ✓ `simulator-aws/stepfunctions.go:102::handleSFNUpdateStateMachine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.TagResource` | ✓ `simulator-aws/stepfunctions.go:103::handleSFNTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.UntagResource` | ✓ `simulator-aws/stepfunctions.go:104::handleSFNUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ListTagsForResource` | ✓ `simulator-aws/stepfunctions.go:105::handleSFNListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ValidateStateMachineDefinition` | ✓ `simulator-aws/stepfunctions.go:106::handleSFNValidateStateMachineDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ListStateMachineVersions` | ✓ `simulator-aws/stepfunctions.go:107::handleSFNListStateMachineVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.StartExecution` | ✓ `simulator-aws/stepfunctions.go:108::handleSFNStartExecution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DescribeExecution` | ✓ `simulator-aws/stepfunctions.go:109::handleSFNDescribeExecution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.GetExecutionHistory` | ✓ `simulator-aws/stepfunctions.go:110::handleSFNGetExecutionHistory` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ListExecutions` | ✓ `simulator-aws/stepfunctions.go:111::handleSFNListExecutions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.StopExecution` | ✓ `simulator-aws/stepfunctions.go:112::handleSFNStopExecution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.CreateActivity` | ✓ `simulator-aws/stepfunctions.go:118::handleSFNCreateActivity` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DeleteActivity` | ✓ `simulator-aws/stepfunctions.go:119::handleSFNDeleteActivity` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DescribeActivity` | ✓ `simulator-aws/stepfunctions.go:120::handleSFNDescribeActivity` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ListActivities` | ✓ `simulator-aws/stepfunctions.go:121::handleSFNListActivities` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.GetActivityTask` | ✓ `simulator-aws/stepfunctions.go:122::handleSFNGetActivityTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.SendTaskSuccess` | ✓ `simulator-aws/stepfunctions.go:123::handleSFNSendTaskSuccess` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.SendTaskFailure` | ✓ `simulator-aws/stepfunctions.go:124::handleSFNSendTaskFailure` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.SendTaskHeartbeat` | ✓ `simulator-aws/stepfunctions.go:125::handleSFNSendTaskHeartbeat` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.PublishStateMachineVersion` | ✓ `simulator-aws/stepfunctions.go:131::handleSFNPublishStateMachineVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DeleteStateMachineVersion` | ✓ `simulator-aws/stepfunctions.go:132::handleSFNDeleteStateMachineVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.CreateStateMachineAlias` | ✓ `simulator-aws/stepfunctions.go:133::handleSFNCreateStateMachineAlias` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DeleteStateMachineAlias` | ✓ `simulator-aws/stepfunctions.go:134::handleSFNDeleteStateMachineAlias` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DescribeStateMachineAlias` | ✓ `simulator-aws/stepfunctions.go:135::handleSFNDescribeStateMachineAlias` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ListStateMachineAliases` | ✓ `simulator-aws/stepfunctions.go:136::handleSFNListStateMachineAliases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.UpdateStateMachineAlias` | ✓ `simulator-aws/stepfunctions.go:137::handleSFNUpdateStateMachineAlias` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DescribeStateMachineForExecution` | ✓ `simulator-aws/stepfunctions.go:140::handleSFNDescribeStateMachineForExecution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.RedriveExecution` | ✓ `simulator-aws/stepfunctions.go:141::handleSFNRedriveExecution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DescribeMapRun` | ✓ `simulator-aws/stepfunctions.go:143::handleSFNDescribeMapRun` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ListMapRuns` | ✓ `simulator-aws/stepfunctions.go:144::handleSFNListMapRuns` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.UpdateMapRun` | ✓ `simulator-aws/stepfunctions.go:145::handleSFNUpdateMapRun` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.TestState` | ✓ `simulator-aws/stepfunctions.go:148::handleSFNTestState` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.StartSyncExecution` | ✓ `simulator-aws/stepfunctions.go:149::handleSFNStartSyncExecution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
