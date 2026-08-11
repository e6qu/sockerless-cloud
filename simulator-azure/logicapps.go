package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Microsoft.Logic/workflows ARM control plane. The slice stores the full
// workflow definition/parameters payload that clients send and exposes the
// lifecycle/status operations used by the Azure SDK, az CLI, and Terraform.

type LogicWorkflow struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location,omitempty"`
	Identity   map[string]any    `json:"identity,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
}

type LogicWorkflowRun struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

var (
	logicWorkflows sim.Store[LogicWorkflow]
	logicRuns      sim.Store[LogicWorkflowRun]
)

func registerLogicApps(srv *sim.Server) {
	makeAzureKeyGens(srv)
	logicWorkflows = sim.MakeStore[LogicWorkflow](srv.DB(), "logic_workflows")
	logicRuns = sim.MakeStore[LogicWorkflowRun](srv.DB(), "logic_workflow_runs")

	const base = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows"
	srv.HandleFunc("PUT "+base+"/{workflowName}", handleLogicWorkflowPut)
	srv.HandleFunc("PATCH "+base+"/{workflowName}", handleLogicWorkflowPatch)
	srv.HandleFunc("GET "+base+"/{workflowName}", handleLogicWorkflowGet)
	srv.HandleFunc("DELETE "+base+"/{workflowName}", handleLogicWorkflowDelete)
	srv.HandleFunc("GET "+base, handleLogicWorkflowListByRG)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Logic/workflows", handleLogicWorkflowListBySubscription)
	srv.HandleFunc("POST "+base+"/{workflowName}/disable", handleLogicWorkflowDisable)
	srv.HandleFunc("POST "+base+"/{workflowName}/enable", handleLogicWorkflowEnable)
	srv.HandleFunc("POST "+base+"/{workflowName}/validate", handleLogicWorkflowValidate)
	srv.HandleFunc("POST "+base+"/{workflowName}/triggers/{triggerName}/run", handleLogicWorkflowTriggerRun)
	srv.HandleFunc("GET "+base+"/{workflowName}/runs", handleLogicWorkflowRunList)
	srv.HandleFunc("GET "+base+"/{workflowName}/runs/{runName}", handleLogicWorkflowRunGet)
	srv.HandleFunc("POST "+base+"/{workflowName}/runs/{runName}/cancel", handleLogicWorkflowRunCancel)

	registerLogicAppsMore(srv)
}

func logicWorkflowID(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Logic/workflows/%s", sub, rg, name)
}

func logicWorkflowRunID(sub, rg, workflow, run string) string {
	return logicWorkflowID(sub, rg, workflow) + "/runs/" + run
}

func handleLogicWorkflowPut(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "workflowName")
	var req LogicWorkflow
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := logicWorkflowID(sub, rg, name)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	props := map[string]any{
		"provisioningState": "Succeeded",
		"state":             "Enabled",
		"createdTime":       now,
		"changedTime":       now,
		"version":           generateUUID(),
		"accessEndpoint":    fmt.Sprintf("%s://%s%s/triggers/manual/paths/invoke", azureRequestScheme(r), r.Host, id),
	}
	if existing, ok := logicWorkflows.Get(id); ok && existing.Properties != nil {
		if created, ok := existing.Properties["createdTime"]; ok {
			props["createdTime"] = created
		}
	}
	for k, v := range req.Properties {
		props[k] = v
	}
	logicNormalizeWorkflowProperties(props)
	wf := LogicWorkflow{
		ID:         id,
		Name:       name,
		Type:       "Microsoft.Logic/workflows",
		Location:   req.Location,
		Identity:   req.Identity,
		Tags:       req.Tags,
		Properties: props,
	}
	logicWorkflows.Put(id, wf)
	logicSnapshotWorkflowVersion(wf)
	logicSyncTriggers(wf)
	sim.WriteJSON(w, http.StatusOK, wf)
}

func handleLogicWorkflowPatch(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "workflowName")
	id := logicWorkflowID(sub, rg, name)
	var req LogicWorkflow
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	ok := logicWorkflows.Update(id, func(wf *LogicWorkflow) {
		if req.Tags != nil {
			wf.Tags = req.Tags
		}
		if req.Identity != nil {
			wf.Identity = req.Identity
		}
		if req.Properties != nil {
			if wf.Properties == nil {
				wf.Properties = map[string]any{}
			}
			for k, v := range req.Properties {
				wf.Properties[k] = v
			}
			wf.Properties["changedTime"] = time.Now().UTC().Format(time.RFC3339Nano)
			logicNormalizeWorkflowProperties(wf.Properties)
		}
	})
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", name)
		return
	}
	wf, _ := logicWorkflows.Get(id)
	sim.WriteJSON(w, http.StatusOK, wf)
}

func logicNormalizeWorkflowProperties(props map[string]any) {
	if props == nil {
		return
	}
	def, ok := props["definition"].(map[string]any)
	if !ok || def == nil {
		def = map[string]any{}
	}
	if def["$schema"] == nil {
		def["$schema"] = "https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#"
	}
	if def["contentVersion"] == nil {
		def["contentVersion"] = "1.0.0.0"
	}
	for _, key := range []string{"parameters", "triggers", "actions", "outputs"} {
		value, ok := def[key].(map[string]any)
		if !ok || value == nil {
			def[key] = map[string]any{}
		}
	}
	props["definition"] = def
	parameters, ok := props["parameters"].(map[string]any)
	if !ok || parameters == nil {
		props["parameters"] = map[string]any{}
	}
}

func handleLogicWorkflowGet(w http.ResponseWriter, r *http.Request) {
	id := logicWorkflowID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "workflowName"))
	wf, ok := logicWorkflows.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", sim.PathParam(r, "workflowName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, wf)
}

func handleLogicWorkflowDelete(w http.ResponseWriter, r *http.Request) {
	id := logicWorkflowID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "workflowName"))
	if !logicWorkflows.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", sim.PathParam(r, "workflowName"))
		return
	}
	logicDropWorkflowKeyGens(id)
	for _, run := range logicRuns.List() {
		if strings.HasPrefix(run.ID, id+"/runs/") {
			logicRuns.Delete(run.ID)
		}
	}
	w.WriteHeader(http.StatusOK)
}

func handleLogicWorkflowListByRG(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Logic/workflows/", sub, rg)
	writeLogicWorkflowList(w, prefix)
}

func handleLogicWorkflowListBySubscription(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/", sub)
	writeLogicWorkflowList(w, prefix)
}

func writeLogicWorkflowList(w http.ResponseWriter, prefix string) {
	out := logicWorkflows.Filter(func(wf LogicWorkflow) bool { return strings.HasPrefix(wf.ID, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if out == nil {
		out = []LogicWorkflow{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleLogicWorkflowDisable(w http.ResponseWriter, r *http.Request) {
	logicWorkflowSetState(w, r, "Disabled")
}

func handleLogicWorkflowEnable(w http.ResponseWriter, r *http.Request) {
	logicWorkflowSetState(w, r, "Enabled")
}

func logicWorkflowSetState(w http.ResponseWriter, r *http.Request, state string) {
	id := logicWorkflowID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "workflowName"))
	if !logicWorkflows.Update(id, func(wf *LogicWorkflow) {
		if wf.Properties == nil {
			wf.Properties = map[string]any{}
		}
		wf.Properties["state"] = state
		wf.Properties["changedTime"] = time.Now().UTC().Format(time.RFC3339Nano)
	}) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", sim.PathParam(r, "workflowName"))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleLogicWorkflowValidate(w http.ResponseWriter, r *http.Request) {
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

func handleLogicWorkflowTriggerRun(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	workflowName := sim.PathParam(r, "workflowName")
	triggerName := sim.PathParam(r, "triggerName")
	workflowID := logicWorkflowID(sub, rg, workflowName)
	wf, ok := logicWorkflows.Get(workflowID)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", workflowName)
		return
	}
	if state, _ := wf.Properties["state"].(string); strings.EqualFold(state, "Disabled") {
		sim.AzureErrorf(w, "WorkflowTriggerIsDisabled", http.StatusBadRequest, "Workflow %q is disabled.", workflowName)
		return
	}
	if !logicWorkflowHasTrigger(wf, triggerName) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Trigger %q not found.", triggerName)
		return
	}
	logicRecordTriggerRun(wf, triggerName)
	w.WriteHeader(http.StatusAccepted)
}

func logicWorkflowHasTrigger(wf LogicWorkflow, triggerName string) bool {
	def, _ := wf.Properties["definition"].(map[string]any)
	triggers, _ := def["triggers"].(map[string]any)
	if len(triggers) == 0 {
		return triggerName == "manual"
	}
	_, ok := triggers[triggerName]
	return ok
}

func handleLogicWorkflowRunList(w http.ResponseWriter, r *http.Request) {
	prefix := logicWorkflowID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "workflowName")) + "/runs/"
	out := logicRuns.Filter(func(run LogicWorkflowRun) bool { return strings.HasPrefix(run.ID, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if out == nil {
		out = []LogicWorkflowRun{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleLogicWorkflowRunGet(w http.ResponseWriter, r *http.Request) {
	id := logicWorkflowRunID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "workflowName"), sim.PathParam(r, "runName"))
	run, ok := logicRuns.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Run %q not found.", sim.PathParam(r, "runName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, run)
}

func handleLogicWorkflowRunCancel(w http.ResponseWriter, r *http.Request) {
	id := logicWorkflowRunID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "workflowName"), sim.PathParam(r, "runName"))
	if !logicRuns.Update(id, func(run *LogicWorkflowRun) {
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
