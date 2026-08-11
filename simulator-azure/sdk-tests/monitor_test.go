package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ingestLogs sends log entries to the simulator's data collection endpoint.
func ingestLogs(t *testing.T, entries []map[string]any) {
	t.Helper()
	body, _ := json.Marshal(entries)
	req, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/dataCollectionRules/dcr-1/streams/Custom-Logs",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}

// queryWorkspace sends a KQL query and returns the parsed response.
func queryWorkspace(t *testing.T, workspaceID, kql string) queryResponse {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"query": kql})
	req, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/v1/workspaces/"+workspaceID+"/query",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result queryResponse
	data, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

type queryResponse struct {
	Tables []struct {
		Name    string `json:"name"`
		Columns []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"columns"`
		Rows [][]any `json:"rows"`
	} `json:"tables"`
}

func requireColumnIndex(t *testing.T, table struct {
	Name    string `json:"name"`
	Columns []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"columns"`
	Rows [][]any `json:"rows"`
}, name string) int {
	t.Helper()
	for i, col := range table.Columns {
		if col.Name == name {
			return i
		}
	}
	require.Failf(t, "missing query result column", "column %q not found in %v", name, table.Columns)
	return -1
}

func TestMonitor_KQLContainerAppLogs(t *testing.T) {
	now := time.Now().UTC()
	ts1 := now.Add(-2 * time.Minute).Format(time.RFC3339)
	ts2 := now.Add(-1 * time.Minute).Format(time.RFC3339)

	// Ingest ContainerAppConsoleLogs_CL entries
	ingestLogs(t, []map[string]any{
		{"TimeGenerated": ts1, "ContainerGroupName_s": "myjob", "Log_s": "starting", "Stream_s": "stdout"},
		{"TimeGenerated": ts2, "ContainerGroupName_s": "myjob", "Log_s": "running", "Stream_s": "stdout"},
		{"TimeGenerated": ts2, "ContainerGroupName_s": "otherjob", "Log_s": "other", "Stream_s": "stdout"},
	})

	// Query with the exact pattern the ACA backend sends
	kql := `ContainerAppConsoleLogs_CL | where ContainerGroupName_s == "myjob" | take 100`
	result := queryWorkspace(t, "default", kql)

	require.Len(t, result.Tables, 1)
	table := result.Tables[0]
	assert.Equal(t, "PrimaryResult", table.Name)

	// Verify columns match ContainerAppConsoleLogs_CL schema
	require.GreaterOrEqual(t, len(table.Columns), 4)
	assert.Equal(t, "TimeGenerated", table.Columns[0].Name)
	assert.Equal(t, "ContainerGroupName_s", table.Columns[1].Name)
	logColumn := requireColumnIndex(t, table, "Log_s")

	// Should only have the 2 "myjob" entries
	require.Len(t, table.Rows, 2)
	assert.Equal(t, "starting", table.Rows[0][logColumn])
	assert.Equal(t, "running", table.Rows[1][logColumn])
}

func TestMonitor_KQLWithDatetimeFilter(t *testing.T) {
	now := time.Now().UTC()
	tsOld := now.Add(-10 * time.Minute).Format(time.RFC3339)
	tsNew := now.Add(-1 * time.Minute).Format(time.RFC3339)
	tsMid := now.Add(-5 * time.Minute).Format(time.RFC3339)

	ingestLogs(t, []map[string]any{
		{"TimeGenerated": tsOld, "ContainerGroupName_s": "dtjob", "Log_s": "old entry"},
		{"TimeGenerated": tsNew, "ContainerGroupName_s": "dtjob", "Log_s": "new entry"},
	})

	// Query with datetime filter — should only return the new entry
	kql := `ContainerAppConsoleLogs_CL | where ContainerGroupName_s == "dtjob" | where TimeGenerated > datetime(` + tsMid + `)`
	result := queryWorkspace(t, "default", kql)

	require.Len(t, result.Tables, 1)
	table := result.Tables[0]
	require.Len(t, table.Rows, 1)
	assert.Equal(t, "new entry", table.Rows[0][requireColumnIndex(t, table, "Log_s")])
}

func TestMonitor_KQLAppTraces(t *testing.T) {
	// Ingest AppTraces entries
	ingestLogs(t, []map[string]any{
		{"TimeGenerated": time.Now().UTC().Format(time.RFC3339), "Message": "function started", "AppRoleName": "my-func"},
		{"TimeGenerated": time.Now().UTC().Format(time.RFC3339), "Message": "other trace", "AppRoleName": "other-func"},
	})

	// Query with the exact pattern the Azure Functions backend sends
	kql := `AppTraces | where AppRoleName == "my-func" | take 50`
	result := queryWorkspace(t, "default", kql)

	require.Len(t, result.Tables, 1)
	table := result.Tables[0]

	// Verify columns match AppTraces schema
	require.GreaterOrEqual(t, len(table.Columns), 3)
	assert.Equal(t, "TimeGenerated", table.Columns[0].Name)
	assert.Equal(t, "Message", table.Columns[1].Name)
	assert.Equal(t, "AppRoleName", table.Columns[2].Name)

	// Should only have the "my-func" entry
	require.Len(t, table.Rows, 1)
	assert.Equal(t, "function started", table.Rows[0][1])
	assert.Equal(t, "my-func", table.Rows[0][2])
}

func TestMonitor_KQLTakeLimit(t *testing.T) {
	// Ingest several entries
	ingestLogs(t, []map[string]any{
		{"TimeGenerated": time.Now().UTC().Format(time.RFC3339), "ContainerGroupName_s": "limitjob", "Log_s": "line1"},
		{"TimeGenerated": time.Now().UTC().Format(time.RFC3339), "ContainerGroupName_s": "limitjob", "Log_s": "line2"},
		{"TimeGenerated": time.Now().UTC().Format(time.RFC3339), "ContainerGroupName_s": "limitjob", "Log_s": "line3"},
	})

	kql := `ContainerAppConsoleLogs_CL | where ContainerGroupName_s == "limitjob" | take 2`
	result := queryWorkspace(t, "default", kql)

	require.Len(t, result.Tables, 1)
	assert.Len(t, result.Tables[0].Rows, 2)
}
