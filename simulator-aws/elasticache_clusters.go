package main

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/cacheengine"
)

// Amazon ElastiCache cache clusters and replication groups. A create answers
// with the resource creating and starts its engine; the resource turns
// available once every node answers, or create-failed when the engine does
// not start. A delete answers deleting, and the resource is gone once its
// engine has stopped.

type ECCluster struct {
	CacheClusterId         string
	CacheNodeType          string
	Engine                 string
	EngineVersion          string
	CacheClusterStatus     string
	NumCacheNodes          int
	CacheClusterCreateTime string
	ARN                    string
	// Endpoint is a Memcached cluster's configuration endpoint, which
	// reaches any of its nodes.
	Endpoint string
	// Port is the port every node accepts connections on.
	Port                      int
	Tags                      map[string]string
	CacheNodes                []ECCacheNode `json:",omitempty"`
	ReplicationGroupId        string        `json:",omitempty"`
	CacheSubnetGroupName      string        `json:",omitempty"`
	AuthTokenEnabled          bool          `json:",omitempty"`
	TransitEncryptionEnabled  bool          `json:",omitempty"`
	TransitEncryptionMode     string        `json:",omitempty"`
	PreferredAvailabilityZone string        `json:",omitempty"`
	// Incarnation tells a background transition apart from one on a cluster
	// recreated under the same identifier.
	Incarnation string `json:",omitempty"`
}

// ECCacheNode is one node of a cache cluster.
type ECCacheNode struct {
	CacheNodeId         string
	CacheNodeStatus     string
	CacheNodeCreateTime string
	Address             string `json:",omitempty"`
	Port                int    `json:",omitempty"`
}

// ecClusterZone is the Availability Zone a cluster's nodes are in: the one
// it asked for, or the Region's first.
func ecClusterZone(c ECCluster) string {
	if c.PreferredAvailabilityZone != "" {
		return c.PreferredAvailabilityZone
	}
	return awsAvailabilityZone()
}

// ECReplicationGroup models the ReplicationGroup shape.
type ECReplicationGroup struct {
	ReplicationGroupId       string
	Description              string
	Status                   string
	CacheNodeType            string
	Engine                   string
	EngineVersion            string `json:",omitempty"`
	AutomaticFailover        string
	MultiAZ                  string
	ClusterEnabled           bool
	MemberClusters           []string
	SnapshotRetentionLimit   int
	SnapshotWindow           string
	ConfigEndpointAddress    string
	ConfigEndpointPort       int
	ARN                      string
	CreateTime               string
	Tags                     map[string]string
	Port                     int           `json:",omitempty"`
	NodeGroups               []ECNodeGroup `json:",omitempty"`
	CacheSubnetGroupName     string        `json:",omitempty"`
	AuthTokenEnabled         bool          `json:",omitempty"`
	TransitEncryptionEnabled bool          `json:",omitempty"`
	TransitEncryptionMode    string        `json:",omitempty"`
	// MemberNodes names the member cluster each engine node is.
	MemberNodes map[int]string `json:",omitempty"`
	Incarnation string         `json:",omitempty"`
}

// ECNodeGroup is one node group (shard) of a replication group.
type ECNodeGroup struct {
	NodeGroupId    string
	Status         string
	Slots          string `json:",omitempty"`
	PrimaryAddress string `json:",omitempty"`
	PrimaryPort    int    `json:",omitempty"`
	ReaderAddress  string `json:",omitempty"`
	ReaderPort     int    `json:",omitempty"`
	Members        []ECNodeGroupMember
}

// ECNodeGroupMember is one node of a node group.
type ECNodeGroupMember struct {
	CacheClusterId string
	CacheNodeId    string
	Address        string `json:",omitempty"`
	Port           int    `json:",omitempty"`
	CurrentRole    string `json:",omitempty"`
}

func ecNow() string { return time.Now().UTC().Format(time.RFC3339) }

func ecCacheNodeID(index int) string { return fmt.Sprintf("%04d", index+1) }

func ecEndpointXML(element, address string, port int) string {
	return fmt.Sprintf("<%s><Address>%s</Address><Port>%d</Port></%s>", element, xmlEscape(address), port, element)
}

func renderECCluster(c ECCluster, showNodes bool) string {
	var b strings.Builder
	b.WriteString("<CacheCluster>")
	fmt.Fprintf(&b, "<CacheClusterId>%s</CacheClusterId>", xmlEscape(c.CacheClusterId))
	fmt.Fprintf(&b, "<CacheNodeType>%s</CacheNodeType>", xmlEscape(c.CacheNodeType))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(c.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(c.EngineVersion))
	fmt.Fprintf(&b, "<CacheClusterStatus>%s</CacheClusterStatus>", xmlEscape(c.CacheClusterStatus))
	fmt.Fprintf(&b, "<NumCacheNodes>%d</NumCacheNodes>", c.NumCacheNodes)
	fmt.Fprintf(&b, "<CacheClusterCreateTime>%s</CacheClusterCreateTime>", xmlEscape(c.CacheClusterCreateTime))
	fmt.Fprintf(&b, "<ARN>%s</ARN>", xmlEscape(c.ARN))
	fmt.Fprintf(&b, "<PreferredAvailabilityZone>%s</PreferredAvailabilityZone>", xmlEscape(ecClusterZone(c)))
	if c.Engine == "memcached" && c.Endpoint != "" {
		b.WriteString(ecEndpointXML("ConfigurationEndpoint", c.Endpoint, c.Port))
	}
	if c.ReplicationGroupId != "" {
		fmt.Fprintf(&b, "<ReplicationGroupId>%s</ReplicationGroupId>", xmlEscape(c.ReplicationGroupId))
	}
	if c.CacheSubnetGroupName != "" {
		fmt.Fprintf(&b, "<CacheSubnetGroupName>%s</CacheSubnetGroupName>", xmlEscape(c.CacheSubnetGroupName))
	}
	fmt.Fprintf(&b, "<AuthTokenEnabled>%t</AuthTokenEnabled>", c.AuthTokenEnabled)
	fmt.Fprintf(&b, "<TransitEncryptionEnabled>%t</TransitEncryptionEnabled>", c.TransitEncryptionEnabled)
	if c.TransitEncryptionMode != "" {
		fmt.Fprintf(&b, "<TransitEncryptionMode>%s</TransitEncryptionMode>", xmlEscape(c.TransitEncryptionMode))
	}
	if showNodes {
		b.WriteString("<CacheNodes>")
		for _, node := range c.CacheNodes {
			b.WriteString("<CacheNode>")
			fmt.Fprintf(&b, "<CacheNodeId>%s</CacheNodeId>", xmlEscape(node.CacheNodeId))
			fmt.Fprintf(&b, "<CacheNodeStatus>%s</CacheNodeStatus>", xmlEscape(node.CacheNodeStatus))
			fmt.Fprintf(&b, "<CacheNodeCreateTime>%s</CacheNodeCreateTime>", xmlEscape(node.CacheNodeCreateTime))
			if node.Address != "" {
				b.WriteString(ecEndpointXML("Endpoint", node.Address, node.Port))
			}
			b.WriteString("<ParameterGroupStatus>in-sync</ParameterGroupStatus>")
			fmt.Fprintf(&b, "<CustomerAvailabilityZone>%s</CustomerAvailabilityZone>", xmlEscape(ecClusterZone(c)))
			b.WriteString("</CacheNode>")
		}
		b.WriteString("</CacheNodes>")
	}
	b.WriteString("</CacheCluster>")
	return b.String()
}

func ecRequestedBool(r *http.Request, name string) bool {
	return strings.EqualFold(r.FormValue(name), "true")
}

// ecTransitEncryption reads a create's in-transit encryption: whether it
// encrypts, and whether it also accepts unencrypted clients.
func ecTransitEncryption(r *http.Request) (enabled bool, mode string, err error) {
	enabled = ecRequestedBool(r, "TransitEncryptionEnabled")
	mode = r.FormValue("TransitEncryptionMode")
	switch {
	case mode != "" && !enabled:
		return false, "", errors.New("TransitEncryptionMode requires TransitEncryptionEnabled")
	case mode != "" && mode != "preferred" && mode != "required":
		return false, "", fmt.Errorf("TransitEncryptionMode %q is not one of preferred or required", mode)
	case enabled && mode == "":
		mode = "required"
	}
	return enabled, mode, nil
}

// ecValidAuthToken reports whether an AUTH token meets the constraints the
// ElastiCache model states: 16 to 128 printable ASCII characters.
func ecValidAuthToken(token string) bool {
	if len(token) < 16 || len(token) > 128 {
		return false
	}
	for _, r := range token {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

// ecRunInBackground runs op on a resource's engine and reports its outcome
// to settled, with a nil engine when this simulator runs none.
func ecRunInBackground(engine *ecEngine, op func(*ecEngine) error, settled func(error)) {
	bg.Go(func() {
		var err error
		if engine != nil {
			engine.opMu.Lock()
			err = op(engine)
			engine.opMu.Unlock()
			if err != nil {
				ecLogEngineFailure(engine.key, err)
			} else {
				engine.save()
			}
		}
		settled(err)
	})
}

func handleECCreate(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	id := strings.ToLower(r.FormValue("CacheClusterId"))
	if id == "" {
		ecErrorXML(w, "MissingParameter", "CacheClusterId is required", http.StatusBadRequest, requestID)
		return
	}
	if _, ok := ecClusters.Get(id); ok {
		ecErrorXML(w, "CacheClusterAlreadyExists", "Cluster already exists", http.StatusBadRequest, requestID)
		return
	}
	if group := r.FormValue("ReplicationGroupId"); group != "" {
		ecAddReplicaCluster(w, r, id, group)
		return
	}
	engine := r.FormValue("Engine")
	if engine == "" {
		engine = "redis"
	}
	version, known := ecResolveEngineVersion(engine, r.FormValue("EngineVersion"))
	if !known {
		ecErrorXML(w, "InvalidParameterCombination",
			fmt.Sprintf("Cannot find version %s for %s", r.FormValue("EngineVersion"), engine), http.StatusBadRequest, requestID)
		return
	}
	nodes := atoiOrZero(r.FormValue("NumCacheNodes"))
	if nodes == 0 {
		nodes = 1
	}
	port := ecRedisPort
	if engine == "memcached" {
		port = ecMemcachedPort
		if nodes > 40 {
			ecErrorXML(w, "InvalidParameterValue", "NumCacheNodes must be between 1 and 40 for Memcached.", http.StatusBadRequest, requestID)
			return
		}
	} else if nodes != 1 {
		ecErrorXML(w, "InvalidParameterValue", "NumCacheNodes must be 1 for a cluster running Valkey or Redis OSS.", http.StatusBadRequest, requestID)
		return
	}
	if p := atoiOrZero(r.FormValue("Port")); p > 0 {
		port = p
	}
	transit, mode, err := ecTransitEncryption(r)
	if err != nil {
		ecErrorXML(w, "InvalidParameterCombination", err.Error(), http.StatusBadRequest, requestID)
		return
	}
	token := r.FormValue("AuthToken")
	if token != "" {
		if engine == "memcached" {
			ecErrorXML(w, "InvalidParameterCombination", "AuthToken is only valid for Valkey or Redis OSS.", http.StatusBadRequest, requestID)
			return
		}
		if !ecValidAuthToken(token) {
			ecErrorXML(w, "InvalidParameterValue", "The AUTH token must be 16 to 128 printable ASCII characters.", http.StatusBadRequest, requestID)
			return
		}
	}
	sealed, err := ecSealToken(token)
	if err != nil {
		ecErrorXML(w, "InternalFailure", err.Error(), http.StatusInternalServerError, requestID)
		return
	}
	created := ecNow()
	c := ECCluster{
		CacheClusterId:            id,
		CacheNodeType:             r.FormValue("CacheNodeType"),
		Engine:                    engine,
		EngineVersion:             version.Version,
		CacheClusterStatus:        "creating",
		NumCacheNodes:             nodes,
		CacheClusterCreateTime:    created,
		ARN:                       ecClusterARN(id),
		Port:                      port,
		Tags:                      parseAWSQueryTagMap(r, "Tags.Tag"),
		CacheSubnetGroupName:      r.FormValue("CacheSubnetGroupName"),
		AuthTokenEnabled:          token != "",
		TransitEncryptionEnabled:  transit,
		TransitEncryptionMode:     mode,
		PreferredAvailabilityZone: r.FormValue("PreferredAvailabilityZone"),
		Incarnation:               sim.NewUUID(),
	}
	record := ecEngineRecord{
		Kind: ecKindCluster, ID: id, Engine: engine, Image: version.Image,
		TLS: transit, TLSPreferred: mode == "preferred", Token: sealed,
	}
	var wanted []cacheengine.Endpoint
	if engine == "memcached" {
		wanted = append(wanted, cacheengine.Endpoint{Role: cacheengine.DiscoveryEndpoint, Port: port})
		for node := 0; node < nodes; node++ {
			wanted = append(wanted, cacheengine.Endpoint{Role: cacheengine.NodeEndpoint, Port: port, Node: node})
			record.Nodes = append(record.Nodes, node)
		}
	} else {
		wanted = []cacheengine.Endpoint{{Role: cacheengine.PrimaryEndpoint, Port: port}}
		record.Nodes = []int{0}
	}
	running, endpoints, err := ecInstallEngine(record, wanted)
	if err != nil {
		ecErrorXML(w, "InsufficientCacheClusterCapacity", err.Error(), http.StatusBadRequest, requestID)
		return
	}
	c.Endpoint, _ = ecEndpointOf(endpoints, cacheengine.DiscoveryEndpoint, 0)
	c.CacheNodes = ecClusterNodes(c, endpoints, nodes, "creating", created)
	ecClusters.Put(id, c)
	ecRunInBackground(running, (*ecEngine).start, func(err error) {
		ecSettleCluster(id, c.Incarnation, err)
	})
	ecXMLResponse(w, "CreateCacheCluster", renderECCluster(c, false), requestID)
}

// ecClusterNodes are a standalone cluster's nodes at the endpoints its engine
// serves them at.
func ecClusterNodes(c ECCluster, endpoints []cacheengine.Endpoint, count int, status, created string) []ECCacheNode {
	nodes := make([]ECCacheNode, 0, count)
	for index := 0; index < count; index++ {
		node := ECCacheNode{CacheNodeId: ecCacheNodeID(index), CacheNodeStatus: status, CacheNodeCreateTime: created}
		if c.Engine == "memcached" {
			node.Address, node.Port = ecEndpointOf(endpoints, cacheengine.NodeEndpoint, index)
		} else {
			node.Address, node.Port = ecEndpointOf(endpoints, cacheengine.PrimaryEndpoint, 0)
		}
		nodes = append(nodes, node)
	}
	return nodes
}

// ecSettleCluster moves a cluster whose engine settled to available, or to
// create-failed when the engine did not start; the model lists no failure
// status for a cache cluster, and create-failed is the one it lists for a
// replication group and a serverless cache.
func ecSettleCluster(id, incarnation string, err error) {
	ecClusters.Update(id, func(c *ECCluster) {
		if c.Incarnation != incarnation || c.CacheClusterStatus != "creating" {
			return
		}
		status := "available"
		if err != nil {
			status = "create-failed"
		}
		c.CacheClusterStatus = status
		for i := range c.CacheNodes {
			c.CacheNodes[i].CacheNodeStatus = status
		}
	})
}

// ecSetClusterAvailable ends a cluster's modification or reboot.
func ecSetClusterAvailable(id, incarnation string) {
	ecClusters.Update(id, func(c *ECCluster) {
		if c.Incarnation != incarnation {
			return
		}
		c.CacheClusterStatus = "available"
		for i := range c.CacheNodes {
			c.CacheNodes[i].CacheNodeStatus = "available"
		}
	})
}

func handleECDescribe(w http.ResponseWriter, r *http.Request) {
	wanted := strings.ToLower(r.FormValue("CacheClusterId"))
	showNodes := ecRequestedBool(r, "ShowCacheNodeInfo")
	standalone := ecRequestedBool(r, "ShowCacheClustersNotInReplicationGroups")
	var b strings.Builder
	b.WriteString("<CacheClusters>")
	matched := false
	for _, c := range ecClusters.List() {
		if wanted != "" && c.CacheClusterId != wanted {
			continue
		}
		if standalone && c.ReplicationGroupId != "" {
			continue
		}
		matched = true
		b.WriteString(renderECCluster(c, showNodes))
	}
	if wanted != "" && !matched {
		ecErrorXML(w, "CacheClusterNotFound", fmt.Sprintf("Cache cluster %q not found", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</CacheClusters>")
	ecXMLResponse(w, "DescribeCacheClusters", b.String(), sim.RequestID(r.Context()))
}

func ecInvalidClusterState(w http.ResponseWriter, r *http.Request, c ECCluster) {
	ecErrorXML(w, "InvalidCacheClusterState",
		fmt.Sprintf("Cache cluster %s is %s, not available.", c.CacheClusterId, c.CacheClusterStatus), http.StatusBadRequest, sim.RequestID(r.Context()))
}

func handleECModify(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	id := strings.ToLower(r.FormValue("CacheClusterId"))
	c, ok := ecClusters.Get(id)
	if !ok {
		ecErrorXML(w, "CacheClusterNotFound", "Cluster not found", http.StatusNotFound, requestID)
		return
	}
	nodes := c.NumCacheNodes
	if v := r.FormValue("NumCacheNodes"); v != "" {
		nodes = atoiOrZero(v)
	}
	if c.Engine != "memcached" && nodes != 1 {
		ecErrorXML(w, "InvalidParameterValue", "NumCacheNodes must be 1 for a cluster running Valkey or Redis OSS.", http.StatusBadRequest, requestID)
		return
	}
	if nodes < 1 || nodes > 40 {
		ecErrorXML(w, "InvalidParameterValue", "NumCacheNodes must be between 1 and 40 for Memcached.", http.StatusBadRequest, requestID)
		return
	}
	version := ecEngineVersion{Version: c.EngineVersion}
	if v := r.FormValue("EngineVersion"); v != "" {
		resolved, known := ecResolveEngineVersion(c.Engine, v)
		if !known {
			ecErrorXML(w, "InvalidParameterCombination", fmt.Sprintf("Cannot find version %s for %s", v, c.Engine), http.StatusBadRequest, requestID)
			return
		}
		if resolved.Version != c.EngineVersion {
			version = resolved
		}
	}
	token, strategy := r.FormValue("AuthToken"), r.FormValue("AuthTokenUpdateStrategy")
	if token != "" && !ecValidAuthToken(token) {
		ecErrorXML(w, "InvalidParameterValue", "The AUTH token must be 16 to 128 printable ASCII characters.", http.StatusBadRequest, requestID)
		return
	}
	if (token != "" || strategy == "DELETE") && c.Engine == "memcached" {
		ecErrorXML(w, "InvalidParameterCombination", "AuthToken is only valid for Valkey or Redis OSS.", http.StatusBadRequest, requestID)
		return
	}
	engineChange := nodes != c.NumCacheNodes || version.Version != c.EngineVersion || token != "" || strategy == "DELETE"
	if engineChange && c.CacheClusterStatus != "available" {
		ecInvalidClusterState(w, r, c)
		return
	}
	if engineChange && c.ReplicationGroupId != "" {
		ecErrorXML(w, "InvalidCacheClusterState",
			fmt.Sprintf("Cache cluster %s is a member of replication group %s; modify the replication group.", id, c.ReplicationGroupId), http.StatusBadRequest, requestID)
		return
	}
	ecClusters.Update(id, func(stored *ECCluster) {
		if v := r.FormValue("CacheNodeType"); v != "" {
			stored.CacheNodeType = v
		}
		if engineChange {
			stored.CacheClusterStatus = "modifying"
		}
	})
	updated, _ := ecClusters.Get(id)
	if engineChange {
		engine, _ := ecLoadEngine(ecEngineKey(ecKindCluster, id))
		ecRunInBackground(engine, func(e *ecEngine) error {
			return ecModifyClusterEngine(e, c, nodes, version, token, strategy)
		}, func(err error) {
			ecClusters.Update(id, func(stored *ECCluster) {
				if stored.Incarnation != c.Incarnation {
					return
				}
				if err == nil {
					stored.NumCacheNodes, stored.EngineVersion = nodes, version.Version
					switch {
					case strategy == "DELETE":
						stored.AuthTokenEnabled = false
					case token != "":
						stored.AuthTokenEnabled = true
					}
					stored.CacheNodes = ecClusterNodes(*stored, ecEngineEndpoints(engine), nodes, "available", stored.CacheClusterCreateTime)
					if stored.Engine == "memcached" {
						stored.Endpoint, _ = ecEndpointOf(ecEngineEndpoints(engine), cacheengine.DiscoveryEndpoint, 0)
					}
				}
			})
			ecSetClusterAvailable(id, c.Incarnation)
		})
	}
	ecXMLResponse(w, "ModifyCacheCluster", renderECCluster(updated, false), requestID)
}

// ecEngineEndpoints is the endpoints a resource's engine serves, or none
// without an engine.
func ecEngineEndpoints(engine *ecEngine) []cacheengine.Endpoint {
	if engine == nil {
		return nil
	}
	return engine.endpoints()
}

func ecModifyClusterEngine(e *ecEngine, c ECCluster, nodes int, version ecEngineVersion, token, strategy string) error {
	if e.memcached != nil {
		if nodes != c.NumCacheNodes {
			if err := e.memcached.Resize(nodes, c.Port); err != nil {
				return err
			}
		}
		if version.Image != "" {
			return fmt.Errorf("the simulator upgrades no Memcached engine in place")
		}
		return nil
	}
	if strategy == "DELETE" {
		token = ""
	}
	if token != "" || strategy == "DELETE" {
		if err := e.redis.SetPassword(token); err != nil {
			return err
		}
		sealed, err := ecSealToken(token)
		if err != nil {
			return err
		}
		ecEngineRecords.Update(e.key, func(record *ecEngineRecord) { record.Token = sealed })
	}
	if version.Image != "" {
		if err := e.redis.Restart(nil, version.Image); err != nil {
			return err
		}
		ecEngineRecords.Update(e.key, func(record *ecEngineRecord) { record.Image = version.Image })
	}
	return nil
}

func handleECDelete(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	id := strings.ToLower(r.FormValue("CacheClusterId"))
	c, ok := ecClusters.Get(id)
	if !ok {
		ecErrorXML(w, "CacheClusterNotFound", "Cluster not found", http.StatusNotFound, requestID)
		return
	}
	if c.ReplicationGroupId != "" {
		ecErrorXML(w, "InvalidCacheClusterState",
			fmt.Sprintf("Cache cluster %s is a member of replication group %s; delete the replication group or decrease its replica count.", id, c.ReplicationGroupId),
			http.StatusBadRequest, requestID)
		return
	}
	if c.CacheClusterStatus == "deleting" {
		ecInvalidClusterState(w, r, c)
		return
	}
	ecClusters.Update(id, func(stored *ECCluster) {
		stored.CacheClusterStatus = "deleting"
		for i := range stored.CacheNodes {
			stored.CacheNodes[i].CacheNodeStatus = "deleting"
		}
	})
	c, _ = ecClusters.Get(id)
	ecDeleteClusterInBackground(id, c.Incarnation)
	ecXMLResponse(w, "DeleteCacheCluster", renderECCluster(c, false), requestID)
}

func ecDeleteClusterInBackground(id, incarnation string) {
	bg.Go(func() {
		ecRemoveEngine(ecEngineKey(ecKindCluster, id))
		if c, ok := ecClusters.Get(id); ok && c.Incarnation == incarnation {
			ecClusters.Delete(id)
		}
	})
}

// ecNodesToReboot reads CacheNodeIdsToReboot as node indices.
func ecNodesToReboot(r *http.Request, count int) ([]int, bool) {
	ids := ecParseStringList(r, "CacheNodeIdsToReboot", "CacheNodeId")
	if len(ids) == 0 {
		return nil, false
	}
	var nodes []int
	for _, id := range ids {
		index, err := strconv.Atoi(id)
		if err != nil || index < 1 || index > count {
			return nil, false
		}
		nodes = append(nodes, index-1)
	}
	return nodes, true
}

func handleECReboot(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	id := strings.ToLower(r.FormValue("CacheClusterId"))
	c, ok := ecClusters.Get(id)
	if !ok {
		ecErrorXML(w, "CacheClusterNotFound", "Cluster not found", http.StatusNotFound, requestID)
		return
	}
	if c.CacheClusterStatus != "available" {
		ecInvalidClusterState(w, r, c)
		return
	}
	nodes, ok := ecNodesToReboot(r, c.NumCacheNodes)
	if !ok {
		ecErrorXML(w, "InvalidParameterValue", "CacheNodeIdsToReboot must name nodes of the cluster.", http.StatusBadRequest, requestID)
		return
	}
	rebooting := map[string]bool{}
	for _, index := range nodes {
		rebooting[ecCacheNodeID(index)] = true
	}
	ecClusters.Update(id, func(stored *ECCluster) {
		stored.CacheClusterStatus = "rebooting cluster nodes"
		for i := range stored.CacheNodes {
			if rebooting[stored.CacheNodes[i].CacheNodeId] {
				stored.CacheNodes[i].CacheNodeStatus = "rebooting"
			}
		}
	})
	updated, _ := ecClusters.Get(id)
	settle := func(error) { ecSetClusterAvailable(id, c.Incarnation) }
	if c.ReplicationGroupId != "" {
		group, _ := ecReplGroups.Get(c.ReplicationGroupId)
		engine, _ := ecLoadEngine(ecEngineKey(ecKindReplicationGroup, c.ReplicationGroupId))
		node, member := ecMemberNode(group, id)
		ecRunInBackground(engine, func(e *ecEngine) error {
			if !member {
				return fmt.Errorf("cache cluster %s is no node of replication group %s", id, group.ReplicationGroupId)
			}
			return e.redis.Reboot([]int{node})
		}, settle)
	} else {
		engine, _ := ecLoadEngine(ecEngineKey(ecKindCluster, id))
		ecRunInBackground(engine, func(e *ecEngine) error {
			if e.memcached != nil {
				return e.memcached.Reboot(nodes)
			}
			return e.redis.Reboot(nodes)
		}, settle)
	}
	ecXMLResponse(w, "RebootCacheCluster", renderECCluster(updated, false), requestID)
}

func ecMemberNode(group ECReplicationGroup, cluster string) (int, bool) {
	for node, name := range group.MemberNodes {
		if name == cluster {
			return node, true
		}
	}
	return 0, false
}

func renderECReplGroup(g ECReplicationGroup) string {
	var b strings.Builder
	b.WriteString("<ReplicationGroup>")
	fmt.Fprintf(&b, "<ReplicationGroupId>%s</ReplicationGroupId>", xmlEscape(g.ReplicationGroupId))
	fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(g.Description))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(g.Status))
	fmt.Fprintf(&b, "<CacheNodeType>%s</CacheNodeType>", xmlEscape(g.CacheNodeType))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(g.Engine))
	fmt.Fprintf(&b, "<AutomaticFailover>%s</AutomaticFailover>", xmlEscape(g.AutomaticFailover))
	fmt.Fprintf(&b, "<MultiAZ>%s</MultiAZ>", xmlEscape(g.MultiAZ))
	fmt.Fprintf(&b, "<ClusterEnabled>%t</ClusterEnabled>", g.ClusterEnabled)
	if g.ClusterEnabled {
		b.WriteString("<ClusterMode>enabled</ClusterMode>")
	} else {
		b.WriteString("<ClusterMode>disabled</ClusterMode>")
	}
	fmt.Fprintf(&b, "<SnapshotRetentionLimit>%d</SnapshotRetentionLimit>", g.SnapshotRetentionLimit)
	if g.SnapshotWindow != "" {
		fmt.Fprintf(&b, "<SnapshotWindow>%s</SnapshotWindow>", xmlEscape(g.SnapshotWindow))
	}
	fmt.Fprintf(&b, "<ReplicationGroupCreateTime>%s</ReplicationGroupCreateTime>", xmlEscape(g.CreateTime))
	fmt.Fprintf(&b, "<ARN>%s</ARN>", xmlEscape(g.ARN))
	fmt.Fprintf(&b, "<AuthTokenEnabled>%t</AuthTokenEnabled>", g.AuthTokenEnabled)
	fmt.Fprintf(&b, "<TransitEncryptionEnabled>%t</TransitEncryptionEnabled>", g.TransitEncryptionEnabled)
	if g.TransitEncryptionMode != "" {
		fmt.Fprintf(&b, "<TransitEncryptionMode>%s</TransitEncryptionMode>", xmlEscape(g.TransitEncryptionMode))
	}
	b.WriteString("<MemberClusters>")
	for _, m := range g.MemberClusters {
		fmt.Fprintf(&b, "<ClusterId>%s</ClusterId>", xmlEscape(m))
	}
	b.WriteString("</MemberClusters>")
	b.WriteString("<NodeGroups>")
	for _, group := range g.NodeGroups {
		b.WriteString("<NodeGroup>")
		fmt.Fprintf(&b, "<NodeGroupId>%s</NodeGroupId>", xmlEscape(group.NodeGroupId))
		fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(group.Status))
		if group.Slots != "" {
			fmt.Fprintf(&b, "<Slots>%s</Slots>", xmlEscape(group.Slots))
		}
		if group.PrimaryAddress != "" {
			b.WriteString(ecEndpointXML("PrimaryEndpoint", group.PrimaryAddress, group.PrimaryPort))
		}
		if group.ReaderAddress != "" {
			b.WriteString(ecEndpointXML("ReaderEndpoint", group.ReaderAddress, group.ReaderPort))
		}
		b.WriteString("<NodeGroupMembers>")
		for _, member := range group.Members {
			b.WriteString("<NodeGroupMember>")
			fmt.Fprintf(&b, "<CacheClusterId>%s</CacheClusterId>", xmlEscape(member.CacheClusterId))
			fmt.Fprintf(&b, "<CacheNodeId>%s</CacheNodeId>", xmlEscape(member.CacheNodeId))
			if member.Address != "" && !g.ClusterEnabled {
				b.WriteString(ecEndpointXML("ReadEndpoint", member.Address, member.Port))
			}
			if member.CurrentRole != "" {
				fmt.Fprintf(&b, "<CurrentRole>%s</CurrentRole>", xmlEscape(member.CurrentRole))
			}
			b.WriteString("</NodeGroupMember>")
		}
		b.WriteString("</NodeGroupMembers>")
		b.WriteString("</NodeGroup>")
	}
	b.WriteString("</NodeGroups>")
	if g.ClusterEnabled && g.ConfigEndpointAddress != "" {
		b.WriteString(ecEndpointXML("ConfigurationEndpoint", g.ConfigEndpointAddress, g.ConfigEndpointPort))
	}
	b.WriteString("</ReplicationGroup>")
	return b.String()
}

func handleECCreateReplGroup(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	invalid := func(code, message string) {
		ecErrorXML(w, code, message, http.StatusBadRequest, requestID)
	}
	id := strings.ToLower(r.FormValue("ReplicationGroupId"))
	if id == "" {
		invalid("MissingParameter", "ReplicationGroupId is required")
		return
	}
	if _, ok := ecReplGroups.Get(id); ok {
		invalid("ReplicationGroupAlreadyExistsFault", "Replication group already exists")
		return
	}
	engine := r.FormValue("Engine")
	if engine == "" {
		engine = "redis"
	}
	if engine != "redis" && engine != "valkey" {
		invalid("InvalidParameterValue", "Engine must be valkey or redis.")
		return
	}
	version, known := ecResolveEngineVersion(engine, r.FormValue("EngineVersion"))
	if !known {
		invalid("InvalidParameterCombination", fmt.Sprintf("Cannot find version %s for %s", r.FormValue("EngineVersion"), engine))
		return
	}
	shards := atoiOrZero(r.FormValue("NumNodeGroups"))
	if shards == 0 {
		shards = 1
	}
	clusterEnabled := strings.EqualFold(r.FormValue("ClusterMode"), "enabled") || shards > 1
	replicas := 0
	switch {
	case r.FormValue("ReplicasPerNodeGroup") != "":
		replicas = atoiOrZero(r.FormValue("ReplicasPerNodeGroup"))
	case r.FormValue("NumCacheClusters") != "":
		if clusterEnabled {
			invalid("InvalidParameterCombination", "NumCacheClusters is not used if there is more than one node group (shard); use ReplicasPerNodeGroup.")
			return
		}
		replicas = atoiOrZero(r.FormValue("NumCacheClusters")) - 1
	}
	if replicas < 0 || replicas > 5 {
		invalid("InvalidParameterValue", "A node group runs 0 to 5 replicas.")
		return
	}
	failover := ecRequestedBool(r, "AutomaticFailoverEnabled")
	if failover && !clusterEnabled && replicas < 1 {
		invalid("InvalidParameterCombination", "AutomaticFailoverEnabled requires NumCacheClusters of at least 2.")
		return
	}
	transit, mode, err := ecTransitEncryption(r)
	if err != nil {
		invalid("InvalidParameterCombination", err.Error())
		return
	}
	token := r.FormValue("AuthToken")
	if token != "" {
		if !transit {
			invalid("InvalidParameterCombination", "AuthToken can be specified only on replication groups where TransitEncryptionEnabled is true.")
			return
		}
		if !ecValidAuthToken(token) {
			invalid("InvalidParameterValue", "The AUTH token must be 16 to 128 printable ASCII characters.")
			return
		}
	}
	sealed, err := ecSealToken(token)
	if err != nil {
		ecErrorXML(w, "InternalFailure", err.Error(), http.StatusInternalServerError, requestID)
		return
	}
	port := ecRedisPort
	if p := atoiOrZero(r.FormValue("Port")); p > 0 {
		port = p
	}
	multiAZ := "disabled"
	if ecRequestedBool(r, "MultiAZEnabled") {
		multiAZ = "enabled"
	}
	automaticFailover := "disabled"
	if failover {
		automaticFailover = "enabled"
	}
	nodes := shards * (replicas + 1)
	g := ECReplicationGroup{
		ReplicationGroupId:       id,
		Description:              r.FormValue("ReplicationGroupDescription"),
		Status:                   "creating",
		CacheNodeType:            r.FormValue("CacheNodeType"),
		Engine:                   engine,
		EngineVersion:            version.Version,
		AutomaticFailover:        automaticFailover,
		MultiAZ:                  multiAZ,
		ClusterEnabled:           clusterEnabled,
		SnapshotRetentionLimit:   atoiOrZero(r.FormValue("SnapshotRetentionLimit")),
		SnapshotWindow:           r.FormValue("SnapshotWindow"),
		ARN:                      ecReplGroupARN(id),
		CreateTime:               ecNow(),
		Tags:                     parseAWSQueryTagMap(r, "Tags.Tag"),
		Port:                     port,
		CacheSubnetGroupName:     r.FormValue("CacheSubnetGroupName"),
		AuthTokenEnabled:         token != "",
		TransitEncryptionEnabled: transit,
		TransitEncryptionMode:    mode,
		MemberNodes:              map[int]string{},
		Incarnation:              sim.NewUUID(),
	}
	record := ecEngineRecord{
		Kind: ecKindReplicationGroup, ID: id, Engine: engine, Image: version.Image, Cluster: clusterEnabled,
		TLS: transit, TLSPreferred: mode == "preferred", Token: sealed,
	}
	record.Replicas = replicas
	var wanted []cacheengine.Endpoint
	if clusterEnabled {
		record.Shards = shards
		wanted = append(wanted, cacheengine.Endpoint{Role: cacheengine.DiscoveryEndpoint, Port: port})
	} else {
		wanted = append(wanted, cacheengine.Endpoint{Role: cacheengine.PrimaryEndpoint, Port: port}, cacheengine.Endpoint{Role: cacheengine.ReadEndpoint, Port: port})
	}
	for node := 0; node < nodes; node++ {
		record.Nodes = append(record.Nodes, node)
		wanted = append(wanted, cacheengine.Endpoint{Role: cacheengine.NodeEndpoint, Port: port, Node: node})
		g.MemberNodes[node] = ecInitialMemberName(id, node, shards, replicas, clusterEnabled)
	}
	running, endpoints, err := ecInstallEngine(record, wanted)
	if err != nil {
		ecErrorXML(w, "InsufficientCacheClusterCapacity", err.Error(), http.StatusBadRequest, requestID)
		return
	}
	ecDescribeReplGroupTopology(&g, ecShapeTopology(shards, replicas, clusterEnabled), endpoints, "creating")
	ecReplGroups.Put(id, g)
	ecPutMemberClusters(g, "creating")
	ecRunInBackground(running, (*ecEngine).start, func(err error) {
		ecSettleReplGroup(id, g.Incarnation, running, err)
	})
	ecXMLResponse(w, "CreateReplicationGroup", renderECReplGroup(g), requestID)
}

// ecInitialMemberName is the member cluster a new replication group's node
// is: <group>-00N for a group without cluster mode, and
// <group>-<shard>-<member> with it, the shard's primary first.
func ecInitialMemberName(id string, node, shards, replicas int, cluster bool) string {
	if !cluster {
		return fmt.Sprintf("%s-%03d", id, node+1)
	}
	if node < shards {
		return fmt.Sprintf("%s-%04d-%03d", id, node+1, 1)
	}
	replica := node - shards
	return fmt.Sprintf("%s-%04d-%03d", id, replica/max(replicas, 1)+1, replica%max(replicas, 1)+2)
}

// ecTopology is the shape a replication group's engine runs: each shard's
// primary node, its replicas, and the slots it owns.
type ecTopology struct {
	primaries []int
	followers map[int][]int
	slots     map[int][]string
}

// ecReadTopology reads the topology a group's engine runs.
func ecReadTopology(g ECReplicationGroup, e *ecEngine) (ecTopology, error) {
	if !g.ClusterEnabled {
		return ecTopology{primaries: []int{e.redis.Primary()}, followers: map[int][]int{e.redis.Primary(): e.redis.Replicas()}}, nil
	}
	primaries, followers, err := e.redis.ShardMap()
	if err != nil {
		return ecTopology{}, err
	}
	slots, err := e.redis.ShardSlots()
	if err != nil {
		return ecTopology{}, err
	}
	return ecTopology{primaries: primaries, followers: followers, slots: slots}, nil
}

// ecShapeTopology is the topology a new group's engine forms: shard s's
// primary is node s, and its replicas follow the primaries, shard by shard,
// as cacheengine forms a cluster; without cluster mode node 0 is the primary.
func ecShapeTopology(shards, replicas int, cluster bool) ecTopology {
	topology := ecTopology{followers: map[int][]int{}, slots: map[int][]string{}}
	if !cluster {
		topology.primaries = []int{0}
		for r := 1; r <= replicas; r++ {
			topology.followers[0] = append(topology.followers[0], r)
		}
		return topology
	}
	for shard := 0; shard < shards; shard++ {
		topology.primaries = append(topology.primaries, shard)
		first, last := cacheengine.SlotRange(shard, shards)
		topology.slots[shard] = []string{fmt.Sprintf("%d-%d", first, last)}
		for r := 0; r < replicas; r++ {
			topology.followers[shard] = append(topology.followers[shard], shards+shard*replicas+r)
		}
	}
	return topology
}

// ecDescribeReplGroupTopology fills a group's node groups, member clusters
// and endpoints from the topology its engine runs. Nodes the group has not yet
// named take the next member name of their shard.
func ecDescribeReplGroupTopology(g *ECReplicationGroup, shape ecTopology, endpoints []cacheengine.Endpoint, status string) {
	taken := map[string]bool{}
	present := map[int]bool{}
	for _, primary := range shape.primaries {
		present[primary] = true
		for _, follower := range shape.followers[primary] {
			present[follower] = true
		}
	}
	for node, name := range g.MemberNodes {
		if !present[node] {
			delete(g.MemberNodes, node)
			continue
		}
		taken[name] = true
	}
	nameFor := func(node, shard, member int) string {
		if name, ok := g.MemberNodes[node]; ok {
			return name
		}
		for n := member + 1; ; n++ {
			name := fmt.Sprintf("%s-%03d", g.ReplicationGroupId, n)
			if g.ClusterEnabled {
				name = fmt.Sprintf("%s-%04d-%03d", g.ReplicationGroupId, shard+1, n)
			}
			if !taken[name] {
				taken[name] = true
				g.MemberNodes[node] = name
				return name
			}
		}
	}
	g.NodeGroups = nil
	g.MemberClusters = nil
	for shard, primary := range shape.primaries {
		group := ECNodeGroup{NodeGroupId: fmt.Sprintf("%04d", shard+1), Status: status}
		if g.ClusterEnabled {
			group.Slots = strings.Join(shape.slots[primary], ",")
		} else {
			group.PrimaryAddress, group.PrimaryPort = ecEndpointOf(endpoints, cacheengine.PrimaryEndpoint, 0)
			group.ReaderAddress, group.ReaderPort = ecEndpointOf(endpoints, cacheengine.ReadEndpoint, 0)
		}
		for member, node := range append([]int{primary}, shape.followers[primary]...) {
			entry := ECNodeGroupMember{CacheClusterId: nameFor(node, shard, member), CacheNodeId: ecCacheNodeID(0)}
			entry.Address, entry.Port = ecEndpointOf(endpoints, cacheengine.NodeEndpoint, node)
			if !g.ClusterEnabled {
				entry.CurrentRole = "replica"
				if member == 0 {
					entry.CurrentRole = "primary"
				}
			}
			group.Members = append(group.Members, entry)
			g.MemberClusters = append(g.MemberClusters, entry.CacheClusterId)
		}
		g.NodeGroups = append(g.NodeGroups, group)
	}
	sort.Strings(g.MemberClusters)
	if g.ClusterEnabled {
		g.ConfigEndpointAddress, g.ConfigEndpointPort = ecEndpointOf(endpoints, cacheengine.DiscoveryEndpoint, 0)
	} else {
		g.ConfigEndpointAddress, g.ConfigEndpointPort = "", 0
	}
}

// ecPutMemberClusters records a group's member clusters, each a cache
// cluster of one node at the endpoint its node answers on, and removes the
// members it no longer has.
func ecPutMemberClusters(g ECReplicationGroup, status string) {
	members := map[string]bool{}
	for _, group := range g.NodeGroups {
		for _, member := range group.Members {
			members[member.CacheClusterId] = true
			existing, found := ecClusters.Get(member.CacheClusterId)
			c := ECCluster{
				CacheClusterId:           member.CacheClusterId,
				CacheNodeType:            g.CacheNodeType,
				Engine:                   g.Engine,
				EngineVersion:            g.EngineVersion,
				CacheClusterStatus:       status,
				NumCacheNodes:            1,
				CacheClusterCreateTime:   g.CreateTime,
				ARN:                      ecClusterARN(member.CacheClusterId),
				Port:                     g.Port,
				ReplicationGroupId:       g.ReplicationGroupId,
				CacheSubnetGroupName:     g.CacheSubnetGroupName,
				AuthTokenEnabled:         g.AuthTokenEnabled,
				TransitEncryptionEnabled: g.TransitEncryptionEnabled,
				TransitEncryptionMode:    g.TransitEncryptionMode,
				Incarnation:              g.Incarnation,
			}
			if found && existing.ReplicationGroupId == g.ReplicationGroupId {
				c.Tags, c.CacheClusterCreateTime = existing.Tags, existing.CacheClusterCreateTime
			} else {
				c.CacheClusterCreateTime = ecNow()
			}
			c.CacheNodes = []ECCacheNode{{
				CacheNodeId: member.CacheNodeId, CacheNodeStatus: status, CacheNodeCreateTime: c.CacheClusterCreateTime,
				Address: member.Address, Port: member.Port,
			}}
			ecClusters.Put(c.CacheClusterId, c)
		}
	}
	for _, c := range ecClusters.List() {
		if c.ReplicationGroupId == g.ReplicationGroupId && !members[c.CacheClusterId] {
			ecClusters.Delete(c.CacheClusterId)
		}
	}
}

// ecSettleReplGroup records the topology a group's engine settled on, and
// moves the group and its members to available, or to create-failed when the
// engine did not start.
func ecSettleReplGroup(id, incarnation string, engine *ecEngine, err error) {
	var topology *ecTopology
	if err == nil && engine != nil {
		engine.opMu.Lock()
		read, readErr := ecReadTopology(mustReplGroup(id), engine)
		engine.opMu.Unlock()
		if readErr != nil {
			err = readErr
			ecLogEngineFailure(engine.key, readErr)
		} else {
			topology = &read
		}
	}
	var settled ECReplicationGroup
	updated := false
	ecReplGroups.Update(id, func(g *ECReplicationGroup) {
		if g.Incarnation != incarnation {
			return
		}
		status := "available"
		if err != nil {
			if g.Status != "creating" {
				return
			}
			status = "create-failed"
		}
		g.Status = status
		if topology != nil {
			ecDescribeReplGroupTopology(g, *topology, ecEngineEndpoints(engine), status)
		} else {
			for i := range g.NodeGroups {
				g.NodeGroups[i].Status = status
			}
		}
		settled, updated = *g, true
	})
	if updated {
		ecPutMemberClusters(settled, settled.Status)
	}
}

func mustReplGroup(id string) ECReplicationGroup {
	g, _ := ecReplGroups.Get(id)
	return g
}

func handleECDescribeReplGroups(w http.ResponseWriter, r *http.Request) {
	wanted := strings.ToLower(r.FormValue("ReplicationGroupId"))
	var b strings.Builder
	b.WriteString("<ReplicationGroups>")
	matched := false
	for _, g := range ecReplGroups.List() {
		if wanted != "" && g.ReplicationGroupId != wanted {
			continue
		}
		matched = true
		b.WriteString(renderECReplGroup(g))
	}
	if wanted != "" && !matched {
		ecErrorXML(w, "ReplicationGroupNotFoundFault", fmt.Sprintf("Replication group %q not found", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</ReplicationGroups>")
	ecXMLResponse(w, "DescribeReplicationGroups", b.String(), sim.RequestID(r.Context()))
}

func ecInvalidReplGroupState(w http.ResponseWriter, r *http.Request, g ECReplicationGroup) {
	ecErrorXML(w, "InvalidReplicationGroupState",
		fmt.Sprintf("Replication group %s is %s, not available.", g.ReplicationGroupId, g.Status), http.StatusBadRequest, sim.RequestID(r.Context()))
}

func handleECModifyReplGroup(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	id := strings.ToLower(r.FormValue("ReplicationGroupId"))
	current, ok := ecReplGroups.Get(id)
	if !ok {
		ecErrorXML(w, "ReplicationGroupNotFoundFault", "Replication group not found", http.StatusNotFound, requestID)
		return
	}
	token, strategy := r.FormValue("AuthToken"), r.FormValue("AuthTokenUpdateStrategy")
	if token != "" && !ecValidAuthToken(token) {
		ecErrorXML(w, "InvalidParameterValue", "The AUTH token must be 16 to 128 printable ASCII characters.", http.StatusBadRequest, requestID)
		return
	}
	if (token != "" || strategy == "DELETE") && !current.TransitEncryptionEnabled {
		ecErrorXML(w, "InvalidParameterCombination", "AuthToken can be specified only on replication groups where TransitEncryptionEnabled is true.", http.StatusBadRequest, requestID)
		return
	}
	version := ecEngineVersion{Version: current.EngineVersion}
	if v := r.FormValue("EngineVersion"); v != "" {
		resolved, known := ecResolveEngineVersion(current.Engine, v)
		if !known {
			ecErrorXML(w, "InvalidParameterCombination", fmt.Sprintf("Cannot find version %s for %s", v, current.Engine), http.StatusBadRequest, requestID)
			return
		}
		if resolved.Version != current.EngineVersion && current.ClusterEnabled {
			ecErrorXML(w, "InvalidParameterCombination", "The simulator upgrades the engine of a replication group without cluster mode only.", http.StatusBadRequest, requestID)
			return
		}
		version = resolved
	}
	engineChange := token != "" || strategy == "DELETE" || version.Version != current.EngineVersion
	if engineChange && current.Status != "available" {
		ecInvalidReplGroupState(w, r, current)
		return
	}
	ecReplGroups.Update(id, func(g *ECReplicationGroup) {
		if v := r.FormValue("ReplicationGroupDescription"); v != "" {
			g.Description = v
		}
		if v := r.FormValue("CacheNodeType"); v != "" {
			g.CacheNodeType = v
		}
		if v := r.FormValue("SnapshotRetentionLimit"); v != "" {
			g.SnapshotRetentionLimit = atoiOrZero(v)
		}
		if v := r.FormValue("SnapshotWindow"); v != "" {
			g.SnapshotWindow = v
		}
		if v := r.FormValue("AutomaticFailoverEnabled"); v != "" {
			if strings.EqualFold(v, "true") {
				g.AutomaticFailover = "enabled"
			} else {
				g.AutomaticFailover = "disabled"
			}
		}
		if v := r.FormValue("MultiAZEnabled"); v != "" {
			if strings.EqualFold(v, "true") {
				g.MultiAZ = "enabled"
			} else {
				g.MultiAZ = "disabled"
			}
		}
		if engineChange {
			g.Status = "modifying"
		}
	})
	updated, _ := ecReplGroups.Get(id)
	if engineChange {
		engine, _ := ecLoadEngine(ecEngineKey(ecKindReplicationGroup, id))
		ecRunInBackground(engine, func(e *ecEngine) error {
			cluster := ECCluster{Engine: current.Engine, EngineVersion: current.EngineVersion}
			if version.Version == current.EngineVersion {
				version.Image = ""
			}
			return ecModifyClusterEngine(e, cluster, 1, version, token, strategy)
		}, func(err error) {
			ecReplGroups.Update(id, func(g *ECReplicationGroup) {
				if g.Incarnation != current.Incarnation {
					return
				}
				if err == nil {
					g.EngineVersion = version.Version
					switch {
					case strategy == "DELETE":
						g.AuthTokenEnabled = false
					case token != "":
						g.AuthTokenEnabled = true
					}
				}
			})
			ecRefreshReplGroup(id, current.Incarnation, engine)
		})
	}
	ecXMLResponse(w, "ModifyReplicationGroup", renderECReplGroup(updated), requestID)
}

// ecRefreshReplGroup records the topology a group's engine now runs and
// moves the group back to available.
func ecRefreshReplGroup(id, incarnation string, engine *ecEngine) {
	var topology *ecTopology
	if engine != nil {
		engine.opMu.Lock()
		read, err := ecReadTopology(mustReplGroup(id), engine)
		engine.opMu.Unlock()
		if err != nil {
			ecLogEngineFailure(engine.key, err)
		} else {
			topology = &read
		}
	}
	var settled ECReplicationGroup
	updated := false
	ecReplGroups.Update(id, func(g *ECReplicationGroup) {
		if g.Incarnation != incarnation {
			return
		}
		g.Status = "available"
		if topology != nil {
			ecDescribeReplGroupTopology(g, *topology, ecEngineEndpoints(engine), "available")
		} else {
			for i := range g.NodeGroups {
				g.NodeGroups[i].Status = "available"
			}
		}
		settled, updated = *g, true
	})
	if updated {
		ecPutMemberClusters(settled, "available")
	}
}

func handleECDeleteReplGroup(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	id := strings.ToLower(r.FormValue("ReplicationGroupId"))
	g, ok := ecReplGroups.Get(id)
	if !ok {
		ecErrorXML(w, "ReplicationGroupNotFoundFault", "Replication group not found", http.StatusNotFound, requestID)
		return
	}
	if g.Status == "deleting" {
		ecInvalidReplGroupState(w, r, g)
		return
	}
	ecReplGroups.Update(id, func(stored *ECReplicationGroup) {
		stored.Status = "deleting"
		for i := range stored.NodeGroups {
			stored.NodeGroups[i].Status = "deleting"
		}
	})
	g, _ = ecReplGroups.Get(id)
	for _, c := range ecClusters.List() {
		if c.ReplicationGroupId == id {
			ecClusters.Update(c.CacheClusterId, func(stored *ECCluster) { stored.CacheClusterStatus = "deleting" })
		}
	}
	ecDeleteReplGroupInBackground(id, g.Incarnation)
	ecXMLResponse(w, "DeleteReplicationGroup", renderECReplGroup(g), requestID)
}

func ecDeleteReplGroupInBackground(id, incarnation string) {
	bg.Go(func() {
		ecRemoveEngine(ecEngineKey(ecKindReplicationGroup, id))
		g, ok := ecReplGroups.Get(id)
		if !ok || g.Incarnation != incarnation {
			return
		}
		for _, c := range ecClusters.List() {
			if c.ReplicationGroupId == id {
				ecClusters.Delete(c.CacheClusterId)
			}
		}
		ecReplGroups.Delete(id)
	})
}

// ecReshapeReplGroup moves a group to modifying, runs reshape on its engine
// in the background and records the topology the engine settles on.
func ecReshapeReplGroup(w http.ResponseWriter, r *http.Request, op string, check func(ECReplicationGroup) (string, bool), reshape func(ECReplicationGroup, *ecEngine) error) {
	requestID := sim.RequestID(r.Context())
	id := strings.ToLower(r.FormValue("ReplicationGroupId"))
	g, ok := ecReplGroups.Get(id)
	if !ok {
		ecErrorXML(w, "ReplicationGroupNotFoundFault", "Replication group not found", http.StatusNotFound, requestID)
		return
	}
	if g.Status != "available" {
		ecInvalidReplGroupState(w, r, g)
		return
	}
	if message, ok := check(g); !ok {
		ecErrorXML(w, "InvalidParameterValue", message, http.StatusBadRequest, requestID)
		return
	}
	ecReplGroups.Update(id, func(stored *ECReplicationGroup) {
		stored.Status = "modifying"
		for i := range stored.NodeGroups {
			stored.NodeGroups[i].Status = "modifying"
		}
	})
	updated, _ := ecReplGroups.Get(id)
	engine, _ := ecLoadEngine(ecEngineKey(ecKindReplicationGroup, id))
	ecRunInBackground(engine, func(e *ecEngine) error { return reshape(g, e) }, func(error) {
		ecRefreshReplGroup(id, g.Incarnation, engine)
	})
	ecXMLResponse(w, op, renderECReplGroup(updated), requestID)
}

func ecReplicasOf(g ECReplicationGroup) int {
	if len(g.NodeGroups) == 0 {
		return 0
	}
	return len(g.NodeGroups[0].Members) - 1
}

func handleECIncreaseReplicaCount(w http.ResponseWriter, r *http.Request) {
	ecChangeReplicaCount(w, r, "IncreaseReplicaCount", func(current, wanted int) bool { return wanted > current })
}

func handleECDecreaseReplicaCount(w http.ResponseWriter, r *http.Request) {
	ecChangeReplicaCount(w, r, "DecreaseReplicaCount", func(current, wanted int) bool { return wanted < current })
}

func ecChangeReplicaCount(w http.ResponseWriter, r *http.Request, op string, valid func(current, wanted int) bool) {
	wanted := atoiOrZero(r.FormValue("NewReplicaCount"))
	ecReshapeReplGroup(w, r, op, func(g ECReplicationGroup) (string, bool) {
		if !valid(ecReplicasOf(g), wanted) || wanted < 0 || wanted > 5 {
			return fmt.Sprintf("NewReplicaCount %d does not change replication group %s's %d replicas that way.", wanted, g.ReplicationGroupId, ecReplicasOf(g)), false
		}
		if wanted == 0 && g.AutomaticFailover == "enabled" && !g.ClusterEnabled {
			return "A replication group with automatic failover enabled needs at least one replica.", false
		}
		return "", true
	}, func(g ECReplicationGroup, e *ecEngine) error {
		if g.ClusterEnabled {
			return e.redis.ResizeCluster(len(g.NodeGroups), wanted)
		}
		if err := e.redis.ResizeInstance(wanted); err != nil {
			return err
		}
		return e.redis.ExposeNodes(g.Port)
	})
}

func handleECModifyReplGroupShardConfig(w http.ResponseWriter, r *http.Request) {
	shards := atoiOrZero(r.FormValue("NodeGroupCount"))
	ecReshapeReplGroup(w, r, "ModifyReplicationGroupShardConfiguration", func(g ECReplicationGroup) (string, bool) {
		if !g.ClusterEnabled {
			return fmt.Sprintf("Replication group %s does not run cluster mode.", g.ReplicationGroupId), false
		}
		if shards < 1 || shards > 90 {
			return "NodeGroupCount must be between 1 and 90.", false
		}
		return "", true
	}, func(g ECReplicationGroup, e *ecEngine) error {
		return e.redis.ResizeCluster(shards, ecReplicasOf(g))
	})
}

func handleECTestFailover(w http.ResponseWriter, r *http.Request) {
	nodeGroup := r.FormValue("NodeGroupId")
	if nodeGroup == "" {
		ecErrorXML(w, "MissingParameter", "NodeGroupId is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	var replica int
	ecReshapeReplGroup(w, r, "TestFailover", func(g ECReplicationGroup) (string, bool) {
		if g.AutomaticFailover != "enabled" && !g.ClusterEnabled {
			return fmt.Sprintf("Replication group %s does not have automatic failover enabled.", g.ReplicationGroupId), false
		}
		for _, group := range g.NodeGroups {
			if group.NodeGroupId != nodeGroup {
				continue
			}
			if len(group.Members) < 2 {
				return fmt.Sprintf("Node group %s has no replica to fail over to.", nodeGroup), false
			}
			node, ok := ecMemberNode(g, group.Members[1].CacheClusterId)
			replica = node
			return "", ok
		}
		return fmt.Sprintf("Node group %s is not part of replication group %s.", nodeGroup, g.ReplicationGroupId), false
	}, func(g ECReplicationGroup, e *ecEngine) error {
		if g.ClusterEnabled {
			return e.redis.FailoverShard(replica)
		}
		return e.redis.Failover(replica)
	})
}

// ecAddReplicaCluster adds a cache cluster to a replication group without
// cluster mode as a read replica, which CreateCacheCluster does when it names
// the group.
func ecAddReplicaCluster(w http.ResponseWriter, r *http.Request, id, groupID string) {
	requestID := sim.RequestID(r.Context())
	g, ok := ecReplGroups.Get(strings.ToLower(groupID))
	if !ok {
		ecErrorXML(w, "ReplicationGroupNotFoundFault", fmt.Sprintf("Replication group %s not found.", groupID), http.StatusNotFound, requestID)
		return
	}
	if g.ClusterEnabled {
		ecErrorXML(w, "InvalidReplicationGroupState", "A cache cluster joins a replication group without cluster mode only.", http.StatusBadRequest, requestID)
		return
	}
	if g.Status != "available" {
		ecInvalidReplGroupState(w, r, g)
		return
	}
	replicas := ecReplicasOf(g)
	if replicas >= 5 {
		ecErrorXML(w, "NodeGroupsPerReplicationGroupQuotaExceeded", "A replication group runs at most 5 replicas.", http.StatusBadRequest, requestID)
		return
	}
	engine, _ := ecLoadEngine(ecEngineKey(ecKindReplicationGroup, g.ReplicationGroupId))
	ecReplGroups.Update(g.ReplicationGroupId, func(stored *ECReplicationGroup) { stored.Status = "modifying" })
	c := ECCluster{
		CacheClusterId: id, CacheNodeType: g.CacheNodeType, Engine: g.Engine, EngineVersion: g.EngineVersion,
		CacheClusterStatus: "creating", NumCacheNodes: 1, CacheClusterCreateTime: ecNow(), ARN: ecClusterARN(id),
		Port: g.Port, ReplicationGroupId: g.ReplicationGroupId, Tags: parseAWSQueryTagMap(r, "Tags.Tag"),
		AuthTokenEnabled: g.AuthTokenEnabled, TransitEncryptionEnabled: g.TransitEncryptionEnabled,
		TransitEncryptionMode: g.TransitEncryptionMode, Incarnation: g.Incarnation,
	}
	c.CacheNodes = []ECCacheNode{{CacheNodeId: ecCacheNodeID(0), CacheNodeStatus: "creating", CacheNodeCreateTime: c.CacheClusterCreateTime}}
	ecClusters.Put(id, c)
	ecRunInBackground(engine, func(e *ecEngine) error {
		before := map[int]bool{}
		for _, node := range e.redis.Nodes() {
			before[node] = true
		}
		if err := e.redis.ResizeInstance(replicas + 1); err != nil {
			return err
		}
		for _, node := range e.redis.Nodes() {
			if !before[node] {
				ecReplGroups.Update(g.ReplicationGroupId, func(stored *ECReplicationGroup) { stored.MemberNodes[node] = id })
			}
		}
		return e.redis.ExposeNodes(g.Port)
	}, func(err error) {
		if err != nil {
			ecClusters.Update(id, func(stored *ECCluster) { stored.CacheClusterStatus = "create-failed" })
		}
		ecRefreshReplGroup(g.ReplicationGroupId, g.Incarnation, engine)
	})
	ecXMLResponse(w, "CreateCacheCluster", renderECCluster(c, false), requestID)
}

// ecRecoverTransitions resumes what a create, modification, reboot or delete
// left unfinished when the previous control-plane process stopped: an engine
// re-adopted at start answers again, so a resource it serves settles, and a
// resource being deleted finishes going.
func ecRecoverTransitions() {
	for _, c := range ecClusters.List() {
		if c.ReplicationGroupId != "" {
			continue
		}
		id, incarnation := c.CacheClusterId, c.Incarnation
		engine, _ := ecLoadEngine(ecEngineKey(ecKindCluster, id))
		switch c.CacheClusterStatus {
		case "deleting":
			ecDeleteClusterInBackground(id, incarnation)
		case "creating":
			ecRunInBackground(engine, (*ecEngine).start, func(err error) { ecSettleCluster(id, incarnation, err) })
		case "modifying", "rebooting cluster nodes":
			ecRunInBackground(engine, (*ecEngine).start, func(error) { ecSetClusterAvailable(id, incarnation) })
		}
	}
	for _, g := range ecReplGroups.List() {
		id, incarnation := g.ReplicationGroupId, g.Incarnation
		engine, _ := ecLoadEngine(ecEngineKey(ecKindReplicationGroup, id))
		switch g.Status {
		case "deleting":
			ecDeleteReplGroupInBackground(id, incarnation)
		case "creating":
			ecRunInBackground(engine, (*ecEngine).start, func(err error) { ecSettleReplGroup(id, incarnation, engine, err) })
		case "modifying", "rebooting cluster nodes":
			ecRunInBackground(engine, (*ecEngine).start, func(error) { ecRefreshReplGroup(id, incarnation, engine) })
		}
	}
	ecRecoverServerlessTransitions()
}
