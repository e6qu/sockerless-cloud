package azure_sdk_test

import (
	"archive/zip"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for the App Service functions-keys, webjobs and
// deployment-extras surfaces:
//
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/host/default/listkeys
//	PUT+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/host/default/{keyType}/{keyName}
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/functions/{functionName}/listkeys
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/functions/{functionName}/listsecrets
//	PUT+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/functions/{functionName}/keys/{keyName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/functions/admin/token
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/host/default/listsyncstatus
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/host/default/sync
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/syncfunctiontriggers
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/listsyncfunctiontriggerstatus
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/webjobs
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/webjobs/{webJobName}
//	GET+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/triggeredwebjobs/{webJobName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/triggeredwebjobs
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/triggeredwebjobs/{webJobName}/run
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/triggeredwebjobs/{webJobName}/history
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/triggeredwebjobs/{webJobName}/history/{id}
//	GET+DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/continuouswebjobs/{webJobName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/continuouswebjobs
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/continuouswebjobs/{webJobName}/start
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/continuouswebjobs/{webJobName}/stop
//	GET+PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/extensions/MSDeploy
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/extensions/MSDeploy/log
//	GET+PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/instances/{instanceId}/extensions/MSDeploy
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/instances/{instanceId}/extensions/MSDeploy/log
//	GET+PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/extensions/onedeploy
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/deploymentStatus
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/deploymentStatus/{deploymentStatusId}
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/sync
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/newpassword
//	GET+PUT /providers/Microsoft.Web/publishingUsers/web
//	GET /providers/Microsoft.Web/sourcecontrols
//	GET+PUT /providers/Microsoft.Web/sourcecontrols/{sourceControlType}
//	slot mirrors: /slots/{slot}/host/default/listkeys, /slots/{slot}/functions/admin/token,
//	/slots/{slot}/host/default/sync, /slots/{slot}/host/default/listsyncstatus,
//	/slots/{slot}/host/default/{keyType}/{keyName}, /slots/{slot}/functions/{functionName}/listkeys,
//	/slots/{slot}/functions/{functionName}/listsecrets, /slots/{slot}/functions/{functionName}/keys/{keyName},
//	/slots/{slot}/syncfunctiontriggers, /slots/{slot}/listsyncfunctiontriggerstatus,
//	/slots/{slot}/webjobs, /slots/{slot}/webjobs/{webJobName},
//	/slots/{slot}/triggeredwebjobs, /slots/{slot}/triggeredwebjobs/{webJobName},
//	/slots/{slot}/triggeredwebjobs/{webJobName}/run, /slots/{slot}/triggeredwebjobs/{webJobName}/history,
//	/slots/{slot}/triggeredwebjobs/{webJobName}/history/{id},
//	/slots/{slot}/continuouswebjobs, /slots/{slot}/continuouswebjobs/{webJobName},
//	/slots/{slot}/continuouswebjobs/{webJobName}/start, /slots/{slot}/continuouswebjobs/{webJobName}/stop,
//	/slots/{slot}/extensions/MSDeploy, /slots/{slot}/extensions/MSDeploy/log,
//	/slots/{slot}/instances/{instanceId}/extensions/MSDeploy, /slots/{slot}/instances/{instanceId}/extensions/MSDeploy/log,
//	/slots/{slot}/deploymentStatus, /slots/{slot}/deploymentStatus/{deploymentStatusId},
//	/slots/{slot}/sync, /slots/{slot}/newpassword

const stage3AlpineImage = "public.ecr.aws/docker/library/alpine:3.20"

// makeJobsZip builds an in-memory zip whose entries are path → file content
// (shell scripts get the executable bit, as a real deployment package
// carries).
func makeJobsZip(t *testing.T, files map[string]string) []byte {
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

// servePackage hosts a deployment package over HTTP, the coordinate a
// packageUri points at.
func servePackage(t *testing.T, pkg []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(pkg)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/package.zip"
}

// stage3CreateFunction declares a function's config (the ARM FunctionEnvelope
// carrying its function.json bindings) through the SDK.
func stage3CreateFunction(t *testing.T, client *armappservice.WebAppsClient, rg, site, fn, authLevel string) {
	t.Helper()
	poller, err := client.BeginCreateFunction(ctx, rg, site, fn, armappservice.FunctionEnvelope{
		Properties: &armappservice.FunctionEnvelopeProperties{
			Config: map[string]any{
				"bindings": []any{
					map[string]any{"type": "httpTrigger", "direction": "in", "name": "req", "authLevel": authLevel},
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

// stage3Invoke performs the real invocation request (Host-routed
// POST /api/function) with an optional key in the given carrier, returning
// the HTTP status.
func stage3Invoke(t *testing.T, siteName, key, carrier string) int {
	t.Helper()
	url := baseURL + "/api/function"
	if key != "" && carrier == "code" {
		url += "?code=" + key
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if key != "" && carrier == "header" {
		req.Header.Set("x-functions-key", key)
	}
	req.Host = siteName + ".azurewebsites.net"
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestSDK_WebApps_HostAndFunctionKeys(t *testing.T) {
	rg, name := "sdk-web-keys-rg", "sdk-keys-app"
	azureCreateSite(t, rg, name, nil)
	defer azureDeleteSite(rg, name)

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	// The host key set real Azure provisions with the site: a master key and
	// a "default" host function key.
	hostKeys, err := client.ListHostKeys(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, hostKeys.MasterKey)
	require.NotEmpty(t, *hostKeys.MasterKey)
	require.Contains(t, hostKeys.FunctionKeys, "default")
	require.NotEmpty(t, *hostKeys.FunctionKeys["default"])

	// Host secret create (generated value), read-back, delete, and the 404 a
	// second delete answers.
	created, err := client.CreateOrUpdateHostSecret(ctx, rg, name, "functionKeys", "deploy", armappservice.KeyInfo{}, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Value)
	require.NotEmpty(t, *created.Value)
	afterCreate, err := client.ListHostKeys(ctx, rg, name, nil)
	require.NoError(t, err)
	require.Contains(t, afterCreate.FunctionKeys, "deploy")
	assert.Equal(t, *created.Value, *afterCreate.FunctionKeys["deploy"])
	_, err = client.DeleteHostSecret(ctx, rg, name, "functionKeys", "deploy", nil)
	require.NoError(t, err)
	_, err = client.DeleteHostSecret(ctx, rg, name, "functionKeys", "deploy", nil)
	require.Error(t, err, "deleting an already-deleted host secret must 404")

	// An explicit master key set through the same operation.
	newMaster := "stage3-master-" + name
	_, err = client.CreateOrUpdateHostSecret(ctx, rg, name, "masterKey", "master", armappservice.KeyInfo{Value: to.Ptr(newMaster)}, nil)
	require.NoError(t, err)
	rotated, err := client.ListHostKeys(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, newMaster, *rotated.MasterKey)

	// Function keys: minted with the function, listable, settable, deletable.
	stage3CreateFunction(t, client, rg, name, "hello", "function")
	fnKeys, err := client.ListFunctionKeys(ctx, rg, name, "hello", nil)
	require.NoError(t, err)
	require.Contains(t, fnKeys.Properties, "default")
	require.NotEmpty(t, *fnKeys.Properties["default"])

	setKey, err := client.CreateOrUpdateFunctionSecret(ctx, rg, name, "hello", "ci", armappservice.KeyInfo{Value: to.Ptr("stage3-ci-key")}, nil)
	require.NoError(t, err)
	assert.Equal(t, "stage3-ci-key", *setKey.Value)
	secrets, err := client.ListFunctionSecrets(ctx, rg, name, "hello", nil)
	require.NoError(t, err)
	assert.Equal(t, *fnKeys.Properties["default"], *secrets.Key)
	require.NotNil(t, secrets.TriggerURL)
	assert.Contains(t, *secrets.TriggerURL, name+".azurewebsites.net/api/hello")
	_, err = client.DeleteFunctionSecret(ctx, rg, name, "hello", "ci", nil)
	require.NoError(t, err)
	_, err = client.DeleteFunctionSecret(ctx, rg, name, "hello", "ci", nil)
	require.Error(t, err, "deleting an already-deleted function key must 404")

	// The functions admin token is a real HS256 JWT signed with the site's
	// master key — verify the signature, not just the shape.
	token, err := client.GetFunctionsAdminToken(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, token.Value)
	parts := strings.Split(*token.Value, ".")
	require.Len(t, parts, 3, "admin token must be a JWT")
	mac := hmac.New(sha256.New, []byte(newMaster))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	assert.Equal(t, expectedSig, parts[2], "admin token must be signed with the master key")
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
	}
	require.NoError(t, json.Unmarshal(claimsJSON, &claims))
	assert.Equal(t, "https://"+name+".azurewebsites.net/azurefunctions", claims.Aud)
	assert.Greater(t, claims.Exp, time.Now().Unix())

	// Host sync family.
	_, err = client.SyncFunctions(ctx, rg, name, nil)
	require.NoError(t, err)
	_, err = client.ListSyncStatus(ctx, rg, name, nil)
	require.NoError(t, err)
	_, err = client.SyncFunctionTriggers(ctx, rg, name, nil)
	require.NoError(t, err)
	syncTriggers, err := client.ListSyncFunctionTriggers(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, newMaster, *syncTriggers.Key)
	assert.Contains(t, *syncTriggers.TriggerURL, "/admin/host/synctriggers")

	// A deployment slot carries its own key set, distinct from production.
	slotPoller, err := client.BeginCreateOrUpdateSlot(ctx, rg, name, "staging", armappservice.Site{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	slotKeys, err := client.ListHostKeysSlot(ctx, rg, name, "staging", nil)
	require.NoError(t, err)
	require.NotNil(t, slotKeys.MasterKey)
	assert.NotEqual(t, *rotated.MasterKey, *slotKeys.MasterKey)
	slotToken, err := client.GetFunctionsAdminTokenSlot(ctx, rg, name, "staging", nil)
	require.NoError(t, err)
	assert.Len(t, strings.Split(*slotToken.Value, "."), 3)
	_, err = client.SyncFunctionsSlot(ctx, rg, name, "staging", nil)
	require.NoError(t, err)
	_, err = client.ListSyncStatusSlot(ctx, rg, name, "staging", nil)
	require.NoError(t, err)
	slotSecret, err := client.CreateOrUpdateHostSecretSlot(ctx, rg, name, "systemKeys", "durable", "staging",
		armappservice.KeyInfo{Value: to.Ptr("slot-system-key")}, nil)
	require.NoError(t, err)
	assert.Equal(t, "slot-system-key", *slotSecret.Value)
	_, err = client.DeleteHostSecretSlot(ctx, rg, name, "systemKeys", "durable", "staging", nil)
	require.NoError(t, err)
}

// TestSDK_WebApps_InvokeAuthLevelContract is the end-to-end contract of the
// Azure Functions authLevel enforcement on the real invocation path: the
// declared httpTrigger authLevel decides which key (if any) POST /api/function
// demands, and a wrong or missing key answers 401 exactly as the real
// Functions host does.
func TestSDK_WebApps_InvokeAuthLevelContract(t *testing.T) {
	rg, name := "sdk-invoke-auth-rg", "invoke-auth-app"
	azureCreateSite(t, rg, name, nil)
	defer azureDeleteSite(rg, name)

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	// authLevel "function": no key → 401; the function's own key, a host
	// function key, or the master key → 200.
	stage3CreateFunction(t, client, rg, name, "function", "function")
	assert.Equal(t, http.StatusUnauthorized, stage3Invoke(t, name, "", ""), "no key must be rejected")
	assert.Equal(t, http.StatusUnauthorized, stage3Invoke(t, name, "wrong-key", "header"), "a wrong key must be rejected")

	fnKeys, err := client.ListFunctionKeys(ctx, rg, name, "function", nil)
	require.NoError(t, err)
	fnKey := *fnKeys.Properties["default"]
	assert.Equal(t, http.StatusOK, stage3Invoke(t, name, fnKey, "code"), "the function key in ?code= must be accepted")
	assert.Equal(t, http.StatusOK, stage3Invoke(t, name, fnKey, "header"), "the function key in x-functions-key must be accepted")

	hostKeys, err := client.ListHostKeys(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, stage3Invoke(t, name, *hostKeys.FunctionKeys["default"], "header"), "a host function key must be accepted")
	assert.Equal(t, http.StatusOK, stage3Invoke(t, name, *hostKeys.MasterKey, "header"), "the master key must be accepted")

	// authLevel "admin": only the master key.
	stage3CreateFunction(t, client, rg, name, "function", "admin")
	assert.Equal(t, http.StatusUnauthorized, stage3Invoke(t, name, fnKey, "header"), "a function key must not open an admin function")
	assert.Equal(t, http.StatusUnauthorized, stage3Invoke(t, name, *hostKeys.FunctionKeys["default"], "header"), "a host function key must not open an admin function")
	assert.Equal(t, http.StatusOK, stage3Invoke(t, name, *hostKeys.MasterKey, "header"), "the master key must open an admin function")

	// authLevel "anonymous": keyless.
	stage3CreateFunction(t, client, rg, name, "function", "anonymous")
	assert.Equal(t, http.StatusOK, stage3Invoke(t, name, "", ""), "an anonymous function must stay keyless")
}

func TestSDK_WebApps_WebJobsRealRuns(t *testing.T) {
	pullImageWithRetry(t, stage3AlpineImage)
	rg, name := "sdk-webjobs-rg", "sdk-webjobs-app"
	azureCreateSiteWithImage(t, rg, name, nil, stage3AlpineImage)
	defer azureDeleteSite(rg, name)

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	pkg := makeJobsZip(t, map[string]string{
		"App_Data/jobs/triggered/report/run.sh": "#!/bin/sh\necho report ran: $WEBJOBS_NAME $WEBJOBS_RUN_ID\nexit 0\n",
		"App_Data/jobs/triggered/flaky/run.sh":  "#!/bin/sh\nexit 3\n",
		"App_Data/jobs/continuous/worker/run.sh": "#!/bin/sh\n" +
			"while true; do sleep 1; done\n",
	})
	pkgURL := servePackage(t, pkg)

	// Deploy the package through the MSDeploy long-running operation.
	deployPoller, err := client.BeginCreateMSDeployOperation(ctx, rg, name, armappservice.MSDeploy{
		Properties: &armappservice.MSDeployCore{PackageURI: to.Ptr(pkgURL)},
	}, nil)
	require.NoError(t, err)
	deployed, err := deployPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, deployed.Properties)

	status, err := client.GetMSDeployStatus(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, status.Properties.ProvisioningState)
	assert.Equal(t, armappservice.MSDeployProvisioningStateSucceeded, *status.Properties.ProvisioningState)
	require.NotNil(t, status.Properties.Complete)
	assert.True(t, *status.Properties.Complete)

	logResp, err := client.GetMSDeployLog(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, logResp.Properties)
	require.GreaterOrEqual(t, len(logResp.Properties.Entries), 2, "the MSDeploy log must record start and completion")

	// The combined webjobs view sees all three deployed jobs.
	var allJobs []*armappservice.WebJob
	pager := client.NewListWebJobsPager(rg, name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		allJobs = append(allJobs, page.Value...)
	}
	require.Len(t, allJobs, 3)
	oneJob, err := client.GetWebJob(ctx, rg, name, "report", nil)
	require.NoError(t, err)
	require.NotNil(t, oneJob.Properties.WebJobType)
	assert.Equal(t, armappservice.WebJobTypeTriggered, *oneJob.Properties.WebJobType)

	var triggered []*armappservice.TriggeredWebJob
	tpager := client.NewListTriggeredWebJobsPager(rg, name, nil)
	for tpager.More() {
		page, err := tpager.NextPage(ctx)
		require.NoError(t, err)
		triggered = append(triggered, page.Value...)
	}
	require.Len(t, triggered, 2)

	// A real run: the job's run.sh executes in a container of the site's own
	// image; the history records the container's actual exit status.
	_, err = client.RunTriggeredWebJob(ctx, rg, name, "report", nil)
	require.NoError(t, err)
	successRun := stage3AwaitRun(t, client, rg, name, "report")
	assert.Equal(t, armappservice.TriggeredWebJobStatusSuccess, *successRun.Properties.Runs[0].Status)
	require.NotNil(t, successRun.Properties.Runs[0].Duration)

	_, err = client.RunTriggeredWebJob(ctx, rg, name, "flaky", nil)
	require.NoError(t, err)
	failedRun := stage3AwaitRun(t, client, rg, name, "flaky")
	assert.Equal(t, armappservice.TriggeredWebJobStatusFailed, *failedRun.Properties.Runs[0].Status,
		"a run whose process exits non-zero must record Failed")

	// A single history entry is addressable by its run id.
	runID := *successRun.Name
	one, err := client.GetTriggeredWebJobHistory(ctx, rg, name, "report", runID, nil)
	require.NoError(t, err)
	assert.Equal(t, runID, *one.Name)
	jobDetail, err := client.GetTriggeredWebJob(ctx, rg, name, "report", nil)
	require.NoError(t, err)
	require.NotNil(t, jobDetail.Properties.LatestRun)

	// The continuous job auto-started with the deployment (no WEBJOBS_STOPPED
	// setting) and runs a real container; stop kills it, start revives it.
	stage3AwaitContinuousStatus(t, client, rg, name, "worker", armappservice.ContinuousWebJobStatusRunning)
	_, err = client.StopContinuousWebJob(ctx, rg, name, "worker", nil)
	require.NoError(t, err)
	stage3AwaitContinuousStatus(t, client, rg, name, "worker", armappservice.ContinuousWebJobStatusStopped)
	_, err = client.StartContinuousWebJob(ctx, rg, name, "worker", nil)
	require.NoError(t, err)
	stage3AwaitContinuousStatus(t, client, rg, name, "worker", armappservice.ContinuousWebJobStatusRunning)

	var continuous []*armappservice.ContinuousWebJob
	cpager := client.NewListContinuousWebJobsPager(rg, name, nil)
	for cpager.More() {
		page, err := cpager.NextPage(ctx)
		require.NoError(t, err)
		continuous = append(continuous, page.Value...)
	}
	require.Len(t, continuous, 1)

	// Deletes remove the jobs (the continuous one's container included).
	_, err = client.DeleteTriggeredWebJob(ctx, rg, name, "flaky", nil)
	require.NoError(t, err)
	_, err = client.GetTriggeredWebJob(ctx, rg, name, "flaky", nil)
	require.Error(t, err)
	_, err = client.DeleteContinuousWebJob(ctx, rg, name, "worker", nil)
	require.NoError(t, err)
	_, err = client.GetContinuousWebJob(ctx, rg, name, "worker", nil)
	require.Error(t, err)

	// The deployment left a deployment-status record observable through the
	// CsmDeploymentStatus surface, including its long-running GET.
	var statuses []*armappservice.CsmDeploymentStatus
	spager := client.NewListProductionSiteDeploymentStatusesPager(rg, name, nil)
	for spager.More() {
		page, err := spager.NextPage(ctx)
		require.NoError(t, err)
		statuses = append(statuses, page.Value...)
	}
	require.NotEmpty(t, statuses)
	assert.Equal(t, armappservice.DeploymentBuildStatusRuntimeSuccessful, *statuses[0].Properties.Status)
	getPoller, err := client.BeginGetProductionSiteDeploymentStatus(ctx, rg, name, *statuses[0].Properties.DeploymentID, nil)
	require.NoError(t, err)
	finalStatus, err := getPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.DeploymentBuildStatusRuntimeSuccessful, *finalStatus.Properties.Status)
}

// stage3AwaitRun polls a triggered webjob's history until its newest run
// reaches a terminal status.
func stage3AwaitRun(t *testing.T, client *armappservice.WebAppsClient, rg, name, job string) *armappservice.TriggeredJobHistory {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for {
		var entries []*armappservice.TriggeredJobHistory
		pager := client.NewListTriggeredWebJobHistoryPager(rg, name, job, nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			require.NoError(t, err)
			entries = append(entries, page.Value...)
		}
		if len(entries) > 0 && len(entries[0].Properties.Runs) > 0 {
			// The SDK's enum predates the 2025-03-01 "Running" member, so the
			// in-flight state is compared as the wire string it is.
			if status := *entries[0].Properties.Runs[0].Status; string(status) != "Running" {
				return entries[0]
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("webjob %q run did not reach a terminal status in time (history entries: %d)", job, len(entries))
		}
		time.Sleep(time.Second)
	}
}

// stage3AwaitContinuousStatus polls a continuous webjob until it reports the
// wanted status.
func stage3AwaitContinuousStatus(t *testing.T, client *armappservice.WebAppsClient, rg, name, job string, want armappservice.ContinuousWebJobStatus) {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for {
		got, err := client.GetContinuousWebJob(ctx, rg, name, job, nil)
		require.NoError(t, err)
		if got.Properties.Status != nil && *got.Properties.Status == want {
			return
		}
		if time.Now().After(deadline) {
			cur := "<nil>"
			if got.Properties.Status != nil {
				cur = string(*got.Properties.Status)
			}
			t.Fatalf("continuous webjob %q status = %s, want %s", job, cur, want)
		}
		time.Sleep(time.Second)
	}
}

func TestSDK_WebApps_DeploymentExtras(t *testing.T) {
	rg, name := "sdk-deploy-extra-rg", "sdk-deploy-extra-app"
	azureCreateSite(t, rg, name, nil)
	defer azureDeleteSite(rg, name)

	cred := &fakeCredential{}
	client, err := armappservice.NewWebAppsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	pkg := makeJobsZip(t, map[string]string{"index.html": "<h1>stage3</h1>"})
	pkgURL := servePackage(t, pkg)

	// OneDeploy. The swagger models no request body for
	// WebApps_CreateOneDeployOperation, so the deployment source travels the
	// way az webapp deploy sends it: a JSON body naming the packageUri.
	putURL := baseURL + "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.Web/sites/" + name + "/extensions/onedeploy?api-version=2025-03-01"
	putReq, err := http.NewRequestWithContext(ctx, "PUT", putURL,
		strings.NewReader(`{"properties":{"packageUri":"`+pkgURL+`","type":"zip"}}`))
	require.NoError(t, err)
	putReq.Header.Set("Content-Type", "application/json")
	putReq.Header.Set("Authorization", simARMBearer)
	putResp, err := http.DefaultClient.Do(putReq)
	require.NoError(t, err)
	putBody, _ := io.ReadAll(putResp.Body)
	putResp.Body.Close()
	require.Equal(t, http.StatusOK, putResp.StatusCode, "OneDeploy PUT: %s", putBody)

	oneDeploy, err := client.GetOneDeployStatus(ctx, rg, name, nil)
	require.NoError(t, err)
	oneDeployObj, ok := oneDeploy.Interface.(map[string]any)
	require.True(t, ok, "OneDeploy status must be a JSON object")
	assert.Equal(t, float64(4), oneDeployObj["status"], "OneDeploy must report Kudu status 4 (Success)")
	assert.Equal(t, "OneDeploy", oneDeployObj["deployer"])

	// The OneDeploy deployment produced a deployment-status record.
	var statuses []*armappservice.CsmDeploymentStatus
	spager := client.NewListProductionSiteDeploymentStatusesPager(rg, name, nil)
	for spager.More() {
		page, err := spager.NextPage(ctx)
		require.NoError(t, err)
		statuses = append(statuses, page.Value...)
	}
	require.Len(t, statuses, 1)
	assert.Equal(t, armappservice.DeploymentBuildStatusRuntimeSuccessful, *statuses[0].Properties.Status)

	// Instance-scoped MSDeploy (the sim's one instance).
	instPoller, err := client.BeginCreateInstanceMSDeployOperation(ctx, rg, name, "0", armappservice.MSDeploy{
		Properties: &armappservice.MSDeployCore{PackageURI: to.Ptr(pkgURL)},
	}, nil)
	require.NoError(t, err)
	_, err = instPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	instStatus, err := client.GetInstanceMsDeployStatus(ctx, rg, name, "0", nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.MSDeployProvisioningStateSucceeded, *instStatus.Properties.ProvisioningState)
	instLog, err := client.GetInstanceMSDeployLog(ctx, rg, name, "0", nil)
	require.NoError(t, err)
	require.NotEmpty(t, instLog.Properties.Entries)

	// Repository sync succeeds against the configured (empty) source control.
	_, err = client.SyncRepository(ctx, rg, name, nil)
	require.NoError(t, err)

	// newpassword rotates the SCM publishing password observably.
	credsPoller, err := client.BeginListPublishingCredentials(ctx, rg, name, nil)
	require.NoError(t, err)
	before, err := credsPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.GenerateNewSitePublishingPassword(ctx, rg, name, nil)
	require.NoError(t, err)
	credsPoller, err = client.BeginListPublishingCredentials(ctx, rg, name, nil)
	require.NoError(t, err)
	after, err := credsPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.NotEqual(t, *before.Properties.PublishingPassword, *after.Properties.PublishingPassword,
		"newpassword must rotate the publishing password")

	// Provider-global publishing user: write, echo without the password, and
	// read back.
	mgmt, err := armappservice.NewWebSiteManagementClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	updated, err := mgmt.UpdatePublishingUser(ctx, armappservice.User{
		Properties: &armappservice.UserProperties{
			PublishingUserName: to.Ptr("stage3-deployer"),
			PublishingPassword: to.Ptr("stage3-P@ssw0rd!"),
		},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "stage3-deployer", *updated.Properties.PublishingUserName)
	assert.Nil(t, updated.Properties.PublishingPassword, "the publishing password is write-only")
	fetched, err := mgmt.GetPublishingUser(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "stage3-deployer", *fetched.Properties.PublishingUserName)

	// Provider-global source controls: catalog, token write, read-back.
	var sourceControls []*armappservice.SourceControl
	scPager := mgmt.NewListSourceControlsPager(nil)
	for scPager.More() {
		page, err := scPager.NextPage(ctx)
		require.NoError(t, err)
		sourceControls = append(sourceControls, page.Value...)
	}
	names := make([]string, 0, len(sourceControls))
	for _, sc := range sourceControls {
		names = append(names, *sc.Name)
	}
	assert.Contains(t, names, "GitHub")
	assert.Contains(t, names, "Bitbucket")
	scUpdated, err := mgmt.UpdateSourceControl(ctx, "GitHub", armappservice.SourceControl{
		Properties: &armappservice.SourceControlProperties{Token: to.Ptr("gho_stage3")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "gho_stage3", *scUpdated.Properties.Token)
	scFetched, err := mgmt.GetSourceControl(ctx, "GitHub", nil)
	require.NoError(t, err)
	assert.Equal(t, "gho_stage3", *scFetched.Properties.Token)

	// Slot mirrors of the deployment surface: MSDeploy + deploymentStatus on
	// a slot.
	slotPoller, err := client.BeginCreateOrUpdateSlot(ctx, rg, name, "staging", armappservice.Site{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	slotDeploy, err := client.BeginCreateMSDeployOperationSlot(ctx, rg, name, "staging", armappservice.MSDeploy{
		Properties: &armappservice.MSDeployCore{PackageURI: to.Ptr(pkgURL)},
	}, nil)
	require.NoError(t, err)
	_, err = slotDeploy.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	slotStatus, err := client.GetMSDeployStatusSlot(ctx, rg, name, "staging", nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.MSDeployProvisioningStateSucceeded, *slotStatus.Properties.ProvisioningState)
	slotLog, err := client.GetMSDeployLogSlot(ctx, rg, name, "staging", nil)
	require.NoError(t, err)
	require.NotEmpty(t, slotLog.Properties.Entries)
	var slotStatuses []*armappservice.CsmDeploymentStatus
	slotSPager := client.NewListSlotSiteDeploymentStatusesSlotPager(rg, name, "staging", nil)
	for slotSPager.More() {
		page, err := slotSPager.NextPage(ctx)
		require.NoError(t, err)
		slotStatuses = append(slotStatuses, page.Value...)
	}
	require.Len(t, slotStatuses, 1)
	slotGet, err := client.BeginGetSlotSiteDeploymentStatusSlot(ctx, rg, name, "staging", *slotStatuses[0].Properties.DeploymentID, nil)
	require.NoError(t, err)
	slotFinal, err := slotGet.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.DeploymentBuildStatusRuntimeSuccessful, *slotFinal.Properties.Status)
	_, err = client.GenerateNewSitePublishingPasswordSlot(ctx, rg, name, "staging", nil)
	require.NoError(t, err)
	_, err = client.SyncRepositorySlot(ctx, rg, name, "staging", nil)
	require.NoError(t, err)
}
