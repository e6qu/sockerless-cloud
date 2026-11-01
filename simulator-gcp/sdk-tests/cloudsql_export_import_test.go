package gcp_sdk_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	_ "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	secretmanager "google.golang.org/api/secretmanager/v1"
	sqladmin "google.golang.org/api/sqladmin/v1"
)

// awaitSQLOperation polls operations.get until the operation is DONE and
// returns it, error or not.
func awaitSQLOperation(t *testing.T, svc *sqladmin.Service, project, operation string) *sqladmin.Operation {
	t.Helper()
	var done *sqladmin.Operation
	require.Eventually(t, func() bool {
		op, err := svc.Operations.Get(project, operation).Do()
		if err != nil || op.Status != "DONE" {
			return false
		}
		done = op
		return true
	}, 3*time.Minute, 500*time.Millisecond, "operation %s must settle", operation)
	return done
}

func requireSQLOperationFailed(t *testing.T, op *sqladmin.Operation, contains string) {
	t.Helper()
	require.NotNil(t, op.Error, "operation %s must fail", op.Name)
	require.NotEmpty(t, op.Error.Errors)
	assert.Contains(t, op.Error.Errors[0].Message, contains)
}

func readGCSObject(t *testing.T, client *storage.Client, bucket, object string) string {
	t.Helper()
	reader, err := client.Bucket(bucket).Object(object).NewReader(ctx)
	require.NoError(t, err)
	defer reader.Close()
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	return string(data)
}

func writeGCSObject(t *testing.T, client *storage.Client, bucket, object, contents string) {
	t.Helper()
	writer := client.Bucket(bucket).Object(object).NewWriter(ctx)
	_, err := writer.Write([]byte(contents))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
}

// exportBucket creates a bucket the instance's service account may read and
// write, as the export documentation has the operator grant
// storage.objectAdmin.
func exportBucket(t *testing.T, client *storage.Client, inst *sqladmin.DatabaseInstance, grant bool) string {
	t.Helper()
	require.NotEmpty(t, inst.ServiceAccountEmailAddress)
	name := uniquename.New("sql-transfer")
	bucket := client.Bucket(name)
	require.NoError(t, bucket.Create(ctx, "test-project", nil))
	if grant {
		grantOnBucket(t, bucket, "roles/storage.objectAdmin", "serviceAccount:"+inst.ServiceAccountEmailAddress)
	}
	return name
}

// instances.export writes a pg_dump of the database and a CSV of a query to
// Cloud Storage as the instance's service account, refusing an object that
// already exists and a bucket the account holds no role on; instances.import
// loads both back into the engine, and a stock driver reads the rows.
func TestCloudSQL_ExportAndImportMoveDataThroughCloudStorage(t *testing.T) {
	t.Parallel()
	testContext, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	svc := sqlAdminService(t)
	gcs := storageClient(t)
	defer gcs.Close()
	const project, name, rootPassword, database = "test-project", "transfer-pg", "RootPassword-123!", "appdb"
	inst := createSQLInstanceWithEngine(t, svc, project, &sqladmin.DatabaseInstance{
		Name: name, Region: "us-central1", DatabaseVersion: "POSTGRES_15", RootPassword: rootPassword,
	})
	_, err := svc.Databases.Insert(project, name, &sqladmin.Database{Name: database}).Do()
	require.NoError(t, err)

	config, err := pgx.ParseConfig(fmt.Sprintf("postgres://postgres@%s:5432/%s?sslmode=prefer", inst.IpAddresses[0].IpAddress, database))
	require.NoError(t, err)
	config.Password = rootPassword
	conn, err := pgx.ConnectConfig(testContext, config)
	require.NoError(t, err)
	defer func() { _ = conn.Close(context.Background()) }()
	_, err = conn.Exec(testContext, `CREATE TABLE ledger (id int PRIMARY KEY, entry text)`)
	require.NoError(t, err)
	_, err = conn.Exec(testContext, `INSERT INTO ledger VALUES (1, 'first'), (2, 'second, with a comma')`)
	require.NoError(t, err)

	denied := exportBucket(t, gcs, inst, false)
	op, err := svc.Instances.Export(project, name, &sqladmin.InstancesExportRequest{ExportContext: &sqladmin.ExportContext{
		FileType: "SQL", Uri: "gs://" + denied + "/dump.sql", Databases: []string{database},
	}}).Do()
	require.NoError(t, err)
	requireSQLOperationFailed(t, awaitSQLOperation(t, svc, project, op.Name),
		"The service account does not have the required permissions for the bucket")

	bucket := exportBucket(t, gcs, inst, true)
	op, err = svc.Instances.Export(project, name, &sqladmin.InstancesExportRequest{ExportContext: &sqladmin.ExportContext{
		FileType: "SQL", Uri: "gs://" + bucket + "/dump.sql.gz", Databases: []string{database},
	}}).Do()
	require.NoError(t, err)
	assert.Equal(t, "EXPORT", op.OperationType)
	require.NotNil(t, op.ExportContext)
	waitSQLOperationDone(t, svc, project, op.Name)
	reader, err := gcs.Bucket(bucket).Object("dump.sql.gz").ReadCompressed(true).NewReader(ctx)
	require.NoError(t, err)
	compressed, err := io.ReadAll(reader)
	require.NoError(t, err)
	_ = reader.Close()
	require.Equal(t, []byte{0x1f, 0x8b}, compressed[:2], "a .gz export is gzip-compressed")

	op, err = svc.Instances.Export(project, name, &sqladmin.InstancesExportRequest{ExportContext: &sqladmin.ExportContext{
		FileType: "SQL", Uri: "gs://" + bucket + "/dump.sql", Databases: []string{database},
	}}).Do()
	require.NoError(t, err)
	waitSQLOperationDone(t, svc, project, op.Name)
	dump := readGCSObject(t, gcs, bucket, "dump.sql")
	assert.Contains(t, dump, "CREATE TABLE public.ledger")
	assert.Contains(t, dump, "second, with a comma")

	op, err = svc.Instances.Export(project, name, &sqladmin.InstancesExportRequest{ExportContext: &sqladmin.ExportContext{
		FileType: "SQL", Uri: "gs://" + bucket + "/dump.sql", Databases: []string{database},
	}}).Do()
	require.NoError(t, err)
	requireSQLOperationFailed(t, awaitSQLOperation(t, svc, project, op.Name), "already exists")

	op, err = svc.Instances.Export(project, name, &sqladmin.InstancesExportRequest{ExportContext: &sqladmin.ExportContext{
		FileType: "CSV", Uri: "gs://" + bucket + "/ledger.csv", Databases: []string{database},
		CsvExportOptions: &sqladmin.ExportContextCsvExportOptions{SelectQuery: "SELECT id, entry FROM ledger ORDER BY id"},
	}}).Do()
	require.NoError(t, err)
	waitSQLOperationDone(t, svc, project, op.Name)
	assert.Equal(t, "1,first\n2,\"second, with a comma\"\n", readGCSObject(t, gcs, bucket, "ledger.csv"))

	_, err = conn.Exec(testContext, `DROP TABLE ledger`)
	require.NoError(t, err)
	op, err = svc.Instances.Import(project, name, &sqladmin.InstancesImportRequest{ImportContext: &sqladmin.ImportContext{
		FileType: "SQL", Uri: "gs://" + bucket + "/dump.sql.gz", Database: database,
	}}).Do()
	require.NoError(t, err)
	assert.Equal(t, "IMPORT", op.OperationType)
	waitSQLOperationDone(t, svc, project, op.Name)
	var count int
	require.NoError(t, conn.QueryRow(testContext, `SELECT count(*) FROM ledger`).Scan(&count))
	assert.Equal(t, 2, count, "the import restores the dumped rows")

	writeGCSObject(t, gcs, bucket, "more.csv", "3,third\n4,\"fourth, quoted\"\n")
	op, err = svc.Instances.Import(project, name, &sqladmin.InstancesImportRequest{ImportContext: &sqladmin.ImportContext{
		FileType: "CSV", Uri: "gs://" + bucket + "/more.csv", Database: database,
		CsvImportOptions: &sqladmin.ImportContextCsvImportOptions{Table: "ledger", Columns: []string{"id", "entry"}},
	}}).Do()
	require.NoError(t, err)
	waitSQLOperationDone(t, svc, project, op.Name)
	var entry string
	require.NoError(t, conn.QueryRow(testContext, `SELECT entry FROM ledger WHERE id = 4`).Scan(&entry))
	assert.Equal(t, "fourth, quoted", entry)

	op, err = svc.Instances.Import(project, name, &sqladmin.InstancesImportRequest{ImportContext: &sqladmin.ImportContext{
		FileType: "SQL", Uri: "gs://" + bucket + "/missing.sql", Database: database,
	}}).Do()
	require.NoError(t, err)
	requireSQLOperationFailed(t, awaitSQLOperation(t, svc, project, op.Name), "404")

	_, err = svc.Instances.Export(project, name, &sqladmin.InstancesExportRequest{ExportContext: &sqladmin.ExportContext{
		FileType: "BAK", Uri: "gs://" + bucket + "/db.bak", Databases: []string{database},
	}}).Do()
	requireSQLAPIError(t, err, 400)
}

// regionalPasswordSecret stores password as a regional secret version in the
// instance's region and returns the version's name.
func regionalPasswordSecret(t *testing.T, location, password string) string {
	t.Helper()
	sm := secretManagerService(t)
	parent := "projects/test-project/locations/" + location
	id := uniquename.New("sql-password")
	_, err := sm.Projects.Locations.Secrets.Create(parent, &secretmanager.Secret{}).SecretId(id).Do()
	require.NoError(t, err)
	version, err := sm.Projects.Locations.Secrets.AddVersion(parent+"/secrets/"+id, &secretmanager.AddSecretVersionRequest{
		Payload: &secretmanager.SecretPayload{Data: base64.StdEncoding.EncodeToString([]byte(password))},
	}).Do()
	require.NoError(t, err)
	return version.Name
}

// instances.executeSql runs statements on the engine as the named user with
// the password Secret Manager holds, returning each statement's columns,
// rows, NULLs and notices; an instance must allow the Data API first, and a
// failing statement returns its error in status and no results.
func TestCloudSQL_ExecuteSqlRunsOnTheEngine(t *testing.T) {
	t.Parallel()
	svc := sqlAdminService(t)
	const project, name, rootPassword, database = "test-project", "execsql-pg", "RootPassword-123!", "appdb"
	createSQLInstanceWithEngine(t, svc, project, &sqladmin.DatabaseInstance{
		Name: name, Region: "us-central1", DatabaseVersion: "POSTGRES_15", RootPassword: rootPassword,
	})
	_, err := svc.Databases.Insert(project, name, &sqladmin.Database{Name: database}).Do()
	require.NoError(t, err)
	_, err = svc.Users.Insert(project, name, &sqladmin.User{Name: "dataapi", Password: "DataApi-123!"}).Do()
	require.NoError(t, err)
	secret := regionalPasswordSecret(t, "us-central1", "DataApi-123!")
	payload := &sqladmin.ExecuteSqlPayload{
		Database: database, User: "dataapi", PasswordSecretVersion: secret,
		SqlStatement: `CREATE TABLE notes (id int, body text); INSERT INTO notes VALUES (1, 'hello'), (2, NULL); ` +
			`DO $$BEGIN RAISE NOTICE 'seeded'; END$$; SELECT id, body, current_user AS who FROM notes ORDER BY id`,
	}

	_, err = svc.Instances.ExecuteSql(project, name, payload).Do()
	requireSQLAPIError(t, err, 400)

	_, err = svc.Instances.Patch(project, name, &sqladmin.DatabaseInstance{Settings: &sqladmin.Settings{DataApiAccess: "ALLOW_DATA_API"}}).Do()
	require.NoError(t, err)
	response, err := svc.Instances.ExecuteSql(project, name, payload).Do()
	require.NoError(t, err)
	require.Nil(t, response.Status)
	require.Len(t, response.Results, 4)
	query := response.Results[3]
	require.Len(t, query.Columns, 3)
	assert.Equal(t, "id", query.Columns[0].Name)
	assert.Equal(t, "int4", query.Columns[0].Type)
	require.Len(t, query.Rows, 2)
	assert.Equal(t, "hello", query.Rows[0].Values[1].Value)
	assert.Equal(t, "dataapi", query.Rows[0].Values[2].Value, "the statements run as the named user")
	assert.True(t, query.Rows[1].Values[1].NullValue)
	require.NotEmpty(t, response.Messages)
	assert.Contains(t, response.Messages[0].Message, "seeded")
	assert.Equal(t, "NOTICE", response.Messages[0].Severity)

	limited, err := svc.Instances.ExecuteSql(project, name, &sqladmin.ExecuteSqlPayload{
		Database: database, User: "dataapi", PasswordSecretVersion: secret, RowLimit: 1,
		SqlStatement: "SELECT id FROM notes ORDER BY id",
	}).Do()
	require.NoError(t, err)
	require.Len(t, limited.Results[0].Rows, 1)

	failed, err := svc.Instances.ExecuteSql(project, name, &sqladmin.ExecuteSqlPayload{
		Database: database, User: "dataapi", PasswordSecretVersion: secret, SqlStatement: "SELECT * FROM missing_table",
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, failed.Status)
	assert.Contains(t, failed.Status.Message, "missing_table")
	assert.Empty(t, failed.Results)

	wrong := regionalPasswordSecret(t, "us-central1", "not-the-password")
	refused, err := svc.Instances.ExecuteSql(project, name, &sqladmin.ExecuteSqlPayload{
		Database: database, User: "dataapi", PasswordSecretVersion: wrong, SqlStatement: "SELECT 1",
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, refused.Status, "the engine refuses a wrong password")

	_, err = svc.Instances.ExecuteSql(project, name, &sqladmin.ExecuteSqlPayload{
		Database: database, AutoIamAuthn: true, PasswordSecretVersion: secret, SqlStatement: "SELECT 1",
	}).Do()
	requireSQLAPIError(t, err, 400)
	_, err = svc.Instances.ExecuteSql(project, name, &sqladmin.ExecuteSqlPayload{
		Database: database, AutoIamAuthn: true, SqlStatement: "SELECT 1",
	}).Do()
	requireSQLAPIError(t, err, 501)
}

// A MySQL instance exports with mysqldump and SELECT ... INTO OUTFILE, imports
// with mysql and LOAD DATA INFILE, and runs executeSql through the stock
// MySQL protocol.
func TestCloudSQL_MySQLExportImportAndExecuteSql(t *testing.T) {
	t.Parallel()
	testContext, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	svc := sqlAdminService(t)
	gcs := storageClient(t)
	defer gcs.Close()
	const project, name, rootPassword, database = "test-project", "transfer-mysql", "RootPassword-123!", "appdb"
	inst := createSQLInstanceWithEngine(t, svc, project, &sqladmin.DatabaseInstance{
		Name: name, Region: "us-central1", DatabaseVersion: "MYSQL_8_0", RootPassword: rootPassword,
		Settings: &sqladmin.Settings{Tier: "db-f1-micro", DataApiAccess: "ALLOW_DATA_API"},
	})
	_, err := svc.Databases.Insert(project, name, &sqladmin.Database{Name: database}).Do()
	require.NoError(t, err)

	db, err := sql.Open("mysql", fmt.Sprintf("root:%s@tcp(%s:3306)/%s?tls=skip-verify&allowCleartextPasswords=true", rootPassword, inst.IpAddresses[0].IpAddress, database))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.ExecContext(testContext, `CREATE TABLE ledger (id int PRIMARY KEY, entry varchar(64))`)
	require.NoError(t, err)
	_, err = db.ExecContext(testContext, `INSERT INTO ledger VALUES (1, 'first'), (2, 'second')`)
	require.NoError(t, err)

	bucket := exportBucket(t, gcs, inst, true)
	op, err := svc.Instances.Export(project, name, &sqladmin.InstancesExportRequest{ExportContext: &sqladmin.ExportContext{
		FileType: "SQL", Uri: "gs://" + bucket + "/all.sql",
	}}).Do()
	require.NoError(t, err)
	waitSQLOperationDone(t, svc, project, op.Name)
	dump := readGCSObject(t, gcs, bucket, "all.sql")
	assert.Contains(t, dump, "CREATE DATABASE")
	assert.Contains(t, dump, "`ledger`")
	assert.NotContains(t, dump, "USE `mysql`", "the mysql system database stays out of the export")

	op, err = svc.Instances.Export(project, name, &sqladmin.InstancesExportRequest{ExportContext: &sqladmin.ExportContext{
		FileType: "CSV", Uri: "gs://" + bucket + "/ledger.csv", Databases: []string{database},
		CsvExportOptions: &sqladmin.ExportContextCsvExportOptions{SelectQuery: "SELECT id, entry FROM ledger ORDER BY id", EscapeCharacter: "5C"},
	}}).Do()
	require.NoError(t, err)
	waitSQLOperationDone(t, svc, project, op.Name)
	assert.Equal(t, "1,\"first\"\n2,\"second\"\n", readGCSObject(t, gcs, bucket, "ledger.csv"))

	_, err = db.ExecContext(testContext, `DROP TABLE ledger`)
	require.NoError(t, err)
	op, err = svc.Instances.Import(project, name, &sqladmin.InstancesImportRequest{ImportContext: &sqladmin.ImportContext{
		FileType: "SQL", Uri: "gs://" + bucket + "/all.sql",
	}}).Do()
	require.NoError(t, err)
	waitSQLOperationDone(t, svc, project, op.Name)
	var count int
	require.NoError(t, db.QueryRowContext(testContext, `SELECT count(*) FROM ledger`).Scan(&count))
	assert.Equal(t, 2, count)

	writeGCSObject(t, gcs, bucket, "more.csv", "3,\"third\"\n")
	op, err = svc.Instances.Import(project, name, &sqladmin.InstancesImportRequest{ImportContext: &sqladmin.ImportContext{
		FileType: "CSV", Uri: "gs://" + bucket + "/more.csv", Database: database,
		CsvImportOptions: &sqladmin.ImportContextCsvImportOptions{Table: "ledger"},
	}}).Do()
	require.NoError(t, err)
	waitSQLOperationDone(t, svc, project, op.Name)

	secret := regionalPasswordSecret(t, "us-central1", rootPassword)
	response, err := svc.Instances.ExecuteSql(project, name, &sqladmin.ExecuteSqlPayload{
		Database: database, User: "root", PasswordSecretVersion: secret,
		SqlStatement: "SELECT id, entry FROM ledger ORDER BY id; SELECT NULL AS nothing",
	}).Do()
	require.NoError(t, err)
	require.Nil(t, response.Status)
	require.Len(t, response.Results, 2)
	require.Len(t, response.Results[0].Rows, 3)
	assert.Equal(t, "third", response.Results[0].Rows[2].Values[1].Value)
	assert.Equal(t, "INT", response.Results[0].Columns[0].Type)
	assert.True(t, response.Results[1].Rows[0].Values[0].NullValue)
	require.NotNil(t, response.Metadata)
	assert.True(t, strings.HasSuffix(response.Metadata.SqlStatementExecutionTime, "s"), "a google.protobuf.Duration")
}
