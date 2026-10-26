package azure_sdk_test

import (
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/operationalinsights/armoperationalinsights/v2"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A query reads only rows within a table's interactive retention: a row older
// than the workspace's 30 days stays out of every query until the table's
// retention covers it.
func TestMonitor_QueryReadsWithinTheTableRetention(t *testing.T) {
	t.Parallel()
	p := newLogsPipeline(t, "monitor-retention-rg", "Retained_CL", "Job", "Log")
	now := time.Now().UTC()
	job := uniquename.New("retained")
	p.upload(t, []map[string]any{
		{"TimeGenerated": now.Add(-40 * 24 * time.Hour).Format(time.RFC3339), "Job": job, "Log": "forty days"},
		{"TimeGenerated": now.Add(-time.Minute).Format(time.RFC3339), "Job": job, "Log": "fresh"},
	})
	logs := func() []any {
		result := queryWorkspace(t, p.ws.customerID, `Retained_CL | where Job == "`+job+`" | project Log`)
		require.Len(t, result.Tables, 1)
		var out []any
		for _, row := range result.Tables[0].Rows {
			out = append(out, row[0])
		}
		return out
	}
	assert.ElementsMatch(t, []any{"fresh"}, logs())

	client, err := armoperationalinsights.NewTablesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginUpdate(ctx, p.ws.rg, p.ws.name, p.table, armoperationalinsights.Table{
		Properties: &armoperationalinsights.TableProperties{RetentionInDays: to.Ptr[int32](60)},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.ElementsMatch(t, []any{"forty days", "fresh"}, logs())
}
