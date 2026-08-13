package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// web_more_extra.go holds the slot config-section mirrors, deployment-slot
// CRUD, the App Service plan list/patch/usages additions, the
// subscription-global Microsoft.Web catalogs, and the Static Web Apps slice.

// --- Slot config-section mirrors (reuse the production-site stores) ----------

func webSlotStringDictPut(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	var req AzureSiteAppSettings
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	cfg, _ := siteConfigStore.Get(webResourceID(r))
	cfg.AppSettings = req.Properties
	siteConfigStore.Put(webResourceID(r), cfg)
	sim.WriteJSON(w, http.StatusOK, AzureSiteAppSettings{
		ID:         webResourceID(r) + "/config/appsettings",
		Name:       "appsettings",
		Type:       "Microsoft.Web/sites/config",
		Properties: req.Properties,
	})
}

func webSlotStringDictList(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	cfg, _ := siteConfigStore.Get(webResourceID(r))
	props := cfg.AppSettings
	if props == nil {
		props = map[string]string{}
	}
	sim.WriteJSON(w, http.StatusOK, AzureSiteAppSettings{
		ID:         webResourceID(r) + "/config/appsettings",
		Name:       "appsettings",
		Type:       "Microsoft.Web/sites/config",
		Properties: props,
	})
}

func webSlotConnStringsPut(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	var req AzureSiteConnectionStrings
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	cfg, _ := siteConfigStore.Get(webResourceID(r))
	cfg.ConnectionStrings = req.Properties
	siteConfigStore.Put(webResourceID(r), cfg)
	sim.WriteJSON(w, http.StatusOK, AzureSiteConnectionStrings{
		ID:         webResourceID(r) + "/config/connectionstrings",
		Name:       "connectionstrings",
		Type:       "Microsoft.Web/sites/config",
		Properties: req.Properties,
	})
}

func webSlotConnStringsList(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	cfg, _ := siteConfigStore.Get(webResourceID(r))
	props := cfg.ConnectionStrings
	if props == nil {
		props = map[string]AzureSiteConnStringValue{}
	}
	sim.WriteJSON(w, http.StatusOK, AzureSiteConnectionStrings{
		ID:         webResourceID(r) + "/config/connectionstrings",
		Name:       "connectionstrings",
		Type:       "Microsoft.Web/sites/config",
		Properties: props,
	})
}

func webSlotAzureStoragePut(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	var req AzureStoragePropertyDictionaryResource
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
		return
	}
	store := webResourceStore(r)
	row, _ := store.Get(webResourceID(r))
	row.AzureStorageAccounts = req.Properties
	store.Put(webResourceID(r), row)
	webWriteAzureStorage(w, webResourceID(r), req.Properties)
}

func webSlotAzureStorageList(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	row, _ := webResource(r)
	webWriteAzureStorage(w, webResourceID(r), row.AzureStorageAccounts)
}

func webWriteAzureStorage(w http.ResponseWriter, resID string, props map[string]*AzureStorageInfoValue) {
	if props == nil {
		props = map[string]*AzureStorageInfoValue{}
	}
	sim.WriteJSON(w, http.StatusOK, AzureStoragePropertyDictionaryResource{
		ID:         resID + "/config/azurestorageaccounts",
		Name:       "azurestorageaccounts",
		Type:       "Microsoft.Web/sites/config",
		Properties: props,
	})
}

// webPublishingCredentials returns the SCM publishing user/password
// (deterministic per resource ID, stable across reads, rotated by POST
// /newpassword). Secret-bearing, so only via the POST /list action — never
// echoed on a GET.
func webPublishingCredentials(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	name := sim.PathParam(r, "siteName")
	user := "$" + name
	password := webPublishingPassword(webResourceID(r))
	scmURI := fmt.Sprintf("https://%s:%s@%s.scm.azurewebsites.net", user, password, name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"id":   webResourceID(r) + "/config/publishingcredentials",
		"name": "publishingcredentials",
		"type": "Microsoft.Web/sites/config",
		"properties": map[string]any{
			"publishingUserName": user,
			"publishingPassword": password,
			"scmUri":             scmURI,
		},
	})
}

// --- Deployment-slot CRUD ----------------------------------------------------

func registerWebSlotCRUD(srv *sim.Server) {
	base := webProvider + "/sites/{siteName}"

	// GET /slots — list a site's deployment slots.
	srv.HandleFunc("GET "+base+"/slots", func(w http.ResponseWriter, r *http.Request) {
		siteID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "siteName"))
		if _, ok := azfSites.Get(siteID); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' was not found.", sim.PathParam(r, "siteName"))
			return
		}
		prefix := siteID + "/slots/"
		out := webSlots.Filter(func(s Site) bool { return strings.HasPrefix(s.ID, prefix) })
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	// GET /slots/{slot}
	srv.HandleFunc("GET "+base+"/slots/{slot}", func(w http.ResponseWriter, r *http.Request) {
		if webMissing(w, r) {
			return
		}
		row, _ := webResource(r)
		sim.WriteJSON(w, http.StatusOK, row)
	})

	// PUT /slots/{slot} — create or update a slot. The parent production site
	// must exist (real Azure requires it).
	srv.HandleFunc("PUT "+base+"/slots/{slot}", func(w http.ResponseWriter, r *http.Request) {
		siteID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "siteName"))
		if _, ok := azfSites.Get(siteID); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' was not found.", sim.PathParam(r, "siteName"))
			return
		}
		var req Site
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		name := sim.PathParam(r, "siteName")
		slot := sim.PathParam(r, "slot")
		resourceID := webResourceID(r)
		kind := req.Kind
		if kind == "" {
			kind = "functionapp"
		}
		host := name + "-" + slot + ".azurewebsites.net"
		siteConfig := req.Properties.SiteConfig
		if siteConfig == nil {
			siteConfig = &SiteConfig{}
		}
		slotSite := Site{
			ID:       resourceID,
			Name:     name + "/" + slot,
			Type:     "Microsoft.Web/sites/slots",
			Kind:     kind,
			Location: req.Location,
			Tags:     req.Tags,
			Properties: SiteProperties{
				State:            "Running",
				DefaultHostName:  host,
				HostNames:        []string{host},
				Enabled:          true,
				EnabledHostNames: []string{host, name + "-" + slot + ".scm.azurewebsites.net"},
				ServerFarmID:     req.Properties.ServerFarmID,
				SKU:              webPlanSKUFor(req.Properties.ServerFarmID),
				Reserved:         req.Properties.Reserved,
				SiteConfig:       siteConfig,
				ResourceGroup:    sim.PathParam(r, "resourceGroupName"),
				LastModifiedTime: time.Now().UTC().Format(time.RFC3339),
				HTTPSOnly:        req.Properties.HTTPSOnly,
			},
		}
		webSlots.Put(resourceID, slotSite)
		// A slot carries its own Functions host key set, like a real slot.
		ensureWebHostKeys(resourceID)
		// virtualNetworkSubnetId on the envelope is regional VNet integration
		// for the slot, exactly as on a production site; a PUT that omits it
		// keeps an existing integration, and the stored row reflects the
		// current integration either way.
		if req.Properties.VirtualNetworkSubnetID != "" {
			if err := applySiteVirtualNetworkSubnetID(r, slotSite, req.Properties.VirtualNetworkSubnetID); err != nil {
				sim.AzureErrorf(w, "InternalServerError", http.StatusInternalServerError,
					"failed to integrate slot %q into VNet: %v", slotSite.Name, err)
				return
			}
		} else {
			syncSiteVnetSubnetProperty(r)
		}
		slotSite, _ = webSlots.Get(resourceID)
		sim.WriteJSON(w, http.StatusOK, slotSite)
	})

	srv.HandleFunc("PATCH "+base+"/slots/{slot}", func(w http.ResponseWriter, r *http.Request) {
		patchWebSite(w, r, webSlots)
	})

	srv.HandleFunc("DELETE "+base+"/slots/{slot}", func(w http.ResponseWriter, r *http.Request) {
		if webSlots.Delete(webResourceID(r)) {
			webCleanupSiteResources(webResourceID(r))
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// --- App Service plan additions ---------------------------------------------

func registerAppServicePlanMore(srv *sim.Server) {
	planID := func(r *http.Request) string {
		return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/%s",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "planName"))
	}

	// GET serverfarms (list by subscription).
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/serverfarms", func(w http.ResponseWriter, r *http.Request) {
		prefix := "/subscriptions/" + sim.PathParam(r, "subscriptionId") + "/"
		out := azureAppServicePlans.Filter(func(p AppServicePlan) bool { return strings.HasPrefix(p.ID, prefix) })
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	// GET serverfarms (list by resource group) — lowercase spelling the
	// SDK's ListByResourceGroup pager sends.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms", func(w http.ResponseWriter, r *http.Request) {
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"))
		out := azureAppServicePlans.Filter(func(p AppServicePlan) bool { return strings.HasPrefix(p.ID, prefix) })
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	// PATCH serverfarms/{planName} — merge tags/sku.
	srv.HandleFunc("PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}", func(w http.ResponseWriter, r *http.Request) {
		id := planID(r)
		plan, ok := azureAppServicePlans.Get(id)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Server farm '%s' not found.", sim.PathParam(r, "planName"))
			return
		}
		var req AppServicePlan
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Tags != nil {
			plan.Tags = req.Tags
		}
		if req.Sku.Name != "" {
			plan.Sku = req.Sku
		}
		azureAppServicePlans.Put(id, plan)
		sim.WriteJSON(w, http.StatusOK, plan)
	})

	planExists := func(w http.ResponseWriter, r *http.Request) bool {
		if _, ok := azureAppServicePlans.Get(planID(r)); ok {
			return true
		}
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Server farm '%s' not found.", sim.PathParam(r, "planName"))
		return false
	}

	// GET serverfarms/{planName}/sites — the web apps assigned to a plan.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/sites", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		id := planID(r)
		out := azfSites.Filter(func(s Site) bool { return strings.EqualFold(s.Properties.ServerFarmID, id) })
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	// GET serverfarms/{planName}/usages — usage quotas (none in the sim).
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/usages", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
	})

	// GET serverfarms/{planName}/virtualNetworkConnections — VNet
	// integrations (response is a bare array of VnetInfoResource), assembled
	// from the connections the plan's sites actually have.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/virtualNetworkConnections", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		sim.WriteJSON(w, http.StatusOK, webPlanVnetConnections(planID(r)))
	})

	// POST serverfarms/{planName}/restartSites — restart every app on the plan.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}/restartSites", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// --- Subscription-global Microsoft.Web catalogs ------------------------------

func registerWebGlobal(srv *sim.Server) {
	// GET sites (list by subscription).
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/sites", func(w http.ResponseWriter, r *http.Request) {
		prefix := "/subscriptions/" + sim.PathParam(r, "subscriptionId") + "/"
		out := azfSites.Filter(func(s Site) bool { return strings.HasPrefix(s.ID, prefix) })
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	// GET geoRegions — supported regions (empty list is a valid response).
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/geoRegions", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
	})

	// GET skus — global SKU catalog.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/skus", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	// GET deploymentLocations — locations + hosting environments.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/deploymentLocations", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	// GET deletedSites — soft-deleted apps (none in the sim).
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/deletedSites", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
	})
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Web/locations/{location}/deletedSites", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
	})

	// POST locations/{location}/checknameavailability — regional name check.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.Web/locations/{location}/checknameavailability", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		suffix := "/providers/Microsoft.Web/sites/" + req.Name
		taken := len(azfSites.Filter(func(s Site) bool { return strings.HasSuffix(s.ID, suffix) })) > 0
		resp := map[string]any{"nameAvailable": !taken, "message": ""}
		if taken {
			resp["reason"] = "AlreadyExists"
			resp["message"] = fmt.Sprintf("Hostname '%s' already exists.", req.Name)
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
}
