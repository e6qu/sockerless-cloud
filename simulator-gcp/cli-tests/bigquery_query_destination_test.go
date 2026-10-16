package gcp_cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bq query --destination_table writes the result to a table, --append_table
// adds to it and --replace overwrites it.
func TestBigQuery_CLI_QueryWritesItsDestinationTable(t *testing.T) {
	t.Parallel()
	const dataset = "cli_dest_ds"
	runCLI(t, bqCLI("mk", "--dataset", project+":"+dataset))
	count := func() int {
		t.Helper()
		var got []map[string]string
		require.NoError(t, json.Unmarshal([]byte(runCLI(t, bqCLI("query", "--nouse_legacy_sql", "--format=json",
			"SELECT word FROM "+dataset+".dst"))), &got))
		return len(got)
	}
	dest := "--destination_table=" + project + ":" + dataset + ".dst"
	runCLI(t, bqCLI("query", "--nouse_legacy_sql", dest, "SELECT 'x' AS word"))
	assert.Equal(t, 1, count())
	runCLI(t, bqCLI("query", "--nouse_legacy_sql", dest, "--append_table", "SELECT 'y' AS word"))
	assert.Equal(t, 2, count())
	runCLI(t, bqCLI("query", "--nouse_legacy_sql", dest, "--replace", "SELECT 'z' AS word"))
	assert.Equal(t, 1, count())
}
