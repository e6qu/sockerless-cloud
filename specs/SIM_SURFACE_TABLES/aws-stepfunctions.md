# Sim surface — aws-stepfunctions

Surface registered in `simulator-aws/stepfunctions.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action AWSStepFunctions.DescribeStateMachine` | ✓ `simulator-aws/stepfunctions.go:100::handleSFNDescribeStateMachine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ListStateMachines` | ✓ `simulator-aws/stepfunctions.go:101::handleSFNListStateMachines` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DeleteStateMachine` | ✓ `simulator-aws/stepfunctions.go:102::handleSFNDeleteStateMachine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.UpdateStateMachine` | ✓ `simulator-aws/stepfunctions.go:103::handleSFNUpdateStateMachine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.TagResource` | ✓ `simulator-aws/stepfunctions.go:104::handleSFNTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.UntagResource` | ✓ `simulator-aws/stepfunctions.go:105::handleSFNUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ListTagsForResource` | ✓ `simulator-aws/stepfunctions.go:106::handleSFNListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ValidateStateMachineDefinition` | ○ `simulator-aws/stepfunctions.go:107::handleSFNValidateStateMachineDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ListStateMachineVersions` | ✓ `simulator-aws/stepfunctions.go:108::handleSFNListStateMachineVersions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.StartExecution` | ✓ `simulator-aws/stepfunctions.go:109::handleSFNStartExecution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DescribeExecution` | ✓ `simulator-aws/stepfunctions.go:110::handleSFNDescribeExecution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.GetExecutionHistory` | ✓ `simulator-aws/stepfunctions.go:111::handleSFNGetExecutionHistory` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ListExecutions` | ✓ `simulator-aws/stepfunctions.go:112::handleSFNListExecutions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.StopExecution` | ✓ `simulator-aws/stepfunctions.go:113::handleSFNStopExecution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.CreateActivity` | ✓ `simulator-aws/stepfunctions.go:119::handleSFNCreateActivity` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DeleteActivity` | ✓ `simulator-aws/stepfunctions.go:120::handleSFNDeleteActivity` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DescribeActivity` | ✓ `simulator-aws/stepfunctions.go:121::handleSFNDescribeActivity` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ListActivities` | ✓ `simulator-aws/stepfunctions.go:122::handleSFNListActivities` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.GetActivityTask` | ✓ `simulator-aws/stepfunctions.go:123::handleSFNGetActivityTask` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.SendTaskSuccess` | ✓ `simulator-aws/stepfunctions.go:124::handleSFNSendTaskSuccess` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.SendTaskFailure` | ✓ `simulator-aws/stepfunctions.go:125::handleSFNSendTaskFailure` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.SendTaskHeartbeat` | ✓ `simulator-aws/stepfunctions.go:126::handleSFNSendTaskHeartbeat` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.PublishStateMachineVersion` | ✓ `simulator-aws/stepfunctions.go:132::handleSFNPublishStateMachineVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DeleteStateMachineVersion` | ✓ `simulator-aws/stepfunctions.go:133::handleSFNDeleteStateMachineVersion` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.CreateStateMachineAlias` | ✓ `simulator-aws/stepfunctions.go:134::handleSFNCreateStateMachineAlias` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DeleteStateMachineAlias` | ✓ `simulator-aws/stepfunctions.go:135::handleSFNDeleteStateMachineAlias` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DescribeStateMachineAlias` | ✓ `simulator-aws/stepfunctions.go:136::handleSFNDescribeStateMachineAlias` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ListStateMachineAliases` | ✓ `simulator-aws/stepfunctions.go:137::handleSFNListStateMachineAliases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.UpdateStateMachineAlias` | ✓ `simulator-aws/stepfunctions.go:138::handleSFNUpdateStateMachineAlias` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DescribeStateMachineForExecution` | ✓ `simulator-aws/stepfunctions.go:141::handleSFNDescribeStateMachineForExecution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.RedriveExecution` | ✓ `simulator-aws/stepfunctions.go:142::handleSFNRedriveExecution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.DescribeMapRun` | ✓ `simulator-aws/stepfunctions.go:144::handleSFNDescribeMapRun` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.ListMapRuns` | ✓ `simulator-aws/stepfunctions.go:145::handleSFNListMapRuns` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.UpdateMapRun` | ✓ `simulator-aws/stepfunctions.go:146::handleSFNUpdateMapRun` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.TestState` | ✓ `simulator-aws/stepfunctions.go:149::handleSFNTestState` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.StartSyncExecution` | ✓ `simulator-aws/stepfunctions.go:150::handleSFNStartSyncExecution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action AWSStepFunctions.CreateStateMachine` | ✓ `simulator-aws/stepfunctions.go:99::handleSFNCreateStateMachine` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
