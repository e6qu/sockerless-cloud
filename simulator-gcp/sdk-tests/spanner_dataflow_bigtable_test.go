package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/bigtableadmin/v2"
	"google.golang.org/api/dataflow/v1b3"
	"google.golang.org/api/option"
	"google.golang.org/api/spanner/v1"
)

func TestSpanner_InstanceDatabaseSessionSDK(t *testing.T) {
	svc, err := spanner.NewService(ctx, option.WithEndpoint(baseURL+"/spanner/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	op, err := svc.Projects.Instances.Create("projects/test-project", &spanner.CreateInstanceRequest{
		InstanceId: "sdk-spanner",
		Instance: &spanner.Instance{
			DisplayName: "SDK Spanner",
			NodeCount:   1,
			Labels:      map[string]string{"env": "sdk"},
		},
	}).Do()
	require.NoError(t, err)
	assert.True(t, op.Done)

	inst, err := svc.Projects.Instances.Get("projects/test-project/instances/sdk-spanner").Do()
	require.NoError(t, err)
	assert.Equal(t, "READY", inst.State)

	dbOp, err := svc.Projects.Instances.Databases.Create("projects/test-project/instances/sdk-spanner", &spanner.CreateDatabaseRequest{
		CreateStatement: "CREATE DATABASE `sdkdb`",
	}).Do()
	require.NoError(t, err)
	assert.True(t, dbOp.Done)
	var createMetadata map[string]any
	require.NoError(t, json.Unmarshal(dbOp.Metadata, &createMetadata))
	assert.Equal(t, "projects/test-project/instances/sdk-spanner/databases/sdkdb", createMetadata["database"])
	assert.Equal(t, "projects/test-project/instances/sdk-spanner/databases/sdkdb", createMetadata["resource"])

	db, err := svc.Projects.Instances.Databases.Get("projects/test-project/instances/sdk-spanner/databases/sdkdb").Do()
	require.NoError(t, err)
	assert.Equal(t, "READY", db.State)

	ddlOp, err := svc.Projects.Instances.Databases.UpdateDdl(db.Name, &spanner.UpdateDatabaseDdlRequest{
		Statements: []string{"CREATE TABLE Users (UserId STRING(36) NOT NULL) PRIMARY KEY (UserId)"},
	}).Do()
	require.NoError(t, err)
	assert.True(t, ddlOp.Done)
	assert.Contains(t, ddlOp.Name, "/databases/sdkdb/operations/")
	var ddlMetadata map[string]any
	require.NoError(t, json.Unmarshal(ddlOp.Metadata, &ddlMetadata))
	assert.Equal(t, db.Name, ddlMetadata["database"])
	var ddlResponse map[string]any
	require.NoError(t, json.Unmarshal(ddlOp.Response, &ddlResponse))
	assert.Equal(t, "type.googleapis.com/google.protobuf.Empty", ddlResponse["@type"])

	session, err := svc.Projects.Instances.Databases.Sessions.Create(db.Name, &spanner.CreateSessionRequest{
		Session: &spanner.Session{Labels: map[string]string{"kind": "sdk"}},
	}).Do()
	require.NoError(t, err)
	assert.Contains(t, session.Name, "/sessions/")

	sessions, err := svc.Projects.Instances.Databases.Sessions.List(db.Name).Do()
	require.NoError(t, err)
	require.Len(t, sessions.Sessions, 1)
	assert.Equal(t, session.Name, sessions.Sessions[0].Name)
}

func TestSpanner_InstanceConfigSDK(t *testing.T) {
	svc, err := spanner.NewService(ctx, option.WithEndpoint(baseURL+"/spanner/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	const parent = "projects/test-project"
	op, err := svc.Projects.InstanceConfigs.Create(parent, &spanner.CreateInstanceConfigRequest{
		InstanceConfigId: "custom-sdk-config",
		InstanceConfig: &spanner.InstanceConfig{
			DisplayName: "SDK Custom Config",
			BaseConfig:  "projects/test-project/instanceConfigs/nam3",
			Replicas: []*spanner.ReplicaInfo{
				{Location: "us-central1", Type: "READ_WRITE", DefaultLeaderLocation: true},
				{Location: "us-east1", Type: "READ_ONLY"},
			},
			LeaderOptions: []string{"us-central1"},
			Labels:        map[string]string{"env": "sdk"},
		},
	}).Do()
	require.NoError(t, err)
	assert.True(t, op.Done)

	cfg, err := svc.Projects.InstanceConfigs.Get("projects/test-project/instanceConfigs/custom-sdk-config").Do()
	require.NoError(t, err)
	assert.Equal(t, "SDK Custom Config", cfg.DisplayName)
	assert.Equal(t, "USER_MANAGED", cfg.ConfigType)
	assert.Equal(t, "READY", cfg.State)
	require.Len(t, cfg.Replicas, 2)
	assert.Equal(t, "us-central1", cfg.Replicas[0].Location)
	assert.True(t, cfg.Replicas[0].DefaultLeaderLocation)

	list, err := svc.Projects.InstanceConfigs.List(parent).Do()
	require.NoError(t, err)
	found := false
	for _, c := range list.InstanceConfigs {
		if c.Name == "projects/test-project/instanceConfigs/custom-sdk-config" {
			found = true
		}
	}
	assert.True(t, found, "created config should be listed")

	patchOp, err := svc.Projects.InstanceConfigs.Patch(cfg.Name, &spanner.UpdateInstanceConfigRequest{
		InstanceConfig: &spanner.InstanceConfig{
			Name:        cfg.Name,
			DisplayName: "SDK Renamed Config",
		},
		UpdateMask: "displayName",
	}).Do()
	require.NoError(t, err)
	assert.True(t, patchOp.Done)

	updated, err := svc.Projects.InstanceConfigs.Get(cfg.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "SDK Renamed Config", updated.DisplayName)

	// Instance configuration LRO surfaces: list at config level and at the
	// project-wide instanceConfigOperations collection, then read one back.
	cfgOps, err := svc.Projects.InstanceConfigs.Operations.List(cfg.Name + "/operations").Do()
	require.NoError(t, err)
	require.NotEmpty(t, cfgOps.Operations)
	gotOp, err := svc.Projects.InstanceConfigs.Operations.Get(cfgOps.Operations[0].Name).Do()
	require.NoError(t, err)
	assert.Equal(t, cfgOps.Operations[0].Name, gotOp.Name)

	collOps, err := svc.Projects.InstanceConfigOperations.List(parent).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, collOps.Operations)

	// ssdCaches operations list (empty but routable).
	ssdOps, err := svc.Projects.InstanceConfigs.SsdCaches.Operations.List(cfg.Name + "/ssdCaches/cache-1/operations").Do()
	require.NoError(t, err)
	assert.Empty(t, ssdOps.Operations)

	_, err = svc.Projects.InstanceConfigs.Delete(cfg.Name).Do()
	require.NoError(t, err)
	_, err = svc.Projects.InstanceConfigs.Get(cfg.Name).Do()
	require.Error(t, err, "deleted config should not be gettable")
}

func TestSpanner_ScansListSDK(t *testing.T) {
	svc, err := spanner.NewService(ctx, option.WithEndpoint(baseURL+"/spanner/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	resp, err := svc.Scans.List("scans").Do()
	require.NoError(t, err)
	assert.Empty(t, resp.Scans)
}

func TestDataflow_RegionalJobSDK(t *testing.T) {
	svc, err := dataflow.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	job, err := svc.Projects.Locations.Jobs.Create("test-project", "us-central1", &dataflow.Job{
		Name:   "sdk-dataflow-job",
		Type:   "JOB_TYPE_BATCH",
		Labels: map[string]string{"env": "sdk"},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "JOB_STATE_RUNNING", job.CurrentState)

	got, err := svc.Projects.Locations.Jobs.Get("test-project", "us-central1", job.Id).Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-dataflow-job", got.Name)

	list, err := svc.Projects.Locations.Jobs.List("test-project", "us-central1").Do()
	require.NoError(t, err)
	require.Len(t, list.Jobs, 1)
	assert.Equal(t, job.Id, list.Jobs[0].Id)

	// Update drives a state transition via requestedState.
	updated, err := svc.Projects.Locations.Jobs.Update("test-project", "us-central1", job.Id, &dataflow.Job{
		RequestedState: "JOB_STATE_CANCELLED",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "JOB_STATE_CANCELLED", updated.CurrentState)

	// Messages + metrics return faithful representative shapes.
	msgs, err := svc.Projects.Locations.Jobs.Messages.List("test-project", "us-central1", job.Id).Do()
	require.NoError(t, err)
	require.Len(t, msgs.JobMessages, 1)
	assert.Equal(t, "JOB_MESSAGE_BASIC", msgs.JobMessages[0].MessageImportance)

	metrics, err := svc.Projects.Locations.Jobs.GetMetrics("test-project", "us-central1", job.Id).Do()
	require.NoError(t, err)
	require.Len(t, metrics.Metrics, 1)
	assert.Equal(t, "ElementCount", metrics.Metrics[0].Name.Name)

	exec, err := svc.Projects.Locations.Jobs.GetExecutionDetails("test-project", "us-central1", job.Id).Do()
	require.NoError(t, err)
	require.Len(t, exec.Stages, 1)
	assert.Equal(t, "s1", exec.Stages[0].StageId)
}

func TestDataflow_GlobalJobSDK(t *testing.T) {
	svc, err := dataflow.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	job, err := svc.Projects.Jobs.Create("global-df-project", &dataflow.Job{
		Name:     "sdk-global-job",
		Type:     "JOB_TYPE_BATCH",
		Location: "us-central1",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "JOB_STATE_RUNNING", job.CurrentState)

	got, err := svc.Projects.Jobs.Get("global-df-project", job.Id).Do()
	require.NoError(t, err)
	assert.Equal(t, "sdk-global-job", got.Name)

	agg, err := svc.Projects.Jobs.Aggregated("global-df-project").Do()
	require.NoError(t, err)
	require.NotEmpty(t, agg.Jobs)

	metrics, err := svc.Projects.Jobs.GetMetrics("global-df-project", job.Id).Do()
	require.NoError(t, err)
	require.Len(t, metrics.Metrics, 1)
}

func TestDataflow_SnapshotsSDK(t *testing.T) {
	svc, err := dataflow.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	job, err := svc.Projects.Locations.Jobs.Create("snap-project", "us-east1", &dataflow.Job{
		Name: "snap-source-job",
		Type: "JOB_TYPE_STREAMING",
	}).Do()
	require.NoError(t, err)

	snap, err := svc.Projects.Locations.Jobs.Snapshot("snap-project", "us-east1", job.Id, &dataflow.SnapshotJobRequest{
		Description: "sdk snapshot",
		Location:    "us-east1",
		Ttl:         "604800s",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, job.Id, snap.SourceJobId)
	assert.Equal(t, "READY", snap.State)

	got, err := svc.Projects.Locations.Snapshots.Get("snap-project", "us-east1", snap.Id).Do()
	require.NoError(t, err)
	assert.Equal(t, snap.Id, got.Id)

	list, err := svc.Projects.Locations.Snapshots.List("snap-project", "us-east1").Do()
	require.NoError(t, err)
	require.NotEmpty(t, list.Snapshots)

	jobSnaps, err := svc.Projects.Locations.Jobs.Snapshots.List("snap-project", "us-east1", job.Id).Do()
	require.NoError(t, err)
	require.Len(t, jobSnaps.Snapshots, 1)

	gget, err := svc.Projects.Snapshots.Get("snap-project", snap.Id).Do()
	require.NoError(t, err)
	assert.Equal(t, snap.Id, gget.Id)

	glist, err := svc.Projects.Snapshots.List("snap-project").Do()
	require.NoError(t, err)
	require.NotEmpty(t, glist.Snapshots)

	_, err = svc.Projects.Locations.Snapshots.Delete("snap-project", "us-east1", snap.Id).Do()
	require.NoError(t, err)
}

func TestDataflow_TemplatesSDK(t *testing.T) {
	svc, err := dataflow.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	// Classic template launch (regional) → a Job inside LaunchTemplateResponse.
	launch, err := svc.Projects.Locations.Templates.Launch("tmpl-project", "us-central1", &dataflow.LaunchTemplateParameters{
		JobName: "tmpl-job",
	}).GcsPath("gs://dataflow-templates/word-count").Do()
	require.NoError(t, err)
	require.NotNil(t, launch.Job)
	assert.Equal(t, "tmpl-job", launch.Job.Name)

	// Classic template launch (global).
	glaunch, err := svc.Projects.Templates.Launch("tmpl-project", &dataflow.LaunchTemplateParameters{
		JobName: "tmpl-job-global",
	}).GcsPath("gs://dataflow-templates/word-count").Do()
	require.NoError(t, err)
	require.NotNil(t, glaunch.Job)

	// GetTemplate metadata.
	tmpl, err := svc.Projects.Locations.Templates.Get("tmpl-project", "us-central1").GcsPath("gs://dataflow-templates/word-count").Do()
	require.NoError(t, err)
	assert.Equal(t, "LEGACY", tmpl.TemplateType)

	// Flex template launch → a Job inside LaunchFlexTemplateResponse.
	flex, err := svc.Projects.Locations.FlexTemplates.Launch("tmpl-project", "us-central1", &dataflow.LaunchFlexTemplateRequest{
		LaunchParameter: &dataflow.LaunchFlexTemplateParameter{
			JobName:              "flex-job",
			ContainerSpecGcsPath: "gs://my-bucket/flex.json",
		},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, flex.Job)
	assert.Equal(t, "flex-job", flex.Job.Name)
}

func TestBigtable_InstanceClusterTableSDK(t *testing.T) {
	svc, err := bigtableadmin.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	op, err := svc.Projects.Instances.Create("projects/test-project", &bigtableadmin.CreateInstanceRequest{
		InstanceId: "sdk-bt",
		Instance: &bigtableadmin.Instance{
			DisplayName: "SDK Bigtable",
			Type:        "PRODUCTION",
			Labels:      map[string]string{"env": "sdk"},
		},
		Clusters: map[string]bigtableadmin.Cluster{
			"sdk-bt-c1": {
				Location:           "projects/test-project/locations/us-central1-a",
				ServeNodes:         1,
				DefaultStorageType: "SSD",
			},
		},
	}).Do()
	require.NoError(t, err)
	assert.True(t, op.Done)

	inst, err := svc.Projects.Instances.Get("projects/test-project/instances/sdk-bt").Do()
	require.NoError(t, err)
	assert.Equal(t, "READY", inst.State)

	cluster, err := svc.Projects.Instances.Clusters.Get("projects/test-project/instances/sdk-bt/clusters/sdk-bt-c1").Do()
	require.NoError(t, err)
	assert.Equal(t, "READY", cluster.State)

	table, err := svc.Projects.Instances.Tables.Create("projects/test-project/instances/sdk-bt", &bigtableadmin.CreateTableRequest{
		TableId: "sdk_table",
		Table: &bigtableadmin.Table{
			ColumnFamilies: map[string]bigtableadmin.ColumnFamily{
				"cf": {},
			},
		},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "projects/test-project/instances/sdk-bt/tables/sdk_table", table.Name)

	list, err := svc.Projects.Instances.Tables.List("projects/test-project/instances/sdk-bt").Do()
	require.NoError(t, err)
	require.Len(t, list.Tables, 1)
	assert.Equal(t, table.Name, list.Tables[0].Name)
}

// TestBigtable_AdminSurfaceSDK exercises the wider Bigtable Admin REST surface
// — instance IAM + PUT update, cluster partial-update / hot tablets / memory
// layer, app profiles, backups (+ copy + IAM), table colon verbs
// (modifyColumnFamilies / generateConsistencyToken / checkConsistency /
// dropRowRange / undelete / restore / IAM), authorized views, schema bundles,
// logical & materialized views, and locations / operations — via the official
// bigtableadmin/v2 SDK against the simulator.
func TestBigtable_AdminSurfaceSDK(t *testing.T) {
	svc, err := bigtableadmin.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	const project = "projects/bt-admin"
	const inst = project + "/instances/bt-admin-i"
	const clusterName = inst + "/clusters/bt-admin-c1"

	// Instance + cluster.
	op, err := svc.Projects.Instances.Create(project, &bigtableadmin.CreateInstanceRequest{
		InstanceId: "bt-admin-i",
		Instance:   &bigtableadmin.Instance{DisplayName: "Admin", Type: "PRODUCTION"},
		Clusters: map[string]bigtableadmin.Cluster{
			"bt-admin-c1": {Location: project + "/locations/us-central1-a", ServeNodes: 1, DefaultStorageType: "SSD"},
		},
	}).Do()
	require.NoError(t, err)
	assert.True(t, op.Done)

	// Locations + project operations.
	locs, err := svc.Projects.Locations.List(project).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, locs.Locations)
	_, err = svc.Operations.Projects.Operations.List("operations/" + project).Do()
	require.NoError(t, err)

	// Instance PUT update + IAM round-trip.
	upd, err := svc.Projects.Instances.Update(inst, &bigtableadmin.Instance{DisplayName: "Admin2", Type: "PRODUCTION"}).Do()
	require.NoError(t, err)
	assert.Equal(t, "Admin2", upd.DisplayName)

	setPol, err := svc.Projects.Instances.SetIamPolicy(inst, &bigtableadmin.SetIamPolicyRequest{
		Policy: &bigtableadmin.Policy{Bindings: []*bigtableadmin.Binding{{Role: "roles/bigtable.user", Members: []string{"user:a@example.com"}}}},
	}).Do()
	require.NoError(t, err)
	require.Len(t, setPol.Bindings, 1)
	gotPol, err := svc.Projects.Instances.GetIamPolicy(inst, &bigtableadmin.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, gotPol.Bindings, 1)
	perms, err := svc.Projects.Instances.TestIamPermissions(inst, &bigtableadmin.TestIamPermissionsRequest{Permissions: []string{"bigtable.tables.list"}}).Do()
	require.NoError(t, err)
	assert.Equal(t, []string{"bigtable.tables.list"}, perms.Permissions)

	// Cluster partial-update + hot tablets + memory layer.
	_, err = svc.Projects.Instances.Clusters.PartialUpdateCluster(clusterName, &bigtableadmin.Cluster{ServeNodes: 3}).
		UpdateMask("serveNodes").Do()
	require.NoError(t, err)
	cl, err := svc.Projects.Instances.Clusters.Get(clusterName).Do()
	require.NoError(t, err)
	assert.EqualValues(t, 3, cl.ServeNodes)
	hot, err := svc.Projects.Instances.Clusters.HotTablets.List(clusterName).Do()
	require.NoError(t, err)
	assert.Empty(t, hot.HotTablets)
	ml, err := svc.Projects.Instances.Clusters.GetMemoryLayer(clusterName + "/memoryLayer").Do()
	require.NoError(t, err)
	assert.Equal(t, "DISABLED", ml.State)
	require.NotEmpty(t, ml.Etag)
	memoryLayers, err := svc.Projects.Instances.Clusters.MemoryLayers.List(clusterName).Do()
	require.NoError(t, err)
	require.Len(t, memoryLayers.MemoryLayers, 1)
	assert.Equal(t, "DISABLED", memoryLayers.MemoryLayers[0].State)

	// updateMemoryLayer was published in Discovery before the generated Go
	// client acquired a call type. Drive its exact
	// PATCH /v2/projects/{project}/instances/{instance}/clusters/{cluster}/memoryLayer
	// route through the same authenticated OAuth2 transport used by the
	// official SDK.
	enableBody := `{"name":"` + clusterName + `/memoryLayer","memoryConfig":{},"etag":"` + ml.Etag + `"}`
	enableReq, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		baseURL+"/v2/"+clusterName+"/memoryLayer?updateMask=memoryConfig", strings.NewReader(enableBody))
	require.NoError(t, err)
	enableReq.Header.Set("Content-Type", "application/json")
	enableResp, err := simAuthHTTPClient().Do(enableReq)
	require.NoError(t, err)
	defer enableResp.Body.Close()
	enablePayload, err := io.ReadAll(enableResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, enableResp.StatusCode, string(enablePayload))
	var enableOp bigtableadmin.Operation
	require.NoError(t, json.Unmarshal(enablePayload, &enableOp))
	assert.True(t, enableOp.Done)

	ml, err = svc.Projects.Instances.Clusters.GetMemoryLayer(clusterName + "/memoryLayer").Do()
	require.NoError(t, err)
	assert.Equal(t, "READY", ml.State)
	require.NotNil(t, ml.MemoryConfig)
	require.NotEqual(t, memoryLayers.MemoryLayers[0].Etag, ml.Etag)

	staleReq, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		baseURL+"/v2/"+clusterName+"/memoryLayer?updateMask=memoryConfig", strings.NewReader(enableBody))
	require.NoError(t, err)
	staleReq.Header.Set("Content-Type", "application/json")
	staleResp, err := simAuthHTTPClient().Do(staleReq)
	require.NoError(t, err)
	defer staleResp.Body.Close()
	assert.Equal(t, http.StatusConflict, staleResp.StatusCode)

	disableBody := `{"name":"` + clusterName + `/memoryLayer","etag":"` + ml.Etag + `"}`
	disableReq, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		baseURL+"/v2/"+clusterName+"/memoryLayer?updateMask=memoryConfig", strings.NewReader(disableBody))
	require.NoError(t, err)
	disableReq.Header.Set("Content-Type", "application/json")
	disableResp, err := simAuthHTTPClient().Do(disableReq)
	require.NoError(t, err)
	defer disableResp.Body.Close()
	disablePayload, err := io.ReadAll(disableResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, disableResp.StatusCode, string(disablePayload))
	ml, err = svc.Projects.Instances.Clusters.GetMemoryLayer(clusterName + "/memoryLayer").Do()
	require.NoError(t, err)
	assert.Equal(t, "DISABLED", ml.State)
	assert.Nil(t, ml.MemoryConfig)

	// App profile.
	ap, err := svc.Projects.Instances.AppProfiles.Create(inst, &bigtableadmin.AppProfile{
		Description:               "ap",
		MultiClusterRoutingUseAny: &bigtableadmin.MultiClusterRoutingUseAny{},
	}).AppProfileId("bt-admin-ap").IgnoreWarnings(true).Do()
	require.NoError(t, err)
	assert.Equal(t, inst+"/appProfiles/bt-admin-ap", ap.Name)
	apList, err := svc.Projects.Instances.AppProfiles.List(inst).Do()
	require.NoError(t, err)
	require.Len(t, apList.AppProfiles, 1)
	_, err = svc.Projects.Instances.AppProfiles.Patch(ap.Name, &bigtableadmin.AppProfile{Description: "ap2"}).
		UpdateMask("description").IgnoreWarnings(true).Do()
	require.NoError(t, err)
	apGet, err := svc.Projects.Instances.AppProfiles.Get(ap.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "ap2", apGet.Description)

	// Table for the table-scoped surfaces.
	tbl, err := svc.Projects.Instances.Tables.Create(inst, &bigtableadmin.CreateTableRequest{
		TableId: "bt_admin_t",
		Table:   &bigtableadmin.Table{ColumnFamilies: map[string]bigtableadmin.ColumnFamily{"cf": {}}},
	}).Do()
	require.NoError(t, err)

	// Table colon verbs.
	mcfTbl, err := svc.Projects.Instances.Tables.ModifyColumnFamilies(tbl.Name, &bigtableadmin.ModifyColumnFamiliesRequest{
		Modifications: []*bigtableadmin.Modification{{Id: "cf2", Create: &bigtableadmin.ColumnFamily{}}},
	}).Do()
	require.NoError(t, err)
	assert.Contains(t, mcfTbl.ColumnFamilies, "cf2")
	tok, err := svc.Projects.Instances.Tables.GenerateConsistencyToken(tbl.Name, &bigtableadmin.GenerateConsistencyTokenRequest{}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, tok.ConsistencyToken)
	chk, err := svc.Projects.Instances.Tables.CheckConsistency(tbl.Name, &bigtableadmin.CheckConsistencyRequest{ConsistencyToken: tok.ConsistencyToken}).Do()
	require.NoError(t, err)
	assert.True(t, chk.Consistent)
	_, err = svc.Projects.Instances.Tables.DropRowRange(tbl.Name, &bigtableadmin.DropRowRangeRequest{DeleteAllDataFromTable: true}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Instances.Tables.Undelete(tbl.Name, &bigtableadmin.UndeleteTableRequest{}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Instances.Tables.Patch(tbl.Name, &bigtableadmin.Table{DeletionProtection: true}).
		UpdateMask("deletionProtection").Do()
	require.NoError(t, err)
	_, err = svc.Projects.Instances.Tables.SetIamPolicy(tbl.Name, &bigtableadmin.SetIamPolicyRequest{
		Policy: &bigtableadmin.Policy{Bindings: []*bigtableadmin.Binding{{Role: "roles/bigtable.reader", Members: []string{"user:b@example.com"}}}},
	}).Do()
	require.NoError(t, err)

	// Authorized views (table-scoped).
	avOp, err := svc.Projects.Instances.Tables.AuthorizedViews.Create(tbl.Name, &bigtableadmin.AuthorizedView{
		DeletionProtection: false,
	}).AuthorizedViewId("av1").Do()
	require.NoError(t, err)
	assert.True(t, avOp.Done)
	avName := tbl.Name + "/authorizedViews/av1"
	_, err = svc.Projects.Instances.Tables.AuthorizedViews.Get(avName).Do()
	require.NoError(t, err)
	avList, err := svc.Projects.Instances.Tables.AuthorizedViews.List(tbl.Name).Do()
	require.NoError(t, err)
	require.Len(t, avList.AuthorizedViews, 1)
	_, err = svc.Projects.Instances.Tables.AuthorizedViews.SetIamPolicy(avName, &bigtableadmin.SetIamPolicyRequest{
		Policy: &bigtableadmin.Policy{},
	}).Do()
	require.NoError(t, err)

	// Schema bundles (table-scoped).
	sbOp, err := svc.Projects.Instances.Tables.SchemaBundles.Create(tbl.Name, &bigtableadmin.SchemaBundle{}).
		SchemaBundleId("sb1").Do()
	require.NoError(t, err)
	assert.True(t, sbOp.Done)
	sbName := tbl.Name + "/schemaBundles/sb1"
	_, err = svc.Projects.Instances.Tables.SchemaBundles.Get(sbName).Do()
	require.NoError(t, err)
	sbList, err := svc.Projects.Instances.Tables.SchemaBundles.List(tbl.Name).Do()
	require.NoError(t, err)
	require.Len(t, sbList.SchemaBundles, 1)

	// Logical & materialized views (instance-scoped).
	lvOp, err := svc.Projects.Instances.LogicalViews.Create(inst, &bigtableadmin.LogicalView{Query: "SELECT 1"}).
		LogicalViewId("lv1").Do()
	require.NoError(t, err)
	assert.True(t, lvOp.Done)
	lvList, err := svc.Projects.Instances.LogicalViews.List(inst).Do()
	require.NoError(t, err)
	require.Len(t, lvList.LogicalViews, 1)
	mvOp, err := svc.Projects.Instances.MaterializedViews.Create(inst, &bigtableadmin.MaterializedView{Query: "SELECT 1"}).
		MaterializedViewId("mv1").Do()
	require.NoError(t, err)
	assert.True(t, mvOp.Done)
	mvList, err := svc.Projects.Instances.MaterializedViews.List(inst).Do()
	require.NoError(t, err)
	require.Len(t, mvList.MaterializedViews, 1)

	// Backups (cluster-scoped) + copy + IAM.
	bkOp, err := svc.Projects.Instances.Clusters.Backups.Create(clusterName, &bigtableadmin.Backup{
		SourceTable: tbl.Name,
		ExpireTime:  "2099-01-01T00:00:00Z",
	}).BackupId("bk1").Do()
	require.NoError(t, err)
	assert.True(t, bkOp.Done)
	bkName := clusterName + "/backups/bk1"
	_, err = svc.Projects.Instances.Clusters.Backups.Get(bkName).Do()
	require.NoError(t, err)
	bkList, err := svc.Projects.Instances.Clusters.Backups.List(clusterName).Do()
	require.NoError(t, err)
	require.Len(t, bkList.Backups, 1)
	_, err = svc.Projects.Instances.Clusters.Backups.Patch(bkName, &bigtableadmin.Backup{ExpireTime: "2100-01-01T00:00:00Z"}).
		UpdateMask("expireTime").Do()
	require.NoError(t, err)
	copyOp, err := svc.Projects.Instances.Clusters.Backups.Copy(clusterName, &bigtableadmin.CopyBackupRequest{
		BackupId:     "bk1-copy",
		SourceBackup: bkName,
		ExpireTime:   "2099-06-01T00:00:00Z",
	}).Do()
	require.NoError(t, err)
	assert.True(t, copyOp.Done)
	_, err = svc.Projects.Instances.Clusters.Backups.SetIamPolicy(bkName, &bigtableadmin.SetIamPolicyRequest{
		Policy: &bigtableadmin.Policy{},
	}).Do()
	require.NoError(t, err)

	// Restore a table from the backup.
	restoreOp, err := svc.Projects.Instances.Tables.Restore(inst, &bigtableadmin.RestoreTableRequest{
		TableId: "bt_admin_restored",
		Backup:  bkName,
	}).Do()
	require.NoError(t, err)
	assert.True(t, restoreOp.Done)
	restored, err := svc.Projects.Instances.Tables.Get(inst + "/tables/bt_admin_restored").Do()
	require.NoError(t, err)
	assert.Equal(t, inst+"/tables/bt_admin_restored", restored.Name)

	// Deletes across the new families.
	for _, del := range []func() error{
		func() error { _, e := svc.Projects.Instances.Tables.AuthorizedViews.Delete(avName).Do(); return e },
		func() error { _, e := svc.Projects.Instances.Tables.SchemaBundles.Delete(sbName).Do(); return e },
		func() error {
			_, e := svc.Projects.Instances.LogicalViews.Delete(inst + "/logicalViews/lv1").Do()
			return e
		},
		func() error {
			_, e := svc.Projects.Instances.MaterializedViews.Delete(inst + "/materializedViews/mv1").Do()
			return e
		},
		func() error { _, e := svc.Projects.Instances.Clusters.Backups.Delete(bkName).Do(); return e },
		func() error { _, e := svc.Projects.Instances.AppProfiles.Delete(ap.Name).Do(); return e },
	} {
		require.NoError(t, del())
	}
}
