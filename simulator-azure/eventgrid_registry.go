package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Microsoft.EventGrid tenant-level read-only registries: the resource-provider
// operations list, the topic-type catalog, and the system event types each
// topic type emits.

// eventGridTopicTypeInfo describes a built-in Event Grid topic type and the
// system event types its sources emit.
type eventGridTopicTypeInfo struct {
	name               string
	provider           string
	displayName        string
	description        string
	resourceRegionType string
	sourceFormat       string
	eventTypes         []eventGridEventTypeInfo
}

type eventGridEventTypeInfo struct {
	name        string
	displayName string
	description string
}

// eventGridTopicTypeCatalog is a representative slice of the real Azure Event
// Grid topic-type catalog — the custom topic/domain types plus widely-used
// system-topic source types and their canonical system event types.
var eventGridTopicTypeCatalog = []eventGridTopicTypeInfo{
	{
		name: "Microsoft.EventGrid.Topics", provider: "Microsoft.EventGrid",
		displayName: "EventGrid Topics", description: "Microsoft EventGrid Topics",
		resourceRegionType: "RegionalResource", sourceFormat: "/subscriptions/{SubscriptionId}/resourceGroups/{ResourceGroup}/providers/Microsoft.EventGrid/topics/{TopicName}",
	},
	{
		name: "Microsoft.EventGrid.Domains", provider: "Microsoft.EventGrid",
		displayName: "EventGrid Domains", description: "Microsoft EventGrid Domains",
		resourceRegionType: "RegionalResource", sourceFormat: "/subscriptions/{SubscriptionId}/resourceGroups/{ResourceGroup}/providers/Microsoft.EventGrid/domains/{DomainName}",
	},
	{
		name: "Microsoft.Storage.StorageAccounts", provider: "Microsoft.Storage",
		displayName: "Storage Accounts", description: "Microsoft Storage Accounts",
		resourceRegionType: "RegionalResource", sourceFormat: "/subscriptions/{SubscriptionId}/resourceGroups/{ResourceGroup}/providers/Microsoft.Storage/storageAccounts/{StorageAccount}",
		eventTypes: []eventGridEventTypeInfo{
			{"Microsoft.Storage.BlobCreated", "Blob Created", "Raised when a blob is created or replaced."},
			{"Microsoft.Storage.BlobDeleted", "Blob Deleted", "Raised when a blob is deleted."},
			{"Microsoft.Storage.DirectoryCreated", "Directory Created", "Raised when a directory is created."},
			{"Microsoft.Storage.DirectoryDeleted", "Directory Deleted", "Raised when a directory is deleted."},
		},
	},
	{
		name: "Microsoft.Resources.Subscriptions", provider: "Microsoft.Resources",
		displayName: "Azure Subscriptions", description: "Microsoft Resources Subscriptions",
		resourceRegionType: "GlobalResource", sourceFormat: "/subscriptions/{SubscriptionId}",
		eventTypes: []eventGridEventTypeInfo{
			{"Microsoft.Resources.ResourceWriteSuccess", "Resource Write Success", "Raised when a resource create or update operation succeeds."},
			{"Microsoft.Resources.ResourceDeleteSuccess", "Resource Delete Success", "Raised when a resource delete operation succeeds."},
		},
	},
	{
		name: "Microsoft.Resources.ResourceGroups", provider: "Microsoft.Resources",
		displayName: "Azure Resource Groups", description: "Microsoft Resources Resource Groups",
		resourceRegionType: "GlobalResource", sourceFormat: "/subscriptions/{SubscriptionId}/resourceGroups/{ResourceGroup}",
		eventTypes: []eventGridEventTypeInfo{
			{"Microsoft.Resources.ResourceWriteSuccess", "Resource Write Success", "Raised when a resource create or update operation succeeds."},
			{"Microsoft.Resources.ResourceDeleteSuccess", "Resource Delete Success", "Raised when a resource delete operation succeeds."},
		},
	},
	{
		name: "Microsoft.EventHub.Namespaces", provider: "Microsoft.EventHub",
		displayName: "Event Hubs Namespaces", description: "Microsoft Event Hubs Namespaces",
		resourceRegionType: "RegionalResource", sourceFormat: "/subscriptions/{SubscriptionId}/resourceGroups/{ResourceGroup}/providers/Microsoft.EventHub/namespaces/{NamespaceName}",
		eventTypes: []eventGridEventTypeInfo{
			{"Microsoft.EventHub.CaptureFileCreated", "Capture File Created", "Raised when an Event Hubs capture file is created."},
		},
	},
	{
		name: "Microsoft.ContainerRegistry.Registries", provider: "Microsoft.ContainerRegistry",
		displayName: "Container Registries", description: "Microsoft Container Registries",
		resourceRegionType: "RegionalResource", sourceFormat: "/subscriptions/{SubscriptionId}/resourceGroups/{ResourceGroup}/providers/Microsoft.ContainerRegistry/registries/{RegistryName}",
		eventTypes: []eventGridEventTypeInfo{
			{"Microsoft.ContainerRegistry.ImagePushed", "Image Pushed", "Raised when an image is pushed to a container registry."},
			{"Microsoft.ContainerRegistry.ImageDeleted", "Image Deleted", "Raised when an image is deleted from a container registry."},
		},
	},
	{
		name: "Microsoft.KeyVault.vaults", provider: "Microsoft.KeyVault",
		displayName: "Azure Key Vault", description: "Microsoft Key Vaults",
		resourceRegionType: "RegionalResource", sourceFormat: "/subscriptions/{SubscriptionId}/resourceGroups/{ResourceGroup}/providers/Microsoft.KeyVault/vaults/{VaultName}",
		eventTypes: []eventGridEventTypeInfo{
			{"Microsoft.KeyVault.SecretNewVersionCreated", "Secret New Version Created", "Raised when a new version of a secret is created."},
			{"Microsoft.KeyVault.CertificateNewVersionCreated", "Certificate New Version Created", "Raised when a new version of a certificate is created."},
		},
	},
}

func eventGridTopicTypeResource(info eventGridTopicTypeInfo) EventGridTopic {
	return EventGridTopic{
		ID:   "/providers/Microsoft.EventGrid/topicTypes/" + info.name,
		Name: info.name,
		Type: "Microsoft.EventGrid/topicTypes",
		Properties: map[string]any{
			"provider":             info.provider,
			"displayName":          info.displayName,
			"description":          info.description,
			"resourceRegionType":   info.resourceRegionType,
			"provisioningState":    "Succeeded",
			"sourceResourceFormat": info.sourceFormat,
			"supportedLocations":   []any{"East US", "West US", "North Europe", "West Europe"},
		},
	}
}

func eventGridEventTypeResource(topicType string, et eventGridEventTypeInfo) EventGridTopic {
	return EventGridTopic{
		ID:   "/providers/Microsoft.EventGrid/topicTypes/" + topicType + "/eventTypes/" + et.name,
		Name: et.name,
		Type: "Microsoft.EventGrid/topicTypes/eventTypes",
		Properties: map[string]any{
			"displayName":    et.displayName,
			"description":    et.description,
			"schemaUrl":      "",
			"isInDefaultSet": true,
		},
	}
}

func handleEventGridListTopicTypes(w http.ResponseWriter, r *http.Request) {
	out := make([]EventGridTopic, 0, len(eventGridTopicTypeCatalog))
	for _, info := range eventGridTopicTypeCatalog {
		out = append(out, eventGridTopicTypeResource(info))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleEventGridGetTopicType(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "topicTypeName")
	for _, info := range eventGridTopicTypeCatalog {
		if strings.EqualFold(info.name, name) {
			sim.WriteJSON(w, http.StatusOK, eventGridTopicTypeResource(info))
			return
		}
	}
	AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "topic type %q not found", name)
}

func handleEventGridListTopicTypeEventTypes(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "topicTypeName")
	for _, info := range eventGridTopicTypeCatalog {
		if strings.EqualFold(info.name, name) {
			out := make([]EventGridTopic, 0, len(info.eventTypes))
			for _, et := range info.eventTypes {
				out = append(out, eventGridEventTypeResource(info.name, et))
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
			return
		}
	}
	AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "topic type %q not found", name)
}

// handleEventGridListResourceEventTypes lists the system event types emitted by
// an arbitrary Azure resource (Topics_ListEventTypes / generic eventTypes by
// resource), matched to the catalog by the resource's provider + type.
func handleEventGridListResourceEventTypes(w http.ResponseWriter, r *http.Request) {
	provider := sim.PathParam(r, "providerNamespace")
	resourceType := sim.PathParam(r, "resourceTypeName")
	out := make([]EventGridTopic, 0)
	for _, info := range eventGridTopicTypeCatalog {
		// Topic type names are "<Provider>.<ResourceTypePlural>"; match the
		// resource's provider and final type segment loosely.
		if !strings.EqualFold(info.provider, provider) {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(info.name), strings.ToLower(resourceType)) {
			continue
		}
		for _, et := range info.eventTypes {
			out = append(out, eventGridEventTypeResource(info.name, et))
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

// eventGridOperations is a representative slice of the Microsoft.EventGrid
// resource-provider operation catalog returned by Operations_List.
var eventGridOperations = []map[string]any{
	egOp("Microsoft.EventGrid/topics/read", "EventGrid Topics", "Read a topic"),
	egOp("Microsoft.EventGrid/topics/write", "EventGrid Topics", "Create or update a topic"),
	egOp("Microsoft.EventGrid/topics/delete", "EventGrid Topics", "Delete a topic"),
	egOp("Microsoft.EventGrid/topics/listKeys/action", "EventGrid Topics", "List keys of a topic"),
	egOp("Microsoft.EventGrid/topics/regenerateKey/action", "EventGrid Topics", "Regenerate key of a topic"),
	egOp("Microsoft.EventGrid/domains/read", "EventGrid Domains", "Read a domain"),
	egOp("Microsoft.EventGrid/domains/write", "EventGrid Domains", "Create or update a domain"),
	egOp("Microsoft.EventGrid/domains/delete", "EventGrid Domains", "Delete a domain"),
	egOp("Microsoft.EventGrid/eventSubscriptions/read", "EventGrid Event Subscriptions", "Read an event subscription"),
	egOp("Microsoft.EventGrid/eventSubscriptions/write", "EventGrid Event Subscriptions", "Create or update an event subscription"),
	egOp("Microsoft.EventGrid/eventSubscriptions/delete", "EventGrid Event Subscriptions", "Delete an event subscription"),
	egOp("Microsoft.EventGrid/systemTopics/read", "EventGrid System Topics", "Read a system topic"),
	egOp("Microsoft.EventGrid/partnerTopics/read", "EventGrid Partner Topics", "Read a partner topic"),
	egOp("Microsoft.EventGrid/operations/read", "EventGrid Operations", "List operations"),
}

func egOp(name, resource, operation string) map[string]any {
	return map[string]any{
		"name": name,
		"display": map[string]any{
			"provider":  "Microsoft Event Grid",
			"resource":  resource,
			"operation": operation,
		},
		"isDataAction": false,
		"origin":       "user,system",
	}
}

func handleEventGridListOperations(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": eventGridOperations})
}
