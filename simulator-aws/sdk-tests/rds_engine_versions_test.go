package aws_sdk_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rdsServerVersion is the version the engine behind a session reports.
func rdsServerVersion(t *testing.T, database auroraSnapshotClient) string {
	t.Helper()
	var version string
	switch session := database.(type) {
	case auroraPostgres:
		require.NoError(t, session.conn.QueryRow(session.ctx, `SHOW server_version`).Scan(&version))
	case auroraMySQL:
		require.NoError(t, session.db.QueryRowContext(session.ctx, `SELECT VERSION()`).Scan(&version))
	default:
		t.Fatalf("unknown session type %T", database)
	}
	return version
}

// Each engine version runs the engine's own release of that version:
// DescribeDBEngineVersions lists the versions Amazon RDS offers with the
// upgrades it runs, CreateDBInstance refuses a version it does not offer and
// resolves a major version alone to that major's newest version, and
// ModifyDBInstance upgrades the engine in place, keeping the data, while it
// refuses a downgrade.
func TestRDS_EngineVersionsRunTheirOwnRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	f := rdsInstanceFixture{t: t, ctx: ctx, client: rdsClient(), family: "postgres"}
	c := f.client

	versions, err := c.DescribeDBEngineVersions(ctx, &rds.DescribeDBEngineVersionsInput{Engine: aws.String("postgres"), EngineVersion: aws.String("16.14")})
	require.NoError(t, err)
	require.Len(t, versions.DBEngineVersions, 1)
	offered := versions.DBEngineVersions[0]
	assert.Equal(t, "postgres16", aws.ToString(offered.DBParameterGroupFamily))
	var targets []string
	for _, target := range offered.ValidUpgradeTarget {
		targets = append(targets, aws.ToString(target.EngineVersion))
	}
	assert.Contains(t, targets, "16.15")
	defaults, err := c.DescribeDBEngineVersions(ctx, &rds.DescribeDBEngineVersionsInput{Engine: aws.String("postgres"), DefaultOnly: aws.Bool(true)})
	require.NoError(t, err)
	require.Len(t, defaults.DBEngineVersions, 1)
	assert.Equal(t, "16.15", aws.ToString(defaults.DBEngineVersions[0].EngineVersion))

	create := func(id, version string) (*rds.CreateDBInstanceOutput, error) {
		return c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
			DBInstanceIdentifier:  aws.String(id),
			DBInstanceClass:       aws.String("db.t3.micro"),
			Engine:                aws.String("postgres"),
			EngineVersion:         aws.String(version),
			AllocatedStorage:      aws.Int32(20),
			MasterUsername:        aws.String(restoreSourceUsername),
			MasterUserPassword:    aws.String(restoreSourcePassword),
			DBName:                aws.String(restoreSourceDatabase),
			BackupRetentionPeriod: aws.Int32(0),
		})
	}
	_, err = create(uniqueName("sdk-version-unoffered"), "15.4")
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")

	id := uniqueName("sdk-version-pg")
	created, err := create(id, "16.14")
	require.NoError(t, err)
	f.cleanup(id)
	assert.Equal(t, "16.14", aws.ToString(created.DBInstance.EngineVersion))
	db := f.connect(id)
	assert.Equal(t, "16.14", rdsServerVersion(t, db), "the instance runs PostgreSQL 16.14")
	db.exec(t, `CREATE TABLE ledger (entry varchar(64) NOT NULL)`)
	db.exec(t, `INSERT INTO ledger VALUES ('before-upgrade')`)

	upgrading, err := c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(id), EngineVersion: aws.String("16.15"), ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, "upgrading", aws.ToString(upgrading.DBInstance.DBInstanceStatus))
	require.NotNil(t, upgrading.DBInstance.PendingModifiedValues)
	assert.Equal(t, "16.15", aws.ToString(upgrading.DBInstance.PendingModifiedValues.EngineVersion))
	upgraded := f.waitAvailable(id)
	assert.Equal(t, "16.15", aws.ToString(upgraded.EngineVersion))
	db = f.connect(id)
	assert.Equal(t, "16.15", rdsServerVersion(t, db), "the upgraded instance runs PostgreSQL 16.15")
	assert.Equal(t, []string{"before-upgrade"}, db.entries(t), "the upgrade keeps the data")

	_, err = c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(id), EngineVersion: aws.String("16.14"), ApplyImmediately: aws.Bool(true),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")

	majorID := uniqueName("sdk-version-major")
	major, err := create(majorID, "16")
	require.NoError(t, err)
	f.cleanup(majorID)
	assert.Equal(t, "16.15", aws.ToString(major.DBInstance.EngineVersion), "a major version alone names its newest version")

	seventeenID := uniqueName("sdk-version-17")
	seventeen, err := create(seventeenID, "17")
	require.NoError(t, err)
	f.cleanup(seventeenID)
	assert.Equal(t, "17.11", aws.ToString(seventeen.DBInstance.EngineVersion))
	require.Len(t, seventeen.DBInstance.DBParameterGroups, 1)
	assert.Equal(t, "default.postgres17", aws.ToString(seventeen.DBInstance.DBParameterGroups[0].DBParameterGroupName))
	assert.Equal(t, "17.11", rdsServerVersion(t, f.connect(seventeenID)), "the instance runs PostgreSQL 17.11")

	mysql := rdsInstanceFixture{t: t, ctx: ctx, client: c, family: "mysql"}
	mysqlID := uniqueName("sdk-version-mysql")
	_, err = c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:  aws.String(mysqlID),
		DBInstanceClass:       aws.String("db.t3.micro"),
		Engine:                aws.String("mysql"),
		AllocatedStorage:      aws.Int32(20),
		MasterUsername:        aws.String(restoreSourceUsername),
		MasterUserPassword:    aws.String(restoreSourcePassword),
		DBName:                aws.String(restoreSourceDatabase),
		BackupRetentionPeriod: aws.Int32(0),
	})
	require.NoError(t, err)
	mysql.cleanup(mysqlID)
	version := aws.ToString(rdsDescribeInstance(t, ctx, c, mysqlID).EngineVersion)
	assert.True(t, strings.HasPrefix(rdsServerVersion(t, mysql.connect(mysqlID)), version), "the instance runs MySQL %s", version)
}

// An instance created without a DB parameter group is associated with its
// engine family's default group, which DescribeDBParameterGroups and
// DescribeDBParameters read and which cannot be modified or deleted; a group
// of another family is refused.
func TestRDS_DefaultDBParameterGroup(t *testing.T) {
	ctx := context.Background()
	c := rdsClient()
	id := uniqueName("sdk-default-group")
	created, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:  aws.String(id),
		DBInstanceClass:       aws.String("db.t3.micro"),
		Engine:                aws.String("mysql"),
		AllocatedStorage:      aws.Int32(20),
		MasterUsername:        aws.String("admin"),
		MasterUserPassword:    aws.String("password123!"),
		BackupRetentionPeriod: aws.Int32(0),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{DBInstanceIdentifier: aws.String(id), SkipFinalSnapshot: aws.Bool(true)})
	})
	require.Len(t, created.DBInstance.DBParameterGroups, 1)
	assert.Equal(t, "default.mysql8.0", aws.ToString(created.DBInstance.DBParameterGroups[0].DBParameterGroupName))
	assert.Equal(t, "in-sync", aws.ToString(created.DBInstance.DBParameterGroups[0].ParameterApplyStatus))

	groups, err := c.DescribeDBParameterGroups(ctx, &rds.DescribeDBParameterGroupsInput{DBParameterGroupName: aws.String("default.mysql8.0")})
	require.NoError(t, err)
	require.Len(t, groups.DBParameterGroups, 1)
	assert.Equal(t, "mysql8.0", aws.ToString(groups.DBParameterGroups[0].DBParameterGroupFamily))
	parameters, err := c.DescribeDBParameters(ctx, &rds.DescribeDBParametersInput{DBParameterGroupName: aws.String("default.mysql8.0")})
	require.NoError(t, err)
	assert.NotEmpty(t, parameters.Parameters)

	_, err = c.ModifyDBParameterGroup(ctx, &rds.ModifyDBParameterGroupInput{
		DBParameterGroupName: aws.String("default.mysql8.0"),
		Parameters:           parameters.Parameters[:1],
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")
	_, err = c.DeleteDBParameterGroup(ctx, &rds.DeleteDBParameterGroupInput{DBParameterGroupName: aws.String("default.mysql8.0")})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")

	_, err = c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(id), DBParameterGroupName: aws.String("default.postgres16"), ApplyImmediately: aws.Bool(true),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")
}
