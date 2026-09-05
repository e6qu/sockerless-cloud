package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ElastiCache — awsQuery protocol. Surface scoped to the 90th-
// percentile lifecycle: CreateCacheCluster, DescribeCacheClusters
// (waiter), ModifyCacheCluster, DeleteCacheCluster, plus tags.
// Engine itself (Redis / Memcached) is not simulated; the sim
// reports Status=available immediately.

type ECCluster struct {
	CacheClusterId         string
	CacheNodeType          string
	Engine                 string
	EngineVersion          string
	CacheClusterStatus     string
	NumCacheNodes          int
	CacheClusterCreateTime string
	ARN                    string
	Endpoint               string
	Port                   int
	Tags                   map[string]string
}

// ECReplicationGroup models the subset of the ReplicationGroup shape
// the SDK + terraform-provider-aws read back after Create/Modify.
type ECReplicationGroup struct {
	ReplicationGroupId     string
	Description            string
	Status                 string
	CacheNodeType          string
	Engine                 string
	AutomaticFailover      string
	MultiAZ                string
	ClusterEnabled         bool
	MemberClusters         []string
	SnapshotRetentionLimit int
	SnapshotWindow         string
	ConfigEndpointAddress  string
	ConfigEndpointPort     int
	ARN                    string
	CreateTime             string
	Tags                   map[string]string
}

// ECSubnetGroup models the CacheSubnetGroup shape.
type ECSubnetGroup struct {
	Name        string
	Description string
	VpcId       string
	SubnetIds   []string
	ARN         string
	Tags        map[string]string
}

// ECParameterGroup models the CacheParameterGroup shape. Params holds
// the user-modified parameter values (DescribeCacheParameters returns
// these merged over the engine defaults; ResetCacheParameterGroup
// clears them).
type ECParameterGroup struct {
	Name        string
	Family      string
	Description string
	ARN         string
	Params      map[string]string
	Tags        map[string]string
}

// ECSnapshot models the Snapshot shape created from a cache cluster or
// replication group. NodeSnapshots carry the per-node backup metadata
// the SDK + CLI read back.
type ECSnapshot struct {
	SnapshotName           string
	ReplicationGroupId     string
	CacheClusterId         string
	SnapshotStatus         string
	SnapshotSource         string
	CacheNodeType          string
	Engine                 string
	EngineVersion          string
	NumCacheNodes          int
	Port                   int
	SnapshotRetentionLimit int
	SnapshotWindow         string
	ARN                    string
	CacheClusterCreateTime string
	Tags                   map[string]string
}

// ECUser models the User shape (ElastiCache RBAC for Redis/Valkey).
type ECUser struct {
	UserId               string
	UserName             string
	Status               string
	Engine               string
	MinimumEngineVersion string
	AccessString         string
	NoPasswordRequired   bool
	PasswordCount        int
	UserGroupIds         []string
	ARN                  string
	Tags                 map[string]string
}

// ECUserGroup models the UserGroup shape.
type ECUserGroup struct {
	UserGroupId          string
	Status               string
	Engine               string
	MinimumEngineVersion string
	UserIds              []string
	ReplicationGroups    []string
	ARN                  string
	Tags                 map[string]string
}

var (
	ecClusters    sim.Store[ECCluster]
	ecReplGroups  sim.Store[ECReplicationGroup]
	ecSubnetGrps  sim.Store[ECSubnetGroup]
	ecParamGroups sim.Store[ECParameterGroup]
	ecSnapshots   sim.Store[ECSnapshot]
	ecUsers       sim.Store[ECUser]
	ecUserGroups  sim.Store[ECUserGroup]
	// The reservations this account has bought, keyed by reserved-node id.
	ecReservedNodes sim.Store[ECReservedCacheNode]
)

// ECReservedCacheNode is one purchased reservation. What it costs and what it
// reserves are the offering's terms, carried over at purchase rather than
// restated: a reservation whose price did not come from the offering it was
// bought against would be a number this simulator made up.
type ECReservedCacheNode struct {
	ReservedCacheNodeId string `json:"reservedCacheNodeId"`
	OfferingId          string `json:"offeringId"`
	CacheNodeType       string `json:"cacheNodeType"`
	Duration            int    `json:"duration"`
	FixedPrice          string `json:"fixedPrice"`
	UsagePrice          string `json:"usagePrice"`
	ProductDescription  string `json:"productDescription"`
	OfferingType        string `json:"offeringType"`
	RecurringAmount     string `json:"recurringAmount"`
	RecurringFrequency  string `json:"recurringFrequency"`
	CacheNodeCount      int    `json:"cacheNodeCount"`
	StartTime           string `json:"startTime"`
	State               string `json:"state"`
}

// ecReservedCacheNodesOffering is one purchasable offering.
//
// The offerings the simulator answers with are the ones a purchase can be made
// against — the two are the same table, so a reservation's terms are the
// offering's terms and a purchase against an id that is not here is refused the
// way the service refuses one. Before that, the purchase ignored the offering
// it was handed and answered with terms of its own, which is a receipt for
// something nobody sold.
//
// The table is a transcription, the way the AWS Lambda runtime images and the
// ElastiCache engine versions are: what AWS actually sells is a per-region
// price list that changes, and it is not in the vendored model. Keeping one
// row is what lets a purchase mean anything at all — declining the catalogue
// outright would leave PurchaseReservedCacheNodesOffering unreachable, an
// operation served but impossible to succeed at.
type ecReservedCacheNodesOffering struct {
	Id                 string
	CacheNodeType      string
	Duration           int
	FixedPrice         string
	UsagePrice         string
	ProductDescription string
	OfferingType       string
	RecurringAmount    string
	RecurringFrequency string
}

var ecReservedCacheNodesOfferings = []ecReservedCacheNodesOffering{{
	Id:                 "649fd0c8-cf6d-47a0-bfa6-060f8e75e95f",
	CacheNodeType:      "cache.t3.micro",
	Duration:           31536000,
	FixedPrice:         "0.0",
	UsagePrice:         "0.0",
	ProductDescription: "redis",
	OfferingType:       "No Upfront",
	RecurringAmount:    "0.018",
	RecurringFrequency: "Hourly",
}}

// ecAPIVersion is the canonical AWS ElastiCache API version (Query
// Protocol). Used to disambiguate Action names from other awsQuery
// services in the AWSQueryRouter dispatch.
const ecAPIVersion = "2015-02-02"

func registerElastiCache(r *AWSQueryRouter, srv *sim.Server) {
	ecClusters = sim.MakeStore[ECCluster](srv.DB(), "elasticache_clusters")
	ecReplGroups = sim.MakeStore[ECReplicationGroup](srv.DB(), "elasticache_replication_groups")
	ecSubnetGrps = sim.MakeStore[ECSubnetGroup](srv.DB(), "elasticache_subnet_groups")
	ecParamGroups = sim.MakeStore[ECParameterGroup](srv.DB(), "elasticache_parameter_groups")
	r.RegisterVersioned(ecAPIVersion, "CreateCacheCluster", handleECCreate)
	r.RegisterVersioned(ecAPIVersion, "DescribeCacheClusters", handleECDescribe)
	r.RegisterVersioned(ecAPIVersion, "ModifyCacheCluster", handleECModify)
	r.RegisterVersioned(ecAPIVersion, "DeleteCacheCluster", handleECDelete)
	r.RegisterVersioned(ecAPIVersion, "RebootCacheCluster", handleECReboot)
	r.RegisterVersioned(ecAPIVersion, "CreateReplicationGroup", handleECCreateReplGroup)
	r.RegisterVersioned(ecAPIVersion, "DescribeReplicationGroups", handleECDescribeReplGroups)
	r.RegisterVersioned(ecAPIVersion, "ModifyReplicationGroup", handleECModifyReplGroup)
	r.RegisterVersioned(ecAPIVersion, "DeleteReplicationGroup", handleECDeleteReplGroup)
	r.RegisterVersioned(ecAPIVersion, "CreateCacheSubnetGroup", handleECCreateSubnetGroup)
	r.RegisterVersioned(ecAPIVersion, "DescribeCacheSubnetGroups", handleECDescribeSubnetGroups)
	r.RegisterVersioned(ecAPIVersion, "ModifyCacheSubnetGroup", handleECModifySubnetGroup)
	r.RegisterVersioned(ecAPIVersion, "DeleteCacheSubnetGroup", handleECDeleteSubnetGroup)
	r.RegisterVersioned(ecAPIVersion, "CreateCacheParameterGroup", handleECCreateParamGroup)
	r.RegisterVersioned(ecAPIVersion, "DescribeCacheParameterGroups", handleECDescribeParamGroups)
	r.RegisterVersioned(ecAPIVersion, "DeleteCacheParameterGroup", handleECDeleteParamGroup)
	r.RegisterVersioned(ecAPIVersion, "AddTagsToResource", handleECAddTags)
	r.RegisterVersioned(ecAPIVersion, "ListTagsForResource", handleECListTags)
	r.RegisterVersioned(ecAPIVersion, "RemoveTagsFromResource", handleECRemoveTags)
	ecSnapshots = sim.MakeStore[ECSnapshot](srv.DB(), "elasticache_snapshots")
	ecReservedNodes = sim.MakeStore[ECReservedCacheNode](srv.DB(), "elasticache_reserved_nodes")
	ecUsers = sim.MakeStore[ECUser](srv.DB(), "elasticache_users")
	ecUserGroups = sim.MakeStore[ECUserGroup](srv.DB(), "elasticache_user_groups")
	r.RegisterVersioned(ecAPIVersion, "CreateSnapshot", handleECCreateSnapshot)
	r.RegisterVersioned(ecAPIVersion, "DescribeSnapshots", handleECDescribeSnapshots)
	r.RegisterVersioned(ecAPIVersion, "DeleteSnapshot", handleECDeleteSnapshot)
	r.RegisterVersioned(ecAPIVersion, "CopySnapshot", handleECCopySnapshot)
	r.RegisterVersioned(ecAPIVersion, "CreateUser", handleECCreateUser)
	r.RegisterVersioned(ecAPIVersion, "DescribeUsers", handleECDescribeUsers)
	r.RegisterVersioned(ecAPIVersion, "ModifyUser", handleECModifyUser)
	r.RegisterVersioned(ecAPIVersion, "DeleteUser", handleECDeleteUser)
	r.RegisterVersioned(ecAPIVersion, "CreateUserGroup", handleECCreateUserGroup)
	r.RegisterVersioned(ecAPIVersion, "DescribeUserGroups", handleECDescribeUserGroups)
	r.RegisterVersioned(ecAPIVersion, "ModifyUserGroup", handleECModifyUserGroup)
	r.RegisterVersioned(ecAPIVersion, "DeleteUserGroup", handleECDeleteUserGroup)
	r.RegisterVersioned(ecAPIVersion, "DescribeCacheParameters", handleECDescribeParameters)
	r.RegisterVersioned(ecAPIVersion, "ModifyCacheParameterGroup", handleECModifyParameters)
	r.RegisterVersioned(ecAPIVersion, "ResetCacheParameterGroup", handleECResetParameters)
	r.RegisterVersioned(ecAPIVersion, "DescribeEngineDefaultParameters", handleECDescribeEngineDefaultParameters)
	r.RegisterVersioned(ecAPIVersion, "DescribeEvents", handleECDescribeEvents)
	r.RegisterVersioned(ecAPIVersion, "DescribeCacheEngineVersions", handleECDescribeCacheEngineVersions)
	r.RegisterVersioned(ecAPIVersion, "DescribeReservedCacheNodes", handleECDescribeReservedCacheNodes)
	r.RegisterVersioned(ecAPIVersion, "DescribeReservedCacheNodesOfferings", handleECDescribeReservedCacheNodesOfferings)
	r.RegisterVersioned(ecAPIVersion, "DescribeServiceUpdates", handleECDescribeServiceUpdates)
	r.RegisterVersioned(ecAPIVersion, "DescribeCacheSecurityGroups", handleECDescribeCacheSecurityGroups)
	registerElastiCacheServerless(r, srv)
}

func ecClusterARN(id string) string {
	return fmt.Sprintf("arn:aws:elasticache:%s:%s:cluster:%s", awsRegion(), awsAccountID(), id)
}

func ecReplGroupARN(id string) string {
	return fmt.Sprintf("arn:aws:elasticache:%s:%s:replicationgroup:%s", awsRegion(), awsAccountID(), id)
}

func ecSubnetGroupARN(name string) string {
	return fmt.Sprintf("arn:aws:elasticache:%s:%s:subnetgroup:%s", awsRegion(), awsAccountID(), name)
}

func ecParamGroupARN(name string) string {
	return fmt.Sprintf("arn:aws:elasticache:%s:%s:parametergroup:%s", awsRegion(), awsAccountID(), name)
}

func ecSnapshotARN(name string) string {
	return fmt.Sprintf("arn:aws:elasticache:%s:%s:snapshot:%s", awsRegion(), awsAccountID(), name)
}

func ecUserARN(id string) string {
	return fmt.Sprintf("arn:aws:elasticache:%s:%s:user:%s", awsRegion(), awsAccountID(), id)
}

func ecUserGroupARN(id string) string {
	return fmt.Sprintf("arn:aws:elasticache:%s:%s:usergroup:%s", awsRegion(), awsAccountID(), id)
}

func ecXMLResponse(w http.ResponseWriter, op string, body string, requestID string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w,
		`<%sResponse xmlns="http://elasticache.amazonaws.com/doc/2015-02-02/"><%sResult>%s</%sResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></%sResponse>`,
		op, op, body, op, requestID, op)
}

func ecErrorXML(w http.ResponseWriter, code, message string, status int, requestID string) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w,
		`<ErrorResponse xmlns="http://elasticache.amazonaws.com/doc/2015-02-02/"><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>%s</RequestId></ErrorResponse>`,
		code, message, requestID)
}

func renderECCluster(c ECCluster) string {
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
	fmt.Fprintf(&b, "<ConfigurationEndpoint><Address>%s</Address><Port>%d</Port></ConfigurationEndpoint>", xmlEscape(c.Endpoint), c.Port)
	b.WriteString("</CacheCluster>")
	return b.String()
}

func handleECCreate(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CacheClusterId")
	if id == "" {
		ecErrorXML(w, "MissingParameter", "CacheClusterId is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := ecClusters.Get(id); ok {
		ecErrorXML(w, "CacheClusterAlreadyExists", "Cluster already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	engine := r.FormValue("Engine")
	port := 6379
	if engine == "memcached" {
		port = 11211
	}
	if p := atoiOrZero(r.FormValue("Port")); p > 0 {
		port = p
	}
	num := atoiOrZero(r.FormValue("NumCacheNodes"))
	if num == 0 {
		num = 1
	}
	engineVersion := r.FormValue("EngineVersion")
	if engineVersion == "" {
		engineVersion = ecDefaultEngineVersion(engine)
	}
	c := ECCluster{
		CacheClusterId:         id,
		CacheNodeType:          r.FormValue("CacheNodeType"),
		Engine:                 engine,
		EngineVersion:          engineVersion,
		CacheClusterStatus:     "available",
		NumCacheNodes:          num,
		CacheClusterCreateTime: time.Now().UTC().Format(time.RFC3339),
		ARN:                    ecClusterARN(id),
		Endpoint:               fmt.Sprintf("%s.%s.cache.amazonaws.com", id, awsRegion()),
		Port:                   port,
		Tags:                   parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	ecClusters.Put(id, c)
	ecXMLResponse(w, "CreateCacheCluster", renderECCluster(c), sim.RequestID(r.Context()))
}

func handleECDescribe(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("CacheClusterId")
	var b strings.Builder
	b.WriteString("<CacheClusters>")
	matched := false
	for _, c := range ecClusters.List() {
		if wanted != "" && c.CacheClusterId != wanted {
			continue
		}
		matched = true
		b.WriteString(renderECCluster(c))
	}
	if wanted != "" && !matched {
		ecErrorXML(w, "CacheClusterNotFound", fmt.Sprintf("Cache cluster %q not found", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</CacheClusters>")
	ecXMLResponse(w, "DescribeCacheClusters", b.String(), sim.RequestID(r.Context()))
}

func handleECModify(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CacheClusterId")
	if _, ok := ecClusters.Get(id); !ok {
		ecErrorXML(w, "CacheClusterNotFound", "Cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ecClusters.Update(id, func(c *ECCluster) {
		if v := r.FormValue("CacheNodeType"); v != "" {
			c.CacheNodeType = v
		}
		if v := r.FormValue("EngineVersion"); v != "" {
			c.EngineVersion = v
		}
		if v := r.FormValue("NumCacheNodes"); v != "" {
			c.NumCacheNodes = atoiOrZero(v)
		}
	})
	updated, _ := ecClusters.Get(id)
	ecXMLResponse(w, "ModifyCacheCluster", renderECCluster(updated), sim.RequestID(r.Context()))
}

func handleECDelete(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CacheClusterId")
	c, ok := ecClusters.Get(id)
	if !ok {
		ecErrorXML(w, "CacheClusterNotFound", "Cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	c.CacheClusterStatus = "deleting"
	ecClusters.Delete(id)
	ecXMLResponse(w, "DeleteCacheCluster", renderECCluster(c), sim.RequestID(r.Context()))
}

func handleECReboot(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("CacheClusterId")
	if _, ok := ecClusters.Get(id); !ok {
		ecErrorXML(w, "CacheClusterNotFound", "Cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ecClusters.Update(id, func(c *ECCluster) {
		c.CacheClusterStatus = "rebooting cache cluster nodes"
	})
	updated, _ := ecClusters.Get(id)
	ecXMLResponse(w, "RebootCacheCluster", renderECCluster(updated), sim.RequestID(r.Context()))
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
	fmt.Fprintf(&b, "<SnapshotRetentionLimit>%d</SnapshotRetentionLimit>", g.SnapshotRetentionLimit)
	if g.SnapshotWindow != "" {
		fmt.Fprintf(&b, "<SnapshotWindow>%s</SnapshotWindow>", xmlEscape(g.SnapshotWindow))
	}
	fmt.Fprintf(&b, "<ReplicationGroupCreateTime>%s</ReplicationGroupCreateTime>", xmlEscape(g.CreateTime))
	fmt.Fprintf(&b, "<ARN>%s</ARN>", xmlEscape(g.ARN))
	b.WriteString("<MemberClusters>")
	for _, m := range g.MemberClusters {
		fmt.Fprintf(&b, "<ClusterId>%s</ClusterId>", xmlEscape(m))
	}
	b.WriteString("</MemberClusters>")
	fmt.Fprintf(&b, "<ConfigurationEndpoint><Address>%s</Address><Port>%d</Port></ConfigurationEndpoint>", xmlEscape(g.ConfigEndpointAddress), g.ConfigEndpointPort)
	b.WriteString("</ReplicationGroup>")
	return b.String()
}

func handleECCreateReplGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ReplicationGroupId")
	if id == "" {
		ecErrorXML(w, "MissingParameter", "ReplicationGroupId is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := ecReplGroups.Get(id); ok {
		ecErrorXML(w, "ReplicationGroupAlreadyExistsFault", "Replication group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	engine := r.FormValue("Engine")
	if engine == "" {
		engine = "redis"
	}
	port := 6379
	if p := atoiOrZero(r.FormValue("Port")); p > 0 {
		port = p
	}
	failover := "disabled"
	if strings.EqualFold(r.FormValue("AutomaticFailoverEnabled"), "true") {
		failover = "enabled"
	}
	multiAZ := "disabled"
	if strings.EqualFold(r.FormValue("MultiAZEnabled"), "true") {
		multiAZ = "enabled"
	}
	clusterEnabled := strings.EqualFold(r.FormValue("ClusterMode"), "enabled") || atoiOrZero(r.FormValue("NumNodeGroups")) > 1
	num := atoiOrZero(r.FormValue("NumCacheClusters"))
	if num == 0 {
		num = 1
	}
	members := make([]string, 0, num)
	for i := 1; i <= num; i++ {
		members = append(members, fmt.Sprintf("%s-%03d", id, i))
	}
	g := ECReplicationGroup{
		ReplicationGroupId:     id,
		Description:            r.FormValue("ReplicationGroupDescription"),
		Status:                 "available",
		CacheNodeType:          r.FormValue("CacheNodeType"),
		Engine:                 engine,
		AutomaticFailover:      failover,
		MultiAZ:                multiAZ,
		ClusterEnabled:         clusterEnabled,
		MemberClusters:         members,
		SnapshotRetentionLimit: atoiOrZero(r.FormValue("SnapshotRetentionLimit")),
		SnapshotWindow:         r.FormValue("SnapshotWindow"),
		ConfigEndpointAddress:  fmt.Sprintf("clustercfg.%s.%s.cache.amazonaws.com", id, awsRegion()),
		ConfigEndpointPort:     port,
		ARN:                    ecReplGroupARN(id),
		CreateTime:             time.Now().UTC().Format(time.RFC3339),
		Tags:                   parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	ecReplGroups.Put(id, g)
	ecXMLResponse(w, "CreateReplicationGroup", renderECReplGroup(g), sim.RequestID(r.Context()))
}

func handleECDescribeReplGroups(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("ReplicationGroupId")
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

func handleECModifyReplGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ReplicationGroupId")
	if _, ok := ecReplGroups.Get(id); !ok {
		ecErrorXML(w, "ReplicationGroupNotFoundFault", "Replication group not found", http.StatusNotFound, sim.RequestID(r.Context()))
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
	})
	updated, _ := ecReplGroups.Get(id)
	ecXMLResponse(w, "ModifyReplicationGroup", renderECReplGroup(updated), sim.RequestID(r.Context()))
}

func handleECDeleteReplGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ReplicationGroupId")
	g, ok := ecReplGroups.Get(id)
	if !ok {
		ecErrorXML(w, "ReplicationGroupNotFoundFault", "Replication group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	g.Status = "deleting"
	ecReplGroups.Delete(id)
	ecXMLResponse(w, "DeleteReplicationGroup", renderECReplGroup(g), sim.RequestID(r.Context()))
}

func renderECSubnetGroup(g ECSubnetGroup) string {
	var b strings.Builder
	b.WriteString("<CacheSubnetGroup>")
	fmt.Fprintf(&b, "<CacheSubnetGroupName>%s</CacheSubnetGroupName>", xmlEscape(g.Name))
	fmt.Fprintf(&b, "<CacheSubnetGroupDescription>%s</CacheSubnetGroupDescription>", xmlEscape(g.Description))
	fmt.Fprintf(&b, "<VpcId>%s</VpcId>", xmlEscape(g.VpcId))
	fmt.Fprintf(&b, "<ARN>%s</ARN>", xmlEscape(g.ARN))
	b.WriteString("<Subnets>")
	for _, s := range g.SubnetIds {
		fmt.Fprintf(&b, "<Subnet><SubnetIdentifier>%s</SubnetIdentifier><SubnetAvailabilityZone><Name>%s</Name></SubnetAvailabilityZone></Subnet>", xmlEscape(s), xmlEscape(awsRegion()+"a"))
	}
	b.WriteString("</Subnets>")
	b.WriteString("</CacheSubnetGroup>")
	return b.String()
}

func ecParseSubnetIds(r *http.Request) []string {
	var ids []string
	for n := 1; n <= 50; n++ {
		v := r.FormValue(fmt.Sprintf("SubnetIds.SubnetIdentifier.%d", n))
		if v == "" {
			v = r.FormValue(fmt.Sprintf("SubnetIds.member.%d", n))
		}
		if v == "" {
			break
		}
		ids = append(ids, v)
	}
	return ids
}

func handleECCreateSubnetGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("CacheSubnetGroupName")
	if name == "" {
		ecErrorXML(w, "MissingParameter", "CacheSubnetGroupName is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := ecSubnetGrps.Get(name); ok {
		ecErrorXML(w, "CacheSubnetGroupAlreadyExistsFault", "Cache subnet group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	g := ECSubnetGroup{
		Name:        name,
		Description: r.FormValue("CacheSubnetGroupDescription"),
		VpcId:       "vpc-" + sim.RequestID(r.Context())[:8],
		SubnetIds:   ecParseSubnetIds(r),
		ARN:         ecSubnetGroupARN(name),
		Tags:        parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	ecSubnetGrps.Put(name, g)
	ecXMLResponse(w, "CreateCacheSubnetGroup", renderECSubnetGroup(g), sim.RequestID(r.Context()))
}

func handleECDescribeSubnetGroups(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("CacheSubnetGroupName")
	var b strings.Builder
	b.WriteString("<CacheSubnetGroups>")
	matched := false
	for _, g := range ecSubnetGrps.List() {
		if wanted != "" && g.Name != wanted {
			continue
		}
		matched = true
		b.WriteString(renderECSubnetGroup(g))
	}
	if wanted != "" && !matched {
		ecErrorXML(w, "CacheSubnetGroupNotFoundFault", fmt.Sprintf("Cache subnet group %q not found", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</CacheSubnetGroups>")
	ecXMLResponse(w, "DescribeCacheSubnetGroups", b.String(), sim.RequestID(r.Context()))
}

func handleECModifySubnetGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("CacheSubnetGroupName")
	if _, ok := ecSubnetGrps.Get(name); !ok {
		ecErrorXML(w, "CacheSubnetGroupNotFoundFault", "Cache subnet group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ecSubnetGrps.Update(name, func(g *ECSubnetGroup) {
		if v := r.FormValue("CacheSubnetGroupDescription"); v != "" {
			g.Description = v
		}
		if ids := ecParseSubnetIds(r); len(ids) > 0 {
			g.SubnetIds = ids
		}
	})
	updated, _ := ecSubnetGrps.Get(name)
	ecXMLResponse(w, "ModifyCacheSubnetGroup", renderECSubnetGroup(updated), sim.RequestID(r.Context()))
}

func handleECDeleteSubnetGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("CacheSubnetGroupName")
	if _, ok := ecSubnetGrps.Get(name); !ok {
		ecErrorXML(w, "CacheSubnetGroupNotFoundFault", "Cache subnet group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ecSubnetGrps.Delete(name)
	ecXMLResponse(w, "DeleteCacheSubnetGroup", "", sim.RequestID(r.Context()))
}

func renderECParamGroup(g ECParameterGroup) string {
	var b strings.Builder
	b.WriteString("<CacheParameterGroup>")
	fmt.Fprintf(&b, "<CacheParameterGroupName>%s</CacheParameterGroupName>", xmlEscape(g.Name))
	fmt.Fprintf(&b, "<CacheParameterGroupFamily>%s</CacheParameterGroupFamily>", xmlEscape(g.Family))
	fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(g.Description))
	fmt.Fprintf(&b, "<ARN>%s</ARN>", xmlEscape(g.ARN))
	b.WriteString("</CacheParameterGroup>")
	return b.String()
}

func handleECCreateParamGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("CacheParameterGroupName")
	if name == "" {
		ecErrorXML(w, "MissingParameter", "CacheParameterGroupName is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := ecParamGroups.Get(name); ok {
		ecErrorXML(w, "CacheParameterGroupAlreadyExistsFault", "Cache parameter group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	g := ECParameterGroup{
		Name:        name,
		Family:      r.FormValue("CacheParameterGroupFamily"),
		Description: r.FormValue("Description"),
		ARN:         ecParamGroupARN(name),
		Tags:        parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	ecParamGroups.Put(name, g)
	ecXMLResponse(w, "CreateCacheParameterGroup", renderECParamGroup(g), sim.RequestID(r.Context()))
}

func handleECDescribeParamGroups(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("CacheParameterGroupName")
	var b strings.Builder
	b.WriteString("<CacheParameterGroups>")
	matched := false
	for _, g := range ecParamGroups.List() {
		if wanted != "" && g.Name != wanted {
			continue
		}
		matched = true
		b.WriteString(renderECParamGroup(g))
	}
	if wanted != "" && !matched {
		ecErrorXML(w, "CacheParameterGroupNotFoundFault", fmt.Sprintf("Cache parameter group %q not found", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</CacheParameterGroups>")
	ecXMLResponse(w, "DescribeCacheParameterGroups", b.String(), sim.RequestID(r.Context()))
}

func handleECDeleteParamGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("CacheParameterGroupName")
	if _, ok := ecParamGroups.Get(name); !ok {
		ecErrorXML(w, "CacheParameterGroupNotFoundFault", "Cache parameter group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ecParamGroups.Delete(name)
	ecXMLResponse(w, "DeleteCacheParameterGroup", "", sim.RequestID(r.Context()))
}

// ecMutateTags resolves an ElastiCache ARN (cluster, replication
// group, cache subnet group, or cache parameter group) and applies fn
// to that resource's tag map, persisting the change. It returns the
// resulting tag map and whether the resource was found.
func ecMutateTags(arn string, fn func(map[string]string)) (map[string]string, bool) {
	for _, c := range ecClusters.List() {
		if c.ARN == arn {
			ecClusters.Update(c.CacheClusterId, func(cc *ECCluster) {
				if cc.Tags == nil {
					cc.Tags = map[string]string{}
				}
				fn(cc.Tags)
			})
			updated, _ := ecClusters.Get(c.CacheClusterId)
			return updated.Tags, true
		}
	}
	for _, g := range ecReplGroups.List() {
		if g.ARN == arn {
			ecReplGroups.Update(g.ReplicationGroupId, func(gg *ECReplicationGroup) {
				if gg.Tags == nil {
					gg.Tags = map[string]string{}
				}
				fn(gg.Tags)
			})
			updated, _ := ecReplGroups.Get(g.ReplicationGroupId)
			return updated.Tags, true
		}
	}
	for _, g := range ecSubnetGrps.List() {
		if g.ARN == arn {
			ecSubnetGrps.Update(g.Name, func(gg *ECSubnetGroup) {
				if gg.Tags == nil {
					gg.Tags = map[string]string{}
				}
				fn(gg.Tags)
			})
			updated, _ := ecSubnetGrps.Get(g.Name)
			return updated.Tags, true
		}
	}
	for _, g := range ecParamGroups.List() {
		if g.ARN == arn {
			ecParamGroups.Update(g.Name, func(gg *ECParameterGroup) {
				if gg.Tags == nil {
					gg.Tags = map[string]string{}
				}
				fn(gg.Tags)
			})
			updated, _ := ecParamGroups.Get(g.Name)
			return updated.Tags, true
		}
	}
	for _, s := range ecSnapshots.List() {
		if s.ARN == arn {
			ecSnapshots.Update(s.SnapshotName, func(ss *ECSnapshot) {
				if ss.Tags == nil {
					ss.Tags = map[string]string{}
				}
				fn(ss.Tags)
			})
			updated, _ := ecSnapshots.Get(s.SnapshotName)
			return updated.Tags, true
		}
	}
	for _, u := range ecUsers.List() {
		if u.ARN == arn {
			ecUsers.Update(u.UserId, func(uu *ECUser) {
				if uu.Tags == nil {
					uu.Tags = map[string]string{}
				}
				fn(uu.Tags)
			})
			updated, _ := ecUsers.Get(u.UserId)
			return updated.Tags, true
		}
	}
	for _, g := range ecUserGroups.List() {
		if g.ARN == arn {
			ecUserGroups.Update(g.UserGroupId, func(gg *ECUserGroup) {
				if gg.Tags == nil {
					gg.Tags = map[string]string{}
				}
				fn(gg.Tags)
			})
			updated, _ := ecUserGroups.Get(g.UserGroupId)
			return updated.Tags, true
		}
	}
	return nil, false
}

func ecRenderTagList(tags map[string]string) string {
	var b strings.Builder
	b.WriteString("<TagList>")
	for k, v := range tags {
		fmt.Fprintf(&b, "<Tag><Key>%s</Key><Value>%s</Value></Tag>", xmlEscape(k), xmlEscape(v))
	}
	b.WriteString("</TagList>")
	return b.String()
}

func handleECAddTags(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceName")
	tags, ok := ecMutateTags(arn, func(m map[string]string) {
		for n := 1; n <= 50; n++ {
			k := r.FormValue(fmt.Sprintf("Tags.Tag.%d.Key", n))
			v := r.FormValue(fmt.Sprintf("Tags.Tag.%d.Value", n))
			if k == "" {
				break
			}
			m[k] = v
		}
	})
	if !ok {
		ecErrorXML(w, "CacheClusterNotFound", "Resource not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ecXMLResponse(w, "AddTagsToResource", ecRenderTagList(tags), sim.RequestID(r.Context()))
}

func handleECListTags(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceName")
	tags, ok := ecLookupTags(arn)
	if !ok {
		ecErrorXML(w, "CacheClusterNotFound", "Resource not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ecXMLResponse(w, "ListTagsForResource", ecRenderTagList(tags), sim.RequestID(r.Context()))
}

// ecLookupTags resolves an ElastiCache ARN to its tag map without
// mutating it.
func ecLookupTags(arn string) (map[string]string, bool) {
	for _, c := range ecClusters.List() {
		if c.ARN == arn {
			return c.Tags, true
		}
	}
	for _, g := range ecReplGroups.List() {
		if g.ARN == arn {
			return g.Tags, true
		}
	}
	for _, g := range ecSubnetGrps.List() {
		if g.ARN == arn {
			return g.Tags, true
		}
	}
	for _, g := range ecParamGroups.List() {
		if g.ARN == arn {
			return g.Tags, true
		}
	}
	for _, s := range ecSnapshots.List() {
		if s.ARN == arn {
			return s.Tags, true
		}
	}
	for _, u := range ecUsers.List() {
		if u.ARN == arn {
			return u.Tags, true
		}
	}
	for _, g := range ecUserGroups.List() {
		if g.ARN == arn {
			return g.Tags, true
		}
	}
	return nil, false
}

func handleECRemoveTags(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceName")
	tags, ok := ecMutateTags(arn, func(m map[string]string) {
		for n := 1; n <= 50; n++ {
			k := r.FormValue(fmt.Sprintf("TagKeys.member.%d", n))
			if k == "" {
				break
			}
			delete(m, k)
		}
	})
	if !ok {
		ecErrorXML(w, "CacheClusterNotFound", "Resource not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ecXMLResponse(w, "RemoveTagsFromResource", ecRenderTagList(tags), sim.RequestID(r.Context()))
}

// ecDefaultEngineVersion returns the engine's current GA major
// version when CreateCacheCluster omits EngineVersion. Real
// ElastiCache populates the default server-side; the
// terraform-provider-aws resource captures the resolved value into
// state, so an empty echo produces drift on next plan.
func ecDefaultEngineVersion(engine string) string {
	switch engine {
	case "redis":
		return "7.1"
	case "valkey":
		return "8.0"
	case "memcached":
		return "1.6.22"
	}
	return ""
}

// Snapshots

func renderECSnapshot(s ECSnapshot) string {
	var b strings.Builder
	b.WriteString("<Snapshot>")
	fmt.Fprintf(&b, "<SnapshotName>%s</SnapshotName>", xmlEscape(s.SnapshotName))
	if s.ReplicationGroupId != "" {
		fmt.Fprintf(&b, "<ReplicationGroupId>%s</ReplicationGroupId>", xmlEscape(s.ReplicationGroupId))
	}
	if s.CacheClusterId != "" {
		fmt.Fprintf(&b, "<CacheClusterId>%s</CacheClusterId>", xmlEscape(s.CacheClusterId))
	}
	fmt.Fprintf(&b, "<SnapshotStatus>%s</SnapshotStatus>", xmlEscape(s.SnapshotStatus))
	fmt.Fprintf(&b, "<SnapshotSource>%s</SnapshotSource>", xmlEscape(s.SnapshotSource))
	fmt.Fprintf(&b, "<CacheNodeType>%s</CacheNodeType>", xmlEscape(s.CacheNodeType))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(s.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(s.EngineVersion))
	fmt.Fprintf(&b, "<NumCacheNodes>%d</NumCacheNodes>", s.NumCacheNodes)
	fmt.Fprintf(&b, "<Port>%d</Port>", s.Port)
	fmt.Fprintf(&b, "<SnapshotRetentionLimit>%d</SnapshotRetentionLimit>", s.SnapshotRetentionLimit)
	if s.SnapshotWindow != "" {
		fmt.Fprintf(&b, "<SnapshotWindow>%s</SnapshotWindow>", xmlEscape(s.SnapshotWindow))
	}
	if s.CacheClusterCreateTime != "" {
		fmt.Fprintf(&b, "<CacheClusterCreateTime>%s</CacheClusterCreateTime>", xmlEscape(s.CacheClusterCreateTime))
	}
	fmt.Fprintf(&b, "<ARN>%s</ARN>", xmlEscape(s.ARN))
	b.WriteString("<NodeSnapshots>")
	fmt.Fprintf(&b, "<NodeSnapshot><CacheNodeId>0001</CacheNodeId><CacheSize></CacheSize><SnapshotCreateTime>%s</SnapshotCreateTime></NodeSnapshot>",
		xmlEscape(time.Now().UTC().Format(time.RFC3339)))
	b.WriteString("</NodeSnapshots>")
	b.WriteString("</Snapshot>")
	return b.String()
}

func handleECCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("SnapshotName")
	if name == "" {
		ecErrorXML(w, "MissingParameter", "SnapshotName is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := ecSnapshots.Get(name); ok {
		ecErrorXML(w, "SnapshotAlreadyExistsFault", "Snapshot already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	s := ECSnapshot{
		SnapshotName:   name,
		SnapshotStatus: "available",
		SnapshotSource: "manual",
		Port:           6379,
		Tags:           parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	if cid := r.FormValue("CacheClusterId"); cid != "" {
		c, ok := ecClusters.Get(cid)
		if !ok {
			ecErrorXML(w, "CacheClusterNotFound", fmt.Sprintf("Cache cluster %q not found", cid), http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
		s.CacheClusterId = cid
		s.CacheNodeType = c.CacheNodeType
		s.Engine = c.Engine
		s.EngineVersion = c.EngineVersion
		s.NumCacheNodes = c.NumCacheNodes
		s.Port = c.Port
		s.CacheClusterCreateTime = c.CacheClusterCreateTime
	} else if rgid := r.FormValue("ReplicationGroupId"); rgid != "" {
		g, ok := ecReplGroups.Get(rgid)
		if !ok {
			ecErrorXML(w, "ReplicationGroupNotFoundFault", fmt.Sprintf("Replication group %q not found", rgid), http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
		s.ReplicationGroupId = rgid
		s.CacheNodeType = g.CacheNodeType
		s.Engine = g.Engine
		s.NumCacheNodes = len(g.MemberClusters)
		s.Port = g.ConfigEndpointPort
		s.SnapshotRetentionLimit = g.SnapshotRetentionLimit
		s.SnapshotWindow = g.SnapshotWindow
		s.CacheClusterCreateTime = g.CreateTime
	} else {
		ecErrorXML(w, "InvalidParameterCombination", "CacheClusterId or ReplicationGroupId is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if s.EngineVersion == "" {
		s.EngineVersion = ecDefaultEngineVersion(s.Engine)
	}
	s.ARN = ecSnapshotARN(name)
	ecSnapshots.Put(name, s)
	ecXMLResponse(w, "CreateSnapshot", renderECSnapshot(s), sim.RequestID(r.Context()))
}

func handleECDescribeSnapshots(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("SnapshotName")
	wantCluster := r.FormValue("CacheClusterId")
	wantRG := r.FormValue("ReplicationGroupId")
	var b strings.Builder
	b.WriteString("<Snapshots>")
	matched := false
	for _, s := range ecSnapshots.List() {
		if wanted != "" && s.SnapshotName != wanted {
			continue
		}
		if wantCluster != "" && s.CacheClusterId != wantCluster {
			continue
		}
		if wantRG != "" && s.ReplicationGroupId != wantRG {
			continue
		}
		matched = true
		b.WriteString(renderECSnapshot(s))
	}
	if wanted != "" && !matched {
		ecErrorXML(w, "SnapshotNotFoundFault", fmt.Sprintf("Snapshot %q not found", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</Snapshots>")
	ecXMLResponse(w, "DescribeSnapshots", b.String(), sim.RequestID(r.Context()))
}

func handleECDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("SnapshotName")
	s, ok := ecSnapshots.Get(name)
	if !ok {
		ecErrorXML(w, "SnapshotNotFoundFault", "Snapshot not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	s.SnapshotStatus = "deleting"
	ecSnapshots.Delete(name)
	ecXMLResponse(w, "DeleteSnapshot", renderECSnapshot(s), sim.RequestID(r.Context()))
}

func handleECCopySnapshot(w http.ResponseWriter, r *http.Request) {
	src := r.FormValue("SourceSnapshotName")
	target := r.FormValue("TargetSnapshotName")
	if target == "" {
		ecErrorXML(w, "MissingParameter", "TargetSnapshotName is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	s, ok := ecSnapshots.Get(src)
	if !ok {
		ecErrorXML(w, "SnapshotNotFoundFault", fmt.Sprintf("Snapshot %q not found", src), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if _, ok := ecSnapshots.Get(target); ok {
		ecErrorXML(w, "SnapshotAlreadyExistsFault", "Snapshot already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	s.SnapshotName = target
	s.ARN = ecSnapshotARN(target)
	s.SnapshotStatus = "available"
	s.Tags = parseAWSQueryTagMap(r, "Tags.Tag")
	ecSnapshots.Put(target, s)
	ecXMLResponse(w, "CopySnapshot", renderECSnapshot(s), sim.RequestID(r.Context()))
}

// Users + User Groups (ElastiCache RBAC)

// renderECUserBody emits the User shape's members without any wrapper
// element. CreateUser / ModifyUser / DeleteUser return the User shape
// flattened directly into their Result; DescribeUsers wraps each entry
// in a UserList <member> element.
func renderECUserBody(u ECUser) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<UserId>%s</UserId>", xmlEscape(u.UserId))
	fmt.Fprintf(&b, "<UserName>%s</UserName>", xmlEscape(u.UserName))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(u.Status))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(u.Engine))
	if u.MinimumEngineVersion != "" {
		fmt.Fprintf(&b, "<MinimumEngineVersion>%s</MinimumEngineVersion>", xmlEscape(u.MinimumEngineVersion))
	}
	fmt.Fprintf(&b, "<AccessString>%s</AccessString>", xmlEscape(u.AccessString))
	fmt.Fprintf(&b, "<ARN>%s</ARN>", xmlEscape(u.ARN))
	b.WriteString("<UserGroupIds>")
	for _, id := range u.UserGroupIds {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(id))
	}
	b.WriteString("</UserGroupIds>")
	authType := "password"
	if u.NoPasswordRequired {
		authType = "no-password"
	}
	fmt.Fprintf(&b, "<Authentication><Type>%s</Type><PasswordCount>%d</PasswordCount></Authentication>", authType, u.PasswordCount)
	return b.String()
}

func ecParsePasswordCount(r *http.Request) int {
	n := 0
	for i := 1; i <= 2; i++ {
		if r.FormValue(fmt.Sprintf("Passwords.member.%d", i)) != "" {
			n++
		}
	}
	return n
}

func handleECCreateUser(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("UserId")
	if id == "" {
		ecErrorXML(w, "MissingParameter", "UserId is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := ecUsers.Get(id); ok {
		ecErrorXML(w, "UserAlreadyExistsFault", "User already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	engine := strings.ToLower(r.FormValue("Engine"))
	if engine == "" {
		engine = "redis"
	}
	u := ECUser{
		UserId:               id,
		UserName:             r.FormValue("UserName"),
		Status:               "active",
		Engine:               engine,
		MinimumEngineVersion: "6.0",
		AccessString:         r.FormValue("AccessString"),
		NoPasswordRequired:   strings.EqualFold(r.FormValue("NoPasswordRequired"), "true"),
		PasswordCount:        ecParsePasswordCount(r),
		ARN:                  ecUserARN(id),
		Tags:                 parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	ecUsers.Put(id, u)
	ecXMLResponse(w, "CreateUser", renderECUserBody(u), sim.RequestID(r.Context()))
}

func handleECDescribeUsers(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("UserId")
	var b strings.Builder
	b.WriteString("<Users>")
	matched := false
	for _, u := range ecUsers.List() {
		if wanted != "" && u.UserId != wanted {
			continue
		}
		matched = true
		b.WriteString("<member>")
		b.WriteString(renderECUserBody(u))
		b.WriteString("</member>")
	}
	if wanted != "" && !matched {
		ecErrorXML(w, "UserNotFoundFault", fmt.Sprintf("User %q not found", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</Users>")
	ecXMLResponse(w, "DescribeUsers", b.String(), sim.RequestID(r.Context()))
}

func handleECModifyUser(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("UserId")
	if _, ok := ecUsers.Get(id); !ok {
		ecErrorXML(w, "UserNotFoundFault", "User not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	ecUsers.Update(id, func(u *ECUser) {
		if v := r.FormValue("AccessString"); v != "" {
			u.AccessString = v
		}
		if v := r.FormValue("AppendAccessString"); v != "" {
			u.AccessString = strings.TrimSpace(u.AccessString + " " + v)
		}
		if v := r.FormValue("NoPasswordRequired"); v != "" {
			u.NoPasswordRequired = strings.EqualFold(v, "true")
		}
		if n := ecParsePasswordCount(r); n > 0 {
			u.PasswordCount = n
		}
	})
	updated, _ := ecUsers.Get(id)
	ecXMLResponse(w, "ModifyUser", renderECUserBody(updated), sim.RequestID(r.Context()))
}

func handleECDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("UserId")
	u, ok := ecUsers.Get(id)
	if !ok {
		ecErrorXML(w, "UserNotFoundFault", "User not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	u.Status = "deleting"
	ecUsers.Delete(id)
	ecXMLResponse(w, "DeleteUser", renderECUserBody(u), sim.RequestID(r.Context()))
}

// renderECUserGroupBody emits the UserGroup shape's members without any
// wrapper. CreateUserGroup / ModifyUserGroup / DeleteUserGroup return
// the UserGroup shape flattened directly into their Result;
// DescribeUserGroups wraps each entry in a UserGroupList <member>.
func renderECUserGroupBody(g ECUserGroup) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<UserGroupId>%s</UserGroupId>", xmlEscape(g.UserGroupId))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(g.Status))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(g.Engine))
	if g.MinimumEngineVersion != "" {
		fmt.Fprintf(&b, "<MinimumEngineVersion>%s</MinimumEngineVersion>", xmlEscape(g.MinimumEngineVersion))
	}
	b.WriteString("<UserIds>")
	for _, id := range g.UserIds {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(id))
	}
	b.WriteString("</UserIds>")
	b.WriteString("<ReplicationGroups>")
	for _, id := range g.ReplicationGroups {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(id))
	}
	b.WriteString("</ReplicationGroups>")
	fmt.Fprintf(&b, "<ARN>%s</ARN>", xmlEscape(g.ARN))
	return b.String()
}

func ecParseUserIds(r *http.Request, field string) []string {
	var ids []string
	for n := 1; n <= 100; n++ {
		v := r.FormValue(fmt.Sprintf("%s.member.%d", field, n))
		if v == "" {
			break
		}
		ids = append(ids, v)
	}
	return ids
}

func handleECCreateUserGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("UserGroupId")
	if id == "" {
		ecErrorXML(w, "MissingParameter", "UserGroupId is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := ecUserGroups.Get(id); ok {
		ecErrorXML(w, "UserGroupAlreadyExistsFault", "User group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	engine := strings.ToLower(r.FormValue("Engine"))
	if engine == "" {
		engine = "redis"
	}
	g := ECUserGroup{
		UserGroupId:          id,
		Status:               "active",
		Engine:               engine,
		MinimumEngineVersion: "6.0",
		UserIds:              ecParseUserIds(r, "UserIds"),
		ARN:                  ecUserGroupARN(id),
		Tags:                 parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	ecUserGroups.Put(id, g)
	ecXMLResponse(w, "CreateUserGroup", renderECUserGroupBody(g), sim.RequestID(r.Context()))
}

func handleECDescribeUserGroups(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("UserGroupId")
	var b strings.Builder
	b.WriteString("<UserGroups>")
	matched := false
	for _, g := range ecUserGroups.List() {
		if wanted != "" && g.UserGroupId != wanted {
			continue
		}
		matched = true
		b.WriteString("<member>")
		b.WriteString(renderECUserGroupBody(g))
		b.WriteString("</member>")
	}
	if wanted != "" && !matched {
		ecErrorXML(w, "UserGroupNotFoundFault", fmt.Sprintf("User group %q not found", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</UserGroups>")
	ecXMLResponse(w, "DescribeUserGroups", b.String(), sim.RequestID(r.Context()))
}

func handleECModifyUserGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("UserGroupId")
	if _, ok := ecUserGroups.Get(id); !ok {
		ecErrorXML(w, "UserGroupNotFoundFault", "User group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	add := ecParseUserIds(r, "UserIdsToAdd")
	remove := ecParseUserIds(r, "UserIdsToRemove")
	ecUserGroups.Update(id, func(g *ECUserGroup) {
		for _, a := range add {
			found := false
			for _, e := range g.UserIds {
				if e == a {
					found = true
					break
				}
			}
			if !found {
				g.UserIds = append(g.UserIds, a)
			}
		}
		if len(remove) > 0 {
			kept := g.UserIds[:0:0]
			for _, e := range g.UserIds {
				drop := false
				for _, rm := range remove {
					if e == rm {
						drop = true
						break
					}
				}
				if !drop {
					kept = append(kept, e)
				}
			}
			g.UserIds = kept
		}
	})
	updated, _ := ecUserGroups.Get(id)
	ecXMLResponse(w, "ModifyUserGroup", renderECUserGroupBody(updated), sim.RequestID(r.Context()))
}

func handleECDeleteUserGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("UserGroupId")
	g, ok := ecUserGroups.Get(id)
	if !ok {
		ecErrorXML(w, "UserGroupNotFoundFault", "User group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	g.Status = "deleting"
	ecUserGroups.Delete(id)
	ecXMLResponse(w, "DeleteUserGroup", renderECUserGroupBody(g), sim.RequestID(r.Context()))
}

// Parameter detail

// ecDefaultParameters returns a representative slice of engine-default
// parameters keyed by name. Real ElastiCache exposes hundreds; the sim
// returns the small, stable subset clients commonly read back.
func ecDefaultParameters() []struct {
	Name, Value, DataType, Description, Source, ChangeType string
} {
	return []struct {
		Name, Value, DataType, Description, Source, ChangeType string
	}{
		{"maxmemory-policy", "volatile-lru", "string", "Max memory policy", "system", "immediate"},
		{"timeout", "0", "integer", "Close connection after a client is idle for N seconds", "system", "immediate"},
		{"databases", "16", "integer", "Set the number of databases", "system", "requires-reboot"},
	}
}

func renderECParameter(name, value, dataType, description, source, changeType string) string {
	var b strings.Builder
	b.WriteString("<Parameter>")
	fmt.Fprintf(&b, "<ParameterName>%s</ParameterName>", xmlEscape(name))
	fmt.Fprintf(&b, "<ParameterValue>%s</ParameterValue>", xmlEscape(value))
	fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(description))
	fmt.Fprintf(&b, "<Source>%s</Source>", xmlEscape(source))
	fmt.Fprintf(&b, "<DataType>%s</DataType>", xmlEscape(dataType))
	fmt.Fprintf(&b, "<IsModifiable>%t</IsModifiable>", true)
	fmt.Fprintf(&b, "<MinimumEngineVersion>%s</MinimumEngineVersion>", "6.0")
	fmt.Fprintf(&b, "<ChangeType>%s</ChangeType>", xmlEscape(changeType))
	b.WriteString("</Parameter>")
	return b.String()
}

func handleECDescribeParameters(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("CacheParameterGroupName")
	g, ok := ecParamGroups.Get(name)
	if !ok {
		ecErrorXML(w, "CacheParameterGroupNotFoundFault", "Cache parameter group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	var b strings.Builder
	b.WriteString("<Parameters>")
	for _, p := range ecDefaultParameters() {
		value := p.Value
		source := p.Source
		if g.Params != nil {
			if v, ok := g.Params[p.Name]; ok {
				value = v
				source = "user"
			}
		}
		b.WriteString(renderECParameter(p.Name, value, p.DataType, p.Description, source, p.ChangeType))
	}
	b.WriteString("</Parameters>")
	b.WriteString("<CacheNodeTypeSpecificParameters></CacheNodeTypeSpecificParameters>")
	ecXMLResponse(w, "DescribeCacheParameters", b.String(), sim.RequestID(r.Context()))
}

func ecParseParameterNameValues(r *http.Request) map[string]string {
	out := map[string]string{}
	for n := 1; n <= 100; n++ {
		k := r.FormValue(fmt.Sprintf("ParameterNameValues.ParameterNameValue.%d.ParameterName", n))
		if k == "" {
			k = r.FormValue(fmt.Sprintf("ParameterNameValues.member.%d.ParameterName", n))
		}
		if k == "" {
			break
		}
		v := r.FormValue(fmt.Sprintf("ParameterNameValues.ParameterNameValue.%d.ParameterValue", n))
		if v == "" {
			v = r.FormValue(fmt.Sprintf("ParameterNameValues.member.%d.ParameterValue", n))
		}
		out[k] = v
	}
	return out
}

func handleECModifyParameters(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("CacheParameterGroupName")
	if _, ok := ecParamGroups.Get(name); !ok {
		ecErrorXML(w, "CacheParameterGroupNotFoundFault", "Cache parameter group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	updates := ecParseParameterNameValues(r)
	ecParamGroups.Update(name, func(g *ECParameterGroup) {
		if g.Params == nil {
			g.Params = map[string]string{}
		}
		for k, v := range updates {
			g.Params[k] = v
		}
	})
	ecXMLResponse(w, "ModifyCacheParameterGroup",
		fmt.Sprintf("<CacheParameterGroupName>%s</CacheParameterGroupName>", xmlEscape(name)),
		sim.RequestID(r.Context()))
}

func handleECResetParameters(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("CacheParameterGroupName")
	if _, ok := ecParamGroups.Get(name); !ok {
		ecErrorXML(w, "CacheParameterGroupNotFoundFault", "Cache parameter group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	resetAll := strings.EqualFold(r.FormValue("ResetAllParameters"), "true")
	reset := ecParseParameterNameValues(r)
	ecParamGroups.Update(name, func(g *ECParameterGroup) {
		if resetAll {
			g.Params = map[string]string{}
			return
		}
		if g.Params == nil {
			return
		}
		for k := range reset {
			delete(g.Params, k)
		}
	})
	ecXMLResponse(w, "ResetCacheParameterGroup",
		fmt.Sprintf("<CacheParameterGroupName>%s</CacheParameterGroupName>", xmlEscape(name)),
		sim.RequestID(r.Context()))
}

func handleECDescribeEngineDefaultParameters(w http.ResponseWriter, r *http.Request) {
	family := r.FormValue("CacheParameterGroupFamily")
	if family == "" {
		family = "redis7"
	}
	var b strings.Builder
	b.WriteString("<EngineDefaults>")
	fmt.Fprintf(&b, "<CacheParameterGroupFamily>%s</CacheParameterGroupFamily>", xmlEscape(family))
	b.WriteString("<Parameters>")
	for _, p := range ecDefaultParameters() {
		b.WriteString(renderECParameter(p.Name, p.Value, p.DataType, p.Description, "system", p.ChangeType))
	}
	b.WriteString("</Parameters>")
	b.WriteString("<CacheNodeTypeSpecificParameters></CacheNodeTypeSpecificParameters>")
	b.WriteString("</EngineDefaults>")
	ecXMLResponse(w, "DescribeEngineDefaultParameters", b.String(), sim.RequestID(r.Context()))
}

// Events

func handleECDescribeEvents(w http.ResponseWriter, r *http.Request) {
	sourceType := r.FormValue("SourceType")
	if sourceType == "" {
		sourceType = "cache-cluster"
	}
	srcID := r.FormValue("SourceIdentifier")
	var b strings.Builder
	b.WriteString("<Events>")
	now := time.Now().UTC().Format(time.RFC3339)
	emit := func(id, msg string) {
		b.WriteString("<Event>")
		fmt.Fprintf(&b, "<SourceIdentifier>%s</SourceIdentifier>", xmlEscape(id))
		fmt.Fprintf(&b, "<SourceType>%s</SourceType>", xmlEscape(sourceType))
		fmt.Fprintf(&b, "<Message>%s</Message>", xmlEscape(msg))
		fmt.Fprintf(&b, "<Date>%s</Date>", xmlEscape(now))
		b.WriteString("</Event>")
	}
	switch sourceType {
	case "cache-cluster":
		for _, c := range ecClusters.List() {
			if srcID != "" && c.CacheClusterId != srcID {
				continue
			}
			emit(c.CacheClusterId, "Cache cluster created")
		}
	case "replication-group":
		for _, g := range ecReplGroups.List() {
			if srcID != "" && g.ReplicationGroupId != srcID {
				continue
			}
			emit(g.ReplicationGroupId, "Replication group created")
		}
	}
	b.WriteString("</Events>")
	ecXMLResponse(w, "DescribeEvents", b.String(), sim.RequestID(r.Context()))
}

// Cache engine versions

func handleECDescribeCacheEngineVersions(w http.ResponseWriter, r *http.Request) {
	wantEngine := strings.ToLower(r.FormValue("Engine"))
	versions := []struct {
		Engine, Version, Family, Desc string
	}{
		{"redis", "7.1", "redis7", "Redis"},
		{"redis", "6.2", "redis6.x", "Redis"},
		{"valkey", "8.0", "valkey8", "Valkey"},
		{"memcached", "1.6.22", "memcached1.6", "memcached"},
	}
	var b strings.Builder
	b.WriteString("<CacheEngineVersions>")
	for _, v := range versions {
		if wantEngine != "" && v.Engine != wantEngine {
			continue
		}
		b.WriteString("<CacheEngineVersion>")
		fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(v.Engine))
		fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(v.Version))
		fmt.Fprintf(&b, "<CacheParameterGroupFamily>%s</CacheParameterGroupFamily>", xmlEscape(v.Family))
		fmt.Fprintf(&b, "<CacheEngineDescription>%s</CacheEngineDescription>", xmlEscape(v.Desc))
		fmt.Fprintf(&b, "<CacheEngineVersionDescription>%s %s</CacheEngineVersionDescription>", xmlEscape(v.Desc), xmlEscape(v.Version))
		b.WriteString("</CacheEngineVersion>")
	}
	b.WriteString("</CacheEngineVersions>")
	ecXMLResponse(w, "DescribeCacheEngineVersions", b.String(), sim.RequestID(r.Context()))
}

// Reserved cache nodes + offerings

func handleECDescribeReservedCacheNodes(w http.ResponseWriter, r *http.Request) {
	// An account that has bought nothing has an empty list, which is what AWS
	// answers — not an error. One that has bought gets what it bought back.
	var b strings.Builder
	b.WriteString("<ReservedCacheNodes>")
	if id := r.FormValue("ReservedCacheNodeId"); id != "" {
		if n, ok := ecReservedNodes.Get(id); ok {
			ecReservedNodeXML(&b, n)
		}
	} else {
		nodes := ecReservedNodes.List()
		sort.Slice(nodes, func(i, j int) bool {
			return nodes[i].ReservedCacheNodeId < nodes[j].ReservedCacheNodeId
		})
		wantOffering := r.FormValue("ReservedCacheNodesOfferingId")
		wantType := r.FormValue("CacheNodeType")
		for _, n := range nodes {
			if wantOffering != "" && n.OfferingId != wantOffering {
				continue
			}
			if wantType != "" && n.CacheNodeType != wantType {
				continue
			}
			ecReservedNodeXML(&b, n)
		}
	}
	b.WriteString("</ReservedCacheNodes>")
	ecXMLResponse(w, "DescribeReservedCacheNodes", b.String(), sim.RequestID(r.Context()))
}

func handleECDescribeReservedCacheNodesOfferings(w http.ResponseWriter, r *http.Request) {
	wantID := r.FormValue("ReservedCacheNodesOfferingId")
	wantType := r.FormValue("CacheNodeType")
	var b strings.Builder
	b.WriteString("<ReservedCacheNodesOfferings>")
	for _, o := range ecReservedCacheNodesOfferings {
		if wantID != "" && o.Id != wantID {
			continue
		}
		if wantType != "" && o.CacheNodeType != wantType {
			continue
		}
		b.WriteString("<ReservedCacheNodesOffering>")
		fmt.Fprintf(&b, "<ReservedCacheNodesOfferingId>%s</ReservedCacheNodesOfferingId>", xmlEscape(o.Id))
		fmt.Fprintf(&b, "<CacheNodeType>%s</CacheNodeType>", xmlEscape(o.CacheNodeType))
		fmt.Fprintf(&b, "<Duration>%d</Duration>", o.Duration)
		fmt.Fprintf(&b, "<FixedPrice>%s</FixedPrice>", xmlEscape(o.FixedPrice))
		fmt.Fprintf(&b, "<UsagePrice>%s</UsagePrice>", xmlEscape(o.UsagePrice))
		fmt.Fprintf(&b, "<ProductDescription>%s</ProductDescription>", xmlEscape(o.ProductDescription))
		fmt.Fprintf(&b, "<OfferingType>%s</OfferingType>", xmlEscape(o.OfferingType))
		fmt.Fprintf(&b, "<RecurringCharges><RecurringCharge><RecurringChargeAmount>%s</RecurringChargeAmount><RecurringChargeFrequency>%s</RecurringChargeFrequency></RecurringCharge></RecurringCharges>",
			xmlEscape(o.RecurringAmount), xmlEscape(o.RecurringFrequency))
		b.WriteString("</ReservedCacheNodesOffering>")
	}
	b.WriteString("</ReservedCacheNodesOfferings>")
	ecXMLResponse(w, "DescribeReservedCacheNodesOfferings", b.String(), sim.RequestID(r.Context()))
}

// ecReservedNodeXML renders one purchased reservation.
func ecReservedNodeXML(b *strings.Builder, n ECReservedCacheNode) {
	b.WriteString("<ReservedCacheNode>")
	fmt.Fprintf(b, "<ReservedCacheNodeId>%s</ReservedCacheNodeId>", xmlEscape(n.ReservedCacheNodeId))
	fmt.Fprintf(b, "<ReservedCacheNodesOfferingId>%s</ReservedCacheNodesOfferingId>", xmlEscape(n.OfferingId))
	fmt.Fprintf(b, "<CacheNodeType>%s</CacheNodeType>", xmlEscape(n.CacheNodeType))
	fmt.Fprintf(b, "<StartTime>%s</StartTime>", xmlEscape(n.StartTime))
	fmt.Fprintf(b, "<Duration>%d</Duration>", n.Duration)
	fmt.Fprintf(b, "<FixedPrice>%s</FixedPrice>", xmlEscape(n.FixedPrice))
	fmt.Fprintf(b, "<UsagePrice>%s</UsagePrice>", xmlEscape(n.UsagePrice))
	fmt.Fprintf(b, "<CacheNodeCount>%d</CacheNodeCount>", n.CacheNodeCount)
	fmt.Fprintf(b, "<ProductDescription>%s</ProductDescription>", xmlEscape(n.ProductDescription))
	fmt.Fprintf(b, "<OfferingType>%s</OfferingType>", xmlEscape(n.OfferingType))
	fmt.Fprintf(b, "<State>%s</State>", xmlEscape(n.State))
	fmt.Fprintf(b, "<RecurringCharges><RecurringCharge><RecurringChargeAmount>%s</RecurringChargeAmount><RecurringChargeFrequency>%s</RecurringChargeFrequency></RecurringCharge></RecurringCharges>",
		xmlEscape(n.RecurringAmount), xmlEscape(n.RecurringFrequency))
	fmt.Fprintf(b, "<ReservationARN>arn:aws:elasticache:%s:%s:reserved-instance:%s</ReservationARN>",
		awsRegion(), awsAccountID(), xmlEscape(n.ReservedCacheNodeId))
	b.WriteString("</ReservedCacheNode>")
}

// Service updates

func handleECDescribeServiceUpdates(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("<ServiceUpdates>")
	b.WriteString("<ServiceUpdate>")
	b.WriteString("<ServiceUpdateName>elasticache-20240101-001</ServiceUpdateName>")
	fmt.Fprintf(&b, "<ServiceUpdateReleaseDate>%s</ServiceUpdateReleaseDate>", time.Now().UTC().Add(-720*time.Hour).Format(time.RFC3339))
	fmt.Fprintf(&b, "<ServiceUpdateEndDate>%s</ServiceUpdateEndDate>", time.Now().UTC().Add(720*time.Hour).Format(time.RFC3339))
	b.WriteString("<ServiceUpdateSeverity>important</ServiceUpdateSeverity>")
	b.WriteString("<ServiceUpdateStatus>available</ServiceUpdateStatus>")
	b.WriteString("<ServiceUpdateDescription>Security and reliability update</ServiceUpdateDescription>")
	b.WriteString("<ServiceUpdateType>security-update</ServiceUpdateType>")
	b.WriteString("<Engine>redis</Engine>")
	b.WriteString("<AutoUpdateAfterRecommendedApplyByDate>true</AutoUpdateAfterRecommendedApplyByDate>")
	b.WriteString("</ServiceUpdate>")
	b.WriteString("</ServiceUpdates>")
	ecXMLResponse(w, "DescribeServiceUpdates", b.String(), sim.RequestID(r.Context()))
}

// Cache security groups (EC2-Classic; empty in a VPC-only sim)

func handleECDescribeCacheSecurityGroups(w http.ResponseWriter, r *http.Request) {
	// CacheSecurityGroups are an EC2-Classic concept. New accounts are
	// VPC-only and hold none; AWS returns an empty list.
	ecXMLResponse(w, "DescribeCacheSecurityGroups", "<CacheSecurityGroups></CacheSecurityGroups>", sim.RequestID(r.Context()))
}
