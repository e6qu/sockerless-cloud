package rds_restore_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/require"
)

func TestRDSRestoreTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	seedSnapshot(t, env)

	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	outputs := readOutputs(t, env)
	require.True(t, strings.HasPrefix(outputs.must(t, "rds_restored_instance_arn"), "arn:aws:rds:us-east-1:"),
		"Restored RDS instance ARN must include the rds-region prefix")
	require.Contains(t, outputs.must(t, "rds_restored_instance_arn"), ":db:tf-rds-restored",
		"Restored RDS instance ARN must end with :db:<identifier>")
	require.Equal(t, "postgres", outputs.must(t, "rds_restored_instance_engine"),
		"Restored RDS engine must round-trip through terraform-provider-aws refresh")
	require.Equal(t, "terraform", outputs.must(t, "rds_restored_instance_tags_env"),
		"Restored RDS tags must round-trip through ListTagsForResource")

	env.Terraform(t, "destroy", "-auto-approve")
}

func seedSnapshot(t *testing.T, env *tfsim.Env) {
	t.Helper()
	ctx := context.Background()
	client := rds.New(rds.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(env.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:   env.Client,
	})
	_, err := client.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String("tf-rds-restore-source"),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		EngineVersion:        aws.String("17.5"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	_, err = client.CreateDBSnapshot(ctx, &rds.CreateDBSnapshotInput{
		DBInstanceIdentifier: aws.String("tf-rds-restore-source"),
		DBSnapshotIdentifier: aws.String("tf-rds-snapshot-source"),
	})
	require.NoError(t, err)
	// Amazon RDS refuses a restore from a snapshot still being taken ("is
	// creating; it must be available to restore from"). The provider waits for
	// its own aws_db_snapshot; this one is seeded outside Terraform, so wait for
	// it here with the SDK's DBSnapshotAvailable waiter.
	err = rds.NewDBSnapshotAvailableWaiter(client, func(o *rds.DBSnapshotAvailableWaiterOptions) {
		o.MinDelay = 250 * time.Millisecond
		o.MaxDelay = 2 * time.Second
	}).Wait(ctx, &rds.DescribeDBSnapshotsInput{
		DBSnapshotIdentifier: aws.String("tf-rds-snapshot-source"),
	}, 2*time.Minute)
	require.NoError(t, err, "snapshot tf-rds-snapshot-source never became available to restore from")
	t.Cleanup(func() {
		_, err := client.DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{
			DBSnapshotIdentifier: aws.String("tf-rds-snapshot-source"),
		})
		require.NoError(t, err)
		_, err = client.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String("tf-rds-restore-source"),
			SkipFinalSnapshot:    aws.Bool(true),
		})
		require.NoError(t, err)
	})
}

type tfOutputs map[string]struct {
	Value any `json:"value"`
}

func (o tfOutputs) must(t *testing.T, key string) string {
	t.Helper()
	v, ok := o[key]
	require.True(t, ok, "output %q missing from terraform state", key)
	s, ok := v.Value.(string)
	require.True(t, ok, "output %q is not a string (got %T)", key, v.Value)
	require.NotEmpty(t, s, "output %q is empty", key)
	return s
}

func readOutputs(t *testing.T, env *tfsim.Env) tfOutputs {
	t.Helper()
	var outputs tfOutputs
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &outputs))
	return outputs
}
