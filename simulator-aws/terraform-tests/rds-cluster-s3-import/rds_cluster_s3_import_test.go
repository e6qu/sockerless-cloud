package rds_cluster_s3_import_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// takeXtraBackup runs a MySQL 8.0 server holding the statements' data and
// returns a Percona XtraBackup of it as an xbstream archive.
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
	docker("run", "-d", "--name", name, "-e", "MYSQL_ROOT_PASSWORD=source-root", "-v", volume+":/var/lib/mysql",
		"public.ecr.aws/docker/library/mysql:8.0")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	// The image's initialisation server listens on no TCP port, so a TCP
	// client reaches only the server that stays; mysqld offers nothing to
	// wait on until it answers one.
	client := []string{"exec", name, "mysql", "--protocol=TCP", "--host=127.0.0.1", "--user=root", "--password=source-root"}
	for exec.Command("docker", append(client, "--execute=SELECT 1")...).Run() != nil {
		time.Sleep(250 * time.Millisecond)
	}
	docker(append(client, "--execute="+strings.Join(statements, "; "))...)
	return docker("run", "--rm", "--user", "root", "--network", "container:"+name, "-v", volume+":/var/lib/mysql:ro",
		"docker.io/percona/percona-xtrabackup:8.0", "xtrabackup", "--backup", "--stream=xbstream", "--user=root",
		"--password=source-root", "--host=127.0.0.1", "--target-dir=/tmp")
}

// terraform-provider-aws creates an Aurora MySQL cluster through s3_import
// from a Percona XtraBackup in Amazon S3, and the cluster serves the backup's
// data under the master credential the configuration names.
func TestRDSClusterS3ImportTerraform(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	env := tfsim.Start(t, ".")
	credentialsProvider := credentials.NewStaticCredentialsProvider("test", "test", "")
	rdsClient := rds.New(rds.Options{Region: "us-east-1", BaseEndpoint: aws.String(env.Endpoint), Credentials: credentialsProvider, HTTPClient: env.Client})
	s3Client := s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(env.Endpoint), Credentials: credentialsProvider, HTTPClient: env.Client, UsePathStyle: true})
	iamClient := iam.New(iam.Options{Region: "us-east-1", BaseEndpoint: aws.String(env.Endpoint), Credentials: credentialsProvider, HTTPClient: env.Client})

	backup := takeXtraBackup(t, "tf-xtrabackup-source",
		"CREATE DATABASE shop",
		"CREATE TABLE shop.orders (id INT PRIMARY KEY, item VARCHAR(32) NOT NULL)",
		"INSERT INTO shop.orders VALUES (1, 'kettle'), (2, 'teapot')")
	bucket := "tf-aurora-s3-import"
	_, err := s3Client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = s3Client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("backups/shop.xbstream"), Body: bytes.NewReader(backup)})
	require.NoError(t, err)
	role, err := iamClient.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String("tf-rds-s3-import"),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Principal":{"Service":"rds.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	_, err = iamClient.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName: aws.String("tf-rds-s3-import"), PolicyName: aws.String("read"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListBucket","s3:GetObject"],` +
			`"Resource":["arn:aws:s3:::` + bucket + `","arn:aws:s3:::` + bucket + `/*"]}]}`),
	})
	require.NoError(t, err)

	vars := []string{"-var", "bucket=" + bucket, "-var", "ingestion_role=" + aws.ToString(role.Role.Arn)}
	env.Terraform(t, "init")
	env.Terraform(t, append([]string{"apply", "-auto-approve"}, vars...)...)
	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	require.Contains(t, outputs["imported_arn"].Value, ":cluster:tf-aurora-s3-import")
	require.Equal(t, "03:00-03:30", outputs["imported_backup_window"].Value)

	instanceID := "tf-aurora-s3-import-1"
	_, err = rdsClient.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID), DBClusterIdentifier: aws.String("tf-aurora-s3-import"),
		Engine: aws.String("aurora-mysql"), DBInstanceClass: aws.String("db.r6g.large"),
	})
	require.NoError(t, err)
	require.NoError(t, rds.NewDBInstanceAvailableWaiter(rdsClient, func(o *rds.DBInstanceAvailableWaiterOptions) {
		o.MinDelay = 250 * time.Millisecond
		o.MaxDelay = 2 * time.Second
	}).Wait(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instanceID)}, 3*time.Minute))
	described, err := rdsClient.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String("tf-aurora-s3-import")})
	require.NoError(t, err)
	cluster := described.DBClusters[0]
	config := mysql.Config{
		User: "dbadmin", Passwd: "MasterPassword-123!", Net: "tcp", DBName: "application",
		Addr:      fmt.Sprintf("%s:%d", aws.ToString(cluster.Endpoint), aws.ToInt32(cluster.Port)),
		TLSConfig: "skip-verify", AllowCleartextPasswords: true,
	}
	db, err := sql.Open("mysql", config.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	var items []string
	rows, err := db.QueryContext(ctx, `SELECT item FROM shop.orders ORDER BY id`)
	require.NoError(t, err)
	for rows.Next() {
		var item string
		require.NoError(t, rows.Scan(&item))
		items = append(items, item)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, []string{"kettle", "teapot"}, items, "the cluster serves the backup's data")

	_, err = rdsClient.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true)})
	require.NoError(t, err)
	require.NoError(t, rds.NewDBInstanceDeletedWaiter(rdsClient, func(o *rds.DBInstanceDeletedWaiterOptions) {
		o.MinDelay = 250 * time.Millisecond
		o.MaxDelay = 2 * time.Second
	}).Wait(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instanceID)}, 3*time.Minute))
	env.Terraform(t, append([]string{"destroy", "-auto-approve"}, vars...)...)
}
