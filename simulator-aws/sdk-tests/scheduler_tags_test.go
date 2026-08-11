package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScheduler_ScheduleGroupTags exercises tag CRUD on a schedule group: tag,
// list, untag, list again. The ResourceArn shares the real `/tags/{ResourceArn}`
// path with other REST-JSON services, so this also pins the sim's SigV4-signing-
// service routing of scheduler-signed tag traffic.
func TestScheduler_ScheduleGroupTags(t *testing.T) {
	c := schedulerClient()
	const group = "tagged-group"

	createOut, err := c.CreateScheduleGroup(ctx, &scheduler.CreateScheduleGroupInput{
		Name: aws.String(group),
	})
	require.NoError(t, err)
	arn := aws.ToString(createOut.ScheduleGroupArn)
	require.NotEmpty(t, arn)
	t.Cleanup(func() {
		_, _ = c.DeleteScheduleGroup(ctx, &scheduler.DeleteScheduleGroupInput{Name: aws.String(group)})
	})

	// Initially untagged.
	listOut, err := c.ListTagsForResource(ctx, &scheduler.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	assert.Empty(t, listOut.Tags)

	// Tag with two keys.
	_, err = c.TagResource(ctx, &scheduler.TagResourceInput{
		ResourceArn: aws.String(arn),
		Tags: []schedtypes.Tag{
			{Key: aws.String("env"), Value: aws.String("prod")},
			{Key: aws.String("team"), Value: aws.String("ci")},
		},
	})
	require.NoError(t, err)

	listOut, err = c.ListTagsForResource(ctx, &scheduler.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	got := map[string]string{}
	for _, tg := range listOut.Tags {
		got[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	assert.Equal(t, map[string]string{"env": "prod", "team": "ci"}, got)

	// Untag one key.
	_, err = c.UntagResource(ctx, &scheduler.UntagResourceInput{
		ResourceArn: aws.String(arn),
		TagKeys:     []string{"team"},
	})
	require.NoError(t, err)

	listOut, err = c.ListTagsForResource(ctx, &scheduler.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, listOut.Tags, 1)
	assert.Equal(t, "env", aws.ToString(listOut.Tags[0].Key))
	assert.Equal(t, "prod", aws.ToString(listOut.Tags[0].Value))
}

// TestScheduler_ScheduleTags tags a schedule (not a group), exercising the
// schedule ARN form `.../schedule/{group}/{name}` on the tag route.
func TestScheduler_ScheduleTags(t *testing.T) {
	c := schedulerClient()
	const name = "tagged-schedule"

	createOut, err := c.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name:               aws.String(name),
		ScheduleExpression: aws.String("rate(1 hour)"),
		FlexibleTimeWindow: &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff},
		Target: &schedtypes.Target{
			Arn:     aws.String("arn:aws:lambda:us-east-1:123456789012:function:tagged"),
			RoleArn: aws.String("arn:aws:iam::123456789012:role/scheduler-role"),
		},
	})
	require.NoError(t, err)
	arn := aws.ToString(createOut.ScheduleArn)
	t.Cleanup(func() {
		_, _ = c.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String(name)})
	})

	_, err = c.TagResource(ctx, &scheduler.TagResourceInput{
		ResourceArn: aws.String(arn),
		Tags:        []schedtypes.Tag{{Key: aws.String("k"), Value: aws.String("v")}},
	})
	require.NoError(t, err)

	listOut, err := c.ListTagsForResource(ctx, &scheduler.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, listOut.Tags, 1)
	assert.Equal(t, "v", aws.ToString(listOut.Tags[0].Value))
}
