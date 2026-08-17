package gcp_cli_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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

	require.Contains(t, spannerListNames(t, "spanner", "backups", "list", "--instance="+instance),
		spannerBackupName(instance, "cli-bk"), "spanner backups list missing cli-bk")
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
	// Matching whole resource names rather than the raw listing text keeps the
	// check independent of line endings, and keeps "cli-bk" from matching the
	// surviving "cli-bk-copy".
	backupNames := spannerListNames(t, "spanner", "backups", "list", "--instance="+instance)
	if slices.Contains(backupNames, spannerBackupName(instance, "cli-bk")) {
		t.Fatalf("the deleted backup is still listed: %v", backupNames)
	}
	if !slices.Contains(backupNames, spannerBackupName(instance, "cli-bk-copy")) {
		t.Fatalf("the delete removed more than the backup it named: %v", backupNames)
	}
}

// spannerBackupName is the resource name of a backup in an instance of the
// suite's project.
func spannerBackupName(instance, backup string) string {
	return fmt.Sprintf("projects/%s/instances/%s/backups/%s", project, instance, backup)
}

// spannerListNames runs a `gcloud spanner ... list` and returns the resource
// names it reported. The listing is read as JSON rather than
// `--format=value(name)`: gcloud renders a Spanner list's name column as the
// bare resource id, which cannot distinguish one instance's backup from
// another's, while the JSON carries the fully-qualified name the API returned.
func spannerListNames(t *testing.T, args ...string) []string {
	t.Helper()
	var listed []struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, gcloudCLI(append(args, "--format=json")...)), &listed)
	names := make([]string, 0, len(listed))
	for _, item := range listed {
		names = append(names, item.Name)
	}
	return names
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
	scheduleName := fmt.Sprintf("projects/%s/instances/%s/databases/%s/backupSchedules/cli-nightly",
		project, instance, database)
	require.Contains(t, spannerListNames(t, "spanner", "backup-schedules", "list",
		"--instance="+instance, "--database="+database),
		scheduleName, "spanner backup-schedules list missing cli-nightly")

	// The described schedule carries the crontab and retention window the
	// create was given — a schedule that stored neither would list just as
	// happily.
	var schedule struct {
		Name string `json:"name"`
		Spec struct {
			CronSpec struct {
				Text string `json:"text"`
			} `json:"cronSpec"`
		} `json:"spec"`
		RetentionDuration string `json:"retentionDuration"`
	}
	parseJSONObject(t, runCLI(t, gcloudCLI("spanner", "backup-schedules", "describe", "cli-nightly",
		"--instance="+instance, "--database="+database, "--format=json")), &schedule)
	require.Equal(t, scheduleName, schedule.Name)
	require.Equal(t, "0 2 * * *", schedule.Spec.CronSpec.Text)
	// gcloud renders --retention-duration=2d as the protobuf Duration the API
	// declares.
	require.Equal(t, "172800s", schedule.RetentionDuration)

	runCLI(t, gcloudCLI("spanner", "backup-schedules", "delete", "cli-nightly",
		"--instance="+instance, "--database="+database, "--quiet"))
	require.NotContains(t,
		spannerListNames(t, "spanner", "backup-schedules", "list",
			"--instance="+instance, "--database="+database),
		scheduleName, "the deleted backup schedule is still listed")

	runCLI(t, gcloudCLI("spanner", "instance-partitions", "create", "cli-shard",
		"--instance="+instance,
		"--config=regional-us-east1",
		"--description=CLI shard",
		"--nodes=1",
		"--format=json"))
	partitionName := fmt.Sprintf("projects/%s/instances/%s/instancePartitions/cli-shard", project, instance)
	require.Contains(t, spannerListNames(t, "spanner", "instance-partitions", "list",
		"--instance="+instance),
		partitionName, "spanner instance-partitions list missing cli-shard")

	// The described partition carries the configuration, node count and
	// description the create asked for.
	var partition struct {
		Name        string `json:"name"`
		Config      string `json:"config"`
		DisplayName string `json:"displayName"`
		NodeCount   int    `json:"nodeCount"`
		State       string `json:"state"`
	}
	parseJSONObject(t, runCLI(t, gcloudCLI("spanner", "instance-partitions", "describe", "cli-shard",
		"--instance="+instance, "--format=json")), &partition)
	require.Equal(t, partitionName, partition.Name)
	require.True(t, strings.HasSuffix(partition.Config, "/instanceConfigs/regional-us-east1"),
		"the partition sits in the configuration it was created in: %q", partition.Config)
	require.Equal(t, "CLI shard", partition.DisplayName)
	require.Equal(t, 1, partition.NodeCount)
	require.Equal(t, "READY", partition.State)

	runCLI(t, gcloudCLI("spanner", "instance-partitions", "delete", "cli-shard",
		"--instance="+instance, "--quiet"))
	require.NotContains(t,
		spannerListNames(t, "spanner", "instance-partitions", "list", "--instance="+instance),
		partitionName, "the deleted instance partition is still listed")

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

	// The instance's own operation collection holds the operations recorded
	// against this instance and nothing else: the simulator records every
	// service's long-running operations in one store, so a listing that did
	// not scope to the instance would also carry operations from the other
	// tests in this suite.
	operations := spannerListNames(t, "spanner", "operations", "list", "--instance="+instance)
	require.NotEmpty(t, operations, "spanner operations list reported no instance operations")
	prefix := fmt.Sprintf("projects/%s/instances/%s/operations/", project, instance)
	for _, name := range operations {
		require.True(t, strings.HasPrefix(name, prefix),
			"the instance's operation collection must hold only its own operations; got %q", name)
	}
}
