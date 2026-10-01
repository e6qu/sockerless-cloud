package gcp_sdk_test

import (
	"testing"

	"cloud.google.com/go/apigateway/apiv1/apigatewaypb"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	longrunningpb "cloud.google.com/go/longrunning/autogen/longrunningpb"
	"cloud.google.com/go/redis/apiv1/redispb"
	"cloud.google.com/go/redis/cluster/apiv1/clusterpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apigateway "google.golang.org/api/apigateway/v1"
	artifactregistry "google.golang.org/api/artifactregistry/v1"
	redis "google.golang.org/api/redis/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// grpcOperation reads an operation over the google.longrunning.Operations gRPC
// service and unwraps its response and metadata into the messages the method's
// operation_info declares — which fails unless the operation carries exactly
// those.
func grpcOperation(t *testing.T, ops longrunningpb.OperationsClient, name string, response, metadata proto.Message) {
	t.Helper()
	op, err := ops.GetOperation(ctx, &longrunningpb.GetOperationRequest{Name: name})
	require.NoError(t, err, "operations.get %s over gRPC", name)
	require.True(t, op.GetDone())
	require.NoError(t, op.GetResponse().UnmarshalTo(response), "response is %s", op.GetResponse().GetTypeUrl())
	require.NoError(t, op.GetMetadata().UnmarshalTo(metadata), "metadata is %s", op.GetMetadata().GetTypeUrl())
}

// TestOperations_RESTServiceOperationsReadOverGRPC starts operations through
// the REST surfaces of Memorystore for Redis, Memorystore for Redis Cluster,
// Artifact Registry and API Gateway, and reads each back over gRPC with the
// response and metadata messages its method declares — naming the method it
// ran and the resource it acted on, for deletes too.
func TestOperations_RESTServiceOperationsReadOverGRPC(t *testing.T) {
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	ops := longrunningpb.NewOperationsClient(conn)
	const parent = "projects/ops-grpc-project/locations/us-central1"

	redisSvc := redisService(t)
	instanceName := parent + "/instances/grpc-cache"
	op, err := redisSvc.Projects.Locations.Instances.Create(parent, &redis.Instance{Tier: "BASIC", MemorySizeGb: 1}).
		InstanceId("grpc-cache").Do()
	require.NoError(t, err)
	instance, instanceMeta := &redispb.Instance{}, &redispb.OperationMetadata{}
	grpcOperation(t, ops, op.Name, instance, instanceMeta)
	assert.Equal(t, instanceName, instance.GetName())
	assert.Equal(t, "create", instanceMeta.GetVerb())
	assert.Equal(t, instanceName, instanceMeta.GetTarget())

	op, err = redisSvc.Projects.Locations.Instances.Delete(instanceName).Do()
	require.NoError(t, err)
	instanceMeta = &redispb.OperationMetadata{}
	grpcOperation(t, ops, op.Name, &emptypb.Empty{}, instanceMeta)
	assert.Equal(t, "delete", instanceMeta.GetVerb())
	assert.Equal(t, instanceName, instanceMeta.GetTarget(), "a delete names the resource it removed")

	clusterName := parent + "/clusters/grpc-cluster"
	op, err = redisSvc.Projects.Locations.Clusters.Create(parent, &redis.Cluster{ShardCount: 1}).ClusterId("grpc-cluster").Do()
	require.NoError(t, err)
	cluster, clusterMeta := &clusterpb.Cluster{}, &clusterpb.OperationMetadata{}
	grpcOperation(t, ops, op.Name, cluster, clusterMeta)
	assert.Equal(t, clusterName, cluster.GetName())
	assert.Equal(t, int32(1), cluster.GetShardCount())
	assert.Equal(t, "create", clusterMeta.GetVerb())
	assert.Equal(t, clusterName, clusterMeta.GetTarget())

	op, err = redisSvc.Projects.Locations.Clusters.Backup(clusterName, &redis.BackupClusterRequest{BackupId: "grpc-backup"}).Do()
	require.NoError(t, err)
	clusterMeta = &clusterpb.OperationMetadata{}
	grpcOperation(t, ops, op.Name, &clusterpb.Cluster{}, clusterMeta)
	assert.Equal(t, "backup", clusterMeta.GetVerb(), "a custom method is its own verb")
	assert.Equal(t, clusterName, clusterMeta.GetTarget())

	gcs := storageClient(t)
	t.Cleanup(func() { gcs.Close() })
	exportBucket := uniqueName("grpc-backup-export")
	require.NoError(t, gcs.Bucket(exportBucket).Create(ctx, "ops-grpc-project", nil))
	op, err = redisSvc.Projects.Locations.BackupCollections.Backups.Export(
		parent+"/backupCollections/grpc-cluster/backups/grpc-backup",
		&redis.ExportBackupRequest{GcsBucket: exportBucket}).Do()
	require.NoError(t, err)
	backup := &clusterpb.Backup{}
	grpcOperation(t, ops, op.Name, backup, &clusterpb.OperationMetadata{})
	assert.Equal(t, clusterName, backup.GetCluster(), "the exported backup names its cluster")

	op, err = redisSvc.Projects.Locations.Clusters.Delete(clusterName).Do()
	require.NoError(t, err)
	clusterMeta = &clusterpb.OperationMetadata{}
	grpcOperation(t, ops, op.Name, &emptypb.Empty{}, clusterMeta)
	assert.Equal(t, "delete", clusterMeta.GetVerb())
	assert.Equal(t, clusterName, clusterMeta.GetTarget())

	arSvc := arAdminService(t)
	repoName := parent + "/repositories/grpc-repo"
	arOp, err := arSvc.Projects.Locations.Repositories.Create(parent, &artifactregistry.Repository{Format: "DOCKER"}).
		RepositoryId("grpc-repo").Do()
	require.NoError(t, err)
	repo := &artifactregistrypb.Repository{}
	grpcOperation(t, ops, arOp.Name, repo, &artifactregistrypb.OperationMetadata{})
	assert.Equal(t, repoName, repo.GetName())
	assert.Equal(t, artifactregistrypb.Repository_DOCKER, repo.GetFormat())

	arOp, err = arSvc.Projects.Locations.Repositories.Delete(repoName).Do()
	require.NoError(t, err)
	grpcOperation(t, ops, arOp.Name, &emptypb.Empty{}, &artifactregistrypb.OperationMetadata{})

	gwSvc := apigatewayService(t)
	apiName := "projects/ops-grpc-project/locations/global/apis/grpc-api"
	gwOp, err := gwSvc.Projects.Locations.Apis.Create("projects/ops-grpc-project/locations/global", &apigateway.ApigatewayApi{
		DisplayName: "grpc api",
	}).ApiId("grpc-api").Do()
	require.NoError(t, err)
	api, apiMeta := &apigatewaypb.Api{}, &apigatewaypb.OperationMetadata{}
	grpcOperation(t, ops, gwOp.Name, api, apiMeta)
	assert.Equal(t, apiName, api.GetName())
	assert.Equal(t, "grpc api", api.GetDisplayName())
	assert.Equal(t, "create", apiMeta.GetVerb())
	assert.Equal(t, apiName, apiMeta.GetTarget())

	gwOp, err = gwSvc.Projects.Locations.Apis.Delete(apiName).Do()
	require.NoError(t, err)
	apiMeta = &apigatewaypb.OperationMetadata{}
	grpcOperation(t, ops, gwOp.Name, &emptypb.Empty{}, apiMeta)
	assert.Equal(t, "delete", apiMeta.GetVerb())
	assert.Equal(t, apiName, apiMeta.GetTarget())
}
