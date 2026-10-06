package main

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// logMu protects the read-modify-write cycle on monitorLogs entries.
// StateStore is individually thread-safe, but Get+append+Put is not atomic.
// logMu guards the Azure Monitor log rows. Reading a site's retained container
// output excludes nothing but a writer; ingesting a row keeps taking Lock.
var logMu sync.RWMutex
var azureMonitorWorkspaces sim.Store[Workspace]

// A workspace is stored under its ARM id while the query data plane addresses
// it by the customer id it was issued, so the one reaches the other through an
// index rather than a walk of every workspace.
var azureWorkspacesByCustomerID sim.GenerationIndex[Workspace]

func azureWorkspaceCustomerIDKeys(ws Workspace) []string {
	if ws.Properties.CustomerID == "" {
		return nil
	}
	return []string{strings.ToLower(ws.Properties.CustomerID)}
}

// Workspace represents an Azure Log Analytics Workspace.
type Workspace struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Type       string              `json:"type"`
	Location   string              `json:"location"`
	Tags       map[string]string   `json:"tags,omitempty"`
	Properties WorkspaceProperties `json:"properties"`
}

// WorkspaceProperties holds the properties of a Log Analytics Workspace.
type WorkspaceProperties struct {
	CustomerID                      string             `json:"customerId"`
	ProvisioningState               string             `json:"provisioningState"`
	RetentionInDays                 int                `json:"retentionInDays,omitempty"`
	Sku                             *WorkspaceSku      `json:"sku,omitempty"`
	Features                        *WorkspaceFeatures `json:"features,omitempty"`
	PublicNetworkAccessForIngestion string             `json:"publicNetworkAccessForIngestion,omitempty"`
	PublicNetworkAccessForQuery     string             `json:"publicNetworkAccessForQuery,omitempty"`
}

// WorkspaceSku holds the SKU of a Log Analytics Workspace.
type WorkspaceSku struct {
	Name string `json:"name"`
}

// WorkspaceFeatures holds workspace feature flags.
// The azurerm provider (go-azure-sdk) dereferences this struct — it must not be nil.
type WorkspaceFeatures struct {
	EnableLogAccessUsingOnlyResourcePermissions *bool `json:"enableLogAccessUsingOnlyResourcePermissions,omitempty"`
	DisableLocalAuth                            *bool `json:"disableLocalAuth,omitempty"`
	EnableDataExport                            *bool `json:"enableDataExport,omitempty"`
	ImmediatePurgeDataOn30Days                  *bool `json:"immediatePurgeDataOn30Days,omitempty"`
}

// QueryRequest holds a KQL query request body.
type QueryRequest struct {
	Query    string `json:"query"`
	Timespan string `json:"timespan,omitempty"`
}

type QueryResponse struct {
	Tables []Table `json:"tables"`
}

// Table holds a single result table from a KQL query.
type Table struct {
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// Column holds a column definition in a query result table.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// monitorLogs stores a table's rows under logRowsKey (a workspace's) or
// appLogRowsKey (a classic Application Insights component's).
var monitorLogs sim.Store[[]monitorLogRow]

// webSiteDockerLogs holds each site's retained container output, the docker
// log App Service keeps for the site whatever its monitoring settings.
var webSiteDockerLogs sim.Store[[]monitorLogRow]

// monitorMaxRetainedRows bounds the rows retained per table/log. Log Analytics
// ages out rows past the workspace retention policy; the sim caps the in-memory
// window so a long-running workload cannot grow a table without bound. Reads are
// insertion-order, filtered then limited from the front, so dropping the oldest
// rows is faithful to retention. The cap sits well above any single query or the
// dashboard's 100-row flatten.
const monitorMaxRetainedRows = 50000

// appendLogRow safely appends a log row to the given store key,
// protecting the read-modify-write cycle with logMu.
func appendLogRow(storeKey string, row monitorLogRow) {
	appendRetainedRow(monitorLogs, storeKey, row)
}

func appendRetainedRow(store sim.Store[[]monitorLogRow], key string, row monitorLogRow) {
	logMu.Lock()
	defer logMu.Unlock()
	existing, _ := store.Get(key)
	existing = append(existing, row)
	if over := len(existing) - monitorMaxRetainedRows; over > 0 {
		existing = existing[over:]
	}
	store.Put(key, existing)
}

// appendSiteDockerLog keeps one line of a site's container output and hands
// it to the site's open log streams.
func appendSiteDockerLog(siteID, at, message string) {
	appendRetainedRow(webSiteDockerLogs, strings.ToLower(siteID), monitorLogRow{"TimeGenerated": at, "Message": message})
	kuduPublishLogLine(siteID, at+" "+message)
}

func registerAzureMonitor(srv *sim.Server) {
	monitorLogs = sim.MakeStore[[]monitorLogRow](srv.DB(), "monitor_logs")
	webSiteDockerLogs = sim.MakeStore[[]monitorLogRow](srv.DB(), "web_site_docker_logs")
	workspaces := sim.MakeStore[Workspace](srv.DB(), "monitor_workspaces")
	monitorWorkspaces = workspaces
	azureMonitorWorkspaces = workspaces

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.OperationalInsights"
	registerMonitorSharedKeys(srv, armBase)
	registerLogAnalyticsTables(srv, armBase)
	registerDataCollectionEndpoints(srv)
	registerDataCollectionRules(srv)

	// Subscription-scoped list of soft-deleted workspaces. Real Azure
	// keeps deleted workspaces recoverable for 14 days; terraform-
	// provider-azurerm queries this pre-create to find a recoverable
	// match. The sim hard-deletes workspaces (no soft-delete state
	// tracked), so the truthful response is always an empty list.
	deletedWorkspacesHandler := func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
	}
	srv.HandleFunc(
		"GET /subscriptions/{subscriptionId}/providers/Microsoft.OperationalInsights/deletedWorkspaces",
		deletedWorkspacesHandler,
	)
	srv.HandleFunc(
		"GET /subscriptions/{subscriptionId}/resourcegroups/{resourceGroupName}/providers/Microsoft.OperationalInsights/deletedWorkspaces",
		deletedWorkspacesHandler,
	)

	// PUT - Create or update workspace
	srv.HandleFunc("PUT "+armBase+"/workspaces/{workspaceName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "workspaceName")

		var req Workspace
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.Location == "" {
			AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.OperationalInsights/workspaces/%s", sub, rg, name)
		customerID := sim.NewUUID()

		boolTrue := true
		boolFalse := false
		ws := Workspace{
			ID:       resourceID,
			Name:     name,
			Type:     "Microsoft.OperationalInsights/workspaces",
			Location: req.Location,
			Tags:     req.Tags,
			Properties: WorkspaceProperties{
				CustomerID:                      customerID,
				ProvisioningState:               "Succeeded",
				RetentionInDays:                 30,
				Sku:                             &WorkspaceSku{Name: "PerGB2018"},
				PublicNetworkAccessForIngestion: "Enabled",
				PublicNetworkAccessForQuery:     "Enabled",
				Features: &WorkspaceFeatures{
					EnableLogAccessUsingOnlyResourcePermissions: &boolTrue,
					DisableLocalAuth:           &boolFalse,
					EnableDataExport:           &boolFalse,
					ImmediatePurgeDataOn30Days: &boolFalse,
				},
			},
		}

		if req.Properties.RetentionInDays > 0 {
			ws.Properties.RetentionInDays = req.Properties.RetentionInDays
		}
		if req.Properties.PublicNetworkAccessForIngestion != "" {
			ws.Properties.PublicNetworkAccessForIngestion = req.Properties.PublicNetworkAccessForIngestion
		}
		if req.Properties.PublicNetworkAccessForQuery != "" {
			ws.Properties.PublicNetworkAccessForQuery = req.Properties.PublicNetworkAccessForQuery
		}

		workspaces.Put(resourceID, ws)

		// go-azure-sdk expects 200 for sync creates
		sim.WriteJSON(w, http.StatusOK, ws)
	})

	// PATCH - Update workspace (azurerm v3 provider sends PATCH after initial create)
	srv.HandleFunc("PATCH "+armBase+"/workspaces/{workspaceName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "workspaceName")

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.OperationalInsights/workspaces/%s", sub, rg, name)

		ws, ok := workspaces.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.OperationalInsights/workspaces/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		// Apply partial update from request body
		var patch Workspace
		if err := sim.ReadJSON(r, &patch); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if patch.Tags != nil {
			ws.Tags = patch.Tags
		}
		if patch.Properties.RetentionInDays > 0 {
			ws.Properties.RetentionInDays = patch.Properties.RetentionInDays
		}
		if patch.Properties.PublicNetworkAccessForIngestion != "" {
			ws.Properties.PublicNetworkAccessForIngestion = patch.Properties.PublicNetworkAccessForIngestion
		}
		if patch.Properties.PublicNetworkAccessForQuery != "" {
			ws.Properties.PublicNetworkAccessForQuery = patch.Properties.PublicNetworkAccessForQuery
		}
		ws.Properties.ProvisioningState = "Succeeded"
		workspaces.Put(resourceID, ws)

		sim.WriteJSON(w, http.StatusOK, ws)
	})

	// GET - Get workspace
	srv.HandleFunc("GET "+armBase+"/workspaces/{workspaceName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "workspaceName")

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.OperationalInsights/workspaces/%s", sub, rg, name)

		ws, ok := workspaces.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.OperationalInsights/workspaces/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		sim.WriteJSON(w, http.StatusOK, ws)
	})

	// POST - Get shared keys (azurerm provider reads these when linking workspace to Container App Environment)
	srv.HandleFunc("POST "+armBase+"/workspaces/{workspaceName}/sharedKeys", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "workspaceName")

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.OperationalInsights/workspaces/%s", sub, rg, name)

		if _, ok := workspaces.Get(resourceID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.OperationalInsights/workspaces/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		// The keys are the workspace's own pair, minted on first use and
		// replaced only by a regeneration — a constant here would make a
		// regeneration unobservable and an agent's signature meaningless.
		keys := monitorKeysFor(resourceID)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"primarySharedKey":   keys.Primary,
			"secondarySharedKey": keys.Secondary,
		})
	})

	// DELETE - Delete workspace
	srv.HandleFunc("DELETE "+armBase+"/workspaces/{workspaceName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "workspaceName")

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.OperationalInsights/workspaces/%s", sub, rg, name)

		if ws, ok := workspaces.Get(resourceID); ok {
			dropWorkspaceLogs(ws)
		}
		if workspaces.Delete(resourceID) {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// GET - Subscription-scoped workspace list. terraform-provider-azurerm
	// resolves a Container App Environment's log_analytics_workspace_id by
	// listing every workspace in the subscription and matching customerId
	// (findWorkspaceResourceIDFromCustomerID); without this list the read
	// can't recover the workspace id and plans to re-add it every refresh.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.OperationalInsights/workspaces", func(w http.ResponseWriter, r *http.Request) {
		prefix := fmt.Sprintf("/subscriptions/%s/", sim.PathParam(r, "subscriptionId"))
		items := workspaces.Filter(func(ws Workspace) bool { return strings.HasPrefix(ws.ID, prefix) })
		if items == nil {
			items = []Workspace{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": items})
	})

	// GET - Resource-group-scoped workspace list (ListByResourceGroup).
	srv.HandleFunc("GET "+armBase+"/workspaces", func(w http.ResponseWriter, r *http.Request) {
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.OperationalInsights/workspaces/",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"))
		items := workspaces.Filter(func(ws Workspace) bool { return strings.HasPrefix(ws.ID, prefix) })
		if items == nil {
			items = []Workspace{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": items})
	})

	// Execute KQL query (Log Analytics data-plane, https://api.loganalytics.io/v1).
	// POST carries the query in the body; GET carries it as the `query` query
	// parameter. Both return the QueryResults tabular shape.
	postQueryHandler := func(w http.ResponseWriter, r *http.Request) {
		scope, ok := queryLogScope(w, r)
		if !ok {
			return
		}
		var req QueryRequest
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "BadArgumentError", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Query == "" {
			AzureError(w, "BadArgumentError", "The 'query' property is required.", http.StatusBadRequest)
			return
		}
		writeKQLResult(w, scope, req.Query, req.Timespan)
	}
	getQueryHandler := func(w http.ResponseWriter, r *http.Request) {
		scope, ok := queryLogScope(w, r)
		if !ok {
			return
		}
		query := r.URL.Query().Get("query")
		if query == "" {
			AzureError(w, "BadArgumentError", "The 'query' parameter is required.", http.StatusBadRequest)
			return
		}
		writeKQLResult(w, scope, query, r.URL.Query().Get("timespan"))
	}
	srv.HandleFunc("POST /v1/workspaces/{workspaceId}/query", postQueryHandler)
	srv.HandleFunc("GET /v1/workspaces/{workspaceId}/query", getQueryHandler)

	// Query_ExecuteWithResourceId and Query_GetWithResourceId — the same query,
	// addressed by the Azure resource whose logs are being read rather than by
	// the workspace they land in. A resource id is a whole ARM path of no fixed
	// depth, which Go's router cannot spell and which enumerating would turn
	// into a handful of invented path shapes. So it is intercepted, exactly as
	// Microsoft.Authorization's any-scope routes are.
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resourceID, ok := logAnalyticsResourceQueryPath(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			r.SetPathValue("resourceId", resourceID)
			switch r.Method {
			case http.MethodPost:
				postQueryHandler(w, r)
			case http.MethodGet:
				getQueryHandler(w, r)
			default:
				next.ServeHTTP(w, r)
			}
		})
	})

	// Workspace schema metadata (tables and their columns) — the data-plane
	// metadata API. GET and POST return the same MetadataResults shape.
	metadataHandler := func(w http.ResponseWriter, r *http.Request) {
		ws, ok := logWorkspaceByCustomerID(sim.PathParam(r, "workspaceId"))
		if !ok {
			writeWorkspaceNotFound(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, logAnalyticsMetadata(ws))
	}
	srv.HandleFunc("GET /v1/workspaces/{workspaceId}/metadata", metadataHandler)
	srv.HandleFunc("POST /v1/workspaces/{workspaceId}/metadata", metadataHandler)

	// Batch of queries — each request runs against its workspace; responses
	// come back keyed by the request id, in request order.
	srv.HandleFunc("POST /v1/$batch", func(w http.ResponseWriter, r *http.Request) {
		var batch struct {
			Requests []struct {
				ID        string       `json:"id"`
				Body      QueryRequest `json:"body"`
				Workspace string       `json:"workspace"`
			} `json:"requests"`
		}
		if err := sim.ReadJSON(r, &batch); err != nil {
			AzureError(w, "BadArgumentError", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		responses := make([]map[string]any, 0, len(batch.Requests))
		for _, req := range batch.Requests {
			scope, found := workspaceLogScope(req.Workspace)
			if !found {
				responses = append(responses, map[string]any{
					"id": req.ID, "status": http.StatusNotFound, "body": workspaceNotFoundBody(),
				})
				continue
			}
			result, qerr := runKQLQuery(scope, req.Body.Query, req.Body.Timespan)
			if qerr != nil {
				responses = append(responses, map[string]any{
					"id": req.ID, "status": http.StatusBadRequest, "body": qerr.body(),
				})
				continue
			}
			responses = append(responses, map[string]any{
				"id": req.ID, "status": http.StatusOK, "body": result,
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"responses": responses})
	})
}

// logAnalyticsMetadata builds the MetadataResults schema document for a
// workspace from the tables it holds — the data-plane metadata API's
// tables/columns view.
func logAnalyticsMetadata(ws Workspace) map[string]any {
	held := logAnalyticsWorkspaceTables(ws)
	tables := make([]map[string]any, 0, len(held))
	for _, t := range held {
		schema := logAnalyticsTableSchema(t)
		cols := make([]map[string]any, 0, len(schema))
		for _, c := range schema {
			cols = append(cols, map[string]any{"name": c.Name, "type": c.Type})
		}
		tables = append(tables, map[string]any{
			"id":             t.Name,
			"name":           t.Name,
			"timespanColumn": "TimeGenerated",
			"columns":        cols,
		})
	}
	return map[string]any{
		"tables": tables,
		"workspaces": []map[string]any{{
			"id":         ws.Properties.CustomerID,
			"name":       ws.Name,
			"region":     ws.Location,
			"resourceId": ws.ID,
		}},
	}
}

// queryLogScope resolves what a query request addresses: a workspace by its
// customer id, or a resource by its ARM id.
func queryLogScope(w http.ResponseWriter, r *http.Request) (logScope, bool) {
	if resourceID := r.PathValue("resourceId"); resourceID != "" {
		return resourceLogScope(resourceID), true
	}
	scope, ok := workspaceLogScope(sim.PathParam(r, "workspaceId"))
	if !ok {
		writeWorkspaceNotFound(w)
	}
	return scope, ok
}

func workspaceNotFoundBody() map[string]any {
	return map[string]any{"error": map[string]any{
		"code":    "WorkspaceNotFoundError",
		"message": "The workspace could not be found",
	}}
}

// writeWorkspaceNotFound answers a query addressed to a workspace id no
// workspace was issued.
func writeWorkspaceNotFound(w http.ResponseWriter) {
	sim.WriteJSON(w, http.StatusNotFound, workspaceNotFoundBody())
}

// logAnalyticsResourceQueryPath reports whether a request addresses the
// resource-centric query, and the resource it names.
//
// The two spellings that have their own routes — the workspace query and the
// Application Insights app query — are left to them: a resource id is any ARM
// path, so without excluding those this would swallow both.
func logAnalyticsResourceQueryPath(r *http.Request) (string, bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		return "", false
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/v1/")
	if !ok {
		return "", false
	}
	resourceID, ok := strings.CutSuffix(rest, "/query")
	if !ok || resourceID == "" {
		return "", false
	}
	// A resource id is an ARM path, so it begins at a scope. Anything else
	// under /v1 belongs to whichever data plane owns it.
	if !strings.HasPrefix(strings.ToLower(resourceID), "subscriptions/") {
		return "", false
	}
	return resourceID, true
}
