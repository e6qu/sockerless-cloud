package rds_blue_green_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// An aws_db_instance with blue_green_update applies a DB parameter group
// change through a blue/green deployment: the instance keeps
// its identifier and endpoint, now served by the green instance with a new
// DbiResourceId and every row the blue instance held, and the provider
// deletes the blue instance the switchover renamed and the deployment.
func TestRDSBlueGreenTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	vars := func(version, group string) []string {
		return []string{"-var", "engine_version=" + version, "-var", "parameter_group_name=" + group}
	}
	env.Terraform(t, append([]string{"apply", "-auto-approve"}, vars("8.0.46", "blue")...)...)
	blue := readOutputs(t, env)
	require.Equal(t, "8.0.46", blue.must(t, "engine_version"))
	require.Equal(t, "tf-rds-bg-blue", blue.must(t, "parameter_group_name"))
	endpoint := net.JoinHostPort(blue.must(t, "address"), blue.must(t, "port"))
	db := openMySQL(t, endpoint)
	for _, statement := range []string{
		`CREATE TABLE ledger (entry varchar(64) NOT NULL)`,
		`INSERT INTO ledger VALUES ('written-to-blue')`,
	} {
		_, err := db.Exec(statement)
		require.NoError(t, err, statement)
	}

	env.Terraform(t, append([]string{"apply", "-auto-approve"}, vars("8.0.46", "green")...)...)
	// The outputs were planned before the switchover replaced the instance's
	// resource ID; a refresh reads the instance now in production.
	env.Terraform(t, append([]string{"apply", "-refresh-only", "-auto-approve"}, vars("8.0.46", "green")...)...)
	green := readOutputs(t, env)
	require.Equal(t, "8.0.46", green.must(t, "engine_version"))
	require.Equal(t, "tf-rds-bg-green", green.must(t, "parameter_group_name"))
	require.NotEqual(t, blue.must(t, "resource_id"), green.must(t, "resource_id"),
		"the green instance took over the identifier")
	require.Equal(t, endpoint, net.JoinHostPort(green.must(t, "address"), green.must(t, "port")),
		"the green instance took over the endpoint")
	var entry string
	require.NoError(t, openMySQL(t, endpoint).QueryRow(`SELECT entry FROM ledger`).Scan(&entry))
	require.Equal(t, "written-to-blue", entry)

	client := rds.New(rds.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(env.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:   env.Client,
	})
	ctx := context.Background()
	_, err := client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String("tf-rds-bg-old1")})
	var notFound *types.DBInstanceNotFoundFault
	require.True(t, errors.As(err, &notFound), "the provider deletes the renamed blue instance: %v", err)
	deployments, err := client.DescribeBlueGreenDeployments(ctx, &rds.DescribeBlueGreenDeploymentsInput{})
	require.NoError(t, err)
	require.Empty(t, deployments.BlueGreenDeployments, "the provider deletes the deployment")

	env.Terraform(t, append([]string{"destroy", "-auto-approve"}, vars("8.0.46", "green")...)...)
}

func openMySQL(t *testing.T, endpoint string) *sql.DB {
	t.Helper()
	config := mysql.Config{
		User: "dbadmin", Passwd: "MasterPassword-123!", Net: "tcp", Addr: endpoint, DBName: "application",
		TLSConfig: "skip-verify", AllowCleartextPasswords: true,
	}
	db, err := sql.Open("mysql", config.FormatDSN())
	require.NoError(t, err)
	db.SetMaxIdleConns(0)
	t.Cleanup(func() { _ = db.Close() })
	return db
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
