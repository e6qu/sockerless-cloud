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
		out = runCLIExpectError(t, awsCLI("codebuild", "start-build-batch", "--project-name", name, "--service-role-override", role))
		assertCLIRefusal(t, out, "InvalidInputException", "CodeBuild is not authorized to perform: sts:AssumeRole on "+role)
	}
}

func TestCodeBuild_RefusesAFleetServiceRoleItCannotAssume_CLI(t *testing.T) {
	t.Parallel()
	otherService, absent := cliUntrustedRoles(t)
	create := func(role string) []string {
		return []string{"codebuild", "create-fleet", "--name", cliUniqueName("cli-cb-fleet-role"), "--base-capacity", "1",
			"--compute-type", "BUILD_GENERAL1_SMALL", "--environment-type", "LINUX_CONTAINER", "--fleet-service-role", role}
	}
	for _, role := range []string{otherService, absent} {
		out := runCLIExpectError(t, awsCLI(create(role)...))
		assertCLIRefusal(t, out, "InvalidInputException", "CodeBuild is not authorized to perform: sts:AssumeRole on "+role)
	}

	trusted := cliServiceRole(t, "codebuild.amazonaws.com")
	arn := strings.TrimSpace(runCLI(t, awsCLI(append(create(trusted), "--query", "fleet.arn", "--output", "text")...)))
	t.Cleanup(func() { _ = awsCLI("codebuild", "delete-fleet", "--arn", arn).Run() })
	out := runCLIExpectError(t, awsCLI("codebuild", "update-fleet", "--arn", arn, "--fleet-service-role", otherService))
	assertCLIRefusal(t, out, "InvalidInputException", "CodeBuild is not authorized to perform: sts:AssumeRole on "+otherService)
	assert.Equal(t, trusted, strings.TrimSpace(runCLI(t, awsCLI("codebuild", "batch-get-fleets", "--names", arn,
		"--query", "fleets[0].fleetServiceRole", "--output", "text"))))
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

func TestGlue_RefusesARunRoleItCannotAssume_CLI(t *testing.T) {
	t.Parallel()
	otherService, absent := cliUntrustedRoles(t)
	refusal := func(role string) string {
		return "Role " + role + " should be given assume role permissions for Glue Service."
	}
	database, table, blueprint := cliUniqueName("cli-glue-run-role"), "events", cliUniqueName("cli-glue-run-role")
	runCLI(t, awsCLI("glue", "create-database", "--database-input", `{"Name":"`+database+`"}`))
	t.Cleanup(func() { _ = awsCLI("glue", "delete-database", "--name", database).Run() })
	runCLI(t, awsCLI("glue", "create-table", "--database-name", database, "--table-input", `{"Name":"`+table+`"}`))
	t.Cleanup(func() { _ = awsCLI("glue", "delete-table", "--database-name", database, "--name", table).Run() })
	runCLI(t, awsCLI("glue", "create-blueprint", "--name", blueprint, "--blueprint-location", "s3://bucket/blueprint.zip"))
	t.Cleanup(func() { _ = awsCLI("glue", "delete-blueprint", "--name", blueprint).Run() })

	source := `{"GlueTable":{"DatabaseName":"` + database + `","TableName":"` + table + `"}}`
	tableArgs := []string{"--database-name", database, "--table-name", table}
	for _, role := range []string{otherService, absent} {
		out := runCLIExpectError(t, awsCLI("glue", "start-data-quality-ruleset-evaluation-run", "--data-source", source,
			"--role", role, "--ruleset-names", cliUniqueName("cli-glue-ruleset")))
		assertCLIRefusal(t, out, "InvalidInputException", refusal(role))
		out = runCLIExpectError(t, awsCLI("glue", "start-data-quality-rule-recommendation-run", "--data-source", source, "--role", role))
		assertCLIRefusal(t, out, "InvalidInputException", refusal(role))
		out = runCLIExpectError(t, awsCLI(append([]string{"glue", "create-column-statistics-task-settings", "--role", role}, tableArgs...)...))
		assertCLIRefusal(t, out, "InvalidInputException", refusal(role))
		out = runCLIExpectError(t, awsCLI(append([]string{"glue", "start-column-statistics-task-run", "--role", role}, tableArgs...)...))
		assertCLIRefusal(t, out, "InvalidInputException", refusal(role))
		out = runCLIExpectError(t, awsCLI("glue", "start-blueprint-run", "--blueprint-name", blueprint, "--role-arn", role))
		assertCLIRefusal(t, out, "InvalidInputException", refusal(role))
	}

	trusted := cliServiceRole(t, "glue.amazonaws.com")
	runCLI(t, awsCLI(append([]string{"glue", "create-column-statistics-task-settings", "--role", trusted}, tableArgs...)...))
	t.Cleanup(func() {
		_ = awsCLI(append([]string{"glue", "delete-column-statistics-task-settings"}, tableArgs...)...).Run()
	})
	out := runCLIExpectError(t, awsCLI(append([]string{"glue", "update-column-statistics-task-settings", "--role", otherService}, tableArgs...)...))
	assertCLIRefusal(t, out, "InvalidInputException", refusal(otherService))
	assert.Equal(t, trusted, strings.TrimSpace(runCLI(t, awsCLI(append([]string{"glue", "get-column-statistics-task-settings",
		"--query", "ColumnStatisticsTaskSettings.Role", "--output", "text"}, tableArgs...)...))))
	runCLI(t, awsCLI("glue", "start-blueprint-run", "--blueprint-name", blueprint, "--role-arn", trusted))
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

func TestSFN_RefusesAnExecutionRoleItCannotAssume_CLI(t *testing.T) {
	t.Parallel()
	otherService, absent := cliUntrustedRoles(t)
	const refusal = "Neither the global service principal states.amazonaws.com, or the regional one is authorized to assume the provided role."
	const definition = `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`
	create := func(name, role string) []string {
		return []string{"stepfunctions", "create-state-machine", "--name", name, "--definition", definition,
			"--role-arn", role, "--query", "stateMachineArn", "--output", "text"}
	}
	syncEndpoint := sfnSyncEndpoint(t)
	for _, role := range []string{otherService, absent} {
		out := runCLIExpectError(t, awsCLI(create(cliUniqueName("cli-sfn-untrusted"), role)...))
		assertCLIRefusal(t, out, "AccessDeniedException", refusal)
		out = runCLIExpectError(t, awsCLIHostPrefixed("stepfunctions", "test-state",
			"--definition", `{"Type":"Pass","End":true}`, "--role-arn", role, "--endpoint-url", syncEndpoint))
		assertCLIRefusal(t, out, "AccessDeniedException", refusal)
	}

	machine := strings.TrimSpace(runCLI(t, awsCLI(create(cliUniqueName("cli-sfn-trusted"), cliServiceRole(t, "states.amazonaws.com"))...)))
	t.Cleanup(func() { _ = awsCLI("stepfunctions", "delete-state-machine", "--state-machine-arn", machine).Run() })
	out := runCLIExpectError(t, awsCLI("stepfunctions", "update-state-machine", "--state-machine-arn", machine, "--role-arn", otherService))
	assertCLIRefusal(t, out, "AccessDeniedException", refusal)
}

func TestScheduler_RefusesATargetRoleItCannotAssume_CLI(t *testing.T) {
	t.Parallel()
	otherService, absent := cliUntrustedRoles(t)
	const refusal = "The execution role you provide must allow AWS EventBridge Scheduler to assume the role."
	queue := "arn:aws:sqs:us-east-1:" + strings.Split(otherService, ":")[4] + ":" + cliUniqueName("cli-sched-trust")
	schedule := func(operation, name, expression, role string) []string {
		return []string{"scheduler", operation, "--name", name, "--schedule-expression", expression,
			"--flexible-time-window", `{"Mode":"OFF"}`, "--target", `{"Arn":"` + queue + `","RoleArn":"` + role + `","Input":"tick"}`}
	}
	for _, role := range []string{otherService, absent} {
		out := runCLIExpectError(t, awsCLI(schedule("create-schedule", cliUniqueName("cli-sched-untrusted"), "rate(1 day)", role)...))
		assertCLIRefusal(t, out, "ValidationException", refusal)
	}

	name := cliUniqueName("cli-sched-trusted")
	runCLI(t, awsCLI(schedule("create-schedule", name, "rate(1 day)", cliServiceRole(t, "scheduler.amazonaws.com"))...))
	t.Cleanup(func() { _ = awsCLI("scheduler", "delete-schedule", "--name", name).Run() })
	out := runCLIExpectError(t, awsCLI(schedule("update-schedule", name, "rate(2 days)", otherService)...))
	assertCLIRefusal(t, out, "ValidationException", refusal)
}
