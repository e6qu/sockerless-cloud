package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// An Amazon ECS gateway provisions nothing until a service in an associated
// cluster connects through a gateway connector; each connection is a gateway
// route (ecs_gateway_routes.go).

const (
	ecsGatewayStatusActive           = "ACTIVE"
	ecsGatewayStatusUpdateInProgress = "UPDATE_IN_PROGRESS"
	ecsGatewayStatusUpdateComplete   = "UPDATE_COMPLETE"
	ecsGatewayStatusUpdateFailed     = "UPDATE_FAILED"
	ecsGatewayStatusDeleted          = "DELETED"

	ecsGatewayAssociationActive         = "ACTIVE"
	ecsGatewayAssociationDisassociating = "DISASSOCIATING"

	// Gateway Service Limits: "Max Gateway-Cluster Associations Per Cluster 5",
	// "Max Clusters Per Gateway 10,000", "Max Application Load Balancers Per
	// Gateway 200", "Max Services Per Application Load Balancer 25".
	ecsGatewayMaxAssociationsPerCluster = 5
	ecsGatewayMaxClustersPerGateway     = 10000
	ecsGatewayMaxAlbCountLimit          = 200
	ecsGatewayRoutesPerLoadBalancer     = 25

	ecsGatewayDefaultMaxAlbCount = 10
)

type ecsGatewayElbNetworkConfiguration struct {
	InboundSecurityGroup string   `json:"inboundSecurityGroup,omitempty"`
	Subnets              []string `json:"subnets,omitempty"`
	IpAddressType        string   `json:"ipAddressType,omitempty"`
}

type ecsGatewayAccessLogS3Destination struct {
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix,omitempty"`
}

type ecsGatewayAccessLogDestination struct {
	S3 *ecsGatewayAccessLogS3Destination `json:"s3,omitempty"`
}

type ecsGatewayAccessLogConfiguration struct {
	Destination *ecsGatewayAccessLogDestination `json:"destination,omitempty"`
}

type ecsGatewayTestTrafficHeader struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

type ecsGatewayTestTrafficRules struct {
	Header *ecsGatewayTestTrafficHeader `json:"header,omitempty"`
}

// As DescribeGateway returns it; the request shapes are subsets.
type ecsGatewayAlbConfiguration struct {
	NetworkConfiguration *ecsGatewayElbNetworkConfiguration `json:"networkConfiguration,omitempty"`
	Scheme               string                             `json:"scheme,omitempty"`
	WebACLArn            string                             `json:"webACLArn,omitempty"`
	AccessLogs           *ecsGatewayAccessLogConfiguration  `json:"accessLogs,omitempty"`
	Port                 *int32                             `json:"port,omitempty"`
	Protocol             string                             `json:"protocol,omitempty"`
	MaxAlbCount          *int32                             `json:"maxAlbCount,omitempty"`
	CurrentAlbCount      *int32                             `json:"currentAlbCount,omitempty"`
	HttpDesync           string                             `json:"httpDesync,omitempty"`
	TestTrafficRules     *ecsGatewayTestTrafficRules        `json:"testTrafficRules,omitempty"`
}

type ecsGatewayOperationError struct {
	Operation string `json:"operation"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

type ecsGatewayResourceFailure struct {
	Arn    string                     `json:"arn"`
	Errors []ecsGatewayOperationError `json:"errors"`
}

// With the listener and security group created alongside it.
type ECSGatewayLoadBalancer struct {
	LoadBalancerArn string `json:"loadBalancerArn"`
	DNSName         string `json:"dnsName"`
	ListenerArn     string `json:"listenerArn,omitempty"`
	SecurityGroupID string `json:"securityGroupId,omitempty"`
	// None while the gateway has an inbound security group of its own.
	IngressRuleIDs []string `json:"ingressRuleIds,omitempty"`
	WebACLArn      string   `json:"webACLArn,omitempty"`
}

type ECSGateway struct {
	GatewayArn            string                      `json:"gatewayArn"`
	Name                  string                      `json:"name"`
	ID                    string                      `json:"id"`
	Status                string                      `json:"status"`
	StatusReason          string                      `json:"statusReason,omitempty"`
	Failures              []ecsGatewayResourceFailure `json:"failures,omitempty"`
	InfrastructureRoleArn string                      `json:"infrastructureRoleArn"`
	DeletionProtection    bool                        `json:"deletionProtection"`
	Configuration         ecsGatewayAlbConfiguration  `json:"configuration"`
	AccessType            string                      `json:"accessType"`
	PropagateTags         string                      `json:"propagateTags"`
	Tags                  []ECSTag                    `json:"tags,omitempty"`
	CreatedAt             float64                     `json:"createdAt"`
	UpdatedAt             float64                     `json:"updatedAt"`
	ClientToken           string                      `json:"clientToken,omitempty"`
	LoadBalancers         []ECSGatewayLoadBalancer    `json:"loadBalancers,omitempty"`
	// An update applying an older configuration does not report over a newer one.
	UpdateGeneration int `json:"updateGeneration,omitempty"`
}

type ECSGatewayAssociation struct {
	GatewayArn string `json:"gatewayArn"`
	ClusterArn string `json:"clusterArn"`
	Status     string `json:"status"`
}

var (
	ecsGateways            sim.Store[ECSGateway]
	ecsGatewayAssociations sim.Store[ECSGatewayAssociation]

	// Serialize gateway, association and route changes so two routes never both
	// take a load balancer's last free slot.
	ecsGatewayMu sync.Mutex
)

func registerECSGateways(r *AWSRouter, srv *sim.Server) {
	ecsGateways = sim.MakeStore[ECSGateway](srv.DB(), "ecs_gateways")
	ecsGatewayAssociations = sim.MakeStore[ECSGatewayAssociation](srv.DB(), "ecs_gateway_associations")
	ecsGatewayRoutes = sim.MakeStore[ECSGatewayRoute](srv.DB(), "ecs_gateway_routes")
	ecsServiceGatewayConnectors = sim.MakeStore[[]ecsGatewayConnector](srv.DB(), "ecs_service_gateway_connectors")

	r.Register("AmazonEC2ContainerServiceV20141113.CreateGateway", handleECSCreateGateway)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeGateway", handleECSDescribeGateway)
	r.Register("AmazonEC2ContainerServiceV20141113.ListGateways", handleECSListGateways)
	r.Register("AmazonEC2ContainerServiceV20141113.UpdateGateway", handleECSUpdateGateway)
	r.Register("AmazonEC2ContainerServiceV20141113.DeleteGateway", handleECSDeleteGateway)
	r.Register("AmazonEC2ContainerServiceV20141113.AssociateGatewayWithCluster", handleECSAssociateGatewayWithCluster)
	r.Register("AmazonEC2ContainerServiceV20141113.DisassociateGatewayFromCluster", handleECSDisassociateGatewayFromCluster)
	r.Register("AmazonEC2ContainerServiceV20141113.ListGatewayClusterAssociations", handleECSListGatewayClusterAssociations)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeGatewayResources", handleECSDescribeGatewayResources)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeGatewayRoute", handleECSDescribeGatewayRoute)
	r.Register("AmazonEC2ContainerServiceV20141113.ListGatewayRoutes", handleECSListGatewayRoutes)
}

func ecsGatewayNotFound(w http.ResponseWriter) {
	AWSError(w, "GatewayNotFoundException",
		"The specified gateway wasn't found. Check that the gateway name or ARN is correct and that the gateway exists in your account and Region.",
		http.StatusBadRequest)
}

func ecsResolveGateway(ref string) (ECSGateway, bool) {
	if ref == "" {
		return ECSGateway{}, false
	}
	if strings.HasPrefix(ref, "arn:") {
		return ecsGateways.Get(ref)
	}
	return ecsGatewaysByName.Lookup(ecsGateways, ref, func(gateway ECSGateway) []string {
		return []string{gateway.Name}
	})
}

var ecsGatewaysByName sim.GenerationIndex[ECSGateway]

func ecsResolveCluster(ref string) (ECSCluster, bool) {
	cluster, ok := ecsClusters.Get(ecsClusterNameFromRef(ref))
	if !ok || cluster.Status != "ACTIVE" {
		return ECSCluster{}, false
	}
	if strings.HasPrefix(ref, "arn:") && cluster.ClusterArn != ref {
		return ECSCluster{}, false
	}
	return cluster, true
}

func ecsGatewayAssociationKey(gatewayArn, clusterArn string) string {
	return gatewayArn + "|" + clusterArn
}

// Exactly one member names the gateway type.
func ecsGatewayConfigurationVariant(raw json.RawMessage) (*ecsGatewayAlbConfiguration, string) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, "configuration is required"
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, "configuration must be an object"
	}
	if len(members) != 1 {
		return nil, "Exactly one configuration variant must be set."
	}
	body, ok := members["hostSharedAlb"]
	if !ok {
		for name := range members {
			return nil, fmt.Sprintf("Unsupported gateway configuration variant: %s", name)
		}
	}
	var config ecsGatewayAlbConfiguration
	if err := json.Unmarshal(body, &config); err != nil {
		return nil, "configuration.hostSharedAlb is malformed: " + err.Error()
	}
	return &config, ""
}

func ecsValidateGatewayMutableSettings(config *ecsGatewayAlbConfiguration) string {
	if config.MaxAlbCount != nil && (*config.MaxAlbCount < 1 || *config.MaxAlbCount > ecsGatewayMaxAlbCountLimit) {
		return fmt.Sprintf("maxAlbCount must be between 1 and %d.", ecsGatewayMaxAlbCountLimit)
	}
	if config.HttpDesync != "" && !slices.Contains([]string{"monitor", "defensive", "strictest"}, config.HttpDesync) {
		return "httpDesync must be one of monitor, defensive or strictest."
	}
	if logs := config.AccessLogs; logs != nil {
		if logs.Destination == nil || logs.Destination.S3 == nil || logs.Destination.S3.Bucket == "" {
			return "accessLogs.destination.s3.bucket is required."
		}
	}
	if rules := config.TestTrafficRules; rules != nil && rules.Header != nil {
		if rules.Header.Name == "" || len(rules.Header.Values) == 0 {
			return "testTrafficRules.header requires a name and at least one value."
		}
	}
	if config.WebACLArn != "" && !strings.HasPrefix(config.WebACLArn, "arn:") {
		return "webACLArn must be an ARN."
	}
	return ""
}

// The defaults the API reference states.
func ecsGatewayApplyCreateDefaults(config *ecsGatewayAlbConfiguration) string {
	if config.NetworkConfiguration == nil {
		config.NetworkConfiguration = &ecsGatewayElbNetworkConfiguration{}
	}
	network := config.NetworkConfiguration
	switch network.IpAddressType {
	case "":
		network.IpAddressType = "dualstack"
	case "ipv4", "dualstack", "dualstack-without-public-ipv4":
	default:
		return "ipAddressType must be one of ipv4, dualstack or dualstack-without-public-ipv4."
	}
	if len(network.Subnets) == 0 {
		network.Subnets = ecsDefaultVPCSubnets()
		if len(network.Subnets) == 0 {
			return "No default VPC is available in this Region; specify subnets for the gateway."
		}
	}
	switch config.Scheme {
	case "":
		config.Scheme = "internet-facing"
	case "internet-facing", "internal":
	default:
		return "scheme must be internet-facing or internal."
	}
	// "The gateway supports only HTTPS at port 443."
	if config.Port != nil && *config.Port != 443 {
		return "The gateway supports only HTTPS at port 443."
	}
	if config.Protocol != "" && config.Protocol != "HTTPS" {
		return "The gateway supports only HTTPS at port 443."
	}
	port := int32(443)
	config.Port = &port
	config.Protocol = "HTTPS"
	if config.MaxAlbCount == nil {
		count := int32(ecsGatewayDefaultMaxAlbCount)
		config.MaxAlbCount = &count
	}
	if config.HttpDesync == "" {
		config.HttpDesync = "defensive"
	}
	return ecsValidateGatewayMutableSettings(config)
}

func ecsDefaultVPCSubnets() []string {
	var subnets []string
	for _, subnet := range ec2Subnets.List() {
		if vpc, ok := ec2Vpcs.Get(subnet.VpcId); ok && vpc.IsDefault {
			subnets = append(subnets, subnet.SubnetId)
		}
	}
	slices.Sort(subnets)
	return subnets
}

func ecsGatewayAccessType(scheme string) string {
	if scheme == "internal" {
		return "PRIVATE"
	}
	return "PUBLIC"
}

// CreateGateway and UpdateGateway check the role before accepting it.
func ecsGatewayRoleAssumable(gatewayArn, roleArn string) bool {
	_, err := awsAssumeServiceRole(awsActor{Service: "ecs.amazonaws.com", Source: gatewayArn, Role: roleArn})
	return err == nil
}

const ecsGatewayRoleNotAssumable = "The infrastructureRoleArn could not be assumed. Verify the role exists and its trust policy allows ECS to assume it."

func ecsValidateECSTags(tags []ECSTag) string {
	if len(tags) > 50 {
		return "The number of tags exceeds the maximum of 50."
	}
	seen := map[string]bool{}
	for _, tag := range tags {
		if tag.Key == "" || len(tag.Key) > 128 {
			return "Tag keys must be between 1 and 128 characters."
		}
		if len(tag.Value) > 256 {
			return "Tag values must be at most 256 characters."
		}
		if seen[tag.Key] {
			return "Tag keys must be unique: " + tag.Key
		}
		seen[tag.Key] = true
	}
	return ""
}

func handleECSCreateGateway(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                  string          `json:"name"`
		InfrastructureRoleArn string          `json:"infrastructureRoleArn"`
		ClientToken           string          `json:"clientToken"`
		DeletionProtection    *bool           `json:"deletionProtection"`
		Configuration         json.RawMessage `json:"configuration"`
		Tags                  []ECSTag        `json:"tags"`
		PropagateTags         string          `json:"propagateTags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		AWSError(w, "InvalidParameterException", "name is required", http.StatusBadRequest)
		return
	}
	if len(req.Name) > 255 {
		AWSError(w, "InvalidParameterException", "The gateway name can be up to 255 characters.", http.StatusBadRequest)
		return
	}
	if req.InfrastructureRoleArn == "" {
		AWSError(w, "InvalidParameterException", "infrastructureRoleArn is required", http.StatusBadRequest)
		return
	}
	config, problem := ecsGatewayConfigurationVariant(req.Configuration)
	if problem == "" {
		problem = ecsGatewayApplyCreateDefaults(config)
	}
	if problem == "" {
		problem = ecsValidateECSTags(req.Tags)
	}
	propagate := req.PropagateTags
	if propagate == "" {
		propagate = "GATEWAY"
	}
	if problem == "" && propagate != "GATEWAY" && propagate != "NONE" {
		problem = "propagateTags must be GATEWAY or NONE."
	}
	if problem != "" {
		AWSError(w, "InvalidParameterException", problem, http.StatusBadRequest)
		return
	}

	ecsGatewayMu.Lock()
	defer ecsGatewayMu.Unlock()
	if existing, ok := ecsResolveGateway(req.Name); ok {
		if req.ClientToken != "" && existing.ClientToken == req.ClientToken {
			sim.WriteJSON(w, http.StatusOK, ecsGatewaySummaryJSON(existing))
			return
		}
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"A gateway named %s already exists in this account and Region.", req.Name)
		return
	}
	id := sim.RandomHex(16)
	arn := ecsArn("gateway", req.Name+"/"+id)
	if !ecsGatewayRoleAssumable(arn, req.InfrastructureRoleArn) {
		AWSError(w, "InvalidParameterException", ecsGatewayRoleNotAssumable, http.StatusBadRequest)
		return
	}
	deletionProtection := true
	if req.DeletionProtection != nil {
		deletionProtection = *req.DeletionProtection
	}
	now := ecsEpochSeconds()
	gateway := ECSGateway{
		GatewayArn:            arn,
		Name:                  req.Name,
		ID:                    id,
		Status:                ecsGatewayStatusActive,
		InfrastructureRoleArn: req.InfrastructureRoleArn,
		DeletionProtection:    deletionProtection,
		Configuration:         *config,
		AccessType:            ecsGatewayAccessType(config.Scheme),
		PropagateTags:         propagate,
		Tags:                  req.Tags,
		CreatedAt:             now,
		UpdatedAt:             now,
		ClientToken:           req.ClientToken,
	}
	ecsGateways.Put(arn, gateway)
	sim.WriteJSON(w, http.StatusOK, ecsGatewaySummaryJSON(gateway))
}

func ecsGatewaySummaryJSON(gateway ECSGateway) map[string]any {
	return map[string]any{
		"gatewayArn": gateway.GatewayArn,
		"name":       gateway.Name,
		"status":     gateway.Status,
	}
}

func ecsGatewayJSON(gateway ECSGateway) map[string]any {
	config := gateway.Configuration
	current := int32(len(gateway.LoadBalancers))
	config.CurrentAlbCount = &current
	tags := gateway.Tags
	if tags == nil {
		tags = []ECSTag{}
	}
	out := map[string]any{
		"gatewayArn":            gateway.GatewayArn,
		"name":                  gateway.Name,
		"status":                gateway.Status,
		"infrastructureRoleArn": gateway.InfrastructureRoleArn,
		"deletionProtection":    gateway.DeletionProtection,
		"configuration":         map[string]any{"hostSharedAlb": config},
		"accessType":            gateway.AccessType,
		"propagateTags":         gateway.PropagateTags,
		"tags":                  tags,
		"createdAt":             gateway.CreatedAt,
		"updatedAt":             gateway.UpdatedAt,
	}
	if gateway.StatusReason != "" {
		out["statusReason"] = gateway.StatusReason
	}
	// "failures appears only when the gateway is in a failure state."
	if gateway.Status == ecsGatewayStatusUpdateFailed && len(gateway.Failures) > 0 {
		out["failures"] = gateway.Failures
	}
	return out
}

func handleECSDescribeGateway(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Gateway string `json:"gateway"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Gateway == "" {
		AWSError(w, "InvalidParameterException", "gateway is required", http.StatusBadRequest)
		return
	}
	gateway, ok := ecsResolveGateway(req.Gateway)
	if !ok {
		ecsGatewayNotFound(w)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"gateway": ecsGatewayJSON(gateway)})
}

var ecsGatewayStatuses = []string{
	ecsGatewayStatusActive, ecsGatewayStatusUpdateInProgress, ecsGatewayStatusUpdateComplete,
	ecsGatewayStatusUpdateFailed, ecsGatewayStatusDeleted,
}

// Checks the enum, the uniqueItems trait and the length bound.
func ecsValidateStatusFilter(statuses, allowed []string, max int) string {
	if len(statuses) > max {
		return fmt.Sprintf("status accepts at most %d values.", max)
	}
	seen := map[string]bool{}
	for _, status := range statuses {
		if !slices.Contains(allowed, status) {
			return fmt.Sprintf("Invalid status %q: must be one of %s.", status, strings.Join(allowed, ", "))
		}
		if seen[status] {
			return fmt.Sprintf("status contains %q more than once.", status)
		}
		seen[status] = true
	}
	return ""
}

func ecsValidateMaxResults(maxResults *int, max int) string {
	if maxResults != nil && (*maxResults < 1 || *maxResults > max) {
		return fmt.Sprintf("maxResults must be between 1 and %d.", max)
	}
	return ""
}

func ecsMaxResults(maxResults *int) int {
	if maxResults == nil {
		return 0
	}
	return *maxResults
}

func handleECSListGateways(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names      []string `json:"names"`
		Status     []string `json:"status"`
		MaxResults *int     `json:"maxResults"`
		NextToken  string   `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	problem := ecsValidateStatusFilter(req.Status, ecsGatewayStatuses, 5)
	if problem == "" && len(req.Names) > 10 {
		problem = "names accepts at most 10 gateway names."
	}
	if problem == "" {
		problem = ecsValidateMaxResults(req.MaxResults, 100)
	}
	if problem != "" {
		AWSError(w, "InvalidParameterException", problem, http.StatusBadRequest)
		return
	}
	matched := ecsGateways.Filter(func(gateway ECSGateway) bool {
		return (len(req.Names) == 0 || slices.Contains(req.Names, gateway.Name)) &&
			(len(req.Status) == 0 || slices.Contains(req.Status, gateway.Status))
	})
	page, next, ok := awsKeyPage(w, ecsBadToken, matched, func(g ECSGateway) string { return g.GatewayArn },
		req.NextToken, ecsMaxResults(req.MaxResults), 100)
	if !ok {
		return
	}
	summaries := make([]map[string]any, 0, len(page))
	for _, gateway := range page {
		summaries = append(summaries, map[string]any{
			"gatewayArn": gateway.GatewayArn,
			"name":       gateway.Name,
			"status":     gateway.Status,
			"createdAt":  gateway.CreatedAt,
		})
	}
	out := map[string]any{"gateways": summaries}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleECSUpdateGateway(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Gateway               string          `json:"gateway"`
		InfrastructureRoleArn string          `json:"infrastructureRoleArn"`
		DeletionProtection    *bool           `json:"deletionProtection"`
		Configuration         json.RawMessage `json:"configuration"`
		PropagateTags         string          `json:"propagateTags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Gateway == "" {
		AWSError(w, "InvalidParameterException", "gateway is required", http.StatusBadRequest)
		return
	}
	var update *ecsGatewayAlbConfiguration
	if len(req.Configuration) > 0 && string(req.Configuration) != "null" {
		config, problem := ecsGatewayConfigurationVariant(req.Configuration)
		if problem == "" {
			problem = ecsValidateGatewayMutableSettings(config)
		}
		if problem != "" {
			AWSError(w, "InvalidParameterException", problem, http.StatusBadRequest)
			return
		}
		update = config
	}
	if req.PropagateTags != "" && req.PropagateTags != "GATEWAY" && req.PropagateTags != "NONE" {
		AWSError(w, "InvalidParameterException", "propagateTags must be GATEWAY or NONE.", http.StatusBadRequest)
		return
	}

	ecsGatewayMu.Lock()
	defer ecsGatewayMu.Unlock()
	gateway, ok := ecsResolveGateway(req.Gateway)
	if !ok {
		ecsGatewayNotFound(w)
		return
	}
	if req.InfrastructureRoleArn != "" {
		if !ecsGatewayRoleAssumable(gateway.GatewayArn, req.InfrastructureRoleArn) {
			AWSError(w, "InvalidParameterException", ecsGatewayRoleNotAssumable, http.StatusBadRequest)
			return
		}
		gateway.InfrastructureRoleArn = req.InfrastructureRoleArn
	}
	if req.DeletionProtection != nil {
		gateway.DeletionProtection = *req.DeletionProtection
	}
	if req.PropagateTags != "" {
		gateway.PropagateTags = req.PropagateTags
	}
	if update != nil {
		// The request's configuration replaces the mutable settings; omitted ones
		// revert or go. Creation-time settings are not in the update shape.
		config := gateway.Configuration
		network := *config.NetworkConfiguration
		network.InboundSecurityGroup = ""
		if update.NetworkConfiguration != nil {
			network.InboundSecurityGroup = update.NetworkConfiguration.InboundSecurityGroup
		}
		config.NetworkConfiguration = &network
		config.WebACLArn = update.WebACLArn
		config.AccessLogs = update.AccessLogs
		config.TestTrafficRules = update.TestTrafficRules
		config.HttpDesync = update.HttpDesync
		if config.HttpDesync == "" {
			config.HttpDesync = "defensive"
		}
		config.MaxAlbCount = update.MaxAlbCount
		if config.MaxAlbCount == nil {
			count := int32(ecsGatewayDefaultMaxAlbCount)
			config.MaxAlbCount = &count
		}
		gateway.Configuration = config
	}
	gateway.Status = ecsGatewayStatusUpdateInProgress
	gateway.UpdatedAt = ecsEpochSeconds()
	gateway.UpdateGeneration++
	ecsGateways.Put(gateway.GatewayArn, gateway)
	ecsGatewayScheduleUpdate(gateway.GatewayArn)
	sim.WriteJSON(w, http.StatusOK, ecsGatewaySummaryJSON(gateway))
}

func handleECSDeleteGateway(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Gateway string `json:"gateway"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Gateway == "" {
		AWSError(w, "InvalidParameterException", "gateway is required", http.StatusBadRequest)
		return
	}
	ecsGatewayMu.Lock()
	defer ecsGatewayMu.Unlock()
	gateway, ok := ecsResolveGateway(req.Gateway)
	if !ok {
		ecsGatewayNotFound(w)
		return
	}
	if gateway.DeletionProtection {
		AWSErrorf(w, "ClientException", http.StatusBadRequest,
			"The gateway %s has deletion protection enabled. Disable deletion protection before you delete the gateway.", gateway.Name)
		return
	}
	for _, association := range ecsGatewayAssociations.List() {
		if association.GatewayArn == gateway.GatewayArn {
			AWSErrorf(w, "ClientException", http.StatusBadRequest,
				"The gateway %s has cluster associations in the %s state. Disassociate every cluster before you delete the gateway.",
				gateway.Name, association.Status)
			return
		}
	}
	ecsGateways.Delete(gateway.GatewayArn)
	gateway.Status = ecsGatewayStatusDeleted
	sim.WriteJSON(w, http.StatusOK, ecsGatewaySummaryJSON(gateway))
}

func handleECSAssociateGatewayWithCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Gateway string `json:"gateway"`
		Cluster string `json:"cluster"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Gateway == "" || req.Cluster == "" {
		AWSError(w, "InvalidParameterException", "gateway and cluster are required", http.StatusBadRequest)
		return
	}
	ecsGatewayMu.Lock()
	defer ecsGatewayMu.Unlock()
	gateway, ok := ecsResolveGateway(req.Gateway)
	if !ok {
		ecsGatewayNotFound(w)
		return
	}
	cluster, ok := ecsResolveCluster(req.Cluster)
	if !ok {
		AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest, "Cluster not found: %s", req.Cluster)
		return
	}
	key := ecsGatewayAssociationKey(gateway.GatewayArn, cluster.ClusterArn)
	if existing, ok := ecsGatewayAssociations.Get(key); ok {
		// Associating again cancels a pending disassociation.
		existing.Status = ecsGatewayAssociationActive
		ecsGatewayAssociations.Put(key, existing)
		sim.WriteJSON(w, http.StatusOK, existing)
		return
	}
	perCluster, perGateway := 0, 0
	for _, association := range ecsGatewayAssociations.List() {
		if association.ClusterArn == cluster.ClusterArn {
			perCluster++
		}
		if association.GatewayArn == gateway.GatewayArn {
			perGateway++
		}
	}
	if perCluster >= ecsGatewayMaxAssociationsPerCluster {
		AWSErrorf(w, "ClientException", http.StatusBadRequest,
			"Cluster has reached the maximum number of gateway associations (%d).", ecsGatewayMaxAssociationsPerCluster)
		return
	}
	if perGateway >= ecsGatewayMaxClustersPerGateway {
		AWSErrorf(w, "ClientException", http.StatusBadRequest,
			"Gateway has reached the maximum number of cluster associations (%d).", ecsGatewayMaxClustersPerGateway)
		return
	}
	association := ECSGatewayAssociation{
		GatewayArn: gateway.GatewayArn,
		ClusterArn: cluster.ClusterArn,
		Status:     ecsGatewayAssociationActive,
	}
	ecsGatewayAssociations.Put(key, association)
	sim.WriteJSON(w, http.StatusOK, association)
}

func handleECSDisassociateGatewayFromCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Gateway string `json:"gateway"`
		Cluster string `json:"cluster"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Gateway == "" || req.Cluster == "" {
		AWSError(w, "InvalidParameterException", "gateway and cluster are required", http.StatusBadRequest)
		return
	}
	ecsGatewayMu.Lock()
	defer ecsGatewayMu.Unlock()
	gateway, ok := ecsResolveGateway(req.Gateway)
	if !ok {
		ecsGatewayNotFound(w)
		return
	}
	cluster, ok := ecsResolveCluster(req.Cluster)
	if !ok {
		AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest, "Cluster not found: %s", req.Cluster)
		return
	}
	key := ecsGatewayAssociationKey(gateway.GatewayArn, cluster.ClusterArn)
	association, ok := ecsGatewayAssociations.Get(key)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The gateway %s is not associated with cluster %s.", gateway.Name, cluster.ClusterName)
		return
	}
	association.Status = ecsGatewayAssociationDisassociating
	ecsGatewayAssociations.Put(key, association)
	// DISASSOCIATING until the cluster's last route through the gateway is gone.
	ecsGatewayCompleteDisassociation(gateway.GatewayArn, cluster.ClusterArn)
	sim.WriteJSON(w, http.StatusOK, association)
}

func handleECSListGatewayClusterAssociations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Gateway    string   `json:"gateway"`
		Cluster    string   `json:"cluster"`
		Status     []string `json:"status"`
		MaxResults *int     `json:"maxResults"`
		NextToken  string   `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	problem := ecsValidateStatusFilter(req.Status,
		[]string{ecsGatewayAssociationActive, ecsGatewayAssociationDisassociating, "INACTIVE"}, 3)
	if problem == "" {
		problem = ecsValidateMaxResults(req.MaxResults, 100)
	}
	if problem != "" {
		AWSError(w, "InvalidParameterException", problem, http.StatusBadRequest)
		return
	}
	gatewayArn, clusterArn := "", ""
	if req.Gateway != "" {
		gatewayArn = req.Gateway
		if gateway, ok := ecsResolveGateway(req.Gateway); ok {
			gatewayArn = gateway.GatewayArn
		}
	}
	if req.Cluster != "" {
		clusterArn = req.Cluster
		if !strings.HasPrefix(req.Cluster, "arn:") {
			clusterArn = ecsArn("cluster", req.Cluster)
		}
	}
	matched := ecsGatewayAssociations.Filter(func(association ECSGatewayAssociation) bool {
		return (gatewayArn == "" || association.GatewayArn == gatewayArn) &&
			(clusterArn == "" || association.ClusterArn == clusterArn) &&
			(len(req.Status) == 0 || slices.Contains(req.Status, association.Status))
	})
	page, next, ok := awsKeyPage(w, ecsBadToken, matched, func(a ECSGatewayAssociation) string {
		return ecsGatewayAssociationKey(a.GatewayArn, a.ClusterArn)
	}, req.NextToken, ecsMaxResults(req.MaxResults), 100)
	if !ok {
		return
	}
	out := map[string]any{"gatewayClusterAssociations": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleECSDescribeGatewayResources(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Gateway    string `json:"gateway"`
		MaxResults *int   `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Gateway == "" {
		AWSError(w, "InvalidParameterException", "gateway is required", http.StatusBadRequest)
		return
	}
	if problem := ecsValidateMaxResults(req.MaxResults, 20); problem != "" {
		AWSError(w, "InvalidParameterException", problem, http.StatusBadRequest)
		return
	}
	gateway, ok := ecsResolveGateway(req.Gateway)
	if !ok {
		ecsGatewayNotFound(w)
		return
	}
	loadBalancers := sortBy(slices.Clone(gateway.LoadBalancers), func(lb ECSGatewayLoadBalancer) string { return lb.LoadBalancerArn })
	page, next, err := listq.KeyPage(loadBalancers, func(lb ECSGatewayLoadBalancer) string { return lb.LoadBalancerArn },
		req.NextToken, ecsMaxResults(req.MaxResults), 10, 20)
	if err != nil {
		ecsBadToken(w, req.NextToken)
		return
	}
	routes := ecsGatewayRoutes.Filter(func(route ECSGatewayRoute) bool { return route.GatewayArn == gateway.GatewayArn })
	groups := make([]map[string]any, 0, len(page))
	for _, lb := range page {
		routeArns := []string{}
		for _, route := range routes {
			if route.LoadBalancerArn == lb.LoadBalancerArn {
				routeArns = append(routeArns, route.GatewayRouteArn)
			}
		}
		slices.Sort(routeArns)
		group := map[string]any{
			"loadBalancerArn":  lb.LoadBalancerArn,
			"gatewayRouteArns": routeArns,
		}
		if lb.ListenerArn != "" {
			group["listenerArn"] = lb.ListenerArn
		}
		if lb.SecurityGroupID != "" {
			group["securityGroup"] = lb.SecurityGroupID
		}
		groups = append(groups, group)
	}
	out := map[string]any{"gatewayResources": map[string]any{
		"gatewayArn": gateway.GatewayArn,
		"resources":  map[string]any{"hostSharedAlb": groups},
	}}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleECSDescribeGatewayRoute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GatewayRouteArn string `json:"gatewayRouteArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.GatewayRouteArn == "" {
		AWSError(w, "InvalidParameterException", "gatewayRouteArn is required", http.StatusBadRequest)
		return
	}
	route, ok := ecsGatewayRoutes.Get(req.GatewayRouteArn)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The gateway route %s was not found.", req.GatewayRouteArn)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"gatewayRoute": ecsGatewayRouteJSON(route)})
}

func handleECSListGatewayRoutes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Gateway                  string   `json:"gateway"`
		Cluster                  string   `json:"cluster"`
		Service                  string   `json:"service"`
		ServiceContainerPortName string   `json:"serviceContainerPortName"`
		Status                   []string `json:"status"`
		MaxResults               *int     `json:"maxResults"`
		NextToken                string   `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	problem := ecsValidateStatusFilter(req.Status, ecsGatewayRouteStatuses, 7)
	if problem == "" {
		problem = ecsValidateMaxResults(req.MaxResults, 100)
	}
	// "Filtering by a service name alone is not allowed, because service names
	// are unique only within a cluster."
	if problem == "" && req.Service != "" && !strings.HasPrefix(req.Service, "arn:") && req.Cluster == "" {
		problem = "Pass a full service ARN, or a service name together with its cluster."
	}
	if problem != "" {
		AWSError(w, "InvalidParameterException", problem, http.StatusBadRequest)
		return
	}
	gatewayArn := ""
	if req.Gateway != "" {
		gateway, ok := ecsResolveGateway(req.Gateway)
		if !ok {
			ecsGatewayNotFound(w)
			return
		}
		gatewayArn = gateway.GatewayArn
	}
	clusterArn := ""
	if req.Cluster != "" {
		clusterArn = req.Cluster
		if !strings.HasPrefix(req.Cluster, "arn:") {
			clusterArn = ecsArn("cluster", req.Cluster)
		}
	}
	serviceArn := ""
	if req.Service != "" {
		serviceArn = req.Service
		if !strings.HasPrefix(req.Service, "arn:") {
			serviceArn = ecsArn("service", ecsClusterNameFromRef(req.Cluster)+"/"+req.Service)
		}
	}
	matched := ecsGatewayRoutes.Filter(func(route ECSGatewayRoute) bool {
		return (gatewayArn == "" || route.GatewayArn == gatewayArn) &&
			(clusterArn == "" || route.ClusterArn == clusterArn) &&
			(serviceArn == "" || route.ServiceArn == serviceArn) &&
			(req.ServiceContainerPortName == "" || route.PortName == req.ServiceContainerPortName) &&
			(len(req.Status) == 0 || slices.Contains(req.Status, route.Status))
	})
	page, next, ok := awsKeyPage(w, ecsBadToken, matched, func(route ECSGatewayRoute) string { return route.GatewayRouteArn },
		req.NextToken, ecsMaxResults(req.MaxResults), 100)
	if !ok {
		return
	}
	summaries := make([]map[string]any, 0, len(page))
	for _, route := range page {
		summaries = append(summaries, map[string]any{
			"gatewayRouteArn":          route.GatewayRouteArn,
			"gatewayArn":               route.GatewayArn,
			"clusterArn":               route.ClusterArn,
			"serviceArn":               route.ServiceArn,
			"serviceContainerPortName": route.PortName,
			"status":                   route.Status,
		})
	}
	out := map[string]any{"gatewayRoutes": summaries}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}
