package aws_cli_test

import (
	"os/exec"
	"strings"
	"testing"
)

// scopedKeyCLI mints an IAM user carrying policyDocument and returns its access
// key pair, so a CLI call signed with it is evaluated as a real principal.
func scopedKeyCLI(t *testing.T, user, policyName, policyDocument string) (id, secret string) {
	t.Helper()
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() { _ = awsCLI("iam", "delete-user", "--user-name", user).Run() })
	runCLI(t, awsCLI("iam", "put-user-policy", "--user-name", user,
		"--policy-name", policyName, "--policy-document", policyDocument))
	out := runCLI(t, awsCLI("iam", "create-access-key", "--user-name", user, "--output", "json"))
	var ck struct {
		AccessKey struct {
			AccessKeyId     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
		} `json:"AccessKey"`
	}
	parseJSON(t, out, &ck)
	if ck.AccessKey.AccessKeyId == "" {
		t.Fatalf("create-access-key returned no key id: %s", out)
	}
	t.Cleanup(func() {
		_ = awsCLI("iam", "delete-access-key", "--user-name", user,
			"--access-key-id", ck.AccessKey.AccessKeyId).Run()
	})
	return ck.AccessKey.AccessKeyId, ck.AccessKey.SecretAccessKey
}

func asUser(cmd *exec.Cmd, id, secret string) *exec.Cmd {
	cmd.Env = withCreds(cmd.Env, id, secret)
	return cmd
}

// A CloudWatch Logs grant written the way an operator writes it — the group ARN
// and the ":*" form covering its streams — allows a group-scoped read and a
// stream write under that group, and denies another group. This is what proves
// the gate requests the group ARN without a trailing wildcard and the stream
// ARN for the four stream-scoped actions.
func TestIAM_CloudWatchLogsResourceScopedCLI(t *testing.T) {
	for _, group := range []string{"/cli-scoped/app", "/cli-other/app"} {
		runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
		group := group
		t.Cleanup(func() { _ = awsCLI("logs", "delete-log-group", "--log-group-name", group).Run() })
		runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", "s1"))
	}

	const arn = "arn:aws:logs:us-east-1:123456789012:log-group:/cli-scoped/app"
	id, secret := scopedKeyCLI(t, "cli-logs-scoped-user", "one-group",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",`+
			`"Action":["logs:FilterLogEvents","logs:DescribeLogStreams","logs:PutLogEvents"],`+
			`"Resource":["`+arn+`","`+arn+`:*"]}]}`)

	runCLI(t, asUser(awsCLI("logs", "filter-log-events",
		"--log-group-name", "/cli-scoped/app"), id, secret))
	runCLI(t, asUser(awsCLI("logs", "describe-log-streams",
		"--log-group-name", "/cli-scoped/app"), id, secret))
	runCLI(t, asUser(awsCLI("logs", "put-log-events",
		"--log-group-name", "/cli-scoped/app", "--log-stream-name", "s1",
		"--log-events", `[{"timestamp":1770000000000,"message":"scoped"}]`), id, secret))

	deny := runCLIExpectError(t, asUser(awsCLI("logs", "filter-log-events",
		"--log-group-name", "/cli-other/app"), id, secret))
	if !strings.Contains(deny, "AccessDeniedException") {
		t.Fatalf("filter-log-events on an ungranted group expected AccessDeniedException; got: %s", deny)
	}
}

// `aws codebuild start-build` under a project-scoped grant is the call that
// follows an Amazon ECR read in an image-source build loop.
func TestIAM_CodeBuildResourceScopedCLI(t *testing.T) {
	for _, project := range []string{"cli-scoped-build", "cli-other-build"} {
		runCLI(t, awsCLI("codebuild", "create-project",
			"--name", project,
			"--source", `{"type":"NO_SOURCE"}`,
			"--artifacts", `{"type":"NO_ARTIFACTS"}`,
			"--environment", `{"type":"LINUX_CONTAINER","image":"public.ecr.aws/docker/library/alpine:3.21","computeType":"BUILD_GENERAL1_SMALL"}`,
			"--service-role", "arn:aws:iam::123456789012:role/cli-build-role"))
		project := project
		t.Cleanup(func() { _ = awsCLI("codebuild", "delete-project", "--name", project).Run() })
	}

	id, secret := scopedKeyCLI(t, "cli-codebuild-scoped-user", "one-project",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"codebuild:BatchGetProjects",`+
			`"Resource":"arn:aws:codebuild:us-east-1:123456789012:project/cli-scoped-build"}]}`)

	runCLI(t, asUser(awsCLI("codebuild", "batch-get-projects", "--names", "cli-scoped-build"), id, secret))

	deny := runCLIExpectError(t, asUser(
		awsCLI("codebuild", "batch-get-projects", "--names", "cli-other-build"), id, secret))
	if !strings.Contains(deny, "AccessDeniedException") {
		t.Fatalf("batch-get-projects on an ungranted project expected AccessDeniedException; got: %s", deny)
	}
}

// A caller allowed to register a task definition but allowed to pass only one
// role is denied when it passes another, because iam:PassRole is authorized
// against the role the request hands to the service.
func TestIAM_PassRoleResourceScopedCLI(t *testing.T) {
	const trust = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
		`"Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	for _, role := range []string{"cli-task-allowed", "cli-task-forbidden"} {
		runCLI(t, awsCLI("iam", "create-role", "--role-name", role,
			"--assume-role-policy-document", trust))
		role := role
		t.Cleanup(func() { _ = awsCLI("iam", "delete-role", "--role-name", role).Run() })
	}

	id, secret := scopedKeyCLI(t, "cli-passrole-user", "register-and-pass-one-role",
		`{"Version":"2012-10-17","Statement":[`+
			`{"Effect":"Allow","Action":"ecs:RegisterTaskDefinition","Resource":"*"},`+
			`{"Effect":"Allow","Action":"iam:PassRole",`+
			`"Resource":"arn:aws:iam::123456789012:role/cli-task-allowed"}]}`)

	register := func(role string) *exec.Cmd {
		return asUser(awsCLI("ecs", "register-task-definition",
			"--family", "cli-passrole-family",
			"--task-role-arn", "arn:aws:iam::123456789012:role/"+role,
			"--container-definitions",
			`[{"name":"app","image":"public.ecr.aws/docker/library/alpine:3.21"}]`), id, secret)
	}

	runCLI(t, register("cli-task-allowed"))

	deny := runCLIExpectError(t, register("cli-task-forbidden"))
	if !strings.Contains(deny, "AccessDeniedException") {
		t.Fatalf("register-task-definition passing an ungranted role expected AccessDeniedException; got: %s", deny)
	}
}

// `aws logs get-log-events` without --start-from-head reads the end of the
// stream. A reader who takes the default and sees the beginning of history
// concludes the workload is silent, which is why the default page is bounded
// and anchored at the tail rather than returning everything from the oldest.
func TestCloudWatchLogsGetLogEventsReadsTheTailByDefaultCLI(t *testing.T) {
	const group, stream = "/cli-tail/app", "s1"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	t.Cleanup(func() { _ = awsCLI("logs", "delete-log-group", "--log-group-name", group).Run() })
	runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", stream))

	var events strings.Builder
	events.WriteByte('[')
	for i := range 40 {
		if i > 0 {
			events.WriteByte(',')
		}
		events.WriteString(`{"timestamp":`)
		events.WriteString(itoa(1770000000000 + int64(i)*1000))
		events.WriteString(`,"message":"line-`)
		events.WriteString(itoa(int64(i)))
		events.WriteString(`"}`)
	}
	events.WriteByte(']')
	runCLI(t, awsCLI("logs", "put-log-events",
		"--log-group-name", group, "--log-stream-name", stream,
		"--log-events", events.String()))

	var page struct {
		Events []struct {
			Message string `json:"message"`
		} `json:"events"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "get-log-events",
		"--log-group-name", group, "--log-stream-name", stream,
		"--limit", "5", "--output", "json")), &page)
	if len(page.Events) != 5 {
		t.Fatalf("get-log-events --limit 5 returned %d events", len(page.Events))
	}
	if last := page.Events[4].Message; last != "line-39" {
		t.Fatalf("the default read ended at %q, want the stream's newest line-39 — "+
			"without --start-from-head the page is the tail", last)
	}

	parseJSON(t, runCLI(t, awsCLI("logs", "get-log-events",
		"--log-group-name", group, "--log-stream-name", stream,
		"--limit", "5", "--start-from-head", "--output", "json")), &page)
	if first := page.Events[0].Message; first != "line-0" {
		t.Fatalf("--start-from-head began at %q, want line-0", first)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
