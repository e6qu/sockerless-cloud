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

// azureFunctionInstance tracks the container App Service runs for a site. The
// platform keeps one container per site instance: a request to the site starts
// it when none is running, Always On starts it without one, and a restart, a
// configuration change or the site's deletion tears it down. The per-instance
// mutex serializes the start so concurrent requests share one container.
type azureFunctionInstance struct {
	mu sync.Mutex
	// handle is the placed site container, containerID its identifier.
	handle         placement.Handle
	containerID    string
	sidecarHandles []placement.Handle
	// exit reports when the main container stops running; logsDone closes
	// once its output has been read to the end, which is when it exits.
	exit     *containerExit
	logsDone <-chan struct{}
	// network is the engine network the container runs on, and subnetID the
	// subnet it leased an address in when the site integrates into a virtual
	// network's subnet.
	network  azureWorkloadNetwork
	subnetID string
	// extraNetworks are the further virtual networks the container joined.
	extraNetworks []siteExtraNetwork
	// port is the container port the front end forwards the site's requests
	// to; candidates are the addresses that port may be reached at, and
	// address is the one that answered.
	port       int
	candidates []string
	address    string
	// stopGrace is the WEBSITES_CONTAINER_STOP_TIME_LIMIT the containers were
	// started under, applied when they are torn down.
	stopGrace time.Duration
	// vnets are the virtual networks (by lower-cased resource id) the site
	// joined through VNet integration, set by the virtualNetwork connection
	// handlers (both the swift and the classic spelling). The site's container
	// runs on the first one's engine network, at an address leased in the
	// integration subnet, with that network's sidecar as its resolver. They
	// outlive a restart, as the integration does.
	vnets []string
	// references are the Key Vault reference values the running containers
	// started with, and referenceRefresh re-fetches them when the platform's
	// cache of them expires.
	references       webReferenceValues
	referenceRefresh *bg.Timer
}

// addNetworkLocked records a virtual network the site integrated into.
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

// removeNetworkLocked forgets a virtual network. Caller holds inst.mu.
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

// siteNetworkLocked is the engine network the site's container runs on: the
// first virtual network it integrated into, with an address leased in the
// integration subnet, or the platform network. Caller holds inst.mu.
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

// siteConnectionSubnet is the subnet the site's connection to the virtual
// network names, else the network's first subnet, else "".
func siteConnectionSubnet(site *Site, vnet VirtualNetwork) string {
	for _, conn := range siteVnetConnections(site.ID) {
		if strings.Contains(conn.Properties.VnetResourceID, "/subnets/") &&
			strings.EqualFold(azureVNetIDOfSubnet(conn.Properties.VnetResourceID), vnet.ID) {
			return conn.Properties.VnetResourceID
		}
	}
	return azureWholeVNetSubnet(vnet)
}

// siteVNetNetwork is the engine network a site's container joins for its
// connection to the virtual network: the network of the prefix its subnet
// lies in, or the first prefix's for a connection naming no subnet.
func siteVNetNetwork(ctx context.Context, vnet VirtualNetwork, subnetID string) (azureWorkloadNetwork, error) {
	if subnetID != "" {
		return azureSubnetNetwork(ctx, placementWeb, subnetID)
	}
	return azureVNetNetworkFor(ctx, placementWeb, vnet.ID)
}

// siteNetworkCurrentLocked reports whether the running container is on the
// networks the site's integration names now. Caller holds inst.mu.
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

// extraNetworksLocked leases the workload an address on the engine network
// of every further virtual network the site integrated into through a
// classic connection, in that connection's subnet, and returns the leases
// with the attachments the placement makes: a container joins each network
// after it starts, and a guest boots with an interface on each. A further
// network's agent joins it here for a service on guests. Caller holds
// inst.mu.
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
		// A connection to a virtual network that has no subnet leaves the
		// address to the engine, which a guest cannot take: its backend
		// refuses the placement.
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

// siteExtraNetwork is a further virtual network the site's workload has an
// interface on, the address it holds there, and the lease's owner.
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

// azfInstanceFor returns the (lazily created) instance holder for a site,
// creating an empty one on first reference. The returned holder's own mutex
// guards its container lifecycle.
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

// ensureStarted starts the site's container if none is running, or restarts
// one whose VNet integration changed; a stopped site runs nothing. Caller
// holds inst.mu.
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
			// A changed VNet integration restarts the site on its new
			// network, as App Service restarts the app.
			inst.teardownLocked()
		}
	}
	return inst.startLocked(site)
}

// siteImageMissing explains why the simulator has nothing to run for a site.
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

// startLocked runs the site's container the way App Service runs a Linux
// custom container: the image's own entrypoint, with the site's startup command
// (siteConfig.appCommandLine, or a sitecontainer's startUpCommand) as the
// container command in place of the image's CMD, the app settings and PORT in
// its environment (for a main sitecontainer, the app settings only when it
// inherits them), and any sidecar sitecontainers in its network namespace.
// Caller holds inst.mu.
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
		// The stack image's entrypoint takes the startup command as its
		// arguments and hands it to Oryx as the user startup command.
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
		// The host image's entrypoint starts the Functions host on PORT; App
		// Service runs it without a startup command.
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
		// A shared named volume like gitlab-runner's /builds dir maps to one
		// Azure Files share, so every container that mounts it sees the same
		// workspace.
		binds = siteAzureStorageBinds(site)
		// A custom container mounts the site's persistent /home only when
		// WEBSITES_ENABLE_APP_SERVICE_STORAGE is true.
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

	// The host pulls the site's image with the credential the site declared
	// for its registry — its Azure Container Registry managed identity or
	// its DOCKER_REGISTRY_SERVER_* settings — as App Service does.
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

	// Sidecar sitecontainers share the main's network namespace, so a
	// sidecar that binds a port is reachable from the main on
	// localhost:<port> — the App Service multi-container loopback contract.
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

// releaseExtraNetworksLocked returns the leases the workload held on its
// further virtual networks. Caller holds inst.mu.
func (inst *azureFunctionInstance) releaseExtraNetworksLocked() {
	for _, extra := range inst.extraNetworks {
		extra.releaseSubnetLease()
	}
	inst.extraNetworks = nil
}

// siteContainerCandidates returns the addresses the site's port may answer at:
// the container's address on its network, which reaches the port from the
// host and from a harness container alike, and the port the engine publishes
// on the host's loopback, which also reaches it where container addresses are
// not routed. A container that has already stopped publishes no port and has
// no address, so it has no candidates, and the start reports its exit.
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
		// A guest publishes no port on the host's loopback; its address is
		// the one route to it.
		if handle.Kind() == placement.MicroVM && len(candidates) > 0 {
			return candidates, nil
		}
		return nil, fmt.Errorf("read the site's published port: %w", err)
	}
	return append(candidates, fmt.Sprintf("http://127.0.0.1:%d", hostPort)), nil
}

// containerExit reports when a placed container stops running: done closes
// when it has, and the handle then holds its exit status.
type containerExit struct {
	done   <-chan struct{}
	handle placement.Handle
}

func newContainerExit(handle placement.Handle) *containerExit {
	return &containerExit{done: handle.Exited(), handle: handle}
}

// exitedBeforeAnswering is the start failure of a site whose container stopped
// before its port answered.
func (e *containerExit) exitedBeforeAnswering(site string, port int) error {
	code := e.handle.Wait().ExitCode
	if code < 0 {
		return fmt.Errorf("site %q container exited before it answered on port %d", site, port)
	}
	return fmt.Errorf("site %q container exited before it answered on port %d: exit code %d", site, port, code)
}

// teardown is teardownLocked under the instance's lock.
func (inst *azureFunctionInstance) teardown() {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.teardownLocked()
}

// running reads the container the instance runs and its handle under the
// instance's lock.
func (inst *azureFunctionInstance) running() (string, placement.Handle) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.containerID, inst.handle
}

// siteStart is what a started site container answers on.
type siteStart struct {
	containerID string
	port        int
	candidates  []string
	address     string
	exit        *containerExit
	logsDone    <-chan struct{}
}

// start starts the site's container if none runs and reads what it answers
// on, under the instance's lock.
func (inst *azureFunctionInstance) start(site *Site) (siteStart, error) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if err := inst.ensureStarted(site); err != nil {
		return siteStart{}, err
	}
	return siteStart{containerID: inst.containerID, port: inst.port, candidates: inst.candidates,
		address: inst.address, exit: inst.exit, logsDone: inst.logsDone}, nil
}

// teardownLocked stops the instance's main container, its sidecars, and its log
// stream, clearing the recorded handles. The VNet-integration networks stay
// recorded: the integration belongs to the site, not to one container. Caller
// holds inst.mu.
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

// stopAzureFunctionInstance tears a deleted site's container down and forgets
// the site.
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

// restartAzureFunctionInstance restarts a site the way App Service does after a
// restart request or a configuration change: the running container stops, and
// the next one starts from the site's current configuration — at once for an
// Always On site, otherwise on the next request.
func restartAzureFunctionInstance(site Site) {
	inst := azfInstanceFor(site.Name)
	inst.teardown()
	startAlwaysOnSite(site)
}

// startAlwaysOnSite starts an Always On site's container in the background, as
// the platform does once the site is created or changed; a start that fails is
// recorded in the site's log, and the next request retries it.
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
