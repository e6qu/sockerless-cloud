package aws_sdk_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rdsAwaitBlueGreenStatus polls DescribeBlueGreenDeployments, for which the
// SDK carries no waiter, until the deployment reports status.
func rdsAwaitBlueGreenStatus(t *testing.T, ctx context.Context, c *rds.Client, id, status string) types.BlueGreenDeployment {
	t.Helper()
	var deployment types.BlueGreenDeployment
	require.Eventually(t, func() bool {
		out, err := c.DescribeBlueGreenDeployments(ctx, &rds.DescribeBlueGreenDeploymentsInput{BlueGreenDeploymentIdentifier: aws.String(id)})
		if err != nil || len(out.BlueGreenDeployments) != 1 {
			return false
		}
		deployment = out.BlueGreenDeployments[0]
		return aws.ToString(deployment.Status) == status
	}, 5*time.Minute, 250*time.Millisecond, "blue/green deployment %s must reach %s", id, status)
	return deployment
}

// rdsAwaitBlueGreenGone polls until DescribeBlueGreenDeployments no longer
// finds the deployment.
func rdsAwaitBlueGreenGone(t *testing.T, ctx context.Context, c *rds.Client, id string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := c.DescribeBlueGreenDeployments(ctx, &rds.DescribeBlueGreenDeploymentsInput{BlueGreenDeploymentIdentifier: aws.String(id)})
		var notFound *types.BlueGreenDeploymentNotFoundFault
		return errors.As(err, &notFound)
	}, 2*time.Minute, 250*time.Millisecond, "blue/green deployment %s must be deleted", id)
}

func rdsDescribeInstance(t *testing.T, ctx context.Context, c *rds.Client, id string) types.DBInstance {
	t.Helper()
	out, err := c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)})
	require.NoError(t, err)
	require.Len(t, out.DBInstances, 1)
	return out.DBInstances[0]
}

func rdsEndpointOf(instance types.DBInstance) string {
	return fmt.Sprintf("%s:%d", aws.ToString(instance.Endpoint.Address), aws.ToInt32(instance.Endpoint.Port))
}

// rdsWriteFails reports the error a write through the session returns.
func rdsWriteFails(t *testing.T, database auroraSnapshotClient, statement string) error {
	t.Helper()
	switch session := database.(type) {
	case auroraPostgres:
		_, err := session.conn.Exec(session.ctx, statement)
		return err
	case auroraMySQL:
		_, err := session.db.ExecContext(session.ctx, statement)
		return err
	}
	t.Fatalf("unknown session type %T", database)
	return nil
}

// A blue/green deployment of a DB instance provisions a green instance that
// holds the blue instance's data, with the target engine version and DB
// parameter group, and serves it read-only. The switchover gives the green
// instance the blue identifier, ARN and endpoint with every row the blue
// instance committed, and keeps the blue instance under -old1 on an endpoint
// of its own; Terraform deletes that instance by the deployment's Source.
func TestRDS_BlueGreenDeploymentSwitchesOver(t *testing.T) {
	for _, engine := range []string{"mysql", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			f := rdsInstanceFixture{t: t, ctx: ctx, client: rdsClient(), family: engine}
			c := f.client
			blueID := uniqueName("sdk-bg-" + engine)
			_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
				DBInstanceIdentifier:  aws.String(blueID),
				DBInstanceClass:       aws.String("db.t3.micro"),
				Engine:                aws.String(engine),
				AllocatedStorage:      aws.Int32(20),
				MasterUsername:        aws.String(restoreSourceUsername),
				MasterUserPassword:    aws.String(restoreSourcePassword),
				DBName:                aws.String(restoreSourceDatabase),
				BackupRetentionPeriod: aws.Int32(1),
			})
			require.NoError(t, err)
			f.cleanup(blueID)
			f.cleanup(blueID + "-old1")
			blueDB := f.connect(blueID)
			blueDB.exec(t, `CREATE TABLE ledger (entry varchar(64) NOT NULL)`)
			blueDB.exec(t, `INSERT INTO ledger VALUES ('before-green')`)
			blue := rdsDescribeInstance(t, ctx, c, blueID)

			family := map[string]string{"mysql": "mysql8.0", "postgres": "postgres17"}[engine]
			targetVersion := map[string]string{"mysql": "8.0.41", "postgres": "17.6"}[engine]
			group := uniqueName("sdk-bg-green")
			_, err = c.CreateDBParameterGroup(ctx, &rds.CreateDBParameterGroupInput{
				DBParameterGroupName: aws.String(group), DBParameterGroupFamily: aws.String(family), Description: aws.String("green"),
			})
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = c.DeleteDBParameterGroup(context.Background(), &rds.DeleteDBParameterGroupInput{DBParameterGroupName: aws.String(group)})
			})

			created, err := c.CreateBlueGreenDeployment(ctx, &rds.CreateBlueGreenDeploymentInput{
				BlueGreenDeploymentName:    aws.String(uniqueName("sdk-bg")),
				Source:                     blue.DBInstanceArn,
				TargetEngineVersion:        aws.String(targetVersion),
				TargetDBParameterGroupName: aws.String(group),
			})
			require.NoError(t, err)
			deploymentID := aws.ToString(created.BlueGreenDeployment.BlueGreenDeploymentIdentifier)
			t.Cleanup(func() {
				_, _ = c.DeleteBlueGreenDeployment(context.Background(), &rds.DeleteBlueGreenDeploymentInput{
					BlueGreenDeploymentIdentifier: aws.String(deploymentID), DeleteTarget: aws.Bool(true)})
			})
			assert.Equal(t, "PROVISIONING", aws.ToString(created.BlueGreenDeployment.Status))
			assert.Equal(t, aws.ToString(blue.DBInstanceArn), aws.ToString(created.BlueGreenDeployment.Source))
			greenARN := aws.ToString(created.BlueGreenDeployment.Target)
			greenID := greenARN[strings.LastIndex(greenARN, ":")+1:]
			assert.Regexp(t, "^"+blueID+"-green-[a-z]{6}$", greenID)

			available := rdsAwaitBlueGreenStatus(t, ctx, c, deploymentID, "AVAILABLE")
			require.Len(t, available.SwitchoverDetails, 1)
			assert.Equal(t, "AVAILABLE", aws.ToString(available.SwitchoverDetails[0].Status))
			for _, task := range available.Tasks {
				assert.Equal(t, "COMPLETED", aws.ToString(task.Status), aws.ToString(task.Name))
			}
			green := rdsDescribeInstance(t, ctx, c, greenID)
			assert.Equal(t, "available", aws.ToString(green.DBInstanceStatus))
			assert.Equal(t, targetVersion, aws.ToString(green.EngineVersion))
			require.Len(t, green.DBParameterGroups, 1)
			assert.Equal(t, group, aws.ToString(green.DBParameterGroups[0].DBParameterGroupName))
			assert.Equal(t, blueID, aws.ToString(green.ReadReplicaSourceDBInstanceIdentifier))
			assert.Contains(t, rdsDescribeInstance(t, ctx, c, blueID).ReadReplicaDBInstanceIdentifiers, greenID)

			greenDB := connectRDSDatabase(t, ctx, engine, rdsEndpointOf(green))
			assert.Equal(t, []string{"before-green"}, greenDB.entries(t), "the green instance holds the blue instance's data")
			assert.Error(t, rdsWriteFails(t, greenDB, `INSERT INTO ledger VALUES ('written-to-green')`),
				"the green instance serves its sessions read-only")

			blueDB.exec(t, `INSERT INTO ledger VALUES ('during-deployment')`)

			switched, err := c.SwitchoverBlueGreenDeployment(ctx, &rds.SwitchoverBlueGreenDeploymentInput{
				BlueGreenDeploymentIdentifier: aws.String(deploymentID)})
			require.NoError(t, err)
			assert.Equal(t, "SWITCHOVER_IN_PROGRESS", aws.ToString(switched.BlueGreenDeployment.Status))
			completed := rdsAwaitBlueGreenStatus(t, ctx, c, deploymentID, "SWITCHOVER_COMPLETED")
			assert.Equal(t, aws.ToString(blue.DBInstanceArn), aws.ToString(completed.Target),
				"after the switchover the target is the instance in production")
			assert.True(t, strings.HasSuffix(aws.ToString(completed.Source), ":db:"+blueID+"-old1"),
				"after the switchover the source is the blue instance under its -old1 name: %s", aws.ToString(completed.Source))

			production := rdsDescribeInstance(t, ctx, c, blueID)
			assert.Equal(t, aws.ToString(green.DbiResourceId), aws.ToString(production.DbiResourceId),
				"the green instance took over the blue identifier")
			assert.Equal(t, targetVersion, aws.ToString(production.EngineVersion))
			assert.Equal(t, rdsEndpointOf(blue), rdsEndpointOf(production), "the green instance took over the blue endpoint")
			assert.Empty(t, aws.ToString(production.ReadReplicaSourceDBInstanceIdentifier))
			retired := rdsDescribeInstance(t, ctx, c, blueID+"-old1")
			assert.Equal(t, aws.ToString(blue.DbiResourceId), aws.ToString(retired.DbiResourceId))
			assert.NotEqual(t, rdsEndpointOf(blue), rdsEndpointOf(retired))
			_, err = c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(greenID)})
			var notFound *types.DBInstanceNotFoundFault
			assert.True(t, errors.As(err, &notFound), "the green identifier is gone: %v", err)

			productionDB := connectRDSDatabase(t, ctx, engine, rdsEndpointOf(production))
			assert.Equal(t, []string{"before-green", "during-deployment"}, productionDB.entries(t),
				"production holds every row the blue instance committed")
			productionDB.exec(t, `INSERT INTO ledger VALUES ('after-switchover')`)
			retiredDB := connectRDSDatabase(t, ctx, engine, rdsEndpointOf(f.waitAvailable(blueID+"-old1")))
			assert.Equal(t, []string{"before-green", "during-deployment"}, retiredDB.entries(t))

			_, err = c.DeleteBlueGreenDeployment(ctx, &rds.DeleteBlueGreenDeploymentInput{
				BlueGreenDeploymentIdentifier: aws.String(deploymentID), DeleteTarget: aws.Bool(true)})
			var invalid *types.InvalidBlueGreenDeploymentStateFault
			assert.True(t, errors.As(err, &invalid), "DeleteTarget is refused after a switchover: %v", err)
			deleted, err := c.DeleteBlueGreenDeployment(ctx, &rds.DeleteBlueGreenDeploymentInput{
				BlueGreenDeploymentIdentifier: aws.String(deploymentID)})
			require.NoError(t, err)
			assert.Equal(t, "DELETING", aws.ToString(deleted.BlueGreenDeployment.Status))
			rdsAwaitBlueGreenGone(t, ctx, c, deploymentID)
			rdsDescribeInstance(t, ctx, c, blueID)
			rdsDescribeInstance(t, ctx, c, blueID+"-old1")
		})
	}
}

// Deleting a blue/green deployment before its switchover with DeleteTarget
// deletes the green instance; without it the green instance stays as a
// standalone instance that accepts writes. A deployment refuses a DB cluster
// source, a source without automated backups, a second deployment of the
// same name, and a DB parameter group that does not exist, as ModifyDBInstance
// does; an instance upgrades minor versions automatically unless told not to.
func TestRDS_BlueGreenDeploymentDeletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	f := rdsInstanceFixture{t: t, ctx: ctx, client: rdsClient(), family: "mysql"}
	c := f.client
	create := func(id string, retention int32) types.DBInstance {
		_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
			DBInstanceIdentifier: aws.String(id), DBInstanceClass: aws.String("db.t3.micro"), Engine: aws.String("mysql"),
			AllocatedStorage: aws.Int32(20), MasterUsername: aws.String(restoreSourceUsername),
			MasterUserPassword: aws.String(restoreSourcePassword), DBName: aws.String(restoreSourceDatabase),
			BackupRetentionPeriod: aws.Int32(retention),
		})
		require.NoError(t, err)
		f.cleanup(id)
		return f.waitAvailable(id)
	}
	errorCode := func(err error) string {
		var apiErr smithy.APIError
		require.True(t, errors.As(err, &apiErr), "want an API error, got %v", err)
		return apiErr.ErrorCode()
	}
	blueID := uniqueName("sdk-bg-delete")
	blue := create(blueID, 1)
	blueDB := f.connect(blueID)
	blueDB.exec(t, `CREATE TABLE ledger (entry varchar(64) NOT NULL)`)
	blueDB.exec(t, `INSERT INTO ledger VALUES ('blue')`)

	deploy := func(name string) (string, string) {
		out, err := c.CreateBlueGreenDeployment(ctx, &rds.CreateBlueGreenDeploymentInput{
			BlueGreenDeploymentName: aws.String(name), Source: blue.DBInstanceArn,
		})
		require.NoError(t, err)
		target := aws.ToString(out.BlueGreenDeployment.Target)
		greenID := target[strings.LastIndex(target, ":")+1:]
		f.cleanup(greenID)
		return aws.ToString(out.BlueGreenDeployment.BlueGreenDeploymentIdentifier), greenID
	}

	name := uniqueName("sdk-bg-first")
	first, firstGreen := deploy(name)
	_, err := c.CreateBlueGreenDeployment(ctx, &rds.CreateBlueGreenDeploymentInput{
		BlueGreenDeploymentName: aws.String(name), Source: blue.DBInstanceArn,
	})
	assert.Equal(t, "BlueGreenDeploymentAlreadyExistsFault", errorCode(err))
	rdsAwaitBlueGreenStatus(t, ctx, c, first, "AVAILABLE")
	_, err = c.DeleteBlueGreenDeployment(ctx, &rds.DeleteBlueGreenDeploymentInput{
		BlueGreenDeploymentIdentifier: aws.String(first), DeleteTarget: aws.Bool(true)})
	require.NoError(t, err)
	rdsAwaitBlueGreenGone(t, ctx, c, first)
	require.NoError(t, rds.NewDBInstanceDeletedWaiter(c, func(o *rds.DBInstanceDeletedWaiterOptions) {
		o.MinDelay = waiterMinDelay
		o.MaxDelay = waiterMaxDelay
	}).Wait(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(firstGreen)}, 3*time.Minute),
		"DeleteTarget deletes the green instance")
	assert.NotContains(t, rdsDescribeInstance(t, ctx, c, blueID).ReadReplicaDBInstanceIdentifiers, firstGreen)

	second, secondGreen := deploy(uniqueName("sdk-bg-second"))
	rdsAwaitBlueGreenStatus(t, ctx, c, second, "AVAILABLE")
	_, err = c.DeleteBlueGreenDeployment(ctx, &rds.DeleteBlueGreenDeploymentInput{BlueGreenDeploymentIdentifier: aws.String(second)})
	require.NoError(t, err)
	rdsAwaitBlueGreenGone(t, ctx, c, second)
	standalone := f.waitAvailable(secondGreen)
	assert.Empty(t, aws.ToString(standalone.ReadReplicaSourceDBInstanceIdentifier))
	standaloneDB := connectRDSDatabase(t, ctx, "mysql", rdsEndpointOf(standalone))
	standaloneDB.exec(t, `INSERT INTO ledger VALUES ('standalone')`)
	assert.Equal(t, []string{"blue", "standalone"}, standaloneDB.entries(t), "a green instance left standing accepts writes")
	assert.Equal(t, []string{"blue"}, blueDB.entries(t))

	_, err = c.CreateBlueGreenDeployment(ctx, &rds.CreateBlueGreenDeploymentInput{
		BlueGreenDeploymentName: aws.String(uniqueName("sdk-bg-group")), Source: blue.DBInstanceArn,
		TargetDBParameterGroupName: aws.String(uniqueName("missing-group")),
	})
	assert.Equal(t, "DBParameterGroupNotFound", errorCode(err))

	unbacked := create(uniqueName("sdk-bg-unbacked"), 0)
	assert.True(t, aws.ToBool(unbacked.AutoMinorVersionUpgrade), "Amazon RDS upgrades minor versions by default")
	modified, err := c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: unbacked.DBInstanceIdentifier, AutoMinorVersionUpgrade: aws.Bool(false), ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(modified.DBInstance.AutoMinorVersionUpgrade))
	_, err = c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: unbacked.DBInstanceIdentifier, DBParameterGroupName: aws.String(uniqueName("missing-group")),
	})
	assert.Equal(t, "DBParameterGroupNotFound", errorCode(err))
	_, err = c.CreateBlueGreenDeployment(ctx, &rds.CreateBlueGreenDeploymentInput{
		BlueGreenDeploymentName: aws.String(uniqueName("sdk-bg-unbacked")), Source: unbacked.DBInstanceArn,
	})
	assert.Equal(t, "SourceDatabaseNotSupportedFault", errorCode(err))

	clusterID := uniqueName("sdk-bg-cluster")
	cluster, err := c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID), Engine: aws.String("mysql"), MasterUsername: aws.String("admin"),
		DBClusterInstanceClass: aws.String("db.m6gd.large"), AllocatedStorage: aws.Int32(100),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(context.Background(), &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(clusterID), SkipFinalSnapshot: aws.Bool(true)})
	})
	_, err = c.CreateBlueGreenDeployment(ctx, &rds.CreateBlueGreenDeploymentInput{
		BlueGreenDeploymentName: aws.String(uniqueName("sdk-bg-cluster")), Source: cluster.DBCluster.DBClusterArn,
	})
	assert.Equal(t, "SourceClusterNotSupportedFault", errorCode(err), "a Multi-AZ DB cluster has no blue/green deployments")
}
