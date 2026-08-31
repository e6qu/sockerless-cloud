package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Microsoft.EventGrid ARM control plane plus custom-topic publish
// data plane. Topics are addressed through ARM; events publish to
// the topic endpoint's /api/events path and synchronously fan out to
// webhook event subscriptions.

type EventGridTopic struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Location string `json:"location,omitempty"`
	// Tags is omitted when empty: proxy resources (domains/topics,
	// partner topics before tagging) have no tags member at all.
	Tags       map[string]string `json:"tags,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
}

type EventGridEventSubscription struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

var (
	eventGridTopics        sim.Store[EventGridTopic]
	eventGridDomains       sim.Store[EventGridTopic]
	eventGridDomainTopics  sim.Store[EventGridTopic]
	eventGridSystemTopics  sim.Store[EventGridTopic]
	eventGridPartnerTopics sim.Store[EventGridTopic]
	eventGridSubscriptions sim.Store[EventGridEventSubscription]
)

func registerEventGrid(srv *sim.Server) {
	makeAzureKeyGens(srv)
	eventGridTopics = sim.MakeStore[EventGridTopic](srv.DB(), "eventgrid_topics")
	eventGridDomains = sim.MakeStore[EventGridTopic](srv.DB(), "eventgrid_domains")
	eventGridDomainTopics = sim.MakeStore[EventGridTopic](srv.DB(), "eventgrid_domain_topics")
	eventGridSystemTopics = sim.MakeStore[EventGridTopic](srv.DB(), "eventgrid_system_topics")
	registerEventGridExtensionTopics(srv)
	eventGridPartnerTopics = sim.MakeStore[EventGridTopic](srv.DB(), "eventgrid_partner_topics")
	eventGridSubscriptions = sim.MakeStore[EventGridEventSubscription](srv.DB(), "eventgrid_subscriptions")

	const topicsBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/topics"
	const domainsBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/domains"
	const systemTopicsBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/systemTopics"
	const partnerTopicsBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.EventGrid/partnerTopics"
	srv.HandleFunc("PUT "+topicsBase+"/{topicName}", handleEventGridCreateTopic)
	srv.HandleFunc("GET "+topicsBase+"/{topicName}", handleEventGridGetTopic)
	srv.HandleFunc("POST "+topicsBase+"/{topicName}/listKeys", handleEventGridListTopicKeys)
	srv.HandleFunc("DELETE "+topicsBase+"/{topicName}", handleEventGridDeleteTopic)
	srv.HandleFunc("GET "+topicsBase, handleEventGridListTopicsByRG)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/topics", handleEventGridListTopicsBySubscription)

	srv.HandleFunc("PUT "+topicsBase+"/{topicName}/providers/Microsoft.EventGrid/eventSubscriptions/{eventSubscriptionName}", handleEventGridCreateEventSubscription)
	srv.HandleFunc("GET "+topicsBase+"/{topicName}/providers/Microsoft.EventGrid/eventSubscriptions/{eventSubscriptionName}", handleEventGridGetEventSubscription)
	srv.HandleFunc("DELETE "+topicsBase+"/{topicName}/providers/Microsoft.EventGrid/eventSubscriptions/{eventSubscriptionName}", handleEventGridDeleteEventSubscription)
	srv.HandleFunc("GET "+topicsBase+"/{topicName}/providers/Microsoft.EventGrid/eventSubscriptions", handleEventGridListEventSubscriptions)
	srv.HandleFunc("GET "+topicsBase+"/{topicName}/eventSubscriptions", handleEventGridListEventSubscriptions)

	srv.HandleFunc("PUT "+domainsBase+"/{domainName}", handleEventGridCreateDomain)
	srv.HandleFunc("GET "+domainsBase+"/{domainName}", handleEventGridGetDomain)
	srv.HandleFunc("POST "+domainsBase+"/{domainName}/listKeys", handleEventGridListDomainKeys)
	srv.HandleFunc("DELETE "+domainsBase+"/{domainName}", handleEventGridDeleteDomain)
	srv.HandleFunc("GET "+domainsBase, handleEventGridListDomainsByRG)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/domains", handleEventGridListDomainsBySubscription)
	srv.HandleFunc("PUT "+domainsBase+"/{domainName}/topics/{domainTopicName}", handleEventGridCreateDomainTopic)
	srv.HandleFunc("GET "+domainsBase+"/{domainName}/topics/{domainTopicName}", handleEventGridGetDomainTopic)
	srv.HandleFunc("DELETE "+domainsBase+"/{domainName}/topics/{domainTopicName}", handleEventGridDeleteDomainTopic)
	srv.HandleFunc("GET "+domainsBase+"/{domainName}/topics", handleEventGridListDomainTopics)
	srv.HandleFunc("PUT "+domainsBase+"/{domainName}/topics/{domainTopicName}/providers/Microsoft.EventGrid/eventSubscriptions/{eventSubscriptionName}", handleEventGridCreateEventSubscription)
	srv.HandleFunc("GET "+domainsBase+"/{domainName}/topics/{domainTopicName}/providers/Microsoft.EventGrid/eventSubscriptions/{eventSubscriptionName}", handleEventGridGetEventSubscription)
	srv.HandleFunc("DELETE "+domainsBase+"/{domainName}/topics/{domainTopicName}/providers/Microsoft.EventGrid/eventSubscriptions/{eventSubscriptionName}", handleEventGridDeleteEventSubscription)
	srv.HandleFunc("GET "+domainsBase+"/{domainName}/topics/{domainTopicName}/providers/Microsoft.EventGrid/eventSubscriptions", handleEventGridListEventSubscriptions)

	srv.HandleFunc("PUT "+systemTopicsBase+"/{systemTopicName}", handleEventGridCreateSystemTopic)
	srv.HandleFunc("GET "+systemTopicsBase+"/{systemTopicName}", handleEventGridGetSystemTopic)
	srv.HandleFunc("DELETE "+systemTopicsBase+"/{systemTopicName}", handleEventGridDeleteSystemTopic)
	srv.HandleFunc("GET "+systemTopicsBase, handleEventGridListSystemTopicsByRG)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/systemTopics", handleEventGridListSystemTopicsBySubscription)
	srv.HandleFunc("PUT "+systemTopicsBase+"/{systemTopicName}/eventSubscriptions/{eventSubscriptionName}", handleEventGridCreateEventSubscription)
	srv.HandleFunc("GET "+systemTopicsBase+"/{systemTopicName}/eventSubscriptions/{eventSubscriptionName}", handleEventGridGetEventSubscription)
	srv.HandleFunc("DELETE "+systemTopicsBase+"/{systemTopicName}/eventSubscriptions/{eventSubscriptionName}", handleEventGridDeleteEventSubscription)
	srv.HandleFunc("GET "+systemTopicsBase+"/{systemTopicName}/eventSubscriptions", handleEventGridListEventSubscriptions)

	srv.HandleFunc("PUT "+partnerTopicsBase+"/{partnerTopicName}", handleEventGridCreatePartnerTopic)
	srv.HandleFunc("PATCH "+partnerTopicsBase+"/{partnerTopicName}", handleEventGridUpdatePartnerTopic)
	srv.HandleFunc("GET "+partnerTopicsBase+"/{partnerTopicName}", handleEventGridGetPartnerTopic)
	srv.HandleFunc("POST "+partnerTopicsBase+"/{partnerTopicName}/activate", handleEventGridActivatePartnerTopic)
	srv.HandleFunc("POST "+partnerTopicsBase+"/{partnerTopicName}/deactivate", handleEventGridDeactivatePartnerTopic)
	srv.HandleFunc("DELETE "+partnerTopicsBase+"/{partnerTopicName}", handleEventGridDeletePartnerTopic)
	srv.HandleFunc("GET "+partnerTopicsBase, handleEventGridListPartnerTopicsByRG)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.EventGrid/partnerTopics", handleEventGridListPartnerTopicsBySubscription)
	srv.HandleFunc("PUT "+partnerTopicsBase+"/{partnerTopicName}/providers/Microsoft.EventGrid/eventSubscriptions/{eventSubscriptionName}", handleEventGridCreateEventSubscription)
	srv.HandleFunc("GET "+partnerTopicsBase+"/{partnerTopicName}/providers/Microsoft.EventGrid/eventSubscriptions/{eventSubscriptionName}", handleEventGridGetEventSubscription)
	srv.HandleFunc("DELETE "+partnerTopicsBase+"/{partnerTopicName}/providers/Microsoft.EventGrid/eventSubscriptions/{eventSubscriptionName}", handleEventGridDeleteEventSubscription)
	srv.HandleFunc("GET "+partnerTopicsBase+"/{partnerTopicName}/providers/Microsoft.EventGrid/eventSubscriptions", handleEventGridListEventSubscriptions)

	registerEventGridMore(srv)
	registerEventGridPartner(srv)

	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			if i := strings.LastIndex(host, ":"); i >= 0 {
				host = host[:i]
			}
			if strings.Contains(host, ".eventgrid.") && r.Method == http.MethodPost && strings.TrimRight(r.URL.Path, "/") == "/api/events" {
				handleEventGridPublishEvents(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	srv.HandleFunc("POST /api/events", handleEventGridPublishEvents)
}

func eventGridTopicID(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/topics/%s", sub, rg, name)
}

func eventGridDomainID(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/domains/%s", sub, rg, name)
}

func eventGridDomainTopicID(sub, rg, domain, topic string) string {
	return eventGridDomainID(sub, rg, domain) + "/topics/" + topic
}

func eventGridSystemTopicID(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/systemTopics/%s", sub, rg, name)
}

func eventGridPartnerTopicID(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/partnerTopics/%s", sub, rg, name)
}

func eventGridSubscriptionID(topicID, name string) string {
	return topicID + "/providers/Microsoft.EventGrid/eventSubscriptions/" + name
}

func eventGridSystemTopicSubscriptionID(systemTopicID, name string) string {
	return systemTopicID + "/eventSubscriptions/" + name
}

func eventGridScopeFromRequest(r *http.Request) (string, sim.Store[EventGridTopic], bool) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	switch {
	case sim.PathParam(r, "topicName") != "":
		return eventGridTopicID(sub, rg, sim.PathParam(r, "topicName")), eventGridTopics, true
	case sim.PathParam(r, "domainTopicName") != "":
		return eventGridDomainTopicID(sub, rg, sim.PathParam(r, "domainName"), sim.PathParam(r, "domainTopicName")), eventGridDomainTopics, true
	case sim.PathParam(r, "systemTopicName") != "":
		return eventGridSystemTopicID(sub, rg, sim.PathParam(r, "systemTopicName")), eventGridSystemTopics, true
	case sim.PathParam(r, "partnerTopicName") != "":
		return eventGridPartnerTopicID(sub, rg, sim.PathParam(r, "partnerTopicName")), eventGridPartnerTopics, true
	case sim.PathParam(r, "domainName") != "":
		// A domain-scoped event subscription (domains/{domainName}/
		// eventSubscriptions/...) — distinct from a domain TOPIC scope,
		// which sets domainTopicName and is matched above.
		return eventGridDomainID(sub, rg, sim.PathParam(r, "domainName")), eventGridDomains, true
	default:
		return "", nil, false
	}
}

// eventGridSubscriptionIDForRequest builds the canonical event-subscription
// resource ID for the addressed scope. Azure routes the same logical
// subscription through two URL shapes: the provider-qualified
// ".../providers/Microsoft.EventGrid/eventSubscriptions/{name}" form
// (the EventSubscriptions operations group at an arbitrary {scope}) and the
// nested ".../eventSubscriptions/{name}" form (the per-resource
// {Topic,Domain,SystemTopic,PartnerTopic,DomainTopic}EventSubscriptions
// groups). The ID mirrors whichever form addressed it.
func eventGridSubscriptionIDForRequest(r *http.Request, scopeID, name string) string {
	if strings.Contains(r.URL.Path, "/providers/Microsoft.EventGrid/eventSubscriptions/") {
		return eventGridSubscriptionID(scopeID, name)
	}
	return eventGridSystemTopicSubscriptionID(scopeID, name)
}

func eventGridEndpointHost(r *http.Request, topic string) string {
	hostname, portSuffix := azureRequestHostParts(r)
	if net.ParseIP(hostname) != nil {
		hostname = "localhost"
	}
	return strings.Join([]string{topic, "eventgrid", hostname}, ".") + portSuffix
}

// eventGridTopicWithEndpoint returns a copy of topic whose properties carry the
// data-plane endpoint. The endpoint is a stable per-topic property: once stamped
// (at create) it is preserved, so reads are pure and never overwrite stored
// state with a request-Host-derived value. The returned topic shares no maps
// with the input, so callers may safely persist the original unchanged.
func eventGridTopicWithEndpoint(r *http.Request, topic EventGridTopic) EventGridTopic {
	props := make(map[string]any, len(topic.Properties)+1)
	for k, v := range topic.Properties {
		props[k] = v
	}
	if _, ok := props["endpoint"]; !ok {
		if endpoint := azureEventGridEndpointURL(r, topic.Name); endpoint != "" {
			props["endpoint"] = endpoint
		} else {
			props["endpoint"] = fmt.Sprintf("%s://%s/api/events", azureRequestScheme(r), eventGridEndpointHost(r, topic.Name))
		}
	}
	topic.Properties = props
	return topic
}

func handleEventGridCreateTopic(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "topicName")
	var req EventGridTopic
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := eventGridTopicID(sub, rg, name)
	props := req.Properties
	if props == nil {
		props = map[string]any{}
	}
	props["provisioningState"] = "Succeeded"
	if _, ok := props["inputSchema"]; !ok {
		props["inputSchema"] = "EventGridSchema"
	}
	switch fmt.Sprint(props["inputSchema"]) {
	case "EventGridSchema", "CloudEventSchemaV1_0", "CustomEventSchema":
	default:
		sim.AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest,
			"inputSchema %q is invalid; must be one of EventGridSchema, CloudEventSchemaV1_0, CustomEventSchema", props["inputSchema"])
		return
	}
	if _, ok := props["publicNetworkAccess"]; !ok {
		props["publicNetworkAccess"] = "Enabled"
	}
	tags := req.Tags
	if tags == nil {
		tags = map[string]string{}
	}
	topic := EventGridTopic{
		ID:         id,
		Name:       name,
		Type:       "Microsoft.EventGrid/topics",
		Location:   req.Location,
		Tags:       tags,
		Properties: props,
	}
	// Stamp the endpoint once, at create, so it becomes a stable property of
	// the topic that later reads return verbatim.
	topic = eventGridTopicWithEndpoint(r, topic)
	eventGridTopics.Put(id, topic)
	sim.WriteJSON(w, http.StatusCreated, topic)
}

func handleEventGridGetTopic(w http.ResponseWriter, r *http.Request) {
	id := eventGridTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "topicName"))
	topic, ok := eventGridTopics.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "topic %q not found", id)
		return
	}
	// Pure read: derive the endpoint into the response copy without persisting
	// a request-Host-derived value back into the store.
	topic = eventGridTopicWithEndpoint(r, topic)
	sim.WriteJSON(w, http.StatusOK, topic)
}

func handleEventGridListTopicKeys(w http.ResponseWriter, r *http.Request) {
	id := eventGridTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "topicName"))
	if _, ok := eventGridTopics.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "topic %q not found", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, eventGridListKeysResponse(id))
}

func handleEventGridDeleteTopic(w http.ResponseWriter, r *http.Request) {
	id := eventGridTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "topicName"))
	if !eventGridTopics.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "topic %q not found", id)
		return
	}
	for _, sub := range eventGridSubscriptions.List() {
		if strings.HasPrefix(sub.ID, id+"/providers/Microsoft.EventGrid/eventSubscriptions/") {
			eventGridSubscriptions.Delete(sub.ID)
		}
	}
	eventGridDropKeyGens(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleEventGridListTopicsByRG(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/topics/", sub, rg)
	out := make([]EventGridTopic, 0)
	for _, topic := range eventGridTopics.List() {
		if strings.HasPrefix(topic.ID, prefix) {
			out = append(out, eventGridTopicWithEndpoint(r, topic))
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleEventGridListTopicsBySubscription(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/", sub)
	out := make([]EventGridTopic, 0)
	for _, topic := range eventGridTopics.List() {
		if strings.HasPrefix(topic.ID, prefix) {
			out = append(out, eventGridTopicWithEndpoint(r, topic))
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleEventGridCreateDomain(w http.ResponseWriter, r *http.Request) {
	id := eventGridDomainID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "domainName"))
	eventGridCreateARMResource(w, r, eventGridDomains, id, sim.PathParam(r, "domainName"), "Microsoft.EventGrid/domains", func(props map[string]any) {
		props["provisioningState"] = "Succeeded"
		if _, ok := props["endpoint"]; !ok {
			if endpoint := azureEventGridEndpointURL(r, sim.PathParam(r, "domainName")); endpoint != "" {
				props["endpoint"] = endpoint
			} else {
				props["endpoint"] = fmt.Sprintf("%s://%s/api/events", azureRequestScheme(r), eventGridEndpointHost(r, sim.PathParam(r, "domainName")))
			}
		}
	})
}

func handleEventGridGetDomain(w http.ResponseWriter, r *http.Request) {
	eventGridGetARMResource(w, eventGridDomains, eventGridDomainID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "domainName")), "domain")
}

func handleEventGridListDomainKeys(w http.ResponseWriter, r *http.Request) {
	id := eventGridDomainID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "domainName"))
	if _, ok := eventGridDomains.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "domain %q not found", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, eventGridListKeysResponse(id))
}

func handleEventGridDeleteDomain(w http.ResponseWriter, r *http.Request) {
	id := eventGridDomainID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "domainName"))
	if !eventGridDomains.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "domain %q not found", id)
		return
	}
	for _, topic := range eventGridDomainTopics.List() {
		if strings.HasPrefix(topic.ID, id+"/topics/") {
			deleteEventGridScope(topic.ID)
			eventGridDomainTopics.Delete(topic.ID)
		}
	}
	eventGridDropKeyGens(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleEventGridListDomainsByRG(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/domains/", sub, rg)
	eventGridListARMResources(w, eventGridDomains, prefix)
}

func handleEventGridListDomainsBySubscription(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/", sub)
	eventGridListARMResources(w, eventGridDomains, prefix)
}

func handleEventGridCreateDomainTopic(w http.ResponseWriter, r *http.Request) {
	domainID := eventGridDomainID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "domainName"))
	if _, ok := eventGridDomains.Get(domainID); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "domain %q not found", domainID)
		return
	}
	id := eventGridDomainTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "domainName"), sim.PathParam(r, "domainTopicName"))
	// DomainTopic is a proxy resource (allOf Resource): no location, no
	// tags members.
	topic := EventGridTopic{
		ID:   id,
		Name: sim.PathParam(r, "domainTopicName"),
		Type: "Microsoft.EventGrid/domains/topics",
		Properties: map[string]any{
			"provisioningState": "Succeeded",
		},
	}
	eventGridDomainTopics.Put(id, topic)
	sim.WriteJSON(w, http.StatusCreated, topic)
}

func handleEventGridGetDomainTopic(w http.ResponseWriter, r *http.Request) {
	eventGridGetARMResource(w, eventGridDomainTopics, eventGridDomainTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "domainName"), sim.PathParam(r, "domainTopicName")), "domain topic")
}

func handleEventGridDeleteDomainTopic(w http.ResponseWriter, r *http.Request) {
	id := eventGridDomainTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "domainName"), sim.PathParam(r, "domainTopicName"))
	if !eventGridDomainTopics.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "domain topic %q not found", id)
		return
	}
	deleteEventGridScope(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleEventGridListDomainTopics(w http.ResponseWriter, r *http.Request) {
	prefix := eventGridDomainID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "domainName")) + "/topics/"
	eventGridListARMResources(w, eventGridDomainTopics, prefix)
}

func handleEventGridCreateSystemTopic(w http.ResponseWriter, r *http.Request) {
	id := eventGridSystemTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "systemTopicName"))
	eventGridCreateARMResource(w, r, eventGridSystemTopics, id, sim.PathParam(r, "systemTopicName"), "Microsoft.EventGrid/systemTopics", func(props map[string]any) {
		props["provisioningState"] = "Succeeded"
		if source, _ := props["source"].(string); source != "" {
			props["metricResourceId"] = source
		}
	})
}

func handleEventGridGetSystemTopic(w http.ResponseWriter, r *http.Request) {
	eventGridGetARMResource(w, eventGridSystemTopics, eventGridSystemTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "systemTopicName")), "system topic")
}

func handleEventGridDeleteSystemTopic(w http.ResponseWriter, r *http.Request) {
	id := eventGridSystemTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "systemTopicName"))
	if !eventGridSystemTopics.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "system topic %q not found", id)
		return
	}
	deleteEventGridScope(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleEventGridListSystemTopicsByRG(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/systemTopics/", sub, rg)
	eventGridListARMResources(w, eventGridSystemTopics, prefix)
}

func handleEventGridListSystemTopicsBySubscription(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/", sub)
	eventGridListARMResources(w, eventGridSystemTopics, prefix)
}

func handleEventGridCreatePartnerTopic(w http.ResponseWriter, r *http.Request) {
	id := eventGridPartnerTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "partnerTopicName"))
	eventGridCreateARMResource(w, r, eventGridPartnerTopics, id, sim.PathParam(r, "partnerTopicName"), "Microsoft.EventGrid/partnerTopics", func(props map[string]any) {
		props["provisioningState"] = "Succeeded"
		if _, ok := props["activationState"]; !ok {
			props["activationState"] = "NeverActivated"
		}
	})
}

func handleEventGridUpdatePartnerTopic(w http.ResponseWriter, r *http.Request) {
	id := eventGridPartnerTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "partnerTopicName"))
	existing, ok := eventGridPartnerTopics.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "partner topic %q not found", id)
		return
	}
	var req EventGridTopic
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "invalid request body: %v", err)
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
	eventGridPartnerTopics.Put(id, existing)
	sim.WriteJSON(w, http.StatusOK, existing)
}

func handleEventGridGetPartnerTopic(w http.ResponseWriter, r *http.Request) {
	eventGridGetARMResource(w, eventGridPartnerTopics, eventGridPartnerTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "partnerTopicName")), "partner topic")
}

func handleEventGridActivatePartnerTopic(w http.ResponseWriter, r *http.Request) {
	id := eventGridPartnerTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "partnerTopicName"))
	topic, ok := eventGridPartnerTopics.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "partner topic %q not found", id)
		return
	}
	if topic.Properties == nil {
		topic.Properties = map[string]any{}
	}
	topic.Properties["activationState"] = "Activated"
	topic.Properties["provisioningState"] = "Succeeded"
	eventGridPartnerTopics.Put(id, topic)
	sim.WriteJSON(w, http.StatusOK, topic)
}

func handleEventGridDeactivatePartnerTopic(w http.ResponseWriter, r *http.Request) {
	id := eventGridPartnerTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "partnerTopicName"))
	topic, ok := eventGridPartnerTopics.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "partner topic %q not found", id)
		return
	}
	if topic.Properties == nil {
		topic.Properties = map[string]any{}
	}
	topic.Properties["activationState"] = "Deactivated"
	topic.Properties["provisioningState"] = "Succeeded"
	eventGridPartnerTopics.Put(id, topic)
	sim.WriteJSON(w, http.StatusOK, topic)
}

func handleEventGridDeletePartnerTopic(w http.ResponseWriter, r *http.Request) {
	id := eventGridPartnerTopicID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "partnerTopicName"))
	if !eventGridPartnerTopics.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "partner topic %q not found", id)
		return
	}
	deleteEventGridScope(id)
	w.WriteHeader(http.StatusNoContent)
}

func handleEventGridListPartnerTopicsByRG(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/partnerTopics/", sub, rg)
	eventGridListARMResources(w, eventGridPartnerTopics, prefix)
}

func handleEventGridListPartnerTopicsBySubscription(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/", sub)
	eventGridListARMResources(w, eventGridPartnerTopics, prefix)
}

func eventGridCreateARMResource(w http.ResponseWriter, r *http.Request, store sim.Store[EventGridTopic], id, name, resourceType string, mutate func(map[string]any)) {
	var req EventGridTopic
	if err := sim.ReadJSON(r, &req); err != nil && r.ContentLength != 0 {
		sim.AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	props := req.Properties
	if props == nil {
		props = map[string]any{}
	}
	mutate(props)
	tags := req.Tags
	if tags == nil {
		tags = map[string]string{}
	}
	resource := EventGridTopic{
		ID:         id,
		Name:       name,
		Type:       resourceType,
		Location:   req.Location,
		Tags:       tags,
		Properties: props,
	}
	store.Put(id, resource)
	sim.WriteJSON(w, http.StatusCreated, resource)
}

func eventGridGetARMResource(w http.ResponseWriter, store sim.Store[EventGridTopic], id, label string) {
	resource, ok := store.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "%s %q not found", label, id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, resource)
}

func eventGridListARMResources(w http.ResponseWriter, store sim.Store[EventGridTopic], prefix string) {
	out := make([]EventGridTopic, 0)
	for _, resource := range store.List() {
		if strings.HasPrefix(resource.ID, prefix) {
			out = append(out, resource)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func deleteEventGridScope(scopeID string) {
	for _, sub := range eventGridSubscriptions.List() {
		if strings.HasPrefix(sub.ID, scopeID+"/providers/Microsoft.EventGrid/eventSubscriptions/") ||
			strings.HasPrefix(sub.ID, scopeID+"/eventSubscriptions/") {
			eventGridSubscriptions.Delete(sub.ID)
		}
	}
}

func handleEventGridCreateEventSubscription(w http.ResponseWriter, r *http.Request) {
	scopeID, store, ok := eventGridScopeFromRequest(r)
	if !ok {
		sim.AzureErrorf(w, "InvalidRequest", http.StatusBadRequest, "event subscription scope is not supported")
		return
	}
	if _, ok := store.Get(scopeID); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "event subscription scope %q not found", scopeID)
		return
	}
	name := sim.PathParam(r, "eventSubscriptionName")
	var req EventGridEventSubscription
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	props := req.Properties
	if props == nil {
		props = map[string]any{}
	}
	props["provisioningState"] = "Succeeded"
	props["topic"] = scopeID
	es := EventGridEventSubscription{
		ID:         eventGridSubscriptionIDForRequest(r, scopeID, name),
		Name:       name,
		Type:       "Microsoft.EventGrid/eventSubscriptions",
		Properties: props,
	}
	eventGridSubscriptions.Put(es.ID, es)
	deliverEventGridValidation(es)
	sim.WriteJSON(w, http.StatusCreated, es)
}

func handleEventGridGetEventSubscription(w http.ResponseWriter, r *http.Request) {
	scopeID, _, ok := eventGridScopeFromRequest(r)
	if !ok {
		sim.AzureErrorf(w, "InvalidRequest", http.StatusBadRequest, "event subscription scope is not supported")
		return
	}
	id := eventGridSubscriptionIDForRequest(r, scopeID, sim.PathParam(r, "eventSubscriptionName"))
	es, ok := eventGridSubscriptions.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "event subscription %q not found", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, es)
}

func handleEventGridDeleteEventSubscription(w http.ResponseWriter, r *http.Request) {
	scopeID, _, ok := eventGridScopeFromRequest(r)
	if !ok {
		sim.AzureErrorf(w, "InvalidRequest", http.StatusBadRequest, "event subscription scope is not supported")
		return
	}
	id := eventGridSubscriptionIDForRequest(r, scopeID, sim.PathParam(r, "eventSubscriptionName"))
	if !eventGridSubscriptions.Delete(id) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "event subscription %q not found", id)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleEventGridListEventSubscriptions(w http.ResponseWriter, r *http.Request) {
	scopeID, _, ok := eventGridScopeFromRequest(r)
	if !ok {
		sim.AzureErrorf(w, "InvalidRequest", http.StatusBadRequest, "event subscription scope is not supported")
		return
	}
	prefix := scopeID + "/providers/Microsoft.EventGrid/eventSubscriptions/"
	systemPrefix := scopeID + "/eventSubscriptions/"
	out := make([]EventGridEventSubscription, 0)
	for _, es := range eventGridSubscriptions.List() {
		if strings.HasPrefix(es.ID, prefix) || strings.HasPrefix(es.ID, systemPrefix) {
			out = append(out, es)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleEventGridPublishEvents(w http.ResponseWriter, r *http.Request) {
	scope, ok := eventGridPublishScopeFromHost(r.Host)
	if !ok {
		sim.AzureErrorf(w, "TopicNotFound", http.StatusNotFound, "event grid topic host %q not found", r.Host)
		return
	}
	publishEventGridScope(w, r, scope)
}

// eventGridPublishScope is a resource that accepts an /api/events publish. A
// custom topic and a domain advertise the same endpoint shape and serve the
// same two access keys, so they authenticate identically; they differ in how a
// published event is routed — a custom topic fans out to its own event
// subscriptions, while a domain routes each event to the domain topic the
// event's `topic` member names.
type eventGridPublishScope struct {
	resource EventGridTopic
	isDomain bool
}

func publishEventGridScope(w http.ResponseWriter, r *http.Request, scope eventGridPublishScope) {
	if !eventGridAuthorizePublish(w, r, scope.resource.ID, scope.resource.Properties) {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		sim.AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "failed to read request body: %v", err)
		return
	}
	events, err := validateEventGridPublishBody(body, scope.isDomain)
	if err != nil {
		sim.AzureErrorf(w, "InvalidEvent", http.StatusBadRequest, "%v", err)
		return
	}
	if !scope.isDomain {
		deliverEventGridBatch(scope.resource.ID, body)
		w.WriteHeader(http.StatusOK)
		return
	}
	// A domain fans each event out to its own domain topic. A domain-scoped
	// event subscription receives every event published to the domain, which
	// falls out of routing each event to the domain scope as well.
	for i, event := range events {
		single, marshalErr := json.Marshal([]json.RawMessage{rawEventGridEvent(body, i)})
		if marshalErr != nil {
			sim.AzureErrorf(w, "InvalidEvent", http.StatusBadRequest, "event %d could not be re-encoded: %v", i, marshalErr)
			return
		}
		deliverEventGridBatch(eventGridDomainTopicScopeID(scope.resource.ID, event.Topic), single)
		deliverEventGridBatch(scope.resource.ID, single)
	}
	w.WriteHeader(http.StatusOK)
}

// eventGridDomainTopicScopeID is the resource ID of a domain topic beneath a
// domain, which is the scope a domain-published event's subscriptions hang off.
func eventGridDomainTopicScopeID(domainID, topic string) string {
	return domainID + "/topics/" + topic
}

// deliverEventGridBatch posts a publish batch to every webhook subscription of
// one scope.
func deliverEventGridBatch(scopeID string, body []byte) {
	// A publish delivers to one scope's subscriptions, so the store is indexed
	// by the scopes a subscription belongs to rather than read in full for
	// every published event.
	for _, es := range eventGridSubscriptionsByTopic.LookupAll(eventGridSubscriptions, scopeID,
		eventGridSubscriptionTopics) {
		if endpoint := eventGridWebhookEndpoint(es); endpoint != "" {
			if resp, err := http.Post(endpoint, "application/json", bytes.NewReader(body)); err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}
	}
}

// rawEventGridEvent returns the i-th event of a publish batch exactly as the
// publisher encoded it, so a domain delivery carries the operator's own JSON
// rather than a re-serialisation of the fields the validator models.
func rawEventGridEvent(body []byte, i int) json.RawMessage {
	var raw []json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || i >= len(raw) {
		return json.RawMessage("null")
	}
	return raw[i]
}

type eventGridPublishEvent struct {
	ID          string          `json:"id"`
	Subject     string          `json:"subject"`
	EventType   string          `json:"eventType"`
	EventTime   string          `json:"eventTime"`
	Data        json.RawMessage `json:"data"`
	DataVersion string          `json:"dataVersion"`
	// Topic names the domain topic an event published to a domain belongs to.
	// It is the publisher's choice on a domain and the service's own value on
	// a custom topic.
	Topic string `json:"topic"`
}

// validateEventGridPublishBody checks a publish batch against the
// EventGridEvent schema and returns the parsed events. A domain additionally
// requires each event to name the domain topic it belongs to, because that
// member is what routes it.
func validateEventGridPublishBody(body []byte, isDomain bool) ([]eventGridPublishEvent, error) {
	var events []eventGridPublishEvent
	if err := json.Unmarshal(body, &events); err != nil {
		return nil, fmt.Errorf("request body must be a JSON array of Event Grid events: %w", err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("request body must contain at least one event")
	}
	for i, event := range events {
		switch {
		case event.ID == "":
			return nil, fmt.Errorf("event %d is missing required field id", i)
		case event.Subject == "":
			return nil, fmt.Errorf("event %d is missing required field subject", i)
		case event.EventType == "":
			return nil, fmt.Errorf("event %d is missing required field eventType", i)
		case event.EventTime == "":
			return nil, fmt.Errorf("event %d is missing required field eventTime", i)
		case len(event.Data) == 0:
			return nil, fmt.Errorf("event %d is missing required field data", i)
		case event.DataVersion == "":
			return nil, fmt.Errorf("event %d is missing required field dataVersion", i)
		case isDomain && event.Topic == "":
			return nil, fmt.Errorf("event %d is missing required field topic, which names the domain topic it is published to", i)
		}
		if _, err := time.Parse(time.RFC3339Nano, event.EventTime); err != nil {
			return nil, fmt.Errorf("event %d has invalid eventTime: %w", i, err)
		}
	}
	return events, nil
}

var eventGridSubscriptionsByTopic sim.GenerationIndex[EventGridEventSubscription]

// eventGridSubscriptionTopics returns the scopes a subscription belongs to —
// the two eventGridSubscriptionBelongsToTopic accepts: the resource its
// identifier hangs off, and the topic its properties name.
func eventGridSubscriptionTopics(es EventGridEventSubscription) []string {
	const segment = "/providers/Microsoft.EventGrid/eventSubscriptions/"
	var scopes []string
	// Every occurrence is offered, so a resource whose own name contains the
	// segment cannot hide the scope that precedes it.
	for at := 0; ; {
		i := strings.Index(es.ID[at:], segment)
		if i < 0 {
			break
		}
		scopes = append(scopes, es.ID[:at+i])
		at += i + len(segment)
	}
	if es.Properties != nil {
		if topic, ok := es.Properties["topic"].(string); ok && topic != "" {
			scopes = append(scopes, topic)
		}
	}
	return scopes
}

// eventGridSubscriptionBelongsToTopic answers from the same function the
// delivery index is keyed on, so the predicate and the index cannot disagree
// about which subscriptions a scope owns.
func eventGridSubscriptionBelongsToTopic(es EventGridEventSubscription, topicID string) bool {
	return slices.Contains(eventGridSubscriptionTopics(es), topicID)
}

// eventGridPublishScopeFromHost resolves the publishing resource an
// /api/events request addresses. Both a custom topic and a domain advertise an
// endpoint whose first host label is the resource's globally unique name, so
// the label selects between the two stores.
func eventGridPublishScopeFromHost(host string) (eventGridPublishScope, bool) {
	hostname := host
	if i := strings.LastIndex(hostname, ":"); i >= 0 {
		hostname = hostname[:i]
	}
	name := strings.Split(hostname, ".")[0]
	if topic, ok := eventGridTopicsByName.Lookup(eventGridTopics, name,
		func(t EventGridTopic) []string { return []string{t.Name} }); ok {
		return eventGridPublishScope{resource: topic}, true
	}
	if domain, ok := eventGridDomainsByName.Lookup(eventGridDomains, name,
		func(d EventGridTopic) []string { return []string{d.Name} }); ok {
		return eventGridPublishScope{resource: domain, isDomain: true}, true
	}
	return eventGridPublishScope{}, false
}

// Both stores are read by a handler wrapper, so every request into the
// simulator paid two full scans before reaching its own handler.
var (
	eventGridTopicsByName  sim.GenerationIndex[EventGridTopic]
	eventGridDomainsByName sim.GenerationIndex[EventGridTopic]
)

func eventGridWebhookEndpoint(es EventGridEventSubscription) string {
	dest, ok := es.Properties["destination"].(map[string]any)
	if !ok {
		return ""
	}
	props, ok := dest["properties"].(map[string]any)
	if !ok {
		return ""
	}
	if endpoint, _ := props["endpointUrl"].(string); endpoint != "" {
		return endpoint
	}
	if endpoint, _ := props["endpointBaseUrl"].(string); endpoint != "" {
		return endpoint
	}
	return ""
}

func deliverEventGridValidation(es EventGridEventSubscription) {
	endpoint := eventGridWebhookEndpoint(es)
	if endpoint == "" {
		return
	}
	event := []map[string]any{{
		"id":        generateUUID(),
		"eventType": "Microsoft.EventGrid.SubscriptionValidationEvent",
		"subject":   "",
		"eventTime": time.Now().UTC().Format(time.RFC3339Nano),
		"data": map[string]any{
			"validationCode": generateUUID(),
			"validationUrl":  endpoint,
		},
		"dataVersion": "1",
	}}
	payload, _ := json.Marshal(event)
	if resp, err := http.Post(endpoint, "application/json", bytes.NewReader(payload)); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}
