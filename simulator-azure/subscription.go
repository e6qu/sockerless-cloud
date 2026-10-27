package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The namespaces the azurerm provider queries at initialization.
var resourceProviderNamespaces = []string{
	"Microsoft.ApiManagement",
	"Microsoft.Authorization",
	"Microsoft.Cache",
	"Microsoft.Compute",
	"Microsoft.ContainerRegistry",
	"Microsoft.ContainerService",
	"Microsoft.DBforPostgreSQL",
	"Microsoft.EventGrid",
	"Microsoft.EventHub",
	"Microsoft.Insights",
	"Microsoft.KeyVault",
	"Microsoft.ManagedIdentity",
	"Microsoft.App",
	"Microsoft.Network",
	"Microsoft.OperationalInsights",
	"Microsoft.Resources",
	"Microsoft.ServiceBus",
	"Microsoft.Storage",
	"Microsoft.Web",
}

var azureProviderResourceTypes = map[string][]string{
	"Microsoft.ApiManagement":       {"service"},
	"Microsoft.App":                 {"managedEnvironments", "containerApps", "jobs"},
	"Microsoft.Authorization":       {"roleAssignments"},
	"Microsoft.Cache":               {"Redis", "Redis/firewallRules"},
	"Microsoft.Compute":             {"virtualMachines", "virtualMachines/instanceView", "disks"},
	"Microsoft.ContainerRegistry":   {"registries"},
	"Microsoft.DBforPostgreSQL":     {"flexibleServers", "flexibleServers/databases", "flexibleServers/firewallRules", "flexibleServers/configurations"},
	"Microsoft.EventGrid":           {"topics", "domains", "domains/topics", "systemTopics", "partnerTopics", "eventSubscriptions"},
	"Microsoft.EventHub":            {"namespaces", "namespaces/eventhubs", "namespaces/eventhubs/consumerGroups", "namespaces/authorizationRules"},
	"Microsoft.Insights":            {"components"},
	"Microsoft.KeyVault":            {"vaults", "vaults/accessPolicies", "deletedVaults"},
	"Microsoft.ManagedIdentity":     {"userAssignedIdentities"},
	"Microsoft.Network":             {"virtualNetworks", "virtualNetworks/subnets", "networkInterfaces", "networkInterfaces/ipConfigurations", "publicIPAddresses", "publicIPPrefixes", "natGateways", "loadBalancers", "loadBalancers/frontendIPConfigurations", "loadBalancers/backendAddressPools", "loadBalancers/loadBalancingRules", "loadBalancers/probes", "networkSecurityGroups", "networkSecurityGroups/securityRules", "privateDnsZones", "dnsZones", "dnsZones/A", "dnsZones/AAAA", "dnsZones/CAA", "dnsZones/CNAME", "dnsZones/MX", "dnsZones/NS", "dnsZones/PTR", "dnsZones/SOA", "dnsZones/SRV", "dnsZones/TXT"},
	"Microsoft.OperationalInsights": {"workspaces"},
	"Microsoft.Resources":           {"resourceGroups", "resources", "providers"},
	"Microsoft.ServiceBus":          {"namespaces", "namespaces/queues", "namespaces/topics", "namespaces/topics/subscriptions", "namespaces/networkRuleSets", "namespaces/authorizationRules"},
	"Microsoft.Storage":             {"storageAccounts", "storageAccounts/blobServices", "storageAccounts/blobServices/containers", "storageAccounts/fileServices", "storageAccounts/fileServices/shares"},
	"Microsoft.Web":                 {"serverfarms", "sites"},
}

func registerSubscription(srv *sim.Server) {
	// data.azurerm_subscription reads this.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		sim.WriteJSON(w, http.StatusOK, azureSubscriptionObject(sub))
	})

	// azurerm populates its provider cache from this on init.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")

		// Built by the function the single-provider read uses, so both reads agree on
		// registration.
		providers := make([]map[string]any, 0, len(resourceProviderNamespaces))
		for _, ns := range resourceProviderNamespaces {
			providers = append(providers, azureProviderObject(sub, ns))
		}

		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": providers,
		})
	})

	// The default subscription, every subscription the Microsoft.Subscription API
	// created or changed, and any subscription owning a resource group.
	srv.HandleFunc("GET /subscriptions", func(w http.ResponseWriter, _ *http.Request) {
		seen := map[string]bool{azureDefaultSubscriptionID: true}
		out := []map[string]any{azureSubscriptionObject(azureDefaultSubscriptionID)}
		add := func(sub string) {
			if sub == "" || seen[sub] {
				return
			}
			seen[sub] = true
			out = append(out, azureSubscriptionObject(sub))
		}
		for _, rec := range azureSubscriptionRecords.List() {
			add(rec.SubscriptionID)
		}
		for _, sub := range azureSubscriptionIDsWithResourceGroups() {
			add(sub)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/locations", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": azureLocations(sub)})
	})

	srv.HandleFunc("GET /tenants", func(w http.ResponseWriter, _ *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": []map[string]any{{
				"id":            "/tenants/" + simTenantID,
				"tenantId":      simTenantID,
				"displayName":   azureTenantDisplayName(simTenantID),
				"defaultDomain": "simulator.onmicrosoft.com",
				"tenantType":    "AAD",
				"countryCode":   "US",
			}},
		})
	})

	// CheckResourceNameClient checks a name against reserved words.
	srv.HandleFunc("POST /providers/Microsoft.Resources/checkResourceName", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		status := "Allowed"
		if azureReservedResourceName(req.Name) {
			status = "Reserved"
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"name":   req.Name,
			"type":   req.Type,
			"status": status,
		})
	})

	// SubscriptionClient.CheckZonePeers; the SDK URL carries a trailing slash.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/Microsoft.Resources/checkZonePeers/", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		var req struct {
			Location        string   `json:"location"`
			SubscriptionIDs []string `json:"subscriptionIds"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		peers := make([]map[string]any, 0, len(req.SubscriptionIDs))
		for _, peer := range req.SubscriptionIDs {
			peers = append(peers, map[string]any{"availabilityZone": "1", "peers": []map[string]any{{"availabilityZone": "1", "subscriptionId": peer}}})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"subscriptionId":        sub,
			"location":              req.Location,
			"availabilityZonePeers": peers,
		})
	})
}

// Also the instance-metadata endpoints' subscription.
const azureDefaultSubscriptionID = "00000000-0000-0000-0000-000000000001"

// The simulator's own tenant is the one directory it holds; a tenant named in
// a cross-tenant request belongs to a directory it cannot read, so it has no
// name.
func azureTenantDisplayName(tenantID string) string {
	if tenantID == simTenantID {
		return "Simulator Tenant"
	}
	return ""
}

func azureSubscriptionIDsWithResourceGroups() []string {
	seen := map[string]bool{}
	var out []string
	for _, rg := range azureResourceGroups.List() {
		parts := strings.Split(strings.TrimPrefix(rg.ID, "/subscriptions/"), "/")
		if len(parts) == 0 || parts[0] == "" || seen[parts[0]] {
			continue
		}
		seen[parts[0]] = true
		out = append(out, parts[0])
	}
	return out
}

// The stored row when the Microsoft.Subscription API created or changed the
// subscription, the implicit identity otherwise. A row naming no tenant lives
// in the simulator's own; only a subscription created for, or handed to,
// another tenant names a different one.
func azureSubscriptionView(sub string) (displayName, state, tenantID string) {
	if rec, ok := azureSubscriptionRecords.Get(sub); ok {
		tenant := rec.TenantID
		if tenant == "" {
			tenant = simTenantID
		}
		return rec.DisplayName, rec.State, tenant
	}
	return "Simulator Subscription", "Enabled", simTenantID
}

func azureSubscriptionObject(sub string) map[string]any {
	displayName, state, tenantID := azureSubscriptionView(sub)
	return map[string]any{
		"id":             armSubscriptionID(sub),
		"subscriptionId": sub,
		"tenantId":       tenantID,
		"displayName":    displayName,
		"state":          state,
		"subscriptionPolicies": map[string]any{
			"locationPlacementId": "Internal_2014-09-01",
			"quotaId":             "Internal_2014-09-01",
			"spendingLimit":       "Off",
		},
	}
}

// The regions the provider resource-type entries advertise.
func azureLocations(sub string) []map[string]any {
	type region struct{ name, display, regional string }
	regions := []region{
		{"eastus", "East US", "(US) East US"},
		{"westeurope", "West Europe", "(Europe) West Europe"},
		{"westus", "West US", "(US) West US"},
	}
	out := make([]map[string]any, 0, len(regions))
	for _, rg := range regions {
		out = append(out, map[string]any{
			"id":                  armChildID(armSubscriptionID(sub), "locations", rg.name),
			"subscriptionId":      sub,
			"name":                rg.name,
			"displayName":         rg.display,
			"regionalDisplayName": rg.regional,
			"type":                "Region",
		})
	}
	return out
}

// The reserved set documented for the checkResourceName API.
func azureReservedResourceName(name string) bool {
	reserved := map[string]bool{
		"login": true, "microsoft": true, "azure": true, "windows": true,
		"xbox": true, "bing": true,
	}
	return reserved[strings.ToLower(name)]
}

func azureProviderResourceTypeEntries(namespace string) []map[string]any {
	names := azureProviderResourceTypes[namespace]
	entries := make([]map[string]any, 0, len(names))
	for _, name := range names {
		entries = append(entries, map[string]any{
			"resourceType": name,
			"locations":    []string{"eastus", "westeurope"},
			"apiVersions":  []string{"2024-11-01", "2024-04-01-preview", "2023-05-01", "2021-04-01"},
			"capabilities": "SupportsTags",
		})
	}
	return entries
}
