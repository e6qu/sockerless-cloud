package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogsIntegrationAndLookupCLI exercises OpenSearch integrations and lookup
// tables through the aws CLI.
func TestLogsIntegrationAndLookupCLI(t *testing.T) {
	// OpenSearch integration.
	integName := "cli-opensearch-integration"
	defer runCLIIgnore(awsCLI("logs", "delete-integration",
		"--integration-name", integName, "--force"))

	resourceConfig := `{"openSearchResourceConfig":{"dataSourceRoleArn":"arn:aws:iam::123456789012:role/cwl-os","dashboardViewerPrincipals":["arn:aws:iam::123456789012:user/viewer"],"retentionDays":90}}`
	runCLI(t, awsCLI("logs", "put-integration",
		"--integration-name", integName,
		"--integration-type", "OPENSEARCH",
		"--resource-config", resourceConfig))

	getInteg := runCLI(t, awsCLI("logs", "get-integration",
		"--integration-name", integName, "--output", "json"))
	var gi struct {
		IntegrationName   string `json:"integrationName"`
		IntegrationType   string `json:"integrationType"`
		IntegrationStatus string `json:"integrationStatus"`
	}
	parseJSON(t, getInteg, &gi)
	assert.Equal(t, integName, gi.IntegrationName)
	assert.Equal(t, "OPENSEARCH", gi.IntegrationType)

	listInteg := runCLI(t, awsCLI("logs", "list-integrations",
		"--integration-name-prefix", "cli-opensearch", "--output", "json"))
	var li struct {
		IntegrationSummaries []struct {
			IntegrationName string `json:"integrationName"`
		} `json:"integrationSummaries"`
	}
	parseJSON(t, listInteg, &li)
	foundInteg := false
	for _, s := range li.IntegrationSummaries {
		if s.IntegrationName == integName {
			foundInteg = true
		}
	}
	assert.True(t, foundInteg, "integration should be listed")
	runCLI(t, awsCLI("logs", "delete-integration", "--integration-name", integName, "--force"))

	// Lookup table.
	ltName := "cli_lookup_table"
	body := "key,value\nfoo,bar\n"
	create := runCLI(t, awsCLI("logs", "create-lookup-table",
		"--lookup-table-name", ltName,
		"--table-body", body,
		"--description", "cli lookup", "--output", "json"))
	var ct struct {
		LookupTableArn string `json:"lookupTableArn"`
		CreatedAt      int64  `json:"createdAt"`
	}
	parseJSON(t, create, &ct)
	require.NotEmpty(t, ct.LookupTableArn)
	defer runCLIIgnore(awsCLI("logs", "delete-lookup-table", "--lookup-table-arn", ct.LookupTableArn))

	getLT := runCLI(t, awsCLI("logs", "get-lookup-table",
		"--lookup-table-arn", ct.LookupTableArn, "--output", "json"))
	var glt struct {
		LookupTableName string `json:"lookupTableName"`
		TableBody       string `json:"tableBody"`
		SizeBytes       int64  `json:"sizeBytes"`
	}
	parseJSON(t, getLT, &glt)
	assert.Equal(t, ltName, glt.LookupTableName)
	assert.Equal(t, body, glt.TableBody)

	runCLI(t, awsCLI("logs", "update-lookup-table",
		"--lookup-table-arn", ct.LookupTableArn,
		"--table-body", "key,value\nbaz,qux\n"))

	descLT := runCLI(t, awsCLI("logs", "describe-lookup-tables",
		"--lookup-table-name-prefix", "cli_lookup", "--output", "json"))
	var dlt struct {
		LookupTables []struct {
			LookupTableArn string `json:"lookupTableArn"`
		} `json:"lookupTables"`
	}
	parseJSON(t, descLT, &dlt)
	foundLT := false
	for _, lt := range dlt.LookupTables {
		if lt.LookupTableArn == ct.LookupTableArn {
			foundLT = true
		}
	}
	assert.True(t, foundLT, "lookup table should be described")
	runCLI(t, awsCLI("logs", "delete-lookup-table", "--lookup-table-arn", ct.LookupTableArn))
}

// TestLogsScheduledQueryAndTransformerCLI exercises scheduled queries and
// per-log-group transformers through the aws CLI.
func TestLogsScheduledQueryAndTransformerCLI(t *testing.T) {
	// Scheduled query.
	sqName := "cli-scheduled-query"
	dest := `{"s3Configuration":{"destinationIdentifier":"s3://cli-sq-bucket","roleArn":"arn:aws:iam::123456789012:role/sq-role"}}`
	create := runCLI(t, awsCLI("logs", "create-scheduled-query",
		"--name", sqName,
		"--query-language", "CWLI",
		"--query-string", "fields @timestamp, @message",
		"--schedule-expression", "rate(1 hour)",
		"--execution-role-arn", "arn:aws:iam::123456789012:role/sq-exec",
		"--destination-configuration", dest,
		"--state", "ENABLED", "--output", "json"))
	var cs struct {
		ScheduledQueryArn string `json:"scheduledQueryArn"`
		State             string `json:"state"`
	}
	parseJSON(t, create, &cs)
	require.NotEmpty(t, cs.ScheduledQueryArn)
	assert.Equal(t, "ENABLED", cs.State)
	defer runCLIIgnore(awsCLI("logs", "delete-scheduled-query", "--identifier", cs.ScheduledQueryArn))

	getSQ := runCLI(t, awsCLI("logs", "get-scheduled-query",
		"--identifier", cs.ScheduledQueryArn, "--output", "json"))
	var gsq struct {
		Name               string `json:"name"`
		ScheduleExpression string `json:"scheduleExpression"`
	}
	parseJSON(t, getSQ, &gsq)
	assert.Equal(t, sqName, gsq.Name)

	runCLI(t, awsCLI("logs", "update-scheduled-query",
		"--identifier", cs.ScheduledQueryArn,
		"--query-language", "CWLI",
		"--query-string", "fields @timestamp, @message",
		"--execution-role-arn", "arn:aws:iam::123456789012:role/sq-exec",
		"--schedule-expression", "rate(2 hours)"))

	listSQ := runCLI(t, awsCLI("logs", "list-scheduled-queries", "--output", "json"))
	var lsq struct {
		ScheduledQueries []struct {
			ScheduledQueryArn string `json:"scheduledQueryArn"`
		} `json:"scheduledQueries"`
	}
	parseJSON(t, listSQ, &lsq)
	foundSQ := false
	for _, sq := range lsq.ScheduledQueries {
		if sq.ScheduledQueryArn == cs.ScheduledQueryArn {
			foundSQ = true
		}
	}
	assert.True(t, foundSQ, "scheduled query should be listed")

	histSQ := runCLI(t, awsCLI("logs", "get-scheduled-query-history",
		"--identifier", cs.ScheduledQueryArn,
		"--start-time", "0",
		"--end-time", "99999999999999",
		"--output", "json"))
	var hsq struct {
		ScheduledQueryArn string `json:"scheduledQueryArn"`
	}
	parseJSON(t, histSQ, &hsq)
	assert.Equal(t, cs.ScheduledQueryArn, hsq.ScheduledQueryArn)
	runCLI(t, awsCLI("logs", "delete-scheduled-query", "--identifier", cs.ScheduledQueryArn))

	// Transformer.
	lg := "cli-transformer-lg"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", lg))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", lg))
	defer runCLIIgnore(awsCLI("logs", "delete-transformer", "--log-group-identifier", lg))

	processors := `[{"parseJSON":{}}]`
	runCLI(t, awsCLI("logs", "put-transformer",
		"--log-group-identifier", lg,
		"--transformer-config", processors))

	getT := runCLI(t, awsCLI("logs", "get-transformer",
		"--log-group-identifier", lg, "--output", "json"))
	var gt struct {
		LogGroupIdentifier string `json:"logGroupIdentifier"`
	}
	parseJSON(t, getT, &gt)
	assert.Equal(t, lg, gt.LogGroupIdentifier)

	testT := runCLI(t, awsCLI("logs", "test-transformer",
		"--transformer-config", processors,
		"--log-event-messages", `{"level":"INFO"}`, "--output", "json"))
	var tt struct {
		TransformedLogs []struct {
			EventNumber int64 `json:"eventNumber"`
		} `json:"transformedLogs"`
	}
	parseJSON(t, testT, &tt)
	require.Len(t, tt.TransformedLogs, 1)
	runCLI(t, awsCLI("logs", "delete-transformer", "--log-group-identifier", lg))
}

// TestLogsImportAndStructureCLI exercises import tasks, log-structure reads,
// deletion protection, anomalies, and log-group listing through the aws CLI.
func TestLogsImportAndStructureCLI(t *testing.T) {
	// Import task.
	create := runCLI(t, awsCLI("logs", "create-import-task",
		"--import-source-arn", "arn:aws:s3:::cli-import-bucket",
		"--import-role-arn", "arn:aws:iam::123456789012:role/import-role",
		"--output", "json"))
	var ci struct {
		ImportId             string `json:"importId"`
		ImportDestinationArn string `json:"importDestinationArn"`
	}
	parseJSON(t, create, &ci)
	require.NotEmpty(t, ci.ImportId)

	descImp := runCLI(t, awsCLI("logs", "describe-import-tasks",
		"--import-id", ci.ImportId, "--output", "json"))
	var di struct {
		Imports []struct {
			ImportStatus string `json:"importStatus"`
		} `json:"imports"`
	}
	parseJSON(t, descImp, &di)
	require.Len(t, di.Imports, 1)
	assert.Equal(t, "COMPLETED", di.Imports[0].ImportStatus)

	batchImp := runCLI(t, awsCLI("logs", "describe-import-task-batches",
		"--import-id", ci.ImportId, "--output", "json"))
	var bi struct {
		ImportId string `json:"importId"`
	}
	parseJSON(t, batchImp, &bi)
	assert.Equal(t, ci.ImportId, bi.ImportId)

	cancelImp := runCLI(t, awsCLI("logs", "cancel-import-task",
		"--import-id", ci.ImportId, "--output", "json"))
	var cai struct {
		ImportId string `json:"importId"`
	}
	parseJSON(t, cancelImp, &cai)
	assert.Equal(t, ci.ImportId, cai.ImportId)

	// Log-structure reads + deletion protection over a real log group.
	lg := "cli-structure-lg"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", lg))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", lg))

	fields := runCLI(t, awsCLI("logs", "get-log-group-fields",
		"--log-group-name", lg, "--output", "json"))
	var gf struct {
		LogGroupFields []struct {
			Name string `json:"name"`
		} `json:"logGroupFields"`
	}
	parseJSON(t, fields, &gf)
	assert.Empty(t, gf.LogGroupFields, "log group with no events has no fields")

	rec := runCLI(t, awsCLI("logs", "get-log-record",
		"--log-record-pointer", "missing|stream|0", "--output", "json"))
	var gr struct {
		LogRecord map[string]string `json:"logRecord"`
	}
	parseJSON(t, rec, &gr)
	assert.Empty(t, gr.LogRecord, "unknown record pointer resolves empty")

	runCLI(t, awsCLI("logs", "put-log-group-deletion-protection",
		"--log-group-identifier", lg,
		"--deletion-protection-enabled"))

	// Anomalies over a real detector.
	detOut := runCLI(t, awsCLI("logs", "create-log-anomaly-detector",
		"--log-group-arn-list", "arn:aws:logs:us-east-1:123456789012:log-group:"+lg,
		"--detector-name", "cli-anomaly-detector",
		"--evaluation-frequency", "ONE_HOUR", "--output", "json"))
	var det struct {
		AnomalyDetectorArn string `json:"anomalyDetectorArn"`
	}
	parseJSON(t, detOut, &det)
	require.NotEmpty(t, det.AnomalyDetectorArn)
	defer runCLIIgnore(awsCLI("logs", "delete-log-anomaly-detector",
		"--anomaly-detector-arn", det.AnomalyDetectorArn))

	runCLI(t, awsCLI("logs", "update-log-anomaly-detector",
		"--anomaly-detector-arn", det.AnomalyDetectorArn,
		"--evaluation-frequency", "FIFTEEN_MIN",
		"--enabled"))

	listAnom := runCLI(t, awsCLI("logs", "list-anomalies",
		"--anomaly-detector-arn", det.AnomalyDetectorArn, "--output", "json"))
	var la struct {
		Anomalies []any `json:"anomalies"`
	}
	parseJSON(t, listAnom, &la)
	assert.Empty(t, la.Anomalies, "fresh detector has no anomalies")

	runCLI(t, awsCLI("logs", "update-anomaly",
		"--anomaly-detector-arn", det.AnomalyDetectorArn,
		"--pattern-id", "abcdefabcdefabcdefabcdefabcdef123456",
		"--suppression-type", "INFINITE"))

	// Log-group listing.
	listLG := runCLI(t, awsCLI("logs", "list-log-groups",
		"--log-group-name-pattern", "cli-structure", "--output", "json"))
	var llg struct {
		LogGroups []struct {
			LogGroupName string `json:"logGroupName"`
		} `json:"logGroups"`
	}
	parseJSON(t, listLG, &llg)
	foundLG := false
	for _, g := range llg.LogGroups {
		if g.LogGroupName == lg {
			foundLG = true
		}
	}
	assert.True(t, foundLG, "log group should appear in ListLogGroups")

	aggLG := runCLI(t, awsCLI("logs", "list-aggregate-log-group-summaries",
		"--group-by", "DATA_SOURCE_NAME_AND_TYPE", "--output", "json"))
	var alg struct {
		AggregateLogGroupSummaries []any `json:"aggregateLogGroupSummaries"`
	}
	parseJSON(t, aggLG, &alg)
	assert.Empty(t, alg.AggregateLogGroupSummaries, "no aggregates without integrations")
}
