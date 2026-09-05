package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// AppInsightsPurge tracks an asynchronous data-purge operation
// (ComponentsClient.Purge / GetPurgeStatus).
type AppInsightsPurge struct {
	OperationID string `json:"operationId"`
	Status      string `json:"status"`
}

// AppInsightsComponent represents an Azure Application Insights component.
type AppInsightsComponent struct {
	ID         string                         `json:"id"`
	Name       string                         `json:"name"`
	Type       string                         `json:"type"`
	Location   string                         `json:"location"`
	Kind       string                         `json:"kind,omitempty"`
	Tags       map[string]string              `json:"tags"`
	Properties AppInsightsComponentProperties `json:"properties"`
}

// AppInsightsComponentProperties holds the properties of an Application Insights component.
// App Insights uses PascalCase for some property names (InstrumentationKey, ConnectionString)
// unlike most ARM APIs which use camelCase — the SDK serde reflects this.
type AppInsightsComponentProperties struct {
	ApplicationID                   string  `json:"ApplicationId,omitempty"`
	ApplicationType                 string  `json:"Application_Type,omitempty"`
	InstrumentationKey              string  `json:"InstrumentationKey,omitempty"`
	ConnectionString                string  `json:"ConnectionString,omitempty"`
	RetentionInDays                 int     `json:"RetentionInDays,omitempty"`
	SamplingPercentage              float64 `json:"SamplingPercentage,omitempty"`
	PublicNetworkAccessForIngestion string  `json:"publicNetworkAccessForIngestion,omitempty"`
	PublicNetworkAccessForQuery     string  `json:"publicNetworkAccessForQuery,omitempty"`
	// WorkspaceResourceId links a workspace-based component to its Log Analytics
	// workspace; terraform-provider-azurerm reads it back as workspace_id.
	WorkspaceResourceId string `json:"WorkspaceResourceId,omitempty"`
	ProvisioningState   string `json:"provisioningState"`
}

// AppInsightsBillingFeatures is the currentbillingfeatures wire shape. App
// Insights is a legacy API that uses PascalCase JSON (not camelCase) — see the
// note on AppInsightsComponentProperties.
type AppInsightsBillingFeatures struct {
	DataVolumeCap          AppInsightsDataVolumeCap `json:"DataVolumeCap"`
	CurrentBillingFeatures []string                 `json:"CurrentBillingFeatures"`
}

type AppInsightsDataVolumeCap struct {
	Cap                                  float64 `json:"Cap"`
	ResetTime                            int     `json:"ResetTime"`
	StopSendNotificationWhenHitCap       bool    `json:"StopSendNotificationWhenHitCap"`
	StopSendNotificationWhenHitThreshold bool    `json:"StopSendNotificationWhenHitThreshold"`
	WarningThreshold                     int     `json:"WarningThreshold"`
	MaxHistoryCap                        int     `json:"MaxHistoryCap"`
}

var azureAppInsightsComponents sim.Store[AppInsightsComponent]

// A component is stored under its ARM id while the query data plane addresses
// it by the application id it was issued, so the one reaches the other through
// an index rather than a walk of every component.
var azureInsightsByApplicationID sim.GenerationIndex[AppInsightsComponent]

func azureInsightsApplicationIDKeys(c AppInsightsComponent) []string {
	if c.Properties.ApplicationID == "" {
		return nil
	}
	return []string{strings.ToLower(c.Properties.ApplicationID)}
}

func registerApplicationInsights(srv *sim.Server) {
	components := sim.MakeStore[AppInsightsComponent](srv.DB(), "insights_components")
	azureAppInsightsComponents = components

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights"

	// PUT - Create or update component
	srv.HandleFunc("PUT "+armBase+"/components/{componentName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "componentName")

		var req AppInsightsComponent
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.Location == "" {
			AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Insights/components/%s", sub, rg, name)

		kind := req.Kind
		if kind == "" {
			kind = "web"
		}

		// Preserve stable IDs across upserts (real App Insights keeps the same instrumentation key).
		appID := generateUUID()
		instrumentationKey := generateUUID()
		if existing, exists := components.Get(resourceID); exists {
			appID = existing.Properties.ApplicationID
			instrumentationKey = existing.Properties.InstrumentationKey
		}

		// Default provider-managed properties when the request omits them so the
		// read-back echoes the same values terraform-provider-azurerm expects,
		// matching real ARM Microsoft.Insights/components defaults.
		appType := req.Properties.ApplicationType
		if appType == "" {
			appType = "web"
		}
		retentionInDays := req.Properties.RetentionInDays
		if retentionInDays == 0 {
			retentionInDays = 90
		}
		samplingPercentage := req.Properties.SamplingPercentage
		if samplingPercentage == 0 {
			samplingPercentage = 100
		}
		publicNetworkAccessForIngestion := req.Properties.PublicNetworkAccessForIngestion
		if publicNetworkAccessForIngestion == "" {
			publicNetworkAccessForIngestion = "Enabled"
		}
		publicNetworkAccessForQuery := req.Properties.PublicNetworkAccessForQuery
		if publicNetworkAccessForQuery == "" {
			publicNetworkAccessForQuery = "Enabled"
		}

		// terraform reads tags as a non-null map; echo {} rather than absent/null.
		tags := req.Tags
		if tags == nil {
			tags = map[string]string{}
		}

		comp := AppInsightsComponent{
			ID:       resourceID,
			Name:     name,
			Type:     "Microsoft.Insights/components",
			Location: req.Location,
			Kind:     kind,
			Tags:     tags,
			Properties: AppInsightsComponentProperties{
				ApplicationID:      appID,
				ApplicationType:    appType,
				InstrumentationKey: instrumentationKey,
				ConnectionString: fmt.Sprintf(
					"InstrumentationKey=%s;IngestionEndpoint=https://eastus-0.in.applicationinsights.azure.com/;LiveEndpoint=https://eastus.livediagnostics.monitor.azure.com/;ApplicationId=%s",
					instrumentationKey, appID),
				RetentionInDays:                 retentionInDays,
				SamplingPercentage:              samplingPercentage,
				PublicNetworkAccessForIngestion: publicNetworkAccessForIngestion,
				PublicNetworkAccessForQuery:     publicNetworkAccessForQuery,
				WorkspaceResourceId:             req.Properties.WorkspaceResourceId,
				ProvisioningState:               "Succeeded",
			},
		}

		components.Put(resourceID, comp)
		// go-azure-sdk expects 200 for sync creates
		sim.WriteJSON(w, http.StatusOK, comp)
	})

	// GET - Get component
	srv.HandleFunc("GET "+armBase+"/components/{componentName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "componentName")

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Insights/components/%s", sub, rg, name)

		comp, ok := components.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Insights/components/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		sim.WriteJSON(w, http.StatusOK, comp)
	})

	// DELETE - Delete component
	srv.HandleFunc("DELETE "+armBase+"/components/{componentName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "componentName")

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Insights/components/%s", sub, rg, name)

		if components.Delete(resourceID) {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// PATCH - Update component tags only (ComponentsClient.UpdateTags).
	srv.HandleFunc("PATCH "+armBase+"/components/{componentName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "componentName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Insights/components/%s", sub, rg, name)
		comp, ok := components.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Insights/components/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		var req struct {
			Tags map[string]string `json:"tags"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Tags == nil {
			req.Tags = map[string]string{}
		}
		comp.Tags = req.Tags
		components.Put(resourceID, comp)
		sim.WriteJSON(w, http.StatusOK, comp)
	})

	// GET - List components in a resource group (ComponentsClient.NewListByResourceGroupPager).
	srv.HandleFunc("GET "+armBase+"/components", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Insights/components/", sub, rg)
		out := components.Filter(func(c AppInsightsComponent) bool {
			return strings.HasPrefix(c.ID, prefix)
		})
		if out == nil {
			out = []AppInsightsComponent{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	// GET - List components in the subscription (ComponentsClient.NewListPager).
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Insights/components", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/", sub)
		out := components.Filter(func(c AppInsightsComponent) bool {
			return strings.HasPrefix(c.ID, prefix)
		})
		if out == nil {
			out = []AppInsightsComponent{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	// POST - Purge data, and GET its status (ComponentsClient.Purge /
	// GetPurgeStatus). Purge is a 202 that returns an operationId the caller
	// later polls for completion.
	purges := sim.MakeStore[AppInsightsPurge](srv.DB(), "insights_purges")
	srv.HandleFunc("POST "+armBase+"/components/{componentName}/purge", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "componentName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Insights/components/%s", sub, rg, name)
		if _, ok := components.Get(resourceID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Insights/components/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		var body struct {
			Table   string `json:"table"`
			Filters []any  `json:"filters"`
		}
		if err := sim.ReadJSON(r, &body); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Table == "" {
			AzureError(w, "InvalidRequestContent", "The 'table' property is required.", http.StatusBadRequest)
			return
		}
		purgeID := generateUUID()
		purges.Put(purgeID, AppInsightsPurge{OperationID: purgeID, Status: "completed"})
		// Real App Insights returns the purge id in both the body and the
		// x-ms-status-location header pointing at the operations status URL.
		w.Header().Set("x-ms-status-location", fmt.Sprintf("%s://%s%s/operations/%s?api-version=%s",
			azureRequestScheme(r), r.Host, resourceID, purgeID, r.URL.Query().Get("api-version")))
		sim.WriteJSON(w, http.StatusAccepted, map[string]any{"operationId": purgeID})
	})
	srv.HandleFunc("GET "+armBase+"/components/{componentName}/operations/{purgeId}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := purges.Get(sim.PathParam(r, "purgeId"))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Purge operation %q not found.", sim.PathParam(r, "purgeId"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"status": p.Status})
	})

	// GET/PUT - Billing features (azurerm provider reads then updates after
	// creating a component). PUT must persist + echo the submitted DataVolumeCap
	// / CurrentBillingFeatures so e.g. daily_data_cap_in_gb round-trips instead
	// of perpetually drifting to a static value.
	billing := sim.MakeStore[AppInsightsBillingFeatures](srv.DB(), "insights_billing")
	defaultBilling := AppInsightsBillingFeatures{
		DataVolumeCap: AppInsightsDataVolumeCap{
			Cap:              100,
			ResetTime:        0,
			WarningThreshold: 90,
			MaxHistoryCap:    500,
		},
		CurrentBillingFeatures: []string{"Basic"},
	}
	billingKey := func(r *http.Request) string {
		return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Insights/components/%s",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "componentName"))
	}
	registerInsightsFeatures(srv, armBase, billing, defaultBilling, billingKey)

	srv.HandleFunc("GET "+armBase+"/components/{componentName}/currentbillingfeatures", func(w http.ResponseWriter, r *http.Request) {
		b, ok := billing.Get(billingKey(r))
		if !ok {
			b = insightsDefaultBilling(defaultBilling)
		}
		sim.WriteJSON(w, http.StatusOK, b)
	})
	srv.HandleFunc("PUT "+armBase+"/components/{componentName}/currentbillingfeatures", func(w http.ResponseWriter, r *http.Request) {
		b := insightsDefaultBilling(defaultBilling)
		if err := sim.ReadJSON(r, &b); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		// ResetTime / MaxHistoryCap are server-computed read-only fields.
		b.DataVolumeCap.MaxHistoryCap = defaultBilling.DataVolumeCap.MaxHistoryCap
		billing.Put(billingKey(r), b)
		sim.WriteJSON(w, http.StatusOK, b)
	})

	// The data plane — the application's telemetry, read through the same query
	// engine Log Analytics uses.
	registerInsightsDataPlane(srv)
}

// insightsDefaultBilling copies the plan a component starts on, slice and all.
// Assigning the default gives away its backing array, and decoding a request
// body into the copy writes through it: a PUT naming the Enterprise plan
// overwrote "Basic" inside the default, so every component created afterwards
// started on Enterprise without anyone asking. A default is read by every
// request that has no record of its own, which makes it exactly the value a
// request must never be able to write to.
func insightsDefaultBilling(from AppInsightsBillingFeatures) AppInsightsBillingFeatures {
	out := from
	out.CurrentBillingFeatures = append([]string(nil), from.CurrentBillingFeatures...)
	return out
}
