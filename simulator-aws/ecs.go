package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/signals"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/kvstore"
)

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
	// Stored raw so they round-trip exactly; DescribeClusters shows them only when
	// `include` lists SETTINGS or CONFIGURATIONS.
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
	// The container runtime creates it when the image lacks it.
	WorkingDirectory string `json:"workingDirectory,omitempty"`
	// Seconds Amazon ECS waits after SIGTERM before killing the container; unset
	// means the platform default.
	StopTimeout *int `json:"stopTimeout,omitempty"`
	// The runtime decodes healthCheck and secrets; every other field rides `raw`.
	HealthCheck json.RawMessage          `json:"healthCheck,omitempty"`
	Secrets     json.RawMessage          `json:"secrets,omitempty"`
	DependsOn   []ECSContainerDependency `json:"dependsOn,omitempty"`
	// Seconds a container that depends on this one waits for it to reach a
	// COMPLETE, SUCCESS or HEALTHY condition, counted from this one's start.
	StartTimeout *int `json:"startTimeout,omitempty"`

	// raw holds the exact registered bytes. The provider hashes the whole
	// containerDefinitions JSON as ForceNew, so dropping any field forces a new
	// revision every plan.
	//
	// Being unexported, raw skips the persistence envelope's hidden-field sidecar;
	// it survives restarts only through the MarshalJSON/UnmarshalJSON codec below.
	raw json.RawMessage `json:"-"`
}

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

// Containers built in-process have no capture and use the typed encoding.
func (c ECSContainerDefinition) MarshalJSON() ([]byte, error) {
	if len(c.raw) > 0 {
		return c.raw, nil
	}
	type alias ECSContainerDefinition
	return json.Marshal(alias(c))
}

type ECSContainerDependency struct {
	ContainerName string `json:"containerName"`
	Condition     string `json:"condition"`
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
	// The provider reads these ForceNew fields back; nested objects ride
	// RawMessage verbatim.
	RuntimePlatform       json.RawMessage `json:"runtimePlatform,omitempty"`
	EphemeralStorage      json.RawMessage `json:"ephemeralStorage,omitempty"`
	ProxyConfiguration    json.RawMessage `json:"proxyConfiguration,omitempty"`
	PlacementConstraints  json.RawMessage `json:"placementConstraints,omitempty"`
	InferenceAccelerators json.RawMessage `json:"inferenceAccelerators,omitempty"`
	PidMode               string          `json:"pidMode,omitempty"`
	IpcMode               string          `json:"ipcMode,omitempty"`
	EnableFaultInjection  *bool           `json:"enableFaultInjection,omitempty"`
	// Compatibilities is the AWS-computed launch-type list, not
	// requiresCompatibilities. requiresAttributes is not modelled: it needs AWS's
	// capability-attribute engine.
	Compatibilities []string `json:"compatibilities,omitempty"`
	// AWS returns tags beside the taskDefinition object, not inside it (always
	// from RegisterTaskDefinition, from DescribeTaskDefinition with include=TAGS);
	// see ecsTaskDefTagsResponse.
	Tags   []ECSTag `json:"-"`
	Status string   `json:"status"`
}

type ECSTaskContainer struct {
	ContainerArn string `json:"containerArn"`
	// imageDigest is known once the image is pulled; cpu is "0" when the
	// definition set none, and memory and memoryReservation are then absent.
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
	// Set only for a container whose definition declares a healthCheck.
	HealthStatus string `json:"healthStatus,omitempty"`
}

// A bridge-network task advertises its host ports through these.
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
	// Fractional epoch seconds: Amazon ECS reports task timestamps at millisecond
	// resolution, and whole seconds would put startedAt before pullStoppedAt.
	StartedAt *float64 `json:"startedAt,omitempty"`
	// Between StoppingAt and StoppedAt the desired status is STOPPED while the
	// containers serve their stop timeout.
	StoppingAt *float64 `json:"stoppingAt,omitempty"`
	StoppedAt  *float64 `json:"stoppedAt,omitempty"`
	// The agent reports these while the task runs; DescribeTasks returns them.
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
	// Set only for a task with a container whose definition declares a
	// healthCheck.
	HealthStatus string `json:"healthStatus,omitempty"`
	// Internal state for resuming a task whose containers had not started at a
	// restart.
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

// The Task shape conveys networking only through attachments. Persist
// networkConfiguration, which subnet-egress wiring reads, but strip it from
// responses.
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

var (
	ecsClusters        sim.Store[ECSCluster]
	ecsTaskDefinitions sim.Store[ECSTaskDefinition]
	// The data plane reads this store on every forwarded request, possibly before
	// the service registers; an empty store answers that nothing is associated.
	ecsTasks       sim.Store[ECSTask] = sim.NewStateStore[ECSTask]()
	ecsTaskChanges                    = signals.New[string]()
	// Describes and task starts read it; registering writes it. Not reentrant.
	ecsRevisionMu     sync.RWMutex
	ecsRevisions      map[string]int // family -> latest revision
	ecsProcessHandles sync.Map       // map[taskID]*ecsTaskProcesses
	// Serialize asynchronous task startup with StopTask and scheduler stops:
	// a stop must not return while a startup can still attach networking or
	// publish RUNNING.
	ecsTaskLifecycleLocks sync.Map // map[taskID]ecsLifecycleLock
	// Orderly shutdown drains task-start goroutines before SQLite closes.
	ecsBackgroundServer *sim.Server
)

type ecsTaskProcesses struct {
	// Each container's SIGTERM-to-SIGKILL grace, from its stopTimeout.
	StopGrace map[string]time.Duration
	Essential map[string]bool

	// A task's containers are placed one at a time while readers already hold
	// the task's processes.
	mu sync.Mutex
	// The container owning the task's network namespace, which takes the
	// task's ingress and route.
	mainContainerName string
	handles           map[string]placement.Handle
}

func newECSTaskProcesses(td ECSTaskDefinition, mainContainerName string) *ecsTaskProcesses {
	return &ecsTaskProcesses{
		StopGrace:         ecsTaskStopGrace(td),
		Essential:         ecsTaskEssential(td),
		mainContainerName: mainContainerName,
		handles:           make(map[string]placement.Handle, len(td.ContainerDefinitions)),
	}
}

// Amazon ECS treats a container whose definition omits essential as
// essential.
func ecsContainerEssential(cd ECSContainerDefinition) bool {
	return cd.Essential == nil || *cd.Essential
}

func ecsTaskEssential(td ECSTaskDefinition) map[string]bool {
	essential := make(map[string]bool, len(td.ContainerDefinitions))
	for _, cd := range td.ContainerDefinitions {
		essential[cd.Name] = ecsContainerEssential(cd)
	}
	return essential
}

func (p *ecsTaskProcesses) setHandle(name string, h placement.Handle) {
	p.mu.Lock()
	p.handles[name] = h
	p.mu.Unlock()
}

func (p *ecsTaskProcesses) setMain(name string) {
	p.mu.Lock()
	p.mainContainerName = name
	p.mu.Unlock()
}

// The handle of the container owning the task's network namespace.
func (p *ecsTaskProcesses) mainHandle() placement.Handle {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handles[p.mainContainerName]
}

func (p *ecsTaskProcesses) handleList() []placement.Handle {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]placement.Handle, 0, len(p.handles))
	for _, h := range p.handles {
		out = append(out, h)
	}
	return out
}

func (p *ecsTaskProcesses) named() map[string]placement.Handle {
	p.mu.Lock()
	defer p.mu.Unlock()
	return maps.Clone(p.handles)
}

// A running container holding the task's network namespace, the owner first;
// nil once every container placed has exited.
func (p *ecsTaskProcesses) liveNamespaceHandle() placement.Handle {
	p.mu.Lock()
	defer p.mu.Unlock()
	running := func(h placement.Handle) bool {
		if h == nil {
			return false
		}
		select {
		case <-h.Exited():
			return false
		default:
			return true
		}
	}
	if h := p.handles[p.mainContainerName]; running(h) {
		return h
	}
	names := slices.Sorted(maps.Keys(p.handles))
	for _, name := range names {
		if h := p.handles[name]; running(h) {
			return h
		}
	}
	return nil
}

// The namespace owner goes last, after every container that joined it.
func (p *ecsTaskProcesses) removalOrder() []placement.Handle {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]placement.Handle, 0, len(p.handles))
	for name, h := range p.handles {
		if name != p.mainContainerName {
			out = append(out, h)
		}
	}
	if h := p.handles[p.mainContainerName]; h != nil {
		out = append(out, h)
	}
	return out
}

// 30 seconds on Fargate and the EC2 agent's ECS_CONTAINER_STOP_TIMEOUT
// default alike.
const ecsDefaultStopTimeout = 30 * time.Second

// The definition's stopTimeout, else the platform default.
func ecsContainerStopGrace(cd ECSContainerDefinition) time.Duration {
	if cd.StopTimeout != nil && *cd.StopTimeout >= 0 {
		return time.Duration(*cd.StopTimeout) * time.Second
	}
	return ecsDefaultStopTimeout
}

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
	p.mu.Lock()
	defer p.mu.Unlock()
	if h := p.handles[p.mainContainerName]; h != nil {
		return h
	}
	for _, h := range p.handles {
		return h
	}
	return nil
}

func (p *ecsTaskProcesses) handleFor(containerName string) placement.Handle {
	if p == nil {
		return nil
	}
	if containerName == "" {
		return p.firstHandle()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handles[containerName]
}

// A lifecycle step can stop waiting for it when the simulator shuts down.
type ecsLifecycleLock chan struct{}

func (l ecsLifecycleLock) Lock() { l <- struct{}{} }

func (l ecsLifecycleLock) Unlock() { <-l }

// Reports false once ctx is done first.
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
	// Signal all containers together, each with its own stopTimeout, so the task
	// takes as long as its slowest container. A cancelled ctx ends the wait, not
	// the stop.
	var stopping sync.WaitGroup
	for _, h := range p.handleList() {
		if h == nil {
			continue
		}
		stopping.Add(1)
		go func(h placement.Handle) {
			defer stopping.Done()
			h.Cancel()
			select {
			case <-h.Exited():
			case <-ctx.Done():
			}
		}(h)
	}
	stopping.Wait()
}

// The task stays PENDING and the next process resumes it under the same
// container names, so none may outlive this one.
func ecsAbandonTaskStart(taskID string, p *ecsTaskProcesses) {
	if p == nil {
		return
	}
	for _, handle := range p.handleList() {
		if handle == nil || handle.ID() == "" {
			continue
		}
		if err := sim.RemoveContainer(handle.ID()); err != nil {
			fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: remove container of the interrupted start: %v\n", taskID, err)
		}
	}
}

// Once ctx is done, leave the rest to the next process's recovery.
func cleanupECSTaskProcesses(ctx context.Context, taskID string, p *ecsTaskProcesses) {
	if p == nil {
		return
	}
	stopECSTaskProcesses(ctx, p)
	if ctx.Err() != nil {
		return
	}
	for _, handle := range p.removalOrder() {
		if handle == nil || handle.ID() == "" {
			continue
		}
		select {
		case <-handle.Exited():
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: container %s is still being removed; the next process's recovery finishes it\n", taskID, handle.ID())
			return
		}
	}
	if err := sim.RemoveVolumesNamed(ecsTaskVolumePrefix(taskID)); err != nil {
		fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: remove its volumes: %v\n", taskID, err)
	}
	if _, vpcID, ok := ecsTaskENIGroups(taskID); ok {
		ec2ReapplyIngress(vpcID)
	}
}

func ecsTaskVolumePrefix(taskID string) string { return "sockerless-ecs-task-" + taskID + "-" }

func ecsArn(resourceType, id string) string {
	return fmt.Sprintf("arn:aws:ecs:"+awsRegion()+":"+awsAccountID()+":%s/%s", resourceType, id)
}

func registerECS(r *AWSRouter, srv *sim.Server) {
	ecsClusters = sim.MakeStore[ECSCluster](srv.DB(), "ecs_clusters")
	// Every ListTaskDefinitions call reads them all, so keep the rows in memory.
	ecsTaskDefinitions = sim.MakeCachedStore[ECSTaskDefinition](srv.DB(), "ecs_task_definitions")
	ecsTasks = sim.Announcing(sim.MakeStore[ECSTask](srv.DB(), "ecs_tasks"), ecsTaskChanges)
	ecsManagedVolumeOwners = sim.MakeStore[ecsManagedVolumeOwner](srv.DB(), "ecs_managed_volume_owners")
	ecsBackgroundServer = srv
	kvstore.StartSweeper(srv, "Amazon ECS stopped task retention", storeSweepInterval, func(now time.Time) { ecsSweepStoppedTasks(now) })
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

	registerECSServices(r, srv)

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

// Case-insensitive, as the AWS enum.
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

// Without awslogs the container still runs; its output is not captured.
type discardLogSink struct{}

func (discardLogSink) WriteLog(sim.LogLine) {}

type cwLogSink struct {
	logGroup  string
	logStream string
}

func (s *cwLogSink) WriteLog(line sim.LogLine) {
	cwIngestWorkloadLogLine(s.logGroup, s.logStream, line.Text, line.Timestamp)
}

// `family:<family>` is the documented default.
func ecsTaskGroup(requested string, td ECSTaskDefinition) string {
	if requested != "" {
		return requested
	}
	return "family:" + td.Family
}

// Strings from the container definition with the override applied; cpu is
// "0" when neither set it.
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
