package gcp_cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serviceURL(service string) string {
	return fmt.Sprintf("%s/v1/projects/%s/services/%s", baseURL, project, service)
}

func TestServiceUsage_EnableAndList(t *testing.T) {
	t.Parallel()
	var never struct {
		State string `json:"state"`
	}
	parseJSON(t, httpDoJSON(t, "GET", serviceURL("cli-never-enabled.googleapis.com"), ""), &never)
	assert.Equal(t, "DISABLED", never.State, "a service the project never enabled is DISABLED")

	enableURL := serviceURL("compute.googleapis.com:enable")
	httpDoJSON(t, "POST", enableURL, `{}`)

	getURL := serviceURL("compute.googleapis.com")
	out := httpDoJSON(t, "GET", getURL, "")

	var svc struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	parseJSON(t, out, &svc)
	assert.Equal(t, "ENABLED", svc.State)

	listURL := fmt.Sprintf("%s/v1/projects/%s/services", baseURL, project)
	out = httpDoJSON(t, "GET", listURL, "")

	var result struct {
		Services []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"services"`
	}
	parseJSON(t, out, &result)
	assert.NotEmpty(t, result.Services)
}

func TestServiceUsage_ListPagination(t *testing.T) {
	t.Parallel()
	svcs := []string{"page-a.googleapis.com", "page-b.googleapis.com", "page-c.googleapis.com"}
	for _, s := range svcs {
		httpDoJSON(t, "POST", serviceURL(s+":enable"), `{}`)
	}

	listBase := fmt.Sprintf("%s/v1/projects/%s/services", baseURL, project)
	token := ""
	pages := 0
	// Record every page's entries and fail on a repeat: a handler that returned
	// the same service on every page would satisfy a count.
	seen := map[string]bool{}
	for {
		url := listBase + "?pageSize=1"
		if token != "" {
			url += "&pageToken=" + token
		}
		out := httpDoJSON(t, "GET", url, "")
		var result struct {
			Services []struct {
				Name string `json:"name"`
			} `json:"services"`
			NextPageToken string `json:"nextPageToken"`
		}
		parseJSON(t, out, &result)
		require.LessOrEqual(t, len(result.Services), 1, "pageSize=1 must cap the page")
		for _, s := range result.Services {
			require.False(t, seen[s.Name], "service %q was paged out twice", s.Name)
			seen[s.Name] = true
		}
		pages++
		require.Less(t, pages, 200, "pagination must terminate")
		token = result.NextPageToken
		if token == "" {
			break
		}
	}
	for _, s := range svcs {
		assert.True(t, seen[fmt.Sprintf("projects/%s/services/%s", project, s)],
			"service %s must appear across the pages", s)
	}
	assert.Greater(t, pages, 1, "with pageSize=1 and multiple services there must be multiple pages")
}

func TestServiceUsage_Disable(t *testing.T) {
	t.Parallel()
	// The harness enabled the services the suite drives, so this test uses its own.
	const service = "cli-disable.googleapis.com"
	httpDoJSON(t, "POST", serviceURL(service+":enable"), `{}`)
	httpDoJSON(t, "POST", serviceURL(service+":disable"), `{}`)

	getURL := serviceURL(service)
	out := httpDoJSON(t, "GET", getURL, "")

	var svc struct {
		State string `json:"state"`
	}
	parseJSON(t, out, &svc)
	assert.Equal(t, "DISABLED", svc.State)
}

// TestServiceUsage_CLI_EnableAndList drives `gcloud services enable`, which
// waits on the operation Service Usage answers, and `gcloud services list`,
// which lists the project's enabled services.
func TestServiceUsage_CLI_EnableAndList(t *testing.T) {
	t.Parallel()
	const service = "cli-enable.googleapis.com"
	runCLI(t, gcloudCLI("services", "enable", service))

	out := runCLI(t, gcloudCLI("services", "list", "--format=json"))
	var listed []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	parseJSON(t, out, &listed)
	found := false
	for _, s := range listed {
		assert.Equal(t, "ENABLED", s.State, "gcloud services list lists enabled services only: %s", s.Name)
		if strings.HasSuffix(s.Name, "/services/"+service) {
			found = true
		}
	}
	assert.Truef(t, found, "gcloud services list did not list %s: %s", service, out)
}
