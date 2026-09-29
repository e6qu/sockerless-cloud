package gcp_sdk_test

import (
	"testing"

	"cloud.google.com/go/bigtable"
	adminpb "cloud.google.com/go/bigtable/admin/apiv2/adminpb"
	longrunningpb "cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// bigtableOperationMetadata unwraps an admin operation's metadata into meta —
// which fails unless the operation carries exactly that message — and requires
// operations.get to answer the same metadata under the operation's name.
func bigtableOperationMetadata[T proto.Message](t *testing.T, ops longrunningpb.OperationsClient, op *longrunningpb.Operation, err error, meta T) T {
	t.Helper()
	require.NoError(t, err)
	require.NotNil(t, op.GetMetadata(), "operation %q carries no metadata", op.GetName())
	require.NoError(t, op.GetMetadata().UnmarshalTo(meta), "operation %q carries %s", op.GetName(), op.GetMetadata().GetTypeUrl())
	fetched, err := ops.GetOperation(ctx, &longrunningpb.GetOperationRequest{Name: op.GetName()})
	require.NoError(t, err)
	stored := meta.ProtoReflect().New().Interface()
	require.NoError(t, fetched.GetMetadata().UnmarshalTo(stored))
	assert.True(t, proto.Equal(meta, stored), "operations.get answers %v, the call answered %v", stored, meta)
	return meta
}

func bigtableRequireInterval(t *testing.T, what string, start, end *timestamppb.Timestamp) {
	t.Helper()
	require.NotNil(t, start, "%s start", what)
	require.NotNil(t, end, "%s end", what)
	assert.False(t, end.AsTime().Before(start.AsTime()), "%s ends before it starts", what)
}

// TestBigtableAdminGRPC_OperationMetadata drives every Cloud Bigtable admin
// method that answers with a long-running operation and asserts that the
// operation carries the metadata message the method's operation_info declares,
// naming the request it tracks.
func TestBigtableAdminGRPC_OperationMetadata(t *testing.T) {
	t.Setenv("BIGTABLE_EMULATOR_HOST", grpcAddr)
	ia, ta, ops := bigtableAdminGRPCConn(t)
	const project = "bt-grpc-metadata"

	createInstance := &adminpb.CreateInstanceRequest{
		Parent: "projects/" + project, InstanceId: "inst",
		Instance: &adminpb.Instance{DisplayName: "inst", Type: adminpb.Instance_PRODUCTION},
		Clusters: map[string]*adminpb.Cluster{"c1": {
			Location: "projects/" + project + "/locations/us-east1-b", ServeNodes: 1, DefaultStorageType: adminpb.StorageType_SSD,
		}},
	}
	op, err := ia.CreateInstance(ctx, createInstance)
	instanceMeta := bigtableOperationMetadata(t, ops, op, err, &adminpb.CreateInstanceMetadata{})
	assert.True(t, proto.Equal(createInstance, instanceMeta.GetOriginalRequest()))
	bigtableRequireInterval(t, "CreateInstance", instanceMeta.GetRequestTime(), instanceMeta.GetFinishTime())
	instance := "projects/" + project + "/instances/inst"
	cluster := instance + "/clusters/c1"
	table := bigtableAdminGRPCTable(t, ta, instance, "events", "cf")

	client, err := bigtable.NewClient(ctx, project, "inst")
	require.NoError(t, err)
	defer client.Close()
	mut := bigtable.NewMutation()
	mut.Set("cf", "q", bigtable.Timestamp(1000), []byte("value"))
	require.NoError(t, client.Open("events").Apply(ctx, "row#1", mut))

	createCluster := &adminpb.CreateClusterRequest{
		Parent: instance, ClusterId: "c2",
		Cluster: &adminpb.Cluster{Location: "projects/" + project + "/locations/us-east1-c", ServeNodes: 1},
	}
	op, err = ia.CreateCluster(ctx, createCluster)
	clusterMeta := bigtableOperationMetadata(t, ops, op, err, &adminpb.CreateClusterMetadata{})
	assert.True(t, proto.Equal(createCluster, clusterMeta.GetOriginalRequest()))
	require.Contains(t, clusterMeta.GetTables(), table, "the table the instance holds is copied to the new cluster")
	progress := clusterMeta.GetTables()[table]
	assert.Equal(t, adminpb.CreateClusterMetadata_TableProgress_COMPLETED, progress.GetState())
	assert.Positive(t, progress.GetEstimatedSizeBytes())
	assert.Equal(t, progress.GetEstimatedSizeBytes(), progress.GetEstimatedCopiedBytes())

	partialInstance := &adminpb.PartialUpdateInstanceRequest{
		Instance: &adminpb.Instance{Name: instance, DisplayName: "renamed"}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"display_name"}},
	}
	op, err = ia.PartialUpdateInstance(ctx, partialInstance)
	assert.True(t, proto.Equal(partialInstance,
		bigtableOperationMetadata(t, ops, op, err, &adminpb.UpdateInstanceMetadata{}).GetOriginalRequest()))

	updateCluster := &adminpb.Cluster{Name: cluster, ServeNodes: 3}
	op, err = ia.UpdateCluster(ctx, updateCluster)
	assert.True(t, proto.Equal(updateCluster,
		bigtableOperationMetadata(t, ops, op, err, &adminpb.UpdateClusterMetadata{}).GetOriginalRequest()))

	partialCluster := &adminpb.PartialUpdateClusterRequest{
		Cluster: &adminpb.Cluster{Name: cluster, ServeNodes: 4}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"serve_nodes"}},
	}
	op, err = ia.PartialUpdateCluster(ctx, partialCluster)
	assert.True(t, proto.Equal(partialCluster,
		bigtableOperationMetadata(t, ops, op, err, &adminpb.PartialUpdateClusterMetadata{}).GetOriginalRequest()))

	_, err = ia.CreateAppProfile(ctx, &adminpb.CreateAppProfileRequest{
		Parent: instance, AppProfileId: "reads", AppProfile: &adminpb.AppProfile{Description: "reads"},
	})
	require.NoError(t, err)
	op, err = ia.UpdateAppProfile(ctx, &adminpb.UpdateAppProfileRequest{
		AppProfile: &adminpb.AppProfile{Name: instance + "/appProfiles/reads", Description: "all reads"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"description"}},
	})
	bigtableOperationMetadata(t, ops, op, err, &adminpb.UpdateAppProfileMetadata{})

	createLogical := &adminpb.CreateLogicalViewRequest{
		Parent: instance, LogicalViewId: "lv", LogicalView: &adminpb.LogicalView{Query: "SELECT 1"},
	}
	op, err = ia.CreateLogicalView(ctx, createLogical)
	logicalMeta := bigtableOperationMetadata(t, ops, op, err, &adminpb.CreateLogicalViewMetadata{})
	assert.True(t, proto.Equal(createLogical, logicalMeta.GetOriginalRequest()))
	bigtableRequireInterval(t, "CreateLogicalView", logicalMeta.GetStartTime(), logicalMeta.GetEndTime())
	updateLogical := &adminpb.UpdateLogicalViewRequest{
		LogicalView: &adminpb.LogicalView{Name: instance + "/logicalViews/lv", Query: "SELECT 2"},
		UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"query"}},
	}
	op, err = ia.UpdateLogicalView(ctx, updateLogical)
	assert.True(t, proto.Equal(updateLogical,
		bigtableOperationMetadata(t, ops, op, err, &adminpb.UpdateLogicalViewMetadata{}).GetOriginalRequest()))

	createMat := &adminpb.CreateMaterializedViewRequest{
		Parent: instance, MaterializedViewId: "mv", MaterializedView: &adminpb.MaterializedView{Query: "SELECT count(*)"},
	}
	op, err = ia.CreateMaterializedView(ctx, createMat)
	assert.True(t, proto.Equal(createMat,
		bigtableOperationMetadata(t, ops, op, err, &adminpb.CreateMaterializedViewMetadata{}).GetOriginalRequest()))
	updateMat := &adminpb.UpdateMaterializedViewRequest{
		MaterializedView: &adminpb.MaterializedView{Name: instance + "/materializedViews/mv", DeletionProtection: true},
		UpdateMask:       &fieldmaskpb.FieldMask{Paths: []string{"deletion_protection"}},
	}
	op, err = ia.UpdateMaterializedView(ctx, updateMat)
	assert.True(t, proto.Equal(updateMat,
		bigtableOperationMetadata(t, ops, op, err, &adminpb.UpdateMaterializedViewMetadata{}).GetOriginalRequest()))

	op, err = ta.UpdateTable(ctx, &adminpb.UpdateTableRequest{
		Table:      &adminpb.Table{Name: table, DeletionProtection: true},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"deletion_protection"}},
	})
	assert.Equal(t, table, bigtableOperationMetadata(t, ops, op, err, &adminpb.UpdateTableMetadata{}).GetName())
	op, err = ta.UndeleteTable(ctx, &adminpb.UndeleteTableRequest{Name: table})
	assert.Equal(t, table, bigtableOperationMetadata(t, ops, op, err, &adminpb.UndeleteTableMetadata{}).GetName())

	createAuth := &adminpb.CreateAuthorizedViewRequest{
		Parent: table, AuthorizedViewId: "users",
		AuthorizedView: &adminpb.AuthorizedView{AuthorizedView: &adminpb.AuthorizedView_SubsetView_{
			SubsetView: &adminpb.AuthorizedView_SubsetView{RowPrefixes: [][]byte{[]byte("user#")}},
		}},
	}
	op, err = ta.CreateAuthorizedView(ctx, createAuth)
	assert.True(t, proto.Equal(createAuth,
		bigtableOperationMetadata(t, ops, op, err, &adminpb.CreateAuthorizedViewMetadata{}).GetOriginalRequest()))
	updateAuth := &adminpb.UpdateAuthorizedViewRequest{
		AuthorizedView: &adminpb.AuthorizedView{Name: table + "/authorizedViews/users", DeletionProtection: true},
		UpdateMask:     &fieldmaskpb.FieldMask{Paths: []string{"deletion_protection"}},
	}
	op, err = ta.UpdateAuthorizedView(ctx, updateAuth)
	assert.True(t, proto.Equal(updateAuth,
		bigtableOperationMetadata(t, ops, op, err, &adminpb.UpdateAuthorizedViewMetadata{}).GetOriginalRequest()))

	bundle := table + "/schemaBundles/rows"
	op, err = ta.CreateSchemaBundle(ctx, &adminpb.CreateSchemaBundleRequest{
		Parent: table, SchemaBundleId: "rows",
		SchemaBundle: &adminpb.SchemaBundle{Type: &adminpb.SchemaBundle_ProtoSchema{
			ProtoSchema: &adminpb.ProtoSchema{ProtoDescriptors: bigtableTestDescriptorSet(t, "Row")},
		}},
	})
	assert.Equal(t, bundle, bigtableOperationMetadata(t, ops, op, err, &adminpb.CreateSchemaBundleMetadata{}).GetName())
	op, err = ta.UpdateSchemaBundle(ctx, &adminpb.UpdateSchemaBundleRequest{
		SchemaBundle: &adminpb.SchemaBundle{Name: bundle, Type: &adminpb.SchemaBundle_ProtoSchema{
			ProtoSchema: &adminpb.ProtoSchema{ProtoDescriptors: bigtableTestDescriptorSet(t, "RowV2")},
		}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"proto_schema"}},
	})
	assert.Equal(t, bundle, bigtableOperationMetadata(t, ops, op, err, &adminpb.UpdateSchemaBundleMetadata{}).GetName())

	snapshot := &adminpb.SnapshotTableRequest{Name: table, Cluster: cluster, SnapshotId: "nightly"}
	op, err = ta.SnapshotTable(ctx, snapshot)
	assert.True(t, proto.Equal(snapshot,
		bigtableOperationMetadata(t, ops, op, err, &adminpb.SnapshotTableMetadata{}).GetOriginalRequest()))
	fromSnapshot := &adminpb.CreateTableFromSnapshotRequest{
		Parent: instance, TableId: "from-snapshot", SourceSnapshot: cluster + "/snapshots/nightly",
	}
	op, err = ta.CreateTableFromSnapshot(ctx, fromSnapshot)
	assert.True(t, proto.Equal(fromSnapshot,
		bigtableOperationMetadata(t, ops, op, err, &adminpb.CreateTableFromSnapshotMetadata{}).GetOriginalRequest()))

	backup := cluster + "/backups/b1"
	op, err = ta.CreateBackup(ctx, &adminpb.CreateBackupRequest{
		Parent: cluster, BackupId: "b1", Backup: &adminpb.Backup{SourceTable: table, ExpireTime: timestamppb.Now()},
	})
	backupMeta := bigtableOperationMetadata(t, ops, op, err, &adminpb.CreateBackupMetadata{})
	assert.Equal(t, backup, backupMeta.GetName())
	assert.Equal(t, table, backupMeta.GetSourceTable())
	bigtableRequireInterval(t, "CreateBackup", backupMeta.GetStartTime(), backupMeta.GetEndTime())

	op, err = ta.CopyBackup(ctx, &adminpb.CopyBackupRequest{
		Parent: cluster, BackupId: "b2", SourceBackup: backup, ExpireTime: timestamppb.Now(),
	})
	copyMeta := bigtableOperationMetadata(t, ops, op, err, &adminpb.CopyBackupMetadata{})
	assert.Equal(t, cluster+"/backups/b2", copyMeta.GetName())
	assert.Equal(t, backup, copyMeta.GetSourceBackupInfo().GetBackup())
	assert.Equal(t, table, copyMeta.GetSourceBackupInfo().GetSourceTable())
	assert.EqualValues(t, 100, copyMeta.GetProgress().GetProgressPercent())

	op, err = ta.RestoreTable(ctx, &adminpb.RestoreTableRequest{
		Parent: instance, TableId: "restored", Source: &adminpb.RestoreTableRequest_Backup{Backup: backup},
	})
	restoreMeta := bigtableOperationMetadata(t, ops, op, err, &adminpb.RestoreTableMetadata{})
	assert.Equal(t, instance+"/tables/restored", restoreMeta.GetName())
	assert.Equal(t, adminpb.RestoreSourceType_BACKUP, restoreMeta.GetSourceType())
	assert.Equal(t, backup, restoreMeta.GetBackupInfo().GetBackup())
	assert.EqualValues(t, 100, restoreMeta.GetProgress().GetProgressPercent())
}
