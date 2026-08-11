package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_DeploymentConfigurationRoundTrip verifies the deployment
// configuration (circuit breaker + percentages) round-trips through
// DescribeServices.
func TestECS_DeploymentConfigurationRoundTrip(t *testing.T) {
	c := ecsClient()
	cluster := "deploycfg-cluster"
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	td, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("deploycfg-task"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name: aws.String("app"), Image: aws.String(containerCommandImage),
			Command: []string{"hold"}, Memory: aws.Int32(128),
		}},
	})
	require.NoError(t, err)

	_, err = c.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String("deploycfg-svc"),
		TaskDefinition: td.TaskDefinition.TaskDefinitionArn,
		DesiredCount:   aws.Int32(1),
		DeploymentConfiguration: &ecstypes.DeploymentConfiguration{
			DeploymentCircuitBreaker: &ecstypes.DeploymentCircuitBreaker{Enable: true, Rollback: true},
			MaximumPercent:           aws.Int32(200),
			MinimumHealthyPercent:    aws.Int32(100),
		},
	})
	require.NoError(t, err)
	cleanupECSService(t, c, cluster, "deploycfg-svc")

	desc, err := c.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(cluster),
		Services: []string{"deploycfg-svc"},
	})
	require.NoError(t, err)
	require.Len(t, desc.Services, 1)
	dc := desc.Services[0].DeploymentConfiguration
	require.NotNil(t, dc, "deploymentConfiguration must round-trip through DescribeServices")
	require.NotNil(t, dc.DeploymentCircuitBreaker)
	assert.True(t, dc.DeploymentCircuitBreaker.Enable)
	assert.True(t, dc.DeploymentCircuitBreaker.Rollback)
	assert.Equal(t, int32(200), aws.ToInt32(dc.MaximumPercent))
	assert.Equal(t, int32(100), aws.ToInt32(dc.MinimumHealthyPercent))
}
