# Sim surface — aws-batch

Surface registered in `simulator-aws/batch.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `POST /v1/createcomputeenvironment` | ✓ `simulator-aws/batch.go:171::handleBatchCreateComputeEnvironment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/describecomputeenvironments` | ✓ `simulator-aws/batch.go:172::handleBatchDescribeComputeEnvironments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/updatecomputeenvironment` | ✓ `simulator-aws/batch.go:173::handleBatchUpdateComputeEnvironment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/deletecomputeenvironment` | ✓ `simulator-aws/batch.go:174::handleBatchDeleteComputeEnvironment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/createjobqueue` | ✓ `simulator-aws/batch.go:176::handleBatchCreateJobQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/describejobqueues` | ✓ `simulator-aws/batch.go:177::handleBatchDescribeJobQueues` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/updatejobqueue` | ✓ `simulator-aws/batch.go:178::handleBatchUpdateJobQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/deletejobqueue` | ✓ `simulator-aws/batch.go:179::handleBatchDeleteJobQueue` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/registerjobdefinition` | ✓ `simulator-aws/batch.go:181::handleBatchRegisterJobDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/describejobdefinitions` | ✓ `simulator-aws/batch.go:182::handleBatchDescribeJobDefinitions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/deregisterjobdefinition` | ✓ `simulator-aws/batch.go:183::handleBatchDeregisterJobDefinition` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/submitjob` | ✓ `simulator-aws/batch.go:185::handleBatchSubmitJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/describejobs` | ✓ `simulator-aws/batch.go:186::handleBatchDescribeJobs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/listjobs` | ✓ `simulator-aws/batch.go:187::handleBatchListJobs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/canceljob` | ✓ `simulator-aws/batch.go:188::handleBatchCancelJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/terminatejob` | ✓ `simulator-aws/batch.go:189::handleBatchTerminateJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/createschedulingpolicy` | ✓ `simulator-aws/batch.go:191::handleBatchCreateSchedulingPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/describeschedulingpolicies` | ✓ `simulator-aws/batch.go:192::handleBatchDescribeSchedulingPolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/listschedulingpolicies` | ✓ `simulator-aws/batch.go:193::handleBatchListSchedulingPolicies` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/updateschedulingpolicy` | ✓ `simulator-aws/batch.go:194::handleBatchUpdateSchedulingPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/deleteschedulingpolicy` | ✓ `simulator-aws/batch.go:195::handleBatchDeleteSchedulingPolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/createconsumableresource` | ✓ `simulator-aws/batch.go:198::handleBatchCreateConsumableResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/describeconsumableresource` | ✓ `simulator-aws/batch.go:199::handleBatchDescribeConsumableResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/listconsumableresources` | ✓ `simulator-aws/batch.go:200::handleBatchListConsumableResources` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/updateconsumableresource` | ✓ `simulator-aws/batch.go:201::handleBatchUpdateConsumableResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/deleteconsumableresource` | ✓ `simulator-aws/batch.go:202::handleBatchDeleteConsumableResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/listjobsbyconsumableresource` | ✓ `simulator-aws/batch.go:203::handleBatchListJobsByConsumableResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/createserviceenvironment` | ✓ `simulator-aws/batch.go:206::handleBatchCreateServiceEnvironment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/describeserviceenvironments` | ✓ `simulator-aws/batch.go:207::handleBatchDescribeServiceEnvironments` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/updateserviceenvironment` | ✓ `simulator-aws/batch.go:208::handleBatchUpdateServiceEnvironment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/deleteserviceenvironment` | ✓ `simulator-aws/batch.go:209::handleBatchDeleteServiceEnvironment` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/submitservicejob` | ✓ `simulator-aws/batch.go:212::handleBatchSubmitServiceJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/describeservicejob` | ✓ `simulator-aws/batch.go:213::handleBatchDescribeServiceJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/listservicejobs` | ✓ `simulator-aws/batch.go:214::handleBatchListServiceJobs` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/terminateservicejob` | ✓ `simulator-aws/batch.go:215::handleBatchTerminateServiceJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/updateservicejob` | ✓ `simulator-aws/batch.go:216::handleBatchUpdateServiceJob` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/createquotashare` | ✓ `simulator-aws/batch.go:219::handleBatchCreateQuotaShare` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/describequotashare` | ✓ `simulator-aws/batch.go:220::handleBatchDescribeQuotaShare` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/listquotashares` | ✓ `simulator-aws/batch.go:221::handleBatchListQuotaShares` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/updatequotashare` | ✓ `simulator-aws/batch.go:222::handleBatchUpdateQuotaShare` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/deletequotashare` | ✓ `simulator-aws/batch.go:223::handleBatchDeleteQuotaShare` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/getjobqueuesnapshot` | ✓ `simulator-aws/batch.go:226::handleBatchGetJobQueueSnapshot` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `GET /v1/tags/{resourceArn}` | ✓ `simulator-aws/batch.go:229::handleBatchListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `POST /v1/tags/{resourceArn}` | ✓ `simulator-aws/batch.go:230::handleBatchTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `DELETE /v1/tags/{resourceArn}` | ✓ `simulator-aws/batch.go:231::handleBatchUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
