package gcp_cli_test

import (
	"fmt"
	"testing"

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

	// Create returns an LRO operation
	var op struct {
		Done     bool `json:"done"`
		Response struct {
			Name string `json:"name"`
		} `json:"response"`
	}
	parseJSON(t, out, &op)

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
	require.NotEmpty(t, result.Functions)

	found := false
	for _, f := range result.Functions {
		if f.Name != "" {
			found = true
		}
	}
	assert.True(t, found, "Expected to find functions in list")

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

	// Query Cloud Logging for the function's log entries
	out := runCLI(t, gcloudCLI("logging", "read",
		`resource.type="cloud_run_revision" AND resource.labels.service_name="cli-invoke-fn"`,
		"--format", "json",
	))

	// Verify "Function invoked" appears in logs
	assert.Contains(t, out, "Function invoked", "expected invocation log entry")

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
