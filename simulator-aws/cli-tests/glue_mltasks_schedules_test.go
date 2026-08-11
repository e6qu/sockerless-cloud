package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlueMLTaskRunCLI exercises the machine-learning task-run family via the CLI.
func TestGlueMLTaskRunCLI(t *testing.T) {
	out := runCLI(t, awsCLI("glue", "create-ml-transform",
		"--name", "glue-cli-mltask-tf",
		"--input-record-tables", `[{"DatabaseName":"db","TableName":"tbl"}]`,
		"--parameters", `{"TransformType":"FIND_MATCHES"}`,
		"--role", "arn:aws:iam::123456789012:role/GlueRole",
	))
	var created struct {
		TransformId string `json:"TransformId"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.TransformId)
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-ml-transform", "--transform-id", created.TransformId))
	})

	out = runCLI(t, awsCLI("glue", "start-ml-evaluation-task-run", "--transform-id", created.TransformId))
	var eval struct {
		TaskRunId string `json:"TaskRunId"`
	}
	parseJSON(t, out, &eval)
	require.NotEmpty(t, eval.TaskRunId)

	runCLI(t, awsCLI("glue", "start-ml-labeling-set-generation-task-run",
		"--transform-id", created.TransformId, "--output-s3-path", "s3://bucket/labels/"))
	runCLI(t, awsCLI("glue", "start-export-labels-task-run",
		"--transform-id", created.TransformId, "--output-s3-path", "s3://bucket/export/"))
	runCLI(t, awsCLI("glue", "start-import-labels-task-run",
		"--transform-id", created.TransformId, "--input-s3-path", "s3://bucket/import/", "--replace-all-labels"))

	out = runCLI(t, awsCLI("glue", "get-ml-task-run",
		"--transform-id", created.TransformId, "--task-run-id", eval.TaskRunId))
	var getRun struct {
		TaskRunId string `json:"TaskRunId"`
		Status    string `json:"Status"`
	}
	parseJSON(t, out, &getRun)
	assert.Equal(t, eval.TaskRunId, getRun.TaskRunId)
	assert.Equal(t, "SUCCEEDED", getRun.Status)

	out = runCLI(t, awsCLI("glue", "get-ml-task-runs", "--transform-id", created.TransformId))
	var runs struct {
		TaskRuns []struct {
			TaskRunId string `json:"TaskRunId"`
		} `json:"TaskRuns"`
	}
	parseJSON(t, out, &runs)
	assert.Len(t, runs.TaskRuns, 4)

	out = runCLI(t, awsCLI("glue", "cancel-ml-task-run",
		"--transform-id", created.TransformId, "--task-run-id", eval.TaskRunId))
	var cancel struct {
		Status string `json:"Status"`
	}
	parseJSON(t, out, &cancel)
	assert.Equal(t, "STOPPED", cancel.Status)
}

// TestGlueCrawlerScheduleCLI exercises crawler/column-statistics schedule toggles,
// GetCrawlerMetrics, ListCrawls and the materialized-view refresh family.
func TestGlueCrawlerScheduleCLI(t *testing.T) {
	const crawler = "glue-cli-sched-crawler"
	runCLI(t, awsCLI("glue", "create-crawler",
		"--name", crawler,
		"--role", "arn:aws:iam::123456789012:role/glue-crawler-role",
		"--schedule", "cron(15 12 * * ? *)",
		"--targets", `{"S3Targets":[{"Path":"s3://bucket/data/"}]}`,
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-crawler", "--name", crawler))
	})

	runCLI(t, awsCLI("glue", "stop-crawler-schedule", "--crawler-name", crawler))
	runCLI(t, awsCLI("glue", "start-crawler-schedule", "--crawler-name", crawler))
	runCLI(t, awsCLI("glue", "update-crawler-schedule",
		"--crawler-name", crawler, "--schedule", "cron(0 0 * * ? *)"))

	out := runCLI(t, awsCLI("glue", "get-crawler-metrics", "--crawler-name-list", crawler))
	var metrics struct {
		CrawlerMetricsList []struct {
			CrawlerName string `json:"CrawlerName"`
		} `json:"CrawlerMetricsList"`
	}
	parseJSON(t, out, &metrics)
	require.Len(t, metrics.CrawlerMetricsList, 1)
	assert.Equal(t, crawler, metrics.CrawlerMetricsList[0].CrawlerName)

	out = runCLI(t, awsCLI("glue", "list-crawls", "--crawler-name", crawler))
	var crawls struct {
		Crawls []any `json:"Crawls"`
	}
	parseJSON(t, out, &crawls)
	assert.Empty(t, crawls.Crawls)

	const db = "glue-cli-sched-db"
	const tbl = "glue-cli-sched-tbl"
	runCLI(t, awsCLI("glue", "create-database", "--database-input", `{"Name":"`+db+`"}`))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-database", "--name", db))
	})
	runCLI(t, awsCLI("glue", "create-table", "--database-name", db,
		"--table-input", `{"Name":"`+tbl+`"}`))

	runCLI(t, awsCLI("glue", "start-column-statistics-task-run-schedule",
		"--database-name", db, "--table-name", tbl))
	runCLI(t, awsCLI("glue", "stop-column-statistics-task-run-schedule",
		"--database-name", db, "--table-name", tbl))

	out = runCLI(t, awsCLI("glue", "start-materialized-view-refresh-task-run",
		"--catalog-id", "123456789012", "--database-name", db, "--table-name", tbl, "--full-refresh"))
	var mvStart struct {
		MaterializedViewRefreshTaskRunId string `json:"MaterializedViewRefreshTaskRunId"`
	}
	parseJSON(t, out, &mvStart)
	require.NotEmpty(t, mvStart.MaterializedViewRefreshTaskRunId)

	out = runCLI(t, awsCLI("glue", "get-materialized-view-refresh-task-run",
		"--catalog-id", "123456789012",
		"--materialized-view-refresh-task-run-id", mvStart.MaterializedViewRefreshTaskRunId))
	var mvGet struct {
		MaterializedViewRefreshTaskRun struct {
			MaterializedViewRefreshTaskRunId string `json:"MaterializedViewRefreshTaskRunId"`
		} `json:"MaterializedViewRefreshTaskRun"`
	}
	parseJSON(t, out, &mvGet)
	assert.Equal(t, mvStart.MaterializedViewRefreshTaskRunId,
		mvGet.MaterializedViewRefreshTaskRun.MaterializedViewRefreshTaskRunId)

	out = runCLI(t, awsCLI("glue", "list-materialized-view-refresh-task-runs",
		"--catalog-id", "123456789012", "--database-name", db, "--table-name", tbl))
	var mvList struct {
		MaterializedViewRefreshTaskRuns []any `json:"MaterializedViewRefreshTaskRuns"`
	}
	parseJSON(t, out, &mvList)
	assert.Len(t, mvList.MaterializedViewRefreshTaskRuns, 1)

	runCLI(t, awsCLI("glue", "stop-materialized-view-refresh-task-run",
		"--catalog-id", "123456789012", "--database-name", db, "--table-name", tbl))
}

// TestGlueWorkflowRunCLI exercises workflow-run properties, UpdateWorkflow,
// ListTriggers/UpdateTrigger and the job-bookmark family.
func TestGlueWorkflowRunCLI(t *testing.T) {
	const wf = "glue-cli-wfrun"
	runCLI(t, awsCLI("glue", "create-workflow", "--name", wf))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-workflow", "--name", wf))
	})

	out := runCLI(t, awsCLI("glue", "start-workflow-run", "--name", wf,
		"--run-properties", `{"k":"v"}`))
	var start struct {
		RunId string `json:"RunId"`
	}
	parseJSON(t, out, &start)
	require.NotEmpty(t, start.RunId)

	out = runCLI(t, awsCLI("glue", "get-workflow-runs", "--name", wf))
	var runs struct {
		Runs []any `json:"Runs"`
	}
	parseJSON(t, out, &runs)
	assert.Len(t, runs.Runs, 1)

	runCLI(t, awsCLI("glue", "put-workflow-run-properties", "--name", wf,
		"--run-id", start.RunId, "--run-properties", `{"k":"v2","extra":"1"}`))

	out = runCLI(t, awsCLI("glue", "get-workflow-run-properties", "--name", wf, "--run-id", start.RunId))
	var props struct {
		RunProperties map[string]string `json:"RunProperties"`
	}
	parseJSON(t, out, &props)
	assert.Equal(t, "v2", props.RunProperties["k"])
	assert.Equal(t, "1", props.RunProperties["extra"])

	out = runCLI(t, awsCLI("glue", "resume-workflow-run", "--name", wf,
		"--run-id", start.RunId, "--node-ids", "node-1"))
	var resume struct {
		RunId string `json:"RunId"`
	}
	parseJSON(t, out, &resume)
	require.NotEmpty(t, resume.RunId)

	runCLI(t, awsCLI("glue", "stop-workflow-run", "--name", wf, "--run-id", start.RunId))

	out = runCLI(t, awsCLI("glue", "update-workflow", "--name", wf,
		"--description", "updated", "--default-run-properties", `{"env":"prod"}`,
		"--max-concurrent-runs", "3"))
	var upd struct {
		Name string `json:"Name"`
	}
	parseJSON(t, out, &upd)
	assert.Equal(t, wf, upd.Name)

	const trig = "glue-cli-wfrun-trigger"
	runCLI(t, awsCLI("glue", "create-trigger", "--name", trig,
		"--type", "SCHEDULED", "--schedule", "cron(15 12 * * ? *)",
		"--workflow-name", wf, "--actions", `[{"JobName":"some-job"}]`))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-trigger", "--name", trig))
	})

	out = runCLI(t, awsCLI("glue", "list-triggers"))
	var listT struct {
		TriggerNames []string `json:"TriggerNames"`
	}
	parseJSON(t, out, &listT)
	assert.Contains(t, listT.TriggerNames, trig)

	out = runCLI(t, awsCLI("glue", "update-trigger", "--name", trig,
		"--trigger-update", `{"Description":"updated trigger","Schedule":"cron(0 0 * * ? *)"}`))
	var updT struct {
		Trigger struct {
			Description string `json:"Description"`
			Schedule    string `json:"Schedule"`
		} `json:"Trigger"`
	}
	parseJSON(t, out, &updT)
	assert.Equal(t, "updated trigger", updT.Trigger.Description)
	assert.Equal(t, "cron(0 0 * * ? *)", updT.Trigger.Schedule)

	const job = "glue-cli-wfrun-job"
	runCLI(t, awsCLI("glue", "create-job", "--name", job,
		"--role", "arn:aws:iam::123456789012:role/GlueRole",
		"--command", `{"Name":"glueetl"}`))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-job", "--job-name", job))
	})

	out = runCLI(t, awsCLI("glue", "get-job-bookmark", "--job-name", job))
	var bm struct {
		JobBookmarkEntry struct {
			JobName string `json:"JobName"`
		} `json:"JobBookmarkEntry"`
	}
	parseJSON(t, out, &bm)
	assert.Equal(t, job, bm.JobBookmarkEntry.JobName)

	out = runCLI(t, awsCLI("glue", "reset-job-bookmark", "--job-name", job))
	var reset struct {
		JobBookmarkEntry struct {
			JobName string `json:"JobName"`
		} `json:"JobBookmarkEntry"`
	}
	parseJSON(t, out, &reset)
	assert.Equal(t, job, reset.JobBookmarkEntry.JobName)
}

// TestGlueConnectionTypeCLI exercises the connection-type registry and
// TestConnection via the CLI.
func TestGlueConnectionTypeCLI(t *testing.T) {
	const ctName = "REST-glue-cli-conntype"
	out := runCLI(t, awsCLI("glue", "register-connection-type",
		"--connection-type", ctName,
		"--integration-type", "REST",
		"--description", "cli connection type",
		"--connection-properties", `{}`,
		"--connector-authentication-configuration", `{"AuthenticationTypes":["BASIC"]}`,
		"--rest-configuration", `{}`,
	))
	var reg struct {
		ConnectionTypeArn string `json:"ConnectionTypeArn"`
	}
	parseJSON(t, out, &reg)
	require.NotEmpty(t, reg.ConnectionTypeArn)
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-connection-type", "--connection-type", ctName))
	})

	out = runCLI(t, awsCLI("glue", "describe-connection-type", "--connection-type", ctName))
	var desc struct {
		ConnectionType string `json:"ConnectionType"`
		Capabilities   struct {
			SupportedAuthenticationTypes []string `json:"SupportedAuthenticationTypes"`
		} `json:"Capabilities"`
	}
	parseJSON(t, out, &desc)
	assert.Equal(t, ctName, desc.ConnectionType)
	assert.NotEmpty(t, desc.Capabilities.SupportedAuthenticationTypes)

	out = runCLI(t, awsCLI("glue", "list-connection-types"))
	var listCT struct {
		ConnectionTypes []struct {
			ConnectionType string `json:"ConnectionType"`
		} `json:"ConnectionTypes"`
	}
	parseJSON(t, out, &listCT)
	foundCT := false
	for _, b := range listCT.ConnectionTypes {
		if b.ConnectionType == ctName {
			foundCT = true
		}
	}
	assert.True(t, foundCT)

	const conn = "glue-cli-conntype-conn"
	runCLI(t, awsCLI("glue", "create-connection",
		"--connection-input", `{"Name":"`+conn+`","ConnectionType":"JDBC","ConnectionProperties":{"JDBC_CONNECTION_URL":"jdbc:mysql://host/db"}}`,
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("glue", "delete-connection", "--connection-name", conn))
	})

	runCLI(t, awsCLI("glue", "test-connection", "--connection-name", conn))
}
