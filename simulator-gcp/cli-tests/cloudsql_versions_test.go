package gcp_cli_test

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gcloud sql instances create --database-version=POSTGRES_15 runs PostgreSQL
// 15, which the dump gcloud sql export sql writes names, and
// gcloud sql instances patch --database-version=POSTGRES_16 upgrades the
// instance in place to PostgreSQL 16 with its data.
func TestCloudSQLCLI_DatabaseVersionRunsItsReleaseAndUpgrades(t *testing.T) {
	t.Parallel()
	instance := "cli-sql-version"
	createCLISQLInstance(t, instance, "POSTGRES_15")
	installedVersion := func() string {
		var described struct {
			DatabaseVersion          string `json:"databaseVersion"`
			DatabaseInstalledVersion string `json:"databaseInstalledVersion"`
		}
		parseJSON(t, runCLI(t, gcloudCLI("sql", "instances", "describe", instance, "--format=json")), &described)
		return described.DatabaseVersion + " " + described.DatabaseInstalledVersion
	}
	assert.Equal(t, "POSTGRES_15 POSTGRES_15_19", installedVersion())

	_, serviceAccount := describeCLISQLServerCA(t, instance)
	require.NotEmpty(t, serviceAccount)
	runCLI(t, gcloudCLI("sql", "databases", "create", "appdb", "--instance", instance, "--quiet", "--format=json"))
	bucket := uniquename.New("cli-sql-version")
	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--location=us", "--format=json"))
	t.Cleanup(func() { _ = gcloudCLI("storage", "rm", "--recursive", "gs://"+bucket).Run() })
	runCLI(t, gcloudCLI("storage", "buckets", "add-iam-policy-binding", "gs://"+bucket,
		"--member=serviceAccount:"+serviceAccount, "--role=roles/storage.objectAdmin", "--format=json"))
	dump := func(object string) string {
		runCLI(t, gcloudCLI("sql", "export", "sql", instance, "gs://"+bucket+"/"+object, "--database", "appdb", "--format=json"))
		return runCLI(t, gcloudCLI("storage", "cat", "gs://"+bucket+"/"+object))
	}
	assert.Contains(t, dump("before.sql"), "-- Dumped from database version 15.19")

	runCLI(t, gcloudCLI("sql", "instances", "patch", instance, "--database-version=POSTGRES_16", "--quiet", "--format=json"))
	assert.Equal(t, "POSTGRES_16 POSTGRES_16_15", installedVersion())
	assert.Contains(t, dump("after.sql"), "-- Dumped from database version 16.15",
		"the upgraded instance's appdb is served by PostgreSQL 16")
}
