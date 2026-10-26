package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cacheengine"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// The Azure Cache for Redis data plane.
//
// A cache is a real Redis engine on sim/cacheengine: a primary, and on the
// Standard and Premium tiers its replicas, each a workload on the backend the
// redis service is configured onto, on the platform network. The simulator
// owns a loopback listener at the cache's sslPort, which terminates TLS, and,
// while enableNonSslPort holds, one at its non-SSL port on the same address;
// it registers the cache's hostName against that address in its DNS front,
// the coordinate a client resolves on Azure. Both access keys authenticate as
// the engine's default user. The cache reports provisioningState Creating
// until the engine answers.

const (
	redisSSLPort    = 6380
	redisNonSSLPort = cacheengine.Port
)

var placementRedisCache = placement.Service{Name: "redis"}

// redisEngineVersions maps the redisVersion a cache asks for to the version it
// reports and the image it runs. 'latest' is 6.0, the latest version the
// specification lists.
func redisEngineVersion(requested string) (version, image string, ok bool) {
	switch strings.ToLower(requested) {
	case "", "latest", "6", "6.0":
		return "6.0", "mirror.gcr.io/library/redis:6.0-alpine", true
	case "4", "4.0":
		return "4.0", "mirror.gcr.io/library/redis:4.0-alpine", true
	}
	return "", "", false
}

// redisEngineRecord is what a control-plane restart needs to serve a cache's
// engine again.
type redisEngineRecord struct {
	cacheengine.Record
	// Owner is the resource ID the cache was created under, which names its
	// engine's workloads, volume and loopback address whatever group a move
	// takes the cache to.
	Owner    string `json:"owner"`
	Image    string `json:"image"`
	HostName string `json:"hostName"`
	MinTLS   string `json:"minimumTlsVersion,omitempty"`
}

var redisEngineRecords sim.Store[redisEngineRecord]

var redisEngines sync.Map // cache ID -> *cacheengine.Plane

// redisEngineIDs maps an engine's owner to the cache's resource ID now.
var redisEngineIDs sync.Map

func redisCurrentID(owner string) string {
	if id, ok := redisEngineIDs.Load(owner); ok {
		if current, ok := id.(string); ok {
			return current
		}
	}
	return owner
}

func redisLoadEngine(id string) (*cacheengine.Plane, bool) {
	value, ok := redisEngines.Load(id)
	if !ok {
		return nil, false
	}
	plane, ok := value.(*cacheengine.Plane)
	return plane, ok
}

func redisRunsEngines() bool {
	return sim.RequireContainerRuntime("the Azure Cache for Redis data plane") == nil
}

func redisScopedName(id string) string {
	digest := sha256.Sum256([]byte(sim.WorkloadScope() + "\x00" + strings.ToLower(id)))
	return "sockerless-azure-redis-" + hex.EncodeToString(digest[:8])
}

// redisAuthority signs the certificate every cache's sslPort presents. Azure
// reports no certificate authority for a cache: a real endpoint presents a
// certificate a public root signs.
var redisAuthority = sync.OnceValues(func() (cacheengine.Authority, error) {
	return cacheengine.NewAuthority(pkix.Name{
		Organization: []string{"Azure Cache for Redis simulator"},
		CommonName:   "Azure Cache for Redis simulator CA",
	}, 10*365*24*time.Hour)
})

// redisServerTLS is the TLS a cache serves, at the minimum version its
// minimumTlsVersion names.
func redisServerTLS(minimum string) (*tls.Config, error) {
	authority, err := redisAuthority()
	if err != nil {
		return nil, err
	}
	config, err := cacheengine.ServerTLS(authority.CertPEM, authority.KeyPEM)
	if err != nil {
		return nil, err
	}
	switch minimum {
	case "1.0":
		config.MinVersion = tls.VersionTLS10
	case "1.1":
		config.MinVersion = tls.VersionTLS11
	}
	return config, nil
}

func redisEngineNetwork(ctx context.Context) (string, []string, error) {
	network, err := azurePlatformNetwork(ctx)
	if err != nil {
		return "", nil, err
	}
	return network.Network, network.resolvers(), nil
}

// redisNewEngine builds the engine a cache's record describes. Both access
// keys authenticate as the default user, and the primary key is the one the
// replicas replicate with.
func redisNewEngine(id string, record redisEngineRecord) (*cacheengine.Plane, error) {
	config, err := redisServerTLS(record.MinTLS)
	if err != nil {
		return nil, err
	}
	owner := record.Owner
	redisEngineIDs.Store(owner, id)
	scoped := redisScopedName(owner)
	return cacheengine.New(cacheengine.Config{
		Name:      "Azure Cache for Redis " + owner,
		LogTag:    "azure-redis",
		Owner:     owner,
		Image:     record.Image,
		Platform:  "linux/amd64",
		Volume:    scoped,
		Labels:    map[string]string{"sockerless-azure-redis": scoped},
		NodeLabel: "sockerless-azure-redis-node",
		Backend:   func() (placement.Backend, error) { return azurePlacementBackend(placementRedisCache) },
		Network:   redisEngineNetwork,
		Sandbox:   SandboxACA,
		Password:  azureKeyMaterial32(id, "primary"),
		TLS:       config,
		Users: func() ([]cacheengine.User, error) {
			id := redisCurrentID(owner)
			current, _ := redisEngineRecords.Get(id)
			return redisAccessKeyUsers(id, current.Image)
		},
	}, record.Record), nil
}

// redisAccessKeyUsers is the default user both access keys authenticate as.
// Redis 4.0 predates ACL users and takes the primary key alone.
func redisAccessKeyUsers(id, image string) ([]cacheengine.User, error) {
	if strings.HasSuffix(image, ":4.0-alpine") {
		return nil, nil
	}
	return []cacheengine.User{{
		Name:      "default",
		Passwords: []string{azureKeyMaterial32(id, "primary"), azureKeyMaterial32(id, "secondary")},
		// Redis 6.0 predates channel permissions, and grants every channel.
		Rule: []string{"~*", "+@all"},
	}}, nil
}

// redisReplicaCount is how many replicas a cache's SKU runs behind its
// primary: none on Basic, one on Standard, and replicasPerPrimary (or its
// older name replicasPerMaster, one by default) on Premium.
func redisReplicaCount(properties map[string]any) int {
	sku, _ := properties["sku"].(map[string]any)
	name, _ := sku["name"].(string)
	switch name {
	case "Standard":
		return 1
	case "Premium":
		for _, key := range []string{"replicasPerPrimary", "replicasPerMaster"} {
			if count, ok := properties[key].(float64); ok && count > 0 {
				return int(count)
			}
		}
		return 1
	}
	return 0
}

func redisNonSSLEnabled(properties map[string]any) bool {
	enabled, _ := properties["enableNonSslPort"].(bool)
	return enabled
}

// redisEngineDirectives are the redisConfiguration settings that are Redis
// directives of the same name; the others, such as maxmemory-reserved, are
// the service's own.
func redisEngineDirectives(properties map[string]any) [][2]string {
	configuration, _ := properties["redisConfiguration"].(map[string]any)
	var directives [][2]string
	for _, name := range []string{"maxmemory-policy", "notify-keyspace-events"} {
		if value, ok := configuration[name].(string); ok && value != "" {
			directives = append(directives, [2]string{name, value})
		}
	}
	return directives
}

// redisInstallEngine binds a new cache's endpoints, registers its hostName
// and records its engine. It returns nil when this simulator runs no engines.
func redisInstallEngine(cache RedisCache, image string) (*cacheengine.Plane, error) {
	if !redisRunsEngines() {
		return nil, nil
	}
	hostName, _ := cache.Properties["hostName"].(string)
	minimum, _ := cache.Properties["minimumTlsVersion"].(string)
	replicas := redisReplicaCount(cache.Properties)
	record := redisEngineRecord{Owner: cache.ID, Image: image, HostName: hostName, MinTLS: minimum}
	record.Replicas = replicas
	for node := 0; node <= replicas; node++ {
		record.Nodes = append(record.Nodes, node)
	}
	wanted := []cacheengine.Endpoint{{Role: cacheengine.PrimaryEndpoint, Port: redisSSLPort}}
	if redisNonSSLEnabled(cache.Properties) {
		wanted = append(wanted, cacheengine.Endpoint{Role: cacheengine.PrimaryEndpoint, Port: redisNonSSLPort, Plaintext: true})
	}
	listeners, endpoints, err := cacheengine.Bind(cache.ID, wanted)
	if err != nil {
		return nil, fmt.Errorf("bind the cache's ports: %w", err)
	}
	record.Endpoints = endpoints
	plane, err := redisNewEngine(cache.ID, record)
	if err != nil {
		cacheengine.CloseListeners(listeners)
		return nil, err
	}
	if err := plane.Configure(redisEngineDirectives(cache.Properties)); err != nil {
		cacheengine.CloseListeners(listeners)
		return nil, err
	}
	plane.ServeEndpoints(listeners)
	redisEngines.Store(cache.ID, plane)
	redisEngineRecords.Put(cache.ID, record)
	if hostName != "" {
		RegisterAzureDNSName(hostName, endpoints[0].Address)
	}
	return plane, nil
}

// redisStartEngine brings a cache's engine up. A volume an earlier engine left
// under the cache's name is not this cache's, so it goes first.
func redisStartEngine(plane *cacheengine.Plane) error {
	sim.RemoveVolumeSettled(plane.Volume(), "azure-redis")
	return plane.Ensure()
}

// redisSaveEngine records where a cache's engine topology now stands.
func redisSaveEngine(id string, plane *cacheengine.Plane) {
	redisEngineRecords.Update(id, func(record *redisEngineRecord) { record.Record = plane.Record() })
}

// redisRemoveEngine stops a deleted cache's engine, closes its ports, removes
// its volume and its hostName.
func redisRemoveEngine(id string) {
	record, recorded := redisEngineRecords.Get(id)
	redisEngineRecords.Delete(id)
	if recorded && record.HostName != "" {
		UnregisterAzureDNSName(record.HostName)
	}
	if recorded {
		redisEngineIDs.Delete(record.Owner)
	}
	value, ok := redisEngines.LoadAndDelete(id)
	if !ok {
		return
	}
	if plane, ok := value.(*cacheengine.Plane); ok {
		plane.Remove()
	}
}

// redisMoveEngine follows a cache to the resource ID a move gives it; the
// engine keeps serving at the address its hostName resolves to.
func redisMoveEngine(oldID, newID string) {
	record, ok := redisEngineRecords.Get(oldID)
	if !ok {
		return
	}
	redisEngineRecords.Delete(oldID)
	redisEngineRecords.Put(newID, record)
	redisEngineIDs.Store(record.Owner, newID)
	if plane, running := redisEngines.LoadAndDelete(oldID); running {
		redisEngines.Store(newID, plane)
	}
}

// redisRecoverEngines rebinds every cache's ports after a control-plane
// restart, registers its hostName again and re-adopts the engine workloads
// an earlier process left.
func redisRecoverEngines() error {
	for _, cache := range redisCaches.List() {
		record, ok := redisEngineRecords.Get(cache.ID)
		if !ok {
			continue
		}
		plane, err := redisNewEngine(cache.ID, record)
		if err != nil {
			return fmt.Errorf("recover %s: %w", cache.ID, err)
		}
		if err := plane.Configure(redisEngineDirectives(cache.Properties)); err != nil {
			return fmt.Errorf("recover %s: %w", cache.ID, err)
		}
		if err := plane.Adopt(); err != nil {
			return fmt.Errorf("re-adopt the engine of %s: %w", cache.ID, err)
		}
		listeners, err := cacheengine.Rebind(record.Owner, record.Endpoints)
		if err != nil {
			return err
		}
		plane.ServeEndpoints(listeners)
		redisEngines.Store(cache.ID, plane)
		if record.HostName != "" && len(record.Endpoints) > 0 {
			RegisterAzureDNSName(record.HostName, record.Endpoints[0].Address)
		}
	}
	return nil
}

// redisSetNonSSLPort starts or stops serving a cache's non-SSL port on the
// address its sslPort answers at.
func redisSetNonSSLPort(id string, plane *cacheengine.Plane, enabled bool) error {
	serving := false
	for _, endpoint := range plane.Endpoints() {
		if endpoint.Plaintext {
			serving = true
		}
	}
	if serving == enabled {
		return nil
	}
	if !enabled {
		plane.CloseEndpoint(cacheengine.Endpoint{Role: cacheengine.PrimaryEndpoint, Plaintext: true}.Key())
		redisSaveEngine(id, plane)
		return nil
	}
	record, _ := redisEngineRecords.Get(id)
	listeners, endpoints, err := cacheengine.Bind(record.Owner, []cacheengine.Endpoint{{Role: cacheengine.PrimaryEndpoint, Port: redisNonSSLPort, Plaintext: true}})
	if err != nil {
		return err
	}
	plane.AddEndpoints(endpoints...)
	plane.ServeEndpoints(listeners)
	redisSaveEngine(id, plane)
	return nil
}

func redisLogEngineFailure(id string, err error) {
	log.Printf("Azure Cache for Redis %s: %v", id, err)
}
