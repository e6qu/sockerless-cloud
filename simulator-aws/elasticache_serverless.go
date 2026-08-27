package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// ElastiCache serverless caches, serverless cache snapshots, global
// replication groups, cache security groups, update actions, replica /
// shard reconfiguration, online migration, and reserved-node purchase.
// awsQuery protocol; XML responses match the real ElastiCache shapes the
// AWS SDK for Go v2 and the aws CLI parse. Lifecycle states settle
// synchronously (Create returns the resource already "available").

// ECServerlessCache models the ServerlessCache shape.
type ECServerlessCache struct {
	ServerlessCacheName    string
	Description            string
	CreateTime             string
	Status                 string
	Engine                 string
	MajorEngineVersion     string
	FullEngineVersion      string
	UsageDataStorageMax    int
	UsageDataStorageMin    int
	UsageDataStorageUnit   string
	UsageECPUMax           int
	UsageECPUMin           int
	KmsKeyId               string
	StorageEncryptionType  string
	SecurityGroupIds       []string
	EndpointAddress        string
	EndpointPort           int
	ReaderEndpointAddress  string
	ReaderEndpointPort     int
	ARN                    string
	UserGroupId            string
	SubnetIds              []string
	SnapshotRetentionLimit int
	DailySnapshotTime      string
	NetworkType            string
	Tags                   map[string]string
}

// ECServerlessCacheSnapshot models the ServerlessCacheSnapshot shape.
type ECServerlessCacheSnapshot struct {
	ServerlessCacheSnapshotName string
	ARN                         string
	KmsKeyId                    string
	SnapshotType                string
	Status                      string
	CreateTime                  string
	ExpiryTime                  string
	BytesUsedForCache           string
	// ServerlessCacheConfiguration snapshot of the source cache.
	CacheName               string
	CacheEngine             string
	CacheMajorEngineVersion string
	Tags                    map[string]string
}

// ECGlobalNodeGroup models the GlobalNodeGroup shape.
type ECGlobalNodeGroup struct {
	GlobalNodeGroupId string
	Slots             string
}

// ECGlobalReplicationGroupMember models a member region of a Global
// Datastore.
type ECGlobalReplicationGroupMember struct {
	ReplicationGroupId     string
	ReplicationGroupRegion string
	Role                   string
	AutomaticFailover      string
	Status                 string
}

// ECGlobalReplicationGroup models the GlobalReplicationGroup shape (a
// Global Datastore: a primary replication group plus secondary
// regions).
type ECGlobalReplicationGroup struct {
	GlobalReplicationGroupId          string
	GlobalReplicationGroupDescription string
	Status                            string
	CacheNodeType                     string
	Engine                            string
	EngineVersion                     string
	ClusterEnabled                    bool
	AuthTokenEnabled                  bool
	TransitEncryptionEnabled          bool
	AtRestEncryptionEnabled           bool
	ARN                               string
	Members                           []ECGlobalReplicationGroupMember
	GlobalNodeGroups                  []ECGlobalNodeGroup
}

// ECCacheSecurityGroupRule models one authorized EC2 security group
// ingress rule on a CacheSecurityGroup.
type ECCacheSecurityGroupRule struct {
	EC2SecurityGroupName    string
	EC2SecurityGroupOwnerId string
	Status                  string
}

// ECCacheSecurityGroup models the EC2-Classic-style CacheSecurityGroup
// shape with its authorized ingress rules.
type ECCacheSecurityGroup struct {
	CacheSecurityGroupName string
	Description            string
	OwnerId                string
	ARN                    string
	EC2SecurityGroups      []ECCacheSecurityGroupRule
}

var (
	ecServerlessCaches    sim.Store[ECServerlessCache]
	ecServerlessSnapshots sim.Store[ECServerlessCacheSnapshot]
	ecGlobalReplGroups    sim.Store[ECGlobalReplicationGroup]
	ecCacheSecGroups      sim.Store[ECCacheSecurityGroup]
)

func registerElastiCacheServerless(r *sim.AWSQueryRouter, srv *sim.Server) {
	ecServerlessCaches = sim.MakeStore[ECServerlessCache](srv.DB(), "elasticache_serverless_caches")
	ecServerlessSnapshots = sim.MakeStore[ECServerlessCacheSnapshot](srv.DB(), "elasticache_serverless_snapshots")
	ecGlobalReplGroups = sim.MakeStore[ECGlobalReplicationGroup](srv.DB(), "elasticache_global_replication_groups")
	ecCacheSecGroups = sim.MakeStore[ECCacheSecurityGroup](srv.DB(), "elasticache_cache_security_groups")

	// Serverless caches.
	r.RegisterVersioned(ecAPIVersion, "CreateServerlessCache", handleECCreateServerlessCache)
	r.RegisterVersioned(ecAPIVersion, "DescribeServerlessCaches", handleECDescribeServerlessCaches)
	r.RegisterVersioned(ecAPIVersion, "ModifyServerlessCache", handleECModifyServerlessCache)
	r.RegisterVersioned(ecAPIVersion, "DeleteServerlessCache", handleECDeleteServerlessCache)

	// Serverless cache snapshots.
	r.RegisterVersioned(ecAPIVersion, "CreateServerlessCacheSnapshot", handleECCreateServerlessSnapshot)
	r.RegisterVersioned(ecAPIVersion, "DescribeServerlessCacheSnapshots", handleECDescribeServerlessSnapshots)
	r.RegisterVersioned(ecAPIVersion, "DeleteServerlessCacheSnapshot", handleECDeleteServerlessSnapshot)
	r.RegisterVersioned(ecAPIVersion, "CopyServerlessCacheSnapshot", handleECCopyServerlessSnapshot)
	r.RegisterVersioned(ecAPIVersion, "ExportServerlessCacheSnapshot", handleECExportServerlessSnapshot)

	// Global replication groups (Global Datastore).
	r.RegisterVersioned(ecAPIVersion, "CreateGlobalReplicationGroup", handleECCreateGlobalReplGroup)
	r.RegisterVersioned(ecAPIVersion, "DescribeGlobalReplicationGroups", handleECDescribeGlobalReplGroups)
	r.RegisterVersioned(ecAPIVersion, "ModifyGlobalReplicationGroup", handleECModifyGlobalReplGroup)
	r.RegisterVersioned(ecAPIVersion, "DeleteGlobalReplicationGroup", handleECDeleteGlobalReplGroup)
	r.RegisterVersioned(ecAPIVersion, "DisassociateGlobalReplicationGroup", handleECDisassociateGlobalReplGroup)
	r.RegisterVersioned(ecAPIVersion, "FailoverGlobalReplicationGroup", handleECFailoverGlobalReplGroup)
	r.RegisterVersioned(ecAPIVersion, "IncreaseNodeGroupsInGlobalReplicationGroup", handleECIncreaseNodeGroupsGlobal)
	r.RegisterVersioned(ecAPIVersion, "DecreaseNodeGroupsInGlobalReplicationGroup", handleECDecreaseNodeGroupsGlobal)
	r.RegisterVersioned(ecAPIVersion, "RebalanceSlotsInGlobalReplicationGroup", handleECRebalanceSlotsGlobal)

	// Cache security groups (EC2-Classic ingress rules).
	r.RegisterVersioned(ecAPIVersion, "CreateCacheSecurityGroup", handleECCreateCacheSecGroup)
	r.RegisterVersioned(ecAPIVersion, "DeleteCacheSecurityGroup", handleECDeleteCacheSecGroup)
	r.RegisterVersioned(ecAPIVersion, "AuthorizeCacheSecurityGroupIngress", handleECAuthorizeCacheSecGroupIngress)
	r.RegisterVersioned(ecAPIVersion, "RevokeCacheSecurityGroupIngress", handleECRevokeCacheSecGroupIngress)

	// Service-update actions on existing clusters / replication groups.
	r.RegisterVersioned(ecAPIVersion, "DescribeUpdateActions", handleECDescribeUpdateActions)
	r.RegisterVersioned(ecAPIVersion, "BatchApplyUpdateAction", handleECBatchApplyUpdateAction)
	r.RegisterVersioned(ecAPIVersion, "BatchStopUpdateAction", handleECBatchStopUpdateAction)

	// Replica / shard reconfiguration on the existing replication-group store.
	r.RegisterVersioned(ecAPIVersion, "IncreaseReplicaCount", handleECIncreaseReplicaCount)
	r.RegisterVersioned(ecAPIVersion, "DecreaseReplicaCount", handleECDecreaseReplicaCount)
	r.RegisterVersioned(ecAPIVersion, "ModifyReplicationGroupShardConfiguration", handleECModifyReplGroupShardConfig)
	r.RegisterVersioned(ecAPIVersion, "TestFailover", handleECTestFailover)

	// Node-type modifications + reserved-node purchase + online migration.
	r.RegisterVersioned(ecAPIVersion, "ListAllowedNodeTypeModifications", handleECListAllowedNodeTypeModifications)
	r.RegisterVersioned(ecAPIVersion, "PurchaseReservedCacheNodesOffering", handleECPurchaseReservedCacheNodesOffering)
	r.RegisterVersioned(ecAPIVersion, "StartMigration", handleECStartMigration)
	r.RegisterVersioned(ecAPIVersion, "CompleteMigration", handleECCompleteMigration)
	r.RegisterVersioned(ecAPIVersion, "TestMigration", handleECTestMigration)
}

func ecServerlessCacheARN(name string) string {
	return fmt.Sprintf("arn:aws:elasticache:%s:%s:serverlesscache:%s", awsRegion(), awsAccountID(), name)
}

func ecServerlessSnapshotARN(name string) string {
	return fmt.Sprintf("arn:aws:elasticache:%s:%s:serverlesscachesnapshot:%s", awsRegion(), awsAccountID(), name)
}

func ecGlobalReplGroupARN(id string) string {
	return fmt.Sprintf("arn:aws:elasticache:%s:%s:globalreplicationgroup:%s", awsRegion(), awsAccountID(), id)
}

func ecCacheSecGroupARN(name string) string {
	return fmt.Sprintf("arn:aws:elasticache:%s:%s:securitygroup:%s", awsRegion(), awsAccountID(), name)
}

// ecParseStringList collects a flattened awsQuery list of strings under
// the given prefix, trying the member's xmlName entry name first and the
// generic ".member.N" form second.
func ecParseStringList(r *http.Request, prefix, entry string) []string {
	var out []string
	for n := 1; n <= 100; n++ {
		v := r.FormValue(fmt.Sprintf("%s.%s.%d", prefix, entry, n))
		if v == "" {
			v = r.FormValue(fmt.Sprintf("%s.member.%d", prefix, n))
		}
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}

// Serverless caches

// renderECServerlessCacheBody emits the ServerlessCache shape's members
// without any wrapper element. CreateServerlessCache / Modify / Delete
// wrap it in <ServerlessCache>; DescribeServerlessCaches wraps each entry
// in the list's <member> element.
func renderECServerlessCacheBody(c ECServerlessCache) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<ServerlessCacheName>%s</ServerlessCacheName>", xmlEscape(c.ServerlessCacheName))
	if c.Description != "" {
		fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(c.Description))
	}
	fmt.Fprintf(&b, "<CreateTime>%s</CreateTime>", xmlEscape(c.CreateTime))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(c.Status))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(c.Engine))
	fmt.Fprintf(&b, "<MajorEngineVersion>%s</MajorEngineVersion>", xmlEscape(c.MajorEngineVersion))
	fmt.Fprintf(&b, "<FullEngineVersion>%s</FullEngineVersion>", xmlEscape(c.FullEngineVersion))
	b.WriteString("<CacheUsageLimits>")
	b.WriteString("<DataStorage>")
	fmt.Fprintf(&b, "<Maximum>%d</Maximum>", c.UsageDataStorageMax)
	if c.UsageDataStorageMin > 0 {
		fmt.Fprintf(&b, "<Minimum>%d</Minimum>", c.UsageDataStorageMin)
	}
	fmt.Fprintf(&b, "<Unit>%s</Unit>", xmlEscape(c.UsageDataStorageUnit))
	b.WriteString("</DataStorage>")
	b.WriteString("<ECPUPerSecond>")
	fmt.Fprintf(&b, "<Maximum>%d</Maximum>", c.UsageECPUMax)
	if c.UsageECPUMin > 0 {
		fmt.Fprintf(&b, "<Minimum>%d</Minimum>", c.UsageECPUMin)
	}
	b.WriteString("</ECPUPerSecond>")
	b.WriteString("</CacheUsageLimits>")
	if c.KmsKeyId != "" {
		fmt.Fprintf(&b, "<KmsKeyId>%s</KmsKeyId>", xmlEscape(c.KmsKeyId))
	}
	fmt.Fprintf(&b, "<StorageEncryptionType>%s</StorageEncryptionType>", xmlEscape(c.StorageEncryptionType))
	b.WriteString("<SecurityGroupIds>")
	for _, s := range c.SecurityGroupIds {
		fmt.Fprintf(&b, "<SecurityGroupId>%s</SecurityGroupId>", xmlEscape(s))
	}
	b.WriteString("</SecurityGroupIds>")
	fmt.Fprintf(&b, "<Endpoint><Address>%s</Address><Port>%d</Port></Endpoint>", xmlEscape(c.EndpointAddress), c.EndpointPort)
	fmt.Fprintf(&b, "<ReaderEndpoint><Address>%s</Address><Port>%d</Port></ReaderEndpoint>", xmlEscape(c.ReaderEndpointAddress), c.ReaderEndpointPort)
	fmt.Fprintf(&b, "<ARN>%s</ARN>", xmlEscape(c.ARN))
	if c.UserGroupId != "" {
		fmt.Fprintf(&b, "<UserGroupId>%s</UserGroupId>", xmlEscape(c.UserGroupId))
	}
	b.WriteString("<SubnetIds>")
	for _, s := range c.SubnetIds {
		fmt.Fprintf(&b, "<SubnetId>%s</SubnetId>", xmlEscape(s))
	}
	b.WriteString("</SubnetIds>")
	fmt.Fprintf(&b, "<SnapshotRetentionLimit>%d</SnapshotRetentionLimit>", c.SnapshotRetentionLimit)
	if c.DailySnapshotTime != "" {
		fmt.Fprintf(&b, "<DailySnapshotTime>%s</DailySnapshotTime>", xmlEscape(c.DailySnapshotTime))
	}
	fmt.Fprintf(&b, "<NetworkType>%s</NetworkType>", xmlEscape(c.NetworkType))
	return b.String()
}

func renderECServerlessCache(c ECServerlessCache) string {
	return "<ServerlessCache>" + renderECServerlessCacheBody(c) + "</ServerlessCache>"
}

func ecParseUsageLimits(r *http.Request, c *ECServerlessCache) {
	if v := atoiOrZero(r.FormValue("CacheUsageLimits.DataStorage.Maximum")); v > 0 {
		c.UsageDataStorageMax = v
	}
	if v := atoiOrZero(r.FormValue("CacheUsageLimits.DataStorage.Minimum")); v > 0 {
		c.UsageDataStorageMin = v
	}
	if v := r.FormValue("CacheUsageLimits.DataStorage.Unit"); v != "" {
		c.UsageDataStorageUnit = v
	}
	if v := atoiOrZero(r.FormValue("CacheUsageLimits.ECPUPerSecond.Maximum")); v > 0 {
		c.UsageECPUMax = v
	}
	if v := atoiOrZero(r.FormValue("CacheUsageLimits.ECPUPerSecond.Minimum")); v > 0 {
		c.UsageECPUMin = v
	}
}

func handleECCreateServerlessCache(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("ServerlessCacheName")
	if name == "" {
		ecErrorXML(w, "MissingParameter", "ServerlessCacheName is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := ecServerlessCaches.Get(name); ok {
		ecErrorXML(w, "ServerlessCacheAlreadyExistsFault", "Serverless cache already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	engine := r.FormValue("Engine")
	if engine == "" {
		engine = "redis"
	}
	major := r.FormValue("MajorEngineVersion")
	if major == "" {
		major = "7"
	}
	storageType := "sse-elasticache"
	if r.FormValue("KmsKeyId") != "" {
		storageType = "sse-kms"
	}
	netType := r.FormValue("NetworkType")
	if netType == "" {
		netType = "ipv4"
	}
	c := ECServerlessCache{
		ServerlessCacheName:    name,
		Description:            r.FormValue("Description"),
		CreateTime:             time.Now().UTC().Format(time.RFC3339),
		Status:                 "available",
		Engine:                 engine,
		MajorEngineVersion:     major,
		FullEngineVersion:      ecDefaultEngineVersion(engine),
		UsageDataStorageMax:    5000,
		UsageDataStorageUnit:   "GB",
		UsageECPUMax:           15000,
		KmsKeyId:               r.FormValue("KmsKeyId"),
		StorageEncryptionType:  storageType,
		SecurityGroupIds:       ecParseStringList(r, "SecurityGroupIds", "SecurityGroupId"),
		EndpointAddress:        fmt.Sprintf("%s.serverless.%s.cache.amazonaws.com", name, awsRegion()),
		EndpointPort:           6379,
		ReaderEndpointAddress:  fmt.Sprintf("%s-ro.serverless.%s.cache.amazonaws.com", name, awsRegion()),
		ReaderEndpointPort:     6380,
		ARN:                    ecServerlessCacheARN(name),
		UserGroupId:            r.FormValue("UserGroupId"),
		SubnetIds:              ecParseStringList(r, "SubnetIds", "SubnetId"),
		SnapshotRetentionLimit: atoiOrZero(r.FormValue("SnapshotRetentionLimit")),
		DailySnapshotTime:      r.FormValue("DailySnapshotTime"),
		NetworkType:            netType,
		Tags:                   parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	if c.FullEngineVersion == "" {
		c.FullEngineVersion = major + ".0"
	}
	ecParseUsageLimits(r, &c)
	if c.UsageDataStorageUnit == "" {
		c.UsageDataStorageUnit = "GB"
	}
	ecServerlessCaches.Put(name, c)
	ecXMLResponse(w, "CreateServerlessCache", renderECServerlessCache(c), sim.RequestID(r.Context()))
}

func handleECDescribeServerlessCaches(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("ServerlessCacheName")
	var b strings.Builder
	b.WriteString("<ServerlessCaches>")
	matched := false
	for _, c := range ecServerlessCaches.List() {
		if wanted != "" && c.ServerlessCacheName != wanted {
			continue
		}
		matched = true
		b.WriteString("<member>")
		b.WriteString(renderECServerlessCacheBody(c))
		b.WriteString("</member>")
	}
	if wanted != "" && !matched {
		ecErrorXML(w, "ServerlessCacheNotFoundFault", fmt.Sprintf("Serverless cache %q not found", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</ServerlessCaches>")
	ecXMLResponse(w, "DescribeServerlessCaches", b.String(), sim.RequestID(r.Context()))
}

func handleECModifyServerlessCache(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("ServerlessCacheName")
	if _, ok := ecServerlessCaches.Get(name); !ok {
		ecErrorXML(w, "ServerlessCacheNotFoundFault", "Serverless cache not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ecServerlessCaches.Update(name, func(c *ECServerlessCache) {
		if v := r.FormValue("Description"); v != "" {
			c.Description = v
		}
		if v := r.FormValue("UserGroupId"); v != "" {
			c.UserGroupId = v
		}
		if v := r.FormValue("SnapshotRetentionLimit"); v != "" {
			c.SnapshotRetentionLimit = atoiOrZero(v)
		}
		if v := r.FormValue("DailySnapshotTime"); v != "" {
			c.DailySnapshotTime = v
		}
		if ids := ecParseStringList(r, "SecurityGroupIds", "SecurityGroupId"); len(ids) > 0 {
			c.SecurityGroupIds = ids
		}
		ecParseUsageLimits(r, c)
	})
	updated, _ := ecServerlessCaches.Get(name)
	ecXMLResponse(w, "ModifyServerlessCache", renderECServerlessCache(updated), sim.RequestID(r.Context()))
}

func handleECDeleteServerlessCache(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("ServerlessCacheName")
	c, ok := ecServerlessCaches.Get(name)
	if !ok {
		ecErrorXML(w, "ServerlessCacheNotFoundFault", "Serverless cache not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	c.Status = "deleting"
	ecServerlessCaches.Delete(name)
	ecXMLResponse(w, "DeleteServerlessCache", renderECServerlessCache(c), sim.RequestID(r.Context()))
}

// Serverless cache snapshots

func renderECServerlessSnapshot(s ECServerlessCacheSnapshot) string {
	var b strings.Builder
	b.WriteString("<ServerlessCacheSnapshot>")
	fmt.Fprintf(&b, "<ServerlessCacheSnapshotName>%s</ServerlessCacheSnapshotName>", xmlEscape(s.ServerlessCacheSnapshotName))
	fmt.Fprintf(&b, "<ARN>%s</ARN>", xmlEscape(s.ARN))
	if s.KmsKeyId != "" {
		fmt.Fprintf(&b, "<KmsKeyId>%s</KmsKeyId>", xmlEscape(s.KmsKeyId))
	}
	fmt.Fprintf(&b, "<SnapshotType>%s</SnapshotType>", xmlEscape(s.SnapshotType))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(s.Status))
	fmt.Fprintf(&b, "<CreateTime>%s</CreateTime>", xmlEscape(s.CreateTime))
	if s.ExpiryTime != "" {
		fmt.Fprintf(&b, "<ExpiryTime>%s</ExpiryTime>", xmlEscape(s.ExpiryTime))
	}
	fmt.Fprintf(&b, "<BytesUsedForCache>%s</BytesUsedForCache>", xmlEscape(s.BytesUsedForCache))
	b.WriteString("<ServerlessCacheConfiguration>")
	fmt.Fprintf(&b, "<ServerlessCacheName>%s</ServerlessCacheName>", xmlEscape(s.CacheName))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(s.CacheEngine))
	fmt.Fprintf(&b, "<MajorEngineVersion>%s</MajorEngineVersion>", xmlEscape(s.CacheMajorEngineVersion))
	b.WriteString("</ServerlessCacheConfiguration>")
	b.WriteString("</ServerlessCacheSnapshot>")
	return b.String()
}

func handleECCreateServerlessSnapshot(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("ServerlessCacheSnapshotName")
	if name == "" {
		ecErrorXML(w, "MissingParameter", "ServerlessCacheSnapshotName is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := ecServerlessSnapshots.Get(name); ok {
		ecErrorXML(w, "ServerlessCacheSnapshotAlreadyExistsFault", "Serverless cache snapshot already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	cacheName := r.FormValue("ServerlessCacheName")
	c, ok := ecServerlessCaches.Get(cacheName)
	if !ok {
		ecErrorXML(w, "ServerlessCacheNotFoundFault", fmt.Sprintf("Serverless cache %q not found", cacheName), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	now := time.Now().UTC()
	s := ECServerlessCacheSnapshot{
		ServerlessCacheSnapshotName: name,
		ARN:                         ecServerlessSnapshotARN(name),
		KmsKeyId:                    r.FormValue("KmsKeyId"),
		SnapshotType:                "manual",
		Status:                      "available",
		CreateTime:                  now.Format(time.RFC3339),
		BytesUsedForCache:           "0",
		CacheName:                   c.ServerlessCacheName,
		CacheEngine:                 c.Engine,
		CacheMajorEngineVersion:     c.MajorEngineVersion,
		Tags:                        parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	ecServerlessSnapshots.Put(name, s)
	ecXMLResponse(w, "CreateServerlessCacheSnapshot", renderECServerlessSnapshot(s), sim.RequestID(r.Context()))
}

func handleECDescribeServerlessSnapshots(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("ServerlessCacheSnapshotName")
	wantCache := r.FormValue("ServerlessCacheName")
	var b strings.Builder
	b.WriteString("<ServerlessCacheSnapshots>")
	matched := false
	for _, s := range ecServerlessSnapshots.List() {
		if wanted != "" && s.ServerlessCacheSnapshotName != wanted {
			continue
		}
		if wantCache != "" && s.CacheName != wantCache {
			continue
		}
		matched = true
		b.WriteString(renderECServerlessSnapshot(s))
	}
	if wanted != "" && !matched {
		ecErrorXML(w, "ServerlessCacheSnapshotNotFoundFault", fmt.Sprintf("Serverless cache snapshot %q not found", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</ServerlessCacheSnapshots>")
	ecXMLResponse(w, "DescribeServerlessCacheSnapshots", b.String(), sim.RequestID(r.Context()))
}

func handleECDeleteServerlessSnapshot(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("ServerlessCacheSnapshotName")
	s, ok := ecServerlessSnapshots.Get(name)
	if !ok {
		ecErrorXML(w, "ServerlessCacheSnapshotNotFoundFault", "Serverless cache snapshot not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	s.Status = "deleting"
	ecServerlessSnapshots.Delete(name)
	ecXMLResponse(w, "DeleteServerlessCacheSnapshot", renderECServerlessSnapshot(s), sim.RequestID(r.Context()))
}

func handleECCopyServerlessSnapshot(w http.ResponseWriter, r *http.Request) {
	src := r.FormValue("SourceServerlessCacheSnapshotName")
	target := r.FormValue("TargetServerlessCacheSnapshotName")
	if target == "" {
		ecErrorXML(w, "MissingParameter", "TargetServerlessCacheSnapshotName is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	s, ok := ecServerlessSnapshots.Get(src)
	if !ok {
		ecErrorXML(w, "ServerlessCacheSnapshotNotFoundFault", fmt.Sprintf("Serverless cache snapshot %q not found", src), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if _, ok := ecServerlessSnapshots.Get(target); ok {
		ecErrorXML(w, "ServerlessCacheSnapshotAlreadyExistsFault", "Serverless cache snapshot already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	s.ServerlessCacheSnapshotName = target
	s.ARN = ecServerlessSnapshotARN(target)
	s.Status = "available"
	s.CreateTime = time.Now().UTC().Format(time.RFC3339)
	if v := r.FormValue("KmsKeyId"); v != "" {
		s.KmsKeyId = v
	}
	s.Tags = parseAWSQueryTagMap(r, "Tags.Tag")
	ecServerlessSnapshots.Put(target, s)
	ecXMLResponse(w, "CopyServerlessCacheSnapshot", renderECServerlessSnapshot(s), sim.RequestID(r.Context()))
}

func handleECExportServerlessSnapshot(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("ServerlessCacheSnapshotName")
	s, ok := ecServerlessSnapshots.Get(name)
	if !ok {
		ecErrorXML(w, "ServerlessCacheSnapshotNotFoundFault", fmt.Sprintf("Serverless cache snapshot %q not found", name), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if r.FormValue("S3BucketName") == "" {
		ecErrorXML(w, "MissingParameter", "S3BucketName is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	ecXMLResponse(w, "ExportServerlessCacheSnapshot", renderECServerlessSnapshot(s), sim.RequestID(r.Context()))
}

// Global replication groups (Global Datastore)

func renderECGlobalReplGroup(g ECGlobalReplicationGroup) string {
	var b strings.Builder
	b.WriteString("<GlobalReplicationGroup>")
	fmt.Fprintf(&b, "<GlobalReplicationGroupId>%s</GlobalReplicationGroupId>", xmlEscape(g.GlobalReplicationGroupId))
	fmt.Fprintf(&b, "<GlobalReplicationGroupDescription>%s</GlobalReplicationGroupDescription>", xmlEscape(g.GlobalReplicationGroupDescription))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(g.Status))
	fmt.Fprintf(&b, "<CacheNodeType>%s</CacheNodeType>", xmlEscape(g.CacheNodeType))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(g.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(g.EngineVersion))
	fmt.Fprintf(&b, "<ClusterEnabled>%t</ClusterEnabled>", g.ClusterEnabled)
	b.WriteString("<Members>")
	for _, m := range g.Members {
		b.WriteString("<GlobalReplicationGroupMember>")
		fmt.Fprintf(&b, "<ReplicationGroupId>%s</ReplicationGroupId>", xmlEscape(m.ReplicationGroupId))
		fmt.Fprintf(&b, "<ReplicationGroupRegion>%s</ReplicationGroupRegion>", xmlEscape(m.ReplicationGroupRegion))
		fmt.Fprintf(&b, "<Role>%s</Role>", xmlEscape(m.Role))
		fmt.Fprintf(&b, "<AutomaticFailover>%s</AutomaticFailover>", xmlEscape(m.AutomaticFailover))
		fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(m.Status))
		b.WriteString("</GlobalReplicationGroupMember>")
	}
	b.WriteString("</Members>")
	b.WriteString("<GlobalNodeGroups>")
	for _, ng := range g.GlobalNodeGroups {
		b.WriteString("<GlobalNodeGroup>")
		fmt.Fprintf(&b, "<GlobalNodeGroupId>%s</GlobalNodeGroupId>", xmlEscape(ng.GlobalNodeGroupId))
		fmt.Fprintf(&b, "<Slots>%s</Slots>", xmlEscape(ng.Slots))
		b.WriteString("</GlobalNodeGroup>")
	}
	b.WriteString("</GlobalNodeGroups>")
	fmt.Fprintf(&b, "<AuthTokenEnabled>%t</AuthTokenEnabled>", g.AuthTokenEnabled)
	fmt.Fprintf(&b, "<TransitEncryptionEnabled>%t</TransitEncryptionEnabled>", g.TransitEncryptionEnabled)
	fmt.Fprintf(&b, "<AtRestEncryptionEnabled>%t</AtRestEncryptionEnabled>", g.AtRestEncryptionEnabled)
	fmt.Fprintf(&b, "<ARN>%s</ARN>", xmlEscape(g.ARN))
	b.WriteString("</GlobalReplicationGroup>")
	return b.String()
}

func handleECCreateGlobalReplGroup(w http.ResponseWriter, r *http.Request) {
	suffix := r.FormValue("GlobalReplicationGroupIdSuffix")
	if suffix == "" {
		ecErrorXML(w, "MissingParameter", "GlobalReplicationGroupIdSuffix is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	primary := r.FormValue("PrimaryReplicationGroupId")
	src, ok := ecReplGroups.Get(primary)
	if !ok {
		ecErrorXML(w, "ReplicationGroupNotFoundFault", fmt.Sprintf("Replication group %q not found", primary), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	// AWS prefixes a random tag to keep the global id unique across the
	// global namespace; the sim uses a stable deterministic prefix.
	id := "ldgnf-" + suffix
	if _, ok := ecGlobalReplGroups.Get(id); ok {
		ecErrorXML(w, "GlobalReplicationGroupAlreadyExistsFault", "Global replication group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	g := ECGlobalReplicationGroup{
		GlobalReplicationGroupId:          id,
		GlobalReplicationGroupDescription: r.FormValue("GlobalReplicationGroupDescription"),
		Status:                            "available",
		CacheNodeType:                     src.CacheNodeType,
		Engine:                            src.Engine,
		EngineVersion:                     ecDefaultEngineVersion(src.Engine),
		ClusterEnabled:                    src.ClusterEnabled,
		ARN:                               ecGlobalReplGroupARN(id),
		Members: []ECGlobalReplicationGroupMember{{
			ReplicationGroupId:     primary,
			ReplicationGroupRegion: awsRegion(),
			Role:                   "PRIMARY",
			AutomaticFailover:      src.AutomaticFailover,
			Status:                 "associated",
		}},
		GlobalNodeGroups: []ECGlobalNodeGroup{{
			GlobalNodeGroupId: id + "-0001",
			Slots:             "0-16383",
		}},
	}
	ecGlobalReplGroups.Put(id, g)
	ecXMLResponse(w, "CreateGlobalReplicationGroup", renderECGlobalReplGroup(g), sim.RequestID(r.Context()))
}

func handleECDescribeGlobalReplGroups(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("GlobalReplicationGroupId")
	var b strings.Builder
	b.WriteString("<GlobalReplicationGroups>")
	matched := false
	for _, g := range ecGlobalReplGroups.List() {
		if wanted != "" && g.GlobalReplicationGroupId != wanted {
			continue
		}
		matched = true
		b.WriteString(renderECGlobalReplGroup(g))
	}
	if wanted != "" && !matched {
		ecErrorXML(w, "GlobalReplicationGroupNotFoundFault", fmt.Sprintf("Global replication group %q not found", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</GlobalReplicationGroups>")
	ecXMLResponse(w, "DescribeGlobalReplicationGroups", b.String(), sim.RequestID(r.Context()))
}

// ecGlobalReplGroupMutate looks up a global replication group, applies fn,
// and writes back. It returns the updated group and whether it existed.
func ecGlobalReplGroupMutate(w http.ResponseWriter, r *http.Request, op string, fn func(*ECGlobalReplicationGroup)) {
	id := r.FormValue("GlobalReplicationGroupId")
	if _, ok := ecGlobalReplGroups.Get(id); !ok {
		ecErrorXML(w, "GlobalReplicationGroupNotFoundFault", "Global replication group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ecGlobalReplGroups.Update(id, fn)
	updated, _ := ecGlobalReplGroups.Get(id)
	ecXMLResponse(w, op, renderECGlobalReplGroup(updated), sim.RequestID(r.Context()))
}

func handleECModifyGlobalReplGroup(w http.ResponseWriter, r *http.Request) {
	ecGlobalReplGroupMutate(w, r, "ModifyGlobalReplicationGroup", func(g *ECGlobalReplicationGroup) {
		if v := r.FormValue("GlobalReplicationGroupDescription"); v != "" {
			g.GlobalReplicationGroupDescription = v
		}
		if v := r.FormValue("CacheNodeType"); v != "" {
			g.CacheNodeType = v
		}
		if v := r.FormValue("EngineVersion"); v != "" {
			g.EngineVersion = v
		}
		if v := r.FormValue("AutomaticFailoverEnabled"); v != "" {
			fo := "disabled"
			if strings.EqualFold(v, "true") {
				fo = "enabled"
			}
			for i := range g.Members {
				g.Members[i].AutomaticFailover = fo
			}
		}
	})
}

func handleECDeleteGlobalReplGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("GlobalReplicationGroupId")
	g, ok := ecGlobalReplGroups.Get(id)
	if !ok {
		ecErrorXML(w, "GlobalReplicationGroupNotFoundFault", "Global replication group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	g.Status = "deleting"
	ecGlobalReplGroups.Delete(id)
	ecXMLResponse(w, "DeleteGlobalReplicationGroup", renderECGlobalReplGroup(g), sim.RequestID(r.Context()))
}

func handleECDisassociateGlobalReplGroup(w http.ResponseWriter, r *http.Request) {
	replGroupId := r.FormValue("ReplicationGroupId")
	region := r.FormValue("ReplicationGroupRegion")
	ecGlobalReplGroupMutate(w, r, "DisassociateGlobalReplicationGroup", func(g *ECGlobalReplicationGroup) {
		kept := g.Members[:0:0]
		for _, m := range g.Members {
			if m.ReplicationGroupId == replGroupId && (region == "" || m.ReplicationGroupRegion == region) {
				continue
			}
			kept = append(kept, m)
		}
		g.Members = kept
	})
}

func handleECFailoverGlobalReplGroup(w http.ResponseWriter, r *http.Request) {
	primaryRegion := r.FormValue("PrimaryRegion")
	primaryRG := r.FormValue("PrimaryReplicationGroupId")
	ecGlobalReplGroupMutate(w, r, "FailoverGlobalReplicationGroup", func(g *ECGlobalReplicationGroup) {
		for i := range g.Members {
			isNewPrimary := g.Members[i].ReplicationGroupId == primaryRG ||
				(primaryRG == "" && g.Members[i].ReplicationGroupRegion == primaryRegion)
			if isNewPrimary {
				g.Members[i].Role = "PRIMARY"
			} else {
				g.Members[i].Role = "SECONDARY"
			}
		}
	})
}

func handleECIncreaseNodeGroupsGlobal(w http.ResponseWriter, r *http.Request) {
	target := atoiOrZero(r.FormValue("NodeGroupCount"))
	ecGlobalReplGroupMutate(w, r, "IncreaseNodeGroupsInGlobalReplicationGroup", func(g *ECGlobalReplicationGroup) {
		for len(g.GlobalNodeGroups) < target {
			n := len(g.GlobalNodeGroups) + 1
			g.GlobalNodeGroups = append(g.GlobalNodeGroups, ECGlobalNodeGroup{
				GlobalNodeGroupId: fmt.Sprintf("%s-%04d", g.GlobalReplicationGroupId, n),
				Slots:             "",
			})
		}
		g.ClusterEnabled = true
	})
}

func handleECDecreaseNodeGroupsGlobal(w http.ResponseWriter, r *http.Request) {
	target := atoiOrZero(r.FormValue("NodeGroupCount"))
	ecGlobalReplGroupMutate(w, r, "DecreaseNodeGroupsInGlobalReplicationGroup", func(g *ECGlobalReplicationGroup) {
		if target > 0 && target < len(g.GlobalNodeGroups) {
			g.GlobalNodeGroups = g.GlobalNodeGroups[:target]
		}
	})
}

func handleECRebalanceSlotsGlobal(w http.ResponseWriter, r *http.Request) {
	ecGlobalReplGroupMutate(w, r, "RebalanceSlotsInGlobalReplicationGroup", func(g *ECGlobalReplicationGroup) {
		g.Status = "available"
	})
}

// Cache security groups (EC2-Classic ingress)

func renderECCacheSecGroup(g ECCacheSecurityGroup) string {
	var b strings.Builder
	b.WriteString("<CacheSecurityGroup>")
	fmt.Fprintf(&b, "<OwnerId>%s</OwnerId>", xmlEscape(g.OwnerId))
	fmt.Fprintf(&b, "<CacheSecurityGroupName>%s</CacheSecurityGroupName>", xmlEscape(g.CacheSecurityGroupName))
	fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(g.Description))
	b.WriteString("<EC2SecurityGroups>")
	for _, rule := range g.EC2SecurityGroups {
		b.WriteString("<EC2SecurityGroup>")
		fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(rule.Status))
		fmt.Fprintf(&b, "<EC2SecurityGroupName>%s</EC2SecurityGroupName>", xmlEscape(rule.EC2SecurityGroupName))
		fmt.Fprintf(&b, "<EC2SecurityGroupOwnerId>%s</EC2SecurityGroupOwnerId>", xmlEscape(rule.EC2SecurityGroupOwnerId))
		b.WriteString("</EC2SecurityGroup>")
	}
	b.WriteString("</EC2SecurityGroups>")
	fmt.Fprintf(&b, "<ARN>%s</ARN>", xmlEscape(g.ARN))
	b.WriteString("</CacheSecurityGroup>")
	return b.String()
}

func handleECCreateCacheSecGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("CacheSecurityGroupName")
	if name == "" {
		ecErrorXML(w, "MissingParameter", "CacheSecurityGroupName is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := ecCacheSecGroups.Get(name); ok {
		ecErrorXML(w, "CacheSecurityGroupAlreadyExists", "Cache security group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	g := ECCacheSecurityGroup{
		CacheSecurityGroupName: name,
		Description:            r.FormValue("Description"),
		OwnerId:                awsAccountID(),
		ARN:                    ecCacheSecGroupARN(name),
	}
	ecCacheSecGroups.Put(name, g)
	ecXMLResponse(w, "CreateCacheSecurityGroup", renderECCacheSecGroup(g), sim.RequestID(r.Context()))
}

func handleECDeleteCacheSecGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("CacheSecurityGroupName")
	if _, ok := ecCacheSecGroups.Get(name); !ok {
		ecErrorXML(w, "CacheSecurityGroupNotFound", "Cache security group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ecCacheSecGroups.Delete(name)
	ecXMLResponse(w, "DeleteCacheSecurityGroup", "", sim.RequestID(r.Context()))
}

func handleECAuthorizeCacheSecGroupIngress(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("CacheSecurityGroupName")
	if _, ok := ecCacheSecGroups.Get(name); !ok {
		ecErrorXML(w, "CacheSecurityGroupNotFound", "Cache security group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ec2Name := r.FormValue("EC2SecurityGroupName")
	owner := r.FormValue("EC2SecurityGroupOwnerId")
	if owner == "" {
		owner = awsAccountID()
	}
	ecCacheSecGroups.Update(name, func(g *ECCacheSecurityGroup) {
		g.EC2SecurityGroups = append(g.EC2SecurityGroups, ECCacheSecurityGroupRule{
			EC2SecurityGroupName:    ec2Name,
			EC2SecurityGroupOwnerId: owner,
			Status:                  "authorizing",
		})
	})
	updated, _ := ecCacheSecGroups.Get(name)
	ecXMLResponse(w, "AuthorizeCacheSecurityGroupIngress", renderECCacheSecGroup(updated), sim.RequestID(r.Context()))
}

func handleECRevokeCacheSecGroupIngress(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("CacheSecurityGroupName")
	if _, ok := ecCacheSecGroups.Get(name); !ok {
		ecErrorXML(w, "CacheSecurityGroupNotFound", "Cache security group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ec2Name := r.FormValue("EC2SecurityGroupName")
	ecCacheSecGroups.Update(name, func(g *ECCacheSecurityGroup) {
		kept := g.EC2SecurityGroups[:0:0]
		for _, rule := range g.EC2SecurityGroups {
			if rule.EC2SecurityGroupName == ec2Name {
				continue
			}
			kept = append(kept, rule)
		}
		g.EC2SecurityGroups = kept
	})
	updated, _ := ecCacheSecGroups.Get(name)
	ecXMLResponse(w, "RevokeCacheSecurityGroupIngress", renderECCacheSecGroup(updated), sim.RequestID(r.Context()))
}

// Service-update actions

// ecUpdateActionTargets enumerates (clusterId, replicationGroupId) pairs
// the update action applies to, honoring the CacheClusterIds /
// ReplicationGroupIds filters.
func ecUpdateActionTargets(r *http.Request) [][2]string {
	clusterIds := ecParseStringList(r, "CacheClusterIds", "member")
	replGroupIds := ecParseStringList(r, "ReplicationGroupIds", "member")
	var out [][2]string
	if len(clusterIds) == 0 && len(replGroupIds) == 0 {
		for _, c := range ecClusters.List() {
			out = append(out, [2]string{c.CacheClusterId, ""})
		}
		for _, g := range ecReplGroups.List() {
			out = append(out, [2]string{"", g.ReplicationGroupId})
		}
		return out
	}
	for _, id := range clusterIds {
		out = append(out, [2]string{id, ""})
	}
	for _, id := range replGroupIds {
		out = append(out, [2]string{"", id})
	}
	return out
}

func handleECDescribeUpdateActions(w http.ResponseWriter, r *http.Request) {
	svcUpdate := r.FormValue("ServiceUpdateName")
	if svcUpdate == "" {
		svcUpdate = "elasticache-20240101-001"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var b strings.Builder
	b.WriteString("<UpdateActions>")
	for _, t := range ecUpdateActionTargets(r) {
		b.WriteString("<UpdateAction>")
		if t[0] != "" {
			fmt.Fprintf(&b, "<CacheClusterId>%s</CacheClusterId>", xmlEscape(t[0]))
		}
		if t[1] != "" {
			fmt.Fprintf(&b, "<ReplicationGroupId>%s</ReplicationGroupId>", xmlEscape(t[1]))
		}
		fmt.Fprintf(&b, "<ServiceUpdateName>%s</ServiceUpdateName>", xmlEscape(svcUpdate))
		fmt.Fprintf(&b, "<ServiceUpdateReleaseDate>%s</ServiceUpdateReleaseDate>", now)
		b.WriteString("<ServiceUpdateSeverity>important</ServiceUpdateSeverity>")
		b.WriteString("<ServiceUpdateStatus>available</ServiceUpdateStatus>")
		b.WriteString("<ServiceUpdateType>security-update</ServiceUpdateType>")
		fmt.Fprintf(&b, "<UpdateActionAvailableDate>%s</UpdateActionAvailableDate>", now)
		b.WriteString("<UpdateActionStatus>not-applied</UpdateActionStatus>")
		fmt.Fprintf(&b, "<UpdateActionStatusModifiedDate>%s</UpdateActionStatusModifiedDate>", now)
		b.WriteString("<NodesUpdated>0/1</NodesUpdated>")
		b.WriteString("<Engine>redis</Engine>")
		b.WriteString("</UpdateAction>")
	}
	b.WriteString("</UpdateActions>")
	ecXMLResponse(w, "DescribeUpdateActions", b.String(), sim.RequestID(r.Context()))
}

func ecRenderUpdateActionResults(r *http.Request, status string) string {
	svcUpdate := r.FormValue("ServiceUpdateName")
	var b strings.Builder
	b.WriteString("<ProcessedUpdateActions>")
	for _, t := range ecUpdateActionTargets(r) {
		b.WriteString("<ProcessedUpdateAction>")
		if t[0] != "" {
			fmt.Fprintf(&b, "<CacheClusterId>%s</CacheClusterId>", xmlEscape(t[0]))
		}
		if t[1] != "" {
			fmt.Fprintf(&b, "<ReplicationGroupId>%s</ReplicationGroupId>", xmlEscape(t[1]))
		}
		fmt.Fprintf(&b, "<ServiceUpdateName>%s</ServiceUpdateName>", xmlEscape(svcUpdate))
		fmt.Fprintf(&b, "<UpdateActionStatus>%s</UpdateActionStatus>", xmlEscape(status))
		b.WriteString("</ProcessedUpdateAction>")
	}
	b.WriteString("</ProcessedUpdateActions>")
	b.WriteString("<UnprocessedUpdateActions></UnprocessedUpdateActions>")
	return b.String()
}

func handleECBatchApplyUpdateAction(w http.ResponseWriter, r *http.Request) {
	ecXMLResponse(w, "BatchApplyUpdateAction", ecRenderUpdateActionResults(r, "scheduling"), sim.RequestID(r.Context()))
}

func handleECBatchStopUpdateAction(w http.ResponseWriter, r *http.Request) {
	ecXMLResponse(w, "BatchStopUpdateAction", ecRenderUpdateActionResults(r, "stopping"), sim.RequestID(r.Context()))
}

// Replica / shard reconfiguration + failover testing on the existing
// replication-group store

// ecReplGroupAction looks up a replication group, applies fn, and returns
// the updated group under the named result wrapper.
func ecReplGroupAction(w http.ResponseWriter, r *http.Request, op string, fn func(*ECReplicationGroup)) {
	id := r.FormValue("ReplicationGroupId")
	if _, ok := ecReplGroups.Get(id); !ok {
		ecErrorXML(w, "ReplicationGroupNotFoundFault", "Replication group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if fn != nil {
		ecReplGroups.Update(id, fn)
	}
	updated, _ := ecReplGroups.Get(id)
	ecXMLResponse(w, op, renderECReplGroup(updated), sim.RequestID(r.Context()))
}

func handleECIncreaseReplicaCount(w http.ResponseWriter, r *http.Request) {
	newCount := atoiOrZero(r.FormValue("NewReplicaCount"))
	ecReplGroupAction(w, r, "IncreaseReplicaCount", func(g *ECReplicationGroup) {
		// MemberClusters = 1 primary + N replicas per node group.
		want := newCount + 1
		for len(g.MemberClusters) < want {
			n := len(g.MemberClusters) + 1
			g.MemberClusters = append(g.MemberClusters, fmt.Sprintf("%s-%03d", g.ReplicationGroupId, n))
		}
	})
}

func handleECDecreaseReplicaCount(w http.ResponseWriter, r *http.Request) {
	newCount := atoiOrZero(r.FormValue("NewReplicaCount"))
	ecReplGroupAction(w, r, "DecreaseReplicaCount", func(g *ECReplicationGroup) {
		want := newCount + 1
		if want >= 1 && want < len(g.MemberClusters) {
			g.MemberClusters = g.MemberClusters[:want]
		}
	})
}

func handleECModifyReplGroupShardConfig(w http.ResponseWriter, r *http.Request) {
	nodeGroupCount := atoiOrZero(r.FormValue("NodeGroupCount"))
	ecReplGroupAction(w, r, "ModifyReplicationGroupShardConfiguration", func(g *ECReplicationGroup) {
		if nodeGroupCount > 1 {
			g.ClusterEnabled = true
		}
	})
}

func handleECTestFailover(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("NodeGroupId") == "" {
		ecErrorXML(w, "MissingParameter", "NodeGroupId is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	ecReplGroupAction(w, r, "TestFailover", nil)
}

// Node-type modifications + reserved-node purchase + online migration

func handleECListAllowedNodeTypeModifications(w http.ResponseWriter, r *http.Request) {
	scaleUp := []string{"cache.r7g.large", "cache.r7g.xlarge", "cache.r7g.2xlarge"}
	scaleDown := []string{"cache.t4g.micro", "cache.t4g.small", "cache.t4g.medium"}
	var b strings.Builder
	b.WriteString("<ScaleUpModifications>")
	for _, t := range scaleUp {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(t))
	}
	b.WriteString("</ScaleUpModifications>")
	b.WriteString("<ScaleDownModifications>")
	for _, t := range scaleDown {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(t))
	}
	b.WriteString("</ScaleDownModifications>")
	ecXMLResponse(w, "ListAllowedNodeTypeModifications", b.String(), sim.RequestID(r.Context()))
}

func handleECPurchaseReservedCacheNodesOffering(w http.ResponseWriter, r *http.Request) {
	offeringId := r.FormValue("ReservedCacheNodesOfferingId")
	if offeringId == "" {
		ecErrorXML(w, "MissingParameter", "ReservedCacheNodesOfferingId is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	rcnId := r.FormValue("ReservedCacheNodeId")
	if rcnId == "" {
		rcnId = "ri-" + sim.RequestID(r.Context())[:8]
	}
	count := atoiOrZero(r.FormValue("CacheNodeCount"))
	if count == 0 {
		count = 1
	}
	var b strings.Builder
	b.WriteString("<ReservedCacheNode>")
	fmt.Fprintf(&b, "<ReservedCacheNodeId>%s</ReservedCacheNodeId>", xmlEscape(rcnId))
	fmt.Fprintf(&b, "<ReservedCacheNodesOfferingId>%s</ReservedCacheNodesOfferingId>", xmlEscape(offeringId))
	b.WriteString("<CacheNodeType>cache.t3.micro</CacheNodeType>")
	fmt.Fprintf(&b, "<StartTime>%s</StartTime>", time.Now().UTC().Format(time.RFC3339))
	b.WriteString("<Duration>31536000</Duration>")
	b.WriteString("<FixedPrice>0.0</FixedPrice>")
	b.WriteString("<UsagePrice>0.0</UsagePrice>")
	fmt.Fprintf(&b, "<CacheNodeCount>%d</CacheNodeCount>", count)
	b.WriteString("<ProductDescription>redis</ProductDescription>")
	b.WriteString("<OfferingType>No Upfront</OfferingType>")
	b.WriteString("<State>payment-pending</State>")
	b.WriteString("<RecurringCharges><RecurringCharge><RecurringChargeAmount>0.018</RecurringChargeAmount><RecurringChargeFrequency>Hourly</RecurringChargeFrequency></RecurringCharge></RecurringCharges>")
	fmt.Fprintf(&b, "<ReservationARN>arn:aws:elasticache:%s:%s:reserved-instance:%s</ReservationARN>", awsRegion(), awsAccountID(), xmlEscape(rcnId))
	b.WriteString("</ReservedCacheNode>")
	ecXMLResponse(w, "PurchaseReservedCacheNodesOffering", b.String(), sim.RequestID(r.Context()))
}

func handleECStartMigration(w http.ResponseWriter, r *http.Request) {
	ecReplGroupAction(w, r, "StartMigration", func(g *ECReplicationGroup) {
		g.Status = "modifying"
	})
}

func handleECCompleteMigration(w http.ResponseWriter, r *http.Request) {
	ecReplGroupAction(w, r, "CompleteMigration", func(g *ECReplicationGroup) {
		g.Status = "available"
	})
}

func handleECTestMigration(w http.ResponseWriter, r *http.Request) {
	ecReplGroupAction(w, r, "TestMigration", func(g *ECReplicationGroup) {
		g.Status = "modifying"
	})
}
