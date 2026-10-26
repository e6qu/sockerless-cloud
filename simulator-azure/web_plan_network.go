package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// App Service plan networking and workers: plan VNet connection views, routes
// and gateways, hybrid connection views (from web_hybrid.go's site stores),
// capability and SKU catalogs, and worker instances. Every plan view reads real
// state: the plan's sites hold the connections, and their containers are its
// workers.

var webPlanVnetRoutes sim.Store[WebVnetRoute]

const webPlanBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/serverfarms/{planName}"

// The lowercase-serverfarms spelling appserviceplan.go stores plans under.
func webPlanID(r *http.Request) string {
	return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Web", "serverfarms", sim.PathParam(r, "planName"))
}

func canonicalServerFarmID(id string) string {
	const marker = "/providers/microsoft.web/serverfarms/"
	i := sim.CaseInsensitiveIndex(id, marker)
	if i < 0 {
		return id
	}
	return id[:i] + "/providers/Microsoft.Web/serverfarms/" + id[i+len(marker):]
}

func webPlanSites(planID string) []Site {
	onPlan := func(s Site) bool {
		return strings.EqualFold(canonicalServerFarmID(s.Properties.ServerFarmID), planID)
	}
	out := azfSites.Filter(onPlan)
	out = append(out, webSlots.Filter(onPlan)...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// One entry per connection name.
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

// Sorted by ID; serverFarmID may be in either casing.
func planVnetRoutes(serverFarmID, vnetName string) []WebVnetRoute {
	if webPlanVnetRoutes == nil {
		return nil
	}
	prefix := canonicalServerFarmID(serverFarmID) + "/virtualNetworkConnections/" + vnetName + "/routes/"
	out := webPlanVnetRoutes.Filter(func(rt WebVnetRoute) bool { return strings.HasPrefix(rt.ID, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// One entry per namespace/relay pair.
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

// A running container of a site on the plan, named by its identity.
type webPlanWorker struct {
	site   Site
	handle placement.Handle
}

func (wk webPlanWorker) containerID() string { return wk.handle.ID() }

func (wk webPlanWorker) instanceName() string { return webInstanceName(wk.containerID()) }

// The document holds instance names to ^[a-zA-Z0-9]+$ and a guest's identity
// carries hyphens, so the name is a digest.
func webInstanceName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:6])
}

func webPlanWorkers(planID string) []webPlanWorker {
	var out []webPlanWorker
	for _, site := range webPlanSites(planID) {
		inst := azfInstanceFor(site.Name)
		_, handle := inst.running()
		if handle == nil {
			continue
		}
		select {
		case <-handle.Exited():
		default:
			out = append(out, webPlanWorker{site: site, handle: handle})
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
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
	// The contract returns an array for the single-route read.
	srv.HandleFunc("GET "+webPlanBase+"/virtualNetworkConnections/{vnetName}/routes/{routeName}", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		rt, ok := webPlanVnetRoutes.Get(planVnetConnID(r) + "/routes/" + sim.PathParam(r, "routeName"))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Virtual network connection '%s' not found.", sim.PathParam(r, "vnetName"))
			return
		}
		var req WebVnetRoute
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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
			AzureError(w, "InvalidRequestContent", "startAddress is required.", http.StatusBadRequest)
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Hybrid connection '%s/%s' not found.", sim.PathParam(r, "namespaceName"), sim.PathParam(r, "relayName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, stripSendKeyValue(hc))
	})

	// The plan groups what its apps configured, so remove the connection from
	// every site on it.
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Server farm '%s' not found.", sim.PathParam(r, "planName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, webPlanCapabilities(plan))
	})

	srv.HandleFunc("GET "+webPlanBase+"/skus", func(w http.ResponseWriter, r *http.Request) {
		plan, ok := azureAppServicePlans.Get(webPlanID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Server farm '%s' not found.", sim.PathParam(r, "planName"))
			return
		}
		capacity := plan.Sku.Capacity
		if capacity < 1 {
			capacity = 1
		}
		// The SKU the plan runs, as SkuInfos like the subscription-global catalog.
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

	// Each running container of a site on the plan is a worker instance; a plan
	// with none answers the empty set.
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
			if ip, err := wk.handle.Address(r.Context()); err == nil && ip != "" {
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
			// Tear the container down; the site's next invoke starts a fresh one on its
			// VNet-integration networks.
			inst := azfInstanceFor(wk.site.Name)
			inst.teardown()
			return wk, true
		}
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
			"id":           sim.NewUUID(),
			"name":         "RecycleManagedInstanceWorker",
			"status":       "Succeeded",
			"createdTime":  now,
			"modifiedTime": now,
		})
	})

	// The workers are Linux containers with no RDP session, so report that rather
	// than mint a password nothing accepts.
	srv.HandleFunc("POST "+webPlanBase+"/getrdppassword", func(w http.ResponseWriter, r *http.Request) {
		if !planExists(w, r) {
			return
		}
		AzureError(w, "BadRequest",
			"RDP is only available on Windows Container App Service plans; this plan runs Linux workers.",
			http.StatusBadRequest)
	})
}

// App Service's per-plan quota by tier: Basic 5, Standard 25, Premium and
// Isolated 220; other tiers 0.
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

// Linux workers (the reserved flag), the always-on site container, and
// regional VNet integration.
func webPlanCapabilities(plan AppServicePlan) []map[string]any {
	return []map[string]any{
		{"name": "LinuxWorkers", "value": fmt.Sprintf("%t", plan.Properties.Reserved)},
		{"name": "AlwaysOn", "value": "true"},
		{"name": "VnetIntegration", "value": "true"},
	}
}
