package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

type ResourceGroup struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags,omitempty"`
	Properties struct {
		ProvisioningState string `json:"provisioningState"`
	} `json:"properties"`
}

// azureResourceGroups is the shared Microsoft.Resources/resourceGroups store,
// read by the resources-ARM PATCH/exportTemplate handlers in resourcesarm.go.
var azureResourceGroups sim.Store[ResourceGroup]

func registerResourceGroups(srv *sim.Server) {
	resourceGroups := sim.MakeStore[ResourceGroup](srv.DB(), "resource_groups")
	azureResourceGroups = resourceGroups

	// PUT - Create or update resource group
	srv.HandleFunc("PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rgName := sim.PathParam(r, "resourceGroupName")

		var req struct {
			Location string            `json:"location"`
			Tags     map[string]string `json:"tags,omitempty"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", sub, rgName)

		_, exists := resourceGroups.Get(resourceID)

		rg := ResourceGroup{
			ID:       resourceID,
			Name:     rgName,
			Type:     "Microsoft.Resources/resourceGroups",
			Location: req.Location,
			Tags:     req.Tags,
		}
		rg.Properties.ProvisioningState = "Succeeded"
		resourceGroups.Put(resourceID, rg)

		if exists {
			sim.WriteJSON(w, http.StatusOK, rg)
		} else {
			sim.WriteJSON(w, http.StatusCreated, rg)
		}
	})

	// GET - Get resource group
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rgName := sim.PathParam(r, "resourceGroupName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", sub, rgName)

		rg, ok := resourceGroups.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceGroupNotFound", http.StatusNotFound,
				"Resource group '%s' could not be found.", rgName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, rg)
	})

	// GET - List resource groups
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/", sub)
		all := resourceGroups.Filter(func(rg ResourceGroup) bool {
			return strings.HasPrefix(rg.ID, prefix)
		})
		if all == nil {
			all = []ResourceGroup{}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
	})

	// DELETE - Delete resource group
	srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rgName := sim.PathParam(r, "resourceGroupName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", sub, rgName)

		resourceGroups.Delete(resourceID)
		w.WriteHeader(http.StatusOK)
	})

	// GET - List resources in resource group (used by azurerm provider during destroy)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/resources", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		values := azureResourcesInResourceGroup(sub, rg)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": values,
		})
	})

	// GET - List resources in subscription. terraform-provider-azurerm
	// uses this to populate per-subscription caches (e.g. resolving a
	// Key Vault URL → resource ID for azurerm_key_vault_secret on
	// every plan refresh). Real Azure supports a `$filter` query but
	// the sim returns every Key Vault in the subscription regardless
	// — terraform-provider-azurerm's KV-cache logic filters client-
	// side by `properties.vaultUri`, so the broader list is harmless.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resources", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		prefix := fmt.Sprintf("/subscriptions/%s/", sub)
		vaults := keyVaults.Filter(func(v KeyVault) bool {
			return strings.HasPrefix(v.ID, prefix)
		})
		values := make([]any, 0, len(vaults))
		for _, v := range vaults {
			values = append(values, v)
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": values})
	})

	// HEAD - Check resource group existence
	srv.HandleFunc("HEAD /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rgName := sim.PathParam(r, "resourceGroupName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", sub, rgName)

		if _, ok := resourceGroups.Get(resourceID); ok {
			w.WriteHeader(http.StatusNoContent)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func azureResourceIDPrefix(sub, rg string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/", sub, rg)
}

func appendAzureResource[T any](values []any, store sim.Store[T], include func(T) (any, string, bool)) []any {
	if store == nil {
		return values
	}
	for _, item := range store.List() {
		projected, _, ok := include(item)
		if ok {
			values = append(values, projected)
		}
	}
	return values
}

func azureResourcesInResourceGroup(sub, rg string) []any {
	prefix := azureResourceIDPrefix(sub, rg)
	values := make([]any, 0)
	matchID := func(id string) bool { return strings.HasPrefix(id, prefix) }

	values = appendAzureResource(values, azStorageAccounts, func(v StorageAccount) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, keyVaults, func(v KeyVault) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, acrRegistries, func(v Registry) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azureVnets, func(v VirtualNetwork) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azureSubnets, func(v Subnet) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azureNSGs, func(v NetworkSecurityGroup) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azureNatGateways, func(v NatGateway) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azureRouteTables, func(v RouteTable) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azurePublicIPs, func(v PublicIPAddress) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azurePublicIPPrefixes, func(v PublicIPPrefix) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azureNICs, func(v NetworkInterface) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azureVMs, func(v VirtualMachine) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, sbNamespaces, func(v SBNamespace) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, sbQueues, func(v SBQueue) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, sbTopics, func(v SBTopic) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, ehNamespaces, func(v EHNamespace) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, ehEventHubs, func(v EHEventHub) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, eventGridTopics, func(v EventGridTopic) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, eventGridDomains, func(v EventGridTopic) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, eventGridSystemTopics, func(v EventGridTopic) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, redisCaches, func(v RedisCache) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, pgServers, func(v PGFlexibleServer) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, apimServices, func(v APIMService) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azureMonitorWorkspaces, func(v Workspace) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azureAppInsightsComponents, func(v AppInsightsComponent) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azurePrivateDNSZones, func(v PrivateDnsZone) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azurePrivateDNSRecordSets, func(v RecordSet) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azurePrivateDNSVNetLinks, func(v VNetLink) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azurePublicDNSZones, func(v PublicDnsZone) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azurePublicDNSRecordSets, func(v PublicRecordSet) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, acaEnvironments, func(v ContainerAppEnvironment) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, acaApps, func(v ContainerApp) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, acaJobs, func(v ContainerAppJob) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, acaEnvStorages, func(v ManagedEnvironmentStorage) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azureAppServicePlans, func(v AppServicePlan) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azfSites, func(v Site) (any, string, bool) {
		return v, v.ID, matchID(v.ID)
	})
	values = appendAzureResource(values, azureRoleAssignments, func(v RoleAssignment) (any, string, bool) {
		return v, v.ID, strings.Contains(v.ID, "/resourceGroups/"+rg+"/")
	})

	sort.Slice(values, func(i, j int) bool {
		return azureResourceID(values[i]) < azureResourceID(values[j])
	})
	return values
}

func azureResourceID(v any) string {
	if m, ok := v.(map[string]any); ok {
		if id, ok := m["id"].(string); ok {
			return id
		}
	}
	switch r := v.(type) {
	case StorageAccount:
		return r.ID
	case KeyVault:
		return r.ID
	case Registry:
		return r.ID
	case VirtualNetwork:
		return r.ID
	case Subnet:
		return r.ID
	case NetworkSecurityGroup:
		return r.ID
	case NatGateway:
		return r.ID
	case RouteTable:
		return r.ID
	case PublicIPAddress:
		return r.ID
	case PublicIPPrefix:
		return r.ID
	case NetworkInterface:
		return r.ID
	case VirtualMachine:
		return r.ID
	case SBNamespace:
		return r.ID
	case SBQueue:
		return r.ID
	case SBTopic:
		return r.ID
	case EHNamespace:
		return r.ID
	case EHEventHub:
		return r.ID
	case EventGridTopic:
		return r.ID
	case RedisCache:
		return r.ID
	case PGFlexibleServer:
		return r.ID
	case APIMService:
		return r.ID
	case Workspace:
		return r.ID
	case AppInsightsComponent:
		return r.ID
	case PrivateDnsZone:
		return r.ID
	case RecordSet:
		return r.ID
	case VNetLink:
		return r.ID
	case ContainerAppEnvironment:
		return r.ID
	case ContainerApp:
		return r.ID
	case ContainerAppJob:
		return r.ID
	case ManagedEnvironmentStorage:
		return r.ID
	case AppServicePlan:
		return r.ID
	case Site:
		return r.ID
	case RoleAssignment:
		return r.ID
	default:
		return ""
	}
}
