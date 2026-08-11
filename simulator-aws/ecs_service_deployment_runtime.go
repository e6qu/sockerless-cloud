package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// ECSServiceSchedulerState is the durable scheduler bookkeeping that is not
// part of the public Amazon ECS Service shape. Keeping it in SQLite makes
// launch throttling and rollback decisions survive a simulator restart.
type ECSServiceSchedulerState struct {
	ServiceArn          string                        `json:"serviceArn"`
	DeploymentID        string                        `json:"deploymentId"`
	FailureCount        int                           `json:"failureCount"`
	FailedTaskIDs       []string                      `json:"failedTaskIds,omitempty"`
	NextLaunchAttemptAt int64                         `json:"nextLaunchAttemptAt,omitempty"`
	LastCompleted       *ECSServiceDeploymentSnapshot `json:"lastCompleted,omitempty"`
	RollbackInProgress  bool                          `json:"rollbackInProgress,omitempty"`
}

// ECSServiceDeploymentSnapshot contains the service fields Amazon ECS restores
// when it rolls a deployment back to the last completed revision. Desired
// count and operator-managed service metadata intentionally remain current.
type ECSServiceDeploymentSnapshot struct {
	TaskDefinition              string          `json:"taskDefinition"`
	LaunchType                  string          `json:"launchType,omitempty"`
	PlatformVersion             string          `json:"platformVersion,omitempty"`
	NetworkConfiguration        json.RawMessage `json:"networkConfiguration,omitempty"`
	LoadBalancers               json.RawMessage `json:"loadBalancers,omitempty"`
	ServiceRegistries           json.RawMessage `json:"serviceRegistries,omitempty"`
	CapacityProviderStrategy    json.RawMessage `json:"capacityProviderStrategy,omitempty"`
	ServiceConnectConfiguration json.RawMessage `json:"serviceConnectConfiguration,omitempty"`
	VolumeConfigurations        json.RawMessage `json:"volumeConfigurations,omitempty"`
	VpcLatticeConfigurations    json.RawMessage `json:"vpcLatticeConfigurations,omitempty"`
}

var (
	ecsServiceSchedulerStates sim.Store[ECSServiceSchedulerState]
	ecsServiceRetryTimers     sync.Map // map[service store key]*time.Timer
)

const ecsServiceSteadyStateWindow = time.Second

func ecsServiceSnapshot(service ECSService) ECSServiceDeploymentSnapshot {
	return ECSServiceDeploymentSnapshot{
		TaskDefinition:              service.TaskDefinition,
		LaunchType:                  service.LaunchType,
		PlatformVersion:             service.PlatformVersion,
		NetworkConfiguration:        append(json.RawMessage(nil), service.NetworkConfiguration...),
		LoadBalancers:               append(json.RawMessage(nil), service.LoadBalancers...),
		ServiceRegistries:           append(json.RawMessage(nil), service.ServiceRegistries...),
		CapacityProviderStrategy:    append(json.RawMessage(nil), service.CapacityProviderStrategy...),
		ServiceConnectConfiguration: append(json.RawMessage(nil), service.ServiceConnectConfiguration...),
		VolumeConfigurations:        append(json.RawMessage(nil), service.VolumeConfigurations...),
		VpcLatticeConfigurations:    append(json.RawMessage(nil), service.VpcLatticeConfigurations...),
	}
}

func ecsApplyServiceSnapshot(service *ECSService, snapshot ECSServiceDeploymentSnapshot) {
	service.TaskDefinition = snapshot.TaskDefinition
	service.LaunchType = snapshot.LaunchType
	service.PlatformVersion = snapshot.PlatformVersion
	service.NetworkConfiguration = append(json.RawMessage(nil), snapshot.NetworkConfiguration...)
	service.LoadBalancers = append(json.RawMessage(nil), snapshot.LoadBalancers...)
	service.ServiceRegistries = append(json.RawMessage(nil), snapshot.ServiceRegistries...)
	service.CapacityProviderStrategy = append(json.RawMessage(nil), snapshot.CapacityProviderStrategy...)
	service.ServiceConnectConfiguration = append(json.RawMessage(nil), snapshot.ServiceConnectConfiguration...)
	service.VolumeConfigurations = append(json.RawMessage(nil), snapshot.VolumeConfigurations...)
	service.VpcLatticeConfigurations = append(json.RawMessage(nil), snapshot.VpcLatticeConfigurations...)
}

func ecsServiceDeploymentCompleted(service ECSService) bool {
	return len(service.Deployments) > 0 &&
		service.Deployments[0].RolloutState == "COMPLETED"
}

func ecsBeginServiceDeployment(key string, previous, current ECSService) {
	state, _ := ecsServiceSchedulerStates.Get(key)
	state.ServiceArn = current.ServiceArn
	state.DeploymentID = ""
	if len(current.Deployments) > 0 {
		state.DeploymentID = current.Deployments[0].Id
	}
	if state.LastCompleted == nil && previous.ServiceArn != "" && ecsServiceDeploymentCompleted(previous) {
		snapshot := ecsServiceSnapshot(previous)
		state.LastCompleted = &snapshot
	}
	state.FailureCount = 0
	state.FailedTaskIDs = nil
	state.NextLaunchAttemptAt = 0
	state.RollbackInProgress = false
	ecsServiceSchedulerStates.Put(key, state)
	ecsCancelServiceRetry(key)
	ecsAddServiceEvent(key, fmt.Sprintf(
		"(service %s) began deployment %s.", current.ServiceName, state.DeploymentID,
	))
}

func ecsMarkServiceDeploymentCompleted(key string, service ECSService) {
	state, _ := ecsServiceSchedulerStates.Get(key)
	if state.DeploymentID == "" && len(service.Deployments) > 0 {
		state.DeploymentID = service.Deployments[0].Id
	}
	if state.DeploymentID != "" && len(service.Deployments) > 0 &&
		state.DeploymentID != service.Deployments[0].Id {
		return
	}
	snapshot := ecsServiceSnapshot(service)
	state.ServiceArn = service.ServiceArn
	state.LastCompleted = &snapshot
	state.FailureCount = 0
	state.FailedTaskIDs = nil
	state.NextLaunchAttemptAt = 0
	wasRollback := state.RollbackInProgress
	state.RollbackInProgress = false
	ecsServiceSchedulerStates.Put(key, state)
	ecsCancelServiceRetry(key)
	if wasRollback {
		ecsFinishServiceDeploymentRollback(service.ServiceArn)
		ecsAddServiceEvent(key, fmt.Sprintf(
			"(service %s) deployment rollback completed.", service.ServiceName,
		))
	}
}

func ecsAddServiceEvent(key, message string) {
	ecsServices.Update(key, func(service *ECSService) {
		event := ECSServiceEvent{
			Id:        generateUUID(),
			CreatedAt: float64(time.Now().Unix()),
			Message:   message,
		}
		service.Events = append([]ECSServiceEvent{event}, service.Events...)
		if len(service.Events) > 100 {
			service.Events = service.Events[:100]
		}
	})
}

type ecsServiceDeploymentConfigurationRuntime struct {
	MaximumPercent           *int `json:"maximumPercent"`
	MinimumHealthyPercent    *int `json:"minimumHealthyPercent"`
	DeploymentCircuitBreaker *struct {
		Enable                 bool  `json:"enable"`
		Rollback               bool  `json:"rollback"`
		ResetOnHealthyTask     *bool `json:"resetOnHealthyTask"`
		ThresholdConfiguration *struct {
			Type  string `json:"type"`
			Value int    `json:"value"`
		} `json:"thresholdConfiguration"`
	} `json:"deploymentCircuitBreaker"`
	Alarms *struct {
		AlarmNames []string `json:"alarmNames"`
		Enable     bool     `json:"enable"`
		Rollback   bool     `json:"rollback"`
	} `json:"alarms"`
}

func ecsServiceRuntimeDeploymentConfiguration(service ECSService) ecsServiceDeploymentConfigurationRuntime {
	var configuration ecsServiceDeploymentConfigurationRuntime
	if len(service.DeploymentConfiguration) > 0 &&
		string(service.DeploymentConfiguration) != "null" {
		_ = json.Unmarshal(service.DeploymentConfiguration, &configuration)
	}
	return configuration
}

func ecsServiceFailureThreshold(service ECSService, configuration ecsServiceDeploymentConfigurationRuntime) int {
	threshold := int(math.Ceil(0.5 * float64(ecsServiceTaskTargetCount(service))))
	if threshold < 3 {
		threshold = 3
	}
	if threshold > 200 {
		threshold = 200
	}
	if configuration.DeploymentCircuitBreaker == nil ||
		configuration.DeploymentCircuitBreaker.ThresholdConfiguration == nil {
		return threshold
	}
	requested := configuration.DeploymentCircuitBreaker.ThresholdConfiguration
	switch requested.Type {
	case "COUNT":
		if requested.Value > 0 {
			return requested.Value
		}
	case "UNBOUNDED_PERCENT":
		return int(math.Ceil(float64(requested.Value) * float64(ecsServiceTaskTargetCount(service)) / 100))
	case "BOUNDED_PERCENT", "":
		calculated := int(math.Ceil(float64(requested.Value) * float64(ecsServiceTaskTargetCount(service)) / 100))
		if calculated < 3 {
			calculated = 3
		}
		if calculated > 200 {
			calculated = 200
		}
		return calculated
	}
	return threshold
}

func ecsServiceTaskFailureAlreadyRecorded(state ECSServiceSchedulerState, taskID string) bool {
	for _, recorded := range state.FailedTaskIDs {
		if recorded == taskID {
			return true
		}
	}
	return false
}

func ecsRecordServiceTaskFailure(task ECSTask) {
	if !strings.HasPrefix(task.Group, "service:") ||
		task.LastStatus != ECSTaskStatusStopped ||
		task.StopCode != "EssentialContainerExited" {
		return
	}
	key := ecsServiceKey(
		ecsClusterNameFromRef(task.ClusterArn),
		strings.TrimPrefix(task.Group, "service:"),
	)
	service, ok := ecsServices.Get(key)
	if !ok || service.Status != "ACTIVE" || len(service.Deployments) == 0 ||
		service.Deployments[0].RolloutState != "IN_PROGRESS" ||
		!ecsServiceTaskMatchesDeployment(task, service.Deployments[0]) {
		return
	}
	state, _ := ecsServiceSchedulerStates.Get(key)
	if state.DeploymentID != service.Deployments[0].Id ||
		ecsServiceTaskFailureAlreadyRecorded(state, task.TaskID()) {
		return
	}
	state.FailedTaskIDs = append(state.FailedTaskIDs, task.TaskID())
	ecsPersistServiceLaunchFailure(key, service, state, task.StoppedReason)
}

func ecsServiceTaskMatchesDeployment(task ECSTask, deployment ECSDeployment) bool {
	definition, ok := ecsServiceTaskDefinition(deployment.TaskDefinition)
	return ok && task.TaskDefinitionArn == definition.TaskDefinitionArn
}

func ecsResetServiceFailureCountAfterHealthyTask(key string, service ECSService) {
	configuration := ecsServiceRuntimeDeploymentConfiguration(service)
	if configuration.DeploymentCircuitBreaker == nil ||
		!configuration.DeploymentCircuitBreaker.Enable {
		return
	}
	reset := configuration.DeploymentCircuitBreaker.ResetOnHealthyTask
	if reset != nil && !*reset {
		return
	}
	state, ok := ecsServiceSchedulerStates.Get(key)
	if !ok || state.FailureCount == 0 {
		return
	}
	state.FailureCount = 0
	state.NextLaunchAttemptAt = 0
	ecsServiceSchedulerStates.Put(key, state)
	ecsCancelServiceRetry(key)
}

func ecsRecordServiceLaunchFailure(key, reason string) {
	service, ok := ecsServices.Get(key)
	if !ok || service.Status != "ACTIVE" || len(service.Deployments) == 0 ||
		service.Deployments[0].RolloutState != "IN_PROGRESS" {
		return
	}
	state, _ := ecsServiceSchedulerStates.Get(key)
	if state.DeploymentID != service.Deployments[0].Id {
		state.DeploymentID = service.Deployments[0].Id
		state.FailureCount = 0
		state.FailedTaskIDs = nil
	}
	ecsPersistServiceLaunchFailure(key, service, state, reason)
}

func ecsPersistServiceLaunchFailure(
	key string,
	service ECSService,
	state ECSServiceSchedulerState,
	reason string,
) {
	state.FailureCount++
	delay := time.Second << min(state.FailureCount-1, 6)
	state.NextLaunchAttemptAt = time.Now().Add(delay).Unix()
	ecsServiceSchedulerStates.Put(key, state)
	ecsAddServiceEvent(key, fmt.Sprintf(
		"(service %s) was unable to place a task. Reason: %s. Retrying after %s.",
		service.ServiceName, reason, delay,
	))

	configuration := ecsServiceRuntimeDeploymentConfiguration(service)
	circuitBreaker := configuration.DeploymentCircuitBreaker
	if circuitBreaker != nil && circuitBreaker.Enable &&
		state.FailureCount >= ecsServiceFailureThreshold(service, configuration) {
		ecsFailServiceDeployment(key, reason, circuitBreaker.Rollback)
		return
	}
	ecsScheduleServiceRetry(key, state.NextLaunchAttemptAt)
}

func ecsServiceRetryReady(key string) bool {
	state, ok := ecsServiceSchedulerStates.Get(key)
	if !ok || state.NextLaunchAttemptAt == 0 {
		return true
	}
	if time.Now().Unix() >= state.NextLaunchAttemptAt {
		return true
	}
	ecsScheduleServiceRetry(key, state.NextLaunchAttemptAt)
	return false
}

func ecsScheduleServiceRetry(key string, at int64) {
	delay := time.Until(time.Unix(at, 0))
	if delay < 0 {
		delay = 0
	}
	timer := time.AfterFunc(delay, func() {
		ecsServiceRetryTimers.Delete(key)
		ecsRequestServiceReconcile(key)
	})
	if _, loaded := ecsServiceRetryTimers.LoadOrStore(key, timer); loaded {
		timer.Stop()
	}
}

func ecsCancelServiceRetry(key string) {
	value, ok := ecsServiceRetryTimers.LoadAndDelete(key)
	if !ok {
		return
	}
	if timer, timerOK := value.(*time.Timer); timerOK {
		timer.Stop()
	}
}

func ecsCheckServiceDeploymentAlarms(key string, service ECSService) bool {
	if scheduler, ok := ecsServiceSchedulerStates.Get(key); ok && scheduler.RollbackInProgress {
		// The alarm triggered the rollback. Amazon ECS monitors the failed
		// deployment, not the rollback deployment, against that same alarm;
		// otherwise an alarm that remains ALARM would recursively roll back the
		// rollback forever.
		return false
	}
	configuration := ecsServiceRuntimeDeploymentConfiguration(service)
	if configuration.Alarms == nil || !configuration.Alarms.Enable ||
		len(service.Deployments) == 0 ||
		service.Deployments[0].RolloutState != "IN_PROGRESS" {
		return false
	}
	for _, alarmName := range configuration.Alarms.AlarmNames {
		alarm, ok := cwAlarms.Get(alarmName)
		if !ok {
			continue
		}
		state, _ := cwAlarmEffectiveState(alarm)
		if state != "ALARM" {
			continue
		}
		ecsFailServiceDeployment(
			key,
			fmt.Sprintf("CloudWatch alarm %s entered the ALARM state", alarmName),
			configuration.Alarms.Rollback,
		)
		return true
	}
	return false
}

func ecsRequestServiceReconcileForAlarm(alarmName string) {
	for _, service := range ecsServices.List() {
		configuration := ecsServiceRuntimeDeploymentConfiguration(service)
		if configuration.Alarms == nil || !configuration.Alarms.Enable {
			continue
		}
		for _, configured := range configuration.Alarms.AlarmNames {
			if configured == alarmName {
				ecsRequestServiceReconcile(ecsServiceStoreKey(service))
				break
			}
		}
	}
}

func ecsFailServiceDeployment(key, reason string, rollback bool) {
	service, ok := ecsServices.Get(key)
	if !ok || len(service.Deployments) == 0 ||
		service.Deployments[0].RolloutState == "FAILED" {
		return
	}
	failed := service.Deployments[0]
	failed.RolloutState = "FAILED"
	failed.RolloutStateReason = reason
	failed.UpdatedAt = float64(time.Now().Unix())
	ecsServices.Update(key, func(current *ECSService) {
		current.PendingCount = 0
		current.Deployments[0] = failed
	})
	ecsMarkServiceDeploymentFailed(service.ServiceArn, reason)
	ecsAddServiceEvent(key, fmt.Sprintf(
		"(service %s) deployment %s failed: %s.", service.ServiceName, failed.Id, reason,
	))
	ecsCancelServiceRetry(key)
	if rollback {
		ecsStartServiceDeploymentRollback(key, service, failed)
	}
}

func ecsStartServiceDeploymentRollback(key string, service ECSService, failed ECSDeployment) {
	state, ok := ecsServiceSchedulerStates.Get(key)
	if !ok || state.LastCompleted == nil {
		ecsAddServiceEvent(key, fmt.Sprintf(
			"(service %s) could not roll back because no completed deployment was found.",
			service.ServiceName,
		))
		return
	}
	// ecsFailServiceDeployment persisted the failed rollout and its service
	// event before entering rollback. Reload that record so restoring the last
	// completed deployment configuration cannot overwrite those public events.
	if current, exists := ecsServices.Get(key); exists {
		service = current
	}
	ecsDeregisterServiceRegistryTasks(service)
	ecsApplyServiceSnapshot(&service, *state.LastCompleted)
	now := float64(time.Now().Unix())
	rollbackDeployment := ecsServiceDeployment(service, now)
	rollbackDeployment.RolloutStateReason = "Deployment rollback is in progress"
	failed.Status = "ACTIVE"
	service.Deployments = []ECSDeployment{rollbackDeployment, failed}
	ecsServices.Put(key, service)

	state.DeploymentID = rollbackDeployment.Id
	state.FailureCount = 0
	state.FailedTaskIDs = nil
	state.NextLaunchAttemptAt = 0
	state.RollbackInProgress = true
	ecsServiceSchedulerStates.Put(key, state)
	ecsMarkServiceDeploymentRollbackInProgress(service.ServiceArn, reasonForFailedDeployment(failed))
	ecsRecordServiceDeployment(service, ecsServiceConnectNamespace(service.ServiceConnectConfiguration))
	ecsAddServiceEvent(key, fmt.Sprintf(
		"(service %s) began rolling back to task definition %s.",
		service.ServiceName, service.TaskDefinition,
	))
	ecsRequestServiceReconcile(key)
}

func reasonForFailedDeployment(deployment ECSDeployment) string {
	if deployment.RolloutStateReason != "" {
		return deployment.RolloutStateReason
	}
	return "service deployment failed"
}

func ecsMarkServiceDeploymentFailed(serviceARN, reason string) {
	now := float64(time.Now().Unix())
	for _, deployment := range ecsServiceDeployments.List() {
		if deployment.ServiceArn != serviceARN || deployment.Status != "IN_PROGRESS" {
			continue
		}
		deployment.Status = "STOPPED"
		deployment.StatusReason = reason
		deployment.FinishedAt = now
		deployment.UpdatedAt = now
		ecsServiceDeployments.Put(deployment.ServiceDeploymentArn, deployment)
	}
}

func ecsMarkServiceDeploymentRollbackInProgress(serviceARN, reason string) {
	now := float64(time.Now().Unix())
	for _, deployment := range ecsServiceDeployments.List() {
		if deployment.ServiceArn != serviceARN || deployment.Status != "STOPPED" ||
			deployment.StatusReason != reason {
			continue
		}
		deployment.Status = "ROLLBACK_IN_PROGRESS"
		deployment.FinishedAt = 0
		deployment.UpdatedAt = now
		ecsServiceDeployments.Put(deployment.ServiceDeploymentArn, deployment)
	}
}

func ecsFinishServiceDeploymentRollback(serviceARN string) {
	now := float64(time.Now().Unix())
	for _, deployment := range ecsServiceDeployments.List() {
		if deployment.ServiceArn != serviceARN || deployment.Status != "ROLLBACK_IN_PROGRESS" {
			continue
		}
		deployment.Status = "ROLLBACK_SUCCESSFUL"
		deployment.FinishedAt = now
		deployment.UpdatedAt = now
		ecsServiceDeployments.Put(deployment.ServiceDeploymentArn, deployment)
	}
}
