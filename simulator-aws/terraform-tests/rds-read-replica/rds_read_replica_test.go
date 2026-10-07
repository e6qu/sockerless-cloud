package rds_read_replica_test

import (
	"database/sql"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// An aws_db_instance with replicate_source_db is an RDS for MySQL read replica
// of the source instance: it holds the source's rows, applies the source's
// later writes and refuses writes of its own. The source, created with a major
// version alone and no DB parameter group, runs that major's newest version
// with its family's default group.
func TestRDSReadReplicaTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")
	outputs := readOutputs(t, env)
	require.Equal(t, "8.0.46", outputs.must(t, "source_engine_version"))
	require.Equal(t, "default.mysql8.0", outputs.must(t, "source_parameter_group_name"))
	require.Equal(t, "tf-rds-rr-source", outputs.must(t, "replica_source"))
	require.Equal(t, "mysql", outputs.must(t, "replica_engine"))

	source := openMySQL(t, net.JoinHostPort(outputs.must(t, "source_address"), outputs.must(t, "source_port")))
	replica := openMySQL(t, net.JoinHostPort(outputs.must(t, "replica_address"), outputs.must(t, "replica_port")))
	for _, statement := range []string{
		`CREATE TABLE ledger (entry varchar(64) NOT NULL)`,
		`INSERT INTO ledger VALUES ('written-to-source')`,
	} {
		_, err := source.Exec(statement)
		require.NoError(t, err, statement)
	}
	require.Eventually(t, func() bool {
		var entry string
		return replica.QueryRow(`SELECT entry FROM ledger`).Scan(&entry) == nil && entry == "written-to-source"
	}, 2*time.Minute, 100*time.Millisecond, "the replica applies the source's writes")
	_, err := replica.Exec(`INSERT INTO ledger VALUES ('written-to-replica')`)
	require.Error(t, err, "the replica serves its sessions read-only")

	env.Terraform(t, "destroy", "-auto-approve")
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
