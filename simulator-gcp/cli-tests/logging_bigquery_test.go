package gcp_cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A sink gcloud creates with --use-partitioned-tables streams the entries
// gcloud writes into one table per log, partitioned on its timestamp column,
// which bq reads back.
func TestLogging_CLI_SinkToBigQueryPartitionedTable(t *testing.T) {
	t.Parallel()
	const dataset = "cli_log_export"
	runCLI(t, bqCLI("mk", "--dataset", project+":"+dataset))
	runCLI(t, gcloudCLI("logging", "sinks", "create", "cli-bq-export",
		"bigquery.googleapis.com/projects/"+project+"/datasets/"+dataset,
		"--log-filter=logName:\"logs/cli-bq-app\"", "--use-partitioned-tables", "--quiet"))
	t.Cleanup(func() { runCLI(t, gcloudCLI("logging", "sinks", "delete", "cli-bq-export", "--quiet")) })

	var sink struct {
		BigqueryOptions struct{ UsePartitionedTables, UsesTimestampColumnPartitioning bool }
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, gcloudCLI("logging", "sinks", "describe", "cli-bq-export", "--format=json"))), &sink))
	assert.True(t, sink.BigqueryOptions.UsePartitionedTables)
	assert.True(t, sink.BigqueryOptions.UsesTimestampColumnPartitioning)

	runCLI(t, gcloudCLI("logging", "write", "cli-bq-app", "exported through the sink", "--severity=ERROR"))

	var table struct {
		TimePartitioning struct{ Type, Field string }
		NumRows          string
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, bqCLI("show", "--format=json", dataset+".cli_bq_app"))), &table))
	assert.Equal(t, "timestamp", table.TimePartitioning.Field)
	assert.Equal(t, "1", table.NumRows)

	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, bqCLI("head", "--format=json", dataset+".cli_bq_app"))), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "ERROR", rows[0]["severity"])
	assert.Equal(t, "exported through the sink", rows[0]["textPayload"])
}
