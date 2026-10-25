package aws_cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cbCLICreateProject creates a NO_SOURCE project with a one-command buildspec so
// build/batch/sandbox round-trips have a real project to run against.
// cbCLIBuildCompletionBudget bounds every wait for an AWS CodeBuild build or
// sandbox command to reach a terminal state. Both are real container work — the
// build environment image is created and the commands run inside it — so the
// budget is really a budget for the container engine alongside whatever else is
// running on the host. Real AWS CodeBuild builds take minutes.
const cbCLIBuildCompletionBudget = 4 * time.Minute

func cbCLICreateProject(t *testing.T, name, buildspec string) {
	t.Helper()
	runCLI(t, awsCLI("codebuild", "create-project",
		"--name", name,
		"--source", `{"type":"NO_SOURCE","buildspec":"`+buildspec+`"}`,
		"--artifacts", `{"type":"NO_ARTIFACTS"}`,
		"--environment", `{"type":"LINUX_CONTAINER","image":"mirror.gcr.io/library/alpine:3.21","computeType":"BUILD_GENERAL1_SMALL"}`,
		"--service-role", cliServiceRole(t, "codebuild.amazonaws.com"),
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("codebuild", "delete-project", "--name", name))
	})
}

// TestCodeBuildCLI_BuildBatches covers start-build-batch, batch-get-build-batches,
// list-build-batches, list-build-batches-for-project, stop-build-batch,
// retry-build-batch, delete-build-batch, and batch-delete-builds: a
// build-list batch starts a build per task and reports each in buildGroups.
func TestCodeBuildCLI_BuildBatches(t *testing.T) {
	t.Parallel()
	proj := uniquename.New("cb-cli-batch")
	batchRole := cliServiceRole(t, "codebuild.amazonaws.com")
	batchRoleName := batchRole[strings.LastIndex(batchRole, "/")+1:]
	runCLI(t, awsCLI("iam", "put-role-policy", "--role-name", batchRoleName, "--policy-name", "batch", "--policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["codebuild:StartBuild","codebuild:StopBuild","codebuild:RetryBuild"],"Resource":"*"}]}`))
	t.Cleanup(func() {
		_ = awsCLI("iam", "delete-role-policy", "--role-name", batchRoleName, "--policy-name", "batch").Run()
	})
	runCLI(t, awsCLI("codebuild", "create-project",
		"--name", proj,
		"--source", `{"type":"NO_SOURCE","buildspec":"version: 0.2\nbatch:\n  build-list:\n    - identifier: linux\n    - identifier: docs\nphases:\n  build:\n    commands:\n      - printf ok\n"}`,
		"--artifacts", `{"type":"NO_ARTIFACTS"}`,
		"--environment", `{"type":"LINUX_CONTAINER","image":"mirror.gcr.io/library/alpine:3.21","computeType":"BUILD_GENERAL1_SMALL"}`,
		"--service-role", cliServiceRole(t, "codebuild.amazonaws.com"),
		"--build-batch-config", `{"serviceRole":"`+batchRole+`","restrictions":{"maximumBuildsAllowed":2,"computeTypesAllowed":["BUILD_GENERAL1_SMALL"]}}`,
	))
	t.Cleanup(func() { runCLI(t, awsCLI("codebuild", "delete-project", "--name", proj)) })

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

	var bg struct {
		BuildBatches []struct {
			BuildBatchStatus string `json:"buildBatchStatus"`
			BuildGroups      []struct {
				Identifier          string `json:"identifier"`
				CurrentBuildSummary struct {
					Arn         string `json:"arn"`
					BuildStatus string `json:"buildStatus"`
				} `json:"currentBuildSummary"`
			} `json:"buildGroups"`
		} `json:"buildBatches"`
	}
	require.Eventually(t, func() bool {
		parseJSON(t, runCLI(t, awsCLI("codebuild", "batch-get-build-batches", "--ids", batchID)), &bg)
		return len(bg.BuildBatches) == 1 && bg.BuildBatches[0].BuildBatchStatus != "IN_PROGRESS"
	}, cbCLIBuildCompletionBudget, 100*time.Millisecond)
	require.Equal(t, "SUCCEEDED", bg.BuildBatches[0].BuildBatchStatus)
	var identifiers []string
	for _, group := range bg.BuildBatches[0].BuildGroups {
		identifiers = append(identifiers, group.Identifier)
		assert.Equal(t, "SUCCEEDED", group.CurrentBuildSummary.BuildStatus)
		assert.Contains(t, group.CurrentBuildSummary.Arn, ":build/"+proj+":")
	}
	assert.ElementsMatch(t, []string{"linux", "docs"}, identifiers)

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

	// A succeeded batch build cannot be retried.
	assert.Contains(t, runCLIExpectError(t, awsCLI("codebuild", "retry-build-batch", "--id", batchID)), "InvalidInputException")

	runCLI(t, awsCLI("codebuild", "stop-build-batch", "--id", batchID))
	out = runCLI(t, awsCLI("codebuild", "delete-build-batch", "--id", batchID))
	var deleted struct {
		BuildsDeleted []string `json:"buildsDeleted"`
	}
	parseJSON(t, out, &deleted)
	assert.Len(t, deleted.BuildsDeleted, 2)

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

// start-build --image-override runs the build in the override's image: the
// project's busybox image has no /etc/os-release and Alpine's has.
func TestCodeBuildCLI_StartBuildImageOverride(t *testing.T) {
	t.Parallel()
	proj := uniquename.New("cb-cli-image")
	runCLI(t, awsCLI("codebuild", "create-project",
		"--name", proj,
		"--source", `{"type":"NO_SOURCE","buildspec":"version: 0.2\nphases:\n  build:\n    commands:\n      - cat /etc/os-release\n"}`,
		"--artifacts", `{"type":"NO_ARTIFACTS"}`,
		"--environment", `{"type":"LINUX_CONTAINER","image":"mirror.gcr.io/library/busybox:latest","computeType":"BUILD_GENERAL1_SMALL"}`,
		"--service-role", cliServiceRole(t, "codebuild.amazonaws.com"),
	))
	t.Cleanup(func() { runCLI(t, awsCLI("codebuild", "delete-project", "--name", proj)) })

	out := runCLI(t, awsCLI("codebuild", "start-build", "--project-name", proj,
		"--image-override", "mirror.gcr.io/library/alpine:3.21"))
	var start struct {
		Build struct {
			ID          string `json:"id"`
			Environment struct {
				Image string `json:"image"`
			} `json:"environment"`
		} `json:"build"`
	}
	parseJSON(t, out, &start)
	assert.Equal(t, "mirror.gcr.io/library/alpine:3.21", start.Build.Environment.Image)

	var builds struct {
		Builds []struct {
			BuildStatus string `json:"buildStatus"`
			Logs        struct {
				GroupName  string `json:"groupName"`
				StreamName string `json:"streamName"`
			} `json:"logs"`
		} `json:"builds"`
	}
	require.Eventually(t, func() bool {
		parseJSON(t, runCLI(t, awsCLI("codebuild", "batch-get-builds", "--ids", start.Build.ID)), &builds)
		return builds.Builds[0].BuildStatus != "IN_PROGRESS"
	}, cbCLIBuildCompletionBudget, 100*time.Millisecond)
	require.Equal(t, "SUCCEEDED", builds.Builds[0].BuildStatus)
	logs := runCLI(t, awsCLI("logs", "filter-log-events",
		"--log-group-name", builds.Builds[0].Logs.GroupName,
		"--log-stream-names", builds.Builds[0].Logs.StreamName))
	assert.Contains(t, logs, "ID=alpine")
}

// TestCodeBuildCLI_Fleets covers create-fleet, update-fleet, batch-get-fleets,
// list-fleets, delete-fleet.
func TestCodeBuildCLI_Fleets(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	}, cbCLIBuildCompletionBudget, 100*time.Millisecond)

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
	t.Parallel()
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
	t.Parallel()
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

	// The build writes the JUnit XML its reports entry declares, and the entry
	// names the group by ARN so the report lands in the group created above; a
	// bare name would create "<project-name>-<report-group-name>" instead. The
	// content rides base64 so neither the buildspec YAML nor the shell touches
	// it. The two cases below are what the file holds — one passing, one failing.
	proj := "cb-cli-insights-proj"
	cbCLICreateProject(t, proj, "version: 0.2\\nphases:\\n  build:\\n    commands:\\n"+
		"      - mkdir -p test-results\\n"+
		"      - printf %s PD94bWwgdmVyc2lvbj0iMS4wIiBlbmNvZGluZz0iVVRGLTgiPz48dGVzdHN1aXRlcz48dGVzdHN1aXRlIG5hbWU9ImNoZWNrb3V0Ij48dGVzdGNhc2UgY2xhc3NuYW1lPSJjYXJ0LkNoZWNrb3V0VGVzdCIgbmFtZT0idG90YWxzVGhlQmFza2V0IiB0aW1lPSIwLjEyNSIvPjx0ZXN0Y2FzZSBjbGFzc25hbWU9ImNhcnQuQ2hlY2tvdXRUZXN0IiBuYW1lPSJyZWplY3RzQW5FbXB0eUJhc2tldCIgdGltZT0iMC41Ij48ZmFpbHVyZSBtZXNzYWdlPSJleHBlY3RlZCA0MDAsIGdvdCAyMDAiPmJvb208L2ZhaWx1cmU+PC90ZXN0Y2FzZT48L3Rlc3RzdWl0ZT48L3Rlc3RzdWl0ZXM+ | base64 -d > test-results/junit.xml\\n"+
		"reports:\\n  "+rgArn+":\\n    files:\\n      - junit.xml\\n"+
		"    base-directory: test-results\\n    file-format: JUNITXML\\n")

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
	}, cbCLIBuildCompletionBudget, 100*time.Millisecond)
	require.NotEmpty(t, reportArn)

	// The two test cases are the ones the JUnit XML held, with the statuses,
	// suite, prefix and duration it recorded.
	out = runCLI(t, awsCLI("codebuild", "describe-test-cases", "--report-arn", reportArn))
	var tc struct {
		TestCases []struct {
			ReportArn             string `json:"reportArn"`
			Name                  string `json:"name"`
			Prefix                string `json:"prefix"`
			Status                string `json:"status"`
			Message               string `json:"message"`
			TestSuiteName         string `json:"testSuiteName"`
			DurationInNanoSeconds int64  `json:"durationInNanoSeconds"`
			TestRawDataPath       string `json:"testRawDataPath"`
		} `json:"testCases"`
	}
	parseJSON(t, out, &tc)
	require.Len(t, tc.TestCases, 2)
	byName := map[string]string{}
	for _, c := range tc.TestCases {
		byName[c.Name] = c.Status
		assert.Equal(t, reportArn, c.ReportArn)
		assert.Equal(t, "cart.CheckoutTest", c.Prefix)
		assert.Equal(t, "checkout", c.TestSuiteName)
		assert.Equal(t, "junit.xml", c.TestRawDataPath)
	}
	assert.Equal(t, "SUCCEEDED", byName["totalsTheBasket"])
	assert.Equal(t, "FAILED", byName["rejectsAnEmptyBasket"])

	// The build wrote no coverage file, and this is a TEST report group, so the
	// coverage answer is empty rather than an invented hundred-percent row.
	out = runCLI(t, awsCLI("codebuild", "describe-code-coverages", "--report-arn", reportArn))
	var cov struct {
		CodeCoverages []struct {
			ReportARN string `json:"reportARN"`
		} `json:"codeCoverages"`
	}
	parseJSON(t, out, &cov)
	assert.Empty(t, cov.CodeCoverages)

	out = runCLI(t, awsCLI("codebuild", "get-report-group-trend",
		"--report-group-arn", rgArn, "--trend-field", "PASS_RATE"))
	var trend struct {
		RawData []struct {
			ReportArn string `json:"reportArn"`
			Data      string `json:"data"`
		} `json:"rawData"`
		Stats struct {
			Average string `json:"average"`
		} `json:"stats"`
	}
	parseJSON(t, out, &trend)
	require.Len(t, trend.RawData, 1)
	// One of the report's two cases passed.
	assert.Equal(t, "50", trend.RawData[0].Data)

	runCLI(t, awsCLI("codebuild", "delete-report", "--arn", reportArn))
}

// TestCodeBuildCLI_ResourcePolicyAndControls covers put-resource-policy,
// get-resource-policy, delete-resource-policy, list-shared-projects,
// list-shared-report-groups, update-project-visibility, invalidate-project-cache,
// and list-curated-environment-images.
func TestCodeBuildCLI_ResourcePolicyAndControls(t *testing.T) {
	t.Parallel()
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
