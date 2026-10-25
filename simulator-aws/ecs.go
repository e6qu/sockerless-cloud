package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/signals"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/kvstore"
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
	// WorkingDirectory is the directory the container's process starts in;
	// the container runtime creates it when the image does not hold it.
	WorkingDirectory string `json:"workingDirectory,omitempty"`
	// StopTimeout is how many seconds Amazon ECS waits after SIGTERM before it
	// kills the container; unset means the platform default.
	StopTimeout *int `json:"stopTimeout,omitempty"`
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
	Name          string `json:"name,omitempty"`
	AppProtocol   string `json:"appProtocol,omitempty"`
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
	Iops              int                                 `json:"iops,omitempty"`
	Throughput        int                                 `json:"throughput,omitempty"`
	FilesystemType    string                              `json:"filesystemType,omitempty"`
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
	ContainerArn string `json:"containerArn"`
	// The task, image, digest and sizing Amazon ECS reports for each container
	// in DescribeTasks. imageDigest is known once the image is pulled; cpu is
	// "0" when the definition set none, memory and memoryReservation are absent.
	TaskArn           string                `json:"taskArn,omitempty"`
	Name              string                `json:"name"`
	Image             string                `json:"image,omitempty"`
	ImageDigest       string                `json:"imageDigest,omitempty"`
	Cpu               string                `json:"cpu,omitempty"`
	Memory            string                `json:"memory,omitempty"`
	MemoryReservation string                `json:"memoryReservation,omitempty"`
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
	// Seconds since the epoch with a fractional part, like createdAt and the
	// pull timestamps: Amazon ECS reports every task timestamp at millisecond
	// resolution, and whole seconds here put startedAt before pullStoppedAt.
	StartedAt *float64 `json:"startedAt,omitempty"`
	// StoppingAt is when the task was asked to stop and StoppedAt when it
	// finished stopping. Between the two the task's desired status is STOPPED
	// while its containers are still being given their stop timeout.
	StoppingAt *float64 `json:"stoppingAt,omitempty"`
	StoppedAt  *float64 `json:"stoppedAt,omitempty"`
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
	// ecsTaskChanges wakes a waiter on a task when the store writes it.
	ecsTaskChanges = signals.New[string]()
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
	ecsTaskLifecycleLocks sync.Map // map[taskID]ecsLifecycleLock
	// ecsBackgroundServer owns the task-container-start goroutines launched by
	// runECSTasks. Orderly shutdown drains them before SQLite is closed, so an
	// in-flight container start cannot read a durable store after the database
	// is closed (the BUG-2827 class, extended to ECS task startup).
	ecsBackgroundServer *sim.Server
)

type ecsTaskProcesses struct {
	MainContainerName string
	Handles           map[string]placement.Handle
	// StopGrace is each container's SIGTERM-to-SIGKILL grace, from its
	// definition's stopTimeout.
	StopGrace map[string]time.Duration
}

// ecsDefaultStopTimeout is what Amazon ECS waits between SIGTERM and SIGKILL
// when a container definition sets no stopTimeout: 30 seconds on Fargate and
// the EC2 agent's ECS_CONTAINER_STOP_TIMEOUT default alike.
const ecsDefaultStopTimeout = 30 * time.Second

// ecsContainerStopGrace is the grace a stopping container gets: its
// definition's stopTimeout, else the platform default.
func ecsContainerStopGrace(cd ECSContainerDefinition) time.Duration {
	if cd.StopTimeout != nil && *cd.StopTimeout >= 0 {
		return time.Duration(*cd.StopTimeout) * time.Second
	}
	return ecsDefaultStopTimeout
}

// ecsTaskStopGrace maps each of a definition's containers to its stop grace.
func ecsTaskStopGrace(td ECSTaskDefinition) map[string]time.Duration {
	grace := make(map[string]time.Duration, len(td.ContainerDefinitions))
	for _, cd := range td.ContainerDefinitions {
		grace[cd.Name] = ecsContainerStopGrace(cd)
	}
	return grace
}

func (p *ecsTaskProcesses) firstHandle() placement.Handle {
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

func (p *ecsTaskProcesses) handleFor(containerName string) placement.Handle {
	if p == nil {
		return nil
	}
	if containerName != "" {
		return p.Handles[containerName]
	}
	return p.firstHandle()
}

// ecsLifecycleLock is a task's lifecycle mutex that a lifecycle step can stop
// waiting for when the simulator shuts down.
type ecsLifecycleLock chan struct{}

func (l ecsLifecycleLock) Lock() { l <- struct{}{} }

func (l ecsLifecycleLock) Unlock() { <-l }

// LockContext takes the lock, or reports false once ctx is done first.
func (l ecsLifecycleLock) LockContext(ctx context.Context) bool {
	select {
	case l <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func ecsTaskLifecycleLock(taskID string) ecsLifecycleLock {
	lock, _ := ecsTaskLifecycleLocks.LoadOrStore(taskID, make(ecsLifecycleLock, 1))
	mutex, ok := lock.(ecsLifecycleLock)
	if !ok {
		panic("Amazon ECS task lifecycle lock contained a non-lock value")
	}
	return mutex
}

func stopECSTaskProcesses(ctx context.Context, p *ecsTaskProcesses) {
	if p == nil {
		return
	}
	// Amazon ECS signals the containers of a stopping task together, each with
	// its own stopTimeout (the grace its workload carries), so the task takes
	// as long as its slowest container, not the sum of them. A cancelled ctx
	// stops the wait, not the stop: the backend has the signal and kills the
	// container on its own schedule.
	var stopping sync.WaitGroup
	for name, h := range p.Handles {
		if h == nil {
			continue
		}
		stopping.Add(1)
		go func(h placement.Handle, grace time.Duration) {
			defer stopping.Done()
			h.Cancel()
			waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second+grace)
			defer cancel()
			select {
			case <-h.Exited():
			case <-waitCtx.Done():
			}
		}(h, p.StopGrace[name])
	}
	stopping.Wait()
}

// ecsAbandonTaskStart removes the containers of a start the simulator's
// shutdown interrupted. The task stays PENDING, and the next process resumes
// it under the same container names, so none of them may outlive this one.
func ecsAbandonTaskStart(taskID string, p *ecsTaskProcesses) {
	if p == nil {
		return
	}
	for _, handle := range p.Handles {
		if handle == nil || handle.ID() == "" {
			continue
		}
		if err := sim.RemoveContainer(handle.ID()); err != nil {
			fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: remove container of the interrupted start: %v\n", taskID, err)
		}
	}
}

// cleanupECSTaskProcesses stops a task's containers and waits for the engine
// to remove them. Once ctx is done it stops waiting and leaves the rest to the
// next process's recovery of the stopping task.
func cleanupECSTaskProcesses(ctx context.Context, taskID string, p *ecsTaskProcesses) {
	if p == nil {
		return
	}
	stopECSTaskProcesses(ctx, p)
	if ctx.Err() != nil {
		return
	}
	handles := make([]placement.Handle, 0, len(p.Handles))
	for name, handle := range p.Handles {
		if name != p.MainContainerName {
			handles = append(handles, handle)
		}
	}
	if mainHandle := p.Handles[p.MainContainerName]; mainHandle != nil {
		handles = append(handles, mainHandle)
	}
	for _, handle := range handles {
		if handle == nil || handle.ID() == "" {
			continue
		}
		select {
		case <-handle.Exited():
		case <-time.After(5 * time.Second):
			fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: container %s is still being removed\n", taskID, handle.ID())
		}
	}
	if err := sim.RemoveVolumesNamed(ecsTaskVolumePrefix(taskID)); err != nil {
		fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: remove its volumes: %v\n", taskID, err)
	}
	if _, vpcID, ok := ecsTaskENIGroups(taskID); ok {
		ec2ReapplyIngress(vpcID)
	}
}

// ecsTaskVolumePrefix names a task's own volumes, by the task.
func ecsTaskVolumePrefix(taskID string) string { return "sockerless-ecs-task-" + taskID + "-" }

func ecsArn(resourceType, id string) string {
	return fmt.Sprintf("arn:aws:ecs:"+awsRegion()+":"+awsAccountID()+":%s/%s", resourceType, id)
}

func registerECS(r *AWSRouter, srv *sim.Server) {
	ecsClusters = sim.MakeStore[ECSCluster](srv.DB(), "ecs_clusters")
	// Read in bulk by every ListTaskDefinitions and ListTaskDefinitionFamilies,
	// so the rows stay in memory rather than each call decoding them all.
	ecsTaskDefinitions = sim.MakeCachedStore[ECSTaskDefinition](srv.DB(), "ecs_task_definitions")
	ecsTasks = sim.Announcing(sim.MakeStore[ECSTask](srv.DB(), "ecs_tasks"), ecsTaskChanges)
	ecsManagedVolumeOwners = sim.MakeStore[ecsManagedVolumeOwner](srv.DB(), "ecs_managed_volume_owners")
	ecsBackgroundServer = srv
	kvstore.StartSweeper(srv, "Amazon ECS stopped task retention", storeSweepInterval, func(now time.Time) { ecsSweepStoppedTasks(now) })
	startECSServiceMetricsPublisher(srv)
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
	cwIngestWorkloadLogLine(s.logGroup, s.logStream, line.Text, line.Timestamp)
}

// ecsTaskGroup is the task group Amazon ECS records: the one the request named,
// or `family:<family>` — the documented default — when it named none.
func ecsTaskGroup(requested string, td ECSTaskDefinition) string {
	if requested != "" {
		return requested
	}
	return "family:" + td.Family
}

// ecsApplyContainerSizing reports a container's CPU units and memory the way
// Amazon ECS does in DescribeTasks — strings, from the container definition
// with the run's container override applied. cpu is "0" when neither set it.
func ecsApplyContainerSizing(c *ECSTaskContainer, cd ECSContainerDefinition, overrides *ECSTaskOverride) {
	cpu, memory, reservation := cd.Cpu, cd.Memory, cd.MemoryReservation
	o := ecsContainerOverrideFor(overrides, cd.Name)
	if o.Cpu != nil {
		cpu = *o.Cpu
	}
	if o.Memory != nil {
		memory = *o.Memory
	}
	if o.MemoryReservation != nil {
		reservation = *o.MemoryReservation
	}
	c.Cpu = strconv.Itoa(cpu)
	if memory > 0 {
		c.Memory = strconv.Itoa(memory)
	}
	if reservation > 0 {
		c.MemoryReservation = strconv.Itoa(reservation)
	}
}
