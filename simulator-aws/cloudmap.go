package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cloud Map types

type CMNamespace struct {
	Id               string                 `json:"Id"`
	Arn              string                 `json:"Arn"`
	Name             string                 `json:"Name"`
	Type             string                 `json:"Type"`
	Description      string                 `json:"Description,omitempty"`
	Properties       *CMNamespaceProperties `json:"Properties,omitempty"`
	CreateDate       int64                  `json:"CreateDate"`
	CreatorRequestId string                 `json:"CreatorRequestId,omitempty"`
}

type CMNamespaceProperties struct {
	DnsProperties  *CMDnsProperties  `json:"DnsProperties,omitempty"`
	HttpProperties *CMHttpProperties `json:"HttpProperties,omitempty"`
}

type CMHttpProperties struct {
	HttpName string `json:"HttpName"`
}

type CMDnsProperties struct {
	HostedZoneId string `json:"HostedZoneId"`
	SOA          *struct {
		TTL int64 `json:"TTL"`
	} `json:"SOA,omitempty"`
}

type CMService struct {
	Id                      string                     `json:"Id"`
	Arn                     string                     `json:"Arn"`
	Name                    string                     `json:"Name"`
	NamespaceId             string                     `json:"NamespaceId"`
	Description             string                     `json:"Description,omitempty"`
	DnsConfig               *CMDnsConfig               `json:"DnsConfig,omitempty"`
	HealthCheckConfig       *CMHealthCheckConfig       `json:"HealthCheckConfig,omitempty"`
	HealthCheckCustomConfig *CMHealthCheckCustomConfig `json:"HealthCheckCustomConfig,omitempty"`
	Type                    string                     `json:"Type,omitempty"`
	CreateDate              int64                      `json:"CreateDate"`
	InstanceCount           int                        `json:"InstanceCount"`
	CreatorRequestId        string                     `json:"CreatorRequestId,omitempty"`
}

type CMHealthCheckConfig struct {
	Type             string `json:"Type,omitempty"`
	ResourcePath     string `json:"ResourcePath,omitempty"`
	FailureThreshold int    `json:"FailureThreshold,omitempty"`
}

// CMHealthCheckCustomConfig is the HealthCheckCustomConfig shape: a service
// whose instance health is reported by the caller through
// UpdateInstanceCustomHealthStatus rather than by a Route 53 health check.
type CMHealthCheckCustomConfig struct {
	FailureThreshold int `json:"FailureThreshold,omitempty"`
}

type CMDnsConfig struct {
	NamespaceId   string        `json:"NamespaceId,omitempty"`
	RoutingPolicy string        `json:"RoutingPolicy,omitempty"`
	DnsRecords    []CMDnsRecord `json:"DnsRecords,omitempty"`
}

type CMDnsRecord struct {
	Type string `json:"Type"`
	TTL  int64  `json:"TTL"`
}

type CMInstance struct {
	Id         string            `json:"Id"`
	Attributes map[string]string `json:"Attributes,omitempty"`
	// CustomHealthStatus, when set, overrides the registered health for an
	// instance whose service uses a HealthCheckCustomConfig (the value
	// UpdateInstanceCustomHealthStatus writes: HEALTHY or UNHEALTHY).
	CustomHealthStatus string `json:"CustomHealthStatus,omitempty"`
	CreatorRequestId   string `json:"CreatorRequestId,omitempty"`
}

type CMOperation struct {
	OperationId string            `json:"OperationId"`
	Status      string            `json:"Status"`
	Type        string            `json:"Type,omitempty"`
	NamespaceId string            `json:"NamespaceId,omitempty"`
	ServiceId   string            `json:"ServiceId,omitempty"`
	Targets     map[string]string `json:"Targets,omitempty"`
	CreateDate  int64             `json:"CreateDate,omitempty"`
	UpdateDate  int64             `json:"UpdateDate,omitempty"`
}

// State stores
var (
	cmNamespaces    sim.Store[CMNamespace]
	cmNamespaceVPCs sim.Store[string]
	cmServices      sim.Store[CMService]
	cmInstances     sim.Store[CMInstance]
	cmOperations    sim.Store[CMOperation]
	// cmServiceAttributes holds the per-service key/value attribute map
	// exposed by Get/Update/DeleteServiceAttributes, keyed by service ID.
	cmServiceAttributes sim.Store[map[string]string]
	// cmServiceRevisions tracks each service's instance-list revision,
	// incremented on every RegisterInstance/DeregisterInstance and returned
	// by DiscoverInstancesRevision. Keyed by service ID.
	cmServiceRevisions sim.Store[int64]
	// cmTags holds the tags of every taggable Cloud Map resource, keyed by the
	// resource ARN — the key TagResource/UntagResource/ListTagsForResource
	// address a resource by.
	cmTags sim.Store[map[string]string]
	// cmNamespaceNetworks records the Docker user-defined network that locally
	// realizes a private namespace's DNS, keyed by namespace ID. It is the
	// simulator's realization mechanism, not part of the Cloud Map API, so it
	// lives beside the namespace record rather than inside the wire shape.
	cmNamespaceNetworks sim.Store[string]
)

// cmMaxTagsPerResource is the Cloud Map per-resource tag limit; exceeding it is
// a TooManyTagsException.
const cmMaxTagsPerResource = 50

func cmArn(resourceType, id string) string {
	return fmt.Sprintf("arn:aws:servicediscovery:%s:%s:%s/%s", awsRegion(), awsAccountID(), resourceType, id)
}

func cmInstanceKey(serviceId, instanceId string) string {
	return serviceId + ":" + instanceId
}

// cmTag is the wire Tag shape carried by the Create* requests and the tagging
// operations.
type cmTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// cmPutResourceTags stores the tags a Create* request carried for a new
// resource. Cloud Map tags every namespace and service by ARN, and
// ListTagsForResource reads them back — the same tags an operator (or the ECS
// backend's network-state recovery) filters on.
func cmPutResourceTags(arn string, tags []cmTag) {
	if len(tags) == 0 {
		return
	}
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[t.Key] = t.Value
	}
	cmTags.Put(arn, m)
}

// cmResourceExists reports whether an ARN addresses a live namespace or
// service — the two taggable Cloud Map resource types.
func cmResourceExists(arn string) bool {
	for _, ns := range cmNamespaces.List() {
		if ns.Arn == arn {
			return true
		}
	}
	for _, svc := range cmServices.List() {
		if svc.Arn == arn {
			return true
		}
	}
	return false
}

// cmNamespaceServiceCount counts the services in a namespace — the
// ServiceCount member of the Namespace and NamespaceSummary shapes.
func cmNamespaceServiceCount(namespaceId string) int {
	n := 0
	for _, svc := range cmServices.List() {
		if svc.NamespaceId == namespaceId {
			n++
		}
	}
	return n
}

// cmNamespaceSummary projects a stored namespace onto the NamespaceSummary
// shape returned by ListNamespaces.
func cmNamespaceSummary(ns CMNamespace) map[string]any {
	out := map[string]any{
		"Id":           ns.Id,
		"Arn":          ns.Arn,
		"Name":         ns.Name,
		"Type":         ns.Type,
		"CreateDate":   ns.CreateDate,
		"ServiceCount": cmNamespaceServiceCount(ns.Id),
	}
	if ns.Description != "" {
		out["Description"] = ns.Description
	}
	if ns.Properties != nil {
		out["Properties"] = ns.Properties
	}
	return out
}

// cmNamespaceView projects a stored namespace onto the Namespace shape
// returned by GetNamespace — the summary plus CreatorRequestId.
func cmNamespaceView(ns CMNamespace) map[string]any {
	out := cmNamespaceSummary(ns)
	if ns.CreatorRequestId != "" {
		out["CreatorRequestId"] = ns.CreatorRequestId
	}
	return out
}

// cmInstanceSummary projects a stored instance onto the InstanceSummary shape
// ListInstances returns: the registered ID and attributes only. The custom
// health status and the owning service ID are simulator bookkeeping, not wire
// members, and InstanceSummary has no CreatorRequestId member.
func cmInstanceSummary(inst CMInstance) map[string]any {
	attrs := inst.Attributes
	if attrs == nil {
		attrs = map[string]string{}
	}
	return map[string]any{
		"Id":         inst.Id,
		"Attributes": attrs,
	}
}

// cmInstanceView projects a stored instance onto the Instance shape GetInstance
// returns — the summary members plus the CreatorRequestId of the registration.
func cmInstanceView(inst CMInstance) map[string]any {
	out := cmInstanceSummary(inst)
	if inst.CreatorRequestId != "" {
		out["CreatorRequestId"] = inst.CreatorRequestId
	}
	return out
}

// cmServiceInstances returns every instance registered under a service, sorted
// by instance ID so pagination is stable. Registrations are keyed by
// service+instance, so the record returned is the one this service registered:
// the same instance ID may back several services with different attributes.
func cmServiceInstances(serviceId string) []CMInstance {
	seen := make(map[string]struct{})
	var out []CMInstance
	for _, inst := range cmInstances.List() {
		if _, dup := seen[inst.Id]; dup {
			continue
		}
		stored, ok := cmInstances.Get(cmInstanceKey(serviceId, inst.Id))
		if !ok {
			continue
		}
		seen[inst.Id] = struct{}{}
		out = append(out, stored)
	}
	return sortBy(out, func(inst CMInstance) string { return inst.Id })
}

// cmFilterMatch applies one Cloud Map list filter to a resource's actual value.
// EQ (the default) and IN both match any of the filter's values; BEGINS_WITH
// matches a prefix. BETWEEN is only meaningful for the UPDATE_DATE operation
// filter, which the operation-filter path handles separately.
func cmFilterMatch(condition string, values []string, actual string) bool {
	switch condition {
	case "BEGINS_WITH":
		for _, v := range values {
			if strings.HasPrefix(actual, v) {
				return true
			}
		}
		return false
	default: // EQ, IN, or unset
		for _, v := range values {
			if actual == v {
				return true
			}
		}
		return false
	}
}

func registerCloudMap(r *AWSRouter, srv *sim.Server) {
	cmNamespaces = sim.MakeStore[CMNamespace](srv.DB(), "cloudmap_namespaces")
	cmNamespaceVPCs = sim.MakeStore[string](srv.DB(), "cloudmap_namespace_vpcs")
	cmServices = sim.MakeStore[CMService](srv.DB(), "cloudmap_services")
	cmInstances = sim.MakeStore[CMInstance](srv.DB(), "cloudmap_instances")
	cmOperations = sim.MakeStore[CMOperation](srv.DB(), "cloudmap_operations")
	cmServiceAttributes = sim.MakeStore[map[string]string](srv.DB(), "cloudmap_service_attributes")
	cmServiceRevisions = sim.MakeStore[int64](srv.DB(), "cloudmap_service_revisions")
	cmTags = sim.MakeStore[map[string]string](srv.DB(), "cloudmap_tags")
	cmNamespaceNetworks = sim.MakeStore[string](srv.DB(), "cloudmap_namespace_networks")

	r.Register("Route53AutoNaming_v20170314.CreatePrivateDnsNamespace", handleCMCreatePrivateDnsNamespace)
	r.Register("Route53AutoNaming_v20170314.CreatePublicDnsNamespace", handleCMCreatePublicDnsNamespace)
	r.Register("Route53AutoNaming_v20170314.CreateHttpNamespace", handleCMCreateHttpNamespace)
	r.Register("Route53AutoNaming_v20170314.GetNamespace", handleCMGetNamespace)
	r.Register("Route53AutoNaming_v20170314.DeleteNamespace", handleCMDeleteNamespace)
	r.Register("Route53AutoNaming_v20170314.UpdateHttpNamespace", handleCMUpdateHttpNamespace)
	r.Register("Route53AutoNaming_v20170314.UpdatePrivateDnsNamespace", handleCMUpdatePrivateDnsNamespace)
	r.Register("Route53AutoNaming_v20170314.UpdatePublicDnsNamespace", handleCMUpdatePublicDnsNamespace)
	r.Register("Route53AutoNaming_v20170314.CreateService", handleCMCreateService)
	r.Register("Route53AutoNaming_v20170314.GetService", handleCMGetService)
	r.Register("Route53AutoNaming_v20170314.UpdateService", handleCMUpdateService)
	r.Register("Route53AutoNaming_v20170314.GetServiceAttributes", handleCMGetServiceAttributes)
	r.Register("Route53AutoNaming_v20170314.UpdateServiceAttributes", handleCMUpdateServiceAttributes)
	r.Register("Route53AutoNaming_v20170314.DeleteServiceAttributes", handleCMDeleteServiceAttributes)
	r.Register("Route53AutoNaming_v20170314.RegisterInstance", handleCMRegisterInstance)
	r.Register("Route53AutoNaming_v20170314.DeregisterInstance", handleCMDeregisterInstance)
	r.Register("Route53AutoNaming_v20170314.GetInstance", handleCMGetInstance)
	r.Register("Route53AutoNaming_v20170314.ListInstances", handleCMListInstances)
	r.Register("Route53AutoNaming_v20170314.UpdateInstanceCustomHealthStatus", handleCMUpdateInstanceCustomHealthStatus)
	r.Register("Route53AutoNaming_v20170314.GetInstancesHealthStatus", handleCMGetInstancesHealthStatus)
	r.Register("Route53AutoNaming_v20170314.DiscoverInstances", handleCMDiscoverInstances)
	r.Register("Route53AutoNaming_v20170314.DiscoverInstancesRevision", handleCMDiscoverInstancesRevision)
	r.Register("Route53AutoNaming_v20170314.GetOperation", handleCMGetOperation)
	r.Register("Route53AutoNaming_v20170314.ListOperations", handleCMListOperations)
	r.Register("Route53AutoNaming_v20170314.ListNamespaces", handleCMListNamespaces)
	r.Register("Route53AutoNaming_v20170314.ListServices", handleCMListServices)
	r.Register("Route53AutoNaming_v20170314.DeleteService", handleCMDeleteService)
	r.Register("Route53AutoNaming_v20170314.ListTagsForResource", handleCMListTagsForResource)
	r.Register("Route53AutoNaming_v20170314.TagResource", handleCMTagResource)
	r.Register("Route53AutoNaming_v20170314.UntagResource", handleCMUntagResource)
}

// cmSOAProperties is the SOA carried by the Create*Namespace Properties member
// (PrivateDnsNamespaceProperties / PublicDnsNamespaceProperties).
type cmSOAProperties struct {
	DnsProperties *struct {
		SOA *struct {
			TTL int64 `json:"TTL"`
		} `json:"SOA"`
	} `json:"DnsProperties"`
}

// cmDnsPropertiesFor builds a new DNS namespace's DnsProperties: a fresh
// hosted zone plus the SOA the request configured, if any.
func cmDnsPropertiesFor(props *cmSOAProperties) *CMDnsProperties {
	dns := &CMDnsProperties{HostedZoneId: "Z" + generateUUID()[:12]}
	if props != nil && props.DnsProperties != nil && props.DnsProperties.SOA != nil {
		dns.SOA = &struct {
			TTL int64 `json:"TTL"`
		}{TTL: props.DnsProperties.SOA.TTL}
	}
	return dns
}

// cmNamespaceNameTaken reports whether a namespace with this name already
// exists — Cloud Map namespace names are unique per account and region.
func cmNamespaceNameTaken(name string) bool {
	for _, ns := range cmNamespaces.List() {
		if ns.Name == name {
			return true
		}
	}
	return false
}

// cmCreateNamespace stores a new namespace, its tags and the SUCCESS
// CREATE_NAMESPACE operation, then writes the OperationId response the three
// Create*Namespace operations share.
func cmCreateNamespace(w http.ResponseWriter, ns CMNamespace, tags []cmTag) {
	cmNamespaces.Put(ns.Id, ns)
	cmPutResourceTags(ns.Arn, tags)

	operationId := generateUUID()
	now := time.Now().Unix()
	cmOperations.Put(operationId, CMOperation{
		OperationId: operationId,
		Status:      "SUCCESS",
		Type:        "CREATE_NAMESPACE",
		NamespaceId: ns.Id,
		Targets:     map[string]string{"NAMESPACE": ns.Id},
		CreateDate:  now,
		UpdateDate:  now,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"OperationId": operationId,
	})
}

func handleCMCreatePrivateDnsNamespace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string           `json:"Name"`
		Vpc              string           `json:"Vpc"`
		Description      string           `json:"Description"`
		CreatorRequestId string           `json:"CreatorRequestId"`
		Properties       *cmSOAProperties `json:"Properties"`
		Tags             []cmTag          `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		AWSError(w, "InvalidInput", "Name is required", http.StatusBadRequest)
		return
	}
	if req.Vpc == "" {
		AWSError(w, "InvalidInput", "Vpc is required", http.StatusBadRequest)
		return
	}
	if cmNamespaceNameTaken(req.Name) {
		AWSErrorf(w, "NamespaceAlreadyExists", http.StatusBadRequest,
			"A namespace with the name '%s' already exists", req.Name)
		return
	}

	nsId := "ns-" + generateUUID()[:16]
	cmNamespaceVPCs.Put(nsId, req.Vpc)
	cmCreateNamespace(w, CMNamespace{
		Id:               nsId,
		Arn:              cmArn("namespace", nsId),
		Name:             req.Name,
		Type:             "DNS_PRIVATE",
		Description:      req.Description,
		CreatorRequestId: req.CreatorRequestId,
		Properties: &CMNamespaceProperties{
			DnsProperties: cmDnsPropertiesFor(req.Properties),
		},
		CreateDate: time.Now().Unix(),
	}, req.Tags)
}

// handleCMCreatePublicDnsNamespace creates a DNS_PUBLIC namespace. Public DNS
// namespaces back internet-routable DNS records; the control-plane resource is
// modeled identically to a private one minus the VPC association.
func handleCMCreatePublicDnsNamespace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string           `json:"Name"`
		Description      string           `json:"Description"`
		CreatorRequestId string           `json:"CreatorRequestId"`
		Properties       *cmSOAProperties `json:"Properties"`
		Tags             []cmTag          `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		AWSError(w, "InvalidInput", "Name is required", http.StatusBadRequest)
		return
	}
	if cmNamespaceNameTaken(req.Name) {
		AWSErrorf(w, "NamespaceAlreadyExists", http.StatusBadRequest,
			"A namespace with the name '%s' already exists", req.Name)
		return
	}

	nsId := "ns-" + generateUUID()[:16]
	cmCreateNamespace(w, CMNamespace{
		Id:               nsId,
		Arn:              cmArn("namespace", nsId),
		Name:             req.Name,
		Type:             "DNS_PUBLIC",
		Description:      req.Description,
		CreatorRequestId: req.CreatorRequestId,
		Properties: &CMNamespaceProperties{
			DnsProperties: cmDnsPropertiesFor(req.Properties),
		},
		CreateDate: time.Now().Unix(),
	}, req.Tags)
}

// handleCMCreateHttpNamespace creates an HTTP namespace. HTTP namespaces have
// no DNS records — discovery is via the DiscoverInstances API only — and carry
// an HttpProperties.HttpName (defaulting to the namespace name).
func handleCMCreateHttpNamespace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string  `json:"Name"`
		Description      string  `json:"Description"`
		CreatorRequestId string  `json:"CreatorRequestId"`
		Tags             []cmTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		AWSError(w, "InvalidInput", "Name is required", http.StatusBadRequest)
		return
	}
	if cmNamespaceNameTaken(req.Name) {
		AWSErrorf(w, "NamespaceAlreadyExists", http.StatusBadRequest,
			"A namespace with the name '%s' already exists", req.Name)
		return
	}

	nsId := "ns-" + generateUUID()[:16]
	cmCreateNamespace(w, CMNamespace{
		Id:               nsId,
		Arn:              cmArn("namespace", nsId),
		Name:             req.Name,
		Type:             "HTTP",
		Description:      req.Description,
		CreatorRequestId: req.CreatorRequestId,
		Properties: &CMNamespaceProperties{
			HttpProperties: &CMHttpProperties{HttpName: req.Name},
		},
		CreateDate: time.Now().Unix(),
	}, req.Tags)
}

func handleCMGetNamespace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id string `json:"Id"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Id == "" {
		AWSError(w, "InvalidInput", "Id is required", http.StatusBadRequest)
		return
	}

	ns, ok := cmNamespaces.Get(req.Id)
	if !ok {
		AWSErrorf(w, "NamespaceNotFound", http.StatusBadRequest,
			"Namespace '%s' not found", req.Id)
		return
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Namespace": cmNamespaceView(ns),
	})
}

func handleCMDeleteNamespace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id string `json:"Id"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Id == "" {
		AWSError(w, "InvalidInput", "Id is required", http.StatusBadRequest)
		return
	}

	ns, ok := cmNamespaces.Get(req.Id)
	if !ok {
		AWSErrorf(w, "NamespaceNotFound", http.StatusBadRequest,
			"Namespace '%s' not found", req.Id)
		return
	}
	if cmNamespaceHasServices(req.Id) {
		AWSErrorf(w, "ResourceInUse", http.StatusBadRequest,
			"Namespace '%s' contains services and can't be deleted", req.Id)
		return
	}

	cmNamespaces.Delete(req.Id)
	cmNamespaceVPCs.Delete(req.Id)
	cmNamespaceNetworks.Delete(req.Id)
	cmTags.Delete(ns.Arn)
	operationId := generateUUID()
	now := time.Now().Unix()
	cmOperations.Put(operationId, CMOperation{
		OperationId: operationId,
		Status:      "SUCCESS",
		Type:        "DELETE_NAMESPACE",
		NamespaceId: req.Id,
		Targets:     map[string]string{"NAMESPACE": req.Id},
		CreateDate:  now,
		UpdateDate:  now,
	})

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"OperationId": operationId,
	})
}

// cmUpdateNamespaceOp records a successful UPDATE_NAMESPACE operation and writes
// the OperationId response shared by all three Update*Namespace ops.
func cmUpdateNamespaceOp(w http.ResponseWriter, nsId string) {
	operationId := generateUUID()
	now := time.Now().Unix()
	cmOperations.Put(operationId, CMOperation{
		OperationId: operationId,
		Status:      "SUCCESS",
		Type:        "UPDATE_NAMESPACE",
		NamespaceId: nsId,
		Targets:     map[string]string{"NAMESPACE": nsId},
		CreateDate:  now,
		UpdateDate:  now,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"OperationId": operationId,
	})
}

func handleCMUpdateHttpNamespace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id        string `json:"Id"`
		Namespace struct {
			Description *string `json:"Description"`
		} `json:"Namespace"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Id == "" {
		AWSError(w, "InvalidInput", "Id is required", http.StatusBadRequest)
		return
	}
	if _, ok := cmNamespaces.Get(req.Id); !ok {
		AWSErrorf(w, "NamespaceNotFound", http.StatusBadRequest,
			"Namespace '%s' not found", req.Id)
		return
	}
	cmNamespaces.Update(req.Id, func(ns *CMNamespace) {
		if req.Namespace.Description != nil {
			ns.Description = *req.Namespace.Description
		}
	})
	cmUpdateNamespaceOp(w, req.Id)
}

// handleCMUpdatePrivateDnsNamespace updates a DNS_PRIVATE namespace's mutable
// fields: Description and the DNS SOA TTL.
func handleCMUpdatePrivateDnsNamespace(w http.ResponseWriter, r *http.Request) {
	handleCMUpdateDnsNamespace(w, r)
}

// handleCMUpdatePublicDnsNamespace updates a DNS_PUBLIC namespace's mutable
// fields: Description and the DNS SOA TTL.
func handleCMUpdatePublicDnsNamespace(w http.ResponseWriter, r *http.Request) {
	handleCMUpdateDnsNamespace(w, r)
}

func handleCMUpdateDnsNamespace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id        string `json:"Id"`
		Namespace struct {
			Description *string `json:"Description"`
			Properties  *struct {
				DnsProperties *struct {
					SOA *struct {
						TTL int64 `json:"TTL"`
					} `json:"SOA"`
				} `json:"DnsProperties"`
			} `json:"Properties"`
		} `json:"Namespace"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Id == "" {
		AWSError(w, "InvalidInput", "Id is required", http.StatusBadRequest)
		return
	}
	if _, ok := cmNamespaces.Get(req.Id); !ok {
		AWSErrorf(w, "NamespaceNotFound", http.StatusBadRequest,
			"Namespace '%s' not found", req.Id)
		return
	}
	cmNamespaces.Update(req.Id, func(ns *CMNamespace) {
		if req.Namespace.Description != nil {
			ns.Description = *req.Namespace.Description
		}
		if p := req.Namespace.Properties; p != nil && p.DnsProperties != nil && p.DnsProperties.SOA != nil {
			if ns.Properties == nil {
				ns.Properties = &CMNamespaceProperties{}
			}
			if ns.Properties.DnsProperties == nil {
				ns.Properties.DnsProperties = &CMDnsProperties{}
			}
			ns.Properties.DnsProperties.SOA = &struct {
				TTL int64 `json:"TTL"`
			}{TTL: p.DnsProperties.SOA.TTL}
		}
	})
	cmUpdateNamespaceOp(w, req.Id)
}

func handleCMCreateService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                    string                     `json:"Name"`
		NamespaceId             string                     `json:"NamespaceId"`
		Description             string                     `json:"Description"`
		CreatorRequestId        string                     `json:"CreatorRequestId"`
		DnsConfig               *CMDnsConfig               `json:"DnsConfig"`
		HealthCheckConfig       *CMHealthCheckConfig       `json:"HealthCheckConfig"`
		HealthCheckCustomConfig *CMHealthCheckCustomConfig `json:"HealthCheckCustomConfig"`
		Type                    string                     `json:"Type"`
		Tags                    []cmTag                    `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		AWSError(w, "InvalidInput", "Name is required", http.StatusBadRequest)
		return
	}

	namespaceId := req.NamespaceId
	if namespaceId == "" && req.DnsConfig != nil {
		namespaceId = req.DnsConfig.NamespaceId
	}
	if namespaceId != "" {
		if _, ok := cmNamespaces.Get(namespaceId); !ok {
			AWSErrorf(w, "NamespaceNotFound", http.StatusBadRequest,
				"Namespace '%s' not found", namespaceId)
			return
		}
	}
	// Service names are unique within a namespace.
	for _, existing := range cmServices.List() {
		if existing.NamespaceId == namespaceId && existing.Name == req.Name {
			AWSErrorf(w, "ServiceAlreadyExists", http.StatusBadRequest,
				"A service with the name '%s' already exists in namespace '%s'", req.Name, namespaceId)
			return
		}
	}

	svcId := "srv-" + generateUUID()[:16]
	if req.HealthCheckCustomConfig != nil {
		// AWS Cloud Map always uses a failure threshold of one for custom
		// health checks, regardless of the value supplied by the caller.
		req.HealthCheckCustomConfig.FailureThreshold = 1
	}
	svc := CMService{
		Id:                      svcId,
		Arn:                     cmArn("service", svcId),
		Name:                    req.Name,
		NamespaceId:             namespaceId,
		Description:             req.Description,
		CreatorRequestId:        req.CreatorRequestId,
		DnsConfig:               req.DnsConfig,
		HealthCheckConfig:       req.HealthCheckConfig,
		HealthCheckCustomConfig: req.HealthCheckCustomConfig,
		Type:                    cmServiceType(req.Type, namespaceId, req.DnsConfig),
		CreateDate:              time.Now().Unix(),
	}
	cmServices.Put(svcId, svc)
	cmPutResourceTags(svc.Arn, req.Tags)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Service": svc,
	})
}

func handleCMGetService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id string `json:"Id"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Id == "" {
		AWSError(w, "InvalidInput", "Id is required", http.StatusBadRequest)
		return
	}

	svc, ok := cmServices.Get(req.Id)
	if !ok {
		AWSErrorf(w, "ServiceNotFound", http.StatusBadRequest,
			"Service '%s' not found", req.Id)
		return
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Service": svc,
	})
}

// cmServiceType derives a service's discovery Type from the explicit request
// value (if any), the owning namespace, and whether the service has DNS records
// — mirroring how Cloud Map labels a service DNS / DNS_HTTP / HTTP. A service in
// an HTTP namespace, or one without DNS records, is HTTP; a service in a DNS
// namespace with DNS records is DNS (DNS_HTTP when discoverable both ways).
func cmServiceType(explicit, namespaceId string, dnsConfig *CMDnsConfig) string {
	if explicit != "" {
		return explicit
	}
	hasDNS := dnsConfig != nil && len(dnsConfig.DnsRecords) > 0
	if ns, ok := cmNamespaces.Get(namespaceId); ok {
		switch ns.Type {
		case "HTTP":
			return "HTTP"
		case "DNS_PRIVATE", "DNS_PUBLIC":
			if hasDNS {
				return "DNS_HTTP"
			}
			return "HTTP"
		}
	}
	if hasDNS {
		return "DNS"
	}
	return "HTTP"
}

func handleCMUpdateService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id      string `json:"Id"`
		Service struct {
			Description       *string              `json:"Description"`
			DnsConfig         *CMDnsConfig         `json:"DnsConfig"`
			HealthCheckConfig *CMHealthCheckConfig `json:"HealthCheckConfig"`
		} `json:"Service"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Id == "" {
		AWSError(w, "InvalidInput", "Id is required", http.StatusBadRequest)
		return
	}
	svc, ok := cmServices.Get(req.Id)
	if !ok {
		AWSErrorf(w, "ServiceNotFound", http.StatusBadRequest,
			"Service '%s' not found", req.Id)
		return
	}
	cmServices.Update(req.Id, func(s *CMService) {
		if req.Service.Description != nil {
			s.Description = *req.Service.Description
		}
		if req.Service.DnsConfig != nil {
			s.DnsConfig = req.Service.DnsConfig
		}
		if req.Service.HealthCheckConfig != nil {
			s.HealthCheckConfig = req.Service.HealthCheckConfig
		}
	})

	operationId := generateUUID()
	now := time.Now().Unix()
	cmOperations.Put(operationId, CMOperation{
		OperationId: operationId,
		Status:      "SUCCESS",
		Type:        "UPDATE_SERVICE",
		NamespaceId: svc.NamespaceId,
		ServiceId:   req.Id,
		Targets:     map[string]string{"SERVICE": req.Id},
		CreateDate:  now,
		UpdateDate:  now,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"OperationId": operationId,
	})
}

func handleCMGetServiceAttributes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceId string `json:"ServiceId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceId == "" {
		AWSError(w, "InvalidInput", "ServiceId is required", http.StatusBadRequest)
		return
	}
	svc, ok := cmServices.Get(req.ServiceId)
	if !ok {
		AWSErrorf(w, "ServiceNotFound", http.StatusBadRequest,
			"Service '%s' not found", req.ServiceId)
		return
	}
	attrs, ok := cmServiceAttributes.Get(req.ServiceId)
	if !ok {
		attrs = map[string]string{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ServiceAttributes": map[string]any{
			"ServiceArn": svc.Arn,
			"Attributes": attrs,
		},
	})
}

func handleCMUpdateServiceAttributes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceId  string            `json:"ServiceId"`
		Attributes map[string]string `json:"Attributes"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceId == "" {
		AWSError(w, "InvalidInput", "ServiceId is required", http.StatusBadRequest)
		return
	}
	if _, ok := cmServices.Get(req.ServiceId); !ok {
		AWSErrorf(w, "ServiceNotFound", http.StatusBadRequest,
			"Service '%s' not found", req.ServiceId)
		return
	}
	attrs, ok := cmServiceAttributes.Get(req.ServiceId)
	if !ok {
		attrs = map[string]string{}
	}
	merged := make(map[string]string, len(attrs)+len(req.Attributes))
	for k, v := range attrs {
		merged[k] = v
	}
	for k, v := range req.Attributes {
		merged[k] = v
	}
	cmServiceAttributes.Put(req.ServiceId, merged)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCMDeleteServiceAttributes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceId  string   `json:"ServiceId"`
		Attributes []string `json:"Attributes"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceId == "" {
		AWSError(w, "InvalidInput", "ServiceId is required", http.StatusBadRequest)
		return
	}
	if _, ok := cmServices.Get(req.ServiceId); !ok {
		AWSErrorf(w, "ServiceNotFound", http.StatusBadRequest,
			"Service '%s' not found", req.ServiceId)
		return
	}
	if attrs, ok := cmServiceAttributes.Get(req.ServiceId); ok {
		next := make(map[string]string, len(attrs))
		for k, v := range attrs {
			next[k] = v
		}
		for _, k := range req.Attributes {
			delete(next, k)
		}
		cmServiceAttributes.Put(req.ServiceId, next)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCMRegisterInstance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceId        string            `json:"ServiceId"`
		InstanceId       string            `json:"InstanceId"`
		Attributes       map[string]string `json:"Attributes"`
		CreatorRequestId string            `json:"CreatorRequestId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceId == "" || req.InstanceId == "" {
		AWSError(w, "InvalidInput", "ServiceId and InstanceId are required", http.StatusBadRequest)
		return
	}

	svc, ok := cmServices.Get(req.ServiceId)
	if !ok {
		AWSErrorf(w, "ServiceNotFound", http.StatusBadRequest,
			"Service '%s' not found", req.ServiceId)
		return
	}
	ns, nsOk := cmNamespaces.Get(svc.NamespaceId)
	if !nsOk {
		AWSErrorf(w, "NamespaceNotFound", http.StatusBadRequest,
			"Namespace '%s' not found", svc.NamespaceId)
		return
	}

	// Store the instance BEFORE realizing DNS so the realization below sees
	// this registration. Real Cloud Map registers an instance per service
	// (ServiceId+InstanceId), so one container (instance ID) may back several
	// services — i.e. resolve under several DNS names that all point at its IP.
	// The realization paths gather the full set of names for the container.
	instance := CMInstance{
		Id:               req.InstanceId,
		Attributes:       req.Attributes,
		CreatorRequestId: req.CreatorRequestId,
	}
	key := cmInstanceKey(req.ServiceId, req.InstanceId)
	_, existed := cmInstances.Get(key)
	cmInstances.Put(key, instance)
	if !existed {
		cmServices.Update(req.ServiceId, func(svc *CMService) {
			svc.InstanceCount++
		})
	}
	cmBumpServiceRevision(req.ServiceId)
	rollback := func() {
		if !existed {
			cmInstances.Delete(key)
			cmServices.Update(req.ServiceId, func(svc *CMService) {
				if svc.InstanceCount > 0 {
					svc.InstanceCount--
				}
			})
		}
		cmBumpServiceRevision(req.ServiceId)
	}

	containerName := resolveTaskContainerForInstance(req.InstanceId)
	switch {
	case cmContainerUsesHostEntries(containerName):
		// netns/awsvpc tier: the real ENI occupies eth0, so DNS is realized via
		// /etc/hosts entries (syncCMNamespaceHosts already gathers every service
		// name per instance IP — multi-name aware).
		if err := syncCMNamespaceHosts(svc.NamespaceId); err != nil {
			rollback()
			AWSErrorf(w, "InternalFailure", http.StatusInternalServerError,
				"failed to update Cloud Map task hosts: %v", err)
			return
		}
	case containerName != "":
		// Docker-network tier: realize EVERY service name this container backs
		// as a DNS alias on the namespace network, so siblings resolve it by any
		// of its registered names (e.g. a service alias `redis` AND its task
		// hostname both point at the redis container).
		if err := realizeCMContainerDockerAliases(ns, containerName); err != nil {
			rollback()
			AWSErrorf(w, "InternalFailure", http.StatusInternalServerError,
				"failed to connect task container to Cloud Map namespace network: %v", err)
			return
		}
	}

	cmWriteInstanceOperation(w, "REGISTER_INSTANCE", svc, req.InstanceId)
}

// cmWriteInstanceOperation records the SUCCESS instance operation
// (REGISTER_INSTANCE / DEREGISTER_INSTANCE) a caller polls with GetOperation
// and writes the OperationId response.
func cmWriteInstanceOperation(w http.ResponseWriter, opType string, svc CMService, instanceId string) {
	operationId := generateUUID()
	now := time.Now().Unix()
	targets := map[string]string{
		"INSTANCE": instanceId,
		"SERVICE":  svc.Id,
	}
	if svc.NamespaceId != "" {
		targets["NAMESPACE"] = svc.NamespaceId
	}
	cmOperations.Put(operationId, CMOperation{
		OperationId: operationId,
		Status:      "SUCCESS",
		Type:        opType,
		NamespaceId: svc.NamespaceId,
		ServiceId:   svc.Id,
		Targets:     targets,
		CreateDate:  now,
		UpdateDate:  now,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"OperationId": operationId,
	})
}

// cmDockerAliasesForContainer returns every DNS alias the container must answer
// to on the namespace's Docker network: for each Cloud Map service whose
// registered instance maps to containerName, both the short service name AND
// its `<service>.<namespace>` FQDN (so e.g. `redis` and `redis.skls-net.local`
// both resolve — the runtime's network DNS resolves both as network aliases).
// This is the Docker-network-tier analogue of syncCMNamespaceHosts, which adds
// the same two forms as /etc/hosts entries for the netns tier.
func cmDockerAliasesForContainer(namespaceID, containerName string) []string {
	nsName := ""
	if ns, ok := cmNamespaces.Get(namespaceID); ok {
		nsName = ns.Name
	}
	seen := make(map[string]struct{})
	var aliases []string
	add := func(name string) {
		if name == "" {
			return
		}
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		aliases = append(aliases, name)
	}
	// Resolve every instance ID to its task container in a single combined pass
	// over instances and ECS tasks, so the service×instance loop below is a map
	// lookup instead of re-scanning all instances and all tasks per pair.
	instanceContainers := resolveTaskContainersForInstances()
	for _, svc := range cmServices.List() {
		if svc.NamespaceId != namespaceID {
			continue
		}
		for _, inst := range cmServiceInstances(svc.Id) {
			if instanceContainers[inst.Id] != containerName {
				continue
			}
			add(svc.Name)
			if nsName != "" {
				add(svc.Name + "." + nsName)
			}
		}
	}
	return aliases
}

// resolveTaskContainersForInstances maps every Cloud Map instance ID to its task
// container name in two single passes — one to index ECS tasks by private IP,
// one over instances — replacing the per-instance re-scan of all instances
// (cmInstanceIPv4) and all ECS tasks (resolveTaskContainerForInstance) that a
// naive nested loop incurs. The per-instance result is identical to
// resolveTaskContainerForInstance(id).
func resolveTaskContainersForInstances() map[string]string {
	containerByIP := make(map[string]string)
	for _, task := range ecsTasks.List() {
		ip := ecsTaskPrivateIP(task)
		if ip == "" {
			continue
		}
		// Derive task UUID from ARN ("arn:…/task/<cluster>/<taskId>").
		taskId := task.TaskArn
		if i := lastSlash(taskId); i >= 0 {
			taskId = taskId[i+1:]
		}
		if len(taskId) < 12 {
			continue
		}
		containerName := "sockerless-sim-aws-task-" + taskId[:12]
		if taskHasENI(task) && ec2ECSRealNetAvailable() {
			containerName += "-pause"
		}
		containerByIP[ip] = containerName
	}
	out := make(map[string]string)
	for _, inst := range cmInstances.List() {
		if _, dup := out[inst.Id]; dup {
			continue
		}
		ip := inst.Attributes["AWS_INSTANCE_IPV4"]
		if ip == "" {
			continue
		}
		if name, ok := containerByIP[ip]; ok {
			out[inst.Id] = name
		}
	}
	return out
}

// realizeCMContainerDockerAliases (re)attaches a task container to its Cloud Map
// namespace network with the full set of service-name aliases it currently
// backs. Docker rejects connecting an already-connected container and can't add
// an alias to a live endpoint, so it re-attaches with the full set (the same
// disconnect-then-connect pattern the azure ACA multi-CNAME path uses). The
// disconnect is best-effort: a not-yet-attached container errors there, which
// the connect corrects; when the container backs no services it stays detached.
func realizeCMContainerDockerAliases(ns CMNamespace, containerName string) error {
	networkName, err := ensureCMNamespaceDockerNetwork(ns)
	if err != nil {
		return err
	}
	aliases := cmDockerAliasesForContainer(ns.Id, containerName)
	_ = sim.DisconnectContainerFromNetwork(containerName, networkName)
	if len(aliases) == 0 {
		return nil
	}
	return sim.ConnectContainerToNetwork(containerName, networkName, aliases)
}

// resolveTaskContainerForInstance maps a Cloud Map instance to the simulator's
// Docker container by the registered `AWS_INSTANCE_IPV4` attribute — the
// faithful Cloud Map mechanism (a client registers an instance with the task's
// IP; the sim matches it to the task carrying that private IP), not any
// sockerless-specific tag. On the netns awsvpc fabric the pause container owns
// the namespace; that tier cannot join Docker's DNS network after the real ENI
// occupies eth0, so Cloud Map uses host entries in the task container instead.
func resolveTaskContainerForInstance(instanceId string) string {
	ip := cmInstanceIPv4(instanceId)
	if ip == "" {
		return ""
	}
	for _, task := range ecsTasks.List() {
		if ecsTaskPrivateIP(task) != ip {
			continue
		}
		// Derive task UUID from ARN ("arn:…/task/<cluster>/<taskId>").
		taskId := task.TaskArn
		if i := lastSlash(taskId); i >= 0 {
			taskId = taskId[i+1:]
		}
		if len(taskId) < 12 {
			return ""
		}
		containerName := "sockerless-sim-aws-task-" + taskId[:12]
		if taskHasENI(task) && ec2ECSRealNetAvailable() {
			return containerName + "-pause"
		}
		return containerName
	}
	return ""
}

// cmInstanceIPv4 returns a registered Cloud Map instance's AWS_INSTANCE_IPV4
// attribute (the standard address attribute), searched across services.
func cmInstanceIPv4(instanceID string) string {
	for _, inst := range cmInstances.List() {
		if inst.Id == instanceID {
			return inst.Attributes["AWS_INSTANCE_IPV4"]
		}
	}
	return ""
}

// ecsTaskPrivateIP returns a task's private IPv4 address from its awsvpc ENI
// attachment's `privateIPv4Address` detail (the value DescribeTasks surfaces and
// the ECS backend registers as the Cloud Map instance's AWS_INSTANCE_IPV4).
func ecsTaskPrivateIP(task ECSTask) string {
	for _, att := range task.Attachments {
		if ip := ecsTaskDetail(att.Details, "privateIPv4Address"); ip != "" {
			return ip
		}
	}
	return ""
}

func cmContainerUsesHostEntries(containerName string) bool {
	return strings.HasSuffix(containerName, "-pause")
}

func cmNamespaceHasHostEntryTargets(namespaceID string) bool {
	if _, ok := cmNamespaces.Get(namespaceID); !ok || !ec2ECSRealNetAvailable() {
		return false
	}
	vpcID, _ := cmNamespaceVPCs.Get(namespaceID)
	for _, task := range ecsTasks.List() {
		if task.LastStatus != ECSTaskStatusRunning || !taskHasENI(task) {
			continue
		}
		if vpcID == "" || taskVPCID(task) == vpcID {
			return true
		}
	}
	return false
}

func cmTaskContainerName(task ECSTask) string {
	taskID := task.TaskArn
	if i := lastSlash(taskID); i >= 0 {
		taskID = taskID[i+1:]
	}
	if len(taskID) < 12 {
		return ""
	}
	return "sockerless-sim-aws-task-" + taskID[:12]
}

func taskVPCID(task ECSTask) string {
	for _, att := range task.Attachments {
		if att.Type != "ElasticNetworkInterface" {
			continue
		}
		for _, d := range att.Details {
			if d.Name != "subnetId" {
				continue
			}
			if subnet, ok := ec2Subnets.Get(d.Value); ok {
				return subnet.VpcId
			}
		}
	}
	return ""
}

func syncCMNamespaceHosts(namespaceID string) error {
	ns, ok := cmNamespaces.Get(namespaceID)
	if !ok {
		return fmt.Errorf("namespace %s not found", namespaceID)
	}
	vpcID, _ := cmNamespaceVPCs.Get(namespaceID)

	var entries []sim.HostEntry
	for _, svc := range cmServices.List() {
		if svc.NamespaceId != namespaceID {
			continue
		}
		for _, inst := range cmServiceInstances(svc.Id) {
			ip := inst.Attributes["AWS_INSTANCE_IPV4"]
			if ip == "" {
				continue
			}
			entries = append(entries, sim.HostEntry{IP: ip, Name: svc.Name})
			if ns.Name != "" {
				entries = append(entries, sim.HostEntry{IP: ip, Name: svc.Name + "." + ns.Name})
			}
		}
	}

	marker := "sockerless-cloudmap-" + namespaceID
	for _, task := range ecsTasks.List() {
		if task.LastStatus != ECSTaskStatusRunning || !taskHasENI(task) {
			continue
		}
		if vpcID != "" && taskVPCID(task) != vpcID {
			continue
		}
		containerName := cmTaskContainerName(task)
		if containerName == "" {
			continue
		}
		if err := sim.SyncContainerHostEntries(containerName, marker, entries); err != nil {
			return fmt.Errorf("sync hosts for %s: %w", containerName, err)
		}
	}
	return nil
}

func taskHasENI(task ECSTask) bool {
	for _, att := range task.Attachments {
		if att.Type == "ElasticNetworkInterface" {
			return true
		}
	}
	return false
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

func handleCMDeregisterInstance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceId  string `json:"ServiceId"`
		InstanceId string `json:"InstanceId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceId == "" || req.InstanceId == "" {
		AWSError(w, "InvalidInput", "ServiceId and InstanceId are required", http.StatusBadRequest)
		return
	}

	if _, ok := cmServices.Get(req.ServiceId); !ok {
		AWSErrorf(w, "ServiceNotFound", http.StatusBadRequest,
			"Service '%s' not found", req.ServiceId)
		return
	}
	key := cmInstanceKey(req.ServiceId, req.InstanceId)
	if !cmInstances.Delete(key) {
		AWSErrorf(w, "InstanceNotFound", http.StatusBadRequest,
			"Instance '%s' not found", req.InstanceId)
		return
	}

	// Update service instance count
	cmServices.Update(req.ServiceId, func(svc *CMService) {
		if svc.InstanceCount > 0 {
			svc.InstanceCount--
		}
	})
	cmBumpServiceRevision(req.ServiceId)

	svc, svcOk := cmServices.Get(req.ServiceId)
	if svcOk {
		containerName := resolveTaskContainerForInstance(req.InstanceId)
		networkName, _ := cmNamespaceNetworks.Get(svc.NamespaceId)
		if cmContainerUsesHostEntries(containerName) || cmNamespaceHasHostEntryTargets(svc.NamespaceId) {
			if err := syncCMNamespaceHosts(svc.NamespaceId); err != nil {
				AWSErrorf(w, "InternalFailure", http.StatusInternalServerError,
					"failed to update Cloud Map task hosts: %v", err)
				return
			}
		} else if ns, nsOk := cmNamespaces.Get(svc.NamespaceId); nsOk && networkName != "" {
			// Re-realize the container's REMAINING aliases: it may still back
			// other services in the namespace, so a plain disconnect would drop
			// names that are still registered. realizeCMContainerDockerAliases
			// reconnects with the reduced set, or detaches when none remain.
			if containerName != "" {
				_ = realizeCMContainerDockerAliases(ns, containerName)
			}
		}
	}

	cmWriteInstanceOperation(w, "DEREGISTER_INSTANCE", svc, req.InstanceId)
}

func handleCMListInstances(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceId  string `json:"ServiceId"`
		MaxResults *int32 `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceId == "" {
		AWSError(w, "InvalidInput", "ServiceId is required", http.StatusBadRequest)
		return
	}
	if _, ok := cmServices.Get(req.ServiceId); !ok {
		AWSErrorf(w, "ServiceNotFound", http.StatusBadRequest,
			"Service '%s' not found", req.ServiceId)
		return
	}

	page, next := awsPage(cmServiceInstances(req.ServiceId), req.NextToken, awsMaxResults(req.MaxResults), 100)
	summaries := make([]map[string]any, 0, len(page))
	for _, inst := range page {
		summaries = append(summaries, cmInstanceSummary(inst))
	}
	out := map[string]any{"Instances": summaries}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleCMListNamespaces(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters []struct {
			Name      string   `json:"Name"`
			Values    []string `json:"Values"`
			Condition string   `json:"Condition"`
		} `json:"Filters"`
		MaxResults *int32 `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidInput", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}

	namespaces := sortBy(cmNamespaces.List(), func(ns CMNamespace) string { return ns.Id })
	if len(req.Filters) > 0 {
		filtered := make([]CMNamespace, 0, len(namespaces))
		for _, ns := range namespaces {
			match := true
			for _, f := range req.Filters {
				var actual string
				switch f.Name {
				case "TYPE":
					actual = ns.Type
				case "NAME":
					actual = ns.Name
				case "HTTP_NAME":
					if ns.Properties != nil && ns.Properties.HttpProperties != nil {
						actual = ns.Properties.HttpProperties.HttpName
					}
				case "RESOURCE_OWNER":
					actual = awsAccountID()
				default:
					AWSErrorf(w, "InvalidInput", http.StatusBadRequest,
						"'%s' is not a valid namespace filter name", f.Name)
					return
				}
				if !cmFilterMatch(f.Condition, f.Values, actual) {
					match = false
					break
				}
			}
			if match {
				filtered = append(filtered, ns)
			}
		}
		namespaces = filtered
	}

	page, next := awsPage(namespaces, req.NextToken, awsMaxResults(req.MaxResults), 100)
	summaries := make([]map[string]any, 0, len(page))
	for _, ns := range page {
		summaries = append(summaries, cmNamespaceSummary(ns))
	}
	out := map[string]any{"Namespaces": summaries}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleCMListServices(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters []struct {
			Name      string   `json:"Name"`
			Values    []string `json:"Values"`
			Condition string   `json:"Condition"`
		} `json:"Filters"`
		MaxResults *int32 `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidInput", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}

	services := sortBy(cmServices.List(), func(svc CMService) string { return svc.Id })

	if len(req.Filters) > 0 {
		filtered := make([]CMService, 0, len(services))
		for _, svc := range services {
			match := true
			for _, f := range req.Filters {
				var actual string
				switch f.Name {
				case "NAMESPACE_ID":
					actual = svc.NamespaceId
				case "RESOURCE_OWNER":
					actual = awsAccountID()
				default:
					AWSErrorf(w, "InvalidInput", http.StatusBadRequest,
						"'%s' is not a valid service filter name", f.Name)
					return
				}
				if !cmFilterMatch(f.Condition, f.Values, actual) {
					match = false
					break
				}
			}
			if match {
				filtered = append(filtered, svc)
			}
		}
		services = filtered
	}

	page, next := awsPage(services, req.NextToken, awsMaxResults(req.MaxResults), 100)
	summaries := make([]map[string]any, 0, len(page))
	for _, svc := range page {
		summaries = append(summaries, cmServiceSummary(svc))
	}
	out := map[string]any{"Services": summaries}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// cmServiceSummary projects a stored service onto the ServiceSummary
// shape. NamespaceId is a Service (GetService/CreateService) member kept
// in the store for namespace filtering and DNS wiring; ServiceSummary
// has no such member.
func cmServiceSummary(svc CMService) map[string]any {
	out := map[string]any{
		"Id":            svc.Id,
		"Arn":           svc.Arn,
		"Name":          svc.Name,
		"CreateDate":    svc.CreateDate,
		"InstanceCount": svc.InstanceCount,
	}
	if svc.Description != "" {
		out["Description"] = svc.Description
	}
	if svc.DnsConfig != nil {
		out["DnsConfig"] = svc.DnsConfig
	}
	if svc.HealthCheckConfig != nil {
		out["HealthCheckConfig"] = svc.HealthCheckConfig
	}
	if svc.HealthCheckCustomConfig != nil {
		out["HealthCheckCustomConfig"] = svc.HealthCheckCustomConfig
	}
	if svc.Type != "" {
		out["Type"] = svc.Type
	}
	return out
}

func handleCMDeleteService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id string `json:"Id"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Id == "" {
		AWSError(w, "InvalidInput", "Id is required", http.StatusBadRequest)
		return
	}

	svc, ok := cmServices.Get(req.Id)
	if !ok {
		AWSErrorf(w, "ServiceNotFound", http.StatusBadRequest,
			"Service '%s' not found", req.Id)
		return
	}
	if cmServiceHasInstances(req.Id) {
		AWSErrorf(w, "ResourceInUse", http.StatusBadRequest,
			"Service '%s' contains instances and can't be deleted", req.Id)
		return
	}

	cmServices.Delete(req.Id)
	cmServiceAttributes.Delete(req.Id)
	cmServiceRevisions.Delete(req.Id)
	cmTags.Delete(svc.Arn)

	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func ensureCMNamespaceDockerNetwork(ns CMNamespace) (string, error) {
	if existing, ok := cmNamespaceNetworks.Get(ns.Id); ok && existing != "" {
		return existing, nil
	}
	networkName := "sim-" + ns.Id
	if _, err := sim.EnsureDockerNetwork(networkName); err != nil {
		return "", err
	}
	cmNamespaceNetworks.Put(ns.Id, networkName)
	return networkName, nil
}

func cmNamespaceHasServices(namespaceId string) bool {
	for _, svc := range cmServices.List() {
		if svc.NamespaceId == namespaceId {
			return true
		}
	}
	return false
}

func cmServiceHasInstances(serviceId string) bool {
	for _, inst := range cmInstances.List() {
		if _, ok := cmInstances.Get(cmInstanceKey(serviceId, inst.Id)); ok {
			return true
		}
	}
	return false
}

func handleCMGetOperation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OperationId string `json:"OperationId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.OperationId == "" {
		AWSError(w, "InvalidInput", "OperationId is required", http.StatusBadRequest)
		return
	}
	op, ok := cmOperations.Get(req.OperationId)
	if !ok {
		AWSErrorf(w, "OperationNotFound", http.StatusBadRequest,
			"Operation '%s' not found", req.OperationId)
		return
	}

	result := map[string]any{
		"Id":     op.OperationId,
		"Status": op.Status,
	}
	if op.Type != "" {
		result["Type"] = op.Type
	}
	if op.CreateDate != 0 {
		result["CreateDate"] = op.CreateDate
	}
	if op.UpdateDate != 0 {
		result["UpdateDate"] = op.UpdateDate
	}
	if len(op.Targets) > 0 {
		result["Targets"] = op.Targets
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Operation": result,
	})
}

func handleCMListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string `json:"ResourceARN"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidInput", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if req.ResourceARN == "" {
		AWSError(w, "InvalidInput", "ResourceARN is required", http.StatusBadRequest)
		return
	}
	if !cmResourceExists(req.ResourceARN) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Resource '%s' not found", req.ResourceARN)
		return
	}
	stored, _ := cmTags.Get(req.ResourceARN)
	tags := make([]cmTag, 0, len(stored))
	for k, v := range stored {
		tags = append(tags, cmTag{Key: k, Value: v})
	}
	sortBy(tags, func(t cmTag) string { return t.Key })
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Tags": tags,
	})
}

func handleCMTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string  `json:"ResourceARN"`
		Tags        []cmTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidInput", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if req.ResourceARN == "" {
		AWSError(w, "InvalidInput", "ResourceARN is required", http.StatusBadRequest)
		return
	}
	if !cmResourceExists(req.ResourceARN) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Resource '%s' not found", req.ResourceARN)
		return
	}
	stored, _ := cmTags.Get(req.ResourceARN)
	merged := make(map[string]string, len(stored)+len(req.Tags))
	for k, v := range stored {
		merged[k] = v
	}
	for _, t := range req.Tags {
		if t.Key == "" {
			AWSError(w, "InvalidInput", "Tag keys can't be empty", http.StatusBadRequest)
			return
		}
		merged[t.Key] = t.Value
	}
	if len(merged) > cmMaxTagsPerResource {
		AWSErrorf(w, "TooManyTagsException", http.StatusBadRequest,
			"Resource '%s' would exceed the limit of %d tags", req.ResourceARN, cmMaxTagsPerResource)
		return
	}
	cmTags.Put(req.ResourceARN, merged)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCMUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string   `json:"ResourceARN"`
		TagKeys     []string `json:"TagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidInput", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if req.ResourceARN == "" {
		AWSError(w, "InvalidInput", "ResourceARN is required", http.StatusBadRequest)
		return
	}
	if !cmResourceExists(req.ResourceARN) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Resource '%s' not found", req.ResourceARN)
		return
	}
	if stored, ok := cmTags.Get(req.ResourceARN); ok {
		next := make(map[string]string, len(stored))
		for k, v := range stored {
			next[k] = v
		}
		for _, k := range req.TagKeys {
			delete(next, k)
		}
		cmTags.Put(req.ResourceARN, next)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCMDiscoverInstances(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NamespaceName      string            `json:"NamespaceName"`
		ServiceName        string            `json:"ServiceName"`
		HealthStatus       string            `json:"HealthStatus"`
		MaxResults         *int32            `json:"MaxResults"`
		QueryParameters    map[string]string `json:"QueryParameters"`
		OptionalParameters map[string]string `json:"OptionalParameters"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.NamespaceName == "" || req.ServiceName == "" {
		AWSError(w, "InvalidInput", "NamespaceName and ServiceName are required", http.StatusBadRequest)
		return
	}

	// Find the namespace by name
	var targetNs *CMNamespace
	for _, ns := range cmNamespaces.List() {
		if ns.Name == req.NamespaceName {
			nsCopy := ns
			targetNs = &nsCopy
			break
		}
	}
	if targetNs == nil {
		AWSErrorf(w, "NamespaceNotFound", http.StatusBadRequest,
			"Namespace '%s' not found", req.NamespaceName)
		return
	}

	// Find the service by name in this namespace
	var targetSvc *CMService
	for _, svc := range cmServices.List() {
		if svc.Name == req.ServiceName && svc.NamespaceId == targetNs.Id {
			svcCopy := svc
			targetSvc = &svcCopy
			break
		}
	}
	if targetSvc == nil {
		AWSErrorf(w, "ServiceNotFound", http.StatusBadRequest,
			"Service '%s' not found in namespace '%s'", req.ServiceName, req.NamespaceName)
		return
	}

	// Collect the service's instances. Each reports its effective health — the
	// custom health status a caller set, else the AWS_INIT_HEALTH_STATUS
	// attribute it registered with, defaulting to HEALTHY — and the
	// HealthStatus request filter scopes the result the way real Cloud Map does.
	type discovered struct {
		entry  map[string]any
		health string
	}
	var all []discovered
	for _, inst := range cmServiceInstances(targetSvc.Id) {
		// QueryParameters restrict discovery to instances carrying every
		// listed attribute with the listed value.
		if !cmInstanceMatchesParams(inst, req.QueryParameters) {
			continue
		}
		health := cmInstanceHealth(inst)
		attrs := inst.Attributes
		if attrs == nil {
			attrs = map[string]string{}
		}
		all = append(all, discovered{
			entry: map[string]any{
				"InstanceId":    inst.Id,
				"NamespaceName": req.NamespaceName,
				"ServiceName":   req.ServiceName,
				"HealthStatus":  health,
				"Attributes":    attrs,
			},
			health: health,
		})
	}
	// OptionalParameters narrow the result only when at least one instance
	// matches them; otherwise the unnarrowed set is returned.
	if len(req.OptionalParameters) > 0 {
		var preferred []discovered
		for _, d := range all {
			attrs, _ := d.entry["Attributes"].(map[string]string)
			if cmAttributesMatchParams(attrs, req.OptionalParameters) {
				preferred = append(preferred, d)
			}
		}
		if len(preferred) > 0 {
			all = preferred
		}
	}
	pick := func(keep func(string) bool) []map[string]any {
		out := []map[string]any{}
		for _, d := range all {
			if keep(d.health) {
				out = append(out, d.entry)
			}
		}
		return out
	}
	var httpInstances []map[string]any
	switch req.HealthStatus {
	case "HEALTHY":
		httpInstances = pick(func(h string) bool { return h == "HEALTHY" })
	case "UNHEALTHY":
		httpInstances = pick(func(h string) bool { return h == "UNHEALTHY" })
	case "ALL":
		httpInstances = pick(func(string) bool { return true })
	default: // omitted / HEALTHY_OR_ELSE_ALL: healthy, or all if none are healthy
		httpInstances = pick(func(h string) bool { return h == "HEALTHY" })
		if len(httpInstances) == 0 {
			httpInstances = pick(func(string) bool { return true })
		}
	}
	if max := awsMaxResults(req.MaxResults); max > 0 && len(httpInstances) > max {
		httpInstances = httpInstances[:max]
	}

	revision, _ := cmServiceRevisions.Get(targetSvc.Id)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Instances":         httpInstances,
		"InstancesRevision": revision,
	})
}

// cmInstanceMatchesParams reports whether an instance carries every attribute
// in params with the same value — the DiscoverInstances QueryParameters /
// OptionalParameters matching rule.
func cmInstanceMatchesParams(inst CMInstance, params map[string]string) bool {
	return cmAttributesMatchParams(inst.Attributes, params)
}

func cmAttributesMatchParams(attrs, params map[string]string) bool {
	for k, v := range params {
		if attrs[k] != v {
			return false
		}
	}
	return true
}

// cmBumpServiceRevision increments a service's instance-list revision. The
// revision starts at 0 and increases on every RegisterInstance and
// DeregisterInstance, matching the Cloud Map InstancesRevision contract
// (health-status updates do NOT bump it).
func cmBumpServiceRevision(serviceId string) {
	cur, _ := cmServiceRevisions.Get(serviceId)
	cmServiceRevisions.Put(serviceId, cur+1)
}

// cmInstanceHealth returns an instance's effective health: the custom health
// status when set (HealthCheckCustomConfig services), otherwise the registered
// AWS_INIT_HEALTH_STATUS attribute, defaulting to HEALTHY.
func cmInstanceHealth(inst CMInstance) string {
	if inst.CustomHealthStatus != "" {
		return inst.CustomHealthStatus
	}
	if h := inst.Attributes["AWS_INIT_HEALTH_STATUS"]; h != "" {
		return h
	}
	return "HEALTHY"
}

func handleCMGetInstance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceId  string `json:"ServiceId"`
		InstanceId string `json:"InstanceId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceId == "" || req.InstanceId == "" {
		AWSError(w, "InvalidInput", "ServiceId and InstanceId are required", http.StatusBadRequest)
		return
	}
	if _, ok := cmServices.Get(req.ServiceId); !ok {
		AWSErrorf(w, "ServiceNotFound", http.StatusBadRequest,
			"Service '%s' not found", req.ServiceId)
		return
	}
	inst, ok := cmInstances.Get(cmInstanceKey(req.ServiceId, req.InstanceId))
	if !ok {
		AWSErrorf(w, "InstanceNotFound", http.StatusBadRequest,
			"Instance '%s' not found", req.InstanceId)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Instance": cmInstanceView(inst),
	})
}

func handleCMUpdateInstanceCustomHealthStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceId  string `json:"ServiceId"`
		InstanceId string `json:"InstanceId"`
		Status     string `json:"Status"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceId == "" || req.InstanceId == "" {
		AWSError(w, "InvalidInput", "ServiceId and InstanceId are required", http.StatusBadRequest)
		return
	}
	if req.Status != "HEALTHY" && req.Status != "UNHEALTHY" {
		AWSError(w, "InvalidInput", "Status must be HEALTHY or UNHEALTHY", http.StatusBadRequest)
		return
	}
	svc, ok := cmServices.Get(req.ServiceId)
	if !ok {
		AWSErrorf(w, "ServiceNotFound", http.StatusBadRequest,
			"Service '%s' not found", req.ServiceId)
		return
	}
	key := cmInstanceKey(req.ServiceId, req.InstanceId)
	if _, ok := cmInstances.Get(key); !ok {
		AWSErrorf(w, "InstanceNotFound", http.StatusBadRequest,
			"Instance '%s' not found", req.InstanceId)
		return
	}
	// Only a service configured with HealthCheckCustomConfig reports health
	// through this operation.
	if svc.HealthCheckCustomConfig == nil {
		AWSErrorf(w, "CustomHealthNotFound", http.StatusBadRequest,
			"Service '%s' does not have a custom health check", req.ServiceId)
		return
	}
	cmInstances.Update(key, func(inst *CMInstance) {
		inst.CustomHealthStatus = req.Status
	})
	// UpdateInstanceCustomHealthStatus has an empty (Unit) response body.
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCMGetInstancesHealthStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceId  string   `json:"ServiceId"`
		Instances  []string `json:"Instances"`
		MaxResults *int32   `json:"MaxResults"`
		NextToken  string   `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceId == "" {
		AWSError(w, "InvalidInput", "ServiceId is required", http.StatusBadRequest)
		return
	}
	if _, ok := cmServices.Get(req.ServiceId); !ok {
		AWSErrorf(w, "ServiceNotFound", http.StatusBadRequest,
			"Service '%s' not found", req.ServiceId)
		return
	}
	want := map[string]bool{}
	for _, id := range req.Instances {
		want[id] = true
	}
	selected := make([]CMInstance, 0)
	for _, inst := range cmServiceInstances(req.ServiceId) {
		if len(want) > 0 && !want[inst.Id] {
			continue
		}
		selected = append(selected, inst)
	}
	// A named instance that isn't registered under the service is an error,
	// not a silently missing map entry.
	if len(want) > 0 && len(selected) != len(want) {
		found := map[string]bool{}
		for _, inst := range selected {
			found[inst.Id] = true
		}
		for _, id := range req.Instances {
			if !found[id] {
				AWSErrorf(w, "InstanceNotFound", http.StatusBadRequest,
					"Instance '%s' not found", id)
				return
			}
		}
	}

	page, next := awsPage(selected, req.NextToken, awsMaxResults(req.MaxResults), 100)
	status := map[string]string{}
	for _, inst := range page {
		status[inst.Id] = cmInstanceHealth(inst)
	}
	out := map[string]any{"Status": status}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleCMDiscoverInstancesRevision(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NamespaceName string `json:"NamespaceName"`
		ServiceName   string `json:"ServiceName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInput", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.NamespaceName == "" || req.ServiceName == "" {
		AWSError(w, "InvalidInput", "NamespaceName and ServiceName are required", http.StatusBadRequest)
		return
	}
	var targetNs *CMNamespace
	for _, ns := range cmNamespaces.List() {
		if ns.Name == req.NamespaceName {
			nsCopy := ns
			targetNs = &nsCopy
			break
		}
	}
	if targetNs == nil {
		AWSErrorf(w, "NamespaceNotFound", http.StatusBadRequest,
			"Namespace '%s' not found", req.NamespaceName)
		return
	}
	var targetSvc *CMService
	for _, svc := range cmServices.List() {
		if svc.Name == req.ServiceName && svc.NamespaceId == targetNs.Id {
			svcCopy := svc
			targetSvc = &svcCopy
			break
		}
	}
	if targetSvc == nil {
		AWSErrorf(w, "ServiceNotFound", http.StatusBadRequest,
			"Service '%s' not found in namespace '%s'", req.ServiceName, req.NamespaceName)
		return
	}
	revision, _ := cmServiceRevisions.Get(targetSvc.Id)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"InstancesRevision": revision,
	})
}

func handleCMListOperations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters []struct {
			Name      string   `json:"Name"`
			Values    []string `json:"Values"`
			Condition string   `json:"Condition"`
		} `json:"Filters"`
		MaxResults *int32 `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidInput", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}

	matches := func(op CMOperation) bool {
		for _, f := range req.Filters {
			var actual string
			switch f.Name {
			case "NAMESPACE_ID":
				actual = op.NamespaceId
			case "SERVICE_ID":
				actual = op.ServiceId
			case "STATUS":
				actual = op.Status
			case "TYPE":
				actual = op.Type
			case "UPDATE_DATE":
				// Date-range filtering is not modeled; treat as a match so a
				// client narrowing by date still sees the synchronous ops.
				continue
			default:
				continue
			}
			if !cmFilterMatch(f.Condition, f.Values, actual) {
				return false
			}
		}
		return true
	}

	selected := make([]CMOperation, 0)
	for _, op := range sortBy(cmOperations.List(), func(op CMOperation) string { return op.OperationId }) {
		if !matches(op) {
			continue
		}
		selected = append(selected, op)
	}
	page, next := awsPage(selected, req.NextToken, awsMaxResults(req.MaxResults), 100)
	operations := make([]map[string]any, 0, len(page))
	for _, op := range page {
		operations = append(operations, map[string]any{
			"Id":     op.OperationId,
			"Status": op.Status,
		})
	}
	out := map[string]any{"Operations": operations}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}
