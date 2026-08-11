package aws_cli_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cbCLICreateProject creates a NO_SOURCE project with a one-command buildspec so
// build/batch/sandbox round-trips have a real project to run against.
func cbCLICreateProject(t *testing.T, name, buildspec string) {
	t.Helper()
	runCLI(t, awsCLI("codebuild", "create-project",
		"--name", name,
		"--source", `{"type":"NO_SOURCE","buildspec":"`+buildspec+`"}`,
		"--artifacts", `{"type":"NO_ARTIFACTS"}`,
		"--environment", `{"type":"LINUX_CONTAINER","image":"public.ecr.aws/docker/library/alpine:3.21","computeType":"BUILD_GENERAL1_SMALL"}`,
		"--service-role", "arn:aws:iam::123456789012:role/cb-role",
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("codebuild", "delete-project", "--name", name))
	})
}

// TestCodeBuildCLI_BuildBatches covers start-build-batch, batch-get-build-batches,
// list-build-batches, list-build-batches-for-project, stop-build-batch,
// retry-build-batch, delete-build-batch, and batch-delete-builds.
func TestCodeBuildCLI_BuildBatches(t *testing.T) {
	proj := "cb-cli-batch-proj"
	cbCLICreateProject(t, proj, "version: 0.2\\nphases:\\n  build:\\n    commands:\\n      - printf ok\\n")

	out := runCLI(t, awsCLI("codebuild", "start-build-batch", "--project-name", proj))
	var start struct {
		BuildBatch struct {
			ID          string `json:"id"`
			ProjectName string `json:"projectName"`
		} `json:"buildBatch"`
	}
	parseJSON(t, out, &start)
	require.NotEmpty(t, start.BuildBatch.ID)
	assert.Equal(t, proj, start.BuildBatch.ProjectName)
	batchID := start.BuildBatch.ID

	require.Eventually(t, func() bool {
		o := runCLI(t, awsCLI("codebuild", "batch-get-build-batches", "--ids", batchID))
		var bg struct {
			BuildBatches []struct {
				BuildBatchStatus string `json:"buildBatchStatus"`
			} `json:"buildBatches"`
		}
		parseJSON(t, o, &bg)
		return len(bg.BuildBatches) == 1 && bg.BuildBatches[0].BuildBatchStatus == "SUCCEEDED"
	}, 10*time.Second, 100*time.Millisecond)

	out = runCLI(t, awsCLI("codebuild", "list-build-batches"))
	var lb struct {
		Ids []string `json:"ids"`
	}
	parseJSON(t, out, &lb)
	require.Contains(t, lb.Ids, batchID)

	out = runCLI(t, awsCLI("codebuild", "list-build-batches-for-project", "--project-name", proj))
	var lbp struct {
		Ids []string `json:"ids"`
	}
	parseJSON(t, out, &lbp)
	require.Contains(t, lbp.Ids, batchID)

	out = runCLI(t, awsCLI("codebuild", "retry-build-batch", "--id", batchID))
	var retry struct {
		BuildBatch struct {
			ID string `json:"id"`
		} `json:"buildBatch"`
	}
	parseJSON(t, out, &retry)
	require.NotEmpty(t, retry.BuildBatch.ID)
	assert.NotEqual(t, batchID, retry.BuildBatch.ID)

	runCLI(t, awsCLI("codebuild", "stop-build-batch", "--id", batchID))
	runCLI(t, awsCLI("codebuild", "delete-build-batch", "--id", batchID))
	runCLI(t, awsCLI("codebuild", "delete-build-batch", "--id", retry.BuildBatch.ID))

	// batch-delete-builds round-trip on a regular build.
	out = runCLI(t, awsCLI("codebuild", "start-build", "--project-name", proj))
	var sb struct {
		Build struct {
			ID string `json:"id"`
		} `json:"build"`
	}
	parseJSON(t, out, &sb)
	out = runCLI(t, awsCLI("codebuild", "batch-delete-builds", "--ids", sb.Build.ID))
	var del struct {
		BuildsDeleted []string `json:"buildsDeleted"`
	}
	parseJSON(t, out, &del)
	require.Contains(t, del.BuildsDeleted, sb.Build.ID)
}

// TestCodeBuildCLI_Fleets covers create-fleet, update-fleet, batch-get-fleets,
// list-fleets, delete-fleet.
func TestCodeBuildCLI_Fleets(t *testing.T) {
	name := "cb-cli-fleet"
	out := runCLI(t, awsCLI("codebuild", "create-fleet",
		"--name", name,
		"--base-capacity", "1",
		"--compute-type", "BUILD_GENERAL1_SMALL",
		"--environment-type", "LINUX_CONTAINER",
	))
	var create struct {
		Fleet struct {
			Arn  string `json:"arn"`
			Name string `json:"name"`
		} `json:"fleet"`
	}
	parseJSON(t, out, &create)
	require.NotEmpty(t, create.Fleet.Arn)
	assert.Equal(t, name, create.Fleet.Name)
	arn := create.Fleet.Arn
	t.Cleanup(func() {
		runCLI(t, awsCLI("codebuild", "delete-fleet", "--arn", arn))
	})

	out = runCLI(t, awsCLI("codebuild", "update-fleet", "--arn", arn, "--base-capacity", "3"))
	var upd struct {
		Fleet struct {
			BaseCapacity int `json:"baseCapacity"`
		} `json:"fleet"`
	}
	parseJSON(t, out, &upd)
	assert.Equal(t, 3, upd.Fleet.BaseCapacity)

	out = runCLI(t, awsCLI("codebuild", "batch-get-fleets", "--names", arn))
	var bg struct {
		Fleets []struct {
			Name string `json:"name"`
		} `json:"fleets"`
		FleetsNotFound []string `json:"fleetsNotFound"`
	}
	parseJSON(t, out, &bg)
	require.Len(t, bg.Fleets, 1)
	assert.Equal(t, name, bg.Fleets[0].Name)
	assert.Empty(t, bg.FleetsNotFound)

	out = runCLI(t, awsCLI("codebuild", "list-fleets"))
	var lf struct {
		Fleets []string `json:"fleets"`
	}
	parseJSON(t, out, &lf)
	require.Contains(t, lf.Fleets, arn)

	runCLI(t, awsCLI("codebuild", "delete-fleet", "--arn", arn))
}

// TestCodeBuildCLI_SandboxesAndCommands covers start-sandbox, batch-get-sandboxes,
// list-sandboxes, list-sandboxes-for-project, start-sandbox-connection,
// start-command-execution, batch-get-command-executions,
// list-command-executions-for-sandbox, stop-sandbox.
func TestCodeBuildCLI_SandboxesAndCommands(t *testing.T) {
	proj := "cb-cli-sandbox-proj"
	cbCLICreateProject(t, proj, "version: 0.2\\nphases:\\n  build:\\n    commands:\\n      - printf ok\\n")

	out := runCLI(t, awsCLI("codebuild", "start-sandbox", "--project-name", proj))
	var start struct {
		Sandbox struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"sandbox"`
	}
	parseJSON(t, out, &start)
	require.NotEmpty(t, start.Sandbox.ID)
	assert.Equal(t, "RUNNING", start.Sandbox.Status)
	sbID := start.Sandbox.ID

	out = runCLI(t, awsCLI("codebuild", "batch-get-sandboxes", "--ids", sbID))
	var bg struct {
		Sandboxes []struct {
			ID string `json:"id"`
		} `json:"sandboxes"`
		SandboxesNotFound []string `json:"sandboxesNotFound"`
	}
	parseJSON(t, out, &bg)
	require.Len(t, bg.Sandboxes, 1)
	assert.Empty(t, bg.SandboxesNotFound)

	out = runCLI(t, awsCLI("codebuild", "list-sandboxes"))
	var ls struct {
		Ids []string `json:"ids"`
	}
	parseJSON(t, out, &ls)
	require.Contains(t, ls.Ids, sbID)

	out = runCLI(t, awsCLI("codebuild", "list-sandboxes-for-project", "--project-name", proj))
	var lsp struct {
		Ids []string `json:"ids"`
	}
	parseJSON(t, out, &lsp)
	require.Contains(t, lsp.Ids, sbID)

	out = runCLI(t, awsCLI("codebuild", "start-sandbox-connection", "--sandbox-id", sbID))
	var conn struct {
		SsmSession struct {
			SessionID  string `json:"sessionId"`
			StreamURL  string `json:"streamUrl"`
			TokenValue string `json:"tokenValue"`
		} `json:"ssmSession"`
	}
	parseJSON(t, out, &conn)
	require.NotEmpty(t, conn.SsmSession.SessionID)
	require.NotEmpty(t, conn.SsmSession.StreamURL)
	require.NotEmpty(t, conn.SsmSession.TokenValue)

	out = runCLI(t, awsCLI("codebuild", "start-command-execution",
		"--sandbox-id", sbID, "--command", "printf hello"))
	var cmd struct {
		CommandExecution struct {
			ID string `json:"id"`
		} `json:"commandExecution"`
	}
	parseJSON(t, out, &cmd)
	require.NotEmpty(t, cmd.CommandExecution.ID)
	cmdID := cmd.CommandExecution.ID

	require.Eventually(t, func() bool {
		o := runCLI(t, awsCLI("codebuild", "batch-get-command-executions",
			"--sandbox-id", sbID, "--command-execution-ids", cmdID))
		var bgc struct {
			CommandExecutions []struct {
				Status string `json:"status"`
			} `json:"commandExecutions"`
		}
		parseJSON(t, o, &bgc)
		return len(bgc.CommandExecutions) == 1 && bgc.CommandExecutions[0].Status == "SUCCEEDED"
	}, 10*time.Second, 100*time.Millisecond)

	out = runCLI(t, awsCLI("codebuild", "list-command-executions-for-sandbox", "--sandbox-id", sbID))
	var lc struct {
		CommandExecutions []struct {
			ID string `json:"id"`
		} `json:"commandExecutions"`
	}
	parseJSON(t, out, &lc)
	require.Len(t, lc.CommandExecutions, 1)
	assert.Equal(t, cmdID, lc.CommandExecutions[0].ID)

	out = runCLI(t, awsCLI("codebuild", "stop-sandbox", "--id", sbID))
	var stop struct {
		Sandbox struct {
			Status string `json:"status"`
		} `json:"sandbox"`
	}
	parseJSON(t, out, &stop)
	assert.Equal(t, "STOPPED", stop.Sandbox.Status)
}

// TestCodeBuildCLI_Webhooks covers create-webhook, update-webhook, delete-webhook.
func TestCodeBuildCLI_Webhooks(t *testing.T) {
	proj := "cb-cli-webhook-proj"
	cbCLICreateProject(t, proj, "version: 0.2\\nphases:\\n  build:\\n    commands:\\n      - printf ok\\n")

	out := runCLI(t, awsCLI("codebuild", "create-webhook", "--project-name", proj, "--branch-filter", "main"))
	var create struct {
		Webhook struct {
			PayloadURL   string `json:"payloadUrl"`
			Secret       string `json:"secret"`
			BranchFilter string `json:"branchFilter"`
		} `json:"webhook"`
	}
	parseJSON(t, out, &create)
	require.NotEmpty(t, create.Webhook.PayloadURL)
	require.NotEmpty(t, create.Webhook.Secret)
	assert.Equal(t, "main", create.Webhook.BranchFilter)

	out = runCLI(t, awsCLI("codebuild", "update-webhook",
		"--project-name", proj, "--branch-filter", "develop", "--rotate-secret"))
	var upd struct {
		Webhook struct {
			BranchFilter string `json:"branchFilter"`
			Secret       string `json:"secret"`
		} `json:"webhook"`
	}
	parseJSON(t, out, &upd)
	assert.Equal(t, "develop", upd.Webhook.BranchFilter)
	assert.NotEqual(t, create.Webhook.Secret, upd.Webhook.Secret)

	runCLI(t, awsCLI("codebuild", "delete-webhook", "--project-name", proj))
}

// TestCodeBuildCLI_ReportInsights covers describe-test-cases, describe-code-coverages,
// get-report-group-trend, and delete-report against a real report.
func TestCodeBuildCLI_ReportInsights(t *testing.T) {
	rgName := "cb-cli-insights-rg"
	out := runCLI(t, awsCLI("codebuild", "create-report-group",
		"--name", rgName, "--type", "TEST", "--export-config", `{"exportConfigType":"NO_EXPORT"}`))
	var rg struct {
		ReportGroup struct {
			Arn string `json:"arn"`
		} `json:"reportGroup"`
	}
	parseJSON(t, out, &rg)
	rgArn := rg.ReportGroup.Arn
	t.Cleanup(func() {
		runCLI(t, awsCLI("codebuild", "delete-report-group", "--arn", rgArn, "--delete-reports"))
	})

	proj := "cb-cli-insights-proj"
	cbCLICreateProject(t, proj, "version: 0.2\\nphases:\\n  build:\\n    commands:\\n      - printf ok\\nreports:\\n  "+rgName+":\\n    files:\\n      - '**/*'\\n")

	out = runCLI(t, awsCLI("codebuild", "start-build", "--project-name", proj))
	var start struct {
		Build struct {
			ID string `json:"id"`
		} `json:"build"`
	}
	parseJSON(t, out, &start)

	var reportArn string
	require.Eventually(t, func() bool {
		o := runCLI(t, awsCLI("codebuild", "batch-get-builds", "--ids", start.Build.ID))
		var gb struct {
			Builds []struct {
				BuildStatus string   `json:"buildStatus"`
				ReportArns  []string `json:"reportArns"`
			} `json:"builds"`
		}
		parseJSON(t, o, &gb)
		if len(gb.Builds) != 1 || gb.Builds[0].BuildStatus != "SUCCEEDED" || len(gb.Builds[0].ReportArns) == 0 {
			return false
		}
		reportArn = gb.Builds[0].ReportArns[0]
		return true
	}, 10*time.Second, 100*time.Millisecond)
	require.NotEmpty(t, reportArn)

	out = runCLI(t, awsCLI("codebuild", "describe-test-cases", "--report-arn", reportArn))
	var tc struct {
		TestCases []struct {
			ReportArn string `json:"reportArn"`
		} `json:"testCases"`
	}
	parseJSON(t, out, &tc)
	require.NotEmpty(t, tc.TestCases)
	assert.Equal(t, reportArn, tc.TestCases[0].ReportArn)

	out = runCLI(t, awsCLI("codebuild", "describe-code-coverages", "--report-arn", reportArn))
	var cov struct {
		CodeCoverages []struct {
			ReportARN string `json:"reportARN"`
		} `json:"codeCoverages"`
	}
	parseJSON(t, out, &cov)
	require.NotEmpty(t, cov.CodeCoverages)
	assert.Equal(t, reportArn, cov.CodeCoverages[0].ReportARN)

	out = runCLI(t, awsCLI("codebuild", "get-report-group-trend",
		"--report-group-arn", rgArn, "--trend-field", "PASS_RATE"))
	var trend struct {
		RawData []struct {
			ReportArn string `json:"reportArn"`
		} `json:"rawData"`
		Stats struct {
			Average string `json:"average"`
		} `json:"stats"`
	}
	parseJSON(t, out, &trend)
	require.NotEmpty(t, trend.RawData)

	runCLI(t, awsCLI("codebuild", "delete-report", "--arn", reportArn))
}

// TestCodeBuildCLI_ResourcePolicyAndControls covers put-resource-policy,
// get-resource-policy, delete-resource-policy, list-shared-projects,
// list-shared-report-groups, update-project-visibility, invalidate-project-cache,
// and list-curated-environment-images.
func TestCodeBuildCLI_ResourcePolicyAndControls(t *testing.T) {
	proj := "cb-cli-policy-proj"
	cbCLICreateProject(t, proj, "version: 0.2\\nphases:\\n  build:\\n    commands:\\n      - printf ok\\n")
	projARN := "arn:aws:codebuild:us-east-1:123456789012:project/" + proj
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":["codebuild:BatchGetProjects"],"Resource":"` + projARN + `"}]}`

	out := runCLI(t, awsCLI("codebuild", "put-resource-policy",
		"--resource-arn", projARN, "--policy", policy))
	var put struct {
		ResourceArn string `json:"resourceArn"`
	}
	parseJSON(t, out, &put)
	assert.Equal(t, projARN, put.ResourceArn)

	out = runCLI(t, awsCLI("codebuild", "get-resource-policy", "--resource-arn", projARN))
	var get struct {
		Policy string `json:"policy"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, policy, get.Policy)

	out = runCLI(t, awsCLI("codebuild", "list-shared-projects"))
	var lsp struct {
		Projects []string `json:"projects"`
	}
	parseJSON(t, out, &lsp)
	require.Contains(t, lsp.Projects, projARN)

	rgArn := "arn:aws:codebuild:us-east-1:123456789012:report-group/" + proj + "-rg"
	runCLI(t, awsCLI("codebuild", "put-resource-policy", "--resource-arn", rgArn, "--policy", policy))
	out = runCLI(t, awsCLI("codebuild", "list-shared-report-groups"))
	var lsr struct {
		ReportGroups []string `json:"reportGroups"`
	}
	parseJSON(t, out, &lsr)
	require.Contains(t, lsr.ReportGroups, rgArn)
	runCLI(t, awsCLI("codebuild", "delete-resource-policy", "--resource-arn", rgArn))

	runCLI(t, awsCLI("codebuild", "delete-resource-policy", "--resource-arn", projARN))

	// update-project-visibility / invalidate-project-cache.
	out = runCLI(t, awsCLI("codebuild", "update-project-visibility",
		"--project-arn", projARN, "--project-visibility", "PRIVATE"))
	var vis struct {
		ProjectVisibility string `json:"projectVisibility"`
		ProjectArn        string `json:"projectArn"`
	}
	parseJSON(t, out, &vis)
	assert.Equal(t, "PRIVATE", vis.ProjectVisibility)
	assert.Equal(t, projARN, vis.ProjectArn)

	runCLI(t, awsCLI("codebuild", "invalidate-project-cache", "--project-name", proj))

	out = runCLI(t, awsCLI("codebuild", "list-curated-environment-images"))
	var imgs struct {
		Platforms []struct {
			Platform  string `json:"platform"`
			Languages []struct {
				Images []struct {
					Name string `json:"name"`
				} `json:"images"`
			} `json:"languages"`
		} `json:"platforms"`
	}
	parseJSON(t, out, &imgs)
	require.NotEmpty(t, imgs.Platforms)
	found := false
	for _, p := range imgs.Platforms {
		for _, lang := range p.Languages {
			for _, img := range lang.Images {
				if img.Name == "aws/codebuild/standard:7.0" {
					found = true
				}
			}
		}
	}
	assert.True(t, found, "curated images must include the Ubuntu standard 7.0 image")
}
