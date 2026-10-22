package aws_sdk_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/amplify"
	"github.com/aws/aws-sdk-go-v2/service/batch"
	batchtypes "github.com/aws/aws-sdk-go-v2/service/batch/types"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	cbtypes "github.com/aws/aws-sdk-go-v2/service/codebuild/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// untrustedRoles returns the ARN of a role whose trust policy names another
// service, and an ARN in the same account that names no role.
func untrustedRoles(t *testing.T) (otherService, absent string) {
	t.Helper()
	otherService = serviceRole(t, "ec2.amazonaws.com")
	absent = otherService[:strings.LastIndex(otherService, "/")+1] + uniqueName("absent")
	return otherService, absent
}

func requireAWSError(t *testing.T, err error, code, message string) {
	t.Helper()
	requireAWSErrorCode(t, err, code)
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, message, apiErr.ErrorMessage())
}

// AWS Lambda refuses an execution role it cannot assume, at create and at
// configuration update, and accepts a role created a moment before.
func TestLambda_RefusesAnExecutionRoleItCannotAssume_SDK(t *testing.T) {
	t.Parallel()
	c := lambdaClient()
	otherService, absent := untrustedRoles(t)
	const refusal = "The role defined for the function cannot be assumed by Lambda."
	create := func(name, role string) error {
		_, err := c.CreateFunction(ctx, &lambda.CreateFunctionInput{
			FunctionName: aws.String(name),
			Role:         aws.String(role),
			Runtime:      lambdatypes.RuntimeNodejs20x,
			Handler:      aws.String("index.handler"),
			Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
		})
		return err
	}
	for _, role := range []string{otherService, absent} {
		requireAWSError(t, create(uniqueName("fn-untrusted"), role), "InvalidParameterValueException", refusal)
	}

	name := uniqueName("fn-trusted")
	require.NoError(t, create(name, serviceRole(t, "lambda.amazonaws.com")))
	t.Cleanup(func() {
		_, _ = c.DeleteFunction(context.Background(), &lambda.DeleteFunctionInput{FunctionName: aws.String(name)})
	})
	_, err := c.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{
		FunctionName: aws.String(name), Role: aws.String(otherService),
	})
	requireAWSError(t, err, "InvalidParameterValueException", refusal)
	replacement := serviceRole(t, "lambda.amazonaws.com")
	updated, err := c.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{
		FunctionName: aws.String(name), Role: aws.String(replacement),
	})
	require.NoError(t, err)
	assert.Equal(t, replacement, aws.ToString(updated.Role))
}

// AWS CodeBuild refuses a service role it cannot assume.
func TestCodeBuild_RefusesAServiceRoleItCannotAssume_SDK(t *testing.T) {
	t.Parallel()
	c := codebuildClient()
	otherService, absent := untrustedRoles(t)
	create := func(name, role string) error {
		_, err := c.CreateProject(ctx, &codebuild.CreateProjectInput{
			Name:      aws.String(name),
			Source:    &cbtypes.ProjectSource{Type: cbtypes.SourceTypeNoSource, Buildspec: aws.String("version: 0.2")},
			Artifacts: &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
			Environment: &cbtypes.ProjectEnvironment{
				Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String("aws/codebuild/standard:7.0"),
				ComputeType: cbtypes.ComputeTypeBuildGeneral1Small,
			},
			ServiceRole: aws.String(role),
		})
		return err
	}
	for _, role := range []string{otherService, absent} {
		requireAWSError(t, create(uniqueName("cb-untrusted"), role), "InvalidInputException",
			"CodeBuild is not authorized to perform: sts:AssumeRole on "+role)
	}

	name := uniqueName("cb-trusted")
	require.NoError(t, create(name, serviceRole(t, "codebuild.amazonaws.com")))
	t.Cleanup(func() {
		_, _ = c.DeleteProject(context.Background(), &codebuild.DeleteProjectInput{Name: aws.String(name)})
	})
	_, err := c.UpdateProject(ctx, &codebuild.UpdateProjectInput{Name: aws.String(name), ServiceRole: aws.String(otherService)})
	requireAWSError(t, err, "InvalidInputException", "CodeBuild is not authorized to perform: sts:AssumeRole on "+otherService)
}

// AWS Batch refuses a job role or an execution role that Amazon ECS tasks
// cannot assume.
func TestBatch_RefusesARoleECSTasksCannotAssume_SDK(t *testing.T) {
	t.Parallel()
	c := batchClient()
	otherService, absent := untrustedRoles(t)
	register := func(properties batchtypes.ContainerProperties) error {
		properties.Image = aws.String("mirror.gcr.io/library/alpine:3")
		properties.Command = []string{"true"}
		_, err := c.RegisterJobDefinition(ctx, &batch.RegisterJobDefinitionInput{
			JobDefinitionName:   aws.String(uniqueName("batch-role-trust")),
			Type:                batchtypes.JobDefinitionTypeContainer,
			ContainerProperties: &properties,
		})
		return err
	}
	requireAWSError(t, register(batchtypes.ContainerProperties{JobRoleArn: aws.String(otherService)}), "ClientException",
		"jobRoleArn "+otherService+" cannot be assumed by ecs-tasks.amazonaws.com")
	requireAWSError(t, register(batchtypes.ContainerProperties{ExecutionRoleArn: aws.String(absent)}), "ClientException",
		"executionRoleArn "+absent+" cannot be assumed by ecs-tasks.amazonaws.com")
	require.NoError(t, register(batchtypes.ContainerProperties{
		JobRoleArn:       aws.String(serviceRole(t, "ecs-tasks.amazonaws.com")),
		ExecutionRoleArn: aws.String(serviceRole(t, "ecs-tasks.amazonaws.com")),
	}))
}

// AWS Glue refuses a job role it cannot assume, named by ARN or by name, and
// accepts a trusted role by its name.
func TestGlue_RefusesAJobRoleItCannotAssume_SDK(t *testing.T) {
	t.Parallel()
	c := glueClient()
	otherService, absent := untrustedRoles(t)
	command := &gluetypes.JobCommand{Name: aws.String("glueetl"), ScriptLocation: aws.String("s3://bucket/script.py")}
	create := func(name, role string) error {
		_, err := c.CreateJob(ctx, &glue.CreateJobInput{Name: aws.String(name), Role: aws.String(role), Command: command})
		return err
	}
	otherServiceName := otherService[strings.LastIndex(otherService, "/")+1:]
	for _, role := range []string{otherService, absent, otherServiceName} {
		requireAWSError(t, create(uniqueName("glue-untrusted"), role), "InvalidInputException",
			"Role "+role+" should be given assume role permissions for Glue Service.")
	}

	trusted := serviceRole(t, "glue.amazonaws.com")
	name := uniqueName("glue-trusted")
	require.NoError(t, create(name, trusted[strings.LastIndex(trusted, "/")+1:]))
	t.Cleanup(func() { _, _ = c.DeleteJob(context.Background(), &glue.DeleteJobInput{JobName: aws.String(name)}) })
	_, err := c.UpdateJob(ctx, &glue.UpdateJobInput{
		JobName:   aws.String(name),
		JobUpdate: &gluetypes.JobUpdate{Role: aws.String(otherService), Command: command},
	})
	requireAWSError(t, err, "InvalidInputException", "Role "+otherService+" should be given assume role permissions for Glue Service.")
}

// AWS Amplify refuses a compute role or a service role it cannot assume, on
// an app and on a branch.
func TestAmplify_RefusesARoleItCannotAssume_SDK(t *testing.T) {
	t.Parallel()
	c := amplifyClient()
	otherService, absent := untrustedRoles(t)
	_, err := c.CreateApp(ctx, &amplify.CreateAppInput{Name: aws.String(uniqueName("amp-untrusted")), ComputeRoleArn: aws.String(otherService)})
	requireAWSError(t, err, "BadRequestException", "computeRoleArn "+otherService+" cannot be assumed by amplify.amazonaws.com")
	_, err = c.CreateApp(ctx, &amplify.CreateAppInput{Name: aws.String(uniqueName("amp-untrusted")), IamServiceRoleArn: aws.String(absent)})
	requireAWSError(t, err, "BadRequestException", "iamServiceRoleArn "+absent+" cannot be assumed by amplify.amazonaws.com")

	compute := serviceRole(t, "amplify.amazonaws.com")
	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name:              aws.String(uniqueName("amp-trusted")),
		ComputeRoleArn:    aws.String(compute),
		IamServiceRoleArn: aws.String(serviceRole(t, "amplify.amazonaws.com")),
	})
	require.NoError(t, err)
	appID := aws.ToString(app.App.AppId)
	t.Cleanup(func() { _, _ = c.DeleteApp(context.Background(), &amplify.DeleteAppInput{AppId: aws.String(appID)}) })
	assert.Equal(t, compute, aws.ToString(app.App.ComputeRoleArn))

	_, err = c.UpdateApp(ctx, &amplify.UpdateAppInput{AppId: aws.String(appID), ComputeRoleArn: aws.String(otherService)})
	requireAWSError(t, err, "BadRequestException", "computeRoleArn "+otherService+" cannot be assumed by amplify.amazonaws.com")
	_, err = c.CreateBranch(ctx, &amplify.CreateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"), ComputeRoleArn: aws.String(absent),
	})
	requireAWSError(t, err, "BadRequestException", "computeRoleArn "+absent+" cannot be assumed by amplify.amazonaws.com")
	_, err = c.CreateBranch(ctx, &amplify.CreateBranchInput{AppId: aws.String(appID), BranchName: aws.String("main")})
	require.NoError(t, err)
	_, err = c.UpdateBranch(ctx, &amplify.UpdateBranchInput{
		AppId: aws.String(appID), BranchName: aws.String("main"), ComputeRoleArn: aws.String(otherService),
	})
	requireAWSError(t, err, "BadRequestException", "computeRoleArn "+otherService+" cannot be assumed by amplify.amazonaws.com")
}

func ecsRoleRefusal(role string) string {
	return "ECS was unable to assume the role '" + role + "' that was provided for this task. " +
		"Please verify that the role being passed has the proper trust relationship and permissions " +
		"and that your IAM user has permissions to pass this role."
}

// Amazon ECS registers a task definition whatever roles it names, and refuses
// to launch a task whose task role or execution role it cannot assume, named
// by the task definition or by the task's overrides.
func TestECS_RefusesToLaunchATaskWithARoleItCannotAssume_SDK(t *testing.T) {
	t.Parallel()
	c := ecsClient()
	otherService, absent := untrustedRoles(t)
	cluster := uniqueName("ecs-role-trust")
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCluster(context.Background(), &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})
	subnetID := createECSTestSubnet(t, "ecs-role-trust")

	register := func(taskRole, executionRole string) *string {
		input := &ecs.RegisterTaskDefinitionInput{
			Family:                  aws.String(uniqueName("ecs-role-trust")),
			NetworkMode:             ecstypes.NetworkModeAwsvpc,
			RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
			Cpu:                     aws.String("256"),
			Memory:                  aws.String("512"),
			ContainerDefinitions: []ecstypes.ContainerDefinition{{
				Name:        aws.String("app"),
				Image:       aws.String("mirror.gcr.io/library/alpine:3.21"),
				Command:     []string{"true"},
				StopTimeout: aws.Int32(2),
			}},
		}
		if taskRole != "" {
			input.TaskRoleArn = aws.String(taskRole)
		}
		if executionRole != "" {
			input.ExecutionRoleArn = aws.String(executionRole)
		}
		out, err := c.RegisterTaskDefinition(ctx, input)
		require.NoError(t, err)
		return out.TaskDefinition.TaskDefinitionArn
	}
	run := func(taskDefinition *string, overrides *ecstypes.TaskOverride) (*ecs.RunTaskOutput, error) {
		return c.RunTask(ctx, &ecs.RunTaskInput{
			Cluster:        aws.String(cluster),
			TaskDefinition: taskDefinition,
			LaunchType:     ecstypes.LaunchTypeFargate,
			Overrides:      overrides,
			NetworkConfiguration: &ecstypes.NetworkConfiguration{
				AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
			},
		})
	}

	_, err = run(register(otherService, ""), nil)
	requireAWSError(t, err, "ClientException", ecsRoleRefusal(otherService))
	_, err = run(register("", absent), nil)
	requireAWSError(t, err, "ClientException", ecsRoleRefusal(absent))

	trusted := register(serviceRole(t, "ecs-tasks.amazonaws.com"), serviceRole(t, "ecs-tasks.amazonaws.com"))
	_, err = run(trusted, &ecstypes.TaskOverride{TaskRoleArn: aws.String(otherService)})
	requireAWSError(t, err, "ClientException", ecsRoleRefusal(otherService))
	_, err = run(trusted, &ecstypes.TaskOverride{ExecutionRoleArn: aws.String(absent)})
	requireAWSError(t, err, "ClientException", ecsRoleRefusal(absent))

	out, err := run(trusted, nil)
	require.NoError(t, err)
	require.Len(t, out.Tasks, 1)
	taskArn := aws.ToString(out.Tasks[0].TaskArn)
	cleanupECSTask(t, c, cluster, taskArn)
	waitTaskStopped(t, c, cluster, taskArn)
}
