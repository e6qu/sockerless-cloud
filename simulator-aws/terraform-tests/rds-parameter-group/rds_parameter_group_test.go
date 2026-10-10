package rds_parameter_group_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// An aws_db_parameter_group's parameters configure the engine of the
// aws_db_instance that uses it: the engine starts with them and takes a
// changed dynamic parameter at once. A class change Terraform applies without
// apply_immediately waits for the instance's maintenance_window, which then
// applies it, leaving no drift.
func TestRDSParameterGroupTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	now := time.Now().UTC().Truncate(time.Minute)
	weekly := func(start time.Time) string {
		at := func(t time.Time) string { return strings.ToLower(t.Format("Mon")) + ":" + t.Format("15:04") }
		return at(start) + "-" + at(start.Add(30*time.Minute))
	}
	backup := now.Add(12 * time.Hour)
	vars := func(workMem, class, window string) []string {
		return []string{"-var", "work_mem=" + workMem, "-var", "instance_class=" + class, "-var", "maintenance_window=" + window,
			"-var", "backup_window=" + backup.Format("15:04") + "-" + backup.Add(30*time.Minute).Format("15:04")}
	}
	apply := func(args []string) { env.Terraform(t, append([]string{"apply", "-auto-approve"}, args...)...) }

	apply(vars("8192", "db.t3.micro", weekly(now.Add(24*time.Hour))))
	outputs := readOutputs(t, env)
	endpoint := net.JoinHostPort(outputs.must(t, "address"), outputs.must(t, "port"))
	require.Equal(t, "8MB", setting(t, endpoint, "work_mem"), "the engine starts with the group's dynamic parameter")
	require.Equal(t, "150", setting(t, endpoint, "max_connections"), "the engine starts with the group's static parameter")

	client := rds.New(rds.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(env.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:   env.Client,
	})
	ctx := context.Background()
	window := weekly(time.Now().UTC().Truncate(time.Minute).Add(2 * time.Minute))
	apply(vars("8192", "db.t3.small", window))
	described, err := client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String("tf-rds-parameters")})
	require.NoError(t, err)
	instance := described.DBInstances[0]
	require.Equal(t, window, aws.ToString(instance.PreferredMaintenanceWindow))
	require.Equal(t, "db.t3.micro", aws.ToString(instance.DBInstanceClass), "the class change waits for the maintenance window")
	require.Equal(t, "db.t3.small", aws.ToString(instance.PendingModifiedValues.DBInstanceClass))

	_, err = rds.NewDBInstanceAvailableWaiter(client, func(o *rds.DBInstanceAvailableWaiterOptions) {
		o.MinDelay = time.Second
		o.MaxDelay = 5 * time.Second
		o.Retryable = func(_ context.Context, _ *rds.DescribeDBInstancesInput, out *rds.DescribeDBInstancesOutput, err error) (bool, error) {
			if err != nil {
				return false, err
			}
			instance := out.DBInstances[0]
			return aws.ToString(instance.DBInstanceStatus) != "available" || instance.PendingModifiedValues.DBInstanceClass != nil, nil
		}
	}).WaitForOutput(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String("tf-rds-parameters")}, 5*time.Minute)
	require.NoError(t, err, "the maintenance window %s applies the class change", window)
	env.Terraform(t, append([]string{"plan", "-detailed-exitcode"}, vars("8192", "db.t3.small", window)...)...)

	apply(vars("16384", "db.t3.small", window))
	require.Equal(t, "16MB", setting(t, endpoint, "work_mem"), "the running engine takes the changed dynamic parameter")
	described, err = client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String("tf-rds-parameters")})
	require.NoError(t, err)
	require.Equal(t, "in-sync", aws.ToString(described.DBInstances[0].DBParameterGroups[0].ParameterApplyStatus))

	env.Terraform(t, append([]string{"destroy", "-auto-approve"}, vars("16384", "db.t3.small", window)...)...)
}

func setting(t *testing.T, endpoint, name string) string {
	t.Helper()
	ctx := context.Background()
	config, err := pgx.ParseConfig(fmt.Sprintf("postgres://dbadmin@%s/application?sslmode=require", endpoint))
	require.NoError(t, err)
	config.Password = "MasterPassword-123!"
	config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
	conn, err := pgx.ConnectConfig(ctx, config)
	require.NoError(t, err)
	defer conn.Close(ctx)
	var value string
	require.NoError(t, conn.QueryRow(ctx, `SELECT current_setting($1)`, name).Scan(&value))
	return value
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
