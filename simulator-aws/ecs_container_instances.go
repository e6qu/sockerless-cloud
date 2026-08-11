package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// ECS container instances — the EC2 hosts an ECS agent registers into a
// cluster for EC2-launch-type placement. The agent calls RegisterContainerInstance
// (and the agent-poll trio SubmitContainerStateChange / SubmitTaskStateChange /
// SubmitAttachmentStateChanges / DiscoverPollEndpoint); the control plane offers
// Describe/List/UpdateContainerInstancesState/UpdateContainerAgent /
// DeregisterContainerInstance. The sim stores each instance keyed by its id
// within a cluster so the full lifecycle round-trips.

// ECSContainerInstance is the stored shape of a container instance.
type ECSContainerInstance struct {
	ContainerInstanceArn string          `json:"containerInstanceArn"`
	Ec2InstanceId        string          `json:"ec2InstanceId,omitempty"`
	CapacityProviderName string          `json:"capacityProviderName,omitempty"`
	Version              int64           `json:"version"`
	VersionInfo          json.RawMessage `json:"versionInfo,omitempty"`
	RemainingResources   json.RawMessage `json:"remainingResources,omitempty"`
	RegisteredResources  json.RawMessage `json:"registeredResources,omitempty"`
	Status               string          `json:"status"`
	StatusReason         string          `json:"statusReason,omitempty"`
	AgentConnected       bool            `json:"agentConnected"`
	RunningTasksCount    int             `json:"runningTasksCount"`
	PendingTasksCount    int             `json:"pendingTasksCount"`
	AgentUpdateStatus    string          `json:"agentUpdateStatus,omitempty"`
	Attributes           []ECSAttribute  `json:"attributes,omitempty"`
	RegisteredAt         float64         `json:"registeredAt"`
	Attachments          json.RawMessage `json:"attachments,omitempty"`
	Tags                 []ECSTag        `json:"tags,omitempty"`
	HealthStatus         json.RawMessage `json:"healthStatus,omitempty"`
	// ClusterName is the store-key prefix; never on the wire.
	ClusterName string `json:"-"`
}

// ECSAttribute is a name/value attribute attached to a container instance.
type ECSAttribute struct {
	Name       string `json:"name"`
	Value      string `json:"value,omitempty"`
	TargetType string `json:"targetType,omitempty"`
	TargetId   string `json:"targetId,omitempty"`
	// Cluster scopes a stored attribute to its cluster for ListAttributes; it
	// is never on the wire (the Attribute shape has no cluster field).
	Cluster string `json:"-"`
}

var ecsContainerInstances sim.Store[ECSContainerInstance]

func registerECSContainerInstances(r *sim.AWSRouter, srv *sim.Server) {
	ecsContainerInstances = sim.MakeStore[ECSContainerInstance](srv.DB(), "ecs_container_instances")

	r.Register("AmazonEC2ContainerServiceV20141113.RegisterContainerInstance", handleECSRegisterContainerInstance)
	r.Register("AmazonEC2ContainerServiceV20141113.DeregisterContainerInstance", handleECSDeregisterContainerInstance)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeContainerInstances", handleECSDescribeContainerInstances)
	r.Register("AmazonEC2ContainerServiceV20141113.ListContainerInstances", handleECSListContainerInstances)
	r.Register("AmazonEC2ContainerServiceV20141113.UpdateContainerInstancesState", handleECSUpdateContainerInstancesState)
	r.Register("AmazonEC2ContainerServiceV20141113.UpdateContainerAgent", handleECSUpdateContainerAgent)
	r.Register("AmazonEC2ContainerServiceV20141113.SubmitContainerStateChange", handleECSSubmitContainerStateChange)
	r.Register("AmazonEC2ContainerServiceV20141113.SubmitTaskStateChange", handleECSSubmitTaskStateChange)
	r.Register("AmazonEC2ContainerServiceV20141113.SubmitAttachmentStateChanges", handleECSSubmitAttachmentStateChanges)
	r.Register("AmazonEC2ContainerServiceV20141113.DiscoverPollEndpoint", handleECSDiscoverPollEndpoint)
}

func ecsContainerInstanceKey(cluster, id string) string { return cluster + "/" + id }

// ecsContainerInstanceID extracts the instance id from an id or ARN
// (...:container-instance/<cluster>/<id>).
func ecsContainerInstanceID(ref string) string {
	if !strings.HasPrefix(ref, "arn:") {
		return ref
	}
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1]
}

func handleECSRegisterContainerInstance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster             string          `json:"cluster"`
		TotalResources      json.RawMessage `json:"totalResources"`
		VersionInfo         json.RawMessage `json:"versionInfo"`
		Attributes          []ECSAttribute  `json:"attributes"`
		Tags                []ECSTag        `json:"tags"`
		InstanceIdentityDoc string          `json:"instanceIdentityDocument"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	if _, ok := ecsClusters.Get(clusterName); !ok {
		sim.AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest, "Cluster not found: %s", req.Cluster)
		return
	}
	id := generateUUID()
	ci := ECSContainerInstance{
		ContainerInstanceArn: ecsArn("container-instance", clusterName+"/"+id),
		Version:              1,
		VersionInfo:          req.VersionInfo,
		RemainingResources:   req.TotalResources,
		RegisteredResources:  req.TotalResources,
		Status:               "ACTIVE",
		AgentConnected:       true,
		Attributes:           req.Attributes,
		RegisteredAt:         float64(time.Now().Unix()),
		Tags:                 req.Tags,
		ClusterName:          clusterName,
	}
	ecsContainerInstances.Put(ecsContainerInstanceKey(clusterName, id), ci)
	ecsClusters.Update(clusterName, func(c *ECSCluster) { c.RegisteredContainerInstancesCount++ })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"containerInstance": ci})
}

func handleECSDescribeContainerInstances(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster            string   `json:"cluster"`
		ContainerInstances []string `json:"containerInstances"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	var found []ECSContainerInstance
	var failures []map[string]string
	for _, ref := range req.ContainerInstances {
		id := ecsContainerInstanceID(ref)
		ci, ok := ecsContainerInstances.Get(ecsContainerInstanceKey(clusterName, id))
		if !ok {
			failures = append(failures, map[string]string{
				"arn":    ecsArn("container-instance", clusterName+"/"+id),
				"reason": "MISSING",
			})
			continue
		}
		found = append(found, ci)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"containerInstances": found,
		"failures":           failures,
	})
}

func handleECSListContainerInstances(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster    string `json:"cluster"`
		Status     string `json:"status"`
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	var arns []string
	for _, ci := range ecsContainerInstances.List() {
		if ci.ClusterName != clusterName {
			continue
		}
		if req.Status != "" && ci.Status != req.Status {
			continue
		}
		arns = append(arns, ci.ContainerInstanceArn)
	}
	sort.Strings(arns)
	page, next := awsPage(arns, req.NextToken, req.MaxResults, 100)
	out := map[string]any{"containerInstanceArns": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleECSUpdateContainerInstancesState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster            string   `json:"cluster"`
		ContainerInstances []string `json:"containerInstances"`
		Status             string   `json:"status"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	if req.Status != "ACTIVE" && req.Status != "DRAINING" {
		sim.AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"The container instance state '%s' is invalid. Valid states are ACTIVE and DRAINING.", req.Status)
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	var found []ECSContainerInstance
	var failures []map[string]string
	for _, ref := range req.ContainerInstances {
		id := ecsContainerInstanceID(ref)
		key := ecsContainerInstanceKey(clusterName, id)
		ci, ok := ecsContainerInstances.Get(key)
		if !ok {
			failures = append(failures, map[string]string{
				"arn":    ecsArn("container-instance", clusterName+"/"+id),
				"reason": "MISSING",
			})
			continue
		}
		ci.Status = req.Status
		ecsContainerInstances.Put(key, ci)
		found = append(found, ci)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"containerInstances": found,
		"failures":           failures,
	})
}

func handleECSUpdateContainerAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster           string `json:"cluster"`
		ContainerInstance string `json:"containerInstance"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	id := ecsContainerInstanceID(req.ContainerInstance)
	key := ecsContainerInstanceKey(clusterName, id)
	ci, ok := ecsContainerInstances.Get(key)
	if !ok {
		sim.AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"Could not find container instance '%s'.", req.ContainerInstance)
		return
	}
	ci.AgentUpdateStatus = "UPDATED"
	ecsContainerInstances.Put(key, ci)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"containerInstance": ci})
}

func handleECSDeregisterContainerInstance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster           string `json:"cluster"`
		ContainerInstance string `json:"containerInstance"`
		Force             bool   `json:"force"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	id := ecsContainerInstanceID(req.ContainerInstance)
	key := ecsContainerInstanceKey(clusterName, id)
	ci, ok := ecsContainerInstances.Get(key)
	if !ok {
		sim.AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"Could not find container instance '%s'.", req.ContainerInstance)
		return
	}
	ci.Status = "INACTIVE"
	ci.AgentConnected = false
	ecsContainerInstances.Delete(key)
	ecsClusters.Update(clusterName, func(c *ECSCluster) {
		if c.RegisteredContainerInstancesCount > 0 {
			c.RegisteredContainerInstancesCount--
		}
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{"containerInstance": ci})
}

// The agent-poll submit operations acknowledge a state change without exposing
// a control-plane resource. Real ECS returns an "acknowledgment" string (the
// reported status) so the agent knows the change was recorded.

func handleECSSubmitContainerStateChange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster       string `json:"cluster"`
		Task          string `json:"task"`
		ContainerName string `json:"containerName"`
		Status        string `json:"status"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"acknowledgment": "ACK"})
}

func handleECSSubmitTaskStateChange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string `json:"cluster"`
		Task    string `json:"task"`
		Status  string `json:"status"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"acknowledgment": "ACK"})
}

func handleECSSubmitAttachmentStateChanges(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster     string          `json:"cluster"`
		Attachments json.RawMessage `json:"attachments"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"acknowledgment": "ACK"})
}

// handleECSDiscoverPollEndpoint returns the agent-communication endpoints. The
// agent uses these to poll for tasks and report telemetry; the sim points them
// at its own base so the agent contract round-trips.
func handleECSDiscoverPollEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ContainerInstance string `json:"containerInstance"`
		Cluster           string `json:"cluster"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	endpoint := "https://ecs-a-1." + awsRegion() + ".amazonaws.com/"
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"endpoint":          endpoint,
		"telemetryEndpoint": "https://ecs-t-1." + awsRegion() + ".amazonaws.com/",
	})
}
