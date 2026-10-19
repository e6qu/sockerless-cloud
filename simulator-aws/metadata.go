package main

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/sidecar"
	"github.com/e6qu/sockerless-cloud/sim/sidecar/egress"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// Workloads read what the platform tells them about themselves from
// endpoints their network's sidecar forwards to the simulator: an EC2
// instance its instance metadata service (ec2_imds.go), an Amazon ECS task
// its task metadata endpoint, and a workload running under a role the
// container credentials endpoint (role_credentials.go).

// imdsInstancesByIP finds the instance a metadata request comes from.
var imdsInstancesByIP workloadhost.MetadataIndex[EC2Instance]

func registerHostMetadata(srv *sim.Server) {
	// A NAT gateway's egress sidecar reports its connections here, under the
	// token the simulator minted for it.
	srv.HandleFunc("POST "+egress.ReportPath, sidecar.EgressReports(ec2TakeNatEgressReport))
	registerEC2IMDS(srv)
	registerRoleCredentials(srv)

	// ECS task metadata v4. Real ECS sets ECS_CONTAINER_METADATA_URI_V4
	// to a per-task local URL like http://169.254.170.2/v4/<id>; tasks reach
	// the same handler through the host callback address.
	srv.HandleFunc("GET /v4/{id}/task", func(w http.ResponseWriter, r *http.Request) {
		id := sim.PathParam(r, "id")
		if !ecsTaskOwnsMetadataRequest(id, r) {
			http.Error(w, "ECS task metadata not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		taskMetadata, ok := ecsTaskMetadataV4(id)
		if !ok {
			http.Error(w, "ECS task metadata not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(taskMetadata)
	})
	srv.HandleFunc("GET /v4/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := sim.PathParam(r, "id")
		if !ecsTaskOwnsMetadataRequest(id, r) {
			http.Error(w, "ECS task metadata not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		containerMetadata, ok := ecsContainerMetadataV4(id)
		if !ok {
			http.Error(w, "ECS container metadata not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(containerMetadata)
	})
}

// ecsTaskOwnsMetadataRequest reports whether a request that arrived through a
// network sidecar comes from the awsvpc task whose metadata it asks for:
// the task's own VPC network and its elastic network interface address. The
// task metadata endpoint is task-local on Amazon ECS, so another task's
// containers find nothing there. A request from the simulator's own listener,
// or for a task without an interface of its own, is answered as before.
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
	return definition.TaskRoleArn
}

// simListenAddr is the listen address main() serves on.
var simListenAddr string

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

// ecsTaskMetadataEnv is what the Amazon ECS agent gives a task's containers:
// the task metadata endpoint, and the container credentials endpoint serving
// the task role's sessions when the task has a role.
func ecsTaskMetadataEnv(origin, taskID string) map[string]string {
	env := grantContainerCredentials(origin, roleHolder("ecs", taskID), ecsTaskRoleArn(taskID), taskID)
	env["ECS_CONTAINER_METADATA_URI_V4"] = origin + "/v4/" + taskID
	env["ECS_CONTAINER_METADATA_URI"] = origin + "/v4/" + taskID
	return env
}

// hostNetworkMetadataEnv is ecsTaskMetadataEnv for a task that shares
// the container instance's network stack: it has no sidecar, so it reaches
// the simulator's listener directly.
func hostNetworkMetadataEnv(taskID string) (map[string]string, error) {
	addr, err := workloadhost.CallbackAddr(simListenAddr)
	if err != nil {
		return nil, err
	}
	return ecsTaskMetadataEnv("http://"+addr, taskID), nil
}
