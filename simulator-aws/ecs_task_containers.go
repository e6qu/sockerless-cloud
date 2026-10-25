package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

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
// network stack, so the denial does not apply to it. A container definition's
// `privileged` is accepted and not granted: the simulator starts no privileged
// container.
func ecsTaskSandbox(launchType string) sim.SandboxProfile {
	if strings.EqualFold(launchType, "FARGATE") {
		return SandboxFargate
	}
	return sim.SandboxProfile{}
}

// ecsResolvedImage is a container definition's image as the local runtime
// knows it: the workload reference mapped to a pullable name, and the
// platform the pulled image reports.
type ecsResolvedImage struct {
	Image    string
	Platform string
	// Digest is the manifest digest Amazon ECS reports as the container's
	// imageDigest.
	Digest string
	// RegistryAuth is the credential the host pulls Image with.
	RegistryAuth string
}

// ecsEpochSeconds is the ECS API's timestamp shape (seconds since the epoch,
// fractional), as createdAt already is.
func ecsEpochSeconds() float64 {
	return float64(time.Now().UnixMilli()) / 1000
}

// ecsEpochTime is the inverse of ecsEpochSeconds.
func ecsEpochTime(seconds float64) time.Time {
	return time.UnixMilli(int64(seconds * 1000))
}

// ecsResourceInitializationError is a task start that failed while preparing
// the task's resources, before any container ran: Amazon ECS stops such a task
// with TaskFailedToStart and a ResourceInitializationError reason.
type ecsResourceInitializationError struct{ err error }

func (e *ecsResourceInitializationError) Error() string {
	return "ResourceInitializationError: " + e.err.Error()
}

func (e *ecsResourceInitializationError) Unwrap() error { return e.err }

// ecsEFSVolumeHost resolves an Amazon EFS volume to the host directory the
// task mounts: the access point's root when the volume names one, otherwise
// the file system's rootDirectory, which must exist, as an NFS mount of a
// missing path fails.
func ecsEFSVolumeHost(cfg *ECSEfsVolumeConfig) (string, error) {
	if cfg.AuthorizationConfig != nil && cfg.AuthorizationConfig.AccessPointId != "" {
		return EFSAccessPointHostDir(cfg.AuthorizationConfig.AccessPointId)
	}
	if cfg.FileSystemId == "" {
		return "", fmt.Errorf("the volume names no file system")
	}
	if _, ok := efsFileSystems.Get(cfg.FileSystemId); !ok {
		return "", fmt.Errorf("file system %s does not exist", cfg.FileSystemId)
	}
	host, err := EFSFileSystemHostDir(cfg.FileSystemId)
	if err != nil {
		return "", err
	}
	if cfg.RootDirectory == "" || cfg.RootDirectory == "/" {
		return host, nil
	}
	host = filepath.Join(host, strings.TrimPrefix(cfg.RootDirectory, "/"))
	if _, err := os.Stat(host); err != nil {
		return "", fmt.Errorf("root directory %s of file system %s: %w", cfg.RootDirectory, cfg.FileSystemId, err)
	}
	return host, nil
}

func startECSTaskContainers(ctx context.Context, taskID string, td ECSTaskDefinition, taskTags []ECSTag, overrides *ECSTaskOverride, taskVolumeHosts map[string]string, sink sim.LogSink, launchType string, phases *ecsPhaseTimer) (*ecsTaskProcesses, error) {
	if len(td.ContainerDefinitions) == 0 {
		return nil, nil
	}
	if phases == nil {
		phases = newECSPhaseTimer(time.Now)
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
			host, err := ecsEFSVolumeHost(v.EfsVolumeConfiguration)
			if err != nil {
				return nil, &ecsResourceInitializationError{err: fmt.Errorf("failed to invoke EFS utils commands to set up EFS volumes: volume %q: %w", v.Name, err)}
			}
			volMap[v.Name] = host
			continue
		}
		if v.Host != nil && v.Host.SourcePath != "" {
			volMap[v.Name] = v.Host.SourcePath
			continue
		}
		// A volume with no configuration is the task's own ephemeral
		// storage, shared by its containers and gone with it.
		volMap[v.Name] = ecsTaskVolumePrefix(taskID) + v.Name
	}

	processes := &ecsTaskProcesses{
		MainContainerName: td.ContainerDefinitions[0].Name,
		Handles:           make(map[string]placement.Handle, len(td.ContainerDefinitions)),
		StopGrace:         ecsTaskStopGrace(td),
	}
	backend, err := awsPlacement.For(ecsPlacementService(launchType))
	if err != nil {
		return nil, err
	}
	// fail releases what the start created. A start the shutdown interrupted
	// is not a failed task, so its containers are removed at once rather than
	// given their stop timeout, and the caller leaves the task PENDING.
	fail := func(err error) (*ecsTaskProcesses, error) {
		if ctx.Err() != nil {
			ecsAbandonTaskStart(taskID, processes)
			return nil, fmt.Errorf("%w: %w", errECSTaskStartInterrupted, err)
		}
		cleanupECSTaskProcesses(context.Background(), taskID, processes)
		return nil, err
	}
	// The task definition's networkMode decides the network every container in
	// the task lands on: awsvpc gets the task its own elastic network interface
	// in the VPC, host shares the container instance's network stack, none has
	// no connectivity, and bridge uses the container instance's default Docker
	// bridge.
	networkMode := ecsEffectiveNetworkMode(td)
	if _, _, hasENI := ecsTaskENIInfo(taskID); networkMode == ecsNetworkModeAwsvpc && !hasENI {
		return nil, fmt.Errorf("awsvpc task %s has no elastic network interface attachment", taskID)
	}
	phases.Mark("volumes")
	// The task's network decides where its containers resolve names and read
	// their metadata: through the VPC's sidecar for an awsvpc task, the
	// platform network's for a bridge task, and the simulator's own listener
	// for a task sharing the container instance's network stack. A none task
	// has no network to read anything over. The credentials of an awsvpc or
	// bridge task are on each network namespace's own loopback, which the
	// task's awsvpc containers share and each bridge container has alone.
	var (
		taskNetwork awsWorkloadNetwork
		eniAddress  string
		metadata    containerCredentials
	)
	switch networkMode {
	case ecsNetworkModeHost:
		metadata.Env, err = hostNetworkMetadataEnv(taskID)
		if err != nil {
			return fail(fmt.Errorf("resolve Amazon ECS metadata callback: %w", err))
		}
	case ecsNetworkModeNone:
	case ecsNetworkModeAwsvpc:
		taskNetwork, eniAddress, err = ecsTaskVPCNetwork(ctx, taskID)
		if err != nil {
			return fail(err)
		}
		metadata = ecsTaskMetadataEnv(backend, taskNetwork, taskID)
	default:
		taskNetwork, err = awsPlatformNetwork(ctx)
		if err != nil {
			return fail(fmt.Errorf("provision the container instance's network: %w", err))
		}
		metadata = ecsTaskMetadataEnv(backend, taskNetwork, taskID)
	}
	var mainDockerID string

	// Pull every container image before any container starts, the way the
	// ECS agent does, and report the window on the task: pullStartedAt when
	// the first pull begins, pullStoppedAt when the last image is present.
	// Amazon ECS populates both even when every image is cached, and it is
	// how a caller attributes a slow start to the pull rather than to the
	// container. Without it a 6 GB first pull read as three minutes of
	// nothing between createdAt and startedAt.
	pullStartedAt := ecsEpochSeconds()
	ecsTasks.Update(taskID, func(t *ECSTask) { t.PullStartedAt = &pullStartedAt })
	images := make(map[string]ecsResolvedImage, len(td.ContainerDefinitions))
	for _, cd := range td.ContainerDefinitions {
		if cd.Image == "" {
			continue
		}
		localImage := ecrWorkloadImage(cd.Image)
		registryAuth, err := ecrWorkloadRegistryAuth(localImage)
		if err != nil {
			return fail(fmt.Errorf("task container %q: %w", cd.Name, err))
		}
		platform, err := workload.LocalImagePlatform(ctx, localImage, registryAuth)
		if err != nil {
			return fail(fmt.Errorf("resolve task container %q image platform: %w", cd.Name, err))
		}
		digest, err := localImageDigest(ctx, cd.Image, localImage)
		if err != nil {
			return fail(fmt.Errorf("resolve task container %q image digest: %w", cd.Name, err))
		}
		images[cd.Name] = ecsResolvedImage{Image: localImage, Platform: platform, Digest: digest, RegistryAuth: registryAuth}
	}
	pullStoppedAt := ecsEpochSeconds()
	ecsTasks.Update(taskID, func(t *ECSTask) {
		t.PullStoppedAt = &pullStoppedAt
		// The agent knows each container's digest once its image is present.
		for j := range t.Containers {
			if img, ok := images[t.Containers[j].Name]; ok {
				t.Containers[j].ImageDigest = img.Digest
			}
		}
	})
	phases.Mark("image-pull")

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
		localImage := images[cd.Name].Image
		platform := images[cd.Name].Platform

		command := cd.Command
		if len(containerOverride.Command) > 0 {
			command = containerOverride.Command
		}
		cfg := sim.ContainerConfig{
			CancelGracePeriod: ecsContainerStopGrace(cd),
			Image:             localImage,
			RegistryAuth:      images[cd.Name].RegistryAuth,
			Architecture:      platform,
			Command:           cd.EntryPoint,
			Args:              command,
			WorkingDir:        cd.WorkingDirectory,
			Env:               workloadhost.MergeEnv(cmdEnv, metadata.Env),
			Name:              containerName,
			Labels: map[string]string{
				"sockerless-sim-task":           taskID,
				"sockerless-sim-task-container": cd.Name,
			},
			Tty:       wantTTY || cd.PseudoTerminal,
			OpenStdin: wantTTY || cd.Interactive,
			Binds:     binds,
			Sandbox:   ecsTaskSandbox(launchType),
		}
		// Docker Desktop does not route a Linux VM's user-defined bridge
		// addresses back to the host process. Publish the task definition's
		// declared listener ports on random loopback ports so host-resident
		// Elastic Load Balancing can reach the same real container listener.
		// The public Amazon ECS/ENI coordinate remains the task IP; the
		// mapping is rediscovered from Docker on restart. The task's
		// containers share one namespace, which the first owns, so it
		// publishes every container's ports.
		if networkMode == ecsNetworkModeAwsvpc && i == 0 {
			for _, each := range td.ContainerDefinitions {
				for _, mapping := range each.PortMappings {
					if mapping.ContainerPort > 0 && !slices.Contains(cfg.PublishPorts, mapping.ContainerPort) {
						cfg.PublishPorts = append(cfg.PublishPorts, mapping.ContainerPort)
					}
				}
			}
		}
		cfg.MemoryBytes, cfg.NanoCPU = ecsContainerResourceLimits(td, cd)
		switch {
		case networkMode == ecsNetworkModeHost:
			// host mode: the containers use the container instance's network
			// stack directly, so they share its interfaces and its loopback.
			cfg.NetworkMode = "host"
			cfg.ExtraHosts = append(workloadhost.ExtraHosts(), elbv2WorkloadExtraHosts()...)
		case networkMode == ecsNetworkModeNone:
			// none mode: the containers have no external connectivity.
			cfg.NetworkMode = "none"
		case networkMode == ecsNetworkModeAwsvpc && i > 0:
			// Every container in an awsvpc task shares the task ENI: the
			// others join the first as members of its namespace.
		case networkMode == ecsNetworkModeAwsvpc:
			cfg.ExtraHosts = append(workloadhost.ExtraHosts(), elbv2WorkloadExtraHosts()...)
			cfg.Network = taskNetwork.Name
			cfg.IPv4Address = eniAddress
			cfg.MACAddress = ecsTaskENIDetail(taskID, "macAddress")
			cfg.DNS = taskNetwork.DNS()
			if groups, _, ok := ecsTaskENIGroups(taskID); ok {
				cfg.Ingress = ec2IngressStages(groups)
			}
			if _, subnetID, ok := ecsTaskENIInfo(taskID); ok {
				cfg.Gateway = ec2SubnetGateway(subnetID)
			}
		default:
			// bridge mode: each container gets its own address on the
			// container instance's network, the platform network.
			cfg.ExtraHosts = append(workloadhost.ExtraHosts(), elbv2WorkloadExtraHosts()...)
			cfg.Network = taskNetwork.Name
			cfg.DNS = taskNetwork.DNS()
		}

		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		var handle placement.Handle
		if networkMode == ecsNetworkModeAwsvpc && i > 0 {
			handle, err = backend.PlaceMember(ctx, mainDockerID, cfg, sink)
		} else {
			handle, err = metadata.place(ctx, backend, cfg, sink)
		}
		if err != nil {
			return fail(fmt.Errorf("start task container %q: %w", cd.Name, err))
		}
		if i == 0 {
			mainDockerID = handle.ID()
			if networkMode == ecsNetworkModeAwsvpc {
				// The task's interface is a member of its groups now, which
				// the permissions naming them expand over on its neighbours.
				if _, vpcID, ok := ecsTaskENIGroups(taskID); ok {
					ec2ReapplyIngress(vpcID)
				}
			}
		}
		processes.Handles[cd.Name] = handle
		phases.Mark("container:" + cd.Name)
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

// ecsTaskENIInfo reads a task's awsvpc ENI IP + subnet from its attachment.
// Returns ok=false for tasks without an awsvpc ENI.
// ecsTaskENIDetail is one detail of the task's elastic network interface
// attachment.
func ecsTaskENIDetail(taskID, name string) string {
	task, found := ecsTasks.Get(taskID)
	if !found {
		return ""
	}
	for _, att := range task.Attachments {
		if att.Type == "ElasticNetworkInterface" {
			return ecsTaskDetail(att.Details, name)
		}
	}
	return ""
}

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
				dockerID = h.ID()
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

// ecsTaskVPCNetwork resolves the engine network realizing an awsvpc task's
// VPC, creating it at the first attach, and the task's elastic network
// interface address on it. Every failure is returned so an awsvpc launch
// fails rather than landing the task on the container instance's default
// bridge with an address that is not its ENI's.
func ecsTaskVPCNetwork(ctx context.Context, taskID string) (network awsWorkloadNetwork, eniAddress string, err error) {
	eniIP, subnetID, ok := ecsTaskENIInfo(taskID)
	if !ok {
		return awsWorkloadNetwork{}, "", fmt.Errorf("awsvpc task %s has no elastic network interface attachment", taskID)
	}
	subnet, ok := ec2Subnets.Get(subnetID)
	if !ok {
		return awsWorkloadNetwork{}, "", fmt.Errorf("subnet %s for task %s not found", subnetID, taskID)
	}
	network, err = ec2EnsureVPCNetwork(ctx, subnet.VpcId)
	if err != nil {
		if errors.Is(err, sim.ErrAddressRangeInUse) {
			return awsWorkloadNetwork{}, "", &ecsResourceInitializationError{err: fmt.Errorf("failed to configure ENI: %w", err)}
		}
		return awsWorkloadNetwork{}, "", fmt.Errorf("provision VPC network for %s: %w", subnet.VpcId, err)
	}
	return network, eniIP, nil
}
