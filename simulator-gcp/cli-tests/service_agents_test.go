package gcp_cli_test

import (
	"strings"
	"testing"
)

// Every identity another service names for the project carries the number
// Cloud Resource Manager holds for it, as gcloud and bq read them.
func TestServiceAgentsCLI_NamedForTheProjectNumber(t *testing.T) {
	number := strings.TrimSpace(runCLI(t, gcloudCLI("projects", "describe", project, "--format=value(projectNumber)")))
	if number == "" {
		t.Fatal("gcloud projects describe printed no project number")
	}

	var dnsProject struct {
		ID     string `json:"id"`
		Number string `json:"number"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("dns", "project-info", "describe", project, "--format=json")), &dnsProject)
	if dnsProject.ID != project || dnsProject.Number != number {
		t.Errorf("gcloud dns project-info describe = %+v, want id %s and number %s", dnsProject, project, number)
	}

	var computeProject struct {
		DefaultServiceAccount string `json:"defaultServiceAccount"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("compute", "project-info", "describe", "--format=json")), &computeProject)
	if want := number + "-compute@developer.gserviceaccount.com"; computeProject.DefaultServiceAccount != want {
		t.Errorf("Compute Engine's default service account = %q, want %q", computeProject.DefaultServiceAccount, want)
	}

	var build struct {
		ServiceAccountEmail string `json:"serviceAccountEmail"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("builds", "get-default-service-account", "--region="+location, "--format=json")), &build)
	if want := "projects/" + project + "/serviceAccounts/" + number + "@cloudbuild.gserviceaccount.com"; build.ServiceAccountEmail != want {
		t.Errorf("Cloud Build's default service account = %q, want %q", build.ServiceAccountEmail, want)
	}

	var settings struct {
		LoggingServiceAccountID string `json:"loggingServiceAccountId"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("logging", "settings", "describe", "--project="+project, "--format=json")), &settings)
	if want := "service-" + number + "@gcp-sa-logging.iam.gserviceaccount.com"; settings.LoggingServiceAccountID != want {
		t.Errorf("Cloud Logging's service agent = %q, want %q", settings.LoggingServiceAccountID, want)
	}

	encryption := strings.TrimSpace(runCLI(t, bqCLI("show", "--encryption_service_account")))
	if want := "bq-" + number + "@bigquery-encryption.iam.gserviceaccount.com"; !strings.Contains(encryption, want) {
		t.Errorf("bq show --encryption_service_account printed %q, want %q", encryption, want)
	}
}
