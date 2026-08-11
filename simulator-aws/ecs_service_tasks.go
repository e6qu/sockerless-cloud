package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Amazon ECS services own long-lived tasks. Reconciliation uses the same
// runECSTasks path as RunTask, so service tasks receive the same task-definition
// validation, networking, volumes, container runtime, logs, and durable state.

var (
	ecsServiceReconcileLocks  sync.Map // map[service store key]*sync.Mutex
	ecsServiceStabilityTimers sync.Map // map[service store key]*time.Timer
)

type ecsServiceLoadBalancer struct {
	TargetGroupArn string `json:"targetGroupArn"`
	ContainerName  string `json:"containerName"`
	ContainerPort  int    `json:"containerPort"`
}

type ecsServiceRegistry struct {
	RegistryArn   string `json:"registryArn"`
	ContainerName string `json:"containerName"`
	ContainerPort *int   `json:"containerPort"`
	Port          *int   `json:"port"`
}

func ecsServiceRegistries(raw json.RawMessage) ([]ecsServiceRegistry, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var registries []ecsServiceRegistry
	if err := json.Unmarshal(raw, &registries); err != nil {
		return nil, fmt.Errorf("serviceRegistries must be an array: %w", err)
	}
	return registries, nil
}

// ecsServiceTaskGroup is the Group tag Amazon ECS assigns to a task started for
// a service: "service:<service-name>".
func ecsServiceTaskGroup(serviceName string) string {
	return "service:" + serviceName
}

// ecsServiceRunningTasks returns the service's non-STOPPED tasks.
func ecsServiceRunningTasks(clusterArn, group string) []ECSTask {
	tasks := ecsServiceTasksForGroup(clusterArn, group)
	out := tasks[:0]
	for _, task := range tasks {
		if task.LastStatus != ECSTaskStatusStopped {
			out = append(out, task)
		}
	}
	return out
}

// ecsServiceTasksForGroup enumerates every task in the cluster whose Group
// matches the service's service:<name> tag, including STOPPED tasks.
func ecsServiceTasksForGroup(clusterArn, group string) []ECSTask {
	if group == "" {
		return nil
	}
	return ecsTasks.Filter(func(task ECSTask) bool {
		return task.ClusterArn == clusterArn && task.Group == group
	})
}

// ecsRequestServiceReconcile schedules an event-driven reconciliation. The
// per-service lock makes concurrent task transitions and API updates collapse
// onto the newest persisted service state without launching duplicate tasks.
func ecsRequestServiceReconcile(key string) {
	if key == "" {
		return
	}
	go ecsReconcileService(key)
}

func ecsServiceLock(key string) *sync.Mutex {
	lock, _ := ecsServiceReconcileLocks.LoadOrStore(key, &sync.Mutex{})
	mutex, ok := lock.(*sync.Mutex)
	if !ok {
		panic("Amazon ECS service lock contained a non-mutex value")
	}
	return mutex
}

// ecsContinueServiceStabilization keeps one readiness reconciliation pending
// while the primary deployment is still converging. Elastic Load Balancing
// target health can change without an Amazon ECS task transition, so task
// events alone cannot complete a service deployment.
func ecsContinueServiceStabilization(key string) {
	service, ok := ecsServices.Get(key)
	if !ok || service.Status != "ACTIVE" || !ecsServiceUsesECSScheduler(service) ||
		len(service.Deployments) == 0 ||
		service.Deployments[0].RolloutState != "IN_PROGRESS" {
		ecsCancelServiceStabilization(key)
		return
	}
	timer := time.AfterFunc(ecsServiceSteadyStateWindow, func() {
		ecsServiceStabilityTimers.Delete(key)
		ecsRequestServiceReconcile(key)
	})
	if _, loaded := ecsServiceStabilityTimers.LoadOrStore(key, timer); loaded {
		timer.Stop()
	}
}

func ecsCancelServiceStabilization(key string) {
	value, ok := ecsServiceStabilityTimers.LoadAndDelete(key)
	if !ok {
		return
	}
	if timer, timerOK := value.(*time.Timer); timerOK {
		timer.Stop()
	}
}

// recoverECSServiceTasks reconciles durable services after persisted tasks have
// been adopted. It never manufactures recovery state: the service and task
// stores remain the source of truth.
func recoverECSServiceTasks() {
	for _, service := range ecsServices.List() {
		if service.Status != "ACTIVE" {
			continue
		}
		ecsRequestServiceReconcile(ecsServiceStoreKey(service))
	}
}

func ecsServiceStoreKey(service ECSService) string {
	return ecsServiceKey(ecsClusterNameFromRef(service.ClusterArn), service.ServiceName)
}

func ecsRequestServiceReconcileForTask(task ECSTask) {
	const prefix = "service:"
	if !strings.HasPrefix(task.Group, prefix) {
		return
	}
	ecsRecordServiceTaskFailure(task)
	serviceName := strings.TrimPrefix(task.Group, prefix)
	key := ecsServiceKey(
		ecsClusterNameFromRef(task.ClusterArn),
		serviceName,
	)
	ecsRequestServiceReconcile(key)
	if task.LastStatus == ECSTaskStatusRunning && task.StartedAt != nil {
		delay := time.Until(time.Unix(*task.StartedAt, 0).Add(ecsServiceSteadyStateWindow))
		if delay > 0 {
			time.AfterFunc(delay, func() { ecsRequestServiceReconcile(key) })
		}
	}
}

func ecsServiceTaskDefinition(ref string) (ECSTaskDefinition, bool) {
	key := ref
	if strings.HasPrefix(key, "arn:") {
		if slash := strings.LastIndexByte(key, '/'); slash >= 0 {
			key = key[slash+1:]
		}
	}
	if !strings.Contains(key, ":") {
		ecsRevisionMu.Lock()
		revision, ok := ecsRevisions[key]
		ecsRevisionMu.Unlock()
		if !ok {
			return ECSTaskDefinition{}, false
		}
		key = fmt.Sprintf("%s:%d", key, revision)
	}
	definition, ok := ecsTaskDefinitions.Get(key)
	return definition, ok
}

func ecsServiceTaskTargetCount(service ECSService) int {
	if !strings.EqualFold(service.SchedulingStrategy, "DAEMON") {
		return service.DesiredCount
	}
	count := 0
	for _, instance := range ecsContainerInstances.List() {
		if instance.ClusterName == ecsClusterNameFromRef(service.ClusterArn) &&
			instance.Status == "ACTIVE" && instance.AgentConnected {
			count++
		}
	}
	return count
}

type ecsServiceDeploymentConfiguration struct {
	MaximumPercent        *int `json:"maximumPercent"`
	MinimumHealthyPercent *int `json:"minimumHealthyPercent"`
}

func ecsServiceDeploymentBounds(service ECSService, desired int) (minimumHealthy, maximumActive int) {
	maximumPercent, minimumHealthyPercent := 200, 100
	if strings.EqualFold(service.SchedulingStrategy, "DAEMON") {
		maximumPercent, minimumHealthyPercent = 100, 0
	}
	if len(service.DeploymentConfiguration) > 0 &&
		string(service.DeploymentConfiguration) != "null" {
		var requested ecsServiceDeploymentConfiguration
		if json.Unmarshal(service.DeploymentConfiguration, &requested) == nil {
			if requested.MaximumPercent != nil {
				maximumPercent = *requested.MaximumPercent
			}
			if requested.MinimumHealthyPercent != nil {
				minimumHealthyPercent = *requested.MinimumHealthyPercent
			}
		}
	}
	minimumHealthy = (desired*minimumHealthyPercent + 99) / 100
	maximumActive = desired * maximumPercent / 100
	if desired > 0 && maximumActive < 1 {
		maximumActive = 1
	}
	return minimumHealthy, maximumActive
}

func ecsServiceUsesECSScheduler(service ECSService) bool {
	if len(service.DeploymentController) == 0 ||
		string(service.DeploymentController) == "null" {
		return true
	}
	var controller struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(service.DeploymentController, &controller); err != nil {
		return false
	}
	return controller.Type == "" || strings.EqualFold(controller.Type, "ECS")
}

func ecsServiceTaskProtected(task ECSTask) bool {
	protection, ok := ecsTaskProtections.Get(task.TaskID())
	if !ok || !protection.ProtectionEnabled {
		return false
	}
	if protection.ExpirationDate != nil &&
		time.Now().Unix() >= int64(*protection.ExpirationDate) {
		ecsTaskProtections.Delete(task.TaskID())
		return false
	}
	return true
}

func ecsServiceTaskTags(service ECSService) []ECSTag {
	var tags []ECSTag
	if strings.EqualFold(service.PropagateTags, "SERVICE") {
		tags = append(tags, service.Tags...)
	}
	if service.EnableECSManagedTags {
		tags = append(tags,
			ECSTag{Key: "aws:ecs:clusterName", Value: ecsClusterNameFromRef(service.ClusterArn)},
			ECSTag{Key: "aws:ecs:serviceName", Value: service.ServiceName},
		)
	}
	return tags
}

func ecsServiceRunInput(service ECSService, count int) (ecsRunTaskInput, error) {
	var network *ECSTaskNetworkConfig
	if len(service.NetworkConfiguration) > 0 &&
		string(service.NetworkConfiguration) != "null" {
		network = &ECSTaskNetworkConfig{}
		if err := json.Unmarshal(service.NetworkConfiguration, network); err != nil {
			return ecsRunTaskInput{}, fmt.Errorf("decode service network configuration: %w", err)
		}
	}
	var volumes []ECSTaskVolumeConfiguration
	if len(service.VolumeConfigurations) > 0 &&
		string(service.VolumeConfigurations) != "null" {
		if err := json.Unmarshal(service.VolumeConfigurations, &volumes); err != nil {
			return ecsRunTaskInput{}, fmt.Errorf("decode service volume configurations: %w", err)
		}
	}
	startedBy := "ecs-svc/" + service.ServiceName
	if len(service.Deployments) > 0 && service.Deployments[0].Id != "" {
		startedBy = service.Deployments[0].Id
	}
	return ecsRunTaskInput{
		Cluster:              service.ClusterArn,
		TaskDefinition:       service.TaskDefinition,
		Count:                count,
		Group:                ecsServiceTaskGroup(service.ServiceName),
		LaunchType:           service.LaunchType,
		Tags:                 ecsServiceTaskTags(service),
		PropagateTags:        service.PropagateTags,
		EnableExecuteCommand: service.EnableExecuteCommand,
		VolumeConfigurations: volumes,
		NetworkConfiguration: network,
		StartedBy:            startedBy,
	}, nil
}

// ecsReconcileService converges one service from persisted service/task state.
// It implements the default rolling policy: launch the complete new revision
// before draining the previous revision, matching minimumHealthyPercent=100
// and maximumPercent=200.
func ecsReconcileService(key string) {
	lock := ecsServiceLock(key)
	lock.Lock()
	defer func() {
		lock.Unlock()
		ecsContinueServiceStabilization(key)
	}()

	service, ok := ecsServices.Get(key)
	if !ok || service.Status != "ACTIVE" {
		return
	}
	if ecsCheckServiceDeploymentAlarms(key, service) {
		return
	}
	if !ecsServiceRetryReady(key) {
		return
	}
	if !ecsServiceUsesECSScheduler(service) {
		ecsRefreshServiceState(key)
		return
	}
	definition, ok := ecsServiceTaskDefinition(service.TaskDefinition)
	if !ok {
		ecsRecordServiceLaunchFailure(key, "Task definition not found: "+service.TaskDefinition)
		return
	}
	targetCount := ecsServiceTaskTargetCount(service)
	minimumHealthy, maximumActive := ecsServiceDeploymentBounds(service, targetCount)
	group := ecsServiceTaskGroup(service.ServiceName)
	active := ecsServiceRunningTasks(service.ClusterArn, group)
	var current, previous []ECSTask
	for _, task := range active {
		if task.TaskDefinitionArn == definition.TaskDefinitionArn {
			current = append(current, task)
		} else {
			previous = append(previous, task)
		}
	}

	// When the configured maximum does not leave room for a replacement, stop
	// only as many old tasks as the configured minimum healthy count permits.
	// This opens capacity for the next real deployment batch.
	if len(current) < targetCount {
		launchCount := targetCount - len(current)
		available := maximumActive - len(active)
		if available < launchCount {
			launchCount = available
		}
		if launchCount <= 0 && len(previous) > 0 {
			stoppable := ecsCountServiceTasks(active, ECSTaskStatusRunning) - minimumHealthy
			if stoppable > len(previous) {
				stoppable = len(previous)
			}
			if stoppable > 0 {
				ecsStopServiceTaskSlice(previous, stoppable, "Service deployment replaced task")
				active = ecsServiceRunningTasks(service.ClusterArn, group)
				available = maximumActive - len(active)
				launchCount = targetCount - len(current)
				if available < launchCount {
					launchCount = available
				}
			}
		}
		if launchCount > 0 {
			input, err := ecsServiceRunInput(service, launchCount)
			if err != nil {
				ecsRecordServiceLaunchFailure(key, err.Error())
				return
			}
			for input.Count > 0 {
				count := input.Count
				if count > 10 {
					count = 10
				}
				next := input
				next.Count = count
				if _, requestErr := runECSTasks(context.Background(), next); requestErr != nil {
					ecsRecordServiceLaunchFailure(key, requestErr.message)
					fmt.Fprintf(os.Stderr, "[sim-ecs] service %s reconciliation failed: %s\n",
						service.ServiceArn, requestErr.message)
					return
				}
				input.Count -= count
			}
		}
		active = ecsServiceRunningTasks(service.ClusterArn, group)
		current = nil
		previous = nil
		for _, task := range active {
			if task.TaskDefinitionArn == definition.TaskDefinitionArn {
				current = append(current, task)
			} else {
				previous = append(previous, task)
			}
		}
	}

	if err := ecsSyncServiceRegistryInstances(service); err != nil {
		ecsRecordServiceLaunchFailure(key, err.Error())
		return
	}
	ecsSyncServiceLoadBalancerTargets(service)
	currentHealthy := ecsCountHealthyServiceTasks(service, current)
	if currentHealthy >= targetCount {
		ecsStopServiceTaskSlice(previous, len(previous), "Service deployment replaced task")
	}
	if len(current) > targetCount {
		ecsStopServiceTaskSlice(current, len(current)-targetCount, "Service scheduler reduced desired count")
	}

	ecsRefreshServiceState(key)
}

func ecsCountServiceTasks(tasks []ECSTask, status ECSTaskStatus) int {
	count := 0
	for _, task := range tasks {
		if task.LastStatus == status {
			count++
		}
	}
	return count
}

func ecsCountHealthyServiceTasks(service ECSService, tasks []ECSTask) int {
	count := 0
	for _, task := range tasks {
		if task.LastStatus == ECSTaskStatusRunning && ecsServiceTaskHealthy(service, task) {
			count++
		}
	}
	return count
}

func ecsServiceTaskHealthy(service ECSService, task ECSTask) bool {
	if task.StartedAt == nil ||
		time.Since(time.Unix(*task.StartedAt, 0)) < ecsServiceSteadyStateWindow {
		return false
	}
	bindings, err := ecsServiceLoadBalancers(service.LoadBalancers)
	if err != nil || len(bindings) == 0 {
		return err == nil
	}
	for _, binding := range bindings {
		targetGroup, ok := elbv2TargetGroups.Get(binding.TargetGroupArn)
		if !ok {
			return false
		}
		target, ok := ecsServiceLoadBalancerTarget(task, binding, targetGroup)
		if !ok || elbv2ProbeTarget(context.Background(), targetGroup, target) != "healthy" {
			return false
		}
	}
	return true
}

func ecsStopServiceTaskSlice(tasks []ECSTask, count int, reason string) {
	sort.Slice(tasks, func(i, j int) bool {
		left, right := float64(0), float64(0)
		if tasks[i].CreatedAt != nil {
			left = *tasks[i].CreatedAt
		}
		if tasks[j].CreatedAt != nil {
			right = *tasks[j].CreatedAt
		}
		return left > right
	})
	for _, task := range tasks {
		if count == 0 {
			return
		}
		if ecsServiceTaskProtected(task) {
			continue
		}
		if stopECSTask(task.TaskID(), reason, "ServiceSchedulerInitiated") {
			count--
		}
	}
}

// ecsRefreshServiceState derives public counts and deployment state from real
// service tasks, then synchronizes Elastic Load Balancing target registration.
func ecsRefreshServiceState(key string) {
	service, ok := ecsServices.Get(key)
	if !ok {
		return
	}
	if err := ecsSyncServiceRegistryInstances(service); err != nil {
		ecsRecordServiceLaunchFailure(key, err.Error())
		return
	}
	ecsSyncServiceLoadBalancerTargets(service)
	definition, _ := ecsServiceTaskDefinition(service.TaskDefinition)
	tasks := ecsServiceRunningTasks(service.ClusterArn, ecsServiceTaskGroup(service.ServiceName))
	running, pending, currentRunning, currentHealthy := 0, 0, 0, 0
	oldActive := false
	for _, task := range tasks {
		switch task.LastStatus {
		case ECSTaskStatusRunning:
			running++
		default:
			pending++
		}
		if task.TaskDefinitionArn == definition.TaskDefinitionArn {
			if task.LastStatus == ECSTaskStatusRunning {
				currentRunning++
				if ecsServiceTaskHealthy(service, task) {
					currentHealthy++
				}
			}
		} else {
			oldActive = true
		}
	}
	targetCount := ecsServiceTaskTargetCount(service)
	now := float64(time.Now().Unix())
	if currentHealthy > 0 {
		ecsResetServiceFailureCountAfterHealthyTask(key, service)
	}
	ecsServices.Update(key, func(current *ECSService) {
		current.RunningCount = running
		current.PendingCount = pending
		if len(current.Deployments) == 0 {
			current.Deployments = []ECSDeployment{ecsServiceDeployment(*current, now)}
		}
		deployment := &current.Deployments[0]
		deployment.DesiredCount = targetCount
		deployment.RunningCount = currentRunning
		deployment.PendingCount = pending
		deployment.UpdatedAt = now
		if currentRunning == targetCount && currentHealthy == targetCount && pending == 0 && !oldActive {
			deployment.RolloutState = "COMPLETED"
			deployment.RolloutStateReason = ""
			ecsCompleteServiceDeployments(current.ServiceArn, now)
		} else if deployment.RolloutState != "FAILED" {
			deployment.RolloutState = "IN_PROGRESS"
			deployment.RolloutStateReason = ""
		}
	})
	if completed, ok := ecsServices.Get(key); ok && ecsServiceDeploymentCompleted(completed) {
		ecsMarkServiceDeploymentCompleted(key, completed)
	}
}

// ecsStopServiceTasks drains every non-STOPPED task for a service.
func ecsStopServiceTasks(service ECSService) {
	group := ecsServiceTaskGroup(service.ServiceName)
	for _, task := range ecsServiceRunningTasks(service.ClusterArn, group) {
		stopECSTask(task.TaskID(), "Service deleted", "ServiceSchedulerInitiated")
	}
	ecsSyncServiceLoadBalancerTargets(service)
	_ = ecsSyncServiceRegistryInstances(service)
}

func ecsServiceRegistryID(arn string) string {
	if slash := strings.LastIndexByte(arn, '/'); slash >= 0 {
		return arn[slash+1:]
	}
	return arn
}

func ecsServiceRegistryTaskAttributes(
	service ECSService,
	task ECSTask,
	registry ecsServiceRegistry,
) (map[string]string, bool) {
	var privateIPv4 string
	for _, container := range task.Containers {
		if registry.ContainerName != "" && container.Name != registry.ContainerName {
			continue
		}
		if len(container.NetworkInterfaces) > 0 {
			privateIPv4 = container.NetworkInterfaces[0].PrivateIpv4Address
			break
		}
	}
	if privateIPv4 == "" {
		for _, container := range task.Containers {
			if len(container.NetworkInterfaces) > 0 {
				privateIPv4 = container.NetworkInterfaces[0].PrivateIpv4Address
				break
			}
		}
	}
	if privateIPv4 == "" {
		return nil, false
	}
	availabilityZone := awsAvailabilityZone()
	for _, attachment := range task.Attachments {
		for _, detail := range attachment.Details {
			if detail.Name != "subnetId" {
				continue
			}
			if subnet, ok := ec2Subnets.Get(detail.Value); ok && subnet.AvailabilityZone != "" {
				availabilityZone = subnet.AvailabilityZone
			}
		}
	}
	family, revision := ecsTaskDefinitionFamilyRevision(task.TaskDefinitionArn)
	attributes := map[string]string{
		"AWS_INSTANCE_IPV4":            privateIPv4,
		"AVAILABILITY_ZONE":            availabilityZone,
		"REGION":                       awsRegion(),
		"ECS_SERVICE_NAME":             service.ServiceName,
		"ECS_CLUSTER_NAME":             ecsClusterNameFromRef(service.ClusterArn),
		"ECS_TASK_DEFINITION_FAMILY":   family,
		"ECS_TASK_DEFINITION_REVISION": revision,
	}
	port := registry.Port
	if port == nil {
		port = registry.ContainerPort
	}
	if port != nil {
		attributes["AWS_INSTANCE_PORT"] = strconv.Itoa(*port)
	}
	return attributes, true
}

func ecsCloudMapJSONCall(handler http.HandlerFunc, input any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/x-amz-json-1.1")
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code >= http.StatusBadRequest {
		code, message := awsJSONError(response.Body.Bytes())
		if code == "" {
			code = http.StatusText(response.Code)
		}
		return fmt.Errorf("AWS Cloud Map %s: %s", code, message)
	}
	return nil
}

func ecsStringMapEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

// ecsSyncServiceRegistryInstances makes each running service task a real AWS
// Cloud Map instance and removes registrations for tasks that stopped. The
// public RegisterInstance and DeregisterInstance handlers perform the durable
// write, revision update, operation creation, and DNS realization.
func ecsSyncServiceRegistryInstances(service ECSService) error {
	registries, err := ecsServiceRegistries(service.ServiceRegistries)
	if err != nil {
		return err
	}
	if len(registries) == 0 {
		return nil
	}
	tasks := ecsServiceTasksForGroup(service.ClusterArn, ecsServiceTaskGroup(service.ServiceName))
	clusterName := ecsClusterNameFromRef(service.ClusterArn)
	for _, registry := range registries {
		serviceID := ecsServiceRegistryID(registry.RegistryArn)
		desired := map[string]map[string]string{}
		for _, task := range tasks {
			if task.LastStatus != ECSTaskStatusRunning {
				continue
			}
			attributes, ok := ecsServiceRegistryTaskAttributes(service, task, registry)
			if ok {
				desired[task.TaskID()] = attributes
			}
		}
		for _, instance := range cmServiceInstances(serviceID) {
			if desired[instance.Id] != nil ||
				instance.Attributes["ECS_SERVICE_NAME"] != service.ServiceName ||
				instance.Attributes["ECS_CLUSTER_NAME"] != clusterName {
				continue
			}
			if err := ecsCloudMapJSONCall(handleCMDeregisterInstance, map[string]any{
				"ServiceId": serviceID, "InstanceId": instance.Id,
			}); err != nil {
				return err
			}
		}
		for instanceID, attributes := range desired {
			existing, exists := cmInstances.Get(cmInstanceKey(serviceID, instanceID))
			if exists && ecsStringMapEqual(existing.Attributes, attributes) {
				continue
			}
			if err := ecsCloudMapJSONCall(handleCMRegisterInstance, map[string]any{
				"ServiceId":  serviceID,
				"InstanceId": instanceID,
				"Attributes": attributes,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func ecsDeregisterServiceRegistryTasks(service ECSService) {
	registries, err := ecsServiceRegistries(service.ServiceRegistries)
	if err != nil {
		return
	}
	clusterName := ecsClusterNameFromRef(service.ClusterArn)
	for _, registry := range registries {
		serviceID := ecsServiceRegistryID(registry.RegistryArn)
		for _, instance := range cmServiceInstances(serviceID) {
			if instance.Attributes["ECS_SERVICE_NAME"] != service.ServiceName ||
				instance.Attributes["ECS_CLUSTER_NAME"] != clusterName {
				continue
			}
			_ = ecsCloudMapJSONCall(handleCMDeregisterInstance, map[string]any{
				"ServiceId": serviceID, "InstanceId": instance.Id,
			})
		}
	}
}

func ecsServiceLoadBalancers(raw json.RawMessage) ([]ecsServiceLoadBalancer, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var bindings []ecsServiceLoadBalancer
	if err := json.Unmarshal(raw, &bindings); err != nil {
		return nil, fmt.Errorf("loadBalancers must be an array: %w", err)
	}
	return bindings, nil
}

func validateECSServiceLoadBalancers(raw json.RawMessage, taskDefinition string) (string, string) {
	bindings, err := ecsServiceLoadBalancers(raw)
	if err != nil {
		return "InvalidParameterException", err.Error()
	}
	definition, ok := ecsServiceTaskDefinition(taskDefinition)
	if len(bindings) > 0 && !ok {
		return "ClientException", "Unable to describe task definition: " + taskDefinition
	}
	for _, binding := range bindings {
		targetGroup, ok := elbv2TargetGroups.Get(binding.TargetGroupArn)
		if !ok {
			return "InvalidParameterException", "Target group not found: " + binding.TargetGroupArn
		}
		if ecsEffectiveNetworkMode(definition) == ecsNetworkModeAwsvpc &&
			!strings.EqualFold(targetGroup.TargetType, "ip") {
			return "InvalidParameterException",
				"Services with tasks that use the awsvpc network mode require a target group with target type ip."
		}
		found := false
		for _, container := range definition.ContainerDefinitions {
			if container.Name != binding.ContainerName {
				continue
			}
			for _, mapping := range container.PortMappings {
				if mapping.ContainerPort == binding.ContainerPort {
					found = true
					break
				}
			}
		}
		if !found {
			return "InvalidParameterException", fmt.Sprintf(
				"The container %s does not have a container port %d defined.",
				binding.ContainerName, binding.ContainerPort,
			)
		}
	}
	return "", ""
}

func ecsSyncServiceLoadBalancerTargets(service ECSService) {
	bindings, err := ecsServiceLoadBalancers(service.LoadBalancers)
	if err != nil {
		return
	}
	allTasks := ecsServiceTasksForGroup(
		service.ClusterArn,
		ecsServiceTaskGroup(service.ServiceName),
	)
	for _, binding := range bindings {
		targetGroup, ok := elbv2TargetGroups.Get(binding.TargetGroupArn)
		if !ok {
			continue
		}
		managed := map[string]bool{}
		desired := map[string]ELBv2TargetDescription{}
		for _, task := range allTasks {
			target, ok := ecsServiceLoadBalancerTarget(task, binding, targetGroup)
			if !ok {
				continue
			}
			key := ecsServiceTargetKey(target)
			managed[key] = true
			if task.LastStatus == ECSTaskStatusRunning {
				desired[key] = target
			}
		}
		elbv2TargetGroups.Update(binding.TargetGroupArn, func(group *ELBv2TargetGroup) {
			next := make([]ELBv2TargetDescription, 0, len(group.Targets)+len(desired))
			for _, target := range group.Targets {
				if !managed[ecsServiceTargetKey(target)] {
					next = append(next, target)
				}
			}
			for _, target := range desired {
				next = append(next, target)
			}
			sort.Slice(next, func(i, j int) bool {
				return ecsServiceTargetKey(next[i]) < ecsServiceTargetKey(next[j])
			})
			group.Targets = next
		})
	}
}

func ecsServiceTargetKey(target ELBv2TargetDescription) string {
	return fmt.Sprintf("%s:%d", target.ID, target.Port)
}

func ecsServiceLoadBalancerTarget(
	task ECSTask,
	binding ecsServiceLoadBalancer,
	targetGroup ELBv2TargetGroup,
) (ELBv2TargetDescription, bool) {
	port := binding.ContainerPort
	if strings.EqualFold(targetGroup.TargetType, "instance") {
		key := ecsContainerInstanceKeyFromARN(task.ContainerInstanceArn)
		instance, ok := ecsContainerInstances.Get(key)
		if !ok || instance.Ec2InstanceId == "" {
			return ELBv2TargetDescription{}, false
		}
		if definition, ok := ecsTaskDefinitionForARN(task.TaskDefinitionArn); ok {
			for _, container := range definition.ContainerDefinitions {
				if container.Name != binding.ContainerName {
					continue
				}
				for _, mapping := range container.PortMappings {
					if mapping.ContainerPort == binding.ContainerPort && mapping.HostPort != 0 {
						port = mapping.HostPort
					}
				}
			}
		}
		return ELBv2TargetDescription{ID: instance.Ec2InstanceId, Port: port}, true
	}
	for _, container := range task.Containers {
		if container.Name != binding.ContainerName ||
			len(container.NetworkInterfaces) == 0 {
			continue
		}
		return ELBv2TargetDescription{
			ID:   container.NetworkInterfaces[0].PrivateIpv4Address,
			Port: port,
		}, true
	}
	return ELBv2TargetDescription{}, false
}

// TaskID returns the bare task ID (trailing path segment of the TaskArn).
func (task ECSTask) TaskID() string {
	arn := task.TaskArn
	if index := strings.LastIndex(arn, "/"); index >= 0 {
		return arn[index+1:]
	}
	return arn
}
