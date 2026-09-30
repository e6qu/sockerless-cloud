package aws_sdk_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Amazon Aurora starts and stops a member only through its cluster and
// deletes a cluster only once its instances are gone.
func TestRDS_AuroraMemberLifecycleFollowsTheCluster(t *testing.T) {
	c := rdsClient()
	clusterID, instanceID := "sdk-aurora-lifecycle", "sdk-aurora-lifecycle-1"
	_, err := c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
		Engine:              aws.String("aurora-postgresql"),
		MasterUsername:      aws.String("dbadmin"),
		MasterUserPassword:  aws.String("MasterPassword-123!"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
		})
		_, _ = c.DeleteDBCluster(context.Background(), &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(clusterID), SkipFinalSnapshot: aws.Bool(true),
		})
	})
	_, err = c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID),
		DBClusterIdentifier:  aws.String(clusterID),
		Engine:               aws.String("aurora-postgresql"),
		DBInstanceClass:      aws.String("db.r6g.large"),
	})
	require.NoError(t, err)

	_, err = c.StopDBInstance(ctx, &rds.StopDBInstanceInput{DBInstanceIdentifier: aws.String(instanceID)})
	var stopFault *rdstypes.InvalidDBClusterStateFault
	require.ErrorAs(t, err, &stopFault)
	_, err = c.StartDBInstance(ctx, &rds.StartDBInstanceInput{DBInstanceIdentifier: aws.String(instanceID)})
	var startFault *rdstypes.InvalidDBClusterStateFault
	require.ErrorAs(t, err, &startFault)
	described, err := c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instanceID)})
	require.NoError(t, err)
	assert.Equal(t, "available", aws.ToString(described.DBInstances[0].DBInstanceStatus))

	_, err = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID), SkipFinalSnapshot: aws.Bool(true),
	})
	var deleteFault *rdstypes.InvalidDBClusterStateFault
	require.ErrorAs(t, err, &deleteFault)
	clusters, err := c.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterID)})
	require.NoError(t, err)
	require.Len(t, clusters.DBClusters[0].DBClusterMembers, 1)
	assert.Equal(t, instanceID, aws.ToString(clusters.DBClusters[0].DBClusterMembers[0].DBInstanceIdentifier))

	_, err = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
	})
	require.NoError(t, err)
	deleted, err := c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID), SkipFinalSnapshot: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, "deleting", aws.ToString(deleted.DBCluster.Status))
}

// An Aurora cluster's writer, reader and instance endpoints serve the one
// cluster volume: a row the writer endpoint commits reads back through the
// reader endpoint and through each instance's own endpoint.
func TestRDS_AuroraEndpointsShareTheClusterVolume(t *testing.T) {
	testContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := rdsClient()
	const (
		username = "dbadmin"
		password = "MasterPassword-123!"
		database = "application"
	)
	createAurora := func(t *testing.T, clusterID, engine string) []string {
		t.Helper()
		created, err := c.CreateDBCluster(testContext, &rds.CreateDBClusterInput{
			DBClusterIdentifier: aws.String(clusterID),
			Engine:              aws.String(engine),
			MasterUsername:      aws.String(username),
			MasterUserPassword:  aws.String(password),
			DatabaseName:        aws.String(database),
		})
		require.NoError(t, err)
		instances := []string{clusterID + "-1", clusterID + "-2"}
		t.Cleanup(func() {
			for _, instanceID := range instances {
				_, _ = c.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
					DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
				})
			}
			_, _ = c.DeleteDBCluster(context.Background(), &rds.DeleteDBClusterInput{
				DBClusterIdentifier: aws.String(clusterID), SkipFinalSnapshot: aws.Bool(true),
			})
		})
		port := aws.ToInt32(created.DBCluster.Port)
		endpoints := []string{
			fmt.Sprintf("%s:%d", aws.ToString(created.DBCluster.Endpoint), port),
			fmt.Sprintf("%s:%d", aws.ToString(created.DBCluster.ReaderEndpoint), port),
		}
		for _, instanceID := range instances {
			instance, err := c.CreateDBInstance(testContext, &rds.CreateDBInstanceInput{
				DBInstanceIdentifier: aws.String(instanceID),
				DBClusterIdentifier:  aws.String(clusterID),
				Engine:               aws.String(engine),
				DBInstanceClass:      aws.String("db.r6g.large"),
			})
			require.NoError(t, err)
			assert.Equal(t, port, aws.ToInt32(instance.DBInstance.Endpoint.Port))
			endpoints = append(endpoints, fmt.Sprintf("%s:%d", aws.ToString(instance.DBInstance.Endpoint.Address), port))
		}
		return endpoints
	}

	t.Run("Aurora PostgreSQL", func(t *testing.T) {
		endpoints := createAurora(t, "sdk-aurora-postgresql", "aurora-postgresql")
		connect := func(endpoint string) *pgx.Conn {
			config, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@%s/%s?sslmode=require", username, endpoint, database))
			require.NoError(t, err)
			config.Password = password
			config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
			connection, err := pgx.ConnectConfig(testContext, config)
			require.NoError(t, err)
			t.Cleanup(func() { _ = connection.Close(context.Background()) })
			return connection
		}
		writer := connect(endpoints[0])
		_, err := writer.Exec(testContext, `CREATE TABLE shared_volume (id integer PRIMARY KEY, value text NOT NULL)`)
		require.NoError(t, err)
		_, err = writer.Exec(testContext, `INSERT INTO shared_volume (id, value) VALUES (1, 'written-through-the-cluster-endpoint')`)
		require.NoError(t, err)
		for _, endpoint := range endpoints[1:] {
			var value string
			require.NoError(t, connect(endpoint).QueryRow(testContext, `SELECT value FROM shared_volume WHERE id = 1`).Scan(&value))
			assert.Equal(t, "written-through-the-cluster-endpoint", value, endpoint)
		}
	})

	t.Run("Aurora MySQL", func(t *testing.T) {
		endpoints := createAurora(t, "sdk-aurora-mysql", "aurora-mysql")
		connect := func(endpoint string) *sql.DB {
			config := mysql.Config{
				User: username, Passwd: password, Net: "tcp", Addr: endpoint, DBName: database,
				TLSConfig: "skip-verify", AllowCleartextPasswords: true,
			}
			connection, err := sql.Open("mysql", config.FormatDSN())
			require.NoError(t, err)
			t.Cleanup(func() { _ = connection.Close() })
			return connection
		}
		writer := connect(endpoints[0])
		_, err := writer.ExecContext(testContext, `CREATE TABLE shared_volume (id integer PRIMARY KEY, value varchar(255) NOT NULL)`)
		require.NoError(t, err)
		_, err = writer.ExecContext(testContext, `INSERT INTO shared_volume (id, value) VALUES (1, 'written-through-the-cluster-endpoint')`)
		require.NoError(t, err)
		for _, endpoint := range endpoints[1:] {
			var value string
			require.NoError(t, connect(endpoint).QueryRowContext(testContext, `SELECT value FROM shared_volume WHERE id = 1`).Scan(&value))
			assert.Equal(t, "written-through-the-cluster-endpoint", value, endpoint)
		}
	})
}
