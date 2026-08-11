package aws_sdk_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/e6qu/sockerless-cloud/testutil/githttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func codebuildClient() *codebuild.Client {
	return codebuild.NewFromConfig(sdkConfig(), func(o *codebuild.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

func TestCodeBuild_ProjectCRUD_SDK(t *testing.T) {
	c := codebuildClient()

	create, err := c.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name: aws.String("cb-sdk-project"),
		Source: &cbtypes.ProjectSource{
			Type: cbtypes.SourceTypeNoSource,
		},
		Artifacts: &cbtypes.ProjectArtifacts{
			Type: cbtypes.ArtifactsTypeNoArtifacts,
		},
		Environment: &cbtypes.ProjectEnvironment{
			Type:        cbtypes.EnvironmentTypeLinuxContainer,
			Image:       aws.String("public.ecr.aws/docker/library/alpine:3.21"),
			ComputeType: cbtypes.ComputeTypeBuildGeneral1Small,
		},
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/cb-role"),
		Tags: []cbtypes.Tag{
			{Key: aws.String("env"), Value: aws.String("sdk")},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, create.Project)
	assert.Equal(t, "cb-sdk-project", aws.ToString(create.Project.Name))
	t.Cleanup(func() {
		_, _ = c.DeleteProject(ctx, &codebuild.DeleteProjectInput{Name: aws.String("cb-sdk-project")})
	})

	get, err := c.BatchGetProjects(ctx, &codebuild.BatchGetProjectsInput{
		Names: []string{"cb-sdk-project"},
	})
	require.NoError(t, err)
	require.Len(t, get.Projects, 1)
	assert.Equal(t, "cb-sdk-project", aws.ToString(get.Projects[0].Name))
	assert.Empty(t, get.ProjectsNotFound)

	list, err := c.ListProjects(ctx, &codebuild.ListProjectsInput{})
	require.NoError(t, err)
	found := false
	for _, name := range list.Projects {
		if name == "cb-sdk-project" {
			found = true
		}
	}
	assert.True(t, found)

	_, err = c.UpdateProject(ctx, &codebuild.UpdateProjectInput{
		Name:        aws.String("cb-sdk-project"),
		Description: aws.String("updated"),
	})
	require.NoError(t, err)

	get, err = c.BatchGetProjects(ctx, &codebuild.BatchGetProjectsInput{
		Names: []string{"cb-sdk-project"},
	})
	require.NoError(t, err)
	assert.Equal(t, "updated", aws.ToString(get.Projects[0].Description))

	// Tags are set on create and accessible via BatchGetProjects.
	get, err = c.BatchGetProjects(ctx, &codebuild.BatchGetProjectsInput{
		Names: []string{"cb-sdk-project"},
	})
	require.NoError(t, err)
	found = false
	for _, tag := range get.Projects[0].Tags {
		if aws.ToString(tag.Key) == "env" && aws.ToString(tag.Value) == "sdk" {
			found = true
		}
	}
	assert.True(t, found, "expected env=sdk tag set at create time")
}

func TestCodeBuild_BuildLifecycle_SDK(t *testing.T) {
	c := codebuildClient()

	_, err := c.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name: aws.String("cb-sdk-build-project"),
		Source: &cbtypes.ProjectSource{
			Type:      cbtypes.SourceTypeNoSource,
			Buildspec: aws.String("version: 0.2\nphases:\n  build:\n    commands:\n      - test -f /etc/alpine-release\n      - printf codebuild-sdk-ready\n"),
		},
		Artifacts: &cbtypes.ProjectArtifacts{
			Type: cbtypes.ArtifactsTypeNoArtifacts,
		},
		Environment: &cbtypes.ProjectEnvironment{
			Type:        cbtypes.EnvironmentTypeLinuxContainer,
			Image:       aws.String("public.ecr.aws/docker/library/alpine:3.21"),
			ComputeType: cbtypes.ComputeTypeBuildGeneral1Small,
		},
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/cb-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteProject(ctx, &codebuild.DeleteProjectInput{Name: aws.String("cb-sdk-build-project")})
	})

	startResp, err := c.StartBuild(ctx, &codebuild.StartBuildInput{
		ProjectName: aws.String("cb-sdk-build-project"),
	})
	require.NoError(t, err)
	require.NotNil(t, startResp.Build)
	buildID := aws.ToString(startResp.Build.Id)
	assert.NotEmpty(t, buildID)
	require.NotNil(t, startResp.Build.Logs)
	require.NotNil(t, startResp.Build.Logs.CloudWatchLogs)
	assert.Equal(t, cbtypes.LogsConfigStatusTypeDisabled, startResp.Build.Logs.CloudWatchLogs.Status)

	var build cbtypes.Build
	require.Eventually(t, func() bool {
		builds, err := c.BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{
			Ids: []string{buildID},
		})
		require.NoError(t, err)
		require.Len(t, builds.Builds, 1)
		build = builds.Builds[0]
		return build.BuildStatus == cbtypes.StatusTypeSucceeded
	}, 10*time.Second, 100*time.Millisecond)
	assert.Equal(t, buildID, aws.ToString(build.Id))
	assert.NotEmpty(t, build.Phases)
	// Log enablement is the real LogsLocation member cloudWatchLogs.status
	// (the sim runs builds without a CloudWatch sink, so DISABLED).
	require.NotNil(t, build.Logs)
	require.NotNil(t, build.Logs.CloudWatchLogs)
	assert.Equal(t, cbtypes.LogsConfigStatusTypeDisabled, build.Logs.CloudWatchLogs.Status)

	buildList, err := c.ListBuildsForProject(ctx, &codebuild.ListBuildsForProjectInput{
		ProjectName: aws.String("cb-sdk-build-project"),
	})
	require.NoError(t, err)
	require.Contains(t, buildList.Ids, buildID)

	allBuilds, err := c.ListBuilds(ctx, &codebuild.ListBuildsInput{})
	require.NoError(t, err)
	require.Contains(t, allBuilds.Ids, buildID)
}

// TestCodeBuild_PrivateGitSourceRealContainer_SDK proves that imported source
// credentials are used by the real Git transport and that the checked-out
// buildspec executes inside the configured image rather than on the simulator
// host. The private repository, official AWS SDK, Git smart-HTTP server, and
// Docker image are all independent boundaries.
func TestCodeBuild_PrivateGitSourceRealContainer_SDK(t *testing.T) {
	c := codebuildClient()
	secretsAPI := smClient()
	testContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	const token = "codebuild-private-source-token"
	repository := githttp.ServeBasicAuth(t, "main", map[string]string{
		"buildspec.yml": `version: 0.2
phases:
  build:
    commands:
      - test -f /etc/alpine-release
      - test "$CODEBUILD_PROJECT_NAME" = "cb-private-source"
      - printf private-source-built
`,
	}, "oauth2", token)
	imported, err := c.ImportSourceCredentials(testContext, &codebuild.ImportSourceCredentialsInput{
		ServerType: cbtypes.ServerTypeGithub,
		AuthType:   cbtypes.AuthTypePersonalAccessToken,
		Username:   aws.String("oauth2"),
		Token:      aws.String(token),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteSourceCredentials(context.Background(), &codebuild.DeleteSourceCredentialsInput{Arn: imported.Arn})
	})

	const projectName = "cb-private-source"
	_, err = c.CreateProject(testContext, &codebuild.CreateProjectInput{
		Name: aws.String(projectName),
		Source: &cbtypes.ProjectSource{
			Type:     cbtypes.SourceTypeGithub,
			Location: aws.String(repository),
		},
		Artifacts: &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
		Environment: &cbtypes.ProjectEnvironment{
			Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String("public.ecr.aws/docker/library/alpine:3.21"),
			ComputeType: cbtypes.ComputeTypeBuildGeneral1Small,
		},
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/cb-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteProject(context.Background(), &codebuild.DeleteProjectInput{Name: aws.String(projectName)})
	})

	started, err := c.StartBuild(testContext, &codebuild.StartBuildInput{
		ProjectName:   aws.String(projectName),
		SourceVersion: aws.String("main"),
	})
	require.NoError(t, err)
	buildID := aws.ToString(started.Build.Id)
	require.Eventually(t, func() bool {
		builds, getErr := c.BatchGetBuilds(testContext, &codebuild.BatchGetBuildsInput{Ids: []string{buildID}})
		return getErr == nil && len(builds.Builds) == 1 &&
			builds.Builds[0].BuildStatus == cbtypes.StatusTypeSucceeded
	}, 4*time.Minute, 100*time.Millisecond)

	const (
		secretsToken = "codebuild-secrets-manager-source-token"
		secretName   = "/CodeBuild/private-source"
		secretProj   = "cb-private-secrets-manager"
	)
	secretsRepository := githttp.ServeBasicAuth(t, "main", map[string]string{
		"buildspec.yml": `version: 0.2
phases:
  build:
    commands:
      - test -f /etc/alpine-release
      - test "$CODEBUILD_PROJECT_NAME" = "cb-private-secrets-manager"
`,
	}, "oauth2", secretsToken)
	secret, err := secretsAPI.CreateSecret(testContext, &secretsmanager.CreateSecretInput{
		Name: aws.String(secretName),
		SecretString: aws.String(
			`{"ServerType":"GITHUB","AuthType":"PERSONAL_ACCESS_TOKEN","Token":"` + secretsToken + `"}`,
		),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = secretsAPI.DeleteSecret(context.Background(), &secretsmanager.DeleteSecretInput{
			SecretId:                   secret.ARN,
			ForceDeleteWithoutRecovery: aws.Bool(true),
		})
	})
	_, err = c.CreateProject(testContext, &codebuild.CreateProjectInput{
		Name: aws.String(secretProj),
		Source: &cbtypes.ProjectSource{
			Type:     cbtypes.SourceTypeGithub,
			Location: aws.String(secretsRepository),
			Auth: &cbtypes.SourceAuth{
				Type:     cbtypes.SourceAuthTypeSecretsManager,
				Resource: secret.ARN,
			},
		},
		Artifacts: &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
		Environment: &cbtypes.ProjectEnvironment{
			Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String("public.ecr.aws/docker/library/alpine:3.21"),
			ComputeType: cbtypes.ComputeTypeBuildGeneral1Small,
		},
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/cb-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteProject(context.Background(), &codebuild.DeleteProjectInput{Name: aws.String(secretProj)})
	})
	secretsBuild, err := c.StartBuild(testContext, &codebuild.StartBuildInput{
		ProjectName: aws.String(secretProj), SourceVersion: aws.String("main"),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		builds, getErr := c.BatchGetBuilds(testContext, &codebuild.BatchGetBuildsInput{
			Ids: []string{aws.ToString(secretsBuild.Build.Id)},
		})
		return getErr == nil && len(builds.Builds) == 1 &&
			builds.Builds[0].BuildStatus == cbtypes.StatusTypeSucceeded
	}, 4*time.Minute, 100*time.Millisecond)
}

// TestCodeBuild_ListBuildsSortOrder_SDK pins that ListBuildsForProject honors
// the sortOrder parameter: DESCENDING (the AWS default) returns most-recent
// builds first, ASCENDING reverses it. The sim used to ignore sortOrder and
// always sort alphabetically by build ID.
func TestCodeBuild_ListBuildsSortOrder_SDK(t *testing.T) {
	c := codebuildClient()
	proj := "cb-sortorder-project"
	_, err := c.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name:        aws.String(proj),
		Source:      &cbtypes.ProjectSource{Type: cbtypes.SourceTypeNoSource, Buildspec: aws.String("version: 0.2\nphases:\n  build:\n    commands:\n      - printf ok\n")},
		Artifacts:   &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
		Environment: &cbtypes.ProjectEnvironment{Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String("public.ecr.aws/docker/library/alpine:3.21"), ComputeType: cbtypes.ComputeTypeBuildGeneral1Small},
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/cb-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteProject(ctx, &codebuild.DeleteProjectInput{Name: aws.String(proj)}) })

	var ordered []string
	for i := 0; i < 3; i++ {
		sb, err := c.StartBuild(ctx, &codebuild.StartBuildInput{ProjectName: aws.String(proj)})
		require.NoError(t, err)
		ordered = append(ordered, aws.ToString(sb.Build.Id))
	}

	desc, err := c.ListBuildsForProject(ctx, &codebuild.ListBuildsForProjectInput{
		ProjectName: aws.String(proj),
		SortOrder:   cbtypes.SortOrderTypeDescending,
	})
	require.NoError(t, err)
	require.Len(t, desc.Ids, 3)
	// DESCENDING = most-recent (last started) first.
	assert.Equal(t, []string{ordered[2], ordered[1], ordered[0]}, desc.Ids)

	asc, err := c.ListBuildsForProject(ctx, &codebuild.ListBuildsForProjectInput{
		ProjectName: aws.String(proj),
		SortOrder:   cbtypes.SortOrderTypeAscending,
	})
	require.NoError(t, err)
	require.Len(t, asc.Ids, 3)
	assert.Equal(t, []string{ordered[0], ordered[1], ordered[2]}, asc.Ids)
}

// TestCodeBuild_FailedBuildPhaseContext_SDK pins where failure detail
// lives on the Build shape: the BUILD phase's contexts (PhaseContext),
// not the LogsLocation.
func TestCodeBuild_FailedBuildPhaseContext_SDK(t *testing.T) {
	c := codebuildClient()

	_, err := c.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name: aws.String("cb-sdk-fail-project"),
		Source: &cbtypes.ProjectSource{
			Type:      cbtypes.SourceTypeNoSource,
			Buildspec: aws.String("version: 0.2\nphases:\n  build:\n    commands:\n      - exit 7\n"),
		},
		Artifacts: &cbtypes.ProjectArtifacts{
			Type: cbtypes.ArtifactsTypeNoArtifacts,
		},
		Environment: &cbtypes.ProjectEnvironment{
			Type:        cbtypes.EnvironmentTypeLinuxContainer,
			Image:       aws.String("public.ecr.aws/docker/library/alpine:3.21"),
			ComputeType: cbtypes.ComputeTypeBuildGeneral1Small,
		},
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/cb-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteProject(ctx, &codebuild.DeleteProjectInput{Name: aws.String("cb-sdk-fail-project")})
	})

	startResp, err := c.StartBuild(ctx, &codebuild.StartBuildInput{
		ProjectName: aws.String("cb-sdk-fail-project"),
	})
	require.NoError(t, err)
	buildID := aws.ToString(startResp.Build.Id)

	var build cbtypes.Build
	require.Eventually(t, func() bool {
		builds, err := c.BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{
			Ids: []string{buildID},
		})
		require.NoError(t, err)
		require.Len(t, builds.Builds, 1)
		build = builds.Builds[0]
		return build.BuildStatus == cbtypes.StatusTypeFailed
	}, 10*time.Second, 100*time.Millisecond)

	var buildPhase *cbtypes.BuildPhase
	for i := range build.Phases {
		if build.Phases[i].PhaseType == cbtypes.BuildPhaseTypeBuild {
			buildPhase = &build.Phases[i]
		}
	}
	require.NotNil(t, buildPhase, "build must report a BUILD phase")
	require.NotEmpty(t, buildPhase.Contexts, "failed BUILD phase must carry a context")
	assert.Equal(t, "COMMAND_EXECUTION_ERROR", aws.ToString(buildPhase.Contexts[0].StatusCode))
	assert.Contains(t, aws.ToString(buildPhase.Contexts[0].Message), "status 7")
}

// TestCodeBuild_StopAndRetryBuild_SDK exercises StopBuild (a long-running build
// settles to STOPPED) and RetryBuild (a new build of the same project).
func TestCodeBuild_StopAndRetryBuild_SDK(t *testing.T) {
	c := codebuildClient()
	proj := "cb-sdk-stop-project"
	_, err := c.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name: aws.String(proj),
		// A short sleep keeps the build IN_PROGRESS long enough to stop it.
		Source:      &cbtypes.ProjectSource{Type: cbtypes.SourceTypeNoSource, Buildspec: aws.String("version: 0.2\nphases:\n  build:\n    commands:\n      - sleep 5\n")},
		Artifacts:   &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
		Environment: &cbtypes.ProjectEnvironment{Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String("public.ecr.aws/docker/library/alpine:3.21"), ComputeType: cbtypes.ComputeTypeBuildGeneral1Small},
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/cb-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteProject(ctx, &codebuild.DeleteProjectInput{Name: aws.String(proj)}) })

	start, err := c.StartBuild(ctx, &codebuild.StartBuildInput{ProjectName: aws.String(proj)})
	require.NoError(t, err)
	buildID := aws.ToString(start.Build.Id)

	stop, err := c.StopBuild(ctx, &codebuild.StopBuildInput{Id: aws.String(buildID)})
	require.NoError(t, err)
	require.NotNil(t, stop.Build)
	assert.Equal(t, cbtypes.StatusTypeStopped, stop.Build.BuildStatus)

	retry, err := c.RetryBuild(ctx, &codebuild.RetryBuildInput{Id: aws.String(buildID)})
	require.NoError(t, err)
	require.NotNil(t, retry.Build)
	assert.NotEqual(t, buildID, aws.ToString(retry.Build.Id), "RetryBuild creates a new build id")
	assert.Equal(t, proj, aws.ToString(retry.Build.ProjectName))
}

// TestCodeBuild_ReportGroupsAndReports_SDK covers report-group CRUD plus the
// reports a build produces when its buildspec references a report group:
// CreateReportGroup, UpdateReportGroup, BatchGetReportGroups, ListReportGroups,
// then ListReports / ListReportsForReportGroup / BatchGetReports, then
// DeleteReportGroup.
func TestCodeBuild_ReportGroupsAndReports_SDK(t *testing.T) {
	c := codebuildClient()
	rgName := "cb-sdk-rg"
	rg, err := c.CreateReportGroup(ctx, &codebuild.CreateReportGroupInput{
		Name: aws.String(rgName),
		Type: cbtypes.ReportTypeTest,
		ExportConfig: &cbtypes.ReportExportConfig{
			ExportConfigType: cbtypes.ReportExportConfigTypeNoExport,
		},
		Tags: []cbtypes.Tag{{Key: aws.String("team"), Value: aws.String("ci")}},
	})
	require.NoError(t, err)
	require.NotNil(t, rg.ReportGroup)
	rgArn := aws.ToString(rg.ReportGroup.Arn)
	assert.Equal(t, cbtypes.ReportGroupStatusTypeActive, rg.ReportGroup.Status)
	t.Cleanup(func() {
		_, _ = c.DeleteReportGroup(ctx, &codebuild.DeleteReportGroupInput{Arn: aws.String(rgArn), DeleteReports: true})
	})

	_, err = c.UpdateReportGroup(ctx, &codebuild.UpdateReportGroupInput{
		Arn:  aws.String(rgArn),
		Tags: []cbtypes.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
	})
	require.NoError(t, err)

	bg, err := c.BatchGetReportGroups(ctx, &codebuild.BatchGetReportGroupsInput{ReportGroupArns: []string{rgArn}})
	require.NoError(t, err)
	require.Len(t, bg.ReportGroups, 1)
	assert.Equal(t, rgName, aws.ToString(bg.ReportGroups[0].Name))
	assert.Empty(t, bg.ReportGroupsNotFound)

	lg, err := c.ListReportGroups(ctx, &codebuild.ListReportGroupsInput{})
	require.NoError(t, err)
	assert.Contains(t, lg.ReportGroups, rgArn)

	// A build whose buildspec references the report group produces a Report.
	proj := "cb-sdk-report-project"
	buildspec := "version: 0.2\nphases:\n  build:\n    commands:\n      - printf ok\nreports:\n  " + rgName + ":\n    files:\n      - '**/*'\n"
	_, err = c.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name:        aws.String(proj),
		Source:      &cbtypes.ProjectSource{Type: cbtypes.SourceTypeNoSource, Buildspec: aws.String(buildspec)},
		Artifacts:   &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
		Environment: &cbtypes.ProjectEnvironment{Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String("public.ecr.aws/docker/library/alpine:3.21"), ComputeType: cbtypes.ComputeTypeBuildGeneral1Small},
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/cb-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteProject(ctx, &codebuild.DeleteProjectInput{Name: aws.String(proj)}) })

	start, err := c.StartBuild(ctx, &codebuild.StartBuildInput{ProjectName: aws.String(proj)})
	require.NoError(t, err)
	buildID := aws.ToString(start.Build.Id)

	var reportArn string
	require.Eventually(t, func() bool {
		builds, err := c.BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{Ids: []string{buildID}})
		require.NoError(t, err)
		require.Len(t, builds.Builds, 1)
		b := builds.Builds[0]
		if b.BuildStatus != cbtypes.StatusTypeSucceeded {
			return false
		}
		if len(b.ReportArns) == 0 {
			return false
		}
		reportArn = b.ReportArns[0]
		return true
	}, 10*time.Second, 100*time.Millisecond)
	require.NotEmpty(t, reportArn)

	lr, err := c.ListReports(ctx, &codebuild.ListReportsInput{})
	require.NoError(t, err)
	assert.Contains(t, lr.Reports, reportArn)

	lrg, err := c.ListReportsForReportGroup(ctx, &codebuild.ListReportsForReportGroupInput{ReportGroupArn: aws.String(rgArn)})
	require.NoError(t, err)
	assert.Contains(t, lrg.Reports, reportArn)

	br, err := c.BatchGetReports(ctx, &codebuild.BatchGetReportsInput{ReportArns: []string{reportArn}})
	require.NoError(t, err)
	require.Len(t, br.Reports, 1)
	assert.Equal(t, rgArn, aws.ToString(br.Reports[0].ReportGroupArn))
	assert.Equal(t, cbtypes.ReportStatusTypeSucceeded, br.Reports[0].Status)
	assert.Empty(t, br.ReportsNotFound)
}

// TestCodeBuild_SourceCredentials_SDK covers ImportSourceCredentials,
// ListSourceCredentials, and DeleteSourceCredentials. The token is never echoed
// back; only the ARN, serverType, and authType are readable.
func TestCodeBuild_SourceCredentials_SDK(t *testing.T) {
	c := codebuildClient()
	imp, err := c.ImportSourceCredentials(ctx, &codebuild.ImportSourceCredentialsInput{
		Token:      aws.String("ghp_exampletoken"),
		ServerType: cbtypes.ServerTypeGithub,
		AuthType:   cbtypes.AuthTypePersonalAccessToken,
	})
	require.NoError(t, err)
	credArn := aws.ToString(imp.Arn)
	require.NotEmpty(t, credArn)
	t.Cleanup(func() {
		_, _ = c.DeleteSourceCredentials(ctx, &codebuild.DeleteSourceCredentialsInput{Arn: aws.String(credArn)})
	})

	list, err := c.ListSourceCredentials(ctx, &codebuild.ListSourceCredentialsInput{})
	require.NoError(t, err)
	found := false
	for _, info := range list.SourceCredentialsInfos {
		if aws.ToString(info.Arn) == credArn {
			found = true
			assert.Equal(t, cbtypes.ServerTypeGithub, info.ServerType)
			assert.Equal(t, cbtypes.AuthTypePersonalAccessToken, info.AuthType)
		}
	}
	assert.True(t, found, "imported credential must be listed")

	del, err := c.DeleteSourceCredentials(ctx, &codebuild.DeleteSourceCredentialsInput{Arn: aws.String(credArn)})
	require.NoError(t, err)
	assert.Equal(t, credArn, aws.ToString(del.Arn))
}
