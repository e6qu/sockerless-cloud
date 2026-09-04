package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// web_plan_network.go serves the App Service plan networking and worker tail:
// the plan-level Virtual Network connection views, VNet routes and gateways,
// the plan-level hybrid connection views (assembled from the site-level
// hybrid connection stores in web_hybrid.go), the capability/SKU catalogs, and
// the worker-instance surface. Everything plan-level is a view over real
// state: sites on the plan hold the VNet connections and hybrid connections,
// and their containers are the plan's worker instances.

var webPlanVnetRoutes sim.Store[WebVnetRoute]

const webPlanBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}"

// webPlanID is the canonical (lowercase serverfarms segment) plan resource id
// for the addressed request — the spelling appserviceplan.go stores plans under.
func webPlanID(r *http.Request) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "planName"))
}

// canonicalServerFarmID rewrites the serverFarms path segment (either casing
// clients send) to the canonical lowercase spelling plan state is keyed by.
func canonicalServerFarmID(id string) string {
	const marker = "/providers/microsoft.web/serverfarms/"
	i := sim.CaseInsensitiveIndex(id, marker)
	if i < 0 {
		return id
	}
	return id[:i] + "/providers/Microsoft.Web/serverfarms/" + id[i+len(marker):]
}

// webPlanSites lists the sites and deployment slots assigned to a plan.
func webPlanSites(planID string) []Site {
	onPlan := func(s Site) bool {
		return strings.EqualFold(canonicalServerFarmID(s.Properties.ServerFarmID), planID)
	}
	out := azfSites.Filter(onPlan)
	out = append(out, webSlots.Filter(onPlan)...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// webPlanVnetConnections assembles the plan-level view of the VNet
// connections the plan's sites actually have, one entry per connection name.
func webPlanVnetConnections(planID string) []WebVnetConnection {
	byName := map[string]WebVnetConnection{}
	for _, site := range webPlanSites(planID) {
		for _, conn := range siteVnetConnections(site.ID) {
			if _, seen := byName[conn.Name]; seen {
				continue
			}
			c := conn
			c.ID = planID + "/virtualNetworkConnections/" + conn.Name
			c.Type = "Microsoft.Web/serverfarms/virtualNetworkConnections"
			c.Properties.Routes = planVnetRoutes(planID, conn.Name)
			byName[conn.Name] = c
		}
	}
	out := make([]WebVnetConnection, 0, len(byName))
	for _, c := range byName {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// planVnetRoutes lists the routes recorded for one plan VNet connection,
// sorted by id. serverFarmID accepts either casing clients send.
func planVnetRoutes(serverFarmID, vnetName string) []WebVnetRoute {
	if webPlanVnetRoutes == nil {
		return nil
	}
	prefix := canonicalServerFarmID(serverFarmID) + "/virtualNetworkConnections/" + vnetName + "/routes/"
	out := webPlanVnetRoutes.Filter(func(rt WebVnetRoute) bool { return strings.HasPrefix(rt.ID, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// webPlanHybridConnections assembles the plan-level view of the Service Bus
// relay hybrid connections configured on the plan's sites, one entry per
// namespace/relay pair.
func webPlanHybridConnections(planID string) []WebHybridConnection {
	byRelay := map[string]WebHybridConnection{}
	for _, site := range webPlanSites(planID) {
		for _, hc := range siteHybridConnections(site.ID) {
			key := strings.ToLower(hc.Properties.ServiceBusNamespace + "/" + hc.Properties.RelayName)
			if _, seen := byRelay[key]; seen {
				continue
			}
			c := hc
			c.ID = planID + "/hybridConnectionNamespaces/" + hc.Properties.ServiceBusNamespace +
				"/relays/" + hc.Properties.RelayName
			c.Type = "Microsoft.Web/serverfarms/hybridConnectionNamespaces/relays"
			byRelay[key] = c
		}
	}
	out := make([]WebHybridConnection, 0, len(byRelay))
	for _, c := range byRelay {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// webPlanWorker is one live worker instance of a plan: a running container of
// a site on the plan. Its instance name is the container's identity.
type webPlanWorker struct {
	site        Site
	containerID string
}

func (wk webPlanWorker) instanceName() string {
	if len(wk.containerID) > 12 {
		return wk.containerID[:12]
	}
	return wk.containerID
}

// webPlanWorkers lists the plan's live worker instances from real container
// state.
func webPlanWorkers(planID string) []webPlanWorker {
	var out []webPlanWorker
	for _, site := range webPlanSites(planID) {
		inst := azfInstanceFor(site.Name)
		inst.mu.Lock()
		cid := inst.containerID
		inst.mu.Unlock()
		if cid != "" && sim.ContainerRunning(cid) {
			out = append(out, webPlanWorker{site: site, containerID: cid})
		}
	}
	return out
}

func registerAppServicePlanNetworking(srv *sim.Server) {
	webPlanVnetRoutes = sim.MakeStore[WebVnetRoute](srv.DB(), "web_plan_vnet_routes")

	planExists := func(w http.ResponseWriter, r *http.Request) bool {
		if _, ok := azureAppServicePlans.Get(webPlanID(r)); ok {
			return true
		}
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Server farm '%s' not found.", sim.PathParam(r, "planName"))
		return false
	}
	planVnetConnID := func(r *http.Request) string {
		return webPlanID(r) + "/virtualNetworkConnections/" + sim.PathParam(r, "vnetName")
	}
	planVnetConn := func(r *http.Request) (WebVnetConnection, bool) {
		for _, c := range webPlanVnetConnections(webPlanID(r)) {
			if strings.EqualFold(c.Name, sim.PathParam(r, "vnetName")) {
				return c, true
			}
		}
		return WebVnetConnection{}, false
	}

	srv.HandleFunc("GET "+webPlanBase+"/virtualNetworkConnections/{vnetName}", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		conn, ok := planVnetConn(r)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Virtual network connection '%s' not found.", sim.PathParam(r, "vnetName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, conn)
	})

	srv.HandleFunc("GET "+webPlanBase+"/virtualNetworkConnections/{vnetName}/gateways/{gatewayName}", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		gw, ok := webVnetGateways.Get(planVnetConnID(r) + "/gateways/" + sim.PathParam(r, "gatewayName"))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Gateway '%s' not found on virtual network connection '%s'.",
				sim.PathParam(r, "gatewayName"), sim.PathParam(r, "vnetName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, gw)
	})
	srv.HandleFunc("PUT "+webPlanBase+"/virtualNetworkConnections/{vnetName}/gateways/{gatewayName}", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		if _, ok := planVnetConn(r); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Virtual network connection '%s' not found.", sim.PathParam(r, "vnetName"))
			return
		}
		writeVnetGateway(w, r, planVnetConnID(r), "Microsoft.Web/serverfarms/virtualNetworkConnections/gateways")
	})

	srv.HandleFunc("GET "+webPlanBase+"/virtualNetworkConnections/{vnetName}/routes", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		routes := planVnetRoutes(webPlanID(r), sim.PathParam(r, "vnetName"))
		if routes == nil {
			routes = []WebVnetRoute{}
		}
		sim.WriteJSON(w, http.StatusOK, routes)
	})
	// The contract returns an ARRAY for the single-route read.
	srv.HandleFunc("GET "+webPlanBase+"/virtualNetworkConnections/{vnetName}/routes/{routeName}", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		rt, ok := webPlanVnetRoutes.Get(planVnetConnID(r) + "/routes/" + sim.PathParam(r, "routeName"))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Route '%s' not found on virtual network connection '%s'.",
				sim.PathParam(r, "routeName"), sim.PathParam(r, "vnetName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, []WebVnetRoute{rt})
	})
	putRoute := func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		if _, ok := planVnetConn(r); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Virtual network connection '%s' not found.", sim.PathParam(r, "vnetName"))
			return
		}
		var req WebVnetRoute
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := planVnetConnID(r) + "/routes/" + sim.PathParam(r, "routeName")
		rt := req
		if existing, ok := webPlanVnetRoutes.Get(id); ok {
			if rt.Properties.StartAddress == "" {
				rt.Properties.StartAddress = existing.Properties.StartAddress
			}
			if rt.Properties.EndAddress == "" {
				rt.Properties.EndAddress = existing.Properties.EndAddress
			}
			if rt.Properties.RouteType == "" {
				rt.Properties.RouteType = existing.Properties.RouteType
			}
		}
		if rt.Properties.StartAddress == "" {
			sim.AzureError(w, "InvalidRequestContent", "startAddress is required.", http.StatusBadRequest)
			return
		}
		if rt.Properties.RouteType == "" {
			rt.Properties.RouteType = "STATIC"
		}
		rt.ID = id
		rt.Name = sim.PathParam(r, "routeName")
		rt.Type = "Microsoft.Web/serverfarms/virtualNetworkConnections/routes"
		webPlanVnetRoutes.Put(id, rt)
		sim.WriteJSON(w, http.StatusOK, rt)
	}
	srv.HandleFunc("PUT "+webPlanBase+"/virtualNetworkConnections/{vnetName}/routes/{routeName}", putRoute)
	srv.HandleFunc("PATCH "+webPlanBase+"/virtualNetworkConnections/{vnetName}/routes/{routeName}", putRoute)
	srv.HandleFunc("DELETE "+webPlanBase+"/virtualNetworkConnections/{vnetName}/routes/{routeName}", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		if !webPlanVnetRoutes.Delete(planVnetConnID(r) + "/routes/" + sim.PathParam(r, "routeName")) {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Route '%s' not found on virtual network connection '%s'.",
				sim.PathParam(r, "routeName"), sim.PathParam(r, "vnetName"))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	planRelay := func(r *http.Request) (WebHybridConnection, bool) {
		ns, relay := sim.PathParam(r, "namespaceName"), sim.PathParam(r, "relayName")
		for _, hc := range webPlanHybridConnections(webPlanID(r)) {
			if strings.EqualFold(hc.Properties.ServiceBusNamespace, ns) && strings.EqualFold(hc.Properties.RelayName, relay) {
				return hc, true
			}
		}
		return WebHybridConnection{}, false
	}

	srv.HandleFunc("GET "+webPlanBase+"/hybridConnectionRelays", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		conns := webPlanHybridConnections(webPlanID(r))
		out := make([]WebHybridConnection, 0, len(conns))
		for _, hc := range conns {
			out = append(out, stripSendKeyValue(hc))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	srv.HandleFunc("GET "+webPlanBase+"/hybridConnectionNamespaces/{namespaceName}/relays/{relayName}", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		hc, ok := planRelay(r)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Hybrid connection '%s/%s' not found.", sim.PathParam(r, "namespaceName"), sim.PathParam(r, "relayName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, stripSendKeyValue(hc))
	})

	// Plan-level delete removes the hybrid connection from every site on the
	// plan — the plan groups what its apps configured.
	srv.HandleFunc("DELETE "+webPlanBase+"/hybridConnectionNamespaces/{namespaceName}/relays/{relayName}", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		ns, relay := sim.PathParam(r, "namespaceName"), sim.PathParam(r, "relayName")
		deleted := false
		for _, site := range webPlanSites(webPlanID(r)) {
			for _, hc := range siteHybridConnections(site.ID) {
				if strings.EqualFold(hc.Properties.ServiceBusNamespace, ns) && strings.EqualFold(hc.Properties.RelayName, relay) {
					webHybridConnections.Delete(hc.ID)
					deleted = true
				}
			}
		}
		if !deleted {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Hybrid connection '%s/%s' not found.", ns, relay)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	srv.HandleFunc("POST "+webPlanBase+"/hybridConnectionNamespaces/{namespaceName}/relays/{relayName}/listKeys", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		hc, ok := planRelay(r)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Hybrid connection '%s/%s' not found.", sim.PathParam(r, "namespaceName"), sim.PathParam(r, "relayName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":   hc.ID,
			"name": hc.Properties.RelayName,
			"type": "Microsoft.Web/serverfarms/hybridConnectionNamespaces/relays",
			"properties": map[string]any{
				"sendKeyName":  hc.Properties.SendKeyName,
				"sendKeyValue": hc.Properties.SendKeyValue,
			},
		})
	})

	srv.HandleFunc("GET "+webPlanBase+"/hybridConnectionNamespaces/{namespaceName}/relays/{relayName}/sites", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		ns, relay := sim.PathParam(r, "namespaceName"), sim.PathParam(r, "relayName")
		apps := []string{}
		for _, site := range webPlanSites(webPlanID(r)) {
			for _, hc := range siteHybridConnections(site.ID) {
				if strings.EqualFold(hc.Properties.ServiceBusNamespace, ns) && strings.EqualFold(hc.Properties.RelayName, relay) {
					apps = append(apps, site.ID)
					break
				}
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": apps})
	})

	srv.HandleFunc("GET "+webPlanBase+"/hybridConnectionPlanLimits/limit", func(w http.ResponseWriter, r *http.Request) {
		plan, ok := azureAppServicePlans.Get(webPlanID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Server farm '%s' not found.", sim.PathParam(r, "planName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":   plan.ID + "/hybridConnectionPlanLimits/limit",
			"name": "limit",
			"type": "Microsoft.Web/serverfarms/hybridConnectionPlanLimits",
			"properties": map[string]any{
				"current": len(webPlanHybridConnections(plan.ID)),
				"maximum": hybridConnectionPlanLimit(plan.Sku.Tier),
			},
		})
	})

	srv.HandleFunc("GET "+webPlanBase+"/capabilities", func(w http.ResponseWriter, r *http.Request) {
		plan, ok := azureAppServicePlans.Get(webPlanID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Server farm '%s' not found.", sim.PathParam(r, "planName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, webPlanCapabilities(plan))
	})

	srv.HandleFunc("GET "+webPlanBase+"/skus", func(w http.ResponseWriter, r *http.Request) {
		plan, ok := azureAppServicePlans.Get(webPlanID(r))
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Server farm '%s' not found.", sim.PathParam(r, "planName"))
			return
		}
		capacity := plan.Sku.Capacity
		if capacity < 1 {
			capacity = 1
		}
		// The selectable-SKU catalog the simulator can vouch for: the SKU the
		// plan really runs, shaped as SkuInfos (resourceType + GlobalCsmSkuDescription
		// entries) like the subscription-global catalog.
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"resourceType": "Microsoft.Web/serverfarms",
			"skus": []map[string]any{{
				"name":   plan.Sku.Name,
				"tier":   plan.Sku.Tier,
				"size":   plan.Sku.Size,
				"family": plan.Sku.Family,
				"capacity": map[string]any{
					"minimum":   1,
					"maximum":   capacity,
					"default":   1,
					"scaleType": "Manual",
				},
				"locations":    []string{plan.Location},
				"capabilities": webPlanCapabilities(plan),
			}},
		})
	})

	// The plan's instance details come from real container state: sites on the
	// plan run real containers, each one a worker instance. A plan with no
	// running instances answers the honest empty set.
	srv.HandleFunc("POST "+webPlanBase+"/listinstances", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		workers := webPlanWorkers(webPlanID(r))
		instances := make([]map[string]any, 0, len(workers))
		for _, wk := range workers {
			entry := map[string]any{
				"instanceName": wk.instanceName(),
				"status":       "Ready",
			}
			if ip := sim.ContainerIPv4(wk.containerID); ip != "" {
				entry["ipAddress"] = ip
			}
			instances = append(instances, entry)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"serverFarmName": sim.PathParam(r, "planName"),
			"instances":      instances,
			"instanceCount":  len(instances),
		})
	})

	rebootWorker := func(w http.ResponseWriter, r *http.Request) (webPlanWorker, bool) {
		name := sim.PathParam(r, "workerName")
		for _, wk := range webPlanWorkers(webPlanID(r)) {
			if !strings.EqualFold(wk.instanceName(), name) {
				continue
			}
			// A worker reboot is the real thing: the container is torn down;
			// the site's next invoke starts a fresh one on its recorded
			// VNet-integration networks.
			inst := azfInstanceFor(wk.site.Name)
			inst.mu.Lock()
			inst.teardownLocked()
			inst.mu.Unlock()
			return wk, true
		}
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Worker '%s' not found on server farm '%s'.", name, sim.PathParam(r, "planName"))
		return webPlanWorker{}, false
	}

	srv.HandleFunc("POST "+webPlanBase+"/workers/{workerName}/reboot", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		if _, ok := rebootWorker(w, r); !ok {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv.HandleFunc("POST "+webPlanBase+"/workers/{workerName}/recycleinstance", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		if _, ok := rebootWorker(w, r); !ok {
			return
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":           generateUUID(),
			"name":         "RecycleManagedInstanceWorker",
			"status":       "Succeeded",
			"createdTime":  now,
			"modifiedTime": now,
		})
	})

	// The simulator's plan workers are Linux containers; there is no Windows
	// Container worker to hold an RDP session, so the operation reports that
	// truthfully rather than minting a password nothing accepts.
	srv.HandleFunc("POST "+webPlanBase+"/getrdppassword", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		sim.AzureError(w, "BadRequest",
			"RDP is only available on Windows Container App Service plans; this plan runs Linux workers.",
			http.StatusBadRequest)
	})
}

// hybridConnectionPlanLimit is the per-plan hybrid connection quota real App
// Service enforces by tier (Basic 5, Standard 25, Premium and Isolated 220);
// tiers without hybrid connection support answer 0.
func hybridConnectionPlanLimit(tier string) int {
	switch {
	case strings.EqualFold(tier, "Basic"):
		return 5
	case strings.EqualFold(tier, "Standard"):
		return 25
	case strings.Contains(strings.ToLower(tier), "premium"), strings.Contains(strings.ToLower(tier), "isolated"):
		return 220
	default:
		return 0
	}
}

// webPlanCapabilities reports the capabilities the simulator's plans really
// have: whether workers are Linux (the plan's reserved flag), the always-on
// persistent site container, and regional VNet integration.
func webPlanCapabilities(plan AppServicePlan) []map[string]any {
	return []map[string]any{
		{"name": "LinuxWorkers", "value": fmt.Sprintf("%t", plan.Properties.Reserved)},
		{"name": "AlwaysOn", "value": "true"},
		{"name": "VnetIntegration", "value": "true"},
	}
}
