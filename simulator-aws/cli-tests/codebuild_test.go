package aws_cli_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodeBuild_ProjectCRUD_CLI(t *testing.T) {
	out := runCLI(t, awsCLI("codebuild", "create-project",
		"--name", "cb-cli-project",
		"--source", `{"type":"NO_SOURCE"}`,
		"--artifacts", `{"type":"NO_ARTIFACTS"}`,
		"--environment", `{"type":"LINUX_CONTAINER","image":"public.ecr.aws/docker/library/alpine:3.21","computeType":"BUILD_GENERAL1_SMALL"}`,
		"--service-role", "arn:aws:iam::123456789012:role/cb-role",
	))
	var created struct {
		Project struct {
			Name string `json:"name"`
			Arn  string `json:"arn"`
		} `json:"project"`
	}
	parseJSON(t, out, &created)
	assert.Equal(t, "cb-cli-project", created.Project.Name)
	require.NotEmpty(t, created.Project.Arn)
	t.Cleanup(func() {
		runCLI(t, awsCLI("codebuild", "delete-project", "--name", "cb-cli-project"))
	})

	out = runCLI(t, awsCLI("codebuild", "batch-get-projects", "--names", "cb-cli-project"))
	var getProjects struct {
		Projects []struct {
			Name string `json:"name"`
		} `json:"projects"`
		ProjectsNotFound []string `json:"projectsNotFound"`
	}
	parseJSON(t, out, &getProjects)
	require.Len(t, getProjects.Projects, 1)
	assert.Equal(t, "cb-cli-project", getProjects.Projects[0].Name)
	assert.Empty(t, getProjects.ProjectsNotFound)

	out = runCLI(t, awsCLI("codebuild", "list-projects"))
	var list struct {
		Projects []string `json:"projects"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, p := range list.Projects {
		if p == "cb-cli-project" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestCodeBuild_Build_CLI(t *testing.T) {
	runCLI(t, awsCLI("codebuild", "create-project",
		"--name", "cb-cli-build-proj",
		"--source", `{"type":"NO_SOURCE","buildspec":"version: 0.2\nphases:\n  build:\n    commands:\n      - printf codebuild-cli-ready\n"}`,
		"--artifacts", `{"type":"NO_ARTIFACTS"}`,
		"--environment", `{"type":"LINUX_CONTAINER","image":"public.ecr.aws/docker/library/alpine:3.21","computeType":"BUILD_GENERAL1_SMALL"}`,
		"--service-role", "arn:aws:iam::123456789012:role/cb-role",
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("codebuild", "delete-project", "--name", "cb-cli-build-proj"))
	})

	out := runCLI(t, awsCLI("codebuild", "start-build",
		"--project-name", "cb-cli-build-proj",
	))
	var startResult struct {
		Build struct {
			ID          string `json:"id"`
			BuildStatus string `json:"buildStatus"`
		} `json:"build"`
	}
	parseJSON(t, out, &startResult)
	require.NotEmpty(t, startResult.Build.ID)

	var getBuilds struct {
		Builds []struct {
			ID          string `json:"id"`
			BuildStatus string `json:"buildStatus"`
		} `json:"builds"`
	}
	require.Eventually(t, func() bool {
		out = runCLI(t, awsCLI("codebuild", "batch-get-builds", "--ids", startResult.Build.ID))
		parseJSON(t, out, &getBuilds)
		require.Len(t, getBuilds.Builds, 1)
		return getBuilds.Builds[0].BuildStatus == "SUCCEEDED"
	}, 10*time.Second, 100*time.Millisecond)
	assert.Equal(t, startResult.Build.ID, getBuilds.Builds[0].ID)
	assert.Equal(t, "SUCCEEDED", getBuilds.Builds[0].BuildStatus)

	out = runCLI(t, awsCLI("codebuild", "list-builds-for-project",
		"--project-name", "cb-cli-build-proj"))
	var buildList struct {
		IDs []string `json:"ids"`
	}
	parseJSON(t, out, &buildList)
	require.Contains(t, buildList.IDs, startResult.Build.ID)

	out = runCLI(t, awsCLI("codebuild", "list-builds"))
	var allBuilds struct {
		IDs []string `json:"ids"`
	}
	parseJSON(t, out, &allBuilds)
	require.Contains(t, allBuilds.IDs, startResult.Build.ID)
}

// TestCodeBuildCLI_StopAndRetryBuild covers stop-build and retry-build.
func TestCodeBuildCLI_StopAndRetryBuild(t *testing.T) {
	runCLI(t, awsCLI("codebuild", "create-project",
		"--name", "cb-cli-stop-proj",
		"--source", `{"type":"NO_SOURCE","buildspec":"version: 0.2\nphases:\n  build:\n    commands:\n      - sleep 5\n"}`,
		"--artifacts", `{"type":"NO_ARTIFACTS"}`,
		"--environment", `{"type":"LINUX_CONTAINER","image":"public.ecr.aws/docker/library/alpine:3.21","computeType":"BUILD_GENERAL1_SMALL"}`,
		"--service-role", "arn:aws:iam::123456789012:role/cb-role",
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("codebuild", "delete-project", "--name", "cb-cli-stop-proj"))
	})

	out := runCLI(t, awsCLI("codebuild", "start-build", "--project-name", "cb-cli-stop-proj"))
	var start struct {
		Build struct {
			ID string `json:"id"`
		} `json:"build"`
	}
	parseJSON(t, out, &start)
	require.NotEmpty(t, start.Build.ID)

	out = runCLI(t, awsCLI("codebuild", "stop-build", "--id", start.Build.ID))
	var stopped struct {
		Build struct {
			BuildStatus string `json:"buildStatus"`
		} `json:"build"`
	}
	parseJSON(t, out, &stopped)
	assert.Equal(t, "STOPPED", stopped.Build.BuildStatus)

	out = runCLI(t, awsCLI("codebuild", "retry-build", "--id", start.Build.ID))
	var retried struct {
		Build struct {
			ID          string `json:"id"`
			ProjectName string `json:"projectName"`
		} `json:"build"`
	}
	parseJSON(t, out, &retried)
	require.NotEmpty(t, retried.Build.ID)
	assert.NotEqual(t, start.Build.ID, retried.Build.ID)
	assert.Equal(t, "cb-cli-stop-proj", retried.Build.ProjectName)
}

// TestCodeBuildCLI_ReportGroupsAndReports covers report-group CRUD and the
// reports a build produces when its buildspec references the group.
func TestCodeBuildCLI_ReportGroupsAndReports(t *testing.T) {
	out := runCLI(t, awsCLI("codebuild", "create-report-group",
		"--name", "cb-cli-rg",
		"--type", "TEST",
		"--export-config", `{"exportConfigType":"NO_EXPORT"}`,
	))
	var rg struct {
		ReportGroup struct {
			Arn    string `json:"arn"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"reportGroup"`
	}
	parseJSON(t, out, &rg)
	require.NotEmpty(t, rg.ReportGroup.Arn)
	assert.Equal(t, "ACTIVE", rg.ReportGroup.Status)
	rgArn := rg.ReportGroup.Arn
	t.Cleanup(func() {
		runCLI(t, awsCLI("codebuild", "delete-report-group", "--arn", rgArn, "--delete-reports"))
	})

	runCLI(t, awsCLI("codebuild", "update-report-group",
		"--arn", rgArn,
		"--export-config", `{"exportConfigType":"NO_EXPORT"}`,
	))

	out = runCLI(t, awsCLI("codebuild", "batch-get-report-groups", "--report-group-arns", rgArn))
	var bg struct {
		ReportGroups []struct {
			Name string `json:"name"`
		} `json:"reportGroups"`
		ReportGroupsNotFound []string `json:"reportGroupsNotFound"`
	}
	parseJSON(t, out, &bg)
	require.Len(t, bg.ReportGroups, 1)
	assert.Equal(t, "cb-cli-rg", bg.ReportGroups[0].Name)
	assert.Empty(t, bg.ReportGroupsNotFound)

	out = runCLI(t, awsCLI("codebuild", "list-report-groups"))
	var lg struct {
		ReportGroups []string `json:"reportGroups"`
	}
	parseJSON(t, out, &lg)
	require.Contains(t, lg.ReportGroups, rgArn)

	// A build referencing the report group produces a Report.
	runCLI(t, awsCLI("codebuild", "create-project",
		"--name", "cb-cli-report-proj",
		"--source", `{"type":"NO_SOURCE","buildspec":"version: 0.2\nphases:\n  build:\n    commands:\n      - printf ok\nreports:\n  cb-cli-rg:\n    files:\n      - '**/*'\n"}`,
		"--artifacts", `{"type":"NO_ARTIFACTS"}`,
		"--environment", `{"type":"LINUX_CONTAINER","image":"public.ecr.aws/docker/library/alpine:3.21","computeType":"BUILD_GENERAL1_SMALL"}`,
		"--service-role", "arn:aws:iam::123456789012:role/cb-role",
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("codebuild", "delete-project", "--name", "cb-cli-report-proj"))
	})

	out = runCLI(t, awsCLI("codebuild", "start-build", "--project-name", "cb-cli-report-proj"))
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
		require.Len(t, gb.Builds, 1)
		if gb.Builds[0].BuildStatus != "SUCCEEDED" || len(gb.Builds[0].ReportArns) == 0 {
			return false
		}
		reportArn = gb.Builds[0].ReportArns[0]
		return true
	}, 10*time.Second, 100*time.Millisecond)
	require.NotEmpty(t, reportArn)

	out = runCLI(t, awsCLI("codebuild", "list-reports"))
	var lr struct {
		Reports []string `json:"reports"`
	}
	parseJSON(t, out, &lr)
	require.Contains(t, lr.Reports, reportArn)

	out = runCLI(t, awsCLI("codebuild", "list-reports-for-report-group", "--report-group-arn", rgArn))
	var lrg struct {
		Reports []string `json:"reports"`
	}
	parseJSON(t, out, &lrg)
	require.Contains(t, lrg.Reports, reportArn)

	out = runCLI(t, awsCLI("codebuild", "batch-get-reports", "--report-arns", reportArn))
	var br struct {
		Reports []struct {
			ReportGroupArn string `json:"reportGroupArn"`
			Status         string `json:"status"`
		} `json:"reports"`
		ReportsNotFound []string `json:"reportsNotFound"`
	}
	parseJSON(t, out, &br)
	require.Len(t, br.Reports, 1)
	assert.Equal(t, rgArn, br.Reports[0].ReportGroupArn)
	assert.Equal(t, "SUCCEEDED", br.Reports[0].Status)
	assert.Empty(t, br.ReportsNotFound)
}

// TestCodeBuildCLI_SourceCredentials covers import/list/delete source credentials.
func TestCodeBuildCLI_SourceCredentials(t *testing.T) {
	out := runCLI(t, awsCLI("codebuild", "import-source-credentials",
		"--token", "ghp_clitoken",
		"--server-type", "GITHUB",
		"--auth-type", "PERSONAL_ACCESS_TOKEN",
	))
	var imp struct {
		Arn string `json:"arn"`
	}
	parseJSON(t, out, &imp)
	require.NotEmpty(t, imp.Arn)
	t.Cleanup(func() {
		// Tolerant: the body deletes the credential explicitly, so cleanup is a
		// best-effort safety net that may find it already gone.
		runCLIIgnore(awsCLI("codebuild", "delete-source-credentials", "--arn", imp.Arn))
	})

	out = runCLI(t, awsCLI("codebuild", "list-source-credentials"))
	var list struct {
		SourceCredentialsInfos []struct {
			Arn        string `json:"arn"`
			ServerType string `json:"serverType"`
			AuthType   string `json:"authType"`
		} `json:"sourceCredentialsInfos"`
	}
	parseJSON(t, out, &list)
	found := false
	for _, info := range list.SourceCredentialsInfos {
		if info.Arn == imp.Arn {
			found = true
			assert.Equal(t, "GITHUB", info.ServerType)
			assert.Equal(t, "PERSONAL_ACCESS_TOKEN", info.AuthType)
		}
	}
	assert.True(t, found)

	out = runCLI(t, awsCLI("codebuild", "delete-source-credentials", "--arn", imp.Arn))
	var del struct {
		Arn string `json:"arn"`
	}
	parseJSON(t, out, &del)
	assert.Equal(t, imp.Arn, del.Arn)
}
