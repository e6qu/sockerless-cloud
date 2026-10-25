package azure_sdk_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/monitor/armmonitor"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Activity Logs - List, GET
// /subscriptions/{subscriptionId}/providers/Microsoft.Insights/eventtypes/management/values:
// every ARM write, delete and action leaves a BeginRequest and an EndRequest
// event, which the list returns newest first for the time span and the scope
// the filter names.
func TestActivityLog_RecordsResourceGroupOperations(t *testing.T) {
	t.Parallel()
	groups, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	const group = "activity-log-rg"
	start := time.Now().UTC().Add(-time.Minute)
	_, err = groups.CreateOrUpdate(ctx, group, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	_, err = groups.CreateOrUpdate(ctx, group, armresources.ResourceGroup{Location: to.Ptr("eastus"), Tags: map[string]*string{"team": to.Ptr("console")}}, nil)
	require.NoError(t, err)

	logs, err := armmonitor.NewActivityLogsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	filter := fmt.Sprintf("eventTimestamp ge '%s' and eventTimestamp le '%s' and resourceGroupName eq '%s'",
		start.Format(time.RFC3339), time.Now().UTC().Add(time.Minute).Format(time.RFC3339), group)
	pager := logs.NewListPager(filter, nil)
	var events []*armmonitor.EventData
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		events = append(events, page.Value...)
	}
	require.Len(t, events, 4, "two operations, each with a BeginRequest and an EndRequest")
	for i := 1; i < len(events); i++ {
		assert.False(t, events[i].EventTimestamp.After(*events[i-1].EventTimestamp), "newest first")
	}
	newest := events[0]
	assert.Equal(t, "Microsoft.Resources/subscriptions/resourceGroups/write", *newest.OperationName.Value)
	assert.Equal(t, "EndRequest", *newest.EventName.Value)
	assert.Equal(t, "Succeeded", *newest.Status.Value)
	assert.Equal(t, "OK", *newest.SubStatus.Value)
	assert.Equal(t, "Administrative", *newest.Category.Value)
	assert.Equal(t, group, *newest.ResourceGroupName)
	assert.Equal(t, fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", subscriptionID, group), *newest.ResourceID)
	assert.NotEmpty(t, *newest.Caller)
	begin := events[1]
	assert.Equal(t, "BeginRequest", *begin.EventName.Value)
	assert.Equal(t, "Started", *begin.Status.Value)
	assert.Equal(t, *newest.OperationID, *begin.OperationID, "a request's two events share its operation ID")

	selected := logs.NewListPager(filter, &armmonitor.ActivityLogsClientListOptions{Select: to.Ptr("operationName,status")})
	page, err := selected.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, page.Value)
	assert.NotNil(t, page.Value[0].OperationName)
	assert.Nil(t, page.Value[0].ResourceID, "$select projects the named fields")

	refused := logs.NewListPager(fmt.Sprintf("resourceGroupName eq '%s'", group), nil)
	_, err = refused.NextPage(ctx)
	require.Error(t, err, "the list requires an eventTimestamp range")
}
