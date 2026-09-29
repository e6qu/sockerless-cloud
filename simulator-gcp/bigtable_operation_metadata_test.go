package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	btadmin "cloud.google.com/go/bigtable/admin/apiv2/adminpb"
	btpb "cloud.google.com/go/bigtable/apiv2/bigtablepb"
	longrunningpb "cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// btOperationMetadata unwraps an admin operation's metadata into meta, which
// fails unless the operation carries exactly that message, and requires
// operations.get to answer the same metadata for the operation's name.
func btOperationMetadata[T proto.Message](t *testing.T, op *longrunningpb.Operation, err error, meta T) T {
	t.Helper()
	if err != nil {
		t.Fatalf("admin call: %v", err)
	}
	if op.GetMetadata() == nil {
		t.Fatalf("operation %s carries no metadata", op.GetName())
	}
	if err := op.GetMetadata().UnmarshalTo(meta); err != nil {
		t.Fatalf("operation %s carries %s, want %s: %v", op.GetName(), op.GetMetadata().GetTypeUrl(),
			meta.ProtoReflect().Descriptor().FullName(), err)
	}
	fetched, err := (&grpcOperationsService{}).GetOperation(context.Background(),
		&longrunningpb.GetOperationRequest{Name: op.GetName()})
	if err != nil {
		t.Fatalf("get operation %s: %v", op.GetName(), err)
	}
	stored := meta.ProtoReflect().New().Interface()
	if fetched.GetMetadata() == nil || fetched.GetMetadata().UnmarshalTo(stored) != nil || !proto.Equal(stored, meta) {
		t.Fatalf("operations.get answers metadata %v for %s, the call answered %v", fetched.GetMetadata(), op.GetName(), meta)
	}
	return meta
}

func btRequireInterval(t *testing.T, what string, start, end *timestamppb.Timestamp) {
	t.Helper()
	if start == nil || end == nil || end.AsTime().Before(start.AsTime()) {
		t.Fatalf("%s: start %v, end %v", what, start, end)
	}
}

func btRequireOriginal(t *testing.T, what string, got, want proto.Message) {
	t.Helper()
	if !proto.Equal(got, want) {
		t.Fatalf("%s: original_request = %v, want %v", what, got, want)
	}
}

// Every Cloud Bigtable admin method that answers with a long-running operation
// carries the metadata message its operation_info declares, filled with the
// fields that message defines, and operations.get answers the same metadata.
func TestBigtableAdminOperationsCarryTheirDeclaredMetadata(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	ctx := context.Background()
	ia, ta := &bigtableInstanceAdminGRPC{}, &bigtableTableAdminGRPC{}
	const project = "bt-metadata"
	instance := "projects/" + project + "/instances/inst"
	cluster := instance + "/clusters/c1"

	createInstance := &btadmin.CreateInstanceRequest{
		Parent: "projects/" + project, InstanceId: "inst",
		Instance: &btadmin.Instance{DisplayName: "inst", Type: btadmin.Instance_PRODUCTION},
		Clusters: map[string]*btadmin.Cluster{"c1": {ServeNodes: 1, DefaultStorageType: btadmin.StorageType_SSD}},
	}
	op, err := ia.CreateInstance(ctx, createInstance)
	instanceMeta := btOperationMetadata(t, op, err, &btadmin.CreateInstanceMetadata{})
	btRequireOriginal(t, "CreateInstance", instanceMeta.GetOriginalRequest(), createInstance)
	btRequireInterval(t, "CreateInstance", instanceMeta.GetRequestTime(), instanceMeta.GetFinishTime())

	table, err := ta.CreateTable(ctx, &btadmin.CreateTableRequest{
		Parent: instance, TableId: "events",
		Table: &btadmin.Table{ColumnFamilies: map[string]*btadmin.ColumnFamily{"cf": {}}},
	})
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := (&bigtableDataGRPC{}).MutateRow(ctx, &btpb.MutateRowRequest{
		TableName: table.GetName(), RowKey: []byte("row#1"),
		Mutations: []*btpb.Mutation{{Mutation: &btpb.Mutation_SetCell_{SetCell: &btpb.Mutation_SetCell{
			FamilyName: "cf", ColumnQualifier: []byte("q"), TimestampMicros: 1000, Value: []byte("value"),
		}}}},
	}); err != nil {
		t.Fatalf("mutate row: %v", err)
	}
	// row key 5 + family 2 + qualifier 1 + timestamp 8 + value 5.
	const tableBytes = 21

	createCluster := &btadmin.CreateClusterRequest{
		Parent: instance, ClusterId: "c2",
		Cluster: &btadmin.Cluster{ServeNodes: 2, DefaultStorageType: btadmin.StorageType_HDD},
	}
	op, err = ia.CreateCluster(ctx, createCluster)
	clusterMeta := btOperationMetadata(t, op, err, &btadmin.CreateClusterMetadata{})
	btRequireOriginal(t, "CreateCluster", clusterMeta.GetOriginalRequest(), createCluster)
	btRequireInterval(t, "CreateCluster", clusterMeta.GetRequestTime(), clusterMeta.GetFinishTime())
	progress := clusterMeta.GetTables()[table.GetName()]
	if len(clusterMeta.GetTables()) != 1 || progress.GetState() != btadmin.CreateClusterMetadata_TableProgress_COMPLETED ||
		progress.GetEstimatedSizeBytes() != tableBytes || progress.GetEstimatedCopiedBytes() != tableBytes {
		t.Fatalf("CreateCluster tables = %v, want %s COMPLETED at %d bytes", clusterMeta.GetTables(), table.GetName(), tableBytes)
	}

	partialInstance := &btadmin.PartialUpdateInstanceRequest{
		Instance:   &btadmin.Instance{Name: instance, DisplayName: "renamed"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"display_name"}},
	}
	op, err = ia.PartialUpdateInstance(ctx, partialInstance)
	btRequireOriginal(t, "PartialUpdateInstance",
		btOperationMetadata(t, op, err, &btadmin.UpdateInstanceMetadata{}).GetOriginalRequest(), partialInstance)

	updateCluster := &btadmin.Cluster{Name: cluster, ServeNodes: 3}
	op, err = ia.UpdateCluster(ctx, updateCluster)
	btRequireOriginal(t, "UpdateCluster",
		btOperationMetadata(t, op, err, &btadmin.UpdateClusterMetadata{}).GetOriginalRequest(), updateCluster)

	partialCluster := &btadmin.PartialUpdateClusterRequest{
		Cluster:    &btadmin.Cluster{Name: cluster, ServeNodes: 4},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"serve_nodes"}},
	}
	op, err = ia.PartialUpdateCluster(ctx, partialCluster)
	btRequireOriginal(t, "PartialUpdateCluster",
		btOperationMetadata(t, op, err, &btadmin.PartialUpdateClusterMetadata{}).GetOriginalRequest(), partialCluster)

	if _, err := ia.CreateAppProfile(ctx, &btadmin.CreateAppProfileRequest{
		Parent: instance, AppProfileId: "reads", AppProfile: &btadmin.AppProfile{Description: "reads"},
	}); err != nil {
		t.Fatalf("create app profile: %v", err)
	}
	op, err = ia.UpdateAppProfile(ctx, &btadmin.UpdateAppProfileRequest{
		AppProfile: &btadmin.AppProfile{Name: instance + "/appProfiles/reads", Description: "all reads"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"description"}},
	})
	btOperationMetadata(t, op, err, &btadmin.UpdateAppProfileMetadata{})

	createLogical := &btadmin.CreateLogicalViewRequest{
		Parent: instance, LogicalViewId: "lv", LogicalView: &btadmin.LogicalView{Query: "SELECT 1"},
	}
	op, err = ia.CreateLogicalView(ctx, createLogical)
	logicalMeta := btOperationMetadata(t, op, err, &btadmin.CreateLogicalViewMetadata{})
	btRequireOriginal(t, "CreateLogicalView", logicalMeta.GetOriginalRequest(), createLogical)
	btRequireInterval(t, "CreateLogicalView", logicalMeta.GetStartTime(), logicalMeta.GetEndTime())
	updateLogical := &btadmin.UpdateLogicalViewRequest{
		LogicalView: &btadmin.LogicalView{Name: instance + "/logicalViews/lv", Query: "SELECT 2"},
		UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"query"}},
	}
	op, err = ia.UpdateLogicalView(ctx, updateLogical)
	btRequireOriginal(t, "UpdateLogicalView",
		btOperationMetadata(t, op, err, &btadmin.UpdateLogicalViewMetadata{}).GetOriginalRequest(), updateLogical)

	createMat := &btadmin.CreateMaterializedViewRequest{
		Parent: instance, MaterializedViewId: "mv", MaterializedView: &btadmin.MaterializedView{Query: "SELECT count(*)"},
	}
	op, err = ia.CreateMaterializedView(ctx, createMat)
	btRequireOriginal(t, "CreateMaterializedView",
		btOperationMetadata(t, op, err, &btadmin.CreateMaterializedViewMetadata{}).GetOriginalRequest(), createMat)
	updateMat := &btadmin.UpdateMaterializedViewRequest{
		MaterializedView: &btadmin.MaterializedView{Name: instance + "/materializedViews/mv", DeletionProtection: true},
		UpdateMask:       &fieldmaskpb.FieldMask{Paths: []string{"deletion_protection"}},
	}
	op, err = ia.UpdateMaterializedView(ctx, updateMat)
	btRequireOriginal(t, "UpdateMaterializedView",
		btOperationMetadata(t, op, err, &btadmin.UpdateMaterializedViewMetadata{}).GetOriginalRequest(), updateMat)

	op, err = ta.UpdateTable(ctx, &btadmin.UpdateTableRequest{
		Table:      &btadmin.Table{Name: table.GetName(), DeletionProtection: true},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"deletion_protection"}},
	})
	updateTableMeta := btOperationMetadata(t, op, err, &btadmin.UpdateTableMetadata{})
	if updateTableMeta.GetName() != table.GetName() {
		t.Fatalf("UpdateTable metadata names %q", updateTableMeta.GetName())
	}
	btRequireInterval(t, "UpdateTable", updateTableMeta.GetStartTime(), updateTableMeta.GetEndTime())

	op, err = ta.UndeleteTable(ctx, &btadmin.UndeleteTableRequest{Name: table.GetName()})
	if got := btOperationMetadata(t, op, err, &btadmin.UndeleteTableMetadata{}).GetName(); got != table.GetName() {
		t.Fatalf("UndeleteTable metadata names %q", got)
	}

	createAuth := &btadmin.CreateAuthorizedViewRequest{
		Parent: table.GetName(), AuthorizedViewId: "users",
		AuthorizedView: &btadmin.AuthorizedView{AuthorizedView: &btadmin.AuthorizedView_SubsetView_{
			SubsetView: &btadmin.AuthorizedView_SubsetView{RowPrefixes: [][]byte{[]byte("user#")}},
		}},
	}
	op, err = ta.CreateAuthorizedView(ctx, createAuth)
	btRequireOriginal(t, "CreateAuthorizedView",
		btOperationMetadata(t, op, err, &btadmin.CreateAuthorizedViewMetadata{}).GetOriginalRequest(), createAuth)
	updateAuth := &btadmin.UpdateAuthorizedViewRequest{
		AuthorizedView: &btadmin.AuthorizedView{Name: table.GetName() + "/authorizedViews/users", DeletionProtection: true},
		UpdateMask:     &fieldmaskpb.FieldMask{Paths: []string{"deletion_protection"}},
	}
	op, err = ta.UpdateAuthorizedView(ctx, updateAuth)
	btRequireOriginal(t, "UpdateAuthorizedView",
		btOperationMetadata(t, op, err, &btadmin.UpdateAuthorizedViewMetadata{}).GetOriginalRequest(), updateAuth)

	op, err = ta.CreateSchemaBundle(ctx, &btadmin.CreateSchemaBundleRequest{
		Parent: table.GetName(), SchemaBundleId: "rows",
		SchemaBundle: &btadmin.SchemaBundle{Type: &btadmin.SchemaBundle_ProtoSchema{
			ProtoSchema: &btadmin.ProtoSchema{ProtoDescriptors: []byte{}},
		}},
	})
	bundle := table.GetName() + "/schemaBundles/rows"
	if got := btOperationMetadata(t, op, err, &btadmin.CreateSchemaBundleMetadata{}).GetName(); got != bundle {
		t.Fatalf("CreateSchemaBundle metadata names %q", got)
	}
	op, err = ta.UpdateSchemaBundle(ctx, &btadmin.UpdateSchemaBundleRequest{
		SchemaBundle: &btadmin.SchemaBundle{Name: bundle, Type: &btadmin.SchemaBundle_ProtoSchema{
			ProtoSchema: &btadmin.ProtoSchema{ProtoDescriptors: []byte{}},
		}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"proto_schema"}},
	})
	if got := btOperationMetadata(t, op, err, &btadmin.UpdateSchemaBundleMetadata{}).GetName(); got != bundle {
		t.Fatalf("UpdateSchemaBundle metadata names %q", got)
	}

	snapshot := &btadmin.SnapshotTableRequest{Name: table.GetName(), Cluster: cluster, SnapshotId: "nightly"}
	op, err = ta.SnapshotTable(ctx, snapshot)
	btRequireOriginal(t, "SnapshotTable",
		btOperationMetadata(t, op, err, &btadmin.SnapshotTableMetadata{}).GetOriginalRequest(), snapshot)
	// data_size_bytes counts the bytes of the data captured, not its rows.
	snapshotted := &btadmin.Snapshot{}
	if err := op.GetResponse().UnmarshalTo(snapshotted); err != nil || snapshotted.GetDataSizeBytes() != tableBytes {
		t.Fatalf("SnapshotTable response data_size_bytes = %d (%v), want %d", snapshotted.GetDataSizeBytes(), err, tableBytes)
	}
	readBack, err := ta.GetSnapshot(ctx, &btadmin.GetSnapshotRequest{Name: cluster + "/snapshots/nightly"})
	if err != nil || readBack.GetDataSizeBytes() != tableBytes {
		t.Fatalf("GetSnapshot data_size_bytes = %d (%v), want %d", readBack.GetDataSizeBytes(), err, tableBytes)
	}
	fromSnapshot := &btadmin.CreateTableFromSnapshotRequest{
		Parent: instance, TableId: "from-snapshot", SourceSnapshot: cluster + "/snapshots/nightly",
	}
	op, err = ta.CreateTableFromSnapshot(ctx, fromSnapshot)
	btRequireOriginal(t, "CreateTableFromSnapshot",
		btOperationMetadata(t, op, err, &btadmin.CreateTableFromSnapshotMetadata{}).GetOriginalRequest(), fromSnapshot)

	op, err = ta.CreateBackup(ctx, &btadmin.CreateBackupRequest{
		Parent: cluster, BackupId: "b1", Backup: &btadmin.Backup{SourceTable: table.GetName()},
	})
	backupMeta := btOperationMetadata(t, op, err, &btadmin.CreateBackupMetadata{})
	if backupMeta.GetName() != cluster+"/backups/b1" || backupMeta.GetSourceTable() != table.GetName() {
		t.Fatalf("CreateBackup metadata = %v", backupMeta)
	}
	btRequireInterval(t, "CreateBackup", backupMeta.GetStartTime(), backupMeta.GetEndTime())
	backup, err := ta.GetBackup(ctx, &btadmin.GetBackupRequest{Name: cluster + "/backups/b1"})
	if err != nil {
		t.Fatalf("get backup: %v", err)
	}
	if backup.GetSizeBytes() != tableBytes {
		t.Fatalf("backup size_bytes = %d, want %d", backup.GetSizeBytes(), tableBytes)
	}
	if !proto.Equal(backup.GetStartTime(), backupMeta.GetStartTime()) || !proto.Equal(backup.GetEndTime(), backupMeta.GetEndTime()) {
		t.Fatalf("backup times %v..%v differ from its operation's %v..%v",
			backup.GetStartTime(), backup.GetEndTime(), backupMeta.GetStartTime(), backupMeta.GetEndTime())
	}

	op, err = ta.CopyBackup(ctx, &btadmin.CopyBackupRequest{
		Parent: cluster, BackupId: "b2", SourceBackup: cluster + "/backups/b1", ExpireTime: timestamppb.Now(),
	})
	copyMeta := btOperationMetadata(t, op, err, &btadmin.CopyBackupMetadata{})
	if copyMeta.GetName() != cluster+"/backups/b2" || copyMeta.GetSourceBackupInfo().GetBackup() != cluster+"/backups/b1" ||
		copyMeta.GetSourceBackupInfo().GetSourceTable() != table.GetName() || copyMeta.GetProgress().GetProgressPercent() != 100 {
		t.Fatalf("CopyBackup metadata = %v", copyMeta)
	}
	btRequireInterval(t, "CopyBackup source", copyMeta.GetSourceBackupInfo().GetStartTime(), copyMeta.GetSourceBackupInfo().GetEndTime())

	op, err = ta.RestoreTable(ctx, &btadmin.RestoreTableRequest{
		Parent: instance, TableId: "restored", Source: &btadmin.RestoreTableRequest_Backup{Backup: cluster + "/backups/b1"},
	})
	restoreMeta := btOperationMetadata(t, op, err, &btadmin.RestoreTableMetadata{})
	if restoreMeta.GetName() != instance+"/tables/restored" || restoreMeta.GetSourceType() != btadmin.RestoreSourceType_BACKUP ||
		restoreMeta.GetBackupInfo().GetBackup() != cluster+"/backups/b1" || restoreMeta.GetProgress().GetProgressPercent() != 100 {
		t.Fatalf("RestoreTable metadata = %v", restoreMeta)
	}
	btRequireInterval(t, "RestoreTable progress", restoreMeta.GetProgress().GetStartTime(), restoreMeta.GetProgress().GetEndTime())

	layer, err := ia.GetMemoryLayer(ctx, &btadmin.GetMemoryLayerRequest{Name: cluster + "/memoryLayer"})
	if err != nil {
		t.Fatalf("get memory layer: %v", err)
	}
	updateLayer := &btadmin.UpdateMemoryLayerRequest{
		MemoryLayer: &btadmin.MemoryLayer{Name: layer.GetName(), MemoryConfig: &btadmin.MemoryLayer_MemoryConfig{}, Etag: layer.GetEtag()},
		UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"memory_config"}},
	}
	op, err = ia.UpdateMemoryLayer(ctx, updateLayer)
	btRequireOriginal(t, "UpdateMemoryLayer",
		btOperationMetadata(t, op, err, &btadmin.UpdateMemoryLayerMetadata{}).GetOriginalRequest(), updateLayer)

	// The REST door records the same messages, so an operation it started
	// reads back over gRPC with its declared metadata.
	rest := func(method, path, body string) *longrunningpb.Operation {
		t.Helper()
		req := httptest.NewRequest(method, "http://bigtableadmin.googleapis.com"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body)
		}
		op := &longrunningpb.Operation{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(rec.Body.Bytes(), op); err != nil {
			t.Fatalf("decode %s %s operation: %v: %s", method, path, err, rec.Body)
		}
		return op
	}
	restInstance := btOperationMetadata(t,
		rest(http.MethodPost, "/v2/projects/"+project+"/instances",
			`{"instanceId":"rest-inst","instance":{"displayName":"rest"},"clusters":{"rc":{"serveNodes":1}}}`),
		nil, &btadmin.CreateInstanceMetadata{})
	if restInstance.GetOriginalRequest().GetInstanceId() != "rest-inst" ||
		restInstance.GetOriginalRequest().GetParent() != "projects/"+project ||
		restInstance.GetOriginalRequest().GetClusters()["rc"].GetServeNodes() != 1 {
		t.Fatalf("REST CreateInstance original_request = %v", restInstance.GetOriginalRequest())
	}
	restCluster := btOperationMetadata(t,
		rest(http.MethodPost, "/v2/projects/"+project+"/instances/inst/clusters?clusterId=rc2", `{"serveNodes":2}`),
		nil, &btadmin.CreateClusterMetadata{})
	if restCluster.GetOriginalRequest().GetClusterId() != "rc2" ||
		restCluster.GetTables()[table.GetName()].GetEstimatedSizeBytes() != tableBytes {
		t.Fatalf("REST CreateCluster metadata = %v", restCluster)
	}
	restBackup := btOperationMetadata(t,
		rest(http.MethodPost, "/v2/projects/"+project+"/instances/inst/clusters/c1/backups?backupId=rb",
			`{"sourceTable":"`+table.GetName()+`"}`),
		nil, &btadmin.CreateBackupMetadata{})
	if restBackup.GetName() != cluster+"/backups/rb" || restBackup.GetSourceTable() != table.GetName() {
		t.Fatalf("REST CreateBackup metadata = %v", restBackup)
	}
	restView := btOperationMetadata(t,
		rest(http.MethodPost, "/v2/projects/"+project+"/instances/inst/logicalViews?logicalViewId=rlv", `{"query":"SELECT 3"}`),
		nil, &btadmin.CreateLogicalViewMetadata{})
	if restView.GetOriginalRequest().GetLogicalView().GetQuery() != "SELECT 3" || restView.GetOriginalRequest().GetLogicalViewId() != "rlv" {
		t.Fatalf("REST CreateLogicalView original_request = %v", restView.GetOriginalRequest())
	}
}
