package main

import (
	"net/http"
	"strconv"
	"strings"
)

// Amazon RDS's request condition keys: the instance classes, engines,
// storage and backup placement a request asks for, which a policy holds to
// an approved set, plus rds:req-tag/${TagKey} — the tags the request carries.
//
// A request that copies a source — a blue/green deployment's database, a
// snapshot's instance, a restore's snapshot or cluster — settles the keys that
// describe the source from the source itself.
//
// rds:MultiAz and rds:StorageEncrypted describe a DB cluster source only: this
// simulator stores neither Multi-AZ placement nor storage encryption on a DB
// instance, so a DB instance source leaves both unset rather than invent them.

func init() {
	registerIAMRequestConditionPopulator("rds", iamPopulateRDSRequestConditionKeys)
}

func iamPopulateRDSRequestConditionKeys(r *http.Request, operation string, _ []byte, ctx map[string][]string) {
	// rds:req-tag/${TagKey} is how RDS spells the tags carried IN the request —
	// the key the reference declares on all 33 of its tag-on-create and tag
	// operations, and the only RDS spelling of that fact. RDS's awsQuery tag
	// member is Tags.Tag.N, not the Tag.N the aws:RequestTag reader looks for,
	// so a policy holding a create to a required tag had nothing to match.
	for _, t := range parseIndexedTags(r, "Tags.Tag") {
		ctx["rds:req-tag/"+t.Key] = []string{t.Value}
	}

	switch operation {
	case "CreateDBInstance", "RestoreDBInstanceFromDBSnapshot", "RestoreDBInstanceToPointInTime":
		ecSetString(ctx, "rds:BackupTarget", r.FormValue("BackupTarget"))
	case "CopyDBSnapshot":
		ecSetBool(ctx, "rds:CopyOptionGroup", r.FormValue("CopyOptionGroup"))
	case "CreateTenantDatabase":
		ecSetString(ctx, "rds:TenantDatabaseName", r.FormValue("TenantDBName"))
	case "ModifyTenantDatabase":
		ecSetString(ctx, "rds:TenantDatabaseName", r.FormValue("NewTenantDBName"))
	case "AddTagsToResource":
		if len(parseIndexedTags(r, "Tags.Tag")) > 0 {
			ctx["rds:TagsFromRequest"] = []string{"true"}
		}
	case "CreateDBClusterEndpoint":
		ecSetString(ctx, "rds:EndpointType", r.FormValue("EndpointType"))
	case "CreateBlueGreenDeployment":
		// The green environment's shape is the request's Target* members,
		// and the source's where the request leaves one out.
		ecSetString(ctx, "rds:DatabaseClass", r.FormValue("TargetDBInstanceClass"))
		ecSetInteger(ctx, "rds:Piops", r.FormValue("TargetIops"))
		ecSetInteger(ctx, "rds:StorageSize", r.FormValue("TargetAllocatedStorage"))
		iamSetRDSBlueGreenSourceKeys(ctx, r.FormValue("Source"))
		if group, ok := rdsParamGroups.Get(r.FormValue("TargetDBParameterGroupName")); ok {
			iamSetRDSTagKeys(ctx, "rds:pg-tag/", group.Tags)
		}
		if group, ok := rdsClusterParamGroups.Get(r.FormValue("TargetDBClusterParameterGroupName")); ok {
			iamSetRDSTagKeys(ctx, "rds:cluster-pg-tag/", group.Tags)
		}
	case "CreateDBSnapshot":
		if instance, ok := rdsInstances.Get(r.FormValue("DBInstanceIdentifier")); ok {
			ecSetString(ctx, "rds:BackupTarget", instance.BackupTarget)
		}
	case "RestoreDBClusterFromSnapshot":
		// A DB cluster is restored from a DB cluster snapshot or, for a
		// migration, from a DB snapshot.
		id := r.FormValue("SnapshotIdentifier")
		if snapshot, ok := findRDSClusterSnapshotByARN(id); ok {
			iamSetRDSStorageSize(ctx, snapshot.AllocatedStorage)
		} else if snapshot, ok := findRDSSnapshotByARN(id); ok {
			iamSetRDSStorageSize(ctx, snapshot.AllocatedStorage)
		}
	case "RestoreDBClusterToPointInTime":
		if cluster, ok := iamRDSPointInTimeSource(r); ok {
			iamSetRDSStorageSize(ctx, cluster.AllocatedStorage)
		}
	}

	switch operation {
	case "CreateDBCluster", "ModifyDBCluster", "RestoreDBClusterFromSnapshot", "RestoreDBClusterToPointInTime":
		ecSetString(ctx, "rds:DatabaseClass", r.FormValue("DBClusterInstanceClass"))
		ecSetInteger(ctx, "rds:Piops", r.FormValue("Iops"))
	}

	switch operation {
	case "CreateDBCluster", "ModifyDBCluster":
		ecSetInteger(ctx, "rds:StorageSize", r.FormValue("AllocatedStorage"))
	}

	switch operation {
	case "CreateDBCluster", "RestoreDBClusterFromS3":
		ecSetString(ctx, "rds:DatabaseEngine", r.FormValue("Engine"))
		ecSetString(ctx, "rds:DatabaseName", r.FormValue("DatabaseName"))
		ecSetBool(ctx, "rds:StorageEncrypted", r.FormValue("StorageEncrypted"))
	}

	// A cluster created without Iops has no Provisioned IOPS, which AWS
	// expresses as 0. A modify or restore that omits Iops keeps a value this
	// simulator does not record, so it settles nothing.
	if _, set := ctx["rds:Piops"]; operation == "CreateDBCluster" && !set {
		ctx["rds:Piops"] = []string{"0"}
	}
}

// iamSetRDSBlueGreenSourceKeys settles the keys that describe the database a
// blue/green deployment clones: a DB instance or a DB cluster, named by ARN.
// Every DB instance and DB cluster runs in a virtual private cloud.
func iamSetRDSBlueGreenSourceKeys(ctx map[string][]string, source string) {
	if !strings.HasPrefix(source, "arn:") {
		return
	}
	if instance, ok := findRDSByARN(source); ok {
		ecSetString(ctx, "rds:DatabaseEngine", instance.Engine)
		ecSetString(ctx, "rds:DatabaseName", instance.DBName)
		if _, set := ctx["rds:DatabaseClass"]; !set {
			ecSetString(ctx, "rds:DatabaseClass", instance.DBInstanceClass)
		}
		if _, set := ctx["rds:StorageSize"]; !set {
			iamSetRDSStorageSize(ctx, instance.AllocatedStorage)
		}
		ctx["rds:Vpc"] = []string{"true"}
		return
	}
	cluster, ok := findRDSClusterByARN(source)
	if !ok {
		return
	}
	ecSetString(ctx, "rds:DatabaseEngine", cluster.Engine)
	ecSetString(ctx, "rds:DatabaseName", cluster.DatabaseName)
	ctx["rds:StorageEncrypted"] = []string{strconv.FormatBool(cluster.StorageEncrypted)}
	ctx["rds:MultiAz"] = []string{strconv.FormatBool(rdsClusterSpansZones(cluster.DBClusterIdentifier))}
	if _, set := ctx["rds:StorageSize"]; !set {
		iamSetRDSStorageSize(ctx, cluster.AllocatedStorage)
	}
	ctx["rds:Vpc"] = []string{"true"}
}

// rdsClusterSpansZones reports whether a DB cluster has DB instances in more
// than one Availability Zone, which is what Amazon RDS reports as its MultiAZ.
func rdsClusterSpansZones(clusterID string) bool {
	zones := map[string]bool{}
	for _, instance := range rdsInstances.List() {
		if instance.DBClusterIdentifier == clusterID && instance.AvailabilityZone != "" {
			zones[instance.AvailabilityZone] = true
		}
	}
	return len(zones) > 1
}

// iamRDSPointInTimeSource is the DB cluster a point-in-time restore reads, by
// identifier, ARN or resource ID.
func iamRDSPointInTimeSource(r *http.Request) (RDSCluster, bool) {
	if id := r.FormValue("SourceDBClusterIdentifier"); id != "" {
		return findRDSClusterByARN(id)
	}
	if resourceID := r.FormValue("SourceDbClusterResourceId"); resourceID != "" {
		for _, cluster := range rdsClusters.List() {
			if cluster.DbClusterResourceId == resourceID {
				return cluster, true
			}
		}
	}
	return RDSCluster{}, false
}

func iamSetRDSStorageSize(ctx map[string][]string, gib int) {
	if gib > 0 {
		ctx["rds:StorageSize"] = []string{strconv.Itoa(gib)}
	}
}

func iamSetRDSTagKeys(ctx map[string][]string, prefix string, tags map[string]string) {
	for key, value := range tags {
		ctx[prefix+key] = []string{value}
	}
}
