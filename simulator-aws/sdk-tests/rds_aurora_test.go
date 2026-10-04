package aws_sdk_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	rdsauth "github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
// reader endpoint and through each instance's own endpoint. The writer's
// endpoints write; the reader endpoint and the Aurora Replica's instance
// endpoint refuse a write with the engine's read-only transaction error.
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

		_, err = connect(endpoints[2]).Exec(testContext, `INSERT INTO shared_volume (id, value) VALUES (2, 'written-through-the-writer-instance')`)
		require.NoError(t, err)
		for _, endpoint := range []string{endpoints[1], endpoints[3]} {
			_, err := connect(endpoint).Exec(testContext, `INSERT INTO shared_volume (id, value) VALUES (3, 'written-through-a-replica')`)
			var refusal *pgconn.PgError
			require.ErrorAs(t, err, &refusal, endpoint)
			assert.Equal(t, "25006", refusal.Code, endpoint)
			assert.Equal(t, "cannot execute INSERT in a read-only transaction", refusal.Message, endpoint)
		}
		var rows int
		require.NoError(t, writer.QueryRow(testContext, `SELECT count(*) FROM shared_volume`).Scan(&rows))
		assert.Equal(t, 2, rows)
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

		_, err = connect(endpoints[2]).ExecContext(testContext, `INSERT INTO shared_volume (id, value) VALUES (2, 'written-through-the-writer-instance')`)
		require.NoError(t, err)
		for _, endpoint := range []string{endpoints[1], endpoints[3]} {
			_, err := connect(endpoint).ExecContext(testContext, `INSERT INTO shared_volume (id, value) VALUES (3, 'written-through-a-replica')`)
			var refusal *mysql.MySQLError
			require.ErrorAs(t, err, &refusal, endpoint)
			assert.Equal(t, uint16(1792), refusal.Number, endpoint)
			assert.Equal(t, "25006", string(refusal.SQLState[:]), endpoint)
			assert.Equal(t, "Cannot execute statement in a READ ONLY transaction.", refusal.Message, endpoint)
		}
		var rows int
		require.NoError(t, writer.QueryRowContext(testContext, `SELECT count(*) FROM shared_volume`).Scan(&rows))
		assert.Equal(t, 2, rows)
	})
}

// An Aurora cluster's endpoints sign in the database users the engine holds,
// each under its own password and into a session with its own privileges. A
// user granted rds_iam on Aurora PostgreSQL signs in only with an IAM
// authentication token, which no other user can sign in with.
func TestRDS_AuroraDatabaseUsersSignInThroughTheEngine(t *testing.T) {
	testContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	const (
		appUser     = "app_user"
		appPassword = "App-Password-1"
		iamUser     = "iam_user"
	)
	createCluster := func(t *testing.T, clusterID, engine, family string) string {
		t.Helper()
		f := auroraClusterFixture{t: t, ctx: testContext, client: rdsClient(), engine: engine, family: family}
		_, err := f.client.CreateDBCluster(testContext, &rds.CreateDBClusterInput{
			DBClusterIdentifier:             aws.String(clusterID),
			Engine:                          aws.String(engine),
			MasterUsername:                  aws.String(restoreSourceUsername),
			MasterUserPassword:              aws.String(restoreSourcePassword),
			DatabaseName:                    aws.String(restoreSourceDatabase),
			EnableIAMDatabaseAuthentication: aws.Bool(true),
		})
		require.NoError(t, err)
		f.cleanupCluster(clusterID)
		f.addWriter(clusterID)
		return f.describe(clusterID).endpoint
	}

	t.Run("Aurora PostgreSQL", func(t *testing.T) {
		endpoint := createCluster(t, "sdk-aurora-users-postgresql", "aurora-postgresql", "postgres")
		signIn := func(user, password string) (*pgx.Conn, error) {
			config, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@%s/%s?sslmode=require", user, endpoint, restoreSourceDatabase))
			require.NoError(t, err)
			config.Password = password
			config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
			connection, err := pgx.ConnectConfig(testContext, config)
			if err == nil {
				t.Cleanup(func() { _ = connection.Close(context.Background()) })
			}
			return connection, err
		}
		refused := func(user, password string) {
			t.Helper()
			_, err := signIn(user, password)
			var refusal *pgconn.PgError
			require.ErrorAs(t, err, &refusal, user)
			assert.Equal(t, "28P01", refusal.Code, user)
		}
		master, err := signIn(restoreSourceUsername, restoreSourcePassword)
		require.NoError(t, err)
		for _, statement := range []string{
			`CREATE TABLE ledger (entry text PRIMARY KEY)`,
			`INSERT INTO ledger VALUES ('written-by-the-master-user')`,
			fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, appUser, appPassword),
			fmt.Sprintf(`GRANT SELECT ON ledger TO %s`, appUser),
			fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'Iam-Password-1'`, iamUser),
			fmt.Sprintf(`GRANT rds_iam TO %s`, iamUser),
		} {
			_, err := master.Exec(testContext, statement)
			require.NoError(t, err, statement)
		}

		app, err := signIn(appUser, appPassword)
		require.NoError(t, err, "a role the engine holds signs in under its own password")
		var user, entry string
		require.NoError(t, app.QueryRow(testContext, `SELECT current_user, entry FROM ledger`).Scan(&user, &entry))
		assert.Equal(t, appUser, user)
		assert.Equal(t, "written-by-the-master-user", entry)
		_, err = app.Exec(testContext, `INSERT INTO ledger VALUES ('written-by-the-app-user')`)
		var denied *pgconn.PgError
		require.ErrorAs(t, err, &denied, "the session holds only the role's own privileges")
		assert.Equal(t, "42501", denied.Code)
		refused(appUser, "Wrong-Password-1")
		refused("no_such_role", appPassword)
		refused(iamUser, "Iam-Password-1")

		token := func(user string) string {
			t.Helper()
			token, err := rdsauth.BuildAuthToken(testContext, endpoint, "us-east-1", user, sdkConfig().Credentials)
			require.NoError(t, err)
			return token
		}
		iam, err := signIn(iamUser, token(iamUser))
		require.NoError(t, err, "a role granted rds_iam signs in with an IAM authentication token")
		require.NoError(t, iam.QueryRow(testContext, `SELECT current_user`).Scan(&user))
		assert.Equal(t, iamUser, user)
		refused(appUser, token(appUser))
	})

	t.Run("Aurora MySQL", func(t *testing.T) {
		endpoint := createCluster(t, "sdk-aurora-users-mysql", "aurora-mysql", "mysql")
		signIn := func(user, password string) (*sql.DB, error) {
			config := mysql.Config{
				User: user, Passwd: password, Net: "tcp", Addr: endpoint, DBName: restoreSourceDatabase,
				TLSConfig: "skip-verify", AllowCleartextPasswords: true,
			}
			connection, err := sql.Open("mysql", config.FormatDSN())
			require.NoError(t, err)
			t.Cleanup(func() { _ = connection.Close() })
			return connection, connection.PingContext(testContext)
		}
		refused := func(user, password string) {
			t.Helper()
			_, err := signIn(user, password)
			var refusal *mysql.MySQLError
			require.ErrorAs(t, err, &refusal, user)
			assert.Equal(t, uint16(1045), refusal.Number, user)
		}
		master, err := signIn(restoreSourceUsername, restoreSourcePassword)
		require.NoError(t, err)
		for _, statement := range []string{
			`CREATE TABLE ledger (entry varchar(64) PRIMARY KEY)`,
			`INSERT INTO ledger VALUES ('written-by-the-master-user')`,
			fmt.Sprintf(`CREATE USER '%s'@'%%' IDENTIFIED BY '%s'`, appUser, appPassword),
			fmt.Sprintf(`GRANT SELECT ON %s.ledger TO '%s'@'%%'`, restoreSourceDatabase, appUser),
		} {
			_, err := master.ExecContext(testContext, statement)
			require.NoError(t, err, statement)
		}

		app, err := signIn(appUser, appPassword)
		require.NoError(t, err, "a user the engine holds signs in under its own password")
		var user, entry string
		require.NoError(t, app.QueryRowContext(testContext, `SELECT CURRENT_USER(), entry FROM ledger`).Scan(&user, &entry))
		assert.Equal(t, appUser+"@%", user)
		assert.Equal(t, "written-by-the-master-user", entry)
		_, err = app.ExecContext(testContext, `INSERT INTO ledger VALUES ('written-by-the-app-user')`)
		var denied *mysql.MySQLError
		require.ErrorAs(t, err, &denied, "the session holds only the user's own privileges")
		assert.Equal(t, uint16(1142), denied.Number)
		refused(appUser, "Wrong-Password-1")
		refused("no_such_user", appPassword)
		refused("root", restoreSourcePassword)
	})
}
