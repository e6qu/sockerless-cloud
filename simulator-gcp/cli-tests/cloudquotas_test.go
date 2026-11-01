package gcp_cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliQuotaPreference struct {
	Name        string            `json:"name"`
	Service     string            `json:"service"`
	QuotaID     string            `json:"quotaId"`
	Dimensions  map[string]string `json:"dimensions"`
	Reconciling bool              `json:"reconciling"`
	QuotaConfig struct {
		PreferredValue string `json:"preferredValue"`
		GrantedValue   string `json:"grantedValue"`
		TraceID        string `json:"traceId"`
	} `json:"quotaConfig"`
}

// gcloud beta quotas reads Cloud Run's regional CPU quota and sets a region's
// value through a quota preference: an increase with a contact email, a
// decrease within 10 percent as asked, and a larger one only with
// --allow-high-percentage-quota-decrease. The preference names a region no
// other test deploys to, so it bounds nothing the suite runs.
func TestCloudQuotasCLI_QuotaPreferenceForCloudRunCPU(t *testing.T) {
	t.Parallel()
	const id = "cli-run-cpu-europe-north1"
	quotaFlags := []string{"--service=run.googleapis.com", "--quota-id=CpuAllocPerProjectRegion",
		"--dimensions=region=europe-north1", "--project=" + project, "--format=json"}

	var info struct {
		QuotaID         string   `json:"quotaId"`
		Metric          string   `json:"metric"`
		Dimensions      []string `json:"dimensions"`
		DimensionsInfos []struct {
			Details struct {
				Value string `json:"value"`
			} `json:"details"`
		} `json:"dimensionsInfos"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("beta", "quotas", "info", "describe", "CpuAllocPerProjectRegion",
		"--service=run.googleapis.com", "--project="+project, "--format=json")), &info)
	assert.Equal(t, "run.googleapis.com/cpu_allocation", info.Metric)
	assert.Equal(t, []string{"region"}, info.Dimensions)
	require.NotEmpty(t, info.DimensionsInfos)

	var created cliQuotaPreference
	parseJSON(t, runCLI(t, gcloudCLI(append([]string{"beta", "quotas", "preferences", "create",
		"--preference-id=" + id, "--preferred-value=900000", "--email=quota-owner@example.com"}, quotaFlags...)...)), &created)
	assert.True(t, strings.HasSuffix(created.Name, "/locations/global/quotaPreferences/"+id), created.Name)
	assert.Equal(t, "900000", created.QuotaConfig.GrantedValue)
	assert.NotEmpty(t, created.QuotaConfig.TraceID, "an increase carries a trace id")

	var lowered cliQuotaPreference
	parseJSON(t, runCLI(t, gcloudCLI(append([]string{"beta", "quotas", "preferences", "update", id,
		"--preferred-value=850000"}, quotaFlags...)...)), &lowered)
	assert.Equal(t, "850000", lowered.QuotaConfig.GrantedValue)

	out, err := gcloudCLI(append([]string{"beta", "quotas", "preferences", "update", id,
		"--preferred-value=100000"}, quotaFlags...)...).CombinedOutput()
	require.Error(t, err, "a decrease of more than 10 percent without its flag: %s", out)
	assert.Contains(t, string(out), "more than 10 percent")

	var cut cliQuotaPreference
	parseJSON(t, runCLI(t, gcloudCLI(append([]string{"beta", "quotas", "preferences", "update", id,
		"--preferred-value=100000", "--allow-high-percentage-quota-decrease"}, quotaFlags...)...)), &cut)
	assert.Equal(t, "100000", cut.QuotaConfig.GrantedValue)

	var described cliQuotaPreference
	parseJSON(t, runCLI(t, gcloudCLI("beta", "quotas", "preferences", "describe", id,
		"--project="+project, "--format=json")), &described)
	assert.Equal(t, "100000", described.QuotaConfig.PreferredValue)
	assert.Equal(t, map[string]string{"region": "europe-north1"}, described.Dimensions)

	// gcloud's ListQuotaPreferences prints its --page-size, "None" when unset,
	// to stdout ahead of the list (api_lib/quotas/quota_preference.py).
	listOut := runCLI(t, gcloudCLI("beta", "quotas", "preferences", "list", "--project="+project, "--format=json"))
	listOut = strings.TrimPrefix(listOut, "None\n")
	var listed []cliQuotaPreference
	parseJSON(t, listOut, &listed)
	var names []string
	for _, p := range listed {
		names = append(names, p.Name)
	}
	assert.Contains(t, names, described.Name)

	// The v1 surface reads back what gcloud's v1beta calls wrote.
	var v1 cliQuotaPreference
	parseJSON(t, httpDoJSON(t, "GET", fmt.Sprintf("%s/v1/projects/%s/locations/global/quotaPreferences/%s", baseURL, project, id), ""), &v1)
	assert.Equal(t, "100000", v1.QuotaConfig.GrantedValue)
	var regionInfo struct {
		DimensionsInfos []struct {
			Dimensions map[string]string `json:"dimensions"`
			Details    struct {
				Value string `json:"value"`
			} `json:"details"`
		} `json:"dimensionsInfos"`
	}
	parseJSON(t, httpDoJSON(t, "GET", fmt.Sprintf("%s/v1beta/projects/%s/locations/global/services/run.googleapis.com/quotaInfos/CpuAllocPerProjectRegion", baseURL, project), ""), &regionInfo)
	require.NotEmpty(t, regionInfo.DimensionsInfos)
	assert.Equal(t, "europe-north1", regionInfo.DimensionsInfos[0].Dimensions["region"])
	assert.Equal(t, "100000", regionInfo.DimensionsInfos[0].Details.Value)
}
