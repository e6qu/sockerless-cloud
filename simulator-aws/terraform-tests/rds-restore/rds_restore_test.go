package rds_restore_test

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

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
	env.AWSQuery(t, "rds", url.Values{
		"Action":               {"CreateDBInstance"},
		"Version":              {"2014-10-31"},
		"DBInstanceIdentifier": {"tf-rds-restore-source"},
		"DBInstanceClass":      {"db.t3.micro"},
		"Engine":               {"postgres"},
		"EngineVersion":        {"17.5"},
		"MasterUsername":       {"admin"},
		"MasterUserPassword":   {"password123!"},
		"AllocatedStorage":     {"20"},
		"SkipFinalSnapshot":    {"true"},
		"ApplyImmediately":     {"true"},
	})
	env.AWSQuery(t, "rds", url.Values{
		"Action":               {"CreateDBSnapshot"},
		"Version":              {"2014-10-31"},
		"DBInstanceIdentifier": {"tf-rds-restore-source"},
		"DBSnapshotIdentifier": {"tf-rds-snapshot-source"},
	})
	t.Cleanup(func() {
		env.AWSQuery(t, "rds", url.Values{
			"Action":               {"DeleteDBSnapshot"},
			"Version":              {"2014-10-31"},
			"DBSnapshotIdentifier": {"tf-rds-snapshot-source"},
		})
		env.AWSQuery(t, "rds", url.Values{
			"Action":               {"DeleteDBInstance"},
			"Version":              {"2014-10-31"},
			"DBInstanceIdentifier": {"tf-rds-restore-source"},
			"SkipFinalSnapshot":    {"true"},
		})
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
