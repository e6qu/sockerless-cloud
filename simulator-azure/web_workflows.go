package main

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A workflow deployed to a site lives in the same stores as the standalone
// Microsoft.Logic slice, keyed by the site-scoped hostruntime resource ID:
// the hostruntime paths are a second coordinate onto the same Logic Apps
// engine. Only the deployed artifact files have their own store, because
// they are the site's deployment state, not Logic Apps entities.

// Keyed by the site or slot resource ID.
type webWorkflowArtifactFiles struct {
	Files map[string]any `json:"files"`
}

var webWorkflowFiles sim.Store[webWorkflowArtifactFiles]

const webHostruntimeWorkflows = "/hostruntime/runtime/webhooks/workflow/api/management/workflows"

func webWorkflowID(r *http.Request, workflow string) string {
	return webResourceID(r) + webHostruntimeWorkflows + "/" + workflow
}

func registerWebWorkflows(srv *sim.Server) {
	webWorkflowFiles = sim.MakeStore[webWorkflowArtifactFiles](srv.DB(), "web_workflow_artifact_files")

	both := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}"+suffix, h)
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}/slots/{slot}"+suffix, h)
	}

	both("GET", "/workflows", webWorkflowsList)
	both("GET", "/workflows/{workflowName}", webWorkflowsGet)
	both("POST", "/deployWorkflowArtifacts", webDeployWorkflowArtifacts)
	both("POST", "/listWorkflowsConnections", webListWorkflowsConnections)

	// The swagger declares no slot spelling for the hostruntime bridge.
	wf := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}"+webHostruntimeWorkflows+"/{workflowName}"+suffix, h)
	}

	wf("POST", "/regenerateAccessKey", webWorkflowRegenerateAccessKey)
	wf("POST", "/validate", webWorkflowValidate)

	wf("GET", "/runs", webWorkflowRunsList)
	wf("GET", "/runs/{runName}", webWorkflowRunsGet)
	wf("POST", "/runs/{runName}/cancel", webWorkflowRunsCancel)

	wf("GET", "/runs/{runName}/actions", webWorkflowRunActionsList)
	wf("GET", "/runs/{runName}/actions/{actionName}", webWorkflowRunActionsGet)
	wf("POST", "/runs/{runName}/actions/{actionName}/listExpressionTraces", handleLogicExpressionTraces)

	// Recorded runs carry no repetition state, as in the standalone
	// Microsoft.Logic slice.
	wf("GET", "/runs/{runName}/actions/{actionName}/repetitions", handleLogicEmptyList)
	wf("GET", "/runs/{runName}/actions/{actionName}/repetitions/{repetitionName}", handleLogicNestedNotFound("repetition"))
	wf("POST", "/runs/{runName}/actions/{actionName}/repetitions/{repetitionName}/listExpressionTraces", handleLogicExpressionTraces)
	wf("GET", "/runs/{runName}/actions/{actionName}/repetitions/{repetitionName}/requestHistories", handleLogicEmptyList)
	wf("GET", "/runs/{runName}/actions/{actionName}/repetitions/{repetitionName}/requestHistories/{requestHistoryName}", handleLogicNestedNotFound("request history"))
	wf("GET", "/runs/{runName}/actions/{actionName}/scopeRepetitions", handleLogicEmptyList)
	wf("GET", "/runs/{runName}/actions/{actionName}/scopeRepetitions/{repetitionName}", handleLogicNestedNotFound("scope repetition"))

	wf("GET", "/triggers", webWorkflowTriggersList)
	wf("GET", "/triggers/{triggerName}", webWorkflowTriggersGet)
	wf("GET", "/triggers/{triggerName}/schemas/json", webWorkflowTriggerSchemaJSON)
	wf("POST", "/triggers/{triggerName}/listCallbackUrl", webWorkflowTriggerListCallbackURL)
	wf("POST", "/triggers/{triggerName}/run", webWorkflowTriggerRun)

	wf("GET", "/triggers/{triggerName}/histories", webWorkflowTriggerHistoriesList)
	wf("GET", "/triggers/{triggerName}/histories/{historyName}", webWorkflowTriggerHistoriesGet)
	wf("POST", "/triggers/{triggerName}/histories/{historyName}/resubmit", webWorkflowTriggerHistoryResubmit)

	wf("GET", "/versions", webWorkflowVersionsList)
	wf("GET", "/versions/{versionId}", webWorkflowVersionsGet)
}

// Files merge into the site's artifact set, filesToDelete removes members,
// each "<workflow>/workflow.json" creates or updates that workflow, and
// appSettings merge into the site's application settings.
func webDeployWorkflowArtifacts(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	var req struct {
		AppSettings   map[string]string `json:"appSettings"`
		Files         map[string]any    `json:"files"`
		FilesToDelete []string          `json:"filesToDelete"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	resID := webResourceID(r)

	rec, _ := webWorkflowFiles.Get(resID)
	if rec.Files == nil {
		rec.Files = map[string]any{}
	}
	for name, content := range req.Files {
		rec.Files[name] = content
	}
	// A name carried by both files and filesToDelete is deleted, so the artifact
	// set and the workflow entities agree.
	deleted := map[string]bool{}
	for _, name := range req.FilesToDelete {
		deleted[name] = true
		delete(rec.Files, name)
		if wfName, ok := webWorkflowFileName(name); ok {
			webDeleteSiteWorkflow(resID + webHostruntimeWorkflows + "/" + wfName)
		}
	}
	webWorkflowFiles.Put(resID, rec)

	for name, content := range req.Files {
		if deleted[name] {
			continue
		}
		if wfName, ok := webWorkflowFileName(name); ok {
			webUpsertSiteWorkflow(resID, wfName, content)
		}
	}

	if len(req.AppSettings) > 0 {
		cfg, _ := siteConfigStore.Get(resID)
		if cfg.AppSettings == nil {
			cfg.AppSettings = map[string]string{}
		}
		for k, v := range req.AppSettings {
			cfg.AppSettings[k] = v
		}
		siteConfigStore.Put(resID, cfg)
	}
	w.WriteHeader(http.StatusOK)
}

func webWorkflowFileName(file string) (string, bool) {
	wfName, ok := strings.CutSuffix(file, "/workflow.json")
	if !ok || wfName == "" || strings.Contains(wfName, "/") {
		return "", false
	}
	return wfName, true
}

// The entity's type is the runtime-relative "workflows", so derived children
// carry "workflows/runs" and so on, as the hostruntime bridge proxies them.
func webUpsertSiteWorkflow(resID, name string, fileContent any) {
	id := resID + webHostruntimeWorkflows + "/" + name
	content, _ := fileContent.(map[string]any)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	props := map[string]any{
		"provisioningState": "Succeeded",
		"state":             "Enabled",
		"createdTime":       now,
		"changedTime":       now,
		"version":           sim.NewUUID(),
	}
	if existing, ok := logicWorkflows.Get(id); ok && existing.Properties != nil {
		if created, ok := existing.Properties["createdTime"]; ok {
			props["createdTime"] = created
		}
		if state, ok := existing.Properties["state"]; ok {
			props["state"] = state
		}
	}
	if def, ok := content["definition"]; ok {
		props["definition"] = def
	}
	if kind, ok := content["kind"]; ok {
		props["kind"] = kind
	}
	logicNormalizeWorkflowProperties(props)
	// WorkflowVersion requires a location, and the snapshot inherits the site's
	// region from here.
	location := ""
	if site, ok := webSiteStoreFor(resID).Get(resID); ok {
		location = site.Location
	}
	wf := LogicWorkflow{ID: id, Name: name, Type: "workflows", Location: location, Properties: props}
	logicWorkflows.Put(id, wf)
	logicSnapshotWorkflowVersion(wf)
	logicSyncTriggers(wf)
}

func webDeleteSiteWorkflow(wfID string) {
	if !logicWorkflows.Delete(wfID) {
		return
	}
	logicDropWorkflowKeyGens(wfID)
	for _, run := range logicRuns.Filter(func(run LogicWorkflowRun) bool { return strings.HasPrefix(run.ID, wfID+"/") }) {
		logicDeleteRun(run.ID)
	}
	for _, store := range []sim.Store[LogicResource]{logicTriggers, logicTriggerHistories, logicRunActions, logicWorkflowVersions} {
		for _, res := range store.Filter(func(res LogicResource) bool { return strings.HasPrefix(res.ID, wfID+"/") }) {
			store.Delete(res.ID)
		}
	}
}

func webSiteWorkflows(resID string) []LogicWorkflow {
	prefix := resID + webHostruntimeWorkflows + "/"
	out := logicWorkflows.Filter(func(wf LogicWorkflow) bool {
		rest := strings.TrimPrefix(wf.ID, prefix)
		return strings.HasPrefix(wf.ID, prefix) && !strings.Contains(rest, "/")
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func webWorkflowEnvelope(r *http.Request, wf LogicWorkflow) map[string]any {
	resID := webResourceID(r)
	files := map[string]any{}
	if rec, ok := webWorkflowFiles.Get(resID); ok {
		if f, ok := rec.Files[wf.Name+"/workflow.json"]; ok {
			files["workflow.json"] = f
		}
	}
	props := map[string]any{
		"files":  files,
		"health": map[string]any{"state": "Healthy"},
	}
	if state, _ := wf.Properties["state"].(string); state != "" {
		props["flowState"] = state
	}
	env := map[string]any{
		"id":         resID + "/workflows/" + wf.Name,
		"name":       wf.Name,
		"type":       webChildType(r, "workflows"),
		"properties": props,
	}
	if kind, _ := wf.Properties["kind"].(string); kind != "" {
		env["kind"] = kind
	}
	return env
}

func webWorkflowsList(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	envs := []any{}
	for _, wf := range webSiteWorkflows(webResourceID(r)) {
		envs = append(envs, webWorkflowEnvelope(r, wf))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": envs})
}

func webWorkflowsGet(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	wf, ok := logicWorkflows.Get(webWorkflowID(r, sim.PathParam(r, "workflowName")))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Workflow %q not found.", sim.PathParam(r, "workflowName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, webWorkflowEnvelope(r, wf))
}

func webListWorkflowsConnections(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	resID := webResourceID(r)
	files := map[string]any{}
	if rec, ok := webWorkflowFiles.Get(resID); ok {
		if c, ok := rec.Files["connections.json"]; ok {
			files["connections.json"] = c
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"id":         resID + "/workflows/connections",
		"name":       "connections",
		"type":       webChildType(r, "workflows"),
		"properties": map[string]any{"files": files},
	})
}

func webHostWorkflow(w http.ResponseWriter, r *http.Request) (LogicWorkflow, bool) {
	if webMissing(w, r) {
		return LogicWorkflow{}, false
	}
	wf, ok := logicWorkflows.Get(webWorkflowID(r, sim.PathParam(r, "workflowName")))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Workflow %q not found.", sim.PathParam(r, "workflowName"))
		return LogicWorkflow{}, false
	}
	return wf, true
}

func webWorkflowRegenerateAccessKey(w http.ResponseWriter, r *http.Request) {
	wf, ok := webHostWorkflow(w, r)
	if !ok {
		return
	}
	var req struct {
		KeyType string `json:"keyType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	slot := logicAccessKeySlot(req.KeyType)
	if slot == "" {
		AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest,
			"The value '%s' is not valid for property 'keyType'.", req.KeyType)
		return
	}
	azureBumpKeyGen(wf.ID, slot, "")
	w.WriteHeader(http.StatusOK)
}

func webWorkflowValidate(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	var req LogicWorkflow
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if req.Properties == nil {
		AzureError(w, "InvalidWorkflow", "Workflow properties are required.", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func webWorkflowRunsList(w http.ResponseWriter, r *http.Request) {
	wf, ok := webHostWorkflow(w, r)
	if !ok {
		return
	}
	prefix := wf.ID + "/runs/"
	out := logicRuns.Filter(func(run LogicWorkflowRun) bool { return strings.HasPrefix(run.ID, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if out == nil {
		out = []LogicWorkflowRun{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func webWorkflowRunID(r *http.Request) string {
	return armChildID(webWorkflowID(r, sim.PathParam(r, "workflowName")), "runs", sim.PathParam(r, "runName"))
}

func webWorkflowRunsGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := webHostWorkflow(w, r); !ok {
		return
	}
	run, ok := logicRuns.Get(webWorkflowRunID(r))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Run %q not found.", sim.PathParam(r, "runName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, run)
}

func webWorkflowRunsCancel(w http.ResponseWriter, r *http.Request) {
	if _, ok := webHostWorkflow(w, r); !ok {
		return
	}
	writeLogicRunCancel(w, webWorkflowRunID(r), sim.PathParam(r, "runName"))
}

func webWorkflowRunActionsList(w http.ResponseWriter, r *http.Request) {
	if _, ok := webHostWorkflow(w, r); !ok {
		return
	}
	prefix := webWorkflowRunID(r) + "/actions/"
	writeLogicResourceList(w, logicRunActions, func(res LogicResource) bool {
		rest := strings.TrimPrefix(res.ID, prefix)
		return strings.HasPrefix(res.ID, prefix) && !strings.Contains(rest, "/")
	})
}

func webWorkflowRunActionsGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := webHostWorkflow(w, r); !ok {
		return
	}
	action, ok := logicRunActions.Get(webWorkflowRunID(r) + "/actions/" + sim.PathParam(r, "actionName"))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The run action %q was not found.", sim.PathParam(r, "actionName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, action)
}

func webResolveHostWorkflowTriggers(w http.ResponseWriter, r *http.Request) (LogicWorkflow, bool) {
	wf, ok := webHostWorkflow(w, r)
	if !ok {
		return LogicWorkflow{}, false
	}
	logicSyncTriggers(wf)
	return wf, true
}

func webWorkflowTriggersList(w http.ResponseWriter, r *http.Request) {
	wf, ok := webResolveHostWorkflowTriggers(w, r)
	if !ok {
		return
	}
	prefix := wf.ID + "/triggers/"
	writeLogicResourceList(w, logicTriggers, func(res LogicResource) bool {
		rest := strings.TrimPrefix(res.ID, prefix)
		return strings.HasPrefix(res.ID, prefix) && !strings.Contains(rest, "/")
	})
}

func webWorkflowTriggersGet(w http.ResponseWriter, r *http.Request) {
	wf, ok := webResolveHostWorkflowTriggers(w, r)
	if !ok {
		return
	}
	trigger, ok := logicTriggers.Get(wf.ID + "/triggers/" + sim.PathParam(r, "triggerName"))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Trigger %q not found.", sim.PathParam(r, "triggerName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, trigger)
}

func webWorkflowTriggerSchemaJSON(w http.ResponseWriter, r *http.Request) {
	if _, ok := webResolveHostWorkflowTriggers(w, r); !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"title":   sim.PathParam(r, "triggerName"),
		"content": `{"type":"object"}`,
	})
}

func webWorkflowTriggerListCallbackURL(w http.ResponseWriter, r *http.Request) {
	wf, ok := webResolveHostWorkflowTriggers(w, r)
	if !ok {
		return
	}
	logicWriteTriggerCallbackURL(w, r, wf.ID)
}

// The run completes synchronously, so answer the declared 200: a bare 202
// with no polling URL breaks the long-running-operation contract.
func webWorkflowTriggerRun(w http.ResponseWriter, r *http.Request) {
	wf, ok := webResolveHostWorkflowTriggers(w, r)
	if !ok {
		return
	}
	triggerName := sim.PathParam(r, "triggerName")
	if state, _ := wf.Properties["state"].(string); strings.EqualFold(state, "Disabled") {
		AzureErrorf(w, "WorkflowTriggerIsDisabled", http.StatusBadRequest, "Workflow %q is disabled.", wf.Name)
		return
	}
	if !logicWorkflowHasTrigger(wf, triggerName) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Trigger %q not found.", triggerName)
		return
	}
	logicRecordTriggerRun(wf, triggerName)
	w.WriteHeader(http.StatusOK)
}

func webWorkflowTriggerHistoriesList(w http.ResponseWriter, r *http.Request) {
	wf, ok := webResolveHostWorkflowTriggers(w, r)
	if !ok {
		return
	}
	prefix := wf.ID + "/triggers/" + sim.PathParam(r, "triggerName") + "/histories/"
	writeLogicResourceList(w, logicTriggerHistories, func(res LogicResource) bool { return strings.HasPrefix(res.ID, prefix) })
}

func webWorkflowTriggerHistoriesGet(w http.ResponseWriter, r *http.Request) {
	wf, ok := webResolveHostWorkflowTriggers(w, r)
	if !ok {
		return
	}
	histID := wf.ID + "/triggers/" + sim.PathParam(r, "triggerName") + "/histories/" + sim.PathParam(r, "historyName")
	hist, ok := logicTriggerHistories.Get(histID)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Trigger history %q not found.", sim.PathParam(r, "historyName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, hist)
}

// The operation is declared 202-only, so the response carries an
// Azure-AsyncOperation URL the SDK poller drives to completion.
func webWorkflowTriggerHistoryResubmit(w http.ResponseWriter, r *http.Request) {
	wf, ok := webResolveHostWorkflowTriggers(w, r)
	if !ok {
		return
	}
	triggerName := sim.PathParam(r, "triggerName")
	histID := wf.ID + "/triggers/" + triggerName + "/histories/" + sim.PathParam(r, "historyName")
	if _, ok := logicTriggerHistories.Get(histID); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Trigger history %q not found.", sim.PathParam(r, "historyName"))
		return
	}
	site, _ := webResource(r)
	opID := startAzureAsyncOperationOutcome(func() *AsyncOperationError {
		logicRecordTriggerRun(wf, triggerName)
		return nil
	})
	opURL := azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"), "Microsoft.Web",
		logicResLocation(site.Location), "operationStatuses", opID, r.URL.Query().Get("api-version"))
	w.Header().Set("Azure-AsyncOperation", opURL)
	setAzureAsyncOperationRetryAfter(w, opID)
	w.WriteHeader(http.StatusAccepted)
}

func webWorkflowVersionsList(w http.ResponseWriter, r *http.Request) {
	wf, ok := webHostWorkflow(w, r)
	if !ok {
		return
	}
	prefix := wf.ID + "/versions/"
	writeLogicResourceList(w, logicWorkflowVersions, func(res LogicResource) bool { return strings.HasPrefix(res.ID, prefix) })
}

func webWorkflowVersionsGet(w http.ResponseWriter, r *http.Request) {
	wf, ok := webHostWorkflow(w, r)
	if !ok {
		return
	}
	version, ok := logicWorkflowVersions.Get(wf.ID + "/versions/" + sim.PathParam(r, "versionId"))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The version %q was not found.", sim.PathParam(r, "versionId"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, version)
}
