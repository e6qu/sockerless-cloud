package gcp_cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type bqCLIJob struct {
	Configuration struct {
		Query struct {
			DestinationTable struct{ ProjectID, DatasetID, TableID string }
		}
	}
	Statistics struct {
		Query struct {
			StatementType      string
			NumDmlAffectedRows string
			DmlStats           map[string]string
		}
	}
}

func bqShowJob(t *testing.T, jobID string) bqCLIJob {
	t.Helper()
	var job bqCLIJob
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, bqCLI("show", "--format=json", "-j", jobID))), &job))
	return job
}

// bq query runs INSERT, UPDATE, DELETE and MERGE, and the job bq show reads
// back reports the rows each touched.
func TestBigQuery_CLI_DMLStatements(t *testing.T) {
	t.Parallel()
	const dataset = "cli_dml_ds"
	runCLI(t, bqCLI("mk", "--dataset", project+":"+dataset))
	runCLI(t, bqCLI("mk", "--table", project+":"+dataset+".stock", "item:STRING,qty:INTEGER"))

	run := func(jobID, sql string) bqCLIJob {
		t.Helper()
		runCLI(t, bqCLI("query", "--nouse_legacy_sql", "--job_id="+jobID, sql))
		return bqShowJob(t, jobID)
	}
	job := run("cli_dml_insert", "INSERT INTO "+dataset+".stock (item, qty) VALUES ('nut', 10), ('bolt', 5)")
	assert.Equal(t, "INSERT", job.Statistics.Query.StatementType)
	assert.Equal(t, "2", job.Statistics.Query.NumDmlAffectedRows)
	assert.Equal(t, "2", job.Statistics.Query.DmlStats["insertedRowCount"])

	job = run("cli_dml_update", "UPDATE "+dataset+".stock SET qty = qty + 1 WHERE item = 'bolt'")
	assert.Equal(t, "1", job.Statistics.Query.DmlStats["updatedRowCount"])

	job = run("cli_dml_merge", "MERGE "+dataset+".stock T USING (SELECT 'gear' AS item, 3 AS qty) S ON T.item = S.item WHEN NOT MATCHED THEN INSERT ROW")
	assert.Equal(t, "MERGE", job.Statistics.Query.StatementType)
	assert.Equal(t, "1", job.Statistics.Query.DmlStats["insertedRowCount"])

	job = run("cli_dml_delete", "DELETE FROM "+dataset+".stock WHERE qty > 8")
	assert.Equal(t, "1", job.Statistics.Query.DmlStats["deletedRowCount"])

	var rows []map[string]string
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, bqCLI("query", "--nouse_legacy_sql", "--format=json",
		"SELECT item, qty FROM "+dataset+".stock ORDER BY item"))), &rows))
	assert.Equal(t, []map[string]string{{"item": "bolt", "qty": "6"}, {"item": "gear", "qty": "3"}}, rows)
}

// A query without a destination table names the anonymous table it wrote,
// which bq show and bq head read back.
func TestBigQuery_CLI_AnonymousDestination(t *testing.T) {
	t.Parallel()
	const dataset = "cli_anon_ds"
	runCLI(t, bqCLI("mk", "--dataset", project+":"+dataset))
	runCLI(t, bqCLI("query", "--nouse_legacy_sql", "--job_id=cli_anon_job", "SELECT 'x' AS word UNION ALL SELECT 'y'"))
	dest := bqShowJob(t, "cli_anon_job").Configuration.Query.DestinationTable
	require.True(t, strings.HasPrefix(dest.DatasetID, "_"), "an anonymous dataset: %+v", dest)
	require.True(t, strings.HasPrefix(dest.TableID, "anon"), "an anonymous table: %+v", dest)

	ref := dest.ProjectID + ":" + dest.DatasetID + "." + dest.TableID
	var table struct {
		NumRows        string
		ExpirationTime string
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, bqCLI("show", "--format=json", ref))), &table))
	assert.Equal(t, "2", table.NumRows)
	assert.NotEmpty(t, table.ExpirationTime)
	var rows []map[string]string
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, bqCLI("head", "--format=json", ref))), &rows))
	assert.Len(t, rows, 2)
}

// bq query --dry_run validates a statement without running it.
func TestBigQuery_CLI_DryRun(t *testing.T) {
	t.Parallel()
	const dataset = "cli_dry_ds"
	runCLI(t, bqCLI("mk", "--dataset", project+":"+dataset))
	runCLI(t, bqCLI("mk", "--table", project+":"+dataset+".t", "n:INTEGER"))
	out := runCLI(t, bqCLI("query", "--nouse_legacy_sql", "--dry_run", "INSERT INTO "+dataset+".t (n) VALUES (1)"))
	assert.Contains(t, out, "successfully validated")
	var rows []map[string]string
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, bqCLI("query", "--nouse_legacy_sql", "--format=json", "SELECT n FROM "+dataset+".t"))), &rows))
	assert.Empty(t, rows, "the dry run inserted nothing")
}

// bq load --time_partitioning_field and --clustering_fields create the table
// with that layout, and bq cp --snapshot makes a table snapshot of it.
func TestBigQuery_CLI_PartitionedLoadAndSnapshot(t *testing.T) {
	t.Parallel()
	const dataset = "cli_part_ds"
	runCLI(t, bqCLI("mk", "--dataset", project+":"+dataset))
	source := filepath.Join(t.TempDir(), "events.csv")
	require.NoError(t, os.WriteFile(source, []byte("1,2024-01-02 03:04:05 UTC\n2,2024-01-03 00:00:00 UTC\n"), 0o600))
	runCLI(t, bqCLI("load", "--source_format=CSV", "--time_partitioning_field=at", "--time_partitioning_type=DAY",
		"--clustering_fields=id", dataset+".events", source, "id:INTEGER,at:TIMESTAMP"))

	var table struct {
		Type             string
		NumRows          string
		TimePartitioning struct{ Type, Field string }
		Clustering       struct{ Fields []string }
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, bqCLI("show", "--format=json", dataset+".events"))), &table))
	assert.Equal(t, "at", table.TimePartitioning.Field)
	assert.Equal(t, "DAY", table.TimePartitioning.Type)
	assert.Equal(t, []string{"id"}, table.Clustering.Fields)

	runCLI(t, bqCLI("cp", "--snapshot", "--no_clobber", dataset+".events", dataset+".events_snap"))
	var snap struct {
		Type               string
		NumRows            string
		SnapshotDefinition struct {
			BaseTableReference struct{ TableID string }
		}
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, bqCLI("show", "--format=json", dataset+".events_snap"))), &snap))
	assert.Equal(t, "SNAPSHOT", snap.Type)
	assert.Equal(t, "events", snap.SnapshotDefinition.BaseTableReference.TableID)
	assert.Equal(t, "2", snap.NumRows)
}
