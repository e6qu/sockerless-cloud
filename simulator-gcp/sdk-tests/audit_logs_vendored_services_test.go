package gcp_sdk_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"cloud.google.com/go/logging"
	"cloud.google.com/go/logging/logadmin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apigateway "google.golang.org/api/apigateway/v1"
	bigquery "google.golang.org/api/bigquery/v2"
	crmv1 "google.golang.org/api/cloudresourcemanager/v1"
	crm "google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/iterator"
	loggingrpc "google.golang.org/api/logging/v2"
	run "google.golang.org/api/run/v1"
	serviceusage "google.golang.org/api/serviceusage/v1"
	sqladmin "google.golang.org/api/sqladmin/v1"
	"google.golang.org/genproto/googleapis/cloud/audit"
)

// auditActivityEntriesIn reads a project's Admin Activity entries for a
// method on a resource.
func auditActivityEntriesIn(t *testing.T, project, methodName, resourceName string) []*logging.Entry {
	t.Helper()
	it := logadminClientFor(t, project).Entries(ctx, logadmin.Filter(
		`logName="projects/`+project+`/logs/cloudaudit.googleapis.com%2Factivity" AND protoPayload.methodName="`+
			methodName+`" AND protoPayload.resourceName="`+resourceName+`"`))
	var entries []*logging.Entry
	for {
		entry, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return entries
		}
		require.NoError(t, err)
		entries = append(entries, entry)
	}
}

func auditActivityEntryIn(t *testing.T, project, methodName, resourceName string) (*logging.Entry, *audit.AuditLog) {
	t.Helper()
	entries := auditActivityEntriesIn(t, project, methodName, resourceName)
	require.Len(t, entries, 1, "one Admin Activity entry for %s on %s", methodName, resourceName)
	payload, ok := entries[0].Payload.(*audit.AuditLog)
	require.True(t, ok, "the payload is a google.cloud.audit.AuditLog, got %T", entries[0].Payload)
	return entries[0], payload
}

// TestAuditLogs_LoggingServiceUsageAPIGatewayResourceManager drives one
// Admin Activity call of each service whose audit-logging page is vendored
// and reads the entry back under the methodName, permission and resource the
// page states.
func TestAuditLogs_LoggingServiceUsageAPIGatewayResourceManager(t *testing.T) {
	t.Parallel()
	const project = "test-project"

	sinkName := uniqueName("audit-sink")
	_, err := loggingRESTService(t).Projects.Sinks.Create("projects/"+project, &loggingrpc.LogSink{
		Name: sinkName, Destination: "storage.googleapis.com/" + sinkName,
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = loggingRESTService(t).Projects.Sinks.Delete("projects/" + project + "/sinks/" + sinkName).Do()
	})
	entry, payload := auditActivityEntryIn(t, project, "google.logging.v2.ConfigServiceV2.CreateSink", "projects/"+project+"/sinks/"+sinkName)
	assert.Equal(t, "logging.googleapis.com", payload.GetServiceName())
	assert.Equal(t, "logging.sinks.create", auditPermission(t, payload))
	assert.Equal(t, "type.googleapis.com/google.logging.v2.CreateSinkRequest", payload.GetRequest().GetFields()["@type"].GetStringValue())
	assert.Equal(t, "audited_resource", entry.Resource.GetType())

	suProject := uniqueName("audit-su")
	requireProject(t, suProject)
	const enabled = "pubsub.googleapis.com"
	op, err := serviceUsageService(t).Services.Enable("projects/"+suProject+"/services/"+enabled, &serviceusage.EnableServiceRequest{}).Do()
	require.NoError(t, err)
	entry, payload = auditActivityEntryIn(t, suProject, "google.api.serviceusage.v1.ServiceUsage.EnableService", "projects/"+suProject+"/services/"+enabled)
	assert.Equal(t, "serviceusage.googleapis.com", payload.GetServiceName())
	assert.Equal(t, "serviceusage.services.enable", auditPermission(t, payload))
	require.NotNil(t, entry.Operation, "EnableService is a long-running operation")
	assert.Equal(t, op.Name, entry.Operation.GetId())
	assert.Equal(t, "serviceusage.googleapis.com", entry.Operation.GetProducer())
	assert.True(t, entry.Operation.GetFirst())

	apiID := uniqueName("audit-api")
	gateways := apigatewayService(t)
	apiOp, err := gateways.Projects.Locations.Apis.Create("projects/"+project+"/locations/global", &apigateway.ApigatewayApi{DisplayName: "audit"}).ApiId(apiID).Do()
	require.NoError(t, err)
	awaitAPIGatewayLRO(t, gateways, apiOp)
	apiName := "projects/" + project + "/locations/global/apis/" + apiID
	t.Cleanup(func() { _, _ = gateways.Projects.Locations.Apis.Delete(apiName).Do() })
	entry, payload = auditActivityEntryIn(t, project, "google.cloud.apigateway.v1.ApiGatewayService.CreateApi", apiName)
	assert.Equal(t, "apigateway.googleapis.com", payload.GetServiceName())
	assert.Equal(t, "apigateway.apis.create", auditPermission(t, payload))
	require.NotNil(t, entry.Operation, "CreateApi is a long-running operation")
	assert.Equal(t, apiOp.Name, entry.Operation.GetId())
	assert.True(t, entry.Operation.GetFirst())

	tags := crmV3Service(t)
	keyOp, err := tags.TagKeys.Create(&crm.TagKey{Parent: "projects/" + project, ShortName: uniqueName("audit")}).Do()
	require.NoError(t, err)
	tagKey := crmOpResourceName(t, keyOp)
	t.Cleanup(func() { _, _ = tags.TagKeys.Delete(tagKey).Do() })
	entry, payload = auditActivityEntryIn(t, project, "CreateTagKey", tagKey)
	assert.Equal(t, "cloudresourcemanager.googleapis.com", payload.GetServiceName())
	assert.Equal(t, "resourcemanager.tagKeys.create", auditPermission(t, payload))
	assert.Equal(t, project, entry.Resource.GetLabels()["project_id"])

	// A tag key's reads are Data Access entries, written only when enabled;
	// the Admin Activity log holds none for them.
	_, err = tags.TagKeys.Get(tagKey).Do()
	require.NoError(t, err)
	assert.Empty(t, auditActivityEntriesIn(t, project, "GetTagKey", tagKey))
}

// TestAuditLogs_BigQueryCloudSQLCloudRunV1 covers the Discovery-defined
// services: BigQuery records google.cloud.bigquery.v2.DatasetService.InsertDataset
// on the bigquery_dataset resource, Cloud SQL cloudsql.instances.create with
// the operation it answered, and Cloud Run Admin v1
// google.cloud.run.v1.Jobs.CreateJob on the cloud_run_job resource.
func TestAuditLogs_BigQueryCloudSQLCloudRunV1(t *testing.T) {
	t.Parallel()
	const project = "test-project"

	dataset := "audit_ds_" + uniqueName("")[1:]
	bq := bqService(t)
	_, err := bq.Datasets.Insert(project, &bigquery.Dataset{
		DatasetReference: &bigquery.DatasetReference{ProjectId: project, DatasetId: dataset},
		Location:         "US",
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _ = bq.Datasets.Delete(project, dataset).Do() })
	entry, payload := auditActivityEntryIn(t, project, "google.cloud.bigquery.v2.DatasetService.InsertDataset", "projects/"+project+"/datasets/"+dataset)
	assert.Equal(t, "bigquery.googleapis.com", payload.GetServiceName())
	assert.Equal(t, "bigquery.datasets.create", auditPermission(t, payload))
	assert.Equal(t, "bigquery_dataset", entry.Resource.GetType())
	assert.Equal(t, dataset, entry.Resource.GetLabels()["dataset_id"])

	sql := sqlAdminService(t)
	instance := uniqueName("audit-sql")
	insertOp, err := sql.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name: instance, Region: "us-central1", DatabaseVersion: "POSTGRES_15", RootPassword: "RootPassword-123!",
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sql.Instances.Delete(project, instance).Do() })
	waitSQLOperationDone(t, sql, project, insertOp.Name)
	entry, payload = auditActivityEntryIn(t, project, "cloudsql.instances.create", "projects/"+project+"/instances/"+instance)
	assert.Equal(t, "cloudsql.googleapis.com", payload.GetServiceName())
	assert.Equal(t, "cloudsql.instances.create", auditPermission(t, payload))
	assert.Equal(t, "cloudsql_database", entry.Resource.GetType())
	assert.Equal(t, instance, entry.Resource.GetLabels()["database_id"])
	assert.Equal(t, "us-central1", entry.Resource.GetLabels()["region"])
	require.NotNil(t, entry.Operation, "the create answered an operation")
	assert.Equal(t, insertOp.Name, entry.Operation.GetId())
	assert.True(t, entry.Operation.GetFirst() && entry.Operation.GetLast(), "the operation was done when the call answered")

	jobs := newRunV1(t)
	jobID := uniqueName("audit-v1-job")
	_, err = jobs.Namespaces.Jobs.Create("namespaces/"+project, &run.Job{
		Metadata: &run.ObjectMeta{Name: jobID},
		Spec: &run.JobSpec{Template: &run.ExecutionTemplateSpec{Spec: &run.ExecutionSpec{
			Template: &run.TaskTemplateSpec{Spec: &run.TaskSpec{
				Containers: []*run.Container{{Image: simWorkloadImage, Args: []string{"true"}}},
			}},
		}}},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = jobs.Namespaces.Jobs.Delete("namespaces/" + project + "/jobs/" + jobID).Do() })
	entry, payload = auditActivityEntryIn(t, project, "google.cloud.run.v1.Jobs.CreateJob", "namespaces/"+project+"/jobs/"+jobID)
	assert.Equal(t, "run.googleapis.com", payload.GetServiceName())
	assert.Equal(t, "run.jobs.create", auditPermission(t, payload))
	assert.Equal(t, "cloud_run_job", entry.Resource.GetType())
	assert.Equal(t, jobID, entry.Resource.GetLabels()["job_name"])
}

// TestAuditLogs_OperationLastEntries covers the operations whose records live
// outside the shared operation stores: Resource Manager v1's CreateProject
// answers a done operation, so its one entry is both first and last, and
// Cloud Build's CreateBuild writes its last entry once the build has run.
func TestAuditLogs_OperationLastEntries(t *testing.T) {
	t.Parallel()

	projectID := uniqueName("audit-create")
	op, err := crmService(t).Projects.Create(&crmv1.Project{ProjectId: projectID}).Do()
	require.NoError(t, err)
	entry, payload := auditActivityEntryIn(t, projectID, "CreateProject", "projects/"+projectID)
	assert.Equal(t, "resourcemanager.projects.create", auditPermission(t, payload))
	require.NotNil(t, entry.Operation, "CreateProject answers an operation")
	assert.Equal(t, op.Name, entry.Operation.GetId())
	assert.Equal(t, "cloudresourcemanager.googleapis.com", entry.Operation.GetProducer())
	assert.True(t, entry.Operation.GetFirst() && entry.Operation.GetLast(), "the operation was done when the call answered")

	pullImageWithRetry(t, cbBaseImage)
	const buildProject = "cb-test-project"
	const bucket = "cb-test-bucket"
	createBucket(t, buildProject, bucket)
	object := fmt.Sprintf("audit-context-%d.tar.gz", time.Now().UnixNano())
	uploadGCSObject(t, bucket, object, makeTarGz(t, map[string]string{"README": "audit\n"}))
	started := readStartedBuild(t, httpPOST(t, fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, buildProject), fmt.Sprintf(`{
		"source":{"storageSource":{"bucket":%q,"object":%q}},
		"steps":[{"name":%q,"args":["true"]}]
	}`, bucket, object, cbBaseImage)))
	_, err = waitBuild(t, started)
	require.NoError(t, err)

	var first, last *logging.Entry
	require.Eventually(t, func() bool {
		first, last = nil, nil
		for _, entry := range auditActivityEntriesIn(t, buildProject, "google.devtools.cloudbuild.v1.CloudBuild.CreateBuild", "projects/"+buildProject+"/builds") {
			if entry.Operation.GetId() != started.Operation {
				continue
			}
			if entry.Operation.GetFirst() {
				first = entry
			}
			if entry.Operation.GetLast() {
				last = entry
			}
		}
		return first != nil && last != nil
	}, 30*time.Second, 200*time.Millisecond, "CreateBuild writes a first entry when it answers and a last one when the build ends")
	assert.NotSame(t, first, last, "the build was not done when the call answered")
	assert.Equal(t, "cloudbuild.googleapis.com", first.Operation.GetProducer())
	lastPayload := last.Payload.(*audit.AuditLog)
	assert.Equal(t, "type.googleapis.com/google.devtools.cloudbuild.v1.Build", lastPayload.GetResponse().GetFields()["@type"].GetStringValue(),
		"the last entry carries the finished build")
}
