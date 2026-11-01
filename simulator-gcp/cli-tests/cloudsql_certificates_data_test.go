package gcp_cli_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliSQLSslCert struct {
	Cert            string `json:"cert"`
	CommonName      string `json:"commonName"`
	Sha1Fingerprint string `json:"sha1Fingerprint"`
	CreateTime      string `json:"createTime"`
}

func createCLISQLInstance(t *testing.T, instance, version string, extra ...string) {
	t.Helper()
	args := append([]string{"sql", "instances", "create", instance,
		"--database-version", version, "--region", location, "--tier", "db-f1-micro",
		"--root-password", "Str0ng-password-12345!", "--quiet", "--format=json"}, extra...)
	runCLI(t, gcloudCLI(args...))
	t.Cleanup(func() {
		_ = gcloudCLI("sql", "instances", "delete", instance, "--quiet", "--format=json").Run()
	})
}

func describeCLISQLServerCA(t *testing.T, instance string) (serverCA cliSQLSslCert, serviceAccount string) {
	t.Helper()
	var described struct {
		ServerCaCert               cliSQLSslCert `json:"serverCaCert"`
		ServiceAccountEmailAddress string        `json:"serviceAccountEmailAddress"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("sql", "instances", "describe", instance, "--format=json")), &described)
	return described.ServerCaCert, described.ServiceAccountEmailAddress
}

func activeFingerprints(t *testing.T, args ...string) []cliSQLSslCert {
	t.Helper()
	var certs []cliSQLSslCert
	parseJSON(t, runCLI(t, gcloudCLI(append(args, "--format=json")...)), &certs)
	return certs
}

// gcloud sql ssl client-certs create writes the private key Cloud SQL
// returns, and describe reads back the certificate the instance's server CA
// signed; server-ca-certs create, rotate and rollback move the active CA, and
// reset-ssl-config deletes every client certificate.
func TestCloudSQLCLI_SslCertificates(t *testing.T) {
	t.Parallel()
	instance := "cli-sql-ssl"
	createCLISQLInstance(t, instance, "POSTGRES_15")
	serverCA, _ := describeCLISQLServerCA(t, instance)
	require.NotEmpty(t, serverCA.Cert)

	keyFile := filepath.Join(t.TempDir(), "client-key.pem")
	runCLI(t, gcloudCLI("sql", "ssl", "client-certs", "create", "cli-client", keyFile, "--instance", instance, "--format=json"))
	key, err := os.ReadFile(keyFile)
	require.NoError(t, err)
	var described cliSQLSslCert
	parseJSON(t, runCLI(t, gcloudCLI("sql", "ssl", "client-certs", "describe", "cli-client", "--instance", instance, "--format=json")), &described)
	_, err = tls.X509KeyPair([]byte(described.Cert), key)
	require.NoError(t, err, "the key gcloud wrote belongs to the certificate")
	block, _ := pem.Decode([]byte(described.Cert))
	require.NotNil(t, block)
	clientCert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM([]byte(serverCA.Cert)))
	_, err = clientCert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	require.NoError(t, err, "the instance's server CA signs the client certificate")
	require.Len(t, activeFingerprints(t, "sql", "ssl", "client-certs", "list", "--instance", instance), 1)

	runCLI(t, gcloudCLI("sql", "ssl", "server-ca-certs", "create", "--instance", instance, "--format=json"))
	cas := activeFingerprints(t, "sql", "ssl", "server-ca-certs", "list", "--instance", instance)
	require.Len(t, cas, 2)
	runCLI(t, gcloudCLI("sql", "ssl", "server-ca-certs", "rotate", "--instance", instance, "--format=json"))
	rotated, _ := describeCLISQLServerCA(t, instance)
	assert.NotEqual(t, serverCA.Sha1Fingerprint, rotated.Sha1Fingerprint, "rotate makes the upcoming CA active")
	runCLI(t, gcloudCLI("sql", "ssl", "server-ca-certs", "rollback", "--instance", instance, "--format=json"))
	rolledBack, _ := describeCLISQLServerCA(t, instance)
	assert.Equal(t, serverCA.Sha1Fingerprint, rolledBack.Sha1Fingerprint, "rollback returns to the previous CA")

	runCLI(t, gcloudCLI("sql", "instances", "reset-ssl-config", instance, "--quiet", "--format=json"))
	assert.Empty(t, activeFingerprints(t, "sql", "ssl", "client-certs", "list", "--instance", instance))
	reset, _ := describeCLISQLServerCA(t, instance)
	assert.NotEqual(t, serverCA.Sha1Fingerprint, reset.Sha1Fingerprint)
}

// gcloud sql ssl server-certs rotates the server certificate of a shared-CA
// instance, and entraid-certs the Microsoft Entra ID certificate of a SQL
// Server instance.
func TestCloudSQLCLI_ServerAndEntraIdCertificates(t *testing.T) {
	t.Parallel()
	shared := "cli-sql-cas"
	createCLISQLInstance(t, shared, "POSTGRES_15", "--server-ca-mode=GOOGLE_MANAGED_CAS_CA")
	var listed []struct {
		CaCert   cliSQLSslCert `json:"ca_cert"`
		SslCert  cliSQLSslCert `json:"ssl_cert"`
		Status   string        `json:"status"`
		Category string        `json:"category"`
	}
	out := runCLI(t, gcloudCLI("sql", "ssl", "server-certs", "list", "--instance", shared, "--format=json"))
	require.NotEmpty(t, strings.TrimSpace(out))
	runCLI(t, gcloudCLI("sql", "ssl", "server-certs", "create", "--instance", shared, "--format=json"))
	runCLI(t, gcloudCLI("sql", "ssl", "server-certs", "rotate", "--instance", shared, "--format=json"))
	out = runCLI(t, gcloudCLI("sql", "ssl", "server-certs", "list", "--instance", shared, "--format=json"))
	parseJSON(t, out, &listed)
	require.NotEmpty(t, listed)

	sqlServer := "cli-sql-entra"
	createCLISQLInstance(t, sqlServer, "SQLSERVER_2019_STANDARD")
	runCLI(t, gcloudCLI("sql", "ssl", "entraid-certs", "create", "--instance", sqlServer, "--format=json"))
	runCLI(t, gcloudCLI("sql", "ssl", "entraid-certs", "create", "--instance", sqlServer, "--format=json"))
	before := runCLI(t, gcloudCLI("sql", "ssl", "entraid-certs", "list", "--instance", sqlServer, "--format=json"))
	runCLI(t, gcloudCLI("sql", "ssl", "entraid-certs", "rotate", "--instance", sqlServer, "--format=json"))
	after := runCLI(t, gcloudCLI("sql", "ssl", "entraid-certs", "list", "--instance", sqlServer, "--format=json"))
	assert.NotEqual(t, before, after, "rotate changes which Entra ID certificate is active")
}

// gcloud sql import csv loads a Cloud Storage object into the engine and
// gcloud sql export csv and export sql write the engine's data back out, as
// the instance's service account the bucket's policy grants
// storage.objectAdmin.
func TestCloudSQLCLI_ExportAndImport(t *testing.T) {
	t.Parallel()
	instance := "cli-sql-transfer"
	createCLISQLInstance(t, instance, "POSTGRES_15")
	_, serviceAccount := describeCLISQLServerCA(t, instance)
	require.NotEmpty(t, serviceAccount)
	runCLI(t, gcloudCLI("sql", "databases", "create", "appdb", "--instance", instance, "--quiet", "--format=json"))

	bucket := uniquename.New("cli-sql-transfer")
	runCLI(t, gcloudCLI("storage", "buckets", "create", "gs://"+bucket, "--location=us", "--format=json"))
	t.Cleanup(func() { _ = gcloudCLI("storage", "rm", "--recursive", "gs://"+bucket).Run() })

	dir := t.TempDir()
	schema := filepath.Join(dir, "schema.sql")
	require.NoError(t, os.WriteFile(schema, []byte("CREATE TABLE ledger (id int, entry text);\n"), 0o600))
	runCLI(t, gcloudCLI("storage", "cp", schema, "gs://"+bucket+"/schema.sql"))
	rows := filepath.Join(dir, "rows.csv")
	require.NoError(t, os.WriteFile(rows, []byte("1,first\n2,\"second, quoted\"\n"), 0o600))
	runCLI(t, gcloudCLI("storage", "cp", rows, "gs://"+bucket+"/rows.csv"))

	failed := gcloudCLIFails(t, gcloudCLI("sql", "import", "sql", instance, "gs://"+bucket+"/schema.sql",
		"--database", "appdb", "--quiet", "--format=json"))
	assert.Contains(t, failed, "The service account does not have the required permissions for the bucket")

	runCLI(t, gcloudCLI("storage", "buckets", "add-iam-policy-binding", "gs://"+bucket,
		"--member=serviceAccount:"+serviceAccount, "--role=roles/storage.objectAdmin", "--format=json"))
	runCLI(t, gcloudCLI("sql", "import", "sql", instance, "gs://"+bucket+"/schema.sql",
		"--database", "appdb", "--quiet", "--format=json"))
	runCLI(t, gcloudCLI("sql", "import", "csv", instance, "gs://"+bucket+"/rows.csv",
		"--database", "appdb", "--table", "ledger", "--quiet", "--format=json"))

	runCLI(t, gcloudCLI("sql", "export", "csv", instance, "gs://"+bucket+"/out.csv",
		"--database", "appdb", "--query", "SELECT id, entry FROM ledger ORDER BY id", "--format=json"))
	assert.Equal(t, "1,first\n2,\"second, quoted\"\n", runCLI(t, gcloudCLI("storage", "cat", "gs://"+bucket+"/out.csv")))

	runCLI(t, gcloudCLI("sql", "export", "sql", instance, "gs://"+bucket+"/dump.sql", "--database", "appdb", "--format=json"))
	dump := runCLI(t, gcloudCLI("storage", "cat", "gs://"+bucket+"/dump.sql"))
	assert.Contains(t, dump, "CREATE TABLE public.ledger")
	assert.Contains(t, dump, "second, quoted")

	failed = gcloudCLIFails(t, gcloudCLI("sql", "export", "sql", instance, "gs://"+bucket+"/dump.sql", "--database", "appdb", "--format=json"))
	assert.Contains(t, failed, "already exists")
}

// gcloud sql instances execute-sql needs an instance that allows the Data
// API; gcloud authenticates with the caller's IAM identity (autoIamAuthn),
// which the simulator declares unserved, so the CLI meets the refusal.
func TestCloudSQLCLI_ExecuteSql(t *testing.T) {
	t.Parallel()
	instance := "cli-sql-execsql"
	createCLISQLInstance(t, instance, "POSTGRES_15")
	refused := gcloudCLIFails(t, gcloudCLI("sql", "instances", "execute-sql", instance, "--database", "postgres", "--sql", "SELECT 1", "--format=json"))
	assert.Contains(t, refused, "dataApiAccess")
}
