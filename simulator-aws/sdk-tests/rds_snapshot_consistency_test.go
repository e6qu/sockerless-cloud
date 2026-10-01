package aws_sdk_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rdsCommitLog counts the transactions a writer has committed and signals
// each one, so the test waits on the writer's own progress.
type rdsCommitLog struct {
	mu        sync.Mutex
	committed int
	err       error
	signal    chan struct{}
}

func (l *rdsCommitLog) record(committed int, err error) {
	l.mu.Lock()
	l.committed, l.err = committed, err
	l.mu.Unlock()
	select {
	case l.signal <- struct{}{}:
	default:
	}
}

func (l *rdsCommitLog) state() (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.committed, l.err
}

func (l *rdsCommitLog) awaitAbove(ctx context.Context, t *testing.T, above int) int {
	t.Helper()
	for {
		committed, err := l.state()
		require.NoError(t, err, "the writer's connection must survive the snapshot")
		if committed > above {
			return committed
		}
		select {
		case <-l.signal:
		case <-ctx.Done():
			t.Fatalf("the writer committed nothing past transaction %d: %v", above, ctx.Err())
		}
	}
}

// An Amazon RDS snapshot of an instance under write load holds one point in
// time of its data.
//
// A writer commits transactions throughout the capture, each inserting the
// next number of a sequence and moving one unit between two accounts. The
// instance restored from the snapshot holds a gapless prefix of the sequence
// whose length matches both balances — every transaction whole or absent —
// and includes every transaction committed before CreateDBSnapshot was
// called. The writer's connection outlives the capture and keeps committing.
func TestRDS_SnapshotUnderWriteLoadIsOnePointInTime(t *testing.T) {
	testContext, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	c := rdsClient()
	const (
		instanceID = "rds-snapshot-consistency-source"
		restoredID = "rds-snapshot-consistency-restored"
		snapshotID = "rds-snapshot-consistency-snap"
		username   = "dbadmin"
		password   = "MasterPassword-123!"
		database   = "application"
		opening    = 1000000
	)
	waitAvailable := func(id string) *rds.DescribeDBInstancesOutput {
		t.Helper()
		described, err := rds.NewDBInstanceAvailableWaiter(c, func(o *rds.DBInstanceAvailableWaiterOptions) {
			o.MinDelay = waiterMinDelay
			o.MaxDelay = waiterMaxDelay
		}).WaitForOutput(testContext, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)}, 3*time.Minute)
		require.NoError(t, err)
		return described
	}
	connect := func(described *rds.DescribeDBInstancesOutput) *pgx.Conn {
		t.Helper()
		endpoint := described.DBInstances[0].Endpoint
		config, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@%s:%d/%s?sslmode=require",
			username, aws.ToString(endpoint.Address), aws.ToInt32(endpoint.Port), database))
		require.NoError(t, err)
		config.Password = password
		config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
		conn, err := pgx.ConnectConfig(testContext, config)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return conn
	}

	_, err := c.CreateDBInstance(testContext, &rds.CreateDBInstanceInput{
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
		_, _ = c.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
		})
	})
	source := connect(waitAvailable(instanceID))
	_, err = source.Exec(testContext, `CREATE TABLE sequence (n integer PRIMARY KEY)`)
	require.NoError(t, err)
	_, err = source.Exec(testContext, `CREATE TABLE account (id integer PRIMARY KEY, balance bigint NOT NULL)`)
	require.NoError(t, err)
	_, err = source.Exec(testContext, fmt.Sprintf(`INSERT INTO account VALUES (1, %d), (2, 0)`, opening))
	require.NoError(t, err)

	commits := &rdsCommitLog{signal: make(chan struct{}, 1)}
	stop := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for n := 1; ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			err := pgx.BeginFunc(testContext, source, func(tx pgx.Tx) error {
				if _, err := tx.Exec(testContext, `INSERT INTO sequence VALUES ($1)`, n); err != nil {
					return err
				}
				if _, err := tx.Exec(testContext, `UPDATE account SET balance = balance - 1 WHERE id = 1`); err != nil {
					return err
				}
				_, err := tx.Exec(testContext, `UPDATE account SET balance = balance + 1 WHERE id = 2`)
				return err
			})
			if err != nil {
				commits.record(n-1, err)
				return
			}
			commits.record(n, nil)
		}
	}()
	stopWriter := sync.OnceFunc(func() {
		close(stop)
		<-writerDone
	})
	defer stopWriter()

	beforeSnapshot := commits.awaitAbove(testContext, t, 0)
	_, err = c.CreateDBSnapshot(testContext, &rds.CreateDBSnapshotInput{
		DBSnapshotIdentifier: aws.String(snapshotID),
		DBInstanceIdentifier: aws.String(instanceID),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBSnapshot(context.Background(), &rds.DeleteDBSnapshotInput{
			DBSnapshotIdentifier: aws.String(snapshotID),
		})
	})
	require.NoError(t, rds.NewDBSnapshotAvailableWaiter(c, func(o *rds.DBSnapshotAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(testContext, &rds.DescribeDBSnapshotsInput{DBSnapshotIdentifier: aws.String(snapshotID)}, 3*time.Minute))
	afterSnapshot, _ := commits.state()
	commits.awaitAbove(testContext, t, afterSnapshot)
	stopWriter()
	_, writerErr := commits.state()
	require.NoError(t, writerErr, "the writer's connection must survive the snapshot")

	_, err = c.RestoreDBInstanceFromDBSnapshot(testContext, &rds.RestoreDBInstanceFromDBSnapshotInput{
		DBInstanceIdentifier: aws.String(restoredID),
		DBSnapshotIdentifier: aws.String(snapshotID),
		DBInstanceClass:      aws.String("db.t3.micro"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(restoredID), SkipFinalSnapshot: aws.Bool(true),
		})
	})
	restored := connect(waitAvailable(restoredID))
	var count, low, high int
	require.NoError(t, restored.QueryRow(testContext,
		`SELECT count(*), coalesce(min(n), 0), coalesce(max(n), 0) FROM sequence`).Scan(&count, &low, &high))
	var debited, credited int
	require.NoError(t, restored.QueryRow(testContext,
		`SELECT (SELECT balance FROM account WHERE id = 1), (SELECT balance FROM account WHERE id = 2)`).Scan(&debited, &credited))

	assert.Equal(t, 1, low, "the captured sequence starts at its first transaction")
	assert.Equal(t, high, count, "the captured sequence has no gaps")
	assert.GreaterOrEqual(t, high, beforeSnapshot, "the capture holds every transaction committed before CreateDBSnapshot")
	assert.Equal(t, high, credited, "each captured transaction's credit is in the capture, and no other")
	assert.Equal(t, opening-high, debited, "each captured transaction's debit is in the capture, and no other")
}

// A master-password change issued alongside an Amazon RDS snapshot takes
// effect whichever of the two reaches the engine first — the change waits out
// the capture's brief I/O suspension, or the capture waits for the change —
// and the snapshot still completes.
func TestRDS_MasterPasswordChangeDuringSnapshot(t *testing.T) {
	testContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := rdsClient()
	const (
		instanceID      = "rds-snapshot-password-source"
		snapshotID      = "rds-snapshot-password-snap"
		username        = "dbadmin"
		initialPassword = "MasterPassword-123!"
		rotatedPassword = "DuringSnapshot-456!"
		database        = "application"
	)
	_, err := c.CreateDBInstance(testContext, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		AllocatedStorage:     aws.Int32(20),
		MasterUsername:       aws.String(username),
		MasterUserPassword:   aws.String(initialPassword),
		DBName:               aws.String(database),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instanceID), SkipFinalSnapshot: aws.Bool(true),
		})
	})
	described, err := rds.NewDBInstanceAvailableWaiter(c, func(o *rds.DBInstanceAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).WaitForOutput(testContext, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instanceID)}, 3*time.Minute)
	require.NoError(t, err)
	endpoint := described.DBInstances[0].Endpoint
	connect := func(password string) (*pgx.Conn, error) {
		config, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@%s:%d/%s?sslmode=require",
			username, aws.ToString(endpoint.Address), aws.ToInt32(endpoint.Port), database))
		require.NoError(t, err)
		config.Password = password
		config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
		return pgx.ConnectConfig(testContext, config)
	}
	initial, err := connect(initialPassword)
	require.NoError(t, err)
	require.NoError(t, initial.Close(testContext))

	_, err = c.CreateDBSnapshot(testContext, &rds.CreateDBSnapshotInput{
		DBSnapshotIdentifier: aws.String(snapshotID),
		DBInstanceIdentifier: aws.String(instanceID),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBSnapshot(context.Background(), &rds.DeleteDBSnapshotInput{
			DBSnapshotIdentifier: aws.String(snapshotID),
		})
	})
	_, err = c.ModifyDBInstance(testContext, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID),
		MasterUserPassword:   aws.String(rotatedPassword),
		ApplyImmediately:     aws.Bool(true),
	})
	require.NoError(t, err, "a password change during a snapshot waits for the capture's I/O suspension to end")

	rotated, err := connect(rotatedPassword)
	require.NoError(t, err, "the rotated master password authenticates")
	var one int
	require.NoError(t, rotated.QueryRow(testContext, `SELECT 1`).Scan(&one))
	require.NoError(t, rotated.Close(testContext))
	_, err = connect(initialPassword)
	var refusal *pgconn.PgError
	require.ErrorAs(t, err, &refusal, "the previous master password stops authenticating")
	require.Equal(t, "28P01", refusal.Code, "PostgreSQL refuses the previous password as invalid_password")

	require.NoError(t, rds.NewDBSnapshotAvailableWaiter(c, func(o *rds.DBSnapshotAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(testContext, &rds.DescribeDBSnapshotsInput{DBSnapshotIdentifier: aws.String(snapshotID)}, 3*time.Minute))
}
