package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon ECS Express Mode: an Express service is a Fargate service behind an
// Application Load Balancer, a target group, an HTTPS listener rule, an AWS
// Certificate Manager certificate, security groups and an Application Auto
// Scaling target with a target-tracking policy, all of which Amazon ECS creates
// through those services' APIs as the service's infrastructure role. Up to 25
// Express services with the same network configuration share one load
// balancer, each reached through its own hostname on the shared HTTPS listener.
// What the role is refused is recorded on the managed resource as FAILED, the
// way DescribeServiceRevisions reports it in ecsManagedResources.

// expressGatewayContainer mirrors the SDK ExpressGatewayContainer document
// (camelCase wire keys). Unmodeled blocks ride RawMessage so they round-trip.
type expressGatewayContainer struct {
	Image                 string                        `json:"image,omitempty"`
	ContainerPort         *int32                        `json:"containerPort,omitempty"`
	Command               []string                      `json:"command,omitempty"`
	Environment           []ECSKeyValuePair             `json:"environment,omitempty"`
	Secrets               json.RawMessage               `json:"secrets,omitempty"`
	RepositoryCredentials *expressRepositoryCredentials `json:"repositoryCredentials,omitempty"`
	AwsLogsConfiguration  *expressAwsLogsConfiguration  `json:"awsLogsConfiguration,omitempty"`
}

type expressRepositoryCredentials struct {
	CredentialsParameter string `json:"credentialsParameter,omitempty"`
}

type expressAwsLogsConfiguration struct {
	LogGroup        string `json:"logGroup,omitempty"`
	LogStreamPrefix string `json:"logStreamPrefix,omitempty"`
}

type expressNetworkConfiguration struct {
	SecurityGroups []string `json:"securityGroups,omitempty"`
	Subnets        []string `json:"subnets,omitempty"`
}

type expressScalingTarget struct {
	AutoScalingMetric      string `json:"autoScalingMetric,omitempty"`
	AutoScalingTargetValue *int32 `json:"autoScalingTargetValue,omitempty"`
	MaxTaskCount           *int32 `json:"maxTaskCount,omitempty"`
	MinTaskCount           *int32 `json:"minTaskCount,omitempty"`
}

type expressIngressPath struct {
	AccessType string `json:"accessType"`
	Endpoint   string `json:"endpoint"`
}

// expressGatewayServiceConfiguration is the per-revision configuration the SDK
// reads back (an activeConfigurations[] entry, or the Update targetConfiguration).
type expressGatewayServiceConfiguration struct {
	Cpu                  string                       `json:"cpu,omitempty"`
	CreatedAt            float64                      `json:"createdAt"`
	ExecutionRoleArn     string                       `json:"executionRoleArn,omitempty"`
	HealthCheckPath      string                       `json:"healthCheckPath,omitempty"`
	IngressPaths         []expressIngressPath         `json:"ingressPaths,omitempty"`
	Memory               string                       `json:"memory,omitempty"`
	NetworkConfiguration *expressNetworkConfiguration `json:"networkConfiguration,omitempty"`
	PrimaryContainer     *expressGatewayContainer     `json:"primaryContainer,omitempty"`
	ScalingTarget        *expressScalingTarget        `json:"scalingTarget,omitempty"`
	ServiceRevisionArn   string                       `json:"serviceRevisionArn,omitempty"`
	TaskDefinitionArn    string                       `json:"taskDefinitionArn,omitempty"`
	TaskRoleArn          string                       `json:"taskRoleArn,omitempty"`
}

// expressGatewayServiceStatus is the {statusCode,statusReason} status object.
type expressGatewayServiceStatus struct {
	StatusCode   string `json:"statusCode"`
	StatusReason string `json:"statusReason,omitempty"`
}

const (
	expressStatusActive  = "ACTIVE"
	expressStatusDeleted = "DELETED"
	expressStatusFailed  = "FAILED"
)

// expressManagedResource is one resource Amazon ECS manages for an Express
// service, in the members every ECSManagedResources entry shares.
type expressManagedResource struct {
	Arn          string  `json:"arn,omitempty"`
	Status       string  `json:"status,omitempty"`
	StatusReason string  `json:"statusReason,omitempty"`
	UpdatedAt    float64 `json:"updatedAt,omitempty"`
}

func (r expressManagedResource) created() bool { return r.Status != "" }

func (r expressManagedResource) active() bool { return r.Status == expressStatusActive }

func expressResourceActive(arn string) expressManagedResource {
	return expressManagedResource{Arn: arn, Status: expressStatusActive, UpdatedAt: ecsEpochSeconds()}
}

func (r *expressManagedResource) fail(failure *ecsGatewayCallFailure) {
	r.Status = expressStatusFailed
	r.StatusReason = failure.Code + ": " + failure.Message
	r.UpdatedAt = ecsEpochSeconds()
}

func (r *expressManagedResource) deleted() {
	r.Status = expressStatusDeleted
	r.StatusReason = ""
	r.UpdatedAt = ecsEpochSeconds()
}

// ECSExpressLoadBalancer is a load balancer Express services share: the
// services whose network configuration is its NetworkKey are placed on it
// until it carries expressServicesPerLoadBalancer of them.
type ECSExpressLoadBalancer struct {
	LoadBalancerArn string                 `json:"loadBalancerArn"`
	DNSName         string                 `json:"dnsName"`
	Scheme          string                 `json:"scheme"`
	Subnets         []string               `json:"subnets"`
	NetworkKey      string                 `json:"networkKey"`
	LoadBalancer    expressManagedResource `json:"loadBalancer"`
	SecurityGroupID string                 `json:"securityGroupId"`
	SecurityGroup   expressManagedResource `json:"securityGroup"`
	ListenerArn     string                 `json:"listenerArn,omitempty"`
	Listener        expressManagedResource `json:"listener"`
}

const expressServicesPerLoadBalancer = 25

// ECSExpressService is the stored control-plane record for an Express service
// and the resources Amazon ECS manages for it.
type ECSExpressService struct {
	ServiceArn            string                               `json:"serviceArn"`
	ServiceName           string                               `json:"serviceName"`
	Cluster               string                               `json:"cluster"`
	InfrastructureRoleArn string                               `json:"infrastructureRoleArn"`
	CreatedAt             float64                              `json:"createdAt"`
	UpdatedAt             float64                              `json:"updatedAt"`
	Status                expressGatewayServiceStatus          `json:"status"`
	ActiveConfigurations  []expressGatewayServiceConfiguration `json:"activeConfigurations"`
	Tags                  []ECSTag                             `json:"tags,omitempty"`

	AccessType      string `json:"accessType"`
	Hostname        string `json:"hostname"`
	LoadBalancerArn string `json:"loadBalancerArn,omitempty"`
	// LoadBalancerFailure is the load balancer the role was refused, which no
	// shared record names.
	LoadBalancerFailure *expressManagedResource `json:"loadBalancerFailure,omitempty"`
	Certificate         expressManagedResource  `json:"certificate"`
	Rule                expressManagedResource  `json:"rule"`
	TargetGroup         expressManagedResource  `json:"targetGroup"`
	TargetGroupPort     int                     `json:"targetGroupPort,omitempty"`
	// ServiceSecurityGroup is the group Amazon ECS created for the tasks when
	// the caller supplied none.
	ServiceSecurityGroup       *expressManagedResource  `json:"serviceSecurityGroup,omitempty"`
	ServiceSecurityGroupID     string                   `json:"serviceSecurityGroupId,omitempty"`
	ServiceSecurityGroupRuleID string                   `json:"serviceSecurityGroupRuleId,omitempty"`
	EcsServiceKey              string                   `json:"ecsServiceKey"`
	ScalableTarget             expressManagedResource   `json:"scalableTarget"`
	MinCapacity                int                      `json:"minCapacity"`
	MaxCapacity                int                      `json:"maxCapacity"`
	ScalingPolicy              expressManagedResource   `json:"scalingPolicy"`
	ScalingPolicyName          string                   `json:"scalingPolicyName"`
	ScalingMetric              string                   `json:"scalingMetric"`
	ScalingTargetValue         int32                    `json:"scalingTargetValue"`
	MetricAlarms               []expressManagedResource `json:"metricAlarms,omitempty"`
}

var (
	ecsExpressServices      sim.Store[ECSExpressService]
	ecsExpressLoadBalancers sim.Store[ECSExpressLoadBalancer]
)

func registerECSExpress(r *AWSRouter, srv *sim.Server) {
	ecsExpressServices = sim.MakeStore[ECSExpressService](srv.DB(), "ecs_express_services")
	ecsExpressLoadBalancers = sim.MakeStore[ECSExpressLoadBalancer](srv.DB(), "ecs_express_load_balancers")

	r.Register("AmazonEC2ContainerServiceV20141113.CreateExpressGatewayService", handleECSCreateExpressGatewayService)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeExpressGatewayService", handleECSDescribeExpressGatewayService)
	r.Register("AmazonEC2ContainerServiceV20141113.UpdateExpressGatewayService", handleECSUpdateExpressGatewayService)
	r.Register("AmazonEC2ContainerServiceV20141113.DeleteExpressGatewayService", handleECSDeleteExpressGatewayService)
}

// ecsExpressArn builds the ARN that identifies an Express service:
// arn:aws:ecs:<region>:<account>:express-gateway-service/<cluster>/<name>.
func ecsExpressArn(cluster, name string) string {
	return ecsArn("express-gateway-service", cluster+"/"+name)
}

func expressClient(svc ECSExpressService) ecsGatewayClient {
	return ecsInfrastructureClient(svc.ServiceArn, svc.InfrastructureRoleArn)
}

func expressManagedTags() [][2]string {
	return [][2]string{{"AmazonECSManaged", "true"}}
}

func handleECSCreateExpressGatewayService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster               string                       `json:"cluster"`
		Cpu                   string                       `json:"cpu"`
		Memory                string                       `json:"memory"`
		ExecutionRoleArn      string                       `json:"executionRoleArn"`
		InfrastructureRoleArn string                       `json:"infrastructureRoleArn"`
		TaskRoleArn           string                       `json:"taskRoleArn"`
		HealthCheckPath       string                       `json:"healthCheckPath"`
		ServiceName           string                       `json:"serviceName"`
		NetworkConfiguration  *expressNetworkConfiguration `json:"networkConfiguration"`
		PrimaryContainer      *expressGatewayContainer     `json:"primaryContainer"`
		ScalingTarget         *expressScalingTarget        `json:"scalingTarget"`
		Tags                  []ECSTag                     `json:"tags"`
		TaskDefinitionArn     string                       `json:"taskDefinitionArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.InfrastructureRoleArn == "" {
		AWSError(w, "InvalidParameterException", "infrastructureRoleArn is required", http.StatusBadRequest)
		return
	}

	// taskDefinitionArn is mutually exclusive with the container/role/size knobs
	// the managed task definition derives.
	if req.TaskDefinitionArn != "" {
		if req.PrimaryContainer != nil || req.ExecutionRoleArn != "" || req.TaskRoleArn != "" || req.Cpu != "" || req.Memory != "" {
			AWSError(w, "InvalidParameterException",
				"taskDefinitionArn cannot be specified with primaryContainer, executionRoleArn, taskRoleArn, cpu, or memory",
				http.StatusBadRequest)
			return
		}
	}

	clusterName := ecsClusterNameFromRef(req.Cluster)
	cluster, ok := ecsClusters.Get(clusterName)
	if !ok {
		AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest,
			"Cluster not found: %s", clusterName)
		return
	}

	serviceName := req.ServiceName
	if serviceName == "" {
		serviceName = "express-" + sim.NewUUID()[:8]
	}
	serviceArn := ecsExpressArn(clusterName, serviceName)
	if existing, ok := ecsExpressServices.Get(serviceArn); ok && existing.Status.StatusCode != "INACTIVE" {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"An Express service named %q already exists in cluster %q.", serviceName, clusterName)
		return
	}
	if !ecsGatewayRoleAssumable(serviceArn, req.InfrastructureRoleArn) {
		AWSError(w, "InvalidParameterException", ecsGatewayRoleNotAssumable, http.StatusBadRequest)
		return
	}

	cpu := firstNonEmpty(req.Cpu, "256")
	memory := firstNonEmpty(req.Memory, "512")
	healthCheckPath := firstNonEmpty(req.HealthCheckPath, "/ping")

	now := ecsEpochSeconds()

	taskDefArn := req.TaskDefinitionArn
	container := req.PrimaryContainer
	containerPort := int32(80)
	if container != nil && container.ContainerPort != nil {
		containerPort = *container.ContainerPort
	}
	if taskDefArn == "" {
		taskDefArn = expressRegisterManagedTaskDef(serviceName, cpu, memory, req.ExecutionRoleArn, req.TaskRoleArn, container, containerPort)
	}

	// An Express service is PUBLIC: its load balancer is internet-facing in
	// the default VPC's subnets unless the request names others.
	netCfg := req.NetworkConfiguration
	if netCfg == nil {
		netCfg = &expressNetworkConfiguration{}
	}
	if len(netCfg.Subnets) == 0 {
		netCfg.Subnets = ecsDefaultVPCSubnets()
		if len(netCfg.Subnets) == 0 {
			AWSError(w, "InvalidParameterException",
				"No default VPC subnet is available for the Express service.", http.StatusBadRequest)
			return
		}
	}

	svc := ECSExpressService{
		ServiceArn:  serviceArn,
		ServiceName: serviceName,
		// Amazon ECS returns the cluster as its full ARN in the Express service
		// record; terraform-provider-aws parses the cluster name back out of it
		// (clusterNameFromARN splits on "/"), so a bare name would read back empty.
		Cluster:               cluster.ClusterArn,
		InfrastructureRoleArn: req.InfrastructureRoleArn,
		CreatedAt:             now,
		UpdatedAt:             now,
		Status:                expressGatewayServiceStatus{StatusCode: "ACTIVE"},
		Tags:                  req.Tags,
		AccessType:            "PUBLIC",
		Hostname:              ecsGatewayHostname(),
	}
	client := expressClient(svc)
	securityGroups := expressProvisionIngress(client, &svc, netCfg.Subnets, netCfg.SecurityGroups, int(containerPort), healthCheckPath)

	scaling := expressManagedScalingTarget(req.ScalingTarget)
	svc.EcsServiceKey = expressCreateFargateService(clusterName, cluster.ClusterArn, serviceName, taskDefArn,
		int(*scaling.MinTaskCount), netCfg.Subnets, securityGroups, svc.TargetGroup, int(containerPort))
	backing, _ := ecsServices.Get(svc.EcsServiceKey)
	ecsBeginServiceDeployment(svc.EcsServiceKey, ECSService{}, backing)
	revisionArn := ecsRecordServiceDeployment(backing, "")
	ecsRequestServiceReconcile(svc.EcsServiceKey)

	expressProvisionAutoScaling(client, &svc, scaling)

	svc.ActiveConfigurations = []expressGatewayServiceConfiguration{{
		Cpu:                  cpu,
		CreatedAt:            now,
		ExecutionRoleArn:     req.ExecutionRoleArn,
		HealthCheckPath:      healthCheckPath,
		IngressPaths:         expressIngressPaths(svc),
		Memory:               memory,
		NetworkConfiguration: &expressNetworkConfiguration{SecurityGroups: securityGroups, Subnets: netCfg.Subnets},
		PrimaryContainer:     container,
		ScalingTarget:        scaling,
		ServiceRevisionArn:   revisionArn,
		TaskDefinitionArn:    taskDefArn,
		TaskRoleArn:          req.TaskRoleArn,
	}}
	svc.Status.StatusReason = expressFailureReason(svc)
	ecsExpressServices.Put(serviceArn, svc)

	sim.WriteJSON(w, http.StatusOK, map[string]any{"service": expressServiceJSON(svc)})
}

func handleECSDescribeExpressGatewayService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceArn string   `json:"serviceArn"`
		Include    []string `json:"include"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceArn == "" {
		AWSError(w, "InvalidParameterException", "serviceArn is required", http.StatusBadRequest)
		return
	}
	svc, ok := ecsExpressServices.Get(req.ServiceArn)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Express service not found: %s", req.ServiceArn)
		return
	}
	out := expressServiceJSON(svc)
	includeTags := false
	for _, inc := range req.Include {
		if inc == "TAGS" {
			includeTags = true
		}
	}
	if !includeTags {
		delete(out, "tags")
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"service": out})
}

func handleECSUpdateExpressGatewayService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceArn           string                       `json:"serviceArn"`
		Cpu                  string                       `json:"cpu"`
		Memory               string                       `json:"memory"`
		ExecutionRoleArn     string                       `json:"executionRoleArn"`
		TaskRoleArn          string                       `json:"taskRoleArn"`
		HealthCheckPath      string                       `json:"healthCheckPath"`
		NetworkConfiguration *expressNetworkConfiguration `json:"networkConfiguration"`
		PrimaryContainer     *expressGatewayContainer     `json:"primaryContainer"`
		ScalingTarget        *expressScalingTarget        `json:"scalingTarget"`
		TaskDefinitionArn    string                       `json:"taskDefinitionArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceArn == "" {
		AWSError(w, "InvalidParameterException", "serviceArn is required", http.StatusBadRequest)
		return
	}
	if req.TaskDefinitionArn != "" {
		if req.PrimaryContainer != nil || req.ExecutionRoleArn != "" || req.TaskRoleArn != "" || req.Cpu != "" || req.Memory != "" {
			AWSError(w, "InvalidParameterException",
				"taskDefinitionArn cannot be specified with primaryContainer, executionRoleArn, taskRoleArn, cpu, or memory",
				http.StatusBadRequest)
			return
		}
	}
	svc, ok := ecsExpressServices.Get(req.ServiceArn)
	if !ok {
		AWSErrorf(w, "ServiceNotFoundException", http.StatusBadRequest,
			"Express service not found: %s", req.ServiceArn)
		return
	}
	if svc.Status.StatusCode != "ACTIVE" {
		AWSErrorf(w, "ServiceNotActiveException", http.StatusBadRequest,
			"Express service %s is not active", req.ServiceArn)
		return
	}
	if req.TaskDefinitionArn != "" {
		if _, ok := ecsServiceTaskDefinition(req.TaskDefinitionArn); !ok {
			AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
				"Unable to describe task definition: %s", req.TaskDefinitionArn)
			return
		}
	}
	client := expressClient(svc)

	// New revision derived from the current one with the mutable fields applied.
	cur := svc.ActiveConfigurations[len(svc.ActiveConfigurations)-1]
	now := ecsEpochSeconds()
	next := cur
	next.CreatedAt = now
	if req.Cpu != "" {
		next.Cpu = req.Cpu
	}
	if req.Memory != "" {
		next.Memory = req.Memory
	}
	if req.ExecutionRoleArn != "" {
		next.ExecutionRoleArn = req.ExecutionRoleArn
	}
	if req.TaskRoleArn != "" {
		next.TaskRoleArn = req.TaskRoleArn
	}
	if req.HealthCheckPath != "" {
		next.HealthCheckPath = req.HealthCheckPath
	}
	if req.NetworkConfiguration != nil {
		next.NetworkConfiguration = req.NetworkConfiguration
	}
	if req.PrimaryContainer != nil {
		next.PrimaryContainer = req.PrimaryContainer
	}
	if req.ScalingTarget != nil {
		next.ScalingTarget = expressNormalizeScalingTarget(req.ScalingTarget)
		expressUpdateAutoScaling(client, &svc, req.ScalingTarget)
	}
	port := 80
	if next.PrimaryContainer != nil && next.PrimaryContainer.ContainerPort != nil {
		port = int(*next.PrimaryContainer.ContainerPort)
	}
	if req.TaskDefinitionArn != "" {
		next.TaskDefinitionArn = req.TaskDefinitionArn
	} else if req.Cpu != "" || req.Memory != "" || req.ExecutionRoleArn != "" ||
		req.TaskRoleArn != "" || req.PrimaryContainer != nil {
		next.TaskDefinitionArn = expressRegisterManagedTaskDef(
			svc.ServiceName,
			next.Cpu,
			next.Memory,
			next.ExecutionRoleArn,
			next.TaskRoleArn,
			next.PrimaryContainer,
			int32(port),
		)
	}
	expressUpdateTargetGroup(client, &svc, port, next.HealthCheckPath)

	// Roll the backing Fargate service to the (possibly new) task definition.
	if ecsKey := svc.EcsServiceKey; ecsKey != "" {
		ecsServices.Update(ecsKey, func(es *ECSService) {
			es.TaskDefinition = next.TaskDefinitionArn
			if next.ScalingTarget != nil {
				if next.ScalingTarget.MinTaskCount != nil &&
					es.DesiredCount < int(*next.ScalingTarget.MinTaskCount) {
					es.DesiredCount = int(*next.ScalingTarget.MinTaskCount)
				}
				if next.ScalingTarget.MaxTaskCount != nil &&
					es.DesiredCount > int(*next.ScalingTarget.MaxTaskCount) {
					es.DesiredCount = int(*next.ScalingTarget.MaxTaskCount)
				}
			}
			if next.NetworkConfiguration != nil {
				es.NetworkConfiguration = expressAwsvpcConfiguration(next.NetworkConfiguration.Subnets, next.NetworkConfiguration.SecurityGroups)
			}
			es.LoadBalancers = expressServiceLoadBalancers(svc.TargetGroup, port)
			es.Deployments = []ECSDeployment{ecsServiceDeployment(*es, now)}
		})
		if backing, ok := ecsServices.Get(ecsKey); ok {
			next.ServiceRevisionArn = ecsRecordServiceDeployment(backing, "")
		}
		ecsRequestServiceReconcile(ecsKey)
	}

	svc.ActiveConfigurations = append(svc.ActiveConfigurations, next)
	svc.UpdatedAt = now
	svc.Status.StatusReason = expressFailureReason(svc)
	ecsExpressServices.Put(svc.ServiceArn, svc)

	updated := map[string]any{
		"cluster":             svc.Cluster,
		"createdAt":           svc.CreatedAt,
		"serviceArn":          svc.ServiceArn,
		"serviceName":         svc.ServiceName,
		"status":              svc.Status,
		"targetConfiguration": expressConfigJSON(next),
		"updatedAt":           svc.UpdatedAt,
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"service": updated})
}

func handleECSDeleteExpressGatewayService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceArn string `json:"serviceArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ServiceArn == "" {
		AWSError(w, "InvalidParameterException", "serviceArn is required", http.StatusBadRequest)
		return
	}
	svc, ok := ecsExpressServices.Get(req.ServiceArn)
	if !ok {
		AWSErrorf(w, "ServiceNotFoundException", http.StatusBadRequest,
			"Express service not found: %s", req.ServiceArn)
		return
	}
	if svc.Status.StatusCode != "ACTIVE" {
		AWSErrorf(w, "ServiceNotActiveException", http.StatusBadRequest,
			"Express service %s is not active", req.ServiceArn)
		return
	}

	// Amazon ECS answers the delete with the service DRAINING and converges it
	// to INACTIVE once its tasks have stopped and its resources are removed.
	// The teardown here runs before the response, so the next Describe (the
	// provider's delete waiter polls until INACTIVE) sees it complete, while
	// a resource the role is refused keeps the service DRAINING and says why.
	svc.Status = expressGatewayServiceStatus{StatusCode: "DRAINING", StatusReason: "Service is being deleted"}
	svc.UpdatedAt = ecsEpochSeconds()
	ecsExpressServices.Put(svc.ServiceArn, svc)
	resp := svc

	failure := expressTeardown(expressClient(svc), &svc)
	svc.UpdatedAt = ecsEpochSeconds()
	if failure == nil {
		svc.Status = expressGatewayServiceStatus{StatusCode: "INACTIVE", StatusReason: "Service has been deleted"}
	} else {
		svc.Status.StatusReason = failure.Error()
		resp = svc
	}
	ecsExpressServices.Put(svc.ServiceArn, svc)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"service": expressServiceJSON(resp)})
}

func expressServiceJSON(svc ECSExpressService) map[string]any {
	configs := make([]map[string]any, 0, len(svc.ActiveConfigurations))
	for _, c := range svc.ActiveConfigurations {
		configs = append(configs, expressConfigJSON(c))
	}
	out := map[string]any{
		"serviceArn":            svc.ServiceArn,
		"serviceName":           svc.ServiceName,
		"cluster":               svc.Cluster,
		"infrastructureRoleArn": svc.InfrastructureRoleArn,
		"createdAt":             svc.CreatedAt,
		"updatedAt":             svc.UpdatedAt,
		"status":                svc.Status,
		"activeConfigurations":  configs,
	}
	tags := svc.Tags
	if tags == nil {
		tags = []ECSTag{}
	}
	out["tags"] = tags
	return out
}

func expressConfigJSON(c expressGatewayServiceConfiguration) map[string]any {
	m := map[string]any{
		"cpu":                c.Cpu,
		"createdAt":          c.CreatedAt,
		"healthCheckPath":    c.HealthCheckPath,
		"memory":             c.Memory,
		"serviceRevisionArn": c.ServiceRevisionArn,
		"taskDefinitionArn":  c.TaskDefinitionArn,
	}
	if c.ExecutionRoleArn != "" {
		m["executionRoleArn"] = c.ExecutionRoleArn
	}
	if c.TaskRoleArn != "" {
		m["taskRoleArn"] = c.TaskRoleArn
	}
	if len(c.IngressPaths) > 0 {
		m["ingressPaths"] = c.IngressPaths
	}
	if c.NetworkConfiguration != nil {
		m["networkConfiguration"] = c.NetworkConfiguration
	}
	if c.PrimaryContainer != nil {
		m["primaryContainer"] = c.PrimaryContainer
	}
	if c.ScalingTarget != nil {
		m["scalingTarget"] = c.ScalingTarget
	}
	return m
}

// expressIngressPaths is the service's entry point once its load balancer
// carries its hostname.
func expressIngressPaths(svc ECSExpressService) []expressIngressPath {
	if svc.LoadBalancerArn == "" {
		return nil
	}
	return []expressIngressPath{{AccessType: svc.AccessType, Endpoint: "https://" + svc.Hostname}}
}

// expressFailureReason names the first managed resource the role was refused,
// which the service's statusReason carries.
func expressFailureReason(svc ECSExpressService) string {
	type named struct {
		name     string
		resource *expressManagedResource
	}
	resources := []named{
		{"load balancer", svc.LoadBalancerFailure},
		{"certificate", &svc.Certificate},
		{"target group", &svc.TargetGroup},
		{"listener rule", &svc.Rule},
		{"service security group", svc.ServiceSecurityGroup},
		{"scalable target", &svc.ScalableTarget},
		{"scaling policy", &svc.ScalingPolicy},
	}
	if lb, ok := ecsExpressLoadBalancers.Get(svc.LoadBalancerArn); ok {
		resources = append(resources, named{"listener", &lb.Listener})
	}
	for _, entry := range resources {
		if entry.resource != nil && entry.resource.Status == expressStatusFailed {
			return "The " + entry.name + " could not be provisioned: " + entry.resource.StatusReason
		}
	}
	return ""
}

func expressNormalizeScalingTarget(in *expressScalingTarget) *expressScalingTarget {
	if in == nil {
		return nil
	}
	out := *in
	if out.AutoScalingTargetValue == nil {
		v := int32(60)
		out.AutoScalingTargetValue = &v
	}
	if out.AutoScalingMetric == "" {
		out.AutoScalingMetric = "AVERAGE_CPU"
	}
	return &out
}

// expressManagedScalingTarget returns the auto-scaling configuration ECS reports
// for the managed scalable target it always provisions. When the caller omitted
// scalingTarget, ECS applies the documented defaults (metric AVERAGE_CPU, target
// value 60, min/max task count 1), so the read-back configuration is never nil.
func expressManagedScalingTarget(in *expressScalingTarget) *expressScalingTarget {
	st := in
	if st == nil {
		st = &expressScalingTarget{}
	}
	out := *expressNormalizeScalingTarget(st)
	if out.MinTaskCount == nil {
		v := int32(1)
		out.MinTaskCount = &v
	}
	if out.MaxTaskCount == nil {
		v := int32(1)
		out.MaxTaskCount = &v
	}
	return &out
}

// expressRegisterManagedTaskDef registers a Fargate task definition with a
// single container named `Main` (the Express contract) into ecsTaskDefinitions,
// the same store RegisterTaskDefinition uses, and returns its ARN.
func expressRegisterManagedTaskDef(serviceName, cpu, memory, execRole, taskRole string, container *expressGatewayContainer, port int32) string {
	family := "express-" + serviceName

	main := ECSContainerDefinition{
		Name:         "Main",
		Essential:    expressBoolPtr(true),
		PortMappings: []ECSPortMapping{{ContainerPort: int(port), Protocol: "tcp"}},
	}
	if container != nil {
		main.Image = container.Image
		main.Command = container.Command
		main.Environment = container.Environment
	}

	ecsRevisionMu.Lock()
	ecsRevisions[family]++
	revision := ecsRevisions[family]
	ecsRevisionMu.Unlock()

	td := ECSTaskDefinition{
		TaskDefinitionArn:       fmt.Sprintf("arn:aws:ecs:"+awsRegion()+":"+awsAccountID()+":task-definition/%s:%d", family, revision),
		Family:                  family,
		Revision:                revision,
		ContainerDefinitions:    []ECSContainerDefinition{main},
		Cpu:                     cpu,
		Memory:                  memory,
		NetworkMode:             "awsvpc",
		RequiresCompatibilities: []string{"FARGATE"},
		ExecutionRoleArn:        execRole,
		TaskRoleArn:             taskRole,
		Compatibilities:         ecsComputeCompatibilities("awsvpc", []string{"FARGATE"}),
		Status:                  "ACTIVE",
	}
	ecsTaskDefinitions.Put(fmt.Sprintf("%s:%d", family, revision), td)
	return td.TaskDefinitionArn
}

func expressAwsvpcConfiguration(subnets, securityGroups []string) json.RawMessage {
	netJSON, _ := json.Marshal(map[string]any{
		"awsvpcConfiguration": map[string]any{
			"subnets":        subnets,
			"securityGroups": securityGroups,
			"assignPublicIp": "ENABLED",
		},
	})
	return netJSON
}

// expressServiceLoadBalancers binds the service's Main container to its target
// group; a service whose target group was refused has no binding.
func expressServiceLoadBalancers(targetGroup expressManagedResource, port int) json.RawMessage {
	if !targetGroup.active() {
		return nil
	}
	lbJSON, _ := json.Marshal([]map[string]any{{
		"targetGroupArn": targetGroup.Arn,
		"containerName":  "Main",
		"containerPort":  port,
	}})
	return lbJSON
}

// expressCreateFargateService records the backing Fargate service in
// ecsServices and returns its store key.
func expressCreateFargateService(clusterName, clusterArn, serviceName, taskDefArn string, desired int, subnets, securityGroups []string, targetGroup expressManagedResource, port int) string {
	now := ecsEpochSeconds()
	svc := ECSService{
		ServiceArn:           ecsArn("service", clusterName+"/"+serviceName),
		ServiceName:          serviceName,
		ClusterArn:           clusterArn,
		TaskDefinition:       taskDefArn,
		DesiredCount:         desired,
		RunningCount:         0,
		Status:               "ACTIVE",
		LaunchType:           "FARGATE",
		SchedulingStrategy:   "REPLICA",
		CreatedAt:            now,
		NetworkConfiguration: expressAwsvpcConfiguration(subnets, securityGroups),
		LoadBalancers:        expressServiceLoadBalancers(targetGroup, port),
	}
	svc.Deployments = []ECSDeployment{ecsServiceDeployment(svc, now)}
	key := ecsServiceKey(clusterName, serviceName)
	ecsServices.Put(key, svc)
	return key
}

// expressNetworkKey is what consolidated services share: the load balancer's
// scheme and subnets and the tasks' security groups.
func expressNetworkKey(scheme string, subnets, securityGroups []string) string {
	return scheme + "|" + joinSorted(subnets) + "|" + joinSorted(securityGroups)
}

// expressServicesOnLoadBalancer is every service, other than the one named,
// that a shared load balancer carries.
func expressServicesOnLoadBalancer(lbArn, exceptServiceArn string) []ECSExpressService {
	return ecsExpressServices.Filter(func(other ECSExpressService) bool {
		return other.LoadBalancerArn == lbArn && other.ServiceArn != exceptServiceArn && other.Status.StatusCode != "INACTIVE"
	})
}

// expressProvisionIngress creates what fronts the service, as the
// infrastructure role, and returns the security groups its tasks run in. A
// refusal is recorded on the resource refused; the resources that need it are
// not attempted.
// expressLoadBalancerScheme is the scheme of every Express service's load
// balancer, since an Express service is PUBLIC.
const expressLoadBalancerScheme = "internet-facing"

func expressProvisionIngress(client ecsGatewayClient, svc *ECSExpressService, subnets, callerSecurityGroups []string, port int, healthCheckPath string) []string {
	vpc, _ := ecsSubnetsPlacement(subnets)
	lb, ok := expressEnsureLoadBalancer(client, svc, expressLoadBalancerScheme, subnets, callerSecurityGroups, vpc)
	securityGroups := callerSecurityGroups
	if len(securityGroups) == 0 && ok {
		securityGroups = expressCreateServiceSecurityGroup(client, svc, vpc, lb.SecurityGroupID, port)
	}
	if !ok {
		return securityGroups
	}
	certArn, failure := ecsIssueManagedCertificate(client, svc.Hostname, expressManagedTags())
	if failure != nil {
		svc.Certificate.fail(failure)
		return securityGroups
	}
	svc.Certificate = expressResourceActive(certArn)
	if failure := expressCreateTargetGroup(client, svc, vpc, port, healthCheckPath); failure != nil {
		return securityGroups
	}
	if lb.ListenerArn == "" {
		form := url.Values{
			"LoadBalancerArn":                      {lb.LoadBalancerArn},
			"Protocol":                             {"HTTPS"},
			"Port":                                 {"443"},
			"Certificates.member.1.CertificateArn": {certArn},
			"DefaultActions.member.1.Type":         {"fixed-response"},
			"DefaultActions.member.1.FixedResponseConfig.StatusCode":  {"404"},
			"DefaultActions.member.1.FixedResponseConfig.ContentType": {"text/plain"},
		}
		ecsGatewayELBTags(form, expressManagedTags())
		body, failure := client.elb("CreateListener", form)
		if failure != nil {
			lb.Listener.fail(failure)
			ecsExpressLoadBalancers.Put(lb.LoadBalancerArn, lb)
			return securityGroups
		}
		lb.ListenerArn = awsXMLText(body, "ListenerArn")
		lb.Listener = expressResourceActive(lb.ListenerArn)
		ecsExpressLoadBalancers.Put(lb.LoadBalancerArn, lb)
	} else if _, failure := client.elb("AddListenerCertificates", url.Values{
		"ListenerArn":                          {lb.ListenerArn},
		"Certificates.member.1.CertificateArn": {certArn},
	}); failure != nil {
		svc.Certificate.fail(failure)
		return securityGroups
	}
	form := url.Values{
		"ListenerArn":               {lb.ListenerArn},
		"Priority":                  {strconv.Itoa(expressFreeRulePriority(*svc))},
		"Conditions.member.1.Field": {"host-header"},
		"Conditions.member.1.HostHeaderConfig.Values.member.1": {svc.Hostname},
		"Actions.member.1.Type":                                {"forward"},
		"Actions.member.1.TargetGroupArn":                      {svc.TargetGroup.Arn},
	}
	ecsGatewayELBTags(form, expressManagedTags())
	body, failure := client.elb("CreateRule", form)
	if failure != nil {
		svc.Rule.fail(failure)
		return securityGroups
	}
	svc.Rule = expressResourceActive(awsXMLText(body, "RuleArn"))
	return securityGroups
}

// expressFreeRulePriority is the lowest listener rule priority no other
// service on the load balancer holds.
func expressFreeRulePriority(svc ECSExpressService) int {
	used := map[int]bool{}
	for _, other := range expressServicesOnLoadBalancer(svc.LoadBalancerArn, svc.ServiceArn) {
		if other.Rule.active() {
			used[other.rulePriority()] = true
		}
	}
	priority := 1
	for used[priority] {
		priority++
	}
	return priority
}

// rulePriority reads a rule's priority back from Elastic Load Balancing, which
// is where it lives.
func (svc ECSExpressService) rulePriority() int {
	if rule, ok := elbv2Rules.Get(svc.Rule.Arn); ok {
		priority, _ := strconv.Atoi(rule.Priority)
		return priority
	}
	return 0
}

// expressEnsureLoadBalancer places the service on a shared load balancer with
// its network configuration and room for it, or creates one with its security
// group. It reports false when the service has no load balancer.
func expressEnsureLoadBalancer(client ecsGatewayClient, svc *ECSExpressService, scheme string, subnets, callerSecurityGroups []string, vpc string) (ECSExpressLoadBalancer, bool) {
	key := expressNetworkKey(scheme, subnets, callerSecurityGroups)
	shared := ecsExpressLoadBalancers.Filter(func(lb ECSExpressLoadBalancer) bool {
		return lb.NetworkKey == key && lb.LoadBalancer.active() &&
			len(expressServicesOnLoadBalancer(lb.LoadBalancerArn, svc.ServiceArn)) < expressServicesPerLoadBalancer
	})
	sort.Slice(shared, func(i, j int) bool { return shared[i].LoadBalancer.UpdatedAt < shared[j].LoadBalancer.UpdatedAt })
	if len(shared) > 0 {
		svc.LoadBalancerArn = shared[0].LoadBalancerArn
		return shared[0], true
	}
	tags := expressManagedTags()
	groupForm := url.Values{
		"GroupName":                       {ecsGatewayRandomName()},
		"GroupDescription":                {"Managed by Amazon ECS for Express service " + svc.ServiceName},
		"VpcId":                           {vpc},
		"TagSpecification.1.ResourceType": {"security-group"},
	}
	for i, tag := range tags {
		groupForm.Set(fmt.Sprintf("TagSpecification.1.Tag.%d.Key", i+1), tag[0])
		groupForm.Set(fmt.Sprintf("TagSpecification.1.Tag.%d.Value", i+1), tag[1])
	}
	lb := ECSExpressLoadBalancer{Scheme: scheme, Subnets: subnets, NetworkKey: key}
	body, failure := client.ec2("CreateSecurityGroup", groupForm)
	if failure != nil {
		lb.LoadBalancer.fail(failure)
		svc.LoadBalancerFailure = &lb.LoadBalancer
		return lb, false
	}
	lb.SecurityGroupID = awsXMLText(body, "groupId")
	lb.SecurityGroup = expressResourceActive(ecsGatewaySecurityGroupArn(lb.SecurityGroupID))
	if _, failure := ecsGatewayAuthorizeHTTPS(client, lb.SecurityGroupID, "ipv4"); failure != nil {
		lb.LoadBalancer.fail(ecsGatewayAbandonSecurityGroup(client, lb.SecurityGroupID, failure))
		svc.LoadBalancerFailure = &lb.LoadBalancer
		return lb, false
	}
	lbForm := url.Values{
		"Name":                    {ecsGatewayRandomName()},
		"Type":                    {"application"},
		"Scheme":                  {scheme},
		"IpAddressType":           {"ipv4"},
		"SecurityGroups.member.1": {lb.SecurityGroupID},
	}
	for i, subnet := range subnets {
		lbForm.Set(fmt.Sprintf("Subnets.member.%d", i+1), subnet)
	}
	ecsGatewayELBTags(lbForm, tags)
	body, failure = client.elb("CreateLoadBalancer", lbForm)
	if failure != nil {
		lb.LoadBalancer.fail(ecsGatewayAbandonSecurityGroup(client, lb.SecurityGroupID, failure))
		svc.LoadBalancerFailure = &lb.LoadBalancer
		return lb, false
	}
	lb.LoadBalancerArn = awsXMLText(body, "LoadBalancerArn")
	lb.DNSName = awsXMLText(body, "DNSName")
	lb.LoadBalancer = expressResourceActive(lb.LoadBalancerArn)
	ecsExpressLoadBalancers.Put(lb.LoadBalancerArn, lb)
	svc.LoadBalancerArn = lb.LoadBalancerArn
	return lb, true
}

// expressCreateServiceSecurityGroup creates the tasks' security group, which
// admits the container port from the load balancer's group.
func expressCreateServiceSecurityGroup(client ecsGatewayClient, svc *ECSExpressService, vpc, loadBalancerGroupID string, port int) []string {
	form := url.Values{
		"GroupName":                       {ecsGatewayRandomName()},
		"GroupDescription":                {"Managed by Amazon ECS for the tasks of Express service " + svc.ServiceName},
		"VpcId":                           {vpc},
		"TagSpecification.1.ResourceType": {"security-group"},
	}
	for i, tag := range expressManagedTags() {
		form.Set(fmt.Sprintf("TagSpecification.1.Tag.%d.Key", i+1), tag[0])
		form.Set(fmt.Sprintf("TagSpecification.1.Tag.%d.Value", i+1), tag[1])
	}
	svc.ServiceSecurityGroup = &expressManagedResource{}
	body, failure := client.ec2("CreateSecurityGroup", form)
	if failure != nil {
		svc.ServiceSecurityGroup.fail(failure)
		return nil
	}
	svc.ServiceSecurityGroupID = awsXMLText(body, "groupId")
	*svc.ServiceSecurityGroup = expressResourceActive(ecsGatewaySecurityGroupArn(svc.ServiceSecurityGroupID))
	if failure := expressAdmitLoadBalancer(client, svc, loadBalancerGroupID, port); failure != nil {
		svc.ServiceSecurityGroup.fail(failure)
	}
	return []string{svc.ServiceSecurityGroupID}
}

// expressAdmitLoadBalancer lets the load balancer's security group reach the
// container port in the tasks' group.
func expressAdmitLoadBalancer(client ecsGatewayClient, svc *ECSExpressService, loadBalancerGroupID string, port int) *ecsGatewayCallFailure {
	body, failure := client.ec2("AuthorizeSecurityGroupIngress", url.Values{
		"GroupId":                              {svc.ServiceSecurityGroupID},
		"IpPermissions.1.IpProtocol":           {"tcp"},
		"IpPermissions.1.FromPort":             {strconv.Itoa(port)},
		"IpPermissions.1.ToPort":               {strconv.Itoa(port)},
		"IpPermissions.1.Groups.1.GroupId":     {loadBalancerGroupID},
		"IpPermissions.1.Groups.1.Description": {"Amazon ECS Express service " + svc.ServiceArn},
	})
	if failure != nil {
		return failure
	}
	svc.ServiceSecurityGroupRuleID = awsXMLText(body, "securityGroupRuleId")
	return nil
}

// expressCreateTargetGroup creates the service's target group for its
// container port and health check path.
func expressCreateTargetGroup(client ecsGatewayClient, svc *ECSExpressService, vpc string, port int, healthCheckPath string) *ecsGatewayCallFailure {
	form := url.Values{
		"Name":                       {ecsGatewayRandomName()},
		"Protocol":                   {"HTTP"},
		"Port":                       {strconv.Itoa(port)},
		"VpcId":                      {vpc},
		"TargetType":                 {"ip"},
		"HealthCheckProtocol":        {"HTTP"},
		"HealthCheckPath":            {healthCheckPath},
		"HealthCheckIntervalSeconds": {"30"},
		"HealthCheckTimeoutSeconds":  {"5"},
		"Matcher.HttpCode":           {"200"},
	}
	ecsGatewayELBTags(form, expressManagedTags())
	svc.TargetGroupPort = port
	body, failure := client.elb("CreateTargetGroup", form)
	if failure != nil {
		svc.TargetGroup.fail(failure)
		return failure
	}
	svc.TargetGroup = expressResourceActive(awsXMLText(body, "TargetGroupArn"))
	return nil
}

// expressUpdateTargetGroup applies a new health check path to the target
// group, and a new container port by replacing it: a target group's port is
// fixed at creation. The tasks' group admits the new port instead of the old.
func expressUpdateTargetGroup(client ecsGatewayClient, svc *ECSExpressService, port int, healthCheckPath string) {
	if !svc.TargetGroup.active() {
		return
	}
	current := svc.ActiveConfigurations[len(svc.ActiveConfigurations)-1]
	if port == svc.TargetGroupPort {
		if healthCheckPath != current.HealthCheckPath {
			if _, failure := client.elb("ModifyTargetGroup", url.Values{
				"TargetGroupArn":  {svc.TargetGroup.Arn},
				"HealthCheckPath": {healthCheckPath},
			}); failure != nil {
				svc.TargetGroup.fail(failure)
				return
			}
			svc.TargetGroup.UpdatedAt = ecsEpochSeconds()
		}
		return
	}
	previous := svc.TargetGroup
	vpc, _ := ecsSubnetsPlacement(current.NetworkConfiguration.Subnets)
	if failure := expressCreateTargetGroup(client, svc, vpc, port, healthCheckPath); failure != nil {
		return
	}
	if svc.Rule.active() {
		if _, failure := client.elb("ModifyRule", url.Values{
			"RuleArn":                         {svc.Rule.Arn},
			"Actions.member.1.Type":           {"forward"},
			"Actions.member.1.TargetGroupArn": {svc.TargetGroup.Arn},
		}); failure != nil {
			svc.Rule.fail(failure)
			return
		}
		svc.Rule.UpdatedAt = ecsEpochSeconds()
	}
	if _, failure := client.elb("DeleteTargetGroup", url.Values{"TargetGroupArn": {previous.Arn}}); failure != nil {
		svc.TargetGroup.fail(failure)
		return
	}
	if svc.ServiceSecurityGroup != nil && svc.ServiceSecurityGroup.active() {
		lb, _ := ecsExpressLoadBalancers.Get(svc.LoadBalancerArn)
		if _, failure := client.ec2("RevokeSecurityGroupIngress", url.Values{
			"GroupId":               {svc.ServiceSecurityGroupID},
			"SecurityGroupRuleId.1": {svc.ServiceSecurityGroupRuleID},
		}); failure != nil {
			svc.ServiceSecurityGroup.fail(failure)
			return
		}
		if failure := expressAdmitLoadBalancer(client, svc, lb.SecurityGroupID, port); failure != nil {
			svc.ServiceSecurityGroup.fail(failure)
			return
		}
		svc.ServiceSecurityGroup.UpdatedAt = ecsEpochSeconds()
	}
}

// expressMetricToPredefined maps the Express autoScalingMetric enum to the
// Application Auto Scaling ECS predefined-metric type.
func expressMetricToPredefined(metric string) string {
	switch metric {
	case "AVERAGE_MEMORY":
		return "ECSServiceAverageMemoryUtilization"
	case "REQUEST_COUNT_PER_TARGET":
		return "ALBRequestCountPerTarget"
	default:
		return "ECSServiceAverageCPUUtilization"
	}
}

const (
	expressScalingNamespace = "ecs"
	expressScalingDimension = "ecs:service:DesiredCount"
)

func expressScalingResourceID(svc ECSExpressService) string {
	return "service/" + ecsClusterNameFromRef(svc.Cluster) + "/" + svc.ServiceName
}

// expressProvisionAutoScaling registers the service's scalable target and
// puts its target-tracking policy, as the infrastructure role.
func expressProvisionAutoScaling(client ecsGatewayClient, svc *ECSExpressService, scaling *expressScalingTarget) {
	svc.ScalingPolicyName = "express-" + svc.ServiceName
	expressApplyAutoScaling(client, svc, scaling)
}

// expressUpdateAutoScaling re-applies the scaling configuration on update.
func expressUpdateAutoScaling(client ecsGatewayClient, svc *ECSExpressService, scaling *expressScalingTarget) {
	applied := expressNormalizeScalingTarget(scaling)
	if applied.MinTaskCount == nil {
		floor := int32(svc.MinCapacity)
		applied.MinTaskCount = &floor
	}
	if applied.MaxTaskCount == nil {
		ceiling := int32(svc.MaxCapacity)
		applied.MaxTaskCount = &ceiling
	}
	expressApplyAutoScaling(client, svc, applied)
}

func expressApplyAutoScaling(client ecsGatewayClient, svc *ECSExpressService, scaling *expressScalingTarget) {
	resourceID := expressScalingResourceID(*svc)
	body, failure := client.json("application-autoscaling", "application-autoscaling",
		"AnyScaleFrontendService.RegisterScalableTarget", "RegisterScalableTarget", map[string]any{
			"ServiceNamespace":  expressScalingNamespace,
			"ResourceId":        resourceID,
			"ScalableDimension": expressScalingDimension,
			"MinCapacity":       *scaling.MinTaskCount,
			"MaxCapacity":       *scaling.MaxTaskCount,
		})
	if failure != nil {
		svc.ScalableTarget.fail(failure)
		return
	}
	var registered struct {
		ScalableTargetARN string `json:"ScalableTargetARN"`
	}
	_ = json.Unmarshal(body, &registered)
	svc.ScalableTarget = expressResourceActive(registered.ScalableTargetARN)
	svc.MinCapacity = int(*scaling.MinTaskCount)
	svc.MaxCapacity = int(*scaling.MaxTaskCount)

	metric := expressMetricToPredefined(scaling.AutoScalingMetric)
	body, failure = client.json("application-autoscaling", "application-autoscaling",
		"AnyScaleFrontendService.PutScalingPolicy", "PutScalingPolicy", map[string]any{
			"PolicyName":        svc.ScalingPolicyName,
			"ServiceNamespace":  expressScalingNamespace,
			"ResourceId":        resourceID,
			"ScalableDimension": expressScalingDimension,
			"PolicyType":        "TargetTrackingScaling",
			"TargetTrackingScalingPolicyConfiguration": map[string]any{
				"TargetValue":                   *scaling.AutoScalingTargetValue,
				"PredefinedMetricSpecification": map[string]any{"PredefinedMetricType": metric},
			},
		})
	if failure != nil {
		svc.ScalingPolicy.fail(failure)
		return
	}
	var put struct {
		PolicyARN string `json:"PolicyARN"`
		Alarms    []struct {
			AlarmARN string `json:"AlarmARN"`
		} `json:"Alarms"`
	}
	_ = json.Unmarshal(body, &put)
	svc.ScalingPolicy = expressResourceActive(put.PolicyARN)
	svc.ScalingMetric = metric
	svc.ScalingTargetValue = *scaling.AutoScalingTargetValue
	svc.MetricAlarms = nil
	for _, alarm := range put.Alarms {
		svc.MetricAlarms = append(svc.MetricAlarms, expressResourceActive(alarm.AlarmARN))
	}
}

// expressTeardown removes the service's resources as the infrastructure role,
// stopping at the first refusal, which it records on the resource. The shared
// load balancer, its listener and its security group go with the last service
// on it.
func expressTeardown(client ecsGatewayClient, svc *ECSExpressService) *ecsGatewayCallFailure {
	if svc.EcsServiceKey != "" {
		lock := ecsServiceLock(svc.EcsServiceKey)
		lock.Lock()
		if service, ok := ecsServices.Get(svc.EcsServiceKey); ok {
			service.Status = "INACTIVE"
			service.DesiredCount = 0
			service.RunningCount = 0
			service.PendingCount = 0
			ecsServices.Put(svc.EcsServiceKey, service)
			ecsStopServiceTasks(service)
		}
		lock.Unlock()
	}
	save := func() { ecsExpressServices.Put(svc.ServiceArn, *svc) }
	if svc.Rule.active() {
		if _, failure := client.elb("DeleteRule", url.Values{"RuleArn": {svc.Rule.Arn}}); failure != nil {
			svc.Rule.fail(failure)
			return failure
		}
		svc.Rule.deleted()
		save()
	}
	if svc.TargetGroup.active() {
		if _, failure := client.elb("DeleteTargetGroup", url.Values{"TargetGroupArn": {svc.TargetGroup.Arn}}); failure != nil {
			svc.TargetGroup.fail(failure)
			return failure
		}
		svc.TargetGroup.deleted()
		save()
	}
	// The last service on a shared load balancer takes the listener, the load
	// balancer and its security group with it; an earlier one takes its
	// certificate off the listener. Either way the certificate goes once no
	// listener carries it.
	lb, onLoadBalancer := ecsExpressLoadBalancers.Get(svc.LoadBalancerArn)
	remaining := expressServicesOnLoadBalancer(svc.LoadBalancerArn, svc.ServiceArn)
	if onLoadBalancer && len(remaining) == 0 {
		if failure := expressDeleteLoadBalancer(client, lb); failure != nil {
			return failure
		}
	} else if onLoadBalancer && lb.ListenerArn != "" && svc.Certificate.active() {
		if failure := expressDetachCertificate(client, *svc, lb, remaining); failure != nil {
			svc.Certificate.fail(failure)
			return failure
		}
	}
	if svc.Certificate.active() {
		if _, failure := client.json("acm", "acm", "CertificateManager.DeleteCertificate", "DeleteCertificate",
			map[string]string{"CertificateArn": svc.Certificate.Arn}); failure != nil && failure.Code != "ResourceNotFoundException" {
			svc.Certificate.fail(failure)
			return failure
		}
		svc.Certificate.deleted()
		save()
	}
	if svc.ScalingPolicy.active() {
		if _, failure := client.json("application-autoscaling", "application-autoscaling",
			"AnyScaleFrontendService.DeleteScalingPolicy", "DeleteScalingPolicy", map[string]any{
				"PolicyName":        svc.ScalingPolicyName,
				"ServiceNamespace":  expressScalingNamespace,
				"ResourceId":        expressScalingResourceID(*svc),
				"ScalableDimension": expressScalingDimension,
			}); failure != nil {
			svc.ScalingPolicy.fail(failure)
			return failure
		}
		svc.ScalingPolicy.deleted()
		for i := range svc.MetricAlarms {
			svc.MetricAlarms[i].deleted()
		}
		save()
	}
	if svc.ScalableTarget.active() {
		if _, failure := client.json("application-autoscaling", "application-autoscaling",
			"AnyScaleFrontendService.DeregisterScalableTarget", "DeregisterScalableTarget", map[string]any{
				"ServiceNamespace":  expressScalingNamespace,
				"ResourceId":        expressScalingResourceID(*svc),
				"ScalableDimension": expressScalingDimension,
			}); failure != nil {
			svc.ScalableTarget.fail(failure)
			return failure
		}
		svc.ScalableTarget.deleted()
		save()
	}
	// The tasks' group goes after the load balancer's, which its rule names.
	if svc.ServiceSecurityGroup != nil && svc.ServiceSecurityGroup.active() {
		if failure := ecsGatewayDeleteSecurityGroup(client, svc.ServiceSecurityGroupID); failure != nil && failure.Code != "InvalidGroup.NotFound" {
			svc.ServiceSecurityGroup.fail(failure)
			return failure
		}
		svc.ServiceSecurityGroup.deleted()
		save()
	}
	return nil
}

// expressDeleteLoadBalancer removes a shared load balancer its last service
// left: its listener, then the load balancer, then its security group.
func expressDeleteLoadBalancer(client ecsGatewayClient, lb ECSExpressLoadBalancer) *ecsGatewayCallFailure {
	if lb.Listener.active() {
		if _, failure := client.elb("DeleteListener", url.Values{"ListenerArn": {lb.ListenerArn}}); failure != nil {
			lb.Listener.fail(failure)
			ecsExpressLoadBalancers.Put(lb.LoadBalancerArn, lb)
			return failure
		}
		lb.Listener.deleted()
		ecsExpressLoadBalancers.Put(lb.LoadBalancerArn, lb)
	}
	if _, failure := client.elb("DeleteLoadBalancer", url.Values{"LoadBalancerArn": {lb.LoadBalancerArn}}); failure != nil {
		lb.LoadBalancer.fail(failure)
		ecsExpressLoadBalancers.Put(lb.LoadBalancerArn, lb)
		return failure
	}
	lb.LoadBalancer.deleted()
	if failure := ecsGatewayDeleteSecurityGroup(client, lb.SecurityGroupID); failure != nil && failure.Code != "InvalidGroup.NotFound" {
		lb.SecurityGroup.fail(failure)
		ecsExpressLoadBalancers.Put(lb.LoadBalancerArn, lb)
		return failure
	}
	lb.SecurityGroup.deleted()
	ecsExpressLoadBalancers.Delete(lb.LoadBalancerArn)
	return nil
}

// expressDetachCertificate takes a leaving service's certificate off the
// shared listener; when it is the listener's default, another service's
// certificate takes its place.
func expressDetachCertificate(client ecsGatewayClient, svc ECSExpressService, lb ECSExpressLoadBalancer, remaining []ECSExpressService) *ecsGatewayCallFailure {
	listener, ok := elbv2Listeners.Get(lb.ListenerArn)
	if ok && len(listener.Certificates) > 0 && listener.Certificates[0] == svc.Certificate.Arn {
		for _, other := range remaining {
			if !other.Certificate.active() {
				continue
			}
			if _, failure := client.elb("ModifyListener", url.Values{
				"ListenerArn":                          {lb.ListenerArn},
				"Certificates.member.1.CertificateArn": {other.Certificate.Arn},
			}); failure != nil {
				return failure
			}
			break
		}
	}
	_, failure := client.elb("RemoveListenerCertificates", url.Values{
		"ListenerArn":                          {lb.ListenerArn},
		"Certificates.member.1.CertificateArn": {svc.Certificate.Arn},
	})
	return failure
}

// ecsExpressManagedResourcesJSON is the ECSManagedResources of the Express
// service whose revision revisionArn is, or nil for any other revision.
func ecsExpressManagedResourcesJSON(revisionArn string) map[string]any {
	if ecsExpressServices == nil {
		return nil
	}
	owners := ecsExpressServices.Filter(func(svc ECSExpressService) bool {
		for _, config := range svc.ActiveConfigurations {
			if config.ServiceRevisionArn == revisionArn {
				return true
			}
		}
		return false
	})
	if len(owners) == 0 {
		return nil
	}
	svc := owners[0]
	resource := func(r expressManagedResource, extra map[string]any) map[string]any {
		out := map[string]any{"status": r.Status, "updatedAt": r.UpdatedAt}
		if r.Arn != "" {
			out["arn"] = r.Arn
		}
		if r.StatusReason != "" {
			out["statusReason"] = r.StatusReason
		}
		for key, value := range extra {
			out[key] = value
		}
		return out
	}
	path := map[string]any{"accessType": svc.AccessType, "endpoint": "https://" + svc.Hostname}
	if lb, ok := ecsExpressLoadBalancers.Get(svc.LoadBalancerArn); ok {
		path["loadBalancer"] = resource(lb.LoadBalancer, map[string]any{
			"scheme": lb.Scheme, "subnetIds": lb.Subnets, "securityGroupIds": []string{lb.SecurityGroupID},
		})
		path["loadBalancerSecurityGroups"] = []map[string]any{resource(lb.SecurityGroup, nil)}
		if lb.Listener.created() {
			path["listener"] = resource(lb.Listener, nil)
		}
	} else if svc.LoadBalancerFailure != nil {
		path["loadBalancer"] = resource(*svc.LoadBalancerFailure, map[string]any{"scheme": expressLoadBalancerScheme})
	}
	if svc.Certificate.created() {
		path["certificate"] = resource(svc.Certificate, map[string]any{"domainName": svc.Hostname})
	}
	if svc.Rule.created() {
		path["rule"] = resource(svc.Rule, nil)
	}
	if svc.TargetGroup.created() {
		extra := map[string]any{
			"port":            svc.TargetGroupPort,
			"healthCheckPort": svc.TargetGroupPort,
			"healthCheckPath": svc.ActiveConfigurations[len(svc.ActiveConfigurations)-1].HealthCheckPath,
		}
		path["targetGroups"] = []map[string]any{resource(svc.TargetGroup, extra)}
	}
	out := map[string]any{"ingressPaths": []map[string]any{path}}
	autoScaling := map[string]any{}
	if svc.ScalableTarget.created() {
		autoScaling["scalableTarget"] = resource(svc.ScalableTarget, map[string]any{
			"minCapacity": svc.MinCapacity, "maxCapacity": svc.MaxCapacity,
		})
	}
	if svc.ScalingPolicy.created() {
		autoScaling["applicationAutoScalingPolicies"] = []map[string]any{resource(svc.ScalingPolicy, map[string]any{
			"policyType": "TargetTrackingScaling", "targetValue": float64(svc.ScalingTargetValue), "metric": svc.ScalingMetric,
		})}
	}
	if len(autoScaling) > 0 {
		out["autoScaling"] = autoScaling
	}
	if len(svc.MetricAlarms) > 0 {
		alarms := make([]map[string]any, 0, len(svc.MetricAlarms))
		for _, alarm := range svc.MetricAlarms {
			alarms = append(alarms, resource(alarm, nil))
		}
		out["metricAlarms"] = alarms
	}
	if svc.ServiceSecurityGroup != nil {
		out["serviceSecurityGroups"] = []map[string]any{resource(*svc.ServiceSecurityGroup, nil)}
	}
	return out
}

// ecsExpressHostnameAddresses answers each Express service's hostname with the
// address of the load balancer that carries it, while the service is on it.
func ecsExpressHostnameAddresses() map[string]string {
	if ecsExpressServices == nil {
		return nil
	}
	out := map[string]string{}
	for _, svc := range ecsExpressServices.Filter(func(svc ECSExpressService) bool {
		return svc.LoadBalancerArn != "" && svc.Status.StatusCode != "INACTIVE"
	}) {
		out[svc.Hostname] = svc.LoadBalancerArn
	}
	return out
}

func expressBoolPtr(b bool) *bool { return &b }

// joinSorted joins a string slice into a stable, order-independent key (a copy
// is sorted so the caller's slice is untouched).
func joinSorted(in []string) string {
	cp := append([]string(nil), in...)
	sort.Strings(cp)
	return strings.Join(cp, ",")
}
