package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Container Apps revisions, replicas and the log streams a replica's
// containers and an app's system events are read through.
//
// Real API: https://learn.microsoft.com/en-us/rest/api/containerapps/container-apps-revisions
// and https://learn.microsoft.com/en-us/azure/container-apps/log-streaming.
// `az containerapp logs show` asks getAuthToken for a bearer token, reads the
// app's eventStreamEndpoint, and GETs
// <endpoint host>/subscriptions/…/containerApps/{app}/revisions/{rev}/replicas/{replica}/containers/{container}/logstream
// for console output, or the eventStreamEndpoint itself for system events.

// acaContainerLogMaxBytes is the kubelet's default containerLogMaxSize: the
// node keeps that much of a container's output, which bounds what a log
// stream's tail can reach back to.
const acaContainerLogMaxBytes = 10 << 20

// acaLog is an append-only record a log stream reads and follows.
type acaLog struct {
	mu      sync.Mutex
	entries []acaLogEntry
	first   int // sequence number of entries[0]
	size    int
	ended   bool
	changed chan struct{}
}

type acaLogEntry struct {
	at   time.Time
	text string
}

func newACALog() *acaLog { return &acaLog{changed: make(chan struct{})} }

func (l *acaLog) append(e acaLogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return
	}
	l.entries = append(l.entries, e)
	l.size += len(e.text)
	for l.size > acaContainerLogMaxBytes && len(l.entries) > 1 {
		l.size -= len(l.entries[0].text)
		l.entries = l.entries[1:]
		l.first++
	}
	close(l.changed)
	l.changed = make(chan struct{})
}

// end marks the log complete: its writer has exited and nothing follows.
func (l *acaLog) end() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return
	}
	l.ended = true
	close(l.changed)
	l.changed = make(chan struct{})
}

// since returns the entries from sequence number from on, the sequence number
// after the last of them, whether the log has ended, and a channel that closes
// on the next append or end.
func (l *acaLog) since(from int) ([]acaLogEntry, int, bool, <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	from = max(from, l.first)
	out := append([]acaLogEntry(nil), l.entries[from-l.first:]...)
	return out, l.first + len(l.entries), l.ended, l.changed
}

// acaReplica is one replica of a container app's revision: a pod running the
// template's containers.
type acaReplica struct {
	name       string
	revision   string
	created    time.Time
	containers []*acaReplicaContainer
}

type acaReplicaContainer struct {
	name string
	log  *acaLog

	mu          sync.Mutex
	containerID string
	terminated  bool
	exitCode    int
}

// sink feeds the container's output to its log stream and to the workspace's
// ContainerAppConsoleLogs table.
func (c *acaReplicaContainer) sink(appName string) sim.LogSink {
	return acaReplicaContainerSink{container: c, appName: appName}
}

type acaReplicaContainerSink struct {
	container *acaReplicaContainer
	appName   string
}

func (s acaReplicaContainerSink) WriteLog(line sim.LogLine) {
	at := line.Timestamp
	if at.IsZero() {
		at = time.Now()
	}
	s.container.log.append(acaLogEntry{at: at.UTC(), text: line.Text})
	injectContainerAppReplicaLog(s.appName, line.Text)
}

// track records the started container and, once it exits, its exit code; the
// container's log ends with it.
func (c *acaReplicaContainer) track(handle *sim.ContainerHandle, resourceID, appName, revision, replica string) {
	c.mu.Lock()
	c.containerID = handle.ContainerID
	c.mu.Unlock()
	acaRecordSystemEvent(resourceID, appName, revision, replica, "ContainerCreated", fmt.Sprintf("Created container '%s'", c.name))
	acaRecordSystemEvent(resourceID, appName, revision, replica, "ContainerStarted", fmt.Sprintf("Started container '%s'", c.name))
	go func() {
		result := handle.Wait()
		c.mu.Lock()
		c.terminated = true
		c.exitCode = result.ExitCode
		c.mu.Unlock()
		c.log.end()
		reason := "Completed"
		if result.ExitCode != 0 {
			reason = "Error"
		}
		acaRecordSystemEvent(resourceID, appName, revision, replica, "ContainerTerminated",
			fmt.Sprintf("Container '%s' was terminated with exit code '%d' and reason '%s'", c.name, result.ExitCode, reason))
	}()
}

func newACAReplica(revision string, containerNames []string) *acaReplica {
	replica := &acaReplica{
		name:     fmt.Sprintf("%s-%s-%s", revision, randomSuffix(10), randomSuffix(5)),
		revision: revision,
		created:  time.Now().UTC(),
	}
	for _, name := range containerNames {
		replica.containers = append(replica.containers, &acaReplicaContainer{name: name, log: newACALog()})
	}
	return replica
}

// acaAppReplicas holds each revision's current replicas, keyed by revision ID.
var acaAppReplicas sync.Map // map[revisionID][]*acaReplica

func acaCurrentReplicas(revisionID string) []*acaReplica {
	if v, ok := acaAppReplicas.Load(revisionID); ok {
		replicas, _ := v.([]*acaReplica)
		return replicas
	}
	return nil
}

// acaAppSystemLogs holds each app's system events, keyed by resource ID.
var acaAppSystemLogs sync.Map // map[resourceID]*acaLog

func acaSystemLog(resourceID string) *acaLog {
	v, _ := acaAppSystemLogs.LoadOrStore(resourceID, newACALog())
	log, _ := v.(*acaLog)
	return log
}

// acaSystemEvent is one line of the system log stream, in the shape the
// ContainerAppSystemLogs table records.
type acaSystemEvent struct {
	TimeStamp        string `json:"TimeStamp"`
	Type             string `json:"Type"`
	ContainerAppName string `json:"ContainerAppName"`
	RevisionName     string `json:"RevisionName"`
	ReplicaName      string `json:"ReplicaName"`
	Msg              string `json:"Msg"`
	Reason           string `json:"Reason"`
	EventSource      string `json:"EventSource"`
	Count            int    `json:"Count"`
}

func acaRecordSystemEvent(resourceID, appName, revision, replica, reason, message string) {
	now := time.Now().UTC()
	line, err := json.Marshal(acaSystemEvent{
		TimeStamp:        now.Format(time.RFC3339Nano),
		Type:             "Normal",
		ContainerAppName: appName,
		RevisionName:     revision,
		ReplicaName:      replica,
		Msg:              message,
		Reason:           reason,
		EventSource:      "ContainerAppController",
		Count:            1,
	})
	if err != nil {
		return
	}
	acaSystemLog(resourceID).append(acaLogEntry{at: now, text: string(line)})
}

// ContainerAppRevision mirrors armappcontainers.Revision.
type ContainerAppRevision struct {
	ID         string                    `json:"id"`
	Name       string                    `json:"name"`
	Type       string                    `json:"type"`
	Properties ContainerAppRevisionProps `json:"properties"`
}

// ContainerAppRevisionProps mirrors armappcontainers.RevisionProperties.
type ContainerAppRevisionProps struct {
	CreatedTime       string                `json:"createdTime,omitempty"`
	LastActiveTime    string                `json:"lastActiveTime,omitempty"`
	Fqdn              string                `json:"fqdn,omitempty"`
	Template          *ContainerAppTemplate `json:"template,omitempty"`
	Active            bool                  `json:"active"`
	Replicas          int32                 `json:"replicas"`
	TrafficWeight     int32                 `json:"trafficWeight"`
	HealthState       string                `json:"healthState"`
	ProvisioningState string                `json:"provisioningState"`
	RunningState      string                `json:"runningState"`
}

// ContainerAppReplica mirrors armappcontainers.Replica.
type ContainerAppReplica struct {
	ID         string                   `json:"id"`
	Name       string                   `json:"name"`
	Type       string                   `json:"type"`
	Properties ContainerAppReplicaProps `json:"properties"`
}

// ContainerAppReplicaProps mirrors armappcontainers.ReplicaProperties.
type ContainerAppReplicaProps struct {
	CreatedTime         string                         `json:"createdTime"`
	RunningState        string                         `json:"runningState"`
	RunningStateDetails string                         `json:"runningStateDetails,omitempty"`
	Containers          []ContainerAppReplicaContainer `json:"containers"`
}

// ContainerAppReplicaContainer mirrors armappcontainers.ReplicaContainer.
type ContainerAppReplicaContainer struct {
	Name                string `json:"name"`
	ContainerID         string `json:"containerId,omitempty"`
	Ready               bool   `json:"ready"`
	Started             bool   `json:"started"`
	RestartCount        int32  `json:"restartCount"`
	RunningState        string `json:"runningState"`
	RunningStateDetails string `json:"runningStateDetails,omitempty"`
	LogStreamEndpoint   string `json:"logStreamEndpoint,omitempty"`
}

func acaRevisionView(rev acaRevision, weights map[string]int32) ContainerAppRevision {
	view := ContainerAppRevision{
		ID:   rev.ID,
		Name: rev.Name,
		Type: "Microsoft.App/containerApps/revisions",
		Properties: ContainerAppRevisionProps{
			CreatedTime:       rev.CreatedTime.Format(time.RFC3339),
			Fqdn:              rev.Fqdn,
			Template:          rev.Template,
			Active:            rev.Active,
			TrafficWeight:     weights[rev.Name],
			HealthState:       "None",
			ProvisioningState: "Deprovisioned",
			RunningState:      "Stopped",
		},
	}
	if !rev.Active {
		view.Properties.TrafficWeight = 0
		view.Properties.LastActiveTime = rev.LastActiveTime.Format(time.RFC3339)
		return view
	}
	replicas := acaCurrentReplicas(rev.ID)
	running := 0
	for _, replica := range replicas {
		if acaReplicaRunning(replica) {
			running++
		}
	}
	minReplicas := int32(1)
	if rev.Template != nil && rev.Template.Scale != nil && rev.Template.Scale.MinReplicas != nil {
		minReplicas = *rev.Template.Scale.MinReplicas
	}
	runningState, healthState := "Running", "Healthy"
	switch {
	case len(replicas) == 0 && minReplicas > 0:
		runningState, healthState = "Stopped", "None"
	case len(replicas) == 0:
		healthState = "None"
	case running == 0:
		runningState, healthState = "Failed", "Unhealthy"
	case running < len(replicas):
		runningState, healthState = "Degraded", "Unhealthy"
	}
	view.Properties.Replicas = int32(len(replicas))
	view.Properties.HealthState = healthState
	view.Properties.ProvisioningState = "Provisioned"
	view.Properties.RunningState = runningState
	return view
}

func acaReplicaRunning(replica *acaReplica) bool {
	for _, c := range replica.containers {
		c.mu.Lock()
		terminated := c.terminated
		c.mu.Unlock()
		if !terminated {
			return true
		}
	}
	return false
}

func acaReplicaView(r *http.Request, app ContainerApp, replica *acaReplica) ContainerAppReplica {
	sub, rg := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName")
	view := ContainerAppReplica{
		ID:   app.ID + "/revisions/" + replica.revision + "/replicas/" + replica.name,
		Name: replica.name,
		Type: "Microsoft.App/containerApps/revisions/replicas",
		Properties: ContainerAppReplicaProps{
			CreatedTime:  replica.created.Format(time.RFC3339),
			RunningState: "NotRunning",
			Containers:   []ContainerAppReplicaContainer{},
		},
	}
	if acaReplicaRunning(replica) {
		view.Properties.RunningState = "Running"
	}
	for _, c := range replica.containers {
		c.mu.Lock()
		holder := ContainerAppReplicaContainer{
			Name:         c.name,
			ContainerID:  c.containerID,
			Ready:        !c.terminated,
			Started:      c.containerID != "",
			RunningState: "Running",
			LogStreamEndpoint: fmt.Sprintf("%s://%s/subscriptions/%s/resourceGroups/%s/containerApps/%s/revisions/%s/replicas/%s/containers/%s/logstream",
				azureRequestScheme(r), r.Host, sub, rg, app.Name, replica.revision, replica.name, c.name),
		}
		if c.terminated {
			holder.RunningState = "Terminated"
			holder.RunningStateDetails = "Completed"
			if c.exitCode != 0 {
				holder.RunningStateDetails = "Error"
			}
		}
		c.mu.Unlock()
		view.Properties.Containers = append(view.Properties.Containers, holder)
	}
	return view
}

// acaAppAuthTokens records the tokens getAuthtoken issued, which the log
// streams accept as their bearer credential.
var acaAppAuthTokens sync.Map // map[token]acaIssuedToken

type acaIssuedToken struct {
	resourceID string
	expires    time.Time
}

func acaAuthorizedForApp(r *http.Request, resourceID string) bool {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	v, ok := acaAppAuthTokens.Load(header[len(prefix):])
	if !ok {
		return false
	}
	issued, ok := v.(acaIssuedToken)
	return ok && issued.resourceID == resourceID && time.Now().Before(issued.expires)
}

func registerContainerAppsReplicas(srv *sim.Server, apps sim.Store[ContainerApp]) {
	const appPath = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/containerApps/{appName}"
	const streamPath = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/containerApps/{appName}"

	lookupApp := func(w http.ResponseWriter, r *http.Request) (ContainerApp, bool) {
		sub, rg, name := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "appName")
		app, ok := apps.Get(fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/containerApps/%s", sub, rg, name))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/containerApps/%s' under resource group '%s' was not found.", name, rg)
		}
		return app, ok
	}
	lookupRevision := func(w http.ResponseWriter, r *http.Request) (ContainerApp, acaRevision, bool) {
		app, ok := lookupApp(w, r)
		if !ok {
			return app, acaRevision{}, false
		}
		rev, ok := acaAppRevision(app.ID, sim.PathParam(r, "revisionName"))
		if !ok {
			AzureErrorf(w, "ContainerAppRevisionNotFound", http.StatusNotFound,
				"Revision '%s' was not found in container app '%s'.", sim.PathParam(r, "revisionName"), app.Name)
		}
		return app, rev, ok
	}
	lookupReplica := func(w http.ResponseWriter, r *http.Request) (ContainerApp, *acaReplica, bool) {
		app, rev, ok := lookupRevision(w, r)
		if !ok {
			return app, nil, false
		}
		name := sim.PathParam(r, "replicaName")
		for _, replica := range acaCurrentReplicas(rev.ID) {
			if replica.name == name {
				return app, replica, true
			}
		}
		AzureErrorf(w, "ContainerAppReplicaNotFound", http.StatusNotFound,
			"Replica '%s' was not found in revision '%s'.", name, rev.Name)
		return app, nil, false
	}

	srv.HandleFunc("GET "+appPath+"/revisions", func(w http.ResponseWriter, r *http.Request) {
		app, ok := lookupApp(w, r)
		if !ok {
			return
		}
		weights := acaTrafficWeights(app)
		views := []ContainerAppRevision{}
		for _, rev := range acaAppRevisions(app.ID) {
			views = append(views, acaRevisionView(rev, weights))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": views})
	})
	srv.HandleFunc("GET "+appPath+"/revisions/{revisionName}", func(w http.ResponseWriter, r *http.Request) {
		app, rev, ok := lookupRevision(w, r)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, acaRevisionView(rev, acaTrafficWeights(app)))
	})
	// Restart, activate and deactivate answer 200 with a JSON string naming
	// what succeeded, which the Azure CLI prints.
	srv.HandleFunc("POST "+appPath+"/revisions/{revisionName}/restart", func(w http.ResponseWriter, r *http.Request) {
		app, rev, ok := lookupRevision(w, r)
		if !ok {
			return
		}
		if rev.Active {
			if err := startACARevisionReplicas(r.Context(), app, rev); err != nil {
				AzureErrorf(w, "ContainerAppRevisionFailed", http.StatusInternalServerError,
					"failed to restart revision %s: %v", rev.Name, err)
				return
			}
		}
		sim.WriteJSON(w, http.StatusOK, "Restart succeeded")
	})
	srv.HandleFunc("POST "+appPath+"/revisions/{revisionName}/activate", func(w http.ResponseWriter, r *http.Request) {
		app, rev, ok := lookupRevision(w, r)
		if !ok {
			return
		}
		if !rev.Active {
			if err := startACARevisionReplicas(r.Context(), app, rev); err != nil {
				AzureErrorf(w, "ContainerAppRevisionFailed", http.StatusInternalServerError,
					"failed to activate revision %s: %v", rev.Name, err)
				return
			}
			rev.Active = true
			acaRevisions.Put(rev.ID, rev)
			// A single-revision app deprovisions any revision but its latest
			// again as soon as it is activated.
			acaEnforceRevisionMode(app)
		}
		sim.WriteJSON(w, http.StatusOK, "Activate succeeded")
	})
	srv.HandleFunc("POST "+appPath+"/revisions/{revisionName}/deactivate", func(w http.ResponseWriter, r *http.Request) {
		app, rev, ok := lookupRevision(w, r)
		if !ok {
			return
		}
		if rev.Active {
			acaDeactivateRevision(rev)
			acaPruneInactiveRevisions(app)
		}
		sim.WriteJSON(w, http.StatusOK, "Deactivate succeeded")
	})

	srv.HandleFunc("GET "+appPath+"/revisions/{revisionName}/replicas", func(w http.ResponseWriter, r *http.Request) {
		app, rev, ok := lookupRevision(w, r)
		if !ok {
			return
		}
		views := []ContainerAppReplica{}
		for _, replica := range acaCurrentReplicas(rev.ID) {
			views = append(views, acaReplicaView(r, app, replica))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": views})
	})
	getReplica := func(w http.ResponseWriter, r *http.Request) {
		app, replica, ok := lookupReplica(w, r)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, acaReplicaView(r, app, replica))
	}
	srv.HandleFunc("GET "+appPath+"/revisions/{revisionName}/replicas/{replicaName}", getReplica)
	// The Azure CLI asks for a replica with a trailing slash.
	srv.HandleFunc("GET "+appPath+"/revisions/{revisionName}/replicas/{replicaName}/{$}", getReplica)

	srv.HandleFunc("GET "+streamPath+"/revisions/{revisionName}/replicas/{replicaName}/containers/{containerName}/logstream", func(w http.ResponseWriter, r *http.Request) {
		app, replica, ok := lookupReplica(w, r)
		if !ok {
			return
		}
		if !acaAuthorizedForApp(r, app.ID) {
			AzureError(w, "Unauthorized", "The log stream requires a token the container app's getAuthToken issued.", http.StatusUnauthorized)
			return
		}
		name := sim.PathParam(r, "containerName")
		var container *acaReplicaContainer
		for _, c := range replica.containers {
			if c.name == name {
				container = c
			}
		}
		if container == nil {
			AzureErrorf(w, "ContainerNotFound", http.StatusNotFound,
				"Container '%s' was not found in replica '%s'.", name, replica.name)
			return
		}
		header := []acaLogEntry{
			{at: time.Now().UTC(), text: fmt.Sprintf("Connecting to the container '%s'...", name)},
			{at: time.Now().UTC(), text: fmt.Sprintf("Successfully Connected to container: '%s' [Revision: '%s', Replica: '%s']", name, replica.revision, replica.name)},
		}
		acaServeLogStream(w, r, container.log, header, func(e acaLogEntry, output string) string {
			if output == "text" {
				return e.at.Format(time.RFC3339Nano) + " " + e.text
			}
			line, _ := json.Marshal(struct {
				TimeStamp string `json:"TimeStamp"`
				Log       string `json:"Log"`
			}{e.at.Format(time.RFC3339Nano), e.text})
			return string(line)
		})
	})

	srv.HandleFunc("GET "+streamPath+"/eventstream", func(w http.ResponseWriter, r *http.Request) {
		app, ok := lookupApp(w, r)
		if !ok {
			return
		}
		if !acaAuthorizedForApp(r, app.ID) {
			AzureError(w, "Unauthorized", "The event stream requires a token the container app's getAuthToken issued.", http.StatusUnauthorized)
			return
		}
		acaServeLogStream(w, r, acaSystemLog(app.ID), nil, func(e acaLogEntry, _ string) string { return e.text })
	})
}

// acaServeLogStream writes a log's tail as newline-delimited lines and, when
// the request follows, every line appended after it until the log ends or the
// client goes away.
func acaServeLogStream(w http.ResponseWriter, r *http.Request, log *acaLog, header []acaLogEntry, format func(acaLogEntry, string) string) {
	query := r.URL.Query()
	follow := query.Get("follow") == "true"
	output := query.Get("output")
	tail := -1
	if raw := query.Get("tailLines"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			AzureErrorf(w, "InvalidParameter", http.StatusBadRequest, "tailLines must be a non-negative integer, got %q.", raw)
			return
		}
		tail = n
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	write := func(entries []acaLogEntry) bool {
		for _, e := range entries {
			if _, err := fmt.Fprintln(w, format(e, output)); err != nil {
				return false
			}
		}
		return controller.Flush() == nil
	}
	if !write(header) {
		return
	}
	entries, next, ended, changed := log.since(0)
	if tail >= 0 && len(entries) > tail {
		entries = entries[len(entries)-tail:]
	}
	if !write(entries) || !follow {
		return
	}
	for !ended {
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		}
		entries, next, ended, changed = log.since(next)
		if !write(entries) {
			return
		}
	}
}
