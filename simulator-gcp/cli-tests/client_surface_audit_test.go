package gcp_cli_test

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	const destination = "bigquery.googleapis.com/projects/test-project/datasets/audit_logs"
	runCLI(t, gcloudCLI("logging", "sinks", "create", "cli-audit-sink",
		destination,
		"--log-filter=severity >= WARNING",
		"--format=json",
		"--quiet"))

	list := runCLI(t, gcloudCLI("logging", "sinks", "list", "--format=json"))
	assert.Contains(t, list, "cli-audit-sink")

	// The described sink carries the destination and filter the create sent.
	out := runCLI(t, gcloudCLI("logging", "sinks", "describe", "cli-audit-sink", "--format=json"))
	var sink struct {
		Name        string `json:"name"`
		Destination string `json:"destination"`
		Filter      string `json:"filter"`
	}
	parseJSONObject(t, out, &sink)
	assert.Equal(t, "cli-audit-sink", sink.Name)
	assert.Equal(t, destination, sink.Destination)
	assert.Equal(t, "severity >= WARNING", sink.Filter)

	runCLI(t, gcloudCLI("logging", "sinks", "delete", "cli-audit-sink", "--quiet"))

	// The delete removed the sink: describing it fails and the list no longer
	// carries it.
	gone, err := gcloudCLI("logging", "sinks", "describe", "cli-audit-sink", "--format=json").CombinedOutput()
	require.Error(t, err, "describing a deleted sink must fail: %s", gone)
	assert.NotContains(t,
		runCLI(t, gcloudCLI("logging", "sinks", "list", "--format=json")),
		"cli-audit-sink")
}

// cliLoggingMetric is the subset of the logging/v2 LogMetric this suite reads
// back. Cloud Logging returns the short metric id in `name`.
type cliLoggingMetric struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Filter      string `json:"filter"`
}

func TestCLI_LoggingMetricCRUD(t *testing.T) {
	runCLI(t, gcloudCLI("logging", "metrics", "create", "cli-audit-metric",
		"--description=CLI audit metric",
		"--log-filter=severity >= ERROR",
		"--format=json",
		"--quiet"))

	list := runCLI(t, gcloudCLI("logging", "metrics", "list", "--format=json"))
	assert.Contains(t, list, "cli-audit-metric")

	describe := func() cliLoggingMetric {
		t.Helper()
		var metric cliLoggingMetric
		parseJSONObject(t,
			runCLI(t, gcloudCLI("logging", "metrics", "describe", "cli-audit-metric", "--format=json")),
			&metric)
		return metric
	}

	created := describe()
	assert.Equal(t, "cli-audit-metric", created.Name)
	assert.Equal(t, "CLI audit metric", created.Description)
	assert.Equal(t, "severity >= ERROR", created.Filter)

	runCLI(t, gcloudCLI("logging", "metrics", "update", "cli-audit-metric",
		"--description=Updated CLI audit metric",
		"--format=json",
		"--quiet"))

	// The update is read back off the metric: it moved the description and
	// left the filter it did not name alone.
	updated := describe()
	assert.Equal(t, "Updated CLI audit metric", updated.Description,
		"the update must be visible on a fresh read")
	assert.Equal(t, "severity >= ERROR", updated.Filter,
		"the update names only the description; the filter stands")

	runCLI(t, gcloudCLI("logging", "metrics", "delete", "cli-audit-metric", "--quiet"))

	gone, err := gcloudCLI("logging", "metrics", "describe", "cli-audit-metric", "--format=json").CombinedOutput()
	require.Error(t, err, "describing a deleted metric must fail: %s", gone)
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

	// The echo the add printed is the response body, not proof of a write —
	// a fresh get-iam-policy has to report the binding as well.
	out = runCLI(t, gcloudCLI("projects", "get-iam-policy", "test-project", "--format=json"))
	var reread struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
	}
	parseJSON(t, out, &reread)
	found := false
	for _, b := range reread.Bindings {
		if b.Role == "roles/viewer" &&
			slices.Contains(b.Members, "serviceAccount:robot@test-project.iam.gserviceaccount.com") {
			found = true
		}
	}
	assert.True(t, found, "the binding must be readable back off the project policy: %s", out)
}

func TestCLI_IAMServiceAccountKeys(t *testing.T) {
	// Create a service account to own the keys.
	runCLI(t, gcloudCLI("iam", "service-accounts", "create", "cli-key-sa",
		"--display-name=CLI Key SA",
		"--format=json",
		"--quiet"))
	saEmail := "cli-key-sa@test-project.iam.gserviceaccount.com"

	// Create a key. gcloud decodes privateKeyData and writes the JSON key file
	// an application would authenticate with.
	keyFile := filepath.Join(tmpDir, "sa-key.json")
	runCLI(t, gcloudCLI("iam", "service-accounts", "keys", "create", keyFile,
		"--iam-account="+saEmail,
		"--format=json",
		"--quiet"))

	// The written file is the whole point of the command: it has to be a
	// service-account key document carrying a usable private key, not an
	// empty or truncated file.
	keyBytes, err := os.ReadFile(keyFile)
	require.NoError(t, err, "keys create must write the key file it was given")
	var key struct {
		Type         string `json:"type"`
		ProjectID    string `json:"project_id"`
		PrivateKeyID string `json:"private_key_id"`
		PrivateKey   string `json:"private_key"`
		ClientEmail  string `json:"client_email"`
		TokenURI     string `json:"token_uri"`
	}
	require.NoError(t, json.Unmarshal(keyBytes, &key), "key file: %s", keyBytes)
	assert.Equal(t, "service_account", key.Type)
	assert.Equal(t, "test-project", key.ProjectID)
	assert.Equal(t, saEmail, key.ClientEmail)
	assert.NotEmpty(t, key.PrivateKeyID)
	assert.NotEmpty(t, key.TokenURI)
	block, _ := pem.Decode([]byte(key.PrivateKey))
	require.NotNil(t, block, "private_key must be PEM: %q", key.PrivateKey)
	assert.Equal(t, "PRIVATE KEY", block.Type)
	_, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	require.NoError(t, err, "private_key must be a parseable PKCS#8 key")

	// List keys — the key just minted is there, user-managed.
	list := runCLI(t, gcloudCLI("iam", "service-accounts", "keys", "list",
		"--iam-account="+saEmail,
		"--format=json"))
	assert.Contains(t, list, "USER_MANAGED")
	assert.Contains(t, list, key.PrivateKeyID,
		"the listing must hold the key the create minted")

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

	// List instance templates — by the resource's own name field, not by the
	// string appearing anywhere in the document.
	list := runCLI(t, gcloudCLI("compute", "instance-templates", "list", "--format=json"))
	var listed []struct {
		Name string `json:"name"`
	}
	parseJSON(t, list, &listed)
	names := make([]string, 0, len(listed))
	for _, tmpl := range listed {
		names = append(names, tmpl.Name)
	}
	assert.Contains(t, names, "cli-audit-tmpl")

	// Describe — the template carries the machine type the CLI built into its
	// properties, which is the whole of what --machine-type does.
	desc := runCLI(t, gcloudCLI("compute", "instance-templates", "describe", "cli-audit-tmpl",
		"--format=json"))
	var described struct {
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		SelfLink   string `json:"selfLink"`
		Properties struct {
			MachineType string `json:"machineType"`
		} `json:"properties"`
	}
	parseJSONObject(t, desc, &described)
	assert.Equal(t, "compute#instanceTemplate", described.Kind)
	assert.Equal(t, "cli-audit-tmpl", described.Name)
	assert.Equal(t, "n1-standard-1", described.Properties.MachineType)
	assert.True(t, strings.HasSuffix(described.SelfLink, "/global/instanceTemplates/cli-audit-tmpl"),
		"selfLink addresses the template: %s", described.SelfLink)

	// Delete.
	runCLI(t, gcloudCLI("compute", "instance-templates", "delete", "cli-audit-tmpl",
		"--quiet"))

	gone, err := gcloudCLI("compute", "instance-templates", "describe", "cli-audit-tmpl",
		"--format=json").CombinedOutput()
	require.Error(t, err, "describing a deleted instance template must fail: %s", gone)
}
