package main

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// PostgreSQL and MySQL-compatible instances expose their real database wire
// protocols through managed engine containers. The control plane reports an
// instance available once its endpoint listener is installed, and starts the
// engine on the first data-plane connection.

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
	Region               string
	// ReadReplicaSource is the source DB instance identifier when this
	// instance is a read replica (created by CreateDBInstanceReadReplica);
	// empty for primary instances.
	ReadReplicaSource string
	// ReadReplicas lists the identifiers of read replicas created from
	// this instance (populated on the source when a replica is created).
	ReadReplicas []string
	// AutomatedBackupsReplications are the ARNs of the replicated automated
	// backups StartDBInstanceAutomatedBackupsReplication made of this
	// instance's automated backups.
	AutomatedBackupsReplications []string `json:",omitempty"`
	Tags                         map[string]string
	// Sealed under the simulator's AWS owned RDS KMS key; never rendered on the API.
	MasterUserSecret []byte
	// BackendMasterUserSecret records the password currently installed in the
	// native database engine. It can temporarily differ from MasterUserSecret
	// when a stopped instance receives a password change; the engine applies
	// that pending change when it next starts.
	BackendMasterUserSecret         []byte
	EnableIAMDatabaseAuthentication bool
	DeletionProtection              bool
	// DBClusterIdentifier names the DB cluster the instance is a member of.
	DBClusterIdentifier   string
	BackupRetentionPeriod int
	PreferredBackupWindow string
	// BaseBackups are the captures, oldest first, a restore to a time starts
	// from.
	BaseBackups []RDSBaseBackup `json:",omitempty"`
	// While a restore seeds the creating instance's volume: the volume it
	// seeds from and, for a restore to a time, the source volume whose log it
	// replays, the time, and the binary log position the replay starts at.
	RestoreSourceVolume string `json:",omitempty"`
	RestoreLogVolume    string `json:",omitempty"`
	RestoreToTime       string `json:",omitempty"`
	RestoreBinlogFile   string `json:",omitempty"`
	RestoreBinlogOffset int    `json:",omitempty"`
	// The Amazon S3 backup a RestoreDBInstanceFromS3 imports while the
	// instance is creating.
	ImportS3Bucket string `json:",omitempty"`
	ImportS3Prefix string `json:",omitempty"`
	ImportS3Role   string `json:",omitempty"`
	// RetainAutomatedBackups records a deletion's DeleteAutomatedBackups=false.
	RetainAutomatedBackups bool `json:",omitempty"`
	// BackupTarget is where the instance's automated backups and manual
	// snapshots are stored: "region" or "outposts".
	BackupTarget string `json:",omitempty"`
	// AutoMinorVersionUpgrade is the request's choice; nil is Amazon RDS's
	// default of true.
	AutoMinorVersionUpgrade *bool `json:",omitempty"`
	// DBParameterGroupName names the DB parameter group the instance is
	// associated with: the one a request named, or its family's default.
	DBParameterGroupName string `json:",omitempty"`
	// PendingEngineVersion is the version an upgrade in progress moves the
	// instance's engine to.
	PendingEngineVersion string `json:",omitempty"`
	// PreferredMaintenanceWindow is the weekly window, ddd:hh24:mi-ddd:hh24:mi
	// in UTC, that applies PendingModifications.
	PreferredMaintenanceWindow string `json:",omitempty"`
	// PendingModifications are the changes ModifyDBInstance deferred to the
	// next maintenance window.
	PendingModifications *RDSPendingModifications `json:",omitempty"`
	// EngineParameters are the DB parameter group parameters the engine runs
	// with; nil until the engine first starts.
	EngineParameters *RDSEngineParameters `json:",omitempty"`
	// ReplicationUserSecret seals the password of the replication user the
	// root of a replication chain creates for its read replicas.
	ReplicationUserSecret []byte `json:",omitempty"`
	// A MySQL-family replica's position in its source's binary log, which its
	// engine starts replicating from on its first start.
	ReplicaSourceLogFile string `json:",omitempty"`
	ReplicaSourceLogPos  int    `json:",omitempty"`
	// ReplicationStatus and ReplicationMessage are a read replica's read
	// replication status as its engine last reported it.
	ReplicationStatus  string `json:",omitempty"`
	ReplicationMessage string `json:",omitempty"`
	// BlueGreenDeploymentIdentifier names the blue/green deployment whose
	// green environment the instance is, which serves its sessions read-only.
	BlueGreenDeploymentIdentifier string `json:",omitempty"`
	MultiAZ                       bool   `json:",omitempty"`
	// SecondaryAvailabilityZone is where a Multi-AZ instance's standby runs.
	SecondaryAvailabilityZone string `json:",omitempty"`
	// AvailabilityConversion records a conversion between Single-AZ and
	// Multi-AZ whose standby is not yet built or removed.
	AvailabilityConversion  bool   `json:",omitempty"`
	StorageType             string `json:",omitempty"`
	Iops                    int    `json:",omitempty"`
	StorageThroughput       int    `json:",omitempty"`
	DBSubnetGroupName       string `json:",omitempty"`
	LicenseModel            string `json:",omitempty"`
	OptionGroupName         string `json:",omitempty"`
	CACertificateIdentifier string `json:",omitempty"`
	// ServerCertificateValidTill is when the server certificate the endpoint
	// last presented expires.
	ServerCertificateValidTill string `json:",omitempty"`
	// Restart is what the engine restart of a modifying instance applies.
	Restart *RDSRestartModifications `json:",omitempty"`
	// DBCapacityReservationIdentifier names the DB capacity reservation the
	// instance belongs to, which a stopped instance keeps unless it detached.
	DBCapacityReservationIdentifier string `json:",omitempty"`
}

// rdsRequestedBackupTarget is the BackupTarget a create or restore asks for,
// which Amazon RDS defaults to the AWS Region.
func rdsRequestedBackupTarget(r *http.Request) string {
	if target := r.FormValue("BackupTarget"); target != "" {
		return target
	}
	return "region"
}

// RDSSnapshot is creating while its volume is captured, then available, or
// failed with StatusReason; DeleteDBSnapshot removes it.
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
	// DBName is the database the instance was created with, which a restore
	// from the snapshot keeps.
	DBName string
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
	// The source instance's sealed master credential: Amazon RDS restores the master
	// credentials with the data, so a restored engine starts under the credentials
	// the data expects.
	MasterUserSecret []byte
}

// RDSCluster models an Aurora or Multi-AZ DB cluster. An Aurora cluster runs
// one engine over its cluster volume behind its writer and reader endpoints
// and its members' instance endpoints.
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
	// Sealed under the simulator's AWS owned RDS KMS key; never rendered on the API.
	MasterUserSecret                []byte
	EnableIAMDatabaseAuthentication bool
	// BackendMasterUserSecret is the password installed in the cluster's
	// engine, which lags MasterUserSecret while a ModifyDBCluster waits for
	// the engine to run.
	BackendMasterUserSecret []byte
	// RestoreSourceVolume is the volume a creating restored cluster seeds its
	// cluster volume from.
	RestoreSourceVolume string
	// RestoreLogVolume and RestoreToTime name the source cluster volume whose
	// log a creating point-in-time restore replays, and the time it replays
	// to; an Aurora MySQL replay starts at RestoreBinlogFile and
	// RestoreBinlogOffset.
	RestoreLogVolume    string
	RestoreToTime       string
	RestoreBinlogFile   string `json:",omitempty"`
	RestoreBinlogOffset int    `json:",omitempty"`
	// BaseBackups are the captures, oldest first, a restore to a time starts
	// from.
	BaseBackups []RDSBaseBackup `json:",omitempty"`
	// ImportS3Bucket, ImportS3Prefix and ImportS3Role name the Percona
	// XtraBackup a creating RestoreDBClusterFromS3 cluster imports.
	ImportS3Bucket string `json:",omitempty"`
	ImportS3Prefix string `json:",omitempty"`
	ImportS3Role   string `json:",omitempty"`
	// RetainAutomatedBackups records a deletion's DeleteAutomatedBackups=false.
	RetainAutomatedBackups bool `json:",omitempty"`
}

// RDSSubnetGroup is a named set of VPC subnets Amazon RDS places DB instances into.
type RDSSubnetGroup struct {
	DBSubnetGroupName        string
	DBSubnetGroupDescription string
	VpcId                    string
	SubnetGroupStatus        string
	SubnetIds                []string
	// SubnetZones are the Availability Zones of SubnetIds, in the same order.
	SubnetZones []string
	ARN         string
	Tags        map[string]string
}

// Zones are the distinct Availability Zones the group's subnets cover.
func (g RDSSubnetGroup) Zones() []string {
	var zones []string
	for _, zone := range g.SubnetZones {
		if !slices.Contains(zones, zone) {
			zones = append(zones, zone)
		}
	}
	return zones
}

// RDSParamGroup models a DB parameter group. Parameters holds the values
// ModifyDBParameterGroup set, with the apply method each was set with, over
// the family's engine defaults.
type RDSParamGroup struct {
	DBParameterGroupName   string
	DBParameterGroupFamily string
	Description            string
	Parameters             map[string]string
	ApplyMethods           map[string]string `json:",omitempty"`
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
	ApplyMethods                map[string]string `json:",omitempty"`
	ARN                         string
	Tags                        map[string]string
}

// RDSClusterSnapshot models a DB cluster snapshot: creating (or copying)
// while its cluster volume is captured, then available, or failed with the
// capture's error in StatusReason.
type RDSClusterSnapshot struct {
	DBClusterSnapshotIdentifier string
	DBClusterIdentifier         string
	DbClusterResourceId         string
	Engine                      string
	EngineVersion               string
	EngineMode                  string
	Status                      string
	StatusReason                string
	SourceDBClusterSnapshotArn  string
	DatabaseName                string
	IAMDatabaseAuthentication   bool
	// MasterUserSecret and BackendMasterUserSecret carry the cluster's
	// master credential and the one its engine holds, so a restored engine
	// opens the captured data under the credentials it was written with.
	MasterUserSecret        []byte
	BackendMasterUserSecret []byte
	AllocatedStorage        int
	MasterUsername          string
	Port                    int
	VpcId                   string
	StorageEncrypted        bool
	SnapshotCreateTime      string
	ClusterCreateTime       string
	SnapshotType            string // manual | automated
	PercentProgress         int
	AvailabilityZones       []string
	ARN                     string
	Tags                    map[string]string
}

// Options are not simulated; the group is a control-plane row.
type RDSOptionGroup struct {
	OptionGroupName                       string
	OptionGroupDescription                string
	EngineName                            string
	MajorEngineVersion                    string
	AllowsVpcAndNonVpcInstanceMemberships bool
	ARN                                   string
	Tags                                  map[string]string
}

// RDSGlobalCluster is an Aurora global database linking regional DB clusters.
// The simulator runs no global engine; Status settles to "available" on create.
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

// The RDS Query API version, which tells its Action names apart in AWSQueryRouter.
const rdsAPIVersion = "2014-10-31"

func registerRDS(r *AWSQueryRouter, srv *sim.Server) {
	rdsInstances = rdsObserve(sim.MakeStore[RDSInstance](srv.DB(), "rds_instances"), rdsObserveInstance)
	rdsSnapshots = rdsObserve(sim.MakeStore[RDSSnapshot](srv.DB(), "rds_snapshots"), rdsObserveSnapshot)
	rdsEvents = sim.MakeStore[RDSEvent](srv.DB(), "rds_events")
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
	registerRDSCapacityReservations(r, srv)
	rdsMigrateEngineVersions()
	if err := rdsMoveVolumesToKindNames(); err != nil {
		panic(fmt.Sprintf("move Amazon Relational Database Service volumes: %v", err))
	}
	if err := rdsRecoverDataPlanes(); err != nil {
		panic(fmt.Sprintf("restore Amazon Relational Database Service data planes: %v", err))
	}
	rdsRecoverClusterTransitions()
	rdsRecoverInstanceSnapshots()
	rdsRecoverRetainedBackups()
	rdsRecoverBlueGreenDeployments()
	rdsRecoverMaintenance()
	rdsRecoverInstanceSettings()
}

// rdsRequestedBool is a boolean member the request sets, or nil.
func rdsRequestedBool(r *http.Request, name string) *bool {
	value := r.FormValue(name)
	if value == "" {
		return nil
	}
	set := strings.EqualFold(value, "true")
	return &set
}

func rdsInstanceARN(id string) string {
	return rdsInstanceARNIn(awsRegion(), id)
}

func rdsResourceID() string {
	return "db-" + strings.ToUpper(strings.ReplaceAll(sim.NewUUID(), "-", ""))[:26]
}

func rdsXMLResponse(w http.ResponseWriter, op string, body string, requestID string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w,
		`<%sResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><%sResult>%s</%sResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></%sResponse>`,
		op, op, body, op, requestID, op)
}

func rdsErrorXML(w http.ResponseWriter, code, message string, status int, requestID string) {
	awsWriteError(w, awsErrorEnvelope{Protocol: awsQuery, Namespace: "http://rds.amazonaws.com/doc/2014-10-31/"}, status, code, message, requestID)
}

func renderRDSInstance(i RDSInstance) string {
	var b strings.Builder
	b.WriteString("<DBInstance>")
	fmt.Fprintf(&b, "<DBInstanceIdentifier>%s</DBInstanceIdentifier>", xmlEscape(i.DBInstanceIdentifier))
	fmt.Fprintf(&b, "<DbiResourceId>%s</DbiResourceId>", xmlEscape(i.DbiResourceId))
	fmt.Fprintf(&b, "<DBInstanceClass>%s</DBInstanceClass>", xmlEscape(i.DBInstanceClass))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(i.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(i.EngineVersion))
	if i.LicenseModel != "" {
		fmt.Fprintf(&b, "<LicenseModel>%s</LicenseModel>", xmlEscape(i.LicenseModel))
	}
	fmt.Fprintf(&b, "<DBInstanceStatus>%s</DBInstanceStatus>", xmlEscape(i.DBInstanceStatus))
	if i.DBClusterIdentifier != "" {
		fmt.Fprintf(&b, "<DBClusterIdentifier>%s</DBClusterIdentifier>", xmlEscape(i.DBClusterIdentifier))
	}
	fmt.Fprintf(&b, "<MasterUsername>%s</MasterUsername>", xmlEscape(i.MasterUsername))
	fmt.Fprintf(&b, "<DBName>%s</DBName>", xmlEscape(i.DBName))
	fmt.Fprintf(&b, "<AllocatedStorage>%d</AllocatedStorage>", i.AllocatedStorage)
	fmt.Fprintf(&b, "<AvailabilityZone>%s</AvailabilityZone>", xmlEscape(i.AvailabilityZone))
	fmt.Fprintf(&b, "<InstanceCreateTime>%s</InstanceCreateTime>", xmlEscape(i.InstanceCreateTime))
	fmt.Fprintf(&b, "<DBInstanceArn>%s</DBInstanceArn>", xmlEscape(i.ARN))
	fmt.Fprintf(&b, "<Endpoint><Address>%s</Address><Port>%d</Port></Endpoint>", xmlEscape(i.Endpoint), i.Port)
	fmt.Fprintf(&b, "<IAMDatabaseAuthenticationEnabled>%t</IAMDatabaseAuthenticationEnabled>", i.EnableIAMDatabaseAuthentication)
	fmt.Fprintf(&b, "<DeletionProtection>%t</DeletionProtection>", i.DeletionProtection)
	if i.BackupTarget != "" {
		fmt.Fprintf(&b, "<BackupTarget>%s</BackupTarget>", xmlEscape(i.BackupTarget))
	}
	fmt.Fprintf(&b, "<AutoMinorVersionUpgrade>%t</AutoMinorVersionUpgrade>", i.AutoMinorVersionUpgrade == nil || *i.AutoMinorVersionUpgrade)
	if i.DBParameterGroupName != "" {
		fmt.Fprintf(&b, "<DBParameterGroups><DBParameterGroup><DBParameterGroupName>%s</DBParameterGroupName><ParameterApplyStatus>%s</ParameterApplyStatus></DBParameterGroup></DBParameterGroups>",
			xmlEscape(i.DBParameterGroupName), rdsParameterApplyStatus(i))
	}
	if i.PreferredMaintenanceWindow != "" {
		fmt.Fprintf(&b, "<PreferredMaintenanceWindow>%s</PreferredMaintenanceWindow>", xmlEscape(i.PreferredMaintenanceWindow))
	}
	if reservation := rdsInstanceCapacityReservation(i); reservation != "" {
		fmt.Fprintf(&b, "<DBCapacityReservationIdentifier>%s</DBCapacityReservationIdentifier>", xmlEscape(reservation))
	}
	b.WriteString(renderRDSInstanceSettings(i))
	b.WriteString(renderRDSPendingModifiedValues(i))
	b.WriteString(renderRDSReplicaStatusInfos(i))
	b.WriteString(renderRDSInstanceBackups(i))
	b.WriteString(rdsRenderAutomatedBackupsReplications(i.AutomatedBackupsReplications))
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

// Real RDS transitions an instance through starting→available and
// stopping→stopped. The simulator keeps the instance volume but tears down
// the engine and listener while stopped, then reinstalls them on start.
