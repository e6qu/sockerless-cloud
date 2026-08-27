package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// registerRDSRestoreExtras mounts the second tranche of Amazon RDS
// (Aurora) control-plane operations on top of the core slice in
// rds.go: point-in-time / S3 restores, reserved instances, blue/green
// deployments, zero-ETL integrations, tenant databases, Aurora
// Limitless shard groups, database activity streams, Aurora backtrack,
// snapshot-export tasks, and the remaining cluster / snapshot-attribute
// / static-describe operations.
//
// Every handler does faithful CRUD against real sim.Store rows (the
// restores create a new instance/cluster from the stored source; the
// named resources persist and read back; toggles flip a status field)
// and renders the exact awsQuery XML shapes RDS returns, matching the
// rds.smithy.json.gz spec the runtime validator checks against.
func registerRDSRestoreExtras(r *sim.AWSQueryRouter, srv *sim.Server) {
	rdsReservedInstances = sim.MakeStore[RDSReservedInstance](srv.DB(), "rds_reserved_instances")
	rdsBlueGreenDeployments = sim.MakeStore[RDSBlueGreenDeployment](srv.DB(), "rds_bluegreen_deployments")
	rdsIntegrations = sim.MakeStore[RDSIntegration](srv.DB(), "rds_integrations")
	rdsTenantDatabases = sim.MakeStore[RDSTenantDatabase](srv.DB(), "rds_tenant_databases")
	rdsShardGroups = sim.MakeStore[RDSShardGroup](srv.DB(), "rds_shard_groups")
	rdsExportTasks = sim.MakeStore[RDSExportTask](srv.DB(), "rds_export_tasks")
	rdsActivityStreamStatus = sim.MakeStore[string](srv.DB(), "rds_activity_stream_status")
	rdsClusterBacktracks = sim.MakeStore[[]rdsBacktrack](srv.DB(), "rds_cluster_backtracks")
	rdsHTTPEndpointEnabled = sim.MakeStore[bool](srv.DB(), "rds_http_endpoint_enabled")
	rdsClusterSnapshotAttrs = sim.MakeStore[[]string](srv.DB(), "rds_cluster_snapshot_attributes")

	// Restore family.
	r.RegisterVersioned(rdsAPIVersion, "RestoreDBClusterFromSnapshot", handleRDSRestoreClusterFromSnapshot)
	r.RegisterVersioned(rdsAPIVersion, "RestoreDBClusterToPointInTime", handleRDSRestoreClusterToPointInTime)
	r.RegisterVersioned(rdsAPIVersion, "RestoreDBInstanceToPointInTime", handleRDSRestoreInstanceToPointInTime)
	r.RegisterVersioned(rdsAPIVersion, "RestoreDBClusterFromS3", handleRDSRestoreClusterFromS3)
	r.RegisterVersioned(rdsAPIVersion, "RestoreDBInstanceFromS3", handleRDSRestoreInstanceFromS3)

	// Reserved instances.
	r.RegisterVersioned(rdsAPIVersion, "DescribeReservedDBInstances", handleRDSDescribeReservedInstances)
	r.RegisterVersioned(rdsAPIVersion, "DescribeReservedDBInstancesOfferings", handleRDSDescribeReservedInstancesOfferings)
	r.RegisterVersioned(rdsAPIVersion, "PurchaseReservedDBInstancesOffering", handleRDSPurchaseReservedInstancesOffering)

	// Blue/green deployments.
	r.RegisterVersioned(rdsAPIVersion, "CreateBlueGreenDeployment", handleRDSCreateBlueGreenDeployment)
	r.RegisterVersioned(rdsAPIVersion, "DescribeBlueGreenDeployments", handleRDSDescribeBlueGreenDeployments)
	r.RegisterVersioned(rdsAPIVersion, "DeleteBlueGreenDeployment", handleRDSDeleteBlueGreenDeployment)
	r.RegisterVersioned(rdsAPIVersion, "SwitchoverBlueGreenDeployment", handleRDSSwitchoverBlueGreenDeployment)

	// Zero-ETL integrations.
	r.RegisterVersioned(rdsAPIVersion, "CreateIntegration", handleRDSCreateIntegration)
	r.RegisterVersioned(rdsAPIVersion, "DescribeIntegrations", handleRDSDescribeIntegrations)
	r.RegisterVersioned(rdsAPIVersion, "ModifyIntegration", handleRDSModifyIntegration)
	r.RegisterVersioned(rdsAPIVersion, "DeleteIntegration", handleRDSDeleteIntegration)

	// Tenant databases.
	r.RegisterVersioned(rdsAPIVersion, "CreateTenantDatabase", handleRDSCreateTenantDatabase)
	r.RegisterVersioned(rdsAPIVersion, "DescribeTenantDatabases", handleRDSDescribeTenantDatabases)
	r.RegisterVersioned(rdsAPIVersion, "ModifyTenantDatabase", handleRDSModifyTenantDatabase)
	r.RegisterVersioned(rdsAPIVersion, "DeleteTenantDatabase", handleRDSDeleteTenantDatabase)

	// Aurora Limitless shard groups.
	r.RegisterVersioned(rdsAPIVersion, "CreateDBShardGroup", handleRDSCreateShardGroup)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBShardGroups", handleRDSDescribeShardGroups)
	r.RegisterVersioned(rdsAPIVersion, "ModifyDBShardGroup", handleRDSModifyShardGroup)
	r.RegisterVersioned(rdsAPIVersion, "DeleteDBShardGroup", handleRDSDeleteShardGroup)
	r.RegisterVersioned(rdsAPIVersion, "RebootDBShardGroup", handleRDSRebootShardGroup)

	// Database activity streams.
	r.RegisterVersioned(rdsAPIVersion, "StartActivityStream", handleRDSStartActivityStream)
	r.RegisterVersioned(rdsAPIVersion, "StopActivityStream", handleRDSStopActivityStream)
	r.RegisterVersioned(rdsAPIVersion, "ModifyActivityStream", handleRDSModifyActivityStream)

	// Aurora backtrack.
	r.RegisterVersioned(rdsAPIVersion, "BacktrackDBCluster", handleRDSBacktrackCluster)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBClusterBacktracks", handleRDSDescribeClusterBacktracks)

	// Snapshot-export tasks.
	r.RegisterVersioned(rdsAPIVersion, "StartExportTask", handleRDSStartExportTask)
	r.RegisterVersioned(rdsAPIVersion, "DescribeExportTasks", handleRDSDescribeExportTasks)
	r.RegisterVersioned(rdsAPIVersion, "CancelExportTask", handleRDSCancelExportTask)

	// Cluster operations.
	r.RegisterVersioned(rdsAPIVersion, "RebootDBCluster", handleRDSRebootCluster)
	r.RegisterVersioned(rdsAPIVersion, "ResetDBClusterParameterGroup", handleRDSResetClusterParameterGroup)
	r.RegisterVersioned(rdsAPIVersion, "ModifyDBClusterEndpoint", handleRDSModifyClusterEndpoint)
	r.RegisterVersioned(rdsAPIVersion, "FailoverGlobalCluster", handleRDSFailoverGlobalCluster)
	r.RegisterVersioned(rdsAPIVersion, "RemoveFromGlobalCluster", handleRDSRemoveFromGlobalCluster)
	r.RegisterVersioned(rdsAPIVersion, "PromoteReadReplicaDBCluster", handleRDSPromoteReadReplicaCluster)
	r.RegisterVersioned(rdsAPIVersion, "EnableHttpEndpoint", handleRDSEnableHTTPEndpoint)
	r.RegisterVersioned(rdsAPIVersion, "DisableHttpEndpoint", handleRDSDisableHTTPEndpoint)

	// Cluster-snapshot attributes and ModifyDBSnapshot.
	r.RegisterVersioned(rdsAPIVersion, "ModifyDBClusterSnapshotAttribute", handleRDSModifyClusterSnapshotAttribute)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBClusterSnapshotAttributes", handleRDSDescribeClusterSnapshotAttributes)
	r.RegisterVersioned(rdsAPIVersion, "ModifyDBSnapshot", handleRDSModifySnapshot)

	// Static describes (real-shaped catalogs / defaults).
	r.RegisterVersioned(rdsAPIVersion, "DescribeOptionGroupOptions", handleRDSDescribeOptionGroupOptions)
	r.RegisterVersioned(rdsAPIVersion, "DescribeEngineDefaultParameters", handleRDSDescribeEngineDefaultParameters)
	r.RegisterVersioned(rdsAPIVersion, "DescribeEngineDefaultClusterParameters", handleRDSDescribeEngineDefaultClusterParameters)
	r.RegisterVersioned(rdsAPIVersion, "DescribeSourceRegions", handleRDSDescribeSourceRegions)
	r.RegisterVersioned(rdsAPIVersion, "DescribeDBMajorEngineVersions", handleRDSDescribeDBMajorEngineVersions)
}

// Stores for the resources introduced here. The instance/cluster/snapshot
// stores are declared in rds.go and reused.

var (
	rdsReservedInstances    sim.Store[RDSReservedInstance]
	rdsBlueGreenDeployments sim.Store[RDSBlueGreenDeployment]
	rdsIntegrations         sim.Store[RDSIntegration]
	rdsTenantDatabases      sim.Store[RDSTenantDatabase]
	rdsShardGroups          sim.Store[RDSShardGroup]
	rdsExportTasks          sim.Store[RDSExportTask]
)

// RDSReservedInstance models a purchased reserved DB instance — a row
// created by PurchaseReservedDBInstancesOffering from a catalog
// offering. The reservation is a billing artifact; no engine runs.
type RDSReservedInstance struct {
	ReservedDBInstanceId          string
	ReservedDBInstancesOfferingId string
	DBInstanceClass               string
	DBInstanceCount               int
	Duration                      int
	ProductDescription            string
	OfferingType                  string
	MultiAZ                       bool
	State                         string
	FixedPrice                    float64
	UsagePrice                    float64
	CurrencyCode                  string
	StartTime                     string
	ARN                           string
}

// RDSBlueGreenDeployment models a blue/green deployment: a named pair
// binding a source (blue) DB cluster/instance ARN to a freshly created
// green replica. Switchover swaps the Source/Target roles.
type RDSBlueGreenDeployment struct {
	BlueGreenDeploymentIdentifier string
	BlueGreenDeploymentName       string
	Source                        string
	Target                        string
	Status                        string // PROVISIONING | AVAILABLE | SWITCHOVER_COMPLETED | DELETING
	CreateTime                    string
	Tags                          map[string]string
}

// RDSIntegration models a zero-ETL integration linking a source DB
// (cluster ARN) to a target (e.g. Redshift namespace ARN).
type RDSIntegration struct {
	IntegrationName string
	IntegrationArn  string
	SourceArn       string
	TargetArn       string
	KMSKeyId        string
	Description     string
	Status          string // creating | active | modifying | deleting
	CreateTime      string
	Tags            map[string]string
}

// RDSTenantDatabase models a tenant database inside a multi-tenant
// (Oracle CDB) DB instance.
type RDSTenantDatabase struct {
	DBInstanceIdentifier     string
	TenantDBName             string
	TenantDatabaseResourceId string
	DbiResourceId            string
	Status                   string // available | deleting
	MasterUsername           string
	CharacterSetName         string
	NcharCharacterSetName    string
	DeletionProtection       bool
	CreateTime               string
	ARN                      string
	Tags                     map[string]string
}

// RDSShardGroup models an Aurora Limitless DB shard group attached to a
// cluster.
type RDSShardGroup struct {
	DBShardGroupIdentifier string
	DBClusterIdentifier    string
	DBShardGroupResourceId string
	ComputeRedundancy      int
	MaxACU                 float64
	MinACU                 float64
	PubliclyAccessible     bool
	Endpoint               string
	Status                 string // creating | available | modifying | deleting
	ARN                    string
	Tags                   map[string]string
}

// RDSExportTask models a snapshot-to-S3 export task. With no real
// engine extract, the task settles to COMPLETE inline (100%).
type RDSExportTask struct {
	ExportTaskIdentifier string
	SourceArn            string
	S3Bucket             string
	S3Prefix             string
	IamRoleArn           string
	KmsKeyId             string
	Status               string // STARTING | IN_PROGRESS | COMPLETE | CANCELED
	PercentProgress      int
	SnapshotTime         string
	TaskStartTime        string
	TaskEndTime          string
	SourceType           string // SNAPSHOT | CLUSTER
	ExportOnly           []string
}

// Restore family

// rdsClusterFromSource builds a new RDSCluster row from an optional
// source cluster/snapshot, applying the new identifier and any
// request overrides. It centralizes the shared restore shape so the
// four cluster-restore handlers don't clone it.
func rdsClusterFromSource(r *http.Request, newID, engine, engineVersion string) RDSCluster {
	port := rdsDefaultPort(engine)
	if p := atoiOrZero(r.FormValue("Port")); p > 0 {
		port = p
	}
	if engineVersion == "" {
		engineVersion = rdsDefaultEngineVersion(engine)
	}
	paramGroup := r.FormValue("DBClusterParameterGroupName")
	if paramGroup == "" {
		paramGroup = "default." + engine
	}
	return RDSCluster{
		DBClusterIdentifier:        newID,
		DbClusterResourceId:        rdsClusterResourceID(),
		Engine:                     engine,
		EngineVersion:              engineVersion,
		EngineMode:                 "provisioned",
		Status:                     "available",
		DatabaseName:               r.FormValue("DatabaseName"),
		MasterUsername:             r.FormValue("MasterUsername"),
		Port:                       port,
		Endpoint:                   fmt.Sprintf("%s.cluster-%s.%s.rds.amazonaws.com", newID, "sim", awsRegion()),
		ReaderEndpoint:             fmt.Sprintf("%s.cluster-ro-%s.%s.rds.amazonaws.com", newID, "sim", awsRegion()),
		DBClusterParameterGroup:    paramGroup,
		DBSubnetGroup:              r.FormValue("DBSubnetGroupName"),
		BackupRetentionPeriod:      1,
		ClusterCreateTime:          time.Now().UTC().Format(time.RFC3339),
		AvailabilityZones:          []string{awsRegion() + "a", awsRegion() + "b", awsRegion() + "c"},
		PreferredBackupWindow:      "07:00-09:00",
		PreferredMaintenanceWindow: "mon:00:00-mon:03:00",
		ARN:                        rdsClusterARN(newID),
		Tags:                       parseAWSQueryTagMap(r, "Tags.Tag"),
	}
}

func handleRDSRestoreClusterFromSnapshot(w http.ResponseWriter, r *http.Request) {
	newID := r.FormValue("DBClusterIdentifier")
	snapID := r.FormValue("SnapshotIdentifier")
	if newID == "" || snapID == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBClusterIdentifier and SnapshotIdentifier are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
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
	if _, exists := rdsClusters.Get(newID); exists {
		rdsErrorXML(w, "DBClusterAlreadyExistsFault",
			fmt.Sprintf("DBCluster %q already exists", newID),
			http.StatusConflict, sim.RequestID(r.Context()))
		return
	}
	engine := r.FormValue("Engine")
	if engine == "" {
		engine = snap.Engine
	}
	cl := rdsClusterFromSource(r, newID, engine, snap.EngineVersion)
	cl.MasterUsername = snap.MasterUsername
	cl.AllocatedStorage = snap.AllocatedStorage
	cl.StorageEncrypted = snap.StorageEncrypted
	rdsClusters.Put(newID, cl)
	rdsXMLResponse(w, "RestoreDBClusterFromSnapshot", renderRDSCluster(cl), sim.RequestID(r.Context()))
}

func handleRDSRestoreClusterToPointInTime(w http.ResponseWriter, r *http.Request) {
	newID := r.FormValue("DBClusterIdentifier")
	srcID := r.FormValue("SourceDBClusterIdentifier")
	if newID == "" || srcID == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBClusterIdentifier and SourceDBClusterIdentifier are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	src, ok := rdsClusters.Get(srcID)
	if !ok {
		src, ok = findRDSClusterByARN(srcID)
		if !ok {
			rdsErrorXML(w, "DBClusterNotFoundFault",
				fmt.Sprintf("DBCluster %q not found", srcID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	if _, exists := rdsClusters.Get(newID); exists {
		rdsErrorXML(w, "DBClusterAlreadyExistsFault",
			fmt.Sprintf("DBCluster %q already exists", newID),
			http.StatusConflict, sim.RequestID(r.Context()))
		return
	}
	cl := rdsClusterFromSource(r, newID, src.Engine, src.EngineVersion)
	cl.MasterUsername = src.MasterUsername
	cl.DatabaseName = src.DatabaseName
	cl.AllocatedStorage = src.AllocatedStorage
	cl.StorageEncrypted = src.StorageEncrypted
	cl.Port = src.Port
	rdsClusters.Put(newID, cl)
	rdsXMLResponse(w, "RestoreDBClusterToPointInTime", renderRDSCluster(cl), sim.RequestID(r.Context()))
}

func handleRDSRestoreClusterFromS3(w http.ResponseWriter, r *http.Request) {
	newID := r.FormValue("DBClusterIdentifier")
	if newID == "" {
		rdsErrorXML(w, "MissingParameter", "DBClusterIdentifier is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, exists := rdsClusters.Get(newID); exists {
		rdsErrorXML(w, "DBClusterAlreadyExistsFault",
			fmt.Sprintf("DBCluster %q already exists", newID),
			http.StatusConflict, sim.RequestID(r.Context()))
		return
	}
	engine := r.FormValue("Engine")
	if engine == "" {
		engine = "aurora-mysql"
	}
	cl := rdsClusterFromSource(r, newID, engine, r.FormValue("EngineVersion"))
	cl.AllocatedStorage = atoiOrZero(r.FormValue("AllocatedStorage"))
	rdsClusters.Put(newID, cl)
	rdsXMLResponse(w, "RestoreDBClusterFromS3", renderRDSCluster(cl), sim.RequestID(r.Context()))
}

// rdsInstanceFromSource builds a new RDSInstance row for the
// instance-restore handlers, applying the new identifier and request
// overrides over an optional source instance.
func rdsInstanceFromSource(r *http.Request, newID string, src RDSInstance, engine string) RDSInstance {
	if engine == "" {
		engine = src.Engine
		if engine == "" {
			engine = "mysql"
		}
	}
	class := r.FormValue("DBInstanceClass")
	if class == "" {
		class = src.DBInstanceClass
	}
	engineVersion := r.FormValue("EngineVersion")
	if engineVersion == "" {
		engineVersion = src.EngineVersion
	}
	storage := src.AllocatedStorage
	if v := atoiOrZero(r.FormValue("AllocatedStorage")); v > 0 {
		storage = v
	}
	return RDSInstance{
		DBInstanceIdentifier: newID,
		DbiResourceId:        rdsResourceID(),
		DBInstanceClass:      class,
		Engine:               engine,
		EngineVersion:        engineVersion,
		DBInstanceStatus:     "available",
		MasterUsername:       src.MasterUsername,
		DBName:               src.DBName,
		AllocatedStorage:     storage,
		Endpoint:             fmt.Sprintf("%s.%s.rds.amazonaws.com", newID, awsRegion()),
		Port:                 rdsDefaultPort(engine),
		AvailabilityZone:     awsRegion() + "a",
		InstanceCreateTime:   time.Now().UTC().Format(time.RFC3339),
		ARN:                  rdsInstanceARN(newID),
		Tags:                 parseAWSQueryTagMap(r, "Tags.Tag"),
	}
}

func handleRDSRestoreInstanceToPointInTime(w http.ResponseWriter, r *http.Request) {
	newID := r.FormValue("TargetDBInstanceIdentifier")
	srcID := r.FormValue("SourceDBInstanceIdentifier")
	if newID == "" {
		rdsErrorXML(w, "MissingParameter", "TargetDBInstanceIdentifier is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	var src RDSInstance
	if srcID != "" {
		s, ok := rdsInstances.Get(srcID)
		if !ok {
			rdsErrorXML(w, "DBInstanceNotFound",
				fmt.Sprintf("DBInstance %q not found", srcID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
		src = s
	}
	if _, exists := rdsInstances.Get(newID); exists {
		rdsErrorXML(w, "DBInstanceAlreadyExists",
			fmt.Sprintf("DBInstance %q already exists", newID),
			http.StatusConflict, sim.RequestID(r.Context()))
		return
	}
	inst := rdsInstanceFromSource(r, newID, src, r.FormValue("Engine"))
	rdsInstances.Put(newID, inst)
	rdsXMLResponse(w, "RestoreDBInstanceToPointInTime", renderRDSInstance(inst), sim.RequestID(r.Context()))
}

func handleRDSRestoreInstanceFromS3(w http.ResponseWriter, r *http.Request) {
	newID := r.FormValue("DBInstanceIdentifier")
	if newID == "" {
		rdsErrorXML(w, "MissingParameter", "DBInstanceIdentifier is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, exists := rdsInstances.Get(newID); exists {
		rdsErrorXML(w, "DBInstanceAlreadyExists",
			fmt.Sprintf("DBInstance %q already exists", newID),
			http.StatusConflict, sim.RequestID(r.Context()))
		return
	}
	inst := rdsInstanceFromSource(r, newID, RDSInstance{}, r.FormValue("Engine"))
	inst.MasterUsername = r.FormValue("MasterUsername")
	rdsInstances.Put(newID, inst)
	rdsXMLResponse(w, "RestoreDBInstanceFromS3", renderRDSInstance(inst), sim.RequestID(r.Context()))
}

// Reserved instances

// rdsReservedOfferings is the small real-shaped offering catalog the
// sim serves. The IDs are fixed so a purchase referencing one resolves.
var rdsReservedOfferings = []RDSReservedInstance{
	{
		ReservedDBInstancesOfferingId: "438012d3-4052-4cc7-b2e3-8d3372e0e706",
		DBInstanceClass:               "db.t3.micro",
		Duration:                      31536000,
		ProductDescription:            "mysql",
		OfferingType:                  "No Upfront",
		MultiAZ:                       false,
		FixedPrice:                    0,
		UsagePrice:                    0.017,
		CurrencyCode:                  "USD",
	},
	{
		ReservedDBInstancesOfferingId: "649fd0c8-cf6d-47a0-bfa6-060f8e75e95f",
		DBInstanceClass:               "db.r5.large",
		Duration:                      94608000,
		ProductDescription:            "postgresql",
		OfferingType:                  "All Upfront",
		MultiAZ:                       true,
		FixedPrice:                    3200,
		UsagePrice:                    0,
		CurrencyCode:                  "USD",
	},
}

func renderRDSRecurringCharges() string {
	// A single hourly recurring charge, matching the offering catalog's
	// usage price. Members are named <RecurringCharge> per the spec
	// RecurringChargeList xmlName.
	return "<RecurringCharges><RecurringCharge><RecurringChargeAmount>0.017</RecurringChargeAmount>" +
		"<RecurringChargeFrequency>Hourly</RecurringChargeFrequency></RecurringCharge></RecurringCharges>"
}

func renderRDSReservedOffering(o RDSReservedInstance) string {
	var b strings.Builder
	b.WriteString("<ReservedDBInstancesOffering>")
	fmt.Fprintf(&b, "<ReservedDBInstancesOfferingId>%s</ReservedDBInstancesOfferingId>", xmlEscape(o.ReservedDBInstancesOfferingId))
	fmt.Fprintf(&b, "<DBInstanceClass>%s</DBInstanceClass>", xmlEscape(o.DBInstanceClass))
	fmt.Fprintf(&b, "<Duration>%d</Duration>", o.Duration)
	fmt.Fprintf(&b, "<FixedPrice>%g</FixedPrice>", o.FixedPrice)
	fmt.Fprintf(&b, "<UsagePrice>%g</UsagePrice>", o.UsagePrice)
	fmt.Fprintf(&b, "<CurrencyCode>%s</CurrencyCode>", xmlEscape(o.CurrencyCode))
	fmt.Fprintf(&b, "<ProductDescription>%s</ProductDescription>", xmlEscape(o.ProductDescription))
	fmt.Fprintf(&b, "<OfferingType>%s</OfferingType>", xmlEscape(o.OfferingType))
	fmt.Fprintf(&b, "<MultiAZ>%t</MultiAZ>", o.MultiAZ)
	b.WriteString(renderRDSRecurringCharges())
	b.WriteString("</ReservedDBInstancesOffering>")
	return b.String()
}

func renderRDSReservedInstance(ri RDSReservedInstance) string {
	var b strings.Builder
	b.WriteString("<ReservedDBInstance>")
	fmt.Fprintf(&b, "<ReservedDBInstanceId>%s</ReservedDBInstanceId>", xmlEscape(ri.ReservedDBInstanceId))
	fmt.Fprintf(&b, "<ReservedDBInstancesOfferingId>%s</ReservedDBInstancesOfferingId>", xmlEscape(ri.ReservedDBInstancesOfferingId))
	fmt.Fprintf(&b, "<DBInstanceClass>%s</DBInstanceClass>", xmlEscape(ri.DBInstanceClass))
	fmt.Fprintf(&b, "<StartTime>%s</StartTime>", xmlEscape(ri.StartTime))
	fmt.Fprintf(&b, "<Duration>%d</Duration>", ri.Duration)
	fmt.Fprintf(&b, "<FixedPrice>%g</FixedPrice>", ri.FixedPrice)
	fmt.Fprintf(&b, "<UsagePrice>%g</UsagePrice>", ri.UsagePrice)
	fmt.Fprintf(&b, "<CurrencyCode>%s</CurrencyCode>", xmlEscape(ri.CurrencyCode))
	fmt.Fprintf(&b, "<DBInstanceCount>%d</DBInstanceCount>", ri.DBInstanceCount)
	fmt.Fprintf(&b, "<ProductDescription>%s</ProductDescription>", xmlEscape(ri.ProductDescription))
	fmt.Fprintf(&b, "<OfferingType>%s</OfferingType>", xmlEscape(ri.OfferingType))
	fmt.Fprintf(&b, "<MultiAZ>%t</MultiAZ>", ri.MultiAZ)
	fmt.Fprintf(&b, "<State>%s</State>", xmlEscape(ri.State))
	b.WriteString(renderRDSRecurringCharges())
	fmt.Fprintf(&b, "<ReservedDBInstanceArn>%s</ReservedDBInstanceArn>", xmlEscape(ri.ARN))
	b.WriteString("</ReservedDBInstance>")
	return b.String()
}

func handleRDSDescribeReservedInstancesOfferings(w http.ResponseWriter, r *http.Request) {
	wantOffering := r.FormValue("ReservedDBInstancesOfferingId")
	wantClass := r.FormValue("DBInstanceClass")
	var b strings.Builder
	b.WriteString("<ReservedDBInstancesOfferings>")
	for _, o := range rdsReservedOfferings {
		if wantOffering != "" && o.ReservedDBInstancesOfferingId != wantOffering {
			continue
		}
		if wantClass != "" && o.DBInstanceClass != wantClass {
			continue
		}
		b.WriteString(renderRDSReservedOffering(o))
	}
	b.WriteString("</ReservedDBInstancesOfferings>")
	rdsXMLResponse(w, "DescribeReservedDBInstancesOfferings", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDescribeReservedInstances(w http.ResponseWriter, r *http.Request) {
	wantID := r.FormValue("ReservedDBInstanceId")
	var b strings.Builder
	b.WriteString("<ReservedDBInstances>")
	for _, ri := range rdsReservedInstances.List() {
		if wantID != "" && ri.ReservedDBInstanceId != wantID {
			continue
		}
		b.WriteString(renderRDSReservedInstance(ri))
	}
	b.WriteString("</ReservedDBInstances>")
	rdsXMLResponse(w, "DescribeReservedDBInstances", b.String(), sim.RequestID(r.Context()))
}

func handleRDSPurchaseReservedInstancesOffering(w http.ResponseWriter, r *http.Request) {
	offeringID := r.FormValue("ReservedDBInstancesOfferingId")
	if offeringID == "" {
		rdsErrorXML(w, "MissingParameter", "ReservedDBInstancesOfferingId is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	var offering *RDSReservedInstance
	for i := range rdsReservedOfferings {
		if rdsReservedOfferings[i].ReservedDBInstancesOfferingId == offeringID {
			offering = &rdsReservedOfferings[i]
			break
		}
	}
	if offering == nil {
		rdsErrorXML(w, "ReservedDBInstancesOfferingNotFound",
			fmt.Sprintf("Reserved offering %q not found", offeringID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	count := 1
	if v := atoiOrZero(r.FormValue("DBInstanceCount")); v > 0 {
		count = v
	}
	resID := r.FormValue("ReservedDBInstanceId")
	if resID == "" {
		resID = "ri-" + strings.ToLower(strings.ReplaceAll(generateUUID(), "-", ""))[:17]
	}
	ri := RDSReservedInstance{
		ReservedDBInstanceId:          resID,
		ReservedDBInstancesOfferingId: offering.ReservedDBInstancesOfferingId,
		DBInstanceClass:               offering.DBInstanceClass,
		DBInstanceCount:               count,
		Duration:                      offering.Duration,
		ProductDescription:            offering.ProductDescription,
		OfferingType:                  offering.OfferingType,
		MultiAZ:                       offering.MultiAZ,
		State:                         "active",
		FixedPrice:                    offering.FixedPrice,
		UsagePrice:                    offering.UsagePrice,
		CurrencyCode:                  offering.CurrencyCode,
		StartTime:                     time.Now().UTC().Format(time.RFC3339),
		ARN:                           fmt.Sprintf("arn:aws:rds:%s:%s:ri:%s", awsRegion(), awsAccountID(), resID),
	}
	rdsReservedInstances.Put(resID, ri)
	rdsXMLResponse(w, "PurchaseReservedDBInstancesOffering", renderRDSReservedInstance(ri), sim.RequestID(r.Context()))
}

// Blue/green deployments

func renderRDSBlueGreenDeployment(d RDSBlueGreenDeployment) string {
	var b strings.Builder
	b.WriteString("<BlueGreenDeployment>")
	fmt.Fprintf(&b, "<BlueGreenDeploymentIdentifier>%s</BlueGreenDeploymentIdentifier>", xmlEscape(d.BlueGreenDeploymentIdentifier))
	fmt.Fprintf(&b, "<BlueGreenDeploymentName>%s</BlueGreenDeploymentName>", xmlEscape(d.BlueGreenDeploymentName))
	fmt.Fprintf(&b, "<Source>%s</Source>", xmlEscape(d.Source))
	fmt.Fprintf(&b, "<Target>%s</Target>", xmlEscape(d.Target))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(d.Status))
	fmt.Fprintf(&b, "<CreateTime>%s</CreateTime>", xmlEscape(d.CreateTime))
	// SwitchoverDetails members serialize as <member> (no xmlName in the
	// spec's SwitchoverDetailList).
	b.WriteString("<SwitchoverDetails><member>")
	fmt.Fprintf(&b, "<SourceMember>%s</SourceMember>", xmlEscape(d.Source))
	fmt.Fprintf(&b, "<TargetMember>%s</TargetMember>", xmlEscape(d.Target))
	if d.Status == "SWITCHOVER_COMPLETED" {
		b.WriteString("<Status>SWITCHOVER_COMPLETED</Status>")
	} else {
		b.WriteString("<Status>AVAILABLE</Status>")
	}
	b.WriteString("</member></SwitchoverDetails>")
	// Tasks members serialize as <member> (no xmlName in
	// BlueGreenDeploymentTaskList).
	b.WriteString("<Tasks><member><Name>CREATING_READ_REPLICA_OF_SOURCE</Name><Status>COMPLETED</Status></member></Tasks>")
	b.WriteString(renderRDSTagList(d.Tags))
	b.WriteString("</BlueGreenDeployment>")
	return b.String()
}

func handleRDSCreateBlueGreenDeployment(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("BlueGreenDeploymentName")
	source := r.FormValue("Source")
	if name == "" || source == "" {
		rdsErrorXML(w, "MissingParameter",
			"BlueGreenDeploymentName and Source are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	id := "bgd-" + strings.ToLower(strings.ReplaceAll(generateUUID(), "-", ""))[:17]
	target := source + "-green"
	d := RDSBlueGreenDeployment{
		BlueGreenDeploymentIdentifier: id,
		BlueGreenDeploymentName:       name,
		Source:                        source,
		Target:                        target,
		Status:                        "AVAILABLE",
		CreateTime:                    time.Now().UTC().Format(time.RFC3339),
		Tags:                          parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsBlueGreenDeployments.Put(id, d)
	rdsXMLResponse(w, "CreateBlueGreenDeployment", renderRDSBlueGreenDeployment(d), sim.RequestID(r.Context()))
}

func handleRDSDescribeBlueGreenDeployments(w http.ResponseWriter, r *http.Request) {
	wantID := r.FormValue("BlueGreenDeploymentIdentifier")
	var b strings.Builder
	b.WriteString("<BlueGreenDeployments>")
	for _, d := range rdsBlueGreenDeployments.List() {
		if wantID != "" && d.BlueGreenDeploymentIdentifier != wantID {
			continue
		}
		// List members serialize as <member> (no xmlName in
		// BlueGreenDeploymentList) — renderRDSBlueGreenDeployment emits the
		// <BlueGreenDeployment> element, so wrap it as a list member.
		b.WriteString("<member>")
		b.WriteString(strings.TrimSuffix(strings.TrimPrefix(renderRDSBlueGreenDeployment(d), "<BlueGreenDeployment>"), "</BlueGreenDeployment>"))
		b.WriteString("</member>")
	}
	b.WriteString("</BlueGreenDeployments>")
	rdsXMLResponse(w, "DescribeBlueGreenDeployments", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeleteBlueGreenDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("BlueGreenDeploymentIdentifier")
	d, ok := rdsBlueGreenDeployments.Get(id)
	if !ok {
		rdsErrorXML(w, "BlueGreenDeploymentNotFoundFault",
			fmt.Sprintf("BlueGreenDeployment %q not found", id),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsBlueGreenDeployments.Delete(id)
	d.Status = "DELETING"
	rdsXMLResponse(w, "DeleteBlueGreenDeployment", renderRDSBlueGreenDeployment(d), sim.RequestID(r.Context()))
}

func handleRDSSwitchoverBlueGreenDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("BlueGreenDeploymentIdentifier")
	_, ok := rdsBlueGreenDeployments.Get(id)
	if !ok {
		rdsErrorXML(w, "BlueGreenDeploymentNotFoundFault",
			fmt.Sprintf("BlueGreenDeployment %q not found", id),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	// Switchover promotes the green (Target) environment: the roles swap
	// and the deployment settles to SWITCHOVER_COMPLETED.
	rdsBlueGreenDeployments.Update(id, func(x *RDSBlueGreenDeployment) {
		x.Source, x.Target = x.Target, x.Source
		x.Status = "SWITCHOVER_COMPLETED"
	})
	d, _ := rdsBlueGreenDeployments.Get(id)
	rdsXMLResponse(w, "SwitchoverBlueGreenDeployment", renderRDSBlueGreenDeployment(d), sim.RequestID(r.Context()))
}

// Zero-ETL integrations

// renderRDSIntegrationInner emits the Integration member fields without
// a wrapping element. The single-resource ops (Create/Modify/Delete)
// bind their output to the Integration shape directly, so its members
// serialize straight into the <...Result>; the list op wraps each in
// the <Integration> element (the IntegrationList xmlName).
func renderRDSIntegrationInner(in RDSIntegration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<IntegrationName>%s</IntegrationName>", xmlEscape(in.IntegrationName))
	fmt.Fprintf(&b, "<IntegrationArn>%s</IntegrationArn>", xmlEscape(in.IntegrationArn))
	fmt.Fprintf(&b, "<SourceArn>%s</SourceArn>", xmlEscape(in.SourceArn))
	fmt.Fprintf(&b, "<TargetArn>%s</TargetArn>", xmlEscape(in.TargetArn))
	if in.KMSKeyId != "" {
		fmt.Fprintf(&b, "<KMSKeyId>%s</KMSKeyId>", xmlEscape(in.KMSKeyId))
	}
	if in.Description != "" {
		fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(in.Description))
	}
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(in.Status))
	fmt.Fprintf(&b, "<CreateTime>%s</CreateTime>", xmlEscape(in.CreateTime))
	// Integration.Tags is a TagList whose member is named <Tag>.
	b.WriteString("<Tags>")
	for k, v := range in.Tags {
		fmt.Fprintf(&b, "<Tag><Key>%s</Key><Value>%s</Value></Tag>", xmlEscape(k), xmlEscape(v))
	}
	b.WriteString("</Tags>")
	return b.String()
}

func renderRDSIntegration(in RDSIntegration) string {
	return "<Integration>" + renderRDSIntegrationInner(in) + "</Integration>"
}

func handleRDSCreateIntegration(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("IntegrationName")
	source := r.FormValue("SourceArn")
	target := r.FormValue("TargetArn")
	if name == "" || source == "" || target == "" {
		rdsErrorXML(w, "MissingParameter",
			"IntegrationName, SourceArn and TargetArn are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsIntegrations.Get(name); ok {
		rdsErrorXML(w, "IntegrationAlreadyExistsFault",
			fmt.Sprintf("Integration %q already exists", name),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	in := RDSIntegration{
		IntegrationName: name,
		IntegrationArn:  fmt.Sprintf("arn:aws:rds:%s:%s:integration:%s", awsRegion(), awsAccountID(), generateUUID()),
		SourceArn:       source,
		TargetArn:       target,
		KMSKeyId:        r.FormValue("KMSKeyId"),
		Description:     r.FormValue("Description"),
		Status:          "active",
		CreateTime:      time.Now().UTC().Format(time.RFC3339),
		Tags:            parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsIntegrations.Put(name, in)
	rdsXMLResponse(w, "CreateIntegration", renderRDSIntegrationInner(in), sim.RequestID(r.Context()))
}

func findRDSIntegration(idOrArn string) (RDSIntegration, bool) {
	if in, ok := rdsIntegrations.Get(idOrArn); ok {
		return in, true
	}
	for _, in := range rdsIntegrations.List() {
		if in.IntegrationArn == idOrArn {
			return in, true
		}
	}
	return RDSIntegration{}, false
}

func handleRDSDescribeIntegrations(w http.ResponseWriter, r *http.Request) {
	wantID := r.FormValue("IntegrationIdentifier")
	var b strings.Builder
	b.WriteString("<Integrations>")
	for _, in := range rdsIntegrations.List() {
		if wantID != "" && in.IntegrationArn != wantID && in.IntegrationName != wantID {
			continue
		}
		b.WriteString(renderRDSIntegration(in))
	}
	b.WriteString("</Integrations>")
	rdsXMLResponse(w, "DescribeIntegrations", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyIntegration(w http.ResponseWriter, r *http.Request) {
	wantID := r.FormValue("IntegrationIdentifier")
	in, ok := findRDSIntegration(wantID)
	if !ok {
		rdsErrorXML(w, "IntegrationNotFoundFault",
			fmt.Sprintf("Integration %q not found", wantID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	newName := r.FormValue("IntegrationName")
	rdsIntegrations.Update(in.IntegrationName, func(x *RDSIntegration) {
		x.Status = "active"
		if newName != "" {
			x.IntegrationName = newName
		}
	})
	// Re-key when renamed so Describe by the new name resolves.
	if newName != "" && newName != in.IntegrationName {
		updated, _ := rdsIntegrations.Get(in.IntegrationName)
		rdsIntegrations.Delete(in.IntegrationName)
		rdsIntegrations.Put(newName, updated)
	}
	in, _ = findRDSIntegration(wantID)
	rdsXMLResponse(w, "ModifyIntegration", renderRDSIntegrationInner(in), sim.RequestID(r.Context()))
}

func handleRDSDeleteIntegration(w http.ResponseWriter, r *http.Request) {
	wantID := r.FormValue("IntegrationIdentifier")
	in, ok := findRDSIntegration(wantID)
	if !ok {
		rdsErrorXML(w, "IntegrationNotFoundFault",
			fmt.Sprintf("Integration %q not found", wantID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsIntegrations.Delete(in.IntegrationName)
	in.Status = "deleting"
	rdsXMLResponse(w, "DeleteIntegration", renderRDSIntegrationInner(in), sim.RequestID(r.Context()))
}

// Tenant databases

func rdsTenantKey(instanceID, tenantName string) string {
	return instanceID + "/" + tenantName
}

func renderRDSTenantDatabase(t RDSTenantDatabase) string {
	var b strings.Builder
	b.WriteString("<TenantDatabase>")
	fmt.Fprintf(&b, "<TenantDatabaseResourceId>%s</TenantDatabaseResourceId>", xmlEscape(t.TenantDatabaseResourceId))
	fmt.Fprintf(&b, "<DBInstanceIdentifier>%s</DBInstanceIdentifier>", xmlEscape(t.DBInstanceIdentifier))
	fmt.Fprintf(&b, "<TenantDBName>%s</TenantDBName>", xmlEscape(t.TenantDBName))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(t.Status))
	fmt.Fprintf(&b, "<MasterUsername>%s</MasterUsername>", xmlEscape(t.MasterUsername))
	fmt.Fprintf(&b, "<DbiResourceId>%s</DbiResourceId>", xmlEscape(t.DbiResourceId))
	fmt.Fprintf(&b, "<CharacterSetName>%s</CharacterSetName>", xmlEscape(t.CharacterSetName))
	fmt.Fprintf(&b, "<NcharCharacterSetName>%s</NcharCharacterSetName>", xmlEscape(t.NcharCharacterSetName))
	fmt.Fprintf(&b, "<DeletionProtection>%t</DeletionProtection>", t.DeletionProtection)
	fmt.Fprintf(&b, "<TenantDatabaseCreateTime>%s</TenantDatabaseCreateTime>", xmlEscape(t.CreateTime))
	fmt.Fprintf(&b, "<TenantDatabaseARN>%s</TenantDatabaseARN>", xmlEscape(t.ARN))
	b.WriteString(renderRDSTagList(t.Tags))
	b.WriteString("</TenantDatabase>")
	return b.String()
}

func handleRDSCreateTenantDatabase(w http.ResponseWriter, r *http.Request) {
	instID := r.FormValue("DBInstanceIdentifier")
	tenantName := r.FormValue("TenantDBName")
	if instID == "" || tenantName == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBInstanceIdentifier and TenantDBName are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	key := rdsTenantKey(instID, tenantName)
	if _, ok := rdsTenantDatabases.Get(key); ok {
		rdsErrorXML(w, "TenantDatabaseAlreadyExistsFault",
			fmt.Sprintf("Tenant database %q already exists", tenantName),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	t := RDSTenantDatabase{
		DBInstanceIdentifier:     instID,
		TenantDBName:             tenantName,
		TenantDatabaseResourceId: "tdb-" + strings.ToUpper(strings.ReplaceAll(generateUUID(), "-", ""))[:24],
		DbiResourceId:            rdsResourceID(),
		Status:                   "available",
		MasterUsername:           r.FormValue("MasterUsername"),
		CharacterSetName:         r.FormValue("CharacterSetName"),
		NcharCharacterSetName:    r.FormValue("NcharCharacterSetName"),
		DeletionProtection:       r.FormValue("DeletionProtection") == "true",
		CreateTime:               time.Now().UTC().Format(time.RFC3339),
		ARN:                      fmt.Sprintf("arn:aws:rds:%s:%s:tenant-database:%s", awsRegion(), awsAccountID(), tenantName),
		Tags:                     parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsTenantDatabases.Put(key, t)
	rdsXMLResponse(w, "CreateTenantDatabase", renderRDSTenantDatabase(t), sim.RequestID(r.Context()))
}

func handleRDSDescribeTenantDatabases(w http.ResponseWriter, r *http.Request) {
	wantInst := r.FormValue("DBInstanceIdentifier")
	wantTenant := r.FormValue("TenantDBName")
	var b strings.Builder
	b.WriteString("<TenantDatabases>")
	for _, t := range rdsTenantDatabases.List() {
		if wantInst != "" && t.DBInstanceIdentifier != wantInst {
			continue
		}
		if wantTenant != "" && t.TenantDBName != wantTenant {
			continue
		}
		b.WriteString(renderRDSTenantDatabase(t))
	}
	b.WriteString("</TenantDatabases>")
	rdsXMLResponse(w, "DescribeTenantDatabases", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyTenantDatabase(w http.ResponseWriter, r *http.Request) {
	instID := r.FormValue("DBInstanceIdentifier")
	tenantName := r.FormValue("TenantDBName")
	key := rdsTenantKey(instID, tenantName)
	_, ok := rdsTenantDatabases.Get(key)
	if !ok {
		rdsErrorXML(w, "TenantDatabaseNotFoundFault",
			fmt.Sprintf("Tenant database %q not found", tenantName),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	newName := r.FormValue("NewTenantDBName")
	rdsTenantDatabases.Update(key, func(x *RDSTenantDatabase) {
		if v := r.FormValue("DeletionProtection"); v != "" {
			x.DeletionProtection = v == "true"
		}
		if newName != "" {
			x.TenantDBName = newName
			x.ARN = fmt.Sprintf("arn:aws:rds:%s:%s:tenant-database:%s", awsRegion(), awsAccountID(), newName)
		}
	})
	t, _ := rdsTenantDatabases.Get(key)
	// Re-key if renamed so Describe by the new name resolves.
	if newName != "" && newName != tenantName {
		rdsTenantDatabases.Delete(key)
		rdsTenantDatabases.Put(rdsTenantKey(instID, newName), t)
	}
	rdsXMLResponse(w, "ModifyTenantDatabase", renderRDSTenantDatabase(t), sim.RequestID(r.Context()))
}

func handleRDSDeleteTenantDatabase(w http.ResponseWriter, r *http.Request) {
	instID := r.FormValue("DBInstanceIdentifier")
	tenantName := r.FormValue("TenantDBName")
	key := rdsTenantKey(instID, tenantName)
	t, ok := rdsTenantDatabases.Get(key)
	if !ok {
		rdsErrorXML(w, "TenantDatabaseNotFoundFault",
			fmt.Sprintf("Tenant database %q not found", tenantName),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsTenantDatabases.Delete(key)
	t.Status = "deleting"
	rdsXMLResponse(w, "DeleteTenantDatabase", renderRDSTenantDatabase(t), sim.RequestID(r.Context()))
}

// Aurora Limitless shard groups

// renderRDSShardGroupInner emits the DBShardGroup member fields without
// a wrapping element — the Create/Modify/Delete/Reboot ops bind their
// output to the DBShardGroup shape directly; DescribeDBShardGroups
// wraps each in the <DBShardGroup> element (the DBShardGroupsList
// xmlName).
func renderRDSShardGroupInner(g RDSShardGroup) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<DBShardGroupResourceId>%s</DBShardGroupResourceId>", xmlEscape(g.DBShardGroupResourceId))
	fmt.Fprintf(&b, "<DBShardGroupIdentifier>%s</DBShardGroupIdentifier>", xmlEscape(g.DBShardGroupIdentifier))
	fmt.Fprintf(&b, "<DBClusterIdentifier>%s</DBClusterIdentifier>", xmlEscape(g.DBClusterIdentifier))
	fmt.Fprintf(&b, "<MaxACU>%g</MaxACU>", g.MaxACU)
	fmt.Fprintf(&b, "<MinACU>%g</MinACU>", g.MinACU)
	fmt.Fprintf(&b, "<ComputeRedundancy>%d</ComputeRedundancy>", g.ComputeRedundancy)
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(g.Status))
	fmt.Fprintf(&b, "<PubliclyAccessible>%t</PubliclyAccessible>", g.PubliclyAccessible)
	fmt.Fprintf(&b, "<Endpoint>%s</Endpoint>", xmlEscape(g.Endpoint))
	fmt.Fprintf(&b, "<DBShardGroupArn>%s</DBShardGroupArn>", xmlEscape(g.ARN))
	b.WriteString(renderRDSTagList(g.Tags))
	return b.String()
}

func renderRDSShardGroup(g RDSShardGroup) string {
	return "<DBShardGroup>" + renderRDSShardGroupInner(g) + "</DBShardGroup>"
}

func handleRDSCreateShardGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBShardGroupIdentifier")
	clusterID := r.FormValue("DBClusterIdentifier")
	if id == "" || clusterID == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBShardGroupIdentifier and DBClusterIdentifier are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsShardGroups.Get(id); ok {
		rdsErrorXML(w, "DBShardGroupAlreadyExistsFault",
			fmt.Sprintf("DBShardGroup %q already exists", id),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	maxACU := rdsParseFloat(r.FormValue("MaxACU"))
	minACU := rdsParseFloat(r.FormValue("MinACU"))
	g := RDSShardGroup{
		DBShardGroupIdentifier: id,
		DBClusterIdentifier:    clusterID,
		DBShardGroupResourceId: "shardgroup-" + strings.ToLower(strings.ReplaceAll(generateUUID(), "-", ""))[:17],
		ComputeRedundancy:      atoiOrZero(r.FormValue("ComputeRedundancy")),
		MaxACU:                 maxACU,
		MinACU:                 minACU,
		PubliclyAccessible:     r.FormValue("PubliclyAccessible") == "true",
		Endpoint:               fmt.Sprintf("%s.shardgrp-sim.%s.rds.amazonaws.com", id, awsRegion()),
		Status:                 "available",
		ARN:                    fmt.Sprintf("arn:aws:rds:%s:%s:shard-group:%s", awsRegion(), awsAccountID(), id),
		Tags:                   parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsShardGroups.Put(id, g)
	rdsXMLResponse(w, "CreateDBShardGroup", renderRDSShardGroupInner(g), sim.RequestID(r.Context()))
}

func handleRDSDescribeShardGroups(w http.ResponseWriter, r *http.Request) {
	wantID := r.FormValue("DBShardGroupIdentifier")
	var b strings.Builder
	b.WriteString("<DBShardGroups>")
	for _, g := range rdsShardGroups.List() {
		if wantID != "" && g.DBShardGroupIdentifier != wantID {
			continue
		}
		b.WriteString(renderRDSShardGroup(g))
	}
	b.WriteString("</DBShardGroups>")
	rdsXMLResponse(w, "DescribeDBShardGroups", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyShardGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBShardGroupIdentifier")
	_, ok := rdsShardGroups.Get(id)
	if !ok {
		rdsErrorXML(w, "DBShardGroupNotFoundFault",
			fmt.Sprintf("DBShardGroup %q not found", id),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsShardGroups.Update(id, func(x *RDSShardGroup) {
		if v := r.FormValue("MaxACU"); v != "" {
			x.MaxACU = rdsParseFloat(v)
		}
		if v := r.FormValue("MinACU"); v != "" {
			x.MinACU = rdsParseFloat(v)
		}
		if v := r.FormValue("ComputeRedundancy"); v != "" {
			x.ComputeRedundancy = atoiOrZero(v)
		}
	})
	g, _ := rdsShardGroups.Get(id)
	rdsXMLResponse(w, "ModifyDBShardGroup", renderRDSShardGroupInner(g), sim.RequestID(r.Context()))
}

func handleRDSDeleteShardGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBShardGroupIdentifier")
	g, ok := rdsShardGroups.Get(id)
	if !ok {
		rdsErrorXML(w, "DBShardGroupNotFoundFault",
			fmt.Sprintf("DBShardGroup %q not found", id),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsShardGroups.Delete(id)
	g.Status = "deleting"
	rdsXMLResponse(w, "DeleteDBShardGroup", renderRDSShardGroupInner(g), sim.RequestID(r.Context()))
}

func handleRDSRebootShardGroup(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBShardGroupIdentifier")
	g, ok := rdsShardGroups.Get(id)
	if !ok {
		rdsErrorXML(w, "DBShardGroupNotFoundFault",
			fmt.Sprintf("DBShardGroup %q not found", id),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsXMLResponse(w, "RebootDBShardGroup", renderRDSShardGroupInner(g), sim.RequestID(r.Context()))
}

// Database activity streams (per-cluster status toggle)

// rdsActivityStreamStatus holds the current activity-stream status for a
// cluster ARN: started | stopped. With no real Kinesis stream, the
// status simply toggles. Empty status == stopped/not-started.
var rdsActivityStreamStatus sim.Store[string]

func handleRDSStartActivityStream(w http.ResponseWriter, r *http.Request) {
	resourceArn := r.FormValue("ResourceArn")
	if resourceArn == "" {
		rdsErrorXML(w, "MissingParameter", "ResourceArn is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	mode := r.FormValue("Mode")
	if mode == "" {
		mode = "async"
	}
	kmsKeyID := r.FormValue("KmsKeyId")
	rdsActivityStreamStatus.Put(resourceArn, "starting")
	streamName := "aws-rds-das-" + strings.ToLower(strings.ReplaceAll(generateUUID(), "-", ""))[:17]
	var b strings.Builder
	fmt.Fprintf(&b, "<KmsKeyId>%s</KmsKeyId>", xmlEscape(kmsKeyID))
	fmt.Fprintf(&b, "<KinesisStreamName>%s</KinesisStreamName>", xmlEscape(streamName))
	fmt.Fprintf(&b, "<Status>starting</Status>")
	fmt.Fprintf(&b, "<Mode>%s</Mode>", xmlEscape(mode))
	fmt.Fprintf(&b, "<ApplyImmediately>%t</ApplyImmediately>", r.FormValue("ApplyImmediately") == "true")
	rdsXMLResponse(w, "StartActivityStream", b.String(), sim.RequestID(r.Context()))
}

func handleRDSStopActivityStream(w http.ResponseWriter, r *http.Request) {
	resourceArn := r.FormValue("ResourceArn")
	if resourceArn == "" {
		rdsErrorXML(w, "MissingParameter", "ResourceArn is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	rdsActivityStreamStatus.Put(resourceArn, "stopped")
	var b strings.Builder
	b.WriteString("<KmsKeyId></KmsKeyId>")
	b.WriteString("<KinesisStreamName></KinesisStreamName>")
	b.WriteString("<Status>stopping</Status>")
	rdsXMLResponse(w, "StopActivityStream", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyActivityStream(w http.ResponseWriter, r *http.Request) {
	resourceArn := r.FormValue("ResourceArn")
	mode := r.FormValue("AuditPolicyState")
	status, _ := rdsActivityStreamStatus.Get(resourceArn)
	if status == "" {
		status = "stopped"
	}
	var b strings.Builder
	b.WriteString("<KmsKeyId></KmsKeyId>")
	b.WriteString("<KinesisStreamName></KinesisStreamName>")
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(status))
	b.WriteString("<Mode>async</Mode>")
	if mode != "" {
		fmt.Fprintf(&b, "<PolicyStatus>%s</PolicyStatus>", xmlEscape(mode))
	}
	rdsXMLResponse(w, "ModifyActivityStream", b.String(), sim.RequestID(r.Context()))
}

// Aurora backtrack

// rdsClusterBacktracks holds recorded backtracks keyed by cluster
// identifier. Backtrack rewinds an Aurora MySQL cluster to a prior
// timestamp; the sim records the request as a completed backtrack row.
var rdsClusterBacktracks sim.Store[[]rdsBacktrack]

type rdsBacktrack struct {
	BacktrackIdentifier string
	DBClusterIdentifier string
	BacktrackTo         string
	BacktrackedFrom     string
	CreationTime        string
	Status              string
}

// renderRDSBacktrackInner emits DBClusterBacktrack member fields with no
// wrapping element — BacktrackDBCluster binds its output to the
// DBClusterBacktrack shape directly; DescribeDBClusterBacktracks wraps
// each in the <DBClusterBacktrack> element.
func renderRDSBacktrackInner(bt rdsBacktrack) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<DBClusterIdentifier>%s</DBClusterIdentifier>", xmlEscape(bt.DBClusterIdentifier))
	fmt.Fprintf(&b, "<BacktrackIdentifier>%s</BacktrackIdentifier>", xmlEscape(bt.BacktrackIdentifier))
	fmt.Fprintf(&b, "<BacktrackTo>%s</BacktrackTo>", xmlEscape(bt.BacktrackTo))
	fmt.Fprintf(&b, "<BacktrackedFrom>%s</BacktrackedFrom>", xmlEscape(bt.BacktrackedFrom))
	fmt.Fprintf(&b, "<BacktrackRequestCreationTime>%s</BacktrackRequestCreationTime>", xmlEscape(bt.CreationTime))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(bt.Status))
	return b.String()
}

func renderRDSBacktrack(bt rdsBacktrack) string {
	return "<DBClusterBacktrack>" + renderRDSBacktrackInner(bt) + "</DBClusterBacktrack>"
}

func handleRDSBacktrackCluster(w http.ResponseWriter, r *http.Request) {
	clusterID := r.FormValue("DBClusterIdentifier")
	backtrackTo := r.FormValue("BacktrackTo")
	if clusterID == "" || backtrackTo == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBClusterIdentifier and BacktrackTo are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsClusters.Get(clusterID); !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault",
			fmt.Sprintf("DBCluster %q not found", clusterID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	bt := rdsBacktrack{
		BacktrackIdentifier: generateUUID(),
		DBClusterIdentifier: clusterID,
		BacktrackTo:         backtrackTo,
		BacktrackedFrom:     now,
		CreationTime:        now,
		Status:              "COMPLETED",
	}
	rdsClusterBacktracks.Upsert(clusterID, func(backtracks *[]rdsBacktrack) {
		*backtracks = append(*backtracks, bt)
	})
	rdsXMLResponse(w, "BacktrackDBCluster", renderRDSBacktrackInner(bt), sim.RequestID(r.Context()))
}

func handleRDSDescribeClusterBacktracks(w http.ResponseWriter, r *http.Request) {
	clusterID := r.FormValue("DBClusterIdentifier")
	if clusterID == "" {
		rdsErrorXML(w, "MissingParameter", "DBClusterIdentifier is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsClusters.Get(clusterID); !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault",
			fmt.Sprintf("DBCluster %q not found", clusterID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	var b strings.Builder
	b.WriteString("<DBClusterBacktracks>")
	backtracks, _ := rdsClusterBacktracks.Get(clusterID)
	for _, bt := range backtracks {
		b.WriteString(renderRDSBacktrack(bt))
	}
	b.WriteString("</DBClusterBacktracks>")
	rdsXMLResponse(w, "DescribeDBClusterBacktracks", b.String(), sim.RequestID(r.Context()))
}

// Snapshot-export tasks

func renderRDSExportTask(t RDSExportTask) string {
	var b strings.Builder
	b.WriteString("<ExportTaskIdentifier>")
	b.WriteString(xmlEscape(t.ExportTaskIdentifier))
	b.WriteString("</ExportTaskIdentifier>")
	fmt.Fprintf(&b, "<SourceArn>%s</SourceArn>", xmlEscape(t.SourceArn))
	b.WriteString("<ExportOnly>")
	for _, e := range t.ExportOnly {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(e))
	}
	b.WriteString("</ExportOnly>")
	fmt.Fprintf(&b, "<SnapshotTime>%s</SnapshotTime>", xmlEscape(t.SnapshotTime))
	fmt.Fprintf(&b, "<TaskStartTime>%s</TaskStartTime>", xmlEscape(t.TaskStartTime))
	fmt.Fprintf(&b, "<TaskEndTime>%s</TaskEndTime>", xmlEscape(t.TaskEndTime))
	fmt.Fprintf(&b, "<S3Bucket>%s</S3Bucket>", xmlEscape(t.S3Bucket))
	fmt.Fprintf(&b, "<S3Prefix>%s</S3Prefix>", xmlEscape(t.S3Prefix))
	fmt.Fprintf(&b, "<IamRoleArn>%s</IamRoleArn>", xmlEscape(t.IamRoleArn))
	fmt.Fprintf(&b, "<KmsKeyId>%s</KmsKeyId>", xmlEscape(t.KmsKeyId))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(t.Status))
	fmt.Fprintf(&b, "<PercentProgress>%d</PercentProgress>", t.PercentProgress)
	fmt.Fprintf(&b, "<SourceType>%s</SourceType>", xmlEscape(t.SourceType))
	return b.String()
}

func handleRDSStartExportTask(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ExportTaskIdentifier")
	source := r.FormValue("SourceArn")
	bucket := r.FormValue("S3BucketName")
	if id == "" || source == "" || bucket == "" {
		rdsErrorXML(w, "MissingParameter",
			"ExportTaskIdentifier, SourceArn and S3BucketName are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsExportTasks.Get(id); ok {
		rdsErrorXML(w, "ExportTaskAlreadyExistsFault",
			fmt.Sprintf("ExportTask %q already exists", id),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	sourceType := "SNAPSHOT"
	if strings.Contains(source, ":cluster:") {
		sourceType = "CLUSTER"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	t := RDSExportTask{
		ExportTaskIdentifier: id,
		SourceArn:            source,
		S3Bucket:             bucket,
		S3Prefix:             r.FormValue("S3Prefix"),
		IamRoleArn:           r.FormValue("IamRoleArn"),
		KmsKeyId:             r.FormValue("KmsKeyId"),
		Status:               "COMPLETE",
		PercentProgress:      100,
		SnapshotTime:         now,
		TaskStartTime:        now,
		TaskEndTime:          now,
		SourceType:           sourceType,
		ExportOnly:           rdsParseExportOnly(r),
	}
	rdsExportTasks.Put(id, t)
	rdsXMLResponse(w, "StartExportTask", renderRDSExportTask(t), sim.RequestID(r.Context()))
}

func rdsParseExportOnly(r *http.Request) []string {
	var out []string
	for n := 1; n <= 50; n++ {
		v := r.FormValue(fmt.Sprintf("ExportOnly.member.%d", n))
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}

func handleRDSDescribeExportTasks(w http.ResponseWriter, r *http.Request) {
	wantID := r.FormValue("ExportTaskIdentifier")
	wantSource := r.FormValue("SourceArn")
	var b strings.Builder
	b.WriteString("<ExportTasks>")
	for _, t := range rdsExportTasks.List() {
		if wantID != "" && t.ExportTaskIdentifier != wantID {
			continue
		}
		if wantSource != "" && t.SourceArn != wantSource {
			continue
		}
		b.WriteString("<ExportTask>")
		b.WriteString(renderRDSExportTask(t))
		b.WriteString("</ExportTask>")
	}
	b.WriteString("</ExportTasks>")
	rdsXMLResponse(w, "DescribeExportTasks", b.String(), sim.RequestID(r.Context()))
}

func handleRDSCancelExportTask(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ExportTaskIdentifier")
	_, ok := rdsExportTasks.Get(id)
	if !ok {
		rdsErrorXML(w, "ExportTaskNotFoundFault",
			fmt.Sprintf("ExportTask %q not found", id),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsExportTasks.Update(id, func(x *RDSExportTask) { x.Status = "CANCELING" })
	t, _ := rdsExportTasks.Get(id)
	rdsXMLResponse(w, "CancelExportTask", renderRDSExportTask(t), sim.RequestID(r.Context()))
}

// Cluster operations

func handleRDSRebootCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	cl, ok := rdsClusters.Get(id)
	if !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault", "DB cluster not found",
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsXMLResponse(w, "RebootDBCluster", renderRDSCluster(cl), sim.RequestID(r.Context()))
}

func handleRDSResetClusterParameterGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("DBClusterParameterGroupName")
	g, ok := rdsClusterParamGroups.Get(name)
	if !ok {
		rdsErrorXML(w, "DBParameterGroupNotFound",
			fmt.Sprintf("DBClusterParameterGroup %q not found", name),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	// Reset clears user overrides (back to engine defaults). When
	// ResetAllParameters is false, only the named parameters are cleared.
	rdsClusterParamGroups.Update(name, func(x *RDSClusterParamGroup) {
		if r.FormValue("ResetAllParameters") == "true" {
			x.Parameters = map[string]string{}
			return
		}
		for n := 1; n <= 50; n++ {
			pn := r.FormValue(fmt.Sprintf("Parameters.Parameter.%d.ParameterName", n))
			if pn == "" {
				break
			}
			delete(x.Parameters, pn)
		}
	})
	_ = g
	rdsXMLResponse(w, "ResetDBClusterParameterGroup",
		fmt.Sprintf("<DBClusterParameterGroupName>%s</DBClusterParameterGroupName>", xmlEscape(name)),
		sim.RequestID(r.Context()))
}

func handleRDSModifyClusterEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterEndpointIdentifier")
	_, ok := rdsClusterEndpoints.Get(id)
	if !ok {
		rdsErrorXML(w, "DBClusterEndpointNotFoundFault",
			fmt.Sprintf("DBClusterEndpoint %q not found", id),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsClusterEndpoints.Update(id, func(x *RDSClusterEndpoint) {
		if v := r.FormValue("EndpointType"); v != "" {
			x.CustomEndpointType = v
		}
		x.StaticMembers = rdsParseEndpointMembers(r, "StaticMembers")
		x.ExcludedMembers = rdsParseEndpointMembers(r, "ExcludedMembers")
	})
	e, _ := rdsClusterEndpoints.Get(id)
	rdsXMLResponse(w, "ModifyDBClusterEndpoint", renderRDSClusterEndpoint(e), sim.RequestID(r.Context()))
}

func rdsParseEndpointMembers(r *http.Request, field string) []string {
	var out []string
	for n := 1; n <= 50; n++ {
		v := r.FormValue(fmt.Sprintf("%s.member.%d", field, n))
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}

func handleRDSFailoverGlobalCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("GlobalClusterIdentifier")
	g, ok := rdsGlobalClusters.Get(id)
	if !ok {
		rdsErrorXML(w, "GlobalClusterNotFoundFault",
			fmt.Sprintf("GlobalCluster %q not found", id),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	target := r.FormValue("TargetDbClusterIdentifier")
	// Failover promotes the target cluster to writer: move it to the head
	// of the member list (index 0 == writer in renderRDSGlobalCluster).
	if target != "" {
		rdsGlobalClusters.Update(id, func(x *RDSGlobalCluster) {
			var reordered []string
			reordered = append(reordered, target)
			for _, m := range x.Members {
				if m != target {
					reordered = append(reordered, m)
				}
			}
			x.Members = reordered
		})
		g, _ = rdsGlobalClusters.Get(id)
	}
	rdsXMLResponse(w, "FailoverGlobalCluster", renderRDSGlobalCluster(g, "GlobalCluster"), sim.RequestID(r.Context()))
}

func handleRDSRemoveFromGlobalCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("GlobalClusterIdentifier")
	_, ok := rdsGlobalClusters.Get(id)
	if !ok {
		rdsErrorXML(w, "GlobalClusterNotFoundFault",
			fmt.Sprintf("GlobalCluster %q not found", id),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	dbClusterArn := r.FormValue("DbClusterIdentifier")
	rdsGlobalClusters.Update(id, func(x *RDSGlobalCluster) {
		var kept []string
		for _, m := range x.Members {
			if m != dbClusterArn {
				kept = append(kept, m)
			}
		}
		x.Members = kept
	})
	g, _ := rdsGlobalClusters.Get(id)
	rdsXMLResponse(w, "RemoveFromGlobalCluster", renderRDSGlobalCluster(g, "GlobalCluster"), sim.RequestID(r.Context()))
}

func handleRDSPromoteReadReplicaCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	cl, ok := rdsClusters.Get(id)
	if !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault",
			fmt.Sprintf("DBCluster %q not found", id),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsXMLResponse(w, "PromoteReadReplicaDBCluster", renderRDSCluster(cl), sim.RequestID(r.Context()))
}

func handleRDSEnableHTTPEndpoint(w http.ResponseWriter, r *http.Request) {
	resourceArn := r.FormValue("ResourceArn")
	if resourceArn == "" {
		rdsErrorXML(w, "MissingParameter", "ResourceArn is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	rdsHTTPEndpointEnabled.Put(resourceArn, true)
	body := fmt.Sprintf("<ResourceArn>%s</ResourceArn><HttpEndpointEnabled>true</HttpEndpointEnabled>", xmlEscape(resourceArn))
	rdsXMLResponse(w, "EnableHttpEndpoint", body, sim.RequestID(r.Context()))
}

func handleRDSDisableHTTPEndpoint(w http.ResponseWriter, r *http.Request) {
	resourceArn := r.FormValue("ResourceArn")
	if resourceArn == "" {
		rdsErrorXML(w, "MissingParameter", "ResourceArn is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	rdsHTTPEndpointEnabled.Put(resourceArn, false)
	body := fmt.Sprintf("<ResourceArn>%s</ResourceArn><HttpEndpointEnabled>false</HttpEndpointEnabled>", xmlEscape(resourceArn))
	rdsXMLResponse(w, "DisableHttpEndpoint", body, sim.RequestID(r.Context()))
}

// rdsHTTPEndpointEnabled tracks the RDS Data API (Aurora Serverless v1
// HTTP endpoint) enablement per resource ARN.
var rdsHTTPEndpointEnabled sim.Store[bool]

// Cluster-snapshot attributes and ModifyDBSnapshot

// rdsClusterSnapshotAttrs holds the "restore" attribute values granted
// per cluster-snapshot identifier.
var rdsClusterSnapshotAttrs sim.Store[[]string]

func renderRDSClusterSnapshotAttributesResult(snapID string) string {
	var b strings.Builder
	b.WriteString("<DBClusterSnapshotAttributesResult>")
	fmt.Fprintf(&b, "<DBClusterSnapshotIdentifier>%s</DBClusterSnapshotIdentifier>", xmlEscape(snapID))
	b.WriteString("<DBClusterSnapshotAttributes>")
	b.WriteString("<DBClusterSnapshotAttribute><AttributeName>restore</AttributeName><AttributeValues>")
	attrs, _ := rdsClusterSnapshotAttrs.Get(snapID)
	for _, v := range attrs {
		fmt.Fprintf(&b, "<AttributeValue>%s</AttributeValue>", xmlEscape(v))
	}
	b.WriteString("</AttributeValues></DBClusterSnapshotAttribute>")
	b.WriteString("</DBClusterSnapshotAttributes>")
	b.WriteString("</DBClusterSnapshotAttributesResult>")
	return b.String()
}

func handleRDSModifyClusterSnapshotAttribute(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("DBClusterSnapshotIdentifier")
	if _, ok := rdsClusterSnapshots.Get(snapID); !ok {
		if _, ok := findRDSClusterSnapshotByARN(snapID); !ok {
			rdsErrorXML(w, "DBClusterSnapshotNotFoundFault",
				fmt.Sprintf("DBClusterSnapshot %q not found", snapID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	if r.FormValue("AttributeName") != "restore" {
		rdsErrorXML(w, "InvalidParameterValue", "AttributeName must be 'restore'",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	cur, _ := rdsClusterSnapshotAttrs.Get(snapID)
	for n := 1; n <= 50; n++ {
		v := r.FormValue(fmt.Sprintf("ValuesToAdd.AttributeValue.%d", n))
		if v == "" {
			break
		}
		if !rdsContains(cur, v) {
			cur = append(cur, v)
		}
	}
	for n := 1; n <= 50; n++ {
		v := r.FormValue(fmt.Sprintf("ValuesToRemove.AttributeValue.%d", n))
		if v == "" {
			break
		}
		cur = rdsRemove(cur, v)
	}
	rdsClusterSnapshotAttrs.Put(snapID, cur)
	rdsXMLResponse(w, "ModifyDBClusterSnapshotAttribute",
		renderRDSClusterSnapshotAttributesResult(snapID), sim.RequestID(r.Context()))
}

func handleRDSDescribeClusterSnapshotAttributes(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("DBClusterSnapshotIdentifier")
	if _, ok := rdsClusterSnapshots.Get(snapID); !ok {
		if _, ok := findRDSClusterSnapshotByARN(snapID); !ok {
			rdsErrorXML(w, "DBClusterSnapshotNotFoundFault",
				fmt.Sprintf("DBClusterSnapshot %q not found", snapID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	rdsXMLResponse(w, "DescribeDBClusterSnapshotAttributes",
		renderRDSClusterSnapshotAttributesResult(snapID), sim.RequestID(r.Context()))
}

func handleRDSModifySnapshot(w http.ResponseWriter, r *http.Request) {
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
	rdsSnapshots.Update(snap.DBSnapshotIdentifier, func(x *RDSSnapshot) {
		if v := r.FormValue("EngineVersion"); v != "" {
			x.EngineVersion = v
		}
	})
	snap, _ = rdsSnapshots.Get(snap.DBSnapshotIdentifier)
	rdsXMLResponse(w, "ModifyDBSnapshot", renderRDSSnapshot(snap), sim.RequestID(r.Context()))
}

// Static describes (real-shaped catalogs / engine defaults)

func handleRDSDescribeOptionGroupOptions(w http.ResponseWriter, r *http.Request) {
	engine := r.FormValue("EngineName")
	if engine == "" {
		engine = "mysql"
	}
	major := r.FormValue("MajorEngineVersion")
	if major == "" {
		major = "8.0"
	}
	var b strings.Builder
	b.WriteString("<OptionGroupOptions>")
	b.WriteString("<OptionGroupOption>")
	b.WriteString("<Name>MEMCACHED</Name>")
	b.WriteString("<Description>Innodb Memcached for MySQL</Description>")
	fmt.Fprintf(&b, "<EngineName>%s</EngineName>", xmlEscape(engine))
	fmt.Fprintf(&b, "<MajorEngineVersion>%s</MajorEngineVersion>", xmlEscape(major))
	b.WriteString("<MinimumRequiredMinorEngineVersion>1</MinimumRequiredMinorEngineVersion>")
	b.WriteString("<PortRequired>true</PortRequired>")
	b.WriteString("<DefaultPort>11211</DefaultPort>")
	b.WriteString("<Persistent>false</Persistent>")
	b.WriteString("<Permanent>false</Permanent>")
	b.WriteString("<RequiresAutoMinorEngineVersionUpgrade>false</RequiresAutoMinorEngineVersionUpgrade>")
	b.WriteString("<VpcOnly>false</VpcOnly>")
	b.WriteString("</OptionGroupOption>")
	b.WriteString("</OptionGroupOptions>")
	rdsXMLResponse(w, "DescribeOptionGroupOptions", b.String(), sim.RequestID(r.Context()))
}

// rdsEngineDefaultParams renders the canonical EngineDefaults body —
// a parameter family plus a small set of real default parameters.
func rdsEngineDefaultParams(family string, params [][2]string) string {
	var b strings.Builder
	b.WriteString("<EngineDefaults>")
	fmt.Fprintf(&b, "<DBParameterGroupFamily>%s</DBParameterGroupFamily>", xmlEscape(family))
	b.WriteString("<Parameters>")
	for _, p := range params {
		b.WriteString("<Parameter>")
		fmt.Fprintf(&b, "<ParameterName>%s</ParameterName>", xmlEscape(p[0]))
		fmt.Fprintf(&b, "<ParameterValue>%s</ParameterValue>", xmlEscape(p[1]))
		b.WriteString("<Source>engine-default</Source>")
		b.WriteString("<ApplyType>dynamic</ApplyType>")
		b.WriteString("<DataType>integer</DataType>")
		b.WriteString("<IsModifiable>true</IsModifiable>")
		b.WriteString("</Parameter>")
	}
	b.WriteString("</Parameters>")
	b.WriteString("</EngineDefaults>")
	return b.String()
}

func handleRDSDescribeEngineDefaultParameters(w http.ResponseWriter, r *http.Request) {
	family := r.FormValue("DBParameterGroupFamily")
	if family == "" {
		family = "mysql8.0"
	}
	body := rdsEngineDefaultParams(family, [][2]string{
		{"max_connections", "{DBInstanceClassMemory/12582880}"},
		{"max_allowed_packet", "4194304"},
	})
	rdsXMLResponse(w, "DescribeEngineDefaultParameters", body, sim.RequestID(r.Context()))
}

func handleRDSDescribeEngineDefaultClusterParameters(w http.ResponseWriter, r *http.Request) {
	family := r.FormValue("DBParameterGroupFamily")
	if family == "" {
		family = "aurora-mysql8.0"
	}
	body := rdsEngineDefaultParams(family, [][2]string{
		{"aurora_lab_mode", "0"},
		{"server_audit_logging", "0"},
	})
	rdsXMLResponse(w, "DescribeEngineDefaultClusterParameters", body, sim.RequestID(r.Context()))
}

func handleRDSDescribeSourceRegions(w http.ResponseWriter, r *http.Request) {
	regions := []string{"us-east-1", "us-west-2", "eu-west-1", "ap-southeast-1"}
	var b strings.Builder
	b.WriteString("<SourceRegions>")
	for _, region := range regions {
		b.WriteString("<SourceRegion>")
		fmt.Fprintf(&b, "<RegionName>%s</RegionName>", xmlEscape(region))
		fmt.Fprintf(&b, "<Endpoint>https://rds.%s.amazonaws.com</Endpoint>", xmlEscape(region))
		b.WriteString("<Status>available</Status>")
		b.WriteString("<SupportsDBInstanceAutomatedBackupsReplication>true</SupportsDBInstanceAutomatedBackupsReplication>")
		b.WriteString("</SourceRegion>")
	}
	b.WriteString("</SourceRegions>")
	rdsXMLResponse(w, "DescribeSourceRegions", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDescribeDBMajorEngineVersions(w http.ResponseWriter, r *http.Request) {
	wantEngine := r.FormValue("Engine")
	type majorVer struct {
		engine string
		major  string
	}
	versions := []majorVer{
		{"mysql", "8.0"},
		{"postgres", "16"},
		{"aurora-mysql", "8.0"},
		{"aurora-postgresql", "16"},
	}
	var b strings.Builder
	b.WriteString("<DBMajorEngineVersions>")
	for _, v := range versions {
		if wantEngine != "" && v.engine != wantEngine {
			continue
		}
		b.WriteString("<DBMajorEngineVersion>")
		fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(v.engine))
		fmt.Fprintf(&b, "<MajorEngineVersion>%s</MajorEngineVersion>", xmlEscape(v.major))
		b.WriteString("<SupportedEngineLifecycles>")
		b.WriteString("<SupportedEngineLifecycle>")
		b.WriteString("<LifecycleSupportName>open-source-rds-standard-support</LifecycleSupportName>")
		b.WriteString("<LifecycleSupportStartDate>2024-01-01T00:00:00Z</LifecycleSupportStartDate>")
		b.WriteString("<LifecycleSupportEndDate>2027-01-01T00:00:00Z</LifecycleSupportEndDate>")
		b.WriteString("</SupportedEngineLifecycle>")
		b.WriteString("</SupportedEngineLifecycles>")
		b.WriteString("</DBMajorEngineVersion>")
	}
	b.WriteString("</DBMajorEngineVersions>")
	rdsXMLResponse(w, "DescribeDBMajorEngineVersions", b.String(), sim.RequestID(r.Context()))
}

// Small shared helpers

func rdsParseFloat(s string) float64 {
	var f float64
	if s == "" {
		return 0
	}
	if _, err := fmt.Sscanf(s, "%g", &f); err != nil {
		return 0
	}
	return f
}

func rdsContains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func rdsRemove(xs []string, v string) []string {
	var out []string
	for _, x := range xs {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
