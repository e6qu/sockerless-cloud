package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// eventGridKeySlots are the two access-key slots every Event Grid publishing
// resource — a custom topic, a domain, a partner namespace — serves through
// listKeys and rotates through regenerateKey.
var eventGridKeySlots = []string{"key1", "key2"}

// eventGridKeyMaterial returns the current access key for a resource's key
// slot. Event Grid's keys live in the rotation store every other Azure key
// surface shares (key_rotation.go), so a rotation and a cross-resource-group
// key pin behave identically here and on Storage, Service Bus, Event Hubs,
// Azure Cache for Redis and Azure Container Registry.
func eventGridKeyMaterial(id, keyName string) string {
	return azureKeyMaterial32(id, keyName)
}

// eventGridListKeysResponse is the {key1,key2} body listKeys and
// regenerateKey both return, reflecting every rotation performed so far.
func eventGridListKeysResponse(id string) map[string]string {
	return map[string]string{
		"key1": eventGridKeyMaterial(id, "key1"),
		"key2": eventGridKeyMaterial(id, "key2"),
	}
}

// eventGridDropKeyGens removes a deleted resource's rotation state so a later
// resource created under the same id starts from fresh key material.
func eventGridDropKeyGens(id string) {
	azureDropKeyGens(id, eventGridKeySlots...)
}

// Microsoft.EventGrid ARM control plane — operations, topic types, resource
// updates (PATCH), key regeneration, the per-scope EventSubscription
// extensions (update / getFullUrl / getDeliveryAttributes) and the
// subscription-, resource-group-, regional- and topic-type-scoped event
// subscription list views, plus private link resources / private endpoint
// connections. These extend the core CRUD in eventgrid.go.

var eventGridPrivateEndpointConnections sim.Store[EventGridTopic]

func registerEventGridMore(srv *sim.Server) {
	eventGridPrivateEndpointConnections = sim.MakeStore[EventGridTopic](srv.DB(), "eventgrid_private_endpoint_connections")

	const topicsBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/topics"
	const domainsBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/domains"
	const systemTopicsBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/systemTopics"
	const partnerTopicsBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/partnerTopics"

	// Provider operations + topic types (tenant-level read-only registries).
	srv.HandleFunc("GET /providers/Microsoft.EventGrid/operations", handleEventGridListOperations)
	srv.HandleFunc("GET /providers/Microsoft.EventGrid/topicTypes", handleEventGridListTopicTypes)
	srv.HandleFunc("GET /providers/Microsoft.EventGrid/topicTypes/{topicTypeName}", handleEventGridGetTopicType)
	srv.HandleFunc("GET /providers/Microsoft.EventGrid/topicTypes/{topicTypeName}/eventTypes", handleEventGridListTopicTypeEventTypes)

	// Resource updates (PATCH) and key regeneration.
	srv.HandleFunc("PATCH "+topicsBase+"/{topicName}", handleEventGridUpdateTopic)
	srv.HandleFunc("POST "+topicsBase+"/{topicName}/regenerateKey", handleEventGridRegenerateTopicKey)
	srv.HandleFunc("PATCH "+domainsBase+"/{domainName}", handleEventGridUpdateDomain)
	srv.HandleFunc("POST "+domainsBase+"/{domainName}/regenerateKey", handleEventGridRegenerateDomainKey)
	srv.HandleFunc("PATCH "+systemTopicsBase+"/{systemTopicName}", handleEventGridUpdateSystemTopic)

	// Event-subscription extensions, registered on every scope. PATCH covers
	// the per-resource *EventSubscriptions update groups and — by the
	// route-validity matcher's scope expansion — the global {scope}
	// EventSubscriptions_Update too. getFullUrl / getDeliveryAttributes the
	// same.
	registerEventGridSubscriptionExtras(srv, topicsBase+"/{topicName}/providers/Microsoft.EventGrid/eventSubscriptions/{eventSubscriptionName}")
	registerEventGridSubscriptionExtras(srv, domainsBase+"/{domainName}/topics/{domainTopicName}/providers/Microsoft.EventGrid/eventSubscriptions/{eventSubscriptionName}")
	registerEventGridSubscriptionExtras(srv, systemTopicsBase+"/{systemTopicName}/eventSubscriptions/{eventSubscriptionName}")
	registerEventGridSubscriptionExtras(srv, partnerTopicsBase+"/{partnerTopicName}/providers/Microsoft.EventGrid/eventSubscriptions/{eventSubscriptionName}")

	// The 2022-06-15 per-resource EventSubscriptions groups address the
	// subscription through the nested (non-provider) URL form. Full CRUD on
	// each scope.
	registerEventGridNestedSubscription(srv, topicsBase+"/{topicName}/eventSubscriptions/{eventSubscriptionName}")
	registerEventGridNestedSubscription(srv, domainsBase+"/{domainName}/eventSubscriptions/{eventSubscriptionName}")
	registerEventGridNestedSubscription(srv, domainsBase+"/{domainName}/topics/{domainTopicName}/eventSubscriptions/{eventSubscriptionName}")
	registerEventGridNestedSubscription(srv, partnerTopicsBase+"/{partnerTopicName}/eventSubscriptions/{eventSubscriptionName}")
	// The topics nested-list (topics/{topicName}/eventSubscriptions) is already
	// registered in registerEventGrid; the remaining scopes add theirs here.
	srv.HandleFunc("GET "+domainsBase+"/{domainName}/eventSubscriptions", handleEventGridListEventSubscriptions)
	srv.HandleFunc("GET "+domainsBase+"/{domainName}/topics/{domainTopicName}/eventSubscriptions", handleEventGridListEventSubscriptions)
	srv.HandleFunc("GET "+partnerTopicsBase+"/{partnerTopicName}/eventSubscriptions", handleEventGridListEventSubscriptions)

	// Subscription / resource-group / regional / topic-type-scoped list views
	// (the EventSubscriptions list pagers).
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/eventSubscriptions", handleEventGridListSubsGlobal)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/eventSubscriptions", handleEventGridListSubsGlobal)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/locations/{location}/eventSubscriptions", handleEventGridListSubsRegional)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/locations/{location}/eventSubscriptions", handleEventGridListSubsRegional)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/topicTypes/{topicTypeName}/eventSubscriptions", handleEventGridListSubsForTopicType)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/topicTypes/{topicTypeName}/eventSubscriptions", handleEventGridListSubsForTopicType)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/locations/{location}/topicTypes/{topicTypeName}/eventSubscriptions", handleEventGridListSubsForTopicType)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/locations/{location}/topicTypes/{topicTypeName}/eventSubscriptions", handleEventGridListSubsForTopicType)

	// Event subscriptions + event types under an arbitrary Azure resource
	// (ListByResource).
	const byResourceBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{providerNamespace}/{resourceTypeName}/{resourceName}/providers/Microsoft.EventGrid"
	srv.HandleFunc("GET "+byResourceBase+"/eventSubscriptions", handleEventGridListSubsByResource)
	srv.HandleFunc("GET "+byResourceBase+"/eventTypes", handleEventGridListResourceEventTypes)

	// Private link resources + private endpoint connections, mounted under an
	// arbitrary parent (topics / domains / partnerNamespaces).
	const parentBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/{parentType}/{parentName}"
	srv.HandleFunc("GET "+parentBase+"/privateLinkResources", handleEventGridListPrivateLinkResources)
	srv.HandleFunc("GET "+parentBase+"/privateLinkResources/{privateLinkResourceName}", handleEventGridGetPrivateLinkResource)
	srv.HandleFunc("GET "+parentBase+"/privateEndpointConnections", handleEventGridListPrivateEndpointConnections)
	srv.HandleFunc("GET "+parentBase+"/privateEndpointConnections/{privateEndpointConnectionName}", handleEventGridGetPrivateEndpointConnection)
	srv.HandleFunc("PUT "+parentBase+"/privateEndpointConnections/{privateEndpointConnectionName}", handleEventGridPutPrivateEndpointConnection)
	srv.HandleFunc("DELETE "+parentBase+"/privateEndpointConnections/{privateEndpointConnectionName}", handleEventGridDeletePrivateEndpointConnection)
}

func registerEventGridSubscriptionExtras(srv *sim.Server, base string) {
	srv.HandleFunc("PATCH "+base, handleEventGridUpdateEventSubscription)
	srv.HandleFunc("POST "+base+"/getFullUrl", handleEventGridGetEventSubscriptionFullURL)
	srv.HandleFunc("POST "+base+"/getDeliveryAttributes", handleEventGridGetEventSubscriptionDeliveryAttributes)
}

func registerEventGridNestedSubscription(srv *sim.Server, base string) {
	srv.HandleFunc("PUT "+base, handleEventGridCreateEventSubscription)
	srv.HandleFunc("GET "+base, handleEventGridGetEventSubscription)
	srv.HandleFunc("DELETE "+base, handleEventGridDeleteEventSubscription)
	registerEventGridSubscriptionExtras(srv, base)
}

// eventGridMergeUpdate applies a TrackedResource-shaped PATCH (tags +
// properties) onto an existing resource, preserving fields the request omits.
func eventGridUpdateARMResource(w http.ResponseWriter, r *http.Request, store sim.Store[EventGridTopic], id, label string) {
	existing, ok := store.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "%s %q not found", label, id)
		return
	}
	var req EventGridTopic
	if err := sim.ReadJSON(r, &req); err != nil && r.ContentLength != 0 {
		AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if req.Tags != nil {
		existing.Tags = req.Tags
	}
	if req.Properties != nil {
		if existing.Properties == nil {
			existing.Properties = map[string]any{}
		}
		for k, v := range req.Properties {
			existing.Properties[k] = v
		}
	}
	store.Put(id, existing)
	sim.WriteJSON(w, http.StatusOK, existing)
}

func handleEventGridUpdateTopic(w http.ResponseWriter, r *http.Request) {
	id := eventGridTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "topicName"))
	eventGridUpdateARMResource(w, r, eventGridTopics, id, "topic")
}

func handleEventGridUpdateDomain(w http.ResponseWriter, r *http.Request) {
	id := eventGridDomainID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "domainName"))
	eventGridUpdateARMResource(w, r, eventGridDomains, id, "domain")
}

func handleEventGridUpdateSystemTopic(w http.ResponseWriter, r *http.Request) {
	id := eventGridSystemTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "systemTopicName"))
	eventGridUpdateARMResource(w, r, eventGridSystemTopics, id, "system topic")
}

// eventGridRegenerateKeyResponse rotates one of a resource's two SAS keys and
// returns the resulting {key1,key2}. The rotated key changes deterministically
// per generation so a subsequent listKeys observes the new value; the other key
// is unchanged.
func eventGridRegenerateKeyResponse(w http.ResponseWriter, r *http.Request, store sim.Store[EventGridTopic], id, label string) {
	if _, ok := store.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "%s %q not found", label, id)
		return
	}
	var req struct {
		KeyName string `json:"keyName"`
	}
	_ = sim.ReadJSON(r, &req)
	rotated := req.KeyName
	if rotated != "key1" && rotated != "key2" {
		rotated = "key1"
	}
	azureBumpKeyGen(id, rotated, "")
	sim.WriteJSON(w, http.StatusOK, eventGridListKeysResponse(id))
}

func handleEventGridRegenerateTopicKey(w http.ResponseWriter, r *http.Request) {
	id := eventGridTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "topicName"))
	eventGridRegenerateKeyResponse(w, r, eventGridTopics, id, "topic")
}

func handleEventGridRegenerateDomainKey(w http.ResponseWriter, r *http.Request) {
	id := eventGridDomainID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "domainName"))
	eventGridRegenerateKeyResponse(w, r, eventGridDomains, id, "domain")
}

func handleEventGridUpdateEventSubscription(w http.ResponseWriter, r *http.Request) {
	scopeID, _, ok := eventGridScopeFromRequest(r)
	if !ok {
		AzureErrorf(w, "InvalidRequest", http.StatusBadRequest, "event subscription scope is not supported")
		return
	}
	id := eventGridSubscriptionIDForRequest(r, scopeID, sim.PathParam(r, "eventSubscriptionName"))
	es, ok := eventGridSubscriptions.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "event subscription %q not found", id)
		return
	}
	// EventSubscriptionUpdateParameters carries the mutable fields at the top
	// level (not wrapped in "properties"); merge each provided field into the
	// stored subscription's properties.
	var patch map[string]any
	if err := sim.ReadJSON(r, &patch); err != nil && r.ContentLength != 0 {
		AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if es.Properties == nil {
		es.Properties = map[string]any{}
	}
	for _, k := range []string{
		"destination", "filter", "labels", "deadLetterDestination", "retryPolicy",
		"eventDeliverySchema", "expirationTimeUtc", "deliveryWithResourceIdentity",
		"deadLetterWithResourceIdentity",
	} {
		if v, present := patch[k]; present {
			es.Properties[k] = v
		}
	}
	es.Properties["provisioningState"] = "Succeeded"
	eventGridSubscriptions.Put(id, es)
	// EventSubscriptions_Update declares 201 as its (only) success status.
	sim.WriteJSON(w, http.StatusCreated, es)
}

func handleEventGridGetEventSubscriptionFullURL(w http.ResponseWriter, r *http.Request) {
	es, ok := eventGridLookupSubscription(w, r)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"endpointUrl": eventGridWebhookEndpoint(es)})
}

func handleEventGridGetEventSubscriptionDeliveryAttributes(w http.ResponseWriter, r *http.Request) {
	es, ok := eventGridLookupSubscription(w, r)
	if !ok {
		return
	}
	value := make([]any, 0)
	if dest, ok := es.Properties["destination"].(map[string]any); ok {
		if props, ok := dest["properties"].(map[string]any); ok {
			if mappings, ok := props["deliveryAttributeMappings"].([]any); ok {
				value = mappings
			}
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
}

func eventGridLookupSubscription(w http.ResponseWriter, r *http.Request) (EventGridEventSubscription, bool) {
	scopeID, _, ok := eventGridScopeFromRequest(r)
	if !ok {
		AzureErrorf(w, "InvalidRequest", http.StatusBadRequest, "event subscription scope is not supported")
		return EventGridEventSubscription{}, false
	}
	id := eventGridSubscriptionIDForRequest(r, scopeID, sim.PathParam(r, "eventSubscriptionName"))
	es, ok := eventGridSubscriptions.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "event subscription %q not found", id)
		return EventGridEventSubscription{}, false
	}
	return es, true
}

func eventGridListSubsFiltered(w http.ResponseWriter, keep func(EventGridEventSubscription) bool) {
	out := make([]EventGridEventSubscription, 0)
	for _, es := range eventGridSubscriptions.List() {
		if keep(es) {
			out = append(out, es)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func eventGridSubInScope(es EventGridEventSubscription, sub, rg string) bool {
	prefix := "/subscriptions/" + sub + "/"
	if rg != "" {
		prefix = "/subscriptions/" + sub + "/resourceGroups/" + rg + "/"
	}
	return strings.HasPrefix(strings.ToLower(es.ID), strings.ToLower(prefix))
}

func handleEventGridListSubsGlobal(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	eventGridListSubsFiltered(w, func(es EventGridEventSubscription) bool {
		return eventGridSubInScope(es, sub, rg)
	})
}

func handleEventGridListSubsRegional(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	location := sim.PathParam(r, "location")
	eventGridListSubsFiltered(w, func(es EventGridEventSubscription) bool {
		return eventGridSubInScope(es, sub, rg) && eventGridScopeLocation(es) == location
	})
}

func handleEventGridListSubsForTopicType(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	location := sim.PathParam(r, "location")
	topicType := sim.PathParam(r, "topicTypeName")
	eventGridListSubsFiltered(w, func(es EventGridEventSubscription) bool {
		if !eventGridSubInScope(es, sub, rg) {
			return false
		}
		if location != "" && eventGridScopeLocation(es) != location {
			return false
		}
		return strings.EqualFold(eventGridScopeTopicType(es), topicType)
	})
}

func handleEventGridListSubsByResource(w http.ResponseWriter, r *http.Request) {
	resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/%s/%s/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"),
		sim.PathParam(r, "providerNamespace"), sim.PathParam(r, "resourceTypeName"), sim.PathParam(r, "resourceName"))
	eventGridListSubsFiltered(w, func(es EventGridEventSubscription) bool {
		if topic, _ := es.Properties["topic"].(string); strings.EqualFold(topic, resourceID) {
			return true
		}
		return strings.HasPrefix(strings.ToLower(es.ID), strings.ToLower(resourceID+"/providers/Microsoft.EventGrid/eventSubscriptions/")) ||
			strings.HasPrefix(strings.ToLower(es.ID), strings.ToLower(resourceID+"/eventSubscriptions/"))
	})
}

// eventGridScopeLocation resolves the location of an event subscription's
// parent source resource by looking it up across the topic-family stores.
func eventGridScopeLocation(es EventGridEventSubscription) string {
	scope, _ := es.Properties["topic"].(string)
	for _, store := range []sim.Store[EventGridTopic]{eventGridTopics, eventGridDomains, eventGridSystemTopics, eventGridPartnerTopics} {
		if res, ok := store.Get(scope); ok {
			return res.Location
		}
	}
	// Domain topics inherit the parent domain's location.
	if i := strings.Index(scope, "/topics/"); i >= 0 {
		if res, ok := eventGridDomains.Get(scope[:i]); ok {
			return res.Location
		}
	}
	return ""
}

// eventGridScopeTopicType maps an event subscription's parent source resource
// type to its Event Grid topic type name.
func eventGridScopeTopicType(es EventGridEventSubscription) string {
	scope, _ := es.Properties["topic"].(string)
	lower := strings.ToLower(scope)
	switch {
	case strings.Contains(lower, "/providers/microsoft.eventgrid/domains/") && strings.Contains(lower, "/topics/"):
		return "Microsoft.EventGrid.Domains"
	case strings.Contains(lower, "/providers/microsoft.eventgrid/topics/"):
		return "Microsoft.EventGrid.Topics"
	case strings.Contains(lower, "/providers/microsoft.eventgrid/domains/"):
		return "Microsoft.EventGrid.Domains"
	case strings.Contains(lower, "/providers/microsoft.eventgrid/partnertopics/"):
		return "Microsoft.EventGrid.PartnerTopics"
	case strings.Contains(lower, "/providers/microsoft.eventgrid/systemtopics/"):
		if res, ok := eventGridSystemTopics.Get(scope); ok {
			if tt, _ := res.Properties["topicType"].(string); tt != "" {
				return tt
			}
		}
		return "Microsoft.EventGrid.SystemTopics"
	}
	return ""
}

func eventGridParentExists(r *http.Request) (string, bool) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	parentType := sim.PathParam(r, "parentType")
	parentName := sim.PathParam(r, "parentName")
	id := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/%s/%s", sub, rg, parentType, parentName)
	switch strings.ToLower(parentType) {
	case "topics":
		_, ok := eventGridTopics.Get(id)
		return id, ok
	case "domains":
		_, ok := eventGridDomains.Get(id)
		return id, ok
	case "partnernamespaces":
		_, ok := eventGridPartnerNamespaces.Get(id)
		return id, ok
	}
	return id, false
}

// eventGridPrivateLinkGroupID returns the private-link sub-resource group id a
// given parent resource type exposes (a topic exposes "topic", a domain
// "domain", a partner namespace "partnernamespace").
func eventGridPrivateLinkGroupID(parentType string) string {
	switch strings.ToLower(parentType) {
	case "domains":
		return "domain"
	case "partnernamespaces":
		return "partnernamespace"
	default:
		return "topic"
	}
}

func eventGridPrivateLinkResource(parentID, parentType string) EventGridTopic {
	group := eventGridPrivateLinkGroupID(parentType)
	parentName := parentID[strings.LastIndex(parentID, "/")+1:]
	return EventGridTopic{
		ID:   parentID + "/privateLinkResources/" + group,
		Name: parentName + "/" + group,
		Type: "Microsoft.EventGrid/" + parentType + "/privateLinkResources",
		Properties: map[string]any{
			"groupId":           group,
			"displayName":       group,
			"requiredMembers":   []any{group},
			"requiredZoneNames": []any{"privatelink.eventgrid.azure.net"},
		},
	}
}

func handleEventGridListPrivateLinkResources(w http.ResponseWriter, r *http.Request) {
	parentID, ok := eventGridParentExists(r)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "parent resource %q not found", parentID)
		return
	}
	plr := eventGridPrivateLinkResource(parentID, sim.PathParam(r, "parentType"))
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []EventGridTopic{plr}})
}

func handleEventGridGetPrivateLinkResource(w http.ResponseWriter, r *http.Request) {
	parentID, ok := eventGridParentExists(r)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "parent resource %q not found", parentID)
		return
	}
	name := sim.PathParam(r, "privateLinkResourceName")
	if !strings.EqualFold(name, eventGridPrivateLinkGroupID(sim.PathParam(r, "parentType"))) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "private link resource %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, eventGridPrivateLinkResource(parentID, sim.PathParam(r, "parentType")))
}

func eventGridPrivateEndpointConnectionID(r *http.Request) (parentID, id string) {
	parentID = fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/%s/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"),
		sim.PathParam(r, "parentType"), sim.PathParam(r, "parentName"))
	return parentID, parentID + "/privateEndpointConnections/" + sim.PathParam(r, "privateEndpointConnectionName")
}

func handleEventGridListPrivateEndpointConnections(w http.ResponseWriter, r *http.Request) {
	parentID, ok := eventGridParentExists(r)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "parent resource %q not found", parentID)
		return
	}
	prefix := parentID + "/privateEndpointConnections/"
	out := make([]EventGridTopic, 0)
	for _, conn := range eventGridPrivateEndpointConnections.List() {
		if strings.HasPrefix(conn.ID, prefix) {
			out = append(out, conn)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleEventGridGetPrivateEndpointConnection(w http.ResponseWriter, r *http.Request) {
	_, id := eventGridPrivateEndpointConnectionID(r)
	conn, ok := eventGridPrivateEndpointConnections.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "private endpoint connection %q not found", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, conn)
}

func handleEventGridPutPrivateEndpointConnection(w http.ResponseWriter, r *http.Request) {
	parentID, id := eventGridPrivateEndpointConnectionID(r)
	if _, ok := eventGridParentExists(r); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "parent resource %q not found", parentID)
		return
	}
	var req EventGridTopic
	if err := sim.ReadJSON(r, &req); err != nil && r.ContentLength != 0 {
		AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	props := req.Properties
	if props == nil {
		props = map[string]any{}
	}
	props["provisioningState"] = "Succeeded"
	conn := EventGridTopic{
		ID:         id,
		Name:       sim.PathParam(r, "privateEndpointConnectionName"),
		Type:       "Microsoft.EventGrid/" + sim.PathParam(r, "parentType") + "/privateEndpointConnections",
		Properties: props,
	}
	eventGridPrivateEndpointConnections.Put(id, conn)
	sim.WriteJSON(w, http.StatusOK, conn)
}

func handleEventGridDeletePrivateEndpointConnection(w http.ResponseWriter, r *http.Request) {
	_, id := eventGridPrivateEndpointConnectionID(r)
	if !eventGridPrivateEndpointConnections.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "private endpoint connection %q not found", id)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ExtensionTopics_Get — the extension topic of an arbitrary Azure resource.
//
// An extension topic is not a resource a client creates: it is the addressable
// name Event Grid gives the events a resource already emits, and it points at
// the system topic carrying them. So the answer is derived from the scope in
// the path — the resource being asked about — and names the system topic whose
// source is that resource, when the subscription holds one.
func registerEventGridExtensionTopics(srv *sim.Server) {
	// Go's router cannot spell a wildcard that is not last, so the scopes an
	// extension topic is addressed at are enumerated the way every other
	// scope-rooted Event Grid route in this simulator is: the subscription, the
	// resource group, and a resource under a provider.
	const suffix = "/providers/Microsoft.EventGrid/extensionTopics/default"
	handler := func(w http.ResponseWriter, r *http.Request) {
		scope := strings.TrimSuffix(r.URL.Path, suffix)

		// The system topic mapped to the source, if the subscription has
		// one. A resource nobody created a system topic for still has an
		// extension topic — the events exist either way — and it names no
		// system topic, because there is none to name.
		systemTopic := ""
		if eventGridSystemTopics != nil {
			for _, held := range eventGridSystemTopics.List() {
				source, _ := held.Properties["source"].(string)
				if source != "" && strings.EqualFold(source, scope) {
					systemTopic = held.ID
					break
				}
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":   scope + "/providers/Microsoft.EventGrid/extensionTopics/default",
			"name": "default",
			"type": "Microsoft.EventGrid/extensionTopics",
			"properties": map[string]any{
				"description": "Extension topic for " + scope + ".",
				"systemTopic": systemTopic,
			},
		})
	}
	for _, scope := range []string{
		"/subscriptions/{subscriptionId}",
		"/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}",
		"/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{resourceProviderNamespace}/{resourceType}/{resourceName}",
	} {
		srv.HandleFunc("GET "+scope+suffix, handler)
	}
}
