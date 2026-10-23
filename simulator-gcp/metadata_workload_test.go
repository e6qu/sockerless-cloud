package main

import (
	"net/http"
	"strings"
	"testing"
)

// An instance reads its project's ID and number from Cloud Resource Manager
// and runs as the project's Compute Engine default service account, which IAM
// holds; a caller that is no workload reaches no metadata server.
func TestMetadataServesTheInstanceItsProjectFromCloudResourceManager(t *testing.T) {
	get := metadataTreeServer(t)
	number := gcpTestProjectNumber(t)
	want := map[string]string{
		"/computeMetadata/v1/project/project-id":                                              gcpTestProjectID,
		"/computeMetadata/v1/project/numeric-project-id":                                      number,
		"/computeMetadata/v1/instance/service-accounts/default/email":                         number + "-compute@developer.gserviceaccount.com",
		"/computeMetadata/v1/instance/zone":                                                   "projects/" + number + "/zones/us-central1-a",
		"/computeMetadata/v1/instance/name":                                                   "metadata-reader",
		"/computeMetadata/v1/instance/id":                                                     "4815162342",
		"/computeMetadata/v1/instance/service-accounts/other@x.iam.gserviceaccount.com/email": "",
	}
	for path, value := range want {
		rec := get(path)
		if value == "" {
			if rec.Code != http.StatusNotFound {
				t.Fatalf("GET %s = %d, want 404 for an account the workload does not run as", path, rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusOK || rec.Body.String() != value {
			t.Fatalf("GET %s = %d %q, want %q", path, rec.Code, rec.Body.String(), value)
		}
	}
	if _, ok := iamServiceAccounts.Get("projects/" + gcpTestProjectID + "/serviceAccounts/" + number + "-compute@developer.gserviceaccount.com"); !ok {
		t.Fatal("IAM holds no Compute Engine default service account for the instance's project")
	}

	gcpMetadataInstancesByIP.Delete(metadataReaderAddress)
	for _, path := range []string{
		"/computeMetadata/v1/project/project-id",
		"/computeMetadata/v1/instance/service-accounts/default/token",
		"/computeMetadata/v1/instance/service-accounts/",
	} {
		if rec := get(path); rec.Code != http.StatusNotFound || rec.Header().Get("Metadata-Flavor") != "" {
			t.Fatalf("GET %s from outside every workload = %d %v, want a 404 from no metadata server", path, rec.Code, rec.Header())
		}
	}
}

// A Cloud Run container reads the account its revision or resource runs as,
// or its project's Compute Engine default service account when that names
// none.
func TestMetadataPlacesCloudRunWorkloads(t *testing.T) {
	metadataTreeServer(t)
	const revision = "projects/test-project/locations/us-central1/services/metadata-svc/revisions/metadata-svc-00001-x7k"
	crv2Revisions.Put(revision, RevisionV2{Name: revision, ServiceAccount: "runner@test-project.iam.gserviceaccount.com"})
	const execution = "projects/test-project/locations/us-central1/jobs/metadata-job/executions/metadata-job-abc"
	crjExecutions.Put(execution, Execution{Name: execution, Template: &TaskTemplate{}})

	for _, tc := range []struct {
		labels  map[string]string
		project string
		account string
	}{
		{map[string]string{cloudRunResourceLabel: revision}, "test-project", "runner@test-project.iam.gserviceaccount.com"},
		{map[string]string{cloudRunTaskExecutionLabel: execution}, "test-project", gcpTestProjectNumber(t) + "-compute@developer.gserviceaccount.com"},
	} {
		w, ok := cloudRunWorkloadFromLabels(tc.labels)
		if !ok {
			t.Fatalf("labels %v placed no workload", tc.labels)
		}
		account, ok := w.defaultServiceAccount()
		if w.projectID() != tc.project || !ok || account != tc.account {
			t.Fatalf("labels %v placed %s running as %q, want %s running as %s", tc.labels, w.projectID(), account, tc.project, tc.account)
		}
	}
	if _, ok := cloudRunWorkloadFromLabels(map[string]string{"sockerless-sim": "true"}); ok {
		t.Fatal("a container no Cloud Run resource owns placed a workload")
	}
}

// IAM resolves the Compute Engine default service account through the -
// project wildcard by the project number its email carries.
func TestComputeDefaultServiceAccountThroughTheWildcard(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	got := gcpHostOK(t, srv, "iam.googleapis.com", http.MethodGet,
		"/v1/projects/-/serviceAccounts/"+gcpTestProjectNumber(t)+"-compute@developer.gserviceaccount.com", "")
	if got["projectId"] != "test-project" || got["displayName"] != "Compute Engine default service account" ||
		!strings.HasPrefix(got["name"].(string), "projects/test-project/serviceAccounts/") {
		t.Fatalf("default service account %v", got)
	}
}
