package rds_multi_az_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// An aws_db_instance with multi_az runs a standby in a second Availability
// Zone of the Region, which DescribeDBInstances reports as its
// SecondaryAvailabilityZone. A RebootDBInstance with ForceFailover moves the
// instance to that zone with its data; a refresh reads the new zone, and the
// next plan finds nothing to change.
func TestRDSMultiAZTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")
	outputs := readOutputs(t, env)
	require.Equal(t, "true", outputs.must(t, "multi_az"))
	primaryZone := outputs.must(t, "availability_zone")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := rds.New(rds.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(env.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:   env.Client,
	})
	id := outputs.must(t, "identifier")
	described, err := client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)})
	require.NoError(t, err)
	instance := described.DBInstances[0]
	require.True(t, aws.ToBool(instance.MultiAZ))
	require.Equal(t, primaryZone, aws.ToString(instance.AvailabilityZone))
	standbyZone := aws.ToString(instance.SecondaryAvailabilityZone)
	require.NotEmpty(t, standbyZone, "a Multi-AZ instance reports its standby's Availability Zone")
	require.NotEqual(t, primaryZone, standbyZone)

	conn := connect(t, ctx, outputs)
	_, err = conn.Exec(ctx, `CREATE TABLE ledger (entry text); INSERT INTO ledger VALUES ('before the failover')`)
	require.NoError(t, err)
	require.NoError(t, conn.Close(ctx))

	_, err = client.RebootDBInstance(ctx, &rds.RebootDBInstanceInput{DBInstanceIdentifier: aws.String(id), ForceFailover: aws.Bool(true)})
	require.NoError(t, err)
	failedOver, err := rds.NewDBInstanceAvailableWaiter(client, func(o *rds.DBInstanceAvailableWaiterOptions) {
		o.MinDelay = time.Second
		o.MaxDelay = 5 * time.Second
	}).WaitForOutput(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)}, 5*time.Minute)
	require.NoError(t, err)
	require.Equal(t, standbyZone, aws.ToString(failedOver.DBInstances[0].AvailabilityZone))
	require.Equal(t, primaryZone, aws.ToString(failedOver.DBInstances[0].SecondaryAvailabilityZone))

	conn = connect(t, ctx, outputs)
	var entry string
	require.NoError(t, conn.QueryRow(ctx, `SELECT entry FROM ledger`).Scan(&entry))
	require.Equal(t, "before the failover", entry)
	require.NoError(t, conn.Close(ctx))

	env.Terraform(t, "apply", "-refresh-only", "-auto-approve")
	require.Equal(t, standbyZone, readOutputs(t, env).must(t, "availability_zone"), "Terraform reads the zone the instance failed over to")
	env.Terraform(t, "plan", "-detailed-exitcode")
	env.Terraform(t, "destroy", "-auto-approve")
}

func connect(t *testing.T, ctx context.Context, outputs tfOutputs) *pgx.Conn {
	t.Helper()
	endpoint := net.JoinHostPort(outputs.must(t, "address"), outputs.must(t, "port"))
	config, err := pgx.ParseConfig(fmt.Sprintf("postgres://dbadmin@%s/application?sslmode=require", endpoint))
	require.NoError(t, err)
	config.Password = "MasterPassword-123!"
	config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
	conn, err := pgx.ConnectConfig(ctx, config)
	require.NoError(t, err)
	return conn
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
