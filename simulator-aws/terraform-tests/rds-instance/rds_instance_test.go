package rds_instance_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

func TestRDSInstanceTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")

	outputs := readOutputs(t, env)
	require.True(t, strings.HasPrefix(outputs.must(t, "rds_instance_arn"), "arn:aws:rds:us-east-1:"),
		"RDS instance ARN must include the rds-region prefix")
	require.Contains(t, outputs.must(t, "rds_instance_arn"), ":db:tf-rds-db",
		"RDS instance ARN must end with :db:<identifier>")
	require.Equal(t, "postgres", outputs.must(t, "rds_instance_engine"),
		"RDS engine must round-trip through terraform-provider-aws refresh")
	port, err := strconv.Atoi(outputs.must(t, "rds_instance_port"))
	require.NoError(t, err)
	require.Positive(t, port, "RDS endpoint port must round-trip through provider refresh")
	require.Equal(t, "terraform", outputs.must(t, "rds_instance_tags_env"),
		"RDS tags must round-trip through ListTagsForResource")
	require.True(t, strings.HasPrefix(outputs.must(t, "rds_replicated_backup_arn"), "arn:aws:rds:us-west-2:123456789012:auto-backup:ab-"),
		"StartDBInstanceAutomatedBackupsReplication must create the replicated automated backup in the destination Region")
	require.Equal(t, "3", outputs.must(t, "rds_replicated_backup_retention_period"),
		"the replicated automated backup's retention period must round-trip through DescribeDBInstanceAutomatedBackups")
	require.True(t, strings.HasPrefix(outputs.must(t, "rds_first_automated_snapshot_id"), "rds:tf-rds-db-"),
		"Amazon RDS must take the instance's first automated snapshot without any client connecting")
	require.Equal(t, "available", outputs.must(t, "rds_first_automated_snapshot_status"))

	require.Equal(t, "true", outputs.must(t, "rds_mysql_iam_enabled"))
	signsInIAMUsers(t, net.JoinHostPort(outputs.must(t, "rds_mysql_iam_address"), outputs.must(t, "rds_mysql_iam_port")))

	env.Terraform(t, "destroy", "-auto-approve")
}

// signsInIAMUsers holds the RDS for MySQL instance Terraform created with
// iam_database_authentication_enabled to IAM database authentication: a user
// identified with AWSAuthenticationPlugin signs in with an IAM authentication
// token as itself, and the master user does not sign in with one.
func signsInIAMUsers(t *testing.T, endpoint string) {
	t.Helper()
	ctx := context.Background()
	open := func(user, password string) *sql.DB {
		config := mysql.Config{
			User: user, Passwd: password, Net: "tcp", Addr: endpoint, DBName: "application",
			TLSConfig: "skip-verify", AllowCleartextPasswords: true,
		}
		db, err := sql.Open("mysql", config.FormatDSN())
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	token := func(user string) string {
		token, err := auth.BuildAuthToken(ctx, endpoint, "us-east-1", user, credentials.NewStaticCredentialsProvider("test", "test", ""))
		require.NoError(t, err)
		return token
	}
	master := open("dbadmin", "MasterPassword-123!")
	for _, statement := range []string{
		`CREATE USER 'app_iam'@'%' IDENTIFIED WITH AWSAuthenticationPlugin AS 'RDS'`,
		`GRANT SELECT ON application.* TO 'app_iam'@'%'`,
	} {
		_, err := master.ExecContext(ctx, statement)
		require.NoError(t, err, statement)
	}
	var user string
	require.NoError(t, open("app_iam", token("app_iam")).QueryRowContext(ctx, `SELECT CURRENT_USER()`).Scan(&user))
	require.Equal(t, "app_iam@%", user, "the session runs as the user the token names")
	var refusal *mysql.MySQLError
	err := open("dbadmin", token("dbadmin")).PingContext(ctx)
	require.True(t, errors.As(err, &refusal), "the master user does not sign in with a token: %v", err)
	require.Equal(t, uint16(1045), refusal.Number)
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
