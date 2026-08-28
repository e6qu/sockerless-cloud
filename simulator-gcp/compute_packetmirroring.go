package main

import (
	"log"
	"strings"
	"sync"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Google Compute Engine packet mirroring. A policy names a set of mirrored
// resources — instances, whole subnetworks, or every instance carrying a
// network tag — and a collector internal load balancer, and from then on a copy
// of the mirrored traffic arrives at the load balancer's backends.
//
// That last clause is the resource. A policy that stored `enable: true` and
// forwarded nothing would read back perfectly through every client and be
// entirely fictional, which is why this landed with the send half of the
// substrate (realexec.StartMirror) rather than before it: the read half the
// packet capture already provided is only half a mirroring policy.
//
// The collector is resolved the way the resource defines it — collectorIlb is a
// forwarding rule, whose backend service names instance groups, whose members
// are the instances that receive the traffic — so pointing a policy at a real
// internal load balancer delivers to the instances really behind it.

// gcpRegionForwardingRules and gcpRegionBackendServices are the regional
// collections a collectorIlb resolves through. They are assigned where those
// collections are registered.
var (
	gcpRegionForwardingRules sim.Store[map[string]any]
	gcpRegionBackendServices sim.Store[map[string]any]
)

// gcpPacketMirrorings holds the policies. It is the same map-backed store the
// other metadata Compute collections use; the sessions it drives are kept
// separately because a running socket is not persistable state.
var gcpPacketMirrorings sim.Store[map[string]any]

// gcpMirrorSessions holds the live mirroring sessions, keyed by policy key then
// by the mirrored interface, so reconciling a policy can tell which sessions it
// already owns.
var (
	gcpMirrorMu       sync.Mutex
	gcpMirrorSessions = map[string]map[string]*realexec.Mirror{}
)

func registerComputePacketMirroring(srv *sim.Server) {
	gcpPacketMirrorings = sim.MakeStore[map[string]any](srv.DB(), "compute_packet_mirrorings")
	(computeMetaResource{
		collection: "packetMirrorings", kind: "compute#packetMirroring",
		scope: cScopeRegion, store: gcpPacketMirrorings,
		patch: true, aggregated: true,
		reconcile: gcpReconcilePacketMirroring,
	}).register(srv)
	// The Discovery document exposes testIamPermissions on packetMirrorings
	// and neither of the other two IAM verbs.
	registerComputeTestIamOnly(srv, cScopeRegion, "packetMirrorings")
}

// gcpReconcilePacketMirroring brings the running mirroring sessions in line
// with the policy stored under key: it starts a session for every mirrored
// interface the policy selects and stops the ones it no longer does. A deleted
// or disabled policy therefore stops mirroring, and a patched one starts
// mirroring the resources it gained.
//
// A host that cannot mirror leaves the policy stored and mirrors nothing, and
// says so in the log rather than silently: the control-plane resource is real
// metadata on every platform, but the traffic only moves on a Linux host with
// packet-socket support. That asymmetry is the same one packet captures carry.
func gcpReconcilePacketMirroring(key string) {
	policy, stored := gcpPacketMirrorings.Get(key)

	wanted := map[string]realexec.MirrorSpec{}
	if stored && gcpPacketMirroringEnabled(policy) {
		collectors := gcpPacketMirroringCollectors(policy)
		if len(collectors) > 0 {
			direction, filters := gcpPacketMirroringFilter(policy)
			for _, source := range gcpPacketMirroringSources(policy) {
				wanted[source.NamespaceName+"/"+source.InterfaceName] = realexec.MirrorSpec{
					SourceNamespace: source.NamespaceName,
					SourceInterface: source.InterfaceName,
					Collectors:      collectors,
					Filters:         filters,
					Direction:       direction,
				}
			}
		}
	}

	gcpMirrorMu.Lock()
	defer gcpMirrorMu.Unlock()
	running := gcpMirrorSessions[key]
	if running == nil {
		running = map[string]*realexec.Mirror{}
	}
	for id, session := range running {
		if _, keep := wanted[id]; !keep {
			_ = session.Stop()
			delete(running, id)
		}
	}
	for id, spec := range wanted {
		if _, already := running[id]; already {
			continue
		}
		session, err := realexec.StartMirror(spec)
		if err != nil {
			log.Printf("packet mirroring %s: %v", key, err)
			continue
		}
		running[id] = session
	}
	if len(running) == 0 {
		delete(gcpMirrorSessions, key)
		return
	}
	gcpMirrorSessions[key] = running
}

// gcpPacketMirroringEnabled reads the policy's enable member. The Discovery
// document types it as a string enum ("TRUE"/"FALSE") whose default is TRUE,
// and clients also send the JSON boolean, so both are read.
func gcpPacketMirroringEnabled(policy map[string]any) bool {
	switch v := policy["enable"].(type) {
	case string:
		return !strings.EqualFold(v, "FALSE")
	case bool:
		return v
	default:
		return true
	}
}

// gcpPacketMirroringFilter maps the policy's filter onto the substrate's.
// An absent filter mirrors everything, which is what the resource documents.
func gcpPacketMirroringFilter(policy map[string]any) (realexec.MirrorDirection, []realexec.CaptureFilter) {
	filter, _ := policy["filter"].(map[string]any)
	if filter == nil {
		return realexec.MirrorBoth, nil
	}
	direction := realexec.MirrorBoth
	if d, _ := filter["direction"].(string); d != "" {
		direction = realexec.MirrorDirection(strings.ToUpper(d))
	}
	protocols := gcpStringList(filter["IPProtocols"])
	ranges := gcpStringList(filter["cidrRanges"])
	if len(protocols) == 0 && len(ranges) == 0 {
		return direction, nil
	}
	// The policy's protocols and CIDR ranges are alternatives: traffic
	// matching any listed protocol and any listed range is mirrored, so the
	// cross-product is expressed as one filter per combination.
	if len(protocols) == 0 {
		protocols = []string{""}
	}
	if len(ranges) == 0 {
		ranges = []string{""}
	}
	var filters []realexec.CaptureFilter
	for _, protocol := range protocols {
		for _, cidr := range ranges {
			filters = append(filters, realexec.CaptureFilter{
				Protocol:     protocol,
				LocalAddress: cidr,
			})
		}
	}
	return direction, filters
}

// gcpPacketMirroringSources resolves the policy's mirrored resources to the
// interfaces their traffic crosses. All three selectors the resource defines
// are honoured: named instances, every instance in a named subnetwork, and
// every instance carrying a named network tag.
func gcpPacketMirroringSources(policy map[string]any) []realexec.MirrorTarget {
	mirrored, _ := policy["mirroredResources"].(map[string]any)
	if mirrored == nil {
		return nil
	}
	selected := map[string]bool{}
	for _, url := range gcpResourceInfoURLs(mirrored["instances"]) {
		selected[gcpComputeResourcePath(url)] = true
	}

	subnets := map[string]bool{}
	for _, url := range gcpResourceInfoURLs(mirrored["subnetworks"]) {
		subnets[gcpComputeResourcePath(url)] = true
	}
	tags := gcpStringList(mirrored["tags"])
	if len(subnets) > 0 || len(tags) > 0 {
		for _, inst := range gcpInstances.List() {
			if gcpInstanceHasAnyTag(inst, tags) {
				selected[gcpComputeResourcePath(inst.SelfLink)] = true
				continue
			}
			for _, ni := range inst.NetworkInterfaces {
				if subnets[gcpComputeResourcePath(ni.Subnetwork)] {
					selected[gcpComputeResourcePath(inst.SelfLink)] = true
					break
				}
			}
		}
	}

	var targets []realexec.MirrorTarget
	for path := range selected {
		targets = append(targets, gcpInstanceMirrorTargets(path)...)
	}
	return targets
}

// gcpPacketMirroringCollectors resolves collectorIlb — a forwarding rule — to
// the interfaces of the instances behind its backend service, which is where a
// real collector's traffic lands.
func gcpPacketMirroringCollectors(policy map[string]any) []realexec.MirrorTarget {
	collector, _ := policy["collectorIlb"].(map[string]any)
	if collector == nil {
		return nil
	}
	url, _ := collector["url"].(string)
	if url == "" {
		return nil
	}
	rule, ok := gcpLookupComputeResource(gcpRegionForwardingRules, url)
	if !ok {
		return nil
	}
	backendServiceURL, _ := rule["backendService"].(string)
	if backendServiceURL == "" {
		return nil
	}
	service, ok := gcpLookupComputeResource(gcpRegionBackendServices, backendServiceURL)
	if !ok {
		return nil
	}
	backends, _ := service["backends"].([]any)
	var targets []realexec.MirrorTarget
	for _, entry := range backends {
		backend, _ := entry.(map[string]any)
		group, _ := backend["group"].(string)
		if group == "" {
			continue
		}
		for _, instanceURL := range gcpInstanceGroupMemberURLs(group) {
			targets = append(targets, gcpInstanceMirrorTargets(gcpComputeResourcePath(instanceURL))...)
		}
	}
	return targets
}

func gcpInstanceGroupMemberURLs(groupURL string) []string {
	if gcpInstanceGroups == nil {
		return nil
	}
	path := gcpComputeResourcePath(groupURL)
	for _, group := range gcpInstanceGroups.List() {
		if gcpComputeResourcePath(group.SelfLink) != path {
			continue
		}
		urls := make([]string, 0, len(group.Instances))
		for _, member := range group.Instances {
			urls = append(urls, member.Instance)
		}
		return urls
	}
	return nil
}

// gcpInstanceMirrorTargets returns the interfaces an instance's traffic
// crosses. An instance with no live interface contributes none, so a policy
// naming a stopped instance mirrors nothing for it rather than appearing to.
func gcpInstanceMirrorTargets(instancePath string) []realexec.MirrorTarget {
	var inst *ComputeInstance
	for _, candidate := range gcpInstances.List() {
		if gcpComputeResourcePath(candidate.SelfLink) == instancePath {
			c := candidate
			inst = &c
			break
		}
	}
	if inst == nil {
		return nil
	}
	var targets []realexec.MirrorTarget
	gcpRealMu.RLock()
	defer gcpRealMu.RUnlock()
	for _, ni := range inst.NetworkInterfaces {
		nicID := inst.SelfLink + "/" + ni.Name
		if tap := gcpRealVMNICs[nicID]; tap != nil {
			targets = append(targets, realexec.MirrorTarget{
				NamespaceName: tap.NetworkNamespace(),
				InterfaceName: tap.TapName,
			})
			continue
		}
		if nic := gcpRealNICs[nicID]; nic != nil {
			targets = append(targets, realexec.MirrorTarget{
				NamespaceName: nic.NamespaceName,
				InterfaceName: nic.HostVethName,
			})
		}
	}
	return targets
}

// gcpLookupComputeResource finds a map-store resource by any of the URL forms a
// client may send: the full selfLink, the /compute/v1-relative path, or a
// partial URL.
func gcpLookupComputeResource(store sim.Store[map[string]any], url string) (map[string]any, bool) {
	if store == nil {
		return nil, false
	}
	path := gcpComputeResourcePath(url)
	if m, ok := store.Get(path); ok {
		return m, true
	}
	for _, m := range store.List() {
		selfLink, _ := m["selfLink"].(string)
		if selfLink != "" && gcpComputeResourcePath(selfLink) == path {
			return m, true
		}
	}
	return nil, false
}

// gcpComputeResourcePath reduces any of the URL forms Compute accepts to the
// "projects/…" path they share, so a resource referenced as a full selfLink and
// one referenced as a partial URL resolve to the same resource — which is what
// the API does.
func gcpComputeResourcePath(url string) string {
	if i := strings.Index(url, "/compute/v1/"); i >= 0 {
		return url[i+len("/compute/v1/"):]
	}
	if i := strings.Index(url, "/compute/beta/"); i >= 0 {
		return url[i+len("/compute/beta/"):]
	}
	return strings.TrimPrefix(url, "/")
}

// gcpResourceInfoURLs reads the url member out of a list of the
// {url, canonicalUrl} info objects packet mirroring uses for its resource
// references.
func gcpResourceInfoURLs(node any) []string {
	entries, _ := node.([]any)
	var urls []string
	for _, entry := range entries {
		info, _ := entry.(map[string]any)
		if url, _ := info["url"].(string); url != "" {
			urls = append(urls, url)
		}
	}
	return urls
}

func gcpStringList(node any) []string {
	entries, _ := node.([]any)
	var out []string
	for _, entry := range entries {
		if s, _ := entry.(string); s != "" {
			out = append(out, s)
		}
	}
	return out
}
