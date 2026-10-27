package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The simulator meters no billing, offers no premier add-ons and enforces no
// location quotas, so those collections are empty. The deleted-web-app reads
// answer from the retention store a site or slot delete writes
// (web_backup.go), which WebApps_RestoreFromDeletedApp replays.

func registerWebProviderGlobal(srv *sim.Server, site func(string, string, http.HandlerFunc)) {
	srv.HandleFunc("GET /providers/Microsoft.Web/operations", func(w http.ResponseWriter, _ *http.Request) {
		op := func(name, resource, operation, description string) map[string]any {
			return map[string]any{
				"name": name,
				"display": map[string]any{
					"provider":    "Microsoft Web Apps",
					"resource":    resource,
					"operation":   operation,
					"description": description,
				},
				"origin": "user,system",
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []map[string]any{
			op("Microsoft.Web/sites/Read", "Web App", "Get Web App", "Get the properties of a Web App"),
			op("Microsoft.Web/sites/Write", "Web App", "Create or Update Web App", "Create a new Web App or update an existing one"),
			op("Microsoft.Web/sites/Delete", "Web App", "Delete Web App", "Delete an existing Web App"),
			op("Microsoft.Web/sites/slots/Read", "Web App Slot", "Get Web App Slot", "Get the properties of a Web App deployment slot"),
			op("Microsoft.Web/sites/slots/Write", "Web App Slot", "Create or Update Web App Slot", "Create a new Web App deployment slot or update an existing one"),
			op("Microsoft.Web/sites/slots/Delete", "Web App Slot", "Delete Web App Slot", "Delete an existing Web App deployment slot"),
			op("Microsoft.Web/serverfarms/Read", "App Service Plan", "Get App Service Plan", "Get the properties of an App Service Plan"),
			op("Microsoft.Web/serverfarms/Write", "App Service Plan", "Create or Update App Service Plan", "Create a new App Service Plan or update an existing one"),
			op("Microsoft.Web/serverfarms/Delete", "App Service Plan", "Delete App Service Plan", "Delete an existing App Service Plan"),
			op("Microsoft.Web/certificates/Read", "Certificate", "Get Certificate", "Get the list of certificates"),
			op("Microsoft.Web/certificates/Write", "Certificate", "Add or Update Certificate", "Add a new certificate or update an existing one"),
			op("Microsoft.Web/certificates/Delete", "Certificate", "Delete Certificate", "Delete an existing certificate"),
			op("Microsoft.Web/staticSites/Read", "Static Web App", "Get Static Web App", "Get the properties of a Static Web App"),
			op("Microsoft.Web/staticSites/Write", "Static Web App", "Create or Update Static Web App", "Create a new Static Web App or update an existing one"),
			op("Microsoft.Web/staticSites/Delete", "Static Web App", "Delete Static Web App", "Delete an existing Static Web App"),
			op("Microsoft.Web/sites/hostNameBindings/Read", "Hostname Binding", "Get Hostname Binding", "Get Web App's hostname bindings"),
			op("Microsoft.Web/sites/hostNameBindings/Write", "Hostname Binding", "Create or Update Hostname Binding", "Create or update a Web App hostname binding"),
			op("Microsoft.Web/sites/hostNameBindings/Delete", "Hostname Binding", "Delete Hostname Binding", "Delete a Web App hostname binding"),
		}})
	})

	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/validate", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name       string `json:"name"`
			Type       string `json:"type"`
			Location   string `json:"location"`
			Properties struct {
				ServerFarmID string `json:"serverFarmId"`
				SkuName      string `json:"skuName"`
				Capacity     int    `json:"capacity"`
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		fail := func(code, message string) {
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"status": "Failure",
				"error":  map[string]any{"code": code, "message": message},
			})
		}
		if req.Name == "" || req.Type == "" {
			fail("ValidateRequestInvalid", "The validate request requires both 'name' and 'type'.")
			return
		}
		switch {
		case strings.EqualFold(req.Type, "Site"):
			if req.Properties.ServerFarmID == "" {
				fail("ServerFarmRequired", "The 'properties.serverFarmId' member is required to validate a Site.")
				return
			}
			if _, ok := webValidateLookupPlan(req.Properties.ServerFarmID); !ok {
				fail("ServerFarmNotFound", fmt.Sprintf("The App Service plan '%s' was not found.", req.Properties.ServerFarmID))
				return
			}
		case strings.EqualFold(req.Type, "ServerFarm"):
			if req.Properties.Capacity < 0 {
				fail("InvalidCapacity", "The 'properties.capacity' member must not be negative.")
				return
			}
		case strings.EqualFold(req.Type, "Microsoft.Web/hostingEnvironments"):
			suffix := "/providers/Microsoft.Web/hostingEnvironments/" + req.Name
			for _, ase := range webHostingEnvironments.List() {
				if strings.HasSuffix(strings.ToLower(ase.ID), strings.ToLower(suffix)) {
					fail("AlreadyExists", fmt.Sprintf("The App Service Environment '%s' already exists.", req.Name))
					return
				}
			}
		default:
			fail("InvalidResourceType", fmt.Sprintf("The resource type '%s' is not one of ServerFarm, Site, Microsoft.Web/hostingEnvironments.", req.Type))
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"status": "Success"})
	})

	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.Web/verifyHostingEnvironmentVnet", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		var req struct {
			Properties struct {
				VnetResourceGroup string `json:"vnetResourceGroup"`
				VnetName          string `json:"vnetName"`
				VnetSubnetName    string `json:"vnetSubnetName"`
				SubnetResourceID  string `json:"subnetResourceId"`
			} `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		subnetID := req.Properties.SubnetResourceID
		if subnetID == "" {
			subnetID = armResourceID(sub, req.Properties.VnetResourceGroup, "Microsoft.Network", "virtualNetworks", req.Properties.VnetName, "subnets", req.Properties.VnetSubnetName)
		}
		var failedTests []map[string]any
		if _, ok := azureSubnets.Get(subnetID); !ok {
			failedTests = append(failedTests, map[string]any{
				"name": "Subnet exists",
				"properties": map[string]any{
					"testName": "Subnet exists",
					"details":  fmt.Sprintf("The subnet '%s' was not found.", subnetID),
				},
			})
		}
		props := map[string]any{
			"failed":      len(failedTests) > 0,
			"failedTests": failedTests,
			"warnings":    []any{},
		}
		if len(failedTests) == 0 {
			props["failedTests"] = []any{}
			props["message"] = "The virtual network configuration passed validation."
		} else {
			props["message"] = "The virtual network configuration failed validation."
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"name":       req.Properties.VnetName,
			"type":       "Microsoft.Web/verifyHostingEnvironmentVnet",
			"properties": props,
		})
	})

	// The response contract carries no schema; the reset is the whole effect.
	site("POST", "/updatemachinekey", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/locations/{location}/usages", func(w http.ResponseWriter, _ *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
	})

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/billingMeters", func(w http.ResponseWriter, _ *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
	})

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/aseRegions", func(w http.ResponseWriter, _ *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
	})

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/premieraddonoffers", func(w http.ResponseWriter, _ *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
	})

	deletedSiteGet := func(w http.ResponseWriter, r *http.Request) {
		row, ok := webDeletedSiteByRequest(sim.PathParam(r, "deletedSiteId"))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The deleted app with id '%s' was not found.", sim.PathParam(r, "deletedSiteId"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, deletedSiteWire(row))
	}
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/deletedSites/{deletedSiteId}", deletedSiteGet)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/locations/{location}/deletedSites/{deletedSiteId}", deletedSiteGet)
	// The snapshots DeletedAppRestoreRequest.snapshotTime can name.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/deletedSites/{deletedSiteId}/snapshots", func(w http.ResponseWriter, r *http.Request) {
		row, ok := webDeletedSiteByRequest(sim.PathParam(r, "deletedSiteId"))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The deleted app with id '%s' was not found.", sim.PathParam(r, "deletedSiteId"))
			return
		}
		out := make([]any, 0, len(row.Snapshots))
		for _, snap := range row.Snapshots {
			out = append(out, map[string]any{
				"id":         snap.ID,
				"name":       snap.Time,
				"type":       "Microsoft.Web/sites/snapshots",
				"properties": map[string]any{"time": snap.Time},
			})
		}
		// The specification declares a bare array of Snapshot, not a paged
		// collection.
		sim.WriteJSON(w, http.StatusOK, out)
	})

	// 204 while the operation record exists (the outcome travels on the
	// operationStatuses envelope), 404 for an operation never issued.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/locations/{location}/operations/{operationId}", func(w http.ResponseWriter, r *http.Request) {
		opID := sim.PathParam(r, "operationId")
		if _, ok := azureAsyncOps.Get(opID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Operation %q not found.", opID)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// webPlanSKUFor is the SKU tier of the site's App Service plan, which
// `properties.sku` reports and `az webapp` commands depend on; "" when the
// plan does not resolve.
func webPlanSKUFor(serverFarmID string) string {
	if serverFarmID == "" {
		return ""
	}
	plan, ok := webValidateLookupPlan(serverFarmID)
	if !ok {
		return ""
	}
	if plan.Sku.Tier != "" {
		return plan.Sku.Tier
	}
	return plan.Sku.Name
}

// Match case-insensitively on the ID's fixed segments.
func webValidateLookupPlan(planID string) (AppServicePlan, bool) {
	if p, ok := azureAppServicePlans.Get(planID); ok {
		return p, true
	}
	for _, p := range azureAppServicePlans.List() {
		if strings.EqualFold(p.ID, planID) {
			return p, true
		}
	}
	return AppServicePlan{}, false
}
