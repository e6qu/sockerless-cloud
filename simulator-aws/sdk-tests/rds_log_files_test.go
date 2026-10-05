package aws_sdk_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/smithy-go"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rdsDownloadWholeLogFile reads a log file from its start through the SDK's
// paginator, the way `aws rds download-db-log-file-portion --starting-token 0`
// does.
func rdsDownloadWholeLogFile(ctx context.Context, client *rds.Client, instanceID, name string) (string, error) {
	pages := rds.NewDownloadDBLogFilePortionPaginator(client, &rds.DownloadDBLogFilePortionInput{
		DBInstanceIdentifier: aws.String(instanceID),
		LogFileName:          aws.String(name),
		Marker:               aws.String("0"),
	}, func(o *rds.DownloadDBLogFilePortionPaginatorOptions) {
		o.Limit = 5
		o.StopOnDuplicateToken = true
	})
	var whole strings.Builder
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return "", err
		}
		whole.WriteString(aws.ToString(page.LogFileData))
	}
	return whole.String(), nil
}

// rdsAwaitLogFileHolding waits until one of the instance's log files holds
// text and returns its name. No waiter covers log delivery, so it re-reads the
// listing.
func rdsAwaitLogFileHolding(ctx context.Context, t *testing.T, client *rds.Client, instanceID, text string) string {
	t.Helper()
	var holding string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		listed, err := client.DescribeDBLogFiles(ctx, &rds.DescribeDBLogFilesInput{DBInstanceIdentifier: aws.String(instanceID)})
		if !assert.NoError(c, err) {
			return
		}
		var seen []string
		for _, file := range listed.DescribeDBLogFiles {
			name := aws.ToString(file.LogFileName)
			whole, err := rdsDownloadWholeLogFile(ctx, client, instanceID, name)
			if !assert.NoError(c, err) {
				return
			}
			if strings.Contains(whole, text) {
				holding = name
				return
			}
			seen = append(seen, fmt.Sprintf("%s (%d bytes):\n%s", name, len(whole), whole))
		}
		assert.Fail(c, "no log file holds the line", "want %q in one of:\n%s", text, strings.Join(seen, "\n"))
	}, time.Minute, 200*time.Millisecond)
	return holding
}

// TestRDSLogFilesServeTheEngineOutput_SDK finds a line the real engine logged
// behind an Amazon RDS instance in the log files DescribeDBLogFiles lists and
// DownloadDBLogFilePortion returns. A log file for an hour before the instance
// existed is not found.
func TestRDSLogFilesServeTheEngineOutput_SDK(t *testing.T) {
	testContext, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	client := rdsClient()

	t.Run("PostgreSQL", func(t *testing.T) {
		const (
			instanceID = "rds-log-files-postgresql"
			username   = "dbadmin"
			password   = "MasterPassword-123!"
			database   = "application"
		)
		created, err := client.CreateDBInstance(testContext, &rds.CreateDBInstanceInput{
			DBInstanceIdentifier: aws.String(instanceID),
			DBInstanceClass:      aws.String("db.t3.micro"),
			Engine:               aws.String("postgres"),
			AllocatedStorage:     aws.Int32(20),
			MasterUsername:       aws.String(username),
			MasterUserPassword:   aws.String(password),
			DBName:               aws.String(database),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = client.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
				DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
			})
		})
		waitForRDSInstanceAvailable(t, client, testContext, instanceID)

		_, err = client.DownloadDBLogFilePortion(testContext, &rds.DownloadDBLogFilePortionInput{
			DBInstanceIdentifier: aws.String(instanceID),
			LogFileName:          aws.String("error/postgresql.log." + created.DBInstance.InstanceCreateTime.Add(-time.Hour).UTC().Format("2006-01-02-15")),
		})
		var apiErr smithy.APIError
		require.True(t, errors.As(err, &apiErr), "got %v", err)
		assert.Equal(t, "DBLogFileNotFoundFault", apiErr.ErrorCode())

		endpoint := fmt.Sprintf("%s:%d", aws.ToString(created.DBInstance.Endpoint.Address), aws.ToInt32(created.DBInstance.Endpoint.Port))
		config, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@%s/%s?sslmode=require", username, endpoint, database))
		require.NoError(t, err)
		config.Password = password
		config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
		connection, err := pgx.ConnectConfig(testContext, config)
		require.NoError(t, err)
		defer connection.Close(context.Background())
		_, err = connection.Exec(testContext, `SELECT * FROM rds_log_files_probe_missing_table`)
		require.Error(t, err)

		const logged = `relation "rds_log_files_probe_missing_table" does not exist`
		name := rdsAwaitLogFileHolding(testContext, t, client, instanceID, logged)
		assert.True(t, strings.HasPrefix(name, "error/postgresql.log."), "log file %q", name)

		whole, err := rdsDownloadWholeLogFile(testContext, client, instanceID, name)
		require.NoError(t, err)
		named, err := client.DescribeDBLogFiles(testContext, &rds.DescribeDBLogFilesInput{
			DBInstanceIdentifier: aws.String(instanceID),
			FilenameContains:     aws.String(strings.TrimPrefix(name, "error/")),
		})
		require.NoError(t, err)
		require.Len(t, named.DescribeDBLogFiles, 1)
		file := named.DescribeDBLogFiles[0]
		assert.GreaterOrEqual(t, aws.ToInt64(file.Size), int64(len(whole)))
		assert.WithinDuration(t, time.Now(), time.UnixMilli(aws.ToInt64(file.LastWritten)), 5*time.Minute)

		larger, err := client.DescribeDBLogFiles(testContext, &rds.DescribeDBLogFilesInput{
			DBInstanceIdentifier: aws.String(instanceID),
			FileSize:             file.Size,
		})
		require.NoError(t, err)
		for _, other := range larger.DescribeDBLogFiles {
			assert.Greater(t, aws.ToInt64(other.Size), aws.ToInt64(file.Size))
		}

		tail, err := client.DownloadDBLogFilePortion(testContext, &rds.DownloadDBLogFilePortionInput{
			DBInstanceIdentifier: aws.String(instanceID),
			LogFileName:          aws.String(name),
			NumberOfLines:        aws.Int32(1),
		})
		require.NoError(t, err)
		assert.Equal(t, 1, strings.Count(aws.ToString(tail.LogFileData), "\n"), "one most recent line")
		assert.False(t, aws.ToBool(tail.AdditionalDataPending))
	})

	t.Run("MySQL", func(t *testing.T) {
		const (
			instanceID = "rds-log-files-mysql"
			username   = "dbadmin"
			password   = "MasterPassword-123!"
		)
		created, err := client.CreateDBInstance(testContext, &rds.CreateDBInstanceInput{
			DBInstanceIdentifier: aws.String(instanceID),
			DBInstanceClass:      aws.String("db.t3.micro"),
			Engine:               aws.String("mysql"),
			AllocatedStorage:     aws.Int32(20),
			MasterUsername:       aws.String(username),
			MasterUserPassword:   aws.String(password),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = client.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
				DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
			})
		})
		waitForRDSInstanceAvailable(t, client, testContext, instanceID)
		endpoint := fmt.Sprintf("%s:%d", aws.ToString(created.DBInstance.Endpoint.Address), aws.ToInt32(created.DBInstance.Endpoint.Port))
		config := mysql.Config{
			User: username, Passwd: password, Net: "tcp", Addr: endpoint,
			TLSConfig: "skip-verify", AllowCleartextPasswords: true,
		}
		connection, err := sql.Open("mysql", config.FormatDSN())
		require.NoError(t, err)
		defer connection.Close()
		require.NoError(t, connection.PingContext(testContext))

		name := rdsAwaitLogFileHolding(testContext, t, client, instanceID, "ready for connections")
		assert.True(t, strings.HasPrefix(name, "error/mysql-error"), "log file %q", name)

		listed, err := client.DescribeDBLogFiles(testContext, &rds.DescribeDBLogFilesInput{DBInstanceIdentifier: aws.String(instanceID)})
		require.NoError(t, err)
		var names []string
		for _, file := range listed.DescribeDBLogFiles {
			names = append(names, aws.ToString(file.LogFileName))
		}
		assert.Contains(t, names, "error/mysql-error.log")
		assert.Contains(t, names, "error/mysql-error-running.log")
	})
}
