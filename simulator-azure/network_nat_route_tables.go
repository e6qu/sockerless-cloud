package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/sidecar"
	"github.com/e6qu/sockerless-cloud/sim/sidecar/egress"
)

func registerNATGatewaysAndRouteTables(srv *sim.Server, armBase string) {
	// NAT gateways give subnets explicit egress, as Container Apps with VNet
	// integration needs; route tables override default routes.
	natGateways := sim.MakeStore[NatGateway](srv.DB(), "nat_gateways")
	routeTables := sim.MakeStore[RouteTable](srv.DB(), "route_tables")
	azureNatGateways = natGateways
	azureRouteTables = routeTables

	srv.HandleFunc("POST "+egress.ReportPath, sidecar.EgressReports(azureTakeNATEgressReport))
	srv.HandleFunc("PUT "+armBase+"/natGateways/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		var req NatGateway
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		resourceID := armResourceID(sub, rg, "Microsoft.Network", "natGateways", name)
		gw := NatGateway{
			ID:       resourceID,
			Name:     name,
			Type:     "Microsoft.Network/natGateways",
			Location: req.Location,
			Tags:     req.Tags,
			Sku:      req.Sku,
			Properties: NatGatewayProps{
				IdleTimeoutInMinutes: req.Properties.IdleTimeoutInMinutes,
				PublicIPAddresses:    req.Properties.PublicIPAddresses,
				PublicIPPrefixes:     req.Properties.PublicIPPrefixes,
				Subnets:              req.Properties.Subnets,
				ProvisioningState:    "Succeeded",
			},
		}
		if gw.Properties.IdleTimeoutInMinutes == 0 {
			gw.Properties.IdleTimeoutInMinutes = 4 // Azure's default
		}
		if gw.Sku == nil {
			gw.Sku = &SkuName{Name: "Standard"}
		}
		natGateways.Put(resourceID, gw)
		for _, ref := range gw.Properties.Subnets {
			// Store the association on the subnet, so the subnet's Get and
			// azureSubnetsForNatGateway both see it.
			azureSubnets.Update(ref.ID, func(sn *Subnet) {
				sn.Properties.NatGateway = &SubResource{ID: gw.ID}
			})
		}
		azureReapplyNAT()
		sim.WriteJSON(w, http.StatusOK, gw)
	})

	srv.HandleFunc("GET "+armBase+"/natGateways/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		resourceID := armResourceID(sub, rg, "Microsoft.Network", "natGateways", name)
		gw, ok := natGateways.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"NAT gateway %q not found.", name)
			return
		}
		gw.Properties.Subnets = azureSubnetsForNatGateway(gw.ID)
		sim.WriteJSON(w, http.StatusOK, gw)
	})

	srv.HandleFunc("GET "+armBase+"/natGateways", func(w http.ResponseWriter, r *http.Request) {
		prefix := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "natGateways") + "/"
		items := natGateways.Filter(func(gw NatGateway) bool {
			return strings.HasPrefix(gw.ID, prefix)
		})
		if items == nil {
			items = []NatGateway{}
		}
		for i := range items {
			items[i].Properties.Subnets = azureSubnetsForNatGateway(items[i].ID)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": items})
	})

	srv.HandleFunc("DELETE "+armBase+"/natGateways/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		resourceID := armResourceID(sub, rg, "Microsoft.Network", "natGateways", name)
		natGateways.Delete(resourceID)
		azureRemoveNATGatewayEgress(resourceID)
		azureReapplyRoutes()
		w.WriteHeader(http.StatusOK)
	})

	srv.HandleFunc("PUT "+armBase+"/routeTables/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		var req RouteTable
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		resourceID := armResourceID(sub, rg, "Microsoft.Network", "routeTables", name)
		rt := RouteTable{
			ID:       resourceID,
			Name:     name,
			Type:     "Microsoft.Network/routeTables",
			Location: req.Location,
			Tags:     req.Tags,
			Properties: RouteTableProps{
				DisableBgpRoutePropagation: req.Properties.DisableBgpRoutePropagation,
				Routes:                     normalizeRouteEntries(resourceID, req.Properties.Routes),
				ProvisioningState:          "Succeeded",
			},
		}
		routeTables.Put(resourceID, rt)
		sim.WriteJSON(w, http.StatusOK, rt)
	})

	srv.HandleFunc("GET "+armBase+"/routeTables/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		resourceID := armResourceID(sub, rg, "Microsoft.Network", "routeTables", name)
		rt, ok := routeTables.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Route table %q not found.", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, rt)
	})

	srv.HandleFunc("DELETE "+armBase+"/routeTables/{name}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "name")
		resourceID := armResourceID(sub, rg, "Microsoft.Network", "routeTables", name)
		routeTables.Delete(resourceID)
		w.WriteHeader(http.StatusOK)
	})
}
