package aws_sdk_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rdsEngineSetting reads a setting from the engine behind a session: SHOW on
// PostgreSQL, the global system variable on MySQL.
func rdsEngineSetting(t *testing.T, database auroraSnapshotClient, name string) string {
	t.Helper()
	var value string
	switch session := database.(type) {
	case auroraPostgres:
		require.NoError(t, session.conn.QueryRow(session.ctx, `SELECT current_setting($1)`, name).Scan(&value))
	case auroraMySQL:
		require.NoError(t, session.db.QueryRowContext(session.ctx, `SELECT @@GLOBAL.`+name).Scan(&value))
	default:
		t.Fatalf("unknown session type %T", database)
	}
	return value
}

// rdsUserParameters lists every parameter a DB parameter group sets, page by
// page.
func rdsUserParameters(t *testing.T, ctx context.Context, c *rds.Client, group string) map[string]types.Parameter {
	t.Helper()
	parameters := map[string]types.Parameter{}
	pages := rds.NewDescribeDBParametersPaginator(c, &rds.DescribeDBParametersInput{
		DBParameterGroupName: aws.String(group), Source: aws.String("user"),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		require.NoError(t, err)
		for _, p := range page.Parameters {
			parameters[aws.ToString(p.ParameterName)] = p
		}
	}
	return parameters
}

func rdsParameterApplyStatus(t *testing.T, ctx context.Context, c *rds.Client, id string) string {
	t.Helper()
	instance := rdsDescribeInstance(t, ctx, c, id)
	require.Len(t, instance.DBParameterGroups, 1)
	return aws.ToString(instance.DBParameterGroups[0].ParameterApplyStatus)
}

// A DB parameter group family's catalog is its engine's own: every setting the
// engine has, with its default, type, allowed values and whether a restart
// applies it, page by page. An instance's engine starts with the parameters
// its group sets; a dynamic parameter ModifyDBParameterGroup applies
// immediately reaches the running engine at once, a static one waits for
// RebootDBInstance with the instance's group pending-reboot, and a reset
// returns a dynamic parameter to its default at once.
func TestRDS_DBParameterGroupsConfigureTheEngine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c := rdsClient()

	defaults := map[string]types.Parameter{}
	pages := rds.NewDescribeEngineDefaultParametersPaginator(c, &rds.DescribeEngineDefaultParametersInput{
		DBParameterGroupFamily: aws.String("postgres16"), MaxRecords: aws.Int32(100),
	})
	for pageCount := 1; pages.HasMorePages(); pageCount++ {
		page, err := pages.NextPage(ctx)
		require.NoError(t, err)
		assert.Equal(t, "postgres16", aws.ToString(page.EngineDefaults.DBParameterGroupFamily))
		assert.LessOrEqual(t, len(page.EngineDefaults.Parameters), 100)
		for _, p := range page.EngineDefaults.Parameters {
			defaults[aws.ToString(p.ParameterName)] = p
		}
		require.Less(t, pageCount, 10)
	}
	assert.Greater(t, len(defaults), 300, "the catalog holds every PostgreSQL 16 setting")
	workMem := defaults["work_mem"]
	assert.Equal(t, "4096", aws.ToString(workMem.ParameterValue))
	assert.Equal(t, "dynamic", aws.ToString(workMem.ApplyType))
	assert.Equal(t, "integer", aws.ToString(workMem.DataType))
	assert.Equal(t, "64-2147483647", aws.ToString(workMem.AllowedValues))
	assert.Equal(t, "engine-default", aws.ToString(workMem.Source))
	assert.True(t, aws.ToBool(workMem.IsModifiable))
	assert.Equal(t, "static", aws.ToString(defaults["max_connections"].ApplyType))
	assert.False(t, aws.ToBool(defaults["server_version"].IsModifiable))

	group := uniqueName("sdk-pg-params")
	_, err := c.CreateDBParameterGroup(ctx, &rds.CreateDBParameterGroupInput{
		DBParameterGroupName: aws.String(group), DBParameterGroupFamily: aws.String("postgres16"), Description: aws.String("engine parameters"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBParameterGroup(context.Background(), &rds.DeleteDBParameterGroupInput{DBParameterGroupName: aws.String(group)})
	})
	modify := func(parameters ...types.Parameter) error {
		_, err := c.ModifyDBParameterGroup(ctx, &rds.ModifyDBParameterGroupInput{DBParameterGroupName: aws.String(group), Parameters: parameters})
		return err
	}
	parameter := func(name, value string, method types.ApplyMethod) types.Parameter {
		return types.Parameter{ParameterName: aws.String(name), ParameterValue: aws.String(value), ApplyMethod: method}
	}
	assertAWSAPIErrorCode(t, modify(parameter("max_connections", "150", types.ApplyMethodImmediate)), "InvalidParameterCombination")
	assertAWSAPIErrorCode(t, modify(parameter("no_such_parameter", "1", types.ApplyMethodImmediate)), "InvalidParameterValue")
	assertAWSAPIErrorCode(t, modify(parameter("work_mem", "1", types.ApplyMethodImmediate)), "InvalidParameterValue")
	assertAWSAPIErrorCode(t, modify(parameter("archive_mode", "off", types.ApplyMethodPendingReboot)), "InvalidParameterValue")
	require.NoError(t, modify(
		parameter("work_mem", "8192", types.ApplyMethodImmediate),
		parameter("max_connections", "150", types.ApplyMethodPendingReboot),
	))
	set := rdsUserParameters(t, ctx, c, group)
	require.Len(t, set, 2)
	assert.Equal(t, "8192", aws.ToString(set["work_mem"].ParameterValue))
	assert.Equal(t, "user", aws.ToString(set["work_mem"].Source))
	assert.Equal(t, types.ApplyMethodPendingReboot, set["max_connections"].ApplyMethod)

	f := rdsInstanceFixture{t: t, ctx: ctx, client: c, family: "postgres"}
	id := uniqueName("sdk-pg-params")
	_, err = c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:  aws.String(id),
		DBInstanceClass:       aws.String("db.t3.micro"),
		Engine:                aws.String("postgres"),
		AllocatedStorage:      aws.Int32(20),
		MasterUsername:        aws.String(restoreSourceUsername),
		MasterUserPassword:    aws.String(restoreSourcePassword),
		DBName:                aws.String(restoreSourceDatabase),
		DBParameterGroupName:  aws.String(group),
		BackupRetentionPeriod: aws.Int32(0),
	})
	require.NoError(t, err)
	f.cleanup(id)
	db := f.connect(id)
	assert.Equal(t, "8MB", rdsEngineSetting(t, db, "work_mem"), "the engine starts with its group's dynamic parameters")
	assert.Equal(t, "150", rdsEngineSetting(t, db, "max_connections"), "the engine starts with its group's static parameters")
	assert.Equal(t, "in-sync", rdsParameterApplyStatus(t, ctx, c, id))

	require.NoError(t, modify(
		parameter("work_mem", "16384", types.ApplyMethodImmediate),
		parameter("max_connections", "200", types.ApplyMethodPendingReboot),
	))
	assert.Equal(t, "16MB", rdsEngineSetting(t, f.connect(id), "work_mem"), "a dynamic parameter reaches the running engine at once")
	assert.Equal(t, "150", rdsEngineSetting(t, f.connect(id), "max_connections"), "a static parameter waits for a reboot")
	assert.Equal(t, "pending-reboot", rdsParameterApplyStatus(t, ctx, c, id))

	_, err = c.RebootDBInstance(ctx, &rds.RebootDBInstanceInput{DBInstanceIdentifier: aws.String(id)})
	require.NoError(t, err)
	db = f.connect(id)
	assert.Equal(t, "200", rdsEngineSetting(t, db, "max_connections"), "the reboot applies the static parameter")
	assert.Equal(t, "16MB", rdsEngineSetting(t, db, "work_mem"))
	assert.Equal(t, "in-sync", rdsParameterApplyStatus(t, ctx, c, id))

	_, err = c.ResetDBParameterGroup(ctx, &rds.ResetDBParameterGroupInput{
		DBParameterGroupName: aws.String(group), Parameters: []types.Parameter{{ParameterName: aws.String("work_mem"), ApplyMethod: types.ApplyMethodImmediate}},
	})
	require.NoError(t, err)
	assert.Equal(t, "4MB", rdsEngineSetting(t, f.connect(id), "work_mem"), "a reset dynamic parameter returns to its default at once")
	assert.NotContains(t, rdsUserParameters(t, ctx, c, group), "work_mem")

	_, err = c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(id), DBParameterGroupName: aws.String("default.postgres16"), ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, "pending-reboot", rdsParameterApplyStatus(t, ctx, c, id), "a newly associated group applies at the next reboot")
}

// An RDS for MySQL engine starts with its group's static and dynamic
// parameters, and takes a dynamic change while it runs.
func TestRDS_DBParameterGroupsConfigureTheMySQLEngine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c := rdsClient()
	group := uniqueName("sdk-mysql-params")
	_, err := c.CreateDBParameterGroup(ctx, &rds.CreateDBParameterGroupInput{
		DBParameterGroupName: aws.String(group), DBParameterGroupFamily: aws.String("mysql8.0"), Description: aws.String("engine parameters"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBParameterGroup(context.Background(), &rds.DeleteDBParameterGroupInput{DBParameterGroupName: aws.String(group)})
	})
	_, err = c.ModifyDBParameterGroup(ctx, &rds.ModifyDBParameterGroupInput{DBParameterGroupName: aws.String(group), Parameters: []types.Parameter{
		{ParameterName: aws.String("max_connections"), ParameterValue: aws.String("321"), ApplyMethod: types.ApplyMethodImmediate},
		{ParameterName: aws.String("innodb_read_io_threads"), ParameterValue: aws.String("8"), ApplyMethod: types.ApplyMethodPendingReboot},
	}})
	require.NoError(t, err)

	f := rdsInstanceFixture{t: t, ctx: ctx, client: c, family: "mysql"}
	id := uniqueName("sdk-mysql-params")
	_, err = c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:  aws.String(id),
		DBInstanceClass:       aws.String("db.t3.micro"),
		Engine:                aws.String("mysql"),
		AllocatedStorage:      aws.Int32(20),
		MasterUsername:        aws.String(restoreSourceUsername),
		MasterUserPassword:    aws.String(restoreSourcePassword),
		DBName:                aws.String(restoreSourceDatabase),
		DBParameterGroupName:  aws.String(group),
		BackupRetentionPeriod: aws.Int32(0),
	})
	require.NoError(t, err)
	f.cleanup(id)
	db := f.connect(id)
	assert.Equal(t, "321", rdsEngineSetting(t, db, "max_connections"))
	assert.Equal(t, "8", rdsEngineSetting(t, db, "innodb_read_io_threads"))

	_, err = c.ModifyDBParameterGroup(ctx, &rds.ModifyDBParameterGroupInput{DBParameterGroupName: aws.String(group), Parameters: []types.Parameter{
		{ParameterName: aws.String("max_connections"), ParameterValue: aws.String("222"), ApplyMethod: types.ApplyMethodImmediate},
	}})
	require.NoError(t, err)
	assert.Equal(t, "222", rdsEngineSetting(t, db, "max_connections"), "the running engine takes the dynamic change")
	assert.Equal(t, "in-sync", rdsParameterApplyStatus(t, ctx, c, id))
}

// rdsWindowAfterNow is a 30-minute maintenance window that opens at the next
// minute, with a backup window half a day away from it.
func rdsWindowAfterNow() (maintenance, backup string) {
	start := time.Now().UTC().Truncate(time.Minute).Add(time.Minute)
	weekly := func(at time.Time) string {
		return strings.ToLower(at.Format("Mon")) + ":" + at.Format("15:04")
	}
	backupStart := start.Add(12 * time.Hour)
	return weekly(start) + "-" + weekly(start.Add(30*time.Minute)),
		backupStart.Format("15:04") + "-" + backupStart.Add(30*time.Minute).Format("15:04")
}

// ModifyDBInstance without ApplyImmediately holds a class change, a storage
// change and an engine version upgrade for the instance's maintenance window,
// reporting them in PendingModifiedValues meanwhile; a later request with
// ApplyImmediately applies every pending change at once, and the window
// applies what it holds when it opens.
func TestRDS_DBInstanceModificationsWaitForTheMaintenanceWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c := rdsClient()
	f := rdsInstanceFixture{t: t, ctx: ctx, client: c, family: "postgres"}
	id := uniqueName("sdk-pending")
	created, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:  aws.String(id),
		DBInstanceClass:       aws.String("db.t3.micro"),
		Engine:                aws.String("postgres"),
		EngineVersion:         aws.String("16.14"),
		AllocatedStorage:      aws.Int32(20),
		MasterUsername:        aws.String(restoreSourceUsername),
		MasterUserPassword:    aws.String(restoreSourcePassword),
		DBName:                aws.String(restoreSourceDatabase),
		BackupRetentionPeriod: aws.Int32(0),
	})
	require.NoError(t, err)
	f.cleanup(id)
	assert.Regexp(t, `^(mon|tue|wed|thu|fri|sat|sun):\d\d:\d\d-(mon|tue|wed|thu|fri|sat|sun):\d\d:\d\d$`,
		aws.ToString(created.DBInstance.PreferredMaintenanceWindow), "Amazon RDS picks a weekly maintenance window")
	f.waitAvailable(id)

	held, err := c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(id), AllocatedStorage: aws.Int32(30),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(20), aws.ToInt32(held.DBInstance.AllocatedStorage))
	assert.Equal(t, int32(30), aws.ToInt32(held.DBInstance.PendingModifiedValues.AllocatedStorage))
	applied, err := c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(id), ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(30), aws.ToInt32(applied.DBInstance.AllocatedStorage), "ApplyImmediately applies the pending changes")
	assert.Nil(t, applied.DBInstance.PendingModifiedValues.AllocatedStorage)

	maintenance, backup := rdsWindowAfterNow()
	held, err = c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier:       aws.String(id),
		DBInstanceClass:            aws.String("db.t3.small"),
		EngineVersion:              aws.String("16.15"),
		PreferredMaintenanceWindow: aws.String(maintenance),
		PreferredBackupWindow:      aws.String(backup),
	})
	require.NoError(t, err)
	assert.Equal(t, "available", aws.ToString(held.DBInstance.DBInstanceStatus))
	assert.Equal(t, maintenance, aws.ToString(held.DBInstance.PreferredMaintenanceWindow), "the window itself changes at once")
	assert.Equal(t, "db.t3.micro", aws.ToString(held.DBInstance.DBInstanceClass))
	assert.Equal(t, "16.14", aws.ToString(held.DBInstance.EngineVersion))
	assert.Equal(t, "db.t3.small", aws.ToString(held.DBInstance.PendingModifiedValues.DBInstanceClass))
	assert.Equal(t, "16.15", aws.ToString(held.DBInstance.PendingModifiedValues.EngineVersion))

	_, err = c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(id), PreferredMaintenanceWindow: aws.String("mon:03:00-mon:03:10"),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")

	maintained, err := rds.NewDBInstanceAvailableWaiter(c, func(o *rds.DBInstanceAvailableWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
		o.Retryable = func(_ context.Context, _ *rds.DescribeDBInstancesInput, out *rds.DescribeDBInstancesOutput, err error) (bool, error) {
			if err != nil {
				return false, err
			}
			instance := out.DBInstances[0]
			pending := instance.PendingModifiedValues
			return aws.ToString(instance.DBInstanceStatus) != "available" ||
				pending != nil && (pending.DBInstanceClass != nil || pending.EngineVersion != nil), nil
		}
	}).WaitForOutput(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)}, 5*time.Minute)
	require.NoError(t, err, "the maintenance window applies the pending changes")
	instance := maintained.DBInstances[0]
	assert.Equal(t, "db.t3.small", aws.ToString(instance.DBInstanceClass))
	assert.Equal(t, "16.15", aws.ToString(instance.EngineVersion))
	assert.Equal(t, "16.15", rdsServerVersion(t, f.connect(id)), fmt.Sprintf("the window upgraded the engine of %s", id))
}
