package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	dockercontainer "github.com/docker/docker/api/types/container"
	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
	"github.com/gorilla/websocket"
)

// Microsoft.ContainerInstance/containerGroups. Container groups are ARM
// resources; in Docker runtime mode the sim also starts real local
// containers for the group's container specs and records their actual
// stdout/stderr for the ACI logs endpoint.

type ACIContainerGroup struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location,omitempty"`
	Identity   map[string]any    `json:"identity,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
	Zones      []string          `json:"zones,omitempty"`
}

// ACIContainerState is the instance-view state of an Azure Container Instances
// container/group. Using a named type makes a mistyped state literal a compile
// error.
type ACIContainerState string

const (
	ACIStatePending    ACIContainerState = "Pending"
	ACIStateRunning    ACIContainerState = "Running"
	ACIStateTerminated ACIContainerState = "Terminated"
	ACIStateStopped    ACIContainerState = "Stopped"
)

type aciRuntimeRecord struct {
	ContainerID string            `json:"containerId"`
	State       ACIContainerState `json:"state"`
	ExitCode    int               `json:"exitCode,omitempty"`
	StartTime   string            `json:"startTime,omitempty"`
	FinishTime  string            `json:"finishTime,omitempty"`
}

type aciExecSession struct {
	ContainerID string `json:"containerId"`
	Command     string `json:"command"`
	Password    string `json:"password"`
}

// aciAttachSession is a pending websocket attach to a running container's
// standard streams (the analogue of `docker attach`).
type aciAttachSession struct {
	ContainerID string `json:"containerId"`
	Password    string `json:"password"`
}

var (
	aciContainerGroups sim.Store[ACIContainerGroup]
	aciRuntimeRecords  sim.Store[aciRuntimeRecord]
	aciExecSessions    sync.Map
	aciAttachSessions  sync.Map
	aciLogMu           sync.Mutex
	// aciLogs holds each container's log lines, keyed by runtime key. It is
	// store-backed like the container groups themselves: real Azure serves a
	// finished container's logs long after the platform restarts, so a
	// SIM_PERSIST restart must not blank the Logs API while the group persists.
	aciLogs sim.Store[[]string]
)

func registerContainerInstances(srv *sim.Server) {
	aciContainerGroups = sim.MakeStore[ACIContainerGroup](srv.DB(), "aci_container_groups")
	aciRuntimeRecords = sim.MakeStore[aciRuntimeRecord](srv.DB(), "aci_runtime_records")
	aciLogs = sim.MakeStore[[]string](srv.DB(), "aci_container_logs")

	const base = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ContainerInstance/containerGroups"
	srv.HandleFunc("PUT "+base+"/{containerGroupName}", handleACIContainerGroupPut)
	srv.HandleFunc("PATCH "+base+"/{containerGroupName}", handleACIContainerGroupPatch)
	srv.HandleFunc("GET "+base+"/{containerGroupName}", handleACIContainerGroupGet)
	srv.HandleFunc("DELETE "+base+"/{containerGroupName}", handleACIContainerGroupDelete)
	srv.HandleFunc("GET "+base, handleACIContainerGroupListByRG)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.ContainerInstance/containerGroups", handleACIContainerGroupListBySubscription)
	srv.HandleFunc("POST "+base+"/{containerGroupName}/stop", handleACIContainerGroupStop)
	srv.HandleFunc("POST "+base+"/{containerGroupName}/start", handleACIContainerGroupStart)
	srv.HandleFunc("POST "+base+"/{containerGroupName}/restart", handleACIContainerGroupRestart)
	srv.HandleFunc("GET "+base+"/{containerGroupName}/containers/{containerName}/logs", handleACIContainerLogs)
	srv.HandleFunc("POST "+base+"/{containerGroupName}/containers/{containerName}/exec", handleACIContainerExec)
	srv.HandleFunc("GET "+base+"/{containerGroupName}/containers/{containerName}/execSessions/{sessionID}", handleACIContainerExecSession)

	// Provider operation metadata.
	srv.HandleFunc("GET /providers/Microsoft.ContainerInstance/operations", handleACIOperationsList)
	// Per-region capacity, capabilities and cached images.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.ContainerInstance/locations/{location}/usages", handleACILocationUsages)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.ContainerInstance/locations/{location}/capabilities", handleACILocationCapabilities)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.ContainerInstance/locations/{location}/cachedImages", handleACILocationCachedImages)
	// Outbound network dependency endpoints for a container group.
	srv.HandleFunc("GET "+base+"/{containerGroupName}/outboundNetworkDependenciesEndpoints", handleACIOutboundNetworkDeps)
	// Attach to a running container's standard streams (websocket handshake).
	srv.HandleFunc("POST "+base+"/{containerGroupName}/containers/{containerName}/attach", handleACIContainerAttach)
	srv.HandleFunc("GET "+base+"/{containerGroupName}/containers/{containerName}/attachSessions/{sessionID}", handleACIContainerAttachSession)
	// Delete the subnet<->container-group service association link.
	srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/virtualNetworks/{virtualNetworkName}/subnets/{subnetName}/providers/Microsoft.ContainerInstance/serviceAssociationLinks/default", handleACISubnetServiceAssociationLinkDelete)
}

func aciContainerGroupID(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerInstance/containerGroups/%s", sub, rg, name)
}

func aciRuntimeKey(groupID, containerName string) string {
	return groupID + "/containers/" + containerName
}

func handleACIContainerGroupPut(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "containerGroupName")
	var req ACIContainerGroup
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if req.Properties == nil {
		sim.AzureError(w, "InvalidContainerGroup", "properties are required.", http.StatusBadRequest)
		return
	}
	id := aciContainerGroupID(sub, rg, name)
	group := ACIContainerGroup{
		ID:         id,
		Name:       name,
		Type:       "Microsoft.ContainerInstance/containerGroups",
		Location:   req.Location,
		Identity:   req.Identity,
		Tags:       req.Tags,
		Zones:      req.Zones,
		Properties: cloneMap(req.Properties),
	}
	aciApplyGroupDefaults(r, &group)
	aciContainerGroups.Put(id, group)
	if err := aciStartGroupContainers(group); err != nil {
		aciContainerGroups.Delete(id)
		sim.AzureErrorf(w, "ContainerGroupDeploymentFailed", http.StatusBadRequest, "%v", err)
		return
	}
	// Real Azure never echoes osProfile/env secureValue back; strip it from
	// the stored group once the runtime has consumed it.
	aciContainerGroups.Update(id, func(stored *ACIContainerGroup) {
		aciStripSecureValues(stored)
	})
	group, _ = aciContainerGroups.Get(id)
	opID := issueAzureAsyncOperation(func() {
		aciContainerGroups.Update(id, func(stored *ACIContainerGroup) {
			aciSetGroupProvisioning(stored, "Succeeded")
		})
	})
	opURL := azureAsyncOperationHeader(r, sub, "Microsoft.ContainerInstance", group.Location, "operationStatuses", opID, r.URL.Query().Get("api-version"))
	writeAzureAsyncCreateHeaders(w, opURL, azureCurrentRequestURL(r))
	sim.WriteJSON(w, http.StatusCreated, group)
}

func handleACIContainerGroupPatch(w http.ResponseWriter, r *http.Request) {
	id := aciContainerGroupID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "containerGroupName"))
	var req ACIContainerGroup
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	if !aciContainerGroups.Update(id, func(group *ACIContainerGroup) {
		if req.Tags != nil {
			group.Tags = req.Tags
		}
	}) {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Container group %q not found.", sim.PathParam(r, "containerGroupName"))
		return
	}
	group, _ := aciContainerGroups.Get(id)
	sim.WriteJSON(w, http.StatusOK, group)
}

func handleACIContainerGroupGet(w http.ResponseWriter, r *http.Request) {
	id := aciContainerGroupID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "containerGroupName"))
	group, ok := aciContainerGroups.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Container group %q not found.", sim.PathParam(r, "containerGroupName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, group)
}

func handleACIContainerGroupDelete(w http.ResponseWriter, r *http.Request) {
	id := aciContainerGroupID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "containerGroupName"))
	group, ok := aciContainerGroups.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Container group %q not found.", sim.PathParam(r, "containerGroupName"))
		return
	}
	aciStopGroupContainers(group)
	aciContainerGroups.Delete(id)
	w.WriteHeader(http.StatusOK)
}

func handleACIContainerGroupListByRG(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerInstance/containerGroups/", sub, rg)
	writeACIContainerGroupList(w, prefix)
}

func handleACIContainerGroupListBySubscription(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/", sub)
	writeACIContainerGroupList(w, prefix)
}

func writeACIContainerGroupList(w http.ResponseWriter, prefix string) {
	out := aciContainerGroups.Filter(func(group ACIContainerGroup) bool { return strings.HasPrefix(group.ID, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if out == nil {
		out = []ACIContainerGroup{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleACIContainerGroupStop(w http.ResponseWriter, r *http.Request) {
	id := aciContainerGroupID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "containerGroupName"))
	group, ok := aciContainerGroups.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Container group %q not found.", sim.PathParam(r, "containerGroupName"))
		return
	}
	aciStopGroupContainers(group)
	aciContainerGroups.Update(id, func(stored *ACIContainerGroup) {
		aciSetGroupState(stored, ACIStateStopped)
	})
	w.WriteHeader(http.StatusNoContent)
}

func handleACIContainerGroupStart(w http.ResponseWriter, r *http.Request) {
	id := aciContainerGroupID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "containerGroupName"))
	group, ok := aciContainerGroups.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Container group %q not found.", sim.PathParam(r, "containerGroupName"))
		return
	}
	if err := aciStartGroupContainers(group); err != nil {
		sim.AzureErrorf(w, "ContainerGroupStartFailed", http.StatusBadRequest, "%v", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleACIContainerGroupRestart(w http.ResponseWriter, r *http.Request) {
	id := aciContainerGroupID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "containerGroupName"))
	group, ok := aciContainerGroups.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Container group %q not found.", sim.PathParam(r, "containerGroupName"))
		return
	}
	aciStopGroupContainers(group)
	if err := aciStartGroupContainers(group); err != nil {
		sim.AzureErrorf(w, "ContainerGroupRestartFailed", http.StatusBadRequest, "%v", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleACIContainerLogs(w http.ResponseWriter, r *http.Request) {
	id := aciContainerGroupID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "containerGroupName"))
	containerName := sim.PathParam(r, "containerName")
	if _, ok := aciContainerGroups.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Container group %q not found.", sim.PathParam(r, "containerGroupName"))
		return
	}
	key := aciRuntimeKey(id, containerName)
	lines, _ := aciLogs.Get(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"content": strings.Join(lines, "\n")})
}

func handleACIContainerExec(w http.ResponseWriter, r *http.Request) {
	id := aciContainerGroupID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "containerGroupName"))
	containerName := sim.PathParam(r, "containerName")
	if _, ok := aciContainerGroups.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Container group %q not found.", sim.PathParam(r, "containerGroupName"))
		return
	}
	var req struct {
		Command string `json:"command"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	req.Command = strings.TrimSpace(req.Command)
	if req.Command == "" {
		sim.AzureError(w, "BadRequest", "command is required.", http.StatusBadRequest)
		return
	}
	rec, ok := aciRuntimeRecords.Get(aciRuntimeKey(id, containerName))
	if !ok || rec.ContainerID == "" || rec.State != ACIStateRunning {
		sim.AzureErrorf(w, "ContainerNotRunning", http.StatusBadRequest, "Container %q is not running.", containerName)
		return
	}

	sessionID := generateUUID()
	password := generateUUID()
	aciExecSessions.Store(sessionID, aciExecSession{
		ContainerID: rec.ContainerID,
		Command:     req.Command,
		Password:    password,
	})
	scheme := "ws"
	if strings.EqualFold(azureRequestScheme(r), "https") {
		scheme = "wss"
	}
	query := url.Values{}
	query.Set("password", password)
	if apiVersion := r.URL.Query().Get("api-version"); apiVersion != "" {
		query.Set("api-version", apiVersion)
	}
	uri := fmt.Sprintf("%s://%s%s/containers/%s/execSessions/%s?%s",
		scheme, r.Host, aciContainerGroupID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "containerGroupName")),
		containerName, sessionID, query.Encode())
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"webSocketUri": uri,
		"password":     password,
	})
}

func handleACIContainerExecSession(w http.ResponseWriter, r *http.Request) {
	sessionID := sim.PathParam(r, "sessionID")
	v, ok := aciExecSessions.LoadAndDelete(sessionID)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Exec session %q not found.", sessionID)
		return
	}
	session, ok := v.(aciExecSession)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Exec session %q not found.", sessionID)
		return
	}
	if session.Password != "" && r.URL.Query().Get("password") != session.Password && r.Header.Get("Authorization") != session.Password {
		sim.AzureError(w, "Unauthorized", "Invalid exec session password.", http.StatusUnauthorized)
		return
	}
	conn, err := acaWSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close() //nolint:errcheck

	cli := sim.DockerClient()
	if cli == nil {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "docker client not initialised"))
		return
	}
	command := strings.Fields(session.Command)
	if len(command) == 0 {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "command is required"))
		return
	}
	execCfg := dockercontainer.ExecOptions{
		Cmd:          command,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	}
	ctx := r.Context()
	execResp, err := cli.ContainerExecCreate(ctx, session.ContainerID, execCfg)
	if err != nil {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()))
		return
	}
	attach, err := cli.ContainerExecAttach(ctx, execResp.ID, dockercontainer.ExecAttachOptions{})
	if err != nil {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()))
		return
	}
	defer attach.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, rerr := attach.Reader.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if _, werr := attach.Conn.Write(msg); werr != nil {
			break
		}
	}
	_ = attach.CloseWrite()
	<-done
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(5*time.Second),
	)
}

// handleACIOperationsList returns the Microsoft.ContainerInstance resource
// provider's published operation metadata (Operations_List).
func handleACIOperationsList(w http.ResponseWriter, _ *http.Request) {
	op := func(name, resource, operation, description string) map[string]any {
		return map[string]any{
			"name": name,
			"display": map[string]any{
				"provider":    "Microsoft Container Instance",
				"resource":    resource,
				"operation":   operation,
				"description": description,
			},
			"origin": "User, System",
		}
	}
	value := []map[string]any{
		op("Microsoft.ContainerInstance/register/action", "Microsoft Container Instance", "Registers the subscription for the container instance resource provider and enables the creation of container groups.", "Registers the subscription for the container instance resource provider and enables the creation of container groups."),
		op("Microsoft.ContainerInstance/containerGroups/read", "Container Groups", "Get Container Groups", "Get all container groups."),
		op("Microsoft.ContainerInstance/containerGroups/write", "Container Groups", "Create or Update Container Groups", "Create or update a specific container group."),
		op("Microsoft.ContainerInstance/containerGroups/delete", "Container Groups", "Delete Container Groups", "Delete the specified container group."),
		op("Microsoft.ContainerInstance/containerGroups/restart/action", "Container Groups", "Restart Container Groups", "Restarts a specific container group."),
		op("Microsoft.ContainerInstance/containerGroups/stop/action", "Container Groups", "Stop Container Groups", "Stops a specific container group."),
		op("Microsoft.ContainerInstance/containerGroups/start/action", "Container Groups", "Start Container Groups", "Starts a specific container group."),
		op("Microsoft.ContainerInstance/containerGroups/containers/exec/action", "Container", "Exec Container", "Exec a specific container."),
		op("Microsoft.ContainerInstance/containerGroups/containers/attach/action", "Container", "Attach Container", "Attach to the output stream of a container."),
		op("Microsoft.ContainerInstance/containerGroups/containers/logs/read", "Container Logs", "Get Container Logs", "Get logs for a specific container."),
		op("Microsoft.ContainerInstance/locations/usages/read", "Location Usages", "Get Usages", "Get the usage of the specific resource type under a subscription."),
		op("Microsoft.ContainerInstance/locations/capabilities/read", "Location Capabilities", "Get Capabilities", "Get the capabilities for a region."),
		op("Microsoft.ContainerInstance/operations/read", "Operations", "List Operations", "List the operations for Azure Container Instance service."),
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
}

// handleACILocationUsages reports the subscription's Azure Container Instances
// usage for a region (Location_ListUsage). Current consumption is computed from
// the container groups the simulator actually holds in that region; the limits
// are the published per-region default quotas.
func handleACILocationUsages(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	location := sim.PathParam(r, "location")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/", sub)
	groupCount := int32(0)
	var cores float64
	for _, group := range aciContainerGroups.Filter(func(group ACIContainerGroup) bool {
		return strings.HasPrefix(group.ID, prefix) && aciLocationMatches(group.Location, location)
	}) {
		groupCount++
		cores += aciGroupCPU(&group)
	}
	usage := func(name string, current, limit int32) map[string]any {
		return map[string]any{
			"id":           fmt.Sprintf("/subscriptions/%s/providers/Microsoft.ContainerInstance/locations/%s/usages/%s", sub, location, name),
			"unit":         "Count",
			"currentValue": current,
			"limit":        limit,
			"name": map[string]any{
				"value":          name,
				"localizedValue": name,
			},
		}
	}
	value := []map[string]any{
		usage("ContainerGroups", groupCount, 100),
		usage("StandardCores", int32(cores), 100),
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
}

// handleACILocationCapabilities returns the published Azure Container Instances
// resource capabilities for a region (Location_ListCapabilities).
func handleACILocationCapabilities(w http.ResponseWriter, r *http.Request) {
	location := sim.PathParam(r, "location")
	capability := func(osType, ipType string) map[string]any {
		return map[string]any{
			"resourceType":  "containerGroups",
			"osType":        osType,
			"location":      location,
			"ipAddressType": ipType,
			"gpu":           "None",
			"capabilities": map[string]any{
				"maxMemoryInGB": 16,
				"maxCpu":        4,
				"maxGpuCount":   4,
			},
		}
	}
	value := []map[string]any{
		capability("Linux", "Public"),
		capability("Linux", "Private"),
		capability("Windows", "Public"),
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
}

// handleACILocationCachedImages returns the images cached by the platform in a
// region (Location_ListCachedImages). The simulator maintains no Azure-managed
// image cache, so the list is empty.
func handleACILocationCachedImages(w http.ResponseWriter, _ *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
}

// handleACIOutboundNetworkDeps returns the container group's outbound network
// dependency endpoints. Per the Azure contract this list is always empty.
func handleACIOutboundNetworkDeps(w http.ResponseWriter, r *http.Request) {
	id := aciContainerGroupID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "containerGroupName"))
	if _, ok := aciContainerGroups.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Container group %q not found.", sim.PathParam(r, "containerGroupName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, []string{})
}

// handleACIContainerAttach issues the websocket handshake for attaching to a
// running container's standard streams (Containers_Attach).
func handleACIContainerAttach(w http.ResponseWriter, r *http.Request) {
	id := aciContainerGroupID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "containerGroupName"))
	containerName := sim.PathParam(r, "containerName")
	if _, ok := aciContainerGroups.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Container group %q not found.", sim.PathParam(r, "containerGroupName"))
		return
	}
	rec, ok := aciRuntimeRecords.Get(aciRuntimeKey(id, containerName))
	if !ok || rec.ContainerID == "" || rec.State != ACIStateRunning {
		sim.AzureErrorf(w, "ContainerNotRunning", http.StatusBadRequest, "Container %q is not running.", containerName)
		return
	}
	sessionID := generateUUID()
	password := generateUUID()
	aciAttachSessions.Store(sessionID, aciAttachSession{ContainerID: rec.ContainerID, Password: password})
	scheme := "ws"
	if strings.EqualFold(azureRequestScheme(r), "https") {
		scheme = "wss"
	}
	query := url.Values{}
	query.Set("password", password)
	if apiVersion := r.URL.Query().Get("api-version"); apiVersion != "" {
		query.Set("api-version", apiVersion)
	}
	uri := fmt.Sprintf("%s://%s%s/containers/%s/attachSessions/%s?%s",
		scheme, r.Host, id, containerName, sessionID, query.Encode())
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"webSocketUri": uri,
		"password":     password,
	})
}

// handleACIContainerAttachSession upgrades the attach websocket and streams the
// running container's standard streams over it.
func handleACIContainerAttachSession(w http.ResponseWriter, r *http.Request) {
	sessionID := sim.PathParam(r, "sessionID")
	v, ok := aciAttachSessions.LoadAndDelete(sessionID)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Attach session %q not found.", sessionID)
		return
	}
	session, ok := v.(aciAttachSession)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Attach session %q not found.", sessionID)
		return
	}
	if session.Password != "" && r.URL.Query().Get("password") != session.Password && r.Header.Get("Authorization") != session.Password {
		sim.AzureError(w, "Unauthorized", "Invalid attach session password.", http.StatusUnauthorized)
		return
	}
	conn, err := acaWSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close() //nolint:errcheck

	cli := sim.DockerClient()
	if cli == nil {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "docker client not initialised"))
		return
	}
	ctx := r.Context()
	attach, err := cli.ContainerAttach(ctx, session.ContainerID, dockercontainer.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()))
		return
	}
	defer attach.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, rerr := attach.Reader.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if _, werr := attach.Conn.Write(msg); werr != nil {
			break
		}
	}
	_ = attach.CloseWrite()
	<-done
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(5*time.Second),
	)
}

// handleACISubnetServiceAssociationLinkDelete removes the service association
// link that binds a delegated subnet to Azure Container Instances. The
// simulator holds no such link, so the delete is a no-op success.
func handleACISubnetServiceAssociationLinkDelete(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// aciLocationMatches compares two Azure region names, ignoring case and the
// spaces Azure permits in display-form locations ("East US" == "eastus").
func aciLocationMatches(a, b string) bool {
	norm := func(s string) string { return strings.ReplaceAll(strings.ToLower(s), " ", "") }
	return norm(a) == norm(b)
}

// aciGroupCPU sums the CPU cores requested across a container group's containers.
func aciGroupCPU(group *ACIContainerGroup) float64 {
	var total float64
	for _, c := range aciContainers(group) {
		props, _ := c["properties"].(map[string]any)
		if props == nil {
			continue
		}
		resources, _ := props["resources"].(map[string]any)
		if resources == nil {
			continue
		}
		requests, _ := resources["requests"].(map[string]any)
		if requests == nil {
			continue
		}
		switch cpu := requests["cpu"].(type) {
		case float64:
			total += cpu
		case int:
			total += float64(cpu)
		}
	}
	return total
}

func aciApplyGroupDefaults(r *http.Request, group *ACIContainerGroup) {
	props := group.Properties
	props["provisioningState"] = "Creating"
	if _, ok := props["restartPolicy"]; !ok {
		props["restartPolicy"] = "Always"
	}
	if _, ok := props["osType"]; !ok {
		props["osType"] = "Linux"
	}
	if _, ok := props["instanceView"]; !ok {
		props["instanceView"] = map[string]any{"state": ACIStatePending}
	}
	if ip, ok := props["ipAddress"].(map[string]any); ok {
		if ip["fqdn"] == nil {
			ip["fqdn"] = azureEndpointHostname(r, group.Name, "aci")
		}
		if _, ok := ip["ports"]; !ok {
			ip["ports"] = []any{}
		}
		props["ipAddress"] = ip
	}
	containers := aciContainers(group)
	for i, c := range containers {
		containers[i] = aciNormalizeContainer(c)
	}
	if containers == nil {
		containers = []map[string]any{}
	}
	props["containers"] = containers
}

func aciSetGroupProvisioning(group *ACIContainerGroup, state string) {
	if group.Properties == nil {
		group.Properties = map[string]any{}
	}
	group.Properties["provisioningState"] = state
}

func aciSetGroupState(group *ACIContainerGroup, state ACIContainerState) {
	if group.Properties == nil {
		group.Properties = map[string]any{}
	}
	group.Properties["instanceView"] = map[string]any{"state": state}
	containers := aciContainers(group)
	for i, c := range containers {
		c = aciNormalizeContainer(c)
		props, ok := c["properties"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := c["name"].(string)
		currentState := map[string]any{
			"state":        state,
			"startTime":    time.Now().UTC().Format(time.RFC3339Nano),
			"detailStatus": state,
		}
		props["instanceView"] = map[string]any{
			"currentState": currentState,
		}
		if name != "" {
			if rec, ok := aciRuntimeRecords.Get(aciRuntimeKey(group.ID, name)); ok && rec.ExitCode != 0 {
				currentState["exitCode"] = rec.ExitCode
			}
		}
		c["properties"] = props
		containers[i] = c
	}
	group.Properties["containers"] = containers
}

func aciStartGroupContainers(group ACIContainerGroup) error {
	if DockerRuntimeDisabled() {
		return fmt.Errorf("container execution requires a Docker or Podman runtime; SIM_RUNTIME=process supports API-only operations")
	}
	containers := aciContainers(&group)
	for _, c := range containers {
		name, _ := c["name"].(string)
		props, _ := c["properties"].(map[string]any)
		image, _ := props["image"].(string)
		if name == "" || image == "" {
			return fmt.Errorf("container name and image are required")
		}
		key := aciRuntimeKey(group.ID, name)
		if rec, ok := aciRuntimeRecords.Get(key); ok && rec.ContainerID != "" && rec.State == ACIStateRunning {
			continue
		}
		command := stringSlice(props["command"])
		env := map[string]string{}
		if vals, ok := props["environmentVariables"].([]any); ok {
			for _, item := range vals {
				m, _ := item.(map[string]any)
				n, _ := m["name"].(string)
				v, _ := m["value"].(string)
				if v == "" {
					if sv, ok := m["secureValue"].(string); ok {
						v = sv
					}
				}
				if n != "" {
					env[n] = v
				}
			}
		}
		logKey := key
		sink := sim.FuncSink(func(line sim.LogLine) {
			// The append is a read-modify-write on the stored slice; the mutex
			// keeps concurrent sink callbacks from dropping lines.
			aciLogMu.Lock()
			lines, _ := aciLogs.Get(logKey)
			aciLogs.Put(logKey, append(lines, line.Text))
			aciLogMu.Unlock()
		})
		cfg := sim.ContainerConfig{
			Image:        sim.ResolveLocalImage(image),
			Architecture: "linux/" + runtime.GOARCH,
			Env:          env,
			Labels: map[string]string{
				"sockerless-sim": "true",
				"aci-group":      group.ID,
				"aci-container":  name,
			},
			Name:    "sockerless-aci-" + sanitizeContainerName(group.Name) + "-" + sanitizeContainerName(name),
			Sandbox: sim.SandboxACA,
		}
		if len(command) > 0 {
			cfg.Command = command[:1]
			if len(command) > 1 {
				cfg.Args = command[1:]
			}
		}
		handle, err := sim.StartContainerSync(cfg, sink)
		if err != nil {
			return err
		}
		start := time.Now().UTC().Format(time.RFC3339Nano)
		aciRuntimeRecords.Put(key, aciRuntimeRecord{ContainerID: handle.ContainerID, State: ACIStateRunning, StartTime: start})
		go func(groupID, containerName, key string, h *sim.ContainerHandle) {
			res := h.Wait()
			aciRuntimeRecords.Update(key, func(rec *aciRuntimeRecord) {
				rec.State = ACIStateTerminated
				rec.ExitCode = res.ExitCode
				rec.FinishTime = time.Now().UTC().Format(time.RFC3339Nano)
			})
			aciContainerGroups.Update(groupID, func(stored *ACIContainerGroup) {
				aciSetGroupState(stored, ACIStateTerminated)
			})
		}(group.ID, name, key, handle)
	}
	aciContainerGroups.Update(group.ID, func(stored *ACIContainerGroup) {
		aciSetGroupState(stored, ACIStateRunning)
		aciSetGroupProvisioning(stored, "Succeeded")
	})
	return nil
}

func aciStopGroupContainers(group ACIContainerGroup) {
	for _, c := range aciContainers(&group) {
		name, _ := c["name"].(string)
		key := aciRuntimeKey(group.ID, name)
		if rec, ok := aciRuntimeRecords.Get(key); ok && rec.ContainerID != "" {
			sim.StopAndRemoveContainer(rec.ContainerID)
			rec.State = ACIStateStopped
			rec.FinishTime = time.Now().UTC().Format(time.RFC3339Nano)
			aciRuntimeRecords.Put(key, rec)
		}
	}
	aciContainerGroups.Update(group.ID, func(stored *ACIContainerGroup) {
		aciSetGroupState(stored, ACIStateStopped)
		aciSetGroupProvisioning(stored, "Succeeded")
	})
}

func aciContainers(group *ACIContainerGroup) []map[string]any {
	props := group.Properties
	if props == nil {
		return nil
	}
	raw, ok := props["containers"].([]any)
	if !ok {
		if typed, ok := props["containers"].([]map[string]any); ok {
			return typed
		}
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// aciStripSecureValues removes write-only env secureValue from every
// container in the group. Real Azure accepts secureValue on PUT but never
// returns it on GET.
func aciStripSecureValues(group *ACIContainerGroup) {
	for _, c := range aciContainers(group) {
		props, _ := c["properties"].(map[string]any)
		if props == nil {
			continue
		}
		vals, ok := props["environmentVariables"].([]any)
		if !ok {
			continue
		}
		for _, item := range vals {
			if m, ok := item.(map[string]any); ok {
				delete(m, "secureValue")
			}
		}
	}
}

func aciNormalizeContainer(c map[string]any) map[string]any {
	props, _ := c["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	if _, ok := props["ports"]; !ok {
		props["ports"] = []any{}
	}
	if _, ok := props["environmentVariables"]; !ok {
		props["environmentVariables"] = []any{}
	}
	c["properties"] = props
	return c
}

func stringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sanitizeContainerName(s string) string {
	var b bytes.Buffer
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return generateUUID()
	}
	return b.String()
}

func DockerRuntimeDisabled() bool {
	return strings.EqualFold(strings.TrimSpace(getenvDefault("SIM_RUNTIME", "docker")), "process")
}

func getenvDefault(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
