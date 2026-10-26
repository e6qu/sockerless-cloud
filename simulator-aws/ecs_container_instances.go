package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The agent registers instances and reports state through the submit
// operations; the control plane describes, updates and deregisters them.

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
	// The store-key prefix; never on the wire.
	ClusterName string `json:"-"`
}

type ECSAttribute struct {
	Name       string `json:"name"`
	Value      string `json:"value,omitempty"`
	TargetType string `json:"targetType,omitempty"`
	TargetId   string `json:"targetId,omitempty"`
	// Scopes the attribute for ListAttributes; the Attribute shape has no cluster.
	Cluster string `json:"-"`
}

var ecsContainerInstances sim.Store[ECSContainerInstance]

func registerECSContainerInstances(r *AWSRouter, srv *sim.Server) {
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

// Accepts an id or …:container-instance/<cluster>/<id>.
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
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	if _, ok := ecsClusters.Get(clusterName); !ok {
		AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest, "Cluster not found: %s", req.Cluster)
		return
	}
	// The identity document's instanceId is the ec2InstanceId; callers join a
	// container instance to its EC2 instance on it.
	ec2InstanceID, identityRegion := ecsInstanceIdentity(req.InstanceIdentityDoc)
	_ = identityRegion
	id := sim.NewUUID()
	ci := ECSContainerInstance{
		ContainerInstanceArn: ecsArn("container-instance", clusterName+"/"+id),
		Ec2InstanceId:        ec2InstanceID,
		Version:              1,
		VersionInfo:          req.VersionInfo,
		RemainingResources:   req.TotalResources,
		RegisteredResources:  req.TotalResources,
		Status:               "ACTIVE",
		AgentConnected:       true,
		Attributes:           req.Attributes,
		RegisteredAt:         ecsEpochSeconds(),
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
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	var found []ECSContainerInstance
	failures := []map[string]string{}
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
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
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
	page, next, pageOK := awsKeyPage(w, ecsBadToken, arns, func(it string) string { return it }, req.NextToken, req.MaxResults, 100)
	if !pageOK {
		return
	}
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
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	if req.Status != "ACTIVE" && req.Status != "DRAINING" {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"The container instance state '%s' is invalid. Valid states are ACTIVE and DRAINING.", req.Status)
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	var found []ECSContainerInstance
	failures := []map[string]string{}
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
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
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
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
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
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
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
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"Could not find container instance '%s'.", req.ContainerInstance)
		return
	}
	// Without force, Amazon ECS refuses to deregister an instance still running
	// tasks.
	if !req.Force {
		if running := ecsRunningTasksOnContainerInstance(ci.ContainerInstanceArn); running > 0 {
			AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
				"You cannot deregister a container instance while it has running tasks. "+
					"Stop the %d task(s) or use the force flag.", running)
			return
		}
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

// The submit operations answer with an "acknowledgment" of the reported
// status.

// Apply the container's status, exit code and network bindings to the task
// record DescribeTasks returns.
func handleECSSubmitContainerStateChange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster         string `json:"cluster"`
		Task            string `json:"task"`
		ContainerName   string `json:"containerName"`
		RuntimeId       string `json:"runtimeId"`
		Status          string `json:"status"`
		ExitCode        *int   `json:"exitCode"`
		Reason          string `json:"reason"`
		NetworkBindings []struct {
			BindIP        string `json:"bindIP"`
			ContainerPort int    `json:"containerPort"`
			HostPort      int    `json:"hostPort"`
			Protocol      string `json:"protocol"`
		} `json:"networkBindings"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	taskID, task, ok := ecsTaskFromAgentReference(req.Task, req.Cluster)
	if !ok {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"Could not find task: %s", req.Task)
		return
	}
	found := false
	for i := range task.Containers {
		if task.Containers[i].Name != req.ContainerName {
			continue
		}
		found = true
		if req.Status != "" {
			task.Containers[i].LastStatus = req.Status
		}
		if req.ExitCode != nil {
			task.Containers[i].ExitCode = req.ExitCode
		}
		if req.Reason != "" {
			task.Containers[i].Reason = req.Reason
		}
		if req.RuntimeId != "" {
			task.Containers[i].RuntimeId = req.RuntimeId
		}
		for _, binding := range req.NetworkBindings {
			task.Containers[i].NetworkBindings = append(task.Containers[i].NetworkBindings,
				ECSNetworkBinding{
					BindIP:        binding.BindIP,
					ContainerPort: binding.ContainerPort,
					HostPort:      binding.HostPort,
					Protocol:      binding.Protocol,
				})
		}
	}
	if !found {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"Could not find container %q in task %s", req.ContainerName, req.Task)
		return
	}
	ecsTasks.Put(taskID, task)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"acknowledgment": "ACK"})
}

// Move lastStatus to what the agent reached, stamp the time and keep a
// stopping task's reason; DescribeTasks and the scheduler read the record.
func handleECSSubmitTaskStateChange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster            string   `json:"cluster"`
		Task               string   `json:"task"`
		Status             string   `json:"status"`
		Reason             string   `json:"reason"`
		ExecutionStoppedAt *float64 `json:"executionStoppedAt"`
		PullStartedAt      *float64 `json:"pullStartedAt"`
		PullStoppedAt      *float64 `json:"pullStoppedAt"`
		Containers         []struct {
			ContainerName string `json:"containerName"`
			Status        string `json:"status"`
			ExitCode      *int   `json:"exitCode"`
		} `json:"containers"`
		Attachments []struct {
			AttachmentArn string `json:"attachmentArn"`
			Status        string `json:"status"`
		} `json:"attachments"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	taskID, task, ok := ecsTaskFromAgentReference(req.Task, req.Cluster)
	if !ok {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"Could not find task: %s", req.Task)
		return
	}
	now := ecsEpochSeconds()
	if req.Status != "" {
		task.LastStatus = ECSTaskStatus(req.Status)
		switch ECSTaskStatus(req.Status) {
		case ECSTaskStatusRunning:
			if task.StartedAt == nil {
				started := now
				task.StartedAt = &started
			}
			task.Connectivity = "CONNECTED"
		case ECSTaskStatusStopped:
			if task.StoppedAt == nil {
				stopped := now
				task.StoppedAt = &stopped
			}
			// An empty report does not overwrite a reason the scheduler recorded.
			if req.Reason != "" {
				task.StoppedReason = req.Reason
			}
		}
	}
	if req.PullStartedAt != nil {
		task.PullStartedAt = req.PullStartedAt
	}
	if req.PullStoppedAt != nil {
		task.PullStoppedAt = req.PullStoppedAt
	}
	if req.ExecutionStoppedAt != nil {
		task.ExecutionStoppedAt = req.ExecutionStoppedAt
	}
	// A task report carries its containers' states.
	for _, reported := range req.Containers {
		for i := range task.Containers {
			if task.Containers[i].Name != reported.ContainerName {
				continue
			}
			if reported.Status != "" {
				task.Containers[i].LastStatus = reported.Status
			}
			if reported.ExitCode != nil {
				task.Containers[i].ExitCode = reported.ExitCode
			}
		}
	}
	for _, reported := range req.Attachments {
		ecsApplyAttachmentStatus(&task, reported.AttachmentArn, reported.Status)
	}
	ecsTasks.Put(taskID, task)
	// The scheduler must react to a task the agent stopped.
	ecsRequestServiceReconcileForTask(task)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"acknowledgment": "ACK"})
}

// The agent sends an ARN or a bare id. A report scoped to one cluster must not
// reach a task in another.
func ecsTaskFromAgentReference(ref, cluster string) (string, ECSTask, bool) {
	id := ref
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		id = ref[i+1:]
	}
	task, ok := ecsTasks.Get(id)
	if !ok {
		return id, task, false
	}
	if cluster != "" && !ecsTaskInCluster(task, cluster) {
		return id, ECSTask{}, false
	}
	return id, task, true
}

// Matches the attachment id or an ARN ending with it.
func ecsApplyAttachmentStatus(task *ECSTask, attachmentRef, status string) bool {
	if attachmentRef == "" || status == "" {
		return false
	}
	id := attachmentRef
	if i := strings.LastIndex(attachmentRef, "/"); i >= 0 {
		id = attachmentRef[i+1:]
	}
	for i := range task.Attachments {
		if task.Attachments[i].Id != id {
			continue
		}
		task.Attachments[i].Status = status
		return true
	}
	return false
}

// An awsvpc task's attachment moves to ATTACHED once its interface is
// provisioned, which DescribeTasks reports.
func handleECSSubmitAttachmentStateChanges(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster     string `json:"cluster"`
		Attachments []struct {
			AttachmentArn string `json:"attachmentArn"`
			Status        string `json:"status"`
		} `json:"attachments"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Attachments) == 0 {
		AWSError(w, "InvalidParameterException", "attachments is required", http.StatusBadRequest)
		return
	}
	// An attachment names no task; scan the cluster's tasks for its owner.
	applied := 0
	for _, reported := range req.Attachments {
		for _, task := range ecsTasks.List() {
			if req.Cluster != "" && !ecsTaskInCluster(task, req.Cluster) {
				continue
			}
			owned := task
			if !ecsApplyAttachmentStatus(&owned, reported.AttachmentArn, reported.Status) {
				continue
			}
			ecsTasks.Put(owned.TaskID(), owned)
			applied++
			break
		}
	}
	if applied == 0 {
		AWSError(w, "InvalidParameterException",
			"no task holds the attachments reported", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"acknowledgment": "ACK"})
}

func ecsTaskInCluster(task ECSTask, cluster string) bool {
	return ecsClusterNameFromRef(task.ClusterArn) == ecsClusterNameFromRef(cluster)
}

// The agent polls these endpoints, so point them at this simulator.
func handleECSDiscoverPollEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ContainerInstance string `json:"containerInstance"`
		Cluster           string `json:"cluster"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	base := "http://" + r.Host + "/"
	if r.TLS != nil {
		base = "https://" + r.Host + "/"
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"endpoint":               base,
		"telemetryEndpoint":      base,
		"serviceConnectEndpoint": base,
	})
}

// Decides whether deregistering needs force.
func ecsRunningTasksOnContainerInstance(containerInstanceArn string) int {
	running := 0
	for _, task := range ecsTasks.List() {
		if task.ContainerInstanceArn != containerInstanceArn {
			continue
		}
		if task.LastStatus == ECSTaskStatusStopped {
			continue
		}
		running++
	}
	return running
}

// The document /latest/dynamic/instance-identity/document serves; none leaves
// both values empty.
func ecsInstanceIdentity(document string) (instanceID, region string) {
	if strings.TrimSpace(document) == "" {
		return "", ""
	}
	var identity struct {
		InstanceID string `json:"instanceId"`
		Region     string `json:"region"`
	}
	if json.Unmarshal([]byte(document), &identity) != nil {
		return "", ""
	}
	return identity.InstanceID, identity.Region
}
