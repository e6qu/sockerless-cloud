package main

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/sidecar"
	"github.com/e6qu/sockerless-cloud/sim/sidecar/egress"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// Workloads read what the platform tells them about themselves from
// endpoints their network's sidecar forwards to the simulator: an EC2
// instance its instance metadata service (ec2_imds.go), an Amazon ECS task
// its task metadata endpoint, and a workload running under a role the
// container credentials endpoint (role_credentials.go).

var imdsInstancesByIP workloadhost.MetadataIndex[EC2Instance]

func registerHostMetadata(srv *sim.Server) {
	// A NAT gateway's egress sidecar reports its connections here, under the
	// token the simulator minted for it.
	srv.HandleFunc("POST "+egress.ReportPath, sidecar.EgressReports(ec2TakeNatEgressReport))
	registerEC2IMDS(srv)

	// ECS task metadata v4. Amazon ECS gives each container its own
	// ECS_CONTAINER_METADATA_URI_V4, http://169.254.170.2/v4/<endpoint ID>; the
	// simulator's endpoint ID is the task ID and the container's index, and the
	// containers reach it through their network's sidecar or the host callback
	// address.
	metadataEndpoint := func(path string, serve func(w http.ResponseWriter, r *http.Request, taskID, container string)) {
		srv.HandleFunc("GET /v4/{id}"+path, func(w http.ResponseWriter, r *http.Request) {
			taskID, container, ok := ecsMetadataEndpointContainer(sim.PathParam(r, "id"))
			if !ok || !ecsTaskOwnsMetadataRequest(taskID, r) {
				http.Error(w, "ECS task metadata not found", http.StatusNotFound)
				return
			}
			ecsAwaitContainerPlaced(r.Context(), taskID, container)
			serve(w, r, taskID, container)
		})
	}
	metadataEndpoint("", func(w http.ResponseWriter, _ *http.Request, taskID, container string) {
		containerMetadata, ok := ecsContainerMetadataV4(taskID, container)
		if !ok {
			http.Error(w, "ECS container metadata not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(containerMetadata)
	})
	metadataEndpoint("/task", func(w http.ResponseWriter, _ *http.Request, taskID, _ string) {
		taskMetadata, ok := ecsTaskMetadataV4(taskID)
		if !ok {
			http.Error(w, "ECS task metadata not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(taskMetadata)
	})
	// The stats paths answer the container engine's stats for the container, and
	// for the task a map from each container's Docker ID to its stats.
	metadataEndpoint("/stats", func(w http.ResponseWriter, r *http.Request, taskID, container string) {
		dockerID, _ := ecsTaskDockerInfo(taskID, container)
		stats, err := ecsRawContainerStats(r.Context(), dockerID)
		if err != nil {
			http.Error(w, "ECS container stats not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(stats)
	})
	metadataEndpoint("/task/stats", func(w http.ResponseWriter, r *http.Request, taskID, _ string) {
		task, ok := ecsTasks.Get(taskID)
		td, tdOK := ecsTaskDefinitionByArn(task.TaskDefinitionArn)
		if !ok || !tdOK {
			http.Error(w, "ECS task stats not found", http.StatusNotFound)
			return
		}
		// A container that has not started has no stats yet.
		started := map[string]bool{}
		for _, c := range task.Containers {
			started[c.Name] = c.LastStatus != "" && c.LastStatus != "PENDING"
		}
		all := map[string]json.RawMessage{}
		for _, cd := range td.ContainerDefinitions {
			dockerID, _ := ecsTaskDockerInfo(taskID, cd.Name)
			if dockerID == "" || !started[cd.Name] {
				continue
			}
			stats, err := ecsRawContainerStats(r.Context(), dockerID)
			if err != nil {
				http.Error(w, "ECS task stats not found", http.StatusNotFound)
				return
			}
			all[dockerID] = stats
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(all)
	})
}

// ecsMetadataEndpointContainer resolves a container's metadata endpoint ID,
// <task ID>-<container index>, to its task and container name.
func ecsMetadataEndpointContainer(id string) (taskID, container string, ok bool) {
	i := strings.LastIndex(id, "-")
	if i < 0 {
		return "", "", false
	}
	index, err := strconv.Atoi(id[i+1:])
	if err != nil {
		return "", "", false
	}
	taskID = id[:i]
	task, ok := ecsTasks.Get(taskID)
	if !ok {
		return "", "", false
	}
	td, ok := ecsTaskDefinitionByArn(task.TaskDefinitionArn)
	if !ok || index < 0 || index >= len(td.ContainerDefinitions) {
		return "", "", false
	}
	return taskID, td.ContainerDefinitions[index].Name, true
}

// ecsContainerMetadataEnv points a task's container at its own metadata
// endpoint under the task's metadata origin.
func ecsContainerMetadataEnv(taskEnv map[string]string, index int) map[string]string {
	env := map[string]string{}
	for _, name := range []string{"ECS_CONTAINER_METADATA_URI_V4", "ECS_CONTAINER_METADATA_URI"} {
		if uri, ok := taskEnv[name]; ok {
			env[name] = uri + "-" + strconv.Itoa(index)
		}
	}
	return env
}

// ecsTaskOwnsMetadataRequest reports whether a request that arrived through a
// network sidecar comes from the awsvpc task whose metadata it asks for: the
// task's own VPC network and ENI address. The task metadata endpoint is
// task-local on Amazon ECS, so another task's containers find nothing there. A
// request through the simulator's own listener, or for a task without an
// interface of its own, passes.
func ecsTaskOwnsMetadataRequest(taskID string, r *http.Request) bool {
	network := sim.SidecarNetwork(r.Context())
	if network == "" {
		return true
	}
	eniIP, subnetID, ok := ecsTaskENIInfo(taskID)
	if !ok {
		return true
	}
	subnet, ok := ec2Subnets.Get(subnetID)
	if !ok {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return network == ecsVPCNetworkName(subnet.VpcId) && host == eniIP
}

func ecsTaskRoleArn(taskID string) string {
	task, ok := ecsTasks.Get(taskID)
	if !ok {
		return ""
	}
	definition, ok := ecsTaskDefinitionForARN(task.TaskDefinitionArn)
	if !ok {
		return ""
	}
	taskRole, _ := ecsTaskRoles(definition, task.Overrides)
	return taskRole
}

// simListenAddr is the API listen address main() serves on, and
// simCallbackListenAddr the plain-HTTP one the workloads call back on; the
// two differ when the API listener terminates TLS.
var simListenAddr, simCallbackListenAddr string

func rewriteHostDockerInternalEnv(env map[string]string) map[string]string {
	containerized := workloadhost.InContainer()
	if !containerized {
		return env
	}
	gateway := workloadhost.OuterHostGatewayIPv4()
	return rewriteHostDockerInternalEnvForRuntime(env, containerized, gateway)
}

func rewriteHostDockerInternalEnvForRuntime(env map[string]string, containerized bool, gateway string) map[string]string {
	if !containerized || gateway == "" {
		return env
	}
	return rewriteHostDockerInternalEnvWithGateway(env, gateway)
}

func rewriteHostDockerInternalEnvWithGateway(env map[string]string, gateway string) map[string]string {
	out := make(map[string]string, len(env))
	for key, value := range env {
		out[key] = strings.ReplaceAll(value, "host.docker.internal", gateway)
	}
	return out
}

// ec2MetadataEnv routes an EC2 instance's instance metadata reads to origin,
// the sidecar of its network, which forwards them to the simulator with the
// instance's address.
func ec2MetadataEnv(origin string) map[string]string {
	return map[string]string{
		"AWS_EC2_METADATA_SERVICE_ENDPOINT":      origin + "/",
		"AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE": "IPv4",
	}
}

// ecsTaskMetadataEnv is what the Amazon ECS agent gives the containers of a
// task backend places on network: the task metadata endpoint, and the
// container credentials endpoint serving the task role's sessions when the
// task has a role.
func ecsTaskMetadataEnv(backend placement.Backend, network awsWorkloadNetwork, taskID string) containerCredentials {
	credentials := grantContainerCredentials(backend, network, roleHolder("ecs", taskID), ecsTaskRoleArn(taskID), taskID)
	credentials.Env["ECS_CONTAINER_METADATA_URI_V4"] = network.MetadataOrigin() + "/v4/" + taskID
	credentials.Env["ECS_CONTAINER_METADATA_URI"] = network.MetadataOrigin() + "/v4/" + taskID
	return credentials
}

// hostNetworkMetadataEnv is ecsTaskMetadataEnv for a task that shares
// the container instance's network stack: it has no sidecar, so it reaches
// the simulator's listener directly.
func hostNetworkMetadataEnv(taskID string) (map[string]string, error) {
	addr, err := workloadhost.CallbackAddr(simCallbackListenAddr)
	if err != nil {
		return nil, err
	}
	origin := "http://" + addr
	env := grantContainerCredentialsAt(origin, roleHolder("ecs", taskID), ecsTaskRoleArn(taskID), taskID)
	env["ECS_CONTAINER_METADATA_URI_V4"] = origin + "/v4/" + taskID
	env["ECS_CONTAINER_METADATA_URI"] = origin + "/v4/" + taskID
	return env, nil
}
