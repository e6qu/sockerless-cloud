package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_ContainerHealthCheckAndSecretsRoundTrip verifies healthCheck and
// secrets survive RegisterTaskDefinition -> DescribeTaskDefinition. The
// provider folds the whole containerDefinitions into a ForceNew hash, so a
// dropped field forces a new revision (and a cascading service update) on
// every plan.
func TestECS_ContainerHealthCheckAndSecretsRoundTrip(t *testing.T) {
	c := ecsClient()
	reg, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("hc-secrets-task"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:  aws.String("app"),
			Image: aws.String("nginx"),
			HealthCheck: &ecstypes.HealthCheck{
				Command:     []string{"CMD-SHELL", "curl -f http://localhost/ || exit 1"},
				Interval:    aws.Int32(30),
				Timeout:     aws.Int32(5),
				Retries:     aws.Int32(3),
				StartPeriod: aws.Int32(30),
			},
			Secrets: []ecstypes.Secret{{
				Name:      aws.String("DB"),
				ValueFrom: aws.String("arn:aws:ssm:us-east-1:123456789012:parameter/db"),
			}},
		}},
	})
	require.NoError(t, err)

	desc, err := c.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: reg.TaskDefinition.TaskDefinitionArn,
	})
	require.NoError(t, err)
	require.Len(t, desc.TaskDefinition.ContainerDefinitions, 1)
	cd := desc.TaskDefinition.ContainerDefinitions[0]

	require.NotNil(t, cd.HealthCheck, "healthCheck must round-trip")
	assert.Equal(t, []string{"CMD-SHELL", "curl -f http://localhost/ || exit 1"}, cd.HealthCheck.Command)
	assert.Equal(t, int32(30), aws.ToInt32(cd.HealthCheck.Interval))
	assert.Equal(t, int32(3), aws.ToInt32(cd.HealthCheck.Retries))

	require.Len(t, cd.Secrets, 1, "secrets must round-trip")
	assert.Equal(t, "DB", aws.ToString(cd.Secrets[0].Name))
	assert.Equal(t, "arn:aws:ssm:us-east-1:123456789012:parameter/db", aws.ToString(cd.Secrets[0].ValueFrom))
}
