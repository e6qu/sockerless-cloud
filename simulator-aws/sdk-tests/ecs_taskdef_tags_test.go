package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_TaskDefinitionTagsIncludePath verifies task-definition tags surface on
// the paths terraform-provider-aws reads: the RegisterTaskDefinition response
// (top-level Tags), DescribeTaskDefinition with include=TAGS (top-level Tags),
// and ListTagsForResource. Without include=TAGS, Describe returns no tags
// (matching real AWS), so the provider only sees them on the include path.
func TestECS_TaskDefinitionTagsIncludePath(t *testing.T) {
	c := ecsClient()
	tags := []ecstypes.Tag{
		{Key: aws.String("Name"), Value: aws.String("test")},
		{Key: aws.String("env"), Value: aws.String("ci")},
	}
	reg, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:               aws.String("taskdef-tags"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{Name: aws.String("app"), Image: aws.String("nginx")}},
		Tags:                 tags,
	})
	require.NoError(t, err)
	// RegisterTaskDefinition echoes tags at the response top level.
	assert.ElementsMatch(t, tagPairs(tags), tagPairs(reg.Tags))

	arn := aws.ToString(reg.TaskDefinition.TaskDefinitionArn)

	// DescribeTaskDefinition with include=TAGS surfaces them (the provider's path).
	withTags, err := c.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(arn),
		Include:        []ecstypes.TaskDefinitionField{ecstypes.TaskDefinitionFieldTags},
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, tagPairs(tags), tagPairs(withTags.Tags),
		"DescribeTaskDefinition --include TAGS must return the tags")

	// Without include=TAGS, no tags (real AWS omits them).
	noTags, err := c.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Empty(t, noTags.Tags, "DescribeTaskDefinition without include=TAGS returns no tags")

	// ListTagsForResource also returns them.
	lt, err := c.ListTagsForResource(ctx, &ecs.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	require.NoError(t, err)
	assert.ElementsMatch(t, tagPairs(tags), tagPairs(lt.Tags))
}

func tagPairs(tags []ecstypes.Tag) [][2]string {
	out := make([][2]string, 0, len(tags))
	for _, tg := range tags {
		out = append(out, [2]string{aws.ToString(tg.Key), aws.ToString(tg.Value)})
	}
	return out
}

// TestECS_DescribeTasksTagsIncludePath verifies that DescribeTasks surfaces
// task tags only when Include=[TAGS] is passed, matching real AWS. Without
// Include, the tags field is absent from each task object.
func TestECS_DescribeTasksTagsIncludePath(t *testing.T) {
	c := ecsClient()

	reg, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:               aws.String("task-tags-include"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{Name: aws.String("app"), Image: aws.String("nginx")}},
	})
	require.NoError(t, err)

	cluster, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String("task-tags-include-cluster")})
	require.NoError(t, err)
	clusterArn := cluster.Cluster.ClusterArn

	run, err := c.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        clusterArn,
		TaskDefinition: reg.TaskDefinition.TaskDefinitionArn,
		Tags: []ecstypes.Tag{
			{Key: aws.String("app"), Value: aws.String("billing")},
			{Key: aws.String("env"), Value: aws.String("prod")},
		},
	})
	require.NoError(t, err)
	require.Len(t, run.Tasks, 1)
	taskArn := run.Tasks[0].TaskArn

	// With Include=TAGS, the tags appear on the task.
	withTags, err := c.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: clusterArn,
		Tasks:   []string{aws.ToString(taskArn)},
		Include: []ecstypes.TaskField{ecstypes.TaskFieldTags},
	})
	require.NoError(t, err)
	require.Len(t, withTags.Tasks, 1)
	assert.NotEmpty(t, withTags.Tasks[0].Tags, "DescribeTasks with Include=TAGS must return tags")

	// Without Include, tags are absent.
	noTags, err := c.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: clusterArn,
		Tasks:   []string{aws.ToString(taskArn)},
	})
	require.NoError(t, err)
	require.Len(t, noTags.Tasks, 1)
	assert.Empty(t, noTags.Tasks[0].Tags, "DescribeTasks without Include=TAGS must omit tags")

	_, _ = c.StopTask(ctx, &ecs.StopTaskInput{Cluster: clusterArn, Task: taskArn})
}
