package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// azureFunctionInstance is the container App Service runs for a site
// instance: a request starts it when none runs, Always On starts it without
// one, and a restart, a configuration change or deletion tears it down. The
// mutex serializes the start so concurrent requests share one container.
type azureFunctionInstance struct {
	mu             sync.Mutex
	handle         placement.Handle
	containerID    string
	sidecarHandles []placement.Handle
	// logsDone closes once the output has been read to the end, which is when
	// the container exits.
	exit          *containerExit
	logsDone      <-chan struct{}
	network       azureWorkloadNetwork
	subnetID      string
	extraNetworks []siteExtraNetwork
	// candidates are the addresses port may be reached at; address is the one
	// that answered.
	port       int
	candidates []string
	address    string
	stopGrace  time.Duration
	// vnets are the integrated virtual networks, by lower-cased resource id. The
	// container runs on the first one's engine network, at an address leased in
	// the integration subnet, with that network's sidecar as its resolver. They
	// outlive a restart, as the integration does.
	vnets []string
	// referenceRefresh re-fetches the Key Vault references when the platform's
	// cache of them expires.
	references       webReferenceValues
	referenceRefresh *bg.Timer
}

// Caller holds inst.mu.
func (inst *azureFunctionInstance) addNetworkLocked(vnetID string) {
	vnetID = strings.ToLower(vnetID)
	for _, n := range inst.vnets {
		if n == vnetID {
			return
		}
	}
	inst.vnets = append(inst.vnets, vnetID)
}

// Caller holds inst.mu.
func (inst *azureFunctionInstance) removeNetworkLocked(vnetID string) {
	vnetID = strings.ToLower(vnetID)
	kept := inst.vnets[:0]
	for _, n := range inst.vnets {
		if n != vnetID {
			kept = append(kept, n)
		}
	}
	inst.vnets = kept
}

// siteNetworkLocked is the first integrated virtual network's engine network,
// or the platform network. Caller holds inst.mu.
func (inst *azureFunctionInstance) siteNetworkLocked(ctx context.Context, site *Site) (azureWorkloadNetwork, string, error) {
	if len(inst.vnets) == 0 {
		network, err := azurePlatformNetwork(ctx)
		return network, "", err
	}
	vnet, ok := azureVnetByID(inst.vnets[0])
	if !ok {
		return azureWorkloadNetwork{}, "", fmt.Errorf("virtual network %s of site %s no longer exists", inst.vnets[0], site.Name)
	}
	subnetID := siteConnectionSubnet(site, vnet)
	network, err := siteVNetNetwork(ctx, vnet, subnetID)
	if err != nil {
		return azureWorkloadNetwork{}, "", fmt.Errorf("attach site %s to virtual network %s: %w", site.Name, vnet.ID, err)
	}
	return network, subnetID, nil
}

func siteConnectionSubnet(site *Site, vnet VirtualNetwork) string {
	for _, conn := range siteVnetConnections(site.ID) {
		if strings.Contains(conn.Properties.VnetResourceID, "/subnets/") &&
			strings.EqualFold(azureVNetIDOfSubnet(conn.Properties.VnetResourceID), vnet.ID) {
			return conn.Properties.VnetResourceID
		}
	}
	return azureWholeVNetSubnet(vnet)
}

// siteVNetNetwork is the network of the prefix the subnet lies in, or the
// first prefix's for a connection naming no subnet.
func siteVNetNetwork(ctx context.Context, vnet VirtualNetwork, subnetID string) (azureWorkloadNetwork, error) {
	if subnetID != "" {
		return azureSubnetNetwork(ctx, placementWeb, subnetID)
	}
	return azureVNetNetworkFor(ctx, placementWeb, vnet.ID)
}

// Caller holds inst.mu.
func (inst *azureFunctionInstance) siteNetworkCurrentLocked() bool {
	want := ""
	if len(inst.vnets) > 0 {
		want = inst.vnets[0]
	}
	if azureNetworkVNet(inst.network.Network) != want {
		return false
	}
	return len(inst.extraNetworks) == max(len(inst.vnets)-1, 0)
}

// extraNetworksLocked leases the workload an address on each further virtual
// network a classic connection integrated, in that connection's subnet: a
// container joins each network after it starts, and a guest boots with an
// interface on each. Caller holds inst.mu.
func (inst *azureFunctionInstance) extraNetworksLocked(ctx context.Context, site *Site, owner string) ([]siteExtraNetwork, []sim.NetworkAttachment, error) {
	if len(inst.vnets) < 2 {
		return nil, nil, nil
	}
	var leases []siteExtraNetwork
	var attachments []sim.NetworkAttachment
	release := func() {
		for _, lease := range leases {
			lease.releaseSubnetLease()
		}
	}
	for _, vnetID := range inst.vnets[1:] {
		vnet, ok := azureVnetByID(vnetID)
		if !ok {
			release()
			return nil, nil, fmt.Errorf("virtual network %s of site %s no longer exists", vnetID, site.Name)
		}
		lease := siteExtraNetwork{owner: owner, subnetID: siteConnectionSubnet(site, vnet)}
		network, err := siteVNetNetwork(ctx, vnet, lease.subnetID)
		if err != nil {
			release()
			return nil, nil, fmt.Errorf("attach site %s to virtual network %s: %w", site.Name, vnet.ID, err)
		}
		lease.network = network.Network
		// A connection without a subnet leaves the address to the engine, which a
		// guest cannot take: its backend refuses the placement.
		if lease.subnetID != "" {
			if lease.address, err = azureLeaseWorkloadAddress(lease.subnetID, owner); err != nil {
				release()
				return nil, nil, err
			}
		}
		leases = append(leases, lease)
		attachments = append(attachments, sim.NetworkAttachment{Network: network.Network, IPv4Address: lease.address})
	}
	return leases, attachments, nil
}

type siteExtraNetwork struct {
	network, subnetID, address, owner string
}

func (extra siteExtraNetwork) releaseSubnetLease() {
	if extra.subnetID != "" {
		azureReleaseWorkloadAddress(extra.subnetID, extra.owner, extra.address)
	}
}

var azureFunctionInstances = struct {
	sync.Mutex
	bySite map[string]*azureFunctionInstance
}{bySite: map[string]*azureFunctionInstance{}}

func azfInstanceFor(siteName string) *azureFunctionInstance {
	azureFunctionInstances.Lock()
	defer azureFunctionInstances.Unlock()
	inst := azureFunctionInstances.bySite[siteName]
	if inst == nil {
		inst = &azureFunctionInstance{}
		azureFunctionInstances.bySite[siteName] = inst
	}
	return inst
}

// A stopped site runs nothing. Caller holds inst.mu.
func (inst *azureFunctionInstance) ensureStarted(site *Site) error {
	if current, ok := webJobSite(site.ID); ok && siteStopped(&current) {
		return errSiteStopped
	}
	if inst.containerID != "" {
		select {
		case <-inst.exit.done:
			inst.teardownLocked()
		default:
			if inst.siteNetworkCurrentLocked() {
				return nil
			}
			// A changed VNet integration restarts the site on its new network, as App
			// Service restarts the app.
			inst.teardownLocked()
		}
	}
	return inst.startLocked(site)
}

func siteImageMissing(site *Site) error {
	stack := siteRuntimeStack(site)
	switch {
	case siteIsFunctionApp(site) && stack != "":
		return fmt.Errorf(
			"function app %q is configured with the built-in runtime stack %q, which this simulator "+
				"does not run the Azure Functions host for; the stacks it runs are %s, or configure a "+
				"container image (linuxFxVersion \"DOCKER|<image>\")",
			site.Name, stack, strings.Join(functionsHostStackNames(), ", "))
	case siteIsFunctionApp(site):
		return fmt.Errorf(
			"function app %q names no runtime: configure a built-in stack (%s) or a container image "+
				"(linuxFxVersion \"DOCKER|<image>\")", site.Name, strings.Join(functionsHostStackNames(), ", "))
	case stack != "":
		return fmt.Errorf(
			"site %q is configured with the built-in runtime stack %q, which this simulator does not "+
				"run; the stacks it runs are %s", site.Name, stack, strings.Join(appServiceLinuxStackNames(), ", "))
	}
	return fmt.Errorf(
		"site %q names no runtime: configure a built-in stack (%s) or a container image "+
			"(linuxFxVersion \"DOCKER|<image>\")", site.Name, strings.Join(appServiceLinuxStackNames(), ", "))
}

// startLocked runs the site the way App Service runs a Linux custom
// container: the image's own entrypoint, the startup command in place of its
// CMD, the app settings and PORT in its environment (for a main
// sitecontainer, the app settings only when it inherits them), and any
// sidecar sitecontainers in its network namespace. Caller holds inst.mu.
func (inst *azureFunctionInstance) startLocked(site *Site) error {
	references := webSiteReferenceValues(site)
	main := mainSiteContainer(site.ID)
	var (
		image        string
		args         []string
		inheritedEnv = workloadhost.MergeEnv(references.appSettings, references.connectionStrings)
		containerEnv map[string]string
		binds        []string
	)
	port := siteContainerPort(site)
	if main != nil {
		image = main.Properties.Image
		args = splitStartUpCommand(main.Properties.StartUpCommand)
		inheritedEnv = main.inheritedEnv(references)
		containerEnv = siteContainerEnv(main.Properties.EnvironmentVariables, references.appSettings)
		binds = siteContainerVolumeBinds(site.Name, main.Properties.VolumeMounts)
		if p, err := strconv.Atoi(strings.TrimSpace(main.Properties.TargetPort)); err == nil && p > 0 && p < 65536 {
			port = p
		}
	} else if stack, ok := siteBuiltInStack(site); ok {
		// The stack image's entrypoint hands its arguments to Oryx as the user
		// startup command.
		image = stack.Image
		if site.Properties.SiteConfig != nil {
			if cmd := strings.TrimSpace(site.Properties.SiteConfig.AppCommandLine); cmd != "" {
				args = []string{cmd}
			}
		}
		if _, set := siteAppSettings(site)["WEBSITES_PORT"]; !set {
			port = stack.Port
		}
		home, err := prepareSiteHome(site)
		if err != nil {
			return err
		}
		binds = append(home, siteAzureStorageBinds(site)...)
	} else if stack, ok := siteFunctionsHostStack(site); ok {
		// App Service runs the Functions host image without a startup command.
		image = stack.Image
		home, err := prepareSiteHome(site)
		if err != nil {
			return err
		}
		if err := syncFunctionsHostSecrets(site); err != nil {
			return err
		}
		binds = append(home, siteAzureStorageBinds(site)...)
	} else {
		image = siteContainerImage(site)
		if site.Properties.SiteConfig != nil {
			args = splitStartUpCommand(site.Properties.SiteConfig.AppCommandLine)
		}
		// A shared named volume maps to one Azure Files share, so every container
		// that mounts it sees the same workspace.
		binds = siteAzureStorageBinds(site)
		if strings.EqualFold(strings.TrimSpace(siteAppSettings(site)["WEBSITES_ENABLE_APP_SERVICE_STORAGE"]), "true") {
			home, err := prepareSiteHome(site)
			if err != nil {
				return err
			}
			binds = append(home, binds...)
		}
	}
	if image == "" {
		return siteImageMissing(site)
	}

	localImage := sim.ResolveLocalImage(image)
	ctx, cancel := context.WithTimeout(context.Background(), siteStartTimeLimit(site))
	defer cancel()

	// Pull with the credential the site declared for its registry, as App
	// Service does.
	registryAuth := acrWorkloadRegistryAuth(image, siteWorkloadRegistries(site, image))
	platform, err := workload.LocalImagePlatform(ctx, localImage, registryAuth)
	if err != nil {
		return err
	}
	network, subnetID, err := inst.siteNetworkLocked(ctx, site)
	if err != nil {
		return err
	}
	metadataEnv, err := hostMetadataEnv(site, network.DNS)
	if err != nil {
		return err
	}
	env := workloadhost.MergeEnv(inheritedEnv, appServicePlatformEnv(site),
		map[string]string{"PORT": strconv.Itoa(port)}, metadataEnv, containerEnv)
	sink := newFuncLogSink(site)
	machine := webNewSiteMachineName(site.Name)

	name := fmt.Sprintf("sockerless-sim-azure-site-%s-%s", siteStorageName(site.Name), sim.RandomHex(8))
	address, releaseLease, err := azureWorkloadAddress(ctx, network, subnetID, name)
	if err != nil {
		return err
	}
	backend, err := azurePlacementBackend(placementWeb)
	if err != nil {
		releaseLease()
		return err
	}
	extraNetworks, attachments, err := inst.extraNetworksLocked(ctx, site, name)
	if err != nil {
		releaseLease()
		return err
	}
	releaseExtra := func() {
		for _, extra := range extraNetworks {
			extra.releaseSubnetLease()
		}
	}
	handle, err := backend.Place(ctx, sim.ContainerConfig{
		Image:        localImage,
		Architecture: platform,
		RegistryAuth: registryAuth,
		Env:          env,
		Args:         args,
		Binds:        binds,
		Name:         name,
		Hostname:     machine,
		Labels: map[string]string{
			"sockerless-sim-type": "azure-site",
			"sockerless-site":     site.Name,
		},
		Network:           network.Network,
		IPv4Address:       address,
		ExtraNetworks:     attachments,
		DNS:               network.resolvers(),
		ExtraHosts:        workloadhost.ExtraHosts(),
		PublishPorts:      []int{port},
		Sandbox:           SandboxAZF,
		CancelGracePeriod: siteStopGrace(site),
	}, sink)
	if err != nil {
		releaseExtra()
		releaseLease()
		return fmt.Errorf("start site container: %w", err)
	}
	azureReleaseOnExit(handle.Exited(), releaseLease)
	for i := range extraNetworks {
		if extraNetworks[i].address == "" {
			extraNetworks[i].address = sim.ContainerIPv4On(ctx, handle.ID(), extraNetworks[i].network).String()
		}
	}
	inst.extraNetworks = extraNetworks
	containerID := handle.ID()
	exit := newContainerExit(handle)
	stop := func() {
		handle.Cancel()
		<-handle.Exited()
	}

	// Sidecars share the main's network namespace, so the main reaches a
	// sidecar's port on localhost — the App Service multi-container contract.
	sidecarHandles, err := startSidecarContainers(ctx, site, containerID, references, network.DNS, sink)
	if err != nil {
		stop()
		return fmt.Errorf("start sitecontainers: %w", err)
	}

	candidates, err := siteContainerCandidates(ctx, handle, port)
	if err != nil {
		for _, handle := range sidecarHandles {
			handle.Cancel()
		}
		stop()
		return err
	}
	inst.handle = handle
	inst.containerID = containerID
	inst.sidecarHandles = sidecarHandles
	inst.exit = exit
	inst.logsDone = handle.Exited()
	inst.network = network
	inst.subnetID = subnetID
	inst.address = ""
	inst.port = port
	inst.candidates = candidates
	inst.stopGrace = siteStopGrace(site)
	inst.cacheReferencesLocked(site.ID, containerID, references)
	return nil
}

// Caller holds inst.mu.
func (inst *azureFunctionInstance) releaseExtraNetworksLocked() {
	for _, extra := range inst.extraNetworks {
		extra.releaseSubnetLease()
	}
	inst.extraNetworks = nil
}

// siteContainerCandidates returns the container's address on its network,
// which the host and a harness container both reach, and the port the engine
// publishes on the host's loopback, which reaches it where container
// addresses are not routed. A stopped container has no candidates, so the
// start reports its exit.
func siteContainerCandidates(ctx context.Context, handle placement.Handle, port int) ([]string, error) {
	select {
	case <-handle.Exited():
		return nil, nil
	default:
	}
	var candidates []string
	if ip, err := handle.Address(ctx); err == nil && ip != "" {
		candidates = append(candidates, "http://"+net.JoinHostPort(ip, strconv.Itoa(port)))
	}
	hostPort, err := handle.PublishedPort(ctx, port)
	if err != nil {
		if errors.Is(err, sim.ErrNotRunning) {
			return nil, nil
		}
		// A guest publishes no port on the host's loopback.
		if handle.Kind() == placement.MicroVM && len(candidates) > 0 {
			return candidates, nil
		}
		return nil, fmt.Errorf("read the site's published port: %w", err)
	}
	return append(candidates, fmt.Sprintf("http://127.0.0.1:%d", hostPort)), nil
}

type containerExit struct {
	done   <-chan struct{}
	handle placement.Handle
}

func newContainerExit(handle placement.Handle) *containerExit {
	return &containerExit{done: handle.Exited(), handle: handle}
}

func (e *containerExit) exitedBeforeAnswering(site string, port int) error {
	code := e.handle.Wait().ExitCode
	if code < 0 {
		return fmt.Errorf("site %q container exited before it answered on port %d", site, port)
	}
	return fmt.Errorf("site %q container exited before it answered on port %d: exit code %d", site, port, code)
}

func (inst *azureFunctionInstance) teardown() {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.teardownLocked()
}

func (inst *azureFunctionInstance) running() (string, placement.Handle) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.containerID, inst.handle
}

type siteStart struct {
	containerID string
	port        int
	candidates  []string
	address     string
	exit        *containerExit
	logsDone    <-chan struct{}
}

func (inst *azureFunctionInstance) start(site *Site) (siteStart, error) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if err := inst.ensureStarted(site); err != nil {
		return siteStart{}, err
	}
	return siteStart{containerID: inst.containerID, port: inst.port, candidates: inst.candidates,
		address: inst.address, exit: inst.exit, logsDone: inst.logsDone}, nil
}

// The VNet-integration networks stay recorded: the integration belongs to
// the site, not to one container. Caller holds inst.mu.
func (inst *azureFunctionInstance) teardownLocked() {
	if inst.referenceRefresh != nil {
		inst.referenceRefresh.Stop()
	}
	for _, h := range inst.sidecarHandles {
		h.Cancel()
	}
	if inst.handle != nil {
		inst.handle.Cancel()
		<-inst.handle.Exited()
	}
	inst.releaseExtraNetworksLocked()
	inst.handle = nil
	inst.containerID = ""
	inst.sidecarHandles = nil
	inst.exit = nil
	inst.logsDone = nil
	inst.network = azureWorkloadNetwork{}
	inst.subnetID = ""
	inst.port = 0
	inst.candidates = nil
	inst.address = ""
	inst.references = webReferenceValues{}
	inst.referenceRefresh = nil
}

func stopAzureFunctionInstance(siteName string) {
	azureFunctionInstances.Lock()
	inst := azureFunctionInstances.bySite[siteName]
	delete(azureFunctionInstances.bySite, siteName)
	azureFunctionInstances.Unlock()
	if inst != nil {
		inst.teardown()
	}
	removeSiteHome(siteName)
}

// The next container starts from the site's current configuration — at once
// for an Always On site, otherwise on the next request.
func restartAzureFunctionInstance(site Site) {
	inst := azfInstanceFor(site.Name)
	inst.teardown()
	startAlwaysOnSite(site)
}

// A failed start is recorded in the site's log, and the next request retries
// it.
func startAlwaysOnSite(site Site) {
	if !siteAlwaysOn(&site) || !siteRunsContainer(&site) || siteStopped(&site) {
		return
	}
	go func() {
		inst := azfInstanceFor(site.Name)
		inst.mu.Lock()
		defer inst.mu.Unlock()
		// The site may have been deleted or changed while the start waited.
		current, ok := webJobSite(site.ID)
		if !ok || !siteAlwaysOn(&current) || !siteRunsContainer(&current) || siteStopped(&current) {
			return
		}
		if err := inst.ensureStarted(&current); err != nil {
			injectSiteTrace(&current, fmt.Sprintf("Site start failed: %v", err))
		}
	}()
}
