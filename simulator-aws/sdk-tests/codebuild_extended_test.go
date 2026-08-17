package aws_sdk_test

import (
	"encoding/base64"
	"sort"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cbCreateBuildspecProject creates a NO_SOURCE project with a one-command
// buildspec (and optional report group) so build/batch round-trips have a real
// project to run against.
func cbCreateBuildspecProject(t *testing.T, c *codebuild.Client, name, buildspec string) {
	t.Helper()
	_, err := c.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name:        aws.String(name),
		Source:      &cbtypes.ProjectSource{Type: cbtypes.SourceTypeNoSource, Buildspec: aws.String(buildspec)},
		Artifacts:   &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
		Environment: &cbtypes.ProjectEnvironment{Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String("public.ecr.aws/docker/library/alpine:3.21"), ComputeType: cbtypes.ComputeTypeBuildGeneral1Small},
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/cb-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteProject(ctx, &codebuild.DeleteProjectInput{Name: aws.String(name)}) })
}

// TestCodeBuild_BuildBatches_SDK covers StartBuildBatch, BatchGetBuildBatches,
// ListBuildBatches, ListBuildBatchesForProject, StopBuildBatch, RetryBuildBatch,
// DeleteBuildBatch, plus BatchDeleteBuilds.
func TestCodeBuild_BuildBatches_SDK(t *testing.T) {
	c := codebuildClient()
	proj := "cb-sdk-batch-project"
	cbCreateBuildspecProject(t, c, proj, "version: 0.2\nphases:\n  build:\n    commands:\n      - printf ok\n")

	start, err := c.StartBuildBatch(ctx, &codebuild.StartBuildBatchInput{ProjectName: aws.String(proj)})
	require.NoError(t, err)
	require.NotNil(t, start.BuildBatch)
	batchID := aws.ToString(start.BuildBatch.Id)
	require.NotEmpty(t, batchID)
	assert.Equal(t, proj, aws.ToString(start.BuildBatch.ProjectName))

	var lastBatchStatus cbtypes.StatusType
	batchSucceeded := assert.Eventually(t, func() bool {
		bg, err := c.BatchGetBuildBatches(ctx, &codebuild.BatchGetBuildBatchesInput{Ids: []string{batchID}})
		require.NoError(t, err)
		if len(bg.BuildBatches) == 1 {
			lastBatchStatus = bg.BuildBatches[0].BuildBatchStatus
		}
		return len(bg.BuildBatches) == 1 && bg.BuildBatches[0].BuildBatchStatus == cbtypes.StatusTypeSucceeded
	}, cbBuildCompletionBudget, 100*time.Millisecond)
	require.True(t, batchSucceeded, "build batch did not reach SUCCEEDED; last status: %s", lastBatchStatus)

	lb, err := c.ListBuildBatches(ctx, &codebuild.ListBuildBatchesInput{})
	require.NoError(t, err)
	assert.Contains(t, lb.Ids, batchID)

	lbp, err := c.ListBuildBatchesForProject(ctx, &codebuild.ListBuildBatchesForProjectInput{ProjectName: aws.String(proj)})
	require.NoError(t, err)
	assert.Contains(t, lbp.Ids, batchID)

	// RetryBuildBatch produces a fresh batch resource.
	retry, err := c.RetryBuildBatch(ctx, &codebuild.RetryBuildBatchInput{Id: aws.String(batchID)})
	require.NoError(t, err)
	require.NotNil(t, retry.BuildBatch)
	retryID := aws.ToString(retry.BuildBatch.Id)
	assert.NotEqual(t, batchID, retryID)

	// StopBuildBatch on an already-settled batch is idempotent.
	stop, err := c.StopBuildBatch(ctx, &codebuild.StopBuildBatchInput{Id: aws.String(batchID)})
	require.NoError(t, err)
	require.NotNil(t, stop.BuildBatch)

	_, err = c.DeleteBuildBatch(ctx, &codebuild.DeleteBuildBatchInput{Id: aws.String(batchID)})
	require.NoError(t, err)
	_, err = c.DeleteBuildBatch(ctx, &codebuild.DeleteBuildBatchInput{Id: aws.String(retryID)})
	require.NoError(t, err)

	// BatchDeleteBuilds round-trip on a regular build.
	sb, err := c.StartBuild(ctx, &codebuild.StartBuildInput{ProjectName: aws.String(proj)})
	require.NoError(t, err)
	buildID := aws.ToString(sb.Build.Id)
	del, err := c.BatchDeleteBuilds(ctx, &codebuild.BatchDeleteBuildsInput{Ids: []string{buildID}})
	require.NoError(t, err)
	assert.Contains(t, del.BuildsDeleted, buildID)
}

// TestCodeBuild_Fleets_SDK covers CreateFleet, UpdateFleet, BatchGetFleets,
// ListFleets, DeleteFleet.
func TestCodeBuild_Fleets_SDK(t *testing.T) {
	c := codebuildClient()
	name := "cb-sdk-fleet"
	create, err := c.CreateFleet(ctx, &codebuild.CreateFleetInput{
		Name:            aws.String(name),
		BaseCapacity:    aws.Int32(1),
		ComputeType:     cbtypes.ComputeTypeBuildGeneral1Small,
		EnvironmentType: cbtypes.EnvironmentTypeLinuxContainer,
		Tags:            []cbtypes.Tag{{Key: aws.String("team"), Value: aws.String("ci")}},
	})
	require.NoError(t, err)
	require.NotNil(t, create.Fleet)
	arn := aws.ToString(create.Fleet.Arn)
	require.NotEmpty(t, arn)
	assert.Equal(t, name, aws.ToString(create.Fleet.Name))
	t.Cleanup(func() { _, _ = c.DeleteFleet(ctx, &codebuild.DeleteFleetInput{Arn: aws.String(arn)}) })

	upd, err := c.UpdateFleet(ctx, &codebuild.UpdateFleetInput{
		Arn:          aws.String(arn),
		BaseCapacity: aws.Int32(3),
	})
	require.NoError(t, err)
	require.NotNil(t, upd.Fleet)
	assert.Equal(t, int32(3), aws.ToInt32(upd.Fleet.BaseCapacity))

	bg, err := c.BatchGetFleets(ctx, &codebuild.BatchGetFleetsInput{Names: []string{arn}})
	require.NoError(t, err)
	require.Len(t, bg.Fleets, 1)
	assert.Equal(t, name, aws.ToString(bg.Fleets[0].Name))
	assert.Empty(t, bg.FleetsNotFound)

	lf, err := c.ListFleets(ctx, &codebuild.ListFleetsInput{})
	require.NoError(t, err)
	assert.Contains(t, lf.Fleets, arn)

	_, err = c.DeleteFleet(ctx, &codebuild.DeleteFleetInput{Arn: aws.String(arn)})
	require.NoError(t, err)
}

// TestCodeBuild_SandboxesAndCommands_SDK covers StartSandbox, BatchGetSandboxes,
// ListSandboxes, ListSandboxesForProject, StartSandboxConnection,
// StartCommandExecution, BatchGetCommandExecutions,
// ListCommandExecutionsForSandbox, StopSandbox.
func TestCodeBuild_SandboxesAndCommands_SDK(t *testing.T) {
	c := codebuildClient()
	proj := "cb-sdk-sandbox-project"
	cbCreateBuildspecProject(t, c, proj, "version: 0.2\nphases:\n  build:\n    commands:\n      - printf ok\n")

	start, err := c.StartSandbox(ctx, &codebuild.StartSandboxInput{ProjectName: aws.String(proj)})
	require.NoError(t, err)
	require.NotNil(t, start.Sandbox)
	sbID := aws.ToString(start.Sandbox.Id)
	require.NotEmpty(t, sbID)
	assert.Equal(t, "RUNNING", aws.ToString(start.Sandbox.Status))

	bg, err := c.BatchGetSandboxes(ctx, &codebuild.BatchGetSandboxesInput{Ids: []string{sbID}})
	require.NoError(t, err)
	require.Len(t, bg.Sandboxes, 1)
	assert.Empty(t, bg.SandboxesNotFound)

	ls, err := c.ListSandboxes(ctx, &codebuild.ListSandboxesInput{})
	require.NoError(t, err)
	assert.Contains(t, ls.Ids, sbID)

	lsp, err := c.ListSandboxesForProject(ctx, &codebuild.ListSandboxesForProjectInput{ProjectName: aws.String(proj)})
	require.NoError(t, err)
	assert.Contains(t, lsp.Ids, sbID)

	conn, err := c.StartSandboxConnection(ctx, &codebuild.StartSandboxConnectionInput{SandboxId: aws.String(sbID)})
	require.NoError(t, err)
	require.NotNil(t, conn.SsmSession)
	assert.NotEmpty(t, aws.ToString(conn.SsmSession.SessionId))
	assert.NotEmpty(t, aws.ToString(conn.SsmSession.StreamUrl))
	assert.NotEmpty(t, aws.ToString(conn.SsmSession.TokenValue))

	cmd, err := c.StartCommandExecution(ctx, &codebuild.StartCommandExecutionInput{
		SandboxId: aws.String(sbID),
		Command:   aws.String("printf hello"),
	})
	require.NoError(t, err)
	require.NotNil(t, cmd.CommandExecution)
	cmdID := aws.ToString(cmd.CommandExecution.Id)
	require.NotEmpty(t, cmdID)

	require.Eventually(t, func() bool {
		bgc, err := c.BatchGetCommandExecutions(ctx, &codebuild.BatchGetCommandExecutionsInput{
			SandboxId:           aws.String(sbID),
			CommandExecutionIds: []string{cmdID},
		})
		require.NoError(t, err)
		return len(bgc.CommandExecutions) == 1 && aws.ToString(bgc.CommandExecutions[0].Status) == "SUCCEEDED"
	}, 10*time.Second, 100*time.Millisecond)

	lc, err := c.ListCommandExecutionsForSandbox(ctx, &codebuild.ListCommandExecutionsForSandboxInput{SandboxId: aws.String(sbID)})
	require.NoError(t, err)
	require.Len(t, lc.CommandExecutions, 1)
	assert.Equal(t, cmdID, aws.ToString(lc.CommandExecutions[0].Id))

	stop, err := c.StopSandbox(ctx, &codebuild.StopSandboxInput{Id: aws.String(sbID)})
	require.NoError(t, err)
	require.NotNil(t, stop.Sandbox)
	assert.Equal(t, "STOPPED", aws.ToString(stop.Sandbox.Status))
}

// TestCodeBuild_Webhooks_SDK covers CreateWebhook, UpdateWebhook, DeleteWebhook.
func TestCodeBuild_Webhooks_SDK(t *testing.T) {
	c := codebuildClient()
	proj := "cb-sdk-webhook-project"
	cbCreateBuildspecProject(t, c, proj, "version: 0.2\nphases:\n  build:\n    commands:\n      - printf ok\n")

	create, err := c.CreateWebhook(ctx, &codebuild.CreateWebhookInput{
		ProjectName:  aws.String(proj),
		BranchFilter: aws.String("main"),
	})
	require.NoError(t, err)
	require.NotNil(t, create.Webhook)
	require.NotEmpty(t, aws.ToString(create.Webhook.PayloadUrl))
	require.NotEmpty(t, aws.ToString(create.Webhook.Secret))
	assert.Equal(t, "main", aws.ToString(create.Webhook.BranchFilter))

	upd, err := c.UpdateWebhook(ctx, &codebuild.UpdateWebhookInput{
		ProjectName:  aws.String(proj),
		BranchFilter: aws.String("develop"),
		RotateSecret: true,
	})
	require.NoError(t, err)
	require.NotNil(t, upd.Webhook)
	assert.Equal(t, "develop", aws.ToString(upd.Webhook.BranchFilter))
	assert.NotEqual(t, aws.ToString(create.Webhook.Secret), aws.ToString(upd.Webhook.Secret))

	_, err = c.DeleteWebhook(ctx, &codebuild.DeleteWebhookInput{ProjectName: aws.String(proj)})
	require.NoError(t, err)
}

// cbReportBuildspec builds a buildspec whose build phase writes raw report
// files into the build container and whose reports section declares them, the
// way a real project does: the framework writes the results, the reports
// section names where they are and what format they are in. The content is
// carried base64-encoded so the YAML and the shell both leave it alone.
func cbReportBuildspec(entries string, files map[string]string) string {
	commands := "      - mkdir -p test-results\n"
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		commands += "      - printf %s " + base64.StdEncoding.EncodeToString([]byte(files[name])) +
			" | base64 -d > test-results/" + name + "\n"
	}
	return "version: 0.2\nphases:\n  build:\n    commands:\n" + commands + "reports:\n" + entries
}

const cbJUnitReportFile = `<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="checkout">
    <testcase classname="cart.CheckoutTest" name="totalsTheBasket" time="0.125"/>
    <testcase classname="cart.CheckoutTest" name="rejectsAnEmptyBasket" time="0.5">
      <failure message="expected 400, got 200">at CheckoutTest.java:42</failure>
    </testcase>
  </testsuite>
</testsuites>`

const cbJaCoCoReportFile = `<?xml version="1.0" encoding="UTF-8"?>
<report name="cart">
  <package name="com/example/cart">
    <sourcefile name="Checkout.java">
      <counter type="BRANCH" missed="1" covered="3"/>
      <counter type="LINE" missed="2" covered="8"/>
    </sourcefile>
  </package>
</report>`

// cbWaitForBuildReports runs a build and returns the report ARNs it produced
// once it has settled.
func cbWaitForBuildReports(t *testing.T, c *codebuild.Client, project string) []string {
	t.Helper()
	start, err := c.StartBuild(ctx, &codebuild.StartBuildInput{ProjectName: aws.String(project)})
	require.NoError(t, err)
	buildID := aws.ToString(start.Build.Id)

	var reportArns []string
	require.Eventually(t, func() bool {
		builds, err := c.BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{Ids: []string{buildID}})
		require.NoError(t, err)
		if len(builds.Builds) != 1 || builds.Builds[0].BuildStatus == cbtypes.StatusTypeInProgress {
			return false
		}
		require.Equal(t, cbtypes.StatusTypeSucceeded, builds.Builds[0].BuildStatus,
			"build %s did not succeed: %+v", buildID, builds.Builds[0].Phases)
		reportArns = builds.Builds[0].ReportArns
		return true
	}, cbBuildCompletionBudget, 100*time.Millisecond)
	return reportArns
}

// TestCodeBuild_ReportInsights_SDK covers DeleteReport, DescribeTestCases,
// DescribeCodeCoverages and GetReportGroupTrend against reports produced by a
// build that actually wrote the raw result files its buildspec declared. Every
// assertion here is about what those files contained: a test case the build did
// not run, or a coverage figure no file reported, is the defect this test
// exists to catch.
func TestCodeBuild_ReportInsights_SDK(t *testing.T) {
	c := codebuildClient()
	rg, err := c.CreateReportGroup(ctx, &codebuild.CreateReportGroupInput{
		Name:         aws.String("cb-sdk-insights-rg"),
		Type:         cbtypes.ReportTypeTest,
		ExportConfig: &cbtypes.ReportExportConfig{ExportConfigType: cbtypes.ReportExportConfigTypeNoExport},
	})
	require.NoError(t, err)
	rgArn := aws.ToString(rg.ReportGroup.Arn)
	t.Cleanup(func() {
		_, _ = c.DeleteReportGroup(ctx, &codebuild.DeleteReportGroupInput{Arn: aws.String(rgArn), DeleteReports: true})
	})

	proj := "cb-sdk-insights-project"
	cbCreateBuildspecProject(t, c, proj, cbReportBuildspec(
		"  "+rgArn+":\n    files:\n      - 'junit.xml'\n    base-directory: test-results\n    file-format: JUNITXML\n",
		map[string]string{"junit.xml": cbJUnitReportFile}))

	reportArns := cbWaitForBuildReports(t, c, proj)
	require.Len(t, reportArns, 1)
	reportArn := reportArns[0]

	// One case passed and one failed, so the report is FAILED — "Some of the
	// test cases were not successful".
	reports, err := c.BatchGetReports(ctx, &codebuild.BatchGetReportsInput{ReportArns: []string{reportArn}})
	require.NoError(t, err)
	require.Len(t, reports.Reports, 1)
	assert.Equal(t, cbtypes.ReportStatusTypeFailed, reports.Reports[0].Status)
	assert.Equal(t, cbtypes.ReportTypeTest, reports.Reports[0].Type)
	assert.False(t, aws.ToBool(reports.Reports[0].Truncated))

	tc, err := c.DescribeTestCases(ctx, &codebuild.DescribeTestCasesInput{ReportArn: aws.String(reportArn)})
	require.NoError(t, err)
	require.Len(t, tc.TestCases, 2, "the report must carry the two cases the JUnit XML held")
	byName := map[string]cbtypes.TestCase{}
	for _, testCase := range tc.TestCases {
		byName[aws.ToString(testCase.Name)] = testCase
	}
	passed, ok := byName["totalsTheBasket"]
	require.True(t, ok, "the passing case the file named is missing: %+v", tc.TestCases)
	assert.Equal(t, "SUCCEEDED", aws.ToString(passed.Status))
	assert.Equal(t, "cart.CheckoutTest", aws.ToString(passed.Prefix))
	assert.Equal(t, "checkout", aws.ToString(passed.TestSuiteName))
	assert.Equal(t, int64(125000000), aws.ToInt64(passed.DurationInNanoSeconds))
	assert.Equal(t, "junit.xml", aws.ToString(passed.TestRawDataPath))
	assert.Equal(t, reportArn, aws.ToString(passed.ReportArn))
	failed, ok := byName["rejectsAnEmptyBasket"]
	require.True(t, ok, "the failing case the file named is missing: %+v", tc.TestCases)
	assert.Equal(t, "FAILED", aws.ToString(failed.Status))
	assert.Equal(t, "expected 400, got 200", aws.ToString(failed.Message))

	// The filter is applied to what the file held, not to an invented set.
	onlyFailures, err := c.DescribeTestCases(ctx, &codebuild.DescribeTestCasesInput{
		ReportArn: aws.String(reportArn),
		Filter:    &cbtypes.TestCaseFilter{Status: aws.String("FAILED")},
	})
	require.NoError(t, err)
	require.Len(t, onlyFailures.TestCases, 1)
	assert.Equal(t, "rejectsAnEmptyBasket", aws.ToString(onlyFailures.TestCases[0].Name))

	// A TEST report has no code coverage: the build wrote no coverage file, so
	// the answer is empty rather than a fabricated hundred-percent row.
	cov, err := c.DescribeCodeCoverages(ctx, &codebuild.DescribeCodeCoveragesInput{ReportArn: aws.String(reportArn)})
	require.NoError(t, err)
	assert.Empty(t, cov.CodeCoverages)

	// PASS_RATE is the fraction of the report's own cases that succeeded.
	trend, err := c.GetReportGroupTrend(ctx, &codebuild.GetReportGroupTrendInput{
		ReportGroupArn: aws.String(rgArn),
		TrendField:     cbtypes.ReportGroupTrendFieldTypePassRate,
	})
	require.NoError(t, err)
	require.NotNil(t, trend.Stats)
	require.Len(t, trend.RawData, 1)
	assert.Equal(t, "50", aws.ToString(trend.RawData[0].Data))
	total, err := c.GetReportGroupTrend(ctx, &codebuild.GetReportGroupTrendInput{
		ReportGroupArn: aws.String(rgArn),
		TrendField:     cbtypes.ReportGroupTrendFieldTypeTotal,
	})
	require.NoError(t, err)
	require.Len(t, total.RawData, 1)
	assert.Equal(t, "2", aws.ToString(total.RawData[0].Data))

	_, err = c.DeleteReport(ctx, &codebuild.DeleteReportInput{Arn: aws.String(reportArn)})
	require.NoError(t, err)
	// A deleted report takes its test cases with it.
	_, err = c.DescribeTestCases(ctx, &codebuild.DescribeTestCasesInput{ReportArn: aws.String(reportArn)})
	assert.Error(t, err)
}

// TestCodeBuild_CodeCoverageReport_SDK proves a code coverage report carries
// the figures the JaCoCo file held, and that naming a new report group by name
// creates "<project-name>-<report-group-name>" the way the service documents.
func TestCodeBuild_CodeCoverageReport_SDK(t *testing.T) {
	c := codebuildClient()
	proj := "cb-sdk-coverage-project"
	cbCreateBuildspecProject(t, c, proj, cbReportBuildspec(
		"  coverage:\n    files:\n      - 'jacoco.xml'\n    base-directory: test-results\n    file-format: JACOCOXML\n",
		map[string]string{"jacoco.xml": cbJaCoCoReportFile}))

	reportArns := cbWaitForBuildReports(t, c, proj)
	require.Len(t, reportArns, 1)
	reportArn := reportArns[0]

	groupArn := "arn:aws:codebuild:us-east-1:123456789012:report-group/" + proj + "-coverage"
	t.Cleanup(func() {
		_, _ = c.DeleteReportGroup(ctx, &codebuild.DeleteReportGroupInput{
			Arn: aws.String(groupArn), DeleteReports: true})
	})
	groups, err := c.BatchGetReportGroups(ctx, &codebuild.BatchGetReportGroupsInput{
		ReportGroupArns: []string{groupArn}})
	require.NoError(t, err)
	require.Len(t, groups.ReportGroups, 1,
		"a reports entry naming a new group must create <project-name>-<report-group-name>")
	assert.Equal(t, cbtypes.ReportTypeCodeCoverage, groups.ReportGroups[0].Type)

	cov, err := c.DescribeCodeCoverages(ctx, &codebuild.DescribeCodeCoveragesInput{
		ReportArn: aws.String(reportArn)})
	require.NoError(t, err)
	require.Len(t, cov.CodeCoverages, 1)
	row := cov.CodeCoverages[0]
	assert.Equal(t, "com/example/cart/Checkout.java", aws.ToString(row.FilePath))
	assert.Equal(t, reportArn, aws.ToString(row.ReportARN))
	assert.Equal(t, int32(8), aws.ToInt32(row.LinesCovered))
	assert.Equal(t, int32(2), aws.ToInt32(row.LinesMissed))
	assert.InDelta(t, 80.0, aws.ToFloat64(row.LineCoveragePercentage), 0.001)
	assert.Equal(t, int32(3), aws.ToInt32(row.BranchesCovered))
	assert.Equal(t, int32(1), aws.ToInt32(row.BranchesMissed))
	assert.InDelta(t, 75.0, aws.ToFloat64(row.BranchCoveragePercentage), 0.001)

	// A CODE_COVERAGE report has no test cases.
	tc, err := c.DescribeTestCases(ctx, &codebuild.DescribeTestCasesInput{ReportArn: aws.String(reportArn)})
	require.NoError(t, err)
	assert.Empty(t, tc.TestCases)
}

// TestCodeBuild_ReportsWithoutRawData_SDK covers the two ways a build produces
// no results: a buildspec with no reports section produces no report at all,
// and a reports entry whose files match nothing produces an INCOMPLETE report
// carrying nothing. Neither may answer with an invented test case.
func TestCodeBuild_ReportsWithoutRawData_SDK(t *testing.T) {
	c := codebuildClient()

	noReports := "cb-sdk-noreports-project"
	cbCreateBuildspecProject(t, c, noReports,
		"version: 0.2\nphases:\n  build:\n    commands:\n      - printf ok\n")
	assert.Empty(t, cbWaitForBuildReports(t, c, noReports),
		"a build whose buildspec declares no reports must produce none")

	unmatched := "cb-sdk-unmatched-project"
	cbCreateBuildspecProject(t, c, unmatched,
		"version: 0.2\nphases:\n  build:\n    commands:\n      - printf ok\n"+
			"reports:\n  unit:\n    files:\n      - 'junit.xml'\n    base-directory: test-results\n")
	reportArns := cbWaitForBuildReports(t, c, unmatched)
	require.Len(t, reportArns, 1)
	t.Cleanup(func() {
		_, _ = c.DeleteReportGroup(ctx, &codebuild.DeleteReportGroupInput{
			Arn:           aws.String("arn:aws:codebuild:us-east-1:123456789012:report-group/" + unmatched + "-unit"),
			DeleteReports: true})
	})

	reports, err := c.BatchGetReports(ctx, &codebuild.BatchGetReportsInput{ReportArns: reportArns})
	require.NoError(t, err)
	require.Len(t, reports.Reports, 1)
	assert.Equal(t, cbtypes.ReportStatusTypeIncomplete, reports.Reports[0].Status)

	tc, err := c.DescribeTestCases(ctx, &codebuild.DescribeTestCasesInput{ReportArn: aws.String(reportArns[0])})
	require.NoError(t, err)
	assert.Empty(t, tc.TestCases, "a report with no raw data file must carry no test cases")
	cov, err := c.DescribeCodeCoverages(ctx, &codebuild.DescribeCodeCoveragesInput{ReportArn: aws.String(reportArns[0])})
	require.NoError(t, err)
	assert.Empty(t, cov.CodeCoverages, "a report with no raw data file must carry no coverage")
}

// TestCodeBuild_ResourcePolicy_SDK covers PutResourcePolicy, GetResourcePolicy,
// DeleteResourcePolicy, ListSharedProjects, ListSharedReportGroups.
func TestCodeBuild_ResourcePolicy_SDK(t *testing.T) {
	c := codebuildClient()
	proj := "cb-sdk-policy-project"
	_, err := c.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name:        aws.String(proj),
		Source:      &cbtypes.ProjectSource{Type: cbtypes.SourceTypeNoSource},
		Artifacts:   &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
		Environment: &cbtypes.ProjectEnvironment{Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String("public.ecr.aws/docker/library/alpine:3.21"), ComputeType: cbtypes.ComputeTypeBuildGeneral1Small},
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/cb-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteProject(ctx, &codebuild.DeleteProjectInput{Name: aws.String(proj)}) })

	projARN := "arn:aws:codebuild:us-east-1:123456789012:project/" + proj
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":["codebuild:BatchGetProjects"],"Resource":"` + projARN + `"}]}`

	put, err := c.PutResourcePolicy(ctx, &codebuild.PutResourcePolicyInput{
		ResourceArn: aws.String(projARN),
		Policy:      aws.String(policy),
	})
	require.NoError(t, err)
	assert.Equal(t, projARN, aws.ToString(put.ResourceArn))

	get, err := c.GetResourcePolicy(ctx, &codebuild.GetResourcePolicyInput{ResourceArn: aws.String(projARN)})
	require.NoError(t, err)
	assert.Equal(t, policy, aws.ToString(get.Policy))

	lsp, err := c.ListSharedProjects(ctx, &codebuild.ListSharedProjectsInput{})
	require.NoError(t, err)
	assert.Contains(t, lsp.Projects, projARN)

	// A shared report group is surfaced once its ARN carries a policy.
	rgArn := "arn:aws:codebuild:us-east-1:123456789012:report-group/" + proj + "-rg"
	_, err = c.PutResourcePolicy(ctx, &codebuild.PutResourcePolicyInput{
		ResourceArn: aws.String(rgArn),
		Policy:      aws.String(policy),
	})
	require.NoError(t, err)
	lsr, err := c.ListSharedReportGroups(ctx, &codebuild.ListSharedReportGroupsInput{})
	require.NoError(t, err)
	assert.Contains(t, lsr.ReportGroups, rgArn)
	_, _ = c.DeleteResourcePolicy(ctx, &codebuild.DeleteResourcePolicyInput{ResourceArn: aws.String(rgArn)})

	_, err = c.DeleteResourcePolicy(ctx, &codebuild.DeleteResourcePolicyInput{ResourceArn: aws.String(projARN)})
	require.NoError(t, err)
	_, err = c.GetResourcePolicy(ctx, &codebuild.GetResourcePolicyInput{ResourceArn: aws.String(projARN)})
	require.Error(t, err)
}

// TestCodeBuild_ProjectControls_SDK covers UpdateProjectVisibility,
// InvalidateProjectCache, and ListCuratedEnvironmentImages.
func TestCodeBuild_ProjectControls_SDK(t *testing.T) {
	c := codebuildClient()
	proj := "cb-sdk-controls-project"
	cbCreateBuildspecProject(t, c, proj, "version: 0.2\nphases:\n  build:\n    commands:\n      - printf ok\n")
	projARN := "arn:aws:codebuild:us-east-1:123456789012:project/" + proj

	vis, err := c.UpdateProjectVisibility(ctx, &codebuild.UpdateProjectVisibilityInput{
		ProjectArn:        aws.String(projARN),
		ProjectVisibility: cbtypes.ProjectVisibilityTypePrivate,
	})
	require.NoError(t, err)
	assert.Equal(t, cbtypes.ProjectVisibilityTypePrivate, vis.ProjectVisibility)
	assert.Equal(t, projARN, aws.ToString(vis.ProjectArn))

	_, err = c.InvalidateProjectCache(ctx, &codebuild.InvalidateProjectCacheInput{ProjectName: aws.String(proj)})
	require.NoError(t, err)

	imgs, err := c.ListCuratedEnvironmentImages(ctx, &codebuild.ListCuratedEnvironmentImagesInput{})
	require.NoError(t, err)
	require.NotEmpty(t, imgs.Platforms)
	found := false
	for _, p := range imgs.Platforms {
		for _, lang := range p.Languages {
			for _, img := range lang.Images {
				if aws.ToString(img.Name) == "aws/codebuild/standard:7.0" {
					found = true
				}
			}
		}
	}
	assert.True(t, found, "curated images must include the Ubuntu standard 7.0 image")
}
