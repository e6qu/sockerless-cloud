package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlue_MLTaskRunLifecycle_SDK exercises the machine-learning task-run family:
// start each task-run type against an ML transform, then Get/List/Cancel.
func TestGlue_MLTaskRunLifecycle_SDK(t *testing.T) {
	c := glueClient()

	create, err := c.CreateMLTransform(ctx, &glue.CreateMLTransformInput{
		Name: aws.String("glue-sdk-mltask-tf"),
		InputRecordTables: []gluetypes.GlueTable{
			{DatabaseName: aws.String("db"), TableName: aws.String("tbl")},
		},
		Parameters: &gluetypes.TransformParameters{
			TransformType: gluetypes.TransformTypeFindMatches,
		},
		Role: aws.String("arn:aws:iam::123456789012:role/GlueRole"),
	})
	require.NoError(t, err)
	id := aws.ToString(create.TransformId)
	require.NotEmpty(t, id)
	t.Cleanup(func() {
		_, _ = c.DeleteMLTransform(ctx, &glue.DeleteMLTransformInput{TransformId: aws.String(id)})
	})

	eval, err := c.StartMLEvaluationTaskRun(ctx, &glue.StartMLEvaluationTaskRunInput{
		TransformId: aws.String(id),
	})
	require.NoError(t, err)
	evalRun := aws.ToString(eval.TaskRunId)
	require.NotEmpty(t, evalRun)

	gen, err := c.StartMLLabelingSetGenerationTaskRun(ctx, &glue.StartMLLabelingSetGenerationTaskRunInput{
		TransformId:  aws.String(id),
		OutputS3Path: aws.String("s3://bucket/labels/"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(gen.TaskRunId))

	exp, err := c.StartExportLabelsTaskRun(ctx, &glue.StartExportLabelsTaskRunInput{
		TransformId:  aws.String(id),
		OutputS3Path: aws.String("s3://bucket/export/"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(exp.TaskRunId))

	imp, err := c.StartImportLabelsTaskRun(ctx, &glue.StartImportLabelsTaskRunInput{
		TransformId:      aws.String(id),
		InputS3Path:      aws.String("s3://bucket/import/"),
		ReplaceAllLabels: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(imp.TaskRunId))

	getRun, err := c.GetMLTaskRun(ctx, &glue.GetMLTaskRunInput{
		TransformId: aws.String(id),
		TaskRunId:   aws.String(evalRun),
	})
	require.NoError(t, err)
	assert.Equal(t, evalRun, aws.ToString(getRun.TaskRunId))
	assert.Equal(t, gluetypes.TaskStatusTypeSucceeded, getRun.Status)
	require.NotNil(t, getRun.Properties)
	assert.Equal(t, gluetypes.TaskTypeEvaluation, getRun.Properties.TaskType)

	runs, err := c.GetMLTaskRuns(ctx, &glue.GetMLTaskRunsInput{TransformId: aws.String(id)})
	require.NoError(t, err)
	assert.Len(t, runs.TaskRuns, 4)

	cancel, err := c.CancelMLTaskRun(ctx, &glue.CancelMLTaskRunInput{
		TransformId: aws.String(id),
		TaskRunId:   aws.String(evalRun),
	})
	require.NoError(t, err)
	assert.Equal(t, gluetypes.TaskStatusTypeStopped, cancel.Status)
}

// TestGlue_CrawlerScheduleLifecycle_SDK exercises the crawler/column-statistics
// schedule toggles plus GetCrawlerMetrics, ListCrawls and the materialized-view
// refresh family.
func TestGlue_CrawlerScheduleLifecycle_SDK(t *testing.T) {
	c := glueClient()

	const crawler = "glue-sdk-sched-crawler"
	_, err := c.CreateCrawler(ctx, &glue.CreateCrawlerInput{
		Name:     aws.String(crawler),
		Role:     aws.String("arn:aws:iam::123456789012:role/glue-crawler-role"),
		Schedule: aws.String("cron(15 12 * * ? *)"),
		Targets: &gluetypes.CrawlerTargets{
			S3Targets: []gluetypes.S3Target{{Path: aws.String("s3://bucket/data/")}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCrawler(ctx, &glue.DeleteCrawlerInput{Name: aws.String(crawler)})
	})

	_, err = c.StopCrawlerSchedule(ctx, &glue.StopCrawlerScheduleInput{CrawlerName: aws.String(crawler)})
	require.NoError(t, err)
	_, err = c.StartCrawlerSchedule(ctx, &glue.StartCrawlerScheduleInput{CrawlerName: aws.String(crawler)})
	require.NoError(t, err)
	_, err = c.UpdateCrawlerSchedule(ctx, &glue.UpdateCrawlerScheduleInput{
		CrawlerName: aws.String(crawler),
		Schedule:    aws.String("cron(0 0 * * ? *)"),
	})
	require.NoError(t, err)

	get, err := c.GetCrawler(ctx, &glue.GetCrawlerInput{Name: aws.String(crawler)})
	require.NoError(t, err)
	require.NotNil(t, get.Crawler.Schedule)
	assert.Equal(t, "cron(0 0 * * ? *)", aws.ToString(get.Crawler.Schedule.ScheduleExpression))

	metrics, err := c.GetCrawlerMetrics(ctx, &glue.GetCrawlerMetricsInput{
		CrawlerNameList: []string{crawler},
	})
	require.NoError(t, err)
	require.Len(t, metrics.CrawlerMetricsList, 1)
	assert.Equal(t, crawler, aws.ToString(metrics.CrawlerMetricsList[0].CrawlerName))

	crawls, err := c.ListCrawls(ctx, &glue.ListCrawlsInput{CrawlerName: aws.String(crawler)})
	require.NoError(t, err)
	assert.Empty(t, crawls.Crawls)

	// Column-statistics schedule needs a real table to anchor to.
	const db = "glue-sdk-sched-db"
	const tbl = "glue-sdk-sched-tbl"
	_, err = c.CreateDatabase(ctx, &glue.CreateDatabaseInput{
		DatabaseInput: &gluetypes.DatabaseInput{Name: aws.String(db)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDatabase(ctx, &glue.DeleteDatabaseInput{Name: aws.String(db)})
	})
	_, err = c.CreateTable(ctx, &glue.CreateTableInput{
		DatabaseName: aws.String(db),
		TableInput:   &gluetypes.TableInput{Name: aws.String(tbl)},
	})
	require.NoError(t, err)

	_, err = c.StartColumnStatisticsTaskRunSchedule(ctx, &glue.StartColumnStatisticsTaskRunScheduleInput{
		DatabaseName: aws.String(db),
		TableName:    aws.String(tbl),
	})
	require.NoError(t, err)
	_, err = c.StopColumnStatisticsTaskRunSchedule(ctx, &glue.StopColumnStatisticsTaskRunScheduleInput{
		DatabaseName: aws.String(db),
		TableName:    aws.String(tbl),
	})
	require.NoError(t, err)

	// Materialized-view refresh task runs.
	start, err := c.StartMaterializedViewRefreshTaskRun(ctx, &glue.StartMaterializedViewRefreshTaskRunInput{
		CatalogId:    aws.String("123456789012"),
		DatabaseName: aws.String(db),
		TableName:    aws.String(tbl),
		FullRefresh:  aws.Bool(true),
	})
	require.NoError(t, err)
	mvRun := aws.ToString(start.MaterializedViewRefreshTaskRunId)
	require.NotEmpty(t, mvRun)

	gotMV, err := c.GetMaterializedViewRefreshTaskRun(ctx, &glue.GetMaterializedViewRefreshTaskRunInput{
		CatalogId:                        aws.String("123456789012"),
		MaterializedViewRefreshTaskRunId: aws.String(mvRun),
	})
	require.NoError(t, err)
	require.NotNil(t, gotMV.MaterializedViewRefreshTaskRun)
	assert.Equal(t, mvRun, aws.ToString(gotMV.MaterializedViewRefreshTaskRun.MaterializedViewRefreshTaskRunId))

	listMV, err := c.ListMaterializedViewRefreshTaskRuns(ctx, &glue.ListMaterializedViewRefreshTaskRunsInput{
		CatalogId:    aws.String("123456789012"),
		DatabaseName: aws.String(db),
		TableName:    aws.String(tbl),
	})
	require.NoError(t, err)
	assert.Len(t, listMV.MaterializedViewRefreshTaskRuns, 1)

	_, err = c.StopMaterializedViewRefreshTaskRun(ctx, &glue.StopMaterializedViewRefreshTaskRunInput{
		CatalogId:    aws.String("123456789012"),
		DatabaseName: aws.String(db),
		TableName:    aws.String(tbl),
	})
	require.NoError(t, err)
}

// TestGlue_WorkflowRunProperties_SDK exercises workflow-run reads/writes plus
// UpdateWorkflow, ListTriggers, UpdateTrigger and the job-bookmark family.
func TestGlue_WorkflowRunProperties_SDK(t *testing.T) {
	c := glueClient()

	const wf = "glue-sdk-wfrun"
	_, err := c.CreateWorkflow(ctx, &glue.CreateWorkflowInput{Name: aws.String(wf)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteWorkflow(ctx, &glue.DeleteWorkflowInput{Name: aws.String(wf)})
	})

	start, err := c.StartWorkflowRun(ctx, &glue.StartWorkflowRunInput{
		Name:          aws.String(wf),
		RunProperties: map[string]string{"k": "v"},
	})
	require.NoError(t, err)
	runID := aws.ToString(start.RunId)
	require.NotEmpty(t, runID)

	runs, err := c.GetWorkflowRuns(ctx, &glue.GetWorkflowRunsInput{Name: aws.String(wf)})
	require.NoError(t, err)
	assert.Len(t, runs.Runs, 1)

	_, err = c.PutWorkflowRunProperties(ctx, &glue.PutWorkflowRunPropertiesInput{
		Name:          aws.String(wf),
		RunId:         aws.String(runID),
		RunProperties: map[string]string{"k": "v2", "extra": "1"},
	})
	require.NoError(t, err)

	props, err := c.GetWorkflowRunProperties(ctx, &glue.GetWorkflowRunPropertiesInput{
		Name:  aws.String(wf),
		RunId: aws.String(runID),
	})
	require.NoError(t, err)
	assert.Equal(t, "v2", props.RunProperties["k"])
	assert.Equal(t, "1", props.RunProperties["extra"])

	resume, err := c.ResumeWorkflowRun(ctx, &glue.ResumeWorkflowRunInput{
		Name:    aws.String(wf),
		RunId:   aws.String(runID),
		NodeIds: []string{"node-1"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(resume.RunId))
	assert.Equal(t, []string{"node-1"}, resume.NodeIds)

	_, err = c.StopWorkflowRun(ctx, &glue.StopWorkflowRunInput{
		Name:  aws.String(wf),
		RunId: aws.String(runID),
	})
	require.NoError(t, err)

	upd, err := c.UpdateWorkflow(ctx, &glue.UpdateWorkflowInput{
		Name:                 aws.String(wf),
		Description:          aws.String("updated"),
		DefaultRunProperties: map[string]string{"env": "prod"},
		MaxConcurrentRuns:    aws.Int32(3),
	})
	require.NoError(t, err)
	assert.Equal(t, wf, aws.ToString(upd.Name))

	// Triggers: ListTriggers + UpdateTrigger.
	const trig = "glue-sdk-wfrun-trigger"
	_, err = c.CreateTrigger(ctx, &glue.CreateTriggerInput{
		Name:         aws.String(trig),
		Type:         gluetypes.TriggerTypeScheduled,
		Schedule:     aws.String("cron(15 12 * * ? *)"),
		WorkflowName: aws.String(wf),
		Actions:      []gluetypes.Action{{JobName: aws.String("some-job")}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteTrigger(ctx, &glue.DeleteTriggerInput{Name: aws.String(trig)})
	})

	listT, err := c.ListTriggers(ctx, &glue.ListTriggersInput{})
	require.NoError(t, err)
	assert.Contains(t, listT.TriggerNames, trig)

	updT, err := c.UpdateTrigger(ctx, &glue.UpdateTriggerInput{
		Name: aws.String(trig),
		TriggerUpdate: &gluetypes.TriggerUpdate{
			Description: aws.String("updated trigger"),
			Schedule:    aws.String("cron(0 0 * * ? *)"),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, updT.Trigger)
	assert.Equal(t, "updated trigger", aws.ToString(updT.Trigger.Description))
	assert.Equal(t, "cron(0 0 * * ? *)", aws.ToString(updT.Trigger.Schedule))

	// Job bookmarks.
	const job = "glue-sdk-wfrun-job"
	_, err = c.CreateJob(ctx, &glue.CreateJobInput{
		Name:    aws.String(job),
		Role:    aws.String("arn:aws:iam::123456789012:role/GlueRole"),
		Command: &gluetypes.JobCommand{Name: aws.String("glueetl")},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteJob(ctx, &glue.DeleteJobInput{JobName: aws.String(job)})
	})

	bm, err := c.GetJobBookmark(ctx, &glue.GetJobBookmarkInput{JobName: aws.String(job)})
	require.NoError(t, err)
	require.NotNil(t, bm.JobBookmarkEntry)
	assert.Equal(t, job, aws.ToString(bm.JobBookmarkEntry.JobName))

	reset, err := c.ResetJobBookmark(ctx, &glue.ResetJobBookmarkInput{JobName: aws.String(job)})
	require.NoError(t, err)
	require.NotNil(t, reset.JobBookmarkEntry)
	assert.Equal(t, job, aws.ToString(reset.JobBookmarkEntry.JobName))
}

// TestGlue_ConnectionTypeRegistry_SDK exercises the connection-type registry and
// TestConnection against a stored connection.
func TestGlue_ConnectionTypeRegistry_SDK(t *testing.T) {
	c := glueClient()

	const ctName = "REST-glue-sdk-conntype"
	reg, err := c.RegisterConnectionType(ctx, &glue.RegisterConnectionTypeInput{
		ConnectionType:                       aws.String(ctName),
		IntegrationType:                      gluetypes.IntegrationTypeRest,
		Description:                          aws.String("sdk connection type"),
		ConnectionProperties:                 &gluetypes.ConnectionPropertiesConfiguration{},
		ConnectorAuthenticationConfiguration: &gluetypes.ConnectorAuthenticationConfiguration{AuthenticationTypes: []gluetypes.AuthenticationType{gluetypes.AuthenticationTypeBasic}},
		RestConfiguration:                    &gluetypes.RestConfiguration{},
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(reg.ConnectionTypeArn))
	t.Cleanup(func() {
		_, _ = c.DeleteConnectionType(ctx, &glue.DeleteConnectionTypeInput{ConnectionType: aws.String(ctName)})
	})

	desc, err := c.DescribeConnectionType(ctx, &glue.DescribeConnectionTypeInput{
		ConnectionType: aws.String(ctName),
	})
	require.NoError(t, err)
	assert.Equal(t, ctName, aws.ToString(desc.ConnectionType))
	require.NotNil(t, desc.Capabilities)
	assert.NotEmpty(t, desc.Capabilities.SupportedAuthenticationTypes)

	listCT, err := c.ListConnectionTypes(ctx, &glue.ListConnectionTypesInput{})
	require.NoError(t, err)
	foundCT := false
	for _, b := range listCT.ConnectionTypes {
		if string(b.ConnectionType) == ctName {
			foundCT = true
		}
	}
	assert.True(t, foundCT)

	// TestConnection against a stored connection.
	const conn = "glue-sdk-conntype-conn"
	_, err = c.CreateConnection(ctx, &glue.CreateConnectionInput{
		ConnectionInput: &gluetypes.ConnectionInput{
			Name:                 aws.String(conn),
			ConnectionType:       gluetypes.ConnectionTypeJdbc,
			ConnectionProperties: map[string]string{"JDBC_CONNECTION_URL": "jdbc:mysql://host/db"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteConnection(ctx, &glue.DeleteConnectionInput{ConnectionName: aws.String(conn)})
	})

	_, err = c.TestConnection(ctx, &glue.TestConnectionInput{
		ConnectionName: aws.String(conn),
	})
	require.NoError(t, err)
}
