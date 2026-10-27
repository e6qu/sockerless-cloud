package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_TaskDefinitionTagsIncludePath expects tags on the paths
// terraform-provider-aws reads: the RegisterTaskDefinition response,
// DescribeTaskDefinition with include=TAGS, and ListTagsForResource; without
// include=TAGS, Describe returns none.
func TestECS_TaskDefinitionTagsIncludePath(t *testing.T) {
	t.Parallel()
	c := ecsClient()
	tags := []ecstypes.Tag{
		{Key: aws.String("Name"), Value: aws.String("test")},
		{Key: aws.String("env"), Value: aws.String("ci")},
	}
	reg, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:               aws.String("taskdef-tags"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{StopTimeout: aws.Int32(2), Name: aws.String("app"), Image: aws.String("mirror.gcr.io/library/nginx:latest")}},
		Tags:                 tags,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, tagPairs(tags), tagPairs(reg.Tags))

	arn := aws.ToString(reg.TaskDefinition.TaskDefinitionArn)

	withTags, err := c.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(arn),
		Include:        []ecstypes.TaskDefinitionField{ecstypes.TaskDefinitionFieldTags},
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, tagPairs(tags), tagPairs(withTags.Tags),
		"DescribeTaskDefinition --include TAGS must return the tags")

	noTags, err := c.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Empty(t, noTags.Tags, "DescribeTaskDefinition without include=TAGS returns no tags")

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

// TestECS_DescribeTasksTagsIncludePath expects DescribeTasks to return task tags
// only with Include=[TAGS].
func TestECS_DescribeTasksTagsIncludePath(t *testing.T) {
	t.Parallel()
	c := ecsClient()

	reg, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:               aws.String("task-tags-include"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{StopTimeout: aws.Int32(2), Name: aws.String("app"), Image: aws.String("mirror.gcr.io/library/nginx:latest")}},
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

	withTags, err := c.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: clusterArn,
		Tasks:   []string{aws.ToString(taskArn)},
		Include: []ecstypes.TaskField{ecstypes.TaskFieldTags},
	})
	require.NoError(t, err)
	require.Len(t, withTags.Tasks, 1)
	assert.NotEmpty(t, withTags.Tasks[0].Tags, "DescribeTasks with Include=TAGS must return tags")

	noTags, err := c.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: clusterArn,
		Tasks:   []string{aws.ToString(taskArn)},
	})
	require.NoError(t, err)
	require.Len(t, noTags.Tasks, 1)
	assert.Empty(t, noTags.Tasks[0].Tags, "DescribeTasks without Include=TAGS must omit tags")

	_, _ = c.StopTask(ctx, &ecs.StopTaskInput{Cluster: clusterArn, Task: taskArn})
}
