package aws_sdk_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	rdsauth "github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRDSNativeDataPlanesWithIAMAuthentication_SDK creates database instances
// through the official Amazon RDS SDK, generates their IAM database
// authentication tokens through AWS's official signer, and then executes
// schema/write/read transactions through stock PostgreSQL and MySQL drivers.
// The driver connections are the independent data-plane oracle; the simulator
// test does not call an internal database endpoint. Each instance signs in the
// users its engine holds under their own passwords, and RDS for PostgreSQL
// signs in a user granted rds_iam only with an IAM authentication token.
func TestRDSNativeDataPlanesWithIAMAuthentication_SDK(t *testing.T) {
	testContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rdsAPI := rdsClient()
	iamAPI := iamClient()
	const (
		iamUser   = "rds-database-client"
		iamPolicy = "rds-database-connect"
	)
	_, err := iamAPI.CreateUser(testContext, &iam.CreateUserInput{UserName: aws.String(iamUser)})
	require.NoError(t, err)
	accessKey, err := iamAPI.CreateAccessKey(testContext, &iam.CreateAccessKeyInput{UserName: aws.String(iamUser)})
	require.NoError(t, err)
	accessKeyID := aws.ToString(accessKey.AccessKey.AccessKeyId)
	secretAccessKey := aws.ToString(accessKey.AccessKey.SecretAccessKey)
	t.Cleanup(func() {
		_, _ = iamAPI.DeleteUserPolicy(context.Background(), &iam.DeleteUserPolicyInput{
			UserName: aws.String(iamUser), PolicyName: aws.String(iamPolicy),
		})
		_, _ = iamAPI.DeleteAccessKey(context.Background(), &iam.DeleteAccessKeyInput{
			UserName: aws.String(iamUser), AccessKeyId: aws.String(accessKeyID),
		})
		_, _ = iamAPI.DeleteUser(context.Background(), &iam.DeleteUserInput{UserName: aws.String(iamUser)})
	})
	credentialProvider := credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")

	t.Run("PostgreSQL", func(t *testing.T) {
		const (
			instanceID = "rds-postgresql-wire"
			username   = "dbadmin"
			database   = "application"
		)
		created, err := rdsAPI.CreateDBInstance(testContext, &rds.CreateDBInstanceInput{
			DBInstanceIdentifier:            aws.String(instanceID),
			DBInstanceClass:                 aws.String("db.t3.micro"),
			Engine:                          aws.String("postgres"),
			AllocatedStorage:                aws.Int32(20),
			MasterUsername:                  aws.String(username),
			MasterUserPassword:              aws.String("MasterPassword-123!"),
			DBName:                          aws.String(database),
			EnableIAMDatabaseAuthentication: aws.Bool(false),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = rdsAPI.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
				DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
			})
		})
		waitForRDSInstanceAvailable(t, rdsAPI, testContext, instanceID)
		endpoint := fmt.Sprintf("%s:%d", aws.ToString(created.DBInstance.Endpoint.Address), aws.ToInt32(created.DBInstance.Endpoint.Port))
		pgConfig := func(user, password, sslMode string) *pgx.ConnConfig {
			config, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@%s/%s?sslmode=%s", user, endpoint, database, sslMode))
			require.NoError(t, err)
			config.Password = password
			if sslMode == "require" {
				config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
			}
			return config
		}
		signIn := func(user, password string) (*pgx.Conn, error) {
			connection, err := pgx.ConnectConfig(testContext, pgConfig(user, password, "require"))
			if err == nil {
				t.Cleanup(func() { _ = connection.Close(context.Background()) })
			}
			return connection, err
		}
		refused := func(user, password, reason string) {
			t.Helper()
			_, err := signIn(user, password)
			var refusal *pgconn.PgError
			require.ErrorAs(t, err, &refusal, reason)
			assert.Equal(t, "28P01", refusal.Code, reason)
		}
		token := func(user string) string {
			t.Helper()
			token, err := rdsauth.BuildAuthToken(testContext, endpoint, "us-east-1", user, credentialProvider)
			require.NoError(t, err)
			return token
		}

		master, err := signIn(username, "MasterPassword-123!")
		require.NoError(t, err)
		for _, statement := range []string{
			`CREATE TABLE fidelity (id integer PRIMARY KEY, value text NOT NULL)`,
			`INSERT INTO fidelity (id, value) VALUES (1, 'postgres-real-engine')`,
			`CREATE USER reader PASSWORD 'Reader-Password-1'`,
			`GRANT SELECT ON fidelity TO reader`,
			`CREATE USER iam_writer`,
			`GRANT rds_iam TO iam_writer`,
			`GRANT SELECT, INSERT ON fidelity TO iam_writer`,
		} {
			_, err := master.Exec(testContext, statement)
			require.NoError(t, err, statement)
		}

		reader, err := signIn("reader", "Reader-Password-1")
		require.NoError(t, err, "a user created with CREATE USER ... PASSWORD signs in under its own password")
		var user, value string
		require.NoError(t, reader.QueryRow(testContext, `SELECT current_user, value FROM fidelity WHERE id = 1`).Scan(&user, &value))
		assert.Equal(t, "reader", user)
		assert.Equal(t, "postgres-real-engine", value)
		_, err = reader.Exec(testContext, `INSERT INTO fidelity (id, value) VALUES (2, 'written-by-the-reader')`)
		var denied *pgconn.PgError
		require.ErrorAs(t, err, &denied, "the session holds only the user's own privileges")
		assert.Equal(t, "42501", denied.Code)
		refused("reader", "Wrong-Password-1", "a wrong password is refused")
		refused("no_such_user", "Reader-Password-1", "a user the engine does not hold is refused")

		refused("iam_writer", token("iam_writer"), "IAM database authentication must be denied while the instance setting is disabled")
		modified, err := rdsAPI.ModifyDBInstance(testContext, &rds.ModifyDBInstanceInput{
			DBInstanceIdentifier:            aws.String(instanceID),
			EnableIAMDatabaseAuthentication: aws.Bool(true),
			ApplyImmediately:                aws.Bool(true),
		})
		require.NoError(t, err)
		require.True(t, aws.ToBool(modified.DBInstance.IAMDatabaseAuthenticationEnabled))
		refused("iam_writer", token("iam_writer"), "a valid IAM database token without rds-db:connect must be denied")
		_, err = iamAPI.PutUserPolicy(testContext, &iam.PutUserPolicyInput{
			UserName:   aws.String(iamUser),
			PolicyName: aws.String(iamPolicy),
			PolicyDocument: aws.String(
				`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"rds-db:connect","Resource":"*"}]}`,
			),
		})
		require.NoError(t, err)
		_, err = pgx.ConnectConfig(testContext, pgConfig("iam_writer", token("iam_writer"), "disable"))
		require.Error(t, err, "IAM database authentication must require TLS")
		refused(username, token(username), "a user not granted rds_iam cannot sign in with a token")
		refused("reader", token("reader"), "a user not granted rds_iam cannot sign in with a token")
		refused("iam_writer", "", "a user granted rds_iam signs in only with a token")

		writer, err := signIn("iam_writer", token("iam_writer"))
		require.NoError(t, err, "a user granted rds_iam signs in with an IAM authentication token")
		require.NoError(t, writer.QueryRow(testContext, `SELECT current_user`).Scan(&user))
		assert.Equal(t, "iam_writer", user, "the session runs as the user the token names")
		_, err = writer.Exec(testContext, `INSERT INTO fidelity (id, value) VALUES (2, 'written-by-iam')`)
		require.NoError(t, err)
		require.NoError(t, master.QueryRow(testContext, `SELECT value FROM fidelity WHERE id = 2`).Scan(&value))
		assert.Equal(t, "written-by-iam", value)
	})

	t.Run("MySQL", func(t *testing.T) {
		const (
			instanceID = "rds-mysql-wire"
			username   = "dbadmin"
			database   = "application"
		)
		created, err := rdsAPI.CreateDBInstance(testContext, &rds.CreateDBInstanceInput{
			DBInstanceIdentifier:            aws.String(instanceID),
			DBInstanceClass:                 aws.String("db.t3.micro"),
			Engine:                          aws.String("mysql"),
			AllocatedStorage:                aws.Int32(20),
			MasterUsername:                  aws.String(username),
			MasterUserPassword:              aws.String("MasterPassword-123!"),
			DBName:                          aws.String(database),
			EnableIAMDatabaseAuthentication: aws.Bool(true),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = rdsAPI.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
				DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
			})
		})
		waitForRDSInstanceAvailable(t, rdsAPI, testContext, instanceID)
		endpoint := fmt.Sprintf("%s:%d", aws.ToString(created.DBInstance.Endpoint.Address), aws.ToInt32(created.DBInstance.Endpoint.Port))
		token, err := rdsauth.BuildAuthToken(testContext, endpoint, "us-east-1", username, credentialProvider)
		require.NoError(t, err)
		config := mysql.Config{
			User: username, Passwd: token, Net: "tcp", Addr: endpoint, DBName: database,
			TLSConfig: "skip-verify", AllowCleartextPasswords: true,
		}
		databaseConnection, err := sql.Open("mysql", config.FormatDSN())
		require.NoError(t, err)
		defer databaseConnection.Close()
		require.NoError(t, databaseConnection.PingContext(testContext))

		_, err = databaseConnection.ExecContext(testContext, `CREATE TABLE fidelity (id integer PRIMARY KEY, value varchar(255) NOT NULL)`)
		require.NoError(t, err)
		_, err = databaseConnection.ExecContext(testContext, `INSERT INTO fidelity (id, value) VALUES (1, 'mysql-real-engine')`)
		require.NoError(t, err)
		var value string
		require.NoError(t, databaseConnection.QueryRowContext(testContext, `SELECT value FROM fidelity WHERE id = 1`).Scan(&value))
		assert.Equal(t, "mysql-real-engine", value)

		const rotatedPassword = "RotatedPassword-456!"
		_, err = rdsAPI.ModifyDBInstance(testContext, &rds.ModifyDBInstanceInput{
			DBInstanceIdentifier: aws.String(instanceID),
			MasterUserPassword:   aws.String(rotatedPassword),
			ApplyImmediately:     aws.Bool(true),
		})
		require.NoError(t, err)
		oldPasswordConfig := mysql.Config{
			User: username, Passwd: "MasterPassword-123!", Net: "tcp", Addr: endpoint, DBName: database,
			TLSConfig: "skip-verify", AllowCleartextPasswords: true,
		}
		oldPasswordConnection, err := sql.Open("mysql", oldPasswordConfig.FormatDSN())
		require.NoError(t, err)
		require.Error(t, oldPasswordConnection.PingContext(testContext), "the previous master password must stop authenticating")
		_ = oldPasswordConnection.Close()
		rotatedConfig := oldPasswordConfig
		rotatedConfig.Passwd = rotatedPassword
		rotatedConnection, err := sql.Open("mysql", rotatedConfig.FormatDSN())
		require.NoError(t, err)
		defer rotatedConnection.Close()
		require.NoError(t, rotatedConnection.PingContext(testContext))
		require.NoError(t, rotatedConnection.QueryRowContext(testContext, `SELECT value FROM fidelity WHERE id = 1`).Scan(&value))
		assert.Equal(t, "mysql-real-engine", value, "password rotation must preserve the real database volume")

		for _, statement := range []string{
			`CREATE USER 'reader'@'%' IDENTIFIED BY 'Reader-Password-1'`,
			`GRANT SELECT ON application.fidelity TO 'reader'@'%'`,
		} {
			_, err := rotatedConnection.ExecContext(testContext, statement)
			require.NoError(t, err, statement)
		}
		signIn := func(user, password string) (*sql.DB, error) {
			config := rotatedConfig
			config.User, config.Passwd = user, password
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
		reader, err := signIn("reader", "Reader-Password-1")
		require.NoError(t, err, "a user created with CREATE USER ... IDENTIFIED BY signs in under its own password")
		var user string
		require.NoError(t, reader.QueryRowContext(testContext, `SELECT CURRENT_USER(), value FROM fidelity WHERE id = 1`).Scan(&user, &value))
		assert.Equal(t, "reader@%", user)
		assert.Equal(t, "mysql-real-engine", value)
		_, err = reader.ExecContext(testContext, `INSERT INTO fidelity (id, value) VALUES (2, 'written-by-the-reader')`)
		var denied *mysql.MySQLError
		require.ErrorAs(t, err, &denied, "the session holds only the user's own privileges")
		assert.Equal(t, uint16(1142), denied.Number)
		refused("reader", "Wrong-Password-1")
		refused("no_such_user", "Reader-Password-1")
		refused("root", rotatedPassword)
	})

	t.Run("MariaDB stopped password change", func(t *testing.T) {
		const (
			instanceID      = "rds-mariadb-wire"
			username        = "dbadmin"
			database        = "application"
			initialPassword = "MasterPassword-123!"
			rotatedPassword = "StoppedRotation-789!"
		)
		created, err := rdsAPI.CreateDBInstance(testContext, &rds.CreateDBInstanceInput{
			DBInstanceIdentifier: aws.String(instanceID),
			DBInstanceClass:      aws.String("db.t3.micro"),
			Engine:               aws.String("mariadb"),
			AllocatedStorage:     aws.Int32(20),
			MasterUsername:       aws.String(username),
			MasterUserPassword:   aws.String(initialPassword),
			DBName:               aws.String(database),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = rdsAPI.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
				DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
			})
		})
		waitForRDSInstanceAvailable(t, rdsAPI, testContext, instanceID)
		endpoint := fmt.Sprintf("%s:%d", aws.ToString(created.DBInstance.Endpoint.Address), aws.ToInt32(created.DBInstance.Endpoint.Port))
		config := mysql.Config{
			User: username, Passwd: initialPassword, Net: "tcp", Addr: endpoint, DBName: database,
			TLSConfig: "skip-verify", AllowCleartextPasswords: true,
		}
		connection, err := sql.Open("mysql", config.FormatDSN())
		require.NoError(t, err)
		require.NoError(t, connection.PingContext(testContext))
		_, err = connection.ExecContext(testContext, `CREATE TABLE fidelity (id integer PRIMARY KEY, value varchar(255) NOT NULL)`)
		require.NoError(t, err)
		_, err = connection.ExecContext(testContext, `INSERT INTO fidelity (id, value) VALUES (1, 'mariadb-real-engine')`)
		require.NoError(t, err)
		require.NoError(t, connection.Close())

		stopped, err := rdsAPI.StopDBInstance(testContext, &rds.StopDBInstanceInput{
			DBInstanceIdentifier: aws.String(instanceID),
		})
		require.NoError(t, err)
		require.Equal(t, "stopping", aws.ToString(stopped.DBInstance.DBInstanceStatus))
		waitForRDSInstanceStatus(t, rdsAPI, testContext, instanceID, "stopped")
		stoppedConnection, err := sql.Open("mysql", config.FormatDSN())
		require.NoError(t, err)
		require.Error(t, stoppedConnection.PingContext(testContext), "a stopped instance's endpoint must refuse clients")
		require.NoError(t, stoppedConnection.Close())
		_, err = rdsAPI.ModifyDBInstance(testContext, &rds.ModifyDBInstanceInput{
			DBInstanceIdentifier: aws.String(instanceID),
			MasterUserPassword:   aws.String(rotatedPassword),
			ApplyImmediately:     aws.Bool(true),
		})
		require.NoError(t, err)
		started, err := rdsAPI.StartDBInstance(testContext, &rds.StartDBInstanceInput{
			DBInstanceIdentifier: aws.String(instanceID),
		})
		require.NoError(t, err)
		require.Equal(t, "starting", aws.ToString(started.DBInstance.DBInstanceStatus))
		waitForRDSInstanceAvailable(t, rdsAPI, testContext, instanceID)
		endpoint = fmt.Sprintf("%s:%d", aws.ToString(started.DBInstance.Endpoint.Address), aws.ToInt32(started.DBInstance.Endpoint.Port))

		oldConfig := config
		oldConfig.Addr = endpoint
		oldConnection, err := sql.Open("mysql", oldConfig.FormatDSN())
		require.NoError(t, err)
		require.Error(t, oldConnection.PingContext(testContext), "the stopped instance must apply its pending password change on start")
		require.NoError(t, oldConnection.Close())

		rotatedConfig := oldConfig
		rotatedConfig.Passwd = rotatedPassword
		rotatedConnection, err := sql.Open("mysql", rotatedConfig.FormatDSN())
		require.NoError(t, err)
		defer rotatedConnection.Close()
		require.NoError(t, rotatedConnection.PingContext(testContext))
		var value string
		require.NoError(t, rotatedConnection.QueryRowContext(testContext, `SELECT value FROM fidelity WHERE id = 1`).Scan(&value))
		assert.Equal(t, "mariadb-real-engine", value, "stopping and starting must preserve the real MariaDB volume")
	})
}
