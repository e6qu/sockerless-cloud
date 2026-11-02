package sts_session_limits_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests/internal/tfsim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSTSSessionLimitsTerraform creates a role with a permissions boundary
// and a maximum session duration through aws_iam_role, then has the provider
// work as a session of it through its assume_role block. The boundary caps
// the session to Amazon S3 although the role's own policy also allows Amazon
// SQS, a session policy narrows it further, and a duration past the role's
// maximum is refused.
func TestSTSSessionLimitsTerraform(t *testing.T) {
	env := tfsim.Start(t, ".")
	env.Terraform(t, "init")
	env.Terraform(t, "apply", "-auto-approve")
	var role map[string]struct {
		Value json.RawMessage `json:"value"`
	}
	require.NoError(t, json.Unmarshal(env.Terraform(t, "output", "-json"), &role))
	var roleArn string
	require.NoError(t, json.Unmarshal(role["role_arn"].Value, &roleArn))
	assert.JSONEq(t, "7200", string(role["max_session_duration"].Value))

	session := env.In("session")
	session.Terraform(t, "init")
	vars := func(extra ...string) []string {
		return append([]string{"-auto-approve", "-var", "role_arn=" + roleArn}, extra...)
	}

	tooLong := string(session.TerraformFails(t, append([]string{"apply"}, vars("-var", "duration=3h")...)...))
	assert.Contains(t, tooLong, "ValidationError")
	assert.Contains(t, tooLong, "MaxSessionDuration", "a session longer than the role's maximum is refused")

	narrowed := string(session.TerraformFails(t, append([]string{"apply"}, vars("-var", "duration=2h",
		"-var", `session_policy={"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"}]}`)...)...))
	assert.Contains(t, narrowed, "CreateBucket")
	assert.Contains(t, narrowed, "AccessDenied", "a session policy that allows only Amazon SQS refuses the bucket")

	capped := string(session.TerraformFails(t, append([]string{"apply"}, vars("-var", "duration=2h", "-var", "create_queue=true")...)...))
	assert.Contains(t, capped, "AccessDenied", "the boundary refuses the queue the role's policy allows")
	assert.Contains(t, strings.ToLower(capped), "sqs")

	session.Terraform(t, append([]string{"apply"}, vars("-var", "duration=2h")...)...)
	var outputs map[string]struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(session.Terraform(t, "output", "-json"), &outputs))
	assert.True(t, strings.HasSuffix(outputs["bucket_arn"].Value, ":::tf-session-limits"), outputs["bucket_arn"].Value)

	session.Terraform(t, append([]string{"destroy"}, vars("-var", "duration=2h")...)...)
	env.Terraform(t, "destroy", "-auto-approve")
}
