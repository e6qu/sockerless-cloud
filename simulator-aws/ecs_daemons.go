package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Daemons, their deployments and revisions, and daemon task definitions.

type ECSDaemon struct {
	DaemonArn               string   `json:"daemonArn"`
	ClusterArn              string   `json:"clusterArn"`
	Status                  string   `json:"status"`
	DaemonTaskDefinitionArn string   `json:"daemonTaskDefinitionArn,omitempty"`
	CapacityProviderArns    []string `json:"capacityProviderArns,omitempty"`
	DeploymentArn           string   `json:"deploymentArn,omitempty"`
	CreatedAt               float64  `json:"createdAt"`
	UpdatedAt               float64  `json:"updatedAt"`
	// The daemon is taggable and CreateDaemon accepts tags.
	Tags []ECSTag `json:"tags,omitempty"`
}

type ECSDaemonDeployment struct {
	DaemonDeploymentArn     string  `json:"daemonDeploymentArn"`
	DaemonArn               string  `json:"daemonArn"`
	ClusterArn              string  `json:"clusterArn"`
	Status                  string  `json:"status"`
	TargetDaemonRevisionArn string  `json:"targetDaemonRevisionArn,omitempty"`
	CreatedAt               float64 `json:"createdAt"`
	StartedAt               float64 `json:"startedAt"`
	FinishedAt              float64 `json:"finishedAt"`
}

type ECSDaemonRevision struct {
	DaemonRevisionArn       string  `json:"daemonRevisionArn"`
	ClusterArn              string  `json:"clusterArn"`
	DaemonArn               string  `json:"daemonArn"`
	DaemonTaskDefinitionArn string  `json:"daemonTaskDefinitionArn,omitempty"`
	CreatedAt               float64 `json:"createdAt"`
}

type ECSDaemonTaskDefinition struct {
	DaemonTaskDefinitionArn string          `json:"daemonTaskDefinitionArn"`
	Family                  string          `json:"family"`
	Revision                int             `json:"revision"`
	TaskRoleArn             string          `json:"taskRoleArn,omitempty"`
	ExecutionRoleArn        string          `json:"executionRoleArn,omitempty"`
	ContainerDefinitions    json.RawMessage `json:"containerDefinitions,omitempty"`
	Volumes                 json.RawMessage `json:"volumes,omitempty"`
	Cpu                     string          `json:"cpu,omitempty"`
	Memory                  string          `json:"memory,omitempty"`
	Status                  string          `json:"status"`
	RegisteredAt            float64         `json:"registeredAt"`
	DeleteRequestedAt       *float64        `json:"deleteRequestedAt,omitempty"`
	// The resource is taggable and RegisterDaemonTaskDefinition accepts tags.
	Tags    []ECSTag `json:"tags,omitempty"`
	PidMode string   `json:"pidMode,omitempty"`
	IpcMode string   `json:"ipcMode,omitempty"`
}

var (
	ecsDaemons               sim.Store[ECSDaemon]
	ecsDaemonDeployments     sim.Store[ECSDaemonDeployment]
	ecsDaemonRevisions       sim.Store[ECSDaemonRevision]
	ecsDaemonTaskDefinitions sim.Store[ECSDaemonTaskDefinition]
	ecsDaemonTDRevisionMu    sync.Mutex
	ecsDaemonTDRevisions     map[string]int
)

func registerECSDaemons(r *AWSRouter, srv *sim.Server) {
	ecsDaemons = sim.MakeStore[ECSDaemon](srv.DB(), "ecs_daemons")
	ecsDaemonDeployments = sim.MakeStore[ECSDaemonDeployment](srv.DB(), "ecs_daemon_deployments")
	ecsDaemonRevisions = sim.MakeStore[ECSDaemonRevision](srv.DB(), "ecs_daemon_revisions")
	ecsDaemonTaskDefinitions = sim.MakeStore[ECSDaemonTaskDefinition](srv.DB(), "ecs_daemon_task_definitions")
	ecsDaemonTDRebuildRevisionIndex()

	r.Register("AmazonEC2ContainerServiceV20141113.CreateDaemon", handleECSCreateDaemon)
	r.Register("AmazonEC2ContainerServiceV20141113.DeleteDaemon", handleECSDeleteDaemon)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeDaemon", handleECSDescribeDaemon)
	r.Register("AmazonEC2ContainerServiceV20141113.UpdateDaemon", handleECSUpdateDaemon)
	r.Register("AmazonEC2ContainerServiceV20141113.ListDaemons", handleECSListDaemons)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeDaemonDeployments", handleECSDescribeDaemonDeployments)
	r.Register("AmazonEC2ContainerServiceV20141113.ListDaemonDeployments", handleECSListDaemonDeployments)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeDaemonRevisions", handleECSDescribeDaemonRevisions)
	r.Register("AmazonEC2ContainerServiceV20141113.RegisterDaemonTaskDefinition", handleECSRegisterDaemonTaskDefinition)
	r.Register("AmazonEC2ContainerServiceV20141113.DeleteDaemonTaskDefinition", handleECSDeleteDaemonTaskDefinition)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeDaemonTaskDefinition", handleECSDescribeDaemonTaskDefinition)
	r.Register("AmazonEC2ContainerServiceV20141113.ListDaemonTaskDefinitions", handleECSListDaemonTaskDefinitions)
}

func ecsDaemonTDRebuildRevisionIndex() {
	ecsDaemonTDRevisionMu.Lock()
	defer ecsDaemonTDRevisionMu.Unlock()
	ecsDaemonTDRevisions = make(map[string]int)
	for _, definition := range ecsDaemonTaskDefinitions.List() {
		if definition.Family != "" && definition.Revision > ecsDaemonTDRevisions[definition.Family] {
			ecsDaemonTDRevisions[definition.Family] = definition.Revision
		}
	}
}

func handleECSCreateDaemon(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DaemonName              string   `json:"daemonName"`
		ClusterArn              string   `json:"clusterArn"`
		DaemonTaskDefinitionArn string   `json:"daemonTaskDefinitionArn"`
		CapacityProviderArns    []string `json:"capacityProviderArns"`
		Tags                    []ECSTag `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.DaemonName == "" {
		AWSError(w, "InvalidParameterException", "daemonName is required", http.StatusBadRequest)
		return
	}
	clusterName := ecsClusterNameFromRef(req.ClusterArn)
	cluster, ok := ecsClusters.Get(clusterName)
	if !ok {
		AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest, "Cluster not found: %s", req.ClusterArn)
		return
	}
	now := ecsEpochSeconds()
	daemonID := req.DaemonName + "/" + sim.NewUUID()
	daemonArn := ecsArn("daemon", daemonID)
	deploymentArn := ecsArn("daemon-deployment", daemonID+"/"+sim.NewUUID())
	revisionArn := ecsArn("daemon-revision", daemonID+"/1")

	d := ECSDaemon{
		DaemonArn:               daemonArn,
		ClusterArn:              cluster.ClusterArn,
		Status:                  "ACTIVE",
		DaemonTaskDefinitionArn: req.DaemonTaskDefinitionArn,
		CapacityProviderArns:    req.CapacityProviderArns,
		DeploymentArn:           deploymentArn,
		CreatedAt:               now,
		UpdatedAt:               now,
		Tags:                    req.Tags,
	}
	ecsDaemons.Put(daemonArn, d)
	ecsDaemonRevisions.Put(revisionArn, ECSDaemonRevision{
		DaemonRevisionArn:       revisionArn,
		ClusterArn:              cluster.ClusterArn,
		DaemonArn:               daemonArn,
		DaemonTaskDefinitionArn: req.DaemonTaskDefinitionArn,
		CreatedAt:               now,
	})
	ecsDaemonDeployments.Put(deploymentArn, ECSDaemonDeployment{
		DaemonDeploymentArn:     deploymentArn,
		DaemonArn:               daemonArn,
		ClusterArn:              cluster.ClusterArn,
		Status:                  "SUCCESSFUL",
		TargetDaemonRevisionArn: revisionArn,
		CreatedAt:               now,
		StartedAt:               now,
		FinishedAt:              now,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"daemonArn":     daemonArn,
		"status":        "ACTIVE",
		"createdAt":     now,
		"deploymentArn": deploymentArn,
	})
}

func handleECSDescribeDaemon(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DaemonArn string `json:"daemonArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	d, ok := ecsDaemons.Get(req.DaemonArn)
	if !ok {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest, "The specified daemon does not exist: %s", req.DaemonArn)
		return
	}
	// DaemonRevisionDetail's arn is the revision ARN, unlike the standalone
	// DaemonRevision shape.
	var currentRevisions []map[string]any
	for _, rev := range ecsDaemonRevisions.List() {
		if rev.DaemonArn == d.DaemonArn {
			currentRevisions = append(currentRevisions, map[string]any{
				"arn":               rev.DaemonRevisionArn,
				"totalRunningCount": 0,
			})
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"daemon": map[string]any{
			"daemonArn":        d.DaemonArn,
			"clusterArn":       d.ClusterArn,
			"status":           d.Status,
			"currentRevisions": currentRevisions,
			"deploymentArn":    d.DeploymentArn,
			"createdAt":        d.CreatedAt,
			"updatedAt":        d.UpdatedAt,
		},
	})
}

func handleECSUpdateDaemon(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DaemonArn               string   `json:"daemonArn"`
		DaemonTaskDefinitionArn string   `json:"daemonTaskDefinitionArn"`
		CapacityProviderArns    []string `json:"capacityProviderArns"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	d, ok := ecsDaemons.Get(req.DaemonArn)
	if !ok {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest, "The specified daemon does not exist: %s", req.DaemonArn)
		return
	}
	now := ecsEpochSeconds()
	if req.DaemonTaskDefinitionArn != "" {
		d.DaemonTaskDefinitionArn = req.DaemonTaskDefinitionArn
	}
	if req.CapacityProviderArns != nil {
		d.CapacityProviderArns = req.CapacityProviderArns
	}
	d.UpdatedAt = now
	ecsDaemons.Put(req.DaemonArn, d)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"daemonArn":     d.DaemonArn,
		"status":        d.Status,
		"createdAt":     d.CreatedAt,
		"updatedAt":     d.UpdatedAt,
		"deploymentArn": d.DeploymentArn,
	})
}

func handleECSDeleteDaemon(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DaemonArn string `json:"daemonArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	d, ok := ecsDaemons.Get(req.DaemonArn)
	if !ok {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest, "The specified daemon does not exist: %s", req.DaemonArn)
		return
	}
	d.Status = "DELETE_IN_PROGRESS"
	d.UpdatedAt = ecsEpochSeconds()
	ecsDaemons.Delete(req.DaemonArn)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"daemonArn":     d.DaemonArn,
		"status":        d.Status,
		"createdAt":     d.CreatedAt,
		"updatedAt":     d.UpdatedAt,
		"deploymentArn": d.DeploymentArn,
	})
}

func handleECSListDaemons(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClusterArn string `json:"clusterArn"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	clusterArn := ""
	if req.ClusterArn != "" {
		clusterName := ecsClusterNameFromRef(req.ClusterArn)
		c, ok := ecsClusters.Get(clusterName)
		if !ok {
			AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest, "Cluster not found: %s", req.ClusterArn)
			return
		}
		clusterArn = c.ClusterArn
	}
	var daemons []ECSDaemon
	for _, d := range ecsDaemons.List() {
		if clusterArn != "" && d.ClusterArn != clusterArn {
			continue
		}
		daemons = append(daemons, d)
	}
	summaries := make([]map[string]any, 0, len(daemons))
	for _, d := range daemons {
		summaries = append(summaries, map[string]any{
			"daemonArn": d.DaemonArn,
			"status":    d.Status,
			"createdAt": d.CreatedAt,
			"updatedAt": d.UpdatedAt,
		})
	}
	page, next, pageOK := awsKeyPage(w, ecsBadToken, summaries, awsFieldKey("daemonArn"), req.NextToken, req.MaxResults, 100)
	if !pageOK {
		return
	}
	out := map[string]any{"daemonSummariesList": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleECSDescribeDaemonDeployments(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DaemonDeploymentArns []string `json:"daemonDeploymentArns"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	var found []map[string]any
	failures := []map[string]string{}
	for _, arn := range req.DaemonDeploymentArns {
		dep, ok := ecsDaemonDeployments.Get(arn)
		if !ok {
			failures = append(failures, map[string]string{"arn": arn, "reason": "MISSING"})
			continue
		}
		found = append(found, map[string]any{
			"daemonDeploymentArn": dep.DaemonDeploymentArn,
			"clusterArn":          dep.ClusterArn,
			"status":              dep.Status,
			// A DaemonDeploymentRevisionDetail (arn and instance counts), not a
			// DaemonRevision (daemonRevisionArn).
			"targetDaemonRevision": map[string]any{
				"arn": dep.TargetDaemonRevisionArn,
			},
			"createdAt":  dep.CreatedAt,
			"startedAt":  dep.StartedAt,
			"finishedAt": dep.FinishedAt,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"daemonDeployments": found,
		"failures":          failures,
	})
}

func handleECSListDaemonDeployments(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DaemonArn  string `json:"daemonArn"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.DaemonArn == "" {
		AWSError(w, "InvalidParameterException", "daemonArn is required", http.StatusBadRequest)
		return
	}
	var deps []ECSDaemonDeployment
	for _, dep := range ecsDaemonDeployments.List() {
		if dep.DaemonArn != req.DaemonArn {
			continue
		}
		deps = append(deps, dep)
	}
	summaries := make([]map[string]any, 0, len(deps))
	for _, dep := range deps {
		summaries = append(summaries, map[string]any{
			"daemonDeploymentArn":     dep.DaemonDeploymentArn,
			"daemonArn":               dep.DaemonArn,
			"clusterArn":              dep.ClusterArn,
			"status":                  dep.Status,
			"targetDaemonRevisionArn": dep.TargetDaemonRevisionArn,
			"createdAt":               dep.CreatedAt,
			"startedAt":               dep.StartedAt,
			"finishedAt":              dep.FinishedAt,
		})
	}
	page, next, pageOK := awsKeyPage(w, ecsBadToken, summaries, awsFieldKey("daemonDeploymentArn"), req.NextToken, req.MaxResults, 100)
	if !pageOK {
		return
	}
	out := map[string]any{"daemonDeployments": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleECSDescribeDaemonRevisions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DaemonRevisionArns []string `json:"daemonRevisionArns"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	var found []ECSDaemonRevision
	failures := []map[string]string{}
	for _, arn := range req.DaemonRevisionArns {
		rev, ok := ecsDaemonRevisions.Get(arn)
		if !ok {
			failures = append(failures, map[string]string{"arn": arn, "reason": "MISSING"})
			continue
		}
		found = append(found, rev)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"daemonRevisions": found,
		"failures":        failures,
	})
}

func ecsDaemonTDKey(family string, revision int) string {
	return fmt.Sprintf("%s:%d", family, revision)
}

func handleECSRegisterDaemonTaskDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Family               string          `json:"family"`
		TaskRoleArn          string          `json:"taskRoleArn"`
		ExecutionRoleArn     string          `json:"executionRoleArn"`
		ContainerDefinitions json.RawMessage `json:"containerDefinitions"`
		Volumes              json.RawMessage `json:"volumes"`
		Cpu                  string          `json:"cpu"`
		Memory               string          `json:"memory"`
		PidMode              string          `json:"pidMode"`
		IpcMode              string          `json:"ipcMode"`
		Tags                 []ECSTag        `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Family == "" {
		AWSError(w, "InvalidParameterException", "family is required", http.StatusBadRequest)
		return
	}
	ecsDaemonTDRevisionMu.Lock()
	ecsDaemonTDRevisions[req.Family]++
	rev := ecsDaemonTDRevisions[req.Family]
	ecsDaemonTDRevisionMu.Unlock()

	arn := ecsArn("daemon-task-definition", ecsDaemonTDKey(req.Family, rev))
	td := ECSDaemonTaskDefinition{
		DaemonTaskDefinitionArn: arn,
		Family:                  req.Family,
		Revision:                rev,
		TaskRoleArn:             req.TaskRoleArn,
		ExecutionRoleArn:        req.ExecutionRoleArn,
		ContainerDefinitions:    req.ContainerDefinitions,
		Volumes:                 req.Volumes,
		Cpu:                     req.Cpu,
		Memory:                  req.Memory,
		Status:                  "ACTIVE",
		RegisteredAt:            ecsEpochSeconds(),
		PidMode:                 req.PidMode,
		IpcMode:                 req.IpcMode,
		Tags:                    req.Tags,
	}
	ecsDaemonTaskDefinitions.Put(ecsDaemonTDKey(req.Family, rev), td)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"daemonTaskDefinitionArn": arn})
}

func ecsDaemonTDRefKey(ref string) string {
	if strings.HasPrefix(ref, "arn:") {
		parts := strings.Split(ref, "/")
		return parts[len(parts)-1]
	}
	return ref
}

func handleECSDescribeDaemonTaskDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DaemonTaskDefinition string `json:"daemonTaskDefinition"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	td, ok := ecsDaemonTaskDefinitions.Get(ecsDaemonTDRefKey(req.DaemonTaskDefinition))
	if !ok {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"Unable to describe daemon task definition: %s", req.DaemonTaskDefinition)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"daemonTaskDefinition": td})
}

func handleECSDeleteDaemonTaskDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DaemonTaskDefinition string `json:"daemonTaskDefinition"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := ecsDaemonTDRefKey(req.DaemonTaskDefinition)
	td, ok := ecsDaemonTaskDefinitions.Get(key)
	if !ok {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"Unable to describe daemon task definition: %s", req.DaemonTaskDefinition)
		return
	}
	now := ecsEpochSeconds()
	td.Status = "DELETE_IN_PROGRESS"
	td.DeleteRequestedAt = &now
	ecsDaemonTaskDefinitions.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"daemonTaskDefinitionArn": td.DaemonTaskDefinitionArn})
}

func handleECSListDaemonTaskDefinitions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FamilyPrefix string `json:"familyPrefix"`
		Family       string `json:"family"`
		Status       string `json:"status"`
		MaxResults   int    `json:"maxResults"`
		NextToken    string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	var tds []ECSDaemonTaskDefinition
	for _, td := range ecsDaemonTaskDefinitions.List() {
		if req.Family != "" && td.Family != req.Family {
			continue
		}
		if req.FamilyPrefix != "" && !strings.HasPrefix(td.Family, req.FamilyPrefix) {
			continue
		}
		// Honour the status filter.
		if req.Status != "" && !strings.EqualFold(td.Status, req.Status) {
			continue
		}
		tds = append(tds, td)
	}
	summaries := make([]map[string]any, 0, len(tds))
	for _, td := range tds {
		summaries = append(summaries, map[string]any{
			"arn":          td.DaemonTaskDefinitionArn,
			"registeredAt": td.RegisteredAt,
			"status":       td.Status,
		})
	}
	page, next, pageOK := awsKeyPage(w, ecsBadToken, summaries, awsFieldKey("arn"), req.NextToken, req.MaxResults, 100)
	if !pageOK {
		return
	}
	out := map[string]any{"daemonTaskDefinitions": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}
