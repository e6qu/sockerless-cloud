package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

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

// The caller reports instance health through UpdateInstanceCustomHealthStatus
// instead of a Route 53 health check.
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
	// CustomHealthStatus, when set, overrides the registered health.
	CustomHealthStatus string `json:"CustomHealthStatus,omitempty"`
	// PendingHealthStatus is a requested status still waiting out AWS Cloud Map's
	// 30 seconds since PendingHealthSince (Unix milliseconds).
	PendingHealthStatus string `json:"PendingHealthStatus,omitempty"`
	PendingHealthSince  int64  `json:"PendingHealthSince,omitempty"`
	// HealthCheckId names the Route 53 health check AWS Cloud Map created for the
	// instance.
	HealthCheckId    string `json:"HealthCheckId,omitempty"`
	CreatorRequestId string `json:"CreatorRequestId,omitempty"`
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

var (
	cmNamespaces        sim.Store[CMNamespace]
	cmNamespaceVPCs     sim.Store[string]
	cmServices          sim.Store[CMService]
	cmInstances         sim.Store[CMInstance]
	cmOperations        sim.Store[CMOperation]
	cmServiceAttributes sim.Store[map[string]string]
	// RegisterInstance and DeregisterInstance bump the revision
	// DiscoverInstancesRevision returns.
	cmServiceRevisions sim.Store[int64]
	// Keyed by resource ARN, as the tagging operations address resources.
	cmTags sim.Store[map[string]string]
)

// Exceeding the limit is a TooManyTagsException.
const cmMaxTagsPerResource = 50

func cmArn(resourceType, id string) string {
	return fmt.Sprintf("arn:aws:servicediscovery:%s:%s:%s/%s", awsRegion(), awsAccountID(), resourceType, id)
}

func cmInstanceKey(serviceId, instanceId string) string {
	return serviceId + ":" + instanceId
}

type cmTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// Operators, and the Amazon ECS backend's network-state recovery, filter on
// these tags.
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

// Namespaces and services are the taggable resource types.
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

func cmNamespaceServiceCount(namespaceId string) int {
	n := 0
	for _, svc := range cmServices.List() {
		if svc.NamespaceId == namespaceId {
			n++
		}
	}
	return n
}

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

// GetNamespace adds CreatorRequestId to the summary.
func cmNamespaceView(ns CMNamespace) map[string]any {
	out := cmNamespaceSummary(ns)
	if ns.CreatorRequestId != "" {
		out["CreatorRequestId"] = ns.CreatorRequestId
	}
	return out
}

// Custom health and the owning service are bookkeeping, not wire members;
// InstanceSummary has no CreatorRequestId.
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

func cmInstanceView(inst CMInstance) map[string]any {
	out := cmInstanceSummary(inst)
	if inst.CreatorRequestId != "" {
		out["CreatorRequestId"] = inst.CreatorRequestId
	}
	return out
}

// Sort by ID for stable pagination. Registrations key on service+instance, so
// one instance ID may back several services with different attributes.
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

// EQ (the default) and IN match any value; BEGINS_WITH a prefix. BETWEEN
// applies only to UPDATE_DATE, which the operation-filter path handles.
func cmFilterMatch(condition string, values []string, actual string) bool {
	switch condition {
	case "BEGINS_WITH":
		for _, v := range values {
			if strings.HasPrefix(actual, v) {
				return true
			}
		}
		return false
	default:
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

type cmSOAProperties struct {
	DnsProperties *struct {
		SOA *struct {
			TTL int64 `json:"TTL"`
		} `json:"SOA"`
	} `json:"DnsProperties"`
}

// Namespace names are unique per account and Region.
func cmNamespaceNameTaken(name string) bool {
	for _, ns := range cmNamespaces.List() {
		if ns.Name == name {
			return true
		}
	}
	return false
}

// The three Create*Namespace operations share the OperationId response.
func cmCreateNamespace(w http.ResponseWriter, ns CMNamespace, tags []cmTag) {
	cmNamespaces.Put(ns.Id, ns)
	cmPutResourceTags(ns.Arn, tags)

	operationId := sim.NewUUID()
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

	nsId := "ns-" + sim.NewUUID()[:16]
	cmNamespaceVPCs.Put(nsId, req.Vpc)
	ns := CMNamespace{
		Id:               nsId,
		Arn:              cmArn("namespace", nsId),
		Name:             req.Name,
		Type:             "DNS_PRIVATE",
		Description:      req.Description,
		CreatorRequestId: req.CreatorRequestId,
		CreateDate:       time.Now().Unix(),
	}
	ns.Properties = &CMNamespaceProperties{DnsProperties: cmCreateHostedZone(ns, req.Vpc, req.Properties)}
	cmCreateNamespace(w, ns, req.Tags)
}

// A public namespace is a private one without the VPC association.
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

	nsId := "ns-" + sim.NewUUID()[:16]
	ns := CMNamespace{
		Id:               nsId,
		Arn:              cmArn("namespace", nsId),
		Name:             req.Name,
		Type:             "DNS_PUBLIC",
		Description:      req.Description,
		CreatorRequestId: req.CreatorRequestId,
		CreateDate:       time.Now().Unix(),
	}
	ns.Properties = &CMNamespaceProperties{DnsProperties: cmCreateHostedZone(ns, "", req.Properties)}
	cmCreateNamespace(w, ns, req.Tags)
}

// An HTTP namespace has no DNS records; HttpName defaults to the namespace
// name.
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

	nsId := "ns-" + sim.NewUUID()[:16]
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

	cmDeleteHostedZone(ns)
	cmNamespaces.Delete(req.Id)
	cmNamespaceVPCs.Delete(req.Id)
	cmTags.Delete(ns.Arn)
	operationId := sim.NewUUID()
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

func cmUpdateNamespaceOp(w http.ResponseWriter, nsId string) {
	operationId := sim.NewUUID()
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

func handleCMUpdatePrivateDnsNamespace(w http.ResponseWriter, r *http.Request) {
	handleCMUpdateDnsNamespace(w, r)
}

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
	cmSyncNamespaceRecords(req.Id)
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

	svcId := "srv-" + sim.NewUUID()[:16]
	if req.HealthCheckCustomConfig != nil {
		// AWS Cloud Map always uses a failure threshold of one for custom health
		// checks.
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

// A service in an HTTP namespace or without DNS records is HTTP; one in a DNS
// namespace with DNS records is DNS, or DNS_HTTP when discoverable both ways.
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
			if s.DnsConfig == nil {
				s.DnsConfig = &CMDnsConfig{}
			}
			s.DnsConfig.DnsRecords = req.Service.DnsConfig.DnsRecords
		}
		if req.Service.HealthCheckConfig != nil {
			s.HealthCheckConfig = req.Service.HealthCheckConfig
		}
	})
	if updated, ok := cmServices.Get(req.Id); ok && req.Service.HealthCheckConfig != nil {
		for _, inst := range cmServiceInstances(req.Id) {
			cmDeleteInstanceHealthCheck(inst)
			id := cmCreateInstanceHealthCheck(updated, inst.Attributes)
			cmInstances.Update(cmInstanceKey(req.Id, inst.Id), func(i *CMInstance) { i.HealthCheckId = id })
		}
	}
	cmSyncNamespaceRecords(svc.NamespaceId)

	operationId := sim.NewUUID()
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
	if _, nsOk := cmNamespaces.Get(svc.NamespaceId); !nsOk {
		AWSErrorf(w, "NamespaceNotFound", http.StatusBadRequest,
			"Namespace '%s' not found", svc.NamespaceId)
		return
	}

	// Store the instance before realizing DNS so the realization sees it. One
	// instance ID may back several services, resolving under each name.
	instance := CMInstance{
		Id:               req.InstanceId,
		Attributes:       req.Attributes,
		CreatorRequestId: req.CreatorRequestId,
	}
	key := cmInstanceKey(req.ServiceId, req.InstanceId)
	previous, existed := cmInstances.Get(key)
	if existed {
		cmDeleteInstanceHealthCheck(previous)
	}
	instance.HealthCheckId = cmCreateInstanceHealthCheck(svc, req.Attributes)
	cmInstances.Put(key, instance)
	if !existed {
		cmServices.Update(req.ServiceId, func(svc *CMService) {
			svc.InstanceCount++
		})
	}
	cmBumpServiceRevision(req.ServiceId)
	cmSyncNamespaceRecords(svc.NamespaceId)
	cmWriteInstanceOperation(w, "REGISTER_INSTANCE", svc, req.InstanceId)
}

// A caller polls the operation with GetOperation.
func cmWriteInstanceOperation(w http.ResponseWriter, opType string, svc CMService, instanceId string) {
	operationId := sim.NewUUID()
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
	if inst, ok := cmInstances.Get(key); ok {
		cmDeleteInstanceHealthCheck(inst)
	}
	if !cmInstances.Delete(key) {
		AWSErrorf(w, "InstanceNotFound", http.StatusBadRequest,
			"Instance '%s' not found", req.InstanceId)
		return
	}

	cmServices.Update(req.ServiceId, func(svc *CMService) {
		if svc.InstanceCount > 0 {
			svc.InstanceCount--
		}
	})
	cmBumpServiceRevision(req.ServiceId)

	svc, svcOk := cmServices.Get(req.ServiceId)
	if svcOk {
		cmSyncNamespaceRecords(svc.NamespaceId)
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

	page, next, pageOK := awsKeyPage(w, cloudMapBadToken, cmServiceInstances(req.ServiceId), func(it CMInstance) string { return it.Id }, req.NextToken, awsMaxResults(req.MaxResults), 100)
	if !pageOK {
		return
	}
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

	page, next, pageOK := awsKeyPage(w, cloudMapBadToken, namespaces, func(it CMNamespace) string { return it.Id }, req.NextToken, awsMaxResults(req.MaxResults), 100)
	if !pageOK {
		return
	}
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

	page, next, pageOK := awsKeyPage(w, cloudMapBadToken, services, func(it CMService) string { return it.Id }, req.NextToken, awsMaxResults(req.MaxResults), 100)
	if !pageOK {
		return
	}
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

// ServiceSummary has no NamespaceId member.
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

	// Report effective health: the custom status, else AWS_INIT_HEALTH_STATUS,
	// else HEALTHY.
	type discovered struct {
		entry  map[string]any
		health string
	}
	var all []discovered
	for _, inst := range cmServiceInstances(targetSvc.Id) {
		// QueryParameters keep instances carrying every listed attribute value.
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
	// OptionalParameters narrow the result only when some instance matches them.
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
	default:
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

// Health-status updates do not bump the revision.
func cmBumpServiceRevision(serviceId string) {
	cur, _ := cmServiceRevisions.Get(serviceId)
	cmServiceRevisions.Put(serviceId, cur+1)
}

// AWS Cloud Map waits this long before applying a custom health status.
const cmCustomHealthDelay = 30 * time.Second

// A request matching the status in force cancels a waiting change; one
// repeating the waiting change keeps the first's time.
func cmRequestCustomHealth(inst *CMInstance, status string, now time.Time) {
	current := cmInstanceHealth(*inst)
	if inst.PendingHealthStatus != "" && current == inst.PendingHealthStatus {
		inst.CustomHealthStatus = current
		inst.PendingHealthStatus, inst.PendingHealthSince = "", 0
	}
	switch {
	case status == current:
		inst.PendingHealthStatus, inst.PendingHealthSince = "", 0
	case status != inst.PendingHealthStatus:
		inst.PendingHealthStatus, inst.PendingHealthSince = status, now.UnixMilli()
	}
}

func cmInstanceHealth(inst CMInstance) string {
	if inst.HealthCheckId != "" {
		if r53HealthCheckHealthy(inst.HealthCheckId) {
			return "HEALTHY"
		}
		return "UNHEALTHY"
	}
	if inst.PendingHealthStatus != "" && time.Since(time.UnixMilli(inst.PendingHealthSince)) >= cmCustomHealthDelay {
		return inst.PendingHealthStatus
	}
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
	// Only a HealthCheckCustomConfig service reports health this way.
	if svc.HealthCheckCustomConfig == nil {
		AWSErrorf(w, "CustomHealthNotFound", http.StatusBadRequest,
			"Service '%s' does not have a custom health check", req.ServiceId)
		return
	}
	cmInstances.Update(key, func(inst *CMInstance) {
		cmRequestCustomHealth(inst, req.Status, time.Now())
	})
	// The response is Unit.
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
	// A named instance not registered under the service is an error.
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

	page, next, pageOK := awsKeyPage(w, cloudMapBadToken, selected, func(it CMInstance) string { return it.Id }, req.NextToken, awsMaxResults(req.MaxResults), 100)
	if !pageOK {
		return
	}
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
				// The simulator does not model UPDATE_DATE filtering; it matches every
				// operation.
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
	page, next, pageOK := awsKeyPage(w, cloudMapBadToken, selected, func(it CMOperation) string { return it.OperationId }, req.NextToken, awsMaxResults(req.MaxResults), 100)
	if !pageOK {
		return
	}
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
