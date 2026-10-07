package aws_cli_test

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliConnectMySQLAs opens a MySQL-protocol session to an RDS endpoint over
// TLS as user.
func cliConnectMySQLAs(t *testing.T, host string, port int, user, password string) *sql.DB {
	t.Helper()
	config := mysql.Config{
		User: user, Passwd: password, Net: "tcp", Addr: fmt.Sprintf("%s:%d", host, port), DBName: cliRestoreDatabase,
		TLSConfig: "skip-verify", AllowCleartextPasswords: true,
	}
	db, err := sql.Open("mysql", config.FormatDSN())
	require.NoError(t, err)
	db.SetMaxIdleConns(0)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func cliMySQLLedger(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT entry FROM ledger ORDER BY entry`)
	require.NoError(t, err)
	defer rows.Close()
	var entries []string
	for rows.Next() {
		var entry string
		require.NoError(t, rows.Scan(&entry))
		entries = append(entries, entry)
	}
	require.NoError(t, rows.Err())
	return entries
}

// aws rds restore-db-instance-to-point-in-time --restore-time returns an RDS
// for MariaDB instance to the rows its binary log dates before that time.
// MariaDB dates a transaction in whole seconds, so the restore time falls on
// the second the later row was committed in.
func TestRDSCLI_MariaDBInstanceRestoresToAPointInTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sourceID, restoredID := "cli-mariadb-pitr-source", "cli-mariadb-pitr-restored"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", sourceID,
		"--db-instance-class", "db.t3.micro",
		"--engine", "mariadb",
		"--allocated-storage", "20",
		"--master-username", cliRestoreUsername,
		"--master-user-password", cliRestorePassword,
		"--db-name", cliRestoreDatabase,
		"--backup-retention-period", "1"))
	cliCleanupDBInstance(t, sourceID)
	source := cliAvailableDBInstance(t, sourceID)
	db := cliConnectMySQLAs(t, source.Endpoint.Address, source.Endpoint.Port, cliRestoreUsername, cliRestorePassword)
	for _, statement := range []string{
		`CREATE TABLE ledger (entry varchar(64) NOT NULL)`,
		`INSERT INTO ledger VALUES ('before-restore-time')`,
	} {
		_, err := db.ExecContext(ctx, statement)
		require.NoError(t, err, statement)
	}
	var next int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT FLOOR(UNIX_TIMESTAMP(NOW(6))) + 1`).Scan(&next))
	_, err := db.ExecContext(ctx, fmt.Sprintf(`DO SLEEP(GREATEST(0, %d - UNIX_TIMESTAMP(NOW(6))))`, next))
	require.NoError(t, err)
	restoreTo := time.Unix(next, 0).UTC()
	_, err = db.ExecContext(ctx, `INSERT INTO ledger VALUES ('after-restore-time')`)
	require.NoError(t, err)

	described := cliAvailableDBInstance(t, sourceID)
	require.NotEmpty(t, described.LatestRestorableTime, "an instance that keeps automated backups reports LatestRestorableTime")

	out := runCLI(t, awsCLI("rds", "restore-db-instance-to-point-in-time",
		"--source-db-instance-identifier", sourceID,
		"--target-db-instance-identifier", restoredID,
		"--restore-time", restoreTo.Format(time.RFC3339)))
	cliCleanupDBInstance(t, restoredID)
	var restored struct {
		DBInstance cliDBInstance `json:"DBInstance"`
	}
	parseJSON(t, out, &restored)
	assert.Equal(t, "creating", restored.DBInstance.DBInstanceStatus)
	target := cliAvailableDBInstance(t, restoredID)
	assert.Equal(t, []string{"before-restore-time"},
		cliMySQLLedger(t, ctx, cliConnectMySQLAs(t, target.Endpoint.Address, target.Endpoint.Port, cliRestoreUsername, cliRestorePassword)),
		"the restored instance holds the rows dated before the restore time and none after it")
}

// On RDS for MySQL and RDS for MariaDB a user identified with
// AWSAuthenticationPlugin signs in with the token aws rds
// generate-db-auth-token makes for it, as itself, and never with a password;
// a token for any other user, the master user's included, is refused.
func TestRDSCLI_MySQLIAMDatabaseUsers(t *testing.T) {
	for _, engine := range []string{"mysql", "mariadb"} {
		t.Run(engine, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			instanceID := "cli-iam-users-" + engine
			runCLI(t, awsCLI("rds", "create-db-instance",
				"--db-instance-identifier", instanceID,
				"--db-instance-class", "db.t3.micro",
				"--engine", engine,
				"--allocated-storage", "20",
				"--master-username", cliRestoreUsername,
				"--master-user-password", cliRestorePassword,
				"--db-name", cliRestoreDatabase,
				"--enable-iam-database-authentication",
				"--backup-retention-period", "0"))
			cliCleanupDBInstance(t, instanceID)
			instance := cliAvailableDBInstance(t, instanceID)
			host, port := instance.Endpoint.Address, instance.Endpoint.Port
			master := cliConnectMySQLAs(t, host, port, cliRestoreUsername, cliRestorePassword)
			for _, statement := range []string{
				`CREATE TABLE ledger (entry varchar(64) NOT NULL)`,
				`INSERT INTO ledger VALUES ('written-by-the-master-user')`,
				`CREATE USER 'ledger_iam'@'%' IDENTIFIED WITH AWSAuthenticationPlugin AS 'RDS'`,
				`GRANT SELECT ON application.ledger TO 'ledger_iam'@'%'`,
			} {
				_, err := master.ExecContext(ctx, statement)
				require.NoError(t, err, statement)
			}
			token := func(user string) string {
				return strings.TrimSpace(runCLI(t, awsCLI("rds", "generate-db-auth-token",
					"--hostname", host, "--port", strconv.Itoa(port), "--username", user)))
			}
			refused := func(user, password, reason string) {
				t.Helper()
				err := cliConnectMySQLAs(t, host, port, user, password).PingContext(ctx)
				var refusal *mysql.MySQLError
				require.ErrorAs(t, err, &refusal, reason)
				assert.Equal(t, uint16(1045), refusal.Number, reason)
			}

			var user string
			require.NoError(t, cliConnectMySQLAs(t, host, port, "ledger_iam", token("ledger_iam")).
				QueryRowContext(ctx, `SELECT CURRENT_USER()`).Scan(&user))
			assert.Equal(t, "ledger_iam@%", user, "the session runs as the user the token names")
			assert.Equal(t, []string{"written-by-the-master-user"},
				cliMySQLLedger(t, ctx, cliConnectMySQLAs(t, host, port, "ledger_iam", token("ledger_iam"))))
			refused("ledger_iam", "Ledger-Password-1", "a user identified with AWSAuthenticationPlugin does not sign in with a password")
			refused("ledger_iam", "", "a user identified with AWSAuthenticationPlugin does not sign in without a token")
			refused(cliRestoreUsername, token(cliRestoreUsername), "the master user is not identified with AWSAuthenticationPlugin")
		})
	}
}
