package gcp_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func serviceURL(service string) string {
	return fmt.Sprintf("%s/v1/projects/%s/services/%s", baseURL, project, service)
}

func TestServiceUsage_EnableAndList(t *testing.T) {
	// Enable a service
	enableURL := serviceURL("compute.googleapis.com:enable")
	httpDoJSON(t, "POST", enableURL, `{}`)

	// Get service to verify it's enabled
	getURL := serviceURL("compute.googleapis.com")
	out := httpDoJSON(t, "GET", getURL, "")

	var svc struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	parseJSON(t, out, &svc)
	assert.Equal(t, "ENABLED", svc.State)

	// List services
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

// TestServiceUsage_ListPagination asserts the ListServices handler honors
// pageSize + pageToken rather than returning the whole set.
func TestServiceUsage_ListPagination(t *testing.T) {
	svcs := []string{"page-a.googleapis.com", "page-b.googleapis.com", "page-c.googleapis.com"}
	for _, s := range svcs {
		httpDoJSON(t, "POST", serviceURL(s+":enable"), `{}`)
	}

	listBase := fmt.Sprintf("%s/v1/projects/%s/services", baseURL, project)
	token := ""
	pages := 0
	total := 0
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
		assert.LessOrEqual(t, len(result.Services), 1, "pageSize=1 must cap the page")
		total += len(result.Services)
		pages++
		if pages > 200 {
			t.Fatal("pagination did not terminate")
		}
		token = result.NextPageToken
		if token == "" {
			break
		}
	}
	assert.GreaterOrEqual(t, total, len(svcs), "all enabled services must appear across pages")
	assert.Greater(t, pages, 1, "with pageSize=1 and multiple services there must be multiple pages")
}

func TestServiceUsage_Disable(t *testing.T) {
	// Enable first
	enableURL := serviceURL("storage.googleapis.com:enable")
	httpDoJSON(t, "POST", enableURL, `{}`)

	// Disable
	disableURL := serviceURL("storage.googleapis.com:disable")
	httpDoJSON(t, "POST", disableURL, `{}`)

	// Verify it's disabled
	getURL := serviceURL("storage.googleapis.com")
	out := httpDoJSON(t, "GET", getURL, "")

	var svc struct {
		State string `json:"state"`
	}
	parseJSON(t, out, &svc)
	assert.Equal(t, "DISABLED", svc.State)
}
