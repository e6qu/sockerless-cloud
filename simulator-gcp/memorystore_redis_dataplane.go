package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"strconv"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/blobstore"
)

// msRedisEnginesEnabled is whether the simulator runs workloads; one started
// API-only runs no engines even when the process holds a container client.
var msRedisEnginesEnabled bool

func msRedisRunsEngines() bool {
	return msRedisEnginesEnabled && sim.RequireContainerRuntime("the Memorystore for Redis data plane") == nil
}

func msRedisPlaneLabels(name string) map[string]string {
	return map[string]string{"sockerless-memorystore": name}
}

func msRedisNewPlane(name, image string, topology msRedisTopology, record msRedisPlaneRecord, configs [][2]string) *msRedisPlane {
	plane := &msRedisPlane{
		name:     name,
		volume:   msRedisVolume(name),
		labels:   msRedisPlaneLabels(name),
		topology: topology,
		password: record.AuthString,
		configs:  configs,
		image:    image,
		primary:  record.Primary,
	}
	msRedisPlanes.Store(name, plane)
	return plane
}

// msRedisInstanceReplicas is the replica count an instance runs: none on the
// Basic tier, one on the Standard tier without read replicas, and the
// requested count — two when unset — with them.
func msRedisInstanceReplicas(tier, readReplicasMode string, requested int) int {
	if tier != "STANDARD_HA" {
		return 0
	}
	if readReplicasMode != "READ_REPLICAS_ENABLED" {
		return 1
	}
	if requested == 0 {
		return 2
	}
	return requested
}

// msRedisInstallInstance binds an instance's endpoints and records them on the
// instance. Without a container runtime, or without a loopback address at
// Redis's port, no engine serves the instance: it reports no host, and the
// operator reads why on stderr.
func msRedisInstallInstance(inst *MSRedisInstance, record *msRedisPlaneRecord, image string, configs [][2]string) {
	if !msRedisRunsEngines() {
		return
	}
	primary, primaryIP, err := msRedisListen(inst.Name, msRedisPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sim-memorystore] instance %s has no endpoint: %v\n", inst.Name, err)
		return
	}
	var read net.Listener
	var readIP string
	if inst.ReadReplicasMode == "READ_REPLICAS_ENABLED" {
		if read, readIP, err = msRedisListen(inst.Name+"#read", msRedisPort); err != nil {
			_ = primary.Close()
			fmt.Fprintf(os.Stderr, "[sim-memorystore] instance %s has no read endpoint: %v\n", inst.Name, err)
			return
		}
	}
	record.Replicas = inst.ReplicaCount
	record.Endpoints = []msRedisEndpointRecord{{Role: msRedisPrimaryEndpoint, Address: primaryIP}}
	plane := msRedisNewPlane(inst.Name, image, msRedisTopology{Replicas: record.Replicas}, *record, configs)
	plane.serve(primary, plane.primaryTarget)
	inst.Host, inst.Port = primaryIP, msRedisPort
	if read != nil {
		record.Endpoints = append(record.Endpoints, msRedisEndpointRecord{Role: msRedisReadEndpoint, Address: readIP})
		plane.serve(read, plane.readTarget)
		inst.ReadEndpoint, inst.ReadEndpointPort = readIP, msRedisPort
	}
}

// msRedisInstallCluster binds a cluster's discovery endpoint and one endpoint
// per node, which is the address that node announces to clients.
func msRedisInstallCluster(cluster *MSRedisCluster, record *msRedisPlaneRecord) {
	if !msRedisRunsEngines() {
		return
	}
	topology := msRedisTopology{Cluster: true, Shards: cluster.ShardCount, Replicas: cluster.ReplicaCount}
	var listeners []net.Listener
	fail := func(err error) {
		for _, listener := range listeners {
			_ = listener.Close()
		}
		fmt.Fprintf(os.Stderr, "[sim-memorystore] cluster %s has no endpoint: %v\n", cluster.Name, err)
	}
	discovery, discoveryIP, err := msRedisListen(cluster.Name, msRedisPort)
	if err != nil {
		fail(err)
		return
	}
	listeners = append(listeners, discovery)
	endpoints := []msRedisEndpointRecord{{Role: msRedisDiscoveryEndpoint, Address: discoveryIP}}
	for node := 0; node < topology.nodes(); node++ {
		listener, ip, err := msRedisListen(cluster.Name+"#node-"+strconv.Itoa(node), msRedisNodePort(node))
		if err != nil {
			fail(err)
			return
		}
		listeners = append(listeners, listener)
		topology.Announce = append(topology.Announce, ip)
		endpoints = append(endpoints, msRedisEndpointRecord{Role: msRedisNodeEndpoint, Address: ip, Node: node})
	}
	record.Endpoints, record.Shards, record.Replicas = endpoints, topology.Shards, topology.Replicas
	configs, _ := msRedisEngineDirectives(0, cluster.RedisConfigs)
	plane := msRedisNewPlane(cluster.Name, msRedisClusterEngineImage, topology, *record, configs)
	plane.serve(discovery, fixedTarget(0))
	for node := 0; node < topology.nodes(); node++ {
		plane.serve(listeners[node+1], fixedTarget(node))
	}
	cluster.DiscoveryEndpoints = []MSRedisDiscoveryEndpoint{{Address: discoveryIP, Port: msRedisPort}}
}

// msRedisStartEngine brings a new resource's engine up inside the create, so
// the operation that reports the resource ready reports an engine serving at
// its endpoints. A resource whose engine does not start is not created.
func msRedisStartEngine(name string) error {
	plane, ok := msRedisLoadPlane(name)
	if !ok {
		return nil
	}
	if err := plane.Ensure(); err != nil {
		msRedisRemovePlane(name)
		return err
	}
	return nil
}

// msRedisRecoverPlanes rebinds every recorded endpoint after a control-plane
// restart and re-adopts the engine containers an earlier process left.
func msRedisRecoverPlanes() error {
	for _, inst := range msRedisInstances.List() {
		record, ok := msRedisPlaneRecords.Get(inst.Name)
		if !ok || len(record.Endpoints) == 0 {
			continue
		}
		image, _ := msRedisEngineImage(inst.RedisVersion)
		configs, _ := msRedisEngineDirectives(inst.MemorySizeGb, inst.RedisConfigs)
		plane := msRedisNewPlane(inst.Name, image, msRedisTopology{Replicas: record.Replicas}, record, configs)
		if err := msRedisRecoverEndpoints(plane, record); err != nil {
			return err
		}
	}
	for _, cluster := range msRedisClusters.List() {
		record, ok := msRedisPlaneRecords.Get(cluster.Name)
		if !ok || len(record.Endpoints) == 0 {
			continue
		}
		topology := msRedisTopology{Cluster: true, Shards: record.Shards, Replicas: record.Replicas}
		for _, endpoint := range record.Endpoints {
			if endpoint.Role == msRedisNodeEndpoint {
				topology.Announce = append(topology.Announce, endpoint.Address)
			}
		}
		configs, _ := msRedisEngineDirectives(0, cluster.RedisConfigs)
		plane := msRedisNewPlane(cluster.Name, msRedisClusterEngineImage, topology, record, configs)
		if err := msRedisRecoverEndpoints(plane, record); err != nil {
			return err
		}
	}
	return nil
}

func msRedisRecoverEndpoints(plane *msRedisPlane, record msRedisPlaneRecord) error {
	if err := plane.Adopt(); err != nil {
		return fmt.Errorf("re-adopt the Redis engine of %s: %w", plane.name, err)
	}
	for _, endpoint := range record.Endpoints {
		port := msRedisPort
		if endpoint.Role == msRedisNodeEndpoint {
			port = msRedisNodePort(endpoint.Node)
		}
		listener, err := net.Listen("tcp", net.JoinHostPort(endpoint.Address, strconv.Itoa(port)))
		if err != nil {
			return fmt.Errorf("rebind %s at %s: %w", plane.name, endpoint.Address, err)
		}
		switch endpoint.Role {
		case msRedisPrimaryEndpoint:
			plane.serve(listener, plane.primaryTarget)
		case msRedisReadEndpoint:
			plane.serve(listener, plane.readTarget)
		case msRedisDiscoveryEndpoint:
			plane.serve(listener, fixedTarget(0))
		case msRedisNodeEndpoint:
			plane.serve(listener, fixedTarget(endpoint.Node))
		}
	}
	return nil
}

// msRedisEngine is the plane serving a resource, or why none does.
func msRedisEngine(name string) (*msRedisPlane, error) {
	plane, ok := msRedisLoadPlane(name)
	if !ok {
		return nil, fmt.Errorf("no Redis engine serves %s: this simulator runs API-only or the host offers no loopback address at port %d", name, msRedisPort)
	}
	return plane, nil
}

// A cluster backup holds the RDB snapshot of every shard, as payloads.
type msRedisBackupFileContent struct {
	FileName string `json:"fileName"`
	Ref      string `json:"ref"`
}

type msRedisBackupContent struct {
	Files []msRedisBackupFileContent `json:"files"`
}

var (
	msRedisBackupContents sim.Store[msRedisBackupContent]
	msRedisBackupPayloads *blobstore.Payloads
)

func msRedisOpenBackupPayloads(srv *sim.Server) {
	payloads, err := srv.Payloads("memorystore-redis-backups")
	if err != nil {
		log.Fatalf("Memorystore for Redis backup contents: %v", err)
	}
	referenced := map[string]bool{}
	for _, content := range msRedisBackupContents.List() {
		for _, file := range content.Files {
			referenced[file.Ref] = true
		}
	}
	if _, err := payloads.Sweep(func(ref string) bool { return referenced[ref] }); err != nil {
		log.Fatalf("Memorystore for Redis backup contents: %v", err)
	}
	msRedisBackupPayloads = payloads
}

func msRedisReleaseBackup(name string) {
	content, ok := msRedisBackupContents.Get(name)
	if !ok {
		return
	}
	msRedisBackupContents.Delete(name)
	for _, file := range content.Files {
		if err := msRedisBackupPayloads.Remove(file.Ref); err != nil {
			log.Printf("Memorystore backup %s: release %s: %v", name, file.FileName, err)
		}
	}
}
