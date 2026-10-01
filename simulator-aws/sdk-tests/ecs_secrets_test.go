package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_TaskDefinitionSecretsInjected covers the ECS secrets contract: a
// container definition's `secrets` array (valueFrom → SecretsManager) must be
// resolved at task launch and injected as the named environment variable,
// indistinguishable from a plain `environment` entry to the container.
func TestECS_TaskDefinitionSecretsInjected(t *testing.T) {
	family := uniqueName("ecs-secrets")
	secretName := uniqueName("ecs/inject-secret")
	client := ecsClient()
	sm := smClient()
	cw := cwLogsClient()

	const secretValue = "sockerless-ecs-secret-deadbeef"
	created, err := sm.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         aws.String(secretName),
		SecretString: aws.String(secretValue),
	})
	require.NoError(t, err)
	secretArn := aws.ToString(created.ARN)
	require.NotEmpty(t, secretArn)

	cluster := uniqueName("ecs-secrets-cluster")
	_, err = client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	subnetID := createECSTestSubnet(t, "secrets")

	logGroup := uniqueName("/ecs/secrets-inject")
	_, _ = cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(logGroup)})
	t.Cleanup(func() {
		_, _ = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroup)})
	})

	td, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(family),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("app"),
			Image:       aws.String("public.ecr.aws/docker/library/busybox:latest"),
			EntryPoint:  []string{"sh", "-c"},
			Command:     []string{`echo "RESOLVED=$EDD_AGENT_SECRET"`},
			Secrets: []ecstypes.Secret{{
				Name:      aws.String("EDD_AGENT_SECRET"),
				ValueFrom: aws.String(secretArn),
			}},
			LogConfiguration: &ecstypes.LogConfiguration{
				LogDriver: ecstypes.LogDriverAwslogs,
				Options: map[string]string{
					"awslogs-group":         logGroup,
					"awslogs-create-group":  "true",
					"awslogs-stream-prefix": "ecs",
				},
			},
		}},
	})
	require.NoError(t, err)

	run, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(cluster),
		TaskDefinition: td.TaskDefinition.TaskDefinitionArn,
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
		},
	})
	require.NoError(t, err)
	require.Len(t, run.Tasks, 1)
	taskArn := aws.ToString(run.Tasks[0].TaskArn)
	cleanupECSTask(t, client, cluster, taskArn)
	waitForECSTaskStatus(t, client, cluster, taskArn, "STOPPED")

	// The awslogs driver has delivered every line by the time the task stops.
	ev, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(logGroup)})
	require.NoError(t, err)
	var resolved bool
	for _, e := range ev.Events {
		resolved = resolved || strings.Contains(aws.ToString(e.Message), "RESOLVED="+secretValue)
	}
	assert.True(t, resolved, "container must receive the resolved secret as $EDD_AGENT_SECRET")
}
