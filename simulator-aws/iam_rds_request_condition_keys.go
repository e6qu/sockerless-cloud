package main

import "net/http"

// Amazon RDS's request condition keys: the instance classes, engines,
// storage and backup placement a request asks for, which a policy holds to
// an approved set.

func init() {
	registerIAMRequestConditionPopulator("rds", iamPopulateRDSRequestConditionKeys)
}

func iamPopulateRDSRequestConditionKeys(r *http.Request, operation string, _ []byte, ctx map[string][]string) {
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
