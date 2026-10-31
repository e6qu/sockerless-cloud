package gcp_tf_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

type tfQuotaPreference struct {
	Dimensions  map[string]string `json:"dimensions"`
	QuotaConfig struct {
		PreferredValue string `json:"preferredValue"`
		GrantedValue   string `json:"grantedValue"`
	} `json:"quotaConfig"`
	Justification string `json:"justification"`
}

func tfQuotaPreferenceAt(t *testing.T) tfQuotaPreference {
	t.Helper()
	var pref tfQuotaPreference
	require.NoError(t, json.Unmarshal(simCall(t, "GET", "/v1/projects/test-project/locations/global/quotaPreferences/tf-run-cpu-asia-northeast1", ""), &pref))
	return pref
}

// TestTerraformCloudQuotasQuotaPreference sets Cloud Run's regional CPU quota
// through google_cloud_quotas_quota_preference, reads it back without drift,
// and raises it in place; Cloud Quotas grants each value and its QuotaInfo
// carries the region's.
func TestTerraformCloudQuotasQuotaPreference(t *testing.T) {
	const fixture = "cloud-quotas"
	outputs := applyFixture(t, fixture)
	require.Equal(t, "tf-run-cpu-asia-northeast1", outputs.must(t, "name"))
	require.Equal(t, "1000000", outputs.must(t, "granted_value"))
	require.Equal(t, false, outputs.mustValue(t, "reconciling"))
	requireNoDrift(t, fixture)

	pref := tfQuotaPreferenceAt(t)
	require.Equal(t, map[string]string{"region": "asia-northeast1"}, pref.Dimensions)
	require.Equal(t, "room for the region's Cloud Run instances", pref.Justification)

	out, err := runTimed(t, "terraform apply "+fixture+" (raise)", terraformCmdInDir("fixtures/"+fixture,
		"apply", "-auto-approve", "-var=preferred_value=1200000"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)
	require.Equal(t, "1200000", tfQuotaPreferenceAt(t).QuotaConfig.GrantedValue)

	var info struct {
		DimensionsInfos []struct {
			Dimensions map[string]string `json:"dimensions"`
			Details    struct {
				Value string `json:"value"`
			} `json:"details"`
		} `json:"dimensionsInfos"`
	}
	require.NoError(t, json.Unmarshal(simCall(t, "GET",
		"/v1/projects/test-project/locations/global/services/run.googleapis.com/quotaInfos/CpuAllocPerProjectRegion", ""), &info))
	require.NotEmpty(t, info.DimensionsInfos)
	require.Equal(t, "asia-northeast1", info.DimensionsInfos[0].Dimensions["region"])
	require.Equal(t, "1200000", info.DimensionsInfos[0].Details.Value)
}
