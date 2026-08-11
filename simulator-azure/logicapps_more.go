package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Extended Microsoft.Logic ARM control plane: workflow versions/triggers/run
// actions, the integration-account artifact collections (maps, schemas,
// partners, agreements, assemblies, batch configurations, sessions,
// certificates) and their callback-url actions, and the integration service
// environments (with managed APIs) long-running operations. Every resource is
// keyed by its real ARM resource id (the request path), so list operations
// filter by id prefix exactly as the cloud does.

// LogicResource is the generic ARM resource envelope shared by every Logic
// child type. The Resource-based types carry location/tags; the SubResource
// based types (triggers, run actions, trigger histories) leave them empty so
// `omitempty` keeps them out of the response. Integration accounts and
// integration service environments add sku/identity.
type LogicResource struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	Identity   map[string]any    `json:"identity,omitempty"`
	Sku        map[string]any    `json:"sku,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
}

var (
	logicWorkflowVersions sim.Store[LogicResource]
	logicTriggers         sim.Store[LogicResource]
	logicTriggerHistories sim.Store[LogicResource]
	logicRunActions       sim.Store[LogicResource]
	logicIntegrationAccts sim.Store[LogicResource]
	logicIAArtifacts      sim.Store[LogicResource]
	logicServiceEnvs      sim.Store[LogicResource]
	logicSEManagedApis    sim.Store[LogicResource]
)

func registerLogicAppsMore(srv *sim.Server) {
	logicWorkflowVersions = sim.MakeStore[LogicResource](srv.DB(), "logic_workflow_versions")
	logicTriggers = sim.MakeStore[LogicResource](srv.DB(), "logic_workflow_triggers")
	logicTriggerHistories = sim.MakeStore[LogicResource](srv.DB(), "logic_trigger_histories")
	logicRunActions = sim.MakeStore[LogicResource](srv.DB(), "logic_run_actions")
	logicIntegrationAccts = sim.MakeStore[LogicResource](srv.DB(), "logic_integration_accounts")
	logicIAArtifacts = sim.MakeStore[LogicResource](srv.DB(), "logic_integration_account_artifacts")
	logicServiceEnvs = sim.MakeStore[LogicResource](srv.DB(), "logic_service_environments")
	logicSEManagedApis = sim.MakeStore[LogicResource](srv.DB(), "logic_service_environment_managed_apis")

	// ---- provider operations ----
	srv.HandleFunc("GET /providers/Microsoft.Logic/operations", handleLogicOperationsList)

	// ---- workflow extras ----
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/generateUpgradedDefinition", handleLogicWorkflowGenerateUpgradedDefinition)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/listCallbackUrl", handleLogicWorkflowListCallbackURL)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/listSwagger", handleLogicWorkflowListSwagger)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/move", handleLogicWorkflowMove)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/regenerateAccessKey", handleLogicWorkflowRegenerateAccessKey)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/locations/{location}/workflows/{workflowName}/validate", handleLogicWorkflowValidateByLocation)

	// ---- workflow versions ----
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/versions", handleLogicVersionList)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/versions/{versionId}", handleLogicResourceGet(logicWorkflowVersions, "version"))
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/versions/{versionId}/triggers/{triggerName}/listCallbackUrl", handleLogicTriggerListCallbackURL)

	// ---- workflow triggers ----
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/triggers", handleLogicTriggerList)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/triggers/{triggerName}", handleLogicTriggerGet)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/triggers/{triggerName}/schemas/json", handleLogicTriggerSchemaJSON)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/triggers/{triggerName}/listCallbackUrl", handleLogicTriggerListCallbackURL)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/triggers/{triggerName}/reset", handleLogicTriggerReset)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/triggers/{triggerName}/setState", handleLogicTriggerSetState)

	// ---- workflow trigger histories ----
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/triggers/{triggerName}/histories", handleLogicTriggerHistoryList)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/triggers/{triggerName}/histories/{historyName}", handleLogicResourceGet(logicTriggerHistories, "trigger history"))
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/triggers/{triggerName}/histories/{historyName}/resubmit", handleLogicTriggerHistoryResubmit)

	// ---- workflow run actions and their nested collections ----
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/runs/{runName}/actions", handleLogicRunActionList)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/runs/{runName}/actions/{actionName}", handleLogicResourceGet(logicRunActions, "run action"))
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/runs/{runName}/actions/{actionName}/repetitions", handleLogicEmptyList)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/runs/{runName}/actions/{actionName}/repetitions/{repetitionName}", handleLogicNestedNotFound("repetition"))
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/runs/{runName}/actions/{actionName}/repetitions/{repetitionName}/requestHistories", handleLogicEmptyList)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/runs/{runName}/actions/{actionName}/repetitions/{repetitionName}/requestHistories/{requestHistoryName}", handleLogicNestedNotFound("request history"))
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/runs/{runName}/actions/{actionName}/requestHistories", handleLogicEmptyList)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/runs/{runName}/actions/{actionName}/requestHistories/{requestHistoryName}", handleLogicNestedNotFound("request history"))
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/runs/{runName}/actions/{actionName}/scopeRepetitions", handleLogicEmptyList)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/runs/{runName}/actions/{actionName}/scopeRepetitions/{repetitionName}", handleLogicNestedNotFound("scope repetition"))
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/runs/{runName}/operations/{operationId}", handleLogicNestedNotFound("run operation"))
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/runs/{runName}/actions/{actionName}/listExpressionTraces", handleLogicExpressionTraces)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/workflows/{workflowName}/runs/{runName}/actions/{actionName}/repetitions/{repetitionName}/listExpressionTraces", handleLogicExpressionTraces)

	// ---- integration accounts ----
	srv.HandleFunc("PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}", handleLogicResourcePut(logicIntegrationAccts, "Microsoft.Logic/integrationAccounts", http.StatusOK, false))
	srv.HandleFunc("PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}", handleLogicResourcePatch(logicIntegrationAccts, "integration account"))
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}", handleLogicResourceGet(logicIntegrationAccts, "integration account"))
	srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}", handleLogicIntegrationAccountDelete)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts", handleLogicResourceList(logicIntegrationAccts))
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Logic/integrationAccounts", handleLogicIntegrationAccountListBySub)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/listCallbackUrl", handleLogicIntegrationAccountCallbackURL)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/listKeyVaultKeys", handleLogicIntegrationAccountKeyVaultKeys)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/logTrackingEvents", handleLogicIntegrationAccountLogTrackingEvents)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/regenerateAccessKey", handleLogicIntegrationAccountRegenerateAccessKey)

	// ---- integration account artifacts ----
	logicRegisterArtifact(srv, "maps", "Microsoft.Logic/integrationAccounts/maps", true)
	logicRegisterArtifact(srv, "schemas", "Microsoft.Logic/integrationAccounts/schemas", true)
	logicRegisterArtifact(srv, "partners", "Microsoft.Logic/integrationAccounts/partners", true)
	logicRegisterArtifact(srv, "agreements", "Microsoft.Logic/integrationAccounts/agreements", true)
	logicRegisterArtifact(srv, "assemblies", "Microsoft.Logic/integrationAccounts/assemblies", false)
	logicRegisterArtifact(srv, "batchConfigurations", "Microsoft.Logic/integrationAccounts/batchConfigurations", true)
	logicRegisterArtifact(srv, "sessions", "Microsoft.Logic/integrationAccounts/sessions", true)
	logicRegisterArtifact(srv, "certificates", "Microsoft.Logic/integrationAccounts/certificates", true)

	// ---- integration service environments ----
	srv.HandleFunc("PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationServiceEnvironments/{integrationServiceEnvironmentName}", handleLogicServiceEnvPut)
	srv.HandleFunc("PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationServiceEnvironments/{integrationServiceEnvironmentName}", handleLogicServiceEnvPatch)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationServiceEnvironments/{integrationServiceEnvironmentName}", handleLogicResourceGet(logicServiceEnvs, "integration service environment"))
	srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationServiceEnvironments/{integrationServiceEnvironmentName}", handleLogicServiceEnvDelete)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationServiceEnvironments", handleLogicResourceList(logicServiceEnvs))
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Logic/integrationServiceEnvironments", handleLogicServiceEnvListBySub)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationServiceEnvironments/{integrationServiceEnvironmentName}/restart", handleLogicServiceEnvRestart)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationServiceEnvironments/{integrationServiceEnvironmentName}/health/network", handleLogicServiceEnvNetworkHealth)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationServiceEnvironments/{integrationServiceEnvironmentName}/skus", handleLogicEmptyList)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationServiceEnvironments/{integrationServiceEnvironmentName}/managedApis", handleLogicResourceList(logicSEManagedApis))
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationServiceEnvironments/{integrationServiceEnvironmentName}/managedApis/{apiName}", handleLogicResourceGet(logicSEManagedApis, "managed API"))
	srv.HandleFunc("PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationServiceEnvironments/{integrationServiceEnvironmentName}/managedApis/{apiName}", handleLogicServiceEnvManagedApiPut)
	srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationServiceEnvironments/{integrationServiceEnvironmentName}/managedApis/{apiName}", handleLogicServiceEnvManagedApiDelete)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationServiceEnvironments/{integrationServiceEnvironmentName}/managedApis/{apiName}/apiOperations", handleLogicEmptyList)
}

func logicNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func logicLastSeg(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// ---- generic CRUD bound to a store, keyed by the request path (the ARM id) ----

func handleLogicResourcePut(store sim.Store[LogicResource], typ string, status int, timestamps bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path
		var req LogicResource
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		props := map[string]any{}
		for k, v := range req.Properties {
			props[k] = v
		}
		now := logicNow()
		if existing, ok := store.Get(id); ok && existing.Properties != nil {
			if c, ok := existing.Properties["createdTime"]; ok {
				props["createdTime"] = c
			}
		} else if timestamps {
			props["createdTime"] = now
		}
		if timestamps {
			props["changedTime"] = now
		}
		store.Put(id, LogicResource{
			ID: id, Name: logicLastSeg(id), Type: typ,
			Location: req.Location, Tags: req.Tags, Identity: req.Identity, Sku: req.Sku,
			Properties: props,
		})
		res, _ := store.Get(id)
		sim.WriteJSON(w, status, res)
	}
}

func handleLogicResourcePatch(store sim.Store[LogicResource], label string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path
		var req LogicResource
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		if !store.Update(id, func(res *LogicResource) {
			if req.Tags != nil {
				res.Tags = req.Tags
			}
			if req.Sku != nil {
				res.Sku = req.Sku
			}
			if req.Identity != nil {
				res.Identity = req.Identity
			}
			if req.Properties != nil {
				if res.Properties == nil {
					res.Properties = map[string]any{}
				}
				for k, v := range req.Properties {
					res.Properties[k] = v
				}
			}
		}) {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The %s %q was not found.", label, logicLastSeg(id))
			return
		}
		res, _ := store.Get(id)
		sim.WriteJSON(w, http.StatusOK, res)
	}
}

func handleLogicResourceGet(store sim.Store[LogicResource], label string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, ok := store.Get(r.URL.Path)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The %s %q was not found.", label, logicLastSeg(r.URL.Path))
			return
		}
		sim.WriteJSON(w, http.StatusOK, res)
	}
}

func handleLogicResourceDelete(store sim.Store[LogicResource]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !store.Delete(r.URL.Path) {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The resource %q was not found.", logicLastSeg(r.URL.Path))
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func handleLogicResourceList(store sim.Store[LogicResource]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prefix := strings.TrimRight(r.URL.Path, "/") + "/"
		writeLogicResourceList(w, store, func(res LogicResource) bool {
			rest := strings.TrimPrefix(res.ID, prefix)
			return strings.HasPrefix(res.ID, prefix) && !strings.Contains(rest, "/")
		})
	}
}

func writeLogicResourceList(w http.ResponseWriter, store sim.Store[LogicResource], match func(LogicResource) bool) {
	out := store.Filter(match)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if out == nil {
		out = []LogicResource{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleLogicEmptyList(w http.ResponseWriter, _ *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
}

func handleLogicNestedNotFound(label string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The %s %q was not found.", label, logicLastSeg(r.URL.Path))
	}
}

func handleLogicExpressionTraces(w http.ResponseWriter, _ *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"inputs": []any{}})
}

// ---- provider operations ----

func handleLogicOperationsList(w http.ResponseWriter, _ *http.Request) {
	op := func(name, resource, operation, description string) map[string]any {
		return map[string]any{
			"name":   name,
			"origin": "user,system",
			"display": map[string]any{
				"provider":    "Microsoft.Logic",
				"resource":    resource,
				"operation":   operation,
				"description": description,
			},
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{
		op("Microsoft.Logic/workflows/read", "Workflows", "Get Workflow", "Gets the workflow."),
		op("Microsoft.Logic/workflows/write", "Workflows", "Create or update Workflow", "Creates or updates the workflow."),
		op("Microsoft.Logic/workflows/delete", "Workflows", "Delete Workflow", "Deletes the workflow."),
		op("Microsoft.Logic/integrationAccounts/read", "IntegrationAccounts", "Get Integration Account", "Gets the integration account."),
		op("Microsoft.Logic/integrationAccounts/write", "IntegrationAccounts", "Create or update Integration Account", "Creates or updates the integration account."),
		op("Microsoft.Logic/integrationServiceEnvironments/read", "IntegrationServiceEnvironments", "Get Integration Service Environment", "Gets the integration service environment."),
		op("Microsoft.Logic/operations/read", "Operations", "List operations", "Lists all of the available Microsoft.Logic provider operations."),
	}})
}

// ---- workflow extras ----

func handleLogicWorkflowGenerateUpgradedDefinition(w http.ResponseWriter, r *http.Request) {
	wf, ok := logicWorkflows.Get(strings.TrimSuffix(r.URL.Path, "/generateUpgradedDefinition"))
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", sim.PathParam(r, "workflowName"))
		return
	}
	def, _ := wf.Properties["definition"].(map[string]any)
	if def == nil {
		def = map[string]any{}
	}
	sim.WriteJSON(w, http.StatusOK, def)
}

func handleLogicWorkflowListSwagger(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "workflowName")
	if _, ok := logicWorkflows.Get(strings.TrimSuffix(r.URL.Path, "/listSwagger")); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"swagger": "2.0",
		"info":    map[string]any{"title": name, "version": "1.0.0"},
		"paths":   map[string]any{},
	})
}

// logicAccessKeySlot maps the KeyType enum regenerateAccessKey sends to the
// workflow's access-key slot. Real Logic Apps signs callback URLs with the
// primary key and keeps a secondary for staged rotation.
func logicAccessKeySlot(keyType string) string {
	switch strings.ToLower(keyType) {
	case "primary", "":
		return "logic-access-primary"
	case "secondary":
		return "logic-access-secondary"
	}
	return ""
}

// logicCallbackSignature derives the `sig` a Logic Apps callback URL carries.
// Real Logic Apps signs the callback with the workflow's access key, so
// regenerating that key invalidates every previously issued callback URL —
// which is exactly what a caller observes here: listCallbackUrl returns a
// different signature after regenerateAccessKey.
func logicCallbackSignature(workflowID, path string) string {
	key := azureKeyMaterial32(workflowID, "logic-access-primary")
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(path))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// logicCallbackQueries is the query set a Logic Apps callback URL carries:
// the API version, the signed permission + signature version, and the
// access-key-derived signature.
func logicCallbackQueries(workflowID, path string) map[string]any {
	return map[string]any{
		"api-version": "2016-10-01",
		"sp":          "%2Ftriggers%2Fmanual%2Frun",
		"sv":          "1.0",
		"sig":         logicCallbackSignature(workflowID, path),
	}
}

// logicCallbackURL appends the signed query string to a callback endpoint.
func logicCallbackURL(endpoint string, queries map[string]any) string {
	sep := "?"
	if strings.Contains(endpoint, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%sapi-version=%s&sp=%s&sv=%s&sig=%s",
		endpoint, sep, queries["api-version"], queries["sp"], queries["sv"], queries["sig"])
}

// logicDropWorkflowKeyGens removes a deleted workflow's access-key rotation
// state so a later workflow of the same name signs with fresh key material.
func logicDropWorkflowKeyGens(workflowID string) {
	azureDropKeyGens(workflowID, "logic-access-primary", "logic-access-secondary")
}

func handleLogicWorkflowListCallbackURL(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.URL.Path, "/listCallbackUrl")
	wf, ok := logicWorkflows.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", sim.PathParam(r, "workflowName"))
		return
	}
	endpoint, _ := wf.Properties["accessEndpoint"].(string)
	queries := logicCallbackQueries(id, id+"/triggers/manual/paths/invoke")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value":        logicCallbackURL(endpoint, queries),
		"method":       "POST",
		"basePath":     endpoint,
		"relativePath": "/triggers/manual/paths/invoke",
		"queries":      queries,
	})
}

// handleLogicWorkflowRegenerateAccessKey rotates the workflow's primary or
// secondary access key. Because callback URLs are signed with the access key,
// a subsequent listCallbackUrl returns a different signature — real Logic
// Apps invalidates outstanding callback URLs the same way.
func handleLogicWorkflowRegenerateAccessKey(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.URL.Path, "/regenerateAccessKey")
	if _, ok := logicWorkflows.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", sim.PathParam(r, "workflowName"))
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
	azureBumpKeyGen(id, slot, "")
	w.WriteHeader(http.StatusOK)
}

func handleLogicWorkflowValidateByLocation(w http.ResponseWriter, r *http.Request) {
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

func handleLogicWorkflowMove(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.URL.Path, "/move")
	wf, ok := logicWorkflows.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", sim.PathParam(r, "workflowName"))
		return
	}
	var ref struct {
		ID string `json:"id"`
	}
	_ = sim.ReadJSON(r, &ref)
	sub := sim.PathParam(r, "subscriptionId")
	opID := issueAzureAsyncOperation(func() {
		if ref.ID == "" || ref.ID == id {
			return
		}
		moved := wf
		moved.ID = ref.ID
		moved.Name = logicLastSeg(ref.ID)
		logicWorkflows.Put(ref.ID, moved)
		logicWorkflows.Delete(id)
	})
	// Move is a POST long-running operation with no result resource: advertise
	// only the Azure-AsyncOperation status URL (no Location), so the SDK poller
	// reads the operation status rather than re-GETting the POST action path.
	opURL := azureAsyncOperationHeader(r, sub, "Microsoft.Logic", logicResLocation(wf.Location), "operationStatuses", opID, r.URL.Query().Get("api-version"))
	w.Header().Set("Azure-AsyncOperation", opURL)
	w.Header().Set("Retry-After", "0")
	w.WriteHeader(http.StatusAccepted)
}

// ---- workflow versions ----

func handleLogicVersionList(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Path + "/"
	writeLogicResourceList(w, logicWorkflowVersions, func(res LogicResource) bool { return strings.HasPrefix(res.ID, prefix) })
}

func logicSnapshotWorkflowVersion(wf LogicWorkflow) {
	version, _ := wf.Properties["version"].(string)
	if version == "" {
		return
	}
	id := wf.ID + "/versions/" + version
	props := map[string]any{}
	for _, k := range []string{"definition", "parameters", "provisioningState", "state", "version", "createdTime", "changedTime", "accessEndpoint"} {
		if v, ok := wf.Properties[k]; ok {
			props[k] = v
		}
	}
	logicWorkflowVersions.Put(id, LogicResource{
		ID: id, Name: version, Type: "Microsoft.Logic/workflows/versions", Properties: props,
	})
}

// ---- workflow triggers ----

func logicSyncTriggers(wf LogicWorkflow) {
	state, _ := wf.Properties["state"].(string)
	if state == "" {
		state = "Enabled"
	}
	def, _ := wf.Properties["definition"].(map[string]any)
	triggers, _ := def["triggers"].(map[string]any)
	names := make([]string, 0, len(triggers))
	for tn := range triggers {
		names = append(names, tn)
	}
	if len(names) == 0 {
		names = []string{"manual"}
	}
	for _, tn := range names {
		id := wf.ID + "/triggers/" + tn
		if _, ok := logicTriggers.Get(id); ok {
			continue
		}
		now := logicNow()
		logicTriggers.Put(id, LogicResource{
			ID: id, Name: tn, Type: "Microsoft.Logic/workflows/triggers",
			Properties: map[string]any{
				"state":             state,
				"status":            "NotSpecified",
				"provisioningState": "Succeeded",
				"createdTime":       now,
				"changedTime":       now,
				"workflow": map[string]any{
					"id": wf.ID, "name": wf.Name, "type": "Microsoft.Logic/workflows",
				},
			},
		})
	}
}

func logicResolveWorkflowForTrigger(r *http.Request) (LogicWorkflow, bool) {
	id := logicWorkflowID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "workflowName"))
	wf, ok := logicWorkflows.Get(id)
	if ok {
		logicSyncTriggers(wf)
	}
	return wf, ok
}

func handleLogicTriggerList(w http.ResponseWriter, r *http.Request) {
	wf, ok := logicResolveWorkflowForTrigger(r)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", sim.PathParam(r, "workflowName"))
		return
	}
	prefix := wf.ID + "/triggers/"
	writeLogicResourceList(w, logicTriggers, func(res LogicResource) bool {
		rest := strings.TrimPrefix(res.ID, prefix)
		return strings.HasPrefix(res.ID, prefix) && !strings.Contains(rest, "/")
	})
}

func handleLogicTriggerGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := logicResolveWorkflowForTrigger(r); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", sim.PathParam(r, "workflowName"))
		return
	}
	trigger, ok := logicTriggers.Get(r.URL.Path)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Trigger %q not found.", sim.PathParam(r, "triggerName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, trigger)
}

func handleLogicTriggerSchemaJSON(w http.ResponseWriter, r *http.Request) {
	if _, ok := logicResolveWorkflowForTrigger(r); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", sim.PathParam(r, "workflowName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"title":   sim.PathParam(r, "triggerName"),
		"content": `{"type":"object"}`,
	})
}

// handleLogicTriggerListCallbackURL signs the trigger callback with the
// parent workflow's access key, so regenerateAccessKey invalidates it exactly
// as it invalidates the workflow-level callback.
func handleLogicTriggerListCallbackURL(w http.ResponseWriter, r *http.Request) {
	scheme := azureRequestScheme(r)
	id := strings.TrimSuffix(r.URL.Path, "/listCallbackUrl")
	workflowID := logicWorkflowIDForPath(r)
	basePath := scheme + "://" + r.Host + id
	queries := logicCallbackQueries(workflowID, id+"/run")
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value":        logicCallbackURL(basePath+"/run", queries),
		"method":       "POST",
		"basePath":     basePath,
		"relativePath": "/run",
		"queries":      queries,
	})
}

// logicWorkflowIDForPath returns the ARM resource ID of the workflow that
// owns the addressed trigger (or trigger version), so every callback issued
// under a workflow is signed with that workflow's access key.
func logicWorkflowIDForPath(r *http.Request) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Logic/workflows/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "workflowName"))
}

func handleLogicTriggerReset(w http.ResponseWriter, r *http.Request) {
	if _, ok := logicResolveWorkflowForTrigger(r); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", sim.PathParam(r, "workflowName"))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleLogicTriggerSetState(w http.ResponseWriter, r *http.Request) {
	if _, ok := logicResolveWorkflowForTrigger(r); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", sim.PathParam(r, "workflowName"))
		return
	}
	var body struct {
		Source map[string]any `json:"source"`
	}
	_ = sim.ReadJSON(r, &body)
	w.WriteHeader(http.StatusOK)
}

// ---- workflow trigger histories ----

func handleLogicTriggerHistoryList(w http.ResponseWriter, r *http.Request) {
	if _, ok := logicResolveWorkflowForTrigger(r); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Workflow %q not found.", sim.PathParam(r, "workflowName"))
		return
	}
	prefix := strings.TrimSuffix(r.URL.Path, "/histories") + "/histories/"
	writeLogicResourceList(w, logicTriggerHistories, func(res LogicResource) bool { return strings.HasPrefix(res.ID, prefix) })
}

func handleLogicTriggerHistoryResubmit(w http.ResponseWriter, r *http.Request) {
	histID := strings.TrimSuffix(r.URL.Path, "/resubmit")
	if _, ok := logicTriggerHistories.Get(histID); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Trigger history %q not found.", sim.PathParam(r, "historyName"))
		return
	}
	if wf, ok := logicResolveWorkflowForTrigger(r); ok {
		logicRecordTriggerRun(wf, sim.PathParam(r, "triggerName"))
	}
	w.WriteHeader(http.StatusAccepted)
}

// ---- run actions ----

func handleLogicRunActionList(w http.ResponseWriter, r *http.Request) {
	prefix := strings.TrimSuffix(r.URL.Path, "/actions") + "/actions/"
	writeLogicResourceList(w, logicRunActions, func(res LogicResource) bool {
		rest := strings.TrimPrefix(res.ID, prefix)
		return strings.HasPrefix(res.ID, prefix) && !strings.Contains(rest, "/")
	})
}

// logicRecordTriggerRun creates a run plus its synthesized actions and a
// trigger history entry, mirroring what a single workflow execution produces.
func logicRecordTriggerRun(wf LogicWorkflow, triggerName string) string {
	logicSyncTriggers(wf)
	runName := generateUUID()
	now := logicNow()
	runID := wf.ID + "/runs/" + runName
	logicRuns.Put(runID, LogicWorkflowRun{
		ID: runID, Name: runName, Type: "Microsoft.Logic/workflows/runs",
		Properties: map[string]any{
			"startTime":     now,
			"endTime":       now,
			"waitEndTime":   now,
			"status":        "Succeeded",
			"correlationId": generateUUID(),
			"trigger": map[string]any{
				"name": triggerName, "startTime": now, "endTime": now, "status": "Succeeded",
			},
			"workflow": map[string]any{"id": wf.ID, "name": wf.Name, "type": "Microsoft.Logic/workflows"},
			"outputs":  map[string]any{},
		},
	})

	def, _ := wf.Properties["definition"].(map[string]any)
	actions, _ := def["actions"].(map[string]any)
	for actionName := range actions {
		actID := runID + "/actions/" + actionName
		logicRunActions.Put(actID, LogicResource{
			ID: actID, Name: actionName, Type: "Microsoft.Logic/workflows/runs/actions",
			Properties: map[string]any{
				"status": "Succeeded", "code": "OK", "startTime": now, "endTime": now,
			},
		})
	}

	histName := generateUUID()
	histID := wf.ID + "/triggers/" + triggerName + "/histories/" + histName
	logicTriggerHistories.Put(histID, LogicResource{
		ID: histID, Name: histName, Type: "Microsoft.Logic/workflows/triggers/histories",
		Properties: map[string]any{
			"status": "Succeeded", "code": "OK", "startTime": now, "endTime": now,
			"scheduledTime": now, "fired": true,
			"run": map[string]any{"id": runID, "name": runName, "type": "Microsoft.Logic/workflows/runs"},
		},
	})
	return runName
}

// ---- integration accounts ----

func handleLogicIntegrationAccountDelete(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path
	if !logicIntegrationAccts.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Integration account %q not found.", sim.PathParam(r, "integrationAccountName"))
		return
	}
	logicDropWorkflowKeyGens(id)
	for _, art := range logicIAArtifacts.Filter(func(res LogicResource) bool { return strings.HasPrefix(res.ID, id+"/") }) {
		logicIAArtifacts.Delete(art.ID)
	}
	w.WriteHeader(http.StatusOK)
}

func handleLogicIntegrationAccountListBySub(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	writeLogicResourceList(w, logicIntegrationAccts, func(res LogicResource) bool {
		return strings.HasPrefix(res.ID, "/subscriptions/"+sub+"/resourceGroups/") &&
			strings.Contains(res.ID, "/providers/Microsoft.Logic/integrationAccounts/")
	})
}

// handleLogicIntegrationAccountCallbackURL signs the account's callback with
// its access key, so regenerateAccessKey invalidates the outstanding URL the
// way real Logic Apps does.
func handleLogicIntegrationAccountCallbackURL(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.URL.Path, "/listCallbackUrl")
	if _, ok := logicIntegrationAccts.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Integration account %q not found.", sim.PathParam(r, "integrationAccountName"))
		return
	}
	endpoint := azureRequestScheme(r) + "://" + r.Host + id + "/callback"
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": logicCallbackURL(endpoint, logicCallbackQueries(id, id+"/callback")),
	})
}

func handleLogicIntegrationAccountKeyVaultKeys(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.URL.Path, "/listKeyVaultKeys")
	if _, ok := logicIntegrationAccts.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Integration account %q not found.", sim.PathParam(r, "integrationAccountName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
}

func handleLogicIntegrationAccountLogTrackingEvents(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.URL.Path, "/logTrackingEvents")
	if _, ok := logicIntegrationAccts.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Integration account %q not found.", sim.PathParam(r, "integrationAccountName"))
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleLogicIntegrationAccountRegenerateAccessKey rotates the account's
// primary or secondary access key and returns the account, as real Logic Apps
// does. The account's callback URL is signed with that key, so listCallbackUrl
// returns a different signature afterwards.
func handleLogicIntegrationAccountRegenerateAccessKey(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.URL.Path, "/regenerateAccessKey")
	acct, ok := logicIntegrationAccts.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Integration account %q not found.", sim.PathParam(r, "integrationAccountName"))
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
	azureBumpKeyGen(id, slot, "")
	sim.WriteJSON(w, http.StatusOK, acct)
}

// ---- integration account artifacts ----

func logicRegisterArtifact(srv *sim.Server, seg, typ string, timestamps bool) {
	switch seg {
	case "maps":
		srv.HandleFunc("PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/maps/{mapName}", handleLogicResourcePut(logicIAArtifacts, typ, http.StatusOK, timestamps))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/maps/{mapName}", handleLogicResourceGet(logicIAArtifacts, "map"))
		srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/maps/{mapName}", handleLogicResourceDelete(logicIAArtifacts))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/maps", handleLogicResourceList(logicIAArtifacts))
		srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/maps/{mapName}/listContentCallbackUrl", handleLogicArtifactContentCallbackURL)
	case "schemas":
		srv.HandleFunc("PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/schemas/{schemaName}", handleLogicResourcePut(logicIAArtifacts, typ, http.StatusOK, timestamps))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/schemas/{schemaName}", handleLogicResourceGet(logicIAArtifacts, "schema"))
		srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/schemas/{schemaName}", handleLogicResourceDelete(logicIAArtifacts))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/schemas", handleLogicResourceList(logicIAArtifacts))
		srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/schemas/{schemaName}/listContentCallbackUrl", handleLogicArtifactContentCallbackURL)
	case "partners":
		srv.HandleFunc("PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/partners/{partnerName}", handleLogicResourcePut(logicIAArtifacts, typ, http.StatusOK, timestamps))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/partners/{partnerName}", handleLogicResourceGet(logicIAArtifacts, "partner"))
		srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/partners/{partnerName}", handleLogicResourceDelete(logicIAArtifacts))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/partners", handleLogicResourceList(logicIAArtifacts))
		srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/partners/{partnerName}/listContentCallbackUrl", handleLogicArtifactContentCallbackURL)
	case "agreements":
		srv.HandleFunc("PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/agreements/{agreementName}", handleLogicResourcePut(logicIAArtifacts, typ, http.StatusOK, timestamps))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/agreements/{agreementName}", handleLogicResourceGet(logicIAArtifacts, "agreement"))
		srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/agreements/{agreementName}", handleLogicResourceDelete(logicIAArtifacts))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/agreements", handleLogicResourceList(logicIAArtifacts))
		srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/agreements/{agreementName}/listContentCallbackUrl", handleLogicArtifactContentCallbackURL)
	case "assemblies":
		srv.HandleFunc("PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/assemblies/{assemblyArtifactName}", handleLogicResourcePut(logicIAArtifacts, typ, http.StatusOK, timestamps))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/assemblies/{assemblyArtifactName}", handleLogicResourceGet(logicIAArtifacts, "assembly"))
		srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/assemblies/{assemblyArtifactName}", handleLogicResourceDelete(logicIAArtifacts))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/assemblies", handleLogicResourceList(logicIAArtifacts))
		srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/assemblies/{assemblyArtifactName}/listContentCallbackUrl", handleLogicArtifactContentCallbackURL)
	case "batchConfigurations":
		srv.HandleFunc("PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/batchConfigurations/{batchConfigurationName}", handleLogicResourcePut(logicIAArtifacts, typ, http.StatusOK, timestamps))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/batchConfigurations/{batchConfigurationName}", handleLogicResourceGet(logicIAArtifacts, "batch configuration"))
		srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/batchConfigurations/{batchConfigurationName}", handleLogicResourceDelete(logicIAArtifacts))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/batchConfigurations", handleLogicResourceList(logicIAArtifacts))
	case "sessions":
		srv.HandleFunc("PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/sessions/{sessionName}", handleLogicResourcePut(logicIAArtifacts, typ, http.StatusOK, timestamps))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/sessions/{sessionName}", handleLogicResourceGet(logicIAArtifacts, "session"))
		srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/sessions/{sessionName}", handleLogicResourceDelete(logicIAArtifacts))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/sessions", handleLogicResourceList(logicIAArtifacts))
	case "certificates":
		srv.HandleFunc("PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/certificates/{certificateName}", handleLogicResourcePut(logicIAArtifacts, typ, http.StatusOK, timestamps))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/certificates/{certificateName}", handleLogicResourceGet(logicIAArtifacts, "certificate"))
		srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/certificates/{certificateName}", handleLogicResourceDelete(logicIAArtifacts))
		srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Logic/integrationAccounts/{integrationAccountName}/certificates", handleLogicResourceList(logicIAArtifacts))
	}
}

func handleLogicArtifactContentCallbackURL(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.URL.Path, "/listContentCallbackUrl")
	if _, ok := logicIAArtifacts.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The artifact %q was not found.", logicLastSeg(id))
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value":  azureRequestScheme(r) + "://" + r.Host + id + "/content",
		"method": "GET",
	})
}

// ---- integration service environments (LRO) ----

func handleLogicServiceEnvPut(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path
	var req LogicResource
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	props := map[string]any{}
	for k, v := range req.Properties {
		props[k] = v
	}
	props["provisioningState"] = "Creating"
	if _, ok := props["state"]; !ok {
		props["state"] = "Enabled"
	}
	logicServiceEnvs.Put(id, LogicResource{
		ID: id, Name: logicLastSeg(id), Type: "Microsoft.Logic/integrationServiceEnvironments",
		Location: req.Location, Tags: req.Tags, Identity: req.Identity, Sku: req.Sku, Properties: props,
	})
	opID := issueAzureAsyncOperation(func() {
		logicServiceEnvs.Update(id, func(res *LogicResource) {
			if res.Properties == nil {
				res.Properties = map[string]any{}
			}
			res.Properties["provisioningState"] = "Succeeded"
		})
	})
	opURL := azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"), "Microsoft.Logic", logicResLocation(req.Location), "operationStatuses", opID, r.URL.Query().Get("api-version"))
	writeAzureAsyncCreateHeaders(w, opURL, azureCurrentRequestURL(r))
	res, _ := logicServiceEnvs.Get(id)
	sim.WriteJSON(w, http.StatusCreated, res)
}

func handleLogicServiceEnvPatch(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path
	res, ok := logicServiceEnvs.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Integration service environment %q not found.", sim.PathParam(r, "integrationServiceEnvironmentName"))
		return
	}
	var req LogicResource
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	logicServiceEnvs.Update(id, func(stored *LogicResource) {
		if req.Tags != nil {
			stored.Tags = req.Tags
		}
		if req.Sku != nil {
			stored.Sku = req.Sku
		}
		if req.Properties != nil {
			if stored.Properties == nil {
				stored.Properties = map[string]any{}
			}
			for k, v := range req.Properties {
				stored.Properties[k] = v
			}
		}
	})
	opID := issueAzureAsyncOperation(nil)
	opURL := azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"), "Microsoft.Logic", logicResLocation(res.Location), "operationStatuses", opID, r.URL.Query().Get("api-version"))
	writeAzureAsyncCreateHeaders(w, opURL, azureCurrentRequestURL(r))
	updated, _ := logicServiceEnvs.Get(id)
	sim.WriteJSON(w, http.StatusOK, updated)
}

func handleLogicServiceEnvDelete(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path
	if !logicServiceEnvs.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Integration service environment %q not found.", sim.PathParam(r, "integrationServiceEnvironmentName"))
		return
	}
	for _, api := range logicSEManagedApis.Filter(func(m LogicResource) bool { return strings.HasPrefix(m.ID, id+"/") }) {
		logicSEManagedApis.Delete(api.ID)
	}
	w.WriteHeader(http.StatusOK)
}

func handleLogicServiceEnvListBySub(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	writeLogicResourceList(w, logicServiceEnvs, func(res LogicResource) bool {
		return strings.HasPrefix(res.ID, "/subscriptions/"+sub+"/resourceGroups/") &&
			strings.Contains(res.ID, "/providers/Microsoft.Logic/integrationServiceEnvironments/")
	})
}

func handleLogicServiceEnvRestart(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.URL.Path, "/restart")
	if _, ok := logicServiceEnvs.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Integration service environment %q not found.", sim.PathParam(r, "integrationServiceEnvironmentName"))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleLogicServiceEnvNetworkHealth(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.URL.Path, "/health/network")
	if _, ok := logicServiceEnvs.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Integration service environment %q not found.", sim.PathParam(r, "integrationServiceEnvironmentName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleLogicServiceEnvManagedApiPut(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path
	var req LogicResource
	_ = sim.ReadJSON(r, &req)
	props := map[string]any{}
	for k, v := range req.Properties {
		props[k] = v
	}
	props["provisioningState"] = "Succeeded"
	logicSEManagedApis.Put(id, LogicResource{
		ID: id, Name: logicLastSeg(id), Type: "Microsoft.Logic/integrationServiceEnvironments/managedApis",
		Location: req.Location, Properties: props,
	})
	opID := issueAzureAsyncOperation(nil)
	opURL := azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"), "Microsoft.Logic", logicResLocation(req.Location), "operationStatuses", opID, r.URL.Query().Get("api-version"))
	writeAzureAsyncCreateHeaders(w, opURL, azureCurrentRequestURL(r))
	res, _ := logicSEManagedApis.Get(id)
	sim.WriteJSON(w, http.StatusCreated, res)
}

func handleLogicServiceEnvManagedApiDelete(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path
	res, ok := logicSEManagedApis.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Managed API %q not found.", sim.PathParam(r, "apiName"))
		return
	}
	opID := issueAzureAsyncOperation(func() { logicSEManagedApis.Delete(id) })
	opURL := azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"), "Microsoft.Logic", logicResLocation(res.Location), "operationStatuses", opID, r.URL.Query().Get("api-version"))
	writeAzureAsyncCreateHeaders(w, opURL, azureCurrentRequestURL(r))
	w.WriteHeader(http.StatusAccepted)
}

func logicResLocation(loc string) string {
	if loc != "" {
		return loc
	}
	return "global"
}
