package main

import (
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// ECS service deployments + service revisions — the newer rollout-tracking
// surface (DescribeServiceDeployments / ListServiceDeployments /
// DescribeServiceRevisions / StopServiceDeployment / ContinueServiceDeployment)
// plus ListServicesByNamespace. Each CreateService / UpdateService records a
// service revision (the immutable config snapshot) and a service deployment
// (the rollout from the previous revision to it). Reconciliation marks the
// deployment successful only after real tasks reach the target revision.

// ECSServiceDeploymentRec is the stored shape of a service deployment.
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
}

// ECSServiceRevisionRec is the stored shape of a service revision.
type ECSServiceRevisionRec struct {
	ServiceRevisionArn string  `json:"serviceRevisionArn"`
	ServiceArn         string  `json:"serviceArn"`
	ClusterArn         string  `json:"clusterArn"`
	TaskDefinition     string  `json:"taskDefinition"`
	LaunchType         string  `json:"launchType,omitempty"`
	PlatformVersion    string  `json:"platformVersion,omitempty"`
	CreatedAt          float64 `json:"createdAt"`
	// Namespace links the revision (and so the service) to a Cloud Map namespace
	// for ListServicesByNamespace; never on the wire.
	Namespace string `json:"-"`
}

var (
	ecsServiceDeployments sim.Store[ECSServiceDeploymentRec]
	ecsServiceRevisions   sim.Store[ECSServiceRevisionRec]
)

func registerECSServiceDeployments(r *sim.AWSRouter, srv *sim.Server) {
	ecsServiceDeployments = sim.MakeStore[ECSServiceDeploymentRec](srv.DB(), "ecs_service_deployments")
	ecsServiceRevisions = sim.MakeStore[ECSServiceRevisionRec](srv.DB(), "ecs_service_revisions")

	r.Register("AmazonEC2ContainerServiceV20141113.DescribeServiceDeployments", handleECSDescribeServiceDeployments)
	r.Register("AmazonEC2ContainerServiceV20141113.ListServiceDeployments", handleECSListServiceDeployments)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeServiceRevisions", handleECSDescribeServiceRevisions)
	r.Register("AmazonEC2ContainerServiceV20141113.StopServiceDeployment", handleECSStopServiceDeployment)
	r.Register("AmazonEC2ContainerServiceV20141113.ContinueServiceDeployment", handleECSContinueServiceDeployment)
	r.Register("AmazonEC2ContainerServiceV20141113.ListServicesByNamespace", handleECSListServicesByNamespace)
}

// ecsRecordServiceDeployment snapshots a service into a new revision + an
// in-progress deployment, called on CreateService and UpdateService. namespace is
// the Cloud Map namespace the service is associated with (for
// ListServicesByNamespace), derived by the caller from serviceConnectConfiguration.
func ecsRecordServiceDeployment(svc ECSService, namespace string) {
	now := float64(time.Now().Unix())
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
	ecsServiceDeployments.Put(depArn, ECSServiceDeploymentRec{
		ServiceDeploymentArn:     depArn,
		ServiceArn:               svc.ServiceArn,
		ClusterArn:               svc.ClusterArn,
		CreatedAt:                now,
		StartedAt:                now,
		UpdatedAt:                now,
		TargetServiceRevisionArn: revArn,
		Status:                   "IN_PROGRESS",
	})
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

// ecsServiceArnSuffix returns the <cluster>/<service> portion of a service ARN.
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	var found []map[string]any
	var failures []map[string]string
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Service == "" {
		sim.AWSError(w, "InvalidParameterException", "service is required", http.StatusBadRequest)
		return
	}
	_, svc, ok := ecsResolveService(req.Cluster, req.Service)
	if !ok {
		sim.AWSErrorf(w, "ServiceNotFoundException", http.StatusBadRequest,
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
	sort.Slice(deps, func(i, j int) bool { return deps[i].ServiceDeploymentArn < deps[j].ServiceDeploymentArn })
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
	page, next := awsPage(briefs, req.NextToken, req.MaxResults, 100)
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	var found []ECSServiceRevisionRec
	var failures []map[string]string
	for _, arn := range req.ServiceRevisionArns {
		rev, ok := ecsServiceRevisions.Get(arn)
		if !ok {
			failures = append(failures, map[string]string{"arn": arn, "reason": "MISSING"})
			continue
		}
		found = append(found, rev)
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	dep, ok := ecsServiceDeployments.Get(req.ServiceDeploymentArn)
	if !ok {
		sim.AWSErrorf(w, "ServiceDeploymentNotFoundException", http.StatusBadRequest,
			"The service deployment %s does not exist.", req.ServiceDeploymentArn)
		return
	}
	if req.StopType == "ABORT" {
		dep.Status = "ROLLBACK_REQUESTED"
	} else {
		dep.Status = "STOP_REQUESTED"
	}
	dep.UpdatedAt = float64(time.Now().Unix())
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
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	dep, ok := ecsServiceDeployments.Get(req.ServiceDeploymentArn)
	if !ok {
		sim.AWSErrorf(w, "ServiceDeploymentNotFoundException", http.StatusBadRequest,
			"The service deployment %s does not exist.", req.ServiceDeploymentArn)
		return
	}
	if req.Action == "ROLLBACK" {
		dep.Status = "ROLLBACK_IN_PROGRESS"
	} else {
		dep.Status = "IN_PROGRESS"
	}
	dep.UpdatedAt = float64(time.Now().Unix())
	ecsServiceDeployments.Put(req.ServiceDeploymentArn, dep)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"serviceDeploymentArn": dep.ServiceDeploymentArn})
}

func handleECSListServicesByNamespace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Namespace  string `json:"namespace"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Namespace == "" {
		sim.AWSError(w, "InvalidParameterException", "namespace is required", http.StatusBadRequest)
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
		// Only list services that still exist and are ACTIVE.
		if svc, ok := ecsServiceByArn(rev.ServiceArn); ok && svc.Status == "ACTIVE" {
			seen[rev.ServiceArn] = true
			arns = append(arns, rev.ServiceArn)
		}
	}
	sort.Strings(arns)
	page, next := awsPage(arns, req.NextToken, req.MaxResults, 100)
	out := map[string]any{"serviceArns": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// ecsServiceByArn resolves a service ARN to its stored service.
func ecsServiceByArn(arn string) (ECSService, bool) {
	_, _, svc, ok := ecsServiceFromARN(arn)
	return svc, ok
}
