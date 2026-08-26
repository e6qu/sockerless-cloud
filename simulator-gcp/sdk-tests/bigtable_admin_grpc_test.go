package gcp_sdk_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigtable"
	adminpb "cloud.google.com/go/bigtable/admin/apiv2/adminpb"
	iampb "cloud.google.com/go/iam/apiv1/iampb"
	longrunningpb "cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The Cloud Bigtable admin surface over gRPC: app profiles, logical and
// materialized views, backups, authorized views, schema bundles, the
// consistency-token pair, the instance and cluster updates and the IAM
// triples, driven through the generated google.bigtable.admin.v2 clients and
// the google.longrunning.Operations client against the simulator's gRPC port.
//
// The same resources are served over REST from the same stores, so these tests
// assert behaviour rather than absence of error: a created resource appears in
// its listing, an update is what reads back, a delete removes it, an IAM
// policy round-trips, and a name that does not exist is a real NotFound.

// bigtableAdminGRPCConn dials the simulator's gRPC port and returns the three
// generated clients the Cloud Bigtable admin surface is reached through.
func bigtableAdminGRPCConn(t *testing.T) (adminpb.BigtableInstanceAdminClient, adminpb.BigtableTableAdminClient, longrunningpb.OperationsClient) {
	t.Helper()
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return adminpb.NewBigtableInstanceAdminClient(conn),
		adminpb.NewBigtableTableAdminClient(conn),
		longrunningpb.NewOperationsClient(conn)
}

// bigtableAdminGRPCInstance provisions an instance holding one cluster and
// returns the instance and cluster resource names.
func bigtableAdminGRPCInstance(t *testing.T, ia adminpb.BigtableInstanceAdminClient, project, instanceID, clusterID string) (string, string) {
	t.Helper()
	op, err := ia.CreateInstance(ctx, &adminpb.CreateInstanceRequest{
		Parent:     "projects/" + project,
		InstanceId: instanceID,
		Instance:   &adminpb.Instance{DisplayName: instanceID, Type: adminpb.Instance_PRODUCTION},
		Clusters: map[string]*adminpb.Cluster{
			clusterID: {
				Location:           fmt.Sprintf("projects/%s/locations/us-east1-b", project),
				ServeNodes:         1,
				DefaultStorageType: adminpb.StorageType_SSD,
			},
		},
	})
	require.NoError(t, err)
	require.True(t, op.GetDone(), "CreateInstance operation must be complete")
	instance := fmt.Sprintf("projects/%s/instances/%s", project, instanceID)
	return instance, instance + "/clusters/" + clusterID
}

// bigtableAdminGRPCTable creates a table with one column family and returns its
// resource name.
func bigtableAdminGRPCTable(t *testing.T, ta adminpb.BigtableTableAdminClient, instance, tableID, family string) string {
	t.Helper()
	table, err := ta.CreateTable(ctx, &adminpb.CreateTableRequest{
		Parent:  instance,
		TableId: tableID,
		Table:   &adminpb.Table{ColumnFamilies: map[string]*adminpb.ColumnFamily{family: {}}},
	})
	require.NoError(t, err)
	return table.GetName()
}

// bigtableLROResource unwraps a completed long-running operation's response
// into msg, proving the operation carries the resource it created.
func bigtableLROResource[T proto.Message](t *testing.T, op *longrunningpb.Operation, msg T) T {
	t.Helper()
	require.True(t, op.GetDone(), "operation %q must be complete", op.GetName())
	require.Nil(t, op.GetError(), "operation %q must not carry an error", op.GetName())
	require.NotNil(t, op.GetResponse(), "operation %q must carry its resource", op.GetName())
	require.NoError(t, op.GetResponse().UnmarshalTo(msg))
	return msg
}

func TestBigtableAdminGRPC_AppProfiles(t *testing.T) {
	ia, _, _ := bigtableAdminGRPCConn(t)
	instance, _ := bigtableAdminGRPCInstance(t, ia, "bt-grpc-profiles", "inst", "c1")

	created, err := ia.CreateAppProfile(ctx, &adminpb.CreateAppProfileRequest{
		Parent:       instance,
		AppProfileId: "reads",
		AppProfile: &adminpb.AppProfile{
			Description: "read traffic",
			RoutingPolicy: &adminpb.AppProfile_MultiClusterRoutingUseAny_{
				MultiClusterRoutingUseAny: &adminpb.AppProfile_MultiClusterRoutingUseAny{},
			},
		},
	})
	require.NoError(t, err)
	name := instance + "/appProfiles/reads"
	assert.Equal(t, name, created.GetName())
	assert.Equal(t, "read traffic", created.GetDescription())
	assert.NotEmpty(t, created.GetEtag(), "the server assigns an etag")
	require.NotNil(t, created.GetMultiClusterRoutingUseAny())

	// A second create under the same id is a conflict.
	_, err = ia.CreateAppProfile(ctx, &adminpb.CreateAppProfileRequest{
		Parent: instance, AppProfileId: "reads",
		AppProfile: &adminpb.AppProfile{Description: "again"},
	})
	requireGRPCCode(t, err, codes.AlreadyExists)

	got, err := ia.GetAppProfile(ctx, &adminpb.GetAppProfileRequest{Name: name})
	require.NoError(t, err)
	assert.Equal(t, created.GetDescription(), got.GetDescription())
	assert.Equal(t, created.GetEtag(), got.GetEtag())

	list, err := ia.ListAppProfiles(ctx, &adminpb.ListAppProfilesRequest{Parent: instance})
	require.NoError(t, err)
	require.Len(t, list.GetAppProfiles(), 1)
	assert.Equal(t, name, list.GetAppProfiles()[0].GetName())

	// The update replaces the routing policy: the two members of the oneof
	// cannot both survive, so a read must report single-cluster routing only.
	op, err := ia.UpdateAppProfile(ctx, &adminpb.UpdateAppProfileRequest{
		AppProfile: &adminpb.AppProfile{
			Name:        name,
			Description: "single cluster reads",
			RoutingPolicy: &adminpb.AppProfile_SingleClusterRouting_{
				SingleClusterRouting: &adminpb.AppProfile_SingleClusterRouting{ClusterId: "c1"},
			},
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"description", "single_cluster_routing"}},
	})
	require.NoError(t, err)
	updated := bigtableLROResource(t, op, &adminpb.AppProfile{})
	assert.Equal(t, "single cluster reads", updated.GetDescription())

	got, err = ia.GetAppProfile(ctx, &adminpb.GetAppProfileRequest{Name: name})
	require.NoError(t, err)
	assert.Equal(t, "single cluster reads", got.GetDescription())
	require.NotNil(t, got.GetSingleClusterRouting(), "the update must install single-cluster routing")
	assert.Equal(t, "c1", got.GetSingleClusterRouting().GetClusterId())
	assert.Nil(t, got.GetMultiClusterRoutingUseAny(), "the replaced routing policy must be gone")
	assert.NotEqual(t, created.GetEtag(), got.GetEtag(), "a write assigns a fresh etag")

	_, err = ia.DeleteAppProfile(ctx, &adminpb.DeleteAppProfileRequest{Name: name})
	require.NoError(t, err)

	_, err = ia.GetAppProfile(ctx, &adminpb.GetAppProfileRequest{Name: name})
	requireGRPCCode(t, err, codes.NotFound)

	list, err = ia.ListAppProfiles(ctx, &adminpb.ListAppProfilesRequest{Parent: instance})
	require.NoError(t, err)
	assert.Empty(t, list.GetAppProfiles())

	_, err = ia.ListAppProfiles(ctx, &adminpb.ListAppProfilesRequest{Parent: instance + "-missing"})
	requireGRPCCode(t, err, codes.NotFound)
}

func TestBigtableAdminGRPC_LogicalAndMaterializedViews(t *testing.T) {
	ia, _, _ := bigtableAdminGRPCConn(t)
	instance, _ := bigtableAdminGRPCInstance(t, ia, "bt-grpc-views", "inst", "c1")

	logicalName := instance + "/logicalViews/daily"
	op, err := ia.CreateLogicalView(ctx, &adminpb.CreateLogicalViewRequest{
		Parent:        instance,
		LogicalViewId: "daily",
		LogicalView:   &adminpb.LogicalView{Query: "SELECT 1"},
	})
	require.NoError(t, err)
	created := bigtableLROResource(t, op, &adminpb.LogicalView{})
	assert.Equal(t, logicalName, created.GetName())
	assert.Equal(t, "SELECT 1", created.GetQuery())
	require.NotEmpty(t, created.GetEtag())

	views, err := ia.ListLogicalViews(ctx, &adminpb.ListLogicalViewsRequest{Parent: instance})
	require.NoError(t, err)
	require.Len(t, views.GetLogicalViews(), 1)
	assert.Equal(t, logicalName, views.GetLogicalViews()[0].GetName())

	op, err = ia.UpdateLogicalView(ctx, &adminpb.UpdateLogicalViewRequest{
		LogicalView: &adminpb.LogicalView{Name: logicalName, Query: "SELECT 2", DeletionProtection: true},
		UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"query", "deletion_protection"}},
	})
	require.NoError(t, err)
	bigtableLROResource(t, op, &adminpb.LogicalView{})

	got, err := ia.GetLogicalView(ctx, &adminpb.GetLogicalViewRequest{Name: logicalName})
	require.NoError(t, err)
	assert.Equal(t, "SELECT 2", got.GetQuery())
	assert.True(t, got.GetDeletionProtection())

	// A masked field the caller leaves unset is cleared, which is what the
	// field mask asks for.
	op, err = ia.UpdateLogicalView(ctx, &adminpb.UpdateLogicalViewRequest{
		LogicalView: &adminpb.LogicalView{Name: logicalName},
		UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"deletion_protection"}},
	})
	require.NoError(t, err)
	bigtableLROResource(t, op, &adminpb.LogicalView{})
	got, err = ia.GetLogicalView(ctx, &adminpb.GetLogicalViewRequest{Name: logicalName})
	require.NoError(t, err)
	assert.False(t, got.GetDeletionProtection())
	assert.Equal(t, "SELECT 2", got.GetQuery(), "an unmasked field is untouched")

	// A delete carrying a stale etag is refused; the current one succeeds.
	_, err = ia.DeleteLogicalView(ctx, &adminpb.DeleteLogicalViewRequest{Name: logicalName, Etag: created.GetEtag()})
	requireGRPCCode(t, err, codes.Aborted)
	_, err = ia.DeleteLogicalView(ctx, &adminpb.DeleteLogicalViewRequest{Name: logicalName, Etag: got.GetEtag()})
	require.NoError(t, err)
	_, err = ia.GetLogicalView(ctx, &adminpb.GetLogicalViewRequest{Name: logicalName})
	requireGRPCCode(t, err, codes.NotFound)

	matName := instance + "/materializedViews/rollup"
	op, err = ia.CreateMaterializedView(ctx, &adminpb.CreateMaterializedViewRequest{
		Parent:             instance,
		MaterializedViewId: "rollup",
		MaterializedView:   &adminpb.MaterializedView{Query: "SELECT count(*)"},
	})
	require.NoError(t, err)
	createdMat := bigtableLROResource(t, op, &adminpb.MaterializedView{})
	assert.Equal(t, matName, createdMat.GetName())

	mats, err := ia.ListMaterializedViews(ctx, &adminpb.ListMaterializedViewsRequest{Parent: instance})
	require.NoError(t, err)
	require.Len(t, mats.GetMaterializedViews(), 1)

	op, err = ia.UpdateMaterializedView(ctx, &adminpb.UpdateMaterializedViewRequest{
		MaterializedView: &adminpb.MaterializedView{Name: matName, DeletionProtection: true},
		UpdateMask:       &fieldmaskpb.FieldMask{Paths: []string{"deletion_protection"}},
	})
	require.NoError(t, err)
	bigtableLROResource(t, op, &adminpb.MaterializedView{})
	gotMat, err := ia.GetMaterializedView(ctx, &adminpb.GetMaterializedViewRequest{Name: matName})
	require.NoError(t, err)
	assert.True(t, gotMat.GetDeletionProtection())
	assert.Equal(t, "SELECT count(*)", gotMat.GetQuery())

	_, err = ia.DeleteMaterializedView(ctx, &adminpb.DeleteMaterializedViewRequest{Name: matName})
	require.NoError(t, err)
	_, err = ia.GetMaterializedView(ctx, &adminpb.GetMaterializedViewRequest{Name: matName})
	requireGRPCCode(t, err, codes.NotFound)
}

func TestBigtableAdminGRPC_InstanceAndClusterUpdates(t *testing.T) {
	ia, _, _ := bigtableAdminGRPCConn(t)
	instance, cluster := bigtableAdminGRPCInstance(t, ia, "bt-grpc-updates", "inst", "c1")

	replaced, err := ia.UpdateInstance(ctx, &adminpb.Instance{
		Name:        instance,
		DisplayName: "replaced",
		Type:        adminpb.Instance_DEVELOPMENT,
		Labels:      map[string]string{"team": "data"},
	})
	require.NoError(t, err)
	assert.Equal(t, "replaced", replaced.GetDisplayName())
	assert.Equal(t, adminpb.Instance_DEVELOPMENT, replaced.GetType())
	assert.Equal(t, map[string]string{"team": "data"}, replaced.GetLabels())

	op, err := ia.PartialUpdateInstance(ctx, &adminpb.PartialUpdateInstanceRequest{
		Instance:   &adminpb.Instance{Name: instance, DisplayName: "patched"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"display_name"}},
	})
	require.NoError(t, err)
	patched := bigtableLROResource(t, op, &adminpb.Instance{})
	assert.Equal(t, "patched", patched.GetDisplayName())

	got, err := ia.GetInstance(ctx, &adminpb.GetInstanceRequest{Name: instance})
	require.NoError(t, err)
	assert.Equal(t, "patched", got.GetDisplayName())
	assert.Equal(t, map[string]string{"team": "data"}, got.GetLabels(), "an unmasked field is untouched")

	_, err = ia.PartialUpdateInstance(ctx, &adminpb.PartialUpdateInstanceRequest{
		Instance:   &adminpb.Instance{Name: instance + "-missing", DisplayName: "x"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"display_name"}},
	})
	requireGRPCCode(t, err, codes.NotFound)

	op, err = ia.UpdateCluster(ctx, &adminpb.Cluster{Name: cluster, ServeNodes: 4})
	require.NoError(t, err)
	scaled := bigtableLROResource(t, op, &adminpb.Cluster{})
	assert.EqualValues(t, 4, scaled.GetServeNodes())

	op, err = ia.PartialUpdateCluster(ctx, &adminpb.PartialUpdateClusterRequest{
		Cluster:    &adminpb.Cluster{Name: cluster, ServeNodes: 7},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"serve_nodes"}},
	})
	require.NoError(t, err)
	bigtableLROResource(t, op, &adminpb.Cluster{})

	gotCluster, err := ia.GetCluster(ctx, &adminpb.GetClusterRequest{Name: cluster})
	require.NoError(t, err)
	assert.EqualValues(t, 7, gotCluster.GetServeNodes())
	assert.Equal(t, adminpb.StorageType_SSD, gotCluster.GetDefaultStorageType())

	hot, err := ia.ListHotTablets(ctx, &adminpb.ListHotTabletsRequest{Parent: cluster})
	require.NoError(t, err)
	assert.Empty(t, hot.GetHotTablets(), "no tablet in this cluster is hot")

	_, err = ia.ListHotTablets(ctx, &adminpb.ListHotTabletsRequest{Parent: instance + "/clusters/missing"})
	requireGRPCCode(t, err, codes.NotFound)
}

func TestBigtableAdminGRPC_IAMPolicies(t *testing.T) {
	ia, ta, _ := bigtableAdminGRPCConn(t)
	instance, _ := bigtableAdminGRPCInstance(t, ia, "bt-grpc-iam", "inst", "c1")
	table := bigtableAdminGRPCTable(t, ta, instance, "events", "cf")

	// An instance starts with an empty policy carrying a stable etag.
	policy, err := ia.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: instance})
	require.NoError(t, err)
	assert.Empty(t, policy.GetBindings())
	require.NotEmpty(t, policy.GetEtag())

	set, err := ia.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{
		Resource: instance,
		Policy: &iampb.Policy{
			Version: 1,
			Etag:    policy.GetEtag(),
			Bindings: []*iampb.Binding{{
				Role:    "roles/bigtable.user",
				Members: []string{"user:alice@example.com"},
			}},
		},
	})
	require.NoError(t, err)
	require.Len(t, set.GetBindings(), 1)

	read, err := ia.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: instance})
	require.NoError(t, err)
	require.Len(t, read.GetBindings(), 1)
	assert.Equal(t, "roles/bigtable.user", read.GetBindings()[0].GetRole())
	assert.Equal(t, []string{"user:alice@example.com"}, read.GetBindings()[0].GetMembers())
	assert.Equal(t, set.GetEtag(), read.GetEtag())

	// Writing against the etag that was just superseded is refused.
	_, err = ia.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{
		Resource: instance,
		Policy:   &iampb.Policy{Etag: policy.GetEtag(), Bindings: []*iampb.Binding{}},
	})
	requireGRPCCode(t, err, codes.Aborted)

	perms, err := ia.TestIamPermissions(ctx, &iampb.TestIamPermissionsRequest{
		Resource:    instance,
		Permissions: []string{"bigtable.tables.list"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"bigtable.tables.list"}, perms.GetPermissions())

	_, err = ia.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: instance + "-missing"})
	requireGRPCCode(t, err, codes.NotFound)

	// The table admin service serves the triple on tables, over the same
	// per-resource policy store.
	tablePolicy, err := ta.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{
		Resource: table,
		Policy: &iampb.Policy{Bindings: []*iampb.Binding{{
			Role:    "roles/bigtable.reader",
			Members: []string{"group:analysts@example.com"},
		}}},
	})
	require.NoError(t, err)
	require.Len(t, tablePolicy.GetBindings(), 1)

	readTable, err := ta.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: table})
	require.NoError(t, err)
	require.Len(t, readTable.GetBindings(), 1)
	assert.Equal(t, "roles/bigtable.reader", readTable.GetBindings()[0].GetRole())

	// The instance policy is a different resource and is unchanged by it.
	readInstance, err := ia.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: instance})
	require.NoError(t, err)
	assert.Equal(t, "roles/bigtable.user", readInstance.GetBindings()[0].GetRole())

	_, err = ta.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: table + "-missing"})
	requireGRPCCode(t, err, codes.NotFound)

	// A resource that does not exist grants nothing rather than erroring.
	missingPerms, err := ta.TestIamPermissions(ctx, &iampb.TestIamPermissionsRequest{
		Resource:    table + "-missing",
		Permissions: []string{"bigtable.tables.get"},
	})
	require.NoError(t, err)
	assert.Empty(t, missingPerms.GetPermissions())
}

func TestBigtableAdminGRPC_BackupsAndRestore(t *testing.T) {
	// The data client reaches the simulator through the coordinate a real
	// client uses for the Cloud Bigtable emulator.
	t.Setenv("BIGTABLE_EMULATOR_HOST", grpcAddr)
	ia, ta, _ := bigtableAdminGRPCConn(t)
	const project, instanceID, family = "bt-grpc-backups", "inst", "cf"
	instance, cluster := bigtableAdminGRPCInstance(t, ia, project, instanceID, "c1")
	table := bigtableAdminGRPCTable(t, ta, instance, "events", family)

	// A backup captures the source table's rows, so the assertions further
	// down read them back out of the restore. Were the capture missing, a
	// restore would yield an empty table and still report success — every row
	// silently lost behind a green result.
	client, err := bigtable.NewClient(ctx, project, instanceID)
	require.NoError(t, err)
	defer client.Close()
	source := client.Open("events")
	for _, key := range []string{"user#1", "user#2"} {
		mut := bigtable.NewMutation()
		mut.Set(family, "name", bigtable.Now(), []byte(key))
		require.NoError(t, source.Apply(ctx, key, mut))
	}

	expire := timestamppb.New(time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second))
	op, err := ta.CreateBackup(ctx, &adminpb.CreateBackupRequest{
		Parent:   cluster,
		BackupId: "nightly",
		Backup:   &adminpb.Backup{SourceTable: table, ExpireTime: expire},
	})
	require.NoError(t, err)
	backupName := cluster + "/backups/nightly"
	created := bigtableLROResource(t, op, &adminpb.Backup{})
	assert.Equal(t, backupName, created.GetName())
	assert.Equal(t, table, created.GetSourceTable())
	assert.Equal(t, adminpb.Backup_READY, created.GetState())

	// A backup of a table that does not exist is a NotFound, not an empty
	// backup.
	_, err = ta.CreateBackup(ctx, &adminpb.CreateBackupRequest{
		Parent: cluster, BackupId: "bogus",
		Backup: &adminpb.Backup{SourceTable: instance + "/tables/missing", ExpireTime: expire},
	})
	requireGRPCCode(t, err, codes.NotFound)

	got, err := ta.GetBackup(ctx, &adminpb.GetBackupRequest{Name: backupName})
	require.NoError(t, err)
	assert.Equal(t, expire.AsTime(), got.GetExpireTime().AsTime())

	list, err := ta.ListBackups(ctx, &adminpb.ListBackupsRequest{Parent: cluster})
	require.NoError(t, err)
	require.Len(t, list.GetBackups(), 1)
	assert.Equal(t, backupName, list.GetBackups()[0].GetName())

	newExpire := timestamppb.New(time.Now().Add(96 * time.Hour).UTC().Truncate(time.Second))
	updated, err := ta.UpdateBackup(ctx, &adminpb.UpdateBackupRequest{
		Backup:     &adminpb.Backup{Name: backupName, ExpireTime: newExpire},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"expire_time"}},
	})
	require.NoError(t, err)
	assert.Equal(t, newExpire.AsTime(), updated.GetExpireTime().AsTime())
	assert.Equal(t, table, updated.GetSourceTable(), "an unmasked field is untouched")

	got, err = ta.GetBackup(ctx, &adminpb.GetBackupRequest{Name: backupName})
	require.NoError(t, err)
	assert.Equal(t, newExpire.AsTime(), got.GetExpireTime().AsTime())

	op, err = ta.CopyBackup(ctx, &adminpb.CopyBackupRequest{
		Parent:       cluster,
		BackupId:     "nightly-copy",
		SourceBackup: backupName,
		ExpireTime:   newExpire,
	})
	require.NoError(t, err)
	copied := bigtableLROResource(t, op, &adminpb.Backup{})
	assert.Equal(t, cluster+"/backups/nightly-copy", copied.GetName())
	assert.Equal(t, backupName, copied.GetSourceBackup())
	assert.Equal(t, table, copied.GetSourceTable(), "the copy carries the source's table")

	list, err = ta.ListBackups(ctx, &adminpb.ListBackupsRequest{Parent: cluster})
	require.NoError(t, err)
	assert.Len(t, list.GetBackups(), 2)

	// One page at a time, with the token continuing the listing.
	page, err := ta.ListBackups(ctx, &adminpb.ListBackupsRequest{Parent: cluster, PageSize: 1})
	require.NoError(t, err)
	require.Len(t, page.GetBackups(), 1)
	require.NotEmpty(t, page.GetNextPageToken())
	next, err := ta.ListBackups(ctx, &adminpb.ListBackupsRequest{Parent: cluster, PageSize: 1, PageToken: page.GetNextPageToken()})
	require.NoError(t, err)
	require.Len(t, next.GetBackups(), 1)
	assert.NotEqual(t, page.GetBackups()[0].GetName(), next.GetBackups()[0].GetName())

	// Written after the backup was taken, so it belongs to the source table
	// and not to the capture.
	late := bigtable.NewMutation()
	late.Set(family, "name", bigtable.Now(), []byte("late#1"))
	require.NoError(t, source.Apply(ctx, "late#1", late))

	op, err = ta.RestoreTable(ctx, &adminpb.RestoreTableRequest{
		Parent:  instance,
		TableId: "events-restored",
		Source:  &adminpb.RestoreTableRequest_Backup{Backup: backupName},
	})
	require.NoError(t, err)
	restored := bigtableLROResource(t, op, &adminpb.Table{})
	assert.Equal(t, instance+"/tables/events-restored", restored.GetName())

	readBack, err := ta.GetTable(ctx, &adminpb.GetTableRequest{Name: instance + "/tables/events-restored"})
	require.NoError(t, err)
	assert.Equal(t, restored.GetName(), readBack.GetName())
	assert.Contains(t, readBack.GetColumnFamilies(), family, "the restore carries the source's schema")

	assert.Equal(t, []string{"user#1", "user#2"}, bigtableRowKeys(t, client.Open("events-restored")),
		"the restore holds the rows the backup captured, and only those")

	_, err = ta.RestoreTable(ctx, &adminpb.RestoreTableRequest{
		Parent:  instance,
		TableId: "events-from-nothing",
		Source:  &adminpb.RestoreTableRequest_Backup{Backup: cluster + "/backups/missing"},
	})
	requireGRPCCode(t, err, codes.NotFound)

	_, err = ta.DeleteBackup(ctx, &adminpb.DeleteBackupRequest{Name: backupName})
	require.NoError(t, err)
	_, err = ta.GetBackup(ctx, &adminpb.GetBackupRequest{Name: backupName})
	requireGRPCCode(t, err, codes.NotFound)
	_, err = ta.DeleteBackup(ctx, &adminpb.DeleteBackupRequest{Name: backupName})
	requireGRPCCode(t, err, codes.NotFound)

	list, err = ta.ListBackups(ctx, &adminpb.ListBackupsRequest{Parent: cluster})
	require.NoError(t, err)
	require.Len(t, list.GetBackups(), 1)
	assert.Equal(t, copied.GetName(), list.GetBackups()[0].GetName())

	// The copy holds its own capture, so deleting the backup it was copied
	// from leaves it restorable with the same rows.
	op, err = ta.RestoreTable(ctx, &adminpb.RestoreTableRequest{
		Parent:  instance,
		TableId: "events-from-copy",
		Source:  &adminpb.RestoreTableRequest_Backup{Backup: copied.GetName()},
	})
	require.NoError(t, err)
	fromCopy := bigtableLROResource(t, op, &adminpb.Table{})
	assert.Equal(t, instance+"/tables/events-from-copy", fromCopy.GetName())
	assert.Equal(t, []string{"user#1", "user#2"}, bigtableRowKeys(t, client.Open("events-from-copy")))
}

func TestBigtableAdminGRPC_AuthorizedViewsAndSchemaBundles(t *testing.T) {
	ia, ta, _ := bigtableAdminGRPCConn(t)
	instance, _ := bigtableAdminGRPCInstance(t, ia, "bt-grpc-tableviews", "inst", "c1")
	table := bigtableAdminGRPCTable(t, ta, instance, "events", "cf")

	viewName := table + "/authorizedViews/users"
	op, err := ta.CreateAuthorizedView(ctx, &adminpb.CreateAuthorizedViewRequest{
		Parent:           table,
		AuthorizedViewId: "users",
		AuthorizedView: &adminpb.AuthorizedView{
			AuthorizedView: &adminpb.AuthorizedView_SubsetView_{
				SubsetView: &adminpb.AuthorizedView_SubsetView{RowPrefixes: [][]byte{[]byte("user#")}},
			},
		},
	})
	require.NoError(t, err)
	created := bigtableLROResource(t, op, &adminpb.AuthorizedView{})
	assert.Equal(t, viewName, created.GetName())
	require.NotNil(t, created.GetSubsetView())
	assert.Equal(t, [][]byte{[]byte("user#")}, created.GetSubsetView().GetRowPrefixes())

	got, err := ta.GetAuthorizedView(ctx, &adminpb.GetAuthorizedViewRequest{Name: viewName})
	require.NoError(t, err)
	require.NotNil(t, got.GetSubsetView())

	nameOnly, err := ta.GetAuthorizedView(ctx, &adminpb.GetAuthorizedViewRequest{
		Name: viewName, View: adminpb.AuthorizedView_NAME_ONLY,
	})
	require.NoError(t, err)
	assert.Equal(t, viewName, nameOnly.GetName())
	assert.Nil(t, nameOnly.GetSubsetView(), "NAME_ONLY carries the name alone")

	views, err := ta.ListAuthorizedViews(ctx, &adminpb.ListAuthorizedViewsRequest{Parent: table})
	require.NoError(t, err)
	require.Len(t, views.GetAuthorizedViews(), 1)
	assert.Equal(t, viewName, views.GetAuthorizedViews()[0].GetName())

	op, err = ta.UpdateAuthorizedView(ctx, &adminpb.UpdateAuthorizedViewRequest{
		AuthorizedView: &adminpb.AuthorizedView{
			Name: viewName,
			AuthorizedView: &adminpb.AuthorizedView_SubsetView_{
				SubsetView: &adminpb.AuthorizedView_SubsetView{RowPrefixes: [][]byte{[]byte("admin#")}},
			},
			DeletionProtection: true,
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"subset_view", "deletion_protection"}},
	})
	require.NoError(t, err)
	bigtableLROResource(t, op, &adminpb.AuthorizedView{})

	got, err = ta.GetAuthorizedView(ctx, &adminpb.GetAuthorizedViewRequest{Name: viewName})
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("admin#")}, got.GetSubsetView().GetRowPrefixes())
	assert.True(t, got.GetDeletionProtection())

	_, err = ta.DeleteAuthorizedView(ctx, &adminpb.DeleteAuthorizedViewRequest{Name: viewName, Etag: created.GetEtag()})
	requireGRPCCode(t, err, codes.Aborted)
	_, err = ta.DeleteAuthorizedView(ctx, &adminpb.DeleteAuthorizedViewRequest{Name: viewName, Etag: got.GetEtag()})
	require.NoError(t, err)
	_, err = ta.GetAuthorizedView(ctx, &adminpb.GetAuthorizedViewRequest{Name: viewName})
	requireGRPCCode(t, err, codes.NotFound)

	bundleName := table + "/schemaBundles/rows"
	op, err = ta.CreateSchemaBundle(ctx, &adminpb.CreateSchemaBundleRequest{
		Parent:         table,
		SchemaBundleId: "rows",
		SchemaBundle: &adminpb.SchemaBundle{
			Type: &adminpb.SchemaBundle_ProtoSchema{
				ProtoSchema: &adminpb.ProtoSchema{ProtoDescriptors: bigtableTestDescriptorSet(t, "Row")},
			},
		},
	})
	require.NoError(t, err)
	createdBundle := bigtableLROResource(t, op, &adminpb.SchemaBundle{})
	assert.Equal(t, bundleName, createdBundle.GetName())
	require.NotNil(t, createdBundle.GetProtoSchema())

	gotBundle, err := ta.GetSchemaBundle(ctx, &adminpb.GetSchemaBundleRequest{Name: bundleName})
	require.NoError(t, err)
	assert.Equal(t, createdBundle.GetProtoSchema().GetProtoDescriptors(), gotBundle.GetProtoSchema().GetProtoDescriptors())

	bundles, err := ta.ListSchemaBundles(ctx, &adminpb.ListSchemaBundlesRequest{Parent: table})
	require.NoError(t, err)
	require.Len(t, bundles.GetSchemaBundles(), 1)

	next := bigtableTestDescriptorSet(t, "RowV2")
	op, err = ta.UpdateSchemaBundle(ctx, &adminpb.UpdateSchemaBundleRequest{
		SchemaBundle: &adminpb.SchemaBundle{
			Name: bundleName,
			Type: &adminpb.SchemaBundle_ProtoSchema{ProtoSchema: &adminpb.ProtoSchema{ProtoDescriptors: next}},
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"proto_schema"}},
	})
	require.NoError(t, err)
	bigtableLROResource(t, op, &adminpb.SchemaBundle{})

	gotBundle, err = ta.GetSchemaBundle(ctx, &adminpb.GetSchemaBundleRequest{Name: bundleName})
	require.NoError(t, err)
	assert.Equal(t, next, gotBundle.GetProtoSchema().GetProtoDescriptors())

	_, err = ta.DeleteSchemaBundle(ctx, &adminpb.DeleteSchemaBundleRequest{Name: bundleName})
	require.NoError(t, err)
	_, err = ta.GetSchemaBundle(ctx, &adminpb.GetSchemaBundleRequest{Name: bundleName})
	requireGRPCCode(t, err, codes.NotFound)

	_, err = ta.ListSchemaBundles(ctx, &adminpb.ListSchemaBundlesRequest{Parent: instance + "/tables/missing"})
	requireGRPCCode(t, err, codes.NotFound)
}

// bigtableTestDescriptorSet serializes a one-message FileDescriptorSet, the
// payload a schema bundle's proto schema carries.
func bigtableTestDescriptorSet(t *testing.T, message string) []byte {
	t.Helper()
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name:    proto.String("bigtable_admin_grpc_test.proto"),
		Package: proto.String("sockerless.bigtable.test"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String(message),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:   proto.String("key"),
				Number: proto.Int32(1),
				Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			}},
		}},
	}}}
	raw, err := proto.Marshal(set)
	require.NoError(t, err)
	return raw
}

func TestBigtableAdminGRPC_TableUpdatesConsistencyAndDropRowRange(t *testing.T) {
	t.Setenv("BIGTABLE_EMULATOR_HOST", grpcAddr)
	ia, ta, _ := bigtableAdminGRPCConn(t)
	const (
		project    = "bt-grpc-tables"
		instanceID = "inst"
		family     = "cf"
	)
	instance, _ := bigtableAdminGRPCInstance(t, ia, project, instanceID, "c1")
	table := bigtableAdminGRPCTable(t, ta, instance, "events", family)

	op, err := ta.UpdateTable(ctx, &adminpb.UpdateTableRequest{
		Table:      &adminpb.Table{Name: table, DeletionProtection: true},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"deletion_protection"}},
	})
	require.NoError(t, err)
	updated := bigtableLROResource(t, op, &adminpb.Table{})
	assert.True(t, updated.GetDeletionProtection())

	got, err := ta.GetTable(ctx, &adminpb.GetTableRequest{Name: table})
	require.NoError(t, err)
	assert.True(t, got.GetDeletionProtection())

	// The store keeps a table's deletion protection and column families; a
	// mask naming a field it does not keep says so rather than dropping it.
	_, err = ta.UpdateTable(ctx, &adminpb.UpdateTableRequest{
		Table:      &adminpb.Table{Name: table},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"change_stream_config"}},
	})
	requireGRPCCode(t, err, codes.Unimplemented)

	_, err = ta.UpdateTable(ctx, &adminpb.UpdateTableRequest{
		Table:      &adminpb.Table{Name: table},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"not_a_field"}},
	})
	requireGRPCCode(t, err, codes.InvalidArgument)

	// A live table undeletes to itself; a name that was deleted is gone.
	op, err = ta.UndeleteTable(ctx, &adminpb.UndeleteTableRequest{Name: table})
	require.NoError(t, err)
	undeleted := bigtableLROResource(t, op, &adminpb.Table{})
	assert.Equal(t, table, undeleted.GetName())
	_, err = ta.UndeleteTable(ctx, &adminpb.UndeleteTableRequest{Name: instance + "/tables/never"})
	requireGRPCCode(t, err, codes.NotFound)

	token, err := ta.GenerateConsistencyToken(ctx, &adminpb.GenerateConsistencyTokenRequest{Name: table})
	require.NoError(t, err)
	require.NotEmpty(t, token.GetConsistencyToken())

	consistent, err := ta.CheckConsistency(ctx, &adminpb.CheckConsistencyRequest{
		Name: table, ConsistencyToken: token.GetConsistencyToken(),
	})
	require.NoError(t, err)
	assert.True(t, consistent.GetConsistent())

	_, err = ta.CheckConsistency(ctx, &adminpb.CheckConsistencyRequest{Name: table})
	requireGRPCCode(t, err, codes.InvalidArgument)
	_, err = ta.GenerateConsistencyToken(ctx, &adminpb.GenerateConsistencyTokenRequest{Name: instance + "/tables/missing"})
	requireGRPCCode(t, err, codes.NotFound)

	// DropRowRange deletes real rows: write through the data plane, drop a
	// prefix, and read back what survived.
	client, err := bigtable.NewClient(ctx, project, instanceID)
	require.NoError(t, err)
	defer client.Close()
	tbl := client.Open("events")
	for _, key := range []string{"user#1", "user#2", "admin#1"} {
		mut := bigtable.NewMutation()
		mut.Set(family, "name", bigtable.Now(), []byte(key))
		require.NoError(t, tbl.Apply(ctx, key, mut))
	}

	_, err = ta.DropRowRange(ctx, &adminpb.DropRowRangeRequest{
		Name:   table,
		Target: &adminpb.DropRowRangeRequest_RowKeyPrefix{RowKeyPrefix: []byte("user#")},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"admin#1"}, bigtableRowKeys(t, tbl), "the dropped prefix must be gone")

	_, err = ta.DropRowRange(ctx, &adminpb.DropRowRangeRequest{
		Name:   table,
		Target: &adminpb.DropRowRangeRequest_DeleteAllDataFromTable{DeleteAllDataFromTable: true},
	})
	require.NoError(t, err)
	assert.Empty(t, bigtableRowKeys(t, tbl), "dropping the whole table must empty it")

	_, err = ta.DropRowRange(ctx, &adminpb.DropRowRangeRequest{Name: table})
	requireGRPCCode(t, err, codes.InvalidArgument)
	_, err = ta.DropRowRange(ctx, &adminpb.DropRowRangeRequest{
		Name:   instance + "/tables/missing",
		Target: &adminpb.DropRowRangeRequest_DeleteAllDataFromTable{DeleteAllDataFromTable: true},
	})
	requireGRPCCode(t, err, codes.NotFound)
}

// bigtableRowKeys reads every row key in the table.
func bigtableRowKeys(t *testing.T, tbl *bigtable.Table) []string {
	t.Helper()
	keys := []string{}
	require.NoError(t, tbl.ReadRows(ctx, bigtable.InfiniteRange(""), func(row bigtable.Row) bool {
		keys = append(keys, row.Key())
		return true
	}))
	return keys
}

// TestBigtableAdminGRPC_SnapshotsAndCreateTableFromSnapshot drives the
// snapshot family, which the bigtableadmin Discovery document does not
// declare — it is served on the gRPC door alone, out of the same capture the
// backups use.
func TestBigtableAdminGRPC_SnapshotsAndCreateTableFromSnapshot(t *testing.T) {
	// The data client reaches the simulator through the coordinate a real
	// client uses for the Cloud Bigtable emulator.
	t.Setenv("BIGTABLE_EMULATOR_HOST", grpcAddr)
	ia, ta, _ := bigtableAdminGRPCConn(t)
	const project, instanceID, family = "bt-grpc-snapshots", "inst", "cf"
	instance, cluster := bigtableAdminGRPCInstance(t, ia, project, instanceID, "c1")
	table := bigtableAdminGRPCTable(t, ta, instance, "events", family)

	client, err := bigtable.NewClient(ctx, project, instanceID)
	require.NoError(t, err)
	defer client.Close()
	source := client.Open("events")
	for _, key := range []string{"a#1", "b#1"} {
		mut := bigtable.NewMutation()
		mut.Set(family, "name", bigtable.Now(), []byte(key))
		require.NoError(t, source.Apply(ctx, key, mut))
	}

	op, err := ta.SnapshotTable(ctx, &adminpb.SnapshotTableRequest{
		Name:        table,
		Cluster:     cluster,
		SnapshotId:  "nightly",
		Ttl:         durationpb.New(24 * time.Hour),
		Description: "before the migration",
	})
	require.NoError(t, err)
	snapshotName := cluster + "/snapshots/nightly"
	created := bigtableLROResource(t, op, &adminpb.Snapshot{})
	assert.Equal(t, snapshotName, created.GetName())
	assert.Equal(t, adminpb.Snapshot_READY, created.GetState())
	assert.Equal(t, "before the migration", created.GetDescription())
	assert.Equal(t, table, created.GetSourceTable().GetName())
	assert.NotNil(t, created.GetDeleteTime(), "a ttl gives the snapshot a delete time")
	assert.Equal(t, int64(2), created.GetDataSizeBytes(), "the snapshot reports what it captured")

	_, err = ta.SnapshotTable(ctx, &adminpb.SnapshotTableRequest{
		Name: instance + "/tables/missing", Cluster: cluster, SnapshotId: "bogus",
	})
	requireGRPCCode(t, err, codes.NotFound)

	got, err := ta.GetSnapshot(ctx, &adminpb.GetSnapshotRequest{Name: snapshotName})
	require.NoError(t, err)
	assert.Equal(t, created.GetDescription(), got.GetDescription())

	list, err := ta.ListSnapshots(ctx, &adminpb.ListSnapshotsRequest{Parent: cluster})
	require.NoError(t, err)
	require.Len(t, list.GetSnapshots(), 1)
	assert.Equal(t, snapshotName, list.GetSnapshots()[0].GetName())

	// Written after the snapshot, so it is not part of what it captured.
	late := bigtable.NewMutation()
	late.Set(family, "name", bigtable.Now(), []byte("c#1"))
	require.NoError(t, source.Apply(ctx, "c#1", late))

	op, err = ta.CreateTableFromSnapshot(ctx, &adminpb.CreateTableFromSnapshotRequest{
		Parent:         instance,
		TableId:        "events-restored",
		SourceSnapshot: snapshotName,
	})
	require.NoError(t, err)
	restored := bigtableLROResource(t, op, &adminpb.Table{})
	assert.Equal(t, instance+"/tables/events-restored", restored.GetName())
	assert.Contains(t, restored.GetColumnFamilies(), family)
	assert.Equal(t, []string{"a#1", "b#1"}, bigtableRowKeys(t, client.Open("events-restored")),
		"the new table holds the rows the snapshot captured, and only those")

	_, err = ta.CreateTableFromSnapshot(ctx, &adminpb.CreateTableFromSnapshotRequest{
		Parent: instance, TableId: "from-nothing", SourceSnapshot: cluster + "/snapshots/missing",
	})
	requireGRPCCode(t, err, codes.NotFound)

	_, err = ta.DeleteSnapshot(ctx, &adminpb.DeleteSnapshotRequest{Name: snapshotName})
	require.NoError(t, err)
	_, err = ta.GetSnapshot(ctx, &adminpb.GetSnapshotRequest{Name: snapshotName})
	requireGRPCCode(t, err, codes.NotFound)
	_, err = ta.DeleteSnapshot(ctx, &adminpb.DeleteSnapshotRequest{Name: snapshotName})
	requireGRPCCode(t, err, codes.NotFound)
}

func TestBigtableAdminGRPC_Operations(t *testing.T) {
	ia, _, ops := bigtableAdminGRPCConn(t)
	instance, _ := bigtableAdminGRPCInstance(t, ia, "bt-grpc-ops", "inst", "c1")

	created, err := ia.CreateLogicalView(ctx, &adminpb.CreateLogicalViewRequest{
		Parent:        instance,
		LogicalViewId: "daily",
		LogicalView:   &adminpb.LogicalView{Query: "SELECT 1"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.GetName())

	// A Cloud Bigtable admin operation lives under the project, in the
	// "operations/" collection the bigtableadmin document declares — the same
	// name the REST operations door addresses it by.
	const operationsParent = "operations/projects/bt-grpc-ops"
	require.True(t, strings.HasPrefix(created.GetName(), operationsParent+"/operations/"),
		"an operation name must be the one both doors address: got %q", created.GetName())

	listed, err := ops.ListOperations(ctx, &longrunningpb.ListOperationsRequest{Name: operationsParent})
	require.NoError(t, err)
	names := make([]string, 0, len(listed.GetOperations()))
	for _, op := range listed.GetOperations() {
		names = append(names, op.GetName())
	}
	assert.Contains(t, names, created.GetName(), "the create operation must be listed under its project")

	// The listing is scoped: another project's operations collection is empty.
	empty, err := ops.ListOperations(ctx, &longrunningpb.ListOperationsRequest{Name: "operations/projects/bt-grpc-ops-elsewhere"})
	require.NoError(t, err)
	assert.Empty(t, empty.GetOperations())

	fetched, err := ops.GetOperation(ctx, &longrunningpb.GetOperationRequest{Name: created.GetName()})
	require.NoError(t, err)
	assert.True(t, fetched.GetDone())

	// Cancelling a finished operation is acknowledged and leaves its result
	// in place; an unknown operation is a NotFound.
	_, err = ops.CancelOperation(ctx, &longrunningpb.CancelOperationRequest{Name: created.GetName()})
	require.NoError(t, err)
	after, err := ops.GetOperation(ctx, &longrunningpb.GetOperationRequest{Name: created.GetName()})
	require.NoError(t, err)
	assert.True(t, after.GetDone())
	assert.Nil(t, after.GetError())

	_, err = ops.CancelOperation(ctx, &longrunningpb.CancelOperationRequest{Name: operationsParent + "/operations/404"})
	requireGRPCCode(t, err, codes.NotFound)

	_, err = ops.ListOperations(ctx, &longrunningpb.ListOperationsRequest{Name: operationsParent, Filter: "done=true"})
	requireGRPCCode(t, err, codes.Unimplemented)

	_, err = ops.DeleteOperation(ctx, &longrunningpb.DeleteOperationRequest{Name: created.GetName()})
	require.NoError(t, err)
	_, err = ops.GetOperation(ctx, &longrunningpb.GetOperationRequest{Name: created.GetName()})
	requireGRPCCode(t, err, codes.NotFound)
}
