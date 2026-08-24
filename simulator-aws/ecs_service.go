package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// ECS Service family — terraform-provider-aws declares `aws_ecs_service` for
// long-lived Fargate services (the common case; RunTask alone covers only
// one-shot tasks) and `aws_ecs_cluster_capacity_providers` for the cluster's
// capacity-provider config. Services reconcile durable tasks through the same
// runtime as RunTask; their counts and deployment state are derived from those
// tasks rather than synthesized from desiredCount.

// ECSService is a control-plane model of an ECS service. Config blocks the sim
// doesn't interpret (network/load-balancer/registry config) are held as raw
// JSON so they round-trip byte-exact.
type ECSService struct {
	ServiceArn               string          `json:"serviceArn"`
	ServiceName              string          `json:"serviceName"`
	ClusterArn               string          `json:"clusterArn"`
	TaskDefinition           string          `json:"taskDefinition"`
	DesiredCount             int             `json:"desiredCount"`
	RunningCount             int             `json:"runningCount"`
	PendingCount             int             `json:"pendingCount"`
	Status                   string          `json:"status"`
	LaunchType               string          `json:"launchType,omitempty"`
	PlatformVersion          string          `json:"platformVersion,omitempty"`
	SchedulingStrategy       string          `json:"schedulingStrategy,omitempty"`
	RoleArn                  string          `json:"roleArn,omitempty"`
	PropagateTags            string          `json:"propagateTags,omitempty"`
	EnableExecuteCommand     bool            `json:"enableExecuteCommand,omitempty"`
	CreatedAt                float64         `json:"createdAt"`
	NetworkConfiguration     json.RawMessage `json:"networkConfiguration,omitempty"`
	LoadBalancers            json.RawMessage `json:"loadBalancers,omitempty"`
	ServiceRegistries        json.RawMessage `json:"serviceRegistries,omitempty"`
	DeploymentController     json.RawMessage `json:"deploymentController,omitempty"`
	DeploymentConfiguration  json.RawMessage `json:"deploymentConfiguration,omitempty"`
	CapacityProviderStrategy json.RawMessage `json:"capacityProviderStrategy,omitempty"`
	// Top-level Service knobs the provider reads back — each was dropped on
	// create/update, so aws_ecs_service.{enable_ecs_managed_tags,
	// health_check_grace_period_seconds,ordered_placement_strategy,
	// placement_constraints,availability_zone_rebalancing} drifted every plan.
	EnableECSManagedTags          bool            `json:"enableECSManagedTags,omitempty"`
	HealthCheckGracePeriodSeconds *int            `json:"healthCheckGracePeriodSeconds,omitempty"`
	PlacementConstraints          json.RawMessage `json:"placementConstraints,omitempty"`
	PlacementStrategy             json.RawMessage `json:"placementStrategy,omitempty"`
	AvailabilityZoneRebalancing   string          `json:"availabilityZoneRebalancing,omitempty"`
	// These three surface inside the deployment record (not the Service top
	// level) — the SDK Service type has no such fields. Stored here for
	// round-trip, emitted via the primary deployment.
	ServiceConnectConfiguration json.RawMessage   `json:"-"`
	VolumeConfigurations        json.RawMessage   `json:"-"`
	VpcLatticeConfigurations    json.RawMessage   `json:"-"`
	Deployments                 []ECSDeployment   `json:"deployments"`
	Events                      []ECSServiceEvent `json:"events,omitempty"`
	Tags                        []ECSTag          `json:"tags,omitempty"`
}

type ECSServiceEvent struct {
	Id        string  `json:"id"`
	CreatedAt float64 `json:"createdAt"`
	Message   string  `json:"message"`
}

// ECSDeployment is the service's deployment record. Its counts and rollout
// state follow the tasks owned by that deployment.
type ECSDeployment struct {
	Id                          string          `json:"id"`
	Status                      string          `json:"status"`
	TaskDefinition              string          `json:"taskDefinition"`
	DesiredCount                int             `json:"desiredCount"`
	RunningCount                int             `json:"runningCount"`
	PendingCount                int             `json:"pendingCount"`
	RolloutState                string          `json:"rolloutState"`
	RolloutStateReason          string          `json:"rolloutStateReason,omitempty"`
	CreatedAt                   float64         `json:"createdAt"`
	UpdatedAt                   float64         `json:"updatedAt"`
	ServiceConnectConfiguration json.RawMessage `json:"serviceConnectConfiguration,omitempty"`
	VolumeConfigurations        json.RawMessage `json:"volumeConfigurations,omitempty"`
	VpcLatticeConfigurations    json.RawMessage `json:"vpcLatticeConfigurations,omitempty"`
}

var ecsServices sim.Store[ECSService]

func registerECSServices(r *sim.AWSRouter, srv *sim.Server) {
	ecsServices = sim.MakeStore[ECSService](srv.DB(), "ecs_services")
	ecsServiceSchedulerStates = sim.MakeStore[ECSServiceSchedulerState](srv.DB(), "ecs_service_scheduler_states")

	r.Register("AmazonEC2ContainerServiceV20141113.CreateService", handleECSCreateService)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeServices", handleECSDescribeServices)
	r.Register("AmazonEC2ContainerServiceV20141113.ListServices", handleECSListServices)
	r.Register("AmazonEC2ContainerServiceV20141113.UpdateService", handleECSUpdateService)
	r.Register("AmazonEC2ContainerServiceV20141113.DeleteService", handleECSDeleteService)
	r.Register("AmazonEC2ContainerServiceV20141113.PutClusterCapacityProviders", handleECSPutClusterCapacityProviders)
	r.Register("AmazonEC2ContainerServiceV20141113.ListClusters", handleECSListClusters)
	r.Register("AmazonEC2ContainerServiceV20141113.ListTaskDefinitions", handleECSListTaskDefinitions)
	r.Register("AmazonEC2ContainerServiceV20141113.ListTaskDefinitionFamilies", handleECSListTaskDefinitionFamilies)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeCapacityProviders", handleECSDescribeCapacityProviders)

	// ECS Express Mode (Express Gateway services) — the managed
	// ALB+Fargate+autoscaling bundle.
	registerECSExpress(r, srv)
}

// ecsBuiltInCapacityProviders are the two AWS-managed providers every account
// has; they are always ACTIVE and need no cluster association.
var ecsBuiltInCapacityProviders = []string{"FARGATE", "FARGATE_SPOT"}

// handleECSDescribeCapacityProviders is the read-back for capacity providers
// — without it `aws_ecs_cluster_capacity_providers` shows spurious
// drift on the post-apply plan. The sim has no standalone capacity-provider
// store (providers live as names on clusters via PutClusterCapacityProviders),
// so it resolves the built-in FARGATE/FARGATE_SPOT plus any custom provider
// referenced by a cluster. A requested name that resolves to neither is a
// failure, mirroring real ECS.
func handleECSDescribeCapacityProviders(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CapacityProviders []string `json:"capacityProviders"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}

	known := map[string]bool{}
	for _, name := range ecsBuiltInCapacityProviders {
		known[name] = true
	}
	for _, c := range ecsClusters.List() {
		for _, cp := range c.CapacityProviders {
			known[ecsCapacityProviderName(cp)] = true
		}
	}
	// Custom providers created via CreateCapacityProvider carry their full
	// config (autoScalingGroupProvider, status, type, tags); echo them verbatim.
	custom := map[string]ECSCapacityProvider{}
	for _, cp := range ecsCapacityProviders.List() {
		known[cp.Name] = true
		custom[cp.Name] = cp
	}

	requested := req.CapacityProviders
	if len(requested) == 0 {
		for name := range known {
			requested = append(requested, name)
		}
		sort.Strings(requested)
	}

	providers := make([]any, 0, len(requested))
	failures := make([]map[string]any, 0)
	for _, ref := range requested {
		name := ecsCapacityProviderName(ref)
		if !known[name] {
			failures = append(failures, map[string]any{
				"arn":    ecsArn("capacity-provider", name),
				"reason": "MISSING",
			})
			continue
		}
		if cp, ok := custom[name]; ok {
			providers = append(providers, cp)
			continue
		}
		providers = append(providers, map[string]any{
			"capacityProviderArn": ecsArn("capacity-provider", name),
			"name":                name,
			"status":              "ACTIVE",
			"tags":                []any{},
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"capacityProviders": providers,
		"failures":          failures,
	})
}

// ecsCapacityProviderName normalizes a capacity provider name or ARN to its
// bare name.
func ecsCapacityProviderName(ref string) string {
	if strings.HasPrefix(ref, "arn:") {
		parts := strings.Split(ref, "/")
		return parts[len(parts)-1]
	}
	return ref
}

// handleECSListTaskDefinitionFamilies aggregates the distinct families across
// the registered task definitions — the family companion to
// ListTaskDefinitions. status (ACTIVE/INACTIVE/ALL) selects which revisions a
// family must have to be listed; familyPrefix narrows by name prefix.
func handleECSListTaskDefinitionFamilies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FamilyPrefix string `json:"familyPrefix"`
		Status       string `json:"status"`
		MaxResults   int    `json:"maxResults"`
		NextToken    string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}

	status := req.Status
	if status == "" {
		status = "ACTIVE"
	}
	// active[family] is true if the family has ≥1 ACTIVE revision.
	active := map[string]bool{}
	seen := map[string]bool{}
	for _, td := range ecsTaskDefinitions.List() {
		seen[td.Family] = true
		if td.Status == "ACTIVE" {
			active[td.Family] = true
		}
	}
	families := make([]string, 0, len(seen))
	for family := range seen {
		if req.FamilyPrefix != "" && !strings.HasPrefix(family, req.FamilyPrefix) {
			continue
		}
		switch status {
		case "ACTIVE":
			if !active[family] {
				continue
			}
		case "INACTIVE":
			if active[family] {
				continue
			}
		}
		families = append(families, family)
	}
	sort.Strings(families)
	page, next := awsPage(families, req.NextToken, req.MaxResults, 100)
	out := map[string]any{"families": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// ecsClusterNameFromRef extracts the cluster name from a name or ARN,
// defaulting to "default" (the implicit cluster) when empty.
func ecsClusterNameFromRef(ref string) string {
	if ref == "" {
		return "default"
	}
	if strings.HasPrefix(ref, "arn:") {
		parts := strings.Split(ref, "/")
		return parts[len(parts)-1]
	}
	return ref
}

// ecsServiceNameFromRef extracts the service name from a name or ARN.
func ecsServiceNameFromRef(ref string) string {
	if strings.HasPrefix(ref, "arn:") {
		parts := strings.Split(ref, "/")
		return parts[len(parts)-1]
	}
	return ref
}

func ecsServiceKey(cluster, service string) string { return cluster + "/" + service }

// validateECSServiceRegistries enforces the Amazon ECS service-discovery
// coordinate rules that depend on the AWS Cloud Map DNS record type.
func validateECSServiceRegistries(raw json.RawMessage) (string, string) {
	registries, err := ecsServiceRegistries(raw)
	if err != nil {
		return "InvalidParameterException", err.Error()
	}
	for _, registry := range registries {
		serviceID := registry.RegistryArn[strings.LastIndex(registry.RegistryArn, "/")+1:]
		service, found := cmServices.Get(serviceID)
		if !found || service.Arn != registry.RegistryArn {
			return "InvalidParameterException", fmt.Sprintf("Service registry %s does not exist", registry.RegistryArn)
		}
		hasSRVRecord := false
		if service.DnsConfig != nil {
			for _, record := range service.DnsConfig.DnsRecords {
				if record.Type == "SRV" {
					hasSRVRecord = true
					break
				}
			}
		}
		if !hasSRVRecord && (registry.ContainerPort != nil || registry.Port != nil) {
			return "InvalidParameterException", fmt.Sprintf("The values specified for serviceRegistries do not require a value for 'containerPort'. Remove the value and retry. Registry: %s", registry.RegistryArn)
		}
	}
	return "", ""
}

// ecsServiceFromARN resolves a service ARN (arn:...:service/<cluster>/<name>,
// or the older arn:...:service/<name> form) to its stored service.
func ecsServiceFromARN(arn string) (clusterName, key string, svc ECSService, ok bool) {
	idx := strings.Index(arn, ":service/")
	if idx < 0 {
		return "", "", ECSService{}, false
	}
	rest := arn[idx+len(":service/"):]
	if parts := strings.SplitN(rest, "/", 2); len(parts) == 2 {
		clusterName, key = parts[0], ecsServiceKey(parts[0], parts[1])
	} else {
		clusterName, key = "default", ecsServiceKey("default", parts[0])
	}
	svc, ok = ecsServices.Get(key)
	return clusterName, key, svc, ok
}

func handleECSCreateService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster                  string          `json:"cluster"`
		ServiceName              string          `json:"serviceName"`
		TaskDefinition           string          `json:"taskDefinition"`
		DesiredCount             *int            `json:"desiredCount"`
		LaunchType               string          `json:"launchType"`
		PlatformVersion          string          `json:"platformVersion"`
		SchedulingStrategy       string          `json:"schedulingStrategy"`
		Role                     string          `json:"role"`
		PropagateTags            string          `json:"propagateTags"`
		EnableExecuteCommand     bool            `json:"enableExecuteCommand"`
		NetworkConfiguration     json.RawMessage `json:"networkConfiguration"`
		LoadBalancers            json.RawMessage `json:"loadBalancers"`
		ServiceRegistries        json.RawMessage `json:"serviceRegistries"`
		DeploymentController     json.RawMessage `json:"deploymentController"`
		DeploymentConfiguration  json.RawMessage `json:"deploymentConfiguration"`
		CapacityProviderStrategy json.RawMessage `json:"capacityProviderStrategy"`

		EnableECSManagedTags          bool            `json:"enableECSManagedTags"`
		HealthCheckGracePeriodSeconds *int            `json:"healthCheckGracePeriodSeconds"`
		PlacementConstraints          json.RawMessage `json:"placementConstraints"`
		PlacementStrategy             json.RawMessage `json:"placementStrategy"`
		AvailabilityZoneRebalancing   string          `json:"availabilityZoneRebalancing"`
		ServiceConnectConfiguration   json.RawMessage `json:"serviceConnectConfiguration"`
		VolumeConfigurations          json.RawMessage `json:"volumeConfigurations"`
		VpcLatticeConfigurations      json.RawMessage `json:"vpcLatticeConfigurations"`
		Tags                          []ECSTag        `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if code, message := validateECSServiceRegistries(req.ServiceRegistries); code != "" {
		sim.AWSError(w, code, message, http.StatusBadRequest)
		return
	}
	if req.ServiceName == "" {
		sim.AWSError(w, "InvalidParameterException", "serviceName is required", http.StatusBadRequest)
		return
	}
	if req.TaskDefinition == "" {
		sim.AWSError(w, "InvalidParameterException", "taskDefinition is required", http.StatusBadRequest)
		return
	}
	if _, ok := ecsServiceTaskDefinition(req.TaskDefinition); !ok {
		sim.AWSErrorf(w, "ClientException", http.StatusBadRequest,
			"Unable to describe task definition: %s", req.TaskDefinition)
		return
	}
	if code, message := validateECSServiceLoadBalancers(req.LoadBalancers, req.TaskDefinition); code != "" {
		sim.AWSError(w, code, message, http.StatusBadRequest)
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	cluster, ok := ecsClusters.Get(clusterName)
	if !ok {
		sim.AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest,
			"Cluster not found: %s", clusterName)
		return
	}
	key := ecsServiceKey(clusterName, req.ServiceName)
	if existing, ok := ecsServices.Get(key); ok && existing.Status == "ACTIVE" {
		sim.AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"Creating a service named %q in cluster %q is not permitted while a service of the same name already exists.",
			req.ServiceName, clusterName)
		return
	}
	desired := 0
	if req.DesiredCount != nil {
		desired = *req.DesiredCount
	}
	strategy := req.SchedulingStrategy
	if strategy == "" {
		strategy = "REPLICA"
	}
	now := float64(time.Now().Unix())
	svc := ECSService{
		ServiceArn:                    ecsArn("service", clusterName+"/"+req.ServiceName),
		ServiceName:                   req.ServiceName,
		ClusterArn:                    cluster.ClusterArn,
		TaskDefinition:                req.TaskDefinition,
		DesiredCount:                  desired,
		RunningCount:                  0,
		PendingCount:                  0,
		Status:                        "ACTIVE",
		LaunchType:                    req.LaunchType,
		PlatformVersion:               req.PlatformVersion,
		SchedulingStrategy:            strategy,
		RoleArn:                       req.Role,
		PropagateTags:                 req.PropagateTags,
		EnableExecuteCommand:          req.EnableExecuteCommand,
		CreatedAt:                     now,
		NetworkConfiguration:          req.NetworkConfiguration,
		LoadBalancers:                 req.LoadBalancers,
		ServiceRegistries:             req.ServiceRegistries,
		DeploymentController:          req.DeploymentController,
		DeploymentConfiguration:       req.DeploymentConfiguration,
		CapacityProviderStrategy:      req.CapacityProviderStrategy,
		EnableECSManagedTags:          req.EnableECSManagedTags,
		HealthCheckGracePeriodSeconds: req.HealthCheckGracePeriodSeconds,
		PlacementConstraints:          req.PlacementConstraints,
		PlacementStrategy:             req.PlacementStrategy,
		AvailabilityZoneRebalancing:   req.AvailabilityZoneRebalancing,
		ServiceConnectConfiguration:   req.ServiceConnectConfiguration,
		VolumeConfigurations:          req.VolumeConfigurations,
		VpcLatticeConfigurations:      req.VpcLatticeConfigurations,
		Tags:                          req.Tags,
	}
	svc.Deployments = []ECSDeployment{ecsServiceDeployment(svc, now)}
	ecsServices.Put(key, svc)
	ecsBeginServiceDeployment(key, ECSService{}, svc)
	ecsRecordServiceDeployment(svc, ecsServiceConnectNamespace(svc.ServiceConnectConfiguration))
	// CreateService records the requested state and answers with it; placing
	// the tasks is the service scheduler's work. Amazon ECS answers a create
	// for ten tasks with the service at runningCount 0 and a PRIMARY
	// deployment of desiredCount 10 / runningCount 0, and converges it after
	// the response.
	svc, _ = ecsServices.Get(key)
	ecsRequestServiceReconcile(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"service": svc})
}

// ecsServiceConnectNamespace extracts the Cloud Map namespace from a service's
// serviceConnectConfiguration JSON, for ListServicesByNamespace. Returns "" when
// no namespace is configured.
func ecsServiceConnectNamespace(scc []byte) string {
	if len(scc) == 0 {
		return ""
	}
	var cfg struct {
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(scc, &cfg); err != nil {
		return ""
	}
	return cfg.Namespace
}

// ecsServiceDeployment builds the service's single PRIMARY deployment. The
// service-connect / volume / vpc-lattice configs surface here (the SDK Service
// type carries them only inside the deployment record, not at the top level).
func ecsServiceDeployment(svc ECSService, now float64) ECSDeployment {
	// "When a service deployment is started, it begins in an IN_PROGRESS
	// state. When the service reaches a steady state, the deployment
	// transitions to a COMPLETED state." Deciding that from the counts the
	// service carries at the moment the deployment record is built reads the
	// previous revision's tasks: a service already running its desired count
	// would start its next rollout finished. The scheduler completes the
	// rollout once the deployment's own tasks are running and in service.
	return ECSDeployment{
		Id:                          "ecs-svc/" + generateUUID(),
		Status:                      "PRIMARY",
		TaskDefinition:              svc.TaskDefinition,
		DesiredCount:                svc.DesiredCount,
		RunningCount:                svc.RunningCount,
		PendingCount:                svc.PendingCount,
		RolloutState:                "IN_PROGRESS",
		CreatedAt:                   now,
		UpdatedAt:                   now,
		ServiceConnectConfiguration: svc.ServiceConnectConfiguration,
		VolumeConfigurations:        svc.VolumeConfigurations,
		VpcLatticeConfigurations:    svc.VpcLatticeConfigurations,
	}
}

// handleECSDescribeServices reports the service records the control plane
// holds. Counts, deployment rollout state and events are owned by the service
// scheduler, which converges them from real task state on every task lifecycle
// transition, on its stabilization timer while a deployment is in progress, and
// on its launch-retry timer; a read reports that record and never runs a
// scheduler pass of its own.
func handleECSDescribeServices(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster  string   `json:"cluster"`
		Services []string `json:"services"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	if _, ok := ecsClusters.Get(clusterName); !ok {
		sim.AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest,
			"Cluster not found: %s", clusterName)
		return
	}
	services := make([]ECSService, 0, len(req.Services))
	failures := make([]map[string]string, 0)
	for _, ref := range req.Services {
		name := ecsServiceNameFromRef(ref)
		key := ecsServiceKey(clusterName, name)
		if svc, ok := ecsServices.Get(key); ok {
			services = append(services, svc)
		} else {
			failures = append(failures, map[string]string{
				"arn":    ecsArn("service", clusterName+"/"+name),
				"reason": "MISSING",
			})
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"services": services,
		"failures": failures,
	})
}

func handleECSListServices(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster    string `json:"cluster"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	all := make([]string, 0)
	for _, svc := range ecsServices.List() {
		if ecsClusterNameFromRef(svc.ClusterArn) == clusterName && svc.Status == "ACTIVE" {
			all = append(all, svc.ServiceArn)
		}
	}
	sort.Strings(all)
	page, next := awsPage(all, req.NextToken, req.MaxResults, 100)
	out := map[string]any{"serviceArns": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleECSListClusters(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := make([]string, 0)
	for _, c := range ecsClusters.List() {
		all = append(all, c.ClusterArn)
	}
	sort.Strings(all)
	page, next := awsPage(all, req.NextToken, req.MaxResults, 100)
	out := map[string]any{"clusterArns": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleECSListTaskDefinitions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FamilyPrefix string `json:"familyPrefix"`
		Status       string `json:"status"`
		Sort         string `json:"sort"`
		MaxResults   int    `json:"maxResults"`
		NextToken    string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// status selects the lifecycle state; real AWS defaults to ACTIVE (INACTIVE
	// revisions are excluded unless INACTIVE or ALL is requested explicitly).
	status := strings.ToUpper(req.Status)
	if status == "" {
		status = "ACTIVE"
	}
	type tdRef struct {
		arn      string
		family   string
		revision int
	}
	var defs []tdRef
	for _, td := range ecsTaskDefinitions.List() {
		if req.FamilyPrefix != "" && !strings.HasPrefix(td.Family, req.FamilyPrefix) {
			continue
		}
		if status != "ALL" {
			tdStatus := td.Status
			if tdStatus == "" {
				tdStatus = "ACTIVE"
			}
			if !strings.EqualFold(tdStatus, status) {
				continue
			}
		}
		defs = append(defs, tdRef{td.TaskDefinitionArn, td.Family, td.Revision})
	}
	// Order by family, then revision ascending (numeric, not lexical — so :10
	// sorts after :2); sort=DESC returns newest-revision-first.
	sort.Slice(defs, func(i, j int) bool {
		if defs[i].family != defs[j].family {
			return defs[i].family < defs[j].family
		}
		return defs[i].revision < defs[j].revision
	})
	if strings.EqualFold(req.Sort, "DESC") {
		for i, j := 0, len(defs)-1; i < j; i, j = i+1, j-1 {
			defs[i], defs[j] = defs[j], defs[i]
		}
	}
	all := make([]string, 0, len(defs))
	for _, d := range defs {
		all = append(all, d.arn)
	}
	page, next := awsPage(all, req.NextToken, req.MaxResults, 100)
	out := map[string]any{"taskDefinitionArns": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleECSUpdateService(w http.ResponseWriter, r *http.Request) {
	// UpdateService accepts nearly the whole service surface; the provider sends
	// every changed attribute here. Anything not persisted drifts on the next
	// plan, so each updatable field below is read and written.
	var req struct {
		Cluster                       string          `json:"cluster"`
		Service                       string          `json:"service"`
		TaskDefinition                string          `json:"taskDefinition"`
		DesiredCount                  *int            `json:"desiredCount"`
		NetworkConfiguration          json.RawMessage `json:"networkConfiguration"`
		EnableExecuteCommand          *bool           `json:"enableExecuteCommand"`
		EnableECSManagedTags          *bool           `json:"enableECSManagedTags"`
		PlatformVersion               string          `json:"platformVersion"`
		PropagateTags                 string          `json:"propagateTags"`
		HealthCheckGracePeriodSeconds *int            `json:"healthCheckGracePeriodSeconds"`
		AvailabilityZoneRebalancing   string          `json:"availabilityZoneRebalancing"`
		DeploymentConfiguration       json.RawMessage `json:"deploymentConfiguration"`
		CapacityProviderStrategy      json.RawMessage `json:"capacityProviderStrategy"`
		PlacementConstraints          json.RawMessage `json:"placementConstraints"`
		PlacementStrategy             json.RawMessage `json:"placementStrategy"`
		LoadBalancers                 json.RawMessage `json:"loadBalancers"`
		ServiceRegistries             json.RawMessage `json:"serviceRegistries"`
		ServiceConnectConfiguration   json.RawMessage `json:"serviceConnectConfiguration"`
		VolumeConfigurations          json.RawMessage `json:"volumeConfigurations"`
		VpcLatticeConfigurations      json.RawMessage `json:"vpcLatticeConfigurations"`
		ForceNewDeployment            bool            `json:"forceNewDeployment"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if code, message := validateECSServiceRegistries(req.ServiceRegistries); code != "" {
		sim.AWSError(w, code, message, http.StatusBadRequest)
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	key := ecsServiceKey(clusterName, ecsServiceNameFromRef(req.Service))
	svc, ok := ecsServices.Get(key)
	if !ok {
		sim.AWSErrorf(w, "ServiceNotFoundException", http.StatusBadRequest,
			"Service not found: %s", req.Service)
		return
	}
	previousService := svc
	targetTaskDefinition := svc.TaskDefinition
	if req.TaskDefinition != "" {
		targetTaskDefinition = req.TaskDefinition
	}
	targetLoadBalancers := svc.LoadBalancers
	if req.LoadBalancers != nil {
		targetLoadBalancers = req.LoadBalancers
	}
	if code, message := validateECSServiceLoadBalancers(targetLoadBalancers, targetTaskDefinition); code != "" {
		sim.AWSError(w, code, message, http.StatusBadRequest)
		return
	}
	deploymentRequired := req.ForceNewDeployment ||
		(req.TaskDefinition != "" && req.TaskDefinition != svc.TaskDefinition) ||
		req.NetworkConfiguration != nil ||
		req.LoadBalancers != nil ||
		req.PlatformVersion != "" ||
		req.CapacityProviderStrategy != nil ||
		req.ServiceConnectConfiguration != nil ||
		req.VolumeConfigurations != nil ||
		req.VpcLatticeConfigurations != nil
	if req.TaskDefinition != "" {
		svc.TaskDefinition = req.TaskDefinition
	}
	if req.DesiredCount != nil {
		svc.DesiredCount = *req.DesiredCount
	}
	if req.NetworkConfiguration != nil {
		svc.NetworkConfiguration = req.NetworkConfiguration
	}
	if req.EnableExecuteCommand != nil {
		svc.EnableExecuteCommand = *req.EnableExecuteCommand
	}
	if req.EnableECSManagedTags != nil {
		svc.EnableECSManagedTags = *req.EnableECSManagedTags
	}
	if req.PlatformVersion != "" {
		svc.PlatformVersion = req.PlatformVersion
	}
	if req.PropagateTags != "" {
		svc.PropagateTags = req.PropagateTags
	}
	if req.HealthCheckGracePeriodSeconds != nil {
		svc.HealthCheckGracePeriodSeconds = req.HealthCheckGracePeriodSeconds
	}
	if req.AvailabilityZoneRebalancing != "" {
		svc.AvailabilityZoneRebalancing = req.AvailabilityZoneRebalancing
	}
	if req.DeploymentConfiguration != nil {
		svc.DeploymentConfiguration = req.DeploymentConfiguration
	}
	if req.CapacityProviderStrategy != nil {
		svc.CapacityProviderStrategy = req.CapacityProviderStrategy
	}
	if req.PlacementConstraints != nil {
		svc.PlacementConstraints = req.PlacementConstraints
	}
	if req.PlacementStrategy != nil {
		svc.PlacementStrategy = req.PlacementStrategy
	}
	if req.LoadBalancers != nil {
		svc.LoadBalancers = req.LoadBalancers
	}
	if req.ServiceRegistries != nil {
		ecsDeregisterServiceRegistryTasks(svc)
		svc.ServiceRegistries = req.ServiceRegistries
	}
	if req.ServiceConnectConfiguration != nil {
		svc.ServiceConnectConfiguration = req.ServiceConnectConfiguration
	}
	if req.VolumeConfigurations != nil {
		svc.VolumeConfigurations = req.VolumeConfigurations
	}
	if req.VpcLatticeConfigurations != nil {
		svc.VpcLatticeConfigurations = req.VpcLatticeConfigurations
	}
	now := float64(time.Now().Unix())
	if deploymentRequired {
		svc.Deployments = []ECSDeployment{ecsServiceDeployment(svc, now)}
	} else {
		ecsUpdatePrimaryDeploymentCounts(&svc, now)
	}
	ecsServices.Put(key, svc)
	if deploymentRequired {
		ecsBeginServiceDeployment(key, previousService, svc)
		ecsRecordServiceDeployment(svc, ecsServiceConnectNamespace(svc.ServiceConnectConfiguration))
	}
	// The update is recorded and answered with; applying it is the service
	// scheduler's work, which starts new tasks and replaces running ones under
	// the deployment configuration's minimumHealthyPercent and maximumPercent.
	svc, _ = ecsServices.Get(key)
	ecsRequestServiceReconcile(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"service": svc})
}

func ecsUpdatePrimaryDeploymentCounts(service *ECSService, now float64) {
	if len(service.Deployments) == 0 {
		service.Deployments = []ECSDeployment{ecsServiceDeployment(*service, now)}
		return
	}
	deployment := &service.Deployments[0]
	deployment.DesiredCount = service.DesiredCount
	deployment.RunningCount = service.RunningCount
	deployment.PendingCount = service.PendingCount
	deployment.UpdatedAt = now
	if deployment.RunningCount == deployment.DesiredCount && deployment.PendingCount == 0 {
		deployment.RolloutState = "COMPLETED"
		deployment.RolloutStateReason = ""
	} else if deployment.RolloutState != "FAILED" {
		deployment.RolloutState = "IN_PROGRESS"
		deployment.RolloutStateReason = ""
	}
}

func handleECSDeleteService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string `json:"cluster"`
		Service string `json:"service"`
		Force   bool   `json:"force"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	key := ecsServiceKey(clusterName, ecsServiceNameFromRef(req.Service))
	_, ok := ecsServices.Get(key)
	if !ok {
		sim.AWSErrorf(w, "ServiceNotFoundException", http.StatusBadRequest,
			"Service not found: %s", req.Service)
		return
	}
	lock := ecsServiceLock(key)
	lock.Lock()
	defer lock.Unlock()
	svc, ok := ecsServices.Get(key)
	if !ok {
		sim.AWSErrorf(w, "ServiceNotFoundException", http.StatusBadRequest,
			"Service not found: %s", req.Service)
		return
	}
	// Without force, Amazon ECS refuses to delete a service that is still
	// scaled up: the caller must scale it to zero first. The flag had been
	// parsed and ignored, so a delete that AWS rejects succeeded here and a
	// caller relying on the rejection — the usual scale-down-then-delete
	// sequence — was never told it had skipped a step.
	if !req.Force && svc.DesiredCount > 0 {
		sim.AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"The service cannot be stopped while it is scaled above 0.")
		return
	}

	// Real ECS drains then marks the service INACTIVE; DescribeServices keeps
	// returning it as INACTIVE, which is how terraform-provider-aws confirms the
	// delete converged. Persist INACTIVE before stopping tasks so their
	// lifecycle callbacks cannot race the delete and launch a replacement.
	svc.Status = "INACTIVE"
	svc.DesiredCount = 0
	svc.RunningCount = 0
	svc.PendingCount = 0
	ecsServices.Put(key, svc)
	ecsCancelServiceStabilization(key)
	ecsStopServiceTasks(svc)
	ecsSyncServiceLoadBalancerTargets(svc)
	ecsDeregisterServiceRegistryTasks(svc)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"service": svc})
}

func handleECSPutClusterCapacityProviders(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster                         string          `json:"cluster"`
		CapacityProviders               []string        `json:"capacityProviders"`
		DefaultCapacityProviderStrategy json.RawMessage `json:"defaultCapacityProviderStrategy"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	cluster, ok := ecsClusters.Get(clusterName)
	if !ok {
		sim.AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest,
			"Cluster not found: %s", clusterName)
		return
	}
	if req.CapacityProviders == nil {
		req.CapacityProviders = []string{}
	}
	cluster.CapacityProviders = req.CapacityProviders
	cluster.DefaultCapacityProviderStrategy = req.DefaultCapacityProviderStrategy
	ecsClusters.Put(clusterName, cluster)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"cluster": cluster})
}
