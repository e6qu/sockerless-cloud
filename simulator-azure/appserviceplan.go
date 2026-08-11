package main

import (
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

type AppServicePlan struct {
	ID         string                   `json:"id"`
	Name       string                   `json:"name"`
	Type       string                   `json:"type"`
	Location   string                   `json:"location"`
	Tags       map[string]string        `json:"tags,omitempty"`
	Kind       string                   `json:"kind"`
	Sku        AppServicePlanSku        `json:"sku"`
	Properties AppServicePlanProperties `json:"properties"`
}

type AppServicePlanSku struct {
	Name     string `json:"name"`
	Tier     string `json:"tier"`
	Size     string `json:"size"`
	Family   string `json:"family"`
	Capacity int    `json:"capacity"`
}

type AppServicePlanProperties struct {
	ProvisioningState         string `json:"provisioningState"`
	Status                    string `json:"status"`
	Reserved                  bool   `json:"reserved"`
	IsXenon                   bool   `json:"isXenon"`
	MaximumElasticWorkerCount int    `json:"maximumElasticWorkerCount"`
	NumberOfSites             int    `json:"numberOfSites"`
}

var azureAppServicePlans sim.Store[AppServicePlan]

func registerAppServicePlan(srv *sim.Server) {
	plans := sim.MakeStore[AppServicePlan](srv.DB(), "app_service_plans")
	azureAppServicePlans = plans

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web"

	// go-azure-sdk uses "serverFarms" (capital F), azurestack uses "serverfarms".
	// Register both casings. Resource IDs are normalized to lowercase.
	for _, sfPath := range []string{"serverfarms", "serverFarms"} {
		// PUT - Create or update service plan
		srv.HandleFunc("PUT "+armBase+"/"+sfPath+"/{planName}", func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			planName := sim.PathParam(r, "planName")

			var req AppServicePlan
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
				return
			}

			resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/%s",
				sub, rg, planName)

			// Default SKU values
			sku := req.Sku
			if sku.Name == "" {
				sku.Name = "Y1"
			}
			if sku.Tier == "" {
				sku.Tier = "Dynamic"
			}

			plan := AppServicePlan{
				ID:       resourceID,
				Name:     planName,
				Type:     "Microsoft.Web/serverfarms",
				Location: req.Location,
				Tags:     req.Tags,
				Kind:     req.Kind,
				Sku:      sku,
				Properties: AppServicePlanProperties{
					ProvisioningState: "Succeeded",
					Status:            "Ready",
					Reserved:          req.Properties.Reserved,
					IsXenon:           req.Properties.IsXenon,
				},
			}
			plans.Put(resourceID, plan)

			sim.WriteJSON(w, http.StatusOK, plan)
		})

		// GET - Get service plan
		srv.HandleFunc("GET "+armBase+"/"+sfPath+"/{planName}", func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			planName := sim.PathParam(r, "planName")
			resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/%s",
				sub, rg, planName)

			plan, ok := plans.Get(resourceID)
			if !ok {
				sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"Server farm '%s' not found.", planName)
				return
			}
			sim.WriteJSON(w, http.StatusOK, plan)
		})

		// DELETE - Delete service plan
		srv.HandleFunc("DELETE "+armBase+"/"+sfPath+"/{planName}", func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			planName := sim.PathParam(r, "planName")
			resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/%s",
				sub, rg, planName)

			plans.Delete(resourceID)
			w.WriteHeader(http.StatusOK)
		})
	}

	// The azurerm v3 provider (go-azure-sdk) also sends a GET to
	// .../providers/Microsoft.Web/serverFarms?api-version=... (list all in RG)
	// when checking if a service plan exists. Handle this as well.
	srv.HandleFunc("GET "+armBase+"/serverFarms", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/",
			sub, rg)
		filtered := plans.Filter(func(p AppServicePlan) bool {
			return strings.HasPrefix(p.ID, prefix)
		})
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": filtered})
	})
}
