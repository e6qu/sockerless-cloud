package aws_sdk_test

import (
	"encoding/base64"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/e6qu/sockerless-cloud/testutil/registrytrust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A privileged CodeBuild build environment runs docker steps against the
// engine the mode grants it: its buildspec logs in to this simulator's ECR
// with the token the control plane issued, builds an image and pushes it.
// A Lambda function created from that reference — the registry named at
// the coordinate the engine reaches it at — then pulls the image from the
// registry with the host's own ECR credential and runs it. This is the
// path a backend's bootstrap overlay takes: CodeBuild builds and pushes,
// Lambda pulls.
func TestCodeBuild_PrivilegedBuildPushesToECRAndLambdaRunsIt(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("platform gate: the container engine performs the registry push and pull itself, and on a host whose engine runs inside its own virtual machine it has no route to this host's loopback, where the simulator's registry listens. Linux hosts share one loopback between engine and simulator.")
	}

	cb := codebuildClient()
	ec := ecrClient()
	lc := lambdaClient()
	cw := cwLogsClient()

	const repo = "codebuild-overlay"
	_, err := ec.CreateRepository(ctx, &ecr.CreateRepositoryInput{RepositoryName: aws.String(repo)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ec.DeleteRepository(ctx, &ecr.DeleteRepositoryInput{RepositoryName: aws.String(repo), Force: true})
	})

	// The registry at the coordinate the engine reaches this simulator at.
	// Docker trusts a plain-HTTP loopback registry natively; Podman takes a
	// scoped registries.conf.d entry, which the helper writes and removes.
	registry := fmt.Sprintf("127.0.0.1:%d", simPort)
	cleanupTrust, err := registrytrust.ConfigureLoopbackHTTPRegistry(ctx, registry)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanupTrust()) })
	image := registry + "/" + repo + ":built"

	token, err := ec.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	require.NoError(t, err)
	require.Len(t, token.AuthorizationData, 1)
	password := ecrPasswordFromAuthorizationToken(t, aws.ToString(token.AuthorizationData[0].AuthorizationToken))

	const project = "cb-privileged-docker-push"
	_, err = cb.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name: aws.String(project),
		Source: &cbtypes.ProjectSource{
			Type: cbtypes.SourceTypeNoSource,
			Buildspec: aws.String(strings.Join([]string{
				"version: 0.2",
				"phases:",
				"  pre_build:",
				"    commands:",
				`      - echo "$ECR_PASSWORD" | docker login --username AWS --password-stdin "$REGISTRY"`,
				"  build:",
				"    commands:",
				`      - printf 'FROM public.ecr.aws/docker/library/alpine:latest\nCMD ["echo", "built-by-codebuild"]\n' > Dockerfile`,
				`      - docker build -t "$IMAGE" .`,
				"  post_build:",
				"    commands:",
				`      - docker push "$IMAGE"`,
				"",
			}, "\n")),
		},
		Artifacts: &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
		Environment: &cbtypes.ProjectEnvironment{
			// The environment type matching this host's architecture, so
			// the build runs natively — an ARM_CONTAINER on an arm64 host.
			Type:           codeBuildEnvironmentTypeForHost(),
			Image:          aws.String("public.ecr.aws/docker/library/docker:27-cli"),
			ComputeType:    cbtypes.ComputeTypeBuildGeneral1Small,
			PrivilegedMode: aws.Bool(true),
			EnvironmentVariables: []cbtypes.EnvironmentVariable{
				{Name: aws.String("REGISTRY"), Value: aws.String(registry)},
				{Name: aws.String("IMAGE"), Value: aws.String(image)},
			},
		},
		ServiceRole: aws.String("arn:aws:iam::123456789012:role/cb-role"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cb.DeleteProject(ctx, &codebuild.DeleteProjectInput{Name: aws.String(project)})
	})

	started, err := cb.StartBuild(ctx, &codebuild.StartBuildInput{
		ProjectName: aws.String(project),
		EnvironmentVariablesOverride: []cbtypes.EnvironmentVariable{
			{Name: aws.String("ECR_PASSWORD"), Value: aws.String(password)},
		},
	})
	require.NoError(t, err)
	buildID := aws.ToString(started.Build.Id)

	var build cbtypes.Build
	require.Eventually(t, func() bool {
		builds, err := cb.BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{Ids: []string{buildID}})
		require.NoError(t, err)
		require.Len(t, builds.Builds, 1)
		build = builds.Builds[0]
		switch build.BuildStatus {
		case cbtypes.StatusTypeSucceeded, cbtypes.StatusTypeFailed, cbtypes.StatusTypeFault, cbtypes.StatusTypeTimedOut, cbtypes.StatusTypeStopped:
			return true
		}
		return false
	}, cbBuildCompletionBudget, 250*time.Millisecond)
	require.Equal(t, cbtypes.StatusTypeSucceeded, build.BuildStatus, "build phases: %s\nbuild log:\n%s", describeBuildPhases(build), buildLog(t, cw, build))

	// The push landed in the repository.
	images, err := ec.DescribeImages(ctx, &ecr.DescribeImagesInput{RepositoryName: aws.String(repo)})
	require.NoError(t, err)
	var tags []string
	for _, d := range images.ImageDetails {
		tags = append(tags, d.ImageTags...)
	}
	assert.Contains(t, tags, "built")

	// A function created from the pushed reference runs the pushed image.
	fnName := "codebuild-overlay-fn"
	_, err = lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		PackageType:  lambdatypes.PackageTypeImage,
		Code:         &lambdatypes.FunctionCode{ImageUri: aws.String(image)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)})
	})

	invoked, err := lc.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)
	assert.Nil(t, invoked.FunctionError, "payload: %s", string(invoked.Payload))

	logGroupName := "/aws/lambda/" + fnName
	events, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(logGroupName)})
	require.NoError(t, err)
	var messages []string
	for _, e := range events.Events {
		messages = append(messages, aws.ToString(e.Message))
	}
	assert.True(t, strings.Contains(strings.Join(messages, "\n"), "built-by-codebuild"), "the pushed image's command ran: %v", messages)
	_, _ = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroupName)})
}

// codeBuildEnvironmentTypeForHost is the build environment type whose
// architecture is this host's: ARM_CONTAINER on arm64, LINUX_CONTAINER
// (x86_64) otherwise.
func codeBuildEnvironmentTypeForHost() cbtypes.EnvironmentType {
	if runtime.GOARCH == "arm64" {
		return cbtypes.EnvironmentTypeArmContainer
	}
	return cbtypes.EnvironmentTypeLinuxContainer
}

// buildLog reads a build's CloudWatch log stream.
func buildLog(t *testing.T, cw *cloudwatchlogs.Client, build cbtypes.Build) string {
	t.Helper()
	if build.Logs == nil || build.Logs.GroupName == nil {
		return "(no log location)"
	}
	events, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName:   build.Logs.GroupName,
		LogStreamNames: []string{aws.ToString(build.Logs.StreamName)},
	})
	if err != nil {
		return "(read log: " + err.Error() + ")"
	}
	var lines []string
	for _, e := range events.Events {
		lines = append(lines, aws.ToString(e.Message))
	}
	return strings.Join(lines, "\n")
}

// describeBuildPhases renders a build's phases and their contexts, which is
// where a failed buildspec command's message lives.
func describeBuildPhases(build cbtypes.Build) string {
	var parts []string
	if build.Environment != nil {
		parts = append(parts, fmt.Sprintf("environment %s %s", build.Environment.Type, aws.ToString(build.Environment.Image)))
	}
	for _, p := range build.Phases {
		line := fmt.Sprintf("%s=%s", p.PhaseType, p.PhaseStatus)
		for _, c := range p.Contexts {
			line += " " + aws.ToString(c.Message)
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, "; ")
}

// ecrPasswordFromAuthorizationToken returns the password half of an ECR
// authorization token — base64 of `AWS:<password>` — which is what
// `aws ecr get-login-password` prints.
func ecrPasswordFromAuthorizationToken(t *testing.T, token string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(token)
	require.NoError(t, err)
	user, password, found := strings.Cut(string(raw), ":")
	require.True(t, found, "authorization token is user:password")
	require.Equal(t, "AWS", user)
	return password
}
