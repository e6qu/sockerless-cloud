package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// AppInsightsPurge is an asynchronous data purge (Purge / GetPurgeStatus).
type AppInsightsPurge struct {
	OperationID string `json:"operationId"`
	Status      string `json:"status"`
}

type AppInsightsComponent struct {
	ID         string                         `json:"id"`
	Name       string                         `json:"name"`
	Type       string                         `json:"type"`
	Location   string                         `json:"location"`
	Kind       string                         `json:"kind,omitempty"`
	Tags       map[string]string              `json:"tags"`
	Properties AppInsightsComponentProperties `json:"properties"`
}

// Application Insights spells some properties in PascalCase
// (InstrumentationKey, ConnectionString), unlike most Azure Resource Manager
// APIs.
type AppInsightsComponentProperties struct {
	// AppID is the id the data plane addresses the component by; ApplicationID
	// mirrors the component's name.
	AppID                           string  `json:"AppId,omitempty"`
	ApplicationID                   string  `json:"ApplicationId,omitempty"`
	ApplicationType                 string  `json:"Application_Type,omitempty"`
	InstrumentationKey              string  `json:"InstrumentationKey,omitempty"`
	ConnectionString                string  `json:"ConnectionString,omitempty"`
	RetentionInDays                 int     `json:"RetentionInDays,omitempty"`
	SamplingPercentage              float64 `json:"SamplingPercentage,omitempty"`
	PublicNetworkAccessForIngestion string  `json:"publicNetworkAccessForIngestion,omitempty"`
	PublicNetworkAccessForQuery     string  `json:"publicNetworkAccessForQuery,omitempty"`
	// terraform-provider-azurerm reads WorkspaceResourceId back as workspace_id.
	WorkspaceResourceId string `json:"WorkspaceResourceId,omitempty"`
	ProvisioningState   string `json:"provisioningState"`
}

// The currentbillingfeatures wire shape, in Application Insights' PascalCase.
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

// The store keys components by resource ID while the query data plane
// addresses them by app id.
var azureInsightsByApplicationID sim.GenerationIndex[AppInsightsComponent]

func azureInsightsApplicationIDKeys(c AppInsightsComponent) []string {
	if c.Properties.AppID == "" {
		return nil
	}
	return []string{strings.ToLower(c.Properties.AppID)}
}

// Telemetry names its component by instrumentation key, and a resource-centric
// query by resource ID in any casing.
var (
	azureInsightsByInstrumentationKey sim.GenerationIndex[AppInsightsComponent]
	azureInsightsByResourceID         sim.GenerationIndex[AppInsightsComponent]
)

func appInsightsInstrumentationKeyKeys(c AppInsightsComponent) []string {
	if c.Properties.InstrumentationKey == "" {
		return nil
	}
	return []string{strings.ToLower(c.Properties.InstrumentationKey)}
}

func appInsightsResourceIDKeys(c AppInsightsComponent) []string {
	return []string{strings.ToLower(c.ID)}
}

func registerApplicationInsights(srv *sim.Server) {
	components := sim.MakeStore[AppInsightsComponent](srv.DB(), "insights_components")
	azureAppInsightsComponents = components

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights"

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

		resourceID := armResourceID(sub, rg, "Microsoft.Insights", "components", name)

		kind := req.Kind
		if kind == "" {
			kind = "web"
		}

		// Application Insights keeps the instrumentation key across upserts.
		appID := sim.NewUUID()
		instrumentationKey := sim.NewUUID()
		if existing, exists := components.Get(resourceID); exists {
			appID = existing.Properties.AppID
			instrumentationKey = existing.Properties.InstrumentationKey
		}

		// Default the provider-managed properties as Microsoft.Insights/components
		// does, so the read-back matches what terraform-provider-azurerm expects.
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

		// terraform reads tags as a non-null map.
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
				AppID:              appID,
				ApplicationID:      name,
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
		// go-azure-sdk expects 200 for a synchronous create.
		sim.WriteJSON(w, http.StatusOK, comp)
	})

	srv.HandleFunc("GET "+armBase+"/components/{componentName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "componentName")

		resourceID := armResourceID(sub, rg, "Microsoft.Insights", "components", name)

		comp, ok := components.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Insights/components/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		sim.WriteJSON(w, http.StatusOK, comp)
	})

	srv.HandleFunc("DELETE "+armBase+"/components/{componentName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "componentName")

		resourceID := armResourceID(sub, rg, "Microsoft.Insights", "components", name)

		if components.Delete(resourceID) {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// ComponentsClient.UpdateTags: tags only.
	srv.HandleFunc("PATCH "+armBase+"/components/{componentName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "componentName")
		resourceID := armResourceID(sub, rg, "Microsoft.Insights", "components", name)
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

	srv.HandleFunc("GET "+armBase+"/components", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		prefix := armResourceID(sub, rg, "Microsoft.Insights", "components") + "/"
		out := components.Filter(func(c AppInsightsComponent) bool {
			return strings.HasPrefix(c.ID, prefix)
		})
		if out == nil {
			out = []AppInsightsComponent{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Insights/components", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		prefix := armSubscriptionID(sub) + "/resourceGroups/"
		out := components.Filter(func(c AppInsightsComponent) bool {
			return strings.HasPrefix(c.ID, prefix)
		})
		if out == nil {
			out = []AppInsightsComponent{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	// Purge answers 202 with an operationId the caller polls.
	purges := sim.MakeStore[AppInsightsPurge](srv.DB(), "insights_purges")
	srv.HandleFunc("POST "+armBase+"/components/{componentName}/purge", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "componentName")
		resourceID := armResourceID(sub, rg, "Microsoft.Insights", "components", name)
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
		purgeID := sim.NewUUID()
		purges.Put(purgeID, AppInsightsPurge{OperationID: purgeID, Status: "completed"})
		// Application Insights returns the purge id in the body and an
		// x-ms-status-location header at the operation status URL.
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

	// The azurerm provider reads then updates billing features after creating a
	// component, so PUT persists DataVolumeCap and CurrentBillingFeatures and
	// daily_data_cap_in_gb round-trips.
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
		return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Insights", "components", sim.PathParam(r, "componentName"))
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
		// ResetTime and MaxHistoryCap are server-computed and read-only.
		b.DataVolumeCap.MaxHistoryCap = defaultBilling.DataVolumeCap.MaxHistoryCap
		billing.Put(billingKey(r), b)
		sim.WriteJSON(w, http.StatusOK, b)
	})

	// The application's telemetry, read through the Log Analytics query engine.
	registerInsightsDataPlane(srv)
}

// insightsDefaultBilling deep-copies the default plan: decoding a request into
// a shallow copy writes through the shared slice into the default every
// component without a record reads.
func insightsDefaultBilling(from AppInsightsBillingFeatures) AppInsightsBillingFeatures {
	out := from
	out.CurrentBillingFeatures = append([]string(nil), from.CurrentBillingFeatures...)
	return out
}
