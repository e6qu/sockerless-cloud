package main

import (
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// web_workflows.go serves the App-Service-hosted Logic Apps workflow surface
// of Microsoft.Web: the /sites/{name}/hostruntime/runtime/webhooks/workflow/
// api/management bridge (workflow runs, triggers, trigger histories, run
// actions, versions, access keys), the WorkflowEnvelope read surface at
// /sites/{name}/workflows (production + slot), deployWorkflowArtifacts and
// listWorkflowsConnections. A workflow deployed to a site is a Logic Apps
// workflow: its entity, runs, triggers, trigger histories, run actions and
// versions live in the SAME stores as the standalone Microsoft.Logic slice
// (logicWorkflows, logicRuns, logicTriggers, logicTriggerHistories,
// logicRunActions, logicWorkflowVersions), keyed by the site-scoped
// hostruntime resource ID — the hostruntime paths are a second ARM coordinate
// onto the same Logic Apps engine, not a parallel implementation. Only the
// deployed artifact files themselves (wwwroot content: workflow.json per
// workflow, connections.json, host.json, ...) have their own store, because
// they are deployment state of the site, not Logic Apps entities.

// webWorkflowArtifactFiles is the deployed workflow-artifact file set of one
// site or slot (the wwwroot content deployWorkflowArtifacts writes), keyed by
// the site/slot resource ID.
type webWorkflowArtifactFiles struct {
	Files map[string]any `json:"files"`
}

var webWorkflowFiles sim.Store[webWorkflowArtifactFiles]

// webHostruntimeWorkflows is the hostruntime bridge path under a site through
// which ARM reaches the site's Logic Apps workflow runtime.
const webHostruntimeWorkflows = "/hostruntime/runtime/webhooks/workflow/api/management/workflows"

// webWorkflowID is the canonical resource ID of a site-hosted workflow: the
// hostruntime management spelling under the addressed site or slot.
func webWorkflowID(r *http.Request, workflow string) string {
	return webResourceID(r) + webHostruntimeWorkflows + "/" + workflow
}

func registerWebWorkflows(srv *sim.Server) {
	webWorkflowFiles = sim.MakeStore[webWorkflowArtifactFiles](srv.DB(), "web_workflow_artifact_files")

	both := func(method, suffix string, h http.HandlerFunc) {
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}"+suffix, h)
		srv.HandleFunc(method+" "+webProvider+"/sites/{siteName}/slots/{slot}"+suffix, h)
	}

	// WorkflowEnvelope read surface + artifact deployment (production + slot).
	both("GET", "/workflows", webWorkflowsList)
	both("GET", "/workflows/{workflowName}", webWorkflowsGet)
	both("POST", "/deployWorkflowArtifacts", webDeployWorkflowArtifacts)
	both("POST", "/listWorkflowsConnections", webListWorkflowsConnections)

	// The hostruntime workflow management bridge exists only on the
	// production site (the swagger declares no slot spelling).
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

	// Repetition-scoped sub-collections mirror the standalone Microsoft.Logic
	// slice: recorded runs carry no repetition state, so the collections are
	// empty and a named member is absent.
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

// ---- artifact deployment --------------------------------------------------

// webDeployWorkflowArtifacts realizes WebApps_DeployWorkflowArtifacts[Slot]:
// the request's files merge into the site's deployed artifact set (and
// filesToDelete removes members), every "<workflow>/workflow.json" file
// materializes or updates the named Logic Apps workflow, and appSettings merge
// into the site's application settings — the deployment surface Logic Apps
// Standard tooling (az logicapp, VS Code) drives.
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
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
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
	// filesToDelete wins over files: a name carried by both is deleted, so
	// the artifact set and the workflow entities stay consistent.
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

// webWorkflowFileName reports the workflow a deployed file belongs to: a
// workflow definition lives at "<workflow>/workflow.json" inside the artifact
// set, one directory per workflow.
func webWorkflowFileName(file string) (string, bool) {
	wfName, ok := strings.CutSuffix(file, "/workflow.json")
	if !ok || wfName == "" || strings.Contains(wfName, "/") {
		return "", false
	}
	return wfName, true
}

// webUpsertSiteWorkflow creates or updates the Logic Apps workflow a deployed
// workflow.json declares, in the same stores the standalone Microsoft.Logic
// slice uses, keyed by the site-scoped hostruntime resource ID. The entity's
// type is the runtime-relative "workflows" spelling the site's workflow
// runtime reports, so derived children carry "workflows/runs",
// "workflows/triggers", ... exactly as the hostruntime bridge proxies them.
func webUpsertSiteWorkflow(resID, name string, fileContent any) {
	id := resID + webHostruntimeWorkflows + "/" + name
	content, _ := fileContent.(map[string]any)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	props := map[string]any{
		"provisioningState": "Succeeded",
		"state":             "Enabled",
		"createdTime":       now,
		"changedTime":       now,
		"version":           generateUUID(),
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
	wf := LogicWorkflow{ID: id, Name: name, Type: "workflows", Properties: props}
	logicWorkflows.Put(id, wf)
	logicSnapshotWorkflowVersion(wf)
	logicSyncTriggers(wf)
}

// webDeleteSiteWorkflow removes a site-hosted workflow and every child entity
// recorded under it (runs, run actions, triggers, trigger histories,
// versions), plus its access-key rotation state.
func webDeleteSiteWorkflow(wfID string) {
	if !logicWorkflows.Delete(wfID) {
		return
	}
	logicDropWorkflowKeyGens(wfID)
	for _, run := range logicRuns.Filter(func(run LogicWorkflowRun) bool { return strings.HasPrefix(run.ID, wfID+"/") }) {
		logicRuns.Delete(run.ID)
	}
	for _, store := range []sim.Store[LogicResource]{logicTriggers, logicTriggerHistories, logicRunActions, logicWorkflowVersions} {
		for _, res := range store.Filter(func(res LogicResource) bool { return strings.HasPrefix(res.ID, wfID+"/") }) {
			store.Delete(res.ID)
		}
	}
}

// ---- WorkflowEnvelope read surface ------------------------------------------

// webSiteWorkflows returns the site's (or slot's) workflows, sorted by name.
func webSiteWorkflows(resID string) []LogicWorkflow {
	prefix := resID + webHostruntimeWorkflows + "/"
	out := logicWorkflows.Filter(func(wf LogicWorkflow) bool {
		rest := strings.TrimPrefix(wf.ID, prefix)
		return strings.HasPrefix(wf.ID, prefix) && !strings.Contains(rest, "/")
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// webWorkflowEnvelope projects a site-hosted workflow onto the
// WorkflowEnvelope wire shape: the deployed workflow.json under files, the
// workflow state as flowState, and the workflow health.
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
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Workflow %q not found.", sim.PathParam(r, "workflowName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, webWorkflowEnvelope(r, wf))
}

// webListWorkflowsConnections returns the site's shared connections manifest
// (the deployed connections.json) as a WorkflowEnvelope, the shape the real
// listWorkflowsConnections action emits.
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

// ---- hostruntime workflow management bridge ----------------------------------

// webHostWorkflow resolves the addressed site-hosted workflow; on a missing
// site or workflow it writes the ARM 404 and reports false.
func webHostWorkflow(w http.ResponseWriter, r *http.Request) (LogicWorkflow, bool) {
	if webMissing(w, r) {
		return LogicWorkflow{}, false
	}
	wf, ok := logicWorkflows.Get(webWorkflowID(r, sim.PathParam(r, "workflowName")))
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
	_ = sim.ReadJSON(r, &req)
	slot := logicAccessKeySlot(req.KeyType)
	if slot == "" {
		sim.AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest,
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
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if req.Properties == nil {
		sim.AzureError(w, "InvalidWorkflow", "Workflow properties are required.", http.StatusBadRequest)
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
	return webWorkflowID(r, sim.PathParam(r, "workflowName")) + "/runs/" + sim.PathParam(r, "runName")
}

func webWorkflowRunsGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := webHostWorkflow(w, r); !ok {
		return
	}
	run, ok := logicRuns.Get(webWorkflowRunID(r))
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Run %q not found.", sim.PathParam(r, "runName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, run)
}

func webWorkflowRunsCancel(w http.ResponseWriter, r *http.Request) {
	if _, ok := webHostWorkflow(w, r); !ok {
		return
	}
	if !logicRuns.Update(webWorkflowRunID(r), func(run *LogicWorkflowRun) {
		if run.Properties == nil {
			run.Properties = map[string]any{}
		}
		run.Properties["status"] = "Cancelled"
		run.Properties["endTime"] = time.Now().UTC().Format(time.RFC3339Nano)
	}) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Run %q not found.", sim.PathParam(r, "runName"))
		return
	}
	w.WriteHeader(http.StatusOK)
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
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The run action %q was not found.", sim.PathParam(r, "actionName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, action)
}

// webResolveHostWorkflowTriggers resolves the workflow and materializes its
// declared triggers, exactly as the standalone trigger surface does.
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
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Trigger %q not found.", sim.PathParam(r, "triggerName"))
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

// webWorkflowTriggerRun fires the trigger and records the run. The run
// completes synchronously, so the response is the operation's declared 200
// terminal status — ARM's long-running-operation contract forbids a bare 202
// with no polling URL.
func webWorkflowTriggerRun(w http.ResponseWriter, r *http.Request) {
	wf, ok := webResolveHostWorkflowTriggers(w, r)
	if !ok {
		return
	}
	triggerName := sim.PathParam(r, "triggerName")
	if state, _ := wf.Properties["state"].(string); strings.EqualFold(state, "Disabled") {
		sim.AzureErrorf(w, "WorkflowTriggerIsDisabled", http.StatusBadRequest, "Workflow %q is disabled.", wf.Name)
		return
	}
	if !logicWorkflowHasTrigger(wf, triggerName) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Trigger %q not found.", triggerName)
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
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Trigger history %q not found.", sim.PathParam(r, "historyName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, hist)
}

// webWorkflowTriggerHistoryResubmit re-fires the trigger behind a recorded
// history entry. The operation is declared 202-only, so the response carries
// an Azure-AsyncOperation URL the SDK poller drives to completion.
func webWorkflowTriggerHistoryResubmit(w http.ResponseWriter, r *http.Request) {
	wf, ok := webResolveHostWorkflowTriggers(w, r)
	if !ok {
		return
	}
	triggerName := sim.PathParam(r, "triggerName")
	histID := wf.ID + "/triggers/" + triggerName + "/histories/" + sim.PathParam(r, "historyName")
	if _, ok := logicTriggerHistories.Get(histID); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Trigger history %q not found.", sim.PathParam(r, "historyName"))
		return
	}
	site, _ := webResource(r)
	opID := issueAzureAsyncOperation(func() { logicRecordTriggerRun(wf, triggerName) })
	opURL := azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"), "Microsoft.Web",
		logicResLocation(site.Location), "operationStatuses", opID, r.URL.Query().Get("api-version"))
	w.Header().Set("Azure-AsyncOperation", opURL)
	w.Header().Set("Retry-After", "0")
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
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The version %q was not found.", sim.PathParam(r, "versionId"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, version)
}
