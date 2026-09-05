package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"

	"github.com/gorilla/websocket"
	"github.com/moby/moby/api/pkg/stdcopy"
	dockerclient "github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ECS types

type ECSCluster struct {
	ClusterArn                        string          `json:"clusterArn"`
	ClusterName                       string          `json:"clusterName"`
	Status                            string          `json:"status"`
	RunningTasksCount                 int             `json:"runningTasksCount"`
	PendingTasksCount                 int             `json:"pendingTasksCount"`
	ActiveServicesCount               int             `json:"activeServicesCount"`
	RegisteredContainerInstancesCount int             `json:"registeredContainerInstancesCount"`
	CapacityProviders                 []string        `json:"capacityProviders,omitempty"`
	DefaultCapacityProviderStrategy   json.RawMessage `json:"defaultCapacityProviderStrategy,omitempty"`
	Tags                              []ECSTag        `json:"tags,omitempty"`
	// Settings (containerInsights) and Configuration (executeCommandConfiguration)
	// are stored raw so they round-trip exactly; DescribeClusters only surfaces
	// them when SETTINGS / CONFIGURATIONS is in the `include` list.
	Settings               json.RawMessage `json:"settings,omitempty"`
	Configuration          json.RawMessage `json:"configuration,omitempty"`
	ServiceConnectDefaults json.RawMessage `json:"serviceConnectDefaults,omitempty"`
}

type ECSContainerDefinition struct {
	Name              string               `json:"name"`
	Image             string               `json:"image"`
	Cpu               int                  `json:"cpu,omitempty"`
	Memory            int                  `json:"memory,omitempty"`
	MemoryReservation int                  `json:"memoryReservation,omitempty"`
	Essential         *bool                `json:"essential,omitempty"`
	Environment       []ECSKeyValuePair    `json:"environment,omitempty"`
	MountPoints       []ECSMountPoint      `json:"mountPoints,omitempty"`
	PortMappings      []ECSPortMapping     `json:"portMappings,omitempty"`
	LogConfiguration  *ECSLogConfiguration `json:"logConfiguration,omitempty"`
	EntryPoint        []string             `json:"entryPoint,omitempty"`
	Command           []string             `json:"command,omitempty"`
	PseudoTerminal    bool                 `json:"pseudoTerminal,omitempty"`
	Interactive       bool                 `json:"interactive,omitempty"`
	Privileged        bool                 `json:"privileged,omitempty"`
	// healthCheck and secrets are decoded for the runtime (secret injection reads
	// Secrets); every other field rides the verbatim `raw` capture below.
	HealthCheck json.RawMessage `json:"healthCheck,omitempty"`
	Secrets     json.RawMessage `json:"secrets,omitempty"`

	// raw holds the exact bytes the client registered. The provider folds the
	// whole containerDefinitions JSON into a ForceNew hash, so dropping ANY
	// registered field (ulimits, dependsOn, linuxParameters, dockerLabels, user,
	// workingDirectory, privileged, stop/startTimeout, systemControls, …) forces
	// a new revision every plan. Echoing the captured bytes round-trips every
	// field faithfully while the typed fields above stay available to the runtime.
	//
	// Durability invariant: being unexported, this field is skipped by the
	// persistence envelope's hidden-field sidecar; it survives restarts only
	// because the custom MarshalJSON/UnmarshalJSON codec below re-emits and
	// re-captures the bytes on every persist/load round trip. Removing that
	// codec would silently drop every non-modeled container-definition field
	// from persisted task definitions.
	raw json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes the typed fields the runtime needs and captures the
// verbatim bytes so DescribeTaskDefinition can echo every registered field.
func (c *ECSContainerDefinition) UnmarshalJSON(data []byte) error {
	type alias ECSContainerDefinition
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*c = ECSContainerDefinition(a)
	c.raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON re-emits the captured bytes verbatim when present, so no field is
// silently dropped on read-back. Containers built in-process (no capture) fall
// back to the typed encoding.
func (c ECSContainerDefinition) MarshalJSON() ([]byte, error) {
	if len(c.raw) > 0 {
		return c.raw, nil
	}
	type alias ECSContainerDefinition
	return json.Marshal(alias(c))
}

type ECSKeyValuePair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ECSMountPoint struct {
	SourceVolume  string `json:"sourceVolume"`
	ContainerPath string `json:"containerPath"`
	ReadOnly      bool   `json:"readOnly"`
}

type ECSPortMapping struct {
	ContainerPort int    `json:"containerPort"`
	HostPort      int    `json:"hostPort,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
}

type ECSLogConfiguration struct {
	LogDriver string            `json:"logDriver"`
	Options   map[string]string `json:"options,omitempty"`
}

type ECSVolume struct {
	Name                   string              `json:"name"`
	Host                   *ECSHostVolume      `json:"host,omitempty"`
	EfsVolumeConfiguration *ECSEfsVolumeConfig `json:"efsVolumeConfiguration,omitempty"`
	ConfiguredAtLaunch     bool                `json:"configuredAtLaunch,omitempty"`
}

type ECSHostVolume struct {
	SourcePath string `json:"sourcePath,omitempty"`
}

type ECSEfsVolumeConfig struct {
	FileSystemId        string                     `json:"fileSystemId"`
	RootDirectory       string                     `json:"rootDirectory,omitempty"`
	TransitEncryption   string                     `json:"transitEncryption,omitempty"`
	AuthorizationConfig *ECSEfsAuthorizationConfig `json:"authorizationConfig,omitempty"`
}

type ECSEfsAuthorizationConfig struct {
	AccessPointId string `json:"accessPointId,omitempty"`
	Iam           string `json:"iam,omitempty"`
}

type ECSTaskVolumeConfiguration struct {
	Name             string                                `json:"name"`
	ManagedEBSVolume *ECSTaskManagedEBSVolumeConfiguration `json:"managedEBSVolume,omitempty"`
}

type ECSTaskManagedEBSVolumeConfiguration struct {
	Encrypted         bool                                `json:"encrypted,omitempty"`
	KmsKeyId          string                              `json:"kmsKeyId,omitempty"`
	VolumeType        string                              `json:"volumeType,omitempty"`
	SizeInGiB         int                                 `json:"sizeInGiB,omitempty"`
	SnapshotId        string                              `json:"snapshotId,omitempty"`
	RoleArn           string                              `json:"roleArn,omitempty"`
	TerminationPolicy *ECSTaskManagedEBSTerminationPolicy `json:"terminationPolicy,omitempty"`
	TagSpecifications []ECSTaskManagedEBSTagSpecification `json:"tagSpecifications,omitempty"`
}

type ECSTaskManagedEBSTerminationPolicy struct {
	DeleteOnTermination *bool `json:"deleteOnTermination,omitempty"`
}

type ECSTaskManagedEBSTagSpecification struct {
	ResourceType string   `json:"resourceType,omitempty"`
	Tags         []ECSTag `json:"tags,omitempty"`
}

type ECSTaskOverride struct {
	ContainerOverrides           []ECSContainerOverride `json:"containerOverrides,omitempty"`
	Cpu                          string                 `json:"cpu,omitempty"`
	Memory                       string                 `json:"memory,omitempty"`
	ExecutionRoleArn             string                 `json:"executionRoleArn,omitempty"`
	TaskRoleArn                  string                 `json:"taskRoleArn,omitempty"`
	EphemeralStorage             json.RawMessage        `json:"ephemeralStorage,omitempty"`
	InferenceAcceleratorOverride json.RawMessage        `json:"inferenceAcceleratorOverrides,omitempty"`
}

type ECSContainerOverride struct {
	Name                 string            `json:"name,omitempty"`
	Command              []string          `json:"command,omitempty"`
	Cpu                  *int              `json:"cpu,omitempty"`
	Environment          []ECSKeyValuePair `json:"environment,omitempty"`
	EnvironmentFiles     json.RawMessage   `json:"environmentFiles,omitempty"`
	Memory               *int              `json:"memory,omitempty"`
	MemoryReservation    *int              `json:"memoryReservation,omitempty"`
	ResourceRequirements json.RawMessage   `json:"resourceRequirements,omitempty"`
}

type ECSTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type ECSTaskDefinition struct {
	TaskDefinitionArn       string                   `json:"taskDefinitionArn"`
	Family                  string                   `json:"family"`
	Revision                int                      `json:"revision"`
	ContainerDefinitions    []ECSContainerDefinition `json:"containerDefinitions"`
	Cpu                     string                   `json:"cpu,omitempty"`
	Memory                  string                   `json:"memory,omitempty"`
	NetworkMode             string                   `json:"networkMode,omitempty"`
	RequiresCompatibilities []string                 `json:"requiresCompatibilities,omitempty"`
	ExecutionRoleArn        string                   `json:"executionRoleArn,omitempty"`
	TaskRoleArn             string                   `json:"taskRoleArn,omitempty"`
	Volumes                 []ECSVolume              `json:"volumes,omitempty"`
	// Top-level knobs the provider reads back (all ForceNew); each was dropped on
	// register, so aws_ecs_task_definition.{runtime_platform,ephemeral_storage,
	// proxy_configuration,pid_mode,ipc_mode,placement_constraints,…} drifted into
	// a new revision every plan. Nested objects ride RawMessage (verbatim).
	RuntimePlatform       json.RawMessage `json:"runtimePlatform,omitempty"`
	EphemeralStorage      json.RawMessage `json:"ephemeralStorage,omitempty"`
	ProxyConfiguration    json.RawMessage `json:"proxyConfiguration,omitempty"`
	PlacementConstraints  json.RawMessage `json:"placementConstraints,omitempty"`
	InferenceAccelerators json.RawMessage `json:"inferenceAccelerators,omitempty"`
	PidMode               string          `json:"pidMode,omitempty"`
	IpcMode               string          `json:"ipcMode,omitempty"`
	EnableFaultInjection  *bool           `json:"enableFaultInjection,omitempty"`
	// Compatibilities is the AWS-computed launch-type list (distinct from the
	// requiresCompatibilities input). requiresAttributes is intentionally NOT
	// modelled: it requires AWS's capability-attribute engine, no stable client
	// reads it, and fabricating a list would be a fake.
	Compatibilities []string `json:"compatibilities,omitempty"`
	// Tags are internal-only: real AWS does not carry them inside the
	// taskDefinition object — they surface at the response top level (from
	// RegisterTaskDefinition always, DescribeTaskDefinition only with
	// include=TAGS). Serializing them here would be silently dropped by the SDK
	// model, so the provider would still see no tags. See ecsTaskDefTagsResponse.
	Tags   []ECSTag `json:"-"`
	Status string   `json:"status"`
}

type ECSTaskContainer struct {
	ContainerArn      string                `json:"containerArn"`
	Name              string                `json:"name"`
	LastStatus        string                `json:"lastStatus"`
	ExitCode          *int                  `json:"exitCode,omitempty"`
	Reason            string                `json:"reason,omitempty"`
	RuntimeId         string                `json:"runtimeId,omitempty"`
	NetworkBindings   []ECSNetworkBinding   `json:"networkBindings,omitempty"`
	NetworkInterfaces []ECSNetworkInterface `json:"networkInterfaces,omitempty"`
}

// ECSNetworkBinding is a port the agent reports a container bound, which is
// how a bridge-network task advertises the host port it was given.
type ECSNetworkBinding struct {
	BindIP        string `json:"bindIP,omitempty"`
	ContainerPort int    `json:"containerPort,omitempty"`
	HostPort      int    `json:"hostPort,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
}

type ECSNetworkInterface struct {
	AttachmentId       string `json:"attachmentId"`
	PrivateIpv4Address string `json:"privateIpv4Address"`
}

type ECSAttachment struct {
	Id      string            `json:"id"`
	Type    string            `json:"type"`
	Status  string            `json:"status"`
	Details []ECSKeyValuePair `json:"details,omitempty"`
}

// ECSTaskStatus is the lifecycle status of an ECS task (lastStatus /
// desiredStatus). Using a named type makes a mistyped literal a compile error.
type ECSTaskStatus string

const (
	ECSTaskStatusProvisioning   ECSTaskStatus = "PROVISIONING"
	ECSTaskStatusPending        ECSTaskStatus = "PENDING"
	ECSTaskStatusRunning        ECSTaskStatus = "RUNNING"
	ECSTaskStatusStopped        ECSTaskStatus = "STOPPED"
	ECSTaskStatusDeprovisioning ECSTaskStatus = "DEPROVISIONING"
)

type ECSTask struct {
	TaskArn           string             `json:"taskArn"`
	TaskDefinitionArn string             `json:"taskDefinitionArn"`
	ClusterArn        string             `json:"clusterArn"`
	LastStatus        ECSTaskStatus      `json:"lastStatus"`
	DesiredStatus     ECSTaskStatus      `json:"desiredStatus"`
	Connectivity      string             `json:"connectivity,omitempty"`
	Containers        []ECSTaskContainer `json:"containers"`
	CreatedAt         *float64           `json:"createdAt,omitempty"`
	StartedAt         *int64             `json:"startedAt,omitempty"`
	StoppedAt         *int64             `json:"stoppedAt,omitempty"`
	// The agent reports these while a task runs: when it finished pulling its
	// images and when execution stopped. DescribeTasks returns them, and they
	// were being discarded.
	PullStartedAt        *float64              `json:"pullStartedAt,omitempty"`
	PullStoppedAt        *float64              `json:"pullStoppedAt,omitempty"`
	ExecutionStoppedAt   *float64              `json:"executionStoppedAt,omitempty"`
	StopCode             string                `json:"stopCode,omitempty"`
	StoppedReason        string                `json:"stoppedReason,omitempty"`
	Attachments          []ECSAttachment       `json:"attachments,omitempty"`
	Tags                 []ECSTag              `json:"tags,omitempty"`
	LaunchType           string                `json:"launchType,omitempty"`
	Cpu                  string                `json:"cpu,omitempty"`
	Memory               string                `json:"memory,omitempty"`
	Group                string                `json:"group,omitempty"`
	Overrides            *ECSTaskOverride      `json:"overrides,omitempty"`
	EnableExecuteCommand bool                  `json:"enableExecuteCommand,omitempty"`
	NetworkConfiguration *ECSTaskNetworkConfig `json:"networkConfiguration,omitempty"`
	StartedBy            string                `json:"startedBy,omitempty"`
	ContainerInstanceArn string                `json:"containerInstanceArn,omitempty"`
	// VolumeHosts records the resolved host or named-volume source for every
	// task-definition volume while the task is active. It is internal execution
	// state used only to resume a pre-container restart window.
	VolumeHosts map[string]string `json:"-"`
}

type ECSTaskNetworkConfig struct {
	AwsvpcConfiguration *ECSTaskVpcConfig `json:"awsvpcConfiguration,omitempty"`
}

type ECSTaskVpcConfig struct {
	Subnets        []string `json:"subnets"`
	SecurityGroups []string `json:"securityGroups"`
	AssignPublicIp string   `json:"assignPublicIp"`
}

// ecsTaskWire wraps ECSTask for response emission. The real Task shape
// conveys networking via attachments only; networkConfiguration is a
// store-side field (subnet-egress wiring reads it back from persisted
// tasks) that must never reach the wire. The struct keeps its canonical
// JSON tag so the field survives Store persistence; the wrapper strips
// it from API responses.
type ecsTaskWire struct {
	ECSTask
	includeTags bool
}

func (t ecsTaskWire) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(t.ECSTask)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	delete(m, "networkConfiguration")
	if !t.includeTags {
		delete(m, "tags")
	}
	return json.Marshal(m)
}

func ecsTasksWire(tasks []ECSTask) []ecsTaskWire {
	return ecsTasksWireInclude(tasks, true)
}

func ecsTasksWireInclude(tasks []ECSTask, includeTags bool) []ecsTaskWire {
	out := make([]ecsTaskWire, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, ecsTaskWire{ECSTask: task, includeTags: includeTags})
	}
	return out
}

// State stores
var (
	ecsClusters        sim.Store[ECSCluster]
	ecsTaskDefinitions sim.Store[ECSTaskDefinition]
	// The data plane consults this store on every forwarded request, and a
	// forwarded request can arrive before this service is registered — a
	// simulator assembled without it, or a test that mounts one data plane.
	// An empty store answers the question truthfully (nothing is registered,
	// so nothing is associated); a nil one panicked in the middle of serving.
	ecsTasks sim.Store[ECSTask] = sim.NewStateStore[ECSTask]()
	// ecsRevisionMu guards the task-definition revision index. Resolving a
	// revision is a read on the hot path of every describe and every task
	// start; registering one is the write. Reads take RLock, writes keep Lock,
	// and neither is reentrant.
	ecsRevisionMu     sync.RWMutex
	ecsRevisions      map[string]int // family -> latest revision
	ecsProcessHandles sync.Map       // map[taskID]*ecsTaskProcesses
	// ecsTaskLifecycleLocks serialize asynchronous task startup with StopTask
	// and service-scheduler stop requests. Stop must not return while an
	// in-flight startup can still attach networking or publish RUNNING after
	// the task was stopped.
	ecsTaskLifecycleLocks sync.Map // map[taskID]*sync.Mutex
	// ecsBackgroundServer owns the task-container-start goroutines launched by
	// runECSTasks. Orderly shutdown drains them before SQLite is closed, so an
	// in-flight container start cannot read a durable store after the database
	// is closed (the BUG-2827 class, extended to ECS task startup).
	ecsBackgroundServer *sim.Server
)

type ecsTaskProcesses struct {
	MainContainerName string
	Handles           map[string]*sim.ContainerHandle
}

func (p *ecsTaskProcesses) firstHandle() *sim.ContainerHandle {
	if p == nil {
		return nil
	}
	if p.MainContainerName != "" {
		if h := p.Handles[p.MainContainerName]; h != nil {
			return h
		}
	}
	for _, h := range p.Handles {
		return h
	}
	return nil
}

func (p *ecsTaskProcesses) handleFor(containerName string) *sim.ContainerHandle {
	if p == nil {
		return nil
	}
	if containerName != "" {
		return p.Handles[containerName]
	}
	return p.firstHandle()
}

func ecsTaskLifecycleLock(taskID string) *sync.Mutex {
	lock, _ := ecsTaskLifecycleLocks.LoadOrStore(taskID, &sync.Mutex{})
	mutex, ok := lock.(*sync.Mutex)
	if !ok {
		panic("Amazon ECS task lifecycle lock contained a non-mutex value")
	}
	return mutex
}

func stopECSTaskProcesses(p *ecsTaskProcesses) {
	if p == nil {
		return
	}
	for _, h := range p.Handles {
		if h != nil {
			sim.StopContainer(h.ContainerID, time.Second)
		}
	}
}

func cleanupECSTaskProcesses(taskID string, p *ecsTaskProcesses) {
	if p == nil {
		return
	}
	stopECSTaskProcesses(p)
	handles := make([]*sim.ContainerHandle, 0, len(p.Handles))
	for name, handle := range p.Handles {
		if name != p.MainContainerName && name != "__pause__" {
			handles = append(handles, handle)
		}
	}
	if mainHandle := p.Handles[p.MainContainerName]; mainHandle != nil {
		handles = append(handles, mainHandle)
	}
	if pauseHandle := p.Handles["__pause__"]; pauseHandle != nil {
		handles = append(handles, pauseHandle)
	}
	for _, handle := range handles {
		if handle == nil || handle.ContainerID == "" {
			continue
		}
		if err := sim.WaitContainerRemoved(handle.ContainerID, 5*time.Second); err != nil {
			fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: wait for container cleanup failed: %v\n", taskID, err)
		}
	}
	ec2DetachRealECSTaskNIC(context.Background(), taskID)
}

func generateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func ecsArn(resourceType, id string) string {
	return fmt.Sprintf("arn:aws:ecs:"+awsRegion()+":"+awsAccountID()+":%s/%s", resourceType, id)
}

func registerECS(r *AWSRouter, srv *sim.Server) {
	ecsClusters = sim.MakeStore[ECSCluster](srv.DB(), "ecs_clusters")
	ecsTaskDefinitions = sim.MakeStore[ECSTaskDefinition](srv.DB(), "ecs_task_definitions")
	ecsTasks = sim.MakeStore[ECSTask](srv.DB(), "ecs_tasks")
	ecsBackgroundServer = srv
	ecsStartStoppedTaskSweeper(srv)
	ecsRebuildRevisionIndex()

	r.Register("AmazonEC2ContainerServiceV20141113.CreateCluster", handleECSCreateCluster)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeClusters", handleECSDescribeClusters)
	r.Register("AmazonEC2ContainerServiceV20141113.UpdateCluster", handleECSUpdateCluster)
	r.Register("AmazonEC2ContainerServiceV20141113.UpdateClusterSettings", handleECSUpdateClusterSettings)
	r.Register("AmazonEC2ContainerServiceV20141113.RegisterTaskDefinition", handleECSRegisterTaskDefinition)
	r.Register("AmazonEC2ContainerServiceV20141113.DeregisterTaskDefinition", handleECSDeregisterTaskDefinition)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeTaskDefinition", handleECSDescribeTaskDefinition)
	r.Register("AmazonEC2ContainerServiceV20141113.RunTask", handleECSRunTask)
	r.Register("AmazonEC2ContainerServiceV20141113.DescribeTasks", handleECSDescribeTasks)
	r.Register("AmazonEC2ContainerServiceV20141113.StopTask", handleECSStopTask)
	r.Register("AmazonEC2ContainerServiceV20141113.ListTasks", handleECSListTasks)
	r.Register("AmazonEC2ContainerServiceV20141113.DeleteCluster", handleECSDeleteCluster)
	r.Register("AmazonEC2ContainerServiceV20141113.ListTagsForResource", handleECSListTagsForResource)
	r.Register("AmazonEC2ContainerServiceV20141113.TagResource", handleECSTagResource)
	r.Register("AmazonEC2ContainerServiceV20141113.UntagResource", handleECSUntagResource)
	r.Register("AmazonEC2ContainerServiceV20141113.ExecuteCommand", handleECSExecuteCommand(srv))

	// ECS Service family + cluster capacity providers (aws_ecs_service /
	// aws_ecs_cluster_capacity_providers).
	registerECSServices(r, srv)

	// The wider ECS control-plane surface: capacity providers, blue/green task
	// sets, container instances, account settings, attributes, task protection,
	// daemons, and service deployments.
	registerECSCapacity(r, srv)
	registerECSTaskSets(r, srv)
	registerECSContainerInstances(r, srv)
	registerECSAccount(r, srv)
	registerECSAttributes(r, srv)
	registerECSTaskProtection(r, srv)
	registerECSDaemons(r, srv)
	registerECSServiceDeployments(r, srv)
	registerECSStartTask(r, srv)
	r.Register("AmazonEC2ContainerServiceV20141113.DeleteTaskDefinitions", handleECSDeleteTaskDefinitions)

	// Static WebSocket route for ECS exec sessions (session ID is a path param)
	srv.HandleFunc("GET /ecs-exec/{sessionId}", func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.PathValue("sessionId")
		handleECSExecWebSocket(sessionID)(w, r)
	})

	// Archive upload endpoint: forward tar archive to the Docker container backing an ECS task
	srv.HandleFunc("PUT /sockerless/tasks/{taskId}/archive", func(w http.ResponseWriter, r *http.Request) {
		taskID := r.PathValue("taskId")
		path := r.URL.Query().Get("path")
		if path == "" {
			http.Error(w, "missing path query parameter", http.StatusBadRequest)
			return
		}

		// Poll for the container handle — it may not be stored yet if the
		// Docker container is still starting (async after RUNNING state).
		var handle *sim.ContainerHandle
		for i := 0; i < 20; i++ {
			if v, ok := ecsProcessHandles.Load(taskID); ok {
				if procs, ok := v.(*ecsTaskProcesses); ok {
					handle = procs.firstHandle()
				}
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if handle == nil {
			http.Error(w, "no running container for task "+taskID, http.StatusNotFound)
			return
		}

		cli := sim.DockerClient()
		if cli == nil {
			http.Error(w, "docker client not available", http.StatusInternalServerError)
			return
		}

		// Create target directory if it doesn't exist
		mkdirExec, mkdirErr := cli.ExecCreate(r.Context(), handle.ContainerID, dockerclient.ExecCreateOptions{
			Cmd: []string{"mkdir", "-p", path},
		})
		if mkdirErr == nil {
			_, _ = cli.ExecStart(r.Context(), mkdirExec.ID, dockerclient.ExecStartOptions{})
		}

		_, err := cli.CopyToContainer(r.Context(), handle.ContainerID, dockerclient.CopyToContainerOptions{
			DestinationPath:           path,
			Content:                   r.Body,
			AllowOverwriteDirWithFile: true,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func ecsRebuildRevisionIndex() {
	ecsRevisionMu.Lock()
	defer ecsRevisionMu.Unlock()
	ecsRevisions = make(map[string]int)
	for _, definition := range ecsTaskDefinitions.List() {
		if definition.Family != "" && definition.Revision > ecsRevisions[definition.Family] {
			ecsRevisions[definition.Family] = definition.Revision
		}
	}
}

func handleECSCreateCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClusterName                     string          `json:"clusterName"`
		Tags                            []ECSTag        `json:"tags"`
		CapacityProviders               []string        `json:"capacityProviders"`
		DefaultCapacityProviderStrategy json.RawMessage `json:"defaultCapacityProviderStrategy"`
		Settings                        json.RawMessage `json:"settings"`
		Configuration                   json.RawMessage `json:"configuration"`
		ServiceConnectDefaults          json.RawMessage `json:"serviceConnectDefaults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ClusterName == "" {
		req.ClusterName = "default"
	}

	cluster := ECSCluster{
		ClusterArn:                      ecsArn("cluster", req.ClusterName),
		ClusterName:                     req.ClusterName,
		Status:                          "ACTIVE",
		Tags:                            req.Tags,
		CapacityProviders:               req.CapacityProviders,
		DefaultCapacityProviderStrategy: req.DefaultCapacityProviderStrategy,
		Settings:                        req.Settings,
		Configuration:                   req.Configuration,
		ServiceConnectDefaults:          req.ServiceConnectDefaults,
	}
	ecsClusters.Put(req.ClusterName, cluster)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"cluster": cluster,
	})
}

func handleECSDescribeClusters(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Clusters []string `json:"clusters"`
		Include  []string `json:"include"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	includeSettings, includeConfig := false, false
	for _, inc := range req.Include {
		switch inc {
		case "SETTINGS":
			includeSettings = true
		case "CONFIGURATIONS":
			includeConfig = true
		}
	}

	var clusters []ECSCluster
	var failures []map[string]string

	for _, nameOrArn := range req.Clusters {
		// Extract cluster name from ARN if needed
		name := nameOrArn
		if strings.HasPrefix(nameOrArn, "arn:") {
			parts := strings.Split(nameOrArn, "/")
			if len(parts) > 1 {
				name = parts[len(parts)-1]
			}
		}

		cluster, ok := ecsClusters.Get(name)
		if ok {
			// Update running task count
			runningCount := 0
			for _, t := range ecsTasks.List() {
				if t.ClusterArn == cluster.ClusterArn && t.LastStatus == ECSTaskStatusRunning {
					runningCount++
				}
			}
			cluster.RunningTasksCount = runningCount
			activeServices := 0
			for _, s := range ecsServices.List() {
				if s.ClusterArn == cluster.ClusterArn && s.Status == "ACTIVE" {
					activeServices++
				}
			}
			cluster.ActiveServicesCount = activeServices
			// settings / configuration only surface when explicitly included,
			// matching real DescribeClusters.
			if !includeSettings {
				cluster.Settings = nil
			}
			if !includeConfig {
				cluster.Configuration = nil
			}
			clusters = append(clusters, cluster)
		} else {
			failures = append(failures, map[string]string{
				"arn":    ecsArn("cluster", name),
				"reason": "MISSING",
			})
		}
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"clusters": clusters,
		"failures": failures,
	})
}

// handleECSUpdateCluster updates a cluster's settings / configuration /
// serviceConnectDefaults in place. Without it, any change to
// aws_ecs_cluster.{setting,configuration,service_connect_defaults} forced
// recreation.
func handleECSUpdateCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster                string          `json:"cluster"`
		Settings               json.RawMessage `json:"settings"`
		Configuration          json.RawMessage `json:"configuration"`
		ServiceConnectDefaults json.RawMessage `json:"serviceConnectDefaults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name := ecsClusterNameFromRef(req.Cluster)
	cluster, ok := ecsClusters.Get(name)
	if !ok {
		AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest, "Cluster not found: %s", req.Cluster)
		return
	}
	if req.Settings != nil {
		cluster.Settings = req.Settings
	}
	if req.Configuration != nil {
		cluster.Configuration = req.Configuration
	}
	if req.ServiceConnectDefaults != nil {
		cluster.ServiceConnectDefaults = req.ServiceConnectDefaults
	}
	ecsClusters.Put(name, cluster)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"cluster": cluster})
}

// handleECSUpdateClusterSettings updates only the cluster settings
// (containerInsights). The provider uses it for aws_ecs_cluster.setting changes.
func handleECSUpdateClusterSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster  string          `json:"cluster"`
		Settings json.RawMessage `json:"settings"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name := ecsClusterNameFromRef(req.Cluster)
	cluster, ok := ecsClusters.Get(name)
	if !ok {
		AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest, "Cluster not found: %s", req.Cluster)
		return
	}
	if req.Settings != nil {
		cluster.Settings = req.Settings
	}
	ecsClusters.Put(name, cluster)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"cluster": cluster})
}

func handleECSRegisterTaskDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Family                  string                   `json:"family"`
		ContainerDefinitions    []ECSContainerDefinition `json:"containerDefinitions"`
		Cpu                     string                   `json:"cpu,omitempty"`
		Memory                  string                   `json:"memory,omitempty"`
		NetworkMode             string                   `json:"networkMode,omitempty"`
		RequiresCompatibilities []string                 `json:"requiresCompatibilities,omitempty"`
		ExecutionRoleArn        string                   `json:"executionRoleArn,omitempty"`
		TaskRoleArn             string                   `json:"taskRoleArn,omitempty"`
		Volumes                 []ECSVolume              `json:"volumes,omitempty"`
		RuntimePlatform         json.RawMessage          `json:"runtimePlatform,omitempty"`
		EphemeralStorage        json.RawMessage          `json:"ephemeralStorage,omitempty"`
		ProxyConfiguration      json.RawMessage          `json:"proxyConfiguration,omitempty"`
		PlacementConstraints    json.RawMessage          `json:"placementConstraints,omitempty"`
		InferenceAccelerators   json.RawMessage          `json:"inferenceAccelerators,omitempty"`
		PidMode                 string                   `json:"pidMode,omitempty"`
		IpcMode                 string                   `json:"ipcMode,omitempty"`
		EnableFaultInjection    *bool                    `json:"enableFaultInjection,omitempty"`
		Tags                    []ECSTag                 `json:"tags,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Family == "" {
		AWSError(w, "InvalidParameterException", "Family is required", http.StatusBadRequest)
		return
	}
	if len(req.ContainerDefinitions) == 0 {
		AWSError(w, "InvalidParameterException", "At least one container definition is required", http.StatusBadRequest)
		return
	}

	// Fargate requires task-level cpu and memory — AWS runs this compatibility
	// check when requiresCompatibilities includes FARGATE and rejects a task
	// definition missing either with a ClientException.
	if hasFargate(req.RequiresCompatibilities) && (req.Cpu == "" || req.Memory == "") {
		AWSError(w, "ClientException",
			"Task definition does not support launch type FARGATE: task-level memory and cpu are required.",
			http.StatusBadRequest)
		return
	}

	// Validate Fargate CPU/memory combinations
	if hasFargate(req.RequiresCompatibilities) && req.Cpu != "" && req.Memory != "" {
		if err := validateFargateResources(req.Cpu, req.Memory); err != nil {
			AWSError(w, "ClientException", err.Error(), http.StatusBadRequest)
			return
		}
	}

	// Auto-increment revision
	ecsRevisionMu.Lock()
	ecsRevisions[req.Family]++
	revision := ecsRevisions[req.Family]
	ecsRevisionMu.Unlock()

	td := ECSTaskDefinition{
		TaskDefinitionArn:       fmt.Sprintf("arn:aws:ecs:"+awsRegion()+":"+awsAccountID()+":task-definition/%s:%d", req.Family, revision),
		Family:                  req.Family,
		Revision:                revision,
		ContainerDefinitions:    req.ContainerDefinitions,
		Cpu:                     req.Cpu,
		Memory:                  req.Memory,
		NetworkMode:             req.NetworkMode,
		RequiresCompatibilities: req.RequiresCompatibilities,
		ExecutionRoleArn:        req.ExecutionRoleArn,
		TaskRoleArn:             req.TaskRoleArn,
		Volumes:                 req.Volumes,
		RuntimePlatform:         req.RuntimePlatform,
		EphemeralStorage:        req.EphemeralStorage,
		ProxyConfiguration:      req.ProxyConfiguration,
		PlacementConstraints:    req.PlacementConstraints,
		InferenceAccelerators:   req.InferenceAccelerators,
		PidMode:                 req.PidMode,
		IpcMode:                 req.IpcMode,
		EnableFaultInjection:    req.EnableFaultInjection,
		Compatibilities:         ecsComputeCompatibilities(req.NetworkMode, req.RequiresCompatibilities),
		Tags:                    req.Tags,
		Status:                  "ACTIVE",
	}

	key := fmt.Sprintf("%s:%d", req.Family, revision)
	ecsTaskDefinitions.Put(key, td)

	// Real RegisterTaskDefinition echoes the tags at the response top level.
	resp := map[string]any{"taskDefinition": td}
	if len(td.Tags) > 0 {
		resp["tags"] = td.Tags
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleECSDeregisterTaskDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskDefinition string `json:"taskDefinition"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.TaskDefinition == "" {
		AWSError(w, "InvalidParameterException", "taskDefinition is required", http.StatusBadRequest)
		return
	}

	// Extract family:revision from ARN or direct reference
	key := req.TaskDefinition
	if strings.HasPrefix(key, "arn:") {
		parts := strings.Split(key, "/")
		if len(parts) > 1 {
			key = parts[len(parts)-1]
		}
	}

	found := ecsTaskDefinitions.Update(key, func(td *ECSTaskDefinition) {
		td.Status = "INACTIVE"
	})

	if !found {
		AWSErrorf(w, "ClientException", http.StatusBadRequest,
			"Unable to describe task definition: %s", req.TaskDefinition)
		return
	}

	td, _ := ecsTaskDefinitions.Get(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"taskDefinition": td,
	})
}

func handleECSDescribeTaskDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskDefinition string   `json:"taskDefinition"`
		Include        []string `json:"include"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.TaskDefinition == "" {
		AWSError(w, "InvalidParameterException", "taskDefinition is required", http.StatusBadRequest)
		return
	}

	key := req.TaskDefinition
	if strings.HasPrefix(key, "arn:") {
		parts := strings.Split(key, "/")
		if len(parts) > 1 {
			key = parts[len(parts)-1]
		}
	}

	// If no revision specified, find the latest active one
	if !strings.Contains(key, ":") {
		ecsRevisionMu.RLock()
		rev, exists := ecsRevisions[key]
		ecsRevisionMu.RUnlock()
		if exists {
			key = fmt.Sprintf("%s:%d", key, rev)
		}
	}

	td, ok := ecsTaskDefinitions.Get(key)
	if !ok {
		AWSErrorf(w, "ClientException", http.StatusBadRequest,
			"Unable to describe task definition: %s", req.TaskDefinition)
		return
	}

	// Tags surface at the response top level only when include=TAGS — this is
	// the path terraform-provider-aws reads task-definition tags on refresh.
	resp := map[string]any{"taskDefinition": td}
	if len(td.Tags) > 0 && ecsIncludeHasTags(req.Include) {
		resp["tags"] = td.Tags
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// ecsComputeCompatibilities derives the AWS-computed launch-type list. Every
// task definition is EC2-compatible; FARGATE additionally requires the awsvpc
// network mode. requiresCompatibilities is always a subset of the result.
func ecsComputeCompatibilities(networkMode string, requires []string) []string {
	set := map[string]bool{"EC2": true}
	for _, c := range requires {
		set[strings.ToUpper(c)] = true
	}
	if strings.EqualFold(networkMode, "awsvpc") {
		set["FARGATE"] = true
	}
	var out []string
	for _, c := range []string{"EC2", "FARGATE", "EXTERNAL"} {
		if set[c] {
			out = append(out, c)
		}
	}
	return out
}

// Amazon Elastic Container Service (ECS) task networking modes, as spelled by
// the ECS API's NetworkMode enum.
const (
	ecsNetworkModeAwsvpc = "awsvpc"
	ecsNetworkModeBridge = "bridge"
	ecsNetworkModeHost   = "host"
	ecsNetworkModeNone   = "none"
)

// ecsEffectiveNetworkMode returns the networking mode a task definition's
// containers run under. Amazon ECS defaults an unset networkMode to bridge, so
// the launch path resolves the default once instead of treating "" as a mode of
// its own.
func ecsEffectiveNetworkMode(td ECSTaskDefinition) string {
	mode := strings.ToLower(strings.TrimSpace(td.NetworkMode))
	if mode == "" {
		return ecsNetworkModeBridge
	}
	return mode
}

// ecsIncludeHasTags reports whether the DescribeTaskDefinition include list
// requests TAGS (case-insensitive, matching the AWS enum).
func ecsIncludeHasTags(include []string) bool {
	for _, v := range include {
		if strings.EqualFold(v, "TAGS") {
			return true
		}
	}
	return false
}

type ecsRequestError struct {
	code    string
	message string
	status  int
}

func ecsConfiguredAtLaunchVolumes(td ECSTaskDefinition) map[string]bool {
	out := map[string]bool{}
	for _, vol := range td.Volumes {
		if vol.ConfiguredAtLaunch {
			out[vol.Name] = true
		}
	}
	return out
}

func ecsManagedEBSTags(specs []ECSTaskManagedEBSTagSpecification) []EC2Tag {
	var tags []EC2Tag
	for _, spec := range specs {
		if spec.ResourceType != "" && spec.ResourceType != "volume" {
			continue
		}
		for _, tag := range spec.Tags {
			tags = append(tags, EC2Tag(tag))
		}
	}
	return tags
}

func ecsTaskDetail(details []ECSKeyValuePair, name string) string {
	for _, detail := range details {
		if detail.Name == name {
			return detail.Value
		}
	}
	return ""
}

// ecsPendingEBSRestore is a managed-EBS snapshot restore that has been recorded
// but not yet performed. Real ECS returns RunTask in milliseconds and hydrates
// the volume while the task moves PROVISIONING → PENDING → RUNNING, which is why
// an EBS attachment reports ATTACHING at all; a volume created from a snapshot
// is usable immediately there and its blocks fault in lazily from S3. Copying
// the data inline in the request handler instead made RunTask block for the
// length of the copy, so any reverse proxy in front of the simulator timed the
// call out and the caller saw a 502 rather than a task it could poll.
type ecsPendingEBSRestore struct {
	AttachmentID string
	VolumeName   string
	DockerSrc    string
	DockerDst    string
	HostSrc      string
	HostDst      string
}

func ecsPrepareManagedEBSVolumes(ctx context.Context, td ECSTaskDefinition, configs []ECSTaskVolumeConfiguration, taskID, requestedSubnet string) (map[string]string, []ECSAttachment, []ecsPendingEBSRestore, *ecsRequestError) {
	allowed := ecsConfiguredAtLaunchVolumes(td)
	hosts := map[string]string{}
	var attachments []ECSAttachment
	var restores []ecsPendingEBSRestore
	if len(configs) == 0 {
		return hosts, attachments, restores, nil
	}

	az := awsAvailabilityZone()
	if requestedSubnet != "" {
		if subnet, ok := ec2Subnets.Get(requestedSubnet); ok && subnet.AvailabilityZone != "" {
			az = subnet.AvailabilityZone
		}
	}

	for _, cfg := range configs {
		if cfg.Name == "" {
			return nil, nil, nil, &ecsRequestError{"InvalidParameterException", "volumeConfigurations.name is required", http.StatusBadRequest}
		}
		if !allowed[cfg.Name] {
			return nil, nil, nil, &ecsRequestError{"ClientException", fmt.Sprintf("Volume %s is not configuredAtLaunch in the task definition", cfg.Name), http.StatusBadRequest}
		}
		managed := cfg.ManagedEBSVolume
		if managed == nil {
			return nil, nil, nil, &ecsRequestError{"ClientException", fmt.Sprintf("Volume %s requires managedEBSVolume", cfg.Name), http.StatusBadRequest}
		}

		size := managed.SizeInGiB
		if size == 0 {
			size = 8
		}
		snapshotID := managed.SnapshotId
		var snapshotDockerVolumeName string
		var snapshotHostPath string
		if snapshotID != "" {
			snap, ok := ec2Snapshots.Get(snapshotID)
			if !ok {
				return nil, nil, nil, &ecsRequestError{"InvalidParameterException", fmt.Sprintf("Snapshot not found: %s", snapshotID), http.StatusBadRequest}
			}
			if snap.State != "completed" {
				return nil, nil, nil, &ecsRequestError{"InvalidParameterException", fmt.Sprintf("Snapshot is not completed: %s", snapshotID), http.StatusBadRequest}
			}
			if size < snap.VolumeSize {
				size = snap.VolumeSize
			}
			snapshotDockerVolumeName = snap.DockerVolumeName
			snapshotHostPath = snap.HostPath
		}
		volumeType := managed.VolumeType
		if volumeType == "" {
			volumeType = "gp3"
		}
		deleteOnTermination := true
		if managed.TerminationPolicy != nil && managed.TerminationPolicy.DeleteOnTermination != nil {
			deleteOnTermination = *managed.TerminationPolicy.DeleteOnTermination
		}

		volumeID := ec2ID("vol")
		now := time.Now().UTC().Format(time.RFC3339)
		vol := EC2Volume{
			VolumeId:         volumeID,
			Size:             size,
			SnapshotId:       snapshotID,
			AvailabilityZone: az,
			State:            "in-use",
			CreateTime:       now,
			VolumeType:       volumeType,
			Encrypted:        managed.Encrypted,
			Tags:             ecsManagedEBSTags(managed.TagSpecifications),
			Attachments: []EC2VolumeAttachment{{
				VolumeId:            volumeID,
				InstanceId:          taskID,
				Device:              cfg.Name,
				State:               "attached",
				AttachTime:          now,
				DeleteOnTermination: deleteOnTermination,
			}},
		}
		// In process mode (SIM_RUNTIME=process) there is no Docker client, so
		// back the managed-EBS volume with a host-path directory — the same
		// in-memory model ec2:CreateVolume uses — rather than a Docker named
		// volume. Otherwise the deleteOnTermination cleanup (ebsRemoveDockerVolume)
		// would dereference the nil Docker client and panic the transition
		// goroutine. Container mode keeps the Docker named volume.
		processMode := sim.DockerClient() == nil
		if processMode {
			if err := ebsPrepareVolumeHostPath(&vol); err != nil {
				return nil, nil, nil, &ecsRequestError{"InternalError", fmt.Sprintf("could not create managed EBS volume data path: %v", err), http.StatusInternalServerError}
			}
		} else {
			vol.DockerVolumeName = ebsECSDockerVolumeName(volumeID)
		}
		// Docker auto-creates the destination volume on first container use so no
		// explicit VolumeCreate is needed — the copy container triggers creation.
		// The copy itself is deferred: see ecsPendingEBSRestore.
		if snapshotDockerVolumeName != "" {
			if processMode {
				return nil, nil, nil, &ecsRequestError{"InvalidParameterException", fmt.Sprintf("managed-EBS snapshot %s is Docker-volume-backed and cannot be restored under SIM_RUNTIME=process — start the simulator in container runtime to restore it", snapshotID), http.StatusBadRequest}
			}
			restores = append(restores, ecsPendingEBSRestore{
				AttachmentID: "ebs-" + volumeID,
				VolumeName:   cfg.Name,
				DockerSrc:    snapshotDockerVolumeName,
				DockerDst:    vol.DockerVolumeName,
			})
		} else if snapshotHostPath != "" {
			// Snapshot came from an EC2/Firecracker volume (host-path); fall back to
			// directory copy. Only works in on-host topology where the sim process runs
			// on the same machine as the Docker host.
			if err := ebsPrepareVolumeHostPath(&vol); err != nil {
				return nil, nil, nil, &ecsRequestError{"InternalError", fmt.Sprintf("could not create managed EBS volume data path: %v", err), http.StatusInternalServerError}
			}
			restores = append(restores, ecsPendingEBSRestore{
				AttachmentID: "ebs-" + volumeID,
				VolumeName:   cfg.Name,
				HostSrc:      snapshotHostPath,
				HostDst:      vol.HostPath,
			})
			// Use host-path bind-mount for this volume since the data is on-disk.
			ec2Volumes.Put(volumeID, vol)
			hosts[cfg.Name] = vol.HostPath
			attachments = append(attachments, ECSAttachment{
				Id:     "ebs-" + volumeID,
				Type:   "AmazonElasticBlockStorage",
				Status: "ATTACHING",
				Details: []ECSKeyValuePair{
					{Name: "volumeName", Value: cfg.Name},
					{Name: "volumeId", Value: volumeID},
					{Name: "deleteOnTermination", Value: strconv.FormatBool(deleteOnTermination)},
				},
			})
			continue
		}
		ec2Volumes.Put(volumeID, vol)
		if processMode {
			hosts[cfg.Name] = vol.HostPath
		} else {
			hosts[cfg.Name] = vol.DockerVolumeName
		}
		attachments = append(attachments, ECSAttachment{
			Id:     "ebs-" + volumeID,
			Type:   "AmazonElasticBlockStorage",
			Status: "ATTACHING",
			Details: []ECSKeyValuePair{
				{Name: "volumeName", Value: cfg.Name},
				{Name: "volumeId", Value: volumeID},
				{Name: "deleteOnTermination", Value: strconv.FormatBool(deleteOnTermination)},
			},
		})
	}
	return hosts, attachments, restores, nil
}

// ecsRunPendingEBSRestores hydrates managed-EBS volumes created from a snapshot
// and marks their attachments ATTACHED once the data is in place. It runs on the
// task's transition path rather than in the RunTask handler, so a large restore
// delays only this task's move to RUNNING — which a caller polling DescribeTasks
// is already prepared for — instead of holding the API response open.
func ecsRunPendingEBSRestores(ctx context.Context, taskID string, restores []ecsPendingEBSRestore) error {
	for _, restore := range restores {
		switch {
		case restore.DockerDst != "":
			if err := ebsCopyDockerVolumes(ctx, restore.DockerSrc, restore.DockerDst); err != nil {
				return fmt.Errorf("volume %s: %w", restore.VolumeName, err)
			}
		case restore.HostDst != "":
			if err := ebsCopyDir(restore.HostDst, restore.HostSrc); err != nil {
				return fmt.Errorf("volume %s: %w", restore.VolumeName, err)
			}
		}
		ecsTasks.Update(taskID, func(t *ECSTask) {
			for i := range t.Attachments {
				if t.Attachments[i].Id == restore.AttachmentID {
					t.Attachments[i].Status = "ATTACHED"
				}
			}
		})
	}
	return nil
}

func ecsCleanupTaskManagedEBS(task *ECSTask) {
	for i := range task.Attachments {
		att := &task.Attachments[i]
		if att.Type != "AmazonElasticBlockStorage" {
			continue
		}
		volumeID := ecsTaskDetail(att.Details, "volumeId")
		if volumeID == "" {
			continue
		}
		deleteOnTermination, _ := strconv.ParseBool(ecsTaskDetail(att.Details, "deleteOnTermination"))
		if deleteOnTermination {
			if vol, ok := ec2Volumes.Get(volumeID); ok {
				if vol.DockerVolumeName != "" {
					ebsRemoveDockerVolume(vol.DockerVolumeName)
				} else {
					_ = os.RemoveAll(vol.HostPath)
				}
			}
			ec2Volumes.Delete(volumeID)
		} else {
			ec2Volumes.Update(volumeID, func(vol *EC2Volume) {
				vol.State = "available"
				vol.Attachments = nil
			})
		}
		att.Status = "DETACHED"
	}
}

// ecsTaskCloudWatchSink prepares CloudWatch Logs resources for a task and
// returns a log sink for containers using the awslogs driver. The log group and
// stream are created before the container starts so they are observable as soon
// as the task reports RUNNING.
func ecsTaskCloudWatchSink(td ECSTaskDefinition, taskID string) sim.LogSink {
	var sink sim.LogSink = discardLogSink{}
	for _, cd := range td.ContainerDefinitions {
		if cd.LogConfiguration == nil || cd.LogConfiguration.LogDriver != "awslogs" {
			continue
		}
		logGroup := cd.LogConfiguration.Options["awslogs-group"]
		streamPrefix := cd.LogConfiguration.Options["awslogs-stream-prefix"]
		if logGroup == "" || streamPrefix == "" {
			continue
		}
		logStreamName := fmt.Sprintf("%s/%s/%s", streamPrefix, cd.Name, taskID)
		nowMs := time.Now().UnixMilli()

		// Create log group if not exists
		if _, exists := cwLogGroups.Get(logGroup); !exists {
			cwLogGroups.Put(logGroup, CWLogGroup{
				LogGroupName: logGroup,
				Arn:          cwLogGroupArn(logGroup),
				CreationTime: nowMs,
			})
		}

		// Create log stream
		key := cwEventsKey(logGroup, logStreamName)
		cwLogStreams.Put(key, CWLogStream{
			LogStreamName:       logStreamName,
			LogGroupName:        logGroup,
			CreationTime:        nowMs,
			FirstEventTimestamp: nowMs,
			LastEventTimestamp:  nowMs,
			Arn:                 cwLogStreamArn(logGroup, logStreamName),
			UploadSequenceToken: "1",
		})

		// Insert initial log event
		cmdDesc := strings.Join(append(cd.EntryPoint, cd.Command...), " ")
		if cmdDesc == "" {
			cmdDesc = "container started"
		}
		cwLogEvents.Put(key, []CWLogEvent{
			{
				Timestamp:     nowMs,
				Message:       cmdDesc,
				IngestionTime: nowMs,
			},
		})

		sink = &cwLogSink{logGroup: logGroup, logStream: logStreamName}
		break
	}
	return sink
}

// ecsRunTaskInput is the parsed RunTask request — extracted so the in-process
// service scheduler can launch tasks through the same code path as the
// RunTask API handler.
type ecsRunTaskInput struct {
	Cluster              string
	TaskDefinition       string
	Count                int
	Group                string
	LaunchType           string
	Tags                 []ECSTag
	PropagateTags        string
	EnableExecuteCommand bool
	Overrides            *ECSTaskOverride
	VolumeConfigurations []ECSTaskVolumeConfiguration
	NetworkConfiguration *ECSTaskNetworkConfig
	StartedBy            string
	ContainerInstanceArn string
	ContainerInstanceKey string
}

func ecsUpdateContainerInstanceTaskCounts(key string, pendingDelta, runningDelta int) {
	if key == "" {
		return
	}
	ecsContainerInstances.Update(key, func(instance *ECSContainerInstance) {
		instance.PendingTasksCount += pendingDelta
		instance.RunningTasksCount += runningDelta
		if instance.PendingTasksCount < 0 {
			instance.PendingTasksCount = 0
		}
		if instance.RunningTasksCount < 0 {
			instance.RunningTasksCount = 0
		}
	})
}

func ecsContainerInstanceKeyFromARN(arn string) string {
	resource := arn
	if i := strings.LastIndex(resource, ":"); i >= 0 {
		resource = resource[i+1:]
	}
	parts := strings.Split(resource, "/")
	if len(parts) < 3 {
		return ""
	}
	return ecsContainerInstanceKey(parts[len(parts)-2], parts[len(parts)-1])
}

func handleECSRunTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster              string                       `json:"cluster"`
		TaskDefinition       string                       `json:"taskDefinition"`
		Count                int                          `json:"count"`
		LaunchType           string                       `json:"launchType"`
		Group                string                       `json:"group"`
		StartedBy            string                       `json:"startedBy"`
		Tags                 []ECSTag                     `json:"tags,omitempty"`
		PropagateTags        string                       `json:"propagateTags,omitempty"`
		EnableExecuteCommand bool                         `json:"enableExecuteCommand,omitempty"`
		Overrides            *ECSTaskOverride             `json:"overrides,omitempty"`
		VolumeConfigurations []ECSTaskVolumeConfiguration `json:"volumeConfigurations,omitempty"`
		NetworkConfiguration *ECSTaskNetworkConfig        `json:"networkConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.TaskDefinition == "" {
		AWSError(w, "InvalidParameterException", "taskDefinition is required", http.StatusBadRequest)
		return
	}
	if req.Count == 0 {
		req.Count = 1
	}
	// RunTask accepts up to 10 tasks per call (documented max).
	if req.Count > 10 {
		AWSError(w, "InvalidParameterException",
			"count cannot be greater than 10.", http.StatusBadRequest)
		return
	}
	in := ecsRunTaskInput{
		Cluster:              req.Cluster,
		TaskDefinition:       req.TaskDefinition,
		Count:                req.Count,
		Group:                req.Group,
		LaunchType:           req.LaunchType,
		StartedBy:            req.StartedBy,
		Tags:                 req.Tags,
		PropagateTags:        req.PropagateTags,
		EnableExecuteCommand: req.EnableExecuteCommand,
		Overrides:            req.Overrides,
		VolumeConfigurations: req.VolumeConfigurations,
		NetworkConfiguration: req.NetworkConfiguration,
	}
	tasks, rerr := runECSTasks(r.Context(), in)
	if rerr != nil {
		AWSError(w, rerr.code, rerr.message, rerr.status)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"tasks":    ecsTasksWire(tasks),
		"failures": []any{},
	})
}

// runECSTasks launches `in.Count` tasks for the named cluster / task
// definition, performing the same validation, ENI allocation, and async
// PROVISIONING → PENDING → RUNNING lifecycle transitions as the RunTask API.
// Used by handleECSRunTask and the in-process ECS service scheduler.
func runECSTasks(ctx context.Context, in ecsRunTaskInput) ([]ECSTask, *ecsRequestError) {
	if in.Cluster == "" {
		in.Cluster = "default"
	}

	// Resolve cluster name
	clusterName := in.Cluster
	if strings.HasPrefix(clusterName, "arn:") {
		parts := strings.Split(clusterName, "/")
		if len(parts) > 1 {
			clusterName = parts[len(parts)-1]
		}
	}

	cluster, ok := ecsClusters.Get(clusterName)
	if !ok {
		return nil, &ecsRequestError{"ClusterNotFoundException",
			fmt.Sprintf("Cluster not found: %s", in.Cluster), http.StatusBadRequest}
	}

	// Resolve task definition
	tdKey := in.TaskDefinition
	if strings.HasPrefix(tdKey, "arn:") {
		parts := strings.Split(tdKey, "/")
		if len(parts) > 1 {
			tdKey = parts[len(parts)-1]
		}
	}
	if !strings.Contains(tdKey, ":") {
		ecsRevisionMu.Lock()
		rev, exists := ecsRevisions[tdKey]
		ecsRevisionMu.Unlock()
		if exists {
			tdKey = fmt.Sprintf("%s:%d", tdKey, rev)
		}
	}

	td, ok := ecsTaskDefinitions.Get(tdKey)
	if !ok {
		return nil, &ecsRequestError{"ClientException",
			fmt.Sprintf("Unable to describe task definition: %s", in.TaskDefinition), http.StatusBadRequest}
	}

	// Validate security groups exist
	if in.NetworkConfiguration != nil && in.NetworkConfiguration.AwsvpcConfiguration != nil {
		for _, sgID := range in.NetworkConfiguration.AwsvpcConfiguration.SecurityGroups {
			if _, sgOK := ec2SecurityGroups.Get(sgID); !sgOK {
				return nil, &ecsRequestError{"InvalidParameterException",
					fmt.Sprintf("The security group '%s' does not exist", sgID), http.StatusBadRequest}
			}
		}
	}

	// networkConfiguration is required for awsvpc task definitions — that is
	// what allocates the task its own elastic network interface — and is not
	// supported for any other network mode, where the task shares the container
	// instance's networking. Rejecting both mismatches keeps a caller from
	// silently getting networking it did not ask for.
	networkMode := ecsEffectiveNetworkMode(td)
	if strings.EqualFold(in.LaunchType, "FARGATE") && networkMode != ecsNetworkModeAwsvpc {
		return nil, &ecsRequestError{"ClientException",
			"Fargate tasks require the awsvpc network mode.", http.StatusBadRequest}
	}
	hasAwsvpcConfig := in.NetworkConfiguration != nil &&
		in.NetworkConfiguration.AwsvpcConfiguration != nil &&
		len(in.NetworkConfiguration.AwsvpcConfiguration.Subnets) > 0
	switch {
	case networkMode == ecsNetworkModeAwsvpc && !hasAwsvpcConfig:
		return nil, &ecsRequestError{"InvalidParameterException",
			"Network Configuration must be provided when networking mode is awsvpc.", http.StatusBadRequest}
	case networkMode != ecsNetworkModeAwsvpc && in.NetworkConfiguration != nil:
		return nil, &ecsRequestError{"InvalidParameterException",
			fmt.Sprintf("Network Configuration is not supported when networking mode is %s.", networkMode),
			http.StatusBadRequest}
	}

	// Real ECS validates the subnet exists in EC2 and uses its CIDR for
	// task IP assignment. Pull the requested subnet up front; surface a
	// clean InvalidParameterException when the caller passes one we
	// don't know about (matches real AWS InvalidSubnetID.NotFound).
	var requestedSubnet string
	if hasAwsvpcConfig {
		requestedSubnet = in.NetworkConfiguration.AwsvpcConfiguration.Subnets[0]
	}

	var tasks []ECSTask
	for i := 0; i < in.Count; i++ {
		_ = i
		taskID := generateUUID()
		taskArn := fmt.Sprintf("arn:aws:ecs:"+awsRegion()+":"+awsAccountID()+":task/%s/%s", clusterName, taskID)

		// Only an awsvpc task is allocated an elastic network interface. A
		// bridge/host/none task shares the container instance's networking and
		// carries no ENI attachment and no per-container networkInterfaces.
		eniID := generateUUID()
		var privateIP, subnetID string
		if networkMode == ecsNetworkModeAwsvpc {
			ip, ipErr := AllocateSubnetIP(requestedSubnet)
			if ipErr != nil {
				return nil, &ecsRequestError{"InvalidParameterException", ipErr.Error(), http.StatusBadRequest}
			}
			privateIP = ip
			subnetID = requestedSubnet
		}
		createdAt := float64(time.Now().Unix())

		var containers []ECSTaskContainer
		for _, cd := range td.ContainerDefinitions {
			c := ECSTaskContainer{
				ContainerArn: fmt.Sprintf("arn:aws:ecs:"+awsRegion()+":"+awsAccountID()+":container/%s", generateUUID()),
				Name:         cd.Name,
				LastStatus:   "PROVISIONING",
			}
			if networkMode == ecsNetworkModeAwsvpc {
				c.NetworkInterfaces = []ECSNetworkInterface{{
					AttachmentId:       eniID,
					PrivateIpv4Address: privateIP,
				}}
			}
			containers = append(containers, c)
		}

		// Merge tags: request tags take priority, then inherited from task def
		var taskTags []ECSTag
		if in.PropagateTags == "TASK_DEFINITION" && len(td.Tags) > 0 {
			taskTags = append(taskTags, td.Tags...)
		}
		taskTags = append(taskTags, in.Tags...)

		taskVolumeHosts, ebsAttachments, pendingRestores, ebsErr := ecsPrepareManagedEBSVolumes(ctx, td, in.VolumeConfigurations, taskID, requestedSubnet)
		if ebsErr != nil {
			return nil, ebsErr
		}

		task := ECSTask{
			TaskArn:              taskArn,
			TaskDefinitionArn:    td.TaskDefinitionArn,
			ClusterArn:           cluster.ClusterArn,
			LastStatus:           ECSTaskStatusProvisioning,
			DesiredStatus:        ECSTaskStatusRunning,
			Containers:           containers,
			CreatedAt:            &createdAt,
			Tags:                 taskTags,
			LaunchType:           in.LaunchType,
			Cpu:                  ecsTaskCPU(td, in.Overrides),
			Memory:               ecsTaskMemory(td, in.Overrides),
			Group:                in.Group,
			Overrides:            in.Overrides,
			EnableExecuteCommand: in.EnableExecuteCommand,
			StartedBy:            in.StartedBy,
			ContainerInstanceArn: in.ContainerInstanceArn,
			VolumeHosts:          taskVolumeHosts,
		}
		if networkMode == ecsNetworkModeAwsvpc {
			task.Attachments = append(task.Attachments, ECSAttachment{
				Id:     eniID,
				Type:   "ElasticNetworkInterface",
				Status: "ATTACHING",
				Details: []ECSKeyValuePair{
					{Name: "subnetId", Value: subnetID},
					{Name: "privateIPv4Address", Value: privateIP},
				},
			})
		}
		task.Attachments = append(task.Attachments, ebsAttachments...)

		// Store VPC network configuration from request
		if in.NetworkConfiguration != nil && in.NetworkConfiguration.AwsvpcConfiguration != nil {
			vpc := in.NetworkConfiguration.AwsvpcConfiguration
			task.NetworkConfiguration = &ECSTaskNetworkConfig{
				AwsvpcConfiguration: &ECSTaskVpcConfig{
					Subnets:        vpc.Subnets,
					SecurityGroups: vpc.SecurityGroups,
					AssignPublicIp: vpc.AssignPublicIp,
				},
			}
		}

		ecsTasks.Put(taskID, task)
		if in.ContainerInstanceKey != "" {
			ecsContainerInstances.Update(in.ContainerInstanceKey, func(instance *ECSContainerInstance) {
				instance.PendingTasksCount++
			})
		}
		tasks = append(tasks, task)

		// Perform the asynchronous PROVISIONING → PENDING → RUNNING lifecycle.
		// RUNNING is not reported until the task's containers are actually
		// started and their Docker handles are stored. This matches real ECS
		// semantics and eliminates a class of races where callers (e.g.
		// ExecuteCommand, task metadata) see RUNNING before the container exists.
		// The goroutine reads durable stores (task, ELB, CloudWatch) while it
		// provisions real containers, so it runs under the server lifecycle and
		// is drained before SQLite closes (the BUG-2827 class).
		start := func(id string, td ECSTaskDefinition, taskTags []ECSTag, overrides *ECSTaskOverride, taskVolumeHosts map[string]string, launchType, containerInstanceKey string) {
			lifecycleLock := ecsTaskLifecycleLock(id)
			lifecycleLock.Lock()
			defer lifecycleLock.Unlock()
			current, exists := ecsTasks.Get(id)
			if !exists || current.DesiredStatus == ECSTaskStatusStopped {
				return
			}

			// PROVISIONING → PENDING
			ecsTasks.Update(id, func(t *ECSTask) {
				t.LastStatus = ECSTaskStatusPending
				for j := range t.Containers {
					t.Containers[j].LastStatus = "PENDING"
				}
			})

			// Hydrate any snapshot-backed managed-EBS volume before starting the
			// containers that mount it. A failure here is a resource problem, not
			// an exited container, so it stops the task the way real ECS does —
			// with TaskFailedToStart and a ResourceInitializationError reason the
			// caller can read from DescribeTasks.
			if len(pendingRestores) > 0 {
				if err := ecsRunPendingEBSRestores(context.Background(), id, pendingRestores); err != nil {
					fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: managed EBS restore failed: %v\n", id, err)
					stoppedAt := time.Now().Unix()
					ecsTasks.Update(id, func(t *ECSTask) {
						t.LastStatus = ECSTaskStatusStopped
						t.DesiredStatus = ECSTaskStatusStopped
						t.StoppedAt = &stoppedAt
						t.StopCode = "TaskFailedToStart"
						t.StoppedReason = fmt.Sprintf("ResourceInitializationError: unable to restore managed EBS volume: %v", err)
						for j := range t.Containers {
							t.Containers[j].LastStatus = "STOPPED"
						}
						ecsCleanupTaskManagedEBS(t)
					})
					ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, -1, 0)
					if task, ok := ecsTasks.Get(id); ok {
						ecsRequestServiceReconcileForTask(task)
					}
					return
				}
			}

			// Prepare CloudWatch logs before the container starts, so the log
			// group/stream are observable as soon as the task reports RUNNING.
			sink := ecsTaskCloudWatchSink(td, id)

			// Start containers. This is the real work that RUNNING must wait for.
			processes, err := startECSTaskContainers(id, td, taskTags, overrides, taskVolumeHosts, sink, launchType)
			if err != nil {
				// Surface the start failure: it's otherwise only recorded in
				// the task's StoppedReason, so an intermittent awsvpc netns /
				// pause-container / image-pull failure reads as an opaque
				// container ExitCode -1 with no diagnosable cause in logs.
				fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: container start failed: %v\n", id, err)
				stoppedAt := time.Now().Unix()
				ecsTasks.Update(id, func(t *ECSTask) {
					t.LastStatus = ECSTaskStatusStopped
					t.DesiredStatus = ECSTaskStatusStopped
					t.StoppedAt = &stoppedAt
					t.StopCode = "EssentialContainerExited"
					t.StoppedReason = fmt.Sprintf("Container start failed: %v", err)
					exitCode := -1
					for j := range t.Containers {
						t.Containers[j].LastStatus = "STOPPED"
						t.Containers[j].ExitCode = &exitCode
					}
					ecsCleanupTaskManagedEBS(t)
				})
				ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, -1, 0)
				if task, ok := ecsTasks.Get(id); ok {
					ecsRequestServiceReconcileForTask(task)
				}
				return
			}

			// Containers are actually running. Report RUNNING and wire up
			// lifecycle waits. Store handles before marking RUNNING so any
			// concurrent observer that sees RUNNING also sees handles.
			now := time.Now().Unix()
			ecsTasks.Update(id, func(t *ECSTask) {
				t.LastStatus = ECSTaskStatusRunning
				t.Connectivity = "CONNECTED"
				t.StartedAt = &now
				for j := range t.Containers {
					t.Containers[j].LastStatus = "RUNNING"
				}
				for j := range t.Attachments {
					t.Attachments[j].Status = "ATTACHED"
				}
			})
			ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, -1, 1)
			if processes != nil {
				ecsProcessHandles.Store(id, processes)
				ecsWatchTaskProcesses(id, containerInstanceKey, processes)
			}
			if task, ok := ecsTasks.Get(id); ok {
				ecsRequestServiceReconcileForTask(task)
			}
		}
		ecsScheduleTaskStart(start, taskID, td, taskTags, in.Overrides, taskVolumeHosts, in.LaunchType, in.ContainerInstanceKey)
	}

	return tasks, nil
}

// ecsScheduleTaskStart runs one task's PROVISIONING→RUNNING lifecycle under the
// server's background-worker lifecycle when the ECS service has registered one,
// so orderly shutdown drains it before SQLite is closed. The unit-test paths
// that exercise runECSTasks without a registered server fall back to a plain
// goroutine, matching the prior behaviour.
func ecsScheduleTaskStart(
	start func(string, ECSTaskDefinition, []ECSTag, *ECSTaskOverride, map[string]string, string, string),
	id string, td ECSTaskDefinition, taskTags []ECSTag, overrides *ECSTaskOverride,
	taskVolumeHosts map[string]string, launchType, containerInstanceKey string,
) {
	if ecsBackgroundServer == nil {
		go start(id, td, taskTags, overrides, taskVolumeHosts, launchType, containerInstanceKey)
		return
	}
	ecsBackgroundServer.StartBackground(func(context.Context) {
		// Counted in the test drain barrier as well as the server's: the
		// lifecycle this runs reads the control-plane stores, and a test that
		// has awaited quiescence must not replace them underneath it.
		simTracked(func() {
			start(id, td, taskTags, overrides, taskVolumeHosts, launchType, containerInstanceKey)
		})
	})
}

func ecsWatchTaskProcesses(taskID, containerInstanceKey string, processes *ecsTaskProcesses) {
	for _, handle := range processes.Handles {
		go func(taskID string, handle *sim.ContainerHandle) {
			result := handle.Wait()
			lifecycleLock := ecsTaskLifecycleLock(taskID)
			lifecycleLock.Lock()
			defer lifecycleLock.Unlock()
			owned, ownsLifecycle := ecsProcessHandles.LoadAndDelete(taskID)
			if !ownsLifecycle {
				return
			}
			ownedProcesses, ok := owned.(*ecsTaskProcesses)
			if !ok {
				panic("Amazon ECS task process registry contained a non-process value")
			}
			cleanupECSTaskProcesses(taskID, ownedProcesses)
			stoppedAt := time.Now().Unix()
			transitioned := false
			ecsTasks.Update(taskID, func(t *ECSTask) {
				if t.LastStatus == ECSTaskStatusStopped {
					return
				}
				transitioned = true
				t.LastStatus = ECSTaskStatusStopped
				t.DesiredStatus = ECSTaskStatusStopped
				t.StoppedAt = &stoppedAt
				t.StopCode = "EssentialContainerExited"
				t.StoppedReason = "Essential container in task exited"
				exitCode := result.ExitCode
				for j := range t.Containers {
					t.Containers[j].LastStatus = "STOPPED"
					t.Containers[j].ExitCode = &exitCode
				}
				ecsCleanupTaskManagedEBS(t)
			})
			if transitioned {
				ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, 0, -1)
				if task, ok := ecsTasks.Get(taskID); ok {
					ecsRequestServiceReconcileForTask(task)
				}
			}
		}(taskID, handle)
	}
}

func recoverECSTasks() error {
	return recoverECSTasksWithContainerFinder(sim.FindExistingContainers)
}

func recoverECSTasksWithContainerFinder(
	findExistingContainers func(map[string]string) ([]sim.ExistingContainer, error),
) error {
	for _, task := range ecsTasks.List() {
		switch task.LastStatus {
		case ECSTaskStatusProvisioning, ECSTaskStatusPending:
			definition, ok := ecsTaskDefinitionForARN(task.TaskDefinitionArn)
			if !ok {
				return fmt.Errorf("task %s references missing task definition %s", task.TaskArn, task.TaskDefinitionArn)
			}
			go ecsResumePendingTask(task, definition)
		case ECSTaskStatusRunning:
			definition, ok := ecsTaskDefinitionForARN(task.TaskDefinitionArn)
			if !ok {
				return fmt.Errorf("task %s references missing task definition %s", task.TaskArn, task.TaskDefinitionArn)
			}
			if err := ecsRecoverRunningTask(task, definition, findExistingContainers); err != nil {
				return err
			}
		}
	}
	return nil
}

func ecsTaskDefinitionForARN(arn string) (ECSTaskDefinition, bool) {
	key := arn
	if separator := strings.LastIndexByte(key, '/'); separator >= 0 {
		key = key[separator+1:]
	}
	return ecsTaskDefinitions.Get(key)
}

func ecsResumePendingTask(task ECSTask, definition ECSTaskDefinition) {
	taskID := task.TaskID()
	ecsTasks.Update(taskID, func(current *ECSTask) {
		current.LastStatus = ECSTaskStatusPending
		for index := range current.Containers {
			current.Containers[index].LastStatus = "PENDING"
		}
	})
	sink := ecsTaskCloudWatchSink(definition, taskID)
	processes, err := startECSTaskContainers(
		taskID,
		definition,
		task.Tags,
		task.Overrides,
		task.VolumeHosts,
		sink,
		task.LaunchType,
	)
	containerInstanceKey := ecsContainerInstanceKeyFromARN(task.ContainerInstanceArn)
	if err != nil {
		stoppedAt := time.Now().Unix()
		ecsTasks.Update(taskID, func(current *ECSTask) {
			current.LastStatus = ECSTaskStatusStopped
			current.DesiredStatus = ECSTaskStatusStopped
			current.StoppedAt = &stoppedAt
			current.StopCode = "EssentialContainerExited"
			current.StoppedReason = fmt.Sprintf("Container start failed after control-plane restart: %v", err)
			exitCode := -1
			for index := range current.Containers {
				current.Containers[index].LastStatus = "STOPPED"
				current.Containers[index].ExitCode = &exitCode
			}
			ecsCleanupTaskManagedEBS(current)
		})
		ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, -1, 0)
		if current, ok := ecsTasks.Get(taskID); ok {
			ecsRequestServiceReconcileForTask(current)
		}
		return
	}
	startedAt := time.Now().Unix()
	ecsTasks.Update(taskID, func(current *ECSTask) {
		current.LastStatus = ECSTaskStatusRunning
		current.Connectivity = "CONNECTED"
		current.StartedAt = &startedAt
		for index := range current.Containers {
			current.Containers[index].LastStatus = "RUNNING"
		}
		for index := range current.Attachments {
			current.Attachments[index].Status = "ATTACHED"
		}
	})
	ecsUpdateContainerInstanceTaskCounts(containerInstanceKey, -1, 1)
	if processes != nil {
		ecsProcessHandles.Store(taskID, processes)
		ecsWatchTaskProcesses(taskID, containerInstanceKey, processes)
	}
	if current, ok := ecsTasks.Get(taskID); ok {
		ecsRequestServiceReconcileForTask(current)
	}
}

func ecsRecoverRunningTask(
	task ECSTask,
	definition ECSTaskDefinition,
	findExistingContainers func(map[string]string) ([]sim.ExistingContainer, error),
) error {
	if len(definition.ContainerDefinitions) == 0 {
		return fmt.Errorf("task %s references an Amazon ECS task definition without containers", task.TaskArn)
	}
	taskID := task.TaskID()
	existing, err := findExistingContainers(map[string]string{"sockerless-sim-task": taskID})
	if err != nil {
		return fmt.Errorf("find Amazon ECS task %s containers: %w", task.TaskArn, err)
	}
	if len(existing) == 0 {
		ecsMarkMissingRunningTaskStopped(task)
		return nil
	}
	return ecsAdoptRunningTask(task, definition, existing)
}

func ecsMarkMissingRunningTaskStopped(task ECSTask) {
	taskID := task.TaskID()
	stoppedAt := time.Now().Unix()
	transitioned := false
	ecsTasks.Update(taskID, func(current *ECSTask) {
		if current.LastStatus != ECSTaskStatusRunning {
			return
		}
		transitioned = true
		current.LastStatus = ECSTaskStatusStopped
		current.DesiredStatus = ECSTaskStatusStopped
		current.Connectivity = ""
		current.StoppedAt = &stoppedAt
		current.StopCode = "EssentialContainerExited"
		current.StoppedReason = "Workload containers not found after control-plane restart"
		exitCode := -1
		for index := range current.Containers {
			current.Containers[index].LastStatus = "STOPPED"
			current.Containers[index].ExitCode = &exitCode
		}
		ecsCleanupTaskManagedEBS(current)
	})
	if !transitioned {
		return
	}
	fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: workload containers not found after control-plane restart; transitioned task to STOPPED\n", taskID)
	ecsUpdateContainerInstanceTaskCounts(
		ecsContainerInstanceKeyFromARN(task.ContainerInstanceArn),
		0,
		-1,
	)
	go ec2DetachRealECSTaskNIC(context.Background(), taskID)
	ecsRequestServiceReconcileForTask(task)
}

func ecsAdoptRunningTask(
	task ECSTask,
	definition ECSTaskDefinition,
	existing []sim.ExistingContainer,
) error {
	taskID := task.TaskID()
	processes := &ecsTaskProcesses{
		MainContainerName: definition.ContainerDefinitions[0].Name,
		Handles:           make(map[string]*sim.ContainerHandle, len(existing)),
	}
	sink := ecsTaskCloudWatchSink(definition, taskID)
	for _, container := range existing {
		name := container.Labels["sockerless-sim-task-container"]
		if container.Labels["sockerless-sim-task-pause"] == "true" {
			name = "__pause__"
		}
		if name == "" {
			return fmt.Errorf("task %s Amazon ECS container %s has no container identity label", task.TaskArn, container.ID)
		}
		handle, err := sim.AdoptContainer(container.ID, sim.ContainerConfig{}, sink)
		if err != nil {
			return fmt.Errorf("adopt Amazon ECS task %s container %s: %w", task.TaskArn, name, err)
		}
		processes.Handles[name] = handle
	}
	ecsProcessHandles.Store(taskID, processes)
	ecsWatchTaskProcesses(taskID, ecsContainerInstanceKeyFromARN(task.ContainerInstanceArn), processes)
	return nil
}

func ecsTaskCPU(td ECSTaskDefinition, overrides *ECSTaskOverride) string {
	if overrides != nil && overrides.Cpu != "" {
		return overrides.Cpu
	}
	return td.Cpu
}

func ecsTaskMemory(td ECSTaskDefinition, overrides *ECSTaskOverride) string {
	if overrides != nil && overrides.Memory != "" {
		return overrides.Memory
	}
	return td.Memory
}

// ecsPauseImage is the image for the netns pause container — a long-lived sleep
// that owns the task's VPC network namespace. Defaults to busybox from ECR
// (always has `sleep`, avoids Docker Hub throttling); override with the env var.
func ecsPauseImage() string {
	if v := os.Getenv("SOCKERLESS_ECS_PAUSE_IMAGE"); v != "" {
		return v
	}
	return "public.ecr.aws/docker/library/busybox:latest"
}

// startECSPauseContainer launches the netns pause container (Fargate-style): a
// long-lived process that holds the task's network namespace so the ENI can be
// plumbed in with no start-race, then shared by every task container.
// The pause container carries the task's resolver because it owns the network
// namespace. Docker refuses --dns on a container that joins another's namespace
// — "conflicting options: dns and the network mode" — and rightly so: the
// resolver is a property of the namespace, and every task container inherits
// this one along with the interface.
func startECSPauseContainer(taskID string, td ECSTaskDefinition, dns []string, sink sim.LogSink) (*sim.ContainerHandle, error) {
	img := sim.ResolveLocalImage(ecsPauseImage())
	platform, err := localImagePlatform(context.Background(), img)
	if err != nil {
		return nil, fmt.Errorf("resolve pause image platform: %w", err)
	}
	return sim.StartContainerSync(sim.ContainerConfig{
		Image:        img,
		Architecture: platform,
		Command:      []string{"sleep"},
		Args:         []string{"2147483647"},
		Name:         fmt.Sprintf("sockerless-sim-aws-task-%s-pause", taskID[:12]),
		Labels: map[string]string{
			"sockerless-sim-task":       taskID,
			"sockerless-sim-task-pause": "true",
		},
		Sandbox: SandboxFargate,
		DNS:     dns,
	}, sink)
}

// ecsContainerResourceLimits translates the advertised ECS sizing into Docker
// cgroup limits so the launched container is actually bounded the way the task
// metadata reports (a Fargate task advertising 512 CPU / 1024 MiB should see a
// matching cpu.max / memory.max, not the host's full capacity). Container-level
// cpu/memory refine the task-level size (matching ECS, where per-container
// limits sit under the task size); the task size is the fallback. CPU is in ECS
// units (1024 == 1 vCPU); memory is in MiB.
func ecsContainerResourceLimits(td ECSTaskDefinition, cd ECSContainerDefinition) (memBytes, nanoCPU int64) {
	memMiB := cd.Memory
	if memMiB == 0 {
		if m, err := strconv.Atoi(td.Memory); err == nil {
			memMiB = m
		}
	}
	if memMiB > 0 {
		memBytes = int64(memMiB) * 1024 * 1024
	}

	cpuUnits := cd.Cpu
	if cpuUnits == 0 {
		if c, err := strconv.Atoi(td.Cpu); err == nil {
			cpuUnits = c
		}
	}
	if cpuUnits > 0 {
		nanoCPU = int64(cpuUnits) * 1_000_000_000 / 1024
	}
	return memBytes, nanoCPU
}

// ecsTaskSandbox returns the isolation profile a task's containers run under.
// The AWS Fargate profile denies host networking because Fargate never offers
// it — Fargate requires the awsvpc network mode. A task definition that asks
// for the host network mode is by definition running on the EC2 or EXTERNAL
// launch type, where Amazon ECS does give the task the container instance's own
// network stack, so the denial does not apply to it.
func ecsTaskSandbox(launchType string, privileged bool) sim.SandboxProfile {
	if strings.EqualFold(launchType, "FARGATE") {
		return SandboxFargate
	}
	// Amazon ECS on EC2 and EXTERNAL hosts exposes the container instance's
	// Docker capabilities. The task definition, not Fargate, decides whether a
	// workload is privileged; host networking and host bind mounts remain
	// available exactly as on the registered container instance. An omitted
	// launch type follows the cluster's EC2 capacity when no capacity-provider
	// strategy was supplied.
	return sim.SandboxProfile{Privileged: privileged}
}

func startECSTaskContainers(taskID string, td ECSTaskDefinition, taskTags []ECSTag, overrides *ECSTaskOverride, taskVolumeHosts map[string]string, sink sim.LogSink, launchType string) (*ecsTaskProcesses, error) {
	if len(td.ContainerDefinitions) == 0 {
		return nil, nil
	}

	wantTTY := false
	for _, tag := range taskTags {
		if tag.Key == "sockerless-tty" && tag.Value == "true" {
			wantTTY = true
			break
		}
	}

	volMap := make(map[string]string)
	for _, v := range td.Volumes {
		if host, ok := taskVolumeHosts[v.Name]; ok {
			volMap[v.Name] = host
			continue
		}
		if v.EfsVolumeConfiguration != nil {
			cfg := v.EfsVolumeConfiguration
			var host string
			if cfg.AuthorizationConfig != nil && cfg.AuthorizationConfig.AccessPointId != "" {
				host = EFSAccessPointHostDir(cfg.AuthorizationConfig.AccessPointId)
			}
			if host == "" && cfg.FileSystemId != "" {
				host = EFSFileSystemHostDir(cfg.FileSystemId)
				if cfg.RootDirectory != "" && cfg.RootDirectory != "/" {
					host = fmt.Sprintf("%s/%s", host, strings.TrimPrefix(cfg.RootDirectory, "/"))
				}
			}
			if host != "" {
				volMap[v.Name] = host
				continue
			}
		}
		if v.Host != nil && v.Host.SourcePath != "" {
			volMap[v.Name] = v.Host.SourcePath
			continue
		}
		volMap[v.Name] = v.Name
	}

	processes := &ecsTaskProcesses{
		MainContainerName: td.ContainerDefinitions[0].Name,
		Handles:           make(map[string]*sim.ContainerHandle, len(td.ContainerDefinitions)),
	}
	// The task definition's networkMode decides the fabric every container in
	// the task lands on: awsvpc gets the task its own elastic network interface
	// in the VPC, host shares the container instance's network stack, none has
	// no connectivity, and bridge uses the container instance's default Docker
	// bridge.
	//
	// awsvpc netns tier (Linux + CAP_NET_ADMIN): a pause container holds the
	// task's VPC network namespace (a long-lived sleep, so the ENI is plumbed
	// with no start-race), the ENI veth is attached into it, and every task
	// container shares that netns — overlapping VPC CIDRs work natively with the
	// real ENI IP. Otherwise the cross-platform Docker-network tier lands the
	// first container on the VPC's bridge — whose subnet is allocator-assigned,
	// never the VPC CIDR, so same-CIDR VPCs coexist — and plumbs the ENI IP as
	// a secondary address on its interface.
	networkMode := ecsEffectiveNetworkMode(td)
	eniIP, subnetID, hasENI := ecsTaskENIInfo(taskID)
	if networkMode == ecsNetworkModeAwsvpc && !hasENI {
		return nil, fmt.Errorf("awsvpc task %s has no elastic network interface attachment", taskID)
	}
	netnsTier := networkMode == ecsNetworkModeAwsvpc && ec2ECSRealNetAvailable()
	// A task in its own namespace cannot reach the resolver Docker would
	// configure — that one lives on the Docker networks the namespace is
	// detached from — so it asks the VPC's, which the namespace redirects to
	// the simulator's own. Without this the redirect is in place and nothing
	// uses it: every lookup still goes to an address that answers nothing and
	// blocks until it times out.
	var taskDNS []string
	if netnsTier {
		taskDNS = []string{realexec.VPCResolverIPv4}
	}
	var sharedNetMode string
	if netnsTier {
		pause, perr := startECSPauseContainer(taskID, td, taskDNS, sink)
		if perr != nil {
			return nil, perr
		}
		processes.Handles["__pause__"] = pause
		if derr := sim.DisconnectContainerNetworks(pause.ContainerID); derr != nil {
			cleanupECSTaskProcesses(taskID, processes)
			return nil, fmt.Errorf("disconnect task netns pause from Docker networks: %w", derr)
		}
		pid, perr := sim.ContainerPID(pause.ContainerID)
		if perr != nil {
			cleanupECSTaskProcesses(taskID, processes)
			return nil, fmt.Errorf("task netns pause pid: %w", perr)
		}
		if aerr := ec2AttachRealECSTaskNIC(context.Background(), taskID, subnetID, pid, eniIP, ecsTaskSecurityGroupIDs(taskID)); aerr != nil {
			cleanupECSTaskProcesses(taskID, processes)
			return nil, fmt.Errorf("attach task to VPC netns: %w", aerr)
		}
		sharedNetMode = "container:" + pause.ContainerID
	}
	metadataEnv, err := hostMetadataEnv(taskID)
	if err != nil {
		cleanupECSTaskProcesses(taskID, processes)
		return nil, fmt.Errorf("resolve Amazon ECS metadata callback: %w", err)
	}
	if netnsTier {
		metadataEnv = hostMetadataLinkLocalEnv(taskID)
	}
	var mainDockerID string

	for i, cd := range td.ContainerDefinitions {
		if cd.Image == "" {
			continue
		}
		containerOverride := ecsContainerOverrideFor(overrides, cd.Name)
		cmdEnv := make(map[string]string, len(cd.Environment))
		for _, ev := range cd.Environment {
			cmdEnv[ev.Name] = ev.Value
		}
		// Resolve the container definition's `secrets` (valueFrom →
		// SecretsManager/SSM) at launch and inject them as env vars, exactly as
		// real ECS does — indistinguishable from `environment` to the container.
		for name, val := range resolveECSContainerSecrets(cd.Secrets) {
			cmdEnv[name] = val
		}
		for _, ev := range containerOverride.Environment {
			cmdEnv[ev.Name] = ev.Value
		}
		if netnsTier {
			simulatorPort, portErr := simHostMetadataPort()
			if portErr != nil {
				cleanupECSTaskProcesses(taskID, processes)
				return nil, fmt.Errorf("resolve simulator endpoint for task VPC: %w", portErr)
			}
			cmdEnv = rewriteSimulatorEndpointForRealVPC(cmdEnv, simulatorPort)
		}
		var binds []string
		for _, mp := range cd.MountPoints {
			if src, ok := volMap[mp.SourceVolume]; ok {
				// `z` = SELinux shared relabel: on an SELinux-enforcing host
				// (e.g. a local podman machine) the sim-spawned task container
				// runs confined as `container_t`, which cannot write to the EFS
				// host dir's default label even at mode 0777 — relabeling the
				// bind to a shared container label lets the workload access it.
				// Ignored on hosts without SELinux (Docker on CI), so safe
				// everywhere.
				opts := "z"
				if mp.ReadOnly {
					opts = "ro,z"
				}
				binds = append(binds, src+":"+mp.ContainerPath+":"+opts)
			}
		}

		containerName := fmt.Sprintf("sockerless-sim-aws-task-%s", taskID[:12])
		if i > 0 {
			containerName = fmt.Sprintf("%s-%s", containerName, cd.Name)
		}
		localImage := sim.ResolveLocalImage(cd.Image)
		platform, err := localImagePlatform(context.Background(), localImage)
		if err != nil {
			cleanupECSTaskProcesses(taskID, processes)
			return nil, fmt.Errorf("resolve task container %q image platform: %w", cd.Name, err)
		}

		command := cd.Command
		if len(containerOverride.Command) > 0 {
			command = containerOverride.Command
		}
		cfg := sim.ContainerConfig{
			Image:        localImage,
			Architecture: platform,
			Command:      cd.EntryPoint,
			Args:         command,
			Env:          mergeEnv(cmdEnv, metadataEnv),
			Name:         containerName,
			Labels: map[string]string{
				"sockerless-sim-task":           taskID,
				"sockerless-sim-task-container": cd.Name,
			},
			Tty:       wantTTY || cd.PseudoTerminal,
			OpenStdin: wantTTY || cd.Interactive,
			Binds:     binds,
			Sandbox:   ecsTaskSandbox(launchType, cd.Privileged),
		}
		// Docker Desktop does not route a Linux VM's user-defined bridge
		// addresses back to the host process. Publish the task definition's
		// declared listener ports on random loopback ports in that transport
		// tier so host-resident Elastic Load Balancing can reach the same real
		// container listener. The public Amazon ECS/ENI coordinate remains the
		// task IP; the mapping is rediscovered from Docker on restart.
		if networkMode == ecsNetworkModeAwsvpc && !netnsTier {
			cfg.PublishPorts = make(map[int]int)
			for _, mapping := range cd.PortMappings {
				if mapping.ContainerPort > 0 {
					cfg.PublishPorts[mapping.ContainerPort] = 0
				}
			}
		}
		cfg.MemoryBytes, cfg.NanoCPU = ecsContainerResourceLimits(td, cd)
		switch {
		case sharedNetMode != "":
			// netns tier: share the pause container's ENI netns.
			cfg.NetworkMode = sharedNetMode
		case networkMode == ecsNetworkModeHost:
			// host mode: the containers use the container instance's network
			// stack directly, so they share its interfaces and its loopback.
			cfg.NetworkMode = "host"
			cfg.ExtraHosts = append(hostMetadataExtraHosts(), elbv2WorkloadExtraHosts()...)
		case networkMode == ecsNetworkModeNone:
			// none mode: the containers have no external connectivity.
			cfg.NetworkMode = "none"
		case networkMode == ecsNetworkModeAwsvpc && i > 0:
			// Every container in an awsvpc task shares the task ENI.
			cfg.NetworkMode = "container:" + mainDockerID
		case networkMode == ecsNetworkModeAwsvpc:
			cfg.ExtraHosts = append(hostMetadataExtraHosts(), elbv2WorkloadExtraHosts()...)
			netName, eniAddress, nerr := ecsTaskVPCNetwork(taskID)
			if nerr != nil {
				cleanupECSTaskProcesses(taskID, processes)
				return nil, nerr
			}
			cfg.Network = netName
			cfg.ENIAddress = eniAddress
		default:
			// bridge mode: each container gets its own address on the
			// container instance's default Docker bridge.
			cfg.ExtraHosts = append(hostMetadataExtraHosts(), elbv2WorkloadExtraHosts()...)
		}

		handle, err := sim.StartContainerSync(cfg, sink)
		if err != nil {
			cleanupECSTaskProcesses(taskID, processes)
			return nil, fmt.Errorf("start task container %q: %w", cd.Name, err)
		}
		if i == 0 {
			mainDockerID = handle.ContainerID
		}
		processes.Handles[cd.Name] = handle
	}

	if len(processes.Handles) == 0 {
		return nil, nil
	}
	return processes, nil
}

func ecsContainerOverrideFor(overrides *ECSTaskOverride, containerName string) ECSContainerOverride {
	if overrides == nil {
		return ECSContainerOverride{}
	}
	for _, override := range overrides.ContainerOverrides {
		if override.Name == containerName {
			return override
		}
	}
	return ECSContainerOverride{}
}

// ecsVPCNetworkName is the Docker network backing a VPC.
func ecsVPCNetworkName(vpcID string) string { return "sockerless-sim-vpc-" + vpcID }

// ecsTaskSecurityGroupIDs returns the awsvpc security groups attached to the
// task — the SGs whose ingress/egress rules the real-exec netns tier enforces
// at the packet layer on Linux + CAP_NET_ADMIN hosts.
func ecsTaskSecurityGroupIDs(taskID string) []string {
	task, ok := ecsTasks.Get(taskID)
	if !ok || task.NetworkConfiguration == nil || task.NetworkConfiguration.AwsvpcConfiguration == nil {
		return nil
	}
	return task.NetworkConfiguration.AwsvpcConfiguration.SecurityGroups
}

// ecsTaskENIInfo reads a task's awsvpc ENI IP + subnet from its attachment.
// Returns ok=false for tasks without an awsvpc ENI.
func ecsTaskENIInfo(taskID string) (eniIP, subnetID string, ok bool) {
	task, found := ecsTasks.Get(taskID)
	if !found {
		return "", "", false
	}
	for _, att := range task.Attachments {
		if att.Type != "ElasticNetworkInterface" {
			continue
		}
		for _, d := range att.Details {
			switch d.Name {
			case "subnetId":
				subnetID = d.Value
			case "privateIPv4Address":
				eniIP = d.Value
			}
		}
	}
	return eniIP, subnetID, eniIP != "" && subnetID != ""
}

func ecsTaskDefinitionFamilyRevision(taskDefinitionArn string) (family, revision string) {
	ref := taskDefinitionArn
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	family = ref
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		family = ref[:i]
		revision = ref[i+1:]
	}
	return family, revision
}

func ecsTaskDefinitionByArn(taskDefinitionArn string) (ECSTaskDefinition, bool) {
	key := taskDefinitionArn
	if i := strings.LastIndex(key, "/"); i >= 0 {
		key = key[i+1:]
	}
	return ecsTaskDefinitions.Get(key)
}

func ecsTaskDockerInfo(taskID, containerName string) (dockerID, dockerName string) {
	if v, ok := ecsProcessHandles.Load(taskID); ok {
		if procs, ok := v.(*ecsTaskProcesses); ok {
			if h := procs.handleFor(containerName); h != nil {
				dockerID = h.ContainerID
			}
		}
	}
	if dockerID == "" {
		return "", ""
	}
	dockerName = dockerID
	return dockerID, dockerName
}

func ecsTaskContainerMetadata(taskID string, task ECSTask, td ECSTaskDefinition, cd ECSContainerDefinition, eniIP string) (map[string]any, bool) {
	dockerID, dockerName := ecsTaskDockerInfo(taskID, cd.Name)
	if dockerID == "" {
		return nil, false
	}
	out := map[string]any{
		"DockerId":   dockerID,
		"DockerName": dockerName,
		"Name":       cd.Name,
		"Image":      cd.Image,
		"KnownStatus": func() string {
			for _, c := range task.Containers {
				if c.Name == cd.Name {
					return c.LastStatus
				}
			}
			return string(task.LastStatus)
		}(),
	}
	if td.Cpu != "" || td.Memory != "" {
		limits := map[string]string{}
		if td.Cpu != "" {
			limits["CPU"] = td.Cpu
		}
		if td.Memory != "" {
			limits["Memory"] = td.Memory
		}
		out["Limits"] = limits
	}
	if eniIP != "" {
		out["Networks"] = []map[string]any{{
			"NetworkMode":   "awsvpc",
			"IPv4Addresses": []string{eniIP},
		}}
	}
	return out, true
}

func ecsTaskMetadataV4(taskID string) (map[string]any, bool) {
	task, ok := ecsTasks.Get(taskID)
	if !ok {
		return nil, false
	}
	td, _ := ecsTaskDefinitionByArn(task.TaskDefinitionArn)
	eniIP, _, _ := ecsTaskENIInfo(taskID)
	family, revision := ecsTaskDefinitionFamilyRevision(task.TaskDefinitionArn)
	var containers []map[string]any
	for _, cd := range td.ContainerDefinitions {
		container, ok := ecsTaskContainerMetadata(taskID, task, td, cd, eniIP)
		if !ok {
			return nil, false
		}
		containers = append(containers, container)
	}
	return map[string]any{
		"Cluster":       task.ClusterArn,
		"TaskARN":       task.TaskArn,
		"Family":        family,
		"Revision":      revision,
		"DesiredStatus": task.DesiredStatus,
		"KnownStatus":   task.LastStatus,
		"Containers":    containers,
		"LaunchType":    task.LaunchType,
	}, true
}

func ecsContainerMetadataV4(taskID string) (map[string]any, bool) {
	task, ok := ecsTasks.Get(taskID)
	if !ok {
		return nil, false
	}
	td, ok := ecsTaskDefinitionByArn(task.TaskDefinitionArn)
	if !ok || len(td.ContainerDefinitions) == 0 {
		return nil, false
	}
	eniIP, _, _ := ecsTaskENIInfo(taskID)
	return ecsTaskContainerMetadata(taskID, task, td, td.ContainerDefinitions[0], eniIP)
}

// ecsTaskVPCNetwork resolves the VPC Docker network + ENI address for an
// awsvpc task, ensuring the network exists. The ENI address is returned in
// CIDR notation with the VPC CIDR's prefix length, ready to be plumbed as the
// container's secondary address (ContainerConfig.ENIAddress). Every failure
// is returned so an awsvpc launch fails loudly rather than silently landing
// the task on the container instance's default bridge with an address that is
// not its ENI's.
func ecsTaskVPCNetwork(taskID string) (networkName, eniAddress string, err error) {
	task, found := ecsTasks.Get(taskID)
	if !found {
		return "", "", fmt.Errorf("task %s not found", taskID)
	}
	var subnetID, eniIP string
	for _, att := range task.Attachments {
		if att.Type != "ElasticNetworkInterface" {
			continue
		}
		for _, d := range att.Details {
			switch d.Name {
			case "subnetId":
				subnetID = d.Value
			case "privateIPv4Address":
				eniIP = d.Value
			}
		}
	}
	if subnetID == "" || eniIP == "" {
		return "", "", fmt.Errorf("awsvpc task %s has no elastic network interface attachment", taskID)
	}
	subnet, ok := ec2Subnets.Get(subnetID)
	if !ok {
		return "", "", fmt.Errorf("subnet %s for task %s not found", subnetID, taskID)
	}
	vpc, ok := ec2Vpcs.Get(subnet.VpcId)
	if !ok {
		return "", "", fmt.Errorf("vpc %s for subnet %s not found", subnet.VpcId, subnetID)
	}
	if vpc.CidrBlock == "" {
		return "", "", fmt.Errorf("vpc %s has no CIDR block", subnet.VpcId)
	}
	vpcPrefix, perr := netip.ParsePrefix(vpc.CidrBlock)
	if perr != nil {
		return "", "", fmt.Errorf("vpc %s CIDR %q: %w", subnet.VpcId, vpc.CidrBlock, perr)
	}
	name := ecsVPCNetworkName(subnet.VpcId)
	if _, nerr := sim.EnsureVPCNetwork(name); nerr != nil {
		return "", "", fmt.Errorf("provision VPC network for %s: %w", subnet.VpcId, nerr)
	}
	return name, fmt.Sprintf("%s/%d", eniIP, vpcPrefix.Bits()), nil
}

// ecsRequireCluster resolves a cluster ref (name or ARN; "" → "default") and,
// if it doesn't exist, writes a ClusterNotFoundException and returns false. Real
// ECS rejects every cluster-scoped operation against an unknown cluster, so a
// deleted/unknown cluster is distinguishable from an empty cluster or a missing
// task.
func ecsRequireCluster(w http.ResponseWriter, ref string) bool {
	name := ref
	if name == "" {
		name = "default"
	}
	if strings.HasPrefix(name, "arn:") {
		parts := strings.Split(name, "/")
		if len(parts) > 1 {
			name = parts[len(parts)-1]
		}
	}
	if _, ok := ecsClusters.Get(name); !ok {
		AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest, "Cluster not found: %s", ref)
		return false
	}
	return true
}

func handleECSDescribeTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string   `json:"cluster"`
		Tasks   []string `json:"tasks"`
		Include []string `json:"include"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// The `tasks` list is required and must be non-empty.
	if len(req.Tasks) == 0 {
		AWSError(w, "InvalidParameterException", "Tasks cannot be empty.", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}

	var tasks []ECSTask
	var failures []map[string]string

	for _, taskRef := range req.Tasks {
		// Extract task ID from ARN
		taskID := taskRef
		if strings.HasPrefix(taskRef, "arn:") {
			parts := strings.Split(taskRef, "/")
			if len(parts) > 0 {
				taskID = parts[len(parts)-1]
			}
		}

		task, ok := ecsTasks.Get(taskID)
		if ok {
			tasks = append(tasks, task)
		} else {
			failures = append(failures, map[string]string{
				"arn":    taskRef,
				"reason": "MISSING",
			})
		}
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"tasks":    ecsTasksWireInclude(tasks, ecsIncludeHasTags(req.Include)),
		"failures": failures,
	})
}

func handleECSStopTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string `json:"cluster"`
		Task    string `json:"task"`
		Reason  string `json:"reason"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	if req.Task == "" {
		AWSError(w, "InvalidParameterException", "task is required", http.StatusBadRequest)
		return
	}

	taskID := ecsTaskIDFromRef(req.Task)
	if !stopECSTask(taskID, req.Reason, "UserInitiated") {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"Task not found: %s", req.Task)
		return
	}
	task, _ := ecsTasks.Get(taskID)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"task": ecsTaskWire{ECSTask: task, includeTags: true},
	})
}

// stopECSTask transitions a task to STOPPED — stopping its Docker containers,
// recording the stop code/reason/exit code, and tearing down its VPC veth.
// Returns false when the task is unknown. Used by the StopTask API handler
// and the in-process service scheduler.
func stopECSTask(taskID, reason, code string) bool {
	lifecycleLock := ecsTaskLifecycleLock(taskID)
	lifecycleLock.Lock()
	defer lifecycleLock.Unlock()

	existing, ok := ecsTasks.Get(taskID)
	if !ok {
		return false
	}

	// Stop running container if any
	if v, ok := ecsProcessHandles.LoadAndDelete(taskID); ok {
		if procs, ok := v.(*ecsTaskProcesses); ok {
			cleanupECSTaskProcesses(taskID, procs)
		}
	}

	now := time.Now().Unix()
	ecsTasks.Update(taskID, func(t *ECSTask) {
		t.DesiredStatus = ECSTaskStatusStopped
		t.LastStatus = ECSTaskStatusStopped
		t.StoppedAt = &now
		t.StopCode = code
		switch {
		case reason != "":
			t.StoppedReason = reason
		case code == "UserInitiated":
			t.StoppedReason = "Task stopped by user"
		default:
			t.StoppedReason = ""
		}
		// A user-initiated stop SIGKILLs the container; the faithful exit code is
		// 137 (128+SIGKILL), what real Fargate reports — not a clean-exit 0.
		exitCode := 137
		for j := range t.Containers {
			t.Containers[j].LastStatus = "STOPPED"
			t.Containers[j].ExitCode = &exitCode
		}
		ecsCleanupTaskManagedEBS(t)
	})
	instanceKey := ecsContainerInstanceKeyFromARN(existing.ContainerInstanceArn)
	switch existing.LastStatus {
	case ECSTaskStatusProvisioning, ECSTaskStatusPending:
		ecsUpdateContainerInstanceTaskCounts(instanceKey, -1, 0)
	case ECSTaskStatusRunning:
		ecsUpdateContainerInstanceTaskCounts(instanceKey, 0, -1)
	}

	// Tear down the task's VPC veth (netns tier) after cloud-visible state is
	// updated; Docker/netns cleanup can take seconds on CI.
	go ec2DetachRealECSTaskNIC(context.Background(), taskID)
	ecsRequestServiceReconcileForTask(existing)
	return true
}

// ecsStoppedTaskRetention is how long a stopped task remains visible. Amazon
// ECS stops returning stopped tasks from ListTasks about an hour after they
// stop and keeps them behind DescribeTasks only briefly after that, so a
// simulator that retains them forever diverges in a way that compounds: a
// cluster accumulates thousands of stopped tasks, ListTasks pages through all
// of them to answer "what happened to my task", and a cluster with one running
// task and thousands stopped reads at a glance like a crash loop.
const ecsStoppedTaskRetention = time.Hour

// ecsTaskExpired reports whether a stopped task has aged out of the window
// Amazon ECS keeps it visible for. A task that is not stopped never expires,
// and neither does one whose stop time was never recorded — the absence of a
// timestamp is not evidence of age.
func ecsTaskExpired(t ECSTask, now time.Time) bool {
	if t.LastStatus != ECSTaskStatusStopped || t.StoppedAt == nil {
		return false
	}
	return now.Sub(time.Unix(*t.StoppedAt, 0)) > ecsStoppedTaskRetention
}

// ecsSweepStoppedTasks deletes the tasks that have aged out, so the retention
// is a real bound on what the simulator holds rather than only a filter on what
// it reports.
func ecsSweepStoppedTasks(now time.Time) int {
	swept := 0
	for _, task := range ecsTasks.List() {
		if !ecsTaskExpired(task, now) {
			continue
		}
		if ecsTasks.Delete(task.TaskArn) {
			swept++
		}
	}
	return swept
}

// ecsStartStoppedTaskSweeper prunes aged-out stopped tasks for as long as the
// server runs.
func ecsStartStoppedTaskSweeper(srv *sim.Server) {
	srv.StartBackground(func(ctx context.Context) {
		ticker := time.NewTicker(ecsStoppedTaskSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ecsSweepStoppedTasks(time.Now())
			}
		}
	})
}

// ecsStoppedTaskSweepInterval is how often the sweep runs. It is far shorter
// than the retention window so a task leaves soon after it ages out, and far
// longer than a request so the sweep costs nothing measurable.
const ecsStoppedTaskSweepInterval = time.Minute

func handleECSListTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster       string `json:"cluster"`
		Family        string `json:"family"`
		DesiredStatus string `json:"desiredStatus"`
		LaunchType    string `json:"launchType"`
		ServiceName   string `json:"serviceName"`
		StartedBy     string `json:"startedBy"`
		NextToken     string `json:"nextToken"`
		MaxResults    int    `json:"maxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}

	clusterName := req.Cluster
	if clusterName == "" {
		clusterName = "default"
	}
	if strings.HasPrefix(clusterName, "arn:") {
		parts := strings.Split(clusterName, "/")
		if len(parts) > 1 {
			clusterName = parts[len(parts)-1]
		}
	}

	clusterArn := ecsArn("cluster", clusterName)

	now := time.Now()
	tasks := ecsTasks.Filter(func(t ECSTask) bool {
		if t.ClusterArn != clusterArn {
			return false
		}
		if ecsTaskExpired(t, now) {
			return false
		}
		if req.Family != "" {
			td, ok := ecsTaskDefinitions.Get(extractTDKey(t.TaskDefinitionArn))
			if !ok || td.Family != req.Family {
				return false
			}
		}
		if req.DesiredStatus != "" && string(t.DesiredStatus) != req.DesiredStatus {
			return false
		}
		if req.LaunchType != "" && t.LaunchType != req.LaunchType {
			return false
		}
		if req.StartedBy != "" && t.StartedBy != req.StartedBy {
			return false
		}
		if req.ServiceName != "" {
			group := t.Group
			if !strings.HasPrefix(group, "service:") || group[len("service:"):] != req.ServiceName {
				return false
			}
		}
		return true
	})
	sortBy(tasks, func(t ECSTask) string { return t.TaskArn })

	page, next := awsPage(tasks, req.NextToken, req.MaxResults, 100)

	taskArns := make([]string, 0, len(page))
	for _, t := range page {
		taskArns = append(taskArns, t.TaskArn)
	}

	out := map[string]any{"taskArns": taskArns}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleECSDeleteCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster string `json:"cluster"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Cluster == "" {
		AWSError(w, "InvalidParameterException", "cluster is required", http.StatusBadRequest)
		return
	}

	name := req.Cluster
	if strings.HasPrefix(name, "arn:") {
		parts := strings.Split(name, "/")
		if len(parts) > 1 {
			name = parts[len(parts)-1]
		}
	}

	cluster, ok := ecsClusters.Get(name)
	if !ok {
		AWSErrorf(w, "ClusterNotFoundException", http.StatusBadRequest,
			"Cluster not found: %s", req.Cluster)
		return
	}

	cluster.Status = "INACTIVE"
	ecsClusters.Delete(name)

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"cluster": cluster,
	})
}

// handleECSTagResource implements `AmazonEC2ContainerServiceV20141113.TagResource`.
// `mergeECSTagsByKey` adds new tags + overwrites existing keys;
// missing tags persist. Real ECS rejects TagResource on STOPPED
// tasks; we mirror that behaviour so the recovery.go "skip STOPPED"
// logic exercises the same gate.
func handleECSTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string   `json:"resourceArn"`
		Tags        []ECSTag `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceArn == "" {
		AWSError(w, "InvalidParameterException", "resourceArn is required", http.StatusBadRequest)
		return
	}
	if fault := ecsRejectTaggingAStoppedTask(req.ResourceArn); fault != nil {
		ecsWriteTagFault(w, fault)
		return
	}
	target, fault := ecsResolveTaggable(req.ResourceArn)
	if fault != nil {
		ecsWriteTagFault(w, fault)
		return
	}
	target.replace(mergeECSTagsByKey(target.tags, req.Tags))
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleECSUntagResource implements `AmazonEC2ContainerServiceV20141113.UntagResource`.
// Companion to TagResource; removes the named tags. Same STOPPED-task
// rejection rule.
func handleECSUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string   `json:"resourceArn"`
		TagKeys     []string `json:"tagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceArn == "" || len(req.TagKeys) == 0 {
		AWSError(w, "InvalidParameterException", "resourceArn and tagKeys are required", http.StatusBadRequest)
		return
	}
	if fault := ecsRejectTaggingAStoppedTask(req.ResourceArn); fault != nil {
		ecsWriteTagFault(w, fault)
		return
	}
	target, fault := ecsResolveTaggable(req.ResourceArn)
	if fault != nil {
		ecsWriteTagFault(w, fault)
		return
	}
	drop := make(map[string]struct{}, len(req.TagKeys))
	for _, key := range req.TagKeys {
		drop[key] = struct{}{}
	}
	// Built fresh rather than filtered in place: the slice belongs to the
	// stored record, and writing through it would edit the store's own copy
	// before the replace decides to.
	kept := make([]ECSTag, 0, len(target.tags))
	for _, tag := range target.tags {
		if _, gone := drop[tag.Key]; gone {
			continue
		}
		kept = append(kept, tag)
	}
	target.replace(kept)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// mergeECSTagsByKey combines `existing` with `incoming`: any key
// present in both is overwritten by the `incoming` value (matching
// real ECS TagResource semantics — "If existing tags on a resource
// are not specified in the request parameters, they aren't changed").
func mergeECSTagsByKey(existing, incoming []ECSTag) []ECSTag {
	byKey := make(map[string]ECSTag, len(existing)+len(incoming))
	for _, t := range existing {
		byKey[t.Key] = t
	}
	for _, t := range incoming {
		byKey[t.Key] = t
	}
	out := make([]ECSTag, 0, len(byKey))
	for _, t := range byKey {
		out = append(out, t)
	}
	return out
}

func handleECSListTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"resourceArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	// Resolved through the same function TagResource uses, so a type cannot be
	// taggable and then invisible here.
	target, fault := ecsResolveTaggable(req.ResourceArn)
	if fault != nil {
		ecsWriteTagFault(w, fault)
		return
	}
	tags := target.tags
	if tags == nil {
		tags = []ECSTag{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// ecsExecSessions tracks active ECS exec sessions for WebSocket handlers.
var ecsExecSessions sync.Map // map[sessionID]ecsExecSession

type ecsExecSession struct {
	taskID            string
	command           string
	dockerContainerID string
	tokenValue        string
}

// ssmStreamWriter wraps chunks in an SSM output_stream_data AgentMessage
// frame before sending over the WebSocket. The backend's decoder
// parses these frames to reconstruct the Docker-mux'd stream;
// sending raw bytes silently produces empty exec output.
type ssmStreamWriter struct {
	conn        *websocket.Conn
	payloadType uint32 // 1 = stdout, 11 = stderr
	mu          *sync.Mutex
}

func (w *ssmStreamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	frame := buildSSMOutputFrame(w.payloadType, p)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		return 0, err
	}
	return len(p), nil
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// resolveECSContainerSecrets resolves a container definition's `secrets` array
// (`[{"name","valueFrom"}]`) to name→value pairs by fetching each `valueFrom`
// from Secrets Manager or SSM Parameter Store, as real ECS does at task launch.
func resolveECSContainerSecrets(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var secrets []struct {
		Name      string `json:"name"`
		ValueFrom string `json:"valueFrom"`
	}
	if err := json.Unmarshal(raw, &secrets); err != nil {
		return out
	}
	for _, s := range secrets {
		if s.Name == "" || s.ValueFrom == "" {
			continue
		}
		if v, ok := resolveECSSecretValue(s.ValueFrom); ok {
			out[s.Name] = v
		}
	}
	return out
}

// resolveECSSecretValue resolves a single ECS secret `valueFrom` reference.
// Secrets Manager: arn:aws:secretsmanager:…:secret:name-suffix[:jsonKey:stage:id]
// — an optional jsonKey selects a field from a JSON SecretString. SSM:
// arn:aws:ssm:…:parameter/name or a bare /name.
func resolveECSSecretValue(valueFrom string) (string, bool) {
	if strings.Contains(valueFrom, ":secretsmanager:") {
		parts := strings.Split(valueFrom, ":")
		if len(parts) < 7 {
			return "", false
		}
		baseARN := strings.Join(parts[:7], ":")
		secret, ok := resolveSMSecret(baseARN)
		if !ok {
			return "", false
		}
		val := secret.SecretString
		if len(parts) >= 8 && parts[7] != "" {
			var m map[string]any
			if json.Unmarshal([]byte(val), &m) == nil {
				if jv, ok := m[parts[7]]; ok {
					return fmt.Sprint(jv), true
				}
			}
		}
		return val, true
	}
	// SSM Parameter Store (ARN or bare name).
	name := valueFrom
	if i := strings.Index(valueFrom, ":parameter"); i >= 0 {
		name = valueFrom[i+len(":parameter"):]
	}
	if p, ok := ssmParams.Get(ensureLeadingSlash(name)); ok {
		return p.Value, true
	}
	return "", false
}

// handleECSExecuteCommand returns a handler that implements ECS ExecuteCommand.
// It creates a session and registers a WebSocket handler for command execution.
func handleECSExecuteCommand(srv *sim.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Cluster     string `json:"cluster"`
			Task        string `json:"task"`
			Container   string `json:"container"`
			Command     string `json:"command"`
			Interactive bool   `json:"interactive"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
			return
		}
		if req.Task == "" {
			AWSError(w, "InvalidParameterException", "task is required", http.StatusBadRequest)
			return
		}
		if req.Command == "" {
			AWSError(w, "InvalidParameterException", "command is required", http.StatusBadRequest)
			return
		}
		if !req.Interactive {
			AWSError(w, "InvalidParameterException",
				"Amazon ECS only supports initiating interactive execute command sessions. Specify interactive as true.",
				http.StatusBadRequest)
			return
		}

		// Extract task ID from ARN
		taskID := req.Task
		if strings.HasPrefix(taskID, "arn:") {
			parts := strings.Split(taskID, "/")
			if len(parts) > 0 {
				taskID = parts[len(parts)-1]
			}
		}

		// Verify task exists and is RUNNING
		task, ok := ecsTasks.Get(taskID)
		if !ok {
			AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
				"Task not found: %s", req.Task)
			return
		}
		if task.LastStatus != ECSTaskStatusRunning {
			AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
				"Execute command is not supported on task in %s status", task.LastStatus)
			return
		}
		if req.Cluster != "" && task.ClusterArn != ecsArn("cluster", ecsClusterNameFromRef(req.Cluster)) {
			AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
				"Task not found: %s", req.Task)
			return
		}
		container := ecsExecTargetContainer(task, req.Container)
		if container == nil {
			if req.Container == "" {
				AWSError(w, "InvalidParameterException",
					"container is required when the task has multiple containers",
					http.StatusBadRequest)
			} else {
				AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
					"Container not found: %s", req.Container)
			}
			return
		}
		// Real ECS rejects exec unless the task was started with
		// enableExecuteCommand=true (the SSM exec agent is only injected then).
		if !task.EnableExecuteCommand {
			AWSError(w, "InvalidParameterException",
				"The execute command failed because execute command was not enabled when the task was run or the execute command agent isn't running. Wait and try again or run a new task with execute command enabled and try again.",
				http.StatusBadRequest)
			return
		}

		sessionID := generateUUID()

		// Store the session
		// Look up the Docker container ID for this task. Because RUNNING is now
		// reported only after the container handle is stored, a brief grace
		// poll is enough to cover the tiny window between the store and the
		// status update on extremely loaded runners.
		var dockerContainerID string
		for i := 0; i < 10; i++ {
			if v, ok := ecsProcessHandles.Load(taskID); ok {
				if procs, ok := v.(*ecsTaskProcesses); ok {
					if handle := procs.handleFor(container.Name); handle != nil {
						dockerContainerID = handle.ContainerID
						break
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		if dockerContainerID == "" {
			AWSError(w, "TargetNotConnectedException",
				"The execute command cannot run because the task target is not connected.",
				http.StatusBadRequest)
			return
		}

		tokenValue := "token-" + sessionID[:8]
		ecsExecSessions.Store(sessionID, ecsExecSession{
			taskID:            taskID,
			command:           req.Command,
			dockerContainerID: dockerContainerID,
			tokenValue:        tokenValue,
		})

		// Determine host from the incoming request
		host := r.Host
		if host == "" {
			host = "localhost:4566"
		}
		streamURL := fmt.Sprintf("ws://%s/ecs-exec/%s", host, sessionID)

		// WebSocket endpoint is registered statically as /ecs-exec/{sessionId}

		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"clusterArn":    task.ClusterArn,
			"containerArn":  container.ContainerArn,
			"containerName": container.Name,
			"interactive":   true,
			"session": map[string]any{
				"sessionId":  sessionID,
				"streamUrl":  streamURL,
				"tokenValue": tokenValue,
			},
			"taskArn": task.TaskArn,
		})
	}
}

func ecsExecTargetContainer(task ECSTask, containerName string) *ECSTaskContainer {
	if containerName != "" {
		for i := range task.Containers {
			if task.Containers[i].Name == containerName {
				return &task.Containers[i]
			}
		}
		return nil
	}
	if len(task.Containers) == 1 {
		return &task.Containers[0]
	}
	return nil
}

// handleECSExecWebSocket returns a handler for an ECS exec WebSocket session.
// It upgrades the connection and bridges stdin/stdout/stderr of the command.
func handleECSExecWebSocket(sessionID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessVal, ok := ecsExecSessions.LoadAndDelete(sessionID)
		if !ok {
			http.Error(w, "session not found or already used", http.StatusNotFound)
			return
		}
		sess, ok := sessVal.(ecsExecSession)
		if !ok {
			http.Error(w, "session not found or already used", http.StatusNotFound)
			return
		}

		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck

		// SSM Session Manager data-channel handshake. A real client
		// (session-manager-plugin, or any faithful client like the ECS
		// backend's exec_cloud.go) sends an OpenDataChannel message as the
		// FIRST WebSocket frame — a JSON document carrying the TokenValue
		// returned by ECS.ExecuteCommand. The service validates the token
		// before any AgentMessage streaming begins. Consume and validate it
		// here so a coordinate-only consumer drives the sim exactly as it
		// drives real AWS; without this the token is never checked and the
		// first frame would be mis-read as input_stream_data.
		if !validateSSMOpenDataChannel(conn, sess.tokenValue) {
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.ClosePolicyViolation,
					"OpenDataChannel handshake failed: invalid or missing TokenValue"))
			return
		}

		// Execute command inside the real Docker container
		if sess.dockerContainerID != "" {
			cli := sim.DockerClient()
			if cli != nil {
				// Always wrap the entire received string as a shell script.
				// Backends now wrap commands in `sh -c '<script>'` before
				// sending to ECS.ExecuteCommand (real AWS exec()s argv[0]
				// and rejects shell builtins / pipes / env-expansion). The
				// previous "unwrap if it starts with sh -c " path stripped
				// `-c ` then handed the remaining bytes to Docker exec
				// verbatim, which left the surrounding single quotes
				// intact — `'echo …'` was then exec()'d as a single
				// command name and produced "sh: 'echo …': not found".
				// Treat the whole received string as one shell script
				// regardless of whether the backend already wrapped it;
				// double-wrapping is correct (the inner shell parses the
				// outer script and dispatches the inner shell itself).
				execCmd := []string{"sh", "-c", sess.command}
				execCfg := dockerclient.ExecCreateOptions{
					Cmd:          execCmd,
					AttachStdin:  true,
					AttachStdout: true,
					AttachStderr: true,
				}
				execResp, err := cli.ExecCreate(r.Context(), sess.dockerContainerID, execCfg)
				if err != nil {
					_ = conn.WriteMessage(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()))
					return
				}
				attach, err := cli.ExecAttach(r.Context(), execResp.ID, dockerclient.ExecAttachOptions{})
				if err != nil {
					_ = conn.WriteMessage(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()))
					return
				}
				defer attach.Close()

				// Bridge: WebSocket → Docker exec stdin. The backend wraps
				// stdin in SSM `input_stream_data` AgentMessage frames; real
				// ssm-agent decodes the frame, forwards only the payload to
				// the user process, and closes the user's stdin when the
				// frame's FIN flag is set so readers like `cat`, `tar`, and
				// `gzip` see EOF. Match that contract.
				simGo(func() {
					defer attach.CloseWrite() //nolint:errcheck
					for {
						_, msg, rerr := conn.ReadMessage()
						if rerr != nil {
							return
						}
						payload, mt, fin, perr := decodeSSMInputFrame(msg)
						if perr != nil {
							// Not a parseable SSM frame — skip silently.
							// Real ssm-agent ignores unrecognized frames.
							continue
						}
						if mt != ssmMTInputStreamData {
							continue
						}
						if len(payload) > 0 {
							if _, werr := attach.Conn.Write(payload); werr != nil {
								return
							}
						}
						if fin {
							return
						}
					}
				})

				// Bridge: Docker exec → WebSocket wrapped in SSM
				// AgentMessage frames. The backend's SSM decoder
				// (backends/ecs/exec_cloud.go, will only see
				// output if each chunk arrives as a proper
				// output_stream_data frame.
				writeMu := &sync.Mutex{}
				stdoutWriter := &ssmStreamWriter{conn: conn, payloadType: ssmPayloadStdout, mu: writeMu}
				stderrWriter := &ssmStreamWriter{conn: conn, payloadType: ssmPayloadStderr, mu: writeMu}
				_, _ = stdcopy.StdCopy(stdoutWriter, stderrWriter, attach.Reader)

				// Real AWS Session Manager sends an output_stream_data
				// frame with PayloadType=12 carrying the exec process's
				// exit code before the channel is closed. Match that so
				// the backend decoder sees the true exit status.
				exitCode := 0
				if inspect, err := cli.ExecInspect(r.Context(), execResp.ID, dockerclient.ExecInspectOptions{}); err == nil {
					exitCode = inspect.ExitCode
				}
				writeMu.Lock()
				_ = conn.WriteMessage(websocket.BinaryMessage,
					buildSSMOutputFrame(ssmPayloadExitCode, []byte(strconv.Itoa(exitCode))))
				// Then signal channel close so the decoder unwinds cleanly.
				_ = conn.WriteMessage(websocket.BinaryMessage, buildSSMChannelClosed())
				writeMu.Unlock()

				_ = conn.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
					time.Now().Add(5*time.Second),
				)
				return
			}
		}

		// No fallback: ExecuteCommand requires the task's Docker
		// container. The sim never `os/exec`s the command on the sim
		// host — that would run against the wrong "host" entirely
		// (sim-binary host, not the Fargate-shaped task container).
		// See feedback_sim_host_model.md.
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr,
				"ECS ExecuteCommand requires a running Docker container for the task"))
	}
}

// ssmOpenDataChannelInput is the JSON body a client sends as the first
// WebSocket frame to open an SSM Session Manager data channel. Mirrors
// the session-manager-plugin's service.OpenDataChannelInput; only
// TokenValue is load-bearing for the sim (it validates the token issued
// by ECS.ExecuteCommand). Extra fields are tolerated.
type ssmOpenDataChannelInput struct {
	MessageSchemaVersion string `json:"MessageSchemaVersion"`
	RequestId            string `json:"RequestId"`
	TokenValue           string `json:"TokenValue"`
	ClientId             string `json:"ClientId"`
}

// validateSSMOpenDataChannel reads the first WebSocket frame, parses it as
// an OpenDataChannel message, and reports whether its TokenValue matches
// the token issued for this session. A short read deadline bounds a client
// that connects but never sends the handshake (real AWS times such
// connections out rather than streaming).
func validateSSMOpenDataChannel(conn *websocket.Conn, expectedToken string) bool {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		return false
	}
	// Clear the deadline so subsequent stdin frames aren't bounded.
	_ = conn.SetReadDeadline(time.Time{})

	var in ssmOpenDataChannelInput
	if err := json.Unmarshal(msg, &in); err != nil {
		return false
	}
	return in.TokenValue != "" && in.TokenValue == expectedToken
}

// extractTDKey extracts "family:revision" from a task definition ARN.
func extractTDKey(arn string) string {
	parts := strings.Split(arn, "/")
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return arn
}

// discardLogSink drops log lines. Used when a task definition has no
// awslogs configuration — the container still runs (so task lifecycle
// transitions to STOPPED) but its stdout/stderr aren't captured.
type discardLogSink struct{}

func (discardLogSink) WriteLog(sim.LogLine) {}

// cwLogSink implements sim.LogSink and writes log lines to CloudWatch.
type cwLogSink struct {
	logGroup  string
	logStream string
}

func (s *cwLogSink) WriteLog(line sim.LogLine) {
	cwIngestWorkloadLogLine(s.logGroup, s.logStream, line.Text)
}

// Fargate CPU/memory validation. Valid combinations per AWS docs.
// Lower tiers (256, 512) have explicit valid values; higher tiers use ranges.
type fargateCombo struct {
	cpu        int
	memOptions []int // explicit valid values (nil = use range)
	memMin     int
	memMax     int
	memInc     int
}

var fargateCombos = []fargateCombo{
	{256, []int{512, 1024, 2048}, 0, 0, 0},
	{512, []int{1024, 2048, 3072, 4096}, 0, 0, 0},
	{1024, nil, 2048, 8192, 1024},
	{2048, nil, 4096, 16384, 1024},
	{4096, nil, 8192, 30720, 1024},
	{8192, nil, 16384, 61440, 4096},
	{16384, nil, 32768, 122880, 8192},
}

func hasFargate(compatibilities []string) bool {
	for _, c := range compatibilities {
		if strings.EqualFold(c, "FARGATE") {
			return true
		}
	}
	return false
}

func validateFargateResources(cpuStr, memStr string) error {
	cpu, err := strconv.Atoi(cpuStr)
	if err != nil {
		return fmt.Errorf("invalid cpu value: %s", cpuStr)
	}
	mem, err := strconv.Atoi(memStr)
	if err != nil {
		return fmt.Errorf("invalid memory value: %s", memStr)
	}

	for _, combo := range fargateCombos {
		if combo.cpu != cpu {
			continue
		}
		if len(combo.memOptions) > 0 {
			for _, opt := range combo.memOptions {
				if opt == mem {
					return nil
				}
			}
			return fmt.Errorf("invalid memory value %d for cpu %d, valid values: %v", mem, cpu, combo.memOptions)
		}
		if mem >= combo.memMin && mem <= combo.memMax && (mem-combo.memMin)%combo.memInc == 0 {
			return nil
		}
		return fmt.Errorf("invalid memory value %d for cpu %d, valid range: %d-%d in %d increments",
			mem, cpu, combo.memMin, combo.memMax, combo.memInc)
	}
	return fmt.Errorf("invalid cpu value %d, valid values: 256, 512, 1024, 2048, 4096, 8192, 16384", cpu)
}
