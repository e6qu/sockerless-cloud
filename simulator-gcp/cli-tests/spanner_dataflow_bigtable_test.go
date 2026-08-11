package gcp_cli_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSpannerCLI_InstanceDatabaseList(t *testing.T) {
	runCLI(t, gcloudCLI("spanner", "instances", "create", "cli-spanner",
		"--config=regional-us-central1",
		"--description=CLI Spanner",
		"--nodes=1",
		"--format=json"))
	runCLI(t, gcloudCLI("spanner", "databases", "create", "clidb",
		"--instance=cli-spanner",
		"--format=json"))

	out := runCLI(t, gcloudCLI("spanner", "databases", "list", "--instance=cli-spanner", "--format=value(name)"))
	if !strings.Contains(out, "clidb") {
		t.Fatalf("spanner databases list missing clidb: %s", out)
	}
}

func TestSpannerCLI_RESTDataPlane(t *testing.T) {
	const (
		instance = "cli-spanner-data"
		database = "clidatadb"
	)
	runCLI(t, gcloudCLI("spanner", "instances", "create", instance,
		"--config=regional-us-central1",
		"--description=CLI Spanner data plane",
		"--nodes=1",
		"--format=json"))
	runCLI(t, gcloudCLI("spanner", "databases", "create", database,
		"--instance="+instance,
		"--format=json"))
	runCLI(t, gcloudCLI("spanner", "databases", "ddl", "update", database,
		"--instance="+instance,
		"--ddl=CREATE TABLE Users (UserId STRING(36) NOT NULL, Name STRING(100) NOT NULL) PRIMARY KEY (UserId)",
		"--format=json"))

	runCLI(t, gcloudCLI("spanner", "databases", "execute-sql", database,
		"--instance="+instance,
		"--sql=INSERT INTO Users (UserId, Name) VALUES ('cli-1', 'CLI Alice')",
		"--enable-partitioned-dml",
		"--format=json"))
	out := runCLI(t, gcloudCLI("spanner", "databases", "execute-sql", database,
		"--instance="+instance,
		"--sql=SELECT UserId, Name FROM Users",
		"--format=json"))
	if !strings.Contains(out, "cli-1") || !strings.Contains(out, "CLI Alice") {
		t.Fatalf("Spanner execute-sql did not return the committed row: %s", out)
	}
}

func TestDataflowCLI_ListRegionalJobs(t *testing.T) {
	httpDoJSON(t, "POST", baseURL+"/v1b3/projects/"+project+"/locations/"+location+"/jobs",
		`{"name":"cli-dataflow-job","type":"JOB_TYPE_BATCH"}`)

	out := runCLI(t, gcloudCLI("dataflow", "jobs", "list", "--region="+location, "--format=value(name)"))
	if !strings.Contains(out, "cli-dataflow-job") {
		t.Fatalf("dataflow jobs list missing seeded job: %s", out)
	}
}

func TestBigtableCLI_InstanceTableList(t *testing.T) {
	runCLI(t, gcloudCLI("bigtable", "instances", "create", "cli-bt",
		"--display-name=CLI Bigtable",
		"--cluster=cli-bt-c1",
		"--cluster-zone=us-central1-a",
		"--cluster-num-nodes=1",
		"--instance-type=PRODUCTION",
		"--format=json"))
	runCLI(t, gcloudCLI("bigtable", "tables", "create", "cli_table",
		"--instance=cli-bt",
		"--column-families=cf",
		"--format=json"))

	out := runCLI(t, gcloudCLI("bigtable", "tables", "list", "--instances=cli-bt", "--format=value(name)"))
	if !strings.Contains(out, "cli_table") {
		t.Fatalf("bigtable tables list missing cli_table: %s", out)
	}
}

// TestBigtableCLI_DataPlane_cbt exercises the Bigtable Data API v2 gRPC slice
// through the canonical data-plane CLI `cbt` (a distinct adaptor from the Go
// SDK), reaching the sim via BIGTABLE_EMULATOR_HOST — the same coordinate the
// real client uses for the real emulator. Closes the Bigtable data-plane CLI
// gap: the data plane had no second-adaptor coverage at all (gcloud bigtable is
// admin-only; the data plane's CLI surface is cbt). cbt is built in TestMain
// (see installCBT) so the test runs unconditionally.
func TestBigtableCLI_DataPlane_cbt(t *testing.T) {
	const (
		instance = "cbt-dp"
		table    = "cbt_table"
		family   = "cf"
	)
	// Provision the instance via gcloud (admin) — cbt can create instances too,
	// but gcloud is the canonical admin CLI and already wired here.
	runCLI(t, gcloudCLI("bigtable", "instances", "create", instance,
		"--display-name=cbt data plane",
		"--cluster="+instance+"-c1",
		"--cluster-zone=us-central1-a",
		"--cluster-num-nodes=1",
		"--instance-type=PRODUCTION",
		"--format=json"))

	// cbt inherits BIGTABLE_EMULATOR_HOST from its env, which points it at the
	// sim's gRPC server (admin + data plane). -project and -instance are the
	// canonical cbt flags (positional instance args are not part of cbt's CLI).
	cbt := func(args ...string) string {
		cmd := exec.Command(cbtPath, append([]string{
			"-project=" + project, "-instance=" + instance,
		}, args...)...)
		cmd.Env = append(gcloudEnv(), "BIGTABLE_EMULATOR_HOST="+grpcAddr)
		return runCLI(t, cmd)
	}

	// Admin via cbt (exercising BigtableTableAdmin gRPC, not just gcloud):
	// createtable then createfamily.
	cbt("createtable", table)
	cbt("createfamily", table, family)

	// set: data-plane MutateRow. Value format is family:qualifier=value.
	cbt("set", table, "r1", family+":name=alice")
	cbt("set", table, "r2", family+":name=bob")

	// lookup: single-row ReadRow.
	got := cbt("lookup", table, "r1")
	if !strings.Contains(got, "alice") {
		t.Fatalf("cbt lookup r1 missing value alice: %s", got)
	}

	// read: full-table ReadRows scan must surface both rows.
	out := cbt("read", table)
	if !strings.Contains(out, "alice") || !strings.Contains(out, "bob") {
		t.Fatalf("cbt read missing seeded values: %s", out)
	}

	// count: a distinct data-plane op (streams SampleRowKeys internally and
	// reports the row count) — covers SampleRowKeys from the second adaptor.
	cnt := cbt("count", table)
	if !strings.Contains(cnt, "2") {
		t.Fatalf("cbt count did not report 2 rows: %s", cnt)
	}
}

// gcloudEnv returns the environment-variable set TestMain's gcloudCLI helper
// builds for every gcloud invocation — reused so cbt inherits the same admin
// endpoint overrides when it falls back to admin calls internally.
func gcloudEnv() []string {
	return []string{
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_DNS=" + baseURL + "/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_BIGTABLE=" + baseURL + "/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_BIGTABLEADMIN=" + baseURL + "/",
		"CLOUDSDK_CORE_PROJECT=" + project,
	}
}
