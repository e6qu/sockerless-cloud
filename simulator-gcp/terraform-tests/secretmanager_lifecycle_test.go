package gcp_tf_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTerraformSecretManagerUpdateDelete(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "secretmanager-lifecycle")

	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)

	out, err = runTimed(t, "terraform apply", terraformCmdInDir(fixtureDir, "apply", "-auto-approve", "-var", "secret_label_env=dev"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	out, err = runTimed(t, "terraform apply (update labels)", terraformCmdInDir(fixtureDir, "apply", "-auto-approve", "-var", "secret_label_env=prod"))
	require.NoError(t, err, "terraform apply with updated Secret Manager labels failed:\n%s", out)

	outputs := readOutputsInDir(t, fixtureDir)
	require.Equal(t, "prod", outputs.must(t, "secret_label_env"))
	require.Contains(t, outputs.must(t, "secret_id"), "projects/test-project/secrets/tf-update-secret")

	out, err = runTimed(t, "terraform destroy", terraformCmdInDir(fixtureDir, "destroy", "-auto-approve", "-var", "secret_label_env=prod"))
	require.NoError(t, err, "terraform destroy failed:\n%s", out)
}

func terraformCmdInDir(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("terraform", args...)
	if filepath.IsAbs(dir) {
		cmd.Dir = dir
	} else {
		cmd.Dir = filepath.Join(filepath.Dir(mustAbs("main.tf")), dir)
	}
	// Own process group so runTimed can reap the terraform process tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(),
		"BIGTABLE_EMULATOR_HOST="+grpcEndpoint,
		"TF_VAR_endpoint="+tfEndpoint,
		"TF_VAR_access_token="+accessToken,
	)
	if caCertFile != "" {
		cmd.Env = append(cmd.Env, "SSL_CERT_FILE="+caCertFile)
	}
	return cmd
}

func readOutputsInDir(t *testing.T, dir string) tfOutputs {
	t.Helper()
	out, err := terraformCmdInDir(dir, "output", "-json").CombinedOutput()
	require.NoError(t, err, "terraform output failed:\n%s", out)
	var outputs tfOutputs
	require.NoError(t, json.Unmarshal(out, &outputs))
	return outputs
}
