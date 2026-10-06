package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliScopedUser creates an IAM user holding one inline policy and returns its
// access key.
func cliScopedUser(t *testing.T, user, policy string) (akid, secret string) {
	t.Helper()
	user = fmt.Sprintf("%s-%d", user, time.Now().UnixNano())
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() { _ = awsCLI("iam", "delete-user", "--user-name", user).Run() })
	runCLI(t, awsCLI("iam", "put-user-policy", "--user-name", user, "--policy-name", "scoped",
		"--policy-document", policy))
	var key struct {
		AccessKey struct{ AccessKeyId, SecretAccessKey string }
	}
	parseJSON(t, runCLI(t, awsCLI("iam", "create-access-key", "--user-name", user, "--output", "json")), &key)
	return key.AccessKey.AccessKeyId, key.AccessKey.SecretAccessKey
}

func cliAs(akid, secret string, args ...string) *exec.Cmd {
	cmd := awsCLI(args...)
	cmd.Env = withCreds(cmd.Env, akid, secret)
	return cmd
}

// TestCloudWatch_DefaultDatasetResourceTagCLI covers the default metrics
// dataset over the CLI: it exists without being created, no other dataset
// does, and aws:ResourceTag on get-dataset reports the tags tag-resource gives
// it.
func TestCloudWatch_DefaultDatasetResourceTagCLI(t *testing.T) {
	var dataset struct{ DatasetId, Arn string }
	parseJSON(t, runCLI(t, awsCLI("cloudwatch", "get-dataset", "--dataset-identifier", "default", "--output", "json")), &dataset)
	assert.Equal(t, "default", dataset.DatasetId)
	out := runCLIExpectError(t, awsCLI("cloudwatch", "get-dataset", "--dataset-identifier",
		"arn:aws:cloudwatch:us-east-1:123456789012:dataset/other"))
	assert.Contains(t, out, "ResourceNotFoundException")

	akid, secret := cliScopedUser(t, "cli-cw-dataset", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Action":"cloudwatch:GetDataset","Resource":"*","Condition":{"StringEquals":{"aws:ResourceTag/stage":"prod"}}}]}`)
	runCLI(t, awsCLI("cloudwatch", "untag-resource", "--resource-arn", dataset.Arn, "--tag-keys", "stage"))
	out = runCLIExpectError(t, cliAs(akid, secret, "cloudwatch", "get-dataset", "--dataset-identifier", "default"))
	assert.Contains(t, out, "AccessDenied")

	runCLI(t, awsCLI("cloudwatch", "tag-resource", "--resource-arn", dataset.Arn, "--tags", "Key=stage,Value=prod"))
	t.Cleanup(func() {
		_ = awsCLI("cloudwatch", "untag-resource", "--resource-arn", dataset.Arn, "--tag-keys", "stage").Run()
	})
	runCLI(t, cliAs(akid, secret, "cloudwatch", "get-dataset", "--dataset-identifier", "default"))
}

// TestLambda_FunctionURLPermissionConditionsCLI covers the conditions
// add-permission writes for a function URL: the auth type a public URL is
// opened with, and lambda:InvokedViaFunctionUrl on the invoke it needs too.
func TestLambda_FunctionURLPermissionConditionsCLI(t *testing.T) {
	zipPath := createDummyZip(t)
	fn := fmt.Sprintf("cli-url-perm-%d", time.Now().UnixNano())
	var created struct{ FunctionArn string }
	parseJSON(t, runCLI(t, awsCLI("lambda", "create-function", "--function-name", fn, "--runtime", "nodejs18.x",
		"--role", "arn:aws:iam::123456789012:role/test-role", "--handler", "index.handler",
		"--zip-file", "fileb://"+zipPath, "--output", "json")), &created)
	t.Cleanup(func() { _ = awsCLI("lambda", "delete-function", "--function-name", fn).Run() })

	runCLI(t, awsCLI("lambda", "add-permission", "--function-name", created.FunctionArn,
		"--statement-id", "public-url", "--action", "lambda:InvokeFunctionUrl", "--principal", "*",
		"--function-url-auth-type", "NONE"))
	runCLI(t, awsCLI("lambda", "add-permission", "--function-name", created.FunctionArn,
		"--statement-id", "public-invoke", "--action", "lambda:InvokeFunction", "--principal", "*",
		"--invoked-via-function-url"))

	var policy struct{ Policy string }
	parseJSON(t, runCLI(t, awsCLI("lambda", "get-policy", "--function-name", fn, "--output", "json")), &policy)
	var document struct {
		Statement []struct {
			Sid       string
			Condition map[string]map[string]string
		}
	}
	require.NoError(t, json.Unmarshal([]byte(policy.Policy), &document))
	conditions := map[string]map[string]map[string]string{}
	for _, statement := range document.Statement {
		conditions[statement.Sid] = statement.Condition
	}
	assert.Equal(t, "NONE", conditions["public-url"]["StringEquals"]["lambda:FunctionUrlAuthType"])
	assert.Equal(t, "true", conditions["public-invoke"]["Bool"]["lambda:InvokedViaFunctionUrl"])
}

// TestRDSCLI_BlueGreenSourceConditionKeys covers the keys a blue/green
// deployment settles from the DB cluster it clones.
func TestRDSCLI_BlueGreenSourceConditionKeys(t *testing.T) {
	cluster := func(team string) string {
		id := fmt.Sprintf("cli-bg-source-%s-%d", team, time.Now().UnixNano())
		var out struct{ DBCluster struct{ DBClusterArn string } }
		parseJSON(t, runCLI(t, awsCLI("rds", "create-db-cluster", "--db-cluster-identifier", id,
			"--engine", "mysql", "--master-username", "admin", "--db-cluster-instance-class", "db.m6gd.large",
			"--allocated-storage", "100", "--storage-encrypted", "--tags", "Key=team,Value="+team, "--output", "json")), &out)
		t.Cleanup(func() {
			_ = awsCLI("rds", "delete-db-cluster", "--db-cluster-identifier", id, "--skip-final-snapshot").Run()
		})
		return out.DBCluster.DBClusterArn
	}
	data, web := cluster("data"), cluster("web")
	akid, secret := cliScopedUser(t, "cli-rds-bg", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Action":"rds:CreateBlueGreenDeployment","Resource":"*","Condition":{
		"StringEquals":{"rds:DatabaseEngine":"mysql","rds:cluster-tag/team":"data"},"Bool":{"rds:StorageEncrypted":"true"}}}]}`)
	create := func(source string) *exec.Cmd {
		return cliAs(akid, secret, "rds", "create-blue-green-deployment", "--blue-green-deployment-name",
			fmt.Sprintf("cli-bg-%d", time.Now().UnixNano()), "--source", source, "--output", "json")
	}
	var deployment struct {
		BlueGreenDeployment struct{ BlueGreenDeploymentIdentifier string }
	}
	parseJSON(t, runCLI(t, create(data)), &deployment)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-blue-green-deployment", "--blue-green-deployment-identifier",
			deployment.BlueGreenDeployment.BlueGreenDeploymentIdentifier).Run()
	})
	out := runCLIExpectError(t, create(web))
	assert.True(t, strings.Contains(out, "AccessDenied") || strings.Contains(out, "not authorized"), out)
}

// TestOrganizations_ListPoliciesFilterCLI covers organizations:PolicyType on a
// policy listing, which states the type in its filter.
func TestOrganizations_ListPoliciesFilterCLI(t *testing.T) {
	akid, secret := cliScopedUser(t, "cli-org-scp", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Action":"organizations:ListPolicies","Resource":"*",
		"Condition":{"StringEquals":{"organizations:PolicyType":"SERVICE_CONTROL_POLICY"}}}]}`)
	runCLI(t, cliAs(akid, secret, "organizations", "list-policies", "--filter", "SERVICE_CONTROL_POLICY"))
	out := runCLIExpectError(t, cliAs(akid, secret, "organizations", "list-policies", "--filter", "TAG_POLICY"))
	assert.True(t, strings.Contains(out, "AccessDenied") || strings.Contains(out, "not authorized"), out)
}
