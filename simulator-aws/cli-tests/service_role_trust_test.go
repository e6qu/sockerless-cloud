package aws_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// cliUntrustedRoles returns the ARN of a role whose trust policy names another
// service, and an ARN in the same account that names no role.
func cliUntrustedRoles(t *testing.T) (otherService, absent string) {
	t.Helper()
	otherService = cliServiceRole(t, "ec2.amazonaws.com")
	absent = otherService[:strings.LastIndex(otherService, "/")+1] + cliUniqueName("absent")
	return otherService, absent
}

func assertCLIRefusal(t *testing.T, out, code, message string) {
	t.Helper()
	assert.Contains(t, out, "("+code+")")
	assert.Contains(t, out, message)
}

func TestLambda_RefusesAnExecutionRoleItCannotAssume_CLI(t *testing.T) {
	t.Parallel()
	zipPath := createDummyZip(t)
	otherService, absent := cliUntrustedRoles(t)
	for _, role := range []string{otherService, absent} {
		out := runCLIExpectError(t, awsCLI("lambda", "create-function",
			"--function-name", cliUniqueName("cli-fn-untrusted"), "--runtime", "nodejs20.x",
			"--role", role, "--handler", "index.handler", "--zip-file", "fileb://"+zipPath))
		assertCLIRefusal(t, out, "InvalidParameterValueException", "The role defined for the function cannot be assumed by Lambda.")
	}

	name := cliUniqueName("cli-fn-trusted")
	runCLI(t, awsCLI("lambda", "create-function", "--function-name", name, "--runtime", "nodejs20.x",
		"--role", cliServiceRole(t, "lambda.amazonaws.com"), "--handler", "index.handler", "--zip-file", "fileb://"+zipPath))
	t.Cleanup(func() { _ = awsCLI("lambda", "delete-function", "--function-name", name).Run() })
	out := runCLIExpectError(t, awsCLI("lambda", "update-function-configuration", "--function-name", name, "--role", otherService))
	assertCLIRefusal(t, out, "InvalidParameterValueException", "The role defined for the function cannot be assumed by Lambda.")
}

func TestCodeBuild_RefusesAServiceRoleItCannotAssume_CLI(t *testing.T) {
	t.Parallel()
	otherService, absent := cliUntrustedRoles(t)
	create := func(name, role string) []string {
		return []string{"codebuild", "create-project", "--name", name,
			"--source", `{"type":"NO_SOURCE","buildspec":"version: 0.2"}`,
			"--artifacts", `{"type":"NO_ARTIFACTS"}`,
			"--environment", `{"type":"LINUX_CONTAINER","image":"aws/codebuild/standard:7.0","computeType":"BUILD_GENERAL1_SMALL"}`,
			"--service-role", role}
	}
	for _, role := range []string{otherService, absent} {
		out := runCLIExpectError(t, awsCLI(create(cliUniqueName("cli-cb-untrusted"), role)...))
		assertCLIRefusal(t, out, "InvalidInputException", "CodeBuild is not authorized to perform: sts:AssumeRole on "+role)
	}

	name := cliUniqueName("cli-cb-trusted")
	runCLI(t, awsCLI(create(name, cliServiceRole(t, "codebuild.amazonaws.com"))...))
	t.Cleanup(func() { _ = awsCLI("codebuild", "delete-project", "--name", name).Run() })
	out := runCLIExpectError(t, awsCLI("codebuild", "update-project", "--name", name, "--service-role", otherService))
	assertCLIRefusal(t, out, "InvalidInputException", "CodeBuild is not authorized to perform: sts:AssumeRole on "+otherService)
}

func TestBatch_RefusesARoleECSTasksCannotAssume_CLI(t *testing.T) {
	t.Parallel()
	otherService, absent := cliUntrustedRoles(t)
	register := func(roles string) []string {
		return []string{"batch", "register-job-definition", "--job-definition-name", cliUniqueName("cli-batch-role-trust"),
			"--type", "container",
			"--container-properties", `{"image":"mirror.gcr.io/library/alpine:3","command":["true"]` + roles + `}`}
	}
	out := runCLIExpectError(t, awsCLI(register(`,"jobRoleArn":"`+otherService+`"`)...))
	assertCLIRefusal(t, out, "ClientException", "jobRoleArn "+otherService+" cannot be assumed by ecs-tasks.amazonaws.com")
	out = runCLIExpectError(t, awsCLI(register(`,"executionRoleArn":"`+absent+`"`)...))
	assertCLIRefusal(t, out, "ClientException", "executionRoleArn "+absent+" cannot be assumed by ecs-tasks.amazonaws.com")
	runCLI(t, awsCLI(register(`,"jobRoleArn":"`+cliServiceRole(t, "ecs-tasks.amazonaws.com")+`"`)...))
}

func TestGlue_RefusesAJobRoleItCannotAssume_CLI(t *testing.T) {
	t.Parallel()
	otherService, absent := cliUntrustedRoles(t)
	command := `{"Name":"glueetl","ScriptLocation":"s3://bucket/script.py"}`
	for _, role := range []string{otherService, absent} {
		out := runCLIExpectError(t, awsCLI("glue", "create-job", "--name", cliUniqueName("cli-glue-untrusted"),
			"--role", role, "--command", command))
		assertCLIRefusal(t, out, "InvalidInputException", "Role "+role+" should be given assume role permissions for Glue Service.")
	}

	name := cliUniqueName("cli-glue-trusted")
	runCLI(t, awsCLI("glue", "create-job", "--name", name, "--role", cliServiceRole(t, "glue.amazonaws.com"), "--command", command))
	t.Cleanup(func() { _ = awsCLI("glue", "delete-job", "--job-name", name).Run() })
	out := runCLIExpectError(t, awsCLI("glue", "update-job", "--job-name", name,
		"--job-update", `{"Role":"`+otherService+`","Command":`+command+`}`))
	assertCLIRefusal(t, out, "InvalidInputException", "Role "+otherService+" should be given assume role permissions for Glue Service.")
}

func TestAmplify_RefusesARoleItCannotAssume_CLI(t *testing.T) {
	t.Parallel()
	otherService, absent := cliUntrustedRoles(t)
	out := runCLIExpectError(t, awsCLI("amplify", "create-app", "--name", cliUniqueName("cli-amp-untrusted"),
		"--compute-role-arn", otherService))
	assertCLIRefusal(t, out, "BadRequestException", "computeRoleArn "+otherService+" cannot be assumed by amplify.amazonaws.com")
	out = runCLIExpectError(t, awsCLI("amplify", "create-app", "--name", cliUniqueName("cli-amp-untrusted"),
		"--iam-service-role-arn", absent))
	assertCLIRefusal(t, out, "BadRequestException", "iamServiceRoleArn "+absent+" cannot be assumed by amplify.amazonaws.com")

	var created struct {
		App struct {
			AppID string `json:"appId"`
		} `json:"app"`
	}
	parseJSON(t, runCLI(t, awsCLI("amplify", "create-app", "--name", cliUniqueName("cli-amp-trusted"),
		"--compute-role-arn", cliServiceRole(t, "amplify.amazonaws.com"), "--output", "json")), &created)
	t.Cleanup(func() { _ = awsCLI("amplify", "delete-app", "--app-id", created.App.AppID).Run() })
	out = runCLIExpectError(t, awsCLI("amplify", "update-app", "--app-id", created.App.AppID, "--compute-role-arn", otherService))
	assertCLIRefusal(t, out, "BadRequestException", "computeRoleArn "+otherService+" cannot be assumed by amplify.amazonaws.com")
	out = runCLIExpectError(t, awsCLI("amplify", "create-branch", "--app-id", created.App.AppID, "--branch-name", "main",
		"--compute-role-arn", absent))
	assertCLIRefusal(t, out, "BadRequestException", "computeRoleArn "+absent+" cannot be assumed by amplify.amazonaws.com")
}

func TestBatch_ComputeEnvironmentWithAServiceRoleItCannotAssumeIsInvalid_CLI(t *testing.T) {
	t.Parallel()
	otherService, _ := cliUntrustedRoles(t)
	name := cliUniqueName("cli-batch-ce-untrusted")
	runCLI(t, awsCLI("batch", "create-compute-environment", "--compute-environment-name", name,
		"--type", "UNMANAGED", "--service-role", otherService))
	t.Cleanup(func() { _ = awsCLI("batch", "delete-compute-environment", "--compute-environment", name).Run() })
	status := func() string {
		return strings.TrimSpace(runCLI(t, awsCLI("batch", "describe-compute-environments", "--compute-environments", name,
			"--query", "computeEnvironments[0].[status,statusReason]", "--output", "text")))
	}
	assert.True(t, strings.HasPrefix(status(), "INVALID\tCLIENT_ERROR - Not authorized to perform sts:AssumeRole (Service: AWSSecurityTokenService;"), status())

	runCLI(t, awsCLI("batch", "update-compute-environment", "--compute-environment", name,
		"--service-role", cliServiceRole(t, "batch.amazonaws.com")))
	assert.Equal(t, "VALID\tNone", status())
}

func TestCodeBuild_RefusesAServiceRoleOverrideItCannotAssume_CLI(t *testing.T) {
	t.Parallel()
	otherService, absent := cliUntrustedRoles(t)
	name := cliUniqueName("cli-cb-override")
	runCLI(t, awsCLI("codebuild", "create-project", "--name", name,
		"--source", `{"type":"NO_SOURCE","buildspec":"version: 0.2"}`,
		"--artifacts", `{"type":"NO_ARTIFACTS"}`,
		"--environment", `{"type":"LINUX_CONTAINER","image":"aws/codebuild/standard:7.0","computeType":"BUILD_GENERAL1_SMALL"}`,
		"--service-role", cliServiceRole(t, "codebuild.amazonaws.com")))
	t.Cleanup(func() { _ = awsCLI("codebuild", "delete-project", "--name", name).Run() })
	for _, role := range []string{otherService, absent} {
		out := runCLIExpectError(t, awsCLI("codebuild", "start-build", "--project-name", name, "--service-role-override", role))
		assertCLIRefusal(t, out, "InvalidInputException", "CodeBuild is not authorized to perform: sts:AssumeRole on "+role)
	}
}

func TestGlue_RefusesARoleItCannotAssumeBeyondJobs_CLI(t *testing.T) {
	t.Parallel()
	otherService, absent := cliUntrustedRoles(t)
	const crawlerRefusal = "Service is unable to assume provided role. Please verify role's TrustPolicy"
	jobRefusal := func(role string) string {
		return "Role " + role + " should be given assume role permissions for Glue Service."
	}
	targets := `{"S3Targets":[{"Path":"s3://bucket/prefix"}]}`
	transform := func(role string) []string {
		return []string{"glue", "create-ml-transform", "--name", cliUniqueName("cli-glue-mlt"),
			"--input-record-tables", `[{"DatabaseName":"db","TableName":"tbl"}]`,
			"--parameters", `{"TransformType":"FIND_MATCHES"}`, "--role", role}
	}
	for _, role := range []string{otherService, absent} {
		out := runCLIExpectError(t, awsCLI("glue", "create-crawler", "--name", cliUniqueName("cli-glue-crawler"),
			"--role", role, "--targets", targets))
		assertCLIRefusal(t, out, "InvalidInputException", crawlerRefusal)
		out = runCLIExpectError(t, awsCLI("glue", "create-session", "--id", cliUniqueName("cli-glue-session"),
			"--role", role, "--command", `{"Name":"glueetl"}`))
		assertCLIRefusal(t, out, "InvalidInputException", jobRefusal(role))
		out = runCLIExpectError(t, awsCLI(transform(role)...))
		assertCLIRefusal(t, out, "InvalidInputException", "Unable to assume role "+role)
		out = runCLIExpectError(t, awsCLI("glue", "create-dev-endpoint", "--endpoint-name", cliUniqueName("cli-glue-dev"),
			"--role-arn", role))
		assertCLIRefusal(t, out, "InvalidInputException", jobRefusal(role))
	}

	trusted := cliServiceRole(t, "glue.amazonaws.com")
	crawler := cliUniqueName("cli-glue-crawler")
	runCLI(t, awsCLI("glue", "create-crawler", "--name", crawler, "--role", trusted, "--targets", targets))
	t.Cleanup(func() { _ = awsCLI("glue", "delete-crawler", "--name", crawler).Run() })
	out := runCLIExpectError(t, awsCLI("glue", "update-crawler", "--name", crawler, "--role", otherService))
	assertCLIRefusal(t, out, "InvalidInputException", crawlerRefusal)

	id := strings.TrimSpace(runCLI(t, awsCLI(append(transform(trusted), "--query", "TransformId", "--output", "text")...)))
	t.Cleanup(func() { _ = awsCLI("glue", "delete-ml-transform", "--transform-id", id).Run() })
	out = runCLIExpectError(t, awsCLI("glue", "update-ml-transform", "--transform-id", id, "--role", absent))
	assertCLIRefusal(t, out, "InvalidInputException", "Unable to assume role "+absent)
}

func TestECS_RefusesToLaunchATaskWithARoleItCannotAssume_CLI(t *testing.T) {
	t.Parallel()
	otherService, absent := cliUntrustedRoles(t)
	cluster := cliUniqueName("cli-ecs-role-trust")
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	t.Cleanup(func() { _ = awsCLI("ecs", "delete-cluster", "--cluster", cluster).Run() })
	subnetID := createCLIECSTestSubnet(t, cluster, "10.163.0.0/16")

	register := func(roles ...string) string {
		args := []string{"ecs", "register-task-definition", "--family", cliUniqueName("cli-ecs-role-trust"),
			"--requires-compatibilities", "FARGATE", "--network-mode", "awsvpc", "--cpu", "256", "--memory", "512",
			"--container-definitions", `[{"name":"app","image":"mirror.gcr.io/library/alpine:3.21","command":["true"],"stopTimeout":2}]`,
			"--query", "taskDefinition.taskDefinitionArn", "--output", "text"}
		return strings.TrimSpace(runCLI(t, awsCLI(append(args, roles...)...)))
	}
	run := func(taskDefinition string, extra ...string) []string {
		return append([]string{"ecs", "run-task", "--cluster", cluster, "--task-definition", taskDefinition,
			"--launch-type", "FARGATE", "--network-configuration", "awsvpcConfiguration={subnets=[" + subnetID + "]}"}, extra...)
	}
	refusal := func(role string) string {
		return "ECS was unable to assume the role '" + role + "' that was provided for this task."
	}

	out := runCLIExpectError(t, awsCLI(run(register("--task-role-arn", otherService))...))
	assertCLIRefusal(t, out, "ClientException", refusal(otherService))
	out = runCLIExpectError(t, awsCLI(run(register("--execution-role-arn", absent))...))
	assertCLIRefusal(t, out, "ClientException", refusal(absent))

	trusted := register("--task-role-arn", cliServiceRole(t, "ecs-tasks.amazonaws.com"))
	out = runCLIExpectError(t, awsCLI(run(trusted, "--overrides", `{"taskRoleArn":"`+otherService+`"}`)...))
	assertCLIRefusal(t, out, "ClientException", refusal(otherService))

	taskArn := strings.TrimSpace(runCLI(t, awsCLI(run(trusted, "--query", "tasks[0].taskArn", "--output", "text")...)))
	cleanupCLIECSTask(t, cluster, taskArn)
	waitECSTaskStoppedCLI(t, cluster, taskArn)
}
