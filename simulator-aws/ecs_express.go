package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ECS Express Mode (Express Gateway services) — launched 2025-11-21. An Express
// service is a managed bundle: ECS provisions a Fargate service plus the
// Application Load Balancer, target group, HTTPS listener, ACM certificate,
// security group, and Application Auto Scaling target/policy that front it, and
// hands back an AWS-provided HTTPS endpoint (the ALB DNS name). The four
// operations (Create/Describe/Update/Delete) are awsJson1.1 on the ECS service
// router (`AmazonEC2ContainerServiceV20141113.<Op>`).
//
// The sim composes every backing resource out of the real sim stores so each is
// describable through its own API: the Fargate service lands in ecsServices, the
// task definition in ecsTaskDefinitions, the ALB/target-group/listener in the
// elbv2 stores, the security group in ec2SecurityGroups, the cert in
// acmCertificates, and the scalable target + policy in the application
// auto-scaling stores. Delete tears all of them down. Up to 25 Express services
// with an identical network configuration consolidate behind one ALB.

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

// ECSExpressService is the stored control-plane record for an Express service.
// It carries the active configuration revisions plus the ARNs of every backing
// resource so Delete can tear them all down.
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

	// Backing resource handles — torn down on Delete. AccessType is PUBLIC or
	// PRIVATE; the ALB is shared (consolidated) so its lifecycle is reference
	// counted across Express services in expressLoadBalancerRefs.
	AccessType       string `json:"-"`
	LoadBalancerArn  string `json:"-"`
	TargetGroupArn   string `json:"-"`
	ListenerArn      string `json:"-"`
	CertificateArn   string `json:"-"`
	SecurityGroupID  string `json:"-"` // sim-created SG (torn down); empty if caller supplied one
	EcsServiceKey    string `json:"-"`
	ScalableTargetNS string `json:"-"`
	ScalableResource string `json:"-"`
	ScalableDim      string `json:"-"`
	ScalingPolicy    string `json:"-"`
}

var ecsExpressServices sim.Store[ECSExpressService]

func registerECSExpress(r *AWSRouter, srv *sim.Server) {
	ecsExpressServices = sim.MakeStore[ECSExpressService](srv.DB(), "ecs_express_services")
	expressRebuildLoadBalancerRefs()

	r.Register("AmazonEC2ContainerServiceV20141113.CreateExpressGatewayService", handleECSCreateExpressGatewayService)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeExpressGatewayService", handleECSDescribeExpressGatewayService)
	r.Register("AmazonEC2ContainerServiceV20141113.UpdateExpressGatewayService", handleECSUpdateExpressGatewayService)
	r.Register("AmazonEC2ContainerServiceV20141113.DeleteExpressGatewayService", handleECSDeleteExpressGatewayService)
}

// ecsExpressArn builds the ARN that identifies an Express service. Real ECS
// Express ARNs use the `express-gateway-service` resource type, scoped by
// cluster: arn:aws:ecs:<region>:<account>:express-gateway-service/<cluster>/<name>.
func ecsExpressArn(cluster, name string) string {
	return ecsArn("express-gateway-service", cluster+"/"+name)
}

// ecsExpressRevisionArn builds a service-revision ARN. ECS service revisions use
// the `service-revision` resource type with a numeric id.
func ecsExpressRevisionArn(cluster, name string, revision int) string {
	return ecsArn("service-revision", fmt.Sprintf("%s/%s/%d", cluster, name, revision))
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
		serviceName = "express-" + generateUUID()[:8]
	}
	serviceArn := ecsExpressArn(clusterName, serviceName)
	if existing, ok := ecsExpressServices.Get(serviceArn); ok && existing.Status.StatusCode != "INACTIVE" {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"An Express service named %q already exists in cluster %q.", serviceName, clusterName)
		return
	}

	cpu := firstNonEmpty(req.Cpu, "256")
	memory := firstNonEmpty(req.Memory, "512")
	healthCheckPath := firstNonEmpty(req.HealthCheckPath, "/ping")

	now := float64(time.Now().Unix())

	// Resolve / register the task definition (managed when primaryContainer is
	// given; otherwise the customer-supplied taskDefinitionArn).
	taskDefArn := req.TaskDefinitionArn
	container := req.PrimaryContainer
	containerPort := int32(80)
	if container != nil && container.ContainerPort != nil {
		containerPort = *container.ContainerPort
	}
	if taskDefArn == "" {
		taskDefArn = expressRegisterManagedTaskDef(serviceName, cpu, memory, req.ExecutionRoleArn, req.TaskRoleArn, container, containerPort)
	}

	// Network configuration. PUBLIC unless the request restricts ingress; the
	// SDK has no explicit accessType input on create, so a service with a
	// network config of private subnets is PRIVATE — model PUBLIC by default
	// (internet-facing) which is the documented Express default.
	netCfg := req.NetworkConfiguration
	if netCfg == nil {
		netCfg = &expressNetworkConfiguration{}
	}
	if len(netCfg.Subnets) == 0 {
		defaultSubnet := defaultVPCSubnetID()
		if defaultSubnet == "" {
			AWSError(w, "InvalidParameterException",
				"No default VPC subnet is available for the Express service.", http.StatusBadRequest)
			return
		}
		netCfg.Subnets = []string{defaultSubnet}
	}
	accessType := "PUBLIC"

	// Security group: create one when the caller didn't supply any.
	var createdSG string
	securityGroups := netCfg.SecurityGroups
	if len(securityGroups) == 0 {
		createdSG = expressCreateSecurityGroup(serviceName, expressVPCFromSubnets(netCfg.Subnets), containerPort)
		securityGroups = []string{createdSG}
	}

	// ALB consolidation: reuse an Express ALB with the same network config that
	// has < 25 services; else create a new one (+ its ACM cert).
	scheme := "internet-facing"
	if accessType == "PRIVATE" {
		scheme = "internal"
	}
	lbArn, albDNS, certArn := expressEnsureLoadBalancer(serviceName, scheme, netCfg.Subnets, securityGroups)

	// Target group + HTTPS listener wired to the ALB.
	tgArn := expressCreateTargetGroup(serviceName, int(containerPort), healthCheckPath, expressVPCFromSubnets(netCfg.Subnets))
	listenerArn := expressCreateListener(lbArn, certArn, tgArn)

	// ECS Fargate service backing the Express service.
	desired := 1
	scalingTarget := req.ScalingTarget
	if scalingTarget != nil && scalingTarget.MinTaskCount != nil {
		desired = int(*scalingTarget.MinTaskCount)
	}
	ecsKey := expressCreateFargateService(clusterName, cluster.ClusterArn, serviceName, taskDefArn, desired, netCfg, lbArn, tgArn)

	// Application Auto Scaling target + target-tracking policy.
	asNS, asResource, asDim, policyName := expressCreateAutoScaling(clusterName, serviceName, scalingTarget)

	// Build the active configuration (revision 1).
	cfg := expressGatewayServiceConfiguration{
		Cpu:                  cpu,
		CreatedAt:            now,
		ExecutionRoleArn:     req.ExecutionRoleArn,
		HealthCheckPath:      healthCheckPath,
		IngressPaths:         []expressIngressPath{{AccessType: accessType, Endpoint: "https://" + albDNS}},
		Memory:               memory,
		NetworkConfiguration: &expressNetworkConfiguration{SecurityGroups: securityGroups, Subnets: netCfg.Subnets},
		PrimaryContainer:     container,
		ScalingTarget:        expressManagedScalingTarget(scalingTarget),
		ServiceRevisionArn:   ecsExpressRevisionArn(clusterName, serviceName, 1),
		TaskDefinitionArn:    taskDefArn,
		TaskRoleArn:          req.TaskRoleArn,
	}

	svc := ECSExpressService{
		ServiceArn:  serviceArn,
		ServiceName: serviceName,
		// Real ECS returns the cluster as its full ARN in the Express service
		// record; terraform-provider-aws parses the cluster name back out of it
		// (clusterNameFromARN splits on "/"), so a bare name would read back empty.
		Cluster:               cluster.ClusterArn,
		InfrastructureRoleArn: req.InfrastructureRoleArn,
		CreatedAt:             now,
		UpdatedAt:             now,
		Status:                expressGatewayServiceStatus{StatusCode: "ACTIVE"},
		ActiveConfigurations:  []expressGatewayServiceConfiguration{cfg},
		Tags:                  req.Tags,
		AccessType:            accessType,
		LoadBalancerArn:       lbArn,
		TargetGroupArn:        tgArn,
		ListenerArn:           listenerArn,
		CertificateArn:        certArn,
		SecurityGroupID:       createdSG,
		EcsServiceKey:         ecsKey,
		ScalableTargetNS:      asNS,
		ScalableResource:      asResource,
		ScalableDim:           asDim,
		ScalingPolicy:         policyName,
	}
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

	// New revision derived from the current one with the mutable fields applied.
	cur := svc.ActiveConfigurations[len(svc.ActiveConfigurations)-1]
	now := float64(time.Now().Unix())
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
		expressUpdateAutoScaling(svc, req.ScalingTarget)
	}
	if req.TaskDefinitionArn != "" {
		next.TaskDefinitionArn = req.TaskDefinitionArn
	} else if req.Cpu != "" || req.Memory != "" || req.ExecutionRoleArn != "" ||
		req.TaskRoleArn != "" || req.PrimaryContainer != nil {
		port := int32(80)
		if next.PrimaryContainer != nil && next.PrimaryContainer.ContainerPort != nil {
			port = *next.PrimaryContainer.ContainerPort
		}
		next.TaskDefinitionArn = expressRegisterManagedTaskDef(
			svc.ServiceName,
			next.Cpu,
			next.Memory,
			next.ExecutionRoleArn,
			next.TaskRoleArn,
			next.PrimaryContainer,
			port,
		)
	}

	revision := len(svc.ActiveConfigurations) + 1
	next.ServiceRevisionArn = ecsExpressRevisionArn(ecsClusterNameFromRef(svc.Cluster), svc.ServiceName, revision)

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
				networkJSON, _ := json.Marshal(map[string]any{
					"awsvpcConfiguration": map[string]any{
						"subnets":        next.NetworkConfiguration.Subnets,
						"securityGroups": next.NetworkConfiguration.SecurityGroups,
						"assignPublicIp": "ENABLED",
					},
				})
				es.NetworkConfiguration = networkJSON
			}
			if next.PrimaryContainer != nil && next.PrimaryContainer.ContainerPort != nil {
				port := int(*next.PrimaryContainer.ContainerPort)
				loadBalancers, _ := json.Marshal([]map[string]any{{
					"targetGroupArn": svc.TargetGroupArn,
					"containerName":  "Main",
					"containerPort":  port,
				}})
				es.LoadBalancers = loadBalancers
			}
			es.Deployments = []ECSDeployment{ecsServiceDeployment(*es, now)}
		})
		if backing, ok := ecsServices.Get(ecsKey); ok {
			ecsRecordServiceDeployment(backing, "")
		}
		ecsRequestServiceReconcile(ecsKey)
	}
	if svc.TargetGroupArn != "" {
		elbv2TargetGroups.Update(svc.TargetGroupArn, func(targetGroup *ELBv2TargetGroup) {
			targetGroup.HealthCheckPath = next.HealthCheckPath
			if next.PrimaryContainer != nil && next.PrimaryContainer.ContainerPort != nil {
				targetGroup.Port = int(*next.PrimaryContainer.ContainerPort)
			}
		})
	}

	svc.ActiveConfigurations = append(svc.ActiveConfigurations, next)
	svc.UpdatedAt = now
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

	// Tear down every backing resource. Real ECS returns the service in
	// DRAINING from the Delete call and converges it to INACTIVE as it drains;
	// the sim's teardown is synchronous, so the service is fully drained the
	// moment Delete returns. Persist INACTIVE so the next Describe (the provider's
	// delete-waiter polls until INACTIVE) completes, but return DRAINING in this
	// immediate response to match the API's synchronous shape.
	expressTeardown(svc)

	now := float64(time.Now().Unix())
	svc.Status = expressGatewayServiceStatus{StatusCode: "INACTIVE", StatusReason: "Service has been deleted"}
	svc.UpdatedAt = now
	ecsExpressServices.Put(svc.ServiceArn, svc)

	resp := svc
	resp.Status = expressGatewayServiceStatus{StatusCode: "DRAINING", StatusReason: "Service is being deleted"}
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

// expressCreateFargateService creates the backing ECS Fargate service in the
// real ecsServices store and returns its store key.
func expressCreateFargateService(clusterName, clusterArn, serviceName, taskDefArn string, desired int, netCfg *expressNetworkConfiguration, lbArn, tgArn string) string {
	now := float64(time.Now().Unix())
	netJSON, _ := json.Marshal(map[string]any{
		"awsvpcConfiguration": map[string]any{
			"subnets":        netCfg.Subnets,
			"securityGroups": netCfg.SecurityGroups,
			"assignPublicIp": "ENABLED",
		},
	})
	lbJSON, _ := json.Marshal([]map[string]any{{
		"targetGroupArn": tgArn,
		"containerName":  "Main",
		"containerPort":  elbv2TargetGroupPort(tgArn),
	}})
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
		NetworkConfiguration: netJSON,
		LoadBalancers:        lbJSON,
	}
	svc.Deployments = []ECSDeployment{ecsServiceDeployment(svc, now)}
	key := ecsServiceKey(clusterName, serviceName)
	ecsServices.Put(key, svc)
	ecsRequestServiceReconcile(key)
	return key
}

func elbv2TargetGroupPort(targetGroupArn string) int {
	targetGroup, _ := elbv2TargetGroups.Get(targetGroupArn)
	return targetGroup.Port
}

// expressLoadBalancerRefs counts how many Express services consolidate behind a
// shared ALB, keyed by load-balancer ARN. Guarded by ecsRevisionMu reuse would
// be wrong (different concern); the sim is single-process and the express ops
// run on the serial HTTP handler, so a plain map suffices.
var (
	expressLoadBalancerMu   sync.Mutex
	expressLoadBalancerRefs map[string]int
)

func expressRebuildLoadBalancerRefs() {
	expressLoadBalancerMu.Lock()
	defer expressLoadBalancerMu.Unlock()
	expressLoadBalancerRefs = make(map[string]int)
	for _, service := range ecsExpressServices.List() {
		if service.Status.StatusCode != "INACTIVE" && service.LoadBalancerArn != "" {
			expressLoadBalancerRefs[service.LoadBalancerArn]++
		}
	}
}

// expressALBNetKey is the consolidation key: same scheme + subnets + SGs share
// an ALB.
func expressALBNetKey(scheme string, subnets, sgs []string) string {
	return scheme + "|" + joinSorted(subnets) + "|" + joinSorted(sgs)
}

// expressEnsureLoadBalancer reuses an existing Express ALB with an identical
// network config that has fewer than 25 services; else it creates a new ALB +
// ACM cert. Returns (lbArn, dnsName, certArn).
func expressEnsureLoadBalancer(serviceName, scheme string, subnets, sgs []string) (string, string, string) {
	expressLoadBalancerMu.Lock()
	defer expressLoadBalancerMu.Unlock()
	netKey := expressALBNetKey(scheme, subnets, sgs)
	for _, existing := range ecsExpressServices.List() {
		if existing.Status.StatusCode == "INACTIVE" || existing.LoadBalancerArn == "" {
			continue
		}
		lb, ok := elbv2LoadBalancers.Get(existing.LoadBalancerArn)
		if !ok {
			continue
		}
		if existing.AccessType == "" {
			continue
		}
		existingScheme := "internet-facing"
		if existing.AccessType == "PRIVATE" {
			existingScheme = "internal"
		}
		if expressALBNetKey(existingScheme, lb.Subnets, lb.SecurityGroups) != netKey {
			continue
		}
		if expressLoadBalancerRefs[existing.LoadBalancerArn] >= 25 {
			continue
		}
		expressLoadBalancerRefs[existing.LoadBalancerArn]++
		return existing.LoadBalancerArn, lb.DNSName, existing.CertificateArn
	}

	// Create a new ALB + cert.
	name := "express-" + serviceName
	id := generateUUID()[:12]
	arn := fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:loadbalancer/app/%s/%s", awsRegion(), awsAccountID(), name, id)
	dnsName := fmt.Sprintf("%s-%s.elb.%s.amazonaws.com", name, id[:8], awsRegion())
	lb := ELBv2LoadBalancer{
		Arn:            arn,
		Name:           name,
		DNSName:        dnsName,
		CanonicalZone:  "Z35SXDOTRQ7X7K",
		Scheme:         scheme,
		Type:           "application",
		State:          "active",
		VpcID:          elbv2VPCFromSubnets(subnets),
		Subnets:        subnets,
		SecurityGroups: sgs,
		IpAddressType:  "ipv4",
		CreatedTime:    time.Now().UTC().Format(time.RFC3339),
		Tags:           map[string]string{},
		Attributes:     defaultELBv2LoadBalancerAttributes(),
	}
	elbv2LoadBalancers.Put(arn, lb)
	expressLoadBalancerRefs[arn] = 1

	certArn := expressCreateCertificate(dnsName)
	return arn, dnsName, certArn
}

// expressCreateCertificate creates a managed ACM cert for the ALB DNS name in
// the real acmCertificates store and returns its ARN.
func expressCreateCertificate(domain string) string {
	id := acmRandomID()
	cert := ACMCertificate{
		CertificateArn:     acmCertARN(id),
		DomainName:         domain,
		Status:             "ISSUED",
		Type:               "AMAZON_ISSUED",
		KeyAlgorithm:       "RSA_2048",
		SignatureAlgorithm: "SHA256WITHRSA",
		IssuedAt:           acmEpochNow(),
		CreatedAt:          acmEpochNow(),
		InUseBy:            []string{},
		RenewalEligibility: "INELIGIBLE",
	}
	acmCertificates.Put(id, acmStoredCert{Cert: cert})
	return cert.CertificateArn
}

// expressCreateTargetGroup creates the ALB target group (ip targets, the Fargate
// awsvpc default) in the real elbv2TargetGroups store and returns its ARN.
func expressCreateTargetGroup(serviceName string, port int, healthCheckPath, vpcID string) string {
	name := "express-" + serviceName
	id := generateUUID()[:12]
	arn := fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:targetgroup/%s/%s", awsRegion(), awsAccountID(), name, id)
	tg := ELBv2TargetGroup{
		Arn:                     arn,
		Name:                    name,
		Protocol:                "HTTP",
		Port:                    port,
		VpcID:                   vpcID,
		TargetType:              "ip",
		IpAddressType:           "ipv4",
		HealthCheckProtocol:     "HTTP",
		HealthCheckPort:         "traffic-port",
		HealthCheckPath:         healthCheckPath,
		HealthCheckEnabled:      true,
		HealthCheckInterval:     30,
		HealthCheckTimeout:      5,
		HealthyThresholdCount:   5,
		UnhealthyThresholdCount: 2,
		MatcherHttpCode:         "200",
		Tags:                    map[string]string{},
		Attributes:              defaultELBv2TargetGroupAttributes(),
	}
	elbv2TargetGroups.Put(arn, tg)
	return arn
}

// expressCreateListener creates the HTTPS:443 listener forwarding to the target
// group in the real elbv2Listeners store and returns its ARN.
func expressCreateListener(lbArn, certArn, tgArn string) string {
	lb, ok := elbv2LoadBalancers.Get(lbArn)
	if !ok {
		return ""
	}
	id := generateUUID()[:12]
	arn := fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:listener/%s/%s/%s/%s",
		awsRegion(), awsAccountID(), elbv2LoadBalancerKind(lb), lb.Name, elbv2LoadBalancerID(lb.Arn), id)
	listener := ELBv2Listener{
		Arn:             arn,
		LoadBalancerArn: lbArn,
		Protocol:        "HTTPS",
		Port:            443,
		DefaultActions:  []ELBv2Action{{Type: "forward", TargetGroupArn: tgArn}},
		Certificates:    []string{certArn},
		SslPolicy:       "ELBSecurityPolicy-2016-08",
		Attributes:      defaultELBv2ListenerAttributes(lb.Type),
	}
	elbv2Listeners.Put(arn, listener)
	elbv2TargetGroups.Update(tgArn, func(tg *ELBv2TargetGroup) {
		tg.LoadBalancerArns = appendUnique(tg.LoadBalancerArns, lbArn)
	})
	return arn
}

// expressCreateSecurityGroup models the managed security group ECS Express
// creates for a service. Real Express pairs an ALB group (443 from the world
// for a PUBLIC service) with a task group that admits the container port from
// the ALB group; the sim composes both into one group. Its load balancer
// terminates the listener on the host and reaches the task's elastic network
// interface from the VPC gateway coordinate, so the faithful task-side rule
// admits the container port from the VPC CIDR — the path health checks and
// forwarded traffic really take, and the path the real-VPC tier enforces in
// nftables. A rule-less group would install a deny-all ingress filter there
// and the service could never become healthy.
func expressCreateSecurityGroup(serviceName, vpcID string, containerPort int32) string {
	id := ec2ID("sg")
	vpcCIDR := "0.0.0.0/0"
	if vpc, ok := ec2Vpcs.Get(vpcID); ok && vpc.CidrBlock != "" {
		vpcCIDR = vpc.CidrBlock
	}
	sg := EC2SecurityGroup{
		GroupId:     id,
		GroupName:   "express-" + serviceName,
		Description: "Managed by ECS Express for service " + serviceName,
		VpcId:       vpcID,
		OwnerId:     ec2Owner(),
		IpPermissions: []EC2IpPermission{
			{
				IpProtocol: "tcp",
				FromPort:   443,
				ToPort:     443,
				IpRanges:   []EC2IpRange{{CidrIp: "0.0.0.0/0", Description: "PUBLIC Express ingress"}},
			},
			{
				IpProtocol: "tcp",
				FromPort:   int(containerPort),
				ToPort:     int(containerPort),
				IpRanges:   []EC2IpRange{{CidrIp: vpcCIDR, Description: "Express load balancer to task"}},
			},
		},
		IpPermissionsEgress: []EC2IpPermission{
			{IpProtocol: "-1", IpRanges: []EC2IpRange{{CidrIp: "0.0.0.0/0"}}},
		},
	}
	ec2SecurityGroups.Put(id, sg)
	return id
}

// expressCreateAutoScaling registers the scalable target + target-tracking
// policy in the real application auto-scaling stores. Returns the target triple
// plus the policy name (for teardown). Every Express service is provisioned with
// managed auto-scaling — when the caller omits scalingTarget, ECS applies the
// documented defaults (target value 60 on AVERAGE_CPU), so the scalable target
// and policy are always created.
func expressCreateAutoScaling(clusterName, serviceName string, st *expressScalingTarget) (ns, resourceID, dim, policyName string) {
	if st == nil {
		st = &expressScalingTarget{}
	}
	st = expressNormalizeScalingTarget(st)
	ns = "ecs"
	resourceID = fmt.Sprintf("service/%s/%s", clusterName, serviceName)
	dim = "ecs:service:DesiredCount"
	minCap, maxCap := 1, 1
	if st.MinTaskCount != nil {
		minCap = int(*st.MinTaskCount)
	}
	if st.MaxTaskCount != nil {
		maxCap = int(*st.MaxTaskCount)
	}
	target := AppScalableTarget{
		ServiceNamespace:  ns,
		ResourceId:        resourceID,
		ScalableDimension: dim,
		MinCapacity:       minCap,
		MaxCapacity:       maxCap,
		CreationTime:      float64(time.Now().Unix()),
		ARN:               appScalableTargetARN(generateUUID()),
	}
	appScalableTargets.Put(appScalableTargetKey(ns, resourceID, dim), target)

	policyName = "express-" + serviceName
	metric := expressMetricToPredefined(st.AutoScalingMetric)
	targetValue := int32(60)
	if st.AutoScalingTargetValue != nil {
		targetValue = *st.AutoScalingTargetValue
	}
	cfg, _ := json.Marshal(map[string]any{
		"TargetValue": targetValue,
		"PredefinedMetricSpecification": map[string]any{
			"PredefinedMetricType": metric,
		},
	})
	policy := AppScalingPolicy{
		PolicyName:        policyName,
		PolicyARN:         appScalingPolicyARN(ns, resourceID, policyName),
		ServiceNamespace:  ns,
		ResourceId:        resourceID,
		ScalableDimension: dim,
		PolicyType:        "TargetTrackingScaling",
		TargetTracking:    cfg,
		CreationTime:      float64(time.Now().Unix()),
	}
	appScalingPolicies.Put(appScalingPolicyKey(ns, resourceID, dim, policyName), policy)
	return ns, resourceID, dim, policyName
}

// expressUpdateAutoScaling re-applies the scaling configuration on update.
func expressUpdateAutoScaling(svc ECSExpressService, st *expressScalingTarget) {
	if st == nil || svc.ScalableResource == "" {
		return
	}
	appScalableTargets.Update(appScalableTargetKey(svc.ScalableTargetNS, svc.ScalableResource, svc.ScalableDim), func(t *AppScalableTarget) {
		if st.MinTaskCount != nil {
			t.MinCapacity = int(*st.MinTaskCount)
		}
		if st.MaxTaskCount != nil {
			t.MaxCapacity = int(*st.MaxTaskCount)
		}
	})
	if svc.ScalingPolicy == "" {
		return
	}
	metric := expressMetricToPredefined(st.AutoScalingMetric)
	targetValue := int32(60)
	if st.AutoScalingTargetValue != nil {
		targetValue = *st.AutoScalingTargetValue
	}
	cfg, _ := json.Marshal(map[string]any{
		"TargetValue": targetValue,
		"PredefinedMetricSpecification": map[string]any{
			"PredefinedMetricType": metric,
		},
	})
	appScalingPolicies.Update(appScalingPolicyKey(svc.ScalableTargetNS, svc.ScalableResource, svc.ScalableDim, svc.ScalingPolicy), func(p *AppScalingPolicy) {
		p.TargetTracking = cfg
	})
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

// expressTeardown removes every backing resource for an Express service. The
// ALB (and its cert) is reference counted across consolidated services; it is
// only removed when the last Express service on it is deleted.
func expressTeardown(svc ECSExpressService) {
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
	if svc.ListenerArn != "" {
		elbv2Listeners.Delete(svc.ListenerArn)
	}
	if svc.TargetGroupArn != "" {
		elbv2TargetGroups.Delete(svc.TargetGroupArn)
	}
	if svc.LoadBalancerArn != "" {
		expressLoadBalancerMu.Lock()
		expressLoadBalancerRefs[svc.LoadBalancerArn]--
		if expressLoadBalancerRefs[svc.LoadBalancerArn] <= 0 {
			delete(expressLoadBalancerRefs, svc.LoadBalancerArn)
			elbv2LoadBalancers.Delete(svc.LoadBalancerArn)
			if svc.CertificateArn != "" {
				acmCertificates.Delete(acmARNToID(svc.CertificateArn))
			}
		}
		expressLoadBalancerMu.Unlock()
	}
	if svc.SecurityGroupID != "" {
		ec2SecurityGroups.Delete(svc.SecurityGroupID)
	}
	if svc.EcsServiceKey != "" {
		ecsServices.Update(svc.EcsServiceKey, func(es *ECSService) {
			es.Status = "INACTIVE"
			es.DesiredCount = 0
			es.RunningCount = 0
		})
	}
	if svc.ScalableResource != "" {
		appScalableTargets.Delete(appScalableTargetKey(svc.ScalableTargetNS, svc.ScalableResource, svc.ScalableDim))
		if svc.ScalingPolicy != "" {
			appScalingPolicies.Delete(appScalingPolicyKey(svc.ScalableTargetNS, svc.ScalableResource, svc.ScalableDim, svc.ScalingPolicy))
		}
	}
}

// expressVPCFromSubnets resolves the VPC of the first known subnet (reuses the
// elbv2 helper).
func expressVPCFromSubnets(subnets []string) string {
	return elbv2VPCFromSubnets(subnets)
}

func expressBoolPtr(b bool) *bool { return &b }

// joinSorted joins a string slice into a stable, order-independent key (a copy
// is sorted so the caller's slice is untouched).
func joinSorted(in []string) string {
	cp := append([]string(nil), in...)
	sort.Strings(cp)
	return strings.Join(cp, ",")
}
