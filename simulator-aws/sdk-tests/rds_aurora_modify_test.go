package aws_sdk_test

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ModifyDBCluster changes an Aurora cluster's master password in its engine,
// whether the engine has started yet or not, and moves the cluster's
// endpoints to a new port with the data on the cluster volume intact.
func TestRDS_AuroraModifyClusterRotatesPasswordAndMovesPort(t *testing.T) {
	testContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := rdsClient()
	clusterID := uniqueName("sdk-aurora-modify")
	instanceID := clusterID + "-1"
	const (
		username = "dbadmin"
		database = "application"
	)
	created, err := c.CreateDBCluster(testContext, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
		Engine:              aws.String("aurora-mysql"),
		MasterUsername:      aws.String(username),
		MasterUserPassword:  aws.String("Created-Password-0"),
		DatabaseName:        aws.String(database),
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
	instance, err := c.CreateDBInstance(testContext, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID),
		DBClusterIdentifier:  aws.String(clusterID),
		Engine:               aws.String("aurora-mysql"),
		DBInstanceClass:      aws.String("db.r6g.large"),
	})
	require.NoError(t, err)
	writerHost := aws.ToString(created.DBCluster.Endpoint)
	instanceHost := aws.ToString(instance.DBInstance.Endpoint.Address)
	port := aws.ToInt32(created.DBCluster.Port)

	open := func(host string, port int32, password string) *sql.DB {
		config := mysql.Config{
			User: username, Passwd: password, Net: "tcp", Addr: net.JoinHostPort(host, fmt.Sprint(port)), DBName: database,
			TLSConfig: "skip-verify", AllowCleartextPasswords: true, Timeout: 30 * time.Second,
		}
		connection, err := sql.Open("mysql", config.FormatDSN())
		require.NoError(t, err)
		t.Cleanup(func() { _ = connection.Close() })
		return connection
	}
	modify := func(input *rds.ModifyDBClusterInput) *rds.ModifyDBClusterOutput {
		input.DBClusterIdentifier = aws.String(clusterID)
		input.ApplyImmediately = aws.Bool(true)
		out, err := c.ModifyDBCluster(testContext, input)
		require.NoError(t, err)
		return out
	}

	// The engine has not run yet: it starts on the first connection with the
	// password the cluster was created with and installs the new one.
	modify(&rds.ModifyDBClusterInput{MasterUserPassword: aws.String("Pending-Password-1")})
	writer := open(writerHost, port, "Pending-Password-1")
	_, err = writer.ExecContext(testContext, `CREATE TABLE kept (id integer PRIMARY KEY, value varchar(64) NOT NULL)`)
	require.NoError(t, err)
	_, err = writer.ExecContext(testContext, `INSERT INTO kept (id, value) VALUES (1, 'survives-every-modify')`)
	require.NoError(t, err)
	require.Error(t, open(writerHost, port, "Created-Password-0").PingContext(testContext),
		"the password the cluster was created with no longer signs in")

	// Two rotations in a row against the running engine.
	for _, password := range []string{"Rotated-Password-2", "Rotated-Password-3"} {
		modify(&rds.ModifyDBClusterInput{MasterUserPassword: aws.String(password)})
		require.NoError(t, open(writerHost, port, password).PingContext(testContext), password)
	}
	require.Error(t, open(writerHost, port, "Rotated-Password-2").PingContext(testContext))

	newPort := port + 11
	moved := modify(&rds.ModifyDBClusterInput{Port: aws.Int32(newPort)})
	assert.Equal(t, newPort, aws.ToInt32(moved.DBCluster.Port))
	described, err := c.DescribeDBInstances(testContext, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instanceID)})
	require.NoError(t, err)
	assert.Equal(t, newPort, aws.ToInt32(described.DBInstances[0].Endpoint.Port), "the member follows the cluster's port")
	for _, host := range []string{writerHost, instanceHost} {
		var value string
		require.NoError(t, open(host, newPort, "Rotated-Password-3").QueryRowContext(testContext,
			`SELECT value FROM kept WHERE id = 1`).Scan(&value), host)
		assert.Equal(t, "survives-every-modify", value, host)
		_, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(port)), 5*time.Second)
		assert.Error(t, err, "%s no longer listens on the old port", host)
	}

	_, err = c.ModifyDBCluster(testContext, &rds.ModifyDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID), MasterUserPassword: aws.String("has/slash"),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")
	_, err = c.ModifyDBCluster(testContext, &rds.ModifyDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID), Port: aws.Int32(80),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")
}
