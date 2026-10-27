package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
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
	// An App Service Environment reads its inventory back through this, and its
	// stamp capacity counts the plan's instances against the pool targeted.
	HostingEnvironmentProfile *HostingEnvironmentProfile `json:"hostingEnvironmentProfile,omitempty"`
	// TargetWorkerSizeID selects the App Service Environment pool the plan's
	// instances consume capacity from.
	TargetWorkerSizeID *int32 `json:"targetWorkerSizeId,omitempty"`
}

var azureAppServicePlans sim.Store[AppServicePlan]

// appServicePlanSkuTier maps a SKU name onto the tier and family the service
// reports, from Microsoft's published App Service plan SKU catalog: F1 Free,
// D1 Shared, B* Basic, S* Standard, P*v4 PremiumV4, P*v3 / P*mv3 PremiumV3,
// P*v2 PremiumV2, P* Premium, I*v2 / I*mv2 IsolatedV2, I* Isolated, Y1
// Dynamic, FC1 FlexConsumption, EP* ElasticPremium, WS* WorkflowStandard.
func appServicePlanSkuTier(name string) (tier, family string) {
	upper := strings.ToUpper(name)
	switch {
	case upper == "F1" || upper == "FREE":
		return "Free", "F"
	case upper == "D1" || upper == "SHARED":
		return "Shared", "D"
	case upper == "Y1":
		return "Dynamic", "Y"
	case upper == "FC1":
		return "FlexConsumption", "FC"
	case strings.HasPrefix(upper, "EP"):
		return "ElasticPremium", "EP"
	case strings.HasPrefix(upper, "WS"):
		return "WorkflowStandard", "WS"
	case strings.HasPrefix(upper, "B"):
		return "Basic", "B"
	case strings.HasPrefix(upper, "S"):
		return "Standard", "S"
	case strings.HasPrefix(upper, "P"):
		switch {
		case strings.HasSuffix(upper, "V4"):
			return "PremiumV4", "Pv4"
		case strings.HasSuffix(upper, "V3"):
			return "PremiumV3", "Pv3"
		case strings.HasSuffix(upper, "V2"):
			return "PremiumV2", "Pv2"
		}
		return "Premium", "P"
	case strings.HasPrefix(upper, "I"):
		if strings.HasSuffix(upper, "V2") {
			return "IsolatedV2", "Iv2"
		}
		return "Isolated", "I"
	}
	// Report an uncatalogued name as its own tier rather than claim one.
	return name, name
}

func registerAppServicePlan(srv *sim.Server) {
	plans := sim.MakeStore[AppServicePlan](srv.DB(), "app_service_plans")
	azureAppServicePlans = plans

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web"

	// go-azure-sdk sends "serverFarms", azurestack "serverfarms".
	for _, sfPath := range []string{"serverfarms", "serverFarms"} {
		srv.HandleFunc("PUT "+armBase+"/"+sfPath+"/{planName}", func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			planName := sim.PathParam(r, "planName")

			var req AppServicePlan
			if err := sim.ReadJSON(r, &req); err != nil {
				AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
				return
			}

			resourceID := armResourceID(sub, rg, "Microsoft.Web", "serverfarms", planName)

			// The resource provider refuses an App Service Environment it cannot resolve.
			hostingEnvironment, err := webResolveHostingEnvironmentProfile(req.Properties.HostingEnvironmentProfile)
			if err != nil {
				AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
				return
			}

			// The service derives tier, family and size from the SKU name.
			// terraform-provider-azurerm's azurerm_service_plan sends only the name and
			// capacity, and consumers branch on the tier: the Function App resource
			// refuses a backup configuration on a Dynamic-tier plan.
			sku := req.Sku
			if sku.Name == "" {
				sku.Name = "Y1"
			}
			tier, family := appServicePlanSkuTier(sku.Name)
			if sku.Tier == "" {
				sku.Tier = tier
			}
			if sku.Family == "" {
				sku.Family = family
			}
			if sku.Size == "" {
				sku.Size = sku.Name
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
					ProvisioningState:         "Succeeded",
					Status:                    "Ready",
					Reserved:                  req.Properties.Reserved,
					IsXenon:                   req.Properties.IsXenon,
					HostingEnvironmentProfile: hostingEnvironment,
					TargetWorkerSizeID:        req.Properties.TargetWorkerSizeID,
				},
			}
			plans.Put(resourceID, plan)

			sim.WriteJSON(w, http.StatusOK, plan)
		})

		srv.HandleFunc("GET "+armBase+"/"+sfPath+"/{planName}", func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			planName := sim.PathParam(r, "planName")
			resourceID := armResourceID(sub, rg, "Microsoft.Web", "serverfarms", planName)

			plan, ok := plans.Get(resourceID)
			if !ok {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
					"Server farm '%s' not found.", planName)
				return
			}
			sim.WriteJSON(w, http.StatusOK, plan)
		})

		srv.HandleFunc("DELETE "+armBase+"/"+sfPath+"/{planName}", func(w http.ResponseWriter, r *http.Request) {
			sub := sim.PathParam(r, "subscriptionId")
			rg := sim.PathParam(r, "resourceGroupName")
			planName := sim.PathParam(r, "planName")
			resourceID := armResourceID(sub, rg, "Microsoft.Web", "serverfarms", planName)

			plans.Delete(resourceID)
			// The plan's VNet routes and connection gateways are stored under the plan ID
			// and go with it.
			prefix := resourceID + "/"
			for _, rt := range webPlanVnetRoutes.Filter(func(rt WebVnetRoute) bool { return strings.HasPrefix(rt.ID, prefix) }) {
				webPlanVnetRoutes.Delete(rt.ID)
			}
			for _, gw := range webVnetGateways.Filter(func(gw WebVnetGateway) bool { return strings.HasPrefix(gw.ID, prefix) }) {
				webVnetGateways.Delete(gw.ID)
			}
			w.WriteHeader(http.StatusOK)
		})
	}

	// go-azure-sdk lists the group's serverFarms when checking a plan exists.
	srv.HandleFunc("GET "+armBase+"/serverFarms", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		prefix := armResourceID(sub, rg, "Microsoft.Web", "serverfarms") + "/"
		filtered := plans.Filter(func(p AppServicePlan) bool {
			return strings.HasPrefix(p.ID, prefix)
		})
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": filtered})
	})
}
