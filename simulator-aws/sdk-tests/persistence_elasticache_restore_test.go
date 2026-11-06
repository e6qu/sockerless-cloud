package aws_sdk_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestElastiCacheRestoreSurvivesSimulatorRestart_SDK expects a cluster being
// created from a snapshot to come back from a simulator that stopped mid-way
// holding the snapshot's data, not empty: the restarted simulator loads the
// restore the create named. The simulator is killed before the new cluster's
// container can have started, so only the restarted one can load it.
func TestElastiCacheRestoreSurvivesSimulatorRestart_SDK(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	tcpPort, udpPort := persistentSimulatorPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", tcpPort)
	cmd := startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	t.Cleanup(func() { _ = shutdownSimulator(cmd) })
	client := func() *elasticache.Client {
		return elasticache.NewFromConfig(persistentSDKConfig(), func(o *elasticache.Options) { o.BaseEndpoint = aws.String(endpoint) })
	}
	c := client()
	source, restored := fmt.Sprintf("persist-src-%d", tcpPort), fmt.Sprintf("persist-dst-%d", tcpPort)
	snapshot := fmt.Sprintf("persist-snap-%d", tcpPort)
	_, err := c.CreateCacheCluster(t.Context(), &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String(source), Engine: aws.String("redis"), CacheNodeType: aws.String("cache.t3.micro"), NumCacheNodes: aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, id := range []string{source, restored} {
			_, _ = client().DeleteCacheCluster(context.Background(), &elasticache.DeleteCacheClusterInput{CacheClusterId: aws.String(id)})
		}
	})
	cluster := ecAwaitCluster(t, c, source)
	writer := redisClient(t, &goredis.Options{Addr: ecAddress(cluster.CacheNodes[0].Endpoint)})
	require.NoError(t, writer.Set(t.Context(), "kept", "through-the-restart", 0).Err())

	_, err = c.CreateSnapshot(t.Context(), &elasticache.CreateSnapshotInput{SnapshotName: aws.String(snapshot), CacheClusterId: aws.String(source)})
	require.NoError(t, err)
	deadline := time.Now().Add(ecEngineWait)
	for {
		out, err := c.DescribeSnapshots(t.Context(), &elasticache.DescribeSnapshotsInput{SnapshotName: aws.String(snapshot)})
		require.NoError(t, err)
		require.Len(t, out.Snapshots, 1)
		if aws.ToString(out.Snapshots[0].SnapshotStatus) == "available" {
			break
		}
		require.True(t, time.Now().Before(deadline), "snapshot %s did not become available", snapshot)
		time.Sleep(time.Second)
	}
	ecAwaitCluster(t, c, source)

	_, err = c.CreateCacheCluster(t.Context(), &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String(restored), SnapshotName: aws.String(snapshot),
		CacheNodeType: aws.String("cache.t3.micro"), NumCacheNodes: aws.Int32(1),
	})
	require.NoError(t, err)
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()

	cmd = startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	c = client()
	cluster = ecAwaitCluster(t, c, restored)
	reader := redisClient(t, &goredis.Options{Addr: ecAddress(cluster.CacheNodes[0].Endpoint)})
	value, err := reader.Get(t.Context(), "kept").Result()
	require.NoError(t, err)
	assert.Equal(t, "through-the-restart", value)
}
