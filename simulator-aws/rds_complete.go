package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// rds_complete.go drives the RDS slice to full operation coverage with
// the final 14 awsQuery operations the lifecycle/control plane was
// missing: custom DB engine versions, DB recommendations, DB snapshot
// tenant databases, Serverless v2 platform versions, valid DB instance
// modifications, Serverless v1 cluster capacity, option-group option
// membership, automated-backups cross-region replication, and the
// global-cluster / read-replica switchover relationships.
//
// Every handler does faithful CRUD on a real sim.Store (or mutates an
// existing RDS store) and renders the exact awsQuery XML shape the RDS
// smithy model declares. Renderers shared across handlers are factored
// into small helpers to avoid clone-and-edit drift.

// RDSCustomEngineVersion models a custom DB engine version (CEV) — a
// named (engine, version) pair built from an installation-files
// manifest. The build is not simulated; Status settles to "available"
// inline on Create, matching the sim's instance/cluster convention.
type RDSCustomEngineVersion struct {
	Engine string
	// CustomDBEngineVersionId is the identifier AWS assigns and carries as the
	// third part of the version's ARN. It has no request parameter — a caller
	// names a custom engine version by its engine and version — so it exists
	// only to make the ARN the one AWS publishes.
	CustomDBEngineVersionId string
	EngineVersion           string
	DBParameterGroupFamily  string
	Description             string
	Status                  string // pending-validation | available | failed | inactive
	KMSKeyId                string
	ImageId                 string
	CreateTime              string
	ARN                     string
	Tags                    map[string]string
}

// RDSRecommendation models an Amazon RDS recommendation against a
// resource. ModifyDBRecommendation flips its Status (the only
// user-mutable field on the recommendation lifecycle).
type RDSRecommendation struct {
	RecommendationId string
	ResourceArn      string
	Status           string // active | pending | resolved | dismissed
	Severity         string
	Category         string
	Source           string
	TypeId           string
	Recommendation   string
	Description      string
	CreatedTime      string
	UpdatedTime      string
}

// RDSAutomatedBackupReplication models a cross-region automated-backups
// replication target keyed by the source DB instance ARN. Started by
// StartDBInstanceAutomatedBackupsReplication, removed by Stop.
type RDSAutomatedBackupReplication struct {
	SourceDBInstanceArn   string
	BackupRetentionPeriod int
	KmsKeyId              string
	Status                string // pending | replicating | stopped
}

var (
	rdsCustomEngineVersions    sim.Store[RDSCustomEngineVersion]
	rdsRecommendations         sim.Store[RDSRecommendation]
	rdsOptionGroupOptions      sim.Store[RDSOptionGroupOptions]
	rdsClusterCapacities       sim.Store[RDSClusterCapacity]
	rdsAutomatedBackupReplicas sim.Store[RDSAutomatedBackupReplication]
)

// RDSOptionGroupOptions holds the option names included on an option
// group (the RDSOptionGroup type in rds.go carries no Options field, so
// option membership lives in this side store keyed by group name).
type RDSOptionGroupOptions struct {
	OptionGroupName string
	OptionNames     []string
}

// RDSClusterCapacity holds a Serverless v1 cluster's current capacity
// (the RDSCluster type carries no capacity field). Keyed by cluster
// identifier.
type RDSClusterCapacity struct {
	DBClusterIdentifier string
	CurrentCapacity     int
}

func registerRDSComplete(r *AWSQueryRouter, srv *sim.Server) {
	rdsCustomEngineVersions = sim.MakeStore[RDSCustomEngineVersion](srv.DB(), "rds_custom_engine_versions")
	rdsRecommendations = sim.MakeStore[RDSRecommendation](srv.DB(), "rds_recommendations")
	rdsOptionGroupOptions = sim.MakeStore[RDSOptionGroupOptions](srv.DB(), "rds_option_group_options")
	rdsClusterCapacities = sim.MakeStore[RDSClusterCapacity](srv.DB(), "rds_cluster_capacities")
	rdsAutomatedBackupReplicas = sim.MakeStore[RDSAutomatedBackupReplication](srv.DB(), "rds_automated_backup_replicas")

	r.RegisterVersioned(rdsAPIVersion, "CreateCustomDBEngineVersion", handleRDSCreateCustomEngineVersion)
	r.RegisterVersioned(rdsAPIVersion, "ModifyCustomDBEngineVersion", handleRDSModifyCustomEngineVersion)
	r.RegisterVersioned(rdsAPIVersion, "DeleteCustomDBEngineVersion", handleRDSDeleteCustomEngineVersion)

	r.RegisterVersioned(rdsAPIVersion, "DescribeDBRecommendations", handleRDSDescribeRecommendations)
	r.RegisterVersioned(rdsAPIVersion, "ModifyDBRecommendation", handleRDSModifyRecommendation)

	r.RegisterVersioned(rdsAPIVersion, "DescribeDBSnapshotTenantDatabases", handleRDSDescribeSnapshotTenantDatabases)
	r.RegisterVersioned(rdsAPIVersion, "DescribeServerlessV2PlatformVersions", handleRDSDescribeServerlessV2PlatformVersions)
	r.RegisterVersioned(rdsAPIVersion, "DescribeValidDBInstanceModifications", handleRDSDescribeValidDBInstanceModifications)

	r.RegisterVersioned(rdsAPIVersion, "ModifyCurrentDBClusterCapacity", handleRDSModifyCurrentDBClusterCapacity)
	r.RegisterVersioned(rdsAPIVersion, "ModifyOptionGroup", handleRDSModifyOptionGroup)

	r.RegisterVersioned(rdsAPIVersion, "StartDBInstanceAutomatedBackupsReplication", handleRDSStartAutomatedBackupsReplication)
	r.RegisterVersioned(rdsAPIVersion, "StopDBInstanceAutomatedBackupsReplication", handleRDSStopAutomatedBackupsReplication)

	r.RegisterVersioned(rdsAPIVersion, "SwitchoverGlobalCluster", handleRDSSwitchoverGlobalCluster)
	r.RegisterVersioned(rdsAPIVersion, "SwitchoverReadReplica", handleRDSSwitchoverReadReplica)

	r.RegisterVersioned(rdsAPIVersion, "DescribeAccountAttributes", handleRDSDescribeAccountAttributes)
}

// Account attributes

// rdsAccountQuota is one entry of DescribeAccountAttributes' AccountQuotaList.
// `Used` is always derived from the simulator's own RDS resources — the same
// account state DescribeDBInstances and friends serve — so the quota report
// stays consistent with what the account actually holds.
type rdsAccountQuota struct {
	Name string
	Used int64
	Max  int64
}

// rdsMaxOverAccount returns the highest per-resource count in the account,
// which is what Amazon RDS reports as `Used` for the per-resource quotas
// ("the used value is the highest number of X for a Y in the account").
func rdsMaxOverAccount[T any](items []T, count func(T) int) int64 {
	var high int64
	for _, it := range items {
		if n := int64(count(it)); n > high {
			high = n
		}
	}
	return high
}

// rdsIsDefaultGroupName reports whether a parameter/option group name is one of
// the Amazon RDS-owned defaults. RDS reserves the `default.<family>` (parameter
// groups) and `default:<engine>-<version>` (option groups) name forms for the
// groups it creates itself, and the DBParameterGroups /
// DBClusterParameterGroups / OptionGroups quotas count only the account's own
// nondefault groups.
func rdsIsDefaultGroupName(name string) bool {
	return strings.HasPrefix(name, "default.") || strings.HasPrefix(name, "default:")
}

// rdsAccountQuotas builds the AccountQuotaList DescribeAccountAttributes
// returns. The quota names and their default maxima are the ones Amazon RDS
// documents for an account (the vendored rds Smithy model enumerates them on
// the AccountQuota shape and captures the first fifteen, in this order, in its
// DescribeAccountAttributes example); the used values are counted from the
// simulator's RDS state.
func rdsAccountQuotas() []rdsAccountQuota {
	instances := rdsInstances.List()
	var allocatedStorage int64
	for _, i := range instances {
		allocatedStorage += int64(i.AllocatedStorage)
	}
	// The ManualSnapshots / ManualClusterSnapshots quotas count only the
	// snapshots the account took itself; automated backups don't consume them.
	var manualSnapshots, manualClusterSnapshots int64
	for _, s := range rdsSnapshots.List() {
		if s.SnapshotType == "manual" {
			manualSnapshots++
		}
	}
	for _, s := range rdsClusterSnapshots.List() {
		if s.SnapshotType == "manual" {
			manualClusterSnapshots++
		}
	}
	var paramGroups, clusterParamGroups, optionGroups int64
	for _, g := range rdsParamGroups.List() {
		if !rdsIsDefaultGroupName(g.DBParameterGroupName) {
			paramGroups++
		}
	}
	for _, g := range rdsClusterParamGroups.List() {
		if !rdsIsDefaultGroupName(g.DBClusterParameterGroupName) {
			clusterParamGroups++
		}
	}
	for _, g := range rdsOptionGroups.List() {
		if !rdsIsDefaultGroupName(g.OptionGroupName) {
			optionGroups++
		}
	}
	customEndpointsPerCluster := map[string]int{}
	for _, e := range rdsClusterEndpoints.List() {
		if e.EndpointType == "CUSTOM" {
			customEndpointsPerCluster[e.DBClusterIdentifier]++
		}
	}
	var highestCustomEndpoints int64
	for _, n := range customEndpointsPerCluster {
		if int64(n) > highestCustomEndpoints {
			highestCustomEndpoints = int64(n)
		}
	}

	return []rdsAccountQuota{
		{"DBInstances", int64(len(instances)), 40},
		{"ReservedDBInstances", int64(len(rdsReservedInstances.List())), 40},
		{"AllocatedStorage", allocatedStorage, 100000},
		{"DBSecurityGroups", int64(len(rdsDBSecurityGroups.List())), 25},
		{"AuthorizationsPerDBSecurityGroup", rdsMaxOverAccount(rdsDBSecurityGroups.List(), func(g RDSDBSecurityGroup) int {
			return len(g.IPRanges) + len(g.EC2SecurityGroups)
		}), 20},
		{"DBParameterGroups", paramGroups, 50},
		{"ManualSnapshots", manualSnapshots, 100},
		{"EventSubscriptions", int64(len(rdsEventSubscriptions.List())), 20},
		{"DBSubnetGroups", int64(len(rdsSubnetGroups.List())), 50},
		{"OptionGroups", optionGroups, 20},
		{"SubnetsPerDBSubnetGroup", rdsMaxOverAccount(rdsSubnetGroups.List(), func(g RDSSubnetGroup) int {
			return len(g.SubnetIds)
		}), 20},
		{"ReadReplicasPerMaster", rdsMaxOverAccount(instances, func(i RDSInstance) int {
			return len(i.ReadReplicas)
		}), 5},
		{"DBClusters", int64(len(rdsClusters.List())), 40},
		{"DBClusterParameterGroups", clusterParamGroups, 50},
		{"DBClusterRoles", rdsMaxOverAccount(rdsClusterRoles.List(), func(s rdsRoleSet) int {
			return len(s.Roles)
		}), 5},
		{"DBInstanceRoles", rdsMaxOverAccount(rdsInstanceRoles.List(), func(s rdsRoleSet) int {
			return len(s.Roles)
		}), 5},
		{"ManualClusterSnapshots", manualClusterSnapshots, 100},
		{"CustomEndpointsPerDBCluster", highestCustomEndpoints, 5},
	}
}

// handleRDSDescribeAccountAttributes serves DescribeAccountAttributes, which
// takes no parameters and returns the account's RDS quotas with current usage.
func handleRDSDescribeAccountAttributes(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("<AccountQuotas>")
	for _, q := range rdsAccountQuotas() {
		fmt.Fprintf(&b, "<AccountQuota><AccountQuotaName>%s</AccountQuotaName><Used>%d</Used><Max>%d</Max></AccountQuota>",
			xmlEscape(q.Name), q.Used, q.Max)
	}
	b.WriteString("</AccountQuotas>")
	rdsXMLResponse(w, "DescribeAccountAttributes", b.String(), sim.RequestID(r.Context()))
}

// Custom DB engine versions

// rdsCustomEngineVersionARN builds the ARN AWS publishes for a custom engine
// version: "cev:<engine>/<version>/<id>". The identifier is the third part and
// is not optional — an ARN missing it names nothing, so a policy written
// against the real resource would not match it.
func rdsCustomEngineVersionARN(engine, version, id string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:cev:%s/%s/%s", awsRegion(), awsAccountID(), engine, version, id)
}

func rdsCEVKey(engine, version string) string { return engine + "/" + version }

// renderRDSDBEngineVersion renders a DBEngineVersion. The three custom
// engine version operations all declare DBEngineVersion as their output
// target, so members serialize inline in <...Result> with no wrapper.
func renderRDSDBEngineVersion(c RDSCustomEngineVersion) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(c.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(c.EngineVersion))
	fmt.Fprintf(&b, "<DBParameterGroupFamily>%s</DBParameterGroupFamily>", xmlEscape(c.DBParameterGroupFamily))
	fmt.Fprintf(&b, "<DBEngineVersionDescription>%s</DBEngineVersionDescription>", xmlEscape(c.Description))
	fmt.Fprintf(&b, "<DBEngineVersionArn>%s</DBEngineVersionArn>", xmlEscape(c.ARN))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(c.Status))
	if c.KMSKeyId != "" {
		fmt.Fprintf(&b, "<KMSKeyId>%s</KMSKeyId>", xmlEscape(c.KMSKeyId))
	}
	if c.ImageId != "" {
		fmt.Fprintf(&b, "<Image><ImageId>%s</ImageId></Image>", xmlEscape(c.ImageId))
	}
	fmt.Fprintf(&b, "<CreateTime>%s</CreateTime>", xmlEscape(c.CreateTime))
	b.WriteString("<MajorEngineVersion></MajorEngineVersion>")
	b.WriteString(renderRDSTagList(c.Tags))
	return b.String()
}

func handleRDSCreateCustomEngineVersion(w http.ResponseWriter, r *http.Request) {
	engine := r.FormValue("Engine")
	version := r.FormValue("EngineVersion")
	if engine == "" || version == "" {
		rdsErrorXML(w, "MissingParameter", "Engine and EngineVersion are required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	key := rdsCEVKey(engine, version)
	if _, ok := rdsCustomEngineVersions.Get(key); ok {
		rdsErrorXML(w, "CustomDBEngineVersionAlreadyExistsFault",
			fmt.Sprintf("Custom engine version %q already exists", version),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	cevID := generateUUID()
	cev := RDSCustomEngineVersion{
		Engine:                  engine,
		CustomDBEngineVersionId: cevID,
		EngineVersion:           version,
		DBParameterGroupFamily:  engine + strings.SplitN(version, ".", 2)[0],
		Description:             r.FormValue("Description"),
		// Inline-settle: real RDS goes through "pending-validation" while
		// it builds the image; the sim has no build to gate on and emits
		// the steady-state "available".
		Status:     "available",
		KMSKeyId:   r.FormValue("KMSKeyId"),
		ImageId:    r.FormValue("ImageId"),
		CreateTime: time.Now().UTC().Format(time.RFC3339),
		ARN:        rdsCustomEngineVersionARN(engine, version, cevID),
		Tags:       parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsCustomEngineVersions.Put(key, cev)
	rdsXMLResponse(w, "CreateCustomDBEngineVersion", renderRDSDBEngineVersion(cev), sim.RequestID(r.Context()))
}

func handleRDSModifyCustomEngineVersion(w http.ResponseWriter, r *http.Request) {
	engine := r.FormValue("Engine")
	version := r.FormValue("EngineVersion")
	key := rdsCEVKey(engine, version)
	if _, ok := rdsCustomEngineVersions.Get(key); !ok {
		rdsErrorXML(w, "CustomDBEngineVersionNotFoundFault",
			fmt.Sprintf("Custom engine version %q not found", version),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsCustomEngineVersions.Update(key, func(c *RDSCustomEngineVersion) {
		if v := r.FormValue("Description"); v != "" {
			c.Description = v
		}
		if v := r.FormValue("Status"); v != "" {
			c.Status = v
		}
	})
	cev, _ := rdsCustomEngineVersions.Get(key)
	rdsXMLResponse(w, "ModifyCustomDBEngineVersion", renderRDSDBEngineVersion(cev), sim.RequestID(r.Context()))
}

func handleRDSDeleteCustomEngineVersion(w http.ResponseWriter, r *http.Request) {
	engine := r.FormValue("Engine")
	version := r.FormValue("EngineVersion")
	key := rdsCEVKey(engine, version)
	cev, ok := rdsCustomEngineVersions.Get(key)
	if !ok {
		rdsErrorXML(w, "CustomDBEngineVersionNotFoundFault",
			fmt.Sprintf("Custom engine version %q not found", version),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsCustomEngineVersions.Delete(key)
	rdsXMLResponse(w, "DeleteCustomDBEngineVersion", renderRDSDBEngineVersion(cev), sim.RequestID(r.Context()))
}

// DB recommendations

// renderRDSRecommendation renders a recommendation wrapped in the given
// element. DescribeDBRecommendations lists members as <member> (the
// DBRecommendationList member carries no xmlName), while
// ModifyDBRecommendation returns a single <DBRecommendation> member.
func renderRDSRecommendation(rec RDSRecommendation, elem string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<%s>", elem)
	fmt.Fprintf(&b, "<RecommendationId>%s</RecommendationId>", xmlEscape(rec.RecommendationId))
	fmt.Fprintf(&b, "<TypeId>%s</TypeId>", xmlEscape(rec.TypeId))
	fmt.Fprintf(&b, "<Severity>%s</Severity>", xmlEscape(rec.Severity))
	fmt.Fprintf(&b, "<ResourceArn>%s</ResourceArn>", xmlEscape(rec.ResourceArn))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(rec.Status))
	fmt.Fprintf(&b, "<CreatedTime>%s</CreatedTime>", xmlEscape(rec.CreatedTime))
	fmt.Fprintf(&b, "<UpdatedTime>%s</UpdatedTime>", xmlEscape(rec.UpdatedTime))
	fmt.Fprintf(&b, "<Source>%s</Source>", xmlEscape(rec.Source))
	fmt.Fprintf(&b, "<Category>%s</Category>", xmlEscape(rec.Category))
	fmt.Fprintf(&b, "<Recommendation>%s</Recommendation>", xmlEscape(rec.Recommendation))
	fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(rec.Description))
	fmt.Fprintf(&b, "</%s>", elem)
	return b.String()
}

// rdsSeedRecommendation lazily materializes a recommendation for a
// resource ARN. Real RDS generates recommendations asynchronously from
// engine telemetry; with no engine to observe, the sim derives one
// deterministic recommendation per resource on first read so the
// Describe/Modify lifecycle is exercisable end-to-end against real
// stored state.
func rdsSeedRecommendation(resourceArn string) RDSRecommendation {
	id := "rec-" + strings.ToLower(strings.ReplaceAll(generateUUID(), "-", ""))[:17]
	now := time.Now().UTC().Format(time.RFC3339)
	rec := RDSRecommendation{
		RecommendationId: id,
		ResourceArn:      resourceArn,
		Status:           "active",
		Severity:         "informational",
		Category:         "performance efficiency",
		Source:           "RDS",
		TypeId:           "config_recommendation",
		Recommendation:   "Enable Performance Insights",
		Description:      "Enable Performance Insights to monitor database load.",
		CreatedTime:      now,
		UpdatedTime:      now,
	}
	rdsRecommendations.Put(id, rec)
	return rec
}

func handleRDSDescribeRecommendations(w http.ResponseWriter, r *http.Request) {
	// A "db-instance-arn" filter scopes recommendations to one resource;
	// seed one for that resource if none exists yet.
	resourceArn := rdsFilterValue(r, "db-instance-arn")
	if resourceArn != "" {
		found := false
		for _, rec := range rdsRecommendations.List() {
			if rec.ResourceArn == resourceArn {
				found = true
				break
			}
		}
		if !found {
			rdsSeedRecommendation(resourceArn)
		}
	}
	var b strings.Builder
	b.WriteString("<DBRecommendations>")
	for _, rec := range rdsRecommendations.List() {
		if resourceArn != "" && rec.ResourceArn != resourceArn {
			continue
		}
		b.WriteString(renderRDSRecommendation(rec, "member"))
	}
	b.WriteString("</DBRecommendations>")
	rdsXMLResponse(w, "DescribeDBRecommendations", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyRecommendation(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("RecommendationId")
	if _, ok := rdsRecommendations.Get(id); !ok {
		rdsErrorXML(w, "InvalidParameterValue",
			fmt.Sprintf("Recommendation %q not found", id),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	rdsRecommendations.Update(id, func(rec *RDSRecommendation) {
		if v := r.FormValue("Status"); v != "" {
			rec.Status = v
		}
		rec.UpdatedTime = time.Now().UTC().Format(time.RFC3339)
	})
	rec, _ := rdsRecommendations.Get(id)
	rdsXMLResponse(w, "ModifyDBRecommendation", renderRDSRecommendation(rec, "DBRecommendation"), sim.RequestID(r.Context()))
}

// DB snapshot tenant databases

func handleRDSDescribeSnapshotTenantDatabases(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("DBSnapshotIdentifier")
	instFilter := r.FormValue("DBInstanceIdentifier")
	resourceFilter := r.FormValue("DbiResourceId")

	// The tenant databases of a snapshot are the tenant databases that
	// existed on the snapshot's source DB instance. Resolve the snapshot
	// to its source instance, then read the tenant-database store.
	sourceInstances := map[string]bool{}
	for _, s := range rdsSnapshots.List() {
		if snapID != "" && s.DBSnapshotIdentifier != snapID && s.ARN != snapID {
			continue
		}
		if instFilter != "" && s.DBInstanceIdentifier != instFilter {
			continue
		}
		if resourceFilter != "" && s.DbiResourceId != resourceFilter {
			continue
		}
		sourceInstances[s.DBInstanceIdentifier] = true
	}

	var b strings.Builder
	b.WriteString("<DBSnapshotTenantDatabases>")
	for _, t := range rdsTenantDatabases.List() {
		if !sourceInstances[t.DBInstanceIdentifier] {
			continue
		}
		// Pair every matching source-instance snapshot with the tenant DB.
		for _, s := range rdsSnapshots.List() {
			if s.DBInstanceIdentifier != t.DBInstanceIdentifier {
				continue
			}
			if snapID != "" && s.DBSnapshotIdentifier != snapID && s.ARN != snapID {
				continue
			}
			b.WriteString(renderRDSDBSnapshotTenantDatabase(s, t))
		}
	}
	b.WriteString("</DBSnapshotTenantDatabases>")
	rdsXMLResponse(w, "DescribeDBSnapshotTenantDatabases", b.String(), sim.RequestID(r.Context()))
}

// renderRDSDBSnapshotTenantDatabase renders a DBSnapshotTenantDatabase
// list member. The list member carries an explicit xmlName, so it
// serializes as <DBSnapshotTenantDatabase> (not <member>).
func renderRDSDBSnapshotTenantDatabase(s RDSSnapshot, t RDSTenantDatabase) string {
	var b strings.Builder
	b.WriteString("<DBSnapshotTenantDatabase>")
	fmt.Fprintf(&b, "<DBSnapshotIdentifier>%s</DBSnapshotIdentifier>", xmlEscape(s.DBSnapshotIdentifier))
	fmt.Fprintf(&b, "<DBInstanceIdentifier>%s</DBInstanceIdentifier>", xmlEscape(t.DBInstanceIdentifier))
	fmt.Fprintf(&b, "<DbiResourceId>%s</DbiResourceId>", xmlEscape(t.DbiResourceId))
	fmt.Fprintf(&b, "<EngineName>%s</EngineName>", xmlEscape(s.Engine))
	fmt.Fprintf(&b, "<SnapshotType>%s</SnapshotType>", xmlEscape(s.SnapshotType))
	fmt.Fprintf(&b, "<TenantDatabaseCreateTime>%s</TenantDatabaseCreateTime>", xmlEscape(t.CreateTime))
	fmt.Fprintf(&b, "<TenantDBName>%s</TenantDBName>", xmlEscape(t.TenantDBName))
	fmt.Fprintf(&b, "<MasterUsername>%s</MasterUsername>", xmlEscape(t.MasterUsername))
	fmt.Fprintf(&b, "<TenantDatabaseResourceId>%s</TenantDatabaseResourceId>", xmlEscape(t.TenantDatabaseResourceId))
	fmt.Fprintf(&b, "<CharacterSetName>%s</CharacterSetName>", xmlEscape(t.CharacterSetName))
	fmt.Fprintf(&b, "<NcharCharacterSetName>%s</NcharCharacterSetName>", xmlEscape(t.NcharCharacterSetName))
	arn := fmt.Sprintf("arn:aws:rds:%s:%s:snapshot-tenant-database:%s/%s",
		awsRegion(), awsAccountID(), s.DBSnapshotIdentifier, t.TenantDBName)
	fmt.Fprintf(&b, "<DBSnapshotTenantDatabaseARN>%s</DBSnapshotTenantDatabaseARN>", xmlEscape(arn))
	b.WriteString(renderRDSTagList(t.Tags))
	b.WriteString("</DBSnapshotTenantDatabase>")
	return b.String()
}

// Serverless v2 platform versions / valid DB instance modifications

func handleRDSDescribeServerlessV2PlatformVersions(w http.ResponseWriter, r *http.Request) {
	// ServerlessV2PlatformVersionList members carry no xmlName → <member>.
	// The values mirror the real default Aurora Serverless v2 platform
	// version table (one enabled default version per query).
	engine := r.FormValue("Engine")
	if engine == "" {
		engine = "aurora-postgresql"
	}
	var b strings.Builder
	b.WriteString("<ServerlessV2PlatformVersions>")
	b.WriteString("<member>")
	b.WriteString("<ServerlessV2PlatformVersion>1.0</ServerlessV2PlatformVersion>")
	b.WriteString("<ServerlessV2PlatformVersionDescription>Aurora Serverless v2 platform version 1.0</ServerlessV2PlatformVersionDescription>")
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(engine))
	b.WriteString("<Status>enabled</Status>")
	b.WriteString("<IsDefault>true</IsDefault>")
	b.WriteString("</member>")
	b.WriteString("</ServerlessV2PlatformVersions>")
	rdsXMLResponse(w, "DescribeServerlessV2PlatformVersions", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDescribeValidDBInstanceModifications(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	if _, ok := rdsInstances.Get(id); !ok {
		rdsErrorXML(w, "DBInstanceNotFound",
			fmt.Sprintf("DBInstance %q not found", id),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	// The valid-modifications shape reports AWS's real general-purpose SSD
	// (gp2) and provisioned-IOPS (io1) storage envelopes. ValidStorageOptions
	// and the Range list members carry explicit xmlNames.
	var b strings.Builder
	b.WriteString("<ValidDBInstanceModificationsMessage>")
	b.WriteString("<Storage>")
	b.WriteString(rdsValidStorageOption("gp2", 20, 65536, 1, 0))
	b.WriteString(rdsValidStorageOption("io1", 100, 65536, 1, 3))
	b.WriteString("</Storage>")
	b.WriteString("<ValidProcessorFeatures></ValidProcessorFeatures>")
	b.WriteString("</ValidDBInstanceModificationsMessage>")
	rdsXMLResponse(w, "DescribeValidDBInstanceModifications", b.String(), sim.RequestID(r.Context()))
}

// rdsValidStorageOption renders one <ValidStorageOptions> entry with a
// StorageSize range. When ratioFrom>0 an IopsToStorageRatio is included
// (the io1 envelope).
func rdsValidStorageOption(storageType string, sizeFrom, sizeTo, sizeStep, ratioFrom int) string {
	var b strings.Builder
	b.WriteString("<ValidStorageOptions>")
	fmt.Fprintf(&b, "<StorageType>%s</StorageType>", xmlEscape(storageType))
	b.WriteString("<StorageSize>")
	b.WriteString(rdsRange(sizeFrom, sizeTo, sizeStep))
	b.WriteString("</StorageSize>")
	b.WriteString("<ProvisionedIops></ProvisionedIops>")
	if ratioFrom > 0 {
		b.WriteString("<IopsToStorageRatio>")
		fmt.Fprintf(&b, "<DoubleRange><From>%d.0</From><To>50.0</To></DoubleRange>", ratioFrom)
		b.WriteString("</IopsToStorageRatio>")
	} else {
		b.WriteString("<IopsToStorageRatio></IopsToStorageRatio>")
	}
	b.WriteString("<SupportsStorageAutoscaling>true</SupportsStorageAutoscaling>")
	b.WriteString("</ValidStorageOptions>")
	return b.String()
}

func rdsRange(from, to, step int) string {
	return fmt.Sprintf("<Range><From>%d</From><To>%d</To><Step>%d</Step></Range>", from, to, step)
}

// Serverless v1 cluster capacity

func handleRDSModifyCurrentDBClusterCapacity(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	if _, ok := rdsClusters.Get(id); !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault",
			fmt.Sprintf("DBCluster %q not found", id),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	capacity := atoiOrZero(r.FormValue("Capacity"))
	timeoutAction := r.FormValue("TimeoutAction")
	if timeoutAction == "" {
		timeoutAction = "ForceApplyCapacityChange"
	}
	rdsClusterCapacities.Put(id, RDSClusterCapacity{
		DBClusterIdentifier: id,
		CurrentCapacity:     capacity,
	})
	var b strings.Builder
	fmt.Fprintf(&b, "<DBClusterIdentifier>%s</DBClusterIdentifier>", xmlEscape(id))
	fmt.Fprintf(&b, "<PendingCapacity>%d</PendingCapacity>", capacity)
	fmt.Fprintf(&b, "<CurrentCapacity>%d</CurrentCapacity>", capacity)
	if v := atoiOrZero(r.FormValue("SecondsBeforeTimeout")); v > 0 {
		fmt.Fprintf(&b, "<SecondsBeforeTimeout>%d</SecondsBeforeTimeout>", v)
	}
	fmt.Fprintf(&b, "<TimeoutAction>%s</TimeoutAction>", xmlEscape(timeoutAction))
	rdsXMLResponse(w, "ModifyCurrentDBClusterCapacity", b.String(), sim.RequestID(r.Context()))
}

// Option-group option membership

func handleRDSModifyOptionGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("OptionGroupName")
	og, ok := rdsOptionGroups.Get(name)
	if !ok {
		rdsErrorXML(w, "OptionGroupNotFoundFault",
			fmt.Sprintf("OptionGroup %q not found", name),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	cur, _ := rdsOptionGroupOptions.Get(name)
	cur.OptionGroupName = name
	// OptionsToInclude is a list of OptionConfiguration; the option name
	// is the OptionName member of each entry.
	for n := 1; n <= 200; n++ {
		opt := r.FormValue(fmt.Sprintf("OptionsToInclude.OptionConfiguration.%d.OptionName", n))
		if opt == "" {
			break
		}
		if !rdsContains(cur.OptionNames, opt) {
			cur.OptionNames = append(cur.OptionNames, opt)
		}
	}
	// OptionsToRemove is a list of option-name strings (OptionNamesList
	// serializes with a bare <member> wire key).
	for _, rm := range parseRDSIndexedStringList(r, "OptionsToRemove.member") {
		cur.OptionNames = rdsRemoveString(cur.OptionNames, rm)
	}
	rdsOptionGroupOptions.Put(name, cur)
	rdsXMLResponse(w, "ModifyOptionGroup", renderRDSOptionGroupWithOptions(og, cur.OptionNames), sim.RequestID(r.Context()))
}

// renderRDSOptionGroupWithOptions renders an option group whose <Options>
// list reflects the included option names. OptionsList members carry an
// explicit xmlName, so each serializes as <Option>.
func renderRDSOptionGroupWithOptions(g RDSOptionGroup, options []string) string {
	var b strings.Builder
	b.WriteString("<OptionGroup>")
	fmt.Fprintf(&b, "<OptionGroupName>%s</OptionGroupName>", xmlEscape(g.OptionGroupName))
	fmt.Fprintf(&b, "<OptionGroupDescription>%s</OptionGroupDescription>", xmlEscape(g.OptionGroupDescription))
	fmt.Fprintf(&b, "<EngineName>%s</EngineName>", xmlEscape(g.EngineName))
	fmt.Fprintf(&b, "<MajorEngineVersion>%s</MajorEngineVersion>", xmlEscape(g.MajorEngineVersion))
	fmt.Fprintf(&b, "<AllowsVpcAndNonVpcInstanceMemberships>%t</AllowsVpcAndNonVpcInstanceMemberships>", g.AllowsVpcAndNonVpcInstanceMemberships)
	fmt.Fprintf(&b, "<OptionGroupArn>%s</OptionGroupArn>", xmlEscape(g.ARN))
	b.WriteString("<Options>")
	for _, opt := range options {
		b.WriteString("<Option>")
		fmt.Fprintf(&b, "<OptionName>%s</OptionName>", xmlEscape(opt))
		b.WriteString("<Persistent>false</Persistent>")
		b.WriteString("<Permanent>false</Permanent>")
		b.WriteString("</Option>")
	}
	b.WriteString("</Options>")
	b.WriteString("</OptionGroup>")
	return b.String()
}

// Automated-backups cross-region replication

// renderRDSAutomatedBackup renders a DBInstanceAutomatedBackup. The
// Start/Stop output shapes wrap it in a single <DBInstanceAutomatedBackup>
// member element inside the <...Result>.
func renderRDSAutomatedBackup(inst RDSInstance, rep RDSAutomatedBackupReplication) string {
	var b strings.Builder
	b.WriteString("<DBInstanceAutomatedBackup>")
	fmt.Fprintf(&b, "<DBInstanceArn>%s</DBInstanceArn>", xmlEscape(inst.ARN))
	fmt.Fprintf(&b, "<DBInstanceIdentifier>%s</DBInstanceIdentifier>", xmlEscape(inst.DBInstanceIdentifier))
	fmt.Fprintf(&b, "<DbiResourceId>%s</DbiResourceId>", xmlEscape(inst.DbiResourceId))
	fmt.Fprintf(&b, "<Region>%s</Region>", xmlEscape(awsRegion()))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(rep.Status))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(inst.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(inst.EngineVersion))
	fmt.Fprintf(&b, "<AllocatedStorage>%d</AllocatedStorage>", inst.AllocatedStorage)
	fmt.Fprintf(&b, "<Port>%d</Port>", inst.Port)
	fmt.Fprintf(&b, "<MasterUsername>%s</MasterUsername>", xmlEscape(inst.MasterUsername))
	fmt.Fprintf(&b, "<BackupRetentionPeriod>%d</BackupRetentionPeriod>", rep.BackupRetentionPeriod)
	fmt.Fprintf(&b, "<InstanceCreateTime>%s</InstanceCreateTime>", xmlEscape(inst.InstanceCreateTime))
	fmt.Fprintf(&b, "<AvailabilityZone>%s</AvailabilityZone>", xmlEscape(inst.AvailabilityZone))
	dbabArn := fmt.Sprintf("arn:aws:rds:%s:%s:auto-backup:ab-%s",
		awsRegion(), awsAccountID(), inst.DbiResourceId)
	fmt.Fprintf(&b, "<DBInstanceAutomatedBackupsArn>%s</DBInstanceAutomatedBackupsArn>", xmlEscape(dbabArn))
	if rep.KmsKeyId != "" {
		fmt.Fprintf(&b, "<KmsKeyId>%s</KmsKeyId>", xmlEscape(rep.KmsKeyId))
		b.WriteString("<Encrypted>true</Encrypted>")
	} else {
		b.WriteString("<Encrypted>false</Encrypted>")
	}
	b.WriteString("</DBInstanceAutomatedBackup>")
	return b.String()
}

func rdsInstanceByArn(arn string) (RDSInstance, bool) {
	for _, i := range rdsInstances.List() {
		if i.ARN == arn {
			return i, true
		}
	}
	return RDSInstance{}, false
}

func handleRDSStartAutomatedBackupsReplication(w http.ResponseWriter, r *http.Request) {
	srcArn := r.FormValue("SourceDBInstanceArn")
	inst, ok := rdsInstanceByArn(srcArn)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound",
			fmt.Sprintf("Source DB instance %q not found", srcArn),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	retention := 7
	if v := atoiOrZero(r.FormValue("BackupRetentionPeriod")); v > 0 {
		retention = v
	}
	rep := RDSAutomatedBackupReplication{
		SourceDBInstanceArn:   srcArn,
		BackupRetentionPeriod: retention,
		KmsKeyId:              r.FormValue("KmsKeyId"),
		Status:                "replicating",
	}
	rdsAutomatedBackupReplicas.Put(srcArn, rep)
	rdsXMLResponse(w, "StartDBInstanceAutomatedBackupsReplication", renderRDSAutomatedBackup(inst, rep), sim.RequestID(r.Context()))
}

func handleRDSStopAutomatedBackupsReplication(w http.ResponseWriter, r *http.Request) {
	srcArn := r.FormValue("SourceDBInstanceArn")
	inst, ok := rdsInstanceByArn(srcArn)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound",
			fmt.Sprintf("Source DB instance %q not found", srcArn),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rep, ok := rdsAutomatedBackupReplicas.Get(srcArn)
	if !ok {
		rep = RDSAutomatedBackupReplication{SourceDBInstanceArn: srcArn, BackupRetentionPeriod: 7}
	}
	rep.Status = "stopped"
	rdsAutomatedBackupReplicas.Delete(srcArn)
	rdsXMLResponse(w, "StopDBInstanceAutomatedBackupsReplication", renderRDSAutomatedBackup(inst, rep), sim.RequestID(r.Context()))
}

// Switchover (global cluster / read replica)

func handleRDSSwitchoverGlobalCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("GlobalClusterIdentifier")
	g, ok := rdsGlobalClusters.Get(id)
	if !ok {
		rdsErrorXML(w, "GlobalClusterNotFoundFault",
			fmt.Sprintf("GlobalCluster %q not found", id),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	target := r.FormValue("TargetDbClusterIdentifier")
	// Switchover promotes the target secondary to writer: move its ARN to
	// the front of the member list (index 0 is the writer in
	// renderRDSGlobalCluster).
	if target != "" {
		rdsGlobalClusters.Update(id, func(x *RDSGlobalCluster) {
			for idx, arn := range x.Members {
				if arn == target || strings.HasSuffix(arn, ":"+target) {
					x.Members = append([]string{arn}, append(append([]string{}, x.Members[:idx]...), x.Members[idx+1:]...)...)
					break
				}
			}
		})
		g, _ = rdsGlobalClusters.Get(id)
	}
	rdsXMLResponse(w, "SwitchoverGlobalCluster", renderRDSGlobalCluster(g, "GlobalCluster"), sim.RequestID(r.Context()))
}

func handleRDSSwitchoverReadReplica(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	replica, ok := rdsInstances.Get(id)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound",
			fmt.Sprintf("DBInstance %q not found", id),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if replica.ReadReplicaSource == "" {
		rdsErrorXML(w, "InvalidDBInstanceState",
			fmt.Sprintf("DBInstance %q is not a read replica", id),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	// Switchover swaps the primary/replica roles: the old primary becomes
	// a replica of the named instance, which becomes the new primary.
	oldPrimaryID := replica.ReadReplicaSource
	rdsInstances.Update(id, func(i *RDSInstance) {
		i.ReadReplicaSource = ""
		i.ReadReplicas = rdsAppendUnique(rdsRemoveString(i.ReadReplicas, oldPrimaryID), oldPrimaryID)
	})
	if _, ok := rdsInstances.Get(oldPrimaryID); ok {
		rdsInstances.Update(oldPrimaryID, func(i *RDSInstance) {
			i.ReadReplicaSource = id
			i.ReadReplicas = rdsRemoveString(i.ReadReplicas, id)
		})
	}
	updated, _ := rdsInstances.Get(id)
	rdsXMLResponse(w, "SwitchoverReadReplica", renderRDSInstance(updated), sim.RequestID(r.Context()))
}

// small slice helpers

func rdsRemoveString(xs []string, v string) []string {
	out := xs[:0:0]
	for _, x := range xs {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func rdsAppendUnique(xs []string, v string) []string {
	if rdsContains(xs, v) {
		return xs
	}
	return append(xs, v)
}
