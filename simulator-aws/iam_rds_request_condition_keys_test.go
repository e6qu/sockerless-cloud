package main

import (
	"net/url"
	"testing"
)

func TestRDSConditionKeysReadTheRequest(t *testing.T) {
	cases := []struct {
		name      string
		operation string
		form      url.Values
		want      map[string][]string
	}{
		{"a cluster", "CreateDBCluster", url.Values{
			"DBClusterIdentifier":    {"c"},
			"Engine":                 {"mysql"},
			"DatabaseName":           {"orders"},
			"DBClusterInstanceClass": {"db.m6gd.large"},
			"AllocatedStorage":       {"100"},
			"Iops":                   {"3000"},
			"StorageEncrypted":       {"true"},
		}, map[string][]string{
			"rds:DatabaseEngine":   {"mysql"},
			"rds:DatabaseName":     {"orders"},
			"rds:DatabaseClass":    {"db.m6gd.large"},
			"rds:StorageSize":      {"100"},
			"rds:Piops":            {"3000"},
			"rds:StorageEncrypted": {"true"},
		}},
		{"a cluster without Provisioned IOPS", "CreateDBCluster", url.Values{
			"DBClusterIdentifier": {"c"},
			"Engine":              {"aurora-postgresql"},
		}, map[string][]string{
			"rds:DatabaseEngine": {"aurora-postgresql"},
			"rds:Piops":          {"0"},
			"rds:DatabaseName":   nil,
			"rds:StorageSize":    nil,
		}},
		{"a cluster modification", "ModifyDBCluster", url.Values{
			"DBClusterIdentifier":    {"c"},
			"DBClusterInstanceClass": {"db.r6gd.xlarge"},
			"AllocatedStorage":       {"200"},
		}, map[string][]string{
			"rds:DatabaseClass":  {"db.r6gd.xlarge"},
			"rds:StorageSize":    {"200"},
			"rds:Piops":          nil,
			"rds:DatabaseEngine": nil,
		}},
		{"a point-in-time cluster restore", "RestoreDBClusterToPointInTime", url.Values{
			"DBClusterIdentifier":    {"c2"},
			"DBClusterInstanceClass": {"db.m6gd.large"},
			"Iops":                   {"1000"},
		}, map[string][]string{
			"rds:DatabaseClass": {"db.m6gd.large"},
			"rds:Piops":         {"1000"},
		}},
		{"a cluster restored from Amazon S3", "RestoreDBClusterFromS3", url.Values{
			"Engine":           {"aurora-mysql"},
			"DatabaseName":     {"orders"},
			"StorageEncrypted": {"false"},
		}, map[string][]string{
			"rds:DatabaseEngine":   {"aurora-mysql"},
			"rds:DatabaseName":     {"orders"},
			"rds:StorageEncrypted": {"false"},
		}},
		{"an instance's backup target", "CreateDBInstance", url.Values{"BackupTarget": {"outposts"}},
			map[string][]string{"rds:BackupTarget": {"outposts"}}},
		{"a snapshot copy", "CopyDBSnapshot", url.Values{"CopyOptionGroup": {"true"}},
			map[string][]string{"rds:CopyOptionGroup": {"true"}}},
		{"a tenant database", "CreateTenantDatabase", url.Values{"TenantDBName": {"tenant1"}},
			map[string][]string{"rds:TenantDatabaseName": {"tenant1"}}},
		{"a tenant database rename", "ModifyTenantDatabase", url.Values{"TenantDBName": {"tenant1"}, "NewTenantDBName": {"tenant2"}},
			map[string][]string{"rds:TenantDatabaseName": {"tenant2"}}},
		{"tags supplied in the request", "AddTagsToResource", url.Values{
			"ResourceName":   {"arn:aws:rds:us-east-1:123456789012:db:d"},
			"Tags.Tag.1.Key": {"team"}, "Tags.Tag.1.Value": {"orders"},
		}, map[string][]string{"rds:TagsFromRequest": {"true"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := queryServiceConditionContext(t, "rds", c.operation, c.form)
			assertServiceConditionContext(t, ctx, c.want)
		})
	}
}

func TestRDSConditionKeysAreAbsentForAbsentMembers(t *testing.T) {
	ctx := queryServiceConditionContext(t, "rds", "RestoreDBClusterFromSnapshot", url.Values{
		"DBClusterIdentifier": {"c"},
		"SnapshotIdentifier":  {"s"},
		"Engine":              {"mysql"},
	})
	if len(ctx) != 0 {
		t.Errorf("a restore naming no class or IOPS settled %v", ctx)
	}
	// CreateDBInstance declares none of the cluster keys.
	ctx = queryServiceConditionContext(t, "rds", "CreateDBInstance", url.Values{
		"DBInstanceIdentifier": {"d"},
		"Engine":               {"postgres"},
		"DBInstanceClass":      {"db.t4g.micro"},
		"Iops":                 {"3000"},
	})
	if len(ctx) != 0 {
		t.Errorf("CreateDBInstance settled %v", ctx)
	}
}
