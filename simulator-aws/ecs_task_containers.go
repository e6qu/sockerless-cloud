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

// Bound the container by its advertised sizing, so a task advertising 512 CPU
// / 1024 MiB sees matching cpu.max and memory.max. Container-level values
// refine the task size, which is the fallback. CPU is in ECS units (1024 = 1
// vCPU), memory in MiB.
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

// The AWS Fargate profile denies host networking, which Fargate never offers;
// a host-mode task runs on EC2 or EXTERNAL, where Amazon ECS shares the
// instance's network stack. `privileged` is accepted but not granted.
func ecsTaskSandbox(launchType string) sim.SandboxProfile {
	if strings.EqualFold(launchType, "FARGATE") {
		return SandboxFargate
	}
	return sim.SandboxProfile{}
}

// The workload reference mapped to a pullable name, and the pulled image's
// platform.
type ecsResolvedImage struct {
	Image    string
	Platform string
	// Reported as the container's imageDigest.
	Digest       string
	RegistryAuth string
}

// Fractional epoch seconds, like createdAt.
func ecsEpochSeconds() float64 {
	return float64(time.Now().UnixMilli()) / 1000
}

func ecsEpochTime(seconds float64) time.Time {
	return time.UnixMilli(int64(seconds * 1000))
}

// Amazon ECS stops such a task with TaskFailedToStart and a
// ResourceInitializationError reason.
type ecsResourceInitializationError struct{ err error }

func (e *ecsResourceInitializationError) Error() string {
	return "ResourceInitializationError: " + e.err.Error()
}

func (e *ecsResourceInitializationError) Unwrap() error { return e.err }

// The access point's root, else rootDirectory, which must exist, as an NFS
// mount of a missing path fails.
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

// A start that fails stops the containers it placed; exitCodes holds how each
// of them exited.
type ecsStartFailure struct {
	err       error
	exitCodes map[string]int
}

func (e *ecsStartFailure) Error() string { return e.err.Error() }
func (e *ecsStartFailure) Unwrap() error { return e.err }

// Places each container once every dependency its dependsOn names has reached
// its condition, as the Amazon ECS agent does, and registers the task's
// processes before the first placement so a running container reads its
// metadata while a later one still waits. It returns errECSTaskStartPreempted
// when the task starts stopping first, leaving the processes to the stop.
func startECSTaskContainers(ctx context.Context, taskID string, td ECSTaskDefinition, taskTags []ECSTag, overrides *ECSTaskOverride, taskVolumeHosts map[string]string, sinks ecsTaskLogSinks, launchType, containerInstanceKey string, phases *ecsPhaseTimer) (*ecsTaskProcesses, error) {
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
		// An unconfigured volume is the task's ephemeral storage, gone with it.
		volMap[v.Name] = ecsTaskVolumePrefix(taskID) + v.Name
	}

	processes := newECSTaskProcesses(td, td.ContainerDefinitions[0].Name)
	backend, err := awsPlacement.For(ecsPlacementService(launchType))
	if err != nil {
		return nil, err
	}
	// A start interrupted by shutdown is not a failed task: remove its containers
	// at once and leave the task PENDING. A failed start marks the task stopping
	// first, so the watches of the containers it stops record nothing.
	fail := func(err error) (*ecsTaskProcesses, error) {
		ecsProcessHandles.CompareAndDelete(taskID, processes)
		if ctx.Err() != nil {
			ecsAbandonTaskStart(taskID, processes)
			return nil, fmt.Errorf("%w: %w", errECSTaskStartInterrupted, err)
		}
		ecsTasks.Update(taskID, func(t *ECSTask) { t.DesiredStatus = ECSTaskStatusStopped })
		cleanupECSTaskProcesses(context.Background(), taskID, processes)
		exitCodes := map[string]int{}
		for name, handle := range processes.named() {
			if handle != nil {
				exitCodes[name] = handle.Wait().ExitCode
			}
		}
		return nil, &ecsStartFailure{err: err, exitCodes: exitCodes}
	}
	// awsvpc gets the task its own ENI in the VPC, host shares the instance's
	// network stack, none has no connectivity, bridge uses the default bridge.
	networkMode := ecsEffectiveNetworkMode(td)
	if _, _, hasENI := ecsTaskENIInfo(taskID); networkMode == ecsNetworkModeAwsvpc && !hasENI {
		return nil, fmt.Errorf("awsvpc task %s has no elastic network interface attachment", taskID)
	}
	phases.Mark("volumes")
	// awsvpc tasks resolve and read metadata through the VPC's sidecar, bridge
	// tasks through the platform network's, host tasks through the simulator's
	// listener; none has nothing. Credentials are on each network namespace's own
	// loopback.
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

	// Pull every image before any container starts, as the agent does, recording
	// pullStartedAt and pullStoppedAt; Amazon ECS sets both even when cached, and
	// callers use them to attribute a slow start to the pull.
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
		for j := range t.Containers {
			if img, ok := images[t.Containers[j].Name]; ok {
				t.Containers[j].ImageDigest = img.Digest
			}
		}
	})
	phases.Mark("image-pull")

	// The first container placed owns the task's network namespace and the rest
	// join it. Once every container in it has exited, the next one placed owns a
	// new namespace on the same interface, as a container that follows a
	// completed setup container does.
	placeContainer := func(i int) (placement.Handle, error) {
		cd := td.ContainerDefinitions[i]
		containerOverride := ecsContainerOverrideFor(overrides, cd.Name)
		cmdEnv := make(map[string]string, len(cd.Environment))
		for _, ev := range cd.Environment {
			cmdEnv[ev.Name] = ev.Value
		}
		// Inject secrets as environment variables at launch, as Amazon ECS does.
		for name, val := range resolveECSContainerSecrets(cd.Secrets) {
			cmdEnv[name] = val
		}
		for _, ev := range containerOverride.Environment {
			cmdEnv[ev.Name] = ev.Value
		}
		var binds []string
		for _, mp := range cd.MountPoints {
			if src, ok := volMap[mp.SourceVolume]; ok {
				// `z` relabels for SELinux so a confined task container can write the EFS
				// directory; hosts without SELinux ignore it.
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
			Env:               workloadhost.MergeEnv(cmdEnv, metadata.Env, ecsContainerMetadataEnv(metadata.Env, i)),
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
		cfg.MemoryBytes, cfg.NanoCPU = ecsContainerResourceLimits(td, cd)

		var owner placement.Handle
		if networkMode == ecsNetworkModeAwsvpc {
			owner = processes.liveNamespaceHandle()
		}
		switch {
		case networkMode == ecsNetworkModeHost:
			cfg.NetworkMode = "host"
			cfg.ExtraHosts = append(workloadhost.ExtraHosts(), elbv2WorkloadExtraHosts()...)
		case networkMode == ecsNetworkModeNone:
			cfg.NetworkMode = "none"
		case networkMode == ecsNetworkModeAwsvpc && owner != nil:
			// Every container joins the owner's namespace and shares the task ENI.
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
			// Docker Desktop does not route a VM's bridge addresses to the host, so
			// publish the declared ports on loopback for Elastic Load Balancing. The
			// task IP stays the public coordinate. The owner publishes every
			// container's ports.
			for _, each := range td.ContainerDefinitions {
				for _, mapping := range each.PortMappings {
					if mapping.ContainerPort > 0 && !slices.Contains(cfg.PublishPorts, mapping.ContainerPort) {
						cfg.PublishPorts = append(cfg.PublishPorts, mapping.ContainerPort)
					}
				}
			}
		default:
			// Each container gets its own address on the platform network.
			cfg.ExtraHosts = append(workloadhost.ExtraHosts(), elbv2WorkloadExtraHosts()...)
			cfg.Network = taskNetwork.Name
			cfg.DNS = taskNetwork.DNS()
		}

		if owner != nil {
			return backend.PlaceMember(ctx, owner.ID(), cfg, sinks.For(cd.Name))
		}
		handle, err := metadata.place(ctx, backend, cfg, sinks.For(cd.Name))
		if err != nil {
			return nil, err
		}
		if networkMode == ecsNetworkModeAwsvpc {
			processes.setMain(cd.Name)
			// The interface now belongs to its groups, which neighbours' permissions
			// expand over.
			if _, vpcID, ok := ecsTaskENIGroups(taskID); ok {
				ec2ReapplyIngress(vpcID)
			}
		}
		return handle, nil
	}

	ecsProcessHandles.Store(taskID, processes)
	var waiting []int
	for i, cd := range td.ContainerDefinitions {
		if cd.Image != "" {
			waiting = append(waiting, i)
		}
	}
	started := make(map[string]time.Time, len(waiting))
	for len(waiting) > 0 {
		// Take the change signal before reading the task, so a change between the
		// read and the wait ends the wait.
		changed := ecsTaskChanges.Changed(taskID)
		task, ok := ecsTasks.Get(taskID)
		if !ok || ecsTaskStartPreempted(task, processes.Essential) {
			return processes, errECSTaskStartPreempted
		}
		now := time.Now()
		next := -1
		var deadline time.Time
		for position, i := range waiting {
			met, expires, err := ecsContainerDependenciesMet(td, td.ContainerDefinitions[i], task.Containers, started, now)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[sim-ecs] task %s: %v\n", taskID, err)
				return fail(err)
			}
			if met {
				next = position
				break
			}
			if !expires.IsZero() && (deadline.IsZero() || expires.Before(deadline)) {
				deadline = expires
			}
		}
		if next < 0 {
			var expired <-chan time.Time
			var timer *time.Timer
			if !deadline.IsZero() {
				timer = time.NewTimer(deadline.Sub(now))
				expired = timer.C
			}
			select {
			case <-changed:
			case <-expired:
			case <-ctx.Done():
			}
			if timer != nil {
				timer.Stop()
			}
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			continue
		}

		i := waiting[next]
		waiting = slices.Delete(waiting, next, next+1)
		cd := td.ContainerDefinitions[i]
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		handle, err := placeContainer(i)
		if err != nil {
			return fail(fmt.Errorf("start task container %q: %w", cd.Name, err))
		}
		processes.setHandle(cd.Name, handle)
		started[cd.Name] = handle.StartedAt()
		hasHealthCheck := ecsContainerHealthCheck(cd) != nil
		ecsTasks.Update(taskID, func(t *ECSTask) {
			for j := range t.Containers {
				if t.Containers[j].Name != cd.Name {
					continue
				}
				t.Containers[j].LastStatus = "RUNNING"
				if hasHealthCheck {
					t.Containers[j].HealthStatus = ecsHealthUnknown
				}
			}
			t.HealthStatus = ecsTaskHealthStatus(td, t.Containers)
		})
		ecsContainerPlacements.Announce(taskID)
		ecsWatchTaskContainer(taskID, containerInstanceKey, processes, cd.Name, handle)
		ecsMonitorContainerHealth(taskID, cd, handle)
		phases.Mark("container:" + cd.Name)
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

// Returns "" when the attachment lacks the detail.
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

// ok is false for a task without an awsvpc ENI.
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
	// A container still waiting on its dependencies has not been created, and
	// the agent lists only created containers.
	var containers []map[string]any
	for _, cd := range td.ContainerDefinitions {
		if container, ok := ecsTaskContainerMetadata(taskID, task, td, cd, eniIP); ok {
			containers = append(containers, container)
		}
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

func ecsContainerMetadataV4(taskID, container string) (map[string]any, bool) {
	task, ok := ecsTasks.Get(taskID)
	if !ok {
		return nil, false
	}
	td, ok := ecsTaskDefinitionByArn(task.TaskDefinitionArn)
	if !ok {
		return nil, false
	}
	eniIP, _, _ := ecsTaskENIInfo(taskID)
	for _, cd := range td.ContainerDefinitions {
		if cd.Name == container {
			return ecsTaskContainerMetadata(taskID, task, td, cd, eniIP)
		}
	}
	return nil, false
}

// Creates the VPC network at the first attach. Every failure is returned so an
// awsvpc launch never lands on the default bridge with a non-ENI address.
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
