package aws_cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// smCLIName returns a per-test unique secret name.
func smCLIName(prefix string) string {
	return prefix + "-" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "-")
}

// smCLICreate creates a secret via the CLI and registers a force-delete cleanup.
func smCLICreate(t *testing.T, name, value string) {
	t.Helper()
	runCLI(t, awsCLI("secretsmanager", "create-secret",
		"--name", name,
		"--secret-string", value,
		"--output", "json"))
	t.Cleanup(func() {
		_ = awsCLI("secretsmanager", "delete-secret",
			"--secret-id", name,
			"--force-delete-without-recovery").Run()
	})
}

func TestSecretsManagerCLI_ResourcePolicy(t *testing.T) {
	name := smCLIName("cli-rp")
	smCLICreate(t, name, "v1")

	const policy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":"secretsmanager:GetSecretValue","Resource":"*"}]}`

	putOut := runCLI(t, awsCLI("secretsmanager", "put-resource-policy",
		"--secret-id", name,
		"--resource-policy", policy,
		"--output", "json"))
	var put struct {
		ARN  string `json:"ARN"`
		Name string `json:"Name"`
	}
	parseJSON(t, putOut, &put)
	assert.Equal(t, name, put.Name)

	getOut := runCLI(t, awsCLI("secretsmanager", "get-resource-policy",
		"--secret-id", name,
		"--output", "json"))
	var get struct {
		ResourcePolicy string `json:"ResourcePolicy"`
	}
	parseJSON(t, getOut, &get)
	assert.JSONEq(t, policy, get.ResourcePolicy)

	delOut := runCLI(t, awsCLI("secretsmanager", "delete-resource-policy",
		"--secret-id", name,
		"--output", "json"))
	var del struct {
		Name string `json:"Name"`
	}
	parseJSON(t, delOut, &del)
	assert.Equal(t, name, del.Name)
}

func TestSecretsManagerCLI_ValidateResourcePolicy(t *testing.T) {
	name := smCLIName("cli-vrp")
	smCLICreate(t, name, "v1")

	out := runCLI(t, awsCLI("secretsmanager", "validate-resource-policy",
		"--secret-id", name,
		"--resource-policy", `{"Version":"2012-10-17","Statement":[]}`,
		"--output", "json"))
	var res struct {
		PolicyValidationPassed bool `json:"PolicyValidationPassed"`
		ValidationErrors       []struct {
			CheckName string `json:"CheckName"`
		} `json:"ValidationErrors"`
	}
	parseJSON(t, out, &res)
	assert.True(t, res.PolicyValidationPassed)
	assert.Empty(t, res.ValidationErrors)
}

func TestSecretsManagerCLI_DeleteRestore(t *testing.T) {
	name := smCLIName("cli-restore")
	smCLICreate(t, name, "v1")

	delOut := runCLI(t, awsCLI("secretsmanager", "delete-secret",
		"--secret-id", name,
		"--recovery-window-in-days", "7",
		"--output", "json"))
	var del struct {
		Name         string `json:"Name"`
		DeletionDate string `json:"DeletionDate"`
	}
	parseJSON(t, delOut, &del)
	assert.Equal(t, name, del.Name)
	assert.NotEmpty(t, del.DeletionDate)

	// Reading the value of a scheduled-for-deletion secret fails.
	require.Error(t, awsCLI("secretsmanager", "get-secret-value", "--secret-id", name).Run())

	restOut := runCLI(t, awsCLI("secretsmanager", "restore-secret",
		"--secret-id", name,
		"--output", "json"))
	var rest struct {
		Name string `json:"Name"`
	}
	parseJSON(t, restOut, &rest)
	assert.Equal(t, name, rest.Name)

	// Now readable again.
	getOut := runCLI(t, awsCLI("secretsmanager", "get-secret-value",
		"--secret-id", name,
		"--output", "json"))
	var get struct {
		SecretString string `json:"SecretString"`
	}
	parseJSON(t, getOut, &get)
	assert.Equal(t, "v1", get.SecretString)
}

func TestSecretsManagerCLI_RotateAndCancel(t *testing.T) {
	name := smCLIName("cli-rotate")
	smCLICreate(t, name, "v1")

	rotOut := runCLI(t, awsCLI("secretsmanager", "rotate-secret",
		"--secret-id", name,
		"--rotation-rules", `{"AutomaticallyAfterDays":30}`,
		"--output", "json"))
	var rot struct {
		Name      string `json:"Name"`
		VersionId string `json:"VersionId"`
	}
	parseJSON(t, rotOut, &rot)
	assert.Equal(t, name, rot.Name)
	require.NotEmpty(t, rot.VersionId)

	descOut := runCLI(t, awsCLI("secretsmanager", "describe-secret",
		"--secret-id", name,
		"--output", "json"))
	var desc struct {
		RotationEnabled bool `json:"RotationEnabled"`
	}
	parseJSON(t, descOut, &desc)
	assert.True(t, desc.RotationEnabled)

	cancelOut := runCLI(t, awsCLI("secretsmanager", "cancel-rotate-secret",
		"--secret-id", name,
		"--output", "json"))
	var cancel struct {
		Name string `json:"Name"`
	}
	parseJSON(t, cancelOut, &cancel)
	assert.Equal(t, name, cancel.Name)
}

func TestSecretsManagerCLI_BatchGetSecretValue(t *testing.T) {
	n1 := smCLIName("cli-batch1")
	n2 := smCLIName("cli-batch2")
	smCLICreate(t, n1, "one")
	smCLICreate(t, n2, "two")

	out := runCLI(t, awsCLI("secretsmanager", "batch-get-secret-value",
		"--secret-id-list", n1, n2,
		"--output", "json"))
	var res struct {
		SecretValues []struct {
			Name         string `json:"Name"`
			SecretString string `json:"SecretString"`
		} `json:"SecretValues"`
	}
	parseJSON(t, out, &res)
	values := map[string]string{}
	for _, sv := range res.SecretValues {
		values[sv.Name] = sv.SecretString
	}
	assert.Equal(t, "one", values[n1])
	assert.Equal(t, "two", values[n2])
}

func TestSecretsManagerCLI_UpdateSecretVersionStage(t *testing.T) {
	name := smCLIName("cli-stage")
	smCLICreate(t, name, "v1")

	v1Out := runCLI(t, awsCLI("secretsmanager", "get-secret-value",
		"--secret-id", name, "--output", "json"))
	var v1 struct {
		VersionId string `json:"VersionId"`
	}
	parseJSON(t, v1Out, &v1)

	putOut := runCLI(t, awsCLI("secretsmanager", "put-secret-value",
		"--secret-id", name, "--secret-string", "v2", "--output", "json"))
	var put struct {
		VersionId string `json:"VersionId"`
	}
	parseJSON(t, putOut, &put)

	// Move AWSCURRENT from v2 back to v1.
	updOut := runCLI(t, awsCLI("secretsmanager", "update-secret-version-stage",
		"--secret-id", name,
		"--version-stage", "AWSCURRENT",
		"--remove-from-version-id", put.VersionId,
		"--move-to-version-id", v1.VersionId,
		"--output", "json"))
	var upd struct {
		Name string `json:"Name"`
	}
	parseJSON(t, updOut, &upd)
	assert.Equal(t, name, upd.Name)

	curOut := runCLI(t, awsCLI("secretsmanager", "get-secret-value",
		"--secret-id", name, "--version-stage", "AWSCURRENT", "--output", "json"))
	var cur struct {
		SecretString string `json:"SecretString"`
	}
	parseJSON(t, curOut, &cur)
	assert.Equal(t, "v1", cur.SecretString)
}

func TestSecretsManagerCLI_Replication(t *testing.T) {
	name := smCLIName("cli-replica")
	smCLICreate(t, name, "v1")

	repOut := runCLI(t, awsCLI("secretsmanager", "replicate-secret-to-regions",
		"--secret-id", name,
		"--add-replica-regions", "Region=us-west-2", "Region=eu-west-1",
		"--output", "json"))
	var rep struct {
		ReplicationStatus []struct {
			Region string `json:"Region"`
			Status string `json:"Status"`
		} `json:"ReplicationStatus"`
	}
	parseJSON(t, repOut, &rep)
	require.Len(t, rep.ReplicationStatus, 2)

	westGet := awsCLI("secretsmanager", "get-secret-value",
		"--secret-id", name, "--output", "json")
	westGet.Env = append(westGet.Env, "AWS_DEFAULT_REGION=us-west-2")
	westOut := runCLI(t, westGet)
	var westValue struct {
		ARN          string `json:"ARN"`
		SecretString string `json:"SecretString"`
	}
	parseJSON(t, westOut, &westValue)
	assert.Contains(t, westValue.ARN, ":secretsmanager:us-west-2:")
	assert.Equal(t, "v1", westValue.SecretString)

	rmOut := runCLI(t, awsCLI("secretsmanager", "remove-regions-from-replication",
		"--secret-id", name,
		"--remove-replica-regions", "us-west-2",
		"--output", "json"))
	var rm struct {
		ReplicationStatus []struct {
			Region string `json:"Region"`
		} `json:"ReplicationStatus"`
	}
	parseJSON(t, rmOut, &rm)
	require.Len(t, rm.ReplicationStatus, 1)
	assert.Equal(t, "eu-west-1", rm.ReplicationStatus[0].Region)

	stopCmd := awsCLI("secretsmanager", "stop-replication-to-replica",
		"--secret-id", name,
		"--output", "json")
	stopCmd.Env = append(stopCmd.Env, "AWS_DEFAULT_REGION=eu-west-1")
	stopOut := runCLI(t, stopCmd)
	var stop struct {
		ARN string `json:"ARN"`
	}
	parseJSON(t, stopOut, &stop)
	assert.Contains(t, stop.ARN, ":secret:"+name+"-")
	assert.Contains(t, stop.ARN, ":secretsmanager:eu-west-1:")
}
