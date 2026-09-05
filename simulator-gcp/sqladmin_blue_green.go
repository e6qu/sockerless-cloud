package main

import (
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cloud SQL blue-green deployment: a green copy of a running instance that
// takes the source's place at switchover.
//
// The collection is addressed by location — projects/{p}/locations/{l}/
// blueGreenDeployments/{d} — while the instance it copies is addressed by
// project alone, so the deployment records the source instance by name and the
// green instance is a real instance in the same store every other Cloud SQL
// read serves.

// SQLBlueGreenDeployment mirrors the Discovery BlueGreenDeployment schema.
type SQLBlueGreenDeployment struct {
	Name                     string                 `json:"name,omitempty"`
	SourceInstance           string                 `json:"sourceInstance,omitempty"`
	SwitchoverTargetInstance string                 `json:"switchoverTargetInstance,omitempty"`
	State                    string                 `json:"state,omitempty"`
	CreateTime               string                 `json:"createTime,omitempty"`
	Description              string                 `json:"description,omitempty"`
	ErrorDetail              string                 `json:"errorDetail,omitempty"`
	DeploymentTasks          *SQLDeploymentTasks    `json:"deploymentTasks,omitempty"`
	DeploymentMappings       []map[string]any       `json:"deploymentMappings,omitempty"`
	RequestedConfig          *SQLBlueGreenReqConfig `json:"requestedConfig,omitempty"`

	// project and location carry the coordinates the resource name encodes, so
	// a list does not parse every stored name.
	project  string
	location string
}

// SQLDeploymentTasks is the consolidated task list for the deployment's paired
// nodes. `task` is an array of DeploymentTask, not a single value.
type SQLDeploymentTasks struct {
	Task []SQLDeploymentTask `json:"task,omitempty"`
}

// SQLDeploymentTask mirrors the Discovery DeploymentTask schema.
type SQLDeploymentTask struct {
	Type         string `json:"type,omitempty"`
	State        string `json:"state,omitempty"`
	StartTime    string `json:"startTime,omitempty"`
	EndTime      string `json:"endTime,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

type SQLBlueGreenReqConfig struct {
	DatabaseVersion string `json:"databaseVersion,omitempty"`
}

var sqlBlueGreenDeployments sim.Store[SQLBlueGreenDeployment]

func sqlBlueGreenKey(project, location, deployment string) string {
	return project + "/" + location + "/" + deployment
}

func sqlBlueGreenName(project, location, deployment string) string {
	return "projects/" + project + "/locations/" + location + "/blueGreenDeployments/" + deployment
}

// sqlBlueGreenGreenInstance names the green instance a deployment creates. It
// is a real instance: the source's configuration, its own name, and the
// database version the request asked for when it asked for one.
func sqlBlueGreenGreenInstance(deployment string) string {
	return deployment + "-green"
}

func registerCloudSQLBlueGreenPrefix(srv *sim.Server, prefix string) {
	// Written out per route rather than through a shared base: the generated
	// surface tables read the literal path out of each registration.
	srv.HandleFunc("POST "+prefix+"/projects/{project}/locations/{location}/blueGreenDeployments", handleSQLCreateBlueGreenDeployment)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/locations/{location}/blueGreenDeployments", handleSQLListBlueGreenDeployments)
	srv.HandleFunc("GET "+prefix+"/projects/{project}/locations/{location}/blueGreenDeployments/{deployment}", handleSQLGetBlueGreenDeployment)
	srv.HandleFunc("DELETE "+prefix+"/projects/{project}/locations/{location}/blueGreenDeployments/{deployment}", handleSQLDeleteBlueGreenDeployment)
	srv.HandleFunc("POST "+prefix+"/projects/{project}/locations/{location}/blueGreenDeployments/{deploymentAction}", handleSQLBlueGreenDeploymentAction)
}

func handleSQLCreateBlueGreenDeployment(w http.ResponseWriter, r *http.Request) {
	project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
	var body SQLBlueGreenDeployment
	if err := sim.ReadJSON(r, &body); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	id := r.URL.Query().Get("blueGreenDeploymentId")
	if id == "" {
		GCPError(w, http.StatusBadRequest, "blueGreenDeploymentId is required", "INVALID_ARGUMENT")
		return
	}
	if body.SourceInstance == "" {
		GCPError(w, http.StatusBadRequest, "sourceInstance is required", "INVALID_ARGUMENT")
		return
	}
	// The source is named by instance id; Cloud SQL rejects a deployment whose
	// source does not exist rather than provisioning an empty green.
	source, ok := sqlInstances.Get(sqlInstanceKey(project, sqlBlueGreenSourceID(body.SourceInstance)))
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"Cloud SQL instance %q not found", body.SourceInstance)
		return
	}
	key := sqlBlueGreenKey(project, location, id)
	if _, exists := sqlBlueGreenDeployments.Get(key); exists {
		GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
			"blue-green deployment %q already exists", id)
		return
	}

	// The green instance is a real copy of the source, in the same store, so
	// every other Cloud SQL read sees it exactly as it sees the source.
	green := source
	green.Name = sqlBlueGreenGreenInstance(id)
	green.State = "RUNNABLE"
	green.InstanceType = "READ_REPLICA_INSTANCE"
	green.CreateTime = nowTimestamp()
	if body.RequestedConfig != nil && body.RequestedConfig.DatabaseVersion != "" {
		green.DatabaseVersion = body.RequestedConfig.DatabaseVersion
	}
	sqlInstances.Put(sqlInstanceKey(project, green.Name), green)

	created := nowTimestamp()
	deployment := SQLBlueGreenDeployment{
		Name:           sqlBlueGreenName(project, location, id),
		SourceInstance: body.SourceInstance,
		State:          "SWITCHOVER_READY",
		CreateTime:     created,
		Description:    body.Description,
		DeploymentTasks: &SQLDeploymentTasks{Task: []SQLDeploymentTask{{
			Type: "PROVISION", State: "SUCCEEDED",
			StartTime: created, EndTime: created,
		}}},
		RequestedConfig: body.RequestedConfig,
		project:         project,
		location:        location,
	}
	sqlBlueGreenDeployments.Put(key, deployment)
	sim.WriteJSON(w, http.StatusOK, newSQLOperation(project, "CREATE_BLUE_GREEN_DEPLOYMENT", green.Name))
}

// sqlBlueGreenSourceID reads the instance id out of a sourceInstance, which
// clients send either bare or as a full resource name.
func sqlBlueGreenSourceID(sourceInstance string) string {
	if index := strings.LastIndex(sourceInstance, "/"); index >= 0 {
		return sourceInstance[index+1:]
	}
	return sourceInstance
}

func handleSQLGetBlueGreenDeployment(w http.ResponseWriter, r *http.Request) {
	project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
	id := sim.PathParam(r, "deployment")
	deployment, ok := sqlBlueGreenDeployments.Get(sqlBlueGreenKey(project, location, id))
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "blue-green deployment %q not found", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, deployment)
}

func handleSQLListBlueGreenDeployments(w http.ResponseWriter, r *http.Request) {
	project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
	items := sqlBlueGreenDeployments.Filter(func(d SQLBlueGreenDeployment) bool {
		return d.project == project && d.location == location
	})
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	if items == nil {
		items = []SQLBlueGreenDeployment{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"blueGreenDeployments": items})
}

func handleSQLDeleteBlueGreenDeployment(w http.ResponseWriter, r *http.Request) {
	project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
	id := sim.PathParam(r, "deployment")
	key := sqlBlueGreenKey(project, location, id)
	deployment, ok := sqlBlueGreenDeployments.Get(key)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "blue-green deployment %q not found", id)
		return
	}
	// deleteOldSource retires the instance the switchover replaced; without it
	// the deployment is dropped and both instances remain.
	if gcpQueryBool(r, "deleteOldSource") && deployment.SwitchoverTargetInstance != "" {
		sqlInstances.Delete(sqlInstanceKey(project, sqlBlueGreenSourceID(deployment.SwitchoverTargetInstance)))
	}
	if deployment.State != "SWITCHOVER_COMPLETED" {
		sqlInstances.Delete(sqlInstanceKey(project, sqlBlueGreenGreenInstance(id)))
	}
	sqlBlueGreenDeployments.Delete(key)
	sim.WriteJSON(w, http.StatusOK,
		newSQLOperation(project, "DELETE_BLUE_GREEN_DEPLOYMENT", sqlBlueGreenGreenInstance(id)))
}

// handleSQLBlueGreenDeploymentAction serves the colon verbs on a deployment.
func handleSQLBlueGreenDeploymentAction(w http.ResponseWriter, r *http.Request) {
	project, location := sim.PathParam(r, "project"), sim.PathParam(r, "location")
	id, action, found := strings.Cut(sim.PathParam(r, "deploymentAction"), ":")
	if !found {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "method not found")
		return
	}
	if action != "switchover" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"unknown blue-green deployment action %q", action)
		return
	}
	key := sqlBlueGreenKey(project, location, id)
	deployment, ok := sqlBlueGreenDeployments.Get(key)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "blue-green deployment %q not found", id)
		return
	}
	if deployment.State != "SWITCHOVER_READY" {
		GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
			"blue-green deployment %q is %s, not SWITCHOVER_READY", id, deployment.State)
		return
	}

	// Switchover promotes the green instance into the source's name. The
	// source keeps its data under a retired name so `deleteOldSource` has
	// something to delete and a rollback has somewhere to read from.
	sourceID := sqlBlueGreenSourceID(deployment.SourceInstance)
	greenID := sqlBlueGreenGreenInstance(id)
	green, greenOK := sqlInstances.Get(sqlInstanceKey(project, greenID))
	source, sourceOK := sqlInstances.Get(sqlInstanceKey(project, sourceID))
	if !greenOK || !sourceOK {
		GCPErrorf(w, http.StatusFailedDependency, "FAILED_PRECONDITION",
			"blue-green deployment %q has no instance pair to switch over", id)
		return
	}
	retired := sourceID + "-old"
	source.Name = retired
	sqlInstances.Put(sqlInstanceKey(project, retired), source)
	green.Name = sourceID
	green.InstanceType = "CLOUD_SQL_INSTANCE"
	sqlInstances.Put(sqlInstanceKey(project, sourceID), green)
	sqlInstances.Delete(sqlInstanceKey(project, greenID))

	// The task list accumulates: the provision that built the green is still
	// part of what this deployment did.
	switchedAt := nowTimestamp()
	sqlBlueGreenDeployments.Update(key, func(current *SQLBlueGreenDeployment) {
		current.State = "SWITCHOVER_COMPLETED"
		current.SwitchoverTargetInstance = retired
		if current.DeploymentTasks == nil {
			current.DeploymentTasks = &SQLDeploymentTasks{}
		}
		current.DeploymentTasks.Task = append(current.DeploymentTasks.Task, SQLDeploymentTask{
			Type: "SWITCHOVER", State: "SUCCEEDED",
			StartTime: switchedAt, EndTime: switchedAt,
		})
	})
	sim.WriteJSON(w, http.StatusOK, newSQLOperation(project, "SWITCHOVER", sourceID))
}
