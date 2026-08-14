package gcp_cli_test

import (
	"strings"
	"testing"
)

// TestSpannerCLI_BackupRestoreCrossPlane drives the whole backup lifecycle
// through the real gcloud CLI: a database is created with its schema, rows are
// written through the data plane, the database is backed up and copied, dropped,
// restored from the copy, and queried again. The restored query must return the
// rows the backup captured.
func TestSpannerCLI_BackupRestoreCrossPlane(t *testing.T) {
	const (
		instance = "cli-backup"
		database = "clibackupdb"
	)
	runCLI(t, gcloudCLI("spanner", "instances", "create", instance,
		"--config=regional-us-central1",
		"--description=CLI Spanner backups",
		"--nodes=1",
		"--format=json"))
	runCLI(t, gcloudCLI("spanner", "databases", "create", database,
		"--instance="+instance,
		"--ddl=CREATE TABLE Invoices (InvoiceId STRING(36) NOT NULL, Total INT64 NOT NULL) PRIMARY KEY (InvoiceId)",
		"--format=json"))

	ddl := runCLI(t, gcloudCLI("spanner", "databases", "ddl", "describe", database,
		"--instance="+instance))
	if !strings.Contains(ddl, "CREATE TABLE Invoices") {
		t.Fatalf("spanner databases ddl describe did not report the schema the create built: %s", ddl)
	}

	for _, sql := range []string{
		"INSERT INTO Invoices (InvoiceId, Total) VALUES ('inv-1', 1200)",
		"INSERT INTO Invoices (InvoiceId, Total) VALUES ('inv-2', 3400)",
	} {
		runCLI(t, gcloudCLI("spanner", "databases", "execute-sql", database,
			"--instance="+instance,
			"--sql="+sql,
			"--enable-partitioned-dml",
			"--format=json"))
	}

	runCLI(t, gcloudCLI("spanner", "backups", "create", "cli-bk",
		"--instance="+instance,
		"--database="+database,
		"--retention-period=2d",
		"--format=json"))

	backups := runCLI(t, gcloudCLI("spanner", "backups", "list", "--instance="+instance, "--format=value(name)"))
	if !strings.Contains(backups, "cli-bk") {
		t.Fatalf("spanner backups list missing cli-bk: %s", backups)
	}
	describe := runCLI(t, gcloudCLI("spanner", "backups", "describe", "cli-bk", "--instance="+instance, "--format=json"))
	if !strings.Contains(describe, "\"state\": \"READY\"") {
		t.Fatalf("spanner backups describe did not report a READY backup: %s", describe)
	}
	if strings.Contains(describe, "\"sizeBytes\": \"0\"") {
		t.Fatalf("the backup captured no bytes: %s", describe)
	}

	runCLI(t, gcloudCLI("spanner", "backups", "copy",
		"--source-backup=cli-bk",
		"--source-instance="+instance,
		"--destination-backup=cli-bk-copy",
		"--destination-instance="+instance,
		"--retention-period=2d",
		"--format=json"))

	runCLI(t, gcloudCLI("spanner", "databases", "delete", database, "--instance="+instance, "--quiet"))

	runCLI(t, gcloudCLI("spanner", "databases", "restore",
		"--source-backup=cli-bk-copy",
		"--source-instance="+instance,
		"--destination-database="+database,
		"--destination-instance="+instance,
		"--format=json"))

	restored := runCLI(t, gcloudCLI("spanner", "databases", "execute-sql", database,
		"--instance="+instance,
		"--sql=SELECT InvoiceId, Total FROM Invoices ORDER BY InvoiceId",
		"--format=json"))
	for _, want := range []string{"inv-1", "1200", "inv-2", "3400"} {
		if !strings.Contains(restored, want) {
			t.Fatalf("the restored database did not return %q: %s", want, restored)
		}
	}

	runCLI(t, gcloudCLI("spanner", "backups", "delete", "cli-bk", "--instance="+instance, "--quiet"))
	after := runCLI(t, gcloudCLI("spanner", "backups", "list", "--instance="+instance, "--format=value(name)"))
	if strings.Contains(after, "/cli-bk\n") {
		t.Fatalf("the deleted backup is still listed: %s", after)
	}
}

// TestSpannerCLI_AdminSurface drives the remaining administrative groups —
// backup schedules, instance partitions, database roles and operations — through
// gcloud.
func TestSpannerCLI_AdminSurface(t *testing.T) {
	const (
		instance = "cli-admin"
		database = "cliadmindb"
	)
	runCLI(t, gcloudCLI("spanner", "instances", "create", instance,
		"--config=regional-us-central1",
		"--description=CLI Spanner admin",
		"--nodes=1",
		"--format=json"))
	runCLI(t, gcloudCLI("spanner", "databases", "create", database,
		"--instance="+instance,
		"--ddl=CREATE TABLE Notes (NoteId STRING(36) NOT NULL, Body STRING(MAX)) PRIMARY KEY (NoteId)",
		"--format=json"))

	runCLI(t, gcloudCLI("spanner", "backup-schedules", "create", "cli-nightly",
		"--instance="+instance,
		"--database="+database,
		"--cron=0 2 * * *",
		"--retention-duration=2d",
		"--backup-type=full-backup",
		"--format=json"))
	schedules := runCLI(t, gcloudCLI("spanner", "backup-schedules", "list",
		"--instance="+instance, "--database="+database, "--format=value(name)"))
	if !strings.Contains(schedules, "cli-nightly") {
		t.Fatalf("spanner backup-schedules list missing cli-nightly: %s", schedules)
	}
	runCLI(t, gcloudCLI("spanner", "backup-schedules", "describe", "cli-nightly",
		"--instance="+instance, "--database="+database, "--format=json"))
	runCLI(t, gcloudCLI("spanner", "backup-schedules", "delete", "cli-nightly",
		"--instance="+instance, "--database="+database, "--quiet"))

	runCLI(t, gcloudCLI("spanner", "instance-partitions", "create", "cli-shard",
		"--instance="+instance,
		"--config=regional-us-east1",
		"--description=CLI shard",
		"--nodes=1",
		"--format=json"))
	partitions := runCLI(t, gcloudCLI("spanner", "instance-partitions", "list",
		"--instance="+instance, "--format=value(name)"))
	if !strings.Contains(partitions, "cli-shard") {
		t.Fatalf("spanner instance-partitions list missing cli-shard: %s", partitions)
	}
	runCLI(t, gcloudCLI("spanner", "instance-partitions", "describe", "cli-shard",
		"--instance="+instance, "--format=json"))
	runCLI(t, gcloudCLI("spanner", "instance-partitions", "delete", "cli-shard",
		"--instance="+instance, "--quiet"))

	// Database roles come out of the database's own fine-grained access-control
	// DDL, so creating one through `ddl update` makes it appear in `roles list`.
	runCLI(t, gcloudCLI("spanner", "databases", "ddl", "update", database,
		"--instance="+instance,
		"--ddl=CREATE ROLE cli_auditor",
		"--format=json"))
	roles := runCLI(t, gcloudCLI("spanner", "databases", "roles", "list",
		"--database="+database, "--instance="+instance, "--format=value(name)"))
	if !strings.Contains(roles, "cli_auditor") || !strings.Contains(roles, "public") {
		t.Fatalf("spanner databases roles list did not report the DDL-defined roles: %s", roles)
	}

	operations := runCLI(t, gcloudCLI("spanner", "operations", "list",
		"--instance="+instance, "--format=value(name)"))
	if strings.TrimSpace(operations) == "" {
		t.Fatalf("spanner operations list reported no instance operations")
	}
}
