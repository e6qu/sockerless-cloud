package azure_sdk_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ResourceGroups_List honours $top and continues at the nextLink it returns;
// the SDK pager follows it to the end of the list.
func TestResourceGroups_ListPagesByTop(t *testing.T) {
	t.Parallel()
	client, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	const prefix = "rg-paging-"
	for i := range 5 {
		_, err := client.CreateOrUpdate(ctx, fmt.Sprintf("%s%d", prefix, i), armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
		require.NoError(t, err)
	}

	pager := client.NewListPager(&armresources.ResourceGroupsClientListOptions{Top: to.Ptr[int32](2)})
	seen := map[string]int{}
	pages := 0
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		pages++
		assert.LessOrEqual(t, len(page.Value), 2, "a page holds at most $top groups")
		for _, group := range page.Value {
			if strings.HasPrefix(*group.Name, prefix) {
				seen[*group.Name]++
			}
		}
	}
	assert.GreaterOrEqual(t, pages, 3)
	for i := range 5 {
		assert.Equal(t, 1, seen[fmt.Sprintf("%s%d", prefix, i)], "every group appears exactly once across the pages")
	}
}
