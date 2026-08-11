package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_ServiceFidelitySDK proves the service knobs the provider reads back
// round-trip on both create and update — each was dropped before, drifting
// aws_ecs_service every plan.
func TestECS_ServiceFidelitySDK(t *testing.T) {
	c := ecsClient()

	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String("svc-fidelity-cluster")})
	require.NoError(t, err)
	td, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("svc-fidelity-td"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name: aws.String("app"), Image: aws.String(containerCommandImage), Command: []string{"hold"},
		}},
	})
	require.NoError(t, err)
	tdArn := aws.ToString(td.TaskDefinition.TaskDefinitionArn)

	created, err := c.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:                       aws.String("svc-fidelity-cluster"),
		ServiceName:                   aws.String("svc-fidelity"),
		TaskDefinition:                aws.String(tdArn),
		DesiredCount:                  aws.Int32(2),
		SchedulingStrategy:            ecstypes.SchedulingStrategyReplica,
		EnableECSManagedTags:          true,
		HealthCheckGracePeriodSeconds: aws.Int32(120),
		PropagateTags:                 ecstypes.PropagateTagsService,
		PlacementConstraints: []ecstypes.PlacementConstraint{
			{Type: ecstypes.PlacementConstraintTypeMemberOf, Expression: aws.String("attribute:ecs.instance-type == t3.micro")},
		},
		PlacementStrategy: []ecstypes.PlacementStrategy{
			{Type: ecstypes.PlacementStrategyTypeSpread, Field: aws.String("attribute:ecs.availability-zone")},
		},
	})
	require.NoError(t, err)
	cleanupECSService(t, c, "svc-fidelity-cluster", "svc-fidelity")
	svc := created.Service
	assert.True(t, svc.EnableECSManagedTags, "enable_ecs_managed_tags must round-trip")
	assert.Equal(t, int32(120), aws.ToInt32(svc.HealthCheckGracePeriodSeconds))
	require.Len(t, svc.PlacementConstraints, 1)
	assert.Equal(t, "attribute:ecs.instance-type == t3.micro", aws.ToString(svc.PlacementConstraints[0].Expression))
	require.Len(t, svc.PlacementStrategy, 1)
	assert.Equal(t, ecstypes.PlacementStrategyTypeSpread, svc.PlacementStrategy[0].Type)

	// UpdateService must persist fields beyond desiredCount/taskDefinition.
	_, err = c.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:                       aws.String("svc-fidelity-cluster"),
		Service:                       aws.String("svc-fidelity"),
		DesiredCount:                  aws.Int32(3),
		HealthCheckGracePeriodSeconds: aws.Int32(300),
		EnableExecuteCommand:          aws.Bool(true),
		PropagateTags:                 ecstypes.PropagateTagsTaskDefinition,
	})
	require.NoError(t, err)
	desc, err := c.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster: aws.String("svc-fidelity-cluster"), Services: []string{"svc-fidelity"},
	})
	require.NoError(t, err)
	got := desc.Services[0]
	assert.Equal(t, int32(3), got.DesiredCount, "desired_count update persists")
	assert.Equal(t, int32(300), aws.ToInt32(got.HealthCheckGracePeriodSeconds), "health_check_grace_period update persists")
	assert.True(t, got.EnableExecuteCommand, "enable_execute_command update persists")
	assert.Equal(t, ecstypes.PropagateTagsTaskDefinition, got.PropagateTags, "propagate_tags update persists")
	// Constraints set at create must survive an unrelated update.
	require.Len(t, got.PlacementConstraints, 1)
}

// TestECS_ClusterUpdateFidelitySDK covers UpdateCluster + UpdateClusterSettings,
// which were unregistered — so aws_ecs_cluster setting/configuration changes
// forced recreation.
func TestECS_ClusterUpdateFidelitySDK(t *testing.T) {
	c := ecsClient()

	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String("cluster-update-fidelity"),
		Settings:    []ecstypes.ClusterSetting{{Name: ecstypes.ClusterSettingNameContainerInsights, Value: aws.String("disabled")}},
	})
	require.NoError(t, err)

	// UpdateClusterSettings flips containerInsights.
	_, err = c.UpdateClusterSettings(ctx, &ecs.UpdateClusterSettingsInput{
		Cluster:  aws.String("cluster-update-fidelity"),
		Settings: []ecstypes.ClusterSetting{{Name: ecstypes.ClusterSettingNameContainerInsights, Value: aws.String("enabled")}},
	})
	require.NoError(t, err)
	desc, err := c.DescribeClusters(ctx, &ecs.DescribeClustersInput{
		Clusters: []string{"cluster-update-fidelity"}, Include: []ecstypes.ClusterField{ecstypes.ClusterFieldSettings},
	})
	require.NoError(t, err)
	require.Len(t, desc.Clusters[0].Settings, 1)
	assert.Equal(t, "enabled", aws.ToString(desc.Clusters[0].Settings[0].Value), "UpdateClusterSettings must persist")

	// UpdateCluster sets the execute-command configuration.
	_, err = c.UpdateCluster(ctx, &ecs.UpdateClusterInput{
		Cluster: aws.String("cluster-update-fidelity"),
		Configuration: &ecstypes.ClusterConfiguration{
			ExecuteCommandConfiguration: &ecstypes.ExecuteCommandConfiguration{Logging: ecstypes.ExecuteCommandLoggingNone},
		},
	})
	require.NoError(t, err)
	desc2, err := c.DescribeClusters(ctx, &ecs.DescribeClustersInput{
		Clusters: []string{"cluster-update-fidelity"}, Include: []ecstypes.ClusterField{ecstypes.ClusterFieldConfigurations},
	})
	require.NoError(t, err)
	require.NotNil(t, desc2.Clusters[0].Configuration, "UpdateCluster configuration must round-trip")
	require.NotNil(t, desc2.Clusters[0].Configuration.ExecuteCommandConfiguration)
	assert.Equal(t, ecstypes.ExecuteCommandLoggingNone, desc2.Clusters[0].Configuration.ExecuteCommandConfiguration.Logging)
}
