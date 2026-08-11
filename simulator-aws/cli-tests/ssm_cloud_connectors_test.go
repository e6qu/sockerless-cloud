package aws_cli_test

import (
	"strings"
	"testing"
)

// TestSSMCLI_CloudConnectors covers the cloud-connector control plane through
// the AWS Command Line Interface: create, get, list (with the Azure
// subscription filter), update, validate and delete.
func TestSSMCLI_CloudConnectors(t *testing.T) {
	const tenant = "1c11e0a0-0000-4000-8000-000000001111"
	const subscription = "1c11e0a0-0000-4000-8000-000000003333"

	roleName := "SSMCLICloudConnectorRole"
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ssm.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	_ = awsCLI("iam", "create-role", "--role-name", roleName,
		"--assume-role-policy-document", trust).Run()
	roleArn := strings.TrimSpace(runCLI(t, awsCLI("iam", "get-role", "--role-name", roleName,
		"--query", "Role.Arn", "--output", "text")))

	config := map[string]any{
		"AzureConfiguration": map[string]any{
			"TenantId":               tenant,
			"TenantDisplayName":      "cli-tenant",
			"ApplicationId":          "1c11e0a0-0000-4000-8000-000000005555",
			"ApplicationDisplayName": "cli-app",
			"Targets": map[string]any{
				"Subscriptions": []map[string]any{
					{"Id": subscription, "DisplayName": "cli-subscription"},
				},
			},
		},
	}
	configFile := writeJSONDoc(t, "cloud-connector.json", config)

	id := strings.TrimSpace(runCLI(t, awsCLI("ssm", "create-cloud-connector",
		"--display-name", "cli-azure-connector",
		"--description", "Azure nodes for Systems Manager",
		"--role-arn", roleArn,
		"--config-connector-arn", "arn:aws:config:us-east-1:000000000000:connector/azure",
		"--configuration", "file://"+configFile,
		"--query", "CloudConnectorId", "--output", "text")))
	if id == "" {
		t.Fatal("create-cloud-connector returned no CloudConnectorId")
	}
	defer func() {
		_ = awsCLI("ssm", "delete-cloud-connector", "--cloud-connector-id", id).Run()
		_ = awsCLI("iam", "delete-role", "--role-name", roleName).Run()
	}()

	display := strings.TrimSpace(runCLI(t, awsCLI("ssm", "get-cloud-connector",
		"--cloud-connector-id", id, "--query", "DisplayName", "--output", "text")))
	if display != "cli-azure-connector" {
		t.Fatalf("DisplayName = %q", display)
	}
	gotTenant := strings.TrimSpace(runCLI(t, awsCLI("ssm", "get-cloud-connector",
		"--cloud-connector-id", id,
		"--query", "Configuration.AzureConfiguration.TenantId", "--output", "text")))
	if gotTenant != tenant {
		t.Fatalf("Configuration.AzureConfiguration.TenantId = %q, want %q", gotTenant, tenant)
	}

	listed := strings.TrimSpace(runCLI(t, awsCLI("ssm", "list-cloud-connectors",
		"--filters", "FilterKey=SubscriptionId,FilterValues="+subscription,
		"--query", "length(CloudConnectors)", "--output", "text")))
	if listed != "1" {
		t.Fatalf("list-cloud-connectors length = %q, want 1", listed)
	}
	none := strings.TrimSpace(runCLI(t, awsCLI("ssm", "list-cloud-connectors",
		"--filters", "FilterKey=TenantId,FilterValues=no-such-tenant",
		"--query", "length(CloudConnectors)", "--output", "text")))
	if none != "0" {
		t.Fatalf("filtered list length = %q, want 0", none)
	}

	runCLI(t, awsCLI("ssm", "update-cloud-connector",
		"--cloud-connector-id", id, "--description", "updated by the CLI"))
	desc := strings.TrimSpace(runCLI(t, awsCLI("ssm", "get-cloud-connector",
		"--cloud-connector-id", id, "--query", "Description", "--output", "text")))
	if desc != "updated by the CLI" {
		t.Fatalf("Description = %q", desc)
	}

	// The role exists and trusts Systems Manager, so validation reports the
	// informational tenant/subscription findings and no error.
	errors := strings.TrimSpace(runCLI(t, awsCLI("ssm", "validate-cloud-connector",
		"--cloud-connector-id", id,
		"--query", "length(ValidationFindings[?Type=='ERROR'])", "--output", "text")))
	if errors != "0" {
		t.Fatalf("validate-cloud-connector reported %q error finding(s) for a valid connector", errors)
	}
	scope := strings.TrimSpace(runCLI(t, awsCLI("ssm", "validate-cloud-connector",
		"--cloud-connector-id", id,
		"--query", "ValidationFindings[?Code=='SubscriptionAccessible'].Scope.Id | [0]", "--output", "text")))
	if scope != subscription {
		t.Fatalf("SubscriptionAccessible scope id = %q, want %q", scope, subscription)
	}

	runCLI(t, awsCLI("ssm", "delete-cloud-connector", "--cloud-connector-id", id))
	if err := awsCLI("ssm", "get-cloud-connector", "--cloud-connector-id", id).Run(); err == nil {
		t.Fatal("get-cloud-connector should fail after delete")
	}
}
