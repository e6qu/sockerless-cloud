package gcp_cli_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLI_IAMServiceAccounts(t *testing.T) {
	out := runCLI(t, gcloudCLI("iam", "service-accounts", "create", "cli-audit-sa",
		"--display-name=CLI Audit SA",
		"--format=json",
		"--quiet"))
	assert.Contains(t, out, "cli-audit-sa@test-project.iam.gserviceaccount.com")

	list := runCLI(t, gcloudCLI("iam", "service-accounts", "list", "--format=json"))
	assert.Contains(t, list, "cli-audit-sa@test-project.iam.gserviceaccount.com")

	runCLI(t, gcloudCLI("iam", "service-accounts", "delete",
		"cli-audit-sa@test-project.iam.gserviceaccount.com",
		"--quiet"))
}

func TestCLI_PubSubTopicSubscriptionLifecycle(t *testing.T) {
	runCLI(t, gcloudCLI("pubsub", "topics", "create", "cli-audit-topic", "--format=json", "--quiet"))
	t.Cleanup(func() {
		_ = gcloudCLI("pubsub", "topics", "delete", "cli-audit-topic", "--quiet").Run()
	})

	runCLI(t, gcloudCLI("pubsub", "subscriptions", "create", "cli-audit-sub",
		"--topic=cli-audit-topic",
		"--format=json",
		"--quiet"))
	t.Cleanup(func() {
		_ = gcloudCLI("pubsub", "subscriptions", "delete", "cli-audit-sub", "--quiet").Run()
	})

	runCLI(t, gcloudCLI("pubsub", "topics", "publish", "cli-audit-topic", "--message=hello"))
	pulled := runCLI(t, gcloudCLI("pubsub", "subscriptions", "pull", "cli-audit-sub",
		"--limit=1",
		"--auto-ack",
		"--format=json"))
	var messages []struct {
		Message struct {
			Data string `json:"data"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal([]byte(pulled), &messages))
	require.Len(t, messages, 1)
	data, err := base64.StdEncoding.DecodeString(messages[0].Message.Data)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}

func TestCLI_APIGatewayApis(t *testing.T) {
	runCLI(t, gcloudCLI("api-gateway", "apis", "create", "cli-audit-api",
		"--display-name=CLI Audit API",
		"--async",
		"--format=json",
		"--quiet"))
	t.Cleanup(func() {
		_ = gcloudCLI("api-gateway", "apis", "delete", "cli-audit-api", "--async", "--quiet").Run()
	})

	list := runCLI(t, gcloudCLI("api-gateway", "apis", "list", "--format=json"))
	assert.Contains(t, list, "cli-audit-api")
}

func TestCLI_CloudBuildTriggers(t *testing.T) {
	configPath := filepath.Join(tmpDir, "cloudbuild-trigger.json")
	err := os.WriteFile(configPath, []byte(`{
  "name": "cli-audit-build-trigger",
  "filename": "cloudbuild.yaml",
  "triggerTemplate": {
    "repoName": "cli-repo",
    "branchName": "main"
  }
}`), 0o644)
	require.NoError(t, err)

	created := runCLI(t, gcloudCLI("builds", "triggers", "create", "manual",
		"--trigger-config="+configPath,
		"--format=json",
		"--quiet"))
	assert.Contains(t, created, "cli-audit-build-trigger")

	list := runCLI(t, gcloudCLI("builds", "triggers", "list", "--format=json"))
	assert.Contains(t, list, "cli-audit-build-trigger")

	var trigger struct {
		ID string `json:"id"`
	}
	parseJSON(t, created, &trigger)
	require.NotEmpty(t, trigger.ID)
	runCLI(t, gcloudCLI("builds", "triggers", "delete", trigger.ID, "--quiet"))
}

func TestCLI_LoggingSinkCRUD(t *testing.T) {
	runCLI(t, gcloudCLI("logging", "sinks", "create", "cli-audit-sink",
		"bigquery.googleapis.com/projects/test-project/datasets/audit_logs",
		"--log-filter=severity >= WARNING",
		"--format=json",
		"--quiet"))

	list := runCLI(t, gcloudCLI("logging", "sinks", "list", "--format=json"))
	assert.Contains(t, list, "cli-audit-sink")

	runCLI(t, gcloudCLI("logging", "sinks", "describe", "cli-audit-sink", "--format=json"))

	runCLI(t, gcloudCLI("logging", "sinks", "delete", "cli-audit-sink", "--quiet"))
}

func TestCLI_LoggingMetricCRUD(t *testing.T) {
	runCLI(t, gcloudCLI("logging", "metrics", "create", "cli-audit-metric",
		"--description=CLI audit metric",
		"--log-filter=severity >= ERROR",
		"--format=json",
		"--quiet"))

	list := runCLI(t, gcloudCLI("logging", "metrics", "list", "--format=json"))
	assert.Contains(t, list, "cli-audit-metric")

	runCLI(t, gcloudCLI("logging", "metrics", "describe", "cli-audit-metric", "--format=json"))

	runCLI(t, gcloudCLI("logging", "metrics", "update", "cli-audit-metric",
		"--description=Updated CLI audit metric",
		"--format=json",
		"--quiet"))

	runCLI(t, gcloudCLI("logging", "metrics", "delete", "cli-audit-metric", "--quiet"))
}

func TestCLI_ProjectIAMPolicy(t *testing.T) {
	// get-iam-policy returns the current policy (initially empty bindings).
	out := runCLI(t, gcloudCLI("projects", "get-iam-policy", "test-project", "--format=json"))
	var policy struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
		Etag string `json:"etag"`
	}
	parseJSON(t, out, &policy)
	assert.NotEmpty(t, policy.Etag)

	// add-iam-policy-binding adds a binding.
	bound := runCLI(t, gcloudCLI("projects", "add-iam-policy-binding", "test-project",
		"--member=serviceAccount:robot@test-project.iam.gserviceaccount.com",
		"--role=roles/viewer",
		"--format=json",
		"--quiet"))
	assert.Contains(t, bound, "roles/viewer")
}

func TestCLI_IAMServiceAccountKeys(t *testing.T) {
	// Create a service account to own the keys.
	runCLI(t, gcloudCLI("iam", "service-accounts", "create", "cli-key-sa",
		"--display-name=CLI Key SA",
		"--format=json",
		"--quiet"))
	saEmail := "cli-key-sa@test-project.iam.gserviceaccount.com"

	// Create a key.
	keyFile := filepath.Join(tmpDir, "sa-key.json")
	runCLI(t, gcloudCLI("iam", "service-accounts", "keys", "create", keyFile,
		"--iam-account="+saEmail,
		"--format=json",
		"--quiet"))

	// List keys — must include USER_MANAGED key.
	list := runCLI(t, gcloudCLI("iam", "service-accounts", "keys", "list",
		"--iam-account="+saEmail,
		"--format=json"))
	assert.Contains(t, list, "USER_MANAGED")

	// Cleanup.
	runCLI(t, gcloudCLI("iam", "service-accounts", "delete",
		saEmail, "--quiet"))
}

func TestCLI_ComputeInstanceTemplate(t *testing.T) {
	// Create instance template.
	created := runCLI(t, gcloudCLI("compute", "instance-templates", "create", "cli-audit-tmpl",
		"--machine-type=n1-standard-1",
		"--format=json",
		"--quiet"))
	assert.Contains(t, created, "cli-audit-tmpl")

	// List instance templates.
	list := runCLI(t, gcloudCLI("compute", "instance-templates", "list", "--format=json"))
	assert.Contains(t, list, "cli-audit-tmpl")

	// Describe.
	desc := runCLI(t, gcloudCLI("compute", "instance-templates", "describe", "cli-audit-tmpl",
		"--format=json"))
	assert.Contains(t, desc, "compute#instanceTemplate")

	// Delete.
	runCLI(t, gcloudCLI("compute", "instance-templates", "delete", "cli-audit-tmpl",
		"--quiet"))
}
