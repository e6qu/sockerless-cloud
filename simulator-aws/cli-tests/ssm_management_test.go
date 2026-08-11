package aws_cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ssmStamp() string {
	return strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "-")
}

// ssmInstanceID builds an instance id of the shape AWS Systems Manager admits:
// "i-" followed by exactly eight or seventeen word characters. The stamp
// carries a hyphen, which \w excludes, so it is dropped rather than trimmed
// around.
func ssmInstanceID(prefix string) string {
	id := prefix + strings.ReplaceAll(ssmStamp(), "-", "")
	for len(id) < 17 {
		id += "0"
	}
	return "i-" + id[:17]
}

func TestSSMCLI_DocumentLifecycle(t *testing.T) {
	name := "cli-doc-" + ssmStamp()
	content := `{"schemaVersion":"2.2","mainSteps":[{"action":"aws:runShellScript","name":"s","inputs":{"runCommand":["echo hi"]}}]}`

	createOut := runCLI(t, awsCLI("ssm", "create-document",
		"--name", name,
		"--content", content,
		"--document-type", "Command",
		"--document-format", "JSON",
		"--output", "json"))
	var createResult struct {
		DocumentDescription struct {
			Name            string `json:"Name"`
			DocumentVersion string `json:"DocumentVersion"`
			Status          string `json:"Status"`
		} `json:"DocumentDescription"`
	}
	parseJSON(t, createOut, &createResult)
	require.Equal(t, name, createResult.DocumentDescription.Name)
	require.Equal(t, "1", createResult.DocumentDescription.DocumentVersion)
	require.Equal(t, "Active", createResult.DocumentDescription.Status)
	t.Cleanup(func() {
		_ = awsCLI("ssm", "delete-document", "--name", name).Run()
	})

	getOut := runCLI(t, awsCLI("ssm", "get-document", "--name", name, "--output", "json"))
	var getResult struct {
		Name    string `json:"Name"`
		Content string `json:"Content"`
	}
	parseJSON(t, getOut, &getResult)
	require.Equal(t, content, getResult.Content)

	updateContent := strings.Replace(content, "echo hi", "echo hi2", 1)
	runCLI(t, awsCLI("ssm", "update-document",
		"--name", name,
		"--content", updateContent,
		"--document-version", "$LATEST",
		"--output", "json"))

	lvOut := runCLI(t, awsCLI("ssm", "list-document-versions", "--name", name, "--output", "json"))
	var lvResult struct {
		DocumentVersions []struct {
			DocumentVersion string `json:"DocumentVersion"`
		} `json:"DocumentVersions"`
	}
	parseJSON(t, lvOut, &lvResult)
	require.Len(t, lvResult.DocumentVersions, 2)

	runCLI(t, awsCLI("ssm", "update-document-default-version",
		"--name", name,
		"--document-version", "2",
		"--output", "json"))

	descOut := runCLI(t, awsCLI("ssm", "describe-document", "--name", name, "--output", "json"))
	var descResult struct {
		Document struct {
			DefaultVersion string `json:"DefaultVersion"`
			HashType       string `json:"HashType"`
		} `json:"Document"`
	}
	parseJSON(t, descOut, &descResult)
	require.Equal(t, "2", descResult.Document.DefaultVersion)
	require.Equal(t, "Sha256", descResult.Document.HashType)

	runCLI(t, awsCLI("ssm", "list-documents", "--output", "json"))
}

func TestSSMCLI_MaintenanceWindowLifecycle(t *testing.T) {
	createOut := runCLI(t, awsCLI("ssm", "create-maintenance-window",
		"--name", "cli-mw-"+ssmStamp(),
		"--schedule", "cron(0 16 ? * TUE *)",
		"--duration", "4",
		"--cutoff", "1",
		"--allow-unassociated-targets",
		"--output", "json"))
	var createResult struct {
		WindowId string `json:"WindowId"`
	}
	parseJSON(t, createOut, &createResult)
	windowID := createResult.WindowId
	require.Regexp(t, `^mw-[0-9a-f]{17}$`, windowID)
	t.Cleanup(func() {
		_ = awsCLI("ssm", "delete-maintenance-window", "--window-id", windowID).Run()
	})

	getOut := runCLI(t, awsCLI("ssm", "get-maintenance-window", "--window-id", windowID, "--output", "json"))
	var getResult struct {
		Duration int  `json:"Duration"`
		Enabled  bool `json:"Enabled"`
	}
	parseJSON(t, getOut, &getResult)
	require.Equal(t, 4, getResult.Duration)
	require.True(t, getResult.Enabled)

	runCLI(t, awsCLI("ssm", "update-maintenance-window",
		"--window-id", windowID,
		"--no-enabled",
		"--output", "json"))

	rtOut := runCLI(t, awsCLI("ssm", "register-target-with-maintenance-window",
		"--window-id", windowID,
		"--resource-type", "INSTANCE",
		"--targets", "Key=tag:Env,Values=prod",
		"--output", "json"))
	var rtResult struct {
		WindowTargetId string `json:"WindowTargetId"`
	}
	parseJSON(t, rtOut, &rtResult)
	require.NotEmpty(t, rtResult.WindowTargetId)

	dtOut := runCLI(t, awsCLI("ssm", "describe-maintenance-window-targets", "--window-id", windowID, "--output", "json"))
	var dtResult struct {
		Targets []struct {
			WindowTargetId string `json:"WindowTargetId"`
		} `json:"Targets"`
	}
	parseJSON(t, dtOut, &dtResult)
	require.Len(t, dtResult.Targets, 1)

	rtaskOut := runCLI(t, awsCLI("ssm", "register-task-with-maintenance-window",
		"--window-id", windowID,
		"--task-arn", "AWS-RunShellScript",
		"--task-type", "RUN_COMMAND",
		"--priority", "1",
		"--max-concurrency", "1",
		"--max-errors", "1",
		"--targets", "Key=WindowTargetIds,Values="+rtResult.WindowTargetId,
		"--output", "json"))
	var rtaskResult struct {
		WindowTaskId string `json:"WindowTaskId"`
	}
	parseJSON(t, rtaskOut, &rtaskResult)
	require.NotEmpty(t, rtaskResult.WindowTaskId)

	dtaskOut := runCLI(t, awsCLI("ssm", "describe-maintenance-window-tasks", "--window-id", windowID, "--output", "json"))
	var dtaskResult struct {
		Tasks []struct {
			TaskArn string `json:"TaskArn"`
			Type    string `json:"Type"`
		} `json:"Tasks"`
	}
	parseJSON(t, dtaskOut, &dtaskResult)
	require.Len(t, dtaskResult.Tasks, 1)
	require.Equal(t, "AWS-RunShellScript", dtaskResult.Tasks[0].TaskArn)

	runCLI(t, awsCLI("ssm", "deregister-task-from-maintenance-window",
		"--window-id", windowID, "--window-task-id", rtaskResult.WindowTaskId, "--output", "json"))
	runCLI(t, awsCLI("ssm", "deregister-target-from-maintenance-window",
		"--window-id", windowID, "--window-target-id", rtResult.WindowTargetId, "--output", "json"))

	runCLI(t, awsCLI("ssm", "describe-maintenance-windows", "--output", "json"))
}

func TestSSMCLI_PatchBaselineLifecycle(t *testing.T) {
	createOut := runCLI(t, awsCLI("ssm", "create-patch-baseline",
		"--name", "cli-pb-"+ssmStamp(),
		"--operating-system", "UBUNTU",
		"--description", "cli baseline",
		"--approved-patches", "patch-a", "patch-b",
		"--output", "json"))
	var createResult struct {
		BaselineId string `json:"BaselineId"`
	}
	parseJSON(t, createOut, &createResult)
	baselineID := createResult.BaselineId
	require.NotEmpty(t, baselineID)
	t.Cleanup(func() {
		_ = awsCLI("ssm", "delete-patch-baseline", "--baseline-id", baselineID).Run()
	})

	getOut := runCLI(t, awsCLI("ssm", "get-patch-baseline", "--baseline-id", baselineID, "--output", "json"))
	var getResult struct {
		OperatingSystem string   `json:"OperatingSystem"`
		ApprovedPatches []string `json:"ApprovedPatches"`
	}
	parseJSON(t, getOut, &getResult)
	require.Equal(t, "UBUNTU", getResult.OperatingSystem)
	require.Equal(t, []string{"patch-a", "patch-b"}, getResult.ApprovedPatches)

	runCLI(t, awsCLI("ssm", "update-patch-baseline",
		"--baseline-id", baselineID,
		"--name", "cli-pb-renamed-"+ssmStamp(),
		"--output", "json"))

	runCLI(t, awsCLI("ssm", "describe-patch-baselines", "--output", "json"))

	runCLI(t, awsCLI("ssm", "register-default-patch-baseline", "--baseline-id", baselineID, "--output", "json"))
	gdOut := runCLI(t, awsCLI("ssm", "get-default-patch-baseline", "--operating-system", "UBUNTU", "--output", "json"))
	var gdResult struct {
		BaselineId string `json:"BaselineId"`
	}
	parseJSON(t, gdOut, &gdResult)
	require.Equal(t, baselineID, gdResult.BaselineId)
}

func TestSSMCLI_ServiceSetting(t *testing.T) {
	settingID := "/ssm/managed-instance/activation-tier"

	getOut := runCLI(t, awsCLI("ssm", "get-service-setting", "--setting-id", settingID, "--output", "json"))
	var getResult struct {
		ServiceSetting struct {
			SettingValue string `json:"SettingValue"`
			Status       string `json:"Status"`
		} `json:"ServiceSetting"`
	}
	parseJSON(t, getOut, &getResult)
	require.Equal(t, "Default", getResult.ServiceSetting.Status)

	runCLI(t, awsCLI("ssm", "update-service-setting",
		"--setting-id", settingID,
		"--setting-value", "advanced",
		"--output", "json"))
	t.Cleanup(func() {
		_ = awsCLI("ssm", "reset-service-setting", "--setting-id", settingID).Run()
	})

	get2Out := runCLI(t, awsCLI("ssm", "get-service-setting", "--setting-id", settingID, "--output", "json"))
	var get2Result struct {
		ServiceSetting struct {
			SettingValue string `json:"SettingValue"`
			Status       string `json:"Status"`
		} `json:"ServiceSetting"`
	}
	parseJSON(t, get2Out, &get2Result)
	require.Equal(t, "advanced", get2Result.ServiceSetting.SettingValue)
	require.Equal(t, "Customized", get2Result.ServiceSetting.Status)

	resetOut := runCLI(t, awsCLI("ssm", "reset-service-setting", "--setting-id", settingID, "--output", "json"))
	var resetResult struct {
		ServiceSetting struct {
			Status string `json:"Status"`
		} `json:"ServiceSetting"`
	}
	parseJSON(t, resetOut, &resetResult)
	assert.Equal(t, "Default", resetResult.ServiceSetting.Status)
}

func TestSSMCLI_ResourceDataSync(t *testing.T) {
	syncName := "cli-rds-" + ssmStamp()

	runCLI(t, awsCLI("ssm", "create-resource-data-sync",
		"--sync-name", syncName,
		"--s3-destination", "BucketName=my-bucket,SyncFormat=JsonSerDe,Region=us-east-1",
		"--output", "json"))
	t.Cleanup(func() {
		_ = awsCLI("ssm", "delete-resource-data-sync", "--sync-name", syncName).Run()
	})

	listOut := runCLI(t, awsCLI("ssm", "list-resource-data-sync", "--output", "json"))
	var listResult struct {
		ResourceDataSyncItems []struct {
			SyncName      string `json:"SyncName"`
			S3Destination struct {
				BucketName string `json:"BucketName"`
			} `json:"S3Destination"`
		} `json:"ResourceDataSyncItems"`
	}
	parseJSON(t, listOut, &listResult)
	found := false
	for _, it := range listResult.ResourceDataSyncItems {
		if it.SyncName == syncName {
			found = true
			require.Equal(t, "my-bucket", it.S3Destination.BucketName)
		}
	}
	require.True(t, found)

	runCLI(t, awsCLI("ssm", "delete-resource-data-sync", "--sync-name", syncName, "--output", "json"))
}
