package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	bigquery "google.golang.org/api/bigquery/v2"
)

// bqService is shared with data_saas_coverage_test.go — it builds a BigQuery
// v2 REST client pointed at the simulator using the canonical
// google.golang.org/api construction (WithEndpoint sets the full BasePath,
// which for this client includes the /bigquery/v2/ path).

// bqMakeDataset creates a dataset fixture and returns its ID.
func bqMakeDataset(t *testing.T, svc *bigquery.Service, project, dataset string) {
	t.Helper()
	_, err := svc.Datasets.Insert(project, &bigquery.Dataset{
		DatasetReference: &bigquery.DatasetReference{ProjectId: project, DatasetId: dataset},
	}).Do()
	require.NoError(t, err)
}

// bqMakeTable creates a table fixture under an existing dataset.
func bqMakeTable(t *testing.T, svc *bigquery.Service, project, dataset, table string) {
	t.Helper()
	_, err := svc.Tables.Insert(project, dataset, &bigquery.Table{
		TableReference: &bigquery.TableReference{ProjectId: project, DatasetId: dataset, TableId: table},
	}).Do()
	require.NoError(t, err)
}

func TestBigQueryRoutines_CRUD(t *testing.T) {
	svc := bqService(t)
	const project, dataset, routine = "sock-proj", "ds_routines", "add_one"
	bqMakeDataset(t, svc, project, dataset)

	created, err := svc.Routines.Insert(project, dataset, &bigquery.Routine{
		RoutineReference: &bigquery.RoutineReference{ProjectId: project, DatasetId: dataset, RoutineId: routine},
		RoutineType:      "SCALAR_FUNCTION",
		Language:         "SQL",
		DefinitionBody:   "x + 1",
	}).Do()
	require.NoError(t, err)
	require.Equal(t, routine, created.RoutineReference.RoutineId)
	require.Equal(t, "SCALAR_FUNCTION", created.RoutineType)
	require.NotEmpty(t, created.Etag)
	require.NotEmpty(t, created.CreationTime)

	got, err := svc.Routines.Get(project, dataset, routine).Do()
	require.NoError(t, err)
	require.Equal(t, "x + 1", got.DefinitionBody)
	require.Equal(t, "SQL", got.Language)

	list, err := svc.Routines.List(project, dataset).Do()
	require.NoError(t, err)
	require.Len(t, list.Routines, 1)
	require.Equal(t, routine, list.Routines[0].RoutineReference.RoutineId)

	updated, err := svc.Routines.Update(project, dataset, routine, &bigquery.Routine{
		RoutineReference: &bigquery.RoutineReference{ProjectId: project, DatasetId: dataset, RoutineId: routine},
		RoutineType:      "SCALAR_FUNCTION",
		Language:         "SQL",
		DefinitionBody:   "x + 2",
		Description:      "increment by two",
	}).Do()
	require.NoError(t, err)
	require.Equal(t, "x + 2", updated.DefinitionBody)
	require.Equal(t, "increment by two", updated.Description)
	// CreationTime is preserved across the full PUT replacement.
	require.Equal(t, created.CreationTime, updated.CreationTime)

	require.NoError(t, svc.Routines.Delete(project, dataset, routine).Do())
	_, err = svc.Routines.Get(project, dataset, routine).Do()
	require.Error(t, err)
}

func TestBigQueryRoutines_IAM(t *testing.T) {
	svc := bqService(t)
	const project, dataset, routine = "sock-proj", "ds_routine_iam", "scored"
	bqMakeDataset(t, svc, project, dataset)
	_, err := svc.Routines.Insert(project, dataset, &bigquery.Routine{
		RoutineReference: &bigquery.RoutineReference{ProjectId: project, DatasetId: dataset, RoutineId: routine},
		RoutineType:      "SCALAR_FUNCTION",
		Language:         "SQL",
		DefinitionBody:   "1",
	}).Do()
	require.NoError(t, err)

	resource := "projects/" + project + "/datasets/" + dataset + "/routines/" + routine
	set, err := svc.Routines.SetIamPolicy(resource, &bigquery.SetIamPolicyRequest{
		Policy: &bigquery.Policy{
			Bindings: []*bigquery.Binding{{
				Role:    "roles/bigquery.dataViewer",
				Members: []string{"user:alice@example.com"},
			}},
		},
	}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)

	got, err := svc.Routines.GetIamPolicy(resource, &bigquery.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, got.Bindings, 1)
	require.Equal(t, "roles/bigquery.dataViewer", got.Bindings[0].Role)

	test, err := svc.Routines.TestIamPermissions(resource, &bigquery.TestIamPermissionsRequest{
		Permissions: []string{"bigquery.routines.get"},
	}).Do()
	require.NoError(t, err)
	require.Equal(t, []string{"bigquery.routines.get"}, test.Permissions)
}

func TestBigQueryRowAccessPolicies(t *testing.T) {
	svc := bqService(t)
	const project, dataset, table = "sock-proj", "ds_rap", "sales"
	const p1, p2 = "eu_only", "us_only"
	bqMakeDataset(t, svc, project, dataset)
	bqMakeTable(t, svc, project, dataset, table)

	created, err := svc.RowAccessPolicies.Insert(project, dataset, table, &bigquery.RowAccessPolicy{
		RowAccessPolicyReference: &bigquery.RowAccessPolicyReference{
			ProjectId: project, DatasetId: dataset, TableId: table, PolicyId: p1,
		},
		FilterPredicate: "region = 'EU'",
	}).Do()
	require.NoError(t, err)
	require.Equal(t, p1, created.RowAccessPolicyReference.PolicyId)
	require.Equal(t, "region = 'EU'", created.FilterPredicate)
	require.NotEmpty(t, created.Etag)

	_, err = svc.RowAccessPolicies.Insert(project, dataset, table, &bigquery.RowAccessPolicy{
		RowAccessPolicyReference: &bigquery.RowAccessPolicyReference{
			ProjectId: project, DatasetId: dataset, TableId: table, PolicyId: p2,
		},
		FilterPredicate: "region = 'US'",
	}).Do()
	require.NoError(t, err)

	got, err := svc.RowAccessPolicies.Get(project, dataset, table, p1).Do()
	require.NoError(t, err)
	require.Equal(t, "region = 'EU'", got.FilterPredicate)

	list, err := svc.RowAccessPolicies.List(project, dataset, table).Do()
	require.NoError(t, err)
	require.Len(t, list.RowAccessPolicies, 2)

	updated, err := svc.RowAccessPolicies.Update(project, dataset, table, p1, &bigquery.RowAccessPolicy{
		RowAccessPolicyReference: &bigquery.RowAccessPolicyReference{
			ProjectId: project, DatasetId: dataset, TableId: table, PolicyId: p1,
		},
		FilterPredicate: "region = 'EMEA'",
	}).Do()
	require.NoError(t, err)
	require.Equal(t, "region = 'EMEA'", updated.FilterPredicate)
	require.Equal(t, created.CreationTime, updated.CreationTime)

	// IAM verbs on a row access policy: getIamPolicy + testIamPermissions only
	// (no setIamPolicy is exposed by BigQuery for row access policies).
	resource := "projects/" + project + "/datasets/" + dataset + "/tables/" + table + "/rowAccessPolicies/" + p1
	pol, err := svc.RowAccessPolicies.GetIamPolicy(resource, &bigquery.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.NotNil(t, pol)
	perms, err := svc.RowAccessPolicies.TestIamPermissions(resource, &bigquery.TestIamPermissionsRequest{
		Permissions: []string{"bigquery.rowAccessPolicies.getIamPolicy"},
	}).Do()
	require.NoError(t, err)
	require.Equal(t, []string{"bigquery.rowAccessPolicies.getIamPolicy"}, perms.Permissions)

	// batchDelete removes the named policies.
	require.NoError(t, svc.RowAccessPolicies.BatchDelete(project, dataset, table, &bigquery.BatchDeleteRowAccessPoliciesRequest{
		PolicyIds: []string{p1, p2},
	}).Do())
	after, err := svc.RowAccessPolicies.List(project, dataset, table).Do()
	require.NoError(t, err)
	require.Empty(t, after.RowAccessPolicies)
}

func TestBigQueryProjectsAndServiceAccount(t *testing.T) {
	svc := bqService(t)
	const project, dataset = "sock-proj-list", "ds_for_proj_list"
	bqMakeDataset(t, svc, project, dataset)

	// projects.list enumerates projects with at least one dataset.
	list, err := svc.Projects.List().Do()
	require.NoError(t, err)
	var found bool
	for _, p := range list.Projects {
		if p.ProjectReference != nil && p.ProjectReference.ProjectId == project {
			found = true
		}
	}
	require.True(t, found, "expected %s in projects.list", project)

	// projects.getServiceAccount returns the project's BigQuery service account.
	sa, err := svc.Projects.GetServiceAccount(project).Do()
	require.NoError(t, err)
	require.NotEmpty(t, sa.Email)
	require.Contains(t, sa.Email, "gserviceaccount.com")
}

func TestBigQueryModels(t *testing.T) {
	svc := bqService(t)
	const project, dataset = "sock-proj", "ds_models"
	bqMakeDataset(t, svc, project, dataset)

	// Models are produced by ML query jobs, not created via REST; an empty
	// dataset lists no models, and get/patch/delete of an absent model 404s.
	list, err := svc.Models.List(project, dataset).Do()
	require.NoError(t, err)
	require.Empty(t, list.Models)

	_, err = svc.Models.Get(project, dataset, "nope").Do()
	require.Error(t, err)
}

func TestBigQueryTablesIAM(t *testing.T) {
	svc := bqService(t)
	const project, dataset, table = "sock-proj", "ds_table_iam", "secured"
	bqMakeDataset(t, svc, project, dataset)
	bqMakeTable(t, svc, project, dataset, table)

	resource := "projects/" + project + "/datasets/" + dataset + "/tables/" + table
	set, err := svc.Tables.SetIamPolicy(resource, &bigquery.SetIamPolicyRequest{
		Policy: &bigquery.Policy{
			Bindings: []*bigquery.Binding{{
				Role:    "roles/bigquery.dataViewer",
				Members: []string{"group:analysts@example.com"},
			}},
		},
	}).Do()
	require.NoError(t, err)
	require.Len(t, set.Bindings, 1)

	got, err := svc.Tables.GetIamPolicy(resource, &bigquery.GetIamPolicyRequest{}).Do()
	require.NoError(t, err)
	require.Len(t, got.Bindings, 1)

	perms, err := svc.Tables.TestIamPermissions(resource, &bigquery.TestIamPermissionsRequest{
		Permissions: []string{"bigquery.tables.get"},
	}).Do()
	require.NoError(t, err)
	require.Equal(t, []string{"bigquery.tables.get"}, perms.Permissions)
}

func TestBigQueryDatasetUndelete(t *testing.T) {
	svc := bqService(t)
	const project, dataset = "sock-proj", "ds_undelete"
	bqMakeDataset(t, svc, project, dataset)

	und, err := svc.Datasets.Undelete(project, dataset, &bigquery.UndeleteDatasetRequest{}).Do()
	require.NoError(t, err)
	require.Equal(t, dataset, und.DatasetReference.DatasetId)
}

func TestBigQueryJobCancelAndDelete(t *testing.T) {
	svc := bqService(t)
	const project, dataset, table = "sock-proj", "ds_jobs", "events"
	bqMakeDataset(t, svc, project, dataset)
	bqMakeTable(t, svc, project, dataset, table)

	// A query job completes synchronously in the sim.
	job, err := svc.Jobs.Insert(project, &bigquery.Job{
		Configuration: &bigquery.JobConfiguration{
			Query: &bigquery.JobConfigurationQuery{
				Query: "SELECT * FROM `" + dataset + "." + table + "`",
			},
		},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, job.JobReference)
	jobID := job.JobReference.JobId
	require.NotEmpty(t, jobID)

	// jobs.cancel on a finished job returns its terminal state.
	cancel, err := svc.Jobs.Cancel(project, jobID).Do()
	require.NoError(t, err)
	require.NotNil(t, cancel.Job)
	require.Equal(t, jobID, cancel.Job.JobReference.JobId)

	// jobs.delete removes the job record.
	require.NoError(t, svc.Jobs.Delete(project, jobID).Do())
	_, err = svc.Jobs.Get(project, jobID).Do()
	require.Error(t, err)
}
