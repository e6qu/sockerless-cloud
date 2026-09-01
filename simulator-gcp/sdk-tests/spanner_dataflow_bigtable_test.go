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
	storageapi "google.golang.org/api/storage/v1"
)

// awaitSpannerLRO reads a Cloud Spanner long-running operation to its terminal
// state through the operations collection its own name addresses, and returns
// the finished record. That is how a client learns the outcome, and it holds
// whether the service settles the work inside the request or later.
func awaitSpannerLRO(t *testing.T, name string, get func() (*spanner.Operation, error)) *spanner.Operation {
	t.Helper()
	require.NotEmpty(t, name, "a long-running method answers with the operation it started")
	settled := awaitLRO(t, name, get, func(o *spanner.Operation) bool { return o.Done })
	assert.Equal(t, name, settled.Name)
	assert.Nil(t, settled.Error, "operation %s finished with an error", name)
	return settled
}

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
	awaitSpannerLRO(t, op.Name, func() (*spanner.Operation, error) {
		return svc.Projects.Instances.Operations.Get(op.Name).Do()
	})

	inst, err := svc.Projects.Instances.Get("projects/test-project/instances/sdk-spanner").Do()
	require.NoError(t, err)
	assert.Equal(t, "READY", inst.State)

	dbOp, err := svc.Projects.Instances.Databases.Create("projects/test-project/instances/sdk-spanner", &spanner.CreateDatabaseRequest{
		CreateStatement: "CREATE DATABASE `sdkdb`",
	}).Do()
	require.NoError(t, err)
	awaitSpannerLRO(t, dbOp.Name, func() (*spanner.Operation, error) {
		return svc.Projects.Instances.Databases.Operations.Get(dbOp.Name).Do()
	})
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
	awaitSpannerLRO(t, ddlOp.Name, func() (*spanner.Operation, error) {
		return svc.Projects.Instances.Databases.Operations.Get(ddlOp.Name).Do()
	})
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
	awaitSpannerLRO(t, op.Name, func() (*spanner.Operation, error) {
		return svc.Projects.InstanceConfigs.Operations.Get(op.Name).Do()
	})

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
	awaitSpannerLRO(t, patchOp.Name, func() (*spanner.Operation, error) {
		return svc.Projects.InstanceConfigs.Operations.Get(patchOp.Name).Do()
	})

	updated, err := svc.Projects.InstanceConfigs.Get(cfg.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "SDK Renamed Config", updated.DisplayName)
	assert.Equal(t, "USER_MANAGED", updated.ConfigType,
		"a displayName-masked update leaves the rest of the configuration alone")
	require.Len(t, updated.Replicas, 2)

	// Instance configuration LRO surfaces: list at config level and at the
	// project-wide instanceConfigOperations collection, then read one back.
	cfgOps, err := svc.Projects.InstanceConfigs.Operations.List(cfg.Name + "/operations").Do()
	require.NoError(t, err)
	require.NotEmpty(t, cfgOps.Operations)
	cfgOpNames := make([]string, 0, len(cfgOps.Operations))
	for _, o := range cfgOps.Operations {
		assert.True(t, strings.HasPrefix(o.Name, cfg.Name+"/operations/"),
			"the collection holds this configuration's operations, got %q", o.Name)
		cfgOpNames = append(cfgOpNames, o.Name)
	}
	assert.Contains(t, cfgOpNames, op.Name, "the create's operation is in the configuration's collection")
	assert.Contains(t, cfgOpNames, patchOp.Name, "so is the update's")
	gotOp, err := svc.Projects.InstanceConfigs.Operations.Get(cfgOps.Operations[0].Name).Do()
	require.NoError(t, err)
	assert.Equal(t, cfgOps.Operations[0].Name, gotOp.Name)

	collOps, err := svc.Projects.InstanceConfigOperations.List(parent).Do()
	require.NoError(t, err)
	collOpNames := make([]string, 0, len(collOps.Operations))
	for _, o := range collOps.Operations {
		collOpNames = append(collOpNames, o.Name)
	}
	assert.Contains(t, collOpNames, op.Name,
		"the project-wide instanceConfigOperations collection sees the configuration's operations")

	// The ssdCaches operations sub-collection is a different collection: the
	// configuration's own operations are not in it. Its emptiness here is the
	// scoping assertion — a handler that answered with the parent's operations
	// would return the two above.
	ssdOps, err := svc.Projects.InstanceConfigs.SsdCaches.Operations.List(cfg.Name + "/ssdCaches/cache-1/operations").Do()
	require.NoError(t, err)
	for _, o := range ssdOps.Operations {
		assert.True(t, strings.HasPrefix(o.Name, cfg.Name+"/ssdCaches/cache-1/operations/"),
			"the ssdCaches collection holds only its own operations, got %q", o.Name)
	}
	assert.Empty(t, ssdOps.Operations, "no long-running work was started against this cache")

	_, err = svc.Projects.InstanceConfigs.Delete(cfg.Name).Do()
	require.NoError(t, err)
	_, err = svc.Projects.InstanceConfigs.Get(cfg.Name).Do()
	require.Error(t, err, "deleted config should not be gettable")
	afterDelete, err := svc.Projects.InstanceConfigs.List(parent).Do()
	require.NoError(t, err)
	for _, c := range afterDelete.InstanceConfigs {
		assert.NotEqual(t, cfg.Name, c.Name, "the deleted configuration is out of the listing too")
	}
}

// scans.list is the one Cloud Spanner collection with no method that creates a
// member: a scan is produced by the service's own key-visualizer analysis, which
// this simulator does not run, so an empty listing is the whole of the observable
// behaviour. The assertion here is the route and the response shape the generated
// client has to decode.
func TestSpanner_ScansListSDK(t *testing.T) {
	svc, err := spanner.NewService(ctx, option.WithEndpoint(baseURL+"/spanner/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)

	resp, err := svc.Scans.List("scans").Do()
	require.NoError(t, err)
	assert.Empty(t, resp.Scans)
	assert.Empty(t, resp.NextPageToken, "an empty page is the last page")
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

	// Messages, metrics and execution details are all about the job they name:
	// each resolves the job id and reports NOT_FOUND for one no create minted,
	// rather than answering the same way whatever it is asked about.
	msgs, err := svc.Projects.Locations.Jobs.Messages.List("test-project", "us-central1", job.Id).Do()
	require.NoError(t, err)
	require.NotEmpty(t, msgs.JobMessages)
	for _, m := range msgs.JobMessages {
		assert.NotEmpty(t, m.MessageImportance, "every job message carries its importance")
		assert.NotEmpty(t, m.Time, "every job message carries its time")
	}
	_, err = svc.Projects.Locations.Jobs.Messages.List("test-project", "us-central1", "no-such-job").Do()
	require.Error(t, err, "messages.list resolves the job it is asked about")
	assert.Contains(t, err.Error(), "404")

	metrics, err := svc.Projects.Locations.Jobs.GetMetrics("test-project", "us-central1", job.Id).Do()
	require.NoError(t, err)
	require.NotEmpty(t, metrics.Metrics)
	for _, m := range metrics.Metrics {
		require.NotNil(t, m.Name)
		assert.NotEmpty(t, m.Name.Name, "every metric update names its metric")
	}
	assert.NotEmpty(t, metrics.MetricTime)
	_, err = svc.Projects.Locations.Jobs.GetMetrics("test-project", "us-central1", "no-such-job").Do()
	require.Error(t, err, "getMetrics resolves the job it is asked about")
	assert.Contains(t, err.Error(), "404")

	exec, err := svc.Projects.Locations.Jobs.GetExecutionDetails("test-project", "us-central1", job.Id).Do()
	require.NoError(t, err)
	require.NotEmpty(t, exec.Stages)
	for _, s := range exec.Stages {
		assert.NotEmpty(t, s.StageId, "every stage carries its id")
	}
	_, err = svc.Projects.Locations.Jobs.GetExecutionDetails("test-project", "us-central1", "no-such-job").Do()
	require.Error(t, err, "getExecutionDetails resolves the job it is asked about")
	assert.Contains(t, err.Error(), "404")
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
	require.NotEmpty(t, metrics.Metrics)
	_, err = svc.Projects.Jobs.GetMetrics("global-df-project", "no-such-job").Do()
	require.Error(t, err, "the global getMetrics resolves the job it is asked about too")
	assert.Contains(t, err.Error(), "404")
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
	// The launch started a job, so the jobs collection answers about it.
	launched, err := svc.Projects.Locations.Jobs.Get("tmpl-project", "us-central1", launch.Job.Id).Do()
	require.NoError(t, err, "a launched template's job is in the regional jobs collection")
	assert.Equal(t, "tmpl-job", launched.Name)
	assert.Equal(t, "JOB_STATE_RUNNING", launched.CurrentState)

	// Classic template launch (global).
	glaunch, err := svc.Projects.Templates.Launch("tmpl-project", &dataflow.LaunchTemplateParameters{
		JobName: "tmpl-job-global",
	}).GcsPath("gs://dataflow-templates/word-count").Do()
	require.NoError(t, err)
	require.NotNil(t, glaunch.Job)

	// GetTemplate reads the template the caller staged, and its metadata from
	// the sibling file Dataflow's tooling writes beside it. A path nothing was
	// staged at is not a template — answering one for whatever path was asked
	// about described a template nobody had.
	storage := storageService(t)
	_, err = storage.Buckets.Insert("tmpl-project", &storageapi.Bucket{Name: "staged-templates"}).Do()
	require.NoError(t, err)

	missing, err := svc.Projects.Locations.Templates.Get("tmpl-project", "us-central1").
		GcsPath("gs://staged-templates/never-staged").Do()
	require.Error(t, err, "a path nothing was staged at is not a template")
	require.Nil(t, missing)

	_, err = storage.Objects.Insert("staged-templates", &storageapi.Object{Name: "word-count"}).
		Media(strings.NewReader(`{"name":"word-count"}`)).Do()
	require.NoError(t, err)
	_, err = storage.Objects.Insert("staged-templates", &storageapi.Object{Name: "word-count_metadata"}).
		Media(strings.NewReader(`{"name":"Word Count","parameters":[{"name":"inputFile","label":"Input"}]}`)).Do()
	require.NoError(t, err)

	tmpl, err := svc.Projects.Locations.Templates.Get("tmpl-project", "us-central1").
		GcsPath("gs://staged-templates/word-count").Do()
	require.NoError(t, err)
	require.NotNil(t, tmpl.Status)
	assert.NotEmpty(t, tmpl.TemplateType)
	require.NotNil(t, tmpl.Metadata)
	assert.Equal(t, "Word Count", tmpl.Metadata.Name,
		"the name must come from the staged metadata file, not from the simulator")
	require.Len(t, tmpl.Metadata.Parameters, 1)
	assert.Equal(t, "inputFile", tmpl.Metadata.Parameters[0].Name)

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
	flexed, err := svc.Projects.Locations.Jobs.Get("tmpl-project", "us-central1", flex.Job.Id).Do()
	require.NoError(t, err, "a launched flex template's job is in the regional jobs collection")
	assert.Equal(t, "flex-job", flexed.Name)
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

	// Locations + project operations. Bigtable admin files its long-running
	// operations in the flat operations/{id} collection, and the create above
	// is one of them, so the listing has to contain it.
	locs, err := svc.Projects.Locations.List(project).Do()
	require.NoError(t, err)
	assert.NotEmpty(t, locs.Locations)
	projectOps, err := svc.Operations.Projects.Operations.List("operations/" + project).Do()
	require.NoError(t, err)
	require.NotEmpty(t, projectOps.Operations, "the instance create filed an operation")
	opNames := make([]string, 0, len(projectOps.Operations))
	for _, o := range projectOps.Operations {
		assert.True(t, strings.HasPrefix(o.Name, "operations/"),
			"a Bigtable admin operation is named in the flat collection, got %q", o.Name)
		opNames = append(opNames, o.Name)
	}
	assert.Contains(t, opNames, op.Name, "the create's own operation is in the listing")

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
	// The drop is about the table it names, and it leaves the table and its
	// schema in place — it removes rows, not the table.
	afterDrop, err := svc.Projects.Instances.Tables.Get(tbl.Name).Do()
	require.NoError(t, err)
	assert.Contains(t, afterDrop.ColumnFamilies, "cf")
	assert.Contains(t, afterDrop.ColumnFamilies, "cf2")
	_, err = svc.Projects.Instances.Tables.DropRowRange(inst+"/tables/no_such_table",
		&bigtableadmin.DropRowRangeRequest{DeleteAllDataFromTable: true}).Do()
	require.Error(t, err, "dropRowRange resolves the table it is asked about")
	assert.Contains(t, err.Error(), "404")

	_, err = svc.Projects.Instances.Tables.Undelete(tbl.Name, &bigtableadmin.UndeleteTableRequest{}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.Instances.Tables.Undelete(inst+"/tables/no_such_table", &bigtableadmin.UndeleteTableRequest{}).Do()
	require.Error(t, err, "undelete resolves the table it is asked about")
	assert.Contains(t, err.Error(), "404")

	// A masked patch writes the field it names, and a read confirms it stuck.
	_, err = svc.Projects.Instances.Tables.Patch(tbl.Name, &bigtableadmin.Table{DeletionProtection: true}).
		UpdateMask("deletionProtection").Do()
	require.NoError(t, err)
	protected, err := svc.Projects.Instances.Tables.Get(tbl.Name).Do()
	require.NoError(t, err)
	assert.True(t, protected.DeletionProtection, "the patched deletionProtection persisted")
	assert.Contains(t, protected.ColumnFamilies, "cf2", "the masked patch left the schema alone")

	_, err = svc.Projects.Instances.Tables.SetIamPolicy(tbl.Name, &bigtableadmin.SetIamPolicyRequest{
		Policy: &bigtableadmin.Policy{Bindings: []*bigtableadmin.Binding{{Role: "roles/bigtable.reader", Members: []string{"user:b@example.com"}}}},
	}).Do()
	require.NoError(t, err)
	tablePol, err := svc.Projects.Instances.Tables.GetIamPolicy(tbl.Name, &bigtableadmin.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, tablePol.Bindings, 1, "the table's policy is the one that was set on it")
	assert.Equal(t, "roles/bigtable.reader", tablePol.Bindings[0].Role)
	assert.Equal(t, []string{"user:b@example.com"}, tablePol.Bindings[0].Members)
	// The table's policy is its own: setting it did not overwrite the
	// instance's.
	instPolAfter, err := svc.Projects.Instances.GetIamPolicy(inst, &bigtableadmin.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, instPolAfter.Bindings, 1)
	assert.Equal(t, "roles/bigtable.user", instPolAfter.Bindings[0].Role)

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
		Policy: &bigtableadmin.Policy{Bindings: []*bigtableadmin.Binding{
			{Role: "roles/bigtable.viewer", Members: []string{"user:av@example.com"}},
		}},
	}).Do()
	require.NoError(t, err)
	avPol, err := svc.Projects.Instances.Tables.AuthorizedViews.GetIamPolicy(avName, &bigtableadmin.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, avPol.Bindings, 1, "the authorized view keeps the policy set on it")
	assert.Equal(t, "roles/bigtable.viewer", avPol.Bindings[0].Role)
	assert.Equal(t, []string{"user:av@example.com"}, avPol.Bindings[0].Members)

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
	patchedBk, err := svc.Projects.Instances.Clusters.Backups.Get(bkName).Do()
	require.NoError(t, err)
	assert.Equal(t, "2100-01-01T00:00:00Z", patchedBk.ExpireTime, "the masked patch moved the expiry")
	assert.Equal(t, tbl.Name, patchedBk.SourceTable, "and left the source table alone")

	copyOp, err := svc.Projects.Instances.Clusters.Backups.Copy(clusterName, &bigtableadmin.CopyBackupRequest{
		BackupId:     "bk1-copy",
		SourceBackup: bkName,
		ExpireTime:   "2099-06-01T00:00:00Z",
	}).Do()
	require.NoError(t, err)
	require.True(t, copyOp.Done)
	// The copy made a second backup: it is in the cluster's collection under
	// the id the request asked for, with the expiry the request asked for.
	copyName := clusterName + "/backups/bk1-copy"
	copied, err := svc.Projects.Instances.Clusters.Backups.Get(copyName).Do()
	require.NoError(t, err, "the copy is a backup the cluster holds")
	assert.Equal(t, copyName, copied.Name)
	assert.Equal(t, "2099-06-01T00:00:00Z", copied.ExpireTime)
	bkListAfterCopy, err := svc.Projects.Instances.Clusters.Backups.List(clusterName).Do()
	require.NoError(t, err)
	require.Len(t, bkListAfterCopy.Backups, 2, "the source and its copy are both in the collection")

	_, err = svc.Projects.Instances.Clusters.Backups.SetIamPolicy(bkName, &bigtableadmin.SetIamPolicyRequest{
		Policy: &bigtableadmin.Policy{Bindings: []*bigtableadmin.Binding{
			{Role: "roles/bigtable.viewer", Members: []string{"user:bk@example.com"}},
		}},
	}).Do()
	require.NoError(t, err)
	bkPol, err := svc.Projects.Instances.Clusters.Backups.GetIamPolicy(bkName, &bigtableadmin.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, bkPol.Bindings, 1, "the backup keeps the policy set on it")
	assert.Equal(t, "roles/bigtable.viewer", bkPol.Bindings[0].Role)
	assert.Equal(t, []string{"user:bk@example.com"}, bkPol.Bindings[0].Members)
	// The backup's policy is its own, not the copy's.
	copyPol, err := svc.Projects.Instances.Clusters.Backups.GetIamPolicy(copyName, &bigtableadmin.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	assert.Empty(t, copyPol.Bindings, "nothing was bound on the copy")

	// Restore a table from the backup. The restored table is a table of the
	// instance: it is gettable by the name the request asked for and it shows
	// up in the instance's listing alongside the one it was backed up from.
	restoreOp, err := svc.Projects.Instances.Tables.Restore(inst, &bigtableadmin.RestoreTableRequest{
		TableId: "bt_admin_restored",
		Backup:  bkName,
	}).Do()
	require.NoError(t, err)
	require.True(t, restoreOp.Done)
	restored, err := svc.Projects.Instances.Tables.Get(inst + "/tables/bt_admin_restored").Do()
	require.NoError(t, err)
	assert.Equal(t, inst+"/tables/bt_admin_restored", restored.Name)
	assert.Equal(t, "MILLIS", restored.Granularity)
	tblList, err := svc.Projects.Instances.Tables.List(inst).Do()
	require.NoError(t, err)
	restoredNames := make([]string, 0, len(tblList.Tables))
	for _, x := range tblList.Tables {
		restoredNames = append(restoredNames, x.Name)
	}
	assert.Contains(t, restoredNames, restored.Name)
	assert.Contains(t, restoredNames, tbl.Name)

	// A restore with no table id has nothing to create.
	_, err = svc.Projects.Instances.Tables.Restore(inst, &bigtableadmin.RestoreTableRequest{
		Backup: bkName,
	}).Do()
	require.Error(t, err, "restore requires the tableId it creates")
	assert.Contains(t, err.Error(), "400")
	// A restore into an instance the service does not hold is NOT_FOUND.
	_, err = svc.Projects.Instances.Tables.Restore(project+"/instances/no-such-instance",
		&bigtableadmin.RestoreTableRequest{TableId: "x", Backup: bkName}).Do()
	require.Error(t, err, "restore resolves the instance it restores into")
	assert.Contains(t, err.Error(), "404")

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
