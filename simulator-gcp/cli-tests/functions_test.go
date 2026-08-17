package gcp_cli_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cloud Functions tests use direct HTTP since gcloud functions deploy
// tries to upload source code which won't work with the simulator.

func functionsURL(name string) string {
	return fmt.Sprintf("%s/v2/projects/%s/locations/%s/functions/%s",
		baseURL, project, location, name)
}

func functionsBaseURL() string {
	return fmt.Sprintf("%s/v2/projects/%s/locations/%s/functions",
		baseURL, project, location)
}

func TestFunctions_CreateAndGet(t *testing.T) {
	url := functionsBaseURL() + "?functionId=cli-test-func"
	out := httpDoJSON(t, "POST", url, `{
		"description": "CLI test function",
		"buildConfig": {
			"runtime": "nodejs18",
			"entryPoint": "helloWorld",
			"source": {}
		},
		"serviceConfig": {
			"availableMemory": "256M",
			"timeoutSeconds": 60
		}
	}`)

	// Create returns an LRO whose settled response carries the function it
	// created, under the resource name the caller asked for.
	var op struct {
		Done     bool `json:"done"`
		Response struct {
			Name string `json:"name"`
		} `json:"response"`
	}
	parseJSON(t, out, &op)
	assert.True(t, op.Done, "the create operation is settled when it is returned: %s", out)
	assert.Equal(t,
		fmt.Sprintf("projects/%s/locations/%s/functions/cli-test-func", project, location),
		op.Response.Name, "the operation's response is the function it created")

	// GET the function
	getURL := functionsURL("cli-test-func")
	out = httpDoJSON(t, "GET", getURL, "")

	var fn struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		State       string `json:"state"`
		BuildConfig struct {
			Runtime    string `json:"runtime"`
			EntryPoint string `json:"entryPoint"`
		} `json:"buildConfig"`
	}
	parseJSON(t, out, &fn)
	assert.Contains(t, fn.Name, "cli-test-func")
	assert.Equal(t, "CLI test function", fn.Description)
	assert.Equal(t, "ACTIVE", fn.State)
	assert.Equal(t, "nodejs18", fn.BuildConfig.Runtime)

	// Cleanup
	httpDoJSON(t, "DELETE", getURL, "")
}

func TestFunctions_List(t *testing.T) {
	// Create a function
	url := functionsBaseURL() + "?functionId=list-test-func"
	httpDoJSON(t, "POST", url, `{
		"buildConfig": {"runtime": "python312", "entryPoint": "main"},
		"serviceConfig": {}
	}`)

	// List functions
	out := httpDoJSON(t, "GET", functionsBaseURL(), "")

	var result struct {
		Functions []struct {
			Name string `json:"name"`
		} `json:"functions"`
	}
	parseJSON(t, out, &result)

	// The list has to hold the function this test created, by its full
	// resource name — the presence of some other function proves nothing.
	names := make([]string, 0, len(result.Functions))
	for _, f := range result.Functions {
		names = append(names, f.Name)
	}
	assert.Contains(t, names,
		fmt.Sprintf("projects/%s/locations/%s/functions/list-test-func", project, location),
		"the list must hold the function that was just created")

	// Cleanup
	httpDoJSON(t, "DELETE", functionsURL("list-test-func"), "")
}

func TestFunctions_CLI_InvokeAndCheckLogs(t *testing.T) {
	// Create a function
	url := functionsBaseURL() + "?functionId=cli-invoke-fn"
	httpDoJSON(t, "POST", url, `{
		"buildConfig": {"runtime": "go121", "entryPoint": "Handler"},
		"serviceConfig": {}
	}`)

	// Invoke the function
	httpDoJSON(t, "POST", baseURL+"/v2-functions-invoke/cli-invoke-fn", "{}")

	// Query Cloud Logging for the function's log entries. Ingestion is
	// asynchronous, so the read is polled rather than run once, and the
	// assertion is on an entry whose textPayload is the invocation line.
	var out string
	var payloads []string
	require.Eventually(t, func() bool {
		out = runCLI(t, gcloudCLI("logging", "read",
			`resource.type="cloud_run_revision" AND resource.labels.service_name="cli-invoke-fn"`,
			"--format", "json",
		))
		payloads = logTextPayloads(out)
		return slices.Contains(payloads, "Function invoked")
	}, 60*time.Second, 250*time.Millisecond,
		"the invocation never produced a Cloud Logging entry")
	assert.Contains(t, payloads, "Function invoked",
		"expected an invocation log entry: %s", out)

	// Cleanup
	httpDoJSON(t, "DELETE", functionsURL("cli-invoke-fn"), "")
}

func TestFunctions_Delete(t *testing.T) {
	url := functionsBaseURL() + "?functionId=delete-test-func"
	httpDoJSON(t, "POST", url, `{
		"buildConfig": {"runtime": "go121", "entryPoint": "Handler"},
		"serviceConfig": {}
	}`)

	// Delete
	httpDoJSON(t, "DELETE", functionsURL("delete-test-func"), "")

	// Verify gone
	resp, err := httpDo("GET", functionsURL("delete-test-func"), "")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode)
}
