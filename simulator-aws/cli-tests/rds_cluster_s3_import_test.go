package aws_cli_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliTakeXtraBackup runs a MySQL 8.0 server holding the statements' data and
// writes a Percona XtraBackup of it, as an xbstream archive, to a file.
func cliTakeXtraBackup(t *testing.T, name string, statements ...string) string {
	t.Helper()
	docker := func(stdout *bytes.Buffer, args ...string) {
		t.Helper()
		var stderr bytes.Buffer
		cmd := exec.Command("docker", args...)
		cmd.Stdout, cmd.Stderr = stdout, &stderr
		require.NoError(t, cmd.Run(), "docker %s: %s", strings.Join(args, " "), stderr.String())
	}
	volume := name + "-data"
	docker(&bytes.Buffer{}, "volume", "create", volume)
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", volume).Run() })
	docker(&bytes.Buffer{}, "run", "-d", "--name", name, "-e", "MYSQL_ROOT_PASSWORD=source-root", "-v", volume+":/var/lib/mysql",
		"public.ecr.aws/docker/library/mysql:8.0")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	// The image's initialisation server listens on no TCP port, so a TCP
	// client reaches only the server that stays; mysqld offers nothing to
	// wait on until it answers one.
	client := []string{"exec", name, "mysql", "--protocol=TCP", "--host=127.0.0.1", "--user=root", "--password=source-root"}
	for exec.Command("docker", append(client, "--execute=SELECT 1")...).Run() != nil {
		time.Sleep(250 * time.Millisecond)
	}
	docker(&bytes.Buffer{}, append(client, "--execute="+strings.Join(statements, "; "))...)
	var backup bytes.Buffer
	docker(&backup, "run", "--rm", "--user", "root", "--network", "container:"+name, "-v", volume+":/var/lib/mysql:ro",
		"docker.io/percona/percona-xtrabackup:8.0", "xtrabackup", "--backup", "--stream=xbstream", "--user=root",
		"--password=source-root", "--host=127.0.0.1", "--target-dir=/tmp")
	path := filepath.Join(t.TempDir(), "backup.xbstream")
	require.NoError(t, os.WriteFile(path, backup.Bytes(), 0o600))
	return path
}

// aws rds restore-db-cluster-from-s3 imports a Percona XtraBackup from an
// Amazon S3 prefix the ingestion role reads into a new Aurora MySQL cluster,
// which serves the backup's data to its master user.
func TestRDSCLI_AuroraClusterRestoresFromS3XtraBackup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	backup := cliTakeXtraBackup(t, "cli-xtrabackup-source",
		"CREATE DATABASE shop",
		"CREATE TABLE shop.orders (id INT PRIMARY KEY, item VARCHAR(32) NOT NULL)",
		"INSERT INTO shop.orders VALUES (1, 'kettle'), (2, 'teapot')")
	bucket := "cli-aurora-s3-import"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	runCLI(t, awsCLI("s3", "cp", backup, "s3://"+bucket+"/backups/shop.xbstream"))
	roleName := "cli-rds-s3-import"
	var role struct {
		Role struct {
			Arn string `json:"Arn"`
		} `json:"Role"`
	}
	parseJSON(t, runCLI(t, awsCLI("iam", "create-role", "--role-name", roleName, "--assume-role-policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"rds.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)), &role)
	t.Cleanup(func() {
		_ = awsCLI("iam", "delete-role-policy", "--role-name", roleName, "--policy-name", "read").Run()
		_ = awsCLI("iam", "delete-role", "--role-name", roleName).Run()
	})
	runCLI(t, awsCLI("iam", "put-role-policy", "--role-name", roleName, "--policy-name", "read", "--policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListBucket","s3:GetObject"],"Resource":["arn:aws:s3:::`+bucket+`","arn:aws:s3:::`+bucket+`/*"]}]}`))

	clusterID := "cli-s3-import"
	out := runCLI(t, awsCLI("rds", "restore-db-cluster-from-s3",
		"--db-cluster-identifier", clusterID,
		"--engine", "aurora-mysql",
		"--master-username", cliRestoreUsername,
		"--master-user-password", cliRestorePassword,
		"--database-name", cliRestoreDatabase,
		"--source-engine", "mysql",
		"--source-engine-version", "8.0.40",
		"--s3-bucket-name", bucket,
		"--s3-prefix", "backups/",
		"--s3-ingestion-role-arn", role.Role.Arn))
	cliCleanupDBCluster(t, clusterID)
	var restored struct {
		DBCluster cliDBCluster `json:"DBCluster"`
	}
	parseJSON(t, out, &restored)
	assert.Equal(t, "creating", restored.DBCluster.Status)
	runCLI(t, awsCLI("rds", "wait", "db-cluster-available", "--db-cluster-identifier", clusterID))
	cliAddAuroraWriter(t, clusterID, "aurora-mysql")
	cluster := cliDescribeDBCluster(t, clusterID)
	config := mysql.Config{
		User: cliRestoreUsername, Passwd: cliRestorePassword, Net: "tcp", Addr: fmt.Sprintf("%s:%d", cluster.Endpoint, cluster.Port),
		DBName: cliRestoreDatabase, TLSConfig: "skip-verify", AllowCleartextPasswords: true,
	}
	db, err := sql.Open("mysql", config.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	rows, err := db.QueryContext(ctx, `SELECT item FROM shop.orders ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var items []string
	for rows.Next() {
		var item string
		require.NoError(t, rows.Scan(&item))
		items = append(items, item)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"kettle", "teapot"}, items, "the cluster serves the backup's data")
}

// An Aurora cluster lists the automated DB cluster snapshot Amazon Aurora
// takes when it creates the cluster, though no client connects, which aws rds
// delete-db-cluster-snapshot refuses to delete, and refuses a malformed
// backup window.
func TestRDSCLI_AuroraAutomatedSnapshots(t *testing.T) {
	clusterID := "cli-automated-backups"
	runCLI(t, awsCLI("rds", "create-db-cluster",
		"--db-cluster-identifier", clusterID,
		"--engine", "aurora-postgresql",
		"--master-username", cliRestoreUsername,
		"--master-user-password", cliRestorePassword,
		"--database-name", cliRestoreDatabase,
		"--preferred-backup-window", "03:00-03:30"))
	cliCleanupDBCluster(t, clusterID)

	var snapshots struct {
		DBClusterSnapshots []struct {
			DBClusterSnapshotIdentifier string `json:"DBClusterSnapshotIdentifier"`
			SnapshotType                string `json:"SnapshotType"`
			Status                      string `json:"Status"`
		} `json:"DBClusterSnapshots"`
	}
	// aws rds wait db-cluster-snapshot-available polls every 30 seconds, so
	// read the snapshot's status at the SDK waiter's cadence instead.
	require.Eventually(t, func() bool {
		parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-cluster-snapshots",
			"--db-cluster-identifier", clusterID, "--snapshot-type", "automated")), &snapshots)
		return len(snapshots.DBClusterSnapshots) == 1 && snapshots.DBClusterSnapshots[0].Status == "available"
	}, 3*time.Minute, 500*time.Millisecond, "Amazon Aurora takes the first automated backup when it creates the cluster")
	automated := snapshots.DBClusterSnapshots[0]
	assert.True(t, strings.HasPrefix(automated.DBClusterSnapshotIdentifier, "rds:"+clusterID+"-"))
	assert.Equal(t, "automated", automated.SnapshotType)
	cluster := cliDescribeDBCluster(t, clusterID)
	assert.NotEmpty(t, cluster.EarliestRestorableTime, "a cluster with an automated backup reports EarliestRestorableTime")
	assert.NotEmpty(t, cluster.LatestRestorableTime)
	refused := runCLIExpectError(t, awsCLI("rds", "delete-db-cluster-snapshot", "--db-cluster-snapshot-identifier", automated.DBClusterSnapshotIdentifier))
	assert.Contains(t, refused, "InvalidDBClusterSnapshotStateFault")
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-cluster-snapshots",
		"--db-cluster-identifier", clusterID, "--snapshot-type", "manual")), &snapshots)
	assert.Empty(t, snapshots.DBClusterSnapshots, "the cluster has no manual snapshot")

	refused = runCLIExpectError(t, awsCLI("rds", "modify-db-cluster", "--db-cluster-identifier", clusterID,
		"--preferred-backup-window", "3am-4am"))
	assert.Contains(t, refused, "InvalidParameterValue")
	var modified struct {
		DBCluster struct {
			PreferredBackupWindow string `json:"PreferredBackupWindow"`
		} `json:"DBCluster"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "modify-db-cluster", "--db-cluster-identifier", clusterID,
		"--preferred-backup-window", "04:00-04:30", "--apply-immediately")), &modified)
	assert.Equal(t, "04:00-04:30", modified.DBCluster.PreferredBackupWindow)
}
