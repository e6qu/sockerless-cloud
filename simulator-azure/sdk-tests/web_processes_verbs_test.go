package azure_sdk_test

import (
	"bytes"
	"debug/elf"
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

// A site's processes are read from its container, so killing one removes it
// from the table, and the modules reported are the ones the process loaded.
func TestSDK_WebApps_ProcessModulesAndKill(t *testing.T) {
	t.Parallel()
	rg, name := "sdk-proc-verbs-rg", "sdk-proc-verbs-app"
	host := createSiteForContainers(t, rg, name)

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = client.CreateOrUpdateSiteContainer(ctx, rg, name, "main", armappservice.SiteContainer{
		Properties: &armappservice.SiteContainerProperties{
			Image:          to.Ptr(httpProbeImageName),
			IsMain:         to.Ptr(true),
			TargetPort:     to.Ptr("8080"),
			StartUpCommand: to.Ptr("echo-request"),
		},
	}, nil)
	require.NoError(t, err)
	// A request to the site starts its container.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/function", strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "invoking the site must start its container: %s", body)

	procs := listWebProcesses(t, client, rg, name)
	require.NotEmpty(t, procs, "a running site has processes")
	pid := *procs[0].Properties.Identifier

	// The modules come from the process's own mapping table: an absolute path to
	// the mapped file, and a real base address. A host that does not share the
	// container engine's kernel has no such table and says so; a client is never
	// handed an invented module.
	modules := client.NewListProcessModulesPager(rg, name, itoa(int(pid)), nil)
	modulePage, modulesErr := modules.NextPage(ctx)
	if modulesErr != nil {
		require.Contains(t, modulesErr.Error(), "does not share a kernel with the container engine",
			"a host that cannot read the process's mapping table must say that; got: %v", modulesErr)
	} else {
		require.NotEmpty(t, modulePage.Value, "a running process has mapped at least its own image")
		for _, module := range modulePage.Value {
			path := *module.Properties.FilePath
			base := *module.Properties.BaseAddress
			assert.True(t, strings.HasPrefix(path, "/"),
				"a module's file path is the file the process mapped: %q", path)
			assert.True(t, strings.HasPrefix(base, "0x"),
				"a module's base address is an address: %q", base)
			address, parseErr := strconv.ParseUint(strings.TrimPrefix(base, "0x"), 16, 64)
			require.NoError(t, parseErr, "base address %q is not hexadecimal", base)
			assert.NotEqual(t, uint64(pid), address,
				"the base address is the module's, not the process's identifier")
		}

		base := *modulePage.Value[0].Properties.BaseAddress
		one, err := client.GetProcessModule(ctx, rg, name, itoa(int(pid)), base, nil)
		require.NoError(t, err)
		assert.Equal(t, base, *one.Properties.BaseAddress)

		// A module nothing is mapped at is not found.
		_, err = client.GetProcessModule(ctx, rg, name, itoa(int(pid)), "0xdeadbeef", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "No module is mapped")
	}

	// A process dump is an ELF core a debugger opens, with segments carrying the
	// process's mappings; a host that does not let the simulator read another
	// process's memory says so.
	dump, dumpErr := client.GetProcessDump(ctx, rg, name, itoa(int(pid)), nil)
	if dumpErr != nil {
		require.Contains(t, dumpErr.Error(), "read from its own", // the message names /proc, whose angle brackets JSON escapes
			"a host that cannot read the process's memory must say that; got: %v", dumpErr)
	} else {
		image, readErr := io.ReadAll(dump.Body)
		dump.Body.Close()
		require.NoError(t, readErr)
		core, elfErr := elf.NewFile(bytes.NewReader(image))
		require.NoError(t, elfErr, "the dump is not an ELF image a debugger opens")
		assert.Equal(t, elf.ET_CORE, core.Type, "a process dump is a core image")
		loads := 0
		for _, segment := range core.Progs {
			if segment.Type == elf.PT_LOAD {
				loads++
			}
		}
		assert.NotZero(t, loads, "a core with no loadable segment images nothing")
	}

	// A process the site does not have cannot be killed.
	_, err = client.DeleteProcess(ctx, rg, name, "999999", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "999999")
}
