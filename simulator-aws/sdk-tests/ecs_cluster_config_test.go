package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECS_ClusterSettingsAndConfiguration covers containerInsights
// settings and the executeCommand KMS configuration round-trip through
// DescribeClusters --include SETTINGS CONFIGURATIONS, and are omitted otherwise.
func TestECS_ClusterSettingsAndConfiguration(t *testing.T) {
	c := ecsClient()
	name := "probe-cluster-config"
	_, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{
		ClusterName: aws.String(name),
		Settings: []ecstypes.ClusterSetting{{
			Name:  ecstypes.ClusterSettingNameContainerInsights,
			Value: aws.String("enabled"),
		}},
		Configuration: &ecstypes.ClusterConfiguration{
			ExecuteCommandConfiguration: &ecstypes.ExecuteCommandConfiguration{
				KmsKeyId: aws.String("arn:aws:kms:us-east-1:123456789012:key/test-key"),
				Logging:  ecstypes.ExecuteCommandLoggingDefault,
			},
		},
	})
	require.NoError(t, err)

	withInclude, err := c.DescribeClusters(ctx, &ecs.DescribeClustersInput{
		Clusters: []string{name},
		Include:  []ecstypes.ClusterField{ecstypes.ClusterFieldSettings, ecstypes.ClusterFieldConfigurations},
	})
	require.NoError(t, err)
	require.Len(t, withInclude.Clusters, 1)
	cl := withInclude.Clusters[0]
	require.Len(t, cl.Settings, 1)
	assert.Equal(t, ecstypes.ClusterSettingNameContainerInsights, cl.Settings[0].Name)
	assert.Equal(t, "enabled", aws.ToString(cl.Settings[0].Value))
	require.NotNil(t, cl.Configuration)
	require.NotNil(t, cl.Configuration.ExecuteCommandConfiguration)
	assert.Equal(t, "arn:aws:kms:us-east-1:123456789012:key/test-key",
		aws.ToString(cl.Configuration.ExecuteCommandConfiguration.KmsKeyId))

	// Without include, settings/configuration are not surfaced.
	noInclude, err := c.DescribeClusters(ctx, &ecs.DescribeClustersInput{Clusters: []string{name}})
	require.NoError(t, err)
	require.Len(t, noInclude.Clusters, 1)
	assert.Empty(t, noInclude.Clusters[0].Settings)
	assert.Nil(t, noInclude.Clusters[0].Configuration)
}
