// Package fabric keeps one cloud's realized network fabric: the network
// namespaces behind its virtual networks, the bridges behind its subnets, the
// interfaces and Firecracker machines attached to them, and the source NAT
// its gateways program. Every simulator drives the same registry, so the
// locking that keeps a network teardown from racing an attach is written once.
package fabric

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
)

// Options names a cloud's host objects and sets its address reservation.
type Options struct {
	// NetworkPrefix and SubnetPrefix start the Linux names of the network
	// namespace and the subnet bridge; LinuxName completes them from the key.
	NetworkPrefix string
	SubnetPrefix  string
	// Reserved is the host reservation every subnet's address manager keeps.
	Reserved realexec.HostReservation
	// OnTapClosed runs for every tap interface the fabric closes, so a cloud
	// can drop what it indexed by the interface's address.
	OnTapClosed func(*realexec.TapNIC)
}

type member[K ~string, T any] struct {
	network K
	value   T
}

type snatBinding[K ~string] struct {
	network  K
	publicIP net.IP
	tables   []string
}

// Fabric is the registry. K is the cloud's resource identifier.
type Fabric[K ~string] struct {
	host *realexec.Host
	opts Options

	// createMu serializes creating networks and subnets. A namespace name is
	// derived from its key, and creating one reclaims a namespace of that name
	// left by a dead process — so two concurrent creates of the same key
	// would have the second destroy the first's namespace.
	createMu sync.Mutex

	mu       sync.RWMutex
	networks map[K]*realexec.Network
	subnets  map[K]member[K, *realexec.Subnet]
	nics     map[K]member[K, *realexec.NamespaceNIC]
	taps     map[K]member[K, *realexec.TapNIC]
	vms      map[K]member[K, *realexec.FirecrackerVM]
	snat     map[string]snatBinding[K]
	ownedIPs map[string]net.IP

	lockMu   sync.Mutex
	netLocks map[K]*keyedLock
	vmLocks  map[K]*keyedLock
}

// keyedLock is one key's lock, kept in its map only while a caller holds or
// waits for it, so the maps shrink as networks and machines go away.
type keyedLock struct {
	sync.RWMutex
	refs int
}

func New[K ~string](opts Options) *Fabric[K] {
	return &Fabric[K]{
		host:     realexec.NewHost(),
		opts:     opts,
		networks: map[K]*realexec.Network{},
		subnets:  map[K]member[K, *realexec.Subnet]{},
		nics:     map[K]member[K, *realexec.NamespaceNIC]{},
		taps:     map[K]member[K, *realexec.TapNIC]{},
		vms:      map[K]member[K, *realexec.FirecrackerVM]{},
		snat:     map[string]snatBinding[K]{},
		ownedIPs: map[string]net.IP{},
		netLocks: map[K]*keyedLock{},
		vmLocks:  map[K]*keyedLock{},
	}
}

// acquireKeyed locks key's lock in locks, shared or exclusive, and returns
// the unlock, which drops the lock from locks once no caller holds or awaits it.
func (f *Fabric[K]) acquireKeyed(locks map[K]*keyedLock, key K, shared bool) (unlock func()) {
	f.lockMu.Lock()
	l := locks[key]
	if l == nil {
		l = &keyedLock{}
		locks[key] = l
	}
	l.refs++
	f.lockMu.Unlock()
	if shared {
		l.RLock()
	} else {
		l.Lock()
	}
	return func() {
		if shared {
			l.RUnlock()
		} else {
			l.Unlock()
		}
		f.lockMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(locks, key)
		}
		f.lockMu.Unlock()
	}
}

// HoldNetwork keeps TeardownNetwork from closing the network until release
// runs. An attach holds it from before it provisions anything until it has
// recorded what it built, so a teardown never closes a namespace mid-attach
// and orphans the half-built interface. Holders do not exclude each other.
func (f *Fabric[K]) HoldNetwork(key K) (release func()) {
	return f.acquireKeyed(f.netLocks, key, true)
}

func (f *Fabric[K]) Network(key K) *realexec.Network {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.networks[key]
}

// EnsureNetwork realizes the network namespace for key once.
func (f *Fabric[K]) EnsureNetwork(ctx context.Context, key K) (*realexec.Network, error) {
	if n := f.Network(key); n != nil {
		return n, nil
	}
	f.createMu.Lock()
	defer f.createMu.Unlock()
	return f.ensureNetworkCreating(ctx, key)
}

func (f *Fabric[K]) ensureNetworkCreating(ctx context.Context, key K) (*realexec.Network, error) {
	if n := f.Network(key); n != nil {
		return n, nil
	}
	n, err := f.host.CreateNetworkNamespace(ctx, LinuxName(f.opts.NetworkPrefix, string(key)))
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.networks[key] = n
	f.mu.Unlock()
	return n, nil
}

func (f *Fabric[K]) Subnet(key K) *realexec.Subnet {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.subnets[key].value
}

// EnsureSubnet realizes subnet key inside network netKey, realizing the
// network first when it is not yet.
func (f *Fabric[K]) EnsureSubnet(ctx context.Context, netKey, key K, cidr string, gateway net.IP) (*realexec.Subnet, error) {
	if s := f.Subnet(key); s != nil {
		return s, nil
	}
	f.createMu.Lock()
	defer f.createMu.Unlock()
	if s := f.Subnet(key); s != nil {
		return s, nil
	}
	network, err := f.ensureNetworkCreating(ctx, netKey)
	if err != nil {
		return nil, err
	}
	s, err := network.CreateSubnet(ctx, realexec.SubnetSpec{
		Name:       string(key),
		BridgeName: LinuxName(f.opts.SubnetPrefix, string(key)),
		CIDR:       cidr,
		Gateway:    gateway,
		Reserved:   f.opts.Reserved,
	})
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.subnets[key] = member[K, *realexec.Subnet]{network: netKey, value: s}
	f.mu.Unlock()
	return s, nil
}

// DeleteSubnet closes a subnet's bridge. Interfaces still on it close with it.
func (f *Fabric[K]) DeleteSubnet(ctx context.Context, key K) error {
	f.mu.Lock()
	s := f.subnets[key]
	delete(f.subnets, key)
	f.mu.Unlock()
	if s.value == nil {
		return nil
	}
	return s.value.Close(ctx)
}

func (f *Fabric[K]) subnetMember(key K) (member[K, *realexec.Subnet], error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	s, ok := f.subnets[key]
	if !ok {
		return s, fmt.Errorf("subnet %s is not realized", key)
	}
	return s, nil
}

func (f *Fabric[K]) NIC(key K) *realexec.NamespaceNIC {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.nics[key].value
}

func (f *Fabric[K]) Tap(key K) *realexec.TapNIC {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.taps[key].value
}

// AttachNamespaceNIC gives interface key a namespace of its own on subnet
// subnetKey, or returns the one it already has.
func (f *Fabric[K]) AttachNamespaceNIC(ctx context.Context, subnetKey, key K, spec realexec.NamespaceNICSpec) (*realexec.NamespaceNIC, error) {
	if nic := f.NIC(key); nic != nil {
		return nic, nil
	}
	s, err := f.subnetMember(subnetKey)
	if err != nil {
		return nil, err
	}
	release := f.HoldNetwork(s.network)
	defer release()
	nic, err := s.value.AttachNamespaceNIC(ctx, spec)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing := f.nics[key].value; existing != nil {
		_ = nic.Close(context.Background())
		return existing, nil
	}
	f.nics[key] = member[K, *realexec.NamespaceNIC]{network: s.network, value: nic}
	return nic, nil
}

// CloseNamespaceNIC closes only the namespace interface of key, leaving a tap
// of the same key in place.
func (f *Fabric[K]) CloseNamespaceNIC(ctx context.Context, key K) error {
	f.mu.Lock()
	nic := f.nics[key].value
	delete(f.nics, key)
	f.mu.Unlock()
	if nic == nil {
		return nil
	}
	return nic.Close(ctx)
}

// DeleteNIC closes both the namespace interface and the tap realizing key.
func (f *Fabric[K]) DeleteNIC(ctx context.Context, key K) error {
	f.mu.Lock()
	nic := f.nics[key].value
	delete(f.nics, key)
	tap := f.taps[key].value
	delete(f.taps, key)
	f.mu.Unlock()
	var errs []error
	if nic != nil {
		errs = append(errs, nic.Close(ctx))
	}
	if tap != nil {
		errs = append(errs, f.closeTap(ctx, tap))
	}
	return errors.Join(errs...)
}

func (f *Fabric[K]) closeTap(ctx context.Context, tap *realexec.TapNIC) error {
	if f.opts.OnTapClosed != nil {
		f.opts.OnTapClosed(tap)
	}
	return tap.Close(ctx)
}

// ApplyIngress programs the ingress filter of every interface realizing key.
// nil stages clears the filter, leaving the interface open; a single empty
// stage admits nothing.
func (f *Fabric[K]) ApplyIngress(ctx context.Context, key K, stages [][]realexec.PacketRule) error {
	nic, tap := f.NIC(key), f.Tap(key)
	var errs []error
	if nic != nil {
		if stages == nil {
			errs = append(errs, nic.ClearIngressFilter(ctx))
		} else {
			errs = append(errs, nic.ConfigureStagedIngressFilter(ctx, stages))
		}
	}
	if tap != nil {
		if stages == nil {
			errs = append(errs, tap.ClearIngressFilter(ctx))
		} else {
			errs = append(errs, tap.ConfigureStagedIngressFilter(ctx, stages))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("configure ingress filter on %s: %w", key, err)
	}
	return nil
}

// Realized reports whether any interface realizes key.
func (f *Fabric[K]) Realized(key K) bool {
	return f.NIC(key) != nil || f.Tap(key) != nil
}

func (f *Fabric[K]) VM(key K) *realexec.FirecrackerVM {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.vms[key].value
}

func (f *Fabric[K]) VMAlive(key K) bool {
	return f.VM(key).Alive()
}

// VMSpec describes one machine boot.
type VMSpec[K ~string] struct {
	Key     K
	Network K
	Subnet  K
	// NIC keys the machine's tap; the tap outlives a stop and is reused by the
	// next start, so the machine keeps its address.
	NIC K
	// EnsureSubnet realizes the subnet when the fabric does not hold it yet.
	EnsureSubnet func(context.Context) error
	Tap          realexec.TapNICSpec
	// MetadataPort and MetadataTable route the cloud's instance metadata
	// address on the subnet to the simulator's metadata listener.
	MetadataPort  int
	MetadataTable string
	Machine       realexec.FirecrackerVMConfig
	// BeforeBoot runs once the tap exists and before the machine boots; it may
	// add to the machine's configuration.
	BeforeBoot func(tap *realexec.TapNIC, machine *realexec.FirecrackerVMConfig) error
}

// StartVM boots the machine unless it is already running. It returns the
// machine and its tap, and whether this call booted it.
func (f *Fabric[K]) StartVM(ctx context.Context, spec VMSpec[K]) (*realexec.FirecrackerVM, *realexec.TapNIC, bool, error) {
	release := f.HoldNetwork(spec.Network)
	defer release()
	defer f.acquireKeyed(f.vmLocks, spec.Key, false)()

	if vm := f.VM(spec.Key); vm.Alive() {
		return vm, f.Tap(spec.NIC), false, nil
	}
	subnet := f.Subnet(spec.Subnet)
	if subnet == nil {
		if spec.EnsureSubnet == nil {
			return nil, nil, false, fmt.Errorf("subnet %s is not realized", spec.Subnet)
		}
		if err := spec.EnsureSubnet(ctx); err != nil {
			return nil, nil, false, err
		}
		if subnet = f.Subnet(spec.Subnet); subnet == nil {
			return nil, nil, false, fmt.Errorf("subnet %s no longer exists", spec.Subnet)
		}
	}
	// Membership follows the subnet, whose network is the one a teardown
	// closes, whatever spelling of it the caller held.
	s, err := f.subnetMember(spec.Subnet)
	if err != nil {
		return nil, nil, false, err
	}
	tap := f.Tap(spec.NIC)
	if tap == nil {
		created, err := subnet.AttachTapNIC(ctx, spec.Tap)
		if err != nil {
			return nil, nil, false, err
		}
		tap = created
		f.mu.Lock()
		f.taps[spec.NIC] = member[K, *realexec.TapNIC]{network: s.network, value: tap}
		f.mu.Unlock()
	}
	machine := spec.Machine
	machine.Tap = tap
	machine.MAC = spec.Tap.MAC
	if spec.BeforeBoot != nil {
		if err := spec.BeforeBoot(tap, &machine); err != nil {
			return nil, tap, false, err
		}
	}
	if err := subnet.ConfigureMetadataDNAT(ctx, spec.MetadataPort, spec.MetadataTable); err != nil {
		return nil, tap, false, fmt.Errorf("configure metadata routing for %s: %w", spec.Key, err)
	}
	vm, err := realexec.StartFirecrackerVM(ctx, machine)
	if err != nil {
		return nil, tap, false, err
	}
	f.mu.Lock()
	old := f.vms[spec.Key].value
	f.vms[spec.Key] = member[K, *realexec.FirecrackerVM]{network: s.network, value: vm}
	f.mu.Unlock()
	if old != nil {
		_ = old.Stop(context.Background())
	}
	return vm, tap, true, nil
}

// StopVM stops the machine; before, when set, runs against it first and
// abandons the stop when it fails.
func (f *Fabric[K]) StopVM(ctx context.Context, key K, before func(*realexec.FirecrackerVM) error) error {
	defer f.acquireKeyed(f.vmLocks, key, false)()
	f.mu.Lock()
	vm := f.vms[key].value
	delete(f.vms, key)
	f.mu.Unlock()
	if vm == nil {
		return nil
	}
	if before != nil {
		if err := before(vm); err != nil {
			return err
		}
	}
	return vm.Stop(ctx)
}

// TeardownNetwork closes everything realized in network key and the network
// itself. It waits for every HoldNetwork holder, and extra, when set, closes
// the cloud's own attachments in the network first.
func (f *Fabric[K]) TeardownNetwork(ctx context.Context, key K, extra func(context.Context)) error {
	defer f.acquireKeyed(f.netLocks, key, false)()
	if extra != nil {
		extra(ctx)
	}
	f.mu.Lock()
	network := f.networks[key]
	delete(f.networks, key)
	var vms []*realexec.FirecrackerVM
	for k, m := range f.vms {
		if m.network == key {
			vms = append(vms, m.value)
			delete(f.vms, k)
		}
	}
	var taps []*realexec.TapNIC
	for k, m := range f.taps {
		if m.network == key {
			taps = append(taps, m.value)
			delete(f.taps, k)
		}
	}
	var nics []*realexec.NamespaceNIC
	for k, m := range f.nics {
		if m.network == key {
			nics = append(nics, m.value)
			delete(f.nics, k)
		}
	}
	var subnets []*realexec.Subnet
	for k, m := range f.subnets {
		if m.network == key {
			subnets = append(subnets, m.value)
			delete(f.subnets, k)
		}
	}
	for owner, b := range f.snat {
		if b.network == key {
			delete(f.snat, owner)
		}
	}
	f.mu.Unlock()
	for _, vm := range vms {
		_ = vm.Stop(ctx)
	}
	for _, tap := range taps {
		_ = f.closeTap(ctx, tap)
	}
	for _, nic := range nics {
		_ = nic.Close(ctx)
	}
	for _, s := range subnets {
		_ = s.Close(ctx)
	}
	if network == nil {
		return nil
	}
	return network.Close(ctx)
}

// ReservePublicIP returns the public address owner holds, reserving one from
// reserve the first time. The address stays owned until ReleaseOwned.
func (f *Fabric[K]) ReservePublicIP(owner string, reserve func(owner string, requested net.IP) (net.IP, error)) (net.IP, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ip := f.ownedIPs[owner]; ip != nil {
		return ip, nil
	}
	ip, err := reserve(owner, nil)
	if err != nil {
		return nil, err
	}
	f.ownedIPs[owner] = ip
	return ip, nil
}

// OwnedPublicIP is the address ReservePublicIP gave owner, or nil.
func (f *Fabric[K]) OwnedPublicIP(owner string) net.IP {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.ownedIPs[owner]
}

// ConfigureSNAT makes network key translate traffic from cidrs to publicIP
// on behalf of owner, replacing whatever owner programmed before. An empty
// cidrs removes owner's translation.
func (f *Fabric[K]) ConfigureSNAT(ctx context.Context, key K, owner string, cidrs []string, publicIP net.IP) error {
	if err := f.releaseSNAT(ctx, owner); err != nil {
		return err
	}
	if len(cidrs) == 0 {
		return nil
	}
	release := f.HoldNetwork(key)
	defer release()
	network := f.Network(key)
	if network == nil {
		return fmt.Errorf("network %s is not realized", key)
	}
	sorted := append([]string(nil), cidrs...)
	sort.Strings(sorted)
	binding := snatBinding[K]{network: key, publicIP: publicIP}
	for _, cidr := range sorted {
		table := LinuxName("sn", owner+"|"+cidr)
		binding.tables = append(binding.tables, table)
		if err := network.ConfigureSNAT(ctx, cidr, publicIP, table); err != nil {
			f.mu.Lock()
			f.snat[owner] = binding
			f.mu.Unlock()
			return err
		}
	}
	f.mu.Lock()
	f.snat[owner] = binding
	f.mu.Unlock()
	return nil
}

func (f *Fabric[K]) releaseSNAT(ctx context.Context, owner string) error {
	f.mu.Lock()
	binding, ok := f.snat[owner]
	delete(f.snat, owner)
	stillUsed := false
	for _, other := range f.snat {
		if other.network == binding.network && other.publicIP.Equal(binding.publicIP) {
			stillUsed = true
		}
	}
	network := f.networks[binding.network]
	f.mu.Unlock()
	if !ok || network == nil {
		return nil
	}
	var errs []error
	for _, table := range binding.tables {
		errs = append(errs, network.RemoveSNAT(ctx, table))
	}
	if !stillUsed {
		errs = append(errs, network.RemoveSNATAddress(ctx, binding.publicIP))
	}
	return errors.Join(errs...)
}

// ReleaseOwned removes owner's source NAT and returns the public address it
// reserved to the pool it came from.
func (f *Fabric[K]) ReleaseOwned(ctx context.Context, owner string) error {
	err := f.releaseSNAT(ctx, owner)
	f.mu.Lock()
	ip := f.ownedIPs[owner]
	delete(f.ownedIPs, owner)
	f.mu.Unlock()
	if ip != nil {
		realexec.ReleasePublicIPv4(ip)
	}
	return err
}

// SNATOwners lists every owner with source NAT programmed.
func (f *Fabric[K]) SNATOwners() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]string, 0, len(f.snat))
	for owner := range f.snat {
		out = append(out, owner)
	}
	sort.Strings(out)
	return out
}

// ReconcileLiveness hands markLost every key among claimedRunning whose
// machine is not alive: the record says running, and nothing runs.
func ReconcileLiveness[K comparable](claimedRunning []K, alive func(K) bool, markLost func(K)) {
	for _, key := range claimedRunning {
		if !alive(key) {
			markLost(key)
		}
	}
}
