package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cacheengine"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// The Amazon ElastiCache data plane.
//
// A cache cluster, replication group or serverless cache is a real engine on
// sim/cacheengine: Valkey or Redis OSS nodes, or Memcached nodes, each a
// workload on the backend the elasticache service is configured onto, on the
// platform network, behind listeners this process owns on loopback addresses
// of their own. The address and port a resource reports are the listener's,
// as an Amazon RDS endpoint's are. The engine starts in the background while
// the resource reports creating, and the resource turns available once every
// node answers.

const (
	ecRedisPort     = cacheengine.Port
	ecMemcachedPort = cacheengine.MemcachedPort
	// A serverless cache serves reads from replicas on the port after its
	// endpoint's.
	ecServerlessReaderOffset = 1
	ecEnginePlatform         = "linux/amd64"
	ecAWSOwnedKMSKeyID       = "aws-owned-elasticache"
)

var ecPlacementService = placement.Service{Name: "elasticache"}

// ecEngineVersion is one engine version the simulator runs, and the image it
// runs it from.
type ecEngineVersion struct {
	Engine, Version, Family, Description, Image string
}

// ecEngineVersions are the engine versions DescribeCacheEngineVersions lists
// and a create can name. Redis OSS 7.1 is ElastiCache's own release on the
// Redis 7.0 line, so it runs Redis 7.0.
var ecEngineVersions = []ecEngineVersion{
	{"redis", "7.1", "redis7", "Redis", "mirror.gcr.io/library/redis:7.0-alpine"},
	{"redis", "7.0", "redis7", "Redis", "mirror.gcr.io/library/redis:7.0-alpine"},
	{"redis", "6.2", "redis6.x", "Redis", "mirror.gcr.io/library/redis:6.2-alpine"},
	{"valkey", "8.0", "valkey8", "Valkey", "mirror.gcr.io/valkey/valkey:8.0-alpine"},
	{"memcached", "1.6.22", "memcached1.6", "memcached", "mirror.gcr.io/library/memcached:1.6.22-alpine"},
}

// ecResolveEngineVersion is the version of engine a create runs: the default
// when none is named, and otherwise the listed version the named one is, or
// whose major.minor it shares.
func ecResolveEngineVersion(engine, version string) (ecEngineVersion, bool) {
	if version == "" {
		version = ecDefaultEngineVersion(engine)
	}
	for _, candidate := range ecEngineVersions {
		if candidate.Engine == engine && candidate.Version == version {
			return candidate, true
		}
	}
	parts := strings.SplitN(version, ".", 3)
	if len(parts) >= 2 {
		minor := parts[0] + "." + parts[1]
		for _, candidate := range ecEngineVersions {
			if candidate.Engine == engine && candidate.Version == minor {
				return candidate, true
			}
		}
	}
	return ecEngineVersion{}, false
}

// ecServerlessEngineVersion is the version a serverless cache of a major
// engine version runs.
func ecServerlessEngineVersion(engine, major string) (ecEngineVersion, bool) {
	if major == "" {
		return ecResolveEngineVersion(engine, "")
	}
	for _, candidate := range ecEngineVersions {
		if candidate.Engine == engine && strings.HasPrefix(candidate.Version, major+".") {
			return candidate, true
		}
	}
	return ecEngineVersion{}, false
}

const (
	ecKindCluster          = "cluster"
	ecKindReplicationGroup = "replicationgroup"
	ecKindServerless       = "serverlesscache"
)

// ecEngineRecord is what a control-plane restart needs to serve a resource's
// engine again.
type ecEngineRecord struct {
	cacheengine.Record
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Engine  string `json:"engine"`
	Image   string `json:"image"`
	Cluster bool   `json:"cluster,omitempty"`
	TLS     bool   `json:"tls,omitempty"`
	// TLSPreferred accepts unencrypted clients beside encrypted ones.
	TLSPreferred bool `json:"tlsPreferred,omitempty"`
	// Token is the AUTH token the engine requires, sealed under the AWS
	// owned ElastiCache key.
	Token []byte `json:"token,omitempty"`
}

var ecEngineRecords sim.Store[ecEngineRecord]

// ecEngine is a resource's running data plane: Redis-protocol nodes or
// Memcached nodes.
type ecEngine struct {
	key       string
	redis     *cacheengine.Plane
	memcached *cacheengine.Memcached
	// opMu serialises the operations that change the engine's topology.
	opMu sync.Mutex
}

var ecEngines sync.Map // engine key -> *ecEngine

func ecEngineKey(kind, id string) string { return kind + ":" + id }

func ecLoadEngine(key string) (*ecEngine, bool) {
	value, ok := ecEngines.Load(key)
	if !ok {
		return nil, false
	}
	engine, ok := value.(*ecEngine)
	return engine, ok
}

func ecRunsEngines() bool {
	return sim.RequireContainerRuntime("the Amazon ElastiCache data plane") == nil
}

// ecScopedName names a resource's engine objects after it and the simulator
// that owns it.
func ecScopedName(key string) string {
	digest := sha256.Sum256([]byte(sim.WorkloadScope() + "\x00" + key))
	return "sockerless-elasticache-" + hex.EncodeToString(digest[:8])
}

// ecServerTLS is the TLS configuration every encrypting endpoint serves, from
// a certificate authority this process creates on first use. ElastiCache
// reports no CA of its own: a real endpoint presents a certificate a public
// root signs.
var ecServerTLS = sync.OnceValues(func() (*tls.Config, error) {
	authority, err := cacheengine.NewAuthority(pkix.Name{
		Organization: []string{"Amazon ElastiCache simulator"},
		CommonName:   "Amazon ElastiCache simulator CA",
	}, 10*365*24*time.Hour)
	if err != nil {
		return nil, err
	}
	return cacheengine.ServerTLS(authority.CertPEM, authority.KeyPEM)
})

func ecSealToken(token string) ([]byte, error) {
	if token == "" {
		return nil, nil
	}
	if err := kmsEnsureKeyMaterial(ecAWSOwnedKMSKeyID); err != nil {
		return nil, fmt.Errorf("generate the AWS owned ElastiCache key: %w", err)
	}
	sealed, ok := kmsEncryptBytes(ecAWSOwnedKMSKeyID, []byte(token))
	if !ok {
		return nil, fmt.Errorf("seal the AUTH token")
	}
	return sealed, nil
}

func ecOpenToken(sealed []byte) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	_, token, ok := kmsDecryptBytes(sealed)
	if !ok {
		return "", fmt.Errorf("open the sealed AUTH token")
	}
	return string(token), nil
}

func ecEngineNetwork(ctx context.Context) (string, []string, error) {
	network, err := awsPlatformNetwork(ctx)
	if err != nil {
		return "", nil, err
	}
	return network.Name, network.DNS(), nil
}

func ecEngineBackend() (placement.Backend, error) { return awsPlacement.For(ecPlacementService) }

// ecNewEngine builds the data plane a record describes.
func ecNewEngine(key string, record ecEngineRecord) (*ecEngine, error) {
	scoped := ecScopedName(key)
	labels := map[string]string{"sockerless-elasticache": scoped}
	var tlsConfig *tls.Config
	if record.TLS {
		config, err := ecServerTLS()
		if err != nil {
			return nil, err
		}
		tlsConfig = config
	}
	engine := &ecEngine{key: key}
	name := "Amazon ElastiCache " + strings.Replace(key, ":", " ", 1)
	if record.Engine == "memcached" {
		engine.memcached = cacheengine.NewMemcached(cacheengine.MemcachedConfig{
			Name:      name,
			Owner:     key,
			Image:     record.Image,
			Platform:  ecEnginePlatform,
			Labels:    labels,
			NodeLabel: "sockerless-elasticache-node",
			Backend:   ecEngineBackend,
			Network:   ecEngineNetwork,
			Sandbox:   SandboxFargate,
			TLS:       tlsConfig,
		}, record.Record)
		return engine, nil
	}
	token, err := ecOpenToken(record.Token)
	if err != nil {
		return nil, err
	}
	engine.redis = cacheengine.New(cacheengine.Config{
		Name:                           name,
		LogTag:                         "elasticache",
		Owner:                          key,
		Cluster:                        record.Cluster,
		Image:                          record.Image,
		Platform:                       ecEnginePlatform,
		Volume:                         scoped,
		Labels:                         labels,
		NodeLabel:                      "sockerless-elasticache-node",
		Backend:                        ecEngineBackend,
		Network:                        ecEngineNetwork,
		Sandbox:                        SandboxFargate,
		Password:                       token,
		TLS:                            tlsConfig,
		TLSPreferred:                   record.TLSPreferred,
		ReadFromPrimaryWithoutReplicas: true,
	}, record.Record)
	return engine, nil
}

// ecInstallEngine binds a new resource's endpoints and records its engine.
// It returns the endpoints as bound, or none when this simulator runs no
// engines.
func ecInstallEngine(record ecEngineRecord, wanted []cacheengine.Endpoint) (*ecEngine, []cacheengine.Endpoint, error) {
	if !ecRunsEngines() {
		return nil, nil, nil
	}
	key := ecEngineKey(record.Kind, record.ID)
	listeners, endpoints, err := cacheengine.Bind(key, wanted)
	if err != nil {
		return nil, nil, fmt.Errorf("bind the endpoints: %w", err)
	}
	record.Endpoints = endpoints
	engine, err := ecNewEngine(key, record)
	if err != nil {
		cacheengine.CloseListeners(listeners)
		return nil, nil, err
	}
	engine.serve(listeners)
	ecEngines.Store(key, engine)
	ecEngineRecords.Put(key, record)
	return engine, endpoints, nil
}

func (e *ecEngine) serve(listeners map[string]net.Listener) {
	if e.memcached != nil {
		e.memcached.ServeEndpoints(listeners)
		return
	}
	e.redis.ServeEndpoints(listeners)
}

// start brings the engine up. A volume an earlier engine left under the
// resource's name is not this resource's, so it goes first.
func (e *ecEngine) start() error {
	if e.memcached != nil {
		return e.memcached.Ensure()
	}
	sim.RemoveVolumeSettled(e.redis.Volume(), "elasticache")
	return e.redis.Ensure()
}

func (e *ecEngine) endpoints() []cacheengine.Endpoint {
	if e.memcached != nil {
		return e.memcached.Endpoints()
	}
	return e.redis.Endpoints()
}

func (e *ecEngine) adopt() error {
	if e.memcached != nil {
		return e.memcached.Adopt()
	}
	return e.redis.Adopt()
}

// save records where the engine's topology now stands.
func (e *ecEngine) save() {
	ecEngineRecords.Update(e.key, func(record *ecEngineRecord) {
		if e.memcached != nil {
			record.Record = e.memcached.Record()
			return
		}
		record.Record = e.redis.Record()
	})
}

// ecRemoveEngine stops a deleted resource's engine, closes its endpoints and
// removes its volume.
func ecRemoveEngine(key string) {
	ecEngineRecords.Delete(key)
	value, ok := ecEngines.LoadAndDelete(key)
	if !ok {
		return
	}
	engine, ok := value.(*ecEngine)
	if !ok {
		return
	}
	if engine.memcached != nil {
		engine.memcached.Remove()
		return
	}
	engine.redis.Remove()
}

// ecRecoverEngines rebinds every recorded endpoint after a control-plane
// restart and re-adopts the engine workloads an earlier process left.
func ecRecoverEngines() error {
	for _, record := range ecEngineRecords.List() {
		key := ecEngineKey(record.Kind, record.ID)
		engine, err := ecNewEngine(key, record)
		if err != nil {
			return fmt.Errorf("recover %s: %w", key, err)
		}
		if err := engine.adopt(); err != nil {
			return fmt.Errorf("re-adopt the engine of %s: %w", key, err)
		}
		listeners, err := cacheengine.Rebind(key, record.Endpoints)
		if err != nil {
			return err
		}
		engine.serve(listeners)
		ecEngines.Store(key, engine)
	}
	return nil
}

// ecEndpointOf is the address and port of the endpoint of role, and of node
// for a node endpoint.
func ecEndpointOf(endpoints []cacheengine.Endpoint, role cacheengine.Role, node int) (string, int) {
	for _, endpoint := range endpoints {
		if endpoint.Role == role && (role != cacheengine.NodeEndpoint || endpoint.Node == node) {
			return endpoint.Address, endpoint.PortOrDefault()
		}
	}
	return "", 0
}

// ecLogEngineFailure says why a resource's engine did not come up.
func ecLogEngineFailure(key string, err error) {
	log.Printf("Amazon ElastiCache %s: the engine did not start: %v", key, err)
}
