package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cacheengine"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// The Memorystore for Redis data plane.
//
// An instance or cluster is a real Redis engine on sim/cacheengine: one
// workload per node, placed on the backend the redis service is configured
// onto, on an engine network every Memorystore engine of this simulator
// shares, behind listeners this process owns on loopback addresses of their
// own: an instance's host and readEndpoint, a cluster's discovery endpoint,
// and each cluster node at the address it announces as its hostname. A relay
// terminates TLS when the resource encrypts in transit, and on a cluster that
// authenticates with IAM it exchanges a verified access token for the
// engine's own credential.
//
// The create starts the engine and settles once every node answers, so a
// resource reported ready is serving; the delete stops it and removes its
// volume. Adding a node starts another container while the rest keep serving.

const (
	msRedisPort    = cacheengine.Port
	msRedisTLSPort = 6378
)

// msRedisEngineImage is the engine image for a Memorystore for Redis version,
// from mirror.gcr.io, which serves Docker Hub's official redis images without
// Docker Hub's anonymous pull limit.
func msRedisEngineImage(redisVersion string) (string, bool) {
	switch redisVersion {
	case "REDIS_3_2":
		return "mirror.gcr.io/library/redis:3.2-alpine", true
	case "REDIS_4_0":
		return "mirror.gcr.io/library/redis:4.0-alpine", true
	case "REDIS_5_0":
		return "mirror.gcr.io/library/redis:5.0-alpine", true
	case "REDIS_6_X":
		return "mirror.gcr.io/library/redis:6.2-alpine", true
	case "REDIS_7_0":
		return "mirror.gcr.io/library/redis:7.0-alpine", true
	case "REDIS_7_2":
		return "mirror.gcr.io/library/redis:7.2-alpine", true
	}
	return "", false
}

// msRedisEnginePlatform is the platform the engine runs on whatever the host
// is, as the Cloud SQL data plane's is.
const msRedisEnginePlatform = "linux/amd64"

// Memorystore for Redis Cluster runs Redis 7.2, the engineVersion its backups
// report.
const (
	msRedisClusterEngineImage   = "mirror.gcr.io/library/redis:7.2-alpine"
	msRedisClusterEngineVersion = "redis-7.2"
)

// msRedisPlaneRecord is what a control-plane restart needs to serve a
// resource's endpoints again: the engine's topology, the AUTH string an
// instance requires, and the credential a cluster's engine requires of the
// clients the service authenticated.
type msRedisPlaneRecord struct {
	cacheengine.Record
	AuthString string `json:"authString,omitempty"`
	Secret     string `json:"secret,omitempty"`
}

var msRedisPlaneRecords sim.Store[msRedisPlaneRecord]

// msRedisPlaneSpec is how a resource's engine runs, which the resource's
// configuration decides.
type msRedisPlaneSpec struct {
	Cluster     bool
	Image       string
	Configs     [][2]string
	Persistence msRedisPersistence
	// CA names the certificate authority whose certificates the endpoints
	// present; empty serves plaintext.
	CA string
	// IAMAuth exchanges a verified access token for the engine's credential.
	IAMAuth bool
	// TokenAuth runs the cluster's token-auth users as engine users.
	TokenAuth bool
	// AclPolicy names the ACL policy whose rules define the cluster's other
	// engine users.
	AclPolicy string
}

// msRedisPlane is a resource's engine and what Memorystore keeps beside it:
// the persistence schedule, and the users the engine runs.
type msRedisPlane struct {
	*cacheengine.Plane
	name    string
	cluster bool

	mu          sync.RWMutex
	persistence msRedisPersistence
	snapshots   *time.Timer
	closed      bool
	iamAuth     bool
	tokenAuth   bool
	aclPolicy   string
	// policyUsers are the engine users the applied ACL policy defines, keyed
	// by lowercased username, which an IAM principal of that email becomes.
	policyUsers map[string]string

	// opMu serialises the operations that move a snapshot in or out or change
	// the topology.
	opMu sync.Mutex
}

var msRedisPlanes sync.Map // resource name -> *msRedisPlane

func msRedisLoadPlane(name string) (*msRedisPlane, bool) {
	value, ok := msRedisPlanes.Load(name)
	if !ok {
		return nil, false
	}
	plane, ok := value.(*msRedisPlane)
	return plane, ok
}

// msRedisScopedName names an engine object of a resource after the resource
// and the simulator that owns it, so two simulators serving a resource of the
// same name never share a volume or a network alias.
func msRedisScopedName(name string) string {
	digest := sha256.Sum256([]byte(sim.WorkloadScope() + "\x00" + name))
	return "sockerless-memorystore-" + hex.EncodeToString(digest[:8])
}

// msRedisNetworkName is the network every Memorystore engine of this
// simulator joins.
func msRedisNetworkName() string {
	digest := sha256.Sum256([]byte(sim.WorkloadScope()))
	return "sockerless-memorystore-" + hex.EncodeToString(digest[:6])
}

// msRedisEngineNetwork ensures the engines' network and its sidecar, which
// resolves the nodes' aliases on every placement backend.
func msRedisEngineNetwork(ctx context.Context) (string, []string, error) {
	network := msRedisNetworkName()
	if _, _, err := sim.EnsurePlatformNetwork(ctx, network, nil); err != nil {
		return "", nil, err
	}
	sidecar, err := gcpEnsureNetworkSidecar(ctx, network, netip.Addr{})
	if err != nil {
		return "", nil, err
	}
	return network, []string{sidecar.String()}, nil
}

// msRedisEngineDirectives translates Memorystore's redisConfigs into
// redis-server directives. maxmemory-gb is Memorystore's own knob for
// maxmemory, in GiB; every other supported name is a Redis directive already.
func msRedisEngineDirectives(memorySizeGb int, redisConfigs map[string]string) ([][2]string, error) {
	var directives [][2]string
	maxmemory := int64(memorySizeGb) << 30
	names := make([]string, 0, len(redisConfigs))
	for name := range redisConfigs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := redisConfigs[name]
		if name == "maxmemory-gb" {
			gb, err := strconv.ParseFloat(value, 64)
			if err != nil || gb <= 0 {
				return nil, fmt.Errorf("redisConfigs maxmemory-gb %q is not a positive number", value)
			}
			maxmemory = int64(gb * float64(1<<30))
			continue
		}
		directives = append(directives, [2]string{name, value})
	}
	if maxmemory > 0 {
		directives = append([][2]string{{"maxmemory", strconv.FormatInt(maxmemory, 10)}}, directives...)
	}
	return directives, nil
}

// msRedisNewPlane builds the plane serving a resource from its record.
func msRedisNewPlane(name string, spec msRedisPlaneSpec, record msRedisPlaneRecord) (*msRedisPlane, error) {
	plane := &msRedisPlane{
		name:        name,
		cluster:     spec.Cluster,
		persistence: spec.Persistence,
		iamAuth:     spec.IAMAuth,
		tokenAuth:   spec.TokenAuth,
		aclPolicy:   spec.AclPolicy,
	}
	password := record.AuthString
	if spec.Cluster {
		password = record.Secret
	}
	var tlsConfig *tls.Config
	if spec.CA != "" {
		config, err := msRedisServerTLSConfig(spec.CA)
		if err != nil {
			return nil, err
		}
		tlsConfig = config
	}
	cfg := cacheengine.Config{
		Name:        "Memorystore " + name,
		LogTag:      "memorystore",
		Owner:       name,
		Cluster:     spec.Cluster,
		Image:       spec.Image,
		Platform:    msRedisEnginePlatform,
		Volume:      msRedisScopedName(name),
		Labels:      map[string]string{"sockerless-memorystore": name},
		NodeLabel:   "sockerless-memorystore-node",
		Backend:     func() (placement.Backend, error) { return gcpPlacement.For(placementRedis) },
		Network:     msRedisEngineNetwork,
		Sandbox:     SandboxCloudRun,
		Password:    password,
		Configs:     spec.Configs,
		Persistence: spec.Persistence.engine(),
		TLS:         tlsConfig,
		Started:     plane.scheduleSnapshots,
	}
	if spec.IAMAuth {
		cfg.ExchangeCredential = plane.connectAs
	}
	if spec.Cluster {
		cfg.Users = plane.engineUsers
		cfg.UsersApplied = plane.usersApplied
	}
	plane.Plane = cacheengine.New(cfg, record.Record)
	msRedisPlanes.Store(name, plane)
	plane.scheduleSnapshots()
	return plane, nil
}

// record is the plane's part of the resource's record as it now stands.
func (p *msRedisPlane) record(base msRedisPlaneRecord) msRedisPlaneRecord {
	base.Record = p.Record()
	return base
}

// stopSnapshots stops the snapshot schedule for good.
func (p *msRedisPlane) stopSnapshots() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snapshots != nil {
		p.snapshots.Stop()
	}
	p.closed = true
}

// msRedisRemovePlane closes a deleted resource's endpoints, stops its engine
// and removes its volume.
func msRedisRemovePlane(name string) {
	msRedisPlaneRecords.Delete(name)
	value, ok := msRedisPlanes.LoadAndDelete(name)
	if !ok {
		return
	}
	plane, ok := value.(*msRedisPlane)
	if !ok {
		return
	}
	plane.stopSnapshots()
	plane.Remove()
}
