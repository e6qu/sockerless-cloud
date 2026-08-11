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

// TestECS_TaskRoleCredentialsAuthorizeWorkloadAWSCLI exercises the real
// "GET /v4/{id}/credentials" metadata route from a launched workload.
func TestECS_TaskRoleCredentialsAuthorizeWorkloadAWSCLI(t *testing.T) {
	iamc := iamClient()
	roleName := "ecs-task-role-credentials"
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
	cluster := "task-role-credentials"
	_, err = ecsc.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	subnetID := createECSTestSubnet(t, "task-role-credentials")

	logGroup := "/ecs/task-role-credentials"
	logs := cwLogsClient()
	_, _ = logs.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(logGroup)})
	t.Cleanup(func() {
		_, _ = logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroup)})
	})

	taskDefinition, err := ecsc.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("task-role-credentials"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		TaskRoleArn:             roleOut.Role.Arn,
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:       aws.String("aws-cli"),
			Image:      aws.String("public.ecr.aws/aws-cli/aws-cli:2.27.49"),
			EntryPoint: []string{"sh", "-c"},
			Command: []string{`printf 'credentials-full=%s credentials-relative=%s\n' \
"${AWS_CONTAINER_CREDENTIALS_FULL_URI:-}" "${AWS_CONTAINER_CREDENTIALS_RELATIVE_URI:-}"
# A real-VPC task receives the standard ECS relative URI and the AWS CLI
# consumes it directly. The cross-platform Docker-network tier cannot route
# 169.254.170.2, while Botocore deliberately rejects its host-gateway alias as
# a FULL_URI credential host. Exercise that tier's endpoint explicitly, then
# pass the exact returned session credentials to the same real AWS CLI call.
if [ -z "${AWS_CONTAINER_CREDENTIALS_RELATIVE_URI:-}" ]; then
	eval "$(
		curl --fail --silent "${AWS_CONTAINER_CREDENTIALS_FULL_URI}" |
			python -c 'import json, sys
try:
    from shlex import quote
except ImportError:
    from pipes import quote
d = json.load(sys.stdin)
print("export AWS_ACCESS_KEY_ID=%s AWS_SECRET_ACCESS_KEY=%s AWS_SESSION_TOKEN=%s" % tuple(quote(d[k]) for k in ("AccessKeyId", "SecretAccessKey", "Token")))'
	)"
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
					"awslogs-stream-prefix": "ecs",
				},
			},
		}},
	})
	require.NoError(t, err)

	run, err := ecsc.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        aws.String(cluster),
		TaskDefinition: taskDefinition.TaskDefinition.TaskDefinitionArn,
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets: []string{subnetID},
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, run.Tasks, 1)
	taskArn := aws.ToString(run.Tasks[0].TaskArn)
	cleanupECSTask(t, ecsc, cluster, taskArn)

	task := waitTaskStopped(t, ecsc, cluster, taskArn)
	require.NotEmpty(t, task.Containers)
	require.NotNil(t, task.Containers[0].ExitCode)
	assert.Equal(t, int32(0), aws.ToInt32(task.Containers[0].ExitCode), aws.ToString(task.StoppedReason))

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
	assert.Contains(t, strings.Join(messages, "\n"), "assumed-role/"+roleName+"/")
}
