package azure_sdk_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventgrid/armeventgrid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for the provider-level reads that were the last gaps in three
// documents:
//
//	GET /providers/Microsoft.Compute/operations
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/usages
//	GET /{scope}/providers/Microsoft.EventGrid/extensionTopics/default
//	POST /subscriptions/{subscriptionId}/resourcegroups/{resourceGroupName}/providers/Microsoft.OperationalInsights/workspaces/{workspaceName}/regenerateSharedKey

// The Microsoft.Compute action catalog is the provider's own surface, and its
// per-location usage is counted from what the subscription holds there.
func TestSDK_Compute_OperationsAndUsage(t *testing.T) {
	operations, err := armcompute.NewOperationsClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	catalog, err := operations.NewListPager(nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, catalog.Value)

	actions := map[string]bool{}
	for _, op := range catalog.Value {
		require.NotNil(t, op.Name)
		actions[*op.Name] = true
		require.NotNil(t, op.Display, "%s names what it authorizes", *op.Name)
		assert.Equal(t, "Microsoft Compute", *op.Display.Provider)
	}
	// Every action the catalog carries belongs to this provider, and the ones a
	// role assignment on a virtual machine needs are there.
	for action := range actions {
		assert.True(t, strings.HasPrefix(action, "Microsoft.Compute/"),
			"%s is an action of another provider", action)
	}
	for _, needed := range []string{
		"Microsoft.Compute/virtualMachines/read",
		"Microsoft.Compute/virtualMachines/write",
		"Microsoft.Compute/virtualMachines/start/action",
		"Microsoft.Compute/locations/usages/read",
	} {
		assert.Contains(t, actions, needed)
	}

	// A location the subscription holds nothing in reports nothing held, which
	// needs no machine and so runs on any host.
	usageOnBareHost, err := armcompute.NewUsageClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	empty, err := usageOnBareHost.NewListPager("australiacentral2", nil).NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, empty.Value, "a location still reports the quotas it has")
	for _, usage := range empty.Value {
		assert.Zero(t, *usage.CurrentValue,
			"%s must be zero in a location holding nothing", *usage.Name.Value)
		assert.Positive(t, *usage.Limit, "a usage figure is only meaningful against a quota")
	}
}

// The usage is counted from what the subscription is holding, so creating a
// machine moves it. A machine needs a real network interface, so this half
// needs the Linux network capabilities the simulator's fabric is built on.
func TestSDK_Compute_UsageCountsWhatIsHeld(t *testing.T) {
	requireNetworkHost(t)

	const rg, location = "compute-usage-rg", "eastus"
	ensureRG(t, rg)
	nicID := vmOperationsFixture(t, rg, "usage-vm", "10.94")

	usageClient, err := armcompute.NewUsageClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	usageFor := func(name string) (current int32, limit int64) {
		t.Helper()
		page, err := usageClient.NewListPager(location, nil).NextPage(ctx)
		require.NoError(t, err)
		for _, usage := range page.Value {
			if usage.Name != nil && usage.Name.Value != nil && *usage.Name.Value == name {
				return *usage.CurrentValue, *usage.Limit
			}
		}
		t.Fatalf("the usage list carried no %s entry", name)
		return 0, 0
	}

	beforeMachines, _ := usageFor("virtualMachines")
	beforeCores, _ := usageFor("cores")

	// Creating a machine moves the figures, which is what makes them a count of
	// what the subscription holds rather than a fixed answer.
	createOperationsVM(t, rg, "usage-vm", nicID, func(vm *armcompute.VirtualMachine) {
		vm.Properties.HardwareProfile = &armcompute.HardwareProfile{
			VMSize: to.Ptr(armcompute.VirtualMachineSizeTypes("Standard_B2s")),
		}
	})

	afterMachines, _ := usageFor("virtualMachines")
	afterCores, _ := usageFor("cores")
	assert.Equal(t, beforeMachines+1, afterMachines, "the new machine is counted")
	assert.Equal(t, beforeCores+2, afterCores, "its size decides the cores it takes")
	_ = location
}

// An extension topic is derived from the scope it is asked about, and names the
// system topic whose source is that resource.
func TestSDK_EventGrid_ExtensionTopic(t *testing.T) {
	const rg = "eventgrid-extension-rg"
	ensureRG(t, rg)

	client, err := armeventgrid.NewExtensionTopicsClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)

	scope := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.Storage/storageAccounts/extensionsource"

	// A resource with no system topic still has an extension topic — the events
	// exist either way — and it names no system topic, because there is none.
	bare, err := client.Get(ctx, scope, nil)
	require.NoError(t, err)
	require.NotNil(t, bare.Properties)
	assert.Empty(t, *bare.Properties.SystemTopic)
	assert.Contains(t, *bare.Properties.Description, scope)
	assert.Equal(t, "default", *bare.Name)

	// Create the system topic that carries that resource's events; the
	// extension topic now points at it.
	topics, err := armeventgrid.NewSystemTopicsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := topics.BeginCreateOrUpdate(ctx, rg, "extension-system-topic", armeventgrid.SystemTopic{
		Location: to.Ptr("eastus"),
		Properties: &armeventgrid.SystemTopicProperties{
			Source:    to.Ptr(scope),
			TopicType: to.Ptr("Microsoft.Storage.StorageAccounts"),
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	mapped, err := client.Get(ctx, scope, nil)
	require.NoError(t, err)
	assert.Equal(t, *created.ID, *mapped.Properties.SystemTopic,
		"the extension topic names the system topic carrying the resource's events")

	// Another resource's extension topic is not that one.
	other, err := client.Get(ctx, scope+"other", nil)
	require.NoError(t, err)
	assert.Empty(t, *other.Properties.SystemTopic)
}

// A workspace's shared keys are its own pair, and regenerating them replaces
// the pair rather than reporting that it did.
func TestSDK_OperationalInsights_RegenerateSharedKey(t *testing.T) {
	const rg, workspace = "loganalytics-keys-rg", "keys-workspace"
	ensureRG(t, rg)

	base := baseURL + "/subscriptions/" + subscriptionID + "/resourcegroups/" + rg +
		"/providers/Microsoft.OperationalInsights/workspaces/"

	// The workspace whose keys these are. It is created through its own ARM
	// resource, which is what makes the 404 below a real negative control.
	putARMJSON(t, "/subscriptions/"+subscriptionID+"/resourcegroups/"+rg+
		"/providers/Microsoft.OperationalInsights/workspaces/"+workspace+"?api-version=2020-08-01",
		`{"location":"eastus","properties":{"sku":{"name":"PerGB2018"}}}`)

	read := func(path string) map[string]string {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, path+"?api-version=2020-08-01", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", simARMBearer)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		keys := map[string]string{}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&keys))
		return keys
	}

	first := read(base + workspace + "/sharedKeys")
	require.NotEmpty(t, first["primarySharedKey"])
	require.NotEmpty(t, first["secondarySharedKey"])
	assert.NotEqual(t, first["primarySharedKey"], first["secondarySharedKey"],
		"the two keys of a pair are two keys")

	// Reading again returns the same pair: the keys belong to the workspace.
	again := read(base + workspace + "/sharedKeys")
	assert.Equal(t, first, again)

	regenerated := read(base + workspace + "/regenerateSharedKey")
	assert.NotEqual(t, first["primarySharedKey"], regenerated["primarySharedKey"],
		"a regeneration must replace the key")

	// And the replacement is what the read serves afterwards.
	after := read(base + workspace + "/sharedKeys")
	assert.Equal(t, regenerated, after)

	// A workspace the subscription does not hold has no keys to regenerate.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"absent-workspace/regenerateSharedKey?api-version=2020-08-01", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", simARMBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
