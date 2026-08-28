package azure_cli_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for the App Service functions-keys, webjobs and
// deployment-extras surfaces. Native az commands (az functionapp keys, az
// functionapp function keys, az webapp webjob triggered/continuous, az webapp
// deployment user, az webapp deployment list-publishing-credentials) run
// against a TLS simulator through the CLI's own login flow; the operations no
// az command wraps (MSDeploy/OneDeploy extension PUTs, deploymentStatus,
// sourcecontrols, host sync actions, newpassword, the invoke path) go through
// az rest. Routes exercised:
//
//	POST /sites/{siteName}/host/default/listkeys (+ slot)
//	PUT+DELETE /sites/{siteName}/host/default/{keyType}/{keyName}
//	POST /sites/{siteName}/functions/{functionName}/listkeys
//	POST /sites/{siteName}/functions/{functionName}/listsecrets
//	PUT+DELETE /sites/{siteName}/functions/{functionName}/keys/{keyName}
//	GET /sites/{siteName}/functions/admin/token
//	POST /sites/{siteName}/host/default/listsyncstatus
//	POST /sites/{siteName}/host/default/sync
//	POST /sites/{siteName}/syncfunctiontriggers
//	POST /sites/{siteName}/listsyncfunctiontriggerstatus
//	GET /sites/{siteName}/webjobs
//	GET /sites/{siteName}/triggeredwebjobs
//	GET+DELETE /sites/{siteName}/triggeredwebjobs/{webJobName}
//	POST /sites/{siteName}/triggeredwebjobs/{webJobName}/run
//	GET /sites/{siteName}/triggeredwebjobs/{webJobName}/history
//	GET /sites/{siteName}/triggeredwebjobs/{webJobName}/history/{id}
//	GET /sites/{siteName}/continuouswebjobs
//	GET+DELETE /sites/{siteName}/continuouswebjobs/{webJobName}
//	POST /sites/{siteName}/continuouswebjobs/{webJobName}/start
//	POST /sites/{siteName}/continuouswebjobs/{webJobName}/stop
//	GET+PUT /sites/{siteName}/extensions/MSDeploy
//	GET /sites/{siteName}/extensions/MSDeploy/log
//	GET+PUT /sites/{siteName}/instances/{instanceId}/extensions/MSDeploy
//	GET /sites/{siteName}/instances/{instanceId}/extensions/MSDeploy/log
//	GET+PUT /sites/{siteName}/extensions/onedeploy
//	GET /sites/{siteName}/deploymentStatus
//	GET /sites/{siteName}/deploymentStatus/{deploymentStatusId}
//	POST /sites/{siteName}/sync
//	POST /sites/{siteName}/newpassword
//	GET+PUT /providers/Microsoft.Web/publishingUsers/web
//	GET /providers/Microsoft.Web/sourcecontrols
//	GET+PUT /providers/Microsoft.Web/sourcecontrols/{sourceControlType}

const stage3CLIAlpine = "public.ecr.aws/docker/library/alpine:3.20"

// stage3PullImage pulls the webjob workload image into the local daemon so
// the simulator's if-not-present policy finds it warm.
func stage3PullImage(t *testing.T, image string) {
	t.Helper()
	var lastErr error
	delay := time.Second
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(delay)
			if delay < 8*time.Second {
				delay *= 2
			}
		}
		out, err := exec.Command("docker", "pull", image).CombinedOutput()
		if err == nil {
			return
		}
		lastErr = fmt.Errorf("%v: %s", err, out)
	}
	t.Fatalf("docker pull %s: %v", image, lastErr)
}

// stage3Zip builds an in-memory deployment package (shell scripts carry the
// executable bit).
func stage3Zip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if strings.HasSuffix(name, ".sh") {
			hdr.SetMode(0o755)
		} else {
			hdr.SetMode(0o644)
		}
		f, err := zw.CreateHeader(hdr)
		require.NoError(t, err)
		_, err = f.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func stage3ServeZip(t *testing.T, pkg []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(pkg)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/package.zip"
}

// TestWebAppStage3_NativeAzKeysAndWebJobs drives the keys and webjobs
// surfaces through native az commands, logged in to a TLS simulator running
// the full workload runtime, so a triggered run and a continuous start
// execute real containers.
func TestWebAppStage3_NativeAzKeysAndWebJobs(t *testing.T) {
	stage3PullImage(t, stage3CLIAlpine)
	env := startAzTLSSimulator(t)

	runCLI(t, env.command("cloud", "register", "-n", "sockerless-stage3",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-stage3"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	defer runCLI(t, env.command("logout"))

	rg, app := "stage3-cli-rg", "stage3-cli-app"
	runCLI(t, env.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	siteURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s?api-version=2025-03-01",
		env.baseURL, subscriptionID, rg, app)
	runCLI(t, env.command("rest", "--method", "PUT", "--url", siteURL, "--body", fmt.Sprintf(`{
		"location": "eastus",
		"kind": "functionapp",
		"properties": {"siteConfig": {"linuxFxVersion": "DOCKER|%s"}}
	}`, stage3CLIAlpine), "-o", "json"))

	var hostKeys struct {
		MasterKey    string            `json:"masterKey"`
		FunctionKeys map[string]string `json:"functionKeys"`
	}
	parseJSON(t, runCLI(t, env.command("functionapp", "keys", "list", "-g", rg, "-n", app, "-o", "json")), &hostKeys)
	require.NotEmpty(t, hostKeys.MasterKey)
	require.NotEmpty(t, hostKeys.FunctionKeys["default"])

	// az redacts the key value in the set output, so the write is verified
	// through a fresh list read. (Each read parses into its own struct —
	// json.Unmarshal merges into an existing map rather than replacing it.)
	out := runCLI(t, env.command("functionapp", "keys", "set", "-g", rg, "-n", app,
		"--key-type", "functionKeys", "--key-name", "deploy", "--key-value", "stage3-cli-secret", "-o", "json"))
	assert.Contains(t, out, `"deploy"`)
	var afterSet struct {
		FunctionKeys map[string]string `json:"functionKeys"`
	}
	parseJSON(t, runCLI(t, env.command("functionapp", "keys", "list", "-g", rg, "-n", app, "-o", "json")), &afterSet)
	assert.Equal(t, "stage3-cli-secret", afterSet.FunctionKeys["deploy"])
	runCLI(t, env.command("functionapp", "keys", "delete", "-g", rg, "-n", app,
		"--key-type", "functionKeys", "--key-name", "deploy"))
	var afterDelete struct {
		FunctionKeys map[string]string `json:"functionKeys"`
	}
	parseJSON(t, runCLI(t, env.command("functionapp", "keys", "list", "-g", rg, "-n", app, "-o", "json")), &afterDelete)
	assert.NotContains(t, afterDelete.FunctionKeys, "deploy")

	fnURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s/functions/hello?api-version=2025-03-01",
		env.baseURL, subscriptionID, rg, app)
	runCLI(t, env.command("rest", "--method", "PUT", "--url", fnURL, "--body",
		`{"properties":{"config":{"bindings":[{"type":"httpTrigger","direction":"in","name":"req","authLevel":"function"}]}}}`,
		"-o", "json"))
	out = runCLI(t, env.command("functionapp", "function", "keys", "set", "-g", rg, "-n", app,
		"--function-name", "hello", "--key-name", "ci", "--key-value", "stage3-fn-key", "-o", "json"))
	assert.Contains(t, out, `"ci"`)
	var fnKeys map[string]any
	parseJSON(t, runCLI(t, env.command("functionapp", "function", "keys", "list", "-g", rg, "-n", app,
		"--function-name", "hello", "-o", "json")), &fnKeys)
	keysOut, _ := json.Marshal(fnKeys)
	assert.Contains(t, string(keysOut), "stage3-fn-key")
	runCLI(t, env.command("functionapp", "function", "keys", "delete", "-g", rg, "-n", app,
		"--function-name", "hello", "--key-name", "ci"))

	// --- Deploy a package carrying webjobs, through the MSDeploy extension ---
	pkgURL := stage3ServeZip(t, stage3Zip(t, map[string]string{
		"App_Data/jobs/triggered/report/run.sh":   "#!/bin/sh\necho stage3 report ran\nexit 0\n",
		"App_Data/jobs/continuous/worker/run.sh":  "#!/bin/sh\nwhile true; do sleep 1; done\n",
		"App_Data/jobs/triggered/report/note.txt": "stage3\n",
	}))
	msDeployURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s/extensions/MSDeploy?api-version=2025-03-01",
		env.baseURL, subscriptionID, rg, app)
	runCLI(t, env.command("rest", "--method", "PUT", "--url", msDeployURL,
		"--body", fmt.Sprintf(`{"properties":{"packageUri":"%s"}}`, pkgURL), "-o", "json"))
	stage3AwaitCLI(t, func() (string, bool) {
		var status struct {
			Properties struct {
				ProvisioningState string `json:"provisioningState"`
			} `json:"properties"`
		}
		parseJSON(t, runCLI(t, env.command("rest", "--method", "GET", "--url", msDeployURL, "-o", "json")), &status)
		return status.Properties.ProvisioningState, status.Properties.ProvisioningState == "succeeded"
	}, "MSDeploy provisioningState")

	var triggered []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, env.command("webapp", "webjob", "triggered", "list", "-g", rg, "-n", app, "-o", "json")), &triggered)
	require.Len(t, triggered, 1)
	assert.Equal(t, app+"/report", triggered[0].Name)

	runCLI(t, env.command("webapp", "webjob", "triggered", "run", "-g", rg, "-n", app, "--webjob-name", "report"))
	historyURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s/triggeredwebjobs/report/history?api-version=2025-03-01",
		env.baseURL, subscriptionID, rg, app)
	var runID string
	stage3AwaitCLI(t, func() (string, bool) {
		var history struct {
			Value []struct {
				Name       string `json:"name"`
				Properties struct {
					Runs []struct {
						Status string `json:"status"`
					} `json:"runs"`
				} `json:"properties"`
			} `json:"value"`
		}
		parseJSON(t, runCLI(t, env.command("rest", "--method", "GET", "--url", historyURL, "-o", "json")), &history)
		if len(history.Value) == 0 || len(history.Value[0].Properties.Runs) == 0 {
			return "no runs", false
		}
		runID = history.Value[0].Name
		status := history.Value[0].Properties.Runs[0].Status
		return status, status == "Success"
	}, "triggered webjob run status")
	runOut := runCLI(t, env.command("rest", "--method", "GET", "--url",
		strings.Replace(historyURL, "/history?", "/history/"+runID+"?", 1), "-o", "json"))
	assert.Contains(t, runOut, `"Success"`)

	// The continuous job auto-started with the deployment and runs a real
	// container; stop and start are observable through its status.
	stage3AwaitCLI(t, func() (string, bool) {
		status := stage3ContinuousStatus(t, env, rg, app, "worker")
		return status, status == "Running"
	}, "continuous webjob status after deploy")
	runCLI(t, env.command("webapp", "webjob", "continuous", "stop", "-g", rg, "-n", app, "--webjob-name", "worker"))
	stage3AwaitCLI(t, func() (string, bool) {
		status := stage3ContinuousStatus(t, env, rg, app, "worker")
		return status, status == "Stopped"
	}, "continuous webjob status after stop")
	runCLI(t, env.command("webapp", "webjob", "continuous", "start", "-g", rg, "-n", app, "--webjob-name", "worker"))
	stage3AwaitCLI(t, func() (string, bool) {
		status := stage3ContinuousStatus(t, env, rg, app, "worker")
		return status, status == "Running"
	}, "continuous webjob status after start")
	runCLI(t, env.command("webapp", "webjob", "continuous", "remove", "-g", rg, "-n", app, "--webjob-name", "worker"))
	runCLI(t, env.command("webapp", "webjob", "triggered", "remove", "-g", rg, "-n", app, "--webjob-name", "report"))
	var remaining []any
	parseJSON(t, runCLI(t, env.command("webapp", "webjob", "triggered", "list", "-g", rg, "-n", app, "-o", "json")), &remaining)
	assert.Empty(t, remaining)

	runCLI(t, env.command("webapp", "deployment", "user", "set",
		"--user-name", "stage3clideployer", "--password", "St4ge3-Passw0rd!"))
	userOut := runCLI(t, env.command("webapp", "deployment", "user", "show", "-o", "json"))
	assert.Contains(t, userOut, "stage3clideployer")
	assert.NotContains(t, userOut, "St4ge3-Passw0rd!", "the publishing password is write-only")

	// The CLI prints the flattened Python SDK model, so publishingPassword is
	// a top-level member of its output.
	var credsBefore, credsAfter struct {
		PublishingPassword string `json:"publishingPassword"`
	}
	parseJSON(t, runCLI(t, env.command("webapp", "deployment", "list-publishing-credentials",
		"-g", rg, "-n", app, "-o", "json")), &credsBefore)
	require.NotEmpty(t, credsBefore.PublishingPassword)
	newPasswordURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s/newpassword?api-version=2025-03-01",
		env.baseURL, subscriptionID, rg, app)
	runCLI(t, env.command("rest", "--method", "POST", "--url", newPasswordURL))
	parseJSON(t, runCLI(t, env.command("webapp", "deployment", "list-publishing-credentials",
		"-g", rg, "-n", app, "-o", "json")), &credsAfter)
	require.NotEmpty(t, credsAfter.PublishingPassword)
	assert.NotEqual(t, credsBefore.PublishingPassword, credsAfter.PublishingPassword,
		"POST /newpassword must rotate the publishing password")
}

// stage3ContinuousStatus reads a continuous webjob's status through the
// native az command. The CLI prints the Python SDK model, which flattens the
// ARM properties bag, so status sits at the top level of each entry.
func stage3ContinuousStatus(t *testing.T, env azLoginEnv, rg, app, job string) string {
	t.Helper()
	var jobs []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	parseJSON(t, runCLI(t, env.command("webapp", "webjob", "continuous", "list", "-g", rg, "-n", app, "-o", "json")), &jobs)
	for _, j := range jobs {
		if j.Name == app+"/"+job {
			return j.Status
		}
	}
	return "<absent>"
}

// stage3AwaitCLI polls until check reports done, failing with the last
// observed state.
func stage3AwaitCLI(t *testing.T, check func() (string, bool), what string) {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for {
		state, done := check()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not converge (last: %s)", what, state)
		}
		time.Sleep(time.Second)
	}
}

// TestWebAppStage3_RestDeploymentExtrasAndInvokeAuth exercises the surfaces
// no native az command wraps, through az rest against the suite simulator:
// OneDeploy, deploymentStatus, sourcecontrols, host sync actions, the
// functions admin token, and the authLevel contract on the real invoke path.
func TestWebAppStage3_RestDeploymentExtrasAndInvokeAuth(t *testing.T) {
	app := "stage3-rest-app"
	siteURL := funcURL("sites/" + app)
	runCLI(t, azRest("PUT", siteURL, `{"location":"eastus","kind":"functionapp","properties":{"siteConfig":{}}}`))
	defer runCLI(t, azRest("DELETE", siteURL, ""))

	base := strings.TrimSuffix(siteURL, "?api-version="+functionsAPIVersion)
	sub := func(path string) string { return base + "/" + path + "?api-version=2025-03-01" }

	// OneDeploy: deploy a real package, observe the recorded status.
	pkgURL := stage3ServeZip(t, stage3Zip(t, map[string]string{"index.html": "<h1>stage3 cli</h1>"}))
	oneDeployOut := runCLI(t, azRest("PUT", sub("extensions/onedeploy"),
		fmt.Sprintf(`{"properties":{"packageUri":"%s","type":"zip"}}`, pkgURL)))
	assert.Contains(t, oneDeployOut, `"OneDeploy"`)
	statusOut := runCLI(t, azRest("GET", sub("extensions/onedeploy"), ""))
	assert.Contains(t, statusOut, `"status": 4`)

	// The deployment produced a CsmDeploymentStatus record; its id feeds the
	// single-status GET.
	var statuses struct {
		Value []struct {
			Properties struct {
				DeploymentID string `json:"deploymentId"`
				Status       string `json:"status"`
			} `json:"properties"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("GET", sub("deploymentStatus"), "")), &statuses)
	require.Len(t, statuses.Value, 1)
	assert.Equal(t, "RuntimeSuccessful", statuses.Value[0].Properties.Status)
	oneStatus := runCLI(t, azRest("GET", sub("deploymentStatus/"+statuses.Value[0].Properties.DeploymentID), ""))
	assert.Contains(t, oneStatus, "RuntimeSuccessful")

	// Instance-scoped MSDeploy.
	runCLI(t, azRest("PUT", sub("instances/0/extensions/MSDeploy"),
		fmt.Sprintf(`{"properties":{"packageUri":"%s"}}`, pkgURL)))
	stage3AwaitCLI(t, func() (string, bool) {
		var st struct {
			Properties struct {
				ProvisioningState string `json:"provisioningState"`
			} `json:"properties"`
		}
		parseJSON(t, runCLI(t, azRest("GET", sub("instances/0/extensions/MSDeploy"), "")), &st)
		return st.Properties.ProvisioningState, st.Properties.ProvisioningState == "succeeded"
	}, "instance MSDeploy provisioningState")
	instLog := runCLI(t, azRest("GET", sub("instances/0/extensions/MSDeploy/log"), ""))
	assert.Contains(t, instLog, "Deployment succeeded")

	// Repository sync + host sync family.
	runCLI(t, azRest("POST", sub("sync"), ""))
	runCLI(t, azRest("POST", sub("host/default/sync"), ""))
	runCLI(t, azRest("POST", sub("host/default/listsyncstatus"), ""))
	runCLI(t, azRest("POST", sub("syncfunctiontriggers"), ""))
	syncStatus := runCLI(t, azRest("POST", sub("listsyncfunctiontriggerstatus"), ""))
	assert.Contains(t, syncStatus, "trigger_url")

	// The functions admin token is a JWT string.
	tokenOut := strings.TrimSpace(runCLI(t, azRest("GET", sub("functions/admin/token"), "")))
	assert.True(t, strings.Count(tokenOut, ".") >= 2, "admin token must be a JWT: %s", tokenOut)

	// Provider-global source controls.
	scList := runCLI(t, azRest("GET", baseURL+"/providers/Microsoft.Web/sourcecontrols?api-version=2025-03-01", ""))
	assert.Contains(t, scList, `"GitHub"`)
	runCLI(t, azRest("PUT", baseURL+"/providers/Microsoft.Web/sourcecontrols/GitHub?api-version=2025-03-01",
		`{"properties":{"token":"gho_stage3cli"}}`))
	scGet := runCLI(t, azRest("GET", baseURL+"/providers/Microsoft.Web/sourcecontrols/GitHub?api-version=2025-03-01", ""))
	assert.Contains(t, scGet, "gho_stage3cli")

	// The authLevel contract on the real invoke path: a declared
	// authLevel=function function demands a key; the listed function key and
	// the master key open it; no key answers 401.
	runCLI(t, azRest("PUT", sub("functions/function"),
		`{"properties":{"config":{"bindings":[{"type":"httpTrigger","direction":"in","name":"req","authLevel":"function"}]}}}`))
	invokeURL := baseURL + "/api/function"
	hostHeader := "Host=" + app + ".azurewebsites.net"

	denied := azRest("POST", invokeURL, "{}", "--headers", hostHeader)
	deniedOut, err := denied.CombinedOutput()
	require.Error(t, err, "a keyless invoke of an authLevel=function function must fail")
	assert.Contains(t, string(deniedOut), "Unauthorized", "az rest must surface the 401")

	var fnKeys struct {
		Properties map[string]string `json:"properties"`
	}
	parseJSON(t, runCLI(t, azRest("POST", sub("functions/function/listkeys"), "")), &fnKeys)
	require.NotEmpty(t, fnKeys.Properties["default"])
	runCLI(t, azRest("POST", invokeURL, "{}", "--headers", hostHeader,
		"x-functions-key="+fnKeys.Properties["default"]))

	var hostKeys struct {
		MasterKey string `json:"masterKey"`
	}
	parseJSON(t, runCLI(t, azRest("POST", sub("host/default/listkeys"), "")), &hostKeys)
	require.NotEmpty(t, hostKeys.MasterKey)
	runCLI(t, azRest("POST", invokeURL, "{}", "--headers", hostHeader,
		"x-functions-key="+hostKeys.MasterKey))
}
