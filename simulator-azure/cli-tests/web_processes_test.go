package azure_cli_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for the App Service instance and process family, which the
// simulator reads out of the site's live workload container:
//
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/instances (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/instances/{instanceId} (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/processes (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/processes/{processId} (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/processes/{processId}/threads (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/instances/{instanceId}/processes (+ slot)
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/instances/{instanceId}/processes/{processId} (+ slot)

// webProcessCollection is the ARM collection shape these operations answer
// with.
type webProcessCollection struct {
	Value []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Type       string `json:"type"`
		Properties struct {
			Identifier    int    `json:"identifier"`
			CommandLine   string `json:"command_line"`
			UserName      string `json:"user_name"`
			FileName      string `json:"file_name"`
			ThreadCount   int    `json:"thread_count"`
			WorkingSet    int64  `json:"working_set"`
			VirtualMemory int64  `json:"virtual_memory"`
			StartTime     string `json:"start_time"`
			State         string `json:"state"`
			Process       string `json:"process"`
		} `json:"properties"`
	} `json:"value"`
}

// TestWebApps_CLI_InstancesAndProcesses drives the family with the real Azure
// CLI. The negative control comes first: before the site's container starts,
// the CLI reads an empty instance list and an empty process list.
func TestWebApps_CLI_InstancesAndProcesses(t *testing.T) {
	const apiVersion = "2025-03-01"
	url := func(path string) string { return armURL("Microsoft.Web", path, apiVersion) }
	const site = "cli-webproc-app"

	siteURL := url("sites/" + site)
	runCLI(t, azRest("PUT", siteURL, `{
		"location": "eastus",
		"kind": "functionapp,linux,container",
		"properties": {"siteConfig": {}}
	}`))
	defer runCLI(t, azRest("DELETE", siteURL, ""))

	runCLI(t, azRest("PUT", url("sites/"+site+"/sitecontainers/main"), fmt.Sprintf(`{
		"properties": {"image": %q, "isMain": true, "startUpCommand": "probe-retry cli-webproc-ok"}
	}`, httpProbeImageName)))
	runCLI(t, azRest("PUT", url("sites/"+site+"/sitecontainers/sidecar"), fmt.Sprintf(`{
		"properties": {"image": %q, "isMain": false, "targetPort": "9090", "startUpCommand": "server"}
	}`, httpProbeImageName)))

	// Negative control — nothing is running yet.
	var instances webProcessCollection
	parseJSON(t, runCLI(t, azRest("GET", url("sites/"+site+"/instances"), "")), &instances)
	require.Empty(t, instances.Value, "a site with no running container has no instances")
	var processes webProcessCollection
	parseJSON(t, runCLI(t, azRest("GET", url("sites/"+site+"/processes"), "")), &processes)
	require.Empty(t, processes.Value, "a site with no running container has no processes")

	// Start the container by invoking the site, the way a request to it does.
	runCLI(t, azRest("POST", baseURL+"/api/function", "{}",
		"--headers", "Host="+site+".azurewebsites.net"))

	parseJSON(t, runCLI(t, azRest("GET", url("sites/"+site+"/instances"), "")), &instances)
	require.Len(t, instances.Value, 1, "the running workload container is the site's one instance")
	instanceID := instances.Value[0].Name
	require.NotEmpty(t, instanceID)
	assert.Equal(t, "Microsoft.Web/sites/instances", instances.Value[0].Type)

	// GET .../instances/{instanceId} carries the engine's own statistics.
	var info struct {
		Name       string `json:"name"`
		Properties struct {
			State      string `json:"state"`
			Containers map[string]struct {
				ID          string `json:"id"`
				MemoryStats struct {
					Limit int64 `json:"limit"`
				} `json:"memoryStats"`
				CurrentCPUStats struct {
					CPUUsage struct {
						TotalUsage int64 `json:"totalUsage"`
					} `json:"cpuUsage"`
				} `json:"currentCpuStats"`
			} `json:"containers"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("GET", url("sites/"+site+"/instances/"+instanceID), "")), &info)
	assert.Equal(t, "READY", info.Properties.State)
	require.Len(t, info.Properties.Containers, 1)
	for _, c := range info.Properties.Containers {
		assert.Equal(t, instanceID, c.ID)
		assert.Positive(t, c.MemoryStats.Limit, "the memory limit is the engine's own counter")
		assert.Positive(t, c.CurrentCPUStats.CPUUsage.TotalUsage, "a running container has consumed CPU time")
	}

	// GET .../processes reads the engine's process table.
	parseJSON(t, runCLI(t, azRest("GET", url("sites/"+site+"/processes"), "")), &processes)
	require.NotEmpty(t, processes.Value)
	pid := 0
	for _, p := range processes.Value {
		if strings.Contains(p.Properties.CommandLine, "probe-retry") {
			pid = p.Properties.Identifier
			assert.NotEmpty(t, p.Properties.UserName)
			assert.NotEmpty(t, p.Properties.FileName)
			assert.Positive(t, p.Properties.ThreadCount)
			assert.Positive(t, p.Properties.WorkingSet)
			assert.Positive(t, p.Properties.VirtualMemory)
			assert.NotEmpty(t, p.Properties.StartTime)
		}
	}
	require.NotZero(t, pid, "the process table must carry the container's start-up command: %v", processes.Value)
	pidStr := strconv.Itoa(pid)

	var single struct {
		Name       string `json:"name"`
		Properties struct {
			Identifier  int    `json:"identifier"`
			CommandLine string `json:"command_line"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("GET", url("sites/"+site+"/processes/"+pidStr), "")), &single)
	assert.Equal(t, pid, single.Properties.Identifier)
	assert.Contains(t, single.Properties.CommandLine, "probe-retry")

	// GET .../processes/{processId}/threads reads the engine's thread rows.
	var threads webProcessCollection
	parseJSON(t, runCLI(t, azRest("GET", url("sites/"+site+"/processes/"+pidStr+"/threads"), "")), &threads)
	require.NotEmpty(t, threads.Value)
	for _, th := range threads.Value {
		assert.NotEmpty(t, th.Properties.State, "the thread state is the one the engine reports")
		assert.Contains(t, th.Properties.Process, "/processes/"+pidStr)
	}

	// The per-instance spellings address the same processes under the instance.
	var instProcesses webProcessCollection
	parseJSON(t, runCLI(t, azRest("GET", url("sites/"+site+"/instances/"+instanceID+"/processes"), "")), &instProcesses)
	require.NotEmpty(t, instProcesses.Value)
	assert.Contains(t, instProcesses.Value[0].ID, "/instances/"+instanceID+"/processes/")

	parseJSON(t, runCLI(t, azRest("GET",
		url("sites/"+site+"/instances/"+instanceID+"/processes/"+pidStr), "")), &single)
	assert.Equal(t, pid, single.Properties.Identifier)

	var instThreads webProcessCollection
	parseJSON(t, runCLI(t, azRest("GET",
		url("sites/"+site+"/instances/"+instanceID+"/processes/"+pidStr+"/threads"), "")), &instThreads)
	require.NotEmpty(t, instThreads.Value)

	// An instance identifier that is not the running container, and a process
	// identifier the table does not carry, are both 404.
	_, err := azRest("GET", url("sites/"+site+"/instances/not-a-real-instance"), "").CombinedOutput()
	assert.Error(t, err)
	_, err = azRest("GET", url("sites/"+site+"/processes/999999"), "").CombinedOutput()
	assert.Error(t, err)

	// The slot spellings answer for the slot's own container, which does not
	// exist: empty collections, and 404 for a named instance or process.
	slotURL := url("sites/" + site + "/slots/staging")
	runCLI(t, azRest("PUT", slotURL, `{"location": "eastus", "properties": {}}`))
	var slotInstances webProcessCollection
	parseJSON(t, runCLI(t, azRest("GET", url("sites/"+site+"/slots/staging/instances"), "")), &slotInstances)
	assert.Empty(t, slotInstances.Value)
	var slotProcesses webProcessCollection
	parseJSON(t, runCLI(t, azRest("GET", url("sites/"+site+"/slots/staging/processes"), "")), &slotProcesses)
	assert.Empty(t, slotProcesses.Value)
	_, err = azRest("GET", url("sites/"+site+"/slots/staging/instances/"+instanceID), "").CombinedOutput()
	assert.Error(t, err)
	_, err = azRest("GET", url("sites/"+site+"/slots/staging/processes/"+pidStr), "").CombinedOutput()
	assert.Error(t, err)
	_, err = azRest("GET", url("sites/"+site+"/slots/staging/processes/"+pidStr+"/threads"), "").CombinedOutput()
	assert.Error(t, err)
	_, err = azRest("GET",
		url("sites/"+site+"/slots/staging/instances/"+instanceID+"/processes"), "").CombinedOutput()
	assert.Error(t, err)
	_, err = azRest("GET",
		url("sites/"+site+"/slots/staging/instances/"+instanceID+"/processes/"+pidStr), "").CombinedOutput()
	assert.Error(t, err)
	_, err = azRest("GET",
		url("sites/"+site+"/slots/staging/instances/"+instanceID+"/processes/"+pidStr+"/threads"), "").CombinedOutput()
	assert.Error(t, err)
}
