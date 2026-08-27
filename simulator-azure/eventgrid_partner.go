package main

import (
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Microsoft.EventGrid Partner Events control plane — partner registrations,
// partner namespaces (+ channels and SAS keys), partner configurations
// (the per-resource-group "default" singleton with partner authorization),
// and the tenant-level verified-partners catalog. These are the resources a
// partner publisher provisions to deliver events into a customer's partner
// topics.

var (
	eventGridPartnerRegistrations  sim.Store[EventGridTopic]
	eventGridPartnerNamespaces     sim.Store[EventGridTopic]
	eventGridPartnerChannels       sim.Store[EventGridTopic]
	eventGridPartnerConfigurations sim.Store[EventGridTopic]
)

func registerEventGridPartner(srv *sim.Server) {
	eventGridPartnerRegistrations = sim.MakeStore[EventGridTopic](srv.DB(), "eventgrid_partner_registrations")
	eventGridPartnerNamespaces = sim.MakeStore[EventGridTopic](srv.DB(), "eventgrid_partner_namespaces")
	eventGridPartnerChannels = sim.MakeStore[EventGridTopic](srv.DB(), "eventgrid_partner_channels")
	eventGridPartnerConfigurations = sim.MakeStore[EventGridTopic](srv.DB(), "eventgrid_partner_configurations")

	const regBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/partnerRegistrations"
	const nsBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/partnerNamespaces"
	const cfgBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/partnerConfigurations"

	// Partner registrations.
	srv.HandleFunc("PUT "+regBase+"/{partnerRegistrationName}", handleEventGridPutPartnerRegistration)
	srv.HandleFunc("GET "+regBase+"/{partnerRegistrationName}", handleEventGridGetPartnerRegistration)
	srv.HandleFunc("PATCH "+regBase+"/{partnerRegistrationName}", handleEventGridUpdatePartnerRegistration)
	srv.HandleFunc("DELETE "+regBase+"/{partnerRegistrationName}", handleEventGridDeletePartnerRegistration)
	srv.HandleFunc("GET "+regBase, handleEventGridListPartnerRegistrationsByRG)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/partnerRegistrations", handleEventGridListPartnerRegistrationsBySub)

	// Partner namespaces (+ keys).
	srv.HandleFunc("PUT "+nsBase+"/{partnerNamespaceName}", handleEventGridPutPartnerNamespace)
	srv.HandleFunc("GET "+nsBase+"/{partnerNamespaceName}", handleEventGridGetPartnerNamespace)
	srv.HandleFunc("PATCH "+nsBase+"/{partnerNamespaceName}", handleEventGridUpdatePartnerNamespace)
	srv.HandleFunc("DELETE "+nsBase+"/{partnerNamespaceName}", handleEventGridDeletePartnerNamespace)
	srv.HandleFunc("GET "+nsBase, handleEventGridListPartnerNamespacesByRG)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/partnerNamespaces", handleEventGridListPartnerNamespacesBySub)
	srv.HandleFunc("POST "+nsBase+"/{partnerNamespaceName}/listKeys", handleEventGridListPartnerNamespaceKeys)
	srv.HandleFunc("POST "+nsBase+"/{partnerNamespaceName}/regenerateKey", handleEventGridRegeneratePartnerNamespaceKey)

	// Channels under a partner namespace.
	srv.HandleFunc("PUT "+nsBase+"/{partnerNamespaceName}/channels/{channelName}", handleEventGridPutChannel)
	srv.HandleFunc("GET "+nsBase+"/{partnerNamespaceName}/channels/{channelName}", handleEventGridGetChannel)
	srv.HandleFunc("PATCH "+nsBase+"/{partnerNamespaceName}/channels/{channelName}", handleEventGridUpdateChannel)
	srv.HandleFunc("DELETE "+nsBase+"/{partnerNamespaceName}/channels/{channelName}", handleEventGridDeleteChannel)
	srv.HandleFunc("GET "+nsBase+"/{partnerNamespaceName}/channels", handleEventGridListChannels)
	srv.HandleFunc("POST "+nsBase+"/{partnerNamespaceName}/channels/{channelName}/getFullUrl", handleEventGridGetChannelFullURL)

	// Partner configurations (the per-resource-group "default" singleton).
	srv.HandleFunc("PUT "+cfgBase+"/default", handleEventGridPutPartnerConfiguration)
	srv.HandleFunc("GET "+cfgBase+"/default", handleEventGridGetPartnerConfiguration)
	srv.HandleFunc("PATCH "+cfgBase+"/default", handleEventGridUpdatePartnerConfiguration)
	srv.HandleFunc("DELETE "+cfgBase+"/default", handleEventGridDeletePartnerConfiguration)
	srv.HandleFunc("GET "+cfgBase, handleEventGridListPartnerConfigurationsByRG)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/partnerConfigurations", handleEventGridListPartnerConfigurationsBySub)
	srv.HandleFunc("POST "+cfgBase+"/default/authorizePartner", handleEventGridAuthorizePartner)
	srv.HandleFunc("POST "+cfgBase+"/default/unauthorizePartner", handleEventGridUnauthorizePartner)

	// Tenant-level verified partners catalog (read only).
	srv.HandleFunc("GET /providers/Microsoft.EventGrid/verifiedPartners", handleEventGridListVerifiedPartners)
	srv.HandleFunc("GET /providers/Microsoft.EventGrid/verifiedPartners/{verifiedPartnerName}", handleEventGridGetVerifiedPartner)
}

func eventGridPartnerRegistrationID(r *http.Request) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/partnerRegistrations/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "partnerRegistrationName"))
}

func eventGridPartnerNamespaceID(r *http.Request) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/partnerNamespaces/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "partnerNamespaceName"))
}

func eventGridChannelID(r *http.Request) string {
	return eventGridPartnerNamespaceID(r) + "/channels/" + sim.PathParam(r, "channelName")
}

func eventGridPartnerConfigurationID(r *http.Request) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/partnerConfigurations/default",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"))
}

func handleEventGridPutPartnerRegistration(w http.ResponseWriter, r *http.Request) {
	eventGridCreateARMResource(w, r, eventGridPartnerRegistrations, eventGridPartnerRegistrationID(r),
		sim.PathParam(r, "partnerRegistrationName"), "Microsoft.EventGrid/partnerRegistrations", func(props map[string]any) {
			props["provisioningState"] = "Succeeded"
			if _, ok := props["partnerRegistrationImmutableId"]; !ok {
				props["partnerRegistrationImmutableId"] = generateUUID()
			}
		})
}

func handleEventGridGetPartnerRegistration(w http.ResponseWriter, r *http.Request) {
	eventGridGetARMResource(w, eventGridPartnerRegistrations, eventGridPartnerRegistrationID(r), "partner registration")
}

func handleEventGridUpdatePartnerRegistration(w http.ResponseWriter, r *http.Request) {
	eventGridUpdateARMResource(w, r, eventGridPartnerRegistrations, eventGridPartnerRegistrationID(r), "partner registration")
}

func handleEventGridDeletePartnerRegistration(w http.ResponseWriter, r *http.Request) {
	eventGridDeleteARMResource(w, eventGridPartnerRegistrations, eventGridPartnerRegistrationID(r), "partner registration")
}

func handleEventGridListPartnerRegistrationsByRG(w http.ResponseWriter, r *http.Request) {
	eventGridListARMResources(w, eventGridPartnerRegistrations, fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/partnerRegistrations/",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName")))
}

func handleEventGridListPartnerRegistrationsBySub(w http.ResponseWriter, r *http.Request) {
	eventGridListARMResources(w, eventGridPartnerRegistrations, "/subscriptions/"+sim.PathParam(r, "subscriptionId")+"/resourceGroups/")
}

func handleEventGridPutPartnerNamespace(w http.ResponseWriter, r *http.Request) {
	eventGridCreateARMResource(w, r, eventGridPartnerNamespaces, eventGridPartnerNamespaceID(r),
		sim.PathParam(r, "partnerNamespaceName"), "Microsoft.EventGrid/partnerNamespaces", func(props map[string]any) {
			props["provisioningState"] = "Succeeded"
			if _, ok := props["endpoint"]; !ok {
				props["endpoint"] = fmt.Sprintf("%s://%s/api/events", azureRequestScheme(r), eventGridEndpointHost(r, sim.PathParam(r, "partnerNamespaceName")))
			}
		})
}

func handleEventGridGetPartnerNamespace(w http.ResponseWriter, r *http.Request) {
	eventGridGetARMResource(w, eventGridPartnerNamespaces, eventGridPartnerNamespaceID(r), "partner namespace")
}

func handleEventGridUpdatePartnerNamespace(w http.ResponseWriter, r *http.Request) {
	eventGridUpdateARMResource(w, r, eventGridPartnerNamespaces, eventGridPartnerNamespaceID(r), "partner namespace")
}

func handleEventGridDeletePartnerNamespace(w http.ResponseWriter, r *http.Request) {
	id := eventGridPartnerNamespaceID(r)
	if !eventGridPartnerNamespaces.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "partner namespace %q not found", id)
		return
	}
	for _, ch := range eventGridPartnerChannels.List() {
		if strings.HasPrefix(ch.ID, id+"/channels/") {
			eventGridPartnerChannels.Delete(ch.ID)
		}
	}
	eventGridDropKeyGens(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleEventGridListPartnerNamespacesByRG(w http.ResponseWriter, r *http.Request) {
	eventGridListARMResources(w, eventGridPartnerNamespaces, fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/partnerNamespaces/",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName")))
}

func handleEventGridListPartnerNamespacesBySub(w http.ResponseWriter, r *http.Request) {
	eventGridListARMResources(w, eventGridPartnerNamespaces, "/subscriptions/"+sim.PathParam(r, "subscriptionId")+"/resourceGroups/")
}

func handleEventGridListPartnerNamespaceKeys(w http.ResponseWriter, r *http.Request) {
	id := eventGridPartnerNamespaceID(r)
	if _, ok := eventGridPartnerNamespaces.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "partner namespace %q not found", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, eventGridListKeysResponse(id))
}

func handleEventGridRegeneratePartnerNamespaceKey(w http.ResponseWriter, r *http.Request) {
	eventGridRegenerateKeyResponse(w, r, eventGridPartnerNamespaces, eventGridPartnerNamespaceID(r), "partner namespace")
}

func handleEventGridPutChannel(w http.ResponseWriter, r *http.Request) {
	nsID := eventGridPartnerNamespaceID(r)
	if _, ok := eventGridPartnerNamespaces.Get(nsID); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "partner namespace %q not found", nsID)
		return
	}
	eventGridCreateARMResource(w, r, eventGridPartnerChannels, eventGridChannelID(r),
		sim.PathParam(r, "channelName"), "Microsoft.EventGrid/partnerNamespaces/channels", func(props map[string]any) {
			props["provisioningState"] = "Succeeded"
			if _, ok := props["readinessState"]; !ok {
				props["readinessState"] = "NeverActivated"
			}
		})
}

func handleEventGridGetChannel(w http.ResponseWriter, r *http.Request) {
	eventGridGetARMResource(w, eventGridPartnerChannels, eventGridChannelID(r), "channel")
}

func handleEventGridUpdateChannel(w http.ResponseWriter, r *http.Request) {
	eventGridUpdateARMResource(w, r, eventGridPartnerChannels, eventGridChannelID(r), "channel")
}

func handleEventGridDeleteChannel(w http.ResponseWriter, r *http.Request) {
	eventGridDeleteARMResource(w, eventGridPartnerChannels, eventGridChannelID(r), "channel")
}

func handleEventGridListChannels(w http.ResponseWriter, r *http.Request) {
	eventGridListARMResources(w, eventGridPartnerChannels, eventGridPartnerNamespaceID(r)+"/channels/")
}

func handleEventGridGetChannelFullURL(w http.ResponseWriter, r *http.Request) {
	ch, ok := eventGridPartnerChannels.Get(eventGridChannelID(r))
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "channel %q not found", eventGridChannelID(r))
		return
	}
	endpoint := ""
	if info, ok := ch.Properties["partnerTopicInfo"].(map[string]any); ok {
		endpoint, _ = info["endpoint"].(string)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"endpointUrl": endpoint})
}

func handleEventGridPutPartnerConfiguration(w http.ResponseWriter, r *http.Request) {
	eventGridCreateARMResource(w, r, eventGridPartnerConfigurations, eventGridPartnerConfigurationID(r),
		"default", "Microsoft.EventGrid/partnerConfigurations", func(props map[string]any) {
			props["provisioningState"] = "Succeeded"
		})
}

func handleEventGridGetPartnerConfiguration(w http.ResponseWriter, r *http.Request) {
	eventGridGetARMResource(w, eventGridPartnerConfigurations, eventGridPartnerConfigurationID(r), "partner configuration")
}

func handleEventGridUpdatePartnerConfiguration(w http.ResponseWriter, r *http.Request) {
	eventGridUpdateARMResource(w, r, eventGridPartnerConfigurations, eventGridPartnerConfigurationID(r), "partner configuration")
}

func handleEventGridDeletePartnerConfiguration(w http.ResponseWriter, r *http.Request) {
	eventGridDeleteARMResource(w, eventGridPartnerConfigurations, eventGridPartnerConfigurationID(r), "partner configuration")
}

func handleEventGridListPartnerConfigurationsByRG(w http.ResponseWriter, r *http.Request) {
	eventGridListARMResources(w, eventGridPartnerConfigurations, fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/partnerConfigurations/",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName")))
}

func handleEventGridListPartnerConfigurationsBySub(w http.ResponseWriter, r *http.Request) {
	eventGridListARMResources(w, eventGridPartnerConfigurations, "/subscriptions/"+sim.PathParam(r, "subscriptionId")+"/resourceGroups/")
}

// eventGridPartnerAuthorization returns the mutable partnerAuthorization
// container and its authorizedPartnersList, creating the container if absent.
func eventGridPartnerAuthorization(cfg *EventGridTopic) (map[string]any, []any) {
	if cfg.Properties == nil {
		cfg.Properties = map[string]any{}
	}
	auth, ok := cfg.Properties["partnerAuthorization"].(map[string]any)
	if !ok {
		auth = map[string]any{}
		cfg.Properties["partnerAuthorization"] = auth
	}
	list, _ := auth["authorizedPartnersList"].([]any)
	return auth, list
}

func handleEventGridAuthorizePartner(w http.ResponseWriter, r *http.Request) {
	id := eventGridPartnerConfigurationID(r)
	cfg, ok := eventGridPartnerConfigurations.Get(id)
	if !ok {
		// Authorizing a partner implicitly creates the default configuration.
		cfg = EventGridTopic{ID: id, Name: "default", Type: "Microsoft.EventGrid/partnerConfigurations", Properties: map[string]any{"provisioningState": "Succeeded"}}
	}
	var partner map[string]any
	if err := sim.ReadJSON(r, &partner); err != nil {
		sim.AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	auth, list := eventGridPartnerAuthorization(&cfg)
	auth["authorizedPartnersList"] = append(list, partner)
	eventGridPartnerConfigurations.Put(id, cfg)
	sim.WriteJSON(w, http.StatusOK, cfg)
}

func handleEventGridUnauthorizePartner(w http.ResponseWriter, r *http.Request) {
	id := eventGridPartnerConfigurationID(r)
	cfg, ok := eventGridPartnerConfigurations.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "partner configuration %q not found", id)
		return
	}
	var partner map[string]any
	if err := sim.ReadJSON(r, &partner); err != nil {
		sim.AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	auth, list := eventGridPartnerAuthorization(&cfg)
	kept := make([]any, 0, len(list))
	for _, item := range list {
		entry, _ := item.(map[string]any)
		if entry != nil && fmt.Sprint(entry["partnerRegistrationImmutableId"]) == fmt.Sprint(partner["partnerRegistrationImmutableId"]) &&
			fmt.Sprint(entry["partnerName"]) == fmt.Sprint(partner["partnerName"]) {
			continue
		}
		kept = append(kept, item)
	}
	auth["authorizedPartnersList"] = kept
	eventGridPartnerConfigurations.Put(id, cfg)
	sim.WriteJSON(w, http.StatusOK, cfg)
}

// eventGridVerifiedPartners is the Azure-curated catalog of verified Event Grid
// partners. Auth0 is a real verified partner; its registration immutable id is
// deterministic per the simulator so reads are stable.
func eventGridVerifiedPartners() []EventGridTopic {
	return []EventGridTopic{{
		ID:   "/providers/Microsoft.EventGrid/verifiedPartners/Auth0",
		Name: "Auth0",
		Type: "Microsoft.EventGrid/verifiedPartners",
		Properties: map[string]any{
			"partnerRegistrationImmutableId": "144cca58-00d8-4a64-a7f9-30421f0a249e",
			"organizationName":               "Auth0",
			"partnerDisplayName":             "Auth0",
			"provisioningState":              "Succeeded",
		},
	}}
}

func handleEventGridListVerifiedPartners(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": eventGridVerifiedPartners()})
}

func handleEventGridGetVerifiedPartner(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "verifiedPartnerName")
	for _, vp := range eventGridVerifiedPartners() {
		if strings.EqualFold(vp.Name, name) {
			sim.WriteJSON(w, http.StatusOK, vp)
			return
		}
	}
	sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "verified partner %q not found", name)
}

func eventGridDeleteARMResource(w http.ResponseWriter, store sim.Store[EventGridTopic], id, label string) {
	if !store.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "%s %q not found", label, id)
		return
	}
	w.WriteHeader(http.StatusOK)
}
