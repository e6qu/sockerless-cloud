package gcp_cli_test

import (
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"slices"
	"strings"
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

// TestFunctions_CLI_InvokeAndCheckLogs deploys a container to the Cloud Run
// service behind a function, requests the function at its serviceConfig.uri
// and at its cloudfunctions.net url, and reads the container's request log
// back with gcloud logging read.
func TestFunctions_CLI_InvokeAndCheckLogs(t *testing.T) {
	const functionID = "cli-invoke-fn"
	httpDoJSON(t, "POST", functionsBaseURL()+"?functionId="+functionID, `{
		"buildConfig": {"runtime": "go121", "entryPoint": "Handler"},
		"serviceConfig": {}
	}`)
	t.Cleanup(func() {
		resp, err := httpDo("DELETE", functionsURL(functionID), "")
		if err == nil {
			resp.Body.Close()
		}
	})

	var fn struct {
		URL           string `json:"url"`
		ServiceConfig struct {
			URI     string `json:"uri"`
			Service string `json:"service"`
		} `json:"serviceConfig"`
	}
	parseJSON(t, httpDoJSON(t, "GET", functionsURL(functionID), ""), &fn)
	require.Equal(t, fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, functionID), fn.ServiceConfig.Service)
	assert.Equal(t, fmt.Sprintf("https://%s-%s.cloudfunctions.net/%s", location, project, functionID), fn.URL)

	httpDoJSON(t, "PATCH", baseURL+"/v2/"+fn.ServiceConfig.Service,
		fmt.Sprintf(`{"template":{"containers":[{"image":%q,"args":["log-request"]}]}}`, httpProbeImageName))

	request := func(uri, path string) (int, string) {
		u, err := neturl.Parse(uri)
		require.NoError(t, err)
		resp, err := httpDoHost("GET", baseURL+path, u.Host)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	require.True(t, strings.HasSuffix(fn.ServiceConfig.URI, ".a.run.app"), "the function is served on run.app: %s", fn.ServiceConfig.URI)
	status, body := request(fn.ServiceConfig.URI, "/run-app")
	require.Equal(t, http.StatusOK, status, "body=%q", body)
	assert.Equal(t, "GET /run-app", body)
	status, body = request(fn.URL, "/"+functionID+"/cloudfunctions-net")
	require.Equal(t, http.StatusOK, status, "body=%q", body)
	assert.Equal(t, "GET /cloudfunctions-net", body)

	// Cloud Logging ingests the container's stdout asynchronously, so the read
	// is repeated until the request line the container wrote arrives.
	var out string
	var payloads []string
	require.Eventually(t, func() bool {
		out = runCLI(t, gcloudCLI("logging", "read",
			`resource.type="cloud_run_revision" AND resource.labels.service_name="`+functionID+`"`,
			"--format", "json",
		))
		payloads = logTextPayloads(out)
		return slices.Contains(payloads, "GET /cloudfunctions-net")
	}, 60*time.Second, 250*time.Millisecond,
		"the invocation never produced a Cloud Logging entry")
	assert.Contains(t, payloads, "GET /run-app", "expected the run.app request's log entry: %s", out)
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
