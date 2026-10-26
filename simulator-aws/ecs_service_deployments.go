package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Each CreateService and UpdateService records a service revision (immutable
// snapshot) and a service deployment (the rollout to it). A deployment
// succeeds only after real tasks reach the target revision.

type ECSServiceDeploymentRec struct {
	ServiceDeploymentArn     string  `json:"serviceDeploymentArn"`
	ServiceArn               string  `json:"serviceArn"`
	ClusterArn               string  `json:"clusterArn"`
	CreatedAt                float64 `json:"createdAt"`
	StartedAt                float64 `json:"startedAt"`
	FinishedAt               float64 `json:"finishedAt"`
	UpdatedAt                float64 `json:"updatedAt"`
	TargetServiceRevisionArn string  `json:"targetServiceRevisionArn"`
	Status                   string  `json:"status"`
	StatusReason             string  `json:"statusReason,omitempty"`
	// A service without hooks carries a stage and an empty list, as AWS returns.
	LifecycleStage       string                    `json:"lifecycleStage,omitempty"`
	LifecycleHookDetails []ECSDeploymentHookDetail `json:"lifecycleHookDetails,omitempty"`
}

type ECSServiceRevisionRec struct {
	ServiceRevisionArn string                `json:"serviceRevisionArn"`
	ServiceArn         string                `json:"serviceArn"`
	ClusterArn         string                `json:"clusterArn"`
	TaskDefinition     string                `json:"taskDefinition"`
	LaunchType         string                `json:"launchType,omitempty"`
	PlatformVersion    string                `json:"platformVersion,omitempty"`
	CreatedAt          float64               `json:"createdAt"`
	GatewayConnectors  []ecsGatewayConnector `json:"gatewayConnectors,omitempty"`
	// Links the revision to a Cloud Map namespace for ListServicesByNamespace;
	// never on the wire.
	Namespace string `json:"-"`
}

var (
	ecsServiceDeployments sim.Store[ECSServiceDeploymentRec]
	ecsServiceRevisions   sim.Store[ECSServiceRevisionRec]
)

func registerECSServiceDeployments(r *AWSRouter, srv *sim.Server) {
	ecsServiceDeployments = sim.MakeStore[ECSServiceDeploymentRec](srv.DB(), "ecs_service_deployments")
	ecsServiceRevisions = sim.MakeStore[ECSServiceRevisionRec](srv.DB(), "ecs_service_revisions")

	r.Register("AmazonEC2ContainerServiceV20141113.DescribeServiceDeployments", handleECSDescribeServiceDeployments)
	r.Register("AmazonEC2ContainerServiceV20141113.ListServiceDeployments", handleECSListServiceDeployments)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeServiceRevisions", handleECSDescribeServiceRevisions)
	r.Register("AmazonEC2ContainerServiceV20141113.StopServiceDeployment", handleECSStopServiceDeployment)
	r.Register("AmazonEC2ContainerServiceV20141113.ContinueServiceDeployment", handleECSContinueServiceDeployment)
	r.Register("AmazonEC2ContainerServiceV20141113.ListServicesByNamespace", handleECSListServicesByNamespace)
}

// namespace comes from the caller's serviceConnectConfiguration.
func ecsRecordServiceDeployment(svc ECSService, namespace string) string {
	now := ecsEpochSeconds()
	for _, deployment := range ecsServiceDeployments.List() {
		if deployment.ServiceArn != svc.ServiceArn || deployment.Status != "IN_PROGRESS" {
			continue
		}
		deployment.Status = "STOPPED"
		deployment.StatusReason = "Superseded by a newer service deployment"
		deployment.FinishedAt = now
		deployment.UpdatedAt = now
		ecsServiceDeployments.Put(deployment.ServiceDeploymentArn, deployment)
	}
	revArn := svc.ServiceArn + "/" + generateNumericID()
	ecsServiceRevisions.Put(revArn, ECSServiceRevisionRec{
		ServiceRevisionArn: revArn,
		ServiceArn:         svc.ServiceArn,
		ClusterArn:         svc.ClusterArn,
		TaskDefinition:     svc.TaskDefinition,
		LaunchType:         svc.LaunchType,
		PlatformVersion:    svc.PlatformVersion,
		CreatedAt:          now,
		Namespace:          namespace,
	})
	depArn := ecsArn("service-deployment", ecsServiceArnSuffix(svc.ServiceArn)+"/"+generateNumericID())
	deployment := ECSServiceDeploymentRec{
		ServiceDeploymentArn:     depArn,
		ServiceArn:               svc.ServiceArn,
		ClusterArn:               svc.ClusterArn,
		CreatedAt:                now,
		StartedAt:                now,
		UpdatedAt:                now,
		TargetServiceRevisionArn: revArn,
		Status:                   "IN_PROGRESS",
	}
	// Configured hooks stop the deployment at the first guarded stage, before
	// anything scales up.
	ecsBeginDeploymentLifecycle(&deployment, svc)
	ecsServiceDeployments.Put(depArn, deployment)
	return revArn
}

func ecsCompleteServiceDeployments(serviceArn string, now float64) {
	for _, deployment := range ecsServiceDeployments.List() {
		if deployment.ServiceArn != serviceArn || deployment.Status != "IN_PROGRESS" {
			continue
		}
		deployment.Status = "SUCCESSFUL"
		deployment.FinishedAt = now
		deployment.UpdatedAt = now
		ecsServiceDeployments.Put(deployment.ServiceDeploymentArn, deployment)
	}
}

func ecsServiceArnSuffix(serviceArn string) string {
	idx := strings.Index(serviceArn, ":service/")
	if idx < 0 {
		return serviceArn
	}
	return serviceArn[idx+len(":service/"):]
}

func handleECSDescribeServiceDeployments(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceDeploymentArns []string `json:"serviceDeploymentArns"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	var found []map[string]any
	failures := []map[string]string{}
	for _, arn := range req.ServiceDeploymentArns {
		dep, ok := ecsServiceDeployments.Get(arn)
		if !ok {
			failures = append(failures, map[string]string{"arn": arn, "reason": "MISSING"})
			continue
		}
		found = append(found, ecsServiceDeploymentDetail(dep))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"serviceDeployments": found,
		"failures":           failures,
	})
}

func ecsServiceDeploymentDetail(dep ECSServiceDeploymentRec) map[string]any {
	out := map[string]any{
		"serviceDeploymentArn": dep.ServiceDeploymentArn,
		"serviceArn":           dep.ServiceArn,
		"clusterArn":           dep.ClusterArn,
		"createdAt":            dep.CreatedAt,
		"startedAt":            dep.StartedAt,
		"updatedAt":            dep.UpdatedAt,
		"status":               dep.Status,
		"targetServiceRevision": map[string]any{
			"arn": dep.TargetServiceRevisionArn,
		},
	}
	if dep.FinishedAt != 0 {
		out["finishedAt"] = dep.FinishedAt
	}
	if dep.StatusReason != "" {
		out["statusReason"] = dep.StatusReason
	}
	// Callers need the stage and the hookId ContinueServiceDeployment takes.
	if dep.LifecycleStage != "" {
		out["lifecycleStage"] = dep.LifecycleStage
	}
	if len(dep.LifecycleHookDetails) > 0 {
		details := make([]map[string]any, 0, len(dep.LifecycleHookDetails))
		for _, hook := range dep.LifecycleHookDetails {
			detail := map[string]any{"hookId": hook.HookId, "status": hook.Status}
			if hook.TargetType != "" {
				detail["targetType"] = hook.TargetType
			}
			if hook.TargetArn != "" {
				detail["targetArn"] = hook.TargetArn
			}
			if hook.ExpiresAt != 0 {
				detail["expiresAt"] = hook.ExpiresAt
			}
			if hook.TimeoutAction != "" {
				detail["timeoutAction"] = hook.TimeoutAction
			}
			details = append(details, detail)
		}
		out["lifecycleHookDetails"] = details
	}
	return out
}

func handleECSListServiceDeployments(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Service    string `json:"service"`
		Cluster    string `json:"cluster"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Service == "" {
		AWSError(w, "InvalidParameterException", "service is required", http.StatusBadRequest)
		return
	}
	_, svc, ok := ecsResolveService(req.Cluster, req.Service)
	if !ok {
		AWSErrorf(w, "ServiceNotFoundException", http.StatusBadRequest,
			"The service %s does not exist.", req.Service)
		return
	}
	var deps []ECSServiceDeploymentRec
	for _, dep := range ecsServiceDeployments.List() {
		if dep.ServiceArn != svc.ServiceArn {
			continue
		}
		deps = append(deps, dep)
	}
	briefs := make([]map[string]any, 0, len(deps))
	for _, dep := range deps {
		brief := map[string]any{
			"serviceDeploymentArn":     dep.ServiceDeploymentArn,
			"serviceArn":               dep.ServiceArn,
			"clusterArn":               dep.ClusterArn,
			"startedAt":                dep.StartedAt,
			"createdAt":                dep.CreatedAt,
			"targetServiceRevisionArn": dep.TargetServiceRevisionArn,
			"status":                   dep.Status,
		}
		if dep.FinishedAt != 0 {
			brief["finishedAt"] = dep.FinishedAt
		}
		briefs = append(briefs, brief)
	}
	page, next, pageOK := awsKeyPage(w, ecsBadToken, briefs, awsFieldKey("serviceDeploymentArn"), req.NextToken, req.MaxResults, 100)
	if !pageOK {
		return
	}
	out := map[string]any{"serviceDeployments": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleECSDescribeServiceRevisions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceRevisionArns []string `json:"serviceRevisionArns"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	type revisionView struct {
		ECSServiceRevisionRec
		EcsManagedResources map[string]any `json:"ecsManagedResources,omitempty"`
	}
	var found []revisionView
	failures := []map[string]string{}
	for _, arn := range req.ServiceRevisionArns {
		rev, ok := ecsServiceRevisions.Get(arn)
		if !ok {
			failures = append(failures, map[string]string{"arn": arn, "reason": "MISSING"})
			continue
		}
		view := revisionView{ECSServiceRevisionRec: rev}
		if managed := ecsExpressManagedResourcesJSON(arn); managed != nil {
			view.EcsManagedResources = managed
		}
		if connections := ecsGatewayConnectionsJSON(arn); len(connections) > 0 {
			if view.EcsManagedResources == nil {
				view.EcsManagedResources = map[string]any{}
			}
			view.EcsManagedResources["gatewayConnections"] = connections
		}
		found = append(found, view)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"serviceRevisions": found,
		"failures":         failures,
	})
}

func handleECSStopServiceDeployment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceDeploymentArn string `json:"serviceDeploymentArn"`
		StopType             string `json:"stopType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	dep, ok := ecsServiceDeployments.Get(req.ServiceDeploymentArn)
	if !ok {
		AWSErrorf(w, "ServiceDeploymentNotFoundException", http.StatusBadRequest,
			"The service deployment %s does not exist.", req.ServiceDeploymentArn)
		return
	}
	if req.StopType == "ABORT" {
		dep.Status = "ROLLBACK_REQUESTED"
	} else {
		dep.Status = "STOP_REQUESTED"
	}
	dep.UpdatedAt = ecsEpochSeconds()
	ecsServiceDeployments.Put(req.ServiceDeploymentArn, dep)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"serviceDeploymentArn": dep.ServiceDeploymentArn})
}

func handleECSContinueServiceDeployment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceDeploymentArn string `json:"serviceDeploymentArn"`
		HookId               string `json:"hookId"`
		Action               string `json:"action"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	dep, ok := ecsServiceDeployments.Get(req.ServiceDeploymentArn)
	if !ok {
		AWSErrorf(w, "ServiceDeploymentNotFoundException", http.StatusBadRequest,
			"The service deployment %s does not exist.", req.ServiceDeploymentArn)
		return
	}
	// hookId names the hook this call releases.
	code, message, ok := ecsContinueDeploymentHook(&dep, req.HookId, req.Action)
	if !ok {
		AWSError(w, code, message, http.StatusBadRequest)
		return
	}
	if req.Action == "ROLLBACK" {
		dep.Status = "ROLLBACK_IN_PROGRESS"
	} else {
		dep.Status = "IN_PROGRESS"
	}
	dep.UpdatedAt = ecsEpochSeconds()
	ecsServiceDeployments.Put(req.ServiceDeploymentArn, dep)
	// Released, the service converges; the scheduler held its tasks while the hook
	// waited.
	if service, found := ecsServiceFromARNKey(dep.ServiceArn); found {
		ecsRequestServiceReconcile(service)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"serviceDeploymentArn": dep.ServiceDeploymentArn})
}

func handleECSListServicesByNamespace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Namespace  string `json:"namespace"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Namespace == "" {
		AWSError(w, "InvalidParameterException", "namespace is required", http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	var arns []string
	for _, rev := range ecsServiceRevisions.List() {
		if rev.Namespace != req.Namespace {
			continue
		}
		if seen[rev.ServiceArn] {
			continue
		}
		// List only services that still exist and are ACTIVE.
		if svc, ok := ecsServiceByArn(rev.ServiceArn); ok && svc.Status == "ACTIVE" {
			seen[rev.ServiceArn] = true
			arns = append(arns, rev.ServiceArn)
		}
	}
	page, next, pageOK := awsKeyPage(w, ecsBadToken, arns, func(it string) string { return it }, req.NextToken, req.MaxResults, 100)
	if !pageOK {
		return
	}
	out := map[string]any{"serviceArns": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func ecsServiceByArn(arn string) (ECSService, bool) {
	_, _, svc, ok := ecsServiceFromARN(arn)
	return svc, ok
}

// For paths holding a deployment that must nudge its service.
func ecsServiceFromARNKey(serviceArn string) (string, bool) {
	_, key, _, ok := ecsServiceFromARN(serviceArn)
	return key, ok
}
