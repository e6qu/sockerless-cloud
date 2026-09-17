package main

import "net/http"

// Amazon RDS's request condition keys: the instance classes, engines,
// storage and backup placement a request asks for, which a policy holds to
// an approved set, plus rds:req-tag/${TagKey} — the tags the request carries.
//
// rds:MultiAz is deliberately absent. The vendored reference declares it on the
// db resource and on the CreateBlueGreenDeployment action, i.e. as a property
// of the DB instance the request is about — not as a request parameter. This
// simulator neither stores MultiAZ on an RDSInstance nor renders it, so nothing
// in the request or in the targeted instance determines it; writing a value
// would be inventing one. It becomes populable the day the sim models
// multi-AZ placement.

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
