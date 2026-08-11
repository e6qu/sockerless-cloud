package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_TaskDefinitionFidelitySDK proves the full container-definition
// round-trip (ulimits/dependsOn/dockerLabels/linuxParameters/user/… were
// silently dropped, forcing a new revision every plan) plus the top-level knobs
// the provider reads back (runtime_platform, ephemeral_storage, pid_mode,
// ipc_mode, placement_constraints) and the AWS-computed compatibilities list.
func TestECS_TaskDefinitionFidelitySDK(t *testing.T) {
	c := ecsClient()

	out, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("fidelity-taskdef"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		PidMode:                 ecstypes.PidModeTask,
		IpcMode:                 ecstypes.IpcModeTask,
		RuntimePlatform: &ecstypes.RuntimePlatform{
			CpuArchitecture:       ecstypes.CPUArchitectureArm64,
			OperatingSystemFamily: ecstypes.OSFamilyLinux,
		},
		EphemeralStorage:     &ecstypes.EphemeralStorage{SizeInGiB: 30},
		PlacementConstraints: []ecstypes.TaskDefinitionPlacementConstraint{{Type: ecstypes.TaskDefinitionPlacementConstraintTypeMemberOf, Expression: aws.String("attribute:ecs.os-type == linux")}},
		ContainerDefinitions: []ecstypes.ContainerDefinition{
			{
				Name:                   aws.String("app"),
				Image:                  aws.String("nginx:latest"),
				Essential:              aws.Bool(true),
				User:                   aws.String("1000:1000"),
				WorkingDirectory:       aws.String("/srv"),
				Privileged:             aws.Bool(false),
				ReadonlyRootFilesystem: aws.Bool(true),
				StartTimeout:           aws.Int32(30),
				StopTimeout:            aws.Int32(10),
				DockerLabels:           map[string]string{"com.example.team": "platform"},
				Ulimits: []ecstypes.Ulimit{
					{Name: ecstypes.UlimitNameNofile, SoftLimit: 1024, HardLimit: 2048},
				},
				DependsOn: []ecstypes.ContainerDependency{
					{ContainerName: aws.String("sidecar"), Condition: ecstypes.ContainerConditionStart},
				},
				LinuxParameters: &ecstypes.LinuxParameters{InitProcessEnabled: aws.Bool(true)},
			},
			{
				Name:      aws.String("sidecar"),
				Image:     aws.String("busybox:latest"),
				Essential: aws.Bool(false),
			},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.TaskDefinition.TaskDefinitionArn)

	desc, err := c.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: out.TaskDefinition.TaskDefinitionArn,
	})
	require.NoError(t, err)
	td := desc.TaskDefinition

	// Top-level knobs round-trip.
	require.NotNil(t, td.RuntimePlatform)
	assert.Equal(t, ecstypes.CPUArchitectureArm64, td.RuntimePlatform.CpuArchitecture)
	assert.Equal(t, ecstypes.OSFamilyLinux, td.RuntimePlatform.OperatingSystemFamily)
	require.NotNil(t, td.EphemeralStorage)
	assert.Equal(t, int32(30), td.EphemeralStorage.SizeInGiB)
	assert.Equal(t, ecstypes.PidModeTask, td.PidMode)
	assert.Equal(t, ecstypes.IpcModeTask, td.IpcMode)
	require.Len(t, td.PlacementConstraints, 1)
	assert.Equal(t, "attribute:ecs.os-type == linux", aws.ToString(td.PlacementConstraints[0].Expression))

	// Compatibilities: awsvpc + Fargate ⇒ [EC2, FARGATE].
	assert.Contains(t, td.Compatibilities, ecstypes.CompatibilityEc2)
	assert.Contains(t, td.Compatibilities, ecstypes.CompatibilityFargate)

	// Full container-definition round-trip — every previously-dropped field.
	require.Len(t, td.ContainerDefinitions, 2)
	app := td.ContainerDefinitions[0]
	assert.Equal(t, "1000:1000", aws.ToString(app.User))
	assert.Equal(t, "/srv", aws.ToString(app.WorkingDirectory))
	assert.True(t, aws.ToBool(app.ReadonlyRootFilesystem))
	assert.Equal(t, int32(30), aws.ToInt32(app.StartTimeout))
	assert.Equal(t, int32(10), aws.ToInt32(app.StopTimeout))
	assert.Equal(t, "platform", app.DockerLabels["com.example.team"])
	require.Len(t, app.Ulimits, 1)
	assert.Equal(t, ecstypes.UlimitNameNofile, app.Ulimits[0].Name)
	assert.Equal(t, int32(2048), app.Ulimits[0].HardLimit)
	require.Len(t, app.DependsOn, 1)
	assert.Equal(t, "sidecar", aws.ToString(app.DependsOn[0].ContainerName))
	assert.Equal(t, ecstypes.ContainerConditionStart, app.DependsOn[0].Condition)
	require.NotNil(t, app.LinuxParameters)
	assert.True(t, aws.ToBool(app.LinuxParameters.InitProcessEnabled))
}
