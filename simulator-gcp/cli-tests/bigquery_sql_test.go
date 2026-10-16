package gcp_cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bq inserts rows and a standard SQL query answers only the columns and rows
// it asks for, in the order it asks for them.
func TestBigQuery_CLI_QueryEvaluatesSQL(t *testing.T) {
	t.Parallel()
	const dataset = "cli_sql_ds"
	runCLI(t, bqCLI("mk", "--dataset", project+":"+dataset))
	runCLI(t, bqCLI("mk", "--table", project+":"+dataset+".scores", "player:STRING,points:INTEGER"))
	rows := filepath.Join(t.TempDir(), "rows.json")
	require.NoError(t, os.WriteFile(rows, []byte(
		`{"player":"ann","points":12}`+"\n"+`{"player":"ben","points":30}`+"\n"+`{"player":"cat","points":21}`+"\n"), 0o600))
	runCLI(t, bqCLI("insert", project+":"+dataset+".scores", rows))

	var got []map[string]string
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, bqCLI("query", "--nouse_legacy_sql", "--format=json",
		"SELECT player FROM "+dataset+".scores WHERE points > 15 ORDER BY points DESC"))), &got))
	assert.Equal(t, []map[string]string{{"player": "ben"}, {"player": "cat"}}, got)
}
