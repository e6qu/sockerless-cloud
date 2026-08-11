# Sim surface — aws-codebuild

Surface registered in `simulator-aws/codebuild.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `Action CodeBuild_20161006.CreateProject` | ✓ `simulator-aws/codebuild.go:168::handleCBCreateProject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetProjects` | ✓ `simulator-aws/codebuild.go:169::handleCBBatchGetProjects` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListProjects` | ✓ `simulator-aws/codebuild.go:170::handleCBListProjects` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.UpdateProject` | ✓ `simulator-aws/codebuild.go:171::handleCBUpdateProject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteProject` | ✓ `simulator-aws/codebuild.go:172::handleCBDeleteProject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StartBuild` | ✓ `simulator-aws/codebuild.go:173::handleCBStartBuild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StopBuild` | ✓ `simulator-aws/codebuild.go:174::handleCBStopBuild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.RetryBuild` | ✓ `simulator-aws/codebuild.go:175::handleCBRetryBuild` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetBuilds` | ✓ `simulator-aws/codebuild.go:176::handleCBBatchGetBuilds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListBuildsForProject` | ✓ `simulator-aws/codebuild.go:177::handleCBListBuildsForProject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListBuilds` | ✓ `simulator-aws/codebuild.go:178::handleCBListBuilds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.CreateReportGroup` | ✓ `simulator-aws/codebuild.go:180::handleCBCreateReportGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.UpdateReportGroup` | ✓ `simulator-aws/codebuild.go:181::handleCBUpdateReportGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteReportGroup` | ✓ `simulator-aws/codebuild.go:182::handleCBDeleteReportGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListReportGroups` | ✓ `simulator-aws/codebuild.go:183::handleCBListReportGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetReportGroups` | ✓ `simulator-aws/codebuild.go:184::handleCBBatchGetReportGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListReports` | ✓ `simulator-aws/codebuild.go:185::handleCBListReports` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListReportsForReportGroup` | ✓ `simulator-aws/codebuild.go:186::handleCBListReportsForReportGroup` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetReports` | ✓ `simulator-aws/codebuild.go:187::handleCBBatchGetReports` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ImportSourceCredentials` | ✓ `simulator-aws/codebuild.go:189::handleCBImportSourceCredentials` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListSourceCredentials` | ✓ `simulator-aws/codebuild.go:190::handleCBListSourceCredentials` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteSourceCredentials` | ✓ `simulator-aws/codebuild.go:191::handleCBDeleteSourceCredentials` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchDeleteBuilds` | ✓ `simulator-aws/codebuild_extended.go:158::handleCBBatchDeleteBuilds` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StartBuildBatch` | ✓ `simulator-aws/codebuild_extended.go:161::handleCBStartBuildBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StopBuildBatch` | ✓ `simulator-aws/codebuild_extended.go:162::handleCBStopBuildBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.RetryBuildBatch` | ✓ `simulator-aws/codebuild_extended.go:163::handleCBRetryBuildBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteBuildBatch` | ✓ `simulator-aws/codebuild_extended.go:164::handleCBDeleteBuildBatch` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetBuildBatches` | ✓ `simulator-aws/codebuild_extended.go:165::handleCBBatchGetBuildBatches` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListBuildBatches` | ✓ `simulator-aws/codebuild_extended.go:166::handleCBListBuildBatches` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListBuildBatchesForProject` | ✓ `simulator-aws/codebuild_extended.go:167::handleCBListBuildBatchesForProject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.CreateFleet` | ✓ `simulator-aws/codebuild_extended.go:170::handleCBCreateFleet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.UpdateFleet` | ✓ `simulator-aws/codebuild_extended.go:171::handleCBUpdateFleet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteFleet` | ✓ `simulator-aws/codebuild_extended.go:172::handleCBDeleteFleet` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetFleets` | ✓ `simulator-aws/codebuild_extended.go:173::handleCBBatchGetFleets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListFleets` | ✓ `simulator-aws/codebuild_extended.go:174::handleCBListFleets` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StartSandbox` | ✓ `simulator-aws/codebuild_extended.go:177::handleCBStartSandbox` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StopSandbox` | ✓ `simulator-aws/codebuild_extended.go:178::handleCBStopSandbox` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StartSandboxConnection` | ✓ `simulator-aws/codebuild_extended.go:179::handleCBStartSandboxConnection` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetSandboxes` | ✓ `simulator-aws/codebuild_extended.go:180::handleCBBatchGetSandboxes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListSandboxes` | ✓ `simulator-aws/codebuild_extended.go:181::handleCBListSandboxes` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListSandboxesForProject` | ✓ `simulator-aws/codebuild_extended.go:182::handleCBListSandboxesForProject` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.StartCommandExecution` | ✓ `simulator-aws/codebuild_extended.go:185::handleCBStartCommandExecution` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.BatchGetCommandExecutions` | ✓ `simulator-aws/codebuild_extended.go:186::handleCBBatchGetCommandExecutions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListCommandExecutionsForSandbox` | ✓ `simulator-aws/codebuild_extended.go:187::handleCBListCommandExecutionsForSandbox` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.CreateWebhook` | ✓ `simulator-aws/codebuild_extended.go:190::handleCBCreateWebhook` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.UpdateWebhook` | ✓ `simulator-aws/codebuild_extended.go:191::handleCBUpdateWebhook` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteWebhook` | ✓ `simulator-aws/codebuild_extended.go:192::handleCBDeleteWebhook` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteReport` | ✓ `simulator-aws/codebuild_extended.go:195::handleCBDeleteReport` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DescribeTestCases` | ✓ `simulator-aws/codebuild_extended.go:196::handleCBDescribeTestCases` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DescribeCodeCoverages` | ✓ `simulator-aws/codebuild_extended.go:197::handleCBDescribeCodeCoverages` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.GetReportGroupTrend` | ✓ `simulator-aws/codebuild_extended.go:198::handleCBGetReportGroupTrend` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.PutResourcePolicy` | ✓ `simulator-aws/codebuild_extended.go:201::handleCBPutResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.GetResourcePolicy` | ✓ `simulator-aws/codebuild_extended.go:202::handleCBGetResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.DeleteResourcePolicy` | ✓ `simulator-aws/codebuild_extended.go:203::handleCBDeleteResourcePolicy` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.UpdateProjectVisibility` | ✓ `simulator-aws/codebuild_extended.go:206::handleCBUpdateProjectVisibility` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.InvalidateProjectCache` | ✓ `simulator-aws/codebuild_extended.go:207::handleCBInvalidateProjectCache` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListCuratedEnvironmentImages` | ✓ `simulator-aws/codebuild_extended.go:208::handleCBListCuratedEnvironmentImages` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListSharedProjects` | ✓ `simulator-aws/codebuild_extended.go:209::handleCBListSharedProjects` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CodeBuild_20161006.ListSharedReportGroups` | ✓ `simulator-aws/codebuild_extended.go:210::handleCBListSharedReportGroups` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

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
