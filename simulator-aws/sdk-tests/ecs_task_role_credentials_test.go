package aws_sdk_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_TaskRoleCredentialsAuthorizeWorkloadAWSCLI runs the AWS CLI in an
// awsvpc and a bridge-mode task, each under a task role: the CLI's default
// chain finds the container credentials endpoint the Amazon ECS agent names in
// the environment, and STS sees the task role.
func TestECS_TaskRoleCredentialsAuthorizeWorkloadAWSCLI(t *testing.T) {
	t.Parallel()
	for _, mode := range []ecstypes.NetworkMode{ecstypes.NetworkModeAwsvpc, ecstypes.NetworkModeBridge} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			runTaskRoleCredentialsTask(t, mode)
		})
	}
}

func runTaskRoleCredentialsTask(t *testing.T, mode ecstypes.NetworkMode) {
	iamc := iamClient()
	name := "task-role-credentials-" + string(mode)
	roleName := "ecs-" + name
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	roleOut, err := iamc.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(trust),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = iamc.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
			RoleName: aws.String(roleName), PolicyName: aws.String("caller-identity"),
		})
		_, _ = iamc.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(roleName)})
	})
	_, err = iamc.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:   aws.String(roleName),
		PolicyName: aws.String("caller-identity"),
		PolicyDocument: aws.String(
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:GetCallerIdentity","Resource":"*"}]}`,
		),
	})
	require.NoError(t, err)

	ecsc := ecsClient()
	cluster := name
	_, err = ecsc.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)

	logGroup := "/ecs/" + name
	logs := cwLogsClient()
	_, _ = logs.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(logGroup)})
	t.Cleanup(func() {
		_, _ = logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroup)})
	})

	definition := &ecs.RegisterTaskDefinitionInput{
		Family:      aws.String(name),
		NetworkMode: mode,
		TaskRoleArn: roleOut.Role.Arn,
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			StopTimeout: aws.Int32(2),
			Name:        aws.String("aws-cli"),
			Image:       aws.String("mirror.gcr.io/amazon/aws-cli:2.27.49"),
			EntryPoint:  []string{"sh", "-c"},
			Command: []string{`set -e
# The Amazon ECS agent serves a task one credential until it refreshes it, so
# two fetches a moment apart return the same access key.
if [ -n "${AWS_CONTAINER_CREDENTIALS_RELATIVE_URI:-}" ]; then
	credentials_uri="http://169.254.170.2${AWS_CONTAINER_CREDENTIALS_RELATIVE_URI}"
else
	credentials_uri="${AWS_CONTAINER_CREDENTIALS_FULL_URI}"
fi
access_key_id() {
	curl --fail --silent "$credentials_uri" |
		python -c 'import json, sys; print(json.load(sys.stdin)["AccessKeyId"])'
}
first_key=$(access_key_id)
second_key=$(access_key_id)
if [ -z "$first_key" ] || [ "$first_key" != "$second_key" ]; then
	echo "credentials endpoint served ${first_key:-nothing} and then ${second_key:-nothing}"
	exit 3
fi
aws sts get-caller-identity --query Arn --output text`},
			Environment: []ecstypes.KeyValuePair{
				{
					Name:  aws.String("AWS_ENDPOINT_URL"),
					Value: aws.String(fmt.Sprintf("http://host.docker.internal:%d", simPort)),
				},
				{Name: aws.String("AWS_REGION"), Value: aws.String("us-east-1")},
				{Name: aws.String("AWS_DEFAULT_REGION"), Value: aws.String("us-east-1")},
				{Name: aws.String("AWS_EC2_METADATA_DISABLED"), Value: aws.String("true")},
			},
			LogConfiguration: &ecstypes.LogConfiguration{
				LogDriver: ecstypes.LogDriverAwslogs,
				Options: map[string]string{
					"awslogs-group":         logGroup,
					"awslogs-create-group":  "true",
					"awslogs-stream-prefix": "ecs",
				},
			},
		}},
	}
	run := &ecs.RunTaskInput{Cluster: aws.String(cluster)}
	if mode == ecstypes.NetworkModeAwsvpc {
		definition.RequiresCompatibilities = []ecstypes.Compatibility{ecstypes.CompatibilityFargate}
		definition.Cpu, definition.Memory = aws.String("256"), aws.String("512")
		run.LaunchType = ecstypes.LaunchTypeFargate
		run.NetworkConfiguration = &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{createECSTestSubnet(t, name)}},
		}
	}
	taskDefinition, err := ecsc.RegisterTaskDefinition(ctx, definition)
	require.NoError(t, err)
	run.TaskDefinition = taskDefinition.TaskDefinition.TaskDefinitionArn

	started, err := ecsc.RunTask(ctx, run)
	require.NoError(t, err)
	require.Len(t, started.Tasks, 1)
	taskArn := aws.ToString(started.Tasks[0].TaskArn)
	cleanupECSTask(t, ecsc, cluster, taskArn)

	task := waitTaskStopped(t, ecsc, cluster, taskArn)
	require.NotEmpty(t, task.Containers)
	streams, err := logs.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(logGroup),
	})
	require.NoError(t, err)
	require.Len(t, streams.LogStreams, 1)
	events, err := logs.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(logGroup),
		LogStreamName: streams.LogStreams[0].LogStreamName,
	})
	require.NoError(t, err)
	var messages []string
	for _, event := range events.Events {
		messages = append(messages, aws.ToString(event.Message))
	}
	output := strings.Join(messages, "\n")
	require.NotNil(t, task.Containers[0].ExitCode)
	assert.Equal(t, int32(0), aws.ToInt32(task.Containers[0].ExitCode), "%s\n%s", aws.ToString(task.StoppedReason), output)
	assert.Contains(t, output, "assumed-role/"+roleName+"/")
}
