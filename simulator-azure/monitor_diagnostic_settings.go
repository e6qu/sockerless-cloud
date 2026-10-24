package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Azure Monitor diagnostic settings (Microsoft.Insights/diagnosticSettings,
// 2021-05-01-preview) route a resource's platform logs to a Log Analytics
// workspace, category by category: a setting on an App Service site that
// enables AppServiceConsoleLogs sends the site's console lines to the
// AppServiceConsoleLogs table of the workspace it names, and a setting on a
// Container Apps environment whose logs destination is azure-monitor sends
// ContainerAppConsoleLogs and ContainerAppSystemLogs to the resource-specific
// tables of its workspace. The categories a resource type offers
// (diagnosticSettingsCategories, 2017-05-01-preview) come from the Azure
// Monitor "supported resource logs" reference pages vendored under
// specs/cloud-api/azure (monitor-supported-logs-*.md.gz), and the tables'
// columns from the table reference pages (monitor-table-*.md.gz). Storage,
// Event Hubs and Marketplace destinations are kept and receive nothing.

type DiagnosticSettingsResource struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Type       string             `json:"type"`
	Properties DiagnosticSettings `json:"properties"`
	SystemData *SystemData        `json:"systemData,omitempty"`
}

type DiagnosticSettings struct {
	StorageAccountID            string                     `json:"storageAccountId,omitempty"`
	ServiceBusRuleID            string                     `json:"serviceBusRuleId,omitempty"`
	EventHubAuthorizationRuleID string                     `json:"eventHubAuthorizationRuleId,omitempty"`
	EventHubName                string                     `json:"eventHubName,omitempty"`
	Metrics                     []DiagnosticMetricSettings `json:"metrics"`
	Logs                        []DiagnosticLogSettings    `json:"logs"`
	WorkspaceID                 string                     `json:"workspaceId,omitempty"`
	MarketplacePartnerID        string                     `json:"marketplacePartnerId,omitempty"`
	LogAnalyticsDestinationType string                     `json:"logAnalyticsDestinationType,omitempty"`
}

type DiagnosticLogSettings struct {
	Category        string                     `json:"category,omitempty"`
	CategoryGroup   string                     `json:"categoryGroup,omitempty"`
	Enabled         bool                       `json:"enabled"`
	RetentionPolicy *DiagnosticRetentionPolicy `json:"retentionPolicy,omitempty"`
}

type DiagnosticMetricSettings struct {
	TimeGrain       string                     `json:"timeGrain,omitempty"`
	Category        string                     `json:"category,omitempty"`
	Enabled         bool                       `json:"enabled"`
	RetentionPolicy *DiagnosticRetentionPolicy `json:"retentionPolicy,omitempty"`
}

type DiagnosticRetentionPolicy struct {
	Enabled bool  `json:"enabled"`
	Days    int32 `json:"days"`
}

type DiagnosticSettingsCategoryResource struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Properties struct {
		CategoryType string `json:"categoryType"`
	} `json:"properties"`
}

// diagnosticCategories are the log and metric categories a resource type
// offers, in the order the reference page lists them.
type diagnosticCategories struct {
	logs, metrics []string
	// exists reports whether the resource the diagnostic setting addresses
	// is stored.
	exists func(resourceID string) bool
}

// azureDiagnosticCategoryGroups are the category groups a setting's logs may
// name instead of a category ("For categoryGroup, the available values are
// audit and allLogs", app-containerapps-log-options.md.gz): allLogs stands for
// every log category, audit for the audit ones.
var azureDiagnosticCategoryGroups = []string{"allLogs", "audit"}

// azureDiagnosticResourceTypes are the resource types whose categories the
// simulator carries, keyed by lower-cased type, with the type's canonical
// spelling. The sites' categories are the microsoft.web/sites rows of the
// Azure Monitor supported-logs reference, the environments' the
// microsoft.app/managedenvironments rows; a container app offers the
// AllMetrics metric category and no log category ("Log options in Azure
// Container Apps": at the container app level the --logs parameter is not
// supported, and for --metrics the only available category is AllMetrics).
var azureDiagnosticResourceTypes = map[string]string{
	"microsoft.web/sites":               "Microsoft.Web/sites",
	"microsoft.app/managedenvironments": "Microsoft.App/managedEnvironments",
	"microsoft.app/containerapps":       "Microsoft.App/containerApps",
}

func azureDiagnosticCategoriesOf(resourceType string) (diagnosticCategories, bool) {
	switch strings.ToLower(resourceType) {
	case "microsoft.web/sites":
		return diagnosticCategories{
			logs: []string{"AppServiceAntivirusScanAuditLogs", "AppServiceAppLogs", "AppServiceAuditLogs", "AppServiceAuthenticationLogs",
				"AppServiceConsoleLogs", "AppServiceFileAuditLogs", "AppServiceHTTPLogs", "AppServiceIPSecAuditLogs",
				"AppServicePlatformLogs", "FunctionAppLogs", "WorkflowRuntime"},
			exists: func(id string) bool { _, ok := azfSites.Get(id); return ok },
		}, true
	case "microsoft.app/managedenvironments":
		return diagnosticCategories{
			logs: []string{"AppEnvSessionConsoleLogs", "AppEnvSessionLifeCycleLogs", "AppEnvSessionPoolEventLogs",
				"AppEnvSpringAppConsoleLogs", "ContainerAppConsoleLogs", "ContainerAppHTTPLogs", "ContainerAppSystemLogs"},
			exists: func(id string) bool { _, ok := acaEnvironments.Get(id); return ok },
		}, true
	case "microsoft.app/containerapps":
		return diagnosticCategories{
			metrics: []string{"AllMetrics"},
			exists:  func(id string) bool { _, ok := acaApps.Get(id); return ok },
		}, true
	}
	return diagnosticCategories{}, false
}

var (
	azureDiagnosticSettings           sim.Store[DiagnosticSettingsResource]
	azureDiagnosticSettingsByResource sim.GenerationIndex[DiagnosticSettingsResource]
)

const diagnosticSettingsMarker = "/providers/Microsoft.Insights/diagnosticSettings/"

// azureDiagnosticSettingResource is the lower-cased resource a setting
// addresses, which indexes the settings by resource.
func azureDiagnosticSettingResource(d DiagnosticSettingsResource) []string {
	lower := strings.ToLower(d.ID)
	marker := strings.ToLower(diagnosticSettingsMarker)
	if i := strings.LastIndex(lower, marker); i >= 0 {
		return []string{lower[:i]}
	}
	return nil
}

// azureDiagnosticSettingsFor lists the settings on a resource.
func azureDiagnosticSettingsFor(resourceID string) []DiagnosticSettingsResource {
	if azureDiagnosticSettings == nil {
		return nil
	}
	settings := azureDiagnosticSettingsByResource.LookupAll(azureDiagnosticSettings, strings.ToLower(resourceID), azureDiagnosticSettingResource)
	sort.Slice(settings, func(i, j int) bool { return settings[i].Name < settings[j].Name })
	return settings
}

// enablesLog reports whether the setting sends the log category: a log entry
// naming it, or the allLogs group, enabled.
func (d DiagnosticSettings) enablesLog(category string) bool {
	for _, l := range d.Logs {
		if l.Enabled && (strings.EqualFold(l.Category, category) || strings.EqualFold(l.CategoryGroup, "allLogs")) {
			return true
		}
	}
	return false
}

// azureDiagnosticLogWorkspaces are the workspaces the diagnostic settings on
// the resource send the log category to.
func azureDiagnosticLogWorkspaces(resourceID, category string) []Workspace {
	var out []Workspace
	for _, setting := range azureDiagnosticSettingsFor(resourceID) {
		if setting.Properties.WorkspaceID == "" || !setting.Properties.enablesLog(category) {
			continue
		}
		if ws, ok := logAnalyticsWorkspaceByID(setting.Properties.WorkspaceID); ok {
			out = append(out, ws)
		}
	}
	return out
}

// diagnosticTarget resolves the resource a diagnostic settings request
// addresses: its id, its type in canonical spelling, and the categories the
// simulator carries for the type.
type diagnosticTarget func(r *http.Request) (resourceID, resourceType string, categories diagnosticCategories, known bool)

func registerDiagnosticSettings(srv *sim.Server) {
	azureDiagnosticSettings = sim.MakeStore[DiagnosticSettingsResource](srv.DB(), "monitor_diagnostic_settings")
	// A diagnostic setting addresses the resource it is nested under. The
	// typed routes cover the resources of a resource group — the shape every
	// resource the simulator routes logs for has — and the subscription and
	// resource-group scopes a client can address, for which the simulator
	// carries no categories.
	const subscription = "/subscriptions/{subscriptionId}"
	const resourceGroup = subscription + "/resourceGroups/{resourceGroupName}"
	// The resource types whose categories the simulator carries have routes
	// of their own, so a request to one of their resources resolves to a
	// handler naming the type; every other type resolves to the wildcard.
	types := make([]string, 0, len(azureDiagnosticResourceTypes))
	for _, canonical := range azureDiagnosticResourceTypes {
		types = append(types, canonical)
	}
	sort.Strings(types)
	for _, resourceType := range types {
		registerDiagnosticSettingRoutes(srv, resourceGroup+"/providers/"+resourceType+"/{resourceName}", func(r *http.Request) (string, string, diagnosticCategories, bool) {
			resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/%s/%s",
				sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), resourceType, sim.PathParam(r, "resourceName"))
			categories, known := azureDiagnosticCategoriesOf(resourceType)
			return resourceID, resourceType, categories, known
		})
	}
	registerDiagnosticSettingRoutes(srv, resourceGroup+"/providers/{resourceProviderNamespace}/{resourceType}/{resourceName}", func(r *http.Request) (string, string, diagnosticCategories, bool) {
		ns, typ, name := sim.PathParam(r, "resourceProviderNamespace"), sim.PathParam(r, "resourceType"), sim.PathParam(r, "resourceName")
		resourceType := ns + "/" + typ
		if canonical, ok := azureDiagnosticResourceTypes[strings.ToLower(resourceType)]; ok {
			resourceType = canonical
		}
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/%s/%s",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), resourceType, name)
		categories, known := azureDiagnosticCategoriesOf(resourceType)
		return resourceID, resourceType, categories, known
	})
	registerDiagnosticSettingRoutes(srv, resourceGroup, func(r *http.Request) (string, string, diagnosticCategories, bool) {
		return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName")),
			"Microsoft.Resources/resourceGroups", diagnosticCategories{}, false
	})
	registerDiagnosticSettingRoutes(srv, subscription, func(r *http.Request) (string, string, diagnosticCategories, bool) {
		return "/subscriptions/" + sim.PathParam(r, "subscriptionId"), "Microsoft.Resources/subscriptions", diagnosticCategories{}, false
	})
}

func registerDiagnosticSettingRoutes(srv *sim.Server, scope string, target diagnosticTarget) {
	base := scope + "/providers/Microsoft.Insights"
	notFound := func(w http.ResponseWriter, resourceID string) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource '%s' was not found.", resourceID)
	}
	settingID := func(resourceID, name string) string { return resourceID + diagnosticSettingsMarker + name }
	categoryResource := func(resourceID, name, categoryType string) DiagnosticSettingsCategoryResource {
		c := DiagnosticSettingsCategoryResource{
			ID:   resourceID + "/providers/Microsoft.Insights/diagnosticSettingsCategories/" + name,
			Name: name,
			Type: "Microsoft.Insights/diagnosticSettingsCategories",
		}
		c.Properties.CategoryType = categoryType
		return c
	}

	srv.HandleFunc("GET "+base+"/diagnosticSettingsCategories", func(w http.ResponseWriter, r *http.Request) {
		resourceID, resourceType, categories, known := target(r)
		if !known {
			AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
				"The simulator carries no diagnostic settings categories for resources of type %s.", resourceType)
			return
		}
		if !categories.exists(resourceID) {
			notFound(w, resourceID)
			return
		}
		value := []DiagnosticSettingsCategoryResource{}
		for _, name := range categories.logs {
			value = append(value, categoryResource(resourceID, name, "Logs"))
		}
		for _, name := range categories.metrics {
			value = append(value, categoryResource(resourceID, name, "Metrics"))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
	})

	srv.HandleFunc("GET "+base+"/diagnosticSettingsCategories/{name}", func(w http.ResponseWriter, r *http.Request) {
		resourceID, resourceType, categories, known := target(r)
		if !known {
			AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
				"The simulator carries no diagnostic settings categories for resources of type %s.", resourceType)
			return
		}
		if !categories.exists(resourceID) {
			notFound(w, resourceID)
			return
		}
		name := sim.PathParam(r, "name")
		for _, c := range categories.logs {
			if strings.EqualFold(c, name) {
				sim.WriteJSON(w, http.StatusOK, categoryResource(resourceID, c, "Logs"))
				return
			}
		}
		for _, c := range categories.metrics {
			if strings.EqualFold(c, name) {
				sim.WriteJSON(w, http.StatusOK, categoryResource(resourceID, c, "Metrics"))
				return
			}
		}
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The diagnostic settings category '%s' is not one resources of type %s offer.", name, resourceType)
	})

	srv.HandleFunc("GET "+base+"/diagnosticSettings", func(w http.ResponseWriter, r *http.Request) {
		resourceID, _, categories, known := target(r)
		if known && !categories.exists(resourceID) {
			notFound(w, resourceID)
			return
		}
		value := azureDiagnosticSettingsFor(resourceID)
		if value == nil {
			value = []DiagnosticSettingsResource{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
	})

	srv.HandleFunc("GET "+base+"/diagnosticSettings/{name}", func(w http.ResponseWriter, r *http.Request) {
		resourceID, _, categories, known := target(r)
		if known && !categories.exists(resourceID) {
			notFound(w, resourceID)
			return
		}
		name := sim.PathParam(r, "name")
		setting, ok := azureDiagnosticSettings.Get(strings.ToLower(settingID(resourceID, name)))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Insights/diagnosticSettings/%s' under resource '%s' was not found.", name, resourceID)
			return
		}
		sim.WriteJSON(w, http.StatusOK, setting)
	})

	srv.HandleFunc("PUT "+base+"/diagnosticSettings/{name}", func(w http.ResponseWriter, r *http.Request) {
		resourceID, resourceType, categories, known := target(r)
		if !known {
			AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
				"The simulator carries no diagnostic settings categories for resources of type %s, so it routes none of their logs.", resourceType)
			return
		}
		if !categories.exists(resourceID) {
			notFound(w, resourceID)
			return
		}
		var req DiagnosticSettingsResource
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		p := req.Properties
		if p.WorkspaceID == "" && p.StorageAccountID == "" && p.EventHubAuthorizationRuleID == "" && p.MarketplacePartnerID == "" {
			AzureError(w, "BadRequest", "A diagnostic setting names at least one destination: a Log Analytics workspace, a storage account, an Event Hubs authorization rule or a Marketplace partner.", http.StatusBadRequest)
			return
		}
		if p.WorkspaceID != "" {
			if _, ok := logAnalyticsWorkspaceByID(p.WorkspaceID); !ok {
				AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The Log Analytics workspace '%s' was not found.", p.WorkspaceID)
				return
			}
		}
		for _, l := range p.Logs {
			switch {
			case l.Category != "" && l.CategoryGroup != "":
				AzureError(w, "BadRequest", "A log setting names a category or a category group, not both.", http.StatusBadRequest)
				return
			case l.Category != "":
				if !containsFold(categories.logs, l.Category) {
					AzureErrorf(w, "BadRequest", http.StatusBadRequest,
						"The log category '%s' is not one resources of type %s offer; the categories are %s.", l.Category, resourceType, strings.Join(categories.logs, ", "))
					return
				}
			case l.CategoryGroup != "":
				if len(categories.logs) == 0 || !containsFold(azureDiagnosticCategoryGroups, l.CategoryGroup) {
					AzureErrorf(w, "BadRequest", http.StatusBadRequest,
						"The log category group '%s' is not one resources of type %s offer.", l.CategoryGroup, resourceType)
					return
				}
			default:
				AzureError(w, "BadRequest", "A log setting names a category or a category group.", http.StatusBadRequest)
				return
			}
		}
		for _, m := range p.Metrics {
			if !containsFold(categories.metrics, m.Category) {
				AzureErrorf(w, "BadRequest", http.StatusBadRequest,
					"The metric category '%s' is not one resources of type %s offer.", m.Category, resourceType)
				return
			}
		}
		name := sim.PathParam(r, "name")
		id := settingID(resourceID, name)
		existing, replacing := azureDiagnosticSettings.Get(strings.ToLower(id))
		// "You can create up to five diagnostic settings for your
		// environment" (Log options in Azure Container Apps).
		if !replacing && len(azureDiagnosticSettingsFor(resourceID)) >= 5 {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The resource '%s' already has five diagnostic settings, the most a resource takes.", resourceID)
			return
		}
		if p.Logs == nil {
			p.Logs = []DiagnosticLogSettings{}
		}
		if p.Metrics == nil {
			p.Metrics = []DiagnosticMetricSettings{}
		}
		now := time.Now().UTC().Format(time.RFC3339)
		created := now
		if replacing && existing.SystemData != nil && existing.SystemData.CreatedAt != "" {
			created = existing.SystemData.CreatedAt
		}
		setting := DiagnosticSettingsResource{
			ID: id, Name: name, Type: "Microsoft.Insights/diagnosticSettings", Properties: p,
			SystemData: &SystemData{CreatedAt: created, LastModifiedAt: now},
		}
		azureDiagnosticSettings.Put(strings.ToLower(id), setting)
		sim.WriteJSON(w, http.StatusOK, setting)
	})

	srv.HandleFunc("DELETE "+base+"/diagnosticSettings/{name}", func(w http.ResponseWriter, r *http.Request) {
		resourceID, _, _, _ := target(r)
		name := sim.PathParam(r, "name")
		if azureDiagnosticSettings.Delete(strings.ToLower(settingID(resourceID, name))) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func containsFold(list []string, value string) bool {
	for _, item := range list {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}
