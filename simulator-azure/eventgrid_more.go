package main

import (
	"maps"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

var eventGridKeySlots = []string{"key1", "key2"}

// Event Grid's keys live in the rotation store every Azure key surface shares
// (key_rotation.go), so rotation and move pinning behave as on the other
// services.
func eventGridKeyMaterial(id, keyName string) string {
	return azureKeyMaterial32(id, keyName)
}

func eventGridListKeysResponse(id string) map[string]string {
	return map[string]string{
		"key1": eventGridKeyMaterial(id, "key1"),
		"key2": eventGridKeyMaterial(id, "key2"),
	}
}

// A later resource with the same ID starts from fresh keys.
func eventGridDropKeyGens(id string) {
	azureDropKeyGens(id, eventGridKeySlots...)
}

// The Microsoft.EventGrid operations beyond eventgrid.go's core CRUD:
// operations, topic types, PATCH, key regeneration, event subscription
// extensions and scoped list views, and private link resources and endpoint
// connections.

var eventGridPrivateEndpointConnections sim.Store[EventGridTopic]

func registerEventGridMore(srv *sim.Server) {
	eventGridPrivateEndpointConnections = sim.MakeStore[EventGridTopic](srv.DB(), "eventgrid_private_endpoint_connections")

	const topicsBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/topics"
	const domainsBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/domains"
	const systemTopicsBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/systemTopics"
	const partnerTopicsBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/partnerTopics"

	srv.HandleFunc("GET /providers/Microsoft.EventGrid/operations", handleEventGridListOperations)
	srv.HandleFunc("GET /providers/Microsoft.EventGrid/topicTypes", handleEventGridListTopicTypes)
	srv.HandleFunc("GET /providers/Microsoft.EventGrid/topicTypes/{topicTypeName}", handleEventGridGetTopicType)
	srv.HandleFunc("GET /providers/Microsoft.EventGrid/topicTypes/{topicTypeName}/eventTypes", handleEventGridListTopicTypeEventTypes)

	srv.HandleFunc("PATCH "+topicsBase+"/{topicName}", handleEventGridUpdateTopic)
	srv.HandleFunc("POST "+topicsBase+"/{topicName}/regenerateKey", handleEventGridRegenerateTopicKey)
	srv.HandleFunc("PATCH "+domainsBase+"/{domainName}", handleEventGridUpdateDomain)
	srv.HandleFunc("POST "+domainsBase+"/{domainName}/regenerateKey", handleEventGridRegenerateDomainKey)
	srv.HandleFunc("PATCH "+systemTopicsBase+"/{systemTopicName}", handleEventGridUpdateSystemTopic)

	// PATCH also covers the global {scope} EventSubscriptions_Update through the
	// route-validity matcher's scope expansion; getFullUrl and
	// getDeliveryAttributes likewise.
	registerEventGridSubscriptionExtras(srv, topicsBase+"/{topicName}/providers/Microsoft.EventGrid/eventSubscriptions/{eventSubscriptionName}")
	registerEventGridSubscriptionExtras(srv, domainsBase+"/{domainName}/topics/{domainTopicName}/providers/Microsoft.EventGrid/eventSubscriptions/{eventSubscriptionName}")
	registerEventGridSubscriptionExtras(srv, systemTopicsBase+"/{systemTopicName}/eventSubscriptions/{eventSubscriptionName}")
	registerEventGridSubscriptionExtras(srv, partnerTopicsBase+"/{partnerTopicName}/providers/Microsoft.EventGrid/eventSubscriptions/{eventSubscriptionName}")

	// The 2022-06-15 per-resource groups address the subscription through the
	// nested URL form.
	registerEventGridNestedSubscription(srv, topicsBase+"/{topicName}/eventSubscriptions/{eventSubscriptionName}")
	registerEventGridNestedSubscription(srv, domainsBase+"/{domainName}/eventSubscriptions/{eventSubscriptionName}")
	registerEventGridNestedSubscription(srv, domainsBase+"/{domainName}/topics/{domainTopicName}/eventSubscriptions/{eventSubscriptionName}")
	registerEventGridNestedSubscription(srv, partnerTopicsBase+"/{partnerTopicName}/eventSubscriptions/{eventSubscriptionName}")
	// registerEventGrid registers the topics' nested list.
	srv.HandleFunc("GET "+domainsBase+"/{domainName}/eventSubscriptions", handleEventGridListEventSubscriptions)
	srv.HandleFunc("GET "+domainsBase+"/{domainName}/topics/{domainTopicName}/eventSubscriptions", handleEventGridListEventSubscriptions)
	srv.HandleFunc("GET "+partnerTopicsBase+"/{partnerTopicName}/eventSubscriptions", handleEventGridListEventSubscriptions)

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/eventSubscriptions", handleEventGridListSubsGlobal)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/eventSubscriptions", handleEventGridListSubsGlobal)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/locations/{location}/eventSubscriptions", handleEventGridListSubsRegional)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/locations/{location}/eventSubscriptions", handleEventGridListSubsRegional)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/topicTypes/{topicTypeName}/eventSubscriptions", handleEventGridListSubsForTopicType)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/topicTypes/{topicTypeName}/eventSubscriptions", handleEventGridListSubsForTopicType)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/locations/{location}/topicTypes/{topicTypeName}/eventSubscriptions", handleEventGridListSubsForTopicType)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/locations/{location}/topicTypes/{topicTypeName}/eventSubscriptions", handleEventGridListSubsForTopicType)

	const byResourceBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{providerNamespace}/{resourceTypeName}/{resourceName}/providers/Microsoft.EventGrid"
	srv.HandleFunc("GET "+byResourceBase+"/eventSubscriptions", handleEventGridListSubsByResource)
	srv.HandleFunc("GET "+byResourceBase+"/eventTypes", handleEventGridListResourceEventTypes)

	// Mounted under topics, domains and partnerNamespaces.
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

// eventGridUpdateARMResource applies a tags-and-properties PATCH, keeping the
// fields the request omits.
func eventGridUpdateARMResource(w http.ResponseWriter, r *http.Request, store sim.Store[EventGridTopic], id, label string) {
	existing, ok := store.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "%s %q not found", label, id)
		return
	}
	var req EventGridTopic
	if err := sim.ReadJSON(r, &req); err != nil {
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

// The rotated key changes per generation; the other stays.
func eventGridRegenerateKeyResponse(w http.ResponseWriter, r *http.Request, store sim.Store[EventGridTopic], id, label string) {
	if _, ok := store.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "%s %q not found", label, id)
		return
	}
	var req struct {
		KeyName string `json:"keyName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
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
	prior := es
	prior.Properties = maps.Clone(es.Properties)
	// EventSubscriptionUpdateParameters carries the mutable fields at the top
	// level, not under "properties".
	var patch map[string]any
	if err := sim.ReadJSON(r, &patch); err != nil {
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
	// EventSubscriptions_Update declares 201 as its only success status.
	if !eventGridNeedsValidation(es, &prior) {
		es.Properties["provisioningState"] = "Succeeded"
		eventGridSubscriptions.Put(id, es)
		sim.WriteJSON(w, http.StatusCreated, es)
		return
	}
	es.Properties["provisioningState"] = "Updating"
	eventGridSubscriptions.Put(id, es)
	_, store, _ := eventGridScopeFromRequest(r)
	scope, _ := store.Get(scopeID)
	writeEventGridValidationAccepted(w, r, scope.Location, startEventGridValidation(r, es))
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
	prefix := armSubscriptionID(sub) + "/"
	if rg != "" {
		prefix = armResourceGroupID(sub, rg) + "/"
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
	resourceID := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "providerNamespace"), sim.PathParam(r, "resourceTypeName"), sim.PathParam(r, "resourceName"))
	eventGridListSubsFiltered(w, func(es EventGridEventSubscription) bool {
		if topic, _ := es.Properties["topic"].(string); strings.EqualFold(topic, resourceID) {
			return true
		}
		return strings.HasPrefix(strings.ToLower(es.ID), strings.ToLower(resourceID+"/providers/Microsoft.EventGrid/eventSubscriptions/")) ||
			strings.HasPrefix(strings.ToLower(es.ID), strings.ToLower(resourceID+"/eventSubscriptions/"))
	})
}

func eventGridScopeLocation(es EventGridEventSubscription) string {
	scope, _ := es.Properties["topic"].(string)
	for _, store := range []sim.Store[EventGridTopic]{eventGridTopics, eventGridDomains, eventGridSystemTopics, eventGridPartnerTopics} {
		if res, ok := store.Get(scope); ok {
			return res.Location
		}
	}
	// A domain topic inherits its domain's location.
	if i := strings.Index(scope, "/topics/"); i >= 0 {
		if res, ok := eventGridDomains.Get(scope[:i]); ok {
			return res.Location
		}
	}
	return ""
}

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
	id := armResourceID(sub, rg, "Microsoft.EventGrid", parentType, parentName)
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

// A topic exposes "topic", a domain "domain", a partner namespace
// "partnernamespace".
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
	parentID = armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.EventGrid", sim.PathParam(r, "parentType"), sim.PathParam(r, "parentName"))
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
	if err := sim.ReadJSON(r, &req); err != nil {
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

// ExtensionTopics_Get: an extension topic is the name Event Grid gives the
// events a resource already emits, so the answer derives from the scope in the
// path and names the system topic whose source is that resource, if any.
func registerEventGridExtensionTopics(srv *sim.Server) {
	// Go's mux cannot spell a non-final wildcard, so enumerate the scopes: the
	// subscription, the resource group, and a resource under a provider.
	const suffix = "/providers/Microsoft.EventGrid/extensionTopics/default"
	handler := func(w http.ResponseWriter, r *http.Request) {
		scope := strings.TrimSuffix(r.URL.Path, suffix)

		// A resource without a system topic still has an extension topic, naming none.
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
