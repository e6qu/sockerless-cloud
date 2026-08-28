package azure_sdk_test

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for the App Service instance and process family, which the
// simulator reads out of the site's live workload container:
//
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/instances (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/instances/{instanceId} (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/processes (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/processes/{processId} (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/processes/{processId}/threads (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/instances/{instanceId}/processes (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/instances/{instanceId}/processes/{processId} (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/instances/{instanceId}/processes/{processId}/threads (+ slot)

// TestSDK_WebApps_InstancesAndProcesses proves the instance and process family
// reports the site's real workload container, not a fixed answer:
//
//   - before the site's container starts, the site has no instances and no
//     processes — the negative control for everything that follows;
//   - after one invocation starts it, the instance identifier is the running
//     container, its statistics are the engine's own counters, and the process
//     list is the engine's process table for that container, with the command
//     line the container was started with.
func TestSDK_WebApps_InstancesAndProcesses(t *testing.T) {
	rg := "sdk-webproc-rg"
	site := "webproc-app"
	host := createSiteForContainers(t, rg, site)

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// The site's main container is the long-lived HTTP bootstrap App Service
	// keeps running on an always-on plan, so the instance stays up after the
	// invocation that starts it. The sidecar it answers over the shared
	// loopback keeps that first invocation fast.
	_, err = client.CreateOrUpdateSiteContainer(ctx, rg, site, "main", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:          to.Ptr(httpProbeImageName),
			IsMain:         to.Ptr(true),
			StartUpCommand: to.Ptr("probe-retry webproc-ok"),
		},
	}, nil)
	require.NoError(t, err)
	_, err = client.CreateOrUpdateSiteContainer(ctx, rg, site, "sidecar", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:          to.Ptr(httpProbeImageName),
			IsMain:         to.Ptr(false),
			TargetPort:     to.Ptr("9090"),
			StartUpCommand: to.Ptr("server"),
		},
	}, nil)
	require.NoError(t, err)

	// Negative control: a site whose container has never started has no
	// instances and no processes.
	require.Empty(t, listWebInstances(t, client, rg, site),
		"a site with no running container must report no instances")
	require.Empty(t, listWebProcesses(t, client, rg, site),
		"a site with no running container must report no processes")

	// Start the site container by invoking the app, exactly as a request to
	// the site does.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/function", strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "invoking the site must start its container: %s", body)

	instances := listWebInstances(t, client, rg, site)
	require.Len(t, instances, 1, "one running workload container is one site instance")
	instanceID := *instances[0].Name
	require.NotEmpty(t, instanceID)
	require.Equal(t, armappservice.SiteRuntimeStateREADY, *instances[0].Properties.State)
	require.Equal(t,
		"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+
			"/providers/Microsoft.Web/sites/"+site+"/instances/"+instanceID,
		*instances[0].ID)

	info, err := client.GetInstanceInfo(ctx, rg, site, instanceID, nil)
	require.NoError(t, err)
	require.NotNil(t, info.Properties)
	require.Equal(t, armappservice.SiteRuntimeStateREADY, *info.Properties.State)
	require.NotEmpty(t, info.Properties.Containers,
		"the instance must report the container engine's statistics for its container")
	var containerInfo *armappservice.ContainerInfo
	for _, c := range info.Properties.Containers {
		containerInfo = c
	}
	require.NotNil(t, containerInfo)
	require.Equal(t, instanceID, *containerInfo.ID)
	require.NotNil(t, containerInfo.MemoryStats)
	require.NotNil(t, containerInfo.MemoryStats.Limit)
	assert.Positive(t, *containerInfo.MemoryStats.Limit,
		"the memory limit is the engine's own counter, so it is a real number")
	require.NotNil(t, containerInfo.CurrentCPUStats)
	require.NotNil(t, containerInfo.CurrentCPUStats.CPUUsage)
	require.NotNil(t, containerInfo.CurrentCPUStats.CPUUsage.TotalUsage)
	assert.Positive(t, *containerInfo.CurrentCPUStats.CPUUsage.TotalUsage,
		"a running container has consumed CPU time")

	// An instance identifier that is not the running container is a 404.
	_, err = client.GetInstanceInfo(ctx, rg, site, "not-a-real-instance", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")

	procs := listWebProcesses(t, client, rg, site)
	require.NotEmpty(t, procs, "a running container has at least its main process")

	var main *armappservice.ProcessInfo
	for _, p := range procs {
		require.NotNil(t, p.Properties)
		require.NotNil(t, p.Properties.Identifier)
		if p.Properties.CommandLine != nil && strings.Contains(*p.Properties.CommandLine, "probe-retry") {
			main = p
		}
	}
	require.NotNil(t, main,
		"the process table must carry the command line the container was started with; got %v", procs)
	require.NotNil(t, main.Properties.UserName)
	assert.NotEmpty(t, *main.Properties.UserName, "every process has an owner")
	require.NotNil(t, main.Properties.ThreadCount)
	assert.Positive(t, *main.Properties.ThreadCount)
	require.NotNil(t, main.Properties.WorkingSet)
	assert.Positive(t, *main.Properties.WorkingSet, "resident memory is the engine's own counter")
	require.NotNil(t, main.Properties.VirtualMemory)
	assert.Positive(t, *main.Properties.VirtualMemory)
	require.NotNil(t, main.Properties.StartTime)
	require.NotNil(t, main.Properties.FileName)
	assert.NotEmpty(t, *main.Properties.FileName)

	pid := strconv.Itoa(int(*main.Properties.Identifier))
	got, err := client.GetProcess(ctx, rg, site, pid, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	assert.Equal(t, *main.Properties.Identifier, *got.Properties.Identifier)
	assert.Equal(t, *main.Properties.CommandLine, *got.Properties.CommandLine)

	// A process identifier the container's table does not carry is a 404.
	_, err = client.GetProcess(ctx, rg, site, "999999", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")

	threads := listWebProcessThreads(t, client, rg, site, pid)
	require.NotEmpty(t, threads, "a running process has at least one thread")
	for _, th := range threads {
		require.NotNil(t, th.Properties)
		require.NotNil(t, th.Properties.Identifier)
		assert.Positive(t, *th.Properties.Identifier)
		require.NotNil(t, th.Properties.State)
		assert.NotEmpty(t, *th.Properties.State, "the thread state is the one the engine reports")
		require.NotNil(t, th.Properties.Process)
		assert.Contains(t, *th.Properties.Process, "/processes/"+pid)
	}
	assert.LessOrEqual(t, len(threads), int(*main.Properties.ThreadCount)+1,
		"the thread rows must not outnumber the process table's own thread count")

	instProcs := listWebInstanceProcesses(t, client, rg, site, instanceID)
	require.NotEmpty(t, instProcs)
	assert.Contains(t, *instProcs[0].ID, "/instances/"+instanceID+"/processes/",
		"the per-instance spelling addresses its processes under the instance")

	instGot, err := client.GetInstanceProcess(ctx, rg, site, pid, instanceID, nil)
	require.NoError(t, err)
	require.NotNil(t, instGot.Properties)
	assert.Equal(t, *main.Properties.Identifier, *instGot.Properties.Identifier)

	instThreadsPager := client.NewListInstanceProcessThreadsPager(rg, site, pid, instanceID, nil)
	instThreads := drainProcessThreadPages(t, func() ([]*armappservice.ProcessThreadInfo, bool, error) {
		if !instThreadsPager.More() {
			return nil, false, nil
		}
		page, err := instThreadsPager.NextPage(ctx)
		return page.Value, true, err
	})
	require.NotEmpty(t, instThreads)

	// A deployment slot with no running container of its own has no instances
	// and no processes, and its per-instance reads are 404 — the same answers
	// the production site gave before its container started.
	slotPoller, err := client.BeginCreateOrUpdateSlot(ctx, rg, site, "staging", armappservice.Site{
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	slotInstancesPager := client.NewListInstanceIdentifiersSlotPager(rg, site, "staging", nil)
	require.True(t, slotInstancesPager.More())
	slotInstances, err := slotInstancesPager.NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, slotInstances.Value, "a slot with no running container has no instances")

	slotProcsPager := client.NewListProcessesSlotPager(rg, site, "staging", nil)
	require.True(t, slotProcsPager.More())
	slotProcs, err := slotProcsPager.NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, slotProcs.Value, "a slot with no running container has no processes")

	_, err = client.GetInstanceInfoSlot(ctx, rg, site, instanceID, "staging", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound",
		"the production site's instance is not an instance of the slot")

	_, err = client.GetProcessSlot(ctx, rg, site, pid, "staging", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")

	_, err = client.GetInstanceProcessSlot(ctx, rg, site, pid, "staging", instanceID, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")

	slotThreadsPager := client.NewListProcessThreadsSlotPager(rg, site, pid, "staging", nil)
	require.True(t, slotThreadsPager.More())
	_, err = slotThreadsPager.NextPage(ctx)
	require.Error(t, err, "a slot with no running container has no process to read threads from")

	slotInstProcsPager := client.NewListInstanceProcessesSlotPager(rg, site, "staging", instanceID, nil)
	require.True(t, slotInstProcsPager.More())
	_, err = slotInstProcsPager.NextPage(ctx)
	require.Error(t, err)

	slotInstThreadsPager := client.NewListInstanceProcessThreadsSlotPager(rg, site, pid, "staging", instanceID, nil)
	require.True(t, slotInstThreadsPager.More())
	_, err = slotInstThreadsPager.NextPage(ctx)
	require.Error(t, err)
}

func listWebInstances(t *testing.T, client *armappservice.WebAppsClient, rg, site string) []*armappservice.WebSiteInstanceStatus {
	t.Helper()
	pager := client.NewListInstanceIdentifiersPager(rg, site, nil)
	var out []*armappservice.WebSiteInstanceStatus
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		out = append(out, page.Value...)
	}
	return out
}

func listWebProcesses(t *testing.T, client *armappservice.WebAppsClient, rg, site string) []*armappservice.ProcessInfo {
	t.Helper()
	pager := client.NewListProcessesPager(rg, site, nil)
	var out []*armappservice.ProcessInfo
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		out = append(out, page.Value...)
	}
	return out
}

func listWebInstanceProcesses(t *testing.T, client *armappservice.WebAppsClient, rg, site, instanceID string) []*armappservice.ProcessInfo {
	t.Helper()
	pager := client.NewListInstanceProcessesPager(rg, site, instanceID, nil)
	var out []*armappservice.ProcessInfo
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		out = append(out, page.Value...)
	}
	return out
}

func listWebProcessThreads(t *testing.T, client *armappservice.WebAppsClient, rg, site, pid string) []*armappservice.ProcessThreadInfo {
	t.Helper()
	pager := client.NewListProcessThreadsPager(rg, site, pid, nil)
	var out []*armappservice.ProcessThreadInfo
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		out = append(out, page.Value...)
	}
	return out
}

// drainProcessThreadPages walks a thread pager expressed as a next-page
// closure, so the per-instance and per-site spellings share one drain.
func drainProcessThreadPages(t *testing.T, next func() ([]*armappservice.ProcessThreadInfo, bool, error)) []*armappservice.ProcessThreadInfo {
	t.Helper()
	var out []*armappservice.ProcessThreadInfo
	for {
		page, more, err := next()
		require.NoError(t, err)
		if !more {
			return out
		}
		out = append(out, page...)
	}
}
