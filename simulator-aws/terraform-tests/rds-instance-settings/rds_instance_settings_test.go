package rds_instance_settings_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type settings struct {
	identifier, storageType, caCertificate, optionGroup string
	port, iops, throughput                              int
	multiAZ                                             bool
}

func (s settings) vars() []string {
	vars := []string{
		"-var", "identifier=" + s.identifier,
		"-var", "port=" + strconv.Itoa(s.port),
		"-var", "storage_type=" + s.storageType,
		"-var", "iops=" + strconv.Itoa(s.iops),
		"-var", "multi_az=" + strconv.FormatBool(s.multiAZ),
		"-var", "ca_cert_identifier=" + s.caCertificate,
		"-var", "option_group=" + s.optionGroup,
	}
	if s.throughput > 0 {
		vars = append(vars, "-var", "storage_throughput="+strconv.Itoa(s.throughput))
	}
	return vars
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}

// An aws_db_instance's storage type, Provisioned IOPS, storage throughput,
// Multi-AZ setting, CA certificate, option group, port and identifier change
// in place with apply_immediately: the instance keeps its resource ID and its
// data, answers at the new port under the new identifier, and the next plan
// finds no drift.
func TestRDSInstanceSettingsTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	first := settings{
		identifier: "tf-rds-settings", port: freePort(t), storageType: "io1", iops: 1000,
		caCertificate: "rds-ca-rsa2048-g1", optionGroup: "first",
	}
	env.Terraform(t, append([]string{"apply", "-auto-approve"}, first.vars()...)...)
	outputs := readOutputs(t, env)
	resourceID := outputs.must(t, "resource_id")
	require.Equal(t, strconv.Itoa(first.port), outputs.must(t, "port"))
	ctx := context.Background()
	conn := connect(t, ctx, outputs)
	_, err := conn.Exec(ctx, `CREATE TABLE ledger (entry text); INSERT INTO ledger VALUES ('before the changes')`)
	require.NoError(t, err)
	require.NoError(t, conn.Close(ctx))

	second := settings{
		identifier: "tf-rds-settings-renamed", port: freePort(t), storageType: "gp3", iops: 3000, throughput: 250,
		multiAZ: true, caCertificate: "rds-ca-ecc384-g1", optionGroup: "second",
	}
	env.Terraform(t, append([]string{"apply", "-auto-approve"}, second.vars()...)...)
	outputs = readOutputs(t, env)
	require.Equal(t, resourceID, outputs.must(t, "resource_id"), "the rename keeps the instance")
	require.Equal(t, strconv.Itoa(second.port), outputs.must(t, "port"))

	client := rds.New(rds.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(env.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:   env.Client,
	})
	described, err := client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(second.identifier)})
	require.NoError(t, err)
	instance := described.DBInstances[0]
	require.Equal(t, outputs.must(t, "arn"), aws.ToString(instance.DBInstanceArn))
	require.Equal(t, "gp3", aws.ToString(instance.StorageType))
	require.Equal(t, int32(3000), aws.ToInt32(instance.Iops))
	require.Equal(t, int32(250), aws.ToInt32(instance.StorageThroughput))
	require.True(t, aws.ToBool(instance.MultiAZ))
	require.Equal(t, "rds-ca-ecc384-g1", aws.ToString(instance.CACertificateIdentifier))
	require.Len(t, instance.OptionGroupMemberships, 1)
	require.Equal(t, "tf-rds-settings-second", aws.ToString(instance.OptionGroupMemberships[0].OptionGroupName))
	require.Equal(t, "in-sync", aws.ToString(instance.OptionGroupMemberships[0].Status))
	_, err = client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(first.identifier)})
	require.Error(t, err, "the old identifier no longer names the instance")

	conn = connect(t, ctx, outputs)
	var entry string
	require.NoError(t, conn.QueryRow(ctx, `SELECT entry FROM ledger`).Scan(&entry))
	require.Equal(t, "before the changes", entry)
	require.NoError(t, conn.Close(ctx))

	env.Terraform(t, append([]string{"plan", "-detailed-exitcode"}, second.vars()...)...)
	env.Terraform(t, append([]string{"destroy", "-auto-approve"}, second.vars()...)...)
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
