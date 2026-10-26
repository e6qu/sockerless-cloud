package gcp_cli_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSpannerCLI_InstanceDatabaseList(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	httpDoJSON(t, "POST", baseURL+"/v1b3/projects/"+project+"/locations/"+location+"/jobs",
		`{"name":"cli-dataflow-job","type":"JOB_TYPE_BATCH"}`)

	out := runCLI(t, gcloudCLI("dataflow", "jobs", "list", "--region="+location, "--format=value(name)"))
	if !strings.Contains(out, "cli-dataflow-job") {
		t.Fatalf("dataflow jobs list missing seeded job: %s", out)
	}
}

func TestBigtableCLI_InstanceTableList(t *testing.T) {
	t.Parallel()
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

// TestBigtableCLI_DataPlane_cbt drives the Bigtable Data API v2 through `cbt`,
// configured as against Google: the admin and data endpoints, the bootstrap key
// as its credential, and the authority the gRPC port's certificate chains to.
// gcloud bigtable is admin-only. TestMain builds cbt (installCBT).
func TestBigtableCLI_DataPlane_cbt(t *testing.T) {
	t.Parallel()
	const (
		instance = "cbt-dp"
		table    = "cbt_table"
		family   = "cf"
	)
	runCLI(t, gcloudCLI("bigtable", "instances", "create", instance,
		"--display-name=cbt data plane",
		"--cluster="+instance+"-c1",
		"--cluster-zone=us-central1-a",
		"--cluster-num-nodes=1",
		"--instance-type=PRODUCTION",
		"--format=json"))

	cbt := func(args ...string) string {
		cmd := exec.Command(cbtPath, append([]string{
			"-project=" + project, "-instance=" + instance,
			"-admin-endpoint=" + grpcAddr, "-data-endpoint=" + grpcAddr,
			"-creds=" + bootstrapKeyFile(), "-cert-file=" + grpcTLS.CA,
		}, args...)...)
		cmd.Env = gcloudEnv()
		return runCLI(t, cmd)
	}

	// cbt drives BigtableTableAdmin over gRPC too.
	cbt("createtable", table)
	cbt("createfamily", table, family)

	// Value format is family:qualifier=value.
	cbt("set", table, "r1", family+":name=alice")
	cbt("set", table, "r2", family+":name=bob")

	got := cbt("lookup", table, "r1")
	if !strings.Contains(got, "alice") {
		t.Fatalf("cbt lookup r1 missing value alice: %s", got)
	}

	out := cbt("read", table)
	if !strings.Contains(out, "alice") || !strings.Contains(out, "bob") {
		t.Fatalf("cbt read missing seeded values: %s", out)
	}

	// count streams SampleRowKeys. cbt logs its credential choice first, so
	// compare the whole last line: "2" also matches "12", "20" or a timing suffix.
	countOut := cbt("count", table)
	reported := strings.Split(strings.TrimSpace(countOut), "\n")
	cnt := strings.TrimSpace(reported[len(reported)-1])
	if cnt != "2" {
		t.Fatalf("cbt count reported %q, want the two rows that were written", countOut)
	}
}

// gcloudEnv reuses the admin endpoint overrides TestMain's gcloudCLI sets.
func gcloudEnv() []string {
	return []string{
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_DNS=" + baseURL + "/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_BIGTABLE=" + baseURL + "/",
		"CLOUDSDK_API_ENDPOINT_OVERRIDES_BIGTABLEADMIN=" + baseURL + "/",
		"CLOUDSDK_CORE_PROJECT=" + project,
	}
}
