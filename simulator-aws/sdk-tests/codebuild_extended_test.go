package aws_sdk_test

import (
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
	}, 60*time.Second, 100*time.Millisecond)
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

// TestCodeBuild_ReportInsights_SDK covers DeleteReport, DescribeTestCases,
// DescribeCodeCoverages, and GetReportGroupTrend against a real report produced
// by a build whose buildspec references a report group.
func TestCodeBuild_ReportInsights_SDK(t *testing.T) {
	c := codebuildClient()
	rgName := "cb-sdk-insights-rg"
	rg, err := c.CreateReportGroup(ctx, &codebuild.CreateReportGroupInput{
		Name:         aws.String(rgName),
		Type:         cbtypes.ReportTypeTest,
		ExportConfig: &cbtypes.ReportExportConfig{ExportConfigType: cbtypes.ReportExportConfigTypeNoExport},
	})
	require.NoError(t, err)
	rgArn := aws.ToString(rg.ReportGroup.Arn)
	t.Cleanup(func() {
		_, _ = c.DeleteReportGroup(ctx, &codebuild.DeleteReportGroupInput{Arn: aws.String(rgArn), DeleteReports: true})
	})

	proj := "cb-sdk-insights-project"
	buildspec := "version: 0.2\nphases:\n  build:\n    commands:\n      - printf ok\nreports:\n  " + rgName + ":\n    files:\n      - '**/*'\n"
	cbCreateBuildspecProject(t, c, proj, buildspec)

	start, err := c.StartBuild(ctx, &codebuild.StartBuildInput{ProjectName: aws.String(proj)})
	require.NoError(t, err)
	buildID := aws.ToString(start.Build.Id)

	var reportArn string
	require.Eventually(t, func() bool {
		builds, err := c.BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{Ids: []string{buildID}})
		require.NoError(t, err)
		if len(builds.Builds) != 1 || builds.Builds[0].BuildStatus != cbtypes.StatusTypeSucceeded {
			return false
		}
		if len(builds.Builds[0].ReportArns) == 0 {
			return false
		}
		reportArn = builds.Builds[0].ReportArns[0]
		return true
	}, 10*time.Second, 100*time.Millisecond)
	require.NotEmpty(t, reportArn)

	tc, err := c.DescribeTestCases(ctx, &codebuild.DescribeTestCasesInput{ReportArn: aws.String(reportArn)})
	require.NoError(t, err)
	require.NotEmpty(t, tc.TestCases)
	assert.Equal(t, reportArn, aws.ToString(tc.TestCases[0].ReportArn))

	cov, err := c.DescribeCodeCoverages(ctx, &codebuild.DescribeCodeCoveragesInput{ReportArn: aws.String(reportArn)})
	require.NoError(t, err)
	require.NotEmpty(t, cov.CodeCoverages)
	assert.Equal(t, reportArn, aws.ToString(cov.CodeCoverages[0].ReportARN))

	trend, err := c.GetReportGroupTrend(ctx, &codebuild.GetReportGroupTrendInput{
		ReportGroupArn: aws.String(rgArn),
		TrendField:     cbtypes.ReportGroupTrendFieldTypePassRate,
	})
	require.NoError(t, err)
	require.NotNil(t, trend.Stats)
	assert.NotEmpty(t, trend.RawData)

	_, err = c.DeleteReport(ctx, &codebuild.DeleteReportInput{Arn: aws.String(reportArn)})
	require.NoError(t, err)
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
