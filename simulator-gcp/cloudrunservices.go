package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
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
	Name                  string               `json:"name"`
	UID                   string               `json:"uid,omitempty"`
	Generation            int64                `json:"generation,string,omitempty"`
	Labels                map[string]string    `json:"labels,omitempty"`
	Annotations           map[string]string    `json:"annotations,omitempty"`
	Description           string               `json:"description,omitempty"`
	CreateTime            string               `json:"createTime,omitempty"`
	UpdateTime            string               `json:"updateTime,omitempty"`
	LaunchStage           enumString           `json:"launchStage,omitempty"`
	Client                string               `json:"client,omitempty"`
	ClientVersion         string               `json:"clientVersion,omitempty"`
	Ingress               enumString           `json:"ingress,omitempty"`
	DefaultUriDisabled    bool                 `json:"defaultUriDisabled,omitempty"`
	InvokerIamDisabled    bool                 `json:"invokerIamDisabled,omitempty"`
	IapEnabled            bool                 `json:"iapEnabled,omitempty"`
	SshEnabled            bool                 `json:"sshEnabled,omitempty"`
	CustomAudiences       []string             `json:"customAudiences,omitempty"`
	BinaryAuthorization   *BinaryAuthorization `json:"binaryAuthorization,omitempty"`
	Scaling               *ServiceScaling      `json:"scaling,omitempty"`
	BuildConfig           *ServiceBuildConfig  `json:"buildConfig,omitempty"`
	MultiRegionSettings   *MultiRegionSettings `json:"multiRegionSettings,omitempty"`
	Template              *RevisionTemplate    `json:"template,omitempty"`
	Traffic               []TrafficTarget      `json:"traffic,omitempty"`
	TerminalCondition     *Condition           `json:"terminalCondition,omitempty"`
	Conditions            []Condition          `json:"conditions,omitempty"`
	LatestReadyRevision   string               `json:"latestReadyRevision,omitempty"`
	LatestCreatedRevision string               `json:"latestCreatedRevision,omitempty"`
	URI                   string               `json:"uri,omitempty"`
	Reconciling           bool                 `json:"reconciling,omitempty"`
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

// RevisionScaling caps min/max instance counts for a Cloud Run service
// revision. The backend pins both to 1 today (long-running, single-
// instance pattern) but the proto-JSON shape always carries them.
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
	Type     string `json:"type,omitempty"`
	Revision string `json:"revision,omitempty"`
	Percent  int32  `json:"percent,omitempty"`
	Tag      string `json:"tag,omitempty"`
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
	LaunchStage enumString        `json:"launchStage,omitempty"`
	Service     string            `json:"service,omitempty"`
	Scaling     *RevisionScaling  `json:"scaling,omitempty"`
	VpcAccess   *VpcAccess        `json:"vpcAccess,omitempty"`
	Timeout     string            `json:"timeout,omitempty"`
	Containers  []Container       `json:"containers,omitempty"`
	Volumes     []Volume          `json:"volumes,omitempty"`
	Conditions  []Condition       `json:"conditions,omitempty"`
	Reconciling bool              `json:"reconciling,omitempty"`
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
	containerID string
	containerIP string // bridge IP — preferred when the sim runs in a container
	sidecars    []*sim.ContainerHandle
	hostPort    int
	specSig     string
	cancelLogs  context.CancelFunc
}

var cloudRunServiceInstances = struct {
	sync.Mutex
	byName map[string]*cloudRunServiceInstance
}{byName: map[string]*cloudRunServiceInstance{}}

func ensureCloudRunServiceInstance(ctx context.Context, name, serviceID string, containers []Container, volumes []Volume, sink sim.LogSink) (*cloudRunServiceInstance, error) {
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

	main := containers[0]
	localImage := sim.ResolveLocalImage(main.Image)
	env := containerEnvMap(main.Env)
	bindsFor := serviceBindsFor(volumes)
	platform, err := localImagePlatform(ctx, localImage)
	if err != nil {
		return nil, err
	}
	hostPort, err := pickFreeTCPPort()
	if err != nil {
		return nil, fmt.Errorf("pick free port: %w", err)
	}
	containerID, err := sim.StartHTTPContainer(ctx, sim.HTTPContainerConfig{
		Image:        localImage,
		Architecture: platform,
		HostPort:     hostPort,
		Command:      main.Command,
		Args:         main.Args,
		Env: mergeEnv(mergeEnv(map[string]string{
			"PORT": "8080",
		}, env), hostMetadataEnv()),
		Name:       fmt.Sprintf("sockerless-sim-cloudrun-svc-%s-%d", serviceID, hostPort),
		Labels:     map[string]string{"sockerless-sim-service": serviceID},
		Binds:      bindsFor(main),
		ExtraHosts: hostMetadataExtraHosts(),
		Sandbox:    sim.SandboxCloudRun,
	})
	if err != nil {
		return nil, fmt.Errorf("start service container: %w", err)
	}

	logCtx, cancelLogs := context.WithCancel(context.Background())
	instanceStored := false
	defer func() {
		if !instanceStored {
			cancelLogs()
		}
	}()
	go sim.StreamContainerLogs(logCtx, containerID, sink)

	var sidecars []*sim.ContainerHandle
	for i, sidecar := range containers[1:] {
		sidecarImage := sim.ResolveLocalImage(sidecar.Image)
		sidecarPlatform, err := localImagePlatform(ctx, sidecarImage)
		if err != nil {
			sim.StopAndRemoveContainer(containerID)
			for _, h := range sidecars {
				h.Cancel()
			}
			return nil, err
		}
		handle, err := sim.StartContainerSync(sim.ContainerConfig{
			Image:        sidecarImage,
			Architecture: sidecarPlatform,
			Command:      sidecar.Command,
			Args:         sidecar.Args,
			Env:          mergeEnv(containerEnvMap(sidecar.Env), hostMetadataEnv()),
			Name:         fmt.Sprintf("sockerless-sim-cloudrun-svc-%s-sidecar-%d-%d", serviceID, i, hostPort),
			Labels: map[string]string{
				"sockerless-sim-service":           serviceID,
				"sockerless-sim-service-container": sidecar.Name,
			},
			NetworkMode: "container:" + containerID,
			Binds:       bindsFor(sidecar),
			Sandbox:     sim.SandboxCloudRun,
		}, sink)
		if err != nil {
			sim.StopAndRemoveContainer(containerID)
			for _, h := range sidecars {
				h.Cancel()
			}
			return nil, fmt.Errorf("start service sidecar %q: %w", sidecar.Name, err)
		}
		sidecars = append(sidecars, handle)
	}

	inst := &cloudRunServiceInstance{
		containerID: containerID,
		containerIP: sim.ContainerIPv4(containerID),
		sidecars:    sidecars,
		hostPort:    hostPort,
		specSig:     specSig,
		cancelLogs:  cancelLogs,
	}
	cloudRunServiceInstances.Lock()
	if old := cloudRunServiceInstances.byName[name]; old != nil {
		cloudRunServiceInstances.Unlock()
		stopCloudRunServiceInstance(inst)
		return nil, fmt.Errorf("service %q instance replaced while starting", name)
	}
	cloudRunServiceInstances.byName[name] = inst
	instanceStored = true
	cloudRunServiceInstances.Unlock()
	return inst, nil
}

func deleteCloudRunServiceInstance(name string) {
	cloudRunServiceInstances.Lock()
	inst := cloudRunServiceInstances.byName[name]
	delete(cloudRunServiceInstances.byName, name)
	cloudRunServiceInstances.Unlock()
	stopCloudRunServiceInstance(inst)
}

func stopCloudRunServiceInstance(inst *cloudRunServiceInstance) {
	if inst == nil {
		return
	}
	if inst.cancelLogs != nil {
		inst.cancelLogs()
	}
	for _, h := range inst.sidecars {
		h.Cancel()
	}
	sim.StopAndRemoveContainer(inst.containerID)
}

func postCloudRunServiceInstance(ctx context.Context, inst *cloudRunServiceInstance, requestPath, rawQuery string, body io.Reader, contentType string) ([]byte, int, error) {
	// Reach the workload's bootstrap by whichever address is connectable:
	//   - <containerIP>:8080 — works when the sim runs INSIDE a harness
	//     container (the host-published port binds the host's loopback, not
	//     the sim container's, so 127.0.0.1:hostPort is unreachable there);
	//   - 127.0.0.1:<hostPort> — works when the sim runs directly on the host
	//     (and on podman, the host forwards the published port to loopback).
	var cands []string
	if inst.containerIP != "" {
		cands = append(cands, fmt.Sprintf("http://%s:8080", inst.containerIP))
	}
	cands = append(cands, fmt.Sprintf("http://127.0.0.1:%d", inst.hostPort))

	base, err := firstReachableBase(ctx, cands, 60*time.Second)
	if err != nil {
		return nil, -1, fmt.Errorf("bootstrap not ready (tried %d address(es)): %w", len(cands), err)
	}
	bootstrapURL := base + requestPath
	if rawQuery != "" {
		bootstrapURL += "?" + rawQuery
	}
	return postBootstrapWithRetry(ctx, bootstrapURL, body, contentType, 5*time.Minute)
}

// firstReachableBase polls the candidate base URLs (each round, in order) and
// returns the first whose host:port accepts a TCP connection within timeout.
func firstReachableBase(ctx context.Context, cands []string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		for _, base := range cands {
			parsed, perr := urlpkgParse(base)
			if perr != nil {
				lastErr = perr
				continue
			}
			conn, derr := net.DialTimeout("tcp", parsed.Host, 1*time.Second)
			if derr == nil {
				_ = conn.Close()
				return base, nil
			}
			lastErr = derr
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timeout after %s", timeout)
	}
	return "", lastErr
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

func serviceBindsFor(volumes []Volume) func(Container) []string {
	volByName := make(map[string]Volume)
	for _, v := range volumes {
		volByName[v.Name] = v
	}
	return func(c Container) []string {
		var binds []string
		for _, mp := range c.VolumeMounts {
			v, ok := volByName[mp.Name]
			if !ok || v.Gcs == nil || v.Gcs.Bucket == "" {
				continue
			}
			bind := GCSBucketHostDir(v.Gcs.Bucket) + ":" + mp.MountPath
			if v.Gcs.ReadOnly {
				bind += ":ro"
			}
			binds = append(binds, bind)
		}
		return binds
	}
}

func volumesSignature(volumes []Volume) string {
	if len(volumes) == 0 {
		return ""
	}
	var parts []string
	for _, v := range volumes {
		if v.Gcs != nil {
			parts = append(parts, v.Name+"|gcs|"+v.Gcs.Bucket+"|"+strconv.FormatBool(v.Gcs.ReadOnly))
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
		parts = append(parts, c.Name+"|"+sim.ResolveLocalImage(c.Image)+"|"+strings.Join(c.Command, "\x00")+"|"+strings.Join(c.Args, "\x00")+"|"+envSignature(env)+"|"+volumeMountsSignature(c.VolumeMounts))
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
//
// `host` is the configured cloud API coordinate (Request.Host), so the URI
// routes invocations through this Cloud Run data plane just as a real-cloud
// coordinate routes them through run.app.
func seedServiceV2Defaults(svc ServiceV2, host, project, location, serviceID string) ServiceV2 {
	now := nowTimestamp()
	svc.Name = fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, serviceID)
	svc.UID = generateUUID()
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
	svc.LatestReadyRevision = fmt.Sprintf("%s/revisions/%s-00001-abc", svc.Name, serviceID)
	svc.LatestCreatedRevision = svc.LatestReadyRevision
	if !svc.DefaultUriDisabled {
		svc.URI = fmt.Sprintf("http://%s/v2-services-invoke/%s/%s/%s", host, project, location, serviceID)
	}
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
		UID:         generateUUID(),
		Generation:  svc.Generation,
		CreateTime:  now,
		UpdateTime:  now,
		LaunchStage: svc.LaunchStage,
		Service:     serviceName,
		Conditions: []Condition{
			{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: now},
		},
	}
	if svc.Template != nil {
		rev.Labels = svc.Template.Labels
		rev.Annotations = svc.Template.Annotations
		rev.Containers = svc.Template.Containers
		rev.Volumes = svc.Template.Volumes
		rev.Scaling = svc.Template.Scaling
		rev.VpcAccess = svc.Template.VpcAccess
		rev.Timeout = svc.Template.Timeout
	}
	store.Put(full, rev)
}

func registerCloudRunServicesV2(srv *sim.Server) {
	services := sim.MakeStore[ServiceV2](srv.DB(), "crv2_services")
	crv2Services = services
	revisions := sim.MakeStore[RevisionV2](srv.DB(), "crv2_revisions")
	crv2Revisions = revisions
	if crOperations == nil {
		crOperations = sim.MakeStore[Operation](srv.DB(), "operations")
	}

	// CreateService: POST /v2/projects/{project}/locations/{location}/services?serviceId=<id>
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/services", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		serviceID := r.URL.Query().Get("serviceId")
		if serviceID == "" {
			sim.GCPError(w, http.StatusBadRequest, "serviceId query parameter is required", "INVALID_ARGUMENT")
			return
		}

		var svc ServiceV2
		if err := sim.ReadJSON(r, &svc); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		name := fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, serviceID)
		if _, exists := services.Get(name); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "service %q already exists", name)
			return
		}

		// Cloud Run regional CPU quota check. When SIM_GCP_CPU_QUOTA_PER_REGION
		// is set, each fresh revision deploy debits its CPU load against the
		// per-(project, region) sliding-window budget. Reproduces /
		// deterministically — the live cloud rejects with this same
		// error when the regional cpu_allocation quota is exhausted.
		if !regionalCPUQuotaInstance.tryDebit(project, location, serviceCPULoad(svc)) {
			regionalCPUQuotaErrorJSON(w, name)
			return
		}

		svc = seedServiceV2Defaults(svc, r.Host, project, location, serviceID)

		services.Put(name, svc)
		reconcileServiceRevision(revisions, name, serviceID+"-00001-abc", svc)
		projectCloudRunV2ToV1(svc)

		lro := newLRO(project, location, svc, "type.googleapis.com/google.cloud.run.v2.Service")
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
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on service %q", action, id)
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, serviceParam)
		svc, ok := services.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "service %q not found", name)
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
		page, next, ok := paginateList(w, r, result)
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
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "service %q not found", name)
			return
		}
		services.Delete(name)
		deleteCloudRunServiceProjections(project, location, serviceID)
		deleteCloudRunServiceInstance(name)
		revPrefix := name + "/revisions/"
		for _, rev := range revisions.Filter(func(r RevisionV2) bool { return strings.HasPrefix(r.Name, revPrefix) }) {
			revisions.Delete(rev.Name)
		}
		lro := newLRO(project, location, svc, "type.googleapis.com/google.cloud.run.v2.Service")
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
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "service %q not found", name)
			return
		}

		var update ServiceV2
		if err := sim.ReadJSON(r, &update); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
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
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
				return
			}
			update = merged
		}

		// Cloud Run revisions are immutable, so each PATCH spawns a new
		// revision. Charge its CPU load against the regional sliding-window
		// quota — a quota-exhausted UpdateService is the failure mode
		// behind the Cloud Run functions overlay-and-swap path, which
		// replaces the build output with the runtime overlay image.
		if !regionalCPUQuotaInstance.tryDebit(project, location, serviceCPULoad(update)) {
			regionalCPUQuotaErrorJSON(w, name)
			return
		}

		// Preserve identity fields; allow template / labels / annotations / ingress to change.
		update.Name = existing.Name
		update.UID = existing.UID
		update.CreateTime = existing.CreateTime
		update.Generation = existing.Generation + 1
		update.UpdateTime = nowTimestamp()
		if update.LaunchStage == "" {
			update.LaunchStage = existing.LaunchStage
		}
		update.TerminalCondition = &Condition{
			Type:               "Ready",
			State:              "CONDITION_SUCCEEDED",
			LastTransitionTime: update.UpdateTime,
		}
		revName := fmt.Sprintf("%s-%05d-abc", serviceID, update.Generation)
		update.LatestCreatedRevision = fmt.Sprintf("%s/revisions/%s", name, revName)
		update.LatestReadyRevision = update.LatestCreatedRevision
		update.URI = existing.URI

		services.Put(name, update)
		reconcileServiceRevision(revisions, name, revName, update)
		projectCloudRunV2ToV1(update)
		lro := newLRO(project, location, update, "type.googleapis.com/google.cloud.run.v2.Service")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// --- Service Revisions (get/list/delete) ---

	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/services/{service}/revisions/{revision}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		serviceID := sim.PathParam(r, "service")
		revisionID := sim.PathParam(r, "revision")
		name := fmt.Sprintf("projects/%s/locations/%s/services/%s/revisions/%s", project, location, serviceID, revisionID)
		rev, ok := revisions.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "revision %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, rev)
	})
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/services/{service}/revisions", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		serviceID := sim.PathParam(r, "service")
		prefix := fmt.Sprintf("projects/%s/locations/%s/services/%s/revisions/", project, location, serviceID)
		result := revisions.Filter(func(rev RevisionV2) bool { return strings.HasPrefix(rev.Name, prefix) })
		if result == nil {
			result = []RevisionV2{}
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		page, next, ok := paginateList(w, r, result)
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
		rev, ok := revisions.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "revision %q not found", name)
			return
		}
		revisions.Delete(name)
		lro := newLRO(project, location, rev, "type.googleapis.com/google.cloud.run.v2.Revision")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// --- Service IAM verbs (setIamPolicy / testIamPermissions) ---
	// getIamPolicy rides the GET service handler's colon-split above.
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/services/{serviceAction}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		serviceAction := sim.PathParam(r, "serviceAction")
		id, action, found := strings.Cut(serviceAction, ":")
		if !found {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action on service %q", serviceAction)
			return
		}
		switch action {
		case "setIamPolicy", "testIamPermissions":
			handleResourceIAM(w, r, gcpResourceIAMStore(),
				fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, id), action)
		default:
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on service %q", action, id)
		}
	})

	// --- Operations (delete / wait) ---
	// GET .../operations and GET .../operations/{operation} are served by
	// registerCloudFunctions / registerOperations respectively.
	srv.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/operations/{operation}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		opID := sim.PathParam(r, "operation")
		name := fmt.Sprintf("projects/%s/locations/%s/operations/%s", project, location, opID)
		if !crOperations.Delete(name) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
			return
		}
		// google.longrunning.DeleteOperation returns google.protobuf.Empty.
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})
	waitOperation := func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		opAction := sim.PathParam(r, "opAction")
		opID, action, found := strings.Cut(opAction, ":")
		if !found || action != "wait" {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown operation action %q", opAction)
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/operations/%s", project, location, opID)
		op, ok := crOperations.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", name)
			return
		}
		// The sim does no async work — every LRO is already done — so
		// WaitOperation returns the (completed) operation immediately, as
		// real Cloud Run does once the underlying resource has settled.
		sim.WriteJSON(w, http.StatusOK, op)
	}
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/operations/{opAction}", waitOperation)
	// The Cloud Run Admin v1 API publishes the same google.longrunning
	// WaitOperation over the operation records both API versions share
	// (run.projects.locations.operations.wait). One operation, two spellings.
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/operations/{opAction}", waitOperation)

	// Invoke handler. Real Cloud Run hosts the service URI as
	// `https://<service>-<project>.run.app`; the sim's seedServiceV2Defaults
	// hands back `http://<sim>/v2-services-invoke/<project>/<location>/<service>`
	// instead so backends invoke the sim directly. The handler runs the
	// overlay container on demand and forwards the request envelope to
	// the bootstrap's HTTP listener — same flow as Cloud Functions Gen2
	// (`/v2-functions-invoke/`).
	invokeService := func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		serviceID := sim.PathParam(r, "service")
		name := fmt.Sprintf("projects/%s/locations/%s/services/%s", project, location, serviceID)
		svc, ok := services.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "service %q not found", name)
			return
		}
		if svc.Template == nil || len(svc.Template.Containers) == 0 || svc.Template.Containers[0].Image == "" {
			sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "service %q has no container image", name)
			return
		}
		// Cloud Run invoke body is the user's HTTP request payload.
		// Real Cloud Run accepts gzip-encoded request bodies (the
		// gateway transparently decompresses for HTTP/1.1 clients);
		// the sim does the same via openStreamingBody. Malformed
		// or unsupported encoding bubbles up as a real bad-request
		// error rather than being silently stored mis-decoded.
		rc, err := openStreamingBody(r)
		if err != nil {
			sim.GCPErrorf(w, http.StatusUnsupportedMediaType, "INVALID_ARGUMENT", "%s", err.Error())
			return
		}
		bodyBytes, readErr := io.ReadAll(rc)
		_ = rc.Close()
		if readErr != nil {
			sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "failed to read invoke body: %v", readErr)
			return
		}
		ct := r.Header.Get("Content-Type")
		var body io.Reader
		if len(bodyBytes) > 0 {
			body = bytes.NewReader(bodyBytes)
		}
		sink := &cfLogSink{project: project, functionName: serviceID}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		inst, err := ensureCloudRunServiceInstance(ctx, name, serviceID, svc.Template.Containers, svc.Template.Volumes, sink)
		if err != nil {
			sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "invoke service %q: %v", name, err)
			return
		}
		requestPath := "/"
		if suffix := sim.PathParam(r, "path"); suffix != "" {
			requestPath += suffix
		}
		respBody, exitCode, err := postCloudRunServiceInstance(ctx, inst, requestPath, r.URL.RawQuery, body, ct)
		if err != nil {
			sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "invoke service %q: %v", name, err)
			return
		}
		w.Header().Set("X-Sockerless-Exit-Code", strconv.Itoa(exitCode))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(respBody)
	}
	srv.HandleFunc("POST /v2-services-invoke/{project}/{location}/{service}", invokeService)
	srv.HandleFunc("POST /v2-services-invoke/{project}/{location}/{service}/{path...}", invokeService)
}
