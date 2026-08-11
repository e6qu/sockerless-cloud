package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scopedCredentials mints an IAM user carrying policyDocument and returns a
// config signed with its access key, which is what makes the call-time gate
// evaluate a real principal instead of treating the caller as an unregistered
// test credential.
func scopedCredentials(t *testing.T, user, policyName, policyDocument string) aws.Config {
	t.Helper()
	admin := iamClient()
	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
	})
	_, err = admin.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       aws.String(user),
		PolicyName:     aws.String(policyName),
		PolicyDocument: aws.String(policyDocument),
	})
	require.NoError(t, err)
	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	return aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")}
}

// A project-scoped AWS CodeBuild grant allows the project it names and denies
// another. This is the failure that follows Amazon ECR in an image-source
// build loop: the control plane describes the repository, then starts the
// build, and both were denied for the same reason.
func TestIAM_ResourceARN_CodeBuild(t *testing.T) {
	admin := codebuildClient()
	mkProject := func(name string) {
		_, err := admin.CreateProject(ctx, &codebuild.CreateProjectInput{
			Name:      aws.String(name),
			Source:    &cbtypes.ProjectSource{Type: cbtypes.SourceTypeNoSource},
			Artifacts: &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
			Environment: &cbtypes.ProjectEnvironment{
				Type:        cbtypes.EnvironmentTypeLinuxContainer,
				Image:       aws.String("public.ecr.aws/docker/library/alpine:3.21"),
				ComputeType: cbtypes.ComputeTypeBuildGeneral1Small,
			},
			ServiceRole: aws.String("arn:aws:iam::123456789012:role/cb-scoped-role"),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = admin.DeleteProject(ctx, &codebuild.DeleteProjectInput{Name: aws.String(name)})
		})
	}
	mkProject("cb-scoped-project")
	mkProject("cb-other-project")

	cfg := scopedCredentials(t, "cb-scoped-user", "one-project",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"codebuild:BatchGetProjects",`+
			`"Resource":"arn:aws:codebuild:us-east-1:123456789012:project/cb-scoped-project"}]}`)
	scoped := codebuild.NewFromConfig(cfg, func(o *codebuild.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err := scoped.BatchGetProjects(ctx, &codebuild.BatchGetProjectsInput{Names: []string{"cb-scoped-project"}})
	assert.NoError(t, err, "the granted project must be readable")

	_, err = scoped.BatchGetProjects(ctx, &codebuild.BatchGetProjectsInput{Names: []string{"cb-other-project"}})
	require.Error(t, err, "a project outside the grant must be denied")
	assert.Equal(t, "AccessDeniedException", errCodeOf(err))
}

// CloudWatch Logs defines nested resource types, and the four stream-scoped
// actions authorize against the stream while the rest authorize against the
// group. A grant written the way the console writes it — the group ARN plus
// the ":*" form that covers its streams — must therefore allow both a
// group-scoped read and a stream write, and still deny another group.
func TestIAM_ResourceARN_CloudWatchLogsGroupAndStream(t *testing.T) {
	admin := cloudwatchlogs.NewFromConfig(sdkConfig(), func(o *cloudwatchlogs.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
	for _, group := range []string{"/scoped/app", "/other/app"} {
		_, err := admin.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = admin.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
		})
		_, err = admin.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
			LogGroupName: aws.String(group), LogStreamName: aws.String("s1"),
		})
		require.NoError(t, err)
	}

	const arn = "arn:aws:logs:us-east-1:123456789012:log-group:/scoped/app"
	cfg := scopedCredentials(t, "logs-scoped-user", "one-group",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",`+
			`"Action":["logs:FilterLogEvents","logs:DescribeLogStreams","logs:PutLogEvents"],`+
			`"Resource":["`+arn+`","`+arn+`:*"]}]}`)
	scoped := cloudwatchlogs.NewFromConfig(cfg, func(o *cloudwatchlogs.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})

	_, err := scoped.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String("/scoped/app"),
	})
	assert.NoError(t, err, "a group-scoped read of the granted group must succeed")

	_, err = scoped.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String("/scoped/app"),
	})
	assert.NoError(t, err, "DescribeLogStreams is group-scoped and must succeed")

	_, err = scoped.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String("/scoped/app"),
		LogStreamName: aws.String("s1"),
		LogEvents: []cwltypes.InputLogEvent{{
			Timestamp: aws.Int64(time.Now().UnixMilli()),
			Message:   aws.String("scoped write"),
		}},
	})
	assert.NoError(t, err, "a stream write under the granted group must succeed")

	_, err = scoped.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String("/other/app"),
	})
	require.Error(t, err, "another log group must be denied")
	assert.Equal(t, "AccessDeniedException", errCodeOf(err))
}

// iam:PassRole is authorized separately from the operation that passes the
// role, against the role's own ARN. A caller allowed to register a task
// definition but allowed to pass only one role is denied when it passes
// another — which is the entire purpose of scoping PassRole.
func TestIAM_PassRole_IsAuthorizedAgainstTheRolePassed(t *testing.T) {
	admin := iamClient()
	for _, role := range []string{"ecs-task-allowed", "ecs-task-forbidden"} {
		_, err := admin.CreateRole(ctx, &iam.CreateRoleInput{
			RoleName: aws.String(role),
			AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
				`"Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = admin.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(role)})
		})
	}

	cfg := scopedCredentials(t, "ecs-passrole-user", "register-and-pass-one-role",
		`{"Version":"2012-10-17","Statement":[`+
			`{"Effect":"Allow","Action":"ecs:RegisterTaskDefinition","Resource":"*"},`+
			`{"Effect":"Allow","Action":"iam:PassRole",`+
			`"Resource":"arn:aws:iam::123456789012:role/ecs-task-allowed"}]}`)
	scoped := ecs.NewFromConfig(cfg, func(o *ecs.Options) { o.BaseEndpoint = aws.String(baseURL) })

	register := func(role string) error {
		_, err := scoped.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
			Family:      aws.String("passrole-family"),
			TaskRoleArn: aws.String("arn:aws:iam::123456789012:role/" + role),
			ContainerDefinitions: []ecstypes.ContainerDefinition{{
				Name:  aws.String("app"),
				Image: aws.String("public.ecr.aws/docker/library/alpine:3.21"),
			}},
		})
		return err
	}

	assert.NoError(t, register("ecs-task-allowed"),
		"registering while passing the granted role must succeed")

	err := register("ecs-task-forbidden")
	require.Error(t, err, "passing a role outside the PassRole grant must be denied "+
		"even though ecs:RegisterTaskDefinition itself is allowed")
	assert.Equal(t, "AccessDeniedException", errCodeOf(err))
}
