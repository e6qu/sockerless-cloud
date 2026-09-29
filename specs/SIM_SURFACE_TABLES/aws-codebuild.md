# Sim surface — aws-codebuild

Surface registered in `simulator-aws/codebuild.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

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
| `Action CodeBuild_20161006.CreateProject` | ✓ `simulator-aws/codebuild.go:259::handleCBCreateProject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetProjects` | ✓ `simulator-aws/codebuild.go:260::handleCBBatchGetProjects` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListProjects` | ✓ `simulator-aws/codebuild.go:261::handleCBListProjects` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.UpdateProject` | ✓ `simulator-aws/codebuild.go:262::handleCBUpdateProject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteProject` | ✓ `simulator-aws/codebuild.go:263::handleCBDeleteProject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StartBuild` | ✓ `simulator-aws/codebuild.go:264::handleCBStartBuild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StopBuild` | ✓ `simulator-aws/codebuild.go:265::handleCBStopBuild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.RetryBuild` | ✓ `simulator-aws/codebuild.go:266::handleCBRetryBuild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetBuilds` | ✓ `simulator-aws/codebuild.go:267::handleCBBatchGetBuilds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListBuildsForProject` | ✓ `simulator-aws/codebuild.go:268::handleCBListBuildsForProject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListBuilds` | ✓ `simulator-aws/codebuild.go:269::handleCBListBuilds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.CreateReportGroup` | ✓ `simulator-aws/codebuild.go:271::handleCBCreateReportGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.UpdateReportGroup` | ✓ `simulator-aws/codebuild.go:272::handleCBUpdateReportGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteReportGroup` | ✓ `simulator-aws/codebuild.go:273::handleCBDeleteReportGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListReportGroups` | ✓ `simulator-aws/codebuild.go:274::handleCBListReportGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetReportGroups` | ✓ `simulator-aws/codebuild.go:275::handleCBBatchGetReportGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListReports` | ✓ `simulator-aws/codebuild.go:276::handleCBListReports` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListReportsForReportGroup` | ✓ `simulator-aws/codebuild.go:277::handleCBListReportsForReportGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetReports` | ✓ `simulator-aws/codebuild.go:278::handleCBBatchGetReports` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ImportSourceCredentials` | ✓ `simulator-aws/codebuild.go:280::handleCBImportSourceCredentials` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListSourceCredentials` | ✓ `simulator-aws/codebuild.go:281::handleCBListSourceCredentials` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteSourceCredentials` | ✓ `simulator-aws/codebuild.go:282::handleCBDeleteSourceCredentials` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchDeleteBuilds` | ✓ `simulator-aws/codebuild_extended.go:159::handleCBBatchDeleteBuilds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StartBuildBatch` | ✓ `simulator-aws/codebuild_extended.go:162::handleCBStartBuildBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StopBuildBatch` | ✓ `simulator-aws/codebuild_extended.go:163::handleCBStopBuildBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.RetryBuildBatch` | ✓ `simulator-aws/codebuild_extended.go:164::handleCBRetryBuildBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteBuildBatch` | ✓ `simulator-aws/codebuild_extended.go:165::handleCBDeleteBuildBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetBuildBatches` | ✓ `simulator-aws/codebuild_extended.go:166::handleCBBatchGetBuildBatches` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListBuildBatches` | ✓ `simulator-aws/codebuild_extended.go:167::handleCBListBuildBatches` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListBuildBatchesForProject` | ✓ `simulator-aws/codebuild_extended.go:168::handleCBListBuildBatchesForProject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.CreateFleet` | ✓ `simulator-aws/codebuild_extended.go:171::handleCBCreateFleet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.UpdateFleet` | ✓ `simulator-aws/codebuild_extended.go:172::handleCBUpdateFleet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteFleet` | ✓ `simulator-aws/codebuild_extended.go:173::handleCBDeleteFleet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetFleets` | ✓ `simulator-aws/codebuild_extended.go:174::handleCBBatchGetFleets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListFleets` | ✓ `simulator-aws/codebuild_extended.go:175::handleCBListFleets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StartSandbox` | ✓ `simulator-aws/codebuild_extended.go:178::handleCBStartSandbox` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StopSandbox` | ✓ `simulator-aws/codebuild_extended.go:179::handleCBStopSandbox` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StartSandboxConnection` | ✓ `simulator-aws/codebuild_extended.go:180::handleCBStartSandboxConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetSandboxes` | ✓ `simulator-aws/codebuild_extended.go:181::handleCBBatchGetSandboxes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListSandboxes` | ✓ `simulator-aws/codebuild_extended.go:182::handleCBListSandboxes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListSandboxesForProject` | ✓ `simulator-aws/codebuild_extended.go:183::handleCBListSandboxesForProject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StartCommandExecution` | ✓ `simulator-aws/codebuild_extended.go:186::handleCBStartCommandExecution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetCommandExecutions` | ✓ `simulator-aws/codebuild_extended.go:187::handleCBBatchGetCommandExecutions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListCommandExecutionsForSandbox` | ✓ `simulator-aws/codebuild_extended.go:188::handleCBListCommandExecutionsForSandbox` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.CreateWebhook` | ✓ `simulator-aws/codebuild_extended.go:191::handleCBCreateWebhook` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.UpdateWebhook` | ✓ `simulator-aws/codebuild_extended.go:192::handleCBUpdateWebhook` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteWebhook` | ✓ `simulator-aws/codebuild_extended.go:193::handleCBDeleteWebhook` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteReport` | ✓ `simulator-aws/codebuild_extended.go:196::handleCBDeleteReport` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DescribeTestCases` | ✓ `simulator-aws/codebuild_extended.go:197::handleCBDescribeTestCases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DescribeCodeCoverages` | ✓ `simulator-aws/codebuild_extended.go:198::handleCBDescribeCodeCoverages` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.GetReportGroupTrend` | ✓ `simulator-aws/codebuild_extended.go:199::handleCBGetReportGroupTrend` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.PutResourcePolicy` | ✓ `simulator-aws/codebuild_extended.go:202::handleCBPutResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.GetResourcePolicy` | ✓ `simulator-aws/codebuild_extended.go:203::handleCBGetResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteResourcePolicy` | ✓ `simulator-aws/codebuild_extended.go:204::handleCBDeleteResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.UpdateProjectVisibility` | ✓ `simulator-aws/codebuild_extended.go:207::handleCBUpdateProjectVisibility` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.InvalidateProjectCache` | ✓ `simulator-aws/codebuild_extended.go:208::handleCBInvalidateProjectCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListCuratedEnvironmentImages` | ○ `simulator-aws/codebuild_extended.go:209::handleCBListCuratedEnvironmentImages` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListSharedProjects` | ✓ `simulator-aws/codebuild_extended.go:210::handleCBListSharedProjects` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListSharedReportGroups` | ✓ `simulator-aws/codebuild_extended.go:211::handleCBListSharedReportGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
Build and build-batch jobs clone supported Git sources through either imported,
AWS Key Management Service-encrypted source credentials or an AWS Secrets
Manager source credential. They resolve the requested source revision and
checked-in build specification, then run the commands inside the project's
exact configured container image with the repository mounted at
`CODEBUILD_SRC_DIR`. The real container exit determines terminal status and
phase context. StopBuild, StopBuildBatch, and an aborted synchronous AWS Step
Functions task cancel the underlying container rather than only changing the
control-plane row.

The official AWS SDK and AWS CLI suites prove success, failure, retry, stop,
batch, private authenticated Git, Secrets Manager authentication, source
revision, and real configured-image execution. An AWS Step Functions
integration runs the AWS CLI inside that container against Amazon SQS through
the standard `AWS_ENDPOINT_URL` coordinate and proves both downstream delivery
and cancellation by the absence of a delayed write.
<!-- HAND-WRITTEN END -->
