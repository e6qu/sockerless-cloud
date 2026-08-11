package aws_sdk_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRDS_EngineVersionDefault verifies CreateDBInstance populates
// EngineVersion with the engine's GA default when the client omits it.
// terraform-provider-aws stores the resolved version in state and
// drifts on the next plan if the sim echoes empty.
func TestRDS_EngineVersionDefault(t *testing.T) {
	c := rds.NewFromConfig(sdkConfig(), func(o *rds.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
	ctx := context.Background()

	out, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String("default-engine-pg"),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		AllocatedStorage:     aws.Int32(20),
		MasterUsername:       aws.String("adm"),
		MasterUserPassword:   aws.String("Password1234!"),
	})
	require.NoError(t, err)
	got := aws.ToString(out.DBInstance.EngineVersion)
	require.NotEmpty(t, got, "EngineVersion default must be populated")
	assert.Equal(t, "17.5", got, "postgres GA default must be the canonical sim value")

	// Explicit EngineVersion still wins.
	out2, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String("explicit-engine-pg"),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		AllocatedStorage:     aws.Int32(20),
		EngineVersion:        aws.String("15.4"),
		MasterUsername:       aws.String("adm"),
		MasterUserPassword:   aws.String("Password1234!"),
	})
	require.NoError(t, err)
	assert.Equal(t, "15.4", aws.ToString(out2.DBInstance.EngineVersion),
		"explicit EngineVersion must win over default")
}

// TestElastiCache_EngineVersionDefault — same shape for redis cluster.
func TestElastiCache_EngineVersionDefault(t *testing.T) {
	c := elasticache.NewFromConfig(sdkConfig(), func(o *elasticache.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
	ctx := context.Background()

	out, err := c.CreateCacheCluster(ctx, &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String("default-engine-redis"),
		Engine:         aws.String("redis"),
		CacheNodeType:  aws.String("cache.t3.micro"),
		NumCacheNodes:  aws.Int32(1),
	})
	require.NoError(t, err)
	got := aws.ToString(out.CacheCluster.EngineVersion)
	require.NotEmpty(t, got, "ElastiCache redis EngineVersion default must be populated")
	assert.Equal(t, "7.1", got, "redis GA default must be the canonical sim value")

	out2, err := c.CreateCacheCluster(ctx, &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String("explicit-engine-redis"),
		Engine:         aws.String("redis"),
		EngineVersion:  aws.String("6.2"),
		CacheNodeType:  aws.String("cache.t3.micro"),
		NumCacheNodes:  aws.Int32(1),
	})
	require.NoError(t, err)
	assert.Equal(t, "6.2", aws.ToString(out2.CacheCluster.EngineVersion))
}
