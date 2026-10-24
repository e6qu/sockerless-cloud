package aws_cli_test

import (
	"strings"
	"testing"
)

// TestIAM_RESTRoutesAreGatedCLI proves the call-time gate on a REST service's
// routes: a restricted user's key reaches Amazon EventBridge Scheduler's
// GetSchedule only once its policy grants scheduler:GetSchedule on the
// schedule, and is refused AccessDeniedException until then.
func TestIAM_RESTRoutesAreGatedCLI(t *testing.T) {
	t.Parallel()
	const schedule = "cli-rest-gate"
	roleArn := createCLIECSRole(t, awsCLI, "cli-rest-gate-target", "scheduler.amazonaws.com", "sqs:SendMessage")
	runCLI(t, awsCLI("scheduler", "create-schedule", "--name", schedule,
		"--schedule-expression", "rate(1 hour)",
		"--flexible-time-window", "Mode=OFF",
		"--target", `{"Arn":"arn:aws:sqs:us-east-1:123456789012:cli-rest-gate","RoleArn":"`+roleArn+`"}`))
	t.Cleanup(func() { _ = awsCLI("scheduler", "delete-schedule", "--name", schedule).Run() })

	user := "cli-rest-gate-user"
	runCLI(t, awsCLI("iam", "create-user", "--user-name", user))
	t.Cleanup(func() { _ = awsCLI("iam", "delete-user", "--user-name", user).Run() })
	runCLI(t, awsCLI("iam", "put-user-policy", "--user-name", user, "--policy-name", "list-only",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"scheduler:ListSchedules","Resource":"*"}]}`))
	var key struct {
		AccessKey struct {
			AccessKeyId     string `json:"AccessKeyId"`
			SecretAccessKey string `json:"SecretAccessKey"`
		} `json:"AccessKey"`
	}
	parseJSON(t, runCLI(t, awsCLI("iam", "create-access-key", "--user-name", user, "--output", "json")), &key)
	t.Cleanup(func() {
		_ = awsCLI("iam", "delete-access-key", "--user-name", user, "--access-key-id", key.AccessKey.AccessKeyId).Run()
	})

	listed := awsCLI("scheduler", "list-schedules")
	listed.Env = withCreds(listed.Env, key.AccessKey.AccessKeyId, key.AccessKey.SecretAccessKey)
	runCLI(t, listed)

	denied := awsCLI("scheduler", "get-schedule", "--name", schedule)
	denied.Env = withCreds(denied.Env, key.AccessKey.AccessKeyId, key.AccessKey.SecretAccessKey)
	if out := runCLIExpectError(t, denied); !strings.Contains(out, "AccessDeniedException") {
		t.Fatalf("get-schedule without scheduler:GetSchedule expected AccessDeniedException; got: %s", out)
	}

	runCLI(t, awsCLI("iam", "put-user-policy", "--user-name", user, "--policy-name", "get-schedule",
		"--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"scheduler:GetSchedule","Resource":"arn:aws:scheduler:us-east-1:*:schedule/default/`+schedule+`"}]}`))
	allowed := awsCLI("scheduler", "get-schedule", "--name", schedule, "--query", "Name", "--output", "text")
	allowed.Env = withCreds(allowed.Env, key.AccessKey.AccessKeyId, key.AccessKey.SecretAccessKey)
	if got := strings.TrimSpace(runCLI(t, allowed)); got != schedule {
		t.Fatalf("get-schedule with the grant returned %q", got)
	}
}
