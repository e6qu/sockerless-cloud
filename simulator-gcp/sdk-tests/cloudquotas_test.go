package gcp_sdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	cloudquotas "cloud.google.com/go/cloudquotas/apiv1"
	"cloud.google.com/go/cloudquotas/apiv1/cloudquotaspb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// requireQuotaError checks the HTTP status and the error status a REST call
// failed with; the REST client derives its gRPC code from the HTTP status
// alone, which FAILED_PRECONDITION and INVALID_ARGUMENT share.
func requireQuotaError(t *testing.T, err error, code int, statusText string) {
	t.Helper()
	var gerr *googleapi.Error
	require.ErrorAs(t, err, &gerr)
	assert.Equal(t, code, gerr.Code, gerr.Body)
	var body struct {
		Error struct {
			Status string `json:"status"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(gerr.Body), &body), gerr.Body)
	assert.Equal(t, statusText, body.Error.Status, gerr.Body)
}

// Cloud Quotas reports Cloud Run's regional CPU quota with its default, and a
// quota preference sets the region's value: a decrease of more than 10
// percent needs its safety check waived, an increase a contact email, a
// stale etag is refused, and the QuotaInfo shows the region's value first.
func TestSDK_CloudQuotas_QuotaPreferenceLifecycle(t *testing.T) {
	t.Parallel()
	const project = "sdk-cloud-quotas"
	ensureProject(t, project)
	client := newQuotasClient(t)
	infoName := "projects/" + project + "/locations/global/services/run.googleapis.com/quotaInfos/CpuAllocPerProjectRegion"

	info, err := client.GetQuotaInfo(ctx, &cloudquotaspb.GetQuotaInfoRequest{Name: infoName})
	require.NoError(t, err)
	assert.Equal(t, "run.googleapis.com/cpu_allocation", info.GetMetric())
	assert.Equal(t, []string{"region"}, info.GetDimensions())
	assert.Equal(t, cloudquotaspb.QuotaInfo_PROJECT, info.GetContainerType())
	require.Len(t, info.GetDimensionsInfos(), 1)
	assert.Equal(t, int64(800000), info.GetDimensionsInfos()[0].GetDetails().GetValue())

	infos := client.ListQuotaInfos(ctx, &cloudquotaspb.ListQuotaInfosRequest{
		Parent: "projects/" + project + "/locations/global/services/run.googleapis.com",
	})
	listed, err := infos.Next()
	require.NoError(t, err)
	assert.Equal(t, "CpuAllocPerProjectRegion", listed.GetQuotaId())

	parent := "projects/" + project + "/locations/global"
	request := func(value int64, ignore ...cloudquotaspb.QuotaSafetyCheck) *cloudquotaspb.CreateQuotaPreferenceRequest {
		return &cloudquotaspb.CreateQuotaPreferenceRequest{
			Parent:            parent,
			QuotaPreferenceId: "run-cpu-europe-north1",
			QuotaPreference: &cloudquotaspb.QuotaPreference{
				Service:       "run.googleapis.com",
				QuotaId:       "CpuAllocPerProjectRegion",
				Dimensions:    map[string]string{"region": "europe-north1"},
				QuotaConfig:   &cloudquotaspb.QuotaConfig{PreferredValue: value},
				Justification: "bound the region's Cloud Run spend",
			},
			IgnoreSafetyChecks: ignore,
		}
	}
	_, err = client.CreateQuotaPreference(ctx, request(600000))
	require.Error(t, err)
	requireQuotaError(t, err, http.StatusBadRequest, "FAILED_PRECONDITION")

	pref, err := client.CreateQuotaPreference(ctx, request(600000, cloudquotaspb.QuotaSafetyCheck_QUOTA_DECREASE_PERCENTAGE_TOO_HIGH))
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(pref.GetName(), "/locations/global/quotaPreferences/run-cpu-europe-north1"), pref.GetName())
	assert.Equal(t, int64(600000), pref.GetQuotaConfig().GetGrantedValue().GetValue())
	assert.False(t, pref.GetReconciling())
	assert.Empty(t, pref.GetQuotaConfig().GetTraceId(), "a decrease has no trace id")

	_, err = client.CreateQuotaPreference(ctx, request(600000))
	requireQuotaError(t, err, http.StatusConflict, "ALREADY_EXISTS")

	got, err := client.GetQuotaPreference(ctx, &cloudquotaspb.GetQuotaPreferenceRequest{Name: pref.GetName()})
	require.NoError(t, err)
	assert.Equal(t, pref.GetEtag(), got.GetEtag())
	assert.Equal(t, "bound the region's Cloud Run spend", got.GetJustification())

	update := func(email, etag string) (*cloudquotaspb.QuotaPreference, error) {
		return client.UpdateQuotaPreference(ctx, &cloudquotaspb.UpdateQuotaPreferenceRequest{
			QuotaPreference: &cloudquotaspb.QuotaPreference{
				Name:         pref.GetName(),
				QuotaConfig:  &cloudquotaspb.QuotaConfig{PreferredValue: 900000},
				ContactEmail: email,
				Etag:         etag,
			},
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"quota_config", "contact_email"}},
		})
	}
	_, err = update("", "")
	requireQuotaError(t, err, http.StatusBadRequest, "INVALID_ARGUMENT")
	_, err = update("quota-owner@example.com", "stale")
	requireQuotaError(t, err, http.StatusConflict, "ABORTED")
	raised, err := update("quota-owner@example.com", got.GetEtag())
	require.NoError(t, err)
	assert.Equal(t, int64(900000), raised.GetQuotaConfig().GetGrantedValue().GetValue())
	assert.NotEmpty(t, raised.GetQuotaConfig().GetTraceId(), "an increase carries a trace id")
	assert.Equal(t, "europe-north1", raised.GetDimensions()["region"])

	it := client.ListQuotaPreferences(ctx, &cloudquotaspb.ListQuotaPreferencesRequest{
		Parent: parent,
		Filter: `quotaId="CpuAllocPerProjectRegion" AND reconciling=false`,
	})
	var names []string
	for {
		p, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		names = append(names, p.GetName())
	}
	assert.Equal(t, []string{pref.GetName()}, names)

	info, err = client.GetQuotaInfo(ctx, &cloudquotaspb.GetQuotaInfoRequest{Name: infoName})
	require.NoError(t, err)
	require.Len(t, info.GetDimensionsInfos(), 2)
	assert.Equal(t, map[string]string{"region": "europe-north1"}, info.GetDimensionsInfos()[0].GetDimensions())
	assert.Equal(t, int64(900000), info.GetDimensionsInfos()[0].GetDetails().GetValue())
	assert.Equal(t, int64(800000), info.GetDimensionsInfos()[1].GetDetails().GetValue())

	_, err = client.CreateQuotaPreference(ctx, &cloudquotaspb.CreateQuotaPreferenceRequest{
		Parent: parent,
		QuotaPreference: &cloudquotaspb.QuotaPreference{
			Service: "run.googleapis.com", QuotaId: "NoSuchQuota",
			QuotaConfig: &cloudquotaspb.QuotaConfig{PreferredValue: 1},
		},
	})
	requireQuotaError(t, err, http.StatusBadRequest, "INVALID_ARGUMENT")
	_, err = client.GetQuotaInfo(ctx, &cloudquotaspb.GetQuotaInfoRequest{
		Name: "projects/" + project + "/locations/global/services/run.googleapis.com/quotaInfos/NoSuchQuota",
	})
	requireQuotaError(t, err, http.StatusNotFound, "NOT_FOUND")
}

// The quota adjuster and the folder-, organization- and Google Distributed
// Cloud-level resources answer the simulator's declared 501, on both API
// versions.
func TestSDK_CloudQuotas_UnservedResourcesAreDeclared(t *testing.T) {
	t.Parallel()
	adjuster, err := cloudquotas.NewQuotaAdjusterSettingsManagerRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	defer adjuster.Close()
	_, err = adjuster.GetQuotaAdjusterSettings(ctx, &cloudquotaspb.GetQuotaAdjusterSettingsRequest{
		Name: "projects/test-project/locations/global/quotaAdjusterSettings",
	})
	requireQuotaError(t, err, http.StatusNotImplemented, "UNIMPLEMENTED")

	folder := strings.TrimPrefix(createTestFolder(t, "sdk-cloud-quotas"), "folders/")
	for _, call := range []struct{ method, path string }{
		{http.MethodGet, fmt.Sprintf("/v1/folders/%s/locations/global/quotaPreferences", folder)},
		{http.MethodPost, fmt.Sprintf("/v1/folders/%s/locations/global/quotaPreferences", folder)},
		{http.MethodPatch, fmt.Sprintf("/v1/folders/%s/locations/global/quotaPreferences/p", folder)},
		{http.MethodGet, "/v1/organizations/123456789012/locations/global/services/run.googleapis.com/quotaInfos"},
		{http.MethodPost, "/v1/organizations/123456789012/locations/global/quotaPreferences"},
		{http.MethodPatch, "/v1/organizations/123456789012/locations/global/quotaPreferences/p"},
		{http.MethodGet, "/v1/projects/test-project/locations/global/quotaAdjusterSettings"},
		{http.MethodPatch, "/v1/projects/test-project/locations/global/quotaAdjusterSettings"},
		{http.MethodGet, "/v1/projects/test-project/locations/global/gdcZones/z/gdcServices/s/gdcQuotaInfos"},
		{http.MethodGet, fmt.Sprintf("/v1beta/folders/%s/locations/global/quotaPreferences", folder)},
		{http.MethodPost, fmt.Sprintf("/v1beta/folders/%s/locations/global/quotaPreferences", folder)},
		{http.MethodPatch, fmt.Sprintf("/v1beta/folders/%s/locations/global/quotaPreferences/p", folder)},
		{http.MethodGet, "/v1beta/organizations/123456789012/locations/global/services/run.googleapis.com/quotaInfos"},
		{http.MethodPost, "/v1beta/organizations/123456789012/locations/global/quotaPreferences"},
		{http.MethodPatch, "/v1beta/organizations/123456789012/locations/global/quotaPreferences/p"},
		{http.MethodGet, "/v1beta/projects/test-project/locations/global/quotaAdjusterSettings"},
		{http.MethodPatch, "/v1beta/projects/test-project/locations/global/quotaAdjusterSettings"},
		{http.MethodGet, "/v1beta/projects/test-project/locations/global/gdcZones/z/gdcServices/s/gdcQuotaInfos"},
	} {
		code, body := quotasRawCall(t, call.method, call.path, "{}")
		assert.Equal(t, http.StatusNotImplemented, code, "%s %s: %s", call.method, call.path, body)
	}
}

func quotasRawCall(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(out)
}

// Every project route of both API versions answers through the REST paths a
// client sends.
func TestSDK_CloudQuotas_BothVersionsServeProjectRoutes(t *testing.T) {
	t.Parallel()
	const project = "sdk-cloud-quotas-beta"
	ensureProject(t, project)
	create := `{"service":"run.googleapis.com","quotaId":"CpuAllocPerProjectRegion","dimensions":{"region":"asia-east1"},"quotaConfig":{"preferredValue":"800000"}}`
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPost, fmt.Sprintf("/v1/projects/%s/locations/global/quotaPreferences?quotaPreferenceId=v1", project), create},
		{http.MethodGet, fmt.Sprintf("/v1/projects/%s/locations/global/quotaPreferences", project), ""},
		{http.MethodGet, fmt.Sprintf("/v1/projects/%s/locations/global/quotaPreferences/v1", project), ""},
		{http.MethodPatch, fmt.Sprintf("/v1/projects/%s/locations/global/quotaPreferences/v1?updateMask=justification", project), `{"justification":"steady"}`},
		{http.MethodGet, fmt.Sprintf("/v1/projects/%s/locations/global/services/run.googleapis.com/quotaInfos", project), ""},
		{http.MethodGet, fmt.Sprintf("/v1/projects/%s/locations/global/services/run.googleapis.com/quotaInfos/CpuAllocPerProjectRegion", project), ""},
		{http.MethodPost, fmt.Sprintf("/v1beta/projects/%s/locations/global/quotaPreferences?quotaPreferenceId=v1beta", project), create},
		{http.MethodGet, fmt.Sprintf("/v1beta/projects/%s/locations/global/quotaPreferences", project), ""},
		{http.MethodGet, fmt.Sprintf("/v1beta/projects/%s/locations/global/quotaPreferences/v1beta", project), ""},
		{http.MethodPatch, fmt.Sprintf("/v1beta/projects/%s/locations/global/quotaPreferences/v1beta?updateMask=justification", project), `{"justification":"steady"}`},
		{http.MethodGet, fmt.Sprintf("/v1beta/projects/%s/locations/global/services/run.googleapis.com/quotaInfos", project), ""},
		{http.MethodGet, fmt.Sprintf("/v1beta/projects/%s/locations/global/services/run.googleapis.com/quotaInfos/CpuAllocPerProjectRegion", project), ""},
	} {
		code, body := quotasRawCall(t, call.method, call.path, call.body)
		assert.Equal(t, http.StatusOK, code, "%s %s: %s", call.method, call.path, body)
	}
}
