package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// A gateway route is one service's connection to one gateway. A deployment of
// a service with a gateway connector decides which gateways the service
// connects to and records the routes it adds, keeps, changes and removes;
// the work behind each route then runs after the request, as Amazon ECS does
// it during the deployment, through the Elastic Load Balancing, Amazon EC2,
// AWS Certificate Manager and AWS WAF APIs as the gateway's infrastructure
// role.

const (
	ecsGatewayRouteActive           = "ACTIVE"
	ecsGatewayRouteCreateInProgress = "CREATE_IN_PROGRESS"
	ecsGatewayRouteCreateFailed     = "CREATE_FAILED"
	ecsGatewayRouteUpdateInProgress = "UPDATE_IN_PROGRESS"
	ecsGatewayRouteUpdateFailed     = "UPDATE_FAILED"
	ecsGatewayRouteDeleteInProgress = "DELETE_IN_PROGRESS"
	ecsGatewayRouteDeleteFailed     = "DELETE_FAILED"
)

var ecsGatewayRouteStatuses = []string{
	ecsGatewayRouteActive, ecsGatewayRouteCreateInProgress, ecsGatewayRouteCreateFailed,
	ecsGatewayRouteUpdateInProgress, ecsGatewayRouteUpdateFailed,
	ecsGatewayRouteDeleteInProgress, ecsGatewayRouteDeleteFailed,
}

type ecsGatewayConnectorHealthCheckMatcher struct {
	HttpCode string `json:"httpCode,omitempty"`
	GrpcCode string `json:"grpcCode,omitempty"`
}

type ecsGatewayConnectorHealthCheck struct {
	HealthCheckProtocol        string                                 `json:"healthCheckProtocol,omitempty"`
	HealthCheckPort            *int32                                 `json:"healthCheckPort,omitempty"`
	HealthCheckPath            string                                 `json:"healthCheckPath,omitempty"`
	HealthCheckTimeoutSeconds  *int32                                 `json:"healthCheckTimeoutSeconds,omitempty"`
	HealthCheckIntervalSeconds *int32                                 `json:"healthCheckIntervalSeconds,omitempty"`
	HealthyThresholdCount      *int32                                 `json:"healthyThresholdCount,omitempty"`
	UnhealthyThresholdCount    *int32                                 `json:"unhealthyThresholdCount,omitempty"`
	Matcher                    *ecsGatewayConnectorHealthCheckMatcher `json:"matcher,omitempty"`
}

type ecsGatewayConnectorFilters struct {
	AccessTypes []string `json:"accessTypes,omitempty"`
}

type ecsGatewayConnector struct {
	PortName    string                          `json:"portName"`
	HealthCheck *ecsGatewayConnectorHealthCheck `json:"healthCheck,omitempty"`
	Filters     *ecsGatewayConnectorFilters     `json:"filters,omitempty"`
}

// ecsGatewayTargetGroupSpec is what a route's target group is created with;
// a deployment that changes it replaces the target group.
type ecsGatewayTargetGroupSpec struct {
	Port            int                             `json:"port"`
	ProtocolVersion string                          `json:"protocolVersion,omitempty"`
	HealthCheck     *ecsGatewayConnectorHealthCheck `json:"healthCheck,omitempty"`
}

type ECSGatewayRoute struct {
	GatewayRouteArn string `json:"gatewayRouteArn"`
	GatewayArn      string `json:"gatewayArn"`
	ClusterArn      string `json:"clusterArn"`
	ServiceArn      string `json:"serviceArn"`
	ServiceKey      string `json:"serviceKey"`
	PortName        string `json:"portName"`
	ContainerName   string `json:"containerName"`
	Status          string `json:"status"`
	Hostname        string `json:"hostname,omitempty"`
	AccessType      string `json:"accessType"`
	// ServiceRevisionArns are the service revisions whose deployments carried
	// this connection.
	ServiceRevisionArns []string `json:"serviceRevisionArns,omitempty"`

	// Desired is what the latest deployment asked for; Applied is what the
	// target group was created with.
	Desired           ecsGatewayTargetGroupSpec    `json:"desired"`
	Applied           *ecsGatewayTargetGroupSpec   `json:"applied,omitempty"`
	DesiredTestHeader *ecsGatewayTestTrafficHeader `json:"desiredTestHeader,omitempty"`
	AppliedTestHeader *ecsGatewayTestTrafficHeader `json:"appliedTestHeader,omitempty"`

	Slot                       int      `json:"slot"`
	LoadBalancerArn            string   `json:"loadBalancerArn,omitempty"`
	ListenerArn                string   `json:"listenerArn,omitempty"`
	CertificateArn             string   `json:"certificateArn,omitempty"`
	CertificateOnListener      bool     `json:"certificateOnListener,omitempty"`
	TargetGroupArn             string   `json:"targetGroupArn,omitempty"`
	ListenerRuleArn            string   `json:"listenerRuleArn,omitempty"`
	TestListenerRuleArns       []string `json:"testListenerRuleArns,omitempty"`
	ServiceSecurityGroupID     string   `json:"serviceSecurityGroupId,omitempty"`
	ServiceSecurityGroupRuleID string   `json:"serviceSecurityGroupRuleId,omitempty"`
	ServiceSecurityGroupPort   int      `json:"serviceSecurityGroupPort,omitempty"`
}

var (
	ecsGatewayRoutes            sim.Store[ECSGatewayRoute]
	ecsServiceGatewayConnectors sim.Store[[]ecsGatewayConnector]
)

func ecsParseGatewayConnectors(raw json.RawMessage) ([]ecsGatewayConnector, string) {
	var connectors []ecsGatewayConnector
	if err := json.Unmarshal(raw, &connectors); err != nil {
		return nil, "gatewayConnectors must be a list of gateway connectors: " + err.Error()
	}
	if len(connectors) > 1 {
		return nil, "A service supports no more than one gateway connector."
	}
	for _, connector := range connectors {
		if connector.PortName == "" {
			return nil, "gatewayConnectors.portName is required."
		}
		if connector.Filters != nil {
			if len(connector.Filters.AccessTypes) > 2 {
				return nil, "filters.accessTypes accepts at most 2 values."
			}
			seen := map[string]bool{}
			for _, accessType := range connector.Filters.AccessTypes {
				if accessType != "PUBLIC" && accessType != "PRIVATE" {
					return nil, "filters.accessTypes must be PUBLIC or PRIVATE."
				}
				if seen[accessType] {
					return nil, "filters.accessTypes contains " + accessType + " more than once."
				}
				seen[accessType] = true
			}
		}
		if check := connector.HealthCheck; check != nil && check.HealthCheckProtocol != "" &&
			check.HealthCheckProtocol != "HTTP" && check.HealthCheckProtocol != "HTTPS" {
			return nil, "healthCheck.healthCheckProtocol must be HTTP or HTTPS."
		}
	}
	return connectors, ""
}

func ecsServiceCurrentGatewayConnectors(serviceArn string) []ecsGatewayConnector {
	connectors, _ := ecsServiceGatewayConnectors.Get(serviceArn)
	return connectors
}

// ecsGatewayServicePort resolves a connector's port name to the container and
// port mapping that carries it.
func ecsGatewayServicePort(td ECSTaskDefinition, portName string) (string, ECSPortMapping, bool) {
	for _, container := range td.ContainerDefinitions {
		for _, mapping := range container.PortMappings {
			if mapping.Name == portName {
				return container.Name, mapping, true
			}
		}
	}
	return "", ECSPortMapping{}, false
}

type ecsServiceAwsvpc struct {
	Subnets        []string `json:"subnets"`
	SecurityGroups []string `json:"securityGroups"`
}

func ecsServiceAwsvpcConfiguration(service ECSService) ecsServiceAwsvpc {
	var config struct {
		AwsvpcConfiguration ecsServiceAwsvpc `json:"awsvpcConfiguration"`
	}
	if len(service.NetworkConfiguration) > 0 {
		_ = json.Unmarshal(service.NetworkConfiguration, &config)
	}
	return config.AwsvpcConfiguration
}

// ecsSubnetsPlacement returns the VPC a set of subnets is in and the
// Availability Zones they cover; vpc is "" when they span VPCs or one does not
// exist.
func ecsSubnetsPlacement(subnets []string) (vpc string, zones map[string]bool) {
	zones = map[string]bool{}
	for _, id := range subnets {
		subnet, ok := ec2Subnets.Get(id)
		if !ok {
			return "", zones
		}
		if vpc != "" && subnet.VpcId != vpc {
			return "", zones
		}
		vpc = subnet.VpcId
		zones[subnet.AvailabilityZone] = true
	}
	return vpc, zones
}

// ecsGatewayEligible returns the gateways a service's connector connects it
// to: every gateway with an ACTIVE association with the cluster that matches
// the connector's access types, is in the service's VPC and has subnets in
// every Availability Zone the service's subnets are in.
func ecsGatewayEligible(cluster ECSCluster, service ECSService, connector ecsGatewayConnector) ([]ECSGateway, string) {
	var associated []ECSGateway
	for _, association := range ecsGatewayAssociations.List() {
		if association.ClusterArn != cluster.ClusterArn || association.Status != ecsGatewayAssociationActive {
			continue
		}
		if gateway, ok := ecsGateways.Get(association.GatewayArn); ok {
			associated = append(associated, gateway)
		}
	}
	if len(associated) == 0 {
		return nil, "gatewayConnectors were specified but no gateways are associated with cluster " + cluster.ClusterName
	}
	serviceVPC, serviceZones := ecsSubnetsPlacement(ecsServiceAwsvpcConfiguration(service).Subnets)
	var inVPC []ECSGateway
	for _, gateway := range associated {
		if vpc, _ := ecsSubnetsPlacement(gateway.Configuration.NetworkConfiguration.Subnets); vpc != "" && vpc == serviceVPC {
			inVPC = append(inVPC, gateway)
		}
	}
	if len(inVPC) == 0 {
		return nil, "Cluster " + cluster.ClusterName + " has associated gateways but none are in the service's VPC"
	}
	var eligible []ECSGateway
	for _, gateway := range inVPC {
		if connector.Filters != nil && len(connector.Filters.AccessTypes) > 0 &&
			!slices.Contains(connector.Filters.AccessTypes, gateway.AccessType) {
			continue
		}
		_, gatewayZones := ecsSubnetsPlacement(gateway.Configuration.NetworkConfiguration.Subnets)
		covered := true
		for zone := range serviceZones {
			if !gatewayZones[zone] {
				covered = false
			}
		}
		if covered {
			eligible = append(eligible, gateway)
		}
	}
	if len(eligible) == 0 {
		return nil, "none of the gateways associated with cluster " + cluster.ClusterName + " match"
	}
	slices.SortFunc(eligible, func(a, b ECSGateway) int { return strings.Compare(a.GatewayArn, b.GatewayArn) })
	return eligible, ""
}

// ecsGatewayDeployment is what a service deployment does to the service's
// gateway connections, decided before the deployment is recorded so a request
// that cannot connect is refused whole.
type ecsGatewayDeployment struct {
	connectors    []ecsGatewayConnector
	gateways      []ECSGateway
	containerName string
	spec          ecsGatewayTargetGroupSpec
}

func ecsDeploymentStrategy(service ECSService) string {
	var config struct {
		Strategy string `json:"strategy"`
	}
	if len(service.DeploymentConfiguration) > 0 {
		_ = json.Unmarshal(service.DeploymentConfiguration, &config)
	}
	if config.Strategy == "" {
		return "ROLLING"
	}
	return config.Strategy
}

// ecsPlanGatewayDeployment validates a deployment's gateway connectors against
// the service it deploys and the gateways associated with its cluster.
func ecsPlanGatewayDeployment(cluster ECSCluster, service ECSService, connectors []ecsGatewayConnector) (*ecsGatewayDeployment, string) {
	plan := &ecsGatewayDeployment{connectors: connectors}
	if len(connectors) == 0 {
		return plan, ""
	}
	connector := connectors[0]
	td, ok := ecsServiceTaskDefinition(service.TaskDefinition)
	if !ok {
		return nil, "Unable to describe task definition: " + service.TaskDefinition
	}
	if ecsEffectiveNetworkMode(td) != ecsNetworkModeAwsvpc {
		return nil, "A service must use the awsvpc network mode to connect to a gateway."
	}
	container, mapping, ok := ecsGatewayServicePort(td, connector.PortName)
	if !ok {
		return nil, "InvalidConfiguration: portName not found in task definition portMappings"
	}
	gateways, problem := ecsGatewayEligible(cluster, service, connector)
	if problem != "" {
		return nil, problem
	}
	// "A health check change replaces the target group, so you cannot make one
	// on a service that uses the ROLLING deployment strategy."
	if previous := ecsServiceCurrentGatewayConnectors(service.ServiceArn); len(previous) > 0 &&
		!reflect.DeepEqual(previous[0].HealthCheck, connector.HealthCheck) &&
		ecsDeploymentStrategy(service) == "ROLLING" &&
		len(ecsGatewayRoutes.Filter(func(route ECSGatewayRoute) bool { return route.ServiceArn == service.ServiceArn })) > 0 {
		return nil, "Target group properties cannot be changed with ROLLING deployment strategy."
	}
	plan.gateways = gateways
	plan.containerName = container
	plan.spec = ecsGatewayTargetGroupSpec{
		Port:            mapping.ContainerPort,
		ProtocolVersion: ecsGatewayProtocolVersion(mapping.AppProtocol),
		HealthCheck:     connector.HealthCheck,
	}
	return plan, ""
}

func ecsGatewayProtocolVersion(appProtocol string) string {
	switch appProtocol {
	case "http2":
		return "HTTP2"
	case "grpc":
		return "GRPC"
	default:
		return "HTTP1"
	}
}

// ecsApplyGatewayDeployment records the deployment's connectors and changes
// the service's routes to match: a route to each gateway the plan names that
// the service has none to, the routes it keeps brought up to date, and the
// rest removed.
func ecsApplyGatewayDeployment(service ECSService, plan *ecsGatewayDeployment, revisionArn string) {
	ecsGatewayMu.Lock()
	defer ecsGatewayMu.Unlock()
	if len(plan.connectors) == 0 {
		ecsServiceGatewayConnectors.Delete(service.ServiceArn)
	} else {
		ecsServiceGatewayConnectors.Put(service.ServiceArn, plan.connectors)
	}
	if revisionArn != "" {
		ecsServiceRevisions.Update(revisionArn, func(revision *ECSServiceRevisionRec) {
			revision.GatewayConnectors = plan.connectors
		})
	}
	wanted := map[string]ECSGateway{}
	for _, gateway := range plan.gateways {
		wanted[gateway.GatewayArn] = gateway
	}
	var work []string
	for _, route := range ecsGatewayRoutes.Filter(func(route ECSGatewayRoute) bool { return route.ServiceArn == service.ServiceArn }) {
		gateway, keep := wanted[route.GatewayArn]
		if !keep || route.Status == ecsGatewayRouteDeleteInProgress {
			if route.Status != ecsGatewayRouteDeleteInProgress {
				route.Status = ecsGatewayRouteDeleteInProgress
				ecsGatewayRoutes.Put(route.GatewayRouteArn, route)
			}
			work = append(work, route.GatewayRouteArn)
			continue
		}
		delete(wanted, route.GatewayArn)
		route.PortName = plan.connectors[0].PortName
		route.ContainerName = plan.containerName
		route.Desired = plan.spec
		route.DesiredTestHeader = ecsGatewayTestHeader(gateway)
		route.ServiceRevisionArns = append(route.ServiceRevisionArns, revisionArn)
		switch route.Status {
		case ecsGatewayRouteCreateFailed, ecsGatewayRouteCreateInProgress:
			route.Status = ecsGatewayRouteCreateInProgress
		default:
			route.Status = ecsGatewayRouteUpdateInProgress
		}
		ecsGatewayRoutes.Put(route.GatewayRouteArn, route)
		work = append(work, route.GatewayRouteArn)
	}
	for _, gateway := range plan.gateways {
		if _, missing := wanted[gateway.GatewayArn]; !missing {
			continue
		}
		route := ECSGatewayRoute{
			GatewayRouteArn:     ecsArn("gateway-route", gateway.Name+"/"+gateway.ID+"/"+sim.RandomHex(16)),
			GatewayArn:          gateway.GatewayArn,
			ClusterArn:          service.ClusterArn,
			ServiceArn:          service.ServiceArn,
			ServiceKey:          ecsServiceStoreKey(service),
			PortName:            plan.connectors[0].PortName,
			ContainerName:       plan.containerName,
			Status:              ecsGatewayRouteCreateInProgress,
			AccessType:          gateway.AccessType,
			ServiceRevisionArns: []string{revisionArn},
			Desired:             plan.spec,
			DesiredTestHeader:   ecsGatewayTestHeader(gateway),
			Slot:                -1,
		}
		ecsGatewayRoutes.Put(route.GatewayRouteArn, route)
		work = append(work, route.GatewayRouteArn)
	}
	for _, routeArn := range work {
		ecsGatewayScheduleRoute(routeArn)
	}
}

// ecsServiceUsesGateways reports whether a service has a gateway connector or
// a route left from one.
func ecsServiceUsesGateways(serviceArn string) bool {
	return len(ecsServiceCurrentGatewayConnectors(serviceArn)) > 0 ||
		len(ecsGatewayRoutes.Filter(func(route ECSGatewayRoute) bool { return route.ServiceArn == serviceArn })) > 0
}

// ecsDisconnectServiceFromGateways removes every route of a deleted service.
func ecsDisconnectServiceFromGateways(service ECSService) {
	if ecsServiceUsesGateways(service.ServiceArn) {
		ecsApplyGatewayDeployment(service, &ecsGatewayDeployment{}, "")
	}
}

func ecsGatewayTestHeader(gateway ECSGateway) *ecsGatewayTestTrafficHeader {
	if rules := gateway.Configuration.TestTrafficRules; rules != nil && rules.Header != nil {
		header := *rules.Header
		header.Values = slices.Clone(rules.Header.Values)
		return &header
	}
	return nil
}

// ecsGatewayCompleteDisassociation removes a DISASSOCIATING association once
// the cluster has no route through the gateway left.
func ecsGatewayCompleteDisassociation(gatewayArn, clusterArn string) {
	key := ecsGatewayAssociationKey(gatewayArn, clusterArn)
	association, ok := ecsGatewayAssociations.Get(key)
	if !ok || association.Status != ecsGatewayAssociationDisassociating {
		return
	}
	for _, route := range ecsGatewayRoutes.List() {
		if route.GatewayArn == gatewayArn && route.ClusterArn == clusterArn {
			return
		}
	}
	ecsGatewayAssociations.Delete(key)
}

// ecsServiceGatewayBindings are the target groups the service's routes
// register its tasks in, beside the ones its loadBalancers name.
func ecsServiceGatewayBindings(service ECSService) []ecsServiceLoadBalancer {
	var bindings []ecsServiceLoadBalancer
	for _, route := range ecsGatewayRoutes.Filter(func(route ECSGatewayRoute) bool { return route.ServiceArn == service.ServiceArn }) {
		if route.TargetGroupArn == "" || route.Status == ecsGatewayRouteDeleteInProgress || route.Applied == nil {
			continue
		}
		bindings = append(bindings, ecsServiceLoadBalancer{
			TargetGroupArn: route.TargetGroupArn,
			ContainerName:  route.ContainerName,
			ContainerPort:  route.Applied.Port,
		})
	}
	return bindings
}

// ecsServiceTargetBindings is every target group the service's tasks belong
// in.
func ecsServiceTargetBindings(service ECSService) ([]ecsServiceLoadBalancer, error) {
	bindings, err := ecsServiceLoadBalancers(service.LoadBalancers)
	if err != nil {
		return nil, err
	}
	return append(bindings, ecsServiceGatewayBindings(service)...), nil
}

func ecsGatewayServiceURL(route ECSGatewayRoute) string {
	if route.Hostname == "" {
		return ""
	}
	return "https://" + route.Hostname
}

func ecsGatewayRouteJSON(route ECSGatewayRoute) map[string]any {
	out := map[string]any{
		"gatewayRouteArn":          route.GatewayRouteArn,
		"serviceArn":               route.ServiceArn,
		"serviceContainerPortName": route.PortName,
		"status":                   route.Status,
	}
	if url := ecsGatewayServiceURL(route); url != "" {
		out["serviceUrl"] = url
	}
	resources := map[string]any{}
	if route.CertificateArn != "" {
		resources["acmCertificateArn"] = route.CertificateArn
	}
	if route.LoadBalancerArn != "" {
		resources["loadBalancerArn"] = route.LoadBalancerArn
	}
	if route.ListenerArn != "" {
		resources["listenerArn"] = route.ListenerArn
	}
	if route.ListenerRuleArn != "" {
		resources["listenerRuleArn"] = route.ListenerRuleArn
	}
	if len(route.TestListenerRuleArns) > 0 {
		resources["testListenerRuleArns"] = route.TestListenerRuleArns
	}
	if groups := ecsGatewayRouteLoadBalancerSecurityGroups(route); len(groups) > 0 {
		resources["loadBalancerSecurityGroups"] = groups
	}
	if route.ServiceSecurityGroupID != "" {
		resources["serviceSecurityGroupArn"] = ecsGatewaySecurityGroupArn(route.ServiceSecurityGroupID)
	}
	if len(resources) > 0 {
		out["resources"] = map[string]any{"hostSharedAlb": []any{resources}}
	}
	return out
}

func ecsGatewaySecurityGroupArn(groupID string) string {
	return fmt.Sprintf("arn:aws:ec2:%s:%s:security-group/%s", awsRegion(), awsAccountID(), groupID)
}

func ecsGatewaySecurityGroupRuleArn(ruleID string) string {
	return fmt.Sprintf("arn:aws:ec2:%s:%s:security-group-rule/%s", awsRegion(), awsAccountID(), ruleID)
}

func ecsGatewayRouteLoadBalancer(route ECSGatewayRoute) (ECSGatewayLoadBalancer, bool) {
	gateway, ok := ecsGateways.Get(route.GatewayArn)
	if !ok {
		return ECSGatewayLoadBalancer{}, false
	}
	for _, lb := range gateway.LoadBalancers {
		if lb.LoadBalancerArn == route.LoadBalancerArn {
			return lb, true
		}
	}
	return ECSGatewayLoadBalancer{}, false
}

type ecsGatewayRouteLoadBalancerSecurityGroup struct {
	Arn      string   `json:"arn"`
	RuleArns []string `json:"ruleArns"`
}

func ecsGatewayRouteLoadBalancerSecurityGroups(route ECSGatewayRoute) []ecsGatewayRouteLoadBalancerSecurityGroup {
	lb, ok := ecsGatewayRouteLoadBalancer(route)
	if !ok {
		return nil
	}
	var groups []ecsGatewayRouteLoadBalancerSecurityGroup
	if lb.SecurityGroupID != "" {
		rules := []string{}
		for _, id := range lb.IngressRuleIDs {
			rules = append(rules, ecsGatewaySecurityGroupRuleArn(id))
		}
		groups = append(groups, ecsGatewayRouteLoadBalancerSecurityGroup{Arn: ecsGatewaySecurityGroupArn(lb.SecurityGroupID), RuleArns: rules})
	}
	if gateway, ok := ecsGateways.Get(route.GatewayArn); ok {
		if inbound := gateway.Configuration.NetworkConfiguration.InboundSecurityGroup; inbound != "" {
			groups = append(groups, ecsGatewayRouteLoadBalancerSecurityGroup{Arn: ecsGatewaySecurityGroupArn(inbound), RuleArns: []string{}})
		}
	}
	return groups
}

// ecsGatewayConnectionsJSON is a service revision's gatewayConnections: the
// routes its deployment carried that still exist.
func ecsGatewayConnectionsJSON(revisionArn string) []map[string]any {
	routes := ecsGatewayRoutes.Filter(func(route ECSGatewayRoute) bool {
		return slices.Contains(route.ServiceRevisionArns, revisionArn)
	})
	slices.SortFunc(routes, func(a, b ECSGatewayRoute) int { return strings.Compare(a.GatewayRouteArn, b.GatewayRouteArn) })
	var connections []map[string]any
	for _, route := range routes {
		connection := map[string]any{
			"gatewayArn":      route.GatewayArn,
			"gatewayRouteArn": route.GatewayRouteArn,
			"portName":        route.PortName,
			"accessType":      route.AccessType,
			"status":          route.Status,
		}
		if url := ecsGatewayServiceURL(route); url != "" {
			connection["serviceUrl"] = url
		}
		resources := map[string]any{}
		if route.CertificateArn != "" {
			resources["acmCertificateArn"] = route.CertificateArn
		}
		if route.LoadBalancerArn != "" {
			resources["loadBalancerArn"] = route.LoadBalancerArn
		}
		var groupArns []string
		for _, group := range ecsGatewayRouteLoadBalancerSecurityGroups(route) {
			groupArns = append(groupArns, group.Arn)
		}
		if len(groupArns) > 0 {
			resources["loadBalancerSecurityGroupArns"] = groupArns
		}
		if route.ListenerArn != "" {
			resources["listenerArn"] = route.ListenerArn
		}
		if route.ListenerRuleArn != "" {
			resources["listenerRuleArn"] = route.ListenerRuleArn
		}
		if len(route.TestListenerRuleArns) > 0 {
			resources["testListenerRuleArn"] = route.TestListenerRuleArns[0]
		}
		if route.TargetGroupArn != "" {
			resources["targetGroupArn"] = route.TargetGroupArn
		}
		if len(resources) > 0 {
			connection["resources"] = map[string]any{"hostSharedAlb": []any{resources}}
		}
		connections = append(connections, connection)
	}
	return connections
}

func ecsGatewayScheduleRoute(routeArn string) {
	bg.Go(func() { ecsGatewayRunRoute(routeArn) })
}

func ecsGatewayScheduleUpdate(gatewayArn string) {
	bg.Go(func() { ecsGatewayRunUpdate(gatewayArn) })
}

// recoverECSGatewayWork resumes the route and gateway work a previous process
// left in progress.
func recoverECSGatewayWork() {
	for _, route := range ecsGatewayRoutes.List() {
		switch route.Status {
		case ecsGatewayRouteCreateInProgress, ecsGatewayRouteUpdateInProgress, ecsGatewayRouteDeleteInProgress:
			ecsGatewayScheduleRoute(route.GatewayRouteArn)
		}
	}
	for _, gateway := range ecsGateways.List() {
		if gateway.Status == ecsGatewayStatusUpdateInProgress {
			ecsGatewayScheduleUpdate(gateway.GatewayArn)
		}
	}
}

// ecsGatewayCallFailure is one call to another service that failed, named as
// the gateway's failures and route events name it.
type ecsGatewayCallFailure struct {
	Operation string
	Code      string
	Message   string
}

func (f *ecsGatewayCallFailure) Error() string {
	return f.Operation + ": " + f.Code + ": " + f.Message
}

// ecsGatewayClient makes the calls a gateway's infrastructure role makes.
type ecsGatewayClient struct {
	actor awsActor
}

func ecsGatewayClientFor(gateway ECSGateway) ecsGatewayClient {
	return ecsGatewayClient{actor: awsActor{
		Service: "ecs.amazonaws.com",
		Source:  gateway.GatewayArn,
		Role:    gateway.InfrastructureRoleArn,
	}}
}

func (c ecsGatewayClient) query(signing, prefix, action, version string, form url.Values) ([]byte, *ecsGatewayCallFailure) {
	status, body, err := awsCallQuery(context.Background(), c.actor, signing, action, version, form)
	operation := prefix + ":" + action
	if err != nil {
		code, message := "InternalFailure", err.Error()
		if callErr, ok := err.(*awsCallError); ok {
			code, message = callErr.Code, callErr.Message
		}
		return nil, &ecsGatewayCallFailure{Operation: operation, Code: code, Message: message}
	}
	if status >= 400 {
		code, message := awsXMLError(body)
		return nil, &ecsGatewayCallFailure{Operation: operation, Code: code, Message: message}
	}
	return body, nil
}

func (c ecsGatewayClient) elb(action string, form url.Values) ([]byte, *ecsGatewayCallFailure) {
	return c.query("elasticloadbalancing", "elasticloadbalancing", action, elbv2APIVersion, form)
}

func (c ecsGatewayClient) ec2(action string, form url.Values) ([]byte, *ecsGatewayCallFailure) {
	return c.query("ec2", "ec2", action, "2016-11-15", form)
}

func (c ecsGatewayClient) json(signing, prefix, target, operation string, input any) ([]byte, *ecsGatewayCallFailure) {
	status, body, err := awsCallJSON(context.Background(), c.actor, signing, target, "application/x-amz-json-1.1", input)
	op := prefix + ":" + operation
	if err != nil {
		code, message := "InternalFailure", err.Error()
		if callErr, ok := err.(*awsCallError); ok {
			code, message = callErr.Code, callErr.Message
		}
		return nil, &ecsGatewayCallFailure{Operation: op, Code: code, Message: message}
	}
	if status >= 400 {
		code, message := awsJSONError(body)
		return nil, &ecsGatewayCallFailure{Operation: op, Code: code, Message: message}
	}
	return body, nil
}

// awsXMLTexts returns the character data of every element named name.
func awsXMLTexts(body []byte, name string) []string {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	var out []string
	for {
		token, err := decoder.Token()
		if err != nil {
			return out
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == name {
			var text string
			if decoder.DecodeElement(&text, &start) == nil {
				out = append(out, text)
			}
		}
	}
}

func awsXMLText(body []byte, name string) string {
	if texts := awsXMLTexts(body, name); len(texts) > 0 {
		return texts[0]
	}
	return ""
}

// ecsGatewayManagedTags are the tags a gateway puts on what it creates: its
// own system tags, the route's when the resource is the route's, and the
// gateway's tags unless it propagates none.
func ecsGatewayManagedTags(gateway ECSGateway, routeArn string) [][2]string {
	tags := [][2]string{{"AmazonECSManaged", "true"}, {"aws:ecs:gateway-arn", gateway.GatewayArn}}
	if routeArn != "" {
		tags = append(tags, [2]string{"aws:ecs:gateway-route-arn", routeArn})
	}
	if gateway.PropagateTags == "GATEWAY" {
		for _, tag := range gateway.Tags {
			tags = append(tags, [2]string{tag.Key, tag.Value})
		}
	}
	return tags
}

func ecsGatewayELBTags(form url.Values, tags [][2]string) {
	for i, tag := range tags {
		form.Set(fmt.Sprintf("Tags.member.%d.Key", i+1), tag[0])
		form.Set(fmt.Sprintf("Tags.member.%d.Value", i+1), tag[1])
	}
}

func ecsGatewayRandomName() string {
	return "ecs-gw-" + sim.RandomHex(8)
}

func ecsGatewayHostname() string {
	return sim.RandomHex(8) + ".ecs." + awsRegion() + ".on.aws"
}

func ecsGatewaySaveRoute(route *ECSGatewayRoute) {
	ecsGatewayRoutes.Put(route.GatewayRouteArn, *route)
}

func ecsGatewaySaveLoadBalancer(gatewayArn string, lb ECSGatewayLoadBalancer) {
	ecsGateways.Update(gatewayArn, func(gateway *ECSGateway) {
		for i := range gateway.LoadBalancers {
			if gateway.LoadBalancers[i].LoadBalancerArn == lb.LoadBalancerArn {
				gateway.LoadBalancers[i] = lb
				return
			}
		}
		gateway.LoadBalancers = append(gateway.LoadBalancers, lb)
	})
}

func ecsGatewayRemoveLoadBalancer(gatewayArn, lbArn string) {
	ecsGateways.Update(gatewayArn, func(gateway *ECSGateway) {
		gateway.LoadBalancers = slices.DeleteFunc(gateway.LoadBalancers, func(lb ECSGatewayLoadBalancer) bool {
			return lb.LoadBalancerArn == lbArn
		})
	})
}

func ecsGatewayRunRoute(routeArn string) {
	ecsGatewayMu.Lock()
	route, ok := ecsGatewayRoutes.Get(routeArn)
	if !ok {
		ecsGatewayMu.Unlock()
		return
	}
	gateway, ok := ecsGateways.Get(route.GatewayArn)
	if !ok {
		ecsGatewayMu.Unlock()
		return
	}
	client := ecsGatewayClientFor(gateway)
	var failure *ecsGatewayCallFailure
	var event string
	switch route.Status {
	case ecsGatewayRouteCreateInProgress, ecsGatewayRouteUpdateInProgress:
		creating := route.Status == ecsGatewayRouteCreateInProgress
		failure = ecsGatewayProvisionRoute(client, gateway, &route)
		switch {
		case failure == nil:
			route.Status = ecsGatewayRouteActive
		case creating:
			route.Status = ecsGatewayRouteCreateFailed
		default:
			route.Status = ecsGatewayRouteUpdateFailed
		}
		ecsGatewaySaveRoute(&route)
		if failure != nil {
			event = fmt.Sprintf("(service %s) failed to provision gateway route %s: %s",
				ecsServiceNameFromRef(route.ServiceArn), route.GatewayRouteArn, failure.Error())
		}
	case ecsGatewayRouteDeleteInProgress:
		failure = ecsGatewayTeardownRoute(client, &route)
		if failure == nil {
			ecsGatewayRoutes.Delete(route.GatewayRouteArn)
			ecsGatewayCompleteDisassociation(route.GatewayArn, route.ClusterArn)
		} else {
			route.Status = ecsGatewayRouteDeleteFailed
			ecsGatewaySaveRoute(&route)
			event = fmt.Sprintf("(service %s) failed to remove gateway route %s: %s",
				ecsServiceNameFromRef(route.ServiceArn), route.GatewayRouteArn, failure.Error())
		}
	}
	ecsGatewayMu.Unlock()
	if event != "" {
		ecsAddServiceEvent(route.ServiceKey, event)
	}
	ecsRequestServiceReconcile(route.ServiceKey)
}

// ecsGatewayProvisionRoute brings a route's infrastructure to what its latest
// deployment asked for. Every step records what it created before the next
// one runs, so a step that fails leaves the route holding exactly what exists.
func ecsGatewayProvisionRoute(client ecsGatewayClient, gateway ECSGateway, route *ECSGatewayRoute) *ecsGatewayCallFailure {
	if route.LoadBalancerArn == "" {
		if failure := ecsGatewayPlaceRoute(client, gateway, route); failure != nil {
			return failure
		}
		gateway, _ = ecsGateways.Get(gateway.GatewayArn)
	}
	if route.Hostname == "" {
		route.Hostname = ecsGatewayHostname()
		ecsGatewaySaveRoute(route)
	}
	if route.CertificateArn == "" {
		arn, failure := ecsGatewayIssueCertificate(client, gateway, route)
		if failure != nil {
			return failure
		}
		route.CertificateArn = arn
		ecsGatewaySaveRoute(route)
	}
	lb, _ := ecsGatewayRouteLoadBalancer(*route)
	if lb.ListenerArn == "" {
		form := url.Values{
			"LoadBalancerArn":                      {lb.LoadBalancerArn},
			"Protocol":                             {"HTTPS"},
			"Port":                                 {"443"},
			"Certificates.member.1.CertificateArn": {route.CertificateArn},
			"DefaultActions.member.1.Type":         {"fixed-response"},
			"DefaultActions.member.1.FixedResponseConfig.StatusCode":  {"404"},
			"DefaultActions.member.1.FixedResponseConfig.ContentType": {"text/plain"},
		}
		ecsGatewayELBTags(form, ecsGatewayManagedTags(gateway, ""))
		body, failure := client.elb("CreateListener", form)
		if failure != nil {
			return failure
		}
		lb.ListenerArn = awsXMLText(body, "ListenerArn")
		ecsGatewaySaveLoadBalancer(gateway.GatewayArn, lb)
		route.CertificateOnListener = true
	}
	route.ListenerArn = lb.ListenerArn
	if !route.CertificateOnListener {
		if _, failure := client.elb("AddListenerCertificates", url.Values{
			"ListenerArn":                          {lb.ListenerArn},
			"Certificates.member.1.CertificateArn": {route.CertificateArn},
		}); failure != nil {
			return failure
		}
		route.CertificateOnListener = true
	}
	ecsGatewaySaveRoute(route)

	if route.Applied == nil || !reflect.DeepEqual(*route.Applied, route.Desired) {
		if failure := ecsGatewayReplaceTargetGroup(client, gateway, route, lb); failure != nil {
			return failure
		}
	}
	if route.ListenerRuleArn == "" {
		arn, failure := ecsGatewayCreateRule(client, gateway, route, route.Slot*10+5, nil)
		if failure != nil {
			return failure
		}
		route.ListenerRuleArn = arn
		ecsGatewaySaveRoute(route)
	}
	if !reflect.DeepEqual(route.AppliedTestHeader, route.DesiredTestHeader) || (route.DesiredTestHeader != nil && len(route.TestListenerRuleArns) == 0) {
		for len(route.TestListenerRuleArns) > 0 {
			if _, failure := client.elb("DeleteRule", url.Values{"RuleArn": {route.TestListenerRuleArns[0]}}); failure != nil {
				return failure
			}
			route.TestListenerRuleArns = route.TestListenerRuleArns[1:]
			ecsGatewaySaveRoute(route)
		}
		route.AppliedTestHeader = nil
		if route.DesiredTestHeader != nil {
			arn, failure := ecsGatewayCreateRule(client, gateway, route, route.Slot*10+1, route.DesiredTestHeader)
			if failure != nil {
				return failure
			}
			route.TestListenerRuleArns = []string{arn}
			header := *route.DesiredTestHeader
			route.AppliedTestHeader = &header
		}
		ecsGatewaySaveRoute(route)
	}
	return ecsGatewayAdmitLoadBalancer(client, route, lb)
}

// ecsGatewayPlaceRoute puts a route on a load balancer of its gateway with a
// free place, or on a new one while the gateway is under its maxAlbCount.
func ecsGatewayPlaceRoute(client ecsGatewayClient, gateway ECSGateway, route *ECSGatewayRoute) *ecsGatewayCallFailure {
	others := ecsGatewayRoutes.Filter(func(other ECSGatewayRoute) bool {
		return other.GatewayArn == gateway.GatewayArn && other.GatewayRouteArn != route.GatewayRouteArn
	})
	for _, lb := range gateway.LoadBalancers {
		used := map[int]bool{}
		for _, other := range others {
			if other.LoadBalancerArn == lb.LoadBalancerArn {
				used[other.Slot] = true
			}
		}
		if len(used) >= ecsGatewayRoutesPerLoadBalancer {
			continue
		}
		for slot := 0; slot < ecsGatewayRoutesPerLoadBalancer; slot++ {
			if !used[slot] {
				route.LoadBalancerArn = lb.LoadBalancerArn
				route.Slot = slot
				ecsGatewaySaveRoute(route)
				return nil
			}
		}
	}
	if int32(len(gateway.LoadBalancers)) >= *gateway.Configuration.MaxAlbCount {
		return &ecsGatewayCallFailure{
			Operation: "ecs:CreateGatewayRoute",
			Code:      "ResourceQuotaExceeded",
			Message: fmt.Sprintf("Every load balancer of gateway %s serves %d services and the gateway is at its maxAlbCount of %d.",
				gateway.Name, ecsGatewayRoutesPerLoadBalancer, *gateway.Configuration.MaxAlbCount),
		}
	}
	// A load balancer created before a later step failed is the route's, so
	// removing the route removes it.
	lb, failure := ecsGatewayCreateLoadBalancer(client, gateway)
	if lb.LoadBalancerArn != "" {
		route.LoadBalancerArn = lb.LoadBalancerArn
		route.Slot = 0
		ecsGatewaySaveRoute(route)
	}
	return failure
}

// ecsGatewayCreateLoadBalancer creates a shared Application Load Balancer with
// its security group, and applies the gateway's load balancer settings to it.
func ecsGatewayCreateLoadBalancer(client ecsGatewayClient, gateway ECSGateway) (ECSGatewayLoadBalancer, *ecsGatewayCallFailure) {
	config := gateway.Configuration
	network := config.NetworkConfiguration
	vpc, _ := ecsSubnetsPlacement(network.Subnets)
	tags := ecsGatewayManagedTags(gateway, "")
	groupForm := url.Values{
		"GroupName":                       {ecsGatewayRandomName()},
		"GroupDescription":                {"Managed by Amazon ECS for gateway " + gateway.Name},
		"VpcId":                           {vpc},
		"TagSpecification.1.ResourceType": {"security-group"},
	}
	for i, tag := range tags {
		groupForm.Set(fmt.Sprintf("TagSpecification.1.Tag.%d.Key", i+1), tag[0])
		groupForm.Set(fmt.Sprintf("TagSpecification.1.Tag.%d.Value", i+1), tag[1])
	}
	body, failure := client.ec2("CreateSecurityGroup", groupForm)
	if failure != nil {
		return ECSGatewayLoadBalancer{}, failure
	}
	lb := ECSGatewayLoadBalancer{SecurityGroupID: awsXMLText(body, "groupId")}
	if network.InboundSecurityGroup == "" {
		ruleIDs, failure := ecsGatewayAuthorizeHTTPS(client, lb.SecurityGroupID, network.IpAddressType)
		if failure != nil {
			return ECSGatewayLoadBalancer{}, ecsGatewayAbandonSecurityGroup(client, lb.SecurityGroupID, failure)
		}
		lb.IngressRuleIDs = ruleIDs
	}
	lbForm := url.Values{
		"Name":          {ecsGatewayRandomName()},
		"Type":          {"application"},
		"Scheme":        {config.Scheme},
		"IpAddressType": {network.IpAddressType},
	}
	for i, subnet := range network.Subnets {
		lbForm.Set(fmt.Sprintf("Subnets.member.%d", i+1), subnet)
	}
	groups := []string{lb.SecurityGroupID}
	if network.InboundSecurityGroup != "" {
		groups = append(groups, network.InboundSecurityGroup)
	}
	for i, group := range groups {
		lbForm.Set(fmt.Sprintf("SecurityGroups.member.%d", i+1), group)
	}
	ecsGatewayELBTags(lbForm, tags)
	body, failure = client.elb("CreateLoadBalancer", lbForm)
	if failure != nil {
		return ECSGatewayLoadBalancer{}, ecsGatewayAbandonSecurityGroup(client, lb.SecurityGroupID, failure)
	}
	lb.LoadBalancerArn = awsXMLText(body, "LoadBalancerArn")
	lb.DNSName = awsXMLText(body, "DNSName")
	ecsGatewaySaveLoadBalancer(gateway.GatewayArn, lb)
	if failure := ecsGatewayApplyLoadBalancerAttributes(client, gateway, lb); failure != nil {
		return lb, failure
	}
	if config.WebACLArn != "" {
		if _, failure := client.json("wafv2", "wafv2", "AWSWAF_20190729.AssociateWebACL", "AssociateWebACL",
			map[string]string{"WebACLArn": config.WebACLArn, "ResourceArn": lb.LoadBalancerArn}); failure != nil {
			return lb, failure
		}
		lb.WebACLArn = config.WebACLArn
		ecsGatewaySaveLoadBalancer(gateway.GatewayArn, lb)
	}
	return lb, nil
}

// ecsGatewayAuthorizeHTTPS opens port 443 to every address, which is what the
// gateway's own security group admits while it manages its inbound rules.
func ecsGatewayAuthorizeHTTPS(client ecsGatewayClient, groupID, ipAddressType string) ([]string, *ecsGatewayCallFailure) {
	form := url.Values{
		"GroupId":                           {groupID},
		"IpPermissions.1.IpProtocol":        {"tcp"},
		"IpPermissions.1.FromPort":          {"443"},
		"IpPermissions.1.ToPort":            {"443"},
		"IpPermissions.1.IpRanges.1.CidrIp": {"0.0.0.0/0"},
	}
	if strings.HasPrefix(ipAddressType, "dualstack") {
		form.Set("IpPermissions.1.Ipv6Ranges.1.CidrIpv6", "::/0")
	}
	body, failure := client.ec2("AuthorizeSecurityGroupIngress", form)
	if failure != nil {
		return nil, failure
	}
	return awsXMLTexts(body, "securityGroupRuleId"), nil
}

// ecsGatewayAbandonSecurityGroup deletes the security group a load balancer
// that failed to come up would have used, and says so when it cannot.
func ecsGatewayAbandonSecurityGroup(client ecsGatewayClient, groupID string, failure *ecsGatewayCallFailure) *ecsGatewayCallFailure {
	if cleanup := ecsGatewayDeleteSecurityGroup(client, groupID); cleanup != nil {
		failure.Message += "; the security group " + groupID + " it created was left behind: " + cleanup.Error()
	}
	return failure
}

func ecsGatewayDeleteSecurityGroup(client ecsGatewayClient, groupID string) *ecsGatewayCallFailure {
	_, failure := client.ec2("DeleteSecurityGroup", url.Values{"GroupId": {groupID}})
	return failure
}

// ecsGatewayApplyLoadBalancerAttributes sets the attributes a gateway's
// settings name on one of its load balancers.
func ecsGatewayApplyLoadBalancerAttributes(client ecsGatewayClient, gateway ECSGateway, lb ECSGatewayLoadBalancer) *ecsGatewayCallFailure {
	config := gateway.Configuration
	attributes := [][2]string{
		{"routing.http.desync_mitigation_mode", config.HttpDesync},
		{"deletion_protection.enabled", strconv.FormatBool(gateway.DeletionProtection)},
	}
	if logs := config.AccessLogs; logs != nil && logs.Destination != nil && logs.Destination.S3 != nil {
		attributes = append(attributes,
			[2]string{"access_logs.s3.enabled", "true"},
			[2]string{"access_logs.s3.bucket", logs.Destination.S3.Bucket},
			[2]string{"access_logs.s3.prefix", logs.Destination.S3.Prefix})
	} else {
		attributes = append(attributes, [2]string{"access_logs.s3.enabled", "false"})
	}
	form := url.Values{"LoadBalancerArn": {lb.LoadBalancerArn}}
	for i, attribute := range attributes {
		form.Set(fmt.Sprintf("Attributes.member.%d.Key", i+1), attribute[0])
		form.Set(fmt.Sprintf("Attributes.member.%d.Value", i+1), attribute[1])
	}
	_, failure := client.elb("ModifyLoadBalancerAttributes", form)
	return failure
}

// ecsGatewayIssueCertificate requests the route's certificate. The hostname
// is in a domain AWS owns, so AWS validates it as soon as it is requested.
func ecsGatewayIssueCertificate(client ecsGatewayClient, gateway ECSGateway, route *ECSGatewayRoute) (string, *ecsGatewayCallFailure) {
	var tags []acmTag
	for _, tag := range ecsGatewayManagedTags(gateway, route.GatewayRouteArn) {
		tags = append(tags, acmTag{Key: tag[0], Value: tag[1]})
	}
	body, failure := client.json("acm", "acm", "CertificateManager.RequestCertificate", "RequestCertificate",
		acmRequestCertificateReq{DomainName: route.Hostname, ValidationMethod: "DNS", Tags: tags})
	if failure != nil {
		return "", failure
	}
	var out struct {
		CertificateArn string `json:"CertificateArn"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.CertificateArn == "" {
		return "", &ecsGatewayCallFailure{Operation: "acm:RequestCertificate", Code: "InternalFailure", Message: "no certificate ARN in the response"}
	}
	id := acmARNToID(out.CertificateArn)
	stored, ok := acmCertificates.Get(id)
	if !ok {
		return "", &ecsGatewayCallFailure{Operation: "acm:RequestCertificate", Code: "ResourceNotFoundException", Message: "Could not find certificate " + out.CertificateArn}
	}
	for i := range stored.Cert.DomainValidationOptions {
		stored.Cert.DomainValidationOptions[i].ValidationStatus = "SUCCESS"
	}
	if _, err := acmReconcileIssuance(id, stored); err != nil {
		return "", &ecsGatewayCallFailure{Operation: "acm:RequestCertificate", Code: "InternalFailure", Message: err.Error()}
	}
	return out.CertificateArn, nil
}

// ecsGatewayReplaceTargetGroup creates a target group for the route's desired
// settings, points the route's rules at it and deletes the one it replaced.
func ecsGatewayReplaceTargetGroup(client ecsGatewayClient, gateway ECSGateway, route *ECSGatewayRoute, lb ECSGatewayLoadBalancer) *ecsGatewayCallFailure {
	spec := route.Desired
	vpc, _ := ecsSubnetsPlacement(gateway.Configuration.NetworkConfiguration.Subnets)
	form := url.Values{
		"Name":            {ecsGatewayRandomName()},
		"Protocol":        {"HTTP"},
		"ProtocolVersion": {spec.ProtocolVersion},
		"Port":            {strconv.Itoa(spec.Port)},
		"VpcId":           {vpc},
		"TargetType":      {"ip"},
		// "By default, a gateway sends an HTTP request to / on the port that
		// portName names, every 30 seconds, and expects a 200 response within
		// 5 seconds."
		"HealthCheckProtocol":        {"HTTP"},
		"HealthCheckPath":            {"/"},
		"HealthCheckIntervalSeconds": {"30"},
		"HealthCheckTimeoutSeconds":  {"5"},
		"Matcher.HttpCode":           {"200"},
	}
	if spec.ProtocolVersion == "GRPC" {
		form.Del("Matcher.HttpCode")
	}
	if check := spec.HealthCheck; check != nil {
		set := func(key string, value *int32) {
			if value != nil {
				form.Set(key, strconv.Itoa(int(*value)))
			}
		}
		if check.HealthCheckProtocol != "" {
			form.Set("HealthCheckProtocol", check.HealthCheckProtocol)
		}
		if check.HealthCheckPath != "" {
			form.Set("HealthCheckPath", check.HealthCheckPath)
		}
		set("HealthCheckPort", check.HealthCheckPort)
		set("HealthCheckIntervalSeconds", check.HealthCheckIntervalSeconds)
		set("HealthCheckTimeoutSeconds", check.HealthCheckTimeoutSeconds)
		set("HealthyThresholdCount", check.HealthyThresholdCount)
		set("UnhealthyThresholdCount", check.UnhealthyThresholdCount)
		if check.Matcher != nil {
			if check.Matcher.HttpCode != "" {
				form.Set("Matcher.HttpCode", check.Matcher.HttpCode)
			}
			if check.Matcher.GrpcCode != "" {
				form.Del("Matcher.HttpCode")
				form.Set("Matcher.GrpcCode", check.Matcher.GrpcCode)
			}
		}
	}
	ecsGatewayELBTags(form, ecsGatewayManagedTags(gateway, route.GatewayRouteArn))
	body, failure := client.elb("CreateTargetGroup", form)
	if failure != nil {
		return failure
	}
	previous := route.TargetGroupArn
	route.TargetGroupArn = awsXMLText(body, "TargetGroupArn")
	applied := spec
	route.Applied = &applied
	ecsGatewaySaveRoute(route)
	for _, ruleArn := range append([]string{route.ListenerRuleArn}, route.TestListenerRuleArns...) {
		if ruleArn == "" {
			continue
		}
		if _, failure := client.elb("ModifyRule", url.Values{
			"RuleArn":                         {ruleArn},
			"Actions.member.1.Type":           {"forward"},
			"Actions.member.1.TargetGroupArn": {route.TargetGroupArn},
		}); failure != nil {
			return failure
		}
	}
	if previous != "" {
		if _, failure := client.elb("DeleteTargetGroup", url.Values{"TargetGroupArn": {previous}}); failure != nil {
			return failure
		}
	}
	return nil
}

// ecsGatewayCreateRule creates a route's listener rule: the production rule
// matches the route's hostname, and a test rule also the gateway's test
// traffic header, at a priority ahead of the production rule.
func ecsGatewayCreateRule(client ecsGatewayClient, gateway ECSGateway, route *ECSGatewayRoute, priority int, header *ecsGatewayTestTrafficHeader) (string, *ecsGatewayCallFailure) {
	form := url.Values{
		"ListenerArn":               {route.ListenerArn},
		"Priority":                  {strconv.Itoa(priority)},
		"Conditions.member.1.Field": {"host-header"},
		"Conditions.member.1.HostHeaderConfig.Values.member.1": {route.Hostname},
		"Actions.member.1.Type":                                {"forward"},
		"Actions.member.1.TargetGroupArn":                      {route.TargetGroupArn},
	}
	if header != nil {
		form.Set("Conditions.member.2.Field", "http-header")
		form.Set("Conditions.member.2.HttpHeaderConfig.HttpHeaderName", header.Name)
		for i, value := range header.Values {
			form.Set(fmt.Sprintf("Conditions.member.2.HttpHeaderConfig.Values.member.%d", i+1), value)
		}
	}
	ecsGatewayELBTags(form, ecsGatewayManagedTags(gateway, route.GatewayRouteArn))
	body, failure := client.elb("CreateRule", form)
	if failure != nil {
		return "", failure
	}
	return awsXMLText(body, "RuleArn"), nil
}

// ecsGatewayAdmitLoadBalancer manages the ingress rule that lets the load
// balancer's security group reach the route's port in the service's security
// group.
func ecsGatewayAdmitLoadBalancer(client ecsGatewayClient, route *ECSGatewayRoute, lb ECSGatewayLoadBalancer) *ecsGatewayCallFailure {
	port := route.Applied.Port
	if route.ServiceSecurityGroupRuleID != "" && route.ServiceSecurityGroupPort == port {
		return nil
	}
	if route.ServiceSecurityGroupRuleID != "" {
		if failure := ecsGatewayRevokeServiceRule(client, route); failure != nil {
			return failure
		}
	}
	service, ok := ecsServices.Get(route.ServiceKey)
	if !ok {
		return nil
	}
	awsvpc := ecsServiceAwsvpcConfiguration(service)
	group := ""
	if len(awsvpc.SecurityGroups) > 0 {
		group = awsvpc.SecurityGroups[0]
	} else if vpc, _ := ecsSubnetsPlacement(awsvpc.Subnets); vpc != "" {
		if defaults := ec2DefaultSecurityGroupIDs(vpc); len(defaults) > 0 {
			group = defaults[0]
		}
	}
	if group == "" {
		return nil
	}
	body, failure := client.ec2("AuthorizeSecurityGroupIngress", url.Values{
		"GroupId":                              {group},
		"IpPermissions.1.IpProtocol":           {"tcp"},
		"IpPermissions.1.FromPort":             {strconv.Itoa(port)},
		"IpPermissions.1.ToPort":               {strconv.Itoa(port)},
		"IpPermissions.1.Groups.1.GroupId":     {lb.SecurityGroupID},
		"IpPermissions.1.Groups.1.Description": {"Amazon ECS gateway route " + route.GatewayRouteArn},
	})
	ruleID := ""
	switch {
	case failure == nil:
		ruleID = awsXMLText(body, "securityGroupRuleId")
	case failure.Code == "InvalidPermission.Duplicate":
		for _, rule := range ec2SecurityGroupRules.List() {
			if rule.GroupId == group && !rule.IsEgress && rule.IpProtocol == "tcp" &&
				rule.FromPort == port && rule.ToPort == port && rule.RefGroupId == lb.SecurityGroupID {
				ruleID = rule.RuleId
			}
		}
	default:
		return failure
	}
	route.ServiceSecurityGroupID = group
	route.ServiceSecurityGroupRuleID = ruleID
	route.ServiceSecurityGroupPort = port
	ecsGatewaySaveRoute(route)
	return nil
}

// ecsGatewayRevokeServiceRule revokes the route's ingress rule unless another
// route admits the same load balancer to the same port through it.
func ecsGatewayRevokeServiceRule(client ecsGatewayClient, route *ECSGatewayRoute) *ecsGatewayCallFailure {
	shared := len(ecsGatewayRoutes.Filter(func(other ECSGatewayRoute) bool {
		return other.GatewayRouteArn != route.GatewayRouteArn && other.ServiceSecurityGroupRuleID == route.ServiceSecurityGroupRuleID
	})) > 0
	if !shared && route.ServiceSecurityGroupRuleID != "" {
		if _, failure := client.ec2("RevokeSecurityGroupIngress", url.Values{
			"GroupId":               {route.ServiceSecurityGroupID},
			"SecurityGroupRuleId.1": {route.ServiceSecurityGroupRuleID},
		}); failure != nil {
			return failure
		}
	}
	route.ServiceSecurityGroupID = ""
	route.ServiceSecurityGroupRuleID = ""
	route.ServiceSecurityGroupPort = 0
	ecsGatewaySaveRoute(route)
	return nil
}

// ecsGatewayTeardownRoute removes a route's own resources, and its load
// balancer with that load balancer's listener and security group when the
// route was the last one on it.
func ecsGatewayTeardownRoute(client ecsGatewayClient, route *ECSGatewayRoute) *ecsGatewayCallFailure {
	for len(route.TestListenerRuleArns) > 0 {
		if _, failure := client.elb("DeleteRule", url.Values{"RuleArn": {route.TestListenerRuleArns[0]}}); failure != nil {
			return failure
		}
		route.TestListenerRuleArns = route.TestListenerRuleArns[1:]
		ecsGatewaySaveRoute(route)
	}
	if route.ListenerRuleArn != "" {
		if _, failure := client.elb("DeleteRule", url.Values{"RuleArn": {route.ListenerRuleArn}}); failure != nil {
			return failure
		}
		route.ListenerRuleArn = ""
		ecsGatewaySaveRoute(route)
	}
	if route.TargetGroupArn != "" {
		if _, failure := client.elb("DeleteTargetGroup", url.Values{"TargetGroupArn": {route.TargetGroupArn}}); failure != nil {
			return failure
		}
		route.TargetGroupArn = ""
		route.Applied = nil
		ecsGatewaySaveRoute(route)
	}
	if route.ServiceSecurityGroupRuleID != "" {
		if failure := ecsGatewayRevokeServiceRule(client, route); failure != nil {
			return failure
		}
	}
	lb, onLoadBalancer := ecsGatewayRouteLoadBalancer(*route)
	remaining := ecsGatewayRoutes.Filter(func(other ECSGatewayRoute) bool {
		return other.GatewayRouteArn != route.GatewayRouteArn && other.LoadBalancerArn == route.LoadBalancerArn && route.LoadBalancerArn != ""
	})
	if onLoadBalancer && len(remaining) > 0 && route.CertificateOnListener && lb.ListenerArn != "" {
		if failure := ecsGatewayDetachCertificate(client, route, lb, remaining); failure != nil {
			return failure
		}
	}
	if onLoadBalancer && len(remaining) == 0 {
		if failure := ecsGatewayDeleteLoadBalancer(client, route.GatewayArn, lb); failure != nil {
			return failure
		}
	}
	route.CertificateOnListener = false
	if route.CertificateArn != "" {
		if _, failure := client.json("acm", "acm", "CertificateManager.DeleteCertificate", "DeleteCertificate",
			map[string]string{"CertificateArn": route.CertificateArn}); failure != nil && failure.Code != "ResourceNotFoundException" {
			return failure
		}
		route.CertificateArn = ""
		ecsGatewaySaveRoute(route)
	}
	return nil
}

// ecsGatewayDetachCertificate takes a leaving route's certificate off its
// listener; when it is the listener's default, another route's certificate
// takes its place.
func ecsGatewayDetachCertificate(client ecsGatewayClient, route *ECSGatewayRoute, lb ECSGatewayLoadBalancer, remaining []ECSGatewayRoute) *ecsGatewayCallFailure {
	listener, ok := elbv2Listeners.Get(lb.ListenerArn)
	if ok && slices.Contains(listener.Certificates, route.CertificateArn) {
		replacement := ""
		for _, other := range remaining {
			if other.CertificateArn != "" && other.CertificateOnListener {
				replacement = other.CertificateArn
				break
			}
		}
		if replacement != "" {
			if _, failure := client.elb("ModifyListener", url.Values{
				"ListenerArn":                          {lb.ListenerArn},
				"Certificates.member.1.CertificateArn": {replacement},
			}); failure != nil {
				return failure
			}
			if _, failure := client.elb("RemoveListenerCertificates", url.Values{
				"ListenerArn":                          {lb.ListenerArn},
				"Certificates.member.1.CertificateArn": {replacement},
			}); failure != nil {
				return failure
			}
		}
	} else if _, failure := client.elb("RemoveListenerCertificates", url.Values{
		"ListenerArn":                          {lb.ListenerArn},
		"Certificates.member.1.CertificateArn": {route.CertificateArn},
	}); failure != nil {
		return failure
	}
	route.CertificateOnListener = false
	ecsGatewaySaveRoute(route)
	return nil
}

// ecsGatewayDeleteLoadBalancer removes a load balancer its last route left:
// its listener, the deletion protection the gateway gave it, the load
// balancer itself and then its security group.
func ecsGatewayDeleteLoadBalancer(client ecsGatewayClient, gatewayArn string, lb ECSGatewayLoadBalancer) *ecsGatewayCallFailure {
	if lb.ListenerArn != "" {
		if _, failure := client.elb("DeleteListener", url.Values{"ListenerArn": {lb.ListenerArn}}); failure != nil {
			return failure
		}
		lb.ListenerArn = ""
		ecsGatewaySaveLoadBalancer(gatewayArn, lb)
	}
	if lb.LoadBalancerArn != "" {
		if _, failure := client.elb("ModifyLoadBalancerAttributes", url.Values{
			"LoadBalancerArn":           {lb.LoadBalancerArn},
			"Attributes.member.1.Key":   {"deletion_protection.enabled"},
			"Attributes.member.1.Value": {"false"},
		}); failure != nil && failure.Code != "LoadBalancerNotFound" {
			return failure
		}
		if _, failure := client.elb("DeleteLoadBalancer", url.Values{"LoadBalancerArn": {lb.LoadBalancerArn}}); failure != nil {
			return failure
		}
	}
	if lb.SecurityGroupID != "" {
		if failure := ecsGatewayDeleteSecurityGroup(client, lb.SecurityGroupID); failure != nil && failure.Code != "InvalidGroup.NotFound" {
			return failure
		}
	}
	ecsGatewayRemoveLoadBalancer(gatewayArn, lb.LoadBalancerArn)
	return nil
}

// ecsGatewayRunUpdate applies a gateway's settings to each of its load
// balancers, records the ones it could not update, and reports the outcome
// the gateway's status and statusReason carry.
func ecsGatewayRunUpdate(gatewayArn string) {
	ecsGatewayMu.Lock()
	defer ecsGatewayMu.Unlock()
	gateway, ok := ecsGateways.Get(gatewayArn)
	if !ok || gateway.Status != ecsGatewayStatusUpdateInProgress {
		return
	}
	client := ecsGatewayClientFor(gateway)
	var failures []ecsGatewayResourceFailure
	for _, lb := range gateway.LoadBalancers {
		var errors []ecsGatewayOperationError
		record := func(failure *ecsGatewayCallFailure) {
			if failure != nil && len(errors) < 5 {
				errors = append(errors, ecsGatewayOperationError{Operation: failure.Operation, Code: failure.Code, Message: failure.Message})
			}
		}
		record(ecsGatewayApplyLoadBalancerAttributes(client, gateway, lb))
		record(ecsGatewayApplyInboundSecurityGroup(client, gateway, &lb))
		record(ecsGatewayApplyWebACL(client, gateway, &lb))
		ecsGatewaySaveLoadBalancer(gateway.GatewayArn, lb)
		if len(errors) > 0 && len(failures) < 200 {
			failures = append(failures, ecsGatewayResourceFailure{Arn: lb.LoadBalancerArn, Errors: errors})
		}
	}
	ecsGateways.Update(gatewayArn, func(current *ECSGateway) {
		if current.UpdateGeneration != gateway.UpdateGeneration {
			return
		}
		total := len(gateway.LoadBalancers)
		updated := total - len(failures)
		current.UpdatedAt = ecsEpochSeconds()
		if len(failures) == 0 {
			current.Status = ecsGatewayStatusUpdateComplete
			current.StatusReason = fmt.Sprintf("%d/%d load balancers updated", updated, total)
			current.Failures = nil
			return
		}
		current.Status = ecsGatewayStatusUpdateFailed
		current.StatusReason = fmt.Sprintf("%d/%d load balancers updated, %d load balancers failed update", updated, total, len(failures))
		current.Failures = failures
	})
}

// ecsGatewayApplyInboundSecurityGroup attaches the gateway's inbound security
// group to a load balancer and stops managing inbound rules, or, without one,
// returns the load balancer to the gateway's own group and its rules.
func ecsGatewayApplyInboundSecurityGroup(client ecsGatewayClient, gateway ECSGateway, lb *ECSGatewayLoadBalancer) *ecsGatewayCallFailure {
	network := gateway.Configuration.NetworkConfiguration
	groups := []string{lb.SecurityGroupID}
	if network.InboundSecurityGroup != "" {
		groups = append(groups, network.InboundSecurityGroup)
	}
	form := url.Values{"LoadBalancerArn": {lb.LoadBalancerArn}}
	for i, group := range groups {
		form.Set(fmt.Sprintf("SecurityGroups.member.%d", i+1), group)
	}
	if _, failure := client.elb("SetSecurityGroups", form); failure != nil {
		return failure
	}
	switch {
	case network.InboundSecurityGroup != "" && len(lb.IngressRuleIDs) > 0:
		form := url.Values{"GroupId": {lb.SecurityGroupID}}
		for i, id := range lb.IngressRuleIDs {
			form.Set(fmt.Sprintf("SecurityGroupRuleId.%d", i+1), id)
		}
		if _, failure := client.ec2("RevokeSecurityGroupIngress", form); failure != nil {
			return failure
		}
		lb.IngressRuleIDs = nil
	case network.InboundSecurityGroup == "" && len(lb.IngressRuleIDs) == 0:
		ruleIDs, failure := ecsGatewayAuthorizeHTTPS(client, lb.SecurityGroupID, network.IpAddressType)
		if failure != nil {
			return failure
		}
		lb.IngressRuleIDs = ruleIDs
	}
	return nil
}

func ecsGatewayApplyWebACL(client ecsGatewayClient, gateway ECSGateway, lb *ECSGatewayLoadBalancer) *ecsGatewayCallFailure {
	wanted := gateway.Configuration.WebACLArn
	if wanted == lb.WebACLArn {
		return nil
	}
	if wanted == "" {
		if _, failure := client.json("wafv2", "wafv2", "AWSWAF_20190729.DisassociateWebACL", "DisassociateWebACL",
			map[string]string{"ResourceArn": lb.LoadBalancerArn}); failure != nil {
			return failure
		}
	} else if _, failure := client.json("wafv2", "wafv2", "AWSWAF_20190729.AssociateWebACL", "AssociateWebACL",
		map[string]string{"WebACLArn": wanted, "ResourceArn": lb.LoadBalancerArn}); failure != nil {
		return failure
	}
	lb.WebACLArn = wanted
	return nil
}
