package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Amazon ECS deployment lifecycle hooks.
//
// A service's deploymentConfiguration may declare hooks, each naming the
// lifecycle stages it guards. When a deployment reaches a guarded stage it
// stops there: the hook is recorded on the deployment with an identifier, and
// the deployment goes no further until ContinueServiceDeployment names that
// identifier — CONTINUE to proceed, ROLLBACK to abandon the deployment.
//
// The gate is real, not a label. While a deployment waits at a stage that
// precedes SCALE_UP, the service scheduler does not launch the new revision's
// tasks; that is what a PRE_SCALE_UP hook is for, and a hook that only
// decorated the deployment record while the tasks rolled out anyway would be
// the decoration this simulator refuses to serve.
//
// An AWS_LAMBDA hook is invoked through this simulator's own AWS Lambda
// implementation, with the payload the service sends, and the function's
// reply is not what releases the deployment — real Amazon ECS releases it when
// the hook calls ContinueServiceDeployment, and so does this.

// ECSDeploymentHookDetail is one hook recorded on a deployment. The members
// are the DeploymentLifecycleHookDetail shape the SDK deserialises.
type ECSDeploymentHookDetail struct {
	HookId        string  `json:"hookId"`
	TargetType    string  `json:"targetType,omitempty"`
	TargetArn     string  `json:"targetArn,omitempty"`
	Status        string  `json:"status"`
	ExpiresAt     float64 `json:"expiresAt,omitempty"`
	TimeoutAction string  `json:"timeoutAction,omitempty"`
	// Stage is the lifecycle stage this hook guards. It is not a member of the
	// AWS shape — the deployment's own lifecycleStage reports where it is —
	// but the record needs it to know which gate an identifier belongs to.
	Stage string `json:"-"`
}

// ecsDeploymentLifecycleStages are the stages a deployment passes through, in
// order, as ServiceDeploymentLifecycleStage declares them.
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

// ecsScaleUpStageIndex is where the new revision's tasks start. A hook still
// waiting at or before this point holds the scheduler back.
var ecsScaleUpStageIndex = ecsLifecycleStageIndex("SCALE_UP")

func ecsLifecycleStageIndex(stage string) int {
	for i, candidate := range ecsDeploymentLifecycleStages {
		if candidate == stage {
			return i
		}
	}
	return -1
}

// ecsServiceLifecycleHook is one hook as the service configured it.
type ecsServiceLifecycleHook struct {
	TargetType      string   `json:"targetType"`
	HookTargetArn   string   `json:"hookTargetArn"`
	RoleArn         string   `json:"roleArn"`
	LifecycleStages []string `json:"lifecycleStages"`
}

// ecsServiceLifecycleHooks reads the hooks a service declares. The deployment
// configuration is stored as the document the caller sent, so a service with
// no hooks yields none and nothing downstream changes.
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

// ecsDeploymentHookPending reports whether a hook is still holding the
// deployment: one that has neither succeeded nor been abandoned.
func ecsDeploymentHookPending(detail ECSDeploymentHookDetail) bool {
	return detail.Status == "AWAITING_ACTION" || detail.Status == "IN_PROGRESS"
}

// ecsServiceDeploymentGate reports whether the service's in-flight deployment
// is waiting at a stage before its tasks may be launched. The scheduler asks
// this before it scales the new revision up.
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

// ecsBeginDeploymentLifecycle puts a new deployment at the first stage and
// stops it at the first stage a hook guards.
func ecsBeginDeploymentLifecycle(deployment *ECSServiceDeploymentRec, service ECSService) {
	hooks := ecsServiceLifecycleHooks(service)
	if len(hooks) == 0 {
		// Without hooks a deployment still reports where it is; it simply
		// never stops.
		deployment.LifecycleStage = "SCALE_UP"
		return
	}
	deployment.LifecycleStage = ecsDeploymentLifecycleStages[0]
	ecsAdvanceDeploymentLifecycle(deployment, service)
}

// ecsAdvanceDeploymentLifecycle walks the deployment forward from its current
// stage, stopping at the first stage a hook guards and recording that hook. A
// deployment with nothing left to wait for lands on SCALE_UP, where the
// scheduler takes over.
func ecsAdvanceDeploymentLifecycle(deployment *ECSServiceDeploymentRec, service ECSService) {
	hooks := ecsServiceLifecycleHooks(service)
	for index := ecsLifecycleStageIndex(deployment.LifecycleStage); index >= 0 &&
		index < len(ecsDeploymentLifecycleStages); index++ {
		stage := ecsDeploymentLifecycleStages[index]
		deployment.LifecycleStage = stage
		hook, guarded := ecsHookForStage(hooks, stage)
		if !guarded {
			// Past the point where the scheduler needs to run, the remaining
			// stages are reported as the deployment reaches them rather than
			// walked through here.
			if index >= ecsScaleUpStageIndex {
				return
			}
			continue
		}
		detail := ECSDeploymentHookDetail{
			HookId:     generateUUID(),
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

// ecsHookForStage returns the hook guarding a stage, if one does.
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

// ecsInvokeDeploymentHook calls the hook's AWS Lambda function through this
// simulator's own Lambda implementation, with the payload Amazon ECS sends. The
// reply does not release the deployment: the function releases it by calling
// ContinueServiceDeployment with the hook identifier, which is how the real
// contract works and what the timeout would otherwise expire waiting for.
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
	// Off the request path: the caller is creating a deployment, and the hook
	// is the deployment's own work.
	simGo(func() {
		name := hook.HookTargetArn
		if strings.Contains(name, ":function:") {
			name = strings.SplitN(name, ":function:", 2)[1]
			if i := strings.Index(name, ":"); i >= 0 {
				name = name[:i]
			}
		}
		function, found := lambdaFunctions.Get(name)
		if !found {
			// A hook naming a function that does not exist cannot pass, and a
			// deployment that proceeded anyway would have skipped the gate.
			ecsFailDeploymentHook(deployment.ServiceDeploymentArn, detail.HookId,
				"the deployment lifecycle hook target does not exist: "+hook.HookTargetArn)
			return
		}
		lambdaInvokeAsynchronously(function, payload, "")
	})
}

// ecsFailDeploymentHook marks a hook failed, which stops the deployment: a
// hook that cannot run is not a hook that passed.
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
	deployment.UpdatedAt = float64(time.Now().Unix())
	deployment.FinishedAt = deployment.UpdatedAt
	ecsServiceDeployments.Put(serviceDeploymentArn, deployment)
}

// ecsContinueDeploymentHook applies a ContinueServiceDeployment action to the
// hook it names, and returns the AWS error to answer with when it names one
// that cannot be continued.
func ecsContinueDeploymentHook(
	deployment *ECSServiceDeploymentRec, hookID, action string,
) (code, message string, ok bool) {
	if hookID == "" {
		// The model marks hookId required, so the SDK refuses to send the call
		// without one and only a hand-built request arrives here. It is still
		// answered rather than assumed: there is no such thing as continuing
		// an unnamed hook.
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
	// The stage the hook guarded is complete, so the deployment moves on to the
	// next one — and stops again if another hook guards it.
	if service, found := ecsServiceByArn(deployment.ServiceArn); found {
		next := ecsLifecycleStageIndex(deployment.LifecycleStage) + 1
		if next < len(ecsDeploymentLifecycleStages) {
			deployment.LifecycleStage = ecsDeploymentLifecycleStages[next]
			ecsAdvanceDeploymentLifecycle(deployment, service)
		}
	}
	return "", "", true
}
