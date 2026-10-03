package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/blobstore"
)

// msRedisEnginesEnabled is whether the simulator runs workloads; one started
// API-only runs no engines even when the process holds a container client.
var msRedisEnginesEnabled bool

func msRedisRunsEngines() bool {
	return msRedisEnginesEnabled && sim.RequireContainerRuntime("the Memorystore for Redis data plane") == nil
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

// msRedisInstanceTLS reports whether an instance encrypts in transit.
func msRedisInstanceTLS(inst MSRedisInstance) bool {
	return inst.TransitEncryptionMode == "SERVER_AUTHENTICATION"
}

// msRedisClusterTLS reports whether a cluster encrypts in transit.
func msRedisClusterTLS(cluster MSRedisCluster) bool {
	return cluster.TransitEncryptionMode == "TRANSIT_ENCRYPTION_MODE_SERVER_AUTHENTICATION"
}

// msRedisClusterCA names the certificate authority a cluster's endpoints
// present certificates from: the region's shared one, or the cluster's own.
func msRedisClusterCA(cluster MSRedisCluster) string {
	if cluster.ServerCaMode == "SERVER_CA_MODE_GOOGLE_MANAGED_SHARED_CA" {
		return msRedisSharedCAName(resourceProject(cluster.Name), msRedisLocation(cluster.Name))
	}
	return cluster.Name
}

func msRedisSharedCAName(project, location string) string {
	return fmt.Sprintf("projects/%s/locations/%s/sharedRegionalCertificateAuthority", project, location)
}

// msRedisLocation is the location of a projects/{p}/locations/{l}/… name.
func msRedisLocation(name string) string {
	parts := strings.Split(name, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "locations" {
			return parts[i+1]
		}
	}
	return ""
}

func msRedisInstanceSpec(inst MSRedisInstance) (msRedisPlaneSpec, error) {
	image, known := msRedisEngineImage(inst.RedisVersion)
	if !known {
		return msRedisPlaneSpec{}, fmt.Errorf("redisVersion %q is not a supported Redis version", inst.RedisVersion)
	}
	configs, err := msRedisEngineDirectives(inst.MemorySizeGb, inst.RedisConfigs)
	if err != nil {
		return msRedisPlaneSpec{}, err
	}
	spec := msRedisPlaneSpec{Image: image, Configs: configs, Persistence: msRedisPersistenceOfInstance(inst.PersistenceConfig)}
	if msRedisInstanceTLS(inst) {
		spec.CA = inst.Name
	}
	return spec, nil
}

func msRedisClusterSpec(cluster MSRedisCluster) (msRedisPlaneSpec, error) {
	configs, err := msRedisEngineDirectives(0, cluster.RedisConfigs)
	if err != nil {
		return msRedisPlaneSpec{}, err
	}
	spec := msRedisPlaneSpec{
		Cluster:     true,
		Image:       msRedisClusterEngineImage,
		Configs:     configs,
		Persistence: msRedisPersistenceOfCluster(cluster.PersistenceConfig),
		IAMAuth:     cluster.AuthorizationMode == "AUTH_MODE_IAM_AUTH",
		TokenAuth:   cluster.AuthorizationMode == "AUTH_MODE_TOKEN_AUTH",
		AclPolicy:   cluster.AclPolicy,
	}
	if msRedisClusterTLS(cluster) {
		spec.CA = msRedisClusterCA(cluster)
	}
	return spec, nil
}

// msRedisNewSecret is a credential the engine requires of clients the
// service authenticated itself.
func msRedisNewSecret() string {
	var value [24]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(fmt.Sprintf("read random bytes: %v", err))
	}
	return hex.EncodeToString(value[:])
}

// msRedisInstallInstance binds an instance's endpoints and records them on the
// instance. Without a container runtime, or without a loopback address at
// Redis's port, no engine serves the instance: it reports no host, and the
// operator reads why on stderr.
func msRedisInstallInstance(inst *MSRedisInstance, record *msRedisPlaneRecord, spec msRedisPlaneSpec) error {
	if !msRedisRunsEngines() {
		return nil
	}
	port := msRedisPort
	if spec.CA != "" {
		port = msRedisTLSPort
	}
	primary, primaryIP, err := msRedisListen(inst.Name, port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sim-memorystore] instance %s has no endpoint: %v\n", inst.Name, err)
		return nil
	}
	var read net.Listener
	var readIP string
	if inst.ReadReplicasMode == "READ_REPLICAS_ENABLED" {
		if read, readIP, err = msRedisListen(inst.Name+"#read", port); err != nil {
			_ = primary.Close()
			fmt.Fprintf(os.Stderr, "[sim-memorystore] instance %s has no read endpoint: %v\n", inst.Name, err)
			return nil
		}
	}
	record.Replicas = inst.ReplicaCount
	record.Primary = 0
	record.Nodes = nil
	for node := 0; node <= inst.ReplicaCount; node++ {
		record.Nodes = append(record.Nodes, node)
	}
	record.Endpoints = []msRedisEndpointRecord{{Role: msRedisPrimaryEndpoint, Address: primaryIP, Port: port}}
	if read != nil {
		record.Endpoints = append(record.Endpoints, msRedisEndpointRecord{Role: msRedisReadEndpoint, Address: readIP, Port: port})
	}
	plane, err := msRedisNewPlane(inst.Name, spec, *record)
	if err != nil {
		_ = primary.Close()
		if read != nil {
			_ = read.Close()
		}
		return err
	}
	plane.serve(string(msRedisPrimaryEndpoint), primary, plane.primaryTarget)
	inst.Host, inst.Port = primaryIP, port
	if read != nil {
		plane.serve(string(msRedisReadEndpoint), read, plane.readTarget)
		inst.ReadEndpoint, inst.ReadEndpointPort = readIP, port
	}
	return nil
}

// msRedisInstallCluster binds a cluster's discovery endpoint and one endpoint
// per node, which is the address that node announces to clients.
func msRedisInstallCluster(cluster *MSRedisCluster, record *msRedisPlaneRecord, spec msRedisPlaneSpec) error {
	if !msRedisRunsEngines() {
		return nil
	}
	nodes := cluster.ShardCount * (cluster.ReplicaCount + 1)
	listeners := map[string]net.Listener{}
	fail := func(err error) {
		for _, listener := range listeners {
			_ = listener.Close()
		}
		fmt.Fprintf(os.Stderr, "[sim-memorystore] cluster %s has no endpoint: %v\n", cluster.Name, err)
	}
	discovery, discoveryIP, err := msRedisListen(cluster.Name, msRedisPort)
	if err != nil {
		fail(err)
		return nil
	}
	listeners[string(msRedisDiscoveryEndpoint)] = discovery
	endpoints := []msRedisEndpointRecord{{Role: msRedisDiscoveryEndpoint, Address: discoveryIP, Port: msRedisPort}}
	record.Nodes = nil
	for node := 0; node < nodes; node++ {
		key := "node-" + strconv.Itoa(node)
		listener, ip, err := msRedisListen(cluster.Name+"#"+key, msRedisPort)
		if err != nil {
			fail(err)
			return nil
		}
		listeners[key] = listener
		endpoints = append(endpoints, msRedisEndpointRecord{Role: msRedisNodeEndpoint, Address: ip, Port: msRedisPort, Node: node})
		record.Nodes = append(record.Nodes, node)
	}
	record.Endpoints, record.Shards, record.Replicas = endpoints, cluster.ShardCount, cluster.ReplicaCount
	if spec.IAMAuth || spec.TokenAuth {
		record.Secret = msRedisNewSecret()
	}
	plane, err := msRedisNewPlane(cluster.Name, spec, *record)
	if err != nil {
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return err
	}
	plane.serveEndpoints(listeners)
	cluster.DiscoveryEndpoints = []MSRedisDiscoveryEndpoint{{Address: discoveryIP, Port: msRedisPort}}
	return nil
}

// serveEndpoints serves each endpoint the plane records from the listener
// bound for it.
func (p *msRedisPlane) serveEndpoints(listeners map[string]net.Listener) {
	p.mu.RLock()
	endpoints := append([]msRedisEndpointRecord(nil), p.endpoints...)
	p.mu.RUnlock()
	for _, endpoint := range endpoints {
		listener, ok := listeners[endpoint.key()]
		if !ok {
			continue
		}
		switch endpoint.Role {
		case msRedisPrimaryEndpoint:
			p.serve(endpoint.key(), listener, p.primaryTarget)
		case msRedisReadEndpoint:
			p.serve(endpoint.key(), listener, p.readTarget)
		case msRedisDiscoveryEndpoint:
			p.serve(endpoint.key(), listener, p.discoveryTarget)
		case msRedisNodeEndpoint:
			p.serve(endpoint.key(), listener, fixedTarget(endpoint.Node))
		}
	}
}

// discoveryTarget is the node a cluster's discovery endpoint reaches; every
// node answers a client's topology queries.
func (p *msRedisPlane) discoveryTarget() (int, bool) {
	order := p.nodeOrder()
	if len(order) == 0 {
		return 0, false
	}
	return order[int(p.readCursor.Add(1)-1)%len(order)], true
}

// msRedisStartEngine brings a new resource's engine up inside the create, so
// the operation that reports the resource ready reports an engine serving at
// its endpoints. A resource whose engine does not start is not created. A
// volume left under the resource's name by an earlier engine is not this
// resource's, so it goes first.
func msRedisStartEngine(name string) error {
	plane, ok := msRedisLoadPlane(name)
	if !ok {
		return nil
	}
	sim.RemoveVolumeSettled(plane.volume, "memorystore")
	if err := plane.Ensure(); err != nil {
		msRedisRemovePlane(name)
		return err
	}
	return nil
}

// msRedisSaveRecord records where a plane's topology now stands.
func msRedisSaveRecord(name string) {
	plane, ok := msRedisLoadPlane(name)
	if !ok {
		return
	}
	msRedisPlaneRecords.Update(name, func(record *msRedisPlaneRecord) {
		*record = plane.record(*record)
	})
}

// msRedisRecoverPlanes rebinds every recorded endpoint after a control-plane
// restart and re-adopts the engine containers an earlier process left.
func msRedisRecoverPlanes() error {
	for _, inst := range msRedisInstances.List() {
		record, ok := msRedisPlaneRecords.Get(inst.Name)
		if !ok || len(record.Endpoints) == 0 {
			continue
		}
		if len(record.Nodes) == 0 {
			for node := 0; node <= record.Replicas; node++ {
				record.Nodes = append(record.Nodes, node)
			}
		}
		spec, err := msRedisInstanceSpec(inst)
		if err != nil {
			return fmt.Errorf("recover %s: %w", inst.Name, err)
		}
		if err := msRedisRecoverEndpoints(inst.Name, spec, record); err != nil {
			return err
		}
	}
	for _, cluster := range msRedisClusters.List() {
		record, ok := msRedisPlaneRecords.Get(cluster.Name)
		if !ok || len(record.Endpoints) == 0 {
			continue
		}
		if len(record.Nodes) == 0 {
			for node := 0; node < record.Shards*(record.Replicas+1); node++ {
				record.Nodes = append(record.Nodes, node)
			}
		}
		spec, err := msRedisClusterSpec(cluster)
		if err != nil {
			return fmt.Errorf("recover %s: %w", cluster.Name, err)
		}
		if err := msRedisRecoverEndpoints(cluster.Name, spec, record); err != nil {
			return err
		}
	}
	return nil
}

func msRedisRecoverEndpoints(name string, spec msRedisPlaneSpec, record msRedisPlaneRecord) error {
	plane, err := msRedisNewPlane(name, spec, record)
	if err != nil {
		return fmt.Errorf("recover %s: %w", name, err)
	}
	if err := plane.Adopt(); err != nil {
		return fmt.Errorf("re-adopt the Redis engine of %s: %w", name, err)
	}
	listeners := map[string]net.Listener{}
	for _, endpoint := range record.Endpoints {
		listener, err := net.Listen("tcp", net.JoinHostPort(endpoint.Address, strconv.Itoa(endpoint.port())))
		if err != nil {
			return fmt.Errorf("rebind %s at %s: %w", name, endpoint.Address, err)
		}
		listeners[endpoint.key()] = listener
	}
	plane.serveEndpoints(listeners)
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
