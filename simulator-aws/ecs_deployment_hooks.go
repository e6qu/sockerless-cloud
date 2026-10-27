package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// A deployment reaching a stage a hook guards stops there until
// ContinueServiceDeployment names the hook: CONTINUE proceeds, ROLLBACK
// abandons. While a deployment waits before SCALE_UP, the scheduler launches
// none of the new revision's tasks.
//
// An AWS_LAMBDA hook invokes the simulator's AWS Lambda with the service's
// payload; the function's reply does not release the deployment, its
// ContinueServiceDeployment call does.

// The DeploymentLifecycleHookDetail shape.
type ECSDeploymentHookDetail struct {
	HookId        string  `json:"hookId"`
	TargetType    string  `json:"targetType,omitempty"`
	TargetArn     string  `json:"targetArn,omitempty"`
	Status        string  `json:"status"`
	ExpiresAt     float64 `json:"expiresAt,omitempty"`
	TimeoutAction string  `json:"timeoutAction,omitempty"`
	// Not an AWS member: the record needs to know which gate an identifier
	// belongs to.
	Stage string `json:"-"`
}

// In ServiceDeploymentLifecycleStage order.
var ecsDeploymentLifecycleStages = []string{
	"RECONCILE_SERVICE",
	"PRE_SCALE_UP",
	"SCALE_UP",
	"POST_SCALE_UP",
	"TEST_TRAFFIC_SHIFT",
	"POST_TEST_TRAFFIC_SHIFT",
	"PRODUCTION_TRAFFIC_SHIFT",
	"POST_PRODUCTION_TRAFFIC_SHIFT",
	"BAKE_TIME",
	"CLEAN_UP",
}

// A hook waiting at or before SCALE_UP holds the scheduler back.
var ecsScaleUpStageIndex = ecsLifecycleStageIndex("SCALE_UP")

func ecsLifecycleStageIndex(stage string) int {
	for i, candidate := range ecsDeploymentLifecycleStages {
		if candidate == stage {
			return i
		}
	}
	return -1
}

type ecsServiceLifecycleHook struct {
	TargetType      string   `json:"targetType"`
	HookTargetArn   string   `json:"hookTargetArn"`
	RoleArn         string   `json:"roleArn"`
	LifecycleStages []string `json:"lifecycleStages"`
}

// The deployment configuration is stored as sent, so no hooks yields none.
func ecsServiceLifecycleHooks(service ECSService) []ecsServiceLifecycleHook {
	if len(service.DeploymentConfiguration) == 0 ||
		string(service.DeploymentConfiguration) == "null" {
		return nil
	}
	var configuration struct {
		LifecycleHooks []ecsServiceLifecycleHook `json:"lifecycleHooks"`
	}
	if json.Unmarshal(service.DeploymentConfiguration, &configuration) != nil {
		return nil
	}
	return configuration.LifecycleHooks
}

// Neither succeeded nor abandoned.
func ecsDeploymentHookPending(detail ECSDeploymentHookDetail) bool {
	return detail.Status == "AWAITING_ACTION" || detail.Status == "IN_PROGRESS"
}

// The scheduler asks this before scaling the new revision up.
func ecsServiceDeploymentGate(serviceArn string) (ECSServiceDeploymentRec, bool) {
	for _, deployment := range ecsServiceDeployments.List() {
		if deployment.ServiceArn != serviceArn || deployment.Status != "IN_PROGRESS" {
			continue
		}
		stage := ecsLifecycleStageIndex(deployment.LifecycleStage)
		if stage < 0 || stage > ecsScaleUpStageIndex {
			continue
		}
		for _, hook := range deployment.LifecycleHookDetails {
			if ecsDeploymentHookPending(hook) {
				return deployment, true
			}
		}
	}
	return ECSServiceDeploymentRec{}, false
}

func ecsBeginDeploymentLifecycle(deployment *ECSServiceDeploymentRec, service ECSService) {
	hooks := ecsServiceLifecycleHooks(service)
	if len(hooks) == 0 {
		// Without hooks a deployment still reports its stage and never stops.
		deployment.LifecycleStage = "SCALE_UP"
		return
	}
	deployment.LifecycleStage = ecsDeploymentLifecycleStages[0]
	ecsAdvanceDeploymentLifecycle(deployment, service)
}

// A deployment with nothing left to wait for lands on SCALE_UP, where the
// scheduler takes over.
func ecsAdvanceDeploymentLifecycle(deployment *ECSServiceDeploymentRec, service ECSService) {
	hooks := ecsServiceLifecycleHooks(service)
	for index := ecsLifecycleStageIndex(deployment.LifecycleStage); index >= 0 &&
		index < len(ecsDeploymentLifecycleStages); index++ {
		stage := ecsDeploymentLifecycleStages[index]
		deployment.LifecycleStage = stage
		hook, guarded := ecsHookForStage(hooks, stage)
		if !guarded {
			// Later stages are reported as the deployment reaches them.
			if index >= ecsScaleUpStageIndex {
				return
			}
			continue
		}
		detail := ECSDeploymentHookDetail{
			HookId:     sim.NewUUID(),
			TargetType: hook.TargetType,
			TargetArn:  hook.HookTargetArn,
			Status:     "AWAITING_ACTION",
			Stage:      stage,
		}
		if strings.EqualFold(hook.TargetType, "AWS_LAMBDA") {
			detail.Status = "IN_PROGRESS"
		}
		deployment.LifecycleHookDetails = append(deployment.LifecycleHookDetails, detail)
		if strings.EqualFold(hook.TargetType, "AWS_LAMBDA") {
			ecsInvokeDeploymentHook(*deployment, hook, detail)
		}
		return
	}
}

func ecsHookForStage(hooks []ecsServiceLifecycleHook, stage string) (ecsServiceLifecycleHook, bool) {
	for _, hook := range hooks {
		for _, guarded := range hook.LifecycleStages {
			if strings.EqualFold(guarded, stage) {
				return hook, true
			}
		}
	}
	return ecsServiceLifecycleHook{}, false
}

// The reply does not release the deployment; the function calls
// ContinueServiceDeployment, or the timeout expires.
func ecsInvokeDeploymentHook(
	deployment ECSServiceDeploymentRec,
	hook ecsServiceLifecycleHook,
	detail ECSDeploymentHookDetail,
) {
	payload, err := json.Marshal(map[string]any{
		"executionDetails": map[string]any{
			"serviceDeploymentArn":     deployment.ServiceDeploymentArn,
			"serviceArn":               deployment.ServiceArn,
			"clusterArn":               deployment.ClusterArn,
			"targetServiceRevisionArn": deployment.TargetServiceRevisionArn,
		},
		"lifecycleStage": detail.Stage,
		"hookId":         detail.HookId,
		"roleArn":        hook.RoleArn,
	})
	if err != nil {
		return
	}
	// Off the request path.
	bg.Go(func() {
		name := hook.HookTargetArn
		if strings.Contains(name, ":function:") {
			name = strings.SplitN(name, ":function:", 2)[1]
			if i := strings.Index(name, ":"); i >= 0 {
				name = name[:i]
			}
		}
		function, found := lambdaFunctions.Get(name)
		if !found {
			// A hook naming a missing function cannot pass.
			ecsFailDeploymentHook(deployment.ServiceDeploymentArn, detail.HookId,
				"the deployment lifecycle hook target does not exist: "+hook.HookTargetArn)
			return
		}
		lambdaInvokeAsynchronously(function, payload, "")
	})
}

// A hook that cannot run did not pass, so the deployment stops.
func ecsFailDeploymentHook(serviceDeploymentArn, hookID, reason string) {
	deployment, ok := ecsServiceDeployments.Get(serviceDeploymentArn)
	if !ok {
		return
	}
	for i := range deployment.LifecycleHookDetails {
		if deployment.LifecycleHookDetails[i].HookId != hookID {
			continue
		}
		deployment.LifecycleHookDetails[i].Status = "FAILED"
	}
	deployment.Status = "STOPPED"
	deployment.StatusReason = reason
	deployment.UpdatedAt = ecsEpochSeconds()
	deployment.FinishedAt = deployment.UpdatedAt
	ecsServiceDeployments.Put(serviceDeploymentArn, deployment)
}

// Returns the AWS error for a hook that cannot be continued.
func ecsContinueDeploymentHook(
	deployment *ECSServiceDeploymentRec, hookID, action string,
) (code, message string, ok bool) {
	if hookID == "" {
		// The model requires hookId, so only a hand-built request lacks it.
		return "InvalidParameterException", "hookId is required.", false
	}
	index := -1
	for i := range deployment.LifecycleHookDetails {
		if deployment.LifecycleHookDetails[i].HookId == hookID {
			index = i
		}
	}
	if index < 0 {
		return "InvalidParameterException",
			fmt.Sprintf("The service deployment has no lifecycle hook %q.", hookID), false
	}
	if !ecsDeploymentHookPending(deployment.LifecycleHookDetails[index]) {
		return "InvalidParameterException",
			fmt.Sprintf("The lifecycle hook %q is already %s.",
				hookID, strings.ToLower(deployment.LifecycleHookDetails[index].Status)), false
	}
	if strings.EqualFold(action, "ROLLBACK") {
		deployment.LifecycleHookDetails[index].Status = "FAILED"
		return "", "", true
	}
	deployment.LifecycleHookDetails[index].Status = "SUCCEEDED"
	// Move to the next stage, stopping again if another hook guards it.
	if service, found := ecsServiceByArn(deployment.ServiceArn); found {
		next := ecsLifecycleStageIndex(deployment.LifecycleStage) + 1
		if next < len(ecsDeploymentLifecycleStages) {
			deployment.LifecycleStage = ecsDeploymentLifecycleStages[next]
			ecsAdvanceDeploymentLifecycle(deployment, service)
		}
	}
	return "", "", true
}
