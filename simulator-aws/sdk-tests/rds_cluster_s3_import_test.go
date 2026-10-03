package aws_sdk_test

import (
	"bytes"
	"context"
	"database/sql"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	xtraBackupSourceImage = "public.ecr.aws/docker/library/mysql:8.0"
	xtraBackupImage       = "docker.io/percona/percona-xtrabackup:8.0"
)

// takeXtraBackup runs a MySQL 8.0 server, has it hold the statements' data,
// and returns a Percona XtraBackup of it as an xbstream archive.
func takeXtraBackup(t *testing.T, name string, statements ...string) []byte {
	t.Helper()
	docker := func(args ...string) []byte {
		t.Helper()
		var stdout, stderr bytes.Buffer
		cmd := exec.Command("docker", args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		require.NoError(t, cmd.Run(), "docker %s: %s", strings.Join(args, " "), stderr.String())
		return stdout.Bytes()
	}
	volume := name + "-data"
	docker("volume", "create", volume)
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", volume).Run() })
	docker("run", "-d", "--name", name, "-e", "MYSQL_ROOT_PASSWORD=source-root", "-v", volume+":/var/lib/mysql", xtraBackupSourceImage)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	// The image's initialisation server listens on no TCP port, so a TCP
	// client reaches only the server that stays; mysqld offers nothing to
	// wait on until it answers one.
	client := []string{"exec", name, "mysql", "--protocol=TCP", "--host=127.0.0.1", "--user=root", "--password=source-root"}
	for exec.Command("docker", append(client, "--execute=SELECT 1")...).Run() != nil {
		time.Sleep(waiterMinDelay)
	}
	docker(append(client, "--execute="+strings.Join(statements, "; "))...)
	return docker("run", "--rm", "--user", "root", "--network", "container:"+name, "-v", volume+":/var/lib/mysql:ro",
		xtraBackupImage, "xtrabackup", "--backup", "--stream=xbstream", "--user=root", "--password=source-root",
		"--host=127.0.0.1", "--target-dir=/tmp")
}

// RestoreDBClusterFromS3 imports a Percona XtraBackup of a MySQL 8.0 server
// from Amazon S3 into a new Aurora MySQL cluster, which serves the backup's
// data to the master user the request names. A bucket the ingestion role
// cannot read, or a prefix without objects, is refused.
func TestRDS_AuroraClusterRestoresFromS3XtraBackup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	backup := takeXtraBackup(t, "sdk-xtrabackup-source",
		"CREATE DATABASE shop",
		"CREATE TABLE shop.orders (id INT PRIMARY KEY, item VARCHAR(32) NOT NULL)",
		"INSERT INTO shop.orders VALUES (1, 'kettle'), (2, 'teapot')",
		"CREATE USER 'shopper'@'%' IDENTIFIED WITH mysql_native_password BY 'Shopper-Password-1'",
		"GRANT SELECT ON shop.* TO 'shopper'@'%'")

	bucket := "sdk-aurora-s3-import"
	s3c := s3Client()
	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("backups/shop.xbstream"), Body: bytes.NewReader(backup),
	})
	require.NoError(t, err)

	iamc := iamClient()
	roleName := uniqueName("rds-s3-import")
	role, err := iamc.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Principal":{"Service":"rds.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	_, err = iamc.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName: aws.String(roleName), PolicyName: aws.String("list"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::` + bucket + `"}]}`),
	})
	require.NoError(t, err)

	client := rdsClient()
	request := func(clusterID, prefix string) *rds.RestoreDBClusterFromS3Input {
		return &rds.RestoreDBClusterFromS3Input{
			DBClusterIdentifier: aws.String(clusterID),
			Engine:              aws.String("aurora-mysql"),
			MasterUsername:      aws.String(restoreSourceUsername),
			MasterUserPassword:  aws.String(restoreSourcePassword),
			DatabaseName:        aws.String(restoreSourceDatabase),
			SourceEngine:        aws.String("mysql"),
			SourceEngineVersion: aws.String("8.0.40"),
			S3BucketName:        aws.String(bucket),
			S3Prefix:            aws.String(prefix),
			S3IngestionRoleArn:  role.Role.Arn,
		}
	}
	_, err = client.RestoreDBClusterFromS3(ctx, request("sdk-s3-import-denied", "backups/"))
	assertAWSAPIErrorCode(t, err, "InvalidS3BucketFault")

	_, err = iamc.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName: aws.String(roleName), PolicyName: aws.String("read"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::` + bucket + `/*"}]}`),
	})
	require.NoError(t, err)
	_, err = client.RestoreDBClusterFromS3(ctx, request("sdk-s3-import-empty", "nothing-here/"))
	assertAWSAPIErrorCode(t, err, "InvalidS3BucketFault")

	f := auroraClusterFixture{t: t, ctx: ctx, client: client, engine: "aurora-mysql", family: "mysql"}
	clusterID := "sdk-s3-import"
	restored, err := client.RestoreDBClusterFromS3(ctx, request(clusterID, "backups/"))
	require.NoError(t, err)
	f.cleanupCluster(clusterID)
	assert.Equal(t, "creating", aws.ToString(restored.DBCluster.Status))
	waitForRDSClusterAvailable(t, client, ctx, clusterID)
	f.addWriter(clusterID)
	database := f.connect(clusterID).(auroraMySQL)
	rows, err := database.db.QueryContext(ctx, `SELECT item FROM shop.orders ORDER BY id`)
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
	database.exec(t, `INSERT INTO shop.orders VALUES (3, 'cosy')`)

	shopperConfig := mysql.Config{
		User: "shopper", Passwd: "Shopper-Password-1", Net: "tcp", Addr: f.describe(clusterID).endpoint, DBName: "shop",
		TLSConfig: "skip-verify", AllowCleartextPasswords: true,
	}
	shopper, err := sql.Open("mysql", shopperConfig.FormatDSN())
	require.NoError(t, err)
	defer shopper.Close()
	var count int
	require.NoError(t, shopper.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders`).Scan(&count),
		"a user the backup holds signs in under its own password")
	assert.Equal(t, 3, count)
}
