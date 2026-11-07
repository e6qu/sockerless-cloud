package azure_sdk_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/operationalinsights/armoperationalinsights/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A deleted workspace keeps its data and name for 14 days: it leaves the
// workspace reads, the deleted-workspace list shows it, and re-creating it in
// its region brings it back with its customer ID and rows. force=true deletes
// it for good.
func TestMonitor_WorkspaceSoftDeleteAndRecovery(t *testing.T) {
	t.Parallel()
	p := newLogsPipeline(t, "monitor-softdelete-rg", "Recover_CL", "Msg")
	p.upload(t, []map[string]any{{"TimeGenerated": time.Now().UTC().Format(time.RFC3339), "Msg": "kept"}})
	require.Len(t, queryWorkspace(t, p.ws.customerID, `Recover_CL | project Msg`).Tables[0].Rows, 1)

	client, err := armoperationalinsights.NewWorkspacesClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	deleted, err := armoperationalinsights.NewDeletedWorkspacesClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	deletedNames := func() []string {
		var names []string
		pager := deleted.NewListByResourceGroupPager(p.ws.rg, nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			for _, ws := range page.Value {
				names = append(names, *ws.Name)
			}
		}
		return names
	}
	remove := func(force bool) {
		poller, err := client.BeginDelete(ctx, p.ws.rg, p.ws.name, &armoperationalinsights.WorkspacesClientBeginDeleteOptions{Force: to.Ptr(force)})
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	}
	create := func(location string) (armoperationalinsights.Workspace, error) {
		poller, err := client.BeginCreateOrUpdate(ctx, p.ws.rg, p.ws.name, armoperationalinsights.Workspace{Location: to.Ptr(location)}, nil)
		if err != nil {
			return armoperationalinsights.Workspace{}, err
		}
		resp, err := poller.PollUntilDone(ctx, nil)
		return resp.Workspace, err
	}

	remove(false)
	_, err = client.Get(ctx, p.ws.rg, p.ws.name, nil)
	var missing *azcore.ResponseError
	require.True(t, errors.As(err, &missing), "a soft-deleted workspace leaves the reads, got %v", err)
	assert.Equal(t, http.StatusNotFound, missing.StatusCode)
	assert.Contains(t, deletedNames(), p.ws.name)

	_, err = create("westus")
	var held *azcore.ResponseError
	require.True(t, errors.As(err, &held), "the name stays held from another region, got %v", err)
	assert.Equal(t, http.StatusConflict, held.StatusCode)

	recovered, err := create("eastus")
	require.NoError(t, err)
	assert.Equal(t, p.ws.customerID, *recovered.Properties.CustomerID, "recovery keeps the workspace's customer ID")
	assert.NotContains(t, deletedNames(), p.ws.name)
	rows := queryWorkspace(t, p.ws.customerID, `Recover_CL | project Msg`).Tables[0].Rows
	require.Len(t, rows, 1, "recovery brings the data back")
	assert.Equal(t, "kept", rows[0][0])

	remove(true)
	assert.NotContains(t, deletedNames(), p.ws.name, "a forced delete keeps nothing to recover")
	fresh, err := create("westus")
	require.NoError(t, err, "a forced delete releases the name")
	assert.NotEqual(t, p.ws.customerID, *fresh.Properties.CustomerID)
}
