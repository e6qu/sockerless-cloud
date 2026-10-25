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
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfntypes "github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"github.com/aws/smithy-go"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// untrustedRoles returns the ARN of a role whose trust policy names another
// service, and an ARN in the same account that names no role.
func untrustedRoles(t *testing.T) (otherService, absent string) {
	t.Helper()
	otherService = serviceRole(t, "ec2.amazonaws.com")
	absent = otherService[:strings.LastIndex(otherService, "/")+1] + uniquename.New("absent")
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
		requireAWSError(t, create(uniquename.New("fn-untrusted"), role), "InvalidParameterValueException", refusal)
	}

	name := uniquename.New("fn-trusted")
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
		requireAWSError(t, create(uniquename.New("cb-untrusted"), role), "InvalidInputException",
			"CodeBuild is not authorized to perform: sts:AssumeRole on "+role)
	}

	name := uniquename.New("cb-trusted")
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
			JobDefinitionName:   aws.String(uniquename.New("batch-role-trust")),
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
		requireAWSError(t, create(uniquename.New("glue-untrusted"), role), "InvalidInputException",
			"Role "+role+" should be given assume role permissions for Glue Service.")
	}

	trusted := serviceRole(t, "glue.amazonaws.com")
	name := uniquename.New("glue-trusted")
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
	_, err := c.CreateApp(ctx, &amplify.CreateAppInput{Name: aws.String(uniquename.New("amp-untrusted")), ComputeRoleArn: aws.String(otherService)})
	requireAWSError(t, err, "BadRequestException", "computeRoleArn "+otherService+" cannot be assumed by amplify.amazonaws.com")
	_, err = c.CreateApp(ctx, &amplify.CreateAppInput{Name: aws.String(uniquename.New("amp-untrusted")), IamServiceRoleArn: aws.String(absent)})
	requireAWSError(t, err, "BadRequestException", "iamServiceRoleArn "+absent+" cannot be assumed by amplify.amazonaws.com")

	compute := serviceRole(t, "amplify.amazonaws.com")
	app, err := c.CreateApp(ctx, &amplify.CreateAppInput{
		Name:              aws.String(uniquename.New("amp-trusted")),
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

// AWS Batch accepts a compute environment whose service role it cannot
// assume and leaves it INVALID, and an update to a role it can assume makes it
// VALID.
func TestBatch_ComputeEnvironmentWithAServiceRoleItCannotAssumeIsInvalid_SDK(t *testing.T) {
	t.Parallel()
	c := batchClient()
	otherService, absent := untrustedRoles(t)
	describe := func(name string) batchtypes.ComputeEnvironmentDetail {
		out, err := c.DescribeComputeEnvironments(ctx, &batch.DescribeComputeEnvironmentsInput{ComputeEnvironments: []string{name}})
		require.NoError(t, err)
		require.Len(t, out.ComputeEnvironments, 1)
		return out.ComputeEnvironments[0]
	}
	for _, role := range []string{otherService, absent, otherService[strings.LastIndex(otherService, "/")+1:]} {
		name := uniquename.New("batch-ce-untrusted")
		_, err := c.CreateComputeEnvironment(ctx, &batch.CreateComputeEnvironmentInput{
			ComputeEnvironmentName: aws.String(name), Type: batchtypes.CETypeUnmanaged, ServiceRole: aws.String(role),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = c.DeleteComputeEnvironment(context.Background(), &batch.DeleteComputeEnvironmentInput{ComputeEnvironment: aws.String(name)})
		})
		ce := describe(name)
		assert.Equal(t, batchtypes.CEStatusInvalid, ce.Status)
		assert.True(t, strings.HasPrefix(aws.ToString(ce.StatusReason),
			"CLIENT_ERROR - Not authorized to perform sts:AssumeRole (Service: AWSSecurityTokenService; Status Code: 403; Error Code: AccessDenied; Request ID: "),
			aws.ToString(ce.StatusReason))
	}

	name := uniquename.New("batch-ce-trusted")
	_, err := c.CreateComputeEnvironment(ctx, &batch.CreateComputeEnvironmentInput{
		ComputeEnvironmentName: aws.String(name), Type: batchtypes.CETypeUnmanaged, ServiceRole: aws.String(otherService),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteComputeEnvironment(context.Background(), &batch.DeleteComputeEnvironmentInput{ComputeEnvironment: aws.String(name)})
	})
	_, err = c.UpdateComputeEnvironment(ctx, &batch.UpdateComputeEnvironmentInput{
		ComputeEnvironment: aws.String(name), ServiceRole: aws.String(serviceRole(t, "batch.amazonaws.com")),
	})
	require.NoError(t, err)
	ce := describe(name)
	assert.Equal(t, batchtypes.CEStatusValid, ce.Status)
	assert.Empty(t, aws.ToString(ce.StatusReason))
}

// AWS CodeBuild refuses a build or a batch build whose service role override
// it cannot assume.
func TestCodeBuild_RefusesAServiceRoleOverrideItCannotAssume_SDK(t *testing.T) {
	t.Parallel()
	c := codebuildClient()
	otherService, absent := untrustedRoles(t)
	name := uniquename.New("cb-override")
	_, err := c.CreateProject(ctx, &codebuild.CreateProjectInput{
		Name:      aws.String(name),
		Source:    &cbtypes.ProjectSource{Type: cbtypes.SourceTypeNoSource, Buildspec: aws.String("version: 0.2")},
		Artifacts: &cbtypes.ProjectArtifacts{Type: cbtypes.ArtifactsTypeNoArtifacts},
		Environment: &cbtypes.ProjectEnvironment{
			Type: cbtypes.EnvironmentTypeLinuxContainer, Image: aws.String("aws/codebuild/standard:7.0"),
			ComputeType: cbtypes.ComputeTypeBuildGeneral1Small,
		},
		ServiceRole: aws.String(serviceRole(t, "codebuild.amazonaws.com")),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteProject(context.Background(), &codebuild.DeleteProjectInput{Name: aws.String(name)})
	})
	for _, role := range []string{otherService, absent} {
		_, err := c.StartBuild(ctx, &codebuild.StartBuildInput{ProjectName: aws.String(name), ServiceRoleOverride: aws.String(role)})
		requireAWSError(t, err, "InvalidInputException", "CodeBuild is not authorized to perform: sts:AssumeRole on "+role)
		_, err = c.StartBuildBatch(ctx, &codebuild.StartBuildBatchInput{ProjectName: aws.String(name), ServiceRoleOverride: aws.String(role)})
		requireAWSError(t, err, "InvalidInputException", "CodeBuild is not authorized to perform: sts:AssumeRole on "+role)
	}
}

// AWS CodeBuild refuses a fleet service role it cannot assume, at create and
// at update.
func TestCodeBuild_RefusesAFleetServiceRoleItCannotAssume_SDK(t *testing.T) {
	t.Parallel()
	c := codebuildClient()
	otherService, absent := untrustedRoles(t)
	create := func(role string) (*codebuild.CreateFleetOutput, error) {
		return c.CreateFleet(ctx, &codebuild.CreateFleetInput{
			Name:             aws.String(uniquename.New("cb-fleet-role")),
			BaseCapacity:     aws.Int32(1),
			ComputeType:      cbtypes.ComputeTypeBuildGeneral1Small,
			EnvironmentType:  cbtypes.EnvironmentTypeLinuxContainer,
			FleetServiceRole: aws.String(role),
		})
	}
	for _, role := range []string{otherService, absent} {
		_, err := create(role)
		requireAWSError(t, err, "InvalidInputException", "CodeBuild is not authorized to perform: sts:AssumeRole on "+role)
	}

	trusted := serviceRole(t, "codebuild.amazonaws.com")
	created, err := create(trusted)
	require.NoError(t, err)
	arn := aws.ToString(created.Fleet.Arn)
	t.Cleanup(func() {
		_, _ = c.DeleteFleet(context.Background(), &codebuild.DeleteFleetInput{Arn: aws.String(arn)})
	})
	assert.Equal(t, trusted, aws.ToString(created.Fleet.FleetServiceRole))
	_, err = c.UpdateFleet(ctx, &codebuild.UpdateFleetInput{Arn: aws.String(arn), FleetServiceRole: aws.String(otherService)})
	requireAWSError(t, err, "InvalidInputException", "CodeBuild is not authorized to perform: sts:AssumeRole on "+otherService)
	got, err := c.BatchGetFleets(ctx, &codebuild.BatchGetFleetsInput{Names: []string{arn}})
	require.NoError(t, err)
	require.Len(t, got.Fleets, 1)
	assert.Equal(t, trusted, aws.ToString(got.Fleets[0].FleetServiceRole))
}

// AWS Glue refuses a crawler, an interactive session, a machine learning
// transform or a development endpoint whose role it cannot assume.
func TestGlue_RefusesARoleItCannotAssumeBeyondJobs_SDK(t *testing.T) {
	t.Parallel()
	c := glueClient()
	otherService, absent := untrustedRoles(t)
	const crawlerRefusal = "Service is unable to assume provided role. Please verify role's TrustPolicy"
	jobRefusal := func(role string) string {
		return "Role " + role + " should be given assume role permissions for Glue Service."
	}
	targets := &gluetypes.CrawlerTargets{S3Targets: []gluetypes.S3Target{{Path: aws.String("s3://bucket/prefix")}}}
	transform := func(role string) (*glue.CreateMLTransformOutput, error) {
		return c.CreateMLTransform(ctx, &glue.CreateMLTransformInput{
			Name:              aws.String(uniquename.New("glue-mlt")),
			InputRecordTables: []gluetypes.GlueTable{{DatabaseName: aws.String("db"), TableName: aws.String("tbl")}},
			Parameters:        &gluetypes.TransformParameters{TransformType: gluetypes.TransformTypeFindMatches},
			Role:              aws.String(role),
		})
	}
	for _, role := range []string{otherService, absent} {
		_, err := c.CreateCrawler(ctx, &glue.CreateCrawlerInput{Name: aws.String(uniquename.New("glue-crawler")), Role: aws.String(role), Targets: targets})
		requireAWSError(t, err, "InvalidInputException", crawlerRefusal)
		_, err = c.CreateSession(ctx, &glue.CreateSessionInput{
			Id: aws.String(uniquename.New("glue-session")), Role: aws.String(role),
			Command: &gluetypes.SessionCommand{Name: aws.String("glueetl")},
		})
		requireAWSError(t, err, "InvalidInputException", jobRefusal(role))
		_, err = transform(role)
		requireAWSError(t, err, "InvalidInputException", "Unable to assume role "+role)
		_, err = c.CreateDevEndpoint(ctx, &glue.CreateDevEndpointInput{EndpointName: aws.String(uniquename.New("glue-dev")), RoleArn: aws.String(role)})
		requireAWSError(t, err, "InvalidInputException", jobRefusal(role))
	}

	trusted := serviceRole(t, "glue.amazonaws.com")
	crawler := uniquename.New("glue-crawler")
	_, err := c.CreateCrawler(ctx, &glue.CreateCrawlerInput{Name: aws.String(crawler), Role: aws.String(trusted), Targets: targets})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCrawler(context.Background(), &glue.DeleteCrawlerInput{Name: aws.String(crawler)})
	})
	_, err = c.UpdateCrawler(ctx, &glue.UpdateCrawlerInput{Name: aws.String(crawler), Role: aws.String(otherService)})
	requireAWSError(t, err, "InvalidInputException", crawlerRefusal)

	created, err := transform(trusted)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteMLTransform(context.Background(), &glue.DeleteMLTransformInput{TransformId: created.TransformId})
	})
	_, err = c.UpdateMLTransform(ctx, &glue.UpdateMLTransformInput{TransformId: created.TransformId, Role: aws.String(absent)})
	requireAWSError(t, err, "InvalidInputException", "Unable to assume role "+absent)
}

// AWS Glue refuses a data quality run, a table's column statistics task
// settings, a column statistics task run or a blueprint run whose role it
// cannot assume.
func TestGlue_RefusesARunRoleItCannotAssume_SDK(t *testing.T) {
	t.Parallel()
	c := glueClient()
	otherService, absent := untrustedRoles(t)
	refusal := func(role string) string {
		return "Role " + role + " should be given assume role permissions for Glue Service."
	}
	database, table, blueprint := uniquename.New("glue-run-role"), "events", uniquename.New("glue-run-role")
	_, err := c.CreateDatabase(ctx, &glue.CreateDatabaseInput{DatabaseInput: &gluetypes.DatabaseInput{Name: aws.String(database)}})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDatabase(context.Background(), &glue.DeleteDatabaseInput{Name: aws.String(database)})
	})
	_, err = c.CreateTable(ctx, &glue.CreateTableInput{DatabaseName: aws.String(database), TableInput: &gluetypes.TableInput{Name: aws.String(table)}})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteTable(context.Background(), &glue.DeleteTableInput{DatabaseName: aws.String(database), Name: aws.String(table)})
	})
	_, err = c.CreateBlueprint(ctx, &glue.CreateBlueprintInput{Name: aws.String(blueprint), BlueprintLocation: aws.String("s3://bucket/blueprint.zip")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteBlueprint(context.Background(), &glue.DeleteBlueprintInput{Name: aws.String(blueprint)})
	})

	source := &gluetypes.DataSource{GlueTable: &gluetypes.GlueTable{DatabaseName: aws.String(database), TableName: aws.String(table)}}
	settings := func(role string) error {
		_, err := c.CreateColumnStatisticsTaskSettings(ctx, &glue.CreateColumnStatisticsTaskSettingsInput{
			DatabaseName: aws.String(database), TableName: aws.String(table), Role: aws.String(role),
		})
		return err
	}
	for _, role := range []string{otherService, absent} {
		_, err := c.StartDataQualityRulesetEvaluationRun(ctx, &glue.StartDataQualityRulesetEvaluationRunInput{
			DataSource: source, Role: aws.String(role), RulesetNames: []string{uniquename.New("glue-ruleset")},
		})
		requireAWSError(t, err, "InvalidInputException", refusal(role))
		_, err = c.StartDataQualityRuleRecommendationRun(ctx, &glue.StartDataQualityRuleRecommendationRunInput{
			DataSource: source, Role: aws.String(role),
		})
		requireAWSError(t, err, "InvalidInputException", refusal(role))
		requireAWSError(t, settings(role), "InvalidInputException", refusal(role))
		_, err = c.StartColumnStatisticsTaskRun(ctx, &glue.StartColumnStatisticsTaskRunInput{
			DatabaseName: aws.String(database), TableName: aws.String(table), Role: aws.String(role),
		})
		requireAWSError(t, err, "InvalidInputException", refusal(role))
		_, err = c.StartBlueprintRun(ctx, &glue.StartBlueprintRunInput{BlueprintName: aws.String(blueprint), RoleArn: aws.String(role)})
		requireAWSError(t, err, "InvalidInputException", refusal(role))
	}

	trusted := serviceRole(t, "glue.amazonaws.com")
	require.NoError(t, settings(trusted))
	t.Cleanup(func() {
		_, _ = c.DeleteColumnStatisticsTaskSettings(context.Background(), &glue.DeleteColumnStatisticsTaskSettingsInput{
			DatabaseName: aws.String(database), TableName: aws.String(table),
		})
	})
	_, err = c.UpdateColumnStatisticsTaskSettings(ctx, &glue.UpdateColumnStatisticsTaskSettingsInput{
		DatabaseName: aws.String(database), TableName: aws.String(table), Role: aws.String(otherService),
	})
	requireAWSError(t, err, "InvalidInputException", refusal(otherService))
	got, err := c.GetColumnStatisticsTaskSettings(ctx, &glue.GetColumnStatisticsTaskSettingsInput{
		DatabaseName: aws.String(database), TableName: aws.String(table),
	})
	require.NoError(t, err)
	assert.Equal(t, trusted, aws.ToString(got.ColumnStatisticsTaskSettings.Role))
	_, err = c.StartBlueprintRun(ctx, &glue.StartBlueprintRunInput{BlueprintName: aws.String(blueprint), RoleArn: aws.String(trusted)})
	require.NoError(t, err)
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
	cluster := uniquename.New("ecs-role-trust")
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCluster(context.Background(), &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})
	subnetID := createECSTestSubnet(t, "ecs-role-trust")

	register := func(taskRole, executionRole string) *string {
		input := &ecs.RegisterTaskDefinitionInput{
			Family:                  aws.String(uniquename.New("ecs-role-trust")),
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

const statesRoleRefusal = "Neither the global service principal states.amazonaws.com, or the regional one is authorized to assume the provided role."

// AWS Step Functions refuses an execution role it cannot assume, at create,
// at update and in TestState, and evaluates the trust policy's aws:SourceArn
// condition against the state machine's ARN.
func TestStepFunctions_RefusesAnExecutionRoleItCannotAssume_SDK(t *testing.T) {
	t.Parallel()
	c := sfnClient()
	otherService, absent := untrustedRoles(t)
	const definition = `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`
	create := func(name, role string) (*sfn.CreateStateMachineOutput, error) {
		return c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
			Name: aws.String(name), Definition: aws.String(definition), RoleArn: aws.String(role),
		})
	}
	for _, role := range []string{otherService, absent} {
		_, err := create(uniquename.New("sfn-untrusted"), role)
		requireAWSError(t, err, "AccessDeniedException", statesRoleRefusal)
		_, err = c.TestState(ctx, &sfn.TestStateInput{Definition: aws.String(`{"Type":"Pass","End":true}`), RoleArn: aws.String(role)})
		requireAWSError(t, err, "AccessDeniedException", statesRoleRefusal)
	}

	trusted := serviceRole(t, "states.amazonaws.com")
	created, err := create(uniquename.New("sfn-trusted"), trusted)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(context.Background(), &sfn.DeleteStateMachineInput{StateMachineArn: created.StateMachineArn})
	})
	_, err = c.UpdateStateMachine(ctx, &sfn.UpdateStateMachineInput{StateMachineArn: created.StateMachineArn, RoleArn: aws.String(otherService)})
	requireAWSError(t, err, "AccessDeniedException", statesRoleRefusal)
	described, err := c.DescribeStateMachine(ctx, &sfn.DescribeStateMachineInput{StateMachineArn: created.StateMachineArn})
	require.NoError(t, err)
	assert.Equal(t, trusted, aws.ToString(described.RoleArn))
	tested, err := c.TestState(ctx, &sfn.TestStateInput{
		Definition: aws.String(`{"Type":"Pass","End":true}`), RoleArn: aws.String(trusted), Input: aws.String(`{"ok":true}`),
	})
	require.NoError(t, err)
	assert.Equal(t, sfntypes.TestExecutionStatusSucceeded, tested.Status)

	scopedName := uniquename.New("sfn-scoped")
	iamC := iamClient()
	scoped, err := iamC.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String(uniquename.New("states-scoped")),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Principal":{"Service":"states.amazonaws.com"},"Action":"sts:AssumeRole",` +
			`"Condition":{"ArnLike":{"aws:SourceArn":"arn:aws:states:*:` + strings.Split(trusted, ":")[4] +
			`:stateMachine:` + scopedName + `"}}}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = iamC.DeleteRole(context.Background(), &iam.DeleteRoleInput{RoleName: scoped.Role.RoleName})
	})
	_, err = create(uniquename.New("sfn-unscoped"), aws.ToString(scoped.Role.Arn))
	requireAWSError(t, err, "AccessDeniedException", statesRoleRefusal)
	scopedMachine, err := create(scopedName, aws.ToString(scoped.Role.Arn))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(context.Background(), &sfn.DeleteStateMachineInput{StateMachineArn: scopedMachine.StateMachineArn})
	})
}

const schedulerRoleRefusal = "The execution role you provide must allow AWS EventBridge Scheduler to assume the role."

// EventBridge Scheduler refuses a target role it cannot assume, at create and
// at update.
func TestScheduler_RefusesATargetRoleItCannotAssume_SDK(t *testing.T) {
	t.Parallel()
	c := schedulerClient()
	otherService, absent := untrustedRoles(t)
	queue := "arn:aws:sqs:us-east-1:" + strings.Split(otherService, ":")[4] + ":" + uniquename.New("scheduler-trust")
	target := func(role string) *schedtypes.Target {
		return &schedtypes.Target{Arn: aws.String(queue), RoleArn: aws.String(role), Input: aws.String("tick")}
	}
	window := &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff}
	for _, role := range []string{otherService, absent} {
		_, err := c.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
			Name: aws.String(uniquename.New("sched-untrusted")), ScheduleExpression: aws.String("rate(1 day)"),
			FlexibleTimeWindow: window, Target: target(role),
		})
		requireAWSError(t, err, "ValidationException", schedulerRoleRefusal)
	}

	trusted := serviceRole(t, "scheduler.amazonaws.com")
	name := uniquename.New("sched-trusted")
	_, err := c.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name: aws.String(name), ScheduleExpression: aws.String("rate(1 day)"),
		FlexibleTimeWindow: window, Target: target(trusted),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteSchedule(context.Background(), &scheduler.DeleteScheduleInput{Name: aws.String(name)})
	})
	_, err = c.UpdateSchedule(ctx, &scheduler.UpdateScheduleInput{
		Name: aws.String(name), ScheduleExpression: aws.String("rate(2 days)"),
		FlexibleTimeWindow: window, Target: target(otherService),
	})
	requireAWSError(t, err, "ValidationException", schedulerRoleRefusal)
	got, err := c.GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, trusted, aws.ToString(got.Target.RoleArn))
	assert.Equal(t, "rate(1 day)", aws.ToString(got.ScheduleExpression))
}

// A trust policy may name a service by its regional principal: Step Functions
// takes a role trusting states.<region>.amazonaws.com in the Region it runs
// in, and refuses one trusting another Region's.
func TestStepFunctions_TakesARoleTrustingItsRegionalPrincipal_SDK(t *testing.T) {
	t.Parallel()
	c := sfnClient()
	const definition = `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`
	regional := serviceRole(t, "states.us-east-1.amazonaws.com")
	created, err := c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name: aws.String(uniquename.New("sfn-regional")), Definition: aws.String(definition), RoleArn: aws.String(regional),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteStateMachine(context.Background(), &sfn.DeleteStateMachineInput{StateMachineArn: created.StateMachineArn})
	})

	elsewhere := serviceRole(t, "states.eu-west-1.amazonaws.com")
	_, err = c.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
		Name: aws.String(uniquename.New("sfn-elsewhere")), Definition: aws.String(definition), RoleArn: aws.String(elsewhere),
	})
	requireAWSError(t, err, "AccessDeniedException", statesRoleRefusal)
}
