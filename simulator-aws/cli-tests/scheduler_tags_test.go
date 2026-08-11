package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScheduler_ScheduleGroupTags_CLI exercises tag CRUD on a schedule group
// through the aws CLI: tag, list, untag, list again.
func TestScheduler_ScheduleGroupTags_CLI(t *testing.T) {
	const group = "cli-tagged-group"
	out := runCLI(t, awsCLI("scheduler", "create-schedule-group",
		"--name", group,
		"--output", "json",
	))
	var created struct {
		ScheduleGroupArn string `json:"ScheduleGroupArn"`
	}
	parseJSON(t, out, &created)
	arn := created.ScheduleGroupArn
	require.NotEmpty(t, arn)
	t.Cleanup(func() {
		runCLI(t, awsCLI("scheduler", "delete-schedule-group", "--name", group))
	})

	runCLI(t, awsCLI("scheduler", "tag-resource",
		"--resource-arn", arn,
		"--tags", "Key=env,Value=prod", "Key=team,Value=ci",
	))

	out = runCLI(t, awsCLI("scheduler", "list-tags-for-resource",
		"--resource-arn", arn,
		"--output", "json",
	))
	var listed struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	parseJSON(t, out, &listed)
	got := map[string]string{}
	for _, tg := range listed.Tags {
		got[tg.Key] = tg.Value
	}
	assert.Equal(t, map[string]string{"env": "prod", "team": "ci"}, got)

	runCLI(t, awsCLI("scheduler", "untag-resource",
		"--resource-arn", arn,
		"--tag-keys", "team",
	))

	out = runCLI(t, awsCLI("scheduler", "list-tags-for-resource",
		"--resource-arn", arn,
		"--output", "json",
	))
	parseJSON(t, out, &listed)
	require.Len(t, listed.Tags, 1)
	assert.Equal(t, "env", listed.Tags[0].Key)
	assert.Equal(t, "prod", listed.Tags[0].Value)
}
