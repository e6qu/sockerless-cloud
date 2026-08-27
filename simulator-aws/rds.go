package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// RDS — awsQuery protocol. Surface scoped to the 90th-percentile
// terraform-provider-aws + SDK lifecycle: CreateDBInstance,
// DescribeDBInstances (waiter-driven), ModifyDBInstance,
// DeleteDBInstance, AddTagsToResource, ListTagsForResource,
// RemoveTagsFromResource, CreateDBSnapshot, DescribeDBSnapshots,
// DescribeDBSnapshotAttributes, DeleteDBSnapshot, and
// RestoreDBInstanceFromDBSnapshot. PostgreSQL and MySQL-compatible instances
// expose their real database wire protocols through managed engine containers;
// the control plane returns Status=available once their endpoint listener is
// installed and starts the engine lazily on the first data-plane connection.

type RDSInstance struct {
	DBInstanceIdentifier string
	DbiResourceId        string
	DBInstanceClass      string
	Engine               string
	EngineVersion        string
	DBInstanceStatus     string
	MasterUsername       string
	DBName               string
	AllocatedStorage     int
	Endpoint             string
	Port                 int
	AvailabilityZone     string
	InstanceCreateTime   string
	ARN                  string
	// ReadReplicaSource is the source DB instance identifier when this
	// instance is a read replica (created by CreateDBInstanceReadReplica);
	// empty for primary instances.
	ReadReplicaSource string
	// ReadReplicas lists the identifiers of read replicas created from
	// this instance (populated on the source when a replica is created).
	ReadReplicas []string
	Tags         map[string]string
	// MasterUserSecret is encrypted under the simulator cloud's AWS-owned RDS
	// KMS key and is never rendered on the API.
	MasterUserSecret []byte
	// BackendMasterUserSecret records the password currently installed in the
	// native database engine. It can temporarily differ from MasterUserSecret
	// when a stopped instance receives a password change; the engine applies
	// that pending change when it next starts.
	BackendMasterUserSecret         []byte
	EnableIAMDatabaseAuthentication bool
}

// RDSSnapshot models the canonical RDS DB snapshot state machine:
//
//	(CreateDBSnapshot)        → creating
//	(internal-settle on read) → available
//	(DeleteDBSnapshot)        → deleted (removed from store)
//
// The sim collapses the creating→available transition into an
// inline-settle: every snapshot row is written with Status=available
// from the start. See sim-state-machine-completeness skill — when a
// transition is collapsed, document the choice so future maintainers
// don't read "available" and assume the transient state doesn't
// exist on real RDS.
type RDSSnapshot struct {
	DBSnapshotIdentifier string
	DBInstanceIdentifier string
	DbiResourceId        string
	Engine               string
	EngineVersion        string
	Status               string // creating | available | deleting | failed
	// StatusReason says why a snapshot failed, in the words of the capture
	// that failed; empty otherwise.
	StatusReason       string
	AllocatedStorage   int
	MasterUsername     string
	SnapshotCreateTime string
	SnapshotType       string // manual | automated
	Port               int
	VpcId              string
	// SourceDBSnapshotIdentifier is the ARN of the snapshot this one was
	// copied from (CopyDBSnapshot); empty for snapshots created directly.
	SourceDBSnapshotIdentifier string
	// RestoreAttributeValues holds the AWS account IDs (or "all") granted
	// the "restore" snapshot attribute via ModifyDBSnapshotAttribute.
	// DescribeDBSnapshotAttributes returns these under the "restore"
	// attribute.
	RestoreAttributeValues []string
	ARN                    string
	Tags                   map[string]string
	// MasterUserSecret carries the source instance's encrypted master
	// credential, so an instance restored from this snapshot can start its
	// engine with the credentials the data expects — real RDS restores the
	// master credentials with the data.
	MasterUserSecret []byte
}

// RDSCluster models a (control-plane only) Aurora/Multi-AZ DB cluster.
// The database engine is not simulated; Status settles to "available"
// inline on Create, matching the sim's instance/snapshot convention.
type RDSCluster struct {
	DBClusterIdentifier        string
	DbClusterResourceId        string
	Engine                     string
	EngineVersion              string
	EngineMode                 string
	Status                     string
	DatabaseName               string
	MasterUsername             string
	Port                       int
	Endpoint                   string
	ReaderEndpoint             string
	DBClusterParameterGroup    string
	DBSubnetGroup              string
	AllocatedStorage           int
	BackupRetentionPeriod      int
	StorageEncrypted           bool
	DeletionProtection         bool
	ClusterCreateTime          string
	AvailabilityZones          []string
	PreferredBackupWindow      string
	PreferredMaintenanceWindow string
	ARN                        string
	Tags                       map[string]string
}

// RDSSubnetGroup models a DB subnet group (a named set of VPC subnets
// RDS places DB instances into).
type RDSSubnetGroup struct {
	DBSubnetGroupName        string
	DBSubnetGroupDescription string
	VpcId                    string
	SubnetGroupStatus        string
	SubnetIds                []string
	ARN                      string
	Tags                     map[string]string
}

// RDSParamGroup models a DB parameter group. The default engine
// parameters are not stored; Parameters holds the user-set overrides
// applied via ModifyDBParameterGroup, which DescribeDBParameters
// reflects on top of the engine-default set.
type RDSParamGroup struct {
	DBParameterGroupName   string
	DBParameterGroupFamily string
	Description            string
	Parameters             map[string]string
	ARN                    string
	Tags                   map[string]string
}

// RDSClusterParamGroup models a DB cluster parameter group. Parameters
// holds the user-set overrides applied via
// ModifyDBClusterParameterGroup, which DescribeDBClusterParameters
// reflects on top of the engine-default set.
type RDSClusterParamGroup struct {
	DBClusterParameterGroupName string
	DBParameterGroupFamily      string
	Description                 string
	Parameters                  map[string]string
	ARN                         string
	Tags                        map[string]string
}

// RDSClusterSnapshot models a DB cluster snapshot. Like the instance
// snapshot, the creating→available transition is collapsed into an
// inline-settle (Status=available from the start) — there is no async
// engine work to gate on.
type RDSClusterSnapshot struct {
	DBClusterSnapshotIdentifier string
	DBClusterIdentifier         string
	DbClusterResourceId         string
	Engine                      string
	EngineVersion               string
	EngineMode                  string
	Status                      string
	AllocatedStorage            int
	MasterUsername              string
	Port                        int
	VpcId                       string
	StorageEncrypted            bool
	SnapshotCreateTime          string
	ClusterCreateTime           string
	SnapshotType                string // manual | automated
	PercentProgress             int
	AvailabilityZones           []string
	ARN                         string
	Tags                        map[string]string
}

// RDSOptionGroup models an option group (a named collection of engine
// options). Individual options are not simulated; the group is a
// faithful control-plane row.
type RDSOptionGroup struct {
	OptionGroupName                       string
	OptionGroupDescription                string
	EngineName                            string
	MajorEngineVersion                    string
	AllowsVpcAndNonVpcInstanceMemberships bool
	ARN                                   string
	Tags                                  map[string]string
}

// RDSGlobalCluster models an Aurora global database cluster — a single
// control-plane row that links one or more regional DB clusters. The
// engine is not simulated; Status settles to "available" inline on
// Create, matching the sim's instance/cluster convention.
type RDSGlobalCluster struct {
	GlobalClusterIdentifier string
	GlobalClusterResourceId string
	Engine                  string
	EngineVersion           string
	Status                  string
	DatabaseName            string
	DeletionProtection      bool
	StorageEncrypted        bool
	// Members holds the ARNs of regional DB clusters attached to this
	// global cluster (the first is the writer).
	Members []string
	ARN     string
	Tags    map[string]string
}

// RDSEventSubscription models an RDS event notification subscription
// (a named SNS-topic binding filtered by source type / source IDs /
// event categories). Status settles to "active" inline on Create.
type RDSEventSubscription struct {
	SubscriptionName       string
	CustomerAwsId          string
	SnsTopicArn            string
	Status                 string
	SubscriptionCreateTime string
	SourceType             string
	Enabled                bool
	SourceIds              []string
	EventCategories        []string
	ARN                    string
	Tags                   map[string]string
}

// RDSClusterEndpoint models a custom DB cluster endpoint (a named
// reader/any endpoint scoped to a static or excluded member list).
// Status settles to "available" inline on Create.
type RDSClusterEndpoint struct {
	DBClusterEndpointIdentifier         string
	DBClusterIdentifier                 string
	DBClusterEndpointResourceIdentifier string
	Endpoint                            string
	Status                              string
	EndpointType                        string
	CustomEndpointType                  string
	StaticMembers                       []string
	ExcludedMembers                     []string
	ARN                                 string
	Tags                                map[string]string
}

var (
	rdsInstances          sim.Store[RDSInstance]
	rdsSnapshots          sim.Store[RDSSnapshot]
	rdsClusters           sim.Store[RDSCluster]
	rdsClusterSnapshots   sim.Store[RDSClusterSnapshot]
	rdsSubnetGroups       sim.Store[RDSSubnetGroup]
	rdsParamGroups        sim.Store[RDSParamGroup]
	rdsClusterParamGroups sim.Store[RDSClusterParamGroup]
	rdsOptionGroups       sim.Store[RDSOptionGroup]
	rdsGlobalClusters     sim.Store[RDSGlobalCluster]
	rdsEventSubscriptions sim.Store[RDSEventSubscription]
	rdsClusterEndpoints   sim.Store[RDSClusterEndpoint]
)

// rdsAPIVersion is the canonical AWS RDS API version (Query
// Protocol). Used to disambiguate Action names from other awsQuery
// services in the AWSQueryRouter dispatch.
const rdsAPIVersion = "2014-10-31"

func registerRDS(r *sim.AWSQueryRouter, srv *sim.Server) {
	rdsInstances = sim.MakeStore[RDSInstance](srv.DB(), "rds_instances")
	rdsSnapshots = sim.MakeStore[RDSSnapshot](srv.DB(), "rds_snapshots")
	r.RegisterVersioned(rdsAPIVersion, "CreateDBInstance", handleRDSCreate)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBInstances", handleRDSDescribe)
	r.RegisterVersioned(rdsAPIVersion, "ModifyDBInstance", handleRDSModify)
	r.RegisterVersioned(rdsAPIVersion, "DeleteDBInstance", handleRDSDelete)
	r.RegisterVersioned(rdsAPIVersion, "AddTagsToResource", handleRDSAddTags)
	r.RegisterVersioned(rdsAPIVersion, "ListTagsForResource", handleRDSListTags)
	r.RegisterVersioned(rdsAPIVersion, "RemoveTagsFromResource", handleRDSRemoveTags)
	r.RegisterVersioned(rdsAPIVersion, "CreateDBSnapshot", handleRDSCreateSnapshot)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBSnapshots", handleRDSDescribeSnapshots)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBSnapshotAttributes", handleRDSDescribeSnapshotAttributes)
	r.RegisterVersioned(rdsAPIVersion, "DeleteDBSnapshot", handleRDSDeleteSnapshot)
	r.RegisterVersioned(rdsAPIVersion, "RestoreDBInstanceFromDBSnapshot", handleRDSRestoreFromSnapshot)
	r.RegisterVersioned(rdsAPIVersion, "CopyDBSnapshot", handleRDSCopySnapshot)
	r.RegisterVersioned(rdsAPIVersion, "RebootDBInstance", handleRDSReboot)
	r.RegisterVersioned(rdsAPIVersion, "CreateDBInstanceReadReplica", handleRDSCreateReadReplica)
	r.RegisterVersioned(rdsAPIVersion, "StartDBInstance", handleRDSStartInstance)
	r.RegisterVersioned(rdsAPIVersion, "StopDBInstance", handleRDSStopInstance)
	r.RegisterVersioned(rdsAPIVersion, "PromoteReadReplica", handleRDSPromoteReadReplica)
	r.RegisterVersioned(rdsAPIVersion, "ModifyDBSnapshotAttribute", handleRDSModifySnapshotAttribute)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBParameters", handleRDSDescribeParameters)
	r.RegisterVersioned(rdsAPIVersion, "ModifyDBParameterGroup", handleRDSModifyParameterGroup)
	r.RegisterVersioned(rdsAPIVersion, "ResetDBParameterGroup", handleRDSResetParameterGroup)

	rdsClusters = sim.MakeStore[RDSCluster](srv.DB(), "rds_clusters")
	r.RegisterVersioned(rdsAPIVersion, "CreateDBCluster", handleRDSCreateCluster)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBClusters", handleRDSDescribeClusters)
	r.RegisterVersioned(rdsAPIVersion, "ModifyDBCluster", handleRDSModifyCluster)
	r.RegisterVersioned(rdsAPIVersion, "DeleteDBCluster", handleRDSDeleteCluster)
	r.RegisterVersioned(rdsAPIVersion, "StartDBCluster", handleRDSStartCluster)
	r.RegisterVersioned(rdsAPIVersion, "StopDBCluster", handleRDSStopCluster)
	r.RegisterVersioned(rdsAPIVersion, "FailoverDBCluster", handleRDSFailoverCluster)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBClusterParameters", handleRDSDescribeClusterParameters)
	r.RegisterVersioned(rdsAPIVersion, "ModifyDBClusterParameterGroup", handleRDSModifyClusterParameterGroup)

	rdsGlobalClusters = sim.MakeStore[RDSGlobalCluster](srv.DB(), "rds_global_clusters")
	r.RegisterVersioned(rdsAPIVersion, "CreateGlobalCluster", handleRDSCreateGlobalCluster)
	r.RegisterVersioned(rdsAPIVersion, "DescribeGlobalClusters", handleRDSDescribeGlobalClusters)
	r.RegisterVersioned(rdsAPIVersion, "ModifyGlobalCluster", handleRDSModifyGlobalCluster)
	r.RegisterVersioned(rdsAPIVersion, "DeleteGlobalCluster", handleRDSDeleteGlobalCluster)

	rdsEventSubscriptions = sim.MakeStore[RDSEventSubscription](srv.DB(), "rds_event_subscriptions")
	r.RegisterVersioned(rdsAPIVersion, "CreateEventSubscription", handleRDSCreateEventSubscription)
	r.RegisterVersioned(rdsAPIVersion, "DescribeEventSubscriptions", handleRDSDescribeEventSubscriptions)
	r.RegisterVersioned(rdsAPIVersion, "ModifyEventSubscription", handleRDSModifyEventSubscription)
	r.RegisterVersioned(rdsAPIVersion, "DeleteEventSubscription", handleRDSDeleteEventSubscription)

	rdsClusterEndpoints = sim.MakeStore[RDSClusterEndpoint](srv.DB(), "rds_cluster_endpoints")
	r.RegisterVersioned(rdsAPIVersion, "CreateDBClusterEndpoint", handleRDSCreateClusterEndpoint)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBClusterEndpoints", handleRDSDescribeClusterEndpoints)
	r.RegisterVersioned(rdsAPIVersion, "DeleteDBClusterEndpoint", handleRDSDeleteClusterEndpoint)

	rdsClusterSnapshots = sim.MakeStore[RDSClusterSnapshot](srv.DB(), "rds_cluster_snapshots")
	r.RegisterVersioned(rdsAPIVersion, "CreateDBClusterSnapshot", handleRDSCreateClusterSnapshot)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBClusterSnapshots", handleRDSDescribeClusterSnapshots)
	r.RegisterVersioned(rdsAPIVersion, "DeleteDBClusterSnapshot", handleRDSDeleteClusterSnapshot)
	r.RegisterVersioned(rdsAPIVersion, "CopyDBClusterSnapshot", handleRDSCopyClusterSnapshot)

	rdsClusterParamGroups = sim.MakeStore[RDSClusterParamGroup](srv.DB(), "rds_cluster_param_groups")
	r.RegisterVersioned(rdsAPIVersion, "CreateDBClusterParameterGroup", handleRDSCreateClusterParamGroup)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBClusterParameterGroups", handleRDSDescribeClusterParamGroups)
	r.RegisterVersioned(rdsAPIVersion, "DeleteDBClusterParameterGroup", handleRDSDeleteClusterParamGroup)

	rdsOptionGroups = sim.MakeStore[RDSOptionGroup](srv.DB(), "rds_option_groups")
	r.RegisterVersioned(rdsAPIVersion, "CreateOptionGroup", handleRDSCreateOptionGroup)
	r.RegisterVersioned(rdsAPIVersion, "DescribeOptionGroups", handleRDSDescribeOptionGroups)
	r.RegisterVersioned(rdsAPIVersion, "DeleteOptionGroup", handleRDSDeleteOptionGroup)

	r.RegisterVersioned(rdsAPIVersion, "DescribeEvents", handleRDSDescribeEvents)
	r.RegisterVersioned(rdsAPIVersion, "DescribeEventCategories", handleRDSDescribeEventCategories)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBEngineVersions", handleRDSDescribeEngineVersions)
	r.RegisterVersioned(rdsAPIVersion, "DescribeOrderableDBInstanceOptions", handleRDSDescribeOrderableOptions)

	rdsSubnetGroups = sim.MakeStore[RDSSubnetGroup](srv.DB(), "rds_subnet_groups")
	r.RegisterVersioned(rdsAPIVersion, "CreateDBSubnetGroup", handleRDSCreateSubnetGroup)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBSubnetGroups", handleRDSDescribeSubnetGroups)
	r.RegisterVersioned(rdsAPIVersion, "ModifyDBSubnetGroup", handleRDSModifySubnetGroup)
	r.RegisterVersioned(rdsAPIVersion, "DeleteDBSubnetGroup", handleRDSDeleteSubnetGroup)

	rdsParamGroups = sim.MakeStore[RDSParamGroup](srv.DB(), "rds_param_groups")
	r.RegisterVersioned(rdsAPIVersion, "CreateDBParameterGroup", handleRDSCreateParamGroup)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBParameterGroups", handleRDSDescribeParamGroups)
	r.RegisterVersioned(rdsAPIVersion, "DeleteDBParameterGroup", handleRDSDeleteParamGroup)

	registerRDSProxiesRoles(r, srv)
	registerRDSRestoreExtras(r, srv)
	registerRDSComplete(r, srv)
	if err := rdsRecoverDataPlanes(); err != nil {
		panic(fmt.Sprintf("restore Amazon Relational Database Service data planes: %v", err))
	}
}

func rdsInstanceARN(id string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:db:%s", awsRegion(), awsAccountID(), id)
}

func rdsResourceID() string {
	return "db-" + strings.ToUpper(strings.ReplaceAll(generateUUID(), "-", ""))[:26]
}

func rdsXMLResponse(w http.ResponseWriter, op string, body string, requestID string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w,
		`<%sResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><%sResult>%s</%sResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></%sResponse>`,
		op, op, body, op, requestID, op)
}

func rdsErrorXML(w http.ResponseWriter, code, message string, status int, requestID string) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w,
		`<ErrorResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>%s</RequestId></ErrorResponse>`,
		code, message, requestID)
}

func renderRDSInstance(i RDSInstance) string {
	var b strings.Builder
	b.WriteString("<DBInstance>")
	fmt.Fprintf(&b, "<DBInstanceIdentifier>%s</DBInstanceIdentifier>", xmlEscape(i.DBInstanceIdentifier))
	fmt.Fprintf(&b, "<DbiResourceId>%s</DbiResourceId>", xmlEscape(i.DbiResourceId))
	fmt.Fprintf(&b, "<DBInstanceClass>%s</DBInstanceClass>", xmlEscape(i.DBInstanceClass))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(i.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(i.EngineVersion))
	fmt.Fprintf(&b, "<DBInstanceStatus>%s</DBInstanceStatus>", xmlEscape(i.DBInstanceStatus))
	fmt.Fprintf(&b, "<MasterUsername>%s</MasterUsername>", xmlEscape(i.MasterUsername))
	fmt.Fprintf(&b, "<DBName>%s</DBName>", xmlEscape(i.DBName))
	fmt.Fprintf(&b, "<AllocatedStorage>%d</AllocatedStorage>", i.AllocatedStorage)
	fmt.Fprintf(&b, "<AvailabilityZone>%s</AvailabilityZone>", xmlEscape(i.AvailabilityZone))
	fmt.Fprintf(&b, "<InstanceCreateTime>%s</InstanceCreateTime>", xmlEscape(i.InstanceCreateTime))
	fmt.Fprintf(&b, "<DBInstanceArn>%s</DBInstanceArn>", xmlEscape(i.ARN))
	fmt.Fprintf(&b, "<Endpoint><Address>%s</Address><Port>%d</Port></Endpoint>", xmlEscape(i.Endpoint), i.Port)
	fmt.Fprintf(&b, "<IAMDatabaseAuthenticationEnabled>%t</IAMDatabaseAuthenticationEnabled>", i.EnableIAMDatabaseAuthentication)
	if i.ReadReplicaSource != "" {
		fmt.Fprintf(&b, "<ReadReplicaSourceDBInstanceIdentifier>%s</ReadReplicaSourceDBInstanceIdentifier>", xmlEscape(i.ReadReplicaSource))
	}
	b.WriteString("<ReadReplicaDBInstanceIdentifiers>")
	for _, rep := range i.ReadReplicas {
		fmt.Fprintf(&b, "<ReadReplicaDBInstanceIdentifier>%s</ReadReplicaDBInstanceIdentifier>", xmlEscape(rep))
	}
	b.WriteString("</ReadReplicaDBInstanceIdentifiers>")
	b.WriteString("</DBInstance>")
	return b.String()
}

func handleRDSCreate(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	if id == "" {
		rdsErrorXML(w, "MissingParameter", "DBInstanceIdentifier is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsInstances.Get(id); ok {
		rdsErrorXML(w, "DBInstanceAlreadyExists",
			"DB instance already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	engine := r.FormValue("Engine")
	port := rdsDefaultPort(engine)
	if p := atoiOrZero(r.FormValue("Port")); p > 0 {
		port = p
	}
	az := awsRegion() + "a"
	if v := r.FormValue("AvailabilityZone"); v != "" {
		az = v
	}
	engineVersion := r.FormValue("EngineVersion")
	if engineVersion == "" {
		engineVersion = rdsDefaultEngineVersion(engine)
	}
	inst := RDSInstance{
		DBInstanceIdentifier:            id,
		DbiResourceId:                   rdsResourceID(),
		DBInstanceClass:                 r.FormValue("DBInstanceClass"),
		Engine:                          engine,
		EngineVersion:                   engineVersion,
		DBInstanceStatus:                "available",
		MasterUsername:                  r.FormValue("MasterUsername"),
		DBName:                          r.FormValue("DBName"),
		AllocatedStorage:                atoiOrZero(r.FormValue("AllocatedStorage")),
		Endpoint:                        fmt.Sprintf("%s.%s.rds.amazonaws.com", id, awsRegion()),
		Port:                            port,
		AvailabilityZone:                az,
		InstanceCreateTime:              time.Now().UTC().Format(time.RFC3339),
		ARN:                             rdsInstanceARN(id),
		Tags:                            parseAWSQueryTagMap(r, "Tags.Tag"),
		EnableIAMDatabaseAuthentication: strings.EqualFold(r.FormValue("EnableIAMDatabaseAuthentication"), "true"),
	}
	if err := rdsInstallDataPlane(&inst, r.FormValue("MasterUserPassword")); err != nil {
		rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
		return
	}
	rdsInstances.Put(id, inst)
	rdsXMLResponse(w, "CreateDBInstance", renderRDSInstance(inst), sim.RequestID(r.Context()))
}

func handleRDSDescribe(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("DBInstanceIdentifier")
	wantedResourceID := rdsFilterValue(r, "dbi-resource-id")
	matched := false
	var b strings.Builder
	b.WriteString("<DBInstances>")
	for _, i := range rdsInstances.List() {
		if wanted != "" && i.DBInstanceIdentifier != wanted && i.DbiResourceId != wanted {
			continue
		}
		if wantedResourceID != "" && i.DbiResourceId != wantedResourceID {
			continue
		}
		matched = true
		b.WriteString(renderRDSInstance(i))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "DBInstanceNotFound",
			fmt.Sprintf("DBInstance %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBInstances>")
	rdsXMLResponse(w, "DescribeDBInstances", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModify(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	instance, ok := rdsInstances.Get(id)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound", "DB instance not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if value := r.FormValue("DBInstanceClass"); value != "" {
		instance.DBInstanceClass = value
	}
	if value := r.FormValue("AllocatedStorage"); value != "" {
		instance.AllocatedStorage = atoiOrZero(value)
	}
	if value := r.FormValue("EngineVersion"); value != "" {
		instance.EngineVersion = value
	}
	if value := r.FormValue("EnableIAMDatabaseAuthentication"); value != "" {
		instance.EnableIAMDatabaseAuthentication = strings.EqualFold(value, "true")
	}
	var newPassword *string
	if value := r.FormValue("MasterUserPassword"); value != "" {
		newPassword = &value
	}
	if err := rdsModifyDataPlaneAuthentication(&instance, newPassword); err != nil {
		rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
		return
	}
	rdsInstances.Put(id, instance)
	rdsXMLResponse(w, "ModifyDBInstance", renderRDSInstance(instance), sim.RequestID(r.Context()))
}

func handleRDSDelete(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	inst, ok := rdsInstances.Get(id)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound", "DB instance not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	skipFinal := strings.EqualFold(r.FormValue("SkipFinalSnapshot"), "true")
	finalSnapID := r.FormValue("FinalDBSnapshotIdentifier")
	if skipFinal && finalSnapID != "" {
		rdsErrorXML(w, "InvalidParameterCombination",
			"FinalDBSnapshotIdentifier cannot be specified when SkipFinalSnapshot is true",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if !skipFinal && finalSnapID == "" {
		rdsErrorXML(w, "InvalidParameterCombination",
			"FinalDBSnapshotIdentifier is required unless SkipFinalSnapshot is specified",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if finalSnapID != "" {
		if _, exists := rdsSnapshots.Get(finalSnapID); exists {
			rdsErrorXML(w, "DBSnapshotAlreadyExists",
				fmt.Sprintf("DBSnapshot %q already exists", finalSnapID),
				http.StatusConflict, sim.RequestID(r.Context()))
			return
		}
		rdsSnapshots.Put(finalSnapID, RDSSnapshot{
			DBSnapshotIdentifier: finalSnapID,
			DBInstanceIdentifier: id,
			DbiResourceId:        inst.DbiResourceId,
			Engine:               inst.Engine,
			EngineVersion:        inst.EngineVersion,
			Status:               "creating",
			AllocatedStorage:     inst.AllocatedStorage,
			MasterUsername:       inst.MasterUsername,
			SnapshotCreateTime:   time.Now().UTC().Format(time.RFC3339),
			SnapshotType:         "manual",
			Port:                 inst.Port,
			ARN:                  rdsSnapshotARN(finalSnapID),
			MasterUserSecret:     append([]byte(nil), inst.MasterUserSecret...),
		})
	}
	inst.DBInstanceStatus = "deleting"
	rdsInstances.Delete(id)
	if finalSnapID != "" {
		// The final snapshot captures the instance's volume before the data
		// plane and the volume go away — the capture must finish first, so
		// the shutdown runs after it in the same background task.
		snapID := finalSnapID
		simGo(func() {
			rdsCaptureSnapshotData(snapID, id)
			rdsStopDataPlane(id, true)
		})
	} else {
		rdsStopDataPlane(id, true)
	}
	rdsXMLResponse(w, "DeleteDBInstance", renderRDSInstance(inst), sim.RequestID(r.Context()))
}

func handleRDSAddTags(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceName")
	inst, ok := findRDSByARN(arn)
	if ok {
		rdsInstances.Update(inst.DBInstanceIdentifier, func(i *RDSInstance) {
			i.Tags = mergeTags(i.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	snap, ok := findRDSSnapshotByARN(arn)
	if ok {
		rdsSnapshots.Update(snap.DBSnapshotIdentifier, func(s *RDSSnapshot) {
			s.Tags = mergeTags(s.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	if cl, ok := findRDSClusterByARN(arn); ok {
		rdsClusters.Update(cl.DBClusterIdentifier, func(c *RDSCluster) {
			c.Tags = mergeTags(c.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	if sg, ok := findRDSSubnetGroupByARN(arn); ok {
		rdsSubnetGroups.Update(sg.DBSubnetGroupName, func(g *RDSSubnetGroup) {
			g.Tags = mergeTags(g.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	if pg, ok := findRDSParamGroupByARN(arn); ok {
		rdsParamGroups.Update(pg.DBParameterGroupName, func(g *RDSParamGroup) {
			g.Tags = mergeTags(g.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	if cs, ok := findRDSClusterSnapshotByARN(arn); ok {
		rdsClusterSnapshots.Update(cs.DBClusterSnapshotIdentifier, func(s *RDSClusterSnapshot) {
			s.Tags = mergeTags(s.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	if cpg, ok := findRDSClusterParamGroupByARN(arn); ok {
		rdsClusterParamGroups.Update(cpg.DBClusterParameterGroupName, func(g *RDSClusterParamGroup) {
			g.Tags = mergeTags(g.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	if og, ok := findRDSOptionGroupByARN(arn); ok {
		rdsOptionGroups.Update(og.OptionGroupName, func(g *RDSOptionGroup) {
			g.Tags = mergeTags(g.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	rdsErrorXML(w, rdsTagResourceNotFoundCode(arn), "Resource not found", http.StatusNotFound, sim.RequestID(r.Context()))
}

func handleRDSListTags(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceName")
	inst, ok := findRDSByARN(arn)
	if ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(inst.Tags), sim.RequestID(r.Context()))
		return
	}
	snap, ok := findRDSSnapshotByARN(arn)
	if ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(snap.Tags), sim.RequestID(r.Context()))
		return
	}
	if cl, ok := findRDSClusterByARN(arn); ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(cl.Tags), sim.RequestID(r.Context()))
		return
	}
	if sg, ok := findRDSSubnetGroupByARN(arn); ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(sg.Tags), sim.RequestID(r.Context()))
		return
	}
	if pg, ok := findRDSParamGroupByARN(arn); ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(pg.Tags), sim.RequestID(r.Context()))
		return
	}
	if cs, ok := findRDSClusterSnapshotByARN(arn); ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(cs.Tags), sim.RequestID(r.Context()))
		return
	}
	if cpg, ok := findRDSClusterParamGroupByARN(arn); ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(cpg.Tags), sim.RequestID(r.Context()))
		return
	}
	if og, ok := findRDSOptionGroupByARN(arn); ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(og.Tags), sim.RequestID(r.Context()))
		return
	}
	rdsErrorXML(w, rdsTagResourceNotFoundCode(arn), "Resource not found", http.StatusNotFound, sim.RequestID(r.Context()))
}

func renderRDSTagList(tags map[string]string) string {
	var b strings.Builder
	b.WriteString("<TagList>")
	for k, v := range tags {
		fmt.Fprintf(&b, "<Tag><Key>%s</Key><Value>%s</Value></Tag>", xmlEscape(k), xmlEscape(v))
	}
	b.WriteString("</TagList>")
	return b.String()
}

func handleRDSRemoveTags(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceName")
	inst, ok := findRDSByARN(arn)
	if ok {
		rdsInstances.Update(inst.DBInstanceIdentifier, func(i *RDSInstance) {
			removeAWSQueryTags(i.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	snap, ok := findRDSSnapshotByARN(arn)
	if ok {
		rdsSnapshots.Update(snap.DBSnapshotIdentifier, func(s *RDSSnapshot) {
			removeAWSQueryTags(s.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	if cl, ok := findRDSClusterByARN(arn); ok {
		rdsClusters.Update(cl.DBClusterIdentifier, func(c *RDSCluster) {
			removeAWSQueryTags(c.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	if sg, ok := findRDSSubnetGroupByARN(arn); ok {
		rdsSubnetGroups.Update(sg.DBSubnetGroupName, func(g *RDSSubnetGroup) {
			removeAWSQueryTags(g.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	if pg, ok := findRDSParamGroupByARN(arn); ok {
		rdsParamGroups.Update(pg.DBParameterGroupName, func(g *RDSParamGroup) {
			removeAWSQueryTags(g.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	if cs, ok := findRDSClusterSnapshotByARN(arn); ok {
		rdsClusterSnapshots.Update(cs.DBClusterSnapshotIdentifier, func(s *RDSClusterSnapshot) {
			removeAWSQueryTags(s.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	if cpg, ok := findRDSClusterParamGroupByARN(arn); ok {
		rdsClusterParamGroups.Update(cpg.DBClusterParameterGroupName, func(g *RDSClusterParamGroup) {
			removeAWSQueryTags(g.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	if og, ok := findRDSOptionGroupByARN(arn); ok {
		rdsOptionGroups.Update(og.OptionGroupName, func(g *RDSOptionGroup) {
			removeAWSQueryTags(g.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	rdsErrorXML(w, rdsTagResourceNotFoundCode(arn), "Resource not found", http.StatusNotFound, sim.RequestID(r.Context()))
}

func findRDSByARN(arn string) (RDSInstance, bool) {
	for _, i := range rdsInstances.List() {
		if i.ARN == arn {
			return i, true
		}
	}
	// Some callers pass the instance identifier directly.
	if i, ok := rdsInstances.Get(arn); ok {
		return i, true
	}
	return RDSInstance{}, false
}

func findRDSSnapshotByARN(arn string) (RDSSnapshot, bool) {
	for _, s := range rdsSnapshots.List() {
		if s.ARN == arn {
			return s, true
		}
	}
	if s, ok := rdsSnapshots.Get(arn); ok {
		return s, true
	}
	return RDSSnapshot{}, false
}

func rdsTagResourceNotFoundCode(arn string) string {
	switch {
	case strings.Contains(arn, ":cluster-snapshot:"):
		return "DBClusterSnapshotNotFoundFault"
	case strings.Contains(arn, ":snapshot:"):
		return "DBSnapshotNotFound"
	case strings.Contains(arn, ":cluster-pg:"):
		return "DBClusterParameterGroupNotFound"
	case strings.Contains(arn, ":cluster:"):
		return "DBClusterNotFoundFault"
	case strings.Contains(arn, ":subgrp:"):
		return "DBSubnetGroupNotFoundFault"
	case strings.Contains(arn, ":pg:"):
		return "DBParameterGroupNotFound"
	case strings.Contains(arn, ":og:"):
		return "OptionGroupNotFoundFault"
	}
	return "DBInstanceNotFound"
}

func atoiOrZero(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func parseAWSQueryTagMap(r *http.Request, prefix string) map[string]string {
	tags := map[string]string{}
	for n := 1; n <= 50; n++ {
		k := r.FormValue(fmt.Sprintf("%s.%d.Key", prefix, n))
		v := r.FormValue(fmt.Sprintf("%s.%d.Value", prefix, n))
		if k == "" {
			break
		}
		tags[k] = v
	}
	return tags
}

func rdsFilterValue(r *http.Request, name string) string {
	for n := 1; n <= 50; n++ {
		prefix := fmt.Sprintf("Filters.Filter.%d", n)
		filterName := r.FormValue(prefix + ".Name")
		if filterName == "" {
			break
		}
		if filterName != name {
			continue
		}
		return r.FormValue(prefix + ".Values.Value.1")
	}
	return ""
}

func mergeTags(dst map[string]string, src map[string]string) map[string]string {
	if dst == nil {
		dst = map[string]string{}
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func removeAWSQueryTags(tags map[string]string, r *http.Request) {
	for n := 1; n <= 50; n++ {
		k := r.FormValue(fmt.Sprintf("TagKeys.member.%d", n))
		if k == "" {
			break
		}
		delete(tags, k)
	}
}

// rdsDefaultEngineVersion returns the engine's current GA major
// version when the request omits EngineVersion. Real RDS resolves
// the default server-side and includes it in the CreateDBInstance
// response; the terraform-provider-aws resource captures the
// resolved version into state, so an empty echo persists as `""`
// and surfaces as state drift on the next plan.
//
// Versions kept current as of mid-2026 GA releases. New majors land
// rarely (1-2× per year); update here when they ship.
// rdsDefaultPort returns the engine's default listener port, matching what RDS
// assigns when no explicit Port is given (and what a snapshot restore inherits
// from the source engine).
func rdsDefaultPort(engine string) int {
	switch engine {
	case "postgres", "aurora-postgresql":
		return 5432
	case "sqlserver-ex", "sqlserver-se", "sqlserver-ee", "sqlserver-web":
		return 1433
	case "oracle-ee", "oracle-se2":
		return 1521
	default: // mysql, aurora, aurora-mysql, mariadb, and unknown engines
		return 3306
	}
}

func rdsDefaultEngineVersion(engine string) string {
	switch engine {
	case "postgres":
		return "17.5"
	case "mysql":
		return "8.0.40"
	case "mariadb":
		return "11.4.4"
	case "aurora-postgresql":
		return "16.6"
	case "aurora-mysql":
		return "8.0.mysql_aurora.3.07.0"
	case "oracle-se2":
		return "19.0.0.0.ru-2024-10.rur-2024-10.r1"
	case "oracle-ee":
		return "19.0.0.0.ru-2024-10.rur-2024-10.r1"
	case "sqlserver-ex", "sqlserver-web", "sqlserver-se", "sqlserver-ee":
		return "16.00.4150.1.v1"
	}
	return ""
}

func rdsSnapshotARN(id string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:snapshot:%s", awsRegion(), awsAccountID(), id)
}

func renderRDSSnapshot(s RDSSnapshot) string {
	var b strings.Builder
	b.WriteString("<DBSnapshot>")
	fmt.Fprintf(&b, "<DBSnapshotIdentifier>%s</DBSnapshotIdentifier>", xmlEscape(s.DBSnapshotIdentifier))
	fmt.Fprintf(&b, "<DBInstanceIdentifier>%s</DBInstanceIdentifier>", xmlEscape(s.DBInstanceIdentifier))
	fmt.Fprintf(&b, "<DbiResourceId>%s</DbiResourceId>", xmlEscape(s.DbiResourceId))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(s.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(s.EngineVersion))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(s.Status))
	fmt.Fprintf(&b, "<AllocatedStorage>%d</AllocatedStorage>", s.AllocatedStorage)
	fmt.Fprintf(&b, "<MasterUsername>%s</MasterUsername>", xmlEscape(s.MasterUsername))
	fmt.Fprintf(&b, "<SnapshotCreateTime>%s</SnapshotCreateTime>", xmlEscape(s.SnapshotCreateTime))
	fmt.Fprintf(&b, "<SnapshotType>%s</SnapshotType>", xmlEscape(s.SnapshotType))
	fmt.Fprintf(&b, "<Port>%d</Port>", s.Port)
	fmt.Fprintf(&b, "<VpcId>%s</VpcId>", xmlEscape(s.VpcId))
	fmt.Fprintf(&b, "<DBSnapshotArn>%s</DBSnapshotArn>", xmlEscape(s.ARN))
	if s.SourceDBSnapshotIdentifier != "" {
		fmt.Fprintf(&b, "<SourceDBSnapshotIdentifier>%s</SourceDBSnapshotIdentifier>", xmlEscape(s.SourceDBSnapshotIdentifier))
	}
	b.WriteString("</DBSnapshot>")
	return b.String()
}

func handleRDSCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("DBSnapshotIdentifier")
	instID := r.FormValue("DBInstanceIdentifier")
	if snapID == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBSnapshotIdentifier is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	inst, ok := rdsInstances.Get(instID)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound",
			fmt.Sprintf("DBInstance %q not found", instID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if _, exists := rdsSnapshots.Get(snapID); exists {
		rdsErrorXML(w, "DBSnapshotAlreadyExists",
			fmt.Sprintf("DBSnapshot %q already exists", snapID),
			http.StatusConflict, sim.RequestID(r.Context()))
		return
	}
	snap := RDSSnapshot{
		DBSnapshotIdentifier: snapID,
		DBInstanceIdentifier: instID,
		DbiResourceId:        inst.DbiResourceId,
		Engine:               inst.Engine,
		EngineVersion:        inst.EngineVersion,
		// The snapshot answers "creating" and settles asynchronously once
		// the instance's volume is captured — real work now backs the state
		// machine: the capture is copy-on-write where the engine's volume
		// store supports block cloning, and a full copy elsewhere, either way
		// a complete capture of the data.
		Status:             "creating",
		AllocatedStorage:   inst.AllocatedStorage,
		MasterUsername:     inst.MasterUsername,
		SnapshotCreateTime: time.Now().UTC().Format(time.RFC3339),
		SnapshotType:       "manual",
		Port:               inst.Port,
		ARN:                rdsSnapshotARN(snapID),
		Tags:               parseAWSQueryTagMap(r, "Tags.Tag"),
		// The master credential travels with the data, as it does in RDS:
		// the restored engine expects the credentials the data was written
		// under.
		MasterUserSecret: append([]byte(nil), inst.MasterUserSecret...),
	}
	rdsSnapshots.Put(snapID, snap)
	simGo(func() { rdsCaptureSnapshotData(snapID, instID) })
	rdsXMLResponse(w, "CreateDBSnapshot", renderRDSSnapshot(snap), sim.RequestID(r.Context()))
}

func handleRDSDescribeSnapshots(w http.ResponseWriter, r *http.Request) {
	filterID := r.FormValue("DBSnapshotIdentifier")
	filterInst := r.FormValue("DBInstanceIdentifier")
	matched := false
	var b strings.Builder
	b.WriteString("<DBSnapshots>")
	for _, s := range rdsSnapshots.List() {
		if filterID != "" && s.DBSnapshotIdentifier != filterID {
			continue
		}
		if filterInst != "" && s.DBInstanceIdentifier != filterInst {
			continue
		}
		matched = true
		b.WriteString(renderRDSSnapshot(s))
	}
	if filterID != "" && !matched {
		rdsErrorXML(w, "DBSnapshotNotFound",
			fmt.Sprintf("DBSnapshot %q not found", filterID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBSnapshots>")
	rdsXMLResponse(w, "DescribeDBSnapshots", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDescribeSnapshotAttributes(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("DBSnapshotIdentifier")
	if snapID == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBSnapshotIdentifier is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	snap, ok := rdsSnapshots.Get(snapID)
	if !ok {
		snap, ok = findRDSSnapshotByARN(snapID)
		if !ok {
			rdsErrorXML(w, "DBSnapshotNotFound",
				fmt.Sprintf("DBSnapshot %q not found", snapID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	rdsXMLResponse(w, "DescribeDBSnapshotAttributes", renderRDSSnapshotAttributesResult(snap), sim.RequestID(r.Context()))
}

func handleRDSDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("DBSnapshotIdentifier")
	snap, ok := rdsSnapshots.Get(snapID)
	if !ok {
		snap, ok = findRDSSnapshotByARN(snapID)
		if !ok {
			rdsErrorXML(w, "DBSnapshotNotFound",
				fmt.Sprintf("DBSnapshot %q not found", snapID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	rdsSnapshots.Delete(snap.DBSnapshotIdentifier)
	// The captured data goes with the record: keeping the volume would leak
	// storage the API says no longer exists. On the modeled tier no volume
	// was ever created, and removing a volume without an engine is refused
	// inside RemoveVolume, so the error is expected and dropped there.
	if sim.RequireContainerRuntime("deleting an RDS snapshot volume") == nil {
		_ = sim.RemoveVolume(rdsSnapshotVolume(snap.DBSnapshotIdentifier))
	}
	// Real RDS returns the snapshot with Status="deleted" in the
	// response (it's the final state machine transition before
	// removal). Match that.
	snap.Status = "deleted"
	rdsXMLResponse(w, "DeleteDBSnapshot", renderRDSSnapshot(snap), sim.RequestID(r.Context()))
}

func handleRDSRestoreFromSnapshot(w http.ResponseWriter, r *http.Request) {
	newInstID := r.FormValue("DBInstanceIdentifier")
	snapID := r.FormValue("DBSnapshotIdentifier")
	if newInstID == "" || snapID == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBInstanceIdentifier and DBSnapshotIdentifier are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	snap, ok := rdsSnapshots.Get(snapID)
	if !ok {
		snap, ok = findRDSSnapshotByARN(snapID)
		if !ok {
			rdsErrorXML(w, "DBSnapshotNotFound",
				fmt.Sprintf("DBSnapshot %q not found", snapID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	if _, exists := rdsInstances.Get(newInstID); exists {
		rdsErrorXML(w, "DBInstanceAlreadyExists",
			fmt.Sprintf("DBInstance %q already exists", newInstID),
			http.StatusConflict, sim.RequestID(r.Context()))
		return
	}
	if snap.Status != "available" {
		rdsErrorXML(w, "InvalidDBSnapshotState",
			fmt.Sprintf("DBSnapshot %q is %s; it must be available to restore from", snapID, snap.Status),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	inst := RDSInstance{
		DBInstanceIdentifier: newInstID,
		DbiResourceId:        rdsResourceID(),
		DBInstanceClass:      r.FormValue("DBInstanceClass"),
		Engine:               snap.Engine,
		EngineVersion:        snap.EngineVersion,
		DBInstanceStatus:     "available",
		MasterUsername:       snap.MasterUsername,
		AllocatedStorage:     snap.AllocatedStorage,
		AvailabilityZone:     awsRegion() + "a",
		InstanceCreateTime:   time.Now().UTC().Format(time.RFC3339),
		ARN:                  rdsInstanceARN(newInstID),
		Tags:                 parseAWSQueryTagMap(r, "Tags.Tag"),
		// The engine starts with the credentials the captured data was
		// written under, exactly as a restored RDS instance does.
		MasterUserSecret: append([]byte(nil), snap.MasterUserSecret...),
	}
	// The captured data is cloned into the new instance's volume before the
	// engine can first start, so the restored engine boots on the snapshot's
	// data. A snapshot taken on the modeled tier has no volume and seeds
	// nothing, leaving the restored instance as modeled as its source.
	if err := rdsCloneSnapshotIntoInstance(snap.DBSnapshotIdentifier, newInstID); err != nil {
		rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
		return
	}
	if len(inst.MasterUserSecret) > 0 {
		_, password, decrypted := kmsDecryptBytes(inst.MasterUserSecret)
		if !decrypted {
			rdsErrorXML(w, "ProvisioningFailure",
				"the snapshot's master-user credential could not be decrypted",
				http.StatusInternalServerError, sim.RequestID(r.Context()))
			return
		}
		if err := rdsInstallDataPlane(&inst, string(password)); err != nil {
			rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
			return
		}
	} else {
		// A snapshot from the modeled tier carries no credential and gets no
		// data plane, the same as its source had.
		inst.Endpoint = fmt.Sprintf("%s.%s.rds.amazonaws.com", newInstID, awsRegion())
		inst.Port = rdsDefaultPort(snap.Engine)
	}
	rdsInstances.Put(newInstID, inst)
	rdsXMLResponse(w, "RestoreDBInstanceFromDBSnapshot", renderRDSInstance(inst), sim.RequestID(r.Context()))
}

func handleRDSReboot(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	inst, ok := rdsInstances.Get(id)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound", "DB instance not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if len(inst.MasterUserSecret) > 0 {
		rdsStopDataPlane(id, false)
		_, password, decrypted := kmsDecryptBytes(inst.MasterUserSecret)
		if !decrypted {
			rdsErrorXML(w, "ProvisioningFailure", "RDS master-user credential could not be decrypted", http.StatusInternalServerError, sim.RequestID(r.Context()))
			return
		}
		if err := rdsInstallDataPlane(&inst, string(password)); err != nil {
			rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
			return
		}
		rdsInstances.Put(id, inst)
	}
	rdsXMLResponse(w, "RebootDBInstance", renderRDSInstance(inst), sim.RequestID(r.Context()))
}

func rdsClusterARN(id string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:cluster:%s", awsRegion(), awsAccountID(), id)
}

func rdsClusterResourceID() string {
	return "cluster-" + strings.ToUpper(strings.ReplaceAll(generateUUID(), "-", ""))[:26]
}

func findRDSClusterByARN(arn string) (RDSCluster, bool) {
	for _, c := range rdsClusters.List() {
		if c.ARN == arn {
			return c, true
		}
	}
	if c, ok := rdsClusters.Get(arn); ok {
		return c, true
	}
	return RDSCluster{}, false
}

func renderRDSCluster(c RDSCluster) string {
	var b strings.Builder
	b.WriteString("<DBCluster>")
	fmt.Fprintf(&b, "<DBClusterIdentifier>%s</DBClusterIdentifier>", xmlEscape(c.DBClusterIdentifier))
	fmt.Fprintf(&b, "<DbClusterResourceId>%s</DbClusterResourceId>", xmlEscape(c.DbClusterResourceId))
	fmt.Fprintf(&b, "<DBClusterArn>%s</DBClusterArn>", xmlEscape(c.ARN))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(c.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(c.EngineVersion))
	fmt.Fprintf(&b, "<EngineMode>%s</EngineMode>", xmlEscape(c.EngineMode))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(c.Status))
	fmt.Fprintf(&b, "<DatabaseName>%s</DatabaseName>", xmlEscape(c.DatabaseName))
	fmt.Fprintf(&b, "<MasterUsername>%s</MasterUsername>", xmlEscape(c.MasterUsername))
	fmt.Fprintf(&b, "<Port>%d</Port>", c.Port)
	fmt.Fprintf(&b, "<Endpoint>%s</Endpoint>", xmlEscape(c.Endpoint))
	fmt.Fprintf(&b, "<ReaderEndpoint>%s</ReaderEndpoint>", xmlEscape(c.ReaderEndpoint))
	fmt.Fprintf(&b, "<DBClusterParameterGroup>%s</DBClusterParameterGroup>", xmlEscape(c.DBClusterParameterGroup))
	fmt.Fprintf(&b, "<DBSubnetGroup>%s</DBSubnetGroup>", xmlEscape(c.DBSubnetGroup))
	fmt.Fprintf(&b, "<AllocatedStorage>%d</AllocatedStorage>", c.AllocatedStorage)
	fmt.Fprintf(&b, "<BackupRetentionPeriod>%d</BackupRetentionPeriod>", c.BackupRetentionPeriod)
	fmt.Fprintf(&b, "<StorageEncrypted>%t</StorageEncrypted>", c.StorageEncrypted)
	fmt.Fprintf(&b, "<DeletionProtection>%t</DeletionProtection>", c.DeletionProtection)
	fmt.Fprintf(&b, "<ClusterCreateTime>%s</ClusterCreateTime>", xmlEscape(c.ClusterCreateTime))
	fmt.Fprintf(&b, "<PreferredBackupWindow>%s</PreferredBackupWindow>", xmlEscape(c.PreferredBackupWindow))
	fmt.Fprintf(&b, "<PreferredMaintenanceWindow>%s</PreferredMaintenanceWindow>", xmlEscape(c.PreferredMaintenanceWindow))
	b.WriteString("<AvailabilityZones>")
	for _, az := range c.AvailabilityZones {
		fmt.Fprintf(&b, "<AvailabilityZone>%s</AvailabilityZone>", xmlEscape(az))
	}
	b.WriteString("</AvailabilityZones>")
	b.WriteString("</DBCluster>")
	return b.String()
}

func handleRDSCreateCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	if id == "" {
		rdsErrorXML(w, "MissingParameter", "DBClusterIdentifier is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsClusters.Get(id); ok {
		rdsErrorXML(w, "DBClusterAlreadyExistsFault", "DB cluster already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	engine := r.FormValue("Engine")
	port := rdsDefaultPort(engine)
	if p := atoiOrZero(r.FormValue("Port")); p > 0 {
		port = p
	}
	engineVersion := r.FormValue("EngineVersion")
	if engineVersion == "" {
		engineVersion = rdsDefaultEngineVersion(engine)
	}
	engineMode := r.FormValue("EngineMode")
	if engineMode == "" {
		engineMode = "provisioned"
	}
	paramGroup := r.FormValue("DBClusterParameterGroupName")
	if paramGroup == "" {
		paramGroup = "default." + engine
	}
	backupRetention := 1
	if v := r.FormValue("BackupRetentionPeriod"); v != "" {
		backupRetention = atoiOrZero(v)
	}
	cl := RDSCluster{
		DBClusterIdentifier:        id,
		DbClusterResourceId:        rdsClusterResourceID(),
		Engine:                     engine,
		EngineVersion:              engineVersion,
		EngineMode:                 engineMode,
		Status:                     "available",
		DatabaseName:               r.FormValue("DatabaseName"),
		MasterUsername:             r.FormValue("MasterUsername"),
		Port:                       port,
		Endpoint:                   fmt.Sprintf("%s.cluster-%s.%s.rds.amazonaws.com", id, "sim", awsRegion()),
		ReaderEndpoint:             fmt.Sprintf("%s.cluster-ro-%s.%s.rds.amazonaws.com", id, "sim", awsRegion()),
		DBClusterParameterGroup:    paramGroup,
		DBSubnetGroup:              r.FormValue("DBSubnetGroupName"),
		AllocatedStorage:           atoiOrZero(r.FormValue("AllocatedStorage")),
		BackupRetentionPeriod:      backupRetention,
		StorageEncrypted:           r.FormValue("StorageEncrypted") == "true",
		DeletionProtection:         r.FormValue("DeletionProtection") == "true",
		ClusterCreateTime:          time.Now().UTC().Format(time.RFC3339),
		AvailabilityZones:          []string{awsRegion() + "a", awsRegion() + "b", awsRegion() + "c"},
		PreferredBackupWindow:      "07:00-09:00",
		PreferredMaintenanceWindow: "mon:00:00-mon:03:00",
		ARN:                        rdsClusterARN(id),
		Tags:                       parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsClusters.Put(id, cl)
	rdsXMLResponse(w, "CreateDBCluster", renderRDSCluster(cl), sim.RequestID(r.Context()))
}

func handleRDSDescribeClusters(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("DBClusterIdentifier")
	matched := false
	var b strings.Builder
	b.WriteString("<DBClusters>")
	for _, c := range rdsClusters.List() {
		if wanted != "" && c.DBClusterIdentifier != wanted && c.ARN != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSCluster(c))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "DBClusterNotFoundFault",
			fmt.Sprintf("DBCluster %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBClusters>")
	rdsXMLResponse(w, "DescribeDBClusters", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	if _, ok := rdsClusters.Get(id); !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault", "DB cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsClusters.Update(id, func(c *RDSCluster) {
		if v := r.FormValue("EngineVersion"); v != "" {
			c.EngineVersion = v
		}
		if v := r.FormValue("BackupRetentionPeriod"); v != "" {
			c.BackupRetentionPeriod = atoiOrZero(v)
		}
		if v := r.FormValue("PreferredBackupWindow"); v != "" {
			c.PreferredBackupWindow = v
		}
		if v := r.FormValue("PreferredMaintenanceWindow"); v != "" {
			c.PreferredMaintenanceWindow = v
		}
		if v := r.FormValue("DeletionProtection"); v != "" {
			c.DeletionProtection = v == "true"
		}
		if v := r.FormValue("Port"); v != "" {
			if p := atoiOrZero(v); p > 0 {
				c.Port = p
			}
		}
	})
	updated, _ := rdsClusters.Get(id)
	rdsXMLResponse(w, "ModifyDBCluster", renderRDSCluster(updated), sim.RequestID(r.Context()))
}

func handleRDSDeleteCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	cl, ok := rdsClusters.Get(id)
	if !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault", "DB cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	skipFinal := strings.EqualFold(r.FormValue("SkipFinalSnapshot"), "true")
	finalSnapID := r.FormValue("FinalDBSnapshotIdentifier")
	if skipFinal && finalSnapID != "" {
		rdsErrorXML(w, "InvalidParameterCombination",
			"FinalDBSnapshotIdentifier cannot be specified when SkipFinalSnapshot is true",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if !skipFinal && finalSnapID == "" {
		rdsErrorXML(w, "InvalidParameterCombination",
			"FinalDBSnapshotIdentifier is required unless SkipFinalSnapshot is specified",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if finalSnapID != "" {
		if _, exists := rdsClusterSnapshots.Get(finalSnapID); exists {
			rdsErrorXML(w, "DBClusterSnapshotAlreadyExistsFault",
				fmt.Sprintf("DBClusterSnapshot %q already exists", finalSnapID),
				http.StatusConflict, sim.RequestID(r.Context()))
			return
		}
		// As modeled as every cluster snapshot: clusters hold no engine
		// volume, so the final snapshot is the same metadata tier
		// CreateDBClusterSnapshot records.
		rdsClusterSnapshots.Put(finalSnapID, RDSClusterSnapshot{
			DBClusterSnapshotIdentifier: finalSnapID,
			DBClusterIdentifier:         id,
			DbClusterResourceId:         cl.DbClusterResourceId,
			Engine:                      cl.Engine,
			EngineVersion:               cl.EngineVersion,
			EngineMode:                  cl.EngineMode,
			Status:                      "available",
			AllocatedStorage:            cl.AllocatedStorage,
			MasterUsername:              cl.MasterUsername,
			Port:                        cl.Port,
			StorageEncrypted:            cl.StorageEncrypted,
			SnapshotCreateTime:          time.Now().UTC().Format(time.RFC3339),
			ClusterCreateTime:           cl.ClusterCreateTime,
			SnapshotType:                "manual",
			PercentProgress:             100,
			AvailabilityZones:           cl.AvailabilityZones,
			ARN:                         rdsClusterSnapshotARN(finalSnapID),
		})
	}
	rdsClusters.Delete(id)
	cl.Status = "deleting"
	rdsXMLResponse(w, "DeleteDBCluster", renderRDSCluster(cl), sim.RequestID(r.Context()))
}

func rdsSubnetGroupARN(name string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:subgrp:%s", awsRegion(), awsAccountID(), name)
}

func findRDSSubnetGroupByARN(arn string) (RDSSubnetGroup, bool) {
	for _, g := range rdsSubnetGroups.List() {
		if g.ARN == arn {
			return g, true
		}
	}
	if g, ok := rdsSubnetGroups.Get(arn); ok {
		return g, true
	}
	return RDSSubnetGroup{}, false
}

func parseRDSSubnetIDs(r *http.Request) []string {
	var ids []string
	for n := 1; n <= 50; n++ {
		v := r.FormValue(fmt.Sprintf("SubnetIds.SubnetIdentifier.%d", n))
		if v == "" {
			break
		}
		ids = append(ids, v)
	}
	return ids
}

func renderRDSSubnetGroup(g RDSSubnetGroup) string {
	var b strings.Builder
	b.WriteString("<DBSubnetGroup>")
	fmt.Fprintf(&b, "<DBSubnetGroupName>%s</DBSubnetGroupName>", xmlEscape(g.DBSubnetGroupName))
	fmt.Fprintf(&b, "<DBSubnetGroupDescription>%s</DBSubnetGroupDescription>", xmlEscape(g.DBSubnetGroupDescription))
	fmt.Fprintf(&b, "<VpcId>%s</VpcId>", xmlEscape(g.VpcId))
	fmt.Fprintf(&b, "<SubnetGroupStatus>%s</SubnetGroupStatus>", xmlEscape(g.SubnetGroupStatus))
	fmt.Fprintf(&b, "<DBSubnetGroupArn>%s</DBSubnetGroupArn>", xmlEscape(g.ARN))
	b.WriteString("<Subnets>")
	for i, sid := range g.SubnetIds {
		az := awsRegion() + string(rune('a'+i%3))
		b.WriteString("<Subnet>")
		fmt.Fprintf(&b, "<SubnetIdentifier>%s</SubnetIdentifier>", xmlEscape(sid))
		fmt.Fprintf(&b, "<SubnetAvailabilityZone><Name>%s</Name></SubnetAvailabilityZone>", xmlEscape(az))
		b.WriteString("<SubnetStatus>Active</SubnetStatus>")
		b.WriteString("</Subnet>")
	}
	b.WriteString("</Subnets>")
	b.WriteString("</DBSubnetGroup>")
	return b.String()
}

func handleRDSCreateSubnetGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBSubnetGroupName")
	if name == "" {
		rdsErrorXML(w, "MissingParameter", "DBSubnetGroupName is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsSubnetGroups.Get(name); ok {
		rdsErrorXML(w, "DBSubnetGroupAlreadyExists", "DB subnet group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	vpcID := r.FormValue("VpcId")
	if vpcID == "" {
		vpcID = "vpc-" + strings.ReplaceAll(generateUUID(), "-", "")[:17]
	}
	g := RDSSubnetGroup{
		DBSubnetGroupName:        name,
		DBSubnetGroupDescription: r.FormValue("DBSubnetGroupDescription"),
		VpcId:                    vpcID,
		SubnetGroupStatus:        "Complete",
		SubnetIds:                parseRDSSubnetIDs(r),
		ARN:                      rdsSubnetGroupARN(name),
		Tags:                     parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsSubnetGroups.Put(name, g)
	rdsXMLResponse(w, "CreateDBSubnetGroup", renderRDSSubnetGroup(g), sim.RequestID(r.Context()))
}

func handleRDSDescribeSubnetGroups(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("DBSubnetGroupName")
	matched := false
	var b strings.Builder
	b.WriteString("<DBSubnetGroups>")
	for _, g := range rdsSubnetGroups.List() {
		if wanted != "" && g.DBSubnetGroupName != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSSubnetGroup(g))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "DBSubnetGroupNotFoundFault",
			fmt.Sprintf("DBSubnetGroup %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBSubnetGroups>")
	rdsXMLResponse(w, "DescribeDBSubnetGroups", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifySubnetGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBSubnetGroupName")
	if _, ok := rdsSubnetGroups.Get(name); !ok {
		rdsErrorXML(w, "DBSubnetGroupNotFoundFault", "DB subnet group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsSubnetGroups.Update(name, func(g *RDSSubnetGroup) {
		if v := r.FormValue("DBSubnetGroupDescription"); v != "" {
			g.DBSubnetGroupDescription = v
		}
		if ids := parseRDSSubnetIDs(r); len(ids) > 0 {
			g.SubnetIds = ids
		}
	})
	updated, _ := rdsSubnetGroups.Get(name)
	rdsXMLResponse(w, "ModifyDBSubnetGroup", renderRDSSubnetGroup(updated), sim.RequestID(r.Context()))
}

func handleRDSDeleteSubnetGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBSubnetGroupName")
	if _, ok := rdsSubnetGroups.Get(name); !ok {
		rdsErrorXML(w, "DBSubnetGroupNotFoundFault", "DB subnet group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsSubnetGroups.Delete(name)
	// DeleteDBSubnetGroup has an empty result body on real RDS.
	rdsXMLResponse(w, "DeleteDBSubnetGroup", "", sim.RequestID(r.Context()))
}

func rdsParamGroupARN(name string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:pg:%s", awsRegion(), awsAccountID(), name)
}

func findRDSParamGroupByARN(arn string) (RDSParamGroup, bool) {
	for _, g := range rdsParamGroups.List() {
		if g.ARN == arn {
			return g, true
		}
	}
	if g, ok := rdsParamGroups.Get(arn); ok {
		return g, true
	}
	return RDSParamGroup{}, false
}

func renderRDSParamGroup(g RDSParamGroup) string {
	var b strings.Builder
	b.WriteString("<DBParameterGroup>")
	fmt.Fprintf(&b, "<DBParameterGroupName>%s</DBParameterGroupName>", xmlEscape(g.DBParameterGroupName))
	fmt.Fprintf(&b, "<DBParameterGroupFamily>%s</DBParameterGroupFamily>", xmlEscape(g.DBParameterGroupFamily))
	fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(g.Description))
	fmt.Fprintf(&b, "<DBParameterGroupArn>%s</DBParameterGroupArn>", xmlEscape(g.ARN))
	b.WriteString("</DBParameterGroup>")
	return b.String()
}

func handleRDSCreateParamGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBParameterGroupName")
	family := r.FormValue("DBParameterGroupFamily")
	if name == "" || family == "" {
		rdsErrorXML(w, "MissingParameter", "DBParameterGroupName and DBParameterGroupFamily are required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsParamGroups.Get(name); ok {
		rdsErrorXML(w, "DBParameterGroupAlreadyExists", "DB parameter group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	g := RDSParamGroup{
		DBParameterGroupName:   name,
		DBParameterGroupFamily: family,
		Description:            r.FormValue("Description"),
		ARN:                    rdsParamGroupARN(name),
		Tags:                   parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsParamGroups.Put(name, g)
	rdsXMLResponse(w, "CreateDBParameterGroup", renderRDSParamGroup(g), sim.RequestID(r.Context()))
}

func handleRDSDescribeParamGroups(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("DBParameterGroupName")
	matched := false
	var b strings.Builder
	b.WriteString("<DBParameterGroups>")
	for _, g := range rdsParamGroups.List() {
		if wanted != "" && g.DBParameterGroupName != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSParamGroup(g))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "DBParameterGroupNotFound",
			fmt.Sprintf("DBParameterGroup %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBParameterGroups>")
	rdsXMLResponse(w, "DescribeDBParameterGroups", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeleteParamGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBParameterGroupName")
	if _, ok := rdsParamGroups.Get(name); !ok {
		rdsErrorXML(w, "DBParameterGroupNotFound", "DB parameter group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsParamGroups.Delete(name)
	// DeleteDBParameterGroup has an empty result body on real RDS.
	rdsXMLResponse(w, "DeleteDBParameterGroup", "", sim.RequestID(r.Context()))
}

func handleRDSCreateReadReplica(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	srcID := r.FormValue("SourceDBInstanceIdentifier")
	if id == "" || srcID == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBInstanceIdentifier and SourceDBInstanceIdentifier are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	// The source can be passed as an identifier or an ARN.
	src, ok := findRDSByARN(srcID)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound",
			fmt.Sprintf("DBInstance %q not found", srcID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if _, exists := rdsInstances.Get(id); exists {
		rdsErrorXML(w, "DBInstanceAlreadyExists",
			fmt.Sprintf("DBInstance %q already exists", id),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	class := r.FormValue("DBInstanceClass")
	if class == "" {
		class = src.DBInstanceClass
	}
	az := awsRegion() + "a"
	if v := r.FormValue("AvailabilityZone"); v != "" {
		az = v
	}
	replica := RDSInstance{
		DBInstanceIdentifier: id,
		DbiResourceId:        rdsResourceID(),
		DBInstanceClass:      class,
		Engine:               src.Engine,
		EngineVersion:        src.EngineVersion,
		DBInstanceStatus:     "available",
		MasterUsername:       src.MasterUsername,
		DBName:               src.DBName,
		AllocatedStorage:     src.AllocatedStorage,
		Endpoint:             fmt.Sprintf("%s.%s.rds.amazonaws.com", id, awsRegion()),
		Port:                 src.Port,
		AvailabilityZone:     az,
		InstanceCreateTime:   time.Now().UTC().Format(time.RFC3339),
		ARN:                  rdsInstanceARN(id),
		ReadReplicaSource:    src.DBInstanceIdentifier,
		Tags:                 parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsInstances.Put(id, replica)
	// Link the replica back onto the source.
	rdsInstances.Update(src.DBInstanceIdentifier, func(i *RDSInstance) {
		i.ReadReplicas = append(i.ReadReplicas, id)
	})
	rdsXMLResponse(w, "CreateDBInstanceReadReplica", renderRDSInstance(replica), sim.RequestID(r.Context()))
}

func handleRDSCopySnapshot(w http.ResponseWriter, r *http.Request) {
	srcID := r.FormValue("SourceDBSnapshotIdentifier")
	targetID := r.FormValue("TargetDBSnapshotIdentifier")
	if srcID == "" || targetID == "" {
		rdsErrorXML(w, "MissingParameter",
			"SourceDBSnapshotIdentifier and TargetDBSnapshotIdentifier are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	src, ok := rdsSnapshots.Get(srcID)
	if !ok {
		src, ok = findRDSSnapshotByARN(srcID)
		if !ok {
			rdsErrorXML(w, "DBSnapshotNotFound",
				fmt.Sprintf("DBSnapshot %q not found", srcID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	if _, exists := rdsSnapshots.Get(targetID); exists {
		rdsErrorXML(w, "DBSnapshotAlreadyExists",
			fmt.Sprintf("DBSnapshot %q already exists", targetID),
			http.StatusConflict, sim.RequestID(r.Context()))
		return
	}
	// Real RDS refuses to copy a snapshot that is not available; copying one
	// mid-capture would race the source's own data write.
	if src.Status != "available" {
		rdsErrorXML(w, "InvalidDBSnapshotState",
			fmt.Sprintf("DBSnapshot %q is %s; it must be available to copy", srcID, src.Status),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	tags := parseAWSQueryTagMap(r, "Tags.Tag")
	if r.FormValue("CopyTags") == "true" {
		tags = mergeTags(tags, src.Tags)
	}
	copySnap := RDSSnapshot{
		DBSnapshotIdentifier: targetID,
		DBInstanceIdentifier: src.DBInstanceIdentifier,
		DbiResourceId:        src.DbiResourceId,
		Engine:               src.Engine,
		EngineVersion:        src.EngineVersion,
		// The copy carries the DATA, not just the record: it answers
		// "creating" and settles once the source snapshot's volume is cloned
		// into its own, the same asynchronous machine CreateDBSnapshot runs.
		Status:             "creating",
		AllocatedStorage:   src.AllocatedStorage,
		MasterUsername:     src.MasterUsername,
		SnapshotCreateTime: time.Now().UTC().Format(time.RFC3339),
		SnapshotType:       "manual",
		Port:               src.Port,
		VpcId:              src.VpcId,
		// The master credential travels with the data, exactly as on create:
		// an instance restored from the COPY needs the credentials the data
		// was written under just as much as one restored from the original.
		MasterUserSecret:           append([]byte(nil), src.MasterUserSecret...),
		SourceDBSnapshotIdentifier: src.ARN,
		ARN:                        rdsSnapshotARN(targetID),
		Tags:                       tags,
	}
	rdsSnapshots.Put(targetID, copySnap)
	srcSnapID := src.DBSnapshotIdentifier
	simGo(func() { rdsCopySnapshotData(targetID, srcSnapID) })
	rdsXMLResponse(w, "CopyDBSnapshot", renderRDSSnapshot(copySnap), sim.RequestID(r.Context()))
}

func rdsClusterSnapshotARN(id string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:cluster-snapshot:%s", awsRegion(), awsAccountID(), id)
}

func findRDSClusterSnapshotByARN(arn string) (RDSClusterSnapshot, bool) {
	for _, s := range rdsClusterSnapshots.List() {
		if s.ARN == arn {
			return s, true
		}
	}
	if s, ok := rdsClusterSnapshots.Get(arn); ok {
		return s, true
	}
	return RDSClusterSnapshot{}, false
}

func renderRDSClusterSnapshot(s RDSClusterSnapshot) string {
	var b strings.Builder
	b.WriteString("<DBClusterSnapshot>")
	fmt.Fprintf(&b, "<DBClusterSnapshotIdentifier>%s</DBClusterSnapshotIdentifier>", xmlEscape(s.DBClusterSnapshotIdentifier))
	fmt.Fprintf(&b, "<DBClusterIdentifier>%s</DBClusterIdentifier>", xmlEscape(s.DBClusterIdentifier))
	fmt.Fprintf(&b, "<DbClusterResourceId>%s</DbClusterResourceId>", xmlEscape(s.DbClusterResourceId))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(s.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(s.EngineVersion))
	fmt.Fprintf(&b, "<EngineMode>%s</EngineMode>", xmlEscape(s.EngineMode))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(s.Status))
	fmt.Fprintf(&b, "<AllocatedStorage>%d</AllocatedStorage>", s.AllocatedStorage)
	fmt.Fprintf(&b, "<MasterUsername>%s</MasterUsername>", xmlEscape(s.MasterUsername))
	fmt.Fprintf(&b, "<Port>%d</Port>", s.Port)
	fmt.Fprintf(&b, "<VpcId>%s</VpcId>", xmlEscape(s.VpcId))
	fmt.Fprintf(&b, "<StorageEncrypted>%t</StorageEncrypted>", s.StorageEncrypted)
	fmt.Fprintf(&b, "<SnapshotCreateTime>%s</SnapshotCreateTime>", xmlEscape(s.SnapshotCreateTime))
	fmt.Fprintf(&b, "<ClusterCreateTime>%s</ClusterCreateTime>", xmlEscape(s.ClusterCreateTime))
	fmt.Fprintf(&b, "<SnapshotType>%s</SnapshotType>", xmlEscape(s.SnapshotType))
	fmt.Fprintf(&b, "<PercentProgress>%d</PercentProgress>", s.PercentProgress)
	fmt.Fprintf(&b, "<DBClusterSnapshotArn>%s</DBClusterSnapshotArn>", xmlEscape(s.ARN))
	b.WriteString("<AvailabilityZones>")
	for _, az := range s.AvailabilityZones {
		fmt.Fprintf(&b, "<AvailabilityZone>%s</AvailabilityZone>", xmlEscape(az))
	}
	b.WriteString("</AvailabilityZones>")
	b.WriteString("</DBClusterSnapshot>")
	return b.String()
}

func handleRDSCreateClusterSnapshot(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("DBClusterSnapshotIdentifier")
	clusterID := r.FormValue("DBClusterIdentifier")
	if snapID == "" || clusterID == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBClusterSnapshotIdentifier and DBClusterIdentifier are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	cl, ok := rdsClusters.Get(clusterID)
	if !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault",
			fmt.Sprintf("DBCluster %q not found", clusterID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if _, exists := rdsClusterSnapshots.Get(snapID); exists {
		rdsErrorXML(w, "DBClusterSnapshotAlreadyExistsFault",
			fmt.Sprintf("DBClusterSnapshot %q already exists", snapID),
			http.StatusConflict, sim.RequestID(r.Context()))
		return
	}
	snap := RDSClusterSnapshot{
		DBClusterSnapshotIdentifier: snapID,
		DBClusterIdentifier:         clusterID,
		DbClusterResourceId:         cl.DbClusterResourceId,
		Engine:                      cl.Engine,
		EngineVersion:               cl.EngineVersion,
		EngineMode:                  cl.EngineMode,
		Status:                      "available",
		AllocatedStorage:            cl.AllocatedStorage,
		MasterUsername:              cl.MasterUsername,
		Port:                        cl.Port,
		StorageEncrypted:            cl.StorageEncrypted,
		SnapshotCreateTime:          time.Now().UTC().Format(time.RFC3339),
		ClusterCreateTime:           cl.ClusterCreateTime,
		SnapshotType:                "manual",
		PercentProgress:             100,
		AvailabilityZones:           cl.AvailabilityZones,
		ARN:                         rdsClusterSnapshotARN(snapID),
		Tags:                        parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsClusterSnapshots.Put(snapID, snap)
	rdsXMLResponse(w, "CreateDBClusterSnapshot", renderRDSClusterSnapshot(snap), sim.RequestID(r.Context()))
}

func handleRDSDescribeClusterSnapshots(w http.ResponseWriter, r *http.Request) {
	filterID := r.FormValue("DBClusterSnapshotIdentifier")
	filterCluster := r.FormValue("DBClusterIdentifier")
	matched := false
	var b strings.Builder
	b.WriteString("<DBClusterSnapshots>")
	for _, s := range rdsClusterSnapshots.List() {
		if filterID != "" && s.DBClusterSnapshotIdentifier != filterID {
			continue
		}
		if filterCluster != "" && s.DBClusterIdentifier != filterCluster {
			continue
		}
		matched = true
		b.WriteString(renderRDSClusterSnapshot(s))
	}
	if filterID != "" && !matched {
		rdsErrorXML(w, "DBClusterSnapshotNotFoundFault",
			fmt.Sprintf("DBClusterSnapshot %q not found", filterID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBClusterSnapshots>")
	rdsXMLResponse(w, "DescribeDBClusterSnapshots", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeleteClusterSnapshot(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("DBClusterSnapshotIdentifier")
	snap, ok := rdsClusterSnapshots.Get(snapID)
	if !ok {
		snap, ok = findRDSClusterSnapshotByARN(snapID)
		if !ok {
			rdsErrorXML(w, "DBClusterSnapshotNotFoundFault",
				fmt.Sprintf("DBClusterSnapshot %q not found", snapID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	rdsClusterSnapshots.Delete(snap.DBClusterSnapshotIdentifier)
	snap.Status = "deleted"
	rdsXMLResponse(w, "DeleteDBClusterSnapshot", renderRDSClusterSnapshot(snap), sim.RequestID(r.Context()))
}

func rdsClusterParamGroupARN(name string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:cluster-pg:%s", awsRegion(), awsAccountID(), name)
}

func findRDSClusterParamGroupByARN(arn string) (RDSClusterParamGroup, bool) {
	for _, g := range rdsClusterParamGroups.List() {
		if g.ARN == arn {
			return g, true
		}
	}
	if g, ok := rdsClusterParamGroups.Get(arn); ok {
		return g, true
	}
	return RDSClusterParamGroup{}, false
}

func renderRDSClusterParamGroup(g RDSClusterParamGroup) string {
	var b strings.Builder
	b.WriteString("<DBClusterParameterGroup>")
	fmt.Fprintf(&b, "<DBClusterParameterGroupName>%s</DBClusterParameterGroupName>", xmlEscape(g.DBClusterParameterGroupName))
	fmt.Fprintf(&b, "<DBParameterGroupFamily>%s</DBParameterGroupFamily>", xmlEscape(g.DBParameterGroupFamily))
	fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(g.Description))
	fmt.Fprintf(&b, "<DBClusterParameterGroupArn>%s</DBClusterParameterGroupArn>", xmlEscape(g.ARN))
	b.WriteString("</DBClusterParameterGroup>")
	return b.String()
}

func handleRDSCreateClusterParamGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBClusterParameterGroupName")
	family := r.FormValue("DBParameterGroupFamily")
	if name == "" || family == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBClusterParameterGroupName and DBParameterGroupFamily are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsClusterParamGroups.Get(name); ok {
		rdsErrorXML(w, "DBParameterGroupAlreadyExists",
			"DB cluster parameter group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	g := RDSClusterParamGroup{
		DBClusterParameterGroupName: name,
		DBParameterGroupFamily:      family,
		Description:                 r.FormValue("Description"),
		ARN:                         rdsClusterParamGroupARN(name),
		Tags:                        parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsClusterParamGroups.Put(name, g)
	rdsXMLResponse(w, "CreateDBClusterParameterGroup", renderRDSClusterParamGroup(g), sim.RequestID(r.Context()))
}

func handleRDSDescribeClusterParamGroups(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("DBClusterParameterGroupName")
	matched := false
	var b strings.Builder
	b.WriteString("<DBClusterParameterGroups>")
	for _, g := range rdsClusterParamGroups.List() {
		if wanted != "" && g.DBClusterParameterGroupName != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSClusterParamGroup(g))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "DBParameterGroupNotFound",
			fmt.Sprintf("DBClusterParameterGroup %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBClusterParameterGroups>")
	rdsXMLResponse(w, "DescribeDBClusterParameterGroups", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeleteClusterParamGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBClusterParameterGroupName")
	if _, ok := rdsClusterParamGroups.Get(name); !ok {
		rdsErrorXML(w, "DBParameterGroupNotFound",
			"DB cluster parameter group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsClusterParamGroups.Delete(name)
	rdsXMLResponse(w, "DeleteDBClusterParameterGroup", "", sim.RequestID(r.Context()))
}

func rdsOptionGroupARN(name string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:og:%s", awsRegion(), awsAccountID(), name)
}

func findRDSOptionGroupByARN(arn string) (RDSOptionGroup, bool) {
	for _, g := range rdsOptionGroups.List() {
		if g.ARN == arn {
			return g, true
		}
	}
	if g, ok := rdsOptionGroups.Get(arn); ok {
		return g, true
	}
	return RDSOptionGroup{}, false
}

func renderRDSOptionGroup(g RDSOptionGroup) string {
	var b strings.Builder
	b.WriteString("<OptionGroup>")
	fmt.Fprintf(&b, "<OptionGroupName>%s</OptionGroupName>", xmlEscape(g.OptionGroupName))
	fmt.Fprintf(&b, "<OptionGroupDescription>%s</OptionGroupDescription>", xmlEscape(g.OptionGroupDescription))
	fmt.Fprintf(&b, "<EngineName>%s</EngineName>", xmlEscape(g.EngineName))
	fmt.Fprintf(&b, "<MajorEngineVersion>%s</MajorEngineVersion>", xmlEscape(g.MajorEngineVersion))
	fmt.Fprintf(&b, "<AllowsVpcAndNonVpcInstanceMemberships>%t</AllowsVpcAndNonVpcInstanceMemberships>", g.AllowsVpcAndNonVpcInstanceMemberships)
	fmt.Fprintf(&b, "<OptionGroupArn>%s</OptionGroupArn>", xmlEscape(g.ARN))
	b.WriteString("<Options></Options>")
	b.WriteString("</OptionGroup>")
	return b.String()
}

func handleRDSCreateOptionGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("OptionGroupName")
	engine := r.FormValue("EngineName")
	majorVersion := r.FormValue("MajorEngineVersion")
	desc := r.FormValue("OptionGroupDescription")
	if name == "" || engine == "" || majorVersion == "" || desc == "" {
		rdsErrorXML(w, "MissingParameter",
			"OptionGroupName, EngineName, MajorEngineVersion and OptionGroupDescription are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsOptionGroups.Get(name); ok {
		rdsErrorXML(w, "OptionGroupAlreadyExistsFault",
			"Option group already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	g := RDSOptionGroup{
		OptionGroupName:                       name,
		OptionGroupDescription:                desc,
		EngineName:                            engine,
		MajorEngineVersion:                    majorVersion,
		AllowsVpcAndNonVpcInstanceMemberships: true,
		ARN:                                   rdsOptionGroupARN(name),
		Tags:                                  parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsOptionGroups.Put(name, g)
	rdsXMLResponse(w, "CreateOptionGroup", renderRDSOptionGroup(g), sim.RequestID(r.Context()))
}

func handleRDSDescribeOptionGroups(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("OptionGroupName")
	matched := false
	var b strings.Builder
	b.WriteString("<OptionGroupsList>")
	for _, g := range rdsOptionGroups.List() {
		if wanted != "" && g.OptionGroupName != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSOptionGroup(g))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "OptionGroupNotFoundFault",
			fmt.Sprintf("OptionGroup %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</OptionGroupsList>")
	rdsXMLResponse(w, "DescribeOptionGroups", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeleteOptionGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("OptionGroupName")
	if _, ok := rdsOptionGroups.Get(name); !ok {
		rdsErrorXML(w, "OptionGroupNotFoundFault",
			"Option group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsOptionGroups.Delete(name)
	rdsXMLResponse(w, "DeleteOptionGroup", "", sim.RequestID(r.Context()))
}

// handleRDSDescribeEvents returns RDS service events. The sim does not
// generate engine-level events (no engine is run), so the list is
// empty unless an event source is being described — matching real RDS,
// which returns an empty Events list for a source with no recent
// activity. The response shape (Events list + Marker) is faithful.
func handleRDSDescribeEvents(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("<Events>")
	// Surface a single faithful creation event for a named instance
	// source so consumers can read a non-empty list; real RDS records
	// a "DB instance created" event at RunInstances time.
	srcID := r.FormValue("SourceIdentifier")
	srcType := r.FormValue("SourceType")
	if srcID != "" && (srcType == "" || srcType == "db-instance") {
		if inst, ok := rdsInstances.Get(srcID); ok {
			b.WriteString("<Event>")
			fmt.Fprintf(&b, "<SourceIdentifier>%s</SourceIdentifier>", xmlEscape(inst.DBInstanceIdentifier))
			b.WriteString("<SourceType>db-instance</SourceType>")
			b.WriteString("<Message>DB instance created</Message>")
			b.WriteString("<EventCategories><EventCategory>creation</EventCategory></EventCategories>")
			fmt.Fprintf(&b, "<Date>%s</Date>", xmlEscape(inst.InstanceCreateTime))
			fmt.Fprintf(&b, "<SourceArn>%s</SourceArn>", xmlEscape(inst.ARN))
			b.WriteString("</Event>")
		}
	}
	b.WriteString("</Events>")
	rdsXMLResponse(w, "DescribeEvents", b.String(), sim.RequestID(r.Context()))
}

// rdsEventCategories maps each RDS source type to its canonical set of
// event categories (as published by real RDS). Static-but-real: these
// values match the AWS-documented categories the SDK/CLI consume.
var rdsEventCategories = []struct {
	SourceType string
	Categories []string
}{
	{"db-instance", []string{"availability", "backup", "configuration change", "creation", "deletion", "failover", "failure", "maintenance", "notification", "read replica", "recovery", "restoration", "security"}},
	{"db-security-group", []string{"configuration change", "failure"}},
	{"db-parameter-group", []string{"configuration change"}},
	{"db-snapshot", []string{"creation", "deletion", "notification", "restoration"}},
	{"db-cluster", []string{"failover", "failure", "maintenance", "notification"}},
	{"db-cluster-snapshot", []string{"backup"}},
}

func handleRDSDescribeEventCategories(w http.ResponseWriter, r *http.Request) {
	wantType := r.FormValue("SourceType")
	var b strings.Builder
	b.WriteString("<EventCategoriesMapList>")
	for _, m := range rdsEventCategories {
		if wantType != "" && m.SourceType != wantType {
			continue
		}
		b.WriteString("<EventCategoriesMap>")
		fmt.Fprintf(&b, "<SourceType>%s</SourceType>", xmlEscape(m.SourceType))
		b.WriteString("<EventCategories>")
		for _, c := range m.Categories {
			fmt.Fprintf(&b, "<EventCategory>%s</EventCategory>", xmlEscape(c))
		}
		b.WriteString("</EventCategories>")
		b.WriteString("</EventCategoriesMap>")
	}
	b.WriteString("</EventCategoriesMapList>")
	rdsXMLResponse(w, "DescribeEventCategories", b.String(), sim.RequestID(r.Context()))
}

// rdsEngineVersionRow is a faithful static-but-real DB engine version
// the SDK/CLI can consume. The set mirrors the current GA majors the
// sim defaults to in rdsDefaultEngineVersion.
type rdsEngineVersionRow struct {
	Engine        string
	EngineVersion string
	MajorVersion  string
	Family        string
}

func rdsEngineVersionRows() []rdsEngineVersionRow {
	return []rdsEngineVersionRow{
		{"postgres", "17.5", "17", "postgres17"},
		{"postgres", "16.6", "16", "postgres16"},
		{"mysql", "8.0.40", "8.0", "mysql8.0"},
		{"mariadb", "11.4.4", "11.4", "mariadb11.4"},
		{"aurora-postgresql", "16.6", "16", "aurora-postgresql16"},
		{"aurora-mysql", "8.0.mysql_aurora.3.07.0", "8.0", "aurora-mysql8.0"},
	}
}

func handleRDSDescribeEngineVersions(w http.ResponseWriter, r *http.Request) {
	wantEngine := r.FormValue("Engine")
	wantVersion := r.FormValue("EngineVersion")
	var b strings.Builder
	b.WriteString("<DBEngineVersions>")
	for _, row := range rdsEngineVersionRows() {
		if wantEngine != "" && row.Engine != wantEngine {
			continue
		}
		if wantVersion != "" && row.EngineVersion != wantVersion {
			continue
		}
		b.WriteString("<DBEngineVersion>")
		fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(row.Engine))
		fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(row.EngineVersion))
		fmt.Fprintf(&b, "<MajorEngineVersion>%s</MajorEngineVersion>", xmlEscape(row.MajorVersion))
		fmt.Fprintf(&b, "<DBParameterGroupFamily>%s</DBParameterGroupFamily>", xmlEscape(row.Family))
		fmt.Fprintf(&b, "<DBEngineDescription>%s</DBEngineDescription>", xmlEscape("Amazon RDS "+row.Engine))
		fmt.Fprintf(&b, "<DBEngineVersionDescription>%s</DBEngineVersionDescription>", xmlEscape(row.Engine+" "+row.EngineVersion))
		b.WriteString("<Status>available</Status>")
		b.WriteString("<SupportsReadReplica>true</SupportsReadReplica>")
		b.WriteString("<SupportsLogExportsToCloudwatchLogs>true</SupportsLogExportsToCloudwatchLogs>")
		b.WriteString("</DBEngineVersion>")
	}
	b.WriteString("</DBEngineVersions>")
	rdsXMLResponse(w, "DescribeDBEngineVersions", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDescribeOrderableOptions(w http.ResponseWriter, r *http.Request) {
	engine := r.FormValue("Engine")
	if engine == "" {
		rdsErrorXML(w, "MissingParameter",
			"Engine is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	wantVersion := r.FormValue("EngineVersion")
	if wantVersion == "" {
		wantVersion = rdsDefaultEngineVersion(engine)
	}
	wantClass := r.FormValue("DBInstanceClass")
	classes := []string{"db.t3.micro", "db.t3.small", "db.t3.medium", "db.m5.large", "db.m5.xlarge", "db.r5.large"}
	azs := []string{awsRegion() + "a", awsRegion() + "b", awsRegion() + "c"}
	var b strings.Builder
	b.WriteString("<OrderableDBInstanceOptions>")
	for _, class := range classes {
		if wantClass != "" && class != wantClass {
			continue
		}
		b.WriteString("<OrderableDBInstanceOption>")
		fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(engine))
		fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(wantVersion))
		fmt.Fprintf(&b, "<DBInstanceClass>%s</DBInstanceClass>", xmlEscape(class))
		b.WriteString("<LicenseModel>general-public-license</LicenseModel>")
		b.WriteString("<MultiAZCapable>true</MultiAZCapable>")
		b.WriteString("<ReadReplicaCapable>true</ReadReplicaCapable>")
		b.WriteString("<Vpc>true</Vpc>")
		b.WriteString("<SupportsStorageEncryption>true</SupportsStorageEncryption>")
		b.WriteString("<SupportsIAMDatabaseAuthentication>true</SupportsIAMDatabaseAuthentication>")
		b.WriteString("<StorageType>gp3</StorageType>")
		b.WriteString("<AvailabilityZones>")
		for _, az := range azs {
			b.WriteString("<AvailabilityZone>")
			fmt.Fprintf(&b, "<Name>%s</Name>", xmlEscape(az))
			b.WriteString("</AvailabilityZone>")
		}
		b.WriteString("</AvailabilityZones>")
		b.WriteString("</OrderableDBInstanceOption>")
	}
	b.WriteString("</OrderableDBInstanceOptions>")
	rdsXMLResponse(w, "DescribeOrderableDBInstanceOptions", b.String(), sim.RequestID(r.Context()))
}

// Real RDS transitions an instance through starting→available and
// stopping→stopped. The simulator keeps the instance volume but tears down
// the engine and listener while stopped, then reinstalls them on start.

func handleRDSStartInstance(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	if _, ok := rdsInstances.Get(id); !ok {
		rdsErrorXML(w, "DBInstanceNotFound", "DB instance not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	instance, _ := rdsInstances.Get(id)
	if len(instance.MasterUserSecret) > 0 {
		_, password, decrypted := kmsDecryptBytes(instance.MasterUserSecret)
		if !decrypted {
			rdsErrorXML(w, "ProvisioningFailure", "RDS master-user credential could not be decrypted", http.StatusInternalServerError, sim.RequestID(r.Context()))
			return
		}
		if err := rdsInstallDataPlane(&instance, string(password)); err != nil {
			rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
			return
		}
	}
	instance.DBInstanceStatus = "available"
	rdsInstances.Put(id, instance)
	updated := instance
	rdsXMLResponse(w, "StartDBInstance", renderRDSInstance(updated), sim.RequestID(r.Context()))
}

func handleRDSStopInstance(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	if _, ok := rdsInstances.Get(id); !ok {
		rdsErrorXML(w, "DBInstanceNotFound", "DB instance not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsStopDataPlane(id, false)
	rdsInstances.Update(id, func(i *RDSInstance) { i.DBInstanceStatus = "stopped" })
	updated, _ := rdsInstances.Get(id)
	rdsXMLResponse(w, "StopDBInstance", renderRDSInstance(updated), sim.RequestID(r.Context()))
}

// handleRDSPromoteReadReplica detaches a read replica from its source,
// turning it into a standalone primary (clears ReadReplicaSource and
// unlinks it from the source's ReadReplicas list).
func handleRDSPromoteReadReplica(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	inst, ok := rdsInstances.Get(id)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound", "DB instance not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	src := inst.ReadReplicaSource
	rdsInstances.Update(id, func(i *RDSInstance) {
		i.ReadReplicaSource = ""
		i.DBInstanceStatus = "available"
	})
	if src != "" {
		if _, ok := rdsInstances.Get(src); ok {
			rdsInstances.Update(src, func(i *RDSInstance) {
				kept := i.ReadReplicas[:0]
				for _, rep := range i.ReadReplicas {
					if rep != id {
						kept = append(kept, rep)
					}
				}
				i.ReadReplicas = kept
			})
		}
	}
	updated, _ := rdsInstances.Get(id)
	rdsXMLResponse(w, "PromoteReadReplica", renderRDSInstance(updated), sim.RequestID(r.Context()))
}

func handleRDSStartCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	if _, ok := rdsClusters.Get(id); !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault", "DB cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsClusters.Update(id, func(c *RDSCluster) { c.Status = "available" })
	updated, _ := rdsClusters.Get(id)
	rdsXMLResponse(w, "StartDBCluster", renderRDSCluster(updated), sim.RequestID(r.Context()))
}

func handleRDSStopCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	if _, ok := rdsClusters.Get(id); !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault", "DB cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsClusters.Update(id, func(c *RDSCluster) { c.Status = "stopped" })
	updated, _ := rdsClusters.Get(id)
	rdsXMLResponse(w, "StopDBCluster", renderRDSCluster(updated), sim.RequestID(r.Context()))
}

// handleRDSFailoverCluster simulates an Aurora failover. With no engine
// the cluster stays "available"; the response carries the full
// DBCluster, which is what waiters and SDK consumers read.
func handleRDSFailoverCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	cl, ok := rdsClusters.Get(id)
	if !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault", "DB cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsXMLResponse(w, "FailoverDBCluster", renderRDSCluster(cl), sim.RequestID(r.Context()))
}

func parseRDSIndexedStringList(r *http.Request, prefix string) []string {
	var out []string
	for n := 1; n <= 200; n++ {
		v := r.FormValue(fmt.Sprintf("%s.%d", prefix, n))
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}

func renderRDSSnapshotAttributesResult(snap RDSSnapshot) string {
	var b strings.Builder
	b.WriteString("<DBSnapshotAttributesResult>")
	fmt.Fprintf(&b, "<DBSnapshotIdentifier>%s</DBSnapshotIdentifier>", xmlEscape(snap.DBSnapshotIdentifier))
	b.WriteString("<DBSnapshotAttributes>")
	b.WriteString("<DBSnapshotAttribute><AttributeName>restore</AttributeName><AttributeValues>")
	for _, v := range snap.RestoreAttributeValues {
		fmt.Fprintf(&b, "<AttributeValue>%s</AttributeValue>", xmlEscape(v))
	}
	b.WriteString("</AttributeValues></DBSnapshotAttribute>")
	b.WriteString("</DBSnapshotAttributes>")
	b.WriteString("</DBSnapshotAttributesResult>")
	return b.String()
}

// handleRDSModifySnapshotAttribute adds/removes values for the "restore"
// snapshot attribute (the only attribute RDS supports). The updated
// attribute set is persisted so DescribeDBSnapshotAttributes reads back
// the change.
func handleRDSModifySnapshotAttribute(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("DBSnapshotIdentifier")
	snap, ok := rdsSnapshots.Get(snapID)
	if !ok {
		snap, ok = findRDSSnapshotByARN(snapID)
		if !ok {
			rdsErrorXML(w, "DBSnapshotNotFound",
				fmt.Sprintf("DBSnapshot %q not found", snapID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	attrName := r.FormValue("AttributeName")
	if attrName != "restore" {
		rdsErrorXML(w, "InvalidParameterValue",
			"AttributeName must be 'restore'", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	toAdd := parseRDSIndexedStringList(r, "ValuesToAdd.AttributeValue")
	toRemove := parseRDSIndexedStringList(r, "ValuesToRemove.AttributeValue")
	rdsSnapshots.Update(snap.DBSnapshotIdentifier, func(s *RDSSnapshot) {
		s.RestoreAttributeValues = applyAttributeValueDelta(s.RestoreAttributeValues, toAdd, toRemove)
	})
	updated, _ := rdsSnapshots.Get(snap.DBSnapshotIdentifier)
	rdsXMLResponse(w, "ModifyDBSnapshotAttribute", renderRDSSnapshotAttributesResult(updated), sim.RequestID(r.Context()))
}

func applyAttributeValueDelta(cur, add, remove []string) []string {
	set := map[string]bool{}
	for _, v := range cur {
		set[v] = true
	}
	for _, v := range add {
		set[v] = true
	}
	for _, v := range remove {
		delete(set, v)
	}
	var out []string
	// Preserve insertion order: existing values first (still present),
	// then newly added ones.
	for _, v := range cur {
		if set[v] {
			out = append(out, v)
			delete(set, v)
		}
	}
	for _, v := range add {
		if set[v] {
			out = append(out, v)
			delete(set, v)
		}
	}
	return out
}

// rdsDefaultParameters returns a representative slice of engine
// parameters. RDS exposes hundreds; the sim returns a small faithful
// set (correct Parameter shape) so DescribeDBParameters/Cluster
// round-trips and the CLI/SDK parse a non-empty list.
func rdsDefaultParameters() []struct {
	Name, Value, ApplyType, DataType, Source, ApplyMethod string
	IsModifiable                                          bool
} {
	return []struct {
		Name, Value, ApplyType, DataType, Source, ApplyMethod string
		IsModifiable                                          bool
	}{
		{"max_connections", "LEAST({DBInstanceClassMemory/9531392},5000)", "dynamic", "integer", "engine-default", "pending-reboot", true},
		{"character_set_server", "utf8", "dynamic", "string", "engine-default", "immediate", true},
		{"autocommit", "1", "dynamic", "boolean", "engine-default", "immediate", true},
	}
}

func renderRDSParameter(name, value, applyType, dataType, source, applyMethod string, modifiable bool, override map[string]string) string {
	if v, ok := override[name]; ok {
		value = v
	}
	var b strings.Builder
	b.WriteString("<Parameter>")
	fmt.Fprintf(&b, "<ParameterName>%s</ParameterName>", xmlEscape(name))
	if value != "" {
		fmt.Fprintf(&b, "<ParameterValue>%s</ParameterValue>", xmlEscape(value))
	}
	fmt.Fprintf(&b, "<ApplyType>%s</ApplyType>", xmlEscape(applyType))
	fmt.Fprintf(&b, "<DataType>%s</DataType>", xmlEscape(dataType))
	fmt.Fprintf(&b, "<Source>%s</Source>", xmlEscape(source))
	fmt.Fprintf(&b, "<ApplyMethod>%s</ApplyMethod>", xmlEscape(applyMethod))
	fmt.Fprintf(&b, "<IsModifiable>%t</IsModifiable>", modifiable)
	b.WriteString("</Parameter>")
	return b.String()
}

func renderRDSParametersList(override map[string]string) string {
	var b strings.Builder
	b.WriteString("<Parameters>")
	for _, p := range rdsDefaultParameters() {
		src := p.Source
		if _, ok := override[p.Name]; ok {
			src = "user"
		}
		b.WriteString(renderRDSParameter(p.Name, p.Value, p.ApplyType, p.DataType, src, p.ApplyMethod, p.IsModifiable, override))
	}
	b.WriteString("</Parameters>")
	return b.String()
}

func handleRDSDescribeParameters(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBParameterGroupName")
	g, ok := rdsParamGroups.Get(name)
	if !ok {
		g, ok = findRDSParamGroupByARN(name)
		if !ok {
			rdsErrorXML(w, "DBParameterGroupNotFound",
				fmt.Sprintf("DBParameterGroup %q not found", name),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	rdsXMLResponse(w, "DescribeDBParameters", renderRDSParametersList(g.Parameters), sim.RequestID(r.Context()))
}

func handleRDSDescribeClusterParameters(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBClusterParameterGroupName")
	g, ok := rdsClusterParamGroups.Get(name)
	if !ok {
		g, ok = findRDSClusterParamGroupByARN(name)
		if !ok {
			rdsErrorXML(w, "DBParameterGroupNotFound",
				fmt.Sprintf("DBClusterParameterGroup %q not found", name),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	rdsXMLResponse(w, "DescribeDBClusterParameters", renderRDSParametersList(g.Parameters), sim.RequestID(r.Context()))
}

func parseRDSParameterOverrides(r *http.Request) map[string]string {
	out := map[string]string{}
	for n := 1; n <= 200; n++ {
		name := r.FormValue(fmt.Sprintf("Parameters.Parameter.%d.ParameterName", n))
		if name == "" {
			break
		}
		out[name] = r.FormValue(fmt.Sprintf("Parameters.Parameter.%d.ParameterValue", n))
	}
	return out
}

func handleRDSModifyParameterGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBParameterGroupName")
	if _, ok := rdsParamGroups.Get(name); !ok {
		rdsErrorXML(w, "DBParameterGroupNotFound", "DB parameter group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	override := parseRDSParameterOverrides(r)
	rdsParamGroups.Update(name, func(g *RDSParamGroup) {
		if g.Parameters == nil {
			g.Parameters = map[string]string{}
		}
		for k, v := range override {
			g.Parameters[k] = v
		}
	})
	body := fmt.Sprintf("<DBParameterGroupName>%s</DBParameterGroupName>", xmlEscape(name))
	rdsXMLResponse(w, "ModifyDBParameterGroup", body, sim.RequestID(r.Context()))
}

func handleRDSResetParameterGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBParameterGroupName")
	if _, ok := rdsParamGroups.Get(name); !ok {
		rdsErrorXML(w, "DBParameterGroupNotFound", "DB parameter group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	resetAll := r.FormValue("ResetAllParameters") == "true"
	toReset := parseRDSParameterOverrides(r)
	rdsParamGroups.Update(name, func(g *RDSParamGroup) {
		if resetAll {
			g.Parameters = map[string]string{}
			return
		}
		for k := range toReset {
			delete(g.Parameters, k)
		}
	})
	body := fmt.Sprintf("<DBParameterGroupName>%s</DBParameterGroupName>", xmlEscape(name))
	rdsXMLResponse(w, "ResetDBParameterGroup", body, sim.RequestID(r.Context()))
}

func handleRDSModifyClusterParameterGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBClusterParameterGroupName")
	if _, ok := rdsClusterParamGroups.Get(name); !ok {
		rdsErrorXML(w, "DBParameterGroupNotFound", "DB cluster parameter group not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	override := parseRDSParameterOverrides(r)
	rdsClusterParamGroups.Update(name, func(g *RDSClusterParamGroup) {
		if g.Parameters == nil {
			g.Parameters = map[string]string{}
		}
		for k, v := range override {
			g.Parameters[k] = v
		}
	})
	body := fmt.Sprintf("<DBClusterParameterGroupName>%s</DBClusterParameterGroupName>", xmlEscape(name))
	rdsXMLResponse(w, "ModifyDBClusterParameterGroup", body, sim.RequestID(r.Context()))
}

func handleRDSCopyClusterSnapshot(w http.ResponseWriter, r *http.Request) {
	srcID := r.FormValue("SourceDBClusterSnapshotIdentifier")
	targetID := r.FormValue("TargetDBClusterSnapshotIdentifier")
	if srcID == "" || targetID == "" {
		rdsErrorXML(w, "MissingParameter",
			"SourceDBClusterSnapshotIdentifier and TargetDBClusterSnapshotIdentifier are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	src, ok := rdsClusterSnapshots.Get(srcID)
	if !ok {
		src, ok = findRDSClusterSnapshotByARN(srcID)
		if !ok {
			rdsErrorXML(w, "DBClusterSnapshotNotFoundFault",
				fmt.Sprintf("DBClusterSnapshot %q not found", srcID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	if _, exists := rdsClusterSnapshots.Get(targetID); exists {
		rdsErrorXML(w, "DBClusterSnapshotAlreadyExistsFault",
			fmt.Sprintf("DBClusterSnapshot %q already exists", targetID),
			http.StatusConflict, sim.RequestID(r.Context()))
		return
	}
	tags := parseAWSQueryTagMap(r, "Tags.Tag")
	if r.FormValue("CopyTags") == "true" {
		tags = mergeTags(tags, src.Tags)
	}
	cp := src
	cp.DBClusterSnapshotIdentifier = targetID
	cp.Status = "available"
	cp.SnapshotType = "manual"
	cp.PercentProgress = 100
	cp.SnapshotCreateTime = time.Now().UTC().Format(time.RFC3339)
	cp.ARN = rdsClusterSnapshotARN(targetID)
	cp.Tags = tags
	rdsClusterSnapshots.Put(targetID, cp)
	rdsXMLResponse(w, "CopyDBClusterSnapshot", renderRDSClusterSnapshot(cp), sim.RequestID(r.Context()))
}

func rdsGlobalClusterARN(id string) string {
	return fmt.Sprintf("arn:aws:rds::%s:global-cluster:%s", awsAccountID(), id)
}

func rdsGlobalClusterResourceID() string {
	return "cluster-" + strings.ToUpper(strings.ReplaceAll(generateUUID(), "-", ""))[:26]
}

// renderRDSGlobalCluster renders a global cluster wrapped in the given
// element name. CreateGlobalCluster et al. return a single
// <GlobalCluster> member, while the DescribeGlobalClusters list element
// is named <GlobalClusterMember> (per the GlobalClusterList xmlName in
// the RDS model).
func renderRDSGlobalCluster(g RDSGlobalCluster, elem string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<%s>", elem)
	fmt.Fprintf(&b, "<GlobalClusterIdentifier>%s</GlobalClusterIdentifier>", xmlEscape(g.GlobalClusterIdentifier))
	fmt.Fprintf(&b, "<GlobalClusterResourceId>%s</GlobalClusterResourceId>", xmlEscape(g.GlobalClusterResourceId))
	fmt.Fprintf(&b, "<GlobalClusterArn>%s</GlobalClusterArn>", xmlEscape(g.ARN))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(g.Status))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(g.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(g.EngineVersion))
	fmt.Fprintf(&b, "<DatabaseName>%s</DatabaseName>", xmlEscape(g.DatabaseName))
	fmt.Fprintf(&b, "<DeletionProtection>%t</DeletionProtection>", g.DeletionProtection)
	fmt.Fprintf(&b, "<StorageEncrypted>%t</StorageEncrypted>", g.StorageEncrypted)
	b.WriteString("<GlobalClusterMembers>")
	for i, arn := range g.Members {
		b.WriteString("<GlobalClusterMember>")
		fmt.Fprintf(&b, "<DBClusterArn>%s</DBClusterArn>", xmlEscape(arn))
		fmt.Fprintf(&b, "<IsWriter>%t</IsWriter>", i == 0)
		b.WriteString("<Readers></Readers>")
		b.WriteString("</GlobalClusterMember>")
	}
	b.WriteString("</GlobalClusterMembers>")
	fmt.Fprintf(&b, "</%s>", elem)
	return b.String()
}

func handleRDSCreateGlobalCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("GlobalClusterIdentifier")
	if id == "" {
		rdsErrorXML(w, "MissingParameter", "GlobalClusterIdentifier is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsGlobalClusters.Get(id); ok {
		rdsErrorXML(w, "GlobalClusterAlreadyExistsFault", "Global cluster already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	engine := r.FormValue("Engine")
	engineVersion := r.FormValue("EngineVersion")
	var members []string
	// CreateGlobalCluster can adopt an existing regional cluster as the
	// primary via SourceDBClusterIdentifier.
	if src := r.FormValue("SourceDBClusterIdentifier"); src != "" {
		if cl, ok := findRDSClusterByARN(src); ok {
			members = append(members, cl.ARN)
			if engine == "" {
				engine = cl.Engine
			}
			if engineVersion == "" {
				engineVersion = cl.EngineVersion
			}
		}
	}
	if engine == "" {
		engine = "aurora-mysql"
	}
	g := RDSGlobalCluster{
		GlobalClusterIdentifier: id,
		GlobalClusterResourceId: rdsGlobalClusterResourceID(),
		Engine:                  engine,
		EngineVersion:           engineVersion,
		Status:                  "available",
		DatabaseName:            r.FormValue("DatabaseName"),
		DeletionProtection:      r.FormValue("DeletionProtection") == "true",
		StorageEncrypted:        r.FormValue("StorageEncrypted") == "true",
		Members:                 members,
		ARN:                     rdsGlobalClusterARN(id),
		Tags:                    parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsGlobalClusters.Put(id, g)
	rdsXMLResponse(w, "CreateGlobalCluster", renderRDSGlobalCluster(g, "GlobalCluster"), sim.RequestID(r.Context()))
}

func handleRDSDescribeGlobalClusters(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("GlobalClusterIdentifier")
	matched := false
	var b strings.Builder
	b.WriteString("<GlobalClusters>")
	for _, g := range rdsGlobalClusters.List() {
		if wanted != "" && g.GlobalClusterIdentifier != wanted && g.ARN != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSGlobalCluster(g, "GlobalClusterMember"))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "GlobalClusterNotFoundFault",
			fmt.Sprintf("GlobalCluster %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</GlobalClusters>")
	rdsXMLResponse(w, "DescribeGlobalClusters", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyGlobalCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("GlobalClusterIdentifier")
	if _, ok := rdsGlobalClusters.Get(id); !ok {
		rdsErrorXML(w, "GlobalClusterNotFoundFault", "Global cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	newID := r.FormValue("NewGlobalClusterIdentifier")
	rdsGlobalClusters.Update(id, func(g *RDSGlobalCluster) {
		if v := r.FormValue("EngineVersion"); v != "" {
			g.EngineVersion = v
		}
		if v := r.FormValue("DeletionProtection"); v != "" {
			g.DeletionProtection = v == "true"
		}
		if newID != "" {
			g.GlobalClusterIdentifier = newID
			g.ARN = rdsGlobalClusterARN(newID)
		}
	})
	updated, _ := rdsGlobalClusters.Get(id)
	if newID != "" && newID != id {
		rdsGlobalClusters.Delete(id)
		rdsGlobalClusters.Put(newID, updated)
	}
	rdsXMLResponse(w, "ModifyGlobalCluster", renderRDSGlobalCluster(updated, "GlobalCluster"), sim.RequestID(r.Context()))
}

func handleRDSDeleteGlobalCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("GlobalClusterIdentifier")
	g, ok := rdsGlobalClusters.Get(id)
	if !ok {
		rdsErrorXML(w, "GlobalClusterNotFoundFault", "Global cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsGlobalClusters.Delete(id)
	g.Status = "deleting"
	rdsXMLResponse(w, "DeleteGlobalCluster", renderRDSGlobalCluster(g, "GlobalCluster"), sim.RequestID(r.Context()))
}

func rdsEventSubscriptionARN(name string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:es:%s", awsRegion(), awsAccountID(), name)
}

func findRDSEventSubscriptionByARN(arn string) (RDSEventSubscription, bool) {
	for _, s := range rdsEventSubscriptions.List() {
		if s.ARN == arn {
			return s, true
		}
	}
	if s, ok := rdsEventSubscriptions.Get(arn); ok {
		return s, true
	}
	return RDSEventSubscription{}, false
}

func renderRDSEventSubscription(s RDSEventSubscription) string {
	var b strings.Builder
	b.WriteString("<EventSubscription>")
	fmt.Fprintf(&b, "<CustomerAwsId>%s</CustomerAwsId>", xmlEscape(s.CustomerAwsId))
	fmt.Fprintf(&b, "<CustSubscriptionId>%s</CustSubscriptionId>", xmlEscape(s.SubscriptionName))
	fmt.Fprintf(&b, "<SnsTopicArn>%s</SnsTopicArn>", xmlEscape(s.SnsTopicArn))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(s.Status))
	fmt.Fprintf(&b, "<SubscriptionCreationTime>%s</SubscriptionCreationTime>", xmlEscape(s.SubscriptionCreateTime))
	fmt.Fprintf(&b, "<SourceType>%s</SourceType>", xmlEscape(s.SourceType))
	fmt.Fprintf(&b, "<Enabled>%t</Enabled>", s.Enabled)
	fmt.Fprintf(&b, "<EventSubscriptionArn>%s</EventSubscriptionArn>", xmlEscape(s.ARN))
	b.WriteString("<SourceIdsList>")
	for _, id := range s.SourceIds {
		fmt.Fprintf(&b, "<SourceId>%s</SourceId>", xmlEscape(id))
	}
	b.WriteString("</SourceIdsList>")
	b.WriteString("<EventCategoriesList>")
	for _, c := range s.EventCategories {
		fmt.Fprintf(&b, "<EventCategory>%s</EventCategory>", xmlEscape(c))
	}
	b.WriteString("</EventCategoriesList>")
	b.WriteString("</EventSubscription>")
	return b.String()
}

func handleRDSCreateEventSubscription(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("SubscriptionName")
	topic := r.FormValue("SnsTopicArn")
	if name == "" || topic == "" {
		rdsErrorXML(w, "MissingParameter",
			"SubscriptionName and SnsTopicArn are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsEventSubscriptions.Get(name); ok {
		rdsErrorXML(w, "SubscriptionAlreadyExistFault", "Event subscription already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	enabled := true
	if v := r.FormValue("Enabled"); v != "" {
		enabled = v == "true"
	}
	s := RDSEventSubscription{
		SubscriptionName:       name,
		CustomerAwsId:          awsAccountID(),
		SnsTopicArn:            topic,
		Status:                 "active",
		SubscriptionCreateTime: time.Now().UTC().Format(time.RFC3339),
		SourceType:             r.FormValue("SourceType"),
		Enabled:                enabled,
		SourceIds:              parseRDSIndexedStringList(r, "SourceIds.SourceId"),
		EventCategories:        parseRDSIndexedStringList(r, "EventCategories.EventCategory"),
		ARN:                    rdsEventSubscriptionARN(name),
		Tags:                   parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsEventSubscriptions.Put(name, s)
	rdsXMLResponse(w, "CreateEventSubscription", renderRDSEventSubscription(s), sim.RequestID(r.Context()))
}

func handleRDSDescribeEventSubscriptions(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("SubscriptionName")
	matched := false
	var b strings.Builder
	b.WriteString("<EventSubscriptionsList>")
	for _, s := range rdsEventSubscriptions.List() {
		if wanted != "" && s.SubscriptionName != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSEventSubscription(s))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "SubscriptionNotFoundFault",
			fmt.Sprintf("EventSubscription %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</EventSubscriptionsList>")
	rdsXMLResponse(w, "DescribeEventSubscriptions", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyEventSubscription(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("SubscriptionName")
	if _, ok := rdsEventSubscriptions.Get(name); !ok {
		rdsErrorXML(w, "SubscriptionNotFoundFault", "Event subscription not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsEventSubscriptions.Update(name, func(s *RDSEventSubscription) {
		if v := r.FormValue("SnsTopicArn"); v != "" {
			s.SnsTopicArn = v
		}
		if v := r.FormValue("SourceType"); v != "" {
			s.SourceType = v
		}
		if v := r.FormValue("Enabled"); v != "" {
			s.Enabled = v == "true"
		}
		if cats := parseRDSIndexedStringList(r, "EventCategories.EventCategory"); len(cats) > 0 {
			s.EventCategories = cats
		}
	})
	updated, _ := rdsEventSubscriptions.Get(name)
	rdsXMLResponse(w, "ModifyEventSubscription", renderRDSEventSubscription(updated), sim.RequestID(r.Context()))
}

func handleRDSDeleteEventSubscription(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("SubscriptionName")
	s, ok := rdsEventSubscriptions.Get(name)
	if !ok {
		s, ok = findRDSEventSubscriptionByARN(name)
		if !ok {
			rdsErrorXML(w, "SubscriptionNotFoundFault", "Event subscription not found", http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	rdsEventSubscriptions.Delete(s.SubscriptionName)
	s.Status = "deleting"
	rdsXMLResponse(w, "DeleteEventSubscription", renderRDSEventSubscription(s), sim.RequestID(r.Context()))
}

func rdsClusterEndpointARN(id string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:cluster-endpoint:%s", awsRegion(), awsAccountID(), id)
}

func findRDSClusterEndpointByARN(arn string) (RDSClusterEndpoint, bool) {
	for _, e := range rdsClusterEndpoints.List() {
		if e.ARN == arn {
			return e, true
		}
	}
	if e, ok := rdsClusterEndpoints.Get(arn); ok {
		return e, true
	}
	return RDSClusterEndpoint{}, false
}

func renderRDSClusterEndpoint(e RDSClusterEndpoint) string {
	var b strings.Builder
	b.WriteString("<DBClusterEndpointIdentifier>")
	b.WriteString(xmlEscape(e.DBClusterEndpointIdentifier))
	b.WriteString("</DBClusterEndpointIdentifier>")
	fmt.Fprintf(&b, "<DBClusterIdentifier>%s</DBClusterIdentifier>", xmlEscape(e.DBClusterIdentifier))
	fmt.Fprintf(&b, "<DBClusterEndpointResourceIdentifier>%s</DBClusterEndpointResourceIdentifier>", xmlEscape(e.DBClusterEndpointResourceIdentifier))
	fmt.Fprintf(&b, "<Endpoint>%s</Endpoint>", xmlEscape(e.Endpoint))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(e.Status))
	fmt.Fprintf(&b, "<EndpointType>%s</EndpointType>", xmlEscape(e.EndpointType))
	fmt.Fprintf(&b, "<CustomEndpointType>%s</CustomEndpointType>", xmlEscape(e.CustomEndpointType))
	fmt.Fprintf(&b, "<DBClusterEndpointArn>%s</DBClusterEndpointArn>", xmlEscape(e.ARN))
	b.WriteString("<StaticMembers>")
	for _, m := range e.StaticMembers {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(m))
	}
	b.WriteString("</StaticMembers>")
	b.WriteString("<ExcludedMembers>")
	for _, m := range e.ExcludedMembers {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(m))
	}
	b.WriteString("</ExcludedMembers>")
	return b.String()
}

func handleRDSCreateClusterEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterEndpointIdentifier")
	clusterID := r.FormValue("DBClusterIdentifier")
	endpointType := r.FormValue("EndpointType")
	if id == "" || clusterID == "" || endpointType == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBClusterEndpointIdentifier, DBClusterIdentifier and EndpointType are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsClusters.Get(clusterID); !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault",
			fmt.Sprintf("DBCluster %q not found", clusterID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsClusterEndpoints.Get(id); ok {
		rdsErrorXML(w, "DBClusterEndpointAlreadyExistsFault",
			fmt.Sprintf("DBClusterEndpoint %q already exists", id),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	e := RDSClusterEndpoint{
		DBClusterEndpointIdentifier:         id,
		DBClusterIdentifier:                 clusterID,
		DBClusterEndpointResourceIdentifier: rdsGlobalClusterResourceID(),
		Endpoint:                            fmt.Sprintf("%s.cluster-custom-sim.%s.rds.amazonaws.com", id, awsRegion()),
		Status:                              "available",
		EndpointType:                        "CUSTOM",
		CustomEndpointType:                  endpointType,
		StaticMembers:                       parseRDSIndexedStringList(r, "StaticMembers.member"),
		ExcludedMembers:                     parseRDSIndexedStringList(r, "ExcludedMembers.member"),
		ARN:                                 rdsClusterEndpointARN(id),
		Tags:                                parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsClusterEndpoints.Put(id, e)
	rdsXMLResponse(w, "CreateDBClusterEndpoint", renderRDSClusterEndpoint(e), sim.RequestID(r.Context()))
}

func handleRDSDescribeClusterEndpoints(w http.ResponseWriter, r *http.Request) {
	wantedEndpoint := r.FormValue("DBClusterEndpointIdentifier")
	wantedCluster := r.FormValue("DBClusterIdentifier")
	matched := false
	var b strings.Builder
	b.WriteString("<DBClusterEndpoints>")
	for _, e := range rdsClusterEndpoints.List() {
		if wantedEndpoint != "" && e.DBClusterEndpointIdentifier != wantedEndpoint {
			continue
		}
		if wantedCluster != "" && e.DBClusterIdentifier != wantedCluster {
			continue
		}
		matched = true
		b.WriteString("<DBClusterEndpointList>")
		b.WriteString(renderRDSClusterEndpoint(e))
		b.WriteString("</DBClusterEndpointList>")
	}
	if wantedEndpoint != "" && !matched {
		rdsErrorXML(w, "DBClusterEndpointNotFoundFault",
			fmt.Sprintf("DBClusterEndpoint %q not found", wantedEndpoint),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBClusterEndpoints>")
	rdsXMLResponse(w, "DescribeDBClusterEndpoints", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeleteClusterEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterEndpointIdentifier")
	e, ok := rdsClusterEndpoints.Get(id)
	if !ok {
		e, ok = findRDSClusterEndpointByARN(id)
		if !ok {
			rdsErrorXML(w, "DBClusterEndpointNotFoundFault",
				fmt.Sprintf("DBClusterEndpoint %q not found", id),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	rdsClusterEndpoints.Delete(e.DBClusterEndpointIdentifier)
	e.Status = "deleting"
	rdsXMLResponse(w, "DeleteDBClusterEndpoint", renderRDSClusterEndpoint(e), sim.RequestID(r.Context()))
}
