package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/revisions"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
	apipb "google.golang.org/genproto/googleapis/api"
)

// enumString accepts both proto-JSON enum encodings: the canonical
// string ("INGRESS_TRAFFIC_INTERNAL_ONLY") and the numeric form (4)
// emitted by some Go REST clients (run/apiv2.NewServicesRESTClient
// serializes IngressTraffic as a number even though the wire spec
// allows both). Real Cloud Run accepts either; the sim must too.
type enumString string

func (e *enumString) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*e = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*e = enumString(s)
		return nil
	}
	// Numeric enum form — keep the digits as the string value.
	// The sim doesn't validate ingress against a known set; readers
	// only round-trip it on Get/List, so preserving the bytes works.
	*e = enumString(string(data))
	return nil
}

func (e enumString) MarshalJSON() ([]byte, error) {
	if e == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(e))
}

// launchStageString is a resource's launch stage. The REST transport of the
// Go clients sends enums as their proto numbers, and a read has to answer
// with the name the API declares, so a number maps through google.api's own
// LaunchStage table.
type launchStageString string

func (e *launchStageString) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*e = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*e = launchStageString(s)
		return nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 32)
	if err != nil {
		return fmt.Errorf("launch stage %s: %w", data, err)
	}
	name, ok := apipb.LaunchStage_name[int32(n)]
	if !ok {
		return fmt.Errorf("unknown launch stage enum %s", data)
	}
	*e = launchStageString(name)
	return nil
}

func (e launchStageString) MarshalJSON() ([]byte, error) {
	if e == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(e))
}

// ingressString is the ingress-traffic enum. Like the VPC egress one beside
// it, a client may send the proto numeric form, and the name is what a read
// has to answer with: keeping the digits put a value in the field that the
// enum does not declare, and no generated client can map "2".
type ingressString string

func (e *ingressString) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*e = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*e = ingressString(s)
		return nil
	}
	switch strings.TrimSpace(string(data)) {
	case "0":
		*e = "INGRESS_TRAFFIC_UNSPECIFIED"
	case "1":
		*e = "INGRESS_TRAFFIC_ALL"
	case "2":
		*e = "INGRESS_TRAFFIC_INTERNAL_ONLY"
	case "3":
		*e = "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER"
	case "4":
		*e = "INGRESS_TRAFFIC_NONE"
	default:
		return fmt.Errorf("unknown ingress traffic enum %s", data)
	}
	return nil
}

func (e ingressString) MarshalJSON() ([]byte, error) {
	if e == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(e))
}

type vpcEgressString string

func (e *vpcEgressString) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*e = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*e = vpcEgressString(s)
		return nil
	}
	switch strings.TrimSpace(string(data)) {
	case "1":
		*e = "ALL_TRAFFIC"
	case "2":
		*e = "PRIVATE_RANGES_ONLY"
	case "0":
		*e = ""
	default:
		return fmt.Errorf("unknown VpcAccess egress enum %s", data)
	}
	return nil
}

func (e vpcEgressString) MarshalJSON() ([]byte, error) {
	if e == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(e))
}

// Cloud Run v2 services slice. The cloudrun backend uses
// cloud.google.com/go/run/apiv2 (REST client) which talks to the v2
// REST surface — `/v2/projects/{project}/locations/{location}/services`
// — not the v1 Knative paths handled in cloudrun.go. When
// Config.UseService=true the backend hits these endpoints; without
// them every Service call 404s.
//
// Real API: https://cloud.google.com/run/docs/reference/rest/v2/projects.locations.services

// ServiceV2 is the v2 Cloud Run Service (proto-JSON shape, not the v1
// Knative shape in CRService). Field set is the subset the cloudrun
// backend reads via runpb.Service: name, labels, annotations,
// createTime, template (with containers + env), terminalCondition,
// latestReadyRevision. Generation is encoded as a JSON string per
// proto-JSON int64 rules.
type ServiceV2 struct {
	Name                  string                `json:"name"`
	UID                   string                `json:"uid,omitempty"`
	Generation            int64                 `json:"generation,string,omitempty"`
	Labels                map[string]string     `json:"labels,omitempty"`
	Annotations           map[string]string     `json:"annotations,omitempty"`
	Description           string                `json:"description,omitempty"`
	CreateTime            string                `json:"createTime,omitempty"`
	UpdateTime            string                `json:"updateTime,omitempty"`
	LaunchStage           launchStageString     `json:"launchStage,omitempty"`
	Client                string                `json:"client,omitempty"`
	ClientVersion         string                `json:"clientVersion,omitempty"`
	Ingress               ingressString         `json:"ingress,omitempty"`
	DefaultUriDisabled    bool                  `json:"defaultUriDisabled,omitempty"`
	InvokerIamDisabled    bool                  `json:"invokerIamDisabled,omitempty"`
	IapEnabled            bool                  `json:"iapEnabled,omitempty"`
	SshEnabled            bool                  `json:"sshEnabled,omitempty"`
	CustomAudiences       []string              `json:"customAudiences,omitempty"`
	BinaryAuthorization   *BinaryAuthorization  `json:"binaryAuthorization,omitempty"`
	Scaling               *ServiceScaling       `json:"scaling,omitempty"`
	BuildConfig           *ServiceBuildConfig   `json:"buildConfig,omitempty"`
	MultiRegionSettings   *MultiRegionSettings  `json:"multiRegionSettings,omitempty"`
	Template              *RevisionTemplate     `json:"template,omitempty"`
	Traffic               []TrafficTarget       `json:"traffic,omitempty"`
	TrafficStatuses       []TrafficTargetStatus `json:"trafficStatuses,omitempty"`
	TerminalCondition     *Condition            `json:"terminalCondition,omitempty"`
	Conditions            []Condition           `json:"conditions,omitempty"`
	LatestReadyRevision   string                `json:"latestReadyRevision,omitempty"`
	LatestCreatedRevision string                `json:"latestCreatedRevision,omitempty"`
	URI                   string                `json:"uri,omitempty"`
	Etag                  string                `json:"etag,omitempty"`
	Reconciling           bool                  `json:"reconciling,omitempty"`
}

// ServiceScaling mirrors GoogleCloudRunV2ServiceScaling — service-level
// scaling settings applied across revisions.
type ServiceScaling struct {
	MinInstanceCount    int32      `json:"minInstanceCount,omitempty"`
	MaxInstanceCount    int32      `json:"maxInstanceCount,omitempty"`
	ManualInstanceCount int32      `json:"manualInstanceCount,omitempty"`
	ScalingMode         enumString `json:"scalingMode,omitempty"`
}

// ServiceBuildConfig mirrors GoogleCloudRunV2BuildConfig (the SDK type is
// named BuildConfig; that spelling is taken by the Cloud Functions slice).
type ServiceBuildConfig struct {
	Name                   string            `json:"name,omitempty"`
	SourceLocation         string            `json:"sourceLocation,omitempty"`
	FunctionTarget         string            `json:"functionTarget,omitempty"`
	ImageURI               string            `json:"imageUri,omitempty"`
	BaseImage              string            `json:"baseImage,omitempty"`
	EnableAutomaticUpdates bool              `json:"enableAutomaticUpdates,omitempty"`
	WorkerPool             string            `json:"workerPool,omitempty"`
	EnvironmentVariables   map[string]string `json:"environmentVariables,omitempty"`
	ServiceAccount         string            `json:"serviceAccount,omitempty"`
}

// MultiRegionSettings mirrors GoogleCloudRunV2MultiRegionSettings.
type MultiRegionSettings struct {
	Regions       []string `json:"regions,omitempty"`
	MultiRegionID string   `json:"multiRegionId,omitempty"`
}

// RevisionTemplate is the v2 Cloud Run revision template. Mirrors the
// runpb.RevisionTemplate fields the backend's buildServiceSpec sets.
type RevisionTemplate struct {
	Labels                        map[string]string `json:"labels,omitempty"`
	Annotations                   map[string]string `json:"annotations,omitempty"`
	Revision                      string            `json:"revision,omitempty"`
	Containers                    []Container       `json:"containers,omitempty"`
	Volumes                       []Volume          `json:"volumes,omitempty"`
	Scaling                       *RevisionScaling  `json:"scaling,omitempty"`
	VpcAccess                     *VpcAccess        `json:"vpcAccess,omitempty"`
	Timeout                       string            `json:"timeout,omitempty"`
	ServiceAccount                string            `json:"serviceAccount,omitempty"`
	ExecutionEnvironment          enumString        `json:"executionEnvironment,omitempty"`
	MaxInstanceRequestConcurrency int32             `json:"maxInstanceRequestConcurrency,omitempty"`
	SessionAffinity               bool              `json:"sessionAffinity,omitempty"`
	HealthCheckDisabled           bool              `json:"healthCheckDisabled,omitempty"`
	EncryptionKey                 string            `json:"encryptionKey,omitempty"`
	EncryptionKeyRevocationAction enumString        `json:"encryptionKeyRevocationAction,omitempty"`
	EncryptionKeyShutdownDuration string            `json:"encryptionKeyShutdownDuration,omitempty"`
	GpuZonalRedundancyDisabled    bool              `json:"gpuZonalRedundancyDisabled,omitempty"`
	NodeSelector                  *NodeSelector     `json:"nodeSelector,omitempty"`
	ServiceMesh                   *ServiceMesh      `json:"serviceMesh,omitempty"`
	Client                        string            `json:"client,omitempty"`
	ClientVersion                 string            `json:"clientVersion,omitempty"`
}

type RevisionScaling struct {
	MinInstanceCount int32 `json:"minInstanceCount,omitempty"`
	MaxInstanceCount int32 `json:"maxInstanceCount,omitempty"`
}

// VpcAccess wires a service revision to a Serverless VPC Access
// connector so peer containers can reach the service over its
// internal-ingress IP. The backend sets this when Config.VPCConnector
// is non-empty.
type VpcAccess struct {
	Connector         string             `json:"connector,omitempty"`
	Egress            vpcEgressString    `json:"egress,omitempty"`
	NetworkInterfaces []NetworkInterface `json:"networkInterfaces,omitempty"`
}

// NetworkInterface mirrors google.cloud.run.v2.NetworkInterface — one direct
// VPC egress attachment on a revision.
type NetworkInterface struct {
	Network    string   `json:"network,omitempty"`
	Subnetwork string   `json:"subnetwork,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}

// TrafficTarget is one entry in the Service's traffic-split list.
type TrafficTarget struct {
	Type     trafficAllocationType `json:"type,omitempty"`
	Revision string                `json:"revision,omitempty"`
	Percent  int32                 `json:"percent,omitempty"`
	Tag      string                `json:"tag,omitempty"`
}

// TrafficTargetStatus mirrors GoogleCloudRunV2TrafficTargetStatus: where the
// service's traffic goes, with the URL a tagged target answers on.
type TrafficTargetStatus struct {
	Type     trafficAllocationType `json:"type,omitempty"`
	Revision string                `json:"revision,omitempty"`
	Percent  int32                 `json:"percent,omitempty"`
	Tag      string                `json:"tag,omitempty"`
	URI      string                `json:"uri,omitempty"`
}

// RevisionV2 mirrors the subset of google.cloud.run.v2.Revision the sim
// materializes per service deploy. Cloud Run creates an immutable Revision
// for every Service generation; the sim records one so the
// services.revisions get/list endpoints return faithful data.
type RevisionV2 struct {
	Name        string            `json:"name"`
	UID         string            `json:"uid,omitempty"`
	Generation  int64             `json:"generation,string,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	CreateTime  string            `json:"createTime,omitempty"`
	UpdateTime  string            `json:"updateTime,omitempty"`
	LaunchStage launchStageString `json:"launchStage,omitempty"`
	Service     string            `json:"service,omitempty"`
	Scaling     *RevisionScaling  `json:"scaling,omitempty"`
	VpcAccess   *VpcAccess        `json:"vpcAccess,omitempty"`
	Timeout     string            `json:"timeout,omitempty"`
	Containers  []Container       `json:"containers,omitempty"`
	Volumes     []Volume          `json:"volumes,omitempty"`
	// The revision runs as the service account its template named.
	ServiceAccount                string      `json:"serviceAccount,omitempty"`
	ExecutionEnvironment          enumString  `json:"executionEnvironment,omitempty"`
	MaxInstanceRequestConcurrency int32       `json:"maxInstanceRequestConcurrency,omitempty"`
	SessionAffinity               bool        `json:"sessionAffinity,omitempty"`
	EncryptionKey                 string      `json:"encryptionKey,omitempty"`
	Conditions                    []Condition `json:"conditions,omitempty"`
	Etag                          string      `json:"etag,omitempty"`
	Reconciling                   bool        `json:"reconciling,omitempty"`
}

func containerEnvMap(envVars []EnvVar) map[string]string {
	if len(envVars) == 0 {
		return nil
	}
	env := make(map[string]string, len(envVars))
	for _, ev := range envVars {
		env[ev.Name] = ev.Value
	}
	return env
}

type cloudRunServiceInstance struct {
	specSig string
	// stop ends the start of the instance's containers and its startup
	// probes.
	stop context.CancelFunc
	// ready closes once every container has started and passed its startup
	// probe, or one failed; address and startErr are written before it closes
	// and read only after.
	ready    chan struct{}
	address  string
	startErr error
	// releaseMounts ends the ingestion of writes through the instance's
	// Cloud Storage volumes once its containers have stopped.
	releaseMounts func()

	mu sync.Mutex
	// owner is the container that owns the instance's network namespace, the
	// first to start, and publishes the ingress port.
	owner   placement.Handle
	ingress placement.Handle
	handles []placement.Handle
	// group is the instance's containers once they all started.
	group   *workload.Group
	stopped bool
}

// awaitReady waits for the instance's startup probes and returns the address
// its ingress container answers on.
func (inst *cloudRunServiceInstance) awaitReady(ctx context.Context) (string, error) {
	select {
	case <-inst.ready:
		return inst.address, inst.startErr
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// placed records a container the instance placed, or cancels it when the
// instance stopped while it started.
func (inst *cloudRunServiceInstance) placed(owner bool, ingress bool, h placement.Handle) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if owner {
		inst.owner = h
	}
	if ingress {
		inst.ingress = h
	}
	inst.handles = append(inst.handles, h)
	if inst.stopped {
		h.Cancel()
	}
}

var cloudRunServiceInstances = struct {
	sync.Mutex
	byName map[string]*cloudRunServiceInstance
}{byName: map[string]*cloudRunServiceInstance{}}

// ensureCloudRunServiceInstance returns the Cloud Run instance's running
// containers, or starts them.
func ensureCloudRunServiceInstance(ctx context.Context, name, serviceID string, vpc *VpcAccess, containers []Container, volumes []Volume, sink sim.LogSink) (*cloudRunServiceInstance, error) {
	specSig := serviceContainersSignature(containers, volumes)

	cloudRunServiceInstances.Lock()
	if inst := cloudRunServiceInstances.byName[name]; inst != nil && inst.specSig == specSig {
		cloudRunServiceInstances.Unlock()
		return inst, nil
	}
	old := cloudRunServiceInstances.byName[name]
	delete(cloudRunServiceInstances.byName, name)
	cloudRunServiceInstances.Unlock()
	stopCloudRunServiceInstance(old)

	inst, err := startCloudRunServiceInstance(ctx, name, serviceID, sim.RandomHex(8), vpc, containers, volumes, sink)
	if err != nil {
		return nil, err
	}
	cloudRunServiceInstances.Lock()
	if old := cloudRunServiceInstances.byName[name]; old != nil {
		cloudRunServiceInstances.Unlock()
		stopCloudRunServiceInstance(inst)
		return nil, fmt.Errorf("instance %q replaced while starting", name)
	}
	cloudRunServiceInstances.byName[name] = inst
	cloudRunServiceInstances.Unlock()
	return inst, nil
}

// startCloudRunServiceInstance starts the containers of instance instanceID
// of name, a service revision or a Cloud Run instance: the container that
// owns its network namespace at once, and the others and the startup probes
// in the background, in dependsOn order; awaitReady waits for them.
func startCloudRunServiceInstance(ctx context.Context, name, serviceID, instanceID string, vpc *VpcAccess, containers []Container, volumes []Volume, sink sim.LogSink) (*cloudRunServiceInstance, error) {
	specSig := serviceContainersSignature(containers, volumes)
	order, err := cloudRunContainerStartOrder(containers)
	if err != nil {
		return nil, err
	}
	project := resourceProject(name)
	bindsFor := serviceBindsFor(volumes)
	binds := make([][]string, len(containers))
	auths := make([]string, len(containers))
	var writable []string
	for i, c := range containers {
		containerBinds, containerWritable, err := bindsFor(c)
		if err != nil {
			return nil, err
		}
		binds[i] = containerBinds
		writable = append(writable, containerWritable...)
		if auths[i], err = workloadRegistryAuth(project, sim.ResolveLocalImage(c.Image)); err != nil {
			return nil, err
		}
		// Cloud Run resolves a revision's images before it places an
		// instance, so a missing image is what a deploy reports, whatever
		// network the instance would join.
		if err := sim.PullImageWithCredential(ctx, sim.ResolveLocalImage(c.Image), "", auths[i]); err != nil {
			return nil, err
		}
	}
	netw, err := gcpCloudRunNetwork(ctx, project, resourceLocation(name), vpc, fmt.Sprintf("sockerless-sim-cloudrun-svc-%s-%s", serviceID, instanceID))
	if err != nil {
		return nil, err
	}
	mounts, err := gcsAcquireMounts(writable)
	if err != nil {
		netw.done()
		return nil, err
	}
	releaseMounts := func() {
		mounts()
		netw.done()
	}
	mountsHeld := true
	defer func() {
		if mountsHeld {
			releaseMounts()
		}
	}()
	metadataEnv := netw.env()
	// A service revision's containers learn which service, revision and
	// configuration they serve.
	if serviceName, revisionID, isRevision := strings.Cut(name, "/revisions/"); isRevision {
		serviceID := serviceName[strings.LastIndex(serviceName, "/")+1:]
		metadataEnv = workloadhost.MergeEnv(metadataEnv, map[string]string{
			"K_SERVICE": serviceID, "K_REVISION": revisionID, "K_CONFIGURATION": serviceID,
		})
	}
	members := make([]workload.Member, len(containers))
	for i, c := range containers {
		env := workloadhost.MergeEnv(containerEnvMap(c.Env), metadataEnv)
		containerName := fmt.Sprintf("sockerless-sim-cloudrun-svc-%s-%s", serviceID, instanceID)
		labels := map[string]string{
			cloudRunServiceLabel:   serviceID,
			cloudRunResourceLabel:  name,
			cloudRunContainerLabel: c.Name,
			cloudRunGroupLabel:     instanceID,
			cloudRunSpecLabel:      cloudRunSpecDigest(specSig),
		}
		if i == 0 {
			env = workloadhost.MergeEnv(map[string]string{"PORT": strconv.Itoa(cloudRunContainerPort(c))}, containerEnvMap(c.Env), metadataEnv)
		} else {
			containerName = fmt.Sprintf("sockerless-sim-cloudrun-svc-%s-sidecar-%d-%s", serviceID, i-1, instanceID)
			labels["sockerless-sim-service-container"] = c.Name
		}
		members[i] = workload.Member{
			Container: workload.Container{Name: c.Name, Config: sim.ContainerConfig{
				CancelGracePeriod: cloudRunStopGrace,
				Image:             sim.ResolveLocalImage(c.Image),
				RegistryAuth:      auths[i],
				Command:           c.Command,
				Args:              c.Args,
				Env:               env,
				Name:              containerName,
				Labels:            labels,
				Binds:             binds[i],
				Sandbox:           SandboxCloudRun,
			}},
			DependsOn: c.DependsOn,
		}
		if c.StartupProbe != nil {
			members[i].Startup = cloudRunMemberStartup(c)
		}
	}

	backend, err := gcpPlacement.For(placementRun)
	if err != nil {
		return nil, err
	}
	lifeCtx, stop := context.WithCancel(context.Background())
	inst := &cloudRunServiceInstance{
		specSig:       specSig,
		stop:          stop,
		ready:         make(chan struct{}),
		releaseMounts: releaseMounts,
	}
	mountsHeld = false

	// The first container to start owns the network namespace the others
	// join, and with it the published ingress port, the network the
	// instance sits on and the hosts file the metadata server's name
	// resolves in. The ingress container always runs its startup probe,
	// Cloud Run's default TCP probe when it configures none.
	owner := order[0]
	ingressPort := cloudRunContainerPort(containers[0])
	members[0].Startup = cloudRunMemberStartup(containers[0])
	bg.Go(func() {
		defer close(inst.ready)
		group, err := workload.StartGroup(lifeCtx, backend, members, workload.StartOptions{
			Sink: func(int) sim.LogSink { return sink },
			Owner: func(cfg *sim.ContainerConfig) {
				netw.apply(cfg)
				cfg.PublishPorts = cloudRunNamespacePorts(containers, true)
			},
			Placed: func(i int, h placement.Handle) { inst.placed(i == owner, i == 0, h) },
		})
		if err != nil {
			inst.startErr = err
			return
		}
		inst.mu.Lock()
		ownerHandle := inst.owner
		inst.group = group
		inst.mu.Unlock()
		inst.address, inst.startErr = cloudRunContainerRoute(lifeCtx, ownerHandle, ingressPort)
	})
	return inst, nil
}

// stopUnroutedCloudRunRevisions stops the instances of the service's
// revisions its traffic no longer reaches, by weight or by tag.
func stopUnroutedCloudRunRevisions(svc ServiceV2) {
	stopCloudRunRevisionInstances(svc.Name, cloudRunRoutedRevisions(svc))
}

func deleteCloudRunServiceInstance(name string) {
	cloudRunServiceInstances.Lock()
	inst := cloudRunServiceInstances.byName[name]
	delete(cloudRunServiceInstances.byName, name)
	cloudRunServiceInstances.Unlock()
	stopCloudRunServiceInstance(inst)
}

// deleteCloudRunServiceInstanceIf stops inst and forgets it, unless the
// service has already moved on to another instance.
func deleteCloudRunServiceInstanceIf(name string, inst *cloudRunServiceInstance) {
	cloudRunServiceInstances.Lock()
	if cloudRunServiceInstances.byName[name] != inst {
		cloudRunServiceInstances.Unlock()
		return
	}
	delete(cloudRunServiceInstances.byName, name)
	cloudRunServiceInstances.Unlock()
	stopCloudRunServiceInstance(inst)
}

// stopCloudRunServiceInstance ends the instance's start, stops its containers
// with Cloud Run's grace, and returns once every one has exited, so what the
// containers write as they stop still reaches Cloud Logging before their
// mounts are released.
func stopCloudRunServiceInstance(inst *cloudRunServiceInstance) {
	if inst == nil {
		return
	}
	inst.mu.Lock()
	inst.stopped = true
	inst.mu.Unlock()
	if inst.stop != nil {
		inst.stop()
	}
	<-inst.ready
	inst.mu.Lock()
	handles := inst.handles
	inst.handles = nil
	inst.mu.Unlock()
	for _, h := range handles {
		h.Cancel()
	}
	for _, h := range handles {
		<-h.Exited()
	}
	if inst.releaseMounts != nil {
		inst.releaseMounts()
	}
}

func envSignature(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(env[k])
		b.WriteByte('\x00')
	}
	return b.String()
}

func serviceBindsFor(volumes []Volume) func(Container) ([]string, []string, error) {
	volByName := make(map[string]Volume)
	for _, v := range volumes {
		volByName[v.Name] = v
	}
	return func(c Container) ([]string, []string, error) {
		return cloudRunGCSBinds(volByName, c)
	}
}

func volumesSignature(volumes []Volume) string {
	if len(volumes) == 0 {
		return ""
	}
	var parts []string
	for _, v := range volumes {
		if v.Gcs != nil {
			parts = append(parts, v.Name+"|gcs|"+v.Gcs.Bucket+"|"+strconv.FormatBool(v.Gcs.ReadOnly)+"|"+strings.Join(v.Gcs.MountOptions, ";"))
		} else if v.Nfs != nil {
			parts = append(parts, v.Name+"|nfs|"+v.Nfs.Server+"|"+v.Nfs.Path+"|"+strconv.FormatBool(v.Nfs.ReadOnly))
		} else if v.Secret != nil {
			parts = append(parts, v.Name+"|secret|"+v.Secret.Secret)
		} else {
			parts = append(parts, v.Name+"|empty")
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x00")
}

func volumeMountsSignature(mounts []VolumeMount) string {
	if len(mounts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(mounts))
	for _, m := range mounts {
		parts = append(parts, m.Name+"="+m.MountPath)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x00")
}

func serviceContainersSignature(containers []Container, volumes []Volume) string {
	var parts []string
	for _, c := range containers {
		env := containerEnvMap(c.Env)
		probe, err := json.Marshal(c.StartupProbe)
		if err != nil {
			panic(err)
		}
		parts = append(parts, c.Name+"|"+sim.ResolveLocalImage(c.Image)+"|"+strings.Join(c.Command, "\x00")+"|"+strings.Join(c.Args, "\x00")+"|"+envSignature(env)+"|"+volumeMountsSignature(c.VolumeMounts)+
			"|"+strconv.Itoa(cloudRunContainerPort(c))+"|"+strings.Join(c.DependsOn, "\x00")+"|"+string(probe))
	}
	return strings.Join(parts, "\x01") + "\x02" + volumesSignature(volumes)
}

// crv2Services is the package-scope handle the cloudfunctions slice
// uses to auto-create the backing Cloud Run service when a Cloud
// Functions Gen2 function is created. Real GCP wires the two services
// together server-side; the sim mirrors that linkage so backends that
// expect `function.ServiceConfig.Service` to resolve to a real
// `runpb.Service` (e.g. the gcf overlay-and-swap path) work end-to-end.
var crv2Services sim.Store[ServiceV2]

// seedServiceV2Defaults stamps the immutable identity + initial-rollout
// fields onto a freshly-created service. Real Cloud Run does this
// server-side (UID, generation 1, Ready condition, default URI, first
// revision); the sim mirrors it for both REST CreateService and the
// cloudfunctions auto-wire path so a single source of truth controls
// the shape of "just-created" services.
func seedServiceV2Defaults(svc ServiceV2, project, location, serviceID string) ServiceV2 {
	now := nowTimestamp()
	svc.Name = fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, serviceID)
	svc.UID = sim.NewUUID()
	svc.Generation = 1
	svc.CreateTime = now
	svc.UpdateTime = now
	if svc.LaunchStage == "" {
		svc.LaunchStage = "GA"
	}
	svc.TerminalCondition = &Condition{
		Type:               "Ready",
		State:              "CONDITION_SUCCEEDED",
		LastTransitionTime: now,
	}
	svc.Conditions = []Condition{
		{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: now},
	}
	svc.LatestCreatedRevision = svc.Name + "/revisions/" + cloudRunServiceRevisionID(serviceID, svc.Generation, svc.Template)
	svc.LatestReadyRevision = svc.LatestCreatedRevision
	if !svc.DefaultUriDisabled {
		svc.URI = cloudRunServiceURI(project, location, serviceID)
	}
	settleCloudRunServiceTraffic(&svc)
	return svc
}

// reconcileServiceRevision materializes the immutable Revision a Service
// deploy produces. revName is the bare revision id (e.g. "svc-00001-abc");
// the stored record is keyed by its full resource name.
func reconcileServiceRevision(store sim.Store[RevisionV2], serviceName, revName string, svc ServiceV2) {
	now := nowTimestamp()
	full := serviceName + "/revisions/" + revName
	rev := RevisionV2{
		Name:        full,
		UID:         sim.NewUUID(),
		Generation:  svc.Generation,
		CreateTime:  now,
		UpdateTime:  now,
		LaunchStage: svc.LaunchStage,
		Service:     serviceName,
		Conditions: []Condition{
			{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: now},
		},
		// A revision is immutable, but a fresh deploy replaces the record under
		// the same name, so the fingerprint is minted with it.
		Etag: sim.NewUUID(),
	}
	if svc.Template != nil {
		rev.Labels = svc.Template.Labels
		rev.Annotations = svc.Template.Annotations
		rev.Containers = svc.Template.Containers
		rev.Volumes = svc.Template.Volumes
		rev.Scaling = svc.Template.Scaling
		rev.VpcAccess = svc.Template.VpcAccess
		rev.Timeout = svc.Template.Timeout
		rev.ServiceAccount = svc.Template.ServiceAccount
		rev.ExecutionEnvironment = svc.Template.ExecutionEnvironment
		rev.MaxInstanceRequestConcurrency = svc.Template.MaxInstanceRequestConcurrency
		rev.SessionAffinity = svc.Template.SessionAffinity
		rev.EncryptionKey = svc.Template.EncryptionKey
	}
	store.Put(full, rev)
}

// createCloudRunService stores a new service and the revision its template
// deploys, whichever surface wrote it.
func createCloudRunService(w http.ResponseWriter, svc ServiceV2, project, location, serviceID string) (ServiceV2, bool) {
	name := fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, serviceID)
	// Cloud Run regional CPU quota check. When SIM_GCP_CPU_QUOTA_PER_REGION
	// is set, each fresh revision deploy debits its CPU load against the
	// per-(project, region) sliding-window budget. Reproduces /
	// deterministically — the live cloud rejects with this same
	// error when the regional cpu_allocation quota is exhausted.
	if !regionalCPUQuotaInstance.tryDebit(project, location, serviceCPULoad(svc)) {
		regionalCPUQuotaErrorJSON(w, name)
		return ServiceV2{}, false
	}
	svc = seedServiceV2Defaults(svc, project, location, serviceID)
	svc.Etag = sim.NewUUID()
	crv2Services.Put(name, svc)
	reconcileServiceRevision(crv2Revisions, name, svc.LatestCreatedRevision[strings.LastIndex(svc.LatestCreatedRevision, "/")+1:], svc)
	projectCloudRunV2ToV1(svc)
	keepCloudRunRevisionsWarm(svc)
	return svc, true
}

// updateCloudRunService stores update as existing's next generation, whichever
// surface wrote it: a changed template deploys a revision, under a name no
// revision of the service holds, and the traffic must name revisions it has.
func updateCloudRunService(w http.ResponseWriter, existing, update ServiceV2) (ServiceV2, bool) {
	project, location, serviceID, _ := parseCloudRunV2ServiceName(existing.Name)
	if !cloudRunServiceTemplateValid(w, update.Template) {
		return ServiceV2{}, false
	}
	templateChanged := cloudRunTemplateChanged(existing.Template, update.Template)
	if templateChanged && !cloudRunRevisionNameFree(w, crv2Revisions, existing.Name, serviceID, update.Template) {
		return ServiceV2{}, false
	}
	if !cloudRunServiceTrafficValid(w, crv2Revisions, existing.Name, update.Traffic, update.Template, templateChanged) {
		return ServiceV2{}, false
	}

	// A new revision's CPU load is charged against the regional
	// sliding-window quota — a quota-exhausted UpdateService is the
	// failure mode behind the Cloud Run functions overlay-and-swap path,
	// which replaces the build output with the runtime overlay image.
	if templateChanged && !regionalCPUQuotaInstance.tryDebit(project, location, serviceCPULoad(update)) {
		regionalCPUQuotaErrorJSON(w, existing.Name)
		return ServiceV2{}, false
	}

	// Preserve identity fields; allow template / labels / annotations / ingress to change.
	update.Name = existing.Name
	update.UID = existing.UID
	update.CreateTime = existing.CreateTime
	update.Generation = existing.Generation
	if update.LaunchStage == "" {
		update.LaunchStage = existing.LaunchStage
	}
	update.URI = existing.URI
	update.LatestCreatedRevision = existing.LatestCreatedRevision
	update.LatestReadyRevision = existing.LatestReadyRevision
	return rollOutServiceRevision(update, templateChanged), true
}

// rollOutServiceRevision stores a service whose spec changed as its next
// generation. A changed template deploys a new immutable revision; a change
// to anything else, the traffic among them, deploys none.
func rollOutServiceRevision(svc ServiceV2, templateChanged bool) ServiceV2 {
	serviceID := svc.Name[strings.LastIndex(svc.Name, "/")+1:]
	svc.Generation++
	svc.UpdateTime = nowTimestamp()
	svc.TerminalCondition = &Condition{
		Type:               "Ready",
		State:              "CONDITION_SUCCEEDED",
		LastTransitionTime: svc.UpdateTime,
	}
	if templateChanged {
		revName := cloudRunServiceRevisionID(serviceID, svc.Generation, svc.Template)
		svc.LatestCreatedRevision = svc.Name + "/revisions/" + revName
		svc.LatestReadyRevision = svc.LatestCreatedRevision
		reconcileServiceRevision(crv2Revisions, svc.Name, revName, svc)
	}
	settleCloudRunServiceTraffic(&svc)
	svc.Etag = sim.NewUUID()
	crv2Services.Put(svc.Name, svc)
	projectCloudRunV2ToV1(svc)
	stopUnroutedCloudRunRevisions(svc)
	keepCloudRunRevisionsWarm(svc)
	return svc
}

// cloudRunServiceRevisionID names the revision a service generation's template
// deploys: the template's revision when it names one, otherwise the service
// ID, the generation in five digits and three random characters.
func cloudRunServiceRevisionID(serviceID string, generation int64, tmpl *RevisionTemplate) string {
	if tmpl != nil && tmpl.Revision != "" {
		return tmpl.Revision
	}
	return fmt.Sprintf("%s-%05d-%s", serviceID, generation, gcpRandomSuffix(3))
}

// cloudRunTemplateChanged reports whether a write changes the revision
// template, which is what deploys a new revision.
func cloudRunTemplateChanged(before, after *RevisionTemplate) bool {
	left, errLeft := revisions.Signature(before)
	right, errRight := revisions.Signature(after)
	return errLeft != nil || errRight != nil || left != right
}

// removeCloudRunServiceV2 deletes a service, its revisions and its v1
// projections, and stops its running instance.
func removeCloudRunServiceV2(project, location, serviceID string) {
	name := fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, serviceID)
	crv2Services.Delete(name)
	deleteCloudRunServiceProjections(project, location, serviceID)
	stopCloudRunRevisionInstances(name, nil)
	revPrefix := name + "/revisions/"
	for _, rev := range crv2Revisions.Filter(func(r RevisionV2) bool { return strings.HasPrefix(r.Name, revPrefix) }) {
		crv2Revisions.Delete(rev.Name)
	}
}

func registerCloudRunServicesV2(srv *sim.Server) {
	services := sim.MakeStore[ServiceV2](srv.DB(), "crv2_services")
	crv2Services = services
	revisionStore := sim.MakeStore[RevisionV2](srv.DB(), "crv2_revisions")
	crv2Revisions = revisionStore
	if crOperations == nil {
		crOperations = sim.MakeStore[Operation](srv.DB(), "operations")
	}

	// CreateService: POST /v2/projects/{project}/locations/{location}/services?serviceId=<id>
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/services", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		serviceID := r.URL.Query().Get("serviceId")
		if serviceID == "" {
			GCPError(w, http.StatusBadRequest, "serviceId query parameter is required", "INVALID_ARGUMENT")
			return
		}

		var svc ServiceV2
		if err := sim.ReadJSON(r, &svc); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if !cloudRunServiceTemplateValid(w, svc.Template) {
			return
		}

		name := fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, serviceID)
		if _, exists := services.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "service %q already exists", name)
			return
		}

		svc, ok := createCloudRunService(w, svc, project, location, serviceID)
		if !ok {
			return
		}

		lro := cloudRunLRO(project, location, svc, "type.googleapis.com/google.cloud.run.v2.Service")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// GetService: GET /v2/projects/{project}/locations/{location}/services/{service}
	// The {service} wildcard also captures the GET-side IAM verb
	// `{service}:getIamPolicy` (Go's mux can't spell `{id}:verb`); split
	// on the colon and dispatch to the shared IAM handler.
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/services/{service}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		serviceParam := sim.PathParam(r, "service")
		if id, action, found := strings.Cut(serviceParam, ":"); found {
			if action == "getIamPolicy" {
				handleResourceIAM(w, r, gcpResourceIAMStore(),
					fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, id), action)
				return
			}
			if cloudRunLocationExportHandled(w, r,
				fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, id), action) {
				return
			}
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on service %q", action, id)
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, serviceParam)
		svc, ok := services.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "service %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, svc)
	})

	// ListServices: GET /v2/projects/{project}/locations/{location}/services
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/services", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		prefix := fmt.Sprintf("projects/%s/locations/%s/services/", project, location)
		result := services.Filter(func(s ServiceV2) bool {
			return strings.HasPrefix(s.Name, prefix)
		})
		if result == nil {
			result = []ServiceV2{}
		}
		sortCloudRunServices(result)
		page, next, ok := paginateList(w, r, result, gcpAsc(func(s ServiceV2) string { return s.Name }))
		if !ok {
			return
		}
		resp := map[string]any{"services": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// DeleteService: DELETE /v2/projects/{project}/locations/{location}/services/{service}
	srv.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/services/{service}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		serviceID := sim.PathParam(r, "service")
		name := fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, serviceID)
		svc, ok := services.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "service %q not found", name)
			return
		}
		if !cloudRunEtagOK(w, "service", svc.Name, svc.Etag, r.URL.Query().Get("etag")) {
			return
		}
		removeCloudRunServiceV2(project, location, serviceID)
		lro := cloudRunLRO(project, location, svc, "type.googleapis.com/google.cloud.run.v2.Service")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// UpdateService is not invoked by sockerless today (the backend
	// recreates services rather than patching them). Implement it
	// anyway so terraform's `google_cloud_run_v2_service` resource
	// round-trips against the sim — every cloud-API call sockerless
	// or its declarative-driver counterparts touch must be implemented
	// at fidelity.
	srv.HandleFunc("PATCH /v2/projects/{project}/locations/{location}/services/{service}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		serviceID := sim.PathParam(r, "service")
		name := fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, serviceID)

		existing, ok := services.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "service %q not found", name)
			return
		}

		var update ServiceV2
		if err := sim.ReadJSON(r, &update); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// The etag is read off the request as sent, before any mask merge
		// rewrites the body, so a mask that does not name it cannot smuggle
		// the condition away.
		if !cloudRunEtagOK(w, "service", existing.Name, existing.Etag, update.Etag) {
			return
		}

		// Honor updateMask: real Cloud Run v2 merges only the masked
		// fields into the existing service. A top-level mask like
		// "template" replaces the whole template; a sub-path mask like
		// "template.containers" replaces only that leaf, preserving the
		// rest of the template. A mask naming an unknown or output-only
		// field is rejected with 400 INVALID_ARGUMENT.
		// terraform-provider-google always sends a mask; an absent mask
		// replaces all mutable fields.
		if mask := r.URL.Query().Get("updateMask"); mask != "" {
			merged, err := applyServiceUpdateMask(existing, update, mask)
			if err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
				return
			}
			update = merged
		}
		update, ok = updateCloudRunService(w, existing, update)
		if !ok {
			return
		}
		lro := cloudRunLRO(project, location, update, "type.googleapis.com/google.cloud.run.v2.Service")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/services/{service}/revisions/{revision}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		serviceID := sim.PathParam(r, "service")
		revisionID := sim.PathParam(r, "revision")
		name := fmt.Sprintf("projects/%s/locations/%s/services/%s/revisions/%s", project, location, serviceID, revisionID)
		rev, ok := revisionStore.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "revision %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, rev)
	})
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/services/{service}/revisions", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		serviceID := sim.PathParam(r, "service")
		prefix := fmt.Sprintf("projects/%s/locations/%s/services/%s/revisions/", project, location, serviceID)
		result := revisionStore.Filter(func(rev RevisionV2) bool { return strings.HasPrefix(rev.Name, prefix) })
		if result == nil {
			result = []RevisionV2{}
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		page, next, ok := paginateList(w, r, result, gcpAsc(func(v RevisionV2) string { return v.Name }))
		if !ok {
			return
		}
		resp := map[string]any{"revisions": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
	srv.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/services/{service}/revisions/{revision}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		serviceID := sim.PathParam(r, "service")
		revisionID := sim.PathParam(r, "revision")
		name := fmt.Sprintf("projects/%s/locations/%s/services/%s/revisions/%s", project, location, serviceID, revisionID)
		rev, ok := revisionStore.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "revision %q not found", name)
			return
		}
		if !cloudRunEtagOK(w, "revision", rev.Name, rev.Etag, r.URL.Query().Get("etag")) {
			return
		}
		revisionStore.Delete(name)
		lro := cloudRunLRO(project, location, rev, "type.googleapis.com/google.cloud.run.v2.Revision")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// getIamPolicy rides the GET service handler's colon-split above.
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/services/{serviceAction}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		serviceAction := sim.PathParam(r, "serviceAction")
		id, action, found := strings.Cut(serviceAction, ":")
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action on service %q", serviceAction)
			return
		}
		switch action {
		case "setIamPolicy", "testIamPermissions":
			handleResourceIAM(w, r, gcpResourceIAMStore(),
				fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, id), action)
		case "exportImage":
			cloudRunExportImage(w, r,
				fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, id))
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on service %q", action, id)
		}
	})

	// The colon-verb fan-in on the projects/{project}/locations/{location}
	// operation collection. That URI is one collection for every service the
	// simulator serves under it — Cloud Run, API Gateway, Artifact Registry,
	// Cloud Build's regional operations, Eventarc, Memorystore for Redis and
	// Cloud Logging's project scope all address their operations there — so
	// this one handler dispatches both custom methods those documents declare
	// on it: google.longrunning WaitOperation and CancelOperation.
	operationVerb := func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		opAction := sim.PathParam(r, "opAction")
		opID, action, found := strings.Cut(opAction, ":")
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown operation action %q", opAction)
			return
		}
		name := gcpLocationOperationName(project, location, opID)
		switch action {
		case "wait":
			timeout, err := gcpWaitOperationTimeout(r)
			if err != nil {
				GCPError(w, http.StatusBadRequest, err.Error(), "INVALID_ARGUMENT")
				return
			}
			op, ok := gcpAwaitOperation(r.Context(), name, timeout)
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
				return
			}
			sim.WriteJSON(w, http.StatusOK, op)
		case "cancel":
			// A regional build's operation is a view of the build record;
			// cancelling it cancels the build.
			if build, ok := cbLocationBuild(project, location, opID); ok {
				handleCloudBuildCancelOperation(w, build.ID)
				return
			}
			handleGCPCancelOperation(w, name)
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown operation action %q", opAction)
		}
	}
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/operations/{opAction}", operationVerb)
	// The Cloud Run Admin v1 API publishes the same google.longrunning
	// WaitOperation and CancelOperation over the operation records both API
	// versions share. One collection, two spellings.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/operations/{opAction}", operationVerb)

	registerCloudRunFrontEnd(srv)
}
